(idle — nothing in flight)

Loop #33 COMPLETE + ready to commit: M0118-0009 enabler — `pg_database.datfrozenxid`
+ `datminmxid` catalog-parity columns (design 0118-0096).

What landed:
- `internal/catalog/catalog.go` pgDatabase: added columns `datfrozenxid` (xid,
  ordinal 7) + `datminmxid` (xid, ordinal 8). VirtualRows computes datfrozenxid
  once via `c.DatFrozenXID()` (cluster-wide min relfrozenxid; bootstrap floor
  `storage.FrozenTransactionID`=2 when 0), datminmxid="1" (FirstMultiXactId).
  Closure does NOT hold c.mu (calls ListDatabases the same way) → DatFrozenXID's
  RLock is safe, not nested.
- `internal/catalog/database_test.go`: +TestPgDatabaseExposesFrozenXidColumns.
- Reconciled stale inventory: fk-deadlock.spec (promoted c59eb91d / 0118-0094)
  flipped failed→pass in target-inventory.csv; regen coverage+inventory md
  (isolation tally 105→106 pass / 16→15 failed).
- docs/design/0118-0096 + README index; fix_plan + deferral_ledger updated.

Gates run: TestPgDatabaseExposesFrozenXidColumns PASS; full internal/catalog PASS;
live SELECT datname,datfrozenxid,datminmxid FROM pg_database → "postgres 2 1" (via
throwaway cluster test, since removed); go build ./... clean; go vet + gofmt -l
clean; ralph-state-guard OK. pgbench smoke = pre-commit hook (pending commit).

intra-grant-inplace-db STILL deferred: hard blocker = runtime shared-catalog
MVCC-tuple lock (VACUUM FREEZE must `<waiting ...>` behind uncommitted GRANT … ON
DATABASE row update on global/1262); same capability gates intra-grant-inplace on
pg_class. See ledger 2026-06-25.

Remaining M0118 failed specs (15, all distinct large subsystems — probed this
loop, none a cheap front-end win):
- deadlock-parallel (lock groups), index-only-bitmapscan (bitmap + cursor/EXPLAIN)
- eval-plan-qual (EPQ-over-join re-projection), ri-trigger (user RI triggers)
- fk-partitioned-1/2 (ATTACH PARTITION + partitioned FK)
- horizons (jsonb + IOS prune horizon), stats (pg_stat_* infra)
- intra-grant-inplace{,-db} (shared-catalog inplace MVCC tuple+lock)
- predicate-gin/gist/hash (index AMs + finer predicate locking)
- prepared-transactions{,-cic} (2PC PREPARE TRANSACTION / COMMIT PREPARED)
