package optimizer

// C-12 (P4-03) — the ORDERED upper rel's real `PathSort`. What is pinned:
// the node the producer emits is the rewrite's node (same child, keys,
// position), it now carries `cost_sort`'s price through the NAMED cost
// constants, the price includes the external-merge arm when the sized rel
// says the sort spills (DESIGN §4.3 — the silent failure to look for first),
// both producers exist and are adjudicable on the DPPATH trace, and the C-10c
// arm the Sort still passes through is still reached.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// pricedNode is a Node that carries a search-style PlanCost, so the producer's
// input arm reads the stamp (`legacyDisplayCostOf`) rather than deriving.
type pricedNode struct {
	PlanCost
	sch Schema
}

func (n *pricedNode) Output() Schema { return n.sch }
func (n *pricedNode) Pos() int       { return 0 }
func (n *pricedNode) planNode()      {}

func upperOrderedInput(rows float64) *pricedNode {
	n := &pricedNode{sch: Schema{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "text"}},
		{Name: "w", Type: catalog.Type{Name: "numeric"}},
	}}
	n.setPlanCost(PlanCost{StartupCost: 10, TotalCost: 100, PlanRows: rows, PlanWidth: 1})
	return n
}

// searchedPricedNode is pricedNode plus the searchedTree tag, standing in for
// a search root (`*SeqScan`/`*Join`/... in production) without dragging in
// catalog.Table — M0141-S2b-2a's plumbing test needs `searchedRelOf` to
// resolve on the input Node the way it does for a real searched subtree.
type searchedPricedNode struct {
	pricedNode
	searchedTree
}

func upperOrderedKeys() []SortKey {
	return []SortKey{
		{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}, Desc: true},
		{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, NullsFirst: true},
	}
}

// TestCreateOrderedPathsEmitsTheRewritesSortWithCostSortsPrice: the node is
// what `orderSort = &Sort{pos, Child: node, Keys: keys}` built — and its
// PlanCost is the input's total plus `costSortRun` over the SIZED rel.
func TestCreateOrderedPathsEmitsTheRewritesSortWithCostSortsPrice(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	keys := upperOrderedKeys()
	u := newUpperRels()

	got := createOrderedPaths(u, in, keys, 77, cp, 0, -1)

	srt, ok := got.(*Sort)
	if !ok {
		t.Fatalf("got %T, want *Sort", got)
	}
	if srt.Child != Node(in) {
		t.Fatalf("Sort.Child is %T, want the input node itself", srt.Child)
	}
	if srt.pos != 77 {
		t.Fatalf("Sort.pos = %d, want the statement position 77", srt.pos)
	}
	if len(srt.Keys) != len(keys) {
		t.Fatalf("%d keys, want %d", len(srt.Keys), len(keys))
	}
	for i := range keys {
		if srt.Keys[i].Expr != keys[i].Expr || srt.Keys[i].Desc != keys[i].Desc || srt.Keys[i].NullsFirst != keys[i].NullsFirst {
			t.Fatalf("key %d = %+v, want %+v emitted as written (no baseLeaf, no translation)", i, srt.Keys[i], keys[i])
		}
	}
	if isSearchedTree(srt) {
		t.Fatalf("the ORDERED Sort must not be tagged as a searched root: only createPlanAtSearchRoot marks")
	}

	rel := fetchUpperRel(u, UpperOrdered, 0, 0)
	if rel.NCols != 3 || rel.Rows != 1000 || rel.AvgVarBytes <= 0 {
		t.Fatalf("ORDERED rel not sized from the input: %+v", rel)
	}
	pc, set := srt.PlanCostInfo()
	if !set {
		t.Fatalf("the Sort carries no PlanCost: createPlanNode's stamp was bypassed")
	}
	want := costSortRun(cp, 1000, relNCols(rel), relAvgVarBytes(rel), -1)
	if !approx(pc.StartupCost, 100+want.Startup) || !approx(pc.TotalCost, 100+want.Total) {
		t.Fatalf("Sort cost = (%v, %v), want input total 100 + costSortRun (%v, %v)",
			pc.StartupCost, pc.TotalCost, 100+want.Startup, 100+want.Total)
	}
	if pc.PlanRows != 1000 {
		t.Fatalf("Sort rows = %v, want the input's 1000 (a Sort projects nothing)", pc.PlanRows)
	}
	// The child's stamp is value-identical to what it carried: nothing below
	// the Sort moves.
	if cpc, _ := in.PlanCostInfo(); cpc.StartupCost != 10 || cpc.TotalCost != 100 || cpc.PlanRows != 1000 {
		t.Fatalf("the input's PlanCost was rewritten to %+v", cpc)
	}
	if rel.CheapestTotal == nil || rel.CheapestTotal.Kind != PathSort || len(rel.Pathlist) != 1 {
		t.Fatalf("ORDERED rel pathlist = %d paths, cheapest %v; want the one sort path", len(rel.Pathlist), rel.CheapestTotal)
	}
}

