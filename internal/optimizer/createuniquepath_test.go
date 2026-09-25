package optimizer

// M0142-0008c-1: createUniquePath's cache field and producer. What is
// pinned: the cache (repeat calls return the same *Path, even given a
// mutated/invalid sjinfo the second time), every "return nil" guard (not a
// SEMI join, SemiCanBtree false, no correlation columns, subpath not a
// PathPrebuilt, a correlation column that no longer names the same output
// position), and the priced Sort+DistinctOn shape on the one success path.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// uniquePathFixture builds a 3-column priced leaf (reusing upperOrderedInput's
// shape) wrapped as the PathPrebuilt atomic-RHS shape createUniquePath
// requires, a RelOptInfo over it, and a SEMI SpecialJoinInfo correlated on
// column 0 ("k") — the shape existsUnnestSJInfo produces for `EXISTS (SELECT
// ... WHERE k = outer.col)`.
func uniquePathFixture(rows float64) (*RelOptInfo, *Path, *SpecialJoinInfo) {
	child := upperOrderedInput(rows)
	rel := &RelOptInfo{Rows: rows}
	subpath := newPrebuiltPath(rel, child)
	sjinfo := &SpecialJoinInfo{
		Jointype:     parser.JoinSemi,
		SemiCanBtree: true,
		SemiCanHash:  true,
		SemiRhsExprs: []Expr{&ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}},
	}
	return rel, subpath, sjinfo
}

func TestCreateUniquePathSuccessShape(t *testing.T) {
	cp := defaultCostParams()
	rel, subpath, sjinfo := uniquePathFixture(1000)

	got := createUniquePath(rel, subpath, sjinfo, cp)
	if got == nil {
		t.Fatal("createUniquePath = nil, want a Path")
	}
	if got.Kind != PathUnique {
		t.Errorf("Kind = %v, want PathUnique", got.Kind)
	}
	if got.Rel != rel {
		t.Error("Rel does not point back at the RHS rel")
	}
	if len(got.UniqueKeyCols) != 1 || got.UniqueKeyCols[0] != 0 {
		t.Errorf("UniqueKeyCols = %v, want [0]", got.UniqueKeyCols)
	}
	if len(got.Children) != 1 || got.Children[0].Kind != PathSort {
		t.Fatalf("Children = %v, want exactly one PathSort", got.Children)
	}
	if got.Rows <= 0 {
		t.Errorf("Rows = %v, want > 0", got.Rows)
	}

	// Cost: the stacked Sort's own price, plus PG's
	// "cpu_operator_cost per comparison per input tuple"
	// (pathnode.c:2015-2019) charged to TOTAL only.
	wantSort := sortPathForBounded(subpath, pathkeysForSortKeys([]SortKey{{Expr: sjinfo.SemiRhsExprs[0]}}), cp, -1)
	wantTotal := wantSort.Cost.Total + cp.cpuOperatorCost*rel.Rows*float64(len(sjinfo.SemiRhsExprs))
	if got.Cost.Startup != wantSort.Cost.Startup {
		t.Errorf("Cost.Startup = %v, want %v (bare sort startup, no extra charge)", got.Cost.Startup, wantSort.Cost.Startup)
	}
	if got.Cost.Total != wantTotal {
		t.Errorf("Cost.Total = %v, want %v", got.Cost.Total, wantTotal)
	}
}

func TestCreateUniquePathCachesOnRel(t *testing.T) {
	cp := defaultCostParams()
	rel, subpath, sjinfo := uniquePathFixture(1000)

	first := createUniquePath(rel, subpath, sjinfo, cp)
	if first == nil {
		t.Fatal("first call = nil, want a Path")
	}
	if rel.CheapestUnique != first {
		t.Fatal("RelOptInfo.CheapestUnique was not stamped by the first call")
	}
	// A second call with a now-uncorrelatable sjinfo must still return the
	// CACHED result, never re-derive (PG's own cache check runs before any
	// of the "can't unique-ify" guards — pathnode.c:1748-1753).
	second := createUniquePath(rel, subpath, &SpecialJoinInfo{Jointype: parser.JoinInner}, cp)
	if second != first {
		t.Errorf("second call returned a different *Path; want the cached one")
	}
}

