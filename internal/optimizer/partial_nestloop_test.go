package optimizer

// R94 (plan-parity-fix-take2): ordinary INNER nested-loop outer partition.
//
// Pins the three-way agreement the slice turns on — path classifier
// (partialPathDrivingKind), node predicate (nestedLoopJoinIsPartialCapable)
// plus its four walks, and the R60 producer filing — together, so no arm
// admits a shape another refuses.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// nlTestJoin builds the node shape under test: an ordinary *Join with two
// SeqScan children (never an NLI node, never parameterized).
func nlTestJoin(algo JoinAlgo, typ JoinType, lateral bool) *Join {
	outer := &SeqScan{schema: Schema{{Name: "a"}}}
	inner := &SeqScan{schema: Schema{{Name: "b"}}}
	return &Join{Algo: algo, Type: typ, Lateral: lateral, Left: outer, Right: inner}
}

func TestNestedLoopJoinIsPartialCapable(t *testing.T) {
	approved := nlTestJoin(JoinAlgoNestedLoop, JoinTypeInner, false)
	if !nestedLoopJoinIsPartialCapable(approved) {
		t.Fatal("ordinary INNER nested loop must be partial-capable")
	}
	refusals := map[string]*Join{
		"nil":        nil,
		"hash-inner": {Algo: JoinAlgoHash, Type: JoinTypeInner, Left: &SeqScan{}, Right: &SeqScan{}},
		"merge-inner": {Algo: JoinAlgoMerge, Type: JoinTypeInner, Left: &SeqScan{}, Right: &SeqScan{}},
		"nl-left":    nlTestJoin(JoinAlgoNestedLoop, JoinTypeLeft, false),
		"nl-right":   nlTestJoin(JoinAlgoNestedLoop, JoinTypeRight, false),
		"nl-full":    nlTestJoin(JoinAlgoNestedLoop, JoinTypeFull, false),
		"nl-semi":    nlTestJoin(JoinAlgoNestedLoop, JoinTypeSemi, false),
		"nl-anti":    nlTestJoin(JoinAlgoNestedLoop, JoinTypeAnti, false),
		"nl-lateral": nlTestJoin(JoinAlgoNestedLoop, JoinTypeInner, true),
		"nl-nil-left": {Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Right: &SeqScan{}},
		"nl-nil-right": {Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Left: &SeqScan{}},
	}
	for name, j := range refusals {
		if nestedLoopJoinIsPartialCapable(j) {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestPartialNLWalkAgreement pins the four walks to the same decision on an
// approved tree: eligibility, label, unstamping, and the sort-crossing guard.
func TestPartialNLWalkAgreement(t *testing.T) {
	outer := &SeqScan{schema: Schema{{Name: "a"}}}
	inner := &SeqScan{schema: Schema{{Name: "b"}}}
	n := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Left: outer, Right: inner}

	if got := drivingScan(n); got != Node(outer) {
		t.Fatalf("drivingScan must reach the OUTER scan, got %T", got)
	}
	if drivingScanCrossesSort(n) {
		t.Fatal("no Sort on the spine: crossesSort must be false")
	}
	stamped := stampParallelScan(n)
	sj, ok := stamped.(*Join)
	if !ok {
		t.Fatalf("stamp must copy the Join, got %T", stamped)
	}
	if sj.Left == Node(outer) {
		t.Fatal("stamp must copy on write, not stamp in place")
	}
	ss, ok := sj.Left.(*SeqScan)
	if !ok || !ss.Parallel {
		t.Fatalf("stamp must label the outer scan Parallel, got %T", sj.Left)
	}
	if _, ok := sj.Right.(*SeqScan); !ok {
		t.Fatal("stamp must leave the inner scan in place")
	}
	if is, ok := sj.Right.(*SeqScan); ok && is.Parallel {
		t.Error("stamp must never label the inner scan")
	}
	// The original tree is untouched.
	if outer.Parallel {
		t.Fatal("stamp mutated the input tree")
	}
	unstamped := unstampParallelScan(stamped)
	uj, ok := unstamped.(*Join)
	if !ok {
		t.Fatalf("unstamp must return a Join, got %T", unstamped)
	}
	if us, ok := uj.Left.(*SeqScan); !ok || us.Parallel {
		t.Error("unstamp must clear the outer Parallel label (StripGather path)")
	}

	// Sort between join and scan: eligibility sees through, crossing reads true.
	sorted := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner,
		Left: &Sort{Child: outer}, Right: inner}
	if drivingScan(sorted) == nil {
		t.Fatal("drivingScan must see through a per-worker Sort")
	}
	if !drivingScanCrossesSort(sorted) {
		t.Fatal("crossesSort must read true through the Sort")
	}

	// Every refusal pins all four walks at once. (Hash INNER is NOT a
	// refusal — the hash twin admits it; hash RIGHT is the refused one.)
	for name, j := range map[string]*Join{
		"right": nlTestJoin(JoinAlgoNestedLoop, JoinTypeRight, false),
		"semi":  nlTestJoin(JoinAlgoNestedLoop, JoinTypeSemi, false),
		"lateral": nlTestJoin(JoinAlgoNestedLoop, JoinTypeInner, true),
		"hash-right": {Algo: JoinAlgoHash, Type: JoinTypeRight, Left: outer, Right: inner},
	} {
		if drivingScan(j) != nil {
			t.Errorf("%s: drivingScan must refuse", name)
		}
		if got := stampParallelScan(j); got != Node(j) {
			t.Errorf("%s: stamp must return the tree unchanged", name)
		}
		if drivingScanCrossesSort(j) {
			t.Errorf("%s: crossesSort must be false on refusal", name)
		}
	}
}

// nlPartialOuter builds an outer child path the classifier accepts.
func nlPartialOuter(relids RelSet) *RelOptInfo {
	rel := scanRel(relids, 10000, 100)
	rel.PartialPathlist = []*Path{{
		Kind: PathSeqScan, Rel: rel, Rows: 5000,
		Cost:               Cost{Total: 100},
		ParallelSafe:       true,
		ParallelWorkers:    2,
	}}
	return rel
}

func nlClassifyFixture() (joinrel, outer, inner *RelOptInfo) {
	a, b := relsetOf(0), relsetOf(1)
	outer = nlPartialOuter(a)
	inner = scanRel(b, 500, 5)
	setCheapest(inner)
	joinrel = newRelOptInfo(a|b, 5000, 64)
	joinrel.Pathlist = nil
	return joinrel, outer, inner
}

func nlClassifyPath(joinrel, outer, inner *RelOptInfo, jt parser.JoinType) *Path {
	return &Path{
		Kind: PathNestLoop, Jointype: jt, Rel: joinrel,
		Rows: 2500, Cost: Cost{Total: 300},
		Children:          []*Path{outer.PartialPathlist[0], inner.CheapestTotal},
		RequiredOuter:     0,
		ParallelSafe:      true,
		ParallelWorkers:   2,
	}
}

func TestPartialPathDrivingKindNestLoop(t *testing.T) {
	joinrel, outer, inner := nlClassifyFixture()
	_ = outer
	if got := partialPathDrivingKind(nlClassifyPath(joinrel, outer, inner, parser.JoinInner)); got != PathSeqScan {
		t.Fatalf("approved INNER NL must drive on the outer scan, got %v", got)
	}

	// Refusal matrix: each mutation flips exactly one predicate.
	cases := map[string]func(p *Path){
		"semi-jointype":  func(p *Path) { p.Jointype = parser.JoinSemi },
		"left-jointype":  func(p *Path) { p.Jointype = parser.JoinLeft },
		"root-param":     func(p *Path) { p.RequiredOuter = relsetOf(0) },
		"one-child":      func(p *Path) { p.Children = p.Children[:1] },
		"zero-workers":   func(p *Path) { p.Children[0].ParallelWorkers = 0 },
		"unsafe-outer":   func(p *Path) { p.Children[0].ParallelSafe = false },
		"param-outer":    func(p *Path) { p.Children[0].RequiredOuter = relsetOf(1) },
		"param-inner":    func(p *Path) { p.Children[1].RequiredOuter = relsetOf(0) },
		"memoize-inner":  func(p *Path) { p.Children[1].Kind = PathMemoize },
	}
	for name, mutate := range cases {
		jr, o, i := nlClassifyFixture()
		p := nlClassifyPath(jr, o, i, parser.JoinInner)
		mutate(p)
		if got := partialPathDrivingKind(p); got != PathPrebuilt {
			t.Errorf("%s: must refuse, got %v", name, got)
		}
	}
	if partialPathDrivingKind(nil) != PathPrebuilt {
		t.Error("nil path must refuse")
	}
}

// TestPartialNLFilingInnerOnly pins R60's narrowed V1 gate: only INNER is
// filed, so a refused head can never starve admittable siblings (the
// gather-path reader takes PartialPathlist[0] only).
func TestPartialNLFilingInnerOnly(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsAll)()
		cp := defaultCostParams()
		a, b := relsetOf(0), relsetOf(1)
		for _, jt := range []parser.JoinType{
			parser.JoinInner, parser.JoinLeft, parser.JoinSemi, parser.JoinAnti,
		} {
			outer := nlPartialOuter(a)
			// ParallelSafe is stamped at path creation: the flag must be
			// set BEFORE generateScanPaths runs.
			inner := newRelOptInfo(b, 500, 32)
			inner.ConsiderParallel = true
			generateScanPaths(inner, defaultCostParams(), 5, 0, 0, true)
			setCheapest(inner)
			joinrel := newRelOptInfo(a|b, 5000, 64)
			joinrel.ConsiderParallel = true
			s := &searchCtx{parallelModeOK: true}
			clauses := []*restrictInfo{equiClause(a, b)}
			addPartialNestLoopPaths(s, joinrel, outer, inner, cp, jt, clauses)
			if jt == parser.JoinInner {
				if len(joinrel.PartialPathlist) == 0 {
					t.Error("INNER partial NL must be filed")
				}
				continue
			}
			if len(joinrel.PartialPathlist) != 0 {
				t.Errorf("%v: non-INNER partial NL must not be filed", jt)
			}
		}
	})
}
