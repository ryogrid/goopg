# 0118-0084 — TOAST-relation auto-exposure, slice 1 (M0118-0008)

Status: accepted (epic slice 1 of N; `reindex-concurrently-toast` still NOT promoted)

## Context

`reindex-concurrently-toast` is the **last** unpromoted spec in the M0118-0008
maintenance-concurrency group (the other 24 pass byte-for-byte). Its setup block
depends on a **real auto-created TOAST relation exposed as a catalog object** —
`SELECT reltoastrelid::regclass FROM pg_class WHERE oid='reind_con_wide'::regclass`
must resolve, then the toast relation is `ALTER … RENAME`d and
`REINDEX … CONCURRENTLY pg_toast.<name>`d. The full blocker map is in
[0118-0083](0118-0083-reindex-concurrently-toast-blocker.md), which enumerates a
5-step epic:

1. **Auto-expose `reltoastrelid` + a `pg_toast.pg_toast_<oid>` `pg_class` row** for
   every toastable table (not only those with explicit `toast.*` reloptions). ← **this slice**
2. Resolve the TOAST OID → name in `LookupTableByOID` (so `reltoastrelid::regclass`
   renders the name, not numeric).
3. Synthesize the TOAST index (`pg_toast_<oid>_index`) in `pg_index` / `pg_class`.
4. `ALTER TABLE/INDEX … RENAME` on a `pg_toast` relation + `pg_toast.<name>` resolution.
5. `REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name>` routing (rides 0118-0029).

## What this slice landed

The pg_class virtual builder (`internal/catalog/catalog.go`) previously emitted a
synthetic `relkind='t'` TOAST row **only when the table carried explicit `toast.*`
storage parameters** (`len(t.ToastReloptions) > 0`, DU-002 slice 224). PostgreSQL
auto-creates a TOAST relation for **any** ordinary table / materialized view with
at least one toastable (varlena) column (`needs_toast_table`,
`src/backend/catalog/toasting.c`), so `reind_con_wide(id int, data text)` has
`reltoastrelid != 0` with no reloptions at all.

This slice widens the gate to mirror PG:

```go
hasToastRel := len(t.ToastReloptions) > 0 ||
    (!IsSystemRelation(t.OID) && (relkind == "r" || relkind == "m") &&
        tableNeedsToastRelation(t))
```

- **`tableNeedsToastRelation(t)`** (new) mirrors `needs_toast_table`: true when the
  table has any non-dropped column of a toastable type. Toastability is decided by
  **`columnTypeIsToastable`** (new), whose varlena type-name set is kept in sync
  with the executor's `isToastableType` (`internal/executor/toast.go`) — `text`,
  `varchar`/`character varying`, `char`/`character`/`bpchar`, `bytea`, `json`,
  `jsonb`, `jsonpath`, `xml`, plus any array column (`Type.IsArray`).
- The toast row's `reloptions` cell is now **NULL** unless explicit `toast.*`
  params exist (previously it always rendered `{…}` because the gate implied them).
- `reltoastrelid` on the parent now points at `OID + toastRelidOffset` (the
  existing `100_000_000` convention, matching `internal/executor/toast.go`).

## Blast-radius decision: USER relations only

The critical scoping constraint, discovered by measurement (see below):
**auto-exposure is restricted to user relations (`!IsSystemRelation(t.OID)`,
OID ≥ 16384).**

goopg serves system catalogs (`pg_type`=1247, `pg_attribute`=1249, …) virtually
with **no real heap storage**. The first un-scoped attempt attached `reltoastrelid`
to those catalog `Table` entries too; `pg_amcheck`'s whole-DB walk then **follows
`reltoastrelid`** to verify the toast heap and failed with
`could not open relation: relation does not exist` for `pg_toast_1247` /
`pg_toast_1249` (`TestPort_PgAmcheck002Nonesuch` and three sibling cases). In real
PG those toast heaps exist and `verify_heapam` passes; goopg cannot back them.
Explicit `toast.*` reloptions only ever land on user tables, so the
narrowed gate preserves the prior DU-002 slice-224 behaviour exactly while adding
auto-exposure for user tables.

Partitioned parents (`relkind='p'`, no storage), views, sequences and foreign
tables are also excluded — only `relkind IN ('r','m')` qualify, matching PG.

## Why the spec is still deferred

Steps 2–5 remain. In particular `reltoastrelid::regclass` still renders the
numeric OID (not `pg_toast.pg_toast_<oid>`) because the synthetic row lives only in
the virtual pg_class builder output, not in `c.tables` / `tableByOID` — so
`LookupTableByOID` (which `regclassout` consults) can't name it yet. That is
slice 2. Recorded in the deferral ledger; the spec row stays `defer`.

## Gates

- `go build ./...` clean; `gofmt`/`go vet ./internal/catalog/` clean.
- New unit test `internal/executor/toast_relation_exposure_test.go`
  (`TestToastRelationAutoExposed`): a user table with a `text` column gets
  `reltoastrelid = OID+offset` and a `relkind='t'` `pg_toast_<oid>` row; an
  all-fixed-width table gets neither.
- **Blast-radius parity suites all PASS** (the whole point of this slice):
  `TestPort_PgDumpConnectionSetup`, `TestPort_PgDump*`, `TestPort_PgAmcheck*`
  (all variants, incl. the whole-DB `alltables` / `002` walks), `TestPort_Scripts*`.
- `go test ./internal/catalog/ ./internal/executor/` PASS;
  `TestPort_IsolationPlpgsqlToast`, `TestPort_IsolationReindexConcurrently` PASS.
- pgbench TPC-B smoke = pre-commit hook.

## Oracle

`src/backend/catalog/toasting.c` (`needs_toast_table`,
`create_toast_table`); `src/backend/utils/adt/regproc.c` (`regclassout`);
`src/include/catalog/pg_class.h` (`reltoastrelid`).
