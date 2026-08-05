// The enumeration-provenance channel: the SEARCH side of clause 6
// (M0127-P5.9-l-ii, docs/design/leftdeep-joins/09 §3.11).
//
// spine.go reads the pairing each engine CHOSE and names the bushy partitions
// PG 18.3 chose that goopg's plan does not contain. It closes with the one
// thing it cannot decide: a PG pairing missing from goopg's plan may have been
// enumerated and lost on cost (09 §4 admits that — cost/stats fidelity), or
// never enumerated at all (09 §4 fails that — a shape the search cannot
// express). Both predict the same printed plan.
//
// This file reads the other end. `internal/planner/joinsearchtrace.go` writes a
// `DPTRACE` block per join problem into the server log under
// `GOOPG_PGSHAPED_DP_TRACE=1`, listing every `(outer relset, inner relset,
// phase)` triple `makeJoinRel` was offered plus every pair the connectivity
// gate declined, in the SAME `{a+b} | {c+d}` canonical key `SpineJoin.PairKey`
// produces. Membership of a candidate partition is then a lookup, and the
// answer distinguishes the two readings by name.
//
// A word on why nothing here matches trace blocks to QUERIES. The trace has no
// query attribution — the planner does not carry the statement's audit name —
// and it does not need one: a block is identified by the relset it tops out at,
// and the partitions clause 6 asks about are partitions of exactly that relset.
// Attributing by top relset also survives the audit tool's serial protocol
// changing, which a log-offset scheme would not.
package estimateaudit

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// EnumPair is one `DPTRACE pair` line: a partition the enumerator OFFERED.
type EnumPair struct {
	Phase   int
	Level   int
	Created bool
	Key     string // canonical unordered pair key ({a+b} | {c+d})
	Outer   string // braced relset, in the order the enumerator offered it
	Inner   string
}

// EnumDecline is one `DPTRACE decline` line: a pair the enumerator considered
// and refused, with the gate that refused it.
type EnumDecline struct {
	Phase  int
	Level  int
	Reason string
	Key    string
}

// EnumProblem is one join problem's provenance block.
type EnumProblem struct {
	Rels     []string // relid order (FROM order), as the trace declared it
	Top      string   // braced relset of the final rel, or "" on a failed search
	Status   string   // "ok", or the error the search failed with
	Offered  map[string]EnumPair
	Declined map[string]EnumDecline

	// Built is every relset the search actually created a joinrel for, plus the
	// singletons. It is what makes a NOT-OFFERED answer diagnostic rather than
	// merely negative: a partition can be missing because the pair was never
	// offered, or because one of its two SIDES was never built in the first
	// place, and those are different gaps in different phases.
	Built map[string]bool
}

// EnumTrace is every block harvested from one arm's server log.
type EnumTrace struct {
	Problems []EnumProblem

	// Malformed counts lines that carry the DPTRACE tag but did not parse. A
	// harvest that silently dropped lines would understate enumeration and
	// therefore over-report clause-6 failures, so the count is surfaced rather
	// than swallowed.
	Malformed int

	// ReadErr is the scan error that ended the harvest early, if any. Same
	// reasoning as Malformed: a log that stopped being read is a log whose
	// NOT-ENUMERATED verdicts are unsound, and silence would make it look
	// complete.
	ReadErr string
}

