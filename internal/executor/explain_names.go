package executor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

// explainNames is EXPLAIN's range-table name table: the mapping from a
// scan node's statement-unique range-table identity (the planner-stamped
// RTID, PostgreSQL's varno analogue, A-01(ii)) to the name EXPLAIN prints
// in front of that node's columns.
//
// It exists because goopg's plan expressions are *resolved* references — a
// ColumnRef carries only the column's name and its slot index, so two
// distinct columns of a self-join render identically. Before M0125-0039,
// EXPLAIN printed TPC-DS Q30's correlated filter as
// `Filter: (ctr_state = ctr_state)`, which reads as an unsatisfiable
// self-comparison; PostgreSQL prints `(ctr1.ctr_state = ctr_state)`.
// Nothing in the old output distinguished the two readings, which is exactly
// the distinction a triage loop needs.
//
// Upstream analogue: ruleutils.c's deparse_namespace / set_rtable_names.
// PostgreSQL names each RTE by its FROM-clause alias, falling back to the
// relation name, and disambiguates a repeated name with a `_N` suffix while
// leaving the first occurrence bare (`date_dim`, `date_dim_1`, …).
//
// Divergence from upstream worth knowing: PostgreSQL numbers those suffixes
// over the *query's* range table, which includes entries goopg's plan tree
// never materialises as a node (pulled-up subqueries, eliminated joins), so
// goopg's suffix for the N-th duplicate can differ from PG's for the same
// query. The qualification itself — which relation a column came from — is
// what this table is for and is unaffected.
type explainNames struct {
	// bySource maps statement-unique RTID → printed name. Entry 0 is never
	// populated: RTID 0 means "no identity assigned" (scan kinds whose
	// stamping lands in a later cut, nodes built outside a threaded
	// planning path), and those stay unqualified, exactly as they render
	// today.
	bySource map[int32]string
	// taken is the per-base high-water mark behind printed-name
	// disambiguation (PostgreSQL's RTENameHashEntry.counter): taken[base]
	// is the largest _N suffix handed out for base, and every generated
	// name is itself entered, so a literal alias `x_1` pushes the second
	// `x` to `x_2`. See claimName.
	taken map[string]int
	// seen guards against registering the same RTID twice — a subplan
	// body is collected when its `SubPlan N` subtree is about to render,
	// CTE bodies are *shared* pointers across references (CTEScan.Child
	// aliases one planned body), and NestedLoopIndexJoin.InnerMemo.Child
	// aliases Inner — the same node is reachable twice and must register
	// once.
	seen map[int32]bool
	// cols is the column set the relation registered for an RTID
	// actually exposes, and it is a correctness guard, not a nicety.
	// A ColumnRef naming a derived column can still resolve to an
	// unrelated relation through the bySrc translation below, and
	// EXPLAIN would print a confident, wrong qualifier — strictly worse
	// than the bare name it replaced. Requiring the column name to exist
	// in the claimed relation turns that case back into today's
	// unqualified rendering. (Recorded limit, review F8: on a mixed node
	// whose outputs carry several identities, cols covers every output
	// name, so a later column(src=X, derivedName) lookup can still wrongly
	// qualify. Low-probability; the guard narrows it, it does not close
	// it.)
	cols map[int32]map[string]bool
	// bySrc translates a column reference's per-level SourceTableIdx
	// (the storage type every SchemaColumn and ColumnRef carries,
	// M0071-0009) to the RTID registered for it. The planner's
	// SourceTableIdx counter restarts at 1 for every query level while
	// RTIDs are statement-unique, so one src value can name several
	// relations across levels; the first registration in WALK order wins
	// — root tree before sublink bodies, consumer before CTE body —
	// which is the outer scope, the name a correlated OuterColumnRef,
	// the case this table exists for, needs. (Walk order, not RTID
	// order, deliberately: CTE bodies are allocated before their
	// consumers, so an allocation-order tie-break would resolve an outer
	// reference to the body's inner scan.)
	bySrc map[int16]int32
	// nodeLabels maps each plan node to its EXPLAIN-node-label name,
	// disambiguated per RTID in allocation order so two distinct nodes
	// over the same relation (e.g. a SEMI-join outer and inner side
	// sharing one SourceTableIdx) still get distinguishable labels.
	// Keyed by nodePtr(n). Populated by collect; absent means no
	// disambiguation was needed (bare first occurrence).
	// M0128-P5.1: this is the select_rtable_names_for_explain equivalent
	// for node labels — the existing bySource/taken/seen serve column
	// qualification, which has different collision rules.
	nodeLabels map[string]string
}

// nodePtr returns a unique string id for a plan node.
func nodePtr(n optimizer.Node) string { return fmt.Sprintf("%p", n) }

