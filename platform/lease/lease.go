// Package lease makes a named piece of work single-owner ACROSS REPLICAS.
//
// # What this is for (ROADMAP P0-12 / review finding H-10)
//
// dx-agent-runtime-go serialized a session's turns with a process-local
// sync.Mutex. That is correct on one replica and means nothing on two: both
// receive a message for the same session, both take their own local mutex, both
// run the plan loop, and they collide on the transcript's unique(session_id,
// seq) constraint, diverge the guardrail counters, and clobber each other's
// session status. The lock was doing its job; its scope was the process.
//
// A lease is the same exclusion with a scope that survives leaving the process:
// a row naming the owner and when the claim expires.
//
// # The property that makes it safe, and the one that is easy to miss
//
// Expiry is what stops a dead replica holding a session forever — but expiry is
// also what makes RELEASE DANGEROUS. Once a lease has expired and been taken
// over, the original owner may still be alive and still believe it holds the
// lease; if its Release deletes the row by name, it releases the NEW owner's
// claim and two replicas run at once. That is worse than the defect being
// fixed, because it appears only under the timing the lease exists to handle.
//
// So every mutating operation is FENCED by the owner token: Renew and Release
// affect the row only while `owner` still matches. A stale owner's Release is a
// no-op that reports ErrLost, which is the signal it should stop working.
//
// # Why not an advisory lock
//
// platform/database/sql.Lock takes a session-level advisory lock, which is
// released when the connection drops. That is the right primitive for a
// singleton job and the wrong one here: a replica that pauses (GC, a slow tool
// call) keeps its connection and therefore its lock, with no expiry, so a
// genuinely stuck turn is never taken over. A lease has a deadline that does not
// depend on a TCP connection staying up.
package lease

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
)

// ErrHeld means another owner currently holds the lease.
var ErrHeld = errors.New("lease: held by another owner")

// ErrLost means this owner no longer holds the lease — it expired and was taken
// over, or it was released.
//
// It is distinct from ErrHeld on purpose. ErrHeld answers "can I start?";
// ErrLost answers "should I stop?", and a holder that ignores it is the
// two-replicas-at-once case this package exists to prevent.
var ErrLost = errors.New("lease: no longer held by this owner")

// ErrInterrupted means someone has asked the work behind this lease to stop.
//
// It rides on Renew rather than having its own poll because the holder ALREADY
// round-trips to the database on every renewal, and "do I still own this?" and
// "should I stop?" are the same question asked at the same moment. A separate
// poll would double the query rate for a signal that fires approximately never.
//
// The consequence, stated plainly because it is a real limit rather than an
// implementation detail: an interrupt lands within one RENEWAL interval, not
// instantly. A caller needing sub-second cancellation of remote work needs a
// push channel, which is a different design and should be chosen deliberately.
var ErrInterrupted = errors.New("lease: interrupt requested")

// DefaultTTL is the lease duration when none is given.
//
// It must exceed the longest a holder can go without renewing, or a healthy
// holder is taken over mid-work. Thirty seconds suits a caller that renews on a
// timer of a few seconds; work that can block for minutes between renewals
// needs either a longer TTL or a renewal goroutine, and choosing wrongly is a
// correctness bug rather than a tuning preference.
const DefaultTTL = 30 * time.Second

// Store issues leases from one table.
type Store struct {
	db    dxsql.DB
	table string
}

// New builds a Store over table.
//
// The table must be:
//
//	CREATE TABLE <table> (
//	    name       text PRIMARY KEY,
//	    owner      text NOT NULL,
//	    expires_at timestamptz NOT NULL,
//	    interrupt_requested boolean NOT NULL DEFAULT false
//	);
//
// The primary key on name is the concurrency control — exactly one INSERT wins,
// and the rest are resolved by the ON CONFLICT predicate below. No application
// lock, no check-then-act.
func New(db dxsql.DB, table string) *Store {
	// table is interpolated; MustIdent enforces the compile-time-constant
	// contract (panics on an unsafe identifier). Callers pass a literal.
	_ = dxsql.MustIdent(table)
	return &Store{db: db, table: table}
}

// Lease is a held claim.
type Lease struct {
	store *Store
	name  string
	owner string
}

// Owner is the token that fences this claim. Exposed for logging: an abandoned
// lease is attributable to the replica that died holding it.
func (l *Lease) Owner() string { return l.owner }

