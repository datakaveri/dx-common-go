// Package transaction provides transaction management and propagation for
// PostgreSQL: WithTransaction/InTransaction begin/commit/rollback (the
// latter propagating an ambient transaction through context so repositories
// several calls deep join it automatically), WithRetryableTx/
// InRetryableTransaction retry on serialization failure/deadlock, and
// WithAdvisoryLock provides session-level advisory locking. Connection
// pooling lives in the sibling database/postgres/client package.
package transaction

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// txContextKey is unexported so only this package can stash/retrieve the
// ambient transaction — callers only ever see it through TxFromContext.
type txContextKey struct{}

// TxFromContext returns the transaction InTransaction stashed on ctx, if any.
// Repositories use this to bind to an ambient transaction when present and
// fall back to the pool otherwise, so a caller several layers up can compose
// multiple repository calls into one atomic unit without threading a pgx.Tx
// through every signature.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txContextKey{}).(pgx.Tx)
	return tx, ok
}

// WithTx stashes tx on ctx under this package's ambient-transaction key, so a
// repository calling TxFromContext binds to it.
//
// TRANSITIONAL, and the only reason it is exported. A service migrating onto
// platform/database/sql runs its transactions through sql.Manager, which
// propagates under its OWN context key — so repositories still reading
// TxFromContext would not see it and would quietly run their writes on the
// pool instead, splitting a transaction with no error raised. Bridging the two
// keys for the duration of the migration is what keeps that atomic.
//
// Call it ONLY with a transaction you actually began. Putting an arbitrary
// value here makes every repository downstream believe it is in a transaction.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txContextKey{}, tx)
}
