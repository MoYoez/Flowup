//go:build postgres

// This file is the production Postgres backend. It is kept behind a build tag so
// the default binary doesn't compile pgx in or require a running Postgres. The
// generic sqlStore is always compiled (and tested via SQLite), so only this thin
// driver/dialect shim is tag-gated.
//
// Enable with:
//
//	go build -tags postgres ./...   // pgx is already in go.mod
package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
)

// NewPostgres opens a Postgres-backed Store.
func NewPostgres(ctx context.Context, dsn string) (Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	s := newSQLStore(db, dialect{
		bind:     bindDollar,
		serialPK: "id BIGSERIAL PRIMARY KEY",
	})
	if err := s.Init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
