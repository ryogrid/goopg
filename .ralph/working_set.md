Task: M0118-0003 (row locking) — COMPLETE this loop: promoted `deadlock-simple` (slice 16)
via synchronous simple (lock-upgrade) deadlock detection in lockmgr. M0118-0003 continues.

DONE this loop (committed):
- NEW ENGINE WORK — lockmgr early/simple deadlock detection. lockmgr had ONLY the
  timeout-driven detector (time.AfterFunc(deadlockTimeout)); PG's JoinWaitQueue (proc.c)
  resolves a simple lock-upgrade deadlock SYNCHRONOUSLY when the 2nd upgrader joins the
  queue (no deadlock_timeout wait) → victim errors immediately with NO `<waiting>` marker.
- Added lockState.hasSimpleDeadlock(b,m): mirrors the JoinWaitQueue scan — b holds a lock
  here AND would queue behind first conflicting waiter w (w.Mode conflicts w/ b's held →
  w waits for b) AND b's mode conflicts w/ w's held (b waits for w) → deadlock, b is victim.
- Acquire calls it after canGrantImmediately (mutually exclusive at the deciding waiter)
  and before enqueue; on hit: unlock → ReleaseAll(b) (mirrors timeout victim path so the
  survivor's wake-pass fires) → return ErrDeadlockDetected (→40P01 via acquireRelLockTxn).
- Files: internal/lockmgr/lockmgr.go (+hasSimpleDeadlock +Acquire branch);
  testport/isolation_port_test.go (+TestPort_IsolationDeadlockSimple). CSV failed→pass,
  coverage+inventory regen (isolation pass 52→53). Design NEW 0118-0004 + README index.
  Ledger row appended.

GATES (all PASS): go build ./...; gofmt+vet clean; TestPort_IsolationDeadlockSimple PASS
byte-for-byte; -race ./internal/lockmgr/... PASS (incl. linear-chain false-positive guard);
-race executor Lock|Deadlock PASS; LockNowait/TuplelockConflict/LockCommittedKeyupdate still
PASS; ralph-state-guard OK (self-repaired progress marker); pgbench smoke via pre-commit hook.
DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    Remaining deadlock specs split two ways:
    (A) LOCK-TABLE multi-object: `deadlock-soft` / `deadlock-soft-2` / `deadlock-hard` —
        ride the now-wired tableLockMgr but need the GENERAL timeout detector to (a) find
        multi-object cycles and (b) do SOFT-deadlock queue reordering (rearrange wait queue
        to avoid killing anyone). lockmgr.checkDeadlockLocked already walks a wait-for graph
        + picks youngest victim, but verify it handles multi-tag cycles + add soft reorder.
        CLOSEST to this loop's work — start here.
    (B) ROW-LOCK wait graph: `tuplelock-upgrade-no-deadlock` / `multixact-no-deadlock` —
        the row-lock path waits via xmax/WaitForXID NOT lockmgr, so lockmgr's detector
        can't see those waits; needs a row-lock wait-for graph. Harder.
    Also: `multixact-no-forget` (WAL/pg_multixact member persistence);
    `aborted-keyrevoke`/`delete-abort-savept` (subxact-lock-restore). Each its own loop.
    Per-spec: write TestPort_Isolation<Name> FIRST, run it to capture the live diff BEFORE
    engine work (always measure first); then fix → green → CSV failed→pass (rationale=Go
    func, COMMA-FREE) → regen gen-isolation-coverage + gen-oracle-inventory → design slice
    + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free — [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s);
LOCK TABLE / row-lock path never fires in pgbench TPC-B/TPC-H so TPS blast radius nil.
lockmgr early-deadlock check is mutually exclusive with the early-GRANT rule
(canGrantImmediately) at the deciding waiter — lock-nowait still passes. The simple-deadlock
case is single-object only; multi-object + soft reorder is the GENERAL detector's job.
