# M0106-0010 Step 3s — Index Tuple BlockIdData Encoding Fix

Status: accepted (2026-05-18)

## Problem

After Step 3r corrected the PG18 OID assignment for `pg_index_indexrelid_index` (2679 vs 2678), the `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` re-run produced a new FATAL on every PG backend connection:

```
FATAL:  could not open file "base/5/1249.1" (target block 196608):
        previous segment is only 6 blocks
```

`base/5/1249` is the standby's shared `pg_attribute` heap. The segment-suffix `.1` indicates PG was asked to read block ≥ 131072 (PG's `RELSEG_SIZE` for an 8 KiB block file). Target block 196608 = `0x30000` = `3 << 16`. The heap only has 6 blocks (1 segment). Block 3 of `pg_attribute` was somehow being read back as block `(3 << 16) | 0 = 196608`.

## Root cause

`internal/initdb/btree_index_bootstrap.go` contained two index-tuple encoders, both of which had been writing `heapBlk` as a single little-endian `uint32`:

```go
le.PutUint32(out[0:4], heapBlk)   // pgBuildIndexTupleOidKey
le.PutUint32(out[0:4], heapBlk)   // pgBuildIndexTupleOidInt2Key
```

PG's `ItemPointerData.ip_blkid` is *not* a single LE `uint32`. From `postgres/src/include/storage/block.h`:

```c
typedef struct BlockIdData {
    uint16 bi_hi;
    uint16 bi_lo;
} BlockIdData;

BlockIdSet(BlockIdData *blockId, BlockNumber blockNumber) {
    blockId->bi_hi = blockNumber >> 16;
    blockId->bi_lo = blockNumber & 0xffff;
}
BlockIdGetBlockNumber(const BlockIdData *blockId) {
    return (((BlockNumber) blockId->bi_hi) << 16) | ((BlockNumber) blockId->bi_lo);
}
```

The struct is two host-endian `uint16` halves *in struct declaration order* (`bi_hi` at offset 0, `bi_lo` at offset 2). On disk, with x86-64 LE host endianness:

- bytes `[0..1]`: `bi_hi` as LE uint16
- bytes `[2..3]`: `bi_lo` as LE uint16

For block number `N`, the correct on-disk bytes are `(N >> 16, N & 0xFFFF)` written as two LE `uint16`s — which is byte-equivalent to a *big-endian* `uint32` over the 4-byte window, NOT a little-endian `uint32`.

For block 3:

| Encoding | Bytes `[0..3]` | PG decodes as |
|---|---|---|
| Correct PG layout | `00 00 03 00` | `bi_hi=0, bi_lo=3` → 3 |
| Buggy LE uint32 | `03 00 00 00` | `bi_hi=3, bi_lo=0` → `(3<<16)|0` = **196608** |

This exactly explains the FATAL: the index 2659 (`pg_attribute_relid_attnum_index`) returned an IndexTuple whose TID pointed at block 196608 instead of block 3, and PG's `mdread` could not open segment 1 of a 6-block file.

The bug had been silent through Steps 3l/3m/3o/3p because:

- Step 3l (`pg_opclass_oid_index`) packed all 12 rows on heap block 0 — block 0 round-trips to 0 under either encoding. No FATAL.
- Step 3m (`pg_class_oid_index`) spans more blocks but PG's catcache fills early and the heap-fetch path was not hit during the FATALs that surfaced at 3p/3q/3r (those were syscache-miss errors, not heap-fetch errors).
- Step 3o (`pg_attribute_relid_attnum_index`) — only triggered TID dereferences once Step 3r's catalog correction let PG actually search through 2679, which then chained into pg_attribute lookups for cross-table relation descriptors. That is what surfaced the bug at 3s.

## Fix

`internal/initdb/btree_index_bootstrap.go`:

- `pgBuildIndexTupleOidKey` — replace `le.PutUint32(out[0:4], heapBlk)` with two LE `uint16` writes:

  ```go
  le.PutUint16(out[0:2], uint16(heapBlk>>16))   // bi_hi
  le.PutUint16(out[2:4], uint16(heapBlk&0xFFFF)) // bi_lo
  ```

- `pgBuildIndexTupleOidInt2Key` — same change.
- Doc-comments updated to point at `postgres/src/include/storage/block.h` and explain the `bi_hi/bi_lo` invariant. The previous comment claimed the on-disk LE encoding was "byte-identical to a LE uint32," which was the bug.

`internal/initdb/btree_index_bootstrap_test.go`:

- `TestPgBuildIndexTupleOidKeyLayoutMatchesPG18` and `TestPgBuildIndexTupleOidInt2KeyLayoutMatchesPG18` — decode the block via `(uint32(bi_hi) << 16) | uint32(bi_lo)` and assert the round-trip equals `heapBlk`. The previous test used `le.Uint32(out[0:4])` which pinned the bug rather than catching it.
- `TestBootstrapPgOpclassOidIndexWritesPopulatedBtree`, `TestBootstrapPgClassOidIndexWritesPopulatedBtree`, `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree`, and `TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree` — local block-id decoder switched to the `bi_hi/bi_lo` form.

No other on-disk-layout files needed updating; the cross-package smoke (`./internal/executor ./internal/server ./internal/storage ./internal/catalog ./internal/mvcc`) is byte-clean.

## Out of scope (carry-over)

`internal/storage/heap.go` writes `t_ctid` for fresh heap tuples via `binary.LittleEndian.PutUint32(p[off+12:off+16], uint32(blk))`. PG decodes `t_ctid` via `BlockIdGetBlockNumber`, so the same bug exists there in principle. It is not load-bearing for standby boot — PG only reads `t_ctid` during UPDATE/HOT chain following, which a freshly-bootstrapped cluster does not exercise. A separate step will harmonise the heap encoder once an actual symptom surfaces (most likely during the first physical-rep UPDATE on a bootstrap-time catalog row).

## Verification

- `go test -count=1 -run 'TestPgBuildIndexTuple|TestBootstrapPgOpclassOidIndex|TestBootstrapPgClassOidIndex|TestBootstrapPgIndexIndexrelidIndex|TestBootstrapPgAttributeRelidAttnumIndex|TestPgBuildBtreeLeafRoot|TestPgBuildBtreeMetapage' ./internal/initdb/` — PASS
- `go test -count=1 ./internal/initdb/` — the same 14 pre-existing baseline failures as Step 3r (`TestMigration*`, `TestCreate*`, `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`, `TestOpenOldClusterWithoutM0030FilesStillWorks`, `TestSystemCatalogRelfilesAreValidHeapPages`, `TestCommittedTableSurvivesCrashRestart`, `TestRuntimeCloseTriggersFinalCheckpoint`, `TestMultipleTablesLoadFromHeap`); no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -count=1 -timeout 300s -run TestE2E_FailoverGoopgToPG/async ./internal/testport/` — advances past the `target block 196608` FATAL to the next blocker `FATAL: could not open relation with OID 2684` (`pg_amop_fam_strat_index`), Step 3t territory.

## Regression pins

- `TestPgBuildIndexTupleOidKeyLayoutMatchesPG18` — asserts `(bi_hi<<16)|bi_lo == heapBlk` for `0xDEADBEEF`.
- `TestPgBuildIndexTupleOidInt2KeyLayoutMatchesPG18` — same for the composite-key tuple.

Both pins fail loudly if any future refactor reintroduces the LE-uint32 encoding.