// newExplainNames builds the name table for the plan rooted at n.
func newExplainNames(n optimizer.Node) *explainNames {
	nm := &explainNames{
		bySource:   map[int32]string{},
		taken:      map[string]int{},
		seen:       map[int32]bool{},
		cols:       map[int32]map[string]bool{},
		bySrc:      map[int16]int32{},
		nodeLabels: map[string]string{},
	}
	nm.collect(n)
	return nm
}

// qualify reports whether column references should be printed with their
// relation name. Mirrors upstream's `useprefix = es->rtable_size > 1`
// (explain.c show_upper_qual / show_sort_group_keys): a single-relation
// query keeps the bare column names PostgreSQL prints for it, so the common
// case of EXPLAIN output is byte-identical to before this table existed.
func (nm *explainNames) qualify() bool {
	return nm != nil && len(nm.bySource) > 1
}

// name returns the printed relation name for a binding, or "" when the
// binding has no name (SourceTableIdx with no RTID registered, or a node
// kind that carries no relation).
func (nm *explainNames) name(src int16) string {
	if nm == nil {
		return ""
	}
	return nm.bySource[nm.bySrc[src]]
}

// column renders one column reference. prefix is upstream's
// context->varprefix, resolved by the caller: false renders the bare column
// name, true renders `<relation>.<column>` whenever the binding is known.
// A binding with no name (SourceTableIdx with no RTID registered, or a node
// kind that carries no relation) always renders bare — there is nothing
// truthful to print.
func (nm *explainNames) column(src int16, colName string, prefix bool) string {
	if !prefix {
		return colName
	}
	rtid, ok := nm.bySrc[src]
	if !ok {
		return colName
	}
	rel := nm.bySource[rtid]
	if rel == "" || !nm.cols[rtid][colName] {
		return colName
	}
	return rel + "." + colName
}

// collect walks the plan subtree rooted at n and registers every scan-like
// node's relation name. Registration is ordered by RTID (the statement-wide
// allocation order, ≈ FROM order across query levels) rather than by tree
// position, so the `_N` suffixes do not depend on which join shape the
// planner picked.
func (nm *explainNames) collect(n optimizer.Node) {
	if nm == nil || n == nil {
		return
	}
	type entry struct {
		rtid int32
		src  int16
		base string
		node optimizer.Node
	}
	var found []entry
	var walk func(optimizer.Node)
	walk = func(node optimizer.Node) {
		if node == nil {
			return
		}
		if base, ok := explainRelBaseName(node); ok {
			if rtid, ok := explainNodeRTID(node); ok {
				// The per-level SourceTableIdx rides along for the
				// bySrc translation only; the registration key is
				// the RTID. Nodes whose outputs carry no uniform
				// identity still register — they simply contribute
				// no translation.
				src, _ := explainSingleSourceIdx(node)
				found = append(found, entry{rtid: rtid, src: src, base: base, node: node})
			}
		}
		for _, c := range planChildren(node) {
			walk(c)
		}
		// A-01(ii) cut 2 (F3): sublink bodies hang off Expr fields
		// (SubqueryExpr.Plan, InExpr.Plan, ExistsExpr, ArraySubqueryExpr,
		// MultiAssignSubqRow), not Node children, so the Node walk above
		// never reaches them. Walk each hanging body too, or no
		// sublink-internal scan registers — not just childless-Result
		// InitPlans. Walk order still favours the root tree — children
		// before hanging bodies, consumer before CTE body — and the
		// bySrc pass below keeps the outer name on a per-level
		// SourceTableIdx collision for exactly that reason.
		for _, sp := range optimizer.NodeSubplans(node) {
			walk(sp)
		}
	}
	walk(n)
	// bySrc first-wins in WALK order (not RTID order): this reproduces
	// today's collision preference exactly — root tree before sublink
	// bodies, consumer before CTE body — so a bare SourceTableIdx lookup
	// resolves to the same name it did before the migration. Only suffix
	// assignment (below) moves to allocation order.
	for _, e := range found {
		if e.src != 0 {
			if _, claimed := nm.bySrc[e.src]; !claimed {
				nm.bySrc[e.src] = e.rtid
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].rtid < found[j].rtid })
	for _, e := range found {
		nm.register(e.rtid, e.base, e.node)
	}

	// M0128-P5.1: assign per-node disambiguated labels for the EXPLAIN
	// node label ("Seq Scan on <name>"). This is a separate pass from
	// column qualification (bySource) because two distinct nodes can
	// share a SourceTableIdx (e.g. SEMI-join sides over the same
	// relation) — the column-qualification register() skips the second
	// visit only when it is literally the same node reached twice, but
	// the node label still needs disambiguation per node.
	//
	// Labels follow PG select_rtable_names_for_explain through the same
	// claimName rule as column qualification, over the same RTID-ordered
	// entries with the same first-wins dedup, so label suffixes and
	// column-qualifier suffixes agree with each other.
	labelTaken := map[string]int{}
	labelSeen := map[string]bool{}
	for _, e := range found {
		if e.base == "" {
			continue
		}
		ptr := nodePtr(e.node)
		if labelSeen[ptr] {
			continue
		}
		labelSeen[ptr] = true
		if name := claimName(labelTaken, e.base); name != e.base {
			nm.nodeLabels[ptr] = name
		}
	}
}

