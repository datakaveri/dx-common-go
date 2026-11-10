package attestation

import (
	"time"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// TTL tiers (§7.6). Re-derive from measured p99 before AUTHZ-5 sign-off.
const (
	// HopTTL bounds a PEP→PEP attestation (e.g. MCP gateway → gateway).
	HopTTL = 30 * time.Second
	// EnforcementTTL bounds a gateway → data-plane attestation for one operation.
	EnforcementTTL = 120 * time.Second
)

// Attestation is the decision carried to an enforcing component.
type Attestation struct {
	Issuer     string
	Audience   string
	Subject    string   // the authority on whose behalf the operation occurs
	ActorChain []string // authority → immediate actor
	ID         string   // jti; single-use for high-risk effects
	IssuedAt   time.Time
	ExpiresAt  time.Time

	EvaluationID string
	OperationID  string
	ResourceType string
	ResourceID   string
	Obligations  []decision.Obligation
	Evidence     *decision.Evidence
}

// BuildFromDecision assembles an attestation from an allow decision and the
// request it answered. It flattens obligations across every entitlement (the
// data plane enforces the union; per-grant separation is a PDP concern already
// resolved by the time a single operation is enforced). Panics are impossible:
// a nil or non-allow response yields a zero-obligation attestation the verifier
// will still bind and expire.
func BuildFromDecision(req decision.EvaluationRequest, resp *decision.EvaluationResponse, audience string, expiresAt time.Time) Attestation {
	a := Attestation{
		Audience:     audience,
		Subject:      req.Subject.ID,
		ExpiresAt:    expiresAt,
		ResourceType: req.Resource.Type,
		ResourceID:   req.Resource.ID,
	}
	if dx := req.DXContext(); dx != nil {
		a.OperationID = dx.Operation.ID
		if dx.Actor != nil {
			if len(dx.Actor.Chain) > 0 {
				a.ActorChain = dx.Actor.Chain
			} else {
				a.ActorChain = []string{req.Subject.ID, dx.Actor.ID}
			}
		}
	}
	if ddx := resp.DXContext(); ddx != nil {
		a.EvaluationID = ddx.EvaluationID
		a.Evidence = ddx.Evidence
		for _, ent := range ddx.Entitlements {
			a.Obligations = append(a.Obligations, ent.Obligations...)
		}
	}
	return a
}
