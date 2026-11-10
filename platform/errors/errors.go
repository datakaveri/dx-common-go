// Package errors is the platform's error taxonomy.
//
// It carries the classification a caller needs — what went wrong, whether
// retrying could help, and how the failure maps onto a transport status — and
// deliberately carries nothing about how a failure is RENDERED. The predecessor
// package mixed the two: errors.WriteGinError put *gin.Context inside the error
// taxonomy, which is why two competing error-write paths ended up coexisting in
// the same handler file, and why 22 near-identical local `fail`/`writeReqErr`/
// `asDxError` helpers grew across the fleet to paper over the difference.
// Rendering belongs to platform/http.Problem.
//
// The type is concrete. Its predecessor exposed a seven-method DxError
// interface with exactly one implementation, which bought no substitutability
// and made errors.As awkward. Here the sentinel values compare by CODE, so the
// idiomatic forms work directly:
//
//	errors.Is(err, errors.ErrNotFound)   // any not-found, whatever its message
//	var e *errors.Error; errors.As(err, &e)
//
// Layer: L0 (kernel). Imports stdlib plus google.golang.org/grpc/codes, which
// is a leaf enum package and does not pull in the gRPC runtime.
package errors

import (
	"errors"
	"fmt"
)

// Code is the machine-readable classification of a failure.
//
// It is deliberately separate from the human-readable message so that message
// text can be localised or reworded without any caller's behaviour changing —
// callers branch on Code, never on message text.
type Code string

// The taxonomy. These string values are load-bearing: they appear in logs and
// in the legacy ErrorCode type that aliases this one, so they are not free to
// rename.
const (
	CodeValidation         Code = "ERR_VALIDATION"
	CodeUnauthorized       Code = "ERR_UNAUTHORIZED"
	CodeForbidden          Code = "ERR_FORBIDDEN"
	CodeNotFound           Code = "ERR_NOT_FOUND"
	CodeConflict           Code = "ERR_CONFLICT"
	CodeInternal           Code = "ERR_INTERNAL"
	CodeBadGateway         Code = "ERR_BAD_GATEWAY"
	CodeServiceUnavailable Code = "ERR_SERVICE_UNAVAILABLE"
	CodeTooManyRequests    Code = "ERR_TOO_MANY_REQUESTS"
	CodeExpired            Code = "ERR_EXPIRED"
	CodeDatabase           Code = "ERR_DATABASE"
	CodeMethodNotAllowed   Code = "ERR_METHOD_NOT_ALLOWED"
)

// Error is a classified platform error.
//
// Fields are unexported: an Error is built through a constructor so that code
// and message can never disagree, and is read through methods so that adding a
// facet later (a locale key, a retry hint) does not break construction sites.
type Error struct {
	code    Code
	message string
	details []string
	cause   error
}

// Sentinels for errors.Is. Each matches ANY Error carrying the same code,
// regardless of message — so a handler asks "is this a not-found?" without
// caring which layer produced it or how it was worded.
//
//	if errors.Is(err, errors.ErrNotFound) { ... }
var (
	ErrValidation         = &Error{code: CodeValidation}
	ErrUnauthorized       = &Error{code: CodeUnauthorized}
	ErrForbidden          = &Error{code: CodeForbidden}
	ErrNotFound           = &Error{code: CodeNotFound}
	ErrConflict           = &Error{code: CodeConflict}
	ErrInternal           = &Error{code: CodeInternal}
	ErrBadGateway         = &Error{code: CodeBadGateway}
	ErrServiceUnavailable = &Error{code: CodeServiceUnavailable}
	ErrTooManyRequests    = &Error{code: CodeTooManyRequests}
	ErrExpired            = &Error{code: CodeExpired}
	ErrDatabase           = &Error{code: CodeDatabase}
	ErrMethodNotAllowed   = &Error{code: CodeMethodNotAllowed}
)

// Error implements the error interface.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.code, e.message, e.cause)
	}
	return fmt.Sprintf("[%s] %s", e.code, e.message)
}

// Unwrap exposes the wrapped cause to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.cause }

// Is reports whether target is an Error with the same code. This is what makes
// the package sentinels work with errors.Is while still allowing each error to
// carry its own message.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.code == e.code
}

