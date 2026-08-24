package middleware

import (
	"net/http"
	"runtime/debug"

	"go.uber.org/zap"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// Recovery returns a chi-compatible middleware that catches panics, logs the
// stack trace, and returns a 500 Internal Server Error to the client.
func Recovery(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { //nolint:contextcheck // panic recovery reads r.Context() directly; no context is detached here
				if rec := recover(); rec != nil {
					stack := debug.Stack()
					logger.Error("panic recovered",
						zap.Any("panic", rec),
						zap.ByteString("stack", stack),
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
						zap.String("request_id", RequestIDFromCtx(r.Context())),
					)
					dxerrors.WriteError(w, dxerrors.NewInternal("internal server error"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
