# DML CTE Support (WITH MERGE RETURNING) — M0100-0005 Loop-20

**Status:** accepted  
**Filed:** 2026-05-20  
**Milestone:** M0100-0005

## Problem

The `merge-match-recheck` isolation spec uses a data-modifying CTE:

```sql
WITH t AS (
    MERGE INTO target_tg t
    USING (SELECT 1 as key) s
    ON s.key = t.key
    WHEN MATCHED AND balance < 100 THEN UPDATE SET balance = balance * 2, val = t.val || ' when1'
    WHEN MATCHED AND balance < 200 THEN UPDATE SET balance = balance * 4, val = t.val || ' when2'
    WHEN MATCHED AND balance < 300 THEN UPDATE SET balance = balance * 8, val = t.val || ' when3'
    RETURNING t.*
)
SELECT * FROM t;
```

The parser rejected this with `"CTE body must be a SELECT"`, causing all permutations using
`merge_bal_tg` to fail at parse time.

Additionally, `mergeApplyUpdate` lacked the moved-partition sentinel check that `updateOp.Next()`
already had (M0100-0005n), causing MERGE on a partitioned table that was concurrently
cross-partition-updated to silently skip the row instead of raising the upstream error.

## Solution

### 1. Parser — allow DML bodies in CTEs

`internal/parser/ast.go`: `CommonTableExpr` gains a `DMLBody parser.Stmt` field alongside the
existing `Query *SelectStmt`. The parser stores INSERT/UPDATE/DELETE/MERGE bodies in `DMLBody`
and SELECT bodies in `Query`. The Stage-A restriction is lifted.

### 2. Analyzer — skip DML CTE bodies

`internal/analyzer/analyzer.go::analyzeWith` skips `analyzeSelectWithParent` for CTEs with
`DMLBody != nil` and registers an empty `*catalog.Table` so the outer query knows the name exists.

### 3. Planner — plan DML CTE bodies

`internal/planner/with.go`:
- `plannedCTE` gains `isDML bool`
- `preplanWithClause` now returns `(func(), []dmlCTEPlan, error)`; DML CTEs are planned via new
  `planDMLCTEBody` dispatch (routes INSERT/UPDATE/DELETE/MERGE to their respective planners)
- `wrapDMLCTEPrefix` wraps the outer plan in `CTEDMLPrefix` when DML CTEs exist

`internal/planner/plan.go`:
- `CTEDMLPrefix {Names []string; DMls []Node; Body Node}` — executes DML CTEs in order then runs outer query
- `MaterializedCTEScan {Name, Alias, schema}` — reads from pre-materialized rows in `ctx.MaterializedCTEs`
- `Merge.Returning []Expr; ReturningSchema Schema` — RETURNING support for MERGE

### 4. MERGE RETURNING

`internal/planner/planner.go::planMerge`: resolves the RETURNING clause into `Merge.Returning /
ReturningSchema`.

`internal/executor/operators_merge.go::mergeOp`:
- `retRows [][]Datum; retIdx int` fields for collected RETURNING rows
- `collectReturningRow(row)` evaluates RETURNING expressions after each matched UPDATE/DELETE/INSERT
- `Next()` yields RETURNING rows one at a time after the merge logic completes
- `Schema()` returns `plan.ReturningSchema`

### 5. Executor — DML CTE execution

`internal/executor/context.go`: `MaterializedCTEs map[string][][]Datum` added to `Context`.

`internal/executor/operators_cte_dml.go` (new):
- `cteDMLPrefixOp.Open()`: executes each DML CTE, collects RETURNING rows into
  `ctx.MaterializedCTEs[name]`, then builds and opens the outer query plan
- `materializedCTEScanOp.Next()`: reads from `ctx.MaterializedCTEs[name]`

### 6. MERGE moved-partition check

`internal/executor/operators_merge.go::mergeApplyUpdate`: added `epqSlotMovedToAnotherPartition`
check both immediately after `epqWait` and as a fallback after `epqFollowChain` returns not-found.
Mirrors the same sentinel check in `updateOp.Next()` (M0100-0005n).

## Result

- Parser no longer rejects `WITH t AS (MERGE ... RETURNING ...) SELECT * FROM t`
- MERGE RETURNING materializes output rows available to the outer `SELECT * FROM t`
- `merge_bal_tg` permutations advance past parse error and execute the MERGE correctly
- `mergeOp.applyMod` correctly surfaces the moved-partition error for concurrent cross-partition updates

**MergeMatchRecheck**: 489/503 → 501/503 lines matching. Remaining 2 differences are in wrong
EPQ data from the CTE MERGE concurrent update path (separate investigation needed).
