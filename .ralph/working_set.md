Task: M0118-0004 `tuplelock-upgrade-no-deadlock` perm 9 (savepoint tuple-lock retry).
Design 0118-0012. Spec at 8/9; perm 9 divergence moved expected L216 → L238 this loop.

DONE this loop (committed): three gated engine pieces of the savepoint-scoped
row-lock subsystem perm 9 needs:
- (A) `mvcc.Manager.xidActiveWithSubxact` (manager.go) — IsXIDActive/WaitForXID now
  subxact-aware: subxid active iff top-level parent in progress AND subxid not
  individually aborted. Top-level fast path first → ordinary xids unchanged.
  (subxids deliberately NOT in proc-array per AllocateSubXid.)
- (B) `lockRowsOp.conflictingLockHolders` (operators_lockrows.go) — lock-only conflict
  branch waits ONLY on members whose mode conflicts (multixact.StatusesConflict) with
  the request (MultiXactIdWait semantics), not every active member.
- (C) `MarkSubxactAborted` (subxact_visibility.go) — commitCond.Broadcast() under
  waitMu after the abort state is set (disjoint from subxactMu → no lock-order hazard)
  so a blocked row-lock waiter wakes on ROLLBACK TO SAVEPOINT.
Files: manager.go, subxact_visibility.go, operators_lockrows.go, design 0118-0012 (NEW)
+ README index, fix_plan, deferral_ledger.

GATES (all PASS): build/vet clean; -race internal/mvcc + internal/multixact + row-lock
executor units; row-lock isolation batch (Tuplelock*/LockUpdate*/UpdateLockedTuple/
PropagateLockDelete/LockCommitted*/MultixactNoDeadlock/SkipLocked{,2,3,4}/Nowait{,2,3,4,5}/
LockNowait) PASS; deadlock+merge batch (DeadlockHard/Simple/Soft{,2}/Merge*/EvalPlanQual*)
PASS; full executor+multixact units PASS; pgbench smoke via pre-commit hook. DO NOT stage:
postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

VERIFY: `go test -v -run TestPort_IsolationTuplelockUpgradeNoDeadlock ./internal/testport/`
now diverges at expected L238 (was L216): s2_fornokeyupd completes at the full s1_rollback
instead of at s1_rollback_e.

>>> NEXT STEP (continue perm 9 — GAP D, the root blocker):
    goopg's ENTIRE heap write path stamps the TOP-LEVEL xid: ectx.Tx = session.tx via
    connTx.Tx()→CurrentTransaction() (server/dispatch.go:240,187; session.go:116-120),
    and lockRowsOp calls MaterializeWriterXID which short-circuits on the already-set
    top xid. INSERT too (operators_storage.go:2175/2177 stamp ctx.Tx.XID). So s1's
    FOR SHARE / FOR NO KEY UPDATE upgrades inside savepoints e/f are recorded under
    s1's top-level xid (stampMultiLock re-adds the member at the upgraded strength
    under the SAME xid) → ROLLBACK TO SAVEPOINT can't revert them → Gaps A/B/C have no
    subxid member to act on. FIX: (1) make the lock path (lockRowsOp) stamp under
    EffectiveWriterXID() (the current savepoint subxid) instead of ctx.Tx.XID — the
    statement's writer xid must reflect currentSubXid; (2) make EVERY row-lock
    self-identity check top-level-aware: activeLockHolders/conflictingLockHolders/
    stampMultiLock `m.Xid == o.ctx.Tx.XID` + the `tup.Header.Xmax != o.ctx.Tx.XID`
    conflict gates (operators_lockrows.go ~L722,955,1306,1349) → self = same top-level
    ancestor (mvcc.IsSelfXID / TopLevelXid), so a subtxn never blocks on / clobbers its
    own parent/sibling member. HIGHER BLAST RADIUS (changes what xid a lock is stamped
    under; contradicts the uniform top-level-stamping convention) → its own loop with
    FULL gates (race + ALL isolation specs + pgbench + recovery smoke). After perm 9:
    deadlock-parallel (DEFER: needs lock-group abstraction goopg lacks).
    Promotion workflow when perm 9 lands: green full spec → CSV failed→pass (rationale
    COMMA-FREE) → regen gen-oracle-inventory + gen-isolation-coverage → design + ledger.

GOTCHAS: CSV rationale MUST be comma-free [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go; never gofmt -w [[goopg_gofmt_version_mismatch_no_w]].
mvcc.IsSelfXID(xid, ctx.Tx.XID, ctx.TxnMgr) already exists (top-level-aware self check
used by scan paths) — reuse for the row-lock self checks. tpch-spotcheck INFRA-BLOCKED
(SLRU backfill >60s); savepoint row-lock path never fires in pgbench TPC-B/TPC-H → TPS
blast radius nil. Pre-existing repo lints (unusedfunc/QF1003/etc.) are NOT mine.
