# 13 — Cooperative Parallel Hash Build

| field | value |
| --- | --- |
| status | draft |
| date | 2026-08-08 |
| depends on | [03](03-concurrency-substrate.md), [04](04-parallel-scan.md), [07](07-parallel-hash-join.md), P8 (`parallel_hash_build.go`) |
| milestone | M0129-S4.1 (= P2.1a) |

## 0. Motivation

P8 (`IMPLEMENTATION-TODO.md` §P8) landed parallel hash join: the leader builds
the hash table serially, then publishes it read-only for N probe workers. That
replaced PG's DSA + barrier apparatus with a struct and a map lookup — but it
left the build itself serial.

M0128-P2.1 measured the reopen condition as **MET** (2026-08-07): hash build
time is 12.6–41.0 % of total for medium/large dimensions:

| dim table | build rows | build time | total exec | build % |
|---|---|---|---|---|
| `customer` | 150 K | 324.5 ms | 939 ms | 34.6 % |
| `orders` | 1.5 M | 7,518.7 ms | 18,324 ms | 41.0 % |

The Q13-class shape — LEFT join with the 1.5 M-row `orders` table on the build
side carrying `o_comment NOT LIKE '%special%requests%'` — is the motivating
case. P8 parallelised the *probe* side (150 K rows) and delivered ~1.5 % gain;
the build (90 % of the input) remains serial and is the bottleneck.

This chapter implements the first of two follow-ups named in P8's post-mortem
and the 10-roadmap's "Deliberately deferred" row: **parallelise the build's
scan+filter, keep insertion serial** (producer/consumer split).

## 1. Design

### 1.1 The producer/consumer split

```
buildLazyHashTable (with parallel build):
    leader spawns N producer goroutines + 1 consumer goroutine
    
    each producer:
        gets partition of build table blocks (ParallelScanState)
        opens its own SeqScan → Filter operator subtree
        for each row:
            applies the filter predicate
            sends qualifying row → buffered channel
    
    consumer (R in the diagram below):
        receives rows from channel
        evaluates hash key
        inserts into hash map
        inserts into batch files if spilling
    
    after all producers exit & channel drains:
        consumer finalizes hash table
        build phase complete
```

### 1.2 Why this works

The build side's scan+filter is **embarrassingly parallel**: each block's rows
are independent, and the filter predicate is stateless. The serial section is
the hash-map insertion, which must be exclusive to avoid races on the Go map.
A buffered channel decouples the two phases: producers can scan and filter
while the consumer inserts, hiding the insertion latency behind the scan I/O.

This is different from PG's `Parallel Hash` — there is no barrier protocol, no
DSA, and no cooperative insertion. It is a single-writer design: one goroutine
owns the map; N goroutines feed it. The simplification is correct because Go
maps are NOT safe for concurrent write, and PG's motivation for cooperative
insertion (amortising spinlock overhead across workers) does not apply in a
single-address-space Go program where the channel send is the only
synchronisation.

### 1.3 Eligibility

A hash join is eligible for cooperative parallel build when ALL of:

1. **P8-eligible** — the join type permits shared probe (INNER, SEMI, ANTI;
   LEFT only with probe on the outer side; no FULL/RIGHT). Rule from
   `probeSideIsLeft` and `prebuildSharedHashJoins`.

2. **Not spilling** — the build fits in one batch (`nbatch == 1` after build
   geometry check). A spilling build can't be shared (P8's rule); the same
   applies to a parallel build — batch files are local to one operator
   instance.

3. **Build child is a SeqScan** — the build side must be a plain sequential
   scan of a heap relation (possibly with a Filter child). Complex build
   subtrees (index scans, subquery scans, joins) are built serially. This
   covers all TPC-H dimensions and is the common case.

4. **Build row count ≥ threshold** — skip for trivially small builds (e.g.
   `nation`, 25 rows) where goroutine overhead dominates. The threshold is the
   existing `min_parallel_table_scan_size` GUC (default 8 MB, ~1,000 pages).

### 1.4 What changes

| file | change |
|---|---|
| `internal/executor/parallel_hash_build.go` | New `parallelBuildLazyHashTable` function: spawns producers + consumer, replaces the serial drain loop for eligible joins. New `parallelBuildScanProducer` per-producer function. |
| `internal/executor/operators_join_agg.go` | `buildLazyHashTable` gains an early-return path: when the join is eligible, delegate to `parallelBuildLazyHashTable` instead of the serial `buildLoopRight`/`buildLoopLeft`. |
| `internal/executor/parallel_scan.go` | New `parallelBuildScanState` or reuse `ParallelScanState` for the build-side partition. The build scan is NOT partial in the plan sense (it scans every block), so the state must cover all blocks exactly once across N producers. |
| `internal/planner/` | Minor: `HasShareableHashJoin` or a new predicate recognises parallel-build-eligible joins. |

### 1.5 What does NOT change

- The **probe phase** is identical — workers still receive a shared read-only
  table and open only their partial probe side (P8 unchanged).
- The **hash table** structure is identical — `lazyHash map[string][]Row` and
  `lazyIntHash map[int64][]Row` are populated by the consumer exactly as the
  serial build populates them.
- **Scalars** (`antiBuildRows`, `antiBuildHasNull`, widths) are computed by
  the consumer during insertion, same as the serial path.
