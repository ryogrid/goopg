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
