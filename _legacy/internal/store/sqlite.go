package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo, no Docker, no download)
)

// NewSQLite opens a SQLite-backed Store. This is the dockerless local-dev
// backend for the MVP. WAL + a single writer connection make it crash-safe
// against process kills (kill -9): a committed transaction survives in the WAL
// and is recovered when the file is reopened by the next process.
func NewSQLite(ctx context.Context, path string) (Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // single writer: sidesteps "database is locked"

	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.ExecContext(ctx, p); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite pragma %q: %w", p, err)
		}
	}

	s := newSQLStore(db, dialect{
		bind:     bindQuestion,
		serialPK: "id INTEGER PRIMARY KEY AUTOINCREMENT",
	})
	if err := s.Init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
