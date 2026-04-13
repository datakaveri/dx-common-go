package query

// SelectQuery describes a SELECT statement.
type SelectQuery struct {
	Table      string
	Columns    []string // empty means "*"
	Joins      []Join
	Conditions []Condition
	OrderBy    []OrderBy
	Limit      int
	Offset     int
	ForUpdate  bool
}

// InsertQuery describes an INSERT statement.
type InsertQuery struct {
	Table     string
	Columns   []string
	Values    []any
	Returning []string
}

// UpdateQuery describes an UPDATE statement.
type UpdateQuery struct {
	Table      string
	Set        map[string]any
	Conditions []Condition
	Returning  []string
}

// DeleteQuery describes a DELETE (or soft-delete UPDATE) statement.
type DeleteQuery struct {
	Table      string
	Conditions []Condition
	// SoftDelete, when true, generates an UPDATE SET status='DELETED' instead.
	SoftDelete bool
}

// UpsertQuery describes an INSERT … ON CONFLICT DO UPDATE statement.
type UpsertQuery struct {
	Table          string
	Columns        []string
	Values         []any
	ConflictColumn string
	UpdateColumns  []string
	Returning      []string
}

// Join represents a single JOIN clause.
type Join struct {
	// Type is "INNER", "LEFT", "RIGHT", or "FULL OUTER".
	Type  string
	Table string
	On    string
}

// OrderBy specifies a column sort direction.
type OrderBy struct {
	Column string
	Desc   bool
}
