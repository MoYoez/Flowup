//go:build !postgres

package store

import (
	"context"
	"fmt"
)

// Open selects a store backend from a path/DSN: a "postgres://" URL needs the
// Postgres adapter (build with -tags postgres); anything else is a SQLite file
// (or ":memory:"). This default build is SQLite-only.
func Open(ctx context.Context, dsn string) (Store, error) {
	if isPostgresDSN(dsn) {
		return nil, fmt.Errorf("postgres DSN requires building with -tags postgres")
	}
	return NewSQLite(ctx, dsn)
}
