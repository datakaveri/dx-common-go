package amqp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	dxmq "github.com/datakaveri/dx-common-go/messaging/rabbitmq"
)

// Replay tooling for quarantined events (ROADMAP P0-6).
//
// # What was missing
//
// Quarantine stopped events being destroyed: a version mismatch or an
// undecodable body now dead-letters instead of being acknowledged. But nothing
// read the dead-letter queue back, so P0-6's last acceptance criterion — "an
// operator can replay quarantined messages after remediation and observe the
// projection converge" — was unmet. A quarantine nobody can drain is a queue
// that grows until someone deletes it, which destroys the messages just as
// surely as the Ack did, only later and by hand.
//
// # Why this is an operator tool and not a background sweep
//
// A message is in the DLQ because a consumer could not process it. Replaying it
// before the consumer is fixed simply quarantines it again — a loop that burns
// broker and CPU and hides the depth signal the DLQ exists to raise. Replay is
// therefore explicit, bounded, and invoked AFTER remediation. That is also why
// Inspect exists separately: look before you move.
//
// # The property that matters most
//
// REPLAY MUST NOT LOSE THE MESSAGE IT IS TRYING TO SAVE. The naive
// implementation — consume, publish, ack — loses everything in flight if the
// publish fails or the process dies between the two. So this publishes with
// broker confirms and acknowledges the dead-letter copy only after the broker
// has confirmed the republished one. The failure mode that remains is a
// DUPLICATE (confirmed, then the ack is lost), which consumers must already
// tolerate: the bus is at-least-once, and the outbox that feeds it dedupes on
// request id.

// QuarantinedMessage is one message sitting in a dead-letter queue.
type QuarantinedMessage struct {
	// MessageID is the broker's message id, which the bus sets to the event id.
	MessageID string
	// OriginalRoutingKey is the topic the message was published with, recovered
	// from the broker's x-death header. Replay needs it: republishing under the
	// wrong key silently routes the message to the wrong consumers, or to none.
	OriginalRoutingKey string
	// Reason is why the broker dead-lettered it ("rejected", "expired", ...).
	Reason string
	// Deaths is how many times it has been dead-lettered. A value above one
	// means an earlier replay did not fix it.
	Deaths int64
	// Replays is how many times THIS tool has republished it.
	Replays int64
	// Body is the raw payload, exactly as published.
	Body []byte
	// Event is the decoded envelope when the body parses. An undecodable body
	// is precisely one of the cases quarantine exists for, so this being zero
	// is informative rather than an error.
	Event struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Version int    `json:"version"`
	}
}

// replayHeader counts republications by this tool.
//
// It exists to make a replay loop VISIBLE. Without it, an operator replaying a
// DLQ that still cannot be processed sees the same depth after every attempt
// with nothing to distinguish "new failures" from "the same message going
// round". With it, MaxReplays refuses to move a message that has already been
// tried too often, and the operator is told which ones.
const replayHeader = "x-dx-replays"

// DLQName is the dead-letter queue for a subscription, matching the topology
// DeclareQueueWithDLQ builds.
//
// Exported and derived in one place because an operator tool that computes this
// name differently from the topology is a tool that silently inspects nothing.
func DLQName(topic, group string) string {
	return queueName(topic, group) + ".dlq"
}

// ReplayOptions bounds one replay run.
type ReplayOptions struct {
	// Limit caps how many messages are moved. Zero means DefaultReplayLimit —
	// never unlimited, because a replay that runs until the queue is empty
	// against a still-broken consumer is the loop described above, at full
	// speed.
	Limit int
	// MaxReplays refuses to move a message republished this many times
	// already. Zero means DefaultMaxReplays.
	MaxReplays int64
	// DryRun reports what would move without moving anything.
	DryRun bool
}

const (
	// DefaultReplayLimit is a batch an operator can reason about, and small
	// enough that a mistaken replay is cheap to observe and stop.
	DefaultReplayLimit = 100
	// DefaultMaxReplays stops a message cycling. Two attempts is enough to
	// cover "the fix was deployed to one replica first"; a third means the fix
	// did not work and moving it again is not remediation.
	DefaultMaxReplays = 2
)

// ReplayResult reports one run.
type ReplayResult struct {
	// Replayed were republished to the original exchange and routing key.
	Replayed int
	// Skipped exceeded MaxReplays and were left in the DLQ, deliberately.
	Skipped []QuarantinedMessage
}

// Inspect returns up to limit quarantined messages for a subscription this bus
// owns, WITHOUT removing them.
func (b *Bus) Inspect(ctx context.Context, topic, group string, limit int) ([]QuarantinedMessage, error) {
	return b.InspectQueue(ctx, DLQName(topic, group), limit)
}

