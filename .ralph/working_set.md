(idle — nothing in flight)

Last loop (#16): M0119-0004 NND **CREATE [UNIQUE] INDEX build over NULL-keyed
data** follow-up — LANDED + committed (design 0119-0004 §9). Closes sub-feature
(b) from the loop #15 ledger row.
- `encodeCompositeBTreeKey` (operators_ddl.go) no longer raises 42804 on a NULL
  key column — returns a new `hasNullKey bool`. Both build paths
  (`collectBTreeEntries` + dead-but-compiled `backfillBTree`) SKIP NULL-keyed
  rows for default/non-unique indexes (mirrors runtime maintain — no null
  bitmap), and dedup null-bearing rows for `unique && nullsNotDistinct` via a
  build-local `seenNull`/`nndNullKeyDedupKey` (presence-byte 0x00/0x01 + col
  encoding) → 23505 on a duplicate NULL pattern.
- NND flag threaded as new `nullsNotDistinct` param through `createBTreeIndex`→
  `bulkBuildBTree[WithPredicate]`→`collectBTreeEntries`/`backfillBTree`; all 16
  `createBTreeIndex` sites updated (5 NND-capable forward the real flag, rest
  false), matview-rebuild `bulkBuildBTree` forwards `idx.NullsNotDistinct`.
- Tests: 4 new build-path tests in nulls_not_distinct_test.go. Full executor
  suite + -race (Unique/Index/NullsNotDistinct/Upsert/Conflict/DDL/Partition)
  PASS. Zero blast radius for default indexes (NULL row skipped not errored).

Remaining M0119 backlog (pick topmost actionable next loop):
- M0119-0002 (CLOG Part C / 0007 Part B / 0008 Part B) — Effort-L full-gate
  session (-race mvcc+wal, xlog_replay, PG-standby E2E, fresh TPC-H Q12/Q13).
- M0119-0004 STILL OPEN: pg_dump 002–010 catalog-view parity; deferred-
  constraint-checking-at-COMMIT engine gap; the LAST NND follow-up —
  (c) reordered partition-leaf NND arbiter (`partLeaf != nil`: remap `inserted`
  to leaf order then drop the `partLeaf == nil` guard in upsert Next +
  probeArbiterNND). Ledgered 2026-06-29.
- M0119-0005 (pg_waldump WD-002), M0119-0006 (amcheck server tier — needs index
  AMs), M0119-0007 (pg_basebackup recvlogical — blocked on logical decoding).
