package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFKOmittedRefColumnsResolveToPrimaryKey guards M0134-0171.
//
// A `REFERENCES <table>` clause that omits the referenced-column list resolves
// to the referenced table's PRIMARY KEY — upstream does this once at
// constraint-definition time in transformFkeyGetPrimaryKey
// (postgres/src/backend/commands/tablecmds.c:13382, called from
// ATAddForeignKeyConstraint :10192), building the list "from the indkey
// definition" of the index marked indisprimary.
//
// goopg models the same thing lazily: catalog.ForeignKey.RefColumns is left
// empty and every runtime consumer resolves it through pkColumns(). That
// helper IGNORED the indexes and returned tbl.Columns[0] — the table's first
// column — while carrying a doc comment that already described the index scan
// it never performed. Three wrong answers followed, and only the third was
// ever exercised by the existing single-column tests:
//
//   - multi-column PK  → 1 of N columns, so the arity mismatch made the FK
//     check compare N values against 1 column and EVERY valid row was
//     rejected with a bogus 23503;
//   - single-column PK that is not column 1 → the FK was silently enforced
//     against the WRONG column (a data-integrity hole, not a cosmetic one);
//   - single-column PK that IS column 1 → accidentally correct.
//
// Verified against the PG 18.3 oracle. Reverting pkColumns to
// `return []string{tbl.Columns[0].Name}` fails MultiColumnPK (both subcases)
// and PrimaryKeyNotFirstColumn.
func TestFKOmittedRefColumnsResolveToPrimaryKey(t *testing.T) {
	// A multi-column PK must resolve to ALL its columns. Before the fix the
	// first INSERT — which PG accepts — failed with
	// "Key (x, y)=(1, 2) is not present in table "fkpk1"".
	t.Run("MultiColumnPK", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		mustRunDDL(t, ctx, `CREATE TABLE fkpk1 (a int, b int, c text, PRIMARY KEY (a, b))`)
		mustRunDDL(t, ctx, `INSERT INTO fkpk1 VALUES (1, 2, 'x'), (3, 4, 'y')`)
		mustRunDDL(t, ctx, `CREATE TABLE fkchild1 (x int, y int,
			CONSTRAINT fkchild1_ref FOREIGN KEY (x, y) REFERENCES fkpk1)`)

		// Present in the parent on BOTH key columns → accepted.
		mustRunDDL(t, ctx, `INSERT INTO fkchild1 VALUES (1, 2)`)

		// Matches the parent on the first key column only → still a violation.
		// This is the case the old first-column-only resolution wrongly ALLOWED.
		requireExecError(t, runDDL(t, ctx, `INSERT INTO fkchild1 VALUES (1, 99)`),
			"23503", `violates foreign key constraint`)
	})

	// The PK is the SECOND column, so first-column resolution silently pointed
	// the FK at "label" (text) instead of "id" (int).
	t.Run("PrimaryKeyNotFirstColumn", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		mustRunDDL(t, ctx, `CREATE TABLE fkpk2 (label text, id int PRIMARY KEY)`)
		mustRunDDL(t, ctx, `INSERT INTO fkpk2 VALUES ('aaa', 1), ('bbb', 2)`)
		mustRunDDL(t, ctx, `CREATE TABLE fkchild2 (x int,
			CONSTRAINT fkchild2_ref FOREIGN KEY (x) REFERENCES fkpk2)`)

		// 1 is present in fkpk2.id → accepted.
		mustRunDDL(t, ctx, `INSERT INTO fkchild2 VALUES (1)`)

		// 999 is in neither column → rejected. Before the fix the check ran
		// against fkpk2.label, so the referenced values were 'aaa'/'bbb'.
		requireExecError(t, runDDL(t, ctx, `INSERT INTO fkchild2 VALUES (999)`),
			"23503", `violates foreign key constraint`)
	})

	// A self-referencing FK resolves its own table's PK. goopg registers
	// CREATE TABLE-time FKs BEFORE it creates the PK index, so this only works
	// because resolution stays lazy (deferred to the runtime check) rather
	// than being pinned at DDL time — the ordering constraint that kept this
	// fix out of the DDL path. Ledgered as M0134-0171a.
	t.Run("SelfReferencingFK", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		mustRunDDL(t, ctx, `CREATE TABLE fkself (id int PRIMARY KEY, parent int REFERENCES fkself)`)
		mustRunDDL(t, ctx, `INSERT INTO fkself VALUES (1, NULL)`)
		mustRunDDL(t, ctx, `INSERT INTO fkself VALUES (2, 1)`)
		requireExecError(t, runDDL(t, ctx, `INSERT INTO fkself VALUES (3, 99)`),
			"23503", `violates foreign key constraint`)
	})

	// pkColumns itself: the PK columns come back in index-key order, and a
	// table with no primary key resolves to nil rather than to its first
	// column. The nil case is what a DDL-time port would turn into upstream's
	// 42704 "there is no primary key for referenced table" (tablecmds.c:13437);
	// goopg does not raise that yet — ledgered as M0134-0171b.
	t.Run("PkColumnsHelper", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		mustRunDDL(t, ctx, `CREATE TABLE fkpk3 (a int, b int, c text, PRIMARY KEY (b, a))`)
		mustRunDDL(t, ctx, `CREATE TABLE fknopk (a int, b int)`)

		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "fkpk3"})
		if !ok {
			t.Fatal("fkpk3 not found")
		}
		got := pkColumns(ctx, tbl)
		want := []string{"b", "a"} // declaration order of the PK, not of the table
		if len(got) != len(want) {
			t.Fatalf("pkColumns(fkpk3) = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("pkColumns(fkpk3) = %v, want %v", got, want)
			}
		}

		nopk, ok := cat.LookupTable(parser.ObjectName{Name: "fknopk"})
		if !ok {
			t.Fatal("fknopk not found")
		}
		if got := pkColumns(ctx, nopk); got != nil {
			t.Errorf("pkColumns(fknopk) = %v, want nil (no primary key)", got)
		}
	})
}

// mustRunDDL runs stmt and fails the test if it errors.
func mustRunDDL(t *testing.T, ctx *Context, stmt string) {
	t.Helper()
	if err := runDDL(t, ctx, stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}
