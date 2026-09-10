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
	"math"
	"os"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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

// traceCost is one relset's L-number: what setCheapest found after the whole
// level's pairs had been offered. R53 Step-0's instrument — the sizing-vs-pricing
// separation question ("did {ps,p,s,l} lose on rows or on price?") is answered
// from these lines, and every future costing slice reuses them without
// re-instrumenting.
type traceCost struct {
	level int // relLevel(rel) — the level whose completion produced it
	rel   RelSet
	rows  float64
	paths int // len(Pathlist) at setCheapest time
	kind  string
	// reqouter is the WINNER's RequiredOuter: empty when the path is usable
	// anywhere. It disambiguates the `nli` label — an index-assisted NL whose
	// inner takes bindings from the outer is unparameterised as a PATH when
	// the outer supplies them, and only reqouter says whether the winner can
	// stand in for the rel above.
	reqouter RelSet
	total    float64
	// second/secondTotal is the cheapest pathlist entry that ISN'T the
	// winner, by total. The L6 margin (winner vs second) is what scopes a
	// pricing slice: a 0.7% margin inside one arm is a different job than a
	// 30% gap across arms. Recorded by total, not by use — when the winner
	// (CheapestTotal, unparameterised-only) is NOT the min-total path, the
	// second line names the parameterisation price directly.
	second      string
	secondTotal float64
}

