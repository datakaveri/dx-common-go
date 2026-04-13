package response

// ResponseBuilder provides a fluent API for constructing DxResponse values.
type ResponseBuilder[T any] struct {
	resp DxResponse[T]
}

// New returns an empty ResponseBuilder for type T.
func New[T any]() *ResponseBuilder[T] {
	return &ResponseBuilder[T]{}
}

// WithType sets the "type" URN field.
func (b *ResponseBuilder[T]) WithType(urn string) *ResponseBuilder[T] {
	b.resp.Type = urn
	return b
}

// WithTitle sets the human-readable title.
func (b *ResponseBuilder[T]) WithTitle(title string) *ResponseBuilder[T] {
	b.resp.Title = title
	return b
}

// WithDetail sets the detail string.
func (b *ResponseBuilder[T]) WithDetail(detail string) *ResponseBuilder[T] {
	b.resp.Detail = detail
	return b
}

// WithResult sets the results payload.
func (b *ResponseBuilder[T]) WithResult(result T) *ResponseBuilder[T] {
	b.resp.Results = result
	return b
}

// WithPagination populates the pagination fields (totalHits, limit, offset).
func (b *ResponseBuilder[T]) WithPagination(pg PaginationInfo) *ResponseBuilder[T] {
	b.resp.TotalHits = &pg.TotalHits
	b.resp.Limit = &pg.Limit
	b.resp.Offset = &pg.Offset
	return b
}

// Build returns the assembled DxResponse.
func (b *ResponseBuilder[T]) Build() DxResponse[T] {
	return b.resp
}
