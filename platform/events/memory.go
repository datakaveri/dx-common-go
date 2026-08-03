package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// newID returns a random event id.
//
// Random, not sequential: ids are used for consumer-side deduplication across
// replicas, so they must not collide and must not be guessable enough to be
// forged into a dedup table by a hostile producer.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the platform is unusable; a degraded id
		// would silently break deduplication, which is worse than the panic.
		panic("events: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Memory is an in-process Bus.
//
// Its purpose is testing a real handler against a real bus with no broker: a
// service's subscription wiring, decoding, version check and idempotency all
// execute exactly as they will in production.
//
// It delivers SYNCHRONOUSLY, inside Publish. That is deliberate for a test
// bus — an asynchronous one turns every assertion into a sleep or a channel
// dance, which is how event tests become flaky. It also means Memory is not a
// production transport: there is no persistence, no retry and no cross-process
// delivery, and it says so rather than pretending.
type Memory struct {
	mu   sync.RWMutex
	subs map[string][]subscription
}

type subscription struct {
	group string
	h     Handler
}

// NewMemory builds an in-process bus.
func NewMemory() *Memory { return &Memory{subs: make(map[string][]subscription)} }

// Publish delivers to every group subscribed to the topic.
//
// One handler per group, mirroring the real semantics: members of a group
// share work, distinct groups each see the message. With one handler per group
// registered — the normal case in a test — that is one delivery each.
func (m *Memory) Publish(ctx context.Context, topic string, e Event) error {
	m.mu.RLock()
	subs := append([]subscription(nil), m.subs[topic]...)
	m.mu.RUnlock()

	seen := make(map[string]bool, len(subs))
	for _, s := range subs {
		if seen[s.group] {
			continue // group members share the work; deliver once per group
		}
		seen[s.group] = true
		if err := s.h(ctx, e); err != nil {
			// Surfaced, not swallowed: a test asserting a handler failed must
			// be able to see it. A real transport would retry or DLQ instead.
			return err
		}
	}
	return nil
}

// Subscribe registers a handler for a topic and group.
func (m *Memory) Subscribe(topic, group string, h Handler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[topic] = append(m.subs[topic], subscription{group: group, h: h})
	return nil
}

// Close drops every subscription.
func (m *Memory) Close() error {
	m.mu.Lock()
	m.subs = make(map[string][]subscription)
	m.mu.Unlock()
	return nil
}
