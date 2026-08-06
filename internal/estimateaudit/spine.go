// The join-spine (PAIRING) channel — the instrument
// docs/design/leftdeep-joins/09-verification-and-acceptance.md §4 names in its
// "verified through the §4 parity gate's spine diff" and that four P5.9
// acceptance runs had to score from a proxy because it did not exist
// (09 §3.10, M0127-P5.9-l).
//
// Why the parity channel next door cannot answer clause 6. `Parity` compares
// joinrels keyed by the SET of base relations underneath them, and labels a
// relset only one engine built `SHAPE (PG-only joinrel)`. That key is exactly
// upstream's `RelOptInfo.relids`, and it is the right key for an ESTIMATE
// comparison — the actual row count of `{customer,orders}` is a property of the
// query and the data, not of the plan. But clause 6 does not ask which relsets
// exist; it asks how a relset was **partitioned**:
//
//	PG   Q7: {customer+lineitem+n1+n2+orders+supplier}
//	          = {customer+lineitem+n2+orders} ⋈ {n1+supplier}   ← bushy
//	goopg Q7: {customer+lineitem+n1+n2+orders+supplier}
//	          = {lineitem+n1+n2+orders+supplier} ⋈ {customer}   ← left-deep
//
// Both engines built the top relset, so the parity channel reports it MATCHED
// and says nothing at all about the divergence. The pairing is the unit clause 6
// is stated on and this file is the only place it is computed.
//
// What this file does NOT settle, and P5.9-l-ii does: a PG pairing absent from
// goopg's plan may have been enumerated by the DP and lost on cost, or never
// enumerated at all. Both predict the same observable here — a chosen plan that
// lacks it. Distinguishing them needs the search's own provenance, not the
// printed plan; this channel's job is to name the pairings that question has to
// be asked about (`Clause6Candidates`).
package estimateaudit

import (
	"fmt"
	"sort"
	"strings"
)

// SpineJoin is one join node described by how it PAIRED its inputs, rather
// than by which relations are underneath it.
type SpineJoin struct {
	Query  string
	Depth  int
	Label  string
	Rels   []string   // the joinrel this node builds (== Node.Rels)
	Inputs [][]string // one relset per immediate child, in PRINTED order

	// Bushy is 09 §3.10's definition verbatim: both children, after
	// unwrapping the single-child nodes between them (`Hash`, `Materialize`,
	// `Sort`, `Gather`, `Memoize`, aggregation), are themselves joins.
	Bushy bool

	// Final marks the outermost join — the one whose pairing IS the query's
	// top-level partition, and the only one 09 §3.10 quotes per query.
	Final bool

	// Ambiguous marks a pairing computed from a plan that scans the same
	// relation name more than once without an alias. goopg's EXPLAIN does not
	// deduplicate repeated relation names the way `select_rtable_names`
	// (ruleutils.c) does, so on Q8/Q17/Q18 two distinct range-table entries
	// print identically and collapse into one relset member — see 09 §4.1's
	// "shape_mismatches is an upper bound" note. The pairing is still printed,
	// because a suppressed row reads as an agreeing one, but it is excluded
	// from the clause-6 candidate list: a partition that cannot be named
	// unambiguously cannot be adjudicated.
	Ambiguous bool
}

// PairKey is the canonical, UNORDERED form of the partition: the child relsets
// sorted among themselves. Unordered because upstream's `make_join_rel(x, y)`
// already handles `(y, x)` (joinrels.c:696) — `(a ⋈ b)` and `(b ⋈ a)` are one
// RelOptInfo with two paths, so which side drives is a property of the PATH and
// belongs to the build/probe column of §4's shape skeleton, not to the question
// clause 6 asks.
func (j SpineJoin) PairKey() string {
	parts := make([]string, 0, len(j.Inputs))
	for _, in := range j.Inputs {
		parts = append(parts, "{"+strings.Join(in, "+")+"}")
	}
	sort.Strings(parts)
	return strings.Join(parts, " | ")
}

