package postgres

import "time"

// Config holds connection pool settings for PostgreSQL via pgx.
type Config struct {
	// DSN is the full connection string, e.g.
	// postgres://user:pass@localhost:5433/dbname?sslmode=disable
	DSN             string        `mapstructure:"dsn"`
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
	ConnectTimeout  time.Duration `mapstructure:"connect_timeout"`
}
