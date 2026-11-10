package fga_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/auth/fga"
	httpx "github.com/datakaveri/dx-common-go/platform/http"
)

// TestCheckDecodesTheRealAuthzEnvelope is the contract test F34 was missing.
//
// The server here renders through httpx.Handle with the same options as
// dx-authz-go's POST /v1/check, so the body is the one authz really sends:
// {"type":…,"title":…,"result":{"allowed":true,…}}. The client unwrapped
// "results" (plural), missed the payload, and read every decision as denied —
// the gateway then returned 403 even when authz said allowed.
func TestCheckDecodesTheRealAuthzEnvelope(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		srv := httptest.NewServer(httpx.Handle(
			func(_ context.Context, req fga.CheckRequest) (fga.CheckResponse, error) {
				return fga.CheckResponse{
					Allowed:  allowed,
					Subject:  "user:" + req.SubjectID,
					Object:   req.ResourceType + ":" + req.ResourceID,
					Relation: req.Relation,
				}, nil
			},
			httpx.WithURNs(httpx.URNSpace("authz")),
			httpx.WithMessage("Authorization check", "decision computed"),
		))

		c, err := fga.New(fga.Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Check(context.Background(), fga.CheckRequest{
			SubjectType: fga.SubjectTypeUser, SubjectID: "u1",
			ResourceType: "resource", ResourceID: "r1", Relation: "query",
		})
		srv.Close()
		if err != nil {
			t.Fatalf("allowed=%v: Check: %v", allowed, err)
		}
		if resp.Allowed != allowed {
			t.Fatalf("authz answered allowed=%v, client read %v", allowed, resp.Allowed)
		}
		if resp.Object != "resource:r1" || resp.Relation != "query" {
			t.Fatalf("payload not decoded from the envelope: %+v", resp)
		}
	}
}

// The older "results" envelope and a bare body must keep working.
func TestCheckDecodesLegacyBodies(t *testing.T) {
	for name, body := range map[string]string{
		"results envelope": `{"type":"urn:dx:authz:success","results":{"allowed":true}}`,
		"bare body":        `{"allowed":true}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		c, err := fga.New(fga.Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Check(context.Background(), fga.CheckRequest{
			SubjectType: fga.SubjectTypeUser, SubjectID: "u1",
			ResourceType: "resource", ResourceID: "r1", Relation: "query",
		})
		srv.Close()
		if err != nil {
			t.Fatalf("%s: Check: %v", name, err)
		}
		if !resp.Allowed {
			t.Fatalf("%s: allowed lost", name)
		}
	}
}