// ParseEnumTrace reads DPTRACE blocks out of a server log. Non-trace lines are
// ignored: the log is the engine's own, and its other output is not this
// channel's business.
//
// A block runs from `DPTRACE problem` to `DPTRACE end`. A block cut off by a
// crash (no `end`) is still returned, with Status "truncated" — the pairs it
// did emit are evidence, and dropping them would lose exactly the run most
// worth reading.
func ParseEnumTrace(r io.Reader) EnumTrace {
	var t EnumTrace
	var cur *EnumProblem
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		i := strings.Index(line, "DPTRACE ")
		if i < 0 {
			continue
		}
		// The server log prefixes its lines; the tag is the anchor, not the
		// line start.
		line = line[i:]
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Malformed++
			continue
		}
		switch fields[1] {
		case "problem":
			if cur != nil {
				cur.Status = "truncated"
				t.Problems = append(t.Problems, *cur)
			}
			cur = &EnumProblem{
				Offered:  map[string]EnumPair{},
				Declined: map[string]EnumDecline{},
				Built:    map[string]bool{},
			}
			kv := traceFields(fields[2:])
			if rels := kv["rels"]; rels != "" {
				cur.Rels = strings.Split(rels, ",")
				for _, name := range cur.Rels {
					cur.Built["{"+name+"}"] = true
				}
			}
		case "pair":
			if cur == nil {
				t.Malformed++
				continue
			}
			kv := traceFields(fields[2:])
			p := EnumPair{
				Phase:   atoiOr(kv["phase"], 0),
				Level:   atoiOr(kv["lev"], 0),
				Created: kv["created"] == "1",
				Key:     kv["pair"],
				Outer:   kv["outer"],
				Inner:   kv["inner"],
			}
			if p.Key == "" {
				t.Malformed++
				continue
			}
			// The FIRST offer wins: it is the one that created the joinrel and
			// fixed its size, and a later offer of the same partition (the
			// mirror direction) carries no new provenance.
			if _, seen := cur.Offered[p.Key]; !seen {
				cur.Offered[p.Key] = p
			}
			cur.Built[mergeRelsets(p.Outer, p.Inner)] = true
			cur.Built[p.Outer], cur.Built[p.Inner] = true, true
		case "decline":
			if cur == nil {
				t.Malformed++
				continue
			}
			kv := traceFields(fields[2:])
			d := EnumDecline{
				Phase:  atoiOr(kv["phase"], 0),
				Level:  atoiOr(kv["lev"], 0),
				Reason: kv["reason"],
				Key:    kv["pair"],
			}
			if d.Key == "" {
				t.Malformed++
				continue
			}
			if _, seen := cur.Declined[d.Key]; !seen {
				cur.Declined[d.Key] = d
			}
		case "end":
			if cur == nil {
				t.Malformed++
				continue
			}
			kv := traceFields(fields[2:])
			cur.Top = kv["top"]
			cur.Status = kv["status"]
			if cur.Status == "" {
				cur.Status = "ok"
			}
			t.Problems = append(t.Problems, *cur)
			cur = nil
		default:
			t.Malformed++
		}
	}
	if err := sc.Err(); err != nil {
		t.ReadErr = err.Error()
	}
	if cur != nil {
		cur.Status = "truncated"
		t.Problems = append(t.Problems, *cur)
	}
	return t
}

// traceFields splits `k=v` tokens. A value may not contain a space — the
// planner's renderer guarantees that (relsets are `{a+b}`, reasons are
// hyphenated), except for the pair key, which is `{a} | {b}` and is therefore
// reassembled here rather than in the writer: making the writer emit an
// unreadable key just to dodge two spaces would break the "same string as
// SpineJoin.PairKey" property this whole channel rests on.
func traceFields(fields []string) map[string]string {
	kv := map[string]string{}
	key := ""
	for _, f := range fields {
		if k, v, ok := strings.Cut(f, "="); ok && isTraceKey(k) {
			key, kv[k] = k, v
			continue
		}
		if key != "" {
			kv[key] += " " + f
		}
	}
	return kv
}

// isTraceKey keeps the continuation rule from mistaking a relset for a key: the
// writer's keys are lowercase words, and `{a+b}` / `|` are not.
func isTraceKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

// mergeRelsets is the union of two braced relsets, rendered the same way.
func mergeRelsets(a, b string) string {
	members := append(relsetMembers(a), relsetMembers(b)...)
	sort.Strings(members)
	return "{" + strings.Join(members, "+") + "}"
}

func relsetMembers(s string) []string {
	s = strings.TrimPrefix(strings.TrimSuffix(s, "}"), "{")
	if s == "" {
		return nil
	}
	return strings.Split(s, "+")
}

// EnumVerdict is what the trace says about one partition.
type EnumVerdict string

