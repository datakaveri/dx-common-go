package httpx

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/transport/clientip"
)

// This file is the standard middleware stack every service gets from
// NewRouter.
//
// # Why it exists
//
// It should have existed from the first migration. The gin services installed
// `dxmw.Gin(logger, 30*time.Second, WithTracing())` — tracing, request id, real
// ip, request logging, CORS, compression, recovery and a timeout — and
// NewRouter replaced that with recovery ALONE. So every service that adopted
// the platform router silently lost six of the eight, and the router's own doc
// comment claimed tracing was always-on while nothing installed it. Nine
// services ran with no request log and no trace.
//
// That is exactly the failure the platform exists to prevent, arriving through
// the platform itself: a default that must be remembered per service is a
// default that will be forgotten. So the stack is not optional and there is no
// WithTracing() to omit.
//
// # Global vs per-route
//
// Recovery, tracing, request id, real ip, logging and CORS are GLOBAL — they
// apply to health probes and /metrics too, because "why is readiness flapping"
// is answered by the same request log as everything else.
//
// The timeout and compression are PER-ROUTE, and that split is the whole
// reason this is not one r.Use() chain: both are actively harmful to a
// streaming response. A context deadline kills an SSE stream mid-flight, and a
// buffering compressor holds events until the buffer fills, which is
// indistinguishable from a hung agent. Routes marked Streaming() skip both.

// DefaultTimeout bounds a non-streaming request.
//
// It matches what the gin stack used, so migrating a service does not change
// how long a slow upstream is tolerated.
const DefaultTimeout = 30 * time.Second

// DefaultMaxBodyBytes caps a request body when a service does not choose its
// own limit. 1 MiB is ample for the JSON these APIs accept and small enough
// that an unbounded body cannot exhaust memory. A service that must accept
// larger bodies — GeoJSON in dx-dataplane-ogc-go, uploads in
// dx-files-connect-api-go — raises RouterSpec.MaxBodyBytes, and one that
// streams and bounds its own upload sets it negative to opt out.
const DefaultMaxBodyBytes = 1 << 20 // 1 MiB

// compressionLevel is gzip's default. Higher costs CPU on every response for
// single-digit percentage gains on the JSON these services return.
const compressionLevel = 5

// CORSConfig configures the CORS middleware.
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	// MaxAge is the pre-flight cache lifetime in seconds.
	MaxAge int
}

// DefaultCORS mirrors the legacy default the gin services ran, so migrating
// does not silently change which origins a browser may call from.
func DefaultCORS() CORSConfig {
	return CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		MaxAge:         86400,
	}
}

// globalStack is the middleware every request passes, in order.
//
// Recovery is installed by NewRouter BEFORE this, so a panic in any of these
// is caught too.
func globalStack(log *zap.Logger, cors CORSConfig) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		// Tracing outermost of the observability group, so the span covers the
		// whole request including the time spent in the rest of the stack.
		otelhttp.NewMiddleware("http.server"),
		requestID,
		// Capture BEFORE RealIP, and the order is load-bearing. RealIP
		// overwrites RemoteAddr with a value taken from a client-supplied
		// header, destroying the one address that cannot be forged; Capture
		// stashes the real transport peer first so anything making a SECURITY
		// decision (rate-limit keys, audit attribution) has something
		// trustworthy to fall back to. Swap these two and transport/clientip's
		// fallback silently becomes attacker-controlled (ROADMAP P1-4).
		clientip.Capture,
		// RealIP rewrites RemoteAddr from X-Forwarded-For / X-Real-IP. Every
		// service here sits behind the gateway, so without it the request log
		// records the gateway's address for every caller.
		//
		// It takes the LEFTMOST X-Forwarded-For entry, which is the one a
		// remote client can write, so its output is fit for a log line and not
		// for a decision. Use transport/clientip for the latter.
		//nolint:staticcheck // SA1019: deprecated for exactly the spoofing described above; kept for logs, see clientip
		middleware.RealIP,
		requestLogger(log),
		corsMiddleware(cors),
	}
}

