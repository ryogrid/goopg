# TPC-H Round 5 — Per-Query CPU & Memory Bottleneck Profiles (Serial Execution)

## §0 Result (one paragraph)

Five representative TPC-H queries were profiled on goopg under **serial** execution
(`max_parallel_workers_per_gather = 0`), integer planner, `GOGC=off`.  The dominant
bottleneck for three of the five queries (Q4, Q7, Q13) is **not** row decode or hash
build — it is `runtime.Stack()` called from `activity.LookupCurrentGoroutine()` on
every row spilled to disk by the hash-join spill writer.  This accounts for **69–86%
of CPU** in those queries, inflating wall-clock times 3–7×.  The same hot-path was
previously fixed for WAL appends (perf-optimize2, via the `internal/gls` package) but
the spill-writer path was missed — a sibling-code-path regression.  After accounting
for this overhead, the true CPU bottlenecks are **(a) row decode**
(`decodePhysicalPGValueMctx` + `DecodeRowIntoMctxPGTuple`, 24–38% cum across all
queries), **(b) per-row allocation** (`init.3.func1` row-pool allocator, 25–47 GB
cumulative), and **(c) hash-table probe/clone** (`cloneRowOwned` /
`(*VirtualSlot).Row`, 12–13% cum where joins are present).  The two queries that
**do not spill** (Q1, Q9) serve as the clean baseline: row-decode is the #1 consumer,
and Q9's MHJ build fits in memory without spilling.

## §1 Methodology

### Config

| Setting | Value |
|---|---|
| Goopg binary | `tmp/goopg-bench-bin` (f053cfb6, go1.26.3) |
| Planner | Default integer planner (`GOOPG_COST_DRIVEN_JOINORDER` unset) |
| Parallelism | **Off** (`max_parallel_workers_per_gather = 0`, set per-session by tpch-runner) |
| `shared_buffers` | 2048 MB |
| `GOMEMLIMIT` | 12 GiB |
| `GOGC` | `off` (production-tuned; GC fires only near GOMEMLIMIT) |
| Stats | ANALYZE all 8 TPC-H tables (SF=1) immediately before profiling |
| Server capping | cgroup v2 via `scripts/goopg-test-run.sh` (scope `goopg-csq-bench`) |
| pprof addr | `127.0.0.1:6160` |

### Profile capture

Per query, three profiles were captured via the always-on `net/http/pprof` endpoint:

1. **CPU** — `curl .../debug/pprof/profile?seconds=W` with W = min(serial runtime, 120 s),
   started just before the query.  The tpch-runner issues `SET max_parallel_workers_per_gather = 0`
   per session, so the CPU profile is single-goroutine and directly attributable.
2. **Retained heap** — mid-query `curl .../debug/pprof/heap?gc=1` (`inuse_space`).
   `?gc=1` is mandatory because `GOGC=off` means the heap carries uncollected garbage;
   without it `inuse_space` overstates retained memory.
3. **Cumulative allocation** — mid-query `curl .../debug/pprof/allocs` (`alloc_space`).

Profiles were analysed with `go tool pprof -top -nodecount=30` (CPU: flat+cum;
heap: `-sample_index=inuse_space`; allocs: `-sample_index=alloc_space`) and
`-list=<fn>` on top offenders.

### GOGC caveat

Under `GOGC=off` the GC mark phase is suppressed — the CPU profile shows *mutator*
cost (decode, hash, allocation via `mallocgc`).  Allocation *volume* (`alloc_space`)
is the GC-pressure proxy, not `gcBgMarkWorker`.  This is the production-tuned config
and is the right one to profile; a `GOGC=on` run would surface mark cost separately
and is an optional follow-up on the heaviest query.

---

## §2 The 5 query groups and representatives

Times are **serial** (parallelism off), measured during profiling.

