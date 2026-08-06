package executor

// M0125-0049 — EXPLAIN prints a CTE body ONCE, as a `CTE <name>` section
// under the top plan node, and every reference as a bare `CTE Scan` leaf.
//
// Why this was wrong. goopg's non-recursive CTE planning (M0016-0002) hands
// every consumer the SAME body Node: `planScanRangeVar` builds each CTEScan
// with `Child: ce.body`, so the plan is a DAG, not a tree. The EXPLAIN walker
// walks it as a tree, so an N-times-referenced CTE printed its whole subtree N
// times. TPC-DS Q67 under M0125-0040's grouping-sets source-sharing hoist
// (since retired by M0125-0048) showed eight `CTE Scan on __gs_src_871` nodes
// each carrying a full copy of a four-table join — 36 `store_sales` mentions
// for a join that EXECUTES once (the rows are materialised on the first
// reference and replayed from ctx.CTERowCache). The plan therefore over-stated
// the work by the reference count. The same over-statement reaches any
// multiply-referenced user CTE, which is why the fix outlived the hoist that
// exposed it.
//
// PostgreSQL renders the same shape as ONE section. `SS_process_ctes`
// (optimizer/plan/subselect.c) turns each WITH entry into a subplan of the top
// plan node, and explain.c's ExplainSubPlans prints it under a `CTE <name>`
// heading, in declaration order, after the top node's own detail lines and
// before its InitPlans and children. Verified against PG 18.3:
//
//	 Hash Join
//	   Hash Cond: (q.b = p.a)
//	   CTE x
//	     ->  Seq Scan on t
//	           Filter: (a > 5)
//	   ->  CTE Scan on x q
//	   ->  Hash
//	         ->  CTE Scan on x p
//
// Two divergences from upstream are deliberate and recorded in the deferral
// ledger, both stemming from goopg having no plan-level marker for "this query
// level declared this WITH list":
//
//  1. Sections are hoisted to the root of the rendered plan. PG attaches them
//     to the top node of the DECLARING query level, so a WITH nested inside a
//     CTE body prints its section inside that body; goopg lifts it to the top.
//  2. A CTE referenced ONLY from inside a sublink is not collected (the
//     collector walks the plan spine, not expression trees) and still renders
//     its body inline under its `CTE Scan`. It is printed once either way —
//     one reference is the only way to reach that case.
//
// See docs/design/0125-0049-explain-shared-cte-section.md.

