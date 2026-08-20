package httpx

import (
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RED (rate, errors, duration) metrics for the HTTP server, shared by every
// service NewRouter builds. They exist so availability and latency alerting runs
// on Prometheus rather than on log queries, which are expensive, delayed and the
// wrong tool for an SLO (review P1-13).
//
// Registered once per process on the default registry (served by
// dx-common-go/metrics.Handler), guarded by sync.Once so a second NewRouter — a
// second service in one test binary — does not panic on duplicate registration.
var (
	redOnce     sync.Once
	redRequests *prometheus.CounterVec
	redDuration *prometheus.HistogramVec
	redInFlight prometheus.Gauge
)

func registerREDMetrics() {
	redOnce.Do(func() {
		redRequests = promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "http_server_requests_total",
			Help: "HTTP server requests by method, matched route template and status class.",
		}, []string{"method", "route", "status"})
		redDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_server_request_duration_seconds",
			Help:    "HTTP server request latency by method and matched route template.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"})
		redInFlight = promauto.NewGauge(prometheus.GaugeOpts{
			Name: "http_server_in_flight_requests",
			Help: "In-flight HTTP server requests.",
		})
	})
}

// redMetrics records the RED metrics with BOUNDED labels: the matched chi route
// TEMPLATE (never the raw path) and the status CLASS (2xx/4xx/…), so a path
// parameter or a unique id can never turn one endpoint into unbounded time
// series (review P1-18). Duration excludes nothing the server does, so it is
// outermost of the observation middlewares.
func redMetrics(next http.Handler) http.Handler {
	registerREDMetrics()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redInFlight.Inc()
		defer redInFlight.Dec()

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := routeLabel(r)
		redRequests.WithLabelValues(r.Method, route, statusClass(rec.status)).Inc()
		redDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// routeLabel is the matched route template (e.g. "/items/{id}"), or "unmatched"
// for a 404 — NEVER the raw path, which would make every id its own series.
func routeLabel(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

// statusClass buckets a status code into 1xx…5xx, bounding the label to a
// handful of values instead of one per code.
func statusClass(code int) string {
	switch {
	case code < 200:
		return "1xx"
	case code < 300:
		return "2xx"
	case code < 400:
		return "3xx"
	case code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
