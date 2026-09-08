package optimizer

// The `*NestedLoopIndexJoin` arm of `resolveBaseColumn`
// (docs/design/planner-rowest-collapse/DESIGN.md §3).
//
// `*NestedLoopIndexJoin` is not a `*Join` — it is its own node type carrying
// `Outer`/`Inner` — and the resolver had no arm for it for the whole life of
// the node. Every column read above an NLI therefore resolved to NOTHING, and
// the visible consequence was not a join-size miss but an AGGREGATE one:
// `examineGroupVar` priced each grouping variable at `defaultNumDistinct`, the
// independence product saturated `estimateNumGroups`'s closing
// `numdistinct > input_rows` clamp, and the aggregate's output estimate came
// back as its own INPUT row count. TPC-DS Q99 read 720657 against an actual
// 90; the same query without its `ORDER BY … LIMIT` — which is what put the
// NLI in the plan — read 90 exactly.
//
// The numbers in TestNumGroupsThroughNestedLoopIndexJoin are Q99's, so a
// regression reads as the shape it actually took.

import (
	"math"
	"testing"
)

// mergedNLI wires a NestedLoopIndexJoin the way createNestLoopPlan does:
// schema = outer‖inner, which is the coordinate space its Predicate and the
// nodes above it are written in. It is `mergedJoin`'s twin, and the fact that
// the two node types need two fixtures is the same fact that let their
// resolver arms diverge.
func mergedNLI(t JoinType, outer, inner Node) *NestedLoopIndexJoin {
	n := &NestedLoopIndexJoin{Type: t, Outer: outer, Inner: inner}
	n.schema = append(append(Schema(nil), outer.Output()...), inner.Output()...)
	return n
}

// nliInnerScan is the parameterised inner probe: an *IndexScan over a
// stats-bearing table, with the schema the planner gives it (the table's own
// column order, same as a *SeqScan's).
func nliInnerScan(name string, rows int64, ndistinct ...int64) *IndexScan {
	s := numGroupsScan(name, rows, ndistinct...)
	return &IndexScan{Table: s.Table, schema: s.Output(), Key: jrCol(0)}
}

func TestResolveBaseColumnThroughNestedLoopIndexJoin(t *testing.T) {
	outer := numGroupsScan("dims", 720657, 4, 6)
	inner := nliInnerScan("date_dim", 73049, 3)
	nli := mergedNLI(JoinTypeInner, outer, inner)

	// Outer side: coordinates pass through unshifted.
	if got, want := columnNDistinctForChild(0, nli), int64(4); got != want {
		t.Fatalf("ndistinct of outer col 0 through an NLI = %d, want %d", got, want)
	}
	if st := columnStatsForChild(1, nli); st == nil || st.NDistinct != 6 {
		t.Fatalf("stats of outer col 1 through an NLI did not resolve: %+v", st)
	}

	// Inner side: the merged coordinate is shifted down by the outer's width,
	// exactly as the `*Join` arm shifts by the left's. Reading nd from one
	// column and the MCV list of another is the P5.6-e-ii defect class, so
	// both family members are asserted on the same coordinate.
	ow := len(outer.Output())
	if got, want := columnNDistinctForChild(ow, nli), int64(3); got != want {
		t.Fatalf("ndistinct of inner col 0 through an NLI = %d, want %d", got, want)
	}
	if st := columnStatsForChild(ow, nli); st == nil || st.NDistinct != 3 {
		t.Fatalf("stats/ndistinct siblings disagree through an NLI: %+v", st)
	}

	// The raw-rows companion resolves too: it is what `estimateNumGroups`
	// divides by, and a zero here silently disables the per-relation clamp.
	if got, want := columnRawRowsForChild(ow, nli), float64(73049); got != want {
		t.Fatalf("raw rows of inner col 0 through an NLI = %v, want %v", got, want)
	}
}

func TestNumGroupsThroughNestedLoopIndexJoin(t *testing.T) {
	// Q99's shape and Q99's numbers: three grouping keys with 4, 6 and 3
	// distinct values over a 720657-row NLI input. 4·6·3 = 72 groups; the
	// query returns 90 and PG 18.3 estimates 72.
	const inputRows = int64(720657)
	outer := numGroupsScan("dims", inputRows, 4, 6, 3)
	nli := mergedNLI(JoinTypeInner, outer, nliInnerScan("date_dim", 73049, 3))

	// Guard the fixture rather than the arithmetic: this test only
	// discriminates while the UNRESOLVED product saturates the closing clamp,
	// which is the collapse's signature. Written against the constant, never
	// its value — pinning 200 here would let a recalibration silently turn
	// this into a test of nothing.
	if math.Pow(defaultNumDistinct, 3) <= float64(inputRows) {
		t.Fatalf("fixture no longer discriminates: defaultNumDistinct³ = %v does not exceed inputRows %d",
			math.Pow(defaultNumDistinct, 3), inputRows)
	}

	got := estimateNumGroups([]Expr{jrCol(0), jrCol(1), jrCol(2)}, nli, inputRows)
	if want := int64(72); got != want {
		t.Fatalf("groups above an NLI = %d, want %d (4·6·3)", got, want)
	}
	if got == inputRows {
		t.Fatalf("groups collapsed to the INPUT row count (%d) — the resolver "+
			"is not finding statistics through the NLI", inputRows)
	}
}

func TestResolveBaseColumnNLIDeclinesOnMissingChildren(t *testing.T) {
	// A half-built node must decline, not panic: `resolveBaseColumn` runs
	// during the search over partially wired trees.
	inner := nliInnerScan("date_dim", 73049, 3)
	if _, ok := resolveBaseColumn(0, &NestedLoopIndexJoin{Type: JoinTypeInner, Inner: inner}); ok {
		t.Fatalf("resolved a column through an NLI with no Outer")
	}
	outer := numGroupsScan("dims", 100, 4)
	nli := &NestedLoopIndexJoin{Type: JoinTypeInner, Outer: outer}
	nli.schema = append(Schema(nil), outer.Output()...)
	if _, ok := resolveBaseColumn(len(outer.Output()), nli); ok {
		t.Fatalf("resolved an inner-side coordinate through an NLI with no Inner")
	}
}
