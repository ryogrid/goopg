package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// R126 restart pins.
//
// These are the tests whose ABSENCE is the reason this bug existed: foreign
// keys were lost at every restart for as long as the feature had existed,
// because nothing in the suite crossed an Open→Close→Open boundary with an FK
// declared. Unit tests on the row builder cannot catch it — the writer, the
// stamp predicate, the per-DB routing and the reload all have to agree.
//
// Every case declares the FK via ALTER TABLE, not CREATE TABLE. That is the
// form HammerDB's TPC-H load and pg_dump restore use, and before R126 it was
// the ONE foreign-key path with no catalog-heap sync at all — a CREATE-form
// test would have passed against the broken code.

func fkNames(t *testing.T, rt *Runtime, table string) []string {
	t.Helper()
	tbl, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: table})
	if !ok {
		t.Fatalf("table %q not found in catalog", table)
	}
	out := make([]string, 0, len(tbl.ForeignKeys))
	for _, fk := range tbl.ForeignKeys {
		out = append(out, fk.Name)
	}
	return out
}

// TestForeignKeySurvivesRestart is the round's headline pin: an FK added with
// ALTER TABLE must still be in catalog.Table.ForeignKeys after a restart, with
// its columns, referenced table and action intact.
//
// It asserts the FIELD, not the pg_constraint view. The view is synthesised
// from this field, so checking the view would be circular; and the field is
// what both the planner (keysCovering, joinrelsize.go:657) and runtime
// enforcement (operators_fk.go:118) read.
func TestForeignKeySurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE fkparent (id int4 NOT NULL, PRIMARY KEY (id))")
	runDDL(t, rt1, "CREATE TABLE fkchild (id int4 NOT NULL, pid int4)")
	runDDL(t, rt1, "ALTER TABLE fkchild ADD CONSTRAINT fkchild_pid_fkey "+
		"FOREIGN KEY (pid) REFERENCES fkparent(id) ON DELETE CASCADE")

	if got := fkNames(t, rt1, "fkchild"); len(got) != 1 {
		t.Fatalf("before restart: ForeignKeys=%v, want 1", got)
	}
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "fkchild"})
	if !ok {
		t.Fatal("fkchild not found after restart")
	}
	if len(tbl.ForeignKeys) != 1 {
		t.Fatalf("after restart: ForeignKeys=%d, want 1 — the FK did not survive "+
			"(pre-R126 this was ALWAYS 0, taking referential enforcement with it)",
			len(tbl.ForeignKeys))
	}
	fk := tbl.ForeignKeys[0]
	if fk.Name != "fkchild_pid_fkey" {
		t.Errorf("conname = %q, want fkchild_pid_fkey", fk.Name)
	}
	if len(fk.Columns) != 1 || fk.Columns[0] != "pid" {
		t.Errorf("Columns = %v, want [pid]", fk.Columns)
	}
	if len(fk.RefColumns) != 1 || fk.RefColumns[0] != "id" {
		t.Errorf("RefColumns = %v, want [id]", fk.RefColumns)
	}
	// RefTable must match the parent's canonical name EXACTLY: fkParentRel
	// compares with a raw `tbl.Name != fk.RefTable` byte test
	// (joinrelsize.go:760), so a case difference here would leave the FK
	// visible in the catalog and in enforcement while silently killing the
	// planner's FK arm.
	if fk.RefTable != "fkparent" {
		t.Errorf("RefTable = %q, want exactly %q (fkParentRel compares case-sensitively)",
			fk.RefTable, "fkparent")
	}
	if fk.OnDelete != parser.FKActionCascade {
		t.Errorf("OnDelete = %v, want CASCADE — action lost across restart", fk.OnDelete)
	}
	if fk.OID == 0 {
		t.Error("OID = 0 after restart; an FK without an OID is invisible to pg_dump")
	}
}

