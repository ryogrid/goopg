package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/executor/hashsize"
	"github.com/goopg/goopg/internal/parser"
)

// r90UniqueFixture is a two-relation INNER orientation with a composite bare
// unique index on the complete build side. Its output is deliberately half the
// outer rows so the total-coordinate match fraction is easy to inspect.
func r90UniqueFixture(t *testing.T) (*searchCtx, *RelOptInfo, *RelOptInfo, *RelOptInfo, []*restrictInfo) {
	t.Helper()
	c, partsupp, lineitem := jrsCatalog(t)
	if _, err := c.CreateIndex(parser.ObjectName{Name: "partsupp_ab_uq"}, partsupp,
		[]string{"ps_partkey", "ps_suppkey"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	s := jrsCtx(t, lineitem, partsupp)
	s.cat = c
	outer, inner := jrsRels(100, 40)
	inner.CheapestTotal = &Path{Rel: inner, Rows: inner.Rows}
	joinrel := newRelOptInfo(outer.Relids|inner.Relids, 50, outer.Width+inner.Width)
	keys := []*restrictInfo{
		jrsEq("l_partkey", "ps_partkey", noEquivClass),
		jrsEq("l_suppkey", "ps_suppkey", noEquivClass),
	}
	return s, joinrel, outer, inner, keys
}

func TestHashJoinFinalCostInputProvesCompleteBareUniqueInner(t *testing.T) {
	s, joinrel, outer, inner, keys := r90UniqueFixture(t)
	got := s.hashJoinFinalCostInputFor(joinrel, outer, inner, parser.JoinInner, keys)
	if !got.innerUnique || got.outerMatchFrac != 0.5 {
		t.Fatalf("final input = %+v, want unique inner with total-coordinate fraction 0.5", got)
	}
}

func TestHashJoinFinalCostInputEvaluatesTwoUniqueOrientationsIndependently(t *testing.T) {
	s, joinrel, outer, inner, keys := r90UniqueFixture(t)
	lineitem := s.relInfos[0].table
	if _, err := s.cat.CreateIndex(parser.ObjectName{Name: "lineitem_ab_uq"}, lineitem,
		[]string{"l_partkey", "l_suppkey"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	outer.CheapestTotal = &Path{Rel: outer, Rows: outer.Rows}

	forward := s.hashJoinFinalCostInputFor(joinrel, outer, inner, parser.JoinInner, keys)
	reverse := s.hashJoinFinalCostInputFor(joinrel, inner, outer, parser.JoinInner, keys)
	if !forward.innerUnique || forward.outerMatchFrac != 0.5 {
		t.Fatalf("forward input = %+v, want unique inner and 0.5", forward)
	}
	if !reverse.innerUnique || reverse.outerMatchFrac != 1 {
		t.Fatalf("reverse input = %+v, want unique inner and clamped 1", reverse)
	}
}

func TestHashJoinFinalCostInputDeclinesUnsoundEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*searchCtx, *RelOptInfo, *RelOptInfo, *RelOptInfo, []*restrictInfo) parser.JoinType
	}{
		{
			name: "LEFT join",
			mut:  func(_ *searchCtx, _, _, _ *RelOptInfo, _ []*restrictInfo) parser.JoinType { return parser.JoinLeft },
		},
		{
			name: "incomplete composite",
			mut: func(_ *searchCtx, _, _, _ *RelOptInfo, keys []*restrictInfo) parser.JoinType {
				keys[1] = nil
				return parser.JoinInner
			},
		},
		{
			name: "expression key",
			mut: func(_ *searchCtx, _, _, _ *RelOptInfo, keys []*restrictInfo) parser.JoinType {
				keys[0].leftKey = &BinaryOp{Op: parser.OpAdd, Left: keys[0].leftKey, Right: &IntegerConst{Value: 1}}
				return parser.JoinInner
			},
		},
		{
			name: "joined inner",
			mut: func(_ *searchCtx, _, _, inner *RelOptInfo, _ []*restrictInfo) parser.JoinType {
				inner.Relids |= relsetOf(2)
				return parser.JoinInner
			},
		},
		{
			name: "parameterized inner",
			mut: func(_ *searchCtx, _, _, inner *RelOptInfo, _ []*restrictInfo) parser.JoinType {
				inner.CheapestTotal.RequiredOuter = relsetOf(0)
				return parser.JoinInner
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, joinrel, outer, inner, keys := r90UniqueFixture(t)
			jt := tc.mut(s, joinrel, outer, inner, keys)
			if got := s.hashJoinFinalCostInputFor(joinrel, outer, inner, jt, keys); got.innerUnique {
				t.Fatalf("final input = %+v; unsound evidence must decline", got)
			}
		})
	}
}

func TestHashJoinFinalCostInputDeclinesPartialUniqueIndex(t *testing.T) {
	s, joinrel, outer, inner, keys := r90UniqueFixture(t)
	partial := false
	for _, idx := range s.cat.IndexesOnTable(s.relInfos[1].table) {
		if idx != nil && idx.Unique && len(idx.Columns) == 2 {
			idx.HasPredicate = true
			partial = true
		}
	}
	if !partial {
		t.Fatal("fixture has no composite unique index to mark partial")
	}
	if got := s.hashJoinFinalCostInputFor(joinrel, outer, inner, parser.JoinInner, keys); got != (hashJoinFinalCostInput{}) {
		t.Fatalf("partial unique input = %+v, want zero-value old-cost path", got)
	}
}

func TestHashJoinInnerUniqueFinalCostUsesRoundToEvenWhenGeometryDeclines(t *testing.T) {
	cp := defaultCostParams()
	base := hashJoinInputs{
		outerRows: 5, innerRows: 10, numHashClauses: 1,
		innerBucketSize: 0.1,
	}
	unique := base
	unique.final = hashJoinFinalCostInput{innerUnique: true, outerMatchFrac: 0.5}

	// rint(5 * .5) is 2, not 3. Width zero declines R91's PG virtual
	// geometry, so this pins R90's matched term in isolation.
	build := (cp.cpuOperatorCost + cp.cpuTupleCost) * 10
	wantUnique := build + cp.cpuOperatorCost*5 + cp.cpuOperatorCost*2*1*0.5
	if got := hashJoinCost(cp, unique).Total; math.Abs(got-wantUnique) > 1e-12 {
		t.Fatalf("unique total = %.12g, want %.12g", got, wantUnique)
	}

	// The zero-value final input is byte-for-byte the old non-unique formula.
	wantOrdinary := build + cp.cpuOperatorCost*5 + cp.cpuOperatorCost*5*1*0.5
	if got := hashJoinCost(cp, base).Total; math.Abs(got-wantOrdinary) > 1e-12 {
		t.Fatalf("ordinary total = %.12g, want %.12g", got, wantOrdinary)
	}
}

func TestHashJoinInnerUniqueWithoutBucketStatsAddsUnmatchedVirtualBucketCost(t *testing.T) {
	cp := defaultCostParams()
	ordinary := hashJoinInputs{outerRows: 9, innerRows: 7, outputRows: 4, numHashClauses: 1, innerWidth: 48}
	unique := ordinary
	unique.final = hashJoinFinalCostInput{innerUnique: true, outerMatchFrac: 0.5}
	// rint(9*.5)=4, leaving five unmatched probes. Seven inner rows over
	// the 1024-bucket floor clamp to one tuple; no bucket statistic exists,
	// so this is the unmatched-only PG final-cost delta.
	want := hashJoinCost(cp, ordinary)
	want.Total += cp.cpuOperatorCost * 5 * 1 * 0.05
	if got := hashJoinCost(cp, unique); math.Abs(got.Total-want.Total) > 1e-12 || got.Startup != want.Startup {
		t.Fatalf("unique no-stats cost = %+v, want unmatched-only %+v", got, want)
	}
}

func TestHashJoinInnerUniqueUsesSharedTotalCoordinateFractionForPartialOuter(t *testing.T) {
	cp := defaultCostParams()
	final := hashJoinFinalCostInput{innerUnique: true, outerMatchFrac: 0.5}
	serial := hashJoinInputs{outerRows: 10, innerRows: 10, numHashClauses: 1, innerBucketSize: 0.1, innerWidth: 48, final: final}
	partial := serial
	partial.outerRows = 3

	// The serial candidate sees rint(10*.5)=5; the partial candidate sees
	// rint(3*.5)=2. Both multiply the same rel-level .5, with no second worker
	// divisor and with the complete ten-row inner build retained.
	build := (cp.cpuOperatorCost + cp.cpuTupleCost) * 10
	wantSerial := build + cp.cpuOperatorCost*10 + cp.cpuOperatorCost*5*1*0.5 + cp.cpuOperatorCost*5*1*0.05
	wantPartial := build + cp.cpuOperatorCost*3 + cp.cpuOperatorCost*2*1*0.5 + cp.cpuOperatorCost*1*1*0.05
	if got := hashJoinCost(cp, serial).Total; math.Abs(got-wantSerial) > 1e-12 {
		t.Fatalf("serial total = %.12g, want %.12g", got, wantSerial)
	}
	if got := hashJoinCost(cp, partial).Total; math.Abs(got-wantPartial) > 1e-12 {
		t.Fatalf("partial total = %.12g, want %.12g", got, wantPartial)
	}
}

func TestHashJoinFinalCostInputPreservesNonInnerJoinTypes(t *testing.T) {
	s, joinrel, outer, inner, keys := r90UniqueFixture(t)
	base := hashJoinInputs{
		outerRows: 100, innerRows: 40, numHashClauses: len(keys),
		innerBucketSize: 0.1, innerWidth: 48,
	}
	want := hashJoinCost(s.cp, base)
	for _, jt := range []parser.JoinType{parser.JoinLeft, parser.JoinSemi, parser.JoinAnti} {
		if got := s.hashJoinFinalCostInputFor(joinrel, outer, inner, jt, keys); got != (hashJoinFinalCostInput{}) {
			t.Fatalf("jointype %v input = %+v, want zero-value preservation", jt, got)
		}
		candidate := base
		candidate.final = s.hashJoinFinalCostInputFor(joinrel, outer, inner, jt, keys)
		if got := hashJoinCost(s.cp, candidate); got != want {
			t.Fatalf("jointype %v final cost = %+v, want unchanged %+v", jt, got, want)
		}
	}
}

func TestHashJoinUniqueVirtualGeometryLeavesExecutorSpillCostUnchanged(t *testing.T) {
	cp := defaultCostParams()
	cp.workMem = 64 << 10
	base := hashJoinInputs{
		outerRows: 100, innerRows: 100000, numHashClauses: 1,
		innerBucketSize: 0.01, innerCols: 4, innerAvgVarBytes: 32,
		final: hashJoinFinalCostInput{innerUnique: true, outerMatchFrac: 1},
	}
	// The valid PG geometries are deliberately different, but every outer row
	// matches, so R91's unmatched term is zero. Thus an exact cost identity
	// proves OutputWidth cannot leak into the executor's spill currency.
	narrow, wide := base, base
	narrow.innerWidth, wide.innerWidth = 48, 700
	narrowGeometry, narrowOK := pgHashGeometry(narrow.innerRows, narrow.innerWidth, cp.workMem)
	wideGeometry, wideOK := pgHashGeometry(wide.innerRows, wide.innerWidth, cp.workMem)
	if !narrowOK || !wideOK || narrowGeometry.virtualBuckets <= 0 || wideGeometry.virtualBuckets <= 0 {
		t.Fatalf("PG geometries = %+v/%+v, ok=%v/%v; want valid", narrowGeometry, wideGeometry, narrowOK, wideOK)
	}
	if narrowGeometry == wideGeometry {
		t.Fatalf("fixture did not vary PG virtual geometry: %+v", narrowGeometry)
	}
	executorBefore := hashsize.Choose(base.innerRows, base.innerCols, base.innerAvgVarBytes, cp.workMem)
	if executorBefore.NBatch <= 1 {
		t.Fatalf("fixture did not exercise executor spill geometry: %+v", executorBefore)
	}
	executorAfter := hashsize.Choose(base.innerRows, base.innerCols, base.innerAvgVarBytes, cp.workMem)
	if executorAfter != executorBefore {
		t.Fatalf("executor geometry changed: %+v want %+v", executorAfter, executorBefore)
	}
	if got, want := hashJoinCost(cp, narrow), hashJoinCost(cp, wide); got != want {
		t.Fatalf("unique matched-only cost leaked virtual geometry: %+v want %+v", got, want)
	}
}

func TestHashJoinInnerUniquePathInputReachesSerialAndPartialCosts(t *testing.T) {
	withParallelOn(t, func() {
		defer setGatherPathsModeForTest(gatherPathsAll)()
		s, joinrel, outer, inner, keys := r90UniqueFixture(t)
		s.parallelModeOK = true
		joinrel.ConsiderParallel = true
		outer.CheapestTotal = &Path{Rel: outer, Rows: outer.Rows, ParallelSafe: true}
		inner.CheapestTotal = &Path{Rel: inner, Rows: inner.Rows, ParallelSafe: true}
		inner.Pathlist = []*Path{inner.CheapestTotal}
		outer.PartialPathlist = []*Path{{
			Kind: PathSeqScan, Rel: outer, Rows: 25, ParallelSafe: true, ParallelAware: true, ParallelWorkers: 1,
		}}
		residual := &restrictInfo{relids: outer.Relids | inner.Relids, ecID: noEquivClass}
		addPathsToJoinrel(s, joinrel, outer, inner, append(keys, residual), s.cp, nil)

		var serial, partial *Path
		for _, p := range joinrel.Pathlist {
			if p.Kind == PathHashJoin {
				serial = p
				break
			}
		}
		for _, p := range joinrel.PartialPathlist {
			if p.Kind == PathHashJoin {
				partial = p
				break
			}
		}
		if serial == nil || partial == nil {
			t.Fatalf("hash paths: serial=%v partial=%v", serial != nil, partial != nil)
		}
		if partial.Children[0].Rows != 25 || partial.Children[1].Rows != inner.Rows {
			t.Fatalf("partial coordinates outer=%.0f inner=%.0f, want 25 and %.0f",
				partial.Children[0].Rows, partial.Children[1].Rows, inner.Rows)
		}

		final := s.hashJoinFinalCostInputFor(joinrel, outer, inner, parser.JoinInner, keys)
		bucket := s.estimateHashBucketSize(keys, inner.Relids)
		wantSerial := hashJoinCost(s.cp, hashJoinInputs{
			outer: serial.Children[0].Cost, inner: serial.Children[1].Cost,
			outerRows: serial.Children[0].Rows, innerRows: serial.Children[1].Rows,
			outputRows: joinrel.Rows, numHashClauses: len(keys), innerBucketSize: bucket, final: final,
			innerWidth: pathWidth(serial.Children[1]),
			outerCols:  pathNCols(serial.Children[0]), innerCols: pathNCols(serial.Children[1]),
			outerAvgVarBytes: pathAvgVarBytes(serial.Children[0]), innerAvgVarBytes: pathAvgVarBytes(serial.Children[1]),
		})
		wantSerial.Total += qualEvalCost(s.cp, 1, joinrel.Rows)
		if serial.Cost != wantSerial {
			t.Fatalf("serial cost = %+v, want %+v", serial.Cost, wantSerial)
		}

		wantPartialRows := clampRowEst(joinrel.Rows / getParallelDivisor(partial.Children[0].ParallelWorkers, s.cp.parallelLeaderParticipation))
		wantPartial := hashJoinCost(s.cp, hashJoinInputs{
			outer: partial.Children[0].Cost, inner: partial.Children[1].Cost,
			outerRows: partial.Children[0].Rows, innerRows: partial.Children[1].Rows,
			outputRows: wantPartialRows, numHashClauses: len(keys), innerBucketSize: bucket, final: final,
			innerWidth: pathWidth(partial.Children[1]),
			outerCols:  pathNCols(partial.Children[0]), innerCols: pathNCols(partial.Children[1]),
			outerAvgVarBytes: pathAvgVarBytes(partial.Children[0]), innerAvgVarBytes: pathAvgVarBytes(partial.Children[1]),
		})
		wantPartial.Total += qualEvalCost(s.cp, 1, wantPartialRows)
		if partial.Cost != wantPartial {
			t.Fatalf("partial cost = %+v, want %+v", partial.Cost, wantPartial)
		}
	})
}
