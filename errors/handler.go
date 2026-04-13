package errors

import (
	"encoding/json"
	"net/http"
)

// DxErrorResponse is the JSON body returned for all error responses.
type DxErrorResponse struct {
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Errors []string `json:"errors,omitempty"`
}

// WriteError serialises a DxError as JSON and writes it to w.
func WriteError(w http.ResponseWriter, err DxError) {
	resp := DxErrorResponse{
		Type:   err.URN(),
		Title:  string(err.Code()),
		Detail: err.Error(),
		Errors: err.Details(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.HTTPStatus())
	_ = json.NewEncoder(w).Encode(resp)
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
