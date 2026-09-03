package executor

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// explainOp renders the inner plan tree as a single-column
// "QUERY PLAN" text result. v0 emits one row per plan node in
// pre-order with two-space indent per nesting level, matching
// upstream PG's text-format output well enough for
// debugging-by-eyeball during the M0003 planner work. EXPLAIN
// ANALYZE / VERBOSE / FORMAT JSON wait on later loops; see
// docs/design/0003-0007-explain.md.
type explainOp struct {
	plan *optimizer.Explain
	rows []Row
	idx  int
}

func newExplainOp(p *optimizer.Explain) *explainOp {
	return &explainOp{plan: p}
}

func (o *explainOp) Schema() optimizer.Schema {
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

// formatMemoryLine renders the upstream "Memory: used=NkB  allocated=NkB"
// text (show_memory_counters in postgres/src/backend/commands/explain.c),
// omitting the whole line when memAllocated is zero. M0122-0003.
func formatMemoryLine(s *nodeStats) string {
	if s.memAllocated == 0 && s.memPeak == 0 {
		return ""
	}
	// used = peak bytes (the most in-use at any point), allocated = total ever allocated
	usedKB := s.memPeak / 1024
	allocatedKB := s.memAllocated / 1024
	return fmt.Sprintf("Memory: used=%dkB  allocated=%dkB", usedKB, allocatedKB)
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
func walkPlan(b *strings.Builder, n optimizer.Node, depth int, rows *[]Row, opts parser.ExplainOptions) {
	walkPlanFiltered(n, depth, rows, opts, nil, nil, &subPlanReg{rel: newExplainNames(n), cte: collectCTEHoist(n)})
}

// walkPlanFiltered is the inner driver for walkPlan. attachedFilter
// is a Filter.Predicate carried down from a Filter wrapper that
// was skipped above us — it is rendered as `Filter:` detail under
// the next scan-like node we render. attachedFilterNode is that same
// wrapper's plan node, carried so the collapsed line can report the
// wrapper's POST-qual row estimate (see below).
func walkPlanFiltered(n optimizer.Node, indent int, rows *[]Row, opts parser.ExplainOptions, attachedFilter optimizer.Expr, attachedFilterNode optimizer.Node, reg *subPlanReg) {
	// Skip Project wrappers: PG has no "Projection" plan node;
	// the projection is part of the parent / scan's render.
	if p, ok := n.(*optimizer.Project); ok {
		walkPlanFiltered(p.Child, indent, rows, opts, attachedFilter, attachedFilterNode, reg)
		return
	}
	// Skip Filter wrappers and push their predicate down to be
	// rendered as `Filter:` detail under the next scan node.
	if f, ok := n.(*optimizer.Filter); ok {
		next := f.Predicate
		nextNode := optimizer.Node(f)
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
		walkPlanFiltered(f.Child, indent, rows, opts, next, nextNode, reg)
		return
	}

	// PG-faithful cumulative indent (postgres/src/backend/commands/explain.c
	// ExplainNode, ~1616-1635): `indent` here is `es->indent` as seen on
	// entry. A node at nonzero incoming indent prints "->  " at `indent*2`
	// raw spaces and bumps the running indent by 2 for it; every node then
	// gets an UNCONDITIONAL +1 for its own name. `childIndent` is the
	// resulting value — what gets handed to this node's children, its
	// detail lines, and any InitPlan/SubPlan/CTE label hanging off it. This
	// is NOT a flat `2*depth`: the "->  " marker lands at raw columns
	// 0, 2, 8, 14, 20 … (deltas 2, 6, 6, 6) as nesting deepens, verified
	// against postgres/src/test/regress/expected/aggregates.out:3158-3165.
	prefix := ""
	arrowIndent := indent
	if indent != 0 {
		prefix = strings.Repeat(" ", indent*2) + "->  "
		arrowIndent = indent + 2
	}
	childIndent := arrowIndent + 1
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
		est := optimizer.EstimateRows(rowSrc)
		if est <= 0 {
			est = 1
		}
		// PG's cost annotation, from the path the planner chose (take2 P0-02).
		startup, total, width := explainCostFields(rowSrc, est)
		label += fmt.Sprintf("  (cost=%.2f..%.2f rows=%d width=%d)", startup, total, est, width)
	}
	*rows = append(*rows, Row{NewStringDatum(label)})

	// Detail indent: the node's own post-increment `childIndent`, in raw
	// spaces — mirrors PG's `Sort Key:` / `Index Cond:` / `Filter:` /
	// `Output:` indent convention (explain.c's ExplainPropertyText et al.
	// all indent at the CURRENT es->indent, same column for every property
	// of a node).
	detailIndent := strings.Repeat(" ", childIndent*2)
	emitNodeDetailLines(n, detailIndent, opts.Verbose, rows, attachedFilter, reg)

	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			outline := detailIndent + "Output: " + strings.Join(cols, ", ")
			*rows = append(*rows, Row{NewStringDatum(outline)})
		}
	}

	// The `CTE <name>` sections belong to the top plan node, before its
	// InitPlans and children — upstream prints them from the top node's
	// subplan list (M0125-0049; explain_cte.go carries the shape).
	emitCTESections(rows, indent, childIndent, reg, func(body optimizer.Node, bodyIndent int) {
		walkPlanFiltered(body, bodyIndent, rows, opts, nil, nil, reg)
	})

	// Sublinks referenced by this node's detail lines print their
	// inner plan as an indented `SubPlan N` subtree, as upstream's
	// ExplainSubPlans does. n becomes the ancestor plan node for the
	// duration (upstream's push_ancestor_plan): a correlated reference
	// inside the sublink is deparsed against THIS node's namespace, not
	// the relation the sublink itself scans.
	prevAncestor := reg.ancestor
	reg.ancestor = n
	emitSubPlanSubtrees(rows, detailIndent, opts, reg, nil, func(sub optimizer.Node, subIndent int) {
		walkPlanFiltered(sub, subIndent, rows, opts, nil, nil, reg)
	})
	reg.ancestor = prevAncestor

	for _, c := range renderChildren(n, reg.cte) {
		walkPlanFiltered(c, childIndent, rows, opts, nil, nil, reg)
	}
}

// emitSubPlanSubtrees drains the sublinks assigned while rendering
// the current node and prints each as
//
//	SubPlan N
//	  ->  <inner plan tree>
//
// render walks one sublink's inner plan at the given indent. The
// queue is drained in a loop because a sublink's own plan can
// reference further sublinks, which assign higher numbers as they
// are rendered (matching upstream's nested-SubPlan output).
// spStats, when non-nil (the ANALYZE path), appends this sublink's
// measured execution counters to its `SubPlan N` line.
func emitSubPlanSubtrees(rows *[]Row, detailIndent string, opts parser.ExplainOptions, reg *subPlanReg, spStats map[optimizer.Expr]*SubPlanSiteStats, render func(optimizer.Node, int)) {
	for {
		pending := reg.takePending()
		if len(pending) == 0 {
			return
		}
		for _, sp := range pending {
			// A non-correlated scalar SubqueryExpr is an InitPlan (upstream
			// plan_name "InitPlan %d", rendered bare — see subPlanName); the
			// rest keep the `SubPlan N` label. Same discriminator as
			// subPlanName so the number and its subtree agree.
			kind := "SubPlan"
			if sq, ok := sp.expr.(*optimizer.SubqueryExpr); ok && sq.IsNonCorrelated {
				kind = "InitPlan"
			}
			line := detailIndent + fmt.Sprintf("%s %d", kind, sp.n)
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
				// The `SubPlan N` / `InitPlan N` label was printed at
				// this owner's own childIndent (detailIndent = that
				// value in raw spaces), matching upstream's plan_name
				// branch printing at the CURRENT es->indent. Upstream
				// then does an UNCONDITIONAL `es->indent++` (only +1,
				// not +2 — the body's own "->  " arrow is what adds the
				// +2, once ExplainNode is entered for it) before
				// rendering the body, so the body's incoming indent is
				// childIndent+1 = len(detailIndent)/2 + 1. Verified
				// against postgres/src/test/regress/expected/
				// subselect.out:380-391 (nested SubPlan/InitPlan).
				render(sp.plan, len(detailIndent)/2+1)
			}
		}
	}
}