// TestCreateOrderedPathsChargesTheSpillOfALargeSort is DESIGN §5.7's named
// negative result — "a Sort's new cost is LOWER than its legacy display cost:
// then §4.3's NCols population did not happen and the disk arm is suppressed."
// A sort whose sized input exceeds work_mem must be priced ABOVE both the
// unsized (NCols = 0) price and the legacy in-memory display price.
func TestCreateOrderedPathsChargesTheSpillOfALargeSort(t *testing.T) {
	cp := defaultCostParams()
	u := newUpperRels()
	// Size the rel first to learn its column model, then pick a row count
	// that overflows the budget at that width.
	probe := fetchUpperRel(u, UpperOrdered, 0, 0)
	sizeUpperRelFromNode(probe, upperOrderedInput(1))
	rows := sortRowsFillingBudget(cp.workMem, probe.NCols, 3.0)
	in := upperOrderedInput(rows)

	srt := createOrderedPaths(u, in, upperOrderedKeys(), 0, cp, 0, -1).(*Sort)
	pc, _ := srt.PlanCostInfo()

	unsized := costSortRun(cp, rows, 0, 0, -1)
	if !(pc.StartupCost-100 > unsized.Startup) {
		t.Fatalf("spilling sort priced at %v, not above the NCols=0 in-memory price %v: the disk arm is suppressed",
			pc.StartupCost-100, unsized.Startup)
	}
	legacy := DeriveLegacyDisplayCost(srt, int64(rows))
	if !(pc.StartupCost > legacy.StartupCost) {
		t.Fatalf("cost_sort price %v is not above the legacy display price %v — the negative result DESIGN §5.7 names", pc.StartupCost, legacy.StartupCost)
	}
}

// TestCreateOrderedPathsThreadsSearchCandidatesOntoOrderedRel is M0141-S2b-2a's
// gate: `searchedRelOf(input)`'s Pathlist becomes reachable off the ORDERED
// rel (`RelOptInfo.SearchCandidates`) once `input` is a searched-tree root —
// and, per S2b-2's own scoping recon (design doc
// m0141-s2b-scoping-decomposition.md §"S2b-2 result"), reaching it changes
// NOTHING about the elected plan yet: `addOrderedPaths` still offers only the
// one seed, so `ordered.Pathlist` stays exactly what a non-searched input
// produces (TestCreateOrderedPathsEmitsTheRewritesSortWithCostSortsPrice).
func TestCreateOrderedPathsThreadsSearchCandidatesOntoOrderedRel(t *testing.T) {
	cp := defaultCostParams()
	keys := upperOrderedKeys()
	u := newUpperRels()

	searchRel := &RelOptInfo{}
	cand1 := &Path{Kind: PathAgg, Rows: 5, Cost: Cost{Total: 10}}
	cand2 := &Path{Kind: PathAgg, Rows: 5, Cost: Cost{Total: 20}}
	searchRel.Pathlist = []*Path{cand1, cand2}

	in := &searchedPricedNode{pricedNode: *upperOrderedInput(1000)}
	in.markFromJoinSearch()
	in.setSearchRel(searchRel)

	got := createOrderedPaths(u, in, keys, 0, cp, 0, -1)
	if _, ok := got.(*Sort); !ok {
		t.Fatalf("got %T, want *Sort (the searched tag alone, with no searchPathkeys claimed, must still stack the Sort)", got)
	}

	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	if len(ordered.SearchCandidates) != 2 || ordered.SearchCandidates[0] != cand1 || ordered.SearchCandidates[1] != cand2 {
		t.Fatalf("ordered.SearchCandidates = %v, want the search rel's own 2-entry Pathlist by identity", ordered.SearchCandidates)
	}
	// Plumbing only: the tournament still offers exactly the one seed.
	if len(ordered.Pathlist) != 1 {
		t.Fatalf("ordered.Pathlist = %d entries, want 1 — SearchCandidates must not be offered to the tournament yet", len(ordered.Pathlist))
	}
}

// TestCreateOrderedPathsLeavesSearchCandidatesNilForANonSearchedInput pins the
// negative case: a plain Node with no searched-tree tag leaves the new field
// at its zero value, exactly like today (no field, no behavior).
func TestCreateOrderedPathsLeavesSearchCandidatesNilForANonSearchedInput(t *testing.T) {
	u := newUpperRels()
	createOrderedPaths(u, upperOrderedInput(10), upperOrderedKeys(), 0, defaultCostParams(), 0, -1)
	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	if ordered.SearchCandidates != nil {
		t.Fatalf("ordered.SearchCandidates = %v, want nil for a non-searched input", ordered.SearchCandidates)
	}
	if ordered.SearchCandidateKeys != nil {
		t.Fatalf("ordered.SearchCandidateKeys = %v, want nil for a non-searched input", ordered.SearchCandidateKeys)
	}
}

