package postmaster

// M0143-0001 reload-loop half. The DDL-execution half (database_ddl_chain_test.go)
// closed the "manual psql evidence only" gap for CREATE DATABASE chained with
// executor DDL/DML within a single live session. This file closes the other
// half named at filing: a genuine multi-database WAL-less-restart (close the
// runtime, reopen the same data dir) that drives internal/initdb's
// ListDatabases reload loop (catalog_heap_reload.go: reloadDatabasesFromHeap,
// loadUserTablesFromHeap, loadForeignKeysFromHeap, ...) and re-checks
// pgConstraintTableRel's per-database FK branch (sys_pg_constraint.go)
// AFTER reload, not just within the creating session.
//
// Deliberately in-process with NO subprocess and NO real TCP socket:
// postmaster.Server is used purely as a method-holder for
// tryHandleDatabaseDDL/wireExtensionRows (never Run()) against a real
// initdb.Open runtime — mirroring internal/initdb/heap_catalog_load_test.go's
// restart shape (Init -> Open -> DDL -> Close -> Open) plus
// database_ddl_chain_test.go's DDL-chain shape. This sidesteps the
// pre-existing in-process *postmaster.Server-with-Run() harness hang on
// multi-DB-write shutdown that database_template_oid_collision_test.go's
// package comment names (its Stop/Start round-trip runs the real accept
// loop + connWG.Wait() shutdown drain; a full Server.Run is never invoked
// here, so that drain path never enters the picture).
//
// One finding worth recording: syncPgDatabaseHeapRow — the write that makes
// CREATE DATABASE's pg_database row (global/1262) survive a restart — is
// itself gated on `s.cfg.TxnMgr != nil` (runPgDatabaseHeapTxn,
// database_ddl.go:1365-1368) and silently no-ops otherwise. The already-landed
// DDL-execution-half test never set Config.TxnMgr (it built its own separate
// transam.Manager instead, since that test never restarts), so it never
// exercised this write. Omitting TxnMgr here would make CREATE DATABASE
// invisible after restart with no error at creation time — confirmed live by
// temporarily building the Config without TxnMgr, which reproduced exactly
// that (ListDatabases() empty post-restart, no error at either CREATE
// DATABASE or the restart) before adding it back.
import (
	"slices"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// conNames extracts and sorts the conname column from a pg_constraint result
// set, for order-independent comparison — the executor gives no ordering
// guarantee over a plain SELECT with no ORDER BY.
func conNames(rows []executor.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = string(r[0].Buf)
	}
	slices.Sort(out)
	return out
}

// runChainDDLDurable runs a single DDL statement to completion under its own
// transaction and commits it, against dbName's real catalog-allocated oid
// (via s.wireExtensionRows) — unlike runChainDDL (database_ddl_chain_test.go),
// which leaves its transaction open for that test's same-session,
// never-restarts use case. Mirrors internal/initdb/ddl_catalog_sync_test.go's
// runDDL commit discipline, needed here because the whole point of this test
// is that the DDL survives a Close+Open round trip.
func runChainDDLDurable(t *testing.T, rt *initdb.Runtime, s *Server, dbName, sql string) {
	t.Helper()
	tx, err := rt.TxnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := rt.TxnMgr.SnapshotFor(tx)
	if err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := executor.NewContext()
	ctx.Pool = rt.Pool
	ctx.Catalog = rt.Catalog
	ctx.TxnMgr = rt.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap
	s.wireExtensionRows(ctx, dbName)
	ctx.CommandCounterIncrement()
	ctx.CmdID = ctx.GetCurrentCommandId(true)

	stmts, err := parser.Parse(sql)
	if err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], rt.Catalog)
	if err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := executor.Build(plan)
	if err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("db %s: Open(%q): %v", dbName, sql, err)
	}
	if _, nextErr := op.Next(); nextErr != executor.EOF && nextErr != nil {
		_ = op.Close()
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("db %s: Next(%q): %v", dbName, sql, nextErr)
	}
	if err := op.Close(); err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("db %s: Close(%q): %v", dbName, sql, err)
	}
	if err := rt.TxnMgr.Commit(tx); err != nil {
		t.Fatalf("db %s: commit(%q): %v", dbName, sql, err)
	}
}

