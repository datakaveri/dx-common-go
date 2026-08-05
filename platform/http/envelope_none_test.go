package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/platform/observability/health"
)

// TestNoneResponseOmitsResult pins the envelope for an acknowledged action with
// no payload: 200, the envelope, and NO result key — which is what the
// ServiceWriter this package replaced emitted for resp.Success(w, nil, ...).
// Rendering "result": {} instead is a visible change on every bookmark, vote
// and status-change endpoint that migrates.
func TestNoneResponseOmitsResult(t *testing.T) {
	h := func(context.Context, None) (None, error) { return None{}, nil }

	r := NewRouter(RouterSpec{
		Base:   "/",
		Health: health.New(),
		Logger: zap.NewNop(),
		Auth:   AuthSpec{Authenticate: authAs("u-1")},
	}, Routes("", POST("/ack", Handle(h, WithURNs(URNSpace("test")),
		WithMessage("Bookmarked", "thing bookmarked")))))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ack", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, present := env["result"]; present {
		t.Errorf("result is present for a None response: %s", rec.Body.String())
	}
	if env["title"] != "Bookmarked" || env["detail"] != "thing bookmarked" {
		t.Errorf("envelope = %v, want the supplied title and detail", env)
	}
}

// TestCreatedNoneOmitsResult is the 201 half — resp.Created(w, nil, "Joined").
func TestCreatedNoneOmitsResult(t *testing.T) {
	h := func(context.Context, None) (Created[None], error) { return Created[None]{}, nil }

	r := NewRouter(RouterSpec{
		Base:   "/",
		Health: health.New(),
		Logger: zap.NewNop(),
		Auth:   AuthSpec{Authenticate: authAs("u-1")},
	}, Routes("", POST("/join", Handle(h, WithURNs(URNSpace("test")),
		WithMessage("Joined", "")))))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/join", nil))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if _, present := env["result"]; present {
		t.Errorf("result is present for a Created[None]: %s", rec.Body.String())
	}
	if env["type"] != "urn:dx:test:created" {
		t.Errorf("type = %v, want the created URN", env["type"])
	}
}

// TestNonEmptyResultStillRendered guards the narrowness of the None rule: any
// other zero-ish value still renders, so this cannot quietly swallow a real
// empty payload.
func TestNonEmptyResultStillRendered(t *testing.T) {
	h := func(context.Context, None) (map[string]string, error) {
		return map[string]string{}, nil
	}

	r := NewRouter(RouterSpec{
		Base:   "/",
		Health: health.New(),
		Logger: zap.NewNop(),
		Auth:   AuthSpec{Authenticate: authAs("u-1")},
	}, Routes("", GET("/empty", Handle(h, WithURNs(URNSpace("test"))))))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/empty", nil))
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if _, present := env["result"]; !present {
		t.Errorf("an empty map result was dropped: %s", rec.Body.String())
	}
}
