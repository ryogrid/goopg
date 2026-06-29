(idle — nothing in flight)

Last loop (#23): M0119-0004 **deferred EXCLUDE constraint checking** — LANDED.
The last of the four deferrable constraint kinds. An `EXCLUDE … DEFERRABLE
INITIALLY DEFERRED` (or SET CONSTRAINTS … DEFERRED) now tolerates a transient
conflict mid-txn and enforces at COMMIT (23P01). Direct mirror of deferred-unique
(loop #20). Design `0119-0004-deferred-exclusion`.

- session.go: `deferredExclChecks []DeferredExclusionCheck{TableName,IndexName,
  ExclusionOp,Key,BoxStr,Detail}`; `Add/Take/TakeMatching`; `ExclusionConstraintDeferred`.
- deferred_exclusion.go (new): `excludeCheckDeferred`, `queueDeferredExclusionCheck`
  (+ `exclusionBoxValue`), `RunDeferredExclusionChecks`/`runAllDeferredExclusionChecks`,
  `recheckDeferredExclusionEq` (btree ≥2), `recheckDeferredExclusionOverlap` (box ≥2).
- operators_storage.go: `checkExclusionConstraintsForInsert` queue+continue when deferred.
- operators_tx.go: execCommit drain + SET CONSTRAINTS IMMEDIATE drain (after FK+UNIQUE).
- server/dispatch.go: simple-query COMMIT drain (after FK+UNIQUE, shared rollback block).
- Tests: `internal/testport/deferred_exclusion_e2e_test.go`
  (`TestPort_InitiallyDeferredExclusionCommit` + `TestPort_SetConstraintsExclusionDeferral`)
  PASS; full executor + `-race` + prior deferred e2e PASS. Oracle-grounded vs PG 18.3.

NEXT loop — remaining open under M0119-0004:
- pg_dump 002–010 catalog-view parity battery (DU-002, slice-by-slice).
- extended-protocol commit-time deferral — architecturally entangled (extended
  protocol is auto-commit-per-statement; BEGIN/COMMIT are ignored tags).
Or other M0119: M0119-0002 (CLOG store swap Part B, highest blast radius,
dedicated full-gate session) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).
