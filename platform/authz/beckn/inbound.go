package beckn

import (
	"fmt"
	"strings"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// Case distinguishes the two genuinely different inbound authorization questions
// (§9.7). Collapsing them produces either an over-permissive or an unusable rule.
type Case string

const (
	// CaseCorrelated: a callback (on_*) on a transaction WE initiated. Authority
	// comes from the transaction; the decision is "may this participant advance
	// THIS transaction".
	CaseCorrelated Case = "correlated_callback"
	// CaseUnsolicited: a fresh sell-side request. Authority comes from the
	// participant plus local policy; the decision is "may this participant cause
	// this local effect".
	CaseUnsolicited Case = "unsolicited_request"
)

// NetworkParticipantType is the wire subject type for a verified Beckn peer. No
// FGA object is created for it by default (NFH C.10).
const NetworkParticipantType = string(decision.SubjectNetworkParticipant)

// InboundMsg is a verified inbound Beckn message, as the fabric webhook sees it
// AFTER ONIX has checked the signature, registry and schema (that verification
// result arrives on the trusted adapter channel, not a caller header).
type InboundMsg struct {
	// Method is the Beckn method ("on_confirm", "search", ...).
	Method string
	// ParticipantID is the verified DeDi participant/subscriber id.
	ParticipantID string
	TransactionID string
	MessageID     string
	// KnownTransaction reports whether the webhook has a correlated record for
	// TransactionID. It is the webhook's pre-AuthZEN correlation result.
	KnownTransaction bool
	// LocalResourceType/ID identify the local object an unsolicited request
	// targets (order/offer/grant).
	LocalResourceType string
	LocalResourceID   string
	// OperationID is the signed manifest op for this inbound effect.
	OperationID string
}

// IsCallback reports whether a method is a Beckn callback (on_*).
func IsCallback(method string) bool { return strings.HasPrefix(method, "on_") }

// Classify returns the authorization case for a message.
func Classify(m InboundMsg) Case {
	if IsCallback(m.Method) {
		return CaseCorrelated
	}
	return CaseUnsolicited
}

var inboundActions = map[string]string{
	"search": "query", "select": "query",
	"status": "read", "track": "read",
	"init": "write", "confirm": "write", "cancel": "write", "update": "write", "rating": "write",
}

// baseMethod strips a leading on_ so on_confirm and confirm share an action.
func baseMethod(method string) string { return strings.TrimPrefix(method, "on_") }

// InboundRequest builds the federated AuthZEN request for a verified inbound
// message, applying the Case A/B split. It returns the case so the caller can
// audit which authority path was taken.
//
// It does NOT perform the correlation or replay checks — those are the webhook's
// pre-AuthZEN gates. A correlated callback whose transaction is unknown must be
// rejected by the webhook BEFORE this is called; passing one here is a caller
// error and returns an error rather than a silently-downgraded request.
func InboundRequest(m InboundMsg) (decision.EvaluationRequest, Case, error) {
	if m.ParticipantID == "" {
		return decision.EvaluationRequest{}, "", fmt.Errorf("beckn: inbound message has no verified participant")
	}
	action, ok := inboundActions[baseMethod(m.Method)]
	if !ok {
		return decision.EvaluationRequest{}, "", fmt.Errorf("beckn: unmapped inbound method %q", m.Method)
	}
	c := Classify(m)

	var res decision.Resource
	switch c {
	case CaseCorrelated:
		if !m.KnownTransaction {
			// The webhook must have rejected this already; refuse to build a
			// request that would authorize an uncorrelated callback.
			return decision.EvaluationRequest{}, "", fmt.Errorf("beckn: correlated callback for unknown transaction %q", m.TransactionID)
		}
		if m.TransactionID == "" {
			return decision.EvaluationRequest{}, "", fmt.Errorf("beckn: correlated callback missing transaction id")
		}
		// Authority is the transaction we own.
		res = decision.Resource{Type: "resource", ID: m.TransactionID}
	default: // CaseUnsolicited
		if m.LocalResourceType == "" || m.LocalResourceID == "" {
			return decision.EvaluationRequest{}, "", fmt.Errorf("beckn: unsolicited request missing local resource")
		}
		res = decision.Resource{Type: m.LocalResourceType, ID: m.LocalResourceID}
	}

	req := decision.EvaluationRequest{
		Subject:  decision.Subject{Type: decision.SubjectNetworkParticipant, ID: m.ParticipantID},
		Action:   decision.Action{Name: action},
		Resource: res,
		Context: &decision.RequestContext{
			DX: &decision.DXRequestContext{
				Profile:   decision.ProfileRequestV1,
				Operation: decision.OperationRef{Service: "fabric-webhook", ID: m.OperationID},
				Actor: &decision.Actor{
					Type:          decision.SubjectNetworkParticipant,
					ID:            m.ParticipantID,
					TransactionID: m.TransactionID,
					MessageID:     m.MessageID,
				},
			},
		},
	}
	return req, c, nil
}
