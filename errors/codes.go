package errors

import perrors "github.com/datakaveri/dx-common-go/platform/errors"

// ErrorCode is a short string identifying a category of error.
//
// Deprecated: use platform/errors.Code.
//
// This is a type ALIAS, not a copy. That matters for the migration: it makes
// platform/errors.Error satisfy this package's DxError interface without any
// adapter, so the ~20 fleet call sites doing
//
//	if de, ok := err.(dxerrors.DxError); ok { ... }
//
// keep compiling and behaving identically while they move over wave by wave.
type ErrorCode = perrors.Code

// Deprecated: use the platform/errors.Code* constants.
//
// These are bound to the platform values rather than redeclared, so the two
// sets cannot drift apart. A test in platform/errors pins the string values,
// which appear in logs and must not change silently.
const (
	ErrValidation         = perrors.CodeValidation
	ErrUnauthorized       = perrors.CodeUnauthorized
	ErrForbidden          = perrors.CodeForbidden
	ErrNotFound           = perrors.CodeNotFound
	ErrConflict           = perrors.CodeConflict
	ErrInternal           = perrors.CodeInternal
	ErrBadGateway         = perrors.CodeBadGateway
	ErrServiceUnavailable = perrors.CodeServiceUnavailable
	ErrTooManyRequests    = perrors.CodeTooManyRequests
	ErrExpired            = perrors.CodeExpired
	ErrDatabase           = perrors.CodeDatabase
	ErrMethodNotAllowed   = perrors.CodeMethodNotAllowed
)
