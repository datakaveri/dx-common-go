package sql

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestNumberPlaceholders pins the Raw escape-hatch rendering (ROADMAP P2-2): a
// single ? is a positional placeholder numbered from the builder's current arg
// count, and a doubled ?? is an escaped literal ? — the JSONB key-exists
// operators — so a JSONB predicate no longer has its operator eaten as a
// placeholder.
func TestNumberPlaceholders(t *testing.T) {
	tests := []struct {
		name     string
		fragment string
		start    []any // args already accumulated before this fragment
		values   []any
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "single placeholder numbers from $1",
			fragment: "char_length(name) > ?",
			values:   []any{4},
			wantSQL:  "char_length(name) > $1",
			wantArgs: []any{4},
		},
		{
			name:     "placeholders continue from the current arg count",
			fragment: "x > ?",
			start:    []any{"a", "b"},
			values:   []any{5},
			wantSQL:  "x > $3",
			wantArgs: []any{"a", "b", 5},
		},
		{
			name:     "doubled ?? is the JSONB key-exists operator, not a placeholder",
			fragment: "tags ?? ?",
			values:   []any{"climate"},
			wantSQL:  "tags ? $1",
			wantArgs: []any{"climate"},
		},
		{
			name:     "??| renders the any-key operator ?|",
			fragment: "tags ??| ?",
			values:   []any{[]string{"a", "b"}},
			wantSQL:  "tags ?| $1",
			wantArgs: []any{[]string{"a", "b"}},
		},
		{
			name:     "??& renders the all-keys operator ?&",
			fragment: "tags ??& ?",
			values:   []any{[]string{"a", "b"}},
			wantSQL:  "tags ?& $1",
			wantArgs: []any{[]string{"a", "b"}},
		},
		{
			name:     "operator and placeholder mixed",
			fragment: "metadata ?? ? AND n > ?",
			values:   []any{"k", 5},
			wantSQL:  "metadata ? $1 AND n > $2",
			wantArgs: []any{"k", 5},
		},
		{
			name:     "a ? with no value left is passed through",
			fragment: "a = ? OR trailing ?",
			values:   []any{1},
			wantSQL:  "a = $1 OR trailing ?",
			wantArgs: []any{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]any(nil), tt.start...)
			got := numberPlaceholders(tt.fragment, &args, tt.values)
			if got != tt.wantSQL {
				t.Errorf("SQL = %q, want %q", got, tt.wantSQL)
			}
			if fmt.Sprint(args) != fmt.Sprint(tt.wantArgs) {
				t.Errorf("args = %v, want %v", args, tt.wantArgs)
			}
		})
	}
}

// TestIsNoRows pins that a "no rows" result is recognised by the error chain,
// not the message text (ROADMAP P2-2): only pgx.ErrNoRows, however wrapped with
// %w, counts — an unrelated error that merely quotes the same words does not,
// so it is not misclassified as NotFound.
func TestIsNoRows(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"the sentinel itself", pgx.ErrNoRows, true},
		{"wrapped with %w", fmt.Errorf("load item: %w", pgx.ErrNoRows), true},
		{"double-wrapped", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", pgx.ErrNoRows)), true},
		{"nil", nil, false},
		{"an unrelated error", errors.New("connection refused"), false},
		// The false positive the string match produced: same words, different
		// (non-sentinel) error — e.g. the phrase embedded via %s, or scanned data.
		{"same message text, not the sentinel", errors.New(pgx.ErrNoRows.Error()), false},
		{"quotes the message via %s", fmt.Errorf("scan: %s", pgx.ErrNoRows.Error()), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoRows(tt.err); got != tt.want {
				t.Errorf("isNoRows(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
