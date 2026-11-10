package resolver

import (
	"net/http"
	"strings"

	"github.com/datakaveri/dx-common-go/auth"
	dxjwt "github.com/datakaveri/dx-common-go/auth/jwt"
	dxerrors "github.com/datakaveri/dx-common-go/errors"
	"github.com/datakaveri/dx-common-go/platform/security/workload"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// Middleware returns a chi-compatible handler that establishes auth.DxUser
// in the request context from either the internal subject headers (preferred)
// or a Bearer JWT (fallback). See the package doc for the precedence rules.
//
// # What makes the subject headers trustworthy
//
// They are NOT signed (ROADMAP P0-17 stage 2 — see transport/headers for why
// the HMAC was removed rather than replaced). Their authority comes from the
// CALLER: platform/security/workload verifies the calling workload's
// audience-bound credential and, if that workload is not on the receiving
// service's subject_asserters list, rejects the request before this middleware
// ever runs.
//
// So this reads the headers only when a verified workload principal is on the
// context. That is a precondition, not a config flag, and deliberately so: a
// flag can be set on a service that has no verifier, and the result would be
// that any client could name any user by setting one header. There is no way to
// spell that mistake here — no verified caller, no subject headers.
//
// Misconfiguration is treated as a programming error and panics at handler
// construction time — both verification paths cannot be disabled simultaneously.
//
// Switches:
//   - Subject-header path is enabled by cfg.TrustSubjectHeaders AND a verified
//     workload on the request.
//   - JWT path is enabled when cfg.AllowDirect is true. Real vs dev-mode JWT
//     behaviour is delegated to dx-common-go/auth/jwt via cfg.JWT.Enabled
//     (real validation when true, synthetic-user injection when false).
func Middleware(cfg Config) func(http.Handler) http.Handler {
	allowSubjectHeaders := cfg.TrustSubjectHeaders
	allowDirect := cfg.AllowDirect

	if !allowSubjectHeaders && !allowDirect {
		panic("resolver.Middleware: at least one of TrustSubjectHeaders or AllowDirect must be set")
	}

	// Pre-build the JWT validator so config errors fail at startup, not
	// per request. Only build it if we'll actually use it.
	var jwtMW func(http.Handler) http.Handler
	if allowDirect {
		jwtMW = dxjwt.Middleware(cfg.JWT)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// (1) Subject-header path — only for a VERIFIED workload.
			if allowSubjectHeaders && dxheaders.Asserts(r.Header) {
				if _, verified := workload.From(r.Context()); !verified {
					// Headers naming a user, from a caller we have not
					// authenticated. Refuse rather than fall through to JWT:
					// falling through would serve the request as whoever the
					// Bearer names while silently ignoring a spoofing attempt,
					// and refusing makes the attempt visible.
					dxerrors.WriteError(w, dxerrors.NewUnauthorized(
						"subject headers require a verified workload caller"))
					return
				}
				user, err := dxheaders.Parse(r.Header)
				if err != nil {
					dxerrors.WriteError(w, dxerrors.NewUnauthorized("invalid subject headers"))
					return
				}
				ctx := auth.WithUser(r.Context(), user)
				ctx = WithOrigin(ctx, OriginGateway)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// (2) JWT fallback path.
			if !allowDirect || jwtMW == nil {
				// Either the operator disabled the direct path, or this service
				// only accepts internal calls and the request asserted no
				// subject.
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("internal subject headers required"))
				return
			}

			authz := r.Header.Get("Authorization")
			if authz == "" {
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("missing Authorization header"))
				return
			}
			if !strings.EqualFold(firstWord(authz), "bearer") {
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("Authorization header must be 'Bearer <token>'"))
				return
			}

			// Wrap the next handler so we can tag the origin after dxjwt sets the user.
			tagged := http.HandlerFunc(func(ww http.ResponseWriter, rr *http.Request) {
				ctx := WithOrigin(rr.Context(), OriginDirect)
				next.ServeHTTP(ww, rr.WithContext(ctx))
			})
			jwtMW(tagged).ServeHTTP(w, r)
		})
	}
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
