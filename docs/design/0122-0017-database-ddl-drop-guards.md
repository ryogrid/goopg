# DROP DATABASE guard checks (M0122-0007)

Status: accepted (partial)
Date: 2026-07-09
Supersedes: none

## Problem

`tryHandleDatabaseDDL`'s `databaseDDLDrop` branch (`internal/server/database_ddl.go`,
M0054-0001) was a bare `cat.DropDatabase(name)` map-delete with no guard checks at
all — real PG's `dropdb()` (`postgres/src/backend/commands/dbcommands.c` ~line 1690)
runs several rejection checks before ever touching `pg_database`:

1. `db_istemplate` → `cannot drop a template database` (42809, `ERRCODE_WRONG_OBJECT_TYPE`).
2. `db_id == MyDatabaseId` → `cannot drop the currently open database` (55006,
   `ERRCODE_OBJECT_IN_USE`).
3. `CountOtherDBBackends()` → `database "%s" is being accessed by other users`
   (55006), optionally preceded by `TerminateOtherDBBackends()` under `WITH (FORCE)`.

goopg had none of these — `DROP DATABASE postgres` while connected *to* `postgres`,
or while another session was actively using the target database, silently
succeeded. This is one of the long-repeated "CREATE/DROP DATABASE full DDL" items
under the M0122-0007 fix_plan bucket; full per-database physical storage
isolation (goopg still routes every relation through a single
`catalog.DefaultDBOid`, see the package doc comment in `database_ddl.go`) remains
a separate, much larger architectural item — this slice only adds the three
guard checks above, which need no physical-storage work at all.

## Fix

`databaseDDLDrop` now runs, in PG's own order:

1. `cat.HasDatabase(name)` existence pre-check (moved ahead of the mutating call
   so the guards below can run before anything changes) — preserves the existing
   `IF EXISTS`/`UndefinedDatabase` behavior unchanged.
2. Template rejection: `name == "template0" || name == "template1"`. goopg has no
   `ALTER DATABASE ... IS_TEMPLATE`, so these two names are permanently templates
   — the same hardcoded rule `pg_database`'s own `datistemplate` rendering already
   uses (`catalog.go`'s `pgDatabase.VirtualRows`).
3. Self-drop rejection: `name == liveDBName` (the calling connection's own
   database, already threaded into `tryHandleDatabaseDDL` for the ALTER DATABASE
   SET branch). Checked *before* the busy check below — once it passes,
   `name != liveDBName` is guaranteed, so any backend `CountByDatName(name)` finds
   next is necessarily a connection other than this one. No separate
   self-exclusion bookkeeping was needed (unlike `postinit.c`'s
   `CountOtherDBBackends`, which explicitly skips `MyProcNumber`).
4. Busy check: `s.cfg.Activity.CountByDatName(name) > 0` (nil-safe — some
   embedded/test paths run with no activity registry plumbed, matching the
   pre-existing nil-check pattern for `s.cfg.WAL`). `CountByDatName` already
   existed (`internal/activity/registry.go`, built for M0119-0006's
   `datconnlimit` connect-time check) and is self-inclusive by design — safe here
   precisely because of point 3 above.

`WITH (FORCE)` (`TerminateOtherDBBackends`) is not implemented — goopg has no
signal-driven connection-termination mechanism yet — so `DROP DATABASE ... WITH
(FORCE)` on a busy database still errors the same as the plain form (no
`FORCE`-clause parsing exists either; the clause would currently fall through
`classifyDatabaseDDL`'s prefix match unchanged since it comes after the name).

## Tests

`TestTryHandleDatabaseDDLDropGuards` (`internal/server/database_ddl_test.go`):
template rejection for both `template0`/`template1`, self-drop rejection, busy
rejection via a fake `activity.Backend` registered on the target database name,
and confirmation the drop succeeds once that backend unregisters. Confirmed
non-vacuous via `git stash` on `database_ddl.go` alone (fails: `template0`
drops with no error pre-fix).

## Gates

- `go build ./...` / `go vet ./internal/server/... ./internal/activity/...` clean.
- `go test ./internal/server/... ./internal/activity/...` PASS (full packages,
  no regression).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
  transactions, all 3 workloads).

## Still open (M0122-0007 bucket)

- Full per-database physical storage isolation (template copy on CREATE, real
  directory removal on DROP) — the architectural item every prior loop in this
  bucket has correctly deferred whole; see the deferral ledger row appended
  alongside this doc.
- `WITH (FORCE)` connection termination (needs a signal/cancel-backend
  mechanism goopg doesn't have yet).
- Ownership/permission check before DROP (`object_ownercheck` — goopg's
  database registry tracks no owner today).
- `REINDEX ... CONCURRENTLY` physical rebuild (separate, pre-existing item,
  unaffected by this change).
