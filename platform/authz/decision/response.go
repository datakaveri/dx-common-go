package decision

import "time"

// EvaluationResponse is the AuthZEN Access Evaluation response. A deny is a
// well-formed response with Decision=false, never an HTTP error (§7.8).
type EvaluationResponse struct {
	Decision bool             `json:"decision"`
	Context  *DecisionContext `json:"context,omitempty"`
}

// DecisionContext is the AuthZEN response context as CDPG populates it.
//
// Note the deliberate absence of reason_admin: rule traces go to privileged
// telemetry, never onto a caller-facing response (I-15), so the type cannot
// carry them.
type DecisionContext struct {
	// ReasonUser is the standard's human-readable, caller-safe field.
	ReasonUser string `json:"reason_user,omitempty"`
	// AccessRequest is the AARP requestable-denial structure, present only on a
	// requestable deny (§7.5).
	AccessRequest *AccessRequest `json:"access_request,omitempty"`
	// DX is the CDPG decision profile.
	DX *DXDecisionContext `json:"dx,omitempty"`
}

// DXDecisionContext is the CDPG decision profile (urn:dx:authzen:dec:1).
type DXDecisionContext struct {
	Profile      string     `json:"profile"`
	EvaluationID string     `json:"evaluation_id"`
	ReasonCode   ReasonCode `json:"reason_code"`
	// Complete=false is denial-equivalent for any profile above relationship:
	// the PDP could not evaluate every required stage (§7.3 rule 2).
	Complete         bool            `json:"complete"`
	EvaluatedProfile DecisionProfile `json:"evaluated_profile,omitempty"`
	ValidUntil       *time.Time      `json:"valid_until,omitempty"`
	Evidence         *Evidence       `json:"evidence,omitempty"`
	// Entitlements are per-grant; obligations are NEVER mixed across grants
	// (ADR-10 §3.8).
	Entitlements []Entitlement `json:"entitlements,omitempty"`
	// Attestation is the compact JWS the enforcing component verifies (§7.6).
	Attestation string `json:"attestation,omitempty"`
}

// Evidence records what produced the decision, for audit replay.
type Evidence struct {
	ManifestDigest          string `json:"manifest_digest,omitempty"`
	FGAModelID              string `json:"fga_model_id,omitempty"`
	GrantProjectionRevision string `json:"grant_projection_revision,omitempty"`
	ConditionSchemaVersion  int    `json:"condition_schema_version,omitempty"`
	MappingDigest           string `json:"mapping_digest,omitempty"`
}

// Entitlement is one grant that allowed the access, with its own obligations.
type Entitlement struct {
	GrantID      string       `json:"grant_id"`
	GrantVersion int          `json:"grant_version"`
	Obligations  []Obligation `json:"obligations,omitempty"`
}

// Allow builds a minimal allow decision carrying the CDPG profile. Callers add
// entitlements, obligations, evidence and an attestation as the composite PDP
// resolves them.
func Allow(evaluationID string, profile DecisionProfile) *EvaluationResponse {
	return &EvaluationResponse{
		Decision: true,
		Context: &DecisionContext{
			DX: &DXDecisionContext{
				Profile:          ProfileDecisionV1,
				EvaluationID:     evaluationID,
				ReasonCode:       ReasonAllowed,
				Complete:         true,
				EvaluatedProfile: profile,
			},
		},
	}
}

// Deny builds a deny decision with a stable reason code and caller-safe text.
func Deny(evaluationID string, profile DecisionProfile, code ReasonCode, userMsg string) *EvaluationResponse {
	return &EvaluationResponse{
		Decision: false,
		Context: &DecisionContext{
			ReasonUser: userMsg,
			DX: &DXDecisionContext{
				Profile:          ProfileDecisionV1,
				EvaluationID:     evaluationID,
				ReasonCode:       code,
				Complete:         true,
				EvaluatedProfile: profile,
			},
		},
	}
}

// Incomplete builds a fail-closed deny for a profile the PDP could not fully
// evaluate. Decision is false and Complete is false (§7.3 rule 2, I-11).
func Incomplete(evaluationID string, profile DecisionProfile, code ReasonCode) *EvaluationResponse {
	r := Deny(evaluationID, profile, code, "authorization could not be completed")
	r.Context.DX.Complete = false
	return r
}

// Allowed is a nil-safe accessor: true only for a well-formed allow whose CDPG
// profile (when present) is Complete. A response a PEP cannot fully understand
// is not an allow.
func (r *EvaluationResponse) Allowed() bool {
	if r == nil || !r.Decision {
		return false
	}
	if r.Context != nil && r.Context.DX != nil && !r.Context.DX.Complete {
		return false
	}
	return true
}

// DXContext returns the CDPG decision profile if present, else nil.
func (r *EvaluationResponse) DXContext() *DXDecisionContext {
	if r == nil || r.Context == nil {
		return nil
	}
	return r.Context.DX
}
