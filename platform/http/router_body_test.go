package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpx "github.com/datakaveri/dx-common-go/platform/http"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// bodyReq carries a JSON body behind the standard authenticated adapter, so the
// test exercises the whole path a service uses: RouterSpec.MaxBodyBytes →
// carryMaxBody → the request context → decodeBody.
type bodyReq struct {
	httpx.Actor
	V string `json:"v"`
}

func bodyEcho() http.HandlerFunc {
	return httpx.Handle(func(_ context.Context, in bodyReq) (map[string]string, error) {
		return map[string]string{"v": in.V}, nil
	}, httpx.WithURNs(urns))
}

func postBody(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func jsonOfLen(n int) string {
	const overhead = len(`{"v":""}`)
	return `{"v":"` + strings.Repeat("a", n-overhead) + `"}`
}

// TestRouter_BodyLimitFromSpec is the end-to-end proof of P1-3: the limit set
// on RouterSpec — which a service feeds from config.Server.MaxBodyBytes — is the
// limit a client hits. A body over it is a 400 from the binder, not a 500 or an
// out-of-memory, and a body under it is served.
func TestRouter_BodyLimitFromSpec(t *testing.T) {
	build := func(max int64) http.Handler {
		return httpx.NewRouter(
			httpx.RouterSpec{
				Base:         "/api",
				URNs:         urns,
				MaxBodyBytes: max,
				Auth:         httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})},
			},
			httpx.Routes("/things", httpx.POST("", bodyEcho())),
		)
	}

	t.Run("over the configured limit is rejected", func(t *testing.T) {
		r := build(256)
		rec := postBody(r, "/api/things", jsonOfLen(512))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "256 byte limit") {
			t.Errorf("response must name the configured limit, got %s", rec.Body.String())
		}
	})

	t.Run("under the configured limit is served", func(t *testing.T) {
		r := build(256)
		rec := postBody(r, "/api/things", `{"v":"hello"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "hello") {
			t.Errorf("body not bound: %s", rec.Body.String())
		}
	})

	t.Run("default (spec zero) is 1 MiB", func(t *testing.T) {
		r := build(0) // RouterSpec omitted the field
		rec := postBody(r, "/api/things", jsonOfLen(httpx.DefaultMaxBodyBytes+64))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("a body over the default must 400, got %d", rec.Code)
		}
	})

	t.Run("negative spec disables the cap", func(t *testing.T) {
		r := build(-1)
		rec := postBody(r, "/api/things", jsonOfLen(2<<20)) // 2 MiB, over the default
		if rec.Code != http.StatusOK {
			t.Fatalf("a disabled cap must serve a large body, got %d; body = %s",
				rec.Code, rec.Body.String())
		}
	})
}
