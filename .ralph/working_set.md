(idle — nothing in flight)

Last loop (#15): M0119-0004 NND **ON CONFLICT / upsert arbiter** follow-up —
LANDED + committed (design 0119-0004 §8). Closes sub-feature (a) from loop #14's
ledger row: a NULL-keyed `INSERT … ON CONFLICT (nndcol) DO UPDATE/NOTHING`
against a `NullsNotDistinct` arbiter index now routes to the conflict action
instead of inserting a duplicate.
- New `probeArbiterNND` (internal/executor/operators_upsert.go): gated on
  `idx.NullsNotDistinct && rowHasNullKeyColumn`, reuses
  `checkNullsNotDistinctViaHeapScan`, decodes the conflicting tuple. Wired in
  upsert `Next` right after `probeArbiterWaiting`, only on the non-reordered
  path (`partLeaf == nil`).
- `indexKeyColumnsChanged` made NND-aware (new `nndKeyColumnsEqual`,
  operators_storage.go): a NULL→NULL no-key-change DO UPDATE now skips the probe
  so it doesn't self-conflict with the not-yet-stamped old tuple (upsert checks
  before applyUpdate).
- Tests: 3 new in nulls_not_distinct_test.go (DO NOTHING / DO UPDATE / NULLS
  DISTINCT control). Full executor suite + -race PASS; TestPort_Isolation
  InsertConflict*/Merge* PASS. Zero blast radius outside NND.

Remaining M0119 backlog (pick topmost actionable next loop):
- M0119-0002 (CLOG Part C / 0007 Part B / 0008 Part B) — Effort-L full-gate
  session (-race mvcc+wal, xlog_replay, PG-standby E2E, fresh TPC-H Q12/Q13).
- M0119-0004 STILL OPEN: pg_dump 002–010 catalog-view parity; deferred-
  constraint-checking-at-COMMIT engine gap; remaining NND follow-ups —
  (b) CREATE UNIQUE INDEX build over NULL-keyed data (42804, in
  collectBTreeEntries/backfillBTree via encodeCompositeBTreeKey, operators_ddl.go);
  (c) reordered partition-leaf NND arbiter (remap `inserted` to leaf order then
  drop the `partLeaf == nil` guard in upsert Next). All ledgered 2026-06-29.
- M0119-0005 (pg_waldump WD-002), M0119-0006 (amcheck server tier — needs index
  AMs), M0119-0007 (pg_basebackup recvlogical — blocked on logical decoding).