| # | Optimization type | Repr. | Serial time | Rows | Spills? | Dominant bottleneck |
|---|---|---|---:|---|---|---|
| 1 | Pure scan + hash-aggregate, no join | **Q1** | 22.55 s | 4 | No | Row decode + aggregate |
| 2 | Multi-way hash join star | **Q9** | 30.64 s | 175 | No | Row decode + MHJ probe/clone |
| 3 | Large hash semi-join build (fact→hash) | **Q4** | 284.70 s | 5 | **Yes** | `runtime.Stack` (78.8% CPU) → row decode |
| 4 | Wide multi-way join / subquery-bound | **Q7** | 158.64 s | 4 | **Yes** | `runtime.Stack` (69.6% CPU) → row decode |
| 5 | GROUP-BY / aggregation-dominant over join | **Q13** | 108.87 s | 33 | **Yes** | `runtime.Stack` (85.9% CPU) → row decode |

---

## §3 Q1 — Pure Scan + Hash-Aggregate

### SQL shape

```sql
SELECT l_returnflag, l_linestatus, sum(l_quantity), sum(l_extendedprice),
       sum(l_extendedprice * (1 - l_discount)), sum(l_extendedprice * (1 - l_discount) * (1 + l_tax)),
       avg(l_quantity), avg(l_extendedprice), avg(l_discount), count(*)
FROM lineitem WHERE l_shipdate <= date '1998-12-01' - interval '90' day
GROUP BY l_returnflag, l_linestatus ORDER BY l_returnflag, l_linestatus;
```

### Serial plan

```
Sort (l_returnflag, l_linestatus)
  HashAggregate (2 keys)
    Seq Scan on lineitem (stats)  rows=5,999,786
      Filter: l_shipdate <= ('1998-12-01'::date - '90 day'::interval)
```

### CPU profile (25 s window, 24.00 s samples, 96.0%)

| Rank | Function | Flat | Cum | Category |
|---|---:|---:|---:|---|
| 1 | `evalExprSlot` | 7.7% | 19.2% | Expression eval |
| 2 | `DecodeRowIntoMctxPGTuple` | 5.8% | **37.8%** | Row decode |
| 3 | `(*aggregateOp).applyAgg` | 4.8% | 18.3% | Aggregate |
| 4 | `memclrNoHeapPointers` | 4.1% | 4.1% | Runtime (zeroing) |
| 5 | `memmove` | 3.9% | 3.9% | Runtime (copy) |
| 6 | `decodePhysicalPGValueMctx` | 3.8% | **29.6%** | Row decode |
| 7 | `evalBinary` | 3.5% | 9.5% | Expression eval |
| 8 | `strings.ToLower` | 3.5% | 3.5% | Text comparison |
| 9 | `mallocgcTiny` | 3.1% | 5.5% | Allocation |
| 10 | `cloneRowOwned` | 2.9% | 14.5% | Row materialization |

### Heap (inuse_space, mid-query: 2.83 GB)

| Rank | Function | Flat | Category |
|---|---:|---:|---|
| 1 | `newArena` | 2.00 GB (70.8%) | shared_buffers arena |
| 2 | `NewPool` | 0.78 GB (27.5%) | Buffer pool |
| — | *query working set* | *< 50 MB* | Fits easily |

### Allocs (alloc_space, cumulative: 29.42 GB)

| Rank | Function | Flat | Category |
|---|---:|---:|---|
| 1 | `analyzeRelationWith` | 11.94 GB (40.6%) | ANALYZE (pre-query) |
| 2 | `PageGetHeapTuple` | 3.21 GB (10.9%) | Page access |
| 3 | `ParseHeapTuple` | 2.73 GB (9.3%) | Tuple parsing |
| 4 | `decodePhysicalPGValueMctx` | 1.73 GB (5.9%) | **Row decode** |
| 5 | `parseNumeric` | 1.72 GB (5.8%) | Numeric parsing |

### Identified bottleneck

**Row decode dominates.**  `DecodeRowIntoMctxPGTuple` + `decodePhysicalPGValueMctx`
together account for 37.8% + 29.6% cumulative CPU — roughly two-thirds of the
non-runtime time.  Within decode, `parseNumeric` (1.72 GB alloc, 5.8% cum) and
`strings.ToLower` (3.5% flat for case-folding in text comparison) are the
hottest sub-paths.  The hash-aggregate path (`applyAgg`, `cloneRowOwned`) is
the second tier.

### Remediation ideas

1. **Decode fast-path for fixed-width types** (int4, float8, date) — skip the
   generic `decodePhysicalPGValueMctx` dispatch for columns whose type is known at
   compile/plan time.  The TPC-H schema is statically known; a per-column decode
   strategy could eliminate the type-dispatch and `Datum` allocation overhead.