// claimName returns the printed name for one more occurrence of base and
// records the claim in taken, following PostgreSQL's set_rtable_names
// (ruleutils.c): the first occurrence keeps base bare; each later one takes
// base_N for the smallest N not already used as a name, and every generated
// name is itself entered as a base — so with relations named `x`, `x_1`,
// `x`, the third claims `x_2`, not a second `x_1`.
//
// Parent-namespace preload (take2 P0-A §5 point 2) is deliberately NOT part
// of this rule: global uniqueness does not subsume it. If the outer entry
// holding a name produces no node (eliminated/pulled-up), goopg consumes
// nothing while PG's preload still suffixes the inner first occurrence —
// the same family as the stated §6 divergence, listed there.
func claimName(taken map[string]int, base string) string {
	if _, used := taken[base]; !used {
		taken[base] = 0
		return base
	}
	n := taken[base]
	var cand string
	for {
		n++
		cand = base + "_" + strconv.Itoa(n)
		if _, used := taken[cand]; !used {
			break
		}
	}
	taken[base] = n
	taken[cand] = 0
	return cand
}

// register claims a printed name for one range-table identity. First
// occurrence of a base name keeps it bare; later ones go through claimName
// (`_1`, `_2`, … with collision re-check, set_rtable_names).
//
// First registration wins for a given RTID. The same node can be reached
// twice (shared CTE bodies, InnerMemo aliasing Inner), and the second visit
// is skipped — which is the node's own identity, never a cross-level tie to
// break. Cross-level SourceTableIdx collisions no longer meet here at all:
// each level's scan carries its own RTID and registers its own name, while
// the bySrc translation (first in walk order wins) plus the cols guard
// decide what a bare SourceTableIdx lookup resolves to.
func (nm *explainNames) register(rtid int32, base string, node optimizer.Node) {
	if rtid == 0 || base == "" || nm.seen[rtid] {
		return
	}
	nm.seen[rtid] = true
	cols := map[string]bool{}
	for _, c := range node.Output() {
		cols[c.Name] = true
	}
	nm.cols[rtid] = cols
	nm.bySource[rtid] = claimName(nm.taken, base)
}

// explainRelBaseName returns the un-disambiguated name a scan-like node
// contributes to the range table — its FROM-clause alias when one was
// written, else the relation's own name, matching what describePlan already
// prints after `Seq Scan on` / `CTE Scan on`.
func explainRelBaseName(n optimizer.Node) (string, bool) {
	switch p := n.(type) {
	case *optimizer.SeqScan:
		if p.Alias != "" {
			return p.Alias, true
		}
		if p.Table != nil {
			return strings.ToLower(p.Table.Name), true
		}
	case *optimizer.IndexScan:
		if p.Alias != "" {
			return p.Alias, true
		}
		if p.Table != nil {
			return strings.ToLower(p.Table.Name), true
		}
	case *optimizer.IndexOnlyScan:
		if p.Alias != "" {
			return p.Alias, true
		}
		if p.Table != nil {
			return strings.ToLower(p.Table.Name), true
		}
	case *optimizer.CTEScan:
		if p.Alias != "" {
			return p.Alias, true
		}
		return p.Name, true
	case *optimizer.MaterializedCTEScan:
		if p.Alias != "" {
			return p.Alias, true
		}
		return p.Name, true
	case *optimizer.BitmapHeapScan:
		if p.Alias != "" {
			return p.Alias, true
		}
		if p.Table != nil {
			return strings.ToLower(p.Table.Name), true
		}
	}
	return "", false
}

// explainSingleSourceIdx returns the per-level binding id every column of
// n's output carries, when there is exactly one. It is the storage-type
// fallback for column resolution: a ColumnRef arrives carrying only its
// SourceTableIdx, and collect records this value in the bySrc translation
// alongside the RTID-keyed registration.
func explainSingleSourceIdx(n optimizer.Node) (int16, bool) {
	var src int16
	for _, c := range n.Output() {
		if c.SourceTableIdx == 0 {
			continue
		}
		if src == 0 {
			src = c.SourceTableIdx
			continue
		}
		if src != c.SourceTableIdx {
			return 0, false
		}
	}
	return src, src != 0
}

