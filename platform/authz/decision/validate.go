package decision

import "fmt"

// ValidateRequest checks the structural invariants a PDP relies on before it
// evaluates. It does NOT authorize anything; it rejects requests the composite
// pipeline could otherwise misread. Trust anchoring (subject.id equals the
// token claim) is enforced by the PEP that builds the request, not here.
func (r *EvaluationRequest) Validate() error {
	if err := ValidateSubjectType(r.Subject.Type); err != nil {
		return err
	}
	if r.Subject.ID == "" {
		return fmt.Errorf("subject.id is required")
	}
	if r.Action.Name == "" {
		return fmt.Errorf("action.name is required")
	}
	if r.Resource.Type == "" || r.Resource.ID == "" {
		return fmt.Errorf("resource.type and resource.id are required")
	}
	if dx := r.DXContext(); dx != nil {
		if dx.Profile != ProfileRequestV1 {
			return fmt.Errorf("context.dx.profile must be %q, got %q", ProfileRequestV1, dx.Profile)
		}
		if dx.Operation.ID == "" {
			return fmt.Errorf("context.dx.operation.id is required")
		}
		if dx.Actor != nil {
			if err := ValidateSubjectType(dx.Actor.Type); err != nil {
				return fmt.Errorf("context.dx.actor.type: %w", err)
			}
			if dx.Actor.ID == "" {
				return fmt.Errorf("context.dx.actor.id is required when actor is present")
			}
		}
	}
	return nil
}

// ValidateSubjectType accepts the closed wire vocabulary plus the deprecated
// `user` alias (Class C migration window).
func ValidateSubjectType(t SubjectType) error {
	switch t {
	case SubjectIdentity, SubjectWorkload, SubjectAgent, SubjectNetworkParticipant, SubjectUserAlias:
		return nil
	default:
		return fmt.Errorf("unknown subject.type %q", t)
	}
}

// CanonicalSubjectType folds the deprecated `user` alias onto `identity` so the
// rest of the pipeline sees one spelling.
func CanonicalSubjectType(t SubjectType) SubjectType {
	if t == SubjectUserAlias {
		return SubjectIdentity
	}
	return t
}
