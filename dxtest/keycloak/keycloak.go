// Package keycloak is a fake Keycloak realm for tests.
//
// It exists so the platform's workload-identity behaviour can be exercised
// without a container, and so there is ONE definition of "how the realm
// behaves" that cannot drift between the packages that depend on it.
//
// The behaviours modelled are the ones that carry security weight, and they are
// modelled as the real server actually behaves — verified against Keycloak 26.3
// on 2026-08-07:
//
//   - An optional client scope the requesting client does not hold is REFUSED
//     with HTTP 400 invalid_scope. The permitted call graph is enforced at the
//     token endpoint, not merely unusable at the destination.
//   - A scope that IS held adds its audience to the access token via the
//     scope's audience mapper.
//   - The realm may publish several signing keys at once. That overlap is what
//     makes key rotation lossless.
//
// BreakAudienceMapperFor models the one misconfiguration the token endpoint
// cannot catch: a scope correctly assigned whose audience mapper is missing, so
// a perfectly valid token comes back addressed nowhere useful.
package keycloak

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

// PrimaryKID is the key id the realm signs with until Rotate is called.
const PrimaryKID = "realm-key-1"

// RotatedKID is the key id introduced by Rotate.
const RotatedKID = "realm-key-2"

// Realm is a running fake realm. Create one with New; it shuts down with the
// test.
type Realm struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	signing       []realmKey
	activeKID     string
	grants        map[string][]string
	secrets       map[string]string
	brokenMappers map[string]bool
	mints         map[string]int
	expiresIn     int
}

type realmKey struct {
	kid string
	key *rsa.PrivateKey
}

// New starts a fake realm serving a JWKS and a token endpoint.
func New(t *testing.T) *Realm {
	t.Helper()
	r := &Realm{
		t:             t,
		activeKID:     PrimaryKID,
		grants:        map[string][]string{},
		secrets:       map[string]string{},
		brokenMappers: map[string]bool{},
		mints:         map[string]int{},
		expiresIn:     300,
	}
	r.signing = []realmKey{{kid: PrimaryKID, key: GenerateKey(t)}}

	mux := http.NewServeMux()
	mux.HandleFunc("/protocol/openid-connect/certs", r.serveJWKS)
	mux.HandleFunc("/protocol/openid-connect/token", r.serveToken)
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

// GenerateKey returns a fresh RSA key, for tests that need one the realm does
// not publish (proving a forged signature is rejected).
func GenerateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keycloak: generate RSA key: %v", err)
	}
	return key
}

// Issuer returns the realm's issuer URL, the value tokens carry as `iss`.
func (r *Realm) Issuer() string { return r.srv.URL }

// JWKSURL returns the realm's public key endpoint.
func (r *Realm) JWKSURL() string { return r.srv.URL + "/protocol/openid-connect/certs" }

// TokenURL returns the realm's token endpoint.
func (r *Realm) TokenURL() string { return r.srv.URL + "/protocol/openid-connect/token" }

// RegisterWorkload creates a confidential client permitted to mint credentials
// for the named destinations, and nothing else.
func (r *Realm) RegisterWorkload(clientID, secret string, destinations ...string) {
	scopes := make([]string, 0, len(destinations))
	for _, d := range destinations {
		scopes = append(scopes, workload.ScopeFor(d))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets[clientID] = secret
	r.grants[clientID] = scopes
}

// BreakAudienceMapperFor makes the destination's scope grantable but drops its
// audience from the minted token — the "assigned scope, missing mapper"
// misconfiguration that the token endpoint cannot detect.
func (r *Realm) BreakAudienceMapperFor(destination string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.brokenMappers[workload.ScopeFor(destination)] = true
}

// Rotate publishes a second signing key and starts signing with it, leaving the
// first published. That overlap is a real realm rollover.
func (r *Realm) Rotate() {
	key := GenerateKey(r.t)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signing = append(r.signing, realmKey{kid: RotatedKID, key: key})
	r.activeKID = RotatedKID
}

// MintCount reports how many token requests a client has authenticated, for
// asserting that credentials are cached rather than re-minted per request.
func (r *Realm) MintCount(clientID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mints[clientID]
}

// KeyByKID returns a published signing key, for signing with a specific one.
func (r *Realm) KeyByKID(kid string) *rsa.PrivateKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.signing {
		if k.kid == kid {
			return k.key
		}
	}
	return nil
}

