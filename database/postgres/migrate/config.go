package migrate

import "fmt"

// Mode values for Config.Mode.
const (
	// ModeNone is a no-op. Use it in environments where the schema is
	// provisioned out-of-band rather than by this service — see doc.go.
	ModeNone = "none"
	// ModeMigrate runs every pending migration up to the latest version.
	ModeMigrate = "migrate"
)

// Config configures Run and Status. Mode is a plain string (compare against
// ModeNone/ModeMigrate) rather than a distinct named type, so it binds
// directly from a service's own config field (e.g. `SchemaMode string`)
// without a conversion at the call site.
type Config struct {
	// Mode is ModeNone or ModeMigrate.
	Mode string
	// DSN is the Postgres connection string.
	DSN string
	// TableName is this service's own migrations-history table — the
	// x-migrations-table convention (schema_migrations_<service>) so
	// multiple services can share one interim database without colliding
	// version tracking. See doc.go.
	TableName string
	// SearchPath, when set, is applied as the migration connection's
	// search_path runtime parameter (e.g. "public"). Pass the SAME value the
	// application pool uses (client.Config.SearchPath) so migrations run
	// against — and the history table lands in — the same schema the app
	// reads. Migrations themselves stay schema-agnostic (no SET search_path,
	// no schema-qualified names); the active schema is decided here, by
	// config, alone. Empty leaves the server/DSN default untouched.
	SearchPath string
}

// PartialMigrationError reports that a migration run stopped partway
// through: Version failed, and everything before it already committed.
// Unlike golang-migrate's dirty-state model, goose runs each migration in
// its own transaction and only records a version as applied on success — a
// failed migration rolls back automatically and is never left "dirty" in
// the tracking table. Recovery is just fixing the migration (or the
// underlying DB issue) and restarting the service; goose retries from
// Version on the next Run. No manual `force` step exists or is needed.
type PartialMigrationError struct {
	Version int64
	Table   string
	Err     error
}

func (e *PartialMigrationError) Error() string {
	return fmt.Sprintf(
		"migrate: migration %d failed (table %q): %v — migrations before it are committed; "+
			"fix the migration and restart, no manual recovery step is needed",
		e.Version, e.Table, e.Err,
	)
}

func (e *PartialMigrationError) Unwrap() error {
	return e.Err
}
