// Package sql is the platform's Postgres surface.
//
// It replaces two overlapping CRUD layers. dao.BaseDAO[T] held the mechanics;
// repository.Base[R] wrapped it with transaction propagation, and ~250 of its
// lines were one-line delegations. Worse, Base exposed .DAO() and .Pool(), so
// the abstraction was bypassable by design — an abstraction with a documented
// bypass is a suggestion, and the suggestion was taken. One type ships here.
//
// *pgxpool.Pool does not appear in this package's exported surface. The escape
// hatch is a separate package, platform/database/sql/pgx, so the question "who
// depends on pgx?" is one grep instead of an audit across 23 exported sites in
// 6 packages. The goal is LOCALITY, not concealment: LISTEN/NOTIFY, COPY
// streaming and pgvector are pgx-specific and the platform should not grow an
// opinion on each.
//
// Layer: L1 (capability).
package sql

import (
	"context"
	"time"
)

// DB is the platform database handle.
type DB interface {
	Querier
	// Begin starts a transaction. Prefer Manager.Do, which handles commit,
	// rollback and nesting; use this only where a transaction's lifetime
	// genuinely cannot be expressed as a function call.
	Begin(ctx context.Context, opts ...TxOption) (Tx, error)
	// Check satisfies observability/health.Checker.
	Check(ctx context.Context) error
	Stats() Stats
	Close()
}

// Querier is satisfied by both DB and Tx, so a query is written once and runs
// identically inside or outside a transaction. It is also structurally
// compatible with sqlc's generated DBTX interface, which is what lets
// sqlc-generated code join an ambient transaction without an adapter.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	CopyFrom(ctx context.Context, table string, columns []Column, rows [][]any) (int64, error)
}

// Tx is a transaction.
type Tx interface {
	Querier
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Rows is a result cursor. It is deliberately minimal — everything
// higher-level is a generic free function over it (Collect, One), so this
// interface never has to grow to accommodate a new scanning strategy.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
	// Values returns the current row's values, for the generic struct scanner.
	Values() ([]any, error)
	// FieldDescriptions returns the result's column names, in order.
	FieldDescriptions() []string
}

// Row is a single-row result.
type Row interface {
	Scan(dest ...any) error
}

// Stats reports pool utilisation, for metrics and for diagnosing exhaustion.
type Stats struct {
	Acquired        int32
	Idle            int32
	Total           int32
	Max             int32
	AcquireCount    int64
	AcquireDuration time.Duration
	EmptyAcquires   int64
}

// Config is the connection configuration.
type Config struct {
	DSN string `mapstructure:"dsn"`
	// SearchPath scopes every connection to a schema.
	SearchPath string `mapstructure:"search_path"`

	// MaxConns defaults to 10 — the value currently copy-pasted into every
	// service regardless of its actual concurrency profile. It is surfaced
	// here so a service can size it deliberately rather than inherit it.
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
	MaxConnIdleTime time.Duration `mapstructure:"max_conn_idle_time"`
	ConnectTimeout  time.Duration `mapstructure:"connect_timeout"`

	// SlowQueryThreshold logs any query exceeding it. Zero disables the check.
	SlowQueryThreshold time.Duration `mapstructure:"slow_query_threshold"`
}

// TxOption configures a transaction.
type TxOption func(*txConfig)

type txConfig struct {
	isolation  IsolationLevel
	readOnly   bool
	deferrable bool
}

// IsolationLevel is a transaction isolation level.
type IsolationLevel string

const (
	ReadCommitted  IsolationLevel = "read committed"
	RepeatableRead IsolationLevel = "repeatable read"
	Serializable   IsolationLevel = "serializable"
)

// Isolation sets the isolation level.
func Isolation(l IsolationLevel) TxOption { return func(c *txConfig) { c.isolation = l } }

// ReadOnly marks the transaction read-only, which lets Postgres skip some
// bookkeeping and makes an accidental write fail loudly rather than silently
// succeed in a transaction that was only meant to read.
func ReadOnly() TxOption { return func(c *txConfig) { c.readOnly = true } }

// Deferrable, with Serializable + ReadOnly, lets a long analytical transaction
// avoid serialization failures by waiting instead.
func Deferrable() TxOption { return func(c *txConfig) { c.deferrable = true } }
