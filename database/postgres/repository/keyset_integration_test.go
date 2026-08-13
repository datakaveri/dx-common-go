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
// the seek predicate compares the decoded cursor values — which round-trip
// through JSON as *strings* (a time.Time as RFC3339, an id as text) — against
// real typed columns. Whether pgx lets those string params compare against a
// timestamptz and, crucially, a uuid column is the one thing the codec and
// SQL-shape unit tests cannot prove, and it is exactly where an adopter would
// silently break. Both id column types the fleet uses are covered: text
// (dx-audit-go's varchar ids) and uuid (dx-community-layer-go's).
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

	t.Run("text ids (audit-shaped)", func(t *testing.T) {
		walkKeyset(t, ctx, pool, "text", func(i int) string { return fmt.Sprintf("c-%02d", i) })
	})
	t.Run("uuid ids (community-shaped)", func(t *testing.T) {
		// Monotonic uuids so their binary order matches i, keeping the tie-region
		// assertion (higher i sorts first under DESC) valid.
		walkKeyset(t, ctx, pool, "uuid", func(i int) string {
			return fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		})
	})
}

// walkKeyset seeds a probe table whose id column is idType, then pages
// FindKeyset through it following NextCursor, and asserts full coverage with no
// dupes or gaps and a total (id-DESC) order across a run of rows that tie on
// created_at.
func walkKeyset(t *testing.T, ctx context.Context, pool *pgxpool.Pool, idType string, mkID func(int) string) {
	t.Helper()

	table := "keyset_it_probe_" + idType
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := pool.Exec(ctx,
		fmt.Sprintf(`CREATE TABLE %s (id %s PRIMARY KEY, created_at timestamptz NOT NULL)`, table, idType)); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer pool.Exec(ctx, `DROP TABLE IF EXISTS `+table)

	// 25 rows; three share a created_at (i=10..12) so the id tie-breaker must
	// carry the order — the exact case a non-unique sort key creates.
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var count int
	for i := 0; i < 25; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		if i >= 10 && i <= 12 {
			ts = base.Add(10 * time.Minute)
		}
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (id, created_at) VALUES ($1, $2)`, table), mkID(i), ts); err != nil {
			t.Fatalf("insert %s: %v", mkID(i), err)
		}
		count++
	}

	type ksRow struct {
		ID        string    `db:"id"`
		CreatedAt time.Time `db:"created_at"`
	}
	repo := New[ksRow](pool, WithTable[ksRow](table))

	// Page newest-first, following NextCursor until empty. A broken
	// string-vs-column seek would error on the very first Query.
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

	if len(got) != count {
		t.Fatalf("walked %d rows across pages, want %d (dupes or gaps): %v", len(got), count, got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("row %s returned twice — the seek is not stable: %v", id, got)
		}
		seen[id] = true
	}
	// The tied rows (i=10..12) must keep a total, id-DESC order.
	pos := map[string]int{}
	for i, id := range got {
		pos[id] = i
	}
	hi, mid, lo := mkID(12), mkID(11), mkID(10)
	if !(pos[hi] < pos[mid] && pos[mid] < pos[lo]) {
		t.Fatalf("tied rows out of id-DESC order: %s@%d %s@%d %s@%d", hi, pos[hi], mid, pos[mid], lo, pos[lo])
	}
}
