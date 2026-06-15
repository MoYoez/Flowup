package store

import "strings"

// isPostgresDSN reports whether a store path looks like a Postgres URL (vs a
// SQLite file path / :memory:).
func isPostgresDSN(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}
