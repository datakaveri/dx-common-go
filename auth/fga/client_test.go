package fga

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/transport/headers"
)

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing BaseURL must fail")
	}
	if _, err := New(Config{BaseURL: "http://x"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

// TestClientAssertsNoPseudoUser replaces TestServiceIdentitySigned, which
// asserted the defect as correct (ROADMAP P0-17 stage 2).
//
// The client used to sign X-Subject-* headers naming "svc:<ServiceName>" with
// role "service" — a FABRICATED user — as its service identity, and the old test
// verified that signature and checked the id was `svc:gateway`. It worked only
// because every holder of the shared secret could mint any subject, which is
// review finding C-02 exactly.
//
// The client's identity is now the workload credential (ADR-06), and the pseudo
// user is gone. This asserts its ABSENCE: a request carrying no Workload must
// name no subject at all, rather than inventing one.
func TestClientAssertsNoPseudoUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if headers.Asserts(r.Header) {
			t.Errorf("the FGA client asserted a subject %q; it has no user to speak for and "+
				"must not fabricate one", r.Header.Get(headers.HdrSubjectID))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allowed":true}`))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, ServiceName: "gateway"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Check(context.Background(), CheckRequest{
		SubjectType: SubjectTypeUser, SubjectID: "u1",
		ResourceType: "databank", ResourceID: "r1", Relation: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Allowed {
		t.Fatal("expected allowed=true round trip")
	}
}
