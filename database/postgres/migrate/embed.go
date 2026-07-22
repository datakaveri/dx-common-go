package migrate

import (
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// open wires an embedded migration directory (fsys/dir, rooted via fs.Sub)
// to cfg.DSN through the pgx database/sql driver, ready for Run/Status to
// call Up/GetDBVersion on.
func open(cfg Config, fsys fs.FS, dir string) (*goose.Provider, error) {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: open source %q: %w", dir, err)
	}

	// Apply the config-driven search_path to the migration connection so
	// schema-agnostic migrations run against the same schema the application
	// pool uses (client.Config.SearchPath). The history table lands there too.
	connCfg, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("migrate: parse DSN: %w", err)
	}
	if cfg.SearchPath != "" {
		if connCfg.RuntimeParams == nil {
			connCfg.RuntimeParams = map[string]string{}
		}
		connCfg.RuntimeParams["search_path"] = cfg.SearchPath
	}
	db := stdlib.OpenDB(*connCfg)

	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithTableName(cfg.TableName))
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: new provider: %w", err)
	}

	// provider.Close closes the underlying db itself, so there is nothing
	// left for the caller to close separately.
	return provider, nil
}
