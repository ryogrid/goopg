package optimizer

// C-03b (docs/design/planner-c03-jointype-search/DESIGN.md §4) — jointype-aware
// `addPaths`, landed INERT. The slice's gate is DPPATH OFFERED/ACCEPTED
// adjudication on HAND-BUILT outer/semi/anti problems, because no such pairing
// reaches `makeJoinRel` in production yet (§3: LEFT/RIGHT are peeled by
// `splitOuterSpine`, SEMI/ANTI are declined at the seam gate, FULL declines the
// whole search). C-04 deletes that gate; until then the search is the only
// place these arms can be exercised, and it must be driven directly.
//
// The traps this file is written against, both with a history in this area:
//
//   - "Verify BOTH candidates were generated before comparing." Every decline
//     assertion below is paired with a POSITIVE control proving the arm under
//     test does fire for a jointype that permits it. A test that only checks
//     "no hash path for SEMI" passes just as well when the producer emitted
//     nothing at all for an unrelated reason.
//   - "An unwinnable path is an untested path." These paths cannot win a search
//     today, so the shape is forced here rather than waited for.

import (
	"os"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// jointypeProblem builds the standard two-rel problem these tests adjudicate on:
// a 10000-row outer, a 500-row inner, one usable equality between them (so the
// keyed arms have something to key on) and one inequality (so the residual is
// non-empty). `a` is the SJI's LHS, `b` its RHS.
func jointypeProblem(t *testing.T) (a, b RelSet, outer, inner, joinrel *RelOptInfo, clauses []*restrictInfo) {
	t.Helper()
	a, b = relsetOf(0), relsetOf(1)
	outer, inner = scanRel(a, 10000, 100), scanRel(b, 500, 5)
	joinrel = newRelOptInfo(a|b, 5000, 64)
	clauses = []*restrictInfo{equiClause(a, b), plainClause(a | b)}
	return
}

// kindsOf summarises a pathlist by operator kind, so a decline can be stated as
// "no keyed operator" rather than by indexing into the list.
func kindsOf(list []*Path) map[PathKind]int {
	m := map[PathKind]int{}
	for _, p := range list {
		m[p.Kind]++
	}
	return m
}

// TestJointypeForDirection_OrientationDecidesLegality is the mechanism, stated
// on its own before any path is generated: the SAME sjinfo reaches both
// directions and only the one whose outer covers MinLefthand may perform the
// join. That is PG's arrangement — `populate_joinrel_with_paths` hands its two
// `add_paths_to_joinrel` calls DIFFERENT jointypes (JOIN_LEFT / JOIN_RIGHT at
// joinrels.c:932-939) precisely because the jointype alone cannot express it.
func TestJointypeForDirection_OrientationDecidesLegality(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)

	if jt, ok := jointypeForDirection(nil, a, b); !ok || jt != parser.JoinInner {
		t.Errorf("nil sjinfo forward = (%v, %v), want (JoinInner, true)", jt, ok)
	}
	if jt, ok := jointypeForDirection(nil, b, a); !ok || jt != parser.JoinInner {
		t.Errorf("nil sjinfo reversed = (%v, %v), want (JoinInner, true) — an inner "+
			"join is legal in both directions", jt, ok)
	}

	for _, jtype := range []parser.JoinType{
		parser.JoinLeft, parser.JoinRight, parser.JoinSemi, parser.JoinAnti,
	} {
		sj := mkSJ(jtype, a, b)
		if jt, ok := jointypeForDirection(sj, a, b); !ok || jt != jtype {
			t.Errorf("%v forward = (%v, %v), want (%v, true)", jtype, jt, ok, jtype)
		}
		if _, ok := jointypeForDirection(sj, b, a); ok {
			t.Errorf("%v reversed = legal; want declined — the reversed direction is "+
				"PG's JOIN_RIGHT/JOIN_RIGHT_SEMI/JOIN_RIGHT_ANTI, which goopg does not "+
				"generate", jtype)
		}
	}

	// FULL: neither direction. C-03c — the executor has no FULL hash semantics,
	// so there is no `createPlanNode` arm to emit one and a path would be a
	// plan that silently drops the rows a full join exists to keep.
	sjFull := mkSJ(parser.JoinFull, a, b)
	for _, dir := range [][2]RelSet{{a, b}, {b, a}} {
		if _, ok := jointypeForDirection(sjFull, dir[0], dir[1]); ok {
			t.Errorf("FULL (%#b as outer) = legal; want declined in BOTH directions",
				dir[0])
		}
	}
}

