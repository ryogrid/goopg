# 0119-0004 — REPLICA IDENTITY round-trip in pg_dump (DU-002 slices 305/306)

Status: implemented (2026-06-29, loop #26 slice 305; loop #27 slice 306 USING INDEX)
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

## USING INDEX (`relreplident = 'i'`) — slice 306

For `REPLICA IDENTITY USING INDEX idx`, pg_dump sets `relreplident='i'` (and
emits nothing at table-dump time) and instead emits the clause **at index-dump
time** keyed on `pg_index.indisreplident` for the chosen index:

```c
/* pg_dump.c dumpIndex, ~line 18186 */
if (indxinfo->indisreplident) {
    appendPQExpBuffer(q, "\nALTER TABLE ONLY %s REPLICA IDENTITY USING",
                      fmtQualifiedDumpable(tbinfo));
    appendPQExpBuffer(q, " INDEX %s;\n", qindxname);   /* index name unqualified */
}
```

Implementation (slice 306):

1. **New catalog field** `catalog.Index.IsReplicaIdentity bool`, mirroring
   `pg_index.indisreplident`. Projected to the column at **both** pg_index
   builders — the **virtual** one pg_dump reads (`catalog.go` `VirtualRows`,
   `boolStr(idx.IsReplicaIdentity)`) and the **heap** one
   (`buildUserPGIndexRow`, restart durability). The TOAST-index synthetic row
   stays `'f'`.
2. **Executor** (`operators_ddl.go` `AlterTableReplicaIdentity` case): the `'i'`
   form is now accepted. `resolveReplicaIdentityIndex` validates the named index
   exactly as PG's `check_replica_identity` (`tablecmds.c`): it must exist on the
   table (`42704`), be UNIQUE (`42809 cannot use non-unique index`), IMMEDIATE
   i.e. not `DEFERRABLE INITIALLY DEFERRED` (`0A000 non-immediate`), not an
   expression index (`0A000 expression index`), not partial (`0A000 partial
   index`), and every key column NOT NULL (`42809 … is nullable`). The table's
   `relreplident` becomes `'i'`, and — mirroring `relation_mark_replica_identity`
   — the chosen index's `IsReplicaIdentity` is set while every other index of the
   table is cleared, re-syncing the pg_index **heap** row of any index whose flag
   actually changed (`resyncIndexReplicaIdentHeap`: stamp-old-row +
   `writeHeapRowCanonical`, the delete-old-rows pattern from the table path).

Like the table case this is **dump-fidelity only** (no logical replication). The
in-memory `IsReplicaIdentity` flag is not persisted to the index-DDL WAL record
(like `Index.DeclaredHash`); the heap re-sync keeps a restarting/standby backend
consistent, and a default-identity table is unaffected.

## Tests

- `parser`: `TestParseAlterTableReplicaIdentity` (all four forms; mode + index).
- `executor`: `TestUserPGClassRowReplicaIdentity` (heap pg_class builder emits
  `'d'` default, `'f'`, `'n'`); `TestUserPGIndexRowReplicaIdentity` (heap
  pg_index builder projects `IsReplicaIdentity` → `indisreplident`).
- `testport`: slices 305/306 in `TestPort_PgDumpConnectionSetup` — real pg_dump
  18.3 against a live goopg server: `ri_full`→FULL clause, `ri_nothing`→NOTHING
  clause; `ri_index`+`ri_uidx`→`ALTER TABLE ONLY public.ri_index REPLICA
  IDENTITY USING INDEX ri_uidx;`; `foo`/`bar`/`part` (default identity) emit
  **no** REPLICA IDENTITY clause (the spurious-NOTHING regression guard).

## Blast radius

Zero on query execution / DML. The default flips from a wrong `'n'` to the
correct `'d'`, removing spurious dump lines. The new `Index.IsReplicaIdentity`
field defaults false (⇒ `indisreplident='f'`, the prior hard-wired value), so
every existing index dumps byte-identically; only an explicit `USING INDEX`
ALTER sets it. pgbench/TPC-H tables carry no override.
