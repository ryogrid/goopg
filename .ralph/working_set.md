Task: M0118-0003 multixact — LANDED the MISSED highest-blast-radius read consumer
`mvcc.TupleVisibleSubxact` (the subtransaction-aware twin of `TupleVisible` used by
the MAIN seqscan + FK + MERGE + DDL rewrites). A producer-gate audit proved the
prior "gate satisfied" claim WRONG: the gate is still NOT satisfied — two more
wait-on-deleter consumers remain before the producer.

DONE this loop (#48), committed:
- `mvcc.TupleVisibleSubxact` (internal/mvcc/subxact_visibility.go) now takes
  `mxs *multixact.Store` (new last param) and resolves an updater-bearing
  (`IsHeapTupleXmaxMulti && !IsHeapTupleLockOnly`) MultiXactId xmax to its updater
  via `Store.Members`+`GetUpdateXid` (effXmax) before the self/hint-bit/snapshot
  checks — structurally identical to `TupleVisible`'s multi arm (all-locker→visible,
  unresolvable→invisible; single/lock-only byte-identical; self-xmin branch keeps
  raw h.Xmax to match TupleVisible). Added `multixact` import.
- Threaded `ctx.MultiXact`/`o.ctx.MultiXact` through ALL 13 call sites:
  operators_fk.go ×5, operators_ddl.go ×3, operators_storage.go ×2 (incl.
  seqScanOp.Next L837), operators_merge.go, applyworker.go ×2.
- Test TestTupleVisibleSubxactMultiXact (mirrors TestTupleVisibleMultiXact) +
  3 existing call sites in subxact_visibility_test.go updated (mxs arg).
- Design 0118-0002 §"slice 2 continued, 3" + corrected producer-gate section;
  README index status+summary corrected; ledger row appended.

GATES this loop: `go build ./...` OK; `go vet ./internal/mvcc ./internal/executor`
OK (persistent LSP WrongArgCount diagnostics are STALE cross-pkg cache — real build
& vet pass clean); `-race ./internal/mvcc ./internal/multixact ./internal/storage`
PASS; executor Visib/Scan/FK/Merge/DropColumn/AlterColumn/Truncate/Lock/MultiXact/
Subxact/Apply subset PASS; 9 dedicated row-lock isolation specs PASS
(LockCommittedUpdate/Keyupdate, MergeUpdate, MergeInsertUpdate, PredicateLockHotTuple,
SkipLocked, Nowait, Nowait3, UpdateLockedTuple). ralph-state-guard: run before status.
Stage ONLY code/doc/.ralph files; do NOT git add stray `postgres`, weekly_loc.*,
requirements.txt.

>>> NEXT STEP (producer gate, step (i) — the two REMAINING wait-on-deleter consumers,
    found by the audit; MUST precede the producer per [[pattern_sibling_paths_must_agree]]):
  Make `scanRelForFKMatch` (operators_fk.go ~L712) and `findInProgressConflictKey`
  (operators_upsert.go Case 2 ~L798) multixact-aware: when
  `IsHeapTupleXmaxMulti && !IsHeapTupleLockOnly`, resolve `GetUpdateXid(Store.Members
  (MultiXactId(xmax)))` (and/or the active-member subset like `activeLockHolders`)
  BEFORE `IsXIDActive(xmax)`/`WaitForXID` — never pass the raw MultiXactId to a
  single-TransactionID API. all-locker→clean match; unresolvable→conservative wait.
  Both have `ctx`/`o.ctx` for the store. ALSO audit `stampAtPtr` (operators_lockrows.go
  ~L872) "another real updater arrived" recheck.
  THEN: the PRODUCER (stampLockInner branch (a), the non-key-update+FOR KEY SHARE skip
  path) — build {updater@NoKeyUpdate, locker@ForShare}, CreateFromMembers (keep
  in-progress holders + committed updater, drop dead lockers/aborted updater per
  MultiXactIdExpand), HintBits (NOT lock-only), PageSetHeapTupleXmaxMulti; thread the
  real Store through vacuum/analyze (currently nil). HOT-PATH WARNING: updater-bearing
  multis are NOT visibility-transparent — re-run executor/storage/mvcc/btree + FULL
  isolation row-lock suite after the producer.

GOTCHAS: audit lesson — enumerate xmax consumers by MECHANICALLY sweeping every
`IsHeapTupleLockOnly` site (the hand list missed TupleVisibleSubxact + the 2 FK/upsert
consumers). multixact path only fires with ≥2 concurrent row-lock holders (never
pgbench/TPC-H), TPS blast radius nil. No producer emits non-lock-only multis yet, so
ALL changes are behavior-identical for existing tuples. tpch-spotcheck INFRA-BLOCKED
(startup SLRU backfill >60s); pgbench pre-commit smoke (hook) is the live commit guard.
