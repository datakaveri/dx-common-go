package decision

import (
	"encoding/json"
	"fmt"
)

// ObligationType is the closed core catalogue (§7.4). Security-critical types
// are `required` and a PEP that cannot enforce one MUST deny (P5, I-7).
type ObligationType string

const (
	ObRowFilter    ObligationType = "row_filter"
	ObFieldPolicy  ObligationType = "field_policy"
	ObQuota        ObligationType = "quota"
	ObDeliveryMode ObligationType = "delivery_mode"
	ObTimeBound    ObligationType = "time_bound"
	ObRateLimit    ObligationType = "rate_limit"
	ObAuditTag     ObligationType = "audit_tag"
)

// requiredCore is the set of obligation types whose absence of enforcement is
// an over-disclosure. It is closed by construction: an unknown type is never
// silently treated as advisory.
var requiredCore = map[ObligationType]bool{
	ObRowFilter:    true,
	ObFieldPolicy:  true,
	ObQuota:        true,
	ObDeliveryMode: true,
	ObTimeBound:    true,
	ObRateLimit:    false,
	ObAuditTag:     false,
}

// IsKnownObligation reports whether a type is in the closed core catalogue.
func IsKnownObligation(t ObligationType) bool {
	_, ok := requiredCore[t]
	return ok
}

// Capability is the string a PEP advertises and the PDP matches, "<type>@<version>".
func Capability(t ObligationType, version int) string {
	return fmt.Sprintf("%s@%d", t, version)
}

// Obligation is one typed, versioned instruction a PEP must enforce after an
// allow. Type/Version/Required are always present and typed; the type-specific
// body is preserved verbatim so an unknown-but-required obligation still
// round-trips and a PEP can SEE it in order to deny on it (I-7).
type Obligation struct {
	Type     ObligationType
	Version  int
	Required bool
	// Body holds the type-specific members (expression, allow, mask, limit…),
	// preserved as raw JSON.
	Body map[string]json.RawMessage
}

// Capability returns this obligation's "<type>@<version>" capability string.
func (o Obligation) Capability() string { return Capability(o.Type, o.Version) }

// obligationEnvelope is the reserved key set; everything else is Body.
type obligationEnvelope struct {
	Type     ObligationType `json:"type"`
	Version  int            `json:"version"`
	Required bool           `json:"required"`
}

// MarshalJSON flattens Type/Version/Required and the type-specific Body into one
// object. Body keys never collide with the reserved keys (rejected on decode).
func (o Obligation) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	for k, v := range o.Body {
		out[k] = v
	}
	out["type"], _ = json.Marshal(o.Type)
	out["version"], _ = json.Marshal(o.Version)
	out["required"], _ = json.Marshal(o.Required)
	return json.Marshal(out)
}

// UnmarshalJSON splits the reserved keys from the type-specific body.
func (o *Obligation) UnmarshalJSON(data []byte) error {
	var env obligationEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	if env.Type == "" {
		return fmt.Errorf("obligation: missing type")
	}
	all := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	delete(all, "type")
	delete(all, "version")
	delete(all, "required")
	o.Type = env.Type
	o.Version = env.Version
	o.Required = env.Required
	o.Body = all
	return nil
}

// DecodeBody unmarshals the type-specific body into a typed payload such as
// RowFilter or FieldPolicy.
func (o Obligation) DecodeBody(into any) error {
	b, err := json.Marshal(o.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// Typed payloads. These are convenience decoders over Body; the wire form is
// the flattened object produced by MarshalJSON.

// RowFilter is a typed predicate AST the data plane ANDs into its query. The
// evaluator emits it; a raw datastore query is never carried (ADR-10 §3.8).
type RowFilter struct {
	Expression json.RawMessage `json:"expression"`
}

// FieldPolicy is an allow-list plus per-field masking.
type FieldPolicy struct {
	Allow []string          `json:"allow,omitempty"`
	Mask  map[string]string `json:"mask,omitempty"`
}

// Quota selects a metered limit; SELECTION here is separate from CONSUMPTION at
// the enforcing PEP (ADR-10 §5.13).
type Quota struct {
	Metric     string `json:"metric"`
	Limit      int64  `json:"limit"`
	Window     string `json:"window"`
	CounterKey string `json:"counter_key,omitempty"`
}

// DeliveryMode restricts how data may be delivered (api|file|sub…).
type DeliveryMode struct {
	Allow []string `json:"allow"`
}

// NewObligation builds a typed obligation from a payload struct, setting
// required from the core catalogue.
func NewObligation(t ObligationType, version int, payload any) (Obligation, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Obligation{}, err
	}
	body := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &body); err != nil {
		return Obligation{}, err
	}
	delete(body, "type")
	delete(body, "version")
	delete(body, "required")
	return Obligation{Type: t, Version: version, Required: requiredCore[t], Body: body}, nil
}

// UnsupportedRequired returns the required obligations in the set whose
// "<type>@<version>" capability is not advertised. A PEP denies when this is
// non-empty (I-7). It also treats an unknown (non-catalogue) required type as
// unsupported, because a PEP cannot claim to enforce what it cannot name.
func UnsupportedRequired(obs []Obligation, advertised map[string]bool) []Obligation {
	var out []Obligation
	for _, o := range obs {
		if !o.Required {
			continue
		}
		if !IsKnownObligation(o.Type) || !advertised[o.Capability()] {
			out = append(out, o)
		}
	}
	return out
}
