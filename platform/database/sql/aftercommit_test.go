package sql

import (
	"context"
	"errors"
	"testing"

	perrors "github.com/datakaveri/dx-common-go/platform/errors"
)

// ROADMAP P0-8 / review finding H-06: a retryable transaction must be
// side-effect free. These pin the mechanism that makes that achievable rather
// than merely requested.

func TestAfterCommit_RunsOnceAfterCommit(t *testing.T) {
	var order []string
	h := &hooks{}
	ctx := withHooks(context.Background(), h)

	if err := AfterCommit(ctx, func(context.Context) error {
		order = append(order, "hook")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Nothing ran yet — that is the whole point.
	if len(order) != 0 {
		t.Fatalf("hook ran before commit: %v", order)
	}
	order = append(order, "commit")
	if err := h.run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "commit" || order[1] != "hook" {
		t.Errorf("order = %v, want commit then hook", order)
	}
}

// TestAfterCommit_DiscardedWhenTheAttemptFails is the retry property. DoRetry
// installs a fresh hook set per attempt, so effects registered by an attempt
// that rolled back must not fire when a later attempt succeeds — otherwise a
// retried transaction sends N emails for one grant.
func TestAfterCommit_DiscardedWhenTheAttemptFails(t *testing.T) {
	sends := 0
	register := func(h *hooks) {
		_ = AfterCommit(withHooks(context.Background(), h), func(context.Context) error {
			sends++
			return nil
		})
	}

	// Attempt 1: registers, then "fails" — its hooks are dropped with it.
	failed := &hooks{}
	register(failed)

	// Attempt 2: fresh set, registers, commits.
	succeeded := &hooks{}
	register(succeeded)
	if err := succeeded.run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if sends != 1 {
		t.Errorf("side effect ran %d times across a retry, want exactly 1", sends)
	}
}

// TestAfterCommit_RunsInlineOutsideATransaction: a caller must not have to know
// whether it happens to be inside one.
func TestAfterCommit_RunsInlineOutsideATransaction(t *testing.T) {
	ran := false
	if err := AfterCommit(context.Background(), func(context.Context) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("outside a transaction the effect must run immediately, not be silently dropped")
	}
}

func TestAfterCommit_HookErrorIsReported(t *testing.T) {
	boom := errors.New("smtp down")
	h := &hooks{}
	ctx := withHooks(context.Background(), h)

	_ = AfterCommit(ctx, func(context.Context) error { return boom })
	if err := h.run(ctx); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the hook's error surfaced", err)
	}
}

func TestAfterCommit_RunsInRegistrationOrder(t *testing.T) {
	var got []int
	h := &hooks{}
	ctx := withHooks(context.Background(), h)
	for i := range 3 {
		i := i
		_ = AfterCommit(ctx, func(context.Context) error { got = append(got, i); return nil })
	}
	if err := h.run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("order = %v, want 0,1,2", got)
	}
}

// TestIsRetryable_OnlyConcurrencyFailures is the other half of P0-8.
//
// IsRetryable used to fall through to errors.IsRetryable, which is true for
// anything classified CodeDatabase — and Classify() wraps every unrecognised pg
// error as CodeDatabase. So a constraint violation retried the whole callback
// three times, repeating any side effect inside it, over an error that could
// never succeed.
func TestIsRetryable_OnlyConcurrencyFailures(t *testing.T) {
	// A platform error carrying the broad "database" classification must NOT be
	// treated as a transaction-retry candidate.
	dbErr := perrors.Wrap(errors.New("pq: duplicate key"), perrors.CodeDatabase, "database error")
	if IsRetryable(dbErr) {
		t.Error("a generic database error is retryable — every constraint violation would re-run the transaction and repeat its side effects")
	}
	if IsRetryable(errors.New("some random failure")) {
		t.Error("an unclassified error must not be retryable")
	}
	if IsRetryable(nil) {
		t.Error("nil must not be retryable")
	}
}
