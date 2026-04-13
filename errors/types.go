package errors

// NewValidation creates a 400 Bad Request error for input validation failures.
func NewValidation(message string, details ...string) DxError {
	return &BaseDxError{code: ErrValidation, message: message, details: details}
}

// NewUnauthorized creates a 401 Unauthorized error.
func NewUnauthorized(message string, details ...string) DxError {
	return &BaseDxError{code: ErrUnauthorized, message: message, details: details}
}

// NewForbidden creates a 403 Forbidden error.
func NewForbidden(message string, details ...string) DxError {
	return &BaseDxError{code: ErrForbidden, message: message, details: details}
}

// NewNotFound creates a 404 Not Found error.
func NewNotFound(message string, details ...string) DxError {
	return &BaseDxError{code: ErrNotFound, message: message, details: details}
}

// NewConflict creates a 409 Conflict error.
func NewConflict(message string, details ...string) DxError {
	return &BaseDxError{code: ErrConflict, message: message, details: details}
}

// NewInternal creates a 500 Internal Server Error.
func NewInternal(message string, details ...string) DxError {
	return &BaseDxError{code: ErrInternal, message: message, details: details}
}

// NewDatabase creates a 500 database error.
func NewDatabase(message string, details ...string) DxError {
	return &BaseDxError{code: ErrDatabase, message: message, details: details}
}

// NewExpired creates a 401 token-expired error.
func NewExpired(message string, details ...string) DxError {
	return &BaseDxError{code: ErrExpired, message: message, details: details}
}

// NewBadGateway creates a 502 Bad Gateway error.
func NewBadGateway(message string, details ...string) DxError {
	return &BaseDxError{code: ErrBadGateway, message: message, details: details}
}

// NewTooManyRequests creates a 429 Too Many Requests error.
func NewTooManyRequests(message string, details ...string) DxError {
	return &BaseDxError{code: ErrTooManyRequests, message: message, details: details}
}
