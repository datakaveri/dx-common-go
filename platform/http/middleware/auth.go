// Package middleware carries the platform's HTTP middleware.
//
// Layer: L2 (edge).
package middleware

import (
	"net/http"

	dxjwt "github.com/datakaveri/dx-common-go/auth/jwt"
	authresolver "github.com/datakaveri/dx-common-go/auth/resolver"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"

	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// AuthConfig is the platform's inbound identity wiring.
//
// One type replacing the `type AuthConfig struct{SharedSecret string; JWT ...}`
// currently redeclared in 17 service files.
type AuthConfig struct {
	// HMACSecret verifies the gateway-signed X-Subject-* identity headers. This
	// is the primary path: the gateway is the single PEP, and everything behind
	// it trusts headers it signed.
	HMACSecret string
	// JWT enables a direct Bearer path alongside HMAC, for an operator calling
	// a service without going through the gateway.
	JWT dxjwt.Config
}

// Resolve verifies the caller and puts an identity.Subject on the request
// context, for httpx.Actor and the router's auth gate to read.
//
// It wraps the existing auth resolver rather than reimplementing verification —
// HMAC-first with a JWT fallback, and an invalid HMAC never falls through to
// JWT, which is the property that stops a forged header downgrading into an
// unauthenticated-but-accepted request. This is a TRANSITIONAL bridge: the
// resolver writes the legacy auth.DxUser, and this converts it to the platform
// Subject so migrated handlers see one identity type. It goes away with the
// legacy auth package in Wave 4.
//
// AllowDirect mirrors the established rule: the direct path opens only when JWT
// validation is actually enabled, or when no secret is configured at all (dev,
// where the resolver injects a synthetic user). A production config that HAS a
// secret therefore never silently accepts unauthenticated direct calls.
func Resolve(cfg AuthConfig) func(http.Handler) http.Handler {
	resolver := authresolver.Middleware(authresolver.Config{
		Headers:     dxheaders.Config{Secret: []byte(cfg.HMACSecret)},
		JWT:         cfg.JWT,
		AllowDirect: cfg.JWT.Enabled || cfg.HMACSecret == "",
	})

	return func(next http.Handler) http.Handler {
		return resolver(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sub, ok := SubjectFrom(r); ok {
				r = r.WithContext(identity.With(r.Context(), sub))
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// SubjectFrom converts whatever identity the legacy resolver left on the
// request into a platform Subject.
func SubjectFrom(r *http.Request) (identity.Subject, bool) {
	u, ok := authFromCtx(r)
	if !ok || u.ID == "" {
		return identity.Subject{}, false
	}

	sub := identity.Subject{
		ID: u.ID, Email: u.Email, Name: u.Name,
		Org: u.OrganisationID, OrgName: u.OrganisationName,
		Roles: u.Roles,
	}

	// Delegation. The legacy type carries three separate mechanisms in flat
	// fields; the platform models them as one "constrained credential acting
	// for a principal", distinguished by Kind so a policy can treat an
	// autonomous agent differently from a colleague acting on your behalf.
	switch {
	case u.AgentSubject != "":
		sub.Delegation = &identity.Delegation{
			Actor: u.AgentSubject, GrantID: u.DelegationID,
			Kind: identity.KindAgent, Scopes: scopesOf(u),
		}
	case u.DelegatorID != "":
		sub.Delegation = &identity.Delegation{
			Actor: u.DelegatorID, GrantID: u.DelegationID,
			Kind: identity.KindUser, Scopes: scopesOf(u),
		}
	case len(u.Scopes) > 0:
		// Scopes with no named actor is an application credential: the app is
		// acting for the user, and the token names the scopes but not itself.
		sub.Delegation = &identity.Delegation{
			GrantID: u.DelegationID, Kind: identity.KindApp, Scopes: scopesOf(u),
		}
	}
	return sub, true
}
