# M0146-0075 — MERGE INSERT defaults, constraints and command tag

Status: done 2026-10-07 (`50366b4ad`).

## Symptom

Target table: `mv (a int primary key, c float8 DEFAULT random(), k text NOT
NULL DEFAULT upper('q'), …)`.

| statement | goopg before | PG 18.3 |
|---|---|---|
| `… WHEN NOT MATCHED THEN INSERT (a) VALUES (src.a)` | `c`, `k` stored NULL; tag `SELECT 0` | defaults filled; `MERGE 2` |
| `INSERT (a, k) VALUES (v.a, NULL)` / `INSERT DEFAULT VALUES` | rows stored with NULL `k` / NULL primary key | 23502 not-null violation |
| `INSERT (a, b) VALUES (v.a, -1)` under `CHECK (b > 0)` | stored | 23514 check violation |
| `INSERT (a, b) VALUES (v.a, 1/0)` | stored with NULL `b` | 22012 division by zero |
| `INSERT VALUES (v.a, …)` on a table with a generated column | values shifted one column left | generated column is in the target list |
| `INSERT VALUES (v.a, DEFAULT, …)` | syntax error | accepted |
| `WHEN MATCHED THEN UPDATE SET k = NULL` / `SET b = -3` | stored | 23502 / 23514 |

## PG

- **Target list.** `transformMergeStmt` builds the INSERT action's target
  list with `checkInsertTargets` (all attributes when no column list is
  given) and `transformInsertRow`, which applies the arity rules.
- **Defaults.** The rewriter's `rewriteTargetListIU` adds DEFAULT
  expressions for DEFAULT markers and omitted columns.
- **Execution.** `ExecMergeNotMatched` runs the action through `ExecInsert`,
  so it gets the INSERT path's coercion, BEFORE triggers, generated
  columns and `ExecConstraints`. The UPDATE action runs through
  `ExecUpdate`, which also calls `ExecConstraints`.
- **Command tag.** The tag is `MERGE <n>` (`CMDTAG_MERGE`), counting rows
  inserted, updated or deleted.

## Change

- **Planner (`planMerge`, INSERT action).** It builds a complete
  expression list the way `planInsert` does:
  - each value expression;
  - each DEFAULT marker's resolved default;
  - resolved defaults for the omitted columns
    (`defaultAppendableColumns`).

  It also:
  - raises transformInsertRow's arity errors;
  - raises 428C9 for a non-DEFAULT value into a generated column;
  - lists every live column in `buildInsertColIdx`'s default target list,
    generated ones included.

  Columns with no default, serial/identity columns and generated columns
  stay outside `InsertColIdx`. The executor fills them as omitted: NULL,
  `nextval`, or the generation expression.
- **Grammar.** MERGE's `VALUES` uses `values_item_list`, which accepts
  `DEFAULT`.
- **Executor, INSERT action (`mergeOp`).**
  - Value errors propagate.
  - Provided values go through `coerceRowForConstraintChecks`.
  - After BEFORE triggers and generated columns, the routed leaf row goes
    through `checkRowConstraintsForWrite` (NOT NULL, CHECK, domain).
- **Executor, UPDATE action (`mergeApplyUpdate`).** The new row is checked
  the same way. On a cross-partition move only the destination's columns
  are known, so CHECK is skipped there.
- **Postmaster.** `commandTagFor` gains the `*optimizer.Merge` arm.

## Verification

- `TestMergeInsertFillsDefaultsAndChecksConstraints` checks:
  - omitted-column defaults;
  - DEFAULT markers;
  - a generated column in the target list;
  - nine error cases, every message PG 18.3's.

  The existing M0134-0044 default/serial tests still pass.
- `TestCommandTagForMergeIsMergeN` checks the command tag.
- Server probe, 22 statements against PG: identical except for the two
  pre-existing gaps below.
- Regress A/B over 10 files, including merge, with, triggers,
  generated_stored, identity and insert: identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96, plan shapes 99/99, runtime-moves 0. Total time was +19.6%
    while a concurrent testport run loaded the host; this change touches
    MERGE only;
  - fire set: no fires;
  - ea-ratchet PASS.

## Not covered

- The Failing-row DETAIL renders a date as `01-02-2020`, where PG renders
  `2020-01-02`. Plain INSERT does the same: `formatRowForDetail` uses
  `Datum.Format`, not the type's output function. Filed as M0146-0083.
- The caret of "INSERT has more target columns than expressions" sits at
  WHEN. PG points at the first column without an expression, and the
  parsed column list carries no positions.
- On a cross-partition MERGE UPDATE, CHECK constraints are not checked.
- An identity GENERATED ALWAYS column accepts a non-DEFAULT value in MERGE
  INSERT, as in goopg's plain INSERT. MERGE's `OVERRIDING` clause is not
  parsed.
