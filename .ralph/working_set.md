Task: M0130-S11.4 slice 3b-2c-ii-B2-c-iv (the row-key funnels) — DONE,
committed + pushed. Next is the flip itself (B2-c, REINDEX-required).

Landed (no on-disk change, `pgIndexTupleKeys` still false):
- `(*Context).indexEntryKey(idx, cols, row, tid)` and
  `(*Context).indexRowProbeKey(idx, cols, row)` over one `indexRowKey`
  in `internal/executor/pgindex_btree.go`. desc==nil ⇒
  `encodeIndexKeyFromCols` verbatim; desc!=nil ⇒ `pgIndexTupleKey`.
- Projection factored out as `indexRowKeyValues` (operators_storage.go).
- 7 sites routed: entry = maintainUniqueIndexesForInsert +
  upsert maintainNonArbiterIndexesCapture/ForUpdate; probe =
  checkUniqueIndexesForInsert/ForUpdate, checkExclusionConstraintsForInsert,
  queueDeferredExclusionCheck.
- Spec-insert key cache in maintainNonArbiterIndexesForUpdate now bypassed
  when a descriptor exists (a cached key carries the SPEC row's TID).

Why (do not re-derive): `encodeIndexKeyFromCols` served FOUR roles. Entry
key needs the row's real TID; probe key needs the ZERO TID (minus infinity,
else a duplicate scan starts after its own matches); and TWO value
fingerprints — `indexKeyColumnsChanged` (bytes.Equal) and
`ssiRecordHashIndexInsert` (bucket tag) — must stay TID-free forever. Those
two are deliberately LEFT on encodeIndexKeyFromCols with comments.

Guard: `internal/executor/pgindex_rowkey_test.go` (5 tests) incl. a SOURCE
SCAN over operators_upsert.go + deferred_exclusion.go. Mutation-checked
(reverting the deferred-exclusion site → reported by file:line).
operators_storage.go / ssi.go NOT scanned — that is where the two
fingerprint uses live (ledger row).

Next step: M0130-S11.4 slice 3b-2c-ii-B2-c — THE FLIP. What remains:
the BUILD path `encodeCompositeBTreeKey`/`WithExprs` (operators_ddl.go
~10531/10670 — needs the real heap TID threaded into btree.BulkEntry; a
bulk build SORTS and the TID is part of the heapkeyspace sort key), the
expression writers `encodeArbiterKey` (operators_upsert.go:1490) +
`encodeExprIndexKey`, the per-column uniqueness comparisons in
operators_storage.go (~7264/7347/7439/7524), then `pgIndexTupleKeys = true`.
Scan side + row-shaped writers need ZERO further edits. Gates:
tpch-spotcheck + **TPC-DS SF0.5 gate (mandatory)**, re-pin after REINDEX.
Re-read the fix_plan banner first (M-NIGHTLY filing unconditional; the six
`AI-20260810-011258-*` items are already filed and left unchecked).

Gates run: `go build ./...` clean; `go test` PASS for ./internal/executor
./internal/access/btree; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12 rows=2, Q13 rows=35); commit-hook pgbench smoke PASS. NOT run: TPC-DS
SF0.5 gate (blob path byte-identical, gate still off).

In-flight: none.
