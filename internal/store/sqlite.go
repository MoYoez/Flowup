package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func OpenSQLite(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite pragma %q: %w", statement, err)
		}
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			workflow_name TEXT NOT NULL,
			workflow_version INTEGER NOT NULL,
			workflow_yaml BLOB NOT NULL,
			inputs_json BLOB NOT NULL,
			output_json BLOB,
			current_step INTEGER NOT NULL,
			status TEXT NOT NULL,
			error_code TEXT NOT NULL,
			error_message TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS steps (
			run_id TEXT NOT NULL,
			step_id TEXT NOT NULL,
			step_index INTEGER NOT NULL,
			action_name TEXT NOT NULL,
			input_json BLOB,
			output_json BLOB,
			status TEXT NOT NULL,
			attempt INTEGER NOT NULL,
			error_code TEXT NOT NULL,
			error_message TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL,
			PRIMARY KEY(run_id, step_id)
		)`,
		`CREATE INDEX IF NOT EXISTS steps_run_index ON steps(run_id, step_index)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			step_id TEXT NOT NULL,
			type TEXT NOT NULL,
			data_json BLOB,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS events_run_id ON events(run_id, id)`,
		`CREATE TABLE IF NOT EXISTS approvals (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			step_id TEXT NOT NULL,
			message TEXT NOT NULL,
			preview_json BLOB,
			status TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			decided_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS effects (
			key TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			step_id TEXT NOT NULL,
			action_name TEXT NOT NULL,
			status TEXT NOT NULL,
			output_json BLOB,
			created_at INTEGER NOT NULL,
			completed_at INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate sqlite: %w", err)
		}
	}
	return nil
}

func encodeTime(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixNano()
}

func decodeTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}
