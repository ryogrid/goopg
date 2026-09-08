# E-11 / EX5-04 — AIO `ReadStream` depth-policy measurement (re-run, quiet host)

**Date:** 2026-09-06 · **Outcome: (b) ledger-decline.** No `ReadStream` is
wired; the shipped default (`seqScanLookahead = 4`) is **not changed**.

This is the re-run the item required. The first sweep (2026-09-06 ~05:15) was
taken while two other agents held the same machine and is not attributable;
its arm files are superseded by the ones under `raw/` here.

---

## 1. What was measured, and why the answer is not one number

`seqScanOp.refillPrefetchWindow` (`internal/executor/operators_storage.go`)
keeps `seqScanLookahead` blocks ahead of the scan position warm via
`Pool.Prefetch`. Commit `c6af781f4` made that constant a package var read once
from `GOOPG_SEQSCAN_LOOKAHEAD`, so depth is A/B-able on one binary.

The window has a design-level exclusion that turns out to decide the whole
item (`operators_storage.go`, `refillPrefetchWindow`):

```go
if o.pscan != nil || seqScanLookahead == 0 {
        return
}
```

**Prefetch is disabled for a parallel scan** (P4, chapter 04 §4.2: with a
shared block allocator a worker's next block is not `curBlock+1`, so a
per-worker window prefetches blocks the worker never reads). And at bench
settings *every* TPC-H plan goopg picks is parallel. Captured live in the
alloc arm (`raw/alloc-arms.log`), Q6 at the server default:

```
Finalize Aggregate
  ->  Gather   Workers Planned: 4
        ->  Partial Aggregate
              ->  Parallel Seq Scan on lineitem
```

So the depth knob is **structurally inert on the default path** and only
becomes live when parallelism is switched off. Both regimes were therefore
measured separately. Measuring only the first would have produced a null
result with no explanation; measuring only the second would have produced a
"win" on a path no bench plan takes.

## 2. Common environment (identical for every arm)

| item | value |
| :-- | :-- |
| binary | ONE build, `go build -o <scratchpad>/e11b/goopg-e11b ./cmd/goopg` |
| binary commit | `9ad4f30d4e2037190233b508b2416274f3ff1204`, `internal/` clean at build time |
| binary on-disk hash | `04b4178d65eeda2f` — identical in all 23 arm headers |
| instrument | `c6af781f4` (`GOOPG_SEQSCAN_LOOKAHEAD`) |
| harness | `scripts/tpch-acceptance-arm.sh` — FRESH memory-capped server per arm, server age 0 s at sweep start |
| cluster | TPC-H SF=1, `bench/tpch/runtime_goopg/data`, port 65433, held under `flock /tmp/goopg-65433.lock` for the whole session |
| env | `GOGC=100 GOMEMLIMIT=12GiB GOOPG_ANALYZE_SEED=20260905 PGSHAPED=1 COLLAPSE=1 NO_BUILD=1 PER_Q=600` (900 for the serial arms) |
| cgroup | `GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0` (harness defaults; HIGH above GOMEMLIMIT) |
| host | 16 cores, 31 GiB RAM, ~19 GiB in page cache; `base/16408` (db `tpch`) is 1.9 GiB, so the working set is largely page-cache resident |
| drivers | `raw/sweep.sh`, `raw/serial.sh`, `raw/alloc.sh`; aggregation `raw/agg.py` |

`GOOPG_ANALYZE_SEED` and the fresh-server-per-arm rule are what make the arms
comparable at all (stats are per-connection and ANALYZE is sampled; a server
that has just run a heavy query sits at GOMEMLIMIT and thrashes GC). Arms were
interleaved rather than blocked by depth, so arm position cannot masquerade as
a depth effect:

```
rep1:  d4  d0  d16 d64 d128
rep2:  d128 d64 d16 d0  d4
rep3:  d16 d128 d4  d0  d64
```

## 3. Arm A — full TPC-H suite, default parallelism (5 depths x 3 reps)

