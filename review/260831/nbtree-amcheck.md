# NBTREE + AMCHECK — Code Review 2026-08-31

Files: nbtree/{btree.go, btree_vacuum.go, bulkload.go, dead_purge.go, latch_release.go, lpdead_kill.go, parse_err_dump.go, pgcompare.go, pgcompare_types.go, pgdelete.go, pgformat.go, pgitemcodec.go, pgkeycmp.go, pgnewroot.go, pgpage.go, pgpagedel.go, pgsplit.go, pgsplitleft.go, pgtruncate.go, pgtuple.go, pgvacuum.go, posting.go, replay.go}, amcheck/{bloomfilter.go, heapallindexed.go, heapallindexed_heapscan.go, heapallindexed_relation.go, verify_heapam.go, verify_heapam_relation.go, verify_nbtree.go, verify_nbtree_unique.go}, common/pglz/pglz.go, backup/basebackup.go, commands/vacuum/vacuum.go
Findings count: 14

---

### `pgnewroot.go:PGRestorePageData` — per-item allocation just to append alignment padding
- **Issue**: The loop does `out = append(out, make([]byte, MaxAlign(len(raw))-len(raw))...)` — one `make` allocation per item, purely to append zero bytes. Called on every page whose item area a new-root record must rebuild.
- **Why**: Padding is always zero bytes; a fresh allocation per item (up to hundreds of items per page) is pure churn. The slice already has `cap = BlockSize`.
- **Suggestion**: Pre-zero a small scratch buffer and append a slice of it, or grow once (`out = out[:cap]` after the loop and slice down), or use a single reusable `[7]byte` zero buffer. Simplest: `pad := out[len(out) : len(out)+MaxAlign(len(raw))-len(raw)]` after ensuring capacity, since append already zero-initialises reserved-but-unsliced backing storage only on `make` — but note append semantics; cleanest is a package-level `var zeroPad [7]byte` and `out = append(out, zeroPad[:pad]...)`.
- **Severity**: low (WAL/replay path, not hot, but trivially avoidable)

### `pgnewroot.go:PGParseRestorePageData` — double slice + double copy to reverse the item order
- **Issue**: Parses forward into `reversed` (allocating a per-item copy of every item), then allocates a second `items` slice and copies each item again in reverse. Two extra allocations (reversed + items) plus a full second copy of every item's bytes.
- **Why**: The run is self-framing so it must be walked forward, but the results can be written directly into a single preallocated `items` slice from the back (`items[remaining-1]`), eliminating `reversed` and the second copy.
- **Suggestion**: Allocate `items` with a count known only after the walk, or grow one slice and write in reverse order; at minimum fill one slice back-to-front instead of building two.
- **Severity**: low (WAL/replay path)

### `pgformat.go:WritePGMetaPage` — redundant zeroing of alignment hole and tail padding
- **Issue**: Explicitly zeroes bytes 28..32 (alignment hole) and 41..48 (tail padding) byte-by-byte in a loop. Both callers (`InitPGMetaPage`, `ReplayRestoreMetaPage`) freshly re-initialise the page via `InitPGBTPage` → `storage.InitPage`, which zeroes the whole page first, so the writes are dead. The `for i := off+41; ...` loop is also a slow byte-at-a-time loop.
- **Why**: If a caller ever did a read-modify-write (which `ReplayMetaSetRoot` does via ReadPGMetaPage/WritePGMetaPage), the page may not be zeroed — but in that case the padding bytes are already stable (written by an earlier init), so the explicit zeroing is still redundant there.
- **Suggestion**: Drop the explicit zeroing, or replace the byte loop with a single `clear(p[off+41:off+SizeOfBTMetaPageDataPG])` if kept defensively.
- **Severity**: low

### `pgpage.go:pgFirstDataSlot` — opaque re-decoded on every accessor call inside loops
- **Issue**: Every `pgGetItemRaw`/`pgDataSlot`/`PGDataItemCount` call re-reads and re-decodes the full 16-byte opaque area (`ReadPGOpaque` = five little-endian reads) just to obtain the rightmost bit for the P_FIRSTDATAKEY bias. Loops such as `pgDataItemRaws` (pgsplitleft.go), `ReplayVacuumDelete`, `ReplayRemoveParentDownlink` and `ReplayHalfDeadParent` call these accessors once per item, re-decoding the opaque per iteration.
- **Why**: The rightmost-ness of a page is invariant for the duration of any of these loops (nothing inside the loop mutates btpo_next), so the bias is a loop invariant that is recomputed per item.
- **Suggestion**: Hoist `pgFirstDataSlot(p)` (or `ReadPGOpaque`) out of the loop and thread the bias through the accessors, or add a bias-taking variant of the accessors used by the multi-item loops.
- **Severity**: low/medium (WAL/replay + verify paths; per-item redundant decoding)

