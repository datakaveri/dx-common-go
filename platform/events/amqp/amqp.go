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
// # Delivery guarantees
//
// Publishing is confirmed (see Open). Consuming is at-least-once with a
// bounded attempt count: an event whose handler keeps failing is dead-lettered
// to "<group>.<topic>.dlq" after Config.MaxAttempts, never silently discarded.
// Both halves delegate to messaging/rabbitmq rather than reimplementing —
// Subscribe's doc comment records the two defects that reimplementation cost.
//
// Layer: L3 (adapter).
package amqp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
	"github.com/datakaveri/dx-common-go/platform/events"
)

const (
	// defaultMaxAttempts bounds redeliveries before an event is dead-lettered.
	defaultMaxAttempts = 5
	// closeTimeout bounds how long Close waits for in-flight handlers.
	closeTimeout = 15 * time.Second
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
	// MaxAttempts caps redeliveries of a failing event before it is
	// dead-lettered. Defaults to 5. Zero would mean unlimited, which is how a
	// poison event stalls everything behind it, so Open rewrites 0 to the
	// default rather than honouring it.
	MaxAttempts int `mapstructure:"max_attempts"`
}

// Bus is an events.Bus over RabbitMQ.
type Bus struct {
	pub      *dxmq.ReliablePublisher
	cfg      Config
	log      *zap.Logger
	mu       sync.Mutex
	runners  []*dxmq.ConsumerRunner
	closed   bool
	exchange string

	// ctx bounds every consumer's lifetime. Subscribe takes no context (the
	// events.Bus interface does not offer one), so the bus owns it and Close
	// cancels it.
	ctx    context.Context
	cancel context.CancelFunc
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
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
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
	ctx, cancel := context.WithCancel(context.Background())
	return &Bus{
		pub:      pub,
		cfg:      cfg,
		log:      zap.NewNop(),
		exchange: cfg.Exchange,
		ctx:      ctx,
		cancel:   cancel,
	}, nil
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
//
// Consuming delegates to messaging/rabbitmq.ConsumerRunner, for the same
// reason Open delegates publishing to ReliablePublisher: the runner already
// owns the dial → declare → consume → ack loop with a reconnect supervisor,
// an attempt cap, and W3C trace continuation. The hand-rolled loop this
// replaced had neither of the first two, and both absences were silent:
//
//   - No reconnect. It ranged over one channel's deliveries, so when that
//     channel closed the goroutine RETURNED and the group stopped consuming
//     for the life of the process. Nothing logged it as fatal, because from
//     the range's point of view the stream simply ended. This is the defect
//     ConsumerRunner was built to fix (ROADMAP P0-5), reintroduced here.
//   - No dead-letter queue. A failing event was requeued once and then
//     ACKNOWLEDGED — silently destroyed, with only a log line. At-least-once
//     delivery was the promise; at-most-twice-then-discard was the behaviour.
//
// DeclareQueueWithDLQ supplies the topology, so an event that exhausts
// MaxAttempts lands in "<group>.<topic>.dlq" via AMQP's native dead-lettering
// and can be inspected and replayed.
func (b *Bus) Subscribe(topic, group string, h events.Handler) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return fmt.Errorf("amqp: bus is closed")
	}
	b.mu.Unlock()

	queue := queueName(topic, group)
	runner := dxmq.NewConsumerRunner(dxmq.ConsumerConfig{
		URL:           b.cfg.URL,
		Queue:         queue,
		ConsumerTag:   group,
		PrefetchCount: b.cfg.Prefetch,
		MaxAttempts:   b.cfg.MaxAttempts,
		Logger:        b.log,
		// Setup runs on every (re)connect, so the topology is re-declared
		// after a broker restart that lost it. Declaration is idempotent.
		Setup: func(ch *amqp091.Channel) error {
			_, err := dxmq.DeclareQueueWithDLQ(ch, b.exchange, b.cfg.ExchangeType, queue, topic, true)
			return err
		},
	})

	b.mu.Lock()
	b.runners = append(b.runners, runner)
	b.mu.Unlock()

	go runner.Run(b.ctx, b.dispatch(topic, group, h))
	return nil
}

