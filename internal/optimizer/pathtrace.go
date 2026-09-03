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
	fmt.Fprintf(os.Stderr,
		"%s %s producer=%s relids=%s kind=%d reqouter=%s rows=%.0f startup=%.2f total=%.2f disabled=%d pathkeys=%s verdict=%s\n",
		pathTraceTag, list, producer, relSetBits(rel.Relids), int(p.Kind),
		relSetBits(p.RequiredOuter), p.Rows, p.Cost.Startup, p.Cost.Total,
		p.DisabledNodes, pathkeys, verdict)
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
