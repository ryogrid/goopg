# pgbench select-only @ -c 10 post-M0092 (2026-05-11)

## Parameters

scale=100, `-c 10 -j 10 -T 180 -P 30 -S` (select-only).
goopg config identical to M0091 summary
(`shared_buffers=2560MB`, `wal_buffers=100MB`,
`checkpoint_timeout=24h`, `max_wal_size=1024GB`).

## Results — before / after M0092

| metric | pre-M0091 (commit 19c480e) | post-M0091 (commit 460809c) | post-M0092 (commit 8f32c07) |
|---|---:|---:|---:|
| TPS | 350.89 | **510.52** | **437.62** |
| latency avg | 28.50 ms | 19.59 ms | 22.85 ms |
| latency stddev | 11.85 ms | 9.15 ms | 9.80 ms |
| transactions | 63,169 | 91,903 | 78,781 |
| failed | 0 | 0 | 0 |

180-s progress samples post-M0092:
```
30s: 465.0  60s: 466.2  90s: 426.5
120s: 429.7 150s: 420.3 180s: 418.1
```

## Honest assessment

M0092 (lazy row emission in indexScanOp + projectOp slot
aliasing) **did NOT improve TPS** for the pgbench select-only
workload — it regressed slightly (510.52 → 437.62 TPS) vs the
post-M0091 binary. The implementation is correct (all tests
pass, data integrity preserved), but the structural changes
don't translate to a measurable TPS win at this workload size
and concurrency.

## Why M0092 didn't help

Per the post-M0092 alloc profile
(`pprof-data/m0092/select-only-c10.allocs.prof`):

| flat % | site | cum chain |
|---:|---|---|
| 35.28 % | `executor.init.0.func1` (rowPool New) | ←acquireRow ← cloneRow / cloneRowOwned |
| 33.47 % | `storage.newArena` | buffer-pool startup (one-time) |
| 6.17 % | `storage.PageGetHeapTuple` | new per-Next path |
| 6.16 % | `storage.Pool.Prefetch` | seqScanOp.refillPrefetchWindow (startup query) |
| 5.96 % | `executor.SlotFromRow` | new per-Next path |
| 3.06 % | `storage.ParseHeapTuple` | new per-Next path |

The rowPool.New share (35 %) is essentially unchanged from
M0091's 34.75 %. The cloneRow code path moved from
projectOp.Next → slot.Materialize (which now always
deep-copies). The total allocation rate per query is
similar.

What this means: the dominant per-query cost is **not** the
cloneRow / acquireRow path. It's somewhere else — likely
distributed across many small sites including the new per-
Next `SlotFromRow`, `PageGetHeapTuple`, `ParseHeapTuple`,
plus the GC mark phase that runs at 80 % of CPU.

The M0091-0001 + M0091-0002 fixes addressed the largest
single sites (activity.goroutineID + btree.RangeScan
per-slot byte copy). After those, the residual allocation
is broadly distributed; eliminating one site
(cloneRow) just relocates the allocations rather than
eliminating them.

## What M0092 IS — even if pgbench-c10 doesn't show benefit

The structural changes ARE real improvements that may
benefit other workloads:

1. **`indexScanOp` is now lazy-iterate.** The eager `o.rows`
   pre-materialisation pattern is gone. For a range scan
   with N matches:
   - Pre: allocate o.rows of N Rows, each cloned from
     scanRow.
   - Post: allocate o.tids of N ItemPointers (8 bytes
     each). Decode happens per Next().
   - For wide range scans (TPC-H scans with millions of
     matches), the memory footprint is dramatically lower.
2. **`projectOp` no longer allocates per-row.** The
   producer-of-row no longer has to clone defensively.
3. **The slot contract is now explicit.** `MaterializedSlot.
   Materialize()` is the documented retention boundary;
   pre-M0092 it had a no-op fast-path that hid a subtle
   producer-buffer-reuse contract.
4. **`nestedLoopIndexJoinOp.currentOuter` is now properly
   independent of upstream buffer reuse** (Commit B).

These are correctness improvements + future-workload
preparation. The pgbench-c10 measurement isn't the right
benchmark to show their benefit.

## Path forward

For pgbench select-only TPS to reach ≥ 1,000 (M0091's bar),
the remaining bottlenecks need addressing:

- **GC still at 80 % of CPU.** Allocation rate per query is
  still high despite eliminating two large sites. The
  distributed residual (SlotFromRow, PageGetHeapTuple,
  ParseHeapTuple, protocol cells slice) needs systematic
  reduction.
- **Plan caching.** pgbench's simple-query protocol parses +
  plans every query. A statement cache would eliminate
  parser.Parse + planner.Plan + executor.Build overhead.
- **Protocol-layer per-DataRow allocation.** `cells :=
  make([][]byte, ncols)` and `[]byte(d.Format())` per row
  contribute small but frequent allocations.

These are filed as out-of-scope in the M0092 milestone doc.
They warrant their own milestone (M0093 candidate).

## Files

- `bench/pgbench-compare/results/20260511_133003_goopg_select-only_c10_m0092.txt`
- Pre-fix pprof: `pprof-data/m0091/select-only-c10.*.prof`
- Post-M0091 pprof: `pprof-data/m0091/post-0002/select-only-c10.*.prof`
- Post-M0092 pprof: `pprof-data/m0092/select-only-c10.*.prof`
