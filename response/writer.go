package response

import (
	"encoding/json"
	"net/http"

	"github.com/datakaveri/dx-common-go/platform/paging"
)

// Write serialises body as JSON with the given statusCode.
func Write(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteSuccess writes a 200 OK response with the standard DxResponse envelope.
// Uses the generic urn:dx:rs:success URN; prefer ServiceWriter for per-service URNs.
func WriteSuccess[T any](w http.ResponseWriter, result T, title, detail string) {
	Write(w, http.StatusOK, DxResponse[T]{
		Type:   URNRsSuccess,
		Title:  title,
		Detail: detail,
		Result: result,
	})
}

// WritePaginated writes a 200 OK response with legacy pagination metadata.
//
// Deprecated: use WritePaginatedInfo for page-based pagination matching the
// control-plane contract.
func WritePaginated[T any](w http.ResponseWriter, results T, pg PaginationInfo, title string) {
	Write(w, http.StatusOK, DxResponse[T]{
		Type:      URNRsSuccess,
		Title:     title,
		Result:    results,
		TotalHits: &pg.TotalHits,
		Limit:     &pg.Limit,
		Offset:    &pg.Offset,
	})
}

// WritePaginatedInfo writes a 200 OK response with a nested "paginationInfo"
// object (control-plane contract shape: page/size/totalCount/totalPages/
// hasNext/hasPrevious). Use paging.NewInfo to build info.
func WritePaginatedInfo[T any](w http.ResponseWriter, results T, info paging.Info, title, detail string) {
	Write(w, http.StatusOK, DxPagedResponse[T]{
		Type:           URNRsSuccess,
		Title:          title,
		Detail:         detail,
		Result:         results,
		PaginationInfo: info,
	})
}

// WriteCreated writes a 201 Created response.
func WriteCreated[T any](w http.ResponseWriter, result T, title string) {
	Write(w, http.StatusCreated, DxResponse[T]{
		Type:   URNRsCreated,
		Title:  title,
		Result: result,
	})
}

// WriteAccepted writes a 202 Accepted response for async operations.
func WriteAccepted[T any](w http.ResponseWriter, result T, title string) {
	Write(w, http.StatusAccepted, DxResponse[T]{
		Type:   URNRsSuccess,
		Title:  title,
		Result: result,
	})
}

// WriteNoContent writes a 204 No Content response (empty body).
func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// ────────────────────────────────────────────────────────────────────────────
// ServiceWriter — per-service URN-aware response writer
// ────────────────────────────────────────────────────────────────────────────

// ServiceWriter writes HTTP responses using a service-specific URN prefix.
// Each service creates one at boot (e.g., NewServiceWriter("urn:dx:acl:"))
// and passes it to the handler layer.
type ServiceWriter struct {
	prefix string // e.g., "urn:dx:acl:" → "urn:dx:acl:success", "urn:dx:acl:created"
}

// NewServiceWriter returns a writer that tags every response with the given
// URN prefix. The prefix should end with a colon (e.g., "urn:dx:acl:").
func NewServiceWriter(urnPrefix string) *ServiceWriter {
	return &ServiceWriter{prefix: urnPrefix}
}

// Success writes a 200 OK response.
func (sw *ServiceWriter) Success(w http.ResponseWriter, result any, title, detail string) {
	Write(w, http.StatusOK, DxResponse[any]{
		Type:   sw.prefix + "success",
		Title:  title,
		Detail: detail,
		Result: result,
	})
}

// PaginatedInfo writes a 200 OK response with page-based pagination.
func (sw *ServiceWriter) PaginatedInfo(w http.ResponseWriter, result any, info paging.Info, title, detail string) {
	Write(w, http.StatusOK, DxPagedResponse[any]{
		Type:           sw.prefix + "success",
		Title:          title,
		Detail:         detail,
		Result:         result,
		PaginationInfo: info,
	})
}

// Created writes a 201 Created response.
func (sw *ServiceWriter) Created(w http.ResponseWriter, result any, title string) {
	Write(w, http.StatusCreated, DxResponse[any]{
		Type:   sw.prefix + "created",
		Title:  title,
		Result: result,
	})
}

// Accepted writes a 202 Accepted response.
func (sw *ServiceWriter) Accepted(w http.ResponseWriter, result any, title string) {
	Write(w, http.StatusAccepted, DxResponse[any]{
		Type:   sw.prefix + "success",
		Title:  title,
		Result: result,
	})
}

// NoContent writes a 204 No Content response (empty body).
func (sw *ServiceWriter) NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
