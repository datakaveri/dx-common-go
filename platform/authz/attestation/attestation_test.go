package attestation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

func newPair(t *testing.T) (*Issuer, *Verifier, func() time.Time) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clk := func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }
	iss, err := NewIssuer("dx-authz-go", "k1", priv, clk)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := NewVerifier(Config{Audience: "dx-dataplane-ogc-go", Keys: StaticKey("k1", pub), Issuer: "dx-authz-go", Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return iss, ver, clk
}

func sampleAtt(clk func() time.Time) Attestation {
	rf, _ := decision.NewObligation(decision.ObRowFilter, 1, decision.RowFilter{Expression: json.RawMessage(`{"op":"eq"}`)})
	return Attestation{
		Audience: "dx-dataplane-ogc-go", Subject: "user-1", ActorChain: []string{"user-1", "agent-9"},
		ExpiresAt: clk().Add(EnforcementTTL), EvaluationID: "dec_1", OperationID: "ogc.items",
		ResourceType: "resource", ResourceID: "r1", Obligations: []decision.Obligation{rf},
	}
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	iss, ver, clk := newPair(t)
	tok, err := iss.Issue(sampleAtt(clk))
	if err != nil {
		t.Fatal(err)
	}
	att, err := ver.VerifyBound(tok, "ogc.items", "resource", "r1")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if att.Subject != "user-1" || len(att.Obligations) != 1 || att.Obligations[0].Type != decision.ObRowFilter {
		t.Fatalf("claims lost: %+v", att)
	}
	if len(att.ActorChain) != 2 {
		t.Fatalf("actor chain lost: %+v", att.ActorChain)
	}
}

func TestWrongAudienceRejected(t *testing.T) {
	iss, _, clk := newPair(t)
	pub := iss.PublicKey()
	ver, _ := NewVerifier(Config{Audience: "someone-else", Keys: StaticKey("k1", pub), Clock: clk})
	tok, _ := iss.Issue(sampleAtt(clk))
	if _, err := ver.Verify(tok); err == nil {
		t.Fatal("a token for another audience must be rejected (redirect control)")
	}
}

func TestExpiredRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	issueClk := func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }
	iss, _ := NewIssuer("dx-authz-go", "k1", priv, issueClk)
	tok, _ := iss.Issue(sampleAtt(issueClk))
	// Verify 10 minutes later.
	lateClk := func() time.Time { return issueClk().Add(10 * time.Minute) }
	ver, _ := NewVerifier(Config{Audience: "dx-dataplane-ogc-go", Keys: StaticKey("k1", pub), Clock: lateClk})
	if _, err := ver.Verify(tok); err == nil {
		t.Fatal("an expired attestation must be rejected")
	}
}

func TestTamperedSignatureRejected(t *testing.T) {
	iss, ver, clk := newPair(t)
	tok, _ := iss.Issue(sampleAtt(clk))
	// Flip a character in the payload segment.
	parts := strings.Split(tok, ".")
	payload := []byte(parts[1])
	payload[0] ^= 0x01
	tampered := parts[0] + "." + string(payload) + "." + parts[2]
	if _, err := ver.Verify(tampered); err == nil {
		t.Fatal("a tampered token must fail signature verification")
	}
}

func TestResourceBindingMismatchRejected(t *testing.T) {
	iss, ver, clk := newPair(t)
	tok, _ := iss.Issue(sampleAtt(clk))
	if _, err := ver.VerifyBound(tok, "ogc.items", "resource", "OTHER"); err == nil {
		t.Fatal("a resource mismatch must be rejected")
	}
	if _, err := ver.VerifyBound(tok, "different.op", "resource", "r1"); err == nil {
		t.Fatal("an operation mismatch must be rejected")
	}
}

func TestReplayGuardBlocksSecondUse(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	clk := func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }
	iss, _ := NewIssuer("dx-authz-go", "k1", priv, clk)
	ver, _ := NewVerifier(Config{Audience: "dx-dataplane-ogc-go", Keys: StaticKey("k1", pub), Guard: NewMemoryGuardWithClock(clk), Clock: clk})
	tok, _ := iss.Issue(sampleAtt(clk))
	if _, err := ver.Verify(tok); err != nil {
		t.Fatalf("first use should succeed: %v", err)
	}
	if _, err := ver.Verify(tok); err == nil {
		t.Fatal("second use of a single-use attestation must be rejected (replay)")
	}
}

func TestAlgNoneRejected(t *testing.T) {
	_, ver, _ := newPair(t)
	// Craft a token with alg=none and an empty signature.
	hdr := b64.EncodeToString([]byte(`{"alg":"none","typ":"att+jwt"}`))
	payload := b64.EncodeToString([]byte(`{"aud":"dx-dataplane-ogc-go","sub":"x","jti":"1","iat":0,"exp":9999999999,"dx":{}}`))
	forged := hdr + "." + payload + "."
	if _, err := ver.Verify(forged); err == nil {
		t.Fatal(`alg:"none" must be rejected`)
	}
}

func TestBuildFromDecision(t *testing.T) {
	req := decision.EvaluationRequest{
		Subject:  decision.Subject{Type: decision.SubjectIdentity, ID: "user-1"},
		Action:   decision.Action{Name: "query"},
		Resource: decision.Resource{Type: "resource", ID: "r1"},
		Context:  &decision.RequestContext{DX: &decision.DXRequestContext{Profile: decision.ProfileRequestV1, Operation: decision.OperationRef{ID: "ogc.items"}, Actor: &decision.Actor{Type: decision.SubjectAgent, ID: "agent-9"}}},
	}
	resp := decision.Allow("dec_1", decision.ProfileDataAccess)
	fp, _ := decision.NewObligation(decision.ObFieldPolicy, 1, decision.FieldPolicy{Allow: []string{"speed"}})
	resp.Context.DX.Entitlements = []decision.Entitlement{{GrantID: "g1", Obligations: []decision.Obligation{fp}}}

	a := BuildFromDecision(req, resp, "dx-dataplane-ogc-go", time.Now().Add(EnforcementTTL))
	if a.Subject != "user-1" || a.OperationID != "ogc.items" || a.EvaluationID != "dec_1" {
		t.Fatalf("build wrong: %+v", a)
	}
	if len(a.Obligations) != 1 || a.Obligations[0].Type != decision.ObFieldPolicy {
		t.Fatalf("obligations not flattened: %+v", a.Obligations)
	}
	if len(a.ActorChain) != 2 || a.ActorChain[0] != "user-1" || a.ActorChain[1] != "agent-9" {
		t.Fatalf("actor chain wrong: %+v", a.ActorChain)
	}
}
