// Package grpc carries the platform's gRPC-facing concerns.
//
// The error mapping lives here rather than in platform/errors on purpose. When
// it sat in the L0 error taxonomy it dragged google.golang.org/grpc into the
// dependency tree of every consumer — five services with no gRPC surface at all
// failed to build on a missing go.sum entry. A transport mapping belongs with
// its transport, and services that never speak gRPC should never pay for it.
//
// Layer: L1 (capability). Imports platform/errors (L0).
package grpc

import (
	stderrors "errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// code maps the platform taxonomy onto gRPC status codes.
var code = map[errors.Code]codes.Code{
	errors.CodeValidation:         codes.InvalidArgument,
	errors.CodeUnauthorized:       codes.Unauthenticated,
	errors.CodeForbidden:          codes.PermissionDenied,
	errors.CodeNotFound:           codes.NotFound,
	errors.CodeConflict:           codes.AlreadyExists,
	errors.CodeInternal:           codes.Internal,
	errors.CodeBadGateway:         codes.Unavailable,
	errors.CodeServiceUnavailable: codes.Unavailable,
	errors.CodeTooManyRequests:    codes.ResourceExhausted,
	errors.CodeExpired:            codes.Unauthenticated,
	errors.CodeDatabase:           codes.Internal,
	errors.CodeMethodNotAllowed:   codes.Unimplemented,
}

// fromGRPC is the inverse, for translating a downstream's failure back into the
// platform taxonomy so it can be re-mapped onto whatever transport this service
// answers on.
var fromGRPC = map[codes.Code]errors.Code{
	codes.InvalidArgument:    errors.CodeValidation,
	codes.FailedPrecondition: errors.CodeValidation,
	codes.OutOfRange:         errors.CodeValidation,
	codes.Unauthenticated:    errors.CodeUnauthorized,
	codes.PermissionDenied:   errors.CodeForbidden,
	codes.NotFound:           errors.CodeNotFound,
	codes.AlreadyExists:      errors.CodeConflict,
	codes.ResourceExhausted:  errors.CodeTooManyRequests,
	codes.Unavailable:        errors.CodeServiceUnavailable,
	codes.Unimplemented:      errors.CodeMethodNotAllowed,
	codes.DeadlineExceeded:   errors.CodeServiceUnavailable,
}

// CodeOf returns the gRPC status code for err. An error carrying no platform
// classification maps to Internal — an unclassified failure is one nobody
// decided how to present, and Internal is the honest answer for that.
func CodeOf(err error) codes.Code {
	var e *errors.Error
	if stderrors.As(err, &e) {
		if c, ok := code[e.Code()]; ok {
			return c
		}
	}
	return codes.Internal
}

// Status converts err into a gRPC status suitable for returning from a server
// method.
//
// Only the classified message crosses the wire. An unclassified error yields a
// generic message and the real error is left for the caller to log — the same
// rule platform/http applies, for the same reason: an unclassified error string
// is exactly the kind that carries a DSN, a query, or a file path.
func Status(err error) error {
	if err == nil {
		return nil
	}
	var e *errors.Error
	if stderrors.As(err, &e) {
		return status.Error(CodeOf(err), e.Message())
	}
	return status.Error(codes.Internal, "internal error")
}

// FromStatus translates a gRPC error returned by a downstream call back into
// the platform taxonomy, so a caller can propagate it without hand-writing a
// translation table per client. A 404 from a downstream then surfaces as
// errors.NotFound and renders as a 404 to this service's own caller.
//
// A non-status error is returned unchanged: it is a local failure (a dial
// error, a context cancellation), not a remote verdict.
func FromStatus(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	c, ok := fromGRPC[st.Code()]
	if !ok {
		c = errors.CodeInternal
	}
	return errors.Wrap(err, c, st.Message())
}
