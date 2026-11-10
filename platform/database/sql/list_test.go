package sql

import (
	"net/url"
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/filter"
	"github.com/datakaveri/dx-common-go/platform/paging"
)

func testSpec(t *testing.T) filter.Spec {
	t.Helper()
	return filter.MustNew(
		[]filter.Field{
			filter.StringSet("entityType", "entity_type"),
			filter.StringSet("status", "status", filter.Allowed("pending", "granted", "rejected")),
		},
		filter.DefaultTime("createdAt", "created_at"),
	)
}

func testSchema() ListSchema {
	return ListSchema{
		Fields: map[filter.Key]FilterField{
			"entity_type": {Column: "entity_type", Mode: MatchEqualAny},
			"status":      {Column: "status", Mode: MatchEqualAny},
			"created_at":  {Column: "created_at", Mode: MatchTime},
		},
		Sort:         Sortable{"createdAt": "created_at", "status": "status"},
		DefaultOrder: []Order{Desc("created_at")},
		TieBreak:     Desc("id"),
	}
}

// compile parses q against the spec and compiles it, returning the rendered
// WHERE body and args for assertion.
func compile(t *testing.T, q url.Values, sortRaw string) (string, []any, []Order) {
	t.Helper()
	spec := testSpec(t)
	fr, err := spec.Parse(q)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	keys, err := paging.ParseSort(sortRaw)
	if err != nil {
		t.Fatalf("ParseSort: %v", err)
	}
	preds, orders, err := testSchema().Compile(fr, keys)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	where, args := Where(preds, 0)
	return where, args, orders
}

func TestCompileSingleValueIsEquals(t *testing.T) {
	where, args, _ := compile(t, url.Values{"entityType": {"A"}}, "")
	if !strings.Contains(where, `"entity_type" = $1`) {
		t.Errorf("where = %q, want an equality", where)
	}
	if len(args) != 1 || args[0] != "A" {
		t.Errorf("args = %v, want [A]", args)
	}
}

func TestCompileMultiValueIsAnyArray(t *testing.T) {
	where, args, _ := compile(t, url.Values{"entityType": {"A", "B", "C"}}, "")
	if !strings.Contains(where, `"entity_type" = ANY($1)`) {
		t.Errorf("where = %q, want = ANY($1)", where)
	}
	// One bound parameter regardless of list length: the whole slice.
	if len(args) != 1 {
		t.Fatalf("args = %v, want a single bound array", args)
	}
	got, ok := args[0].([]string)
	if !ok || len(got) != 3 {
		t.Errorf("args[0] = %#v, want []string of len 3", args[0])
	}
}

func TestCompileFieldsAreAnded(t *testing.T) {
	where, args, _ := compile(t, url.Values{"entityType": {"A"}, "status": {"pending"}}, "")
	// Deterministic key order (entity_type before status) and ANDed.
	if !strings.Contains(where, " AND ") {
		t.Errorf("where = %q, want two predicates ANDed", where)
	}
	if i := strings.Index(where, "entity_type"); i < 0 || i > strings.Index(where, "status") {
		t.Errorf("where = %q, want entity_type before status (deterministic)", where)
	}
	if len(args) != 2 {
		t.Errorf("args = %v, want 2", args)
	}
}

func TestCompileTemporalBetween(t *testing.T) {
	q := url.Values{"timerel": {"between"}, "time": {"2026-01-01T00:00:00Z"}, "endTime": {"2026-12-31T00:00:00Z"}}
	where, args, _ := compile(t, q, "")
	if !strings.Contains(where, `"created_at" BETWEEN $1 AND $2`) {
		t.Errorf("where = %q, want a BETWEEN", where)
	}
	if len(args) != 2 {
		t.Errorf("args = %v, want 2 time bounds", args)
	}
}

func TestCompileDefaultOrderAndTieBreak(t *testing.T) {
	_, _, orders := compile(t, url.Values{}, "")
	if len(orders) != 2 || orders[0].Column != "created_at" || !orders[0].Desc || orders[1].Column != "id" {
		t.Fatalf("orders = %+v, want [created_at DESC, id DESC]", orders)
	}
}

func TestCompileRequestedSortGetsTieBreak(t *testing.T) {
	_, _, orders := compile(t, url.Values{}, "status:asc")
	if len(orders) != 2 || orders[0].Column != "status" || orders[0].Desc || orders[1].Column != "id" {
		t.Fatalf("orders = %+v, want [status ASC, id DESC]", orders)
	}
}

func TestCompileRejectsUnmappedSort(t *testing.T) {
	spec := testSpec(t)
	fr, _ := spec.Parse(url.Values{})
	keys, _ := paging.ParseSort("name:asc") // not in Sortable
	if _, _, err := testSchema().Compile(fr, keys); err == nil {
		t.Fatal("unmapped sort field was accepted; it would reach SQL verbatim")
	}
}

func TestVerifyCatchesDrift(t *testing.T) {
	spec := testSpec(t)
	// A schema missing the status mapping must fail Verify at startup.
	bad := ListSchema{
		Fields: map[filter.Key]FilterField{
			"entity_type": {Column: "entity_type", Mode: MatchEqualAny},
			"created_at":  {Column: "created_at", Mode: MatchTime},
		},
		DefaultOrder: []Order{Desc("created_at")},
	}
	if err := bad.Verify(spec); err == nil {
		t.Error("Verify accepted a schema missing the status mapping")
	}
	if err := testSchema().Verify(spec); err != nil {
		t.Errorf("Verify rejected a complete schema: %v", err)
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Errorf("escapeLike = %q", got)
	}
}
