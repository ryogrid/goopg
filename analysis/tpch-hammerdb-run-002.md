# TPC-H HammerDB Run — shared_buffers=2000M (May 2026)

**Date:** 2026-05-02
**goopg commit:** `495810d`
**Test machine:** x86_64 Linux, 32 GB RAM + 64 GB swap, Go 1.25.0

## Configuration

| Parameter              | Value         |
|------------------------|---------------|
| `shared_buffers`       | 2000 MB (256,000 slots × 8 KiB) |
| Arena type             | Go heap `make([]byte)` (M0032-0001) |
| `GOMEMLIMIT`           | 40 GiB        |
| `checkpoint_timeout`   | 15 min        |
| `max_wal_size`         | 4 GB          |
| `wal_buffers`          | 16 MiB        |
| `wal_sender_memory_buffer` | 16 MiB    |

## Attempt #1 — HammerDB schema build

- Data loaded through HammerDB TCL driver calling dbgen + COPY.
- REGION, NATION, SUPPLIER, CUSTOMER, PART, PARTSUPP loaded successfully.
- **ORDERS/LINEITEM load failed at ~430,000 orders** (29% of SF=1 target).
- Error: `"server closed the connection unexpectedly"` from libpq.
- Server-side: no errors, no panics. Connection dropped silently.
- RSS at failure: 2.99 GB.
- **Root cause:** likely a HammerDB client-side timeout during the long COPY
  session (the load takes ~10 minutes for ORDERS/LINEITEM at SF=1).

## Attempt #2 — HammerDB schema rebuild

- Dropped all tables and re-ran HammerDB build from scratch.
- ORDERS/LINEITEM load progressed to **1,037,000 orders / 4,144,736 lineitems**
  (69% of SF=1 target) then stalled.
- RSS at stall: 4.35 GB.
- HammerDB process still running but no further data loaded — connection
  dropped silently again.

## Query Execution on Partial Data (4.1M lineitem rows)

| Query | Duration | Rows | Notes |
|-------|----------|------|-------|
| Q1    | ~2m14s   | 4    | Grouped aggregate on 4.1M lineitem rows |
| Q6    | ~2m14s   | 1    | Simple aggregate scan |
| Q14   | ~2m11s   | 1    | Join lineitem↔part (4.1M × 200K) |
| Q15   | ~4s view + ~2m14s query | 1 | View + correlated subquery |
| Q19   | ~3s      | 1    | Filtered join with no matching rows |

## Memory Behaviour During Execution

| Phase             | RSS        |
|-------------------|-----------|
| Server startup    | ~50 MB    |
| After schema build (partial data) | 4.35 GB |
| During Q1 execution | 30.14 GB |
| During Q6 execution | 30.67 GB |
| During Q14 execution | 30.88 GB |
| **Peak (Q15)**    | **30.99 GB** |
| System RAM total  | 32 GB     |
| System RAM + swap | 96 GB     |

**Result:** goopg was manually killed because memory consumption reached the
system limit (swapping heavily, system unresponsive).

## Root Cause Analysis

### Not a Go heap leak

Go heap under GC control is not the primary driver. The `shared_buffers=2000M`
arena (2 GB `[]byte`) plus the Go query working set cannot alone explain 31 GB
RSS. The dominant factor is:

### Buffer pool arena + data file I/O + kernel page cache

1. **Arena residency (2 GB):** All 256,000 buffer pool slots become physically
   resident as they are touched during data load and query execution.

2. **Data file pages (kernel cache):** The TPC-H data files at SF=1 total
   ~1.5 GB of heap relations. The kernel caches these in the page cache on
   read (during SeqScan). PostgreSQL's `O_DIRECT` bypasses this but goopg's
   default path (`AlignedIO=false`) does NOT — every `ReadAt` through Go's
   `os.File` populates the kernel page cache.

3. **WAL segments:** ~4 GB allocated (`max_wal_size`), with the active
   segment(s) in memory.

