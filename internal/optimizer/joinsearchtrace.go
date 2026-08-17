package optimizer

// M0127-P5.9-l-ii — the SEARCH-side half of clause 6's instrument: enumeration
// provenance.
//
// P5.9-l-i built the PLAN-side half (internal/estimateaudit/spine.go): it reads
// the pairing each engine CHOSE and names the partitions PG 18.3 chose that
// goopg's plan does not contain. That channel cannot finish the argument,
// because two very different facts predict the identical observable —
//
//	(a) the DP enumerated the pairing and `add_path` lost it on cost, or
//	(b) the DP never offered the pairing at all,
//
// and 09 §4's ratchet admits (a) under the cost/stats clause while (b) is the
// "a bushy shape PG can produce that the goopg search cannot express" hard
// failure. The printed plan holds no evidence that separates them; only the
// search's own record does.
//
// So this file records what `makeJoinRel` was actually OFFERED. For one join
// problem it captures the relid → relation-name map (the thing that makes a
// relset comparable to a plan's `{a+b+c}` string) and, per pair, the
// `(outer relset, inner relset, phase)` triple 09 §3.11 asks for. It also
// records the pairs the connectivity gate DECLINED, which is what turns a
// negative answer into a diagnosis: "phase 2 offered {…}⋈{…}" and "phase 2
// declined it for want of a join clause" are different bugs with different
// fixes, and a channel that only logged the accepted pairs would report both as
// silence.
//
// It is off unless `GOOPG_PGSHAPED_DP_TRACE=1`, read once at process start like
// every other planner gate, and it writes to stderr — the server log — so an
// arm run harvests it with no protocol change (`cmd/estimate-audit
// --enum-trace <server log>` parses it back; internal/estimateaudit/enumtrace.go).
// Nothing in the search's behaviour depends on it: with the gate off,
// `searchCtx.trace` is nil and every call site is a nil check.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// dpTrace gates the enumeration trace. Read once at process start so a plan
// cannot start being traced mid-statement (joinsearch.go:52's rule).
var dpTrace = os.Getenv("GOOPG_PGSHAPED_DP_TRACE") == "1"

// dpTraceEnabled reports whether the enumeration trace is on.
func dpTraceEnabled() bool { return dpTrace }

// Enumeration-trace phases. These are `join_search_one_level`'s three passes
// (joinrels.c:85 / :141 / :200), recorded as the pair's provenance rather than
// inferred from the level: a (k, lev−k) split with k == 1 is reachable from
// phase 1 AND from phase 3, and which one produced it is exactly the difference
// between "connected" and "last-ditch cartesian".
const (
	tracePhaseNone      = 0
	tracePhaseLeftRight = 1 // phase 1, clause-connected
	tracePhaseBushy     = 2 // phase 2, bushy
	tracePhaseLastDitch = 3 // phase 3, last-ditch clauseless
)

// tracePair is one `(outer, inner, phase)` triple the enumerator produced, or
// one it declined.
type tracePair struct {
	phase   int
	level   int // relLevel(outer|inner) — the level the pair populates
	outer   RelSet
	inner   RelSet
	created bool   // this pair was the one that CREATED the joinrel
	reason  string // "" for an offered pair; the decline reason otherwise
}

// searchTrace is one join problem's provenance record.
//
// It is per-problem rather than per-process because that is the unit the
// question is asked in: `{customer+lineitem+n2+orders} ⋈ {n1+supplier}` is a
// partition OF Q7's six-relation problem, and a global stream would have to
// re-derive the boundary from level numbers. A sub-joinlist search
// (`searchOneProblem`, relfromjoinlist.go) gets its own trace, which is correct
// — its relset coordinates are its own.
type searchTrace struct {
	// names[i] is the relation the relid bit `1<<i` stands for: the FROM
	// item's ALIAS when it has one, else its catalog name. That is
	// `estimateaudit.leafRel`'s rule verbatim (parity.go:381) and it has to
	// be, or Q7's `nation n1` / `nation n2` collapse into one member on this
	// side and stay distinct on the plan side.
	names []string

	pairs    []tracePair
	declined []tracePair
	top      RelSet
	failed   string
}

// newSearchTrace builds the relid → name map for a problem, or nil when the
// trace is off. `bindings` is in FROM order, which is the order
// `buildInitialRels` derives relid `1<<i` from.
func newSearchTrace(bindings []rangeBinding) *searchTrace {
	if !dpTraceEnabled() {
		return nil
	}
	t := &searchTrace{names: make([]string, len(bindings))}
	for i, b := range bindings {
		t.names[i] = traceRelName(b, i)
	}
	return t
}

// traceRelName names one FROM item. A searched sub-problem enters its enclosing
// problem as a table-less, alias-less binding (relfromjoinlist.go's
// `searchOneProblem` return), so it gets a positional stand-in rather than an
// empty string: the name must stay a distinguishing key even when it cannot be
// a matching one.
func traceRelName(b rangeBinding, i int) string {
	if b.alias != "" {
		return b.alias
	}
	if b.table != nil && b.table.Name != "" {
		name := b.table.Name
		if j := strings.LastIndex(name, "."); j >= 0 {
			name = name[j+1:]
		}
		return name
	}
	return fmt.Sprintf("?%d", i)
}

