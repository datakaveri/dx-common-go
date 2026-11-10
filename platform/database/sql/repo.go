package sql

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/paging"
)

// Collect scans every row into a slice of T, matching columns to `db:"…"` tags.
//
// It reuses pgx's reflection scanner rather than reimplementing one, reaching
// the native rows through an unexported interface so pgx stays out of every
// exported signature.
func Collect[T any](rows Rows, err error) ([]T, error) {
	if err != nil {
		return nil, MapError(err)
	}
	defer rows.Close()

	n, ok := rows.(interface{ native() pgx.Rows })
	if !ok {
		return nil, errors.Internal("sql: rows implementation does not support struct scanning")
	}
	out, err := pgx.CollectRows(n.native(), pgx.RowToStructByNameLax[T])
	if err != nil {
		return nil, MapError(err)
	}
	return out, nil
}

// One scans exactly one row into T, returning a not-found error when there is
// none.
func One[T any](rows Rows, err error) (T, error) {
	var zero T
	items, err := Collect[T](rows, err)
	if err != nil {
		return zero, err
	}
	if len(items) == 0 {
		return zero, errors.NotFound("not found")
	}
	return items[0], nil
}

// ── Repo ───────────────────────────────────────────────────────────────────

// Repo is the generic persistence surface: the mechanics of the old
// dao.BaseDAO plus transaction propagation, with NO .Pool()/.DAO() bypass.
//
// Every method binds to the transaction on ctx when one is present, so a
// repository method composes into a Manager.Do block without knowing it.
type Repo[T any] struct {
	db      DB
	table   string
	id      Column
	columns []Column

	softDelete Column
	deleted    any
	active     any
}

// RepoOption configures a Repo.
type RepoOption[T any] func(*Repo[T])

// WithTable sets the table name.
func WithTable[T any](name string) RepoOption[T] {
	return func(r *Repo[T]) { r.table = name }
}

// WithID sets the primary-key column (default "id").
func WithID[T any](c Column) RepoOption[T] { return func(r *Repo[T]) { r.id = c } }

// WithSoftDelete enables soft deletion on a column.
//
// Once set, every read EXCLUDES deleted rows unless Unscoped() is called. The
// current fleet rule is "filter soft-deleted rows explicitly", which means
// every query is one forgotten predicate away from returning deleted data.
func WithSoftDelete[T any](c Column, deleted, active any) RepoOption[T] {
	return func(r *Repo[T]) { r.softDelete, r.deleted, r.active = c, deleted, active }
}

// NewRepo builds a repository over T.
//
// Columns are derived from T's `db:"…"` tags, so the projection and the struct
// cannot drift apart.
func NewRepo[T any](db DB, opts ...RepoOption[T]) *Repo[T] {
	r := &Repo[T]{db: db, id: "id"}
	for _, o := range opts {
		o(r)
	}
	r.columns = columnsOf[T]()
	return r
}

// querier returns the ambient transaction when there is one, else the pool.
// This single indirection is what makes every repository method
// transaction-aware without the caller passing a tx around.
func (r *Repo[T]) querier(ctx context.Context) Querier {
	if tx, ok := TxFrom(ctx); ok {
		return tx
	}
	return r.db
}

// Table is the repository's table name.
func (r *Repo[T]) Table() string { return r.table }

// Get loads one row by primary key.
func (r *Repo[T]) Get(ctx context.Context, id any) (T, error) {
	return r.Where(Eq(r.id, id)).One(ctx)
}

// Where starts a query.
func (r *Repo[T]) Where(preds ...Pred) *Query[T] {
	return &Query[T]{repo: r, preds: preds}
}

// All starts an unfiltered query.
func (r *Repo[T]) All() *Query[T] { return &Query[T]{repo: r} }

// Unscoped returns a view that includes soft-deleted rows.
func (r *Repo[T]) Unscoped() *Repo[T] {
	c := *r
	c.softDelete = ""
	return &c
}

// Insert writes values and returns the stored row, so a caller sees
// database-generated columns (identity, defaults, triggers) without a re-read.
func (r *Repo[T]) Insert(ctx context.Context, values map[Column]any) (T, error) {
	var zero T
	if len(values) == 0 {
		return zero, errors.Validation("nothing to insert")
	}
	cols, args := splitMap(values)

	var b strings.Builder
	b.WriteString("INSERT INTO " + quoteOne(r.table) + " (")
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(") VALUES (")
	for i := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("$" + strconv.Itoa(i+1))
	}
	b.WriteString(") RETURNING " + r.projection())

	return One[T](r.querier(ctx).Query(ctx, b.String(), args...))
}