// RelKeyOf is the joinrel identity this node builds — the key the PARITY
// channel joins on. It is here so the two channels' units can be compared in
// one place: a pairing divergence with an equal RelKeyOf is precisely a
// divergence the parity channel reports as MATCHED.
func (j SpineJoin) RelKeyOf() string { return strings.Join(j.Rels, "+") }

// Partition renders the pairing in printed (outer-first) order.
func (j SpineJoin) Partition() string {
	parts := make([]string, 0, len(j.Inputs))
	for _, in := range j.Inputs {
		parts = append(parts, "{"+strings.Join(in, "+")+"}")
	}
	return strings.Join(parts, " ⋈ ")
}

// Shape is the one-word classification printed beside a pairing.
func (j SpineJoin) Shape() string {
	if j.Bushy {
		return "BUSHY"
	}
	return "left-deep"
}

func (j SpineJoin) String() string {
	return fmt.Sprintf("%s: %s (%s, %s)", j.Query, j.Partition(), j.Label, j.Shape())
}

// Spine extracts every join pairing of one audited plan, outermost first.
//
// A join's inputs are its IMMEDIATE children in the printed tree, whose relsets
// `attachRels` has already computed. Wrapper nodes are not skipped for the
// relset (a `Hash` reports the same relset as the subtree under it) — only for
// the bushy test, which asks what KIND of node the child ultimately is.
func Spine(r QueryReport) []SpineJoin {
	if r.Err != "" || len(r.Nodes) == 0 {
		return nil
	}
	nodes := r.Nodes
	parent := planParents(nodes)
	children := make([][]int, len(nodes))
	for i, p := range parent {
		if p >= 0 {
			children[p] = append(children[p], i)
		}
	}
	// Leaf OCCURRENCES, not leaf identities: the two differ exactly when the
	// plan scans one relation name twice, which is the rendering gap
	// SpineJoin.Ambiguous exists for.
	leaves := make([]int, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		if _, ok := leafRel(nodes[i].Label); ok {
			leaves[i]++
		}
		if p := parent[i]; p >= 0 {
			leaves[p] += leaves[i]
		}
	}

	var out []SpineJoin
	for i := range nodes {
		if !nodes[i].IsJoin || len(children[i]) < 2 {
			continue
		}
		j := SpineJoin{
			Query:     r.Name,
			Depth:     nodes[i].Depth,
			Label:     nodes[i].Label,
			Rels:      nodes[i].Rels,
			Ambiguous: leaves[i] != len(nodes[i].Rels),
			Final:     r.Final != nil && r.Final.Raw == nodes[i].Raw && r.Final.Depth == nodes[i].Depth,
		}
		bushy := true
		for _, c := range children[i] {
			j.Inputs = append(j.Inputs, nodes[c].Rels)
			if !nodes[unwrapSingleChild(children, c)].IsJoin {
				bushy = false
			}
		}
		j.Bushy = bushy
		out = append(out, j)
	}
	return out
}

// unwrapSingleChild descends through every node that has exactly one child and
// returns the first node that does not — 09 §3.10's "after unwrapping
// `Hash`/`Materialize`/`Sort`/`Gather`/`Memoize`/aggregation nodes".
//
// It is written as "single-child" rather than as a whitelist of those labels
// because the whitelist is the accident and the arity is the rule: every one of
// them is a pipeline node that passes its child's relset through unchanged, and
// a whitelist would silently mis-classify the next such node either engine
// learns to print (goopg's `Materialize`-equivalents, upstream's
// `Incremental Sort`). A node with two or more children stops the walk, which is
// what makes an `Append` under a join read as a non-join child — correctly: an
// append of two scans is not a joinrel PG's search ever paired.
func unwrapSingleChild(children [][]int, i int) int {
	for len(children[i]) == 1 {
		i = children[i][0]
	}
	return i
}

// SpineStatus classifies one pairing of one query.
type SpineStatus string

const (
	SpineMatched   SpineStatus = "matched"    // both engines partitioned a relset this way
	SpineGoopgOnly SpineStatus = "goopg-only" // goopg chose a partition PG did not
	SpineRefOnly   SpineStatus = "pg-only"    // PG chose a partition goopg did not
)

