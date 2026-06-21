Task: M0118-0003 multixact — LANDED the FOUR-WAY row-lock strength member
status (resume point #3, member-status half). Producer now records the correct
FOR KEY SHARE / FOR SHARE / FOR NO KEY UPDATE / FOR UPDATE distinction instead of
collapsing to two strengths.

DONE this loop (#51), committed:
- planner `lockStrengthFromParser`: stop collapsing (KEY SHARE→SHARE /
  NO KEY UPDATE→UPDATE); preserve all 4. Only consumer of LockedRel.Strength is
  the lockRowsOp executor → widening is local.
- executor `Open` (operators_lockrows.go): four-way switch → lockStrength infomask
  bits + new `lockKeysUpdated` bool. FOR UPDATE = ExclLock + HEAP_KEYS_UPDATED.
- new `storage.PageSetHeapTupleLockKeysUpdated(p,slot,on)`: set/clear the
  infomask2 KEYS_UPDATED bit on a lock-only single-holder stamp (both stamp sites).
- `lockMemberStatus` (encode) / `lockOnlyMemberStatus` (decode, now takes
  infomask2) = four-way twins. `tupleLockConflicts(reqStatus, heldInfomask,
  heldInfomask2)` now delegates to verbatim `multixact.StatusesConflict`.
- Tests: TestLockMemberStatusFourWay, TestLockOnlyMemberStatusFourWay, rewritten
  TestTupleLockConflicts (full 4×4 + no-holder), storage
  TestPageSetHeapTupleLockKeysUpdated. Added deferred anchor
  TestPort_IsolationLockUpdateTraversal (t.Skip until it matches).
- Design 0118-0002 "slice 4" section + status line + resume #3 split into
  ✅member-status / ⛔savepoint-subxact / ⛔update-chain-propagation. README index
  status + body slice-4 paragraph. Ledger row appended.

GATES this loop (all PASS): go build ./...; go vet ./internal/executor;
unit tests executor+storage+planner+multixact+analyzer+parser; -race subset
(lockrows/multixact/storage lock tests); 7 dedicated row-lock isolation specs
(nowait, nowait-3, skip-locked, update-locked-tuple, lock-committed-update,
lock-committed-keyupdate) all PASS — NO regression. gofmt: fixed my own 2
double-blank-lines; rest is pre-existing go1.25/1.26 skew (do NOT gofmt -w).
tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s); pgbench pre-commit hook is the
live guard. Stage ONLY code/doc/.ralph; do NOT add stray `postgres`,
weekly_loc.*, requirements.txt.

>>> KEY MEASUREMENT: ran lock-update-traversal BEFORE+AFTER. The member status was
    a real bug but NOT this spec's only blocker. After slice 4, the SOLE remaining
    blocker is **update-chain lock propagation**: s1 `SELECT ... FOR KEY SHARE` on
    a row updated in-flight by s2 forms the multi on the version s1 sees but does
    NOT propagate the lock forward to the successor (1,2); so s2d1 DELETE / s2d2
    key-UPDATE of the live version do not wait (expected: <waiting>). ALSO the
    plain UPDATE/DELETE write path does not honour a lock-only holder at all.

>>> NEXT STEP: land **update-chain lock propagation** (the next resume-#3 half).
    Two coupled pieces: (1) in stampLockInner/stampMultiUpdaterLock, after forming
    the multi on the locked version, traverse t_ctid forward (heap_lock_updated_
    tuple analog) and lock each successor version too; (2) make the plain
    UPDATE/DELETE write path (operators_storage.go update/delete) wait on a
    lock-only row-lock holder from another active txn (resolve via
    activeLockHolders/multixact, WaitForXID, then re-evaluate) — currently absent.
    Together they unblock lock-update-traversal / lock-update-delete; add
    propagate-lock-delete after savepoint/subxact members. Use
    TestPort_IsolationLockUpdateTraversal as the proof spec (3 perms, no savepoint).

GOTCHAS: MultiXactId and TransactionID share uint32; HEAP_XMAX_IS_MULTI is the
ONLY disambiguator. FOR UPDATE vs FOR NO KEY UPDATE as a *lock* differ ONLY by
HEAP_KEYS_UPDATED (infomask2) — both stamp ExclLock. KEYS_UPDATED readers are
confined to lock/multixact paths (NOT mvcc visibility), so the FOR UPDATE
lock-only stamp is contained. Sibling-path rule: lockMemberStatus (encode) ↔
lockOnlyMemberStatus (decode) must agree — both four-way now.
