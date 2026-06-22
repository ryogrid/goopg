(idle — nothing in flight)

Last loop (#25, M0118-0008): promoted `vacuum-skip-locked` (7th M0118-0008
promotion, design 0118-0033). Committed + pushed.

## What landed (one task)
VACUUM/ANALYZE (SKIP_LOCKED) conditional maintenance lock + runner severity fix.
- context.go: new `(*Context).tryAcquireMaintenanceLock(rel,mode) bool` — mirrors
  PG ConditionalLockRelationOid: TryAcquire under active backend, release-now,
  false on contention ⇒ skip.
- operators_vacuum.go / operators_analyze.go: `expandVacuumTargets` /
  `expandAnalyzeTargets` tag each relation explicit (user-named ⇒ WARNING
  "skipping vacuum/analyze of X --- lock not available") vs expanded (partition
  child ⇒ silent skip) and collect partitioned parents. ANALYZE of a partitioned
  parent calls `analyzeInheritanceWait` (free fn) → blocking AccessShareLock on
  each leaf via acquireRelLockMaybeTransient (SKIP_LOCKED does NOT cover the
  inheritance read), so ANALYZE waits under ACCESS EXCLUSIVE but not SHARE; plain
  VACUUM never waits. Shared `vacuumTarget` struct. VACUUM FULL probes
  AccessExclusiveLock; else ShareUpdateExclusiveLock.
- isolation_runner.go: `sessionNoticeQueue.push(severity,msg)` stores
  "SEVERITY:  msg"; handler reads `pq.Error.Severity`; the 4 emit sites print
  "%s: %s" (was hard-coded "%s: NOTICE:  %s"). Empty severity ⇒ NOTICE.
- TestPort_IsolationVacuumSkipLocked strict PASS (16 perms).
GATES: build PASS; vacuum-skip-locked strict PASS; FULL TestPort_Isolation* suite
PASS (584s — no severity-change regression); framework units PASS; executor
vacuum/analyze/freeze + full executor pkg PASS; -race lockmgr PASS; state-guard
OK. CSV field count 7 verified; docs regenerated.

KEY METHODOLOGY: throwaway zz_probe_test.go (RAWDIFF dump) showed first
divergence = first permutation's missing WARNING line — a single behaviour
(conditional lock + severity), not a deep feature gap. Probe deleted.

NEXT loop candidates (remaining M0118-0008 tail — probe-first):
- `alter-table-4`: table INHERITS + ALTER TABLE INHERIT/NO INHERIT + child-lock
  ordering. Med.
- `alter-table-1/2`: FK NOT VALID parse + VALIDATE CONSTRAINT + lock semantics.
- `*-conflict` family (truncate/vacuum/cluster): CREATE ROLE/SET ROLE/OWNER ACL.
  Large (biggest unblock leverage).
- `vacuum-no-cleanup-lock`: needs vacuum_multixact_freeze_min_age GUC + reltuples.
- `detach-partition-concurrently-1`: DETACH PARTITION CONCURRENTLY parse +
  concurrent-detach semantics.

REUSABLE NEW: `tryAcquireMaintenanceLock` (conditional/NOWAIT transient probe);
`expandVacuumTargets`/`expandAnalyzeTargets` (partition expansion + explicit
tag); `analyzeInheritanceWait`. Runner now severity-aware (WARNING vs NOTICE).
PRIOR: acquireDDLLockTxn / acquireWriteLockTxn / acquireScanReadLockTxn /
acquireRelLockMaybeTransient / waitForRelationLockers; mvcc
SnapshotActiveOtherSlots/WaitForSlotsToCommit; planner.ExprContainsColumnRef.

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). SKIP_LOCKED VACUUM
runs in AUTOCOMMIT (TxnLockBackendID==0) ⇒ use per-statement BackendID for the
probe. D-002 CSV is giant single-line row #13 (field 6 rationale COMMA-FREE;
append before `,M0060-0004`; verify `awk -F, 'NR==13{print NF}'`==7). regen:
gen-oracle-port-status + gen-isolation-coverage --repo-root . + gen-oracle-
inventory --repo-root . NEVER `cd` into postgres/. never gofmt -w. Untracked
postgres/ + weekly_loc.* + requirements.txt are stray — leave.
.ralph/progress.json driver-managed.