// queryUnderDBReload mirrors runChainQuery (database_ddl_chain_test.go) but
// opens its own short-lived transaction against rt/s — it runs standalone
// post-restart, with no already-open session context to reuse.
func queryUnderDBReload(t *testing.T, rt *initdb.Runtime, s *Server, dbName, sql string) ([]executor.Row, error) {
	t.Helper()
	tx, err := rt.TxnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = rt.TxnMgr.Rollback(tx) }()
	snap, err := rt.TxnMgr.SnapshotFor(tx)
	if err != nil {
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := executor.NewContext()
	ctx.Pool = rt.Pool
	ctx.Catalog = rt.Catalog
	ctx.TxnMgr = rt.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap
	s.wireExtensionRows(ctx, dbName)
	return runChainQuery(t, ctx, sql)
}

// queryUnderDBReloadDurable is queryUnderDBReload's committing twin (mirrors
// runChainDDLDurable vs runChainDDL's naming), for a DML statement whose
// effect a LATER queryUnderDBReload call in the same test must actually
// observe — queryUnderDBReload always rolls back, so two independent calls
// never see each other's writes. M0143-0003c's UNIQUE-enforcement check needs
// exactly this: one committed INSERT for a second, non-committing INSERT to
// collide with.
func queryUnderDBReloadDurable(t *testing.T, rt *initdb.Runtime, s *Server, dbName, sql string) {
	t.Helper()
	tx, err := rt.TxnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := rt.TxnMgr.SnapshotFor(tx)
	if err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := executor.NewContext()
	ctx.Pool = rt.Pool
	ctx.Catalog = rt.Catalog
	ctx.TxnMgr = rt.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap
	s.wireExtensionRows(ctx, dbName)
	if _, err := runChainQuery(t, ctx, sql); err != nil {
		_ = rt.TxnMgr.Rollback(tx)
		t.Fatalf("db %s: %q: %v", dbName, sql, err)
	}
	if err := rt.TxnMgr.Commit(tx); err != nil {
		t.Fatalf("db %s: commit(%q): %v", dbName, sql, err)
	}
}

