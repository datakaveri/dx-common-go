package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// PublisherConfig configures a ReliablePublisher.
type PublisherConfig struct {
	URL string
	// Exchange / ExchangeType, when set, are declared (durable) on every
	// (re)connect. Leave Exchange empty to publish only to the default
	// exchange (queue-name routing) without declaring anything.
	Exchange     string
	ExchangeType string
	// Confirms puts the channel in publisher-confirm mode: Publish blocks
	// until the broker acks the message (or ctx expires), so a nil return
	// means the broker HAS the message — required when a caller marks
	// durable state on success (e.g. the outbox dispatcher's MarkSent).
	// Without confirms, a nil return only means "written to the socket".
	Confirms bool
	Logger   *zap.Logger
}

// ReliablePublisher publishes to RabbitMQ with lazy reconnect and one
// automatic retry on a closed channel. Safe for concurrent use. It unifies
// the previously per-service publishers (dx-acl-go events.Publisher,
// dx-files-connect-api-go messaging.Client) and satisfies the
// notify/email.Publisher interface via PublishJSON.
type ReliablePublisher struct {
	cfg    PublisherConfig
	logger *zap.Logger

	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewReliablePublisher dials the broker. A failed initial dial is not fatal:
// the first Publish call retries, so service startup never blocks on RMQ.
func NewReliablePublisher(cfg PublisherConfig) (*ReliablePublisher, error) {
	if cfg.URL == "" {
		return nil, errors.New("rabbitmq publisher: URL is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &ReliablePublisher{cfg: cfg, logger: logger}
	if err := p.dial(); err != nil {
		logger.Warn("publisher initial dial failed; will retry on first publish", zap.Error(err))
	}
	return p, nil
}

// PublishOptions carries optional per-message metadata.
type PublishOptions struct {
	MessageID string
}

// Publish sends body to exchange/routingKey, redialing and retrying once if
// the channel was closed. An empty exchange publishes to the default exchange.
// It starts a producer span and stamps W3C trace-context headers onto the
// message so a consumer can continue the trace (a no-op until observability.Init
// configures a TracerProvider and propagator).
func (p *ReliablePublisher) Publish(ctx context.Context, exchange, routingKey string, body []byte, opts PublishOptions) error {
	ctx, span := startProducerSpan(ctx, exchange, routingKey)
	defer span.End()

	headers := amqp.Table{}
	injectTraceContext(ctx, headers)
	pub := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now().UTC(),
		MessageId:    opts.MessageID,
		Headers:      headers,
		Body:         body,
	}

	err := p.publishWithRetry(ctx, exchange, routingKey, pub)
	recordSpanError(span, err)
	return err
}

// publishWithRetry performs one publish, redialing and retrying exactly once
// when the channel was found closed.
func (p *ReliablePublisher) publishWithRetry(ctx context.Context, exchange, routingKey string, pub amqp.Publishing) error {
	if err := p.publishOnce(ctx, exchange, routingKey, pub); err != nil {
		if errors.Is(err, amqp.ErrClosed) || isChannelClosedErr(err) {
			p.logger.Warn("publish channel closed, redialling", zap.Error(err))
			if derr := p.dial(); derr != nil {
				return fmt.Errorf("redial after publish failure: %w", derr)
			}
			if err2 := p.publishOnce(ctx, exchange, routingKey, pub); err2 != nil {
				return fmt.Errorf("publish after redial: %w", err2)
			}
			return nil
		}
		return err
	}
	return nil
}

// PublishJSON marshals v and publishes it (background context). It implements
// the notify/email.Publisher interface so the email notifier can share this
// publisher's connection.
func (p *ReliablePublisher) PublishJSON(exchange, routingKey string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: marshal: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.Publish(ctx, exchange, routingKey, body, PublishOptions{})
}

func (p *ReliablePublisher) publishOnce(ctx context.Context, exchange, routingKey string, pub amqp.Publishing) error {
	p.mu.Lock()
	ch := p.ch
	p.mu.Unlock()
	if ch == nil {
		return amqp.ErrClosed
	}
	if !p.cfg.Confirms {
		return ch.PublishWithContext(ctx, exchange, routingKey, false, false, pub)
	}

	// Confirm mode: wait for the broker's ack before reporting success.
	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey, false, false, pub)
	if err != nil {
		return err
	}
	select {
	case <-dc.Done():
		if !dc.Acked() {
			return fmt.Errorf("rabbitmq publisher: broker nacked message (exchange=%q key=%q)", exchange, routingKey)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("rabbitmq publisher: confirm wait: %w", ctx.Err())
	}
}

// dial atomically replaces the connection + channel and (re)declares the
// configured exchange.
func (p *ReliablePublisher) dial() error {
	conn, err := amqp.Dial(p.cfg.URL)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open channel: %w", err)
	}
	if p.cfg.Confirms {
		if err := ch.Confirm(false); err != nil {
			ch.Close()
			conn.Close()
			return fmt.Errorf("enable publisher confirms: %w", err)
		}
	}
	if p.cfg.Exchange != "" {
		kind := p.cfg.ExchangeType
		if kind == "" {
			kind = "topic"
		}
		if err := ch.ExchangeDeclare(p.cfg.Exchange, kind, true, false, false, false, nil); err != nil {
			ch.Close()
			conn.Close()
			return fmt.Errorf("declare exchange %q: %w", p.cfg.Exchange, err)
		}
	}

	p.mu.Lock()
	old := p.conn
	p.conn, p.ch = conn, ch
	p.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}
	return nil
}

// IsConnected reports whether the publisher currently holds an open channel.
// Used by health.RabbitMQChecker; does not perform network IO.
func (p *ReliablePublisher) IsConnected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ch != nil && !p.ch.IsClosed()
}

// Check implements the platform's health.Checker (Check(ctx) error), so a
// publisher can be registered directly as a readiness probe:
//
//	app.Probe("rabbitmq", publisher)
//
// The interface is satisfied STRUCTURALLY — this package does not import
// platform/observability/health, which would point a legacy messaging package
// at the platform tree. ConsumerRunner and Client carry the same pair, so
// "how do I probe this?" has one answer whichever broker handle a service holds.
//
// Note that a publisher dials lazily: a service that has not published yet
// reports not-connected, which is honest but makes this a poor gating probe
// for a publish-only service that publishes rarely. Prefer Health.AddOptional
// there, and reserve gating for a service whose job stops without the broker.
func (p *ReliablePublisher) Check(context.Context) error {
	if !p.IsConnected() {
		return fmt.Errorf("rabbitmq: publisher not connected to exchange %q", p.cfg.Exchange)
	}
	return nil
}

// Close releases the connection and channel.
func (p *ReliablePublisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil {
		p.ch.Close()
	}
	if p.conn != nil {
		p.conn.Close()
	}
}

// isChannelClosedErr matches amqp errors that mean the channel is unusable.
// The amqp091 driver returns plain errors for these.
func isChannelClosedErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return s == "channel/connection is not open" ||
		s == `Exception (504) Reason: "channel/connection is not open"`
}
