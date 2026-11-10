package jwt

import (
	gojwt "github.com/golang-jwt/jwt/v5"
)

// DxClaims extends the standard RegisteredClaims with Keycloak / CDPG-specific
// fields embedded in the JWT.
type DxClaims struct {
	gojwt.RegisteredClaims

	Email             string           `json:"email"`
	EmailVerified     bool             `json:"email_verified"`
	Name              string           `json:"name"`
	PreferredUsername string           `json:"preferred_username"`
	RealmAccess       RealmAccess      `json:"realm_access"`
	ResourceAccess    map[string]Roles `json:"resource_access"`
	OrganisationID    string           `json:"organisation_id"`
	OrganisationName  string           `json:"organisation_name"`
	DelegatorID       string           `json:"did,omitempty"`
	// AuthorizedParty is the OIDC "azp" claim — the client the token was
	// issued to. On a client-credentials token that client IS the calling
	// workload, which is what platform/security/workload authenticates.
	// Distinct from Subject: `sub` is the service-account user Keycloak
	// created for the client, `azp` is the client itself.
	AuthorizedParty string `json:"azp,omitempty"`
	// ClientID is Keycloak's "client_id" claim. Some token profiles emit it
	// instead of azp, so workload verification falls back to it rather than
	// rejecting a legitimate credential over a claim-name difference.
	ClientID string `json:"client_id,omitempty"`
	// Scope is the raw space-separated scope string from the token.
	Scope            string                 `json:"scope,omitempty"`
	DelegationScopes []DelegationScopeClaim `json:"delegation_scopes,omitempty"`
	// Act identifies the acting party on a delegated token (RFC 8693 §4.1).
	// Present only on tokens minted via token exchange: sub stays the user,
	// Act.Sub names the agent acting on their behalf.
	Act *ActClaim `json:"act,omitempty"`
	// DelegationID joins a delegated token to the grant that authorized it.
	DelegationID string `json:"delegation_id,omitempty"`
}

// ActClaim is the RFC 8693 "act" (actor) claim on a delegated token.
type ActClaim struct {
	Sub string `json:"sub"`
}

// RealmAccess holds the list of realm-level roles.
type RealmAccess struct {
	Roles []string `json:"roles"`
}

// Roles holds the list of roles for a specific client/resource.
type Roles struct {
	Roles []string `json:"roles"`
}

// DelegationScopeClaim represents a single scope entry in a delegation token
// (the AppID provider→consumer delegation feature — unrelated to the agent
// plane's grant model).
//
// Expiry is carried through from the token as-is but is NOT enforced by
// HasScopeForEntity or anywhere else in this codebase: callers that need an
// expiry check must parse and compare it themselves. The authoritative
// "is this still valid" answer for AppID delegations comes from the
// controlplane gRPC (ResolveDelegation), not from this claim.
type DelegationScopeClaim struct {
	Scope    string `json:"scope"`
	EntityID string `json:"entity_id"`
	Expiry   string `json:"expiry"`
}

// AllRoles returns a deduplicated flat list combining realm roles and roles from
// every client in ResourceAccess.
func (c *DxClaims) AllRoles() []string {
	seen := make(map[string]struct{})
	var roles []string
	for _, r := range c.RealmAccess.Roles {
		if _, ok := seen[r]; !ok {
			seen[r] = struct{}{}
			roles = append(roles, r)
		}
	}
	for _, ra := range c.ResourceAccess {
		for _, r := range ra.Roles {
			if _, ok := seen[r]; !ok {
				seen[r] = struct{}{}
				roles = append(roles, r)
			}
		}
	}
	return roles
}

// HasRole returns true if the given role appears anywhere in the token.
func (c *DxClaims) HasRole(role string) bool {
	for _, r := range c.AllRoles() {
		if r == role {
			return true
		}
	}
	return false
}

// HasScopeForEntity returns true when DelegationScopes contains an entry that
// matches both scope and entityID.
func (c *DxClaims) HasScopeForEntity(scope, entityID string) bool {
	for _, ds := range c.DelegationScopes {
		if ds.Scope == scope && ds.EntityID == entityID {
			return true
		}
	}
	return false
}
