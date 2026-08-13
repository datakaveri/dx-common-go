package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// This file is one of only two places in the platform allowed to import pgx
// (the other is platform/database/sql/pgx, the explicit escape hatch). The
// depguard rule in .golangci.yml enforces that.

// Open connects and verifies the connection before returning.
//
// It pings on the way out: a pool constructed against an unreachable database
// succeeds silently, so without this the failure surfaces on the first request
// instead of at boot, where it belongs.
func Open(ctx context.Context, cfg Config, opts ...Option) (DB, error) {
	o := &openOptions{logger: zap.NewNop()}
	for _, f := range opts {
		f(o)
	}

	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("sql: parse DSN: %w", err)
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.ConnectTimeout > 0 {
		pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}
	if cfg.SearchPath != "" {
		if pcfg.ConnConfig.RuntimeParams == nil {
			pcfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		pcfg.ConnConfig.RuntimeParams["search_path"] = cfg.SearchPath
	}

	// Instrumentation is installed at the DRIVER SEAM, never behind a
	// repository abstraction — that is the locked platform rule, and it is why
	// repository interceptor chains were rejected. A tracer here sees every
	// query, including raw SQL and sqlc-generated code.
	tracers := o.tracers
	if cfg.SlowQueryThreshold > 0 {
		tracers = append(tracers, &slowQueryTracer{
			threshold: cfg.SlowQueryThreshold,
			logger:    o.logger,
		})
	}
	if len(tracers) == 1 {
		pcfg.ConnConfig.Tracer = tracers[0]
	} else if len(tracers) > 1 {
		pcfg.ConnConfig.Tracer = multiTracer(tracers)
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("sql: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("sql: ping: %w", err)
	}
	return &db{pool: pool}, nil
}

type openOptions struct {
	tracers []pgx.QueryTracer
	logger  *zap.Logger
}

// Option configures Open.
type Option func(*openOptions)

// WithLogger sets the logger used by the slow-query tracer.
func WithLogger(l *zap.Logger) Option { return func(o *openOptions) { o.logger = l } }

// WithTracer installs a driver query tracer. Reach it through
// platform/database/sql/pgx.Tracer, so that installing one is an explicit,
// greppable act rather than something a service does in passing.
func WithTracer(t pgx.QueryTracer) Option {
	return func(o *openOptions) { o.tracers = append(o.tracers, t) }
}

// db implements DB over pgxpool.
type db struct{ pool *pgxpool.Pool }

func (d *db) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	r, err := d.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, MapError(err)
	}
	return &rows{Rows: r}, nil
}

func (d *db) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return row{d.pool.QueryRow(ctx, sql, args...)}
}

func (d *db) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := d.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, MapError(err)
	}
	return tag.RowsAffected(), nil
}

func (d *db) CopyFrom(ctx context.Context, table string, columns []Column, values [][]any) (int64, error) {
	return copyFrom(ctx, d.pool, table, columns, values)
}

func (d *db) Begin(ctx context.Context, opts ...TxOption) (Tx, error) {
	c := &txConfig{}
	for _, o := range opts {
		o(c)
	}
	t, err := d.pool.BeginTx(ctx, c.pgx())
	if err != nil {
		return nil, MapError(err)
	}
	return &tx{Tx: t}, nil
}

func (d *db) Check(ctx context.Context) error { return d.pool.Ping(ctx) }

// Pool exposes the driver pool to platform/database/sql/pgx. It is deliberately
// NOT part of the DB interface: reaching it requires importing that package by
// name, which is what makes the dependency a grep.
func (d *db) Pool() *pgxpool.Pool { return d.pool }
func (d *db) Close()              { d.pool.Close() }

func (d *db) Stats() Stats {
	s := d.pool.Stat()
	return Stats{
		Acquired: s.AcquiredConns(), Idle: s.IdleConns(),
		Total: s.TotalConns(), Max: s.MaxConns(),
		AcquireCount: s.AcquireCount(), AcquireDuration: s.AcquireDuration(),
		EmptyAcquires: s.EmptyAcquireCount(),
	}
}

func (c *txConfig) pgx() pgx.TxOptions {
	o := pgx.TxOptions{}
	switch c.isolation {
	case ReadCommitted:
		o.IsoLevel = pgx.ReadCommitted
	case RepeatableRead:
		o.IsoLevel = pgx.RepeatableRead
	case Serializable:
		o.IsoLevel = pgx.Serializable
	}
	if c.readOnly {
		o.AccessMode = pgx.ReadOnly
	}
	if c.deferrable {
		o.DeferrableMode = pgx.Deferrable
	}
	return o
}

