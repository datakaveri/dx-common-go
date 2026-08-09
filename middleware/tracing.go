package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/datakaveri/dx-common-go/transport/clientip"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

// Option configures the Standard composite.
type Option func(*standardConfig)

type standardConfig struct {
	tracing bool
}

// WithTracing enables OTel HTTP-server instrumentation (otelhttp) as the
// outermost middleware, reading from whatever TracerProvider
// observability.Init configured. A no-op wrapper when Init hasn't run (or
// ran with no endpoint configured) — OTel's default global TracerProvider is
// itself a no-op, so this is always safe to enable.
func WithTracing() Option {
	return func(c *standardConfig) { c.tracing = true }
}

// Standard is the options-based successor to StandardStack — additive:
// StandardStack is untouched and keeps working for every existing consumer.
// New cross-cutting capabilities (tracing today) are opt-in via Option
// rather than widening StandardStack's fixed signature.
func Standard(logger *zap.Logger, timeout time.Duration, opts ...Option) func(chi.Router) {
	cfg := &standardConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(r chi.Router) {
		if cfg.tracing {
			r.Use(otelhttp.NewMiddleware("http.server"))
		}
		r.Use(RequestID())
		// Capture BEFORE RealIP: RealIP overwrites RemoteAddr from a
		// client-supplied header, and transport/clientip needs the real
		// transport peer as its fallback or that fallback is forgeable too
		// (ROADMAP P1-4).
		r.Use(clientip.Capture)
		//nolint:staticcheck // SA1019: deprecated for the spoofing described above; its output is for LOGS, not decisions — use transport/clientip for those
		r.Use(chimw.RealIP)
		r.Use(Logger(logger))
		r.Use(CORS(DefaultCORSConfig()))
		r.Use(Compression())
		r.Use(chimw.Recoverer)
		r.Use(chimw.Timeout(timeout))
	}
}

// Gin is the gin equivalent of Standard, applying the same optional-tracing
// stack via Wrap. Use r.Use(Gin(logger, timeout, opts...)...) on a gin.Engine.
func Gin(logger *zap.Logger, timeout time.Duration, opts ...Option) []gin.HandlerFunc {
	cfg := &standardConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	stack := make([]gin.HandlerFunc, 0, 7)
	if cfg.tracing {
		stack = append(stack, Wrap(otelhttp.NewMiddleware("http.server")))
	}
	stack = append(stack,
		Wrap(RequestID()),
		// Capture BEFORE RealIP: RealIP overwrites RemoteAddr from a
		// client-supplied header, and transport/clientip needs the real
		// transport peer as its fallback or that fallback is forgeable too
		// (ROADMAP P1-4).
		Wrap(clientip.Capture),
		//nolint:staticcheck // SA1019: deprecated for the spoofing described above; its output is for LOGS, not decisions — use transport/clientip for those
		Wrap(chimw.RealIP),
		Wrap(Logger(logger)),
		Wrap(CORS(DefaultCORSConfig())),
		Wrap(Compression()),
		Wrap(chimw.Recoverer),
		Wrap(chimw.Timeout(timeout)),
	)
	return stack
}
