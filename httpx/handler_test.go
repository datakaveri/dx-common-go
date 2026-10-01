package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestContext(method, target string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	return c, w
}

func TestHandle_NilErrorWritesNothing(t *testing.T) {
	c, w := newTestContext(http.MethodGet, "/x")
	Handle(func(*gin.Context) error { return nil })(c)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want default 200 (nothing written)", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", w.Body.String())
	}
}

func TestHandle_DxErrorWrittenAsIs(t *testing.T) {
	c, w := newTestContext(http.MethodGet, "/x")
	Handle(func(*gin.Context) error { return dxerrors.NewNotFound("missing") })(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestHandle_GenericErrorFallsBackTo500(t *testing.T) {
	c, w := newTestContext(http.MethodGet, "/x")
	Handle(func(*gin.Context) error { return errors.New("boom") })(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
}

func TestHandle_AbortedHandlerIsLeftAlone(t *testing.T) {
	c, w := newTestContext(http.MethodGet, "/x")
	Handle(func(c *gin.Context) error {
		c.AbortWithStatus(http.StatusTeapot)
		return errors.New("already handled")
	})(c)
	if w.Code != http.StatusTeapot {
		t.Fatalf("code = %d, want 418 (handler's own abort must win)", w.Code)
	}
}

var errNotOwner = errors.New("not the owner")

func TestHandle_PerRouteMapperWins(t *testing.T) {
	c, w := newTestContext(http.MethodGet, "/x")
	notOwnerAs404 := func(err error) (dxerrors.DxError, bool) {
		if errors.Is(err, errNotOwner) {
			return dxerrors.NewNotFound("not found"), true
		}
		return nil, false
	}
	Handle(func(*gin.Context) error { return errNotOwner }, notOwnerAs404)(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 via mapper", w.Code)
	}
}

func TestHandle_MapperDeclinesFallsThroughToDefault(t *testing.T) {
	c, w := newTestContext(http.MethodGet, "/x")
	declines := func(error) (dxerrors.DxError, bool) { return nil, false }
	Handle(func(*gin.Context) error { return dxerrors.NewConflict("dup") }, declines)(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (default DxError translation)", w.Code)
	}
}