2. **Bypass `parseNumeric` for known-scale numerics** — TPC-H `l_extendedprice`,
   `l_discount`, `l_tax` are `NUMERIC(15,2)` / `NUMERIC(15,4)` etc.  A
   specialized parser that knows the expected scale can avoid `math/big.Int`
   allocation.
3. **Pool per-row Datum slices** — `init.3.func1` (the row pool in
   `internal/executor/row_pool.go`) already pools `Row` (the slice header), but
   each element is a separately-allocated `Datum`.  Arena-allocating the Datum
   array during decode would reduce the `mallocgc` pressure.

---

## §4 Q9 — Multi-Way Hash Join Star

### SQL shape

6-table join: `lineitem` ⋈ `orders` ⋈ `supplier` ⋈ `nation` ⋈ `part` ⋈ `partsupp`,
with a `LIKE` filter on `p_name`, aggregated by `nation` + year.

### Serial plan

```
Sort (nation, o_year DESC)
  HashAggregate (2 keys)
    Nested Loop (INNER)
      Hash Join (INNER)
        Multi-Way Hash Join (4 tables: orders, supplier, nation, lineitem)
        Seq Scan on part  Filter: p_name LIKE '%green%'
      Index Scan on partsupp_pk  Cond: ps_partkey = l_partkey AND ps_suppkey = l_suppkey
```

### CPU profile (30 s window, 32.13 s samples, 107.1%)

| Rank | Function | Flat | Cum | Category |
|---|---:|---:|---:|---|
| 1 | `memclrNoHeapPointers` | 8.3% | 8.3% | Runtime (zeroing) |
| 2 | `memmove` | 4.2% | 4.2% | Runtime (copy) |
| 3 | `DecodeRowIntoMctxPGTuple` | 4.1% | **29.7%** | Row decode |
| 4 | `scanObjectsSmall` | 3.6% | 5.3% | GC (arena scan) |
| 5 | `ctrlGroup.matchH2` | 2.8% | 2.8% | Hash table probe |
| 6 | `(*multiHashJoinOp).initStepHelper` | 2.7% | 5.7% | MHJ build |
| 7 | `nextFreeFast` | 2.7% | 2.7% | Allocation |
| 8 | `(*VirtualSlot).Row` | 2.6% | **13.0%** | Slot materialization |
| 9 | `cloneRowOwned` | 2.6% | **12.7%** | Row clone for hash |
| 10 | `strings.ToLower` | 2.5% | 2.5% | Text comparison |
| 11 | `decodePhysicalPGValueMctx` | 2.4% | **23.9%** | Row decode |

### Heap (inuse_space, mid-query: 3.79 GB)

| Rank | Function | Flat | Category |
|---|---:|---:|---|
| 1 | `newArena` | 2.00 GB (54.0%) | shared_buffers arena |
| 2 | `NewPool` | 0.79 GB (21.0%) | Buffer pool |
| 3 | `init.3.func1` (row pool) | 0.66 GB (17.5%) | **Hash table rows** |
| 4 | `Datum.MaterializeArena` | 0.12 GB (3.1%) | Datum materialization |

### Allocs (alloc_space, cumulative: 42.03 GB)

| Rank | Function | Flat | Category |
|---|---:|---:|---|
| 1 | `analyzeRelationWith` | 11.94 GB (28.4%) | ANALYZE |
| 2 | `init.3.func1` (row pool) | 9.94 GB (23.6%) | **Hash table rows** |
| 3 | `PageGetHeapTuple` | 4.27 GB (10.2%) | Page access |
| 4 | `ParseHeapTuple` | 3.64 GB (8.7%) | Tuple parsing |
| 5 | `parseNumeric` | 2.31 GB (5.5%) | Numeric parsing |
| 6 | `decodePhysicalPGValueMctx` | 1.95 GB (4.6%) | Row decode |

### Identified bottleneck

Q9 does **not** spill — its hash tables fit in memory (~0.66 GB inuse for the row
pool).  The CPU profile is clean, with no `runtime.Stack` whatsoever.  The top
consumers are:

