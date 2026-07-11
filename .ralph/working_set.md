(idle — nothing in flight)

## Loop summary (2026-07-12, loop #86)

**M0122-0003 — registered `pg_stat_ssl` + `pg_stat_gssapi`** (per-client-backend
auth-transport views). Unlike every other pg_stat view in this milestone (which
live in base `catalog.go`), these are *per-backend*, so they live in
`internal/initdb/` alongside `pg_stat_activity` and are backed by the SAME
`activity.Registry`.

- New file `internal/initdb/pg_stat_ssl_gssapi_view.go`:
  `registerPgStatSslView` (8 cols) + `registerPgStatGssapiView` (5 cols). Both
  walk `reg.Snapshot()`, skip backends with `ClientPort==""` (upstream's
  `WHERE client_port IS NOT NULL` filter → drops bg workers), emit one row/client
  backend. goopg has no TLS/GSSAPI so `ssl`/gss flags = faithful `f`, detail cols
  NULL — byte-identical to real PG 18.3 `ssl=off` no-GSSAPI.
- Wired in `internal/initdb/open.go` right after `registerPgStatActivityView`
  (both take `act *activity.Registry`). NO per-connection twin (snapshot global).
- Column types from `pg_stat_get_activity` `proallargtypes` (`bits` int4,
  `client_serial` numeric, flags bool, rest text).

Files: internal/initdb/pg_stat_ssl_gssapi_view.go (new),
internal/initdb/pg_stat_ssl_gssapi_view_test.go (new,
TestPgStatSslView/TestPgStatGssapiView), internal/initdb/open.go (wiring),
design 0122-0003 + README + ledger + fix_plan.

Gates run: `internal/initdb` full package PASS (273s); go build ./... clean; go
vet initdb clean; manual server e2e smoke PASS (started goopg on :5539, both
views returned one row for the psql backend, ssl='f', detail cols NULL, stopped);
ralph-state-guard OK (auto-repaired); pgbench smoke via pre-commit hook on commit.

M-NIGHTLY: clean this loop — action-items run 20260712-020530 already triaged
loops #82/#84 (acdcfa22) as a stale build break; no newer nightly present.

Next still-unregistered pg_stat view: `pg_stat_subscription_stats` (per-subscription
apply/error counters — belongs in `internal/initdb/replication_views.go`, needs a
`PgStat_StatSubEntry` accumulator analog). That completes the pg_stat family.
In-flight: none
