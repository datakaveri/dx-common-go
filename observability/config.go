// Package observability owns the OpenTelemetry SDK lifecycle for a service:
// one Init call wires a TracerProvider + propagators, or does nothing at all
// when no OTLP endpoint is configured. No framework tracing abstraction is
// built on top — instrumentation happens at each driver's own seam
// (middleware.WithTracing for HTTP, postgres.WithTracers for pgx, …), all
// reading spans from the same global TracerProvider this package sets.
package observability

// Config controls OTel SDK initialization. The zero value is a fully valid,
// no-op configuration — Init(ctx, Config{}) is always safe to call
// unconditionally, regardless of whether tracing is configured for the
// current environment.
type Config struct {
	// ServiceName becomes the resource's service.name attribute. Required
	// for a live SDK; ignored in no-op mode.
	ServiceName string
	// Version becomes the resource's service.version attribute. Empty leaves
	// the attribute unset rather than sending an empty string, so a build that
	// does not stamp a version is distinguishable from one reporting "".
	Version string
	// Environment becomes deployment.environment.name (dev/staging/prod). Empty
	// leaves it unset, same reasoning as Version. Kubernetes pod identity
	// (k8s.pod.name, namespace, node) is added by the Collector's k8sattributes
	// processor, not here — see OBSERVABILITY.md §5.2 / review P1-7.
	Environment string
	// Endpoint is the OTLP/gRPC collector address (host:port). Empty means
	// "read OTEL_EXPORTER_OTLP_ENDPOINT instead" (the standard OTel env var);
	// both empty means no-op mode — Init constructs no SDK, starts no
	// goroutines, and leaves the global TracerProvider as OTel's default
	// no-op implementation.
	Endpoint string
	// SampleRatio is the head-sampling probability applied to a root span with
	// no parent decision. Its semantics are deliberately conservative:
	//
	//	≤ 0 or ≥ 1  → AlwaysSample (record and export every trace). This is the
	//	              pilot default: head sampling cannot retain "all errors"
	//	              because the decision precedes the outcome, so the platform
	//	              samples at the Collector's tail instead (OBSERVABILITY.md
	//	              §9.1 / review P0-1). An unset value therefore keeps every
	//	              trace rather than silently dropping traffic.
	//	0 < r < 1   → ParentBased(TraceIDRatioBased(r)) for the lower-cost path
	//	              that abandons the 100%-error guarantee.
	//
	// A remote parent's decision is always honoured (ParentBased); the gateway
	// owns the trust policy for remote sampled flags (review P1-16).
	SampleRatio float64
	// Secure selects TLS for the OTLP/gRPC exporter. The zero value (false)
	// keeps the insecure transport the in-cluster node-local collector expects
	// and the fleet has always used; set it true to dial a TLS-terminating
	// collector (review P1-9).
	Secure bool
	// Headers are attached to every OTLP export request — the auth a managed
	// collector needs. Empty for the in-cluster collector, which is the norm.
	Headers map[string]string
}
