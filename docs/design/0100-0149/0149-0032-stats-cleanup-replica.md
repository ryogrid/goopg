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
