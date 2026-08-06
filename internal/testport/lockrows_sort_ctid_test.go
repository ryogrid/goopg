package testport

// lockrows_sort_ctid_test.go — `ORDER BY ... FOR UPDATE OF <rel>` over a JOIN
// must still take the row lock.
//
// goopg does not carry the row mark as a resjunk `ctid` column the way PG does
// (preprocess_targetlist adds one per rowmark; nodeLockRows.c reads it back).
// It reconstructs the TID at runtime instead, by two routes:
//
//  1. lockRowsOp.scan — a currentTIDProvider found by walking the operator tree
//     (findScanLeafForRel). Both walkers deliberately STOP at a sortOp and
//     return nil: once a sort has drained and reordered its input the scan
//     cursor no longer points at the row being emitted, so reading
//     currentTID() there would stamp an arbitrary tuple.
//  2. the slot side-channel — sortOp.ctids carries (block, off, has) in
//     lockstep with its rows and re-attaches it in Next, precisely so route 1
//     may fail for `ORDER BY ... FOR UPDATE`; drainAndStamp's ms.hasCTID
//     fallback consumes it.
//
// Route 2 only fires if the slot ENTERING the sort already has hasCTID. A
// seqScanOp stamps it itself, so a single-relation `ORDER BY ... FOR UPDATE`
// always worked. A joinOp does not: it only forwards the heap ctid when
// preserveCTIDRel is set, and markJoinPreserveCTID — which sets it — recursed
// only through Project/Filter/Join and so stopped dead at the Sort. Net effect
// for `LockRows -> Sort -> Hash Join`: no scan (route 1 nil by design) and no
// slot TID (route 2 never populated), so lockRowsOp took its unlocked
// pass-through path and FOR UPDATE became a silent no-op — it neither blocked
// on a concurrently-updated row nor ran the EvalPlanQual recheck, returning the
// stale pre-update row instead of PG's post-update one.
//
// A/B verified against two binaries differing only in the `case *sortOp` arm of
// markJoinPreserveCTID, same plan shape both sides
// (LockRows -> Sort -> Hash Join): pre-fix returned the stale row in 4 ms
// without blocking; fixed blocked 4008 ms and returned the updated row.
//
// NOTE the bound of the fix: sortOp drops the side-channel once it spills
// (ctidsDisabled — the N-way merge cannot carry it), so a row-locking query
// whose sort exceeds work_mem still loses the lock silently. That is recorded
// in .ralph/deferral_ledger.md; the real fix is the resjunk-ctid column.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	_ "github.com/lib/pq"
)

// Transactions here are driven as plain statements on a pinned *sql.Conn, the
// way framework.IsolationRunner drives its sessions — NOT via db.Begin().
// lib/pq's Begin asserts the ReadyForQuery transaction status flips to 'T',
// which goopg does not report, so db.Begin() fails "unexpected transaction
// status idle" before any of this test's semantics are reached.

// TestPort_LockRowsSortOverJoinTakesRowLock pins that a FOR UPDATE whose plan
// puts a Sort above a join still blocks on a concurrently-updated row and then
// sees the updated value. Pre-fix the ORDER BY variant returned immediately
// with the stale row.
func TestPort_LockRowsSortOverJoinTakesRowLock(t *testing.T) {
	c := newCluster(t, "lockrows_sort_ctid")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	dsn := buildDSN(t, c)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(4)

	for _, stmt := range []string{
		`CREATE TABLE lrs_acct (accountid text PRIMARY KEY, balance int NOT NULL)`,
		`CREATE TABLE lrs_side (k text)`,
		`INSERT INTO lrs_acct VALUES ('checking', 600), ('savings', 600)`,
		`INSERT INTO lrs_side VALUES ('checking'), ('savings')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}

	cases := []struct {
		name string
		sql  string
	}{
		// The regression: ORDER BY puts a Sort between LockRows and the join.
		{"sort_over_join", `SELECT a.accountid, a.balance FROM lrs_acct a, lrs_side s
			WHERE a.accountid = s.k ORDER BY a.accountid FOR UPDATE OF a`},
		// Control: same join, no Sort. This route (lockRowsOp.scan) always
		// worked; it is here so a future change cannot "fix" one and break the
		// other unnoticed.
		{"join_no_sort", `SELECT a.accountid, a.balance FROM lrs_acct a, lrs_side s
			WHERE a.accountid = s.k AND a.accountid = 'checking' FOR UPDATE OF a`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE lrs_acct SET balance = 600`); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			// Writer: hold an uncommitted UPDATE of the 'checking' row on its
			// own pinned connection.
			writer, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Close() }()
			if _, err := writer.ExecContext(ctx, `BEGIN`); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.ExecContext(ctx,
				`UPDATE lrs_acct SET balance = balance + 450 WHERE accountid = 'checking'`); err != nil {
				_, _ = writer.ExecContext(ctx, `ROLLBACK`)
				t.Fatal(err)
			}

			// Locker: must BLOCK until the writer commits.
			type result struct {
				balance int
				err     error
			}
			locker, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = locker.Close() }()

			done := make(chan result, 1)
			go func() {
				var bal int
				var id string
				// The locked row is 'checking'; ORDER BY puts it first.
				err := locker.QueryRowContext(ctx, tc.sql).Scan(&id, &bal)
				done <- result{balance: bal, err: err}
			}()

			// Pre-fix this returned in ~4 ms. Give it far more than that, but
			// stay well under any lock timeout.
			select {
			case r := <-done:
				_, _ = writer.ExecContext(ctx, `ROLLBACK`)
				t.Fatalf("FOR UPDATE did not block on the concurrently-updated row: "+
					"returned balance=%d err=%v (row lock was silently skipped)", r.balance, r.err)
			case <-time.After(2 * time.Second):
				// Correct: still waiting on the writer's xmax.
			}

			if _, err := writer.ExecContext(ctx, `COMMIT`); err != nil {
				t.Fatal(err)
			}

			select {
			case r := <-done:
				if r.err != nil {
					t.Fatalf("locker failed after writer commit: %v", r.err)
				}
				// EvalPlanQual must re-read the committed version: 600+450.
				if r.balance != 1050 {
					t.Errorf("locker saw balance=%d, want 1050 "+
						"(stale pre-update row: EvalPlanQual recheck did not run)", r.balance)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("locker never woke after the writer committed")
			}
		})
	}
}
