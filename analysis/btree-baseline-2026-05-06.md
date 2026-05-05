# M0055 B-tree Baseline (2026-05-06)

**Status:** Frozen baseline. Sub-task M0055-0001.
**Author:** goopg perf-analysis branch.
**Run hardware:** Linux 6.6.87.2-microsoft-standard-WSL2, single host
  (development machine).
**Commit at run:** `8b1875d` (perf-analysis HEAD at the moment the
  baseline was captured).

## 1. Purpose

Freeze a measurable "before" set of write-path numbers against which
M0055 Phase A through Phase E deltas can be compared. The harness
itself ships as `internal/access/btree/bench_baseline_test.go`
(test name `TestBenchBaseline_M0055`) so the same code regenerates
the numbers on subsequent commits.

## 2. Workload

100 000 random 8-byte keys (uint64 big-endian, seed 42) inserted
into a fresh `Create`-built BTree. Each insert calls
`(*BTree).Insert(key, ItemPointer)` and the harness times the
single call.

The tree starts empty; the test cleanup tears it down with the
storage pool. No concurrent readers / writers — single-goroutine
write path only. This isolates the steady-state insert cost from
concurrency overhead.

## 3. Numbers (frozen 2026-05-06)

```
M0055-baseline-summary {
  inserts=100000
  total_ms=4248.02
  inserts_per_sec=23540
  splits=346 (0.35 %)
  p50_us=23
  p95_us=49
  p99_us=145
  max_us=6128
  rss_delta_mb=1.5
}
```

### Interpretation

- **23 540 inserts/sec, p95 49 µs.** Steady-state single-writer
  insert path cost is dominated by buffer-pool pin/unpin + page
  decode/rewrite. Per the `analysis/btree-simplifications-and-
  performance-upgrade-plan-2026-05-05.md` report, the whole-page
  rewrite-on-insert path is the chief CPU driver — addressed by
  M0055-0002 Phase A in-page binary insert.
- **346 splits across 100K inserts (0.35 %).** With random uint64
  keys, splits propagate up the tree at roughly the page-fill rate.
  Phase A's byte-aware split-loc should reduce this for variable-
  width key workloads (uint64 keys are fixed-width 8 B so the
  byte-aware policy here is equivalent to count-midpoint; the
  reduction will surface in a later varlen-key bench).
- **p99 latency 145 µs vs p95 49 µs** — the long tail is the
  split-path retry under `splitMu`. Phase C (multi-writer split
  protocol) targets this.
- **max latency 6.1 ms** — outlier from the very first inserts
  triggering metapage / first-leaf allocation. Not representative
  of steady state; ignored in the comparison.
- **RSS delta 1.5 MB** — almost entirely the storage pool's
  cached pages (32 slots × 8 KB = 256 KB plus per-page Go-side
  metadata). Phase E's spill-capable build path doesn't apply to
  this random-insert workload; that bench will use a separate
  CREATE INDEX harness.

## 4. Thresholds for Phase A / B / C / D / E acceptance

Each phase commits with a fresh run of this same harness. The
deltas must beat the following thresholds vs the row above:

| Phase | Headline threshold |
|-------|--------------------|
| A (write-path CPU) | ≥ 30 % `inserts_per_sec` improvement; whole-page rewrite no longer top driver in pprof |
| B (steady-state dedup) | duplicate-heavy variant of this bench (100K inserts of 100 distinct keys) shows post-insert page count bounded |
| C (multi-writer split) | concurrent stress test: 32 goroutines × 100K inserts each completes without lost/duplicate entries; aggregate inserts/sec ≥ 4× single-writer baseline |
| D (deletion/recycling) | crash-replay test: deleted pages return as new allocations |
| E (CREATE INDEX spill) | 100M-row CREATE INDEX completes within bounded work_mem budget |

## 5. Reproducibility

```
go test ./internal/access/btree/ -run TestBenchBaseline_M0055 -count=1 -v
```

The test is `testing.Short`-skipped so it does NOT run under
`go test -short`. CI that wants to track the numbers should run
without `-short` and parse the `M0055-baseline-summary { … }`
block.

## 6. Phase A delta (M0055-0002, 2026-05-06)

After landing M0055-0002 (in-place insert via `PageInsertItemRawAt`
+ binary-search line-pointer probe in `insertItemSorted`), the
same harness reports:

```
M0055-baseline-summary {
  inserts=100000
  total_ms=505.40
  inserts_per_sec=197864
  splits=346 (0.35 %)
  p50_us=4
  p95_us=6
  p99_us=13
  max_us=658
  rss_delta_mb=3.8
}
```

### Delta vs §3 baseline

| Metric | Baseline | Phase A | Δ |
|--------|----------|---------|------|
| total_ms | 4 248 | 505 | **-88.1 %** |
| inserts_per_sec | 23 540 | 197 864 | **+741 %** (8.4× speedup) |
| splits | 346 | 346 | 0 % (deterministic at SF=1) |
| p50_us | 23 | 4 | -82.6 % |
| p95_us | 49 | 6 | -87.8 % |
| p99_us | 145 | 13 | -91.0 % |
| max_us | 6 128 | 658 | -89.3 % |

The whole-page rewrite-on-insert hotspot is eliminated. The
remaining cost is dominated by the binary-search decode-per-probe
(~log₂(items) decode calls per insert) plus pin/unpin. Phase A's
acceptance threshold (≥ 30 % inserts/sec improvement) is met by
a margin of ~25× the bar.

### Phase A scope clarification

This commit lands **(1) in-place binary-position insert**. The
other two Phase A items —
**(2) byte-aware split-loc** and
**(3) rightmost-leaf insert fastpath cache** —
are deferred to follow-ups:

- (2) is a no-op for fixed-width int4/uint64 keys (count-midpoint
  ≡ byte-midpoint when every entry is the same size). The bench
  here uses uint64 keys so the difference would be invisible.
  A varlen-key variant of this bench should land alongside the
  byte-aware-split-loc commit so the split-count delta is
  measurable.
- (3) is an additive optimisation on top of the in-place insert.
  Largest expected win is on monotonic / append-shaped workloads
  (B-tree-style timestamp / serial-id indexes). Tracked as
  `M0055-0002-followup-rightmost-cache`.
