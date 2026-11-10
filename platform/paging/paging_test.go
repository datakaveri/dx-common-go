package paging_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/datakaveri/dx-common-go/pagination"
	"github.com/datakaveri/dx-common-go/platform/paging"
)

// TestInfoWireCompatibility is the load-bearing test of this package.
//
// paging.Info replaces pagination.Info, which is the shape every DX client
// already parses. If these two ever serialise differently, the migration is a
// silent breaking change for every consumer of every list endpoint.
func TestInfoWireCompatibility(t *testing.T) {
	cases := []struct {
		page, size int
		total      int64
	}{
		{1, 20, 42},   // ordinary first page
		{3, 20, 42},   // last page
		{1, 10, 0},    // empty result set
		{1, 10, 10},   // exactly one full page
		{2, 10, 11},   // last page with one item
		{0, 0, 5},     // both below range — clamping must agree
		{-5, -1, 100}, // negatives
	}

	for _, c := range cases {
		// The deprecated call is the point of the test: it is the shape clients
		// parse today, and this asserts the replacement is byte-identical.
		oldJSON, err := json.Marshal(pagination.NewInfo(c.page, c.size, c.total)) //nolint:staticcheck // deliberate: comparing against the deprecated implementation
		if err != nil {
			t.Fatalf("marshal old: %v", err)
		}
		newJSON, err := json.Marshal(paging.NewInfo(c.page, c.size, c.total))
		if err != nil {
			t.Fatalf("marshal new: %v", err)
		}
		if string(oldJSON) != string(newJSON) {
			t.Errorf("wire drift for page=%d size=%d total=%d\n old: %s\n new: %s",
				c.page, c.size, c.total, oldJSON, newJSON)
		}
	}
}

func TestNewInfo_EmptyResultReportsZeroPages(t *testing.T) {
	// Not 1. Clients depend on totalPages==0 meaning "nothing matched".
	if got := paging.NewInfo(1, 10, 0); got.TotalPages != 0 {
		t.Errorf("totalPages = %d, want 0 for an empty result set", got.TotalPages)
	}
}

func TestRequest_Clamping(t *testing.T) {
	tests := []struct {
		name             string
		page, size       int
		wantPage, wantSz int
	}{
		{"in range", 3, 25, 3, 25},
		{"page below one", 0, 25, 1, 25},
		{"negative page", -7, 25, 1, 25},
		{"size zero takes default", 1, 0, 1, paging.DefaultSize},
		{"size over max is capped", 1, 5000, 1, paging.MaxSize},
		{"size exactly max is kept", 1, paging.MaxSize, 1, paging.MaxSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paging.NewRequest(tt.page, tt.size)
			if got.Page != tt.wantPage || got.Size != tt.wantSz {
				t.Errorf("NewRequest(%d,%d) = {%d,%d}, want {%d,%d}",
					tt.page, tt.size, got.Page, got.Size, tt.wantPage, tt.wantSz)
			}
		})
	}
}

func TestRequest_Offset(t *testing.T) {
	tests := []struct {
		page, size, want int
	}{
		{1, 20, 0},
		{2, 20, 20},
		{5, 10, 40},
	}
	for _, tt := range tests {
		if got := paging.NewRequest(tt.page, tt.size).Offset(); got != tt.want {
			t.Errorf("page=%d size=%d: offset = %d, want %d", tt.page, tt.size, got, tt.want)
		}
	}
}