// SpineRow is one pairing, on one or both sides.
type SpineRow struct {
	Query  string
	Status SpineStatus
	Goopg  *SpineJoin
	Ref    *SpineJoin
}

// Clause6Candidate reports whether this row is a partition clause 6 has to be
// adjudicated on: a BUSHY spine PG 18.3 chose that goopg's plan does not
// contain. §4 admits shapes chosen on cost-constant or stats-fidelity grounds
// and reserves hard failure for "a bushy shape PG can produce that the goopg
// search *cannot express*" — so this is the candidate set, not the failure set.
// Which of the two it is cannot be decided here (see the package comment).
func (r SpineRow) Clause6Candidate() bool {
	return r.Status == SpineRefOnly && r.Ref != nil && r.Ref.Bushy && !r.Ref.Ambiguous
}

// SpineDiff joins the two engines' chosen spines per query and per pairing.
// Queries unmeasured on either side are skipped — `uncomparedQueries` already
// names them for the parity section, and naming them twice in one artifact
// reads as two different failures.
func SpineDiff(goopg, reference []QueryReport) []SpineRow {
	refByName := make(map[string]QueryReport, len(reference))
	for _, r := range reference {
		refByName[r.Name] = r
	}
	var out []SpineRow
	for _, g := range goopg {
		ref, ok := refByName[g.Name]
		if !ok || g.Err != "" || ref.Err != "" {
			continue
		}
		gJoins := spineByPairKey(Spine(g))
		rJoins := spineByPairKey(Spine(ref))
		for _, key := range sortedKeys(gJoins) {
			gj := gJoins[key]
			row := SpineRow{Query: g.Name, Status: SpineGoopgOnly, Goopg: gj}
			if rj, ok := rJoins[key]; ok {
				row.Ref = rj
				row.Status = SpineMatched
			}
			out = append(out, row)
		}
		for _, key := range sortedKeys(rJoins) {
			if _, ok := gJoins[key]; ok {
				continue
			}
			out = append(out, SpineRow{Query: g.Name, Status: SpineRefOnly, Ref: rJoins[key]})
		}
	}
	return out
}

// spineByPairKey indexes a plan's pairings. A key repeated within one plan
// keeps the OUTERMOST occurrence, for the reason joinsByRelKey does: the
// top-level partition is what §4 states the diff on, and a self-join printed
// without aliases can make two different levels key the same.
func spineByPairKey(joins []SpineJoin) map[string]*SpineJoin {
	out := make(map[string]*SpineJoin, len(joins))
	for i := range joins {
		j := &joins[i]
		if prev, ok := out[j.PairKey()]; ok && prev.Depth <= j.Depth {
			continue
		}
		out[j.PairKey()] = j
	}
	return out
}

// SpineCounts is the ratchet's numeric summary.
type SpineCounts struct {
	Queries     int
	Matched     int
	GoopgOnly   int
	RefOnly     int
	BushyGoopg  []string // queries whose chosen spine is bushy somewhere
	BushyRef    []string
	Candidates  []SpineRow // Clause6Candidate rows, in query order
	AmbiguousQs []string   // queries whose pairings carry the rendering gap
}

// CountSpine summarises a diff. `goopg`/`reference` are the same reports the
// diff was built from, needed because a query whose spines agree entirely still
// has to be counted as compared.
func CountSpine(goopg, reference []QueryReport, rows []SpineRow) SpineCounts {
	var c SpineCounts
	refByName := make(map[string]QueryReport, len(reference))
	for _, r := range reference {
		refByName[r.Name] = r
	}
	seenBushyG, seenBushyR, seenAmb := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, g := range goopg {
		ref, ok := refByName[g.Name]
		if !ok || g.Err != "" || ref.Err != "" {
			continue
		}
		c.Queries++
		for _, j := range Spine(g) {
			if j.Bushy && !seenBushyG[g.Name] {
				seenBushyG[g.Name] = true
				c.BushyGoopg = append(c.BushyGoopg, g.Name)
			}
			if j.Ambiguous && !seenAmb[g.Name] {
				seenAmb[g.Name] = true
				c.AmbiguousQs = append(c.AmbiguousQs, g.Name)
			}
		}
		for _, j := range Spine(ref) {
			if j.Bushy && !seenBushyR[ref.Name] {
				seenBushyR[ref.Name] = true
				c.BushyRef = append(c.BushyRef, ref.Name)
			}
		}
	}
	for _, r := range rows {
		switch r.Status {
		case SpineMatched:
			c.Matched++
		case SpineGoopgOnly:
			c.GoopgOnly++
		case SpineRefOnly:
			c.RefOnly++
		}
		if r.Clause6Candidate() {
			c.Candidates = append(c.Candidates, r)
		}
	}
	return c
}

