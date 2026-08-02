package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	httpx "github.com/datakaveri/dx-common-go/platform/http"
	"github.com/datakaveri/dx-common-go/platform/observability/health"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// authAs returns middleware that stands in for the real resolver, attaching a
// fixed subject. The router must not care how verification happened.
func authAs(sub identity.Subject) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.With(r.Context(), sub)))
		})
	}
}

func denyAll() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r) // attaches nothing; Actor binding then 401s
		})
	}
}

func okHandler() http.HandlerFunc {
	return httpx.Handle(func(context.Context, httpx.None) (map[string]string, error) {
		return map[string]string{"ok": "yes"}, nil
	}, httpx.WithURNs(urns))
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRouter_MountsRoutesUnderBase(t *testing.T) {
	r := httpx.NewRouter(
		httpx.RouterSpec{
			Base: "/iudx/v2/acl",
			URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})},
		},
		httpx.Routes("/policies", httpx.GET("", okHandler())),
	)

	if rec := get(r, "/iudx/v2/acl/policies"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if rec := get(r, "/policies"); rec.Code != http.StatusNotFound {
		t.Errorf("a route outside Base must 404, got %d", rec.Code)
	}
}

func TestRouter_PathParametersResolve(t *testing.T) {
	type req struct {
		ID string `path:"policyId"`
	}
	var got string
	h := httpx.Handle(func(_ context.Context, in req) (map[string]string, error) {
		got = in.ID
		return map[string]string{"id": in.ID}, nil
	}, httpx.WithURNs(urns))

	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/iudx/v2/acl", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})}},
		httpx.Routes("/policies", httpx.GET("/{policyId}", h)),
	)

	if rec := get(r, "/iudx/v2/acl/policies/7f3a"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got != "7f3a" {
		t.Errorf("path parameter = %q, want 7f3a — the backend accessor is not wired", got)
	}
}

// TestRouter_OperationalEndpointsBypassAuth: kubelet does not carry a bearer
// token, and a readiness probe that needs one fails closed for the wrong reason.
func TestRouter_OperationalEndpointsBypassAuth(t *testing.T) {
	reg := health.New()
	reg.Add("db", health.CheckerFunc(func(context.Context) error { return nil }))

	r := httpx.NewRouter(httpx.RouterSpec{
		Base:    "/iudx/v2/acl",
		URNs:    urns,
		Health:  reg,
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("# metrics")) }),
		// Auth that attaches nothing: a protected route would 401.
		Auth: httpx.AuthSpec{Authenticate: denyAll()},
	})

	for _, path := range []string{"/healthz/live", "/healthz/ready", "/metrics"} {
		if rec := get(r, path); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200 without authentication", path, rec.Code)
		}
	}
}

func TestRouter_PublicRoutesSkipAuth(t *testing.T) {
	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/iudx/v2/cat", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: denyAll()}},
		httpx.Routes("",
			httpx.GET("/search", okHandler(), httpx.Public()),
			httpx.GET("/private", okHandler()),
		),
	)

	if rec := get(r, "/iudx/v2/cat/search"); rec.Code != http.StatusOK {
		t.Errorf("a Public route must serve without authentication, got %d", rec.Code)
	}
	if rec := get(r, "/iudx/v2/cat/private"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a protected route must 401 without a subject, got %d", rec.Code)
	}
}

func TestRouter_RoleGate(t *testing.T) {
	build := func(sub identity.Subject) http.Handler {
		return httpx.NewRouter(
			httpx.RouterSpec{Base: "/iudx/v2/acl", URNs: urns,
				Auth: httpx.AuthSpec{Authenticate: authAs(sub)}},
			httpx.Routes("/admin", httpx.GET("", okHandler(), httpx.Roles("cos_admin", "org_admin"))),
		)
	}

	admin := build(identity.Subject{ID: "u-1", Roles: []string{"org_admin"}})
	if rec := get(admin, "/iudx/v2/acl/admin"); rec.Code != http.StatusOK {
		t.Errorf("a holder of one required role must pass, got %d", rec.Code)
	}

	consumer := build(identity.Subject{ID: "u-2", Roles: []string{"consumer"}})
	rec := get(consumer, "/iudx/v2/acl/admin")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	// A 403 that enumerates what would have worked is a probing oracle.
	if bodyContains(rec.Body.String(), "org_admin") || bodyContains(rec.Body.String(), "cos_admin") {
		t.Errorf("the 403 body names the required roles: %s", rec.Body.String())
	}
}

func TestRouter_RoleGateStill401sWithoutASubject(t *testing.T) {
	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/iudx/v2/acl", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: denyAll()}},
		httpx.Routes("/admin", httpx.GET("", okHandler(), httpx.Roles("cos_admin"))),
	)
	if rec := get(r, "/iudx/v2/acl/admin"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (not 403) when there is no subject at all", rec.Code)
	}
}

