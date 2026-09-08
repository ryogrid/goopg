# `Pool.Prefetch` — resolve the discarded-buffer defect

**Status:** accepted · **Date:** 2026-09-06 · **Outcome: (B) remove the
mechanism**, on cold-cache as well as warm-cache evidence.

Supersedes the heap-scan caller half of
[`0009-0003-aio-storage-integration.md`](../0000-0049/0009-0003-aio-storage-integration.md)
(`Pool.SetPrefetchEnabled` / `Pool.Prefetch` / `seqScanOp.refillPrefetchWindow`).
The smgr-level seam that doc's main subject — `Manager.SetAIO` /
`Manager.PrefetchBlock` — is **kept**; only its single buffer-pool caller goes.

Predecessor measurement: `analysis/executor-refactor/e11-depth-sweep-20260906/`
(item E-11 / EX5-04). Ledger rows `take3-E-11-prefetch-discards-buffer` and
`take3-E-11-readstream-declined`.

---

## 1. The defect

`internal/storage/bufpool.go`:

```go
func (p *Pool) Prefetch(tag BufferTag) {
        if !p.prefetchEnabled.Load() { return }
        if slotIdx, _ := p.bm.Lookup(tag); slotIdx >= 0 { return }
        buf := make([]byte, BlockSize)
        _, _ = p.mgr.PrefetchBlock(tag.Rel, tag.Block, buf)
}
```

`buf` is a fresh 8 KiB heap allocation per hinted block and is **never
installed into the pool** — it is dropped when `Prefetch` returns. The
following `Pin` of that block therefore does a full read anyway.

An AIO engine *is* attached in production (`io_method` BootVal `"worker"`,
`internal/utils/misc/defaults.go:683` → `cmd/goopg/main.go:445` →
`internal/initdb/open.go:335`), so this is a genuine asynchronous submit, not
a synchronous double-read; `Manager.PrefetchBlock`'s inline fallback
(`internal/storage/smgr.go:204`) only fires with no engine attached. The one
effect that survives the discarded buffer is therefore **warming the OS page
cache** — the files are opened plain buffered, `os.O_RDWR|os.O_CREATE`
(`internal/storage/smgr.go:616`), with no `O_DIRECT` anywhere in the tree.

There is a second, subtler cost. `Manager.PrefetchBlock` takes the per-block
latch `relFile.lockBlock(blk)` and holds it until the AIO op's `OnComplete`
fires (`smgr.go:239`). The scan's own `ReadBlock` → `relFile.readBlock` takes
the same latch (`smgr.go:871`). So the scan does not merely re-read the block:
it **blocks on the in-flight prefetch of that block, then reads it again
itself**. Every hinted block pays a goroutine round trip plus a duplicate
`pread`.

Measured cost (E-11 §5, pprof `-base` over 8 serial Q6 executions):
`Pool.Prefetch` is 63.8% of allocation objects and 90.1% of allocation bytes;
depth 4 costs 14.96 M objects / 9.92 GB against depth 0's 5.29 M / 1.00 GB.

The only production caller is `seqScanOp.refillPrefetchWindow`
(`internal/executor/operators_storage.go`), and it returns early for a
parallel scan — which is every TPC-H plan at bench settings.

## 2. The two options

**(A) Make it a real prefetch.** Install the read into a pool slot so the
following `Pin` finds it — PG's `StartReadBuffers` / `WaitReadBuffers`
(`postgres/src/backend/storage/buffer/bufmgr.c:1489`, `:1262`), driven from
`postgres/src/backend/storage/aio/read_stream.c`. goopg already owns most of
the machinery: `slotIOBit` marks a slot IO-in-progress, and `Pool.pinSlow`
already parks a concurrent `Pin` on `slotSema[idx]` until the loader clears it
(`bufpool.go:1912`). A prefetch would claim a victim under `pinMu`, publish
the tag with `slotIOBit` set, submit the read into `s.page`, and clear the bit
from a completion goroutine.

**(B) Remove the mechanism.** Delete `Pool.Prefetch` and its call site, on the
evidence that it costs 63.8% of allocations to warm a page cache the kernel
warms for free.

## 3. Evidence

### 3.1 Upstream has already decided this exact question

`postgres/src/include/storage/read_stream.h:30-36`:

> We usually avoid issuing prefetch advice automatically when sequential
> access is detected, but this flag explicitly disables it, for cases that
> might not be correctly detected. **Explicit advice is known to perform
> worse than letting the kernel (at least Linux) detect sequential access.**

and `postgres/src/backend/access/heap/heapam.c:1220` — PG's **sequential heap
scan** is precisely the caller that passes that flag:

