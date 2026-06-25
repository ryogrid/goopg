# 0118-0112 — SSI dangerous-structure check at PREPARE TRANSACTION — `prepared-transactions` PROMOTED

Status: accepted
Milestone: M0118-0009 (Upstream isolation spec suite pass-through)
Type: **Promotion** (`prepared-transactions.spec`, all 1500 permutations byte-for-byte vs PG 18.3)

## Problem

`prepared-transactions.spec` drives three overlapping SERIALIZABLE transactions
through a `s1 --rw--> s2 --rw--> s3` dangerous structure (s3 commits first) under
two-phase commit, across **1500 permutations**. Exactly one of the three must be
aborted in every permutation. The same-backend 2PC enabler (design 0118-0110)
made the statements parse and execute, but goopg deferred the entire SSI
dangerous-structure decision to `COMMIT PREPARED`, so it diverged from PG on
**which transaction aborts and where the 40001 surfaces**.

Two concrete divergences:

1. **The abort surfaces too late and on the wrong victim.** Upstream runs
   `PreCommit_CheckForSerializationFailure` at **PREPARE TRANSACTION** time (not
   only at commit) and the read/write conflict hooks
   (`OnConflict_CheckForSerializationFailure`) treat a **PREPARED** peer as
   already committed-first. goopg's hooks only recognised a *committed*
   (`FinishedAt != Invalid`) peer, so e.g. in
   `r1 w2 w3 p1 p3 r2 p2 c3 c1 c2` the read `r2` (which forms `s2 --rw--> s3`
   while `s3` is already prepared) did **not** doom `s2` mid-read as PG does;
   instead the structure was only caught at `c3`, dooming `s3`.

2. **PREPARE on an aborted block errored.** After the (correct) 40001 at `r2`,
   the next step `p2 = PREPARE TRANSACTION 's2'` runs on the now-aborted `s2`.
   PostgreSQL silently rolls it back (no error, no result tag —
   `PrepareTransactionBlock` → `EndTransactionBlock` returns `result=false` for
   `TBLOCK_ABORT`). goopg's failed-transaction guard rejected it with `25P02`.

## What upstream does (predicate.c / xact.c)

* `OnConflict_CheckForSerializationFailure` (predicate.c:4536) — Cases 2 and 3
  gate on `SxactIsPrepared(t2)` / `SxactIsPrepared(writer)`. `SXACT_FLAG_PREPARED`
  is set at pre-commit (so a committed xact is *also* prepared); a
  prepared-but-not-committed peer compares against its `prepareSeqNo`. When the
  structure must abort and the writer is prepared (so it cannot be killed), the
  **reader** is cancelled instead ("Canceled on conflict out to pivot, during
  read").
* `PreCommit_CheckForSerializationFailure` (predicate.c:4703) — also runs at
  PREPARE; if the pivot (`nearConflict->sxactOut`) is already prepared it cannot
  be doomed, so the preparer commits suicide ("Canceled on commit attempt with
  conflict in from prepared pivot"). Sets `SXACT_FLAG_PREPARED` on success.
* `PrepareTransactionBlock` (xact.c:3992) — PREPARE on `TBLOCK_ABORT` rolls back
  silently.

## What this change implements

**SSI substrate (`internal/mvcc`):**

* `SerializableXact.Prepared` + `PrepareSeqNo` — mirror `SXACT_FLAG_PREPARED` and
  `prepareSeqNo`. Set only on the PREPARE path; a normal COMMIT finishes
  immediately and is gated out by `FinishedAt`, so neither field is touched for
  the 100+ already-passing non-2PC specs (behaviour byte-unchanged).
* `Manager.PrepareCheckForSerializationFailure` — runs the existing pre-commit
  dangerous-structure scan and, on success, stamps `Prepared`/`PrepareSeqNo`.
* `preCommitCheckForSerializationFailureLocked` — when a dangerous structure is
  found and the pivot is `Prepared`, the preparer/committer commits suicide
  (returns 40001) instead of dooming the pivot (predicate.c:4756).
* `onConflictCheckLocked` — Cases 2 and 3 gain an additive
  prepared-but-not-committed branch (compared against `PrepareSeqNo`), and victim
  selection dooms the **reader** when the writer is committed **or** prepared.
  All new branches are guarded by `Prepared`, which is never set outside 2PC, so
  the committed-only path is unchanged.

**Server (`internal/server`):**

* `execPrepareTransaction` runs `PrepareCheckForSerializationFailure` for an
  explicit SERIALIZABLE txn; on failure it aborts the txn (canonical rollback)
  and reports 40001 — so the later `COMMIT PREPARED` reports `does not exist`.
* PREPARE on a failed/aborted block now silently rolls back via the canonical
  `RollbackStmt` path (no `25P02`), matching `PrepareTransactionBlock`.
* The dispatch failed-transaction guard lets two-phase statements through
  (`isTwoPhaseStmt`) so they reach `execTwoPhaseStmt` for the above handling.

## Blast radius

Nil for non-2PC workloads: `Prepared`/`PrepareSeqNo` are only set by
`PREPARE TRANSACTION`; every new conflict-check branch and the prepared-pivot
suicide path is guarded by `Prepared`. With no prepared xact present the SSI
code reduces token-for-token to the prior committed-only logic. No `port` spec
other than the prepared-transactions family issues these statements.

## Gates

* `TestPort_IsolationPreparedTransactions` strict — all 1500 permutations
  byte-identical to PG 18.3.
* Non-regression: `TestPort_TwoPhaseCommitSameBackend`,
  `TestPort_IsolationPreparedTransactionsCIC`,
  `TestPort_IsolationSimpleWriteSkew`, `TestPort_IsolationReceiptReport` PASS.
* `go test -race ./internal/mvcc/...` green; `go build`/`vet` clean.
* CSV `D-002` rationale updated + coverage/inventory md regenerated.
* pgbench smoke = pre-commit hook.
