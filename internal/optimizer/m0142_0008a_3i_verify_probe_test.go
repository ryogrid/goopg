package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// analyzedThreeTablesCatalog mirrors threeTablesCatalog (t1(x), t2(y,z),
// t3(a,b)) but seeds RowCount stats so EstimateRows produces a real
// (non-zero) number for the sanity checks below — threeTablesCatalog's
// tables are deliberately un-ANALYZEd for its own (unrelated) tests, and
// EstimateRows correctly returns 0 for an un-ANALYZEd base table (no fallback
// GUC is set in this package's tests).
func analyzedThreeTablesCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols []catalog.Column, rows int64) {
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows}
	}
	mk("t1", []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}}, 1000)
	mk("t2", []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}},
		{Name: "z", Type: catalog.Type{Name: "int4"}},
	}, 500)
	mk("t3", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}, 200)
	return c
}

// M0142-0008a-3i-verify (design doc §12.3/12.4/§13, filed by -3i-recon2): a
// live instrumented probe for the "-3i-recon2 is a static read only" caveat.
//
// -3i-recon2 claimed, from reading code rather than running it, that a
// multi-table EXISTS body (TPC-DS Q69's witness class) unnests to a Semi/Anti
// join whose Right child is LITERALLY *Join{Type:JoinTypeInner} — and from
// that claim, that extractSearchLeaves needs a new opaque-leaf wrapper (a
// bare *Filter{Predicate:nil}) to stop at it instead of wrongly recursing in.
//
// This test builds the smallest fixture that reproduces the shape
// (threeTablesCatalog's t1/t2/t3, mirroring Q69's "channel ⋈ date_dim" RHS
// body) and finds the claim is WRONG in a way that matters: j.Right is
// *Project{Child: *Join{Inner}}, not a bare *Join — unnestExistsExpr clones
// the EXISTS body's OWN already-planned subquery tree (`ex.Plan`), and every
// planned SELECT carries its own top-level output-list *Project, even a
// constant one (`SELECT 1`). Since *Project already fails
// extractSearchLeaves's `isJoin` type test, the walk ALREADY stops at j.Right
// as one opaque leaf, with ZERO new wrapper code — the Filter-wrapper
// proposal in §12.3 is unnecessary for this witness class, not merely
// "cheaper than CTEScan" as §12.3 framed it. See design doc §13 for the full
// write-up; only the §12.4(a)/(b)/(c) DP-search plumbing (RelOptInfo/SJInfo
// registration + reresolveJoinByName's post-search splice) remains open.
func TestExistsUnnestTwoRelationRHSTopNodeIsProjectNotBareJoin(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (" +
		"SELECT 1 FROM t2, t3 WHERE t2.z = t1.x AND t2.y = t3.a)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}

	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}

	// Refuted claim: j.Right is NOT a bare *Join. It is topped by the
	// EXISTS body's own output-list *Project.
	if _, ok := j.Right.(*Join); ok {
		t.Fatalf("j.Right = *Join directly (matches -3i-recon2's claim) — re-check whether this fixture still exercises the multi-table-body shape: %s", planString(node))
	}
	proj, ok := j.Right.(*Project)
	if !ok {
		t.Fatalf("j.Right = %T, want *Project (the EXISTS body's own output-list projection): %s", j.Right, planString(node))
	}
	inner, ok := proj.Child.(*Join)
	if !ok || inner.Type != JoinTypeInner {
		t.Fatalf("j.Right.(*Project).Child = %T (%v), want *Join{Type:JoinTypeInner} (the t2 ⋈ t3 body): %s", proj.Child, proj.Child, planString(node))
	}

	// extractSearchLeaves already stops at j.Right (the *Project) as one
	// opaque leaf, out of the box — no new wrapper node needed.
	scans, _, _, _, walkOK := extractSearchLeaves(j.Right)
	if !walkOK {
		t.Fatalf("extractSearchLeaves(j.Right) ok=false, want true (a *Project must not be declined)")
	}
	if len(scans) != 1 || scans[0] != Node(j.Right) {
		t.Errorf("extractSearchLeaves(j.Right) scans=%v, want exactly [j.Right] (already one opaque leaf, no recursion into inner.Left/inner.Right)", scans)
	}

	// EstimateRows already has a generic *Project pass-through case
	// (cardinality.go: `case *Project: return EstimateRows(x.Child)`), so it
	// already recurses correctly to the real join estimate underneath —
	// confirm it's non-zero and matches calling EstimateRows on the inner
	// *Join directly (i.e. the Project is a true no-op for cardinality, the
	// same property §12.3 wanted from its proposed Filter wrapper).
	projRows := EstimateRows(j.Right)
	innerRows := EstimateRows(inner)
	if projRows <= 0 {
		t.Fatalf("EstimateRows(j.Right) = %d, want > 0", projRows)
	}
	if projRows != innerRows {
		t.Errorf("EstimateRows(Project) = %d, EstimateRows(inner Join) = %d — Project is not cardinality-neutral here", projRows, innerRows)
	}

	// baseSeqScanCostInputs's generic non-*SeqScan fallback already applies
	// to j.Right unchanged (leafBaseScan does not unwrap *Project, so
	// leafBaseScan(j.Right) == j.Right, which is not *SeqScan either way).
	var ri baseRelInfo
	pages, tuples, ops := baseSeqScanCostInputs(ri, j.Right, float64(projRows), 8)
	if tuples != float64(projRows) {
		t.Errorf("baseSeqScanCostInputs tuples = %v, want the fallbackRows argument (%v) unchanged — the generic non-*SeqScan branch should have fired", tuples, projRows)
	}
	if pages <= 0 || ops != 0 {
		t.Errorf("baseSeqScanCostInputs(j.Right) = (pages=%d, tuples=%v, ops=%d), want (estScanPages(fallback), fallback, 0) — the doc'd generic fallback shape", pages, tuples, ops)
	}
}
