package executor

import "testing"

// M0122-0005 follow-up (deferral ledger 2026-07-06 "UPDATE enforces no
// table-level NOT NULL/CHECK constraints"): UPDATE (every write path) and
// INSERT ... ON CONFLICT DO UPDATE previously wrote the new row without
// running any NOT NULL / CHECK / domain constraint check at all, silently
// allowing NULLs into NOT NULL columns and values violating CHECK/domain
// constraints. These tests cover each independent write path that gained the
// checkRowConstraintsForWrite call.

func wantExecError23502(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a 23502 not-null-violation error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23502" {
		t.Fatalf("expected ExecError 23502, got: %v", err)
	}
}

func wantExecError23514(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a 23514 check-violation error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23514" {
		t.Fatalf("expected ExecError 23514, got: %v", err)
	}
}

// TestUpdateSeqScanEnforcesNotNull covers updateOp.Next's SeqScan fallback
// path (no usable index for the WHERE clause).
func TestUpdateSeqScanEnforcesNotNull(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE t_upd_nn (id int, v int NOT NULL)`,
		`INSERT INTO t_upd_nn VALUES (1, 10)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `UPDATE t_upd_nn SET v = 20 WHERE v = 10`); err != nil {
		t.Fatalf("valid update failed: %v", err)
	}
	wantExecError23502(t, runDDL(t, ctx, `UPDATE t_upd_nn SET v = NULL WHERE id = 1`))
}

// TestUpdateSeqScanEnforcesCheckConstraint covers the same path for a table
// CHECK constraint.
func TestUpdateSeqScanEnforcesCheckConstraint(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE t_upd_chk (id int, v int CHECK (v > 0))`,
		`INSERT INTO t_upd_chk VALUES (1, 10)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	wantExecError23514(t, runDDL(t, ctx, `UPDATE t_upd_chk SET v = -5 WHERE id = 1`))
}

// TestUpdateViaIndexEnforcesNotNull covers updateOp.updateViaIndex — the
// B-tree point-lookup path chosen when the WHERE clause matches a PK/unique
// index (a sibling code path to the SeqScan fallback above).
func TestUpdateViaIndexEnforcesNotNull(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE t_upd_idx_nn (id int PRIMARY KEY, v int NOT NULL)`,
		`INSERT INTO t_upd_idx_nn VALUES (1, 10)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	wantExecError23502(t, runDDL(t, ctx, `UPDATE t_upd_idx_nn SET v = NULL WHERE id = 1`))
}

// TestUpdateFromEnforcesNotNull covers updateOp.updateWithFrom (UPDATE ...
// FROM).
func TestUpdateFromEnforcesNotNull(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE t_upd_from_tgt (id int, v int NOT NULL)`,
		`CREATE TABLE t_upd_from_src (id int, nv int)`,
		`INSERT INTO t_upd_from_tgt VALUES (1, 10)`,
		`INSERT INTO t_upd_from_src VALUES (1, NULL)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `UPDATE t_upd_from_tgt SET v = t_upd_from_src.nv FROM t_upd_from_src WHERE t_upd_from_tgt.id = t_upd_from_src.id`)
	wantExecError23502(t, err)
}

// TestUpsertInsertArmEnforcesCheckConstraint covers upsertOp.applyInsert (the
// plain-insert arm of INSERT ... ON CONFLICT, taken when there is no
// conflicting row).
func TestUpsertInsertArmEnforcesCheckConstraint(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE t_upsert_ins_chk (id int PRIMARY KEY, v int CHECK (v > 0))`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `INSERT INTO t_upsert_ins_chk VALUES (1, -5) ON CONFLICT (id) DO UPDATE SET v = excluded.v`)
	wantExecError23514(t, err)
}

// TestUpsertUpdateArmEnforcesNotNull covers upsertOp.applyUpdate (the DO
// UPDATE arm, taken when the arbiter key already conflicts).
func TestUpsertUpdateArmEnforcesNotNull(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE t_upsert_upd_nn (id int PRIMARY KEY, v int NOT NULL)`,
		`INSERT INTO t_upsert_upd_nn VALUES (1, 10)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `INSERT INTO t_upsert_upd_nn VALUES (1, 999) ON CONFLICT (id) DO UPDATE SET v = NULL`)
	wantExecError23502(t, err)
}
