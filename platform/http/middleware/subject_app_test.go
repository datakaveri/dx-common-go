package middleware_test

import (
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/auth"
	"github.com/datakaveri/dx-common-go/platform/http/middleware"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// An application credential becomes a KindApp delegation naming the app,
// with the app's scopes — also when the app has none (Java: no scopes means
// forScopes refuses; it must not read as a plain user).
func TestSubjectFromAppCredential(t *testing.T) {
	for _, scopes := range [][]auth.DelegationScopeEntry{{{Scope: "data-access"}}, nil} {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(auth.WithUser(r.Context(), auth.DxUser{ID: "owner-1", Roles: []string{"consumer"}, AppID: "app-1", Scopes: scopes}))
		s, ok := middleware.SubjectFrom(r)
		if !ok || s.ID != "owner-1" || s.Delegation == nil || s.Delegation.Kind != identity.KindApp ||
			s.Delegation.Actor != "app-1" || len(s.Delegation.Scopes) != len(scopes) {
			t.Fatalf("scopes %v: subject = %+v (delegation %+v)", scopes, s, s.Delegation)
		}
	}
}

// A resolved delegation (F58-r) is a KindUser delegation: the subject is the
// delegator, the actor the delegatee, with the capped scopes — and the app the
// delegatee authenticated with, if any. It wins over the app case.
func TestSubjectFromResolvedDelegation(t *testing.T) {
	for _, app := range []string{"", "app-9"} {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(auth.WithUser(r.Context(), auth.DxUser{ID: "delegator-1", Roles: []string{"provider"},
			DelegateeID: "caller-1", AppID: app, Scopes: []auth.DelegationScopeEntry{{Scope: "data-access"}}}))
		s, ok := middleware.SubjectFrom(r)
		if !ok || s.ID != "delegator-1" || s.Delegation == nil || s.Delegation.Kind != identity.KindUser ||
			s.Delegation.Actor != "caller-1" || s.Delegation.App != app || len(s.Delegation.Scopes) != 1 {
			t.Fatalf("app %q: subject = %+v (delegation %+v)", app, s, s.Delegation)
		}
	}
}
