Task: M0118-0003 multixact — LANDED the two REMAINING wait-on-deleter consumers +
the stampAtPtr recheck. The producer gate ([[pattern_sibling_paths_must_agree]]) is
now SATISFIED — the updater-bearing producer can land next.

DONE this loop (#49), committed:
- Two shared package-level resolvers in operators_storage.go (next to
  isConcurrentlyUpdated; file already imports multixact/mvcc so no new import in
  operators_upsert.go):
  - `multixactUpdaterXID(mxs, xmax)` → updater member xid or Invalid (nil store /
    unknown multi / lockers-only).
  - `multixactFirstActiveMember(mxs, txnMgr, self, xmax)` → first active non-self
    member xid or Invalid (wait on ONE live holder; re-probe drains the rest).
- `scanRelForFKMatch` (operators_fk.go): updater-bearing multi → resolve effXmax,
  record the real updater xid in fkPendingRef (WaitForXID/HasAborted/epqChain see a
  real xid); all-locker/unresolvable → clean match (lockers don't delete the parent).
- `findInProgressConflictKey` (operators_upsert.go): Case 2 resolves updater for a
  non-lock-only multi; Case 3 now handles a lock-only MULTI via
  multixactFirstActiveMember (already reachable today via the live lock-only producer).
- `stampAtPtr` (operators_lockrows.go): refactored the "another real updater arrived"
  recheck into `anotherRealUpdaterArrived(h)` which resolves the updater member for an
  updater-bearing multi (byte-identical for all currently-producible states).
- Tests: TestMultixactUpdaterXIDHelper + TestMultixactFirstActiveMemberHelper
  (concurrent_update_xmax_test.go; latter uses AssignXID to materialise active XIDs —
  beginTxn assigns XID lazily so t.XID==0).
- Design 0118-0002: new §"slice 2 (continued, 4)" + flipped "Producer gate: NOW
  SATISFIED"; resume-point list ⛔→✅ for the 3 consumers; README index status+body.

GATES this loop: go build ./... OK; go vet ./internal/executor OK; full
`go test ./internal/executor` PASS; `-race` FK/Upsert/Conflict/Concurrent/Multixact/
LockRows subset PASS; 9 dedicated row-lock isolation specs PASS (LockCommittedUpdate/
Keyupdate, MergeUpdate, MergeInsertUpdate, PredicateLockHotTuple, SkipLocked, Nowait,
Nowait3, UpdateLockedTuple). gofmt: 3 files flagged but ALL pre-existing go1.25/1.26
skew (verified via git stash baseline) — do NOT gofmt -w. ralph-state-guard: run before
status. Stage ONLY code/doc/.ralph files; do NOT git add stray `postgres`, weekly_loc.*,
requirements.txt.

>>> NEXT STEP (the PRODUCER — now unblocked): stampLockInner branch (a) in
    operators_lockrows.go (the non-key-update + FOR KEY SHARE skip path). Build
    {updater@NoKeyUpdate (the in-progress xmax updater), locker@ForShare/ForKeyShare},
    CreateFromMembers (keep in-progress holders + committed updater, drop dead lockers/
    aborted updater per MultiXactIdExpand semantics), HintBits (NOT lock-only),
    PageSetHeapTupleXmaxMulti. Thread the REAL Store through vacuum/analyze stats scans
    (currently pass nil — see TupleVisible/TupleVisibleSubxact callers). HOT-PATH
    WARNING: updater-bearing multis are NOT visibility-transparent — re-run
    executor/storage/mvcc/btree + FULL isolation row-lock suite after the producer.

GOTCHAS: MultiXactId and TransactionID share the uint32 space; HEAP_XMAX_IS_MULTI is
the ONLY disambiguator — never feed a raw multi to IsXIDActive/WaitForXID. The
lock-only producer is ALREADY live (cbf5bf75), so lock-only multis exist NOW (Case 3
fix was a real latent bug, not just future-proofing). multixact path only fires with
≥2 concurrent row-lock holders (never pgbench/TPC-H); TPS blast radius nil. No producer
emits non-lock-only multis yet, so the updater-bearing branches are behavior-identical
for existing tuples. tpch-spotcheck INFRA-BLOCKED (startup SLRU backfill >60s); pgbench
pre-commit smoke (hook) is the live commit guard.
