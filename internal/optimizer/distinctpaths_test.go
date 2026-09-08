package optimizer

// C-16 (P4-07) — the DISTINCT upper rel's hashed / unique paths.
// What is pinned: the DISTINCT sizing (P1-25 estimate), the single-
// candidate-per-shape invariant, hashed-disabled (not skipped) under
// enable_hashagg=off with the unique winner, the Unique arm emitting
// all-columns DistinctOn over the producer-stacked Sort, the DISTINCT ON
// gate (producer never fires there), and the C-10c Sort arm one node up.

import (
	"strings"
	"testing"
)

// distinctTestSpec builds a minimal DISTINCT spec over a priced child.
func distinctTestSpec(child Node) *Distinct {
	return &Distinct{pos: 0, Child: child, schema: child.Output()}
}

// TestSizeDistinctRelFromNode pins the §3.4 duty: Rows from P1-25's
// estimate, Width/NCols/AvgVarBytes from the output.
func TestSizeDistinctRelFromNode(t *testing.T) {
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	if rel.Rows < 1 {
		t.Fatalf("DISTINCT rel rows = %v, want >= 1", rel.Rows)
	}
	if rel.NCols != 3 {
		t.Fatalf("DISTINCT rel NCols = %d, want 3 (output width)", rel.NCols)
	}
	if rel.Width <= 0 {
		t.Fatalf("DISTINCT rel Width = %d, want > 0", rel.Width)
	}
}

// TestAddDistinctPathsSingleCandidatePerShape pins the §5 negative: one
// hashed + one unique candidate, never two of a kind — and no third
// "sorted Distinct", which would price and order identically to unique
// and die as a duplicate in add_path (offering it would be noise).
func TestAddDistinctPathsSingleCandidatePerShape(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	seed := newPrebuiltPath(rel, in)
	seed.Rows = 1000
	seed.Cost = Cost{Total: 100}
	addDistinctPaths(rel, seed, spec, in, cp, DefaultPlannerSettings())
	if len(rel.Pathlist) != 2 {
		t.Fatalf("pathlist holds %d paths, want exactly 2 (hashed, unique)", len(rel.Pathlist))
	}
	var hashed, unique int
	for _, p := range rel.Pathlist {
		if p.Kind != PathDistinct {
			t.Fatalf("path kind %d, want PathDistinct", p.Kind)
		}
		if p.Distinct == nil {
			t.Fatalf("PathDistinct without a spec")
		}
		if p.Unique {
			unique++
			continue
		}
		if len(p.Children) == 1 && p.Children[0] == seed {
			hashed++
			continue
		}
		t.Fatalf("unexpected third shape: unique=%v children=%d", p.Unique, len(p.Children))
	}
	if hashed != 1 || unique != 1 {
		t.Fatalf("hashed=%d unique=%d, want one of each", hashed, unique)
	}
}

// TestCreateDistinctPathsHashAggOffDisabled pins the GUC-off migration:
// the hashed path is offered with disabled=1 (B-17a preference, never
// skip). The winner pin lives in TestCreateDistinctPathsUniqueEmitsDistinctOn
// (DisabledNodes dominance forces unique there, not coincidence).
func TestCreateDistinctPathsHashAggOffDisabled(t *testing.T) {
	lines := captureTrace(t, func() {
		cat := presortedAggCatalog(t)
		stmt := parseOne(t, "select distinct ten from tenk1")
		if _, err := PlanWithSettings(stmt, cat, hashAggSettings(false)); err != nil {
			t.Fatal(err)
		}
	})
	var hashed string
	nDistinct := 0
	for _, l := range lines {
		if strings.Contains(l, "producer="+distinctHashedProducer) {
			hashed = l
		}
		if strings.Contains(l, "producer=upper.distinct.") {
			nDistinct++
		}
	}
	if hashed == "" {
		t.Fatalf("no %s line: the hashed arm was skipped instead of disabled", distinctHashedProducer)
	}
	if !strings.Contains(hashed, "disabled=1 ") {
		t.Fatalf("hashed line = %q, want disabled=1", hashed)
	}
	if nDistinct < 2 {
		t.Fatalf("only %d distinct producers fired, want hashed + at least one Sort-driven arm", nDistinct)
	}
}

// TestCreateDistinctPathsUniqueEmitsDistinctOn pins the C-16b arm mapping:
// with hashing disabled, the winner over a sorted input is a DistinctOn
// with all-output-columns keys (streaming adjacent dedup), not a Distinct.
func TestCreateDistinctPathsUniqueEmitsDistinctOn(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select distinct ten from tenk1")
	node, err := PlanWithSettings(stmt, cat, hashAggSettings(false))
	if err != nil {
		t.Fatal(err)
	}
	// Unwrap an optional Project root: single-column DISTINCT may plan
	// without one.
	var below Node
	if p, ok := node.(*Project); ok {
		below = p.Child
	} else {
		below = node
	}
	uo, ok := below.(*DistinctOn)
	if !ok {
		t.Fatalf("plan child is %T, want *DistinctOn (unique winner GUC-off)", below)
	}
	if len(uo.KeyCols) != len(uo.Output()) {
		t.Fatalf("DistinctOn KeyCols = %v over %d output cols, want all-columns keys", uo.KeyCols, len(uo.Output()))
	}
	if _, ok := uo.Child.(*Sort); !ok {
		t.Fatalf("DistinctOn.Child is %T, want *Sort (producer-stacked order)", uo.Child)
	}
}