// explainNodeRTID returns the statement-unique range-table identity stamped
// on n's scan node (A-01(ii)), when it has one. It is explainSingleSourceIdx
// reshaped: where that helper derives a per-level SourceTableIdx from the
// node's output columns, this one reads the planner-stamped RTID field
// directly. RTID 0 ("no identity": legacy callers, derived-only columns,
// scan kinds whose stamping lands in a later cut) is reported as absent, so
// those nodes keep today's unqualified rendering.
func explainNodeRTID(n optimizer.Node) (int32, bool) {
	var rtid int32
	switch p := n.(type) {
	case *optimizer.SeqScan:
		rtid = p.RTID
	case *optimizer.IndexScan:
		rtid = p.RTID
	case *optimizer.IndexOnlyScan:
		rtid = p.RTID
	case *optimizer.CTEScan:
		rtid = p.RTID
	case *optimizer.MaterializedCTEScan:
		rtid = p.RTID
	case *optimizer.BitmapHeapScan:
		rtid = p.RTID
	default:
		return 0, false
	}
	if rtid == 0 {
		return 0, false
	}
	return rtid, true
}

// resolveInAncestor names the relation an outer (correlated) column
// reference comes from by searching an ancestor plan subtree for the one
// scan-like node that exposes that column name.
//
// It exists because SourceTableIdx alone cannot answer the question for the
// shape that motivated this work. TPC-DS Q30/Q81 correlate through a CTE
// whose body ends in an aggregate, and an aggregate's output columns carry
// SourceTableIdx 0 — "no identity assigned" — so both sides of
// `ctr1.ctr_state = ctr2.ctr_state` arrive with nothing to look up.
//
// Upstream answers the same question the same way: get_parameter calls
// push_ancestor_plan and deparses the Param's expansion against the ANCESTOR
// plan node's namespace rather than the scanned relation's. anc here is that
// ancestor — the plan node the `SubPlan N` subtree hangs off.
//
// Returns "" unless exactly one relation in the ancestor subtree exposes the
// name. An ambiguous match is left unqualified on purpose: a confidently
// wrong relation name is worse than the bare column this replaced.
//
// Recorded limit (review F8): this keys on bare base names, unsuffixed and
// id-free, so it can disagree with bySource's suffixed names. Safe
// direction — ambiguous degrades to "" — but not id-keyed.
func (nm *explainNames) resolveInAncestor(anc optimizer.Node, colName string) string {
	if nm == nil || anc == nil || colName == "" {
		return ""
	}
	var hit string
	n := 0
	var walk func(optimizer.Node)
	walk = func(node optimizer.Node) {
		if node == nil || n > 1 {
			return
		}
		if base, ok := explainRelBaseName(node); ok {
			for _, c := range node.Output() {
				if c.Name == colName {
					if base != hit {
						n++
						hit = base
					}
					break
				}
			}
		}
		for _, c := range planChildren(node) {
			walk(c)
		}
		// A-01(ii) cut 2 (F3): same Expr-hanging bodies as collect —
		// the exposing scan may sit inside a sibling sublink.
		for _, sp := range optimizer.NodeSubplans(node) {
			walk(sp)
		}
	}
	walk(anc)
	if n != 1 {
		return ""
	}
	return hit
}

// disambiguatedName returns the per-node label for EXPLAIN node headers
// (e.g. "Seq Scan on <name>"), disambiguated when the same base relation
// name appears on multiple plan nodes. An empty string means "no
// disambiguation needed" — this node already has a distinct alias or is the
// first occurrence.
//
// This is PG's select_rtable_names_for_explain (ruleutils.c) applied to
// plan node labels. It reads from nodeLabels, which collect builds in a
// separate pass from the column-qualification bySource table, because two
// distinct nodes need distinguishable node labels even where column
// qualification resolves both through one bySrc translation entry (e.g.
// SEMI-join sides over the same relation with the same SourceTableIdx).
func (nm *explainNames) disambiguatedName(n optimizer.Node) string {
	if nm == nil || n == nil {
		return ""
	}
	return nm.nodeLabels[nodePtr(n)]
}

// explainIsScanNode reports whether n is a scan-like node, i.e. one whose
// qual PostgreSQL renders through show_scan_qual rather than
// show_upper_qual. The distinction is a real one upstream: a scan's
// `Filter:` prints bare column names (`useprefix = IsA(plan, SubqueryScan)
// || es->verbose`), while a join/aggregate/result qual prints qualified ones
// (`useprefix = es->rtable_size > 1 || es->verbose`). Q30's CTE Scan filter
// is the case that makes the difference visible.
func explainIsScanNode(n optimizer.Node) bool {
	switch n.(type) {
	case *optimizer.SeqScan, *optimizer.IndexScan, *optimizer.IndexOnlyScan,
		*optimizer.CTEScan, *optimizer.MaterializedCTEScan,
		*optimizer.BitmapHeapScan:
		return true
	}
	return false
}
