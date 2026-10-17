package httpx

import (
	"errors"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// Handler is an error-returning gin handler. Returning an error is the ONLY
// error path; handlers never write an error response themselves.
type Handler func(*gin.Context) error

// ErrorMapper lets a specific route translate a domain error into an
// endpoint-specific response (e.g. ownership mismatch -> 404 not 403)
// without leaking HTTP into the service. Return (nil, false) to decline; the
// next mapper, then the default translation, then runs.
type ErrorMapper func(error) (dxerrors.DxError, bool)

// Handle adapts h to a gin.HandlerFunc and centralizes error translation.
// Equivalent to HandleWithLogger(nil, h, mappers...) — the fallback-500 path
// is not logged. Prefer BaseHandler.Handle, which supplies the handler's own
// logger.
func Handle(h Handler, mappers ...ErrorMapper) gin.HandlerFunc {
	return HandleWithLogger(nil, h, mappers...)
}

// HandleWithLogger is Handle plus a logger for the fallback-500 path, so
// unexpected errors are logged with request context before the generic
// response is written.
//
// Translation order for a returned error:
//  1. mappers, in order — the first to return (dxErr, true) wins.
//  2. errors.As a dxerrors.DxError — written as-is.
//  3. dxerrors.MapPostgresError(err) — the single source of truth for
//     pgx/pgconn -> DxError — re-checked with errors.As.
//  4. Fallback: log (if logger != nil) + a generic 500.
func HandleWithLogger(logger *zap.Logger, h Handler, mappers ...ErrorMapper) gin.HandlerFunc {
	return func(c *gin.Context) {
		err := h(c)
		if err == nil {
			return
		}
		if c.IsAborted() {
			// The handler already wrote a response (or a prior middleware
			// did) and returned its error only for logging/flow purposes.
			return
		}

		for _, m := range mappers {
			if dxErr, ok := m(err); ok {
				dxerrors.WriteGinError(c, dxErr)
				return
			}
		}

		var dxErr dxerrors.DxError
		if errors.As(err, &dxErr) {
			dxerrors.WriteGinError(c, dxErr)
			return
		}

		if mapped := dxerrors.MapPostgresError(err); mapped != err {
			if errors.As(mapped, &dxErr) {
				dxerrors.WriteGinError(c, dxErr)
				return
			}
		}

		if logger != nil {
			logger.Error("unhandled handler error",
				zap.Error(err),
				zap.String("path", c.FullPath()),
				zap.String("method", c.Request.Method),
			)
		}
		dxerrors.WriteGinError(c, dxerrors.NewInternal("internal error"))
	}
}
