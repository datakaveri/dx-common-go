package decision

import (
	"encoding/json"
	"testing"
	"time"
)

func TestObligationRoundTrip(t *testing.T) {
	rf, err := NewObligation(ObRowFilter, 1, RowFilter{Expression: json.RawMessage(`{"op":"eq","field":"provider_org_id","value":"org-1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !rf.Required {
		t.Fatal("row_filter must default to required from the core catalogue")
	}
	b, err := json.Marshal(rf)
	if err != nil {
		t.Fatal(err)
	}
	// The wire form must be flat: type/version/required alongside the body.
	var flat map[string]any
	if err := json.Unmarshal(b, &flat); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"type", "version", "required", "expression"} {
		if _, ok := flat[k]; !ok {
			t.Fatalf("marshalled obligation missing %q: %s", k, b)
		}
	}
	var back Obligation
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != ObRowFilter || back.Version != 1 || !back.Required {
		t.Fatalf("envelope not preserved: %+v", back)
	}
	var payload RowFilter
	if err := back.DecodeBody(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Expression) == 0 {
		t.Fatal("body not preserved through round-trip")
	}
}

func TestUnknownRequiredObligationSurvivesAndIsUnsupported(t *testing.T) {
	// A required obligation of a type this PEP has never heard of must still
	// decode (so the PEP can see it) and must count as unsupported (so the PEP
	// denies) — I-7.
	raw := []byte(`{"type":"future_geo_fence","version":3,"required":true,"polygon":[[1,2],[3,4]]}`)
	var o Obligation
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	if o.Type != "future_geo_fence" || !o.Required {
		t.Fatalf("unknown required obligation not preserved: %+v", o)
	}
	if _, ok := o.Body["polygon"]; !ok {
		t.Fatal("unknown obligation body dropped")
	}
	advertised := map[string]bool{"row_filter@1": true}
	if got := UnsupportedRequired([]Obligation{o}, advertised); len(got) != 1 {
		t.Fatalf("unknown required obligation must be unsupported, got %d", len(got))
	}
}

func TestUnsupportedRequiredMatchesVersion(t *testing.T) {
	rf, _ := NewObligation(ObRowFilter, 2, RowFilter{Expression: json.RawMessage(`{}`)})
	// PEP advertises v1 only; a v2 required obligation is unsupported.
	if got := UnsupportedRequired([]Obligation{rf}, map[string]bool{"row_filter@1": true}); len(got) != 1 {
		t.Fatalf("version mismatch must be unsupported, got %d", len(got))
	}
	if got := UnsupportedRequired([]Obligation{rf}, map[string]bool{"row_filter@2": true}); len(got) != 0 {
		t.Fatalf("matching version must be supported, got %d", len(got))
	}
}

func TestAdvisoryObligationNeverBlocks(t *testing.T) {
	tag, _ := NewObligation(ObAuditTag, 1, map[string]any{"tag": "beta"})
	if tag.Required {
		t.Fatal("audit_tag must be advisory")
	}
	if got := UnsupportedRequired([]Obligation{tag}, nil); len(got) != 0 {
		t.Fatalf("advisory obligation must never be unsupported, got %d", len(got))
	}
}

func TestAllowedIsNilSafeAndHonoursComplete(t *testing.T) {
	if (*EvaluationResponse)(nil).Allowed() {
		t.Fatal("nil response must not be allowed")
	}
	if (&EvaluationResponse{Decision: false}).Allowed() {
		t.Fatal("deny must not be allowed")
	}
	inc := Incomplete("e1", ProfileDataAccess, ReasonProjectionIncomplete)
	if inc.Decision || inc.Allowed() {
		t.Fatal("incomplete must be a fail-closed deny")
	}
	ok := Allow("e2", ProfileRelationship)
	if !ok.Allowed() {
		t.Fatal("a complete allow must be allowed")
	}
}

func TestRequestValidation(t *testing.T) {
	valid := EvaluationRequest{
		Subject:  Subject{Type: SubjectIdentity, ID: "user-1"},
		Action:   Action{Name: "query"},
		Resource: Resource{Type: "resource", ID: "ds-1"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*EvaluationRequest)
	}{
		{"empty subject id", func(r *EvaluationRequest) { r.Subject.ID = "" }},
		{"unknown subject type", func(r *EvaluationRequest) { r.Subject.Type = "robot" }},
		{"empty action", func(r *EvaluationRequest) { r.Action.Name = "" }},
		{"empty resource id", func(r *EvaluationRequest) { r.Resource.ID = "" }},
		{"wrong dx profile", func(r *EvaluationRequest) {
			r.Context = &RequestContext{DX: &DXRequestContext{Profile: "urn:dx:wrong", Operation: OperationRef{ID: "op"}}}
		}},
		{"dx without operation id", func(r *EvaluationRequest) {
			r.Context = &RequestContext{DX: &DXRequestContext{Profile: ProfileRequestV1}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := valid
			c.mut(&r)
			if err := r.Validate(); err == nil {
				t.Fatalf("%s: expected validation error, got nil", c.name)
			}
		})
	}
}

func TestUserAliasCanonicalises(t *testing.T) {
	if CanonicalSubjectType(SubjectUserAlias) != SubjectIdentity {
		t.Fatal("user alias must fold onto identity")
	}
	r := EvaluationRequest{Subject: Subject{Type: SubjectUserAlias, ID: "u"}, Action: Action{Name: "read"}, Resource: Resource{Type: "resource", ID: "r"}}
	if err := r.Validate(); err != nil {
		t.Fatalf("user alias must validate during the migration window: %v", err)
	}
}

func TestIsDelegated(t *testing.T) {
	r := EvaluationRequest{
		Subject:  Subject{Type: SubjectIdentity, ID: "u"},
		Action:   Action{Name: "query"},
		Resource: Resource{Type: "resource", ID: "r"},
		Context:  &RequestContext{Agent: "a", DX: &DXRequestContext{Profile: ProfileRequestV1, Operation: OperationRef{ID: "op"}, Actor: &Actor{Type: SubjectAgent, ID: "a"}}},
	}
	if !r.IsDelegated() {
		t.Fatal("actor of type agent must mark the request delegated")
	}
}

func TestFullResponseRoundTrip(t *testing.T) {
	until := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	rf, _ := NewObligation(ObRowFilter, 1, RowFilter{Expression: json.RawMessage(`{"op":"eq","field":"p","value":"org-1"}`)})
	fp, _ := NewObligation(ObFieldPolicy, 1, FieldPolicy{Allow: []string{"speed"}, Mask: map[string]string{"loc": "redact"}})
	resp := EvaluationResponse{
		Decision: true,
		Context: &DecisionContext{
			ReasonUser: "ok",
			DX: &DXDecisionContext{
				Profile: ProfileDecisionV1, EvaluationID: "e1", ReasonCode: ReasonAllowed,
				Complete: true, EvaluatedProfile: ProfileDataAccess, ValidUntil: &until,
				Entitlements: []Entitlement{{GrantID: "g1", GrantVersion: 7, Obligations: []Obligation{rf, fp}}},
			},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var back EvaluationResponse
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Allowed() || back.DXContext().EvaluationID != "e1" {
		t.Fatalf("round-trip lost fields: %s", b)
	}
	if len(back.Context.DX.Entitlements[0].Obligations) != 2 {
		t.Fatal("obligations lost in round-trip")
	}
}
