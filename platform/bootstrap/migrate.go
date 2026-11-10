package bootstrap

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	dxmigrate "github.com/datakaveri/dx-common-go/database/postgres/migrate"
	"github.com/datakaveri/dx-common-go/platform/config"
)

// runMigrations applies the service's schema before any pool is opened.
//
// Two properties matter here and neither is negotiable:
//
//   - It runs BEFORE dxsql.Open. Migrations take DDL locks, and handing a
//     repository a pool whose schema is not yet current is how a service serves
//     traffic against a half-migrated table.
//   - It is ALWAYS required, even when the database itself is declared
//     optional. A failed migration is a broken deploy, never a degraded mode:
//     continuing would run new code against an old schema, which is strictly
//     worse than not starting.
//
// The runner opens and closes its own connection, so nothing is left holding a
// migration-scoped session once the schema is current.
func runMigrations(_ context.Context, deps Deps, base config.Base, log *zap.Logger, bootMode string) error {
	m := deps.Migrations
	if m == nil || deps.Postgres == nil {
		// Migrations without a database is a wiring mistake worth naming rather
		// than silently skipping.
		if m != nil {
			return fmt.Errorf("migrations declared without a Postgres dependency")
		}
		return nil
	}

	mode := base.SchemaMode

	// The PreSync Job's whole reason to exist is to apply DDL, so it does not
	// need to be told twice. This also removes the failure that motivated
	// ROADMAP P0-4's third defect: the Job and the pods read the same env map,
	// and a SCHEMA_MODE=none aimed at pods used to be able to turn the Job into
	// a silent no-op that exited 0 and let the rollout proceed against an
	// un-migrated schema.
	if bootMode == modeMigrateOnly {
		mode = dxmigrate.ModeMigrate
	}

	// No default. schema_mode used to default to "migrate" here AND in
	// platform/config, so a service that was never configured applied DDL from
	// every replica and raced the others for the advisory lock (ROADMAP P0-4 /
	// review H-02). AD-012 says the mode is configuration; defaulting it meant
	// the mode was decided by omission, and omission chose the dangerous value.
	//
	// Only services that actually declare migrations are affected — this line
	// is unreachable for the other eight.
	if mode == "" {
		return fmt.Errorf(
			"schema_mode is required: this service applies migrations, so it must be told whether to. "+
				"Set schema_mode=%q on the actor that owns the schema (locally, the service itself; "+
				"in Kubernetes, the PreSync migration Job) and schema_mode=%q everywhere else",
			dxmigrate.ModeMigrate, dxmigrate.ModeNone)
	}

	cfg := dxmigrate.Config{
		Mode:       mode,
		DSN:        deps.Postgres.Config.DSN,
		TableName:  m.Table,
		SearchPath: deps.Postgres.Config.SearchPath,
	}
	if err := dxmigrate.Run(cfg, m.FS, m.Dir, log); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
