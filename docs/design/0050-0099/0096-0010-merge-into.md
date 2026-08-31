# 0096-0010 — MERGE INTO Statement

**Status:** accepted  
**Date:** 2026-05-12  
**Milestone:** M0096-0010

## Problem

The `eval-plan-qual-trigger` and related isolation specs use `MERGE INTO` to
perform conditional upsert/delete patterns (e.g. update matching rows, insert
non-matching rows in a single statement). Without MERGE support the server
returns `unsupported statement type *parser.MergeStmt`, blocking those tests.

## Solution

MERGE is implemented as a nested-loop join between the USING source and the
target heap, with no index support (suitable for the table sizes in isolation
tests).

### Parser (internal/parser/)

**token.go** — Two new keywords: `KwMerge` ("merge"), `KwMatched` ("matched").

**keywords.go** — Both mapped to `KwCatUnreserved` so they can appear as
identifiers in column/table name positions.

**ast.go** — Three new types:
- `MergeActionKind` (int): `MergeActionUpdate`, `MergeActionDelete`,
  `MergeActionInsert`.
- `MergeWhenClause`: `Matched bool`, `Condition Expr`, `Action`, `UpdateAssigns
  []UpdateAssign`, `InsertColumns []string`, `InsertValues []Expr`.
- `MergeStmt`: `Target RangeVar`, `Source RangeVar`, `On Expr`,
  `Clauses []*MergeWhenClause`.

**parser.go** — `parseMerge()` and `parseMergeWhenClause()` handle the full
MERGE syntax. MERGE USING accepts a table name or an aliased subquery.

### Planner (internal/planner/)

**plan.go** — Parallel types:
- `MergeActionKind`, `MergeWhenClause`, `Merge` plan node.
- `MergeWhenClause.UpdateSet []Expr` — indexed by target column ordinal.
- `MergeWhenClause.InsertExprs []Expr` + `InsertColIdx []int` — evaluated
  against the source row at execute time (not wrapped in a Values node, which
  would lose the source-row binding context).

**planner.go** — `planMerge(s *parser.MergeStmt, cat) (Node, error)`:
- Looks up the target table.
- Plans the USING source via `planScanRangeVar`.
- Builds a merged schema: target columns at offset 0..N-1, source columns at
  offset N..N+M-1. Both the ON condition and WHEN MATCHED expressions are
  resolved against this merged context.
- WHEN NOT MATCHED INSERT VALUES are resolved against a source-only context
  (offset 0..M-1) so column refs in VALUES refer to the source row.
- `buildInsertColIdx` maps INSERT column names → target ordinals; defaults to
  all non-generated columns when no explicit column list is given.

The `*parser.MergeStmt` case is wired into `Plan()` immediately after
`*parser.DeleteStmt`.

### Executor (internal/executor/)

**operators_merge.go** — `mergeOp` struct and `newMergeOp`:

1. **Drain source**: build and drain the USING operator into a `[]srcEntry`
   slice (each entry carries a materialized row and a `matched` flag).
2. **Scan target**: walk every block of the target heap (same pattern as
   `scanMatching`). For each visible tuple, test every source row against the
   ON condition. On the first match, apply the first matching WHEN MATCHED
   clause (UPDATE or DELETE), mark the source entry `matched = true`, and move
   on.
3. **Apply modifications**: a deferred slice of `pendingMod` avoids
   holding page locks while writing. `mergeApplyUpdate` stamps xmax on the old
   tuple then calls `writeHeapRow` for the new version. `mergeApplyDelete`
   stamps xmax only.
4. **NOT MATCHED INSERT**: iterate unmatched source rows, apply the first
   matching WHEN NOT MATCHED INSERT clause. INSERT VALUES expressions are
   evaluated with the source row as the input row (ColumnRefs resolved at
   plan time against the source-only context map to the right offset).

**executor.go** — `case *planner.Merge:` dispatches to `newMergeOp(p)`.

## Limitations (v0)

- No index acceleration for the ON join condition — full heap scan per source
  row (O(target × source)).
- No RETURNING clause.
- No WHEN NOT MATCHED BY SOURCE / WHEN NOT MATCHED BY TARGET PostgreSQL 15+
  extensions.
- Generated-column recomputation after UPDATE is supported; constraint checks
  (FK, CHECK) are deferred to M0096-0011.

## Follow-up (2026-07-11, M0122-0004): duplicate-source cardinality rule verified across partition routing + errhint added

PostgreSQL enforces that a single target row may not be affected more than
once by one MERGE: when two or more source rows match the same target row and
the WHEN MATCHED action modifies it (UPDATE or DELETE), it raises SQLSTATE
`21000` (`ERRCODE_CARDINALITY_VIOLATION`) `"MERGE command cannot affect row a
second time"` with the hint `"Ensure that not more than one source row matches
any one target row."` (see `src/backend/executor/nodeModifyTable.c`
`ExecMergeMatched`).

`unimplemented_feat.json` carried an `open`, medium-confidence M0100-0007
entry claiming this was *"not fully implemented for merge cross-partition
routing"* with an `unclear` code audit. Re-verification this loop shows the
rule is fully implemented and was already correct across partition routing:

- `mergeOp` (`internal/executor/operators_merge.go`) sets the pending mod's
  `hasDuplicate` flag when, during the (per-partition-child) target scan, a
  second source row matches an already-matched target tuple, and raises the
  `21000` error **after** applying the first modification (and after any
  `epqWait` blocking) so concurrent callers observe PG's
  `<waiting…>`/`<…completed>` ordering.
- Because the duplicate is detected per target *tuple*, it holds whether the
  target is a plain table, a partitioned table scanned child-by-child, or —
  the specifically-flagged case — a MERGE UPDATE whose new key relocates the
  target to a *different* leaf partition (delete + re-insert): the first
  source row's cross-partition move still leaves the second matching source
  row to trip the guard.

The only real gap found was cosmetic: goopg omitted PG's `errhint`. This loop
adds it (`operators_merge.go`, the `hasDuplicate` raise site) so the error is
byte-faithful to upstream.

The behavior had **zero** prior test coverage. New regression tests in
`internal/executor/merge_dup_source_test.go` pin all four shapes:
`TestMergeDupSourceUpdateNonPartitioned` (asserts message + hint + first-mod
applied), `TestMergeDupSourceDeleteNonPartitioned`,
`TestMergeDupSourceUpdateWithinOnePartition`, and
`TestMergeDupSourceCrossPartitionMove` (asserts the row moved to the second
partition before the duplicate was raised). The `unimplemented_feat.json`
M0100-0007 entry is flipped `open → resolved` with this proof.
