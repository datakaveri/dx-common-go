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
			if id == "" {
				id = uuid.NewString()
			}
			w.Header().Set("X-Request-ID", id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromCtx retrieves the request ID stored by the RequestID middleware.
// Returns an empty string if no ID is present.
func RequestIDFromCtx(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
