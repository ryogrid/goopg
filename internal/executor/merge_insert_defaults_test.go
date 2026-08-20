package executor

import "testing"

// TestMergeNotMatchedInsertAppliesDefaults — M0134-0044, MERGE-INSERT twin of
// the plain-INSERT/upsert DEFAULT-substitution fix (operators_storage.go,
// operators_upsert.go:198-214). Before this fix, `WHEN NOT MATCHED THEN
// INSERT (col-list)` left every target column not named in the column list
// as SQL NULL instead of applying its column DEFAULT — PG's MERGE NOT
// MATCHED insert funnels through the same ExecInsert default-substitution
// machinery as plain INSERT (postgres/src/backend/executor/nodeModifyTable.c
// ExecMergeMatched has no separate default-skipping code path).
func TestMergeNotMatchedInsertAppliesDefaults(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, sql := range []string{
		`CREATE TABLE merge_def_tgt (id int PRIMARY KEY, balance int DEFAULT -1)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}

	if err := runMergeStmt(t, ctx,
		`MERGE INTO merge_def_tgt USING (VALUES (1)) AS s(id) ON merge_def_tgt.id = s.id `+
			`WHEN NOT MATCHED THEN INSERT (id) VALUES (s.id)`); err != nil {
		t.Fatalf("MERGE: %v", err)
	}

	rows := runSQL(t, ctx, `SELECT id, balance FROM merge_def_tgt`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 1 {
		t.Errorf("id: got %v want 1", rows[0][0].Int)
	}
	if rows[0][1].IsNull() {
		t.Fatalf("balance: got NULL want -1 (DEFAULT) — MERGE NOT MATCHED INSERT omitted-column default not applied")
	}
	if rows[0][1].Int != -1 {
		t.Errorf("balance: got %v want -1", rows[0][1].Int)
	}
}

// TestMergeNotMatchedInsertAutoGeneratesSerial covers the sibling case: a
// SERIAL/IDENTITY target column omitted from the MERGE INSERT column list
// must get a real generated value via autoGenerateSerialValues, not NULL —
// mirrors TestInsertExplicitColumnListOmittedSerialColumnNoDoubleAdvance
// (default_omitted_column_test.go) and the ON CONFLICT sibling coverage in
// operators_upsert.go:214.
func TestMergeNotMatchedInsertAutoGeneratesSerial(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, sql := range []string{
		`CREATE TABLE merge_serial_tgt (id serial PRIMARY KEY, v text)`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}

	if err := runMergeStmt(t, ctx,
		`MERGE INTO merge_serial_tgt USING (VALUES ('a')) AS s(v) ON merge_serial_tgt.v = s.v `+
			`WHEN NOT MATCHED THEN INSERT (v) VALUES (s.v)`); err != nil {
		t.Fatalf("MERGE: %v", err)
	}

	rows := runSQL(t, ctx, `SELECT id, v FROM merge_serial_tgt`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].IsNull() {
		t.Fatalf("id: got NULL want a generated serial value — MERGE NOT MATCHED INSERT omitted serial column not auto-generated")
	}
	if rows[0][0].Int != 1 {
		t.Errorf("id: got %v want 1", rows[0][0].Int)
	}
	if rows[0][1].StringValue() != "a" {
		t.Errorf("v: got %q want \"a\"", rows[0][1].StringValue())
	}
}
