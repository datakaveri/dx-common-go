package sql

import (
	"context"
	"hash/fnv"
	"reflect"
	"time"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// Manager owns transaction lifecycle and propagation.
//
// Note the callback: func(context.Context) error, with NO tx parameter. The
// transaction rides on the context and repositories pick it up, which removes
// pgx.Tx from every service signature and removes the temptation to use the raw
// transaction directly — the two reasons *pgxpool.Pool ended up inside
// dx-acl-go's service layer.
type Manager interface {
	// Do runs fn in a transaction, committing on nil and rolling back
	// otherwise.
	//
	// When ctx already carries a transaction, fn JOINS it and only the
	// outermost Do commits. That is what lets a service compose several
	// independently-transactional repository calls into one atomic unit without
	// any call site knowing whether it is nested.
	Do(ctx context.Context, fn func(context.Context) error, opts ...TxOption) error

	// DoRetry is Do plus retry on serialization failure (40001) and deadlock
	// (40P01) with jittered backoff.
	//
	// fn MUST be safe to re-run from scratch — it will be. A nested DoRetry
	// does not retry: a transaction that has already failed cannot be salvaged
	// from the inside, and retrying there would re-run only part of the work.
	DoRetry(ctx context.Context, fn func(context.Context) error, opts ...TxOption) error

	// Lock runs fn holding a session-level advisory lock, returning
	// ErrLockHeld immediately when it is taken elsewhere — it does not block.
	//
	// This is the singleton primitive for a non-idempotent recurring job. There
	// is no leader election in this platform; for queue-shaped work prefer
	// Query.ForUpdate(skipLocked) instead, which needs no coordination at all.
	Lock(ctx context.Context, key string, fn func(context.Context) error) error
}

// ErrLockHeld is returned by Lock when another session holds the lock.
var ErrLockHeld = errors.Conflict("advisory lock is held by another session")

type ctxTxKey struct{}

// TxFrom returns the transaction on ctx, if any. Repositories use it to bind to
// an ambient transaction.
func TxFrom(ctx context.Context) (Tx, bool) {
	tx, ok := ctx.Value(ctxTxKey{}).(Tx)
	return tx, ok
}

func withTx(ctx context.Context, tx Tx) context.Context {
	return context.WithValue(ctx, ctxTxKey{}, tx)
}

// NewManager builds a Manager over db.
func NewManager(db DB) Manager { return &manager{db: db} }

type manager struct{ db DB }

func (m *manager) Do(ctx context.Context, fn func(context.Context) error, opts ...TxOption) error {
	// Already in a transaction: join it. Only the outermost Do commits.
	if _, ok := TxFrom(ctx); ok {
		return fn(ctx)
	}

	tx, err := m.db.Begin(ctx, opts...)
	if err != nil {
		return err
	}

	// Rollback on panic as well as on error. Without this a panic mid-write
	// leaves the transaction open until the connection is reaped, holding its
	// locks the whole time.
	committed := false
	defer func() {
		if !committed {
			// Deliberately a fresh context: ctx may already be cancelled, and a
			// rollback that cannot run is how a connection leaks.
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	if err := fn(withTx(ctx, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

const (
	maxTxAttempts  = 3
	retryBaseDelay = 10 * time.Millisecond
)

func (m *manager) DoRetry(ctx context.Context, fn func(context.Context) error, opts ...TxOption) error {
	// Nested: a broken outer transaction cannot be salvaged from the inside,
	// and re-running fn alone would repeat only part of the work.
	if _, ok := TxFrom(ctx); ok {
		return fn(ctx)
	}

	var lastErr error
	for attempt := 1; attempt <= maxTxAttempts; attempt++ {
		err := m.Do(ctx, fn, opts...)
		if err == nil {
			return nil
		}
		lastErr = err
		if !IsRetryable(err) {
			return err
		}
		if attempt == maxTxAttempts {
			break
		}
		// Jittered backoff. Without jitter, two transactions that deadlocked
		// together wake together and deadlock again.
		delay := retryBaseDelay * time.Duration(1<<(attempt-1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay + jitter(delay)):
		}
	}
	return lastErr
}

// Lock takes a session-level advisory lock on a DEDICATED connection held for
// the callback's whole duration.
//
// The dedicated connection is not an optimisation, it is the correctness
// requirement. pg_try_advisory_lock is scoped to a SESSION, and a pooled
// connection is returned to the pool the moment the query finishes — so taking
// the lock through the pool releases nothing, excludes nobody, and (because
// advisory locks are re-entrant within a session) lets the very next caller
// that happens to be handed the same connection acquire it again. An advisory
// singleton that does not exclude is worse than none: the job it guards runs
// twice while appearing guarded.
func (m *manager) Lock(ctx context.Context, key string, fn func(context.Context) error) error {
	locker, ok := m.db.(interface {
		acquireLock(ctx context.Context, id int64) (release func(), acquired bool, err error)
	})
	if !ok {
		return errors.Internal("sql: advisory locks need a pooled connection")
	}

	release, acquired, err := locker.acquireLock(ctx, advisoryKey(key))
	if err != nil {
		return MapError(err)
	}
	if !acquired {
		return ErrLockHeld
	}
	defer release()

	return fn(ctx)
}

// advisoryKey hashes a name into the int64 pg_advisory_lock expects.
//
// Collisions are possible in principle. They are harmless here: two jobs that
// collide serialise against each other, which is a small loss of concurrency,
// never a loss of safety.
func advisoryKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64())
}

// jitter returns a pseudo-random fraction of d, derived without a global RNG so
// the package holds no mutable package-level state.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	n := time.Now().UnixNano()
	return time.Duration(n % int64(d))
}

// columnsOf derives the column list from T's `db:"…"` tags.
func columnsOf[T any]() []Column {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	out := make([]Column, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, ok := f.Tag.Lookup("db")
		if !ok || tag == "-" {
			continue
		}
		if i := indexByte(tag, ','); i >= 0 {
			tag = tag[:i]
		}
		if tag != "" {
			out = append(out, Column(tag))
		}
	}
	return out
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
