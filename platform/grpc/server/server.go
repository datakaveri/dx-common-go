// Package server runs a gRPC server for internal service-to-service calls.
//
// Internal calls between Go services are gRPC (platform owner, 2026-08-08).
// Before this package no Go service in the fleet ran a gRPC server at all, and
// platform/grpc held only status mapping — so the transport existed on the
// client side (grpc/client.Dial) with nothing to dial.
//
// The design constraint that shapes everything here: identity must NOT fork.
// Subject identity (transport/headers) and workload identity
// (platform/security/workload) were built as HTTP concepts, and a gRPC surface
// that grew its own versions would mean two canonical strings, two signers and
// two enforcement policies that drift apart. The interceptors instead reuse
// those packages verbatim, projecting gRPC metadata into an http.Header — so
// one signer serves both transports for as long as the migration takes, and
// turning workload enforcement on covers both at once.
//
// Layer: L2. Only bootstrap imports it.
package server

import (
	"context"
	"fmt"
	"net"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	"github.com/datakaveri/dx-common-go/platform/security/workload"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// Config is a gRPC server's settings.
type Config struct {
	// Port is the listen port. Zero means the service declares no gRPC surface
	// and no socket is opened.
	Port int `mapstructure:"port"`

	// MaxRecvMsgSize caps an inbound message. Zero uses gRPC's own 4 MiB
	// default; this exists so a service can LOWER it, since an uncapped
	// internal endpoint is the memory-exhaustion vector SECURITY-REVIEW flags
	// for HTTP bodies, arriving by another door.
	MaxRecvMsgSize int `mapstructure:"max_recv_msg_size"`

	// ShutdownTimeout bounds the graceful stop before connections are dropped.
	// Zero uses defaultShutdownTimeout. Unbounded, one stuck call hangs a
	// rollout — the same defect platform/bootstrap fixed for HTTP.
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
}

const (
	defaultMaxRecvMsgSize  = 4 << 20
	defaultShutdownTimeout = 20 * time.Second
)

// Registrar registers a service implementation. Services pass one of these
// rather than touching grpc.NewServer, so the interceptor chain cannot be
// bypassed, reordered or partially adopted per service.
type Registrar func(*grpc.Server)

// Options are the collaborators the interceptors need. They are passed in
// rather than built here so bootstrap shares exactly the objects the HTTP side
// uses — one Verifier, one secret, one logger, one enforcement setting.
type Options struct {
	Log *zap.Logger

	// Workload verifies WHICH SERVICE is calling (ADR-06). Nil disables the
	// check, which is the shipped default until P0-2 stage 3.
	Workload *workload.Verifier

	// InternalAuth verifies the X-Subject-* metadata identifying WHICH USER the
	// caller speaks for. An empty Secret disables subject verification, which
	// is the local-dev case only.
	InternalAuth dxheaders.Config

	// Interceptors are appended AFTER the platform's own, so a service may add
	// behaviour but cannot displace identity verification.
	Interceptors []grpc.UnaryServerInterceptor
}

// Server wraps a *grpc.Server with its listen and shutdown policy.
type Server struct {
	grpc     *grpc.Server
	health   *health.Server
	log      *zap.Logger
	port     int
	shutdown time.Duration

	addr string
}

// New builds the server and registers services on it. It does not listen —
// Serve does, so a construction failure is separable from a bind failure.
func New(cfg Config, opts Options, register ...Registrar) (*Server, error) {
	if cfg.Port <= 0 {
		return nil, fmt.Errorf("grpc server: port is required")
	}
	log := opts.Log
	if log == nil {
		log = zap.NewNop()
	}

	maxRecv := cfg.MaxRecvMsgSize
	if maxRecv <= 0 {
		maxRecv = defaultMaxRecvMsgSize
	}
	shutdown := cfg.ShutdownTimeout
	if shutdown <= 0 {
		shutdown = defaultShutdownTimeout
	}

	// Order is fixed, not configurable. Recovery outermost so a panic anywhere
	// later becomes an error instead of killing the process. Then workload
	// (which SERVICE is calling), then subject (which USER it speaks for) —
	// the same order as the HTTP chain, where the workload gate wraps the
	// subject resolver precisely so it runs first: resolving the subject before
	// deciding whether to trust the caller would mean trusting the headers in
	// order to decide whether to trust them.
	chain := []grpc.UnaryServerInterceptor{
		recoveryInterceptor(log),
		loggingInterceptor(log),
	}
	if opts.Workload != nil {
		chain = append(chain, workloadInterceptor(opts.Workload, log))
	}
	chain = append(chain, subjectInterceptor(opts.InternalAuth))
	chain = append(chain, opts.Interceptors...)

	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRecv),
		grpc.ChainUnaryInterceptor(chain...),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 5 * time.Minute,
			Time:              2 * time.Minute,
			Timeout:           20 * time.Second,
		}),
	)

	for _, r := range register {
		if r != nil {
			r(srv)
		}
	}

	// grpc.health.v1, registered here rather than per service: a health service
	// nobody remembered to add reads healthy to a load balancer while serving
	// nothing.
	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)

	return &Server{grpc: srv, health: hs, log: log, port: cfg.Port, shutdown: shutdown}, nil
}

// Addr is the bound address, valid once Serve has listened. Tests use it; a
// port of 0 is not accepted, so this is only ever the configured port.
func (s *Server) Addr() string { return s.addr }

// Serve listens and serves until ctx is cancelled, then stops gracefully within
// ShutdownTimeout and forcibly after it.
func (s *Server) Serve(ctx context.Context) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("grpc server: listen :%d: %w", s.port, err)
	}
	return s.ServeListener(ctx, lis)
}

// ServeListener serves on an existing listener. Tests use it with bufconn; it
// is the same path Serve takes, so what tests exercise is what runs.
func (s *Server) ServeListener(ctx context.Context, lis net.Listener) error {
	s.addr = lis.Addr().String()
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	s.log.Info("grpc server starting", zap.String("addr", s.addr))

	errc := make(chan error, 1)
	go func() { errc <- s.grpc.Serve(lis) }()

	select {
	case err := <-errc:
		if err != nil {
			return fmt.Errorf("grpc server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	// Report NOT_SERVING before draining, so a probe sees the shutdown before
	// the socket closes rather than after.
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	done := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(done) }()
	select {
	case <-done:
		s.log.Info("grpc server stopped")
	case <-time.After(s.shutdown):
		s.log.Warn("grpc graceful stop timed out; forcing", zap.Duration("timeout", s.shutdown))
		s.grpc.Stop()
	}
	<-errc
	return nil
}
