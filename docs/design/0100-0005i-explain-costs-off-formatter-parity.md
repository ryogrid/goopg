# 0100-0005i — EXPLAIN (COSTS OFF) formatter parity

Milestone: M0100-0005i (sub-milestone of M0100 — RC isolation E2E)
Loop: 2026-05-15 (loop 25)
Status: implemented

## Problem

PostgreSQL's `EXPLAIN (COSTS OFF) ...` renders a compact TEXT plan
with bare node labels and PG-style detail lines (`Sort Key:`,
`Index Cond:`, `Filter:`). Several isolation specs — the first
hit being `drop-index-concurrently-1` — compare their step output
against this format byte-for-byte. Pre-loop-25 goopg's renderer
diverged in four ways:

1. `(rows=N)` was appended to every node label unconditionally,
   even under `COSTS OFF`.
2. `Project` plan nodes surfaced as `Projection (rows=N)` lines.
   Upstream PG has no "Projection" plan node — projection is part
   of the parent / scan label, not a separate row.
3. `Sort` rendered with no `Sort Key:` detail line.
4. `IndexScan` / `SeqScan` rendered with no
   `Index Cond:` / `Filter:` detail line.

These all originate in `internal/executor/operators_explain.go`
(the `walkPlan` / `describePlan` pair) — the renderer never had a
notion of "PG-style detail lines" and never gated `(rows=N)` on
the `Costs` option.

## Fix

`internal/executor/operators_explain.go::walkPlan` (and the
analyze variant `walkPlanAnalyze`) are split into a thin entry
function and a `*Filtered` driver that:

- **Skips wrapper nodes that PG doesn't surface as plan nodes.**
  `Project` is unconditionally folded into the child (descend at
  the same depth, do not emit a label). `Filter` is folded into
  the child too, but its `Predicate` is carried down via a new
  `attachedFilter planner.Expr` parameter so the next emitted
  scan node renders it as `Filter:` detail.

- **Gates `(rows=N)` on `opts.Costs`.** The `Costs` option
  defaults to `true` (PG semantics); `COSTS OFF` flips it to
  `false`. The pre-existing branch that appended
  `(rows=N)` when `EstimateRows > 0` is now wrapped in
  `if opts.Costs { ... }`. The `ANALYZE` per-node `actual
  rows=N loops=L (time=...)` instrumentation is independent and
  continues to render unconditionally.

- **Emits PG-style detail lines** via a new shared helper
  `emitNodeDetailLines(n, indent, rows, attachedFilter)`. The
  helper recognises three node kinds:
  - `*planner.Sort` → `Sort Key: <expr_csv>` (with `DESC` suffix
    on descending keys).
  - `*planner.IndexScan` → `Index Cond: (<col> = <key>)` or the
    multi-column / range variant via `formatIndexCond`.
  - `*planner.SeqScan` + attached Filter → `Filter: (<pred>)`.

  When the wrapped child is something other than a scan (e.g.
  `Filter(Aggregate(...))`), the carried predicate falls through
  to a generic `Filter:` line on whatever node we render. This is
  defensive — it keeps the predicate visible rather than silently
  dropping it.

- **Renders expressions in PG style** via a new
  `formatExprPG(planner.Expr) string` recursive renderer:
  `ColumnRef.Name`, integer / numeric / string literals (with
  SQL-escaped quotes for strings), `BinaryOp` infix with the
  operator's `String()` form, `UnaryOp`, `CastExpr` (renders the
  operand — PG omits explicit casts in EXPLAIN), `FuncCall`,
  `ParamRef` (`$N`), and `BooleanConst` / `NullConst`. Unhandled
  kinds fall back to `<%T>` so a future regression on an
  un-covered expression kind is visible without crashing.

The detail-line indent is `len(prefix) + 2` spaces — i.e. the
content column of the node label plus two — matching upstream
PG's `Sort Key:` / `Index Cond:` / `Filter:` indent convention.
For a depth-0 node with no `->  ` prefix the detail line sits at
column 2; for a depth-1 node behind `"  ->  "` (6 chars) the
detail sits at column 8.

## Verification

Unit tests in `internal/executor/explain_costs_off_test.go`:

- `TestExplainCostsOffSuppressesRowsSuffix` — `EXPLAIN (COSTS
  OFF) SELECT * FROM t WHERE data = 34` produces no `(rows=`
  substring.
- `TestExplainCostsOnAnalyzeIncludesActualRows` — converse: the
  `actual rows=N loops=L` block under `EXPLAIN (ANALYZE)` is
  still emitted (`rows=` substring present) so the previous test
  can't pass by always suppressing.
- `TestExplainSuppressesProjectionWrapper` — `EXPLAIN (COSTS
  OFF) SELECT * FROM t WHERE data = 34` output contains
  `Seq Scan on t` but never the substring `Projection`.
- `TestExplainEmitsFilterDetailUnderSeqScan` — same query
  surfaces `Filter: (data = 34)` indented under the SeqScan.
  Also pins that the `Filter` wrapper itself does not leak as a
  node label (no line starts with `Filter` other than
  `Filter:`).
- `TestExplainEmitsSortKeyDetail` — `EXPLAIN (COSTS OFF) SELECT
  * FROM t ORDER BY id, data` surfaces `Sort Key: id, data`.
- `TestExplainEmitsIndexCondDetail` — when the planner picks an
  IndexScan (cost-driven; the test skips if it doesn't), the
  scan label is followed by `Index Cond: (data = 34)`.

`drop-index-concurrently-1` advances substantially past the
loop-24 baseline. Remaining isolation-spec diffs against this
spec are tracked separately and are unrelated to the formatter:

1. **`Sort Key: id, data` vs expected `Sort Key: id`** — PG's
   planner eliminates constant sort keys (the `data` column is
   equality-bound to `34`, so it cannot vary across the sorted
   rows). Goopg's planner does not yet perform this
   sort-key-redundancy elimination. Tracked as a follow-up.
2. **`Seq Scan` vs `Index Scan` choice on `explains` step** —
   the spec re-enables `enable_seqscan` after the explainer
   already cached the index plan; PG picks SeqScan, goopg picks
   IndexScan. Cost-model / plan-cache interaction unrelated to
   the formatter.
3. **`<waiting ...>` annotation on DROP INDEX CONCURRENTLY** —
   `DROP INDEX CONCURRENTLY` does not wait for in-progress
   transactions on the indexed table. This is a real runtime
   gap, not a formatting one.

`go test -race ./internal/executor/ ./internal/server/
./internal/planner/ ./internal/parser/` PASS post-fix.

## Files touched

- `internal/executor/operators_explain.go` — implementation
- `internal/executor/explain_costs_off_test.go` — regression pins
- `docs/design/0100-0005i-explain-costs-off-formatter-parity.md` —
  this doc
- `docs/design/README.md` — index entry
- `.ralph/fix_plan.md` — progress entry under M0100-0005
