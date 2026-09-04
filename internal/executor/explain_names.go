package executor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

// explainNames is EXPLAIN's range-table name table: the mapping from a
// column's binding-of-origin (planner.ColumnRef.SourceTableIdx, the
// per-FROM-clause monotonic id M0071-0009 introduced) to the name EXPLAIN
// prints in front of that column.
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
	// bySource maps SourceTableIdx → printed name. Entry 0 is never
	// populated: sourceIdx 0 means "no identity assigned" (CTE-only /
	// subquery-only bindings), and those stay unqualified, exactly as
	// they render today.
	bySource map[int16]string
	// taken counts how many relations already claimed a given base name,
	// so the second `date_dim` becomes `date_dim_1`.
	taken map[string]int
	// seen guards against registering the same subtree twice — a subplan
	// body is collected when its `SubPlan N` subtree is about to render,
	// and a plan node can be reached more than once that way.
	seen map[int16]bool
	// cols is the column set the relation registered for a binding
	// actually exposes, and it is a correctness guard, not a nicety.
	// planner.go's `nextSourceIdx` restarts at 1 for EVERY query level
	// (planFromItem / the FROM loop), so a subquery binding in the outer
	// scope and a base relation inside that subquery can carry the SAME
	// SourceTableIdx. A ColumnRef naming a derived column then resolves
	// to an unrelated relation, and EXPLAIN would print a confident,
	// wrong qualifier — strictly worse than the bare name it replaced.
	// Requiring the column name to exist in the claimed relation turns
	// that case back into today's unqualified rendering. See the
	// M0125-0039 deferral row: the complete fix is a globally unique
	// range-table id (PostgreSQL's varno), which is planner work.
	cols map[int16]map[string]bool
	// nodeLabels maps each plan node to its EXPLAIN-node-label name,
	// disambiguated independently of SourceTableIdx so two distinct
	// nodes that share a SourceTableIdx (e.g. a SEMI-join outer and
	// inner side over the same relation) still get distinguishable
	// labels. Keyed by nodePtr(n). Populated by collect; nil means no
	// disambiguation was needed.
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
		bySource:   map[int16]string{},
		taken:      map[string]int{},
		seen:       map[int16]bool{},
		cols:       map[int16]map[string]bool{},
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
// binding has no name (sourceIdx 0, or a node kind that carries no relation).
func (nm *explainNames) name(src int16) string {
	if nm == nil {
		return ""
	}
	return nm.bySource[src]
}

// column renders one column reference. prefix is upstream's
// context->varprefix, resolved by the caller: false renders the bare column
// name, true renders `<relation>.<column>` whenever the binding is known.
// A binding with no name (sourceIdx 0, or a node kind that carries no
// relation) always renders bare — there is nothing truthful to print.
func (nm *explainNames) column(src int16, colName string, prefix bool) string {
	if !prefix {
		return colName
	}
	rel := nm.name(src)
	if rel == "" || !nm.cols[src][colName] {
		return colName
	}
	return rel + "." + colName
}

// collect walks the plan subtree rooted at n and registers every scan-like
// node's relation name. Registration is ordered by SourceTableIdx (the
// FROM-clause order the planner assigned) rather than by tree position, so
// the `_N` suffixes do not depend on which join shape the planner picked.
func (nm *explainNames) collect(n optimizer.Node) {
	if nm == nil || n == nil {
		return
	}
	type entry struct {
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
			if src, ok := explainSingleSourceIdx(node); ok {
				found = append(found, entry{src: src, base: base, node: node})
			}
		}
		for _, c := range planChildren(node) {
			walk(c)
		}
	}
	walk(n)
	sort.SliceStable(found, func(i, j int) bool { return found[i].src < found[j].src })
	for _, e := range found {
		nm.register(e.src, e.base, e.node)
	}

	// M0128-P5.1: assign per-node disambiguated labels for the EXPLAIN
	// node label ("Seq Scan on <name>"). This is a separate pass from
	// column qualification (bySource) because two distinct nodes can
	// share a SourceTableIdx (e.g. SEMI-join sides over the same
	// relation) — the column-qualification register() skips the second
	// one via its `seen` guard, but the node label still needs
	// disambiguation.
	//
	// Labels follow PG select_rtable_names_for_explain: first occurrence
	// of a base name keeps it bare; subsequent ones get _1, _2, etc.
	labelTaken := map[string]int{}
	for _, e := range found {
		base := e.base
		n := labelTaken[base]
		labelTaken[base] = n + 1
		if n > 0 {
			base += "_" + strconv.Itoa(n)
			nm.nodeLabels[nodePtr(e.node)] = base
		}
	}
}

// register claims a printed name for one binding. First occurrence of a base
// name keeps it bare; later ones get `_1`, `_2`, … (set_rtable_names).
//
// First registration wins for a given binding id. Root-tree relations are
// collected before any sublink body's, so when a subplan-local binding
// collides with an outer one (the per-query-level SourceTableIdx counter
// makes that possible) the outer name is the one kept — which is the name a
// correlated OuterColumnRef, the case this table exists for, needs.
func (nm *explainNames) register(src int16, base string, node optimizer.Node) {
	if src == 0 || base == "" || nm.seen[src] {
		return
	}
	nm.seen[src] = true
	cols := map[string]bool{}
	for _, c := range node.Output() {
		cols[c.Name] = true
	}
	nm.cols[src] = cols
	n := nm.taken[base]
	nm.taken[base] = n + 1
	if n > 0 {
		base += "_" + strconv.Itoa(n)
	}
	nm.bySource[src] = base
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
	}
	return "", false
}

// explainSingleSourceIdx returns the binding id every column of n's output
// carries, when there is exactly one. A scan node's columns all come from
// one FROM-clause entry, so this is how a plan node is tied back to its
// range-table identity without the planner having to store it twice.
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
// distinct nodes can share a SourceTableIdx (the column-qualification
// register() skips the second one via its `seen` guard) but still need
// distinguishable node labels.
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
		*optimizer.CTEScan, *optimizer.MaterializedCTEScan:
		return true
	}
	return false
}