func TestRouter_PrefixJoining(t *testing.T) {
	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/iudx/v2/acl", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})}},
		httpx.Routes("/policies",
			httpx.GET("", okHandler()),
			httpx.GET("/{id}", okHandler()),
		),
		httpx.Routes("", httpx.GET("/health-ish", okHandler())),
	)

	for _, path := range []string{
		"/iudx/v2/acl/policies",
		"/iudx/v2/acl/policies/7f3a",
		"/iudx/v2/acl/health-ish",
	} {
		if rec := get(r, path); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
		}
	}
}

func TestRouter_CustomMiddlewareRuns(t *testing.T) {
	var ran bool
	r := httpx.NewRouter(
		httpx.RouterSpec{
			Base: "/iudx/v2/acl", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})},
			Middleware: []func(http.Handler) http.Handler{
				func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						ran = true
						next.ServeHTTP(w, r)
					})
				},
			},
		},
		httpx.Routes("/policies", httpx.GET("", okHandler())),
	)
	get(r, "/iudx/v2/acl/policies")
	if !ran {
		t.Error("configured middleware did not run")
	}
}

func TestRouter_MethodNotAllowed(t *testing.T) {
	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/iudx/v2/acl", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})}},
		httpx.Routes("/policies", httpx.GET("", okHandler())),
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/iudx/v2/acl/policies", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func bodyContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestRouter_ProtectedIsAPropertyOfTheRouteNotTheHandler is a regression test
// for a real bug in the first cut of this router.
//
// okHandler takes httpx.None, so it has no embedded Actor and produces no 401
// of its own. If the router relies on Actor binding to enforce authentication,
// such a route is silently PUBLIC despite sitting in the protected group —
// exactly the "public by accident" mistake nobody catches in review.
func TestRouter_ProtectedIsAPropertyOfTheRouteNotTheHandler(t *testing.T) {
	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/iudx/v2/acl", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: denyAll()}},
		// The handler never looks at the caller.
		httpx.Routes("/policies", httpx.GET("", okHandler())),
	)

	if rec := get(r, "/iudx/v2/acl/policies"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — a non-Public route must be enforced regardless of the handler's request type", rec.Code)
	}
}

// TestAuthSpec_ZeroValueIsSafe: AllowAnonymous is spelled as an opt-OUT so a
// service that forgets to configure it gets authentication ENFORCED. A field
// named RequireAuth would default to false and quietly open the whole group.
func TestAuthSpec_ZeroValueIsSafe(t *testing.T) {
	var zero httpx.AuthSpec
	zero.Authenticate = denyAll()

	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/iudx/v2/acl", URNs: urns, Auth: zero},
		httpx.Routes("/policies", httpx.GET("", okHandler())),
	)
	if rec := get(r, "/iudx/v2/acl/policies"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; the zero AuthSpec must enforce authentication", rec.Code)
	}
}

// TestRouter_RecoversFromHandlerPanic.
//
// The router had NO panic recovery in its first cut — found when migrating
// dx-registry-go, whose route test panicked on a nil service and took the test
// binary with it. In production that is worse: one nil dereference in one
// handler kills every in-flight request on the replica, because Go's default
// response to an unrecovered panic is to crash the process.
func TestRouter_RecoversFromHandlerPanic(t *testing.T) {
	boom := httpx.Handle(func(context.Context, httpx.None) (map[string]string, error) {
		var m map[string]string
		_ = m["key"]           // fine
		var p *struct{ N int } //nolint:staticcheck // deliberate nil deref
		_ = p.N                // panics
		return nil, nil
	}, httpx.WithURNs(urns))

	r := httpx.NewRouter(
		httpx.RouterSpec{Base: "/", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})}},
		httpx.Routes("", httpx.GET("/boom", boom)),
	)

	rec := get(r, "/boom") // must not panic out of ServeHTTP
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	// The panic message and stack go to the log, never to the client: a stack
	// trace names internal paths and package layout.
	if bodyContains(rec.Body.String(), "nil pointer") || bodyContains(rec.Body.String(), ".go:") {
		t.Errorf("panic detail leaked into the response: %s", rec.Body.String())
	}
}

// TestRouter_PanicInMiddlewareIsAlsoRecovered: recovery runs first, so it wraps
// every other middleware, not just the handlers.
func TestRouter_PanicInMiddlewareIsAlsoRecovered(t *testing.T) {
	r := httpx.NewRouter(
		httpx.RouterSpec{
			Base: "/", URNs: urns,
			Auth: httpx.AuthSpec{Authenticate: authAs(identity.Subject{ID: "u-1"})},
			Middleware: []func(http.Handler) http.Handler{
				func(http.Handler) http.Handler {
					return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
						panic("middleware exploded")
					})
				},
			},
		},
		httpx.Routes("", httpx.GET("/x", okHandler())),
	)
	if rec := get(r, "/x"); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
