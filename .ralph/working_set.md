Loop #50 COMPLETE: M0118-0009 — prepared-transactions-cic PROMOTED to pass-required
(design 0118-0111). Committing + pushing.

What landed (blast radius nil — WaitForSlotsToCommit byte-unchanged when lock_timeout=0):
- Closed the 0118-0110 residual gap: CIC active-slot wait now honours session lock_timeout.
  - internal/mvcc/manager.go WaitForSlotsToCommit: arms lockwait.Timeout(ctx) via a
    time.Timer; on fire it closes a `timedOut` chan + broadcasts commitCond; the wait
    loop returns lockwait.ErrLockTimeout. Mirrors lockmgr.ProcSleep's
    enable_timeout_after(LOCK_TIMEOUT). No-budget path (nil channel) blocks forever in
    select → unchanged.
  - internal/executor/operators_ddl.go createIndex CIC drain: maps the wait error via the
    shared lockWaitTimeoutError helper → "canceling statement due to lock timeout" (no
    sibling cancellation path); other cancels keep generic 57014.
  - Test: TestPort_IsolationPreparedTransactionsCIC (runIsoSpecStrict) — full spec
    byte-identical to PG 18.3.
  - CSV D-002 rationale appended (comma+quote-free) + md regenerated; design 0118-0111 +
    README row; fix_plan + deferral ledger rows.

Gates (PASS): TestPort_IsolationPreparedTransactionsCIC strict; TestPort_IsolationMultipleCic
non-regression (default lock_timeout=0 path); go test -race ./internal/mvcc/... green;
build+vet+gofmt clean. pgbench smoke = pre-commit hook.

NEXT (remaining M0118-0009, all Effort-L):
- prepared-transactions: full 1500-perm SERIALIZABLE SSI verification across held prepared
  xacts (mechanism in place from 0118-0110; held xact keeps SSI predicate locks + SSI check
  fires at COMMIT PREPARED). Validate byte-for-byte; close first-committer/conflict-ordering
  gaps. Probe via throwaway zz_probe_test.go (IsolationRunner.RunAndCompare).
- stats: needs cumulative pg_stat_* subsystem (function/relation/SLRU stats,
  pg_stat_force_next_flush, track_functions, stats_fetch_consistency) on top of 2PC.
- intra-grant-inplace: pg_class rowmark locking (Effort-L MVCC-tuple-lock core; perm1 done
  in 0118-0109).
Other failing M0118 specs: index-only-bitmapscan, predicate-gin/gist, deadlock-parallel,
fk-partitioned-1/2 (distinct unbuilt subsystems).
