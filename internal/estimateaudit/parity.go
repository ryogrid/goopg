// Per-joinrel parity against the PG 18.3 reference — the bar
// docs/design/leftdeep-joins/09-verification-and-acceptance.md §4 asks P5.9 to
// certify, restated after the 2026-08-05 reference measurement.
//
// Why the absolute tripwire of §5 cannot be that bar: on TPC-H Q18 **PG itself
// misestimates the final joinrel by 1 674×** (it dedups the GROUP BY subquery
// to a plain inner join and estimates 117 159 against an actual of 70), which
// is over §5's own 10³ tripwire. A milestone whose goal is PG-shaped planning
// cannot be gated on a bar the reference implementation fails: the absolute
// factor answers "is this estimate good?", and the question P5.9 has to answer
// is "is this estimate WORSE THAN PG's?". Those differ wherever the query
// itself is hard to estimate — exactly where a ratchet is worth having.
//
// The unit of comparison is the JOINREL, identified the way upstream
// identifies it: by the set of base relations underneath it
// (`RelOptInfo.relids`). Two plans with different join ORDERS still contain the
// same joinrel whenever they build the same relation set, and the ACTUAL row
// count of that joinrel is a property of the query and the data, not of the
// plan — so goopg's misestimate factor for {customer,orders} and PG's for
// {customer,orders} are the same estimation problem even when the two engines
// reached it by different spines. A joinrel only one engine built is NOT a
// parity failure; it is a shape divergence, counted separately and classed per
// §6.
package estimateaudit

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// DefaultParitySlack is how many times worse than the reference goopg's
// misestimate may be before the ratchet counts it. It is not 1 because §4
// deliberately declines a match-all bar "while cost constants and stats
// fidelity still differ" — the ratchet exists to make DRIFT visible, and an
// order of magnitude past the reference is drift no stats-fidelity gap
// explains.
const DefaultParitySlack = 10.0

// DefaultParityFloor keeps the ratchet off joinrels that are accurate in
// absolute terms. Without it a joinrel PG estimates exactly (ratio 1.0) and
// goopg estimates within 20× would count as a 20× parity failure, which says
// nothing about join order at that size.
const DefaultParityFloor = 100.0

// ParityBar parameterises §4's ratchet.
type ParityBar struct {
	Slack float64 // goopg ratio / reference ratio must stay under this
	Floor float64 // ... and goopg's own ratio must exceed this to count
}

// DefaultParityBar is §4's ratchet as restated by P5.6-g-iii.
func DefaultParityBar() ParityBar {
	return ParityBar{Slack: DefaultParitySlack, Floor: DefaultParityFloor}
}

// ParityStatus classifies one joinrel of one query.
type ParityStatus string

const (
	ParityMatched   ParityStatus = "matched"    // both engines built this joinrel
	ParityGoopgOnly ParityStatus = "goopg-only" // shape divergence (§6 class (b))
	ParityRefOnly   ParityStatus = "pg-only"    // shape divergence (§6 class (b))
)

// ParityRow is one joinrel, on one or both sides.
type ParityRow struct {
	Query  string
	Rels   []string
	Status ParityStatus
	Goopg  *Node // nil when Status is ParityRefOnly
	Ref    *Node // nil when Status is ParityGoopgOnly
	Final  bool  // this is goopg's final joinrel (or the reference's, for pg-only rows)

	// Ambiguous marks a key that more than one join level in the same plan
	// resolved to — see joinsByRelKey. The row still compares the outermost
	// occurrence, but the reader is told, because the printed plan does not
	// contain enough information to separate the two.
	Ambiguous bool
}

func (r ParityRow) goopgRatio() float64 {
	if r.Goopg == nil {
		return 0
	}
	return r.Goopg.Ratio()
}

func (r ParityRow) refRatio() float64 {
	if r.Ref == nil {
		return 0
	}
	return r.Ref.Ratio()
}

// Excess is the ratio-of-ratios: how many times worse than the reference
// goopg's estimate of this joinrel is. 1.0 means "as good as PG"; below 1.0
// means goopg is the more accurate of the two. Unmatched rows have no excess.
func (r ParityRow) Excess() float64 {
	if r.Status != ParityMatched {
		return 0
	}
	ref := math.Max(r.refRatio(), 1)
	return r.goopgRatio() / ref
}

