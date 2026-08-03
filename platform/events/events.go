// Package events is the platform's messaging layer.
//
// Services publish and consume domain events without naming a broker. Nothing
// in a service reveals whether the transport is RabbitMQ, Kafka or an
// in-process bus — which is what lets a test run the real handler against a
// real bus with no container, and what makes changing brokers a wiring change
// rather than a rewrite.
//
// # The shape
//
//	type PolicyCreated struct{ PolicyID, ItemID string }
//
//	var PolicyEvents = events.NewTopic[PolicyCreated]("policy.created")
//
//	PolicyEvents.Publish(ctx, bus, PolicyCreated{...})
//	PolicyEvents.Subscribe(bus, "authz-sync", func(ctx context.Context, e PolicyCreated) error { … })
//
// A Topic[T] binds a name to a payload type, so publisher and consumer cannot
// disagree about the shape. The alternative — []byte in, []byte out — is what
// put `type Delivery = amqp.Delivery` in every consumer's signature and made
// eight services import the driver directly.
//
// Layer: L3 (adapter). Depends on L0 and platform/database/sql only.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrDrop tells the consumer to acknowledge and discard a message rather than
// retry it.
//
// Return it for a payload that will never succeed — malformed JSON, a
// referenced entity deleted long ago. Without it such a message retries until
// it exhausts its attempts and lands in the DLQ, which is noise that hides the
// failures that matter.
var ErrDrop = errors.New("events: drop this message")

// Event is the wire envelope. Every message carries it, so a consumer can
// always answer what/when/which-version without the payload's cooperation.
type Event struct {
	// ID deduplicates. A broker may deliver the same message twice — that is
	// at-least-once, not a defect — so idempotency is the consumer's job and
	// this is what it keys on.
	ID string `json:"id"`
	// Type is the topic name, carried in the body as well as the routing key
	// so a message remains self-describing once it leaves the broker (in a
	// DLQ dump, or a log).
	Type string `json:"type"`
	// Version lets a payload evolve. A consumer that does not recognise a
	// version must fail loudly rather than silently misread fields.
	Version int `json:"version"`
	// CorrelationID threads a causal chain across services.
	CorrelationID string `json:"correlationId,omitempty"`
	// OccurredAt is when the domain fact happened — not when it was
	// published, which can be much later for an outbox row.
	OccurredAt time.Time       `json:"occurredAt"`
	Payload    json.RawMessage `json:"payload"`
}

// Bus publishes and subscribes. One implementation per transport.
//
// Subscribe takes a GROUP: every member of a group shares the work, and each
// distinct group sees every message. That is the distinction brokers spell
// differently (Kafka consumer group, AMQP shared queue) and getting it wrong
// is how a kill-switch event reached one replica out of three.
type Bus interface {
	Publish(ctx context.Context, topic string, e Event) error
	Subscribe(topic, group string, h Handler) error
	Close() error
}

// Handler processes one event. Returning an error retries per the consumer's
// policy; returning ErrDrop acknowledges without retrying.
type Handler func(ctx context.Context, e Event) error

// Topic binds a name to a payload type.
type Topic[T any] struct {
	name    string
	version int
}

// NewTopic declares a topic at version 1.
func NewTopic[T any](name string) Topic[T] { return Topic[T]{name: name, version: 1} }

// V returns the topic at a specific payload version. Publish stamps it and
// Subscribe rejects anything else, so a consumer never silently misreads a
// payload written by a newer producer.
func (t Topic[T]) V(version int) Topic[T] { t.version = version; return t }

// Name is the topic's routing name.
func (t Topic[T]) Name() string { return t.name }

// Publish encodes payload and publishes it.
func (t Topic[T]) Publish(ctx context.Context, b Bus, payload T, opts ...Option) error {
	e, err := t.event(payload, opts...)
	if err != nil {
		return err
	}
	return b.Publish(ctx, t.name, e)
}

// Subscribe registers a typed handler.
//
// Decoding happens here, so a handler never sees bytes. A payload that will
// not decode, or carries an unexpected version, is DROPPED rather than
// retried — neither will ever succeed, and retrying them buries real failures.
func (t Topic[T]) Subscribe(b Bus, group string, h func(context.Context, T) error) error {
	return b.Subscribe(t.name, group, func(ctx context.Context, e Event) error {
		if e.Version != t.version {
			return fmt.Errorf("%w: %s expects version %d, got %d",
				ErrDrop, t.name, t.version, e.Version)
		}
		var payload T
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return fmt.Errorf("%w: decoding %s: %v", ErrDrop, t.name, err)
		}
		return h(ctx, payload)
	})
}

// event builds the envelope.
func (t Topic[T]) event(payload T, opts ...Option) (Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("events: encoding %s: %w", t.name, err)
	}
	e := Event{
		ID:         newID(),
		Type:       t.name,
		Version:    t.version,
		OccurredAt: time.Now().UTC(),
		Payload:    body,
	}
	for _, o := range opts {
		o(&e)
	}
	return e, nil
}

// Option customises an event before publication.
type Option func(*Event)

// WithCorrelationID threads a causal chain across services.
func WithCorrelationID(id string) Option {
	return func(e *Event) { e.CorrelationID = id }
}

// WithID sets an explicit event id.
//
// Use it to make publication idempotent: deriving the id from the domain fact
// (an order id, say) means a retried publish produces the SAME id, and a
// consumer deduplicating on it sees one event rather than two.
func WithID(id string) Option { return func(e *Event) { e.ID = id } }

// WithOccurredAt records when the fact happened, for a publish that lags it.
func WithOccurredAt(t time.Time) Option {
	return func(e *Event) { e.OccurredAt = t.UTC() }
}
