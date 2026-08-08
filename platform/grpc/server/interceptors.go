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

// subjectInterceptor verifies the signed X-Subject-* metadata and puts the user
// on the context, exactly as transport/headers.Middleware does for HTTP.
//
// An empty secret disables verification (local dev). It does NOT fall back to
// trusting unsigned metadata when a secret IS configured: that would make the
// signature advisory, which is the downgrade the HTTP path already refuses.
func subjectInterceptor(cfg dxheaders.Config) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if len(cfg.Secret) == 0 {
			return handler(ctx, req)
		}
		md, _ := metadata.FromIncomingContext(ctx)
		user, err := dxheaders.Verify(dxgrpc.MDToHeader(md), cfg)
		switch {
		case err == nil:
			return handler(auth.WithUser(ctx, user), req)
		case errors.Is(err, dxheaders.ErrNotSigned):
			// No subject asserted at all. That is legitimate for a call made by
			// a service on its own behalf, so it is not an error here — the
			// handler decides whether it needs a user. What is NOT allowed is
			// an invalid signature, below.
			return handler(ctx, req)
		default:
			return nil, status.Error(codes.Unauthenticated, "invalid subject signature")
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
		switch {
		case err == nil:
			// A verified caller may only assert a subject if it is on the
			// asserter list. The right to call is not the right to impersonate.
			if assertsSubject(h) && !v.MayAssertSubject(p) {
				return nil, status.Errorf(codes.PermissionDenied,
					"workload %q may not assert a subject", p.ID)
			}
			return handler(workload.With(ctx, p), req)

		case v.Enforcement() == workload.Required:
			log.Warn("workload verification failed",
				zap.String("method", info.FullMethod), zap.Error(err))
			return nil, status.Error(codes.Unauthenticated, "workload verification failed")

		default:
			// permissive/disabled: the legacy HMAC still carries the call.
			// Deliberately NOT an error — that is what makes a staged rollout
			// possible, and it is why enforcement must reach `required` before
			// the shared secret is removed.
			return handler(ctx, req)
		}
	}
}

// assertsSubject reports whether the caller is claiming to speak for a user.
func assertsSubject(h http.Header) bool {
	return h.Get(dxheaders.HdrSubjectID) != ""
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
