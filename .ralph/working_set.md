Task: M0118-0003 — LANDED lock-update-delete divergence (A): isolation-runner
session-setup result printing (slice 8). This loop closed the output-format half
of the lock-update-delete promotion. Spec is NOT yet promoted — divergence (B)
remains. (A)+(B) were both blockers; only (B) is left now.

DONE this loop:
- internal/testport/framework/isolation_runner.go: runPermutation now writes the
  `starting permutation:` header BEFORE the per-session setup loop, and runs each
  session's `setup` block via NEW `execConnSetupCapture` (reuses execStep — same
  statements/side effects, captures rows — formats the LAST row-bearing result via
  pqprintFormat; empty for COMMAND_OK-only blocks). Mirrors isolationtester.c
  run_permutation (PGRES_TUPLES_OK → printResultSet, lines 534-569). SET
  application_name stays uncaptured via execConn (goopg-only, PG never runs it).
- Result: lock-update-delete's `pg_advisory_lock(0)` setup block now prints atop
  all 12 perms; diff dropped from (A)+(B) to ONLY (B).
- Design 0118-0002 slice 8 + resume-#3 status (A→fixed) + README index. Ledger row.

GATES (all PASS): go build ./internal/testport/...; go vet framework; gofmt clean;
framework unit tests; TestPort_IsolationLockUpdateDelete (skips on B, setup block
matches); regression batch TestPort_Isolation{LockCommittedKeyupdate,InsertConflict
DoUpdate,Nowait,LockUpdateTraversal} all PASS (byte-identical — no passing spec has
a row-returning session setup). tpch-spotcheck not triggered (test-only pkg, no
executor/codec change; also INFRA-BLOCKED). DO NOT stage: postgres, weekly_loc.*,
requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (resume point #3 — promote lock-update-delete): fix divergence (B),
    the ONLY remaining blocker. It is the lockRowsOp BLOCKED-THEN-WOKEN-LOCKER
    re-evaluation gap (executor, NOT the test harness). When s1l unblocks at
    s2_unlock under READ COMMITTED it locks+returns the stale originally-visible
    (1,1) row immediately, instead of EvalPlanQual-following t_ctid to the latest
    version and — for blocker1 (DELETE) / blocker2 (key-UPDATE) — WAITING on s2's
    in-flight deleter until s2c, then returning (0 rows). blocker3 (no-key UPDATE)
    already matches PG: (1,1) immediately at s2_unlock (KEY SHARE compatible, no
    wait). So the fix is ONLY the wait+re-traverse path for conflicting blockers,
    in the lockRowsOp Next/stampLock WAKEUP (slice 6 did the write-path side; this
    is the locker-side wakeup — operators_lockrows.go: Next/stampLock/stampLockInner
    /epqRecheckFilter/refetchRow/propagateLockForward). BOUND the wait (M0072-0002
    hang precedent). Then savepoint/subxact members, WAL/pg_multixact persistence,
    remaining MultiXact-cluster spec promotion.

GOTCHAS: only 4 specs have tuple-returning session setups (lock-update-delete +
deferred plpgsql-toast/prepared-transactions/temp-schema-cleanup) — passing specs
are byte-identical under the new capture. PQexec prints only the LAST result of a
multi-statement setup → execConnSetupCapture takes results[last]. Global-setup
result printing (RunSpec, control conn) left unchanged — no in-scope spec needs it.
lockmgr locks statement-scoped ([[lockmgr_locks_are_statement_scoped]]); the
cross-statement gate is WaitForXID / waitForConflictingRowLock, not lockmgr.