Values first: **all 15 arms x 24 queries are byte-identical** (`colsig` /
`ordered` / `unordered` / `rows` from `tpch-runner -digest`). Depth moves no
result, at any depth, which is the precondition for reading the timings at
all.

Per-query elapsed seconds, every arm and repetition (`raw/d<depth>-r<rep>.txt`):

```
Q               d0r1  d0r2  d0r3  d4r1  d4r2  d4r3  d16r1 d16r2 d16r3 d64r1 d64r2 d64r3 d128r1 d128r2 d128r3
Q1              8.82  8.86  8.25  8.16  9.33  8.51  7.82  9.11  8.52  8.24  7.79  8.93   8.05   8.31   8.17
Q2              0.97  1.17  0.93  1.07  1.36  0.97  0.81  0.82  0.94  1.05  1.01  1.15   1.10   0.88   0.97
Q3              2.80  3.06  2.76  3.43  3.15  3.29  2.71  2.87  3.28  3.57  3.22  3.29   4.24   3.29   3.04
Q4              1.64  1.69  1.61  1.71  1.77  1.74  1.58  1.66  1.69  1.77  1.67  1.71   1.93   1.79   1.74
Q5              3.83  4.50  3.89  3.91  4.07  4.06  4.01  4.22  4.01  4.06  3.87  4.57   5.02   3.98   4.02
Q6              0.86  0.85  0.80  0.77  0.84  0.76  0.77  0.81  0.80  0.76  0.76  0.81   1.24   0.76   0.77
Q7              5.54  5.89  5.40  5.54  6.35  5.61  5.54  5.42  5.64  5.36  5.69  6.20   6.09   5.13   5.66
Q8              0.49  0.67  0.60  0.48  0.51  0.49  0.45  0.48  0.54  0.58  0.90  0.58   0.89   0.49   0.52
Q9              8.04  7.62  6.95  6.82  7.64  6.76  6.67  8.69  7.13  7.32  7.54  7.87   9.23   6.58   6.90
Q10             2.79  2.83  2.87  2.65  3.11  2.71  2.65  3.03  2.77  3.59  2.95  3.26   2.98   2.53   2.71
Q11             0.17  0.18  0.20  0.16  0.21  0.18  0.17  0.18  0.18  0.19  0.31  0.18   0.20   0.16   0.18
Q12            13.07 14.46 13.55 14.27 14.55 13.16 13.18 15.79 14.09 15.18 13.97 15.40  14.59  14.34  13.42
Q13             5.05  5.62  5.11  5.31  5.58  5.05  5.23  6.77  5.35  6.23  5.63  6.86   5.78   4.89   5.24
Q14             0.49  0.52  0.47  0.44  0.54  0.47  0.50  0.62  0.50  0.46  0.54  0.49   0.43   0.43   0.46
Q15-CREATEVIEW  0.01  0.01  0.01  0.01  0.00  0.01  0.00  0.01  0.01  0.01  0.01  0.01   0.01   0.01   0.01
Q15a-VIEWBODY   2.60  2.81  2.58  2.98  3.07  2.64  2.65  3.11  2.86  2.53  3.97  2.78   2.50   2.46   2.60
Q15b-MAIN      22.99 25.48 23.89 24.38 26.45 23.83 22.65 28.71 24.36 22.92 24.66 26.30  25.02  23.97  24.26
Q16             0.73  0.84  0.80  0.84  0.87  0.76  0.73  0.92  0.75  0.69  0.91  0.93   0.83   0.72   0.81
Q17             0.52  0.64  0.57  0.55  0.60  0.55  0.54  0.67  0.55  0.52  0.54  0.77   0.56   0.53   0.58
Q18            35.04 37.57 34.49 34.38 43.81 34.76 35.34 38.22 38.80 33.62 37.92 38.98  37.36  34.65  34.54
Q19             2.12  2.52  2.12  2.38  3.04  2.18  2.17  2.37  2.32  2.21  2.22  2.40   2.08   1.99   2.17
Q20             1.26  1.53  1.48  1.60  1.49  1.34  1.31  1.73  1.37  1.49  1.39  1.53   1.42   1.22   1.33
Q21            13.61 14.73 18.32 13.96 14.00 13.39 13.03 15.29 14.02 15.63 14.24 14.42  14.68  12.50  13.36
Q22             0.68  0.87  0.70  0.75  0.68  0.71  0.66  0.77  0.69  0.68  0.66  0.68   0.65   0.66   0.68
```