### `lpdead_kill.go:KillItems` — no issue
- **Issue**: None of consequence. The map grouping of kills by leaf and the per-leaf LSN re-check are necessary given the D7 protocol.
- **Why**: n/a
- **Suggestion**: n/a
- **Severity**: none

### `dead_purge.go:purgeDeadHeapPointers` — no significant issue
- **Issue**: The `tids` slice is always allocated even when the filter never uses it meaningfully; trivial. The all-dead branch's backward scan to free one slot is O(n) but only on the all-dead page.
- **Why**: n/a
- **Suggestion**: Could compute `ndead` in the same pass that builds `out`, avoiding the first `ndead` scan, but the code is already linear.
- **Severity**: none (low at most)

### `parse_err_dump.go` / `latch_release.go` / `pgsplit.go` / `pgpagedel.go` / `pgnewroot.go`(rest) — no issues
- **Issue**: Debug-only dump path (env-gated); latch release is tiny; the split/page-deletion replay helpers are small and format-free.
- **Why**: n/a
- **Suggestion**: n/a
- **Severity**: none

### `btree.go:resetPageItems` — byte-by-byte zeroing of the whole data area on every page rewrite
- **Issue**: `for i := storage.SizeOfPageHeaderData; i < btSpecialOffset; i++ { p[i] = 0 }` zeroes ~8 KiB one byte at a time, and is on the path of every whole-page rewrite: both split halves (`refillDeduplicated` callers), dedup-recovery, VACUUM kept-items rewrites, and WAL replay. The comment even admits it is "not strictly required".
- **Why**: A manual per-byte loop is slower than the built-in vectorised clear and generates more code. Repeated ~8 KiB clears on the split/write path add up.
- **Suggestion**: Replace with `clear(p[storage.SizeOfPageHeaderData:btSpecialOffset])` (Go 1.21+ memclr), or at least `storage.Header(p).SetLower/SetUpper` and skip zeroing entirely (pages are re-filled immediately after).
- **Severity**: medium (hot write path; trivial change)

### `btree.go:findChildBlockDirect` / `btree.go:insertItemSorted` — per-probe key copies inside descent/insert binary search
- **Issue**: The binary-search predicates call `pgGetItemRawAllowDead(p, slot)` (which copies the raw item) and then `f.parse(raw)` (which copies the key again) on EVERY probe — O(log n) allocations per descent / per insert.
- **Why**: The searched page is pinned for the whole search, so the probe keys need not outlive the predicate; aliasing reads (`pgGetItemRawNoCopy` + `parseNoCopy`) are safe here, exactly the pattern the range-scan hot loop already uses (M0091-0002). These two are the descent and leaf-insert inner loops.
- **Suggestion**: Add no-copy probes (e.g. a `readPageItemNoCopy`) and use them in `findChildBlockDirect` and `insertItemSorted`'s binary search, falling back to the copying form only for postings that must allocate anyway.
- **Severity**: medium (hot path allocation reduction; low risk since pin is held)

### `btree.go:Search` — decodes the whole leaf into a slice before binary-searching it
- **Issue**: `Search` calls `bt.format().pageItems(slot.Page())`, decoding every item on the leaf (a full slice of allocations), then `sort.Search` over that slice. A point lookup only needs O(log n) probes and no full-page item slice.
- **Why**: This is the single-row index lookup hot path (pgbench -N point probes). The whole-page decode + slice is O(page items) work and allocations per probe where O(log n) suffices, the same win `findChildBlockDirect` already provides on internal pages.
- **Suggestion**: Binary-search the raw line pointers directly (probe via `readPageItem`-style no-copy reads), expanding only a posting list on the exact match slot. Requires care with the compareKeyAttrs/compare split, but is a drop-in on the leaf.
- **Severity**: medium

