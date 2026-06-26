# 0118-0087 — TOAST-relation RENAME under allow_system_table_mods (slice 4, M0118-0008)

Status: accepted
Date: 2026-06-24
Spec: `postgres/src/test/isolation/specs/reindex-concurrently-toast.spec`
Epic: TOAST-exposure (slices 1–5), mapped in 0118-0083; builds on
0118-0084 (slice 1, auto-exposure), 0118-0085 (slice 2, `reltoastrelid::regclass`
name), 0118-0086 (slice 3, toast index in `pg_class`/`pg_index`).

## Problem

After slices 1–3 the synthetic TOAST relation (`pg_toast.pg_toast_<oid>`) and its
unique btree index (`pg_toast.pg_toast_<oid>_index`) are visible in the `pg_class`
/ `pg_index` virtual builders and name correctly through `::regclass`. The spec's
**global setup** then renames them to deterministic names so `REINDEX …
CONCURRENTLY` can address them (TOAST names are non-deterministic in PG, hence the
`allow_system_table_mods` dance):

```sql
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

Two failures blocked this in goopg:

1. `ALTER TABLE pg_toast.pg_toast_<oid> RENAME TO reind_con_toast` →
   `relation "pg_toast.pg_toast_<oid>" does not exist`. The synthetic TOAST rows
   live **only** in the virtual builders, not in `c.tables`, so
   `lookupTableWithSearch` (and `LookupIndex`) can't find them — there is no real
   row to mutate.
2. `ALTER INDEX pg_toast.pg_toast_<oid>_index RENAME TO reind_con_toast_idx` →
   `relation "" does not exist`. The `ALTER INDEX` parser path had **no RENAME
   arm**: `RENAME TO …` fell into its catch-all "consume rest as no-op" branch
   which returned an `AlterTableStmt` with an **empty `Name`**, so the executor
   never saw the object name.

## Design

A rename of a synthetic catalog object can't mutate a heap row; instead record a
**name override** the virtual builders, `::regclass` rendering and name→OID lookup
all consult.

### Catalog (`internal/catalog/catalog.go`)

- New field `toastRenames map[uint32]string` on `InMemory`, keyed by the synthetic
  OID (parent OID + `toastRelidOffset` for the relation, + `toastIndexOidOffset`
  for its index) → new unqualified name. Initialised in the constructor. Not
  transaction-scoped (only this spec mutates synthetic TOAST objects, always in
  autocommit; see Limitations).
- `toastDisplayNameLocked(oid, deflt)` — returns the override if present, else the
  default `pg_toast_<oid>[_index]`. Caller holds `c.mu`.
- `RenameToastRel(oid, newName)` — records the override (own lock).
- `LookupToastRel(schema, name) (oid, isIndex, ok)` — resolves a schema-qualified
  `pg_toast.<name>` to its synthetic OID. Tries the override map first (any entry
  whose value equals `name`, validated against a still-live parent), then the
  default `pg_toast_<parentOID>[_index]` pattern (parse the OID, confirm the
  parent still owns an auto-exposed TOAST relation). Reusable by slice 5's
  `REINDEX … CONCURRENTLY pg_toast.<name>` routing.
- `ToastRelName(oid)` (slice 2 regclass resolver) now renders the override via
  `toastDisplayNameLocked`, so `indexrelid::regclass` of a renamed object names
  the new name.
- The `pg_class` virtual builder's two synthetic rows (TOAST relation, TOAST
  index) emit `relname` via `toastDisplayNameLocked` instead of the hard-coded
  default. `pg_index` carries no names, so it is unchanged.

### Parser (`internal/parser/ddl.go`)

- The `ALTER INDEX` branch gains a `RENAME TO newname` arm (mirroring the
  `ALTER TABLE` one) that returns an `AlterTableStmt{Name: idxName}` with an
  `AlterTableRenameTable` action — so the object name is preserved and the
  executor can intercept it. Previously this fell into the no-op branch with an
  empty `Name`.

### Executor (`internal/executor/operators_ddl.go`)

- In `execAlterTable`, when `lookupTableWithSearch` and `LookupIndex` both miss
  and the object is in the `pg_toast` schema, a `RENAME TO` action is resolved via
  `InMemory.LookupToastRel`; on a hit it records the override with
  `RenameToastRel` and returns success. Placed after the existing
  implicit-sequence rename fallback, before the `42P01` error.

`ALTER … RENAME` on a real index still routes to the existing index branch
(silent no-op in v0, unchanged) — only the `pg_toast` synthetic case is
intercepted.

## Why this is correct / bounded

- The override map is empty in every flow except this spec's setup, so
  `toastDisplayNameLocked` returns the default and the `pg_class`/`pg_index`/
  `regclass` output is **byte-identical** to slice 3 for all other tests
  (`pg_dump`, `pg_amcheck`, every isolation spec). The change is provably inert
  when no synthetic TOAST object has been renamed.
- `LookupToastRel` re-validates the parent table on every lookup, so a stale
  override left after the parent is dropped (teardown `DROP TABLE`) resolves to
  nothing and the builders emit no row.

## Limitations (carried to the ledger)

- `allow_system_table_mods` is **not enforced** — goopg accepts the rename of a
  synthetic TOAST object unconditionally. The spec always sets the GUC, so output
  matches; enforcing it is unnecessary for any `port` spec.
- The override is **not transaction-scoped** (no ROLLBACK revert). Only this spec
  mutates synthetic TOAST objects, always in autocommit, so it never matters in
  practice.

## Result

The spec's global setup now runs end-to-end byte-for-byte (`ALTER TABLE` →
`reind_con_toast`, `ALTER INDEX` → `reind_con_toast_idx`, both visible in
`pg_class.relname` and through `indexrelid::regclass`). The first divergence
advances from the global setup to the per-permutation `REINDEX TABLE/INDEX
CONCURRENTLY pg_toast.<name>` steps (which still report
`relation "pg_toast.reind_con_toast" does not exist`). **Spec stays `defer`** —
slice 5 (REINDEX routing of a `pg_toast.<name>` synthetic object + the
`<waiting …>` concurrency riding 0118-0029 `waitForRelationLockers` on the parent
table) is the final remaining slice.

## Gates

- New `TestToastRelationRenameViaAlter` (rename rel + index, verify
  `pg_class.relname`, `pg_class WHERE relname=…` OID resolution, and
  `indexrelid::regclass::text` rendering the renamed name).
- `TestToastRelationAutoExposed` / `TestReltoastrelidRegclassRendersToastName` /
  `TestToastRelationIndexExposed` PASS (no slice 1–3 regression).
- `go test ./internal/{catalog,parser}/` PASS; executor toast/reindex/alter
  parse tests PASS.
- Strict isolation siblings PASS: `IsolationReindexConcurrently`,
  `IsolationReindexSchema`, `IsolationMultipleCic`,
  `IsolationDropIndexConcurrently1`, `IsolationAlterTable3`.
- Live probe: `reindex-concurrently-toast` global setup now passes; divergence at
  the REINDEX steps (slice 5).
- `go build ./...` clean; pgbench smoke = pre-commit hook.
