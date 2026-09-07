# E-19 / EX5-05 — a prefetch that actually populates the buffer pool

**Status:** design, for review · **Date:** 2026-09-07 · **Item:** `TODO_ALL.md`
E-19 (§7), filed 2026-09-07 at the owner's request under the same standing as
E-17 cut 2 and E-18 — *design doc + agent review + commit the design first,
then implement.*

**Verdict, after two adversarial reviews (§7) and Probe 0 (§5.0a):
the mechanism is WORTH BUILDING and is NOT built here.**

Probe 0 measured the recoverable budget on a cold, low-correlation,
larger-than-pool index fetch at **4,905.8 ms of a 6,139.3 ms query — 79.9%**,
with disjoint ranges and `read_bytes` proving the cold arms cold. That is
**28× the 3.3% fraction** the sequential case bounded, so `DESIGN.md`'s
outcome-(B) evidence constrains this item not at all. E-19's "no witness
exists" escape is therefore **closed**.

What blocks implementation here is cost, not merit: three prerequisites that do
not exist and eight reworks of paths the first draft wrongly called "unchanged"
(§5.9, §4.1a) — including a **deadlock** and a **dropped checksum verification**
that the reviews caught before any code was written. §5.9a sets the slice order.

One structural finding the next owner must not re-learn: **goopg's bench
corpora cannot score this change.** TPC-H SF=1 is 1.9 GiB inside a 2048 MB
buffer pool and all 15 bitmap witnesses are ≤400-row NLI inners (§3.3, §3.4), so
a correct implementation would measure exactly zero there. The A/B must be
Probe 0's instrument, not a suite median.

This is the **(A) branch** that [`DESIGN.md`](DESIGN.md) (accepted
2026-09-06, outcome (B) *remove*) declined to take. That document is not
withdrawn: its removal was correct for the code that existed. This one asks the
different question it deferred — *would a prefetch that installs into the pool
pay?* — and answers it with a design that can be measured, plus the go/no-go
arithmetic stated **before** the arm is run.

Predecessors: E-11 / EX5-04
(`analysis/executor-refactor/e11-depth-sweep-20260906/`), removal commit
`3fc3d88`, ledger rows `take3-E-11-prefetch-discards-buffer` and
`take3-E-11-readstream-declined`.

---

## 1. Where the tree stands today

`Pool.Prefetch`, `Pool.SetPrefetchEnabled`, `Pool.prefetchEnabled`,
`seqScanOp.refillPrefetchWindow` and the `GOOPG_SEQSCAN_LOOKAHEAD` knob are all
gone (`3fc3d88`). What survives is the seam the removal deliberately kept:

- `Manager.SetAIO` / `Manager.PrefetchBlock` / `AIOEngine` / `AIOHandle`
  (`internal/storage/smgr.go:181-243`) — a real asynchronous submit; an engine
  *is* attached in production (`io_method` BootVal `"worker"`,
  `internal/initdb/open.go:346`).
- `internal/storage/aio/read_stream.go` — a v0 read stream that is
  `File`+offset based and **returns bytes, not buffers**. Its own header says
  the bufmgr-aware `Buffer`-returning variant is a follow-up.
- The buffer pool's IO-in-progress machinery, which is the part that matters:
  `slotIOBit` (`bufpool.go:32`), `claimVictim`, `evictVictim`, `pinLoad`, and
  `pinSlow`'s parking of a concurrent `Pin` on `slotSema[idx]` until the loader
  clears the bit.

So there is **no buffer-pool prefetch of any kind** at HEAD, and no caller.
This item builds both or declines both.

## 2. Correction to the item's own PG citation — read it before anything else

E-19 cites "`bufmgr.c:1489` and `:1262` (the `PrefetchBuffer` /
`StartReadBuffer` path that DOES install)". Those two line numbers are right
but the name attached to them is not, and the difference is the whole design.

**PG's `PrefetchBuffer` does *not* install into shared buffers either.**
`PrefetchSharedBuffer` (`bufmgr.c:560-600`) looks the tag up, and *if it is
absent* calls `smgrprefetch(...)` — i.e. `posix_fadvise(WILLNEED)` — and returns
`{InvalidBuffer, initiated_io=true}`. No victim is claimed, no slot is filled.
Its contract comment (`:640-649`) says so outright: *"there is no way to know
if the data was already cached by the kernel … and no way to know when the I/O
completes other than using synchronous `ReadBuffer()`."*

That is **exactly the shape of the function goopg deleted**, and goopg's was
strictly worse only in that it burned an 8 KiB heap buffer and a per-block
latch to achieve what `fadvise` achieves with neither. The deleted code was not
a botched `StartReadBuffers`; it was a faithful, expensive `PrefetchBuffer`.

The *installing* path is a different mechanism with a different owner:
`StartReadBuffers` / `StartReadBuffersImpl` (`bufmgr.c:1489`, `:1262`) and
`WaitReadBuffers` (`:1632`). It fills a caller-supplied `Buffer *buffers` array
— the buffers are **pinned before submission** (`StartReadBuffersImpl`'s
`PinBufferForBlock` loop, `:1318-1325`, then `AsyncReadBuffers` at `:1408`) and
**the pins are held by the caller** (the `ReadBuffersOperation`) until it hands
them over. `ReadBuffer_common` itself drives this interface synchronously
(`:1251-1256`), so *every* plain `ReadBuffer` is a `StartReadBuffers` caller;
the precise claim is that the only driver of its **asynchronous, look-ahead**
use is `read_stream.c`, which owns the pins, the distance, and the release
cadence (`read_stream.c:983`, "Pin transferred to caller").

*[oracle review]* The contract comment quoted above is at `:632-648`, not
`:640-649`. `PrefetchBufferResult.recent_buffer` does not weaken the claim and
the source says so at `:600-604` — the buffer is *"not pinned and it must be
rechecked"*, the recheck being `ReadRecentBuffer` (`:682`), which pins and only
then re-validates the tag. `read_stream` did not replace the advice-shaped
callers either: `vacuumlazy.c:3408`, `heapam.c:8042`,
`xlogprefetcher.c:767`, `pg_prewarm.c:211` all still call `PrefetchBuffer`, and
every one is fire-and-forget. That strengthens §2 rather than weakening it.

**Consequence for this item, and it is the load-bearing one:** "a prefetch that
populates the buffer pool" is not a fire-and-forget hint with the buffer
plumbed in. In PG it is *a read stream holding pinned look-ahead buffers*.
There is no upstream design in which a background prefetch installs a buffer
that nobody pins and hopes the consumer arrives before the clock sweep does.
Any goopg design that does that is goopg-original, and the owner's standing
ruling on parallel costing (E-20) applies by analogy: prefer transcription.

*[oracle review — tried to refute this and could not]* The closest
counterexample is `xlogprefetcher.c`, and it is not one: it is *purely*
`PrefetchSharedBuffer` (`:767`), storing the unpinned must-recheck
`recent_buffer` (`:768-772`) for `XLogReadBufferExtended` to launder through
`ReadRecentBuffer` (`xlogutils.c:471-476`), and bare `posix_fadvise` on a miss
(`:774-778`). Zero installation, zero pins. Its own header concedes the
mechanism only works *"on systems where PrefetchBuffer() does something useful
(mainly Linux)"* (`:21-23`).

The review also surfaced a mechanism §4.2 must transcribe: the AIO subsystem
takes its **own second pin** at stage time — `buffer_stage_common`,
`bufmgr.c:6852-6853`, `buf_state += BUF_REFCOUNT_ONE; buf_hdr->io_wref =
io_ref;` with the comment *"this backend could error out while AIO is still in
progress, releasing all the pins by the backend itself. This pin is released
again in `TerminateBufferIO()`"* (plus `ResourceOwnerForgetBufferIO` at
`:6885`). So even when the issuer abandons the operation, a pin exists for the
whole IO. That is the upstream answer to hazard 1 and it is stronger than the
`defer` §4.2 originally proposed.

## 3. Which caller — sequential is refuted, bitmap is the live candidate

