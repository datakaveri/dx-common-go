package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

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

	// owner identifies THIS dispatcher in claimed_by. Per-process, so an
	// abandoned lease is attributable to the replica that died holding it.
	owner string

	// lease is how long a claim is exclusive for. It must exceed the time to
	// publish a batch: a lease that expires mid-publish lets a second
	// dispatcher republish rows the first is still working through, which is
	// the defect this replaces, only slower.
	lease time.Duration
}

// NewOutbox builds an outbox over table.
//
// table is interpolated into SQL — a table name cannot be a bind parameter —
// so it MUST be a compile-time constant. Never derive it from input.
// OutboxOption adjusts an Outbox.
type OutboxOption func(*Outbox)

// WithLease sets how long a claim stays exclusive. It must exceed the time to
// publish a batch; see Outbox.lease.
func WithLease(d time.Duration) OutboxOption {
	return func(o *Outbox) {
		if d > 0 {
			o.lease = d
		}
	}
}

func NewOutbox(db dxsql.DB, table string, opts ...OutboxOption) *Outbox {
	// table is interpolated, never bound. MustIdent enforces the "compile-time
	// constant" contract: a name that is not a safe identifier panics here, at
	// construction, rather than reaching SQL. Every caller passes a literal.
	_ = dxsql.MustIdent(table)
	o := &Outbox{db: db, table: table, owner: newOwnerID(), lease: DefaultLease}
	for _, opt := range opts {
		opt(o)
	}
	return o
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
	// token is the claim this row was leased under. markSent requires it, so a
	// dispatcher that lost its lease cannot mark another's row sent.
	token string
}

// claim takes up to limit rows and LEASES them.
//
// The previous implementation relied on FOR UPDATE SKIP LOCKED alone and
// asserted that N dispatchers therefore took disjoint batches. That holds only
// for the duration of the one statement: the row locks are released when it
// ends, publication happens afterwards, and sent_at is still NULL — so a second
// dispatcher re-selects exactly the same rows and publishes them again
// (ROADMAP P0-5 / review finding H-03).
//
// The lease closes that window. A row is claimable only when unsent AND
// unleased-or-expired, and mark-sent requires the token this claim minted, so a
// dispatcher whose lease expired while it was publishing cannot mark a row sent
// that another dispatcher now owns.
func (o *Outbox) claim(ctx context.Context, limit int) ([]pending, error) {
	token := uuid.NewString()
	rows, err := o.db.Query(ctx, fmt.Sprintf(`
		WITH claimed AS (
			SELECT id FROM %s
			 WHERE sent_at IS NULL
			   AND (claimed_until IS NULL OR claimed_until < now())
			 ORDER BY created_at
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE %s o
		   SET attempts      = o.attempts + 1,
		       claimed_by    = $2,
		       claimed_until = now() + make_interval(secs => $3),
		       claim_token   = $4
		  FROM claimed c
		 WHERE o.id = c.id
	 RETURNING o.id, o.topic, o.payload`, o.table, o.table),
		limit, o.owner, o.lease.Seconds(), token)
	if err != nil {
		return nil, fmt.Errorf("outbox: claim: %w", err)
	}
	defer rows.Close()

	var out []pending
	for rows.Next() {
		var p pending
		var body []byte
		p.token = token
		if err := rows.Scan(&p.id, &p.topic, &body); err != nil {
			return nil, fmt.Errorf("outbox: scan: %w", err)
		}
		if err := json.Unmarshal(body, &p.event); err != nil {
			// A row that will never decode must not be claimed forever. It is
			// marked sent and reported, not retried — the alternative is a
			// dispatcher that stalls on one bad row and delivers nothing.
			if merr := o.markSent(ctx, p.id, token); merr != nil {
				return nil, merr
			}
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// markSent marks a row sent, but ONLY if this dispatcher still holds its lease.
//
// The token check is what makes the lease meaningful. Without it a dispatcher
// whose lease expired mid-publish would still mark the row sent — after another
// dispatcher had already re-claimed and republished it — and the duplicate
// would be invisible.
//
// It returns ErrLeaseLost when the row was re-claimed, which the caller treats
// as a warning rather than a failure: the event is not lost, it is being
// handled by whoever holds the lease now.
func (o *Outbox) markSent(ctx context.Context, id, token string) error {
	affected, err := o.db.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET sent_at = now(), claimed_until = NULL
		  WHERE id = $1 AND claim_token = $2`, o.table), id, token)
	if err != nil {
		return fmt.Errorf("outbox: mark sent: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: row %s", ErrLeaseLost, id)
	}
	return nil
}

// ErrLeaseLost signals that a row was re-claimed by another dispatcher before
// this one finished with it.
var ErrLeaseLost = errors.New("outbox: lease lost")

// DefaultLease is how long a claim is exclusive for. Generous relative to a
// publish, because the cost of a too-SHORT lease is a duplicate publish and the
// cost of a too-long one is only delay after a crash.
const DefaultLease = 60 * time.Second

// newOwnerID identifies this dispatcher process in claimed_by.
func newOwnerID() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), uuid.NewString()[:8])
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
		if err := d.outbox.markSent(ctx, p.id, p.token); err != nil {
			if errors.Is(err, ErrLeaseLost) {
				// Another dispatcher re-claimed this row while we were
				// publishing, so it may be published twice. That is
				// at-least-once, which is the contract — consumers deduplicate
				// on Event.ID — but a steady rate here means the lease is too
				// short for how long a publish actually takes.
				d.log.Warn("outbox lease lost before mark-sent; possible duplicate", "id", p.id, "topic", p.topic)
				continue
			}
			// Published but not marked: the next pass republishes it. Same
			// at-least-once contract.
			d.log.Error("outbox published but not marked sent", "id", p.id, "err", err)
		}
	}
	return len(rows), nil
}
