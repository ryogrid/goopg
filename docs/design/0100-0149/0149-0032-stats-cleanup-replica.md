# M0149\-0032 — Port recovery/t/030\_stats\_cleanup\_replica.pl

Status: skipped as a detour after M0149\-0032a landed. TestPort\_Recovery030StatsCleanupReplica is written and gated
\(`GOOPG_TAP_030`\).

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
