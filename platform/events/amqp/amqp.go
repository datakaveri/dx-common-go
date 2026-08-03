// Package amqp is the RabbitMQ implementation of events.Bus.
//
// It is the only package under platform/events permitted to touch a broker
// driver — depguard enforces that, on the same principle as
// platform/database/sql/pgx: the dependency is not hidden, it is made
// locatable. Services depend on events.Bus and never import this for
// publishing.
//
// # Wire format — the decision
//
// The body is the platform Event envelope, with the service's existing domain
// message as its Payload:
//
//	{"id":"…","type":"policy.create","version":1,"occurredAt":"…",
//	 "payload":{"action":"create","policy":{…},"request_id":"…"}}
//
// The alternative was to keep publishing the bare domain body for
// compatibility. That was rejected: the envelope is what carries the id
// consumers deduplicate on and the version they gate on, and a transport that
// strips it would leave every consumer inventing both again — which is the
// state this package exists to end. Consumers accept both shapes during the
// transition; see dx-authz-go's decoder.
//
// Layer: L3 (adapter).
package amqp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
	"github.com/datakaveri/dx-common-go/platform/events"
)

// Config wires the broker.
type Config struct {
	URL      string `mapstructure:"url"`
	Exchange string `mapstructure:"exchange"`
	// ExchangeType defaults to "topic". Topic is what allows a consumer to
	// bind a pattern ("policy.*") rather than enumerate every routing key.
	ExchangeType string `mapstructure:"exchange_type"`
	// Prefetch bounds unacknowledged deliveries per consumer. Without it a
	// broker hands one consumer the whole backlog and the rest idle.
	Prefetch int `mapstructure:"prefetch"`
}

// Bus is an events.Bus over RabbitMQ.
type Bus struct {
	pub      *dxmq.ReliablePublisher
	cfg      Config
	log      *zap.Logger
	mu       sync.Mutex
	conns    []*amqp091.Connection
	chans    []*amqp091.Channel
	closed   bool
	exchange string
}

// Open connects.
//
// Publishing reuses messaging/rabbitmq.ReliablePublisher rather than
// reimplementing: it already has publisher confirms, redial-and-retry and W3C
// trace propagation, all of which the outbox depends on. Confirms in
// particular are load-bearing — the dispatcher marks a row sent when Publish
// returns nil, so nil must mean the broker HAS the message, not "written to a
// socket". That is transitional reuse of a legacy package, and the only reason
// this file does not own its own publisher.
func Open(cfg Config) (*Bus, error) {
	if cfg.ExchangeType == "" {
		cfg.ExchangeType = "topic"
	}
	if cfg.Prefetch <= 0 {
		cfg.Prefetch = 32
	}
	pub, err := dxmq.NewReliablePublisher(dxmq.PublisherConfig{
		URL:          cfg.URL,
		Exchange:     cfg.Exchange,
		ExchangeType: cfg.ExchangeType,
		Confirms:     true,
		Logger:       zap.NewNop(),
	})
	if err != nil {
		return nil, err
	}
	return &Bus{pub: pub, cfg: cfg, log: zap.NewNop(), exchange: cfg.Exchange}, nil
}

// WithLogger sets the logger used for consumer-side reporting.
func (b *Bus) WithLogger(l *zap.Logger) *Bus {
	if l != nil {
		b.log = l
	}
	return b
}

// Publish sends the envelope with the topic as the routing key.
//
// MessageID carries Event.ID so the broker's own tooling — and any consumer
// reading headers rather than the body — can deduplicate without parsing.
func (b *Bus) Publish(ctx context.Context, topic string, e events.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("amqp: encoding %s: %w", topic, err)
	}
	return b.pub.Publish(ctx, b.exchange, topic, body, dxmq.PublishOptions{MessageID: e.ID})
}