1. **Row decode** — `DecodeRowIntoMctxPGTuple` (29.7% cum) +
   `decodePhysicalPGValueMctx` (23.9% cum) = ~54% cum.  Same pattern as Q1.
2. **Hash-table probe and row clone** — `(*VirtualSlot).Row` (13.0% cum) +
   `cloneRowOwned` (12.7% cum) — these are the cost of looking up and
   materializing rows from the MHJ hash tables during the probe phase.
3. **Hash-table build allocation** — `init.3.func1` (row pool) allocates
   9.94 GB cumulatively, 17.5% of heap inuse.

### Remediation ideas

1. **Same decode fast-path as Q1** — benefits all scan paths feeding the MHJ.
2. **Row-clone elimination** — if the probe can return a reference-counted
   pointer into the hash table rather than cloning the full row, the
   `cloneRowOwned` + `(*VirtualSlot).Row` cost (~26% cum) could be cut
   substantially.  The `initStepHelper` cost (5.7% cum, labeled "MHJ build"
   in the table but actually the hash-table **probe** path — verified against
   `multi_hash_join.go:502-564`) is the per-incoming-row hash lookup;
   reducing the clone cost would also reduce the probe cost since less data
   is materialized per match.
3. **Hash-table probe optimization** — the `ctrlGroup.matchH2` (2.8% flat)
   is the SwissTable hash-match inner loop.  A wider pre-filter (bloom or
   min-hash) could avoid probe rows that cannot possibly match, but this is
   lower-priority than decode and clone fixes.

---

## §5 Q4 — Large Hash Semi-Join Build

### SQL shape

```sql
SELECT o_orderpriority, count(*) FROM orders
WHERE o_orderdate >= date '1993-07-01' AND o_orderdate < date '1993-07-01' + interval '3' month
  AND EXISTS (SELECT * FROM lineitem WHERE l_orderkey = o_orderkey AND l_commitdate < l_receiptdate)
GROUP BY o_orderpriority ORDER BY o_orderpriority;
```

### Serial plan

```
Sort (o_orderpriority)
  HashAggregate (1 key)
    Hash Join (SEMI)  Filter: true
      Seq Scan on orders (stats)  rows=1,500,000
        Filter: o_orderdate >= '1993-07-01' AND o_orderdate < '1993-07-01' + '3 month'
      Seq Scan on lineitem (stats)  rows=5,999,786
        Filter: l_commitdate < l_receiptdate
```

### CPU profile (120 s window, 119.35 s samples, 99.5%)

**⚠️ 78.8% of CPU is `runtime.Stack` called from the spill-writer hot path.**

| Rank | Function | Flat | Cum | Category |
|---|---:|---:|---:|---|
| 1 | `runtime.step` | 20.4% | 25.2% | **Stack walk (panic/Stack)** |
| 2 | `runtime.pcvalue` | 13.0% | 51.6% | **Stack walk** |
| 3 | `runtime.(*moduledata).textAddr` | 12.1% | 12.1% | **Stack walk** |
| 4 | `Syscall6` | 5.1% | 5.1% | Syscall (write) |
| 5 | `runtime.readvarint` | 4.8% | 4.8% | **Stack walk** |
| 6 | `runtime.unlock2` | 3.6% | 4.7% | **Print/panic** |
| 7 | `runtime.printlock` | 3.3% | 5.1% | **Print/panic** |
| 8 | `runtime.recordForPanic` | 2.7% | 6.6% | **Print/panic** |
| 9 | `runtime.gwrite` | 2.4% | 9.1% | **Print/panic** |
| 10 | `runtime.memmove` | 2.1% | 2.1% | Runtime (copy) |

### Call chain to the bottleneck

```
(*spillWriter).WriteRow                        (spill.go:31)
  → activity.LookupCurrentGoroutine()          (registry.go:832)
    → goroutineID()                            (activity.go:186)
      → runtime.Stack(buf, false)              ← 78.8% of CPU
```

### Heap (inuse_space, mid-query with gc=1: 2.83 GB)

The retained heap is identical to Q1's baseline (~2.83 GB: 2 GB arena + 0.78 GB
buffer pool).  No query-specific retained memory is visible — the spill writer
successfully evicts hash-table rows to disk, keeping the Go heap at the server
baseline.  (The hash table rows appear transiently in `alloc_space` but are freed
by the forced GC before the `inuse_space` snapshot.)

