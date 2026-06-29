# 0119-0004 — REPLICA IDENTITY round-trip in pg_dump (DU-002 slice 305)

Status: implemented (2026-06-29, loop #26)
Milestone: M0119-0004 (pg_dump 002–010 catalog-view parity battery)
Oracle: `postgres/src/bin/pg_dump/pg_dump.c` (`dumpTableSchema`, ~line 17781);
`postgres/src/include/catalog/pg_class.h` (`REPLICA_IDENTITY_*`)

## Problem

pg_dump emits an `ALTER TABLE ONLY <t> REPLICA IDENTITY {FULL|NOTHING}` clause
for a regular / partitioned / matview relation whenever `pg_class.relreplident`
is **not** `'d'` (`REPLICA_IDENTITY_DEFAULT`):

```c
if ((relkind == r || p || m) && relreplident != REPLICA_IDENTITY_DEFAULT) {
    if (relreplident == 'i')      /* nothing to do; set at index-dump time */
    else if (relreplident == 'n') ... "REPLICA IDENTITY NOTHING"
    else if (relreplident == 'f') ... "REPLICA IDENTITY FULL"
}
```

goopg **hardcoded `relreplident = 'n'`** (`REPLICA_IDENTITY_NOTHING`) in the heap
`pg_class` row builder pg_dump actually reads
(`executor/pg18_user_catalog_rows.go::buildUserPGClassRow`, comment mislabelled
it "REPLICA_IDENTITY_DEFAULT"). PG's implicit default is `'d'`. Consequently
**every** table goopg dumped acquired a spurious
`ALTER TABLE ONLY public.<t> REPLICA IDENTITY NOTHING;` — a silent, pervasive
divergence from real pg_dump (which emits nothing for a default-identity table).
It was latent because no existing slice asserted the *absence* of the clause,
and goopg never parsed `ALTER TABLE ... REPLICA IDENTITY`, so a non-default
setting could not round-trip at all.

(The live in-memory virtual `pg_class` builder in `catalog.go`'s `VirtualRows`
already used `'d'`; only the heap builder — the one pg_dump reads, see slice 166
/167 notes — was wrong. The two are siblings and must agree.)

## Fix

1. **Default corrected to `'d'`.** New `catalog.ReplIdentOrDefault(s)` maps an
   unset (empty) code to `'d'`. Both `pg_class` builders route through it:
   `buildUserPGClassRow` (heap, pg_dump) and `catalog.go` `VirtualRows`
   (in-memory).
2. **Round-trip of an explicit setting.** New
   `catalog.Table.ReplicaIdentity string` (single char `'d'`/`'f'`/`'n'`/`'i'`;
   empty ⇒ `'d'`).
   - Parser: `AlterTableReplicaIdentity` action +
     `ReplicaIdentityMode`/`ReplicaIdentityIndex` fields parse
     `REPLICA IDENTITY { DEFAULT | FULL | NOTHING | USING INDEX name }`
     (`FULL`/`NOTHING` arrive as keyword tokens, so both keyword and ident
     spellings are accepted).
   - Executor: the action sets `tbl.ReplicaIdentity` and flushes the pg_class
     **heap** row through the established delete-old-rows + `syncTableToCatalogHeap`
     path (the same mechanism SET STORAGE / SET COMPRESSION / SET STATISTICS use),
     because pg_dump reads the heap, not the live catalog.

goopg has no logical replication, so this is **dump-fidelity only**, exactly like
the existing SET STORAGE / SET COMPRESSION clauses.

## Deferred: USING INDEX (`relreplident = 'i'`)

For `REPLICA IDENTITY USING INDEX idx`, pg_dump sets `relreplident='i'` (emits
nothing at table-dump time) and instead emits the clause **at index-dump time**
keyed on `pg_index.indisreplident` (pg_dump.c:18186). Round-tripping it therefore
requires marking the chosen index's `indisreplident` across the index pg_index
heap + virtual builders — a separate sibling set. Rather than accept-and-silently-
lose the setting, the executor **rejects** the USING INDEX form with `0A000`
("not yet supported"). The parser still recognises the grammar. Tracked in the
deferral ledger.

## Tests

- `parser`: `TestParseAlterTableReplicaIdentity` (all four forms; mode + index).
- `executor`: `TestUserPGClassRowReplicaIdentity` (heap builder emits `'d'`
  default, `'f'`, `'n'`).
- `testport`: slice 305 in `TestPort_PgDumpConnectionSetup` — real pg_dump 18.3
  against a live goopg server: `ri_full`→FULL clause, `ri_nothing`→NOTHING
  clause present; `foo`/`bar`/`part` (default identity) emit **no** REPLICA
  IDENTITY clause (the spurious-NOTHING regression guard).

## Blast radius

Zero on query execution / DML. The default flips from a wrong `'n'` to the
correct `'d'`, removing spurious dump lines. New catalog field defaults
empty (⇒ `'d'`); pgbench/TPC-H tables carry no override.