// Violates reports whether this row trips the ratchet: goopg materially worse
// than the reference (Slack) AND materially wrong in absolute terms (Floor).
// Both conditions, because either alone flags something the ratchet does not
// mean — see DefaultParityFloor.
func (r ParityRow) Violates(p ParityBar) bool {
	if r.Status != ParityMatched {
		return false
	}
	return r.Excess() > p.Slack && r.goopgRatio() > p.Floor
}

func (r ParityRow) String() string {
	rels := strings.Join(r.Rels, "+")
	switch r.Status {
	case ParityGoopgOnly:
		return fmt.Sprintf("%s: {%s} built by goopg only (est=%d actual=%.0f ratio=%.1fx); PG built no such joinrel",
			r.Query, rels, r.Goopg.EstRows, r.Goopg.ActualTotal(), r.goopgRatio())
	case ParityRefOnly:
		return fmt.Sprintf("%s: {%s} built by PG only (est=%d actual=%.0f ratio=%.1fx); goopg built no such joinrel",
			r.Query, rels, r.Ref.EstRows, r.Ref.ActualTotal(), r.refRatio())
	default:
		return fmt.Sprintf("%s: {%s} goopg est=%d actual=%.0f ratio=%.1fx vs PG est=%d actual=%.0f ratio=%.1fx — %.1fx worse than the reference",
			r.Query, rels, r.Goopg.EstRows, r.Goopg.ActualTotal(), r.goopgRatio(),
			r.Ref.EstRows, r.Ref.ActualTotal(), r.refRatio(), r.Excess())
	}
}

// Parity joins the two engines' audits per query and per joinrel. Queries
// unmeasured on either side are skipped (a missing reference is not a pass and
// not a failure — RenderParity names them).
func Parity(goopg, reference []QueryReport) []ParityRow {
	refByName := make(map[string]QueryReport, len(reference))
	for _, r := range reference {
		refByName[r.Name] = r
	}
	var out []ParityRow
	for _, g := range goopg {
		ref, ok := refByName[g.Name]
		if !ok || g.Err != "" || ref.Err != "" {
			continue
		}
		gJoins, gDup := joinsByRelKey(g)
		rJoins, rDup := joinsByRelKey(ref)
		for _, key := range sortedKeys(gJoins) {
			gn := gJoins[key]
			row := ParityRow{Query: g.Name, Rels: gn.Rels, Goopg: gn, Status: ParityGoopgOnly,
				Final:     g.Final != nil && g.Final.RelKey() == key,
				Ambiguous: gDup[key] > 1}
			if rn, ok := rJoins[key]; ok {
				row.Ref = rn
				row.Status = ParityMatched
				row.Ambiguous = row.Ambiguous || rDup[key] > 1
			}
			out = append(out, row)
		}
		for _, key := range sortedKeys(rJoins) {
			if _, ok := gJoins[key]; ok {
				continue
			}
			rn := rJoins[key]
			out = append(out, ParityRow{Query: g.Name, Rels: rn.Rels, Ref: rn, Status: ParityRefOnly,
				Final:     ref.Final != nil && ref.Final.RelKey() == key,
				Ambiguous: rDup[key] > 1})
		}
	}
	return out
}

// joinsByRelKey indexes a report's joins by joinrel identity, and counts how
// many join levels resolved to each key. A join with no ANALYZE
// instrumentation is dropped: it has no actual to compare.
//
// A repeated key is not a bug in the caller but a limit of the printed plan.
// TPC-H Q18 is the live case: the subquery's `lineitem` is a different range
// table entry from the outer query's, but goopg prints both as
// `Seq Scan on public.lineitem` with no alias, so the SEMI above them keys the
// same as the inner join below it. Upstream's relids would separate them; the
// text cannot. The outermost occurrence wins (final-joinrel comparisons are
// what §4 is stated on) and the collision is surfaced via ParityRow.Ambiguous
// rather than silently resolved.
func joinsByRelKey(r QueryReport) (map[string]*Node, map[string]int) {
	out := make(map[string]*Node, len(r.Joins))
	dup := make(map[string]int, len(r.Joins))
	for i := range r.Joins {
		n := &r.Joins[i]
		if !n.HasAct || len(n.Rels) == 0 {
			continue
		}
		key := n.RelKey()
		dup[key]++
		if prev, ok := out[key]; ok && prev.Depth <= n.Depth {
			continue
		}
		out[key] = n
	}
	return out, dup
}

