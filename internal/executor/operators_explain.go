package executor

import (
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

		if opts.Format == parser.ExplainFormatJSON || opts.Format == parser.ExplainFormatXML || opts.Format == parser.ExplainFormatYAML {
			// Upstream nests the plan tree under a top-level "Plan" key, with
			// Planning Time / Execution Time as its siblings:
			//   [ { "Plan": {...}, "Planning Time": .., "Execution Time": .. } ]
			// horizons.spec reads ...->0->'Plan'->'Heap Fetches', so the
			// wrapper is load-bearing (design 0118-0102).
			trackIOTiming := ctx.Activity != nil && ctx.Activity.TrackIOTiming(ctx.ProcNum)
			root := map[string]any{"Plan": planToJSONWithStats(o.plan.Child, opts, stats, trackIOTiming)}
			if summary {
				root["Planning Time"] = nsToMs(planNs)
				root["Execution Time"] = nsToMs(execNs)
			}
			addExplainSettingsGroup(ctx, opts, root)
			out, err := renderExplainTree(opts.Format, root)
			if err != nil {
				return err
			}
			o.rows = []Row{{NewStringDatum(out)}}
			return nil
		}
		var b strings.Builder
		walkPlanAnalyze(&b, o.plan.Child, 0, &o.rows, opts, stats)
		appendExplainSettingsRow(ctx, opts, &o.rows)
		if summary {
			o.rows = append(o.rows,
				Row{NewStringDatum(fmt.Sprintf("Planning Time: %.3f ms", nsToMs(planNs)))},
				Row{NewStringDatum(fmt.Sprintf("Execution Time: %.3f ms", nsToMs(execNs)))},
			)
		}
		return nil
	}

	if opts.Format == parser.ExplainFormatJSON || opts.Format == parser.ExplainFormatXML || opts.Format == parser.ExplainFormatYAML {
		// FORMAT JSON/XML/YAML: emit one row whose cell is the
		// serialized plan tree, nested under a top-level "Plan" key
		// inside the single-element array, matching upstream's
		// `[ { "Plan": {root} } ]` shape (design 0118-0102).
		root := map[string]any{"Plan": planToJSON(o.plan.Child, opts)}
		addExplainSettingsGroup(ctx, opts, root)
		out, err := renderExplainTree(opts.Format, root)
		if err != nil {
			return err
		}
		o.rows = []Row{{NewStringDatum(out)}}
		return nil
	}
	var b strings.Builder
	walkPlan(&b, o.plan.Child, 0, &o.rows, opts)
	appendExplainSettingsRow(ctx, opts, &o.rows)
	return nil
}

// appendExplainSettingsRow adds the upstream `Settings: k = 'v', ...` TEXT
// line (ExplainPrintSettings in explain.c, non-JSON branch) when EXPLAIN
// (SETTINGS) was requested and at least one FlagExplain-tagged GUC differs
// from its built-in default. Unlike the structured formats, PG prints
// nothing at all — not even an empty "Settings:" label — when the
// modified-GUC list is empty.
func appendExplainSettingsRow(ctx *Context, opts parser.ExplainOptions, rows *[]Row) {
	if !opts.Settings || ctx == nil || ctx.ExplainSettings == nil {
		return
	}
	vals := ctx.ExplainSettings()
	if len(vals) == 0 {
		return
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("%s = '%s'", v.Name, v.Value)
	}
	*rows = append(*rows, Row{NewStringDatum("Settings: " + strings.Join(parts, ", "))})
}

// addExplainSettingsGroup adds the "Settings" object that PG's structured
// (JSON/XML/YAML) formats always include once SETTINGS is requested — unlike
// TEXT, the group is present (as an empty object) even with zero modified
// GUCs, mirroring ExplainPrintSettings's format != EXPLAIN_FORMAT_TEXT branch
// which has no early return.
func addExplainSettingsGroup(ctx *Context, opts parser.ExplainOptions, root map[string]any) {
	if !opts.Settings {
		return
	}
	settings := map[string]any{}
	if ctx != nil && ctx.ExplainSettings != nil {
		for _, v := range ctx.ExplainSettings() {
			settings[v.Name] = v.Value
		}
	}
	root["Settings"] = settings
}

