// Package filter is the platform's storage-neutral inbound filter contract for
// list endpoints.
//
// It sits between transport (platform/http, platform/paging) and persistence
// (platform/database/sql). A per-operation Spec declares ONCE which API filter
// parameters an endpoint accepts, their cardinality, and their value rules;
// Spec.Parse then validates one request's query values against it and returns a
// Request keyed by opaque, storage-neutral Keys. The package knows nothing of
// HTTP, SQL, Elasticsearch, or any service domain: a backend compiler
// (platform/database/sql.ListSchema) maps Keys onto columns and predicates.
//
// This is the middle of the list-query pipeline described in the query-param
// standardisation plan. Its whole job is to make "unknown or malformed input
// must never widen a result set" a construction-time and parse-time guarantee
// rather than a convention every handler re-implements (and one, dx-user-go's
// organisation-request list, got wrong — see the plan).
//
// Layer: L0 (kernel). Imports only platform/errors.
package filter

import (
	"fmt"
	"sort"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// Default bounds. An operation may lower a field's limit; raising it should be
// backed by query-cost evidence. The point is amplification control: seven
// entityType values are normal, thousands are an attack on the database.
const (
	// DefaultMaxValues bounds how many values one repeated filter may carry.
	DefaultMaxValues = 25
	// DefaultMaxLen bounds a single value's byte length.
	DefaultMaxLen = 512
	// MaxTotalValues bounds the sum of values across every filter in one request.
	MaxTotalValues = 100
)

// Key is a storage-neutral field identifier.
//
// It is an OPAQUE token an operation assigns to a logical field — deliberately
// neither the client-facing API name nor a database column. Handlers and
// services pass Keys around; only a backend compiler resolves a Key to a column.
// This is what keeps a client string away from a SQL identifier position and a
// DB column name out of the transport contract.
type Key string

// kind classifies how a field's values are validated and later compiled.
type kind int

const (
	kindStringSet kind = iota // exact match; repeated values are a set (OR within, IN in SQL)
	kindText                  // a single free-text value (prefix/contains/similarity — the backend decides)
)

// Field is one declared, validated API filter parameter.
//
// Construct it with StringSet or Text plus options; the zero value is not
// usable. Fields are immutable once inside a Spec.
type Field struct {
	api       string
	key       Key
	kind      kind
	maxValues int
	maxLen    int
	allowed   map[string]struct{} // nil means any value is accepted
}

// Opt configures a Field.
type Opt func(*Field)

// StringSet declares an exact-match filter that accepts repeated values.
//
// One value compiles to `col = v`; several to `col = ANY($n)`. Values are a set:
// order is not significant and duplicates are collapsed.
func StringSet(api string, key Key, opts ...Opt) Field {
	f := Field{api: api, key: key, kind: kindStringSet, maxValues: DefaultMaxValues, maxLen: DefaultMaxLen}
	for _, o := range opts {
		o(&f)
	}
	return f
}

// Text declares a single-value free-text filter (the backend chooses exact,
// prefix, contains, or similarity). A repeated key is rejected rather than
// silently collapsed, because two text terms for one field is not a coherent
// request.
func Text(api string, key Key, opts ...Opt) Field {
	f := Field{api: api, key: key, kind: kindText, maxValues: 1, maxLen: DefaultMaxLen}
	for _, o := range opts {
		o(&f)
	}
	return f
}

// MaxValues lowers (or raises, with cost evidence) the per-field value cap.
func MaxValues(n int) Opt { return func(f *Field) { f.maxValues = n } }

// MaxLength sets the per-value byte cap.
func MaxLength(n int) Opt { return func(f *Field) { f.maxLen = n } }

// Allowed restricts a field to an enum. A value outside the set is a 400, never
// a silently dropped filter — dropping it would widen the result set.
func Allowed(vals ...string) Opt {
	return func(f *Field) {
		f.allowed = make(map[string]struct{}, len(vals))
		for _, v := range vals {
			f.allowed[v] = struct{}{}
		}
	}
}

// Time declares a temporal filter on one field.
//
// A default time field is queried through the canonical bare parameters
// time/endTime/timerel. A named time field (for an endpoint with more than one
// temporal column) is queried through <api>_time/<api>_endTime/<api>_timerel —
// always the API name, never a DB column.
type Time struct {
	api   string
	key   Key
	named bool
}

// DefaultTime enables the bare time/endTime/timerel query on a field.
func DefaultTime(api string, key Key) Time { return Time{api: api, key: key} }

// NamedTime enables a field-prefixed temporal query, for endpoints with several
// temporal fields.
func NamedTime(api string, key Key) Time { return Time{api: api, key: key, named: true} }

// params returns this time field's three query parameter names.
func (t Time) params() (timeName, endName, relName string) {
	if t.named {
		return t.api + "_time", t.api + "_endTime", t.api + "_timerel"
	}
	return "time", "endTime", "timerel"
}

// Spec is an operation's immutable filter contract.
//
// Build it once during service wiring (New or MustNew) and reuse it for every
// request; it is safe for concurrent use because Parse never mutates it.
type Spec struct {
	fields map[string]Field // by API name
	times  []Time
	names  []string // precomputed allowed query-parameter names, sorted
}

// New freezes a filter contract, validating it. It fails when two fields share
// an API name or Key, when a field is malformed, or when more than one default
// time field is declared — all of which are programming errors that should stop
// startup rather than surface as a confused 400 later.
func New(fields []Field, times ...Time) (Spec, error) {
	s := Spec{fields: make(map[string]Field, len(fields))}
	keys := make(map[Key]struct{}, len(fields))
	for _, f := range fields {
		if f.api == "" || f.key == "" {
			return Spec{}, fmt.Errorf("filter: a field has an empty api name or key")
		}
		if _, dup := s.fields[f.api]; dup {
			return Spec{}, fmt.Errorf("filter: duplicate api field %q", f.api)
		}
		if _, dup := keys[f.key]; dup {
			return Spec{}, fmt.Errorf("filter: duplicate key %q", f.key)
		}
		s.fields[f.api] = f
		keys[f.key] = struct{}{}
	}
	defaults := 0
	for _, t := range times {
		if t.api == "" || t.key == "" {
			return Spec{}, fmt.Errorf("filter: a time field has an empty api name or key")
		}
		if !t.named {
			defaults++
		}
	}
	if defaults > 1 {
		return Spec{}, fmt.Errorf("filter: at most one default time field is allowed (got %d)", defaults)
	}
	s.times = append([]Time(nil), times...)
	s.names = computeNames(s)
	return s, nil
}

// MustNew is New that panics on an invalid contract, for package-level operation
// specs where a malformed contract is a build-time mistake — analogous to
// regexp.MustCompile.
func MustNew(fields []Field, times ...Time) Spec {
	s, err := New(fields, times...)
	if err != nil {
		panic(err)
	}
	return s
}

// Names lists every query parameter this spec accepts, so the transport layer
// can reject anything else. It does NOT include the paging names (page/size/
// sort/cursor); those are the paging package's contract and are unioned in by
// the HTTP binder.
func (s Spec) Names() []string { return s.names }

// Keys lists the storage-neutral keys of the exact-match fields, so a backend
// compiler can prove at startup that it maps all of them.
func (s Spec) Keys() []Key {
	out := make([]Key, 0, len(s.fields))
	for _, f := range s.fields {
		out = append(out, f.key)
	}
	return out
}

// TimeKeys lists the keys of the temporal fields.
func (s Spec) TimeKeys() []Key {
	out := make([]Key, 0, len(s.times))
	for _, t := range s.times {
		out = append(out, t.key)
	}
	return out
}

func computeNames(s Spec) []string {
	set := make(map[string]struct{}, len(s.fields)+len(s.times)*3)
	for api := range s.fields {
		set[api] = struct{}{}
	}
	for _, t := range s.times {
		tn, en, rn := t.params()
		set[tn], set[en], set[rn] = struct{}{}, struct{}{}, struct{}{}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// validate cleans and checks one field's repeated raw values, returning the
// de-duplicated, order-preserved set or a validation error.
func (f Field) validate(vals []string) ([]string, error) {
	if f.kind == kindText && len(vals) > 1 {
		return nil, errors.Validation(f.api + " accepts a single value")
	}
	// Bound work before doing any, so a hostile value count cannot amplify.
	if len(vals) > f.maxValues {
		return nil, errors.Validation(fmt.Sprintf("%s accepts at most %d values", f.api, f.maxValues))
	}
	out := make([]string, 0, len(vals))
	seen := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		if v == "" {
			// An empty value is a client mistake, not "match anything": ?f= must be
			// rejected, or the length-1 case silently becomes `col = ''`.
			return nil, errors.Validation(f.api + " has an empty value; omit the parameter to not filter on it")
		}
		if len(v) > f.maxLen {
			return nil, errors.Validation(fmt.Sprintf("%s value exceeds %d bytes", f.api, f.maxLen))
		}
		if f.allowed != nil {
			if _, ok := f.allowed[v]; !ok {
				return nil, errors.Validation(f.api + " has an unsupported value: " + v)
			}
		}
		if _, dup := seen[v]; dup {
			continue // de-duplicate, preserving first appearance
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out, nil
}