// emitNodeDetailLines writes the PG-style detail lines that
// belong under n (Sort Key / Index Cond / Filter). attachedFilter
// is a Filter.Predicate from a Filter wrapper above n that was
// skipped — it surfaces as `Filter:` when n is a scan-like node.
func emitNodeDetailLines(n optimizer.Node, indent string, verbose bool, rows *[]Row, attachedFilter optimizer.Expr, reg *subPlanReg) {
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
	case *optimizer.Result:
		// A childless Result's targets carry sublinks (the S6 min/max
		// InitPlan). Non-verbose EXPLAIN prints no Output line for Result,
		// but a target-only sublink must still be ASSIGNED here so
		// emitSubPlanSubtrees (run right after this walker in
		// walkPlanFiltered) prints its `InitPlan N` subtree under the
		// Result label — the targets are the one place a sublink can live
		// without a detail line, so nothing else would ever number it. The
		// rendered text is discarded; only the assignment side effect
		// (subPlanName → assign) is wanted. PG does the same structural
		// walk through SS_process_sublinks/ExplainSubPlans.
		for _, t := range p.Targets {
			formatExprQual(t, reg, qualify)
		}
		// S6 Slice 3d: the const-arg inner Result carries a resconstantqual
		// that PG prints as `One-Time Filter:` (explain.c:2234-2240
		// show_upper_qual, "One-Time Filter" label) — same indent/paren shape
		// as the scan `Filter:` line. formatExprQual's IsNullExpr arm renders
		// `(100 IS NOT NULL)` with its parens already built in, and wrapParen
		// does not double-wrap a single paren group.
		//
		// M0134-0010c round 2: a bare literal constant (goopg's always-false
		// NOT NULL reduction wraps the scan in
		// `Result{OneTimeFilter: BooleanConst{false}}`) must NOT be
		// parenthesized — PG's ruleutils never wraps a lone Const in this
		// position (`predicate.out`: `One-Time Filter: false`, no parens).
		// Every compound expression (NullTest, OpExpr, ...) already carries
		// its own parens from formatExprQual and is unaffected by this
		// literal-only exception.
		if p.OneTimeFilter != nil {
			otf := formatExprQual(p.OneTimeFilter, reg, qualify)
			if !isLiteralOneTimeFilterConst(p.OneTimeFilter) {
				otf = wrapParen(otf)
			}
			*rows = append(*rows, Row{NewStringDatum(indent + "One-Time Filter: " + otf)})
		}
	case *optimizer.Gather:
		// PG emits `Workers Planned:` in PLAIN EXPLAIN — it is a plan-time
		// property. `Workers Launched:` is execution-time and belongs to the
		// ANALYZE walk instead.
		*rows = append(*rows, Row{NewStringDatum(
			indent + fmt.Sprintf("Workers Planned: %d", p.WorkersPlanned))})
	case *optimizer.GatherMerge:
		// PG prints Workers Planned for Gather Merge and, unlike Sort, does NOT
		// print the merge keys (explain.c has no show_sort_keys call in the
		// T_GatherMerge arm) — the keys are the child Sort's, and it prints
		// them itself one line below.
		*rows = append(*rows, Row{NewStringDatum(
			indent + fmt.Sprintf("Workers Planned: %d", p.WorkersPlanned))})
	// SIBLING PAIR (M0134-0001 S18): this case and *optimizer.Aggregate's
	// group-key loop below must move together. Both derive from PG's single
	// show_sort_group_keys (explain.c:2767-2823) — a GroupAggregate/Sort
	// stack prints one line from each, two rows apart, so a change to one
	// without the other manufactures an internal inconsistency within a
	// single EXPLAIN output. See docs/design/0134-0001-p2-explain-format.md
	// § S18 for the "reference vs compute" rule this pair implements.
	case *optimizer.Sort:
		if len(p.Keys) > 0 {
			parts := make([]string, 0, len(p.Keys))
			for _, k := range p.Keys {
				s := formatExprQual(k.Expr, reg, qualify)
				// S18: a Sort never evaluates expressions — its key is
				// always PG's OUTER_VAR reference into the child's target
				// list, so get_special_variable's "force parentheses for a
				// non-Var referent" rule applies unconditionally here.
				// Placed BEFORE the DESC/NULLS suffix: PG prints
				// `Sort Key: ((g % 10000)) DESC`, keeping the decoration
				// outside the added pair.
				if _, isVar := k.Expr.(*optimizer.ColumnRef); !isVar {
					s = forceParen(s)
				}
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
	case *optimizer.IndexScan:
		if cond := formatIndexCond(p, reg); cond != "" {
			*rows = append(*rows, Row{NewStringDatum(indent + "Index Cond: " + cond)})
		}
		// `Filter:` comes from either of two places, and upstream renders both
		// the same way because to PG they ARE the same thing — a scan-level
		// qual that is not an index qual (`show_scan_qual` on `scan.plan.qual`,
		// explain.c). goopg reaches it as a `*Filter` node above the scan in the
		// ordinary case, and as `IndexScan.Cond` when the scan is an NLI inner
		// and the quals had to move inside the node (plan.go, IndexScan.Cond).
		// Only one is ever set, but they are joined rather than sequenced so a
		// future third producer cannot make one silently shadow the other.
		filt := attachedFilter
		if p.Cond != nil {
			if filt == nil {
				filt = p.Cond
			} else {
				filt = &optimizer.BinaryOp{Op: parser.OpAnd, Left: filt, Right: p.Cond}
			}
		}
		if filt != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(filt, reg, qualify)))})
		}
	case *optimizer.BitmapHeapScan:
		// PG prints `Recheck Cond:` then `Filter:` on a Bitmap Heap Scan. The
		// two are different things and both belong here: BitmapQual is the
		// recheck list (lossy pages only), Cond is the per-tuple residual the
		// scan carries when it is an NLI inner (plan.go, BitmapHeapScan.Cond).
		if len(p.BitmapQual) > 0 {
			var rc optimizer.Expr = p.BitmapQual[0]
			for _, q := range p.BitmapQual[1:] {
				rc = &optimizer.BinaryOp{Op: parser.OpAnd, Left: rc, Right: q}
			}
			*rows = append(*rows, Row{NewStringDatum(indent + "Recheck Cond: " + wrapParen(formatExprQual(rc, reg, qualify)))})
		}
		filt := attachedFilter
		if p.Cond != nil {
			if filt == nil {
				filt = p.Cond
			} else {
				filt = &optimizer.BinaryOp{Op: parser.OpAnd, Left: filt, Right: p.Cond}
			}
		}
		if filt != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(filt, reg, qualify)))})
		}
	case *optimizer.IndexOnlyScan:
		// S6 min/max rewrite: the IOS's residual `col IS NOT NULL` qual is
		// stored in Cond (it cannot be pushed into the btree probe) and
		// rendered as an Index Cond, matching upstream's build_minmax_path
		// which carries it as an index qual (planagg.c build_minmax_path).
		//
		// The btree bound itself (M0134-0001 S4 class 8: it may now be
		// EXCLUSIVE) renders on the same line, since PG prints one
		// `Index Cond:` holding every index qual.
		var condParts []string
		if bound := formatIndexOnlyCond(p, reg); bound != "" {
			condParts = append(condParts, bound)
		}
		if p.Cond != nil {
			condParts = append(condParts, wrapParen(formatExprQual(p.Cond, reg, qualify)))
		}
		if len(condParts) > 0 {
			cond := strings.Join(condParts, " AND ")
			if len(condParts) > 1 {
				cond = "(" + cond + ")"
			}
			*rows = append(*rows, Row{NewStringDatum(indent + "Index Cond: " + cond)})
		}
		// A residual qual above the IOS (the composite-index trailing-column
		// guard keeps one) is upstream's `show_scan_qual` line and was simply
		// missing here, so a Filter that DOES run went unrendered.
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	case *optimizer.Join:
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
			if p.Algo == optimizer.JoinAlgoMerge {
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
	case *optimizer.NestedLoopIndexJoin:
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
	case *optimizer.Memoize:
		// Upstream shape: `Cache Key: t.a, t.b` under the Memoize node.
		if len(p.KeyExprs) > 0 {
			parts := make([]string, 0, len(p.KeyExprs))
			for _, ke := range p.KeyExprs {
				parts = append(parts, formatExprQual(ke, reg, qualify))
			}
			*rows = append(*rows, Row{NewStringDatum(indent + "Cache Key: " + strings.Join(parts, ", "))})
		}
	case *optimizer.SeqScan:
		// TABLESAMPLE's `Sampling:` line (explain.c show_tablesample,
		// M0134-0175). Upstream prints it BEFORE any Filter, and prints
		// REPEATABLE only when the clause carried one.
		if p.TableSample != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Sampling: " + formatTableSample(p.TableSample, reg, qualify))})
		}
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	case *optimizer.GenerateSeries, *optimizer.GenerateSubscripts, *optimizer.FromUnnest, *optimizer.UserSrfScan:
		// M0134-0001 P2 S2: the verbose-only `Function Call:` deparse
		// (explain.c T_FunctionScan 2067-2083), emitted before any
		// Filter. Built from a synthetic FuncCall and rendered through
		// the existing FuncCall case so `generate_series(1, 3)` is
		// byte-identical to PG. Not emitted for ProjectSet (no case
		// here).
		if verbose {
			if name, args := srfFunctionCallArgs(n); name != "" {
				fc := &optimizer.FuncCall{Name: name, Args: args}
				*rows = append(*rows, Row{NewStringDatum(indent + "Function Call: " + formatExprQual(fc, reg, qualify))})
			}
		}
		if attachedFilter != nil {
			*rows = append(*rows, Row{NewStringDatum(indent + "Filter: " + wrapParen(formatExprQual(attachedFilter, reg, qualify)))})
		}
	case *optimizer.Aggregate:
		// S5 (0134-0001 P2): PG's show_agg_keys prints a `Group Key:` line
		// for ANY grouped aggregate, regardless of strategy — AGG_HASHED
		// included (explain.c:2616-2636); show_hashagg_info emits no key
		// line of its own (explain.c:3716-3830). The grouping-sets path is
		// out of S5 scope (its per-set lines are a separate M0125-0048
		// shape; the suffix on the label carries the set count).
		if len(p.GroupExprs) > 0 && p.GroupingSets == nil {
			// GroupKeyOrder (S8 Slice 2c-i, 0134-0001 P2) reorders only this
			// printed line — GroupExprs itself, and every output binding
			// fixed to its written position, is untouched. nil means the
			// written order (every pre-2c-i plan).
			order := p.GroupKeyOrder
			if order == nil {
				order = make([]int, len(p.GroupExprs))
				for i := range order {
					order[i] = i
				}
			}
			// SIBLING PAIR (M0134-0001 S18): see the *optimizer.Sort case
			// above. A GroupAggregate reads its already-sorted input
			// through a child Sort, so it inherits that Sort's OUTER_VAR
			// reference wrap; a HashAggregate sitting directly on a
			// scan/join computes the group key itself and keeps the
			// single (unwrapped) form. docs/design/0134-0001-p2-explain-format.md
			// § S18.
			_, groupAgg := p.Child.(*optimizer.Sort)
			parts := make([]string, 0, len(order))
			for _, gi := range order {
				s := formatExprQual(p.GroupExprs[gi], reg, qualify)
				if groupAgg {
					if _, isVar := p.GroupExprs[gi].(*optimizer.ColumnRef); !isVar {
						s = forceParen(s)
					}
				}
				parts = append(parts, s)
			}
			*rows = append(*rows, Row{NewStringDatum(indent + "Group Key: " + strings.Join(parts, ", "))})
		}
		// PG order (explain.c:2196-2197): Group Key first, then the HAVING
		// qual as `Filter:` (show_upper_qual plan->qual).
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
func formatJoinKeyCond(p *optimizer.Join, reg *subPlanReg, qualify bool) string {
	if p.Algo != optimizer.JoinAlgoHash && p.Algo != optimizer.JoinAlgoMerge {
		return ""
	}
	pairs := p.HashKeys
	if len(pairs) == 0 {
		if p.LeftKey == nil || p.RightKey == nil {
			return ""
		}
		pairs = []optimizer.JoinKeyPair{{Left: p.LeftKey, Right: p.RightKey}}
	}
	parts := make([]string, 0, len(pairs))
	for _, k := range pairs {
		if k.Left == nil || k.Right == nil {
			continue
		}
		parts = append(parts, formatExprQual(
			&optimizer.BinaryOp{Op: parser.OpEq, Left: k.Left, Right: k.Right}, reg, qualify))
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
func formatJoinFilter(p *optimizer.Join, reg *subPlanReg, qualify bool) string {
	if p == nil || p.Predicate == nil {
		return ""
	}
	residual := p.Predicate
	switch p.Algo {
	case optimizer.JoinAlgoHash:
		residual = p.ExecHashKeyPlan().Residual
	case optimizer.JoinAlgoMerge:
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
func formatIndexCond(p *optimizer.IndexScan, reg *subPlanReg) string {
	if p == nil || p.Index == nil {
		return ""
	}
	return formatIndexCondParts(p.Index, p.Keys, p.Key, p.LowKey, p.HighKey, p.LowOp, p.HighOp, reg)
}

// formatIndexOnlyCond is formatIndexCond for an IndexOnlyScan. The two nodes
// carry the same probe fields (an IOS is a promoted IndexScan), and the
// promotion now copies LowOp/HighOp too (M0134-0001 S4 class 8), so the bound
// must render with the same strictness the executor scans with — a `>` bound
// printed as `>=` would misdescribe the plan.
func formatIndexOnlyCond(p *optimizer.IndexOnlyScan, reg *subPlanReg) string {
	if p == nil || p.Index == nil {
		return ""
	}
	return formatIndexCondParts(p.Index, p.Keys, p.Key, p.LowKey, p.HighKey, p.LowOp, p.HighOp, reg)
}

// formatIndexCondKey renders the key side of one Index Cond bound. The
// index-column side always prints bare (PG prints the catalog column name),
// but the KEY side is qualified when the query names more than one relation:
// PG's show_scan_qual names an outer Var with its relation, printing
// `(c_nationkey = c1.c_nationkey)` for a self-join probe. P0-04: without
// this, that probe renders `(c_nationkey = c_nationkey)` — a
// self-comparison that reads as unsatisfiable, the same defect class
// M0125-0039 removed from Filter lines. A single-relation scan stays bare
// (reg.names().qualify mirrors upstream's rtable_size > 1 rule), and an
// unresolvable binding declines to the bare name as before.
func formatIndexCondKey(e optimizer.Expr, reg *subPlanReg) string {
	qualify := false
	if reg != nil {
		qualify = reg.names().qualify()
	}
	return formatExprQual(e, reg, qualify)
}

// formatIndexCondParts is the shared body of formatIndexCond /
// formatIndexOnlyCond: sibling paths that must not disagree.
func formatIndexCondParts(index *catalog.Index, keys []optimizer.Expr, key, lowKey, highKey optimizer.Expr, lowOp, highOp parser.OpCode, reg *subPlanReg) string {
	p := struct {
		Keys                 []optimizer.Expr
		Key, LowKey, HighKey optimizer.Expr
		LowOp, HighOp        parser.OpCode
	}{keys, key, lowKey, highKey, lowOp, highOp}
	cols := index.Columns
	// Multi-column equality probe.
	if len(p.Keys) > 0 && len(cols) >= len(p.Keys) {
		if len(p.Keys) == 1 {
			return wrapParen(cols[0] + " = " + formatIndexCondKey(p.Keys[0], reg))
		}
		parts := make([]string, len(p.Keys))
		for i, k := range p.Keys {
			parts[i] = cols[i] + " = " + formatIndexCondKey(k, reg)
		}
		return wrapParen(strings.Join(parts, " AND "))
	}
	// Single-column equality.
	if p.Key != nil && len(cols) > 0 {
		return wrapParen(cols[0] + " = " + formatIndexCondKey(p.Key, reg))
	}
	// Range scan.
	if (p.LowKey != nil || p.HighKey != nil) && len(cols) > 0 {
		col := cols[0]
		var parts []string
		if p.LowKey != nil {
			loOp := ">="
			if p.LowOp == parser.OpGt {
				loOp = ">"
			}
			parts = append(parts, col+" "+loOp+" "+formatIndexCondKey(p.LowKey, reg))
		}
		if p.HighKey != nil {
			hiOp := "<="
			if p.HighOp == parser.OpLt {
				hiOp = "<"
			}
			parts = append(parts, col+" "+hiOp+" "+formatIndexCondKey(p.HighKey, reg))
		}
		if len(parts) > 0 {
			return wrapParen(strings.Join(parts, " AND "))
		}
	}
	return ""
}

// isLiteralOneTimeFilterConst reports whether e is a bare literal constant —
// the ONLY case PG's ruleutils leaves unparenthesised in a `One-Time Filter:`
// position (verified against `false` in predicate.out/case.out/join.out/
// inherit.out/groupingsets.out; every compound OneTimeFilter observed there
// — NullTest, OpExpr, function calls, InitPlan refs — already renders with
// its own parens via formatExprQual). Narrowly scoped to the literal-Const
// node kinds this pass can actually produce (BooleanConst) plus the other
// planner literal kinds for consistency; NOT extended to bare Var/FuncExpr
// (`"*VALUES*".column1`, `tattle(9, 8)`) since goopg does not yet build a
// One-Time Filter of those shapes — deferred with the rest of the join-qual
// slice (M0134-0010c round 2 report).
func isLiteralOneTimeFilterConst(e optimizer.Expr) bool {
	switch e.(type) {
	case *optimizer.BooleanConst, *optimizer.IntegerConst, *optimizer.NumericConst,
		*optimizer.StringConst, *optimizer.NullConst:
		return true
	}
	return false
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

// forceParen unconditionally wraps s in one more parenthesis pair — S18's
// `get_special_variable` "force parentheses for a non-Var referent" rule
// (ruleutils.c). Deliberately NOT `wrapParen`: wrapParen is idempotent
// ("skip if already parenthesised") for the Filter:/Index Cond: sites and
// would silently no-op on an already-parenthesised OpExpr like
// `(g % 10000)`, which S18's Sort Key:/Group Key: sites must still wrap to
// `((g % 10000))`.
func forceParen(s string) string {
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
	num     map[optimizer.Expr]int
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
	ancestor optimizer.Node
	// cte holds the CTE bodies lifted out of their reference sites for this
	// render, so a multiply-referenced CTE prints once as a `CTE <name>`
	// section instead of once per reference (M0125-0049). Shared with the
	// sublink walk, which is why it lives here: a reference inside a
	// `SubPlan N` subtree must render as a leaf too. nil when the plan has
	// no CTE, and nil-receiver tolerant either way.
	cte *cteHoist
}

// ancestorNode returns the plan node currently being rendered, or nil.
func (r *subPlanReg) ancestorNode() optimizer.Node {
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
	expr optimizer.Expr
	plan optimizer.Node
}

// assign returns the SubPlan number already given to e, or
// allocates the next one and queues e's inner plan for emission.
func (r *subPlanReg) assign(e optimizer.Expr, plan optimizer.Node) int {
	if r == nil {
		return 0
	}
	if n, ok := r.num[e]; ok {
		return n
	}
	if r.num == nil {
		r.num = make(map[optimizer.Expr]int)
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

// subPlanName renders the plan-name token for e, matching upstream's
// SubPlan.plan_name: a non-correlated scalar SubqueryExpr is an
// InitPlan (`InitPlan %d` — subselect.c:3154 psprintf("InitPlan %d",
// node->plan_id), rendered bare by ExplainSubPlans), everything else a
// SubPlan (`SubPlan %d`). PG 18.3 emits the bare `InitPlan 1` with NO
// `(returns $0)` suffix in every EXPLAIN mode; verified against the
// committed expected/aggregates.out and a live PG 18.3 on 2026-08-15.
// Without a registry the number is unknown, so the bare kind is
// printed instead of a wrong number.
func subPlanName(r *subPlanReg, e optimizer.Expr, plan optimizer.Node) string {
	if n := r.assign(e, plan); n > 0 {
		kind := "SubPlan"
		if sq, ok := e.(*optimizer.SubqueryExpr); ok && sq.IsNonCorrelated {
			kind = "InitPlan"
		}
		return fmt.Sprintf("%s %d", kind, n)
	}
	return "SubPlan"
}

// formatExprPG renders a planner expression in upstream PG's
// EXPLAIN style with no SubPlan registry — sublinks render as
// `SubPlan` without a number. Prefer formatExprPGReg from the
// EXPLAIN walkers so sublinks are numbered and their subtrees
// emitted.
func formatExprPG(e optimizer.Expr) string {
	return formatExprPGReg(e, nil)
}

// formatExprPGReg renders e with bare (unqualified) column names — the
// rendering every EXPLAIN detail line used before M0125-0039, and still the
// right one for a scan-node qual, which upstream deparses with
// varprefix=false (explain.c show_scan_qual). Call formatExprQual directly
// with qualify=true for the upper-node quals PG prefixes.
func formatExprPGReg(e optimizer.Expr, reg *subPlanReg) string {
	return formatExprQual(e, reg, false)
}

// formatExprPGReg renders a planner expression in upstream PG's
// EXPLAIN style: column names, integer/string/numeric literals,
// and infix operators. Sublinks (EXISTS / IN / scalar subquery)
// render as PG-style SubPlan references via reg. Falls back to a
// compact `<type>` token for expression kinds we don't yet render
// (sufficient for the isolation specs that pass through
// `EXPLAIN (COSTS OFF)`; the detail line is informational).
func formatExprQual(e optimizer.Expr, reg *subPlanReg, qualify bool) string {
	if e == nil {
		return ""
	}
	switch x := e.(type) {
	case *optimizer.ColumnRef:
		// qualify is upstream's deparse_context.varprefix
		// (ruleutils.c get_variable's need_prefix): a plain Var is
		// printed bare on a scan qual and qualified everywhere else
		// once the query has more than one range-table entry.
		return reg.names().column(x.SourceTableIdx, x.Name, qualify)
	case *optimizer.OuterColumnRef:
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
	case *optimizer.IntegerConst:
		return fmt.Sprintf("%d", x.Value)
	case *optimizer.NumericConst:
		return x.Value
	case *optimizer.StringConst:
		// PG renders string literals as single-quoted; escape
		// embedded single quotes per SQL convention.
		return "'" + strings.ReplaceAll(x.Value, "'", "''") + "'"
	case *optimizer.BooleanConst:
		if x.Value {
			return "true"
		}
		return "false"
	case *optimizer.NullConst:
		return "NULL"
	case *optimizer.BinaryOp:
		return "(" + formatExprQual(x.Left, reg, qualify) + " " + x.Op.String() + " " + formatExprQual(x.Right, reg, qualify) + ")"
	case *optimizer.UnaryOp:
		return "(" + x.Op.String() + " " + formatExprQual(x.Operand, reg, qualify) + ")"
	case *optimizer.CastExpr:
		return formatExprQual(x.Operand, reg, qualify)
	case *optimizer.FuncCall:
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = formatExprQual(a, reg, qualify)
		}
		return x.Name + "(" + strings.Join(args, ", ") + ")"
	case *optimizer.ParamRef:
		return fmt.Sprintf("$%d", x.Number)
	case *optimizer.ExecParamRef:
		// PARAM_EXEC slot (D4.1) — upstream prints exec params the
		// same `$N` way (e.g. `Index Cond: (l_orderkey = $0)`); the
		// number is the flat per-statement slot ID.
		return fmt.Sprintf("$%d", x.ID)
	case *optimizer.TypedStringLit:
		// Upstream renders a typed literal as `'value'::type`
		// (ruleutils.c get_const_expr with showtype).
		return "'" + strings.ReplaceAll(x.Value, "'", "''") + "'::" + x.Type
	case *optimizer.IntervalLit:
		// `interval 'N' <unit>` (Qualified) folds the unit into the
		// literal text so the rendering stays a single typed constant.
		lit := x.Value
		if x.Unit != "" {
			lit += " " + x.Unit
		}
		return "'" + strings.ReplaceAll(lit, "'", "''") + "'::interval"
	case *optimizer.ExistsExpr:
		// Upstream renders EXISTS sublinks as `EXISTS(SubPlan N)`
		// (ruleutils.c get_rule_expr, T_SubPlan / EXISTS_SUBLINK).
		s := "EXISTS(" + subPlanName(reg, x, x.Plan) + ")"
		if x.Negated {
			return "NOT " + s
		}
		return s
	case *optimizer.SubqueryExpr:
		// EXPR_SUBLINK: upstream decorates scalar subplan
		// references with nothing but parentheses.
		return "(" + subPlanName(reg, x, x.Plan) + ")"
	case *optimizer.ArraySubqueryExpr:
		// ARRAY_SUBLINK: upstream prints `ARRAY(<plan_name>)`.
		return "ARRAY(" + subPlanName(reg, x, x.Plan) + ")"
	case *optimizer.InExpr:
		return formatInExprPG(x, reg, qualify)
	case *optimizer.IsNullExpr:
		// ruleutils.c get_rule_expr, T_NullTest.
		if x.Negated {
			return "(" + formatExprQual(x.Operand, reg, qualify) + " IS NOT NULL)"
		}
		return "(" + formatExprQual(x.Operand, reg, qualify) + " IS NULL)"
	case *optimizer.IsBoolExpr:
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
	case *optimizer.IsDistinctFromExpr:
		op := " IS DISTINCT FROM "
		if x.Negated {
			op = " IS NOT DISTINCT FROM "
		}
		return "(" + formatExprQual(x.Left, reg, qualify) + op + formatExprQual(x.Right, reg, qualify) + ")"
	case *optimizer.CaseExpr:
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
	case *optimizer.ExtractExpr:
		return "EXTRACT(" + x.Field + " FROM " + formatExprQual(x.Source, reg, qualify) + ")"
	case *optimizer.CollateExpr:
		return "(" + formatExprQual(x.Operand, reg, qualify) + " COLLATE " + x.CollationName + ")"
	case *optimizer.RowExpr:
		elems := make([]string, len(x.Elems))
		for i, el := range x.Elems {
			elems[i] = formatExprQual(el, reg, qualify)
		}
		return "ROW(" + strings.Join(elems, ", ") + ")"
	case *optimizer.TableOidExpr:
		return "tableoid"
	case *optimizer.CTIDExpr:
		return "ctid"
	case *optimizer.MergeActionExpr:
		return "merge_action()"
	case *optimizer.MergeWholeRowRef:
		if x.IsOld {
			return "old"
		}
		return "new"
	case *optimizer.MultiAssignSubqRow:
		// Multi-assignment sublink (`SET (a,b) = (SELECT …)`); the row
		// itself is the subplan reference.
		return "(" + subPlanName(reg, x, x.Plan) + ")"
	case *optimizer.MultiAssignSubqElem:
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
func formatInExprPG(x *optimizer.InExpr, reg *subPlanReg, qualify bool) string {
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
func schemaColumnNames(n optimizer.Node) []string {
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
func walkPlanAnalyze(b *strings.Builder, n optimizer.Node, depth int, rows *[]Row, opts parser.ExplainOptions, stats nodeStatsTable, spStats map[optimizer.Expr]*SubPlanSiteStats, memoStats map[*optimizer.Memoize]*MemoizeStats, hashStats map[*optimizer.Join]*HashJoinStats) {
	walkPlanAnalyzeFiltered(n, depth, rows, opts, stats, spStats, memoStats, hashStats, nil, nil, 0, &subPlanReg{rel: newExplainNames(n), cte: collectCTEHoist(n)})
}

func walkPlanAnalyzeFiltered(n optimizer.Node, indent int, rows *[]Row, opts parser.ExplainOptions, stats nodeStatsTable, spStats map[optimizer.Expr]*SubPlanSiteStats, memoStats map[*optimizer.Memoize]*MemoizeStats, hashStats map[*optimizer.Join]*HashJoinStats, attachedFilter optimizer.Expr, attachedFilterNode optimizer.Node, filterRowsRemoved int64, reg *subPlanReg) {
	if p, ok := n.(*optimizer.Project); ok {
		walkPlanAnalyzeFiltered(p.Child, indent, rows, opts, stats, spStats, memoStats, hashStats, attachedFilter, attachedFilterNode, filterRowsRemoved, reg)
		return
	}
	if f, ok := n.(*optimizer.Filter); ok {
		next := f.Predicate
		nextNode := optimizer.Node(f)
		if attachedFilter != nil {
			next = attachedFilter
			nextNode = attachedFilterNode
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
		walkPlanAnalyzeFiltered(f.Child, indent, rows, opts, stats, spStats, memoStats, hashStats, next, nextNode, fr, reg)
		return
	}

	// PG-faithful cumulative indent — see walkPlanFiltered's twin comment
	// (postgres/src/backend/commands/explain.c:1616-1635, ExplainNode) for
	// the derivation. Both walkers must agree: a test pinning one proves
	// nothing about the other.
	prefix := ""
	arrowIndent := indent
	if indent != 0 {
		prefix = strings.Repeat(" ", indent*2) + "->  "
		arrowIndent = indent + 2
	}
	childIndent := arrowIndent + 1
	label := prefix + describePlanVerbose(n, opts.Verbose, reg.names())
	showCostsA := !opts.Set.Costs || opts.Costs
	if showCostsA {
		// SIBLING PAIR with walkPlanFiltered's row-source block (take2
		// P0-04b). A Filter wrapper collapses into the scan/join line it
		// wraps, but the row ESTIMATE lives on the wrapper — upstream never
		// has this split because set_baserel_size_estimates stores rel->rows
		// already scaled by the restriction clauses' selectivity.
		//
		// This walker read the estimate off the collapsed-INTO node and so
		// printed the UNFILTERED count, while plain EXPLAIN printed the
		// filtered one. The two modes therefore disagreed on rows= for the
		// same scan, and every EXPLAIN ANALYZE artefact overstated the
		// planner's estimate on exactly the nodes where it matters most.
		rowSrc := n
		if attachedFilterNode != nil {
			rowSrc = attachedFilterNode
		}
		est := optimizer.EstimateRows(rowSrc)
		if est <= 0 {
			est = 1
		}
		startup, total, width := explainCostFields(rowSrc, est)
		label += fmt.Sprintf("  (cost=%.2f..%.2f rows=%d width=%d)", startup, total, est, width)
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

	detailIndent := strings.Repeat(" ", childIndent*2)
	emitNodeDetailLines(n, detailIndent, opts.Verbose, rows, attachedFilter, reg)

	// IndexOnlyScan emits a "Heap Fetches: N" detail line under ANALYZE,
		// matching upstream's text format (design 0118-0102).
		if _, isIOS := n.(*optimizer.IndexOnlyScan); isIOS {
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
	if m, isMemo := n.(*optimizer.Memoize); isMemo {
		if ms := memoStats[m]; ms != nil {
			*rows = append(*rows, Row{NewStringDatum(detailIndent + fmt.Sprintf(
				"Hits: %d  Misses: %d  Evictions: %d  Overflows: %d",
				ms.Hits, ms.Misses, ms.Evictions, ms.Overflows))})
		}
	}

	// A hash join emits PG's hash-table line under ANALYZE. Upstream hangs it
	// off the HASH node; goopg has no Hash node (the build lives inside
	// joinOp), so it hangs off the Hash Join. M0127-P3.5 / design 06 §4.
	if j, isJoin := n.(*optimizer.Join); isJoin && j.Algo == optimizer.JoinAlgoHash {
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

	// EXPLAIN (ANALYZE, MEMORY) emits a "Memory: used=NkB  allocated=NkB"
	// detail line per node (M0122-0003).
	if opts.Memory {
		if s, ok := stats[n]; ok && s != nil {
			if line := formatMemoryLine(s); line != "" {
				*rows = append(*rows, Row{NewStringDatum(detailIndent + line)})
			}
		}
	}

	if opts.Verbose {
		if cols := schemaColumnNames(n); len(cols) > 0 {
			outline := detailIndent + "Output: " + strings.Join(cols, ", ")
			*rows = append(*rows, Row{NewStringDatum(outline)})
		}
	}

	// The CTE sections print with their instrumentation intact: every
	// reference shares one body Node, so the section IS the subtree that
	// ran (the later references replay from ctx.CTERowCache). M0125-0049.
	emitCTESections(rows, indent, childIndent, reg, func(body optimizer.Node, bodyIndent int) {
		walkPlanAnalyzeFiltered(body, bodyIndent, rows, opts, stats, spStats, memoStats, hashStats, nil, nil, 0, reg)
	})

	// Sublink subtrees keep their instrumentation: stats is passed
	// through so inner nodes still report actual rows / loops.
	prevAncestor := reg.ancestor
	reg.ancestor = n
	emitSubPlanSubtrees(rows, detailIndent, opts, reg, spStats, func(sub optimizer.Node, subIndent int) {
		walkPlanAnalyzeFiltered(sub, subIndent, rows, opts, stats, spStats, memoStats, hashStats, nil, nil, 0, reg)
	})
	reg.ancestor = prevAncestor

	for _, c := range renderChildren(n, reg.cte) {
		walkPlanAnalyzeFiltered(c, childIndent, rows, opts, stats, spStats, memoStats, hashStats, nil, nil, 0, reg)
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
func planToJSONWithStats(n optimizer.Node, opts parser.ExplainOptions, stats nodeStatsTable, trackIOTiming bool) map[string]any {
	// P0-04e: one name table for the whole plan, as in planToJSON.
	nm := newExplainNames(n)
	return planToJSONWithStatsNamed(n, opts, stats, trackIOTiming, &subPlanReg{rel: nm})
}

// planToJSONWithStatsNamed is planToJSONWithStats with the shared render
// state threaded through, so collapsed Filter predicates qualify exactly
// like the text walker's.
func planToJSONWithStatsNamed(n optimizer.Node, opts parser.ExplainOptions, stats nodeStatsTable, trackIOTiming bool, reg *subPlanReg) map[string]any {
	obj := planToJSONNamed(n, opts, reg)
	// P0-04e: stats follow VISIBLE nodes. The text walker never prints
	// wrapper lines, so wrapper stats were invisible there too; stating
	// the surviving child's numbers is what makes the two walkers agree.
	surviving, _, _ := jsonCollapse(n)
	if s, ok := stats[surviving]; ok && s != nil {
		obj["Actual Rows"] = s.rowsOut
		obj["Actual Loops"] = s.loops
		if s.timing {
			obj["Actual Startup Time"] = nsToMs(s.startupNs)
			obj["Actual Total Time"] = nsToMs(s.totalNs)
		}
		// "Heap Fetches" is an IndexOnlyScan-only field upstream emits under
			// ANALYZE (design 0118-0102). horizons.spec reads it via
			// ...->'Plan'->'Heap Fetches'.
			if _, isIOS := surviving.(*optimizer.IndexOnlyScan); isIOS {
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
		// EXPLAIN (ANALYZE, MEMORY): per-node memory counters
		// in kB, matching PG's show_memory_counters JSON output.
		// Outside the opts.Buffers block — memory is independent
		// of buffer tracking. M0122-0003.
		if opts.Memory {
			obj["Memory Used"] = s.memPeak / 1024
			obj["Memory Allocated"] = s.memAllocated / 1024
		}
	}
	// Re-render Plans recursively with stats, replacing the
	// stats-free children that planToJSONNamed installed. Children come
	// from the SURVIVING node (wrappers collapsed above must not reappear
	// as entries) through the same shared table.
	children := planChildren(surviving)
	if len(children) > 0 {
		plans := make([]map[string]any, 0, len(children))
		for _, c := range children {
			plans = append(plans, planToJSONWithStatsNamed(c, opts, stats, trackIOTiming, reg))
		}
		obj["Plans"] = plans
	}
	return obj
}

// jsonCollapse skips Project/Filter wrapper nodes exactly like
// walkPlanFiltered does, so FORMAT JSON emits the same TREE as the text
// walker (and as PG, which has neither node): a Project vanishes, and the
// OUTERMOST Filter's predicate is carried to be rendered as the surviving
// node's "Filter" property. filterNode is the wrapper the predicate came
// from — the text walker's rowSrc rule reports that node's estimate, and
// the JSON costs below do the same. A nil node (or one that is only
// wrappers above nil) collapses to nils; callers must handle that as the
// text walker does by rendering nothing.
func jsonCollapse(n optimizer.Node) (surviving optimizer.Node, filter optimizer.Expr, filterNode optimizer.Node) {
	for {
		if p, ok := n.(*optimizer.Project); ok {
			n = p.Child
			continue
		}
		if f, ok := n.(*optimizer.Filter); ok {
			if filter == nil {
				filter, filterNode = f.Predicate, f
			}
			n = f.Child
			continue
		}
		return n, filter, filterNode
	}
}

// planToJSON renders n as the upstream-style JSON object an
// `EXPLAIN (FORMAT JSON)` row carries. Recursive — children land
// in a `Plans` array. The Output column-name array is emitted
// only when opts.Verbose is set (matches upstream's behaviour
// where columns are part of VERBOSE output, not the default
// JSON shape).
func planToJSON(n optimizer.Node, opts parser.ExplainOptions) map[string]any {
	// P0-04e: one name table for the whole plan, shared down the
	// recursion — a per-node table would count only that subtree's
	// relations and qualify differently from the text walker.
	nm := newExplainNames(n)
	return planToJSONNamed(n, opts, &subPlanReg{rel: nm})
}

// planToJSONNamed is planToJSON with the shared render state threaded
// through. Wrapper collapse happens here (not in the caller) so every
// level — including planToJSONWithStats' re-rendered children below —
// sees the same tree the text walker does.
func planToJSONNamed(n optimizer.Node, opts parser.ExplainOptions, reg *subPlanReg) map[string]any {
	orig := n
	n, filter, filterNode := jsonCollapse(n)
	if n == nil {
		// Degenerate wrapper-over-nil: no surviving node to render.
		// Fall back to the old behaviour (the single type-name
		// fallthrough arm) rather than inventing a node type PG
		// has no word for.
		n = orig
		filter, filterNode = nil, nil
	}
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
	if p, ok := n.(*optimizer.SetOp); ok && !setOpRendersAsAppend(p) {
		obj["Node Type"] = "SetOp"
		obj["Strategy"] = "Hashed"
		obj["Command"] = setOpCommandName(p)
	}
	est := optimizer.EstimateRows(n)
	// P0-04e: when a Filter wrapper collapsed above, the text walker
	// reports the WRAPPER's post-qual estimate on this line (its rowSrc
	// rule) — the child's pre-qual count would overstate by exactly the
	// filter's selectivity. Mirror it so the two walkers agree.
	costNode := n
	if filterNode != nil {
		costNode = filterNode
		est = optimizer.EstimateRows(costNode)
	}
	if est > 0 {
		obj["Plan Rows"] = est
	}
	// take2 P0-02: PG's non-text formats carry Startup Cost, Total Cost and
	// Plan Width alongside Plan Rows (explain.c). goopg emitted only Plan
	// Rows, so FORMAT JSON stated no cost at all — and once the text walkers
	// print real costs, a silent JSON would DISAGREE with text rather than
	// merely lag it.
	startup, total, width := explainCostFields(costNode, est)
	obj["Startup Cost"] = startup
	obj["Total Cost"] = total
	obj["Plan Width"] = width
	// P0-04e: the collapsed Filter's predicate rides as PG's "Filter"
	// property on the surviving node. Same outermost-only rule and same
	// qualify decision as the text walker's `Filter:` detail line, so the
	// two walkers state the same qual.
	if filter != nil {
		fqual := reg.names().qualify() && !explainIsScanNode(n)
		obj["Filter"] = wrapParen(formatExprQual(filter, reg, fqual))
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
			// Same shared name table (P0-04e header comment): a fresh
			// per-child table would qualify differently from text.
			plans = append(plans, planToJSONNamed(c, opts, reg))
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
// explainRelName renders a scanned relation the way PG's ExplainTargetRel does
// (postgres/src/backend/commands/explain.c:4384-4499).
//
// PG sets `objectname = get_rel_name(rte->relid)` — the BARE name — and sets
// `namespace` only when `es->verbose` (:4409-4411). The text branch then emits
// `on <ns>.<obj>` when a namespace was resolved and `on <obj>` otherwise
// (:4490-4497). So the schema appears in VERBOSE mode ONLY.
//
// goopg qualified in both modes, so every scan node of every plain plan carried
// a `public.` PostgreSQL does not print — measured against the :65432 reference
// cluster: `Seq Scan on nation` (PG) vs `Seq Scan on public.nation` (goopg).
// That is one guaranteed rendering divergence per scan node, corpus-wide, which
// would have swamped the plan-parity instrument P0-05/06 builds on top of this
// renderer (take2 P0-04d).
func explainRelName(t *catalog.Table, verbose bool) string {
	if t == nil {
		return ""
	}
	if verbose {
		return t.QualifiedName()
	}
	return t.Name
}

// explainIndexName renders an index the way PG's explain_get_index_name does
// (explain.c, `result = get_rel_name(indexId)`): the BARE name, in BOTH modes.
// PG never schema-qualifies an index in EXPLAIN — verified on the oracle, where
// `EXPLAIN (VERBOSE)` prints `Index Scan using orders_pk on public.orders`: the
// relation gains the schema, the index does not. goopg qualified the index in
// both modes.
func explainIndexName(i *catalog.Index) string {
	if i == nil {
		return ""
	}
	return i.Name
}


// explainCostFields returns the (startup, total, width) EXPLAIN prints for a
// node, and whether they came from the path the planner actually chose.
//
// take2 P0-02. Before this, both text walkers printed the literal string
// `cost=0.00..0.00 ... width=0` on every node of every plan, so no artefact in
// the repository stated what the planner believed and no artefact could
// attribute a wrong plan to a wrong cost. The chosen Path carries real numbers;
// createPlan now stamps them onto the node (optimizer.PlanCost).
//
// Nodes the path search did not produce — everything the legacy rewriter builds
// above the seam — fall to optimizer.DeriveLegacyDisplayCost rather than
// printing zeros. A plan mixing real costs with 0.00 is WORSE than one where
// every cost is 0.00: with all-zero, a reader knows nothing is priced; with a
// mixture, a free node and an unpriced node look identical.
func explainCostFields(n optimizer.Node, rows int64) (startup, total float64, width int) {
	if c, ok := n.(optimizer.PlanCostCarrier); ok {
		if pc, set := c.PlanCostInfo(); set {
			return pc.StartupCost, pc.TotalCost, pc.PlanWidth
		}
	}
	d := optimizer.DeriveLegacyDisplayCost(n, rows)
	return d.StartupCost, d.TotalCost, d.PlanWidth
}

// schemaQualify prepends "public." to an unqualified table name for VERBOSE mode.
func schemaQualify(name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	return "public." + name
}

// srfFunctionScanLabel builds the plain EXPLAIN label for a set-returning
// function scan node. PG prints the FROM-item alias (refname) only when it
// differs from the function name (explain.c:4498); the default alias — no
// AS clause — IS the function name, so it is omitted.
func srfFunctionScanLabel(funcName, alias string) string {
	if alias != "" && alias != funcName {
		return "Function Scan on " + funcName + " " + alias
	}
	return "Function Scan on " + funcName
}

// srfFunctionScanLabelQualified is the verbose counterpart of
// srfFunctionScanLabel: the schema is qualified in the label (explain.c
// 4493-4495), but the alias is still omitted only when it equals the BARE
// function name — PG compares refname against get_func_name(objectname),
// never the namespace-qualified spelling (explain.c:4498).
func srfFunctionScanLabelQualified(qualified, bareFuncName, alias string) string {
	label := "Function Scan on " + qualified
	if alias != "" && alias != bareFuncName {
		label += " " + alias
	}
	return label
}

// srfFunctionCallArgs returns the synthetic FuncCall name and argument list
// for an SRF scan node's verbose-only `Function Call:` detail line (PG
// explain.c T_FunctionScan 2067-2083). Returns ("", nil) for node kinds that
// carry no function call (e.g. ProjectSet). UserSrfScan uses the BARE
// routine name: PG deparses rtfunc->funcexpr, whose FuncExpr name is
// unqualified — the schema is carried only by the label above.
func srfFunctionCallArgs(n optimizer.Node) (string, []optimizer.Expr) {
	switch p := n.(type) {
	case *optimizer.GenerateSeries:
		args := []optimizer.Expr{p.Start, p.Stop}
		if p.Step != nil {
			args = append(args, p.Step)
		}
		return "generate_series", args
	case *optimizer.GenerateSubscripts:
		args := []optimizer.Expr{p.ArrExpr, p.Dim}
		if p.Reversed != nil {
			args = append(args, p.Reversed)
		}
		return "generate_subscripts", args
	case *optimizer.FromUnnest:
		if p.ArrExpr != nil {
			return "unnest", []optimizer.Expr{p.ArrExpr}
		}
		return "unnest", p.ArrExprs
	case *optimizer.UserSrfScan:
		return p.Routine.Name, p.Args
	}
	return "", nil
}

// describePlanVerbose returns the plan-node description; verbose=true adds schema qualification.
func describePlanVerbose(n optimizer.Node, verbose bool, nm *explainNames) string {
	if !verbose {
		return describePlan(n, nm)
	}
	switch p := n.(type) {
	// SIBLING PAIR (M0134-0001 S17): this case and describePlan's
	// *optimizer.SeqScan case must move together. PG's "Parallel " prefix is
	// emitted in ExplainNode before the pname append and is entirely
	// independent of es->verbose (explain.c:1630-1631) — this case has its
	// own return statements rather than delegating to describePlan (except
	// when p.Table == nil), so the prefix has to be applied here too, or
	// EXPLAIN VERBOSE would silently diverge from plain EXPLAIN.
	case *optimizer.SeqScan:
		if p.Table == nil {
			return describePlan(n, nm)
		}
		parallelPrefix := ""
		if p.Parallel {
			parallelPrefix = "Parallel "
		}
		// When the range-table name table disambiguates a repeated
		// relation name (PG select_rtable_names_for_explain), use
		// the disambiguated name instead of the catalog name so a
		// relation scanned twice without an alias prints two
		// distinguishable labels (e.g. "nation" / "nation_1").
		if dname := nm.disambiguatedName(n); dname != "" {
			return parallelPrefix + seqScanLabel(p) + " on " + dname
		}
		tname := schemaQualify(p.Table.QualifiedName())
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return fmt.Sprintf("%s%s on %s %s", parallelPrefix, seqScanLabel(p), tname, p.Alias)
		}
		return parallelPrefix + seqScanLabel(p) + " on " + tname
	case *optimizer.IndexScan:
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("Index Scan using %s on %s", explainIndexName(p.Index), dname)
		}
		// P0-04: print the FROM-clause alias like the SeqScan arm does, so
		// a self-join's second scan renders `on customer c2` as PG's
		// select_rtable_names does, not a bare second `on customer`.
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return fmt.Sprintf("Index Scan using %s on %s %s", explainIndexName(p.Index), schemaQualify(p.Table.QualifiedName()), p.Alias)
		}
		return fmt.Sprintf("Index Scan using %s on %s", explainIndexName(p.Index), schemaQualify(p.Table.QualifiedName()))
	case *optimizer.IndexOnlyScan:
		// S6 max rewrite: PG's ExplainIndexScanDetails (explain.c:4330-4336)
		// puts " Backward" between the scan name and " using".
		dir := ""
		if p.Backward {
			dir = " Backward"
		}
		// M0134-0189: "Parallel " prefix, same rule and same source as the
		// SeqScan / BitmapHeapScan arms — plan->parallel_aware stamped once by
		// stampParallelScan, never inferred from sitting under a Gather. This
		// arm exists in BOTH describePlanVerbose and describePlan and the two
		// must agree: PG's prefix is verbose-independent (explain.c:1630).
		parallelPrefix := ""
		if p.Parallel {
			parallelPrefix = "Parallel "
		}
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("%sIndex Only Scan%s using %s on %s", parallelPrefix, dir, explainIndexName(p.Index), dname)
		}
		return fmt.Sprintf("%sIndex Only Scan%s using %s on %s", parallelPrefix, dir, explainIndexName(p.Index), schemaQualify(p.Table.QualifiedName()))
	case *optimizer.Insert:
		return "Insert on " + schemaQualify(p.Table.QualifiedName())
	case *optimizer.Update:
		return "Update on " + schemaQualify(p.Table.QualifiedName())
	case *optimizer.Delete:
		return "Delete on " + schemaQualify(p.Table.QualifiedName())
	case *optimizer.GenerateSeries:
		// M0134-0001 P2 S2: verbose Function Scan label qualifies the
		// schema (explain.c:4493-4495); builtin SRFs live in pg_catalog.
		return srfFunctionScanLabelQualified("pg_catalog.generate_series", "generate_series", p.Alias)
	case *optimizer.GenerateSubscripts:
		return srfFunctionScanLabelQualified("pg_catalog.generate_subscripts", "generate_subscripts", p.Alias)
	case *optimizer.FromUnnest:
		return srfFunctionScanLabelQualified("pg_catalog.unnest", "unnest", p.Alias)
	case *optimizer.UserSrfScan:
		return srfFunctionScanLabelQualified(p.Routine.QualifiedName(), p.Routine.Name, p.Alias)
	}
	// Every node type with no verbose-specific arm above lands here. It must
	// ask for the VERBOSE rendering, or the schema qualification PG emits in
	// this mode is dropped for exactly those types.
	return describePlanMode(n, nm, true)
}

// describePlan renders the PLAIN (non-verbose) label. It is a thin wrapper so
// that existing callers keep their meaning; the mode-carrying form below is
// what the verbose walker falls through to.
func describePlan(n optimizer.Node, nm *explainNames) string {
	return describePlanMode(n, nm, false)
}

// describePlanMode renders a node's label for the given verbosity.
//
// The mode has to be threaded because PG's schema qualification depends on it
// (explain.c:4409-4411 sets `namespace` only when `es->verbose`), and
// describePlanVerbose delegates HERE for every node type it has no arm of its
// own for — Bitmap Heap Scan, Insert/Update/Delete, Merge and the rest. Before
// the mode was threaded, those types rendered identically in both modes: which
// meant that whichever mode was made correct, the other one was wrong.
func describePlanMode(n optimizer.Node, nm *explainNames, verbose bool) string {
	switch p := n.(type) {
	case *optimizer.Project:
		return "Projection"
	case *optimizer.Filter:
		return "Filter"
	case *optimizer.Sort:
		return "Sort"
	case *optimizer.Limit:
		return "Limit"
	case *optimizer.Result:
		// Childless projection (S6 min/max rewrite top node). PG's T_Result
		// emits a bare `Result` label (explain.c, T_Result arm).
		return "Result"
	case *optimizer.Values:
		return fmt.Sprintf("Values (%d rows)", len(p.Rows))
	case *optimizer.Join:
		// S3 (0134-0001 P2, class 7a): PG interpolates the join type
		// into the node name (explain.c jointype switch 1712-1763, text
		// rule 1754-1758). Base words are explain.c's pname: T_NestLoop
		// 1421 "Nested Loop", T_MergeJoin 1424 "Merge", T_HashJoin 1428
		// "Hash" — "Join" is appended by the jointype rule. NullAware and
		// BuildLeft remain planner fields the executor consumes; only the
		// label rendering changed (no more "(TYPE)", "NULL-AWARE" or
		// "build=left" annotations).
		algo := "Nested Loop"
		if p.Algo == optimizer.JoinAlgoHash {
			algo = "Hash"
		}
		if p.Algo == optimizer.JoinAlgoMerge {
			algo = "Merge"
		}
		return joinLabel(algo, p.Type)
	case *optimizer.Gather:
		return "Gather"
	case *optimizer.GatherMerge:
		return "Gather Merge"
	case *optimizer.Distinct:
		return "Unique"
	case *optimizer.Aggregate:
		// P9: PG prefixes a split aggregate's two halves with "Partial " and
		// "Finalize " (explain.c, from the Agg node's aggsplit). Without them
		// a parallel plan shows two identical HashAggregate lines stacked with
		// a Gather between, which reads as a bug rather than as the split it
		// is. This is the prefix P2's rename was sequenced to protect.
		prefix := ""
		switch p.Mode {
		case optimizer.AggModePartial:
			prefix = "Partial "
		case optimizer.AggModeFinal:
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
		// S5 (0134-0001 P2): the "(%d keys)" suffix is gone — PG's AGG_HASHED
		// label is a bare "HashAggregate" (explain.c:1549). The grouping
		// expressions render as a separate "Group Key: <exprs>" detail line
		// (emitNodeDetailLines; show_agg_keys, explain.c:2616-2636).
		//
		// S8 (M0134-0001): the Strategy field now discriminates. PG labels
		// AGG_SORTED "GroupAggregate" and AGG_HASHED "HashAggregate"
		// (explain.c:1531-1553). The planner does not set Strategy yet, so a
		// hand-built node is the only way this renders GroupAggregate today.
		if p.Strategy == optimizer.AggStrategySorted {
			return prefix + "GroupAggregate"
		}
		return prefix + "HashAggregate"
	case *optimizer.WindowAgg:
		return fmt.Sprintf("WindowAgg (%d funcs)", len(p.Funcs))
	case *optimizer.SeqScan:
		// S17 (0134-0001): "Parallel " prefix mirrors PG's plan->parallel_aware
		// (explain.c:1630-1631), stamped once at Gather-construction time by
		// parallel.go's stampParallelScan — see optimizer.SeqScan.Parallel.
		//
		// SIBLING PAIR: describePlanVerbose's *optimizer.SeqScan case (above,
		// verbose=true path) has its own independent return statements rather
		// than delegating here, so it applies the SAME prefix logic itself —
		// PG's prefix is verbose-independent (explain.c:1630-1631), so if
		// these two ever disagree, EXPLAIN VERBOSE silently drops the label
		// EXPLAIN (plain) shows.
		parallelPrefix := ""
		if p.Parallel {
			parallelPrefix = "Parallel "
		}
		if dname := nm.disambiguatedName(n); dname != "" {
			return parallelPrefix + seqScanLabel(p) + " on " + dname
		}
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return fmt.Sprintf("%s%s on %s %s", parallelPrefix, seqScanLabel(p), explainRelName(p.Table, verbose), p.Alias)
		}
		return fmt.Sprintf("%s%s on %s", parallelPrefix, seqScanLabel(p), explainRelName(p.Table, verbose))
	case *optimizer.IndexScan:
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("Index Scan using %s on %s", explainIndexName(p.Index), dname)
		}
		// P0-04: same alias branch as the verbose arm above.
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return fmt.Sprintf("Index Scan using %s on %s %s", explainIndexName(p.Index), explainRelName(p.Table, verbose), p.Alias)
		}
		return fmt.Sprintf("Index Scan using %s on %s", explainIndexName(p.Index), explainRelName(p.Table, verbose))
	case *optimizer.IndexOnlyScan:
		// M0118-0009 (design 0118-0102): horizons.spec inspects the IOS
		// label via `EXPLAIN (COSTS OFF)` (pruner_query_plan) — mirror
		// upstream's "Index Only Scan using <idx> on <table>".
		// S6 max rewrite: PG puts " Backward" between the scan name and
		// " using" for a backward index scan (ExplainIndexScanDetails,
		// explain.c:4330-4336).
		dir := ""
		if p.Backward {
			dir = " Backward"
		}
		// M0134-0189: "Parallel " prefix, same rule and same source as the
		// SeqScan / BitmapHeapScan arms — plan->parallel_aware stamped once by
		// stampParallelScan, never inferred from sitting under a Gather. This
		// arm exists in BOTH describePlanVerbose and describePlan and the two
		// must agree: PG's prefix is verbose-independent (explain.c:1630).
		parallelPrefix := ""
		if p.Parallel {
			parallelPrefix = "Parallel "
		}
		if dname := nm.disambiguatedName(n); dname != "" {
			return fmt.Sprintf("%sIndex Only Scan%s using %s on %s", parallelPrefix, dir, explainIndexName(p.Index), dname)
		}
		return fmt.Sprintf("%sIndex Only Scan%s using %s on %s", parallelPrefix, dir, explainIndexName(p.Index), explainRelName(p.Table, verbose))
	case *optimizer.Insert:
		return fmt.Sprintf("Insert on %s", explainRelName(p.Table, verbose))
	case *optimizer.Update:
		return fmt.Sprintf("Update on %s", explainRelName(p.Table, verbose))
	case *optimizer.Delete:
		return fmt.Sprintf("Delete on %s", explainRelName(p.Table, verbose))
	case *optimizer.DDL:
		return fmt.Sprintf("DDL %T", p.Stmt)
	case *optimizer.Transaction:
		return fmt.Sprintf("Transaction (%v)", p.Verb)
	case *optimizer.Checkpoint:
		return "Checkpoint"
	case *optimizer.Utility:
		return fmt.Sprintf("Utility %T", p.Stmt)
	case *optimizer.Copy:
		return fmt.Sprintf("Copy %s", p.Table.QualifiedName())
	case *optimizer.Explain:
		return "Explain"
	case *optimizer.CTEScan:
		// take2 P0-01: a CTEScan whose body is a bare WorkTableScan is the
		// RECURSIVE SELF-REFERENCE, not an ordinary CTE reference — with.go
		// registers the working table as the CTE's body while planning the
		// recursive term. PG renders it as a leaf `WorkTable Scan on <name>`
		// (oracle: `->  WorkTable Scan on t t_1`), never as a CTE Scan, and
		// gives it no `CTE <name>` section of its own.
		if isRecursiveSelfRef(p) {
			if p.Alias != "" && p.Alias != p.Name {
				return fmt.Sprintf("WorkTable Scan on %s %s", p.Name, p.Alias)
			}
			return fmt.Sprintf("WorkTable Scan on %s", p.Name)
		}
		// Mirrors upstream's "CTE Scan on <name>" label; the
		// alias is rendered separately when distinct so output
		// like `WITH a AS (SELECT 1) SELECT * FROM a x` shows
		// `CTE Scan on a x`.
		if p.Alias != "" && p.Alias != p.Name {
			return fmt.Sprintf("CTE Scan on %s %s", p.Name, p.Alias)
		}
		return fmt.Sprintf("CTE Scan on %s", p.Name)
	case *optimizer.LockRows:
		// Mirrors upstream's "LockRows" label; per-relation
		// detail is too verbose for the single-line label and
		// is left to a future VERBOSE-only extension.
		return "LockRows"
	case *optimizer.OrdinalityWrap:
		// WITH ORDINALITY wrapper; also reused by the S4a scalar
		// residual rewrite to tag the outer side with a per-row
		// ordinal (preserving duplicate-row multiplicity through
		// the aggregate-above-join shape).
		return "Ordinality"
	case *optimizer.BitmapHeapScan:
		// PG's "Bitmap Heap Scan on <table>" (explain.c). goopg had NO arm for
		// this node at all, so a bitmap scan printed its Go type
		// (`*optimizer.BitmapHeapScan`) — which is also why a plan census
		// grepping for the PG label read zero even once bitmap scans were being
		// chosen.
		if p.Table == nil {
			// A nil Table is a producer bug, not a rendering case. Printing
			// the Go type here was the SAME defect this arm's comment above
			// describes — and it would also defeat the node-type coverage
			// test (P0-01), which cannot distinguish a %T returned by a
			// covered arm from the switch's fallthrough. Render the node
			// name PG uses and let the missing relation be conspicuous.
			return "Bitmap Heap Scan"
		}
		if dname := nm.disambiguatedName(n); dname != "" {
			return "Bitmap Heap Scan on " + dname
		}
		// P0-04: alias branch, same rule as the SeqScan arm.
		if p.Alias != "" && p.Alias != strings.ToLower(p.Table.Name) {
			return "Bitmap Heap Scan on " + explainRelName(p.Table, verbose) + " " + p.Alias
		}
		return "Bitmap Heap Scan on " + explainRelName(p.Table, verbose)
	case *optimizer.BitmapIndexScan:
		// PG names the INDEX here, not the table.
		if p.Index == nil {
			// See the BitmapHeapScan arm above: a %T from a COVERED arm is
			// indistinguishable from the fallthrough, so P0-01's coverage
			// test cannot pin this node while it can return a Go type.
			return "Bitmap Index Scan"
		}
		return "Bitmap Index Scan on " + explainIndexName(p.Index)
	case *optimizer.BitmapAnd:
		return "BitmapAnd"
	case *optimizer.BitmapOr:
		return "BitmapOr"
	case *optimizer.NestedLoopIndexJoin:
		// M0054-0006: this node is always a nested loop; S3
		// (0134-0001 P2) applies the same PG label rule as the
		// parameterised Join above. The inner IndexScan node renders
		// its own label (`Index Scan using <idx> on <table>`) below.
		return joinLabel("Nested Loop", p.Type)
	case *optimizer.Memoize:
		return "Memoize"
	case *optimizer.Merge:
		return fmt.Sprintf("Merge on %s", explainRelName(p.Target, verbose))
	case *optimizer.CTEDMLPrefix:
		return "CTE DML"
	case *optimizer.MaterializedCTEScan:
		return fmt.Sprintf("CTE %s", p.Name)
	case *optimizer.SetOp:
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
	case *optimizer.GenerateSeries:
		// M0134-0001 P2 S2: SRF FROM-clause scans render as PG's
		// `Function Scan on <func> [<alias>]` (explain.c T_FunctionScan
		// 1465-1466) instead of leaking the Go type via the %T fallback.
		return srfFunctionScanLabel("generate_series", p.Alias)
	case *optimizer.GenerateSubscripts:
		return srfFunctionScanLabel("generate_subscripts", p.Alias)
	case *optimizer.FromUnnest:
		return srfFunctionScanLabel("unnest", p.Alias)
	case *optimizer.UserSrfScan:
		// PG's get_func_name returns the BARE function name in both plain
		// and verbose modes; the schema is prepended only under VERBOSE
		// (explain.c:4490-4500: plain emits ` <objectname>`, verbose emits
		// ` <namespace>.<objectname>`). So plain output must not qualify.
		return srfFunctionScanLabel(p.Routine.Name, p.Alias)
	case *optimizer.ProjectSet:
		// PG renders a SELECT-list SRF as a bare `ProjectSet` label
		// (explain.c T_ProjectSet 1382-1384): no `on <funcname>`, no
		// Function Call detail. Its child renders beneath it.
		return "ProjectSet"

	// ---------------------------------------------------------------
	// take2 P0-01: the eighteen node types that had no arm and therefore
	// rendered their Go type name into user-visible EXPLAIN output. Verified
	// live before this change — `EXPLAIN SELECT * FROM regexp_matches(...)`
	// on goopg printed `*optimizer.FromRegexpMatches`, and a WITH RECURSIVE
	// query printed `*optimizer.RecursiveUnion`. Every label below was read
	// off PostgreSQL 18.3 (the :65432 reference cluster) rather than guessed;
	// where PG has no counterpart at all, the comment says so.
	// ---------------------------------------------------------------

	case *optimizer.RecursiveUnion:
		// PG: `Recursive Union` (explain.c T_RecursiveUnion). Confirmed on
		// the oracle with `WITH RECURSIVE t(n) AS (...) SELECT * FROM t`.
		return "Recursive Union"

	case *optimizer.WorkTableScan:
		// PG: `WorkTable Scan on <cte> <alias>` — the oracle prints
		// `WorkTable Scan on t t_1`. goopg's node carries NO name (the
		// struct is `{pos, schema}`), so the relation half cannot be
		// rendered and the bare node name is emitted. That missing name is a
		// real parity gap, not a rendering choice; it is recorded as such
		// rather than invented here.
		return "WorkTable Scan"

	case *optimizer.DistinctOn:
		// PG has no DistinctOn node: `SELECT DISTINCT ON (k) ...` plans as
		// `Unique` over a Sort, and the oracle confirms the top label is
		// `Unique`. goopg fuses the two into one node, so the sort is not
		// separately visible — a shape divergence the parity instrument
		// should COUNT, which it cannot do while the label is a Go type.
		return "Unique"

	case *optimizer.RowsFrom:
		// PG plans `ROWS FROM (f(), g())` as a single FunctionScan and names
		// the FIRST function: the oracle prints `Function Scan on
		// generate_series` for `ROWS FROM (generate_series(1,3),
		// generate_series(1,2))`.
		if name, _ := srfFunctionCallArgs(firstRowsFromFunc(p)); name != "" {
			return srfFunctionScanLabel(name, "")
		}
		return "Function Scan"

	case *optimizer.ScalarFuncScan:
		// A non-set-returning function in FROM. PG renders
		// `Function Scan on <name> <alias>` — confirmed with
		// `FROM nation n, abs(n.n_nationkey) a`. (A constant-foldable call
		// such as `FROM abs(-1)` collapses to `Result` on PG before it ever
		// reaches a scan node, which is a planning difference, not a label.)
		if fc, ok := p.Func.(*optimizer.FuncCall); ok && fc.Name != "" {
			return srfFunctionScanLabel(fc.Name, "")
		}
		return "Function Scan"

	// The remaining twelve are built-in table functions. PG renders every one
	// as `Function Scan on <funcname>`; each label below was confirmed
	// individually against the oracle where the function exists there.
	case *optimizer.FromRegexpMatches:
		return srfFunctionScanLabel("regexp_matches", "")
	case *optimizer.FromRegexpSplitToTable:
		return srfFunctionScanLabel("regexp_split_to_table", "")
	case *optimizer.PgAvailableWalSummaries:
		return srfFunctionScanLabel("pg_available_wal_summaries", "")
	case *optimizer.PgGetCatalogForeignKeys:
		return srfFunctionScanLabel("pg_get_catalog_foreign_keys", "")
	case *optimizer.PgGetPublicationTables:
		return srfFunctionScanLabel("pg_get_publication_tables", "")
	case *optimizer.PgGetSequenceData:
		return srfFunctionScanLabel("pg_get_sequence_data", "")
	case *optimizer.PgInputErrorInfo:
		return srfFunctionScanLabel("pg_input_error_info", "")
	case *optimizer.PgOptionsToTable:
		return srfFunctionScanLabel("pg_options_to_table", "")
	case *optimizer.PgPartitionTree:
		// One node serves two functions; the label must follow the field or
		// half the plans would name the wrong one.
		if p.FuncName != "" {
			return srfFunctionScanLabel(p.FuncName, "")
		}
		return srfFunctionScanLabel("pg_partition_tree", "")
	case *optimizer.PgSequenceParameters:
		return srfFunctionScanLabel("pg_sequence_parameters", "")
	case *optimizer.TSTokenType:
		return srfFunctionScanLabel("ts_token_type", "")
	case *optimizer.VerifyHeapam:
		// amcheck's function. Built in to goopg; an extension on PG, so the
		// reference cluster cannot render it — the label follows the same
		// `Function Scan on <name>` rule every other table function does.
		return srfFunctionScanLabel("verify_heapam", "")

	case *optimizer.Call:
		// PG has NO label for this: `EXPLAIN CALL p()` is a syntax error
		// upstream, because CALL is not in EXPLAIN's grammar. goopg accepts
		// it and reaches this renderer, which is itself a divergence — but
		// one to record, not to fix by printing a Go type name. The label is
		// goopg's own and is marked as such.
		return "Call"
	}
	return fmt.Sprintf("%T", n)
}

// isRecursiveSelfRef reports whether a CTEScan is the self-reference inside a
// WITH RECURSIVE term rather than an ordinary reference to a materialised CTE.
//
// internal/optimizer/with.go registers the CTE's body as a bare WorkTableScan
// while planning the recursive member, so every reference the recursive term
// makes to its own name resolves to a CTEScan wrapping that node. PG models the
// same thing as a distinct T_WorkTableScan plan node and renders it as a leaf,
// with no `CTE <name>` section — so goopg must not hoist a section for it
// either, or a WITH RECURSIVE plan prints the CTE header twice.
func isRecursiveSelfRef(p *optimizer.CTEScan) bool {
	if p == nil {
		return false
	}
	_, ok := p.Child.(*optimizer.WorkTableScan)
	return ok
}

// firstRowsFromFunc returns the first function node of a ROWS FROM list, or
// nil. PG names that function in the FunctionScan label (see the RowsFrom arm).
func firstRowsFromFunc(p *optimizer.RowsFrom) optimizer.Node {
	if len(p.Funcs) == 0 {
		return nil
	}
	return p.Funcs[0]
}

// setOpNodeName renders a SetOp's PG-style label — see the
// describePlan case above for the mapping rationale.
func setOpNodeName(p *optimizer.SetOp) string {
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
func setOpRendersAsAppend(p *optimizer.SetOp) bool {
	return p.All && p.Op == parser.SetOpUnion
}

// setOpCommandName mirrors explain.c's SETOPCMD_* text (the token PG
// appends after the node name, and its JSON "Command" property).
func setOpCommandName(p *optimizer.SetOp) string {
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
func setOpAppendBranches(p *optimizer.SetOp, out []optimizer.Node) []optimizer.Node {
	for _, side := range [...]optimizer.Node{p.Left, p.Right} {
		if inner, ok := side.(*optimizer.SetOp); ok && setOpRendersAsAppend(inner) {
			out = setOpAppendBranches(inner, out)
			continue
		}
		out = append(out, side)
	}
	return out
}

// joinLabel renders PG's EXPLAIN node name for a join, mirroring the
// jointype switch at postgres/src/backend/commands/explain.c:1712-1763
// (word: Left/Right/Full/Semi/Anti; Inner and Cross are never spelled)
// and the text-format rule at 1754-1758: a non-INNER jointype appends
// " <Type> Join"; an INNER appends " Join" unless the node is a
// NestLoop. CROSS JOIN is folded to JOIN_INNER during parse transform
// (parse_clause.c), so it renders through the Inner path — a bare
// "Nested Loop" for a nested-loop cross join. algo is the base node
// word ("Nested Loop" / "Hash" / "Merge").
func joinLabel(algo string, t optimizer.JoinType) string {
	switch t {
	case optimizer.JoinTypeInner, optimizer.JoinTypeCross:
		if algo == "Nested Loop" {
			return algo
		}
		return algo + " Join"
	case optimizer.JoinTypeLeft:
		return algo + " Left Join"
	case optimizer.JoinTypeRight:
		return algo + " Right Join"
	case optimizer.JoinTypeFull:
		return algo + " Full Join"
	case optimizer.JoinTypeSemi:
		return algo + " Semi Join"
	case optimizer.JoinTypeAnti:
		return algo + " Anti Join"
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
func planChildren(n optimizer.Node) []optimizer.Node {
	switch p := n.(type) {
	case *optimizer.Project:
		return []optimizer.Node{p.Child}
	case *optimizer.Result:
		// Childless Result (S6): the InitPlan hangs off the SubqueryExpr
		// target, not a child scan, so the node renders no children. A
		// Result-with-child (S6 Slice 3d const-arg rewrite) renders its inner
		// scan beneath it — the oracle's `-> Result / One-Time Filter: / ->
		// Seq Scan on tenk1` (aggregates.out:1198-1200).
		if p.Child != nil {
			return []optimizer.Node{p.Child}
		}
		return nil
	case *optimizer.Filter:
		return []optimizer.Node{p.Child}
	case *optimizer.Sort:
		return []optimizer.Node{p.Child}
	case *optimizer.Limit:
		return []optimizer.Node{p.Child}
	case *optimizer.Gather:
		return []optimizer.Node{p.Child}
	case *optimizer.GatherMerge:
		return []optimizer.Node{p.Child}
	case *optimizer.Distinct:
		return []optimizer.Node{p.Child}
	case *optimizer.Aggregate:
		return []optimizer.Node{p.Child}
	case *optimizer.WindowAgg:
		return []optimizer.Node{p.Child}
	case *optimizer.Join:
		return []optimizer.Node{p.Left, p.Right}
	case *optimizer.Insert:
		return []optimizer.Node{p.Source}
	case *optimizer.Update:
		out := make([]optimizer.Node, 0, 1+len(p.FromScans))
		out = append(out, p.Child)
		out = append(out, p.FromScans...)
		return out
	case *optimizer.Delete:
		out := make([]optimizer.Node, 0, 1+len(p.UsingScans))
		out = append(out, p.Child)
		out = append(out, p.UsingScans...)
		return out
	case *optimizer.CTEScan:
		// The recursive self-reference renders as a LEAF `WorkTable Scan`,
		// matching PG: descending would print the working table's anchor
		// schema as a phantom subtree beneath it.
		if isRecursiveSelfRef(p) {
			return nil
		}
		return []optimizer.Node{p.Child}
	case *optimizer.LockRows:
		return []optimizer.Node{p.Child}
	case *optimizer.OrdinalityWrap:
		return []optimizer.Node{p.Child}
	case *optimizer.NestedLoopIndexJoin:
		// M0054-0006: render outer driver and inner index probe. With an
		// S7 Memoize attached, the cache node renders between the join
		// and the probe (InnerMemo.Child aliases p.Inner).
		if p.InnerMemo != nil {
			return []optimizer.Node{p.Outer, p.InnerMemo}
		}
		return []optimizer.Node{p.Outer, p.Inner}
	case *optimizer.BitmapHeapScan:
		// The bitmap qual tree renders BENEATH the heap scan, as in PG.
		if p.Outer == nil {
			return nil
		}
		return []optimizer.Node{p.Outer}
	case *optimizer.BitmapAnd:
		return p.Inputs
	case *optimizer.BitmapOr:
		return p.Inputs
	case *optimizer.Memoize:
		return []optimizer.Node{p.Child}
	case *optimizer.Merge:
		return []optimizer.Node{p.Source}

	// take2 P0-01: four node types carried children that this walker never
	// visited, so their ENTIRE SUBTREE was absent from EXPLAIN — the same
	// class of defect as M0125-0037(i)'s missing SetOp arm above, which
	// truncated Q5/Q18/Q67 to a four-line plan. Measured before the fix:
	//
	//   goopg   SELECT DISTINCT ON (k) ...  ->  "Unique"          (one line)
	//   PG 18.3 same query                  ->  Unique / Sort / Seq Scan
	//
	// A truncated plan is worse for a parity census than a mislabelled one:
	// the missing rows read as agreement on everything they do not show.
	case *optimizer.DistinctOn:
		if p.Child == nil {
			return nil
		}
		return []optimizer.Node{p.Child}
	case *optimizer.RecursiveUnion:
		// PG renders the non-recursive term first, then the recursive one
		// (explain.c walks lefttree then righttree): `-> Result` above
		// `-> WorkTable Scan on t t_1`.
		out := make([]optimizer.Node, 0, 2)
		if p.Anchor != nil {
			out = append(out, p.Anchor)
		}
		if p.Recursive != nil {
			out = append(out, p.Recursive)
		}
		return out
	case *optimizer.RowsFrom:
		// PG plans ROWS FROM as ONE FunctionScan with several function
		// expressions, so it has no children to render there. goopg models
		// each entry as its own node; walking them keeps the tree honest
		// rather than silently dropping every entry after the label.
		return p.Funcs
	case *optimizer.Copy:
		if p.Query == nil {
			return nil
		}
		return []optimizer.Node{p.Query}
	case *optimizer.CTEDMLPrefix:
		out := make([]optimizer.Node, 0, len(p.DMls)+1)
		out = append(out, p.DMls...)
		out = append(out, p.Body)
		return out
	case *optimizer.SetOp:
		// M0125-0037(i): the missing case that truncated Q5/Q18/Q67 to a
		// four-line plan. A UNION ALL chain collapses into one Append's
		// child list the way PG plans it; every other set operation is a
		// binary HashSetOp whose two inputs render directly beneath it
		// (verified against PG 18.3: `HashSetOp Intersect` carries two
		// Seq Scan children, not an Append).
		if setOpRendersAsAppend(p) {
			return setOpAppendBranches(p, nil)
		}
		return []optimizer.Node{p.Left, p.Right}
	case *optimizer.ProjectSet:
		// M0134-0001 P2 S2: a SELECT-list SRF's child plan renders
		// beneath the bare `ProjectSet` label (mirrors PG, which walks
		// ProjectSet's single child). Without this case the whole subtree
		// was invisible after the label.
		return []optimizer.Node{p.Child}
	}
	return nil
}

// seqScanLabel returns the node label for a SeqScan: "Sample Scan" when the
// scan carries a TABLESAMPLE clause, "Seq Scan" otherwise. Upstream reaches the
// same two strings from two different plan node tags (T_SampleScan /
// T_SeqScan, explain.c:1189-1195); goopg has one scan node, so the label is a
// property of the node rather than of its type. M0134-0175.
//
// Note the "Parallel " prefix composes with either — upstream prints
// "Parallel Seq Scan" and would print "Parallel Sample Scan" identically,
// since the prefix is applied to whichever label the tag selected.
func seqScanLabel(p *optimizer.SeqScan) string {
	if p.TableSample != nil {
		return "Sample Scan"
	}
	return "Seq Scan"
}

// formatTableSample renders the `Sampling:` line's payload —
// `<method> (<args>) [REPEATABLE (<seed>)]`, mirroring show_tablesample
// (explain.c:3021-3054). M0134-0175.
//
// The argument deparse is where this can drift from the oracle, and the reason
// is worth stating: upstream COERCES each argument to the method's declared
// parameter type (float4) during parse analysis, so a literal `50` reaches
// EXPLAIN as a float4 Const and deparses as `'50'::real`, and `10*2` reaches it
// already constant-folded as `'20'::real`. goopg does not coerce the argument,
// so the same deparse has to be synthesised here: a numeric constant is
// re-rendered in upstream's coerced form, and anything else falls through to
// the ordinary expression printer.
func formatTableSample(ts *optimizer.TableSampleSpec, reg *subPlanReg, qualify bool) string {
	args := make([]string, 0, len(ts.Args))
	for _, a := range ts.Args {
		args = append(args, formatSampleArg(a, reg, qualify, "real"))
	}
	out := ts.Method + " (" + strings.Join(args, ", ") + ")"
	if ts.Repeatable != nil {
		out += " REPEATABLE (" + formatSampleArg(ts.Repeatable, reg, qualify, "double precision") + ")"
	}
	return out
}

// formatSampleArg deparses one TABLESAMPLE argument in upstream's coerced
// form. A constant that evaluates to a number prints as `'<v>'::<typ>`, which
// is what a Const of that type deparses to in ruleutils.c; everything else
// (a Var from a LATERAL outer reference, say) prints through the ordinary
// expression printer, matching upstream's `Sampling: bernoulli
// ("*VALUES*".column1)`.
func formatSampleArg(e optimizer.Expr, reg *subPlanReg, qualify bool, typ string) string {
	if v, ok := constSampleValue(e); ok {
		return "'" + v + "'::" + typ
	}
	return formatExprQual(e, reg, qualify)
}

// constSampleValue folds a TABLESAMPLE argument to its numeric value when the
// expression is a constant one, returning it in PG's float4 output form.
//
// This exists because upstream folds these constants during parse analysis
// (eval_const_expressions, run before EXPLAIN ever sees the plan), so
// `TABLESAMPLE SYSTEM (10*2)` reaches show_tablesample as a single Const and
// prints `'20'::real`. goopg keeps the BinaryOp tree, so the fold has to happen
// at print time. Only the four arithmetic operators appear in the regress
// corpus and only they are folded here; anything else returns ok=false and
// falls back to the ordinary expression deparse, which is the safe direction —
// a wrong EXPLAIN string is worse than a differently-shaped one. M0134-0175.
func constSampleValue(e optimizer.Expr) (string, bool) {
	f, ok := constSampleFloat(e)
	if !ok {
		return "", false
	}
	return PGFloatOut(f, 32), true
}

func constSampleFloat(e optimizer.Expr) (float64, bool) {
	switch v := e.(type) {
	case *optimizer.IntegerConst:
		return float64(v.Value), true
	case *optimizer.NumericConst:
		f, err := strconv.ParseFloat(v.Value, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case *optimizer.BinaryOp:
		l, ok := constSampleFloat(v.Left)
		if !ok {
			return 0, false
		}
		r, ok := constSampleFloat(v.Right)
		if !ok {
			return 0, false
		}
		switch v.Op {
		case parser.OpAdd:
			return l + r, true
		case parser.OpSub:
			return l - r, true
		case parser.OpMul:
			return l * r, true
		case parser.OpDiv:
			if r == 0 {
				return 0, false
			}
			return l / r, true
		}
	}
	return 0, false
}
