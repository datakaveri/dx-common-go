// Package idempotency makes a high-risk action execute at most once, and makes
// a retry after an ambiguous outcome return the original result rather than
// repeating a charge or a provision (ROADMAP P0-11 / review finding H-09).
//
// The gap this closes: dx-mcp-gateway-go already sends a stable Idempotency-Key
// for state-changing tools (marketplace.purchase, subscription.create), and
// NO upstream stored it. So a retried tool call created a second Razorpay order
// or a second subscription, while the UI claimed exactly-once.
//
// # The mechanism
//
// A single table keyed on (scope, key) with a unique primary key. The database's
// uniqueness is the concurrency control — no application lock, no "check then
// act" race. A caller does:
//
//	rec, replay, err := store.Begin(ctx, scope, key)
//	switch {
//	case err != nil:            // includes ErrInProgress — a duplicate is mid-flight
//	case replay:                // rec holds the stored response; return it verbatim
//	default:                    // WE own this key; do the work, then Complete
//	    resp := doTheWork()
//	    store.Complete(ctx, scope, key, resp.Code, resp.Body)
//	}
//
// # What it guarantees, and what it does not
//
// It guarantees a repeated request with the same key returns the STORED response
// rather than re-running the work. That is the "return the original result"
// half of the objective, and it is complete.
//
// It does NOT by itself guarantee "exactly one charge" across a crash BETWEEN
// the external side effect and Complete. That window is real: a process that
// dies after charging Razorpay but before Complete leaves an `executing` row,
// and the retry sees ErrInProgress rather than the charge. Closing it needs
// either the external call to carry its OWN idempotency key (Razorpay supports
// this) or a reconciliation pass over stale `executing` rows. Both are the
// remainder of P0-11; this package makes them expressible by persisting the
// `executing` state durably rather than inferring it.
//
// Layer: L1. It depends only on platform/database/sql.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
)

// ErrInProgress means the key is reserved by an attempt that has not completed.
//
// It is distinct from "already done" on purpose: a caller that gets a completed
// record replays it, but a caller that finds an in-flight duplicate must NOT
// proceed — doing so is the double-charge this package exists to prevent. The
// right response to it is a 409, and a client retries after the first attempt
// resolves.
var ErrInProgress = errors.New("idempotency: an attempt with this key is in progress")

// Status values for a record.
const (
	statusExecuting = "executing"
	statusCompleted = "completed"
)

// Record is a completed idempotent outcome, replayed on a repeat.
type Record struct {
	StatusCode int
	Response   []byte
}

// Store persists idempotency keys in one table.
type Store struct {
	db    dxsql.DB
	table string
}

// New builds a Store over table.
//
// table is interpolated into SQL — a table name cannot be a bind parameter —
// so it must be a trusted constant, never request input.
func New(db dxsql.DB, table string) *Store {
	// table is interpolated; MustIdent enforces the compile-time-constant
	// contract (panics on an unsafe identifier). Callers pass a literal.
	_ = dxsql.MustIdent(table)
	return &Store{db: db, table: table}
}

