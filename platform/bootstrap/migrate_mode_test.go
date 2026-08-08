package bootstrap

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"

	"testing/fstest"

	dxmigrate "github.com/datakaveri/dx-common-go/database/postgres/migrate"
	"github.com/datakaveri/dx-common-go/platform/config"
	dxsql "github.com/datakaveri/dx-common-go/platform/database/sql"
)

// ROADMAP P0-4 / review H-02: exactly one actor applies DDL in any environment,
// chosen explicitly by deployment configuration.
//
// The defect these pin was a DEFAULT, not a bug in the runner: schema_mode
// defaulted to "migrate" in platform/config AND independently in migrate.go, so
// a service nobody configured applied DDL from every replica. Nothing in the
// fleet set the value, so the mode was decided by omission — and omission chose
// the dangerous one.

// migrationDeps is the shape of a service that applies DDL. The DSN points at a
// closed port on purpose: these tests are about which MODE is chosen, and the
// choice is made before anything is dialled.
func migrationDeps() Deps {
	fsys := fstest.MapFS{
		"migrations/0001_init.up.sql":   {Data: []byte("CREATE TABLE t (id int);")},
		"migrations/0001_init.down.sql": {Data: []byte("DROP TABLE t;")},
	}
	return Deps{
		Migrations: Migrations(fsys, "migrations", "schema_migrations_test"),
		Postgres:   Required(dxsql.Config{DSN: "postgres://127.0.0.1:1/nope"}),
	}
}

func TestSchemaModeIsRequiredWhenMigrationsAreDeclared(t *testing.T) {
	err := runMigrations(context.Background(), migrationDeps(), config.Base{}, zap.NewNop(), modeServe)
	if err == nil {
		t.Fatal("an unset schema_mode must fail the boot, not silently apply DDL from every replica")
	}
	// The message has to name the setting and both valid values, or the
	// operator hitting it has to read the source to know what to do.
	for _, want := range []string{"schema_mode", dxmigrate.ModeMigrate, dxmigrate.ModeNone} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestSchemaModeNoneAppliesNothing(t *testing.T) {
	// ModeNone must not even dial — the DSN here is a closed port, so a runner
	// that connected before checking the mode would fail instead of returning.
	base := config.Base{SchemaMode: dxmigrate.ModeNone}
	if err := runMigrations(context.Background(), migrationDeps(), base, zap.NewNop(), modeServe); err != nil {
		t.Fatalf("schema_mode=none must be a no-op that does not touch the database: %v", err)
	}
}

// TestMigrateOnlyModeIgnoresSchemaMode is the fix for the sharpest edge in P0-4.
//
// The Job and the service pods render from the SAME .Values.env map. The chart
// sets SCHEMA_MODE=none on pods, which is correct — and Kubernetes takes the
// last of a duplicated env var, so that value could reach the Job too. A Job
// that honoured it would exit 0 having applied nothing, and the rollout would
// proceed against an un-migrated schema believing the migration succeeded.
//
// migrate-only therefore does not ask: the Job's entire reason to exist is to
// apply DDL.
func TestMigrateOnlyModeIgnoresSchemaMode(t *testing.T) {
	for _, schemaMode := range []string{"", dxmigrate.ModeNone, dxmigrate.ModeMigrate} {
		base := config.Base{SchemaMode: schemaMode}
		err := runMigrations(context.Background(), migrationDeps(), base, zap.NewNop(), modeMigrateOnly)
		// It must get as far as DIALLING — that is the proof it decided to
		// migrate. The dial fails because the port is closed, which is the
		// expected outcome here and the thing a no-op would not produce.
		if err == nil {
			t.Fatalf("schema_mode=%q: migrate-only returned success without touching the database — "+
				"a Job that applies nothing and exits 0 lets the rollout proceed against an un-migrated schema", schemaMode)
		}
		if strings.Contains(err.Error(), "schema_mode is required") {
			t.Errorf("schema_mode=%q: migrate-only must not require the pod-facing setting", schemaMode)
		}
	}
}

func TestBootModeRejectsAnUnknownValue(t *testing.T) {
	t.Setenv(bootModeEnv, "migrate") // a plausible typo for "migrate-only"
	if _, err := bootMode(); err == nil {
		t.Fatal("an unrecognised boot mode must fail: a PreSync Job that silently served would hang the sync")
	}
	if err := run(Spec[testConfig]{Name: "svc", Config: config.Options{Paths: []string{t.TempDir()}}}); err == nil {
		t.Fatal("run() accepted an unrecognised boot mode")
	}
}

func TestBootModeDefaultsToServe(t *testing.T) {
	t.Setenv(bootModeEnv, "")
	got, err := bootMode()
	if err != nil {
		t.Fatalf("an unset boot mode must be valid: %v", err)
	}
	if got != modeServe {
		t.Errorf("bootMode() = %q, want %q", got, modeServe)
	}
}

// TestMigrateOnlyWithoutMigrationsIsAnError: a Job enabled against a service
// that applies no DDL would report a migration that never happened.
func TestMigrateOnlyWithoutMigrationsIsAnError(t *testing.T) {
	t.Setenv(bootModeEnv, modeMigrateOnly)
	t.Setenv("SCHEMA_MODE", dxmigrate.ModeNone)

	err := run(Spec[testConfig]{
		Name:   "svc",
		Config: config.Options{Paths: []string{t.TempDir()}},
		Deps:   func(*testConfig) Deps { return Deps{} },
	})
	if err == nil {
		t.Fatal("migrate-only against a service with no migrations must fail, not exit 0 as though it had migrated")
	}
	if !strings.Contains(err.Error(), "declares no migrations") {
		t.Errorf("error = %v, want it to name the mismatch", err)
	}
}

// TestMigrateOnlyDoesNotServe: the Job must complete. A mode that migrated and
// then bound a port would never finish, and ArgoCD would fail the sync on the
// hook's activeDeadlineSeconds rather than on anything about the schema.
func TestMigrateOnlyDoesNotServe(t *testing.T) {
	t.Setenv(bootModeEnv, modeMigrateOnly)

	wired := false
	err := run(Spec[testConfig]{
		Name:   "svc",
		Config: config.Options{Paths: []string{t.TempDir()}},
		Deps:   func(*testConfig) Deps { return migrationDeps() },
		Wire: func(context.Context, *App[testConfig]) (http.Handler, error) {
			wired = true
			return nil, nil
		},
	})
	// The dial fails (closed port) — that is fine and expected. What matters is
	// that Wire was never reached.
	_ = err
	if wired {
		t.Error("migrate-only called Wire — the Job would start serving and never complete")
	}
}
