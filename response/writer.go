package response

import (
	"encoding/json"
	"net/http"
)

// Write serialises body as JSON with the given statusCode.
func Write(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteSuccess writes a 200 OK response with the standard DxResponse envelope.
func WriteSuccess[T any](w http.ResponseWriter, result T, title, detail string) {
	resp := DxResponse[T]{
		Type:   URNRsSuccess,
		Title:  title,
		Detail: detail,
		Results: result,
	}
	Write(w, http.StatusOK, resp)
}

// WritePaginated writes a 200 OK response with pagination metadata.
func WritePaginated[T any](w http.ResponseWriter, results T, pg PaginationInfo, title string) {
	resp := DxResponse[T]{
		Type:      URNRsSuccess,
		Title:     title,
		Results:   results,
		TotalHits: &pg.TotalHits,
		Limit:     &pg.Limit,
		Offset:    &pg.Offset,
	}
	Write(w, http.StatusOK, resp)
}

// WriteCreated writes a 201 Created response.
func WriteCreated[T any](w http.ResponseWriter, result T, title string) {
	resp := DxResponse[T]{
		Type:    URNRsCreated,
		Title:   title,
		Results: result,
	}
	Write(w, http.StatusCreated, resp)
}

// WriteNoContent writes a 204 No Content response (empty body).
func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
