# Storage (buffer mgr / heap / aio / lmgr) — Code Review 2026-08-31

Files: internal/storage/{arena,bgwriter,bufmap,bufpool,checksum,command_id,freeze,fsm,fsm_fork,heap,io_trace,linepointer,page,pageident_probe,pdlsn_assert,prune,scan_ring,smgr,unsafe,vm,vm_fork,vm_redo,writeback,writeback_linux,writeback_other}.go, internal/storage/aio/{aio,method_iouring_linux,method_iouring_other,method_sync,method_worker,read_stream}.go, internal/storage/lmgr/{deadlock,fastpath,lockmgr}.go, internal/storage/file/pgtemp.go
Findings count: 23

---

## Findings

### `heap.go:CollectDeadHeapSlots` — copies every tuple to inspect only its header
- **Issue**: For each LP_NORMAL line pointer it calls `ParseHeapTuple(p[off:off+ln])`, which `append`s copies of both `Data` and `Bitmap` (two allocations + two memcpys per tuple). The tuple body is never used — only `t.Header` feeds `isDead`. This is the VACUUM dead-set scan over a whole page of tuples.
- **Why**: `ParseHeapTuple`'s defensive copy exists for callers that may retain the tuple past the pin/lock. Here the caller holds the page pin + exclusive content lock for the whole call and consumes the header immediately.
- **Suggestion**: Use `parseHeapTupleAlias(p[off:off+ln])` (already available and used by the hot read paths). Same reasoning as the `PageGetHeapTupleInto` comment (heap.go:732-737) — one allocation instead of three per tuple.
- **Severity**: high

### `vm.go:PageAllVisible` / `PageAllFrozen` — same full-tuple copy, only the header is read
- **Issue**: Both loop over every LP_NORMAL tuple calling `ParseHeapTuple`; only `Xmin`, `Xmax`, `Infomask` are read. VACUUM calls these per page it wants to mark ALL_VISIBLE/ALL_FROZEN, so a fully-visible table scans with an allocation+copy per tuple.
- **Why**: These are scan predicates on a locked/pinned page; the tuple does not escape the call.
- **Suggestion**: Decode with `parseHeapTupleAlias`.
- **Severity**: high

### `prune.go:pagePruneCore` — `ParseHeapTuple` copy per tuple on the prune/VACUUM path
- **Issue**: The main loop decodes each candidate tuple with `ParseHeapTuple(p[off:off+ln])` only to run `isDead(t.Header)` (via `TupleDeadToAll`); the Data/Bitmap copies are garbage immediately. Same for the dead-HOT-root branch which needs `t.Header.IsHotUpdated()`.
- **Why**: Page is exclusively latched by contract (caller "must hold the page's exclusive content lock for the entire call"); tuples don't escape.
- **Suggestion**: Use `parseHeapTupleAlias`.
- **Severity**: high

### `prune.go:pruneChainTip` — per-chain-member `ParseHeapTuple` copy
- **Issue**: Following a HOT chain does `ParseHeapTuple(p[off:off+ln])` per hop, again only the header (`isDead`, `IsHotUpdated`, `CTID`) is used. A long chain (up to `MaxHeapTuplesPerPage` steps) pays a copy per hop.
- **Why**: Page lock held; header-only access.
- **Suggestion**: `parseHeapTupleAlias`.
- **Severity**: medium

### `fsm.go:RecordFreeSpace` — grow-one-element-at-a-time to reach a high block number
- **Issue**: `for int(blk) >= len(pages) { pages = append(pages, 0) }` appends a single zero per iteration, reallocating and copying the whole slice each time. Recording block N costs O(N) appends; a batch of inserts that records blocks 1..N via this path is O(N²) in reallocation work.
- **Why**: `RecordFreeSpace` is on the insert path (per-tuple FSM bookkeeping per perf-optimize/07-wal-fsm-insert.md) and VACUUM records pages across the whole relation.
- **Suggestion**: Grow in one step: `if int(blk) >= len(pages) { grow := make([]uint16, int(blk)+1); copy(grow, pages); pages = grow }` (or use `slices.Grow`/`append` with a precomputed delta).
- **Severity**: medium

