// Package estimateaudit implements the class-(a) regression tripwire of
// docs/design/leftdeep-joins/09-verification-and-acceptance.md §5: for a
// query's `EXPLAIN ANALYZE` output it compares the planner's per-node row
// ESTIMATE against the executor's ACTUAL row count and reports every joinrel
// whose estimate is off by more than a stated factor.
//
// Why a library and not a shell one-liner: §5 requires the audit to be
// "checked by tooling rather than by eye", and the two numbers being compared
// do not mean the same thing on both sides of the comparison —
//
//   - the estimate is clamped at 1 by the planner (`EstimateRows` never
//     reports 0), so a 0-row actual has to be clamped the same way or every
//     empty joinrel reads as an infinite misestimate;
//   - goopg prints the actual row count CUMULATIVELY across loops
//     (`instrumentedOp.stats.rowsOut` is a plain per-row counter,
//     internal/executor/instrument.go, rendered verbatim at
//     internal/executor/operators_explain.go's `actual rows=` label), whereas
//     upstream PG divides by nloops and prints the per-loop AVERAGE
//     (explain.c `ExplainNode`). A tool that "helpfully" multiplied by loops
//     the way PG's output requires would inflate every nested-loop inner node
//     by exactly the loop count. The divergence is recorded in
//     .ralph/deferral_ledger.md; until it is fixed, `Node.ActualTotal` is the
//     printed value as-is.
//
// The unit under audit is the JOINREL, not the node: §5's acceptance
// criterion is stated at "the final joinrel" (the outermost join of the
// query), with a looser 10³ tripwire on every intermediate one.
package estimateaudit

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultTripwire is §5's "flag any joinrel whose estimate is > 10³× off".
const DefaultTripwire = 1000.0

// DefaultFinalJoinMax is §5's tighter bar on the final joinrel — the one Q9
// must meet to show the 04 §3 improvement ("≤ 10²× at the final joinrel").
const DefaultFinalJoinMax = 100.0

// nodeLineRe matches a plan node's cost suffix. It doubles as the
// discriminator between node lines and the detail lines nested underneath
// them ("Hash Cond: ...", "Rows Removed by Filter: N"), which carry no cost
// parenthetical. A plan captured with COSTS OFF therefore parses as having no
// nodes at all — Audit reports that as an error rather than as a clean run.
var nodeLineRe = regexp.MustCompile(`\(cost=[0-9.]+\.\.[0-9.]+ rows=(\d+) width=\d+\)`)

// actualRe matches both ANALYZE renderings: with timing
// ("actual time=0.001..0.002 rows=5.00 loops=1") and with TIMING OFF
// ("actual rows=5.00 loops=1").
var actualRe = regexp.MustCompile(`\(actual (?:time=[0-9.]+\.\.[0-9.]+ )?rows=([0-9.]+) loops=(\d+)\)`)

// joinLabels are the node-label prefixes that describePlan/describePlanVerbose
// (internal/executor/operators_explain.go) emits for a join. "Multi-Way Hash
// Join" is goopg's own MultiHashJoin rendering and is included deliberately:
// while it survives it is a joinrel like any other and its estimate is as
// auditable as a two-way join's.
//
// goopg spells the join type in the label itself, matching upstream
// ("Hash Anti Join", "Merge Left Join", "Nested Loop Semi Join" —
// explain.c's `ExplainNode`; see joinLabel in operators_explain.go). The
// legacy parenthetical spelling ("Hash Join (ANTI)") must still classify,
// because the committed leftdeep-joins plan captures predate the S3
// relabel, and §4's parity gate audits a PG 18.3 plan through this same
// parser: a reference plan whose joins did not classify would read as a
// query PG estimated perfectly.
var joinLabels = []string{
	"Nested Loop",
	"Hash Join",
	"Merge Join",
	"Multi-Way Hash Join",
}

// upstreamJoinPrefixes are the label prefixes under which upstream may spell a
// join type before the word "Join" ("Hash Anti Join"). The word itself is
// required, so PG's bare "Hash" (the hash-build node) and "Merge Append" do
// not classify, and neither does goopg's "HashAggregate" (no space).
var upstreamJoinPrefixes = []string{"Hash ", "Merge "}

