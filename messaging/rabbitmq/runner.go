package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// Delivery is re-exported so handlers need not import amqp directly.
type Delivery = amqp.Delivery

// Outcome is a handler's decision for one delivery.
type Outcome int

const (
	// Ack marks the message processed.
	Ack Outcome = iota
	// Requeue returns the message for another attempt (transient failure).
	Requeue
	// DeadLetter rejects the message without requeue (poison / unknown).
	DeadLetter
)

// Handler processes one delivery and returns an Outcome.
type Handler func(ctx context.Context, d Delivery) Outcome

// ConsumerConfig configures a ConsumerRunner.
type ConsumerConfig struct {
	URL           string
	Queue         string
	ConsumerTag   string
	PrefetchCount int
	Logger        *zap.Logger
	// Setup declares the topology (exchanges, queues, bindings) on every
	// (re)connect, before consuming begins. Required.
	Setup func(ch *amqp.Channel) error
	// MaxAttempts caps how many times a message may be Requeue'd before the
	// runner treats it as DeadLetter instead, breaking an infinite
	// redelivery loop for a message that always fails. Zero means
	// unlimited (the pre-existing behavior). The counter is process-local,
	// keyed by the delivery's MessageId (messages without one are never
	// capped) — it resets on reconnect/restart, since native AMQP
	// Nack(requeue=true) gives no way to stamp an attempt count onto the
	// redelivered message; a durable cross-restart counter needs a
	// republish-based retry topology, which is a further step up in
	// complexity this runner doesn't take.
	MaxAttempts int
	// Dedup, if set, is consulted before every Handler call (keyed by the
	// delivery's MessageId) and marked after a successful Ack — absorbing
	// redeliveries caused by a lost ack rather than reprocessing them.
	// Deliveries without a MessageId are never deduped (Dedup.Seen("") is
	// always false).
	Dedup *Dedup
}

// ConsumerRunner owns the dial → declare → consume → ack loop with a reconnect
// supervisor (exponential backoff, 1s→30s). It centralises the plumbing that
// dx-authz-go and dx-files-connect-api-go each hand-rolled; services supply
// only the topology Setup and a Handler returning an Outcome.
type ConsumerRunner struct {
	cfg    ConsumerConfig
	logger *zap.Logger

	attemptsMu sync.Mutex
	attempts   map[string]int

	// chMu guards ch, which is the runner's connection state: non-nil and open
	// exactly while it is consuming. Client and ReliablePublisher track theirs
	// the same way, for the same reason — see IsConnected.
	chMu sync.RWMutex
	ch   *amqp.Channel

	done chan struct{}
}

// NewConsumerRunner constructs a runner. No network IO until Run is called.
func NewConsumerRunner(cfg ConsumerConfig) *ConsumerRunner {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ConsumerRunner{cfg: cfg, logger: logger, attempts: make(map[string]int), done: make(chan struct{})}
}

// Run blocks until ctx is cancelled, reconnecting transparently on any
// failure. Call Run at most once per ConsumerRunner instance — use Stop to
// learn when a prior Run call has fully returned.
func (r *ConsumerRunner) Run(ctx context.Context, handler Handler) {
	defer close(r.done)

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		err := r.runOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		r.logger.Warn("consumer dropped, will reconnect",
			zap.String("queue", r.cfg.Queue), zap.Error(err), zap.Duration("backoff", backoff))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// Stop blocks until a prior Run call has fully returned, or until ctx is
// done — whichever comes first. Stop does not itself cancel anything; the
// caller must cancel the context it passed to Run. This exists so a caller
// that just cancelled Run's context can know it's now safe to release
// resources the handler depends on (a DB pool, for instance) rather than
// racing Run's final in-flight handler call.
func (r *ConsumerRunner) Stop(ctx context.Context) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *ConsumerRunner) runOnce(ctx context.Context, handler Handler) error {
	conn, err := amqp.Dial(r.cfg.URL)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer ch.Close()

	if r.cfg.Setup != nil {
		if err := r.cfg.Setup(ch); err != nil {
			return fmt.Errorf("topology setup: %w", err)
		}
	}
	if r.cfg.PrefetchCount > 0 {
		if err := ch.Qos(r.cfg.PrefetchCount, 0, false); err != nil {
			return fmt.Errorf("set qos: %w", err)
		}
	}

	notifyClose := ch.NotifyClose(make(chan *amqp.Error, 1))

	deliveries, err := ch.Consume(r.cfg.Queue, r.cfg.ConsumerTag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("start consuming: %w", err)
	}
	r.logger.Info("consumer connected", zap.String("queue", r.cfg.Queue))

	// Publish connection state only once consuming has actually started: a
	// dialled channel that never reached Consume is not delivering anything,
	// and a readiness probe that went green there would be reporting the
	// wrong thing. Cleared on the way out so a dropped connection is visible
	// during the reconnect backoff rather than after it.
	r.setChannel(ch)
	defer r.setChannel(nil)

	for {
		select {
		case <-ctx.Done():
			return nil
		case amqpErr := <-notifyClose:
			if amqpErr == nil {
				return errors.New("amqp channel closed cleanly")
			}
			return fmt.Errorf("amqp channel closed: %w", amqpErr)
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
			r.dispatch(ctx, d, handler)
		}
	}
}

func (r *ConsumerRunner) setChannel(ch *amqp.Channel) {
	r.chMu.Lock()
	defer r.chMu.Unlock()
	r.ch = ch
}

