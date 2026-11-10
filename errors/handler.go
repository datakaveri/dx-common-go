package errors

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

// DxErrorResponse is the JSON body returned for all error responses.
type DxErrorResponse struct {
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Errors []string `json:"errors,omitempty"`
}

// toResponse builds the wire representation shared by WriteError and
// WriteGinError.
func toResponse(err DxError) DxErrorResponse {
	return DxErrorResponse{
		Type:   err.URN(),
		Title:  err.Title(),
		Detail: err.Message(),
		Errors: err.Details(),
	}
}

// WriteError serialises a DxError as JSON and writes it to w. The title field
// is the human-readable HTTP status description (e.g., "Bad Request") matching
// the Java controlplane convention.
func WriteError(w http.ResponseWriter, err DxError) {
	resp := toResponse(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.HTTPStatus())
	_ = json.NewEncoder(w).Encode(resp)
}

// WriteGinError is the gin equivalent of WriteError: it serialises err as
// JSON, writes the response, and aborts the gin handler chain so downstream
// handlers don't run.
func WriteGinError(c *gin.Context, err DxError) {
	c.AbortWithStatusJSON(err.HTTPStatus(), toResponse(err))
}

// GlobalErrorHandler is a chi middleware that recovers from panics that are
// of type DxError and writes a structured JSON response. Non-DxError panics
// are re-panicked so they can be caught by a separate recovery middleware.
func GlobalErrorHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if dxErr, ok := rec.(DxError); ok {
					WriteError(w, dxErr)
					return
				}
				// Re-panic for the generic recovery middleware to handle.
				panic(rec)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
