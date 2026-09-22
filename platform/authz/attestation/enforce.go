package attestation

import (
	"fmt"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// Enforcer is a data plane's obligation-enforcement boundary (invariants I-7,
// I-8). It verifies a carried attestation is for THIS component and THIS
// resource, then checks that every REQUIRED obligation is one this data plane
// can actually enforce — denying if not, because an allow whose required
// obligation the enforcer would silently drop is an over-disclosure.
//
// A data plane NEVER calls the PDP (I-8): the signed, audience-bound attestation
// IS the decision, and this is the only authorization work the data plane does.
type Enforcer struct {
	v    *Verifier
	caps map[string]bool
}

// NewEnforcer builds an enforcer over a verifier and the obligation capabilities
// this data plane can honour, as "<type>@<version>" strings (e.g. "row_filter@1",
// "delivery_mode@1"). An empty set means the data plane advertises NO obligation
// support, so any required obligation forces a deny.
func NewEnforcer(v *Verifier, capabilities []string) *Enforcer {
	caps := make(map[string]bool, len(capabilities))
	for _, c := range capabilities {
		caps[c] = true
	}
	return &Enforcer{v: v, caps: caps}
}

// Enforce verifies and binds the token to the operation+resource, then applies
// the capability gate. It returns the verified attestation — whose Obligations
// are what the handler must apply (row_filter, field_policy, quota, delivery_mode)
// — or an error the data plane renders as a denial.
//
// operationID may be empty to skip the operation binding; resourceType and
// resourceID are always bound (a token for another resource is refused).
func (e *Enforcer) Enforce(token, operationID, resourceType, resourceID string) (*Attestation, error) {
	att, err := e.v.VerifyBound(token, operationID, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	if unsup := decision.UnsupportedRequired(att.Obligations, e.caps); len(unsup) > 0 {
		return nil, fmt.Errorf("attestation: %d required obligation(s) this data plane cannot enforce (first: %s)",
			len(unsup), unsup[0].Capability())
	}
	return att, nil
}

// Supports reports whether this enforcer can honour the given obligation
// capability string.
func (e *Enforcer) Supports(capability string) bool { return e.caps[capability] }
