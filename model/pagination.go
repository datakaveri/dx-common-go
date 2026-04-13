package model

import (
	"net/http"
	"strconv"
)

const (
	defaultLimit = 10
	maxLimit     = 100
	defaultOffset = 0
)

// PaginationRequest carries validated limit/offset query parameters.
type PaginationRequest struct {
	Limit  int `json:"limit"  validate:"min=1,max=100"`
	Offset int `json:"offset" validate:"min=0"`
}

// ParsePagination reads "limit" and "offset" from r's query string and returns
// a PaginationRequest with defaults applied. Invalid or out-of-range values are
// silently clamped to defaults.
func ParsePagination(r *http.Request) PaginationRequest {
	q := r.URL.Query()

	limit := defaultLimit
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v >= 1 {
			limit = v
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	offset := defaultOffset
	if o := q.Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	return PaginationRequest{Limit: limit, Offset: offset}
}