const (
	// EnumOffered: the enumerator produced this pair. Divergence is a COST or
	// STATS outcome — 09 §4 admits it and clause 6 passes on this row.
	EnumOffered EnumVerdict = "OFFERED"
	// EnumDeclined: the pair was reached and refused by the connectivity gate.
	EnumDeclined EnumVerdict = "DECLINED"
	// EnumUnbuiltSide: the pair was never reached because one of its two sides
	// was never built as a joinrel — a gap one level below the pairing.
	EnumUnbuiltSide EnumVerdict = "SIDE-NOT-BUILT"
	// EnumMissing: both sides exist, the pair was neither offered nor
	// declined. A gap in the pass that should have produced it.
	EnumMissing EnumVerdict = "NOT-ENUMERATED"
	// EnumNoProblem: no traced problem covers this relset at all — the trace
	// was not harvested for this query, NOT a statement about the search.
	EnumNoProblem EnumVerdict = "NO-TRACE"
	// EnumCrossLevel: the trace WAS harvested, and no single join problem spans
	// the partition because its two sides were planned at different query
	// levels. TPC-H Q20 is the standing example: goopg's only traced problem is
	// `{nation,supplier}`, and `{lineitem,part,partsupp}` live under SubPlans
	// that are separate planning contexts. The printed plan still shows a node
	// joining the two, so `Spine` reads a pairing there, but no join search of
	// either engine ever chose it — a SubPlan boundary is not a partition.
	//
	// Which side the check came from decides what this means, and the two are
	// opposite (see `RenderEnum`): for a CONTROL it is out of scope, because
	// goopg's own search legitimately never saw its own printed "pairing"; for a
	// CANDIDATE it is a clause-6 FAILURE, because a partition PG enumerated
	// inside one join problem is one goopg cannot reach at all when it did not
	// flatten the sublink into the same problem. Collapsing that into NO-TRACE —
	// which is what this channel did on its first run, 2026-08-06 — voids a
	// sound run on a control and mislabels a real search gap as a harness gap.
	EnumCrossLevel EnumVerdict = "CROSS-QUERY-LEVEL"
)

// EnumCheck is one adjudicated partition.
type EnumCheck struct {
	Query     string
	Kind      string // "candidate" (PG-only bushy) or "control" (goopg's own bushy)
	Partition string
	Key       string
	Verdict   EnumVerdict
	Detail    string
}

// Passed reports whether this check clears clause 6's bar. A candidate passes
// when the search OFFERED the partition (divergence is then cost/stats, which
// §4's ratchet admits); a control passes on the same condition, but a failing
// control indicts the HARNESS rather than the search — goopg's own chosen
// pairing must be in its own trace.
func (c EnumCheck) Passed() bool { return c.Verdict == EnumOffered }

// InScope reports whether this check is a statement about the join SEARCH at
// all. A control whose two sides were planned at different query levels is not:
// no search enumerated it, in goopg or in PG, so demanding it be OFFERED indicts
// a harness that is working. Candidates stay in scope in every verdict —
// CROSS-QUERY-LEVEL is a real answer for them, not an exemption.
func (c EnumCheck) InScope() bool {
	return c.Kind != "control" || c.Verdict != EnumCrossLevel
}

// EnumChecks derives the partitions to adjudicate from a spine diff:
//
//   - candidates — `SpineRow.Clause6Candidate`, the bushy partitions PG chose
//     and goopg's plan lacks. These are the rows clause 6 turns on.
//   - controls — bushy partitions goopg itself CHOSE. Every one of them was by
//     construction offered to `makeJoinRel`, so a control that comes back
//     anything but OFFERED proves the trace was not harvested (wrong log, flag
//     off, arm mismatch) and that a candidate's NOT-ENUMERATED verdict in the
//     same run means nothing. 09 §3.11 names Q20's matched bushy pairing as
//     exactly this control; deriving the set instead of hard-coding Q20 keeps
//     the control alive when the run's matched set changes.
func EnumChecks(rows []SpineRow) []EnumCheck {
	var out []EnumCheck
	for _, r := range rows {
		switch {
		case r.Clause6Candidate():
			out = append(out, EnumCheck{
				Query: r.Query, Kind: "candidate",
				Partition: r.Ref.Partition(), Key: r.Ref.PairKey(),
			})
		case r.Goopg != nil && r.Goopg.Bushy && !r.Goopg.Ambiguous:
			out = append(out, EnumCheck{
				Query: r.Query, Kind: "control",
				Partition: r.Goopg.Partition(), Key: r.Goopg.PairKey(),
			})
		}
	}
	return out
}