// TestDatabaseDDLReloadAcrossRestart drives two non-default databases (r1,
// r2), each with its own FK-bearing table pair, through a genuine
// Close+Open restart and re-verifies, post-reload:
//   - reloadDatabasesFromHeap repopulated ListDatabases() with both r1/r2
//     (the ListDatabases loop's own precondition — every downstream per-DB
//     pass in catalog_heap_reload.go iterates this list);
//   - loadForeignKeysFromHeap's per-database pg_constraint scan
//     (loadForeignKeysFromHeapForDB, keyed by pgConstraintTableRel's
//     tableCatalogHeapDBOid routing) reconstructs each database's OWN FK
//     with the right identity, not just the right count — a swapped-DB
//     bug can preserve "1 row" while returning the wrong conname (the same
//     failure shape the DDL-execution half's live-session test caught via
//     a temporary PGConstraintRowsForDBOid mutation);
//   - namespace isolation survives reload: neither database's table is
//     visible under the other's real oid.
func TestDatabaseDDLReloadAcrossRestart(t *testing.T) {
	dir := t.TempDir() + "/data"
	if err := initdb.Init(initdb.Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	rt1, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("initdb.Open: %v", err)
	}
	s1 := New(Config{Catalog: rt1.Catalog, Pool: rt1.Pool, TxnMgr: rt1.TxnMgr})

	for _, name := range []string{"CREATE DATABASE r1", "CREATE DATABASE r2"} {
		handled, _, err := s1.tryHandleDatabaseDDL(name, "postgres", "", nil)
		if !handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v", name, handled, err)
		}
	}

	runChainDDLDurable(t, rt1, s1, "r1", "CREATE TABLE parent (id int4 PRIMARY KEY)")
	runChainDDLDurable(t, rt1, s1, "r1", "CREATE TABLE child (id int4, pid int4 REFERENCES parent(id))")
	runChainDDLDurable(t, rt1, s1, "r2", "CREATE TABLE other (id int4 PRIMARY KEY)")
	runChainDDLDurable(t, rt1, s1, "r2", "CREATE TABLE otherchild (id int4, pid int4 REFERENCES other(id))")
	// M0143-0003b: a table-level named CHECK constraint. Before this fix
	// catalog.Table.CheckConstraints/NamedChecks was never written to any
	// heap and never reloaded, so this constraint's ENFORCEMENT (not just its
	// pg_constraint row) silently vanished after every restart.
	// M0143-0003d: a named NOT NULL constraint. Column.NotNull (actual
	// attnotnull ENFORCEMENT) already reloaded correctly before this fix — the
	// gap was catalog.Table.NotNullConstraints (the pg_constraint contype='n'
	// METADATA: conname, conislocal, coninhcount), which was never written to
	// any heap and never reloaded.
	// M0143-0003c: a named UNIQUE (non-PRIMARY-KEY) constraint-backed index.
	// The index's own indisunique survived reload before this fix (like any
	// bare CREATE UNIQUE INDEX would), but catalog.Index.IsConstraint — the
	// ONLY thing distinguishing "ADD CONSTRAINT ... UNIQUE" from a bare
	// unique index — was never persisted or reloaded, so the constraint
	// silently reverted to looking like a bare index after every restart.
	// M0143-0003e: a named btree-equality EXCLUDE constraint. Unlike UNIQUE,
	// catalog.Index.IsExclusion had NO reload path at all before this fix —
	// so restoring it wrong would not just mislabel the index (like the
	// UNIQUE gap did), it would leave checkExclusionConstraintsForInsert with
	// nothing to gate on, silently disabling enforcement entirely.
	runChainDDLDurable(t, rt1, s1, "r1",
		"CREATE TABLE gauge (id int4 PRIMARY KEY, level int4 CONSTRAINT gauge_level_check CHECK (level >= 0), code text CONSTRAINT gauge_code_not_null NOT NULL, tag text CONSTRAINT gauge_tag_unique UNIQUE, zone int4, CONSTRAINT gauge_zone_excl EXCLUDE USING btree (zone WITH =))")

	if err := rt1.SaveCatalog(); err != nil {
		t.Fatalf("SaveCatalog: %v", err)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("rt1.Close: %v", err)
	}

	rt2, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("initdb.Open (restart): %v", err)
	}
	defer rt2.Close()
	s2 := New(Config{Catalog: rt2.Catalog, Pool: rt2.Pool, TxnMgr: rt2.TxnMgr})

	im2, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("rt2.Catalog is %T, want *catalog.InMemory", rt2.Catalog)
	}
	var haveR1, haveR2 bool
	for _, name := range im2.ListDatabases() {
		switch name {
		case "r1":
			haveR1 = true
		case "r2":
			haveR2 = true
		}
	}
	if !haveR1 || !haveR2 {
		t.Fatalf("post-restart ListDatabases() missing r1/r2 (have r1=%v r2=%v) — reloadDatabasesFromHeap did not repopulate the ListDatabases loop's own precondition", haveR1, haveR2)
	}

	rowsR1, err := queryUnderDBReload(t, rt2, s2, "r1", "SELECT conname FROM pg_constraint WHERE contype = 'f'")
	if err != nil {
		t.Fatalf("db r1 post-restart: SELECT pg_constraint: %v", err)
	}
	if len(rowsR1) != 1 || string(rowsR1[0][0].Buf) != "child_pid_fkey" {
		t.Errorf("db r1 post-restart: pg_constraint FK rows = %v, want exactly [child_pid_fkey]", rowsR1)
	}

	rowsR2, err := queryUnderDBReload(t, rt2, s2, "r2", "SELECT conname FROM pg_constraint WHERE contype = 'f'")
	if err != nil {
		t.Fatalf("db r2 post-restart: SELECT pg_constraint: %v", err)
	}
	if len(rowsR2) != 1 || string(rowsR2[0][0].Buf) != "otherchild_pid_fkey" {
		t.Errorf("db r2 post-restart: pg_constraint FK rows = %v, want exactly [otherchild_pid_fkey]", rowsR2)
	}

	// M0143-0003a: "parent"/"other"/"gauge" above all declare `id int4
	// PRIMARY KEY` — their backing unique index survives reload
	// (RegisterIndexDuringRecoveryForDB restores Primary/Unique from
	// pg_index), but PGConstraintRowsForDBOid's synthesised pg_constraint row
	// additionally requires idx.IsConstraint, which recovery never set before
	// this fix — every PK vanished from pg_constraint (contype='p')
	// post-restart though the index itself, and its enforcement, kept
	// working.
	rowsR1PK, err := queryUnderDBReload(t, rt2, s2, "r1", "SELECT conname FROM pg_constraint WHERE contype = 'p'")
	if err != nil {
		t.Fatalf("db r1 post-restart: SELECT pg_constraint contype=p: %v", err)
	}
	if got, want := conNames(rowsR1PK), []string{"gauge_pkey", "parent_pkey"}; !slices.Equal(got, want) {
		t.Errorf("db r1 post-restart: pg_constraint PK rows = %v, want exactly %v", got, want)
	}

	rowsR2PK, err := queryUnderDBReload(t, rt2, s2, "r2", "SELECT conname FROM pg_constraint WHERE contype = 'p'")
	if err != nil {
		t.Fatalf("db r2 post-restart: SELECT pg_constraint contype=p: %v", err)
	}
	if len(rowsR2PK) != 1 || string(rowsR2PK[0][0].Buf) != "other_pkey" {
		t.Errorf("db r2 post-restart: pg_constraint PK rows = %v, want exactly [other_pkey]", rowsR2PK)
	}

	// M0143-0003b: the CHECK row itself, then actual enforcement — a
	// catalog-only assertion could pass while tbl.CheckConstraints (the field
	// copy.go/operators_fk.go/operators_storage.go actually gate on) stayed
	// empty, if the reload only rebuilt NamedChecks.
	rowsR1Check, err := queryUnderDBReload(t, rt2, s2, "r1", "SELECT conname FROM pg_constraint WHERE contype = 'c'")
	if err != nil {
		t.Fatalf("db r1 post-restart: SELECT pg_constraint contype=c: %v", err)
	}
	if len(rowsR1Check) != 1 || string(rowsR1Check[0][0].Buf) != "gauge_level_check" {
		t.Errorf("db r1 post-restart: pg_constraint CHECK rows = %v, want exactly [gauge_level_check]", rowsR1Check)
	}
	if _, err := queryUnderDBReload(t, rt2, s2, "r1", "INSERT INTO gauge (id, level, code) VALUES (1, 5, 'ok')"); err != nil {
		t.Errorf("db r1 post-restart: INSERT satisfying gauge_level_check unexpectedly failed: %v", err)
	}
	if _, err := queryUnderDBReload(t, rt2, s2, "r1", "INSERT INTO gauge (id, level, code) VALUES (2, -5, 'ok')"); err == nil {
		t.Error("db r1 post-restart: INSERT violating gauge_level_check unexpectedly succeeded — CHECK enforcement did not survive the restart")
	}

	// M0143-0003d: the NOT NULL constraint row itself, then actual
	// enforcement — Column.NotNull already survived reload before this fix
	// (attnotnull is pg_attribute-backed), so an enforcement-only assertion
	// would pass even with zero code changed; the pg_constraint row is the
	// part this task adds. Filtered to gauge's own conrelid: PRIMARY KEY
	// columns (gauge.id, like parent.id/other.id above) turn out to carry
	// their own named NOT NULL constraint too, so an unfiltered contype='n'
	// scan of the database would also pick up every other table's PK column.
	rowsR1NotNull, err := queryUnderDBReload(t, rt2, s2, "r1",
		"SELECT conname FROM pg_constraint WHERE contype = 'n' AND conrelid = 'gauge'::regclass")
	if err != nil {
		t.Fatalf("db r1 post-restart: SELECT pg_constraint contype=n: %v", err)
	}
	if got, want := conNames(rowsR1NotNull), []string{"gauge_code_not_null", "gauge_id_not_null"}; !slices.Equal(got, want) {
		t.Errorf("db r1 post-restart: pg_constraint NOT NULL rows = %v, want exactly %v", got, want)
	}
	if _, err := queryUnderDBReload(t, rt2, s2, "r1", "INSERT INTO gauge (id, level, code) VALUES (3, 1, NULL)"); err == nil {
		t.Error("db r1 post-restart: INSERT violating gauge_code_not_null unexpectedly succeeded")
	}

	// M0143-0003c: the UNIQUE constraint row itself, then a behavior that only
	// works when catalog.Index.IsConstraint is actually true post-restart —
	// RENAME CONSTRAINT's `!Primary && Unique && IsConstraint` guard
	// (operators_ddl.go) — rather than merely checking the row is visible.
	// Before this fix IsConstraint reverted to false on reload, so this same
	// RENAME would have failed 42704 "constraint ... does not exist" even
	// though the backing index (and its uniqueness enforcement) kept working.
	rowsR1Unique, err := queryUnderDBReload(t, rt2, s2, "r1",
		"SELECT conname FROM pg_constraint WHERE contype = 'u' AND conrelid = 'gauge'::regclass")
	if err != nil {
		t.Fatalf("db r1 post-restart: SELECT pg_constraint contype=u: %v", err)
	}
	if len(rowsR1Unique) != 1 || string(rowsR1Unique[0][0].Buf) != "gauge_tag_unique" {
		t.Errorf("db r1 post-restart: pg_constraint UNIQUE rows = %v, want exactly [gauge_tag_unique]", rowsR1Unique)
	}
	// runChainDDLDurable, not queryUnderDBReload: DDL ops signal completion via
	// a single nil-error/nil-slot Next() rather than EOF, which
	// queryUnderDBReload's underlying runChainQuery (an unconditional
	// Next()-then-Materialize() loop meant for SELECT/INSERT/DML) cannot
	// handle — it would try to Materialize() that nil slot and panic. A
	// pre-fix failure here would surface as runChainDDLDurable's own
	// t.Fatalf (42704 "constraint ... does not exist"), not a silent pass.
	runChainDDLDurable(t, rt2, s2, "r1", "ALTER TABLE gauge RENAME CONSTRAINT gauge_tag_unique TO gauge_tag_uniq2")
	// The first INSERT must actually COMMIT for the second to have something
	// to collide with — queryUnderDBReload always rolls back.
	queryUnderDBReloadDurable(t, rt2, s2, "r1", "INSERT INTO gauge (id, level, code, tag) VALUES (4, 1, 'ok', 'dup')")
	if _, err := queryUnderDBReload(t, rt2, s2, "r1", "INSERT INTO gauge (id, level, code, tag) VALUES (5, 1, 'ok', 'dup')"); err == nil {
		t.Error("db r1 post-restart: INSERT violating gauge_tag_uniq2 unexpectedly succeeded — UNIQUE enforcement did not survive the restart")
	}

	// M0143-0003e: the EXCLUDE constraint row itself, then actual runtime
	// enforcement — checkExclusionConstraintsForInsert gates on
	// idx.IsExclusion/idx.ExclusionOp, neither of which reloaded before this
	// fix, so a pre-fix restart left the row AND the check both silently gone
	// (not merely mislabeled, unlike the UNIQUE case above).
	rowsR1Excl, err := queryUnderDBReload(t, rt2, s2, "r1",
		"SELECT conname FROM pg_constraint WHERE contype = 'x' AND conrelid = 'gauge'::regclass")
	if err != nil {
		t.Fatalf("db r1 post-restart: SELECT pg_constraint contype=x: %v", err)
	}
	if len(rowsR1Excl) != 1 || string(rowsR1Excl[0][0].Buf) != "gauge_zone_excl" {
		t.Errorf("db r1 post-restart: pg_constraint EXCLUDE rows = %v, want exactly [gauge_zone_excl]", rowsR1Excl)
	}
	queryUnderDBReloadDurable(t, rt2, s2, "r1", "INSERT INTO gauge (id, level, code, zone) VALUES (10, 1, 'ok', 7)")
	if _, err := queryUnderDBReload(t, rt2, s2, "r1", "INSERT INTO gauge (id, level, code, zone) VALUES (11, 1, 'ok', 7)"); err == nil {
		t.Error("db r1 post-restart: INSERT violating gauge_zone_excl unexpectedly succeeded — EXCLUDE enforcement did not survive the restart")
	}

	// Namespace isolation must also survive reload: a table created in one
	// database must stay invisible when queried under the other database's
	// own oid.
	if _, err := queryUnderDBReload(t, rt2, s2, "r2", "SELECT id FROM parent"); err == nil {
		t.Error("db r2 post-restart: SELECT FROM parent unexpectedly succeeded — db r1's table leaked into r2's namespace across reload")
	}
	if _, err := queryUnderDBReload(t, rt2, s2, "r1", "SELECT id FROM other"); err == nil {
		t.Error("db r1 post-restart: SELECT FROM other unexpectedly succeeded — db r2's table leaked into r1's namespace across reload")
	}
}
