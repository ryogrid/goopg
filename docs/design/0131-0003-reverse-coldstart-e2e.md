# Reverse cold-start E2E — goopg serves a cluster directory real PG created and wrote

**Status:** draft
**Date:** 2026-08-11
**Milestone:** M0131 (S3)

## Problem

`0130-0002` §"Remaining for full reverse-path parity" item 3 asks for one test —
*"start a real PG instance, create tables, shut it down cleanly, start goopg
against the same `$PGDATA`, and verify `SELECT * FROM <user_table>` returns the
correct rows"* — and defers it for a reason that no longer holds ("needs a
test-harness PG instance lifecycle (M0130-S10)"; the harness landed). What stands
in for it is a *simulation*, `TestReversePathColdStartOpensWithoutCache`
(`internal/initdb/reverse_path_test.go`, 60 lines): `Init(Options{DataDir: dir})`
(`:23`), delete `catalogCachePath(dir, 1)` and `…, 5)` (`:31-34`) to force the
cold-start arm, `Open` (`:36`), assert `pg_class`/`pg_type`/`pg_attribute` resolve
in the in-memory catalog (`:45-57`). That proves `Open` survives a missing catalog
cache and that `registerSystemTables()` populated the system set — and nothing
about PG. No PG binary runs; the heap scanned is goopg's own; there is no user
table, so `loadUserTablesFromHeapForDB` returns early at
`internal/initdb/open.go:2905-2907` and the physical decoder never meets
PG-authored bytes. Its doc comment (`:16-20`) states the inference it substitutes
for evidence: because goopg's heap is PG-compatible, "the same
`DecodePGClassPhysicalRow` fallback that reads PG-created rows also reads
goopg-created rows". S3 measures the converse.

## Design

### Test shape

`TestE2E_GoopgColdStartOnPGDataDir`, `internal/testport/`, gated like its siblings
on `testing.Short()` and `pgcluster.Available(t, binDir)`
(`internal/testutil/pgcluster/cluster.go:91-99`).

1. `pgcluster.New` (`:103-115`) runs the real `initdb` (`runInitdb`, `:183-195`:
   `-D`, `-U <user>`, `--auth-local=trust --auth-host=trust --no-sync`) then
   `appendConf` (`:197-229`). Pass `Options{User: "postgres"}` so the PG-created
   `pg_authid` row matches the superuser goopg's harness connects as
   (`internal/testutil/cluster/cluster.go:126-129`; pgcluster otherwise defaults
   to `$USER`, `:158-164`).
2. `Start()` (`:236-289`), then the workload over `psql` (`Exec`, `:362-372`):
   CREATE SCHEMA, CREATE TABLE, CREATE INDEX, INSERT, UPDATE, DELETE.
3. `Stop()` (`:293-305`) sends `os.Interrupt` — PG's fast shutdown, which runs a
   shutdown checkpoint and leaves `pg_control.State = DB_SHUTDOWNED`. Assert that
   byte before handing the directory over (S3.3).
4. goopg on the same directory: `cluster.New(…, Options{DataDir: pgDir})`
   (`cluster.go:95`) then `Start()` (`:214`, `goopg start -D <dir> -listen <addr>`)
   with **no** `Init()`. `New` only builds a handle; `Init` (`:172`) is what would
   run `goopg init` (`initDataDir`, `:189-210`) and append `fsync = off`
   (`:176-180`), so skipping it leaves PG's `postgresql.conf` untouched — the point
   of the test. (The prior art sets `SyncInit`/`SyncRuntime` true,
   `e2e_failover_pg_to_goopg_test.go:139-140`; both gate `Init` only.)
5. `SELECT`s over the goopg connection return the PG-written rows.

### Preconditions: clean source, S1, S2

