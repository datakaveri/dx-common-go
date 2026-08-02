package email

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// capturePublisher records the last PublishJSON call.
type capturePublisher struct {
	exchange   string
	routingKey string
	payload    any
	err        error
	calls      int
}

func (c *capturePublisher) PublishJSON(exchange, routingKey string, v any) error {
	c.calls++
	c.exchange, c.routingKey, c.payload = exchange, routingKey, v
	return c.err
}

func validReq() Request {
	return Request{
		ConsumerUserID:    "user-1",
		TemplateType:      TemplateTypePath,
		TemplateStructure: "templates/ack.html",
		IsCreated:         true,
	}
}

func TestNotifier_DisabledIsNoop(t *testing.T) {
	pub := &capturePublisher{}
	n := NewNotifier(Config{Enabled: false}, pub, nil)
	if err := n.Send(context.Background(), validReq()); err != nil {
		t.Fatalf("disabled notifier should return nil, got %v", err)
	}
	if pub.calls != 0 {
		t.Fatal("disabled notifier must not publish")
	}
}

func TestNotifier_NilReceiverIsNoop(t *testing.T) {
	var n *Notifier
	if err := n.Send(context.Background(), validReq()); err != nil {
		t.Fatalf("nil notifier should return nil, got %v", err)
	}
}

func TestNotifier_PublishesToDefaultQueue(t *testing.T) {
	pub := &capturePublisher{}
	n := NewNotifier(Config{Enabled: true}, pub, nil)
	if err := n.Send(context.Background(), validReq()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if pub.calls != 1 {
		t.Fatalf("expected 1 publish, got %d", pub.calls)
	}
	if pub.exchange != "" || pub.routingKey != DefaultQueue {
		t.Fatalf("expected default-exchange routing to %q, got exch=%q key=%q",
			DefaultQueue, pub.exchange, pub.routingKey)
	}
	// Payload must marshal to the Java-compatible field names.
	b, _ := json.Marshal(pub.payload)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"consumerUserId", "templateType", "templateStructure", "isCreated"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("payload missing required field %q: %s", k, b)
		}
	}
}

func TestNotifier_AppliesConfigTemplateDefaults(t *testing.T) {
	pub := &capturePublisher{}
	n := NewNotifier(Config{
		Enabled:           true,
		TemplateType:      TemplateTypeInline,
		TemplateStructure: "<p>hi</p>",
	}, pub, nil)

	req := Request{ConsumerUserID: "u", IsCreated: true} // no template fields
	if err := n.Send(context.Background(), req); err != nil {
		t.Fatalf("send: %v", err)
	}
	b, _ := json.Marshal(pub.payload)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["templateType"] != TemplateTypeInline || m["templateStructure"] != "<p>hi</p>" {
		t.Fatalf("config template defaults not applied: %s", b)
	}
}

func TestNotifier_ValidationErrors(t *testing.T) {
	pub := &capturePublisher{}
	n := NewNotifier(Config{Enabled: true}, pub, nil)

	cases := []Request{
		{TemplateType: TemplateTypePath, TemplateStructure: "x", IsCreated: true}, // no consumer
		{ConsumerUserID: "u", TemplateType: "BOGUS", TemplateStructure: "x"},      // bad type
		{ConsumerUserID: "u", TemplateType: TemplateTypePath},                     // no structure
	}
	for i, req := range cases {
		if err := n.Send(context.Background(), req); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
	if pub.calls != 0 {
		t.Fatal("invalid requests must not publish")
	}
}

func TestNotifier_PublishErrorPropagates(t *testing.T) {
	pub := &capturePublisher{err: errors.New("broker down")}
	n := NewNotifier(Config{Enabled: true}, pub, nil)
	if err := n.Send(context.Background(), validReq()); err == nil {
		t.Fatal("expected publish error to propagate")
	}
}
