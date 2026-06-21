Task: M0118-0003 (row locking) — COMPLETE this loop: promoted `lock-nowait` (slice 15)
via transaction-scoped LOCK TABLE heavyweight locks. The M0118-0003 group continues.

DONE this loop (committed):
- NEW ENGINE WORK — txn-scoped LOCK TABLE. lockmgr had every primitive but (1)
  lockmgr.New() had NO production caller (Context.LockMgr always nil — row blocking
  rides xmax/WaitForXID) and (2) lifecycle was per-statement.
- Dedicated always-on `executor.tableLockMgr` singleton (separate from the nil
  Context.LockMgr → confines blast radius to LOCK TABLE; scans/DML/DDL untouched).
- Stable per-connection `connTxState.LockBackendID` (minted once from nextBackendID),
  threaded to `Context.TxnLockBackendID` only while `connTx.InExplicit()`.
- execLockTable→lockRelationTransitively→acquireRelLockTxn Acquire/TryAcquire on
  tableLockMgr under TxnLockBackendID; per-statement ReleaseAll leaves it; End()
  releases via executor.ReleaseTableLocks at COMMIT/ROLLBACK + teardown. NOWAIT→55P03.
- Files: executor/context.go, executor/operators_ddl.go, server/{conn_tx,server,dispatch}.go,
  testport/isolation_port_test.go (+TestPort_IsolationLockNowait). CSV failed→pass,
  inventory+coverage regen (isolation 51→52). Design NEW 0118-0003 + 0118-0002 slice 15
  + README index. Ledger row appended.

GATES (all PASS): go build ./...; gofmt clean; TestPort_IsolationLockNowait PASS
byte-for-byte; -race over lockmgr + LockNowait/LockCommittedUpdate/TuplelockPartition/
PropagateLockDelete PASS; internal/executor + internal/server + internal/lockmgr PASS;
ralph-state-guard OK (self-repaired progress marker); pgbench smoke via pre-commit hook.
DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    Remaining `failed` specs (hardest left): `tuplelock-upgrade-no-deadlock` /
    `multixact-no-deadlock` — both need DEADLOCK DETECTION across the row-lock wait
    graph. KEY INSIGHT: lockmgr already HAS a wait-for-graph deadlock detector (used for
    relation locks) but the ROW-lock path waits via xmax/WaitForXID, NOT lockmgr, so the
    detector cannot see row-lock waits. The new tableLockMgr added this loop is a possible
    host. Also remaining: `multixact-no-forget` (WAL/pg_multixact member persistence);
    `aborted-keyrevoke`/`delete-abort-savept` (subxact-lock-restore). Each is its own loop.
    Per-spec: write TestPort_Isolation<Name> FIRST and run it to capture the live diff
    BEFORE engine work (always measure first), then fix → green → CSV failed→pass
    (rationale=Go func, COMMA-FREE) → regen gen-isolation-coverage + gen-oracle-inventory
    → design doc slice + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free — [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s);
LOCK TABLE / row-lock path never fires in pgbench TPC-B/TPC-H so TPS blast radius nil.
lockmgr.New() is otherwise dead in prod — do NOT wire the GLOBAL Context.LockMgr (would
activate every scan/DML/DDL acquire); use a dedicated manager like tableLockMgr.
