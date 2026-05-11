# Milestone 0091 — Select-only TPS regression recovery (GC + activity tracking + btree.RangeScan allocations)

**Status:** partial 2026-05-11 — M0091-0001 + M0091-0002 +
design docs landed; M0091-0003 re-measurement shows 1.45×
recovery (350.89 → 510.52 TPS). Residual bottleneck identified
(cloneRow allocations in indexScanOp eager materialisation +
projectOp.Next); full recovery to ≥ 1,000 TPS requires a
structural refactor that is out of M0091's "targeted
allocation fixes" theme — filed as the **deferred follow-up**
described below.
**Depends on:** M0026 (concurrent WAL Append) — the historical
baseline used in this milestone is the post-M0026 measurement
recorded in `analysis/oltp-performance/wal-bottleneck.md`.
**Drives:** restore select-only pgbench throughput to the
post-M0026 baseline order of magnitude (thousands of TPS) by
attacking the GC / per-query-allocation / activity-tracking
overhead surfaced in 2026-05-11 pprof captures.

## Context

The 2026-05-11 spot measurement (commit `6b88263`) of
`pgbench -S -c 10 -j 10 -T 180` against goopg at scale 100
yielded **350.89 TPS / 28.50 ms avg latency**. The historical
post-M0026 baseline documented in
`analysis/oltp-performance/wal-bottleneck.md` was:

| workload | clients | TPS (historical) | latency (historical) |
|---|---:|---:|---:|
| select-only | 1 | 3,224 | 0.31 ms |
| select-only | 4 | 6,403 | 0.63 ms |
| select-only | 16 | 5,900 | 2.71 ms |

Current state versus historical at comparable concurrency:

| metric | post-M0026 baseline | 2026-05-11 (-c 10) | ratio |
|---|---:|---:|---:|
| TPS | ~6,000 (at -c 4–16) | **350.89** | **17× slower** |
| per-query latency | 0.63–2.71 ms | **28.50 ms** | **10–45× higher** |

Even accounting for differences in scale and concurrency
between the two measurements, the regression is severe and
clearly observable. read-only workloads should not exhibit
this magnitude of degradation.

## Bottleneck identification (pprof, 2026-05-11)

Two profiles were captured during a sustained `-S -c 10 -T 60`
run (`pprof-data/m0091/select-only-c10.{cpu,heap,allocs}.prof`):

### Finding 1 — Runaway GC (~70 % of CPU)

CPU profile top entries (cumulative):
- `runtime.gcDrain` — **69.96 %**
- `runtime.scanobject` — 57.79 %
- `runtime.findObject` — 16.27 %
- `runtime.greyobject` — 12.20 %

Total system samples: 74.18 s over 30 s wall-clock = 2.46× CPU
utilisation. Roughly half the wall-clock per core is GC mark
work. The Go runtime is fighting a sustained allocation rate
that the executor never had to deal with at the historical TPS
level.

### Finding 2 — `activity.goroutineID` via `runtime.Stack` (~11 % of CPU)

`internal/activity/activity.go:308–322`:

```go
func goroutineID() string {
    const prefix = "goroutine "
    buf := make([]byte, 64)
    n := runtime.Stack(buf, false)
    ...
}
```

`activity.LookupGoroutine` (`activity.go:297`) calls
`goroutineID()` on every wait-event / pgstat lookup. Each call:
- allocates a 64-byte buffer (GC pressure),
- invokes `runtime.Stack` (notoriously slow — walks live
  frames; documented at ~µs per call on small stacks, much
  slower on deep stacks),
- builds a `string` from the buffer (another allocation).

In the CPU profile this is **11.42 %** of cumulative CPU —
roughly the same magnitude as the executor's IndexScan path.

### Finding 3 — `btree.RangeScan` per-query allocations

`internal/access/btree/btree.go:1923–1958`:

```go
func (bt *BTree) RangeScan(lo, hi []byte, fn func(...) (bool, error)) error {
    ...
    for cur != InvalidBlockNumber {
        ...
        type rawSlot struct{ raw []byte }
        rawSlots := make([]rawSlot, 0, count)          // 173 MB / 30 s
        for s := uint16(1); s <= uint16(count); s++ {
            r, _ := storage.PageGetItemRaw(...)          // 104 MB / 30 s
            rawSlots = append(rawSlots, rawSlot{
                append([]byte(nil), r...),               // 126 MB / 30 s
            })
        }
        bt.unpinR(slot)
        for _, rs := range rawSlots { ... fn(...) }
    }
}
```

