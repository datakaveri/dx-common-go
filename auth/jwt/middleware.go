package jwt

import (
	"net/http"
	"strings"

	"github.com/datakaveri/dx-common-go/auth"
	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// Middleware returns a chi-compatible middleware that validates Bearer JWTs,
// populates the request context with a DxUser, and responds 401 on failure.
//
// When cfg.Enabled is false (local dev / testing mode) a synthetic DxUser is
// injected into the context without any actual token validation.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	if !cfg.Enabled {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mockUser := auth.DxUser{
					ID:    "00000000-0000-0000-0000-000000000000",
					Email: "dev@local",
					Name:  "Dev User",
					Roles: []string{"consumer", "provider"},
				}
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), mockUser)))
			})
		}
	}

	validator, err := New(cfg)
	if err != nil {
		// Surface config errors at middleware construction time.
		panic("jwt.Middleware: failed to initialise validator: " + err.Error())
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("missing Authorization header"))
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("Authorization header must be 'Bearer <token>'"))
				return
			}

			claims, err := validator.Validate(parts[1])
			if err != nil {
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("invalid or expired token: "+err.Error()))
				return
			}

			// Convert DelegationScopeClaims → auth.DelegationScopeEntry (plain strings).
			scopeEntries := make([]auth.DelegationScopeEntry, 0, len(claims.DelegationScopes))
			for _, ds := range claims.DelegationScopes {
				scopeEntries = append(scopeEntries, auth.DelegationScopeEntry{
					Scope:    ds.Scope,
					EntityID: ds.EntityID,
					Expiry:   ds.Expiry,
				})
			}

			sub, _ := claims.GetSubject()
			user := auth.DxUser{
				ID:               sub,
				Email:            claims.Email,
				Name:             claims.Name,
				Roles:            claims.AllRoles(),
				OrganisationID:   claims.OrganisationID,
				OrganisationName: claims.OrganisationName,
				DelegatorID:      claims.DelegatorID,
				Scopes:           scopeEntries,
			}

			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
}
