package optimizer

// C-02a gate: PG initsplan.c case table for the per-link delay test.
// Bit convention: leaf 0 = left input, leaf 1 = right input.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func delaySJI(jt parser.JoinType) *SpecialJoinInfo {
	return &SpecialJoinInfo{
		SynLefthand:  0b001,
		SynRighthand: 0b010,
		MinLefthand:  0b001,
		MinRighthand: 0b010,
		Jointype:     jt,
	}
}

func TestDelayedAboveOJ(t *testing.T) {
	tests := []struct {
		name  string
		jt    parser.JoinType
		qual  RelSet
		delay bool
	}{
		// LEFT: nullable side is right.
		{"left/preserved-only places", parser.JoinLeft, 0b001, false},
		{"left/nullable delays", parser.JoinLeft, 0b010, true},
		{"left/spanning delays", parser.JoinLeft, 0b011, true},
		{"left/constant places", parser.JoinLeft, 0, false},
		{"left/outside places", parser.JoinLeft, 0b100, false},
		// Strictness does NOT exempt: `nullable.x = 1` is strict and still
		// delays (demotion already ran; this link survived it).
		{"left/strict-on-nullable delays", parser.JoinLeft, 0b010, true},
		// RIGHT: mirror image.
		{"right/preserved-only places", parser.JoinRight, 0b010, false},
		{"right/nullable delays", parser.JoinRight, 0b001, true},
		{"right/spanning delays", parser.JoinRight, 0b011, true},
		{"right/constant places", parser.JoinRight, 0, false},
		// FULL: both sides nullable.
		{"full/left delays", parser.JoinFull, 0b001, true},
		{"full/right delays", parser.JoinFull, 0b010, true},
		{"full/constant places", parser.JoinFull, 0, false},
		{"full/outside places", parser.JoinFull, 0b100, false},
		// INNER/CROSS never delay.
		{"inner/spanning places", parser.JoinInner, 0b011, false},
		{"cross/spanning places", parser.JoinCross, 0b011, false},
		// SEMI/ANTI fail closed (no caller descends into them).
		{"semi delays", parser.JoinSemi, 0b001, true},
		{"anti delays", parser.JoinAnti, 0b010, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := delayedAboveOJ(tc.qual, delaySJI(tc.jt)); got != tc.delay {
				t.Errorf("delayedAboveOJ(%03b, %v) = %v, want %v",
					tc.qual, tc.jt, got, tc.delay)
			}
		})
	}
}

func TestDelayedAboveOJNilDelays(t *testing.T) {
	if !delayedAboveOJ(0, nil) {
		t.Error("nil sj must delay (fail-closed), even for an empty qual")
	}
	if !delayedAboveOJ(0b001, nil) {
		t.Error("nil sj must delay (fail-closed)")
	}
}

// --- C-02b: plan-local attribution + consumption -----------------------

func srcCol(name string, src int16) SchemaColumn {
	return SchemaColumn{Name: name, Type: catalog.Type{Name: "int4"}, SourceTableIdx: src}
}

func srcScan(table string, cols ...SchemaColumn) *SeqScan {
	return &SeqScan{Table: &catalog.Table{Name: table}, schema: cols}
}

// srcEq builds `<col at merged idx named name from src> = <lit>`.
func srcEq(idx int, name string, src int16, lit int64) Expr {
	return &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}, SourceTableIdx: src},
		Right: &IntegerConst{Value: lit},
	}
}

func srcJoin(typ JoinType, left, right Node) *Join {
	return &Join{
		Type:   typ,
		Algo:   JoinAlgoHash,
		Left:   left,
		Right:  right,
		schema: append(append(Schema{}, left.Output()...), right.Output()...),
	}
}

func TestOutputRelSet(t *testing.T) {
	got, ok := outputRelSet(Schema{srcCol("x", 1), srcCol("y", 3)})
	if !ok || got != 0b101 {
		t.Errorf("outputRelSet = %03b,%v want 101,true", got, ok)
	}
	if _, ok := outputRelSet(Schema{srcCol("x", 1), srcCol("y", 0)}); ok {
		t.Error("unknown (0) identity must fail closed")
	}
	if _, ok := outputRelSet(Schema{srcCol("x", 33)}); ok {
		t.Error("out-of-range identity must fail closed")
	}
	if _, ok := outputRelSet(nil); ok {
		t.Error("empty schema must fail closed")
	}
}

