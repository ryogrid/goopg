package optimizer

// M0127-P5.7-b — the LIMIT fraction. Five claims:
//
//  1. `preprocessLimit` reproduces `preprocess_limit`'s arms, including the
//     ones only a caller-supplied fraction can reach.
//  2. `compareFractionalPathCosts` folds an out-of-range fraction onto the
//     plain total-cost order rather than mis-answering it.
//  3. `getCheapestFractionalPath` converts an ABSOLUTE count against the rel's
//     own rows, and never answers with a parameterised path.
//  4. `consider_startup` decides RETENTION: without a fraction, a path that
//     wins only on startup cost is not kept at all.
//  5. …and the two halves together MOVE THE CHOSEN PATH of a LIMIT-over-join,
//     which is the whole point — P5.7-a's Startup/Total split was inert until
//     something selected on it.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// limitNode builds the `*Limit` the search's caller would hand
// `preprocessLimit`. A nil expression means the clause is absent.
func limitNode(count, offset Expr) *Limit {
	return &Limit{Limit: count, Offset: offset}
}

func intConst(v int64) Expr { return &IntegerConst{Value: v} }

// TestPreprocessLimitMatchesUpstreamArms walks preprocess_limit (planner.c:2577)
// arm by arm. The caller-supplied fraction cases have no goopg caller yet
// (every call site passes 0) and are tested anyway: they are where upstream's
// absolute-vs-fractional heuristics live, and a port that only covered the
// zero-caller case would look correct while quietly dropping them.
func TestPreprocessLimitMatchesUpstreamArms(t *testing.T) {
	cases := []struct {
		name     string
		lim      *Limit
		caller   float64
		want     float64
		wantCnt  int64
		wantOffs int64
	}{
		{"no limit node at all", nil, 0, 0, 0, 0},
		{"no limit node keeps the caller's fraction", nil, 0.25, 0.25, 0, 0},
		// LIMIT is an ABSOLUTE count, and OFFSET adds to it: the skipped rows
		// still have to be produced (planner.c:2646-2650).
		{"LIMIT 10", limitNode(intConst(10), nil), 0, 10, 10, 0},
		{"LIMIT 10 OFFSET 5", limitNode(intConst(10), intConst(5)), 0, 15, 10, 5},
		// LIMIT ALL is a null count, which upstream treats as "not present".
		{"LIMIT ALL", limitNode(&NullConst{}, nil), 0, 0, 0, 0},
		// A zero or negative LIMIT is forced to 1 (:2604): it is still a
		// limit, and 0 would read as "no clause at all".
		{"LIMIT 0 is forced to 1", limitNode(intConst(0), nil), 0, 1, 1, 0},
		{"LIMIT -3 is forced to 1", limitNode(intConst(-3), nil), 0, 1, 1, 0},
		// A negative OFFSET is treated as absent, as the executor treats it.
		{"OFFSET -1 is not present", limitNode(intConst(4), intConst(-1)), 0, 4, 4, 0},
		// Not a constant: the 10% punt (:2641).
		{"non-constant LIMIT punts to 10%", limitNode(&ColumnRef{}, nil), 0, unestimatableLimitFraction, -1, 0},
		{"non-constant OFFSET punts too", limitNode(intConst(10), &ColumnRef{}), 0, unestimatableLimitFraction, 10, -1},
		// Caller absolute + limit absolute: the smaller (:2660).
		{"both absolute takes the smaller", limitNode(intConst(50), nil), 20, 20, 50, 0},
		{"both absolute takes the smaller (limit)", limitNode(intConst(5), nil), 20, 5, 5, 0},
		// Caller fractional + limit absolute: the limit wins (:2673).
		{"caller fractional, limit absolute", limitNode(intConst(7), nil), 0.3, 7, 7, 0},
		// Caller absolute + limit fractional: keep the caller's (:2666).
		{"caller absolute, limit fractional", limitNode(&ColumnRef{}, nil), 25, 25, -1, 0},
		// OFFSET alone with no caller fraction changes NOTHING: with no limit
		// the whole result is fetched anyway (:2690's guard).
		{"OFFSET alone, no caller fraction", limitNode(nil, intConst(100)), 0, 0, 0, 100},
		// OFFSET alone INCREASES a caller fraction — the opposite direction
		// from LIMIT, because more rows must be fetched, not fewer (:2697).
		{"OFFSET alone raises an absolute caller count", limitNode(nil, intConst(100)), 20, 120, 0, 100},
		{"OFFSET alone, caller fractional, offset absolute", limitNode(nil, intConst(100)), 0.3, 0.3, 0, 100},
		// Both fractional and summing past 1 means "fetch everything" (:2726).
		{"both fractional summing past 1 means all rows", limitNode(nil, &ColumnRef{}), 0.95, 0, 0, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, est := preprocessLimit(tc.lim, tc.caller)
			if got != tc.want {
				t.Errorf("tuple fraction = %v, want %v", got, tc.want)
			}
			if est.count != tc.wantCnt || est.offset != tc.wantOffs {
				t.Errorf("estimates = {count:%d offset:%d}, want {count:%d offset:%d}",
					est.count, est.offset, tc.wantCnt, tc.wantOffs)
			}
		})
	}
}

