package decision

import "time"

// AccessRequest is the AARP requestable-denial structure returned in
// context.access_request when policy permits an approval attempt (§7.5). Field
// names are AARP's, not CDPG inventions.
type AccessRequest struct {
	// Endpoint is the Access Request Service submission URI.
	Endpoint string `json:"endpoint"`
	// Template is the opaque workflow identifier.
	Template string `json:"template"`
	// ExpiresAt is required by AARP.
	ExpiresAt time.Time `json:"expires_at"`
	// BindingToken is the integrity-protected denial context (a JWS). CDPG uses
	// by-value binding because the ARS (dx-acl-go) is a separate service from
	// the PDP (dx-authz-go) — §7.5 step 1.
	BindingToken string `json:"binding_token,omitempty"`
	// EvaluationID is the by-reference alternative and the audit thread id.
	EvaluationID string       `json:"evaluation_id,omitempty"`
	Display      *DisplayHint `json:"display,omitempty"`
}

// DisplayHint carries UI text for the approver; it is never authoritative.
type DisplayHint struct {
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// ApprovalRef is the AARP approval object carried in context.dx.approval on a
// re-evaluation after approval (§7.5 step 5). The PDP verifies it independently;
// a known id is NEVER sufficient (I-9).
type ApprovalRef struct {
	ID            string     `json:"id"`
	ApprovedAt    *time.Time `json:"approved_at,omitempty"`
	ApprovedUntil *time.Time `json:"approved_until,omitempty"`
	// State is the optional integrity-protected verifier (a JWS carrying iss,
	// kid, aud); when present the PDP resolves the signer via jwks_uri and
	// checks aud to prevent cross-PDP replay.
	State string `json:"state,omitempty"`
}

// NextAction guides a PEP after a re-evaluation denial (§7.5 step 6).
type NextAction string

const (
	NextActionRequest NextAction = "request"
	NextActionRetry   NextAction = "retry"
	NextActionNone    NextAction = "none"
)
