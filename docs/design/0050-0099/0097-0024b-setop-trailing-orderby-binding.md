# 0097-0024b — Trailing ORDER BY / LIMIT / OFFSET binds to the whole set operation

Status: accepted (2026-05-25)

## Problem

A trailing `ORDER BY` / `LIMIT` / `OFFSET` written after the final branch of
a set operation binds, per SQL §7.6, to the **entire** set operation, not to
the right branch alone. goopg's parser attached them to the right branch
instead, which produced a wrong query shape and, for `copyselect`, a spurious
error.

The `copyselect` regress query (line 56) is:

```sql
copy (select t from test1 where id = 1 UNION select * from v_test1 ORDER BY 1) to stdout;
```

`parseSelect` (`internal/parser/select.go`) parses the trailing `ORDER BY`
(and `LIMIT`/`OFFSET`) *before* `parseSetOpClause`, and `parseSetOpClause`
parses the set-op RHS via a full recursive `parseSelect`. So
`A UNION B ORDER BY 1` greedily attached `ORDER BY 1` to `B`
(`select * from v_test1 ORDER BY 1`). When the planner then planned that RHS
branch in isolation, positional `ORDER BY 1` had to resolve against the
branch's bare `*` target — which the analyzer rejects with
`42601 "'*' is not allowed here"` (the star is not expanded at the point the
positional reference is resolved on a standalone SELECT). The same shape
without `*` was also semantically wrong: the sort/limit applied to the right
branch only, not the combined output.

The sibling path `wrapSetOpSortLimit` (M0097-0024,
`docs/design/0097-0024-setops-union-intersect-except.md`) was already written
to resolve a trailing `ORDER BY`/`LIMIT`/`OFFSET` against the **combined**
set-op output (1-based position → `ColumnRef`, name otherwise) — but it reads
those clauses from the *outer* `SelectStmt` (`s.OrderBy`/`s.Limit`/`s.Offset`),
which were empty because the parser had parked them on the RHS branch.

## Approach

In `parseSelect`, immediately after assigning `s.SetOp = setOp`, lift the
RHS branch's trailing `ORDER BY` / `LIMIT` / `OFFSET` up to `s`, but only when
`s` carries none of its own:

```go
if right := setOp.Right; right != nil {
    if s.OrderBy == nil && right.OrderBy != nil { s.OrderBy = right.OrderBy; right.OrderBy = nil }
    if s.Limit  == nil && right.Limit  != nil { s.Limit  = right.Limit;  right.Limit  = nil }
    if s.Offset == nil && right.Offset != nil { s.Offset = right.Offset; right.Offset = nil }
}
```

This is safe because an operand of a set operation cannot legitimately carry
its own `ORDER BY`/`LIMIT`/`OFFSET` in this grammar — a parenthesised
`(SELECT … ORDER BY …)` operand is parsed through the subquery path, not as a
`SetOpClause.Right`. So any trailing clause that lands on the RHS via the
recursive `parseSelect` belongs to the whole set op.

Chains lift **bottom-up**: for `A UNION B UNION C ORDER BY 1`, the innermost
recursive `parseSelect` (which produced the `B UNION C` subtree) lifts the
clause from `C` to `B`, and the outer `parseSelect` then lifts it from `B`
to `A`. The outermost `SelectStmt` ends up owning the clauses, exactly where
`wrapSetOpSortLimit` reads them.

## Why the parser, not the planner

The planner's `wrapSetOpSortLimit` already resolves the clauses correctly
against the combined output; the only defect was *where the AST parked them*.
Fixing it in the parser keeps a single canonical AST shape (trailing clauses
always on the outermost SELECT of a set-op tree) that the planner, analyzer,
and any future consumer can rely on, rather than every consumer having to
re-derive ownership from the branch structure.

## Tests

- `internal/parser/select_test.go`:
  - `TestParseSetOpTrailingOrderByBindsToWhole` — `A UNION B ORDER BY 1 LIMIT 5
    OFFSET 2` lands all three clauses on the outer SELECT; RHS retains none.
  - `TestParseSetOpChainTrailingOrderBy` — three-branch chain lifts `ORDER BY`
    bottom-up to the outermost SELECT; neither inner branch retains it.
- Verified end-to-end on a live server (port 5599):
  `copy (select t from test1 where id = 1 UNION select * from v_test1b ORDER BY 1) to stdout`
  streams sorted rows; the standalone set-op + the nested derived-table form
  (`copyselect` line 60) both succeed; the `'*' is not allowed here` error is
  gone.

## Scope / remaining

This closes the top remaining `copyselect` blocker (the trailing-ORDER-BY
star-branch error). The two other documented `copyselect` gaps are unchanged
and independent: `COPY (SELECT … INTO …)` rejection, and psql multi-command
`\;`/`\.` STDIN handling.

Related: [[pattern_sibling_paths_must_agree]] (parser AST shape vs planner
consumer expectation).