func nsToMs(ns int64) float64 { return float64(ns) / 1e6 }

// formatBuffersLine renders the upstream "Buffers: shared hit=N read=N
// dirtied=N written=N" text (show_buffer_usage's has_shared/shared-buffer
// branch in postgres/src/backend/commands/explain.c), omitting the whole
// line when all four counters are zero and omitting each individual
// hit=/read=/dirtied=/written= term when that counter is zero.
func formatBuffersLine(s *nodeStats) string {
	if s.bufHit == 0 && s.bufRead == 0 && s.bufDirtied == 0 && s.bufWritten == 0 {
		return ""
	}
	parts := make([]string, 0, 4)
	if s.bufHit > 0 {
		parts = append(parts, fmt.Sprintf("hit=%d", s.bufHit))
	}
	if s.bufRead > 0 {
		parts = append(parts, fmt.Sprintf("read=%d", s.bufRead))
	}
	if s.bufDirtied > 0 {
		parts = append(parts, fmt.Sprintf("dirtied=%d", s.bufDirtied))
	}
	if s.bufWritten > 0 {
		parts = append(parts, fmt.Sprintf("written=%d", s.bufWritten))
	}
	return "Buffers: shared " + strings.Join(parts, " ")
}

// formatIOTimingsLine renders the upstream "I/O Timings: shared read=X.XXX
// write=Y.YYY" text (show_buffer_usage's has_shared_timing branch in
// postgres/src/backend/commands/explain.c), omitting the whole line when
// both counters are zero and each individual read=/write= term when that
// counter is zero. bufWriteTimeNs already folds extend time in (see the
// nodeStats doc comment), matching upstream's single "write=" term — there
// is no separate "extend=" term in real PG's I/O Timings line either. The
// times are naturally zero whenever track_io_timing is off, so no extra
// gate is needed beyond the nonzero check here — unlike TEXT's "Buffers:"
// line this happens to also match the non-text (JSON/XML/YAML) property
// gate goopg uses below, though upstream's non-text branch actually gates
// on the track_io_timing GUC rather than on the values being nonzero
// (goopg's simplification is behaviorally identical in the tested case:
// GUC on and I/O occurred vs. GUC off, differing only when the GUC is on
// but a node touched zero blocks — deferred, ledger).
func formatIOTimingsLine(s *nodeStats) string {
	if s.bufReadTimeNs == 0 && s.bufWriteTimeNs == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if s.bufReadTimeNs > 0 {
		parts = append(parts, fmt.Sprintf("read=%.3f", nsToMs(s.bufReadTimeNs)))
	}
	if s.bufWriteTimeNs > 0 {
		parts = append(parts, fmt.Sprintf("write=%.3f", nsToMs(s.bufWriteTimeNs)))
	}
	return "I/O Timings: shared " + strings.Join(parts, " ")
}

