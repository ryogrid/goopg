# 0118-0086 — TOAST-relation index exposure, slice 3 (M0118-0008)

**Status:** accepted
**Epic:** TOAST-exposure for `reindex-concurrently-toast` (mapped in
[0118-0083](0118-0083-reindex-concurrently-toast-blocker.md); slice 1 =
[0118-0084](0118-0084-toast-relation-auto-exposure-slice1.md), slice 2 =
[0118-0085](0118-0085-toast-relation-regclass-name-slice2.md))
**Spec:** `postgres/src/test/isolation/specs/reindex-concurrently-toast.spec`
(still `defer` — slices 4–5 remain)

## Problem

The `reindex-concurrently-toast` spec's `setup` block discovers the toast
relation's index by name so it can rename it:

```sql
SELECT INTO r indexrelid::regclass::text AS index_name FROM pg_index
  WHERE indrelid = (SELECT oid FROM pg_class WHERE relname = 'reind_con_toast');
EXECUTE 'ALTER INDEX ' || r.index_name || ' RENAME TO reind_con_toast_idx;';
```

Slices 1–2 exposed the TOAST relation (`relkind='t'` `pg_toast_<oid>` row) and
made `reltoastrelid::regclass` render `pg_toast.pg_toast_<oid>`. But PostgreSQL
also auto-creates a **unique btree index** on every TOAST relation —
`pg_toast_<oid>_index` on `(chunk_id, chunk_seq)` — and goopg exposed neither a
`pg_index` row nor a `pg_class` `relkind='i'` row for it. The join above
therefore returned zero rows, so the index name was never resolved.

## Change

Catalog-only (virtual builders + regclass resolution); no executor/storage
change — goopg has no real TOAST index, so this is catalog/dump-only, exactly
like the slice-1 TOAST relation.

1. **`toastIndexOidOffset = 200_000_000`** (`catalog.go`) — synthetic OID of a
   TOAST relation's index = parent table OID + 200M. Kept a full 100M above
   `toastRelidOffset` (100M) so the index range `[200M, 300M)` never overlaps the
   TOAST relation range `[100M, 200M)` for any realistic user OID (< 100M).

2. **`pg_class` virtual builder** — inside the existing `if hasToastRel` block,
   after the `relkind='t'` TOAST row, emit a `relkind='i'` row:
   `relname = pg_toast_<oid>_index`, OID = `OID+200M`, `relnamespace=99`
   (`pg_toast`), `relam=403` (btree), `relnatts=2`. The TOAST relation row's
   `relhasindex` flips `f → t`.

3. **`ToastRelName(oid)`** — now resolves the index range first: an OID
   `>= toastIndexOidOffset` whose parent still owns an auto-exposed TOAST
   relation renders `pg_toast.pg_toast_<parentOID>_index`. The TOAST relation
   range below it is unchanged. The `expr.go` `CastExpr` regclass arm already
   falls through to `ToastRelName` after a `LookupTableByOID` miss, so
   `indexrelid::regclass` now names the index.

4. **`pg_index` virtual builder** — after the real-index loop, emit one toast
   index row per toast-bearing table: `indexrelid = OID+200M`,
   `indrelid = OID+100M`, unique, `indkey = "1 2"` (chunk_id, chunk_seq).

5. **`toastBearingTables()`** — new helper enumerating the **same** table set the
   `pg_class` main loop emits TOAST rows for (system-virtual catalogs skipped;
   `tableHasToastRelation` keeps only relkind `r`/`m`). Both the `pg_class` TOAST
   emission and the `pg_index` toast-index emission gate on the shared
   `tableHasToastRelation`, and the `pg_index` builder iterates this helper, so
   the two catalogs cannot diverge into an `indexrelid` with no `pg_class` row
   (sibling-path invariant).

## Blast radius

Relation/index enumeration feeds `pg_dump`/`pg_amcheck`/`\d` parity. The toast
index sits in the `pg_toast` namespace with `indrelid` = the (non-dumped) TOAST
relation OID, so `pg_dump`'s `getIndexes` (scoped to dumped tables) never picks
it up, and `pg_amcheck`'s whole-DB walk does not open it (verified — see gates).

## Gates

- New `TestToastRelationIndexExposed` (pg_class `relkind='i'` row + `relhasindex`,
  pg_index row, and the end-to-end `indexrelid::regclass` join the spec uses).
- `TestToastRelationAutoExposed` / `TestReltoastrelidRegclassRendersToastName`
  still PASS.
- All blast-radius parity suites PASS: `PgDump001Basic`, `PgDumpConnectionSetup`,
  `PgAmcheck*` incl. `AllTables`/`002`/`BtreeIndexCheck` whole-DB walks,
  `Scripts*`.
- `IsolationReindexConcurrently` / `IsolationPlpgsqlToast` PASS.
- `go test ./internal/{catalog,executor}/` PASS; build/gofmt clean; pgbench
  smoke = pre-commit hook.

## Remaining (spec stays `defer`)

- **Slice 4:** `pg_toast.pg_toast_<oid>` / `…_index` RENAME under
  `allow_system_table_mods` + `pg_toast.<newname>` name resolution.
- **Slice 5:** `REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name>` routing
  (rides 0118-0029's `waitForRelationLockers`).