// TestCreateOrderedPathsValidatesSearchCandidatePathkeys is M0141-S2b-2b's
// gate: every OTHER candidate in the search rel's Pathlist gets its own
// Pathkeys re-earned against the ORDERED rel's published schema
// (`ordered.SearchCandidateKeys`), the same truncation rule
// `stampSearchPathkeys` already applies to the single winning path —
// generalized to every candidate, parallel-indexed to SearchCandidates. Still
// plumbing only: `ordered.Pathlist` stays at exactly 1 (the seed), same as
// S2b-2a's own gate.
func TestCreateOrderedPathsValidatesSearchCandidatePathkeys(t *testing.T) {
	cp := defaultCostParams()
	keys := upperOrderedKeys()
	u := newUpperRels()

	// cand1's one key ("k") fully validates against the input's schema.
	kKey := PathKey{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, SortAsc: true}
	// cand2's first key ("v") validates; its second key addresses a column
	// index the schema doesn't have, so validation truncates after the
	// first — a shorter, not a wrong, ordering claim (file header rule 1).
	vKey := PathKey{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}}
	badKey := PathKey{Expr: &ColumnRef{Index: 99, Name: "bogus", Type: catalog.Type{Name: "int4"}}}
	// cand3 carries no ordering claim at all.

	searchRel := &RelOptInfo{}
	cand1 := &Path{Kind: PathAgg, Rows: 5, Cost: Cost{Total: 10}, Pathkeys: []PathKey{kKey}}
	cand2 := &Path{Kind: PathAgg, Rows: 5, Cost: Cost{Total: 20}, Pathkeys: []PathKey{vKey, badKey}}
	cand3 := &Path{Kind: PathAgg, Rows: 5, Cost: Cost{Total: 30}}
	searchRel.Pathlist = []*Path{cand1, cand2, cand3}

	in := &searchedPricedNode{pricedNode: *upperOrderedInput(1000)}
	in.markFromJoinSearch()
	in.setSearchRel(searchRel)

	createOrderedPaths(u, in, keys, 0, cp, 0, -1)

	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	if len(ordered.SearchCandidateKeys) != 3 {
		t.Fatalf("ordered.SearchCandidateKeys = %d entries, want 3 (parallel-indexed to SearchCandidates)", len(ordered.SearchCandidateKeys))
	}
	if len(ordered.SearchCandidateKeys[0]) != 1 || !pathKeyEqual(ordered.SearchCandidateKeys[0][0], kKey) {
		t.Fatalf("cand1's validated keys = %v, want [%v] (fully validates)", ordered.SearchCandidateKeys[0], kKey)
	}
	if len(ordered.SearchCandidateKeys[1]) != 1 || !pathKeyEqual(ordered.SearchCandidateKeys[1][0], vKey) {
		t.Fatalf("cand2's validated keys = %v, want [%v] (truncated after the bad second key)", ordered.SearchCandidateKeys[1], vKey)
	}
	if ordered.SearchCandidateKeys[2] != nil {
		t.Fatalf("cand3's validated keys = %v, want nil (no Pathkeys claimed)", ordered.SearchCandidateKeys[2])
	}
	// Plumbing only: the tournament still offers exactly the one seed.
	if len(ordered.Pathlist) != 1 {
		t.Fatalf("ordered.Pathlist = %d entries, want 1 — SearchCandidateKeys must not be offered to the tournament yet", len(ordered.Pathlist))
	}
}

// TestAddOrderedPathsOffersExactlyOneProducerPerInput: both producers exist
// and are told apart on the trace by name and by `relids=-`. The input arm is
// driven with a hand-ordered seed because no Node above the seam carries
// pathkeys today (DESIGN §5.5) — the arm C-12a turns live must not be dead
// code when it does.
func TestAddOrderedPathsOffersExactlyOneProducerPerInput(t *testing.T) {
	cp := defaultCostParams()
	keys := pathkeysForSortKeys(upperOrderedKeys())

	unordered := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	sizeUpperRelFromNode(unordered, upperOrderedInput(10))
	lines := captureTrace(t, func() {
		addOrderedPaths(unordered, newPrebuiltPath(unordered, upperOrderedInput(10)), keys, cp, -1)
	})
	lines = dppathLines(lines)
	if len(lines) != 1 || !strings.Contains(lines[0], "producer="+upperOrderedSortProducer+" relids=- ") || !strings.Contains(lines[0], "verdict=accepted") {
		t.Fatalf("unordered input: trace = %q, want one accepted %s line at relids=-", lines, upperOrderedSortProducer)
	}
	if unordered.Pathlist[0].Kind != PathSort {
		t.Fatalf("unordered input must get a Sort path, got kind %d", unordered.Pathlist[0].Kind)
	}

	ordered := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	sizeUpperRelFromNode(ordered, upperOrderedInput(10))
	seed := newPrebuiltPath(ordered, upperOrderedInput(10))
	seed.Pathkeys = append(append([]PathKey{}, keys...), PathKey{Expr: &ColumnRef{Index: 2, Name: "w"}, SortAsc: true})
	lines = captureTrace(t, func() {
		addOrderedPaths(ordered, seed, keys, cp, -1)
	})
	lines = dppathLines(lines)
	if len(lines) != 1 || !strings.Contains(lines[0], "producer="+upperOrderedInputProducer+" relids=- ") {
		t.Fatalf("ordered input: trace = %q, want one %s line", lines, upperOrderedInputProducer)
	}
	if len(ordered.Pathlist) != 1 || ordered.Pathlist[0] != seed {
		t.Fatalf("an input that already delivers the keys must be offered as-is, no Sort stacked")
	}
}

