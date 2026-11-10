package scheduler

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// This file carries the minimal span plumbing a scheduled run needs. It reads
// OTel's global TracerProvider (configured by observability.Init) and is a
// no-op until one is set.

const tracerName = "github.com/datakaveri/dx-common-go/scheduler"

// startJobSpan begins a span for ONE logical job run. It is a NEW ROOT: a
// scheduled tick has no inbound trace context, and — the point the review makes
// — a long-lived worker must be one span PER RUN, never one span for the whole
// process lifetime. No-op until observability.Init runs.
func startJobSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, "job "+name,
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("dx.job.name", name)),
	)
}

// endJobSpan records a failed run on the span and ends it, so a job failure is
// a failed trace and correlates with scheduler_failures_total.
func endJobSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
