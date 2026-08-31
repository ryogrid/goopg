# Storage (buffer mgr / heap / aio / lmgr) — Bug Review 2026-08-31

Files:
- internal/storage/: arena.go, bgwriter.go, bufmap.go, bufpool.go, checksum.go,
  command_id.go, freeze.go, fsm.go, fsm_fork.go, heap.go, io_trace.go,
  linepointer.go, page.go, pageident_probe.go, pdlsn_assert.go, prune.go,
  scan_ring.go, smgr.go, unsafe.go, vm.go, vm_fork.go, vm_redo.go, writeback.go,
  writeback_linux.go, writeback_other.go
- internal/storage/aio/: aio.go, method_iouring_linux.go, method_iouring_other.go,
  method_sync.go, method_worker.go, read_stream.go
- internal/storage/lmgr/: deadlock.go, fastpath.go, lockmgr.go
- internal/storage/file/pgtemp.go

Findings count: 10

---

## Files reviewed with no bugs found (notes)

- **arena.go**: alignment math correct; `raw[off:off+size]` stays in bounds.
- **checksum.go**: faithful port of PG checksum_impl.h; `off==8` mask, block
  mixing, and `(cksum%65535)+1` never-zero reduction all match upstream.
- **command_id.go**: no bug.
- **fsm.go**: no correctness bug. `GetPageWithFreeSpace` always returns the
  lowest-indexed qualifying block (no round-robin) — suboptimal but documented.
- **heap.go**: no clear bug found. Line-pointer offsets/lengths, MAXALIGN
  placement, bounds checks (`off+ln > len(p)`), and repack logic are correct.
- **io_trace.go**, **pageident_probe.go**, **pdlsn_assert.go**: gated diagnostic
  instrumentation; logic correct.
- **linepointer.go**: slide/blank/reserve logic correct.
- **page.go**: LSN encode/decode, header accessors, InitPage correct.
- **scan_ring.go**: ring buffer + TryPin path correct.
- **smgr.go**: lock ordering (Manager.mu vs relFile.mu vs blockMu) is safe;
  extend/truncate/nblocks accounting under mu is correct.
- **unsafe.go**: no bug.
- **vm_fork.go**, **vm_redo.go**: bit arithmetic (2 bits/page, 4 pages/byte)
  correct.
- **writeback_linux.go**, **writeback_other.go**: correct.
- **aio/aio.go**, **method_sync.go**, **method_worker.go**, **read_stream.go**,
  **method_iouring_other.go**: no correctness bug found (slot/queue/backpressure
  accounting checks out).
- **lmgr/deadlock.go**, **fastpath.go**, **lockmgr.go**: wait-queue, conflict
  table, strong-lock counter, and deadlock DFS logic reviewed; no clear bug.
- **file/pgtemp.go**: no bug.

---

## Findings

### `fsm_fork.go:ReadFSMFork` — FSM level reconstruction is wrong for any relation with > 1 FSM leaf page
- **Bug**: the backward tree reconstruction assumes each level's page count is
  `ceil(childLevelCount / fsmSlotsPerPage)`. For the root (`levelCount = 1`)
  it computes `need = ceil(1/4069) = 1` — i.e. it always invents exactly ONE
  page for the level below the root. But the true leaf-page count is derived
  FORWARD from the number of heap pages (`ceil(N / fsmSlotsPerPage)`), not from
  the root. For any relation needing ≥ 2 FSM leaf pages (N > 4069 heap blocks),
  the algorithm under-counts the leaf level and reads only the FIRST leaf page,
  silently dropping every free-space entry for heap blocks ≥ 4069.
- **When it triggers**: `ReadFSMFork` on a `_fsm` file with more than
  `fsmSlotsPerPage` (4069) heap blocks — i.e. any relation > ~32 MB. All
  per-block free-space data past block 4069 is lost; after a restart, inserts
  to those blocks see 0 free space and keep extending the relation (bloat,
  wrong FSM-driven behavior). It is a silent functional/data-loss bug, not a
  panic.