// TestCreateOrderedPathsHonoursEnableSortAsAPreference: B-17a — the producer
// is not skipped when enable_sort is off; the path is offered with a disabled
// node so a query whose only plan needs the sort still plans.
func TestCreateOrderedPathsHonoursEnableSortAsAPreference(t *testing.T) {
	cp := defaultCostParams()
	cp.enableSort = false
	u := newUpperRels()
	lines := captureTrace(t, func() {
		if _, ok := createOrderedPaths(u, upperOrderedInput(10), upperOrderedKeys(), 0, cp, 0, -1).(*Sort); !ok {
			t.Errorf("enable_sort=off must still emit the Sort")
		}
	})
	lines = dppathLines(lines)
	if len(lines) != 1 || !strings.Contains(lines[0], "disabled=1 ") {
		t.Fatalf("trace = %q, want the sort path offered with disabled=1", lines)
	}
}

func dppathLines(lines []string) []string {
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "DPPATH ") {
			out = append(out, line)
		}
	}
	return out
}

// TestCreateOrderedPathsNeverDropsTheSort: no keys hands the input back; no
// registry does NOT — a missing registry must never turn into a missing
// ORDER BY, which would be a wrong answer with green row counts.
func TestCreateOrderedPathsNeverDropsTheSort(t *testing.T) {
	cp := defaultCostParams()
	in := upperOrderedInput(10)
	if got := createOrderedPaths(newUpperRels(), in, nil, 0, cp, 0, -1); got != Node(in) {
		t.Fatalf("no keys: got %T, want the input back", got)
	}
	if _, ok := createOrderedPaths(nil, in, upperOrderedKeys(), 0, cp, 0, -1).(*Sort); !ok {
		t.Fatalf("no registry: the Sort was dropped")
	}
}

