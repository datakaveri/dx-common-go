package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// cannedPDP serves a decision computed by fn from the decoded request.
func cannedPDP(t *testing.T, fn func(decision.EvaluationRequest) (int, *decision.EvaluationResponse)) (*Client, *[]decision.EvaluationRequest) {
	t.Helper()
	var seen []decision.EvaluationRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req decision.EvaluationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, req)
		status, resp := fn(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if resp != nil {
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, PEPID: "dx-gateway-go", Capabilities: []string{"row_filter@1", "field_policy@1"}})
	if err != nil {
		t.Fatal(err)
	}
	return c, &seen
}

func req() decision.EvaluationRequest {
	return decision.EvaluationRequest{
		Subject:  decision.Subject{Type: decision.SubjectIdentity, ID: "u1"},
		Action:   decision.Action{Name: "read"},
		Resource: decision.Resource{Type: "resource", ID: "r1"},
	}
}

func TestAuthorizeAllow(t *testing.T) {
	c, _ := cannedPDP(t, func(decision.EvaluationRequest) (int, *decision.EvaluationResponse) {
		return 200, decision.Allow("e1", decision.ProfileRelationship)
	})
	res, err := c.Authorize(context.Background(), req())
	if err != nil || !res.Allowed {
		t.Fatalf("expected allow, got %+v err=%v", res, err)
	}
}

func TestAuthorizeDenyIs200AndNoError(t *testing.T) {
	c, _ := cannedPDP(t, func(decision.EvaluationRequest) (int, *decision.EvaluationResponse) {
		return 200, decision.Deny("e1", decision.ProfileRelationship, decision.ReasonNoRelationship, "nope")
	})
	res, err := c.Authorize(context.Background(), req())
	if err != nil {
		t.Fatalf("a policy deny must not be an error: %v", err)
	}
	if res.Allowed {
		t.Fatal("deny must not be allowed")
	}
}

func TestAuthorizeFailsClosedOnNon200(t *testing.T) {
	c, _ := cannedPDP(t, func(decision.EvaluationRequest) (int, *decision.EvaluationResponse) {
		return 500, nil
	})
	res, err := c.Authorize(context.Background(), req())
	if err == nil {
		t.Fatal("a non-200 must surface an error")
	}
	if res.Allowed {
		t.Fatal("a transport failure must fail closed")
	}
}

func TestAuthorizeDeniesOnUnsupportedRequiredObligation(t *testing.T) {
	// PEP advertises row_filter@1 + field_policy@1 (see cannedPDP). The PDP
	// returns an allow carrying a REQUIRED quota@1 the PEP cannot enforce.
	c, _ := cannedPDP(t, func(decision.EvaluationRequest) (int, *decision.EvaluationResponse) {
		resp := decision.Allow("e1", decision.ProfileDataAccess)
		quota, _ := decision.NewObligation(decision.ObQuota, 1, decision.Quota{Metric: "records", Limit: 100, Window: "month"})
		resp.Context.DX.Entitlements = []decision.Entitlement{{GrantID: "g1", GrantVersion: 1, Obligations: []decision.Obligation{quota}}}
		return 200, resp
	})
	res, err := c.Authorize(context.Background(), req())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("an allow with an unenforceable required obligation must become a deny (I-7)")
	}
	if len(res.Unsupported) != 1 || res.Unsupported[0].Type != decision.ObQuota {
		t.Fatalf("expected quota reported unsupported, got %+v", res.Unsupported)
	}
}

func TestAuthorizeAllowsWhenObligationIsSupported(t *testing.T) {
	c, _ := cannedPDP(t, func(decision.EvaluationRequest) (int, *decision.EvaluationResponse) {
		resp := decision.Allow("e1", decision.ProfileDataAccess)
		rf, _ := decision.NewObligation(decision.ObRowFilter, 1, decision.RowFilter{Expression: json.RawMessage(`{}`)})
		resp.Context.DX.Entitlements = []decision.Entitlement{{GrantID: "g1", Obligations: []decision.Obligation{rf}}}
		return 200, resp
	})
	res, err := c.Authorize(context.Background(), req())
	if err != nil || !res.Allowed {
		t.Fatalf("row_filter@1 is advertised; expected allow, got %+v err=%v", res, err)
	}
}

