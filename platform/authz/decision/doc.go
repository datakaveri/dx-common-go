// Package decision is the platform's AuthZEN authorization contract.
//
// It defines the wire types every PEP and the PDP share: the OpenID AuthZEN
// Authorization API 1.0 Subject-Action-Resource-Context (SARC) request and
// boolean decision, plus the CDPG profiles that ride in AuthZEN's deliberately
// open `context` object — the request profile (urn:dx:authzen:req:1), the
// decision profile (urn:dx:authzen:dec:1) and the typed obligation catalogue.
//
// # Why this package exists
//
// AuthZEN standardises the decision EXCHANGE and leaves the semantics of the
// response `context` out of scope. That open field is exactly where a data
// exchange must carry row filters, field policy, quotas and decision evidence.
// This package is the one place those semantics are named, so a gateway PEP, a
// service PEP, the MCP gateway and the fabric edges cannot each invent their
// own — and so OpenFGA's relation/tuple vocabulary never leaks past dx-authz-go
// (the engine is private; the contract is not).
//
// It is transport-neutral: no net/http, no gRPC, no OpenFGA. The HTTP binding
// lives in dx-authz-go's transport layer and the PEP client in
// platform/authz/client; both speak these types.
//
// See claude-work/AUTHZEN-COAZ-MCP-BECKN-ADOPTION-PLAN.md §7 for the contract
// this package realises.
package decision