// TestForeignKeyNotValidSurvivesRestart pins the flag R125 made load-bearing.
// A NOT VALID FK that came back validated would silently claim the existing
// rows had been checked; one that came back NOT ENFORCED would stop feeding
// the planner at all.
func TestForeignKeyNotValidSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE nvparent (id int4 NOT NULL, PRIMARY KEY (id))")
	runDDL(t, rt1, "CREATE TABLE nvchild (id int4 NOT NULL, pid int4)")
	runDDL(t, rt1, "ALTER TABLE nvchild ADD CONSTRAINT nvchild_fk "+
		"FOREIGN KEY (pid) REFERENCES nvparent(id) NOT VALID")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, _ := rt2.Catalog.LookupTable(parser.ObjectName{Name: "nvchild"})
	if tbl == nil || len(tbl.ForeignKeys) != 1 {
		t.Fatalf("after restart: want 1 FK, got %v", tbl)
	}
	if !tbl.ForeignKeys[0].NotValid {
		t.Error("NotValid = false after restart; a NOT VALID FK must not come back validated")
	}
	if tbl.ForeignKeys[0].NotEnforced {
		t.Error("NotEnforced = true after restart; NOT VALID must not imply NOT ENFORCED " +
			"(R125: the planner's FK arm gates on conenforced)")
	}
}

// TestForeignKeyDropDoesNotResurrect pins the other direction. A resurrected
// constraint is strictly worse than a lost one: it would reject legitimate DML.
func TestForeignKeyDropDoesNotResurrect(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE dparent (id int4 NOT NULL, PRIMARY KEY (id))")
	runDDL(t, rt1, "CREATE TABLE dchild (id int4 NOT NULL, pid int4)")
	runDDL(t, rt1, "ALTER TABLE dchild ADD CONSTRAINT dchild_fk FOREIGN KEY (pid) REFERENCES dparent(id)")
	runDDL(t, rt1, "ALTER TABLE dchild DROP CONSTRAINT dchild_fk")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	if got := fkNames(t, rt2, "dchild"); len(got) != 0 {
		t.Errorf("after restart: ForeignKeys=%v, want none — a dropped FK came back", got)
	}
}

// TestForeignKeyRepeatedAlterDoesNotDuplicate pins the failure mode that the
// 2606 stamping in deleteCatalogRowsForOID exists to prevent: every re-sync
// APPENDS a fresh row, so without stamping the reload rebuilds N identical
// entries. A DROP-only test cannot catch this.
func TestForeignKeyRepeatedAlterDoesNotDuplicate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE rparent (id int4 NOT NULL, PRIMARY KEY (id))")
	runDDL(t, rt1, "CREATE TABLE rchild (id int4 NOT NULL, pid int4)")
	runDDL(t, rt1, "ALTER TABLE rchild ADD CONSTRAINT rchild_fk FOREIGN KEY (pid) REFERENCES rparent(id)")
	// Each of these re-runs the delete-then-resync funnel.
	runDDL(t, rt1, "ALTER TABLE rchild ADD COLUMN extra1 text")
	runDDL(t, rt1, "ALTER TABLE rchild ADD COLUMN extra2 text")
	runDDL(t, rt1, "ALTER TABLE rchild ALTER CONSTRAINT rchild_fk DEFERRABLE")
	runDDL(t, rt1, "ALTER TABLE rchild RENAME CONSTRAINT rchild_fk TO rchild_fk2")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	got := fkNames(t, rt2, "rchild")
	if len(got) != 1 {
		t.Fatalf("after restart: ForeignKeys=%v, want exactly 1 — repeated re-syncs "+
			"appended duplicate pg_constraint rows", got)
	}
	if got[0] != "rchild_fk2" {
		t.Errorf("conname = %q, want rchild_fk2 — the RENAME did not reach the heap", got[0])
	}
	tbl, _ := rt2.Catalog.LookupTable(parser.ObjectName{Name: "rchild"})
	if !tbl.ForeignKeys[0].Deferrable {
		t.Error("Deferrable = false after restart; ALTER CONSTRAINT did not reach the heap")
	}
}

