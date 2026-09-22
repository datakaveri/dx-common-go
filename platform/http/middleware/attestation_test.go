package middleware

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/attestation"
	"github.com/datakaveri/dx-common-go/platform/authz/decision"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

func testEnforcer(t *testing.T, caps []string) (*attestation.Issuer, *attestation.Enforcer) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss, err := attestation.NewIssuer("dx-authz-go", "kid1", priv, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := attestation.NewVerifier(attestation.Config{
		Audience: "dx-dataplane-ogc",
		Keys:     attestation.StaticKey(iss.Kid(), iss.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return iss, attestation.NewEnforcer(v, caps)
}

func resourceFor(string, string, string) func(*http.Request) (string, string, string) {
	return func(*http.Request) (string, string, string) { return "op1", "resource", "r1" }
}

func TestAttestationGuard_Disabled(t *testing.T) {
	called := false
	h := AttestationGuard(AttestationConfig{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if !called {
		t.Fatal("a nil-enforcer guard must pass through")
	}
}

func TestAttestationGuard_RequiredMissing(t *testing.T) {
	_, e := testEnforcer(t, nil)
	h := AttestationGuard(AttestationConfig{Enforcer: e, Resource: resourceFor("", "", ""), Required: true})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing required attestation: status %d, want 403", rec.Code)
	}
}

func TestAttestationGuard_ValidExposesObligations(t *testing.T) {
	iss, e := testEnforcer(t, []string{"delivery_mode@1"})
	ob, err := decision.NewObligation(decision.ObDeliveryMode, 1, map[string]any{"modes": []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := iss.Issue(attestation.Attestation{
		Audience: "dx-dataplane-ogc", ResourceType: "resource", ResourceID: "r1", OperationID: "op1",
		ExpiresAt: time.Now().Add(time.Minute), Obligations: []decision.Obligation{ob},
	})
	if err != nil {
		t.Fatal(err)
	}

	var gotObs []decision.Obligation
	h := AttestationGuard(AttestationConfig{Enforcer: e, Resource: resourceFor("", "", "")})(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { gotObs = ObligationsFrom(r.Context()) }))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(dxheaders.HdrAttestation, token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid attestation rejected: %d", rec.Code)
	}
	if len(gotObs) != 1 || gotObs[0].Type != decision.ObDeliveryMode {
		t.Errorf("obligations not exposed to handler: %+v", gotObs)
	}
}

func TestAttestationGuard_BadTokenDenies(t *testing.T) {
	_, e := testEnforcer(t, nil)
	h := AttestationGuard(AttestationConfig{Enforcer: e, Resource: resourceFor("", "", "")})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") }))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(dxheaders.HdrAttestation, "not.a.valid.token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad token: status %d, want 403", rec.Code)
	}
}
