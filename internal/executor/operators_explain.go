package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

// explainOp renders the inner plan tree as a single-column
// "QUERY PLAN" text result. v0 emits one row per plan node in
// pre-order with two-space indent per nesting level, matching
// upstream PG's text-format output well enough for
// debugging-by-eyeball during the M0003 planner work. EXPLAIN
// ANALYZE / VERBOSE / FORMAT JSON wait on later loops; see
// docs/design/0003-0007-explain.md.
type explainOp struct {
	plan *planner.Explain
	rows []Row
	idx  int
}

func newExplainOp(p *planner.Explain) *explainOp {
	return &explainOp{plan: p}
}

func (o *explainOp) Schema() planner.Schema {
	return o.plan.Output()
}

func (o *explainOp) Open(_ *Context) error {
	var b strings.Builder
	o.rows = nil
	walkPlan(&b, o.plan.Child, 0, &o.rows)
	return nil
}

func (o *explainOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return r, nil
}

func (o *explainOp) Close() error { return nil }

// walkPlan emits one row per node in n with the given indent
// level (0 = root, +1 per descend). Each row carries a single
// KindString datum already formatted as "<indent>->  <node
// label>" — upstream's render shape — except the root which
// has no leading "->".
func walkPlan(b *strings.Builder, n planner.Node, depth int, rows *[]Row) {
	indent := strings.Repeat("  ", depth)
	prefix := indent
	if depth > 0 {
		prefix = indent + "->  "
	}
	label := prefix + describePlan(n)
	// Append `(rows=N)` when the planner has a non-zero
	// estimate. Zero means "no statistics yet" — leave it out
	// rather than printing a misleading `(rows=0)` for tables
	// that haven't been ANALYZE'd. Matches upstream's
	// "EXPLAIN doesn't show costs without ANALYZE" behaviour.
	if est := planner.EstimateRows(n); est > 0 {
		label += fmt.Sprintf(" (rows=%d)", est)
	}
	*rows = append(*rows, Row{Datum{Kind: KindString, String: label}})

	for _, c := range planChildren(n) {
		walkPlan(b, c, depth+1, rows)
	}
}

// describePlan renders the v0 single-line label for a plan node.
// Format mirrors upstream's `<NodeType> [details]` pattern; the
// details portion captures algorithm hints (hash vs. nested-loop
// for Join), table names (SeqScan / IndexScan), and aggregate
// shapes that are useful for verifying planner choices without
// running the query.
func describePlan(n planner.Node) string {
	switch p := n.(type) {
	case *planner.Project:
		return "Projection"
	case *planner.Filter:
		return "Filter"
	case *planner.Sort:
		return "Sort"
	case *planner.Limit:
		return "Limit"
	case *planner.Values:
		return fmt.Sprintf("Values (%d rows)", len(p.Rows))
	case *planner.Join:
		algo := "Nested Loop"
		if p.Algo == planner.JoinAlgoHash {
			algo = "Hash Join"
		}
		if p.Algo == planner.JoinAlgoMerge {
			algo = "Merge Join"
		}
		return fmt.Sprintf("%s (%s)", algo, joinTypeName(p.Type))
	case *planner.Aggregate:
		if len(p.GroupExprs) == 0 {
			return fmt.Sprintf("Aggregate (%d aggregates)", len(p.Aggs))
		}
		return fmt.Sprintf("GroupAggregate (%d keys, %d aggregates)", len(p.GroupExprs), len(p.Aggs))
	case *planner.SeqScan:
		return fmt.Sprintf("Seq Scan on %s", p.Table.QualifiedName())
	case *planner.IndexScan:
		return fmt.Sprintf("Index Scan using %s on %s", p.Index.QualifiedName(), p.Table.QualifiedName())
	case *planner.Insert:
		return fmt.Sprintf("Insert on %s", p.Table.QualifiedName())
	case *planner.Update:
		return fmt.Sprintf("Update on %s", p.Table.QualifiedName())
	case *planner.Delete:
		return fmt.Sprintf("Delete on %s", p.Table.QualifiedName())
	case *planner.DDL:
		return fmt.Sprintf("DDL %T", p.Stmt)
	case *planner.Transaction:
		return fmt.Sprintf("Transaction (%v)", p.Verb)
	case *planner.Checkpoint:
		return "Checkpoint"
	case *planner.Utility:
		return fmt.Sprintf("Utility %T", p.Stmt)
	case *planner.Copy:
		return fmt.Sprintf("Copy %s", p.Table.QualifiedName())
	case *planner.Explain:
		return "Explain"
	}
	return fmt.Sprintf("%T", n)
}

func joinTypeName(t planner.JoinType) string {
	switch t {
	case planner.JoinTypeInner:
		return "INNER"
	case planner.JoinTypeLeft:
		return "LEFT"
	case planner.JoinTypeRight:
		return "RIGHT"
	case planner.JoinTypeFull:
		return "FULL"
	case planner.JoinTypeCross:
		return "CROSS"
	}
	return "?"
}

// planChildren returns the child plan nodes EXPLAIN should
// recurse into. Limited to the subset of node types whose
// children are public Node fields; storage-side internals
// (Update/Delete/Insert source plans) report their own scan
// children. Returns nil for leaf nodes.
func planChildren(n planner.Node) []planner.Node {
	switch p := n.(type) {
	case *planner.Project:
		return []planner.Node{p.Child}
	case *planner.Filter:
		return []planner.Node{p.Child}
	case *planner.Sort:
		return []planner.Node{p.Child}
	case *planner.Limit:
		return []planner.Node{p.Child}
	case *planner.Aggregate:
		return []planner.Node{p.Child}
	case *planner.Join:
		return []planner.Node{p.Left, p.Right}
	case *planner.Insert:
		return []planner.Node{p.Source}
	}
	return nil
}