// TestAddPaths_FullGeneratesNothing states the FULL decline where it bites: a
// FULL joinrel comes out of `addPathsToJoinrel` with an EMPTY pathlist, which
// `joinSearch` turns into an error (joinsearchlevel.go:305) and the planner
// answers by falling back to the syntactic join shape. That is the same outcome
// the pre-C-03 tree reaches by declining the whole search for FULL, which is
// why the decline is inert rather than a regression.
func TestAddPaths_FullGeneratesNothing(t *testing.T) {
	a, b, outer, inner, joinrel, clauses := jointypeProblem(t)
	sj := mkSJ(parser.JoinFull, a, b)
	for _, dir := range []struct{ o, i *RelOptInfo }{{outer, inner}, {inner, outer}} {
		if err := addPathsToJoinrel(nil, joinrel, dir.o, dir.i, clauses, defaultCostParams(), sj); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
	}
	if len(joinrel.Pathlist)+len(joinrel.PartialPathlist) != 0 {
		t.Errorf("FULL generated %d serial + %d partial paths; want none",
			len(joinrel.Pathlist), len(joinrel.PartialPathlist))
	}
}

// TestAddPaths_InnerUnchanged is the inertness control for every other test in
// this file: with no SpecialJoinInfo — which is what `joinIsLegal` returns for
// every pair in every query the search plans today — both directions generate,
// every arm fires, and every path is INNER.
func TestAddPaths_InnerUnchanged(t *testing.T) {
	_, _, outer, inner, joinrel, clauses := jointypeProblem(t)
	for _, dir := range []struct{ o, i *RelOptInfo }{{outer, inner}, {inner, outer}} {
		if err := addPathsToJoinrel(nil, joinrel, dir.o, dir.i, clauses, defaultCostParams(), nil); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
	}
	// The positive control for every decline below is stated on what was
	// OFFERED, not on what survived: a plain nested loop over these two inputs
	// is dominated by the hash path and `addPath` prunes it, which is correct
	// and is exactly the confusion R4 warns about — an absent path in the
	// pathlist says nothing about whether its producer ran.
	offered := producersIn(captureTrace(t, func() {
		fresh := newRelOptInfo(joinrel.Relids, 5000, 64)
		if err := addPathsToJoinrel(nil, fresh, outer, inner, clauses, defaultCostParams(), nil); err != nil {
			t.Fatalf("addPathsToJoinrel: %v", err)
		}
	}))
	for _, want := range []string{"join.hash", "join.nestloop", "mergejoin"} {
		if !offered[want] {
			t.Errorf("inner join offered %v; want producer %s — the positive control "+
				"for the SEMI/ANTI declines below", offered, want)
		}
	}
	for _, p := range joinrel.Pathlist {
		if p.Jointype != parser.JoinInner {
			t.Errorf("inner-join path kind=%d stamped %v", p.Kind, p.Jointype)
		}
	}
}

// TestAddPaths_OuterLegalDirectionOnly: for LEFT/RIGHT/FULL the legal direction
// generates the SAME arms an inner join would (keyed operators included) and
// stamps them with the SJI's jointype; the reversed direction generates
// nothing at all.
func TestAddPaths_OuterLegalDirectionOnly(t *testing.T) {
	for _, jtype := range []parser.JoinType{parser.JoinLeft, parser.JoinRight} {
		t.Run(joinTypeName(jtype), func(t *testing.T) {
			a, b, outer, inner, joinrel, clauses := jointypeProblem(t)
			sj := mkSJ(jtype, a, b)

			if err := addPathsToJoinrel(nil, joinrel, outer, inner, clauses, defaultCostParams(), sj); err != nil {
				t.Fatalf("legal direction: %v", err)
			}
			kinds := kindsOf(joinrel.Pathlist)
			if kinds[PathHashJoin] == 0 {
				t.Errorf("legal direction generated %v; want a hash path — an outer join "+
					"keeps every arm an inner join has", kinds)
			}
			if len(joinrel.Pathlist) == 0 {
				t.Fatal("legal direction generated no paths")
			}
			for _, p := range joinrel.Pathlist {
				if p.Jointype != jtype {
					t.Errorf("path kind=%d stamped %v, want %v", p.Kind, p.Jointype, jtype)
				}
			}

			before := len(joinrel.Pathlist)
			if err := addPathsToJoinrel(nil, joinrel, inner, outer, clauses, defaultCostParams(), sj); err != nil {
				t.Fatalf("reversed direction: %v", err)
			}
			if len(joinrel.Pathlist) != before {
				t.Errorf("reversed direction added %d paths; want 0",
					len(joinrel.Pathlist)-before)
			}
		})
	}
}

