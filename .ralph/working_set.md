Task: M0118-0004 (deadlock detection) — COMPLETE this loop: promoted `deadlock-hard`
(first general-detector slice; design 0118-0005). M0118-0004 continues.

DONE this loop (committed):
- NEW ENGINE WORK — general timeout-driven multi-object LOCK TABLE deadlock detection.
  Three gaps fixed: (1) `deadlock_timeout` was NOT a GUC → SET failed at session setup;
  registered (config/defaults.go + postgresql.conf.sample, TypeInt UnitMs BootVal 1000
  ContextSuset). (2) detector always picked YOUNGEST backend but PG rolls back the
  backend that RUNS the check → new lockmgr.AcquireWithTimeout + Context.deadlockTimeout()
  feed per-session timeout; timer fires runDeadlockCheckFor(b) → checkDeadlockLockedFor(prefer)
  picks `prefer` when in cycle else youngest; CheckDeadlocksNow keeps prefer=0 (unit tests
  unchanged). (3) runner ignored `(*)` BlockerStar marker → hasStarBlocker renders such a
  step waiting/completed even when it completed immediately (victim's fast 40P01), then
  drainWithTimeout so s7a8 (gated on s8a1) surfaces in order. No passing spec uses (*).
- Files: internal/config/defaults.go + postgresql.conf.sample; internal/lockmgr/lockmgr.go
  (+AcquireWithTimeout +acquire +useConfiguredTimeout) + deadlock.go (runDeadlockCheckFor
  + checkDeadlockLockedFor prefer-victim); internal/executor/context.go (+deadlockTimeout()
  + AcquireWithTimeout call); internal/testport/framework/isolation_runner.go (+hasStarBlocker
  + (*) immediate-branch); testport/isolation_port_test.go (+TestPort_IsolationDeadlockHard).
  CSV failed→pass; coverage+inventory regen (isolation pass 53→54). Design NEW 0118-0005 +
  README index. Ledger row appended; fix_plan M0118-0004 annotated.

GATES (all PASS): go build ./...; gofmt+vet clean; TestPort_IsolationDeadlockHard PASS
byte-for-byte; -race ./internal/lockmgr/... PASS; -race executor Lock|Deadlock PASS;
config tests PASS (GUC + sample); DeadlockSimple/LockNowait/TuplelockUpdate still PASS;
ralph-state-guard OK; pgbench smoke via pre-commit hook.
DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0004 deadlock group, one spec per loop):
    RESUME at `deadlock-soft` (then `deadlock-soft-2`). These need SOFT-deadlock
    wait-queue REORDERING: when a cycle has a soft edge (a waiter blocking another
    waiter, not a holder), PG's deadlock.c FindLockCycle + ProcLockWakeup rearrange
    the wait queue to break the cycle WITHOUT aborting anyone. goopg's detector
    currently only KILLS a victim — it has no queue-reorder path. deadlock-soft:
    4 procs, 2 hard + 2 soft edges; detector reverses the d1-e2 soft edge, unblocking
    d1, nobody dies (expected output has NO error). deadlock-soft-2: s1 must jump over
    BOTH s3 and s4 (hard-blocked on a1) and grab a2 immediately. Both ride tableLockMgr
    + the per-session deadlock_timeout + (*) marker landed this loop. Implement soft-edge
    detection in checkDeadlockLockedFor: distinguish waiter→holder (hard) from
    waiter→waiter (soft) edges; on a cycle with a soft edge, reorder the wait queue
    (move the blocked waiter ahead) and re-run wakePass instead of cancelling.
    THEN: `deadlock-parallel`; `multixact-no-deadlock`/`tuplelock-upgrade-no-deadlock`
    (row-lock xmax/WaitForXID wait graph — lockmgr can't see those waits; hardest).
    Per-spec: write TestPort_Isolation<Name> FIRST, run to capture the live diff BEFORE
    engine work; fix → green → CSV failed→pass (rationale=Go func COMMA-FREE) → regen
    gen-isolation-coverage + gen-oracle-inventory → design slice + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free — [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s);
LOCK TABLE never fires in pgbench TPC-B/TPC-H so TPS blast radius nil. The per-session
deadlock_timeout + firing-backend victim + (*) runner marker are NOW landed — reuse them.
deadlock_timeout sample entry MUST equal BootVal exactly (1000) or config test fails.
