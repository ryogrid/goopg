Task: M0118-0003 — LANDED the ADVISORY-LOCK OWNER-IDENTITY ENABLER (resume point
#3, lock-update-delete). This loop (#54) validated + completed the prior loop's
interrupted WIP (it died at api_limit 18:57 mid-task, uncommitted). The advisory-
lock infinite hang in lock-update-delete is GONE; the spec now runs all 12 perms
to completion. lock-update-delete is NOT yet promoted (output still diverges).

DONE this loop (#54):
- advisory.go: advisorySessionIDFromContext now PREFERS the per-connection
  AdvisorySessionIdentity (*config.SessionRegistry, stable whole-connection) over
  ctx.Session (*BasicSession, nil before first BEGIN). + NEW ReleaseAllAdvisoryLocks
  (releaseAll, both scopes) for backend teardown (PG frees all advisory at exit).
- conn_tx.go: NEW connTxState.AdvisoryID field; End() releases xact advisory under
  it (=sess) not c.sess. server.go: connTx.AdvisoryID=sess + defer ReleaseAllAdvisoryLocks(sess).
  dispatch.go/dispatch_extended.go: release-target order inverted to prefer
  AdvisorySessionIdentity (match acquire). 5 sibling sites all key on per-conn sess.
- FIXED a WIP test bug: TestSyntax_AdvisoryLock_ReleasedOnDisconnect asserted on
  pg_advisory_lock(7)'s return — but pg_advisory_lock() returns VOID (only
  pg_try_advisory_lock returns bool) → changed to ExecContext.
- Tests: TestSyntax_AdvisoryLock_SessionUnlockAcrossBeginBoundary +
  ...ReleasedOnDisconnect PASS; TestPort_IsolationLockUpdateDelete added as
  deferred SKIP guard. Design 0118-0002 slice 7 + resume-#3 + README index. Ledger row.

GATES (all PASS): go build ./...; go vet executor/server; executor+server unit
suites; -race server; all TestSyntax_Advisory* (pre-existing AutoCommit ownership
tests included — NO regression); the 2 new syntax tests PASS. gofmt: fixed NEW
issues in server.go (trailing-comment realign from my longer connTx line) + test
file (blank line); advisory.go stays gofmt-dirty = PRE-EXISTING go1.25/1.26
advisoryManager struct-comment artifact (do NOT gofmt -w it). NOTE: cleaned a STALE
orphan goopg server in goopg-test.scope (tmp/lud-data, 18:53, from the interrupted
loop) before tests — systemctl --user stop goopg-test.scope. tpch-spotcheck not
triggered (advisory path never fires in pgbench/TPC-H). DO NOT stage: postgres,
weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (resume point #3 — promote lock-update-delete to pass): fix the TWO
    remaining output divergences (TestPort_IsolationLockUpdateDelete, deferred):
    (A) [output-format] goopg's isolation_runner omits the session-`setup`
        pg_advisory_lock(0) result block that PG prints atop each permutation.
    (B) [LOAD-BEARING, deep] when s1l unblocks at s2_unlock it returns the STALE
        pre-update (1,1) row immediately instead of re-traversing the update chain
        s2 built while s1l was parked and WAITING on s2's in-flight DELETE/key-UPDATE
        (PG keeps s1l <waiting ...> until s2c, then (0 rows)). This is the READ
        COMMITTED blocked-then-woken FOR KEY SHARE locker re-evaluation gap: s1l
        took its snapshot before s2u; on wakeup it must re-fetch the latest version
        via t_ctid (EPQ) and waitForConflictingRowLock/EPQ-wait on the in-flight
        deleter. This is the lockRowsOp WAKEUP path (slice 6 already did the write
        side). Bound it — M0072-0002 hang precedent. Then savepoint/subxact members,
        WAL/pg_multixact persistence, then remaining spec promotion.

GOTCHAS: pg_advisory_lock() returns VOID; pg_try_advisory_lock returns bool. The 5
advisory identity sites must ALL key on the per-connection *config.SessionRegistry
(sess) — acquire (advisorySessionIDFromContext→AdvisorySessionIdentity), 2 per-stmt
xact releases (dispatch*), End() (AdvisoryID), teardown (ReleaseAllAdvisoryLocks),
pg_advisory_unlock_all (advisorySessionIDFromContext). ctx.Session flips nil→non-nil
at first BEGIN — never key advisory ownership on it. lockmgr locks statement-scoped
([[lockmgr_locks_are_statement_scoped]]).