// Node is one plan node parsed out of an EXPLAIN ANALYZE rendering.
type Node struct {
	Raw     string  // the source line, verbatim
	Depth   int     // 0 = root; one level per "  " of indentation
	Label   string  // node label with the cost/actual suffixes stripped
	EstRows int64   // planner estimate (already clamped >= 1 by EstimateRows)
	Actual  float64 // executor count as printed (cumulative across loops)
	Loops   int64
	HasAct  bool // false when the plan was captured without ANALYZE
	IsJoin  bool

	// Rels is the node's joinrel identity: the sorted set of base-relation
	// leaves underneath it, which is what upstream calls `RelOptInfo.relids`
	// and the only key under which a goopg joinrel and a PG joinrel are the
	// SAME estimation problem (§4's parity gate). Populated by Audit; see
	// attachRels in parity.go.
	Rels []string
}

// RelKey is the canonical form of Rels — the map key §4's parity gate joins
// the two engines' plans on.
func (n Node) RelKey() string { return strings.Join(n.Rels, "+") }

// ActualTotal is the total row count the node emitted. See the package
// comment: goopg's printed value is already cumulative, so this is the
// identity — the method exists so the one place that depends on the
// divergence is named, and so fixing the renderer to PG semantics is a
// one-line change here.
func (n Node) ActualTotal() float64 { return n.Actual }

// Ratio is the misestimate factor: how many times off the estimate is, in
// whichever direction it is wrong. Both sides are floored at 1 because the
// planner's own estimate is (PG's clamp_row_est, costsize.c) — comparing an
// unclamped 0 actual against a clamped 1 estimate would report a misestimate
// that the planner had no way to avoid.
func (n Node) Ratio() float64 {
	if !n.HasAct {
		return 0
	}
	e := math.Max(float64(n.EstRows), 1)
	a := math.Max(n.ActualTotal(), 1)
	if e > a {
		return e / a
	}
	return a / e
}

// Overestimated reports whether the planner guessed HIGH. The direction
// matters for the 09 §6 attribution protocol: an overestimate and an
// underestimate of the same magnitude push the join order opposite ways.
func (n Node) Overestimated() bool {
	return n.HasAct && float64(n.EstRows) > n.ActualTotal()
}

// QueryReport is the audit of a single query.
type QueryReport struct {
	Name  string
	Err   string // capture error (query failed / timed out); Nodes is then empty
	Nodes []Node
	Joins []Node // Nodes filtered to joins, in plan order

	// Final is the outermost join — the "final joinrel" §5's acceptance
	// criterion is stated at. nil when the query has no join.
	Final *Node
	// Worst is the join with the largest Ratio. nil when there is no join.
	Worst *Node
}

// Parse extracts the plan nodes from one EXPLAIN (ANALYZE) rendering.
func Parse(text string) []Node {
	var out []Node
	for _, line := range strings.Split(text, "\n") {
		m := nodeLineRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		est, err := strconv.ParseInt(line[m[2]:m[3]], 10, 64)
		if err != nil {
			continue
		}
		n := Node{Raw: line, EstRows: est}
		n.Depth, n.Label = splitPrefix(line[:m[0]])
		n.IsJoin = isJoinLabel(n.Label)
		if a := actualRe.FindStringSubmatch(line); a != nil {
			n.Actual, _ = strconv.ParseFloat(a[1], 64)
			n.Loops, _ = strconv.ParseInt(a[2], 10, 64)
			n.HasAct = true
		}
		out = append(out, n)
	}
	return out
}

// splitPrefix turns the part of the line before the cost suffix into a depth
// and a bare label. The renderer emits `"  "*depth + "->  " + label` for every
// non-root node and a bare label for the root
// (internal/executor/operators_explain.go, walkPlanAnalyzeFiltered).
func splitPrefix(head string) (int, string) {
	spaces := 0
	for spaces < len(head) && head[spaces] == ' ' {
		spaces++
	}
	rest := head[spaces:]
	depth := spaces / 2
	if strings.HasPrefix(rest, "->  ") {
		rest = rest[len("->  "):]
	} else {
		// Root line: no arrow, no indentation.
		depth = 0
	}
	return depth, strings.TrimRight(rest, " ")
}

func isJoinLabel(label string) bool {
	for _, p := range joinLabels {
		if strings.HasPrefix(label, p) {
			return true
		}
	}
	for _, p := range upstreamJoinPrefixes {
		if strings.HasPrefix(label, p) && strings.Contains(label, "Join") {
			return true
		}
	}
	return false
}