// IsConnected reports whether the runner is currently consuming on an open
// channel. It does not perform network IO, matching Client.IsConnected and
// ReliablePublisher.IsConnected — a readiness probe runs on every kubelet
// tick, so it must not dial.
//
// It is false before the first successful connect and for the whole of every
// reconnect backoff, which is the point: a consumer-only service whose broker
// is unreachable is delivering nothing, and readiness that says otherwise is
// the failure this exists to end.
func (r *ConsumerRunner) IsConnected() bool {
	r.chMu.RLock()
	defer r.chMu.RUnlock()
	return r.ch != nil && !r.ch.IsClosed()
}

// Check implements the platform's health.Checker (Check(ctx) error), so a
// runner can be registered directly as a readiness probe:
//
//	app.Probe("rabbitmq", consumer.Runner())
//
// The interface is satisfied STRUCTURALLY — this package deliberately does not
// import platform/observability/health, which would point a legacy messaging
// package at the platform tree and invert the dependency direction.
//
// Whether an unreachable broker should fail readiness or merely be reported is
// the service's call, not the platform's: register with Health.Add to gate
// traffic, or Health.AddOptional for a service that genuinely degrades.
func (r *ConsumerRunner) Check(context.Context) error {
	if !r.IsConnected() {
		return fmt.Errorf("rabbitmq: not consuming queue %q", r.cfg.Queue)
	}
	return nil
}

func (r *ConsumerRunner) dispatch(ctx context.Context, d Delivery, handler Handler) {
	// Continue the publisher's trace: extract its context from the delivery
	// headers and open a consumer span the handler runs under. A no-op until
	// observability.Init configures a TracerProvider and propagator.
	ctx, span := startConsumerSpan(ctx, d)
	defer span.End()

	if r.cfg.Dedup != nil && r.cfg.Dedup.Seen(d.MessageId) {
		r.logger.Info("duplicate delivery, acking without reprocessing", zap.String("messageId", d.MessageId))
		if err := d.Ack(false); err != nil {
			r.logger.Warn("ack failed; message may be redelivered", zap.Error(err))
		}
		return
	}

	outcome := handler(ctx, d)
	if outcome == Requeue && r.attemptExceeded(d.MessageId) {
		r.logger.Warn("max attempts exceeded, dead-lettering instead of requeuing",
			zap.String("messageId", d.MessageId), zap.Int("maxAttempts", r.cfg.MaxAttempts))
		outcome = DeadLetter
	}

	// Record the final outcome, so a dead-letter is observable (ROADMAP P1-14)
	// without polling queue depth, and stamp it onto the consumer span so a
	// poison/requeue trace carries ERROR status for tail sampling (review P1-3).
	deliveriesTotal.WithLabelValues(r.cfg.Queue, outcomeLabel(outcome)).Inc()
	recordConsumerOutcome(span, outcome)

	switch outcome {
	case Ack:
		if err := d.Ack(false); err != nil {
			r.logger.Warn("ack failed; message may be redelivered", zap.Error(err))
		}
		if r.cfg.Dedup != nil {
			r.cfg.Dedup.Mark(d.MessageId)
		}
		r.forgetAttempts(d.MessageId)
	case Requeue:
		if err := d.Nack(false, true); err != nil {
			r.logger.Warn("nack(requeue) failed; delivery state unknown", zap.Error(err))
		}
	case DeadLetter:
		if err := d.Nack(false, false); err != nil {
			r.logger.Warn("nack(dead-letter) failed; delivery state unknown", zap.Error(err))
		}
		r.forgetAttempts(d.MessageId)
	}
}

// attemptExceeded reports whether messageId has now exceeded MaxAttempts,
// bumping its counter as a side effect. Always false when MaxAttempts is
// unset (0) or messageId is empty (nothing stable to key on).
func (r *ConsumerRunner) attemptExceeded(messageID string) bool {
	if r.cfg.MaxAttempts <= 0 || messageID == "" {
		return false
	}
	r.attemptsMu.Lock()
	defer r.attemptsMu.Unlock()
	r.attempts[messageID]++
	return r.attempts[messageID] >= r.cfg.MaxAttempts
}

func (r *ConsumerRunner) forgetAttempts(messageID string) {
	if messageID == "" {
		return
	}
	r.attemptsMu.Lock()
	defer r.attemptsMu.Unlock()
	delete(r.attempts, messageID)
}

// Dedup is a fixed-capacity FIFO set of processed message/request IDs,
// absorbing redeliveries after a lost ack. Safe for concurrent use.
type Dedup struct {
	mu    sync.Mutex
	cap   int
	order []string
	set   map[string]struct{}
}

// NewDedup creates a Dedup retaining the most recent capacity IDs.
func NewDedup(capacity int) *Dedup {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Dedup{cap: capacity, set: make(map[string]struct{}, capacity)}
}

// Seen reports whether id was already marked. Empty ids are never seen.
func (d *Dedup) Seen(id string) bool {
	if id == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.set[id]
	return ok
}

// Mark records id, evicting the oldest entry past capacity. Empty ids are ignored.
func (d *Dedup) Mark(id string) {
	if id == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.set[id]; ok {
		return
	}
	if len(d.order) >= d.cap {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.set, oldest)
	}
	d.order = append(d.order, id)
	d.set[id] = struct{}{}
}
