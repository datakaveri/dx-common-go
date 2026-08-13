package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFindKeyset_RealDB exercises the whole keyset path against a live Postgres:
// the seek predicate compares the decoded cursor key — a time.Time that
// round-trips through JSON as an RFC3339 *string* — against a real timestamptz
// column. That string-vs-timestamp comparison is the one thing the codec and
// SQL-shape unit tests cannot prove, and every keyset adopter (dx-audit-go,
// dx-community-layer-go) depends on it, so it gets a real database.
//
// Opt-in: set PG_INTEGRATION_DSN (e.g. the local stack's
// postgres://postgres:postgres@localhost:5433/postgres). Skipped otherwise, so
// the default `go test` stays hermetic.
func TestFindKeyset_RealDB(t *testing.T) {
	dsn := os.Getenv("PG_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("set PG_INTEGRATION_DSN to run the keyset integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const table = "keyset_it_probe"
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`CREATE TABLE `+table+` (id text PRIMARY KEY, created_at timestamptz NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer pool.Exec(ctx, `DROP TABLE IF EXISTS `+table)

	// 25 rows. A shared timestamp on three of them forces the tie-breaker (id)
	// to carry the order — the exact case a non-unique sort key creates.
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	type want struct {
		id string
		ts time.Time
	}
	var inserted []want
	for i := 0; i < 25; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		if i >= 10 && i <= 12 {
			ts = base.Add(10 * time.Minute) // three rows tie on created_at
		}
		id := fmt.Sprintf("c-%02d", i)
		if _, err := pool.Exec(ctx,
			`INSERT INTO `+table+` (id, created_at) VALUES ($1, $2)`, id, ts); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		inserted = append(inserted, want{id, ts})
	}

	type ksRow struct {
		ID        string    `db:"id"`
		CreatedAt time.Time `db:"created_at"`
	}
	repo := New[ksRow](pool, WithTable[ksRow](table))

	// Page through newest-first, following NextCursor until it is empty. If the
	// string-vs-timestamp seek were broken this Query would error here.
	const pageSize = 7
	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("cursor never terminated — a page is not advancing")
		}
		page, err := repo.FindKeyset(ctx, nil, "created_at", "id", true, cursor, pageSize,
			func(r ksRow) KeysetCursor { return KeysetCursor{Key: r.CreatedAt, ID: r.ID} })
		if err != nil {
			t.Fatalf("FindKeyset (cursor=%q): %v", cursor, err)
		}
		for _, r := range page.Items {
			got = append(got, r.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	// Expected order: created_at DESC, then id DESC among the tied rows.
	// Ties are c-10..c-12 → DESC id gives c-12, c-11, c-10.
	if len(got) != len(inserted) {
		t.Fatalf("walked %d rows across pages, want %d (dupes or gaps): %v", len(got), len(inserted), got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("row %s returned twice — the seek is not stable: %v", id, got)
		}
		seen[id] = true
	}
	// Spot-check the tie region keeps a total, id-DESC order.
	pos := map[string]int{}
	for i, id := range got {
		pos[id] = i
	}
	if !(pos["c-12"] < pos["c-11"] && pos["c-11"] < pos["c-10"]) {
		t.Fatalf("tied rows out of id-DESC order: c-12@%d c-11@%d c-10@%d", pos["c-12"], pos["c-11"], pos["c-10"])
	}
}
