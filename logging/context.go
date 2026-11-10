package logging

import (
	"context"

	"go.uber.org/zap"
)

type ctxKey struct{}

// Into returns a context carrying l, so code deeper in the call chain can log
// through the SAME logger the entry point already enriched with trace and
// request fields — without every function signature growing a *zap.Logger.
//
// The platform HTTP stack calls this once per request with a logger bound to
// the request id and trace/span ids; a handler then logs through From(ctx) and
// every line joins the request and its trace automatically.
func Into(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the logger stored by Into, or a no-op logger when none is set,
// so a caller never needs a nil check. Enrich at the boundary (Into), read here.
func From(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok {
		return l
	}
	return zap.NewNop()
}

// FromOr returns the logger stored by Into, or fallback when none is set.
//
// Service code that runs both inside a request and outside one (a scheduled
// job, a queue consumer) uses it with its own logger as the fallback: inside a
// request the line carries request_id and trace_id, outside it still logs.
func FromOr(ctx context.Context, fallback *zap.Logger) *zap.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok {
		return l
	}
	return fallback
}
