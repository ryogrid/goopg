package executor

// operators_ddl_alter_type_coercion_test.go pins M0134-0002 C10: the static
// assignment-coercibility gate on `ALTER TABLE t ALTER COLUMN c TYPE <type>`
// WITHOUT USING. PG coerces the old column datum with COERCION_ASSIGNMENT
// (postgres/src/backend/commands/tablecmds.c:14491-14496) and find_coercion_pathway
// (postgres/src/backend/parser/parse_coerce.c:3152) returns NONE for the pairs
// goopg's permissive shared evalCast would otherwise accept (int2/int4/int8 →
// bool, text → int2/int4/int8), so PG raises 42804 at parse time — even on an
// empty table, where no per-row coercion ever runs. The shared evalCast is NOT
// changed: explicit :: casts and narrowing (int8→int4) keep working.

import (
	"testing"
)

// TestAlterColumnTypeAssignCastGate is the table-driven C10 gate: reject pairs
// must raise PG's byte-exact 42804 (message + hint, Pos 0), allow pairs must
// succeed with the rewrite preserving data.
func TestAlterColumnTypeAssignCastGate(t *testing.T) {
	cases := []struct {
		name     string
		colType  string
		newType  string
		reject   bool
		wantMsg  string
		wantHint string
	}{
		{name: "int8-to-bool-reject", colType: "bigint", newType: "boolean", reject: true,
			wantMsg:  `column "c" cannot be cast automatically to type boolean`,
			wantHint: `You might need to specify "USING c::boolean".`},
		{name: "int4-to-bool-reject", colType: "integer", newType: "boolean", reject: true,
			wantMsg:  `column "c" cannot be cast automatically to type boolean`,
			wantHint: `You might need to specify "USING c::boolean".`},
		{name: "int2-to-bool-reject", colType: "smallint", newType: "boolean", reject: true,
			wantMsg:  `column "c" cannot be cast automatically to type boolean`,
			wantHint: `You might need to specify "USING c::boolean".`},
		{name: "text-to-int4-reject", colType: "text", newType: "integer", reject: true,
			wantMsg:  `column "c" cannot be cast automatically to type integer`,
			wantHint: `You might need to specify "USING c::integer".`},
		{name: "int8-to-int4-allow", colType: "bigint", newType: "integer", reject: false},
		{name: "same-type-allow", colType: "integer", newType: "integer", reject: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			if err := runDDL(t, ctx, "CREATE TABLE t (c "+tc.colType+")"); err != nil {
				t.Fatalf("CREATE TABLE t (%s): %v", tc.colType, err)
			}
			if err := runDDL(t, ctx, "INSERT INTO t VALUES (42)"); err != nil {
				t.Fatalf("INSERT t: %v", err)
			}

			err := runDDL(t, ctx, "ALTER TABLE t ALTER COLUMN c TYPE "+tc.newType)
			if tc.reject {
				ee := assertExecError(t, err, tc.wantMsg)
				if ee.Hint != tc.wantHint {
					t.Errorf("hint = %q, want %q", ee.Hint, tc.wantHint)
				}
				if ee.Code != "42804" {
					t.Errorf("code = %q, want 42804", ee.Code)
				}
				if ee.Pos != 0 {
					t.Errorf("Pos = %d, want 0 (PG's ATPrepAlterColumnType 42804 carries no errposition)", ee.Pos)
				}
				// The rewrite must not have run: table intact with its row.
				rows := runQueryRows(t, ctx, "SELECT count(*) FROM t")
				if rows[0][0].Int != 1 {
					t.Errorf("row count after rejected ALTER = %d, want 1 (data loss?)", rows[0][0].Int)
				}
				return
			}
			if err != nil {
				t.Fatalf("ALTER TYPE %s: unexpected error: %v", tc.newType, err)
			}
			// Allow case: the rewrite succeeded and preserved the value.
			rows := runQueryRows(t, ctx, "SELECT c::text FROM t")
			if len(rows) != 1 || rows[0][0].StringValue() != "42" {
				t.Errorf("value after ALTER TYPE %s = %+v, want [42]", tc.newType, rows)
			}
		})
	}
}

// TestAlterColumnTypeAssignCastGateEmptyTable proves the static gate fires
// BEFORE the nBlocks==0 early return: an empty table (no heap rewrite at all)
// still raises 42804, because PG's ATPrepAlterColumnType coercion happens at
// parse time, independent of row count (tablecmds.c:14491-14517).
func TestAlterColumnTypeAssignCastGateEmptyTable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (c bigint)"); err != nil {
		t.Fatalf("CREATE TABLE t: %v", err)
	}
	// No INSERT: the table is empty, nBlocks == 0.
	err := runDDL(t, ctx, "ALTER TABLE t ALTER COLUMN c TYPE boolean")
	ee := assertExecError(t, err, `column "c" cannot be cast automatically to type boolean`)
	if ee.Hint != `You might need to specify "USING c::boolean".` {
		t.Errorf("hint = %q, want %q", ee.Hint, `You might need to specify "USING c::boolean".`)
	}
	if ee.Code != "42804" {
		t.Errorf("code = %q, want 42804", ee.Code)
	}
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0", ee.Pos)
	}
}