// Adjudicate answers each check against the trace.
func (t EnumTrace) Adjudicate(checks []EnumCheck) []EnumCheck {
	out := make([]EnumCheck, len(checks))
	for i, c := range checks {
		out[i] = t.verdict(c)
	}
	return out
}

// verdict resolves one partition. The problem is located by relset — the block
// whose search covers both sides of the pair (see the package comment on why
// not by query name).
func (t EnumTrace) verdict(c EnumCheck) EnumCheck {
	sides := strings.Split(c.Key, " | ")
	var best *EnumProblem
	for i := range t.Problems {
		p := &t.Problems[i]
		if _, ok := p.Offered[c.Key]; ok {
			e := p.Offered[c.Key]
			c.Verdict = EnumOffered
			c.Detail = fmt.Sprintf("phase=%d lev=%d created=%v top=%s", e.Phase, e.Level, e.Created, p.Top)
			return c
		}
		if !problemCovers(p, sides) {
			continue
		}
		if best == nil {
			best = p
		}
	}
	if best == nil {
		if len(t.Problems) == 0 {
			c.Verdict = EnumNoProblem
			c.Detail = "no DPTRACE block was harvested at all — wrong log, or the gate was off"
			return c
		}
		c.Verdict = EnumCrossLevel
		if unseen := t.unseenRels(sides); len(unseen) > 0 {
			c.Detail = fmt.Sprintf("%s entered no traced join problem — planned at another query level (SubPlan/CTE), so this pairing is a planning boundary, not a partition",
				strings.Join(unseen, ", "))
			return c
		}
		c.Detail = "every relation is traced, but never in one problem — the two sides belong to different join problems"
		return c
	}
	if d, ok := best.Declined[c.Key]; ok {
		c.Verdict = EnumDeclined
		c.Detail = fmt.Sprintf("phase=%d lev=%d reason=%s top=%s", d.Phase, d.Level, d.Reason, best.Top)
		return c
	}
	for _, s := range sides {
		if !best.Built[s] {
			c.Verdict = EnumUnbuiltSide
			c.Detail = fmt.Sprintf("no joinrel was ever built over %s (top=%s)", s, best.Top)
			return c
		}
	}
	c.Verdict = EnumMissing
	c.Detail = fmt.Sprintf("both sides built, pair neither offered nor declined (top=%s)", best.Top)
	return c
}

