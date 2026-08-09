package amqp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"

	"github.com/datakaveri/dx-common-go/dxtest/containers"
	"github.com/datakaveri/dx-common-go/platform/events"
)

// ROADMAP P0-6, the last acceptance criterion: "an operator can replay
// quarantined messages after remediation and observe the projection converge."
//
// These run against a REAL broker, because every property under test is broker
// behaviour rather than ours: that a rejected message reaches "<queue>.dlq",
// that it arrives carrying an x-death header naming the ORIGINAL routing key,
// and that republishing to that key routes it back to the consumer. A fake
// would assert our assumptions about AMQP back at us — and the original P0-6
// defect was precisely a wrong assumption about what happens to a message
// nobody acknowledges.
//
// containers.RabbitMQURL SKIPS, not fails, when Docker is unavailable.

// collector is a consumer that quarantines everything until it is "fixed",
// then accepts. It stands in for deploying a compatible reader.
type collector struct {
	mu       sync.Mutex
	fixed    bool
	accepted []events.Event
	seen     int
}

func (c *collector) handle(_ context.Context, e events.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen++
	if !c.fixed {
		return events.ErrQuarantine
	}
	c.accepted = append(c.accepted, e)
	return nil
}

func (c *collector) fix() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fixed = true
}

func (c *collector) snapshot() (accepted []events.Event, seen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.accepted...), c.seen
}