### `btree.go:EncodeNumericKey` — fresh big.Int allocation per trailing-zero strip iteration
- **Issue**: The trailing-zero stripping loop does `new(big.Int).QuoRem(abs, ten, rem)` — allocating a brand-new `*big.Int` on every iteration — plus `big.NewInt(10)` and `big.NewInt(0)` once.
- **Why**: For a value with many trailing zeros the loop runs that many times, churning heap garbage per numeric key encode. Only the quotient is used; the remainder `rem` is reused already, but the receiver is not.
- **Suggestion**: Hoist `q := new(big.Int)` and call `q.QuoRem(abs, ten, rem)`, then `abs.Set(q)` (or just `q.Quo(abs, ten)` and `abs = q`), avoiding a per-iteration allocation.
- **Severity**: low/medium (per-index-key encode; small but free win)

### `btree.go:pinNewOrRecycled` — manual zeroing loop for recycled pages
- **Issue**: `for i := range page { page[i] = 0 }` zeroes an 8 KiB recycled page byte-by-byte on every recycled-block allocation (split path).
- **Why**: Same as resetPageItems — `clear(page)` is the vectorised, idiomatic form.
- **Suggestion**: `clear(page)`.
- **Severity**: low

### `btree.go` — no further issues
- **Issue**: `readOpaque` decodes per call but is used once per page in the hot loops; debug instrumentation is properly gated; `rangeScanPosLeaf`'s posting allocation is documented out-of-scope. `EncodeVarchar`/`DecodeVarchar` are linear and necessary.
- **Why**: n/a
- **Suggestion**: n/a
- **Severity**: none

### `bulkload.go` — no significant issue
- **Issue**: `deduplicateToRawItemsWithSpans` allocates a `tids` slice per posting chunk and `sort.SliceStable` uses the reflection-based sort; both are acceptable for a one-shot build. `linksToInternalItems` copies every separator key — needed for ownership.
- **Why**: n/a
- **Suggestion**: n/a
- **Severity**: none

### `amcheck/heapallindexed.go:fingerprintLeafEntry` — a fresh buffer allocation per element, twice per element
- **Issue**: `fingerprintLeafEntry` does `make([]byte, 6+len(e.Key))` per call, and `VerifyBtreeHeapAllIndexed` calls it once per index leaf entry (add phase) and once per heap entry (probe phase) — so 2 allocations per (key, TID) pair, for millions of entries on a large table. The buffer exists only to be hashed by `bloomAddElement`/`bloomLacksElement`.
- **Why**: This is a maintenance/verification path, but the per-element heap churn is trivially avoidable.
- **Suggestion**: Hash the components directly (a `bloomHashParts(block, offset, key)` that folds the two fixed fields plus the key into the FNV loop), or reuse a caller-owned scratch buffer sized to the longest key.
- **Severity**: low/medium (verification path; large allocations count)

### `amcheck/verify_heapam.go`, `verify_heapam_relation.go`, `verify_nbtree.go`, `verify_nbtree_unique.go`, `heapallindexed_relation.go`, `heapallindexed_heapscan.go` — no significant issues
- **Issue**: Two-pass page walks with a single `entries`/`predecessor` array are linear and necessary; `fmt.Sprintf` messages are only built on corruption findings; the `visited` maps bound cycle detection and are correct; the per-page `src()` calls are unavoidable given the PageSource seam. `checkTupleHeader` reads t_infomask2 twice (once via `infomask2`, once via `readInfomask2`) — negligible.
- **Why**: n/a
- **Suggestion**: none
- **Severity**: none

