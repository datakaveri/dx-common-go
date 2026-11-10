package email

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/datakaveri/dx-common-go/messaging/rabbitmq"
)

type recordingPublisher struct {
	mu         sync.Mutex
	exchange   string
	routingKey string
	body       []byte
	opts       rabbitmq.PublishOptions
	calls      int
	err        error
	done       chan struct{}
}

func (p *recordingPublisher) Publish(_ context.Context, exchange, routingKey string, body []byte, opts rabbitmq.PublishOptions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.exchange, p.routingKey, p.body, p.opts = exchange, routingKey, body, opts
	if p.done != nil {
		close(p.done)
	}
	return p.err
}

func fixedSender(pub EventPublisher) *Sender {
	s := NewSender(Config{Enabled: true}, pub, "dx-test", nil)
	s.now = func() time.Time { return time.Date(2026, 11, 3, 10, 15, 0, 0, time.UTC) }
	s.newID = func() string { return "evt-1" }
	return s
}

func joinEvent() Event {
	return Event{
		EmailType:    "org.join_request.submitted",
		ActorID:      "actor-1",
		RecipientIDs: []string{"admin-1"},
		Entity:       &Entity{Type: "org_join_request", ID: "req-1"},
	}
}

func TestSenderStampsAndPublishes(t *testing.T) {
	pub := &recordingPublisher{}
	if err := fixedSender(pub).Send(context.Background(), joinEvent()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if pub.exchange != "" || pub.routingKey != DefaultQueue {
		t.Fatalf("published to exchange=%q key=%q, want the default exchange and %q", pub.exchange, pub.routingKey, DefaultQueue)
	}
	if pub.opts.MessageID != "evt-1" || pub.opts.Type != "org.join_request.submitted" {
		t.Fatalf("AMQP properties = %+v, want message id evt-1 and type org.join_request.submitted", pub.opts)
	}

	var got map[string]any
	if err := json.Unmarshal(pub.body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"schemaVersion": float64(2),
		"eventId":       "evt-1",
		"emailType":     "org.join_request.submitted",
		"source":        "dx-test",
		"occurredAt":    "2026-11-03T10:15:00Z",
		"actorId":       "actor-1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	// Empty optional fields stay off the wire.
	for _, k := range []string{"recipientRoles", "variables", "traceId"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s should be omitted when empty: %s", k, pub.body)
		}
	}
}

func TestSenderPublishesToExchangeByEmailType(t *testing.T) {
	pub := &recordingPublisher{}
	s := NewSender(Config{Enabled: true, Exchange: "dx.email.events", Queue: "ignored"}, pub, "dx-test", nil)
	if err := s.Send(context.Background(), joinEvent()); err != nil {
		t.Fatal(err)
	}
	if pub.exchange != "dx.email.events" || pub.routingKey != "org.join_request.submitted" {
		t.Fatalf("published to exchange=%q key=%q, want the exchange with the emailType", pub.exchange, pub.routingKey)
	}
}

func TestSenderUsesConfiguredRoutingKey(t *testing.T) {
	pub := &recordingPublisher{}
	s := NewSender(Config{Enabled: true, Exchange: "email-events", RoutingKey: "marketplace.email"}, pub, "dx-test", nil)
	if err := s.Send(context.Background(), joinEvent()); err != nil {
		t.Fatal(err)
	}
	if pub.exchange != "email-events" || pub.routingKey != "marketplace.email" {
		t.Fatalf("published to exchange=%q key=%q, want the configured routing key", pub.exchange, pub.routingKey)
	}
}

func TestSenderKeepsCallerEnvelope(t *testing.T) {
	pub := &recordingPublisher{}
	ev := joinEvent()
	ev.EventID, ev.Source = "caller-id", "dx-user"
	ev.OccurredAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := fixedSender(pub).Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	var got Event
	_ = json.Unmarshal(pub.body, &got)
	if got.EventID != "caller-id" || got.Source != "dx-user" || !got.OccurredAt.Equal(ev.OccurredAt) {
		t.Fatalf("caller's envelope overwritten: %+v", got)
	}
}

func TestSenderDisabledOrNilPublishesNothing(t *testing.T) {
	pub := &recordingPublisher{}
	off := NewSender(Config{Enabled: false}, pub, "dx-test", nil)
	if err := off.Send(context.Background(), joinEvent()); err != nil || pub.calls != 0 {
		t.Fatalf("disabled sender: err=%v calls=%d", err, pub.calls)
	}
	var nilSender *Sender
	if err := nilSender.Send(context.Background(), joinEvent()); err != nil {
		t.Fatalf("nil sender: %v", err)
	}
	nilSender.SendAsync(context.Background(), joinEvent())
}

func TestSenderRejectsInvalidEvent(t *testing.T) {
	pub := &recordingPublisher{}
	ev := joinEvent()
	ev.RecipientIDs = nil
	err := fixedSender(pub).Send(context.Background(), ev)
	if err == nil || !strings.Contains(err.Error(), "recipientIds or recipientRoles") {
		t.Fatalf("err = %v, want the missing-recipient error", err)
	}
	if pub.calls != 0 {
		t.Fatal("an invalid event must not be published")
	}
}

func TestSenderWrapsPublishError(t *testing.T) {
	pub := &recordingPublisher{err: errors.New("broker down")}
	if err := fixedSender(pub).Send(context.Background(), joinEvent()); err == nil {
		t.Fatal("expected the publish error")
	}
}

func TestSendAsyncPublishesDetached(t *testing.T) {
	pub := &recordingPublisher{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	fixedSender(pub).SendAsync(ctx, joinEvent())
	cancel() // the request ending must not stop the email
	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("SendAsync did not publish")
	}
}

func TestEventValidate(t *testing.T) {
	valid := Event{
		SchemaVersion: SchemaVersion, EventID: "e", EmailType: "marketplace.mor_application.submitted",
		Source: "s", OccurredAt: time.Now(), RecipientRoles: []string{RoleCosAdmin},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}

	cases := map[string]func(*Event){
		"wrong version":      func(e *Event) { e.SchemaVersion = 1 },
		"no event id":        func(e *Event) { e.EventID = "" },
		"two-segment type":   func(e *Event) { e.EmailType = "org.created" },
		"uppercase type":     func(e *Event) { e.EmailType = "ORG.JOIN.SUBMITTED" },
		"no source":          func(e *Event) { e.Source = "" },
		"no time":            func(e *Event) { e.OccurredAt = time.Time{} },
		"no recipients":      func(e *Event) { e.RecipientRoles = nil },
		"empty recipient id": func(e *Event) { e.RecipientIDs = []string{" "} },
		"half an entity":     func(e *Event) { e.Entity = &Entity{Type: "x"} },
		"bad variable name":  func(e *Event) { e.Variables = map[string]string{"org-name": "x"} },
		"oversized variable": func(e *Event) { e.Variables = map[string]string{"x": strings.Repeat("a", maxVariableValue+1)} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := valid
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}