### 3.0 Correction: `READ_STREAM_SEQUENTIAL` does NOT mean "no look-ahead"

*[oracle review, finding 1 — the largest correction in this document, and it
also corrects the accepted `DESIGN.md` §3.1.]*

The first draft of §3.1/§3.2 rested on a seq-vs-bitmap dichotomy read off the
`READ_STREAM_SEQUENTIAL` / `READ_STREAM_DEFAULT` flag words. **That dichotomy
does not exist under PG 18.3's default configuration.** The gate is a four-term
`AND` and only one term was transcribed (`read_stream.c:658`, `:669-673`):

```c
stream->sync_mode = io_method == IOMETHOD_SYNC;
...
if (stream->sync_mode &&
    (io_direct_flags & IO_DIRECT_DATA) == 0 &&
    (flags & READ_STREAM_SEQUENTIAL) == 0 &&
    max_ios > 0)
        stream->advice_enabled = true;
```

`DEFAULT_IO_METHOD` is `IOMETHOD_WORKER` (`aio.h:42`, wired at
`guc_tables.c:5436`), so `sync_mode` is **false** out of the box and
`advice_enabled` is false for *every* stream, bitmap included.
`READ_STREAM_SEQUENTIAL` is referenced at exactly one site in the whole tree —
that gate — so under the default `io_method` the flag is a no-op.

What the flag actually suppresses is `posix_fadvise`-based **pseudo-AIO**, used
only when real AIO is off. Under worker AIO, PG 18.3 gives the sequential heap
scan the *full installing look-ahead*: pinned buffers, a distance ramp, and
read combining — the same machinery as the bitmap scan.

Three consequences, all binding:

1. `DESIGN.md` §3.1's "upstream has already decided this exact question" is
   **wrong as an argument about look-ahead**. It is right only about *advice*,
   and only in the `io_method=sync` fallback. The removal it justified is still
   correct — but on the measurement (§3.1 below), not on upstream authority.
2. §3.2's "the caller upstream *does* prefetch for" carries no upstream
   authority either. Bitmap remains the right target here, but on
   goopg-internal grounds (known block list, random access), not by citation.
3. goopg's own `io_method` BootVal is `"worker"`
   (`internal/utils/misc/defaults.go:683`), i.e. goopg sits on the *same* side
   of this branch as default PG. So the thing to transcribe is PG's **AIO**
   path, not its advice path. That is what §4 does, and it should say so.

### 3.1 Sequential scan: closed on measurement, not on upstream authority

`heapam.c:1220` opens the seq-scan / TID-range stream with
`READ_STREAM_SEQUENTIAL`, and `read_stream.h:30-36` gives the reason —
*"Explicit advice is known to perform worse than letting the kernel (at least
Linux) detect sequential access."* Per §3.0 that governs `fadvise` advice only.
goopg's data files are plain buffered (`smgr.go:616`, no `O_DIRECT` in the
tree), so Linux readahead is active, which is the workload half of the same
argument and does survive.

The measurement is what closes it, on a **cold** page cache: `DESIGN.md` §3.2
measured
a 919 MB single-table `count(*)` at 128 MB `shared_buffers` with the relation
`fadvise(DONTNEED)`-ed and `/proc/<pid>/io` `read_bytes` confirming every block
came from the device. The cold-vs-warm delta was **0.175 s on a 5.364 s scan —
3.3%**. That number is the *entire budget a perfect prefetcher could recover on
this access pattern*, and it sits below the arm's own repetition spread
(3.6-7.2%).

There is no sequential design here. Recorded as closed, not re-measured.

### 3.2 Bitmap heap scan: the live candidate, on goopg-internal grounds

`heapam.c:1231` opens the bitmap stream with `READ_STREAM_DEFAULT`. Per §3.0
that does **not** distinguish it from the sequential stream under the default
`io_method`; the earlier draft's claim that it does is withdrawn. What selects
bitmap here is `DESIGN.md` §4.2's own stated reopening condition — "a
random-access buffer-pool caller — bitmap heap scan, or an index scan following
a low-correlation index" — plus the structural facts below. It is the regime
kernel readahead cannot serve, which is the regime §3.1's measurement does not
cover.

*[oracle review]* PG 18.3 has **deleted** the old bitmap prefetch machinery
entirely: `prefetch_target`, `BitmapPrefetch`, `prefetch_iterator`,
`prefetch_pages` do not appear in `nodeBitmapHeapscan.c`, `execnodes.h`,
`heapam_handler.c` or `heapam.c`. `nodeBitmapHeapscan.c` is 594 lines and
contains no occurrence of "prefetch", "read_stream" or "ReadStream" — the
executor node no longer touches I/O at all. Everything is the one
`read_stream_begin_relation` at `heapam.c:1228-1238`, with the callback
`bitmapheap_stream_read_next` at `:313-346` doing nothing but `tbm_iterate`
plus an end-of-relation skip. Two things follow that §4 must absorb:

- The stream carries `sizeof(TBMIterateResult)` of **per-buffer data**.
  Look-ahead runs the iterator ahead of the consumer, so each block's offset
  list has to travel *with* the buffer. §3.2's "the next K block numbers are
  already known" is true about blocks and silent about offsets;
  `bitmapHeapScanOp` needs the same per-slot side-channel and it is not in
  §4.1's API. Added to §4.1.
- Depth is **not** a constant. Distance starts at 1, **doubles after each I/O
  actually waited on** (`read_stream.c:929-932`, capped at
  `max_pinned_buffers`), decays by 1 on a hit (`:361-363`), and collapses at
  end of stream. A fixed-`D` window is goopg-original. And the ramp
  independently supports §3.3's pessimism: over a 400-row median bitmap the
  distance never gets near its cap, so upstream's own controller would barely
  look ahead on goopg's witnesses.

goopg has the operator: `bitmapHeapScanOp`
(`internal/executor/operators_bitmap.go:261`), and it is structurally the
easiest possible prefetch site — easier than PG's, because goopg materialises
the whole `TIDBitmap` before the first fetch (`nextSerial` builds it lazily on
the first `Next`, then iterates `tbmBeginIterate`). **At the moment block B is
pinned, the next K block numbers are already known exactly**, with no callback,
no ramp-up heuristic and no speculation. *[source review — CONFIRMED:
`buildBitmap` runs the whole `RangeScanWithPos` then `tbmLossify` and returns
(`operators_bitmap.go:122-166`); `tbmBeginIterate` materialises **and sorts**
the complete block list up front (`tidbitmap.go:308-320`).]*

The scan pins exactly one page at a time — *[source review — CONFIRMED: only
two `Pool.Pin` sites, `operators_bitmap.go:525` serial and `:618` parallel,
each preceded by `releasePinned`; `fetchExact`, `fetchOneTuple`,
`nextLossyTuple`, `nextParallelTuple`, `Rescan`, `BindOuter` and `Close` add
none]* — so the pin accounting a look-ahead window adds is visible and bounded
by construction.