// Begin reserves (scope, key) for this caller, or reports what already holds it.
//
//   - (nil, false, nil): newly reserved. The caller OWNS the key and must do the
//     work, then call Complete. If it never does (a crash), the row stays
//     `executing` and Begin returns ErrInProgress until a reconciler clears it —
//     which fails CLOSED, the correct direction for a payment.
//   - (rec, true, nil): the key already completed. Replay rec; do NOT re-run.
//   - (nil, false, ErrInProgress): a concurrent or crashed attempt holds it.
//
// The reservation is a single INSERT ... ON CONFLICT DO NOTHING. The unique key
// makes exactly one of N concurrent callers insert the row; the rest fall to the
// SELECT and see either completed (replay) or executing (ErrInProgress).
func (s *Store) Begin(ctx context.Context, scope, key string) (*Record, bool, error) {
	if scope == "" || key == "" {
		return nil, false, errors.New("idempotency: scope and key are required")
	}

	inserted, err := s.db.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s (scope, key, status) VALUES ($1, $2, '%s')
		 ON CONFLICT (scope, key) DO NOTHING`, s.table, statusExecuting), scope, key)
	if err != nil {
		return nil, false, fmt.Errorf("idempotency: reserve: %w", err)
	}
	if inserted == 1 {
		// We won the race — nobody had this key.
		return nil, false, nil
	}

	// Somebody else holds it. Read the existing row to decide replay vs wait.
	var status string
	var code *int
	var response []byte
	row := s.db.QueryRow(ctx, fmt.Sprintf(
		`SELECT status, status_code, response FROM %s WHERE scope = $1 AND key = $2`,
		s.table), scope, key)
	if err := row.Scan(&status, &code, &response); err != nil {
		return nil, false, fmt.Errorf("idempotency: read existing: %w", err)
	}
	if status == statusCompleted {
		rec := &Record{Response: response}
		if code != nil {
			rec.StatusCode = *code
		}
		return rec, true, nil
	}
	return nil, false, ErrInProgress
}

// Complete records the outcome for a key this caller reserved with Begin.
//
// It updates only a row still `executing`, so a late Complete from a crashed
// attempt that a reconciler already cleared cannot overwrite a newer result.
func (s *Store) Complete(ctx context.Context, scope, key string, statusCode int, response []byte) error {
	affected, err := s.db.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET status = '%s', status_code = $3, response = $4, completed_at = now()
		  WHERE scope = $1 AND key = $2 AND status = '%s'`,
		s.table, statusCompleted, statusExecuting), scope, key, statusCode, response)
	if err != nil {
		return fmt.Errorf("idempotency: complete: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("idempotency: complete: no executing row for %s/%s (already completed or reclaimed)", scope, key)
	}
	return nil
}

// Release deletes a reservation, for the failure path: an attempt that failed
// BEFORE any external side effect should free its key so an immediate retry can
// proceed rather than being told ErrInProgress for nothing.
//
// It must NOT be called after a side effect has fired — that is the crash window
// the package documentation describes, and deleting the key there would let the
// retry re-charge.
func (s *Store) Release(ctx context.Context, scope, key string) error {
	_, err := s.db.Exec(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE scope = $1 AND key = $2 AND status = '%s'`,
		s.table, statusExecuting), scope, key)
	if err != nil {
		return fmt.Errorf("idempotency: release: %w", err)
	}
	return nil
}

// Stale returns the keys in scope that have been `executing` longer than age.
//
// It exists so a caller can RESOLVE a stranded key rather than only discard it,
// which Reclaim alone cannot do. The distinction matters: Reclaim frees the key
// for a fresh attempt, but if the original attempt's side effect DID commit,
// the right outcome is to complete the key with that result so a retry replays
// it — one order, not two. Only the service knows how to look for that
// evidence, so this hands it the keys and stays out of the decision.
//
// Ordered oldest-first: a reconciler that can only process part of a backlog
// should clear the keys that have been stuck longest, since those are the ones
// whose owners have been unable to retry.
func (s *Store) Stale(ctx context.Context, scope string, age time.Duration) ([]string, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(
		`SELECT key FROM %s
		  WHERE scope = $1 AND status = '%s' AND created_at < now() - make_interval(secs => $2)
		  ORDER BY created_at`,
		s.table, statusExecuting), scope, age.Seconds())
	if err != nil {
		return nil, fmt.Errorf("idempotency: stale: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("idempotency: stale scan: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("idempotency: stale: %w", err)
	}
	return keys, nil
}

// Reclaim deletes `executing` rows older than age — the reconciliation hook for
// keys stranded by a crash between side effect and Complete.
//
// It is deliberately minimal and deliberately NOT called automatically: whether
// a stranded key is safe to reclaim depends on whether its external side effect
// committed, which this package cannot know. A service wires it with an age that
// exceeds its own external-call timeout, and only where the side effect is
// itself idempotent or absent. Returns how many rows it cleared.
func (s *Store) Reclaim(ctx context.Context, age time.Duration) (int64, error) {
	n, err := s.db.Exec(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE status = '%s' AND created_at < now() - make_interval(secs => $1)`,
		s.table, statusExecuting), age.Seconds())
	if err != nil {
		return 0, fmt.Errorf("idempotency: reclaim: %w", err)
	}
	return n, nil
}
