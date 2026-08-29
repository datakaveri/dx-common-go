package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/datakaveri/dx-common-go/auth"
)

type bindTestBody struct {
	Name string `json:"name"`
}

func newTestContextWithBody(body string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	return c
}

func TestBind_DecodesValidJSON(t *testing.T) {
	c := newTestContextWithBody(`{"name":"widget"}`)
	got, err := Bind[bindTestBody](c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "widget" {
		t.Fatalf("Name = %q, want %q", got.Name, "widget")
	}
}

func TestBind_MalformedJSONReturnsValidationError(t *testing.T) {
	c := newTestContextWithBody(`{not json`)
	_, err := Bind[bindTestBody](c)
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestPathUUID_ValidValue(t *testing.T) {
	want := uuid.New()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Params = gin.Params{{Key: "id", Value: want.String()}}

	got, err := PathUUID(c, "id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPathUUID_MalformedValueIsValidationError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	c.Params = gin.Params{{Key: "id", Value: "not-a-uuid"}}

	if _, err := PathUUID(c, "id"); err == nil {
		t.Fatal("expected a validation error")
	}
}

func TestQueryUUID_AbsentIsNilNotError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)

	got, err := QueryUUID(c, "filter")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != uuid.Nil {
		t.Fatalf("got %v, want uuid.Nil", got)
	}
}

func TestQueryUUID_PresentMalformedIsError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x?filter=nope", nil)

	if _, err := QueryUUID(c, "filter"); err == nil {
		t.Fatal("expected a validation error")
	}
}

func TestUserID_MissingUserIsUnauthorized(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)

	if _, err := UserID(c); err == nil {
		t.Fatal("expected an unauthorized error")
	}
}

func TestUserID_ResolvesFromContext(t *testing.T) {
	want := uuid.New()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	ctx := auth.WithUser(r.Context(), auth.DxUser{ID: want.String()})
	c.Request = r.WithContext(ctx)

	got, err := UserID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
