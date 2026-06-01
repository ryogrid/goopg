# M0097-0096: upsertOp RETURNING support + Stage A guard removal

## Problem

`INSERT … ON CONFLICT … DO UPDATE … RETURNING` returned zero rows in all cases.

Two separate defects combined to block this:

### Defect 1 — upsertOp missing RETURNING

`upsertOp.Next()` processed all source rows in a single loop and returned
`nil, EOF` without streaming any RETURNING rows. The `insertOp` sibling
already had a `retRows []Row` accumulator + `retIdx int` stream-out pattern
(M0100-0005), but `upsertOp` was never updated to match.

`Schema()` returned `nil` unconditionally, hiding the returning schema from
the wire layer even when `plan.Returning` was non-empty.

### Defect 2 — Stage A guard blocked conflict-key column modification

`Open()` contained a static guard that rejected any `ON CONFLICT DO UPDATE
SET` expression that wrote to a conflict-key column:

```
ON CONFLICT DO UPDATE may not modify conflict-key column "a" in v0 (Stage A)
```

This guard was added as a conservative placeholder during the initial upsert
implementation (M0017-0003). Its original rationale was:

> "The arbiter index entry for the original key would still point at the new
> tuple, but the new tuple's actual key bytes differ — future probes would
> land on the wrong row."

That rationale is incorrect. `applyUpdate` already calls `maintainArbiterRow`
which inserts a **new** arbiter entry keyed on the **updated** row. The old
entry remains but points to the xmax'd (dead) old tuple — visibility filtering
skips it on future probes. PostgreSQL allows conflict-key modification (the
common `SET pk = excluded.pk` pattern that preserves the same value), and the
existing index-maintenance path handles it correctly.

## Fix

### Part 1 — Add RETURNING to upsertOp

Added two fields to `upsertOp`:
```go
retRows []Row
retIdx  int
```

Changed `Schema()` to return `plan.ReturningSchema` when `plan.Returning` is
non-empty.

Added `appendUpsertRetRow(row Row)` that evaluates `plan.Returning` expressions
against `row` (either the inserted row or the post-update row) and appends the
result to `retRows`.

Called `appendUpsertRetRow` at three sites in `Next()`:
1. Non-conflict INSERT path: `appendUpsertRetRow(inserted)`
2. Wait-and-retry non-conflict INSERT path: `appendUpsertRetRow(inserted)`
3. DO UPDATE path: `appendUpsertRetRow(updated)` (the fully-evaluated new row)

Updated `Next()` to stream `retRows` on subsequent calls (after the processing
loop), matching the `insertOp` pattern.

### Part 2 — Remove Stage A guard

Deleted the static conflict-key-column modification check from `Open()`.
Updated `TestUpsertConflictKeyModificationRejected` to
`TestUpsertConflictKeyModificationAllowed` — the test now verifies that the
Open/Next cycle succeeds (which is the correct PostgreSQL behaviour).

## Test impact (update.sql regress)

| Query shape                                    | Before | After |
|------------------------------------------------|--------|-------|
| `ON CONFLICT DO UPDATE SET … RETURNING *`      | 0 rows | ✓ returns rows |
| `ON CONFLICT DO UPDATE SET … RETURNING tableoid::regclass, …` | 0 rows | partially fixed |
| Correlated subquery in DO UPDATE SET (…) = (SELECT … WHERE i.a = upsert_test.a) | 0 rows | still 0 rows (separate bug) |

`update.sql` regress diff: 425 → 414 (−11 lines).

## Remaining limitations

- **Correlated subquery in ON CONFLICT DO UPDATE multi-column tuple SET**: the
  pattern `DO UPDATE SET (b, a) = (SELECT b || ', Correlated', a FROM t WHERE
  t.a = upsert_test.a)` still returns 0 rows. Root cause TBD; likely a
  snapshot-visibility issue where the subquery scan does not see the row being
  updated.
- **`xmin`/`xmax` system columns in RETURNING**: not yet resolved for
  `tableoid::regclass, xmin = pg_current_xact_id()::xid`.
- **Partitioned ON CONFLICT**: partition routing for ON CONFLICT RETURNING is
  not yet wired.
