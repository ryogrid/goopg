package testport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// isUniqueErr reports whether err is a duplicate-key (23505) unique-violation.
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}

// TestPort_InitiallyDeferredUniqueCommit exercises a plain UNIQUE constraint
// declared DEFERRABLE INITIALLY DEFERRED — with NO SET CONSTRAINTS override —
// over the simple-query COMMIT path. The canonical win is that a transient
// duplicate is tolerated mid-transaction and only the final state is checked:
// `UPDATE u SET val = val + 1` over a contiguous key range momentarily collides
// but settles to distinct values. A genuine duplicate that survives to COMMIT
// raises 23505 at COMMIT, exactly as PG enforces deferred uniqueness. 0119-0004
// (deferred-unique).
func TestPort_InitiallyDeferredUniqueCommit(t *testing.T) {
	c := newCluster(t, "initdeferreduniq")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE u (id integer PRIMARY KEY, "+
		"val integer UNIQUE DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create u: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO u VALUES (1, 1), (2, 2), (3, 3)"); err != nil {
		t.Fatalf("seed u: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	// Transient-duplicate win: a single `UPDATE … SET val = val + 1` bumps every
	// row one step. Mid-statement (1→2) collides with the existing val=2 row, but
	// the deferred check tolerates it; the final state (2,3,4) is distinct so
	// COMMIT succeeds. Under an immediate UNIQUE this raises 23505 mid-statement.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin transient: %v", err)
	}
	if err := ex("UPDATE u SET val = val + 1"); err != nil {
		t.Fatalf("transient-duplicate UPDATE should not fail mid-statement: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("transient-duplicate commit: unexpected error %v", err)
	}
	rows := runSQL(t, c, "SELECT id, val FROM u ORDER BY id")
	if len(rows) != 3 || rows[0][1] != "2" || rows[1][1] != "3" || rows[2][1] != "4" {
		t.Fatalf("transient-duplicate final state wrong: %v", rows)
	}

	// Genuine duplicate at COMMIT: two fresh rows share val=99. Neither INSERT
	// raises (deferred); the violation surfaces at COMMIT as 23505.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin dup: %v", err)
	}
	if err := ex("INSERT INTO u VALUES (10, 99)"); err != nil {
		t.Fatalf("first deferred dup insert should not fail at INSERT: %v", err)
	}
	if err := ex("INSERT INTO u VALUES (11, 99)"); err != nil {
		t.Fatalf("second deferred dup insert should not fail at INSERT: %v", err)
	}
	if err := ex("COMMIT"); !isUniqueErr(err) {
		t.Fatalf("duplicate at COMMIT: expected 23505 at COMMIT, got %v", err)
	}
	_ = ex("ROLLBACK")

	// The failed COMMIT rolled back: both val=99 rows must be gone.
	rows = runSQL(t, c, "SELECT id FROM u WHERE val = 99")
	if len(rows) != 0 {
		t.Fatalf("duplicate rows should have rolled back, got %v", rows)
	}
}

// TestPort_SetConstraintsUniqueDeferral exercises SET CONSTRAINTS runtime
// deferral of a DEFERRABLE INITIALLY IMMEDIATE UNIQUE constraint end-to-end:
// SET CONSTRAINTS ALL DEFERRED defers the uniqueness check to COMMIT, and
// SET CONSTRAINTS ALL IMMEDIATE forces a queued check to run early. 0119-0004.
func TestPort_SetConstraintsUniqueDeferral(t *testing.T) {
	c := newCluster(t, "setconstraintsuniq")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE uu (id integer PRIMARY KEY, "+
		"val integer UNIQUE DEFERRABLE INITIALLY IMMEDIATE)"); err != nil {
		t.Fatalf("create uu: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO uu VALUES (1, 1)"); err != nil {
		t.Fatalf("seed uu: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, conn := scConn(t, c, ctx)
	defer db.Close()
	defer conn.Close()

	ex := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		return err
	}

	// Control: INITIALLY IMMEDIATE with no SET CONSTRAINTS — duplicate fails at
	// the INSERT.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin control: %v", err)
	}
	if err := ex("INSERT INTO uu VALUES (2, 1)"); !isUniqueErr(err) {
		t.Fatalf("control insert: expected immediate 23505, got %v", err)
	}
	_ = ex("ROLLBACK")

	// SET CONSTRAINTS ALL DEFERRED defers the check: the duplicate INSERT now
	// succeeds and the violation only surfaces at COMMIT.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin deferred: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set constraints all deferred: %v", err)
	}
	if err := ex("INSERT INTO uu VALUES (3, 1)"); err != nil {
		t.Fatalf("deferred duplicate insert should not fail at INSERT: %v", err)
	}
	if err := ex("COMMIT"); !isUniqueErr(err) {
		t.Fatalf("deferred duplicate: expected 23505 at COMMIT, got %v", err)
	}
	_ = ex("ROLLBACK")

	// SET CONSTRAINTS ALL IMMEDIATE forces the queued check to run at the SET.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set deferred: %v", err)
	}
	if err := ex("INSERT INTO uu VALUES (4, 1)"); err != nil {
		t.Fatalf("deferred insert: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL IMMEDIATE"); !isUniqueErr(err) {
		t.Fatalf("set immediate: expected 23505 at SET IMMEDIATE, got %v", err)
	}
	_ = ex("ROLLBACK")

	// A deferred check that is satisfied by COMMIT (no real duplicate) succeeds.
	if err := ex("BEGIN"); err != nil {
		t.Fatalf("begin clean: %v", err)
	}
	if err := ex("SET CONSTRAINTS ALL DEFERRED"); err != nil {
		t.Fatalf("set deferred clean: %v", err)
	}
	if err := ex("INSERT INTO uu VALUES (5, 5)"); err != nil {
		t.Fatalf("clean deferred insert: %v", err)
	}
	if err := ex("COMMIT"); err != nil {
		t.Fatalf("clean deferred commit: unexpected error %v", err)
	}
	rows := runSQL(t, c, "SELECT val FROM uu WHERE id = 5")
	if len(rows) != 1 || rows[0][0] != "5" {
		t.Fatalf("clean deferred row not committed: %v", rows)
	}
}