// InspectQueue is Inspect against a dead-letter queue named directly.
//
// It takes a NAME rather than a subscription because the queue that actually
// accumulates quarantined events today is not one this bus declared:
// dx-authz-go — the consumer P0-6 names in its scope — builds its own topology
// with its own configured DeadLetterQueue and consumes through
// messaging/rabbitmq directly. Tooling that could only address
// "<group>.<topic>.dlq" would be tooling for a bus nobody runs yet.
//
// It is a separate call from Replay because the first question about a DLQ is
// "what is in it", and answering that must not be destructive. Messages are
// fetched and then rejected back onto the queue, so the queue is unchanged.
func (b *Bus) InspectQueue(ctx context.Context, dlq string, limit int) ([]QuarantinedMessage, error) {
	if limit <= 0 {
		limit = DefaultReplayLimit
	}
	ch, closeAdmin, err := b.adminChannel()
	if err != nil {
		return nil, err
	}
	defer closeAdmin()

	var out []QuarantinedMessage
	// Nack with requeue puts each message back, so inspection is read-only.
	// They must all be held until the end, or the first requeue could be
	// redelivered and counted twice within the same loop.
	var held []amqp.Delivery
	defer func() {
		for _, d := range held {
			_ = d.Nack(false, true)
		}
	}()

	for len(out) < limit {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		d, ok, gerr := ch.Get(dlq, false)
		if gerr != nil {
			return out, fmt.Errorf("amqp: inspect %s: %w", dlq, gerr)
		}
		if !ok {
			break
		}
		held = append(held, d)
		out = append(out, describe(d))
	}
	return out, nil
}

// Replay republishes a subscription's quarantined messages to the exchange and
// routing key they arrived on, then acknowledges the dead-letter copies.
//
// Call it AFTER deploying the reader that can process them. Replaying into an
// unchanged consumer re-quarantines everything, which MaxReplays then refuses
// to do again.
func (b *Bus) Replay(ctx context.Context, topic, group string, opts ReplayOptions) (ReplayResult, error) {
	return b.ReplayQueue(ctx, DLQName(topic, group), opts)
}

// ReplayQueue is Replay against a dead-letter queue named directly. See
// InspectQueue for why the name is a parameter.
func (b *Bus) ReplayQueue(ctx context.Context, dlq string, opts ReplayOptions) (ReplayResult, error) {
	var res ReplayResult
	if opts.Limit <= 0 {
		opts.Limit = DefaultReplayLimit
	}
	if opts.MaxReplays <= 0 {
		opts.MaxReplays = DefaultMaxReplays
	}

	ch, closeAdmin, err := b.adminChannel()
	if err != nil {
		return res, err
	}
	defer closeAdmin()

	// Bound the run by the queue's depth AT START, not just by Limit.
	//
	// Without this a run can replay messages it republished moments earlier: a
	// consumer that still cannot read them quarantines them straight back, and
	// they land in the DLQ while this loop is still calling Get. The run then
	// spins until it hits Limit, replaying a handful of messages many times
	// each — the exact loop MaxReplays exists to stop, reintroduced inside a
	// single call where MaxReplays cannot see it (the counter only increments
	// on the copy that goes to the broker).
	//
	// Found by a test asserting one replay and observing two.
	q, derr := ch.QueueDeclarePassive(dlq, true, false, false, false, nil)
	if derr != nil {
		return res, fmt.Errorf("amqp: replay inspect depth of %s: %w", dlq, derr)
	}
	budget := q.Messages
	if budget > opts.Limit {
		budget = opts.Limit
	}

	// Messages skipped for exceeding MaxReplays are held unacknowledged until
	// the run ends and then requeued. Requeuing them one at a time inside the
	// loop would hand them straight back to Get and spin forever.
	var skipped []amqp.Delivery
	defer func() {
		for _, d := range skipped {
			_ = d.Nack(false, true)
		}
	}()

	for res.Replayed+len(res.Skipped) < budget {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		d, ok, gerr := ch.Get(dlq, false)
		if gerr != nil {
			return res, fmt.Errorf("amqp: replay get from %s: %w", dlq, gerr)
		}
		if !ok {
			break
		}

		msg := describe(d)
		if msg.Replays >= opts.MaxReplays {
			// Left in the DLQ on purpose. Moving it again is not remediation,
			// and reporting it is how an operator learns the fix did not work.
			skipped = append(skipped, d)
			res.Skipped = append(res.Skipped, msg)
			continue
		}
		if opts.DryRun {
			skipped = append(skipped, d)
			res.Replayed++
			continue
		}

		key := msg.OriginalRoutingKey
		if key == "" {
			// Without the original key the message cannot be routed back
			// correctly, and guessing would deliver it to the wrong consumers.
			// Leave it and say so.
			skipped = append(skipped, d)
			res.Skipped = append(res.Skipped, msg)
			b.log.Error("amqp: quarantined message has no original routing key; not replayed",
				zap.String("dlq", dlq), zap.String("id", msg.MessageID))
			continue
		}

		// Publish FIRST, with confirms, and acknowledge only once the broker
		// has it. The reverse order loses the message on a failed publish —
		// which is the one outcome a replay tool must never produce.
		if perr := b.pub.Publish(ctx, b.exchange, key, msg.Body, dxmq.PublishOptions{
			MessageID: msg.MessageID,
			Headers:   map[string]any{replayHeader: msg.Replays + 1},
		}); perr != nil {
			_ = d.Nack(false, true)
			return res, fmt.Errorf("amqp: replay publish %s: %w", msg.MessageID, perr)
		}
		if aerr := d.Ack(false); aerr != nil {
			// The republished copy is already durable, so the message is safe;
			// this one stays in the DLQ and will be replayed again, producing a
			// duplicate the consumers already tolerate. Report rather than
			// pretend the run was clean.
			return res, fmt.Errorf("amqp: replay ack %s (message WAS republished, expect a duplicate): %w",
				msg.MessageID, aerr)
		}
		res.Replayed++
		b.log.Info("amqp: replayed quarantined event",
			zap.String("dlq", dlq), zap.String("id", msg.MessageID),
			zap.String("routing_key", key), zap.Int64("replays", msg.Replays+1))
	}
	return res, nil
}

