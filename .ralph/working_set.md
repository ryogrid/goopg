(idle — nothing in flight)

Last loop (#19): M0119-0004 **deferred-RI fresh-snapshot** — LANDED, the
`ConstraintsOverrideActive` gate is DROPPED. Plain `DEFERRABLE INITIALLY
DEFERRED` FKs now enforce at COMMIT on the simple-query path (psql/lib/pq/
isolation runner, which bypasses `execCommit`), matching PG's deferred-RI
`GetLatestSnapshot()` semantics. Design `0119-0004-deferred-ri-fresh-snapshot`.
- `mvcc.Manager.FreshSnapshot()` (manager.go) = exported wrap of
  `captureSnapshot()` (latest committed; CLOG + partition-detach epoch attached).
- `runAllDeferredFKChecks` (operators_fk.go) saves `ctx.Snap`, installs
  `FreshSnapshot()`, restores via `defer` — ONE chokepoint for BOTH execCommit
  and dispatch paths. `fullTableFKCheck` child scan + `assertParentExists`→
  `scanTableForMatchFKWait` parent probe both see post-snapshot commits.
- dispatch.go TxCommit: removed `&& sess.ConstraintsOverrideActive()`.
- Own uncommitted child rows still visible (TupleVisibleSubxact self-check on
  ctx.Tx.XID); empty deferred queue → early return, no snapshot taken (zero
  blast radius for TPC-H/pgbench/IMMEDIATE).
- Tests: `TestPort_IsolationFkSnapshot` (7 perms) + full FK iso group PASS; new
  `TestPort_InitiallyDeferredFKCommit` (ordered commit + raise-at-COMMIT + orphan
  rollback); `TestPort_SetConstraintsDeferral` PASS; -race mvcc+executor PASS.

NEXT loop — pick topmost actionable M0119:
- M0119-0004 still open: pg_dump 002–010 catalog-view parity battery
  (self-promoting `TestPort_PgDumpConnectionSetup` guard, currently GREEN — add a
  fixture to surface the next gap); deferred UNIQUE/EXCLUDE (needs a
  deferred-uniqueness queue parallel to the FK queue); extended-protocol
  commit-time deferral (thread executor session into the extended utility fast
  path so deferral works off the simple protocol).
- Or M0119-0005 (pg_waldump server tier) / M0119-0006 (pg_amcheck server tier).
