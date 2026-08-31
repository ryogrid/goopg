# 0097-0036b — `SELECT *` over JOIN USING / NATURAL merges the join column

Status: accepted
Milestone: M0097-0036 (regress: equivclass / functional_deps, plus join-family tests)
Date: 2026-05-24

## Problem

An unqualified `SELECT *` over a `JOIN ... USING (cols)` (or `NATURAL JOIN`)
emitted the join column **twice**:

```sql
CREATE TABLE t1 (id serial, t text);
CREATE TABLE t2 (id serial, t text);
SELECT * FROM t1 JOIN t2 USING (id);
```

goopg produced a 4-column result:

```
 id | t | id | t
----+---+----+---
  1 | a |  1 | A
```

PostgreSQL emits the merged join column once, so the result is 3 columns
(`id, t, t`):

```
 id | t | t
----+---+---
  1 | a | A
```

This surfaced first in the `copyselect` regress test
(`COPY (SELECT * FROM test1 JOIN test2 USING (id)) TO STDOUT` printed
`1\ta\t1\tA` instead of `1\ta\tA`) and inflates every join-family regress
diff (`join`, `partition_join`, …) that uses `SELECT *` over a USING/NATURAL
join.

## Root cause

`planFromItem` (`internal/planner/planner.go`) already tracks the USING /
NATURAL merged columns on the *right* range binding via
`mergedRightBinding.usingHidden` (added in M0097-0003 / M0097-0006). That field
hides the right-side copy of each merged column from **unqualified column
lookup** (`resolveColumnRefAt`, planner.go:4777) so `id` is not "ambiguous".

But the whole-row star-expansion path, `expandStarTarget`, iterated every
binding's full column list with no `usingHidden` check, so the right binding's
copy of the join column was re-emitted. The lookup path and the star path had
drifted: lookup honored `usingHidden`, star did not.

## Fix

`expandStarTarget` now skips columns named in a binding's `usingHidden` set,
but **only for an unqualified `*`**. A table-qualified star (`t2.*`) still
expands to all of that relation's columns, join column included — matching
PostgreSQL's `expandRTE`, which applies the join's merged column list only for
the whole-row case, not for a per-relation `rel.*`. The left binding never
carries `usingHidden`, so its copy of the merged column survives; the output
order is therefore `left-columns (merged + rest), right-rest`.

```go
qualified := star.Table != "" || star.Schema != ""
// ... qualified path narrows bset to the single matching binding ...
for _, b := range bset {
    for i, c := range b.table.Columns {
        if !qualified && len(b.usingHidden) > 0 && /* c.Name in b.usingHidden */ {
            continue // right-side copy of a merged USING column
        }
        // emit ColumnRef + SchemaColumn
    }
}
```

Column-name comparison uses `strings.EqualFold`, matching the existing
`usingHidden` check in `resolveColumnRefAt`.

## Known limitation

PostgreSQL reorders the merged USING columns to the **front** of the join's
output (`using-cols, left-rest, right-rest`). goopg emits them in left-table
order (`left-cols-in-order, right-rest`). These agree whenever the USING
columns are the leading columns of the left table — the case in every affected
regress test (`USING (id)` where `id` is column 0). They diverge only when a
USING column is not first in the left table (e.g. `a(x,y) JOIN b(y,z)
USING (y)` → PG `y,x,z`; goopg `x,y,z`). Left as a documented follow-up; the
duplicate-column bug this doc fixes is strictly more important and was the
actual regress blocker.

## Verification

- New unit test `TestPlanSelectStarJoinUsingMergesColumn`
  (`internal/planner/planner_test.go`): unqualified `*` over USING → `id,t,t`;
  `NATURAL JOIN` over both shared cols → 2 columns; `t2.*` still 2 columns
  (join column retained).
- `go test ./internal/planner/...` — PASS.
- `copyselect` regress: the `1\ta\t1\tA` divergence is gone (normalized diff
  69 → 59); `join` 10246 → 9933.
- All 17 previously-passing regress cases re-verified PASS (no regression):
  boolean, char, comments, delete, int2, int4, md5, mvcc, name, numerology,
  oid, portals_p2, reindex_catalog, select_having, select_implicit, time,
  varchar.

## Lesson

When a relation-resolution rule (here: hide the right-side USING column) is
added, both the **column-lookup** path and the **whole-row star-expansion**
path must honor it. They are separate loops over the same bindings; updating
one and not the other produces output that is internally inconsistent (a
column unreachable by name yet present in `SELECT *`).
