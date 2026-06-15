//go:build postgres

package store

import "context"

// Open selects Postgres for a "postgres://" DSN, else SQLite. Compiled only with
// -tags postgres (which also pulls in the pgx adapter).
func Open(ctx context.Context, dsn string) (Store, error) {
	if isPostgresDSN(dsn) {
		return NewPostgres(ctx, dsn)
	}
	return NewSQLite(ctx, dsn)
}
