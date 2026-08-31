# 0104-0008 — SSI Oracle-Test Promotion & M0104 Closeout

Status: COMPLETE (2026-05-14)

## Goal

Close out milestone M0104 (Serializable Snapshot Isolation) by promoting
applicable deferred isolation tests for SERIALIZABLE/SSI in
`internal/testport` and evidencing the milestone Definition of Done (DoD)
items #2 ("At least one known serializable anomaly pattern is
deterministically rejected with SQLSTATE `40001`") and #4 ("Applicable
deferred isolation tests for SERIALIZABLE/SSI are promoted and passing
in `internal/testport`").

## Why a focused Go test instead of promoting upstream spec files

Upstream `postgres/src/test/isolation/specs/simple-write-skew.spec` and
its SSI-adjacent neighbours rely on `isolationtester.c`'s implicit
auto-permutation behaviour: when a `.spec` declares sessions and steps
but no explicit `permutation` directives, isolationtester generates the
cartesian product of legal orderings and runs each. Goopg's
`framework.IsolationRunner` deliberately does NOT auto-generate
permutations — it only executes spec-declared `permutation` lines (see
`ParseIsolationSpec` in `internal/testport/framework/isolation.go`).
Lifting auto-permutation into our runner is a separate
infrastructure-scale change (estimated half-milestone of work covering
schedule generation, blocking-detection wait semantics, and per-
permutation setup/teardown) and would mask the SSI behaviour we want to
gate on a runner mechanism whose own correctness is not yet a M0104
deliverable. Therefore M0104-0008 ships a focused multi-session
SQL-driven Go test
(`internal/testport/ssi_write_skew_test.go`) that directly enacts the
canonical permutations from `simple-write-skew.spec` over a live goopg
cluster — the same end-to-end shape that an auto-permutation upstream
run would exercise, but expressed as deterministic Go test cases against
the goopg wire protocol.

## What landed

### `internal/testport/ssi_write_skew_test.go`

Four pass-required Go tests that gate the M0104 DoD:

| Test | Permutation | Expected outcome |
| --- | --- | --- |
| `TestPort_SSI_WriteSkew_NoOverlap_BothCommit` | `rwx1 c1 rwx2 c2` | Both commit (no overlap → no rw-cycle) |
| `TestPort_SSI_WriteSkew_Overlap_SecondCommitterAborts` | `rwx1 rwx2 c1 c2` | `c2` returns SQLSTATE 40001 |
| `TestPort_SSI_WriteSkew_Overlap_FirstCommitterAborts` | `rwx1 rwx2 c2 c1` | `c1` returns SQLSTATE 40001 |
| `TestPort_SSI_WriteSkew_RC_NoSerializationFailure` | same overlap shape under READ COMMITTED / REPEATABLE READ | No serialization failure (control: hooks must short-circuit outside SERIALIZABLE) |

Each test spins up an isolated goopg cluster via the
`internal/testutil/cluster` helper, opens two `*sql.Conn` sessions on
the lib/pq driver, and drives the simple-write-skew scenario
(`UPDATE t SET t='apple' WHERE t='pear'` vs `UPDATE t SET t='pear' WHERE
t='apple'` over `(5,'apple'),(7,'pear'),(11,'banana')`). The 40001
assertion checks both the SQLSTATE and the upstream wording prefix
(`"could not serialize access due to read/write dependencies among
transactions"`) so any wording drift between the executor / dispatch
paths and the upstream error string is caught.

### Three load-bearing gap fixes uncovered by the new tests

The new SQL-driven tests exposed three gaps in the M0104-0007 wiring
that the prior executor unit tests (`internal/executor/ssi_test.go`)
could not reach because they construct the conflict graph by hand
through `mvcc.Manager.Begin` + `MarkDoomedForTest`, not through SQL
flowing through the wire-protocol dispatch:

