// Package attestation is the decision-attestation profile (urn:dx:authzen:att:1).
//
// AuthZEN standardises the decision REQUEST but not a portable decision; ADR-10/
// A1 nevertheless requires the allow and its obligations to travel to the data
// plane so the data plane never calls the PDP (invariant I-8). This package is
// that carried decision: a short-lived, audience-bound, EdDSA-signed compact JWS
// bound to subject, actor, operation, resource and the selected obligations.
//
// The data plane VERIFIES it (audience, expiry, jti replay, operation/resource
// binding, signature) and enforces the obligations; it is never trusted on
// sight and never re-fetched. Issuance uses asymmetric keys (ADR-06 signing
// infrastructure), not the shared HMAC.
//
// The JWS is built by hand (crypto/ed25519 + base64url) to avoid a JWT
// dependency and to keep the exact claim set under the platform's control.
package attestation
