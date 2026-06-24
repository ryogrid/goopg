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

// scanBool runs a single-column boolean-returning query on conn and returns the
// value. Helper for the advisory-identity regression tests below.
func scanBool(ctx context.Context, t *testing.T, conn *sql.Conn, query string) bool {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("%s: no rows", query)
	}
	var b sql.NullBool
	if err := rows.Scan(&b); err != nil {
		t.Fatalf("scan %s: %v", query, err)
	}
	return b.Valid && b.Bool
}

// TestSyntax_AdvisoryLock_SessionUnlockAcrossBeginBoundary is the regression
// test for the M0118-0003 advisory-lock owner-identity fix. A session-scoped
// pg_advisory_lock() taken in auto-commit (before any BEGIN) must still be owned
// by the SAME connection when pg_advisory_unlock() runs AFTER a BEGIN — i.e. the
// owner identity must not flip from the per-connection SessionRegistry (used in
// auto-commit) to the BasicSession (created at the first BEGIN). Before the fix
// the unlock reported "you don't own a lock" / returned false, the lock leaked
// for the server-process lifetime, and the next acquirer of that key blocked
// forever (the lock-update-delete isolation-spec hang). This mirrors the spec's
// step order: setup pg_advisory_lock(0); BEGIN; … ; pg_advisory_unlock(0).
func TestSyntax_AdvisoryLock_SessionUnlockAcrossBeginBoundary(t *testing.T) {
	c := newCluster(t, "syntax_advisory_unlock_across_begin")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	// Take the session-scoped lock in auto-commit, then enter an explicit
	// transaction and unlock it from inside the transaction.
	_ = scanBool(ctx, t, conn, "SELECT pg_advisory_lock(0)")
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if !scanBool(ctx, t, conn, "SELECT pg_advisory_unlock(0)") {
		t.Fatal("pg_advisory_unlock(0) returned false after BEGIN; owner identity flipped (lock leaked)")
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}

	// A second connection must now be able to acquire the same key without
	// blocking — proof the lock was actually released, not leaked.
	conn2, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	if !scanBool(ctx, t, conn2, "SELECT pg_try_advisory_lock(0)") {
		t.Fatal("second connection could not acquire key 0; the unlock-after-BEGIN leaked the lock")
	}
	_ = scanBool(ctx, t, conn2, "SELECT pg_advisory_unlock(0)")
}

// TestSyntax_AdvisoryLock_ReleasedOnDisconnect verifies that session-scoped
// advisory locks are freed when a backend exits, matching PostgreSQL. A client
// that takes pg_advisory_lock() and abandons the connection without unlocking
// must not strand the key: the next acquirer would otherwise block for the
// server-process lifetime. Before the M0118-0003 fix only xact-scoped advisory
// locks were released on teardown.
func TestSyntax_AdvisoryLock_ReleasedOnDisconnect(t *testing.T) {
	c := newCluster(t, "syntax_advisory_release_on_disconnect")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dsn := buildDSN(t, c)

	// Dedicated DB so Close() actually tears down the backend (returns the only
	// connection's underlying socket), triggering server-side teardown.
	holderDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	holderDB.SetMaxIdleConns(0)
	holder, err := holderDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// pg_advisory_lock() returns void (unlike pg_try_advisory_lock's boolean),
	// so just execute it — it blocks until the session-scoped lock is held.
	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_lock(7)"); err != nil {
		t.Fatalf("holder pg_advisory_lock(7): %v", err)
	}
	// Abandon the connection WITHOUT unlocking.
	_ = holder.Close()
	_ = holderDB.Close()

	// A fresh connection must be able to claim the key once the holder's backend
	// has exited. The teardown release is asynchronous (fires when the backend
	// observes EOF), so poll within the deadline.
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	deadline := time.Now().Add(15 * time.Second)
	for {
		if scanBool(ctx, t, conn, "SELECT pg_try_advisory_lock(7)") {
			_ = scanBool(ctx, t, conn, "SELECT pg_advisory_unlock(7)")
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("key 7 still held after holder disconnected; session-scoped advisory lock leaked on teardown")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
