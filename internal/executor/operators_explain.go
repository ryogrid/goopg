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

	// GENERIC_PLAN: PG 16+ option that forces EXPLAIN to show the
	// generic plan. Since goopg has no plan cache, emit a notice and
	// fall through to the custom plan (matching PG's behavior when
	// no generic plan is cached).
	if opts.GenericPlan && opts.Set.GenericPlan {
		ctx.AddNotice("generic plan is not available for this statement; using custom plan")
	}

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
			if opts.Buffers {
				root["Planning"] = planningBufferUsageJSON(trackIOTiming)
			}
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
		walkPlanAnalyze(&b, o.plan.Child, 0, &o.rows, opts, stats, ctx.SubPlanStats, ctx.MemoizeStats, ctx.HashJoinStats)
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
		if opts.Buffers {
			trackIOTiming := ctx.Activity != nil && ctx.Activity.TrackIOTiming(ctx.ProcNum)
			root["Planning"] = planningBufferUsageJSON(trackIOTiming)
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
// formatHashJoinInfoLine renders PG's hash-table line verbatim from
// show_hash_info (explain.c): the two forms differ only in whether the
// originals are shown, and PG shows BOTH originals as soon as EITHER count
// moved. `nbatch > 0` is upstream's whole gate — a join whose build never ran
// (no batch state, so no geometry was chosen) prints nothing at all, which is
// also how goopg represents "this hash join declined batching".
//
// kB rounds UP, PG's BYTES_TO_KILOBYTES.
func formatHashJoinInfoLine(hs *HashJoinStats) string {
	if hs == nil {
		return ""
	}
	if hs.NBatch <= 0 && hs.BuildTimeNs <= 0 {
		return ""
	}
	var parts []string
	if hs.NBatch > 0 {
		kb := (hs.SpacePeak + 1023) / 1024
		if hs.NBatch != hs.OrigNBatch || hs.NBuckets != hs.OrigNBuckets {
			parts = append(parts, fmt.Sprintf("Buckets: %d (originally %d)  Batches: %d (originally %d)  Memory Usage: %dkB",
				hs.NBuckets, hs.OrigNBuckets, hs.NBatch, hs.OrigNBatch, kb))
		} else {
			parts = append(parts, fmt.Sprintf("Buckets: %d  Batches: %d  Memory Usage: %dkB",
				hs.NBuckets, hs.NBatch, kb))
		}
	}
	if hs.BuildTimeNs > 0 {
		parts = append(parts, fmt.Sprintf("Build Time: %.3f ms", float64(hs.BuildTimeNs)/1e6))
	}
	return strings.Join(parts, "  ")
}

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

// planningBufferUsageJSON builds the non-TEXT-format "Planning" group
// upstream's ExplainOnePlan always emits once BUFFERS is requested,
// independent of ANALYZE (explain.c's peek_buffer_usage: for any
// non-EXPLAIN_FORMAT_TEXT format it returns true whenever `bufusage` is
// non-NULL, i.e. whenever es->buffers is set, even when every counter is
// zero — unlike TEXT's show_buffer_usage, which suppresses the whole
// "Planning:\n  Buffers: ..." block when nothing was touched).
//
// Upstream's BufferUsage here reflects I/O the *planner* itself performed
// (catalog/relcache/statistics lookups during pg_plan_query). goopg's
// planner (internal/planner) resolves everything against the in-memory
// catalog and never calls into storage.Pool, so these counters are always
// zero — that's a real architectural fact, not a stub: there is currently
// no planning-phase code path that could produce a nonzero value here.
//
// The Local/Temp terms are likewise always zero: goopg has no
// local-buffer-manager or temp-buffer concept at all (every relation,
// including temp tables, goes through the one shared storage.Pool), so
// there is no counter to accumulate into in the first place — a real
// architectural fact mirroring the Shared comment above, not a narrower
// stub than the Shared fields.
func planningBufferUsageJSON(trackIOTiming bool) map[string]any {
	m := map[string]any{
		"Shared Hit Blocks":     int64(0),
		"Shared Read Blocks":    int64(0),
		"Shared Dirtied Blocks": int64(0),
		"Shared Written Blocks": int64(0),
		"Local Hit Blocks":      int64(0),
		"Local Read Blocks":     int64(0),
		"Local Dirtied Blocks":  int64(0),
		"Local Written Blocks":  int64(0),
		"Temp Read Blocks":      int64(0),
		"Temp Written Blocks":   int64(0),
	}
	// Mirrors show_buffer_usage's non-text branch: once track_io_timing
	// is on, all six *_blk_read/write_time properties are emitted even
	// when zero (peek_buffer_usage: "we print even if the counters are
	// all zeroes"). goopg's planner never touches the buffer pool during
	// cost estimation, so these are architecturally-zero constants, not
	// a narrower stub (same rationale as the Blocks fields above).
	if trackIOTiming {
		m["Shared I/O Read Time"] = float64(0)
		m["Shared I/O Write Time"] = float64(0)
		m["Local I/O Read Time"] = float64(0)
		m["Local I/O Write Time"] = float64(0)
		m["Temp I/O Read Time"] = float64(0)
		m["Temp I/O Write Time"] = float64(0)
	}
	return m
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

// formatWalLine renders the upstream "WAL: records=N bytes=K" text
// (show_wal_usage in postgres/src/backend/commands/explain.c),
// omitting the whole line when both counters are zero and each individual
// records=/bytes= term when that counter is zero. M0122-0003.
func formatWalLine(s *nodeStats) string {
	if s.walRecords == 0 && s.walBytes == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if s.walRecords > 0 {
		parts = append(parts, fmt.Sprintf("records=%d", s.walRecords))
	}
	if s.walBytes > 0 {
		parts = append(parts, fmt.Sprintf("bytes=%d", s.walBytes))
	}
	return "WAL: " + strings.Join(parts, " ")
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
	walkPlanFiltered(n, depth, rows, opts, nil, nil, &subPlanReg{rel: newExplainNames(n), cte: collectCTEHoist(n)})
}

// walkPlanFiltered is the inner driver for walkPlan. attachedFilter
// is a Filter.Predicate carried down from a Filter wrapper that
// was skipped above us — it is rendered as `Filter:` detail under
// the next scan-like node we render. attachedFilterNode is that same
// wrapper's plan node, carried so the collapsed line can report the
// wrapper's POST-qual row estimate (see below).
func walkPlanFiltered(n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions, attachedFilter planner.Expr, attachedFilterNode planner.Node, reg *subPlanReg) {
	// Skip Project wrappers: PG has no "Projection" plan node;
	// the projection is part of the parent / scan's render.
	if p, ok := n.(*planner.Project); ok {
		walkPlanFiltered(p.Child, depth, rows, opts, attachedFilter, attachedFilterNode, reg)
		return
	}
	// Skip Filter wrappers and push their predicate down to be
	// rendered as `Filter:` detail under the next scan node.
	if f, ok := n.(*planner.Filter); ok {
		next := f.Predicate
		nextNode := planner.Node(f)
		// If multiple Filter wrappers stack, render only the
		// outermost predicate to keep the detail line readable.
		// Inner Filter predicates collapse with the outer via
		// short-circuit AND — but PG's Filter detail is a single
		// expression line; chaining is uncommon so prefer the
		// outermost predicate for v0. The outermost wrapper is also
		// the right one to take rows from: its estimate already
		// scales through every inner wrapper below it.
		if attachedFilter != nil {
			next = attachedFilter
			nextNode = attachedFilterNode
		}
		walkPlanFiltered(f.Child, depth, rows, opts, next, nextNode, reg)
		return
	}

	indent := strings.Repeat("  ", depth)
	prefix := indent
	if depth > 0 {
		prefix = indent + "->  "
	}
	label := prefix + describePlanVerbose(n, opts.Verbose, reg.names())
	// COSTS defaults to ON in PostgreSQL (and goopg); only suppress when
	// the user explicitly wrote COSTS OFF (Set.Costs=true and Costs=false).
	showCosts := !opts.Set.Costs || opts.Costs
	if showCosts {
		// The row count belongs to the qual that is rendered ON this line.
		// When a `*Filter` wrapper was collapsed into us above, its
		// predicate prints here as `Filter:`, so its POST-qual estimate is
		// what this line must report — `EstimateRows(n)` is the child's
		// PRE-qual count and understates nothing, it overstates by exactly
		// the filter's selectivity.
		//
		// Upstream never has this gap because the qual and the rowcount
		// live on one struct: `set_baserel_size_estimates` (costsize.c)
		// stores `rel->rows` already multiplied by
		// `clauselist_selectivity(baserestrictinfo)`, and `cost_agg`
		// likewise sets `path->rows` only after scaling `output_tuples` by
		// the HAVING quals' selectivity. goopg splits the two across a
		// wrapper node, so the renderer has to put them back together.
		//
		// goopg's ESTIMATOR was always right here — a parent node reads
		// `EstimateRows(*Filter)` and sees the filtered count. Only the
		// collapsed line lied, which made EXPLAIN (the acceptance
		// instrument for the DS05 `plans` channel and estimate-audit)
		// disagree with PG by one selectivity factor on every filtered
		// scan and every HAVING.
		rowSrc := n
		if attachedFilterNode != nil {
			rowSrc = attachedFilterNode
		}
		est := planner.EstimateRows(rowSrc)
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
	emitNodeDetailLines(n, detailIndent, rows, attachedFilter, reg)

	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			outline := indent + "  Output: " + strings.Join(cols, ", ")
			*rows = append(*rows, Row{NewStringDatum(outline)})
		}
	}

	// The `CTE <name>` sections belong to the top plan node, before its
	// InitPlans and children — upstream prints them from the top node's
	// subplan list (M0125-0049; explain_cte.go carries the shape).
	emitCTESections(rows, depth, reg, func(body planner.Node, bodyDepth int) {
		walkPlanFiltered(body, bodyDepth, rows, opts, nil, nil, reg)
	})

	// Sublinks referenced by this node's detail lines print their
	// inner plan as an indented `SubPlan N` subtree, as upstream's
	// ExplainSubPlans does. n becomes the ancestor plan node for the
	// duration (upstream's push_ancestor_plan): a correlated reference
	// inside the sublink is deparsed against THIS node's namespace, not
	// the relation the sublink itself scans.
	prevAncestor := reg.ancestor
	reg.ancestor = n
	emitSubPlanSubtrees(rows, detailIndent, opts, reg, nil, func(sub planner.Node, subDepth int) {
		walkPlanFiltered(sub, subDepth, rows, opts, nil, nil, reg)
	})
	reg.ancestor = prevAncestor

	for _, c := range renderChildren(n, reg.cte) {
		walkPlanFiltered(c, depth+1, rows, opts, nil, nil, reg)
	}
}

// emitSubPlanSubtrees drains the sublinks assigned while rendering
// the current node and prints each as
//
//	SubPlan N
//	  ->  <inner plan tree>
//
// render walks one sublink's inner plan at the given depth. The
// queue is drained in a loop because a sublink's own plan can
// reference further sublinks, which assign higher numbers as they
// are rendered (matching upstream's nested-SubPlan output).
// spStats, when non-nil (the ANALYZE path), appends this sublink's
// measured execution counters to its `SubPlan N` line.
func emitSubPlanSubtrees(rows *[]Row, detailIndent string, opts parser.ExplainOptions, reg *subPlanReg, spStats map[planner.Expr]*SubPlanSiteStats, render func(planner.Node, int)) {
	for {
		pending := reg.takePending()
		if len(pending) == 0 {
			return
		}
		for _, sp := range pending {
			line := detailIndent + fmt.Sprintf("SubPlan %d", sp.n)
			if s := spStats[sp.expr]; s != nil {
				line += fmt.Sprintf(" (calls=%d rebuilds=%d rescans=%d hits=%d misses=%d)",
					s.Calls, s.Rebuilds, s.Rescans, s.CacheHits, s.CacheMisses)
			}
			*rows = append(*rows, Row{NewStringDatum(line)})
			if sp.plan != nil {
				// A sublink body brings its own range-table entries
				// (Q30's `ctr2` lives only inside SubPlan 1), and they
				// must be named before any of its detail lines render.
				// Registering here rather than in one root-level pass
				// keeps the walk to the tree planChildren exposes —
				// sublink plans hang off expressions, not off it.
				reg.names().collect(sp.plan)
				// Indent the sublink's plan one "->" level under the
				// `SubPlan N` line, matching upstream's shape. A node
				// rendered at depth d starts its "->" at 2*d columns,
				// so the depth that lands on detailIndent+2 is
				// (len(detailIndent)+2)/2. Deriving it from the indent
				// rather than from depth keeps the root node right:
				// depth 0 has no "->  " prefix, so its detailIndent is
				// 4 columns narrower than every deeper node's.
				render(sp.plan, (len(detailIndent)+2)/2)
			}
		}
	}
}

// emitNodeDetailLines writes the PG-style detail lines that
// belong under n (Sort Key / Index Cond / Filter). attachedFilter
// is a Filter.Predicate from a Filter wrapper above n that was
// skipped — it surfaces as `Filter:` when n is a scan-like node.
func emitNodeDetailLines(n planner.Node, indent string, rows *[]Row, attachedFilter planner.Expr, reg *subPlanReg) {
	// M0125-0039: whether this node's detail lines print qualified column
	// references. Upstream splits the decision by node kind — show_scan_qual
	// deparses a scan's `Filter:`/`Index Cond:` with varprefix=false, while
	// show_upper_qual and show_sort_group_keys use
	// `es->rtable_size > 1`. Reproducing that split is what keeps a
	// single-table plan's output byte-identical while a join's
	// `Filter: (cd_marital_status <> cd_marital_status)` becomes the
	// readable `(cd1.cd_marital_status <> cd2.cd_marital_status)`.
	// (VERBOSE does not force prefixing here yet — see the deferral row.)
	qualify := reg.names().qualify() && !explainIsScanNode(n)
	switch p := n.(type) {
	case *planner.Gather:
		// PG emits `Workers Planned:` in PLAIN EXPLAIN — it is a plan-time
		// property. `Workers Launched:` is execution-time and belongs to the
		// ANALYZE walk instead.
		*rows = append(*rows, Row{NewStringDatum(
			indent + fmt.Sprintf("Workers Planned: %d", p.WorkersPlanned))})
	case *planner.GatherMerge:
		// PG prints Workers Planned for Gather Merge and, unlike Sort, does NOT
		// print the merge keys (explain.c has no show_sort_keys call in the
		// T_GatherMerge arm) — the keys are the child Sort's, and it prints
		// them itself one line below.
		*rows = append(*rows, Row{NewStringDatum(
			indent + fmt.Sprintf("Workers Planned: %d", p.WorkersPlanned))})
	case *planner.Sort:
		if len(p.Keys) > 0 {
			parts := make([]string, 0, len(p.Keys))
			for _, k := range p.Keys {
				s := formatExprQual(k.Expr, reg, qualify)
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
		if cond := formatIndexCond(p, reg); cond != "" {
			*rows = append(*rows, Row{NewStringDatum(indent + "Index Cond: " + cond)})
		}
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	case *planner.Join:
		// M0127-P2.1: render the join's equi-key list, upstream's
		// `Hash Cond:` / `Merge Cond:`. explain.c reaches these through
		// show_upper_qual (T_HashJoin → hashclauses, T_MergeJoin →
		// mergeclauses), which is why `qualify` — the rtable_size > 1
		// rule — is the right prefixing decision here and not the
		// scan-node one.
		//
		// goopg emitted NO condition line for joins at all before this,
		// so a plan's key choice was invisible in EXPLAIN — the very
		// property M0125-0035b's degeneracy bug turned on (a
		// constant-pinned key column produced a PG-identical-looking
		// plan that ran quadratically). The list now shows every pair
		// the planner found, so `Hash Cond: ((a = b) AND (c = d))` is
		// readable against PG's own output for the same query.
		if cond := formatJoinKeyCond(p, reg, qualify); cond != "" {
			label := "Hash Cond: "
			if p.Algo == planner.JoinAlgoMerge {
				label = "Merge Cond: "
			}
			*rows = append(*rows, Row{NewStringDatum(indent + label + cond)})
		}
		// M0127-P5.9-o: the join's RESIDUAL qual — upstream's
		// `join.joinqual`, printed as `Join Filter:` immediately after the
		// Cond line and before the node's own `Filter:`
		// (explain.c:ExplainNode, T_NestLoop / T_MergeJoin / T_HashJoin all
		// call show_upper_qual(joinqual, "Join Filter", …) in that slot).
		//
		// Before this, `… ON a.id = b.id AND a.st < b.st` printed its second
		// conjunct NOWHERE: the hash key showed up as `Hash Cond:` and the
		// conjunct the executor re-checks per match was invisible, so a plan
		// could not be read against PG's output for the same query — the
		// same class of blind spot `Hash Cond:` itself closed at P2.1.
		if jf := formatJoinFilter(p, reg, qualify); jf != "" {
			*rows = append(*rows, Row{NewStringDatum(indent + "Join Filter: " + jf)})
		}
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	case *planner.NestedLoopIndexJoin:
		// The NLI's residual predicate (conjuncts the index probe does not
		// enforce — hoisted inner filters, OR-factoring residuals,
		// decorrelated-EXISTS residuals like Q4's l_commitdate <
		// l_receiptdate) was previously invisible in EXPLAIN, which hid a
		// mis-resolution during the Q7 alias/residual fix (deferral ledger,
		// csq-S6). Render it as a Filter: line, house style.
		if p.Predicate != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(p.Predicate, reg, qualify)))})
		}
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	case *planner.Memoize:
		// Upstream shape: `Cache Key: t.a, t.b` under the Memoize node.
		if len(p.KeyExprs) > 0 {
			parts := make([]string, 0, len(p.KeyExprs))
			for _, ke := range p.KeyExprs {
				parts = append(parts, formatExprQual(ke, reg, qualify))
			}
			*rows = append(*rows, Row{NewStringDatum(indent + "Cache Key: " + strings.Join(parts, ", "))})
		}
	case *planner.SeqScan:
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	default:
		// Non-scan nodes keep an attached Filter alive — render it
		// here so the predicate is not silently dropped when our
		// planner places a Filter above a non-scan (e.g. an
		// Aggregate). Matches PG's behaviour of rendering Filter on
		// the node it most directly applies to.
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	}
}