*[source review — the parallel half of this paragraph was WRONG and is
withdrawn.]* The first draft claimed a per-worker window is "equally
well-defined" because the parallel path iterates the same sorted list.
`parallelBitmapState.nextPage` hands out **exactly one index per call** via
`nextIdx.Add(1)` (`parallel_bitmap_scan.go:71-82`). The list is sorted
(`:55-62`), so the *global* order is physical — but a worker cannot see its
next K blocks without claiming them, and under contention worker *i* receives
indices *i, i+N, i+2N…*, interleaved with every other worker's. A per-worker
window therefore requires a **new batched-claim API**, which changes the
allocation granularity the code deliberately chose ("the same granularity as
the serial iterator, which keeps the pin/release cadence identical", `:68-70`)
and so requires re-justifying the work distribution. E-19 obstacle 1 is
narrower than it was for seq scan, but it is not absent.

### 3.3 Witness census — done before designing, because it decides the item

goopg's own pinned plan capture at HEAD
(`plan_snapshots/c20a-c06s-plancost-rows-20260907.txt`, TPC-H 22 queries —
*[source review: the first draft wrote `plan_snapshots/*.txt`, which is
misleading; the directory holds ~40 captures at various HEADs and all 15 nodes
come from this one file]*) contains **15 `Bitmap Heap Scan` nodes**; PG's
reference captures use
one in 7 of 22 TPC-H queries and 7 of 99 TPC-DS queries. So the shape is
reachable and chosen — it is not a hypothetical.

Their **size**, however, is the problem, and it must be stated up front. From
`plan_snapshots/c20a-c06s-plancost-rows-20260907.txt`, the estimated row counts
on those 15 nodes are 4, 400, 400, 80, 400, 29, 6000, 400, 80, 400, 80, 1, 29,
400, 400. The largest is a 6000-row `customer` fetch; the median is 400. At
TPC-H SF=1 a 400-row bitmap fetch touches a few hundred heap pages at most.

**This is the sizing caveat E-19 already flags, now with numbers**: even if a
correct prefetch hid *100%* of the I/O on *every one of the 15 nodes*, the
recoverable budget is bounded by the total device time those fetches spend, and
at SF=1 the whole TPC-H working set is 1.9 GiB against ~19 GiB of host page
cache. The design therefore does **not** assume a win. It specifies the
mechanism, and it specifies the measurement that decides.

*[source review — and the sizes understate it; the **positions** are what
decide.]* The row counts were confirmed verbatim (lines 24, 33, 47, 49, 103,
161, 170, 233, 235, 243, 245, 312, 317, 360, 393). What the first draft did not
say is that all 15 sit in 7 queries (Q2, Q5, Q8, Q11, Q17, Q20, Q21) and
**every one is a child of a `Nested Loop`, an `InitPlan` or a `SubPlan`** —
none is a top-level scan and **none is under a `Gather`**, so the parallel
bitmap path has *zero* witnesses in the corpus:

- `BitmapHeapScan` is an accepted NLI inner (`executor.go:230-236`), rescanned
  **per outer row** (`operators_nljoin.go:323`). A look-ahead window over a
  4-to-400-row bitmap would be built and drained once per outer row.
- Q17's node (line 312, `rows=1`, `Recheck Cond: (l_partkey = $0)`) is a
  one-row bitmap inside `SubPlan 1`, re-executed per outer row. Look-ahead
  there is pure overhead.
- The plain-`Nested Loop` cases sit behind a materialising inner cache
  (`join_nl_stream.go:242`, `:272`, `drainInner`), so the bitmap executes
  **once**, over a handful of pages that are then hot.

Also: the capture is `EXPLAIN`, not `EXPLAIN ANALYZE`, so **execution and loop
counts are not established** by it — a `SubPlan` may never fire. So "the shape
is reachable and chosen" stands; "structurally the easiest possible prefetch
site" does **not**. These sites are per-row-rescanned micro-bitmaps over pages
that are resident after the first outer row.

**And there is no goopg-side TPC-DS census at all.** `bench/tpcds/` holds only
`plans-pg/`; the goopg TPC-DS analysis directories contain zero `Bitmap Heap
Scan` matches. Whether goopg picks a bitmap scan on TPC-DS is **UNVERIFIABLE at
HEAD**, and §5's witness plan silently inherited that gap. PG picks one in 7 of
99 TPC-DS queries, so the question is live and a capture is owed before any
TPC-DS claim is made either way.

### 3.4 The bench cluster cannot host this witness at all — checked, not assumed

The item's own instruction is to check what the arm's cluster is set to before
attributing anything to prefetch. Doing so settles more than it was meant to:

| | |
| :-- | :-- |
| TPC-H goopg cluster `shared_buffers` | **2048 MB** (`bench/tpch/setup_goopg.sh:55`, `bench/tpch/runtime_goopg/data/postgresql.conf:416`) |
| TPC-H SF=1 on-disk size | **1.9 GiB** (`bench/tpch/runtime_goopg/data/base`) |
| TPC-DS SF=0.5 gate cluster `shared_buffers` | 2048 MB (`.../data-sf05/postgresql.conf:390`) |

**The entire TPC-H SF=1 dataset fits inside the buffer pool.** After first
touch, every heap page a bitmap scan wants is a pool hit — `StartRead`'s first
action (`bm.Lookup`) returns `alreadyValid`, and the mechanism does nothing at
all. This is not the 128 MB-vs-2 GB unfairness recorded for the earlier TPC-DS
runs; it is the opposite, and it makes the *bench corpus itself* structurally
blind to any buffer-pool prefetch after the first pass.

That is not a reason to skip the arm — it is the reason the arm must be a
**cold, first-touch** one on a relation larger than the pool, exactly as
`DESIGN.md` §3.2 constructed for the sequential case. But it does mean the
honest reading of "a TPC-H suite A/B shows nothing" is *the corpus cannot see
it*, not *the mechanism does not work* — and, symmetrically, that **no
production-shaped TPC-H win is available here regardless of the mechanism's
quality.**

## 4. The mechanism, on goopg's actual machinery

### 4.1 Shape: `StartReadBuffers`-shaped, pins held by the scan

Following §2, the design is **not** a `Pool.Prefetch(tag)` hint. It is:

```
// StartRead begins loading tag into a pool slot and returns a PINNED slot
// whose contents are not yet valid. The caller MUST call FinishRead (or
// Unpin) on the returned slot exactly once.
func (p *Pool) StartRead(tag BufferTag) (*Slot, bool /*alreadyValid*/, error)

// FinishRead blocks until the slot's in-flight read completes and the slot
// is valid, then returns it still pinned. Idempotent for an already-valid
// slot.
func (p *Pool) FinishRead(s *Slot) error
```

*[oracle review — the single biggest omission]* **This per-block API throws
away the mechanism that actually pays upstream.** `read_stream.c:474-477`
merges consecutive callback blocks into one pending read and flushes at
`io_combine_limit` (`:445-450`); `StartReadBuffersImpl` pins the whole run and
issues **one vectored `preadv`** for it (`bufmgr.c:1318-1325`, `:1408`,
`io_pages[MAX_IO_COMBINE_LIMIT]` at `:1777`), clamped at segment boundaries by
`smgrmaxcombine` (`:1375`). `DEFAULT_IO_COMBINE_LIMIT` is 128 kB / `BLCKSZ` =
**16 blocks** (`bufmgr.h:166`). And the two are multiplicative:
`max_pinned_buffers = (max_ios + 1) * io_combine_limit`
(`read_stream.c:593-594`) — depth alone is the weaker half.

A TID bitmap iterated in block order produces exactly the adjacent runs
combining was built for. A per-block `StartRead` turns one 128 kB read into
16 × 8 kB reads and keeps only the concurrency win, which on a 1.5-1.7 GB/s
device (§5) is plausibly the smaller term. **The API therefore becomes
run-based** —

```
// StartReadRun begins loading a run of up to io_combine_limit CONSECUTIVE
// blocks into pinned pool slots with one submission.
func (p *Pool) StartReadRun(rel RelFileNode, first BlockNumber, n int) (*ReadOp, error)
```

— and goopg's `Manager` needs a vectored counterpart to `PrefetchBlock`
(`preadv` into the N slot pages) before this is worth building at all.

**That prerequisite does not exist and is not small.** goopg's AIO layer is
single-buffer to the floor: `AIOSubmitOp` (`smgr.go:38`) carries one `Buffer
[]byte`, `aio.Op` likewise, and there is **no `preadv`/`readv`/`iovec` anywhere
in `internal/storage/`** — `method_iouring_linux.go:43` says so in as many
words ("so we don't need `IORING_OP_READV/WRITEV`"). Read combining therefore
means a vectored op threaded through `Manager`, `AIOEngine`, and all three
methods (`method_sync`, `method_worker`, `method_iouring_linux`), each with its
own partial-read and completion semantics. It is a slice in its own right, and
it is a **prerequisite** for the half of the upstream mechanism that plausibly
pays. This is added to the cost of the item, and it is the main reason §5.0's
budget probe must run before any of it is written.

`StartRead` is `pinLoad` with the synchronous `p.mgr.ReadBlock` replaced by
`p.mgr.PrefetchBlock(tag.Rel, tag.Block, s.page)` — reading **into the slot's
own page**, which is the entire point and the exact thing the deleted function
failed to do — and with the "transition to valid + wake waiters" tail moved
into a completion goroutine that waits the returned `AIOHandle`.

Three things genuinely are reused unchanged, and they are the ones that make
the concurrency argument work:

- `claimVictim` sets `slotIOBit` and bumps the generation, so **no concurrent
  `Pin` can adopt the slot as valid** while the read is in flight
  (`tryPinSlot` rejects on `stateIO`, `bufpool.go:1455`).
- A concurrent `Pin(tag)` finds the tag in the bufmap with the IO bit set and
  parks on `slotSema[idx]` (`pinSlow`, `:1919-1932`) — it does **not** issue a
  second read. This is the direct answer to E-19 obstacle 2: the second reader
  blocks on *slot validity*, not on `lockBlock`, and never re-reads.
- `Manager.PrefetchBlock`'s `lockBlock(blk)` latch is released by the engine's
  `OnComplete` (`smgr.go:239`). It serialises against a concurrent synchronous
  `readBlock`/`writeBlock` of the same block, which is what it is for.
- `s.page` is stable for the whole async read: assigned once in `NewPool`
  (`:863`) as a fixed slice into a never-reallocated arena
  (`internal/storage/arena.go:18-36`).

### 4.1a *[source review]* "Everything else is reused unchanged" is FALSE — six independent breaks

The falsification pass took the one-line substitution above at its word and
broke it six times. Each is a real edit, not a caveat, and together they are
most of the reason §8 lands where it does.

1. ~~**The substitution silently drops checksum verification.**~~
   **REFUTED 2026-09-07, on the way to S1 — and the refutation found a live
   shipped bug.** `relFile` implements `ReadAt` (`smgr.go:800-810`) and *that*
   verifies: `if err == nil && r.checksums && n == BlockSize && off%BlockSize
   == 0 { verifyOnRead(...) }`, under `r.mu`. `runOp` dispatches `DirRead` to
   `op.File.ReadAt` (`aio/aio.go:699-702`), and `aioFileAdapter.ReadAt`
   forwards to it (`initdb/open.go:2729-2731`). So on both the `sync` and
   `worker` methods — i.e. the production default — `PrefetchBlock` **is**
   checksum-verified, and `StartRead` inherits that for free. The reviewer
   compared against `readBlock` and missed the `ReadAt` sibling.
   **But** the `io_uring` method's raw-fd path bypasses `ReadAt`/`WriteAt`
   entirely and takes its checksum behaviour from `aio.ChecksumFile`
   (`method_iouring_linux.go:391`, `:591-592`), which it asserts on `op.File`
   — the *adapter*, which did not implement it. That hole is not
   E-19's: it was already live on the **write** path
   (`Manager.WriteBlockAIO` ← `bufpool.go:2635`), stamping no checksum at all
   on an `io_method = io_uring` cluster. Fixed as its own commit with its own
   tests; ledger row `aio-adapter-lost-checksumfile`. The original text is
   struck rather than deleted because the wrong finding is what found the real
   one.
   *(historical text)* **The substitution silently drops checksum
   verification.**
   `Manager.ReadBlock` → `relFile.readBlock`, whose *last statement* is
   `return r.verifyOnRead(blk, buf)` (`smgr.go:888`, definition `:706-720`),
   preceded by the short-read check and `recordIOTrace` / `PageIdentityObserve`
   (`:885-887`), all under `r.mu`. `PrefetchBlock` with an engine attached goes
   `eng.Submit` → `runOp` → `op.File.ReadAt` (`aio/aio.go:699-709`) and touches
   **none** of them. Every prefetched page would enter the pool
   **unchecksummed** on a cluster with `DataChecksumVersion != 0`
   (`initdb/open.go:300-303`). Worse for detection: `PrefetchBlock`'s
   *engine-less* fallback does call `readBlock` and does verify, so a test run
   without an AIO engine never sees the divergence. The completion path needs
   its own verify-and-publish step.
2. **A blocking `Submit` under `pinMu`, completing on a 3-goroutine pool — a
   closed cycle.** `pinLoad` is called *with `pinMu` held* (`:1954`, from
   `pinSlow`'s `defer`). `methodWorker.Submit` **blocks** when its queue is
   full, by design (`aio/method_worker.go:6-9`), with capacity
   `io_workers*4` = **12** by default (`:29-33`, `io_workers` BootVal 3,
   `defaults.go:688-694`). The completion callback runs *on the worker
   goroutine* (`aio.go:201-209`), and the design's completion work must take
   `pinMu` (it must read `slotWaiters[idx]` under `pinMu` before clearing the
   IO bit, `:2016-2018`). So a scan holding `pinMu` blocks in `Submit` while
   the three workers that would drain the queue are executing completions that
   want `pinMu`. **Deadlock at default GUCs with a depth-4 window and two
   concurrent scans.** This class is absent from §4.2 entirely; submission must
   move outside `pinMu`, or completion must not need it.
3. **The state machine forbids returning a pinned slot.** The publish is an
   absolute `Store`, not a CAS or merge:
   `newSt := slotValidBit | uint64(1) | (1<<slotUsageShift) | (gen<<slotGenShift);
   s.state.Store(newSt)` (`:2020-2022`) — pin is **hard-coded to 1**. Any pin
   taken at `StartRead` is clobbered or double-counted. The *encoding* permits
   `pin>0` with `slotIOBit` (independent bits, and every reader handles it);
   the **publish path** does not.
4. **The documented escape hatch panics.** "`FinishRead` (or `Unpin`) exactly
   once" is unimplementable: `Unpin` panics on pin 0 ("unpin underflow",
   `:2086-2092`), and a slot between `claimVictim` and publish has state
   `slotIOBit | gen`, pin exactly 0 (`:1534`).
5. **The error path recycles the slot out from under the caller.** On `ioErr`,
   `pinLoad` calls `releaseVictimSlot`, which does `s.state.Store(0)` (`:1483`)
   — wiping the generation — after `bmDelete`. A `*Slot` already handed to the
   scan is now a free slot another backend can claim for a different tag; a
   later `FinishRead`/`Unpin` corrupts someone else's pin count. **§4.2's
   pin-balance test would pass while this is broken**: counts balance,
   ownership does not.
6. **`StartRead` performs a synchronous dirty-page writeback before it submits
   anything.** `pinLoad`'s second step is `evictVictim` (`:1962-1966`), and for
   a dirty victim that is an inline `flushSlot` — a WAL flush plus a `pwrite`
   on the calling goroutine (`:1576-1589`). A depth-`D` window front-loads up
   to **`D` synchronous writebacks before a single read is in flight.** §5's
   arm has no term for this, and it makes §4.2's "eviction delta ≤ D" a *lower*
   bound on the harm rather than a bound on it.

Two more, outside the six:

- **Invalidation skips IO-in-progress slots.** `InvalidateRel` (`:1381-1400`)
  and `InvalidateBlock` (`:1405-1418`) both bail on `!stateValid(st)`, and a
  slot with `slotIOBit` is not valid — so it is skipped and its bufmap entry
  survives. Under synchronous `pinLoad` the exposure is one read; under a
  scan-controlled `D`-deep async window it is arbitrarily long, and the
  completion goroutine would publish a **valid slot for a block a concurrent
  `TruncateRelationTail` (`:1362-1379`) has already removed.**
- **A dormant data race becomes hot.** `PrefetchBlock` reads
  `if blk >= f.nblocks` **without** `r.mu` (`smgr.go:212`), while
  `relFile.extend` writes `r.nblocks++` under `r.mu` (`:914`, `:926`).
  `ReadBlock` is safe because `readBlock` takes `r.mu` (`:873`). The race is
  dormant only because `PrefetchBlock` has no production caller; this design
  puts it on every buffer-pool miss.

Also unflagged in the first draft: `pinLoad` fires `OnBlockReload(tag, s.page)`
under `contentMu` after a successful read (`:2001-2003`) — arbitrary
catalog-reload code that this design would migrate onto an AIO worker
goroutine.

### 4.2 The three accounting hazards, and the answer to each

These are the surfaces a values suite cannot see. Each gets a test, not a
paragraph.

1. **Leak — a prefetch that pins and never unpins.** The window is owned by
   `bitmapHeapScanOp`. The window becomes a small ring of at most `D` slots,
   drained at end of scan, at `Close`, and on the error return of every `Next`
   path. Pin/unpin balance is asserted directly: a test scans a bitmap to EOF,
   to a mid-scan `Close`, and to an injected read error, and asserts
   `sum(statePin)` over the pool returns to its pre-scan value in all three.
   *[source review — three corrections.]* (i) The first draft said the operator
   "already has exactly one release point (`releasePinned`) and one lifecycle
   end". `releasePinned` (`operators_bitmap.go:461-469`) is a **per-page**
   operation, called on *every* page transition (`:523` serial, `:613`
   parallel) as well as from `Rescan` (`:375`), EOF (`:515`, `:606`) and
   `Close` (`:437`) — draining the ring there would unwind the look-ahead at
   every page boundary and the mechanism would never be a window. **The drain
   point has to be a new one**, which is a structural change to the operator
   the "already exists" framing hid. (ii) `Rescan` (`:373-380`) resets
   `tbm`/`iter`/`ownBitmap` and the pin but **not** `inLossyPage`, `lossyOff`,
   `parOffsets`, `parOff`, `pageBlock` — harmless today only because
   `nextLossyTuple` early-returns on `o.pinned == nil` (`:836-838`); a window
   adds more state to the same gap. (iii) `fetchExact` skips a
   dead/invisible/filtered tuple by **recursing into `o.Next()`** (`:743`,
   `:747`, `:753`, `:764`, `:774`, `:788`), so any refill placed in `Next` runs
   re-entrantly, once per skipped tuple.
   And the test itself is not writable against today's API: `getPinCount` is
   unexported (`bufpool.go:112`) and the only exported readers are
   `SlotPinCount(tag)` (`:2072`) and `Capacity()` (`:2033`), so a test in
   `internal/executor` cannot compute the sum — a new accessor is required.
   There is also **no fault-injection seam on the read path** (`OnPinWait` /
   `OnPinDone`, `:418-425`, are timing hooks with no error return), so the
   injected-error arm needs a fake `Manager` or a deliberately truncated file.
2. **Regression — a prefetch that evicts a live buffer.** `StartRead` calls
   `claimVictim`, so a deep window *is* eviction pressure.
   *[oracle review — two corrections.]* First, **PG's pin limit is not an
   eviction control**: it bounds pins, and nothing in that machinery bounds
   eviction. So "PG bounds this explicitly" was wrong; the eviction-delta test
   below is a goopg invention and is labelled as one. Second, PG does **not**
   decline. `read_stream.c:325-341` shrinks distance to
   `pinned_buffers + buffer_limit`, returns false *only if something is
   already pinned to hand the consumer*, and otherwise performs a **forced
   short read**; `:320-321` floors `buffer_limit` at 1 when nothing is pinned,
   *"guarantee progress"*. The design adopts that shape: shrink, and floor at
   one block — a window that can decline to zero with nothing pinned can
   starve. Also: `LimitAdditionalPins` (`bufmgr.c:2543`) is the **relation
   extension** path; the read path uses `GetAdditionalPinLimit` (`:2517-2533`),
   over `MaxProportionalPins = NBuffers / (MaxBackends + NUM_AUXILIARY_PROCS)`
   (`:4019`), minus a deliberate over-estimate, and it may return 0.
   The goopg-invented guard stands: a test asserts that a scan with a window of
   `D` never raises `sharedEvictionCount` above the same scan with `D=0` by
   more than `D`.
3. **Wrong answers — a race with the pin path.** Covered structurally by §4.1
   (IO bit + generation + semaphore), but structure is not evidence: the whole
   feature is developed and gated under `-race`, and the balance test above is
   run with N concurrent scanners of overlapping bitmaps on one pool.

Additionally, the completion goroutine must clear `slotIOBit` and wake waiters
**on every exit including error and panic** — the E-09b lesson (a `sync.Once`
that publishes on return would leave waiters parked forever). An explicit
`defer` that publishes either valid-or-failed, never neither. *[oracle review]*
A `defer` is the weaker form of PG's answer; transcribe the stronger one — the
IO subsystem's own second pin (`buffer_stage_common`, `bufmgr.c:6852-6853`),
which keeps the slot alive across an abandoned operation regardless of what the
issuer's unwinding does. In goopg terms the completion closure holds its own
reference to the slot, released only by the publish step.

### 4.3a Knob naming

*[oracle review]* The first draft called the depth knob
`effective_io_concurrency`. That is wrong: in PG 18.3 `effective_io_concurrency`
is `max_ios`, the number of **concurrent I/Os** (`read_stream.c:565-571`,
default 16, `bufmgr.h:160`), and pinned depth is *derived* —
`(16+1) * 16 = 272` buffers by default. A goopg knob meaning "D blocks of
look-ahead" is a different quantity and must not borrow that name.

### 4.3 Default: OFF, with the knob living with the mechanism

The window depth is a GUC-shaped knob (see §4.3a for what it must *not* be
called; goopg may start with an env instrument as E-11 did) and **defaults to
0 — the mechanism inert — until an arm justifies otherwise.** `DESIGN.md`
§4.1's rule stands in the other direction too: a knob that can resurrect a
harmful path is not left on by default on the strength of a design document.

## 5. The measurement, with the verdict criteria fixed in advance

Stating these before the arm is what makes the result a result.

### 5.0 Probe 0 — the budget probe, which runs BEFORE any code is written

`DESIGN.md` §3.2 reading 2 is the template and it does not need the mechanism
to exist: measure the **cold-minus-warm delta** of the witness shape. That
delta *is* the whole budget a perfect prefetcher could recover; a prefetcher
cannot recover time the machine does not spend.

Probe 0: a bitmap heap scan over a relation **larger than `shared_buffers`**
with a genuinely low-correlation index (so the block list is scattered and
kernel readahead cannot help — the one regime §3.1 does not cover), timed cold
(`fadvise(DONTNEED)`, `read_bytes` verified) and warm, 3 reps each, fresh
capped server per arm.

- If `cold − warm` is inside the arm's own repetition spread, **the witness
  does not exist on this host** and E-19 closes NO-GO/out-of-scope on that
  evidence, with no buffer-pool change written. This is the outcome §3.3 and
  §3.4 jointly predict.
- If `cold − warm` is materially larger than the spread, the budget is real,
  the mechanism is worth building, and §5's full arm decides how much of the
  budget it actually recovers.

Probe 0 is cheap, has no correctness surface, and is the only step that can
turn "we did not measure a gain" into "there was no gain available". It is not
optional.

### 5.0a Probe 0 RESULT — the budget is real and it is **80% of the query**

**Run 2026-09-07. This inverts the outcome §3.3/§3.4 and both reviews
predicted, and it is recorded as such rather than smoothed over.**

Instrument: private cluster at `tmp/e19-probe/data`, port **5537**,
`GOOPG_CG_UNIT=e19-probe`, started only ever through `scripts/goopg-test-run.sh`
(`GOMEMLIMIT=8GiB GOGC=100`). No peer datadir was touched.
`shared_buffers = 128MB` (16,384 slots, logged at startup) against a
**774 MB** `base/` — so the working set cannot be pool-resident.
`max_parallel_workers_per_gather = 0` (E-11's five-way-A/A trap).
Table `p (k int, r int, pad char(40))`, 12,000,000 rows, `r` uniform random and
therefore **uncorrelated with physical order**; btree index on `r`.

Witness query, serial: `SELECT count(k) FROM p WHERE r BETWEEN 1000000 AND
3000000` → 239,590 rows. Chosen plan, **identical in every arm**:

```
Aggregate
  ->  Index Scan using p_r_idx on p   (rows=25505 est)
        Index Cond: (r >= 1000000 AND r <= 3000000)
```

This is the *other* shape `DESIGN.md` §4.2 names as the reopening case — "an
index scan following a low-correlation index". A bitmap plan was not reachable
here (`enable_indexscan = off` did not dislodge the index scan), so the probe
measures the index-scan half of the random-access regime. The heap-fetch access
pattern — scattered single-block `Pin`s driven by index order — is the same one
the bitmap scan produces, which is what the probe is about.

Method per arm: fresh capped server (**server age 0 s in every arm**);
`ANALYZE p` and the timed query in **one psql session** (goopg's stats are
per-connection); the page-cache state re-applied *after* `ANALYZE` via a `\!`
escape so `ANALYZE`'s own sampling reads cannot warm the arm; cold =
`posix_fadvise(DONTNEED)` over the whole datadir, warm = every file read to
`/dev/null`. Arm order permuted (cold, warm, warm, cold, cold, warm).

| arm | rep 1 | rep 2 | rep 3 | median | server `read_bytes` delta |
| :-- | --: | --: | --: | --: | --: |
| **cold** | 6139.3 ms | 5777.4 ms | 6507.6 ms | **6139.3 ms** | 1,072,701,440 / 1,072,701,440 / 1,072,799,744 |
| **warm** | 1187.8 ms | 1233.5 ms | 1759.9 ms | **1233.5 ms** | 0 / 0 / 55,300,096 |

`read_bytes` is the proof the cold arms were cold: **1.07 GB read from the
block device** in every one, and **0** in two of three warm arms.

Three readings:

1. **The recoverable budget is 4,905.8 ms of a 6,139.3 ms query — 79.9%.**
   Compare `DESIGN.md` §3.2's sequential figure: **0.175 s of 5.364 s, 3.3%**.
   The two regimes differ by a factor of **28** in the fraction of the query
   that is I/O wait. §3.1's refutation of sequential prefetch says nothing
   whatever about this one, exactly as E-19 suspected.
2. **The ranges are disjoint by a wide margin.** The *worst* cold arm
   (5,777 ms) is 3.3× the *worst* warm arm (1,760 ms). No noise band explains
   this; cold spread is 12.6% and warm 48% (rep 3 leaked 55 MB of real reads),
   and the effect is an order of magnitude larger than either.
3. **The regime is latency-bound, not bandwidth-bound — which is precisely
   what a prefetcher fixes.** 1.07 GB in 4.91 s is an effective **216 MB/s**,
   against the 1.5-1.7 GB/s this same host sustains on a sequential `dd` of the
   same size (`DESIGN.md` §3.3). At 8 KiB per read that is ~130,900 reads at
   **~37.5 µs each, issued one at a time and waited on**. Kernel readahead
   cannot help — the block order is index order — and goopg's own AIO engine
   already runs **3 workers with a 12-deep queue** (logged:
   `aio engine attached method=worker workers=3 max_concurrency=0`), so the
   overlap a correct `StartRead` window would buy is available and unused.
   (Note also 130,900 reads against an 87,500-page heap: the 16,384-slot pool
   thrashes, so pages are fetched more than once — a second, independent
   argument that the pool-installing half of the design matters.)

**Verdict on Probe 0: the witness EXISTS.** §5.9's step 2 ("close NO-GO on the
absence of a budget") is therefore **not** available, and its step 3 is the live
branch. What §3.3/§3.4 established is narrower than it looked and still stands:
the **corpora** cannot witness this — TPC-H's 1.9 GiB sits inside a 2048 MB
pool and every bitmap node is a ≤400-row NLI inner. So the honest joint reading
is: **the mechanism has a large, measured, real-workload-shaped budget in the
random-access, larger-than-pool regime, and goopg's bench corpora are simply
not that regime.** A gate built on TPC-H/TPC-DS medians would score a correct
implementation at zero — which is a fact about the gate, not the change.

**Arm.** Private clone of the TPC-H data dir, a 55xx port, private
`GOOPG_CG_UNIT`, fresh memory-capped server per arm through
`scripts/goopg-test-run.sh` (server age 0 s in every arm — the sweep-tail
collapse trap). One binary, depth switched by knob, never rebuilt per arm.
Arm order permuted across reps.

**`shared_buffers` is recorded, not assumed.** The TPC-DS clusters ran at
128 MB against PG's 2 GB and that alone produced 43,138 evictions on two
`store_sales` scans. Any eviction figure in the result is meaningless without
the setting beside it.

**Cold arm is mandatory and comes first.** `posix_fadvise(DONTNEED)` over the
relation files, verified by `/proc/<pid>/io` `read_bytes` matching the file
size (the technique `DESIGN.md` §3.2 established; `drop_caches` needs root,
which is not available). A warm arm is reported *beside* it, never instead of
it. The known limitation carries over verbatim: this host's ext4-on-VHD reads
at 1.5-1.7 GB/s, so **no slow-storage arm is possible here** and the result
bounds this device class only.

**Witness shape.** The bitmap heap scan, exercised (a) through the TPC-H
queries whose captures contain one and (b) through a purpose-built
low-correlation fetch large enough that the fetch is not swamped by per-row
CPU — `DESIGN.md` §3.2 records a first attempt that used 704-byte rows and
measured nothing because it was CPU-bound at ~150 s/scan.

**Plans must not move.** This is an I/O-path change. A moved plan is an
investigation, not a result to accept.

**Go / no-go, decided by these and nothing else:**

- **GO** — the cold arm shows the window arm faster than depth 0 by more than
  the arm's own control-vs-control band, with disjoint repetition ranges, on at
  least one witness shape; *and* pin balance, eviction delta and `-race` are
  all clean; *and* the allocation arm does not regress (the number to beat is
  the deleted function's 63.8% of allocation objects). *[source review — the
  first draft said a correct prefetch "allocates **nothing** per block". That
  is wrong. It allocates no 8 KiB heap page, which is the 90.1%-of-bytes term,
  but per block the engine still allocates a `Handle` plus its `done` channel
  and an `Op` copy (`method_worker.go:49`), an `inFlightEntry` map insert
  (`aio.go:506-521`), and the `unlock`/`cb` closures (`smgr.go:239-245`,
  `initdb/open.go:2704-2707`). Fewer objects than the deleted function, not
  zero — and the GO gate is written as an allocation-profile comparison, so the
  wrong figure would have decided it.]*
- **NO-GO, in scope** — the mechanism is correct but measures inside the noise
  band. Land it inert at depth 0 only if the pin/eviction tests are green and
  it costs nothing off; otherwise do not land it.
- **NO-GO, out of scope** — no witness with recoverable I/O exists in either
  corpus at the available scale factors. E-19 authorises this outcome
  explicitly. It closes the row with the cold-arm evidence attached, and it is
  the outcome §3.3's row counts make most likely.

## 5.9 Post-review verdict: what (A) actually costs, and the recommended order

Both reviews changed the shape of this item. The mechanism is *possible* — the
concurrency argument in §4.1 survives, the operator is the right one, and the
buffer pool's IO-in-progress machinery genuinely does the hard part. But "reuse
`pinLoad`, swap one call" was wrong, and the honest bill for (A) is:

**Prerequisites, none of which exist:**

1. A **vectored read** through `Manager`, `AIOEngine` and all three methods —
   without it the design discards `io_combine_limit`, the half of upstream's
   mechanism that plausibly pays (§4.1, oracle finding 2).
2. **Per-backend pin accounting** in `internal/storage/` — there is none, and
   the distance-limit rule has nothing to consult (§4.2-2, source finding 14).
3. A **pin-sum accessor** and a **read-error injection seam**, or §4.2's tests
   cannot be written at all (source finding 12).

**Reworks of paths the draft called unchanged:** the publish (absolute `Store`,
pin hard-coded), `Unpin`'s underflow contract, `releaseVictimSlot`'s recycling,
verify-on-read in the completion path, submission moved out from under `pinMu`,
`InvalidateRel`/`TruncateRelationTail`'s `!stateValid` skip, the `f.nblocks`
race, and a **new** drain point in `bitmapHeapScanOp` (source findings 1-8, 11).

**Against which the witness is:** 15 nodes, median 400 estimated rows, *every
one* an NLI inner or `SubPlan` child rescanned per outer row, none under a
`Gather`; on a cluster whose 2048 MB `shared_buffers` holds the entire 1.9 GiB
dataset (§3.4); with upstream's own distance controller — which ramps from 1
and doubles only on waited I/O — never approaching its cap at those sizes
(oracle finding 8); and with the only cold measurement in evidence putting the
whole recoverable I/O budget of a *919 MB sequential* scan at **0.175 s of
5.364 s** (§3.1).

**Recommended order, and it is not "implement":**

1. **Run §5.0's Probe 0.** It needs no buffer-pool change, no prerequisite, and
   no correctness surface, and it is the only step that can distinguish "we
   measured no gain" from "there was no gain to measure".
2. If Probe 0 shows no recoverable budget on a low-correlation, larger-than-pool
   witness, **close E-19 NO-GO / out of scope** on that evidence, with the
   prerequisites above recorded on the ledger as the resume point. This is the
   outcome §3.3, §3.4 and both reviews jointly predict.
3. Only if Probe 0 shows a real budget: land prerequisite 1 (vectored read) as
   its own slice, then 2 and 3, then §4 — one variable per commit, default off.

Writing §4 before Probe 0 would be building six reworks and three prerequisites
to serve a witness that the corpus, the cluster geometry and the upstream
distance policy all say is not there.

### 5.9a Post-Probe-0 status: step 2 is closed off, step 3 is live

Probe 0 ran (§5.0a) and **the budget is 79.9% of the query**, not the ~3% the
sequential case bounded. So the prediction above was **wrong**, and the item is
*not* closable as "no gain available". Step 3 is the branch, in its stated
order, and it remains a multi-slice piece of work whose bill (§5.9) is
unchanged by the good news:

| slice | why it is first |
| :-- | :-- |
| **S1** vectored read through `Manager` (**LANDED 2026-09-07**) | without it the design discards `io_combine_limit`; also the only slice with no buffer-pool correctness surface |
| **S2** pin accounting + pin-sum accessor + read-error injection seam | §4.2's three hazard tests are unwritable until this lands |
| **S3** `StartRead`/`FinishRead` with the §4.1a reworks | the six breaks, each of which is a wrong-data or deadlock class, not a perf class |
| **S4** the `bitmapHeapScanOp` / index-scan window with a **new** drain point | plus the parallel batched-claim API if the parallel path is in scope |

**S1 scoping note (2026-09-07 workstream session):** "vectored through all
three methods" is harder than it reads. `method_sync`/`method_worker` can
use `preadv(2)` directly, but the io_uring binding covers a DELIBERATELY
narrow surface — `method_iouring_linux.go:43` states IORING_OP_READV/WRITEV
is excluded ("we don't need it") — so S1 needs ring-level surgery to
submit READV, not just a new `Op` kind on top of per-block `ReadAt`
(`aio.go:runOp`). And the checksum story follows the op: `relFile.ReadAt`
verifies per block (`smgr.go:804-808`), so a true single-syscall vectored
read must preserve per-block verification (or route each block's bytes
through `VerifyRead`) — the exact class the E-19 source review already
fired on once. S1 is therefore NOT a thin API shim; scope it as ring
surgery + per-method tests + checksum preservation, and do not accept a
loop-over-`ReadBlock` as "vectored" (it buys no syscall reduction, which
is the whole point of `io_combine_limit`).

### 5.9b S1 LANDED — `Manager.ReadBlocks`, and the combining is measured

`Manager.ReadBlocks(rel, first, bufs)` → `relFile.readBlocks` → `preadvAt`
(`preadv_linux.go` / a ReadAt-loop fallback in `preadv_other.go`).
`MaxIOCombineLimit = 128` and `DefaultIOCombineLimit = 128 KiB / BlockSize = 16`
transcribe `MAX_IO_COMBINE_LIMIT` / `DEFAULT_IO_COMBINE_LIMIT`
(`postgres/src/include/storage/bufmgr.h:165-166`).

**Measured, not asserted:** reading 16 consecutive blocks costs **18** read
syscalls one block at a time and **3** through `ReadBlocks` (one `preadv` plus
the probe's own two `/proc/self/io` reads) — i.e. 16 syscalls collapse to 1.

Three properties `readBlocks` deliberately keeps from `readBlock`, each with a
test, because a vectored read that relaxed any of them would be a correctness
regression no values suite could see:

- **Per-block latches over the whole run**, acquired in **ascending** order —
  which is what makes it deadlock-free against another run (also ascending) and
  against any single-block reader/writer. `TestReadBlocksLatchesEveryBlockInTheRun`
  holds block 2's latch and asserts a 0..3 run cannot complete.
- **The bounds check is under `r.mu`, with the read.** This deliberately does
  *not* repeat `PrefetchBlock`'s unlocked `f.nblocks` read (`smgr.go:212`,
  source review finding 8).
- **Per-block checksum verification**, inside `readBlocks` so no caller can
  bypass it. `TestReadBlocksVerifiesChecksums` caught a real flaw in the first
  draft: on a mismatch it returned the *full* block count, which would tell a
  caller reading count-before-error that the corrupt block had been filled. It
  now returns only the number of blocks that **verified**.

**And the gate needs its own decision before S3 lands.** §6's suites cannot
score this: TPC-H is 1.9 GiB in a 2048 MB pool, so the arm that would show the
win is byte-identical to the arm that would show nothing. The A/B that decides
S3 must be Probe 0's own instrument — a low-correlation fetch on a
larger-than-pool relation, cold — promoted to a checked-in bench, with the
values suites retained only as **regression** gates. Recording that here so the
next owner does not read a flat TPC-H median as a refutation.

## 5.10 Triage of the `releaseVictimSlot` hazard — UNREACHABLE at HEAD, and E-19 is exactly what makes it reachable

Ordered ahead of S1 because a live slot-recycling bug would outrank the whole
performance item. It is not live.

`releaseVictimSlot` (`bufpool.go:1480-1492`) does `s.state.Store(0)` — dropping
validity, the pin count *and* the generation — and has **eight** call sites
(`:1732`, `:1743`, `:1757`, `:1804`, `:1966`, `:1973`, `:1990`, `:2014`). At
HEAD **not one of them can run after the slot pointer has escaped to a caller**,
and the reason is structural rather than lucky: `pinLoad` and `pinNewXID` are
strictly synchronous, and each returns `*Slot` on exactly one path — the last
statement, after the state has been published valid-and-pinned. There is no
program point at which the pool has both handed the pointer out and can still
take an error branch. Site by site: `:1732`/`:1743`/`:1757` (`pinNewXID`
evict/`InitPage`/`Extend` failures) and `:1966`/`:1990`/`:2014` (`pinLoad`
evict/`bmInsert`/read failures) all return `nil`; `:1973` releases *our unused
victim* and returns a **different**, already-pinned slot; and `:1804` releases a
slot it had published valid+dirty+pin=1 but never returned — it re-acquires
through `Pin(tag)` and returns that instead, and while unpublished the slot is
invisible to `claimVictim` (`statePin != 0`) and to `bm.Lookup` (never
inserted).

The generation wipe is likewise benign today for a second, independent reason:
every path reaching `releaseVictimSlot` has already removed the slot's bufmap
entry or never inserted one — `evictVictim` calls `bmDelete` **before** it
returns its flush error (`:1585-1595`), so even the failure branch leaves no
mapping that could later ABA against a recycled generation.

**E-19 is precisely the change that breaks this.** `StartRead` returns the slot
*before* the read completes, so for the first time the pool holds a slot the
caller already has while an error branch is still ahead of it — and that branch
runs on an AIO completion goroutine. S3 must therefore not reuse
`releaseVictimSlot` on the async error path: the slot has an owner, and the
correct action is to publish it **failed** (clear `slotIOBit`, wake waiters,
leave the pin) and let the owner's `FinishRead` observe the error and unpin,
never to `Store(0)` under it. Recorded here rather than on the ledger because
there is no defect at HEAD to defer; it is an S3 acceptance condition.

## 6. Gates

`scripts/tpch-spotcheck.sh` (canonical Q12=2 / Q13=35), values on both corpora
(TPC-H `-digest`/`-diff`, TPC-DS `PASS=95` `CKMISMATCH=0`), `go test -race`
over `internal/storage/` and `internal/executor/`, `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`, and the plan gate. Never `-count=1`.

## 7. Review record

Two adversarial reviews were run against the first draft. Every correction is
folded inline above, marked *[oracle review]* or *[source review]* at the point
it applies; this section is the index, not the record.

### 7.1 PG 18.3 oracle pass over `postgres/` — verdict: §2 survives, §3's
### upstream authority does not, §4 needed three edits

| # | finding | where folded |
| :-- | :-- | :-- |
| 1 | **`advice_enabled` is gated on `sync_mode` too** (`read_stream.c:658`, `:669-673`); `DEFAULT_IO_METHOD = IOMETHOD_WORKER` (`aio.h:42`), so `READ_STREAM_SEQUENTIAL` is a **no-op** by default and the seq-vs-bitmap dichotomy does not exist. Also refutes the accepted `DESIGN.md` §3.1's normative pillar | new §3.0; §3.1 and §3.2 rewritten |
| 2 | **`io_combine_limit` read combining was missing** — `read_stream.c:474-477`/`:445-450`, one vectored `preadv` per run (`bufmgr.c:1408`, `:1777`), 16 blocks by default (`bufmgr.h:166`), multiplicative with depth (`read_stream.c:593-594`). A per-block API keeps only the weaker half | §4.1: API becomes run-based; vectored `Manager` read added as a prerequisite |
| 3 | `effective_io_concurrency` is `max_ios`, **not** window depth (`read_stream.c:565-571`, default 16) | new §4.3a; §4.3 reworded |
| 4 | PG does **not** decline at the pin limit — it shrinks distance, does a **forced short read**, and floors at one buffer to guarantee progress (`read_stream.c:320-341`). `LimitAdditionalPins` is the *extension* path; reads use `GetAdditionalPinLimit` (`bufmgr.c:2517`, `:4019`). The pin limit is **not** an eviction control | §4.2 hazard 2 rewritten; the eviction test relabelled a goopg invention |
| 5 | "only production driver is `read_stream.c`" is wrong as stated — `ReadBuffer_common` drives the same interface synchronously (`bufmgr.c:1251-1256`) | §2 narrowed to "asynchronous, look-ahead use" |
| 6 | §2's central claim **CONFIRMED verbatim** — `PrefetchSharedBuffer` `:560-596` calls `smgrprefetch` only; `recent_buffer` is unpinned and must be rechecked (`:600-604`, `ReadRecentBuffer` `:682`); four advice-shaped callers survive in 18.3 | §2, with corrected line refs |
| 7 | "no upstream design installs an unpinned buffer" **CONFIRMED** — `xlogprefetcher.c` is the closest candidate and is pure advice. Surfaced the AIO subsystem's **own second pin** (`bufmgr.c:6852-6853`) as the upstream answer to hazard 1 | §2 and §4.2 |
| 8 | PG 18.3 **deleted** the old bitmap prefetch machinery; per-buffer `TBMIterateResult` side-channel is required; distance **ramps** (1, doubling on waited I/O) rather than being constant — and over a 400-row bitmap it never nears its cap, which independently supports §3.3 | §3.2 |
| 9 | line-citation drift (`heapam.c:1220`/`:1231`; `bufmgr.c:632-648`) | corrected in place |

### 7.2 Source-falsification pass over `internal/` — verdict: §1 and the §3.3 arithmetic hold exactly; §4.1's "reuses everything unchanged" does not survive contact with the code

| # | finding | where folded |
| :-- | :-- | :-- |
| 1 | **Checksum verification silently dropped** — `readBlock` ends with `verifyOnRead` (`smgr.go:888`, `:706-720`); the `PrefetchBlock`+engine path never reaches it, and the engine-*less* fallback does, so a no-engine test never sees the divergence | §4.1a-1 |
| 2 | **Deadlock**: `Submit` blocks on a 12-deep queue (`method_worker.go:6-9`, `:29-33`) while called under `pinMu` (`bufpool.go:1954`), and completions run on the same 3 worker goroutines (`aio.go:201-209`) and need `pinMu` (`:2016-2018`) | §4.1a-2 |
| 3 | **Publish is an absolute `Store` hard-coding pin = 1** (`:2020-2022`) — the encoding permits `pin>0` with `slotIOBit`, the publish path does not | §4.1a-3 |
| 4 | `Unpin` **panics** on pin 0 (`:2086-2092`); a pre-publish slot has pin 0 (`:1534`), so the documented escape hatch is unimplementable | §4.1a-4 |
| 5 | Error path `releaseVictimSlot` does `s.state.Store(0)` (`:1483`), recycling a slot already handed to the caller — **and the pin-balance test would pass while this is broken** | §4.1a-5 |
| 6 | `StartRead` performs up to **D synchronous dirty-page writebacks** before the first read is in flight (`:1962-1966`, `:1576-1589`); §5's arm has no term for it | §4.1a-6 |
| 7 | `InvalidateRel`/`InvalidateBlock` skip `!stateValid` (`:1391`, `:1412`), so a D-deep window can publish a **valid slot for a truncated block** | §4.1a |
| 8 | `PrefetchBlock` reads `f.nblocks` **without** `r.mu` (`smgr.go:212`) while `extend` writes it under it (`:914`, `:926`) — a dormant race this design puts on every pool miss | §4.1a |
| 9 | **All 15 witnesses are NLI-inner / `InitPlan` / `SubPlan` children, rescanned per outer row; none under a `Gather`**, so the parallel bitmap path has zero witnesses; and the capture is `EXPLAIN`, not `ANALYZE`, so execution is not established | §3.3 |
| 10 | **Parallel per-worker window is NOT "equally well-defined"** — `nextPage` yields one index per `nextIdx.Add(1)` (`parallel_bitmap_scan.go:71-82`); worker blocks interleave; a batched-claim API is required | §3.2 |
| 11 | `releasePinned` is a **per-page** operation (`operators_bitmap.go:461-469`, called at `:375`, `:437`, `:515`, `:523`, `:606`, `:613`) — draining there destroys the window every page; the drain point must be new. Plus `Rescan` leaves lossy/parallel cursor state stale, and `fetchExact` **recurses into `Next()`** | §4.2-1 |
| 12 | §4.2's tests are **not writable today**: `getPinCount` unexported (`:112`), no read-error injection seam (`:418-425` have no error return); `sharedEvictionCount` is confirmed real (`:1555`, `EvictionCount()` `:1188`) but process-wide | §4.2-1 |
| 13 | "a correct prefetch allocates **nothing** per block" is wrong — `Handle`+channel+`Op` copy, `inFlightEntry`, two closures per block | §5 GO criterion |
| 14 | **No per-backend pin accounting exists anywhere in `internal/storage/`** — no `PrivateRefCount` analogue, no owner on `*Slot`. "goopg transcribes the rule" is new infrastructure, and "decline when the limit binds" has nothing to consult | §4.2-2, §4.4 |
| 15 | **No goopg-side TPC-DS bitmap census exists** — UNVERIFIABLE at HEAD | §3.3 |
| 16 | §4.1 omits `OnBlockReload(tag, s.page)` fired under `contentMu` (`:2001-2003`) — arbitrary catalog-reload code that would migrate onto an AIO worker goroutine | §4.1a |
| 17 | **§1 CONFIRMED in full**; §3.2's materialisation and one-page-pinned claims CONFIRMED; `s.page` stability CONFIRMED; every `DESIGN.md` figure quoted here CONFIRMED against `:53`, `:115`, `:116`, `:119`, `:126`, `:132-133`, `:144`; §3.3's count and row list CONFIRMED verbatim | — |
