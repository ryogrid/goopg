# 0097-0024 — Top-level set operations: UNION / INTERSECT / EXCEPT [ALL]

Status: accepted (2026-05-25)

## Problem

The planner and analyzer accepted only `UNION ALL`. Every other set
operation — `UNION` (DISTINCT), `INTERSECT [ALL]`, `EXCEPT [ALL]` — was
rejected with `0A000 "set operations are not supported in v0 planner"`,
even though the parser already produced a `SetOpClause` for all three
keywords. This blocked a broad class of regress cases (`copyselect`,
`union`, `select`, set-op subqueries) and any `COPY (… UNION …) TO STDOUT`.

## Approach

Set operations now flow through one path in `planSelect`
(`internal/planner/planner.go`):

1. Plan the right branch, then the left branch with the `SetOp` chain
   temporarily cleared (unchanged recursion guard).
2. Validate equal column counts across branches → `42601`
   `"each {UNION|INTERSECT|EXCEPT} query must have the same number of
   columns"` (`setOpKeyword` helper), matching PostgreSQL's parse-analysis
   error.
3. Build a `SetOp` plan node carrying the operation kind and `All` flag.
4. Wrap a trailing `ORDER BY` / `LIMIT` / `OFFSET` via
   `wrapSetOpSortLimit`. Per SQL §7.6 the sort keys reference the combined
   output columns only — by 1-based position (a `*parser.IntegerConst`
   becomes a `ColumnRef` into the output schema; out-of-range → `42P10`
   `"ORDER BY position N is not in select list"`) or by output column name
   (resolved against a `newResolveContext(nil, setNode.Output())`). This is
   the shape `copyselect` uses: `… UNION … ORDER BY 1`.

The `SetOp` plan node (`internal/planner/plan.go`) gains an
`Op parser.SetOpType` field. Its zero value is `SetOpUnion`, so the implicit
partition/inheritance UNION ALL construction sites (which set only
`All: true`) keep working unchanged.

The analyzer (`internal/analyzer/analyzer.go`) drops its symmetric rejection
and analyzes both branches for every variant. The recursive-CTE path is
untouched: it still distinguishes a true `UNION [ALL]` recursive body from a
plain body and routes the latter through normal analysis (which now supports
set ops).

### Executor (`internal/executor/operators_setop.go`)

`UNION ALL` keeps the original **streaming** behavior — drain the left child,
then the right — which preserves the per-row `currentTID` provider that
`lockRowsOp` relies on for partition/inheritance scans under
`SELECT … FOR UPDATE` (M0100-0005). `currentTID` returns `false` in buffered
mode.

Every other variant **buffers** both inputs at `Open` and applies multiset
semantics, keyed by the shared `rowKey` hash (same as `DISTINCT` and the
recursive-UNION dedup — [[pattern_sibling_paths_must_agree]]):

| Operation       | Output rule (per key `k`)                     |
|-----------------|-----------------------------------------------|
| `UNION`         | each distinct key once, first-seen order      |
| `INTERSECT`     | once if present in both                        |
| `INTERSECT ALL` | `min(leftCount[k], rightCount[k])` copies     |
| `EXCEPT`        | once if absent from right                      |
| `EXCEPT ALL`    | `max(0, leftCount[k] - rightCount[k])` copies |

Children are closed early once fully consumed.

## Tests

- `internal/executor/operators_setop_test.go`:
  `TestSetOpMultisetSemantics` (asymmetric multiplicities so all six
  variants yield distinct counts), `TestSetOpOrderByPosition` (the
  `copyselect` positional-ORDER-BY shape).
- `internal/planner/planner_test.go`: column-count-mismatch case
  (`SELECT 1 UNION SELECT 2, 3` → `42601`).
- `internal/analyzer/analyzer_test.go`:
  `TestAnalyzeRejectUnsupportedSelectFeatures` now asserts DISTINCT +
  all set-op variants are *accepted*.

Verified end-to-end on a live server: `UNION`/`INTERSECT`/`EXCEPT` with and
without `ORDER BY`, and `COPY (SELECT … UNION SELECT … ORDER BY 1) TO STDOUT`
all produce correct output.

## Scope / follow-ups

`copyselect` still defers. Its set-op COPY now executes correctly, but its
specific query uses `SELECT *` in the UNION right branch
(`select * from v_test1`), which fails with `42601 "'*' is not allowed here"`
(`internal/analyzer/analyzer.go:769`) even though `SELECT * FROM v_test1`
works standalone — a **separate** star-expansion-in-set-op-branch bug. The
remaining `copyselect` diff also needs psql `\;`/`\.` multi-command COPY
strings and `COPY (SELECT INTO)` rejection. Set-op precedence
(`INTERSECT` binding tighter than `UNION`/`EXCEPT`) is not modeled — the
parser builds a simple right-recursive chain; out of scope here.
