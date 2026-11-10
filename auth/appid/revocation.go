package appid

// RevocationConsumer drops a revoked application credential from this
// process's AppId caches as soon as the revocation is published — Java
// AppIdRevocationConsumer (dx-common), F58-s. Without it a revoked app keeps
// working until its cache entries expire. The gateway feeds it the
// VerifyAppId cache (Client.Invalidate); a dataplane its item-access cache.

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
)

// RevocationConfig wires the consumer.
type RevocationConfig struct {
	// URL is the AMQP URL of the vhost the revocation is published on (Java:
	// the internal vhost). Empty disables the consumer.
	URL string `mapstructure:"url"`
	// Exchange is Java's appIdRevokeExchange (default "revoked-appid").
	Exchange string `mapstructure:"exchange"`
}

// revocationKey is the key Java publishes and binds with.
const revocationKey = "##"

// Invalidator is a cache to clear (*Client is one).
type Invalidator interface{ Invalidate(appID string) }

// RevocationConsumer listens for revocations.
type RevocationConsumer struct {
	inv []Invalidator
	log *zap.Logger
	// queuePrefix names this process's private queue (e.g.
	// "gateway.appid-revocations.").
	queuePrefix string
}

// NewRevocationConsumer builds the consumer; every invalidator is told about
// each revoked app.
func NewRevocationConsumer(queuePrefix string, log *zap.Logger, inv ...Invalidator) *RevocationConsumer {
	return &RevocationConsumer{inv: inv, log: log, queuePrefix: queuePrefix}
}

// Run consumes until ctx is cancelled; returns at once when cfg.URL is empty.
//
// Every replica binds its OWN exclusive, auto-delete queue: one shared queue
// (Java declares "revoked-appid" for every consumer) delivers each message to
// only one replica, so the others would keep a revoked app cached. The
// exchange is Java's and is NOT declared here — only checked (passive), so a
// missing one is a logged, retried setup failure, never a broker object this
// service created.
func (c *RevocationConsumer) Run(ctx context.Context, cfg RevocationConfig) {
	if cfg.URL == "" {
		c.log.Warn("appId revocation consumer disabled — a revoked app stays valid until its cache entry expires")
		return
	}
	exchange := cfg.Exchange
	if exchange == "" {
		exchange = "revoked-appid"
	}
	host, _ := os.Hostname()
	queue := c.queuePrefix + host + "." + uuid.NewString()
	runner := dxmq.NewConsumerRunner(dxmq.ConsumerConfig{
		URL:         cfg.URL,
		Queue:       queue,
		ConsumerTag: strings.TrimSuffix(c.queuePrefix, "."),
		Logger:      c.log,
		Setup: func(ch *amqp.Channel) error {
			if err := ch.ExchangeDeclarePassive(exchange, "topic", true, false, false, false, nil); err != nil {
				return err
			}
			if _, err := ch.QueueDeclare(queue, false, true, true, false, nil); err != nil {
				return err
			}
			return ch.QueueBind(queue, revocationKey, exchange, false, nil)
		},
	})
	runner.Run(ctx, c.handle)
}

// handle is Java's message handler: {"appId": "..."} → invalidate; anything
// else is logged and dropped.
func (c *RevocationConsumer) handle(_ context.Context, d dxmq.Delivery) dxmq.Outcome {
	var msg struct {
		AppID string `json:"appId"`
	}
	if err := json.Unmarshal(d.Body, &msg); err != nil {
		c.log.Error("appId revocation message unparseable", zap.Error(err))
		return dxmq.Ack
	}
	if strings.TrimSpace(msg.AppID) == "" {
		c.log.Warn("appId revocation message without appId", zap.ByteString("body", d.Body))
		return dxmq.Ack
	}
	c.log.Info("revoking cached app credential", zap.String("app_id", msg.AppID))
	for _, inv := range c.inv {
		inv.Invalidate(msg.AppID)
	}
	return dxmq.Ack
}
