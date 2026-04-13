package authorization

// DxRole is the string identifier of a platform role.
type DxRole string

const (
	RoleProvider DxRole = "provider"
	RoleConsumer DxRole = "consumer"
	RoleCosAdmin DxRole = "cos_admin"
	RoleOrgAdmin DxRole = "org_admin"
	RoleDelegate DxRole = "delegate"
)

// RoleSet is a set of DxRole values backed by a map for O(1) lookups.
type RoleSet map[DxRole]struct{}

// NewRoleSet creates a RoleSet from the provided roles.
func NewRoleSet(roles ...DxRole) RoleSet {
	s := make(RoleSet, len(roles))
	for _, r := range roles {
		s[r] = struct{}{}
	}
	return s
}

// Has returns true if role is in the set.
func (s RoleSet) Has(role DxRole) bool {
	_, ok := s[role]
	return ok
}

// HasAny returns true if at least one of the given roles is in the set.
func (s RoleSet) HasAny(roles []DxRole) bool {
	for _, r := range roles {
		if s.Has(r) {
			return true
		}
	}
	return false
}
