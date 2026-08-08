package workload_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// spy records what the protected handler actually saw, so a test can assert on
// the identity that reached it rather than only on the status code.
type spy struct {
	served    bool
	principal workload.Principal
	verified  bool
}

func (s *spy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.served = true
		s.principal, s.verified = workload.From(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func serve(t *testing.T, mw func(http.Handler) http.Handler, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mw(h).ServeHTTP(rec, r)
	return rec
}

func request(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, "http://dx-acl-go/v1/policies", nil)
}

// withSubject adds the headers that claim to speak for an end user.
func withSubject(r *http.Request) *http.Request {
	r.Header.Set(dxheaders.HdrSubjectID, "user-42")
	r.Header.Set(dxheaders.HdrSubjectRoles, "consumer")
	return r
}

func TestMiddlewareOffPassesEverythingThrough(t *testing.T) {
	s := &spy{}

	rec := serve(t, workload.Middleware(nil), s.handler(), request(t))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, s.served, "a nil verifier is the pre-rollout default and must change nothing")
	assert.False(t, s.verified, "and must not fabricate a workload identity")
}

// TestMiddlewareRejectsAMissingCredential is the inverse of the test that used
// to live here (TestMiddlewarePermissiveAllowsTheLegacyPath), and the swap IS
// the fix for C-02 (ROADMAP P0-17).
//
// That test asserted a request with no workload credential was allowed through
// to the legacy HMAC path. Its own documentation admitted the consequence: an
// attacker holding the shared secret simply omitted the token. There is nothing
// to fall back to now, so absence is a 401.
func TestMiddlewareRejectsAMissingCredential(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
		c.Enforcement = workload.Required
	})
	s := &spy{}

	rec := serve(t, workload.Middleware(v), s.handler(), request(t))

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a request with no workload credential must not proceed — there is no legacy path to fall back to")
	assert.False(t, s.served, "the handler ran despite no credential being presented")
}

// Invalidity is fatal. Absence used to be tolerated (Permissive); now it is
// not. Falling through on a bad token would hand an attacker a downgrade —
// corrupt your credential and get in on the legacy path instead.
func TestMiddlewareRejectsAnInvalidCredentialInEveryMode(t *testing.T) {
	kc := keycloak.New(t)

	for _, mode := range []workload.Enforcement{workload.Required} {
		t.Run(string(mode), func(t *testing.T) {
			v := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
				c.Enforcement = mode
			})
			s := &spy{}

			r := request(t)
			r.Header.Set(workload.HdrWorkload, "Bearer not-a-real-token")

			rec := serve(t, workload.Middleware(v), s.handler(), r)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.False(t, s.served)
		})
	}
}

func TestMiddlewareRequiredRejectsAMissingCredential(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")
	s := &spy{}

	rec := serve(t, workload.Middleware(v), s.handler(), request(t))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, s.served)
}

func TestMiddlewarePublishesTheVerifiedPrincipal(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")
	s := &spy{}

	r := request(t)
	r.Header.Set(workload.HdrWorkload, "Bearer "+kc.Sign(kc.WorkloadClaims("dx-gateway-go", "dx-acl-go")))

	rec := serve(t, workload.Middleware(v), s.handler(), r)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, s.verified)
	assert.Equal(t, "dx-gateway-go", s.principal.ID)
}

func TestMiddlewareIgnoresAMalformedCredentialHeader(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go")

	tests := []struct {
		name  string
		value string
	}{
		{name: "no scheme", value: "just-a-token"},
		{name: "wrong scheme", value: "Basic dXNlcjpwYXNz"},
		{name: "whitespace", value: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &spy{}
			r := request(t)
			r.Header.Set(workload.HdrWorkload, tt.value)

			rec := serve(t, workload.Middleware(v), s.handler(), r)

			// Under Required these are all "no credential" — a malformed header
			// must never be treated as a valid one, and never crash the check.
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.False(t, s.served)
		})
	}
}

// The gate that stops a compromised ordinary service claiming to be any user it
// likes. Under the shared secret this was not expressible at all.
func TestMiddlewareGatesWhoMayAssertAnEndUserSubject(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-acl-go", func(c *workload.VerifierConfig) {
		c.SubjectAsserters = []string{"dx-gateway-go"}
	})

	tests := []struct {
		name       string
		caller     string
		withUser   bool
		wantStatus int
		reason     string
	}{
		{
			name: "gateway speaking for a user", caller: "dx-gateway-go", withUser: true,
			wantStatus: http.StatusOK,
			reason:     "the gateway resolved the user's JWT; that is its job",
		},
		{
			name: "ordinary service speaking for a user", caller: "dx-catalogue-go", withUser: true,
			wantStatus: http.StatusForbidden,
			reason:     "a service that is not a subject asserter must not be able to impersonate",
		},
		{
			name: "ordinary service speaking for itself", caller: "dx-catalogue-go", withUser: false,
			wantStatus: http.StatusOK,
			reason:     "service-to-service calls carrying no user identity stay allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &spy{}
			r := request(t)
			if tt.withUser {
				r = withSubject(r)
			}
			r.Header.Set(workload.HdrWorkload, "Bearer "+kc.Sign(kc.WorkloadClaims(tt.caller, "dx-acl-go")))

			rec := serve(t, workload.Middleware(v), s.handler(), r)

			assert.Equal(t, tt.wantStatus, rec.Code, tt.reason)
			assert.Equal(t, tt.wantStatus == http.StatusOK, s.served)
		})
	}
}

func TestRequireCallerChecksTheVerifiedWorkload(t *testing.T) {
	kc := keycloak.New(t)
	v := kc.Verifier("dx-agent-registry-go")

	chain := func(next http.Handler) http.Handler {
		return workload.Middleware(v)(workload.RequireCaller("dx-agent-runtime-go")(next))
	}

	tests := []struct {
		name       string
		caller     string
		credential bool
		wantStatus int
		reason     string
	}{
		{
			name: "the named workload", caller: "dx-agent-runtime-go", credential: true,
			wantStatus: http.StatusOK,
		},
		{
			name: "a different workload", caller: "dx-gateway-go", credential: true,
			wantStatus: http.StatusForbidden,
			reason:     "holding a valid credential is not authority to reach an internal route",
		},
		{
			name: "no credential at all", credential: false,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &spy{}
			r := request(t)
			if tt.credential {
				r.Header.Set(workload.HdrWorkload, "Bearer "+kc.Sign(kc.WorkloadClaims(tt.caller, "dx-agent-registry-go")))
			}

			rec := serve(t, chain, s.handler(), r)

			assert.Equal(t, tt.wantStatus, rec.Code, tt.reason)
			assert.False(t, s.served && tt.wantStatus != http.StatusOK)
		})
	}
}

// RequireCaller fails closed when nothing verified the caller. That is
// deliberate: a route guard that silently allows everything because the control
// behind it is switched off is how the defect this replaces survived review.
func TestRequireCallerFailsClosedWithoutVerification(t *testing.T) {
	s := &spy{}

	chain := func(next http.Handler) http.Handler {
		return workload.Middleware(nil)(workload.RequireCaller("dx-agent-runtime-go")(next))
	}

	rec := serve(t, chain, s.handler(), request(t))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, s.served)
}
