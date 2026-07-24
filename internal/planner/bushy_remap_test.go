package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestBuildJoinFromDP_NonAscendingSubsetKeyRemap pins the bushy
// key-remapping correctness prerequisite for cost-driven join order
// (cost-model C4, IMPLEMENTATION-TODO Q8=0 finding).
//
// The DP composes a subset's plan as `leftSchema ++ rightSchema`
// where left/right are ARBITRARY subsets (enumerateSplits assigns
// a=sub, b=comp). So a subset {0,2} can be built as join(scan t2,
// scan t0) with the NON-ascending runtime schema [t2, t0] — t0's
// column sits at local position 1, not 0.
//
// When such a subset is later joined, buildJoinFromDP must resolve
// the join key to t0's ACTUAL position in the child plan (1), not
// the position an ascending-table-order assumption would give (0).
// The old remapKeyToSubset assumed ascending order, so it mapped
// t0's key to 0 — pointing the equality at t2's column, which
// matched nothing (TPC-H Q8 → 0 rows under cost-driven order).
func TestBuildJoinFromDP_NonAscendingSubsetKeyRemap(t *testing.T) {
	col := func(name string, srcIdx int16) SchemaColumn {
		return SchemaColumn{Name: name, Type: catalog.Type{Name: "int4"}, SourceTableIdx: srcIdx}
	}
	tbl := func(name string) *catalog.Table {
		return &catalog.Table{Name: name, Columns: []catalog.Column{{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 0}}}
	}
	t0, t1, t2 := tbl("t0"), tbl("t1"), tbl("t2")

	// Global (FROM-order) layout: t0.c=0, t1.c=1, t2.c=2; width 1 each.
	scan := func(tb *catalog.Table, name string, src int16) *SeqScan {
		return &SeqScan{Table: tb, schema: Schema{col(name, src)}}
	}
	scan0 := scan(t0, "t0c", 1)
	scan1 := scan(t1, "t1c", 2)
	scan2 := scan(t2, "t2c", 3)

	g := &joinGraph{
		nodes:     3,
		mask:      0b111,
		tables:    []*catalog.Table{t0, t1, t2},
		scans:     []Node{scan0, scan1, scan2},
		scanWidth: []int{1, 1, 1},
	}

	// leftPlan = subset {0,2} built NON-ascending as join(t2, t0):
	// runtime schema [t2.c, t0.c] — t0's column is at position 1.
	leftPlan := &Join{
		Type:   JoinTypeInner,
		Algo:   JoinAlgoHash,
		Left:   scan2,
		Right:  scan0,
		schema: Schema{col("t2c", 3), col("t0c", 1)},
	}

	// Edge t0.c = t1.c in global coords (t0.c=0, t1.c=1).
	edge := &joinEdge{
		leftTable:  0,
		rightTable: 1,
		leftKey:    &ColumnRef{Name: "c", Index: 0, Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		rightKey:   &ColumnRef{Name: "c", Index: 1, Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
		predicate:  &BinaryOp{Op: parser.OpEq},
	}

	// Compose {0,2} (left) with {1} (right). leftPlan's runtime schema
	// is [t2.c, t0.c], so its layout is {t2:0, t0:1}.
	leftLayout := map[int]int{2: 0, 0: 1}
	rightLayout := map[int]int{1: 0}
	j := buildJoinFromDP(leftPlan, scan1, 10, 10, 0b101, 0b010, leftLayout, rightLayout, edge, g)

	lk, ok := j.LeftKey.(*ColumnRef)
	if !ok {
		t.Fatalf("LeftKey is not a ColumnRef: %T", j.LeftKey)
	}
	// t0.c lives at position 1 in leftPlan's runtime schema [t2, t0].
	if lk.Index != 1 {
		t.Errorf("LeftKey.Index = %d, want 1 (t0.c's real position in the non-ascending [t2,t0] child schema); an ascending-order assumption wrongly yields 0, pointing the equality at t2's column", lk.Index)
	}

	// The RightKey (t1.c) is shifted by the left schema width (2):
	// position 2 in the merged [t2, t0, t1] schema.
	rk, ok := j.RightKey.(*ColumnRef)
	if !ok {
		t.Fatalf("RightKey is not a ColumnRef: %T", j.RightKey)
	}
	if rk.Index != 2 {
		t.Errorf("RightKey.Index = %d, want 2 (t1.c after +leftWidth shift)", rk.Index)
	}
}
