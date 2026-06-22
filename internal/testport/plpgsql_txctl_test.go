package testport

import (
	"database/sql"
	"testing"

	"github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPlpgSQLDoCommitChainDurability verifies PL/pgSQL transaction control in a
// non-atomic DO block (M0118-0008, plpgsql-toast enabler): a `COMMIT;` inside a
// DO block makes the work done so far durable and chains into a fresh
// transaction, so a later error rolls back only the post-commit work.
func TestPlpgSQLDoCommitChainDurability(t *testing.T) {
	c := newCluster(t, "pl_txctl")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Insert 1, COMMIT (durable), insert 2, then raise — the post-commit insert
	// of 2 must be rolled back while the committed insert of 1 survives.
	_, err = db.Exec(`DO $$
begin
  insert into t values (1);
  commit;
  insert into t values (2);
  raise exception 'boom';
end $$;`)
	if err == nil {
		t.Fatal("expected the DO block to error with 'boom'")
	}

	var cnt, sum int
	if err := db.QueryRow("SELECT count(*), coalesce(sum(a),0) FROM t").Scan(&cnt, &sum); err != nil {
		t.Fatalf("select: %v", err)
	}
	if cnt != 1 || sum != 1 {
		t.Fatalf("after commit-chain: count=%d sum=%d, want count=1 sum=1 (row 1 committed, row 2 rolled back)", cnt, sum)
	}
}

// TestPlpgSQLDoRollbackChain verifies `ROLLBACK;` inside a DO block discards the
// pre-rollback work and continues in a fresh transaction.
func TestPlpgSQLDoRollbackChain(t *testing.T) {
	c := newCluster(t, "pl_txctl_rb")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE t (a int)"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Insert 1, ROLLBACK (discarded), insert 2, then return normally.
	if _, err := db.Exec(`DO $$
begin
  insert into t values (1);
  rollback;
  insert into t values (2);
end $$;`); err != nil {
		t.Fatalf("DO block: %v", err)
	}

	var cnt, sum int
	if err := db.QueryRow("SELECT count(*), coalesce(sum(a),0) FROM t").Scan(&cnt, &sum); err != nil {
		t.Fatalf("select: %v", err)
	}
	if cnt != 1 || sum != 2 {
		t.Fatalf("after rollback-chain: count=%d sum=%d, want count=1 sum=2 (row 1 rolled back, row 2 committed)", cnt, sum)
	}
}

// TestPlpgSQLDoCommitInExplicitBlockRejected verifies that COMMIT inside a DO
// block executed within an explicit transaction block (an atomic context) is
// rejected with SQLSTATE 2D000, matching PostgreSQL. The dispatch installs the
// commit-chain callback only in auto-commit mode.
func TestPlpgSQLDoCommitInExplicitBlockRejected(t *testing.T) {
	c := newCluster(t, "pl_txctl_atomic")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(t.Context(), "BEGIN"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	_, err = conn.ExecContext(t.Context(), `DO $$ begin commit; end $$;`)
	if err == nil {
		t.Fatal("expected COMMIT inside DO in an explicit block to be rejected")
	}
	if pe, ok := err.(*pq.Error); ok {
		if string(pe.Code) != "2D000" {
			t.Fatalf("SQLSTATE = %s, want 2D000; msg=%s", pe.Code, pe.Message)
		}
	} else {
		t.Fatalf("error = %T (%v), want *pq.Error 2D000", err, err)
	}
	_, _ = conn.ExecContext(t.Context(), "ROLLBACK")
}
