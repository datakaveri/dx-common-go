package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// gzipResponseWriter wraps http.ResponseWriter to gzip responses
type gzipResponseWriter struct {
	http.ResponseWriter
	gw *gzip.Writer
}

// Write writes gzipped data
func (grw *gzipResponseWriter) Write(b []byte) (int, error) {
	return grw.gw.Write(b)
}

// Close closes the gzip writer
func (grw *gzipResponseWriter) Close() error {
	return grw.gw.Close()
}

// Compression returns middleware that gzips responses for clients that support it
func Compression() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if client accepts gzip encoding
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}

			// Create gzip writer
			gw := gzip.NewWriter(w)
			defer gw.Close()

			// Wrap response writer
			grw := &gzipResponseWriter{ResponseWriter: w, gw: gw}
			grw.Header().Set("Content-Encoding", "gzip")
			grw.Header().Del("Content-Length") // Remove content-length since we're compressing

			next.ServeHTTP(grw, r)
		})
	}
}

// SelectiveCompression returns middleware that only compresses certain content types
func SelectiveCompression(contentTypesToCompress ...string) func(http.Handler) http.Handler {
	typesToCompress := make(map[string]bool)
	for _, ct := range contentTypesToCompress {
		typesToCompress[ct] = true
	}

	// Default types if none specified
	if len(typesToCompress) == 0 {
		typesToCompress["application/json"] = true
		typesToCompress["text/html"] = true
		typesToCompress["text/plain"] = true
		typesToCompress["text/css"] = true
		typesToCompress["application/javascript"] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if client accepts gzip encoding
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}

			// Wrap response writer to capture content-type
			wrapper := &responseWriterWrapper{ResponseWriter: w}

			next.ServeHTTP(wrapper, r)

			// Check content-type after handler
			contentType := wrapper.Header().Get("Content-Type")
			if !typesToCompress[contentType] && !strings.HasPrefix(contentType, "application/json") {
				// Don't compress if content-type not in list
				return
			}

			// If we get here, we should have compressed but didn't, so let client know
			w.Header().Set("Content-Encoding", "gzip")
		})
	}
}

// responseWriterWrapper captures the first write to get content-type
type responseWriterWrapper struct {
	http.ResponseWriter
	written bool
	gw      *gzip.Writer
}

func (rww *responseWriterWrapper) Write(b []byte) (int, error) {
	if !rww.written {
		rww.written = true

		// Check content-type
		contentType := rww.Header().Get("Content-Type")
		shouldCompress := false

		if strings.Contains(contentType, "application/json") ||
			strings.Contains(contentType, "text/") ||
			strings.Contains(contentType, "javascript") {
			shouldCompress = true
		}

		if shouldCompress {
			rww.gw = gzip.NewWriter(rww.ResponseWriter)
			rww.Header().Set("Content-Encoding", "gzip")
			rww.Header().Del("Content-Length")
			return rww.gw.Write(b)
		}
	}

	if rww.gw != nil {
		return rww.gw.Write(b)
	}

	return rww.ResponseWriter.Write(b)
}

// Flush implements http.Flusher
func (rww *responseWriterWrapper) Flush() {
	if rww.gw != nil {
		rww.gw.Flush()
	}

	if f, ok := rww.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close closes gzip writer if present
func (rww *responseWriterWrapper) Close() error {
	if rww.gw != nil {
		return rww.gw.Close()
	}
	return nil
}
