Task: M0130-S11.4 slice 3b-2c-ii-B2-c-ii (the upper-bound funnel) — DONE,
committed + pushed. Next is the flip itself (B2-c, REINDEX-required).

Landed (no on-disk change, `pgIndexTupleKeys` still false):
- `(*Context).compositeUpperBound(idx, key)` in
  `internal/executor/pgindex_btree.go`. desc==nil ⇒
  `appendCompositeUpperPadding(key)` (byte-identical to before);
  desc!=nil ⇒ `key` unchanged (the prefix pivot B2-c-i's `compareHigh`
  reads as plus infinity).
- All SIX padding sites now call it: operators_index.go ×2,
  operators_indexonly.go ×2, operators_bitmap.go:lookupKey,
  operators_storage.go (UPDATE-by-index).

Why (do not re-derive): 64 `0xFF` is a BLOB-format repair, not a scan
requirement. Under tuple format 0xFF is a malformed attribute image, and
upstream never invents a maximal key (`_bt_check_compare`). Side effect:
the sites' `len(Index.Columns) > 1` guard is now a cheap skip, not a
correctness condition (a full-attribute bound compares the same under
`compareHigh` and `compare`).

Guard: `internal/executor/pgindex_upperbound_test.go` (4 tests), incl. a
SOURCE SCAN pinning `compositeUpperBound` as the padding helper's only
caller. Mutation-checked: reverting the bitmap site → the scan reports it
by file:line.

Still open (ledger row appended this loop):
- ~20 single-column `encodeBTreeKeyForColumn` probe sites still blob.
- `compositeUpperPaddingLen = 64` heuristic survives for every index the
  resolver refuses.
- The source-scan guard is lexical + package-local (helper is unexported).

Next step: M0130-S11.4 slice 3b-2c-ii-B2-c — THE FLIP.
`encodeCompositeBTreeKey` (operators_ddl.go:10785) / `encodeIndexKeyFromCols`
(operators_storage.go:7148) / `encodeArbiterKey` (operators_upsert.go:1490)
→ `pgIndexTupleKey` under `ctx.pgIndexKeyDesc(idx)`; the ~20
`encodeBTreeKeyForColumn` probe sites; `pgIndexTupleKeys = true`; explicit
dual-format decision for indexes the resolver refuses. The upper bound is
NO LONGER part of it. Consider one more split (writers vs probes) — but the
gate flip must be atomic with both. Gates: tpch-spotcheck + **TPC-DS SF0.5
gate (mandatory)**, re-pin after REINDEX. Re-read the fix_plan banner first
(M-NIGHTLY filing unconditional; the six `AI-20260810-011258-*` items are
filed and left unchecked per the banner).

Gates run: `go build ./...` + `go vet` clean; `go test` PASS for
./internal/executor ./internal/access/btree;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35); commit-hook
pgbench smoke PASS. NOT run: TPC-DS SF0.5 gate (blob path byte-identical).

In-flight: none.
