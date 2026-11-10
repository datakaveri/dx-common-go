package authzgrpc

import (
	"encoding/json"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// canonical re-marshals a value so the comparison is order-stable and ignores
// the difference between a golden literal's whitespace and Go's output.
func canonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// requestVectors are golden AuthZEN requests exercising the whole mirrored
// surface: a bare relationship request, and a fully-populated delegated one with
// free-form properties.
var requestVectors = []string{
	`{"subject":{"type":"identity","id":"u1"},"action":{"name":"query"},"resource":{"type":"resource","id":"r1"}}`,
	`{
	  "subject":{"type":"identity","id":"u1","properties":{"organization_id":"o1","roles":["consumer","x"],"assurance_level":"mfa"}},
	  "action":{"name":"query","properties":{"scope":"read-only"}},
	  "resource":{"type":"resource","id":"urn:res:1"},
	  "context":{
	    "agent":"agent-7",
	    "dx":{
	      "profile":"urn:dx:authzen:req:1",
	      "operation":{"service":"dx-mcp-gateway-go","id":"dataplane.features","manifest_digest":"sha256:abc"},
	      "actor":{"type":"agent","id":"agent-7","delegation_id":"dg-1","session_id":"s-1","chain":["u1","agent-7"]},
	      "pep":{"id":"dx-gateway-go","capabilities":["row_filter@1","delivery_mode@1"]},
	      "purpose":"analytics",
	      "request":{"fields":["a","b"],"delivery_mode":"api","amount":"100","currency":"INR","payee":"prov-1","terms_hash":"sha256:t"},
	      "approval":{"id":"appr-1"}
	    }
	  }
	}`,
}

func TestRequestRoundTrip(t *testing.T) {
	for i, raw := range requestVectors {
		var req decision.EvaluationRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("vector %d unmarshal: %v", i, err)
		}
		want := canonical(t, req)

		pb, err := RequestToProto(req)
		if err != nil {
			t.Fatalf("vector %d ToProto: %v", i, err)
		}
		got := canonical(t, RequestFromProto(pb))

		if got != want {
			t.Errorf("vector %d diverged after gRPC round-trip:\n want %s\n  got %s", i, want, got)
		}
	}
}

var responseVectors = []string{
	`{"decision":true,"context":{"dx":{"profile":"urn:dx:authzen:dec:1","evaluation_id":"ev-1","reason_code":"allowed","complete":true,"evaluated_profile":"data_access","attestation":"eyJ.jws.token","evidence":{"fga_model_id":"m1","grant_projection_revision":"pg-42","condition_schema_version":1}}}}`,
	`{"decision":false,"context":{"reason_user":"not authorised","dx":{"profile":"urn:dx:authzen:dec:1","evaluation_id":"ev-2","reason_code":"no_active_grant","complete":false}}}`,
}

func TestResponseRoundTrip(t *testing.T) {
	for i, raw := range responseVectors {
		var resp decision.EvaluationResponse
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatalf("vector %d unmarshal: %v", i, err)
		}
		want := canonical(t, &resp)
		got := canonical(t, ResponseFromProto(ResponseToProto(&resp)))
		if got != want {
			t.Errorf("response vector %d diverged:\n want %s\n  got %s", i, want, got)
		}
	}
}

// TestNilSafety pins the nil handling both directions.
func TestNilSafety(t *testing.T) {
	if RequestFromProto(nil).Subject.ID != "" {
		t.Error("nil proto request should map to a zero request")
	}
	if ResponseFromProto(nil) != nil {
		t.Error("nil proto response should map to nil")
	}
	if ResponseToProto(nil) != nil {
		t.Error("nil response should map to nil proto")
	}
	pb, err := RequestToProto(decision.EvaluationRequest{})
	if err != nil || pb == nil {
		t.Fatalf("empty request should convert: %v", err)
	}
}
