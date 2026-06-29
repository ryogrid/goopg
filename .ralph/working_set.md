(idle — nothing in flight)

Last loop (#17): M0119-0004 NND **reordered partition-leaf arbiter** — LANDED +
committed (design 0119-0004 §10). Closes sub-feature (c); **all NND enforcement
sub-features (a)–(c) are now complete.**
- `operators_upsert.go` upsert `Next`: the NND heap-scan fallback was guarded out
  on reordered partition leaves (`if !conflicted && partLeaf == nil`), so a
  duplicate NULL row was wrongly INSERTED where PG routes to the conflict action.
- Root cause = row/column-order mismatch: `probeArbiterNND` /
  `checkNullsNotDistinctViaHeapScan` / `rowHasNullKeyColumn` resolve key columns
  by NAME against `cols` and read the candidate at the matching ordinal, so the
  passed row must share `cols`' order. Fix = pass `insertedForLeaf` (already
  leaf-ordered on the reordered path; == `inserted` on every non-reordered path)
  and drop the guard. conflictRow stays leaf-decoded → downstream leaf→parent
  remap unchanged. Zero blast radius outside NND.
- Test `TestNullsNotDistinctOnConflictReorderedPartitionLeaf` (parent `(a,b,c)` /
  leaf `(c,b,a)` / NND `(a,b)`; DO NOTHING skip + DO UPDATE target). Confirmed
  RED→GREEN (2 rows → 1). `-race` Upsert/Conflict/Partition +
  `TestPort_IsolationInsertConflict*`/`Merge*` PASS; `go build ./...` clean.

Remaining M0119 backlog (pick topmost actionable next loop):
- M0119-0002 (CLOG Part C / 0007 Part B / 0008 Part B) — Effort-L full-gate
  session (-race mvcc+wal, xlog_replay, PG-standby E2E, fresh TPC-H Q12/Q13).
- M0119-0004 STILL OPEN (NND now fully closed): pg_dump 002–010 catalog-view
  parity battery; deferred-constraint-checking-at-COMMIT engine gap.
- M0119-0005 (pg_waldump WD-002), M0119-0006 (amcheck server tier — needs index
  AMs), M0119-0007 (pg_basebackup recvlogical — blocked on logical decoding).
