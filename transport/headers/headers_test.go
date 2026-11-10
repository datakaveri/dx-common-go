package headers

import (
	"net/http"
	"testing"

	"github.com/datakaveri/dx-common-go/auth"
)

// ROADMAP P0-17 stage 2. Twelve tests in this file asserted properties of the
// HMAC — tamper detection, key rotation, replay expiry, canonical-string
// injection — and every one of them is now meaningless rather than failing.
// They were not weakened; the thing they tested was DELETED, because it proved
// only "somebody holding the shared secret produced this" and every service
// held that secret (review finding C-02).
//
// The reasoning is preserved rather than deleted, because the obvious reading of
// this diff is "they removed a signature and the tests that guarded it", and
// that reading is wrong in a specific way:
//
//   - Tamper detection is now the ASSERTER LIST's job, one layer up. A caller
//     that could previously re-sign an escalated role list (any service could)
//     must now be a verified workload named in the receiving service's
//     subject_asserters. See auth/resolver's tests for the property that
//     replaced these.
//   - Replay expiry protected the signature's validity window. With no
//     signature there is no window; the workload credential has its own
//     lifetime, enforced by its issuer.
//   - Key rotation is gone with the key.
//
// What remains testable here is projection and parsing — and the ONE invariant
// that survives unchanged: this package never mints or reads a blank subject id.

func TestProjectParse_RoundTrip(t *testing.T) {
	user := auth.DxUser{
		ID:             "u-1",
		Email:          "u@x.io",
		Roles:          []string{"provider", "consumer"},
		OrganisationID: "org-1",
	}
	h, err := Project(user)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	got, err := Parse(h)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ID != user.ID || got.Email != user.Email || got.OrganisationID != user.OrganisationID {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(got.Roles) != 2 || got.Roles[0] != "consumer" || got.Roles[1] != "provider" {
		t.Fatalf("roles = %v, want them sorted and intact", got.Roles)
	}
}

func TestProjectParse_AgentRoundTrip(t *testing.T) {
	user := auth.DxUser{ID: "u-1", AgentSubject: "agent-7a1b", DelegationID: "dg-01"}
	h, err := Project(user)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if h.Get(HdrAgentSubject) != "agent-7a1b" || h.Get(HdrDelegationID) != "dg-01" {
		t.Fatalf("agent headers not minted: %v", h)
	}
	got, err := Parse(h)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.AgentSubject != user.AgentSubject || got.DelegationID != user.DelegationID {
		t.Fatalf("agent roundtrip mismatch: %+v", got)
	}
	if !got.IsAgent() {
		t.Fatal("IsAgent() = false for a delegated user")
	}
}

// TestProject_RefusesBlankID is the invariant that survived the HMAC's removal
// unchanged, and the one most worth keeping: a blank subject id authenticates an
// anonymous principal downstream and can match records with an empty owner.
func TestProject_RefusesBlankID(t *testing.T) {
	if _, err := Project(auth.DxUser{ID: "  "}); err == nil {
		t.Fatal("a blank subject id must never be projected")
	}
}

// TestParse_RefusesBlankID is its counterpart. Nothing signs these headers now,
// so a caller CAN send a blank id — which makes checking it here matter more
// than it did when only a signer could produce one.
func TestParse_RefusesBlankID(t *testing.T) {
	h := http.Header{}
	h.Set(HdrSubjectID, "   ")
	if _, err := Parse(h); err != ErrNoSubject {
		t.Fatalf("Parse of a blank id = %v, want ErrNoSubject", err)
	}
}

func TestParse_NoHeadersIsNotAnError(t *testing.T) {
	// A service calling on its own behalf asserts no user. That is legitimate,
	// and distinguishing it from "invalid" is what lets a handler decide
	// whether it needs a subject.
	if _, err := Parse(http.Header{}); err != ErrNoSubject {
		t.Fatalf("Parse of empty headers = %v, want ErrNoSubject", err)
	}
}

// TestProject_RefusesCommaInRole keeps the one injection guard that still has
// teeth. Roles are comma-joined into a single header, so a role containing a
// comma would arrive downstream as TWO roles — fabricating a role the user does
// not hold. The '|' guards went with the canonical string; this one did not.
func TestProject_RefusesCommaInRole(t *testing.T) {
	if _, err := Project(auth.DxUser{ID: "u", Roles: []string{"cos_admin,provider"}}); err == nil {
		t.Fatal("a role containing ',' must be refused: it would split into two roles downstream")
	}
}

// TestStripRemovesEveryHeader guards the list, not the loop.
//
// An edge that strips client-supplied subject headers must strip ALL of them; a
// header added to this package but missed by an edge is a header a client can
// set. Deriving Strip from All is what makes that impossible, and this asserts
// the derivation rather than restating the names.
func TestStripRemovesEveryHeader(t *testing.T) {
	h, err := Project(auth.DxUser{
		ID: "u-1", Email: "e@x.io", Roles: []string{"r"},
		OrganisationID: "o", AgentSubject: "a", DelegationID: "d",
		AppID: "app-1", Scopes: []auth.DelegationScopeEntry{{Scope: "data-access"}},
		DelegateeID: "caller-1",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	// Every header this package can mint must be present, or the test would
	// pass by stripping headers that were never set.
	for _, name := range All {
		if h.Get(name) == "" {
			t.Fatalf("Project did not mint %s — All and Project have drifted", name)
		}
	}
	Strip(h)
	for _, name := range All {
		if got := h.Get(name); got != "" {
			t.Errorf("Strip left %s = %q", name, got)
		}
	}
	if Asserts(h) {
		t.Error("a stripped header set must not assert a subject")
	}
}

func TestAsserts(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "absent", id: "", want: false},
		{name: "blank", id: "   ", want: false},
		{name: "present", id: "u-1", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.id != "" {
				h.Set(HdrSubjectID, tt.id)
			}
			if got := Asserts(h); got != tt.want {
				t.Errorf("Asserts = %v, want %v", got, tt.want)
			}
		})
	}
}

