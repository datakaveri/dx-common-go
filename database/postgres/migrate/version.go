package migrate

import (
	"context"
	"fmt"
	"io/fs"
)

// Status reports the current schema version without applying anything, for
// a boot-time "refuse to start if the DB is ahead of this binary" check.
// version=0 means no migration has run yet. dirty is always false: unlike
// golang-migrate, goose has no dirty-state concept — see
// PartialMigrationError's doc comment — and is kept only for signature
// compatibility with existing callers.
func Status(cfg Config, fsys fs.FS, dir string) (version uint, dirty bool, err error) {
	provider, err := open(cfg, fsys, dir)
	if err != nil {
		return 0, false, err
	}
	defer provider.Close() //nolint:errcheck

	v, err := provider.GetDBVersion(context.Background())
	if err != nil {
		return 0, false, fmt.Errorf("migrate: version: %w", err)
	}
	return uint(v), false, nil
}
