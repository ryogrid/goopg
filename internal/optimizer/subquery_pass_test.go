package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// R33 (K44) walker-coverage pin: every plan-node host that
// NodeSubplans models must also be visited by WalkPlanExprs, or
// sublink discovery built on WalkPlanExprs silently misses the
// sublink (placement) and planHasOuterRef-adjacent checks misread
// IsNonCorrelated. The three R33 arms (Result, IndexScan.Cond,
// IndexOnlyScan) are covered alongside pre-existing hosts so the
// two enumerations cannot drift apart again.
func TestWalkPlanExprsHostsSublinks(t *testing.T) {
	inner := &SeqScan{}
	sq := func() *SubqueryExpr { return &SubqueryExpr{Plan: inner, IsNonCorrelated: true} }
	hosts := map[string]Node{
		"Project.Targets":       &Project{Targets: []Expr{sq()}},
		"Result.Targets":        &Result{Targets: []Expr{sq()}},
		"Result.OneTimeFilter":  &Result{OneTimeFilter: sq()},
		"Filter.Predicate":      &Filter{Predicate: sq()},
		"IndexScan.Cond":        &IndexScan{Cond: sq()},
		"IndexOnlyScan.Cond":    &IndexOnlyScan{Cond: sq()},
		"Sort.Key":              &Sort{Keys: []SortKey{{Expr: sq()}}},
		"Aggregate.GroupExprs":  &Aggregate{GroupExprs: []Expr{sq()}},
		"Join.Predicate":        &Join{Predicate: sq()},
		"Limit.Limit":           &Limit{Limit: sq()},
		"WindowAgg.PartitionBy": &WindowAgg{PartitionBy: []Expr{sq()}},
	}
	for name, host := range hosts {
		// Fixture validity: the complete enumerator must see it.
		foundComplete := false
		for _, p := range NodeSubplans(host) {
			if p == Node(inner) {
				foundComplete = true
			}
		}
		if !foundComplete {
			t.Fatalf("%s: NodeSubplans does not model this host — fixture invalid", name)
		}
		// The pinned property: WalkPlanExprs-driven discovery sees it too.
		foundWalk := false
		WalkPlanExprs(host, func(e Expr) {
			if sub, ok := e.(*SubqueryExpr); ok && sub.Plan == Node(inner) {
				foundWalk = true
			}
		})
		if !foundWalk {
			t.Errorf("%s: WalkPlanExprs misses sublink hosted here", name)
		}
	}
}

// R33 (K44) functional pins for the post-pass sublink recursion.
// Fixtures mirror Q9: a serial top plan carrying uncorrelated scalar
// sublinks whose bodies are single-relation aggregates.

// sublinkTestSettings sizes only agg_sized_t (sizedAggFixture's
// table) past the gate; every other table stays serial.
func sublinkTestSettings() ParallelSettings {
	s := parallelTestSettingsBlocks(10)
	s.BlocksForTable = func(tbl *catalog.Table) (int64, bool) {
		if tbl != nil && tbl.Name == "agg_sized_t" {
			return 4096, true
		}
		return 10, true
	}
	return s
}

// sublinkAggTop builds Project over a small serial scan carrying one
// scalar sublink over inner.
func sublinkAggTop(t *testing.T, inner Node) (*Project, *SubqueryExpr) {
	t.Helper()
	small := bigTable(t, "small")
	top := &Project{
		Child:   seqScanOver(small),
		Targets: []Expr{&ColumnRef{Index: 0, Name: "a"}},
	}
	sq := &SubqueryExpr{Plan: inner, IsNonCorrelated: true}
	top.Targets = append(top.Targets, sq)
	return top, sq
}

// innerSplitShape asserts the Finalize -> Gather -> Partial shape the
// post-pass builds, returning the Partial node.
func innerSplitShape(t *testing.T, plan Node) *Aggregate {
	t.Helper()
	final, ok := plan.(*Aggregate)
	if !ok {
		t.Fatalf("inner root is %T, want split *Aggregate", plan)
	}
	if final.Mode != AggModeFinal {
		t.Fatalf("inner root mode is %v, want AggModeFinal", final.Mode)
	}
	g, ok := final.Child.(*Gather)
	if !ok {
		t.Fatalf("inner Finalize child is %T, want *Gather", final.Child)
	}
	partial, ok := g.Child.(*Aggregate)
	if !ok || partial.Mode != AggModePartial {
		t.Fatalf("inner Gather child is %T, want Partial *Aggregate", g.Child)
	}
	return partial
}

func TestSublinkParallelPassSplitsUncorrelated(t *testing.T) {
	inner := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	top, sq := sublinkAggTop(t, inner)
	out := MaybeAddGather(top, sublinkTestSettings())
	proj, ok := out.(*Project)
	if !ok {
		t.Fatalf("root is %T, want *Project", out)
	}
	var got *SubqueryExpr
	WalkPlanExprs(proj, func(e Expr) {
		if s, ok := e.(*SubqueryExpr); ok && s != sq {
			got = s
		}
	})
	if got == nil {
		t.Fatal("sublink missing from grafted tree")
	}
	innerSplitShape(t, got.Plan)
	// The shared original is untouched (post-cache non-mutation).
	if sq.Plan != Node(inner) {
		t.Error("post-pass mutated the cached sublink Plan")
	}
}

