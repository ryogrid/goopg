# M0097-0044 + M0097-0045: set-op ORDER BY scope + SELECT-list SRF expansion

## Summary

Two improvements to the `union` and `limit` regress tests:

- **M0097-0044**: Fix parenthesised compound-query ORDER BY scope (`union` 48→34 diff lines)
- **M0097-0045**: `generate_series()` in SELECT target list via ProjectSet (`limit` 47→15, `union` 34→34)

---

## M0097-0044: Parenthesised set-op ORDER BY scope

### Root cause

```sql
(((SELECT q1 FROM int8_tbl INTERSECT SELECT q2 FROM int8_tbl ORDER BY 1)))
  UNION ALL SELECT q2 FROM int8_tbl;
```

The parser's `parseParenthesisedSelectStmt` walked the AST to the rightmost
position of the inner compound and attached the outer `UNION ALL` there.  The
`ORDER BY 1` on `innerSel` remained, but `wrapSetOpSortLimit` applied it to
the **whole** UNION ALL output (all 7 rows), when it should sort only the 2
INTERSECT rows before concatenating the 5 q2 rows.

### Fix

New `SelectStmt.InnerSegmentCount int` field records the boundary between
"inner compound segments" and "outer set-op segments":

- `parser/ast.go`: field added with comment.
- `parser/select.go`: `parseParenthesisedSelectStmt` sets `InnerSegmentCount`
  to the segment count when the inner compound has ORDER BY/LIMIT/OFFSET and
  an outer set-op is attached.
- `planner/planner.go`: the set-op building loop calls `wrapSetOpSortLimit`
  at the `InnerSegmentCount` boundary, clears ORDER BY/LIMIT/OFFSET so
  remaining outer segments are appended without re-sorting.

---

## M0097-0045: generate_series() in SELECT target list (ProjectSet)

### Problem

PostgreSQL allows `generate_series()` (and other set-returning functions) in
the SELECT target list, expanding each input row into multiple output rows.
Previously goopg treated it as a scalar function returning just the start
value.

### Design

#### Planner

`buildSelectSrfProjectSet(s, child, ctx)` (new):
1. Scans SELECT targets for `generate_series` FuncCalls.
2. Builds a `ProjectSet` node with:
   - `SrfCols []SrfCol`: one entry per SRF with resolved start/stop/step.
   - `OtherExprs []Expr`: resolved non-SRF expressions; nil slots mark SRF
     positions.
   - `schema`: full output schema ({non-SRF columns...} + {SRF column...}).

**Sort placement** is adaptive:
- Try to resolve each ORDER BY key against the PS output schema.
- If all ORDER BY keys resolve in PS output → **Sort AFTER PS** (default):
  sorts the already-expanded rows by output column values.
- If any key only resolves in the child (base-table) schema → **Sort BEFORE PS**:
  sorts base-table rows first, then PS expands each sorted row.

This handles:
- `ORDER BY unique2` (column in SELECT list) → post-sort ✓
- `ORDER BY tenthous` (column NOT in SELECT list) → pre-sort ✓
- `ORDER BY s2 DESC` (alias for SRF output) → post-sort using direct
  ColumnRef resolution (no alias→SRF substitution that would cause scalar
  evaluation) ✓

#### New plan.go structs

```go
type SrfCol struct {
    ColIdx int  // output column index
    Start  Expr
    Stop   Expr
    Step   Expr // nil → step 1
}

type ProjectSet struct {
    // ... existing fields ...
    SrfCols    []SrfCol  // SELECT-list SRF mode
    OtherExprs []Expr    // nil slot = SRF placeholder
}
```

#### Executor

`openSelectSrfMode(ctx)` in `operators_project_set.go`:
1. For each child row:
   a. Evaluate non-SRF (passthrough) expressions.
   b. Evaluate start/stop/step per SRF and generate `[]int64` values.
   c. ZIP: emit one output row per step, repeating passthrough values.
      Multiple SRFs are zipped with NULL-padding for the shorter one.

### Remaining limit blockers (15 lines)

The `z` column query uses a lateral correlated subquery reference:
```sql
SELECT (SELECT n FROM ... OFFSET s-1) AS z
  FROM generate_series(1,10) AS s;
```
Column `s` from the outer FROM clause is referenced in the inner `OFFSET`
clause. This requires full lateral correlation support which is not yet
implemented.

---

## Baseline changes

| test  | before | after |
|-------|--------|-------|
| union |     48 |    34 |
| limit |     47 |    15 |