func newReplayBus(t *testing.T, exchange string) *Bus {
	t.Helper()
	bus, err := Open(Config{URL: containers.RabbitMQURL(t), Exchange: exchange, MaxAttempts: 1})
	if err != nil {
		t.Fatalf("open bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus.WithLogger(zap.NewNop())
}

// waitForQueue blocks until the subscription's queue exists.
//
// Subscribe starts its consumer on a goroutine and the queue is declared inside
// it, so Subscribe returning does NOT mean the queue is bound. Publishing
// before the binding exists sends the message to an exchange with no matching
// queue, and a topic exchange silently discards it — the test then times out
// waiting for a message that was never routed anywhere. This is a race in the
// TEST, not the bus: a real producer and consumer are separate processes and a
// message published before any consumer subscribes is legitimately dropped.
func waitForQueue(t *testing.T, bus *Bus, topic, group string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ch, closeAdmin, err := bus.adminChannel()
		if err == nil {
			_, derr := ch.QueueDeclarePassive(queueName(topic, group), true, false, false, false, nil)
			closeAdmin()
			if derr == nil {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for queue %s to be declared", queueName(topic, group))
}

// waitFor polls until cond holds, so a test never sleeps a fixed guess at
// broker latency.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestQuarantinedEventIsReplayedAndConverges is the acceptance criterion, end
// to end: an event a consumer cannot read is quarantined with its body intact,
// survives there, and after the reader is fixed a replay delivers it.
func TestQuarantinedEventIsReplayedAndConverges(t *testing.T) {
	const (
		topic    = "policy.created"
		group    = "replay-converge"
		exchange = "dxtest-replay-converge"
	)
	bus := newReplayBus(t, exchange)
	ctx := context.Background()

	c := &collector{}
	if err := bus.Subscribe(topic, group, c.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	waitForQueue(t, bus, topic, group)

	want := events.Event{ID: "evt-1", Type: topic, Version: 2, Payload: json.RawMessage(`{"policy":"p-1"}`)}
	if err := bus.Publish(ctx, topic, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// It must reach the DLQ rather than vanish — the whole of P0-6's first half.
	var quarantined []QuarantinedMessage
	waitFor(t, "the event to be quarantined", func() bool {
		got, err := bus.Inspect(ctx, topic, group, 10)
		if err != nil {
			return false
		}
		quarantined = got
		return len(got) == 1
	})

	q := quarantined[0]
	if q.Event.ID != want.ID {
		t.Errorf("quarantined event id = %q, want %q — the BODY must survive intact", q.Event.ID, want.ID)
	}
	if q.OriginalRoutingKey != topic {
		t.Fatalf("original routing key = %q, want %q; without it replay cannot route the "+
			"message back and would silently deliver it nowhere", q.OriginalRoutingKey, topic)
	}

	// Inspect must be non-destructive, or an operator looking at a quarantine
	// would consume it.
	again, err := bus.Inspect(ctx, topic, group, 10)
	if err != nil {
		t.Fatalf("second inspect: %v", err)
	}
	if len(again) != 1 {
		t.Fatalf("after Inspect the DLQ holds %d messages, want 1 — inspection consumed it", len(again))
	}

	// Remediation: the compatible reader ships.
	c.fix()

	res, err := bus.Replay(ctx, topic, group, ReplayOptions{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Replayed != 1 {
		t.Fatalf("replayed = %d, want 1 (skipped %d)", res.Replayed, len(res.Skipped))
	}

	// THE criterion: the projection converges.
	waitFor(t, "the replayed event to be accepted", func() bool {
		accepted, _ := c.snapshot()
		return len(accepted) == 1
	})
	accepted, _ := c.snapshot()
	if accepted[0].ID != want.ID {
		t.Errorf("accepted event id = %q, want %q", accepted[0].ID, want.ID)
	}
	if string(accepted[0].Payload) != string(want.Payload) {
		t.Errorf("payload = %s, want %s — the body must round-trip through quarantine unchanged",
			accepted[0].Payload, want.Payload)
	}

	// And the quarantine is empty, so its depth is a usable alert signal again.
	waitFor(t, "the DLQ to drain", func() bool {
		got, ierr := bus.Inspect(ctx, topic, group, 10)
		return ierr == nil && len(got) == 0
	})
}

// TestReplayIntoAnUnfixedConsumerStopsAtMaxReplays is the loop guard.
//
// Replaying before remediation re-quarantines the message. Without a bound, an
// operator retrying a DLQ that is still broken loops forever, burning the
// broker and destroying the depth signal. The replay counter makes the loop
// visible and MaxReplays stops it.
func TestReplayIntoAnUnfixedConsumerStopsAtMaxReplays(t *testing.T) {
	const (
		topic    = "policy.created"
		group    = "replay-loop"
		exchange = "dxtest-replay-loop"
	)
	bus := newReplayBus(t, exchange)
	ctx := context.Background()

	c := &collector{} // never fixed
	if err := bus.Subscribe(topic, group, c.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	waitForQueue(t, bus, topic, group)
	if err := bus.Publish(ctx, topic, events.Event{ID: "evt-loop", Type: topic, Version: 2}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the event to be quarantined", func() bool {
		got, err := bus.Inspect(ctx, topic, group, 10)
		return err == nil && len(got) == 1
	})

	// Replay repeatedly into a consumer that still cannot read it. Each attempt
	// republishes, the consumer quarantines again, and the counter climbs.
	for attempt := 1; attempt <= DefaultMaxReplays; attempt++ {
		res, err := bus.Replay(ctx, topic, group, ReplayOptions{})
		if err != nil {
			t.Fatalf("replay %d: %v", attempt, err)
		}
		if res.Replayed != 1 {
			t.Fatalf("replay %d: replayed = %d, want 1", attempt, res.Replayed)
		}
		waitFor(t, "the event to be re-quarantined", func() bool {
			got, ierr := bus.Inspect(ctx, topic, group, 10)
			return ierr == nil && len(got) == 1 && got[0].Replays == int64(attempt)
		})
	}

	// The next attempt must REFUSE to move it, and say which message.
	res, err := bus.Replay(ctx, topic, group, ReplayOptions{})
	if err != nil {
		t.Fatalf("final replay: %v", err)
	}
	if res.Replayed != 0 {
		t.Errorf("replayed = %d, want 0 — a message replayed %d times already must not be "+
			"moved again; the fix did not work and this is a loop", res.Replayed, DefaultMaxReplays)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("skipped = %d, want 1 — the operator must be told which message is stuck", len(res.Skipped))
	}
	if res.Skipped[0].Replays < DefaultMaxReplays {
		t.Errorf("skipped message replays = %d, want >= %d", res.Skipped[0].Replays, DefaultMaxReplays)
	}

	// It is still in the DLQ. Refusing to replay must never mean discarding.
	got, err := bus.Inspect(ctx, topic, group, 10)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("DLQ holds %d, want 1 — a message refused for looping must be KEPT", len(got))
	}
}

// TestUndecodableBodyIsQuarantinedWithItsBytes covers the other half of P0-6's
// criteria: a malformed body lands in quarantine, and what is preserved is the
// raw bytes — the only thing there is to preserve when it will not parse.
func TestUndecodableBodyIsQuarantinedWithItsBytes(t *testing.T) {
	const (
		topic    = "policy.created"
		group    = "replay-malformed"
		exchange = "dxtest-replay-malformed"
	)
	bus := newReplayBus(t, exchange)
	ctx := context.Background()

	c := &collector{fixed: true} // would accept anything that decodes
	if err := bus.Subscribe(topic, group, c.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	waitForQueue(t, bus, topic, group)

	// Publish bytes that are not an events.Event at all, bypassing bus.Publish.
	garbage := []byte(`{"this is": "not an envelope"`)
	if err := bus.pub.Publish(ctx, exchange, topic, garbage, dxmq.PublishOptions{MessageID: "evt-garbage"}); err != nil {
		t.Fatalf("publish raw: %v", err)
	}

	var got []QuarantinedMessage
	waitFor(t, "the malformed body to be quarantined", func() bool {
		g, err := bus.Inspect(ctx, topic, group, 10)
		if err != nil {
			return false
		}
		got = g
		return len(g) == 1
	})
	if string(got[0].Body) != string(garbage) {
		t.Errorf("quarantined body = %q, want the original bytes %q", got[0].Body, garbage)
	}
	if _, seen := c.snapshot(); seen != 0 {
		t.Errorf("handler saw %d events; an undecodable body must not reach it", seen)
	}
}

// TestPurgeIsExplicit: discarding a quarantine must be a named act.
//
// P0-6's defect was messages disappearing silently, not messages ever being
// discarded. Purge exists so an operator who genuinely wants them gone does so
// through a call that says what it does, rather than by deleting a queue.
func TestPurgeIsExplicit(t *testing.T) {
	const (
		topic    = "policy.created"
		group    = "replay-purge"
		exchange = "dxtest-replay-purge"
	)
	bus := newReplayBus(t, exchange)
	ctx := context.Background()

	c := &collector{}
	if err := bus.Subscribe(topic, group, c.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	waitForQueue(t, bus, topic, group)
	if err := bus.Publish(ctx, topic, events.Event{ID: "evt-purge", Type: topic, Version: 2}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the event to be quarantined", func() bool {
		g, err := bus.Inspect(ctx, topic, group, 10)
		return err == nil && len(g) == 1
	})

	n, err := bus.Purge(ctx, topic, group)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Errorf("purged = %d, want 1", n)
	}
	got, err := bus.Inspect(ctx, topic, group, 10)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DLQ holds %d after purge, want 0", len(got))
	}
}
