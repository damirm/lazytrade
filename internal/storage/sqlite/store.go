package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	queries  *generated.Queries
	dbPath   string
	lockFile *agentLock
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite: database path is empty")
	}
	dsn, canonicalPath, err := makeDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	// SQLite has a single writer. One shared connection prevents read-to-write
	// transaction upgrade races between strategy commits and execution ingress.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, queries: generated.New(db), dbPath: canonicalPath}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func makeDSN(path string) (string, string, error) {
	if path == ":memory:" {
		return "file:lazytrade-memory?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", "", nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("sqlite: resolve database path: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", "", fmt.Errorf("sqlite: resolve database directory: %w", err)
	}
	canonical := filepath.Join(dir, filepath.Base(absolute))
	u := &url.URL{Scheme: "file", Path: canonical}
	return u.String() + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", canonical, nil
}

func (s *Store) Close() error {
	_ = s.Release(context.Background())
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("sqlite: close: %w", err)
	}
	return nil
}

func (s *Store) DB() *sql.DB { return s.db } // diagnostics and contract tests only
