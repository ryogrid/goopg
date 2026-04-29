package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/parser"
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

func (o *explainOp) Open(ctx *Context) error {
	o.rows = nil
	opts := o.plan.Options
	var stats nodeStatsTable
	var planNs, execNs int64

	if opts.Analyze {
		// M0018-0003: build the inner plan with instrumentation,
		// drain it to completion so timers fire, then render.
		// M0018-0004: TIMING and SUMMARY default to ON under
		// ANALYZE matching upstream; explicit OFF wins. The
		// `Set` companion struct distinguishes "user said off"
		// (Set.Timing && !opts.Timing) from "user said nothing"
		// (!Set.Timing).
		timing := !opts.Set.Timing || opts.Timing
		summary := !opts.Set.Summary || opts.Summary

		// Top-level Planning / Execution wallclock is independent
		// of per-node TIMING — measure it unconditionally under
		// ANALYZE so SUMMARY=true with TIMING=false still has a
		// number to report. Per-node time= bracket is suppressed
		// when nodeStats.timing is false (the wrapper skips
		// time.Now() at row granularity).
		planStart := time.Now()
		var inner Operator
		var err error
		inner, stats, err = withInstrumentation(timing, func() (Operator, error) {
			return Build(o.plan.Child)
		})
		if err != nil {
			return err
		}
		planNs = time.Since(planStart).Nanoseconds()

		execStart := time.Now()
		if err := inner.Open(ctx); err != nil {
			return err
		}
		for {
			_, err := inner.Next()
			if errors.Is(err, EOF) {
				break
			}
			if err != nil {
				_ = inner.Close()
				return err
			}
		}
		if err := inner.Close(); err != nil {
			return err
		}
		execNs = time.Since(execStart).Nanoseconds()

		if opts.Format == parser.ExplainFormatJSON {
			root := planToJSONWithStats(o.plan.Child, opts, stats)
			if summary {
				root["Planning Time"] = nsToMs(planNs)
				root["Execution Time"] = nsToMs(execNs)
			}
			out, err := json.MarshalIndent([]any{root}, "", "  ")
			if err != nil {
				return fmt.Errorf("explain: marshal JSON: %w", err)
			}
			o.rows = []Row{{Datum{Kind: KindString, String: string(out)}}}
			return nil
		}
		var b strings.Builder
		walkPlanAnalyze(&b, o.plan.Child, 0, &o.rows, opts, stats)
		if summary {
			o.rows = append(o.rows,
				Row{Datum{Kind: KindString, String: fmt.Sprintf("Planning Time: %.3f ms", nsToMs(planNs))}},
				Row{Datum{Kind: KindString, String: fmt.Sprintf("Execution Time: %.3f ms", nsToMs(execNs))}},
			)
		}
		return nil
	}

	if opts.Format == parser.ExplainFormatJSON {
		// FORMAT JSON: emit one row whose cell is the JSON-
		// encoded plan tree. The wrapping single-element array
		// matches upstream's `[ {root} ]` shape so future
		// extensions (multiple top-level entries) don't require
		// a schema change.
		root := planToJSON(o.plan.Child, opts)
		out, err := json.MarshalIndent([]any{root}, "", "  ")
		if err != nil {
			return fmt.Errorf("explain: marshal JSON: %w", err)
		}
		o.rows = []Row{{Datum{Kind: KindString, String: string(out)}}}
		return nil
	}
	var b strings.Builder
	walkPlan(&b, o.plan.Child, 0, &o.rows, opts)
	return nil
}

func nsToMs(ns int64) float64 { return float64(ns) / 1e6 }

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
//
// When opts.Verbose is set, an extra `Output: (col, ...)` line
// is emitted under each node listing its schema columns —
// mirrors upstream's `EXPLAIN VERBOSE` output (M0018-0002).
func walkPlan(b *strings.Builder, n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions) {
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

	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			outline := indent + "  Output: (" + strings.Join(cols, ", ") + ")"
			*rows = append(*rows, Row{Datum{Kind: KindString, String: outline}})
		}
	}

	for _, c := range planChildren(n) {
		walkPlan(b, c, depth+1, rows, opts)
	}
}

// schemaColumnNames returns the names of n's output columns,
// or nil when the node doesn't expose a schema (Insert/Update/
// Delete operators run for side effects and have empty Output).
func schemaColumnNames(n planner.Node) []string {
	out := n.Output()
	if len(out) == 0 {
		return nil
	}
	names := make([]string, len(out))
	for i, c := range out {
		names[i] = c.Name
	}
	return names
}

