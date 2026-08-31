# 0018-0003 — EXPLAIN ANALYZE Runtime Instrumentation

**Status:** accepted (Stage B)
**Milestone:** [0018 — EXPLAIN / EXPLAIN ANALYZE Support](../../milestones/0018-explain-and-explain-analyze.md)
**Spans seam:** per-operator instrumentation wrapper, ANALYZE execution
path in `explainOp`, TEXT/JSON rendering of actual-rows / loops / timing.
**Cross-links:**
[0018-0001](0018-0001-explain-parser-options-and-ast.md) (parser AST),
[0018-0002](0018-0002-static-plan-rendering-and-output-contract.md)
(static rendering this slice extends).

## Context

M0018-0001 + M0018-0002 added the EXPLAIN option plumbing and TEXT
/ JSON rendering for the static (non-ANALYZE) options. ANALYZE is
currently rejected with `0A000 Stage B`. This slice closes the
ANALYZE path: the executor wraps each Operator in an instrumentation
shim that tracks rows emitted, loops (Open call count), and
wall-clock timing; the EXPLAIN renderer pulls those counters into
each per-node label.

## Instrumentation wrapper

`internal/executor/instrument.go` defines a single shim:

```go
type instrumentedOp struct {
    inner     Operator
    plan      planner.Node
    timing    bool
    rowsOut   int64
    loops     int64
    timeOpen  time.Time
    totalDur  time.Duration  // accumulated across all Open()->Close() cycles
}
```

`Open` increments `loops`, snapshots `timeOpen` (if `timing`).
`Next` increments `rowsOut` per non-EOF row and records the
per-row delta into `totalDur` when `timing`. `Close` finalises
the duration.

`Schema()` delegates to `inner.Schema()`. `RowsAffected()` (for DML
shims) delegates via type-assertion so `INSERT N` still reaches the
wire layer when wrapped.

The wrapper carries the source `planner.Node` so the renderer can
look up its counters by Node identity (a `map[planner.Node]*instrumentedOp`
populated by Build's recursive wrapping).

## Build-side wrapping

`Build` gains a sibling `BuildInstrumented(plan, opts) (Operator,
map[planner.Node]*instrumentedOp, error)`. It calls Build, then
walks the plan tree top-down wrapping each child site at the
parent's recursive `Build` call site — implemented by mirroring
the existing dispatch and wrapping its Operator results before
returning.

For Stage B's scope I take the shorter path: a post-build walk
that wraps every node. Implementation detail: the existing Build
already produces a fully connected tree of Operators that hold
references to their children; rewiring to insert wrappers at each
level requires reflection-light access to those child fields
(`projectOp.child`, `filterOp.child`, etc.). Easier: have
`BuildInstrumented` reuse `Build`'s structure but at each branch
inject the wrap before returning. That's what this slice does.

## ANALYZE execution path

`explainOp.Open` branches on `Options.Analyze`:

- `false` (default): existing static path.
- `true`: build the inner plan with `BuildInstrumented`, run it
  to completion (drain Next() until EOF, then Close), and feed
  the captured per-node counters into the renderer. The result
  rows go nowhere — ANALYZE's job is the timing report, not the
  data — but Open / Next / Close run unconditionally so timers
  fire.

After the inner statement drains, the renderer (TEXT or JSON) walks
the plan tree the same way M0018-0002's static path does, but each
node label gains an `(actual time=X..Y rows=R loops=L)` suffix —
matching upstream's textual shape.

## TIMING ON / OFF

`Options.Timing` defaults to true under ANALYZE (matches upstream's
`EXPLAIN (ANALYZE) ...` default). When `Options.Timing == false`
explicitly, the wrapper skips the `time.Now()` calls and the
renderer emits `(actual rows=R loops=L)` without the time bracket.
`Options.Summary` (defaulting to true under ANALYZE) controls the
`Planning Time` / `Execution Time` footer block — implemented via
two extra rows appended to the EXPLAIN output.

## TEXT shape

```
Projection (actual time=0.005..0.012 rows=8 loops=1)
  ->  Seq Scan on pgbench_accounts (actual time=0.001..0.010 rows=8 loops=1)

Planning Time: 0.123 ms
Execution Time: 0.456 ms
```

Pre-existing `(rows=N)` planner-estimate suffix continues to render
under ANALYZE alongside the actual numbers, matching upstream.

## JSON shape

```json
{
  "Node Type": "Seq Scan on pgbench_accounts",
  "Plan Rows": 1000,
  "Actual Rows": 8,
  "Actual Loops": 1,
  "Actual Total Time": 0.012,
  "Actual Startup Time": 0.001,
  "Plans": [...]
}
```

Footer (when Summary is true):

```json
{ ..., "Planning Time": 0.123, "Execution Time": 0.456 }
```

Top-level merge: the array's single object grows the timing
fields. Mirrors upstream's `Planning` / `Execution` annotations.

## Out of scope

- BUFFERS / WAL / SETTINGS counter rendering — M0018-0004.
- Loop counters > 1: nested loops would call Open multiple
  times per outer row. v0's executor doesn't yet have an
  operator that re-Opens its child within a single statement
  (no nested-loop join with rescan, no parameterised subquery
  re-execution); `loops` is therefore always 1 in practice.
  The counter is wired anyway so upstream-style nested-loop
  joins surface correct values when they land.
- Parallel-worker stats — out of milestone scope.
- Per-row / sample-based timing (upstream's deferred-sampling
  optimization) — measure every row in v0; profile if needed.

## Tests

- `TestExplainAnalyzeRunsInnerAndReportsRows` — `EXPLAIN ANALYZE
  SELECT * FROM t` over a 5-row table reports `actual rows=5`.
- `TestExplainAnalyzeIncludesPlanningExecutionTime` — TEXT output
  includes `Planning Time:` and `Execution Time:` footer lines.
- `TestExplainAnalyzeJSONIncludesActualFields` — JSON output's
  root object has `Actual Rows` / `Actual Loops` keys.
- `TestExplainAnalyzeTimingOffSuppressesBracket` — `EXPLAIN
  (ANALYZE, TIMING off) ...` text output includes `actual rows=N
  loops=N` but NOT `time=`.
- `TestExplainAnalyzeOnSelectOneRowsAccurate` — `EXPLAIN ANALYZE
  SELECT 1` reports `actual rows=1 loops=1`.
- `TestExplainAnalyzeRejectsWriteStatements` — defer to a later
  slice; for v0 ANALYZE on INSERT/UPDATE/DELETE is supported
  but their side effects fire (matches upstream). The test is
  a NOTE in the design doc rather than an assertion; v0 callers
  are warned via the milestone documentation.