Suite totals (sum of the 24 labels, per repetition):

| depth | rep 1 | rep 2 | rep 3 | median | vs d4 |
| --: | --: | --: | --: | --: | --: |
| 0 | 134.12 | 144.92 | 138.35 | **138.35 s** | +1.32% |
| 4 (control) | 136.55 | 153.02 | 133.93 | **136.55 s** | — |
| 16 | 131.17 | 152.27 | 141.17 | **141.17 s** | +3.38% |
| 64 | 138.66 | 142.37 | 150.10 | **142.37 s** | +4.26% |
| 128 | 146.88 | 132.27 | 134.14 | **134.14 s** | −1.76% |

### Observed noise band (control vs control)

Depth 4 rep-vs-rep spread, which is the honest floor for reading anything
above:

| query | spread | query | spread | query | spread |
| :-- | --: | :-- | --: | :-- | --: |
| Q2 | 40.2% | Q7 | 14.6% | Q22 | 10.3% |
| Q19 | 39.4% | Q16 | 14.5% | Q17 | 9.1% |
| Q11 | 31.2% | Q1 | 14.3% | Q3 | 8.9% |
| Q18 | 27.4% | Q9 | 13.0% | Q8 | 6.3% |
| Q14 | 22.7% | Q15b | 11.0% | Q21 | 4.6% |
| Q20 | 19.4% | Q12 | 10.6% | Q5 | 4.1% |
| Q10 | 17.4% | Q6 | 10.5% | Q4 | 3.5% |
| Q15a | 16.3% | Q13 | 10.5% | Q15-CREATEVIEW | 0.0% |

**Worst control-vs-control per-query spread 40.2% (Q2); median 12.0%.** Suite
totals at the control depth alone span 133.93–153.02 s, a 14.2% band. The
project's stated ±17% per-query noise band is confirmed, and on the small
queries it is optimistic.

### Reading

**Nothing wins.** The five suite medians span 134.14–142.37 s, a 6.1% band
that is a quarter of the control's own total spread, and the ordering is
physically meaningless: the *deepest* arm (d128) has the fastest median and
d64 the slowest, with d0 and d4 between them. Per-query, every median delta
listed in `raw/` (largest: Q2 −23.4% at d16, Q8 +22.4% at d0) sits inside that
query's own control band. An effect with the sign either hypothesis predicts —
"more lookahead helps I/O" or "more lookahead costs CPU" — would be monotone
in depth. There is no monotone component.

This is the expected result once §1 is known: at the server default the window
is never refilled, so all five arms are running byte-identical code paths and
the table is a five-way A/A.

## 4. Arm B — serial path, where the window is actually live

Same harness, same binary, `tpch-runner -parallel-workers 0` (so
`max_parallel_workers_per_gather = 0` per query), scan-heavy subset
`1,3,6,12,14,19,22`, depths {0, 4, 16}. Values again **identical across all 8
arms** (`raw/s<depth>-r<rep>.txt`).

```
Q       s0r1   s0r2   s0r3  |  s4r1   s4r2   s4r3  |  s16r1  s16r2
Q1     16.52  16.11  16.53  | 18.74  18.90  19.67  | 21.19  21.37
Q3      9.23   8.26   7.90  |  9.78   8.67   9.41  |  8.52  10.11
Q6      3.77   3.85   3.76  |  5.80   5.37   6.30  |  4.80   4.82
Q12    15.05  14.26  15.54  | 13.34  16.09  16.07  | 13.91  14.52
Q14     2.05   1.80   1.96  |  1.81   3.04   2.36  |  1.78   1.84
Q19     9.41   9.05   9.80  |  9.30  11.14  10.28  |  8.84   9.37
Q22     0.63   0.62   0.67  |  0.68   0.69   0.65  |  0.65   0.66
TOTAL  56.66  53.95  56.16  | 59.45  63.90  64.74  | 59.69  62.69
```