func TestAuthorizeStampsPEPCapabilities(t *testing.T) {
	c, seen := cannedPDP(t, func(decision.EvaluationRequest) (int, *decision.EvaluationResponse) {
		return 200, decision.Allow("e1", decision.ProfileRelationship)
	})
	if _, err := c.Authorize(context.Background(), req()); err != nil {
		t.Fatal(err)
	}
	got := (*seen)[0]
	if got.Context == nil || got.Context.DX == nil || got.Context.DX.PEP == nil {
		t.Fatalf("PEP info not stamped onto the request: %+v", got.Context)
	}
	if got.Context.DX.PEP.ID != "dx-gateway-go" || len(got.Context.DX.PEP.Capabilities) != 2 {
		t.Fatalf("PEP info wrong: %+v", got.Context.DX.PEP)
	}
}

func TestFakeSatisfiesEvaluator(t *testing.T) {
	f := &Fake{}
	resp, err := f.Evaluate(context.Background(), req())
	if err != nil || !resp.Allowed() {
		t.Fatalf("default fake should allow: %v", err)
	}
	if len(f.Calls) != 1 {
		t.Fatal("fake must record calls")
	}
}

func TestEvaluateBatchTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != decision.PathEvaluations {
			w.WriteHeader(404)
			return
		}
		var req decision.EvaluationsRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := decision.EvaluationsResponse{}
		for range req.Evaluations {
			out.Evaluations = append(out.Evaluations, *decision.Allow("e", decision.ProfileRelationship))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.EvaluateBatch(context.Background(), decision.EvaluationsRequest{
		Subject:     &decision.Subject{Type: decision.SubjectIdentity, ID: "u"},
		Action:      &decision.Action{Name: "read"},
		Evaluations: []decision.EvaluationItem{{Resource: &decision.Resource{Type: "resource", ID: "1"}}, {Resource: &decision.Resource{Type: "resource", ID: "2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Evaluations) != 2 || !res.Evaluations[0].Allowed() {
		t.Fatalf("unexpected batch result: %+v", res)
	}
}

func shadowWith(t *testing.T, fn func(context.Context, decision.EvaluationRequest) (*decision.EvaluationResponse, error)) (*Shadow, <-chan ShadowResult) {
	t.Helper()
	ch := make(chan ShadowResult, 1)
	sh := NewShadow(&Fake{Fn: fn}, time.Second, nil, func(r ShadowResult) { ch <- r })
	return sh, ch
}

func TestShadowDetectsDivergence(t *testing.T) {
	sh, ch := shadowWith(t, func(context.Context, decision.EvaluationRequest) (*decision.EvaluationResponse, error) {
		return decision.Deny("e", decision.ProfileRelationship, decision.ReasonNoRelationship, "no"), nil
	})
	sh.Compare(true, req()) // legacy allowed, AuthZEN denies -> divergence
	r := <-ch
	if !r.Diverged || r.Legacy != true || r.AuthZEN != false {
		t.Fatalf("expected divergence legacy=true authzen=false, got %+v", r)
	}
	if r.ReasonCode != decision.ReasonNoRelationship {
		t.Fatalf("reason not captured: %s", r.ReasonCode)
	}
}

func TestShadowMatchIsNotDivergence(t *testing.T) {
	sh, ch := shadowWith(t, func(context.Context, decision.EvaluationRequest) (*decision.EvaluationResponse, error) {
		return decision.Allow("e", decision.ProfileRelationship), nil
	})
	sh.Compare(true, req())
	if r := <-ch; r.Diverged {
		t.Fatalf("matching decisions must not diverge: %+v", r)
	}
}

func TestShadowEvalErrorCountsAsDivergence(t *testing.T) {
	sh, ch := shadowWith(t, func(context.Context, decision.EvaluationRequest) (*decision.EvaluationResponse, error) {
		return nil, context.DeadlineExceeded
	})
	sh.Compare(true, req())
	r := <-ch
	if !r.Diverged || r.Err == nil {
		t.Fatalf("a failed shadow eval must be a divergence with an error: %+v", r)
	}
}

func TestShadowNilSafe(t *testing.T) {
	var sh *Shadow
	sh.Compare(true, req()) // must not panic
	(&Shadow{}).Compare(true, req())
}
