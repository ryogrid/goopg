# Row-decode path optimisation — effect verification report

Branch `optimize-row-decode`, cut from local `master` (`60d257504`).
Change under test: `4bfadc38a`. Date: 2026-09-08.

## 1. Result

**TPC-H SF=1 suite wall clock: 115.37 s → 101.98 s, a 11.6% reduction
(0.884×). The before and after ranges are disjoint.**

| arm | pass 1 | pass 2 | rep min |
|---|---:|---:|---:|
| before, rep 1 | 118.17 | 115.37 | **115.37** |
| before, rep 2 | 115.97 | 117.84 | **115.97** |
| after, rep 1 | 101.98 | 103.56 | **101.98** |
| after, rep 2 | 102.96 | 102.17 | **102.17** |

before ∈ [115.37, 118.17], after ∈ [101.98, 103.56]. The gap between the
slowest *after* run and the fastest *before* run is 11.8 s, so this is not a
noise reading.

Summing per-query minimums instead of suite wall clock gives
**82.33 s → 69.44 s, −15.7%**. Both figures are reported because they answer
different questions: the suite wall clock includes per-query connection setup
and client drain, the per-query sum does not.

## 2. What changed

`indexScanOp.Next` called `catalog.InMemory.LookupEnum` **once per column per
row** to discover that the column is not an enum. TPC-H declares **zero enum
types**, so every one of those calls took an RWMutex read lock, lowercased a
type name, missed a map, and returned false.

Enum-ness is now resolved once per scan in `openPrep`, which is what the
**sequential-scan path already did**. This was sibling divergence, not a
missing design — and it is why TPC-DS, which is sequential-scan dominated,
never showed the cost.

Both paths now share `resolveEnumColumns` (`internal/executor/enumcols.go`),
so a future change cannot repair one and miss the other.

## 3. Where the time went — attribution

Per-query minimum across all measured passes, queries above 0.2 s:

| query | before (s) | after (s) | delta | % |
|---|---:|---:|---:|---:|
| **Q18** | 29.64 | 22.59 | −7.05 | **−23.8%** |
| **Q12** | 14.91 | 9.05 | −5.86 | **−39.3%** |
| Q19 | 2.08 | 2.01 | −0.07 | −3.4% |
| Q10 | 1.99 | 1.92 | −0.07 | −3.5% |
| Q21 | 11.42 | 11.36 | −0.06 | −0.5% |
| Q9 | 1.94 | 1.89 | −0.05 | −2.6% |
| Q4 | 1.51 | 1.46 | −0.05 | −3.3% |
| Q7 | 3.49 | 3.57 | +0.08 | +2.3% |
| Q5 | 2.96 | 3.03 | +0.07 | +2.4% |
| Q2 | 0.66 | 0.71 | +0.05 | +7.6% |
| Q6 | 0.71 | 0.75 | +0.04 | +5.6% |

**Q18 and Q12 alone account for 12.91 s of the 12.89 s total improvement.**
Every other query nets to approximately zero. That concentration is the
result's own consistency check: those two are the index-scan-heavy queries,
which is exactly where a per-row cost in `indexScanOp.Next` should live. A
diffuse improvement spread evenly across all 22 would have been evidence of
machine drift rather than of this change.

The small positives (Q2 +7.6%, Q6 +5.6%) are sub-0.1 s movements on
sub-1 s queries and are noise; they are listed rather than suppressed.

## 4. Correctness

- **TPC-H values: 24/24 MATCH on ordered digests**, compared with
  `tpch-runner -diff` between a before-arm and an after-arm log. This compares
  result *values*, not row counts — the distinction matters here, because a
  row-count-only gate has previously missed a 43× regression in this
  repository.
- **Executor suite green**; `go vet` clean.
- **A dedicated enum test, mutation-checked.**
  `TestIndexScanEnumOrderingSurvivesPrecompute` creates an enum whose
  declaration order (`zeta` < `mid` < `alpha`) is deliberately the reverse of
  its alphabetical order, so a scan that fails to inject `KindEnum` returns the
  alphabetical answer and fails. **With the precompute disabled the range
  predicate returns `[mid]` instead of `[mid alpha]`** — the test is not
  vacuous.

  This test is the *only* protection for this behaviour: **no TPC-H or TPC-DS
  query uses an enum at all**, so neither corpus gate can catch a regression
  here, however green it looks.

## 5. Honest limits on this result

- **TPC-DS is expected to show little or nothing**, and that was predicted
  before measuring, not explained afterwards. TPC-DS is sequential-scan
  dominated and already took the fixed path; its share of this cost was 0.10 s
  against TPC-H's 21.09 s. The TPC-DS regression gate remains required as a
  *values* check, not as a source of a timing claim.
- **The measured 11.6% is well below the 17.94% profile share, as predicted.**
  The profile figure was the CPU attributable to the call — an upper bound on
  the saving, not a forecast. Removing it shifts the bottleneck onto
  `DecodeHeapTupleRowInto`, which is 53.29% of the same operator, and TPC-H
  wall time is not purely CPU-bound.
- **Two further targets from the design were not implemented**, and the 11.6%
  does not include them: the `ToLower` in `catalog.PhysicalTypeIsVarlena`
  called per value from `codec.go:1450` (3.27 s TPC-H, 5.89 s TPC-DS), and
  `decodeRowRangeInfo`'s `info == nil` fallback (1.93 s / 2.30 s). The second
  is the more interesting of the two: its cost means some hot caller is passing
  `nil` where a resolved slice exists, which is a wiring gap rather than a
  missing mechanism. TPC-DS's share of the row-decode work sits almost entirely
  in these two.

## 6. A divergence found and deliberately not fixed

The sequential scan converts a `KindBytes` enum value; the index scan converts
only `KindString`. **The two paths have always disagreed about that case.**

Unifying them would change results on one path, so it needs its own reproducer
and its own values gate rather than a silent ride-along on a performance
change. The resolution half is now shared — which is the divergence that was
costing time — and the conversion difference is recorded in `enumcols.go` so
the next reader does not "tidy" it by accident.

## 7. Method

Conditions match `PERF-REPORT-20260905_ja.md` §2: same host,
`shared_buffers = 2GB`, `work_mem = 64MB`, parallelism enabled at 4 workers,
goopg on port 65433, `tpch@tpch`, `tpch-runner -digest` with a 600 s per-query
timeout.

Protocol per arm: fresh memory-capped server (`GOGC=100`,
`GOMEMLIMIT=12GiB`, own cgroup unit), one warm pass discarded, then two
measured passes.

**Arms were alternated** (before, after, before, after) rather than run in two
blocks, so machine drift over the ~25-minute run cannot masquerade as an
effect. Both binaries were built from the same tree at the same time, and each
arm's log records the binary's SHA-256 prefix so an arm cannot silently be
attributed to the wrong build.
