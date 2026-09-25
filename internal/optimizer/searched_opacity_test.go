package optimizer

// M0145-0005 slice 5 — searched-subtree opacity for the residual
// qual-redistribution family. A searched subtree is closed: the
// search's own clause distribution already placed every qual it
// admitted, and a conjunct still above the searched root is one the
// seam deliberately held there (nullable-side or unattributable
// guards). `pushOneConjunct`, `rewriteScanInputsWithSingleTable-
// Predicates`'s outer walk and `rewriteJoinsToNLI` already kept this
// contract (P5.9-b); these tests pin the two holes that remained —
// pushSingleSideQualsIntoInnerJoinInputs (walker, Filter-level entry,
// and the pushConjunctIntoSubtree descent it makes) and the scan hunt
// inside absorbConjunctsIntoSubtree — plus the counter-pin: the
// CTE-inline entry (pushConjunctIntoSubtree) must STAY permissive,
// because its conjunct arrives from outside the body's scope and
// crossing the searched boundary is the parse-level qual pushdown
// (R42's measured witness).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// ijSeqScan builds a one-table SeqScan carrying the given columns.
func ijSeqScan(name string, cols ...string) *SeqScan {
	tbl := &catalog.Table{Name: name}
	var sc Schema
	for _, c := range cols {
		sc = append(sc, ijCol(c))
		tbl.Columns = append(tbl.Columns, catalog.Column{Name: c, Type: catalog.Type{Name: "int4"}})
	}
	return &SeqScan{Table: tbl, schema: sc}
}

// A residual Filter over a searched INNER join must not plant its
// conjunct inside the searched tree — the seam's residual placement is
// deliberate. The join's inputs stay unwrapped and the residual keeps
// the conjunct.
func TestPushdownDoesNotEnterSearchedJoin(t *testing.T) {
	left, right := ijSeqScan("a", "x"), ijSeqScan("b", "y")
	f, j := ijFilterOverInnerJoin(left, right, JoinTypeInner, ijEq(0, "x", 7))
	j = markSearchedTree(j).(*Join)
	f.Child = j

	got := pushSingleSideQualsIntoInnerJoinInputs(f)
	gf, ok := got.(*Filter)
	if !ok || gf.Predicate == nil {
		t.Fatalf("residual Filter lost: got %T", got)
	}
	if _, wrapped := j.Left.(*Filter); wrapped {
		t.Fatal("conjunct planted inside the searched subtree")
	}
}

// Same verdict one level down: the descent into an unsearched join
// must still stop at a searched grandchild — the conjunct stays in
// the residual.
func TestPushdownStopsAtSearchedGrandchild(t *testing.T) {
	searched := markSearchedTree(ijSeqScan("a", "x"))
	other := ijSeqScan("b", "y")
	// unsearched inner join: [a.x=0, b.y=1]; conjunct reads a.x only.
	f, j := ijFilterOverInnerJoin(searched, other, JoinTypeInner, ijEq(0, "x", 7))

	got := pushSingleSideQualsIntoInnerJoinInputs(f)
	gf, ok := got.(*Filter)
	if !ok || gf.Predicate == nil {
		t.Fatalf("residual Filter lost: got %T", got)
	}
	if _, wrapped := j.Left.(*Filter); wrapped {
		t.Fatal("conjunct planted inside the searched grandchild")
	}
}

// And a positive control beside the same shape: the conjunct naming
// the UNSEARCHED side still lands on that input.
func TestPushdownStillEntersUnsearchedSide(t *testing.T) {
	searched := markSearchedTree(ijSeqScan("a", "x"))
	other := ijSeqScan("b", "y")
	f, j := ijFilterOverInnerJoin(searched, other, JoinTypeInner, ijEq(1, "y", 7))

	pushSingleSideQualsIntoInnerJoinInputs(f)
	if _, wrapped := j.Right.(*Filter); !wrapped {
		t.Fatal("unsearched-side conjunct not planted — the prune over-blocks")
	}
}

// Counter-pin: the CTE-inline entry point is intentionally permissive —
// its conjuncts arrive from outside the searched body's scope, and
// crossing the boundary is the qual pushdown PG performs at parse
// level (R42). pushConjunctIntoSubtree must still reach a leaf inside
// a searched subtree.
func TestCTEPushdownStillCrossesSearchedBoundary(t *testing.T) {
	scan := ijSeqScan("a", "x")
	searched := markSearchedTree(scan)
	// A conjunct written in the subtree's own coordinate space, col idx 0.
	c := ijEq(0, "x", 7)
	repl, ok := pushConjunctIntoSubtree(searched, c)
	if !ok {
		t.Fatal("CTE-inline pushdown declined at the searched boundary")
	}
	rf, isFilter := repl.(*Filter)
	if !isFilter || rf.Child != searched {
		t.Fatalf("expected Filter wrapping the searched leaf, got %T", repl)
	}
}

// absorbConjunctsIntoSubtree's scan hunt must not find a SeqScan
// inside a searched subtree: the search costed that scan, and a
// nullable-held conjunct planted below the null-extension would change
// which rows survive — the wrong-answer class the seam's hold exists
// to prevent. The unsearched side's scan stays findable.
func TestScanHuntStopsAtSearchedSubtree(t *testing.T) {
	searched := markSearchedTree(ijSeqScan("a", "x"))
	other := ijSeqScan("b", "y")
	f, _ := ijFilterOverInnerJoin(searched, other, JoinTypeInner, ijEq(0, "x", 7))

	if hit, _, ok := findUniqueSeqScanByColumn(f.Child, "x", f); ok || hit != nil {
		t.Fatal("scan hunt found the searched subtree's scan")
	}
	if hit, _, ok := findUniqueSeqScanByColumn(f.Child, "y", f); !ok || hit == nil {
		t.Fatal("scan hunt lost the unsearched side's scan — over-blocking")
	}
}
