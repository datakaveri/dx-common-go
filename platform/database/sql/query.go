package sql

import (
	"strconv"
	"strings"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/paging"
)

// Column is a SQL identifier.
//
// It is a DISTINCT TYPE, not a string, and that is the point. A plain string —
// notably a user-supplied sort key or filter name — cannot reach an identifier
// position without an explicit, greppable conversion. This turns the standing
// rule "identifiers must come from code or an allowlist, never from user input"
// from a doc comment into something the compiler helps enforce.
//
// Values are always bound as parameters and never interpolated, so only
// identifiers need this treatment.
type Column string

// Order is a resolved sort term: a validated column plus a direction.
type Order struct {
	Column Column
	Desc   bool
}

// Asc and Desc build an Order.
func Asc(c Column) Order  { return Order{Column: c} }
func Desc(c Column) Order { return Order{Column: c, Desc: true} }

// Sortable maps API-visible sort keys onto columns.
//
// It is an enforcement point, not a convention: a key with no entry is REJECTED
// with a validation error rather than reaching the SQL builder. That is what
// makes `?sort=` safe to accept from a client at all.
type Sortable map[string]Column

// Orders resolves parsed sort keys against the allowlist.
func (s Sortable) Orders(keys []paging.SortKey) ([]Order, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([]Order, 0, len(keys))
	for _, k := range keys {
		col, ok := s[k.Field]
		if !ok {
			return nil, errors.Validation("cannot sort by " + strconv.Quote(k.Field))
		}
		out = append(out, Order{Column: col, Desc: k.Dir == paging.Desc})
	}
	return out, nil
}

// Default returns orders for a fallback sort, for when the client asked for
// none. An unordered paginated query is a correctness bug: without ORDER BY,
// Postgres may return the same row on two different pages and omit another.
func (s Sortable) Default(orders ...Order) []Order { return orders }

// ── predicates ─────────────────────────────────────────────────────────────

type op string

const (
	opEq      op = "="
	opNe      op = "<>"
	opGt      op = ">"
	opGte     op = ">="
	opLt      op = "<"
	opLte     op = "<="
	opLike    op = "LIKE"
	opILike   op = "ILIKE"
	opIn      op = "IN"
	opNotIn   op = "NOT IN"
	opNull    op = "IS NULL"
	opNotNull op = "IS NOT NULL"
	opBetween op = "BETWEEN"
	opAnd     op = "AND"
	opOr      op = "OR"
	opNot     op = "NOT"
	opRaw     op = "RAW"
)

// Pred is a WHERE predicate. Build one with Eq, In, And and friends; the zero
// value renders nothing.
type Pred struct {
	op       op
	col      Column
	values   []any
	children []Pred
	raw      string
}

// IsZero reports whether the predicate contributes nothing, so a caller can
// build predicates conditionally without a nil check at every site.
func (p Pred) IsZero() bool { return p.op == "" }

func Eq(c Column, v any) Pred  { return Pred{op: opEq, col: c, values: []any{v}} }
func Ne(c Column, v any) Pred  { return Pred{op: opNe, col: c, values: []any{v}} }
func Gt(c Column, v any) Pred  { return Pred{op: opGt, col: c, values: []any{v}} }
func Gte(c Column, v any) Pred { return Pred{op: opGte, col: c, values: []any{v}} }
func Lt(c Column, v any) Pred  { return Pred{op: opLt, col: c, values: []any{v}} }
func Lte(c Column, v any) Pred { return Pred{op: opLte, col: c, values: []any{v}} }

// Like and ILike take the pattern verbatim, wildcards included. The caller
// decides whether a search is a prefix, a suffix or a contains — wrapping it
// in %…% here would make an indexed prefix search impossible to express.
func Like(c Column, pattern string) Pred  { return Pred{op: opLike, col: c, values: []any{pattern}} }
func ILike(c Column, pattern string) Pred { return Pred{op: opILike, col: c, values: []any{pattern}} }

// In renders `col = ANY($n)` rather than an expanded `IN ($1,$2,…)`. One
// parameter regardless of length keeps the statement shape stable, so Postgres
// can reuse the prepared plan instead of planning afresh for every distinct
// list length.
func In[T any](c Column, vs []T) Pred {
	return Pred{op: opIn, col: c, values: []any{vs}}
}

// NotIn is the negation of In.
//
// Note the SQL semantics it inherits: if the list contains NULL, NOT IN matches
// nothing at all. Filter NULLs out before calling it.
func NotIn[T any](c Column, vs []T) Pred {
	return Pred{op: opNotIn, col: c, values: []any{vs}}
}

func Between(c Column, lo, hi any) Pred { return Pred{op: opBetween, col: c, values: []any{lo, hi}} }
func Null(c Column) Pred                { return Pred{op: opNull, col: c} }
func NotNull(c Column) Pred             { return Pred{op: opNotNull, col: c} }

// And combines predicates conjunctively, skipping zero values so a caller can
// build a filter list conditionally.
func And(ps ...Pred) Pred { return group(opAnd, ps) }

// Or combines predicates disjunctively.
func Or(ps ...Pred) Pred { return group(opOr, ps) }

// Not negates a predicate.
func Not(p Pred) Pred {
	if p.IsZero() {
		return Pred{}
	}
	return Pred{op: opNot, children: []Pred{p}}
}

