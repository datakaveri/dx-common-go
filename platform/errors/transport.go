package errors

import (
	stderrors "errors"
	"net/http"
)

// This file maps the taxonomy onto HTTP. It imports net/http for STATUS
// CONSTANTS ONLY — http.ResponseWriter must never appear in this package, which
// is the boundary that keeps rendering in platform/http and stops the error
// taxonomy from acquiring a web framework the way its predecessor did.
//
// The gRPC mapping deliberately lives in platform/grpc (L1) rather than here.
// Putting it in the L0 kernel forced google.golang.org/grpc into the dependency
// tree of every consumer — including five services with no gRPC surface at all,
// whose builds broke on a missing go.sum entry the moment it was added. A
// transport mapping belongs with the transport.

// httpStatus maps a code to an HTTP status.
var httpStatus = map[Code]int{
	CodeValidation:         http.StatusBadRequest,
	CodeUnauthorized:       http.StatusUnauthorized,
	CodeForbidden:          http.StatusForbidden,
	CodeNotFound:           http.StatusNotFound,
	CodeConflict:           http.StatusConflict,
	CodeInternal:           http.StatusInternalServerError,
	CodeBadGateway:         http.StatusBadGateway,
	CodeServiceUnavailable: http.StatusServiceUnavailable,
	CodeTooManyRequests:    http.StatusTooManyRequests,
	CodeExpired:            http.StatusUnauthorized,
	CodeDatabase:           http.StatusInternalServerError,
	CodeMethodNotAllowed:   http.StatusMethodNotAllowed,
}

// urn maps a code to its problem-type URN.
//
// These are NOT namespaced per service, and that is the established contract:
// an error URN keeps the legacy urn:dx:as: / urn:dx:rs: namespaces regardless of
// which service produced it. Only SUCCESS URNs carry a service prefix, which
// platform/http supplies via URNSpace. Documented in
// claude-docs/CLIENT-CONTRACT-CHANGES.md §1.4.
var urn = map[Code]string{
	CodeValidation:         "urn:dx:as:InvalidParamValue",
	CodeUnauthorized:       "urn:dx:as:Unauthorized",
	CodeForbidden:          "urn:dx:as:Forbidden",
	CodeNotFound:           "urn:dx:rs:ResourceNotFound",
	CodeConflict:           "urn:dx:as:ResourceAlreadyExists",
	CodeInternal:           "urn:dx:as:InternalServerError",
	CodeBadGateway:         "urn:dx:as:BadGateway",
	CodeServiceUnavailable: "urn:dx:as:ServiceUnavailable",
	CodeTooManyRequests:    "urn:dx:as:RateLimitExceeded",
	CodeExpired:            "urn:dx:as:TokenExpired",
	CodeDatabase:           "urn:dx:as:DatabaseError",
	CodeMethodNotAllowed:   "urn:dx:as:MethodNotAllowed",
}

// title maps a code to the human-readable status title clients display. These
// match the Java control-plane wording, so a client migrating from the Java
// stack sees no change.
var title = map[Code]string{
	CodeValidation:         "Bad Request",
	CodeUnauthorized:       "Not Authorized",
	CodeForbidden:          "Forbidden",
	CodeNotFound:           "Not Found",
	CodeConflict:           "Conflict",
	CodeInternal:           "Internal Server Error",
	CodeBadGateway:         "Bad Gateway",
	CodeServiceUnavailable: "Service Unavailable",
	CodeTooManyRequests:    "Too Many Requests",
	CodeExpired:            "Not Authorized",
	CodeDatabase:           "Internal Server Error",
	CodeMethodNotAllowed:   "Method Not Allowed",
}

// retryable lists the codes where retrying the SAME request could plausibly
// succeed without the caller changing anything.
//
// Note what is absent. CodeInternal is not retryable: an unclassified server
// fault is as likely to be a deterministic bug as a blip, and retrying it turns
// one error into a storm. CodeDatabase IS retryable because the platform only
// assigns it to driver-level failures — serialization conflicts, deadlocks and
// connection loss — which are exactly the transient class.
var retryable = map[Code]bool{
	CodeBadGateway:         true,
	CodeServiceUnavailable: true,
	CodeTooManyRequests:    true,
	CodeDatabase:           true,
}

// HTTPStatus returns the HTTP status for this error.
func (e *Error) HTTPStatus() int {
	if s, ok := httpStatus[e.code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// URN returns the problem-type URN for this error.
func (e *Error) URN() string {
	if u, ok := urn[e.code]; ok {
		return u
	}
	return "urn:dx:as:InternalServerError"
}

// Title returns the human-readable status title for this error.
func (e *Error) Title() string {
	if t, ok := title[e.code]; ok {
		return t
	}
	return "Internal Server Error"
}

// Retryable reports whether retrying the same request could plausibly succeed.
func (e *Error) Retryable() bool { return retryable[e.code] }

// ── package-level forms, for an unclassified error ─────────────────────────

// HTTPStatusOf returns the HTTP status for any error, defaulting to 500 for one
// that carries no platform classification.
func HTTPStatusOf(err error) int {
	var e *Error
	if stderrors.As(err, &e) {
		return e.HTTPStatus()
	}
	return http.StatusInternalServerError
}

// URNOf returns the problem-type URN for any error.
func URNOf(err error) string {
	var e *Error
	if stderrors.As(err, &e) {
		return e.URN()
	}
	return "urn:dx:as:InternalServerError"
}

// TitleOf returns the status title for any error.
func TitleOf(err error) string {
	var e *Error
	if stderrors.As(err, &e) {
		return e.Title()
	}
	return "Internal Server Error"
}

// IsRetryable reports whether err is worth retrying. An unclassified error is
// NOT retryable — see the note on the retryable map.
func IsRetryable(err error) bool {
	var e *Error
	if stderrors.As(err, &e) {
		return e.Retryable()
	}
	return false
}