// Audit builds the report for one query's captured plan text.
func Audit(name, text string) QueryReport {
	r := QueryReport{Name: name, Nodes: Parse(text)}
	attachRels(r.Nodes)
	if len(r.Nodes) == 0 {
		r.Err = "no plan nodes parsed (captured with COSTS OFF, or the query errored)"
		return r
	}
	for _, n := range r.Nodes {
		if n.IsJoin {
			r.Joins = append(r.Joins, n)
		}
	}
	if len(r.Joins) == 0 {
		return r
	}
	// The final joinrel is the OUTERMOST join: smallest depth, and among
	// equals the one the renderer printed first (plan order is
	// outer-before-inner, so the first at a given depth is on the spine).
	final := 0
	worst := 0
	for i, j := range r.Joins {
		if j.Depth < r.Joins[final].Depth {
			final = i
		}
		if j.Ratio() > r.Joins[worst].Ratio() {
			worst = i
		}
	}
	r.Final = &r.Joins[final]
	r.Worst = &r.Joins[worst]
	return r
}

// AuditError builds the report for a query whose capture failed. Recording
// the failure keeps a timed-out query distinguishable from a clean one in the
// committed output — a silently dropped query would read as "audited, fine".
func AuditError(name, err string) QueryReport {
	return QueryReport{Name: name, Err: err}
}

// Violation is one joinrel over a stated threshold.
type Violation struct {
	Query     string
	Node      Node
	Threshold float64
	Final     bool // the query's final joinrel (the tighter bar)
}

func (v Violation) String() string {
	dir := "under"
	if v.Node.Overestimated() {
		dir = "over"
	}
	where := "joinrel"
	if v.Final {
		where = "FINAL joinrel"
	}
	return fmt.Sprintf("%s: %s %q est=%d actual=%.0f ratio=%.1fx (%sestimated, threshold %.0fx)",
		v.Query, where, v.Node.Label, v.Node.EstRows, v.Node.ActualTotal(), v.Node.Ratio(), dir, v.Threshold)
}

// Q21AntiJoinMax is the per-query bar on Q21's final joinrel. It is NOT an
// estimator target: PG 18.3 estimates `rows=1` for the same anti-join against
// the same actual of 4 003 (measured on the reference cluster 2026-08-05,
// analysis/leftdeep-joins/2026-08-05-p56g-README.md §3). Upstream does not
// price a `<>` clause through eqjoinsel for JOIN_SEMI/JOIN_ANTI at all —
// `neqjoinsel` (selfuncs.c) returns `1 - nullfrac` by documented design — and
// Q21's eq clause is a self-join on lineitem.l_orderkey, so nd1 = nd2 and
// every branch returns 1.0 in both engines. Holding goopg to the absolute
// tripwire here would demand a DIVERGENCE from PG in a PG-parity milestone.
//
// The bar is a bar and not an exemption on purpose: at 4 003× measured on both
// engines, 5 000× leaves room for ANALYZE's ~5 % resampling noise and nothing
// more, so a real regression of this joinrel still trips it.
const Q21AntiJoinMax = 5000.0

// FinalBar is a per-query bar on the final joinrel, carrying the reason it
// differs from the default. The reason is rendered into the committed
// artifact: a bare number in a map is indistinguishable from a muted alarm six
// months later.
type FinalBar struct {
	Max float64
	Why string
}

// Thresholds parameterises the two bars of §5.
type Thresholds struct {
	// Tripwire applies to every joinrel (default 10³).
	Tripwire float64
	// FinalJoin applies to the outermost joinrel only (default 10²), and
	// per-query overrides live in FinalJoinPerQuery — §5 states the ≤10²
	// bar for Q9 specifically, as the demonstration that 04 §3's mechanisms
	// worked; the other queries are held to the tripwire until P5.9 sets
	// their baselines.
	FinalJoin float64
	// FinalJoinPerQuery overrides FinalJoin for named queries.
	FinalJoinPerQuery map[string]FinalBar
}

// DefaultThresholds is §5 as written: 10³ everywhere, with Q9's final joinrel
// held to 10² — plus Q21's PG-parity bar (see Q21AntiJoinMax), which §5 could
// not state before the reference measurement existed.
func DefaultThresholds() Thresholds {
	return Thresholds{
		Tripwire:  DefaultTripwire,
		FinalJoin: DefaultTripwire,
		FinalJoinPerQuery: map[string]FinalBar{
			"Q9": {DefaultFinalJoinMax, "09 §5's stated demonstration that 04 §3's sizing mechanisms worked"},
			"Q21": {Q21AntiJoinMax, "PG 18.3 estimates rows=1 for the same ANTI against the same actual " +
				"(neqjoinsel returns 1-nullfrac for JOIN_ANTI by design); closing it would diverge from PG"},
		},
	}
}