// WorkloadClaims is a well-formed workload token body addressed to destination,
// which a test then corrupts one field at a time.
func (r *Realm) WorkloadClaims(clientID, destination string) gojwt.MapClaims {
	return gojwt.MapClaims{
		"iss": r.Issuer(),
		"sub": "service-account-" + clientID,
		"azp": clientID,
		"aud": []string{"account", workload.AudienceFor(destination)},
		"jti": "jti-test",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	}
}

// Sign signs claims with the realm's currently active key.
func (r *Realm) Sign(claims gojwt.MapClaims) string {
	r.mu.Lock()
	kid := r.activeKID
	var key *rsa.PrivateKey
	for _, k := range r.signing {
		if k.kid == kid {
			key = k.key
			break
		}
	}
	r.mu.Unlock()
	return r.SignWith(kid, key, claims)
}

// SignWith signs claims with a specific key and key id. Passing a key the realm
// does not publish produces a token no verifier should accept.
func (r *Realm) SignWith(kid string, key *rsa.PrivateKey, claims gojwt.MapClaims) string {
	r.t.Helper()
	if key == nil {
		r.t.Fatalf("keycloak: no signing key for kid %q", kid)
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		r.t.Fatalf("keycloak: sign token: %v", err)
	}
	return s
}

// VerifierConfig returns a config for a service verifying callers against this
// realm, with Required enforcement.
func (r *Realm) VerifierConfig(service string) workload.VerifierConfig {
	return workload.VerifierConfig{
		Enforcement: workload.Required,
		JwksURL:     r.JWKSURL(),
		Issuer:      r.Issuer(),
		Service:     service,
	}
}

// Verifier builds a verifier for service, applying any config adjustments.
func (r *Realm) Verifier(service string, mutate ...func(*workload.VerifierConfig)) *workload.Verifier {
	r.t.Helper()
	cfg := r.VerifierConfig(service)
	for _, m := range mutate {
		m(&cfg)
	}
	v, err := workload.NewVerifier(cfg)
	if err != nil {
		r.t.Fatalf("keycloak: build verifier for %s: %v", service, err)
	}
	return v
}

func (r *Realm) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	keys := make([]map[string]any, 0, len(r.signing))
	for _, k := range r.signing {
		b64 := base64.RawURLEncoding
		keys = append(keys, map[string]any{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": k.kid,
			"n":   b64.EncodeToString(k.key.N.Bytes()),
			"e":   b64.EncodeToString(big.NewInt(int64(k.key.E)).Bytes()),
		})
	}
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

func (r *Realm) serveToken(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	clientID := req.PostForm.Get("client_id")
	secret := req.PostForm.Get("client_secret")
	scope := req.PostForm.Get("scope")

	r.mu.Lock()
	want, known := r.secrets[clientID]
	granted := r.grants[clientID]
	broken := r.brokenMappers[scope]
	if known && want == secret {
		r.mints[clientID]++
	}
	expiresIn := r.expiresIn
	r.mu.Unlock()

	if !known || want != secret {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}

	// Keycloak 26.3 refuses a scope the client does not hold, rather than
	// dropping it. This is what makes the scope assignment an enforced call
	// graph rather than a hint.
	if scope != "" && !slicesContains(granted, scope) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope")
		return
	}

	aud := []string{"account"}
	if scope != "" && !broken {
		if a := audienceOfScope(scope); a != "" {
			aud = append(aud, a)
		}
	}

	claims := gojwt.MapClaims{
		"iss": r.Issuer(),
		"sub": "service-account-" + clientID,
		"azp": clientID,
		"aud": aud,
		"jti": "jti-" + clientID + "-" + scope,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Duration(expiresIn) * time.Second).Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": r.Sign(claims),
		"expires_in":   expiresIn,
		"token_type":   "Bearer",
	})
}

func writeOAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// audienceOfScope mirrors the realm's audience mapper: scope dx-aud:X carries
// audience dx:svc:X.
func audienceOfScope(scope string) string {
	dest, ok := strings.CutPrefix(scope, workload.ScopeFor(""))
	if !ok || dest == "" {
		return ""
	}
	return workload.AudienceFor(dest)
}

func slicesContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
