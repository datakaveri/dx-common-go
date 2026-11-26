package auditing

import (
	"testing"

	"github.com/datakaveri/dx-common-go/auth"
)

// SetActor is Java auditing/v2 AuditContextExtractor.getDelegateId /
// getActorType (and rs DataplaneAuditHelper #204): DELEGATE when the gateway
// resolved a did delegation (DelegateeID = Java delegatee_sub), else APP for an
// app credential, else SELF; the legacy v1 delegator keys are cleared.
func TestSetActor(t *testing.T) {
	const (
		delegatee = "8eae65a1-8dfe-47e2-ae10-10f80ca6dca2"
		app       = "5d3c6b1e-7a2f-4c11-9b8e-1f2e3d4c5b6a"
	)
	cases := []struct {
		name              string
		user              auth.DxUser
		actor, delegateID string
	}{
		{"self", auth.DxUser{ID: delegatee}, ActorSelf, ""},
		{"delegate role only", auth.DxUser{ID: delegatee, Roles: []string{"delegate"}, DelegatorID: app}, ActorSelf, ""},
		{"app", auth.DxUser{ID: delegatee, AppID: app}, ActorApp, ""},
		{"delegation", auth.DxUser{DelegateeID: delegatee}, ActorDelegate, delegatee},
		{"delegation via app", auth.DxUser{DelegateeID: delegatee, AppID: app}, ActorDelegate, delegatee},
		{"non-UUID delegatee", auth.DxUser{DelegateeID: "svc:x"}, ActorDelegate, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Record{DelegatorID: app, DelegatorRole: "consumer"}
			r.SetActor(c.user)
			if r.ActorType != c.actor || r.DelegateID != c.delegateID || r.DelegatorID != "" || r.DelegatorRole != "" {
				t.Fatalf("got %+v", r)
			}
		})
	}
	var nilRec *Record
	nilRec.SetActor(auth.DxUser{}) // no panic
}