// unseenRels lists the partition members that appear in NO traced join problem,
// sorted. That is the positive evidence separating a planning boundary from a
// lost trace: a relation the search never had is a relation planned somewhere
// else, whereas a relation present in a problem that still failed to span the
// pair points at the trace, not at the query structure.
//
// The union is over the WHOLE log, not the query's own blocks, because the trace
// carries no query attribution (see the package comment). The list is therefore
// a lower bound — Q20's `lineitem` and `part` are omitted because Q8's problem in
// the same run contains those names — and it is used only to word the detail
// line, never to decide the verdict, which `best == nil` has already settled.
func (t EnumTrace) unseenRels(sides []string) []string {
	seen := map[string]bool{}
	for i := range t.Problems {
		for _, r := range t.Problems[i].Rels {
			seen[r] = true
		}
	}
	var out []string
	for _, s := range sides {
		for _, m := range relsetMembers(s) {
			if !seen[m] {
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// problemCovers reports whether a traced problem's relation set contains every
// member of both sides — the test for "this is the search that could have
// produced this partition".
func problemCovers(p *EnumProblem, sides []string) bool {
	have := map[string]bool{}
	for _, r := range p.Rels {
		have[r] = true
	}
	for _, s := range sides {
		for _, m := range relsetMembers(s) {
			if !have[m] {
				return false
			}
		}
	}
	return true
}

// RenderEnum writes the enumeration-provenance section of the audit artifact.
func RenderEnum(t EnumTrace, checks []EnumCheck) string {
	var b strings.Builder
	b.WriteString("=== JOIN-SEARCH ENUMERATION PROVENANCE (09 §4 clause 6, P5.9-l-ii)\n")
	fmt.Fprintf(&b, "  traced join problems: %d", len(t.Problems))
	if t.Malformed > 0 {
		fmt.Fprintf(&b, "; %d malformed trace line(s) — treat every verdict below as suspect", t.Malformed)
	}
	if t.ReadErr != "" {
		fmt.Fprintf(&b, "; HARVEST ENDED EARLY: %s", t.ReadErr)
	}
	b.WriteString("\n")
	if len(t.Problems) == 0 {
		b.WriteString("  NO TRACE HARVESTED — was GOOPG_PGSHAPED_DP_TRACE=1 set on the server?\n")
	}
	if len(checks) == 0 {
		b.WriteString("  no bushy partition to adjudicate (no clause-6 candidate, no bushy control)\n")
	}

	var candTotal, candOK, candCross, ctlTotal, ctlOK, ctlOOS int
	for _, c := range checks {
		note := ""
		if !c.InScope() {
			note = "  (out of scope)"
		}
		fmt.Fprintf(&b, "  %-9s %-4s %-17s %s%s\n      %s\n", c.Kind, c.Query, c.Verdict, c.Partition, note, c.Detail)
		if c.Kind == "candidate" {
			candTotal++
			switch {
			case c.Passed():
				candOK++
			case c.Verdict == EnumCrossLevel:
				candCross++
			}
			continue
		}
		if !c.InScope() {
			ctlOOS++
			continue
		}
		ctlTotal++
		if c.Passed() {
			ctlOK++
		}
	}

	b.WriteString("\n=== ENUMERATION SUMMARY (09 §4 clause 6)\n")
	fmt.Fprintf(&b, "  controls (goopg's OWN bushy pairings, must all be OFFERED): %d/%d\n", ctlOK, ctlTotal)
	if ctlOOS > 0 {
		// Printed, never silent: a control set that shrank is the one change
		// that could turn a voided run into a green one without anyone noticing.
		fmt.Fprintf(&b, "  controls set aside as CROSS-QUERY-LEVEL (a SubPlan boundary, not a partition): %d\n", ctlOOS)
	}
	fmt.Fprintf(&b, "  candidates (PG-only bushy pairings): %d/%d offered by the goopg search\n", candOK, candTotal)
	switch {
	case ctlTotal > 0 && ctlOK < ctlTotal:
		b.WriteString("  VERDICT: HARNESS FAULT — goopg's own chosen pairing is missing from its own\n" +
			"           trace, so no candidate verdict in this run is admissible.\n")
	case candTotal == 0:
		b.WriteString("  VERDICT: clause 6 has nothing to adjudicate in this run.\n")
	case ctlTotal == 0 && candOK < candTotal:
		// No in-scope control means nothing independently proves the channel
		// live, so only an all-OFFERED result is self-evidencing (every OFFERED
		// verdict IS a trace hit). A negative one is not.
		b.WriteString("  VERDICT: INCONCLUSIVE — no in-scope control backs this run, and the negative\n" +
			"           candidate verdicts below cannot be told from an unharvested trace.\n")
	case candOK == candTotal:
		b.WriteString("  VERDICT: every PG-only bushy partition WAS enumerated — the divergence is\n" +
			"           cost/stats, which 09 §4's ratchet admits. Clause 6 passes.\n")
	case candCross > 0:
		b.WriteString("  VERDICT: a PG-only bushy partition spans TWO goopg join problems — PG flattened\n" +
			"           a sublink goopg did not, so the shape is unreachable rather than\n" +
			"           merely unchosen. 09 §4 reserves hard failure for that. Clause 6 fails.\n")
	default:
		b.WriteString("  VERDICT: a PG-only bushy partition was NOT enumerated — a named gap in the\n" +
			"           search, which 09 §4 reserves hard failure for. Clause 6 fails.\n")
	}
	fmt.Fprintf(&b, "  RATCHET enum_controls=%d/%d enum_controls_oos=%d enum_candidates_offered=%d/%d enum_candidates_crosslevel=%d enum_problems=%d enum_malformed=%d\n",
		ctlOK, ctlTotal, ctlOOS, candOK, candTotal, candCross, len(t.Problems), t.Malformed)
	return b.String()
}
