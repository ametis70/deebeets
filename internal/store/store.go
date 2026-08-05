// Package store persists deebeets item state in SQLite. SQLite is the source
// of truth: rows are never deleted because a file disappeared from disk.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Item states.
const (
	StateWaiting     = "waiting"
	StateQueued      = "queued"
	StateDownloading = "downloading"
	StateDownloaded  = "downloaded"
	StateFinished    = "finished"
	StateFailed      = "failed"
	StateBlocklisted = "blocklisted"
	StateSkipped     = "skipped"
)

// Failure stages.
const (
	StageDownload = "download"
)

// Orchestrator stages persisted to meta.
const (
	StageIdle        = "idle"
	StageSyncing     = "syncing"
	StageDownloading = "downloading"
	StageImporting   = "importing"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and runs migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer avoids "database is locked" churn under the worker pools.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for read-only callers (e.g. the CLI status).
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for i, name := range names {
		target := i + 1
		if version >= target {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		// PRAGMA can't be parameterized; target is an int we control.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", target)); err != nil {
			tx.Rollback()
			return fmt.Errorf("bump user_version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		version = target
	}
	return nil
}

// GetMeta returns a meta value, or "" if absent.
func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetMeta upserts a meta value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
