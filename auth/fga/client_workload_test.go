package fga_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/auth/fga"
	"github.com/datakaveri/dx-common-go/dxtest/keycloak"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
	"github.com/datakaveri/dx-common-go/platform/security/workload/issuer"
)

// The pseudo-user this client sends — `svc:<ServiceName>` with role "service" —
// is a workload wearing a user's clothes, and any holder of the shared secret
// can mint it for any service name. These tests cover the replacement: a real,
// audience-bound credential addressed to dx-authz-go, sent alongside it.
func TestWorkloadCredentialIsTheOnlyIdentity(t *testing.T) {
	kc := keycloak.New(t)
	kc.RegisterWorkload("dx-gateway-go", "gateway-secret", fga.AuthzWorkload)

	src, err := issuer.New(issuer.Config{
		Enabled: true, TokenURL: kc.TokenURL(),
		ClientID: "dx-gateway-go", ClientSecret: "gateway-secret",
	})
	if err != nil {
		t.Fatalf("issuer.New: %v", err)
	}

	var gotWorkload, gotSubject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWorkload = r.Header.Get(workload.HdrWorkload)
		gotSubject = r.Header.Get("X-Subject-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":{"allowed":true}}`))
	}))
	defer srv.Close()

	c, err := fga.New(fga.Config{
		BaseURL:     srv.URL,
		ServiceName: "gateway",
		Workload:    src,
	})
	if err != nil {
		t.Fatalf("fga.New: %v", err)
	}
	if _, err := c.Check(context.Background(), checkReq()); err != nil {
		t.Fatalf("Check: %v", err)
	}

	// The pseudo-user is GONE (ROADMAP P0-17 stage 2). This assertion used to
	// require X-Subject-Id == "svc:gateway" — a fabricated user that was never
	// a user, and that only worked because any holder of the shared secret
	// could mint any subject. Its absence is now the property.
	if gotSubject != "" {
		t.Fatalf("X-Subject-Id = %q, want none: the client has no user to speak for "+
			"and must not fabricate one", gotSubject)
	}
	if gotWorkload == "" {
		t.Fatal("no workload credential attached")
	}

	p, err := kc.Verifier(fga.AuthzWorkload).Verify(gotWorkload[len("Bearer "):])
	if err != nil {
		t.Fatalf("dx-authz-go rejected the credential: %v", err)
	}
	if p.ID != "dx-gateway-go" {
		t.Fatalf("caller = %q, want dx-gateway-go — the real identity, not a fabricated one", p.ID)
	}
}

// This test previously asserted the OPPOSITE, and said so: "after stage 3
// removes SharedSecret this inverts". It has now inverted (ROADMAP P0-17
// stage 2).
//
// While the fabricated pseudo-user was still being sent, a Keycloak outage
// must not take the PDP path down with it — falling back was better than
// failing. With the pseudo-user deleted there is nothing to fall back TO, so
// proceeding would send an unidentified authorization call that the receiver
// must reject anyway. Failing at the caller is both honest and cheaper.
func TestMintingFailureNowFailsTheAuthorizationCall(t *testing.T) {
	kc := keycloak.New(t)
	// Registered without the dx-authz-go audience, so minting is refused.
	kc.RegisterWorkload("dx-catalogue-go", "catalogue-secret")

	src, err := issuer.New(issuer.Config{
		Enabled: true, TokenURL: kc.TokenURL(),
		ClientID: "dx-catalogue-go", ClientSecret: "catalogue-secret",
	})
	if err != nil {
		t.Fatalf("issuer.New: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":{"allowed":true}}`))
	}))
	defer srv.Close()

	c, err := fga.New(fga.Config{BaseURL: srv.URL, ServiceName: "catalogue", Workload: src})
	if err != nil {
		t.Fatalf("fga.New: %v", err)
	}
	if _, err := c.Check(context.Background(), checkReq()); err == nil {
		t.Fatal("an unmintable credential must fail the call: there is no fallback identity " +
			"left, so the alternative is sending an unidentified authorization request")
	}
}

// checkReq is a minimal valid decision request; its contents are irrelevant
// here — what travels in the HEADERS is the subject of these tests.
func checkReq() fga.CheckRequest {
	return fga.CheckRequest{
		SubjectType:  fga.SubjectTypeUser,
		SubjectID:    "user-1",
		ResourceType: "resource",
		ResourceID:   "r1",
		Relation:     "viewer",
	}
}
