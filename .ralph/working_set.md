Task: M0118-0003 multixact — LANDED the LOCKER HALF of update-chain lock
propagation (resume point #3, locker half). A SELECT FOR KEY SHARE/SHARE on a
version an in-flight no-key updater superseded now traverses t_ctid forward and
locks the successor version(s) too (heap_lock_updated_tuple analog).

DONE this loop (#52), committed:
- operators_lockrows.go: stampLockInner branch (a) `formed` block now captures
  the successor ptr + updater xid (from the ORIGINAL header), then after forming
  the multi on the seen version calls the new `propagateLockForward`.
- NEW methods (after stampMultiUpdaterLock): `updaterXID(hdr)` (single/multi
  update-xid resolver, HeapTupleHeaderGetUpdateXid analog); `propagateLockForward
  (rel, start, priorXmax)` (walks t_ctid, one page-lock per hop, continuity =
  successor.xmin==priorXmax, stops at aborted-xmin / xmax-invalid / only-locked /
  self-ctid / pruned / 32-hop cap); `lockSuccessorVersion(slot,rel,ptr,hdr)`
  (combine via stampMultiLock/stampMultiUpdaterLock, never clobber a real
  updater, else stamp our lock-only).
- Test: TestForKeySharePropagatesLockToUpdatedSuccessor (operators_lockrows_test
  .go) — sA in-flight no-key UPDATE supersedes id=1, sB FOR KEY SHARE → successor
  (xmin=sA) carries sB's FOR KEY SHARE lock-only xmax (lockOnlyMemberStatus).
- Design 0118-0002 "slice 5" section + resume-#3 status (locker ✅ / write-path
  ⛔) + README index status line + slice-5 summary. Ledger row appended.

GATES this loop (all PASS): go build ./...; go vet ./internal/executor; unit
tests executor+multixact+storage+planner; -race subset (executor row-lock tests
+ multixact); 6 dedicated row-lock isolation specs (lock-committed-update,
lock-committed-keyupdate, skip-locked, nowait, nowait-3, update-locked-tuple) —
NO regression. pgbench pre-commit smoke via hook on commit. tpch-spotcheck
INFRA-BLOCKED (SLRU backfill >60s). Stage ONLY code/doc/.ralph; do NOT add stray
`postgres`, weekly_loc.*, requirements.txt.

>>> KEY MEASUREMENT: ran lock-update-traversal AFTER this slice (still SKIPs as
    deferred). s2d2 key-UPDATE now completes WITHOUT `<waiting>` — i.e. the
    propagated lock now EXISTS on the successor (locker half done) but the plain
    UPDATE/DELETE write path does not HONOUR it across statements. The SOLE
    remaining blocker for lock-update-traversal is the **write-path lock-wait**.

>>> NEXT STEP: land the **write-path lock-wait half** (the other resume-#3 half).
    In the UPDATE/DELETE write paths (operators_storage.go: updateViaIndex's
    foreignLockOnly branch @~2299, the seqscan updateOp.Next, deleteOp.Next),
    when the live version carries a foreign lock-only xmax: resolve active
    holders via the multixact member store (activeLockHolders-style /
    multixactFirstActiveMember + IsXIDActive), and if the WRITE conflicts with a
    held strength (DELETE & key-UPDATE = StatusUpdate conflicts with ALL incl
    FOR KEY SHARE; no-key UPDATE = StatusNoKeyUpdate does NOT conflict with FOR
    KEY SHARE) → WaitForXID then re-evaluate (EPQ). Currently the path only takes
    a STATEMENT-SCOPED lockmgr ExclusiveLock (acquireTupleLock) which a
    cross-statement holder has already released — so it never waits. Use
    TestPort_IsolationLockUpdateTraversal (3 perms, no savepoint) as proof; it
    promotes to pass when this lands (then also lock-update-delete /
    propagate-lock-delete, which add advisory locks / FK-INSERT propagation).

GOTCHAS: MultiXactId & TransactionID share uint32; HEAP_XMAX_IS_MULTI is the ONLY
disambiguator — never pass a raw multi xmax to IsXIDActive/WaitForXID. goopg
fresh-tuple CTID = {InvalidBlockNumber,0} (NOT self) — so "is this the latest
version" must gate on xmax-valid, not just ctid!=self (propagateLockForward's
`wasUpdated` already does: xmax-valid && !lock-only && forwardPtr). Sibling-path
rule: lockMemberStatus (encode) ↔ lockOnlyMemberStatus (decode), both four-way.
The propagated lock-only stamp is NOT WAL-persisted (same deferral as the multi
producer; transient lock-only state is correct to lose on crash).
