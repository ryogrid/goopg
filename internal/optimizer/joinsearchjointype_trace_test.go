package optimizer

// C-03d (docs/design/planner-c03-jointype-search/DESIGN.md §4) — the enum-trace
// evidence for the jointype-aware search, and the pin that says why all four
// C-03 slices are inert.
//
// C-03b's own tests drive `addPathsToJoinrel` directly, which proves the ARM.
// This file drives the SEARCH, on a spine-shaped fixture, and reads both
// provenance channels the way the audit tool reads a real server log:
//
//   - DPTRACE (joinsearchtrace.go), through the production reader
//     `estimateaudit.ParseEnumTrace` — was the outer pairing OFFERED at its
//     level, and what did the legality gate decline;
//   - DPPATH (pathtrace.go) — did a path for that pairing get offered and
//     accepted, and with which jointype.
//
// Two channels because they answer different halves and neither substitutes for
// the other: a pairing can be enumerated and produce no path, and a path can be
// produced for a pairing whose partition was never the one under discussion.
// That separation is the whole reason DPPATH exists beside DPTRACE (pathtrace.go
// header, R4).
//
// The search is driven directly because the production seam still peels these
// links off before `makeJoinRel` ever sees them (C-03 DESIGN §3). C-04 deletes
// that seam; until it does, this is where an outer/semi/anti pairing can be
// observed at all.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/testutil/estimateaudit"
)

// spineCtx is the C-03d fixture: three rels named a, b, c, with a LEFT
// SpecialJoinInfo whose minimum LHS is {a} and minimum RHS is {b} — the shape
// `splitOuterSpine` peels in production — plus the real path builder, so the
// pairs the enumerator offers actually generate paths.
func spineCtx(t *testing.T, sj *SpecialJoinInfo) *searchCtx {
	t.Helper()
	s := traceCtx(t, "a", "b", "c")
	s.joinInfoList = []*SpecialJoinInfo{sj}
	// Real rels with real scan paths: the builder must have a cheapest path on
	// each input or `addPathsToJoinrel` reports its invariant error.
	for i := 0; i < 3; i++ {
		rel := s.findRel(relsetOf(i))
		rel.Pathlist = nil
		generateScanPaths(rel, defaultCostParams(), int64(10*(i+1)), 0, 0, true)
		setCheapest(rel)
	}
	s.builder = &searchJoinRelBuilder{s: s}
	return s
}

// runSpineSearch runs the search over the a-b / a-c chain and returns the
// parsed DPTRACE harvest plus the raw DPPATH lines.
func runSpineSearch(t *testing.T, s *searchCtx) (estimateaudit.EnumTrace, []string) {
	t.Helper()
	a, b, c := relsetOf(0), relsetOf(1), relsetOf(2)
	// b-c is present so the pair ({b}, {c}) clears the CONNECTIVITY gate and
	// reaches `joinIsLegal`. Without it the pair is declined one gate earlier
	// ("no-join-clause") and the fixture would prove nothing about legality —
	// two different declines that look identical from the outside.
	clauses := &restrictInfoList{all: []*restrictInfo{
		equiClause(a, b), equiClause(a, c), equiClause(b, c),
	}}
	var searchErr error
	lines := captureTrace(t, func() {
		_, searchErr = s.joinSearch(clauses, s.builder)
	})
	if searchErr != nil {
		t.Fatalf("joinSearch: %v", searchErr)
	}
	return estimateaudit.ParseEnumTrace(strings.NewReader(strings.Join(lines, "\n"))), lines
}

// TestEnumTraceAdjudicatesOuterPairing: the LEFT pairing {a} | {b} is OFFERED at
// its level and its paths are ACCEPTED carrying jointype=left, while the pairing
// that would violate the SJI — {b} | {c}, which would complete the outer join's
// RHS against an unrelated rel — is DECLINED by the legality gate rather than
// silently absent.
//
// "Declined, not absent" is the point. `spine.go` closes on exactly this
// ambiguity: a partition missing from a plan may have been enumerated and lost
// on cost, or never enumerated at all, and both predict the same printed plan.
func TestEnumTraceAdjudicatesOuterPairing(t *testing.T) {
	enableDPTrace(t)
	s := spineCtx(t, mkSJ(parser.JoinLeft, relsetOf(0), relsetOf(1)))
	tr, lines := runSpineSearch(t, s)

	if tr.Malformed != 0 || tr.ReadErr != "" {
		t.Fatalf("harvest is unsound: %d malformed, err=%q — every verdict below "+
			"would be suspect", tr.Malformed, tr.ReadErr)
	}
	if len(tr.Problems) != 1 {
		t.Fatalf("harvested %d problems, want 1", len(tr.Problems))
	}
	pr := tr.Problems[0]
	if pr.Status != "ok" {
		t.Fatalf("problem status %q, want ok", pr.Status)
	}
	if _, ok := pr.Offered["{a} | {b}"]; !ok {
		t.Errorf("the LEFT pairing was not OFFERED; offered=%v", offeredKeysOf(pr.Offered))
	}
	if d, ok := pr.Declined["{b} | {c}"]; !ok {
		t.Errorf("{b} | {c} is absent from the record entirely; it must be recorded "+
			"as DECLINED, or a later reader cannot tell 'never enumerated' from "+
			"'enumerated and lost on cost'. declined=%v", declineKeysOf(pr.Declined))
	} else if d.Reason != "illegal" {
		t.Errorf("{b} | {c} declined for %q, want \"illegal\" — joining the outer "+
			"join's nullable side to an unrelated rel violates the SJI", d.Reason)
	}

	// The DPPATH half: paths for the {a,b} relset, carrying the jointype the
	// SJI dictated, and at least one surviving dominance.
	var offered, accepted int
	for _, l := range lines {
		if !strings.HasPrefix(l, "DPPATH") || !strings.Contains(l, "relids={0,1}") {
			continue
		}
		offered++
		if !strings.Contains(l, "jointype=left") {
			t.Errorf("path over the LEFT joinrel is stamped otherwise: %q", l)
		}
		if strings.Contains(l, "verdict=accepted") {
			accepted++
		}
	}
	if offered == 0 {
		t.Fatal("the LEFT pairing was offered to makeJoinRel but produced no path — " +
			"a joinrel with an empty pathlist is a hard search failure")
	}
	if accepted == 0 {
		t.Errorf("%d LEFT paths offered, none accepted", offered)
	}
}

