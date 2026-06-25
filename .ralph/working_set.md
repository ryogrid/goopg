(idle — nothing in flight)

Last loop (#54) COMPLETE: M0118-0009 — intra-grant-inplace ENABLER (design 0118-0114,
NOT a promotion). Added the reverse-direction wait + deadlock detection:
- GRANT/REVOKE on a table now AWAITS a conflicting concurrent pg_class rowmark
  (execCompatNoop TableACL: record ACL xmax FIRST, then waitForPgClassRowMarks).
- New shared helper executor.waitPgClassInplaceXID registers an edge in the EXISTING
  process-global EPQ wait-for graph (registerWFGAndCheckCycle) → perm8 deadlock detected
  synchronously, addk2 raises 40P01. All 3 pg_class-tuple waits route through it.
Result: perms 1-7 byte-exact + perm 8 deadlock line exact; first divergence L141→L184.
Committed + pushed.

Remaining intra-grant-inplace gaps (perms 8-10, all distinct, deferred — see ledger):
- perm8 (ordering): ONLY residual is grant1/c2 completion-order swap. goopg keeps the
  deadlock victim's XID ACTIVE until the explicit COMMIT, so grant1's WaitForXID(s2)
  unblocks at c2 not at the abort. Fix = deactivate victim XID at deadlock-abort time
  while keeping the txn block open (AbortTransaction-releases-XID-but-block-open). The
  cheapest next intra-grant-inplace step IF it generalises.
- perm9: revoke4 is `DO $$ … REVOKE … $$` — plpgsql parser rejects bare REVOKE in a DO
  body; also needs lockRowsOp on pg_class to await ACL xmax (sfu3-after-grant1).
- perm10: drop1 = `DELETE FROM pg_class` — virtual-pg_class DELETE (deferred drop at
  commit) + SearchSysCacheLocked1 find-then-none → "WARNING: cache lookup failed".

Other failing M0118 specs (distinct unbuilt subsystems): index-only-bitmapscan
(EXPLAIN DECLARE CURSOR + BitmapOr plan), predicate-gin/gist (GIN/GiST AMs),
deadlock-parallel (lock-group abstraction), fk-partitioned-1/2 (ATTACH PARTITION +
partitioned-FK enforcement), stats (pg_stat_* cumulative subsystem).
