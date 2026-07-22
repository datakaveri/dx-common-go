package migrate

import (
	"embed"
	"errors"
	"strings"
	"testing"
)

//go:embed testdata
var testFS embed.FS

func TestRun_ModeNoneIsNoOp(t *testing.T) {
	// No DSN dial should ever happen in ModeNone — an obviously-invalid DSN
	// proves Run returned before touching the network.
	cfg := Config{Mode: ModeNone, DSN: "postgres://invalid:invalid@127.0.0.1:1/nope", TableName: "schema_migrations_test"}
	if err := Run(cfg, testFS, "testdata", nil); err != nil {
		t.Fatalf("Run with ModeNone must be a no-op, got: %v", err)
	}
}

func TestPartialMigrationError_Message(t *testing.T) {
	err := &PartialMigrationError{Version: 3, Table: "schema_migrations_acl", Err: errors.New("boom")}
	msg := err.Error()
	for _, want := range []string{"schema_migrations_acl", "migration 3", "boom", "no manual recovery"} {
		if !strings.Contains(msg, want) {
			t.Errorf("PartialMigrationError message missing %q: %s", want, msg)
		}
	}
}

func TestPartialMigrationError_Unwrap(t *testing.T) {
	inner := errors.New("boom")
	err := &PartialMigrationError{Version: 3, Table: "schema_migrations_acl", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatalf("expected errors.Is to see through Unwrap to %v", inner)
	}
}
