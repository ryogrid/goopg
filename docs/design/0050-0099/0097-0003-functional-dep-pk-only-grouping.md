# 0097-0003 — Functional-dependency GROUP BY recognises only PRIMARY KEY; CREATE VIEW validation error rendered cleanly

Status: accepted
Milestone: M0097-0003 (regress: `functional_deps`)
Parent: [[0097-0036]] (which flagged "view-body validation at create time" as the residual `functional_deps` blocker)

## Problem

Two sibling-path divergences kept the `functional_deps` regress case failing
and, more importantly, allowed silently-wrong query plans.

### 1. Unique constraints wrongly established a functional dependency

`isColumnFunctionallyDetermined` (`internal/planner/planner.go`) decides whether
an *ungrouped, non-aggregate* select-list column may pass through a GROUP BY by
checking whether some index whose columns are all present in the GROUP BY covers
the column's source table. It accepted **both** primary-key and unique indexes:

```go
if !idx.Primary && !idx.Unique {
    continue
}
```

PostgreSQL's `check_functional_grouping`
(`src/backend/catalog/pg_constraint.c`) recognises **only PRIMARY KEY**
constraints — never unique constraints — because a unique constraint may be
deferrable and, when nullable, admits multiple NULL rows that would not
collapse under grouping. The `functional_deps` fixture pins this exactly:

```sql
-- group by unique not null (fail/todo)
SELECT id, keywords, title, body, created FROM articles GROUP BY title;
-- group by unique nullable (fail)
SELECT id, keywords, title, body, created FROM articles GROUP BY body;
```

Both must fail; only `GROUP BY id` (the primary key) may succeed. Whether goopg
got the wrong answer was *state-dependent*: it depended on whether the inline
`UNIQUE` column had a registered index at plan time, which made the same view
creation pass or fail depending on accumulated cluster state — exactly the kind
of nondeterminism that surfaced as a spurious `relation "fdv1" already exists`
in the shared-cluster regress run.

### 2. CREATE VIEW validation error leaked the raw PlanError string

`execCreateView` (`internal/executor/operators_ddl.go`) validates the view body
by calling `planner.Plan(s.Query, …)` and returning any non-`0A000` error. It
returned the raw `*planner.PlanError`, whose `Error()` method renders
`"<code>: <message> (byte <pos>)"`. The simple-query path instead extracts
`.Code`/`.Message` separately (`planErrorFields`), so the same grouping error
came out two different ways:

```
-- direct SELECT (correct):
ERROR:  column "articles.id" must appear in the GROUP BY clause ...
-- CREATE VIEW … (wrong, before):
ERROR:  42803: column "articles.id" must appear in the GROUP BY clause ... (byte 32)
```

## Fix

`internal/planner/planner.go` — `isColumnFunctionallyDetermined` now skips every
non-primary index (`if !idx.Primary { continue }`), matching
`check_functional_grouping`. A column passes through a GROUP BY only when the
table's PRIMARY KEY is fully contained in the grouping keys.

`internal/executor/operators_ddl.go` — `execCreateView` converts a non-`0A000`
`*planner.PlanError` into an `*ExecError{Code, Message, Hint, Pos}` so the wire
layer renders the clean `ERROR:  <message>` line, identical to the direct-SELECT
path. `0A000` (planner "feature not supported") is still tolerated so the
planner's incompleteness does not reject views upstream would accept; those
fail at reference time instead.

## Tests

- `internal/planner/functional_deps_test.go`:
  - `TestGroupByPrimaryKeyEstablishesFunctionalDependency` — `GROUP BY id` (PK)
    accepted.
  - `TestGroupByUniqueColumnRejected` — `GROUP BY body` (UNIQUE, non-PK) rejected
    with SQLSTATE `42803` and the grouping-error wording.
- Manual cluster verification (fresh `goopg init`/`start`, in-tree `psql`):
  `CREATE TEMP VIEW … GROUP BY body` now errors with the clean message and does
  not register the view; the subsequent `GROUP BY id` view creation succeeds.

## Result & residual blocker

`functional_deps` normalized diff stays at **21 lines**, but the composition
changed: the spurious `relation "fdv1" already exists`, the malformed
`42803: … (byte 32)` rendering, and the incorrect acceptance of `GROUP BY body`
are all resolved. The entire remaining diff is one out-of-scope feature —
**`ALTER TABLE … DROP CONSTRAINT … RESTRICT` view→constraint dependency
tracking** (the 5 `cannot drop constraint … because other objects depend on it`
ERRORs, their `DETAIL: view … depends on constraint …` lines, the 5
`HINT: Use DROP … CASCADE` lines) plus prepared-plan re-validation on constraint
drop (`EXECUTE foo` must fail once the PK is gone). That requires a `pg_depend`
-style dependency registry and is deferred.

## Verification of no per-case regression

All 17 previously-passing regress cases were re-checked **in isolation** with the
change and still pass. (The full 232-case suite shares one cluster and a
server-wide plan cache, so its pass set is order-dependent and already noisy on
clean `master` — e.g. `portals_p2`, marked `pass` in the per-case baseline,
fails in the clean-`master` full-suite run. The baseline tracks per-case
isolation status, against which this change regresses nothing.)
