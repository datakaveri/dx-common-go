package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout returns a middleware that cancels the request context after duration.
// If the handler has not written a response by the deadline, the middleware
// writes a 503 Service Unavailable.
func Timeout(duration time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), duration)
			defer cancel()

			done := make(chan struct{})
			tw := &timeoutWriter{ResponseWriter: w}

			go func() {
				next.ServeHTTP(tw, r.WithContext(ctx))
				close(done)
			}()

			select {
			case <-done:
				// Handler completed within the deadline — flush any buffered response.
				tw.flush(w)
			case <-ctx.Done():
				// Timed out before the handler finished.
				tw.timedOut = true
				http.Error(w, "request timeout", http.StatusServiceUnavailable)
			}
		})
	}
}

// timeoutWriter buffers writes until the handler goroutine finishes so we can
// detect a timeout before sending a partial response.
type timeoutWriter struct {
	http.ResponseWriter
	buf      []byte
	code     int
	headers  http.Header
	timedOut bool
}

func (tw *timeoutWriter) WriteHeader(code int) {
	if tw.timedOut {
		return
	}
	tw.code = code
}

func (tw *timeoutWriter) Write(b []byte) (int, error) {
	if tw.timedOut {
		return 0, nil
	}
	tw.buf = append(tw.buf, b...)
	return len(b), nil
}

func (tw *timeoutWriter) Header() http.Header {
	if tw.headers == nil {
		tw.headers = make(http.Header)
	}
	return tw.headers
}

// flush copies the buffered response to the real ResponseWriter.
func (tw *timeoutWriter) flush(w http.ResponseWriter) {
	if tw.timedOut {
		return
	}
	for k, vs := range tw.headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if tw.code != 0 {
		w.WriteHeader(tw.code)
	}
	if len(tw.buf) > 0 {
		_, _ = w.Write(tw.buf)
	}
}
