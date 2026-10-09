# M0146-0039: temp relations resurrect as permanent tables after a restart

Status: landed 2026-10-02 (banner item 2a, S2).

## Defect

`execCreateTable` syncs every new table to the on-disk pg_class and
pg_attribute heaps, temp tables included. `buildUserPGClassRow` assumed
temp tables never reach disk, so it wrote `relpersistence = 'p'` in
namespace public (2200). Startup's `loadUserTablesFromHeapForDB` reloads
every user pg_class row, so after a restart:

- a session's `TEMP TABLE ca` came back as a permanent public table, with
  its rows, visible to every session;
- a new `CREATE TEMP TABLE ca` failed with `relation "ca" does not exist`;
- the table's serial sequence and an explicit `CREATE TEMP SEQUENCE` came
  back the same way, since their catalog rows never carried the temp flag.

Reproducer: `analysis/m0146/m0146-0039/repro.sh`.

## PostgreSQL behaviour

A temp relation has `relpersistence = 't'` and lives in its backend's
private `pg_temp_N` namespace. Indexes and the implicit serial sequence
take the table's persistence (`generateSerialExtraStmts`).

A backend drops its temp relations when it exits. Leftovers from a crash
are removed when a backend next takes that namespace
(`InitTempTableNamespace` → `RemoveTempRelations`, namespace.c) or by
autovacuum's orphan cleanup. They are never visible to another session.

## Change

- `buildUserPGClassRow` writes `t` for `Temp` tables, and
  `indexPersistence` writes `t` for indexes on them.
- `createSeqCatalogTable` takes a `temp` flag and stamps `Temp` /
  `TempOwner` before the heap sync:
  - an explicit `CREATE TEMP SEQUENCE` passes `s.Temporary`;
  - a serial / identity column passes its table's `Temp`, and also marks
    the sequence registry entry temporary.
- Startup skips `t` rows in both the table/sequence loader
  (`loadUserTablesFromHeapForDB`) and the index loader
  (`loadUserIndexesFromHeapForDB`). No backend owns them after a restart.

## Tests and verification

`internal/initdb/temp_relation_restart_test.go`: a temp table with a
serial key and an index, plus a temp sequence and a permanent table, go
through a restart. Only the permanent table comes back. Without the change
all five temp relations come back.

The reproducer now ends with only `keep` in pg_class and a fresh
`CREATE TEMP TABLE ca` succeeding.

Other gates:

- Isolation specs inherit-temp and temp-schema-cleanup pass.
- Regress `temp` and `sequence` are unchanged against a HEAD baseline.
- Units, spotcheck, sweep 96/96, arm, fire set and ea-ratchet pass.

## Left open (ledgered)

- The temp rows still name namespace 2200, not the session's
  `pg_temp_N` OID.
- Leftover temp pg_class / pg_attribute rows and relfiles stay on disk.
  They are skipped, not removed. Session exit (`DropSessionTempObjects`)
  is still in-memory only, where PG's would delete the rows and unlink
  the files.
