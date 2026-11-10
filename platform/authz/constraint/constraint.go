package constraint

import (
	"fmt"
	"net/netip"
	"time"
)

// ConditionSet is the closed, versioned vocabulary of contextual conditions a
// grant may carry. A zero value imposes no conditions and always passes. Every
// field is optional and ANDed: all present conditions must hold.
type ConditionSet struct {
	// Version pins the condition schema; recorded in decision evidence.
	Version int `json:"version,omitempty"`
	// NotBefore/NotAfter bound validity using the PDP's trusted clock.
	NotBefore *time.Time `json:"not_before,omitempty"`
	NotAfter  *time.Time `json:"not_after,omitempty"`
	// AllowedRoles requires the subject to hold at least one of these roles.
	AllowedRoles []string `json:"allowed_roles,omitempty"`
	// AllowedPurposes requires the request purpose to be one of these.
	AllowedPurposes []string `json:"allowed_purposes,omitempty"`
	// SourceCIDRs requires the source address to fall within one of these.
	SourceCIDRs []string `json:"source_cidrs,omitempty"`
	// MinAssurance requires the authentication assurance to meet this level.
	MinAssurance string `json:"min_assurance,omitempty"`
}

// Input is the trusted request context a ConditionSet is evaluated against. It
// is assembled by the PDP from verified facts, never from untrusted payload.
type Input struct {
	// Now is the PDP's trusted server time (never a caller-provided value).
	Now time.Time
	// Roles are the subject's verified roles.
	Roles []string
	// Purpose is the request purpose, once resolved/verified.
	Purpose string
	// SourceIP is the verified source address; the zero Addr means "unknown".
	SourceIP netip.Addr
	// Assurance is the authentication assurance level (e.g. "mfa").
	Assurance string
}

// Outcome is the evaluation result. Allowed is true only when every present
// condition held; otherwise Reason names the first failing condition with a
// stable, non-sensitive code.
type Outcome struct {
	Allowed bool
	Reason  string
}

func allow() Outcome             { return Outcome{Allowed: true} }
func deny(reason string) Outcome { return Outcome{Allowed: false, Reason: reason} }

// Evaluator evaluates a ConditionSet against an Input. It is the seam that keeps
// the composite pipeline independent of HOW conditions are evaluated.
type Evaluator interface {
	Evaluate(cs ConditionSet, in Input) Outcome
}

// Typed is the pure-Go evaluator. Safe for concurrent use; it holds no state.
type Typed struct{}

// New returns the typed evaluator.
func New() Typed { return Typed{} }

// assuranceRank orders assurance levels. An unknown level ranks below all known
// ones, so a MinAssurance requirement is never satisfied by an unrecognised
// input value (fail closed).
var assuranceRank = map[string]int{
	"none":     0,
	"password": 1,
	"otp":      2,
	"mfa":      3,
	"hardware": 4,
}

// Evaluate applies every present condition. The order is cheap-checks-first, and
// the first failure short-circuits with its reason.
func (Typed) Evaluate(cs ConditionSet, in Input) Outcome {
	if cs.NotBefore != nil && in.Now.Before(*cs.NotBefore) {
		return deny("not_yet_valid")
	}
	if cs.NotAfter != nil && in.Now.After(*cs.NotAfter) {
		return deny("expired")
	}
	if len(cs.AllowedRoles) > 0 && !intersects(cs.AllowedRoles, in.Roles) {
		return deny("role_not_permitted")
	}
	if len(cs.AllowedPurposes) > 0 {
		if in.Purpose == "" {
			return deny("purpose_required")
		}
		if !contains(cs.AllowedPurposes, in.Purpose) {
			return deny("purpose_not_permitted")
		}
	}
	if len(cs.SourceCIDRs) > 0 {
		ok, err := ipInAnyCIDR(in.SourceIP, cs.SourceCIDRs)
		if err != nil {
			// A malformed CIDR on the grant is a policy authoring fault; fail
			// closed rather than silently ignoring the network restriction.
			return deny("source_cidr_invalid")
		}
		if !ok {
			return deny("source_not_permitted")
		}
	}
	if cs.MinAssurance != "" {
		req, ok := assuranceRank[cs.MinAssurance]
		if !ok {
			return deny("assurance_requirement_invalid")
		}
		if assuranceRank[in.Assurance] < req { // unknown input assurance ranks 0
			return deny("assurance_insufficient")
		}
	}
	return allow()
}

func intersects(a, b []string) bool {
	set := make(map[string]struct{}, len(b))
	for _, x := range b {
		set[x] = struct{}{}
	}
	for _, x := range a {
		if _, ok := set[x]; ok {
			return true
		}
	}
	return false
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func ipInAnyCIDR(ip netip.Addr, cidrs []string) (bool, error) {
	if !ip.IsValid() {
		// A network restriction with no source address to check cannot be
		// satisfied — fail closed.
		return false, nil
	}
	for _, c := range cidrs {
		prefix, err := netip.ParsePrefix(c)
		if err != nil {
			return false, fmt.Errorf("invalid cidr %q: %w", c, err)
		}
		if prefix.Contains(ip) {
			return true, nil
		}
	}
	return false, nil
}

var _ Evaluator = Typed{}
