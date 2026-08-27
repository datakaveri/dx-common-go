// Package constraint is the platform's typed contextual-constraint evaluator.
//
// It is the "typed Go evaluator over a closed, versioned condition vocabulary"
// that ADR-10 §3.7 / §1.5.1 chose over a policy runtime (OPA/Rego). The
// conditions it evaluates are a CLOSED set of boolean predicates — validity
// window, role intersection, purpose membership, source-CIDR containment and
// assurance level — selected, never computed, from a grant. There is no partial
// evaluation and no residual translation, which is exactly why a policy runtime
// is not required here.
//
// It is pure: no network, no filesystem, deterministic. It sits behind the
// Evaluator interface so a different backend (e.g. a Rego bundle) could be
// substituted without touching the composite pipeline — the adoption trigger in
// ADR-10 §1.5.1 — but until that trigger fires the typed evaluator is the whole
// implementation.
//
// Fail-closed is the rule: a condition whose required input is absent evaluates
// to a DENY with a reason, never "unknown, therefore allow" (plan I-11).
package constraint
