package errors

import "fmt"

// DependencyError marks a failure as coming from another service — dx-user-go,
// the ACL, a payment gateway — rather than from this one.
//
// It changes nothing for the client: it is not classified, so on its own it
// still renders as a 500, and wrapped in ServiceUnavailable(...).WithCause it
// renders as that 503. What it adds is the log line: logging.ErrorFields
// reports dependency= and target= as their own fields, so "which upstream is
// down" is a filter on a field instead of a regexp over a gRPC error string.
type DependencyError struct {
	// Name is the dependency, e.g. "dx-user" or "razorpay".
	Name string
	// Target is where it was reached, e.g. "dx-user-go:9090". Optional.
	Target string
	Err    error
}

// Dependency wraps err as a failure of the named dependency. Wrapping nil
// returns nil, so a call site can wrap unconditionally.
func Dependency(name, target string, err error) error {
	if err == nil {
		return nil
	}
	return &DependencyError{Name: name, Target: target, Err: err}
}

func (e *DependencyError) Error() string { return fmt.Sprintf("%s: %v", e.Name, e.Err) }

// Unwrap keeps the cause — a gRPC status, a context deadline — reachable
// through errors.Is and errors.As.
func (e *DependencyError) Unwrap() error { return e.Err }
