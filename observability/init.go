package observability

import (
	"context"
	"fmt"
	"os"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
)

var initOnce sync.Once

// Init wires a TracerProvider and text-map propagator onto OTel's global
// state, or does nothing when no endpoint is configured (cfg.Endpoint and
// OTEL_EXPORTER_OTLP_ENDPOINT both empty) — services call this
// unconditionally at startup, with zero risk in environments that don't run
// a collector. The returned shutdown flushes and closes the exporter; call
// it during graceful shutdown (deferred right after Init).
//
// Safe to call more than once per process — only the first call takes
// effect, guarding against double-init silently clobbering the global
// TracerProvider a second time. Later calls return a no-op shutdown.
func Init(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }

	var initErr error
	initialized := false
	initOnce.Do(func() {
		initialized = true
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		}
		if endpoint == "" {
			shutdown = noop
			return
		}

		exporter, exportErr := otlptracegrpc.New(ctx, exporterOptions(endpoint, cfg)...)
		if exportErr != nil {
			initErr = fmt.Errorf("observability.Init: create OTLP exporter: %w", exportErr)
			shutdown = noop
			return
		}

		res, resErr := newResource(ctx, cfg)
		if resErr != nil {
			initErr = fmt.Errorf("observability.Init: build resource: %w", resErr)
			shutdown = noop
			return
		}

		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(newSampler(cfg)),
		)
		otel.SetTracerProvider(tp)
		// TraceContext only, Baggage deliberately omitted: OTel Baggage has no
		// integrity guarantee and propagates to downstream (incl. third-party)
		// calls, so it is off until an allowlisted, boundary-filtered use case
		// is approved (OBSERVABILITY.md §5.2 / review P1-17).
		otel.SetTextMapPropagator(propagation.TraceContext{})
		shutdown = tp.Shutdown
	})

	if !initialized {
		// A later call in the same process: report success with a no-op
		// shutdown rather than silently re-running (and re-clobbering) SDK
		// setup — the first call already owns the shutdown lifecycle.
		return noop, nil
	}
	if shutdown == nil {
		shutdown = noop
	}
	return shutdown, initErr
}

// exporterOptions builds the OTLP/gRPC exporter options. Insecure by default
// (the node-local collector), TLS when cfg.Secure, plus any per-request headers
// a managed collector needs.
func exporterOptions(endpoint string, cfg Config) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
	if !cfg.Secure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}
	return opts
}

// newResource builds the OTel resource from the service identity. Only
// non-empty values are attached, so a build that omits a version or environment
// does not report an empty string for it. Host and Kubernetes attributes are
// added downstream by the Collector, not here (review P1-7), which also avoids
// the resource-detector schema-URL merge conflicts.
func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.Version != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.Version))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentName(cfg.Environment))
	}
	// WithFromEnv picks up OTEL_RESOURCE_ATTRIBUTES when an operator sets it,
	// without assuming it carries Kubernetes identity.
	return resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(),
	)
}

// newSampler maps SampleRatio onto a sampler. See Config.SampleRatio for the
// semantics: an unset/zero ratio samples everything (the pilot default), and a
// remote parent's decision is always honoured.
func newSampler(cfg Config) sdktrace.Sampler {
	if cfg.SampleRatio <= 0 || cfg.SampleRatio >= 1 {
		return sdktrace.AlwaysSample()
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))
}