`0130-0002` §"WAL replay constraint" is normative: for a cleanly shut down PG
data dir `replayStart` finds the shutdown checkpoint (via `isCheckpointRecord` /
`xlogCheckpointShutdown`) and positions past it — replay is a no-op. An unclean
dir carries post-checkpoint records with PG-native resource managers goopg's
`replayDecodedXLogRecord` does not handle, and replay fails with
`unsupportedDecodedXLogRecord`. Step 3's assertion is a precondition check.

**S1 (GUC registry)** gates step 4: goopg exits before opening a catalog page if
`postgresql.conf` carries an unregistered parameter. The test must rewrite
nothing — if it still needs a conf edit, S1 is incomplete. This is the opposite
of the standby lane, where `configureGoopgStandbyFromPGBackup`
(`e2e_failover_pg_to_goopg_test.go:302-313`) *overwrites* `postgresql.conf` with
three goopg lines and drops a `standby.signal`. S3 does neither.
**S2 (system identifier)**: without it goopg invents a fresh random ID for a
directory whose `pg_control` already records one; assert equality after start.
`verifyInitialized` (`open.go:3263-3283`) is shallow by design — `PG_VERSION`
compared to `CatalogVersion` = `config.MajorVersion` = `"18"`
(`internal/initdb/initdb.go:82`, `internal/config/version.go:8`) — so a PG 18.3
directory passes it. `normalizePGWALSegmentNames`
(`e2e_failover_pg_to_goopg_test.go:315-339`, still test-local) round-trips each
name through `wal.ParseXLogFileName(name, 16<<20)` → `wal.XLogFileName(tli, segno,
16<<20)` (`internal/wal/xlog_page.go:207-217`), which reads as an identity for any
well-formed 24-character name: **probe whether the new test needs it at all** — if
it does, that is a finding about goopg's segment naming on a non-basebackup
directory and gets a ledger row, not a silent helper call (S3.4).

### What may legitimately be asserted

