// Package workload establishes WHICH SERVICE is calling, separately from which
// end user that service is speaking for.
//
// # Why this exists
//
// The scheme it replaces signed the caller's identity with one HMAC secret
// shared by the gateway and every service. That made possession of the secret
// equivalent to the authority to assert any identity to any service: a single
// compromise was a platform-wide authentication bypass, and a captured header
// set replayed against a different service inside the freshness window
// (review finding C-02, ROADMAP P0-2).
//
// The fix is asymmetry. Each workload holds its own Keycloak client credential
// and mints a short-lived access token addressed to ONE destination service.
// Receivers verify that token against the realm's public JWKS, so a receiver
// can check a credential but can never issue one. That is the entire property:
// verification and issuance stop being the same capability.
//
// # The two questions, kept apart
//
//	Which workload is calling?   -> the token in X-DX-Workload. Cryptographic.
//	Which subject is it for?     -> the X-Subject-* headers, accepted ONLY from
//	                                a workload named in SubjectAsserters.
//
// A compromised ordinary service therefore asserts nothing at all, because it
// is not on that list. That is the difference between this and adding claims to
// the shared secret, which would still let every holder sign for anyone.
//
// # Audience is the anti-replay control
//
// A token names its destination in `aud`, and verification makes the audience
// mandatory and equal to the verifying service. Replaying a captured credential
// against a different service fails on that check with no new code deciding
// anything. Replay against the SAME service inside the token's lifetime is
// bounded, not eliminated — a distributed jti cache is deliberately deferred
// (ADR-06 §4.4).
//
// # Naming
//
// Audiences are namespaced "dx:svc:<service>" and carried by an optional
// Keycloak client scope "dx-aud:<service>". The namespace matters: an ordinary
// user token must never collide with a workload audience, or a user could
// present their own token where a workload credential is expected.
//
// See claude-docs/adr/ADR-06-workload-identity-and-subject-delegation.md.
//
// Layer: L1.
package workload

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// HdrWorkload carries the calling workload's access token, as "Bearer <jwt>".
//
// It is deliberately NOT Authorization: that header already carries the end
// user on the direct path, and one header holding two different principals
// depending on context is the ambiguity this package exists to remove.
const HdrWorkload = "X-DX-Workload"

const (
	// audiencePrefix namespaces workload audiences away from the audiences
	// Keycloak issues to ordinary user-facing clients ("account", and any
	// per-application audience). Without it, a user token carrying the plain
	// service name would satisfy a workload verifier.
	audiencePrefix = "dx:svc:"
	// scopePrefix names the optional Keycloak client scope that carries the
	// audience mapper for a destination.
	scopePrefix = "dx-aud:"
)

// AudienceFor returns the workload audience of a destination service, which is
// the value that service's verifier requires in `aud`.
func AudienceFor(service string) string { return audiencePrefix + service }

// ScopeFor returns the optional Keycloak client scope a caller must request to
// obtain a token addressed to service.
//
// Keycloak only honours a scope assigned to the requesting client, so the set
// of scopes a workload holds IS its permitted call graph — a caller cannot mint
// a credential for a service it was never granted.
func ScopeFor(service string) string { return scopePrefix + service }

// Principal is a verified calling workload.
//
// It is pure data produced by Verifier. Nothing downstream re-derives trust
// from it, and no constructor is exported, so a Principal on a context is
// always one a verifier put there.
type Principal struct {
	// ID is the workload's Keycloak client id (the `azp` claim) — the name to
	// use in AllowedCallers, SubjectAsserters and audit records.
	ID string

	// Subject is the `sub` claim: the service-account user Keycloak created for
	// the client. It is stable and useful for audit, but it is NOT the name to
	// authorize against — that is ID.
	Subject string

	// Audience is the audience this token was minted for, always this service.
	Audience string

	// TokenID is the `jti`. Recorded for audit and as the key a future
	// distributed replay cache would use.
	TokenID string

	IssuedAt  time.Time
	ExpiresAt time.Time
}

// ctxKey is unexported so no package outside this one can write a Principal
// onto a context. Everything downstream treats a context Principal as already
// verified, so the only writer must be the middleware that verified it.
type ctxKey struct{}

// With returns a context carrying p. Call it only after successful
// verification.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the verified calling workload on ctx, if any.
//
// It reports false when workload verification is disabled or when the request
// arrived on the legacy HMAC path, so a caller that must not run without a
// verified workload should use it as a gate rather than assuming presence.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// bearerToken extracts the token from a "Bearer <token>" header value. It
// returns "" for an absent or malformed value; callers treat "" as absent.
func bearerToken(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	scheme, token, found := strings.Cut(v, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// set turns a configured list into a lookup, dropping blanks so a stray empty
// entry in YAML cannot match a principal with an empty id.
func set(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			m[v] = struct{}{}
		}
	}
	return m
}

// passthrough is the no-op middleware used when workload verification is off.
func passthrough(next http.Handler) http.Handler { return next }
