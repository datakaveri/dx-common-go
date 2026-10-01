package transaction

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Manager runs work inside a transaction without leaking *pgxpool.Pool into
// the caller. The tx is carried on the context (see TxFromContext), so
// repositories invoked from fn need NO signature change — they pick up the
// ambient transaction from ctx automatically, exactly as they do with the
// package-level InTransaction/InRetryableTransaction.
//
// Services should depend on this interface instead of a raw *pgxpool.Pool.
type Manager interface {
	InTransaction(ctx context.Context, fn func(context.Context) error) error
	InRetryableTransaction(ctx context.Context, fn func(context.Context) error, cfg ...RetryConfig) error
}

// poolManager is the pool-backed Manager implementation.
type poolManager struct {
	pool *pgxpool.Pool
}

// NewManager returns a Manager backed by pool.
func NewManager(pool *pgxpool.Pool) Manager {
	return &poolManager{pool: pool}
}

func (m *poolManager) InTransaction(ctx context.Context, fn func(context.Context) error) error {
	return InTransaction(ctx, m.pool, func(ctx context.Context, _ pgx.Tx) error {
		return fn(ctx)
	})
}

func (m *poolManager) InRetryableTransaction(ctx context.Context, fn func(context.Context) error, cfg ...RetryConfig) error {
	return InRetryableTransaction(ctx, m.pool, func(ctx context.Context, _ pgx.Tx) error {
		return fn(ctx)
	}, cfg...)
}