### `pglz.go:Compress` — brute-force O(n·window·matchlen) match search
- **Issue**: For every input byte, the encoder scans up to 4095 history offsets, each comparing up to 273 bytes: worst case ~1.1M byte comparisons per input byte. PostgreSQL uses a hash-chain matcher (pg_lzcompress.c) with heuristics precisely because the brute-force window scan does not scale. Compressing even a moderately large catalog blob (e.g. pg_rewrite.ev_action) is quadratic.
- **Why**: The greedy longest-match loop has no acceleration; only the `bestLen == maxLen` early exit bounds the common case.
- **Suggestion**: Maintain a hash chain over the 4095-byte window (upstream's `hhead[]`/`hist_pre[]` approach) or at minimum a last-seen-position hash for each 3-byte prefix to bound the offset candidates, as upstream does.
- **Severity**: high (algorithmic, on the write path)

### `pglz.go:Decompress` — byte-by-byte run-length copy even for non-overlapping matches
- **Issue**: `for k := 0; k < length; k++ { dst = append(dst, dst[start+k]) }` copies every match byte-by-byte, including the common `off >= length` (non-overlapping) case where a builtin `copy` would move the run in one go. For matches up to 273 bytes this is a slow inner loop over compressed output.
- **Why**: The byte loop is needed only when `off < length` (run-length expansion with overlap). The code comments acknowledge this.
- **Suggestion**: `if off >= length { dst = append(dst, dst[start:start+length]...) } else { byte loop }`, keeping the overlapping branch for the RLE case.
- **Severity**: low/medium (decompress path)

### `backup/basebackup.go:baseBackupStreamer.Write` / `streamBackupManifest` — fresh frame buffer allocated per chunk
- **Issue**: Each 64 KiB chunk allocates `make([]byte, 1+n)` (plus a `copy`) to prefix the `'d'` type byte. Over a multi-GB base backup this is a large number of 64 KiB allocations and copies that could be avoided.
- **Why**: The whole stream is chunked writes; the buffer contents never outlive the call.
- **Suggestion**: Reuse a single scratch buffer on the streamer (`scratch [baseBackupChunkBytes+1]byte` or a pooled buffer), and give `WriteCopyData` a prefix+payload form to avoid the copy, or write the `'d'` byte and payload separately.
- **Severity**: low/medium (memory churn during a long backup)

### `backup/basebackup.go:emitBaseBackupTar` / `emitTablespaceTar` — whole-file buffering
- **Issue**: Every file in the data directory is fully read into memory with `os.ReadFile(e.abs)` before being written to the tar and checksummed. A large relfile is fully buffered even though tar could stream it (tar.Writer is an io.Writer).
- **Why**: The checksum must cover the file and tar needs the size in the header before writing the body; but both can be done with a streaming tee (size via `Stat().Size()`, checksum via an `io.MultiWriter`/hash while copying), keeping peak memory at O(buffer) instead of O(largest file).
- **Suggestion**: For files above a size threshold, stream: write the tar header from `info.Size()`, then copy `os.Open` → `io.MultiWriter(tw, hash)` in chunks. Same for the manifest checksum.
- **Severity**: low/medium (memory during backup; correctness unaffected)

### `backup/basebackup.go` — no further issues
- **Issue**: The option parsers and label/manifest builders are one-shot and negligible. `formatLSN`'s Sprintf per result row is trivial.
- **Why**: n/a
- **Suggestion**: n/a
- **Severity**: none

### `commands/vacuum/vacuum.go` — no significant issues
- **Issue**: The `costSeen` map is lazily allocated only when pacing is on; per-page work is linear; dead-TID collection and FSM/VM updates are necessary. `Analyze` walks every block and decodes every LP_NORMAL tuple, which is inherent to a full-scan ANALYZE.
- **Why**: n/a
- **Suggestion**: none
- **Severity**: none

---

## Summary

No correctness problems found; all findings are efficiency-only.

Highest-value items (hot or algorithmic):
- `pglz.Compress` brute-force matcher (quadratic; PG uses a hash chain)
- `btree.go:Search` whole-leaf decode + slice per point lookup; `findChildBlockDirect`/`insertItemSorted` per-probe copies in the descent/insert binary search
- `btree.go:resetPageItems` and `pinNewOrRecycled` manual byte-at-a-time zeroing on the write path

Cheap, mechanical wins:
- per-element `fingerprintLeafEntry` buffer in amcheck heapallindexed
- per-chunk frame buffers in basebackup streaming
- `pglz.Decompress` non-overlapping copy
- `pgnewroot`/`pgformat`/`pgpage` WAL-path allocations and redundant re-decodes

Files with no findings: `latch_release.go`, `parse_err_dump.go`, `pgsplit.go`, `pgpagedel.go`, `pgdelete.go`, `pgsplitleft.go`, `posting.go`, `replay.go`, `pgkeycmp.go`, `pgcompare_types.go`, `pgtuple.go`, `pgvacuum.go`, `bulkload.go`, `btree_vacuum.go`, `dead_purge.go`, `lpdead_kill.go`, `pgitemcodec.go`, `pgcompare.go`, and all of `amcheck/` except the fingerprint note; `commands/vacuum/vacuum.go`.
