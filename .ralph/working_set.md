Task: M0130-S11.4 slice 3b-2c-ii-B2-c-viii (the fingerprint funnel) — DONE,
committed + pushed. Nothing uncommitted.

Landed (no behaviour change at all, `pgIndexTupleKeys` still false, no REINDEX):
- `internal/executor/pgindex_fingerprint.go`: `indexKeyFingerprint` (whole-key)
  and `indexColumnFingerprint` (per-column). Neither takes a `*Context`, a
  descriptor or an ItemPointer — they cannot acquire a heap TID by accident.
- Six sites routed: `indexKeyColumnsChanged`, `ssiRecordHashIndexInsert`,
  `nndKeyColumnsEqual` (×2), `resolveNNDKeyColsFromRow`, `scanNNDLiveMatches`.
- Named invariant now in docs/design/0130-0011: after the flip goopg computes a
  key TWO ways for a describable index (tuple image for the tree, blob for the
  fingerprints), so `encodeIndexKeyFromCols`/`encodeBTreeKeyForColumn` SURVIVE
  the flip. Discovery: the SSI hash bucket pairs the WRITER's fingerprint with
  the READER's *scan search key*, so it holds only because
  `buildPGIndexKeyDesc` refuses non-btree methods — load-bearing for SSI.
Guard: internal/executor/pgindex_fingerprint_test.go (6 tests incl. a
function-scoped source scan), mutation-checked 2 ways (revert one NND site;
remove the access-method refusal).

Next step (per fix_plan banner: M-NIGHTLY filing then M0130):
1. **3b-2c-ii-B2-c — THE FLIP** (REINDEX-required). Every funnel is now in
   place — scan, row writers, bulk build, arbiter, posting dedup — and the
   fingerprints are guarded as permanently blob. Remaining: the standing
   decision that expression indexes stay permanently blob
   (`buildPGIndexKeyDesc` refuses them), then `pgIndexTupleKeys = true`.
   Gates: tpch-spotcheck + **TPC-DS SF0.5 gate (mandatory)**, re-pin anchors
   after the REINDEX.
2. Then 3b-3 (blob MAXALIGN, `_bt_keep_natts` suffix truncation,
   `MaxHighKeyLen`/`bulkHighKeyReserve` → `BTMaxItemSize`, dead
   `backfillBTree`, dead `appendTIDToPosting`/`promoteSingleToPosting`).
Re-read the fix_plan banner first (M-NIGHTLY filing unconditional; the six
`AI-20260810-011258-*` items are already filed and left unchecked).

Gates run: `go build ./...` + `go vet ./internal/executor` clean; `go test` PASS
for ./internal/executor ./internal/access/btree; the 6 new guards PASS and fail
under mutation; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35). NOT run:
TPC-DS SF0.5 gate (pure rename/indirection, byte-identical output).

In-flight: none.
