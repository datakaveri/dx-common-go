package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/paging"
)

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, w.Body.String())
	}
	return m
}

func TestWriteSuccess_GenericURN(t *testing.T) {
	w := httptest.NewRecorder()
	WriteSuccess(w, map[string]int{"n": 1}, "OK", "done")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	m := decode(t, w)
	if m["type"] != URNRsSuccess {
		t.Fatalf("type = %v, want %s", m["type"], URNRsSuccess)
	}
	if m["result"] == nil {
		t.Fatal("missing result")
	}
}

func TestServiceWriter_PerServiceURN(t *testing.T) {
	sw := NewServiceWriter("urn:dx:community:")

	w := httptest.NewRecorder()
	sw.Success(w, nil, "OK", "ok")
	if decode(t, w)["type"] != "urn:dx:community:success" {
		t.Fatalf("success type = %v", decode(t, w)["type"])
	}

	w = httptest.NewRecorder()
	sw.Created(w, map[string]string{"id": "x"}, "Created")
	if w.Code != http.StatusCreated {
		t.Fatalf("created code = %d", w.Code)
	}
	if decode(t, w)["type"] != "urn:dx:community:created" {
		t.Fatalf("created type = %v", decode(t, w)["type"])
	}
}

func TestServiceWriter_PaginatedInfo(t *testing.T) {
	sw := NewServiceWriter("urn:dx:community:")
	w := httptest.NewRecorder()
	sw.PaginatedInfo(w, []int{1, 2, 3}, paging.NewInfo(2, 10, 25), "OK", "page")

	m := decode(t, w)
	pi, ok := m["paginationInfo"].(map[string]any)
	if !ok {
		t.Fatalf("missing paginationInfo: %s", w.Body.String())
	}
	if pi["page"].(float64) != 2 || pi["size"].(float64) != 10 || pi["totalCount"].(float64) != 25 {
		t.Fatalf("pagination wrong: %v", pi)
	}
	// 25 items / size 10 → 3 pages, page 2 has next + previous.
	if pi["totalPages"].(float64) != 3 || pi["hasNext"] != true || pi["hasPrevious"] != true {
		t.Fatalf("derived pagination wrong: %v", pi)
	}
}

func TestWriteNoContent(t *testing.T) {
	w := httptest.NewRecorder()
	WriteNoContent(w)
	if w.Code != http.StatusNoContent {
		t.Fatalf("code = %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", w.Body.String())
	}
}

func TestWriteCreated_URN(t *testing.T) {
	w := httptest.NewRecorder()
	WriteCreated(w, map[string]string{"id": "1"}, "Created")
	if w.Code != http.StatusCreated || decode(t, w)["type"] != URNRsCreated {
		t.Fatalf("created: code=%d type=%v", w.Code, decode(t, w)["type"])
	}
}
