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
	// AgentSubject is populated when the request runs under a delegated token
	// (RFC 8693 act claim): ID stays the human user, AgentSubject names the
	// agent acting on their behalf.
	AgentSubject string
	// DelegationID references the delegation grant that authorized the
	// delegated token. Empty on direct user requests.
	DelegationID string
	// AppID is set when the caller authenticated with an application
	// credential (Basic appId:appSecret): ID is then the app's owner and Scopes
	// the app's scopes (Java AppId principal: sub, app_id, scopes).
	AppID string
	// DelegateeID is set when the request named a delegator in the `did`
	// header and the gateway resolved that delegation (Java Bearer/Basic +
	// did → ResolveDelegation, F58-r): ID, Roles, organisation, Email and Name
	// are then the DELEGATOR's, Scopes the delegation's capped scopes, and
	// DelegateeID the authenticated caller (Java claim delegatee_sub). AppID
	// stays set when that caller is an application credential (Basic + did).
	DelegateeID string
}

// IsAgent reports whether the request is being made by an agent acting on the
// user's behalf rather than by the user directly.
func (u DxUser) IsAgent() bool {
	return u.AgentSubject != ""
}
