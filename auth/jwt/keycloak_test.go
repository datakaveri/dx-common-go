package jwt

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
)

// These tests pin ROADMAP P1-9: the configured refresh interval is the interval
// in effect, and the background refresh goroutine stops with the service (on
// Close) and is never left behind by a failed initial fetch.

// jwksHarness serves a JWKS whose key material can be swapped under a FIXED kid,
// and counts fetches. The fixed kid is deliberate: a rotation of the material
// behind a known kid does NOT trigger the unknown-kid refetch, so the only way
// a token signed with the new material becomes valid is the interval refresh —
// which is exactly what "the configured interval is honoured" means.
type jwksHarness struct {
	srv     *httptest.Server
	mu      sync.Mutex
	key     *rsa.PrivateKey
	fetches atomic.Int32
}

func newJWKSHarness(t *testing.T) *jwksHarness {
	t.Helper()
	h := &jwksHarness{key: genRSA(t)}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.jwksJSON())
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *jwksHarness) current() *rsa.PrivateKey {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.key
}

func (h *jwksHarness) rotate(t *testing.T) *rsa.PrivateKey {
	k := genRSA(t)
	h.mu.Lock()
	h.key = k
	h.mu.Unlock()
	return k
}

func (h *jwksHarness) jwksJSON() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	b64 := base64.RawURLEncoding
	n := b64.EncodeToString(h.key.PublicKey.N.Bytes())
	e := b64.EncodeToString(big.NewInt(int64(h.key.PublicKey.E)).Bytes())
	return map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID, "n": n, "e": e,
	}}}
}

func genRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return k
}

func testConfig(url string, refresh time.Duration) Config {
	return Config{
		JwksURL:         url,
		Issuer:          "https://kc/realms/iudx",
		Audience:        "account",
		RefreshInterval: refresh,
	}
}

// TestJWKS_RefreshIntervalIsHonoured proves the configured cadence takes effect.
// Before the fix, cfg.RefreshInterval was computed and discarded and the client
// refreshed hourly, so this rotation would never be picked up inside the window.
func TestJWKS_RefreshIntervalIsHonoured(t *testing.T) {
	h := newJWKSHarness(t)
	v, err := New(testConfig(h.srv.URL, 100*time.Millisecond))
	if err != nil {
		t.Fatalf("New validator: %v", err)
	}
	defer v.Close() //nolint:errcheck // stop the refresh goroutine at test end

	if _, err := v.Validate(sign(t, h.current(), gojwt.SigningMethodRS256, testKID, baseClaims())); err != nil {
		t.Fatalf("original key must validate: %v", err)
	}

	// Rotate the material behind the SAME kid; only an interval refresh can make
	// the new signature valid (the kid is known, so no unknown-kid refetch).
	newKey := h.rotate(t)
	newTok := sign(t, newKey, gojwt.SigningMethodRS256, testKID, baseClaims())

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := v.Validate(newTok); err == nil {
			break // picked up via the configured interval
		}
		if time.Now().After(deadline) {
			t.Fatal("rotated key material was not picked up within the configured refresh interval")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestJWKS_CloseStopsRefreshing proves shutdown cancels refresh work: after
// Close, the background goroutine is gone and no further fetches occur.
func TestJWKS_CloseStopsRefreshing(t *testing.T) {
	h := newJWKSHarness(t)
	jwks, err := NewKeycloakJWKS(testConfig(h.srv.URL, 50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewKeycloakJWKS: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return h.fetches.Load() >= 3 },
		"the background refresh should have fetched at least 3 times")

	if err := jwks.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Let any in-flight refresh settle, then assert the count is frozen: a live
	// goroutine on a 50ms ticker would add ~8 fetches over the next 400ms.
	time.Sleep(100 * time.Millisecond)
	settled := h.fetches.Load()
	time.Sleep(400 * time.Millisecond)
	if got := h.fetches.Load(); got != settled {
		t.Fatalf("refresh continued after Close: %d fetches before, %d after", settled, got)
	}
}

// TestJWKS_BootResilientWhenEndpointDown proves the deliberate boot-resilience
// (see the constructor doc): an endpoint that fails the initial fetch does NOT
// crash construction, the refresh goroutine keeps retrying, and — the leak
// property — Close still cleans it up so nothing is left behind.
func TestJWKS_BootResilientWhenEndpointDown(t *testing.T) {
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	jwks, err := NewKeycloakJWKS(testConfig(srv.URL, 50*time.Millisecond))
	if err != nil {
		t.Fatalf("a transiently-failing endpoint must not fail construction: %v", err)
	}

	// The goroutine keeps retrying past the one synchronous initial fetch.
	waitFor(t, 2*time.Second, func() bool { return fetches.Load() >= 3 },
		"the refresh goroutine should keep retrying after a failed initial fetch")

	if err := jwks.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // let any in-flight request settle
	settled := fetches.Load()
	time.Sleep(400 * time.Millisecond)
	if got := fetches.Load(); got != settled {
		t.Fatalf("refresh continued after Close: %d fetches before, %d after", settled, got)
	}
}

// TestJWKS_MalformedURLFails proves the genuine construction-error path returns
// an error cleanly (rather than panicking), and by construction starts no
// goroutine to leak — the URL is rejected before any refresh loop launches.
func TestJWKS_MalformedURLFails(t *testing.T) {
	if _, err := NewKeycloakJWKS(testConfig("http://%zz", 50*time.Millisecond)); err == nil {
		t.Fatal("a malformed JWKS URL must fail construction")
	}
}

func waitFor(t *testing.T, within time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