// relsetName renders a relset the way a plan's relset prints: member names
// sorted, `+`-joined, braced. Sorted rather than in relid order because the
// plan side sorts (`dedupeSorted`, parity.go) and an unordered key that is only
// equal up to a permutation is not a key.
func (t *searchTrace) relsetName(rs RelSet) string {
	var parts []string
	for i := 0; i < maxSearchRels; i++ {
		if rs&(RelSet(1)<<uint(i)) == 0 {
			continue
		}
		if i < len(t.names) {
			parts = append(parts, t.names[i])
		} else {
			parts = append(parts, fmt.Sprintf("?%d", i))
		}
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, "+") + "}"
}

// pairKey is the canonical UNORDERED partition key —
// `estimateaudit.SpineJoin.PairKey`'s format, so the two channels' units are
// literally the same string. Unordered for the reason that function gives:
// `make_join_rel(x, y)` already handles `(y, x)`, so which side drives is a
// property of the PATH, not of the partition.
func (t *searchTrace) pairKey(a, b RelSet) string {
	x, y := t.relsetName(a), t.relsetName(b)
	if x > y {
		x, y = y, x
	}
	return x + " | " + y
}

// offer records a pair the enumerator handed to `makeJoinRel`.
func (t *searchTrace) offer(phase int, outer, inner RelSet, created bool) {
	if t == nil {
		return
	}
	t.pairs = append(t.pairs, tracePair{
		phase:   phase,
		level:   relLevel(outer | inner),
		outer:   outer,
		inner:   inner,
		created: created,
	})
}

// decline records a pair the enumerator considered and rejected, with the gate
// that rejected it. Overlapping pairs are NOT recorded: they are not partitions
// of anything, and at n=16 they would swamp the record.
func (t *searchTrace) decline(phase int, a, b RelSet, reason string) {
	if t == nil {
		return
	}
	t.declined = append(t.declined, tracePair{
		phase:  phase,
		level:  relLevel(a | b),
		outer:  a,
		inner:  b,
		reason: reason,
	})
}

// Trace line vocabulary. One block per join problem, framed by `problem` and
// `end`, so a reader can tell a truncated block from a complete one and so two
// backends' blocks cannot be confused for one (the whole block is written with
// a single Write; see emit).
const (
	traceTag     = "DPTRACE"
	traceProblem = traceTag + " problem"
	tracePairTag = traceTag + " pair"
	traceDecline = traceTag + " decline"
	traceEnd     = traceTag + " end"
)

// render formats the whole block. Separated from `emit` so the format is
// testable without capturing stderr.
func (t *searchTrace) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s nrels=%d rels=%s\n", traceProblem, len(t.names), strings.Join(t.names, ","))
	for _, p := range t.pairs {
		fmt.Fprintf(&b, "%s phase=%d lev=%d created=%d pair=%s outer=%s inner=%s\n",
			tracePairTag, p.phase, p.level, boolBit(p.created),
			t.pairKey(p.outer, p.inner), t.relsetName(p.outer), t.relsetName(p.inner))
	}
	for _, p := range t.declined {
		fmt.Fprintf(&b, "%s phase=%d lev=%d reason=%s pair=%s\n",
			traceDecline, p.phase, p.level, p.reason, t.pairKey(p.outer, p.inner))
	}
	status := "ok"
	if t.failed != "" {
		status = t.failed
	}
	fmt.Fprintf(&b, "%s top=%s pairs=%d declined=%d status=%s\n",
		traceEnd, t.relsetName(t.top), len(t.pairs), len(t.declined), status)
	return b.String()
}

func boolBit(v bool) int {
	if v {
		return 1
	}
	return 0
}

// emit writes the block to stderr — the server log — in ONE call. Planning runs
// per backend and several backends can plan at once; `os.File.Write` is a
// single write syscall, so a whole-block write is what keeps two problems'
// lines from interleaving into a third, nonexistent problem.
func (t *searchTrace) emit() {
	if t == nil {
		return
	}
	fmt.Fprint(os.Stderr, t.render())
}

// traceSeamDecline is the trace's OTHER half, added by M0127-P5.9-r: the record
// of a statement that never became a problem at all.
//
// The blocks above describe a search that RAN. When `tryPGShapedJoinSearch`
// declines, no `searchTrace` is ever constructed, so the channel says nothing —
// and "nothing" is exactly what a search that ran and enumerated no pair also
// says. M0127-P5.9-m spent a whole measurement pass on that ambiguity: Q72's
// eleven-way explicit-JOIN level emitted no trace, and separating "the seam
// declined it" from "the search enumerated nothing" took a synthetic control
// and a unit test to settle. This line settles it in the log.
//
// `reason` is a fixed vocabulary, not a message: it names the precondition, so
// a reader can grep one arm's log for `reason=leaf-count` and get a count.
func traceSeamDecline(reason string, nrels, nleaves int) {
	if !dpTraceEnabled() {
		return
	}
	fmt.Fprintf(os.Stderr, "%s seam-decline reason=%s nrels=%d nleaves=%d\n",
		traceTag, reason, nrels, nleaves)
}

// traceSeamSpine is the record of a statement the seam ADMITTED only in part:
// M0127-P5.9-s searched the inner prefix of a chain and left `nspine` pinned
// outer links above it, so the search's own trace block below covers `nprefix` of
// the statement's `nrels` relations and is complete about nothing else.
//
// Without this line the two numbers are indistinguishable in a log — a
// `levels=1..9` block on an eleven-relation query reads as an enumerator that
// gave up at nine, which is the same ambiguity `traceSeamDecline` exists to
// remove one step earlier.
func traceSeamSpine(nspine, nrels, nprefix int) {
	if !dpTraceEnabled() {
		return
	}
	fmt.Fprintf(os.Stderr, "%s seam-spine nspine=%d nrels=%d nprefix=%d\n",
		traceTag, nspine, nrels, nprefix)
}
