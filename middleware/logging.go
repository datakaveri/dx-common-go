package middleware

import (
	"net/http"
	"time"

	"go.uber.org/zap"
)

// responseWriter is a minimal wrapper that captures the status code written by
// the downstream handler so we can log it after the response completes.
type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Logger returns a chi-compatible middleware that logs each request using zap.
// Fields logged: method, path, status code, response time, bytes, requestID.
func Logger(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rw, r)

			logger.Info("request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rw.status),
				zap.Duration("duration", time.Since(start)),
				zap.Int("bytes", rw.bytes),
				zap.String("request_id", RequestIDFromCtx(r.Context())),
				zap.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}
