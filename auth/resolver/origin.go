// Package resolver is a single auth middleware that establishes the request
// subject from one of two sources:
//
//  1. HMAC-signed X-Subject-* headers (the "gateway path") — verified via
//     dx-common-go/transport/headers. Trusted because only the gateway knows
//     the shared secret.
//  2. Authorization: Bearer <jwt> (the "direct path") — verified against
//     Keycloak's JWKS by dx-common-go/auth/jwt.
//
// HMAC is tried first. If a signature header is present BUT invalid, the
// request is rejected — falling through to JWT in that case would let a
// caller smuggle a wrong identity past the signature check.
//
// Downstream handlers and middlewares can call auth.UserFromCtx as usual; the
// caller's path can be inspected with resolver.OriginFromCtx for per-route
// policy. NOTHING does today: the one guard that did (RequireGatewayOrigin) is
// deleted, because "valid shared-secret signature" was never "the gateway"
// (ROADMAP P0-17). This is provenance for logs and tests.
package resolver

import "context"

// Origin identifies which auth path established the subject.
type Origin string

const (
	// OriginGateway means the subject was verified from a gateway-signed
	// HMAC X-Subject-* header set.
	OriginGateway Origin = "gateway"

	// OriginDirect means the subject was verified from a Bearer JWT carried
	// directly by the caller — typically not via the gateway.
	OriginDirect Origin = "direct"
)

type contextKey string

const originContextKey contextKey = "dx_auth_origin"

// WithOrigin tags the context with the resolver origin. Called by the
// middleware after a successful verification.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originContextKey, o)
}

// OriginFromCtx returns the resolver origin if set.
func OriginFromCtx(ctx context.Context) (Origin, bool) {
	o, ok := ctx.Value(originContextKey).(Origin)
	return o, ok
}
