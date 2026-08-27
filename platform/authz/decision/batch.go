package decision

// Batch evaluation (POST /access/v1/evaluations). The top-level subject/action/
// resource/context act as DEFAULTS; each item overrides the members it sets.
// Options.Evaluation selects one of the three AuthZEN semantics.

// BatchSemantics is the evaluation strategy for a batch.
type BatchSemantics string

const (
	// SemExecuteAll runs every item and returns every result (default).
	SemExecuteAll BatchSemantics = "execute_all"
	// SemDenyOnFirstDeny stops at the first deny and returns results so far.
	SemDenyOnFirstDeny BatchSemantics = "deny_on_first_deny"
	// SemPermitOnFirstPermit stops at the first permit.
	SemPermitOnFirstPermit BatchSemantics = "permit_on_first_permit"
)

// BatchOptions carries the evaluation semantics.
type BatchOptions struct {
	Evaluation BatchSemantics `json:"evaluation,omitempty"`
}

// EvaluationItem overrides the batch defaults for one decision. A nil member
// inherits the top-level default.
type EvaluationItem struct {
	Subject  *Subject        `json:"subject,omitempty"`
	Action   *Action         `json:"action,omitempty"`
	Resource *Resource       `json:"resource,omitempty"`
	Context  *RequestContext `json:"context,omitempty"`
}

// EvaluationsRequest is the batch request.
type EvaluationsRequest struct {
	Subject     *Subject         `json:"subject,omitempty"`
	Action      *Action          `json:"action,omitempty"`
	Resource    *Resource        `json:"resource,omitempty"`
	Context     *RequestContext  `json:"context,omitempty"`
	Evaluations []EvaluationItem `json:"evaluations"`
	Options     *BatchOptions    `json:"options,omitempty"`
}

// EvaluationsResponse is the batch response: one decision per evaluated item, in
// order. Under a short-circuit semantics the array may be shorter than the
// request.
type EvaluationsResponse struct {
	Evaluations []EvaluationResponse `json:"evaluations"`
}

// Semantics returns the configured semantics, defaulting to execute_all.
func (r *EvaluationsRequest) Semantics() BatchSemantics {
	if r.Options == nil || r.Options.Evaluation == "" {
		return SemExecuteAll
	}
	return r.Options.Evaluation
}

// Merge resolves item i into a full EvaluationRequest by overlaying it on the
// batch defaults. An item member that is set wins; otherwise the default is
// used. This is the AuthZEN default-inheritance rule.
func (r *EvaluationsRequest) Merge(i int) EvaluationRequest {
	item := r.Evaluations[i]
	out := EvaluationRequest{}
	switch {
	case item.Subject != nil:
		out.Subject = *item.Subject
	case r.Subject != nil:
		out.Subject = *r.Subject
	}
	switch {
	case item.Action != nil:
		out.Action = *item.Action
	case r.Action != nil:
		out.Action = *r.Action
	}
	switch {
	case item.Resource != nil:
		out.Resource = *item.Resource
	case r.Resource != nil:
		out.Resource = *r.Resource
	}
	switch {
	case item.Context != nil:
		out.Context = item.Context
	case r.Context != nil:
		out.Context = r.Context
	}
	return out
}
