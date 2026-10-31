package testport

// Server-level pin for the M-NIGHTLY task "COPY skips the pgstat row
// counters" (filed 2026-10-31 by the relpages recon loop). The filed probe
// ran a 5000-row \copy and read the pgstat row counters: PG 18.3 reports
// pg_stat_get_tuples_inserted = 5000 (n_tup_ins) and pg_stat_get_live_tuples
// = 5000 (n_live_tup); goopg reported 0 — the COPY executor inserted rows
// without ever calling recordRelInsert, and the hand-built COPY context
// dropped the session identity dispatch wires on every other statement, so
// even counters recorded elsewhere sat in a bucket the session's
// pg_stat_force_next_flush() could not reach.
//
// The counters are per-session until flushed, so COPY and the observing
// getters must share ONE connection: lib/pq's CopyIn runs the real
// `COPY t (a) FROM STDIN` CopyIn protocol (the same
// NewCopyFromExecutor/PushLine/Finish streaming the wire layer arms), then
// pg_stat_force_next_flush() + the getters run on that same conn.
//
// The pg_stat_all_tables VIEW's n_tup_ins/n_live_tup cells are the
// M0122-0003 honest-0/reltuples-estimate renderers for EVERY DML class
// (documented design, see docs/design/0122-0003-pg-stat-user-tables.md), not
// a COPY-specific gap; the ledger carries that deferral.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestCopyFromFeedsPgstatCountersServer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	c, err := cluster.New("copy-pgstat-counters", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      dir,
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE cps_srv(a int)"); err != nil {
		t.Fatalf("CREATE TABLE cps_srv: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := sql.Open("postgres", c.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// One physical connection for the whole test: the server's pgstat
	// counters live in the session that ran the COPY until
	// pg_stat_force_next_flush() merges them into the shared store, so the
	// observing getters must land on the SAME server session. lib/pq's
	// CopyIn also requires an open transaction (pq's own rule).
	db.SetMaxOpenConns(1)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	const rows = 5000
	// lib/pq's CopyIn protocol lives on the driver.Stmt path (conn.Prepare
	// → prepareCopyIn): the stmt streams each Exec as a CopyData frame and
	// Close() terminates the stream, over the transaction's single
	// connection.
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn("cps_srv", "a"))
	if err != nil {
		t.Fatalf("Prepare COPY %d rows FROM STDIN: %v", rows, err)
	}
	for i := 1; i <= rows; i++ {
		if _, err := stmt.ExecContext(ctx, i); err != nil {
			_ = stmt.Close()
			t.Fatalf("CopyIn row %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("CopyIn close: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after CopyIn: %v", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Same session: flush this backend's pending counters into the shared
	// store (pg_stat_report_stat at statement end in PG), then read the
	// counters the pg_stat_all_tables cells are built from upstream
	// (system_views.sql: n_tup_ins = pg_stat_get_tuples_inserted(relid)).
	if _, err := conn.ExecContext(ctx, "SELECT pg_stat_force_next_flush()"); err != nil {
		t.Fatalf("pg_stat_force_next_flush: %v", err)
	}
	for _, tc := range []struct{ sql, want string }{
		// The filed probe's pair: insert count + live-tuple estimate.
		{"SELECT pg_stat_get_tuples_inserted('cps_srv'::regclass)::text", "5000"},
		{"SELECT pg_stat_get_live_tuples('cps_srv'::regclass)::text", "5000"},
		// The autovacuum-trigger feeds (needsVacuum ins arm / needsAnalyze
		// mod arm): a COPY-loaded table must cross the thresholds, as PG's
		// does (pgbench -i tables analyze immediately on the next tick).
		{"SELECT pg_stat_get_ins_since_vacuum('cps_srv'::regclass)::text", "5000"},
		{"SELECT pg_stat_get_mod_since_analyze('cps_srv'::regclass)::text", "5000"},
	} {
		var got string
		if err := conn.QueryRowContext(ctx, tc.sql).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		if got != tc.want {
			t.Errorf("%s = %s, want %s", tc.sql, got, tc.want)
		}
	}

	// The data is really loaded (the counters ride the inserts).
	var cnt string
	if err := conn.QueryRowContext(ctx, "SELECT count(*)::text FROM cps_srv").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != "5000" {
		t.Fatalf("SELECT count(*) = %s, want 5000", cnt)
	}
}
