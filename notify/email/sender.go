package email

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/messaging/rabbitmq"
)

// EventPublisher publishes one AMQP message. *rabbitmq.ReliablePublisher
// satisfies it, so a service reuses the broker connection it already has.
type EventPublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, body []byte, opts rabbitmq.PublishOptions) error
}

// asyncTimeout bounds a detached SendAsync publish.
const asyncTimeout = 10 * time.Second

// Sender publishes Events to the email queue. It is the one publishing path for
// every service, so they all stamp the same envelope.
//
// A nil Sender, or one whose Config is disabled, publishes nothing: callers
// wire it unconditionally and let config decide, as with Notifier.
type Sender struct {
	cfg    Config
	pub    EventPublisher
	source string
	logger *zap.Logger
	now    func() time.Time
	newID  func() string
}

// NewSender builds a Sender that publishes as source (e.g. "dx-marketplace").
// With cfg.Exchange set it publishes there with cfg.RoutingKey (the event's
// emailType when empty); otherwise
// to cfg.Queue (DefaultQueue when empty) on the default exchange. Only
// Enabled, Exchange, RoutingKey and Queue are used; the template fields belong to Request.
func NewSender(cfg Config, pub EventPublisher, source string, logger *zap.Logger) *Sender {
	if cfg.Queue == "" {
		cfg.Queue = DefaultQueue
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Sender{
		cfg: cfg, pub: pub, source: source, logger: logger,
		now:   func() time.Time { return time.Now().UTC() },
		newID: uuid.NewString,
	}
}

// Enabled reports whether Send publishes anything.
func (s *Sender) Enabled() bool { return s != nil && s.cfg.Enabled && s.pub != nil }

// Send fills the envelope (schemaVersion, eventId, source, occurredAt and the
// trace id when unset), validates the event and publishes it. A disabled
// Sender returns nil. The message carries eventId as its AMQP message id and
// emailType as its type.
func (s *Sender) Send(ctx context.Context, ev Event) error {
	if !s.Enabled() {
		return nil
	}
	ev = s.stamp(ctx, ev)
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("email: invalid %s event: %w", ev.EmailType, err)
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("email: encode event: %w", err)
	}
	exchange, routingKey := s.route(ev)
	if err := s.pub.Publish(ctx, exchange, routingKey, body, rabbitmq.PublishOptions{
		MessageID: ev.EventID,
		Type:      ev.EmailType,
	}); err != nil {
		return fmt.Errorf("email: publish %s: %w", ev.EmailType, err)
	}
	return nil
}

// SendAsync publishes on a detached goroutine and only logs a failure: an
// email must never fail, or wait on, the request that triggered it. The
// request's trace and values carry over; its cancellation does not.
func (s *Sender) SendAsync(ctx context.Context, ev Event) {
	if !s.Enabled() {
		return
	}
	// Stamp now, so occurredAt is the event's time and the caller's later
	// changes to its slices and map cannot race the goroutine.
	ev = s.stamp(ctx, ev)
	ev.RecipientIDs = slices.Clone(ev.RecipientIDs)
	ev.RecipientRoles = slices.Clone(ev.RecipientRoles)
	ev.Variables = maps.Clone(ev.Variables)
	if ev.Entity != nil {
		entity := *ev.Entity
		ev.Entity = &entity
	}

	ctx = context.WithoutCancel(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, asyncTimeout)
		defer cancel()
		if err := s.Send(ctx, ev); err != nil {
			s.logger.Warn("email notification not published",
				zap.String("email_type", ev.EmailType), zap.String("event_id", ev.EventID), zap.Error(err))
		}
	}()
}

// route is where ev goes: the exchange with the configured key (else the
// emailType), or the queue.
func (s *Sender) route(ev Event) (exchange, routingKey string) {
	if s.cfg.Exchange != "" {
		if s.cfg.RoutingKey != "" {
			return s.cfg.Exchange, s.cfg.RoutingKey
		}
		return s.cfg.Exchange, ev.EmailType
	}
	return "", s.cfg.Queue
}

func (s *Sender) stamp(ctx context.Context, ev Event) Event {
	ev.SchemaVersion = SchemaVersion
	if ev.EventID == "" {
		ev.EventID = s.newID()
	}
	if ev.Source == "" {
		ev.Source = s.source
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = s.now()
	}
	if ev.TraceID == "" {
		if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
			ev.TraceID = sc.TraceID().String()
		}
	}
	return ev
}