| | median total | vs s4 |
| :-- | --: | --: |
| depth 0 | 56.16 s | **−12.1%** |
| depth 4 (control) | 63.90 s | — |
| depth 16 | 61.19 s | −4.2% |

Control A/A band on this subset: 8.9% on the total; per query 5.0% (Q1),
17.3% (Q6), 68.0% (Q14 — 1.8 s, unusable as a signal).

Two queries clear their own band with **non-overlapping repetition ranges**,
which is the only form of per-query claim this suite supports:

- **Q6** (bare `Seq Scan` + `Aggregate` over `lineitem`): 3.76–3.85 s at
  depth 0 vs 5.37–6.30 s at depth 4 — **−35.0%** on medians, against a 17.3%
  control band, ranges disjoint.
- **Q1** (`Seq Scan` + `HashAggregate` + `Sort`): 16.11–16.53 vs 18.74–19.67 —
  **−12.6%** against a 5.0% control band, ranges disjoint.

The totals are also disjoint: the *worst* depth-0 repetition (56.66 s) beats
the *best* depth-4 repetition (59.45 s).

Q3/Q12/Q14/Q19/Q22 are inside noise, and depth 16 is not ordered between 0 and
4 per query (Q1 is +12.6% at depth 16, i.e. worse than depth 4). So the
statement the data supports is narrow and specific: **on a scan-dominated
serial query, any depth > 0 costs time; the cost is not smoothly ordered in
depth.**

## 5. Arm C — allocations (`raw/alloc-arms.log`)

pprof `-base` delta over a window of 8 serial Q6 executions on a fresh capped
server, `heap?gc=1` before and after, depths 4 / 0 / 64 / 4 (the repeated
depth-4 arm is the A/A control).

| depth | 8xQ6 wall | alloc **objects** | alloc bytes |
| --: | --: | --: | --: |
| 4 (rep 1) | 54.36 s | 15,242,687 | 9.90 GB |
| 0 | **32.86 s** | **5,294,822** | **1.00 GB** |
| 64 | 42.90 s | 14,693,558 | 9.92 GB |
| 4 (rep 2, control) | 50.07 s | 14,961,116 | 9.92 GB |

Control-vs-control: 8.6% on wall, 1.9% on object count. Depth 0 is **−37%**
wall, **2.85x fewer allocation objects**, **9.9x fewer allocated bytes** —
all far outside that band. Allocation *counts* matter more than bytes in this
codebase, and the count ratio is the smaller of the two, so the honest
headline is 2.85x.

The whole difference is one call site. At depth 4:

```
flat  flat%           cum   cum%
                    9.54M 63.77%  storage.(*Pool).Prefetch          (alloc_objects)
8.27GB 83.33%       8.94GB 90.09%  storage.(*Pool).Prefetch          (alloc_space)
```

with `aio.(*methodWorker).Submit`, `initdb.aioEngineAdapter.Submit` and
`aio.(*Engine).Submit` making up most of the rest. At depth 0 those six frames
are simply absent and the profile is `cloneRowOwned` + `relFile.lockBlock`.

`internal/storage/bufpool.go`:

```go
func (p *Pool) Prefetch(tag BufferTag) {
        if !p.prefetchEnabled.Load() { return }
        if slotIdx, _ := p.bm.Lookup(tag); slotIdx >= 0 { return }
        buf := make([]byte, BlockSize)
        _, _ = p.mgr.PrefetchBlock(tag.Rel, tag.Block, buf)
}
```

`buf` is a fresh 8 KiB heap allocation per hinted block and is **never
installed into the pool** — it is dropped when `Prefetch` returns. The read
therefore cannot serve the subsequent `Pin`; its only possible benefit is
warming the OS page cache, and the buffer plus the AIO submit path is pure
garbage. `/proc/<pid>/io` over the windows confirms the reads are not doing
work: depth 4 rep 2 moved **0 bytes** of `read_bytes` across the whole 8xQ6
window and still paid 9.92 GB of allocation; depth 0 did 3.3 MB of real reads
and was 34% faster.

