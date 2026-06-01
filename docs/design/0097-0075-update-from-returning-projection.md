# M0097-0075 — UPDATE … FROM … RETURNING projects FROM-table columns

Date: 2026-05-29
Status: Landed
Scope: `internal/executor/operators_storage.go`
Related: M0097-0020 (returning regress test), M0097-0065 (original UPDATE FROM landing)

## Problem

The `returning` regress test failed with 553 diff lines. The first
diverging case was:

```sql
UPDATE foo SET f3 = f3*2
  FROM int4_tbl i
  WHERE foo.f1 + 123455 = i.f1
  RETURNING foo.*, i.f1 as "i.f1";
```

Expected output projected `i.f1 = 123456` from the joined `int4_tbl`.
goopg returned `NULL` for the `i.f1` column.

## Root cause

`planUpdate` (planner.go:4977) resolves RETURNING expressions against a
combined binding context whose schema is
`[target_cols..., from1_cols..., from2_cols...]`. Column references to
FROM tables therefore carry indices that point past `len(tgtCols)`.

In the executor, `updateWithFrom`
(`operators_storage.go:2645`) carries the right data through the
nested-loop step — its `combinedRow` already has the
`[targetRow, fromRow1, ...]` layout when evaluating `FromPred` and the
SET expressions. But `pendingUpdate` only stored `newRow` (target-only,
post-update), and `appendUpdateRetRow(pu.newRow)` (line 1612, 1619)
evaluated RETURNING against that target-only slice. Out-of-range column
references returned NULL silently.

## Fix

Three minimal changes in `internal/executor/operators_storage.go`:

1. Split `appendUpdateRetRow` into a thin wrapper and a new
   `appendUpdateRetRowWithFrom(newRow, fromPortion Row)`. The latter
   builds `evalRow = [newRow..., fromPortion...]` before evaluating
   RETURNING. `fromPortion == nil` falls back to the legacy
   target-only evaluation (used by `updateViaIndex` and the regular
   `Next` path that have no FROM tables).

2. `pendingUpdate` gains a `fromPortion Row` field. In the recurse
   leaf, when `len(o.plan.Returning) > 0`, the FROM portion of the
   live `combinedRow` (`combinedRow[tgtColCount:]`) is cloned into the
   pending entry. Cloning is required because the recursion reuses the
   `combinedRow` backing storage across siblings.

3. The pending-application loop calls
   `o.appendUpdateRetRowWithFrom(pu.newRow, pu.fromPortion)` instead
   of `o.appendUpdateRetRow(pu.newRow)`.

The two non-FROM `updateOp` callers (`updateViaIndex`, `Next[2]`)
continue to call `appendUpdateRetRow(pu.newRow)`, which forwards
`fromPortion == nil` and keeps the existing target-only behaviour.

## Verification

- `go build ./...`: clean.
- `go test ./internal/executor/ ./internal/planner/`: all tests pass
  modulo the pre-existing `TestToastByteaRoundTrip` flake
  (confirmed flaky on the baseline commit `e1185591`; not caused by
  this change).
- `GOOPG_REGRESS_DIFF_DIR=... go test -run
  'TestPort_RegressSuite/returning$' ./internal/testport/`:
  diff lines drop 553 → 545 (-8). The first remaining divergence
  has moved from line 88 (UPDATE FROM RETURNING) to line 104
  (DELETE … USING — parser does not accept `USING`, separate fix).

## Remaining `returning` blockers

In order of first occurrence in the diff:

1. **DELETE … USING parser** — `parseDelete` in
   `internal/parser/dml.go:436` does not accept the `USING`
   keyword; the statement silently no-ops with leftover tokens.
2. **ALTER TABLE … ADD COLUMN … DEFAULT** does not backfill the
   default into existing rows (expected `f4 = 99` for old rows,
   actual is NULL).
3. **INHERITS row propagation in UPDATE FROM** — `foochild` rows
   are not visible to the parent `UPDATE foo SET f3 = f3*2 FROM
   int8_tbl i WHERE foo.f1 = i.q2 RETURNING *`.
4. **RETURNING OLD / RETURNING NEW** alias references not parsed.

Each blocker is sized for a single follow-up loop.
