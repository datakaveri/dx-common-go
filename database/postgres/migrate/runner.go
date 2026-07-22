package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
)

// Run applies every pending migration under dir (an embedded directory of
// NNNN_title.sql files, see doc.go) to cfg.DSN, tracked in cfg.TableName.
// cfg.Mode == ModeNone is a no-op, so callers can invoke Run unconditionally
// from cmd/server/main.go and gate real execution purely through config. A
// nil logger is fine (logging is skipped).
func Run(cfg Config, fsys fs.FS, dir string, logger *zap.Logger) error {
	if cfg.Mode != ModeMigrate {
		return nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	provider, err := open(cfg, fsys, dir)
	if err != nil {
		return err
	}
	defer provider.Close() //nolint:errcheck

	results, err := provider.Up(context.Background())
	if err != nil {
		var partial *goose.PartialError
		if errors.As(err, &partial) {
			return &PartialMigrationError{
				Version: partial.Failed.Source.Version,
				Table:   cfg.TableName,
				Err:     partial.Err,
			}
		}
		return fmt.Errorf("migrate: up: %w", err)
	}

	if len(results) == 0 {
		logger.Info("no pending migrations", zap.String("table", cfg.TableName))
		return nil
	}

	version, verr := provider.GetDBVersion(context.Background())
	if verr == nil {
		logger.Info("migrations applied",
			zap.String("table", cfg.TableName),
			zap.Int64("version", version),
			zap.Int("count", len(results)),
		)
	}
	return nil
}
