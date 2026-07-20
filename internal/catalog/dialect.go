package catalog

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// dialect captures the differences between the supported SQL backends. SQLite is
// the default single-user backend; Postgres is selected by a postgres:// DSN.
// Every query in this package is written once with portable `?` placeholders and
// routed through the helpers below so the same code serves both backends.
type dialect struct {
	postgres bool
}

// rebind converts the `?` placeholders used throughout this package into the
// driver's expected form. SQLite binds `?` positionally; Postgres needs `$1`,
// `$2`, ... in order. Queries in this package never contain a literal `?`, so a
// straight scan is safe.
func (d dialect) rebind(query string) string {
	if !d.postgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

// The helpers below mirror the *sql.DB / *sql.Tx query API and apply rebind so
// callers stay backend-agnostic.

func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.dialect.rebind(query), args...)
}

func (s *Store) execCtx(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.dialect.rebind(query), args...)
}

func (s *Store) query(query string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.dialect.rebind(query), args...)
}

func (s *Store) queryCtx(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.dialect.rebind(query), args...)
}

func (s *Store) queryRow(query string, args ...any) *sql.Row {
	return s.db.QueryRow(s.dialect.rebind(query), args...)
}

func (s *Store) queryRowCtx(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.dialect.rebind(query), args...)
}

func (s *Store) texec(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.Exec(s.dialect.rebind(query), args...)
}

func (s *Store) texecCtx(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, s.dialect.rebind(query), args...)
}

func (s *Store) tqueryCtx(ctx context.Context, tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
	return tx.QueryContext(ctx, s.dialect.rebind(query), args...)
}

func (s *Store) tprepareCtx(ctx context.Context, tx *sql.Tx, query string) (*sql.Stmt, error) {
	return tx.PrepareContext(ctx, s.dialect.rebind(query))
}