// queueName is the durable queue for a subscription.
//
// One definition, because DLQName derives from it: an operator tool that
// computed the queue name differently from Subscribe would inspect a queue that
// does not exist and report an empty quarantine.
func queueName(topic, group string) string { return group + "." + topic }

// dispatch adapts an events.Handler to the runner's Outcome vocabulary.
func (b *Bus) dispatch(topic, group string, h events.Handler) dxmq.Handler {
	return func(ctx context.Context, d dxmq.Delivery) dxmq.Outcome {
		var e events.Event
		if err := json.Unmarshal(d.Body, &e); err != nil {
			// Unparseable: DEAD-LETTER, do not acknowledge. The previous
			// comment here reasoned that "there is no structured payload to
			// inspect on replay" — but the raw BODY is exactly what is worth
			// keeping, and acknowledging destroyed it (ROADMAP P0-6). Requeue
			// would indeed spin the same bytes forever; the DLQ is the third
			// option that was missing.
			b.log.Error("amqp: undecodable event quarantined",
				zap.String("topic", topic), zap.String("group", group), zap.Error(err))
			return dxmq.DeadLetter
		}

		switch err := h(ctx, e); {
		case err == nil:
			return dxmq.Ack
		case isQuarantine(err):
			// Business data this consumer cannot read — a version ahead of it,
			// or a payload that will not decode. It must not be retried and it
			// must not be discarded, so it goes to the DLQ with its body
			// intact, where its depth is an alertable signal and an operator
			// can replay it once a compatible reader ships.
			b.log.Error("amqp: event quarantined",
				zap.String("topic", topic), zap.String("group", group),
				zap.String("id", e.ID), zap.Int("version", e.Version), zap.Error(err))
			return dxmq.DeadLetter
		case isDrop(err):
			// Deliberate, documented noise. This is now the ONLY path that
			// destroys a message, and reaching it requires a handler to say so
			// explicitly with ErrDrop.
			b.log.Warn("amqp: event dropped by handler",
				zap.String("topic", topic), zap.String("id", e.ID), zap.Error(err))
			return dxmq.Ack
		default:
			// Transient: requeue. The runner converts this to DeadLetter once
			// MaxAttempts is exhausted, so a poison event reaches the DLQ
			// instead of cycling forever or being discarded.
			b.log.Warn("amqp: event failed, requeueing",
				zap.String("topic", topic), zap.String("id", e.ID), zap.Error(err))
			return dxmq.Requeue
		}
	}
}

// isDrop reports whether the handler asked for the message to be discarded.
//
// errors.Is, not a hand-rolled unwrap loop: the previous version compared with
// == at each level, which misses any sentinel wrapped by a type implementing
// Is() rather than Unwrap().
func isDrop(err error) bool { return errors.Is(err, events.ErrDrop) }

// isQuarantine reports whether the message must be preserved for inspection
// and replay rather than acknowledged.
func isQuarantine(err error) bool { return errors.Is(err, events.ErrQuarantine) }

// Close stops every consumer and the publisher.
//
// It waits for each runner's in-flight handler to return before closing the
// publisher, so a handler that itself publishes cannot be cut off mid-call.
// The wait is bounded: a handler wedged on a hung dependency must not hold
// shutdown open indefinitely.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	runners := append([]*dxmq.ConsumerRunner(nil), b.runners...)
	b.mu.Unlock()

	// Cancel first, so every runner's Run observes the cancellation and begins
	// unwinding; only then wait for them.
	b.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	for _, r := range runners {
		if err := r.Stop(ctx); err != nil {
			b.log.Warn("amqp: consumer did not stop before the deadline", zap.Error(err))
			break
		}
	}

	b.pub.Close()
	return nil
}
