package sql_test

import (
	"context"
	"testing"

	sql "github.com/datakaveri/dx-common-go/platform/database/sql"
)

func TestUpsert_InsertThenUpdateOnConflict(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()

	// First call inserts.
	got, err := r.Upsert(ctx,
		map[sql.Column]any{"name": "widget-a", "owner": "u-1", "status": "ACTIVE"},
		[]sql.Column{"name"}, []sql.Column{"owner", "status"})
	if err != nil {
		t.Fatalf("upsert insert: %v", err)
	}
	if got.Owner != "u-1" || got.Status != "ACTIVE" {
		t.Fatalf("insert row = %+v", got)
	}
	firstID := got.ID

	// Second call with the same name conflicts and updates owner+status,
	// returning the SAME row (same id), not a new one.
	got, err = r.Upsert(ctx,
		map[sql.Column]any{"name": "widget-a", "owner": "u-2", "status": "INACTIVE"},
		[]sql.Column{"name"}, []sql.Column{"owner", "status"})
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if got.ID != firstID {
		t.Fatalf("upsert created a new row (id %d → %d) instead of updating", firstID, got.ID)
	}
	if got.Owner != "u-2" || got.Status != "INACTIVE" {
		t.Fatalf("upsert did not apply the update: %+v", got)
	}

	// Exactly one row exists.
	n, err := r.All().Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}

func TestUpsert_EmptyUpdateReturnsExistingRow(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()

	first, err := r.Upsert(ctx,
		map[sql.Column]any{"name": "w", "owner": "u-1", "status": "ACTIVE"},
		[]sql.Column{"name"}, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Conflict with an empty update set: no-op, but the EXISTING row comes back
	// (not an error, not an empty result).
	again, err := r.Upsert(ctx,
		map[sql.Column]any{"name": "w", "owner": "u-999", "status": "INACTIVE"},
		[]sql.Column{"name"}, nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if again.ID != first.ID || again.Owner != "u-1" {
		t.Fatalf("empty-update upsert should return the unchanged existing row, got %+v", again)
	}
}

func TestUpsert_RejectsConflictColumnNotInValues(t *testing.T) {
	r := newRepo(t, newDB(t))
	_, err := r.Upsert(context.Background(),
		map[sql.Column]any{"name": "w", "owner": "u-1"},
		[]sql.Column{"id"}, nil)
	if err == nil {
		t.Fatal("expected an error when the conflict column is not in values")
	}
}

func TestInsertIgnore(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()

	inserted, err := r.InsertIgnore(ctx,
		map[sql.Column]any{"name": "once", "owner": "u-1", "status": "ACTIVE"},
		[]sql.Column{"name"})
	if err != nil {
		t.Fatalf("first insert-ignore: %v", err)
	}
	if !inserted {
		t.Fatal("first InsertIgnore reported inserted=false")
	}

	inserted, err = r.InsertIgnore(ctx,
		map[sql.Column]any{"name": "once", "owner": "u-2", "status": "INACTIVE"},
		[]sql.Column{"name"})
	if err != nil {
		t.Fatalf("second insert-ignore: %v", err)
	}
	if inserted {
		t.Fatal("second InsertIgnore reported inserted=true on a conflict")
	}

	// The existing row is untouched (owner still u-1).
	got, err := r.Where(sql.Eq("name", "once")).One(ctx)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Owner != "u-1" {
		t.Fatalf("InsertIgnore altered the existing row: %+v", got)
	}
}

func TestInsertMany(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()

	rows := []map[sql.Column]any{
		{"name": "m1", "owner": "u-1", "status": "ACTIVE"},
		{"name": "m2", "owner": "u-1", "status": "ACTIVE"},
		{"name": "m3", "owner": "u-2", "status": "INACTIVE"},
	}
	out, err := r.InsertMany(ctx, rows)
	if err != nil {
		t.Fatalf("insert many: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("returned %d rows, want 3", len(out))
	}
	// Returned in input order.
	if out[0].Name != "m1" || out[1].Name != "m2" || out[2].Name != "m3" {
		t.Fatalf("rows out of input order: %v", []string{out[0].Name, out[1].Name, out[2].Name})
	}
	if out[2].Status != "INACTIVE" {
		t.Fatalf("row 3 status = %q", out[2].Status)
	}
}

func TestInsertMany_RejectsRaggedRows(t *testing.T) {
	r := newRepo(t, newDB(t))
	_, err := r.InsertMany(context.Background(), []map[sql.Column]any{
		{"name": "a", "owner": "u-1", "status": "ACTIVE"},
		{"name": "b", "owner": "u-1"}, // missing status → different shape
	})
	if err == nil {
		t.Fatal("expected an error for rows with differing columns")
	}
}

func TestInsertMany_EmptyIsRejected(t *testing.T) {
	r := newRepo(t, newDB(t))
	if _, err := r.InsertMany(context.Background(), nil); err == nil {
		t.Fatal("expected an error for an empty batch")
	}
}

func TestKeyset_PaginatesInTotalOrderWithoutGaps(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()

	// 7 rows; page by 3 ascending on (name, id). name is unique here, but the
	// method still appends id as the tiebreaker so a repeated key is safe.
	seed(t, r, "k1", "k2", "k3", "k4", "k5", "k6", "k7")

	cursorOf := func(w widget) sql.KeysetCursor {
		return sql.KeysetCursor{Key: w.Name, ID: w.ID}
	}

	var seen []string
	cursor := ""
	pages := 0
	for {
		page, err := r.All().Keyset(ctx, "name", "id", false, cursor, 3, cursorOf)
		if err != nil {
			t.Fatalf("keyset page: %v", err)
		}
		for _, w := range page.Items {
			seen = append(seen, w.Name)
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("keyset did not terminate")
		}
	}

	want := []string{"k1", "k2", "k3", "k4", "k5", "k6", "k7"}
	if len(seen) != len(want) {
		t.Fatalf("saw %d rows across pages, want %d: %v", len(seen), len(want), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (order/gap defect): %v", i, seen[i], want[i], seen)
		}
	}
}

func TestKeyset_MangledCursorIsValidationError(t *testing.T) {
	r := newRepo(t, newDB(t))
	_, err := r.All().Keyset(context.Background(), "name", "id", false, "!!not-base64!!", 3,
		func(w widget) sql.KeysetCursor { return sql.KeysetCursor{Key: w.Name, ID: w.ID} })
	if err == nil {
		t.Fatal("a mangled cursor must be an error, not silently ignored")
	}
}