### `fsm.go:GetPageWithFreeSpace` — full linear scan of the block array per lookup
- **Issue**: Each call scans `f.pages[key]` from index 0 until it finds `free >= minFreeBytes`, i.e. O(nblocks) per insert-lookup. With `RecordFreeSpace` keeping the array dense up to a large last block, hot inserts walk a large prefix every time.
- **Why**: This is the primary "where can this tuple go" decision on the heap-insert hot path.
- **Suggestion**: Keep a per-relation running max (or a small max-heap / free-lists per category) alongside the array; at minimum cache the highest free block seen to bound the scan, or scan from the last-known-good index.
- **Severity**: medium

### `fsm.go:GetCandidates` — re-allocates the kept-candidates slice on every call
- **Issue**: `kept := make([]entry, 0, n)` allocates (and `out` allocates) per call on the insert path. n is small (typically 4) but the call rate is per-tuple.
- **Why**: Top-n selection is stateless today.
- **Suggestion**: A caller-supplied scratch buffer parameter (like `PageGetHeapTupleInto`'s buf) or a small stack-allocated `[4]entry` fast path would avoid the per-call alloc.
- **Severity**: low

### `fsm_fork.go:buildFSMTree` — re-parses every built page just to recover the max category
- **Issue**: After each `buildFSMPage`, the code calls `parseFSMPage(pg)` (which allocates a full `fsmSlotsPerPage` byte slice and re-reads the page) only to obtain `maxCat`. But `buildFSMPage` already computes the tree and the root node at `nodesStart` is exactly the max of all leaves — the answer is sitting in the page it just wrote. This is O(fsmSlotsPerPage) allocation + O(BlockSize) rescan per page, at every level of the FSM build.
- **Why**: The build already knows the max implicitly through its bottom-up internal-node computation.
- **Suggestion**: Have `buildFSMPage` return the max leaf category (or root node value) directly instead of re-`parseFSMPage`; drop the full-slice allocation when only `maxCat` is wanted.
- **Severity**: medium

### `fsm_fork.go:parseFSMPage` — always allocates a `fsmSlotsPerPage` slice even when only `maxCat` is needed
- **Issue**: `cats = make([]uint8, fsmSlotsPerPage)` (~4030 bytes) is allocated on every call; the only caller that needs `cats` is `ReadFSMFork`. `buildFSMTree` discards them.
- **Why**: Signature returns both cats and maxCat; callers often want one.
- **Suggestion**: Split into `fsmPageMaxCat(p)` and a caller-provided scratch slice for the cats path.
- **Severity**: low

### `vm_fork.go:parseVMPage` — allocates a full `vmMaxHeapPagesPerPage` slice per page during `ReadVMFork`
- **Issue**: `ReadVMFork` reads every VM page with `parseVMPage`, each allocating ~8176 bytes, then `masks = append(masks, page...)` grows the result slice. For a relation with many VM pages this churns allocations and double-copies.
- **Why**: Total size is knowable up front (`numPages * vmMaxHeapPagesPerPage`).
- **Suggestion**: Preallocate `masks` to the known total and fill in place, or pass a reusable scratch buffer.
- **Severity**: medium

### `vm_fork.go:WriteVMFork` — dead `numPages == 0` branch
- **Issue**: `numPages := (len(masks) + vmMaxHeapPagesPerPage - 1) / vmMaxHeapPagesPerPage`; the `if numPages == 0 && len(masks) > 0 { numPages = 1 }` branch is unreachable (the ceiling division already yields ≥1 whenever `len(masks) > 0`).
- **Why**: Leftover from an earlier formula.
- **Suggestion**: Delete the dead branch.
- **Severity**: low

### `bufmap.go:compact` — unpacks a BufferTag and re-hashes instead of hashing the packed key
- **Issue**: For every live bucket it does `tag := unpackKey(k0, k1)` then `bufTagHash(tag)`, reconstructing a struct and re-mixing it. `bufTagHash` and `packKey` share the same FNV-style mixing (they are near-inverses), so the hash can be derived directly from `(k0, k1)` without the round-trip.
- **Why**: `compact` is already the cold-ish eviction-compaction path, but it touches every bucket.
- **Suggestion**: Add a `bufTagHashPacked(k0, k1)` (or have `bufTagHash` operate on the packed words) and drop `unpackKey`.
- **Severity**: low

### `bufmap.go:Lookup` — `in.mask` re-loaded as a field access on every probe iteration
- **Issue**: The probe loop does `h = (h + 1) & in.mask` per step; `in` is a pointer, so this is a load-from-heap per iteration. `size := in.mask + 1` is already hoisted above the loop.
- **Why**: Lookup is the hottest function in the buffer manager (every Pin/TryPin fast path).
- **Suggestion**: Hoist `mask := in.mask` before the loop.
- **Severity**: low

### `lmgr/lockmgr.go:grantedExcept` — O(number of holders) recomputation when the cached mask already has the answer
- **Issue**: `grantedExcept(b)` rebuilds the OR by iterating every holder entry. It is called on the hot path by `canGrantImmediately` (Acquire/TryAcquire) and by `wakePassLocked` for every promoted waiter. `st.granted` already holds the OR of all holders, so the "except b" mask is just `st.granted &^ st.holders[b]`.
- **Why**: `holders` maps are rebuilt/cleared constantly (tuple-level locks on hot pages can have many concurrent holders), so the per-call O(n) walk is paid on every acquire/release and every wake-pass.
- **Suggestion**: `return s.granted &^ s.holders[b]` (or keep the loop but note the mask shortcut when holders[b]==0).
- **Severity**: medium

### `lmgr/deadlock.go:findLockCycle` — linear scan of `visited` slice per recursion step
- **Issue**: Cycle detection checks membership with `for i, v := range visited`, making each visit O(depth) and the whole DFS O(n²) in visited backends.
- **Why**: Deadlock graphs are typically tiny, so impact is bounded; but this is the detector's inner loop and `lm.states` can be large.
- **Suggestion**: Use a `map[BackendID]int` (position) or a colour map for O(1) membership.
- **Severity**: low

### `aio/read_stream.go:refill` — allocates a fresh BlockSize buffer per prefetched block
- **Issue**: `buf := make([]byte, s.cfg.BlockSize)` per `NextBlock()` — one 8KB allocation per block of a sequential scan. `Next` hands the buffer to the caller and drops it; the stream never reuses the memory.
- **Why**: The stream already bounds the window (`Lookahead`); a fixed ring of `Lookahead` buffers would bound memory identically while eliminating per-block allocation.
- **Suggestion**: Pre-allocate `Lookahead` buffers at construction and recycle the head's buffer back into the tail during `refill`.
- **Severity**: medium

### `bufpool.go:Prefetch` — allocates a fresh 8KB buffer per prefetch
- **Issue**: `buf := make([]byte, BlockSize)` is allocated, handed to `PrefetchBlock`, and dropped after the (asynchronous) read lands — an allocation per prefetched block under a scan prefetch workload.
- **Why**: The engine's io_uring path needs a stable buffer for the kernel op's lifetime, so it cannot alias `s.page`; but the buffers are identical in size and disposable.
- **Suggestion**: Maintain a small pool (e.g. `sync.Pool` of `[]byte` cap BlockSize) for prefetch buffers.
- **Severity**: medium

### `bufpool.go:maybeEmitFPI` / `MarkDirtyForceFPI` / `MarkDirtyChangeRecord` / `MarkDirtyLogicalChange` — independent 8KB `make+copy` per FPI
- **Issue**: Each FPI-emission site does `pageCopy := make(Page, BlockSize); copy(pageCopy, s.page)` and then `logFPI`. The copy is needed so the encoder doesn't race the live buffer, but the allocation is repeated at four sites with no reuse.
- **Why**: FPI emission happens once per page per redo epoch, so it is not the hottest path; still a full 8KB alloc+copy each time.
- **Suggestion**: A small `sync.Pool` of BlockSize buffers (or a single scratch page per writer) would remove the allocation; if kept, at least centralise into one helper.
- **Severity**: low

### `page.go:InitPage` — manual zeroing loop instead of `clear`
- **Issue**: `for i := range p { p[i] = 0 }` zeroes the page byte-by-byte. `clear(p)` (Go 1.21+) compiles to the same memset but is clearer and can be recognized by the compiler across versions.
- **Why**: InitPage runs on every new-page / extend path, though the memset cost is inherent either way.
- **Suggestion**: Use `clear(p)`.
- **Severity**: low

### `aio/method_iouring_linux.go:completeOne` — identical `if/else` branches
- **Issue**: `if r.N == 0 { r.Err = io.EOF } else { r.Err = io.EOF }` — both branches assign the same error.
- **Why**: Cosmetic redundancy; no behaviour difference.
- **Suggestion**: Collapse to `r.Err = io.EOF`.
- **Severity**: low

### `checksum.go:pageChecksumBlock` — `if off == 8` branch evaluated in the innermost loop
- **Issue**: The `off == 8` zero-mask test is checked for all 2048 words; only the word at i==0, j==2 needs it. The branch is cheap but sits in the hottest loop of a per-page checksum computation (done on every read and write when checksums are enabled).
- **Why**: Checksums are computed per block on the I/O path.
- **Suggestion**: Handle the single word outside the loop (compute the j==2, i==0 case specially) or unroll the i==0 row.
- **Severity**: low

### `smgr.go:relPath`/`relDir`/`sharedOrPerDBRelDir` and `fsm_fork.go:RelForkPath`, `file/pgtemp.go:FilePattern` — `fmt.Sprintf`/`fmt.Sprint` for integer formatting
- **Issue**: Several path builders format relation OIDs / db OIDs / pids with `fmt.Sprint`/`fmt.Sprintf`. These run per `relFile` open (and per path construction in checkpoint/save-fork paths). `strconv.FormatUint`/`Itoa` is faster and allocation-free for integers.
- **Why**: Path building is repeated at every file open and each fork save; the fmt machinery (reflection-based) is avoidable overhead.
- **Suggestion**: Use `strconv.AppendUint`/`strconv.Itoa` (smgr.go:461-469 already uses `filepath.Join` + `fmt.Sprint`; keep the layout, swap the formatter).
- **Severity**: low

### `smgr.go:extendBatch` — per-block `IsNew` re-check on identical copies
- **Issue**: In the checksums branch, `IsNew(blkBuf)` is evaluated for each of the n identical block copies of `buf`. The result is the same for every block (they are all copies of the same page) and `IsNew` itself re-validates the page header per call.
- **Why**: Cosmetic redundancy on the bulk-extend path.
- **Suggestion**: Compute `isNew := IsNew(buf)` once before the loop.
- **Severity**: low

---

## Files with no findings

- `arena.go`, `bgwriter.go`, `command_id.go`, `freeze.go`, `io_trace.go`, `linepointer.go`, `pageident_probe.go`, `pdlsn_assert.go`, `scan_ring.go`, `unsafe.go`, `vm.go` (read helpers aside from the PageAll* noted above), `vm_redo.go`, `writeback.go`, `writeback_linux.go`, `writeback_other.go`, `aio/aio.go`, `aio/method_iouring_other.go`, `aio/method_sync.go`, `aio/method_worker.go`, `lmgr/fastpath.go` — no wasteful-processing issues beyond the trivial items noted. Most diagnostic files (io_trace, pageident_probe, pdlsn_assert) are env-gated at package init and correctly cost one branch when disabled.
