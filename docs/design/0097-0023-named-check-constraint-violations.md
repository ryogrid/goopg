# 0097-0023 — Named CHECK constraint tracking & PG-compatible violation messages

Status: accepted
Milestone: M0097-0023 (Port DDL / index / cluster / vacuum regress tests)

## Problem

PostgreSQL reports a CHECK constraint violation as:

```
ERROR:  new row for relation "inhg" violates check constraint "foo"
DETAIL:  Failing row contains (x, foo, y).
```

goopg emitted only:

```
ERROR:  new row for relation "inhg" violates check constraint
```

— with no constraint name and no `DETAIL` line. Two parity gaps:

1. The constraint **name** was missing. `checkConstraints`
   (`internal/executor/operators_fk.go`) iterated `tbl.CheckConstraints`,
   a bare `[]string` of expressions, and never had the name available.
   `catalog.Table.NamedChecks` existed (and was read by the `pg_constraint`
   virtual table) but was **never populated by any code path**.
2. The `DETAIL: Failing row contains (…)` line was absent. The helper
   `formatRowForDetail` already existed for NOT NULL violations
   (`operators_storage.go`) but was not used by the CHECK path.

This is the top remaining blocker for `create_table_like` (clients and the
regress harness gate on the exact SQLSTATE/message/DETAIL text).

## Design

### Catalog: keep `CheckConstraints` and `NamedChecks` parallel

`NamedCheckConstraint{Name, Expr, OID}` is documented as parallel to
`CheckConstraints` (index i ↔ index i). To make that invariant true by
construction, all CHECK-constraint writers now go through a single helper:

```go
func (t *Table) AddCheck(name, expr string, oid uint32) {
    t.CheckConstraints = append(t.CheckConstraints, expr)
    t.NamedChecks = append(t.NamedChecks, NamedCheckConstraint{Name: name, Expr: expr, OID: oid})
}
```

Writers wired to `AddCheck` (`operators_ddl.go`):

| Site | Name | OID |
|------|------|-----|
| CREATE inline column CHECK | `""` (parser carries no name) | 0 |
| CREATE table-level CHECK | `""` | 0 |
| `ALTER TABLE … ADD CONSTRAINT name CHECK` | `act.ConstraintName` | 0 |
| `LIKE INCLUDING CONSTRAINTS` copy | source constraint name (preserved) | 0 |

`appendLikeChecks` copies the source's `NamedChecks` (name + expr),
deduplicating by expression, so a constraint copied via LIKE keeps its
original name — matching PostgreSQL, which preserves the constraint name on
`LIKE INCLUDING CONSTRAINTS`.

### Executor: report name + DETAIL

`checkConstraints` now iterates by index and recovers the name from the
parallel `NamedChecks[i]`. On a failed boolean result it returns:

```go
&ExecError{
    Code:    "23514",
    Message: `new row for relation "T" violates check constraint "NAME"`, // name omitted when anonymous
    Detail:  formatRowForDetail(tbl.Columns, row), // "Failing row contains (…)."
}
```

Anonymous constraints (empty name) fall back to the un-named wording — still
an improvement because the DETAIL line is now always present and matches PG.

## Why OID is 0 (pg_constraint stays empty) — latent join bug

`AddCheck` is called with **OID 0** at every site, so the `pg_constraint`
virtual table (`VirtualRows` skips `nc.Name == "" || nc.OID == 0`) continues
to return **no rows**, exactly as before this change.

This is deliberate. Assigning real OIDs surfaces the named CHECK through
`pg_constraint`, which immediately triggers a **pre-existing latent crash**:
psql's `\d`/`\d+` runs

```sql
SELECT c2.relname, …, pg_get_constraintdef(con.oid, true), contype, …
FROM pg_class c, pg_class c2, pg_index i
  LEFT JOIN pg_constraint con ON (conrelid = i.indrelid AND conindid = i.indexrelid
                                  AND contype IN ('p','u','x'))
WHERE c.oid = '…' AND c.oid = i.indrelid AND i.indexrelid = c2.oid …
```

Evaluating `contype IN ('p','u','x')` against a non-empty `pg_constraint`
panics with `index out of range [25] with length 25` in `Slot.Get`
(`opnode.go:98`) via `evalInExpr` — the virtual-table LEFT JOIN resolves the
right-side `contype` column reference to an absolute index equal to the slot
width. This bug was masked while `pg_constraint` always returned 0 rows
(the LEFT JOIN never produced a matched right-side slot).

Fixing the join column-mapping is a separate, non-trivial planner/executor
task and is **out of scope** for this loop. Populating `pg_constraint` with
real constraint rows (the prerequisite for the COMMENT-on-constraint /
description-join queries in `create_table_like`) is blocked on that fix.

## Results

- `create_table_like`: the two targeted lines now match PG exactly
  (constraint name + DETAIL). Isolated normalized-diff `^[<>]` count 64 → 61.
- `constraints` (isolated): 794 → 793, no new errors, no crash.
- No regression in `internal/executor` / `internal/catalog` unit suites
  (two pre-existing unrelated failures remain: publication-tables, TOAST bytea).

## Tests

`internal/executor/operators_ddl_named_check_test.go`:

- `TestNamedCheckPropagatesThroughLikeConstraints` — `ALTER … ADD CONSTRAINT
  foo CHECK` populates `NamedChecks` parallel to `CheckConstraints`, and
  `LIKE INCLUDING CONSTRAINTS` preserves the name.
- `TestCheckViolationReportsNameAndDetail` — violation reports SQLSTATE 23514,
  `… violates check constraint "foo"`, and `Failing row contains (x, foo, y).`;
  a satisfying row passes; named checks keep OID 0 (stay out of pg_constraint).

## Follow-ups

1. Fix the virtual-table LEFT JOIN column-mapping bug (`evalInExpr` /
   `Slot.Get` absolute-index resolution against composed join slots).
2. Then assign real OIDs in `AddCheck` so named CHECK constraints surface in
   `pg_constraint`, unblocking the COMMENT/`pg_description` join queries.
3. Auto-name anonymous CHECK constraints (`<table>_<col>_check`) to match PG.

Related: [[pattern_sibling_paths_must_agree]] (CheckConstraints / NamedChecks
must stay in lock-step), the [[m0097_functional_deps_create_view_validation]]
constraint-dependency thread.