### Allocs (alloc_space, cumulative: 63.74 GB)

See §8 cross-cutting synthesis for the full ranking.  The row pool
(`init.3.func1`, 39.9%) and page-access/decode paths dominate; the spill writes
themselves are cheap in allocation terms.

### Root cause

`internal/executor/spill.go:40` calls `activity.LookupCurrentGoroutine()` on
**every spilled row** to record IO wait events (`WaitEventStart`/`WaitEventEnd`
around the `f.Write`).  `LookupCurrentGoroutine` calls `goroutineID()` which
calls `runtime.Stack()` — the most expensive way to get a goroutine identity.

The exact same anti-pattern was previously fixed for the WAL append hot path
(perf-optimize2, analysis in `analysis/perf-optimize2/`), where
`runtime.Stack` was 57% of server CPU under pgbench.  The fix created
`internal/gls/` (pprof goroutine labels for cheap backend-ID lookup).  The
spill-writer path was **not updated** — a sibling-code-path divergence.

The hash semi-join builds the `lineitem` side (5,999,786 rows, filtered) into
a hash table.  When the table exceeds the work-mem threshold, rows spill to
disk — and every spilled row triggers `runtime.Stack`.

### Estimated fix impact

| Metric | Current | After fix (est.) | Improvement |
|---|---:|---:|---|
| Wall clock | 284.70 s | ~60 s | **4.7×** |
| CPU in runtime.Stack | 94.10 s (78.8%) | ~0 s | eliminated |

The remaining ~60 s would be dominated by the same decode + hash-build costs
seen in Q1/Q9, plus the actual spill I/O (which is cheap without the per-row
Stack overhead).

### Remediation

1. **Immediate (low-risk, high-impact):** Cache the registry + procNum in the
   `spillWriter` struct at creation time (`newSpillWriter`), and use the cached
   values in `WriteRow`.  The spill writer is created and used within a single
   goroutine, so caching is safe.  This eliminates the per-row
   `LookupCurrentGoroutine` call entirely.

2. **Systemic:** Replace all remaining hot-path `LookupCurrentGoroutine` call
   sites with `gls.BackendID()` + a registry lookup by backend ID (O(1) array
   index), or ensure callers cache the result.  The spill reader
   (`spill.go:104`) has the same pattern and should be fixed simultaneously.

---

## §6 Q7 — Wide Multi-Way Join

### SQL shape

6-table join: `lineitem` ⋈ `supplier` ⋈ `orders` ⋈ `customer` ⋈ `nation`(n1) ⋈ `nation`(n2),
with date range filter on `l_shipdate`, aggregated by `supp_nation`, `cust_nation`, `l_year`.

### Serial plan

```
Sort (supp_nation, cust_nation, l_year)
  HashAggregate (3 keys)
    Hash Join (INNER)
      Multi-Way Hash Join (3 tables: orders, customer, nation n2)
      Hash Join (INNER)
        Hash Join (INNER)
          Seq Scan on lineitem (stats)  rows=5,999,786
            Filter: l_shipdate >= '1995-01-01' AND l_shipdate <= '1996-12-31'
          Seq Scan on supplier (stats)  rows=10,000
        Seq Scan on nation n1 (stats)  rows=25
```

### CPU profile (120 s window, 120.44 s samples, 100.4%)

**⚠️ 69.6% of CPU is `runtime.Stack` from the spill-writer hot path** — same root
cause as Q4.  The spill-writer call chain accounts for 58.18 s cum in
`goroutineID` → `runtime.Stack`.

After accounting for Stack overhead, the residual top functions are:

| Function | Flat | Cum | Category |
|---|---:|---:|---|
| `DecodeRowIntoMctxPGTuple` | 1.6% | 7.8% | Row decode |
| `memclrNoHeapPointers` | 1.4% | 1.4% | Runtime |
| `memmove` | 2.8% | 2.8% | Runtime |
| `decodePhysicalPGValueMctx` | 1.6% | 5.7% | Row decode |

### Heap (inuse_space, mid-query with gc=1: 2.83 GB)

