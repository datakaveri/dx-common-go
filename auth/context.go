package auth

import "context"

type contextKey string

const userContextKey contextKey = "dx_user"

// WithUser stores a DxUser in the context and returns the derived context.
func WithUser(ctx context.Context, user DxUser) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

// UserFromCtx retrieves the DxUser from ctx. The second return value is false
// when no user has been stored.
func UserFromCtx(ctx context.Context) (DxUser, bool) {
	u, ok := ctx.Value(userContextKey).(DxUser)
	return u, ok
}
