package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type requestIDKey struct{}

// RequestID returns a middleware that attaches a unique UUID to each request.
// It looks for an existing X-Request-ID header first; if absent it generates one.
// The ID is stored in the request context and written to the X-Request-ID response header.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if !validRequestID(id) {
				id = uuid.NewString()
			}
			w.Header().Set("X-Request-ID", id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// maxRequestIDLen bounds a client-supplied correlation id. It is generous for
// any real id (a UUID is 36) and small enough that an oversized header cannot be
// used to bloat every log line and index entry the id lands in.
const maxRequestIDLen = 128

// validRequestID reports whether a client-supplied X-Request-ID is safe to honour:
// non-empty, within the length bound, and URL-safe identifier characters only.
// An invalid one is replaced with a generated id rather than propagated — the
// value is echoed to clients and stamped onto logs, traces and downstream
// requests, so an unvalidated one is a log-injection and cardinality vector
// (review §6 Security / GW-3).
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// RequestIDFromCtx retrieves the request ID stored by the RequestID middleware.
// Returns an empty string if no ID is present.
func RequestIDFromCtx(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
