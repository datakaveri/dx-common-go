package dao

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/datakaveri/dx-common-go/database/postgres/query"
)

// BaseDAO provides generic CRUD operations for a single database table.
// T is the target struct type; its exported fields must match column names for
// pgx.RowToStructByName to work correctly.
type BaseDAO[T any] struct {
	Pool      *pgxpool.Pool
	TableName string
	builder   *query.SQLBuilder
}

// NewBaseDAO creates a BaseDAO for the given table.
func NewBaseDAO[T any](pool *pgxpool.Pool, tableName string) *BaseDAO[T] {
	return &BaseDAO[T]{Pool: pool, TableName: tableName, builder: query.New()}
}

// FindByID fetches a single row by its primary-key id column.
func (d *BaseDAO[T]) FindByID(ctx context.Context, id string) (*T, error) {
	q := query.SelectQuery{
		Table:      d.TableName,
		Conditions: query.NewConditionBuilder().Eq("id", id).Build(),
		Limit:      1,
	}
	sql, args := d.builder.BuildSelect(q)

	rows, err := d.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, MapPgError(err)
	}
	defer rows.Close()

	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, MapPgError(err)
	}
	return &result, nil
}

// FindAll fetches all rows matching the provided conditions (empty means all).
func (d *BaseDAO[T]) FindAll(ctx context.Context, conditions []query.Condition) ([]T, error) {
	q := query.SelectQuery{
		Table:      d.TableName,
		Conditions: conditions,
	}
	sql, args := d.builder.BuildSelect(q)

	rows, err := d.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, MapPgError(err)
	}
	defer rows.Close()

	results, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, MapPgError(err)
	}
	return results, nil
}

// Count returns the number of rows matching conditions.
func (d *BaseDAO[T]) Count(ctx context.Context, conditions []query.Condition) (int64, error) {
	q := query.SelectQuery{
		Table:      d.TableName,
		Columns:    []string{"COUNT(*) AS count"},
		Conditions: conditions,
	}
	sql, args := d.builder.BuildSelect(q)

	var count int64
	if err := d.Pool.QueryRow(ctx, sql, args...).Scan(&count); err != nil {
		return 0, MapPgError(err)
	}
	return count, nil
}

// Insert inserts a row using the provided column names and corresponding values.
func (d *BaseDAO[T]) Insert(ctx context.Context, columns []string, values []any) error {
	q := query.InsertQuery{
		Table:   d.TableName,
		Columns: columns,
		Values:  values,
	}
	sql, args := d.builder.BuildInsert(q)

	if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
		return MapPgError(err)
	}
	return nil
}

// Update applies SET assignments to all rows matching conditions.
func (d *BaseDAO[T]) Update(ctx context.Context, set map[string]any, conditions []query.Condition) error {
	q := query.UpdateQuery{
		Table:      d.TableName,
		Set:        set,
		Conditions: conditions,
	}
	sql, args := d.builder.BuildUpdate(q)

	if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
		return MapPgError(err)
	}
	return nil
}

// SoftDelete sets status='DELETED' on the row with the given id.
func (d *BaseDAO[T]) SoftDelete(ctx context.Context, id string) error {
	q := query.DeleteQuery{
		Table:      d.TableName,
		Conditions: query.NewConditionBuilder().Eq("id", id).Build(),
		SoftDelete: true,
	}
	sql, args := d.builder.BuildDelete(q)

	tag, err := d.Pool.Exec(ctx, sql, args...)
	if err != nil {
		return MapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return MapPgError(pgx.ErrNoRows)
	}
	return nil
}

// HardDelete permanently deletes rows matching conditions.
func (d *BaseDAO[T]) HardDelete(ctx context.Context, conditions []query.Condition) error {
	q := query.DeleteQuery{
		Table:      d.TableName,
		Conditions: conditions,
	}
	sql, args := d.builder.BuildDelete(q)

	if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
		return MapPgError(err)
	}
	return nil
}

// InsertReturning inserts a row and scans the RETURNING clause into dest.
func (d *BaseDAO[T]) InsertReturning(ctx context.Context, columns []string, values []any, returning []string, dest ...any) error {
	q := query.InsertQuery{
		Table:     d.TableName,
		Columns:   columns,
		Values:    values,
		Returning: returning,
	}
	sql, args := d.builder.BuildInsert(q)

	if err := d.Pool.QueryRow(ctx, sql, args...).Scan(dest...); err != nil {
		return fmt.Errorf("InsertReturning: %w", MapPgError(err))
	}
	return nil
}
