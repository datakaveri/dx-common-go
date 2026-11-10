package sql

import (
	"sort"
	"strings"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/filter"
	"github.com/datakaveri/dx-common-go/platform/paging"
)

// This file is the SQL backend of the list-query pipeline: it maps the
// storage-neutral filter.Request (validated API input, keyed by filter.Key)
// onto typed Column predicates. It is the ONLY place a filter key becomes a
// column, so it is the ONLY place client input could reach an identifier — and
// Column being a distinct type means that step is explicit and greppable.
//
// The inbound representation is shared across backends; this compiler is not. An
// Elasticsearch or NGSI-LD endpoint writes its own adapter over the same
// filter.Request.

// FilterMode is how a mapped column turns a field's values into a predicate.
type FilterMode int

const (
	// MatchEqualAny is exact match: one value → `col = v`, several → `col = ANY($n)`.
	MatchEqualAny FilterMode = iota
	// MatchEqual is exact match that requires exactly one value.
	MatchEqual
	// MatchTime is a temporal predicate (BETWEEN / `>` / `<`).
	MatchTime
	// MatchPrefix is a case-insensitive prefix match (`col ILIKE 'v%'`), literal
	// characters in the value escaped so `%`/`_` cannot smuggle a wildcard.
	MatchPrefix
	// MatchContains is a case-insensitive substring match (`col ILIKE '%v%'`).
	MatchContains
)

// FilterField maps one storage-neutral key onto a column and a match mode.
type FilterField struct {
	Column Column
	Mode   FilterMode
}

// ListSchema is a repository's backend mapping for a list operation: how each
// filter key compiles, which sort keys are allowed and their columns, and the
// default order plus a unique tie-breaker for stable pagination.
//
// It lives in the repository adapter — never in a handler or service — because
// only the persistence layer knows what a column is. Verify it against the
// operation's filter.Spec at wiring time so a missing or mistyped mapping fails
// startup, not a request.
type ListSchema struct {
	Fields map[filter.Key]FilterField
	Sort   Sortable
	// DefaultOrder is applied when the client requests no sort. A paginated query
	// with no order is a correctness bug (rows repeat across pages), so this
	// should be non-empty for any paged operation.
	DefaultOrder []Order
	// TieBreak is a unique column appended to every resolved order so rows that
	// share the leading sort value keep a stable order across pages. Leave the
	// zero value to omit it (only safe when DefaultOrder is already unique).
	TieBreak Order
}

// Compile turns a validated filter.Request and the parsed sort keys into WHERE
// predicates and ORDER BY terms. The predicates carry only bound values; only
// schema-owned Columns reach an identifier position.
//
// A filter key with no mapping is an Internal error, not a client error: it
// means the spec and schema drifted, which Verify catches at startup.
func (s ListSchema) Compile(fr filter.Request, sortKeys []paging.SortKey) ([]Pred, []Order, error) {
	preds := make([]Pred, 0, len(fr.Exact())+len(fr.Times()))

	// Exact filters, in sorted key order so the generated SQL and its argument
	// list are deterministic (a map range is not).
	exact := fr.Exact()
	for _, k := range sortedKeys(exact) {
		ff, ok := s.Fields[k]
		if !ok {
			return nil, nil, errors.Internal("sql: list schema has no mapping for filter key " + string(k))
		}
		p, err := ff.compileExact(exact[k])
		if err != nil {
			return nil, nil, err
		}
		preds = append(preds, p)
	}

	for _, tf := range fr.Times() {
		ff, ok := s.Fields[tf.Key]
		if !ok {
			return nil, nil, errors.Internal("sql: list schema has no mapping for temporal key " + string(tf.Key))
		}
		if ff.Mode != MatchTime {
			return nil, nil, errors.Internal("sql: temporal key " + string(tf.Key) + " is not mapped as MatchTime")
		}
		preds = append(preds, timePred(ff.Column, tf))
	}

	orders, err := s.orders(sortKeys)
	if err != nil {
		return nil, nil, err
	}
	return preds, orders, nil
}

func (ff FilterField) compileExact(vals []string) (Pred, error) {
	switch ff.Mode {
	case MatchEqualAny:
		if len(vals) == 1 {
			return Eq(ff.Column, vals[0]), nil
		}
		return In(ff.Column, vals), nil
	case MatchEqual:
		if len(vals) != 1 {
			return Pred{}, errors.Validation("this filter accepts a single value")
		}
		return Eq(ff.Column, vals[0]), nil
	case MatchPrefix:
		return ILike(ff.Column, escapeLike(vals[0])+"%"), nil
	case MatchContains:
		return ILike(ff.Column, "%"+escapeLike(vals[0])+"%"), nil
	case MatchTime:
		return Pred{}, errors.Internal("sql: MatchTime key used as an exact filter")
	default:
		return Pred{}, errors.Internal("sql: unknown filter mode")
	}
}

func timePred(c Column, tf filter.TimeFilter) Pred {
	switch tf.Rel {
	case filter.Between:
		return Between(c, tf.From, tf.To)
	case filter.After:
		return Gt(c, tf.From)
	case filter.Before:
		return Lt(c, tf.From)
	default:
		return Pred{}
	}
}

// orders resolves the client's sort keys through the allowlist, falls back to
// the default when none were asked for, and appends the tie-breaker.
func (s ListSchema) orders(keys []paging.SortKey) ([]Order, error) {
	resolved, err := s.Sort.Orders(keys)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		resolved = append(resolved, s.DefaultOrder...)
	}
	return s.withTieBreak(resolved), nil
}

// withTieBreak appends the unique tie-breaker unless the sort already ends on
// it (or contains it), so the total order is stable without double-sorting.
func (s ListSchema) withTieBreak(orders []Order) []Order {
	if s.TieBreak.Column == "" {
		return orders
	}
	for _, o := range orders {
		if o.Column == s.TieBreak.Column {
			return orders
		}
	}
	return append(orders, s.TieBreak)
}

// Verify proves, at wiring time, that this schema can compile every field the
// spec declares: every exact key is mapped, every temporal key is mapped as
// MatchTime, and a default order exists. Call it once when constructing a
// repository (or in a test) so a drift between spec and schema stops startup
// rather than surfacing as a 500 on a request.
func (s ListSchema) Verify(spec filter.Spec) error {
	for _, k := range spec.Keys() {
		ff, ok := s.Fields[k]
		if !ok {
			return errors.Internal("sql: list schema is missing a mapping for filter key " + string(k))
		}
		if ff.Mode == MatchTime {
			return errors.Internal("sql: exact filter key " + string(k) + " is mapped as MatchTime")
		}
	}
	for _, k := range spec.TimeKeys() {
		ff, ok := s.Fields[k]
		if !ok {
			return errors.Internal("sql: list schema is missing a mapping for temporal key " + string(k))
		}
		if ff.Mode != MatchTime {
			return errors.Internal("sql: temporal key " + string(k) + " must be mapped as MatchTime")
		}
	}
	if len(s.DefaultOrder) == 0 && s.TieBreak.Column == "" {
		return errors.Internal("sql: list schema has no default order or tie-break; a paginated query needs a total order")
	}
	return nil
}

func sortedKeys(m map[filter.Key][]string) []filter.Key {
	out := make([]filter.Key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// escapeLike escapes the LIKE/ILIKE metacharacters so a value is matched
// literally: a client's `%` or `_` cannot turn a prefix search into a wildcard
// scan. The backslash is escaped first, being the escape character itself.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
