# M0134-0078 — a non-plpgsql trigger must not suppress the row it cannot execute

Status: accepted
Task: M0134-0078 (`triggers.sql`)

## Summary

`triggers.sql` is the regress case with the most already-landed trigger
machinery behind it (M0096/M0097/M0100/M0118/M0119), so its residual divergence
is edge cases, not absent triggers. Sized at HEAD: **2853 raw diff lines / 67
hunks / 108 `^+ERROR` / 64 `^-ERROR`**, the gate runs to completion (no
hang/crash — reaches the final `drop role`).

Bucket **B7** is the one contained correctness bug among them: a
**C-language** (or any non-plpgsql) BEFORE trigger suppresses every row it
would fire on. `CREATE FUNCTION ... LANGUAGE C` succeeds in goopg as a *stub*
(a `pg_proc` entry is stored) but the body can never execute, so the only
honest behaviour is to *skip* the trigger and let the row proceed unmodified —
exactly what the existing `lookupTriggerRoutine == nil → continue` path already
does for an unresolvable function. The pass-through arm contradicts that stance
and instead reads as "the trigger returned NULL", which suppresses the row.

## Evidence (live, against the PG 18.3 oracle)

- A C-language BEFORE trigger on a table: `INSERT` returns `(0 rows)` in goopg
  (every row suppressed); the identical trigger written in plpgsql inserts the
  row correctly.
- The expected output (`expected/triggers.out:15-164`, the
  `trigger_return_old` / `f1_times_10` / `trigger_zed` sections) is ~80 diff
  lines of `(0 rows)` vs the populated rows PG prints.

## Root cause (goopg)

`executePLpgSQLTriggerBody` (`internal/executor/plpgsql_runtime.go:2913-2916`)
returns `(Row, bool, error)` where the `bool` is "the trigger produced an
authoritative return value". Its non-plpgsql arm returns `(nil, true, nil)` —
`ok == true` with a nil row. `fireTriggers` (`internal/executor/operators_trigger.go:64-83`)
then hits its BEFORE arm:

```go
retRow, ok, err := executePLpgSQLTriggerBody(r, trigCtx, ctx)
if err != nil { return nil, false, err }
if !ok { continue }            // pass-through: skip, leave row alone
if timingLow == "before" {
    if retRow == nil {
        return nil, false, nil // RETURN NULL suppresses the row
    }
    ...
}
```

Because the arm returns `ok == true`, `fireTriggers` never reaches the `!ok →
continue` pass-through and instead treats the nil row as "RETURN NULL →
suppress". The fix is to return `(nil, false, nil)` so the row proceeds
unmodified.

## PostgreSQL's rule (the oracle)

`ExecCallTriggerFunc` (`postgres/src/backend/commands/trigger.c:2310`) — the
trigger function's return value drives whether the row proceeds, but PG never
reaches firing for an unloadable function because `CREATE TRIGGER` resolves the
function up front (`trigger.c` `CreateTrigger` → `LookupFuncName`). goopg has no
C executor, so its established best-effort stance for an un-executable trigger
is *skip* (the `lookupTriggerRoutine == nil → continue` path at
`operators_trigger.go:149-163` and `:49-52`); B7's pass-through arm must match
that stance instead of emulating a NULL return.

## Fix

One-line change plus the two comments that describe it:

- `internal/executor/plpgsql_runtime.go:2915`: `return nil, true, nil` →
  `return nil, false, nil`, and correct the inline comment
  (`non-plpgsql trigger: pass-through` is already the *intent* — the `true` was
  the bug).
- The function's doc comment (`:2906-2912`) gains a fourth case:
  `(nil, false, nil)` = trigger not executed (non-plpgsql — row proceeds
  unmodified).

No sibling paths: `fireStatementTriggers` (`operators_trigger.go:119`) discards
the return (`_, _, err`), and the analogous `executePLpgSQLRoutine`
(`plpgsql_runtime.go:1153`) already errors `0A000` rather than pass through.

## Win / scope

- Measured post-fix (`scripts/pg-regress-runner.sh triggers`, 2026-08-23): the
  row *suppression* is fully eliminated — zero `(0 rows)` lines remain anywhere
  in the `trigger_return_old` / `f1_times_10` / `trigger_zed` sections; total
  diff shrinks 2853 → 2818 raw lines (−35; hunks 67 → 69 as contiguous
  suppression blocks collapse into smaller value-diff hunks). Byte-parity for
  Region 1 is **not** achieved and is out of B7 scope: PG's C trigger bodies
  actually execute and their return drives the tuple (`trigger_return_old`
  returns OLD, discarding the UPDATE; `f1_times_10` ×10; `trigger_zed`
  mid-chain reset), while goopg stores an un-executable C stub, so rows now
  proceed *unmodified* where PG shows transformed values. Ledgered as R1.
- The case does **not** flip to PASS —
  buckets B1–B6 and B8–B13 remain (statement-level firing; main_view DROP-COLUMN
  dependency check; INSTEAD OF view triggers; partition-trigger cloning;
  transition tables; enable/disable + `session_replication_role`; ALTER TRIGGER
  RENAME; WHEN-condition evaluation; CREATE TRIGGER validation; plpgsql gaps;
  `information_schema.triggers`; deferred-trigger ACL). CSV row stays
  `failed`/`pass_required=no`; no `make regen-testport`.
