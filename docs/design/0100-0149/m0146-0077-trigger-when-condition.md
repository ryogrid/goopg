# M0146-0077 — trigger WHEN conditions gate firing

Status: done 2026-10-07 (`b86ca66b8`).

## Symptom

`CREATE TRIGGER … FOR EACH ROW WHEN (NEW.a = 123)` fired for every row, for
INSERT and COPY alike. goopg parsed the WHEN expression and kept it in
`catalog.Trigger.WhenExpr`, but used it only to print `pg_get_triggerdef`.

## PG

`TriggerEnabled` (trigger.c) evaluates `tgqual` against the OLD and NEW
slots, and a NULL or false result skips the trigger:

- **BEFORE ROW:** checked at fire time. NEW is the row as already changed
  by earlier BEFORE triggers.
- **AFTER ROW:** checked when the event is queued (`AfterTriggerSaveEvent`).
  A false result queues nothing.
- **Statement level:** the WHEN may not name OLD or NEW.
- **System columns:** OLD may use them. NEW may use them only in an AFTER
  trigger; a BEFORE trigger's WHEN may reference only NEW's `tableoid`.

## Change

- `triggerWhenPasses` (`operators_trigger.go`) binds a copy of the WHEN tree,
  then plans and evaluates it as a plain expression (`evalExprViaSQL`):
  - `OLD.col` / `NEW.col` become typed literals of the column's type (the
    binding a PL/pgSQL variable gets);
  - `OLD` / `OLD.*` / `NEW` / `NEW.*` become `ROW(…)` of those literals;
  - `tableoid` becomes the trigger relation's OID.
- Called from:
  - `fireTriggersCols`, after the event and `UPDATE OF` matches, so it
    covers BEFORE and AFTER row triggers for every DML path including COPY;
  - `fireStatementTriggersCols`.
- `rewriteParserExpr` (`plpgsql_bind_ast.go`) generalises the M0146-0072
  copy-on-write binder with a replacement callback, shared by both callers.

## Verification

- `TestTriggerWhenConditionGatesFiring` checks seven statements against PG
  18.3's NOTICE sequences. The WHEN shapes covered:
  - `NEW.a = 123`;
  - `OLD.* IS DISTINCT FROM NEW.*` and `OLD IS DISTINCT FROM NEW`;
  - a NULL comparison;
  - a quoted text literal;
  - a statement WHEN that is true and one that is false;
  - regress `some_t` (an AFTER WHEN that follows a BEFORE trigger).
- A server probe including COPY is identical to PG.
- Regress A/B over 10 files: `triggers` goes from 2714 to 2670 lines with
  no new divergent line. `foreign_key` row order flaps. The rest are
  identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96, plan shapes 99/99, runtime-moves 0;
  - ea-ratchet PASS.

## Not covered (ledgered)

- **AFTER ROW timing.** WHEN is evaluated when the queued event fires, not
  when it is queued. The rows are the same at both points, so only the
  timing of a volatile function inside WHEN differs.
- **System columns other than `tableoid`.** goopg's rows do not carry ctid
  or xmin, so such a reference fails to plan.
- **Per-row planning.** WHEN is planned once per evaluation; PG compiles it
  once per query (`ri_TrigWhenExprs`). This is a performance cost only.
