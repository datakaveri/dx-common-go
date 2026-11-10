package scheduler

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tryAdvisoryLock attempts to acquire a session-level PostgreSQL advisory
// lock on key without blocking (pg_try_advisory_lock) — unlike
// postgres.WithAdvisoryLock (which blocks until the lock is free),
// WithSingleton needs "another replica already has it, skip this tick" to
// return immediately rather than queue up. ok=false means another session
// (replica) currently holds the lock. On ok=true, the caller must call
// unlock exactly once when done; it releases the lock and returns the
// connection to the pool.
func tryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, key int64) (unlock func(), ok bool, err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("scheduler: acquire connection: %w", err)
	}

	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("scheduler: pg_try_advisory_lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}

	unlock = func() {
		// Best-effort unlock on the same connection; PostgreSQL also
		// releases session-level advisory locks automatically when the
		// connection closes, so a failed unlock here is not a leak.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", key)
		conn.Release()
	}
	return unlock, true, nil
}

// lockKey derives a deterministic 64-bit advisory-lock key from a job name.
// PostgreSQL advisory lock keys are process-wide 64-bit integers, not
// strings — see Job.Name's doc comment on namespacing to avoid collisions.
func lockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64())
}
