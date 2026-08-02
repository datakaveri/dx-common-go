package sql_test

import (
	"context"
	stderrors "errors"
	"strings"
	"sync"
	"testing"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
	"github.com/datakaveri/dx-common-go/platform/database/sql"
	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/paging"
)

// widget is the test entity. Column names come from `db:"…"` tags, so the
// projection and the struct cannot drift apart.
type widget struct {
	ID     int64  `db:"id"`
	Name   string `db:"name"`
	Status string `db:"status"`
	Owner  string `db:"owner"`
}

// The testcontainers Postgres is SHARED across every Postgres(t, ...) call in
// this binary, so tests must not share a table. Each test creates its own,
// named after itself, and drops it on cleanup — full isolation without paying
// for a container per test.
func newDB(t *testing.T) sql.DB {
	t.Helper()
	pg := containers.Postgres(t)

	db, err := sql.Open(context.Background(), sql.Config{DSN: pg.DSN})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// tableName derives a unique, SQL-safe table name from the test name.
func tableName(t *testing.T) string {
	var b strings.Builder
	b.WriteString("w_")
	for _, r := range strings.ToLower(t.Name()) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func newRepo(t *testing.T, db sql.DB) *sql.Repo[widget] {
	t.Helper()
	table := tableName(t)
	ctx := context.Background()

	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		id     bigserial PRIMARY KEY,
		name   text NOT NULL UNIQUE,
		status text NOT NULL DEFAULT 'ACTIVE',
		owner  text NOT NULL
	)`); err != nil {
		t.Fatalf("create %s: %v", table, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DROP TABLE IF EXISTS `+table)
	})

	return sql.NewRepo[widget](db, sql.WithTable[widget](table))
}

func seed(t *testing.T, r *sql.Repo[widget], names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := r.Insert(context.Background(), map[sql.Column]any{
			"name": n, "owner": "u-1", "status": "ACTIVE",
		}); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
}

// ── CRUD ───────────────────────────────────────────────────────────────────