func TestSublinkParallelPassSkipsCorrelated(t *testing.T) {
	inner := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	top, sq := sublinkAggTop(t, inner)
	sq.IsNonCorrelated = false
	if out := MaybeAddGather(top, sublinkTestSettings()); out != Node(top) {
		t.Error("correlated sublink must stay serial with pointer identity")
	}
}

func TestSublinkParallelPassSkipsArgsBound(t *testing.T) {
	inner := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	top, sq := sublinkAggTop(t, inner)
	sq.Args = []Expr{&BooleanConst{Value: true}}
	sq.ParParam = []int{0}
	if out := MaybeAddGather(top, sublinkTestSettings()); out != Node(top) {
		t.Error("Args-bound sublink must stay serial with pointer identity")
	}
}

func TestSublinkParallelPassSkipsUnsafeInterior(t *testing.T) {
	// Temp table inside the sublink: the deep safety scan must refuse
	// even though the size gate clears (the design's risk (c) probe).
	catTbl := bigTable(t, "tempbig")
	catTbl.Temp = true
	inner := &Aggregate{Child: seqScanOver(catTbl)}
	top, _ := sublinkAggTop(t, inner)
	s := sublinkTestSettings()
	s.BlocksForTable = func(tbl *catalog.Table) (int64, bool) {
		if tbl != nil && tbl.Name == "tempbig" {
			return 1 << 20, true
		}
		return 10, true
	}
	if out := MaybeAddGather(top, s); out != Node(top) {
		t.Error("temp table inside sublink must stay serial with pointer identity")
	}
}

func TestSublinkParallelPassSkipsOperandNested(t *testing.T) {
	// A sublink nested in an IN operand is parent-scope-row-adjacent:
	// placement discovery must not reach it, while the outer IN
	// sublink itself is still grafted.
	nested := &SubqueryExpr{Plan: sizedAggFixture(t, 5_900_000, 2, 8, 2), IsNonCorrelated: true}
	outerPlan := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	in := &InExpr{Operand: nested, Plan: outerPlan, IsNonCorrelated: true}
	top := &Project{Child: seqScanOver(bigTable(t, "small")), Targets: []Expr{in}}
	out := MaybeAddGather(top, sublinkTestSettings())
	proj, ok := out.(*Project)
	if !ok {
		t.Fatalf("root is %T, want *Project", out)
	}
	gotIn, ok := proj.Targets[0].(*InExpr)
	if !ok {
		t.Fatalf("target is %T, want *InExpr", proj.Targets[0])
	}
	// Outer IN plan grafted...
	innerSplitShape(t, gotIn.Plan)
	// ...nested operand sublink untouched.
	stillNested, ok := gotIn.Operand.(*SubqueryExpr)
	if !ok {
		t.Fatalf("operand is %T, want untouched *SubqueryExpr", gotIn.Operand)
	}
	if planHasGather(stillNested.Plan) {
		t.Error("operand-nested sublink gained a Gather — placement must not reach it")
	}
}

// TestSublinkParallelPassSkipsNonAggregateRoot pins the
// priced-verdicts-only gate: a big plain-scan sublink (the Q6/Q14
// over-admission shape) stays serial — only Aggregate-rooted Plans
// with a split outcome are grafted.
func TestSublinkParallelPassSkipsNonAggregateRoot(t *testing.T) {
	inner := &Project{Child: seqScanOver(bigTable(t, "bigroot"))}
	top, _ := sublinkAggTop(t, inner)
	s := sublinkTestSettings()
	s.BlocksForTable = func(tbl *catalog.Table) (int64, bool) {
		if tbl != nil && (tbl.Name == "agg_sized_t" || tbl.Name == "bigroot") {
			return 4096, true
		}
		return 10, true
	}
	if out := MaybeAddGather(top, s); out != Node(top) {
		t.Error("non-Aggregate-rooted sublink must stay serial with pointer identity")
	}
}

func TestSublinkParallelPassSkipsUnderGather(t *testing.T) {
	// Top places a Gather and carries a sublink: the nesting rule
	// keeps the nested plan serial (no N x N workers).
	inner := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	aggTop := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	sq := &SubqueryExpr{Plan: inner, IsNonCorrelated: true}
	aggTop.Passthrough = []Expr{sq}
	out := MaybeAddGather(aggTop, sublinkTestSettings())
	if !planHasGather(out) {
		t.Fatal("top aggregate should have placed a Gather (test vacuous otherwise)")
	}
	var got *SubqueryExpr
	WalkPlanExprs(out, func(e Expr) {
		if s, ok := e.(*SubqueryExpr); ok {
			got = s
		}
	})
	if got == nil {
		t.Fatal("sublink missing from grafted tree")
	}
	if planHasGather(got.Plan) {
		t.Error("sublink under a top Gather gained its own — the nesting rule forbids it")
	}
}
