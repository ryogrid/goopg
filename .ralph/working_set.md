Task: M0118-0004 (deadlock detection) — this loop promoted `deadlock-soft` AND
`deadlock-soft-2` (SOFT-deadlock wait-queue reordering). M0118-0004 continues.

DONE this loop (committed):
- NEW ENGINE WORK — soft-deadlock resolution in internal/lockmgr/deadlock.go,
  mirroring postgres deadlock.c (lock groups omitted). The detector previously
  built ONLY hard edges (waiter→conflicting-holder) and ONLY ever cancelled a
  victim, so soft cycles (waiter parked behind a conflicting WAITER) were invisible
  and both blocked waiters parked forever. Added: waitEdge type; findLockCycle
  (records soft queue-order edges, honours hypothetical waitOrders); testConfiguration
  (-1 hard / 0 good / >0 soft); deadLockCheck (DeadLockCheckRecurse — try reversing
  each soft edge); expandConstraints + topoSort (rebuild affected queues,
  waiter-before-blocker, minimal disturbance); applyWaitOrders (rewrite
  lockState.waiters preserving *Waiter ptrs + wakePassLocked). checkDeadlockLockedFor:
  prefer!=0 (timer path) runs soft-aware search — soft cycle → reorder+wake (no abort),
  hard cycle → cancel firing backend; prefer==0 (CheckDeadlocksNow / unit tests) →
  legacy hard-only youngest-victim (factored into checkDeadlockHardOnlyLocked).
- Files: internal/lockmgr/deadlock.go (rewrite); internal/testport/isolation_port_test.go
  (+TestPort_IsolationDeadlockSoft +TestPort_IsolationDeadlockSoft2). CSV 2×failed→pass
  (target-inventory.csv, rationale=Go func COMMA-FREE); coverage+inventory regen
  (isolation pass 54→56). Design NEW 0118-0006 + README index row. fix_plan M0118-0004
  annotated.

GATES (all PASS): go build ./...; gofmt+vet clean; -race ./internal/lockmgr/... PASS;
executor Lock|Deadlock PASS; TestPort_IsolationDeadlockSoft + DeadlockSoft2 PASS
byte-for-byte; regression DeadlockHard/DeadlockSimple/LockNowait/TuplelockUpdate PASS;
ralph-state-guard OK (auto-repaired progress.json); pgbench smoke via pre-commit hook.
DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0004 deadlock group, one spec per loop):
    RESUME at `deadlock-parallel` (lock groups / parallel workers — goopg has no
    lock-group concept yet; the soft/hard wait-for graph treats each backend as its
    own leader, so a faithful port needs a lock-group abstraction OR a spec-specific
    shortcut; assess whether goopg even runs parallel workers for this spec before
    committing to lock-group plumbing). THEN `multixact-no-deadlock` and
    `tuplelock-upgrade-no-deadlock` — these wait on row-lock xmax/WaitForXID, NOT
    lockmgr heavyweight locks, so the wait-for graph in deadlock.go CANNOT see those
    waits; hardest remaining — needs a way to surface xmax-wait edges into the
    detector (or a parallel row-lock wait graph). Per-spec: write TestPort_Isolation<Name>
    FIRST, capture the live diff BEFORE engine work; fix → green → CSV failed→pass
    (rationale=Go func COMMA-FREE) → regen gen-isolation-coverage + gen-oracle-inventory
    → design slice + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free — [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s);
LOCK TABLE never fires in pgbench TPC-B/TPC-H so TPS blast radius nil. Soft-resolution
runs on the timer path ONLY (prefer!=0); CheckDeadlocksNow unchanged. The soft engine
(findLockCycle/deadLockCheck/expandConstraints/topoSort/applyWaitOrders) is NOW landed —
reuse it. There is a pre-existing forvar lint nit at isolation_port_test.go:50 (unrelated
to this change — do not refactor existing tests).