// TestC10cPreservedSideQualMovesThroughOrderedSortArm is C-10c's per-item
// re-assert for C-12 (p4-upper-rels DESIGN §8): the Sort the ORDERED rel
// emits sits at the same tree position as the rewrite's, so
// pushSingleSideQualsIntoInnerJoinInputs' `*Sort` arm still descends through
// it and the preserved-side move below a LEFT link still happens. Mirrors
// TestC10cPreservedSideQualMovesThroughAggregateArm one node up.
func TestC10cPreservedSideQualMovesThroughOrderedSortArm(t *testing.T) {
	left := srcScan("c", srcCol("id", 1), srcCol("name", 1))
	right := srcScan("o", srcCol("cust", 2), srcCol("amount", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	resid := &Filter{Child: j, Predicate: srcGt(0, "id", 1, 7)}
	keys := []SortKey{{Expr: &ColumnRef{Index: 1, Name: "name", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1}}}

	srt, ok := createOrderedPaths(newUpperRels(), resid, keys, 0, defaultCostParams(), 0, -1).(*Sort)
	if !ok {
		t.Fatalf("the ORDERED rel did not emit a *Sort")
	}
	got := pushSingleSideQualsIntoInnerJoinInputs(srt)

	root, ok := got.(*Sort)
	if !ok {
		t.Fatalf("pass returned %T, want the *Sort root back", got)
	}
	nj, ok := root.Child.(*Join)
	if !ok {
		t.Fatalf("Sort.Child is %T, want the *Join (residual spliced through the Sort arm)", root.Child)
	}
	if _, placed := nj.Left.(*Filter); !placed {
		t.Errorf("Join.Left is %T, want the placed *Filter on the preserved input", nj.Left)
	}
	if _, planted := nj.Right.(*Filter); planted {
		t.Errorf("a Filter reached the NULLABLE input for a preserved-side qual")
	}
}

// TestCreateOrderedPathsInputArmIsReachableFromANode is C-12a's blocker,
// FLIPPED red-to-green on 2026-09-07 by C-07's seam half — the flip its own
// predecessor prescribed ("flip this test — Sort-over-Sort becomes the child
// handed back — in the cut that plumbs `Pathkeys` across the search
// boundary").
//
// What it used to pin: `addOrderedPaths` has two arms, and the ordered one was
// reached only by a seed whose `Pathkeys` the TEST set by hand. Production had
// no such seed. `createOrderedPaths` is handed a NODE, the only Node->Path
// bridge is `newPrebuiltPath`, and that bridge left `Pathkeys` nil — so
// `pathkeysContainedIn(nil, keys)` was false for every non-empty `keys` and
// the Sort arm was the only arm production could take, WHATEVER ordering the
// child actually had.
//
// The pin was deliberately strongest-form and it stays that way, inverted: the
// child below is ALREADY in the requested order — it is the Sort the producer
// itself just built, whose pathkeys are exactly `keys` — and the producer now
// hands it straight back instead of stacking a second, redundant Sort.
// `inputNodePathkeys` (upperorderedinput.go) reads the ordering off that
// `*Sort` in the Node's own output coordinates.
//
// The last assertion is UNCHANGED and still holds, which is the point of
// keeping it: `newPrebuiltPath` still carries no ordering. The fix was NOT to
// teach the C0 bridge about pathkeys — it has callers (distinct, grouping,
// partial-agg, window/setop) whose inputs deliver no ordering and must not be
// made to claim one. `createOrderedPaths` derives the claim itself, from the
// Node, at the one seam that has an ORDER BY to compare it against.
func TestCreateOrderedPathsInputArmIsReachableFromANode(t *testing.T) {
	cp := defaultCostParams()
	keys := upperOrderedKeys()

	sorted, ok := createOrderedPaths(newUpperRels(), upperOrderedInput(10), keys, 0, cp, 0, -1).(*Sort)
	if !ok {
		t.Fatal("the first call must emit a Sort (it is the unordered arm)")
	}
	// `sorted` delivers `keys` by construction. Hand it back as the CHILD.
	again := createOrderedPaths(newUpperRels(), sorted, keys, 0, cp, 0, -1)
	if _, isSort := again.(*Sort); isSort && again != Node(sorted) {
		t.Fatal("a second Sort was stacked over a child that already delivers the keys: " +
			"the seam stopped carrying Pathkeys (upperorderedinput.go inputNodePathkeys)")
	}
	if again != Node(sorted) {
		t.Fatalf("got %T; want the already-sorted child handed straight back", again)
	}
	// The C0 bridge itself still carries no ordering, and must not: its other
	// callers hand it inputs that deliver none. The derivation lives in
	// `createOrderedPaths`, not in `newPrebuiltPath`.
	rel := fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	sizeUpperRelFromNode(rel, sorted)
	if seed := newPrebuiltPath(rel, sorted); len(seed.Pathkeys) != 0 {
		t.Fatalf("newPrebuiltPath must stay ordering-free, got %d pathkeys", len(seed.Pathkeys))
	}
}

// TestOrderedDropsHashedSortForPresortedGrouping is the R47 (K101)
// slice-2 TDD gate: with output-coordinate pathkeys on the sorted
// candidate (the translation slice 2 provides), the ordered rel
// must drop hashed+Sort via the existing startup+fuzz dominance —
// no-sort offered, hashed dominated, sorted elected. Costs mirror
// live PG Q4 (totals within fuzz, startup outside it).
// Companion: TestOrderedStacksSortWithoutTranslatedPathkeys pins
// today's impotence (input-coordinate keys never match), which is
// what the translation fixes.
func TestOrderedDropsHashedSortForPresortedGrouping(t *testing.T) {
	cp := defaultCostParams()
	// Output-coordinate order key (shared object → pathKeyEqual TRUE).
	orderCol := &ColumnRef{Index: 0, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}
	orderKeys := []PathKey{{Expr: orderCol, SortAsc: true}}
	u := newUpperRels()
	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	ordered.NCols = 2
	ordered.AvgVarBytes = 8
	// Sorted grouping candidate, translated (output-coord) pathkeys.
	sortedAgg := &Path{
		Kind: PathAgg, Cost: Cost{Startup: 69094, Total: 70122}, Rows: 5,
		Pathkeys: []PathKey{{Expr: orderCol, SortAsc: true}},
		Rel: ordered, ParallelSafe: true,
	}
	// Hashed grouping candidate, unordered.
	hashedAgg := &Path{
		Kind: PathAgg, Cost: Cost{Startup: 68909, Total: 69911}, Rows: 5,
		Rel: ordered, ParallelSafe: true,
	}
	addOrderedPaths(ordered, sortedAgg, orderKeys, cp, -1)
	addOrderedPaths(ordered, hashedAgg, orderKeys, cp, -1)
	setCheapest(ordered)
	best := getCheapestFractionalPath(ordered, 0)
	if best == nil {
		t.Fatal("ordered rel has no cheapest path")
	}
	if best.Kind == PathSort {
		t.Fatalf("hashed+Sort won (cost=%.2f); want the no-sort sorted path — startup dominance did not drop it", best.Cost.Total)
	}
}

func TestOrderedStacksSortWithoutTranslatedPathkeys(t *testing.T) {
	cp := defaultCostParams()
	// Input-coordinate group key: same column, different object and
	// index → pathKeyEqual FALSE (positional identity, exprwalk.go).
	// This is today's state: no-sort can never fire across the
	// grouping boundary, so hashed+Sort wins and the test documents it.
	groupCol := &ColumnRef{Index: 3, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}
	orderCol := &ColumnRef{Index: 0, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}
	orderKeys := []PathKey{{Expr: orderCol, SortAsc: true}}
	u := newUpperRels()
	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	ordered.NCols = 2
	ordered.AvgVarBytes = 8
	sortedAgg := &Path{
		Kind: PathAgg, Cost: Cost{Startup: 69094, Total: 70122}, Rows: 5,
		Pathkeys: []PathKey{{Expr: groupCol, SortAsc: true}},
		Rel: ordered, ParallelSafe: true,
	}
	hashedAgg := &Path{
		Kind: PathAgg, Cost: Cost{Startup: 68909, Total: 69911}, Rows: 5,
		Rel: ordered, ParallelSafe: true,
	}
	addOrderedPaths(ordered, sortedAgg, orderKeys, cp, -1)
	addOrderedPaths(ordered, hashedAgg, orderKeys, cp, -1)
	setCheapest(ordered)
	best := getCheapestFractionalPath(ordered, 0)
	if best == nil {
		t.Fatal("ordered rel has no cheapest path")
	}
	if best.Kind != PathSort {
		t.Fatalf("untranslated pathkeys unexpectedly elected no-sort (kind=%d); want hashed+Sort documenting today's gap", best.Kind)
	}
}

// R47 slice 2 (K101) — grouping-emission translation pins. The two slice-1
// tests above encode the gap (input-coordinate pathkeys never satisfy the
// ORDERED check); these pin the helper that closes it for the group-keys
// Sort variant, and every shape that must still decline. Fixtures are
// Q4-shaped: one bpchar group column at input Index 3, aggregate output
// [o_orderpriority | count], ORDER BY the output column.

func r47slice2GroupFixture() (aggNode *Aggregate, groupCol *ColumnRef, outSchema Schema) {
	groupCol = &ColumnRef{Index: 3, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}
	outSchema = Schema{
		{Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}},
		{Name: "count", Type: catalog.Type{Name: "int8"}},
	}
	child := &pricedNode{sch: Schema{
		{Name: "c0", Type: catalog.Type{Name: "int4"}},
		{Name: "c1", Type: catalog.Type{Name: "int4"}},
		{Name: "c2", Type: catalog.Type{Name: "int4"}},
		{Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}},
	}}
	child.setPlanCost(PlanCost{StartupCost: 100, TotalCost: 68909, PlanRows: 57066, PlanWidth: 448})
	aggNode = &Aggregate{Child: child, GroupExprs: []Expr{groupCol}, schema: outSchema}
	return aggNode, groupCol, outSchema
}

