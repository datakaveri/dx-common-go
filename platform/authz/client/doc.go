// Package client is the platform's AuthZEN PEP client.
//
// It is what a gateway, service, worker, MCP gateway or fabric edge uses to ask
// dx-authz-go for a decision — the replacement for auth/fga's relation-shaped
// Check. Callers depend on the Evaluator interface (so tests use a fake), get
// FAIL-CLOSED behaviour for free (any transport error, non-200 or
// not-fully-honourable response is a deny), and get PEP-side obligation
// capability enforcement: an allow carrying a required obligation this PEP did
// not advertise is turned into a deny (invariant I-7, plan §7.4).
//
// It speaks the decision package's AuthZEN types and never imports OpenFGA.
package client
