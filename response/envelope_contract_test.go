package response

import (
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/paging"
)

// TestEnvelopeWireContract pins the exact JSON every DX client parses.
//
// It exists because the client-facing documentation drifted from the code and
// nothing caught it: CLIENT-CONTRACT-CHANGES.md, CONTRIBUTING.md and
// MIGRATION.md all documented `"results"` with
// `paginationInfo{offset,limit,totalHits}` — a shape no service has ever
// emitted. A golden assertion on the wire bytes is the cheapest thing that
// makes that class of drift impossible to merge.
//
// Changing any string in this test is a client-visible contract change and
// requires an entry in claude-docs/CLIENT-CONTRACT-CHANGES.md.
func TestEnvelopeWireContract(t *testing.T) {
	sw := NewServiceWriter("urn:dx:acl:")

	tests := []struct {
		name  string
		write func(w *httptest.ResponseRecorder)
		want  string
	}{
		{
			name:  "success",
			write: func(w *httptest.ResponseRecorder) { sw.Success(w, []string{"a"}, "Success", "fetched") },
			want:  `{"type":"urn:dx:acl:success","title":"Success","detail":"fetched","result":["a"]}`,
		},
		{
			name: "paginated",
			write: func(w *httptest.ResponseRecorder) {
				sw.PaginatedInfo(w, []string{"a"}, paging.NewInfo(1, 20, 42), "Success", "")
			},
			want: `{"type":"urn:dx:acl:success","title":"Success","result":["a"],` +
				`"paginationInfo":{"page":1,"size":20,"totalCount":42,"totalPages":3,"hasNext":true,"hasPrevious":false}}`,
		},
		{
			name:  "created",
			write: func(w *httptest.ResponseRecorder) { sw.Created(w, map[string]string{"id": "x"}, "Created") },
			want:  `{"type":"urn:dx:acl:created","title":"Created","result":{"id":"x"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.write(w)
			if got := trimNewline(w.Body.String()); got != tt.want {
				t.Errorf("wire shape changed — this is a CLIENT-VISIBLE contract change\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestEmptyResultIsThreeShapes documents a real inconsistency rather than
// asserting a desired one.
//
// Result is typed `any` with `omitempty`, and encoding/json only omits an
// interface field when the interface itself is nil. So "no results" reaches
// clients as THREE different payloads depending on what the repository
// happened to return:
//
//	make([]T, 0)  -> "result":[]      (repos that pre-size the slice)
//	var out []T   -> "result":null    (repos that append to a zero value)
//	nil           -> field absent
//
// Both of the first two shapes ship in the fleet today, per service. Clients
// must therefore treat absent, null and [] as equivalent. The platform's
// paging.Page[T] envelope removes the ambiguity by always carrying a
// non-nil slice — see claude-docs/PLATFORM-ARCHITECTURE.md §5.
func TestEmptyResultIsThreeShapes(t *testing.T) {
	sw := NewServiceWriter("urn:dx:acl:")
	var nilSlice []string

	tests := []struct {
		name string
		val  any
		want string
	}{
		{"pre-sized empty slice", []string{}, `{"type":"urn:dx:acl:success","title":"Success","result":[]}`},
		{"nil slice", nilSlice, `{"type":"urn:dx:acl:success","title":"Success","result":null}`},
		{"untyped nil", nil, `{"type":"urn:dx:acl:success","title":"Success"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			sw.Success(w, tt.val, "Success", "")
			if got := trimNewline(w.Body.String()); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func trimNewline(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\n' {
		return s[:n-1]
	}
	return s
}