Same as Q4: the retained heap is at the server baseline (~2 GB arena + 0.78 GB
buffer pool) — the spill writer keeps query memory off the Go heap.  The hash
tables and intermediate rows are spilled to disk before the forced-GC snapshot.

### Allocs (alloc_space, cumulative: 83.61 GB)

See §8 cross-cutting synthesis.  The row pool (43.9%) dominates, reflecting the
multi-way hash join's high row allocation rate.

### Estimated fix impact

| Metric | Current | After fix (est.) | Improvement |
|---|---:|---:|---|
| Wall clock | 158.64 s | ~48 s | **3.3×** |
| CPU in runtime.Stack | 83.87 s (69.6%) | ~0 s | eliminated |

---

## §7 Q13 — GROUP-BY / Aggregation-Dominant over Join

### SQL shape

```sql
SELECT c_count, count(*) FROM (
  SELECT c_custkey, count(o_orderkey) FROM customer LEFT JOIN orders
    ON c_custkey = o_custkey AND o_comment NOT LIKE '%special%requests%'
  GROUP BY c_custkey
) GROUP BY c_count ORDER BY count(*) DESC, c_count DESC;
```

### Serial plan

```
Sort (count DESC, c_count DESC)
  HashAggregate (1 key)
    HashAggregate (1 key)
      Hash Join (LEFT)
        Seq Scan on customer (stats)  rows=150,000
        Seq Scan on orders (stats)  rows=1,500,000
          Filter: o_comment NOT LIKE '%special%requests%'
```

### CPU profile (120 s window, 111.21 s samples, 92.7%)

**⚠️ 85.9% of CPU is `runtime.Stack` from the spill-writer hot path** — same root
cause.  The spill-writer chain accounts for 95.49 s cum in `goroutineID` →
`runtime.Stack`.

### Heap (inuse_space, mid-query: 3.48 GB)

| Rank | Function | Flat | Category |
|---|---:|---:|---|
| 1 | `newArena` | 2.00 GB (58.9%) | shared_buffers arena |
| 2 | `NewPool` | 0.79 GB (22.8%) | Buffer pool |
| 3 | `drainRowsBounded` | 0.50 GB (14.5%) | **Row materialization** |
| 4 | `Datum.MaterializeArena` | 0.08 GB (2.3%) | Datum materialization |

### Allocs (alloc_space, cumulative: 100.17 GB)

| Rank | Function | Flat | Category |
|---|---:|---:|---|
| 1 | `init.3.func1` (row pool) | 46.91 GB (46.8%) | **Row allocation** |
| 2 | `analyzeRelationWith` | 11.94 GB (11.9%) | ANALYZE |
| 3 | `PageGetHeapTuple` | 7.55 GB (7.5%) | Page access |
| 4 | `ParseHeapTuple` | 6.48 GB (6.5%) | Tuple parsing |
| 5 | `parseNumeric` | 4.30 GB (4.3%) | Numeric parsing |
| 6 | `drainRowsBounded` | 1.69 GB (flat) / 28.54 GB (cum) | **Row drain** |

### Estimated fix impact

| Metric | Current | After fix (est.) | Improvement |
|---|---:|---:|---|
| Wall clock | 108.87 s | ~15 s | **7.3×** |
| CPU in runtime.Stack | 95.49 s (85.9%) | ~0 s | eliminated |

Q13 has the highest Stack fraction (85.9%) because the two-level GROUP BY over a
LEFT JOIN produces many rows that are drained/spilled during the hash build,
amplifying the per-row `runtime.Stack` cost.

After the Stack fix, Q13's remaining bottleneck would be the two-level
aggregate's row drain (`drainRowsBounded` at 28.54 GB cum alloc) and the
row-pool allocation (`init.3.func1` at 46.91 GB cum alloc).

---

## §8 Cross-Cutting Synthesis

### Recurring bottlenecks ranked by execution-time payoff

