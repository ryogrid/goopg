# 06-03: The remaining ~2× — measured decomposition, and how much is "because Go"

Question from the team: both paths now reach roughly half of PostgreSQL's
throughput. Where does the remaining factor come from — and is ~2× simply the
price of a Go implementation versus C, or is there addressable headroom?

Short answer, defended below with the profile data:

- **Write path (1.47×): almost none of the residual is "because Go".** Per
  the excess decomposition in 02 it is two specific mechanisms: the UPDATE
  statement's page-reload mutex serialization (50 % of the excess) and
  commit-group amortization (C5, 27 %) — lock/IO design, not language.
- **Read path (2.03×): roughly 0.6–0.8× of the 2× is Go-runtime reality at
  this architecture (allocation+GC+runtime services ≈ 25–30 % of CPU);
  the larger share is per-query architectural cost (parse+plan+operator-tree
  construction ≈ 40 % of CPU) that a C implementation would also pay less
  for, but which goopg can attack without changing languages.** A well-tuned
  Go engine at this design should land at ~1.3–1.5×, not 2×.

## Write path: the residual is width, not language

02 established both engines now block on the same WAL-flush wait. The
residual decomposes (per-statement arithmetic in 02) into the UPDATE
statement excess (0.805 ms, 50 %) and the END excess (0.435 ms, 27 %).
Both are concurrency-design properties, not language properties: the UPDATE
excess is dominated by `Pool.Pin` waits that bottom out in the per-file
`relFile.readBlock` mutex (647.8 s in the block profile — page-reload
serialization a sharded design removes), and the END excess is commit-group
amortization (PG's `LWLockAcquireOrWait` + `WaitXLogInsertionsToFinish`
sweep, xlog.c — the C5 design). C5 alone takes 1.47× → ~1.34×; the UPDATE
work takes it toward ~1.1×. Language intrinsics barely enter either.

## Read path: measured CPU buckets

goopg `-S`, 90 s window, 907.9 s of samples (~10.1 cores busy) at 89,955 TPS.
`dispatchSimpleQueryViaExecutor` = 86.1 % of samples; buckets below are % of
total profile:

