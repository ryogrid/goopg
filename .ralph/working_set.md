Task: M0118-0003 (row locking) — COMPLETE this loop: promoted skip-locked-4 +
nowait-4 (SKIP LOCKED / NOWAIT on an UPDATED tuple chain) via a single-branch
engine fix. The M0118-0003 spec group continues (more specs below).

DONE this loop (committed):
- internal/executor/operators_lockrows.go: stampLockInner's real-updater wait
  branch (keyConflict path, non-lock-only foreign xmax) now consults
  o.waitPolicy BEFORE WaitForXID — LockWaitSkipLocked→return epqSkipped,
  LockWaitNoWait→relation-qualified 55P03, default→original WaitForXID. Mirrors
  the lock-only branch that already honoured the policy. ~13 new lines.
- internal/testport/isolation_port_test.go: added TestPort_IsolationSkipLocked4
  + TestPort_IsolationNowait4 (both PASS).
- CSV target-inventory: 2 rows failed→pass (comma-free rationales = Go funcs);
  fixed stale "remain deferred" note in skip-locked.spec row; regenerated
  postgres-oracle-target-inventory.md + upstream-isolation-coverage.md.
- docs/design/0118-0002-*: slice 11 section; status checklist (✅ skip-locked-4
  /nowait-4); README index status + slice-11 sentence. Ledger row appended.

ROOT CAUSE (why nowait-5 passed but nowait-4/skip-locked-4 hung): both follow
the t_ctid chain after waking from a pg_advisory_lock(0) gate, but the chain
TIP differs — nowait-5 reaches a FOR SHARE LOCK-ONLY xmax (handled by the
already-correct lock-only branch); nowait-4/skip-locked-4 reach an IN-PROGRESS
REAL UPDATE xmax (s2's uncommitted second UPDATE), which the real-updater branch
waited on unconditionally → `<... timed out waiting>`. Fix = honour the wait
policy in BOTH branches.

GATES (all PASS): go build ./...; go vet executor+testport; gofmt clean;
TestPort_IsolationSkipLocked4/Nowait4 PASS; 12-spec regression batch
(LockUpdateDelete/LockUpdateTraversal/Nowait/Nowait2/Nowait3/Nowait5/SkipLocked/
SkipLocked2/SkipLocked3/LockCommittedKeyupdate/UpdateLockedTuple/
TuplelockConflict) PASS; executor lock-unit -race + multixact -race PASS;
ralph-state-guard OK (auto-repaired progress→in_progress); pgbench smoke via
pre-commit hook. DO NOT stage: postgres, weekly_loc.*, requirements.txt,
weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    RESUME at `propagate-lock-delete` (FK-INSERT lock propagation + RI trigger
    enforcement — heavyweight) OR `lock-nowait` (LOCK TABLE needs a txn-scoped
    heavyweight lock lifecycle, [[lockmgr_locks_are_statement_scoped]]). Then
    `tuplelock-update`/`tuplelock-partition` (advisory chains / partitioned
    tables), `multixact-no-forget` (WAL/pg_multixact member persistence), and the
    deadlock-detection specs (multixact-no-deadlock / tuplelock-upgrade-no-deadlock).
    Per-spec workflow: add TestPort_Isolation<Name> for the live diff → fix →
    green → CSV failed→pass (rationale=Go func, COMMA-FREE) → regen
    gen-isolation-coverage + gen-oracle-inventory → design doc slice + README +
    ledger.

GOTCHAS: CSV rationale MUST be comma-free (unquoted comma-delimited rows) —
[[serena_replace_content_dotall_eats_file]]; prefer built-in Edit for Go code.
The row-lock chain-follow wait-policy path is now COMPLETE for both policies at
both head and successor versions. tpch-spotcheck INFRA-BLOCKED (SLRU backfill
>60s); row-lock path never fires in pgbench TPC-B/TPC-H so TPS blast radius nil.
