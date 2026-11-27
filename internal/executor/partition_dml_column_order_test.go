package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestPartitionDMLMismatchedColumnOrder pins M0149-0061a against PG 18.3:
// UPDATE and DELETE through a partitioned parent reach a partition whose
// columns are in a different order (ATTACH of a separately created table).
// The parent's WHERE / SET / RETURNING evaluate in the parent's column space,
// a moved row is routed on the parent-ordered row and written in the
// destination's order. Before, the WHERE was evaluated against the leaf's raw
// column order: `UPDATE tab1 SET a = 2 WHERE a = 1` reported UPDATE 0 on a
// row that exists, and DELETE likewise missed it.
func TestPartitionDMLMismatchedColumnOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE pco (a int PRIMARY KEY, b text) PARTITION BY LIST (a)",
		"CREATE TABLE pco_1 (b text, a int NOT NULL)",
		"ALTER TABLE pco ATTACH PARTITION pco_1 FOR VALUES IN (1, 2, 3)",
		"CREATE TABLE pco_2 PARTITION OF pco FOR VALUES IN (4, 5, 6)",
		"INSERT INTO pco VALUES (1, 'one'), (3, 'three'), (5, 'five')",
		"UPDATE pco SET a = 2 WHERE a = 1",        // within pco_1
		"UPDATE pco SET a = 4 WHERE a = 2",        // pco_1 -> pco_2
		"UPDATE pco SET a = 1 WHERE a = 5",        // pco_2 -> pco_1
		"UPDATE pco SET b = b || '!' WHERE a = 3", // non-key, pco_1
		"DELETE FROM pco WHERE b = 'one'",         // pco_2 row moved there
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT a, b FROM pco ORDER BY a", ps)), ";")
	if want := "1|five;3|three!"; got != want {
		t.Errorf("rows = %q, want %q", got, want)
	}
	runSQL(t, ctx, "DELETE FROM pco WHERE a = 3")
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT a FROM pco ORDER BY a", ps)), ";"); got != "1" {
		t.Errorf("after DELETE: %q, want 1", got)
	}
}

// TestAlterTableColumnRecursesIntoPartitions pins M0149-0061g against PG
// 18.3: ALTER TABLE ... DROP COLUMN / ADD COLUMN on a partitioned table
// recurse into its partitions (ATExecDropColumn / ATExecAddColumn over
// find_inheritance_children) — including a partition attached with its
// columns in another order — and into inheritance children whose column is
// inherited. Before, the partitions kept the dropped column and never got
// the added one.
func TestAlterTableColumnRecursesIntoPartitions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE acr (a int PRIMARY KEY, b text) PARTITION BY LIST (a)",
		"CREATE TABLE acr_1 (b text, a int NOT NULL)",
		"ALTER TABLE acr ATTACH PARTITION acr_1 FOR VALUES IN (0, 1, 2, 3)",
		"CREATE TABLE acr_2 PARTITION OF acr FOR VALUES IN (5, 6)",
		"INSERT INTO acr VALUES (0, 'zero'), (6, 'six')",
		"ALTER TABLE acr DROP b, ADD COLUMN c text DEFAULT 'dflt', ADD b text",
		"INSERT INTO acr (a, b) VALUES (1, 'x'), (5, 'y')",
		"CREATE TABLE acr_ih (x int, y int)",
		"CREATE TABLE acr_ihc (z int) INHERITS (acr_ih)",
		"ALTER TABLE acr_ih DROP y, ADD w int",
	} {
		runSQL(t, ctx, q)
	}
	names := func(tbl string) string {
		tt, ok := cat.LookupTable(parser.ObjectName{Name: tbl})
		if !ok {
			t.Fatalf("%s not found", tbl)
		}
		var out []string
		for _, c := range tt.Columns {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	for tbl, want := range map[string]string{"acr_1": "a,c,b", "acr_2": "a,c,b", "acr_ihc": "x,z,w"} {
		if got := names(tbl); got != want {
			t.Errorf("%s columns = %s, want %s", tbl, got, want)
		}
	}
	ps := optimizer.DefaultPlannerSettings()
	got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT a, c, b FROM acr ORDER BY a", ps)), ";")
	if want := "0|dflt|NULL;1|dflt|x;5|dflt|y;6|dflt|NULL"; got != want {
		t.Errorf("rows = %q, want %q", got, want)
	}
}
