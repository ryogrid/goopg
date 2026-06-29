(idle — nothing in flight)

Last loop (#22): M0119-0004 **deferred UNIQUE with NULLS NOT DISTINCT (NULL-keyed
rows)** — LANDED. Composed the deferred-unique queue (loop #20) with NND
enforcement (loops #14–#17). The `key == nil` NND arm in
`checkUniqueIndexes{ForInsert,ForUpdate}` (operators_storage.go) ran the
immediate heap-scan raise unconditionally; now it checks `uniqueCheckDeferred`
first and queues a NULL-pattern recheck for COMMIT. Design
`0119-0004-deferred-unique-nnd`.

- session.go: `DeferredUniqueCheck.NNDKeyCols []DeferredNNDKeyCol{ColName,Null,Key}`;
  dedup widened via `sameNNDKeyCols`.
- operators_storage.go: lifted `nndKeyCol` to package scope; extracted
  `scanNNDLiveMatches(ctx,tbl,rel,keyCols,stopAt)` + `resolveNNDKeyColsFromRow`
  + `nndTableColumn`; immediate path stopAt=1, deferred stopAt=2. Added the
  `uniqueCheckDeferred → queueDeferredNNDUniqueCheck; continue` branch at both
  enqueue sites.
- deferred_unique.go: `queueDeferredNNDUniqueCheck` + `recheckDeferredNNDUniqueKey`
  + `runAllDeferredUniqueChecks` branches on `c.NNDKeyCols != nil`.
- Tests: `internal/testport/deferred_unique_nnd_e2e_test.go`
  (`TestPort_InitiallyDeferredNNDUniqueCommit` + `TestPort_DeferredNNDMultiColumn`
  + `TestPort_SetConstraintsNNDDeferral`) PASS; full executor + `-race` + prior
  deferred-unique/FK e2e PASS. Oracle-grounded vs local PG 18.3.

NEXT loop — remaining open under M0119-0004:
- deferred EXCLUDE constraints (no deferred-exclusion queue; EXCLUDE backing
  index carries Deferrable/InitiallyDeferred today as dump-fidelity only).
- extended-protocol commit-time deferral — NOTE: goopg's extended protocol
  (`dispatch_extended.go executeExtendedQueryViaExecutor`) is auto-commit-per-
  statement and treats BEGIN/COMMIT as accepted-but-ignored tags (line ~78); a
  real multi-statement explicit txn over extended protocol isn't modelled, so
  this is architecturally entangled — scope carefully before picking.
- pg_dump 002–010 catalog-view parity battery (slice-by-slice via DU-002).
Or other M0119 tasks: M0119-0002 (CLOG store swap Part B, highest blast radius,
dedicated full-gate session) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).