// Purge permanently deletes quarantined messages.
//
// It exists so that discarding a quarantine is an EXPLICIT, named act rather
// than something an operator does by deleting a queue in a management UI. That
// distinction is the whole of P0-6: the defect was messages disappearing
// silently, not messages ever being discarded.
func (b *Bus) Purge(ctx context.Context, topic, group string) (int, error) {
	return b.PurgeQueue(ctx, DLQName(topic, group))
}

// PurgeQueue is Purge against a dead-letter queue named directly.
func (b *Bus) PurgeQueue(ctx context.Context, dlq string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ch, closeAdmin, err := b.adminChannel()
	if err != nil {
		return 0, err
	}
	defer closeAdmin()

	n, err := ch.QueuePurge(dlq, false)
	if err != nil {
		return 0, fmt.Errorf("amqp: purge %s: %w", dlq, err)
	}
	b.log.Warn("amqp: PURGED quarantined events — they are gone",
		zap.String("dlq", dlq), zap.Int("count", n))
	return n, nil
}

// describe projects a delivery into the operator-facing shape.
func describe(d amqp.Delivery) QuarantinedMessage {
	m := QuarantinedMessage{
		MessageID: d.MessageId,
		Body:      d.Body,
	}
	if v, ok := d.Headers[replayHeader]; ok {
		m.Replays = toInt64(v)
	}
	// x-death is the broker's own record of the dead-lettering, and the only
	// place the ORIGINAL routing key survives: d.RoutingKey on a dead-lettered
	// delivery is the key it was routed to the DLQ with, not the one it was
	// published with.
	deaths, ok := d.Headers["x-death"].([]any)
	if !ok || len(deaths) == 0 {
		return m
	}
	first, ok := deaths[0].(amqp.Table)
	if !ok {
		return m
	}
	if reason, ok := first["reason"].(string); ok {
		m.Reason = reason
	}
	m.Deaths = toInt64(first["count"])
	if keys, ok := first["routing-keys"].([]any); ok && len(keys) > 0 {
		if k, ok := keys[0].(string); ok {
			m.OriginalRoutingKey = k
		}
	}
	_ = json.Unmarshal(d.Body, &m.Event)
	return m
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// adminChannel opens a short-lived channel for an operator command, and returns
// the closer for BOTH it and its connection.
//
// Deliberately not reusing a consumer's channel: an operator command must not
// be able to disturb a running subscription's prefetch or acknowledgements.
//
// It returns a closer rather than just the channel because closing the channel
// alone leaks the connection — one per operator command, held until the process
// exits. That is the kind of leak that only shows up as a broker running out of
// file descriptors during an incident, which is exactly when this tool is used.
func (b *Bus) adminChannel() (*amqp.Channel, func(), error) {
	conn, err := amqp.DialConfig(b.cfg.URL, amqp.Config{Heartbeat: 10 * time.Second})
	if err != nil {
		return nil, nil, fmt.Errorf("amqp: dial for admin channel: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("amqp: open admin channel: %w", err)
	}
	return ch, func() { _ = ch.Close(); _ = conn.Close() }, nil
}
