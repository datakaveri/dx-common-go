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
func runMigrations(_ context.Context, deps Deps, base config.Base, log *zap.Logger) error {
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
	if mode == "" {
		mode = "migrate"
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
