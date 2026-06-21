Task: M0118-0003 — COMPLETE this loop: a measurement-driven BATCH PROMOTION of
FIVE more row-locking specs that already pass byte-for-byte on the slices 1–9
multixact/chain infra with ZERO new engine code. Promoted to pass:
skip-locked-2, nowait-2, skip-locked-3, nowait-5, tuplelock-conflict.

DONE this loop (committed):
- internal/testport/isolation_port_test.go: added dedicated TestPort_Isolation*
  for the 5 specs (SkipLocked2/Nowait2/SkipLocked3/Nowait5/TuplelockConflict).
- docs/test-port/postgres-oracle-target-inventory.csv: 5 rows failed→pass
  (comma-free rationales = Go func names); regenerated
  postgres-oracle-target-inventory.md + upstream-isolation-coverage.md via
  gen-oracle-inventory + gen-isolation-coverage.
- docs/design/0118-0002-*: added slice 10 (batch promotion); downgraded the
  savepoint/subxact-members blocker ⛔→latent (KEY FINDING below); updated
  Deferred items 4+5; README index status + slice-10 sentence. Ledger row.

KEY FINDING (supersedes prior ledger/design estimates):
- tuplelock-conflict passes WITHOUT threading a subxid into the producer: every
  conflict consumer evaluates against the STRONGEST lock mode held on the tuple
  (multixact.HintBits), identical whether or not the SAVEPOINT re-lock is a
  distinct subxid member — subxid membership is NOT observable in its output.
- skip-locked-2/nowait-2 need only IN-MEMORY multixact membership (single-process,
  no crash) — NOT the WAL/pg_multixact persistence prior rows estimated.

GATES (all PASS): go build ./...; go vet executor+testport; gofmt clean;
5 new TestPort_Isolation* PASS; regression batch LockUpdateDelete/
LockUpdateTraversal/Nowait/Nowait3/SkipLocked/LockCommittedKeyupdate/
UpdateLockedTuple PASS; multixact -race + executor lock-unit -race PASS;
ralph-state-guard OK; pgbench smoke via pre-commit hook.
DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    RESUME at skip-locked-4 / nowait-4 — both are SKIP LOCKED / NOWAIT on an
    UPDATED tuple CHAIN (re-measured `defer` this loop, output mismatch). Add
    TestPort_IsolationSkipLocked4 / TestPort_IsolationNowait4 (removed this loop
    since they defer) to see the live diff; the wait-policy (skip/nowait) must be
    honoured while FOLLOWING the chain, not only at the head version. Likely the
    stampLockInner chain-follow recursion does not thread waitPolicy/lock-only
    conflict into the successor read. Then propagate-lock-delete (FK-INSERT + RI
    trigger enforcement — heavyweight), then lock-nowait (LOCK TABLE needs a
    txn-scoped heavyweight lock lifecycle, [[lockmgr_locks_are_statement_scoped]]),
    tuplelock-update/partition (advisory chains / partitioned tables),
    multixact-no-forget (WAL/pg_multixact member persistence), and the
    deadlock-detection specs (multixact-no-deadlock / tuplelock-upgrade-no-deadlock).
    Per-spec workflow: run TestPort_Isolation<Name> for the live diff → fix →
    green → CSV failed→pass (rationale=Go func) → regen gen-isolation-coverage +
    gen-oracle-inventory → design doc slice + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free (unquoted comma-delimited rows) —
[[serena_replace_content_dotall_eats_file]] / memory note. The multixact lock-only
producer (stampMultiLock) + activeLockHolders + tupleLockConflicts (delegates to
multixact.StatusesConflict against the strongest HintBits member) cover the
multi-SHARE conflict path; the SKIP/NOWAIT/wait branch is in stampLockInner's
lock-only conflict block. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s);
row-lock path never fires in pgbench TPC-B/TPC-H so TPS blast radius nil.