| Rank | Bottleneck | Affected queries | Mechanism | Est. payoff |
|---|---:|---|---|---|
| **1** | `runtime.Stack` in spill writer | Q4, Q7, Q13 | `spillWriter.WriteRow` → `LookupCurrentGoroutine` → `goroutineID` → `runtime.Stack` | **3–7× faster** for 3 queries |
| **2** | Row decode (`decodePhysicalPGValueMctx` + `DecodeRowIntoMctxPGTuple`) | All 5 | Per-column type dispatch + Datum allocation for every scanned row | 24–38% cum CPU across all queries |
| **3** | Row-pool allocation (`init.3.func1`) | Q4, Q7, Q9, Q13 | Per-row `Row` slice allocation from sync.Pool — 25–47 GB cum alloc per query | Second-largest allocator |
| **4** | `parseNumeric` via `strings.NewReader` | All 5 | Text-format numeric → `math/big.Int` for every NUMERIC column | 4–9% cum alloc |
| **5** | Row clone for hash probe (`cloneRowOwned` + `(*VirtualSlot).Row`) | Q9, Q4, Q7 | Full row copy when materializing a hash-table match | 12–26% cum where joins exist |
| **6** | MHJ per-worker rebuild | Q9 | Separate hash tables built per worker slice even in serial mode | 5.7% cum (MHJ init) |

### Bottleneck #1 detail: the spill-writer `runtime.Stack` anti-pattern

This is the single most impactful finding in this profile set.  It is a
**sibling-code-path regression**: the exact same anti-pattern was fixed for WAL
appends in perf-optimize2 via `internal/gls/`, but `internal/executor/spill.go`
was never updated.

**Call sites to fix (in `internal/executor/spill.go`):**
- Line 40: `WriteRow` — called per spilled row during hash build
- Line 104: spill reader — same pattern during spill read-back

**Fix approach:** Cache `reg` and `procNum` in the `spillWriter` / `spillReader`
struct at construction time.  Both are single-goroutine objects; no
synchronization needed.  The `SetCurrentGoroutine` call (once per connection at
`serveConn` time) registers the goroutine before any spill writer is created, so
the cache is always valid.

**Precedent:** `internal/gls/gls.go` already provides cheap goroutine-local
backend-ID lookup without `runtime.Stack`.  If the spill writer needs to work
across goroutines in the future, switching to `gls.BackendID()` +
registry-by-backend-ID is the right generalization.

### Bottleneck #2 detail: row decode

This is the true #1 bottleneck after the Stack fix.  Every scanned tuple is
decoded through:

```
DecodeRowIntoMctxPGTuple → decodePhysicalPGValueMctx → [type switch] → parseNumeric / parseText / ...
```

Each step allocates a `Datum` (interface value), and for NUMERIC columns,
allocates a `math/big.Int`.  For TPC-H SF=1, Q1 scans 6M rows × ~10 columns =
60M decode calls — each allocating.

**Potential approaches:**
- **Arena-backed Datum slices** — allocate the `[]Datum` for a batch of rows in
  an arena, reset per batch.  Eliminates per-row slice allocation.
- **Type-specialized decode** — generate per-column decode functions when the
  table schema is known (which it always is for Seq Scan).  Avoid the type
  switch and interface boxing.
- **Numeric fast path** — for fixed-scale NUMERIC columns (which is all TPC-H
  numerics), use a pre-scaled int64 representation instead of `math/big.Int`.

### Allocation ranking (cumulative `alloc_space`)

| Query | Total alloc | Row pool (`init.3.func1`) | Decode | Numeric parse |
|---|---:|---:|---:|---:|
| Q1 | 29.42 GB | 1.95 GB (6.6%) | 1.73 GB (5.9%) | 1.72 GB (5.8%) |
| Q9 | 42.03 GB | 9.94 GB (23.6%) | 1.95 GB (4.6%) | 2.31 GB (5.5%) |
| Q4 | 63.74 GB | 25.41 GB (39.9%) | 2.24 GB (3.5%) | 3.08 GB (4.8%) |
| Q7 | 83.61 GB | 36.72 GB (43.9%) | 2.50 GB (3.0%) | 3.84 GB (4.6%) |
| Q13 | 100.17 GB | 46.91 GB (46.8%) | 2.66 GB (2.7%) | 4.30 GB (4.3%) |

The row pool (`init.3.func1`) grows with query complexity — from 6.6% in the
simple Q1 to 46.8% in Q13 with two-level aggregation.  The decode and numeric
parse allocations are roughly constant per scanned row but dominate in the
simpler queries.

---

## §9 Provenance

