package server

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/datakaveri/dx-common-go/auth"
	dxgrpc "github.com/datakaveri/dx-common-go/platform/grpc"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// subjectInterceptor reads the X-Subject-* metadata and puts the user on the
// context, mirroring what auth/resolver does for HTTP.
//
// ORDERING IS THE CONTROL. The headers are unsigned (ROADMAP P0-17 stage 2), so
// this interceptor performs no authentication and cannot. It runs AFTER
// workloadInterceptor, which verifies the calling workload's credential and
// rejects a caller that is not on the subject_asserters list. Reordering these
// two — or installing this one alone — would trust whatever the caller put in
// the metadata, so the chain in server.go is not a stylistic choice.
//
// A call asserting no subject is legitimate: a service acting on its own behalf
// has no user, and the handler decides whether it needs one.
func subjectInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		user, err := dxheaders.Parse(dxgrpc.MDToHeader(md))
		switch {
		case err == nil:
			// Belt and braces against a future reordering: a subject is only
			// honoured behind a verified workload, exactly as on the HTTP path.
			if _, verified := workload.From(ctx); !verified {
				return nil, status.Error(codes.Unauthenticated,
					"subject metadata requires a verified workload caller")
			}
			return handler(auth.WithUser(ctx, user), req)
		case errors.Is(err, dxheaders.ErrNoSubject):
			return handler(ctx, req)
		default:
			return nil, status.Error(codes.Unauthenticated, "invalid subject metadata")
		}
	}
}

// workloadInterceptor authenticates the CALLING SERVICE (ADR-06).
//
// It mirrors platform/security/workload.Middleware: same Verifier, same
// Enforcement semantics, same metric. A separate policy here would mean the
// P0-2 rollout had to be performed twice and could be half-applied.
func workloadInterceptor(v *workload.Verifier, log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		h := dxgrpc.MDToHeader(md)

		p, err := v.VerifyRequest(&http.Request{Header: h})
		if err != nil {
			// There is no fallback and no permissive rung. A verifier either
			// enforces or does not exist (ROADMAP P0-17 stage 1), and with the
			// shared HMAC gone there is nothing left to degrade to.
			log.Warn("workload verification failed",
				zap.String("method", info.FullMethod), zap.Error(err))
			return nil, status.Error(codes.Unauthenticated, "workload verification failed")
		}
		// A verified caller may only assert a subject if it is on the asserter
		// list. The right to call is not the right to impersonate.
		if dxheaders.Asserts(h) && !v.MayAssertSubject(p) {
			return nil, status.Errorf(codes.PermissionDenied,
				"workload %q may not assert a subject", p.ID)
		}
		return handler(workload.With(ctx, p), req)
	}
}

// recoveryInterceptor turns a panic into an error.
//
// Outermost in the chain: without it one nil dereference in any handler or
// later interceptor takes the whole process down, and a gRPC server hosts every
// internal call a service serves.
func recoveryInterceptor(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in grpc handler",
					zap.String("method", info.FullMethod),
					zap.Any("panic", r),
					zap.ByteString("stack", debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
				resp = nil
			}
		}()
		return handler(ctx, req)
	}
}

// loggingInterceptor records one line per call at debug, and errors at warn.
func loggingInterceptor(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		fields := []zap.Field{
			zap.String("method", info.FullMethod),
			zap.Duration("took", time.Since(start)),
			zap.String("code", status.Code(err).String()),
		}
		if err != nil {
			log.Warn("grpc call failed", append(fields, zap.Error(err))...)
		} else {
			log.Debug("grpc call", fields...)
		}
		return resp, err
	}
}
