# M0146-0005dn — a rank-bounded subquery filter becomes a WindowAgg Run Condition

Status: done (2026-10-03). Parent: M0146-0005 (banner item 3).

## The PG behaviour

`set_subquery_pathlist` hands each outer qual on a subquery's window
function output to `check_and_push_window_quals` and
`find_window_run_conditions` (allpaths.c). When the function is monotonic,
answering `SupportRequestWFuncMonotonic` (rank, row_number, dense_rank,
percent_rank, cume_dist, ntile, count), and the qual compares it to a
constant in the matching direction, the qual becomes the WindowAgg's
`runCondition`:

- `<`, `<=`, `>`, `>=` on the right side: `keep_original = false`, so the
  qual leaves the subquery scan.
- `=`: becomes `<=` (`>=` for a decreasing function, mirrored when the
  window function is on the right) with `keep_original = true`. A function
  that is both increasing and decreasing keeps `=` and drops the original.

EXPLAIN prints `Run Condition: (rank() OVER w1 < 11)` under the WindowAgg.
At run time (nodeWindowAgg.c) the first row that fails the condition ends
the scan for a top-level WindowAgg without PARTITION BY (WINDOWAGG_DONE).
With PARTITION BY, the rest of the partition is skipped
(WINDOWAGG_PASSTHROUGH_STRICT). A lower WindowAgg instead passes rows
through with NULL results, and the top window filters them.

M0146-0005i ported only the estimate side: dropped quals contribute no
selectivity. goopg kept every such qual as a Filter.

## What landed

- **`WindowAgg.RunCondition`** (plan.go): a qual over the node's own
  output.
- **`pushWindowRunConditions`** (window_runcondition.go) runs at `Plan()`'s
  tail, before the trivial-SubqueryScan strip, matching PG's
  subquery_planner-then-setrefs order. For each Filter whose conjunct
  compares a function of the **first** WindowAgg below it (through
  SubqueryScan, identity Project columns and Sorts) to a constant:
  - the `drop` cases move onto `RunCondition`;
  - the `=` case adds its `<=` / `>=` run condition and stays on the
    Filter;
  - an emptied Filter disappears.

  A read-only scan first decides whether anything qualifies. Rebuilding
  unconditionally copied every node and split shared subtrees: a CTE body
  read twice got two copies, whose scans then shared an RTID, which the
  RTID-uniqueness test caught. Duplicate run conditions are skipped,
  since `Plan()` can reach a subquery's tree twice.
- **Executor** (`windowOp.Next`): a row whose run condition is false or
  NULL ends the scan without PARTITION BY, or skips the rest of its
  partition otherwise. The rows are those the Filter used to keep, because
  the function is monotonic.
- **EXPLAIN:** `Run Condition: (…)` after `Window:`, deparsed by
  `windowKeyText`. The redundant outer parentheses of a sort key are
  trimmed.

## Verification

- Scratch probes against PG 18.3, identical apart from the pre-existing
  missing `rc.` qualifier on Window/Sort lines. Cases: `<` without a
  partition; `<=` with PARTITION BY; a constant on the left plus another
  qual; `=` kept as a Filter with a `<=` run condition; `sum()` (not
  monotonic) untouched. Values are identical.
- `TestWindowRunCondition` pins the plans and checks values against the
  same queries with an opaque bound (`rk + 0`). The table includes NULL
  values, and the cases cover the stop and skip-partition arms.
- Regress A/B: `window` 4348→4275, as its run-condition tests now print
  PG's `Run Condition:` lines where goopg printed a Filter. No row changed;
  five other suites are byte-identical.
- TPC-DS: Q44's and Q67's run conditions match PG at both scales; values
  PASS; aligned lines +1 / +1. Categories are flat (the classifier does not
  separate run conditions from filters).
- Gates: units, spotcheck, sweep 96/96, TPC-H arm, ea-ratchet, fire set.

## Residuals (ledgered)

- A function of a lower WindowAgg in the same query level gets no run
  condition. PG would use non-strict pass-through plus the top window's
  `topqual`.
- The executor still computes every window value before scanning; PG
  stops spooling at the run condition. Results are the same; only the
  work saved differs.
- The `rc.` qualification of Window / Sort key lines in single-table
  subqueries is a separate rendering gap.