// tx implements Tx.
//
// It deliberately exposes NO accessor for the embedded driver transaction. One
// existed (PgxTx), for sql/pgx.Tx to assert through while dx-acl-go's outbox
// still took a concrete pgx.Tx; both are gone now that platform/events takes a
// Querier. The driver transaction stays reachable only from inside this
// package, which is what keeps "who can bypass transaction propagation?"
// answerable by reading one file.
type tx struct{ pgx.Tx }

func (t *tx) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	r, err := t.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, MapError(err)
	}
	return &rows{Rows: r}, nil
}

func (t *tx) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return row{t.Tx.QueryRow(ctx, sql, args...)}
}

func (t *tx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.Tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, MapError(err)
	}
	return tag.RowsAffected(), nil
}

func (t *tx) CopyFrom(ctx context.Context, table string, columns []Column, values [][]any) (int64, error) {
	return copyFrom(ctx, t.Tx, table, columns, values)
}

func (t *tx) Commit(ctx context.Context) error   { return MapError(t.Tx.Commit(ctx)) }
func (t *tx) Rollback(ctx context.Context) error { return MapError(t.Tx.Rollback(ctx)) }

type copier interface {
	CopyFrom(ctx context.Context, ident pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error)
}

func copyFrom(ctx context.Context, c copier, table string, columns []Column, values [][]any) (int64, error) {
	cols := make([]string, len(columns))
	for i, col := range columns {
		cols[i] = string(col)
	}
	n, err := c.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(values))
	return n, MapError(err)
}

// rows adapts pgx.Rows.
type rows struct{ pgx.Rows }

func (r *rows) Err() error { return MapError(r.Rows.Err()) }
func (r *rows) Close()     { r.Rows.Close() }

func (r *rows) FieldDescriptions() []string {
	fds := r.Rows.FieldDescriptions()
	out := make([]string, len(fds))
	for i, fd := range fds {
		out[i] = fd.Name
	}
	return out
}

// native exposes the underlying pgx.Rows to the generic scanner in this
// package. It is UNEXPORTED, so Collect and One can reuse pgx's battle-tested
// reflection scanner rather than reimplementing it, while pgx stays out of
// every exported signature.
func (r *rows) native() pgx.Rows { return r.Rows }

type row struct{ pgx.Row }

func (r row) Scan(dest ...any) error { return MapError(r.Row.Scan(dest...)) }

// slowQueryTracer logs queries exceeding a threshold.
type slowQueryTracer struct {
	threshold time.Duration
	logger    *zap.Logger
}

type traceKey struct{}

func (t *slowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, time.Now())
}

func (t *slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(traceKey{}).(time.Time)
	if !ok {
		return
	}
	if d := time.Since(start); d >= t.threshold {
		t.logger.Warn("slow query",
			zap.Duration("took", d),
			zap.String("sql", truncate(data.CommandTag.String(), 200)),
			zap.Error(data.Err))
	}
}

type multiTracer []pgx.QueryTracer

func (m multiTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	for _, t := range m {
		ctx = t.TraceQueryStart(ctx, c, d)
	}
	return ctx
}

func (m multiTracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	for _, t := range m {
		t.TraceQueryEnd(ctx, c, d)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// pgErrorCode extracts a Postgres SQLSTATE, if any.
func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if asPgError(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func asPgError(err error, target **pgconn.PgError) bool {
	for err != nil {
		if e, ok := err.(*pgconn.PgError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// isNoRows reports a "no rows" result by identity, via the error chain — not by
// matching the message text. A wrapped error whose message merely CONTAINS
// "no rows in result set" (a scan of user data, a different failure that quotes
// it) must not be misclassified as NotFound; only pgx.ErrNoRows itself, however
// wrapped with %w, is one (ROADMAP P2-2). Every caller here passes the pgx error
// straight from Scan/Exec, so the chain reaches the sentinel intact.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// acquireLock takes a session-level advisory lock on a dedicated connection and
// holds it until release is called.
//
// The connection is checked out of the pool for the lock's whole lifetime.
// Anything less is incorrect: pg_try_advisory_lock is SESSION-scoped, so a lock
// taken through the pool is released the moment the query returns the
// connection, and — because advisory locks are re-entrant within a session —
// the next caller handed that same connection acquires it again.
//
// It is unexported and reached through an interface assertion in Manager.Lock,
// so the pgx-specific mechanics stay in this file.
func (d *db) acquireLock(ctx context.Context, id int64) (func(), bool, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}

	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}

	release := func() {
		// Unlock on a fresh context: ctx may already be cancelled, and an
		// advisory lock that is never released survives until the backend exits.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", id)
		conn.Release()
	}
	return release, true, nil
}