// Update applies values to rows matching preds and returns them.
//
// It REQUIRES at least one predicate. An unbounded UPDATE is almost never what
// anyone means, and the one time it is, it is worth spelling out — so this
// refuses rather than silently rewriting the table.
func (r *Repo[T]) Update(ctx context.Context, values map[Column]any, preds ...Pred) ([]T, error) {
	if len(values) == 0 {
		return nil, errors.Validation("nothing to update")
	}
	if And(preds...).IsZero() {
		return nil, errors.Internal("sql: refusing an unbounded UPDATE — pass a predicate")
	}
	cols, args := splitMap(values)

	var b strings.Builder
	b.WriteString("UPDATE " + quoteOne(r.table) + " SET ")
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c) + " = $" + strconv.Itoa(i+1))
	}
	where, wargs := Where(r.scoped(preds), len(args))
	if where != "" {
		b.WriteString(" WHERE " + where)
		args = append(args, wargs...)
	}
	b.WriteString(" RETURNING " + r.projection())

	return Collect[T](r.querier(ctx).Query(ctx, b.String(), args...))
}

// UpdateByID applies values to one row and returns it.
func (r *Repo[T]) UpdateByID(ctx context.Context, id any, values map[Column]any) (T, error) {
	var zero T
	out, err := r.Update(ctx, values, Eq(r.id, id))
	if err != nil {
		return zero, err
	}
	if len(out) == 0 {
		return zero, errors.NotFound("not found")
	}
	return out[0], nil
}

// Delete removes rows matching preds, soft-deleting when configured. It
// refuses an unbounded delete for the same reason Update does.
func (r *Repo[T]) Delete(ctx context.Context, preds ...Pred) (int64, error) {
	if And(preds...).IsZero() {
		return 0, errors.Internal("sql: refusing an unbounded DELETE — pass a predicate")
	}
	if r.softDelete != "" {
		out, err := r.Update(ctx, map[Column]any{r.softDelete: r.deleted}, preds...)
		return int64(len(out)), err
	}
	where, args := Where(preds, 0)
	return r.querier(ctx).Exec(ctx, "DELETE FROM "+quoteOne(r.table)+" WHERE "+where, args...)
}

// DeleteByID removes one row.
func (r *Repo[T]) DeleteByID(ctx context.Context, id any) error {
	n, err := r.Delete(ctx, Eq(r.id, id))
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.NotFound("not found")
	}
	return nil
}

// projection is the column list, so SELECT * never appears and adding a column
// to the table cannot silently change a result shape.
func (r *Repo[T]) projection() string {
	parts := make([]string, len(r.columns))
	for i, c := range r.columns {
		parts[i] = quoteIdent(c)
	}
	return strings.Join(parts, ", ")
}

// scoped appends the soft-delete filter, so a caller cannot forget it.
func (r *Repo[T]) scoped(preds []Pred) []Pred {
	if r.softDelete == "" {
		return preds
	}
	return append(append([]Pred(nil), preds...), Eq(r.softDelete, r.active))
}

// ── Query ──────────────────────────────────────────────────────────────────

// Query is the frozen-scope query DSL: exactly what a single-table
// CRUD/filter/sort/page workload needs.
//
// There is no Join, GroupBy, Having, Distinct, subquery, CTE or window
// function, and there will not be. Reaching for one is the signal to use sqlc
// (a static shape) or SQL(...) (dynamic WHERE over JSONB/PostGIS). The previous
// Finder had all five, which is exactly why that signal stopped working — the
// DSL could always be stretched one more inch, so nobody ever reached for sqlc.
type Query[T any] struct {
	repo   *Repo[T]
	preds  []Pred
	orders []Order
	limit  int
	offset int
	lock   string
}

// And narrows the query.
func (q *Query[T]) And(preds ...Pred) *Query[T] {
	q.preds = append(q.preds, preds...)
	return q
}

// Order sets the sort. Prefer resolving user input through Sortable first.
func (q *Query[T]) Order(orders ...Order) *Query[T] {
	q.orders = append(q.orders, orders...)
	return q
}

func (q *Query[T]) Limit(n int) *Query[T]  { q.limit = n; return q }
func (q *Query[T]) Offset(n int) *Query[T] { q.offset = n; return q }

// Page applies a page window.
func (q *Query[T]) Page(p paging.Request) *Query[T] {
	q.limit, q.offset = p.Limit(), p.Offset()
	return q
}

// ForUpdate takes row locks. SKIP LOCKED is the house style for DB-driven
// recurring jobs: it lets N replicas drain a queue table concurrently without
// leader election, each claiming a disjoint set of rows.
func (q *Query[T]) ForUpdate(skipLocked bool) *Query[T] {
	q.lock = " FOR UPDATE"
	if skipLocked {
		q.lock += " SKIP LOCKED"
	}
	return q
}