// tracePVeto is one path-level veto: a partial-path producer that did NOT
// file (R54 Step-1, STEP1.md §2). Stored as relsets and named at render, the
// way pairs/costs are, so the names cannot drift from the problem's map.
type tracePVeto struct {
	site  string // base | hash | merge | mergeu
	rel   RelSet // the joinrel (join sites) or base rel (base site); 0 when
	outer RelSet // the orientation tried (join sites); 0 for site=base
	inner RelSet
	veto  string // V0..V9 | B1..B4 | M0..M12 | admitted
	detail string // space-separated key=value, per-veto contract (STEP1 §2)
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
	costs    []traceCost
	// R54 Step-0's admission records (admit/baseCP/gather above), rendered
	// after the cost lines in one problem's block.
	cpAdmits []traceCP
	gathers  []traceGather
	// R54 Step-1's veto records (pveto below), rendered after the gather
	// lines in one problem's block.
	pvetos []tracePVeto
	top    RelSet
	failed string
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

// tracePathKind renders a path's kind the way Step-0 reads it: the join method
// that won the relset, with index-assisted nestloops (inner takes bindings
// from the outer) reading `nli` apart from plain `nl` — they are the same
// PathKind and the costing slices adjudicate them separately. `nli` reports
// the INNER's parameterisation only: whether the PATH itself needs bindings
// from above is reqouter's job, and the two come apart exactly when the outer
// supplies the bindings (the Q9 L4 winner is that shape). Unknown kinds
// render as `kind<N>` so a new producer can never silently collapse into a
// known label.
func tracePathKind(p *Path) string {
	if p == nil {
		return "none"
	}
	switch p.Kind {
	case PathHashJoin:
		return "hash"
	case PathMergeJoin:
		return "merge"
	case PathNestLoop:
		if len(p.Children) > 1 && p.Children[1].RequiredOuter != 0 {
			return "nli"
		}
		return "nl"
	case PathSeqScan:
		return "seq"
	case PathIndexScan:
		return "idx"
	case PathBitmapHeapScan:
		return "bitmap"
	case PathGather:
		return "gather"
	case PathGatherMerge:
		return "gathermerge"
	case PathSort:
		return "sort"
	case PathAgg:
		return "agg"
	default:
		return fmt.Sprintf("kind%d", int(p.Kind))
	}
}

// cost records one relset's L-number after setCheapest ran. Nil-receiver safe
// like offer/decline, so the call site stays unconditional and production is
// untouched when the gate is off.
func (t *searchTrace) cost(rel *RelOptInfo) {
	if t == nil || rel == nil {
		return
	}
	t.costs = append(t.costs, traceCost{
		level:       relLevel(rel.Relids),
		rel:         rel.Relids,
		rows:        rel.Rows,
		paths:       len(rel.Pathlist),
		kind:        tracePathKind(rel.CheapestTotal),
		reqouter:    winnerRequiredOuter(rel),
		total:       cheapestTotal(rel),
		second:      tracePathKind(secondCheapest(rel)),
		secondTotal: secondTotal(rel),
	})
}

// winnerRequiredOuter is the winner's own parameterisation — the admission
// property setCheapest selected on — as distinct from the inner's, which is
// what the `nli` label reports.
func winnerRequiredOuter(rel *RelOptInfo) RelSet {
	if rel.CheapestTotal == nil {
		return 0
	}
	return rel.CheapestTotal.RequiredOuter
}

// secondCheapest is the cheapest pathlist entry that is not the winner, by
// total. Nil when the winner stands alone.
func secondCheapest(rel *RelOptInfo) *Path {
	var best *Path
	for _, p := range rel.Pathlist {
		if p == rel.CheapestTotal {
			continue
		}
		if best == nil || p.Cost.Total < best.Cost.Total {
			best = p
		}
	}
	return best
}

// secondTotal is secondCheapest's total, or NaN when there is no second path.
func secondTotal(rel *RelOptInfo) float64 {
	if s := secondCheapest(rel); s != nil {
		return s.Cost.Total
	}
	return math.NaN()
}

// cheapestTotal is CheapestTotal's total, or NaN when there is none yet — the
// call site records after setCheapest, so NaN means "no unparameterised path",
// itself a diagnosis.
func cheapestTotal(rel *RelOptInfo) float64 {
	if rel.CheapestTotal == nil {
		return math.NaN()
	}
	return rel.CheapestTotal.Cost.Total
}

// traceCP is R54 Step-0's admission record: one joinrel's or base rel's
// ConsiderParallel verdict with the inputs that decided it. For a joinrel the
// record carries both input flags (S1 propagation reads off in1/in2) and the
// first clause that fails the walk (S2 reads off failidx/failkind); for a base
// rel it carries the leaf kind the `relConsiderParallel` arm below saw (S1 at
// the leaves). Recorded at build time, inside the block, so the death level
// is read off one problem's lines without correlating across statements.
type traceCP struct {
	src      string // "join" or "base"
	rel      RelSet
	cp       bool
	in1, in2 bool   // join only: the two inputs' flags
	nclauses int    // join only
	failidx  int    // join only: first failing clause, -1 when admitted
	failkind string // join only: %T of the failing clause expr, "" when admitted
	leaf     string // base only: traceLeafKind of the rel's leaf
}

// traceGather is one `generateUsefulGatherPaths` decision: the rel, how many
// partial paths stood for election, and which gate admitted or refused. S4's
// "generated but lost" vs "never generated" separation reads off partials +
// verdict together with the `cost` line's cheapest kind.
type traceGather struct {
	rel      RelSet
	partials int
	verdict  string // "no-partials" | "no-parallel-mode" | "no-cp" | "mode" | "admitted"
}

// admit records a newly built joinrel's admission verdict. The VERDICT passed
// in is authoritative — it is the flag `makeJoinRel` just stamped, computed by
// `joinrelConsiderParallel` itself. Only the S2 explanation (which clause)
// re-walks, through `firstParallelUnsafeClause`, the same helper the verdict
// loop is written on, with the same cat — so the name cannot disagree with
// the flag about what failed. Nil-receiver safe like cost/offer/decline.
func (t *searchTrace) admit(rel RelSet, cp, in1, in2 bool, clauses []*restrictInfo, cat catalog.Catalog) {
	if t == nil {
		return
	}
	failidx, failkind := -1, ""
	if i := firstParallelUnsafeClause(clauses, cat); i >= 0 {
		failidx = i
		failkind = fmt.Sprintf("%T", clauseExprForTrace(clauses[i]))
	}
	t.cpAdmits = append(t.cpAdmits, traceCP{
		src: "join", rel: rel, cp: cp, in1: in1, in2: in2,
		nclauses: len(clauses), failidx: failidx, failkind: failkind,
	})
}

// clauseExprForTrace unwraps one restrictInfo for %T naming. A nil entry (the
// verdict loop vetoes it outright) has no expr; it names itself.
func clauseExprForTrace(ri *restrictInfo) any {
	if ri == nil {
		return "nil-clause"
	}
	return ri.clause
}

// baseCP records one base rel's admission verdict with the leaf kind the
// `relConsiderParallel` arm saw. Called from `setBaseRelConsiderParallel`,
// which runs on the same searchCtx that owns this trace (relfromjoinlist.go),
// so the line lands in the problem's own block.
func (t *searchTrace) baseCP(rel RelSet, leaf Node, cp bool) {
	if t == nil {
		return
	}
	t.cpAdmits = append(t.cpAdmits, traceCP{
		src: "base", rel: rel, cp: cp, leaf: traceLeafKind(leaf),
	})
}

// traceLeafKind renders a base leaf's kind in `relConsiderParallel`'s own
// vocabulary: Filter wrappers peeled exactly as the verdict peels them, then
// the type-switch arm names. A kind the verdict does not enumerate renders
// "other" — the verdict fails closed there, and so does the name. Leaf kinds
// outside this list (UserSrfScan, GenerateSeries, ScalarFuncScan, catalog
// SRFs) all render "other": that is a deliberate display gap, not a verdict —
// read the cp flag, not the leaf name, for those.
func traceLeafKind(leaf Node) string {
	base := leaf
	for {
		f, ok := base.(*Filter)
		if !ok || f.Child == nil {
			break
		}
		base = f.Child
	}
	switch base.(type) {
	case *SeqScan:
		return "seq"
	case *IndexScan:
		return "idx"
	case *IndexOnlyScan:
		return "idxonly"
	case *BitmapHeapScan:
		return "bitmap"
	case *CTEScan, *MaterializedCTEScan:
		return "cte"
	case *Values:
		return "values"
	default:
		return "other"
	}
}

// gather records one `generateUsefulGatherPaths` decision. Nil-receiver safe;
// the call site passes the verdict constant for the gate that fired, so the
// record cannot drift from the decision.
func (t *searchTrace) gather(rel RelSet, partials int, verdict string) {
	if t == nil {
		return
	}
	t.gathers = append(t.gathers, traceGather{rel: rel, partials: partials, verdict: verdict})
}

// pveto records one partial-path producer call that did not file — or one
// that did (`veto=admitted`), so absence of lines is distinguishable from
// absence of calls (STEP1.md §2). The caller passes the veto name for the
// gate that fired; the record cannot drift from the decision because each
// hook sits on its own early return. Nil-receiver safe like gather: the
// trace is nil in production, and producers that tolerate a nil searchCtx
// (hash V2, merge M0/M1) reach this through tracePVetoCtx below.
func (t *searchTrace) pveto(site string, rel, outer, inner RelSet, veto, detail string) {
	if t == nil {
		return
	}
	t.pvetos = append(t.pvetos, tracePVeto{site: site, rel: rel, outer: outer, inner: inner, veto: veto, detail: detail})
}

// tracePVetoCtx is the pveto entry for producers holding a possibly-nil
// *searchCtx: a veto that fires on a nil ctx has no block to land in, so it
// records nothing — the nil-ctx arm is defensive-only in production (callers
// pass a live ctx) and a veto line for it would be unactionable anyway.
func tracePVetoCtx(s *searchCtx, site string, rel, outer, inner RelSet, veto, detail string) {
	if s == nil {
		return
	}
	s.trace.pveto(site, rel, outer, inner, veto, detail)
}

// traceRelids returns r's relset, or 0 when r is nil: a veto that fires
// before (or on) a nil check still names itself, and the rel renders `{}`.
func traceRelids(r *RelOptInfo) RelSet {
	if r == nil {
		return 0
	}
	return r.Relids
}

// traceJoinTypeName renders a join type in the veto detail's `jt=` field.
// parser.JoinType is int-based with no String method; the names below are
// the SQL keywords, so the detail reads without a decoder ring.
func traceJoinTypeName(jt parser.JoinType) string {
	switch jt {
	case parser.JoinInner:
		return "INNER"
	case parser.JoinLeft:
		return "LEFT"
	case parser.JoinRight:
		return "RIGHT"
	case parser.JoinFull:
		return "FULL"
	case parser.JoinCross:
		return "CROSS"
	case parser.JoinSemi:
		return "SEMI"
	case parser.JoinAnti:
		return "ANTI"
	default:
		return "other"
	}
}

// traceUpperGate is the S3 line: one post-pass tournament's verdict, where
// the search trace cannot reach. The partial-agg / partial-sort tournaments
// run post-cache over finished Nodes (no searchCtx in scope, after the
// problem block has emitted), so this is a standalone stderr line in
// `traceSeamDecline`'s style rather than a block member — Step-0 runs one
// statement at a time, so log proximity correlates it. `detail` is
// space-separated key=value pairs (workers, divisor, mode).
func traceUpperGate(gate, verdict, detail string) {
	if !dpTraceEnabled() {
		return
	}
	fmt.Fprintf(os.Stderr, "%s upper gate=%s verdict=%s %s\n", traceTag, gate, verdict, detail)
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
	traceCostTag = traceTag + " cost"
	// R54 Step-0's admission lines (cpAdmits/gathers above) plus the
	// standalone post-pass line (traceUpperGate). All three are recognised
	// (not Malformed) by the enumtrace parser; see its cpadmit/cpgather/upper
	// cases.
	traceCPAdmitTag = traceTag + " cpadmit"
	traceGatherTag  = traceTag + " cpgather"
	traceUpperTag   = traceTag + " upper"
	// R54 Step-1's veto lines (pveto above). Recognised (not Malformed) by
	// the enumtrace parser; see its pveto case.
	tracePVetoTag = traceTag + " pveto"
	traceEnd        = traceTag + " end"
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
	for _, c := range t.costs {
		fmt.Fprintf(&b, "%s lev=%d rel=%s rows=%g npaths=%d cheapest=%s reqouter=%s total=%g second=%s secondtotal=%g\n",
			traceCostTag, c.level, t.relsetName(c.rel), c.rows, c.paths, c.kind,
			t.relsetName(c.reqouter), c.total, c.second, c.secondTotal)
	}
	for _, c := range t.cpAdmits {
		if c.src == "base" {
			fmt.Fprintf(&b, "%s src=base rel=%s cp=%d leaf=%s\n",
				traceCPAdmitTag, t.relsetName(c.rel), boolBit(c.cp), c.leaf)
			continue
		}
		failkind := c.failkind
		if failkind == "" {
			failkind = "none"
		}
		fmt.Fprintf(&b, "%s src=join rel=%s cp=%d in1=%d in2=%d nclauses=%d failidx=%d failkind=%s\n",
			traceCPAdmitTag, t.relsetName(c.rel), boolBit(c.cp),
			boolBit(c.in1), boolBit(c.in2), c.nclauses, c.failidx, failkind)
	}
	for _, g := range t.gathers {
		fmt.Fprintf(&b, "%s rel=%s partials=%d verdict=%s\n",
			traceGatherTag, t.relsetName(g.rel), g.partials, g.verdict)
	}
	for _, v := range t.pvetos {
		// The base site tries no orientation (`dir=-`, STEP1.md §2); a
		// zero relset (B1's whole-call skip, or a veto on a nil rel)
		// renders `-` rather than `{}` so it harvests distinctly.
		dir := "-"
		if v.site != "base" {
			dir = t.relsetName(v.outer) + "+" + t.relsetName(v.inner)
		}
		rel := t.relsetName(v.rel)
		if v.rel == 0 {
			rel = "-"
		}
		fmt.Fprintf(&b, "%s site=%s rel=%s dir=%s veto=%s detail=%s\n",
			tracePVetoTag, v.site, rel, dir, v.veto, v.detail)
	}
	status := "ok"
	if t.failed != "" {
		status = t.failed
	}
	fmt.Fprintf(&b, "%s top=%s pairs=%d declined=%d costs=%d status=%s\n",
		traceEnd, t.relsetName(t.top), len(t.pairs), len(t.declined), len(t.costs), status)
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
