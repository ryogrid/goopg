Task: M0130-S11.4 slice 3b-2c-ii-B2-c-iii (the probe-key funnel) — DONE,
committed + pushed. Next is the flip itself (B2-c, REINDEX-required).

Landed (no on-disk change, `pgIndexTupleKeys` still false):
- `(*Context).indexProbeKey(idx, []indexProbeKeyPart)` in
  `internal/executor/pgindex_btree.go`. desc==nil ⇒ concatenated
  `encodeBTreeKeyForColumn` (byte-identical to before); desc!=nil ⇒
  `pgIndexTupleKey(desc, cols, vals, storage.ItemPointer{})` — zero TID =
  heapkeyspace minus infinity; a short probe is a 1-natts pivot.
- All TEN scan-side call sites now use it: operators_index.go (lookupKeys,
  lookupKey, lookupRangeBounds ×2), operators_indexonly.go (same four),
  operators_bitmap.go (lookupKey, lookupKeys), operators_storage.go
  (UPDATE-by-index probe).

Why (do not re-derive): concatenation IS the blob key layout, not an
encoder detail. Tuple keys are one FormPGIndexTuple image. Under the tuple
format there is no fallback — a pgIndexTupleKey refusal errors instead of
emitting a blob key. The funnel also checks the caller's columns against
`pgIndexKeyColumns(idx)` (a pivot silently means "first N attributes").

Guard: `internal/executor/pgindex_probekey_test.go` (5 tests) incl. a
SOURCE SCAN over the three scan files pinning `indexProbeKey` as the only
scan-side encoder. Mutation-checked (reverting the bitmap site → reported
by file:line). operators_storage.go is NOT scanned — it still has ~8
legitimate writer-side callers (ledger row).

Next step: M0130-S11.4 slice 3b-2c-ii-B2-c — THE FLIP, writers only now.
`encodeCompositeBTreeKey`/`encodeCompositeBTreeKeyWithExprs`
(operators_ddl.go), `encodeIndexKeyFromCols` + uniqueness paths
(operators_storage.go ~7184/7264/7347/7439/7524), `encodeArbiterKey` +
expression paths (operators_upsert.go:1527), the SSI predicate encoder
(ssi.go) → `pgIndexTupleKeyFromRow` with the row's REAL heap TID; then
`pgIndexTupleKeys = true`. Scan side needs ZERO further edits. Gates:
tpch-spotcheck + **TPC-DS SF0.5 gate (mandatory)**, re-pin after REINDEX.
Re-read the fix_plan banner first (M-NIGHTLY filing unconditional; the six
`AI-20260810-011258-*` items are filed and left unchecked per the banner).

Gates run: `go build ./...` + `go vet ./internal/executor` clean;
`go test` PASS for ./internal/executor ./internal/access/btree;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35); commit-hook
pgbench smoke PASS. NOT run: TPC-DS SF0.5 gate (blob path byte-identical).

In-flight: none.
