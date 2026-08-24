package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const migrationsDir = "db/migrations"

var migrationPrefix = regexp.MustCompile(`^(\d{4})_`)

var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// cmdNewMigration implements `dx new migration <name>`: scans migrationsDir
// for the highest NNNN_ prefix already in use and writes the next
// NNNN_<slug>.up.sql / .down.sql pair, mirroring the existing
// 0001_baseline.up/.down.sql convention.
func cmdNewMigration(args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: dx new migration <name>")
	}
	name := args[0]
	slug := slugify(name)
	if slug == "" {
		return fmt.Errorf("migration name %q has no usable characters", name)
	}

	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", migrationsDir, err)
	}

	next, err := nextMigrationNumber(migrationsDir)
	if err != nil {
		return err
	}

	base := fmt.Sprintf("%04d_%s", next, slug)
	up := filepath.Join(migrationsDir, base+".up.sql")
	down := filepath.Join(migrationsDir, base+".down.sql")

	if err := writeIfAbsent(up, fmt.Sprintf("-- %s: describe the schema change here.\n", base)); err != nil {
		return err
	}
	if err := writeIfAbsent(down, fmt.Sprintf("-- %s: reverse of the .up.sql migration.\n", base)); err != nil {
		return err
	}

	fmt.Printf("created %s\ncreated %s\n", up, down)
	return nil
}

// nextMigrationNumber scans dir for existing NNNN_* files and returns the
// next free 4-digit sequence number (1 if the directory is empty/absent).
func nextMigrationNumber(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}

	max := 0
	for _, e := range entries {
		m := migrationPrefix.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max + 1, nil
}

// slugify lowercases name and collapses runs of non-alphanumeric characters
// into single underscores, matching the existing "0001_baseline" style.
func slugify(name string) string {
	s := slugPattern.ReplaceAllString(strings.ToLower(name), "_")
	return strings.Trim(s, "_")
}

func writeIfAbsent(path, contents string) error {
	if _, err := os.Stat(path); err == nil { //nolint:gosec // G703: path is built from a slugified name ([^a-z0-9]+ -> _), so no traversal survives
		return fmt.Errorf("%s already exists", path)
	}
	return os.WriteFile(path, []byte(contents), 0o644) //nolint:gosec // G306: generated migration SOURCE, committed to the repo; 0644 is correct
}
