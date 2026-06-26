package testport

// Same-backend two-phase-commit end-to-end coverage (M0118-0009,
// prepared-transactions enabler). Proves that PREPARE TRANSACTION keeps the
// transaction's work pending and that a later COMMIT PREPARED / ROLLBACK
// PREPARED on the same connection finalises it through the canonical
// commit/rollback path. The full SERIALIZABLE prepared-transactions.spec
// (1500 permutations, SSI conflict across prepared xacts) is verified
// separately; this test pins the basic feature semantics + error paths.
//
// Design doc: docs/design/0118-0110-same-backend-two-phase-commit.md.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func twoPhaseConn(t *testing.T, c *cluster.Cluster) (*sql.DB, *sql.Conn) {
	t.Helper()
	host, port, ok := strings.Cut(c.ListenAddr(), ":")
	if !ok {
		t.Fatalf("bad listen addr %q", c.ListenAddr())
	}
	dsn := fmt.Sprintf("host=%s port=%s user=postgres dbname=postgres sslmode=disable", host, port)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("conn: %v", err)
	}
	return db, conn
}

func TestPort_TwoPhaseCommitSameBackend(t *testing.T) {
	c, err := cluster.New("two-phase-commit", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE t2pc(a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	ctx := context.Background()
	exec := func(conn *sql.Conn, q string) error {
		_, e := conn.ExecContext(ctx, q)
		return e
	}

	// COMMIT PREPARED makes the prepared work durable/visible.
	t.Run("commit prepared", func(t *testing.T) {
		db, conn := twoPhaseConn(t, c)
		defer db.Close()
		defer conn.Close()
		for _, q := range []string{
			"BEGIN",
			"INSERT INTO t2pc VALUES (1)",
			"PREPARE TRANSACTION 'g1'",
		} {
			if err := exec(conn, q); err != nil {
				t.Fatalf("%q: %v", q, err)
			}
		}
		// Another session must NOT yet see the prepared (uncommitted) row.
		if got := queryScalar(t, c, "SELECT count(*) FROM t2pc WHERE a = 1"); got != "0" {
			t.Fatalf("prepared row visible before COMMIT PREPARED: count=%q want 0", got)
		}
		if err := exec(conn, "COMMIT PREPARED 'g1'"); err != nil {
			t.Fatalf("COMMIT PREPARED: %v", err)
		}
		if got := queryScalar(t, c, "SELECT count(*) FROM t2pc WHERE a = 1"); got != "1" {
			t.Fatalf("row not visible after COMMIT PREPARED: count=%q want 1", got)
		}
	})

	// ROLLBACK PREPARED discards the prepared work.
	t.Run("rollback prepared", func(t *testing.T) {
		db, conn := twoPhaseConn(t, c)
		defer db.Close()
		defer conn.Close()
		for _, q := range []string{
			"BEGIN",
			"INSERT INTO t2pc VALUES (2)",
			"PREPARE TRANSACTION 'g2'",
			"ROLLBACK PREPARED 'g2'",
		} {
			if err := exec(conn, q); err != nil {
				t.Fatalf("%q: %v", q, err)
			}
		}
		if got := queryScalar(t, c, "SELECT count(*) FROM t2pc WHERE a = 2"); got != "0" {
			t.Fatalf("rolled-back prepared row visible: count=%q want 0", got)
		}
	})

	// PREPARE TRANSACTION outside a transaction block is an error (25P01).
	t.Run("prepare outside txn block", func(t *testing.T) {
		db, conn := twoPhaseConn(t, c)
		defer db.Close()
		defer conn.Close()
		if err := exec(conn, "PREPARE TRANSACTION 'g3'"); err == nil {
			t.Fatalf("PREPARE TRANSACTION outside txn block should error")
		} else if !strings.Contains(err.Error(), "transaction blocks") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// COMMIT PREPARED of an unknown gid is an error (42704).
	t.Run("commit prepared unknown gid", func(t *testing.T) {
		db, conn := twoPhaseConn(t, c)
		defer db.Close()
		defer conn.Close()
		if err := exec(conn, "COMMIT PREPARED 'nope'"); err == nil {
			t.Fatalf("COMMIT PREPARED of unknown gid should error")
		} else if !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
