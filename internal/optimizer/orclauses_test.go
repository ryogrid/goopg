package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// orcCol is column k (0 or 1) of relation r in the rfj fixture layout.
func orcCol(r, k int) *ColumnRef {
	names := []string{"a0", "a1", "b0", "b1", "c0", "c1"}
	return &ColumnRef{Name: names[r*rfjWidth+k], Index: r*rfjWidth + k, SourceTableIdx: int16(r)}
}

func orcCmp(op parser.OpCode, r, k int, v int64) Expr {
	return &BinaryOp{Op: op, Left: orcCol(r, k), Right: &IntegerConst{Value: v}}
}

func orcAnd(a, b Expr) Expr { return &BinaryOp{Op: parser.OpAnd, Left: a, Right: b} }
func orcOr(a, b Expr) Expr  { return &BinaryOp{Op: parser.OpOr, Left: a, Right: b} }

// TestExtractOrClauseFor pins the M0146-0005 slice-4 port of PG's
// extract_or_clause: an OR over relations a and b yields, per relation, the OR
// of each arm's relation-only sub-clauses — and nothing when an arm has none
// for that relation (TPC-H Q19's part/lineitem shape, in miniature).
func TestExtractOrClauseFor(t *testing.T) {
	spans := []leafSpan{{0, 2}, {2, 4}, {4, 6}}
	join := &BinaryOp{Op: parser.OpEq, Left: orcCol(0, 0), Right: orcCol(1, 0)}
	// (a.1 = 1 AND b.1 > 5 AND a0 = b0) OR (a.1 = 2 AND b.1 < 3 AND a0 = b0)
	or := orcOr(
		orcAnd(orcAnd(orcCmp(parser.OpEq, 0, 1, 1), orcCmp(parser.OpGt, 1, 1, 5)), join),
		orcAnd(orcAnd(orcCmp(parser.OpEq, 0, 1, 2), orcCmp(parser.OpLt, 1, 1, 3)), join),
	)
	for rel, want := range map[int]string{0: "a1 = 1 OR a1 = 2", 1: "b1 > 5 OR b1 < 3"} {
		got := extractOrClauseFor(or, rel, spans, nil)
		if got == nil {
			t.Fatalf("rel %d: no clause extracted", rel)
		}
		arms := flattenPlannerOr(got)
		if len(arms) != 2 {
			t.Fatalf("rel %d: %d arms, want 2 (%s)", rel, len(arms), want)
		}
		for _, arm := range arms {
			if rs, _ := relidsOfExpr(arm, spans); rs != RelSet(1)<<uint(rel) {
				t.Errorf("rel %d: arm %v reads relids %b", rel, arm, rs)
			}
		}
	}
	// Relation c appears in no arm: nothing to extract.
	if got := extractOrClauseFor(or, 2, spans, nil); got != nil {
		t.Errorf("rel c: extracted %v from an OR that never mentions it", got)
	}
	// An arm with no a-only clause kills the extraction for a.
	partial := orcOr(orcAnd(orcCmp(parser.OpEq, 0, 1, 1), join), orcAnd(orcCmp(parser.OpLt, 1, 1, 3), join))
	if got := extractOrClauseFor(partial, 0, spans, nil); got != nil {
		t.Errorf("rel a: extracted %v though the second arm has no a-only clause", got)
	}
	// A nested OR inside an arm is flattened into the result.
	nested := orcOr(
		orcAnd(orcOr(orcCmp(parser.OpEq, 0, 1, 1), orcCmp(parser.OpEq, 0, 1, 7)), join),
		orcAnd(orcCmp(parser.OpEq, 0, 1, 2), join),
	)
	if got := extractOrClauseFor(nested, 0, spans, nil); got == nil || len(flattenPlannerOr(got)) != 3 {
		t.Errorf("rel a: nested OR should flatten to 3 arms, got %v", got)
	}
}

// TestOrClauseSelDivisorCompensatesJoinSelectivity pins consider_new_or_clause's
// "hack cached selectivity so join size remains the same": the join OR
// clause's selectivity is divided by the derived restrictions' selectivity.
func TestOrClauseSelDivisorCompensatesJoinSelectivity(t *testing.T) {
	spans := []leafSpan{{0, 2}, {2, 4}}
	or := orcOr(
		orcAnd(orcCmp(parser.OpEq, 0, 1, 1), orcCmp(parser.OpGt, 1, 1, 5)),
		orcAnd(orcCmp(parser.OpEq, 0, 1, 2), orcCmp(parser.OpLt, 1, 1, 3)),
	)
	ri := buildRestrictInfos([]Expr{or}, 0, spans).all
	if len(ri) != 1 {
		t.Fatalf("want the OR as one join restrictInfo, got %d", len(ri))
	}
	plain := (&searchCtx{}).joinClauseSelectivity(ri[0])
	ri2 := buildRestrictInfos([]Expr{or}, 0, spans).all
	s := &searchCtx{orClauseSelDivisor: map[Expr]float64{or: 0.25}}
	got := s.joinClauseSelectivity(ri2[0])
	want := plain / 0.25
	if want > 1 {
		want = 1
	}
	if got != want {
		t.Errorf("compensated selectivity = %g, want %g (plain %g / 0.25)", got, want, plain)
	}
}
