Task: M0118-0003 — COMPLETE this loop: lock-update-delete.spec PROMOTED to pass
(divergence (B) fixed — the READ COMMITTED blocked-then-woken locker chain
re-traversal). Both blockers of lock-update-delete are now closed; spec passes
all 12 permutations byte-identical vs PG 18.3.

DONE this loop (committed):
- internal/executor/operators_lockrows.go: propagateLockForward rewritten as a
  faithful heap_lock_updated_tuple_rec — returns a chainLockOutcome
  (chainLockOK/Deleted/Updated). New classifyChainConflict + chainMembers =
  test_lockmode_for_conflict analog over each chain version's holder(s): WAIT
  (WaitForXID, bounded by query ctx) on a conflicting in-flight DELETE/key-UPDATE
  then re-read the SAME version; committed conflict -> chainLockDeleted (0 rows)
  or chainLockUpdated (EPQ recheck the successor). Branch (a) of stampLockInner
  maps the outcome (epqSkipped for deleted/recheck-fail; keep (1,1) for OK).
- Two goopg seams: (a) goopg DELETE leaves t_ctid self-pointing WITHOUT
  HEAP_KEYS_UPDATED (PG's heap_delete sets it) -> chainMembers classifies a
  self-ctid real-updater as StatusUpdate so it conflicts with FOR KEY SHARE
  (fixed blocker1); (b) foo.key is a PRIMARY KEY so key=1 is the index-scan cond
  not a filterOp pred -> Open folds indexScanPredicate into the EPQ recheck pred
  (fixed blocker2).
- Promotion: target-inventory CSV row -> pass; regen coverage + inventory.
  Design 0118-0002 slice 9 + resume-#3 entry + README index. Ledger row.

GATES (all PASS): go build ./...; go vet executor; gofmt clean;
executor+multixact unit (incl -race); TestPort_IsolationLockUpdateDelete PASS;
regression batch TestPort_Isolation{LockUpdateTraversal,LockCommittedKeyupdate,
UpdateLockedTuple,SkipLocked,Nowait,InsertConflictDoUpdate} all PASS.
make ralph-state-guard OK (self-repaired). pgbench smoke via pre-commit hook.
DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    Pick the next-cheapest remaining spec. Candidates in dependency order:
    - tuplelock-conflict: needs savepoint/subxact members (main xid + a subxid
      locking the same tuple = distinct multixact members; thread the subxid into
      the producer stampMulti*/lockMemberStatus).
    - propagate-lock-delete: needs FK-INSERT lock propagation (and the chain-wait
      now landed). Re-measure its diff first — the locker-side wait may already
      cover part of it.
    - skip-locked-2 / nowait-2 / multixact-no-forget: need WAL/pg_multixact
      persistence of multixact members (Store seeded from pg_control.nextMulti +
      a real heap-lock-updated WAL record carrying member sets).
    - multixact-no-deadlock / tuplelock-upgrade-no-deadlock: need deadlock
      detection across the row-lock wait graph.
    Run the spec's TestPort_Isolation<Name> first to see the live diff before
    designing. Per-spec workflow: green -> CSV status=pass (rationale=Go func) ->
    regen gen-isolation-coverage + gen-oracle-inventory -> design doc + README.

GOTCHAS: HeapKeysUpdated (0x2000, infomask2) is consumed ONLY by lock-conflict
logic (not visibility/decode) — that's why the structural delete-detection is
safe without touching the hot DELETE/WAL path. lockmgr locks are statement-scoped
([[lockmgr_locks_are_statement_scoped]]); cross-statement gate is WaitForXID. The
chain-wait WAIT is bounded by the 10-min query ctx + a hops<64 backstop
(M0072-0002 hang precedent). epqRecheckFilter now also re-applies the index-scan
key predicate (folded in Open) — keep that in mind for any future EPQ change.
