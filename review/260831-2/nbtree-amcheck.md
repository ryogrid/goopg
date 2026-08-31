# NBTREE + AMCHECK — Bug Review 2026-08-31

Files: btree.go, btree_vacuum.go, bulkload.go, dead_purge.go, latch_release.go, lpdead_kill.go, parse_err_dump.go, pgcompare.go, pgcompare_types.go, pgdelete.go, pgformat.go, pgitemcodec.go, pgkeycmp.go, pgnewroot.go, pgpage.go, pgpagedel.go, pgsplit.go, pgsplitleft.go, pgtruncate.go, pgtuple.go, pgvacuum.go, posting.go, replay.go, bloomfilter.go, heapallindexed.go, heapallindexed_heapscan.go, heapallindexed_relation.go, verify_heapam.go, verify_heapam_relation.go, verify_nbtree.go, verify_nbtree_unique.go (internal/access/amcheck/), pglz.go (internal/access/common/pglz/), basebackup.go (internal/backup/), vacuum.go (internal/commands/vacuum/)

Findings count: 5

---

### `vacuum.go:vacuumCore` — VM-skip branch does not update lastNonEmpty, causing tail truncation to drop live all-visible blocks

- **Bug**: When `opts.VM != nil` and a non-last block is all-visible (skipped via line 156-164), `lastNonEmpty` is not updated. Upstream's `heap_vac_scan_next_block` (vacuumlazy.c) DOES update `nonempty_pages` for skipped all-visible blocks, precisely to prevent tail truncation from dropping live data. Without this, a trailing run of all-visible-by-VM (live) blocks is truncated away, causing data loss.
- **When it triggers**: A non-aggressive VACUUM with VM enabled, `Truncate=true`, and a trailing run of all-visible blocks containing live tuples. The last block is empty (no line pointers), so `lastNonEmpty` is not advanced past the earlier all-visible blocks. `keep = lastNonEmpty + 1` is set too low, and `TruncateRelationTail` removes blocks with live data.
- **Fix**: Set `lastNonEmpty = blk` in the VM-skip branch (after line 163), matching upstream's `vacrel->nonempty_pages = blkno + 1` in the same skip path.
- **Severity**: high (data loss of live tuples)

---

### `pglz.go:Decompress` — Match length clamped instead of erroring, silently producing wrong output from corrupt streams

- **Bug**: Lines 161-163 clamp the match copy length to `remaining := rawSize - len(dst)` instead of returning an error when the match would overrun the output. A corrupt or truncated PGLZ token stream whose last match tag claims more bytes than the declared `rawSize` produces exactly `rawSize` bytes of subtly wrong output (the match is truncated at the clamp point) and passes the final `len(dst) != rawSize` check at line 178. Upstream's `pglz_decompress` returns `-1` (error) in this case.
- **When it triggers**: A corrupt or truncated PGLZ-compressed varlena whose token stream overruns the declared rawSize. The decompressor silently produces correct-length but wrong bytes.
- **Fix**: Replace the clamp with `return nil, fmt.Errorf("pglz: match length %d exceeds remaining output size %d", length, remaining)`.
- **Severity**: medium (silent wrong decompression from corrupt input; divergence from upstream)

---

### `btree_vacuum.go:readInternalFirstChildBlock` — Wrong downlink block read (unused code)

- **Bug**: Line 1507 reads `binary.LittleEndian.Uint32(raw[2:6])` as the child block number. For a pivot tuple, the downlink is stored in t_tid's block half: bytes [0:2] = bi_hi (block high 16 bits), [2:4] = bi_lo (block low 16 bits), and [4:6] = offset/natts. The correct read should be `BTreeTupleGetDownLink(raw)` (which reads the two halves and combines them). The buggy read includes the status bits from [4:6] and misses the high 16 bits from [0:2], producing a wrong block number.
- **When it triggers**: This function has **no production callers** (confirmed by grep: defined only in btree_vacuum.go and an unrelated `.claude/waitevent-impl` copy, never called elsewhere). If it were ever called, it would return a wrong child block.
- **Fix**: Use `BTreeTupleGetDownLink(raw)` instead of the manual uint32 read.
- **Severity**: low (dead code, but a correctness bomb if ever called)

---

### `btree.go:descendToLeaf` + `tryInsertOnCachedRightmost` — Wrong sentinel comparison disables rightmost-leaf cache

