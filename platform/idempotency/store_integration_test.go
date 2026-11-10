package idempotency_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
	"github.com/datakaveri/dx-common-go/platform/idempotency"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
)

// ROADMAP P0-11 / review finding H-09. These need a real PostgreSQL —
// containers.Postgres SKIPS rather than fails when Docker is unavailable —
// because the whole point of the primitive is that the DATABASE's uniqueness is
// the concurrency control, which an in-memory fake would not reproduce.

const schema = `
CREATE TABLE IF NOT EXISTS test_idempotency (
    scope        text NOT NULL,
    key          text NOT NULL,
    status       text NOT NULL DEFAULT 'executing',
    status_code  int,
    response     bytea,
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    PRIMARY KEY (scope, key)
);`

func newStore(t *testing.T) (*idempotency.Store, dxsql.DB) {
	t.Helper()
	h := containers.Postgres(t)
	ctx := context.Background()
	if _, err := h.Pool.Exec(ctx, schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := h.Pool.Exec(ctx, `TRUNCATE test_idempotency`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	db, err := dxsql.Open(ctx, dxsql.Config{DSN: h.DSN})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	return idempotency.New(db, "test_idempotency"), db
}

// TestBegin_FirstCallerOwnsTheKey: the first Begin reserves and returns "do the
// work", not a replay.
func TestBegin_FirstCallerOwnsTheKey(t *testing.T) {
	store, _ := newStore(t)
	rec, replay, err := store.Begin(context.Background(), "marketplace.order", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if replay || rec != nil {
		t.Fatalf("first caller must own the key, got replay=%v rec=%v", replay, rec)
	}
}

// TestBegin_CompletedKeyReplays is the "return the original result" objective:
// a repeat returns the stored response, and does NOT re-run the work.
func TestBegin_CompletedKeyReplays(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	_, _, err := store.Begin(ctx, "s", "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, "s", "k", 201, []byte(`{"order":"ord_1"}`)); err != nil {
		t.Fatal(err)
	}

	rec, replay, err := store.Begin(ctx, "s", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !replay {
		t.Fatal("a completed key must replay, not re-run the work")
	}
	if rec.StatusCode != 201 || string(rec.Response) != `{"order":"ord_1"}` {
		t.Errorf("replayed record = %d %q, want the stored response verbatim", rec.StatusCode, rec.Response)
	}
}

// TestBegin_InProgressKeyIsRejected: a second attempt while the first is still
// executing must NOT proceed — proceeding is the double-charge.
func TestBegin_InProgressKeyIsRejected(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	if _, _, err := store.Begin(ctx, "s", "k"); err != nil {
		t.Fatal(err)
	}
	// No Complete — the key is still executing.
	_, _, err := store.Begin(ctx, "s", "k")
	if !errors.Is(err, idempotency.ErrInProgress) {
		t.Fatalf("err = %v, want ErrInProgress — a duplicate must not run alongside an in-flight attempt", err)
	}
}

// TestBegin_ConcurrentCallersExactlyOneOwns is THE property, and it is the one a
// mutex would get subtly wrong. N goroutines Begin the same key at once; exactly
// one may own it, the rest must see ErrInProgress. The database's unique key is
// what makes this hold under real contention.
func TestBegin_ConcurrentCallersExactlyOneOwns(t *testing.T) {
	store, _ := newStore(t)

	const n = 12
	var owners atomic.Int32
	var inProgress atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec, replay, err := store.Begin(context.Background(), "s", "shared")
			switch {
			case err == nil && !replay && rec == nil:
				owners.Add(1)
			case errors.Is(err, idempotency.ErrInProgress):
				inProgress.Add(1)
			default:
				t.Errorf("unexpected outcome: rec=%v replay=%v err=%v", rec, replay, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if owners.Load() != 1 {
		t.Errorf("%d goroutines owned the key, want exactly 1", owners.Load())
	}
	if inProgress.Load() != n-1 {
		t.Errorf("%d saw ErrInProgress, want %d", inProgress.Load(), n-1)
	}
}

// TestComplete_RequiresAnExecutingRow: Complete on a key that is already
// completed (or was reclaimed) fails, so a late Complete from a crashed attempt
// cannot clobber a newer result.
func TestComplete_RequiresAnExecutingRow(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	_, _, _ = store.Begin(ctx, "s", "k")
	if err := store.Complete(ctx, "s", "k", 200, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, "s", "k", 200, []byte("second")); err == nil {
		t.Fatal("a second Complete must fail — the row is no longer executing")
	}

	rec, _, _ := store.Begin(ctx, "s", "k")
	if string(rec.Response) != "first" {
		t.Errorf("response = %q, want the first Complete to have won", rec.Response)
	}
}

// TestRelease_FreesAKeyForImmediateRetry: the failure-before-side-effect path.
func TestRelease_FreesAKeyForImmediateRetry(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	if _, _, err := store.Begin(ctx, "s", "k"); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, "s", "k"); err != nil {
		t.Fatal(err)
	}
	// After Release the key is free — a retry owns it fresh, not ErrInProgress.
	_, replay, err := store.Begin(ctx, "s", "k")
	if err != nil || replay {
		t.Fatalf("after Release a retry must own the key fresh, got replay=%v err=%v", replay, err)
	}
}

// TestReclaim_ClearsStaleExecutingRows: the reconciliation hook for keys
// stranded by a crash between side effect and Complete.
func TestReclaim_ClearsStaleExecutingRows(t *testing.T) {
	store, db := newStore(t)
	ctx := context.Background()

	if _, _, err := store.Begin(ctx, "s", "stale"); err != nil {
		t.Fatal(err)
	}
	// Backdate it past any plausible age.
	if _, err := db.Exec(ctx, `UPDATE test_idempotency SET created_at = now() - interval '1 hour' WHERE key = 'stale'`); err != nil {
		t.Fatal(err)
	}

	n, err := store.Reclaim(ctx, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}
	// A completed row of the same age must NOT be reclaimed.
	_, _, _ = store.Begin(ctx, "s", "done")
	_ = store.Complete(ctx, "s", "done", 200, nil)
	if _, err := db.Exec(ctx, `UPDATE test_idempotency SET created_at = now() - interval '1 hour' WHERE key = 'done'`); err != nil {
		t.Fatal(err)
	}
	n, _ = store.Reclaim(ctx, 5*time.Minute)
	if n != 0 {
		t.Errorf("reclaimed %d completed rows, want 0 — a completed key is a durable result, not stale work", n)
	}
}
