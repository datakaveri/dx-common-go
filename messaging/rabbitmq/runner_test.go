package rabbitmq

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestDedup_SeenAndMark(t *testing.T) {
	d := NewDedup(3)

	if d.Seen("a") {
		t.Fatal("fresh id should not be seen")
	}
	d.Mark("a")
	if !d.Seen("a") {
		t.Fatal("marked id should be seen")
	}
	// Empty ids are never seen or stored.
	if d.Seen("") {
		t.Fatal("empty id should never be seen")
	}
	d.Mark("") // no-op
}

func TestDedup_FIFOEviction(t *testing.T) {
	d := NewDedup(2)
	d.Mark("a")
	d.Mark("b")
	d.Mark("c") // evicts "a"

	if d.Seen("a") {
		t.Fatal("oldest id should have been evicted")
	}
	if !d.Seen("b") || !d.Seen("c") {
		t.Fatal("recent ids should be retained")
	}
}

func TestDedup_MarkIdempotent(t *testing.T) {
	d := NewDedup(2)
	d.Mark("a")
	d.Mark("a") // re-marking must not consume capacity
	d.Mark("b")
	if !d.Seen("a") || !d.Seen("b") {
		t.Fatal("re-marking the same id should not evict others")
	}
}

func TestDedup_Concurrent(t *testing.T) {
	d := NewDedup(128)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := string(rune('a' + n%26))
			d.Mark(id)
			_ = d.Seen(id)
		}(i)
	}
	wg.Wait()
}

// fakeAcker records Ack/Nack/Reject calls instead of talking to a broker.
type fakeAcker struct {
	mu     sync.Mutex
	acked  []uint64
	nacked []struct {
		tag     uint64
		requeue bool
	}
}

func (f *fakeAcker) Ack(tag uint64, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, tag)
	return nil
}

func (f *fakeAcker) Nack(tag uint64, _, requeue bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nacked = append(f.nacked, struct {
		tag     uint64
		requeue bool
	}{tag, requeue})
	return nil
}

func (f *fakeAcker) Reject(uint64, bool) error { return nil }

func newDelivery(acker amqp.Acknowledger, messageID string) Delivery {
	return Delivery{Acknowledger: acker, DeliveryTag: 1, MessageId: messageID}
}

func TestDispatch_MaxAttemptsExceeded_DeadLetters(t *testing.T) {
	acker := &fakeAcker{}
	r := NewConsumerRunner(ConsumerConfig{MaxAttempts: 2})

	d := newDelivery(acker, "msg-1")
	alwaysRequeue := func(context.Context, Delivery) Outcome { return Requeue }

	r.dispatch(context.Background(), d, alwaysRequeue) // attempt 1: still under limit
	r.dispatch(context.Background(), d, alwaysRequeue) // attempt 2: reaches MaxAttempts -> dead-letter

	if len(acker.nacked) != 2 {
		t.Fatalf("expected 2 nacks, got %d", len(acker.nacked))
	}
	if acker.nacked[0].requeue != true {
		t.Fatalf("first nack should still requeue, got requeue=%v", acker.nacked[0].requeue)
	}
	if acker.nacked[1].requeue != false {
		t.Fatalf("second nack should dead-letter (requeue=false) once MaxAttempts is hit, got requeue=%v", acker.nacked[1].requeue)
	}
}

func TestDispatch_MaxAttemptsZero_NeverCaps(t *testing.T) {
	acker := &fakeAcker{}
	r := NewConsumerRunner(ConsumerConfig{}) // MaxAttempts unset

	d := newDelivery(acker, "msg-1")
	alwaysRequeue := func(context.Context, Delivery) Outcome { return Requeue }

	for i := 0; i < 5; i++ {
		r.dispatch(context.Background(), d, alwaysRequeue)
	}
	for i, call := range acker.nacked {
		if !call.requeue {
			t.Fatalf("call %d unexpectedly dead-lettered with MaxAttempts unset", i)
		}
	}
}

func TestDispatch_Dedup_SkipsHandlerOnRedelivery(t *testing.T) {
	acker := &fakeAcker{}
	dedup := NewDedup(16)
	r := NewConsumerRunner(ConsumerConfig{Dedup: dedup})

	d := newDelivery(acker, "msg-1")
	calls := 0
	handler := func(context.Context, Delivery) Outcome {
		calls++
		return Ack
	}

	r.dispatch(context.Background(), d, handler)
	r.dispatch(context.Background(), d, handler) // redelivery of the same MessageId

	if calls != 1 {
		t.Fatalf("handler should run once, ran %d times", calls)
	}
	if len(acker.acked) != 2 {
		t.Fatalf("both deliveries should still be acked, got %d acks", len(acker.acked))
	}
}

func TestConsumerRunner_StopReturnsAfterRunExits(t *testing.T) {
	r := NewConsumerRunner(ConsumerConfig{URL: "amqp://invalid:invalid@127.0.0.1:1/", Queue: "q"})
	ctx, cancel := context.WithCancel(context.Background())

	go r.Run(ctx, func(context.Context, Delivery) Outcome { return Ack })
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := r.Stop(stopCtx); err != nil {
		t.Fatalf("Stop did not observe Run exit in time: %v", err)
	}
}

// TestIsConnected_FalseBeforeConnect pins the state a readiness probe sees
// while the broker is unreachable. Before this existed a consumer-only service
// reported ready with no broker at all, because nothing could observe that it
// was not consuming — see PG-002.
func TestIsConnected_FalseBeforeConnect(t *testing.T) {
	r := NewConsumerRunner(ConsumerConfig{Queue: "q"})

	if r.IsConnected() {
		t.Error("IsConnected = true before Run; a runner that never dialled is not consuming")
	}
	if err := r.Check(context.Background()); err == nil {
		t.Error("Check = nil before Run; readiness would go green with no broker")
	}
}

// TestCheck_NamesTheQueue keeps the probe's failure self-describing: a
// readiness report saying only "not connected" does not say what is not
// connected when a service consumes more than one queue.
func TestCheck_NamesTheQueue(t *testing.T) {
	r := NewConsumerRunner(ConsumerConfig{Queue: "email-notification"})

	err := r.Check(context.Background())
	if err == nil {
		t.Fatal("Check = nil, want an error")
	}
	if !strings.Contains(err.Error(), "email-notification") {
		t.Errorf("Check error %q does not name the queue", err)
	}
}