func (o *explainOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return asSlot(o.Schema(), r), nil
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
//
// M0100-0005i: wrapper nodes that PG does not surface as their
// own plan node are skipped:
//   - Project nodes always fold into the child (PG renders the
//     projection inline as part of the parent / scan label;
//     there is no "Projection" plan node in upstream EXPLAIN).
//   - Filter nodes fold into the child when the child is a scan
//     (the predicate surfaces as `Filter: (<pred>)` under the
//     scan label, matching upstream).
//
// PG-style detail lines are emitted under the rendered node:
//   - Sort → `Sort Key: <expr_csv>`
//   - IndexScan → `Index Cond: (<col> = <key>)` (single-col eq)
//   - SeqScan with attached Filter → `Filter: (<pred>)`
//
// `(rows=N)` is only appended when opts.Costs is true (the PG
// default). `EXPLAIN (COSTS OFF) ...` therefore renders bare
// node labels, matching upstream `COSTS OFF` output.
func walkPlan(b *strings.Builder, n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions) {
	walkPlanFiltered(n, depth, rows, opts, nil)
}

// walkPlanFiltered is the inner driver for walkPlan. attachedFilter
// is a Filter.Predicate carried down from a Filter wrapper that
// was skipped above us — it is rendered as `Filter:` detail under
// the next scan-like node we render.
func walkPlanFiltered(n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions, attachedFilter planner.Expr) {
	// Skip Project wrappers: PG has no "Projection" plan node;
	// the projection is part of the parent / scan's render.
	if p, ok := n.(*planner.Project); ok {
		walkPlanFiltered(p.Child, depth, rows, opts, attachedFilter)
		return
	}
	// Skip Filter wrappers and push their predicate down to be
	// rendered as `Filter:` detail under the next scan node.
	if f, ok := n.(*planner.Filter); ok {
		next := f.Predicate
		// If multiple Filter wrappers stack, render only the
		// outermost predicate to keep the detail line readable.
		// Inner Filter predicates collapse with the outer via
		// short-circuit AND — but PG's Filter detail is a single
		// expression line; chaining is uncommon so prefer the
		// outermost predicate for v0.
		if attachedFilter != nil {
			next = attachedFilter
		}
		walkPlanFiltered(f.Child, depth, rows, opts, next)
		return
	}

	indent := strings.Repeat("  ", depth)
	prefix := indent
	if depth > 0 {
		prefix = indent + "->  "
	}
	label := prefix + describePlanVerbose(n, opts.Verbose)
	// COSTS defaults to ON in PostgreSQL (and goopg); only suppress when
	// the user explicitly wrote COSTS OFF (Set.Costs=true and Costs=false).
	showCosts := !opts.Set.Costs || opts.Costs
	if showCosts {
		est := planner.EstimateRows(n)
		if est <= 0 {
			est = 1
		}
		// Emit PG-compatible cost annotation: (cost=0.00..0.00 rows=N width=0)
		// The mock 0.00 costs are replaced by 'N' in EXPLAIN normalization.
		label += fmt.Sprintf("  (cost=0.00..0.00 rows=%d width=0)", est)
	}
	*rows = append(*rows, Row{NewStringDatum(label)})

	// Detail indent: matches the content column of the node
	// label plus 2 spaces, mirroring PG's `Sort Key:` / `Index
	// Cond:` / `Filter:` indent convention.
	detailIndent := strings.Repeat(" ", len(prefix)+2)
	emitNodeDetailLines(n, detailIndent, rows, attachedFilter)

	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			outline := indent + "  Output: " + strings.Join(cols, ", ")
			*rows = append(*rows, Row{NewStringDatum(outline)})
		}
	}

	for _, c := range planChildren(n) {
		walkPlanFiltered(c, depth+1, rows, opts, nil)
	}
}

// emitNodeDetailLines writes the PG-style detail lines that
// belong under n (Sort Key / Index Cond / Filter). attachedFilter
// is a Filter.Predicate from a Filter wrapper above n that was
// skipped — it surfaces as `Filter:` when n is a scan-like node.
func emitNodeDetailLines(n planner.Node, indent string, rows *[]Row, attachedFilter planner.Expr) {
	switch p := n.(type) {
	case *planner.Sort:
		if len(p.Keys) > 0 {
			parts := make([]string, 0, len(p.Keys))
			for _, k := range p.Keys {
				s := formatExprPG(k.Expr)
				if k.Desc {
					s += " DESC"
				}
				// Emit NULLS FIRST/LAST only when it's non-default.
				// Default: ASC → NULLS LAST, DESC → NULLS FIRST.
				if k.NullsFirst && !k.Desc {
					s += " NULLS FIRST"
				} else if !k.NullsFirst && k.Desc {
					s += " NULLS LAST"
				}
				parts = append(parts, s)
			}
			*rows = append(*rows, Row{NewStringDatum(indent + "Sort Key: " + strings.Join(parts, ", "))})
		}
	case *planner.IndexScan:
		if cond := formatIndexCond(p); cond != "" {
			*rows = append(*rows, Row{NewStringDatum(indent + "Index Cond: " + cond)})
		}
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprPG(attachedFilter)))})
		}
	case *planner.SeqScan:
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprPG(attachedFilter)))})
		}
	default:
		// Non-scan nodes keep an attached Filter alive — render it
		// here so the predicate is not silently dropped when our
		// planner places a Filter above a non-scan (e.g. an
		// Aggregate). Matches PG's behaviour of rendering Filter on
		// the node it most directly applies to.
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprPG(attachedFilter)))})
		}
	}
}

