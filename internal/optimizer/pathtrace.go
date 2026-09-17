package optimizer

import (
	"fmt"
	"os"
	"strings"
)

// Path provenance — planner-refactor-take2 P0-11.
//
// WHY. 09 §1 R4: "verify both candidates were generated before concluding a
// cost bug." Five wrong hypotheses were spent on TPC-H Q8 because the index
// producer emitted NOTHING at that parameterisation, and the investigation
// instrumented the cost FUNCTIONS rather than the point where a candidate does
// or does not arrive. A cost model can only be blamed for a path that was
// offered to it.
//
// The existing `DPTRACE` channel (joinsearchtrace.go) answers the other half of
// R4 — was this PARTITION enumerated — at `makeJoinRel` granularity. It cannot
// answer "which producer offered this PATH, and did it survive dominance".
//
// TAG CHOICE. The bundle's design proposed a third `DPTRACE path` record.
// That was rejected here: `estimateaudit/enumtrace.go` counts DPTRACE-tagged
// lines it cannot parse as `Malformed`, deliberately, so a silent drop cannot
// understate enumeration. A new DPTRACE kind would therefore have to land in
// the same commit as a parser arm, and any older parser reading a newer log
// would report a large bogus Malformed count. A DISTINCT tag has neither
// problem: `enumtrace.go` ignores it entirely (its anchor is the DPTRACE
// substring), so emitter and parser stay independent and old readers are
// unaffected.

const pathTraceTag = "DPPATH"

// pathTraceEnabled reuses GOOPG_PGSHAPED_DP_TRACE rather than adding a second
// diagnostic variable: the two channels answer halves of one question and a
// reader who wants one almost always wants the other. It is read once at
// process start, so the accepted-path fast path below costs one already-loaded
// boolean test — `runtime.Stack` in a hot path is the perf-optimize2 regression
// this repository has already paid for once, which is also why `producer` is a
// caller-supplied string rather than something recovered from the stack.
var pathTraceEnabled = dpTrace

// pathVerdict is what addToPathlist decided about a candidate.
type pathVerdict string

const (
	verdictAccepted  pathVerdict = "accepted"
	verdictDominated pathVerdict = "dominated"
)

// tracePath emits one provenance record. `producer` names the call site that
// offered the path; the trace is worthless without it, since "which producer
// offered this" is the exact question.
func tracePath(rel *RelOptInfo, p *Path, producer string, partial bool, verdict pathVerdict) {
	if !pathTraceEnabled || rel == nil || p == nil {
		return
	}
	list := "path"
	if partial {
		list = "partial"
	}
	var pathkeys string
	if len(p.Pathkeys) > 0 {
		pathkeys = fmt.Sprintf("%d", len(p.Pathkeys))
	} else {
		pathkeys = "0"
	}
	// C-03a: the jointype label. It is what makes a DPPATH line adjudicable
	// for C-03b/C-03d — "was an outer/semi/anti pairing OFFERED at its level,
	// and did it survive dominance" cannot be read off a line that only names
	// the operator kind, because kind is the ALGORITHM (hash/merge/nestloop)
	// and jointype is the SEMANTICS, and the two are orthogonal.
	//
	// Every path emits it, including the scan and wrapper paths that never set
	// the field: `inner` there is the zero value, and a label that appears on
	// some lines but not others would need the reader to know which kinds are
	// joins. Appended at the END of the record, after `verdict`, so a reader
	// splitting on the existing key=value pairs keeps working unchanged.
	//
	// R53 slice 1: the partition labels `outer`/`inner` follow jointype, under
	// the same append-at-end rule — they name the two input relsets in
	// Children order (Path.OuterRelids/InnerRelids), `-` on non-join paths.
	// A DPPATH line still cannot key a DPTRACE pair by itself (bit positions,
	// not names), but relSetBits is the same rendering both channels use, so
	// the two join on it.
	fmt.Fprint(os.Stderr, formatPathLine(list, rel, p, producer, pathkeys, verdict))
}

// formatPathLine renders one provenance record. Separated from `tracePath`
// so the vocabulary (including the R53 slice-1 partition labels) is testable
// without capturing stderr.
//
// R54 Step-2: `width`/`inputtotal` close the reviewed STEP2.md §2 contract —
// rows AND width per leg, plus the input join-path total beneath each upper
// candidate, so the fix round can subtract join-leg delta from upper-leg
// delta. `width` is the rel's byte width (RelOptInfo.Width, what the page
// math prices); `inputtotal` is Children[0]'s total (`-1` when the path
// carries no input — scan leaves and test fixtures), which for every upper
// arm is the priced input the candidate was costed against. Appended at the
// END under the same rule as jointype/partition labels, so a reader
// splitting on key=value pairs keeps working unchanged.
//
// M0142-0003b: `startup`/`total`/`inputtotal` render via `%g`, not `%.2f`.
// M0142-0003a found a near-tie at the full join for TPC-H Q9 — goopg's
// winning candidate and PG's own chain's cheapest candidate both rounded to
// `80099.64` at two decimals, and the fixed `%.2f` could not say whether that
// was a genuine tie decided by DP insertion order or a real (if small) cost
// gap. `%g` matches the sibling `DPTRACE cost`/`decline` channel
// (joinsearchtrace.go's `total=%g`), which already prints full precision for
// exactly this reason — bringing DPPATH in line closes the one place the two
// channels disagreed on precision, rather than inventing a second format.
func formatPathLine(list string, rel *RelOptInfo, p *Path, producer, pathkeys string, verdict pathVerdict) string {
	inputTotal := -1.0
	if len(p.Children) > 0 && p.Children[0] != nil {
		inputTotal = p.Children[0].Cost.Total
	}
	return fmt.Sprintf(
		"%s %s producer=%s relids=%s kind=%d reqouter=%s rows=%.0f startup=%g total=%g disabled=%d pathkeys=%s verdict=%s jointype=%s outer=%s inner=%s width=%d inputtotal=%g\n",
		pathTraceTag, list, producer, relSetBits(rel.Relids), int(p.Kind),
		relSetBits(p.RequiredOuter), p.Rows, p.Cost.Startup, p.Cost.Total,
		p.DisabledNodes, pathkeys, verdict, strings.ToLower(joinTypeName(p.Jointype)),
		relSetBits(p.OuterRelids), relSetBits(p.InnerRelids), rel.Width, inputTotal)
}

