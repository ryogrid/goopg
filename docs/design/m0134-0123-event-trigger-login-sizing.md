# M0134-0123: `event_trigger_login.sql` sizing — PARKED

**Status:** PARKED (regress-sql `not-tried` → CSV `failed`, `pass_required=no`).
**Oracle:** `postgres/src/test/regress/sql/event_trigger_login.sql` /
`postgres/src/test/regress/expected/event_trigger_login.out` (PG 18.3).
**Live sizing:** `scripts/pg-regress-runner.sh --verbose event_trigger_login`
— 0% parity, diff 37 lines pre-fix / 27 lines post-fix.

## Why this is parked

Same dominant blocker as [M0134-0122](m0134-0122-event-trigger-sizing.md):
goopg has no DDL-event-trigger firing engine anywhere. This case is a
variant that fires exclusively at *connection-establishment* time:

```sql
create event trigger on_login_trigger on login execute procedure on_login_proc();
alter event trigger on_login_trigger enable always;
\c
NOTICE:  You are welcome!
select count(*) from user_logins;
 count
-------
     1
```

Nothing in goopg's connection/session-startup path ever looks up registered
`login` event triggers or invokes them — the same "`evtfoid` is written and
never read back" gap M0134-0122 documented, here scoped to PG's
`EventTriggerOnLogin` hook (`postgres/src/backend/commands/event_trigger.c:891-980`,
called from `InitPostgres`/`PerformAuthentication`) instead of a DDL `exec*`
hook. This is not a new blocker; it's the same missing infrastructure viewed
from a different call site.

## What landed instead: `pg_database.dathasloginevt`

Sizing surfaced one independent, firing-engine-free bug: the file's catalog
sanity check

```sql
select dathasloginevt from pg_database where datname = :'DBNAME';
```

errored `column "dathasloginevt" does not exist` on goopg. The column is a
real, contained gap unrelated to the firing engine: PostgreSQL's
`SetDatabaseHasLoginEventTriggers` (`event_trigger.c:390-414`, called from
`CreateEventTrigger` and `ALTER EVENT TRIGGER ... ENABLE`) sets this bit
purely from *registration* state — it does not require the trigger to ever
have fired.

goopg already had two independent `pg_database` column-schema definitions
that had drifted apart — a sibling-catalog-schema-drift bug per
`pattern_sibling_paths_must_agree`:

- `catalog.PgDatabaseColumnsPG18()` (`internal/catalog/pg_database_schema.go`)
  — the physical-heap-backed schema shared by initdb bootstrap and the
  `datfrozenxid` runtime-persistence path (M0117-0008 Part B). This one
  already listed `dathasloginevt` at ordinal 7.
- The SQL-visible **virtual** `pg_database` table registered in
  `catalog.go`'s `registerSystemTables` — the table goopg's own query engine
  actually resolves `SELECT ... FROM pg_database` against. This one never
  had the column at all.

### Fix

Added `dathasloginevt` to the virtual table's `Columns` (appended at
`Ordinal: 17` rather than reordered to match PG's physical position, since
the virtual table's column order already diverges from `pg_database.h` for
unrelated historical reasons — no query in the suite relies on `SELECT *`
positional ordering here). The `VirtualRows` closure computes the value live
on every query:

```go
dathasloginevt := "false"
if n == "postgres" {
    for _, et := range c.ListEventTriggers() {
        if et.Event == "login" && et.Enabled != "D" {
            dathasloginevt = "true"
            break
        }
    }
}
```

This mirrors `SetDatabaseHasLoginEventTriggers`'s "true iff at least one
non-disabled login trigger exists" rule, scoped to the same `n == "postgres"`
sentinel the adjacent `datacl` column already uses (goopg's event-trigger
registry, like its ACL store, is not partitioned per-database — there is
exactly one live scope). Unlike PG, goopg computes this live instead of
storing a toggled bit: PG's flag can go stale (it needs an explicit
opportunistic reset when a login fires and finds no triggers,
`event_trigger.c:934-975`) precisely because it's cached across
connections; goopg has no firing engine to ever create that staleness, so a
live scan is both simpler and always correct.

**Known caveat (not exercised by this case):** the physical-heap
`pg_database` row (`internal/executor/sys_pg_database.go`'s
`SyncPgDatabaseCatalogRow`, used only for real-PG-standby-on-goopg-catalog
fidelity) still always writes `dathasloginevt = false` via
`PgDatabaseColumnsPG18`'s bootstrap default. It is never updated to reflect
the live registry the way the virtual-table fix above does. No test
currently reads this column through that path; see the ledger resume point
if one ever does.

### Verification

- `go build ./...` clean.
- `go test ./internal/executor/... ./internal/catalog/...` — new
  `TestCreateEventTriggerLoginSetsDatHasLoginEvt`
  (`internal/executor/operators_ddl_event_trigger_test.go`) exercises the
  full false → true → false cycle (before `CREATE`, after `CREATE ... ON
  login`, after `ALTER ... DISABLE`).
- `scripts/pg-regress-runner.sh --verbose event_trigger_login`: diff shrank
  37 → 27 lines; the `dathasloginevt` divergence is fully gone (both sides
  print `t`). Remaining 27 lines are entirely the login-firing-engine gap
  (no `NOTICE`, `user_logins` stays empty across both `\c` reconnects).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` passes.

## Resume point (deferred)

Firing engine: reuse M0134-0122's resume point almost verbatim, but invoked
from goopg's connection-startup path (wherever authentication/session setup
completes in `internal/postmaster/`) rather than a DDL `exec*` site — filter
`c.ListEventTriggers()` to `Event == "login"` + non-disabled +
`session_replication_role`; no tag matching needed since PG forbids tag
filters on login triggers (`execCreateEventTrigger` already enforces this).

Full detail in `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0123.

## See also

- [M0134-0122](m0134-0122-event-trigger-sizing.md) — the DDL-event-trigger
  sibling case; same firing-engine blocker, same PARK pattern.
