package resolver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/auth"
	dxjwt "github.com/datakaveri/dx-common-go/auth/jwt"
	"github.com/datakaveri/dx-common-go/auth/resolver"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// ROADMAP P0-17 stage 2. The subject headers are no longer signed, so the tests
// that asserted signature behaviour are replaced by tests of what took its
// place: a subject is honoured only behind a VERIFIED WORKLOAD.
//
// The property that must not be lost in the swap is the one
// TestInvalidHMACDoesNotFallThrough guarded — a request that ASSERTS a subject
// it is not entitled to assert must be REJECTED, never quietly downgraded to
// the JWT path. It is retained below in its new form
// (TestSubjectHeadersWithoutAVerifiedWorkloadAreRejected), because falling
// through would serve the request as whoever the Bearer names while ignoring a
// spoofing attempt.

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

// verifiedWorkload wraps h so the request arrives as though the workload gate
// had already verified its caller — which in production is exactly what has
// happened, since platform/http/middleware.Resolve wraps the resolver in
// workload.Middleware.
func verifiedWorkload(id string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := workload.With(r.Context(), workload.Principal{ID: id})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func projected(t *testing.T, u auth.DxUser) http.Header {
	t.Helper()
	h, err := dxheaders.Project(u)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return h
}

func withHeaders(req *http.Request, h http.Header) *http.Request {
	for k, v := range h {
		req.Header.Set(k, v[0])
	}
	return req
}

func TestSubjectHeaderPath(t *testing.T) {
	user := auth.DxUser{ID: "user-1", Email: "a@b.c", Roles: []string{"consumer"}}

	cap, next := newCapture()
	// JWT intentionally disabled to prove the internal path alone works.
	h := verifiedWorkload("dx-gateway-go",
		resolver.Middleware(resolver.Config{TrustSubjectHeaders: true})(next))

	req := withHeaders(httptest.NewRequest(http.MethodGet, "/", nil), projected(t, user))
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

// TestSubjectHeadersWithoutAVerifiedWorkloadAreRejected is THE test of this
// change, and the direct successor to TestInvalidHMACDoesNotFallThrough.
//
// Before, an attacker needed a valid signature. Now they need to be a verified
// workload. Either way, a request that names a user it may not name must be
// refused — and specifically must NOT fall through to the JWT path, which in
// dev mode injects a synthetic user without checking anything.
func TestSubjectHeadersWithoutAVerifiedWorkloadAreRejected(t *testing.T) {
	cap, next := newCapture()
	// No verifiedWorkload wrapper: this is a caller nobody authenticated.
	h := resolver.Middleware(resolver.Config{
		TrustSubjectHeaders: true,
		JWT:                 dxjwt.Config{Enabled: false}, // dev-mode would inject a synthetic user
		AllowDirect:         true,
	})(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(dxheaders.HdrSubjectID, "attacker")
	req.Header.Set(dxheaders.HdrSubjectRoles, "cos_admin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if cap.called {
		t.Fatal("the handler ran for a spoofed subject — the request must be refused, " +
			"not downgraded to the JWT path")
	}
}

// TestSubjectHeadersAreNotConsultedWhenUntrusted: a service that does not trust
// the internal path must ignore the headers entirely rather than half-read them.
func TestSubjectHeadersAreNotConsultedWhenUntrusted(t *testing.T) {
	cap, next := newCapture()
	h := verifiedWorkload("dx-gateway-go", resolver.Middleware(resolver.Config{
		TrustSubjectHeaders: false,
		JWT:                 dxjwt.Config{Enabled: false},
		AllowDirect:         true,
	})(next))

	req := withHeaders(httptest.NewRequest(http.MethodGet, "/", nil),
		projected(t, auth.DxUser{ID: "user-1"}))
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 via the JWT path, got %d", rec.Code)
	}
	if cap.origin != resolver.OriginDirect {
		t.Errorf("origin = %q, want direct: the subject headers must not have been read", cap.origin)
	}
}

func TestNoAuthMaterialIs401(t *testing.T) {
	// Internal-only mode (no JWT fallback) — a request with no headers must 401.
	_, next := newCapture()
	h := verifiedWorkload("dx-gateway-go",
		resolver.Middleware(resolver.Config{TrustSubjectHeaders: true})(next))

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
		// TrustSubjectHeaders false, JWT.Enabled false → misconfiguration.
	})
}

// Origin is now PROVENANCE ONLY, and the guard that made a trust decision from
// it is deleted (ROADMAP P0-17 stage 2).
//
// RequireGatewayOrigin read as "the gateway called" and never meant that:
// OriginGateway meant a valid HMAC signature, and every service held the same
// secret. Its one consumer (dx-agent-registry-go's credentials endpoint) now
// uses workload.RequireCaller, which checks a cryptographically bound client id.
// This asserts the remaining behaviour — the resolver still LABELS where a
// request came from — with nothing gating on it.
func TestOriginIsRecordedButNotATrustDecision(t *testing.T) {
	cap, next := newCapture()
	h := resolver.Middleware(resolver.Config{
		TrustSubjectHeaders: true,
		JWT:                 dxjwt.Config{Enabled: false},
		AllowDirect:         true,
	})(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer fake-token-for-dev-mode")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — nothing gates on origin any more", rec.Code)
	}
	if cap.origin != resolver.OriginDirect {
		t.Errorf("origin = %q, want direct", cap.origin)
	}
}

// TestParseIsNotAuthentication documents, executably, the one thing a reader of
// transport/headers could get dangerously wrong: Parse accepts anything.
//
// It is not a test of the resolver so much as a guard on the assumption the
// resolver rests on — if Parse ever grew a check, the resolver's precondition
// would look redundant and someone would remove it.
func TestParseIsNotAuthentication(t *testing.T) {
	h := http.Header{}
	h.Set(dxheaders.HdrSubjectID, "anyone-at-all")
	h.Set(dxheaders.HdrSubjectRoles, "cos_admin")
	u, err := dxheaders.Parse(h)
	if err != nil {
		t.Fatalf("Parse must accept any well-formed headers: %v", err)
	}
	if u.ID != "anyone-at-all" || len(u.Roles) != 1 {
		t.Fatalf("Parse mangled the headers: %+v", u)
	}
	// The authority check lives in the resolver, which is why it must run.
	_ = context.Background()
}
