package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	migrationfs "github.com/damirm/lazytrade/db/sqlite/migrations"
)

const currentSchemaVersion = 6

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		dirty INTEGER NOT NULL CHECK (dirty IN (0,1)),
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("sqlite: create migration ledger: %w", err)
	}
	var version int
	var dirty int
	err := s.db.QueryRowContext(ctx,
		`SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1`,
	).Scan(&version, &dirty)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("sqlite: read migration version: %w", err)
	}
	if dirty != 0 {
		return fmt.Errorf("sqlite: schema migration %d is dirty", version)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("sqlite: database schema version %d is newer than supported %d", version, currentSchemaVersion)
	}

	entries, err := fs.Glob(migrationfs.Files, "*.up.sql")
	if err != nil {
		return fmt.Errorf("sqlite: enumerate migrations: %w", err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		migrationVersion, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("sqlite: invalid migration filename %q", name)
		}
		if migrationVersion <= version {
			continue
		}
		body, err := migrationfs.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("sqlite: read migration %q: %w", name, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin migration %d: %w", migrationVersion, err)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, dirty, applied_at) VALUES (?,1,unixepoch('subsec')*1000000)`,
			migrationVersion); err == nil {
			_, err = tx.ExecContext(ctx, string(body))
		}
		if err == nil {
			_, err = tx.ExecContext(ctx,
				`UPDATE schema_migrations SET dirty=0 WHERE version=?`, migrationVersion)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite: apply migration %d: %w", migrationVersion, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("sqlite: commit migration %d: %w", migrationVersion, err)
		}
		version = migrationVersion
	}
	return nil
}