The cold-start arm triggers on the absence of `pg_goopg_catalog_cache.json`
(`0130-0002` §Detection); a PG directory never has one. `Open` (`open.go:269`)
reads `pg_control.DataChecksumVersion` up front (`:286-294`), then
`loadUserTablesFromHeap` (`:2833`) → `loadUserTablesFromHeapForDB` (`:2845-3055`)
scans `base/<dboid>/1259` and `…/1249` with a two-tier decode (`:2864-2894`):
`DecodePGClassRow` first, then `DecodePGClassPhysicalRow`; a physical-layout row
also switches off the `requireCommittedXmin` rule (`:2893`), which is what lets
foreign-XID rows through. Only `relkind ∈ {r,m,v,S}` at `OID >=
catalog.FirstUserOID` (16384, `internal/catalog/catalog.go:3604`) survives the
filter at `:2901`. `DecodePGClassPhysicalRow` (`internal/catalog/codec.go:856-901`; doc comment
`:853-855` — "only the non-null fixed fields needed by `loadUserTablesFromHeap`
are decoded") reads exactly ten columns: `oid`, `relname`, `relnamespace`,
`relkind`, `relnatts`, `relfilenode`, `reltablespace`, `relpersistence`,
`relisshared`, `relispopulated`. The other 23 fields of `FormData_pg_class`
(`postgres/src/include/catalog/pg_class.h:35-143`) are dropped — `reltype`,
`reloftype`, `relowner`, `relam`, the four statistics fields, `reltoastrelid`,
`relhasindex`, `relchecks`, the four `relhas*`/`relrowsecurity` flags,
`relforcerowsecurity`, `relreplident`, `relispartition`, `relrewrite`,
`relfrozenxid`, `relminmxid`, `relacl`, `relpartbound` — with `reloptions` the one
out-of-band exception, re-decoded through `executor.DecodeRowIntoMctxPGTuple` at
`:2880-2888`. Assertions therefore cover relation identity, columns and **row
contents**, never ownership, ACLs, triggers, RLS, replica identity or
partitioning. Anything the workload creates that the decoder cannot see is an
explicit decision: assert it, ledger it, or widen the decoder (S3.5). Two probes,
both hedged: `reloadUserSchemasFromHeap` runs before the table load precisely so
`relnamespace` reverse-maps to a schema name (`open.go:1319-1330`) — verify it
decodes PG-authored `pg_namespace` rows before asserting on `CREATE SCHEMA`; and
`detectCatalogDBOID` (`:3057-3091`) finds the `postgres` OID by scanning
`global/1262`, after which the main pass *reads* `base/<cat.DBOID()>` but
*registers* into `DefaultDBOid` = 1 (`:3296-3304`; same fold at
`internal/initdb/catalog_heap_reload.go:2306`). goopg's own dirs rely on
`mirrorTouchedCatalogsToPostgresDB` for that asymmetry and a PG dir has no
mirroring, so keep the workload in database `postgres` (pgcluster's default,
`cluster.go:165-168`) and probe that a goopg `postgres` connection sees it.

### Two asymmetries the test must respect

**`pg_filenode.map` is written and never read** by goopg — writers at
`internal/initdb/initdb.go:136`, `:1960`, `:2166`; no reader anywhere — so goopg
addresses catalog relfiles by OID. True on a fresh initdb, where `base/5/1247`,
`1249`, `1259` are literally named by OID; false the moment a mapped catalog is
rewritten under a new relfilenode. `VACUUM FULL` / `CLUSTER` / `REINDEX` on a
catalog therefore stay out of the workload, with the reason in a comment; ledger
row #388 names this as its re-arm trigger.

**Checksum state differs, but goopg adapts.** PG 18's `initdb` defaults
`data_checksums` to **true** (`postgres/src/bin/initdb/initdb.c:167`), so the
handed-over directory is checksummed. goopg's `init` **CLI** also defaults it true
(`cmd/goopg/main.go:184`); it is the library entry point `initdb.Init(Options{})`
that leaves it off via the Go zero value — the path `reverse_path_test.go:23`
takes. Either way `Open` reads the flag from `pg_control` and configures the
storage `Manager` accordingly (`open.go:286-294`). The comment at `:290` calling
`0` "the goopg default" is stale relative to `main.go:184`; correct it here.

## Guards

1. `TestE2E_GoopgColdStartOnPGDataDir` runs the full chain: real `initdb` → PG
   workload (CREATE SCHEMA / TABLE / INDEX, INSERT, UPDATE, DELETE) → `Stop()` →
   goopg `Start()` on the same directory → `SELECT`.
2. `pg_control.State == DB_SHUTDOWNED` asserted between the stop and the goopg
   start, so the unclean-source case fails loudly instead of being entered.
3. goopg starts with **zero** edits to `postgresql.conf` and no `standby.signal`;
   any required edit is an S1 defect and gets a ledger row, not a workaround.
4. The system identifier goopg reports after start equals `pg_control`'s (S2).
5. Row-level assertions cover only fields the physical decoders recover; the
   excluded pg_class columns are named in a comment.
6. `normalizePGWALSegmentNames` probed, not assumed — asserted a no-op or ledgered.
7. No `VACUUM FULL` / `CLUSTER` / `REINDEX` on a catalog, reason in a comment.
8. The E2E family stays green (`go test -v -run '^TestE2E_' ./internal/testport/`).
9. UNITS + SMOKE green.

## References

- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §S3;
  `0131-0001-…-conf-compat.md` (S1); `0131-0002-…-pgcontrol-fallback.md` (S2)
- `docs/design/0130-0002-pg-class-heap-persistence.md` §"WAL replay constraint",
  §"Remaining for full reverse-path parity" item 3
- `internal/testutil/{pgcluster,cluster}/cluster.go`;
  `internal/testport/e2e_failover_pg_to_goopg_test.go` (prior art)
- `internal/initdb/open.go`, `internal/initdb/reverse_path_test.go`,
  `internal/catalog/codec.go`
- `postgres/src/include/catalog/pg_class.h`, `postgres/src/bin/initdb/initdb.c:167`;
  deferral-ledger row #388 (`pg_filenode.map` write-only)
