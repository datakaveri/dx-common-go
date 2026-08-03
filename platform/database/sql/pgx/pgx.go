// Package pgx is the deliberate, greppable escape hatch to the raw driver.
//
// Importing this package is an explicit act of stepping outside the platform:
// LISTEN/NOTIFY, COPY-protocol streaming, custom type registration, pgvector.
// The point is not to hide pgx — the platform has no useful opinion about those
// features — it is to make the dependency LOCATABLE.
//
// Before: pgx appeared in 23 exported signatures across 6 packages, so
// answering "what depends on the driver?" meant auditing all of them. After:
//
//	grep -rl "platform/database/sql/pgx"
//
// is the complete answer. That is the whole design, and it is why the depguard
// rule in .golangci.yml bans pgx everywhere else under platform/.
package pgx

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
)

// Pool returns the underlying pgx pool, or nil if db is not pgx-backed (a fake
// in a test, say).
//
// Anything reached through it bypasses the platform's error mapping and
// transaction propagation — a query run on the pool will NOT join an ambient
// transaction. Use dxsql.Conn(ctx, db) when you need that.
func Pool(db dxsql.DB) *pgxpool.Pool {
	p, ok := db.(interface{ Pool() *pgxpool.Pool })
	if !ok {
		return nil
	}
	return p.Pool()
}

// Tracer installs an additional query tracer at open time.
//
// Instrumentation belongs at the driver seam — that is the locked platform rule,
// and it is why repository interceptor chains were rejected. A tracer here sees
// every statement, including raw SQL and sqlc-generated code, which a
// repository-level hook never would.
func Tracer(t pgx.QueryTracer) dxsql.Option { return dxsql.WithTracer(t) }

// Tx returns the pgx transaction the platform manager put on ctx, if any.
//
// TRANSITIONAL. It exists for code that must hand a concrete driver
// transaction to an API predating platform/database/sql — specifically
// messaging/outbox.PGStore.Insert, whose signature takes a pgx.Tx, so a
// transactional-outbox write cannot otherwise join a Manager.Do transaction.
// Without it, migrating a repository to the platform while its outbox insert
// stays on the legacy store silently splits the two writes apart: the outbox
// row commits independently of the row it is supposed to be atomic with, and
// nothing fails.
//
// It goes away with platform/events, whose outbox takes a sql.Querier and
// therefore joins the ambient transaction natively. Do NOT reach for this to
// run ordinary queries — use dxsql.Conn(ctx, db).
//
// The assertion goes through PgxTx rather than straight to pgx.Tx because
// sql.Tx and pgx.Tx both declare CopyFrom with different signatures, so no type
// can satisfy both. A non-pgx transaction (a fake in a test) reports false.
func Tx(ctx context.Context) (pgx.Tx, bool) {
	t, ok := dxsql.TxFrom(ctx)
	if !ok {
		return nil, false
	}
	u, ok := t.(interface{ PgxTx() pgx.Tx })
	if !ok {
		return nil, false
	}
	return u.PgxTx(), true
}
