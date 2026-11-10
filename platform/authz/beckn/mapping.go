package beckn

import (
	"fmt"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// Op is a buy-side Beckn operation an agent may drive.
type Op string

const (
	OpDiscover Op = "beckn.discover"
	OpSelect   Op = "beckn.select"
	OpStatus   Op = "beckn.status"
	OpTrack    Op = "beckn.track"
	OpInit     Op = "beckn.init"
	OpConfirm  Op = "beckn.confirm"
	OpCancel   Op = "beckn.cancel"
	OpRate     Op = "beckn.rate"
)

// Risk is the approval tier for an operation.
type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// VenueResourceType is the stable authorization resource type for a fabric
// venue; VenueNFH is the NFH venue id (NFH C.10). No FGA model change.
const (
	VenueResourceType = "fabric_venue"
	VenueNFH          = "nfh"
)

type opSpec struct {
	action    string
	venue     bool // resource is the venue (discover) rather than an offer/order
	risk      Risk
	needTerms bool // material terms are required (financial write)
}

var specs = map[Op]opSpec{
	OpDiscover: {action: "query", venue: true, risk: RiskLow},
	OpSelect:   {action: "query", risk: RiskMedium},
	OpStatus:   {action: "read", risk: RiskLow},
	OpTrack:    {action: "read", risk: RiskLow},
	OpInit:     {action: "write", risk: RiskHigh, needTerms: true},
	OpConfirm:  {action: "write", risk: RiskHigh, needTerms: true},
	OpCancel:   {action: "write", risk: RiskHigh, needTerms: true},
	OpRate:     {action: "write", risk: RiskMedium},
}

// Risk returns the approval tier for an operation, or "" for an unknown op.
func RiskOf(op Op) Risk {
	if s, ok := specs[op]; ok {
		return s.risk
	}
	return ""
}

// IsHighRisk reports whether an operation requires the AARP step-up.
func IsHighRisk(op Op) bool { return RiskOf(op) == RiskHigh }

// BuyParams describes one buy-side authorization.
type BuyParams struct {
	Op           Op
	Subject      string // the human user id (token sub)
	Agent        string // the agent id (act.sub); required for a delegated call
	DelegationID string
	SessionID    string
	Venue        string // venue id for discover (defaults to nfh)
	ResourceID   string // offer/order/transaction id for non-discover ops
	OperationID  string // signed manifest operation id
	// Terms are the canonicalized material terms; required for init/confirm/cancel.
	Terms *MaterialTerms
}

// BuySide builds the trust-anchored AuthZEN request for a buy-side operation.
// The subject is always the human; the agent is the actor. Material terms ride
// in context.dx.request so a change to any of them changes the decision (and any
// bound approval). No internal artefact here ever reaches the Beckn wire.
func BuySide(p BuyParams) (decision.EvaluationRequest, error) {
	spec, ok := specs[p.Op]
	if !ok {
		return decision.EvaluationRequest{}, fmt.Errorf("beckn: unknown operation %q", p.Op)
	}
	if p.Subject == "" {
		return decision.EvaluationRequest{}, fmt.Errorf("beckn: subject is required")
	}
	if spec.needTerms && p.Terms == nil {
		// A financial write with no authoritative terms cannot be bound: fail
		// closed rather than authorize an unbound purchase.
		return decision.EvaluationRequest{}, fmt.Errorf("beckn: operation %q requires material terms", p.Op)
	}

	res := decision.Resource{Type: "resource", ID: p.ResourceID}
	if spec.venue {
		venue := p.Venue
		if venue == "" {
			venue = VenueNFH
		}
		res = decision.Resource{Type: VenueResourceType, ID: venue}
	} else if p.ResourceID == "" {
		return decision.EvaluationRequest{}, fmt.Errorf("beckn: operation %q requires a resource id", p.Op)
	}

	dx := &decision.DXRequestContext{
		Profile:   decision.ProfileRequestV1,
		Operation: decision.OperationRef{Service: "fabric-edge", ID: p.OperationID},
	}
	rc := &decision.RequestContext{DX: dx}
	if p.Agent != "" {
		rc.Agent = p.Agent
		dx.Actor = &decision.Actor{
			Type: decision.SubjectAgent, ID: p.Agent,
			DelegationID: p.DelegationID, SessionID: p.SessionID,
			Chain: []string{p.Subject, p.Agent},
		}
	}
	if p.Terms != nil {
		dx.Request = &decision.RequestFacts{
			Amount: p.Terms.Amount, Currency: p.Terms.Currency, Payee: p.Terms.Payee,
			TermsHash: p.Terms.Hash(),
		}
	}

	return decision.EvaluationRequest{
		Subject:  decision.Subject{Type: decision.SubjectIdentity, ID: p.Subject},
		Action:   decision.Action{Name: spec.action},
		Resource: res,
		Context:  rc,
	}, nil
}
