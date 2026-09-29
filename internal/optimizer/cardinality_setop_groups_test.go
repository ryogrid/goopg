package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestGroupsOverSetOpClampPerRelation pins M0146-0005bb against PG 18.3's
// estimate_num_groups: grouping columns of a set-operation subquery have no
// statistics, but examine_variable still names their relation, and the
// per-relation clamp caps the product of their 200-default distinct counts at
// a tenth of that relation's rows when several of them come from it (TPC-DS
// Q75's all_sales: 121550 / 10 = 12155 groups).
func TestGroupsOverSetOpClampPerRelation(t *testing.T) {
	union := &SetOp{Op: parser.SetOpUnion, All: true,
		Left: scanWithStats("a", 5000, 100, 100), Right: scanWithStats("b", 5000, 100, 100)}
	rows := EstimateRows(union)
	groups := estimateNumGroups([]Expr{&ColumnRef{Index: 0}, &ColumnRef{Index: 1}}, union, rows)
	if want := rows / 10; groups != want {
		t.Fatalf("two stats-less columns of a %d-row union: %d groups, want the per-relation clamp %d", rows, groups, want)
	}
}

// TestGroupsOverPartitionAppendNotClampedAsSubquery: a partitioned (or
// inherited) table's UNION ALL expansion is one relation to PG, whose
// variables read the parent's inherited statistics — not the stats-less
// subquery default the per-relation clamp above applies to.
func TestGroupsOverPartitionAppendNotClampedAsSubquery(t *testing.T) {
	p1, p2 := scanWithStats("p1", 5000, 100, 100), scanWithStats("p2", 5000, 100, 100)
	p1.Table.PartitionParentOID, p2.Table.PartitionParentOID = 42, 42
	// Each member arrives under the appendrel's column-translation Project,
	// as the planner builds it.
	wrap := func(sc *SeqScan) Node {
		return &Project{Child: sc, Targets: []Expr{jrCol(0), jrCol(1)}, schema: sc.Output()}
	}
	union := &SetOp{Op: parser.SetOpUnion, All: true, Left: wrap(p1), Right: wrap(p2)}
	if groupVarSourceNode(0, union, 0) != nil {
		t.Fatal("a partition expansion must not be taken as a subquery relation")
	}
}
