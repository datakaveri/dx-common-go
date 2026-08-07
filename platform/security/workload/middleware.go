package workload

import (
	"net/http"

	dxerrors "github.com/datakaveri/dx-common-go/errors"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

// Middleware establishes the calling workload and constrains what it may claim.
//
// It must run BEFORE subject resolution. The decision "may this caller speak
// for a user at all" gates whether the X-Subject-* headers can be trusted, so
// resolving the subject first would mean trusting them to decide whether to
// trust them.
//
// A nil Verifier is the Disabled case and passes everything through unchanged,
// which is what keeps this additive for the fleet.
func Middleware(v *Verifier) func(http.Handler) http.Handler {
	if v == nil {
		return passthrough
	}
	mode := v.enforcement

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearerToken(r.Header.Get(HdrWorkload))

			if raw == "" {
				if mode == Required {
					record(resultMissing, "", mode)
					dxerrors.WriteError(w, dxerrors.NewUnauthorized("workload credential required"))
					return
				}
				// Permissive: the legacy HMAC path carries this request.
				record(resultLegacy, "", mode)
				next.ServeHTTP(w, r)
				return
			}

			p, err := v.Verify(raw)
			if err != nil {
				// A credential that is PRESENT and invalid is fatal in EVERY
				// mode, Permissive included. Falling through to the legacy path
				// here would let a caller downgrade out of a failed check by
				// sending a deliberately broken token.
				record(resultRejected, "", mode)
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("invalid workload credential"))
				return
			}

			if assertsSubject(r.Header) && !v.MayAssertSubject(p) {
				record(resultSubjectDenied, p.ID, mode)
				dxerrors.WriteError(w, dxerrors.NewForbidden("this workload may not assert an end-user subject"))
				return
			}

			record(resultVerified, p.ID, mode)
			next.ServeHTTP(w, r.WithContext(With(r.Context(), p)))
		})
	}
}

// RequireCaller rejects any request whose verified workload is not one of ids.
//
// This is the replacement for auth/resolver.RequireGatewayOrigin, which could
// only ever prove that SOMEBODY holding the shared secret called. Here the
// identity is cryptographic and specific.
//
// It fails closed when no workload was verified, so it is only correct on a
// service whose Middleware runs with enforcement enabled. Installing it while
// enforcement is Disabled rejects every request — that is deliberate: a route
// guard that silently allows everything because a control is switched off is
// how the defect this replaces survived review.
func RequireCaller(ids ...string) func(http.Handler) http.Handler {
	allowed := set(ids)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := From(r.Context())
			if !ok {
				dxerrors.WriteError(w, dxerrors.NewForbidden("this endpoint requires a verified workload caller"))
				return
			}
			if _, permitted := allowed[p.ID]; !permitted {
				dxerrors.WriteError(w, dxerrors.NewForbidden("workload "+p.ID+" may not call this endpoint"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// assertsSubject reports whether the request claims to speak for an end user.
//
// It keys off the subject id rather than the signature, because the question is
// "is this request carrying a user identity", not "is that identity signed" —
// under Required the HMAC signature is gone but the subject headers remain.
func assertsSubject(h http.Header) bool {
	return h.Get(dxheaders.HdrSubjectID) != ""
}
