(idle — nothing in flight)

Last loop (#56) COMPLETE: committed loop #55's verified-but-uncommitted WIP for
M0118-0009 — intra-grant-inplace ENABLER (design 0118-0115, NOT a promotion). Loop
#55 hit a usage limit mid-task (progress.json showed api_limit); the code + design +
test were complete and correct but never committed. This loop verified and landed it.

What landed (design 0118-0115, intra-grant-inplace perm-8 ordering):
- mvcc.Manager.ReleaseXIDWaiters(xid): records xid in releasedWaiterXIDs set + broadcasts
  commitCond; xidActiveWithSubxact returns false for a released XID (and its top parent)
  WITHOUT clearing the proc-array slot. finish() clears the marker. Snapshot visibility
  NOT consulted against the set (victim is write-less).
- Context.DeadlockVictim: set only in waitPgClassInplaceXID on cycle detection; reset in
  wire dispatch per-statement block.
- connTxState.AbortInPlaceOnFail(mgr): calls ReleaseXIDWaiters(currentTx.XID), gated on
  SavepointDepth()==0. Called from dispatch.go failure path ONLY when DeadlockVictim set.
- Result: perms 1-8 byte-identical; first divergence advanced L184 → L206 (verified via
  throwaway probe). Spec stays `defer`.

Remaining intra-grant-inplace gaps (perms 9-10, distinct unbuilt subsystems):
- perm9: revoke4 is `DO $$ BEGIN REVOKE … END $$` — plpgsql parser rejects bare REVOKE in
  a DO body ("invalid PL/pgSQL DO body: ... expected ':=' or '=' after revoke"); also needs
  the REVOKE to take a pg_class rowmark + await ACL/rowmark xmax (sfu3-after-grant1).
- perm10: drop1 = `DELETE FROM pg_class WHERE relname=…` — virtual-pg_class tuple delete
  (deferred drop at commit) + SearchSysCacheLocked1 find-then-none "cache lookup failed".

Other failing M0118 specs (distinct unbuilt subsystems): index-only-bitmapscan
(EXPLAIN DECLARE CURSOR + BitmapOr plan), predicate-gin/gist (GIN/GiST AMs),
deadlock-parallel (lock-group abstraction), fk-partitioned-1/2 (ATTACH PARTITION +
partitioned-FK enforcement), stats (pg_stat_* cumulative subsystem).