// TestCompareFractionalPathCostsFoldsOutOfRangeOntoTotal pins the property that
// makes the comparator safe to hand an unconverted absolute count: a fraction
// outside (0,1) means "all rows" and falls back on the total-cost order
// (pathnode.c:142), rather than extrapolating the cost line past its endpoint.
func TestCompareFractionalPathCostsFoldsOutOfRangeOntoTotal(t *testing.T) {
	fast := &Path{Cost: Cost{Startup: 0, Total: 1000}}   // cheap to start
	whole := &Path{Cost: Cost{Startup: 100, Total: 200}} // cheap to finish

	for _, f := range []float64{0, -1, 1.0, 5.0} {
		if got := compareFractionalPathCosts(fast, whole, f); got != +1 {
			t.Errorf("fraction %v: got %d, want +1 (the total-cost order)", f, got)
		}
	}
	// Inside the range the answer flips where the two cost lines cross:
	// fast is 0+f*1000, whole is 100+f*100, equal at f = 1/9.
	if got := compareFractionalPathCosts(fast, whole, 0.05); got != -1 {
		t.Errorf("at 5%% the fast-start path must win, got %d", got)
	}
	if got := compareFractionalPathCosts(fast, whole, 0.5); got != +1 {
		t.Errorf("at 50%% the cheap-total path must win, got %d", got)
	}
	// disabled_nodes trumps the fraction entirely (:133).
	disabled := &Path{Cost: Cost{Startup: 0, Total: 1}, DisabledNodes: 1}
	if got := compareFractionalPathCosts(disabled, whole, 0.05); got != +1 {
		t.Errorf("a disabled node must lose at any fraction, got %d", got)
	}
}

// TestGetCheapestFractionalPathAbsoluteAndParameterised covers the two rules in
// get_cheapest_fractional_path that are not the comparison itself: the absolute
// count is converted HERE, against this rel's rows (planner.c:6627), and a
// parameterised path is never the answer (:6634) because it only produces rows
// once some outer relation supplies its parameter.
func TestGetCheapestFractionalPathAbsoluteAndParameterised(t *testing.T) {
	rel := newRelOptInfo(relsetOf(0), 1000, 32)
	rel.ConsiderStartup = true
	cheapTotal := &Path{Kind: PathSeqScan, Rel: rel, Rows: 1000, Cost: Cost{Startup: 100, Total: 200}}
	fastStart := &Path{Kind: PathIndexScan, Rel: rel, Rows: 1000, Cost: Cost{Startup: 0, Total: 1000}}
	addPath(rel, cheapTotal, "test")
	addPath(rel, fastStart, "test")
	setCheapest(rel)
	if rel.CheapestTotal != cheapTotal {
		t.Fatalf("fixture broken: cheapest-total is %v", rel.CheapestTotal.Kind)
	}

	// "All rows" returns the cheapest-total path untouched — the pre-P5.7-b
	// answer, byte for byte.
	if got := getCheapestFractionalPath(rel, 0); got != cheapTotal {
		t.Errorf("fraction 0 must return the cheapest-total path")
	}
	// LIMIT 10 of 1000 rows is 1%, where fastStart costs 10 and cheapTotal
	// 101. The absolute count must be divided by the rel's rows to see that.
	if got := getCheapestFractionalPath(rel, 10); got != fastStart {
		t.Errorf("LIMIT 10 of 1000 rows must choose the fast-start path")
	}
	// LIMIT 900 is 90%, well past the crossover.
	if got := getCheapestFractionalPath(rel, 900); got != cheapTotal {
		t.Errorf("LIMIT 900 of 1000 rows must choose the cheapest-total path")
	}

	// A parameterised path cheaper than either at the fraction is still not an
	// answer: nothing above can bind its outer.
	param := &Path{Kind: PathIndexScan, Rel: rel, Rows: 1, RequiredOuter: relsetOf(1),
		Cost: Cost{Startup: 0, Total: 1}}
	addPath(rel, param, "test")
	setCheapest(rel)
	if got := getCheapestFractionalPath(rel, 10); got == param {
		t.Errorf("a parameterised path must never be the fractional answer")
	}
}

// TestConsiderStartupGatesFastStartRetention is the RETENTION half, and it is
// what makes the selection half reachable: without a tuple fraction, add_path
// does not keep a path whose only merit is a cheap start
// (compare_path_costs_fuzzily's policy rule, pathnode.c:178-183), so there is
// nothing for a fraction to choose even if one arrives later.
func TestConsiderStartupGatesFastStartRetention(t *testing.T) {
	add := func(considerStartup bool) []*Path {
		rel := newRelOptInfo(relsetOf(0), 1000, 32)
		rel.ConsiderStartup = considerStartup
		addPath(rel, &Path{Kind: PathSeqScan, Rel: rel, Rows: 1000, Cost: Cost{Startup: 100, Total: 200}}, "test")
		addPath(rel, &Path{Kind: PathIndexScan, Rel: rel, Rows: 1000, Cost: Cost{Startup: 0, Total: 1000}}, "test")
		return rel.Pathlist
	}
	if got := add(false); len(got) != 1 || got[0].Kind != PathSeqScan {
		t.Errorf("with no tuple fraction the fast-start path must be pruned, kept %d paths", len(got))
	}
	if got := add(true); len(got) != 2 {
		t.Errorf("under a tuple fraction both paths must survive, kept %d", len(got))
	}
}

