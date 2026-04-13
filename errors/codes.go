package errors

// ErrorCode is a short string identifying a category of error.
type ErrorCode string

const (
	ErrValidation      ErrorCode = "ERR_VALIDATION"
	ErrUnauthorized    ErrorCode = "ERR_UNAUTHORIZED"
	ErrForbidden       ErrorCode = "ERR_FORBIDDEN"
	ErrNotFound        ErrorCode = "ERR_NOT_FOUND"
	ErrConflict        ErrorCode = "ERR_CONFLICT"
	ErrInternal        ErrorCode = "ERR_INTERNAL"
	ErrBadGateway      ErrorCode = "ERR_BAD_GATEWAY"
	ErrTooManyRequests ErrorCode = "ERR_TOO_MANY_REQUESTS"
	ErrExpired         ErrorCode = "ERR_EXPIRED"
	ErrDatabase        ErrorCode = "ERR_DATABASE"
)
