# 03 — Memory and Allocations

Source: `profiles/goopg_c<C>_<wl>.heap.pb.gz` (inuse), `.allocs.pb.gz` (alloc_space), correlated with §2 GC CPU costs.

## §3.1 Heap snapshot — `inuse` is not the problem

Heap inuse, goopg c=10 select-only (snap at T+150):

```
2.68 GB total
 2.50 GB  internal/storage.newArena         (93%)  — shared_buffers (2560 MB)
 0.14 GB  internal/wal.newWALBuffer         (5%)   — wal_buffers (128 MB)
 0.02 GB  internal/wal.NewMemRing           (0.6%)
 < 0.05 GB everything else
```

In other words, **inuse heap is exactly the static buffer pool + WAL buffer**. Nothing else is retained. Inuse heap is **not** a tuning target.

The same snapshot at c=100 select-only shows ~2.7 GB inuse — identical to c=10. Goopg does not leak; it churns.

## §3.2 Alloc-space — the real story

`alloc_space` measures cumulative allocation rate over the 120 s sample. Top allocators by workload, normalised per pgbench transaction:

| pattern | TPS | total alloc / 120 s | alloc / txn |
|---|---:|---:|---:|
| c=10 select-only       | 2 307.7   | 5 335 MB  | **19 KB**  |
| c=10 simple-update     |   410.5   | 11 802 MB | **240 KB** |
| c=10 standard          |   349.3   | 11 350 MB | **271 KB** |
| c=50 select-only       | 5 033.9   | 9 100 MB  | 15 KB      |
| c=50 simple-update     |   347.3   | 14 590 MB | **350 KB** |
| c=50 standard          |   338.5   | 14 200 MB | **350 KB** |
| c=100 select-only      | 6 399.6   | 8 785 MB  | 11 KB      |
| PG c=10 select-only    | 37 062.3  | — (no GC) | ~0         |

PG, by contrast, allocates *zero* GC-scanned objects per query: every per-query allocation goes into a `MemoryContext` that is reset (a pointer bump) at end of statement. PG's actual per-statement memory cost is ~2 KB of bump allocation that survives 1 µs before being freed.

## §3.3 Where the allocations come from

### c=10 select-only top allocators (excluding static buffer pool):

```
cum%   site                                          file:line
26.4%  planner.Plan (chain)                          internal/planner/planner.go:32
24.0%  planner.planSelect                            internal/planner/planner.go:<planSelect>
13.9%  server.executeOneSimpleStmt                   internal/server/dispatch.go:740
 6.1%  parser.Parse                                  internal/parser/parser.go:82
 5.0%  executor.Row materialise                      internal/executor/executor.go:259
 ~3%   protocol.WriteFrame buffer growth             internal/protocol/...
```

### c=10 simple-update top allocators:

```
cum%   site                                          file:line
35.8%  executor.updateOp.Next → updateViaIndex      internal/executor/operators_storage.go:985
27.8%  executor.tryApplyHOTUpdate                    internal/executor/operators_storage.go:<HOT>
13.9%  executor.executeOneSimpleStmt
 ~6%   wal.Writer.Append (record buffer building)    internal/wal/writer.go:611
 ~5%   parser.Parse
 ~4%   planner.Plan
```

For writes, **the executor itself becomes the biggest allocator**, ahead of the planner. Each `UPDATE ... WHERE aid = ?` allocates:

- Row materialisation slots for the WHERE-side index probe.
- A new tuple version for the HOT update (a fresh `Datum` slice).
- A WAL record buffer to encode the on-page change.

PG's analogues: `ExecMaterializeSlot` reuses a fixed-width slot; `heap_form_tuple` writes into a `palloc`'d region that's freed at txn end; the WAL record is built into the `xloginsert.c` per-backend scratchpad (`rdatas[]`) without heap allocation.

## §3.4 Why this drives the §02 GC cost

The Go runtime's GC mark phase scans every allocated object. With 19 KB/query × 2 307 q/s = ~44 MB/s of new garbage, the heap fills the `GOGC=200` headroom (~2× live = ~5 GB) every ~110 s. Each collection scans the static 2.5 GB buffer pool plus the ~2.5 GB of churn — that's the 200 s of `scanobject` time observed across a 120 s window.

