package optimizer

// C-10c (P4-00c) — outer-join qual-placement contract for upper rels.
//
// Design: docs/design/planner-c10c-upper-qual-placement/DESIGN.md.
// Scoping: analysis/planner-refactor-take3/c10-p400-scoping-20260905/README.md §3.
//
// WHY THIS FILE EXISTS. Phase 4 (C-11..C-18) replaces the upper-planner
// plan nodes with paths. Four of them (*Sort, *Limit, *Aggregate,
// *WindowAgg) are today walked by
// pushSingleSideQualsIntoInnerJoinInputs — the SOLE production consumer
// of the delayedAboveOJ oracle. When those arms go, the walk that
// reaches the oracle goes with them, and the first applying cut of an
// upper INPUT TARGET (C-15, at stampAggregateInputTarget) will be the
// first code in the tree that can narrow an upper rel's input across an
// outer-join link.
//
// The hazard is a WRONG ANSWER WITH GREEN ROW COUNTS. Narrowing an upper
// input target across a LEFT link, or pushing a qual below one, makes the
// layer under the link evaluate a NULL-extended row's expression on the
// BASE row: `sum(o.amount)`'s argument, `GROUP BY o.x + 1`, `o.amount >
// 10` all become "the value this row would have had if it had matched".
// PG's guard for the target half is the PlaceHolderVar; goopg has NO
// placeholder machinery (specialjoin.go:109 states it), so goopg's only
// guard is "do not evaluate below the link", decided by delayedAboveOJ.
// No row count changes, so the TPC-H anchors, the TPC-DS SF0.5 gate, the
// pgbench smoke and the plan gate all stay green through the bug.
//
// The five tests below are the red-then-green fixture C-10c owes:
// the oracle arm (guard-sensitive), the C-15 inertness tripwire, a
// non-vacuity control, and the two consumer arms driven through the
// *Aggregate arm that C-15 deletes.
//
// Helpers srcCol / srcScan / srcJoin / srcEq come from
// outerjoin_delay_test.go and srcGt from inner_join_qual_pushdown_test.go
// (same package, deliberately reused so the fixture speaks the C-02
// vocabulary).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

const c10cDesignDoc = "docs/design/planner-c10c-upper-qual-placement/DESIGN.md"

// c10cRef builds a ColumnRef in the Aggregate's INPUT coordinate space
// (positions into agg.Child.Output()), carrying the source identity the
// delay proof speaks.
func c10cRef(idx int, name string, src int16) *ColumnRef {
	return &ColumnRef{
		Index:          idx,
		Name:           name,
		Type:           catalog.Type{Name: "int4"},
		SourceTableIdx: src,
	}
}