// Code returns the classification.
func (e *Error) Code() Code { return e.code }

// Message is the human-readable summary, without the code prefix that Error()
// adds. It is client-safe by construction: a platform Error is only built where
// the message is known to be safe to return, which is the property that lets
// platform/http render it directly and turn everything else into a generic 500.
func (e *Error) Message() string { return e.message }

// Details are additional client-safe strings, typically per-field validation
// failures.
func (e *Error) Details() []string { return e.details }

// WithDetails returns a copy carrying additional detail strings. It copies
// rather than mutating so that a shared sentinel can never be modified in place.
func (e *Error) WithDetails(details ...string) *Error {
	out := *e
	out.details = append(append([]string(nil), e.details...), details...)
	return &out
}

// WithCause returns a copy wrapping cause. The cause is available to errors.Is
// and errors.As but is NEVER rendered to a client — platform/http logs it and
// sends only Message.
func (e *Error) WithCause(cause error) *Error {
	out := *e
	out.cause = cause
	return &out
}

// ── constructors ───────────────────────────────────────────────────────────

func New(code Code, message string, details ...string) *Error {
	return &Error{code: code, message: message, details: details}
}

func Validation(message string, details ...string) *Error {
	return New(CodeValidation, message, details...)
}
func Unauthorized(message string, details ...string) *Error {
	return New(CodeUnauthorized, message, details...)
}
func Forbidden(message string, details ...string) *Error {
	return New(CodeForbidden, message, details...)
}
func NotFound(message string, details ...string) *Error {
	return New(CodeNotFound, message, details...)
}
func Conflict(message string, details ...string) *Error {
	return New(CodeConflict, message, details...)
}
func Internal(message string, details ...string) *Error {
	return New(CodeInternal, message, details...)
}
func BadGateway(message string, details ...string) *Error {
	return New(CodeBadGateway, message, details...)
}
func ServiceUnavailable(message string, details ...string) *Error {
	return New(CodeServiceUnavailable, message, details...)
}
func TooManyRequests(message string, details ...string) *Error {
	return New(CodeTooManyRequests, message, details...)
}
func Expired(message string, details ...string) *Error {
	return New(CodeExpired, message, details...)
}
func Database(message string, details ...string) *Error {
	return New(CodeDatabase, message, details...)
}
func MethodNotAllowed(message string, details ...string) *Error {
	return New(CodeMethodNotAllowed, message, details...)
}

// Wrap classifies an arbitrary error, keeping it reachable through errors.Is
// and errors.As while presenting message to the client.
//
// Wrapping nil returns nil, so a call site can wrap unconditionally:
//
//	return errors.Wrap(err, errors.CodeDatabase, "could not load policy")
func Wrap(err error, code Code, message string) *Error {
	if err == nil {
		return nil
	}
	return &Error{code: code, message: message, cause: err}
}

// ── classification helpers ─────────────────────────────────────────────────

// CodeOf reports the code of the first Error in err's chain, or CodeInternal
// for an unclassified error. An unclassified error is by definition one nobody
// decided how to present, and 500 is the honest answer for that.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.code
	}
	return CodeInternal
}

// Message returns the client-safe message of the first Error in err's chain.
// It returns "" for an unclassified error — deliberately, so a caller cannot
// accidentally leak an internal error string by defaulting to err.Error().
func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.message
	}
	return ""
}

// Details returns the detail strings of the first Error in err's chain.
func Details(err error) []string {
	var e *Error
	if errors.As(err, &e) {
		return e.details
	}
	return nil
}

// Classified reports whether err carries a platform classification. platform/http
// uses this to decide between rendering the error and emitting a generic 500.
func Classified(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Convenience predicates, one per commonly-branched code.
func IsValidation(err error) bool   { return errors.Is(err, ErrValidation) }
func IsUnauthorized(err error) bool { return errors.Is(err, ErrUnauthorized) }
func IsForbidden(err error) bool    { return errors.Is(err, ErrForbidden) }
func IsNotFound(err error) bool     { return errors.Is(err, ErrNotFound) }
func IsConflict(err error) bool     { return errors.Is(err, ErrConflict) }
func IsInternal(err error) bool     { return errors.Is(err, ErrInternal) }
