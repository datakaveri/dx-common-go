package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// ETagFor computes a strong entity tag for a response body.
func ETagFor(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatch reports whether an If-None-Match header value matches the given
// entity tag, using weak comparison (RFC 9110 §8.8.3.2): the W/ prefix is
// ignored on both sides.
func etagMatch(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	if strings.TrimSpace(ifNoneMatch) == "*" {
		return true
	}
	strip := func(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "W/") }
	target := strip(etag)
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if strip(candidate) == target {
			return true
		}
	}
	return false
}

// NotModified sets the ETag header and, when the request's If-None-Match
// matches, writes 304 Not Modified and returns true — the caller must then
// skip writing the body. Use this from handlers that produce (or cache) the
// full response body and can compute its tag cheaply.
func NotModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	w.Header().Set("ETag", etag)
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// NotModifiedSince sets the Last-Modified header and, when the request's
// If-Modified-Since is at or after the given time, writes 304 Not Modified
// and returns true. Timestamps are compared at second precision (HTTP dates
// carry no sub-second component).
func NotModifiedSince(w http.ResponseWriter, r *http.Request, lastModified time.Time) bool {
	lastModified = lastModified.UTC().Truncate(time.Second)
	w.Header().Set("Last-Modified", lastModified.Format(http.TimeFormat))
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil && !lastModified.After(t) {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

// Conditional returns middleware that adds strong ETags to small successful
// GET/HEAD responses and serves 304 Not Modified on If-None-Match hits.
//
// Responses are buffered up to maxBytes to compute the tag; a response that
// grows past the cap (or a non-200, or an explicit ETag set by the handler)
// is passed through untouched, so streaming handlers are unaffected. Handlers
// that stream or cache bodies themselves should call NotModified /
// NotModifiedSince directly instead of relying on this middleware.
func Conditional(maxBytes int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			cw := &conditionalWriter{ResponseWriter: w, max: maxBytes, status: http.StatusOK}
			next.ServeHTTP(cw, r)
			cw.finish(r)
		})
	}
}

// conditionalWriter buffers a response until it either completes (compute
// ETag, maybe 304) or overflows/decides to pass through (flush and stream).
type conditionalWriter struct {
	http.ResponseWriter
	max         int
	status      int
	buf         bytes.Buffer
	passthrough bool
	headerSent  bool
}

func (c *conditionalWriter) WriteHeader(status int) {
	c.status = status
	// Only 200s are tagged; anything else streams through unchanged. An
	// explicit handler-set ETag also disables buffering (handler owns it).
	if status != http.StatusOK || c.Header().Get("ETag") != "" {
		c.startPassthrough()
	}
}

func (c *conditionalWriter) Write(b []byte) (int, error) {
	if c.passthrough {
		return c.ResponseWriter.Write(b)
	}
	if c.buf.Len()+len(b) > c.max {
		c.startPassthrough()
		return c.ResponseWriter.Write(b)
	}
	return c.buf.Write(b)
}

// Flush is honoured by switching to passthrough: a handler that flushes is
// streaming and must not be buffered.
func (c *conditionalWriter) Flush() {
	c.startPassthrough()
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *conditionalWriter) startPassthrough() {
	if c.passthrough {
		return
	}
	c.passthrough = true
	if !c.headerSent {
		c.headerSent = true
		c.ResponseWriter.WriteHeader(c.status)
	}
	if c.buf.Len() > 0 {
		_, _ = c.ResponseWriter.Write(c.buf.Bytes())
		c.buf.Reset()
	}
}

// finish emits the buffered response with its ETag, or a 304 on a match.
func (c *conditionalWriter) finish(r *http.Request) {
	if c.passthrough {
		return
	}
	etag := ETagFor(c.buf.Bytes())
	c.Header().Set("ETag", etag)
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		c.ResponseWriter.WriteHeader(http.StatusNotModified)
		return
	}
	c.ResponseWriter.WriteHeader(c.status)
	_, _ = c.ResponseWriter.Write(c.buf.Bytes())
}
