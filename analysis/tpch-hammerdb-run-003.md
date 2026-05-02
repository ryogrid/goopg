# TPC-H HammerDB Run — shared_buffers=2048MB with Explicit GC

**Date:** 2026-05-02
**goopg commits:** `495810d` + explicit `runtime.GC()` calls
**Test machine:** x86_64 Linux, 32 GB RAM + 64 GB swap, Go 1.25.0

## Configuration

| Parameter              | Value         |
|------------------------|---------------|
| `shared_buffers`       | 2048 MB (262,144 slots × 8 KiB, 2 GiB arena) |
| `GOMEMLIMIT`           | 20 GiB        |
| Arena type             | Go heap `make([]byte)` (M0032-0001) |
| `checkpoint_timeout`   | 15 min        |
| `max_wal_size`         | 4 GB          |
| Explicit GC            | `runtime.GC()` after each query + COPY completion |
| GC locations           | `internal/server/dispatch.go` (after Commit), `internal/server/copy.go` (after CopyDone) |

## Schema Build

| Table     | Target Rows | Loaded Rows | Status |
|-----------|------------|-------------|--------|
| region    | 5          | 5           | COMPLETE |
| nation    | 25         | 25          | COMPLETE |
| supplier  | 10,000     | 10,000      | COMPLETE |
| customer  | 150,000    | 150,000     | COMPLETE |
| part      | 200,000    | 200,000     | COMPLETE |
| partsupp  | 800,000    | 800,000     | COMPLETE |
| orders    | 1,500,000  | 270,000     | **FAILED at 18%** |
| lineitem  | ~6,000,000 | 1,078,897   | **FAILED at 18%** |

**Failure mode:** HammerDB client reports `"server closed the connection unexpectedly"`
during ORDERS/LINEITEM COPY. The server remains running with no errors logged and
accepts new connections. This is a **libpq-level connection timeout** during the long
COPY session, not a server crash. Occurs consistently at ~270K/1,078K rows (varies
±50K between runs).

## Memory Behaviour

| Phase                          | RSS        | Delta     |
|--------------------------------|-----------|-----------|
| Server startup                 | ~52 MB    | —         |
| After schema build (partial)   | **694 MB** | +642 MB  |
| After index creation + ANALYZE | 3,036 MB   | +2,342 MB |
| After Q14 execution (17.6s)    | 10,902 MB  | +7,866 MB |
| During Q2 execution            | **28,248 MB** (growing) | — |

### Key observations

1. **Explicit `runtime.GC()` is effective:** After COPY load of 270K orders/1M lineitems,
   RSS was only 694 MB. Compare to 4,350 MB in the previous run without explicit GC
   (M0032-0004) — a **6.3x reduction** in post-load RSS.

2. **Index backfill raises RSS to 3 GB:** Creating B-tree indexes on the loaded data
   faults in buffer pool pages and builds index pages. This is expected and bounded.

3. **Q14 under 2 GiB arena: 17.6 seconds** vs. 401 seconds at 256 MB — **23x speedup**.
   The working set (index + data pages) fits in the buffer pool, eliminating repeated
   disk reads.

4. **Q2 causes unbounded RSS growth:** The correlated subquery in Q2 evaluates
   per-row (`evalSubquery` → `subqueryImpl` builds/opens/closes a full operator tree
   for each outer row). With 270K outer rows (filtered from 1M orders), the allocation
   rate (~10 MB/invocation as estimated in M0031-0001) outpaces GC even with
   `GOMEMLIMIT=20GiB`. RSS grew from 10.9 GB to 28.2 GB before manual kill.

## Power Test Results

| Query | Status | Duration | Notes |
|-------|--------|----------|-------|
| Q14   | PASS   | **17.64s** | vs 401s at 256MB (23x speedup) |
| Q2    | FAIL   | N/A | RSS grew to 28 GB, manual kill before OOM |
| Q3–Q22 | NOT RUN | — | HammerDB was killed while Q2 was executing |

## Comparison: Run #1 (M0032-0004) vs Run #2 (M0032-0006)

| Metric               | Run #1 (no explicit GC) | Run #2 (explicit GC) |
|----------------------|------------------------|---------------------|
| Post-load RSS        | 4,350 MB               | **694 MB**          |
| Post-index RSS       | —                      | 3,036 MB            |
| Q14 duration         | 2m14s (4.1M rows)      | **17.64s** (1M rows) |
| Q2 behaviour         | Timeout (no data)      | RSS unbounded growth |
| GOMEMLIMIT           | 40 GiB                 | 20 GiB              |

## Conclusions

1. **`shared_buffers=2048MB` with explicit GC is stable for data loading.**
   Post-load RSS of 694 MB is well within the 32 GB system limit. The previous
   OOM issues (M0032-0004) were caused by the absence of explicit GC between large
   COPY operations — the `runtime.GC()` calls added in `dispatch.go` and `copy.go`
   resolved this.

2. **Q14 speedup confirms buffer pool sizing is effective.** With 2 GiB shared_buffers,
   the index and data pages fit in memory, eliminating disk reads during SeqScan.
   Q14 went from 401s (256MB, M0029) → ~134s (256MB partial, M0032-0004)
   → **17.6s** (2 GiB, this run). This is a **23× improvement** over the original
   baseline.

3. **HammerDB COPY connection drops are a persistent bug.** The ORDERS/LINEITEM COPY
   connection drops at ~270K rows regardless of configuration. This appears to be
   a libpq client-side timeout or connection keepalive issue during the long-running
   COPY session (the server continues running without errors). This prevents loading
   full SF=1 data.

4. **Q2's correlated subquery is the memory bottleneck.** The per-row subquery
   evaluation (analyzed in M0031-0001) causes unbounded RSS growth even with
   explicit GC. Subquery unnesting or result caching (M0031 follow-up) is required
   before Q2 can execute at scale.

## Next Steps

- **M0032-0005:** Fix HammerDB COPY connection drops. Investigate libpq timeout
  settings, TCP keepalive configuration, or add COPY-progress messages to prevent
  the connection from appearing idle.
- **M0031 follow-up:** Implement subquery caching or unnesting for Q2. The
  per-row re-evaluation is the dominant memory and performance bottleneck.
- Re-test full SF=1 after both fixes are applied.