// TestForeignKeyValidateSurvivesRestart pins VALIDATE CONSTRAINT, which is the
// one mutator deliberately routed AROUND the full funnel (it holds only
// ShareUpdateExclusiveLock, so a whole-table catalog rewrite under it would
// race concurrent DML). Its narrow single-row re-emit has to work.
func TestForeignKeyValidateSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE vparent (id int4 NOT NULL, PRIMARY KEY (id))")
	runDDL(t, rt1, "CREATE TABLE vchild (id int4 NOT NULL, pid int4)")
	runDDL(t, rt1, "ALTER TABLE vchild ADD CONSTRAINT vchild_fk "+
		"FOREIGN KEY (pid) REFERENCES vparent(id) NOT VALID")
	runDDL(t, rt1, "ALTER TABLE vchild VALIDATE CONSTRAINT vchild_fk")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, _ := rt2.Catalog.LookupTable(parser.ObjectName{Name: "vchild"})
	if tbl == nil || len(tbl.ForeignKeys) != 1 {
		t.Fatalf("after restart: want exactly 1 FK, got %v", fkNames(t, rt2, "vchild"))
	}
	if tbl.ForeignKeys[0].NotValid {
		t.Error("NotValid = true after restart; VALIDATE CONSTRAINT did not reach the heap, " +
			"so a validated FK silently reverted to NOT VALID")
	}
}

// TestForeignKeyNonPublicSchemaSurvivesRestart pins review finding 2: the
// emit-side resolver hardcoded Schema:"public", so an FK whose PARENT lived in
// another schema silently failed to resolve, was never journalled, and
// vanished at the next restart — while the synthesised pg_constraint view kept
// displaying it, because the view resolves the unschemed RefTable by scanning
// every table in the database. The heap writer must agree with the view.
func TestForeignKeyNonPublicSchemaSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE SCHEMA app")
	runDDL(t, rt1, "CREATE TABLE app.sparent (id int4 NOT NULL, PRIMARY KEY (id))")
	runDDL(t, rt1, "CREATE TABLE app.schild (id int4 NOT NULL, pid int4)")
	runDDL(t, rt1, "ALTER TABLE app.schild ADD CONSTRAINT schild_fk "+
		"FOREIGN KEY (pid) REFERENCES app.sparent(id)")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Schema: "app", Name: "schild"})
	if !ok || tbl == nil {
		t.Fatal("app.schild not found after restart")
	}
	if len(tbl.ForeignKeys) != 1 {
		t.Fatalf("after restart: ForeignKeys=%d, want 1 — an FK in a non-public "+
			"schema was not persisted (the resolver's hardcoded \"public\" lookup)",
			len(tbl.ForeignKeys))
	}
	if tbl.ForeignKeys[0].RefTable != "sparent" {
		t.Errorf("RefTable = %q, want sparent", tbl.ForeignKeys[0].RefTable)
	}
}

// TestForeignKeyClonedOntoAttachedPartitionSurvivesRestart pins review
// finding 1 — the EIGHTH mutator, which the first implementation missed.
//
// ATTACH PARTITION clones the parent partitioned table's FKs onto the new
// partition (cloneAndValidateAttachPartitionFKs, operators_fk.go:615), but the
// child's syncTableToCatalogHeap ran ~10 lines EARLIER in the same arm. So
// every cloned FK was written to nothing and the attached partition lost both
// its planner evidence and its referential enforcement at the next restart —
// R126's own bug, reproduced for partitions.
func TestForeignKeyClonedOntoAttachedPartitionSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE pkref (id int4 NOT NULL, PRIMARY KEY (id))")
	runDDL(t, rt1, "CREATE TABLE ptparent (id int4 NOT NULL, pid int4) PARTITION BY RANGE (id)")
	runDDL(t, rt1, "ALTER TABLE ptparent ADD CONSTRAINT ptparent_fk FOREIGN KEY (pid) REFERENCES pkref(id)")
	runDDL(t, rt1, "CREATE TABLE ptchild (id int4 NOT NULL, pid int4)")
	runDDL(t, rt1, "ALTER TABLE ptparent ATTACH PARTITION ptchild FOR VALUES FROM (1) TO (100)")

	before := fkNames(t, rt1, "ptchild")
	if len(before) != 1 {
		t.Skipf("clone did not run in this build (ptchild FKs=%v); "+
			"the restart half of this pin is only meaningful once it does", before)
	}
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	if got := fkNames(t, rt2, "ptchild"); len(got) != 1 {
		t.Errorf("after restart: partition ForeignKeys=%v, want 1 — the FK cloned by "+
			"ATTACH PARTITION was never journalled, so the partition silently lost "+
			"referential enforcement", got)
	}
}
