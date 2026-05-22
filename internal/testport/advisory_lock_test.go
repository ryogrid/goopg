package testport

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_AdvisoryLock_AutoCommitSessionOwnership pins the connection-level
// owner identity for advisory locks outside explicit transactions. A lock
// taken in one auto-commit statement must still be owned by the same session
// when the next statement on the same connection runs pg_advisory_unlock().
func TestSyntax_AdvisoryLock_AutoCommitSessionOwnership(t *testing.T) {
	c := newCluster(t, "syntax_advisory_autocommit")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rows, err := conn.QueryContext(ctx, "SELECT pg_advisory_lock(1, 1)")
	if err != nil {
		t.Fatalf("pg_advisory_lock: %v", err)
	}
	var ignored sql.NullString
	if !rows.Next() {
		rows.Close()
		t.Fatal("pg_advisory_lock returned no rows")
	}
	if err := rows.Scan(&ignored); err != nil {
		rows.Close()
		t.Fatalf("scan pg_advisory_lock: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pg_advisory_lock rows: %v", err)
	}

	rows, err = conn.QueryContext(ctx, "SELECT pg_advisory_unlock(1, 1)")
	if err != nil {
		t.Fatalf("pg_advisory_unlock: %v", err)
	}
	var unlocked bool
	if !rows.Next() {
		rows.Close()
		t.Fatal("pg_advisory_unlock returned no rows")
	}
	if err := rows.Scan(&unlocked); err != nil {
		rows.Close()
		t.Fatalf("scan pg_advisory_unlock: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pg_advisory_unlock rows: %v", err)
	}
	if !unlocked {
		t.Fatal("pg_advisory_unlock returned false; want true on the same auto-commit connection")
	}
}

func TestSyntax_AdvisoryXactLock_AutoCommitReleasesAtStatementEnd(t *testing.T) {
	c := newCluster(t, "syntax_advisory_xact_autocommit")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()

	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()

	rows, err := conn1.QueryContext(ctx, "SELECT pg_advisory_xact_lock(1, 1)")
	if err != nil {
		t.Fatalf("pg_advisory_xact_lock: %v", err)
	}
	var ignored sql.NullString
	if !rows.Next() {
		rows.Close()
		t.Fatal("pg_advisory_xact_lock returned no rows")
	}
	if err := rows.Scan(&ignored); err != nil {
		rows.Close()
		t.Fatalf("scan pg_advisory_xact_lock: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pg_advisory_xact_lock rows: %v", err)
	}

	rows, err = conn2.QueryContext(ctx, "SELECT pg_try_advisory_lock(1, 1)")
	if err != nil {
		t.Fatalf("pg_try_advisory_lock after auto-commit xact lock: %v", err)
	}
	var acquired bool
	if !rows.Next() {
		rows.Close()
		t.Fatal("pg_try_advisory_lock returned no rows")
	}
	if err := rows.Scan(&acquired); err != nil {
		rows.Close()
		t.Fatalf("scan pg_try_advisory_lock: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pg_try_advisory_lock rows: %v", err)
	}
	if !acquired {
		t.Fatal("pg_advisory_xact_lock leaked past statement end; second session could not acquire the key")
	}

	rows, err = conn2.QueryContext(ctx, "SELECT pg_advisory_unlock(1, 1)")
	if err != nil {
		t.Fatalf("cleanup pg_advisory_unlock: %v", err)
	}
	if rows.Next() {
		var unlocked bool
		if err := rows.Scan(&unlocked); err != nil {
			rows.Close()
			t.Fatalf("scan cleanup pg_advisory_unlock: %v", err)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close cleanup pg_advisory_unlock rows: %v", err)
	}
}

func TestSyntax_AdvisoryXactLock_ExplicitTransactionReleasesOnCommit(t *testing.T) {
	c := newCluster(t, "syntax_advisory_xact_explicit")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn1, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()

	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()

	if _, err := conn1.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}

	rows, err := conn1.QueryContext(ctx, "SELECT pg_advisory_xact_lock(2, 2)")
	if err != nil {
		t.Fatalf("pg_advisory_xact_lock in explicit tx: %v", err)
	}
	var ignored sql.NullString
	if !rows.Next() {
		rows.Close()
		t.Fatal("pg_advisory_xact_lock in explicit tx returned no rows")
	}
	if err := rows.Scan(&ignored); err != nil {
		rows.Close()
		t.Fatalf("scan pg_advisory_xact_lock in explicit tx: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pg_advisory_xact_lock in explicit tx rows: %v", err)
	}

	rows, err = conn2.QueryContext(ctx, "SELECT pg_try_advisory_lock(2, 2)")
	if err != nil {
		t.Fatalf("pg_try_advisory_lock while explicit tx lock held: %v", err)
	}
	var acquired bool
	if !rows.Next() {
		rows.Close()
		t.Fatal("pg_try_advisory_lock while explicit tx lock held returned no rows")
	}
	if err := rows.Scan(&acquired); err != nil {
		rows.Close()
		t.Fatalf("scan pg_try_advisory_lock while explicit tx lock held: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pg_try_advisory_lock while explicit tx lock held rows: %v", err)
	}
	if acquired {
		t.Fatal("pg_advisory_xact_lock did not remain held until COMMIT")
	}

	if _, err := conn1.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}

	rows, err = conn2.QueryContext(ctx, "SELECT pg_try_advisory_lock(2, 2)")
	if err != nil {
		t.Fatalf("pg_try_advisory_lock after COMMIT: %v", err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("pg_try_advisory_lock after COMMIT returned no rows")
	}
	if err := rows.Scan(&acquired); err != nil {
		rows.Close()
		t.Fatalf("scan pg_try_advisory_lock after COMMIT: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close pg_try_advisory_lock after COMMIT rows: %v", err)
	}
	if !acquired {
		t.Fatal("pg_advisory_xact_lock remained held after COMMIT")
	}

	rows, err = conn2.QueryContext(ctx, "SELECT pg_advisory_unlock(2, 2)")
	if err != nil {
		t.Fatalf("cleanup pg_advisory_unlock after COMMIT: %v", err)
	}
	if rows.Next() {
		var unlocked bool
		if err := rows.Scan(&unlocked); err != nil {
			rows.Close()
			t.Fatalf("scan cleanup pg_advisory_unlock after COMMIT: %v", err)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close cleanup pg_advisory_unlock after COMMIT rows: %v", err)
	}
}