// TestEnumTraceSemiPairingIsNestloopOnly: the same adjudication for SEMI, where
// the interesting fact is which PRODUCERS appear. The keyed operators are absent
// by C-03b's rule, and the assertion is stated against the trace rather than
// against the surviving pathlist — `addPath` prunes, and an absent path proves
// nothing about whether its producer ran.
func TestEnumTraceSemiPairingIsNestloopOnly(t *testing.T) {
	enableDPTrace(t)
	s := spineCtx(t, mkSJ(parser.JoinSemi, relsetOf(0), relsetOf(1)))
	_, lines := runSpineSearch(t, s)

	got := map[string]bool{}
	for _, l := range lines {
		if !strings.HasPrefix(l, "DPPATH") || !strings.Contains(l, "relids={0,1}") {
			continue
		}
		if !strings.Contains(l, "jointype=semi") {
			t.Errorf("path over the SEMI joinrel is stamped otherwise: %q", l)
		}
		for p := range producersIn([]string{l}) {
			got[p] = true
		}
	}
	if !got["join.nestloop"] {
		t.Fatal("no nested loop offered for the SEMI pairing; it is the only arm " +
			"C-03b leaves open, so its absence is a hard failure, not a decline")
	}
	for _, banned := range []string{"join.hash", "mergejoin"} {
		if got[banned] {
			t.Errorf("producer %s ran for a SEMI pairing; goopg has no unique-ification "+
				"proof and a keyed SEMI would multiply rows", banned)
		}
	}
}

// TestSearchIsInertForDisjointJoinInfoList is the §5 inertness mechanism pin,
// and it is the load-bearing one: the whole C-03 series is behaviour-neutral
// only because every pair the search reaches today yields a NIL SpecialJoinInfo
// from `joinIsLegal`, so `addPathsToJoinrel` takes its `sjinfo == nil` arm and
// behaves exactly as it did before C-03b.
//
// The reason it holds in production is structural, not accidental: the prefix
// problem is handed `ctx.joinInfoList` UNFILTERED, and every entry in it
// describes an outer/semi/anti link the seam already peeled — so its
// MinRighthand is disjoint from the prefix's relset and `join_is_legal`'s
// RHS-overlap fast path (joinrels.c:386-387) rejects it before any other test
// runs. This pins that fast path as a regression test; a change that made a
// peeled link's RHS overlap the prefix would turn every C-03 slice live at once.
func TestSearchIsInertForDisjointJoinInfoList(t *testing.T) {
	// Rels 0-2 are the prefix problem; the SJI describes a link over rels 3-4,
	// which the seam peeled off above it.
	s := jslCtx(t, 5)
	s.joinInfoList = []*SpecialJoinInfo{
		mkSJ(parser.JoinLeft, relsetOf(3), relsetOf(4)),
		mkSJ(parser.JoinSemi, relsetOf(3), relsetOf(4)),
		mkSJ(parser.JoinAnti, relsetOf(3), relsetOf(4)),
	}
	for i := 0; i < 3; i++ {
		for j := i + 1; j < 3; j++ {
			sj, reversed, err := s.joinIsLegal(s.findRel(relsetOf(i)), s.findRel(relsetOf(j)))
			if err != nil {
				t.Errorf("joinIsLegal(%d,%d) = %v; a peeled link must not make a "+
					"prefix-internal pair illegal", i, j, err)
			}
			if sj != nil || reversed {
				t.Errorf("joinIsLegal(%d,%d) = (%v, %v); want (nil, false) — every "+
					"pair the search reaches today is a plain inner join, which is "+
					"why C-03a..d are inert", i, j, sj, reversed)
			}
		}
	}
}

func offeredKeysOf(m map[string]estimateaudit.EnumPair) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func declineKeysOf(m map[string]estimateaudit.EnumDecline) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
