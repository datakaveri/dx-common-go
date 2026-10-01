// Package httpx is a thin gin-ergonomics veneer over dx-common-go's existing
// validation/response/errors/auth primitives. It exists so that resource
// handlers stop repeating the same bind → extract-UUID → extract-user →
// translate-error boilerplate; it invents nothing new underneath.
//
// A resource handler embeds *BaseHandler for its own service's URN-scoped
// responses, writes error-returning methods of type Handler, and registers
// them with Handle:
//
//	type DelegationHandler struct {
//	    *httpx.BaseHandler
//	    svc *delegation.Service
//	}
//
//	func (h *DelegationHandler) Renew(c *gin.Context) error {
//	    req, err := httpx.Bind[RenewDelegationRequest](c)
//	    if err != nil {
//	        return err
//	    }
//	    id, err := httpx.PathUUID(c, "id")
//	    if err != nil {
//	        return err
//	    }
//	    userID, err := httpx.UserID(c)
//	    if err != nil {
//	        return err
//	    }
//	    d, err := h.svc.Renew(c.Request.Context(), id, userID, req.ExpiryAt)
//	    if err != nil {
//	        return err
//	    }
//	    return h.OK(c, d)
//	}
//
//	r.POST("/:id/renew", httpx.Handle(h.Renew))
//
// Handlers never write error responses themselves — returning an error is
// the only error path, and Handle centralizes translation. See ErrorMapper
// for how a specific route customizes that translation (e.g. ownership
// mismatch -> 404 instead of 403) without leaking HTTP concerns into the
// service layer.
package httpx