func group(o op, ps []Pred) Pred {
	kept := make([]Pred, 0, len(ps))
	for _, p := range ps {
		if !p.IsZero() {
			kept = append(kept, p)
		}
	}
	switch len(kept) {
	case 0:
		return Pred{}
	case 1:
		return kept[0]
	}
	return Pred{op: o, children: kept}
}

// Raw is the predicate escape hatch, for what the DSL deliberately cannot
// express: JSONB containment, PostGIS operators, array overlap.
//
// The fragment is emitted verbatim, so it MUST NOT contain user input — write
// `metadata @> ?` and pass the value, never fmt.Sprintf the value in. Values
// are still bound as parameters; use ? as the placeholder and the builder
// numbers it.
//
// A LITERAL question mark — PostgreSQL's JSONB key-exists operators ?, ?| and
// ?& — is written DOUBLED, so it is not mistaken for a placeholder: `tags ?? ?`
// renders `tags ? $n` (does the array contain the bound key), and `tags ??| ?`
// renders `tags ?| $n`. Without this the operator's ? was consumed as a
// placeholder and the query silently changed shape (ROADMAP P2-2).
func Raw(fragment string, values ...any) Pred {
	return Pred{op: opRaw, raw: fragment, values: values}
}

// ── rendering ──────────────────────────────────────────────────────────────

// render appends the predicate's SQL to b, binding values from *n onward.
func (p Pred) render(b *strings.Builder, args *[]any) {
	switch p.op {
	case "":
		return
	case opAnd, opOr:
		b.WriteByte('(')
		for i, c := range p.children {
			if i > 0 {
				b.WriteString(" " + string(p.op) + " ")
			}
			c.render(b, args)
		}
		b.WriteByte(')')
	case opNot:
		b.WriteString("NOT (")
		p.children[0].render(b, args)
		b.WriteByte(')')
	case opNull, opNotNull:
		b.WriteString(quoteIdent(p.col))
		b.WriteByte(' ')
		b.WriteString(string(p.op))
	case opBetween:
		b.WriteString(quoteIdent(p.col))
		b.WriteString(" BETWEEN " + placeholder(args, p.values[0]))
		b.WriteString(" AND " + placeholder(args, p.values[1]))
	case opIn:
		b.WriteString(quoteIdent(p.col) + " = ANY(" + placeholder(args, p.values[0]) + ")")
	case opNotIn:
		b.WriteString("NOT (" + quoteIdent(p.col) + " = ANY(" + placeholder(args, p.values[0]) + "))")
	case opRaw:
		b.WriteString(numberPlaceholders(p.raw, args, p.values))
	default:
		b.WriteString(quoteIdent(p.col) + " " + string(p.op) + " " + placeholder(args, p.values[0]))
	}
}

func placeholder(args *[]any, v any) string {
	*args = append(*args, v)
	return "$" + strconv.Itoa(len(*args))
}

// numberPlaceholders replaces each ? placeholder in a raw fragment with the
// next $n. A DOUBLED ?? is an escaped literal question mark — the JSONB
// key-exists operators ?, ?|, ?& — emitted as a single ? and never numbered, so
// `tags ?? ?` binds one value against the JSONB operator rather than two
// (ROADMAP P2-2). A ? with no value left is passed through unchanged, as before.
func numberPlaceholders(fragment string, args *[]any, values []any) string {
	var b strings.Builder
	vi := 0
	for i := 0; i < len(fragment); i++ {
		if fragment[i] == '?' {
			if i+1 < len(fragment) && fragment[i+1] == '?' {
				b.WriteByte('?') // escaped literal ? (a JSONB operator), collapse ?? -> ?
				i++
				continue
			}
			if vi < len(values) {
				b.WriteString(placeholder(args, values[vi]))
				vi++
				continue
			}
		}
		b.WriteByte(fragment[i])
	}
	return b.String()
}

// quoteIdent double-quotes an identifier, escaping embedded quotes.
//
// Column is a distinct type precisely so a hostile string cannot arrive here,
// but quoting costs nothing and makes a column that collides with a reserved
// word (e.g. "order", "user") work rather than fail confusingly.
func quoteIdent(c Column) string {
	s := string(c)
	// A qualified name (table.column) must be quoted per part, or the dot ends
	// up inside the identifier.
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return quoteOne(s[:i]) + "." + quoteOne(s[i+1:])
	}
	return quoteOne(s)
}

func quoteOne(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// Where renders predicates as a WHERE body (without the keyword), numbering
// placeholders from start+1. Exported so a hand-written statement can splice in
// allowlisted filters instead of rebuilding them.
func Where(preds []Pred, start int) (string, []any) {
	args := make([]any, start)
	p := And(preds...)
	if p.IsZero() {
		return "", nil
	}
	var b strings.Builder
	p.render(&b, &args)
	return b.String(), args[start:]
}

func orderBy(orders []Order) string {
	if len(orders) == 0 {
		return ""
	}
	parts := make([]string, 0, len(orders))
	for _, o := range orders {
		dir := " ASC"
		if o.Desc {
			dir = " DESC"
		}
		parts = append(parts, quoteIdent(o.Column)+dir)
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}