// formatIndexCond renders the equality / range condition of an
// IndexScan node as a PG-style `(col = key)` (or range) expression.
// Empty when the scan has no bound (full-range probe).
func formatIndexCond(p *planner.IndexScan) string {
	if p == nil || p.Index == nil {
		return ""
	}
	cols := p.Index.Columns
	// Multi-column equality probe.
	if len(p.Keys) > 0 && len(cols) >= len(p.Keys) {
		if len(p.Keys) == 1 {
			return wrapParen(cols[0] + " = " + formatExprPG(p.Keys[0]))
		}
		parts := make([]string, len(p.Keys))
		for i, k := range p.Keys {
			parts[i] = cols[i] + " = " + formatExprPG(k)
		}
		return wrapParen(strings.Join(parts, " AND "))
	}
	// Single-column equality.
	if p.Key != nil && len(cols) > 0 {
		return wrapParen(cols[0] + " = " + formatExprPG(p.Key))
	}
	// Range scan.
	if (p.LowKey != nil || p.HighKey != nil) && len(cols) > 0 {
		col := cols[0]
		var parts []string
		if p.LowKey != nil {
			parts = append(parts, col+" >= "+formatExprPG(p.LowKey))
		}
		if p.HighKey != nil {
			parts = append(parts, col+" <= "+formatExprPG(p.HighKey))
		}
		if len(parts) > 0 {
			return wrapParen(strings.Join(parts, " AND "))
		}
	}
	return ""
}

// wrapParen wraps s in parentheses unless it already is parenthesised.
func wrapParen(s string) string {
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		// Heuristic: avoid double-wrap when the whole expression
		// is already a single parenthesised group.
		depth := 0
		for i, r := range s {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i < len(s)-1 {
					// Closing paren before end → not a single
					// wrapping group; needs outer parens.
					return "(" + s + ")"
				}
			}
		}
		return s
	}
	return "(" + s + ")"
}

// formatExprPG renders a planner expression in upstream PG's
// EXPLAIN style: column names, integer/string/numeric literals,
// and infix operators. Falls back to a compact `<type>` token
// for expression kinds we don't yet render (sufficient for the
// isolation specs that pass through `EXPLAIN (COSTS OFF)`; the
// detail line is informational).
func formatExprPG(e planner.Expr) string {
	if e == nil {
		return ""
	}
	switch x := e.(type) {
	case *planner.ColumnRef:
		return x.Name
	case *planner.OuterColumnRef:
		return x.Name
	case *planner.IntegerConst:
		return fmt.Sprintf("%d", x.Value)
	case *planner.NumericConst:
		return x.Value
	case *planner.StringConst:
		// PG renders string literals as single-quoted; escape
		// embedded single quotes per SQL convention.
		return "'" + strings.ReplaceAll(x.Value, "'", "''") + "'"
	case *planner.BooleanConst:
		if x.Value {
			return "true"
		}
		return "false"
	case *planner.NullConst:
		return "NULL"
	case *planner.BinaryOp:
		return "(" + formatExprPG(x.Left) + " " + x.Op.String() + " " + formatExprPG(x.Right) + ")"
	case *planner.UnaryOp:
		return "(" + x.Op.String() + " " + formatExprPG(x.Operand) + ")"
	case *planner.CastExpr:
		return formatExprPG(x.Operand)
	case *planner.FuncCall:
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = formatExprPG(a)
		}
		return x.Name + "(" + strings.Join(args, ", ") + ")"
	case *planner.ParamRef:
		return fmt.Sprintf("$%d", x.Number)
	}
	return fmt.Sprintf("<%T>", e)
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
	walkPlanAnalyzeFiltered(n, depth, rows, opts, stats, nil)
}

