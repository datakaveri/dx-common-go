package amqp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
	"github.com/datakaveri/dx-common-go/platform/events"
)

// testBus is enough of a Bus to exercise dispatch, which touches only the
// logger. Open would need a live broker.
func testBus() *Bus { return &Bus{log: zap.NewNop()} }

func delivery(t *testing.T, e events.Event) dxmq.Delivery {
	t.Helper()
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return dxmq.Delivery{Body: body}
}

// TestDispatchOutcomes pins the handler-error → AMQP-outcome mapping. The
// Requeue case is the one that matters most: it is what lets the runner's
// MaxAttempts cap dead-letter a poison event. Returning Ack here — as the
// hand-rolled loop this replaced ultimately did — silently destroys it.
func TestDispatchOutcomes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want dxmq.Outcome
	}{
		{"success acks", nil, dxmq.Ack},
		{"ErrDrop acks", events.ErrDrop, dxmq.Ack},
		{"wrapped ErrDrop acks", fmt.Errorf("unreadable: %w", events.ErrDrop), dxmq.Ack},
		{"transient failure requeues", errors.New("db unavailable"), dxmq.Requeue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := testBus()
			h := func(context.Context, events.Event) error { return tt.err }

			got := b.dispatch("policy.create", "authz", h)(
				context.Background(), delivery(t, events.Event{ID: "e-1"}))

			if got != tt.want {
				t.Errorf("outcome = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDispatchUndecodableQuarantines is ROADMAP P0-6.
//
// This test previously asserted the opposite — Ack — on the reasoning that
// "bytes that will never parse cannot be replayed, so requeuing them only
// starves the queue behind them." Both halves were wrong. The raw body is
// exactly what is worth keeping (it is the only evidence of what a producer
// actually sent), and DeadLetter REJECTS WITHOUT REQUEUE, so nothing is
// starved. Acknowledging destroyed the message instead.
func TestDispatchUndecodableQuarantines(t *testing.T) {
	b := testBus()
	called := false
	h := func(context.Context, events.Event) error { called = true; return nil }

	got := b.dispatch("policy.create", "authz", h)(
		context.Background(), dxmq.Delivery{Body: []byte("{not json")})

	if got != dxmq.DeadLetter {
		t.Errorf("outcome = %v, want %v — an undecodable body must be preserved for inspection, not destroyed", got, dxmq.DeadLetter)
	}
	if called {
		t.Error("handler ran on an undecodable body; it should never see one")
	}
}

// TestDispatchQuarantinesUnreadableBusinessData: ErrQuarantine must reach the
// DLQ with the body intact, and ErrDrop must remain the ONLY path that
// destroys a message.
func TestDispatchQuarantineVsDrop(t *testing.T) {
	body, err := json.Marshal(events.Event{ID: "e-1", Type: "policy.create", Version: 2})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		err  error
		want dxmq.Outcome
	}{
		{"a version this consumer cannot read", fmt.Errorf("%w: version 2", events.ErrQuarantine), dxmq.DeadLetter},
		{"deliberate, documented noise", fmt.Errorf("%w: heartbeat", events.ErrDrop), dxmq.Ack},
		{"a transient failure", errors.New("connection reset"), dxmq.Requeue},
		{"success", nil, dxmq.Ack},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := testBus()
			h := func(context.Context, events.Event) error { return tt.err }
			got := b.dispatch("policy.create", "authz", h)(context.Background(), dxmq.Delivery{Body: body})
			if got != tt.want {
				t.Errorf("outcome = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDispatchPassesEventThrough guards the decode step: the handler must
// receive the envelope's fields, not a zero Event.
func TestDispatchPassesEventThrough(t *testing.T) {
	b := testBus()
	var got events.Event
	h := func(_ context.Context, e events.Event) error { got = e; return nil }

	want := events.Event{ID: "e-42", Type: "policy.create", Version: 1}
	b.dispatch("policy.create", "authz", h)(context.Background(), delivery(t, want))

	if got.ID != want.ID || got.Type != want.Type || got.Version != want.Version {
		t.Errorf("handler saw %+v, want id/type/version from %+v", got, want)
	}
}