func r47slice2SortedCand(spec *Aggregate, childKeys []PathKey, cost Cost) *Path {
	// Hand-built seed: `newPrebuiltPath` dereferences the rel for Rows,
	// and these fixtures run rel-free (the loop tests below file theirs
	// on a real rel). `node` is set so the elect test's build has a
	// wrapped node to return identically.
	seed := &Path{Kind: PathPrebuilt, Rows: 57066, node: spec.Child}
	return &Path{Kind: PathAgg, AggStrategy: AggStrategySorted, Agg: spec,
		Rows: 5, Cost: cost,
		// Production files the candidate with the sort input's
		// (input-coordinate) pathkeys (`Pathkeys:
		// sortedInput.Pathkeys`, groupingpaths.go) — load-bearing:
		// without them the candidate looks unordered and hashed
		// dominates it at the grouping rel.
		Pathkeys: childKeys,
		Children: []*Path{{Kind: PathSort, Pathkeys: childKeys, Rows: 57066, Children: []*Path{seed}}}}
}

// TestGroupingEmissionTranslatesGroupKeysSort: the group-keys Sort variant
// translates to output-coordinate pathkeys that satisfy the ORDER BY check —
// the hand-built input-coordinate key of
// TestOrderedStacksSortWithoutTranslatedPathkeys becomes the passing shape of
// TestOrderedDropsHashedSortForPresortedGrouping.
func TestGroupingEmissionTranslatesGroupKeysSort(t *testing.T) {
	aggNode, groupCol, _ := r47slice2GroupFixture()
	spec := &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: aggNode.schema}
	cand := r47slice2SortedCand(spec, []PathKey{{Expr: groupCol, SortAsc: true}}, Cost{Startup: 69094, Total: 70122})
	got := groupingEmissionPathkeys(aggNode, cand)
	if len(got) != 1 {
		t.Fatalf("translated %d pathkeys, want 1", len(got))
	}
	orderCol := &ColumnRef{Index: 0, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}
	if !pathKeyEqual(got[0], PathKey{Expr: orderCol, SortAsc: true}) {
		t.Fatalf("translated key %+v does not equal the ORDER BY key", got[0])
	}
	if !pathkeysContainedIn(got, []PathKey{{Expr: orderCol, SortAsc: true}}) {
		t.Fatal("translated emission order does not satisfy the ORDER BY requirement")
	}
}

// TestGroupingEmissionCarriesDirection: a descending group-keys Sort emits
// descending — direction/nulls ride from the child PathKeys, never assumed.
func TestGroupingEmissionCarriesDirection(t *testing.T) {
	aggNode, groupCol, _ := r47slice2GroupFixture()
	spec := &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: aggNode.schema}
	cand := r47slice2SortedCand(spec,
		[]PathKey{{Expr: groupCol, SortAsc: false, NullsFirst: true}}, Cost{Startup: 69094, Total: 70122})
	got := groupingEmissionPathkeys(aggNode, cand)
	if len(got) != 1 {
		t.Fatalf("translated %d pathkeys, want 1", len(got))
	}
	if got[0].SortAsc || !got[0].NullsFirst {
		t.Fatalf("direction lost: got asc=%v nullsFirst=%v", got[0].SortAsc, got[0].NullsFirst)
	}
}

// TestGroupingEmissionAcceptsTrailingKeys: a presorted-shaped Sort whose
// leading run covers all groups in order still translates — extra trailing
// keys cannot change group-emergence order, so accepting them is sound
// (SLICE2 §1 step 3 subsumes the presorted variant here).
func TestGroupingEmissionAcceptsTrailingKeys(t *testing.T) {
	aggNode, groupCol, _ := r47slice2GroupFixture()
	extra := &ColumnRef{Index: 1, Name: "c1", Type: catalog.Type{Name: "int4"}}
	spec := &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: aggNode.schema}
	cand := r47slice2SortedCand(spec,
		[]PathKey{{Expr: groupCol, SortAsc: true}, {Expr: extra, SortAsc: true}}, Cost{Startup: 69094, Total: 70122})
	if got := groupingEmissionPathkeys(aggNode, cand); len(got) != 1 {
		t.Fatalf("group-prefixed presorted shape translated %d keys, want 1", len(got))
	}
}

