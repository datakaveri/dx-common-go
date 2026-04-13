package response

// DxResponse is the standard JSON envelope for successful responses.
// T is the type of the results payload.
type DxResponse[T any] struct {
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Detail    string  `json:"detail,omitempty"`
	Results   T       `json:"results,omitempty"`
	TotalHits *int64  `json:"totalHits,omitempty"`
	Limit     *int    `json:"limit,omitempty"`
	Offset    *int    `json:"offset,omitempty"`
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
