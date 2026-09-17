package testport

// P0-E4 — regression probes for the catalog-xmax/commit-status defect
// diagnosed in tmp/METHODLOGY3_RALPH_CHECK0917/03-new-problems.md §2.2 (the
// 2026-09-16 shared-cluster `:65433` TPC-H data-loss incident) and written up
// in docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md.
//
// Root cause (two independent bugs that must land together as P0-E5):
//   - stampCatalogRowsTuple (internal/executor/operators_ddl.go:18121), called
//     from deleteCatalogRowsForOID (:18186) by every DDL path that replaces a
//     catalog row in place (e.g. finishPrimaryKeyConstraint adopting a UNIQUE
//     INDEX via `ADD CONSTRAINT ... PRIMARY KEY USING INDEX`, :12252), stamps
//     the OLD catalog row's xmax — and the XmaxCommitted hint bit — BEFORE the
//     owning transaction commits.
//   - ProcessRollbackUndos (internal/executor/operators_tx.go:388) never
//     undoes that stamp on abort (M0143-0008 — it only restores
//     CREATE/TRUNCATE/sequence undo entries).
//   - The startup catalog loader (scanCatalogHeapRows/catalogRowLive,
//     internal/initdb/catalog_heap_reload.go:43,75) then discards ANY row
//     with Xmax != InvalidTransactionID unconditionally, never checking
//     whether that xmax's transaction actually committed (CLOG). The NEW row
//     is correctly dropped (its xmin is aborted), but the OLD row is
//     incorrectly dropped too — so the table's pg_class/pg_attribute rows
//     both vanish across the next restart even though the ALTER never
//     committed.
//
// All three probes below reproduce the loss via a distinct abort path per
// P0-E4's repro instructions (fix_plan.md P0-E4): (a) explicit ROLLBACK,
// (b) the client disconnecting without ROLLBACK, (c) `goopg stop -mode
// immediate` while the transaction is still open (the incident's own shape).
// They MUST run inside a `CREATE DATABASE r` database, not the default
// `postgres` database — the default database's tables are also served from
// an in-memory JSON catalog cache (M0114) that bypasses the heap loader
// entirely and only invalidates on a DDL COMMIT, so an aborted DDL there
// would never exercise the loader and the probe would false-negative
// (internal/initdb/open.go ~1135-1147, ~1468-1489).
//
// All three are t.Skip'd pending P0-E5 so they do not fail the unit gate
// before the fix lands; P0-E5 removes the t.Skip calls as part of landing
// the fix — these ARE that task's regression tests.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// startP0E4Cluster starts a throwaway goopg server and returns two handles to
// it: c (the default "postgres" database, for administrative commands like
// CREATE DATABASE and for Stop/Start/DataDir) and r (a second handle to the
// SAME running server, targeting the freshly created "r" database — the
// two-handle-one-server pattern used by TestE2E_PGColdStartOnGoopgDataDir).
func startP0E4Cluster(t *testing.T, name string) (c *cluster.Cluster, r *cluster.Cluster) {
	t.Helper()
	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	dataDir := filepath.Join(t.TempDir(), "data")

	var err error
	c, err = cluster.New(name, cluster.Options{
		RepoRoot:     repo,
		DataDir:      dataDir,
		PSQLPath:     filepath.Join(binDir, "psql"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	t.Cleanup(func() { _ = c.Stop(cluster.ShutdownImmediate) })

	if err := runSQLSimple(t, c, "CREATE DATABASE r"); err != nil {
		t.Fatalf("CREATE DATABASE r: %v", err)
	}
	r, err = cluster.New(name+"-r", cluster.Options{
		RepoRoot:     repo,
		DataDir:      dataDir,
		ListenAddr:   c.ListenAddr(),
		Database:     "r",
		PSQLPath:     filepath.Join(binDir, "psql"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

// p0e4CreateTable lays down the fix_plan P0-E4 repro fixture: a table with a
// backing unique index that a later ALTER adopts as its primary key.
func p0e4CreateTable(t *testing.T, r *cluster.Cluster) {
	t.Helper()
	for _, stmt := range []string{
		"CREATE TABLE t(id int NOT NULL)",
		"CREATE UNIQUE INDEX t_pk ON t(id)",
	} {
		if err := runSQLSimple(t, r, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
}

// p0e4Relfilenode returns t's on-disk relfilenode so the probe can check
// whether the heap FILE (as opposed to the catalog rows describing it)
// survives.
func p0e4Relfilenode(t *testing.T, r *cluster.Cluster) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := r.Query(ctx, "SELECT relfilenode FROM pg_class WHERE relname = 't'")
	if err != nil || len(rows) != 1 {
		t.Fatalf("relfilenode of t: rows=%v err=%v", rows, err)
	}
	return rows[0][0]
}

func p0e4HeapFileExists(t *testing.T, dataDir, relfilenode string) bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, "base", "*", relfilenode))
	if err != nil {
		t.Fatalf("glob heap file for relfilenode %s: %v", relfilenode, err)
	}
	return len(matches) > 0
}

// p0e4State is the deliverable named by fix_plan P0-E4: `\d t`,
// `SELECT count(*) FROM t` and relation-file presence, captured after a
// restart.
type p0e4State struct {
	regclass   string // "" if to_regclass('t') came back NULL (table gone)
	count      string // "" if the count query errored
	countErr   string
	heapExists bool
}

func (s p0e4State) tableLost() bool {
	return s.regclass != "t" || s.countErr != ""
}

func p0e4Probe(t *testing.T, r *cluster.Cluster, dataDir, relfilenode string) p0e4State {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var st p0e4State
	if rows, err := r.Query(ctx, "SELECT coalesce(to_regclass('t')::text, '')"); err == nil && len(rows) == 1 {
		st.regclass = rows[0][0]
	}
	if rows, err := r.Query(ctx, "SELECT count(*) FROM t"); err != nil {
		st.countErr = err.Error()
	} else if len(rows) == 1 {
		st.count = rows[0][0]
	}
	st.heapExists = p0e4HeapFileExists(t, dataDir, relfilenode)
	return st
}

// p0e4WaitSettled gives a session time to reach a blocking point (e.g. the
// pg_sleep after an ALTER) and confirms the server is still answering a
// fresh, unrelated session — mirroring waitForOpenTxnInsert
// (e2e_pg_crashstart_on_goopgdata_test.go) for a goopg-only (no PG-interop)
// test.
func p0e4WaitSettled(r *cluster.Cluster) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	deadline := time.Now().Add(15 * time.Second)
	settled := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.Query(ctx, "SELECT 1"); err != nil {
			return err
		}
		if time.Now().After(settled) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("never settled before the deadline")
}

const p0e4SkipReason = "pending P0-E5 (catalog loader discards a row with " +
	"xmax!=0 regardless of the xmax transaction's commit status) — this test " +
	"IS P0-E5's regression lock; remove the Skip when landing that fix. See " +
	"docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md"

// TestPort_P0E4CatalogXmaxRollback is repro case (a): an explicit ROLLBACK of
// the ADD CONSTRAINT ... PRIMARY KEY USING INDEX transaction.
func TestPort_P0E4CatalogXmaxRollback(t *testing.T) {
	t.Skip(p0e4SkipReason)
	c, r := startP0E4Cluster(t, "p0e4-rollback")
	p0e4CreateTable(t, r)
	relfilenode := p0e4Relfilenode(t, r)

	res, err := r.PSQL("-c", "BEGIN; ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_pk; ROLLBACK;")
	if err != nil {
		t.Fatalf("launch psql BEGIN/ALTER/ROLLBACK: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql BEGIN/ALTER/ROLLBACK exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	st := p0e4Probe(t, r, c.DataDir(), relfilenode)
	if st.tableLost() {
		t.Errorf("table t lost after ROLLBACK + restart (regclass=%q count=%q countErr=%q) — "+
			"a ROLLBACKed ALTER TABLE ADD CONSTRAINT ... PRIMARY KEY USING INDEX must leave t intact",
			st.regclass, st.count, st.countErr)
	}
	if !st.heapExists {
		t.Errorf("heap file for relfilenode %s missing after ROLLBACK + restart (data file lost, not just catalog rows)", relfilenode)
	}
}

// TestPort_P0E4CatalogXmaxClientKill is repro case (b): the client
// disconnects (SIGKILL of the psql process) without ever sending ROLLBACK.
func TestPort_P0E4CatalogXmaxClientKill(t *testing.T) {
	t.Skip(p0e4SkipReason)
	c, r := startP0E4Cluster(t, "p0e4-clientkill")
	p0e4CreateTable(t, r)
	relfilenode := p0e4Relfilenode(t, r)

	openTxn, err := r.StartPSQL(nil, "BEGIN;\nALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_pk;\nSELECT pg_sleep(600);\n")
	if err != nil {
		t.Fatalf("StartPSQL open txn: %v", err)
	}
	defer func() { _ = openTxn.Stop() }()
	if err := p0e4WaitSettled(r); err != nil {
		t.Fatalf("open txn never reached its ALTER: %v", err)
	}
	if err := openTxn.Stop(); err != nil {
		t.Fatalf("kill psql client: %v", err)
	}
	if err := p0e4WaitSettled(r); err != nil {
		t.Fatalf("server did not settle after the client was killed: %v", err)
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	st := p0e4Probe(t, r, c.DataDir(), relfilenode)
	if st.tableLost() {
		t.Errorf("table t lost after client-kill (no ROLLBACK) + restart (regclass=%q count=%q countErr=%q) — "+
			"an aborted-by-disconnect ALTER TABLE ADD CONSTRAINT ... PRIMARY KEY USING INDEX must leave t intact",
			st.regclass, st.count, st.countErr)
	}
	if !st.heapExists {
		t.Errorf("heap file for relfilenode %s missing after client-kill + restart (data file lost, not just catalog rows)", relfilenode)
	}
}

// TestPort_P0E4CatalogXmaxServerImmediateStop is repro case (c): `goopg stop
// -mode immediate` while the ALTER's transaction is still open — the
// 2026-09-16 `:65433` incident's own shape.
func TestPort_P0E4CatalogXmaxServerImmediateStop(t *testing.T) {
	t.Skip(p0e4SkipReason)
	c, r := startP0E4Cluster(t, "p0e4-immediate")
	p0e4CreateTable(t, r)
	relfilenode := p0e4Relfilenode(t, r)

	openTxn, err := r.StartPSQL(nil, "BEGIN;\nALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_pk;\nSELECT pg_sleep(600);\n")
	if err != nil {
		t.Fatalf("StartPSQL open txn: %v", err)
	}
	defer func() { _ = openTxn.Stop() }()
	if err := p0e4WaitSettled(r); err != nil {
		t.Fatalf("open txn never reached its ALTER: %v", err)
	}

	if err := c.Stop(cluster.ShutdownImmediate); err != nil {
		t.Fatalf("stop -mode immediate: %v", err)
	}
	_ = openTxn.Stop() // reap the now-orphaned client; the server is already gone
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	st := p0e4Probe(t, r, c.DataDir(), relfilenode)
	if st.tableLost() {
		t.Errorf("table t lost after `stop -mode immediate` mid-ALTER + restart (regclass=%q count=%q countErr=%q) — "+
			"this is the 2026-09-16 :65433 incident's own shape and must leave t intact",
			st.regclass, st.count, st.countErr)
	}
	if !st.heapExists {
		t.Errorf("heap file for relfilenode %s missing after immediate-stop + restart (data file lost, not just catalog rows)", relfilenode)
	}
}
