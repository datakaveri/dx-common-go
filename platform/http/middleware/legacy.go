package middleware

import (
	"net/http"

	"github.com/datakaveri/dx-common-go/auth"

	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// This file is the ONLY place the platform reads the legacy auth types. Keeping
// it separate makes the transitional surface a single file to delete in Wave 4,
// rather than a set of imports scattered through the middleware package.

func authFromCtx(r *http.Request) (auth.DxUser, bool) {
	return auth.UserFromCtx(r.Context())
}

func scopesOf(u auth.DxUser) []identity.Scope {
	if len(u.Scopes) == 0 {
		return nil
	}
	out := make([]identity.Scope, 0, len(u.Scopes))
	for _, s := range u.Scopes {
		// Expiry is carried on the legacy entry but has never been enforced
		// anywhere, so it is deliberately not copied: a field that looks like a
		// constraint and is not one is worse than its absence.
		out = append(out, identity.Scope{Name: s.Scope, EntityID: s.EntityID})
	}
	return out
}
