package response

import "github.com/datakaveri/dx-common-go/platform/paging"

// DxResponse is the standard JSON envelope for successful responses.
// T is the type of the result payload.
type DxResponse[T any] struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Result T      `json:"result,omitempty"`
	// Deprecated: use DxPagedResponse with WritePaginatedInfo instead.
	TotalHits *int64 `json:"totalHits,omitempty"`
	// Deprecated: use DxPagedResponse with WritePaginatedInfo instead.
	Limit *int `json:"limit,omitempty"`
	// Deprecated: use DxPagedResponse with WritePaginatedInfo instead.
	Offset *int `json:"offset,omitempty"`
}

// DxErrorResponse is the standard JSON envelope for error responses.
type DxErrorResponse struct {
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Errors []string `json:"errors,omitempty"`
}

// PaginationInfo carries pagination metadata for list responses.
type PaginationInfo struct {
	TotalHits int64 `json:"totalHits"`
	Limit     int   `json:"limit"`
	Offset    int   `json:"offset"`
}

// DxPagedResponse is the envelope variant that nests page-based pagination under
// a "paginationInfo" object — the shape used by the control-plane API contract
// (catalogue search/list, etc.).
type DxPagedResponse[T any] struct {
	Type           string      `json:"type"`
	Title          string      `json:"title"`
	Detail         string      `json:"detail,omitempty"`
	Result         T           `json:"result,omitempty"`
	PaginationInfo paging.Info `json:"paginationInfo"`
}