func walkPlanAnalyzeFiltered(n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions, stats nodeStatsTable, attachedFilter planner.Expr) {
	if p, ok := n.(*planner.Project); ok {
		walkPlanAnalyzeFiltered(p.Child, depth, rows, opts, stats, attachedFilter)
		return
	}
	if f, ok := n.(*planner.Filter); ok {
		next := f.Predicate
		if attachedFilter != nil {
			next = attachedFilter
		}
		walkPlanAnalyzeFiltered(f.Child, depth, rows, opts, stats, next)
		return
	}

	indent := strings.Repeat("  ", depth)
	prefix := indent
	if depth > 0 {
		prefix = indent + "->  "
	}
	label := prefix + describePlanVerbose(n, opts.Verbose)
	showCostsA := !opts.Set.Costs || opts.Costs
	if showCostsA {
		est := planner.EstimateRows(n)
		if est <= 0 {
			est = 1
		}
		label += fmt.Sprintf("  (cost=0.00..0.00 rows=%d width=0)", est)
	}
	if s, ok := stats[n]; ok && s != nil {
		if s.timing {
			// PG formats rows as float (e.g. "rows=5.00") for ANALYZE output.
			label += fmt.Sprintf(" (actual time=%.3f..%.3f rows=%.2f loops=%d)",
				nsToMs(s.startupNs), nsToMs(s.totalNs), float64(s.rowsOut), s.loops)
		} else {
			label += fmt.Sprintf(" (actual rows=%.2f loops=%d)", float64(s.rowsOut), s.loops)
		}
	}
	*rows = append(*rows, Row{NewStringDatum(label)})

	detailIndent := strings.Repeat(" ", len(prefix)+2)
	emitNodeDetailLines(n, detailIndent, rows, attachedFilter)

	// IndexOnlyScan emits a "Heap Fetches: N" detail line under ANALYZE,
	// matching upstream's text format (design 0118-0102).
	if _, isIOS := n.(*planner.IndexOnlyScan); isIOS {
		if s, ok := stats[n]; ok && s != nil {
			*rows = append(*rows, Row{NewStringDatum(detailIndent + fmt.Sprintf("Heap Fetches: %d", s.heapFetches))})
		}
	}

	// EXPLAIN (ANALYZE, BUFFERS) emits a "Buffers: shared hit=N read=N"
	// detail line per node (design 0122-0003 BUFFERS slice). Shared-only,
	// hit/read-only for now — local/temp buffers and dirtied/written
	// counts are a deferred follow-up (ledger).
	if opts.Buffers {
		if s, ok := stats[n]; ok && s != nil {
			if line := formatBuffersLine(s); line != "" {
				*rows = append(*rows, Row{NewStringDatum(detailIndent + line)})
			}
			if line := formatIOTimingsLine(s); line != "" {
				*rows = append(*rows, Row{NewStringDatum(detailIndent + line)})
			}
		}
	}

	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			outline := indent + "  Output: " + strings.Join(cols, ", ")
			*rows = append(*rows, Row{NewStringDatum(outline)})
		}
	}

	for _, c := range planChildren(n) {
		walkPlanAnalyzeFiltered(c, depth+1, rows, opts, stats, nil)
	}
}

