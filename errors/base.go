package errors

import (
	"fmt"
	"net/http"
)

// DxError is the interface that all domain errors implement.
type DxError interface {
	error
	Code() ErrorCode
	HTTPStatus() int
	URN() string
	Title() string
	Details() []string
	Message() string
}

// BaseDxError is the concrete implementation of DxError.
type BaseDxError struct {
	code    ErrorCode
	message string
	details []string
	cause   error
}

// Unwrap returns the underlying cause, enabling errors.Is/errors.As traversal.
func (e *BaseDxError) Unwrap() error { return e.cause }

// httpStatusMap maps ErrorCode to HTTP status codes.
var httpStatusMap = map[ErrorCode]int{
	ErrValidation:         http.StatusBadRequest,
	ErrUnauthorized:       http.StatusUnauthorized,
	ErrForbidden:          http.StatusForbidden,
	ErrNotFound:           http.StatusNotFound,
	ErrConflict:           http.StatusConflict,
	ErrInternal:           http.StatusInternalServerError,
	ErrBadGateway:         http.StatusBadGateway,
	ErrServiceUnavailable: http.StatusServiceUnavailable,
	ErrTooManyRequests:    http.StatusTooManyRequests,
	ErrExpired:            http.StatusUnauthorized,
	ErrDatabase:           http.StatusInternalServerError,
	ErrMethodNotAllowed:   http.StatusMethodNotAllowed,
}

// urnMap maps ErrorCode to IUDX/CDPG problem type URNs.
var urnMap = map[ErrorCode]string{
	ErrValidation:         "urn:dx:as:InvalidParamValue",
	ErrUnauthorized:       "urn:dx:as:Unauthorized",
	ErrForbidden:          "urn:dx:as:Forbidden",
	ErrNotFound:           "urn:dx:rs:ResourceNotFound",
	ErrConflict:           "urn:dx:as:ResourceAlreadyExists",
	ErrInternal:           "urn:dx:as:InternalServerError",
	ErrBadGateway:         "urn:dx:as:BadGateway",
	ErrServiceUnavailable: "urn:dx:as:ServiceUnavailable",
	ErrTooManyRequests:    "urn:dx:as:RateLimitExceeded",
	ErrExpired:            "urn:dx:as:TokenExpired",
	ErrDatabase:           "urn:dx:as:DatabaseError",
	ErrMethodNotAllowed:   "urn:dx:as:MethodNotAllowed",
}

// titleMap maps ErrorCode to human-readable HTTP status titles matching the
// Java controlplane convention (e.g., "Bad Request" instead of "ERR_VALIDATION").
var titleMap = map[ErrorCode]string{
	ErrValidation:         "Bad Request",
	ErrUnauthorized:       "Not Authorized",
	ErrForbidden:          "Forbidden",
	ErrNotFound:           "Not Found",
	ErrConflict:           "Conflict",
	ErrInternal:           "Internal Server Error",
	ErrBadGateway:         "Bad Gateway",
	ErrServiceUnavailable: "Service Unavailable",
	ErrTooManyRequests:    "Too Many Requests",
	ErrExpired:            "Not Authorized",
	ErrDatabase:           "Internal Server Error",
	ErrMethodNotAllowed:   "Method Not Allowed",
}

// Error implements the error interface.
func (e *BaseDxError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.code, e.message, e.cause)
	}
	return fmt.Sprintf("[%s] %s", e.code, e.message)
}

// WithCause returns a copy of err carrying cause, for errors.Is/errors.As and
// for the log line. The cause is NEVER sent to the client — the response
// carries only Message — so a service returns
//
//	errors.WithCause(errors.NewServiceUnavailable("could not grant the role; try again"), err)
//
// and the platform logs the failure once, cause included, instead of the
// service logging it and the platform logging it again.
//
// An err that is not a *BaseDxError is returned unchanged.
func WithCause(err DxError, cause error) DxError {
	e, ok := err.(*BaseDxError)
	if !ok || cause == nil {
		return err
	}
	out := *e
	out.cause = cause
	return &out
}

// Code returns the ErrorCode.
func (e *BaseDxError) Code() ErrorCode { return e.code }

// Message returns the human-readable message without the code prefix.
func (e *BaseDxError) Message() string { return e.message }

// HTTPStatus returns the HTTP status code associated with this error.
func (e *BaseDxError) HTTPStatus() int {
	if status, ok := httpStatusMap[e.code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// URN returns the problem type URN for this error.
func (e *BaseDxError) URN() string {
	if urn, ok := urnMap[e.code]; ok {
		return urn
	}
	return "urn:dx:as:InternalServerError"
}

// Title returns the human-readable HTTP status title for this error (e.g.,
// "Bad Request", "Not Found"). Matches the Java controlplane convention.
func (e *BaseDxError) Title() string {
	if t, ok := titleMap[e.code]; ok {
		return t
	}
	return "Internal Server Error"
}

// Details returns the slice of additional detail strings.
func (e *BaseDxError) Details() []string { return e.details }