// An application credential (Java AppId Basic auth) travels as the owner plus
// the app id and the app's scopes — what Java's AuthenticationHandler puts in
// the principal (sub, app_id, scopes) for the dataplane's forScopes check.
func TestProjectParse_AppRoundTrip(t *testing.T) {
	user := auth.DxUser{
		ID: "owner-1", Roles: []string{"consumer"}, AppID: "app-1",
		Scopes: []auth.DelegationScopeEntry{{Scope: "data-access"}, {Scope: "*"}},
	}
	h, err := Project(user)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if h.Get(HdrSubjectAppID) != "app-1" || h.Get(HdrSubjectScopes) != "*,data-access" {
		t.Fatalf("app headers = %q / %q", h.Get(HdrSubjectAppID), h.Get(HdrSubjectScopes))
	}
	got, err := Parse(h)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.AppID != "app-1" || len(got.Scopes) != 2 || got.Scopes[0].Scope != "*" || got.Scopes[1].Scope != "data-access" {
		t.Fatalf("parsed = %+v", got)
	}
}

// Scopes are projected only for an application credential or a resolved
// delegation: an agent's token scopes keep their own channel (no behaviour
// change there).
func TestProject_ScopesOnlyForApps(t *testing.T) {
	h, err := Project(auth.DxUser{ID: "u-1", AgentSubject: "a", Scopes: []auth.DelegationScopeEntry{{Scope: "x"}}})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if h.Get(HdrSubjectScopes) != "" {
		t.Fatalf("scopes projected for a non-app user: %q", h.Get(HdrSubjectScopes))
	}
	if _, err := Project(auth.DxUser{ID: "u-1", AppID: "a", Scopes: []auth.DelegationScopeEntry{{Scope: "a,b"}}}); err == nil {
		t.Fatal("a scope containing ',' must be refused")
	}
}

// A resolved delegation (Java Bearer/Basic + did, F58-r) travels as the
// delegator plus the delegatee and the delegation's capped scopes; an app
// delegatee keeps its app id.
func TestProjectParse_DelegationRoundTrip(t *testing.T) {
	for _, app := range []string{"", "app-9"} {
		user := auth.DxUser{
			ID: "delegator-1", Roles: []string{"provider", "consumer"}, OrganisationID: "org-d",
			DelegateeID: "caller-1", AppID: app,
			Scopes: []auth.DelegationScopeEntry{{Scope: "data-access"}},
		}
		h, err := Project(user)
		if err != nil {
			t.Fatalf("project: %v", err)
		}
		if h.Get(HdrSubjectDelegateeID) != "caller-1" || h.Get(HdrSubjectScopes) != "data-access" ||
			h.Get(HdrSubjectAppID) != app {
			t.Fatalf("app %q: headers = %v", app, h)
		}
		got, err := Parse(h)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.ID != "delegator-1" || got.DelegateeID != "caller-1" || got.AppID != app ||
			len(got.Scopes) != 1 || got.Scopes[0].Scope != "data-access" {
			t.Fatalf("parsed = %+v", got)
		}
	}
}