// planToJSONWithStats is the ANALYZE variant of planToJSON: each
// node object grows Actual Rows / Actual Loops / Actual Startup
// Time / Actual Total Time fields keyed by the instrumented
// node's identity. Mirrors upstream's JSON ANALYZE shape.
//
// trackIOTiming is the caller's track_io_timing GUC snapshot (see the
// "Shared I/O Read/Write Time" gating below) and is threaded unchanged
// through the recursive Plans re-render.
func planToJSONWithStats(n planner.Node, opts parser.ExplainOptions, stats nodeStatsTable, trackIOTiming bool) map[string]any {
	obj := planToJSON(n, opts)
	if s, ok := stats[n]; ok && s != nil {
		obj["Actual Rows"] = s.rowsOut
		obj["Actual Loops"] = s.loops
		if s.timing {
			obj["Actual Startup Time"] = nsToMs(s.startupNs)
			obj["Actual Total Time"] = nsToMs(s.totalNs)
		}
		// "Heap Fetches" is an IndexOnlyScan-only field upstream emits under
		// ANALYZE (design 0118-0102). horizons.spec reads it via
		// ...->'Plan'->'Heap Fetches'.
		if _, isIOS := n.(*planner.IndexOnlyScan); isIOS {
			obj["Heap Fetches"] = s.heapFetches
		}
		// EXPLAIN (ANALYZE, BUFFERS, FORMAT {JSON,XML,YAML}): unlike TEXT's
		// "Buffers:" line (formatBuffersLine, only printed when non-zero),
		// upstream's non-text show_buffer_usage() prints these properties
		// unconditionally once BUFFERS is requested, even when zero
		// (explain.c's peek_buffer_usage: "when format is anything other
		// than text, we print even if the counters are all zeroes"). Only
		// the shared hit/read/dirtied/written counters goopg actually
		// tracks are emitted here; local/temp/I-O-timing remain deferred
		// (ledger, M0122-0003).
		if opts.Buffers {
			obj["Shared Hit Blocks"] = s.bufHit
			obj["Shared Read Blocks"] = s.bufRead
			obj["Shared Dirtied Blocks"] = s.bufDirtied
			obj["Shared Written Blocks"] = s.bufWritten
			// "Shared I/O Read/Write Time" (upstream's show_buffer_usage
			// non-text branch, ExplainPropertyFloat calls gated on the live
			// track_io_timing GUC — emitted even when the accumulated value
			// is zero, matching explain.c's peek_buffer_usage comment: "when
			// format is anything other than text, we print even if the
			// counters are all zeroes"). TEXT's formatIOTimingsLine keeps its
			// own nonzero gate (a line with nothing to report is simply
			// omitted; there's no upstream text-format precedent for an
			// explicit "I/O Timings: " line at all-zero).
			if trackIOTiming {
				obj["Shared I/O Read Time"] = nsToMs(s.bufReadTimeNs)
				obj["Shared I/O Write Time"] = nsToMs(s.bufWriteTimeNs)
			}
		}
	}
	// Re-render Plans recursively with stats, replacing the
	// stats-free children that planToJSON installed.
	children := planChildren(n)
	if len(children) > 0 {
		plans := make([]map[string]any, 0, len(children))
		for _, c := range children {
			plans = append(plans, planToJSONWithStats(c, opts, stats, trackIOTiming))
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
// schemaQualify prepends "public." to an unqualified table name for VERBOSE mode.
func schemaQualify(name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	return "public." + name
}

// describePlanVerbose returns the plan-node description; verbose=true adds schema qualification.
func describePlanVerbose(n planner.Node, verbose bool) string {
	if !verbose {
		return describePlan(n)
	}
	switch p := n.(type) {
	case *planner.SeqScan:
		if p.Table == nil {
			return describePlan(n)
		}
		tname := schemaQualify(p.Table.QualifiedName())
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return fmt.Sprintf("Seq Scan on %s %s", tname, p.Alias)
		}
		return "Seq Scan on " + tname
	case *planner.IndexScan:
		return fmt.Sprintf("Index Scan using %s on %s", p.Index.QualifiedName(), schemaQualify(p.Table.QualifiedName()))
	case *planner.IndexOnlyScan:
		return fmt.Sprintf("Index Only Scan using %s on %s", p.Index.QualifiedName(), schemaQualify(p.Table.QualifiedName()))
	case *planner.Insert:
		return "Insert on " + schemaQualify(p.Table.QualifiedName())
	case *planner.Update:
		return "Update on " + schemaQualify(p.Table.QualifiedName())
	case *planner.Delete:
		return "Delete on " + schemaQualify(p.Table.QualifiedName())
	}
	return describePlan(n)
}

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
	case *planner.Distinct:
		return "Unique"
	case *planner.Aggregate:
		if len(p.GroupExprs) == 0 {
			return "Aggregate"
		}
		return fmt.Sprintf("GroupAggregate (%d keys)", len(p.GroupExprs))
	case *planner.WindowAgg:
		return fmt.Sprintf("WindowAgg (%d funcs)", len(p.Funcs))
	case *planner.SeqScan:
		// `(stats)` flags scans whose Table.Stats has been
		// populated by ANALYZE — the planner's cost-driven
		// decisions (Filter selectivity from MCV / histogram,
		// INNER-join algorithm choice) are only active for
		// these. M0006 / 0006-0004 surfaces this so an operator
		// inspecting EXPLAIN can verify which scans feed the
		// cost model.
		if p.Table != nil && p.Table.Stats != nil {
			if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
				return fmt.Sprintf("Seq Scan on %s %s (stats)", p.Table.QualifiedName(), p.Alias)
			}
			return fmt.Sprintf("Seq Scan on %s (stats)", p.Table.QualifiedName())
		}
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return fmt.Sprintf("Seq Scan on %s %s", p.Table.QualifiedName(), p.Alias)
		}
		return fmt.Sprintf("Seq Scan on %s", p.Table.QualifiedName())
	case *planner.IndexScan:
		return fmt.Sprintf("Index Scan using %s on %s", p.Index.QualifiedName(), p.Table.QualifiedName())
	case *planner.IndexOnlyScan:
		// M0118-0009 (design 0118-0102): horizons.spec inspects the IOS
		// label via `EXPLAIN (COSTS OFF)` (pruner_query_plan) — mirror
		// upstream's "Index Only Scan using <idx> on <table>".
		return fmt.Sprintf("Index Only Scan using %s on %s", p.Index.QualifiedName(), p.Table.QualifiedName())
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
	case *planner.LockRows:
		// Mirrors upstream's "LockRows" label; per-relation
		// detail is too verbose for the single-line label and
		// is left to a future VERBOSE-only extension.
		return "LockRows"
	case *planner.MultiHashJoin:
		// M0054-0003b: render the M0038 multi-way hash join
		// explicitly so EXPLAIN shows the join shape instead of
		// the Go type name "*planner.MultiHashJoin".
		return fmt.Sprintf("Multi-Way Hash Join (%d tables)", len(p.Tables))
	case *planner.NestedLoopIndexJoin:
		// M0054-0006: render `Nested Loop` matching upstream's
		// EXPLAIN output for a nested-loop join with an inner
		// IndexScan side. The inner IndexScan node renders its
		// own label (`Index Scan using <idx> on <table>`) below.
		return fmt.Sprintf("Nested Loop (%s)", joinTypeName(p.Type))
	case *planner.Merge:
		return fmt.Sprintf("Merge on %s", p.Target.QualifiedName())
	case *planner.CTEDMLPrefix:
		return "CTE DML"
	case *planner.MaterializedCTEScan:
		return fmt.Sprintf("CTE %s", p.Name)
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
	case *planner.Distinct:
		return []planner.Node{p.Child}
	case *planner.Aggregate:
		return []planner.Node{p.Child}
	case *planner.WindowAgg:
		return []planner.Node{p.Child}
	case *planner.Join:
		return []planner.Node{p.Left, p.Right}
	case *planner.Insert:
		return []planner.Node{p.Source}
	case *planner.CTEScan:
		return []planner.Node{p.Child}
	case *planner.LockRows:
		return []planner.Node{p.Child}
	case *planner.MultiHashJoin:
		// M0054-0003b: walk every input table of the multi-way
		// hash join. Without this, EXPLAIN truncates the plan
		// tree at the MultiHashJoin label and the underlying
		// SeqScan / IndexScan nodes are invisible — that was
		// the root cause of Q2/Q3/Q5/Q7/Q10/Q11/Q18/Q21
		// reporting "No scan nodes" in the M0054-0002 baseline.
		out := make([]planner.Node, len(p.Tables))
		copy(out, p.Tables)
		return out
	case *planner.NestedLoopIndexJoin:
		// M0054-0006: render outer driver and inner index probe.
		return []planner.Node{p.Outer, p.Inner}
	case *planner.Merge:
		return []planner.Node{p.Source}
	case *planner.CTEDMLPrefix:
		out := make([]planner.Node, 0, len(p.DMls)+1)
		out = append(out, p.DMls...)
		out = append(out, p.Body)
		return out
	}
	return nil
}