| bucket | share | dominant frames |
|---|---:|---|
| **socket syscalls (read+write)** | ~20.4 % flat | `Syscall6` (both directions) |
| **protocol assembly + flush** | ~19.2 % cum | `WriteReadyForQuery`/`FrameWriter.Flush` → `bufio.Flush` → `net.Write` (the syscall share above overlaps ~18 %) |
| **operator-tree construction per query** | ~19.5 % gross, **~10 % net of the probe** | `opOpen` 16.8 % + `BuildFastIterator` 2.7 % — but 9.6 % of total CPU inside `indexScanOp.Open→Rescan` is the ACTUAL btree descent+leaf read executing eagerly (see below) |
| **planner** | ~13.1 % | `planner.Plan` per query |
| **parser** | ~7.1 % | `parser.Parse` per query |
| **actual execution** | **~12.4 %** | `OpIterator.Next` 2.8 % + the eager btree probe inside `indexScanOp.Open` 9.6 % (goopg's TID-eager design runs the index descent at Open time) |
| **GC + allocator + runtime services** | ~25–30 % (overlapping the above) | `mallocgc` 14.7 % cum, `sweepone` 3.8 %, `gcBgMarkWorker` 1.8 %, `madvise` 1.7 %, `memclr` 3.9 % (partly inside mallocgc), scheduler/futex ~4.4 % |
| **forced-GC helper on the query path** | 5.9 % | `maybeForceGCAfterCommit` — 53 s of CPU in a 90 s window on SELECTs |

Allocation rate (allocs profile — cumulative since the pre-workload restart,
~106 s): **136.5 GB allocated** ≈ **14–15 KB per point-SELECT**. That
allocation volume is what feeds the GC bucket.

The single most striking number: **executing the query is ~12 % of the CPU
(probe + fetch); pure per-query setup (parse 7 % + plan 13 % + operator
build net of the probe ~10 %) is ~30 %.** PostgreSQL in the same simple-protocol regime also parses and plans
every query — but in C, with arena (palloc) allocation, no GC, and a plan
tree an order of magnitude cheaper to build. goopg additionally REBUILDS the
executor operator tree per query. (Note: in this workload the plan cache
does NOT help either — pgbench simple protocol interpolates literals, so
every SQL text is unique and `planner.Plan` runs on every query; the cache
frames' 2.1 % is pure overhead here.)

A fairness caveat on the PG target: PG's own `-S` wait sampling in this run is
51 % on-CPU / 49 % `Client:ClientRead` — at 182 k TPS PG is partially
client-bound (pgbench itself saturating), so PG's true ceiling is likely
higher and the "2.03×" is a lower bound on the read gap.

## Attribution: inherent-Go vs addressable-architecture

**Inherent to Go at ANY architecture (the honest "language tax"):**

- GC + write barriers + allocation bookkeeping on whatever the engine
  allocates: measured here at ~20 % of CPU (mallocgc+sweep+markworker+madvise
  +memclr), plus scheduler/futex ~4 %. PG pays ~0 for this (palloc arenas
  freed wholesale per query, no barriers, no marker).
- Bounds checks, interface dispatch (`Datum` handling, operator interfaces),
  no computed-goto dispatch, less mature auto-vectorization: not separable in
  a sampling profile, conventionally worth ~10–20 % on pointer-heavy DB code.
- Combined honest estimate at THIS allocation rate: **1.3–1.5× on the read
  path**. But the qualifier matters: the GC share is proportional to the
  17 KB/query allocation volume, which is an architectural choice. Engines
  like VictoriaMetrics/Badger demonstrate Go hot paths at <5 % GC by
  amortizing allocations; goopg's own M0091/M0092 work already cut the worst
  of it. Go-with-arena-discipline would shrink the language tax toward
  ~1.15–1.25×.

**Addressable without changing language (the larger share):**

1. **lockmgr global mutex (12 % of `-S` latency; 53 % of its block
   delay).** Per-statement relation read locks serialize on ONE mutex
   (`acquireRelLockMaybeTransient` → `LockManager.acquire`/`Release`,
   408.9+233.3 s of delay). Shard by relation OID or fast-path reader
   locks (PG's per-backend fast-path relation locks are the exact analog).
2. **Operator-tree reuse (~10 % of CPU net of the eager probe).** A
   reset-and-rebind protocol (PG's analog: executor state per portal,
   generic plans) removes the rebuild share of `opOpen`+`BuildFastIterator`
   and its allocations — the probe itself (9.6 %) is real work reuse cannot
   remove.
3. **Plan+parse cost (~20 %).** For the extended protocol / prepared
   statements this drops to ~0 by design (pgbench `-M prepared` would show
   the ceiling); for simple protocol, interning/shortcuts for point-lookup
   shapes are possible. Note PG pays this too — just ~5× cheaper.
4. **`maybeForceGCAfterCommit` (5.9 %)** on read-only autocommit statements —
   a leftover from the M0107 fix (counter-first was applied, but the helper
   still costs 53 s/90 s at 90 k TPS). Gate it to write transactions.
5. **Protocol/syscall shape (~20 %+19 % overlapping).** One syscall per
   ReadyForQuery flush per query; batching/corking (writev of DataRow+
   CommandComplete+ReadyForQuery — partially done) and reducing per-frame
   bookkeeping converge toward PG's shape; PG pays the same syscalls but
   assembles frames more cheaply.
6. **Allocation volume (~14–15 KB/query)** feeding bucket one of the language
   tax: per-query `NewContext` (2 %), snapshot capture (2.4 %), parse/plan
   node allocations. Arena/pool the per-query state (the mctx machinery
   exists) — this shrinks BOTH the addressable and the "inherent" share.

**What landing each would buy (order-of-magnitude, additive on 90 k TPS):**

| item | share removed | est. read TPS |
|---|---:|---:|
| lockmgr sharding (latency −12 %) | wait, not CPU | ~101 k |
| operator-tree reuse | ~10 % CPU net | ~111 k |
| forced-GC gate | ~6 % CPU | ~117 k |
| alloc-volume halving (GC ~20 %→~12 %) | ~8 % CPU | ~126 k |
| protocol corking + frame slimming | ~5–8 % CPU | ~134 k |

That trajectory ends near **~1.35× of PG's (client-bound) 182 k** — consistent
with the 1.3–1.5× floor estimate. Getting below ~1.3× would require attacking
the remaining language tax directly (arena Datums end-to-end, generated
per-shape fast paths), with steeply diminishing returns.

## Conclusion

The "half of PostgreSQL" state is **not** primarily a C-vs-Go fact today:

- On writes it is two identified lock/IO-design items (the readBlock-mutex
  page-reload serialization behind UPDATE, and C5 group amortization); the
  language question barely enters.
- On reads, the measured language tax at the current allocation rate is
  ~1.3–1.5× — real, permanent in direction, but smaller than the
  ~40 %-of-CPU per-query setup cost that is architectural and addressable.
  ~2× is therefore NOT the natural floor for this design in Go; ~1.3–1.5×
  is a defensible target, with C5 + operator-tree reuse as the two highest-
  leverage items, one per path.