// Acquire claims name for ttl, returning ErrHeld when someone else holds it.
//
// A lease whose expires_at has passed is taken over. The takeover is part of the
// same statement as the insert, so two replicas racing to take over an expired
// lease resolve to exactly one winner in the database rather than in a
// read-then-write window where both would see it expired.
func (s *Store) Acquire(ctx context.Context, name string, ttl time.Duration) (*Lease, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	owner := uuid.NewString()

	// The WHERE on the DO UPDATE is the whole mechanism: the row is overwritten
	// only if the existing claim has expired. Without it this would be
	// "last writer wins" and the lease would exclude nobody.
	n, err := s.db.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (name, owner, expires_at)
		VALUES ($1, $2, now() + make_interval(secs => $3))
		ON CONFLICT (name) DO UPDATE
		   SET owner = EXCLUDED.owner, expires_at = EXCLUDED.expires_at,
		       -- Clear the flag on takeover. An interrupt is aimed at the WORK
		       -- that was running, not at the name: leaving it set would abort
		       -- the next holder for a request that was already satisfied by
		       -- the previous one stopping.
		       interrupt_requested = false
		 WHERE %s.expires_at < now()`, s.table, s.table),
		name, owner, ttl.Seconds())
	if err != nil {
		return nil, fmt.Errorf("lease: acquire %q: %w", name, err)
	}
	if n == 0 {
		return nil, ErrHeld
	}
	return &Lease{store: s, name: name, owner: owner}, nil
}

// Renew extends the claim, and reports ErrLost if this owner no longer holds it.
//
// The owner check is not belt-and-braces: a holder that paused long enough to be
// taken over MUST NOT be able to extend the new owner's claim back to itself.
func (l *Lease) Renew(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	// One statement, wrapped in a CTE so it ALWAYS returns exactly one row.
	//
	// The obvious form — UPDATE ... RETURNING interrupt_requested — returns no
	// rows when this owner has been taken over, and "no rows" then has to be
	// told apart from a real query failure by matching on driver error text.
	// Aggregating over the CTE turns that into a count: zero means lost, and
	// every other error is genuinely an error.
	var updated int
	var interrupted bool
	err := l.store.db.QueryRow(ctx, fmt.Sprintf(`
		WITH renewed AS (
			UPDATE %s SET expires_at = now() + make_interval(secs => $1)
			 WHERE name = $2 AND owner = $3
		 RETURNING interrupt_requested
		)
		SELECT count(*), coalesce(bool_or(interrupt_requested), false) FROM renewed`,
		l.store.table), ttl.Seconds(), l.name, l.owner).Scan(&updated, &interrupted)
	if err != nil {
		return fmt.Errorf("lease: renew %q: %w", l.name, err)
	}
	if updated == 0 {
		return ErrLost
	}
	if interrupted {
		return ErrInterrupted
	}
	return nil
}

// Interrupt asks whoever holds name to stop, WHEREVER they are running.
//
// It sets a flag rather than cancelling anything directly, because the holder
// is in another process and there is nothing here to cancel. The holder sees it
// on its next renewal and stops itself — so this returns as soon as the request
// is durable, not when the work has actually ended.
//
// Setting it on a lease nobody holds is deliberately NOT an error: the work may
// have finished a moment earlier, and reporting that as a failure would make
// every interrupt racy at the caller.
func (s *Store) Interrupt(ctx context.Context, name string) error {
	if _, err := s.db.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET interrupt_requested = true WHERE name = $1`, s.table), name); err != nil {
		return fmt.Errorf("lease: interrupt %q: %w", name, err)
	}
	return nil
}

// Release drops the claim, and reports ErrLost if this owner no longer holds it.
//
// Fenced by owner for the reason in the package comment: releasing by name alone
// would let a stale holder release the CURRENT owner's claim, putting two
// replicas on the same work — the exact failure the lease prevents, reintroduced
// by its own cleanup path.
func (l *Lease) Release(ctx context.Context) error {
	n, err := l.store.db.Exec(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE name = $1 AND owner = $2`, l.store.table), l.name, l.owner)
	if err != nil {
		return fmt.Errorf("lease: release %q: %w", l.name, err)
	}
	if n == 0 {
		return ErrLost
	}
	return nil
}

// Holder reports the current owner of name, and whether the claim is live.
//
// For diagnostics and for a caller that wants to say WHICH replica holds a
// session rather than only that someone does.
func (s *Store) Holder(ctx context.Context, name string) (owner string, held bool, err error) {
	var expires time.Time
	row := s.db.QueryRow(ctx, fmt.Sprintf(
		`SELECT owner, expires_at FROM %s WHERE name = $1`, s.table), name)
	if err := row.Scan(&owner, &expires); err != nil {
		return "", false, nil //nolint:nilerr // absent row is "not held", not a failure
	}
	return owner, time.Now().Before(expires), nil
}
