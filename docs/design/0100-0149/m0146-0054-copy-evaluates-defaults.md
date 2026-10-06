# M0146-0054 — COPY FROM evaluates an omitted column's DEFAULT per row

Status: done 2026-10-06 (`5dc730024`).

## Symptom

- `COPY t (a, b)` into a table with `c float8 DEFAULT random()` or
  `timestamptz DEFAULT clock_timestamp()` stored `c` NULL in every row.
  PG 18.3 fills it, and INSERT filled the same default.
- `k text NOT NULL DEFAULT upper('q')` failed the COPY with a NOT NULL
  violation.
- Regress `copy`: the `parted_si` case (`rand float8 NOT NULL DEFAULT
  random()`) failed the same way.

## Cause

- COPY filled omitted columns through `applyDefaultsForMissing` →
  `evalGenExpr`. That lightweight evaluator was written for the logical
  apply worker and generated columns. It handles literals, simple
  arithmetic, a few zero-argument time functions, `nextval`, `nullif` and
  `coalesce`.
- Any other function returned NULL with no error, so the column was
  silently NULL, or NOT NULL tripped.
- INSERT does not depend on it for these columns: `planInsert` appends
  each omitted column's default as a planned target expression
  (`defaultAppendableColumns`), which the full evaluator runs.

PG's `BeginCopyFrom` (`src/backend/commands/copyfrom.c`) builds each
omitted column's default once (`build_column_default` +
`ExecPrepareExpr`, the `defexprs` array). `CopyFrom` evaluates it per row
with `ExecEvalExpr`.

## Change

- `optimizer.ResolveColumnDefault(expr, cat)` resolves a default in the
  context `planInsert` uses for its appended defaults.
- `newCopyFromExecutor` resolves the omitted columns' defaults once, into
  `CopyFromExecutor.defaults`, and now returns an error.
  - It skips GENERATED ALWAYS columns, which are computed later, and
    serial/identity columns, whose `nextval` belongs to
    `autoGenerateSerialValues`. This matches `defaultAppendableColumns`.
- `storeCopyRow` → `fillDefaults`:
  - evaluates each default per row with `evalExprSlot`;
  - coerces the result to the column type the way the INSERT path does
    (`coerceRowForConstraintChecks`);
  - fails the COPY on an evaluation error instead of storing NULL.
- `clock_timestamp()` returned `ctx.Now`, the statement timestamp. It now
  returns the wall clock truncated to microseconds, PG's
  `GetCurrentTimestamp` (`timestamp.c` `clock_timestamp`). Its value
  therefore differs per row.
- **postmaster:** the stand-alone COPY path built its executor Context by
  hand, without the session's sequence state. A `DEFAULT nextval(...)`
  filled by COPY therefore never set `currval` / `lastval`.
  - The Context now shares the session's `currval` map.
  - The `lastval` scalars are saved back when the COPY ends: deferred for
    the synchronous paths, `copyInState.onDone` for a CopyIn stream.
  - The inline COPY inside a multi-statement batch already used the
    batch's wired Context.

## Verification

- `TestCopyFromEvaluatesDefaultsPerRow`:
  - `random()` and `clock_timestamp()` are non-NULL and distinct per row;
  - numeric, text-concatenation, arithmetic and `upper()` defaults hold
    PG 18.3's values;
  - `DEFAULT 1/0` fails with 22012 and stores no row;
  - at HEAD the first row fails NOT NULL on `upper('q')`.
- Server probe against PG on :5534: values, `pg_typeof` and
  `currval` / `lastval` after COPY match.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24 MATCH;
  - sf025 96/96, no verdict change;
  - fire set PASS, no plan changed;
  - ea-ratchet PASS.
- Regress A/B over 12 files (copy, copy2, copydml, copyselect, sequence,
  insert, identity, generated_stored, timestamptz, horology, triggers,
  plpgsql):
  - copy improved: `parted_si` loads, diff 298 → 291 lines;
  - plpgsql moved within its known flap;
  - the other 10 are byte-identical.

## Not covered

- **Error timing.** PG constant-folds a default while preparing it
  (`eval_const_expressions`), so `DEFAULT 1/0` errors when the COPY
  starts, before any data. goopg raises at the first row. Both fail the
  statement with the same error.
- **Remaining callers of the weak helper.** `applyDefaultsForMissing`
  still serves the logical apply worker, MERGE's INSERT action and
  ON CONFLICT's parent-row path.
  - ON CONFLICT was probed and matches PG.
  - MERGE ... WHEN NOT MATCHED THEN INSERT leaves omitted defaulted
    columns NULL, skips their NOT NULL check, and reports `SELECT 0`
    instead of `MERGE n`. Filed as M0146-0075 (S2).
- **`timeofday()`** still returns the statement timestamp. PG's is the
  wall clock, rendered as text in the session time zone.