// c10cShape builds the fixture plan:
//
//	Aggregate[ GROUP BY c.name, sum(o.amount) ]   <- upper target reads the NULLABLE side
//	  Filter[ o.amount > 10 ]                     <- residual qual on the NULLABLE side
//	    Join LEFT                                 <- the link
//	      SeqScan c  srcIdx 1 -> [id, name]       (preserved)
//	      SeqScan o  srcIdx 2 -> [cust, amount]   (nullable)
//
// merged input layout: [0 c.id, 1 c.name, 2 o.cust, 3 o.amount].
//
// aggArgSrc selects which side the aggregate argument reads, so the
// non-vacuity control can build the preserved-only variant from the same
// builder rather than from a second, differently-shaped tree.
func c10cShape(aggArg *ColumnRef) (*Aggregate, *Filter, *Join) {
	left := srcScan("c", srcCol("id", 1), srcCol("name", 1))
	right := srcScan("o", srcCol("cust", 2), srcCol("amount", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	// Nullable-side residual: `o.amount > 10` at merged index 3.
	resid := &Filter{Child: j, Predicate: srcGt(3, "amount", 2, 10)}
	agg := &Aggregate{
		Child:      resid,
		GroupExprs: []Expr{c10cRef(1, "name", 1)},
		schema: Schema{
			srcCol("name", 1),
			{Name: "sum", Type: catalog.Type{Name: "int8"}, SourceTableIdx: 1},
		},
	}
	if aggArg != nil {
		agg.Aggs = []AggregateCall{{Name: "sum", Arg: aggArg, SharedStateSlot: -1}}
	} else {
		agg.Aggs = []AggregateCall{{Name: "count", Star: true, SharedStateSlot: -1}}
	}
	return agg, resid, j
}

// c10cColRelSet returns the source-identity relset of one input column.
func c10cColRelSet(t *testing.T, in Schema, pos int) RelSet {
	t.Helper()
	if pos < 0 || pos >= len(in) {
		t.Fatalf("input position %d out of range for a %d-column row", pos, len(in))
	}
	src := in[pos].SourceTableIdx
	if src <= 0 || int(src) > maxSearchRels {
		t.Fatalf("input column %q carries no usable identity (%d); the fixture "+
			"must be fully attributed or the oracle is never consulted", in[pos].Name, src)
	}
	return 1 << (src - 1)
}

// TestC10cOracleRefusesNullableSideUpperInput is the ORACLE arm.
//
// It asks delayedAboveOJ the exact question C-15's applying cut will have
// to ask, at the exact link, for every expression the Aggregate reads
// from its input row plus the residual qual. It is the guard-sensitive
// arm: inverting delayedAboveOJ's LEFT case (nullable side read as
// SynLefthand) flips both verdicts and this test goes red.
func TestC10cOracleRefusesNullableSideUpperInput(t *testing.T) {
	agg, resid, j := c10cShape(c10cRef(3, "amount", 2))

	sj, ok := planJoinDelaySJI(j)
	if !ok {
		t.Fatal("planJoinDelaySJI declined a fully-attributed LEFT link: " +
			"the fixture is not exercising the oracle at all")
	}

	// The aggregate ARGUMENT reads the nullable side. Evaluating it below
	// the link computes sum() over base rows that the link would have
	// null-extended.
	argRel, ok := qualSrcRelSet(agg.Aggs[0].Arg)
	if !ok {
		t.Fatal("qualSrcRelSet declined the aggregate argument; the fixture " +
			"must be attributable or the proof is vacuous")
	}
	if !delayedAboveOJ(argRel, sj) {
		t.Errorf("delayedAboveOJ(%04b, LEFT) = false for the aggregate argument "+
			"o.amount, which reads the NULLABLE side: an upper input target "+
			"carrying it MUST NOT be evaluated below this link (see %s §4)",
			argRel, c10cDesignDoc)
	}

	// The GROUP key reads the preserved side only: permitted.
	keyRel, ok := qualSrcRelSet(agg.GroupExprs[0])
	if !ok {
		t.Fatal("qualSrcRelSet declined the group key")
	}
	if delayedAboveOJ(keyRel, sj) {
		t.Errorf("delayedAboveOJ(%04b, LEFT) = true for the group key c.name, "+
			"which reads the PRESERVED side only: refusing it costs the "+
			"narrowing for no safety (see %s §5, C-15 row)", keyRel, c10cDesignDoc)
	}

	// The residual qual — the C-02 half of the same contract.
	qRel, ok := qualSrcRelSet(resid.Predicate)
	if !ok {
		t.Fatal("qualSrcRelSet declined the residual predicate")
	}
	if !delayedAboveOJ(qRel, sj) {
		t.Errorf("delayedAboveOJ(%04b, LEFT) = false for the residual qual "+
			"`o.amount > 10` on the NULLABLE side: pushing it below the link "+
			"tests NULL-extended rows as base rows", qRel)
	}
}

// TestC10cAggregateInputTargetNarrowingCrossesLeftLink is the C-15
// TRIPWIRE.
//
// stampAggregateInputTarget is COMPUTE-ONLY today (group_input_target.go
// header: "no Project insertion, no schema change, no cost change"). This
// test pins two things at once:
//
//  1. the keep it derives for this shape is a REAL narrowing that reaches
//     the nullable side — so the shape is one where an applying cut would
//     be wrong, not a shape where the question is moot; and
//  2. the stamp left the tree bit-for-bit alone.
//
// When C-15 lands its applying cut at this function without consulting
// delayedAboveOJ, (2) breaks and this test goes red.
func TestC10cAggregateInputTargetNarrowingCrossesLeftLink(t *testing.T) {
	agg, resid, j := c10cShape(c10cRef(3, "amount", 2))

	sj, ok := planJoinDelaySJI(j)
	if !ok {
		t.Fatal("planJoinDelaySJI declined a fully-attributed LEFT link")
	}

	// Snapshot the shape BEFORE the stamp.
	beforeChild := agg.Child
	beforeWidth := len(agg.Child.Output())
	beforeLeft := len(j.Left.Output())
	beforeRight := len(j.Right.Output())
	beforePred := resid.Predicate

	stampAggregateInputTarget(agg, nil)

	// (1a) the stamp is known and is a strict narrowing.
	if !agg.InputTargetKnown {
		t.Fatal("the fixture's Aggregate must produce a KNOWN input target, " +
			"or the tripwire never arms (an unknown stamp declines every cut)")
	}
	keep := agg.InputTarget
	if !gitEqualInts(keep, []int{1, 3}) {
		t.Fatalf("input target keep = %v, want [1 3] (c.name, o.amount); "+
			"the fixture's derivation moved and the tripwire below is "+
			"no longer describing the shape it claims", keep)
	}
	if len(keep) >= beforeWidth {
		t.Fatalf("keep %v is not a narrowing of a %d-column input: an applying "+
			"cut would have nothing to do here and the tripwire is vacuous",
			keep, beforeWidth)
	}

	// (1b) the keep reaches the nullable side, column by column. Per
	// column rather than as a union, because the union of a preserved and
	// a nullable column delays under EITHER orientation of the LEFT case
	// — a union-only assertion would survive an inverted guard.
	in := beforeChild.Output()
	if got := delayedAboveOJ(c10cColRelSet(t, in, 1), sj); got {
		t.Errorf("kept column 1 (%s, PRESERVED) reports delay=%v, want false",
			in[1].Name, got)
	}
	if got := delayedAboveOJ(c10cColRelSet(t, in, 3), sj); !got {
		t.Errorf("kept column 3 (%s, NULLABLE) reports delay=%v, want true: "+
			"C-15's applying cut must refuse to push this narrowing below "+
			"the LEFT link (see %s §5, C-15 row)", in[3].Name, got, c10cDesignDoc)
	}

	// (2) THE TRIPWIRE. The stamp must remain inert on this shape.
	if agg.Child != beforeChild {
		t.Errorf("stampAggregateInputTarget REPLACED the Aggregate's input "+
			"(%T) on a shape whose input target reaches the NULLABLE side of "+
			"a LEFT join. If this is C-15's applying cut: it must consult "+
			"delayedAboveOJ over the crossed links before narrowing, and "+
			"refuse here. See %s §4-§5.", agg.Child, c10cDesignDoc)
	}
	if got := len(agg.Child.Output()); got != beforeWidth {
		t.Errorf("Aggregate input width %d -> %d: an upper input target was "+
			"narrowed across a LEFT link without a delay proof. See %s §4.",
			beforeWidth, got, c10cDesignDoc)
	}
	if gotL, gotR := len(j.Left.Output()), len(j.Right.Output()); gotL != beforeLeft || gotR != beforeRight {
		t.Errorf("LEFT join input widths (%d,%d) -> (%d,%d): the narrowing was "+
			"pushed BELOW the link, which is the wrong-answer case (a "+
			"NULL-extended row's expression evaluated on the base row). See %s §4.",
			beforeLeft, beforeRight, gotL, gotR, c10cDesignDoc)
	}
	if resid.Predicate != beforePred {
		t.Errorf("the residual predicate was rewritten by an input-target stamp; "+
			"qual placement is C-02's contract, not the target's. See %s §5.",
			c10cDesignDoc)
	}
}

// TestC10cPreservedOnlyUpperTargetIsPermitted is the NON-VACUITY control.
//
// Same shape, but the Aggregate reads only the preserved side
// (count(*) instead of sum(o.amount)). The keep shrinks to the group key
// alone and the oracle PERMITS the narrowing — so the refusal in the test
// above is derived from the shape, not a constant.
func TestC10cPreservedOnlyUpperTargetIsPermitted(t *testing.T) {
	agg, _, j := c10cShape(nil)

	sj, ok := planJoinDelaySJI(j)
	if !ok {
		t.Fatal("planJoinDelaySJI declined a fully-attributed LEFT link")
	}

	stampAggregateInputTarget(agg, nil)
	if !agg.InputTargetKnown {
		t.Fatal("count(*) over an enumerable group key must produce a known stamp")
	}
	if !gitEqualInts(agg.InputTarget, []int{1}) {
		t.Fatalf("input target keep = %v, want [1] (c.name only)", agg.InputTarget)
	}

	in := agg.Child.Output()
	for _, pos := range agg.InputTarget {
		if delayedAboveOJ(c10cColRelSet(t, in, pos), sj) {
			t.Errorf("kept column %d (%s) reports delay for a target that reads "+
				"the PRESERVED side only: the contract would refuse every "+
				"narrowing and buy nothing", pos, in[pos].Name)
		}
	}
}

// TestC10cNullableSideQualNeverDescendsThroughAggregateArm drives the
// CONSUMER through the *Aggregate arm
// (inner_join_qual_pushdown.go:123-125) that C-15 deletes: the residual
// qual sits under an Aggregate, so the pass only reaches it by descending
// that arm.
//
// Honest note (inherited from C-02b's own review): this verdict is
// OVER-DETERMINED — joinRestrictionSides declines the nullable side
// before delayedAboveOJ is consulted at all — so this test pins
// PLACEMENT, not the guard. Its value is that it fails if the *Aggregate
// arm is deleted without a replacement that still refuses.
func TestC10cNullableSideQualNeverDescendsThroughAggregateArm(t *testing.T) {
	agg, resid, j := c10cShape(c10cRef(3, "amount", 2))
	beforePred := resid.Predicate

	got := pushSingleSideQualsIntoInnerJoinInputs(agg)

	root, ok := got.(*Aggregate)
	if !ok {
		t.Fatalf("pass returned %T, want the *Aggregate root back", got)
	}
	child, ok := root.Child.(*Filter)
	if !ok {
		t.Fatalf("Aggregate.Child is %T, want the residual *Filter kept: a "+
			"nullable-side qual must never be moved below the LEFT link", root.Child)
	}
	if child.Predicate != beforePred {
		t.Error("residual predicate rewritten; the nullable-side conjunct must " +
			"stay exactly where it was")
	}
	if _, planted := j.Right.(*Filter); planted {
		t.Errorf("a Filter was planted on the NULLABLE input of a LEFT join: "+
			"`o.amount > 10` evaluated there deletes rows the link would have "+
			"NULL-extended and kept. See %s §4.", c10cDesignDoc)
	}
	if _, planted := j.Left.(*Filter); planted {
		t.Errorf("a Filter was planted on the PRESERVED input for a qual that " +
			"does not read it (attribution bug)")
	}
}

// TestC10cPreservedSideQualMovesThroughAggregateArm is the
// GUARD-SENSITIVE consumer arm.
//
// A preserved-side qual above the same LEFT link must MOVE (C-02d): the
// residual Filter is spliced out and the conjunct lands on the preserved
// input. That move requires a POSITIVE delayedAboveOJ verdict — a broken
// guard degrades it to a copy and the residual survives, which this test
// catches. It is the mirror of the oracle arm: the first test fails when
// the guard stops refusing, this one fails when it stops permitting.
func TestC10cPreservedSideQualMovesThroughAggregateArm(t *testing.T) {
	left := srcScan("c", srcCol("id", 1), srcCol("name", 1))
	right := srcScan("o", srcCol("cust", 2), srcCol("amount", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	// `c.id > 7` — preserved side, non-equi so the EC arm never seeds a
	// sibling copy (a planted derivation would veto the move for an
	// unrelated reason and make this test lie).
	resid := &Filter{Child: j, Predicate: srcGt(0, "id", 1, 7)}
	agg := &Aggregate{
		Child:      resid,
		GroupExprs: []Expr{c10cRef(1, "name", 1)},
		Aggs:       []AggregateCall{{Name: "count", Star: true, SharedStateSlot: -1}},
		schema: Schema{
			srcCol("name", 1),
			{Name: "count", Type: catalog.Type{Name: "int8"}, SourceTableIdx: 1},
		},
	}

	got := pushSingleSideQualsIntoInnerJoinInputs(agg)

	root, ok := got.(*Aggregate)
	if !ok {
		t.Fatalf("pass returned %T, want the *Aggregate root back", got)
	}
	if _, stillFiltered := root.Child.(*Filter); stillFiltered {
		t.Fatalf("the residual Filter survived a PRESERVED-side qual over a "+
			"LEFT link: the C-02d move needs delayedAboveOJ to PERMIT the "+
			"descent, so this is what a broken/over-refusing guard looks "+
			"like. See %s §2 (demotion already ran; the guard is the only "+
			"remaining lever) and §6.", c10cDesignDoc)
	}
	nj, ok := root.Child.(*Join)
	if !ok {
		t.Fatalf("Aggregate.Child is %T, want the *Join (residual spliced)", root.Child)
	}
	if _, placed := nj.Left.(*Filter); !placed {
		t.Errorf("Join.Left is %T, want the placed *Filter: a preserved-side "+
			"restriction belongs on the preserved input (PG's "+
			"distribute_restrictinfo_to_rels)", nj.Left)
	}
	if _, planted := nj.Right.(*Filter); planted {
		t.Errorf("a Filter reached the NULLABLE input for a preserved-side qual")
	}
}
