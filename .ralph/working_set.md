(idle — nothing in flight)

Last loop (#14): M0119-0004 sub-feature — NULLS NOT DISTINCT *enforcement* at
INSERT/UPDATE — LANDED + committed (design 0119-0004, agent-reviewed before impl).
`checkUniqueIndexes{ForInsert,ForUpdate}` (internal/executor/operators_storage.go)
now fall back to new `checkNullsNotDistinctViaHeapScan` + `rowHasNullKeyColumn` +
`nndDetail` when a key column is NULL on an `idx.NullsNotDistinct` index, raising
23505 for a duplicate NULL pattern. btree/scan-probe/codec untouched; gated
dead-code for every non-NND index. Tests: internal/executor/nulls_not_distinct_test.go
(4 tests PASS, incl. -race). Full executor suite PASS.

Remaining M0119 backlog (pick topmost actionable next loop):
- M0119-0002 (CLOG Part C / 0007 Part B / 0008 Part B) — Effort-L full-gate session
  (-race mvcc+wal, xlog_replay, PG-standby E2E, fresh TPC-H Q12/Q13). Highest blast.
- M0119-0004 STILL OPEN: pg_dump 002–010 catalog-view parity; the
  deferred-constraint-checking-at-COMMIT engine gap; and the NND follow-ups
  (ON CONFLICT/upsert arbiter — `encodeArbiterKey` ignores the flag → mis-inserts
  a duplicate; CREATE UNIQUE INDEX build over NULL-keyed data). All ledgered
  2026-06-29. The upsert follow-up is well-scoped: thread the conflicting
  ItemPointer (already returned by checkNullsNotDistinctViaHeapScan) into the
  upsert no-conflict branch (operators_upsert.go ~613).
- M0119-0005 (pg_waldump WD-002), M0119-0006 (amcheck server tier — needs index
  AMs), M0119-0007 (pg_basebackup recvlogical — blocked on logical decoding).
