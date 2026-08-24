package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// canonicalSqlcYAML mirrors the layout adopted fleet-wide (see dx-acl-go's
// sqlc.yaml): pgx/v5, no JSON tags/interface (services map generated rows to
// domain types themselves), uuid/timestamptz overrides for pgx-native types.
const canonicalSqlcYAML = `version: "2"
sql:
  - engine: "postgresql"
    schema: "db/sqlc/schema.sql"
    queries: "db/sqlc/queries"
    gen:
      go:
        package: "sqlcgen"
        out: "internal/repository/postgres/sqlcgen"
        sql_package: "pgx/v5"
        emit_json_tags: false
        emit_interface: false
        overrides:
          - db_type: "uuid"
            go_type:
              import: "github.com/google/uuid"
              type: "UUID"
          - db_type: "uuid"
            nullable: true
            go_type:
              import: "github.com/google/uuid"
              type: "UUID"
              pointer: true
          - db_type: "timestamptz"
            go_type:
              import: "time"
              type: "Time"
          - db_type: "pg_catalog.timestamptz"
            go_type:
              import: "time"
              type: "Time"
`

const schemaPlaceholder = `-- Reference schema snapshot ONLY — never applied. Paste the service's
-- current table definitions here so sqlc can validate queries/*.sql
-- against real columns/types. Keep this in sync by hand when the schema
-- (Flyway-owned or migration-owned) changes.
`

// cmdSqlcInit implements `dx sqlc init`: writes the canonical sqlc.yaml and
// db/sqlc/{schema.sql,queries/} layout used across the fleet. Does not
// overwrite existing files — safe to re-run.
func cmdSqlcInit(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: dx sqlc init")
	}

	if err := writeIfAbsent("sqlc.yaml", canonicalSqlcYAML); err != nil {
		return err
	}

	if err := os.MkdirAll("db/sqlc/queries", 0o755); err != nil {
		return fmt.Errorf("create db/sqlc/queries: %w", err)
	}
	if err := writeIfAbsent(filepath.Join("db", "sqlc", "schema.sql"), schemaPlaceholder); err != nil {
		return err
	}
	gitkeep := filepath.Join("db", "sqlc", "queries", ".gitkeep")
	if _, err := os.Stat(gitkeep); os.IsNotExist(err) {
		if err := os.WriteFile(gitkeep, nil, 0o644); err != nil { //nolint:gosec // G306: a committed .gitkeep placeholder; 0644 is correct
			return fmt.Errorf("create %s: %w", gitkeep, err)
		}
	}

	fmt.Println("created sqlc.yaml, db/sqlc/schema.sql, db/sqlc/queries/")
	return nil
}
