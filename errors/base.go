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
	Details() []string
}

// BaseDxError is the concrete implementation of DxError.
type BaseDxError struct {
	code    ErrorCode
	message string
	details []string
}

// httpStatusMap maps ErrorCode to HTTP status codes.
var httpStatusMap = map[ErrorCode]int{
	ErrValidation:      http.StatusBadRequest,
	ErrUnauthorized:    http.StatusUnauthorized,
	ErrForbidden:       http.StatusForbidden,
	ErrNotFound:        http.StatusNotFound,
	ErrConflict:        http.StatusConflict,
	ErrInternal:        http.StatusInternalServerError,
	ErrBadGateway:      http.StatusBadGateway,
	ErrTooManyRequests: http.StatusTooManyRequests,
	ErrExpired:         http.StatusUnauthorized,
	ErrDatabase:        http.StatusInternalServerError,
}

// urnMap maps ErrorCode to IUDX/CDPG problem type URNs.
var urnMap = map[ErrorCode]string{
	ErrValidation:      "urn:dx:as:InvalidParamValue",
	ErrUnauthorized:    "urn:dx:as:Unauthorized",
	ErrForbidden:       "urn:dx:as:Forbidden",
	ErrNotFound:        "urn:dx:rs:ResourceNotFound",
	ErrConflict:        "urn:dx:as:ResourceAlreadyExists",
	ErrInternal:        "urn:dx:as:InternalServerError",
	ErrBadGateway:      "urn:dx:as:BadGateway",
	ErrTooManyRequests: "urn:dx:as:RateLimitExceeded",
	ErrExpired:         "urn:dx:as:TokenExpired",
	ErrDatabase:        "urn:dx:as:DatabaseError",
}

// Error implements the error interface.
func (e *BaseDxError) Error() string {
	return fmt.Sprintf("[%s] %s", e.code, e.message)
}

// Code returns the ErrorCode.
func (e *BaseDxError) Code() ErrorCode { return e.code }

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

// Details returns the slice of additional detail strings.
func (e *BaseDxError) Details() []string { return e.details }
