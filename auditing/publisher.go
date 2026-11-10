package auditing

import (
	"context"
	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
)

// Config wires the audit publisher. The exchange lives on the controlplane's
// internal vhost (Java default: exchange "auditing", routing key "##",
// vhost /internal).
type Config struct {
	Enabled    bool   `mapstructure:"enabled"`
	URL        string `mapstructure:"url"` // AMQP URL incl. /internal vhost
	Exchange   string `mapstructure:"exchange"`
	RoutingKey string `mapstructure:"routing_key"`
}

func (c *Config) applyDefaults() {
	if c.Exchange == "" {
		c.Exchange = "auditing"
	}
	if c.RoutingKey == "" {
		c.RoutingKey = "##"
	}
}

// Publisher sends audit records. A nil Publisher is a no-op, so callers can
// wire it unconditionally and let config decide.
type Publisher struct {
	pub        *dxmq.ReliablePublisher
	exchange   string
	routingKey string
	logger     *zap.Logger
}

// NewPublisher connects to the audit vhost. Returns (nil, nil) when disabled.
//
// It only publishes — to cfg.Exchange with cfg.RoutingKey — and declares
// nothing: the exchange, the queues and the bindings belong to whoever runs the
// broker (the RabbitMQ definitions, dx-audit-go's consumer). So the broker user
// needs "write" on the exchange only, and a missing exchange shows up as a
// failed publish (or, at startup, through CheckExchange) instead of being
// created unbound.
func NewPublisher(cfg Config, logger *zap.Logger) (*Publisher, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	cfg.applyDefaults()
	pub, err := dxmq.NewReliablePublisher(dxmq.PublisherConfig{
		URL:    cfg.URL,
		Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	return &Publisher{
		pub:        pub,
		exchange:   cfg.Exchange,
		routingKey: cfg.RoutingKey,
		logger:     logger,
	}, nil
}

// Publish is fire-and-forget: an audit failure is logged, never surfaced to
// the request path (matching the Java AuditingHandler's behaviour).
func (p *Publisher) Publish(rec *Record) {
	if p == nil || rec == nil {
		return
	}
	if err := p.pub.PublishJSON(p.exchange, p.routingKey, rec); err != nil {
		// WARN: the request already succeeded and the broker connection
		// reports its own outage; this line names the record that was lost.
		p.logger.Warn("audit publish failed",
			zap.String("action", rec.Action),
			zap.String("api", rec.API),
			zap.Error(err))
	}
}

// PublishEvent sends a gateway security event on this publisher's exchange
// and routing key. The gateway constructs a dedicated Publisher pointed at
// the "gateway-logs" exchange for these.
func (p *Publisher) PublishEvent(ev *GatewayEvent) {
	if p == nil || ev == nil {
		return
	}
	if err := p.pub.PublishJSON(p.exchange, p.routingKey, ev); err != nil {
		p.logger.Warn("gateway event publish failed",
			zap.String("event", ev.Event), zap.Error(err))
	}
}

// Close releases the underlying AMQP connection.
// CheckExchange confirms the audit exchange exists, creating nothing (see
// dxmq.ReliablePublisher.CheckExchange): dxmq.ErrExchangeNotFound when it is
// missing, dxmq.ErrNotConnected while the broker is unreachable. A service
// calls it at startup to fail fast on a missing or misspelt exchange.
func (p *Publisher) CheckExchange(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return p.pub.CheckExchange(ctx, p.exchange)
}

// Exchange is the exchange records are published to.
func (p *Publisher) Exchange() string {
	if p == nil {
		return ""
	}
	return p.exchange
}

func (p *Publisher) Close() {
	if p == nil {
		return
	}
	p.pub.Close()
}