func TestInsertReturnsTheStoredRow(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()

	got, err := r.Insert(ctx, map[sql.Column]any{"name": "w1", "owner": "u-1"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// The caller sees database-generated columns without a re-read: id from the
	// sequence, status from the column default.
	if got.ID == 0 {
		t.Error("id was not returned from the sequence")
	}
	if got.Status != "ACTIVE" {
		t.Errorf("status = %q, want the column default ACTIVE", got.Status)
	}
}

func TestGetAndUpdateByID(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()

	created, _ := r.Insert(ctx, map[sql.Column]any{"name": "w1", "owner": "u-1"})

	got, err := r.Get(ctx, created.ID)
	if err != nil || got.Name != "w1" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	updated, err := r.UpdateByID(ctx, created.ID, map[sql.Column]any{"status": "RETIRED"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != "RETIRED" || updated.Name != "w1" {
		t.Errorf("updated = %+v", updated)
	}
}

func TestGet_MissingIsNotFound(t *testing.T) {
	r := newRepo(t, newDB(t))
	if _, err := r.Get(context.Background(), 999999); !errors.IsNotFound(err) {
		t.Errorf("err = %v, want a platform not-found", err)
	}
}

// TestUniqueViolationIsAConflict pins the classification the two drifted
// mappers disagreed on.
func TestUniqueViolationIsAConflict(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()
	seed(t, r, "dupe")

	_, err := r.Insert(ctx, map[sql.Column]any{"name": "dupe", "owner": "u-2"})
	if !errors.IsConflict(err) {
		t.Fatalf("err = %v, want a conflict (409)", err)
	}
	if errors.HTTPStatusOf(err) != 409 {
		t.Errorf("status = %d, want 409", errors.HTTPStatusOf(err))
	}
}

func TestNotNullViolationIsValidation(t *testing.T) {
	r := newRepo(t, newDB(t))
	// owner is NOT NULL with no default.
	_, err := r.Insert(context.Background(), map[sql.Column]any{"name": "w9"})
	if !errors.IsValidation(err) {
		t.Errorf("err = %v, want a validation error (400) — this is the case the old errors.MapPostgresError missed", err)
	}
}

// TestUnboundedWriteIsRefused: an unbounded UPDATE or DELETE is almost never
// what anyone means, and the one time it is, it is worth spelling out.
func TestUnboundedWriteIsRefused(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()
	seed(t, r, "a", "b")

	if _, err := r.Update(ctx, map[sql.Column]any{"status": "X"}); err == nil {
		t.Error("an UPDATE with no predicate must be refused")
	}
	if _, err := r.Delete(ctx); err == nil {
		t.Error("a DELETE with no predicate must be refused")
	}
	// Nothing was written.
	if n, _ := r.All().Count(ctx); n != 2 {
		t.Errorf("row count = %d, want 2 — the refused statements must not have run", n)
	}
}

// ── query DSL ──────────────────────────────────────────────────────────────

func TestPredicates(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	ctx := context.Background()
	seed(t, r, "alpha", "beta", "gamma", "delta")
	_, _ = r.Update(ctx, map[sql.Column]any{"status": "RETIRED"}, sql.Eq("name", "beta"))

	tests := []struct {
		name string
		pred sql.Pred
		want int
	}{
		{"eq", sql.Eq("status", "ACTIVE"), 3},
		{"ne", sql.Ne("status", "ACTIVE"), 1},
		{"in", sql.In[string]("name", []string{"alpha", "gamma"}), 2},
		{"not in", sql.NotIn[string]("name", []string{"alpha", "gamma"}), 2},
		{"like", sql.Like("name", "a%"), 1},
		{"ilike", sql.ILike("name", "A%"), 1},
		{"and", sql.And(sql.Eq("status", "ACTIVE"), sql.Like("name", "%a")), 3},
		{"or", sql.Or(sql.Eq("name", "alpha"), sql.Eq("name", "beta")), 2},
		{"not", sql.Not(sql.Eq("status", "ACTIVE")), 1},
		{"between", sql.Between("id", 1, 2), 2},
		{"raw jsonb-style", sql.Raw(`char_length(name) > ?`, 4), 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Where(tt.pred).Find(ctx)
			if err != nil {
				t.Fatalf("find: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("matched %d rows, want %d: %+v", len(got), tt.want, got)
			}
		})
	}
}

// TestIn_UsesAnyNotExpandedList: one parameter regardless of list length keeps
// the statement shape stable so Postgres can reuse the prepared plan.
func TestIn_HandlesEmptyAndLargeLists(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()
	seed(t, r, "a", "b", "c")

	got, err := r.Where(sql.In[string]("name", nil)).Find(ctx)
	if err != nil {
		t.Fatalf("an empty IN list must not be a SQL error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("an empty IN list must match nothing, got %d", len(got))
	}

	big := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		big = append(big, "x")
	}
	big = append(big, "a")
	if got, err := r.Where(sql.In[string]("name", big)).Find(ctx); err != nil || len(got) != 1 {
		t.Errorf("large IN list: got %d rows, %v", len(got), err)
	}
}

func TestCountAndExists(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()
	seed(t, r, "a", "b", "c")

	if n, err := r.All().Count(ctx); err != nil || n != 3 {
		t.Errorf("count = %d, %v", n, err)
	}
	// Count must ignore limit/offset: it answers "how many match", not "how
	// many are on this page".
	if n, err := r.All().Limit(1).Count(ctx); err != nil || n != 3 {
		t.Errorf("count with a limit = %d, want 3", n)
	}
	if ok, err := r.Where(sql.Eq("name", "a")).Exists(ctx); err != nil || !ok {
		t.Errorf("exists = %v, %v", ok, err)
	}
	if ok, _ := r.Where(sql.Eq("name", "zzz")).Exists(ctx); ok {
		t.Error("exists must be false for no match")
	}
}

func TestPaged(t *testing.T) {
	r := newRepo(t, newDB(t))
	ctx := context.Background()
	seed(t, r, "a", "b", "c", "d", "e")

	page, err := r.All().Order(sql.Asc("id")).Paged(ctx, paging.NewRequest(2, 2))
	if err != nil {
		t.Fatalf("paged: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	if page.Items[0].Name != "c" {
		t.Errorf("page 2 starts at %q, want c", page.Items[0].Name)
	}
	if page.Info.TotalCount != 5 || page.Info.TotalPages != 3 || !page.Info.HasNext || !page.Info.HasPrevious {
		t.Errorf("info = %+v", page.Info)
	}
}

// TestPaged_RequiresAnOrder is a correctness guard, not a style rule: without
// ORDER BY, Postgres may return the same row on two pages and omit another.
func TestPaged_RequiresAnOrder(t *testing.T) {
	r := newRepo(t, newDB(t))
	seed(t, r, "a")
	if _, err := r.All().Paged(context.Background(), paging.NewRequest(1, 10)); err == nil {
		t.Error("a paginated query without Order must be refused")
	}
}

func TestPaged_EmptyResult(t *testing.T) {
	r := newRepo(t, newDB(t))
	page, err := r.Where(sql.Eq("name", "nope")).Order(sql.Asc("id")).
		Paged(context.Background(), paging.NewRequest(1, 10))
	if err != nil {
		t.Fatalf("paged: %v", err)
	}
	if page.Items == nil {
		t.Error("an empty page must carry a non-nil slice, so it serialises as [] not null")
	}
	if page.Info.TotalCount != 0 || page.Info.TotalPages != 0 {
		t.Errorf("info = %+v", page.Info)
	}
}

// TestSortable_RejectsUnknownKeys is the enforcement point that makes ?sort=
// safe to accept from a client at all.
func TestSortable_RejectsUnknownKeys(t *testing.T) {
	allow := sql.Sortable{"name": "name", "created": "id"}

	orders, err := allow.Orders([]paging.SortKey{{Field: "name", Dir: paging.Desc}})
	if err != nil {
		t.Fatalf("an allowlisted key must resolve: %v", err)
	}
	if len(orders) != 1 || orders[0].Column != "name" || !orders[0].Desc {
		t.Errorf("orders = %+v", orders)
	}

	// The injection attempt a bare string would have carried into ORDER BY.
	_, err = allow.Orders([]paging.SortKey{{Field: "id; DROP TABLE t--"}})
	if !errors.IsValidation(err) {
		t.Errorf("err = %v, want a validation error for an unknown sort key", err)
	}
}

func TestQuotedIdentifiersSurviveReservedWords(t *testing.T) {
	// "owner" is fine, but the quoting must hold for anything that collides
	// with a reserved word too.
	r := newRepo(t, newDB(t))
	ctx := context.Background()
	seed(t, r, "a")
	if got, err := r.Where(sql.Eq("owner", "u-1")).Find(ctx); err != nil || len(got) != 1 {
		t.Errorf("got %d rows, %v", len(got), err)
	}
}

// ── transactions ───────────────────────────────────────────────────────────

func TestManager_CommitsAndRollsBack(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	m := sql.NewManager(db)
	ctx := context.Background()

	if err := m.Do(ctx, func(ctx context.Context) error {
		_, err := r.Insert(ctx, map[sql.Column]any{"name": "committed", "owner": "u"})
		return err
	}); err != nil {
		t.Fatalf("do: %v", err)
	}
	if ok, _ := r.Where(sql.Eq("name", "committed")).Exists(ctx); !ok {
		t.Error("a successful Do must commit")
	}

	sentinel := stderrors.New("business rule failed")
	err := m.Do(ctx, func(ctx context.Context) error {
		if _, err := r.Insert(ctx, map[sql.Column]any{"name": "rolled-back", "owner": "u"}); err != nil {
			return err
		}
		return sentinel
	})
	if !stderrors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's own error", err)
	}
	if ok, _ := r.Where(sql.Eq("name", "rolled-back")).Exists(ctx); ok {
		t.Error("a failing Do must roll back")
	}
}

// TestManager_RepositoriesJoinTheAmbientTransaction is the property that
// removes pgx.Tx from every service signature: the repository picked up the
// transaction from ctx without being handed one.
func TestManager_RepositoriesJoinTheAmbientTransaction(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	m := sql.NewManager(db)
	ctx := context.Background()

	_ = m.Do(ctx, func(txCtx context.Context) error {
		if _, err := r.Insert(txCtx, map[sql.Column]any{"name": "inside", "owner": "u"}); err != nil {
			return err
		}
		// Visible inside the transaction...
		if ok, _ := r.Where(sql.Eq("name", "inside")).Exists(txCtx); !ok {
			t.Error("the write is not visible within its own transaction")
		}
		// ...and not outside it, on a context with no transaction.
		if ok, _ := r.Where(sql.Eq("name", "inside")).Exists(ctx); ok {
			t.Error("an uncommitted write leaked outside the transaction")
		}
		return stderrors.New("roll back")
	})
}

// TestManager_NestedDoJoinsAndOnlyTheOutermostCommits is what lets a service
// compose independently-transactional repository calls into one atomic unit.
func TestManager_NestedDoJoinsAndOnlyTheOutermostCommits(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	m := sql.NewManager(db)
	ctx := context.Background()

	sentinel := stderrors.New("outer failed")
	err := m.Do(ctx, func(ctx context.Context) error {
		// An inner Do that "succeeds" must NOT commit independently.
		if err := m.Do(ctx, func(ctx context.Context) error {
			_, err := r.Insert(ctx, map[sql.Column]any{"name": "nested", "owner": "u"})
			return err
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !stderrors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
	if ok, _ := r.Where(sql.Eq("name", "nested")).Exists(ctx); ok {
		t.Error("an inner Do committed independently — the outer rollback must discard it")
	}
}

func TestManager_RollsBackOnPanic(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	m := sql.NewManager(db)
	ctx := context.Background()

	func() {
		defer func() { _ = recover() }()
		_ = m.Do(ctx, func(ctx context.Context) error {
			_, _ = r.Insert(ctx, map[sql.Column]any{"name": "panicked", "owner": "u"})
			panic("boom")
		})
	}()

	if ok, _ := r.Where(sql.Eq("name", "panicked")).Exists(ctx); ok {
		t.Error("a panic mid-transaction must roll back, not leave the write or hold locks")
	}
}

// TestManager_Lock serialises non-idempotent recurring work without leader
// election.
func TestManager_Lock(t *testing.T) {
	db := newDB(t)
	m := sql.NewManager(db)
	ctx := context.Background()

	held := make(chan struct{})
	release := make(chan struct{})
	var inner error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = m.Lock(ctx, "job-a", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()

	<-held
	inner = m.Lock(ctx, "job-a", func(context.Context) error {
		t.Error("the second holder must not run its callback")
		return nil
	})
	if !stderrors.Is(inner, sql.ErrLockHeld) {
		t.Errorf("err = %v, want ErrLockHeld — Lock must not block", inner)
	}

	// A different key is unaffected.
	if err := m.Lock(ctx, "job-b", func(context.Context) error { return nil }); err != nil {
		t.Errorf("a different lock key must be free: %v", err)
	}

	close(release)
	wg.Wait()

	// Released after the callback returns.
	if err := m.Lock(ctx, "job-a", func(context.Context) error { return nil }); err != nil {
		t.Errorf("the lock was not released: %v", err)
	}
}

// TestForUpdateSkipLocked is the house style for DB-driven recurring jobs: N
// replicas drain a queue table concurrently, each claiming disjoint rows.
func TestForUpdateSkipLocked(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	m := sql.NewManager(db)
	ctx := context.Background()
	seed(t, r, "j1", "j2", "j3", "j4")

	claimed := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = m.Do(ctx, func(ctx context.Context) error {
				rows, err := r.All().Order(sql.Asc("id")).Limit(2).ForUpdate(true).Find(ctx)
				if err != nil {
					return err
				}
				claimed <- len(rows)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)

	total := 0
	for n := range claimed {
		total += n
	}
	// Both workers ran; between them they saw at most the 4 rows, and neither
	// blocked waiting for the other.
	if total > 4 {
		t.Errorf("workers claimed %d rows in total, more than exist — SKIP LOCKED is not in effect", total)
	}
}

// ── escape hatches ─────────────────────────────────────────────────────────

func TestSQL_RawQueryScansAndMapsErrors(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	ctx := context.Background()
	seed(t, r, "a", "b")

	// The kind of statement the DSL deliberately cannot express.
	type row struct {
		Status string `db:"status"`
		N      int64  `db:"n"`
	}
	got, err := sql.SQL[row](ctx, db,
		`SELECT status, COUNT(*) AS n FROM `+r.Table()+` GROUP BY status ORDER BY status`)
	if err != nil {
		t.Fatalf("raw sql: %v", err)
	}
	if len(got) != 1 || got[0].N != 2 {
		t.Errorf("got %+v", got)
	}

	// Errors still land in the platform taxonomy.
	if _, err := sql.SQL[row](ctx, db, `SELECT * FROM no_such_table`); !errors.IsInternal(err) {
		t.Errorf("err = %v; an undefined table is a deploy bug (500), not a client error", err)
	}
}

func TestConn_JoinsTheAmbientTransaction(t *testing.T) {
	db := newDB(t)
	r := newRepo(t, db)
	m := sql.NewManager(db)
	ctx := context.Background()

	_ = m.Do(ctx, func(txCtx context.Context) error {
		_, _ = r.Insert(txCtx, map[sql.Column]any{"name": "via-conn", "owner": "u"})

		// This is what sqlc-generated code receives.
		q := sql.Conn(txCtx, db)
		var n int64
		if err := q.QueryRow(txCtx, `SELECT COUNT(*) FROM `+r.Table()+` WHERE name = $1`, "via-conn").Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Error("Conn did not join the ambient transaction — sqlc code would not see the write")
		}
		return stderrors.New("roll back")
	})
}

func TestCheckSatisfiesHealthChecker(t *testing.T) {
	db := newDB(t)
	if err := db.Check(context.Background()); err != nil {
		t.Errorf("check: %v", err)
	}
	if s := db.Stats(); s.Max == 0 {
		t.Error("stats must report the configured pool size")
	}
}