// ParityViolations lists the ratchet failures, worst first.
func ParityViolations(rows []ParityRow, p ParityBar) []ParityRow {
	var out []ParityRow
	for _, r := range rows {
		if r.Violates(p) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Excess() > out[j].Excess() })
	return out
}

// ParityMismatches lists the shape divergences (joinrels only one engine
// built). §4 counts these into the pinned mismatch budget; they are not
// parity violations because there is nothing to compare.
func ParityMismatches(rows []ParityRow) []ParityRow {
	var out []ParityRow
	for _, r := range rows {
		if r.Status != ParityMatched {
			out = append(out, r)
		}
	}
	return out
}

// RenderParity writes the parity column of the committed artifact: one line
// per joinrel with both engines' ratios and the excess, then the ratchet
// summary. Deterministic, like Render.
func RenderParity(goopg, reference []QueryReport, rows []ParityRow, p ParityBar) string {
	var b strings.Builder
	b.WriteString("=== PER-JOINREL PARITY vs PG 18.3 (09 §4 ratchet)\n")

	byQuery := map[string][]ParityRow{}
	for _, r := range rows {
		byQuery[r.Query] = append(byQuery[r.Query], r)
	}
	for _, q := range sortedKeys(byQuery) {
		fmt.Fprintf(&b, "--- %s\n", q)
		for _, r := range byQuery[q] {
			mark := "  "
			if r.Final {
				mark = "F "
			}
			if r.Ambiguous {
				mark = mark[:1] + "~" // "F~" / " ~", still two columns wide
			}
			switch r.Status {
			case ParityMatched:
				fmt.Fprintf(&b, "  %s%-46s goopg %8.1fx | PG %8.1fx | excess %7.1fx%s\n",
					mark, truncate("{"+strings.Join(r.Rels, "+")+"}", 46),
					r.goopgRatio(), r.refRatio(), r.Excess(), violMark(r, p))
			case ParityGoopgOnly:
				fmt.Fprintf(&b, "  %s%-46s goopg %8.1fx | PG        - | SHAPE (goopg-only joinrel)\n",
					mark, truncate("{"+strings.Join(r.Rels, "+")+"}", 46), r.goopgRatio())
			case ParityRefOnly:
				fmt.Fprintf(&b, "  %s%-46s goopg        - | PG %8.1fx | SHAPE (PG-only joinrel)\n",
					mark, truncate("{"+strings.Join(r.Rels, "+")+"}", 46), r.refRatio())
			}
		}
	}

	v := ParityViolations(rows, p)
	m := ParityMismatches(rows)
	var ambiguous int
	for _, r := range rows {
		if r.Ambiguous {
			ambiguous++
		}
	}
	fmt.Fprintf(&b, "\n=== PARITY SUMMARY (slack %.0fx, floor %.0fx)\n", p.Slack, p.Floor)
	fmt.Fprintf(&b, "  joinrels compared: %d matched, %d shape-divergent, %d ambiguous "+
		"(a relation scanned twice without an alias — outermost occurrence compared)\n",
		len(rows)-len(m), len(m), ambiguous)
	if len(v) == 0 {
		b.WriteString("  no joinrel worse than the reference beyond the ratchet\n")
	}
	for _, r := range v {
		fmt.Fprintf(&b, "  PARITY-VIOLATION %s\n", r)
	}
	fmt.Fprintf(&b, "  RATCHET parity_violations=%d shape_mismatches=%d\n", len(v), len(m))
	for _, name := range uncomparedQueries(goopg, reference) {
		fmt.Fprintf(&b, "  UNCOMPARED %s\n", name)
	}
	return b.String()
}

func violMark(r ParityRow, p ParityBar) string {
	if r.Violates(p) {
		return "  <== PARITY-VIOLATION"
	}
	return ""
}

