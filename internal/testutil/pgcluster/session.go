package pgcluster

// M0131-S28 — a backend that OUTLIVES the statement that opened it.
//
// Exec/QueryScalar both shell out to `psql -c`, so their backend exits — and
// therefore ABORTS any open transaction — before the helper returns. That makes
// them structurally incapable of expressing "work that was still uncommitted
// when the postmaster died", which is the one crash-recovery property S28 could
// not assert. A Session pins ONE `database/sql` connection (sql.Conn, not
// sql.DB — a pool hands out arbitrary connections and would run the INSERT and
// the COMMIT on different backends) and holds it open until the caller closes
// it or the server dies underneath it.

import (
	"context"
	"database/sql"
	"fmt"
)

// Session is a single long-lived backend on the cluster. Statements run on
// exactly one connection, so transaction state persists between calls and a
// transaction left open stays open — including across a KillHard.
//
// Close is safe to call after the server is gone; a Session whose backend was
// killed reports errors from every subsequent call, which is the intended
// signal rather than a failure of the harness.
type Session struct {
	db   *sql.DB
	conn *sql.Conn
	name string
}

// OpenSession opens a pinned connection to the cluster. The caller must Close
// it (deferring is fine — Close tolerates a dead server).
//
// applicationName is set on the connection so the caller can find the backend
// in pg_stat_activity from a DIFFERENT session; that is how a test proves the
// backend is really sitting in `idle in transaction` rather than having quietly
// gone away.
func (c *Cluster) OpenSession(ctx context.Context, applicationName string) (*Session, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable",
		c.Host(), c.Port(), c.User(), c.Database())
	if applicationName != "" {
		dsn += " application_name=" + applicationName
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgcluster: open session %q: %w", applicationName, err)
	}
	// One connection only: a Session must never be able to hand a caller a
	// second backend, which would silently split a transaction in two.
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pgcluster: pin session %q: %w", applicationName, err)
	}
	return &Session{db: db, conn: conn, name: applicationName}, nil
}

// Exec runs a statement on the session's backend.
func (s *Session) Exec(ctx context.Context, sqlText string) error {
	if _, err := s.conn.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("pgcluster: session %q exec %q: %w", s.name, sqlText, err)
	}
	return nil
}

// QueryScalar runs a one-cell SELECT on the session's backend and returns the
// value as text. The cast to text happens server-side via the driver's own
// conversion, so callers should select an already-textual expression when the
// exact rendering matters.
func (s *Session) QueryScalar(ctx context.Context, sqlText string) (string, error) {
	var out string
	if err := s.conn.QueryRowContext(ctx, sqlText).Scan(&out); err != nil {
		return "", fmt.Errorf("pgcluster: session %q query %q: %w", s.name, sqlText, err)
	}
	return out, nil
}

// Close releases the backend. Errors are deliberately swallowed: after a
// KillHard there is no server left to talk to, and the test's subject is the
// data directory, not the teardown.
func (s *Session) Close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.db != nil {
		_ = s.db.Close()
	}
}
