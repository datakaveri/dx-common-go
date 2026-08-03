package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/events"
)

type policyCreated struct {
	PolicyID string `json:"policyId"`
	ItemID   string `json:"itemId"`
}

var policyTopic = events.NewTopic[policyCreated]("policy.created")

func TestTypedRoundTrip(t *testing.T) {
	bus := events.NewMemory()
	ctx := context.Background()

	var got policyCreated
	if err := policyTopic.Subscribe(bus, "authz-sync", func(_ context.Context, p policyCreated) error {
		got = p
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	want := policyCreated{PolicyID: "p-1", ItemID: "i-1"}
	if err := policyTopic.Publish(ctx, bus, want); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Each distinct group sees every message; that is the semantic a kill switch
// needs, and getting it wrong is how a suspension reached one replica of three.
func TestEveryGroupSeesTheMessage(t *testing.T) {
	bus := events.NewMemory()
	var a, b int

	_ = policyTopic.Subscribe(bus, "authz-sync", func(context.Context, policyCreated) error { a++; return nil })
	_ = policyTopic.Subscribe(bus, "audit", func(context.Context, policyCreated) error { b++; return nil })

	if err := policyTopic.Publish(context.Background(), bus, policyCreated{PolicyID: "p"}); err != nil {
		t.Fatal(err)
	}
	if a != 1 || b != 1 {
		t.Errorf("deliveries: authz-sync=%d audit=%d, want 1 each", a, b)
	}
}

// Members of ONE group share the work — the message is delivered once, not
// once per member.
func TestGroupMembersShareTheWork(t *testing.T) {
	bus := events.NewMemory()
	var n int

	_ = policyTopic.Subscribe(bus, "authz-sync", func(context.Context, policyCreated) error { n++; return nil })
	_ = policyTopic.Subscribe(bus, "authz-sync", func(context.Context, policyCreated) error { n++; return nil })

	if err := policyTopic.Publish(context.Background(), bus, policyCreated{PolicyID: "p"}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("delivered %d times to one group, want 1", n)
	}
}

// A version mismatch must DROP, not retry: a consumer that cannot read a
// payload will never be able to, and retrying buries the failures that matter.
func TestVersionMismatchDrops(t *testing.T) {
	bus := events.NewMemory()
	v2 := events.NewTopic[policyCreated]("policy.created").V(2)

	called := false
	_ = v2.Subscribe(bus, "g", func(context.Context, policyCreated) error { called = true; return nil })

	// A v1 publisher on the same topic.
	err := policyTopic.Publish(context.Background(), bus, policyCreated{PolicyID: "p"})
	if !errors.Is(err, events.ErrDrop) {
		t.Fatalf("err = %v, want ErrDrop for a version mismatch", err)
	}
	if called {
		t.Error("the handler ran on a payload written at a different version")
	}
}

func TestUndecodablePayloadDrops(t *testing.T) {
	bus := events.NewMemory()

	// A topic whose payload type cannot accept what the other publishes.
	type other struct {
		N int `json:"policyId"` // policyId arrives as a string
	}
	otherTopic := events.NewTopic[other]("policy.created")

	called := false
	_ = otherTopic.Subscribe(bus, "g", func(context.Context, other) error { called = true; return nil })

	err := policyTopic.Publish(context.Background(), bus, policyCreated{PolicyID: "not-a-number"})
	if !errors.Is(err, events.ErrDrop) {
		t.Fatalf("err = %v, want ErrDrop for an undecodable payload", err)
	}
	if called {
		t.Error("the handler ran on a payload it could not decode")
	}
}

// Every event carries an id, and ids are unique — consumer-side deduplication
// keys on it, so a collision would silently drop a distinct event.
func TestEventIDsAreUniqueAndPresent(t *testing.T) {
	bus := events.NewMemory()
	seen := map[string]bool{}

	_ = bus.Subscribe("policy.created", "g", func(_ context.Context, e events.Event) error {
		if e.ID == "" {
			t.Error("event published with no id; deduplication has nothing to key on")
		}
		if seen[e.ID] {
			t.Errorf("duplicate event id %q", e.ID)
		}
		seen[e.ID] = true
		if e.Type != "policy.created" {
			t.Errorf("Type = %q, want the topic name", e.Type)
		}
		if e.OccurredAt.IsZero() {
			t.Error("OccurredAt not stamped")
		}
		return nil
	})

	for i := 0; i < 100; i++ {
		if err := policyTopic.Publish(context.Background(), bus, policyCreated{PolicyID: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 100 {
		t.Errorf("saw %d distinct ids across 100 publishes", len(seen))
	}
}

// WithID makes publication idempotent: a retried publish carries the same id,
// so a consumer deduplicating on it sees one event rather than two.
func TestWithIDIsStable(t *testing.T) {
	bus := events.NewMemory()
	var ids []string
	_ = bus.Subscribe("policy.created", "g", func(_ context.Context, e events.Event) error {
		ids = append(ids, e.ID)
		return nil
	})

	for i := 0; i < 3; i++ {
		if err := policyTopic.Publish(context.Background(), bus,
			policyCreated{PolicyID: "p-1"}, events.WithID("order-42")); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		if id != "order-42" {
			t.Fatalf("id = %q, want the explicit order-42", id)
		}
	}
}

func TestCorrelationIDPropagates(t *testing.T) {
	bus := events.NewMemory()
	var got string
	_ = bus.Subscribe("policy.created", "g", func(_ context.Context, e events.Event) error {
		got = e.CorrelationID
		return nil
	})

	if err := policyTopic.Publish(context.Background(), bus,
		policyCreated{PolicyID: "p"}, events.WithCorrelationID("req-9")); err != nil {
		t.Fatal(err)
	}
	if got != "req-9" {
		t.Errorf("CorrelationID = %q, want req-9", got)
	}
}
