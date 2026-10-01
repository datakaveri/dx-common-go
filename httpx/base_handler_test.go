package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/datakaveri/dx-common-go/pagination"
)

func newBaseHandlerTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	return c, w
}

func TestBaseHandler_OK(t *testing.T) {
	b := NewBaseHandler("urn:dx:test:", nil)
	c, w := newBaseHandlerTestContext()

	if err := b.OK(c, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var body struct {
		Type   string            `json:"type"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Type != "urn:dx:test:success" {
		t.Fatalf("type = %q, want urn:dx:test:success", body.Type)
	}
	if body.Result["k"] != "v" {
		t.Fatalf("result = %v", body.Result)
	}
}

func TestBaseHandler_Created(t *testing.T) {
	b := NewBaseHandler("urn:dx:test:", nil)
	c, w := newBaseHandlerTestContext()

	if err := b.Created(c, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201", w.Code)
	}
}

func TestBaseHandler_NoContent(t *testing.T) {
	b := NewBaseHandler("urn:dx:test:", nil)
	c, w := newBaseHandlerTestContext()

	if err := b.NoContent(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", w.Body.String())
	}
}

func TestBaseHandler_Paginated(t *testing.T) {
	b := NewBaseHandler("urn:dx:test:", nil)
	c, w := newBaseHandlerTestContext()

	info := pagination.NewInfo(1, 10, 42)
	if err := b.Paginated(c, []int{1, 2, 3}, info); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var body struct {
		PaginationInfo pagination.Info `json:"paginationInfo"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.PaginationInfo.TotalCount != 42 {
		t.Fatalf("totalCount = %d, want 42", body.PaginationInfo.TotalCount)
	}
}
