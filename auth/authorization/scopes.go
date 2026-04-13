package authorization

// DelegationScope is the string identifier of a delegation scope.
type DelegationScope string

const (
	ScopeCosAdminAccess DelegationScope = "cos_admin_access"
	ScopeOrgManagement  DelegationScope = "org_management"
	ScopeAssetManagement DelegationScope = "asset_management"
	ScopeDataAccess     DelegationScope = "data_access"
	ScopeAPI            DelegationScope = "api"
	ScopeFileAccess     DelegationScope = "file_access"
	ScopeCommunity      DelegationScope = "community"
	ScopeWildcard       DelegationScope = "*"
)

// ScopeSet is a set of DelegationScope values backed by a map for O(1) lookups.
type ScopeSet map[DelegationScope]struct{}

// NewScopeSet creates a ScopeSet from the provided scopes.
func NewScopeSet(scopes ...DelegationScope) ScopeSet {
	s := make(ScopeSet, len(scopes))
	for _, sc := range scopes {
		s[sc] = struct{}{}
	}
	return s
}

// Has returns true if scope is in the set or the wildcard "*" scope is present.
func (s ScopeSet) Has(scope DelegationScope) bool {
	if _, ok := s[ScopeWildcard]; ok {
		return true
	}
	_, ok := s[scope]
	return ok
}
