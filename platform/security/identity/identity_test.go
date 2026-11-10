package identity_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

func user() identity.Subject {
	return identity.Subject{
		ID: "u-1", Email: "a@b.c", Name: "A", Org: "org-1",
		Roles: []string{"consumer", "provider"},
	}
}

func agentFor(s identity.Subject, scopes ...identity.Scope) identity.Subject {
	s.Delegation = &identity.Delegation{
		Actor: "agent-9", GrantID: "grant-3", Kind: identity.KindAgent, Scopes: scopes,
	}
	return s
}

func TestHasRole_IsCaseInsensitive(t *testing.T) {
	s := user()
	for _, r := range []string{"consumer", "CONSUMER", "Consumer"} {
		if !s.HasRole(r) {
			t.Errorf("HasRole(%q) = false; realm role casing varies between IdPs and a silent mismatch denies access invisibly", r)
		}
	}
	if s.HasRole("admin") {
		t.Error("HasRole must not report a role the subject lacks")
	}
	if s.HasRole("") {
		t.Error("the empty role must never match")
	}
}

func TestHasAnyRole(t *testing.T) {
	s := user()
	if !s.HasAnyRole("admin", "provider") {
		t.Error("HasAnyRole should match on the second entry")
	}
	if s.HasAnyRole("admin", "auditor") {
		t.Error("HasAnyRole must be false when none match")
	}
	if s.HasAnyRole() {
		t.Error("HasAnyRole with no arguments must be false, not vacuously true")
	}
}

func TestDelegationPredicates(t *testing.T) {
	direct := user()
	if direct.IsDelegated() || direct.IsAgent() {
		t.Error("a direct user is neither delegated nor an agent")
	}

	ag := agentFor(user())
	if !ag.IsDelegated() || !ag.IsAgent() {
		t.Error("an agent-delegated subject must report both")
	}

	// An app credential is delegated but is NOT an agent — agent traffic
	// carries stricter controls (dual check, kill switch, HITL).
	app := user()
	app.Delegation = &identity.Delegation{Actor: "client-x", Kind: identity.KindApp}
	if !app.IsDelegated() {
		t.Error("an app credential is delegated")
	}
	if app.IsAgent() {
		t.Error("an app credential must NOT be classified as an agent")
	}
}

// TestActor_IsNeverUsedForAuthorization documents the split that makes
// delegated action auditable: ID stays the human for authorization, Actor names
// who is acting for attribution and rate limiting.
func TestActor_ReturnsTheActingPrincipal(t *testing.T) {
	if got := user().Actor(); got != "u-1" {
		t.Errorf("a direct user's actor is itself, got %q", got)
	}
	ag := agentFor(user())
	if got := ag.Actor(); got != "agent-9" {
		t.Errorf("actor = %q, want the agent", got)
	}
	if ag.ID != "u-1" {
		t.Error("the subject ID must remain the human user even under delegation")
	}
}

func TestActor_FallsBackWhenDelegationHasNoActor(t *testing.T) {
	s := user()
	s.Delegation = &identity.Delegation{GrantID: "g-1", Kind: identity.KindApp}
	if got := s.Actor(); got != "u-1" {
		t.Errorf("actor = %q, want the subject when the delegation names none", got)
	}
}

func TestHasScope(t *testing.T) {
	s := agentFor(user(),
		identity.Scope{Name: "catalogue:read"},
		identity.Scope{Name: "files:read", EntityID: "file-7"},
	)

	tests := []struct {
		name, scope, entity string
		want                bool
	}{
		{"unscoped grant matches any entity", "catalogue:read", "anything", true},
		{"unscoped grant matches empty entity", "catalogue:read", "", true},
		{"entity-scoped grant matches its entity", "files:read", "file-7", true},
		{"entity-scoped grant rejects another entity", "files:read", "file-8", false},
		{"entity-scoped grant matches an unspecified request", "files:read", "", true},
		{"ungranted scope", "marketplace:purchase", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.HasScope(tt.scope, tt.entity); got != tt.want {
				t.Errorf("HasScope(%q,%q) = %v, want %v", tt.scope, tt.entity, got, tt.want)
			}
		})
	}
}

// TestHasScope_FalseForDirectUser is a safety property. A scope check asks about
// DELEGATED authority; a direct user has none. Returning true here would let a
// scope gate silently pass for every non-delegated request.
func TestHasScope_FalseForDirectUser(t *testing.T) {
	if user().HasScope("catalogue:read", "") {
		t.Error("a direct user must not satisfy a delegated-scope check")
	}
}

// TestEmptyScopesGrantNothing: an empty scope slice means no authority, and must
// never be read as "unrestricted".
func TestEmptyScopesGrantNothing(t *testing.T) {
	s := agentFor(user()) // no scopes
	if s.HasScope("catalogue:read", "") {
		t.Error("a delegation with no scopes must grant nothing")
	}
	if got := s.ScopeNames(); len(got) != 0 {
		t.Errorf("ScopeNames = %v, want empty", got)
	}
}

func TestScopeNames_DeduplicatesAndOrders(t *testing.T) {
	s := agentFor(user(),
		identity.Scope{Name: "files:read", EntityID: "a"},
		identity.Scope{Name: "files:read", EntityID: "b"},
		identity.Scope{Name: "catalogue:read"},
	)
	got := s.ScopeNames()
	if len(got) != 2 {
		t.Fatalf("ScopeNames = %v, want 2 distinct names", got)
	}
	if got[0] != "files:read" || got[1] != "catalogue:read" {
		t.Errorf("ScopeNames = %v, want grant order preserved", got)
	}
	if user().ScopeNames() != nil {
		t.Error("a direct user has no scope names")
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := identity.With(context.Background(), user())

	got, ok := identity.From(ctx)
	if !ok {
		t.Fatal("From must find the subject that With stored")
	}
	if got.ID != "u-1" || got.Org != "org-1" {
		t.Errorf("round-tripped subject = %+v", got)
	}

	if _, ok := identity.From(context.Background()); ok {
		t.Error("From on a bare context must report absence")
	}
}

func TestRequire_ReturnsAnErrorNotABool(t *testing.T) {
	// The point of the error return: a caller propagates it instead of
	// hand-writing a 401, which is the ~220-site boilerplate this removes.
	if _, err := identity.Require(context.Background()); !stderrors.Is(err, identity.ErrNoSubject) {
		t.Errorf("Require on a bare context = %v, want ErrNoSubject", err)
	}

	s, err := identity.Require(identity.With(context.Background(), user()))
	if err != nil {
		t.Fatalf("Require on a populated context: %v", err)
	}
	if s.ID != "u-1" {
		t.Errorf("subject = %+v", s)
	}
}

// TestRequire_RejectsEmptyID: a misconfigured verifier producing a Subject with
// no principal would otherwise yield an "authenticated" request that
// authorization then evaluates against nothing.
func TestRequire_RejectsEmptyID(t *testing.T) {
	ctx := identity.With(context.Background(), identity.Subject{Email: "a@b.c"})
	if _, err := identity.Require(ctx); !stderrors.Is(err, identity.ErrNoSubject) {
		t.Error("a Subject with an empty ID must not satisfy Require")
	}
}

func TestMustFrom_PanicsWhenAbsent(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustFrom must panic when no subject is present")
		}
	}()
	_ = identity.MustFrom(context.Background())
}
