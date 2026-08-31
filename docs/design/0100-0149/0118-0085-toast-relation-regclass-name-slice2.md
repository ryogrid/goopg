# 0118-0085 — TOAST-relation regclass name resolution, slice 2 (M0118-0008)

Status: accepted (epic slice 2 of N; `reindex-concurrently-toast` still NOT promoted)

## Context

This is **slice 2** of the TOAST-exposure epic that unblocks the last unpromoted
M0118-0008 spec, `reindex-concurrently-toast`. Slice 1
([0118-0084](0118-0084-toast-relation-auto-exposure-slice1.md)) auto-exposed a
`pg_toast.pg_toast_<oid>` `pg_class` row and pointed the parent's `reltoastrelid`
at it (OID = parent OID + `100_000_000`). The full 5-step map is in
[0118-0083](0118-0083-reindex-concurrently-toast-blocker.md).

After slice 1, `reltoastrelid::regclass` still rendered the **numeric OID**: the
synthetic TOAST row lives only in the virtual pg_class builder's output, not in
`c.tables`, so `regclassout`'s `LookupTableByOID` / `tableByOID` scan could not
name it. The spec's setup does:

```sql
SELECT INTO r reltoastrelid::regclass::text AS table_name FROM pg_class
  WHERE oid = 'reind_con_wide'::regclass;
EXECUTE 'ALTER TABLE ' || r.table_name || ' RENAME TO reind_con_toast;';
```

so `table_name` must read `pg_toast.pg_toast_<oid>` (schema-qualified, because
the `pg_toast` namespace is never in `search_path` — PG's `regclassout` always
qualifies it).

## What this slice landed

- **`catalog.tableHasToastRelation(t)`** (new) — extracts the slice-1
  `hasToastRel` gate into a single function so the virtual-builder gate and the
  new OID→name resolver share one source of truth (a toastable user `r`/`m`
  table, or any table with explicit `toast.*` reloptions; system catalogs
  excluded). The builder now calls it instead of inlining the predicate, so
  exposure and `::regclass` rendering can never diverge.
- **`(*InMemory).ToastRelName(oid)`** (new) — for an OID in the TOAST range
  (`>= toastRelidOffset`), reconstructs `pg_toast.pg_toast_<parentOID>` when the
  parent table still owns an auto-exposed TOAST relation; returns `false`
  otherwise (below range, parent dropped, or parent no longer toastable).
- **`internal/executor/expr.go`**, the `CastExpr` `regclass` arm — after the
  `LookupTableByOID` scan misses, fall through to `im.ToastRelName(...)` and
  return its schema-qualified name. The `InvalidOid → "-"` guard from
  [0118-0083](0118-0083-reindex-concurrently-toast-blocker.md) still runs first,
  and a non-zero OID matching neither a real relation, a toast relation, nor an
  index still falls through to PG's numeric rendering.

## Oracle

`src/backend/utils/adt/regproc.c` (`regclassout` → schema-qualifies relations
not visible via `RelationIsVisible`; `pg_toast` is never search-path-visible);
`src/include/catalog/pg_class.h` (`reltoastrelid`).

## Why the spec is still deferred

Steps 3–5 remain: synthesize the TOAST index (`pg_toast_<oid>_index`) in
`pg_index`/`pg_class` so `SELECT indexrelid … WHERE indrelid = …` finds it
(slice 3); `ALTER TABLE/INDEX … RENAME` on a `pg_toast` relation under
`allow_system_table_mods` plus `pg_toast.<name>` resolution (slice 4); and
`REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name>` routing, which rides the
0118-0029 `waitForRelationLockers` CONCURRENTLY contract (slice 5). Recorded in
the deferral ledger; the spec row stays `defer`.

## Gates

- `go build ./...` clean; `gofmt`/`go vet ./internal/catalog/ ./internal/executor/`
  clean.
- New unit test `internal/executor/toast_relation_exposure_test.go`
  (`TestReltoastrelidRegclassRendersToastName`): for `reind_con_wide(id int, data
  text)`, `reltoastrelid::regclass::text` resolves to `pg_toast.pg_toast_<oid>`.
- `TestToastRelationAutoExposed`, `TestRegclassInvalidOidRendersDash` still PASS.
- **Blast-radius parity suites all PASS** (naming the toast relation could change
  `\d`/regclass output): `TestPort_PgDumpConnectionSetup`, `TestPort_PgDump*`,
  `TestPort_PgAmcheck*` (incl. `alltables`/`002` whole-DB walks),
  `TestPort_Scripts*`.
- `go test ./internal/catalog/ ./internal/executor/` PASS;
  `TestPort_IsolationPlpgsqlToast`, `TestPort_IsolationReindexConcurrently` PASS.
- pgbench TPC-B smoke = pre-commit hook.
