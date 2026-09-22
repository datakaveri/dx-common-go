package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/datakaveri/dx-common-go/platform/authz/attestation"
	"github.com/datakaveri/dx-common-go/platform/authz/decision"
	dxheaders "github.com/datakaveri/dx-common-go/transport/headers"
)

type obligationsCtxKey struct{}

// ObligationsFrom returns the obligations a verified attestation carried, for a
// handler to apply (row_filter, field_policy, quota, delivery_mode). It is empty
// when the request carried no attestation, or the guard is disabled.
func ObligationsFrom(ctx context.Context) []decision.Obligation {
	obs, _ := ctx.Value(obligationsCtxKey{}).([]decision.Obligation)
	return obs
}

// AttestationConfig configures the data-plane attestation guard (invariants
// I-7 / I-8).
type AttestationConfig struct {
	// Enforcer verifies and binds the carried attestation and applies the
	// capability gate. Nil DISABLES the guard entirely (pre-Phase-6 behaviour) —
	// so a data plane adopts enforcement purely by configuration.
	Enforcer *attestation.Enforcer
	// Resource extracts the operation + resource this request targets, so the
	// attestation can be bound to it (a token for another resource is refused).
	Resource func(r *http.Request) (operationID, resourceType, resourceID string)
	// Required rejects a request that carries NO attestation. When false a
	// missing attestation passes (the route is not yet attestation-gated), but a
	// PRESENT one is always verified.
	Required bool
}

// AttestationGuard verifies a carried data-access attestation and makes its
// obligations available to the handler via ObligationsFrom. A data plane NEVER
// calls the PDP (I-8): the signed, audience-bound token IS the decision, and a
// verification failure — or a required obligation this plane cannot honour — is
// a fail-closed 403.
func AttestationGuard(cfg AttestationConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if cfg.Enforcer == nil {
			return next // disabled by configuration
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get(dxheaders.HdrAttestation)
			if token == "" {
				if cfg.Required {
					denyAttestation(w, "a data-access attestation is required for this resource")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			var opID, resType, resID string
			if cfg.Resource != nil {
				opID, resType, resID = cfg.Resource(r)
			}
			att, err := cfg.Enforcer.Enforce(token, opID, resType, resID)
			if err != nil {
				denyAttestation(w, err.Error())
				return
			}
			ctx := context.WithValue(r.Context(), obligationsCtxKey{}, att.Obligations)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func denyAttestation(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"type":   "urn:dx:authz:attestationRejected",
		"title":  "forbidden",
		"detail": detail,
	})
}
