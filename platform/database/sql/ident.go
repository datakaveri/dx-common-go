package sql

import (
	"fmt"
	"regexp"
)

// identPattern matches a safe SQL identifier: a leading letter or underscore
// then letters, digits or underscores, optionally schema-qualified with one
// dot. It is deliberately stricter than PostgreSQL allows — no quoting, no
// spaces, no Unicode — because the only identifiers that reach it are
// code-owned table names, and a name outside this set is a programming error,
// not a value to escape.
var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// ValidIdent reports whether s is a safe, code-owned SQL identifier (an
// optionally schema-qualified table or column name).
//
// A table name cannot be a bind parameter, so any API that interpolates one —
// the outbox, lease and idempotency stores — must be handed a name from a
// compile-time constant, never from input. This is the check that makes "must
// be a constant" enforceable rather than a comment.
func ValidIdent(s string) bool { return identPattern.MatchString(s) }

// Ident is a validated SQL identifier. Constructing one is the sanctioned way
// to pass a table name into an interpolating API: the value cannot exist unless
// it passed ValidIdent, so a holder need not re-check.
type Ident string

// NewIdent validates s and returns it as an Ident, or an error if it is not a
// safe identifier.
func NewIdent(s string) (Ident, error) {
	if !ValidIdent(s) {
		return "", fmt.Errorf("sql: %q is not a valid identifier", s)
	}
	return Ident(s), nil
}

// MustIdent is NewIdent for a compile-time constant: it panics on an invalid
// identifier. Use it at construction with a literal — the panic then surfaces a
// programming error at boot, never on a request path with attacker input.
func MustIdent(s string) Ident {
	id, err := NewIdent(s)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw identifier.
func (i Ident) String() string { return string(i) }
