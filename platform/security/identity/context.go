package identity

import (
	"context"
	"errors"
)

// ErrNoSubject is returned by Require when the context carries no verified
// caller.
//
// It is a plain sentinel rather than a platform error because identity is L0
// and platform/errors is a sibling, not a dependency. platform/http maps it to
// a 401 — see its error-mapper chain.
var ErrNoSubject = errors.New("identity: no verified subject on the request")

// ctxKey is unexported so no other package can write a Subject into the context
// under this key. That matters: everything downstream treats a context Subject
// as already verified, so the only writers must be the middleware that actually
// performed verification.
type ctxKey struct{}

// With returns a context carrying s.
//
// Call this ONLY after verifying the caller. Putting an unverified Subject on a
// context is indistinguishable downstream from a verified one, and that is
// exactly how an authentication bypass gets built by accident.
func With(ctx context.Context, s Subject) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From returns the Subject on ctx, if any.
func From(ctx context.Context) (Subject, bool) {
	s, ok := ctx.Value(ctxKey{}).(Subject)
	return s, ok
}

// Require returns the Subject on ctx, or ErrNoSubject.
//
// It returns an error rather than a bool so a caller can propagate it directly
// instead of hand-writing a 401 — which is the boilerplate this replaces, ~220
// times across the fleet:
//
//	user, ok := auth.UserFromCtx(ctx)
//	if !ok {
//	    dxerrors.WriteGinError(c, dxerrors.NewUnauthorized("authentication required"))
//	    return
//	}
func Require(ctx context.Context) (Subject, error) {
	s, ok := From(ctx)
	if !ok {
		return Subject{}, ErrNoSubject
	}
	// A Subject with no ID is not a subject. Treating it as one would let a
	// misconfigured verifier produce an "authenticated" request with an empty
	// principal, which authorization would then evaluate against nothing.
	if s.ID == "" {
		return Subject{}, ErrNoSubject
	}
	return s, nil
}

// MustFrom returns the Subject on ctx or panics.
//
// Only for code paths that run strictly behind authentication middleware, where
// a missing subject is a programming error rather than a client error. Prefer
// Require everywhere else.
func MustFrom(ctx context.Context) Subject {
	s, err := Require(ctx)
	if err != nil {
		panic(err)
	}
	return s
}