- **Fix**: reconstruct levels from the LEAF count. Either read the leaf pages
  greedily (each non-root level's count = `ceil(pages-below / fsmSlotsPerPage)`,
  stopping when the running total reaches `numPages-1`), or store/compute the
  leaf count from the file size and heap-page count instead of walking
  backwards from the root.
- **Severity**: high

### `prune.go:TupleDeadToAll` — plain XID comparison is wrong under wraparound (can reclaim a live row)
- **Bug**: `if effXmax >= oldestXmin { return false }` uses plain unsigned `>=`.
  Under XID wraparound a freshly assigned XID has a *smaller* numeric value than
  an older one; `XIDPrecedes` (heap.go) exists precisely because plain
  comparisons are wrong for the modular XID space. `PagePruneOpt`'s gate
  `pruneXID >= oldestXmin` and `pagePruneCore`'s `TupleDeadToAll` are the
  decide-what-to-delete path — a wrong "dead" verdict here permanently
  reclaims a live row's storage.
- **When it triggers**: once the cluster's XID counter wraps past 2^31 and a
  deleting xid numerically smaller than `oldestXmin` (but actually newer) is
  tested. VACUUM/prune then treats a live tuple as dead → row loss. Same issue
  class in `vm.go:PageAllVisible` / `PageAllFrozen` (`xmin >= horizon`) and
  `freeze.go:PageFreezeOldTuples` (`xmin >= freezeBelow`).
- **Fix**: use the modular ordering (`!XIDPrecedes(effXmax, oldestXmin)` for
  the "not older ⇒ not dead" test; likewise for horizon comparisons in
  vm.go/freeze.go).
- **Severity**: high (data loss, but only after XID wraparound which PG/autovac
  normally prevents)

### `bufpool.go:pinLoad`/`evictVictim` — dirty victim's unflushed content is discarded when the flush fails
- **Bug**: on a dirty-victim flush error, `evictVictim` has already cleared the
  slot's valid+dirty bits (claimVictim's CAS set IO-inflight only) and deleted
  the tag from bufmap; the caller (`pinLoad`, `pinNewXID`) then calls
  `releaseVictimSlot`, which sets `state` to 0, destroying the newer page
  content with the dirty bit gone — the data can never be retried.
- **When it triggers**: `flushSlot`/`WriteBlock` (or the WAL flush) returns an
  error during eviction under buffer pressure (ENOSPC, I/O error). The affected
  block's latest in-memory content is silently lost (the query gets an error,
  but the page's newer state is not preserved for a retry).
- **Fix**: on flush failure, restore the slot to valid+dirty and re-insert the
  tag into bufmap (or otherwise preserve the dirty page) instead of
  `releaseVictimSlot` wiping it.
- **Severity**: medium

### `bufpool.go:WriteDirtyPages` — bgwriter scan cursor never advances
- **Bug**: `p.bgwriterHand = (start + n) % n` with `n == len(p.slots)`. Since
  `0 <= start < n`, `(start+n)%n == start` — the cursor is always reset to
  itself, so every bgwriter tick scans the pool in identical order from slot 0
  (the documented "independent scan cursor" rotation is dead code).
- **When it triggers**: every WriteDirtyPages call. Pool is fully scanned each
  tick, so no page is skipped, but the rotation/fairness intent is defeated.
- **Fix**: advance by the number scanned, e.g. record `end = (start+i)%n` after
  the scan or `p.bgwriterHand = (start + n) % n` → advance by `maxPages` or by
  the actual scan count.
- **Severity**: low

### `bufmap.go:Lookup` — probe bound is off by one
- **Bug**: `for dist <= size` probes up to `size+1` buckets for a table of
  `size = mask+1` buckets. The extra probe wraps via `& mask` so it is not an
  out-of-bounds read, but a table saturated with tombstones is probed one extra
  time before giving up.
