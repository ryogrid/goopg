Task: M0118-0004 (deadlock detection) — this loop promoted `multixact-no-deadlock`
(re-acquiring a self-held row lock). M0118-0004 continues.

DONE this loop (committed):
- PURE PROMOTION — NO new engine code. multixact-no-deadlock: s1 FOR SHARE; s2
  FOR SHARE (row xmax → SHARE multixact {s1,s2}); s3 FOR UPDATE WAITS; then s1
  re-FOR SHARE (s1lock2) must NOT queue behind s3 (would deadlock) — returns
  immediately; after s2+s1 commit s3 completes. goopg already correct because:
  stampLockInner gates the wait branch on tupleLockConflicts FIRST (re-SHARE vs
  SHARE multixact = no conflict → branch skipped); activeLockHolders filters self
  (m.Xid != Tx.XID); stampMultiLock drops+re-appends self; multixact.MembersConflict
  skips self (port of DoesMultiXactIdConflict). s3 is parked OUTSIDE the tuple xmax
  (waiter, not stamped) so s1 sees only compatible {s1,s2} — detector never consulted.
- Files: internal/testport/isolation_port_test.go (+TestPort_IsolationMultixactNoDeadlock).
  CSV failed→pass (rationale Go func COMMA-FREE); coverage+inventory regen (isolation
  pass 56→57). Design NEW 0118-0007 + README index row. fix_plan M0118-0004 annotated;
  deferral ledger line appended.

GATES (all PASS): go build ./...; gofmt+vet clean; -race ./internal/lockmgr/... PASS;
TestPort_IsolationMultixactNoDeadlock PASS byte-for-byte; regression batch DeadlockHard/
Simple/Soft/Soft2/LockNowait/TuplelockUpdate PASS; ralph-state-guard OK (auto-repaired
progress.json); pgbench smoke via pre-commit hook. DO NOT stage: postgres, weekly_loc.*,
requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0004 deadlock group, one spec per loop):
    RESUME at `tuplelock-upgrade-no-deadlock` — row-lock UPGRADE retry algorithm across
    MANY permutations (7 perms; s0/s1/s2/s3 take KEY SHARE / SHARE / FOR UPDATE / UPDATE /
    DELETE on one row with savepoints). Tests that lock UPGRADES + acquire-in-order do
    NOT deadlock. Like multixact-no-deadlock this rides the row-lock xmax/WaitForXID path,
    NOT lockmgr — write TestPort_IsolationTuplelockUpgradeNoDeadlock FIRST and capture the
    live diff before assuming engine work. May already pass (no-deadlock invariant) or need
    the row-lock retry-after-avoiding-deadlock algorithm (heap_lock_tuple HeapTupleUpdated
    retry). THEN `deadlock-parallel` — DEFER: needs a parallel-query lock-group abstraction
    goopg lacks (soft/hard wait-for graph treats each backend as its own leader). Per-spec:
    write TestPort_Isolation<Name> FIRST, capture live diff BEFORE engine work; fix → green
    → CSV failed→pass (rationale COMMA-FREE) → regen gen-oracle-inventory +
    gen-isolation-coverage → design slice + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free — [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s); the
row-lock path never fires in pgbench TPC-B/TPC-H so TPS blast radius nil. Pre-existing
forvar lint nit at isolation_port_test.go:50 is UNRELATED (do not refactor existing tests).
