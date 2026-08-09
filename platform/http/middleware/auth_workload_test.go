package middleware_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datakaveri/dx-common-go/auth"
	"github.com/datakaveri/dx-common-go/platform/http/middleware"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// These tests cover the composition, not the pieces: that the workload gate
// runs BEFORE subject resolution, and that adding it changes nothing for a
// service that has not enabled it.

type realm struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newRealm(t *testing.T) *realm {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	r := &realm{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
			"n": b64.EncodeToString(key.N.Bytes()),
			"e": b64.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *realm) token(t *testing.T, caller, destination string) string {
	t.Helper()
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, gojwt.MapClaims{
		"iss": r.srv.URL,
		"sub": "service-account-" + caller,
		"azp": caller,
		"aud": []string{"account", workload.AudienceFor(destination)},
		"jti": "jti-1",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(r.key)
	require.NoError(t, err)
	return s
}

func (r *realm) verifier(t *testing.T, service string, asserters ...string) *workload.Verifier {
	t.Helper()
	v, err := workload.NewVerifier(workload.VerifierConfig{
		Enforcement:      workload.Required,
		JwksURL:          r.srv.URL + "/protocol/openid-connect/certs",
		Issuer:           r.srv.URL,
		Service:          service,
		SubjectAsserters: asserters,
	})
	require.NoError(t, err)
	return v
}

// assertSubject puts the internal identity headers on a request, exactly as the
// gateway projects them. They are UNSIGNED (ROADMAP P0-17 stage 2), so what
// makes them trustworthy is the workload credential alongside them — which is
// precisely what these tests vary.
func assertSubject(t *testing.T, r *http.Request, userID string) {
	t.Helper()
	h, err := dxheaders.Project(
		auth.DxUser{ID: userID, Email: userID + "@example.org", Roles: []string{"consumer"}})
	require.NoError(t, err)
	dxheaders.Apply(r, h)
}

type observed struct {
	served  bool
	subject identity.Subject
	caller  workload.Principal
}

func (o *observed) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.served = true
		o.subject, _ = identity.From(r.Context())
		o.caller, _ = workload.From(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

// TestResolveWithoutWorkloadVerifierRejectsSubjectHeaders is the INVERSE of the
// test it replaces, and the inversion is the whole of P0-17 stage 2.
//
// The old test asserted an additive guarantee: "16 services configure AuthConfig
// today and none sets Workload; every one of them must behave exactly as
// before" — i.e. a service with no verifier still honoured signed subject
// headers. With the signature deleted, honouring them would mean ANY client
// could name ANY user by setting one header. So a service with no verifier now
// accepts no subject at all.
//
// This is the failure mode the design makes unspellable: there is no
// configuration in which subject headers are trusted without a verified caller.
func TestResolveWithoutWorkloadVerifierRejectsSubjectHeaders(t *testing.T) {
	obs := &observed{}
	mw := middleware.Resolve(middleware.AuthConfig{TrustSubjectHeaders: true})

	r := httptest.NewRequest(http.MethodGet, "http://svc/v1/things", nil)
	assertSubject(t, r, "user-1")

	rec := httptest.NewRecorder()
	mw(obs.handler()).ServeHTTP(rec, r)

	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"with no workload verifier there is nothing authenticating the caller, so a "+
			"client-supplied X-Subject-Id must not be honoured")
	assert.False(t, obs.served)
	assert.Empty(t, obs.subject.ID)
}

// TestResolveRequiresAWorkloadCredential replaces
// TestResolvePermissiveAcceptsBothCredentials, and the replacement IS the fix
// for C-02 (ROADMAP P0-17).
//
// That test asserted an unmigrated caller presenting only the legacy HMAC "must
// keep working during stage 1". There is no stage 1 any more: nothing is
// deployed, so there was never traffic to migrate, and accepting the HMAC alone
// meant an attacker holding the shared secret could simply omit the token.
func TestResolveRequiresAWorkloadCredential(t *testing.T) {
	kc := newRealm(t)
	cfg := middleware.AuthConfig{
		TrustSubjectHeaders: true,
		Workload:            kc.verifier(t, "dx-acl-go", "dx-gateway-go"),
	}

	t.Run("subject headers alone are rejected", func(t *testing.T) {
		obs := &observed{}
		r := httptest.NewRequest(http.MethodGet, "http://svc/v1/things", nil)
		assertSubject(t, r, "user-1")

		rec := httptest.NewRecorder()
		middleware.Resolve(cfg)(obs.handler()).ServeHTTP(rec, r)

		require.Equal(t, http.StatusUnauthorized, rec.Code,
			"subject headers must never be sufficient on their own — that sufficiency IS C-02")
		assert.False(t, obs.served, "the handler ran on a request with no workload credential")
	})

	t.Run("workload token resolves both identities", func(t *testing.T) {
		obs := &observed{}
		r := httptest.NewRequest(http.MethodGet, "http://svc/v1/things", nil)
		assertSubject(t, r, "user-1")
		r.Header.Set(workload.HdrWorkload, "Bearer "+kc.token(t, "dx-gateway-go", "dx-acl-go"))

		rec := httptest.NewRecorder()
		middleware.Resolve(cfg)(obs.handler()).ServeHTTP(rec, r)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "user-1", obs.subject.ID, "the user still resolves")
		assert.Equal(t, "dx-gateway-go", obs.caller.ID, "and the calling workload is known")
	})
}

// The ordering property. A non-asserting workload sending subject headers must
// be stopped BEFORE the resolver reads those headers — otherwise the subject
// would already be established by the time anyone asked whether this caller was
// allowed to establish it.
func TestWorkloadGateRunsBeforeSubjectResolution(t *testing.T) {
	kc := newRealm(t)
	obs := &observed{}

	mw := middleware.Resolve(middleware.AuthConfig{
		TrustSubjectHeaders: true,
		Workload:            kc.verifier(t, "dx-acl-go", "dx-gateway-go"),
	})

	r := httptest.NewRequest(http.MethodGet, "http://svc/v1/things", nil)
	assertSubject(t, r, "user-1") // headers naming a user...
	r.Header.Set(workload.HdrWorkload, "Bearer "+kc.token(t, "dx-catalogue-go", "dx-acl-go"))

	rec := httptest.NewRecorder()
	mw(obs.handler()).ServeHTTP(rec, r)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"...presented by a workload that may not speak for users is refused")
	assert.False(t, obs.served)
	assert.Empty(t, obs.subject.ID)
}

