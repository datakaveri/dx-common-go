package executor

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/datakaveri/dx-common-go/platform/executor"

// startTaskSpan begins a span for one detached task and LINKS it to the
// initiating request carried on reqCtx.
//
// It is a NEW ROOT, not a child of the request span: the task outlives the
// request (that is the whole reason this package exists), so a child span would
// be held open long after the request's trace ended, or be cut when the request
// context is cancelled. A link preserves the causal navigation — request trace →
// detached work and back — without that lifetime coupling. No-op until
// observability.Init sets a provider.
func startTaskSpan(taskCtx, reqCtx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(taskCtx, "task "+name,
		trace.WithNewRoot(),
		trace.WithLinks(trace.LinkFromContext(reqCtx)),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("dx.task.name", name)),
	)
}