// TestCreateDistinctPathsDistinctOnGate pins BLOCKING-2's gate: DISTINCT ON
// planning never enters the DISTINCT producer (both parsers leave
// Distinct=false there today; the producer gate is defense-in-depth).
func TestCreateDistinctPathsDistinctOnGate(t *testing.T) {
	cat := presortedAggCatalog(t)
	stmt := parseOne(t, "select distinct on (ten) ten, two from tenk1 order by ten")
	lines := captureTrace(t, func() {
		if _, err := Plan(stmt, cat); err != nil {
			t.Fatal(err)
		}
	})
	for _, l := range lines {
		if strings.Contains(l, "producer=upper.distinct.") {
			t.Fatalf("DISTINCT producer fired on a DISTINCT ON query: %q", l)
		}
	}
}

// TestDistinctPathsC10cReassert is C-10c's per-item re-assert for C-16
// (DESIGN §7): the producer introduces no new evaluation site below the
// DISTINCT, and the qual-placement pass treats the DISTINCT subtree
// exactly as before — it has no Distinct/DistinctOn arm, so it stops at
// the DISTINCT root whether the winner is hashed or unique. Driven GUC-off
// so the Sort-driven (unique) winner is exercised. What is pinned is
// preservation: the pass returns the winner with its Sort→Filter→Join
// input spine byte-identical (no descent, no splice, no drop).
func TestDistinctPathsC10cReassert(t *testing.T) {
	left := srcScan("c", srcCol("id", 1), srcCol("name", 1))
	right := srcScan("o", srcCol("cust", 2), srcCol("amount", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	resid := &Filter{Child: j, Predicate: srcGt(0, "id", 1, 7)}
	spec := &Distinct{Child: resid, schema: resid.Output()}
	got, err := createDistinctPaths(newUpperRels(), spec, nil, hashAggSettings(false), 0)
	if err != nil {
		t.Fatal(err)
	}
	uo, ok := got.(*DistinctOn)
	if !ok {
		t.Fatalf("producer returned %T, want *DistinctOn (unique must win GUC-off)", got)
	}
	srt, ok := uo.Child.(*Sort)
	if !ok {
		t.Fatalf("DistinctOn.Child is %T, want *Sort (producer-stacked order)", uo.Child)
	}
	if _, ok := srt.Child.(*Filter); !ok {
		t.Fatalf("Sort.Child is %T, want the *Filter input", srt.Child)
	}
	moved := pushSingleSideQualsIntoInnerJoinInputs(got)
	muo, ok := moved.(*DistinctOn)
	if !ok {
		t.Fatalf("pass returned %T, want the *DistinctOn root back", moved)
	}
	if moved != got {
		t.Fatalf("pass rebuilt the DISTINCT root: want pointer-identical (no arm fires below Distinct)")
	}
	msrt, ok := muo.Child.(*Sort)
	if !ok {
		t.Fatalf("after pass: DistinctOn.Child is %T, want *Sort (no descent, no splice)", muo.Child)
	}
	if _, ok := msrt.Child.(*Filter); !ok {
		t.Fatalf("after pass: Sort.Child is %T, want the untouched *Filter", msrt.Child)
	}
}

// TestCreateDistinctPathsNilSpec pins the defensive error.
func TestCreateDistinctPathsNilSpec(t *testing.T) {
	if _, err := createDistinctPaths(nil, nil, nil, DefaultPlannerSettings(), 0); err == nil {
		t.Fatalf("nil spec: want an error, got nil")
	}
}

// TestDistinctCostSharedInput pins the pricing shape: hashed vs unique
// differ only in input price (seed vs Sort) — the dedup terms are shared,
// pinned as numbers (unique == distinctCost over its Sort input exactly),
// and unique prices strictly above hashed (the Sort costs something).
func TestDistinctCostSharedInput(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	seed := newPrebuiltPath(rel, in)
	seed.Rows = 1000
	seed.Cost = Cost{Total: 100}
	addDistinctPaths(rel, seed, spec, in, cp, DefaultPlannerSettings())
	var unique *Path
	for _, p := range rel.Pathlist {
		if p.Unique {
			unique = p
		}
	}
	if unique == nil {
		t.Fatalf("no unique candidate offered")
	}
	if len(unique.Children) != 1 {
		t.Fatalf("unique has %d children, want the 1 Sort input", len(unique.Children))
	}
	sortIn := unique.Children[0]
	want := distinctCost(sortIn.Cost.Startup, sortIn.Cost.Total, 1000, rel.Rows, cp)
	if unique.Cost != want {
		t.Fatalf("unique %+v != distinctCost over its Sort input %+v", unique.Cost, want)
	}
	for _, p := range rel.Pathlist {
		if p.Unique {
			continue
		}
		if !(unique.Cost.Total > p.Cost.Total) {
			t.Fatalf("unique %+v not above hashed %+v: the Sort must cost something", unique.Cost, p.Cost)
		}
	}
}
