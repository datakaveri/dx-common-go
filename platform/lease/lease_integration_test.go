package lease_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
	"github.com/datakaveri/dx-common-go/platform/lease"
)

// ROADMAP P0-12. Every test here runs against real PostgreSQL, because the
// exclusion is the DATABASE's — the ON CONFLICT predicate and the owner-fenced
// UPDATE/DELETE are the mechanism, and a fake would be testing a reimplementation
// of them.
//
// containers.Postgres SKIPS, not fails, when Docker is unavailable.

const ddl = `
CREATE TABLE IF NOT EXISTS test_leases (
    name       text PRIMARY KEY,
    owner      text NOT NULL,
    expires_at timestamptz NOT NULL
);`

func newStore(t *testing.T) (*lease.Store, context.Context) {
	t.Helper()
	pg := containers.Postgres(t)
	ctx := context.Background()
	db, err := dxsql.Open(ctx, dxsql.Config{DSN: pg.DSN})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Exec(ctx, ddl); err != nil {
		t.Fatalf("provision schema: %v", err)
	}
	return lease.New(db, "test_leases"), ctx
}

func TestOnlyOneHolderAtATime(t *testing.T) {
	s, ctx := newStore(t)
	name := "session-" + t.Name()

	first, err := s.Acquire(ctx, name, time.Minute)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := s.Acquire(ctx, name, time.Minute); !errors.Is(err, lease.ErrHeld) {
		t.Fatalf("second acquire = %v, want ErrHeld", err)
	}

	// Released, so the next caller gets it.
	if err := first.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := s.Acquire(ctx, name, time.Minute); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

// TestConcurrentAcquireYieldsExactlyOneWinner is the property the whole package
// exists for, and the one a read-then-write implementation gets wrong under
// load rather than in a unit test.
func TestConcurrentAcquireYieldsExactlyOneWinner(t *testing.T) {
	s, ctx := newStore(t)
	name := "session-" + t.Name()

	const racers = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		held    int
	)
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, so they genuinely contend
			_, err := s.Acquire(ctx, name, time.Minute)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, lease.ErrHeld):
				held++
			default:
				t.Errorf("unexpected acquire error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("%d of %d racers acquired the same lease — the work would run %d times "+
			"concurrently", winners, racers, winners)
	}
	if held != racers-1 {
		t.Errorf("losers = %d, want %d", held, racers-1)
	}
}

// TestExpiryPermitsTakeoverExactlyOnce is the P0-12 acceptance criterion.
//
// Expiry is what stops a dead replica owning a session forever. It must not
// become a window in which several replicas each decide they may take over.
func TestExpiryPermitsTakeoverExactlyOnce(t *testing.T) {
	s, ctx := newStore(t)
	name := "session-" + t.Name()

	// A lease that is already expired stands in for a replica that died
	// holding one. A sub-second TTL keeps the test fast without sleeping on a
	// real timeout.
	if _, err := s.Acquire(ctx, name, 50*time.Millisecond); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	time.Sleep(120 * time.Millisecond)

	const racers = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
	)
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.Acquire(ctx, name, time.Minute); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("%d replicas took over one expired lease — takeover must be exactly once, "+
			"or expiry turns one stuck turn into several concurrent ones", winners)
	}
}

// TestAStaleOwnerCannotReleaseTheNewHolder is the fencing property, and the one
// whose absence would be worst: a released-by-name implementation passes every
// other test in this file and still puts two replicas on one session.
func TestAStaleOwnerCannotReleaseTheNewHolder(t *testing.T) {
	s, ctx := newStore(t)
	name := "session-" + t.Name()

	stale, err := s.Acquire(ctx, name, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	time.Sleep(120 * time.Millisecond)

	current, err := s.Acquire(ctx, name, time.Minute)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}

	// The stale holder is still running and still believes it owns the lease.
	if err := stale.Release(ctx); !errors.Is(err, lease.ErrLost) {
		t.Fatalf("stale Release = %v, want ErrLost", err)
	}

	// The current holder must STILL hold it. If the stale release had deleted
	// the row, this acquire would succeed and two replicas would be running.
	if _, err := s.Acquire(ctx, name, time.Minute); !errors.Is(err, lease.ErrHeld) {
		t.Fatal("a stale owner's Release freed the CURRENT holder's lease — two replicas " +
			"would now run the same session")
	}
	if err := current.Renew(ctx, time.Minute); err != nil {
		t.Errorf("the current holder lost its lease to a stale release: %v", err)
	}
}

// TestAStaleOwnerCannotRenew: the same fence on the other mutating path. A
// holder that paused past its expiry must not be able to extend the new owner's
// claim back to itself.
func TestAStaleOwnerCannotRenew(t *testing.T) {
	s, ctx := newStore(t)
	name := "session-" + t.Name()

	stale, err := s.Acquire(ctx, name, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := s.Acquire(ctx, name, time.Minute); err != nil {
		t.Fatalf("takeover: %v", err)
	}

	if err := stale.Renew(ctx, time.Minute); !errors.Is(err, lease.ErrLost) {
		t.Fatalf("stale Renew = %v, want ErrLost — a taken-over holder must learn to stop, "+
			"not quietly reclaim the lease", err)
	}
}

// TestRenewKeepsAHealthyHolder: the flip side. A holder that renews must not be
// taken over, or long work is impossible and the fix is worse than the defect.
func TestRenewKeepsAHealthyHolder(t *testing.T) {
	s, ctx := newStore(t)
	name := "session-" + t.Name()

	held, err := s.Acquire(ctx, name, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Work for longer than the TTL, renewing as a holder would.
	for range 4 {
		time.Sleep(60 * time.Millisecond)
		if err := held.Renew(ctx, 150*time.Millisecond); err != nil {
			t.Fatalf("renew: %v", err)
		}
	}
	if _, err := s.Acquire(ctx, name, time.Minute); !errors.Is(err, lease.ErrHeld) {
		t.Fatal("a renewing holder was taken over — long-running work could never hold a lease")
	}
}

func TestHolderReportsOwnership(t *testing.T) {
	s, ctx := newStore(t)
	name := "session-" + t.Name()

	if _, held, _ := s.Holder(ctx, name); held {
		t.Fatal("an unclaimed lease reports as held")
	}
	l, err := s.Acquire(ctx, name, time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	owner, held, err := s.Holder(ctx, name)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	if !held || owner != l.Owner() {
		t.Errorf("Holder = (%q, %v), want (%q, true)", owner, held, l.Owner())
	}
}
