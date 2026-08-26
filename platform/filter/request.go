package filter

import "time"

// Rel is a temporal relation.
//
// during is normalised to Between on the way in, so the compiler only ever sees
// these three. Between is inclusive; After and Before are exclusive (`>` / `<`).
type Rel int

const (
	relUnset Rel = iota
	// Between is an inclusive range: time <= col <= endTime.
	Between
	// After is exclusive: col > time.
	After
	// Before is exclusive: col < time.
	Before
)

// TimeFilter is a validated temporal predicate over one storage-neutral key.
// From is the lower bound (or the single bound for After/Before); To is the
// upper bound, set only for Between. Both are already parsed to time.Time so a
// raw client string never reaches the database.
type TimeFilter struct {
	Key  Key
	Rel  Rel
	From time.Time
	To   time.Time
}

// Request is the parsed, validated result of Spec.Parse.
//
// exact holds the exact-match filters keyed by storage-neutral Key, each value
// slice already de-duplicated and non-empty; times holds the temporal filters.
// A backend compiler reads them through Exact and Times.
type Request struct {
	exact map[Key][]string
	times []TimeFilter
}

// Exact returns the exact-match filters keyed by storage-neutral Key.
//
// The map is the Request's own — treat it as read-only. Each value slice is
// non-empty and de-duplicated; a single value compiles to `=`, several to `IN`.
func (r Request) Exact() map[Key][]string { return r.exact }

// Times returns the temporal filters.
func (r Request) Times() []TimeFilter { return r.times }

// Empty reports whether the request carries no filters at all.
func (r Request) Empty() bool { return len(r.exact) == 0 && len(r.times) == 0 }

// Has reports whether an exact-match filter was supplied for a key. A service
// uses it to apply an operation default only when the client did not filter on
// that field (e.g. an admin listing that defaults to pending).
func (r Request) Has(key Key) bool {
	_, ok := r.exact[key]
	return ok
}
