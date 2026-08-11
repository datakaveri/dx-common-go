// Package resolver is a single auth middleware that establishes the request
// subject from one of two sources:
//
//  1. X-Subject-* headers (the "gateway path") — parsed via
//     dx-common-go/transport/headers. THESE CARRY NO SIGNATURE. They used to be
//     HMAC-signed, and P0-17 stage 2 removed that because every asserter held
//     the same secret, so the signature proved group membership and never
//     identity. They are trusted because the middleware refuses to read them
//     unless a VERIFIED WORKLOAD is on the request context.
//  2. Authorization: Bearer <jwt> (the "direct path") — verified against
//     Keycloak's JWKS by dx-common-go/auth/jwt.
//
// The header path is tried first. There is no signature to reject any more, so
// what once guarded this — refusing to fall through to JWT on a bad signature —
// is now the workload gate: no verified caller, no subject headers read.
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
	// OriginGateway means the subject came from an X-Subject-* header set,
	// which is trustworthy only because a verified workload presented it —
	// the headers themselves are unsigned since P0-17 stage 2.
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