func TestOptionalModeStillAuthenticatesTheWorkload(t *testing.T) {
	kc := newRealm(t)
	obs := &observed{}

	mw := middleware.Resolve(middleware.AuthConfig{
		Mode:                middleware.Optional,
		TrustSubjectHeaders: true,
		Workload:            kc.verifier(t, "dx-acl-go", "dx-gateway-go"),
	})

	// A service-to-service call carrying no user identity at all.
	r := httptest.NewRequest(http.MethodGet, "http://svc/v1/public", nil)
	r.Header.Set(workload.HdrWorkload, "Bearer "+kc.token(t, "dx-catalogue-go", "dx-acl-go"))

	rec := httptest.NewRecorder()
	mw(obs.handler()).ServeHTTP(rec, r)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, obs.subject.ID, "anonymous user")
	assert.Equal(t, "dx-catalogue-go", obs.caller.ID, "but a known caller")
}

func TestInvalidWorkloadCredentialIsNeverDowngraded(t *testing.T) {
	kc := newRealm(t)
	obs := &observed{}

	mw := middleware.Resolve(middleware.AuthConfig{
		TrustSubjectHeaders: true,
		Workload:            kc.verifier(t, "dx-acl-go", "dx-gateway-go"),
	})

	r := httptest.NewRequest(http.MethodGet, "http://svc/v1/things", nil)
	assertSubject(t, r, "user-1")
	// A token minted for a DIFFERENT service, replayed here.
	r.Header.Set(workload.HdrWorkload, "Bearer "+kc.token(t, "dx-gateway-go", "dx-audit-go"))

	rec := httptest.NewRecorder()
	mw(obs.handler()).ServeHTTP(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a bad workload token must not fall back to any other path — that would be a downgrade attack")
	assert.False(t, obs.served)
}
