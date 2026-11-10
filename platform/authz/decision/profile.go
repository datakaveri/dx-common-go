package decision

// Profile identifiers. These are provisional urn:dx:* values adequate for the
// internal contract; §7.2.2 requires promotion to resolvable HTTPS URIs with a
// published schema before any external PEP/PDP interoperates.
const (
	// ProfileRequestV1 tags a request carrying the CDPG context extension.
	ProfileRequestV1 = "urn:dx:authzen:req:1"
	// ProfileDecisionV1 tags a decision carrying the CDPG context extension.
	ProfileDecisionV1 = "urn:dx:authzen:dec:1"
	// ProfileAttestationV1 is the decision-attestation JWS profile (§7.6).
	ProfileAttestationV1 = "urn:dx:authzen:att:1"
)

// SubjectType is the wire subject vocabulary (§8.1). The OpenFGA types
// (user/agent/organization/group) are PRIVATE to dx-authz-go and never appear
// here.
type SubjectType string

const (
	// SubjectIdentity is a human principal; id is the Keycloak `sub`.
	SubjectIdentity SubjectType = "identity"
	// SubjectWorkload is a service/worker acting for itself.
	SubjectWorkload SubjectType = "workload"
	// SubjectAgent is an agent acting for ITSELF (registry administration only,
	// never a delegated data path — a delegated call keeps the human as subject
	// and puts the agent in context.dx.actor).
	SubjectAgent SubjectType = "agent"
	// SubjectNetworkParticipant is a verified external Beckn participant.
	SubjectNetworkParticipant SubjectType = "network_participant"
	// SubjectUserAlias is the deprecated alias for identity, accepted through
	// the migration window (Class C) then rejected.
	SubjectUserAlias SubjectType = "user"
)

// DecisionProfile is the stable, server-selected profile name. The PDP selects
// it from the signed operation manifest; a caller never does (I-4).
type DecisionProfile string

const (
	ProfileNone           DecisionProfile = "none"
	ProfileRelationship   DecisionProfile = "relationship"
	ProfileComposite      DecisionProfile = "composite"
	ProfileDataAccess     DecisionProfile = "data_access"
	ProfileAgentComposite DecisionProfile = "agent_composite"
	ProfileFederated      DecisionProfile = "federated"
)

// ReasonCode is the stable, caller-safe machine taxonomy (§7.8). Rule traces
// never travel with it.
type ReasonCode string

const (
	ReasonAllowed               ReasonCode = "allowed"
	ReasonNoRelationship        ReasonCode = "no_relationship"
	ReasonNoActiveGrant         ReasonCode = "no_active_grant"
	ReasonGrantExpired          ReasonCode = "grant_expired"
	ReasonGrantRevoked          ReasonCode = "grant_revoked"
	ReasonConstraintFailed      ReasonCode = "constraint_failed"
	ReasonDelegationInvalid     ReasonCode = "delegation_invalid"
	ReasonAgentSuspended        ReasonCode = "agent_suspended"
	ReasonApprovalRequired      ReasonCode = "approval_required"
	ReasonApprovalInvalid       ReasonCode = "approval_invalid"
	ReasonObligationUnsupported ReasonCode = "obligation_unsupported"
	ReasonProfileUnsupported    ReasonCode = "profile_unsupported"
	ReasonProjectionIncomplete  ReasonCode = "projection_incomplete"
	ReasonFederationDenied      ReasonCode = "federation_denied"
	ReasonTermsMismatch         ReasonCode = "terms_mismatch"
	ReasonQuotaExhausted        ReasonCode = "quota_exhausted"
	// ReasonInvalidRequest is a caller-side fault surfaced as a decision reason
	// only where the transport has already accepted the request.
	ReasonInvalidRequest ReasonCode = "invalid_request"
)