For every leaf-page visit, the code:
- allocates a slice of `rawSlot` (sized to slot count, hundreds),
- calls `PageGetItemRaw` for every slot (returns a fresh slice
  header per call → 100 MB+ allocation rate),
- copies every slot's raw bytes into a freshly-allocated
  `[]byte` (`append([]byte(nil), r...)`).

For a single point-lookup `WHERE aid = :aid`, the descent reaches
one leaf page with ~400 keys — and the code allocates ~400
short byte slices PER LOOKUP. The copy-out exists to release
the page pin before invoking `fn` (so `fn` can do further btree
operations without deadlocking), but for read-only point
lookups the entire copy is wasted work — `fn` for a SELECT
only follows the TID and never re-enters the btree.

### Finding 4 — `indexScanOp.Open` per-query allocations

`internal/executor/operators_index.go:120` — 470 MB cumulative
over 30 s = ~50 KB per query. Drives:
- The scanFn callback's `cloneRow(row)` per matched key.
- The `o.rows` / `o.tids` slices.
- Per-query arena (M0073-0001).

Each call to `Rescan` (the NLI / Plan-cache hot path) also
re-runs the same allocation pattern.

## Root cause synthesis

The historical 3 224 TPS / 0.31 ms latency at -c 1 implies
goopg was doing a select-only round-trip in about 310 µs.
Today the same workload takes ~28 500 µs per round-trip. The
delta (~28 200 µs per query) breaks down approximately as:

- ~70 % of CPU goes to GC → effective per-query CPU budget
  shrinks by 3-4× when allocation rate is high.
- `activity.goroutineID` adds a `runtime.Stack` call per
  wait-event lookup — multiple times per query — contributing
  ~11 % of CPU and many small allocations.
- `btree.RangeScan` per-query allocations grow with leaf-page
  fan-out — at the scale-100 pkey, ~400 byte-slice allocations
  per descent.
- Each of these dynamics worsens linearly with concurrency
  (per-client allocations × N clients) so the per-client
  effective TPS drops faster than O(1/N).

The two largest, most tractable bottlenecks are:
1. `activity.goroutineID`'s `runtime.Stack` calls — replaceable
   with a goroutine-local TLS-style ID via `sync.Map` keyed by
   pointer + atomic counter assigned on first call, or even
   simpler: store the registry pointer in a `runtime/Pdata`-
   style context-local mechanism (a `context.Context` value or
   a per-goroutine field reachable via the connection handle).
2. `btree.RangeScan`'s per-slot byte copy — for point-lookup
   callers, the copy is unnecessary; for callers that actually
   need to release the pin before re-entering btree, the copy
   can be done lazily (only on-demand inside `fn`) or batched
   into a single arena.

These two changes alone should restore most of the lost
throughput. The remaining GC pressure from per-query Row +
Datum allocation is a follow-on tuning target.

## Required design docs

- `docs/design/0091-0001-activity-tracking-goroutineid-fastpath.md`
  — replace `runtime.Stack`-based goroutineID with a connection-
  level registration pointer (e.g., `connSession.activityReg`)
  cached on the goroutine's `*Context` or similar, eliminating
  the syscall-equivalent cost per lookup. Audit every caller of
  `activity.LookupGoroutine` to confirm an alternative
  injection point exists.
- `docs/design/0091-0002-btree-rangescan-allocation-reduction.md`
  — rework the page-slot capture loop. Two complementary
  options:
  - (a) For callers that don't re-enter the btree (most
    SELECT paths), pass a flag to skip the copy and invoke
    `fn` directly with a `[]byte` aliasing the still-pinned
    page (caller must not retain it beyond `fn`).
  - (b) For callers that may re-enter, use a single arena per
    `RangeScan` invocation (or pool the `rawSlot` slice).
- `docs/design/0091-0003-pprof-baseline-and-regression-gate.md`
  — pin a pprof-based regression gate so a future allocation
  regression of this magnitude is caught before it ships. The
  existing `pprof-data/` convention is the right home; this
  design wires a small CI / dev-loop step that compares against
  a baseline.

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note about the milestone-only convention. The
break-down in fix_plan.md follows this milestone doc's
sub-milestone structure:

- **M0091-0001** — activity.goroutineID fast-path
- **M0091-0002** — btree.RangeScan allocation reduction
- **M0091-0003** — pgbench select-only re-measurement;
  target: ≥ 1 000 TPS at -c 10 -T 180 (3× current); stretch
  target: ≥ 3 000 TPS (the historical -c 1 number, scaled by
  concurrency).
