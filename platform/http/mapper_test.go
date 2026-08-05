package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/platform/observability/health"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// notFoundErr stands in for a service layer's own classified error — the case
// RouterSpec.Mappers exists for.
type notFoundErr struct{}

func (notFoundErr) Error() string { return "thing not found" }

func notFoundMapper(err error) (Problem, bool) {
	var e notFoundErr
	if errors.As(err, &e) {
		return Problem{
			Status: http.StatusNotFound,
			Type:   "urn:dx:test:NotFound",
			Title:  "Not Found",
			Detail: err.Error(),
		}, true
	}
	return Problem{}, false
}

func authAs(id string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(
				identity.With(r.Context(), identity.Subject{ID: id})))
		})
	}
}

// TestRouterSpecMappersReachHandlers is the regression test for the gap this
// mechanism closes.
//
// RouterSpec.Mappers was read ONLY by requireRoles/requireSubject, never by a
// route handler — Handle has already produced an http.HandlerFunc by the time
// NewRouter sees the route. Every service that set the field as the migration
// runbook instructs still returned a generic 500 for each classified 404, 409
// and 403 its service layer produced. Eight services set it.
func TestRouterSpecMappersReachHandlers(t *testing.T) {
	h := func(context.Context, None) (string, error) { return "", notFoundErr{} }

	r := NewRouter(RouterSpec{
		Base:    "/",
		Health:  health.New(),
		Logger:  zap.NewNop(),
		Mappers: []ErrorMapper{notFoundMapper},
		Auth:    AuthSpec{Authenticate: authAs("u-1")},
	}, Routes("", GET("/thing", Handle(h))))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: RouterSpec.Mappers did not reach the handler (%s)",
			rec.Code, rec.Body.String())
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not a Problem: %v", err)
	}
	if p.Detail != "thing not found" {
		t.Errorf("detail = %q, want the mapped message", p.Detail)
	}
}

// TestRouterMappersApplyToVoidAndRaw covers the other two adapters: all three
// render errors through the same path, and a service would be surprised if only
// Handle honoured its mappers.
func TestRouterMappersApplyToVoidAndRaw(t *testing.T) {
	voidH := func(context.Context, None) error { return notFoundErr{} }
	rawH := func(context.Context, None) (Response, error) { return nil, notFoundErr{} }

	r := NewRouter(RouterSpec{
		Base:    "/",
		Health:  health.New(),
		Logger:  zap.NewNop(),
		Mappers: []ErrorMapper{notFoundMapper},
		Auth:    AuthSpec{Authenticate: authAs("u-1")},
	}, Routes("",
		DELETE("/void", HandleVoid(voidH)),
		GET("/raw", HandleRaw(rawH)),
	))

	for _, tc := range []struct{ method, path string }{
		{http.MethodDelete, "/void"},
		{http.MethodGet, "/raw"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

// TestPerHandlerMappersWinOverRouterMappers pins the precedence: the nearer
// registration is the more specific one.
func TestPerHandlerMappersWinOverRouterMappers(t *testing.T) {
	specific := func(err error) (Problem, bool) {
		var e notFoundErr
		if errors.As(err, &e) {
			return Problem{Status: http.StatusGone, Type: "urn:dx:test:Gone", Title: "Gone"}, true
		}
		return Problem{}, false
	}
	h := func(context.Context, None) (string, error) { return "", notFoundErr{} }

	r := NewRouter(RouterSpec{
		Base:    "/",
		Health:  health.New(),
		Logger:  zap.NewNop(),
		Mappers: []ErrorMapper{notFoundMapper},
		Auth:    AuthSpec{Authenticate: authAs("u-1")},
	}, Routes("", GET("/thing", Handle(h, WithMappers(specific)))))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thing", nil))
	if rec.Code != http.StatusGone {
		t.Errorf("status = %d, want 410: the per-handler mapper must win", rec.Code)
	}
}

// TestUnmappedErrorStillGeneric500 guards the rule the mapper chain must not
// weaken: anything no mapper claims is still a generic 500 with the cause
// logged and never sent.
func TestUnmappedErrorStillGeneric500(t *testing.T) {
	h := func(context.Context, None) (string, error) {
		return "", errors.New("pq: host=10.0.0.1 user=admin password=hunter2")
	}

	r := NewRouter(RouterSpec{
		Base:    "/",
		Health:  health.New(),
		Logger:  zap.NewNop(),
		Mappers: []ErrorMapper{notFoundMapper},
		Auth:    AuthSpec{Authenticate: authAs("u-1")},
	}, Routes("", GET("/thing", Handle(h))))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thing", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "10.0.0.1") || strings.Contains(body, "hunter2") {
		t.Errorf("the driver error reached the client: %s", body)
	}
}
