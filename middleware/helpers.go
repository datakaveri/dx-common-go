package middleware

import (
	"context"

	"github.com/datakaveri/dx-common-go/auth"
)

// GetUserFromCtx retrieves the user from request context
func GetUserFromCtx(ctx context.Context) *auth.DxUser {
	user := auth.UserFromCtx(ctx)
	return &user
}

// GetRequestIDFromCtx retrieves the request ID from context
func GetRequestIDFromCtx(ctx context.Context) string {
	return RequestIDFromCtx(ctx)
}
