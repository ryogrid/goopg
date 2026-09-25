# M0145-0008m: functionally dependent passthroughs are known before the grouping election

Status: **LANDED 2026-09-25** (`356cb8b11`). Task: `.ralph/fix_plan.md`
M0145-0008m (Kind: impl, Parent: M0145-0008h). This was a wrong-results
defect, owner-placed third in banner item 2a. Evidence:
`analysis/m0145/m0145-0008m/`.

## Defect

`SELECT sum(c1), c2 FROM agg_sort_order GROUP BY c1`, with `c1` the primary
key, returned `1|`, `2|`, … with `c2` NULL. PG returns the `c2` values. The
same happened with `ORDER BY c2` and with `c2` alone in the target list.

## Cause

`c2` is legal because the primary key determines it. goopg carries such a
column as an Aggregate **passthrough**: its value is read from the first input
row of each group. That passthrough was appended lazily, during target
resolution (`resolveExprAfterAggregate`, `internal/optimizer/planner.go`),
which runs **after** the grouping election (`createGroupingPaths`).

The election could therefore pick `indexOrderedAggInput`'s Index Only Scan on
the primary key. Its coverage check reads `aggNode.Passthrough`, which was
still empty, and the scan emits only `c1`. The executor then evaluated `c2`
against a one-column row; the error ("column ref c2/1 out of MaterializedSlot
range 1") was swallowed and stored as NULL.

## What PG does

`grouping_planner` computes the final target and the grouping target before
`create_grouping_paths`, so every column an upper node needs is known when
the grouping paths are built. No candidate can omit one.

## Change

`prefetchFuncDepPassthroughs` (`internal/optimizer/funcdep_prefetch.go`) runs
right before `createGroupingPaths`. It resolves every bare column that the
target list, ORDER BY and DISTINCT ON read above the aggregate, using the same
`resolveExprAfterAggregate`. That call's only side effect is the passthrough
append, so the later resolution finds each column already added
(`agg.funcDepCols`). Results and errors are discarded; the real pass reports
them in its own order.

The walk skips three kinds of node, each for a reason:
- **Aggregate-call arguments:** they read the aggregate's input, not a
  passthrough.
- **Nested SELECTs:** they are planned in their own scope, and re-planning
  them would allocate a second set of scan identities.
- **Any sub-expression that is itself a grouping expression:** it resolves
  whole to its group key. The first candidate descended into TPC-DS Q23's
  `substr(i_item_desc, 1, 30)` and added `i_item_desc` as a needless
  passthrough, widening a CTE HashAggregate from 48 to 80. The fire set
  caught it.

## Tests

- `TestPrefetchFuncDepPassthroughsBeforeElection` builds the aggregate stage
  as `planSelect` does. It asserts the passthrough list the election will see
  in each case:
  - a target column: added;
  - an ORDER BY column: added;
  - an aggregate argument: none;
  - a composite expression: both columns;
  - a grouping expression: none.
- `TestFuncDepPassthroughSurvivesGroupingElection` asserts that every
  passthrough reads a column the elected input emits. That is an invariant
  test: the in-memory catalog cannot make the index-ordered input win, so the
  test does not reproduce the defect on its own.
- The executor fixture cannot `SET` planner GUCs, so the end-to-end pin is
  the values probe below.

## Verified

- **Probe** (`analysis/m0145/m0145-0008h/probe.txt`, a PG 18.3 capture): every
  row matches PG. The first failing query returns `100|100, 99|99, 98|98`
  again.
- **Full `TestPort_RegressSuite`:** PASS.
- **Gates:**
  - units: PASS.
  - `tpch-spotcheck`: PASS.
  - acceptance arm: 24 MATCH.
  - `tpcds-sf025`: 96 PASS.
  - fire-set (SF0.25 + SF1): PASS, fires none.
  - pgbench smoke: PASS.

Movement: none. This is a wrong-results fix with no corpus plan change.

## Not ported (ledgered)

The executor still turns a failed passthrough evaluation into NULL; PG would
raise an error. Any future planner mistake of the same kind would again be
silent wrong results rather than an error.
