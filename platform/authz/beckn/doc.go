// Package beckn maps a Beckn/NFH fabric interaction onto the platform AuthZEN
// contract. It is the reusable authorization core the fabric edge (buy-side) and
// fabric webhook (sell-side) both consume — the SARC construction, the material-
// terms binding, and the Case A/Case B inbound classifier from the adoption plan
// §9.6–9.7.
//
// It is transport-neutral: it constructs decision.EvaluationRequest values and
// canonical term hashes. It knows NOTHING about ONIX, Beckn message shapes, DeDi
// registration or signatures — those are network-trust and protocol concerns
// that live in the fabric services and never substitute for a local decision
// (invariant I-10, P6). Conversely nothing here is ever emitted onto the Beckn
// wire (I-10): these requests, digests and decisions are internal.
package beckn
