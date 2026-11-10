package logging

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TraceFields returns the trace-correlation fields for the active span on ctx —
// trace_id and span_id — or nil when there is no recording span (before
// observability.Init has run, or outside a traced request).
//
// Include them on a log line so it joins the trace in the log backend: the
// single query that turns an alert into the exact failing request is
// `trace_id: <id>`. It reads OTel's span context, so it is a no-op cost when
// tracing is off — the returned nil adds no fields.
func TraceFields(ctx context.Context) []zap.Field {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []zap.Field{
		zap.String("trace_id", sc.TraceID().String()),
		zap.String("span_id", sc.SpanID().String()),
	}
}