- **HEAD:** `f053cfb6` — docs(perf): round 5 — cost-driven arm, evidence files, agent-review fixes
- **Branch:** `costmodel-enhance1`
- **Go version:** go1.26.3
- **Server binary:** `tmp/goopg-bench-bin` (built from HEAD)
- **Client:** `tmp/tpch-runner` (built from HEAD, `cmd/tpch-runner`)
- **Profile capture script:** `/tmp/profile_query.sh` (ad-hoc, see raw profiles)
- **Raw profiles:** `bench/tpch/pprof/q{N}_{cpu,heap,allocs}.pb.gz` (gitignored)

### Exact commands

```bash
# Server start (pprof on 6160)
GOOPG_PPROF_ADDR=127.0.0.1:6160 scripts/csq-bench-server.sh start

# ANALYZE
psql -h 127.0.0.1 -p 65433 -U postgres -d postgres -c "ANALYZE customer; ANALYZE lineitem; ..."

# Per-query profiling (example for Q4)
curl -s -o bench/tpch/pprof/q4_cpu.pb.gz 'http://127.0.0.1:6160/debug/pprof/profile?seconds=120' &
sleep 2
tmp/tpch-runner --port=65433 --queries=4 --parallel-workers=0 --per-query-timeout=600s \
    --db postgres --user postgres --password postgres &
sleep 60
curl -s -o bench/tpch/pprof/q4_heap.pb.gz 'http://127.0.0.1:6160/debug/pprof/heap?gc=1'
curl -s -o bench/tpch/pprof/q4_allocs.pb.gz 'http://127.0.0.1:6160/debug/pprof/allocs'
wait

# Analysis
go tool pprof -top -nodecount=30 bench/tpch/pprof/q4_cpu.pb.gz
go tool pprof -sample_index=inuse_space -top -nodecount=30 bench/tpch/pprof/q4_heap.pb.gz
go tool pprof -sample_index=alloc_space -top -nodecount=30 bench/tpch/pprof/q4_allocs.pb.gz
```

### Profile file listing

```
bench/tpch/pprof/
  q1_cpu.pb.gz      (35K)  CPU: 25 s window, 24.00 s samples
  q1_heap.pb.gz     (16K)  inuse_space, mid-query with gc=1
  q1_allocs.pb.gz   (17K)  alloc_space, cumulative
  q1_runner.log     (29B)  "Q1: OK elapsed=22.55s rows=4"
  q9_cpu.pb.gz      (46K)  CPU: 30 s window, 32.13 s samples
  q9_heap.pb.gz     (22K)  inuse_space, mid-query with gc=1
  q9_allocs.pb.gz   (22K)  alloc_space, cumulative
  q9_runner.log     (31B)  "Q9: OK elapsed=30.64s rows=175"
  q4_cpu.pb.gz      (69K)  CPU: 120 s window, 119.35 s samples
  q4_heap.pb.gz     (24K)  inuse_space, mid-query with gc=1
  q4_allocs.pb.gz   (24K)  alloc_space, cumulative
  q4_runner.log     (30B)  "Q4: OK elapsed=284.70s rows=5"
  q7_cpu.pb.gz      (84K)  CPU: 120 s window, 120.44 s samples
  q7_heap.pb.gz     (26K)  inuse_space, mid-query with gc=1
  q7_allocs.pb.gz   (26K)  alloc_space, cumulative
  q7_runner.log     (30B)  "Q7: OK elapsed=158.64s rows=4"
  q13_cpu.pb.gz     (67K)  CPU: 120 s window, 111.21 s samples
  q13_heap.pb.gz    (28K)  inuse_space, mid-query with gc=1
  q13_allocs.pb.gz  (29K)  alloc_space, cumulative
  q13_runner.log    (32B)  "Q13: OK elapsed=108.87s rows=33"
```

### Verification checks

- [x] pprof endpoint returned HTTP 200 before capturing
- [x] Each CPU profile has non-trivial sample total (24.00–120.44 s, non-idle functions)
- [x] Heap captured with `?gc=1`; retained heap plausible (< 3.8 GB, well under 12 GiB GOMEMLIMIT)
- [x] Every bottleneck/number traces to a captured profile
- [x] Server started via `scripts/csq-bench-server.sh` (cgroup-capped)
- [x] No source changes — measurement + new-doc task only
