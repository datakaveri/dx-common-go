package migrate_test

import (
	"context"
	"embed"
	"errors"
	"strings"
	"testing"

	dxmigrate "github.com/datakaveri/dx-common-go/database/postgres/migrate"
	"github.com/datakaveri/dx-common-go/dxtest/containers"
)

// A separate external test package (migrate_test, not migrate) is required
// here — dxtest/containers imports database/postgres/migrate itself, so an
// internal test file (package migrate) importing dxtest/containers would be
// a real import cycle.
//
//go:embed testdata
var testFS embed.FS

//go:embed testdata_partial
var testPartialFS embed.FS

func tableName(t *testing.T) string {
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return strings.ToLower("schema_migrations_" + name)
}

func TestRun_AppliesAndIsIdempotent(t *testing.T) {
	h := containers.Postgres(t)
	table := tableName(t)
	cfg := dxmigrate.Config{Mode: dxmigrate.ModeMigrate, DSN: h.DSN, TableName: table}
	t.Cleanup(func() { h.Pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })

	if err := dxmigrate.Run(cfg, testFS, "testdata", nil); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Second call must see zero pending migrations and still return nil —
	// proving Run's idempotent-rerun handling works against a real
	// Postgres, not just in the ModeNone unit test.
	if err := dxmigrate.Run(cfg, testFS, "testdata", nil); err != nil {
		t.Fatalf("second (idempotent) Run: %v", err)
	}
}

// TestRun_SearchPathDirectsSchema proves the active schema is entirely
// config-driven: with Config.SearchPath set, the migration connection (and so
// the history table it creates) lands in that schema — no SET search_path in
// any migration file. Changing schema is a config change alone.
func TestRun_SearchPathDirectsSchema(t *testing.T) {
	h := containers.Postgres(t)
	ctx := context.Background()
	const schema = "cfg_sp"
	if _, err := h.Pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	table := tableName(t)
	t.Cleanup(func() { h.Pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })

	cfg := dxmigrate.Config{Mode: dxmigrate.ModeMigrate, DSN: h.DSN, TableName: table, SearchPath: schema}
	if err := dxmigrate.Run(cfg, testFS, "testdata", nil); err != nil {
		t.Fatalf("Run with SearchPath: %v", err)
	}

	// The history table must exist in the configured schema, not public.
	var got string
	if err := h.Pool.QueryRow(ctx,
		"SELECT schemaname FROM pg_tables WHERE tablename = $1", table).Scan(&got); err != nil {
		t.Fatalf("locate history table: %v", err)
	}
	if got != schema {
		t.Fatalf("history table in schema %q, want %q — SearchPath did not drive the schema", got, schema)
	}
}

func TestStatus_ReportsVersionAndDirty(t *testing.T) {
	h := containers.Postgres(t)
	table := tableName(t)
	cfg := dxmigrate.Config{Mode: dxmigrate.ModeMigrate, DSN: h.DSN, TableName: table}
	t.Cleanup(func() { h.Pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })

	if err := dxmigrate.Run(cfg, testFS, "testdata", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	version, dirty, err := dxmigrate.Status(cfg, testFS, "testdata")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if version != 1 || dirty {
		t.Fatalf("expected version=1 dirty=false, got version=%d dirty=%v", version, dirty)
	}
}

func TestStatus_NoMigrationsRunYet_ReturnsZero(t *testing.T) {
	h := containers.Postgres(t)
	table := tableName(t)
	cfg := dxmigrate.Config{Mode: dxmigrate.ModeMigrate, DSN: h.DSN, TableName: table}
	t.Cleanup(func() { h.Pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })

	version, dirty, err := dxmigrate.Status(cfg, testFS, "testdata")
	if err != nil {
		t.Fatalf("Status before any Run: %v", err)
	}
	if version != 0 || dirty {
		t.Fatalf("expected version=0 dirty=false before any migration ran, got version=%d dirty=%v", version, dirty)
	}
}

// TestRun_PartialFailure_ReturnsPartialMigrationErrorThenRecoversOnRerun
// proves goose's actual recovery story: a migration that fails partway
// rolls back its own transaction and is never recorded, so — unlike
// golang-migrate's dirty-state model — no manual `force` step is needed.
// Fixing the migration and rerunning Run (same Config, same table) just
// works.
func TestRun_PartialFailure_ReturnsPartialMigrationErrorThenRecoversOnRerun(t *testing.T) {
	h := containers.Postgres(t)
	table := tableName(t)
	cfg := dxmigrate.Config{Mode: dxmigrate.ModeMigrate, DSN: h.DSN, TableName: table}
	ctx := context.Background()
	t.Cleanup(func() {
		h.Pool.Exec(ctx, "DROP TABLE IF EXISTS "+table)
		h.Pool.Exec(ctx, "DROP TABLE IF EXISTS migrate_partial_test_1")
		h.Pool.Exec(ctx, "DROP TABLE IF EXISTS migrate_partial_test_2")
	})

	// 0002's Up section fails on its 2nd statement; 0001 commits, 0002 rolls
	// back entirely and is never recorded as applied.
	err := dxmigrate.Run(cfg, testPartialFS, "testdata_partial/broken", nil)
	var partialErr *dxmigrate.PartialMigrationError
	if !errors.As(err, &partialErr) {
		t.Fatalf("expected a *PartialMigrationError, got %v", err)
	}
	if partialErr.Version != 2 {
		t.Fatalf("expected PartialMigrationError.Version = 2, got %d", partialErr.Version)
	}
	if partialErr.Table != table {
		t.Fatalf("expected PartialMigrationError.Table = %q, got %q", table, partialErr.Table)
	}

	version, _, verr := dxmigrate.Status(cfg, testPartialFS, "testdata_partial/broken")
	if verr != nil {
		t.Fatalf("Status after partial failure: %v", verr)
	}
	if version != 1 {
		t.Fatalf("expected version=1 committed before the failure, got version=%d", version)
	}

	// Fix migration 0002 and rerun — no force/unlock step, just Run again.
	if err := dxmigrate.Run(cfg, testPartialFS, "testdata_partial/fixed", nil); err != nil {
		t.Fatalf("Run after fixing the migration: %v", err)
	}
	version, _, verr = dxmigrate.Status(cfg, testPartialFS, "testdata_partial/fixed")
	if verr != nil {
		t.Fatalf("Status after recovery: %v", verr)
	}
	if version != 2 {
		t.Fatalf("expected version=2 after recovery, got version=%d", version)
	}
}