1. **`BEGIN ISOLATION LEVEL <level>` was silently ignored on the simple-
   query path.** The simple-query dispatcher (`internal/server/dispatch.go`
   `executeOneSimpleStmt`) handles `*planner.Transaction{Verb: TxBegin}`
   in an early-return branch that calls `connTx.Begin(ctx.Tx)` directly
   on the auto-commit RC tx allocated at dispatch entry. It never
   invoked `transactionOp.execBegin`, where the M0096-0002 isolation-
   level parsing lives. The result: `BEGIN ISOLATION LEVEL SERIALIZABLE`
   became a no-op for the actual transaction isolation, leaving every
   subsequent statement running under RC and the SSI hooks
   short-circuiting via `ssiActive`. Fix: in the dispatch's `TxBegin`
   branch, if `txNode.IsolationLevel != ""` and the parsed level differs
   from the placeholder tx's level, rollback the placeholder and
   `Begin(parsedLvl)` a fresh tx + snapshot before promoting via
   `connTx.Begin`. The mvcc bookkeeping (`registerSerializableLocked`)
   keys off `Begin(IsolationSerializable)` so the SerializableXact
   registration follows naturally.

2. **`COMMIT` bypassed `ssiPreCommitCheck` on the simple-query path.**
   The same dispatcher handles `*planner.Transaction{Verb: TxCommit}`
   by calling `TxnMgr.Commit(connTx.Tx())` directly, never invoking
   `transactionOp.execCommit` where M0104-0007 wired the pre-commit
   dangerous-structure walk. Fix: gate the dispatch-side
   `TxnMgr.Commit` on `PreCommitCheckForSerializationFailure(handle)`
   for SERIALIZABLE explicit transactions; on detection, rollback +
   `connTx.End` + return a wire-protocol 40001 error with the upstream
   wording, so SQL clients see SQLSTATE 40001 just like the executor
   path does for COMMIT-inside-multi-statement-batch.

3. **`scanMatching` (UPDATE / DELETE inner scan) didn't fire the SSI
   read hook.** M0104-0007 added `ssiRecordTupleRead` calls in
   `seqScanOp.Next` and `indexScanOp.Next`, but the
   `updateOp` / `deleteOp` paths use a separate `scanMatching` loop
   (`internal/executor/operators_storage.go`) that walks heap pages
   directly and never went through the seq/index op machinery. Result:
   the UPDATE statement's predicate scan installed zero SIREAD locks,
   so the peer UPDATE's write-side `CheckForSerializableConflictIn`
   found no covering reader and the rw-conflict graph stayed empty.
   Fix: invoke `ssiRecordTupleRead(ctx, rel, blk, slot, h.Xmin, h.Xmax)`
   immediately after the visibility check inside `scanMatching`,
   mirroring the seqScanOp.Next site. Predicate filtering still happens
   after the hook so the SIREAD covers every tuple the UPDATE/DELETE
   ever observed, not just the matched subset — matching upstream
   `predicate.c` semantics where the lock follows the scanner, not the
   matcher.