- **M0091-0004** — (conditional, only if -0001/-0002 don't
  recover throughput to within 3× of the historical baseline)
  per-query Row + Datum allocation audit + arena reuse across
  SELECT calls.

## Definition of Done (sketch)

- `pprof-data/m0091/select-only-c10.{cpu,heap,allocs}.prof`
  re-captured after each sub-milestone, archived under
  `pprof-data/m0091/post-NNNN/` for diff visualisation.
- A re-run of `pgbench -S -c 10 -j 10 -T 180` against goopg at
  scale 100:
  - TPS ≥ 1 000 (minimum acceptance).
  - GC CPU share < 30 %.
  - `activity.goroutineID` CPU share < 1 %.
  - `btree.RangeScan` per-query allocation < 5 KB.
- Updated entry in `.ralph/fix_plan.md` recording the
  before/after numbers, with the deferred -0004 task moved to
  closed or carried forward as documented.
- A regression note in
  `analysis/oltp-performance/wal-bottleneck.md` referencing
  this milestone as the corrective action.

## Outcome (2026-05-11 partial)

### Landed

- **Commit `5c34192`** — 3 design docs
  (`0091-0001`, `0091-0002`, `0091-0003`).
- **Commit `3bdc1ad` (M0091-0001)** —
  closure-capture `reg + pidStr` in the 4 frame
  reader/writer hooks in `serveConn` + WAL writer + WAL sync
  hooks. Eliminated `runtime.Stack`-based goroutine ID
  lookup on every TCP read/write boundary.
- **Commit `460809c` (M0091-0002)** — rewrote
  `btree.RangeScan` to invoke `fn` while the pin is held,
  eliminating the per-slot `[]byte` copy loop. Added
  `storage.PageGetItemRawNoCopy` and `btree.parseItemNoCopy`
  for page-aliasing reads. Documented the CAT-1 caller
  contract above `RangeScan`. New benchmark
  `BenchmarkRangeScanPointLookup` pins the improvement
  (6,189 ns/op → 2,690 ns/op; 275 allocs/op → 15 allocs/op).
- **Commit (this commit) — M0091-0003** — pgbench
  re-measurement at scale 100, -c 10, -T 180:
  - **TPS 510.52** (vs 350.89 pre-fix → **1.45×**)
  - latency avg 19.59 ms (vs 28.50 ms → 1.45×)
  - 0 failed transactions
  - Results:
    `bench/pgbench-compare/results/20260511_125349_goopg_select-only_c10_m0091.txt`
    + `20260511_goopg_select-only_m0091_summary.md`.

### Deferred follow-up (M0092 candidate)

The M0091-0003 acceptance bar was TPS ≥ 1,000. We landed at
510.52 (half the bar). Post-fix pprof identifies the new
dominant bottleneck:

- `executor.cloneRow → acquireRow → rowPool.Get → New →
  make(Row, width)` fires at TWO sites per query:
  - `operators_index.go:285` — eager
    `o.rows = append(o.rows, cloneRow(row))` in
    `indexScanOp`'s scanFn.
  - `operators.go:94` — projectOp.Next per emitted row.
- Cloned Rows are never returned to the pool (consumer
  retains them past Close — releasing on Close was attempted
  and verified to BREAK existing tests at
  `internal/executor/vm_test.go:169` that read row data after
  Close). The pool is effectively cold; every `acquireRow`
  hits `New` and allocates fresh.

The structural fix is a **lazy-iterate refactor of
indexScanOp** (and a slot-aliasing refactor of projectOp.Next).
That work is sizeable enough to warrant its own milestone —
the M0091 close-out files it as M0092 (or whatever next-
available number) with the post-fix pprof as the starting
point.

The post-fix pgbench result (510.52 TPS) is still a
substantial improvement on the regression baseline (350.89 TPS,
17× off the historical M0026 baseline of ~6,400 TPS) but
remains ~12× below the historical baseline. M0092 is required
to fully recover.

## References

- `analysis/oltp-performance/wal-bottleneck.md` — historical
  baseline (post-M0026, ~6 000 TPS at -c 4).
- `pprof-data/m0091/` — captured profiles
  (cpu / heap / allocs) for the 2026-05-11 reproduction.
- `internal/activity/activity.go:297-330` — goroutineID hot
  path.
- `internal/access/btree/btree.go:1923-1990` — RangeScan hot
  path.
- `internal/executor/operators_index.go:120-296` —
  indexScanOp.Open allocations.