import (
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

// cteSection is one `CTE <name>` heading plus the body it prints.
type cteSection struct {
	name    string
	body    planner.Node
	declSeq int
}

// cteHoist is the per-EXPLAIN-render set of CTE bodies that have been lifted
// out of their reference sites. A declaration present in byDecl renders as a
// leaf wherever it is referenced, INCLUDING inside sublink subtrees (the same
// subPlanReg — and therefore the same cteHoist — is threaded through them).
type cteHoist struct {
	order  []*cteSection
	byDecl map[string]*cteSection
}

// collectCTEHoist walks the plan spine of root and claims one body per CTE
// DECLARATION, keyed exactly as the runtime keys its buffer — CTEScan.DeclKey,
// the declaring CommonTableExpr's source offset plus its name.
//
// Matching the runtime key is the whole requirement: `ctx.CTERowCache` is
// keyed by DeclKey, the first CTEScan for a declaration buffers the rows and
// every later scan of it replays them (operators_cte_dml.go, cteScanOp.Open),
// so one declaration means exactly one materialisation and one section is what
// EXPLAIN should print. Both cheaper identities are wrong in one direction:
//
//   - A body-POINTER key would under-hoist. A plain `WITH` hands every consumer
//     the same body Node (planScanRangeVar's `Child: ce.body`), but planSelect
//     re-enters on the head operand of a set-op chain, so any declaration
//     reached that way is planned twice — distinct, structurally identical
//     bodies for the one buffer they share, and two sections printed for one
//     materialisation. (The witness was M0125-0040's synthetic `__gs_src_N`,
//     retired by M0125-0048; the re-entry it exposed is not.)
//   - A NAME key would over-hoist, and used to: two DIFFERENT declarations
//     sharing a name in disjoint scopes collapsed into one section. That was
//     the render matching the runtime while the runtime was also wrong
//     (M0125-0050: goopg answered 1,1 where PG answers 1,2). Both are keyed by
//     declaration now, so such a query prints two `CTE x` sections — which is
//     also what PG prints, one per subplan.
//
// Returns nil when the plan references no CTE, so the render path costs
// nothing for the overwhelming majority of statements.
func collectCTEHoist(root planner.Node) *cteHoist {
	if root == nil {
		return nil
	}
	h := &cteHoist{byDecl: map[string]*cteSection{}}
	var walk func(planner.Node)
	walk = func(n planner.Node) {
		if n == nil {
			return
		}
		if scan, ok := n.(*planner.CTEScan); ok && scan.Child != nil {
			key := scan.DeclKey()
			if _, claimed := h.byDecl[key]; claimed {
				// A second reference to an already-claimed name. Do NOT
				// descend: the body is the same buffer, so descending would
				// only re-walk a subtree whose CTEs are already claimed.
				return
			}
			sec := &cteSection{name: scan.Name, body: scan.Child, declSeq: scan.DeclSeq()}
			h.byDecl[key] = sec
			h.order = append(h.order, sec)
			// A claimed body may itself reference another CTE (`WITH x AS
			// (...), y AS (SELECT ... FROM x)`), so keep descending — that
			// inner name needs a section of its own.
			walk(scan.Child)
			return
		}
		for _, c := range planChildren(n) {
			walk(c)
		}
	}
	walk(root)
	if len(h.order) == 0 {
		return nil
	}
	// Declaration order, matching SS_process_ctes' left-to-right walk of the
	// WITH list. Stable so equal seqs (scans built outside preplanWithClause)
	// keep tree order.
	sort.SliceStable(h.order, func(i, j int) bool { return h.order[i].declSeq < h.order[j].declSeq })
	return h
}

// hoisted reports whether n is a CTEScan whose body prints as a section
// elsewhere — i.e. whether the walker must render it as a leaf.
func (h *cteHoist) hoisted(n planner.Node) bool {
	if h == nil {
		return false
	}
	scan, ok := n.(*planner.CTEScan)
	if !ok {
		return false
	}
	_, claimed := h.byDecl[scan.DeclKey()]
	return claimed
}

// emitCTESections prints the render's `CTE <name>` headings and their bodies
// under the node currently being rendered at `depth`. It fires only at the top
// node (depth 0) — sections are attached to the plan's root the way upstream
// attaches them to the top plan node's subplan list. A CTE body itself renders
// at depth+2 (heading at the children's indent WITHOUT the `->  ` arrow, body
// one level below it with one), which is the same two-line shape upstream uses
// for `InitPlan N`:
//
//	  CTE x            <- depth+1 indent, no arrow
//	    ->  Seq Scan   <- depth+2
//
// The sections are drained (order emptied) as they print, so the recursive
// render of a body — which passes the same subPlanReg — cannot re-emit them.
func emitCTESections(rows *[]Row, depth int, reg *subPlanReg, render func(planner.Node, int)) {
	if reg == nil || reg.cte == nil || depth != 0 || len(reg.cte.order) == 0 {
		return
	}
	sections := reg.cte.order
	reg.cte.order = nil
	// The owning node has already assigned numbers to the sublinks in its own
	// detail lines (emitNodeDetailLines runs first) but has not yet printed
	// their subtrees. Rendering a CTE body re-enters the walker, whose own
	// emitSubPlanSubtrees would otherwise drain that queue HERE — printing the
	// owner's `SubPlan N` subtree inside the CTE section, and deparsing its
	// correlated references against a body node instead of the owner. Hold the
	// queue aside for the duration; the owner drains it right after we return.
	held := reg.takePending()
	heading := strings.Repeat("  ", depth+1)
	for _, sec := range sections {
		*rows = append(*rows, Row{NewStringDatum(heading + "CTE " + sec.name)})
		render(sec.body, depth+2)
	}
	reg.pending = append(held, reg.pending...)
}

// renderChildren returns the children the EXPLAIN walkers should descend into.
// It is planChildren everywhere except at a hoisted CTE Scan, which is a leaf.
func renderChildren(n planner.Node, h *cteHoist) []planner.Node {
	if h.hoisted(n) {
		return nil
	}
	return planChildren(n)
}
