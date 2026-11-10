package logging

import (
	"context"
	stderrors "errors"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	dxerrors "github.com/datakaveri/dx-common-go/platform/errors"
)

// ErrorFields returns the fields that describe err on a log line: the error
// itself plus, when they are in its chain, typed facts a reader filters on —
//
//	dependency  which upstream failed (platform/errors.Dependency)
//	target      where it was reached
//	grpc_code   the gRPC status code, e.g. Unavailable, NotFound
//	timeout     true when the cause is a context deadline
//
// Use it instead of zap.Error(err) wherever the error may have come from
// another service, so "dx-user is down" is `dependency=dx-user
// grpc_code=Unavailable` and not a substring of a long gRPC message.
func ErrorFields(err error) []zap.Field {
	if err == nil {
		return nil
	}
	fields := []zap.Field{zap.Error(err)}
	var dep *dxerrors.DependencyError
	if stderrors.As(err, &dep) {
		fields = append(fields, zap.String("dependency", dep.Name))
		if dep.Target != "" {
			fields = append(fields, zap.String("target", dep.Target))
		}
	}
	if code, ok := GRPCCode(err); ok {
		fields = append(fields, zap.String("grpc_code", code.String()))
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		fields = append(fields, zap.Bool("timeout", true))
	}
	return fields
}

// GRPCCode reports the gRPC status code anywhere in err's chain. It looks
// through fmt.Errorf("%w") wrapping, which status.Code alone does not.
func GRPCCode(err error) (codes.Code, bool) {
	var se interface{ GRPCStatus() *status.Status }
	if stderrors.As(err, &se) {
		if s := se.GRPCStatus(); s != nil {
			return s.Code(), true
		}
	}
	return codes.OK, false
}

// IsDependencyFailure reports whether err is another service being down or
// slow — something this service handled and that clears by itself, not a bug
// in it. Such failures are logged at WARN; ERROR is for what a person has to
// fix.
//
// A dependency that ANSWERED with a non-transient gRPC code (PermissionDenied,
// InvalidArgument, Internal …) is not one: that is a misconfiguration or a bug
// on one side, and stays ERROR.
func IsDependencyFailure(err error) bool {
	if stderrors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if code, ok := GRPCCode(err); ok {
		switch code {
		case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
			return true
		}
		return false
	}
	var dep *dxerrors.DependencyError
	return stderrors.As(err, &dep)
}