```c
scan->rs_read_stream = read_stream_begin_relation(READ_STREAM_SEQUENTIAL |
                                                  READ_STREAM_USE_BATCHING, ...
```

`READ_STREAM_SEQUENTIAL` suppresses lookahead advice (`read_stream.c:671`:
`(flags & READ_STREAM_SEQUENTIAL) == 0 && max_ios > 0` gates
`stream->advice_enabled`). goopg's `Pool.Prefetch` is exactly advice-shaped —
an extra read issued only to warm the cache — and its only caller is exactly
the scan for which upstream disables it, on buffered files where Linux
readahead is active. Keeping it is the divergence; removing it is the
PG-faithful position.

`read_stream.c`'s own header adds the workload half of the argument: *"When no
I/O is necessary, there is no benefit in looking ahead more than one block.
This is the default initial assumption, but when blocks needing I/O are
streamed, the distance is increased rapidly."*

### 3.2 Cold-cache measurement — the new evidence

E-11's whole evidence base was warm-cache, which systematically favours
removal. This is the missing arm.

| item | value |
| :-- | :-- |
| instrument | one narrow table, 12 M rows / 919 MB / 112 152 blocks, `SELECT count(*)`, `max_parallel_workers_per_gather = 0` |
| why narrow | a first attempt used 704-byte rows and came out CPU-bound at ~150 s/scan regardless of arm — per-row cost swamped all I/O and measured nothing |
| binary | one build of `9ad4f30d4`-era HEAD (`e4c2dd0db`); depth switched via `GOOPG_SEQSCAN_LOOKAHEAD` (`c6af781f4`), never rebuilt per arm |
| server | FRESH memory-capped server per arm (`GOOPG_CG_UNIT`, `GOMEMLIMIT=8GiB GOGC=100`), so server age is 0 s in every arm; `shared_buffers` 128 MB against a 919 MB table, so the pool never holds the scan |
| cold | `posix_fadvise(POSIX_FADV_DONTNEED)` over the relation file before the run (no root available for `drop_caches`); confirmed by `/proc/<pid>/io` `read_bytes` = 918 732 800 = exactly the file |
| warm | file `cat`-ed to `/dev/null` first; `read_bytes` = 0 |
| ordering | 3 reps, arm order permuted per rep so position cannot masquerade as a depth effect |

Wall seconds, every arm:

```
depth 0 cold   5.364  5.112  5.372      median 5.364
depth 4 cold   6.012  6.039  6.445      median 6.039
depth 0 warm   5.322  5.189  5.135      median 5.189
depth 4 warm   5.840  5.787  8.119      median 5.840
```

Rep-to-rep spread at a fixed arm (the noise floor): 3.6% (d0 warm), 5.1%
(d0 cold), 7.2% (d4 cold); d4 warm carries one 8.12 s outlier.

Three readings, in the order they bind:

1. **Prefetch loses on a cold page cache, not only a warm one.** Depth 0 beats
   depth 4 by **11.2%** cold (5.364 vs 6.039) and by **11.1%** warm (5.189 vs
   5.840). Both comparisons have **disjoint repetition ranges** — the worst
   depth-0 arm beats the best depth-4 arm in each pair (5.372 < 6.012 cold;
   5.322 < 5.787 warm).

2. **Prefetch does not reduce the cold-cache penalty at all.** Cold costs
   +0.175 s over warm at depth 0 and +0.199 s at depth 4. The mechanism whose
   entire purpose is to hide that penalty leaves it exactly where it was.
   Kernel readahead has already absorbed it: 919 MB of genuinely cold,
   strictly sequential reads adds 3.4% to the scan.

3. **The mechanism costs several times the I/O it could ever hide.** The whole
   cold-cache I/O penalty is 0.175 s; the prefetch machinery costs 0.65-0.68 s
   in the same arms. Even a perfect prefetcher — one that hid 100% of the I/O
   — could not pay for the mechanism.

### 3.3 What the cold arm does and does not cover — the honest caveat

The cold arm has a genuinely cold **Linux page cache** (`read_bytes` proves
every block came from the block device). It does **not** have slow storage:
this host is WSL2 and the ext4 sits on a VHD backed by the Windows host's
cache, measured at 1.5-1.7 GB/s for a `dd bs=1M` sequential read of the same
file immediately after the same `fadvise`. A rotational-disk or
network-storage regime was **not** reproducible here: `drop_caches` needs
root, and the `io` cgroup controller is not delegated to the user slice
(`/sys/fs/cgroup/user.slice/user-1000.slice/cgroup.controllers` = `cpu memory
pids`), so no root-free bandwidth or IOPS throttle is available.

