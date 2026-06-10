# Working Set (carried from loop 2, 2026-06-11)

## Completed this loop

**M0100-0011 — EvalPlanQualTrigger: EPQ trigger output parity** — DONE
- `TestPort_IsolationEvalPlanQualTrigger` SKIP → PASS
- commit 54e738c6
- PASS count: 18 → 19

## Current PASS / SKIP

PASS (19): ReadWriteUnique, LockCommittedUpdate, LockCommittedKeyupdate,
  InsertConflictDoUpdate{,2,3,4}, InsertConflictDoNothing, FkSnapshot,
  PartitionKeyUpdate{1,2,3,4}, MergeDelete, MergeInsertUpdate, MergeMatchRecheck,
  MergeJoin, DropIndexConcurrently1, EvalPlanQualTrigger

SKIP (3): EvalPlanQual, InsertConflictSpecconflict, MergeUpdate

## Next task (topmost unchecked in fix_plan.md)

**M0100-0010 — EvalPlanQual: EPQ recheck NOTICE parity**
- `TestPort_IsolationEvalPlanQual` SKIP — first divergence at L411: NOTICE
  content differs (`lock_id: text checking = text checking: t` vs
  `upid: text savings = text checking: f`). Output: ~1401 lines vs 1494 expected.
- Root cause: EPQ re-evaluation path in UPDATE/DELETE produces different
  comparison results from PostgreSQL; `noisy_oper` side effects diverge.
- Required: trace EPQ code paths in goopg vs PG's ExecUpdate/ExecDelete loops;
  align NOTICE output ordering and content. Write design doc first.

## Files of interest

- internal/executor/operators_storage.go — contains Phase 1 inline EPQ (M0100-0011)
  and Phase 2 EPQ loops (M0100-0004); the EPQ chain-following logic
  (`epqFollowHOT`, `epqFollowChain`) is the target for M0100-0010 analysis
- internal/testport/isolation_port_test.go — TestPort_IsolationEvalPlanQual (L142)

## Gates run

- go build: PASS
- modified-package unit tests: PASS
- isolation test suite: 19 PASS / 3 SKIP (no regressions)
- TPC-H spotcheck: SKIPPED (no data dir)
- Pre-commit suite: N/A (new commit 54e738c6 landed)
