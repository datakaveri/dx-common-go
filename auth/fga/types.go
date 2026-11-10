// Package fga is a typed HTTP client for the dx-authz-go authorization service.
//
// The same request/response types are used by:
//   - dx-authz-go (server-side handler input/output)
//   - dx-common-go/auth/fga.Client (caller-side, e.g. dx-gateway-go)
//
// Keeping the types in dx-common-go ensures contract drift between caller and
// server is caught at compile time.
package fga

// SubjectType discriminates between user, group and agent subjects.
type SubjectType string

const (
	SubjectTypeUser  SubjectType = "user"
	SubjectTypeGroup SubjectType = "group"
	// SubjectTypeAgent is an AI agent acting under a delegation grant. Agents
	// only ever hold delegated_* relations — never the user's own relations.
	SubjectTypeAgent SubjectType = "agent"
)

// CheckRequest asks whether a subject has a relation on a resource.
//
// For SubjectTypeGroup, OrganisationID must be set: groups are scoped to an
// organisation in the FGA model (group:<org>_<group>#member).
type CheckRequest struct {
	SubjectType    SubjectType `json:"subject_type" validate:"required,oneof=user group agent"`
	SubjectID      string      `json:"subject_id" validate:"required"`
	OrganisationID string      `json:"organisation_id,omitempty"`
	ResourceType   string      `json:"resource_type" validate:"required"`
	ResourceID     string      `json:"resource_id" validate:"required"`
	Relation       string      `json:"relation" validate:"required"`

	// The end user's realm roles, organisation and delegator (the token's
	// did). dx-authz-go decides a user's read of a resource from the
	// catalogue the Java way (owner / cos_admin / org_admin, access rules), so
	// a PEP that has the token sends them. Optional; ignored by OpenFGA.
	SubjectRoles          []string `json:"subject_roles,omitempty"`
	SubjectOrganisationID string   `json:"subject_organisation_id,omitempty"`
	DelegatorID           string   `json:"delegator_id,omitempty"`
}

// CheckResponse is the authorization decision.
type CheckResponse struct {
	Allowed  bool   `json:"allowed"`
	Subject  string `json:"subject"`
	Object   string `json:"object"`
	Relation string `json:"relation"`
}

// PolicyRequest grants or revokes a single relationship tuple.
type PolicyRequest struct {
	SubjectType    SubjectType `json:"subject_type" validate:"required,oneof=user group agent"`
	SubjectID      string      `json:"subject_id" validate:"required"`
	OrganisationID string      `json:"organisation_id,omitempty"`
	ResourceType   string      `json:"resource_type" validate:"required"`
	ResourceID     string      `json:"resource_id" validate:"required"`
	Relation       string      `json:"relation" validate:"required"`
}

// PolicyAcceptedResponse is returned by the async policy endpoints.
type PolicyAcceptedResponse struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id"`
}

// ListPoliciesRequest filters for listing tuples.
type ListPoliciesRequest struct {
	SubjectType       SubjectType
	SubjectID         string
	OrganisationID    string
	ResourceType      string
	ResourceID        string
	PageSize          int32
	ContinuationToken string
}

// PolicyTuple is a single relationship in the FGA store.
type PolicyTuple struct {
	Subject  string `json:"subject"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
}

// ListPoliciesResponse is paginated tuple data.
type ListPoliciesResponse struct {
	Tuples            []PolicyTuple `json:"tuples"`
	ContinuationToken string        `json:"continuation_token,omitempty"`
}

// GroupMemberRequest adds or removes a user from a group.
type GroupMemberRequest struct {
	UserID string `json:"user_id" validate:"required"`
}