This is the mechanism behind §4, and it is a `Pool.Prefetch` property, not a
depth property — which is why depth 16 and 64 are not ordered against depth 4:
past the first block the cost is per-hint and the benefit is zero either way.

## 6. Decision — outcome (b), ledger-decline

**No `ReadStream` is wired. `seqScanLookaheadDefault` stays at 4.** Four
independent reasons, in the order they bind:

1. **The default path cannot observe it.** Every TPC-H plan goopg picks at
   bench settings is parallel, and `refillPrefetchWindow` returns early for a
   parallel scan by design. Arm A is a five-way A/A and reads like one.
   Whatever a `ReadStream` did, the suite would not see it.
2. **Where the mechanism IS live, more prefetch measures worse, not better**
   (Arm B: −12.1% subset, −35.0% Q6 for *removing* it; Arm C: 2.85x the
   allocation objects). A `ReadStream` with a depth policy pushes harder in
   exactly the direction that measures negative. Wiring it to then tune its
   depth toward zero is not a project.
3. **Upstream's own policy agrees, on this workload.**
   `postgres/src/backend/storage/aio/read_stream.c` (header): *"The algorithm
   for controlling the look-ahead distance is based on recent cache hit and
   miss history. When no I/O is necessary, there is no benefit in looking
   ahead more than one block. This is the default initial assumption, but when
   blocks needing I/O are streamed, the distance is increased rapidly."* The
   SF=1 working set is 1.9 GiB against ~19 GiB of page cache; PG's adaptive
   distance would collapse to 1 here. Declining is what upstream's controller
   would decide, not a divergence from it.
4. **It is not a hookup.** `internal/storage/aio/read_stream.go` v0 is
   offset-and-`File`-based and hands back bytes; its own header says *"the
   bufmgr-aware variant that returns `Buffer` handles is a follow-up"*, and
   lists per-block contiguous merge (`io_combine_limit`), `Reset()`, and the
   sequential-detection / ramp-up policy as deferred. Wiring it into
   `seqScanOp` means building that variant plus the ramp — for a path measured
   to want less prefetch.

### What was deliberately NOT done

**The default was not flipped from 4 to 0**, although §4/§5 would support it
on this host. Three reasons:

- The item's rule is not to move the default on a within-noise result, and the
  result on the *default configuration* (Arm A) is within noise. The win exists
  only with parallelism forced off, which no bench plan does.
- Every measurement here is warm-cache. The one case where an OS-page-cache
  hint could genuinely pay — a cold buffer pool and a cold page cache — is
  unmeasured, and setting the default to 0 would delete the mechanism for it
  on the strength of evidence that does not cover it.
- The defect is not the constant. It is that `Pool.Prefetch` allocates and
  discards its buffer (§5). Fixing that inverts the sign of this whole
  measurement, and it lives in `internal/storage/bufpool.go`, outside this
  item. Zeroing the depth would bank a warm-cache win and hide the bug.

Filed instead as ledger rows: `take3-E-11-readstream-declined` (the decline)
and `take3-E-11-prefetch-discards-buffer` (the defect, with the resume point).

The `GOOPG_SEQSCAN_LOOKAHEAD` instrument from `c6af781f4` is **kept**. It is
the apparatus that produced §4 and §5 and is the cheapest way to re-check the
defect after any `Pool.Prefetch` change; it is behaviour-neutral when unset.

## 7. Limits of this measurement

- Warm cache throughout (see above). A cold-page-cache arm was not run.
- Arm start load varies 0.5–9.3 (the decaying tail of the previous arm's
  server teardown). Interleaving keeps it uncorrelated with depth, but it
  inflates the A/A band, so Arm A's null is weaker than the 6.1% total spread
  makes it look. Arm B's conclusion does not rest on it: the s0 and s4
  repetition ranges are disjoint under every pairing.
- One host, one scale factor (SF=1), one AIO method (`io_method=worker`,
  `io_workers=3`, as logged by every arm's server).
- Depth 16 has 2 repetitions in Arm B, 3 everywhere else.
