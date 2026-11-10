package grant

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/constraint"
	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// TestEventRoundTrip proves the envelope survives JSON — including the two
// non-trivial nested types (ConditionSet and the custom-marshalled Obligation),
// since drift there is exactly what a shared contract exists to prevent.
func TestEventRoundTrip(t *testing.T) {
	nb := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	na := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	want := Event{
		Action:    ActionUpserted,
		RequestID: "req-1",
		Timestamp: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		Grant: Projection{
			GrantID:      "grant-1",
			Version:      1,
			ResourceType: "resource",
			ResourceID:   "urn:res:1",
			Permission:   "query",
			Status:       StatusActive,
			UserIDs:      []string{"u1", "u2"},
			OrgIDs:       []string{"o1"},
			Roles:        []string{"consumer"},
			Conditions: constraint.ConditionSet{
				Version:      1,
				NotBefore:    &nb,
				NotAfter:     &na,
				AllowedRoles: []string{"consumer"},
			},
			Obligations: []decision.Obligation{{
				Type:     decision.ObDeliveryMode,
				Version:  1,
				Required: true,
				Body:     map[string]json.RawMessage{"mode": json.RawMessage(`"api"`)},
			}},
		},
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Action != want.Action || got.RequestID != want.RequestID {
		t.Errorf("envelope mismatch: got %+v", got)
	}
	if got.Grant.GrantID != "grant-1" || got.Grant.Permission != "query" || got.Grant.Status != StatusActive {
		t.Errorf("grant core mismatch: got %+v", got.Grant)
	}
	if got.Grant.Conditions.NotAfter == nil || !got.Grant.Conditions.NotAfter.Equal(na) {
		t.Errorf("condition not_after lost: got %+v", got.Grant.Conditions)
	}
	if len(got.Grant.Obligations) != 1 || got.Grant.Obligations[0].Type != decision.ObDeliveryMode {
		t.Fatalf("obligation lost: got %+v", got.Grant.Obligations)
	}
	if b := got.Grant.Obligations[0].Body["mode"]; string(b) != `"api"` {
		t.Errorf("obligation body lost: got %s", b)
	}
}

func TestProjectionValidate(t *testing.T) {
	base := func() Projection {
		return Projection{
			GrantID: "g1", ResourceType: "resource", ResourceID: "r1",
			Permission: "query", Status: StatusActive, UserIDs: []string{"u1"},
		}
	}
	tests := []struct {
		name    string
		mutate  func(p *Projection)
		wantErr bool
	}{
		{"valid active", func(*Projection) {}, false},
		{"missing grant id", func(p *Projection) { p.GrantID = "" }, true},
		{"missing resource", func(p *Projection) { p.ResourceID = "" }, true},
		{"missing permission", func(p *Projection) { p.Permission = "" }, true},
		{"bad status", func(p *Projection) { p.Status = "weird" }, true},
		{"active applies to no one", func(p *Projection) { p.UserIDs = nil }, true},
		{"revoked tombstone needs no subject", func(p *Projection) { p.Status = StatusRevoked; p.UserIDs = nil }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			tc.mutate(&p)
			err := p.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