// TestPage_ItemsNeverNil is the fix for the three-shapes defect: a page must
// always serialise its items as [], never null.
func TestPage_ItemsNeverNil(t *testing.T) {
	var nilSlice []string

	p := paging.NewPage(nilSlice, paging.NewRequest(1, 10), 0)
	if p.Items == nil {
		t.Fatal("NewPage must normalise a nil slice to an empty one")
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(decoded.Items) != "[]" {
		t.Errorf("items serialised as %s, want []", decoded.Items)
	}

	if e := paging.Empty[string](paging.NewRequest(1, 10)); e.Items == nil {
		t.Error("Empty must carry a non-nil slice")
	}
}

func TestMapPage_PreservesMetadata(t *testing.T) {
	src := paging.NewPage([]int{1, 2, 3}, paging.NewRequest(2, 3), 9)
	dst := paging.MapPage(src, func(i int) string { return string(rune('a' + i - 1)) })

	if got, want := len(dst.Items), 3; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	if dst.Items[0] != "a" || dst.Items[2] != "c" {
		t.Errorf("mapped items = %v", dst.Items)
	}
	if dst.Info != src.Info {
		t.Errorf("metadata not preserved: %+v vs %+v", dst.Info, src.Info)
	}
}

func TestMapPage_EmptyStaysNonNil(t *testing.T) {
	src := paging.Empty[int](paging.NewRequest(1, 10))
	if dst := paging.MapPage(src, func(i int) string { return "" }); dst.Items == nil {
		t.Error("MapPage of an empty page must keep items non-nil")
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name             string
		query            string
		wantPage         int
		wantSize         int
		wantSortLen      int
		wantErr          bool
		wantFirstField   string
		wantFirstDirDesc bool
	}{
		{name: "defaults when absent", query: "", wantPage: 1, wantSize: paging.DefaultSize},
		{name: "explicit", query: "page=3&size=25", wantPage: 3, wantSize: 25},
		{name: "size clamped", query: "size=9999", wantPage: 1, wantSize: paging.MaxSize},
		{name: "page clamped", query: "page=0", wantPage: 1, wantSize: paging.DefaultSize},
		{name: "non-numeric page rejected", query: "page=abc", wantErr: true},
		{name: "non-numeric size rejected", query: "size=ten", wantErr: true},
		{
			name: "sort single", query: "sort=created_at:desc",
			wantPage: 1, wantSize: paging.DefaultSize, wantSortLen: 1,
			wantFirstField: "created_at", wantFirstDirDesc: true,
		},
		{
			name: "sort multi", query: "sort=name:asc;created_at:desc",
			wantPage: 1, wantSize: paging.DefaultSize, wantSortLen: 2,
			wantFirstField: "name",
		},
		{name: "sort direction defaults to asc", query: "sort=name", wantPage: 1, wantSize: paging.DefaultSize, wantSortLen: 1, wantFirstField: "name"},
		{name: "bad sort direction rejected", query: "sort=name:sideways", wantErr: true},
		{name: "too many sort keys rejected", query: "sort=a:asc;b:asc;c:asc;d:asc", wantErr: true},
		{name: "duplicate sort field rejected", query: "sort=name:asc;name:desc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := paging.Parse(httptest.NewRequest(http.MethodGet, "/x?"+tt.query, nil))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Page != tt.wantPage || got.Size != tt.wantSize {
				t.Errorf("page/size = %d/%d, want %d/%d", got.Page, got.Size, tt.wantPage, tt.wantSize)
			}
			if len(got.Sort) != tt.wantSortLen {
				t.Fatalf("sort keys = %d, want %d", len(got.Sort), tt.wantSortLen)
			}
			if tt.wantSortLen > 0 {
				if got.Sort[0].Field != tt.wantFirstField {
					t.Errorf("first sort field = %q, want %q", got.Sort[0].Field, tt.wantFirstField)
				}
				wantDir := paging.Asc
				if tt.wantFirstDirDesc {
					wantDir = paging.Desc
				}
				if got.Sort[0].Dir != wantDir {
					t.Errorf("first sort dir = %q, want %q", got.Sort[0].Dir, wantDir)
				}
			}
		})
	}
}

func TestStrict_RejectsUnknownParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?page=1&statuss=ACTIVE", nil)
	if _, err := paging.Strict(req, "status"); err == nil {
		t.Fatal("expected an error for the typo'd parameter 'statuss'")
	}

	req = httptest.NewRequest(http.MethodGet, "/x?page=1&status=ACTIVE&sort=name", nil)
	if _, err := paging.Strict(req, "status"); err != nil {
		t.Fatalf("known parameters must be accepted: %v", err)
	}
}

func TestStrict_ErrorNamesEveryUnknownParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?zeta=1&alpha=2", nil)
	_, err := paging.Strict(req)
	if err == nil {
		t.Fatal("expected an error")
	}
	// Deterministic ordering: map iteration would otherwise make the message
	// vary between runs and defeat any test asserting on it.
	if got, want := err.Error(), "paging: alpha, zeta: unknown query parameter"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// TestParse_SurvivesRawSemicolonInSort is a regression test for a live trap.
//
// Since Go 1.17 url.ParseQuery rejects a raw semicolon, and
// (*http.Request).URL.Query() swallows that error and silently drops the
// offending parameter. The platform's documented multi-field sort syntax uses
// semicolons, so reading sort through url.Values returns UNSORTED data with no
// error at all. Parse must read it from RawQuery instead.
func TestParse_SurvivesRawSemicolonInSort(t *testing.T) {
	const raw = "/x?sort=name:asc;created_at:desc&page=2"

	// Establish that the naive path really does lose the parameter, so this
	// test fails loudly if a future Go release changes the behaviour and the
	// workaround becomes unnecessary.
	if got := httptest.NewRequest(http.MethodGet, raw, nil).URL.Query().Get("sort"); got != "" {
		t.Fatalf("premise broken: url.Values now yields sort=%q; the RawQuery workaround may be removable", got)
	}

	got, err := paging.Parse(httptest.NewRequest(http.MethodGet, raw, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Sort) != 2 {
		t.Fatalf("sort keys = %d, want 2 — the semicolon-separated sort was lost", len(got.Sort))
	}
	if got.Sort[0].Field != "name" || got.Sort[0].Dir != paging.Asc {
		t.Errorf("first key = %+v", got.Sort[0])
	}
	if got.Sort[1].Field != "created_at" || got.Sort[1].Dir != paging.Desc {
		t.Errorf("second key = %+v", got.Sort[1])
	}
	if got.Page != 2 {
		t.Errorf("page = %d, want 2 — other params must still parse normally", got.Page)
	}
}

// TestParse_PercentEncodedSortAlsoWorks: a well-behaved client that encodes the
// separator must get the same result as one that does not.
func TestParse_PercentEncodedSortAlsoWorks(t *testing.T) {
	encoded := "/x?sort=" + url.QueryEscape("name:asc;created_at:desc")
	got, err := paging.Parse(httptest.NewRequest(http.MethodGet, encoded, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Sort) != 2 {
		t.Fatalf("sort keys = %d, want 2", len(got.Sort))
	}
}