- **Bug**: The rightmost-leaf cache is meant to skip the full descent for append-shaped inserts. Two site use the wrong sentinel for the sibling link:
  - Line 2518 (`descendToLeaf`): `if op.Next == 0` — checks the in-memory `BTPageOpaque.Next` field for 0. But `readOpaque` translates the on-disk `P_NONE` (0) to `storage.InvalidBlockNumber` (0xFFFFFFFF), so this condition is **never true** for a rightmost page. The cache is never populated.
  - Line 3583 (`tryInsertOnCachedRightmost`): `if op.Level != 0 || op.Next != 0` — for a rightmost leaf, `op.Next = 0xFFFFFFFF`, which is non-zero, so the cache is always treated as stale and cleared.
  
  The entire optimization is thus dead code: every insert always falls through to the full descent.
- **When it triggers**: Every insert on every tree — the cache is permanently empty. Performance regression, not correctness.
- **Fix**: Line 2518: `if op.Next == storage.InvalidBlockNumber {`. Line 3583: `if op.Level != 0 || op.Next != storage.InvalidBlockNumber {`.
- **Severity**: low (performance regression only; the correct descent fallback produces correct results)

---

### `btree.go:tryInsertOnCachedRightmost` — No check for deleted/half-dead pages

- **Bug**: The cached rightmost leaf entry can point to a page that was deleted and recycled, or is BTDeleted|BTHalfDead (phase-1 marked for deletion). The function checks `op.Level != 0 || op.Next != 0` but never checks `op.IsDeleted() || op.IsHalfDead()`. If the page is mid-deletion, the insert silently succeeds on a page that is about to be unlinked; the deletion path's re-verify (under splitMu, in `unlinkEmptyLeaf`) catches this and reverts the marking, so data is not lost in the half-dead case. However, if the cached block was recycled and re-initialized as a different leaf (via `pinNewOrRecycled`), the insert lands on the wrong page and silently corrupts the tree.
- **When it triggers**: Concurrent delete+insert. The deletion path recycles a block; the cached pointer becomes stale; the next insert through the cache targets the recycled block, which now belongs to a different part of the tree. The cache is currently never populated (see previous finding), so this path is unreachable today — but if the cache bug is fixed, this becomes a live data-corruption race.
- **Fix**: Add `op.IsDeleted() || op.IsHalfDead()` to the staleness checks at line 3583 (after the existing `op.Level != 0 || op.Next != storage.InvalidBlockNumber` check).
- **Severity**: low (unreachable because the cache is never populated; latent if the cache sentinel bug is fixed)

---

### Files with no bugs found (reviewed clean)

- **dead_purge.go, latch_release.go, lpdead_kill.go, parse_err_dump.go**: Correct logic, no issues.
- **pgcompare.go, pgcompare_types.go**: `compareAttr` NULL ordering correct; `comparePGIndexTuples` correct; `decodeNumericParts` short-weight sign extension consistent with the pgnodes codec.
- **pgdelete.go, pgformat.go, pgitemcodec.go, pgkeycmp.go, pgnewroot.go, pgpage.go, pgpagedel.go, pgsplit.go, pgsplitleft.go, pgtruncate.go, pgtuple.go, pgvacuum.go, posting.go, replay.go**: All reviewed clean. Item layout, page wrappers, WAL replay, split/truncate logic, posting list handling, and format translations are correct.
- **bulkload.go**: `buildLevel`, `buildLevelRaw`, `deduplicateToRawItemsWithSpans`, `separatorReserve`, `pageHasSpaceForBulk` are correct.
- **btree_vacuum.go** (except `readInternalFirstChildBlock`): `VacuumIndexPages`, `unlinkEmptyLeaf`, `acquireUnlinkPins`, `liveSibling`, `resolveParentDownlink`, `maybeCascadeEmptyInternal`, `clearHalfDead`, `resetToEmptyRoot` are correct.
- **bloomfilter.go**: `bloomCreate`, `bloomAddElement`, `bloomLacksElement`, `kHashesValues`, `modM`, `bloomHash64` are correct.
- **heapallindexed.go, heapallindexed_heapscan.go, heapallindexed_relation.go**: Fingerprint/probe core, page-scanning, and leaf-level enumeration are correct.
- **verify_heapam.go, verify_heapam_relation.go**: Page-structural checks, HOT-chain validation, xmin/xmax numeric-bounds, and the relation-walking driver are correct.
- **verify_nbtree.go, verify_nbtree_unique.go**: Per-page checks, sibling-link agreement, parent-downlink descent, and duplicate detection are correct.
- **basebackup.go**: Tar streaming, option parsing, WAL range computation, backup_label, manifest building are correct.