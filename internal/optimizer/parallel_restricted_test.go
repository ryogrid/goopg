package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005dh: the Gather post-pass keeps two PG parallel hazards out of the
// partial subtree.
//
//   - A correlated SubPlan in a base relation's qual. Its outer references are
//     PARAM_EXEC Params, so `subplan->parallel_safe` is false and the qual is
//     parallel-restricted; set_rel_consider_parallel then gives the relation
//     no partial path, and PG scans it serially (TPC-DS Q41's
//     `(SubPlan 1) > 0` on `item i1` ran in a Parallel Seq Scan).
//   - A CTE scan: "CTE tuplestores aren't shared among parallel workers, so
//     we force all CTE scans to happen in the leader" (allpaths.c, RTE_CTE).
//     TPC-DS Q2 hashed two CTE scans under a Gather Merge.
func TestGatherPostPassKeepsParallelHazardsSerial(t *testing.T) {
	tbl := bigTable(t, "pr_outer")
	inner := bigTable(t, "pr_inner")
	correlated := &BinaryOp{Op: parser.OpGt,
		Left: &SubqueryExpr{Plan: &Project{Child: &Filter{
			Child: seqScanOver(inner),
			Predicate: &BinaryOp{Op: parser.OpEq,
				Left:  &ColumnRef{Index: 0, Name: "a"},
				Right: &OuterColumnRef{Level: 1, Index: 0, Name: "a"}},
		}}},
		Right: &IntegerConst{Value: 0}}
	plain := &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: 0, Name: "a"}, Right: &IntegerConst{Value: 0}}

	cases := []struct {
		name       string
		root       Node
		wantGather bool
	}{
		{"plain qual still parallel", &Filter{Child: seqScanOver(tbl), Predicate: plain}, true},
		{"correlated SubPlan qual stays serial", &Filter{Child: seqScanOver(tbl), Predicate: correlated}, false},
		{"correlated SubPlan qual under an aggregate stays serial",
			&Aggregate{Child: &Filter{Child: seqScanOver(tbl), Predicate: correlated}}, false},
		{"hash join over a materialized CTE scan stays serial",
			&Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: seqScanOver(tbl),
				Right:     &MaterializedCTEScan{Name: "c", schema: Schema{{Name: "a"}}},
				Predicate: &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 1}}}, false},
		{"hash join over a plain scan still parallel",
			&Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: seqScanOver(tbl), Right: seqScanOver(inner),
				Predicate: &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 1}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MaybeAddGather(c.root, parallelTestSettings())
			if has := subtreeHasGather(got); has != c.wantGather {
				t.Errorf("Gather placed = %v, want %v", has, c.wantGather)
			}
		})
	}
}