- **Spilling / batch files** — if the build spills (unexpected growth), the
  consumer handles it exactly as the serial path does; producers are unaware
  of batching.
- **Eligibility for P8 sharing** — a parallel-built table is published to
  probe workers exactly as a serial-built table is (`captureSharedBuild`).

### 1.6 Concurrency model

```
producer[0] ──┐
producer[1] ──┼──→ buffered channel ──→ consumer (hash builder)
   ...        │        (cap=256)              │
producer[N-1]─┘                               ▼
                                        lazyHash / lazyIntHash
                                              │
                                              ▼
                                        captureSharedBuild
                                              │
                                        ▼ probe workers (P8)
```

- Producers share **nothing** except the channel — each has its own operator
  tree, its own scan context, its own arena.
- The consumer is the **only writer** to the hash map. No lock, no atomic, no
  concurrent map.
- The channel buffer (256 rows) keeps the consumer fed while producers do I/O.
- Producer exit signals "no more rows." After all N producers exit, the
  consumer drains any remaining buffered rows and the build is complete.

## 2. Implementation plan

### 2.1 Phase 1: parallel build scan state

Add `parallelBuildScanBlocks` to `ParallelScanState` or a new helper that
divides a relation's blocks into N contiguous ranges. Unlike the probe-side
parallel scan, which uses atomic next-block allocation, a build-side scan can
pre-assign ranges because all blocks must be scanned exactly once and there is
no early-exit skew.

### 2.2 Phase 2: `parallelBuildLazyHashTable`

New function in `parallel_hash_build.go`:

```go
func (o *joinOp) parallelBuildLazyHashTable(
    ctx *Context,
    buildSide Operator,    // the build child's root (SeqScan or Filter→SeqScan)
    buildWidth int,
    buildIsLeft bool,
) (bool, error)
```

It:
1. Extracts the `SeqScanOp` from the build side (unwrapping Filter if present).
2. Divides the relation's blocks into N ranges (N = `max_parallel_workers`).
3. Creates a buffered channel (`chan buildRow`).
4. Launches N producer goroutines, each with its own operator tree (built from
   the same plan node, via the P8 pattern of `buildChild()`).
5. Runs the consumer loop in the calling goroutine: receive from channel,
   evaluate hash key, insert into map.
6. Waits for all producers (via `sync.WaitGroup`), closes channel, drains
   remaining rows.
7. Returns `probeIsLeft` and any error.

### 2.3 Phase 3: wire into `buildLazyHashTable`

In `buildLazyHashTable`, before the serial `buildLoopLeft`/`buildLoopRight`:

```go
if o.parallelBuildEligible(ctx) {
    return o.parallelBuildLazyHashTable(ctx, ...)
}
// fall through to serial build
```

### 2.4 Phase 4: planner eligibility

Minimal: a function `parallelBuildEligible` checks the four conditions from
§1.3. The planner does NOT generate a different plan shape — the decision is
made at execution time, same as P8's `prebuildSharedHashJoins`.

## 3. Measurement plan

Target: Q13 on TPC-H SF1 (the motivating case from M0128-P2.1). Compare:

| metric | serial build (P8) | parallel build (S4.1) |
|---|---|---|
| build time | ~7.5 s (41 %) | expected < 2 s |
| total Q13 time | ~102 s (warm) | expected < 96 s |
| Q9 total time | — | identity check (no regression) |
| Q17 total time | — | identity check |
| Q19 total time | — | identity check |

Even a modest reduction in build time moves Q13 meaningfully because the build
is on the critical path before any probe worker starts.

If build time drops below ~15 % of total for all measured queries, S4.2
(genuinely concurrent build) is recorded as not-needed.

## 4. Risks and mitigations

| risk | mitigation |
|---|---|
| Channel contention under high row rate | Buffered channel (cap=256); if still bottlenecked, increase buffer or batch rows per send |
| Producer skew (uneven block ranges) | Pre-assigned ranges avoid atomic contention; if one range has dense matches, the channel buffer absorbs it |
| Arena aliasing across goroutines | Each producer has its own operator tree → its own arena; `ownedBuildRow` copy happens in the producer before channel send |
| Test builder functions break | Same class as P8's `sync.Once` fix: eligibility check is on the plan, not the tree; `HasShareableHashJoin` precedent |
| Interaction with batch spilling | Parallel build is declined for projected-spilling joins (§1.3 rule 2); if unprojected spill occurs, the consumer handles it and the build degrades gracefully to private (not shared) |

## 5. Gates

Per 10-roadmap P8, per subtask (S4.1 = P2.1a):
- **Identity over the join corpus** — every hash join that was eligible for
  serial shared build produces identical output with parallel build.
- **RACE** — `make race-gate` under a probe-heavy workload (parallel hash join
  with parallel build enabled).
- **TPC-H Q9/Q17/Q19** — no regression; Q13 speedup measured.
- **UNITS** — `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`.
- **SPOT** — `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35).
- **DS05** — `scripts/tpcds-sf05-regression.sh sweep` (zero row/checksum deltas
  from parallel build).

## 6. Relationship to S4.2

S4.2 (= P2.1b) is a genuinely concurrent build (sharded or
per-worker-partial-then-merge). It is taken ONLY if S4.1's measurement still
shows build dominance after landing the producer/consumer split. The design
here (channel + single writer) is the simpler half; S4.2 would replace the
single consumer with N shard-owners and a merge step.