// traceOrderedCandidatePopulation emits one DPPATH diagnostic line for
// createOrderedPaths's SearchCandidates population step (upperordered.go).
//
// M0141-S7-cd-q64: some ORDER BY witnesses show zero `upper.ordered.*`
// producer lines beyond the seed Sort, and the trace alone cannot tell apart
// "searchedRelOf(input) returned nil" (input is not a searched-tree root —
// e.g. a raw multi-child join or set-op sits at the top, see
// searchedRelOf's own "not on the boundary chain" case) from "it returned a
// rel, but every candidate's re-earned Pathkeys came back empty" (every
// candidate's ordering claim failed validatedSearchCandidateKeys). Both look
// identical downstream — SearchCandidates is unusable either way — but only
// the second is a validator bug; the first is a structural gap in which
// inputs the boundary chain recognises at all.
func traceOrderedCandidatePopulation(searchedRel bool, candidates int, nonEmptyKeys int) {
	if !pathTraceEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "%s candidates producer=upper.ordered.candidates searchedrel=%v candidates=%d nonemptykeys=%d\n",
		pathTraceTag, searchedRel, candidates, nonEmptyKeys)
}

// traceIncrementalSortCandidate emits one DPPATH diagnostic line per
// candidate `addIncrementalSortPaths` (incrementalsortpaths.go) considered
// and declined or accepted — the finer-grained sibling of
// traceOrderedCandidatePopulation for the M0141-S7-cd-candidatepool question:
// which of the `nonempty` candidates traceOrderedCandidatePopulation counted
// actually reach a genuine partial-prefix offer, and why the rest don't
// (`kind`=candidate's own Path.Kind, `keys`=len(SearchCandidateKeys[i]),
// `contained`/`ncommon`=pathkeysCountContainedIn's verdict).
func traceIncrementalSortCandidate(i int, kind PathKind, keyLen int, contained bool, nCommon int, totalCost float64) {
	if !pathTraceEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "%s candidate producer=upper.ordered.incrementalsort.candidate index=%d kind=%d keys=%d contained=%v ncommon=%d totalcost=%v\n",
		pathTraceTag, i, int(kind), keyLen, contained, nCommon, totalCost)
}

// traceOrderedSeedCandidate emits one DPPATH diagnostic line for the seed
// path itself — the same `input` addOrderedPaths (upperordered.go) receives
// as its arm-1/2 candidate — scored against sortPathkeys the identical way
// traceIncrementalSortCandidate scores every OTHER SearchCandidates entry.
// M0141-S7-cd-candidatepool's question is whether the seed's own ordering
// claim ever has a genuine partial-prefix match that addIncrementalSortPaths
// structurally cannot offer (its loop walks ordered.SearchCandidates, which
// may or may not still contain the exact Path that became the seed) — this
// line is the seed-side half of that comparison; traceIncrementalSortCandidate
// is the SearchCandidates-side half. Matching kind/keys/ncommon/totalcost
// across both for the same query means the seed IS represented in the loop
// (totalcost disambiguates same-shape-different-candidate coincidences, since
// every entry in ordered.SearchCandidates shares the same relset and hence
// the same Rows, but the seed is specifically the CHEAPEST of them by
// construction — getCheapestFractionalPath's pick — so an exact cost match
// against one specific SearchCandidates entry is strong identity evidence,
// not just a shape coincidence); a seed line with no matching candidate line
// means it structurally is not.
func traceOrderedSeedCandidate(kind PathKind, keyLen int, contained bool, nCommon int, totalCost float64) {
	if !pathTraceEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "%s seed producer=upper.ordered.seed kind=%d keys=%d contained=%v ncommon=%d totalcost=%v\n",
		pathTraceTag, int(kind), keyLen, contained, nCommon, totalCost)
}

// relSetBits renders a RelSet as a stable, parseable member list. The trace has
// no access to relation NAMES here (addPath is below the level that knows
// them), so the bitmask members are the identity — and they are what
// `DPTRACE pair` lines key on too, so the two channels join.
func relSetBits(s RelSet) string {
	if s == 0 {
		return "-"
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for i := 0; i < 16; i++ {
		if s&(1<<uint(i)) == 0 {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d", i)
		first = false
	}
	b.WriteByte('}')
	return b.String()
}
