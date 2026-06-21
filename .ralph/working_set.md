Task: M0118-0003 (row locking) — COMPLETE this loop: promoted `propagate-lock-delete`
(slice 14). The M0118-0003 spec group continues (specs below still failed).

DONE this loop (committed):
- PURE PROMOTION — NO engine code. propagate-lock-delete already passes byte-for-byte
  on the existing FK in-flight-child-insert wait path + M0118-0003 row-lock infra.
- internal/testport/isolation_port_test.go: +TestPort_IsolationPropagateLockDelete
  (runIsoSpec on propagate-lock-delete.spec).
- CSV target-inventory line 579: failed->pass (comma-free rationale); regenerated
  postgres-oracle-target-inventory.md + upstream-isolation-coverage.md (isolation
  pass 50->51).
- docs/design/0118-0002-*: slice 14 section + status checklist (✅ propagate-lock-delete)
  + README index slice-14 sentence. Ledger row appended.

ROOT CAUSE / FINDING: goopg does NOT model the FK check as a heap row-lock that must
be propagated across the UPDATE's version chain (PG's RI FOR KEY SHARE on parent).
Instead the parent DELETE's enforceFKOnDelete -> fkChildWaitForInFlightInsert
(M0100-0005w) -> detectInFlightChildInsert scans the CHILD relation for an in-flight
referencing INSERT, WaitForXIDs on s1/s2, then after commit assertNoChildRows raises
23503. No parent-side lock to lose across s3's intervening UPDATE; aborted-savepoint
variant keys off the same child inserter xids. All 8 perms already pass.

GATES (all PASS): go build ./...; go vet testport; gofmt clean on isolation_port_test.go;
TestPort_IsolationPropagateLockDelete PASS (8 perms via RunAndCompare expected diff);
regression batch LockUpdateTraversal/LockUpdateDelete/LockCommittedKeyupdate/
TuplelockPartition/TuplelockConflict all PASS; ralph-state-guard OK; pgbench smoke via
pre-commit hook. DO NOT stage: postgres, weekly_loc.*, requirements.txt,
weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    Remaining M0118-0003 `failed` specs: `lock-nowait` (LOCK TABLE — needs a NEW
    txn-scoped heavyweight lock lifecycle, [[lockmgr_locks_are_statement_scoped]]) and
    `tuplelock-upgrade-no-deadlock` (needs row-lock wait-graph DEADLOCK DETECTION across
    sessions — lock upgrades + savepoints + multixact retry; the hardest). Both distinct
    higher-blast subsystems, each its own loop. RECOMMENDED: write the
    TestPort_Isolation<Name> first and run it to capture the live diff before any engine
    work (this loop's spec turned out to already pass — always measure first).
    Per-spec workflow: TestPort_Isolation<Name> -> fix -> green -> CSV failed->pass
    (rationale=Go func, COMMA-FREE) -> regen gen-isolation-coverage + gen-oracle-inventory
    -> design doc slice + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free — [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s);
FK/row-lock path never fires in pgbench TPC-B/TPC-H so TPS blast radius nil.
