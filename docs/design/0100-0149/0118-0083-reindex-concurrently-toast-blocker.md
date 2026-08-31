# 0118-0083 — `reindex-concurrently-toast` blocker analysis + `InvalidOid::regclass` fix (M0118-0008)

Status: accepted (enabler + blocker map; spec NOT promoted)

## Context

`reindex-concurrently-toast` is the **last** unpromoted spec in the M0118-0008
DDL/VACUUM/maintenance-concurrency group. All 24 sibling specs
(`alter-table-{1..4}`, `detach-partition-concurrently-{1..4}`,
`partition-concurrent-attach`, `partition-drop-index-locking`,
`reindex-concurrently`, `reindex-schema`, `multiple-cic`, the `vacuum-*` set,
`truncate-conflict`, `sequence-ddl`, `cluster-conflict{,-partition}`,
`create-trigger`, `inherit-temp`, `plpgsql-toast`) already pass byte-for-byte
via their `runIsoSpecStrict` `TestPort_Isolation*` functions.

The spec's `setup` block (it has no per-permutation engine behaviour beyond
ordinary REINDEX + lock-conflict, which the `reindex-concurrently` promotion
0118-0029 already handles) requires a **real TOAST relation exposed as a
catalog object**:

```sql
CREATE TABLE reind_con_wide(id int primary key, data text);
INSERT INTO reind_con_wide SELECT 1, repeat('1',11) || string_agg(...) ...;  -- > 2 KB
SET allow_system_table_mods TO true;
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

Then `session s2` runs `REINDEX TABLE CONCURRENTLY pg_toast.reind_con_toast`
and `REINDEX INDEX CONCURRENTLY pg_toast.reind_con_toast_idx`.

## What goopg already has

- **Out-of-line TOAST storage** (`internal/executor/toast.go`): values longer
  than `ToastThreshold` (≈2 KB) are chunked into a TOAST relation at
  `RelOid + toastRelOIDOffset` (`100_000_000`). `ToastLargeColumnsIfNeeded` /
  `DetoastValue` are live.
- **Synthetic TOAST `pg_class` row** (`internal/catalog/catalog.go`, DU-002
  slice 224): a `relkind='t'` row named `pg_toast_<oid>` in the `pg_toast`
  namespace, with `reltoastrelid` on the parent pointing at it — **but only when
  the table carries explicit `toast.*` reloptions**. The OID convention
  (`toastRelidOffset == toastRelOIDOffset == 100_000_000`) already matches the
  executor's storage RelOid.

## The blocker (deferred — multi-loop epic)

PG auto-creates a TOAST relation for **any** table with a toastable column
(`needs_toast_table`, `src/backend/catalog/toasting.c`), so `reltoastrelid != 0`
for `reind_con_wide`. To make the spec's setup resolve, goopg must:

1. **Auto-expose `reltoastrelid` + the `pg_toast.pg_toast_<oid>` row** for every
   table with a toastable column (not only those with `toast.*` reloptions).
2. **Resolve the TOAST OID → name** in `LookupTableByOID` so
   `reltoastrelid::regclass` renders `pg_toast.pg_toast_<oid>` (today the
   synthetic row exists only in the virtual `pg_class` builder output, not in
   `c.tables`, so the regclass cast cannot find it).
3. **Synthesize the TOAST index** (`pg_toast_<oid>_index`) in `pg_index` /
   `pg_class` so `SELECT indexrelid … FROM pg_index WHERE indrelid = …` finds it.
4. **`ALTER TABLE/INDEX … RENAME`** on a `pg_toast` relation under
   `allow_system_table_mods`, and `pg_toast.<name>` name resolution.
5. **`REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name>`** routing to the toast
   relation (rides the 0118-0029 `waitForRelationLockers` CONCURRENTLY contract;
   the rebuild is effectively a no-op since chunks are unchanged).

**Why this is high blast radius / deferred:** registering TOAST relations in the
table map (step 2) or emitting a `pg_class` row for every toastable table
(step 1) changes relation enumeration consumed by `pg_dump` (getTables),
`pg_amcheck` (whole-DB relation walk), and `\d`-style catalog queries. Several
`port` parity suites (`pgdump_connsetup`, `pgamcheck00{2,3}*`, `scripts_port`)
hard-compare that output, so the change must be landed incrementally with each
parity suite re-run — exactly the silent-catalog-regression failure mode the
project guards against. This is a dedicated multi-loop slice, not an Effort-S
freebie. Recorded in the deferral ledger; spec stays `defer`.

## What landed this loop (safe, on-path correctness fix)

`InvalidOid (0)::regclass` now renders as `-`, matching PG's `regclassout`
(`src/backend/utils/adt/regproc.c`):

```c
if (classid == InvalidOid) { result = pstrdup("-"); PG_RETURN_CSTRING(result); }
```

Before the fix, `0::regclass` matched the **first virtual relation whose OID is
left unset (also 0)** — `information_schema.routines` — so a
`reltoastrelid::regclass` for any table *without* a TOAST relation rendered
`"routines"` instead of `"-"`. This is a general user-visible correctness bug
for any `oid_column::regclass` cast over a 0/InvalidOid value (reltoastrelid,
relfilenode of a partitioned parent, etc.), independent of the TOAST epic.

Fix site: `internal/executor/expr.go`, the `CastExpr` `regclass` arm — guard
`v.Int == 0 → "-"` before the `LookupTableByOID` scan. A non-zero OID that
matches no relation still falls through to the numeric rendering, matching PG's
`snprintf(result, NAMEDATALEN, "%u", classid)` branch.

Test: `internal/executor/regclass_invalid_oid_test.go`
(`TestRegclassInvalidOidRendersDash`) — asserts `0::regclass::text = '-'` and
`reltoastrelid::regclass::text = '-'` for a no-TOAST table.

## Gates

- `go test ./internal/executor/ ./internal/catalog/` PASS.
- `TestRegclassInvalidOidRendersDash`, `TestPlpgSQLSelectInto` PASS; no test
  depended on the old `"routines"` behaviour.
- `go build` clean.
- pgbench TPC-B smoke = pre-commit hook.