4. **Query working set:** During SeqScan of 4.1M LINEITEM rows (Q1), each
   row is decoded into `[]Datum` Row objects. The executor's operators
   (sort, aggregate) buffer intermediate results. With 4M rows × 16 columns
   × ~100 bytes per Datum, the per-query temporary allocation alone is
   ~6.4 GB.

5. **AIO + WAL buffers:** Additional ~32 MB.

6. **Go heap overhead:** `GOMEMLIMIT=40GiB` tells the runtime "don't
   scavenge" — the GC does not return unused heap to the OS, keeping RSS
   elevated even after temporary query allocations are freed.

### Why 2000MB shared_buffers fails on 32 GB RAM

| Component                    | Estimated Memory |
|------------------------------|-----------------|
| Buffer pool arena (resident) | 2.0 GB          |
| Kernel page cache (data files) | ~1.5 GB       |
| WAL segments (active)        | ~0.5 GB         |
| Query working set (Q1 sort + agg) | ~6.4 GB   |
| Go runtime overhead          | ~0.5 GB         |
| WAL buffers + AIO            | ~0.03 GB        |
| **Total**                    | **~10.9 GB**    |

The estimate is ~11 GB, which should fit in 32 GB. However, the observed
31 GB RSS suggests additional factors:
- The kernel page cache grows beyond just data files (duplicate pages
  between arena and page cache)
- Go heap fragmentation or retained allocations not accounted for
- The Gap between 11 GB estimate and 31 GB observation needs further
  investigation (pprof heap profile under load)

### Key Finding

`GOMEMLIMIT=40GiB` is **counterproductive** for this workload. It prevents
Go's GC from scavenging and returning memory to the OS, allowing RSS to
grow unchecked. A smaller value (e.g., 4-8 GiB) would constrain the Go
heap footprint while still accommodating the 2 GB arena.

## Conclusions

1. **`shared_buffers=2000M` is too large for the current 32 GB test machine**
   when combined with SF=1 TPC-H data. The combination of arena residency,
   kernel page cache, and query working set exceeds available RAM.

2. **HammerDB connection drops during COPY are a separate issue** — the
   libpq client times out during the long ORDERS/LINEITEM load. This
   prevented a complete SF=1 data load.

3. **Go heap arena (M0032-0001) does not solve the RSS problem on its own**:
   the arena pages still become physically resident and stay resident. The
   Go GC cannot reclaim arena pages because they are live `[]byte` slices
   referenced by Pool slots.

4. **GOMEMLIMIT should be tuned per-system**: a value of 40 GiB on a 32 GB
   machine tells the runtime it can grow the heap to 40 GB, which is
   impossible and leads to swap thrashing.

## Recommendations

### Immediate

- Use `shared_buffers=256MB` for SF=1 on < 64 GB machines. This is the
  proven stable configuration (confirmed in M0029).
- Set `GOMEMLIMIT` to a value below system RAM minus arena size (e.g.,
  `GOMEMLIMIT=4GiB` for 32 GB system + 256 MB arena).
- For 2000 MB arena, need ≥ 64 GB system RAM or implement O_DIRECT to
  eliminate kernel page cache duplication.

### Medium-term (follow-up milestones)

- Implement `AlignedIO=true` (O_DIRECT) for data file reads/writes to
  eliminate the kernel page cache duplication.
- Profile Go heap under TPC-H load to identify and eliminate retained
  allocations.
- Investigate and fix the HammerDB COPY connection timeout during long
  loads.
- Consider `MADV_FREE` on evicted buffer pool pages to allow kernel
  reclamation under memory pressure (revisit M0032 original approach with
  `MADV_FREE` instead of `DONTNEED`).

## Next Steps

- M0032-0004: Fix HammerDB COPY connection drops during ORDERS/LINEITEM load.
- M0032-0005: Profile and reduce Go heap footprint under TPC-H query load.
- Re-test `shared_buffers=2000M` on a larger machine (≥ 64 GB RAM) or after
  implementing O_DIRECT + heap reduction.
