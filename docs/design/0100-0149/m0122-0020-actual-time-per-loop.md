# M0122-0020 — EXPLAIN ANALYZE: actual time per loop, timed inside the node

Status: landed 2026-10-27 (loop #185). Area: `internal/executor` (EXPLAIN
ANALYZE instrumentation and renderers). Plan-neutral.

## Symptom

TPC-DS Q14 on the SF0.25 clone printed

    Index Scan using store_sales_pkey … (actual time=0.003..1857.725 rows=42.54 loops=16173)

`rows=` was the per-loop average (M0142-0004a), but `time=` was the total
over all 16173 loops. PG's `ExplainNode` (explain.c:1832-1835, and the
per-worker block at 1892-1901) divides `instrument->startup`,
`instrument->total` and `instrument->ntuples` by `nloops` alike.

## Second defect found while testing: the stopwatch

Once divided, the numbers still broke PG's nested-stopwatch invariant: a
child's total must not exceed its parent's. A child's total is the time
spent inside its own calls, and those calls run inside the parent's calls.
For the nlij fixture the outer Seq Scan printed 0.235 ms under a Nested
Loop of 0.171 ms. `instrumentedOp` had:

- **Timed the gap between returns.** It kept `rowDeltaT`, the time of the
  previous return, and added `now - rowDeltaT` on every returned row. That
  span also covers the parent's own work between two calls. For the outer
  side of a nested loop, that work is the whole inner probe.
- **Skipped the EOF call.** The call that returns EOF added nothing, so a
  loop that returned no rows contributed zero time.
- **Kept one loop's startup.** `startupNs` was assigned, not summed, so it
  held the startup of the last loop that produced a row. PG's
  `InstrEndLoop` adds each cycle's `firsttuple` into `startup`
  (instrument.c:154).

PG's `InstrStartNode`/`InstrStopNode` pair times each `ExecProcNode` call.
The time accumulates into `counter` for the cycle, and `firsttuple` is the
counter value at the cycle's first tuple (instrument.c:117).

## Change

- `instrument.go`:
  - `Open` times the wrapped `Open`. goopg operators do in `Open` the work
    PG's first `ExecProcNode` does: hash build, sort, materialise.
  - `Next` times the wrapped call, every call including the EOF one. Both
    durations go into the per-loop `loopNs` (PG `counter`) and into
    `totalNs`.
  - At the loop's first row, `startupNs += loopNs`.
  - `openTime` and `rowDeltaT` are gone.
- `operators_explain.go`: a new `msPerLoop(ns, loops)` is used by all four
  `actual time` renderers:
  - the text node label;
  - the Hash child line;
  - the per-worker `Worker N:` line;
  - the JSON/YAML/XML `Actual Startup Time` / `Actual Total Time`.

  As with `rowsPerLoop`, `loops <= 0` returns the raw value.

  The buffer I/O timings (`I/O Timings`, `Shared I/O Read Time`) stay as
  totals, as in PG.

Timing on now reads the clock twice per call instead of once, the same as
PG. `TIMING OFF` reads no clock at all, as before.

## Evidence

`TestExplainAnalyzeActualTimePerLoop` runs on the nlij fixture: 100 outer
rows, with an index probe re-opened 100 times. It parses `FORMAT JSON` and
asserts that the probe's per-loop total × 100 ≤ the Nested Loop's total,
and that per-loop startup ≤ per-loop total. Example readings:

- after: NL 0.238 ms, probe 0.0015 ms × 100, outer Seq Scan 0.047 ms;
- before: the probe's cumulative print could exceed the NL by about 100×,
  and the outer scan exceeded the NL.

Gates: units; tpch-spotcheck PASS; tpch-acceptance-arm 24 MATCH; sf025 sweep
PASS with plans 99/99 same; TPC-H census 22/22 with every category 0;
RegressSuite per-case diff counts identical to the M0122-0017 baseline
except the known plpgsql flapper (1653→1644); isolation suite PASS. Fireset
is out of scope (executor only).

## Residual (ledgered)

FORMAT JSON prints the whole node label as `"Node Type"` (e.g. `"Seq Scan on
nlij_outer o"`, `"Index Only Scan using nlij_inner_k on nlij_inner i"`).
PG splits it into `Node Type` / `Relation Name` / `Alias` / `Index Name` /
`Scan Direction`, and it prints `Rows Removed by Filter` only on nodes that
have a filter. This is `planToJSONNamed` (`describePlan`), and it is
outside this task's scope.
