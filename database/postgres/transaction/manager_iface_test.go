package transaction

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestManager_InTransaction_JoinsAmbientTxWithoutTouchingPool(t *testing.T) {
	outer := pgx.Tx(&dummyTx{})
	ctx := context.WithValue(context.Background(), txContextKey{}, outer)

	// pool is deliberately nil: NewManager(nil) plus an ambient tx must not
	// dereference the pool, matching InTransaction's own contract.
	m := NewManager(nil)

	called := false
	err := m.InTransaction(ctx, func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("fn was not invoked")
	}
}

func TestManager_InTransaction_PropagatesFnError(t *testing.T) {
	outer := pgx.Tx(&dummyTx{})
	ctx := context.WithValue(context.Background(), txContextKey{}, outer)
	m := NewManager(nil)

	want := errors.New("boom")
	err := m.InTransaction(ctx, func(_ context.Context) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("got error %v, want %v", err, want)
	}
}

func TestManager_InRetryableTransaction_JoinsAmbientTxWithoutTouchingPool(t *testing.T) {
	outer := pgx.Tx(&dummyTx{})
	ctx := context.WithValue(context.Background(), txContextKey{}, outer)
	m := NewManager(nil)

	called := false
	err := m.InRetryableTransaction(ctx, func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("fn was not invoked")
	}
}

func TestManager_InRetryableTransaction_AcceptsRetryConfig(t *testing.T) {
	outer := pgx.Tx(&dummyTx{})
	ctx := context.WithValue(context.Background(), txContextKey{}, outer)
	m := NewManager(nil)

	// A nested (ambient-tx) call joins directly and ignores cfg, but the
	// call must still type-check and run without panicking.
	err := m.InRetryableTransaction(ctx, func(_ context.Context) error {
		return nil
	}, RetryConfig{MaxAttempts: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