// tfSearch runs a two-relation search whose builder offers each joinrel BOTH
// shapes the fraction has to choose between — a hash-like path that must build
// before it can emit, and a loop-like path that emits immediately but costs far
// more to run to completion. `lim` is the query's LIMIT clause.
//
// It goes through `buildInitialRels` rather than hand-building the context
// because the flag under test is set there, from the fraction: a test that set
// `ConsiderStartup` itself would be asserting on its own fixture.
func tfSearch(t *testing.T, lim *Limit) *searchCtx {
	t.Helper()
	fraction, _ := preprocessLimit(lim, 0)

	leaves := []Node{
		&SeqScan{Table: &catalog.Table{Name: "a"}},
		&SeqScan{Table: &catalog.Table{Name: "b"}},
	}
	s, err := buildInitialRels(
		[]rangeBinding{{}, {}},
		leaves,
		[]baseRelInfo{{filteredRows: 1000}, {filteredRows: 1000}},
		defaultCostParams(),
		fraction,
		nil,
	)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}
	if _, err := s.joinSearch(jslClauses(relsetOf(0)|relsetOf(1)), &twoShapeBuilder{}); err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	return s
}

// twoShapeBuilder adds one cheap-total/dear-startup path and one
// cheap-startup/dear-total path per direction, which is the shape of every real
// hash-vs-nested-loop choice.
type twoShapeBuilder struct{}

func (b *twoShapeBuilder) sizeJoinRel(outer, inner *RelOptInfo, _ []*restrictInfo, _ *SpecialJoinInfo) (float64, int) {
	return 10000, outer.Width + inner.Width
}

func (b *twoShapeBuilder) addPaths(joinrel, outer, inner *RelOptInfo, _ []*restrictInfo, _ *SpecialJoinInfo) error {
	addPath(joinrel, &Path{Kind: PathHashJoin, Rel: joinrel, Rows: joinrel.Rows,
		Cost: Cost{Startup: 500, Total: 900}}, "test")
	addPath(joinrel, &Path{Kind: PathNestLoop, Rel: joinrel, Rows: joinrel.Rows,
		Cost: Cost{Startup: 0, Total: 20000}}, "test")
	return nil
}

// TestLimitOverJoinMovesTheChosenPath is the claim the task exists for. The two
// shapes cross at 500 + f*400 = f*20000, i.e. f ≈ 2.55%, so of 10 000 join rows
// `LIMIT 100` (1%) wants the loop and `LIMIT 5000` (50%) wants the hash — and
// with no LIMIT at all the loop is not even retained.
func TestLimitOverJoinMovesTheChosenPath(t *testing.T) {
	// No LIMIT: the loop never enters the pathlist, and the chosen path is the
	// cheapest-total one, exactly as before this task.
	noLimit := tfSearch(t, nil)
	rel, err := noLimit.finalRel()
	if err != nil {
		t.Fatalf("finalRel: %v", err)
	}
	for _, p := range rel.Pathlist {
		if p.Kind == PathNestLoop {
			t.Fatalf("with no tuple fraction the fast-start loop must not be retained")
		}
	}
	chosen, err := noLimit.finalPath()
	if err != nil {
		t.Fatalf("finalPath: %v", err)
	}
	if chosen != rel.CheapestTotal || chosen.Kind != PathHashJoin {
		t.Fatalf("with no LIMIT the search must choose the cheapest-total hash path, got %v", chosen.Kind)
	}

	// LIMIT 100: the loop is retained AND selected — the cheapest way to the
	// first 100 rows is not the cheapest way to all 10 000.
	small := tfSearch(t, limitNode(intConst(100), nil))
	smallRel, err := small.finalRel()
	if err != nil {
		t.Fatalf("finalRel: %v", err)
	}
	chosen, err = small.finalPath()
	if err != nil {
		t.Fatalf("finalPath: %v", err)
	}
	if chosen.Kind != PathNestLoop {
		t.Fatalf("under LIMIT 100 the search must choose the fast-start loop, got %v", chosen.Kind)
	}
	if chosen == smallRel.CheapestTotal {
		t.Fatalf("the LIMIT must move the choice OFF the cheapest-total path")
	}

	// LIMIT 5000 of the same 10 000 rows is past the crossover: the fraction
	// is honoured, not merely "a LIMIT exists".
	big := tfSearch(t, limitNode(intConst(5000), nil))
	chosen, err = big.finalPath()
	if err != nil {
		t.Fatalf("finalPath: %v", err)
	}
	if chosen.Kind != PathHashJoin {
		t.Fatalf("under LIMIT 5000 the hash path must win again, got %v", chosen.Kind)
	}
}