// Subscribe binds a durable queue for the group and consumes it.
//
// The queue is named "<group>.<topic>" and is DURABLE, so a group that is
// offline still accumulates its messages. One queue per group is what gives
// the semantics events.Bus promises: members of a group share that queue
// (competing consumers), while a distinct group has its own and therefore
// sees every message.
func (b *Bus) Subscribe(topic, group string, h events.Handler) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return fmt.Errorf("amqp: bus is closed")
	}
	b.mu.Unlock()

	conn, err := amqp091.Dial(b.cfg.URL)
	if err != nil {
		return fmt.Errorf("amqp: dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("amqp: channel: %w", err)
	}
	if err := ch.ExchangeDeclare(b.exchange, b.cfg.ExchangeType, true, false, false, false, nil); err != nil {
		return b.abort(conn, ch, fmt.Errorf("amqp: declare exchange: %w", err))
	}

	queue := group + "." + topic
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		return b.abort(conn, ch, fmt.Errorf("amqp: declare queue %s: %w", queue, err))
	}
	if err := ch.QueueBind(queue, topic, b.exchange, false, nil); err != nil {
		return b.abort(conn, ch, fmt.Errorf("amqp: bind %s: %w", queue, err))
	}
	if err := ch.Qos(b.cfg.Prefetch, 0, false); err != nil {
		return b.abort(conn, ch, fmt.Errorf("amqp: qos: %w", err))
	}

	// autoAck=false: acknowledging before the handler runs would lose the
	// message on any handler failure, which is the opposite of at-least-once.
	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return b.abort(conn, ch, fmt.Errorf("amqp: consume %s: %w", queue, err))
	}

	b.mu.Lock()
	b.conns = append(b.conns, conn)
	b.chans = append(b.chans, ch)
	b.mu.Unlock()

	go b.consume(deliveries, topic, group, h)
	return nil
}

// consume dispatches deliveries to the handler.
func (b *Bus) consume(deliveries <-chan amqp091.Delivery, topic, group string, h events.Handler) {
	for d := range deliveries {
		var e events.Event
		if err := json.Unmarshal(d.Body, &e); err != nil {
			// Unparseable: acknowledge and drop. Requeuing would spin the
			// same message forever and starve the queue behind it.
			b.log.Error("amqp: undecodable event dropped",
				zap.String("topic", topic), zap.String("group", group), zap.Error(err))
			_ = d.Ack(false)
			continue
		}

		err := h(context.Background(), e)
		switch {
		case err == nil:
			_ = d.Ack(false)
		case isDrop(err):
			// The handler declared this will never succeed.
			b.log.Warn("amqp: event dropped by handler",
				zap.String("topic", topic), zap.String("id", e.ID), zap.Error(err))
			_ = d.Ack(false)
		default:
			// Requeue ONCE. d.Redelivered tells us this is the second
			// attempt, so a persistently failing message is dropped with a
			// loud log rather than cycling forever — a poison message that
			// requeues indefinitely blocks everything behind it, which is the
			// classic way one bad event stalls an entire projection.
			//
			// A real DLQ replaces this; see the package TODO.
			if d.Redelivered {
				b.log.Error("amqp: event failed twice, dropping",
					zap.String("topic", topic), zap.String("id", e.ID), zap.Error(err))
				_ = d.Ack(false)
				continue
			}
			b.log.Warn("amqp: event failed, requeueing once",
				zap.String("topic", topic), zap.String("id", e.ID), zap.Error(err))
			_ = d.Nack(false, true)
		}
	}
}

// isDrop reports whether the handler asked for the message to be discarded.
func isDrop(err error) bool {
	for err != nil {
		if err == events.ErrDrop {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func (b *Bus) abort(conn *amqp091.Connection, ch *amqp091.Channel, err error) error {
	_ = ch.Close()
	_ = conn.Close()
	return err
}

// Close stops every consumer and the publisher.
func (b *Bus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, ch := range b.chans {
		_ = ch.Close()
	}
	for _, c := range b.conns {
		_ = c.Close()
	}
	b.pub.Close()
	return nil
}
