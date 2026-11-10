// Package paging is the platform's single pagination model.
//
// It replaces six competing representations that coexisted in this library —
// pagination.Request/Info/Response, model.PaginationRequest,
// request.PaginatedRequest, response.PaginationInfo, dao.Page[T] and
// repository.KeysetPage[R] — along with three independent HTTP parsers that
// disagreed about parameter names, defaults and caps.
//
// Two things here are contract, not preference:
//
//   - Info's JSON shape is the client-facing wire contract. Changing a field
//     name or its semantics breaks every DX client and requires an entry in
//     claude-docs/CLIENT-CONTRACT-CHANGES.md. It is pinned by tests.
//   - Page[T].Items is never nil. Callers therefore cannot accidentally emit
//     `"result": null` for an empty page, which is what a repository returning
//     a zero-value slice does today (see TestEmptyResultIsThreeShapes in
//     platform/http's predecessor, response).
//
// Layer: L0 (kernel). Imports nothing from the platform.
package paging

import "math"

// Defaults and bounds for page/size. These match what every current parser
// already converges on, so adopting this package changes no observed behaviour.
const (
	DefaultPage = 1
	DefaultSize = 10
	MaxSize     = 100
)

// Request is a parsed, validated page request: 1-based page plus size.
//
// Construct it with NewRequest or ParseRequest rather than a struct literal —
// both clamp into range, and an unclamped zero value would silently produce
// LIMIT 0.
type Request struct {
	Page int `json:"page" form:"page"`
	Size int `json:"size" form:"size"`
}

// NewRequest clamps page and size into range: page below 1 becomes 1, size
// outside [1, MaxSize] becomes DefaultSize or MaxSize respectively.
func NewRequest(page, size int) Request {
	if page < DefaultPage {
		page = DefaultPage
	}
	switch {
	case size < 1:
		size = DefaultSize
	case size > MaxSize:
		size = MaxSize
	}
	return Request{Page: page, Size: size}
}

// Limit is the SQL LIMIT for this request.
func (r Request) Limit() int { return r.Size }

// Offset is the SQL OFFSET derived from the 1-based page.
//
// Prefer keyset pagination (Cursor) for deep pages: OFFSET makes the database
// scan and discard every preceding row, so cost grows with page number.
func (r Request) Offset() int {
	if r.Page < DefaultPage {
		return 0
	}
	return (r.Page - 1) * r.Size
}

// Info is the "paginationInfo" object clients parse.
//
// The JSON field names below ARE the public API contract. They are camelCase
// and deliberately unlike the internal snake_case shapes this package replaces.
type Info struct {
	Page        int   `json:"page"`
	Size        int   `json:"size"`
	TotalCount  int64 `json:"totalCount"`
	TotalPages  int   `json:"totalPages"`
	HasNext     bool  `json:"hasNext"`
	HasPrevious bool  `json:"hasPrevious"`
	// NextCursor is the opaque token for the next KEYSET page, present only on a
	// cursor-paginated response and omitted on the last page and on every
	// offset-paginated response — so an offset client's contract is unchanged.
	// A keyset response leaves the offset fields (page/totalCount/totalPages)
	// zero: keyset deliberately avoids the COUNT they would need.
	NextCursor string `json:"nextCursor,omitempty"`
}

// NewInfo builds the response metadata for a 1-based page of the given size
// over totalCount matching rows.
//
// totalPages is ceil(totalCount/size), so an empty result set reports 0 pages
// rather than 1 — that is the established control-plane contract and clients
// depend on it.
func NewInfo(page, size int, totalCount int64) Info {
	if page < DefaultPage {
		page = DefaultPage
	}
	if size < 1 {
		size = DefaultSize
	}
	totalPages := int(math.Ceil(float64(totalCount) / float64(size)))
	return Info{
		Page:        page,
		Size:        size,
		TotalCount:  totalCount,
		TotalPages:  totalPages,
		HasNext:     page < totalPages,
		HasPrevious: page > DefaultPage,
	}
}

// InfoFor is NewInfo for an already-parsed Request.
func InfoFor(r Request, totalCount int64) Info { return NewInfo(r.Page, r.Size, totalCount) }

// KeysetInfo builds the response metadata for a KEYSET (cursor) page. nextCursor
// is the token for the following page, or "" when this is the last one. There is
// no total count by design — keyset pagination exists precisely to avoid the
// COUNT that fills TotalCount/TotalPages — so those and Page stay zero and a
// client pages by following NextCursor until it is absent.
func KeysetInfo(size int, nextCursor string) Info {
	if size < 1 {
		size = DefaultSize
	}
	return Info{
		Size:       size,
		HasNext:    nextCursor != "",
		NextCursor: nextCursor,
	}
}

// Page is one page of results plus its metadata — the value a repository
// returns and an HTTP handler renders.
//
// Items is guaranteed non-nil, so a page always serialises as [] rather than
// null. That closes a real client-facing ambiguity: because the response
// envelope's result field is typed `any` with omitempty, "no results" currently
// reaches clients as [], null, or an absent field depending on whether the
// repository returned make([]T,0), a zero-value slice, or nil. All three ship
// in the fleet today.
type Page[T any] struct {
	Items []T  `json:"items"`
	Info  Info `json:"paginationInfo"`
}

// Parts returns the items and metadata as untyped values.
//
// It exists so a renderer can handle any Page[T] without knowing T — Go cannot
// type-switch on a generic instantiation, so platform/http detects a page
// through this method instead. paging stays free of any HTTP dependency.
func (p Page[T]) Parts() (any, Info) { return p.Items, p.Info }

// NewPage builds a Page, normalising a nil slice to an empty one.
func NewPage[T any](items []T, r Request, totalCount int64) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Info: InfoFor(r, totalCount)}
}

// Empty is the zero page for a request — no items, no matches.
func Empty[T any](r Request) Page[T] { return NewPage([]T{}, r, 0) }

// MapPage converts a page of one element type to another, preserving metadata.
// It is the replacement for the row -> domain mapping loop that appears ~20
// times across the fleet:
//
//	out := make([]domain.X, 0, len(page.Data))
//	for i := range page.Data {
//	    out = append(out, page.Data[i].toDomain())
//	}
func MapPage[A, B any](p Page[A], fn func(A) B) Page[B] {
	items := make([]B, 0, len(p.Items))
	for _, a := range p.Items {
		items = append(items, fn(a))
	}
	return Page[B]{Items: items, Info: p.Info}
}
