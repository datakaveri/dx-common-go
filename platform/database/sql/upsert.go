package sql

import (
	"context"
	"strconv"
	"strings"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// Upsert inserts values, or updates the named columns when a row already exists
// with the same value(s) in the conflict column(s). It returns the stored row
// (inserted or updated), so a caller sees database-generated columns without a
// re-read.
//
// It is ON CONFLICT (conflict...) DO UPDATE — the idempotent-write primitive
// (Credits ensure-account, reactions/bookmarks, agent idempotency) that
// otherwise forces a service onto hand-written SQL. update names the columns to
// overwrite from the proposed values; a column in values but not in update is
// written only on insert (e.g. created_at), and the conflict columns are never
// updated. An empty update degenerates to InsertIgnore semantics but returns the
// EXISTING row rather than nothing — use InsertIgnore when you want the no-op.
func (r *Repo[T]) Upsert(ctx context.Context, values map[Column]any, conflict []Column, update []Column) (T, error) {
	var zero T
	if len(values) == 0 {
		return zero, errors.Validation("nothing to upsert")
	}
	if len(conflict) == 0 {
		return zero, errors.Internal("sql: Upsert needs at least one conflict column")
	}
	cols, args := splitMap(values)
	valueCols := make(map[Column]bool, len(cols))
	for _, c := range cols {
		valueCols[c] = true
	}
	for _, c := range conflict {
		if !valueCols[c] {
			return zero, errors.Internal("sql: Upsert conflict column " + string(c) + " is not in values")
		}
	}
	for _, c := range update {
		if !valueCols[c] {
			return zero, errors.Internal("sql: Upsert update column " + string(c) + " is not in values")
		}
	}

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
	b.WriteString(") ON CONFLICT (")
	for i, c := range conflict {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(") DO ")
	if len(update) == 0 {
		// DO NOTHING would not RETURN the conflicting row, so the caller could
		// not read it back. A no-op update on the conflict target does, keeping
		// the "always returns the stored row" contract.
		b.WriteString("UPDATE SET " + quoteIdent(conflict[0]) + " = EXCLUDED." + quoteIdent(conflict[0]))
	} else {
		b.WriteString("UPDATE SET ")
		for i, c := range update {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(c) + " = EXCLUDED." + quoteIdent(c))
		}
	}
	b.WriteString(" RETURNING " + r.projection())

	return One[T](r.querier(ctx).Query(ctx, b.String(), args...))
}

// InsertIgnore inserts values, or does nothing when a row already exists with
// the same conflict-column value(s). It reports whether a row was inserted.
//
// This is ON CONFLICT (conflict...) DO NOTHING — the "create if absent, don't
// care about the existing one" primitive (idempotent event/outbox rows, a
// dedup guard). Unlike Upsert it does not read the existing row back: DO NOTHING
// returns no row, so RETURNING yields nothing on a conflict, which is exactly
// how inserted is detected.
func (r *Repo[T]) InsertIgnore(ctx context.Context, values map[Column]any, conflict []Column) (inserted bool, err error) {
	if len(values) == 0 {
		return false, errors.Validation("nothing to insert")
	}
	if len(conflict) == 0 {
		return false, errors.Internal("sql: InsertIgnore needs at least one conflict column")
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
	b.WriteString(") ON CONFLICT (")
	for i, c := range conflict {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(") DO NOTHING")

	n, err := r.querier(ctx).Exec(ctx, b.String(), args...)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// InsertMany writes several rows of the SAME shape in one round trip and
// returns the stored rows in input order. Every row map MUST have an identical
// key set; a differing shape is rejected rather than silently producing a
// ragged statement.
//
// This is the modest-batch primitive: one multi-row INSERT, not N calls and not
// the row-by-row loop that leaks into services (OGC BulkUpsertFeatures). For
// high-volume ingestion (tens of thousands of rows) use the DB-level CopyFrom
// instead — this builds one parameterized statement, so it is bounded by the
// 65535-parameter protocol limit.
func (r *Repo[T]) InsertMany(ctx context.Context, rows []map[Column]any) ([]T, error) {
	if len(rows) == 0 {
		return nil, errors.Validation("nothing to insert")
	}
	// The column order is taken from the first row (deterministic via splitMap)
	// and every subsequent row must match it exactly.
	cols, firstArgs := splitMap(rows[0])
	if len(cols) == 0 {
		return nil, errors.Validation("nothing to insert")
	}
	if len(cols)*len(rows) > 65535 {
		return nil, errors.Validation("sql: InsertMany exceeds the 65535-parameter limit; use CopyFrom for high volume")
	}

	args := make([]any, 0, len(cols)*len(rows))
	args = append(args, firstArgs...)

	var b strings.Builder
	b.WriteString("INSERT INTO " + quoteOne(r.table) + " (")
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c))
	}
	b.WriteString(") VALUES ")

	n := 0
	writeTuple := func(count int) {
		b.WriteString("(")
		for i := 0; i < count; i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			n++
			b.WriteString("$" + strconv.Itoa(n))
		}
		b.WriteString(")")
	}
	writeTuple(len(cols))

	for ri := 1; ri < len(rows); ri++ {
		rcols, rargs := splitMap(rows[ri])
		if !sameColumns(cols, rcols) {
			return nil, errors.Validation("sql: InsertMany rows must all have the same columns")
		}
		b.WriteString(", ")
		writeTuple(len(cols))
		args = append(args, rargs...)
	}
	b.WriteString(" RETURNING " + r.projection())

	return Collect[T](r.querier(ctx).Query(ctx, b.String(), args...))
}

// sameColumns reports whether two column slices (both from splitMap, so both
// sorted) are element-wise equal.
func sameColumns(a, b []Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
