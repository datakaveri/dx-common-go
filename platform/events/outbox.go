package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
)

// Outbox is the transactional outbox: an event is written in the SAME
// transaction as the domain change that caused it, then published later.
//
// # Why it exists
//
// Publishing inside a write path gives you two systems and no transaction
// between them. Publish-then-commit can publish an event for a change that
// rolls back; commit-then-publish can lose the event if the process dies in
// between. Both failures are silent and both corrupt the consumer's view. The
// outbox makes the event part of the write, so it is exactly as durable.
//
// # Why this replaces messaging/outbox
//
// The legacy store's Insert takes a concrete pgx.Tx. That signature is what
// pinned every service using it to the legacy transaction manager: adopting
// platform/database/sql would have split the outbox write away from the domain
// write, silently, with nothing failing. This one takes a dxsql.Querier, which
// both a DB and a Tx satisfy — so the SAME call joins an ambient transaction
// when there is one and runs standalone when there is not.
type Outbox struct {
	db    dxsql.DB
	table string
}

// NewOutbox builds an outbox over table.
//
// table is interpolated into SQL — a table name cannot be a bind parameter —
// so it MUST be a compile-time constant. Never derive it from input.
func NewOutbox(db dxsql.DB, table string) *Outbox {
	return &Outbox{db: db, table: table}
}

// Write records an event for later publication.
//
// It uses the transaction on ctx when there is one, which is the entire point:
// call it inside sql.Manager.Do alongside the domain write and the two commit
// together or not at all.
func (o *Outbox) Write(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("outbox: encoding event: %w", err)
	}
	q := dxsql.Conn(ctx, o.db)
	_, err = q.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s (id, topic, payload, created_at) VALUES ($1, $2, $3, now())`, o.table),
		e.ID, e.Type, body)
	if err != nil {
		return fmt.Errorf("outbox: insert: %w", err)
	}
	return nil
}

// Publish is Topic.Publish's outbox equivalent: encode, envelope, and write
// transactionally instead of sending now.
func Publish[T any](ctx context.Context, o *Outbox, t Topic[T], payload T, opts ...Option) error {
	e, err := t.event(payload, opts...)
	if err != nil {
		return err
	}
	return o.Write(ctx, e)
}

// pending is one claimed row.
type pending struct {
	id    string
	topic string
	event Event
}

// claim atomically takes up to limit unsent rows.
//
// FOR UPDATE SKIP LOCKED is what makes this safe to run on every replica: each
// dispatcher takes a disjoint set, so N replicas drain N times faster instead
// of publishing each event N times.
func (o *Outbox) claim(ctx context.Context, limit int) ([]pending, error) {
	rows, err := o.db.Query(ctx, fmt.Sprintf(`
		WITH claimed AS (
			SELECT id FROM %s
			 WHERE sent_at IS NULL
			 ORDER BY created_at
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE %s o
		   SET attempts = o.attempts + 1
		  FROM claimed c
		 WHERE o.id = c.id
	 RETURNING o.id, o.topic, o.payload`, o.table, o.table), limit)
	if err != nil {
		return nil, fmt.Errorf("outbox: claim: %w", err)
	}
	defer rows.Close()

	var out []pending
	for rows.Next() {
		var p pending
		var body []byte
		if err := rows.Scan(&p.id, &p.topic, &body); err != nil {
			return nil, fmt.Errorf("outbox: scan: %w", err)
		}
		if err := json.Unmarshal(body, &p.event); err != nil {
			// A row that will never decode must not be claimed forever. It is
			// marked sent and reported, not retried — the alternative is a
			// dispatcher that stalls on one bad row and delivers nothing.
			if merr := o.markSent(ctx, p.id); merr != nil {
				return nil, merr
			}
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (o *Outbox) markSent(ctx context.Context, id string) error {
	_, err := o.db.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET sent_at = now() WHERE id = $1`, o.table), id)
	if err != nil {
		return fmt.Errorf("outbox: mark sent: %w", err)
	}
	return nil
}

// Dispatcher drains an outbox to a bus.
type Dispatcher struct {
	outbox *Outbox
	bus    Bus
	batch  int
	log    Logger
	kick   chan struct{}
}

// Logger is the narrow logging surface this package needs, so events does not
// force a logger choice on its consumers.
type Logger interface {
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

// nopLogger is used when none is supplied.
type nopLogger struct{}

func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// NewDispatcher builds a dispatcher. batch bounds one drain pass.
func NewDispatcher(o *Outbox, b Bus, batch int, log Logger) *Dispatcher {
	if batch <= 0 {
		batch = 100
	}
	if log == nil {
		log = nopLogger{}
	}
	return &Dispatcher{outbox: o, bus: b, batch: batch, log: log, kick: make(chan struct{}, 1)}
}

// Kick requests an immediate drain. Non-blocking and coalescing: call it right
// after a write so an event does not wait for the next tick.
func (d *Dispatcher) Kick() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// Run drains on every tick until ctx is cancelled. Pass it to
// bootstrap.App.Background.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-d.kick:
		}
		// Keep draining while a pass fills its batch: after a burst there is
		// more waiting, and sleeping a full interval between batches turns a
		// spike into minutes of lag.
		for {
			n, err := d.drain(ctx)
			if err != nil {
				d.log.Error("outbox drain failed", "err", err)
				break
			}
			if n < d.batch {
				break
			}
		}
	}
}

// drain publishes one batch and returns how many rows it claimed.
func (d *Dispatcher) drain(ctx context.Context) (int, error) {
	rows, err := d.outbox.claim(ctx, d.batch)
	if err != nil {
		return 0, err
	}
	for _, p := range rows {
		if err := d.bus.Publish(ctx, p.topic, p.event); err != nil {
			// Left unsent deliberately: the attempt counter was already bumped
			// by claim, and the next pass retries it. Marking it sent here
			// would lose the event.
			d.log.Warn("outbox publish failed; will retry", "id", p.id, "topic", p.topic, "err", err)
			continue
		}
		if err := d.outbox.markSent(ctx, p.id); err != nil {
			// Published but not marked: the next pass republishes it. That is
			// at-least-once, which is the contract — consumers deduplicate on
			// Event.ID.
			d.log.Error("outbox published but not marked sent", "id", p.id, "err", err)
		}
	}
	return len(rows), nil
}