// TestGroupingEmissionDeclines: every untranslatable shape returns nil —
// each row is a wrong-order vector if it ever translated.
func TestGroupingEmissionDeclines(t *testing.T) {
	aggNode, groupCol, outSchema := r47slice2GroupFixture()
	other := &ColumnRef{Index: 1, Name: "c1", Type: catalog.Type{Name: "int4"}}
	baseSpec := func() *Aggregate {
		return &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: outSchema}
	}
	baseCand := func(spec *Aggregate) *Path {
		return r47slice2SortedCand(spec, []PathKey{{Expr: groupCol, SortAsc: true}}, Cost{Startup: 69094, Total: 70122})
	}
	cases := map[string]func() *Path{
		"hashed strategy": func() *Path {
			c := baseCand(baseSpec())
			c.AggStrategy = AggStrategyHashed
			return c
		},
		"prebuilt child": func() *Path {
			c := baseCand(baseSpec())
			c.Children[0] = &Path{Kind: PathPrebuilt, Rows: 57066, node: aggNode.Child}
			return c
		},
		"no child": func() *Path {
			c := baseCand(baseSpec())
			c.Children = nil
			return c
		},
		"nil spec": func() *Path {
			c := baseCand(baseSpec())
			c.Agg = nil
			return c
		},
		"grouping sets": func() *Path {
			s := baseSpec()
			s.GroupingSets = [][]int{{0}}
			return baseCand(s)
		},
		"empty groups": func() *Path {
			s := baseSpec()
			s.GroupExprs = nil
			return baseCand(s)
		},
		"non-simple mode": func() *Path {
			s := baseSpec()
			s.Mode = AggModePartial
			return baseCand(s)
		},
		"index order": func() *Path {
			s := baseSpec()
			s.GroupKeyOrder = []int{0}
			return baseCand(s)
		},
		"expression group key": func() *Path {
			s := baseSpec()
			s.GroupExprs = []Expr{&BinaryOp{Left: groupCol, Right: groupCol}}
			return baseCand(s)
		},
		"leading key is not the group": func() *Path {
			return r47slice2SortedCand(baseSpec(),
				[]PathKey{{Expr: other, SortAsc: true}, {Expr: groupCol, SortAsc: true}}, Cost{})
		},
		"short run": func() *Path {
			s := baseSpec()
			s.GroupExprs = []Expr{groupCol, other}
			return baseCand(s)
		},
	}
	for name, build := range cases {
		if got := groupingEmissionPathkeys(aggNode, build()); got != nil {
			t.Errorf("%s: translated %d keys, want nil", name, len(got))
		}
	}
	// Output-prefix mismatch: the layout is verified, never assumed.
	renamed := *aggNode
	renamedSchema := Schema{
		{Name: "renamed", Type: catalog.Type{Name: "bpchar"}},
		{Name: "count", Type: catalog.Type{Name: "int8"}},
	}
	renamed.schema = renamedSchema
	if got := groupingEmissionPathkeys(&renamed, baseCand(baseSpec())); got != nil {
		t.Errorf("renamed output prefix: translated %d keys, want nil", len(got))
	}
	if got := groupingEmissionPathkeys(nil, baseCand(baseSpec())); got != nil {
		t.Errorf("nil node: translated %d keys, want nil", len(got))
	}
	if got := groupingEmissionPathkeys(aggNode, nil); got != nil {
		t.Errorf("nil candidate: translated %d keys, want nil", len(got))
	}
	notAgg := &Path{Kind: PathSort}
	if got := groupingEmissionPathkeys(aggNode, notAgg); got != nil {
		t.Errorf("non-agg path: translated %d keys, want nil", len(got))
	}
}

// TestElectOrderedGroupingDeclinesPristine: a rel-level decline (here the
// GroupKeyOrder index candidate, untranslatable at helper level, leaves <2
// translatable... precisely: zero translate AND the Finalize-free count gate)
// restores the ORDERED rel byte-identical — Pathlist length and cheapest
// fields unchanged, so decline === the loop never ran.
func TestElectOrderedGroupingDeclinesPristine(t *testing.T) {
	aggNode, groupCol, outSchema := r47slice2GroupFixture()
	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	idxSpec := &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: outSchema, GroupKeyOrder: []int{0}}
	idxSeed := newPrebuiltPath(grouped, aggNode.Child)
	hashedSpec := &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: outSchema}
	addPath(grouped, &Path{Kind: PathAgg, AggStrategy: AggStrategySorted, Agg: idxSpec,
		Rows: 5, Cost: Cost{Startup: 69000, Total: 70000}, Rel: grouped, Children: []*Path{idxSeed}}, "test")
	addPath(grouped, &Path{Kind: PathAgg, AggStrategy: AggStrategyHashed, Agg: hashedSpec,
		Rows: 5, Cost: Cost{Startup: 68909, Total: 69911}, Rel: grouped,
		Children: []*Path{newPrebuiltPath(grouped, aggNode.Child)}}, "test")
	setCheapest(grouped)
	agg := &aggregateSurface{node: aggNode}
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}}}
	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	beforeLen := len(ordered.Pathlist)
	if got, ok := electOrderedGrouping(u, agg, aggNode, keys, 0, DefaultPlannerSettings().costParams(), 0, -1); ok || got != nil {
		t.Fatalf("index+hashed rel elected (ok=%v); want decline", ok)
	}
	after := fetchUpperRel(u, UpperOrdered, 0, 0)
	if len(after.Pathlist) != beforeLen || after.CheapestTotal != nil || after.CheapestStartup != nil {
		t.Fatalf("decline mutated the ORDERED rel: paths %d->%d cheapest=%v/%v",
			beforeLen, len(after.Pathlist), after.CheapestTotal, after.CheapestStartup)
	}
}