func TestQualSrcRelSet(t *testing.T) {
	got, ok := qualSrcRelSet(srcEq(0, "x", 2, 1))
	if !ok || got != 0b010 {
		t.Errorf("qualSrcRelSet = %03b,%v want 010,true", got, ok)
	}
	// Constants contribute nothing; multi-ref unions.
	and := combineAnd([]Expr{srcEq(0, "x", 1, 1), srcEq(1, "y", 3, 2)})
	if got, ok := qualSrcRelSet(and); !ok || got != 0b101 {
		t.Errorf("union = %03b,%v want 101,true", got, ok)
	}
	// Outer refs, calls, and unknown identities fail closed (legacy
	// verdict kept) — never allow-by-default.
	if _, ok := qualSrcRelSet(&BinaryOp{Op: parser.OpEq,
		Left: &OuterColumnRef{Index: 0, Name: "x"}, Right: &IntegerConst{Value: 1}}); ok {
		t.Error("outer ref must fail closed")
	}
	call := &BinaryOp{Op: parser.OpEq,
		Left:  &FuncCall{Name: "abs", Args: []Expr{&ColumnRef{Index: 0, Name: "x", SourceTableIdx: 1}}},
		Right: &IntegerConst{Value: 1}}
	if _, ok := qualSrcRelSet(call); ok {
		t.Error("FuncCall must fail closed (volatility: the pass declines these)")
	}
	if _, ok := qualSrcRelSet(srcEq(0, "x", 0, 1)); ok {
		t.Error("unknown identity must fail closed")
	}
}

// NOTE (review): the pass-level tests below pin LEGACY verdicts, not the
// delay proof — wiring delayedAboveOJ into the copy pass is vacuous
// (legacy side gates already decline every nullable-side qual), so these
// pass with or without consumption. They are parity pins for the C-02c/d
// moves: the verdicts the moves must preserve, with attribution complete
// (srcIdx-carrying schemas) so the proof is computable at each site.

func TestDelayDeclinesNullableSideCopy(t *testing.T) {
	// a(1) LEFT JOIN b(2): a copy naming b must not descend;
	// a copy naming only a descends as before.
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeLeft, left, right)

	// Merged layout: [a.x=0, b.y=1].
	if _, ok := pushConjunctIntoSubtree(j, srcEq(1, "y", 2, 7)); ok {
		t.Error("nullable-side copy must decline (delay)")
	}
	if _, hasFilter := j.Right.(*Filter); hasFilter {
		t.Error("declined descent must leave the tree untouched")
	}
	repl, ok := pushConjunctIntoSubtree(j, srcEq(0, "x", 1, 7))
	if !ok {
		t.Fatal("preserved-side copy must still descend (legacy parity)")
	}
	nj := repl.(*Join)
	if _, hasFilter := nj.Left.(*Filter); !hasFilter {
		t.Error("preserved-side copy must land on the left input")
	}
}

func TestDelayNestedTopDecline(t *testing.T) {
	// Top LEFT(a, X) with X = LEFT(b, c). A conjunct naming b (nullable
	// via the lower link) declines; the residual is intact either way.
	// Parity pin: legacy descends-then-declines at the lower link; the
	// C-02c/d moves must reproduce this verdict via the conjunctive
	// delay proof instead.
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	c := srcScan("c", srcCol("z", 3))
	inner := srcJoin(JoinTypeLeft, b, c)
	top := srcJoin(JoinTypeLeft, a, inner)

	// Merged layout: [a.x=0, b.y=1, c.z=2].
	if _, ok := pushConjunctIntoSubtree(top, srcEq(1, "y", 2, 7)); ok {
		t.Error("lower-nullable copy must decline at the top link")
	}
	if _, hasFilter := inner.Left.(*Filter); hasFilter {
		t.Error("declined descent must not plant partial copies")
	}
	// Conjunct naming only a still descends through both links.
	if _, ok := pushConjunctIntoSubtree(top, srcEq(0, "x", 1, 7)); !ok {
		t.Error("preserved-only copy must descend (legacy parity)")
	}
}

func TestDelaySkippedOnUnknownAttribution(t *testing.T) {
	// Schemas without identities (all existing unit fixtures look like
	// this): the delay proof is unavailable, the legacy verdict stands.
	left := &SeqScan{Table: &catalog.Table{Name: "a"}, schema: Schema{ijCol("x")}}
	right := &SeqScan{Table: &catalog.Table{Name: "b"}, schema: Schema{ijCol("y")}}
	j := srcJoin(JoinTypeInner, left, right)
	c := &BinaryOp{Op: parser.OpEq,
		Left: &ColumnRef{Index: 0, Name: "x", Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: 1}}
	if _, ok := pushConjunctIntoSubtree(j, c); !ok {
		t.Error("unknown attribution must keep the legacy copy (INNER)")
	}
}

func TestPlanToParserJoinTypeMapping(t *testing.T) {
	// C-02c lives or dies on this mapping: JoinTypeInner must map to
	// parser.JoinInner (delay=false), or every path would map to the
	// fail-closed default and proven would always be false.
	cases := map[JoinType]parser.JoinType{
		JoinTypeInner: parser.JoinInner,
		JoinTypeLeft:  parser.JoinLeft,
		JoinTypeRight: parser.JoinRight,
		JoinTypeFull:  parser.JoinFull,
		JoinTypeCross: parser.JoinCross,
		JoinTypeSemi:  parser.JoinSemi,
		JoinTypeAnti:  parser.JoinAnti,
	}
	for in, want := range cases {
		if got := planToParserJoinType(in); got != want {
			t.Errorf("planToParserJoinType(%v) = %v, want %v", in, got, want)
		}
	}
}
