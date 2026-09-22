package attestation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

func newIssuerVerifier(t *testing.T, audience string) (*Issuer, *Verifier) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := NewIssuer("dx-authz-go", "kid1", priv, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(Config{
		Audience: audience,
		Keys:     StaticKey(iss.Kid(), iss.PublicKey()),
		Issuer:   "dx-authz-go",
	})
	if err != nil {
		t.Fatal(err)
	}
	return iss, v
}

func requiredRowFilter() decision.Obligation {
	return decision.Obligation{
		Type: decision.ObRowFilter, Version: 1, Required: true,
		Body: map[string]json.RawMessage{"expr": json.RawMessage(`"tenant = 1"`)},
	}
}

func attFor(aud string, obs ...decision.Obligation) Attestation {
	return Attestation{
		Audience:     aud,
		ResourceType: "resource", ResourceID: "r1", OperationID: "op1",
		ExpiresAt:   time.Now().Add(time.Minute),
		Obligations: obs,
	}
}

func TestEnforce_ValidAndSupported(t *testing.T) {
	iss, v := newIssuerVerifier(t, "dx-dataplane-ogc")
	token, err := iss.Issue(attFor("dx-dataplane-ogc", requiredRowFilter()))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEnforcer(v, []string{"row_filter@1"})
	att, err := e.Enforce(token, "op1", "resource", "r1")
	if err != nil {
		t.Fatalf("valid attestation rejected: %v", err)
	}
	if len(att.Obligations) != 1 || att.Obligations[0].Type != decision.ObRowFilter {
		t.Errorf("obligations not returned to the handler: %+v", att.Obligations)
	}
}

func TestEnforce_RequiredObligationUnsupported(t *testing.T) {
	iss, v := newIssuerVerifier(t, "dx-dataplane-ogc")
	token, _ := iss.Issue(attFor("dx-dataplane-ogc", requiredRowFilter()))
	// This data plane advertises NO capabilities — a required row_filter must
	// force a deny (I-7), never a silent drop.
	e := NewEnforcer(v, nil)
	if _, err := e.Enforce(token, "op1", "resource", "r1"); err == nil {
		t.Fatal("expected deny for an unsupported required obligation")
	}
}

func TestEnforce_WrongAudience(t *testing.T) {
	iss, _ := newIssuerVerifier(t, "dx-dataplane-ogc")
	// Verifier for a DIFFERENT data plane must reject a token minted for ogc.
	_, vRS := newIssuerVerifier(t, "dx-dataplane-rs")
	token, _ := iss.Issue(attFor("dx-dataplane-ogc", requiredRowFilter()))
	e := NewEnforcer(vRS, []string{"row_filter@1"})
	if _, err := e.Enforce(token, "op1", "resource", "r1"); err == nil {
		t.Fatal("expected audience rejection (anti-redirect)")
	}
}

func TestEnforce_ResourceBindingMismatch(t *testing.T) {
	iss, v := newIssuerVerifier(t, "dx-dataplane-ogc")
	token, _ := iss.Issue(attFor("dx-dataplane-ogc"))
	e := NewEnforcer(v, []string{"row_filter@1"})
	if _, err := e.Enforce(token, "op1", "resource", "OTHER"); err == nil {
		t.Fatal("expected resource-binding mismatch to be rejected")
	}
}

func TestEnforce_Expired(t *testing.T) {
	iss, v := newIssuerVerifier(t, "dx-dataplane-ogc")
	a := attFor("dx-dataplane-ogc")
	a.ExpiresAt = time.Now().Add(-time.Minute)
	token, _ := iss.Issue(a)
	e := NewEnforcer(v, nil)
	if _, err := e.Enforce(token, "op1", "resource", "r1"); err == nil {
		t.Fatal("expected expired attestation to be rejected")
	}
}