// TestElectOrderedGroupingElectsNoSortAtQ4Numbers: with Q4's live costs the
// loop elects the no-sort sorted candidate (the slice-1 pin's election,
// end to end through build + copy-back): elected, bare *Aggregate winner,
// winner spec (sorted strategy) copied back onto agg.node.
func TestElectOrderedGroupingElectsNoSortAtQ4Numbers(t *testing.T) {
	aggNode, groupCol, outSchema := r47slice2GroupFixture()
	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	mkSpec := func() *Aggregate {
		return &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: outSchema}
	}
	sorted := r47slice2SortedCand(mkSpec(), []PathKey{{Expr: groupCol, SortAsc: true}}, Cost{Startup: 69094, Total: 70122})
	sorted.Rel = grouped
	hashed := &Path{Kind: PathAgg, AggStrategy: AggStrategyHashed, Agg: mkSpec(),
		Rows: 5, Cost: Cost{Startup: 68909, Total: 69911}, Rel: grouped,
		Children: []*Path{newPrebuiltPath(grouped, aggNode.Child)}}
	addPath(grouped, sorted, "test")
	addPath(grouped, hashed, "test")
	setCheapest(grouped)
	agg := &aggregateSurface{node: aggNode}
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}}}
	got, ok := electOrderedGrouping(u, agg, aggNode, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if !ok || got == nil {
		t.Fatal("Q4-shaped rel declined; want the no-sort election")
	}
	built, isAgg := got.(*Aggregate)
	if !isAgg {
		t.Fatalf("winner is %T; want bare *Aggregate (no-sort)", got)
	}
	_ = built
	if agg.node.Strategy != AggStrategySorted {
		t.Fatalf("copy-back strategy = %v; want sorted", agg.node.Strategy)
	}
}

// TestGroupingEmissionTranslatesGatherMergeChild: R56's
// worker-sort-under-GatherMerge no-split arm delivers group-key order
// through a `PathGatherMerge` child — a merge emits its inputs' order,
// the same contract the Sort-child variant relies on — so it
// translates identically (direction/nulls ride from the merge keys).
// A merge on other keys declines exactly like a mis-sorted Sort:
// translating it would elect a no-Sort plan emitting unordered groups.
func TestGroupingEmissionTranslatesGatherMergeChild(t *testing.T) {
	aggNode, groupCol, _ := r47slice2GroupFixture()
	mergeKeys := []PathKey{{Expr: groupCol, SortAsc: true}}
	gmChild := &Path{Kind: PathGatherMerge, Pathkeys: mergeKeys, Rows: 57066,
		Children: []*Path{{Kind: PathSort, Pathkeys: mergeKeys, Rows: 14266}}}
	spec := &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: aggNode.schema}
	cand := &Path{Kind: PathAgg, AggStrategy: AggStrategySorted, Agg: spec,
		Rows: 5, Cost: Cost{Startup: 69094, Total: 70122},
		Pathkeys: mergeKeys, Children: []*Path{gmChild}}
	got := groupingEmissionPathkeys(aggNode, cand)
	if len(got) != 1 {
		t.Fatalf("gathermerge child translated %d pathkeys, want 1", len(got))
	}
	orderCol := &ColumnRef{Index: 0, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}
	if !pathKeyEqual(got[0], PathKey{Expr: orderCol, SortAsc: true}) {
		t.Fatalf("translated key %+v does not equal the ORDER BY key", got[0])
	}
	// Wrong merge keys decline: the merge does not deliver group order.
	other := &ColumnRef{Index: 1, Name: "c1", Type: catalog.Type{Name: "int4"}}
	badKeys := []PathKey{{Expr: other, SortAsc: true}}
	badGM := &Path{Kind: PathGatherMerge, Pathkeys: badKeys, Rows: 57066,
		Children: []*Path{{Kind: PathSort, Pathkeys: badKeys, Rows: 14266}}}
	bad := &Path{Kind: PathAgg, AggStrategy: AggStrategySorted, Agg: spec,
		Rows: 5, Cost: Cost{Startup: 69094, Total: 70122},
		Pathkeys: badKeys, Children: []*Path{badGM}}
	if got := groupingEmissionPathkeys(aggNode, bad); got != nil {
		t.Fatalf("gathermerge on other keys translated %d keys, want nil", len(got))
	}
}