// uncomparedQueries names every query the parity gate could not compare, with
// the side that is missing. §5's rule applies here too: a query silently
// absent from the comparison reads as a compared-and-clean one.
func uncomparedQueries(goopg, reference []QueryReport) []string {
	refByName := map[string]QueryReport{}
	for _, r := range reference {
		refByName[r.Name] = r
	}
	var out []string
	for _, g := range goopg {
		ref, ok := refByName[g.Name]
		switch {
		case !ok:
			out = append(out, g.Name+": no reference plan captured")
		case g.Err != "":
			out = append(out, g.Name+": goopg unmeasured ("+g.Err+")")
		case ref.Err != "":
			out = append(out, g.Name+": reference unmeasured ("+ref.Err+")")
		}
	}
	return out
}

// attachRels computes every node's joinrel identity from the printed plan.
//
// The tree is reconstructed from indentation alone, which is why the depth
// STEP must never be assumed: goopg indents two spaces per level and upstream
// six, so the rule is "strictly deeper than mine, before the next node at my
// depth or shallower, is my descendant" rather than any arithmetic on depth.
func attachRels(nodes []Node) {
	if len(nodes) == 0 {
		return
	}
	parent := make([]int, len(nodes))
	var stack []int
	for i := range nodes {
		for len(stack) > 0 && nodes[stack[len(stack)-1]].Depth >= nodes[i].Depth {
			stack = stack[:len(stack)-1]
		}
		parent[i] = -1
		if len(stack) > 0 {
			parent[i] = stack[len(stack)-1]
		}
		stack = append(stack, i)
	}
	// Children always follow their parent in the printed order, so one
	// backwards pass propagates every leaf to every ancestor.
	for i := len(nodes) - 1; i >= 0; i-- {
		if rel, ok := leafRel(nodes[i].Label); ok {
			nodes[i].Rels = append(nodes[i].Rels, rel)
		}
		nodes[i].Rels = dedupeSorted(nodes[i].Rels)
		if p := parent[i]; p >= 0 {
			nodes[p].Rels = append(nodes[p].Rels, nodes[i].Rels...)
		}
	}
}

// leafRel extracts the base relation a scan node reads, in a form comparable
// across the two engines' renderings:
//
//	goopg: "Seq Scan on public.lineitem l1 (stats)" / "Index Scan using public.orders_pk on public.orders"
//	PG:    "Seq Scan on lineitem l1"                / "Index Scan using orders_pkey on orders"
//
// The ALIAS wins when present, because that is what distinguishes the three
// lineitem scans of Q21 — without it a self-join's joinrels collapse into one
// key. Schema qualification and goopg's "(stats)" marker are dropped: they are
// rendering differences, not identity.
func leafRel(label string) (string, bool) {
	// A bitmap index scan names an INDEX, not a relation; its parent bitmap
	// heap scan supplies the relation. Counting it would invent a leaf.
	if strings.HasPrefix(label, "Bitmap Index Scan") {
		return "", false
	}
	i := strings.LastIndex(label, " on ")
	if i < 0 {
		return "", false
	}
	rest := strings.TrimSpace(label[i+len(" on "):])
	rest = strings.ReplaceAll(rest, "(stats)", "")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", false
	}
	name := fields[0]
	if len(fields) > 1 && !strings.HasPrefix(fields[1], "(") {
		name = fields[1] // alias
	}
	if j := strings.LastIndex(name, "."); j >= 0 {
		name = name[j+1:]
	}
	name = strings.Trim(name, `"`)
	if name == "" {
		return "", false
	}
	return name, true
}

func dedupeSorted(in []string) []string {
	if len(in) < 2 {
		return in
	}
	sort.Strings(in)
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// SplitPlansFile splits a `<label>.plans.txt` artifact (written by
// cmd/estimate-audit --keep-plans) back into per-query plan text, so a parity
// run can be re-derived offline from committed evidence instead of re-running
// a 13-minute power run.
func SplitPlansFile(text string) []QueryReport {
	var out []QueryReport
	var name string
	var body strings.Builder
	flush := func() {
		if name == "" {
			return
		}
		out = append(out, Audit(name, body.String()))
		body.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "=== ") {
			flush()
			name = strings.TrimSpace(strings.TrimPrefix(line, "=== "))
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()
	return out
}