The arena's 327 680 pointers (one per buffer slot) are **all scanned every GC cycle** even though they never change. This is the dominant cost: `internal/storage.newArena` returns `[]byte` (pointer-free), but the surrounding `bufpool.Pool` structures (`partitions[128]bufferPartition`, each with map fields) are pointer-rich and scanned on every cycle.

`practice/go_rdbms_performance_techniques.md` §1 specifically calls this out: "Eliminate heap escapes; pre-size collections; sync.Pool for transient objects; **arena allocation for query lifetime**." goopg's `internal/executor/arena.go` and `arena_registry.go` exist (see §2.5 of the practice doc applied to TPC-H scans — M0073 / M0098-0007a) but they are not engaged for OLTP per-statement paths.

## §3.5 The `pgbench_history` insert path

Drilling into `updateOp.Next → updateViaIndex` (`internal/executor/operators_storage.go:985`):

The c=50 simple-update profile shows 4 365 MB allocated through `(*updateOp).Next` over 120 s × 347 TPS = ~41 700 transactions. That's **105 KB per UPDATE**. PG's equivalent (`heap_update` + WAL insert) typically uses < 1 KB of palloc, almost all of which is freed in bulk at txn end.

Why so much per goopg update?

1. **Row materialisation copies** (`Materialize().Row()` at `executor.go:282`) — every operator boundary copies into a fresh `Row`. PG's `TupleTableSlot` passes pointers into the pinned buffer; no copy.
2. **HOT update path allocates** new `Datum` slices for the updated tuple version (`tryApplyHOTUpdate`).
3. **WAL record building** (`writer.go:Append`) likely encodes per-column changes into a fresh byte slice rather than appending into a per-backend scratch buffer.
4. **MVCC commit path** allocates `txState` and recompute structures (`internal/mvcc/manager.go`).

Each of these is plausibly addressable individually; the practice doc's §1, §5 (`unsafe` for page access), §7 (slices), §16 (concurrency) give the recipes.

## §3.6 Implications for `GOMEMLIMIT` / `GOGC`

`GOMEMLIMIT=18GiB` is well above the working set (≈ 3 GB inuse) so the runtime is not in the "back-pressure" regime where it accelerates GC to stay under the cap. Effectively `GOMEMLIMIT` is a no-op for OLTP at this scale. Useful only as protection against pathological alloc bursts.

`GOGC=200` (M0098-0007) is the right call given the alloc rate — `GOGC=100` would double the GC frequency and likely halve the c=10 SO TPS to ~1 100. Raising further (e.g. `GOGC=500`, `GOGC=off`) trades transient memory growth for reduced scan cost; profile data suggests another 1.3–1.6× TPS lift in c=10 SO is reachable that way, but at the cost of OOM risk under burst traffic.

The **structural** fix is to allocate less, not to collect less. Three avenues (sized in §08):

1. Per-statement arena (`palloc`-style memory context): largest single lift; eliminates 60–80 % of churn in `parser` + `planner` + `executor` boundary copies.
2. `sync.Pool` per operator state (e.g. `seqScanOp`, `indexScanOp`, `updateOp` instances), per `Row` slice, per WAL record buffer.
3. Pointer-free buffer-pool inner structures: keep `byTag` as a hand-rolled open-addressing table with `int32`-indexed values (not `map[BufferTag]int`), so GC has nothing to scan per slot.

## §3.7 Per-symbol summary

| `file:line` | symbol | alloc cum% peak | notes |
|---|---|---:|---|
| `internal/planner/planner.go:32` | `planner.Plan` | 26.4 % (c=100 SO) | Top alloc for reads |
| `internal/planner/planSelect` | `planSelect` | 24.0 % | Sub-plan node construction |
| `internal/executor/operators_storage.go:985` | `updateOp.Next` | 35.8 % (c=10 SU) | Top alloc for writes |
| `internal/executor/operators_storage.go:<HOT>` | `tryApplyHOTUpdate` | 27.8 % (c=10 SU) | New `Datum` slices per version |
| `internal/server/dispatch.go:740` | `executeOneSimpleStmt` | 13.9 % | Boundary allocations |
| `internal/parser/parser.go:82` | `parser.Parse` | 6 % | AST nodes (despite `tokenSlicePool`) |
| `internal/wal/writer.go:611` | `wal.Writer.Append` | ~6 % (writes) | Record body buffer |