// TestAddPaths_SemiAntiNestloopOnly: the keyed operators decline for SEMI/ANTI,
// the nested loops do not. Both halves are asserted — a decline is only
// evidence if the same fixture produces the declined operator for a jointype
// that allows it, which `TestAddPaths_InnerUnchanged` and the LEFT subtest
// above establish on this exact problem.
func TestAddPaths_SemiAntiNestloopOnly(t *testing.T) {
	for _, jtype := range []parser.JoinType{parser.JoinSemi, parser.JoinAnti} {
		t.Run(joinTypeName(jtype), func(t *testing.T) {
			a, b, outer, inner, joinrel, clauses := jointypeProblem(t)
			sj := mkSJ(jtype, a, b)

			if err := addPathsToJoinrel(nil, joinrel, outer, inner, clauses, defaultCostParams(), sj); err != nil {
				t.Fatalf("legal direction: %v", err)
			}
			kinds := kindsOf(joinrel.Pathlist)
			if kinds[PathNestLoop] == 0 {
				t.Fatalf("%v generated %v; want at least one nested loop — a joinrel "+
					"with an empty pathlist is a hard search failure", jtype, kinds)
			}
			if kinds[PathHashJoin] != 0 || kinds[PathMergeJoin] != 0 {
				t.Errorf("%v generated %v; want nestloop only — goopg has no "+
					"unique-ification proof, so a keyed SEMI/ANTI would multiply rows",
					jtype, kinds)
			}
			if len(joinrel.PartialPathlist) != 0 {
				t.Errorf("%v generated %d partial paths; the partial hash arm is keyed "+
					"and must decline with its serial twin", jtype, len(joinrel.PartialPathlist))
			}
			for _, p := range joinrel.Pathlist {
				if p.Jointype != jtype {
					t.Errorf("path kind=%d stamped %v, want %v", p.Kind, p.Jointype, jtype)
				}
			}

			before := len(joinrel.Pathlist)
			if err := addPathsToJoinrel(nil, joinrel, inner, outer, clauses, defaultCostParams(), sj); err != nil {
				t.Fatalf("reversed direction: %v", err)
			}
			if len(joinrel.Pathlist) != before {
				t.Errorf("reversed direction added %d paths; want 0 — PG would emit "+
					"JOIN_RIGHT_SEMI/JOIN_RIGHT_ANTI there and goopg does not",
					len(joinrel.Pathlist)-before)
			}
		})
	}
}

// TestMakeJoinRelPassesSjinfoToBothDirections closes the seam: the SJI the
// legality test matched is what reaches the builder, POST the `reversed` swap,
// on BOTH calls. Without the swap the first call would be the illegal
// orientation and a LEFT joinrel would come out empty.
func TestMakeJoinRelPassesSjinfoToBothDirections(t *testing.T) {
	a, b := relsetOf(0), relsetOf(1)
	sj := mkSJ(parser.JoinLeft, a, b)
	s := jslCtx(t, 2)
	s.joinInfoList = []*SpecialJoinInfo{sj}
	s.clauses = &restrictInfoList{}
	bld := &recordingBuilder{}
	s.builder = bld

	// Offered in the REVERSED order on purpose: makeJoinRel must swap.
	if _, err := s.makeJoinRel(s.findRel(b), s.findRel(a)); err != nil {
		t.Fatalf("makeJoinRel: %v", err)
	}
	if len(bld.pairs) != 2 || len(bld.sjinfos) != 2 {
		t.Fatalf("got %d addPaths calls, want 2", len(bld.pairs))
	}
	for i, got := range bld.sjinfos {
		if got != sj {
			t.Errorf("call %d got sjinfo %v, want the matched LEFT SJI", i, got)
		}
	}
	if bld.pairs[0].outer != a || bld.pairs[0].inner != b {
		t.Errorf("first call = {%#b}x{%#b}; want outer={%#b} (the SJI's LHS) — "+
			"makeJoinRel must apply the `reversed` swap before calling addPaths",
			bld.pairs[0].outer, bld.pairs[0].inner, a)
	}
}

