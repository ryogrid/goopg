(idle — nothing in flight)

Last loop (#20): M0119-0004 **deferred UNIQUE/PK constraint checking** — LANDED.
A `UNIQUE`/`PRIMARY KEY` declared `DEFERRABLE INITIALLY DEFERRED` (or deferred via
`SET CONSTRAINTS … DEFERRED`) now queues its uniqueness check to COMMIT instead
of raising immediately. Mirrors the deferred-FK structure one-for-one.
Design `0119-0004-deferred-unique`.

- New file `internal/executor/deferred_unique.go`:
  - `uniqueCheckDeferred(ctx, idx)` — analogue of `fkCheckDeferred`; short-circuits
    on `!idx.Deferrable` (zero blast radius — pgbench/TPC-H PK not deferrable).
  - `RunDeferredUniqueChecks` / `runAllDeferredUniqueChecks` — drain at COMMIT under
    `mvcc.Manager.FreshSnapshot()`.
  - `recheckDeferredUniqueKey` — `RangeScan(key,key)` the backing btree, count
    DISTINCT live visible heap tuples (dedup ItemPointer, `isLiveForUniqueCheck`);
    **≥2 live = 23505** (candidate row is itself one; deferred predicate vs
    immediate "any other live").
- `session.go`: `deferredUniqChecks []DeferredUniqueCheck{TableName,IndexName,Key,Detail}`
  (reset in EndExplicitTransaction) + Add/Take/TakeMatching (dedup on IndexName+Key);
  resolver extracted to `constraintDeferredByName` shared by FK + new `UniqueConstraintDeferred`.
- Enqueue at `checkUniqueIndexes{ForInsert,ForUpdate}` (operators_storage.go): queue
  + `continue` when deferred. Commit chokepoints: `execCommit` (operators_tx.go) after
  the FK block; simple-query `dispatch.go` TxCommit shares ONE rollback block with FK
  (`deferErr := RunDeferredFKChecks; if nil = RunDeferredUniqueChecks`; ExecError Code
  drives 23503 vs 23505). `setConstraintsOp … IMMEDIATE` drains matching unique subset.
- Catalog already carried `Index.Deferrable`/`InitiallyDeferred` — no parser/catalog change.
- Tests: `TestPort_InitiallyDeferredUniqueCommit` + `TestPort_SetConstraintsUniqueDeferral`
  (testport) PASS; full executor + `-race` executor/mvcc + FK-deferral e2e + IsolationFk PASS.

NEXT loop — pick topmost actionable M0119:
- M0119-0004 still open: pg_dump 002–010 catalog-view parity battery; deferred EXCLUDE
  (parallel deferredExclChecks queue) + NND-INITIALLY-DEFERRED (key off row+NULL pattern,
  not the nil btree key); extended-protocol commit-time deferral.
- Or M0119-0005 (pg_waldump server tier) / M0119-0006 (pg_amcheck server tier) /
  M0119-0002 (CLOG store swap Part B — highest blast radius, needs dedicated full-gate session).
