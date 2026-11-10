package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Wrap adapts a standard net/http middleware (func(http.Handler) http.Handler)
// into a gin.HandlerFunc, so the existing CDPG middleware collection
// (RequestID, Logger, CORS, Compression, Recovery, Timeout, and chi's
// dependency-free Recoverer/RealIP) can be reused unchanged under gin.
//
// # The writer the middleware hands down is the one the rest of the chain uses
//
// A net/http middleware may replace the ResponseWriter it passes to next — the
// access Logger does, to capture status and size; otelhttp does, to record the
// response status on the span. This adapter used to ignore that writer and
// resume the gin chain on the ORIGINAL c.Writer, so every downstream write
// bypassed the middleware's wrapper: the gateway's access log recorded 200 / 0
// bytes for every request — including 401s and 502s — and spans never saw a
// status. Now the replacement writer is installed as c.Writer for the rest of
// the chain and the original restored afterwards.
func Wrap(mw func(http.Handler) http.Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		original := c.Writer
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c.Request = r
			if w != http.ResponseWriter(original) {
				// Start from gin's current status: on gin's own 404/405 path
				// the status is already set before the chain runs, and a
				// default of 200 here would overwrite it.
				gw := &ginWriter{ResponseWriter: original, w: w, status: original.Status(), size: -1}
				c.Writer = gw
				defer func() {
					// gin delays WriteHeader until the first write; a handler
					// that only set a status (c.Status(204)) must still have it
					// sent through the middleware's writer, not later by gin on
					// the original one behind the middleware's back. Only a
					// status set downstream is flushed — otherwise gin finishes
					// the response itself (its 404/405 bodies).
					if gw.statusSet {
						gw.WriteHeaderNow()
					}
					c.Writer = original
				}()
			}
			c.Next()
		})
		mw(next).ServeHTTP(original, c.Request)
	}
}

// ginWriter presents a net/http ResponseWriter (installed by a middleware) as a
// gin.ResponseWriter. Writes, headers and flushes go to w; capabilities gin
// needs that a plain writer lacks (Hijack, CloseNotify, Pusher) come from the
// embedded original gin writer.
type ginWriter struct {
	gin.ResponseWriter
	w           http.ResponseWriter
	status      int
	size        int
	wroteHeader bool
	statusSet   bool
}

func (g *ginWriter) Header() http.Header { return g.w.Header() }

// WriteHeader records the status; like gin, the header is sent on first write.
func (g *ginWriter) WriteHeader(code int) {
	if code > 0 && !g.wroteHeader {
		g.status = code
		g.statusSet = true
	}
}

func (g *ginWriter) WriteHeaderNow() {
	if !g.wroteHeader {
		g.wroteHeader = true
		g.size = 0
		g.w.WriteHeader(g.status)
	}
}

func (g *ginWriter) Write(b []byte) (int, error) {
	g.WriteHeaderNow()
	n, err := g.w.Write(b)
	g.size += n
	return n, err
}

func (g *ginWriter) WriteString(s string) (int, error) { return g.Write([]byte(s)) }

func (g *ginWriter) Status() int   { return g.status }
func (g *ginWriter) Size() int     { return g.size }
func (g *ginWriter) Written() bool { return g.wroteHeader }

// Flush sends the header if needed and flushes through w — required for SSE
// (the gateway's flush routes) to stream rather than sit in a buffer.
func (g *ginWriter) Flush() {
	g.WriteHeaderNow()
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes w to http.ResponseController.
func (g *ginWriter) Unwrap() http.ResponseWriter { return g.w }
