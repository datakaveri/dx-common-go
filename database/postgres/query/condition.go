package query

import "fmt"

// Operator represents a SQL comparison or membership operator.
type Operator string

const (
	OpEq      Operator = "="
	OpNotEq   Operator = "<>"
	OpGt      Operator = ">"
	OpGte     Operator = ">="
	OpLt      Operator = "<"
	OpLte     Operator = "<="
	OpLike    Operator = "LIKE"
	OpILike   Operator = "ILIKE"
	OpIn      Operator = "IN"
	OpNotIn   Operator = "NOT IN"
	OpIsNull  Operator = "IS NULL"
	OpNotNull Operator = "IS NOT NULL"
	OpAnd     Operator = "AND"
	OpOr      Operator = "OR"
)

// Condition represents one predicate in a WHERE clause.
type Condition struct {
	Column   string
	Op       Operator
	Value    any
	// Sub holds nested conditions for AND / OR groupings.
	Sub      []Condition
}

// ConditionBuilder provides a fluent API for constructing WHERE conditions.
type ConditionBuilder struct {
	conditions []Condition
}

// NewConditionBuilder returns a fresh builder.
func NewConditionBuilder() *ConditionBuilder {
	return &ConditionBuilder{}
}

// Eq appends column = value.
func (b *ConditionBuilder) Eq(column string, value any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpEq, Value: value})
	return b
}

// NotEq appends column <> value.
func (b *ConditionBuilder) NotEq(column string, value any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpNotEq, Value: value})
	return b
}

// Gt appends column > value.
func (b *ConditionBuilder) Gt(column string, value any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpGt, Value: value})
	return b
}

// Gte appends column >= value.
func (b *ConditionBuilder) Gte(column string, value any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpGte, Value: value})
	return b
}

// Lt appends column < value.
func (b *ConditionBuilder) Lt(column string, value any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpLt, Value: value})
	return b
}

// Lte appends column <= value.
func (b *ConditionBuilder) Lte(column string, value any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpLte, Value: value})
	return b
}

// Like appends column LIKE pattern.
func (b *ConditionBuilder) Like(column string, pattern string) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpLike, Value: pattern})
	return b
}

// ILike appends column ILIKE pattern (case-insensitive).
func (b *ConditionBuilder) ILike(column string, pattern string) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpILike, Value: pattern})
	return b
}

// In appends column IN (values...).
func (b *ConditionBuilder) In(column string, values any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpIn, Value: values})
	return b
}

// NotIn appends column NOT IN (values...).
func (b *ConditionBuilder) NotIn(column string, values any) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpNotIn, Value: values})
	return b
}

// IsNull appends column IS NULL.
func (b *ConditionBuilder) IsNull(column string) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpIsNull})
	return b
}

// IsNotNull appends column IS NOT NULL.
func (b *ConditionBuilder) IsNotNull(column string) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Column: column, Op: OpNotNull})
	return b
}

// And groups the supplied conditions with AND.
func (b *ConditionBuilder) And(conditions ...Condition) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Op: OpAnd, Sub: conditions})
	return b
}

// Or groups the supplied conditions with OR.
func (b *ConditionBuilder) Or(conditions ...Condition) *ConditionBuilder {
	b.conditions = append(b.conditions, Condition{Op: OpOr, Sub: conditions})
	return b
}

// Build returns the accumulated slice of Conditions.
func (b *ConditionBuilder) Build() []Condition {
	return b.conditions
}

// conditionSQL renders a single Condition into a SQL fragment, appending any
// parameter value to args and returning the next placeholder index.
func conditionSQL(c Condition, args *[]any, idx *int) string {
	switch c.Op {
	case OpIsNull:
		return fmt.Sprintf("%s IS NULL", c.Column)
	case OpNotNull:
		return fmt.Sprintf("%s IS NOT NULL", c.Column)
	case OpIn, OpNotIn:
		*args = append(*args, c.Value)
		placeholder := fmt.Sprintf("$%d", *idx)
		*idx++
		return fmt.Sprintf("%s %s (%s)", c.Column, c.Op, placeholder)
	case OpAnd, OpOr:
		parts := make([]string, 0, len(c.Sub))
		for _, sub := range c.Sub {
			parts = append(parts, conditionSQL(sub, args, idx))
		}
		joined := ""
		sep := fmt.Sprintf(" %s ", c.Op)
		for i, p := range parts {
			if i > 0 {
				joined += sep
			}
			joined += p
		}
		return "(" + joined + ")"
	default:
		*args = append(*args, c.Value)
		placeholder := fmt.Sprintf("$%d", *idx)
		*idx++
		return fmt.Sprintf("%s %s %s", c.Column, c.Op, placeholder)
	}
}
