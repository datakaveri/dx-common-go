package events_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/platform/events"
)

// ROADMAP P0-5 / review finding H-03: an outbox row is published by exactly one
// dispatcher at a time, across replicas, restarts and retries.
//
// The defect these prove was NOT in the SQL — FOR UPDATE SKIP LOCKED does take
// disjoint rows. It was in the lifetime: the locks are released when the claim
// STATEMENT ends, publication happens afterwards, and sent_at is still NULL, so
// the window between claim and mark-sent was completely unprotected.
//
// Every test here needs a real PostgreSQL. containers.Postgres SKIPS rather
// than fails when Docker is unavailable.

// countingBus records what it was asked to publish, and how often.
type countingBus struct {
	mu      sync.Mutex
	byID    map[string]int
	delay   time.Duration
	publish func(id string)
}

func newCountingBus(delay time.Duration) *countingBus {
	return &countingBus{byID: map[string]int{}, delay: delay}
}

func (b *countingBus) Publish(_ context.Context, _ string, e events.Event) error {
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	b.mu.Lock()
	b.byID[e.ID]++
	b.mu.Unlock()
	if b.publish != nil {
		b.publish(e.ID)
	}
	return nil
}

func (b *countingBus) Subscribe(string, string, events.Handler) error { return nil }
func (b *countingBus) Close() error                                   { return nil }

func (b *countingBus) duplicates() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	dup := map[string]int{}
	for id, n := range b.byID {
		if n > 1 {
			dup[id] = n
		}
	}
	return dup
}

func (b *countingBus) total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.byID)
}

// TestConcurrentDispatchersPublishEachRowOnce is the acceptance criterion:
// "With N concurrent dispatchers, every row has at most one active owner at any
// instant."
//
// The publish is deliberately SLOW. A fast publish closes the claim→mark-sent
// window by luck and the old implementation would pass; the delay holds the
// window open, which is where the defect lived.
func TestConcurrentDispatchersPublishEachRowOnce(t *testing.T) {
	db, outbox, mgr := setup(t)
	ctxSeed := context.Background()

	const rows = 40
	for i := range rows {
		id := fmt.Sprintf("evt-%02d", i)
		err := mgr.Do(ctxSeed, func(ctx context.Context) error {
			return events.Publish(ctx, outbox, policyTopic, policyCreated{PolicyID: id})
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	bus := newCountingBus(20 * time.Millisecond)

	// Six dispatchers, each with its OWN Outbox — separate owner ids, exactly
	// as six replicas would have.
	const dispatchers = 6
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for range dispatchers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := events.NewDispatcher(events.NewOutbox(db, "test_outbox"), bus, 10, testLogger{t})
			for {
				n, err := d.DrainOnce(ctx)
				if err != nil || n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()

	if dup := bus.duplicates(); len(dup) > 0 {
		t.Errorf("%d row(s) published more than once by concurrent dispatchers: %v", len(dup), dup)
	}
	if got := bus.total(); got != rows {
		t.Errorf("published %d distinct rows, want %d", got, rows)
	}
}

// TestExpiredLeaseIsReclaimed: "A dispatcher crash after claiming releases the
// row after lease expiry and no earlier."
func TestExpiredLeaseIsReclaimed(t *testing.T) {
	db, _, mgr := setup(t)
	short := events.NewOutbox(db, "test_outbox", events.WithLease(time.Second))
	err := mgr.Do(context.Background(), func(ctx context.Context) error {
		return events.Publish(ctx, short, policyTopic, policyCreated{PolicyID: "abandoned"})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Claim and then "crash": never publish, never mark sent.
	crashed := short
	if n, err := crashed.ClaimForTest(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("first claim: n=%d err=%v", n, err)
	}

	// Before expiry the row is NOT claimable — that is the exclusivity.
	other := events.NewOutbox(db, "test_outbox", events.WithLease(time.Second))
	if n, err := other.ClaimForTest(context.Background(), 10); err != nil || n != 0 {
		t.Fatalf("a leased row was re-claimed before its lease expired: n=%d err=%v", n, err)
	}

	time.Sleep(1500 * time.Millisecond)

	if n, err := other.ClaimForTest(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("an EXPIRED lease must be reclaimable, or a crashed replica strands the event forever: n=%d err=%v", n, err)
	}
}

type testLogger struct{ t *testing.T }

func (l testLogger) Warn(msg string, kv ...any)  { l.t.Logf("WARN %s %v", msg, kv) }
func (l testLogger) Error(msg string, kv ...any) { l.t.Logf("ERROR %s %v", msg, kv) }
