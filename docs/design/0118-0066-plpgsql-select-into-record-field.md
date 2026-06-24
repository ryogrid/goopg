# 0118-0066 — PL/pgSQL single-column `SELECT … INTO record` keeps field access (M0118-0008 `reindex-concurrently-toast` enabler)

**Status:** accepted
**Date:** 2026-06-24
**Milestone:** M0118-0008 (upstream isolation spec suite — DDL/VACUUM/maintenance concurrency)
**Kind:** Enabler, **NOT** a promotion.

## Problem

`reindex-concurrently-toast.spec`'s global `setup` block deterministically renames a
table's TOAST relation and index (so they have a stable name for
`REINDEX … CONCURRENTLY pg_toast.<name>`) via a `DO` block:

```sql
DO $$DECLARE r record;
  BEGIN
  SELECT INTO r reltoastrelid::regclass::text AS table_name FROM pg_class
    WHERE oid = 'reind_con_wide'::regclass;
  EXECUTE 'ALTER TABLE ' || r.table_name || ' RENAME TO reind_con_toast;';
  SELECT INTO r indexrelid::regclass::text AS index_name FROM pg_index
    WHERE indrelid = (SELECT oid FROM pg_class where relname = 'reind_con_toast');
  EXECUTE 'ALTER INDEX ' || r.index_name || ' RENAME TO reind_con_toast_idx;';
END$$;
```

After the 0118-0065 GUC enabler, the spec's first divergence was the `DO` block
raising `qualified names are not supported in PL/pgSQL expressions in v0 (0A000)`
on `r.table_name`.

Root cause is a real PL/pgSQL correctness gap, not specific to this spec.
`bindSelectIntoRow` (the `SELECT … INTO` binder, design 0118-0050) special-cased a
**single-column** result by binding the value *directly to the target as a scalar*:

```go
if len(schema) == 1 {
    if idx, ok := frame.indexByName[name]; ok {
        frame.values[idx] = row[0]   // scalar bind, returns early
        return
    }
}
bindRecordRowComposite(name, row, schema, frame)
```

When the target is a `record` variable (`DECLARE r record`), this scalar shortcut
runs because `r` is a declared variable, so the per-column `_<var>_<col>` sub-field
and the `compositeVarFields[name]` entry that the qualified-name expression path
reads are **never registered**. The subsequent `r.table_name` reference therefore
falls through `lowerPLpgSQLExpr`'s qualified-`ColumnRef` handler (which already
resolves trigger `OLD/NEW` and composite-type vars) to the catch-all
`0A000 qualified names are not supported` error.

PostgreSQL treats `SELECT INTO r single_col AS f` as binding a **record** whose
field `f` is addressable as `r.f`; the single-column case is not special.

## Fix

`internal/executor/plpgsql_runtime.go`, `bindSelectIntoRow`: guard the
single-column scalar shortcut with `!frame.isRecordVar(name)`. A `record`/composite
target now always routes to `bindRecordRowComposite`, which:

1. registers the `_<var>_<col>` sub-field (`_r_table_name`),
2. binds the record to its `(c0,c1,…)` composite Datum framing, and
3. records `compositeVarFields[name]`,

so the existing qualified-name handler resolves `r.table_name` by
`extractCompositeField` — identical to the path already used for multi-column
`SELECT * INTO r` (0118-0054). Plain scalar targets (`text`, `int`, …) are
unaffected because `isRecordVar` is false for them, preserving the scalar bind.

Blast radius is confined to PL/pgSQL `SELECT … INTO` where the **target is declared
`record`** (or is already composite): such a target previously lost field access on
a single-column query and is now PG-faithful. No SQL path, planner, or non-record
PL/pgSQL behaviour changes.

## Why this does NOT promote `reindex-concurrently-toast`

The spec remains fundamentally multi-loop. With this fix the setup advances past the
`r.table_name` error to a new, distinct divergence: the `EXECUTE 'ALTER TABLE … RENAME
TO reind_con_toast'` now runs and the next wall is `relation "routine_column_usage"
does not exist (42P01)` — and beneath that the spec needs **real TOAST relations as
catalog objects** (goopg stores `text`/`bytea` inline, so `reltoastrelid` is `0`,
there is no `pg_toast.<name>` relation to rename or `REINDEX … CONCURRENTLY`). Spec
stays `defer`; blocker chain recorded in the deferral ledger.

## Gates

- New regression: `TestPlpgSQLSelectInto/sel_rec_field` (single-column
  `SELECT … INTO r`, then `r.fname` in an expression → `"ALTER hello X"`).
- `go test ./internal/executor/` PASS (full package).
- `TestPlpgSQLRecordFieldAndText`, `TestPlpgSQLScalarSubquery`,
  `TestPlpgSQLForLoopMaterializeAndRecordFieldSubst`, `TestPlpgSQLDoCommitChain*`
  PASS (no record/SELECT-INTO regression).
- `TestPort_IsolationPlpgsqlToast` strict PASS (the sibling spec that exercises the
  multi-column record path — no regression).
- `go build ./...` clean. Live probe confirms reind-con-toast's first divergence
  advanced past the qualified-name error. pgbench TPC-B smoke = pre-commit hook.

## Oracle

PostgreSQL `src/pl/pgsql/src/pl_exec.c` (`exec_move_row` / `exec_stmt_execsql` INTO
handling): a `record` target bound from any result, single- or multi-column, is an
expanded record whose fields are addressable. goopg mirrors this with the
`(c0,c1,…)` composite framing + `_<var>_<col>` sub-fields.
