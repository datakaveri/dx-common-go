package client

import (
	"context"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// Fake is an in-memory Evaluator for PEP tests. Set Fn to program responses;
// the default is a relationship allow. Every request is recorded on Calls.
type Fake struct {
	Fn    func(ctx context.Context, req decision.EvaluationRequest) (*decision.EvaluationResponse, error)
	Calls []decision.EvaluationRequest
}

// Evaluate records the call and returns the programmed response.
func (f *Fake) Evaluate(ctx context.Context, req decision.EvaluationRequest) (*decision.EvaluationResponse, error) {
	f.Calls = append(f.Calls, req)
	if f.Fn != nil {
		return f.Fn(ctx, req)
	}
	return decision.Allow("fake", decision.ProfileRelationship), nil
}

var _ Evaluator = (*Fake)(nil)
var _ Evaluator = (*Client)(nil)
