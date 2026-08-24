package jwt

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
)

const testKID = "test-key-1"

// jwksServer stands up an httptest endpoint serving a single RSA public key as
// a standard JWKS, and returns the matching private key for signing.
func jwksServer(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	b64 := base64.RawURLEncoding
	n := b64.EncodeToString(key.N.Bytes())
	e := b64.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	jwks := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID, "n": n, "e": e,
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(srv.Close)
	return srv, key
}

func sign(t *testing.T, key *rsa.PrivateKey, method gojwt.SigningMethod, kid string, claims gojwt.MapClaims) string {
	t.Helper()
	tok := gojwt.NewWithClaims(method, claims)
	tok.Header["kid"] = kid
	var (
		s   string
		err error
	)
	if method == gojwt.SigningMethodHS256 {
		s, err = tok.SignedString([]byte("attacker-secret"))
	} else {
		s, err = tok.SignedString(key)
	}
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func newValidator(t *testing.T, jwksURL string) *Validator {
	t.Helper()
	v, err := New(Config{
		JwksURL:  jwksURL,
		Issuer:   "https://kc/realms/iudx",
		Audience: "account",
	})
	if err != nil {
		t.Fatalf("New validator: %v", err)
	}
	return v
}

func baseClaims() gojwt.MapClaims {
	return gojwt.MapClaims{
		"iss": "https://kc/realms/iudx",
		"aud": "account",
		"sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(),
	}
}

func TestValidate_ValidToken(t *testing.T) {
	srv, key := jwksServer(t)
	v := newValidator(t, srv.URL)
	claims, err := v.Validate(sign(t, key, gojwt.SigningMethodRS256, testKID, baseClaims()))
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Fatalf("sub = %q", claims.Subject)
	}
}

func TestValidate_RejectsExpired(t *testing.T) {
	srv, key := jwksServer(t)
	v := newValidator(t, srv.URL)
	c := baseClaims()
	c["exp"] = time.Now().Add(-time.Hour).Unix()
	if _, err := v.Validate(sign(t, key, gojwt.SigningMethodRS256, testKID, c)); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestValidate_RejectsWrongIssuer(t *testing.T) {
	srv, key := jwksServer(t)
	v := newValidator(t, srv.URL)
	c := baseClaims()
	c["iss"] = "https://evil/realms/iudx"
	if _, err := v.Validate(sign(t, key, gojwt.SigningMethodRS256, testKID, c)); err == nil {
		t.Fatal("expected wrong-issuer token to be rejected")
	}
}

func TestValidate_RejectsWrongAudience(t *testing.T) {
	srv, key := jwksServer(t)
	v := newValidator(t, srv.URL)
	c := baseClaims()
	c["aud"] = "some-other-client"
	if _, err := v.Validate(sign(t, key, gojwt.SigningMethodRS256, testKID, c)); err == nil {
		t.Fatal("expected wrong-audience token to be rejected")
	}
}

// The validator pins RS256; an HS256 token (algorithm-confusion attack) must be
// rejected even though it carries a valid kid.
func TestValidate_RejectsAlgorithmConfusion(t *testing.T) {
	srv, key := jwksServer(t)
	v := newValidator(t, srv.URL)
	if _, err := v.Validate(sign(t, key, gojwt.SigningMethodHS256, testKID, baseClaims())); err == nil {
		t.Fatal("expected HS256 token to be rejected (algorithm confusion)")
	}
}

func TestValidate_RejectsUnknownKID(t *testing.T) {
	srv, key := jwksServer(t)
	v := newValidator(t, srv.URL)
	if _, err := v.Validate(sign(t, key, gojwt.SigningMethodRS256, "unknown-kid", baseClaims())); err == nil {
		t.Fatal("expected unknown-kid token to be rejected")
	}
}
