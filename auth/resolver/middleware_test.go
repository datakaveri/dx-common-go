package resolver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/auth"
	dxjwt "github.com/datakaveri/dx-common-go/auth/jwt"
	"github.com/datakaveri/dx-common-go/auth/resolver"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// captureHandler records the resolved user + origin so tests can assert them.
type captured struct {
	user   auth.DxUser
	origin resolver.Origin
	called bool
}

func newCapture() (*captured, http.Handler) {
	c := &captured{}
	return c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFromCtx(r.Context())
		o, _ := resolver.OriginFromCtx(r.Context())
		c.user = u
		c.origin = o
		c.called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestHMACPath(t *testing.T) {
	secret := []byte("test-secret-please")
	user := auth.DxUser{ID: "user-1", Email: "a@b.c", Roles: []string{"consumer"}}

	signed, err := dxheaders.Sign(user, dxheaders.Config{Secret: secret})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	cfg := resolver.Config{
		Headers: dxheaders.Config{Secret: secret, MaxAge: 60 * time.Second},
		// JWT intentionally disabled to prove HMAC alone works.
	}
	cap, next := newCapture()
	h := resolver.Middleware(cfg)(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range signed {
		req.Header.Set(k, v[0])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !cap.called {
		t.Fatal("handler not called")
	}
	if cap.user.ID != user.ID {
		t.Errorf("user.ID: want %q got %q", user.ID, cap.user.ID)
	}
	if cap.origin != resolver.OriginGateway {
		t.Errorf("origin: want gateway got %q", cap.origin)
	}
}

func TestInvalidHMACDoesNotFallThrough(t *testing.T) {
	// Bad signature with HMAC headers present must reject — falling through
	// to JWT here (especially in dev-mode where the JWT middleware would
	// inject a synthetic user without checking anything) would silently
	// promote an attacker to a valid identity.
	cfg := resolver.Config{
		Headers:     dxheaders.Config{Secret: []byte("the-real-secret")},
		JWT:         dxjwt.Config{Enabled: false}, // dev-mode would inject synthetic user
		AllowDirect: true,
	}
	_, next := newCapture()
	h := resolver.Middleware(cfg)(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(dxheaders.HdrSubjectID, "attacker")
	req.Header.Set(dxheaders.HdrSubjectIssuedAt, "9999999999")
	req.Header.Set(dxheaders.HdrSubjectSig, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestJWTFallbackWhenDisabledDevMode(t *testing.T) {
	// dxjwt.Middleware with Enabled=false injects a synthetic dev user — we
	// rely on that here to avoid setting up a real JWKS in unit tests.
	cfg := resolver.Config{
		Headers: dxheaders.Config{Secret: []byte("secret")},
		JWT:     dxjwt.Config{Enabled: false},
		// AllowDirect is auto-derived from JWT.Enabled; here it's false so
		// the JWT fallback should be unreachable.
	}
	_, next := newCapture()
	h := resolver.Middleware(cfg)(next)

	// No HMAC headers, no Authorization → should 401 (HMAC-only mode).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNoAuthMaterialIs401(t *testing.T) {
	// HMAC-only mode (no JWT fallback) — request with no headers must 401.
	cfg := resolver.Config{
		Headers: dxheaders.Config{Secret: []byte("secret")},
	}
	_, next := newCapture()
	h := resolver.Middleware(cfg)(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPanicWhenBothDisabled(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when both paths disabled")
		}
	}()
	resolver.Middleware(resolver.Config{
		// Headers.Secret empty, JWT.Enabled false → misconfiguration.
	})
}

func TestRequireGatewayOriginAllowsGateway(t *testing.T) {
	secret := []byte("secret")
	user := auth.DxUser{ID: "user-1"}
	signed, _ := dxheaders.Sign(user, dxheaders.Config{Secret: secret})

	resolverMW := resolver.Middleware(resolver.Config{
		Headers: dxheaders.Config{Secret: secret},
	})
	gateOnly := resolver.RequireGatewayOrigin()

	_, next := newCapture()
	h := resolverMW(gateOnly(next))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range signed {
		req.Header.Set(k, v[0])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireGatewayOriginRejectsDirect(t *testing.T) {
	// Use the dev-mode JWT path (Enabled=false → synthetic user). The
	// resolver tags this as OriginDirect; RequireGatewayOrigin must 403.
	resolverMW := resolver.Middleware(resolver.Config{
		Headers:     dxheaders.Config{Secret: []byte("secret")},
		JWT:         dxjwt.Config{Enabled: false},
		AllowDirect: true,
	})
	gateOnly := resolver.RequireGatewayOrigin()

	_, next := newCapture()
	h := resolverMW(gateOnly(next))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer fake-token-for-dev-mode")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}
