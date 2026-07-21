package executor

// R3-5 executor verification: lowering a DML WHERE sublink to param slots
// must not change which rows the statement touches.
//
// Lowering changes only HOW the outer value reaches the subplan — a param
// slot bound per row instead of a full outer row pushed onto ctx.OuterRows
// — never when the subplan runs or which snapshot it sees. That argument
// is sound but load-bearing, so these tests check the observable effects
// on both settings of the rescan/hashed kill switches, and cover the two
// shapes where an unsound lowering would show up first: a self-referencing
// UPDATE (Halloween) and a NOT EXISTS whose result flips per row.

import (
	"testing"
)

func newDMLSublinkFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE dml_t (id int, v int, tag text)",
		"CREATE TABLE dml_u (uid int, w int)",
		"INSERT INTO dml_t VALUES (1, 10, 'keep')",
		"INSERT INTO dml_t VALUES (2, 20, 'keep')",
		"INSERT INTO dml_t VALUES (3, 30, 'keep')",
		"INSERT INTO dml_t VALUES (4, 40, 'keep')",
		"INSERT INTO dml_u VALUES (1, 10)",
		"INSERT INTO dml_u VALUES (3, 99)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

// runDMLAndSnapshot executes a DML statement and returns the resulting
// table contents, so the assertion is on row EFFECTS rather than on any
// internal counter.
func runDMLAndSnapshot(t *testing.T, ctx *Context, dml string) []string {
	t.Helper()
	if err := runDDL(t, ctx, dml); err != nil {
		t.Fatalf("%s: %v", dml, err)
	}
	rows, err := runQueryWithErr(ctx, "SELECT id, v, tag FROM dml_t ORDER BY id")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return renderRows(rows)
}

// dmlCases are statements whose WHERE clause carries a correlated sublink.
var dmlCases = []struct {
	name string
	dml  string
	want []string
}{
	{
		name: "update-where-exists",
		// ids 1 and 3 have a dml_u row; 2 and 4 do not.
		dml:  "UPDATE dml_t SET tag = 'hit' WHERE EXISTS (SELECT 1 FROM dml_u WHERE uid = id)",
		want: []string{"1|10|hit", "2|20|keep", "3|30|hit", "4|40|keep"},
	},
	{
		name: "update-where-not-exists",
		dml:  "UPDATE dml_t SET tag = 'miss' WHERE NOT EXISTS (SELECT 1 FROM dml_u WHERE uid = id)",
		want: []string{"1|10|keep", "2|20|miss", "3|30|keep", "4|40|miss"},
	},
	{
		name: "update-where-correlated-in",
		// v IN (w where uid = id): id=1 has w=10 == v=10 -> hit;
		// id=3 has w=99 != v=30 -> no.
		dml:  "UPDATE dml_t SET tag = 'in' WHERE v IN (SELECT w FROM dml_u WHERE uid = id)",
		want: []string{"1|10|in", "2|20|keep", "3|30|keep", "4|40|keep"},
	},
	{
		name: "update-where-scalar-subquery",
		dml:  "UPDATE dml_t SET tag = 'scalar' WHERE v = (SELECT w FROM dml_u WHERE uid = id)",
		want: []string{"1|10|scalar", "2|20|keep", "3|30|keep", "4|40|keep"},
	},
	{
		name: "delete-where-exists",
		dml:  "DELETE FROM dml_t WHERE EXISTS (SELECT 1 FROM dml_u WHERE uid = id)",
		want: []string{"2|20|keep", "4|40|keep"},
	},
	{
		name: "delete-where-correlated-in",
		dml:  "DELETE FROM dml_t WHERE v IN (SELECT w FROM dml_u WHERE uid = id)",
		want: []string{"2|20|keep", "3|30|keep", "4|40|keep"},
	},
	{
		name: "update-self-referencing-halloween",
		// The subquery reads the table being written. PG evaluates the
		// WHERE against the pre-update snapshot, so every row whose id
		// exceeds the current minimum qualifies exactly once — a row
		// updated mid-scan must not re-qualify or vanish.
		dml:  "UPDATE dml_t SET v = v + 100 WHERE EXISTS (SELECT 1 FROM dml_t inner_t WHERE inner_t.id < dml_t.id)",
		want: []string{"1|10|keep", "2|120|keep", "3|130|keep", "4|140|keep"},
	},
	{
		name: "update-self-referencing-reads-written-column",
		// Sharper than the previous case: the subquery reads the very
		// column being written, and each row's predicate depends on a
		// value an earlier row's update would destroy. Against the
		// pre-update snapshot (PG's semantics) ids 2,3,4 all qualify —
		// v-10 is present for each. Against a moving snapshot, row 2's
		// write to 15 removes the 20 that row 3 needs, so row 3 (and
		// then row 4) would stop qualifying. The two readings give
		// different tables, so this case actually discriminates.
		dml:  "UPDATE dml_t SET v = v - 5 WHERE EXISTS (SELECT 1 FROM dml_t i WHERE i.v = dml_t.v - 10)",
		want: []string{"1|10|keep", "2|15|keep", "3|25|keep", "4|35|keep"},
	},
}

// TestDMLSublinkRowEffects runs each statement on a fresh fixture and
// asserts the resulting table contents.
func TestDMLSublinkRowEffects(t *testing.T) {
	for _, tc := range dmlCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := newDMLSublinkFixture(t)
			defer cleanup()
			got := runDMLAndSnapshot(t, ctx, tc.dml)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("row %d: got %q want %q (all: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestDMLSublinkEngineSettingsAgree re-runs every case with the rescan
// engine and the hashed IN probe disabled. Lowering feeds both, so a
// difference between settings would mean the param-slot path and the
// legacy path disagree — exactly what "lowering is semantics-preserving"
// forbids.
func TestDMLSublinkEngineSettingsAgree(t *testing.T) {
	for _, tc := range dmlCases {
		t.Run(tc.name, func(t *testing.T) {
			SetSubPlanRescanEnabled(true)
			SetHashedSubPlanEnabled(true)
			t.Cleanup(func() {
				SetSubPlanRescanEnabled(true)
				SetHashedSubPlanEnabled(true)
			})
			ctxA, cleanupA := newDMLSublinkFixture(t)
			withEngines := runDMLAndSnapshot(t, ctxA, tc.dml)
			cleanupA()

			SetSubPlanRescanEnabled(false)
			SetHashedSubPlanEnabled(false)
			ctxB, cleanupB := newDMLSublinkFixture(t)
			without := runDMLAndSnapshot(t, ctxB, tc.dml)
			cleanupB()

			if len(withEngines) != len(without) {
				t.Fatalf("engine settings disagree: %v vs %v", withEngines, without)
			}
			for i := range withEngines {
				if withEngines[i] != without[i] {
					t.Fatalf("row %d: engines-on %q vs engines-off %q", i, withEngines[i], without[i])
				}
			}
		})
	}
}
