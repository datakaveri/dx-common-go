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
func TestWorkloadCredentialAccompaniesThePseudoUser(t *testing.T) {
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
		BaseURL:      srv.URL,
		SharedSecret: "dev-secret",
		ServiceName:  "gateway",
		Workload:     src,
	})
	if err != nil {
		t.Fatalf("fga.New: %v", err)
	}
	if _, err := c.Check(context.Background(), checkReq()); err != nil {
		t.Fatalf("Check: %v", err)
	}

	if gotSubject != "svc:gateway" {
		t.Fatalf("X-Subject-Id = %q, the legacy identity must still travel during the rollout", gotSubject)
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

// While the pseudo-user is still being sent, a Keycloak outage must not take
// the PDP path down with it: an authorization call that fails to mint is worse
// than one that falls back. After stage 3 removes SharedSecret this inverts.
func TestMintingFailureDoesNotBreakTheAuthorizationPathYet(t *testing.T) {
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

	withLegacy, err := fga.New(fga.Config{
		BaseURL: srv.URL, SharedSecret: "dev-secret", ServiceName: "catalogue", Workload: src,
	})
	if err != nil {
		t.Fatalf("fga.New: %v", err)
	}
	if _, err := withLegacy.Check(context.Background(), checkReq()); err != nil {
		t.Fatalf("Check must still succeed while the pseudo-user carries it: %v", err)
	}

	// With the shared secret gone there is no fallback identity, so the same
	// failure must surface rather than send an unidentified request.
	noLegacy, err := fga.New(fga.Config{BaseURL: srv.URL, Workload: src})
	if err != nil {
		t.Fatalf("fga.New: %v", err)
	}
	if _, err := noLegacy.Check(context.Background(), checkReq()); err == nil {
		t.Fatal("with no legacy identity, an unmintable credential must fail the call")
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
