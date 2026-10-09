# M0146-0076 — DML statement-level triggers and the AFTER trigger queue

Status: done 2026-10-07 (`21fa73ee7`).

## Symptom

Table `t76` has four triggers: BEFORE/AFTER × STATEMENT/ROW for INSERT,
UPDATE and DELETE. goopg diverged from PG 18.3 in these ways:

| statement | goopg before | PG 18.3 |
|---|---|---|
| `INSERT … VALUES (1),(2)` | BS, then BR and AR interleaved per row; no AS | BS, BR 1, BR 2, AR 1, AR 2, AS |
| `INSERT … WHERE false` | BS only | BS, AS |
| `UPDATE` / `DELETE` (any row count) | no statement trigger | BS … AS, even for 0 rows |
| `INSERT … ON CONFLICT DO UPDATE` | no statement trigger | BS INSERT, BS UPDATE … AS UPDATE, AS INSERT |
| `MERGE` | BEFORE ROW only | BS per action kind, AR per row, AS per action kind |
| `UPDATE … FROM`, `DELETE … USING` | no AFTER ROW | AR per row |

A trigger body that counts the table's rows saw a partial count in an
AFTER ROW trigger.

## PG

- **BEFORE STATEMENT.** `ExecModifyTable` fires it at the node's first call
  (`fireBSTriggers`, nodeModifyTable.c). It fires once per query level for
  each relation and command (`before_stmt_triggers_fired`, trigger.c).
- **AFTER events are queued.** AFTER ROW events (`AfterTriggerSaveEvent`)
  and AFTER STATEMENT events (`fireASTriggers`) go onto the current query
  level's list. `AfterTriggerEndQuery` fires the list in order once the
  query has finished.
- **One AFTER STATEMENT per relation and command.** A new AFTER STATEMENT
  event first cancels any earlier queued one for the same relation and
  command (`cancel_prior_stmt_triggers`).
- **Query levels.** A statement run by a trigger or function body opens its
  own level (`AfterTriggerBeginQuery`), so its events fire when that
  statement ends.
- **Firing order and filtering.**
  - Same-event triggers fire in name order: `RelationBuildTriggers` reads
    `pg_trigger` through the relid+name index.
  - A column-specific `UPDATE OF` trigger, at either level, fires only when
    the UPDATE's target columns include one of its columns.
    `TriggerEnabled` gets those columns from `ExecGetAllUpdatedCols`; for
    MERGE they are the union of the UPDATE actions.
- **OLD and NEW.** In a statement-level trigger both are NULL. NEW is NULL
  for DELETE and OLD for INSERT (pl_exec.c).

## Change

- **`internal/executor/after_trigger.go`.** A per-query level,
  `afterTriggerQuery`, holds:
  - the queued events;
  - the set of fired BEFORE STATEMENT keys.

  Its helpers:
  - `fireBeforeStatementTriggers`: fires once per level.
  - `queueAfterStatementTriggers`: cancels the prior event and re-queues
    at the tail.
  - `queueAfterRowTriggers`.
  - `fireAfterTriggerQuery`.

  With no level open, the queue helpers fire immediately, as goopg did
  before.
- **Statement roots own a level.**
  - `stmtCTEScopeOp`: routine-body statements and `Run`.
  - `OpIterator`: the simple-query dispatcher and cursors.
  - `BeginAfterTriggerQuery`: the extended-protocol Execute.

  The roots swap their level into `Context.afterTrigQuery` around each
  Open/Next/Close without allocating. They fire it after a clean Close and
  discard it when Open, Next or Close fails.
- **DML operators.**
  - Each Next for insert, update, delete, upsert and merge fires BEFORE
    STATEMENT ahead of the single processing pass and queues AFTER
    STATEMENT after it.
  - Every AFTER ROW site queues the event instead of firing it. New AFTER
    ROW sites: MERGE (insert, update, delete), UPDATE … FROM and
    DELETE … USING.
- **`UPDATE OF` filtering.** `fireTriggersCols` /
  `fireStatementTriggersCols` take the UPDATE's target column set, from
  `updateTargetColumns` over the SET list.
- **Name order.** `triggerFireOrder` iterates triggers by name.
- **OLD and NEW variables.** `injectTriggerVars` defines `old` / `new` as
  NULL when the event lacks the row. Shared trigger functions name NEW in
  branches that a statement-level call never takes; before this change
  such a call failed with `column "new" does not exist` (regress `merge`).

## Verification

- `TestDMLTriggersFireInPGOrder` checks the full NOTICE sequence of ten
  statements against PG 18.3. The statements cover:
  - multi-row INSERT, a zero-row INSERT … SELECT;
  - UPDATE and DELETE, with and without matching rows;
  - ON CONFLICT DO UPDATE and DO NOTHING;
  - a writable CTE and MERGE;
  - a nested statement-trigger level (an AFTER trigger's INSERT fires
    the log table's triggers in place).

  A failing statement fires none of its queued events.
- `TestUpdateOfTriggersFilterOnTargetColumns` checks `UPDATE OF` at row and
  statement level, through UPDATE and MERGE, plus name order.
- Regress A/B over 12 files against HEAD:

  | file | HEAD | this change |
  |---|---|---|
  | triggers | 2814 | 2714 |
  | merge | 1871 | 1674 |

  - `with` and `inherit` also improved.
  - `foreign_key` row order flaps: two HEAD runs differ.
  - In `plpgsql`, an unrelated function-overload case changed
    (`select * from f1(42)` now returns rows where HEAD raised
    `f1 is not unique`). It is treated as that file's known flap.
  - The other files are byte-identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96, plan shapes 99/99, runtime-moves 0;
  - ea-ratchet PASS.

## Not covered (ledgered or filed)

- **Transition tables.** `REFERENCING OLD/NEW TABLE` relations are not
  materialised. A statement trigger that declares them stays unfired, as
  every statement trigger was before; firing it would fail the DML on the
  missing relation (regress `alter_table`, `insert_conflict`).
- **Writable CTE order.** goopg runs a data-modifying CTE to completion
  before the outer statement starts. PG pulls the CTE lazily through its
  CTE scan. The events are the same, but the outer statement's BEFORE
  STATEMENT fires after the CTE's, and the CTE's AFTER STATEMENT is queued
  before the outer AFTER ROW events.
- **`ALTER TABLE … DISABLE TRIGGER` and `session_replication_role`.**
  Neither is honoured, so a disabled trigger still fires. This was already
  true for row triggers; the statement triggers now firing make it more
  visible. Filed as M0146-0082.
- **Found while probing:**
  - RAISE parameters that are sublinks print empty, and assigning an
    integer subquery to a text variable errors (M0146-0080).
  - A writable-CTE INSERT reports the command tag `SELECT 0`
    (M0146-0081).
  - MERGE's `SELECT 0` tag is already M0146-0075.