// TestDPPATHAdjudicatesOfferedAndAccepted is the slice's named gate. It reads
// the OFFERED/ACCEPTED record the way C-03d's enum-trace fixtures will: every
// path a producer offers appears on a DPPATH line carrying its jointype and its
// dominance verdict, so "was this pairing offered at all" and "did it survive"
// are separable questions — which is the whole point of the channel (R4: five
// wrong hypotheses were once spent on a producer that emitted nothing).
func TestDPPATHAdjudicatesOfferedAndAccepted(t *testing.T) {
	for _, tc := range []struct {
		jtype        parser.JoinType
		wantProducer string // a producer that MUST appear
		bannedKind   string // a producer that must NOT
	}{
		{parser.JoinLeft, "join.hash", ""},
		{parser.JoinSemi, "join.nestloop", "join.hash"},
		{parser.JoinAnti, "join.nestloop", "join.hash"},
	} {
		t.Run(joinTypeName(tc.jtype), func(t *testing.T) {
			a, b, outer, inner, joinrel, clauses := jointypeProblem(t)
			sj := mkSJ(tc.jtype, a, b)
			lines := captureTrace(t, func() {
				if err := addPathsToJoinrel(nil, joinrel, outer, inner, clauses, defaultCostParams(), sj); err != nil {
					t.Fatalf("addPathsToJoinrel: %v", err)
				}
			})
			want := "jointype=" + strings.ToLower(joinTypeName(tc.jtype))
			offered, accepted := 0, 0
			for _, l := range lines {
				if !strings.Contains(l, "DPPATH") {
					continue
				}
				offered++
				if !strings.Contains(l, want) {
					t.Errorf("DPPATH line %q does not carry %q", l, want)
				}
				if strings.Contains(l, "verdict=accepted") {
					accepted++
				}
				if tc.bannedKind != "" && strings.Contains(l, "producer="+tc.bannedKind) {
					t.Errorf("DPPATH line %q: producer %s must not run for %v",
						l, tc.bannedKind, tc.jtype)
				}
			}
			if offered == 0 {
				t.Fatalf("%v: nothing OFFERED — the trace, not the producer, would be "+
					"the bug to look for here", tc.jtype)
			}
			if accepted == 0 {
				t.Errorf("%v: %d paths offered, none ACCEPTED", tc.jtype, offered)
			}
			var sawWanted bool
			for _, l := range lines {
				if strings.Contains(l, "producer="+tc.wantProducer) {
					sawWanted = true
				}
			}
			if !sawWanted {
				t.Errorf("%v: no DPPATH line from producer=%s", tc.jtype, tc.wantProducer)
			}
		})
	}
}

// captureTrace runs fn with the DPPATH channel forced on and returns the lines
// it wrote to stderr. The pipe is drained on a goroutine because a producer can
// emit more than the pipe buffer holds.
func captureTrace(t *testing.T, fn func()) []string {
	t.Helper()
	oldEnabled, oldErr := pathTraceEnabled, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	pathTraceEnabled, os.Stderr = true, w
	fn()
	pathTraceEnabled, os.Stderr = oldEnabled, oldErr
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("close read end: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// producersIn reduces DPPATH lines to the set of producers that offered a path.
func producersIn(lines []string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lines {
		i := strings.Index(l, "producer=")
		if i < 0 {
			continue
		}
		rest := l[i+len("producer="):]
		if j := strings.IndexByte(rest, ' '); j >= 0 {
			rest = rest[:j]
		}
		m[rest] = true
	}
	return m
}
