package query

import (
	"fmt"
	"strings"
)

// SQLBuilder converts query model structs into parameterized SQL strings and
// argument slices suitable for pgx.
type SQLBuilder struct{}

// New returns a fresh SQLBuilder.
func New() *SQLBuilder { return &SQLBuilder{} }

// BuildSelect generates a parameterized SELECT statement.
func (b *SQLBuilder) BuildSelect(q SelectQuery) (string, []any) {
	var sb strings.Builder
	var args []any
	idx := 1

	cols := "*"
	if len(q.Columns) > 0 {
		cols = strings.Join(q.Columns, ", ")
	}
	fmt.Fprintf(&sb, "SELECT %s FROM %s", cols, q.Table)

	for _, j := range q.Joins {
		fmt.Fprintf(&sb, " %s JOIN %s ON %s", j.Type, j.Table, j.On)
	}

	if len(q.Conditions) > 0 {
		sb.WriteString(" WHERE ")
		clauses := buildConditions(q.Conditions, &args, &idx)
		sb.WriteString(clauses)
	}

	if len(q.OrderBy) > 0 {
		parts := make([]string, 0, len(q.OrderBy))
		for _, o := range q.OrderBy {
			dir := "ASC"
			if o.Desc {
				dir = "DESC"
			}
			parts = append(parts, o.Column+" "+dir)
		}
		fmt.Fprintf(&sb, " ORDER BY %s", strings.Join(parts, ", "))
	}

	if q.Limit > 0 {
		fmt.Fprintf(&sb, " LIMIT $%d", idx)
		args = append(args, q.Limit)
		idx++
	}
	if q.Offset > 0 {
		fmt.Fprintf(&sb, " OFFSET $%d", idx)
		args = append(args, q.Offset)
		idx++
	}

	if q.ForUpdate {
		sb.WriteString(" FOR UPDATE")
	}

	return sb.String(), args
}

// BuildInsert generates a parameterized INSERT statement.
func (b *SQLBuilder) BuildInsert(q InsertQuery) (string, []any) {
	var sb strings.Builder
	var args []any
	idx := 1

	placeholders := make([]string, 0, len(q.Columns))
	for _, v := range q.Values {
		placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
		args = append(args, v)
		idx++
	}

	fmt.Fprintf(&sb, "INSERT INTO %s (%s) VALUES (%s)",
		q.Table,
		strings.Join(q.Columns, ", "),
		strings.Join(placeholders, ", "),
	)

	if len(q.Returning) > 0 {
		fmt.Fprintf(&sb, " RETURNING %s", strings.Join(q.Returning, ", "))
	}

	return sb.String(), args
}

// BuildUpdate generates a parameterized UPDATE statement.
func (b *SQLBuilder) BuildUpdate(q UpdateQuery) (string, []any) {
	var sb strings.Builder
	var args []any
	idx := 1

	setParts := make([]string, 0, len(q.Set))
	for col, val := range q.Set {
		setParts = append(setParts, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, val)
		idx++
	}

	fmt.Fprintf(&sb, "UPDATE %s SET %s", q.Table, strings.Join(setParts, ", "))

	if len(q.Conditions) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(buildConditions(q.Conditions, &args, &idx))
	}

	if len(q.Returning) > 0 {
		fmt.Fprintf(&sb, " RETURNING %s", strings.Join(q.Returning, ", "))
	}

	return sb.String(), args
}

// BuildDelete generates a DELETE or soft-delete UPDATE statement.
func (b *SQLBuilder) BuildDelete(q DeleteQuery) (string, []any) {
	var sb strings.Builder
	var args []any
	idx := 1

	if q.SoftDelete {
		fmt.Fprintf(&sb, "UPDATE %s SET status = 'DELETED'", q.Table)
		if len(q.Conditions) > 0 {
			sb.WriteString(" WHERE ")
			sb.WriteString(buildConditions(q.Conditions, &args, &idx))
		}
	} else {
		fmt.Fprintf(&sb, "DELETE FROM %s", q.Table)
		if len(q.Conditions) > 0 {
			sb.WriteString(" WHERE ")
			sb.WriteString(buildConditions(q.Conditions, &args, &idx))
		}
	}

	return sb.String(), args
}

// BuildUpsert generates an INSERT … ON CONFLICT DO UPDATE statement.
func (b *SQLBuilder) BuildUpsert(q UpsertQuery) (string, []any) {
	var sb strings.Builder
	var args []any
	idx := 1

	placeholders := make([]string, 0, len(q.Columns))
	for _, v := range q.Values {
		placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
		args = append(args, v)
		idx++
	}

	fmt.Fprintf(&sb, "INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO UPDATE SET ",
		q.Table,
		strings.Join(q.Columns, ", "),
		strings.Join(placeholders, ", "),
		q.ConflictColumn,
	)

	updateParts := make([]string, 0, len(q.UpdateColumns))
	for _, col := range q.UpdateColumns {
		updateParts = append(updateParts, fmt.Sprintf("%s = EXCLUDED.%s", col, col))
	}
	sb.WriteString(strings.Join(updateParts, ", "))

	if len(q.Returning) > 0 {
		fmt.Fprintf(&sb, " RETURNING %s", strings.Join(q.Returning, ", "))
	}

	return sb.String(), args
}

// buildConditions renders a slice of Conditions as a WHERE body (without the
// WHERE keyword), joining top-level predicates with AND.
func buildConditions(conditions []Condition, args *[]any, idx *int) string {
	parts := make([]string, 0, len(conditions))
	for _, c := range conditions {
		parts = append(parts, conditionSQL(c, args, idx))
	}
	return strings.Join(parts, " AND ")
}
