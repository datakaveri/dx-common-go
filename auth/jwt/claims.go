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
	// Scope is the raw space-separated scope string from the token.
	Scope             string                 `json:"scope,omitempty"`
	DelegationScopes  []DelegationScopeClaim `json:"delegation_scopes,omitempty"`
}

// RealmAccess holds the list of realm-level roles.
type RealmAccess struct {
	Roles []string `json:"roles"`
}

// Roles holds the list of roles for a specific client/resource.
type Roles struct {
	Roles []string `json:"roles"`
}

// DelegationScopeClaim represents a single scope entry in a delegation token.
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
