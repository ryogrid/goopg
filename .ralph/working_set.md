(idle — nothing in flight)

## Loop summary (2026-07-12, loop #87)

**M0122-0003 — registered `pg_stat_subscription_stats`** (per-subscription
error/conflict-counter view, 12 cols). This is the LAST unregistered `pg_stat_*`
view — the family is now complete.

- Unlike the sibling `pg_stat_subscription` (one row per apply worker, backed by
  `*wal.Subscribers`), this view is driven by the *subscription catalog*: upstream
  `system_views.sql` joins `pg_subscription s` with
  `pg_stat_get_subscription_stats(s.oid)`, so one row PER SUBSCRIPTION (appears even
  with no live worker). Lives next to `registerStatSubscriptionView` in
  `internal/initdb/replication_views.go` (`registerStatSubscriptionStatsView`), backed
  by the same `*catalog.PubSub`, wired in `initdb.Open` after `registerSubscriptionViews`.
- goopg has no `PgStat_StatSubEntry` accumulator, so all 9 counters = faithful `0`
  and `stats_reset` NULL — byte-identical to a real PG 18.3 subscription that applied
  cleanly and never reset. Col types from `pg_stat_get_subscription_stats`'
  `proallargtypes` (subid oid, counters int8, stats_reset timestamptz; subname name).

Files: internal/initdb/replication_views.go (new registerStatSubscriptionStatsView),
internal/initdb/replication_views_test.go (new TestStatSubscriptionStatsRendersPerSubscription
+ fmt import), internal/initdb/open.go (wiring), design
0122-0003-pg-stat-user-tables.md (new section + Deferred) + README row + ledger + fix_plan.

Gates run: `go build ./...` clean; `go vet ./internal/initdb` clean; targeted
initdb tests (TestStatSubscription*/TestPgSubscription*/Nailed/Recovery) PASS (136s);
manual server e2e smoke PASS (goopg on :5540, view returned correct 12-col shape/order,
0 rows with no subscription); ralph-state-guard OK (auto-repaired); pgbench smoke via
pre-commit hook on commit.

M-NIGHTLY: clean — action-items run 20260712-020530 already triaged `[x]` at
fix_plan.md:64 (2h12m package-wide test-timeout cascade, not 39 real regressions);
no newer nightly present.

Next: pg_stat family is complete. Pick the next M0122-0003 sub-item or another
open milestone from fix_plan.md.
In-flight: none
