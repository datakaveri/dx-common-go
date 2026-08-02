package httpx

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// requireSubject rejects a request that carries no verified caller.
//
// It exists so that "protected" is a property of the ROUTE rather than of the
// handler's request type. Relying on an embedded Actor to produce the 401 means
// a handler taking httpx.None is silently public even though the route table
// says otherwise.
func requireSubject(next http.HandlerFunc, mappers []ErrorMapper, log *zap.Logger) http.HandlerFunc {
	o := &options{mappers: mappers, log: log}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := identity.Require(r.Context()); err != nil {
			writeError(w, r, err, o)
			return
		}
		next(w, r)
	}
}

// requireRoles gates a handler on the caller holding at least one role.
//
// This is coarse, route-level entitlement only — "may this kind of caller reach
// this endpoint at all". Anything finer (does this caller own THIS resource,
// which rows may they see) is the service's decision, made against the resource
// it just loaded. Encoding resource-level rules in the route table would put
// authorization somewhere that cannot see the resource.
func requireRoles(next http.HandlerFunc, roles []string, mappers []ErrorMapper, log *zap.Logger) http.HandlerFunc {
	o := &options{mappers: mappers, log: log}
	return func(w http.ResponseWriter, r *http.Request) {
		sub, err := identity.Require(r.Context())
		if err != nil {
			writeError(w, r, err, o)
			return
		}
		if !sub.HasAnyRole(roles...) {
			// The message names neither the required role nor the held ones: a
			// 403 that enumerates what would have worked is a probing oracle.
			// The detail goes to the log instead.
			log.Info("route denied on role",
				zap.String("subject", sub.ID),
				zap.String("path", r.URL.Path),
				zap.Strings("required", roles),
				zap.Strings("held", sub.Roles))
			writeError(w, r, errors.Forbidden("you do not have access to this resource"), o)
			return
		}
		next(w, r)
	}
}