func (q *Query[T]) build(projection string) (string, []any) {
	var b strings.Builder
	b.WriteString("SELECT " + projection + " FROM " + quoteOne(q.repo.table))

	where, args := Where(q.repo.scoped(q.preds), 0)
	if where != "" {
		b.WriteString(" WHERE " + where)
	}
	b.WriteString(orderBy(q.orders))
	if q.limit > 0 {
		b.WriteString(" LIMIT " + strconv.Itoa(q.limit))
	}
	if q.offset > 0 {
		b.WriteString(" OFFSET " + strconv.Itoa(q.offset))
	}
	b.WriteString(q.lock)
	return b.String(), args
}

// Find returns every matching row.
func (q *Query[T]) Find(ctx context.Context) ([]T, error) {
	sql, args := q.build(q.repo.projection())
	return Collect[T](q.repo.querier(ctx).Query(ctx, sql, args...))
}

// One returns the single matching row, or a not-found error.
func (q *Query[T]) One(ctx context.Context) (T, error) {
	sql, args := q.Limit(1).build(q.repo.projection())
	return One[T](q.repo.querier(ctx).Query(ctx, sql, args...))
}

// Count returns the number of matching rows.
func (q *Query[T]) Count(ctx context.Context) (int64, error) {
	// Count ignores limit/offset — it answers "how many match", not "how many
	// are on this page".
	c := *q
	c.limit, c.offset, c.orders = 0, 0, nil
	sql, args := c.build("COUNT(*)")

	var n int64
	if err := q.repo.querier(ctx).QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return 0, MapError(err)
	}
	return n, nil
}

// Exists reports whether any row matches.
//
// It is not Count()>0: this stops at the first match, which on a large table is
// the difference between an index probe and a full scan.
func (q *Query[T]) Exists(ctx context.Context) (bool, error) {
	c := *q
	c.limit, c.offset, c.orders = 1, 0, nil
	inner, args := c.build("1")

	var found bool
	if err := q.repo.querier(ctx).QueryRow(ctx, "SELECT EXISTS("+inner+")", args...).Scan(&found); err != nil {
		return false, MapError(err)
	}
	return found, nil
}

// Paged returns one page plus the total count.
//
// It warns rather than silently misbehaving when no sort is set: without ORDER
// BY, Postgres may return the same row on two pages and omit another entirely,
// so an unordered paginated read is a correctness bug, not a style preference.
func (q *Query[T]) Paged(ctx context.Context, p paging.Request) (paging.Page[T], error) {
	if len(q.orders) == 0 {
		return paging.Page[T]{}, errors.Internal(
			"sql: paginated query needs an Order — without one, rows can repeat across pages and others be skipped")
	}
	total, err := q.Count(ctx)
	if err != nil {
		return paging.Page[T]{}, err
	}
	if total == 0 {
		return paging.Empty[T](p), nil
	}
	items, err := q.Page(p).Find(ctx)
	if err != nil {
		return paging.Page[T]{}, err
	}
	return paging.NewPage(items, p, total), nil
}

// ── raw / sqlc escape hatches ──────────────────────────────────────────────

// SQL runs hand-written parameterized SQL through the platform's scanning and
// error mapping, bound to the ambient transaction.
//
// This is the sanctioned path for everything the DSL deliberately cannot
// express: JOINs, CTEs, window functions, JSONB and PostGIS operators.
func SQL[T any](ctx context.Context, q Querier, sql string, args ...any) ([]T, error) {
	return Collect[T](q.Query(ctx, sql, args...))
}

// SQLOne is SQL for a single row.
func SQLOne[T any](ctx context.Context, q Querier, sql string, args ...any) (T, error) {
	return One[T](q.Query(ctx, sql, args...))
}

// Conn returns the transaction-propagating Querier for sqlc-generated code:
//
//	q := gen.New(sql.Conn(ctx, app.DB))
//
// The generated Queries type is used DIRECTLY — sqlc output is never wrapped,
// registered or hidden behind an adapter.
func Conn(ctx context.Context, db DB) Querier {
	if tx, ok := TxFrom(ctx); ok {
		return tx
	}
	return db
}

func splitMap(m map[Column]any) ([]Column, []any) {
	cols := make([]Column, 0, len(m))
	for c := range m {
		cols = append(cols, c)
	}
	// Deterministic order: a stable statement shape lets Postgres reuse the
	// prepared plan instead of planning afresh per map iteration order.
	for i := 1; i < len(cols); i++ {
		for j := i; j > 0 && cols[j] < cols[j-1]; j-- {
			cols[j], cols[j-1] = cols[j-1], cols[j]
		}
	}
	args := make([]any, len(cols))
	for i, c := range cols {
		args[i] = m[c]
	}
	return cols, args
}
