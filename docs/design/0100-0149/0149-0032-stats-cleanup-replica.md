# M0149\-0032 — Port recovery/t/030\_stats\_cleanup\_replica.pl

Status: done. TestPort\_Recovery030StatsCleanupReplica is ungated and passes 3 of 3 runs in all three pairings
\(M0149\-0032a–f\).

## Landed — M0149\-0032a, routines on a running standby

- `Runtime.RefreshCatalogAfterReplay` \(the standby's relcache invalidation after a replayed catalog commit\) now
  also runs `reloadUserRoutinesFromHeapForDBMode(replay)`:
  - only rows whose xmin committed count;
  - routines not yet registered are added, and known ones are left as they are;
  - a registered user routine of the database whose pg\_proc row is gone \(a replayed DROP FUNCTION / DROP SCHEMA
    CASCADE\) is unregistered.
- A PostgreSQL primary's pg\_proc row carries no goopg Routine JSON in proargdefaults. `pgProcRowRoutine` builds the
  Routine from the real columns: proname, pronamespace, prolang \(sql/internal/c/plpgsql\), prorettype and
  proargtypes by OID, provolatile, proisstrict, prokind, prosrc.
- `parseOIDVector` reads proargtypes from the raw oidvector array image the heap holds.

## Remaining path \(over the guard's 3 links\)

- M0149\-0032d: schemas the primary creates are not registered on a goopg standby.
- M0149\-0032e: queries on a goopg standby create no relation/function stats.
- M0149\-0032b: goopg commit records carry no dropped\-stats items.
- M0149\-0032c: goopg standbys do not execute replayed stats drops.

## M0149\-0032d — schemas on a running standby \(landed\)

`RefreshCatalogAfterReplay` runs `reloadUserSchemasFromHeapForDBMode(replay)` before the table reload. Only committed
pg\_namespace rows count, new user schemas are registered, and a registered user schema whose row is gone \(a
replayed DROP SCHEMA\) is unregistered \(`catalog.UserSchemasInDB` lists a database's user schemas\). Doing schemas
first lets a relation the primary created in a new schema resolve its namespace when the table reload registers it.
Next on 030's path: M0149\-0032f \(databases created on the primary are not connectable on the standby\).

## M0149\-0032f — databases on a running standby \(landed\)

pg\_database is a shared catalog \(global/1262, DBOid 0\), and the replayer's catalog\-change tracking skipped DBOid 0,
so a replayed CREATE DATABASE never refreshed anything. `noteCatalogBlocks` now marks key 0 for a global/1262 block.
`RefreshCatalogAfterReplay(0)` then runs `reloadDatabasesFromHeapMode(replay)`: committed rows only, new databases
registered, and databases whose row is gone unregistered. This also closed M0149\-0033b: 048's "pg\_stat\_wal\_receiver
is empty" was a refused connection to a database created after the backup. 048's PG\-primary → goopg\-standby pairing
now passes end to end.

## M0149\-0032e — pending stats flush at backend exit \(landed\)

Not standby\-specific: goopg flushed a session's pending relation and function statistics only on
`pg_stat_force_next_flush()`. A backend that just disconnected left the shared store untouched, and
pg\_stat\_have\_stats stayed false on primaries too. `executor.FlushSessionStats`, called from the connection
teardown in `postmaster/server.go`, is pgstat\_shutdown\_hook's flush. The defer is registered so it runs after the
open\-transaction rollback has folded its staged counts. PG's rate\-limited idle flush \(pgstat\_report\_stat at
ReadyForQuery\) is not done yet. Effects: 030's creation checks pass on goopg standbys, regress stats 235 → 234, and
the isolation stats spec still passes.

## M0149\-0032b — dropped\-stats items in goopg commit and abort records \(landed\)

PG's `pgstat_drop_transactional` / `pgstat_create_transactional` \(pgstat\_xact.c\) queue an `xl_xact_stats_item`
\{kind, dboid, objid\_lo, objid\_hi\} per dropped or created object. `pgstat_get_transactional_drops` puts the dropped
ones into the commit record and the created ones into the abort record \(`XACT_XINFO_HAS_DROPPED_STATS`, after the
relfilelocators chunk and before the invals\). A standby replaying the record drops those stats entries. goopg's
records had no such chunk, so a PG 18.3 standby of a goopg primary kept the stats of dropped objects.

- `transam` keeps a per\-top\-XID pending list \(`AddPendingStatsDrop`, `PendingStatsDrops(top, commit)`,
  `pending_stats_drop.go`\), next to the pending relation deletes:
  - at commit it returns the dropped items, leaving out drops made inside a rolled\-back subtransaction;
  - at abort it returns the created items;
  - the list is forgotten when the transaction ends.
- `xlog.EncodeXactCommitPGWithStatsAt` / `EncodeXactAbortPGWithStatsAt` write the chunk, and `ParseXactRecord` now
  returns the items as `DroppedStats` \(for M0149\-0032c\). The commit hook in `initdb/open.go` passes them in.
- The executor's `noteStatsDrop` \(`pgstat_xact.go`\) records items at:
  - the table drop \(`dropTableByRefImmediate`: the table and its indexes\);
  - the function drop paths \(`walLogDropRoutine`, the deferred\-at\-commit drops, DROP\-then\-CREATE\);
  - DROP SCHEMA CASCADE \(its tables, indexes and functions; it now also drops their stats on the primary\);
  - DROP DATABASE \(the pg\_database row delete transaction, kind database\).
- Effect: 030's goopg\-primary → PG 18.3 standby pairing passes end to end. The goopg\-standby pairings still need
  M0149\-0032c.
- Not done yet \(deferral ledger\):
  - abort\-record items for created objects \(pgstat\_create\_relation / pgstat\_create\_function\);
  - items for views, materialized views, sequences, standalone DROP INDEX and TOAST relations;
  - 2PC prepare / COMMIT PREPARED records.

## M0149\-0032c — replayed stats drops and stats across a standby restart \(landed\)

Four gaps stood between the goopg standbys and 030:

- **Replayed drops.** `StreamReplayer.SetStatsDropReplayHook` passes each replayed commit / abort / COMMIT PREPARED /
  ABORT PREPARED record's `DroppedStats` to `executor.ExecuteTransactionalStatsDrops`
  \(pgstat\_execute\_transactional\_drops\).
  - goopg's relation and function stores key on the OID alone, so an item drops an entry only when that entry belongs
    to the item's database \(or has no database\).
  - A database item drops the database entry and every relation and function attributed to it
    \(pgstat\_drop\_database\_and\_contents\).
- **Restore after a clean standby stop.** Startup chose discard vs restore of `pg_stat/pgstat.stat` by
  `crashRecovery`, which also counts DB\_SHUTDOWNED\_IN\_RECOVERY \(a standby that stopped cleanly\) as a crash, because
  a standby always replays. The new `startupRecoveryDecision.didCrash` is StartupXLOG's own rule: the state is neither
  DB\_SHUTDOWNED nor DB\_SHUTDOWNED\_IN\_RECOVERY.
- **Default\-database attribution.** A connection to the default database carries database OID 0
  \(`catalog.NamespaceDBOid`'s convention\), and `dbStats.noteFlush` skipped 0. Those entries were written to the stats
  file under dboid 0, so after a restart `pg_stat_have_stats('relation', 5, …)` no longer matched them. noteFlush now
  maps 0 to the default database.
- **Clean standby shutdown.** On the PG\-primary pairing the standby's shutdown restartpoint failed: a hot\-standby read
  had dirtied a buffer \(hint bits\) of a relation whose replayed DROP had removed the file. pg\_control then never
  reached DB\_SHUTDOWNED\_IN\_RECOVERY, and the next start discarded the stats. PG's DropRelationFiles →
  smgrdounlinkall drops the relation's buffers before unlinking. `StreamReplayer.SetRelDropReplayHook` now does that:
  for each relation a replayed commit/abort lists, every fork's buffers are discarded through
  `Pool.InvalidateRelForTruncate` before `dropXactRelFiles` removes the files.