// walkPlanAnalyze is the ANALYZE variant of walkPlan: same
// indented TEXT output, but each node line gains an
// `(actual time=startup..total rows=R loops=L)` suffix pulled
// from the instrumentation table. Loops > 0 means the operator
// ran at least once. Total time is in milliseconds.
func walkPlanAnalyze(b *strings.Builder, n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions, stats nodeStatsTable) {
	indent := strings.Repeat("  ", depth)
	prefix := indent
	if depth > 0 {
		prefix = indent + "->  "
	}
	label := prefix + describePlan(n)
	if est := planner.EstimateRows(n); est > 0 {
		label += fmt.Sprintf(" (rows=%d)", est)
	}
	if s, ok := stats[n]; ok && s != nil {
		if s.timing {
			label += fmt.Sprintf(" (actual time=%.3f..%.3f rows=%d loops=%d)",
				nsToMs(s.startupNs), nsToMs(s.totalNs), s.rowsOut, s.loops)
		} else {
			label += fmt.Sprintf(" (actual rows=%d loops=%d)", s.rowsOut, s.loops)
		}
	}
	*rows = append(*rows, Row{Datum{Kind: KindString, String: label}})

	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			outline := indent + "  Output: (" + strings.Join(cols, ", ") + ")"
			*rows = append(*rows, Row{Datum{Kind: KindString, String: outline}})
		}
	}

	for _, c := range planChildren(n) {
		walkPlanAnalyze(b, c, depth+1, rows, opts, stats)
	}
}

// planToJSONWithStats is the ANALYZE variant of planToJSON: each
// node object grows Actual Rows / Actual Loops / Actual Startup
// Time / Actual Total Time fields keyed by the instrumented
// node's identity. Mirrors upstream's JSON ANALYZE shape.
func planToJSONWithStats(n planner.Node, opts parser.ExplainOptions, stats nodeStatsTable) map[string]any {
	obj := planToJSON(n, opts)
	if s, ok := stats[n]; ok && s != nil {
		obj["Actual Rows"] = s.rowsOut
		obj["Actual Loops"] = s.loops
		if s.timing {
			obj["Actual Startup Time"] = nsToMs(s.startupNs)
			obj["Actual Total Time"] = nsToMs(s.totalNs)
		}
	}
	// Re-render Plans recursively with stats, replacing the
	// stats-free children that planToJSON installed.
	children := planChildren(n)
	if len(children) > 0 {
		plans := make([]map[string]any, 0, len(children))
		for _, c := range children {
			plans = append(plans, planToJSONWithStats(c, opts, stats))
		}
		obj["Plans"] = plans
	}
	return obj
}

// planToJSON renders n as the upstream-style JSON object an
// `EXPLAIN (FORMAT JSON)` row carries. Recursive — children land
// in a `Plans` array. The Output column-name array is emitted
// only when opts.Verbose is set (matches upstream's behaviour
// where columns are part of VERBOSE output, not the default
// JSON shape).
func planToJSON(n planner.Node, opts parser.ExplainOptions) map[string]any {
	obj := map[string]any{
		"Node Type": describePlan(n),
	}
	if est := planner.EstimateRows(n); est > 0 {
		obj["Plan Rows"] = est
	}
	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			obj["Output"] = cols
		}
	}
	children := planChildren(n)
	if len(children) > 0 {
		plans := make([]map[string]any, 0, len(children))
		for _, c := range children {
			plans = append(plans, planToJSON(c, opts))
		}
		obj["Plans"] = plans
	}
	return obj
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
		if p.Algo == planner.JoinAlgoHash && p.BuildLeft {
			return fmt.Sprintf("%s (%s, build=left)", algo, joinTypeName(p.Type))
		}
		return fmt.Sprintf("%s (%s)", algo, joinTypeName(p.Type))
	case *planner.Aggregate:
		if len(p.GroupExprs) == 0 {
			return fmt.Sprintf("Aggregate (%d aggregates)", len(p.Aggs))
		}
		return fmt.Sprintf("GroupAggregate (%d keys, %d aggregates)", len(p.GroupExprs), len(p.Aggs))
	case *planner.SeqScan:
		// `(stats)` flags scans whose Table.Stats has been
		// populated by ANALYZE — the planner's cost-driven
		// decisions (Filter selectivity from MCV / histogram,
		// INNER-join algorithm choice) are only active for
		// these. M0006 / 0006-0004 surfaces this so an operator
		// inspecting EXPLAIN can verify which scans feed the
		// cost model.
		if p.Table != nil && p.Table.Stats != nil {
			return fmt.Sprintf("Seq Scan on %s (stats)", p.Table.QualifiedName())
		}
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
	case *planner.CTEScan:
		// Mirrors upstream's "CTE Scan on <name>" label; the
		// alias is rendered separately when distinct so output
		// like `WITH a AS (SELECT 1) SELECT * FROM a x` shows
		// `CTE Scan on a x`.
		if p.Alias != "" && p.Alias != p.Name {
			return fmt.Sprintf("CTE Scan on %s %s", p.Name, p.Alias)
		}
		return fmt.Sprintf("CTE Scan on %s", p.Name)
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
	case *planner.CTEScan:
		return []planner.Node{p.Child}
	}
	return nil
}
