package auth

// DelegationScopeEntry carries scope + entity information from a delegation token.
// Kept as plain strings here to avoid import cycles with the jwt sub-package.
type DelegationScopeEntry struct {
	Scope    string `json:"scope"`
	EntityID string `json:"entity_id"`
	Expiry   string `json:"expiry"`
}

// DxUser is the resolved identity stored in request context after JWT validation.
type DxUser struct {
	// ID is the subject claim (Keycloak user UUID).
	ID string
	// Email is the user's email address.
	Email string
	// Name is the user's display name.
	Name string
	// Roles is the flat list of realm roles assigned to the user.
	// Using []string avoids an import cycle with the authorization sub-package.
	Roles []string
	// OrganisationID is the organisation the user belongs to (custom claim).
	OrganisationID string
	// OrganisationName is the human-readable name of that organisation.
	OrganisationName string
	// DelegatorID is populated when the token is a delegation token (did claim).
	DelegatorID string
	// Scopes contains delegation scope entries from the token.
	Scopes []DelegationScopeEntry
}