// TestCreateUniquePlanEmitsDistinctOn pins createplansimple.go's PathUnique
// arm: always *DistinctOn (never *Distinct — createUniquePath never builds
// the HASHED candidate, see its own doc comment), keyed by UniqueKeyCols,
// over the built Sort child.
func TestCreateUniquePlanEmitsDistinctOn(t *testing.T) {
	cp := defaultCostParams()
	rel, subpath, sjinfo := uniquePathFixture(1000)
	p := createUniquePath(rel, subpath, sjinfo, cp)
	if p == nil {
		t.Fatal("createUniquePath = nil, want a Path")
	}
	node, _ := createPlanNode(p)
	don, ok := node.(*DistinctOn)
	if !ok {
		t.Fatalf("createPlanNode(PathUnique) = %T, want *DistinctOn", node)
	}
	if len(don.KeyCols) != 1 || don.KeyCols[0] != 0 {
		t.Errorf("KeyCols = %v, want [0]", don.KeyCols)
	}
	sortNode, ok := don.Child.(*Sort)
	if !ok {
		t.Fatalf("DistinctOn.Child = %T, want *Sort (createUniquePath always stacks a Sort)", don.Child)
	}
	if sortNode.Child != subpath.node {
		t.Error("Sort.Child is not the SEMI RHS's own built node")
	}
	if len(don.Output()) != len(don.Child.Output()) {
		t.Errorf("Output() has %d columns, want %d (DistinctOn passes every column through)", len(don.Output()), len(don.Child.Output()))
	}
}

func TestCreateUniquePathDeclines(t *testing.T) {
	cp := defaultCostParams()

	t.Run("not a SEMI join", func(t *testing.T) {
		rel, subpath, sjinfo := uniquePathFixture(1000)
		sjinfo.Jointype = parser.JoinInner
		if got := createUniquePath(rel, subpath, sjinfo, cp); got != nil {
			t.Errorf("got %v, want nil (Jointype != JoinSemi)", got)
		}
	})

	t.Run("cannot unique-ify", func(t *testing.T) {
		rel, subpath, sjinfo := uniquePathFixture(1000)
		sjinfo.SemiCanBtree = false
		if got := createUniquePath(rel, subpath, sjinfo, cp); got != nil {
			t.Errorf("got %v, want nil (!SemiCanBtree — HASH-only is an unimplemented gap, M0142-0008c-1a)", got)
		}
	})

	t.Run("no correlation columns", func(t *testing.T) {
		rel, subpath, sjinfo := uniquePathFixture(1000)
		sjinfo.SemiRhsExprs = nil
		if got := createUniquePath(rel, subpath, sjinfo, cp); got != nil {
			t.Errorf("got %v, want nil (empty SemiRhsExprs)", got)
		}
	})

	t.Run("subpath is not the atomic-RHS PathPrebuilt shape", func(t *testing.T) {
		rel, subpath, sjinfo := uniquePathFixture(1000)
		subpath.Kind = PathSeqScan
		if got := createUniquePath(rel, subpath, sjinfo, cp); got != nil {
			t.Errorf("got %v, want nil (subpath.Kind != PathPrebuilt)", got)
		}
	})

	t.Run("correlation column index out of range", func(t *testing.T) {
		rel, subpath, sjinfo := uniquePathFixture(1000)
		sjinfo.SemiRhsExprs = []Expr{&ColumnRef{Index: 99, Name: "k", Type: catalog.Type{Name: "int4"}}}
		if got := createUniquePath(rel, subpath, sjinfo, cp); got != nil {
			t.Errorf("got %v, want nil (index out of range)", got)
		}
	})

	t.Run("correlation column identity drifted", func(t *testing.T) {
		rel, subpath, sjinfo := uniquePathFixture(1000)
		// Index 0 is really "k" (see upperOrderedInput); naming it "k2"
		// simulates the subpath's schema no longer matching what
		// SemiRhsExprs was built against.
		sjinfo.SemiRhsExprs = []Expr{&ColumnRef{Index: 0, Name: "k2", Type: catalog.Type{Name: "int4"}}}
		if got := createUniquePath(rel, subpath, sjinfo, cp); got != nil {
			t.Errorf("got %v, want nil (name mismatch at that index)", got)
		}
	})

	t.Run("non-ColumnRef correlation expression", func(t *testing.T) {
		rel, subpath, sjinfo := uniquePathFixture(1000)
		sjinfo.SemiRhsExprs = []Expr{&IntegerConst{Value: 1}}
		if got := createUniquePath(rel, subpath, sjinfo, cp); got != nil {
			t.Errorf("got %v, want nil (goopg's one producer never emits this, decline rather than guess)", got)
		}
	})
}
