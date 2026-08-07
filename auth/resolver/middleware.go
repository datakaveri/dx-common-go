package resolver

import (
	"net/http"
	"strings"

	"github.com/datakaveri/dx-common-go/auth"
	dxjwt "github.com/datakaveri/dx-common-go/auth/jwt"
	dxerrors "github.com/datakaveri/dx-common-go/errors"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// Middleware returns a chi-compatible handler that establishes auth.DxUser
// in the request context from either HMAC-signed subject headers (preferred)
// or a Bearer JWT (fallback). See the package doc for the precedence rules.
//
// Misconfiguration is treated as a programming error and panics at handler
// construction time — both verification paths cannot be disabled simultaneously.
//
// Switches:
//   - HMAC path is enabled when cfg.Headers.Secret is non-empty.
//   - JWT path is enabled when cfg.AllowDirect is true. Real vs dev-mode JWT
//     behaviour is delegated to dx-common-go/auth/jwt via cfg.JWT.Enabled
//     (real validation when true, synthetic-user injection when false).
func Middleware(cfg Config) func(http.Handler) http.Handler {
	allowHMAC := len(cfg.Headers.Secret) > 0
	allowDirect := cfg.AllowDirect

	if !allowHMAC && !allowDirect {
		panic("resolver.Middleware: at least one of Headers.Secret or AllowDirect must be set")
	}

	// Pre-build the JWT validator so config errors fail at startup, not
	// per request. Only build it if we'll actually use it.
	var jwtMW func(http.Handler) http.Handler
	if allowDirect {
		jwtMW = dxjwt.Middleware(cfg.JWT)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// (1) HMAC path — try first whenever a signature is present.
			if allowHMAC && r.Header.Get(dxheaders.HdrSubjectSig) != "" {
				user, err := dxheaders.Verify(r.Header, cfg.Headers)
				if err != nil {
					// A signature header was sent but failed verification.
					// Do NOT fall through to JWT — that would let a caller
					// smuggle a wrong identity past the signature check.
					dxerrors.WriteError(w, dxerrors.NewUnauthorized("invalid subject signature"))
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
				// runs in HMAC-only mode and the request had no signature.
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("gateway-signed subject headers required"))
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

// RequireGatewayOrigin returns a middleware that 403s any request whose
// resolved origin is not OriginGateway. Use on per-route admin / sensitive
// paths that must not be reachable by a direct caller, even one with a
// valid JWT.
//
// MUST be installed AFTER Middleware in the chain — it reads the origin
// the resolver placed in the context.
//
// SUPERSEDED by platform/security/workload.RequireCaller, and weaker than its
// name suggests. OriginGateway means only that the request carried a valid HMAC
// signature, and every service holds the same secret — so this proves
// "somebody with the shared secret called", not "the gateway called" (review
// finding C-02, ROADMAP P0-2, ADR-06 §2.6). Switch to RequireCaller once the
// service verifies workload identity; it checks a cryptographically bound
// client id instead. Not marked Deprecated yet only because the one remaining
// consumer (dx-agent-registry-go) cannot switch until its verifier is enabled,
// and a lint failure there would block the rollout that removes it.
func RequireGatewayOrigin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin, ok := OriginFromCtx(r.Context())
			if !ok || origin != OriginGateway {
				dxerrors.WriteError(w, dxerrors.NewForbidden("this endpoint is only accessible via the gateway"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
