package resolver

import (
	dxjwt "github.com/datakaveri/dx-common-go/auth/jwt"
)

// Config selects which inbound identity paths a service accepts.
type Config struct {
	// TrustSubjectHeaders enables the internal X-Subject-* path.
	//
	// It is NOT sufficient on its own, and that is the point. The headers are
	// unsigned (ROADMAP P0-17 stage 2), so Middleware additionally requires a
	// VERIFIED WORKLOAD on the request context before reading them. Setting
	// this on a service with no workload verifier therefore does not open a
	// spoofing hole — it opens nothing at all, and every such request is
	// rejected with "subject headers require a verified workload caller".
	//
	// This replaces the old `Headers dxheaders.Config`, whose non-empty Secret
	// both enabled the path AND was the control. Splitting them is deliberate:
	// possession of a shared secret was never evidence of which caller sent the
	// request (review finding C-02).
	TrustSubjectHeaders bool

	// JWT is the Keycloak validator config (JWKS URL, issuer, audience, leeway).
	// If JWT.Enabled is false the JWT fallback is disabled — all requests must
	// use the subject-header path.
	JWT dxjwt.Config

	// AllowDirect controls whether the direct (JWT) path is permitted at all.
	// When false, only internal calls carrying subject headers are accepted.
	//
	// Defaults to false. Set true to enable JWT fallback. When AllowDirect
	// is true, dxjwt.Middleware is built from cfg.JWT — its Enabled flag
	// determines real validation vs dev-mode synthetic-user injection.
	//
	// For per-route enforcement (allow JWT for most routes, deny for a few)
	// keep AllowDirect=true and apply workload.RequireCaller on the routes that
	// must be internal-only.
	AllowDirect bool
}