- **When it triggers**: pathological near-full table (should not happen at ≤50%
  load with compaction, but the bound is still wrong).
- **Fix**: use `dist < size`.
- **Severity**: low

### `bgwriter.go:Bgwriter.Stop` — double-close panic / hang on Stop-without-Start
- **Bug**: `Stop()` unconditionally `close(b.stop)` and blocks on `<-b.done`.
  Calling Stop twice panics (close of closed channel); calling Stop before
  Start() blocks forever (`done` is only closed by `run()`, which never ran).
- **When it triggers**: shutdown paths that call Stop twice, or Stop without a
  preceding Start.
- **Fix**: guard with `sync.Once`/started flag; make Stop safe/defined.
- **Severity**: low

### `aio/method_iouring_linux.go:pokeWake` — NOP written at `tail & mask` without checking SQ ring is not full
- **Bug**: `pokeWake` (Close path) writes the wake-up NOP SQE at
  `idx = tail & m.sqMask` with no check that the SQ ring has space
  (`tail-head < entries`). If Close races a fully-saturated ring (cap ==
  sqEntries in-flight ops whose SQEs the kernel has not yet consumed), the NOP
  overwrites a live, not-yet-consumed SQE, so that op's real I/O never runs
  (its CQE never arrives; only Close's drain loop finishing it with
  errEngineClosed hides the loss).
- **When it triggers**: engine Close while the submission queue is full —
  shutdown-only, bounded impact (pending ops get errEngineClosed).
- **Fix**: skip the poke (or block until the ring drains) when
  `(tail-head) >= entries`, or submit the NOP only after checking space.
- **Severity**: low

### `vm.go:PageAllVisible` / `PageAllFrozen` — plain XID comparison (`xmin >= horizon`) breaks under wraparound
- **Bug**: same modular-XID issue as prune.go; `t.Header.Xmin >= horizon`
  misjudges visibility/frozen-ness once the XID space wraps, potentially
  setting ALL_VISIBLE/ALL_FROZEN for pages whose tuples are actually newer than
  the horizon (an index-only scan could then skip a heap fetch for a tuple a
  snapshot should see). Conservative direction at the boundary, but wrong
  after wraparound.
- **When it triggers**: XID wraparound (same anti-wraparound caveat as the
  prune.go finding).
- **Fix**: use `XIDPrecedes`-based ordering for the horizon test.
- **Severity**: medium

### `freeze.go:PageFreezeOldTuples` — plain XID comparison (`xmin >= freezeBelow`) breaks under wraparound
- **Bug**: `xmin >= freezeBelow` uses plain unsigned comparison; after
  wraparound a numerically-small-but-actually-new xid is judged "old enough to
  freeze", rewriting it to FrozenTransactionID and making a tuple that newer
  snapshots should not see permanently visible. (Also: deleted-but-unpruned
  tuples skipped at the deleted branch are not reflected in
  `MinUnfrozenXID`, which can let relfrozenxid advance past a live referenced
  xid — secondary.)
- **When it triggers**: XID wraparound.
- **Fix**: use `XIDPrecedes`-based ordering for the freezeBelow test.
- **Severity**: medium

### `writeback.go:accountWrite` — `pendingBlocks.Store(0)` races concurrent `Add`
- **Bug**: after crossing the threshold, `pendingBlocks.Store(0)` can clobber an
  `Add(1)` from a concurrent writer on another goroutine (multiple backends can
  call `accountBackendWrite` concurrently), losing one count and delaying the
  next writeback hint. Purely statistical; no data hazard.
- **When it triggers**: concurrent dirty-victim flushes across backends with
  backend_flush_after set.
- **Fix**: reset via CAS loop (e.g. `CompareAndSwap` back to 0 only if the
  value is still the one observed), or accept the approximate cadence (it is a
  hint counter, not correctness-critical).
- **Severity**: low
