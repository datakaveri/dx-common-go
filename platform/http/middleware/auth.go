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
	"github.com/datakaveri/dx-common-go/platform/security/workload"
)

// AuthConfig is the platform's inbound identity wiring.
//
// One type replacing the `type AuthConfig struct{SharedSecret string; JWT ...}`
// currently redeclared in 17 service files.
// Mode selects how a missing credential is treated.
//
// The zero value is Required, deliberately: a service that forgets to set this
// gets the strict resolver rather than an open one. A bool named "Optional"
// would default to false and read as a decision nobody made.
type Mode int

const (
	// Required rejects a request with no verified caller.
	Required Mode = iota
	// Optional serves an anonymous request, but still REJECTS one whose
	// credential is present and invalid.
	//
	// That distinction is the whole point. Falling through to anonymous on a
	// bad credential hands an attacker a downgrade: corrupt your own token and
	// receive the anonymous view of an endpoint that would otherwise have
	// rejected you outright.
	Optional
)

type AuthConfig struct {
	// Mode controls whether an ABSENT credential is tolerated. It never
	// tolerates an invalid one.
	Mode Mode
	// TrustSubjectHeaders enables the internal X-Subject-* identity path. This
	// is the primary path: the gateway is the single PEP, and everything behind
	// it reads the subject it projected.
	//
	// The headers are UNSIGNED (ROADMAP P0-17 stage 2). Setting this alone opens
	// nothing: the resolver additionally requires a verified workload principal
	// on the request, so a service with no Workload verifier rejects every
	// subject header rather than trusting a client-supplied one.
	TrustSubjectHeaders bool
	// JWT enables a direct Bearer path alongside the subject headers, for an operator calling
	// a service without going through the gateway.
	JWT dxjwt.Config

	// Workload authenticates the CALLING SERVICE, which is a different question
	// from the one above: the subject headers and JWT establish *which user* a request
	// speaks for, this establishes *which workload* is speaking.
	//
	// Under the shared HMAC those two collapsed into one — possession of the
	// secret was authority to assert any user to any service (review finding
	// C-02, ROADMAP P0-2). Separating them is the fix, and it is why this is a
	// distinct field rather than another flag on the subject-header path.
	//
	// NIL DISABLES IT, which is the rollout default: a service that pulls this
	// library and changes nothing behaves exactly as before. Build a verifier
	// with workload.NewVerifier once the service's Keycloak client and audience
	// scope exist. See ADR-06.
	Workload *workload.Verifier
}

// Resolve verifies the caller and puts an identity.Subject on the request
// context, for httpx.Actor and the router's auth gate to read.
//
// It wraps the existing auth resolver rather than reimplementing verification —
// subject headers first with a JWT fallback. Those headers carry no signature
// since P0-17 stage 2; what stops a forged header being accepted is the
// workload gate below, which refuses to read them at all unless a verified
// caller presented them. This is a TRANSITIONAL bridge: the
// resolver writes the legacy auth.DxUser, and this converts it to the platform
// Subject so migrated handlers see one identity type. It goes away with the
// legacy auth package in Wave 4.
//
// AllowDirect mirrors the established rule: the direct path opens only when JWT
// validation is actually enabled, or when the internal path is not trusted at
// all (dev, where the resolver injects a synthetic user). A production config
// that accepts internal calls therefore never silently accepts unauthenticated
// direct ones.
func Resolve(cfg AuthConfig) func(http.Handler) http.Handler {
	resolver := authresolver.Middleware(authresolver.Config{
		TrustSubjectHeaders: cfg.TrustSubjectHeaders,
		JWT:                 cfg.JWT,
		AllowDirect:         cfg.JWT.Enabled || !cfg.TrustSubjectHeaders,
	})

	// The workload gate wraps EVERYTHING below, so it runs before the subject is
	// resolved. Since P0-17 stage 2 that order is not merely the point, it is
	// the ONLY thing authenticating the subject: the headers carry no signature,
	// so the gate's verified principal is what the resolver checks for before
	// reading them. Resolving the subject first would mean trusting the
	// X-Subject-* headers in order to decide whether to trust them.
	//
	// With cfg.Workload nil this is the identity function — and the resolver
	// then rejects every subject header, because there is no verified caller.
	workloadGate := workload.Middleware(cfg.Workload)

	return func(next http.Handler) http.Handler {
		publish := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sub, ok := SubjectFrom(r); ok {
				r = r.WithContext(identity.With(r.Context(), sub))
			}
			next.ServeHTTP(w, r)
		})
		inner := resolver(publish)

		if cfg.Mode == Required {
			return workloadGate(inner)
		}
		// Optional applies to the USER credential only. The workload gate still
		// wraps it, so a service-to-service call with no user identity is served
		// anonymously while its calling workload is still authenticated.
		return workloadGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Optional: only run the resolver when a credential is actually
			// present. Running it unconditionally would reject the anonymous
			// request this mode exists to serve.
			//
			// A credential that IS present goes through the full resolver, so
			// an invalid one still fails — absence is tolerated, invalidity is
			// not.
			if hasCredential(r, cfg.TrustSubjectHeaders) {
				inner.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// hasCredential reports whether the request carries any credential the
// resolver would act on.
//
// It must consider EVERY form the resolver accepts. Missing one — Basic for
// app credentials, say — silently anonymises a caller who did authenticate,
// which is the failure mode optional auth is most likely to produce.
func hasCredential(r *http.Request, subjectHeadersTrusted bool) bool {
	if subjectHeadersTrusted && dxheaders.Asserts(r.Header) {
		return true
	}
	return r.Header.Get("Authorization") != ""
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
