package pagination

import (
	"math"
	"strconv"
)

// Request represents pagination parameters from a request
type Request struct {
	Page     int `json:"page" form:"page"`
	PageSize int `json:"page_size" form:"page_size"`
}

// Response represents pagination metadata in response
type Response struct {
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int `json:"total_pages"`
	HasNext    bool `json:"has_next"`
	HasPrev    bool `json:"has_prev"`
}

// Validate validates pagination parameters
func (pr *Request) Validate() error {
	if pr.Page < 1 {
		pr.Page = 1
	}
	if pr.PageSize < 1 {
		pr.PageSize = 10
	}
	if pr.PageSize > 100 {
		pr.PageSize = 100
	}
	return nil
}

// Offset calculates the database offset for the page
func (pr *Request) Offset() int {
	return (pr.Page - 1) * pr.PageSize
}

// NewResponse creates a pagination response
func NewResponse(req Request, total int64) Response {
	totalPages := int(math.Ceil(float64(total) / float64(req.PageSize)))
	if total == 0 {
		totalPages = 1
	}

	return Response{
		Page:       req.Page,
		PageSize:   req.PageSize,
		Total:      total,
		TotalPages: totalPages,
		HasNext:    req.Page < totalPages,
		HasPrev:    req.Page > 1,
	}
}

// ParsePaginationParams extracts pagination params from query string
func ParsePaginationParams(pageStr string, pageSizeStr string) Request {
	page := 1
	pageSize := 10

	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}

	if ps, err := strconv.Atoi(pageSizeStr); err == nil && ps > 0 {
		pageSize = ps
	}

	req := Request{Page: page, PageSize: pageSize}
	req.Validate()
	return req
}

// PaginatedResult wraps data with pagination metadata
type PaginatedResult[T any] struct {
	Data       []T      `json:"data"`
	Pagination Response `json:"pagination"`
}

// NewPaginatedResult creates a paginated result
func NewPaginatedResult[T any](data []T, pagination Response) PaginatedResult[T] {
	return PaginatedResult[T]{
		Data:       data,
		Pagination: pagination,
	}
}
