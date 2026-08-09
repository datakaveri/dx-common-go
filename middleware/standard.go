package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/datakaveri/dx-common-go/transport/clientip"
	"go.uber.org/zap"
)

// StandardStack applies the common CDPG middleware stack to a chi.Router:
// RequestID → RealIP → Logger → CORS → Compression → Recoverer → Timeout.
//
// Use this instead of wiring each middleware individually to keep all Go
// services consistent.
func StandardStack(logger *zap.Logger, timeout time.Duration) func(chi.Router) {
	return func(r chi.Router) {
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

// StandardGin is the gin equivalent of StandardStack, applying the same
// RequestID → RealIP → Logger → CORS → Compression → Recoverer → Timeout
// stack via Wrap. Use r.Use(StandardGin(logger, timeout)...) on a gin.Engine.
func StandardGin(logger *zap.Logger, timeout time.Duration) []gin.HandlerFunc {
	return []gin.HandlerFunc{
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
	}
}

// MetricsHandler returns a simple handler for the /metrics endpoint that can
// be plugged into any chi router. If handler is nil, it returns a 200 OK stub.
func MetricsHandler(handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if handler != nil {
			handler.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# no metrics registered\n"))
	}
}
