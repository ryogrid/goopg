# UPDATE Bottleneck Deep Dive

## Summary of Findings

After implementing concurrent WAL Append (M0026), the TPS for
write-heavy workloads remained unchanged at ~14 TPS. This proves
the bottleneck is NOT WAL serialisation.

CPU profiling during UPDATEs showed only 0.7% CPU utilisation
across 10 seconds (70ms sampled out of 10s). This means the
server is NOT CPU-bound during UPDATE processing — it is spending
most of its time blocked on something else (I/O, locks, or
goroutine scheduling).

## Hypotheses Tested

| Hypothesis | Test | Result |
|------------|------|--------|
| WAL Append serialisation | Concurrent `tryAppend` (M0026) | ❌ No improvement |
| CPU capacity | pprof CPU profile | ❌ Only 0.7% utilisation |
| Connection overhead | pgbench (reuses connections) vs Go client (opens per-query) | Using pgbench gave 13.8 TPS — consistent with Go client. Overhead is server-side, not client-side. |
| WAL flush (fdatasync) | WAL is flushed asynchronously (FlushUpTo only in eviction/checkpoint) | Likely NOT the bottleneck since FlushUpTo is rare. |

## Remaining Hypotheses

### 1. Buffer pool eviction + fdatasync chain

When the buffer pool evicts a dirty page, `flushSlot` writes the
page to disk via `Manager.WriteBlock` (N-ary write) and then calls
`wal.FlushUpTo` (fdatasync). If the working set exceeds the buffer
pool (128MB default), every UPDATE causes an eviction chain.

**Check:** Monitor evictions via eviction counters or log the
eviction rate during a pgbench run.

### 2. Lock manager serialisation

The tuple-level lock in `PageSetHeapTupleXmax` and
`updateOp.lockHeapExtend` might serialise concurrent writers on
the same page. pgbench's random `aid` distribution means most
writes hit different pages, but the extension lock in
`writeHeapRowReturning` serialises ALL extend operations.

**Check:** Disable the extension lock temporarily.

### 3. Heap page slot management (`PageAddHeapTuple`)

Each `PageAddHeapTuple` call scans the page's line pointer array
for a free slot. With ~80 rows/page and no VACUUM between runs,
the page fills up and new tuples must be written to new pages
(extend). This extend does file I/O.

**Check:** Run with a small, constant-size table (no extends).

### 4. Index maintenance

The primary-key index on `aid` must be updated on each UPDATE.
The B-tree insertion path may be O(n) due to a missing optimisation.

**Check:** Compare UPDATE with vs without a UNIQUE index.

## Measurement Methodology

- **Server**: goopg HEAD (perf-analysis branch, includes M0026)
- **Client**: pgbench 18.3 (in-tree `postgres/local_install/bin/pgbench`)
- **Scale factor**: 3
- **Shared buffers**: 128 MB (default)
- **WAL buffers**: 16 MB (default)
- **Profile**: `runtime/pprof` in test process (non-representative of server)
- **Wait events**: `pg_stat_activity` polling (showed no blocking I/O)

## Next Steps

1. Run pgbench with `shared_buffers=4096` (tiny) vs `shared_buffers=1048576`
   (huge) to test eviction hypothesis.
2. Add eviction-rate logging to the buffer pool.
3. Run with GOMAXPROCS=1 to check goroutine scheduling effects.
4. Profile the SERVER process (add pprof HTTP endpoint).
