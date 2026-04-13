package authorization

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/datakaveri/dx-common-go/auth"
	dxerrors "github.com/datakaveri/dx-common-go/errors"
)

// ForRoles returns a middleware that allows a request only when the authenticated
// user holds at least one of the given roles. It must be placed after the JWT
// middleware which populates the DxUser in context.
func ForRoles(roles ...DxRole) func(http.Handler) http.Handler {
	required := NewRoleSet(roles...)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := auth.UserFromCtx(r.Context())
			if !ok {
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("no authenticated user in context"))
				return
			}

			userRoles := make([]DxRole, 0, len(user.Roles))
			for _, s := range user.Roles {
				userRoles = append(userRoles, DxRole(s))
			}

			if !required.HasAny(userRoles) {
				dxerrors.WriteError(w, dxerrors.NewForbidden("insufficient role: one of "+rolesToString(roles)+" required"))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ForScope returns a middleware that allows a request only when the
// authenticated user's delegation scopes contain scope for the entity
// identified by the URL parameter entityIDParam (a chi route parameter name).
//
// A wildcard scope ("*") always grants access.
func ForScope(scope DelegationScope, entityIDParam string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := auth.UserFromCtx(r.Context())
			if !ok {
				dxerrors.WriteError(w, dxerrors.NewUnauthorized("no authenticated user in context"))
				return
			}

			entityID := chi.URLParam(r, entityIDParam)

			for _, entry := range user.Scopes {
				if entry.EntityID == entityID || entry.EntityID == "*" {
					if DelegationScope(entry.Scope) == scope || DelegationScope(entry.Scope) == ScopeWildcard {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			dxerrors.WriteError(w, dxerrors.NewForbidden("delegation scope "+string(scope)+" not granted for this entity"))
		})
	}
}

// rolesToString is a helper for human-readable error messages.
func rolesToString(roles []DxRole) string {
	out := ""
	for i, r := range roles {
		if i > 0 {
			out += ", "
		}
		out += string(r)
	}
	return out
}