// RenderSpine writes the spine section of the committed artifact: every join
// pairing of both engines' chosen plans, per query, then the clause-6 candidate
// list and the ratchet line. Deterministic, like Render and RenderParity.
func RenderSpine(goopg, reference []QueryReport, rows []SpineRow) string {
	var b strings.Builder
	b.WriteString("=== JOIN SPINE PAIRINGS vs PG 18.3 (09 §4 clause 6)\n")

	byQuery := map[string][]SpineRow{}
	for _, r := range rows {
		byQuery[r.Query] = append(byQuery[r.Query], r)
	}
	for _, q := range sortedKeys(byQuery) {
		fmt.Fprintf(&b, "--- %s\n", q)
		for _, r := range byQuery[q] {
			switch r.Status {
			case SpineMatched:
				writeSpineLine(&b, "both ", *r.Goopg, "")
			case SpineGoopgOnly:
				writeSpineLine(&b, "goopg", *r.Goopg, "GOOPG-ONLY PAIRING")
			case SpineRefOnly:
				note := "PG-ONLY PAIRING"
				if r.Clause6Candidate() {
					note = "PG-ONLY PAIRING  <== CLAUSE-6 CANDIDATE"
				}
				writeSpineLine(&b, "PG   ", *r.Ref, note)
			}
		}
	}

	c := CountSpine(goopg, reference, rows)
	b.WriteString("\n=== SPINE SUMMARY (09 §4 clause 6)\n")
	fmt.Fprintf(&b, "  queries compared: %d; pairings: %d matched, %d goopg-only, %d PG-only\n",
		c.Queries, c.Matched, c.GoopgOnly, c.RefOnly)
	fmt.Fprintf(&b, "  bushy spine chosen by: goopg %s | PG %s\n",
		queryList(c.BushyGoopg), queryList(c.BushyRef))
	if len(c.AmbiguousQs) > 0 {
		fmt.Fprintf(&b, "  ambiguous (a relation scanned twice without an alias; "+
			"pairing printed, excluded from candidates): %s\n", queryList(c.AmbiguousQs))
	}
	if len(c.Candidates) == 0 {
		b.WriteString("  no bushy partition PG chose is absent from goopg's plans\n")
	}
	for _, r := range c.Candidates {
		fmt.Fprintf(&b, "  CLAUSE-6-CANDIDATE %s — enumerated-and-lost vs never-enumerated is "+
			"NOT decidable from the plan (P5.9-l-ii)\n", r.Ref)
	}
	fmt.Fprintf(&b, "  RATCHET spine_pg_only=%d spine_goopg_only=%d bushy_pg=%d bushy_goopg=%d clause6_candidates=%d\n",
		c.RefOnly, c.GoopgOnly, len(c.BushyRef), len(c.BushyGoopg), len(c.Candidates))
	return b.String()
}

func writeSpineLine(b *strings.Builder, who string, j SpineJoin, note string) {
	mark := "  "
	if j.Final {
		mark = "F "
	}
	if j.Ambiguous {
		mark = mark[:1] + "~"
	}
	fmt.Fprintf(b, "  %s%s d%-2d %-9s %s%s\n", mark, who, j.Depth, j.Shape(), j.Partition(), noteSuffix(note))
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "   " + note
}

func queryList(qs []string) string {
	if len(qs) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d (%s)", len(qs), strings.Join(qs, ", "))
}