// formatJoinKeyCond renders a hash/merge join's key list the way
// upstream's show_qual does: each pair as its own parenthesised
// equality, and — when there is more than one — the whole list
// re-parenthesised as an explicit AND chain (make_ands_explicit).
// A single pair therefore reads `(a.x = b.x)` and two read
// `((a.x = b.x) AND (a.y = b.y))`, matching PG byte for byte.
//
// Falls back to the single (LeftKey, RightKey) pair when HashKeys is
// empty — a join built outside Plan()'s tail never gets the list, and an
// unrendered condition would be a silent regression against the pre-P2.1
// output for exactly those plans.
func formatJoinKeyCond(p *planner.Join, reg *subPlanReg, qualify bool) string {
	if p.Algo != planner.JoinAlgoHash && p.Algo != planner.JoinAlgoMerge {
		return ""
	}
	pairs := p.HashKeys
	if len(pairs) == 0 {
		if p.LeftKey == nil || p.RightKey == nil {
			return ""
		}
		pairs = []planner.JoinKeyPair{{Left: p.LeftKey, Right: p.RightKey}}
	}
	parts := make([]string, 0, len(pairs))
	for _, k := range pairs {
		if k.Left == nil || k.Right == nil {
			continue
		}
		parts = append(parts, formatExprQual(
			&planner.BinaryOp{Op: parser.OpEq, Left: k.Left, Right: k.Right}, reg, qualify))
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// formatJoinFilter renders the conjuncts of a join's Predicate that the
// Cond line above it does NOT already account for — upstream's
// `join.joinqual`, which `create_hashjoin_plan` builds as
// `list_difference(joinclauses, hashclauses)` (createplan.c) and
// `ExplainNode` prints as `Join Filter:`.
//
// The split is asked of the planner rather than recomputed here, and from the
// SAME method the executor uses to decide what it re-checks per match
// (`ExecHashKeyPlan` / `ExecMergeKeyPlan`, join_exec_keys.go). That is the
// point: a residual EXPLAIN derived independently could disagree with the one
// the executor evaluates, which is precisely the invisibility this line
// exists to remove. Every conjunct therefore prints exactly once — as part of
// the Cond line if a key enforces it, as Join Filter otherwise.
//
// Nested loop has no key list, so its whole Predicate is the residual; that
// matches upstream, where a NestLoop's joinqual is the full set of join
// clauses the inner index scan did not consume.
func formatJoinFilter(p *planner.Join, reg *subPlanReg, qualify bool) string {
	if p == nil || p.Predicate == nil {
		return ""
	}
	residual := p.Predicate
	switch p.Algo {
	case planner.JoinAlgoHash:
		residual = p.ExecHashKeyPlan().Residual
	case planner.JoinAlgoMerge:
		residual = p.ExecMergeKeyPlan().Residual
	}
	if residual == nil {
		return ""
	}
	return wrapParen(formatExprQual(residual, reg, qualify))
}

// formatIndexCond renders the equality / range condition of an
// IndexScan node as a PG-style `(col = key)` (or range) expression.
// Empty when the scan has no bound (full-range probe).
func formatIndexCond(p *planner.IndexScan, reg *subPlanReg) string {
	if p == nil || p.Index == nil {
		return ""
	}
	cols := p.Index.Columns
	// Multi-column equality probe.
	if len(p.Keys) > 0 && len(cols) >= len(p.Keys) {
		if len(p.Keys) == 1 {
			return wrapParen(cols[0] + " = " + formatExprPGReg(p.Keys[0], reg))
		}
		parts := make([]string, len(p.Keys))
		for i, k := range p.Keys {
			parts[i] = cols[i] + " = " + formatExprPGReg(k, reg)
		}
		return wrapParen(strings.Join(parts, " AND "))
	}
	// Single-column equality.
	if p.Key != nil && len(cols) > 0 {
		return wrapParen(cols[0] + " = " + formatExprPGReg(p.Key, reg))
	}
	// Range scan.
	if (p.LowKey != nil || p.HighKey != nil) && len(cols) > 0 {
		col := cols[0]
		var parts []string
		if p.LowKey != nil {
			parts = append(parts, col+" >= "+formatExprPGReg(p.LowKey, reg))
		}
		if p.HighKey != nil {
			parts = append(parts, col+" <= "+formatExprPGReg(p.HighKey, reg))
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

// subPlanReg assigns PG-style "SubPlan N" numbers to the sublink
// expressions encountered while rendering one EXPLAIN result, and
// queues their inner plans so the owning node can print them as
// indented `SubPlan N` subtrees (upstream: ExplainSubPlans in
// postgres/src/backend/commands/explain.c).
//
// Numbers are allocated in render (pre-order) encounter order,
// which is deterministic for a given plan. goopg has no plan-time
// SubPlan node to carry upstream's `plan_id`, so the numbering is
// display-local; it becomes structural when param slots land
// (design 04-subplan-execution-engine.md, D4.1).
//
// A nil *subPlanReg is safe to use: assign reports 0 and queues
// nothing, so callers without a registry (JSON rendering, direct
// formatExprPG users) keep working unchanged.
type subPlanReg struct {
	num     map[planner.Expr]int
	pending []subPlanEntry
	// rel is the render's range-table name table (M0125-0039). It lives
	// here rather than in its own parameter because subPlanReg is already
	// the one piece of per-EXPLAIN state threaded through every walker and
	// every expression-render site, and the two have the same lifetime.
	// nil is safe: explainNames' methods are nil-receiver tolerant, so a
	// registry-less caller renders bare column names as before.
	rel *explainNames
	// ancestor is the plan node whose detail lines are currently being
	// rendered — upstream's push_ancestor_plan target, used to name a
	// correlated reference whose binding id was erased on the way up
	// (an aggregate zeroes SourceTableIdx). Set by the walkers around
	// each node's render; nil outside one.
	ancestor planner.Node
	// cte holds the CTE bodies lifted out of their reference sites for this
	// render, so a multiply-referenced CTE prints once as a `CTE <name>`
	// section instead of once per reference (M0125-0049). Shared with the
	// sublink walk, which is why it lives here: a reference inside a
	// `SubPlan N` subtree must render as a leaf too. nil when the plan has
	// no CTE, and nil-receiver tolerant either way.
	cte *cteHoist
}

// ancestorNode returns the plan node currently being rendered, or nil.
func (r *subPlanReg) ancestorNode() planner.Node {
	if r == nil {
		return nil
	}
	return r.ancestor
}

// names returns the render's range-table name table, or nil when the caller
// has no registry (formatExprPG's direct users). Both are safe to call
// methods on.
func (r *subPlanReg) names() *explainNames {
	if r == nil {
		return nil
	}
	return r.rel
}

// subPlanEntry is one assigned-but-not-yet-emitted sublink. expr
// is retained so ANALYZE can look the sublink's execution counters
// up in Context.SubPlanStats when rendering its `SubPlan N` line.
type subPlanEntry struct {
	n    int
	expr planner.Expr
	plan planner.Node
}

// assign returns the SubPlan number already given to e, or
// allocates the next one and queues e's inner plan for emission.
func (r *subPlanReg) assign(e planner.Expr, plan planner.Node) int {
	if r == nil {
		return 0
	}
	if n, ok := r.num[e]; ok {
		return n
	}
	if r.num == nil {
		r.num = make(map[planner.Expr]int)
	}
	n := len(r.num) + 1
	r.num[e] = n
	r.pending = append(r.pending, subPlanEntry{n: n, expr: e, plan: plan})
	return n
}

// takePending returns the sublinks assigned since the last call
// and clears the queue.
func (r *subPlanReg) takePending() []subPlanEntry {
	if r == nil || len(r.pending) == 0 {
		return nil
	}
	out := r.pending
	r.pending = nil
	return out
}

// subPlanName renders the `SubPlan N` token for e, matching
// upstream's SubPlan.plan_name. Without a registry the number is
// unknown, so the bare kind is printed instead of a wrong number.
func subPlanName(r *subPlanReg, e planner.Expr, plan planner.Node) string {
	if n := r.assign(e, plan); n > 0 {
		return fmt.Sprintf("SubPlan %d", n)
	}
	return "SubPlan"
}

// formatExprPG renders a planner expression in upstream PG's
// EXPLAIN style with no SubPlan registry — sublinks render as
// `SubPlan` without a number. Prefer formatExprPGReg from the
// EXPLAIN walkers so sublinks are numbered and their subtrees
// emitted.
func formatExprPG(e planner.Expr) string {
	return formatExprPGReg(e, nil)
}

// formatExprPGReg renders e with bare (unqualified) column names — the
// rendering every EXPLAIN detail line used before M0125-0039, and still the
// right one for a scan-node qual, which upstream deparses with
// varprefix=false (explain.c show_scan_qual). Call formatExprQual directly
// with qualify=true for the upper-node quals PG prefixes.
func formatExprPGReg(e planner.Expr, reg *subPlanReg) string {
	return formatExprQual(e, reg, false)
}

// formatExprPGReg renders a planner expression in upstream PG's
// EXPLAIN style: column names, integer/string/numeric literals,
// and infix operators. Sublinks (EXISTS / IN / scalar subquery)
// render as PG-style SubPlan references via reg. Falls back to a
// compact `<type>` token for expression kinds we don't yet render
// (sufficient for the isolation specs that pass through
// `EXPLAIN (COSTS OFF)`; the detail line is informational).
func formatExprQual(e planner.Expr, reg *subPlanReg, qualify bool) string {
	if e == nil {
		return ""
	}
	switch x := e.(type) {
	case *planner.ColumnRef:
		// qualify is upstream's deparse_context.varprefix
		// (ruleutils.c get_variable's need_prefix): a plain Var is
		// printed bare on a scan qual and qualified everywhere else
		// once the query has more than one range-table entry.
		return reg.names().column(x.SourceTableIdx, x.Name, qualify)
	case *planner.OuterColumnRef:
		// A correlated reference is always prefixed, even inside a
		// scan qual. Upstream reaches the same output through
		// get_parameter, which forces varprefix=true while deparsing
		// a Param's expansion "since they won't belong to the
		// relation being scanned in the original plan node" — which
		// is what makes PG print Q30's filter as
		// `(ctr1.ctr_state = ctr_state)` rather than the
		// self-comparison goopg used to print.
		if s := reg.names().column(x.SourceTableIdx, x.Name, true); s != x.Name {
			return s
		}
		// SourceTableIdx could not name it — the common case when the
		// correlation runs through an aggregate, which zeroes the
		// binding id. Fall back to upstream's own mechanism and
		// resolve the name against the ancestor plan node
		// (push_ancestor_plan).
		if rel := reg.names().resolveInAncestor(reg.ancestorNode(), x.Name); rel != "" {
			return rel + "." + x.Name
		}
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
		return "(" + formatExprQual(x.Left, reg, qualify) + " " + x.Op.String() + " " + formatExprQual(x.Right, reg, qualify) + ")"
	case *planner.UnaryOp:
		return "(" + x.Op.String() + " " + formatExprQual(x.Operand, reg, qualify) + ")"
	case *planner.CastExpr:
		return formatExprQual(x.Operand, reg, qualify)
	case *planner.FuncCall:
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = formatExprQual(a, reg, qualify)
		}
		return x.Name + "(" + strings.Join(args, ", ") + ")"
	case *planner.ParamRef:
		return fmt.Sprintf("$%d", x.Number)
	case *planner.ExecParamRef:
		// PARAM_EXEC slot (D4.1) — upstream prints exec params the
		// same `$N` way (e.g. `Index Cond: (l_orderkey = $0)`); the
		// number is the flat per-statement slot ID.
		return fmt.Sprintf("$%d", x.ID)
	case *planner.TypedStringLit:
		// Upstream renders a typed literal as `'value'::type`
		// (ruleutils.c get_const_expr with showtype).
		return "'" + strings.ReplaceAll(x.Value, "'", "''") + "'::" + x.Type
	case *planner.IntervalLit:
		// `interval 'N' <unit>` (Qualified) folds the unit into the
		// literal text so the rendering stays a single typed constant.
		lit := x.Value
		if x.Unit != "" {
			lit += " " + x.Unit
		}
		return "'" + strings.ReplaceAll(lit, "'", "''") + "'::interval"
	case *planner.ExistsExpr:
		// Upstream renders EXISTS sublinks as `EXISTS(SubPlan N)`
		// (ruleutils.c get_rule_expr, T_SubPlan / EXISTS_SUBLINK).
		s := "EXISTS(" + subPlanName(reg, x, x.Plan) + ")"
		if x.Negated {
			return "NOT " + s
		}
		return s
	case *planner.SubqueryExpr:
		// EXPR_SUBLINK: upstream decorates scalar subplan
		// references with nothing but parentheses.
		return "(" + subPlanName(reg, x, x.Plan) + ")"
	case *planner.ArraySubqueryExpr:
		// ARRAY_SUBLINK: upstream prints `ARRAY(<plan_name>)`.
		return "ARRAY(" + subPlanName(reg, x, x.Plan) + ")"
	case *planner.InExpr:
		return formatInExprPG(x, reg, qualify)
	case *planner.IsNullExpr:
		// ruleutils.c get_rule_expr, T_NullTest.
		if x.Negated {
			return "(" + formatExprQual(x.Operand, reg, qualify) + " IS NOT NULL)"
		}
		return "(" + formatExprQual(x.Operand, reg, qualify) + " IS NULL)"
	case *planner.IsBoolExpr:
		// T_BooleanTest. TestTrue/TestFalse both false means IS UNKNOWN
		// (see evalExprSlot's IsBoolExpr arm).
		what := "UNKNOWN"
		if x.TestTrue {
			what = "TRUE"
		} else if x.TestFalse {
			what = "FALSE"
		}
		not := ""
		if x.Negated {
			not = "NOT "
		}
		return "(" + formatExprQual(x.Operand, reg, qualify) + " IS " + not + what + ")"
	case *planner.IsDistinctFromExpr:
		op := " IS DISTINCT FROM "
		if x.Negated {
			op = " IS NOT DISTINCT FROM "
		}
		return "(" + formatExprQual(x.Left, reg, qualify) + op + formatExprQual(x.Right, reg, qualify) + ")"
	case *planner.CaseExpr:
		// T_CaseExpr. Simple form keeps the operand after CASE.
		var b strings.Builder
		b.WriteString("CASE")
		if x.Operand != nil {
			b.WriteString(" ")
			b.WriteString(formatExprQual(x.Operand, reg, qualify))
		}
		for _, w := range x.Whens {
			b.WriteString(" WHEN ")
			b.WriteString(formatExprQual(w.When, reg, qualify))
			b.WriteString(" THEN ")
			b.WriteString(formatExprQual(w.Then, reg, qualify))
		}
		if x.Else != nil {
			b.WriteString(" ELSE ")
			b.WriteString(formatExprQual(x.Else, reg, qualify))
		}
		b.WriteString(" END")
		return b.String()
	case *planner.ExtractExpr:
		return "EXTRACT(" + x.Field + " FROM " + formatExprQual(x.Source, reg, qualify) + ")"
	case *planner.CollateExpr:
		return "(" + formatExprQual(x.Operand, reg, qualify) + " COLLATE " + x.CollationName + ")"
	case *planner.RowExpr:
		elems := make([]string, len(x.Elems))
		for i, el := range x.Elems {
			elems[i] = formatExprQual(el, reg, qualify)
		}
		return "ROW(" + strings.Join(elems, ", ") + ")"
	case *planner.TableOidExpr:
		return "tableoid"
	case *planner.CTIDExpr:
		return "ctid"
	case *planner.MergeActionExpr:
		return "merge_action()"
	case *planner.MergeWholeRowRef:
		if x.IsOld {
			return "old"
		}
		return "new"
	case *planner.MultiAssignSubqRow:
		// Multi-assignment sublink (`SET (a,b) = (SELECT …)`); the row
		// itself is the subplan reference.
		return "(" + subPlanName(reg, x, x.Plan) + ")"
	case *planner.MultiAssignSubqElem:
		// One column of the multi-assignment row. PG renders these as
		// PARAM_EXEC slots; goopg has no slot number here, so name the
		// owning subplan and the 1-based column.
		return formatExprQual(x.Row, reg, qualify) + fmt.Sprintf(".%d", x.ColIdx+1)
	}
	return fmt.Sprintf("<%T>", e)
}

// formatInExprPG renders an InExpr — either a sublink form
// (`x = ANY (SubPlan N)`) or a literal in-list (`x = ANY (...)`).
//
// Divergence from upstream: PG renders an ANY sublink as
// `(ANY (<testexpr>))`, where the testexpr's PARAM_EXEC
// references (`$0`) stand in for the subplan's output. goopg has
// no param slots yet, so the operand and the SubPlan reference are
// rendered side by side instead; this converges on PG's form when
// D4.1 lands param slots.
func formatInExprPG(x *planner.InExpr, reg *subPlanReg, qualify bool) string {
	operand := formatExprQual(x.Operand, reg, qualify)

	// Comparison spelling: default `=` for IN, or the explicit
	// operator when the parser recorded one (`col < ALL (...)`).
	op := "="
	if x.AnyOp != 0 {
		op = x.AnyOp.String()
	} else if x.NotEqualAny {
		op = "<>"
	}
	// Quantifier: ALL for `op ALL(...)`, ANY otherwise. A plain
	// NOT IN is `NOT (x = ANY (...))`, matching PG's deparse.
	quant := "ANY"
	if x.AllOp {
		quant = "ALL"
	}

	var rhs string
	if x.Plan != nil {
		rhs = subPlanName(reg, x, x.Plan)
	} else {
		parts := make([]string, len(x.List))
		for i, v := range x.List {
			parts[i] = formatExprQual(v, reg, qualify)
		}
		rhs = strings.Join(parts, ", ")
	}

	s := "(" + operand + " " + op + " " + quant + " (" + rhs + "))"
	if x.Negated {
		return "(NOT " + s + ")"
	}
	return s
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
func walkPlanAnalyze(b *strings.Builder, n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions, stats nodeStatsTable, spStats map[planner.Expr]*SubPlanSiteStats, memoStats map[*planner.Memoize]*MemoizeStats, hashStats map[*planner.Join]*HashJoinStats) {
	walkPlanAnalyzeFiltered(n, depth, rows, opts, stats, spStats, memoStats, hashStats, nil, 0, &subPlanReg{rel: newExplainNames(n), cte: collectCTEHoist(n)})
}

func walkPlanAnalyzeFiltered(n planner.Node, depth int, rows *[]Row, opts parser.ExplainOptions, stats nodeStatsTable, spStats map[planner.Expr]*SubPlanSiteStats, memoStats map[*planner.Memoize]*MemoizeStats, hashStats map[*planner.Join]*HashJoinStats, attachedFilter planner.Expr, filterRowsRemoved int64, reg *subPlanReg) {
	if p, ok := n.(*planner.Project); ok {
		walkPlanAnalyzeFiltered(p.Child, depth, rows, opts, stats, spStats, memoStats, hashStats, attachedFilter, filterRowsRemoved, reg)
		return
	}
	if f, ok := n.(*planner.Filter); ok {
		next := f.Predicate
		if attachedFilter != nil {
			next = attachedFilter
		}
		// A Filter node wraps a scan/join to apply qual(s). Its own
		// instrumentation entry (when ANALYZE) carries the number of
		// rows it rejected. Accumulate that count (chained Filters
		// each reject a subset) and carry it down to the scan/join
		// node so it can render "Rows Removed by Filter".
		fr := filterRowsRemoved
		if fs, ok := stats[f]; ok && fs != nil {
			fr += fs.filterRejected
		}
		walkPlanAnalyzeFiltered(f.Child, depth, rows, opts, stats, spStats, memoStats, hashStats, next, fr, reg)
		return
	}

	indent := strings.Repeat("  ", depth)
	prefix := indent
	if depth > 0 {
		prefix = indent + "->  "
	}
	label := prefix + describePlanVerbose(n, opts.Verbose, reg.names())
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
	emitNodeDetailLines(n, detailIndent, rows, attachedFilter, reg)

	// IndexOnlyScan emits a "Heap Fetches: N" detail line under ANALYZE,
		// matching upstream's text format (design 0118-0102).
		if _, isIOS := n.(*planner.IndexOnlyScan); isIOS {
			if s, ok := stats[n]; ok && s != nil {
				*rows = append(*rows, Row{NewStringDatum(detailIndent + fmt.Sprintf("Heap Fetches: %d", s.heapFetches))})
			}
		}

		// M0128-P5.2: "Rows Removed by Filter" — a scan/join node whose
		// Filter wrapper (collapsed above) rejected tuples. The count is
		// carried down from the collapsed Filter plan node's
		// stats.filterRejected. Mirrors PG's show_instrumentation_count
		// (nfiltered1 for scan qual rejects, per-loop average). Zero
		// suppressed in text mode (PG convention).
		if filterRowsRemoved > 0 {
			if s, ok := stats[n]; ok && s != nil && s.loops > 0 {
				avg := float64(filterRowsRemoved) / float64(s.loops)
				*rows = append(*rows, Row{NewStringDatum(detailIndent + fmt.Sprintf("Rows Removed by Filter: %.0f", avg))})
			}
		}

		// M0128-P5.2: "Rows Removed by Join Filter" — a join node
		// rejected candidate matches on its residual qual. The count is
		// from the join node's own stats.joinFilterRejected. Mirrors
		// PG's show_instrumentation_count (nfiltered1 for joinqual
		// rejects, per-loop average). Zero suppressed in text mode.
		if s, ok := stats[n]; ok && s != nil && s.joinFilterRejected > 0 && s.loops > 0 {
			avg := float64(s.joinFilterRejected) / float64(s.loops)
			*rows = append(*rows, Row{NewStringDatum(detailIndent + fmt.Sprintf("Rows Removed by Join Filter: %.0f", avg))})
		}

		// Memoize emits its cache counters under ANALYZE, matching upstream's
	// show_memoize_info line shape (S7; Memory Usage is deferred — our
	// byte accounting is a budget approximation, not a report-grade
	// number).
	if m, isMemo := n.(*planner.Memoize); isMemo {
		if ms := memoStats[m]; ms != nil {
			*rows = append(*rows, Row{NewStringDatum(detailIndent + fmt.Sprintf(
				"Hits: %d  Misses: %d  Evictions: %d  Overflows: %d",
				ms.Hits, ms.Misses, ms.Evictions, ms.Overflows))})
		}
	}

	// A hash join emits PG's hash-table line under ANALYZE. Upstream hangs it
	// off the HASH node; goopg has no Hash node (the build lives inside
	// joinOp), so it hangs off the Hash Join. M0127-P3.5 / design 06 §4.
	if j, isJoin := n.(*planner.Join); isJoin && j.Algo == planner.JoinAlgoHash {
		if line := formatHashJoinInfoLine(hashStats[j]); line != "" {
			*rows = append(*rows, Row{NewStringDatum(detailIndent + line)})
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

	// EXPLAIN (ANALYZE, WAL) emits a "WAL: records=N bytes=K"
	// detail line per node (M0122-0003).
	if opts.Wal {
		if s, ok := stats[n]; ok && s != nil {
			if line := formatWalLine(s); line != "" {
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

	// The CTE sections print with their instrumentation intact: every
	// reference shares one body Node, so the section IS the subtree that
	// ran (the later references replay from ctx.CTERowCache). M0125-0049.
	emitCTESections(rows, depth, reg, func(body planner.Node, bodyDepth int) {
		walkPlanAnalyzeFiltered(body, bodyDepth, rows, opts, stats, spStats, memoStats, hashStats, nil, 0, reg)
	})

	// Sublink subtrees keep their instrumentation: stats is passed
	// through so inner nodes still report actual rows / loops.
	prevAncestor := reg.ancestor
	reg.ancestor = n
	emitSubPlanSubtrees(rows, detailIndent, opts, reg, spStats, func(sub planner.Node, subDepth int) {
		walkPlanAnalyzeFiltered(sub, subDepth, rows, opts, stats, spStats, memoStats, hashStats, nil, 0, reg)
	})
	reg.ancestor = prevAncestor

	for _, c := range renderChildren(n, reg.cte) {
		walkPlanAnalyzeFiltered(c, depth+1, rows, opts, stats, spStats, memoStats, hashStats, nil, 0, reg)
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
			// M0128-P5.2: "Rows Removed by Filter" and "Rows Removed by Join
			// Filter" per-loop averages, mirroring PG's non-text
			// show_instrumentation_count (emitted unconditionally in structured
			// formats regardless of zero, matching PG's EXPLAIN_FORMAT_TEXT gate).
			if s.loops > 0 {
				obj["Rows Removed by Filter"] = float64(s.filterRejected) / float64(s.loops)
				obj["Rows Removed by Join Filter"] = float64(s.joinFilterRejected) / float64(s.loops)
			}
			// EXPLAIN (ANALYZE, BUFFERS, FORMAT {JSON,XML,YAML}): unlike TEXT's
		// "Buffers:" line (formatBuffersLine, only printed when non-zero),
		// upstream's non-text show_buffer_usage() prints these properties
		// unconditionally once BUFFERS is requested, even when zero
		// (explain.c's peek_buffer_usage: "when format is anything other
		// than text, we print even if the counters are all zeroes"). The
		// shared hit/read/dirtied/written counters goopg actually tracks
		// are emitted from live nodeStats; Local/Temp Blocks are emitted
		// as constant zeros (mirrors planningBufferUsageJSON's own
		// Local/Temp comment — goopg has no local-buffer-manager or
		// temp-buffer concept, so there is no counter to read). Shared/
		// Local/Temp I/O timing remain deferred (ledger, M0122-0003).
		if opts.Buffers {
			obj["Shared Hit Blocks"] = s.bufHit
			obj["Shared Read Blocks"] = s.bufRead
			obj["Shared Dirtied Blocks"] = s.bufDirtied
			obj["Shared Written Blocks"] = s.bufWritten
			obj["Local Hit Blocks"] = int64(0)
			obj["Local Read Blocks"] = int64(0)
			obj["Local Dirtied Blocks"] = int64(0)
			obj["Local Written Blocks"] = int64(0)
			obj["Temp Read Blocks"] = int64(0)
			obj["Temp Written Blocks"] = int64(0)
			// "Shared/Local/Temp I/O Read/Write Time" (upstream's
			// show_buffer_usage non-text branch, ExplainPropertyFloat calls
			// gated on the live track_io_timing GUC — emitted even when the
			// accumulated value is zero, matching explain.c's
			// peek_buffer_usage comment: "when format is anything other
			// than text, we print even if the counters are all zeroes").
			// Shared is real (nsToMs of the live counters); Local/Temp are
			// constant zeros for the same reason the Blocks fields above
			// are — no local-buffer-manager/temp-buffer concept to time.
			// TEXT's formatIOTimingsLine keeps its own nonzero gate (a line
			// with nothing to report is simply omitted; there's no upstream
			// text-format precedent for an explicit "I/O Timings: " line at
			// all-zero).
			if trackIOTiming {
				obj["Shared I/O Read Time"] = nsToMs(s.bufReadTimeNs)
				obj["Shared I/O Write Time"] = nsToMs(s.bufWriteTimeNs)
				obj["Local I/O Read Time"] = float64(0)
				obj["Local I/O Write Time"] = float64(0)
				obj["Temp I/O Read Time"] = float64(0)
				obj["Temp I/O Write Time"] = float64(0)
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
		"Node Type": describePlan(n, nil),
	}
	// M0125-0037(i): upstream's non-text formats do NOT fold the set-op
	// command into the node name the way the text format does. Verified
	// against PG 18.3 `EXPLAIN (FORMAT JSON)` on an INTERSECT ALL:
	// "Node Type": "SetOp", "Strategy": "Hashed", "Command": "Intersect All"
	// (explain.c uses `sname` for the property and `pname` for the text
	// line). A UNION ALL has no SetOp node upstream at all, so it keeps
	// the plain "Append" describePlan gives it.
	if p, ok := n.(*planner.SetOp); ok && !setOpRendersAsAppend(p) {
		obj["Node Type"] = "SetOp"
		obj["Strategy"] = "Hashed"
		obj["Command"] = setOpCommandName(p)
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
func describePlanVerbose(n planner.Node, verbose bool, nm *explainNames) string {
	if !verbose {
		return describePlan(n, nm)
	}
	switch p := n.(type) {
	case *planner.SeqScan:
		if p.Table == nil {
			return describePlan(n, nm)
		}
		// When the range-table name table disambiguates a repeated
		// relation name (PG select_rtable_names_for_explain), use
		// the disambiguated name instead of the catalog name so a
		// relation scanned twice without an alias prints two
		// distinguishable labels (e.g. "nation" / "nation_1").
		if dname := nm.disambiguatedName(n); dname != "" {
			if p.Table.Stats != nil {
				return fmt.Sprintf("Seq Scan on %s (stats)", dname)
			}
			return "Seq Scan on " + dname
		}
		tname := schemaQualify(p.Table.QualifiedName())
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return fmt.Sprintf("Seq Scan on %s %s", tname, p.Alias)
		}
		return "Seq Scan on " + tname
	case *planner.IndexScan:
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("Index Scan using %s on %s", p.Index.QualifiedName(), dname)
		}
		return fmt.Sprintf("Index Scan using %s on %s", p.Index.QualifiedName(), schemaQualify(p.Table.QualifiedName()))
	case *planner.IndexOnlyScan:
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("Index Only Scan using %s on %s", p.Index.QualifiedName(), dname)
		}
		return fmt.Sprintf("Index Only Scan using %s on %s", p.Index.QualifiedName(), schemaQualify(p.Table.QualifiedName()))
	case *planner.Insert:
		return "Insert on " + schemaQualify(p.Table.QualifiedName())
	case *planner.Update:
		return "Update on " + schemaQualify(p.Table.QualifiedName())
	case *planner.Delete:
		return "Delete on " + schemaQualify(p.Table.QualifiedName())
	}
	return describePlan(n, nm)
}

func describePlan(n planner.Node, nm *explainNames) string {
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
		// A NullAware anti join (from unnesting a non-correlated
		// NOT IN, M0122-0011) carries three-valued-NULL semantics
		// the join type alone does not convey; surface it so plan
		// diffs can tell the two anti joins apart.
		jt := joinTypeName(p.Type)
		if p.Type == planner.JoinTypeAnti && p.NullAware {
			jt += " NULL-AWARE"
		}
		if p.Algo == planner.JoinAlgoHash && p.BuildLeft {
			return fmt.Sprintf("%s (%s, build=left)", algo, jt)
		}
		return fmt.Sprintf("%s (%s)", algo, jt)
	case *planner.Gather:
		return "Gather"
	case *planner.GatherMerge:
		return "Gather Merge"
	case *planner.Distinct:
		return "Unique"
	case *planner.Aggregate:
		// P9: PG prefixes a split aggregate's two halves with "Partial " and
		// "Finalize " (explain.c, from the Agg node's aggsplit). Without them
		// a parallel plan shows two identical HashAggregate lines stacked with
		// a Gather between, which reads as a bug rather than as the split it
		// is. This is the prefix P2's rename was sequenced to protect.
		prefix := ""
		switch p.Mode {
		case planner.AggModePartial:
			prefix = "Partial "
		case planner.AggModeFinal:
			prefix = "Finalize "
		}
		if p.GroupingSets != nil {
			// M0125-0048: one node, one hash table per grouping set. PG shows
			// this as a HashAggregate (or MixedAggregate when it sorts some
			// levels) carrying one "Hash Key:"/"Group Key:" line per set,
			// including the bare "Group Key: ()" for the grand total. goopg
			// has no per-key detail lines (see the note below), so the set
			// count rides on the existing key-count suffix — which is what
			// distinguishes this from the N-branch UNION ALL the clause used
			// to expand into.
			return fmt.Sprintf("%sHashAggregate (%d keys, %d grouping sets)",
				prefix, len(p.GroupExprs), len(p.GroupingSets))
		}
		if len(p.GroupExprs) == 0 {
			// PG labels an ungrouped aggregate (AGG_PLAIN) "Aggregate"
			// regardless of strategy, so this one is already faithful.
			return prefix + "Aggregate"
		}
		// P2 (docs/design/parallel-query/06 §4.1): this used to say
		// "GroupAggregate", which in PG means specifically the SORTED,
		// streaming strategy (AGG_SORTED). goopg has exactly one grouped
		// implementation and it is a hash aggregate — aggregateOp.Open
		// builds `groups := map[string]*groupRuntime{}` for every case,
		// with no sorted/streaming variant and no hash-agg spill. The label
		// was therefore describing a strategy the engine does not have.
		//
		// Corrected here rather than later because P5 prefixes these labels
		// with "Partial "/"Finalize " for parallel aggregation, which would
		// otherwise cement "Partial GroupAggregate" onto a hash node.
		//
		// The "(%d keys)" suffix is goopg's own; PG emits a separate
		// "Group Key: <exprs>" detail line instead. Kept as-is to hold this
		// stage to the rename — see the TODO's follow-up note.
		return fmt.Sprintf("%sHashAggregate (%d keys)", prefix, len(p.GroupExprs))
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
		if dname := nm.disambiguatedName(n); dname != "" {
			if p.Table != nil && p.Table.Stats != nil {
				return fmt.Sprintf("Seq Scan on %s (stats)", dname)
			}
			return "Seq Scan on " + dname
		}
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
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("Index Scan using %s on %s", p.Index.QualifiedName(), dname)
		}
		return fmt.Sprintf("Index Scan using %s on %s", p.Index.QualifiedName(), p.Table.QualifiedName())
	case *planner.IndexOnlyScan:
		// M0118-0009 (design 0118-0102): horizons.spec inspects the IOS
		// label via `EXPLAIN (COSTS OFF)` (pruner_query_plan) — mirror
		// upstream's "Index Only Scan using <idx> on <table>".
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("Index Only Scan using %s on %s", p.Index.QualifiedName(), dname)
		}
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
	case *planner.OrdinalityWrap:
		// WITH ORDINALITY wrapper; also reused by the S4a scalar
		// residual rewrite to tag the outer side with a per-row
		// ordinal (preserving duplicate-row multiplicity through
		// the aggregate-above-join shape).
		return "Ordinality"
	case *planner.NestedLoopIndexJoin:
		// M0054-0006: render `Nested Loop` matching upstream's
		// EXPLAIN output for a nested-loop join with an inner
		// IndexScan side. The inner IndexScan node renders its
		// own label (`Index Scan using <idx> on <table>`) below.
		return fmt.Sprintf("Nested Loop (%s)", joinTypeName(p.Type))
	case *planner.Memoize:
		return "Memoize"
	case *planner.Merge:
		return fmt.Sprintf("Merge on %s", p.Target.QualifiedName())
	case *planner.CTEDMLPrefix:
		return "CTE DML"
	case *planner.MaterializedCTEScan:
		return fmt.Sprintf("CTE %s", p.Name)
	case *planner.SetOp:
		// M0125-0037(i): this node used to fall through to the `%T`
		// default and print the raw Go type name `*planner.SetOp` —
		// with no children, because planChildren had no case for it
		// either. TPC-DS Q5/Q18/Q67 therefore rendered as four-line
		// plans with the whole query body invisible, and M0125-0026
		// could not classify them at all.
		//
		// PG's vocabulary (explain.c ExplainNode, T_SetOp) is:
		//   UNION ALL          -> Append          (no SetOp node at all)
		//   UNION (distinct)   -> HashAggregate over Append
		//   INTERSECT / EXCEPT -> "HashSetOp <cmd>" (SETOP_HASHED),
		//                         cmd one of Intersect / Intersect All /
		//                         Except / Except All
		// goopg has ONE fused node for all of these (operators_setop.go:
		// `streaming` for UNION ALL, buffered multiset otherwise), so the
		// two exact mappings are used verbatim and the UNION-distinct case
		// prints `HashSetOp Union` — a spelling PG never emits, chosen
		// because it cannot be confused with a PG node that means
		// something else (PG's SetOpCmd has no Union member). The
		// two-node HashAggregate/Append shape PG uses there is a
		// deferral-ledger row, not a silent divergence.
		return setOpNodeName(p)
	}
	return fmt.Sprintf("%T", n)
}

// setOpNodeName renders a SetOp's PG-style label — see the
// describePlan case above for the mapping rationale.
func setOpNodeName(p *planner.SetOp) string {
	if setOpRendersAsAppend(p) {
		return "Append"
	}
	return "HashSetOp " + setOpCommandName(p)
}

// setOpRendersAsAppend reports whether p is a UNION ALL, which PG
// plans as a plain Append with no SetOp node. This is also the shape
// the planner builds for partition / inheritance expansion
// (planner.go:2495 and :2545 construct `SetOp{All: true}` chains),
// so getting this branch right is what keeps a partitioned scan from
// printing as a set operation.
func setOpRendersAsAppend(p *planner.SetOp) bool {
	return p.All && p.Op == parser.SetOpUnion
}

// setOpCommandName mirrors explain.c's SETOPCMD_* text (the token PG
// appends after the node name, and its JSON "Command" property).
func setOpCommandName(p *planner.SetOp) string {
	var cmd string
	switch p.Op {
	case parser.SetOpIntersect:
		cmd = "Intersect"
	case parser.SetOpExcept:
		cmd = "Except"
	default:
		cmd = "Union"
	}
	if p.All {
		cmd += " All"
	}
	return cmd
}

// setOpAppendBranches flattens a left-deep chain of UNION ALL SetOps
// into the flat child list PG's single Append carries. goopg builds
// `a UNION ALL b UNION ALL c` as SetOp(SetOp(a,b),c); without this,
// EXPLAIN would print nested Appends where PG prints one with three
// children, and TPC-DS Q5's five-branch union would render five levels
// deep. Only ALL-union links are absorbed — an INTERSECT or EXCEPT in
// the chain is a real node and keeps its own line.
func setOpAppendBranches(p *planner.SetOp, out []planner.Node) []planner.Node {
	for _, side := range [...]planner.Node{p.Left, p.Right} {
		if inner, ok := side.(*planner.SetOp); ok && setOpRendersAsAppend(inner) {
			out = setOpAppendBranches(inner, out)
			continue
		}
		out = append(out, side)
	}
	return out
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
	case planner.JoinTypeSemi:
		// Produced by EXISTS / IN unnesting (M0061-0001). Rendered
		// inside goopg's `<algo> (<type>)` label rather than
		// upstream's `Hash Semi Join` node spelling — switching to
		// PG's spelling would churn every existing plan line and is
		// tracked separately.
		return "SEMI"
	case planner.JoinTypeAnti:
		return "ANTI"
	}
	return "?"
}

// planChildren returns the child plan nodes EXPLAIN should
// recurse into. Limited to the subset of node types whose
// children are public Node fields. Update/Delete additionally
// walk their FROM/USING scans (M0097-0065/-0076) alongside the
// target-table Child; note the executor extracts their Child's
// scan info directly (extractScan) rather than Build()-ing it as
// a nested instrumented Operator, so EXPLAIN ANALYZE won't have
// per-node actual-rows/time stats for it (falls back to the
// cost-estimate-only line — see walkPlanAnalyzeFiltered's
// `stats[n]` miss path). Returns nil for leaf nodes.
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
	case *planner.Gather:
		return []planner.Node{p.Child}
	case *planner.GatherMerge:
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
	case *planner.Update:
		out := make([]planner.Node, 0, 1+len(p.FromScans))
		out = append(out, p.Child)
		out = append(out, p.FromScans...)
		return out
	case *planner.Delete:
		out := make([]planner.Node, 0, 1+len(p.UsingScans))
		out = append(out, p.Child)
		out = append(out, p.UsingScans...)
		return out
	case *planner.CTEScan:
		return []planner.Node{p.Child}
	case *planner.LockRows:
		return []planner.Node{p.Child}
	case *planner.OrdinalityWrap:
		return []planner.Node{p.Child}
	case *planner.NestedLoopIndexJoin:
		// M0054-0006: render outer driver and inner index probe. With an
		// S7 Memoize attached, the cache node renders between the join
		// and the probe (InnerMemo.Child aliases p.Inner).
		if p.InnerMemo != nil {
			return []planner.Node{p.Outer, p.InnerMemo}
		}
		return []planner.Node{p.Outer, p.Inner}
	case *planner.Memoize:
		return []planner.Node{p.Child}
	case *planner.Merge:
		return []planner.Node{p.Source}
	case *planner.CTEDMLPrefix:
		out := make([]planner.Node, 0, len(p.DMls)+1)
		out = append(out, p.DMls...)
		out = append(out, p.Body)
		return out
	case *planner.SetOp:
		// M0125-0037(i): the missing case that truncated Q5/Q18/Q67 to a
		// four-line plan. A UNION ALL chain collapses into one Append's
		// child list the way PG plans it; every other set operation is a
		// binary HashSetOp whose two inputs render directly beneath it
		// (verified against PG 18.3: `HashSetOp Intersect` carries two
		// Seq Scan children, not an Append).
		if setOpRendersAsAppend(p) {
			return setOpAppendBranches(p, nil)
		}
		return []planner.Node{p.Left, p.Right}
	}
	return nil
}