func (t Thresholds) finalFor(query string) float64 {
	if v, ok := t.FinalJoinPerQuery[query]; ok {
		return v.Max
	}
	if t.FinalJoin > 0 {
		return t.FinalJoin
	}
	return t.Tripwire
}

// Violations lists every joinrel over its applicable threshold, worst first.
// A query that failed to capture is NOT a violation — it is an unmeasured
// query, reported separately by Unmeasured, because treating it as a pass and
// treating it as a failure are both lies.
func Violations(reports []QueryReport, t Thresholds) []Violation {
	var out []Violation
	for _, r := range reports {
		if r.Err != "" {
			continue
		}
		for i := range r.Joins {
			j := r.Joins[i]
			if !j.HasAct {
				continue
			}
			isFinal := r.Final != nil && r.Final.Raw == j.Raw && r.Final.Depth == j.Depth
			th := t.Tripwire
			if isFinal {
				th = t.finalFor(r.Name)
			}
			if j.Ratio() > th {
				out = append(out, Violation{Query: r.Name, Node: j, Threshold: th, Final: isFinal})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Node.Ratio() > out[j].Node.Ratio() })
	return out
}

// Unmeasured lists the queries whose plan could not be captured.
func Unmeasured(reports []QueryReport) []QueryReport {
	var out []QueryReport
	for _, r := range reports {
		if r.Err != "" {
			out = append(out, r)
		}
	}
	return out
}

// Render writes the committed-artifact form of the audit: one block per
// query listing every joinrel with its estimate, actual and ratio, then the
// violation summary. The output is deterministic (no timings, no wall-clock)
// so successive runs diff cleanly under analysis/leftdeep-joins/.
func Render(reports []QueryReport, t Thresholds) string {
	var b strings.Builder
	for _, r := range reports {
		fmt.Fprintf(&b, "=== %s\n", r.Name)
		if r.Err != "" {
			fmt.Fprintf(&b, "  UNMEASURED: %s\n\n", r.Err)
			continue
		}
		if len(r.Joins) == 0 {
			fmt.Fprintf(&b, "  (no join nodes; %d plan nodes)\n\n", len(r.Nodes))
			continue
		}
		for i := range r.Joins {
			j := r.Joins[i]
			mark := "  "
			if r.Final != nil && r.Final.Raw == j.Raw {
				mark = "F "
			}
			if !j.HasAct {
				fmt.Fprintf(&b, "  %sd%-2d %-40s est=%-10d actual=?        (no ANALYZE)\n",
					mark, j.Depth, truncate(j.Label, 40), j.EstRows)
				continue
			}
			dir := "under"
			if j.Overestimated() {
				dir = "over"
			}
			fmt.Fprintf(&b, "  %sd%-2d %-40s est=%-10d actual=%-10.0f ratio=%.1fx %s\n",
				mark, j.Depth, truncate(j.Label, 40), j.EstRows, j.ActualTotal(), j.Ratio(), dir)
		}
		b.WriteString("\n")
	}

	v := Violations(reports, t)
	fmt.Fprintf(&b, "=== SUMMARY (tripwire %.0fx; final joinrel %.0fx, per-query overrides %v)\n",
		t.Tripwire, t.FinalJoin, sortedOverrides(t.FinalJoinPerQuery))
	if len(v) == 0 {
		b.WriteString("  no joinrel over threshold\n")
	}
	for _, x := range v {
		fmt.Fprintf(&b, "  VIOLATION %s\n", x)
	}
	for _, u := range Unmeasured(reports) {
		fmt.Fprintf(&b, "  UNMEASURED %s: %s\n", u.Name, u.Err)
	}
	for _, k := range sortedKeys(t.FinalJoinPerQuery) {
		fmt.Fprintf(&b, "  OVERRIDE %s final joinrel <=%.0fx — %s\n", k, t.FinalJoinPerQuery[k].Max, t.FinalJoinPerQuery[k].Why)
	}
	return b.String()
}

func sortedOverrides(m map[string]FinalBar) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, fmt.Sprintf("%s<=%.0fx", k, m[k].Max))
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