4. **`ssiRecordTupleRead` only checked the visible writer's xmin.** The
   original wiring passed only `tuple.Header.Xmin` to
   `CheckForSerializableConflictOut`. That covers the reader-after-write
   shape (reader sees a concurrent writer's NEW version directly) but
   not the write-skew shape, where the reader's MVCC snapshot hides the
   concurrent writer's new version and the OLD version it sees still
   carries the concurrent writer's XID in `tuple.Header.Xmax`. Fix:
   extend the helper signature to take `writerXmax` and invoke
   `CheckForSerializableConflictOut(handle, xmax)` whenever
   `xmax != xmin`. The Manager already filters InvalidXID / Bootstrap /
   Frozen and the self-modify case, so the second check is unconditional
   modulo the equality guard. All three call sites (seqScanOp,
   indexScanOp, scanMatching) pass `tuple.Header.Xmax` alongside
   `tuple.Header.Xmin`.

Together these four fixes close the 2-cycle for write-skew end-to-end:
s1's UPDATE seqScan installs SIREAD on every tuple it observes
(including slot=1 'apple'); s2's UPDATE writes slot=1, which fires
`CheckForSerializableConflictIn` and installs the s1→s2 edge; s2's
UPDATE seqScan reads slot=2 ('pear') whose `xmax = s1.XID` because
s1 has marked it for update, which fires
`CheckForSerializableConflictOut(s2, s1.XID)` and installs the s2→s1
edge; pre-commit on the second committer walks `me.inConflicts`, finds
the 2-cycle through the pivot's `inConflicts`, and the second
committer's `Doomed` flag is set so its `COMMIT` returns SQLSTATE
40001.

## Files touched

- `internal/testport/ssi_write_skew_test.go` (new) — four pass-required
  SSI write-skew tests.
- `internal/executor/ssi.go` — `ssiRecordTupleRead` signature gains
  `writerXmax storage.TransactionID`; helper invokes the conflict-out
  check twice when xmax differs from xmin.
- `internal/executor/operators_storage.go` — `scanMatching` invokes
  `ssiRecordTupleRead(..., h.Xmin, h.Xmax)` immediately after
  visibility; `seqScanOp.Next` passes `tuple.Header.Xmax`.
- `internal/executor/operators_index.go` — `indexScanOp.Next` passes
  `tuple.Header.Xmax`.
- `internal/executor/ssi_test.go` — six existing call sites updated to
  pass `storage.InvalidTransactionID` for the xmax slot (no behaviour
  change to those pinned scenarios — they construct hand-rolled writers
  via `MarkDoomedForTest` rather than going through MVCC tuple
  versioning).
- `internal/server/dispatch.go` — `TxBegin` branch promotes the
  placeholder RC tx to a fresh tx at the requested isolation level when
  `BEGIN ISOLATION LEVEL <level>` is observed; `TxCommit` branch
  invokes `PreCommitCheckForSerializationFailure` and translates a hit
  to SQLSTATE 40001 with the upstream wording.

## Out of scope

- Auto-permutation in `framework.IsolationRunner` (would unblock
  promotion of the upstream simple-write-skew.spec et al. through
  `TestPort_IsolationSuite`).
- `SHOW transaction_isolation` GUC tracking the active transaction's
  isolation level — currently always reads the session-default GUC.
  The new tests observed this drift but do not depend on it (the
  SQLSTATE assertion is the load-bearing signal).
- READ-ONLY DEFERRABLE optimisation, `OnConflict_CheckForSerialization`,
  index-target predicate locks for btree range-boundary phantoms —
  staged for follow-up milestones per the M0104 milestone doc's "Out
  of Scope" section.

## Regression gates

- `go test ./internal/executor/ ./internal/mvcc/ ./internal/server/`
  — all green.
- `go test -run TestPort_SSI_WriteSkew ./internal/testport/` —
  4/4 pass.

## Definition of Done evidence

- DoD #2 (anomaly rejection with 40001): both overlap permutations
  (`Overlap_SecondCommitterAborts` and `Overlap_FirstCommitterAborts`)
  return SQLSTATE 40001 with the upstream wording.
- DoD #4 (deferred-test promotion + passing): four pass-required tests
  in `internal/testport` exercise the M0104-0007 wiring end-to-end via
  the wire protocol. The negative control under READ COMMITTED /
  REPEATABLE READ guards against false-positive regression of the
  short-circuit guards.
- DoD #3 (no lock leakage through commit/abort): the `BothCommit`
  permutation runs sequential SERIALIZABLE transactions through
  COMMIT and no spurious 40001 fires on the second one — the
  bookkeeping cleanup at commit is consistent with the predicate-lock
  retention invariants from M0104-0006.
- DoD #5 (no RC/RR regression): the RC + RR control test passes on the
  same overlap shape, confirming the helpers' `ssiActive` short-circuit
  is intact.
