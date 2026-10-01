package httpx

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/datakaveri/dx-common-go/auth"
	dxerrors "github.com/datakaveri/dx-common-go/errors"
	"github.com/datakaveri/dx-common-go/validation"
)

// Bind decodes the JSON request body into T. It wraps
// validation.ValidateRawRequest — a decode failure becomes a 400
// dxerrors.NewValidation. Business-rule validation stays on T's own
// Validate() method, called by the service, not here.
func Bind[T any](c *gin.Context) (T, error) {
	req, dxErr := validation.ValidateRawRequest[T](c.Request)
	if dxErr != nil {
		return req, dxErr
	}
	return req, nil
}

// PathUUID parses the named path parameter as a UUID. Returns
// dxerrors.NewValidation if it is missing or malformed.
func PathUUID(c *gin.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		return uuid.Nil, dxerrors.NewValidation("invalid " + name + ": must be a UUID")
	}
	return id, nil
}

// QueryUUID parses the named query parameter as a UUID. An absent parameter
// is not an error — it returns (uuid.Nil, nil) so callers can treat it as
// optional; a present-but-malformed value still fails validation.
func QueryUUID(c *gin.Context, name string) (uuid.UUID, error) {
	raw := c.Query(name)
	if raw == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, dxerrors.NewValidation("invalid " + name + ": must be a UUID")
	}
	return id, nil
}

// UserID extracts the resolved caller's id as a UUID from context (set by
// auth/resolver.Middleware). Returns dxerrors.NewUnauthorized when no user
// is present or its subject id is not a UUID.
func UserID(c *gin.Context) (uuid.UUID, error) {
	u, ok := auth.UserFromCtx(c.Request.Context())
	if !ok {
		return uuid.Nil, dxerrors.NewUnauthorized("authentication required")
	}
	id, err := uuid.Parse(u.ID)
	if err != nil {
		return uuid.Nil, dxerrors.NewUnauthorized("invalid subject id")
	}
	return id, nil
}
