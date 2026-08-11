// Package identity carries the platform's notion of who is making a request.
//
// A Subject is pure data: it is produced by whatever verified the caller (a JWT
// validator, X-Subject-* identity headers projected by the gateway (unsigned
// since P0-17 stage 2), an app-credential
// exchange) and consumed everywhere else. It contains no verification logic, so
// nothing downstream can be tricked into re-deriving trust from it.
//
// The type this replaces — auth.DxUser — is the core identity of the whole
// platform and had ZERO tests. That is the main reason it is being rebuilt
// rather than moved.
//
// Layer: L0 (kernel). Imports stdlib only.
package identity

import "strings"

// Subject is a verified caller.
//
// The zero Subject is deliberately meaningless: a handler must never see one.
// platform/http enforces that by returning 401 before a handler runs (see the
// embedded httpx.Actor), which is what removes ~220 hand-written
// `user, ok := UserFromCtx(ctx); if !ok { 401 }` blocks from the fleet.
type Subject struct {
	// ID is the authenticated principal — always the HUMAN user, even when an
	// agent is acting on their behalf. Authorization decisions are made against
	// this, intersected with the delegation's scopes.
	ID string

	Email string
	Name  string

	// Org is the organisation the subject belongs to.
	Org     string
	OrgName string

	// Roles are realm roles, as flat strings. They stay strings here so that L0
	// has no dependency on the authorization vocabulary; security/authz gives
	// them meaning.
	Roles []string

	// Delegation is non-nil when something is acting FOR this subject rather
	// than the subject acting directly — an agent, an application credential,
	// or a user-to-user delegation. Its presence never widens authority: the
	// effective permission set is the subject's own rights INTERSECTED with the
	// delegation's scopes, never their union.
	Delegation *Delegation
}

// Delegation records who is acting, under which grant, and how narrowly.
//
// Actor corresponds to the RFC 8693 `act.sub` claim: the token's `sub` stays
// the human user and `act.sub` names the agent. That split is what makes
// delegated action auditable as "agent X acting for user Y" rather than
// indistinguishable from the user acting alone.
type Delegation struct {
	// Actor is the acting principal — an agent id, a client id, or a delegating
	// user's id, depending on Kind.
	Actor string

	// GrantID references the delegation grant that authorised this. It is what
	// an auditor follows to find out who approved the authority, and what a
	// revocation targets.
	GrantID string

	// Kind distinguishes the delegation mechanisms so a policy can treat them
	// differently — an autonomous agent is not the same risk as a user acting
	// for a colleague.
	Kind Kind

	// Scopes are the granted capabilities. An empty slice means the delegation
	// conveys NO authority, which is a valid and deliberately restrictive state
	// — never treat it as "unrestricted".
	Scopes []Scope
}

// Kind enumerates how authority was delegated.
type Kind string

const (
	// KindAgent is an autonomous software agent acting under a grant.
	KindAgent Kind = "agent"
	// KindApp is a machine-to-machine application credential.
	KindApp Kind = "app"
	// KindUser is one user acting on behalf of another.
	KindUser Kind = "user"
)

// Scope is one granted capability, optionally narrowed to a single entity.
type Scope struct {
	// Name is the capability, e.g. "catalogue:read".
	Name string
	// EntityID narrows the scope to one resource. Empty means the scope applies
	// to every entity the subject could otherwise reach — which is broader, so
	// it is worth being explicit about at the granting site.
	EntityID string
}

// IsDelegated reports whether something is acting on the subject's behalf.
func (s Subject) IsDelegated() bool { return s.Delegation != nil }

// IsAgent reports whether an autonomous agent is acting for the subject.
// Distinct from IsDelegated: an app credential or a user-to-user delegation is
// delegated but is not an agent, and agent traffic carries stricter controls
// (dual authorization check, kill switch, HITL on high-risk tools).
func (s Subject) IsAgent() bool {
	return s.Delegation != nil && s.Delegation.Kind == KindAgent
}

// Actor returns the principal that is actually acting: the delegated actor when
// there is one, otherwise the subject itself. This is the value to use for
// rate-limit keys and audit attribution — never for authorization, which is
// always decided against Subject.ID.
func (s Subject) Actor() string {
	if s.Delegation != nil && s.Delegation.Actor != "" {
		return s.Delegation.Actor
	}
	return s.ID
}

// HasRole reports whether the subject holds a role. The comparison is
// case-insensitive because realm role casing varies between identity providers
// and a silent mismatch there fails open-looking (a role appears absent, access
// is denied, and nobody can see why).
func (s Subject) HasRole(role string) bool {
	for _, r := range s.Roles {
		if strings.EqualFold(r, role) {
			return true
		}
	}
	return false
}

// HasAnyRole reports whether the subject holds at least one of the roles.
func (s Subject) HasAnyRole(roles ...string) bool {
	for _, role := range roles {
		if s.HasRole(role) {
			return true
		}
	}
	return false
}

// HasScope reports whether a delegation grants the named scope for entityID.
//
// It returns FALSE for a non-delegated subject, which is the safe reading: a
// scope check is a question about delegated authority, and a direct user has
// none. Callers deciding "may this request proceed" must check the subject's
// own rights as well — HasScope narrows, it never grants.
//
// An empty entityID on the request matches any granted scope of that name; a
// granted scope with an empty EntityID matches any requested entity.
func (s Subject) HasScope(name, entityID string) bool {
	if s.Delegation == nil {
		return false
	}
	for _, sc := range s.Delegation.Scopes {
		if sc.Name != name {
			continue
		}
		if sc.EntityID == "" || entityID == "" || sc.EntityID == entityID {
			return true
		}
	}
	return false
}

// ScopeNames lists the granted scope names, for building a plain-language
// summary of delegated authority (an agent's system prompt, an audit line).
func (s Subject) ScopeNames() []string {
	if s.Delegation == nil {
		return nil
	}
	out := make([]string, 0, len(s.Delegation.Scopes))
	seen := make(map[string]struct{}, len(s.Delegation.Scopes))
	for _, sc := range s.Delegation.Scopes {
		if _, dup := seen[sc.Name]; dup {
			continue
		}
		seen[sc.Name] = struct{}{}
		out = append(out, sc.Name)
	}
	return out
}