What survives that limitation is reading 3.2 rather than 3.1: the cold-vs-warm
delta is **identical with and without prefetch** (0.199 s vs 0.175 s). The
mechanism does not shrink the I/O term at any device speed, because on a
buffered file the kernel's own readahead already saturates sequential
bandwidth for exactly the strictly-ascending pattern `refillPrefetchWindow`
emits — which is upstream's stated reason for `READ_STREAM_SEQUENTIAL` and is
a bandwidth argument, not a device-speed one. On slower storage the I/O term
grows for *both* arms, and the prefetch arm additionally keeps paying its
per-block goroutine round trip and duplicate `pread`.

Recorded limitation rather than hidden: **no slow-storage arm was run.**

## 4. Decision: (B), remove

(A) is the correct shape for a prefetch that should exist. This one should
not, so (A) would be building the right machine for the wrong job:

1. **It cannot win.** §3.2 reading 2 — there is no I/O latency left for a
   correct prefetch to hide on this access pattern. (A) removes the duplicate
   `pread` and the 8 KiB-per-hint garbage, but keeps the part upstream
   disables: issuing lookahead reads out of an AIO worker pool ahead of a
   strictly sequential scan of a buffered file.
2. **It has no caller that wants it.** `Pool.Prefetch`'s one production caller
   is the sequential heap scan, and the window is already off for parallel
   scans — every TPC-H plan at bench settings.
3. **It would trade a wrong-data risk for that.** (A) introduces a
   pinned-but-not-yet-valid slot class into the buffer pool. A concurrent
   `Pin` adopting such a slot as valid is a wrong-data bug, not a performance
   bug. Buying that risk for a measured-negative benefit is not a trade worth
   making.

### 4.1 Scope of the removal

Removed:

- `Pool.Prefetch`, `Pool.SetPrefetchEnabled`, `Pool.prefetchEnabled`
  (`internal/storage/bufpool.go`).
- `pool.SetPrefetchEnabled(true)` at the AIO-engine attachment
  (`internal/initdb/open.go`).
- `seqScanOp.refillPrefetchWindow`, `seqScanOp.prefetchedThru`, the three call
  sites, and the `seqScanLookahead*` family including the
  `GOOPG_SEQSCAN_LOOKAHEAD` knob (`internal/executor/operators_storage.go` —
  deliberately minimal: nothing else in that file changes).

Kept, and why:

- `Manager.SetAIO` / `Manager.PrefetchBlock` / `AIOEngine` / `AIOHandle`
  (`internal/storage/smgr.go`). This is the smgr-level asynchronous-read seam,
  not the defect; it allocates nothing unless called, keeps its own tests, and
  is the entry point any future read-stream work starts from.
- `internal/storage/aio/read_stream.go` — untouched; still the resume point
  for `take3-E-11-readstream-declined`.

`GOOPG_SEQSCAN_LOOKAHEAD` goes with the mechanism it tuned. Leaving a knob
that resurrects a measured-harmful path is worse than leaving no knob; the
apparatus is preserved in git history, in this doc, and in
`analysis/executor-refactor/e11-depth-sweep-20260906/`.

### 4.2 What would reopen this

A **random-access** buffer-pool caller — bitmap heap scan, or an index scan
following a low-correlation index. That is the case where kernel readahead
genuinely cannot help and where PG *does* enable advice (`READ_STREAM_DEFAULT`
via `heapam.c`'s `SO_TYPE_BITMAPSCAN` stream). The answer there is option (A)
shaped as `StartReadBuffers`/`WaitReadBuffers` with I/O combining, driven from
a real read stream — not this function. Filed on the ledger row.

## 5. Correctness argument for the removal

Removing reads is only safe if nothing depended on the side effect.

- `Pool.Prefetch` returns no value and its callers ignore it. Its only
  observable effects are (i) OS page-cache state and (ii) the AIO engine's
  submit counters. Neither is load-bearing for any result: page-cache state is
  invisible above `Manager.ReadBlock`, which reads the block either way.
- It never installed anything into the buffer pool, so no `Pin` can have been
  satisfied by it and no visibility, WAL, or MVCC path can have observed it.
- It never marked a slot dirty and never wrote, so no durability path is
  touched.
- The per-block latch it took (`lockBlock`) was strictly additional
  serialization released on completion; removing it removes contention only.

Gate: `scripts/tpch-spotcheck.sh` (`RESULT=PASS`, canonical Q12/Q13 row
counts), `go test ./internal/storage/ ./internal/executor/`, `go vet`, and the
TPC-H 24-query digest if the bench cluster is touched.
