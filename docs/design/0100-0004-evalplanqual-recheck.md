# 0100-0004 — EvalPlanQual Concurrent UPDATE Recheck (ReadCommitted)

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0100-0004
**Closes:** M0096-0013 (one of the documented remaining blockers)
**Depends on:** M0100-0003 (the row-level wait path; EPQ is the recheck that runs after the wait)

## Problem

In READ COMMITTED, when an UPDATE/DELETE waits on a concurrent UPDATE
of the same row and the holder commits, the waiter must re-evaluate
the UPDATE's qualification against the **latest** row version, not the
original one its snapshot saw. This is PostgreSQL's `EvalPlanQual`
(EPQ) machinery.

Concretely (from `eval-plan-qual.spec`):
- Session 1: `UPDATE accounts SET balance = balance - 100 WHERE owner = 'alice' AND balance >= 100`
- Session 2 (concurrent): `UPDATE accounts SET balance = balance + 50 WHERE owner = 'alice'`
- Session 1 commits first; session 2 wakes up.
- The row's `balance` is now lower than the snapshot value. Session 2's
  qual (`owner = 'alice'`) still holds, so the update applies — but the
  new row's `balance` is used for `balance + 50`.

Without EPQ recheck, session 2 either:
- Applies its update to a stale snapshot value (silent lost update), or
- Skips the row when the qual would still match the new version.

Both break `eval-plan-qual`, `eval-plan-qual-trigger`, and
`merge-match-recheck` test outputs.

## Relationship to M0098-0004 and M0099-0004

M0098-0004 landed an EPQ-shape retry loop (`epqWait` + `epqRecheckVisible`
at `internal/executor/operators_storage.go:78-95`) and explicitly **does
not** follow HOT/update chains: on a committed concurrent UPDATE the
row is skipped. The design doc (0098-0004) records this as the v0
compromise. M0099-0004 added WFG cycle detection for early deadlock
identification on top.

M0100-0004 is the chain-following completion that 0098-0004 deferred.
It depends on M0100-0003 (blocking re-introduced) because chain-following
is only meaningful after waiting for the concurrent transaction to
commit and stamp the new version.

## Solution

In the UPDATE/DELETE re-fetch path completed by M0100-0003:

1. After `WaitForXID` returns, follow the t_ctid update chain to the
   latest visible version of the row (using `HeapTupleSatisfiesUpdate`
   semantics — see upstream `heap_update`).
2. Re-evaluate the UPDATE's qual (`WHERE` clause) against the new tuple
   via the executor's existing expression evaluation
   (`evalExprSlot` / `evalQual`).
3. If the qual no longer matches: skip the row (this is the
   `HeapTupleUpdated`-and-qual-fails case).
4. If the qual still matches: re-bind the SET expressions against the
   new row (`balance` reads the new value) and apply the update on the
   new tuple's ctid.
5. For MERGE WHEN MATCHED, follow the same recheck. For DELETE, the
   qual recheck is the only difference; the action is unconditional
   once the qual holds.

### Scope

- This is **READ COMMITTED** EPQ only. Repeatable Read raises
  `40001 serialization_failure` if the row was updated after our
  snapshot — that path is a one-liner in the same place (raise instead
  of EPQ-recheck) and is included here to keep behaviour aligned.
- SSI / SERIALIZABLE-level dependency tracking is **not** in scope
  (M0100 does not promise SSI).

## Files touched

- `internal/executor/operators_storage.go` — extend the re-fetch loop
  from M0100-0003 with qual + SET re-evaluation against the new row.
- `internal/executor/operators_storage_test.go` — unit test for the
  EPQ path (concurrent UPDATE → both succeed with the right arithmetic).
- `internal/mvcc/visibility.go` — small helper to chase t_ctid forward
  to the latest committed version, if not already present.

## Reference (upstream)

- `postgres/src/backend/executor/execMain.c` —
  `EvalPlanQual`, `EvalPlanQualFetchRowMark`, `EvalPlanQualSlot`.
- `postgres/src/backend/access/heap/heapam.c` —
  `heap_update`'s `HeapTupleUpdated` branch followed by re-fetch via
  `t_ctid`.

## Verification

- `TestPort_IsolationEvalPlanQual`, `TestPort_IsolationEvalPlanQualTrigger`,
  `TestPort_IsolationMergeMatchRecheck` reach `pass`.
- `go test -race ./internal/executor/...` clean.

## Risks

- **Update chain length.** A row updated many times by long-running
  transactions can produce a deep `t_ctid` chain. EPQ walks it once
  per recheck; bound the walk to avoid hangs on pathological chains
  (PG bounds via the snapshot's xmax horizon — adopt the same bound).
- **RR/SSI vs RC branching.** The same code site handles both; gate on
  `tx.Isolation`. RR raises `40001`; RC re-evaluates. Confirm both
  branches by a dedicated test for each.
- **MERGE source-row binding.** For MERGE WHEN MATCHED, the recheck must
  re-bind both the target and source row in the WHEN MATCHED action's
  expression tree. M0096-0010's `mergeOp` already evaluates against a
  merged target+source schema; the recheck reuses that schema, only
  re-fetching the target side.
