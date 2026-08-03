package events_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
	"github.com/datakaveri/dx-common-go/platform/events"
)

// The outbox exists for exactly one property: an event is as durable as the
// domain change that caused it, because they share a transaction. These tests
// assert that property against a real Postgres.
//
// containers.Postgres SKIPS, not fails, when Docker is unavailable.

const outboxSchema = `
CREATE TABLE IF NOT EXISTS test_outbox (
    id         text PRIMARY KEY,
    topic      text NOT NULL,
    payload    jsonb NOT NULL,
    attempts   int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);
CREATE TABLE IF NOT EXISTS test_policy (
    id text PRIMARY KEY
);`

func newDB(t *testing.T, dsn string) dxsql.DB {
	t.Helper()
	db, err := dxsql.Open(context.Background(), dxsql.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func setup(t *testing.T) (dxsql.DB, *events.Outbox, dxsql.Manager) {
	t.Helper()
	h := containers.Postgres(t)
	if _, err := h.Pool.Exec(context.Background(), outboxSchema); err != nil {
		t.Fatalf("provision schema: %v", err)
	}
	// containers.Postgres reuses one container across the package, so tests
	// share these tables. Without truncating, rows left by an earlier test are
	// counted by a later one's assertions — which is exactly the false failure
	// this cost an hour to.
	if _, err := h.Pool.Exec(context.Background(), `TRUNCATE test_outbox, test_policy`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	db := newDB(t, h.DSN)
	return db, events.NewOutbox(db, "test_outbox"), dxsql.NewManager(db)
}

// THE property: when the domain write rolls back, the event goes with it.
// Publishing outside the transaction would have announced a policy that does
// not exist.
func TestOutboxRollsBackWithTheDomainWrite(t *testing.T) {
	db, ob, mgr := setup(t)
	ctx := context.Background()
	boom := errors.New("boom")

	err := mgr.Do(ctx, func(ctx context.Context) error {
		if _, err := dxsql.Conn(ctx, db).Exec(ctx, `INSERT INTO test_policy (id) VALUES ($1)`, "p-1"); err != nil {
			return err
		}
		if err := events.Publish(ctx, ob, policyTopic, policyCreated{PolicyID: "p-1"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	if n := count(t, db, `SELECT count(*) FROM test_policy`); n != 0 {
		t.Errorf("policy rows = %d, want 0", n)
	}
	if n := count(t, db, `SELECT count(*) FROM test_outbox`); n != 0 {
		t.Error("the event survived a rolled-back write — it would announce a policy that does not exist")
	}
}

// And the converse: on commit, both land.
func TestOutboxCommitsWithTheDomainWrite(t *testing.T) {
	db, ob, mgr := setup(t)
	ctx := context.Background()

	if err := mgr.Do(ctx, func(ctx context.Context) error {
		if _, err := dxsql.Conn(ctx, db).Exec(ctx, `INSERT INTO test_policy (id) VALUES ($1)`, "p-2"); err != nil {
			return err
		}
		return events.Publish(ctx, ob, policyTopic, policyCreated{PolicyID: "p-2"})
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if n := count(t, db, `SELECT count(*) FROM test_policy`); n != 1 {
		t.Errorf("policy rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT count(*) FROM test_outbox WHERE sent_at IS NULL`); n != 1 {
		t.Errorf("pending outbox rows = %d, want 1", n)
	}
}

// The dispatcher publishes pending rows and marks them sent.
func TestDispatcherDrains(t *testing.T) {
	db, ob, mgr := setup(t)
	ctx := context.Background()

	for _, id := range []string{"p-1", "p-2", "p-3"} {
		if err := mgr.Do(ctx, func(ctx context.Context) error {
			return events.Publish(ctx, ob, policyTopic, policyCreated{PolicyID: id})
		}); err != nil {
			t.Fatal(err)
		}
	}

	bus := events.NewMemory()
	// Mutex-guarded: the handler runs on the dispatcher's goroutine while the
	// assertion polls from the test's.
	var mu sync.Mutex
	var delivered []string
	_ = policyTopic.Subscribe(bus, "g", func(_ context.Context, p policyCreated) error {
		mu.Lock()
		defer mu.Unlock()
		delivered = append(delivered, p.PolicyID)
		return nil
	})

	d := events.NewDispatcher(ob, bus, 10, nil)
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d.Kick()
	go func() { _ = d.Run(runCtx, 50*time.Millisecond) }()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(delivered) == 3
	}, "3 events delivered")

	if n := count(t, db, `SELECT count(*) FROM test_outbox WHERE sent_at IS NULL`); n != 0 {
		t.Errorf("pending rows after drain = %d, want 0", n)
	}
}

// A publish failure must leave the row PENDING, not mark it sent. Marking it
// sent would silently lose the event.
func TestFailedPublishLeavesTheRowPending(t *testing.T) {
	db, ob, mgr := setup(t)
	ctx := context.Background()

	if err := mgr.Do(ctx, func(ctx context.Context) error {
		return events.Publish(ctx, ob, policyTopic, policyCreated{PolicyID: "p-1"})
	}); err != nil {
		t.Fatal(err)
	}

	d := events.NewDispatcher(ob, failingBus{}, 10, nil)
	runCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	d.Kick()
	_ = d.Run(runCtx, 50*time.Millisecond)

	if n := count(t, db, `SELECT count(*) FROM test_outbox WHERE sent_at IS NULL`); n != 1 {
		t.Error("a failed publish was marked sent; the event is lost")
	}
	// The attempt counter must advance, or a poison row is indistinguishable
	// from a fresh one.
	if n := count(t, db, `SELECT count(*) FROM test_outbox WHERE attempts > 0`); n != 1 {
		t.Error("attempts not incremented; a stuck row cannot be identified")
	}
}

type failingBus struct{}

func (failingBus) Publish(context.Context, string, events.Event) error {
	return errors.New("broker down")
}
func (failingBus) Subscribe(string, string, events.Handler) error { return nil }
func (failingBus) Close() error                                   { return nil }

func count(t *testing.T, db dxsql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