// RequestIDHeader carries the correlation id in and out.
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// requestID honours an inbound correlation id, generates one when absent, puts
// it on the context and ECHOES it on the response.
//
// chi's own RequestID is not used: it neither honours the inbound header
// reliably nor sets a response header. Both matter here. Every service sits
// behind dx-gateway, so the id has to survive the gateway→service hop to join
// the two log lines, and echoing it is what lets a client quote the id of the
// request that failed.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFrom returns the correlation id for this request, or "" outside the
// stack. Include it when logging from a handler so the line joins the request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestLogger logs one line per completed request.
//
// It records the request id so a line can be joined to a trace, and the status
// and duration so a latency question is answerable without a tracing backend
// being reachable.
func requestLogger(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			log.Info("request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rec.status),
				zap.Duration("duration", time.Since(start)),
				zap.Int("bytes", rec.bytes),
				zap.String("request_id", RequestIDFrom(r.Context())),
				zap.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}

// statusRecorder captures the status and size for the request log.
//
// It forwards Flush so a streaming handler still reaches the client
// incrementally — without it, wrapping the writer would turn SSE into a
// response delivered all at once at the end, which is the same bug as
// compressing it.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer, so a
// handler can set a write deadline on a long-lived stream.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// corsMiddleware adds CORS headers and answers pre-flight requests.
func corsMiddleware(cfg CORSConfig) func(http.Handler) http.Handler {
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)
	allowAll := len(cfg.AllowedOrigins) == 1 && cfg.AllowedOrigins[0] == "*"

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" {
				switch {
				case allowAll:
					w.Header().Set("Access-Control-Allow-Origin", "*")
				case originAllowed(cfg.AllowedOrigins, origin):
					w.Header().Set("Access-Control-Allow-Origin", origin)
					// Vary, because the response now depends on the request's
					// Origin — without it a shared cache serves one origin's
					// allow header to another.
					w.Header().Add("Vary", "Origin")
				}
				w.Header().Set("Access-Control-Allow-Methods", methods)
				w.Header().Set("Access-Control-Allow-Headers", headers)
				w.Header().Set("Access-Control-Max-Age", maxAge)
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(allowed []string, origin string) bool {
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}

// perRoute wraps a route handler with the middleware that must NOT apply to a
// streaming route: the request timeout and response compression.
func perRoute(h http.HandlerFunc, streaming bool, timeout time.Duration) http.HandlerFunc {
	if streaming {
		return h
	}
	var wrapped http.Handler = h
	wrapped = middleware.Compress(compressionLevel)(wrapped)
	if timeout > 0 {
		wrapped = middleware.Timeout(timeout)(wrapped)
	}
	return wrapped.ServeHTTP
}

// resolveTimeout maps the RouterSpec value onto an effective duration: zero
// takes the default, negative disables.
//
// Negative-means-off rather than zero-means-off because zero is what a struct
// literal that forgot the field produces, and "forgot to set it" must not be
// the way a service ends up with no request timeout at all.
func resolveTimeout(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return DefaultTimeout
	case d < 0:
		return 0
	default:
		return d
	}
}

// resolveMaxBodyBytes maps the RouterSpec value onto an effective cap: zero
// takes the default, negative disables it. Mirrors resolveTimeout, and for the
// same reason — zero is what a struct literal that omitted the field produces,
// and "forgot to set it" must not be how a service ends up with no body limit
// at all.
func resolveMaxBodyBytes(n int64) int64 {
	switch {
	case n == 0:
		return DefaultMaxBodyBytes
	case n < 0:
		return 0 // disabled — unlimited
	default:
		return n
	}
}

// carryMaxBody records the effective request-body cap on the context so the
// JSON binder and BodyReader enforce the operator-configured limit.
//
// It does NOT wrap r.Body. A blanket MaxBytesReader at the default would
// truncate the services that legitimately read larger bodies themselves —
// dx-files-connect-api-go streams 64 MiB uploads, dx-dataplane-ogc-go accepts
// multi-megabyte GeoJSON — before their own bound ever ran. Enforcement is at
// the point of the read instead: decodeBody for the JSON path, BodyReader for
// a custom Binder, each reading this limit.
func carryMaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(withMaxBodyBytes(r.Context(), n)))
		})
	}
}
