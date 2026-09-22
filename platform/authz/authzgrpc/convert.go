// Package authzgrpc converts between the canonical AuthZEN JSON model
// (platform/authz/decision) and its gRPC mirror (grpc/authzpb), for internal
// hot-path callers (ADR-14 / §4.6).
//
// The JSON model is authoritative; this is a faithful transport mirror. The
// round-trip equivalence test in this package pins the two together, so a field
// added to the JSON contract without a mirror here fails the build's tests
// rather than silently dropping on the gRPC path.
package authzgrpc

import (
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	authzpb "github.com/datakaveri/dx-common-go/grpc/authzpb"
	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// RequestToProto converts a canonical evaluation request to its gRPC form.
func RequestToProto(r decision.EvaluationRequest) (*authzpb.EvaluationRequest, error) {
	subjProps, err := structFromMap(r.Subject.Properties)
	if err != nil {
		return nil, err
	}
	actProps, err := structFromMap(r.Action.Properties)
	if err != nil {
		return nil, err
	}
	resProps, err := structFromMap(r.Resource.Properties)
	if err != nil {
		return nil, err
	}
	out := &authzpb.EvaluationRequest{
		Subject:  &authzpb.Subject{Type: string(r.Subject.Type), Id: r.Subject.ID, Properties: subjProps},
		Action:   &authzpb.Action{Name: r.Action.Name, Properties: actProps},
		Resource: &authzpb.Resource{Type: r.Resource.Type, Id: r.Resource.ID, Properties: resProps},
		Context:  requestContextToProto(r.Context),
	}
	return out, nil
}

// RequestFromProto converts a gRPC request back to the canonical model.
func RequestFromProto(p *authzpb.EvaluationRequest) decision.EvaluationRequest {
	if p == nil {
		return decision.EvaluationRequest{}
	}
	var r decision.EvaluationRequest
	if p.Subject != nil {
		r.Subject = decision.Subject{Type: decision.SubjectType(p.Subject.Type), ID: p.Subject.Id, Properties: mapFromStruct(p.Subject.Properties)}
	}
	if p.Action != nil {
		r.Action = decision.Action{Name: p.Action.Name, Properties: mapFromStruct(p.Action.Properties)}
	}
	if p.Resource != nil {
		r.Resource = decision.Resource{Type: p.Resource.Type, ID: p.Resource.Id, Properties: mapFromStruct(p.Resource.Properties)}
	}
	r.Context = requestContextFromProto(p.Context)
	return r
}

func requestContextToProto(c *decision.RequestContext) *authzpb.RequestContext {
	if c == nil {
		return nil
	}
	out := &authzpb.RequestContext{Agent: c.Agent}
	if c.DX != nil {
		dx := c.DX
		pdx := &authzpb.DXRequestContext{
			Profile: dx.Profile,
			Operation: &authzpb.OperationRef{
				Service: dx.Operation.Service, Id: dx.Operation.ID, ManifestDigest: dx.Operation.ManifestDigest,
			},
			Purpose: dx.Purpose,
		}
		if dx.Actor != nil {
			pdx.Actor = &authzpb.Actor{
				Type: string(dx.Actor.Type), Id: dx.Actor.ID, DelegationId: dx.Actor.DelegationID,
				SessionId: dx.Actor.SessionID, Chain: dx.Actor.Chain,
				TransactionId: dx.Actor.TransactionID, MessageId: dx.Actor.MessageID,
			}
		}
		if dx.PEP != nil {
			pdx.Pep = &authzpb.PEPInfo{Id: dx.PEP.ID, Capabilities: dx.PEP.Capabilities}
		}
		if dx.Request != nil {
			pdx.Request = &authzpb.RequestFacts{
				Fields: dx.Request.Fields, DeliveryMode: dx.Request.DeliveryMode, TermsHash: dx.Request.TermsHash,
				Amount: dx.Request.Amount, Currency: dx.Request.Currency, Payee: dx.Request.Payee,
			}
		}
		if dx.Approval != nil {
			pdx.Approval = &authzpb.ApprovalRef{
				Id:            dx.Approval.ID,
				ApprovedAt:    tsToProto(dx.Approval.ApprovedAt),
				ApprovedUntil: tsToProto(dx.Approval.ApprovedUntil),
			}
		}
		out.Dx = pdx
	}
	return out
}

func requestContextFromProto(p *authzpb.RequestContext) *decision.RequestContext {
	if p == nil {
		return nil
	}
	out := &decision.RequestContext{Agent: p.Agent}
	if p.Dx != nil {
		dx := &decision.DXRequestContext{Profile: p.Dx.Profile, Purpose: p.Dx.Purpose}
		if op := p.Dx.Operation; op != nil {
			dx.Operation = decision.OperationRef{Service: op.Service, ID: op.Id, ManifestDigest: op.ManifestDigest}
		}
		if a := p.Dx.Actor; a != nil {
			dx.Actor = &decision.Actor{
				Type: decision.SubjectType(a.Type), ID: a.Id, DelegationID: a.DelegationId,
				SessionID: a.SessionId, Chain: a.Chain, TransactionID: a.TransactionId, MessageID: a.MessageId,
			}
		}
		if pep := p.Dx.Pep; pep != nil {
			dx.PEP = &decision.PEPInfo{ID: pep.Id, Capabilities: pep.Capabilities}
		}
		if rf := p.Dx.Request; rf != nil {
			dx.Request = &decision.RequestFacts{
				Fields: rf.Fields, DeliveryMode: rf.DeliveryMode, TermsHash: rf.TermsHash,
				Amount: rf.Amount, Currency: rf.Currency, Payee: rf.Payee,
			}
		}
		if ap := p.Dx.Approval; ap != nil {
			dx.Approval = &decision.ApprovalRef{ID: ap.Id, ApprovedAt: tsFromProto(ap.ApprovedAt), ApprovedUntil: tsFromProto(ap.ApprovedUntil)}
		}
		out.DX = dx
	}
	return out
}

// ResponseToProto converts a canonical decision response to its gRPC form.
func ResponseToProto(r *decision.EvaluationResponse) *authzpb.EvaluationResponse {
	if r == nil {
		return nil
	}
	out := &authzpb.EvaluationResponse{Decision: r.Decision}
	if r.Context != nil {
		dc := &authzpb.DecisionContext{ReasonUser: r.Context.ReasonUser}
		if dx := r.Context.DX; dx != nil {
			pdx := &authzpb.DXDecisionContext{
				Profile: dx.Profile, EvaluationId: dx.EvaluationID, ReasonCode: string(dx.ReasonCode),
				Complete: dx.Complete, EvaluatedProfile: string(dx.EvaluatedProfile),
				ValidUntil: tsToProto(dx.ValidUntil), Attestation: dx.Attestation,
			}
			if ev := dx.Evidence; ev != nil {
				pdx.Evidence = &authzpb.Evidence{
					ManifestDigest: ev.ManifestDigest, FgaModelId: ev.FGAModelID,
					GrantProjectionRevision: ev.GrantProjectionRevision,
					ConditionSchemaVersion:  int32(ev.ConditionSchemaVersion), MappingDigest: ev.MappingDigest,
				}
			}
			dc.Dx = pdx
		}
		out.Context = dc
	}
	return out
}

// ResponseFromProto converts a gRPC response back to the canonical model.
func ResponseFromProto(p *authzpb.EvaluationResponse) *decision.EvaluationResponse {
	if p == nil {
		return nil
	}
	out := &decision.EvaluationResponse{Decision: p.Decision}
	if p.Context != nil {
		dc := &decision.DecisionContext{ReasonUser: p.Context.ReasonUser}
		if dx := p.Context.Dx; dx != nil {
			ddx := &decision.DXDecisionContext{
				Profile: dx.Profile, EvaluationID: dx.EvaluationId, ReasonCode: decision.ReasonCode(dx.ReasonCode),
				Complete: dx.Complete, EvaluatedProfile: decision.DecisionProfile(dx.EvaluatedProfile),
				ValidUntil: tsFromProto(dx.ValidUntil), Attestation: dx.Attestation,
			}
			if ev := dx.Evidence; ev != nil {
				ddx.Evidence = &decision.Evidence{
					ManifestDigest: ev.ManifestDigest, FGAModelID: ev.FgaModelId,
					GrantProjectionRevision: ev.GrantProjectionRevision,
					ConditionSchemaVersion:  int(ev.ConditionSchemaVersion), MappingDigest: ev.MappingDigest,
				}
			}
			dc.DX = ddx
		}
		out.Context = dc
	}
	return out
}

// ── helpers ─────────────────────────────────────────────────────────────────

func structFromMap(m map[string]any) (*structpb.Struct, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return structpb.NewStruct(m)
}

func mapFromStruct(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	m := s.AsMap()
	if len(m) == 0 {
		return nil
	}
	return m
}

func tsToProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func tsFromProto(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}
