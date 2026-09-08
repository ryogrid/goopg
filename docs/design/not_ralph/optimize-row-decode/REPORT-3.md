# Row decode part 3 — effect verification report

Branch `optimize-row-decode`. Change under test: `1ab23ceb6` (remove the row
pool). Baseline: `7642bac63` (the part-2 tip). Date: 2026-09-08.

## 1. Result — TPC-H

**91.52 s → 86.36 s, −5.6%. Ranges disjoint.**

| arm | pass 1 | pass 2 | rep min |
|---|---:|---:|---:|
| before, rep 1 | 94.10 | 91.64 | **91.64** |
| before, rep 2 | 94.32 | 91.52 | **91.52** |
| after, rep 1 | 89.55 | 86.60 | **86.60** |
| after, rep 2 | 88.86 | 86.36 | **86.36** |

before ∈ [91.52, 94.32], after ∈ [86.36, 89.55]. The slowest *after* run is
1.97 s faster than the fastest *before* run.

Summing per-query minimums: **61.44 → 58.14 s, −5.4%** — consistent with the
suite figure, as it should be for a change with no plan effect.

Cumulative over the three parts: **115.37 → 86.36 s, −25.1% (0.749×)**.

## 2. The improvement is uniform, which is the expected signature

| query | before (s) | after (s) | delta | % |
|---|---:|---:|---:|---:|
| Q9 | 1.79 | 1.61 | −0.18 | −10.1% |
| Q5 | 2.70 | 2.46 | −0.24 | −8.9% |
| Q7 | 3.43 | 3.19 | −0.24 | −7.0% |
| Q21 | 10.50 | 9.96 | −0.54 | −5.1% |
| Q18 | 19.22 | 18.32 | −0.90 | −4.7% |
| Q13 | 3.94 | 3.76 | −0.18 | −4.6% |
| Q12 | 7.48 | 7.16 | −0.32 | −4.3% |
| Q17 | 0.46 | 0.44 | −0.02 | −4.3% |
| Q22 | 0.58 | 0.56 | −0.02 | −3.4% |
| Q14 | 0.39 | 0.40 | +0.01 | +2.6% |

Almost every query improves by 4–10%, and the one exception is a 0.01 s
movement on a 0.4 s query. That flatness is the point: this removes a cost
paid **once per row by every scan**, so a uniform percentage is what a correct
fix looks like. Part 1's gain was concentrated in two queries and part 2's was
broad-but-uneven; each signature matched its own mechanism.

## 3. Correctness

- **TPC-H values: 24/24 MATCH** on ordered digests (`tpch-runner -diff`).
- **Units gate: 44 packages, 0 failures.** Full `go build ./...` and `go vet`
  clean.
- **Four new tests** (`row_pool_test.go`): the length/cap/all-zero contract the
  pooled version documented, negative width, `releaseRow` inertness across the
  shapes its call sites pass, and — the one that matters — **an anti-recycling
  pin** asserting two live rows never share backing storage. Nothing may
  reintroduce aliasing: a consumer holding row A would otherwise see row B's
  values appear underneath it.

## 4. The allocation census confirmed the mechanism, unprompted

An existing test, `TestDenseBuildAllocsPerRow`, **failed on this change** — and
failed in the direction that proves the diagnosis:

```
legacy header-only allocs/row: 2.00 (pooled)  →  1.000 (make)
```

It asserted `>= 1.5`. The pooled path cost **two** allocations per row: the row
itself, plus `runtime.convTslice` boxing the slice header because `sync.Pool`
stores interface values. Removing the pool left exactly the one `make` the row
needs.

This is independent confirmation that the boxing identified in `DESIGN-3.md`
§1.3 was real and not a profiling artefact — and it arrived from a test written
for an unrelated purpose, which is stronger evidence than a measurement
designed to find it.

The bound was **re-baselined to `>= 0.9`, not deleted**: its job is to catch the
day the legacy path stops allocating per row altogether, which would make the
dense path's comparison vacuous. That job is unchanged.

## 5. Still outstanding

- **TPC-DS: after-arm complete, before-arm running.** `PASS=95 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4` — values gate clean. Total over 95
  comparable queries: **867.9 s**.

  **Interim, cross-session: 888.3 s → 867.9 s = −2.3%.** The 888.3 s baseline
  is part 2's sweep, which is legitimate for a cross-check because
  `0b1bd7e6d → 7642bac63` changes **zero `.go` files** — the same binary
  functionally — but it spans hours of machine drift, so the same-session
  before-arm now running is the primary and this figure may move.

  **This contradicts the prediction, and the prediction was mine.** `DESIGN-3.md`
  argued TPC-DS would carry the *larger* half of this item because `acquireRow`
  was 16.6% of TPC-DS CPU against 8.4% of TPC-H. TPC-H delivered −5.6%; TPC-DS
  is tracking about −2.3%, i.e. **less than half** the TPC-H gain rather than
  more.

  If the same-session pair confirms it, the conclusion is that **a CPU-share
  figure again over-predicted wall-clock gain**, and for a specific reason:
  TPC-DS is more allocation- and I/O-bound than TPC-H, so removing CPU from
  `acquireRow` exposes a different bottleneck rather than converting to wall
  time. That would be the **third** time in this series a share was read as a
  forecast — parts 1 and 2 both over- or under-shot for the same class of
  reason — and it is the single most repeated methodological error of this
  workstream.
- **GC pressure — the risk `DESIGN-3.md` §2.1 named — is not yet closed by a
  direct measurement.** §4's allocs/row halving is strong evidence that
  allocation *fell*, but a `GODEBUG=gctrace=1` arm at `GOGC=100` is queued
  behind the sweeps to count GC cycles on both builds. Until it lands, "GC
  pressure did not increase" is an inference, not a measurement, and is
  labelled as such.

## 6. Method

Conditions match `PERF-REPORT-20260905_ja.md` §2: `shared_buffers = 2GB`,
`work_mem = 64MB`, parallelism at 4 workers, goopg on 65433, `tpch@tpch`,
`tpch-runner -digest`, 600 s per-query timeout. Fresh memory-capped server per
arm (`GOGC=100`, `GOMEMLIMIT=12GiB`, own cgroup unit), one warm pass discarded,
two measured.

**Arms alternated** (before, after, before, after), not blocked, so drift
cannot masquerade as an effect. Each arm's log records its binary's SHA-256
prefix.

Profiles that motivated the change were captured on the **current** build
(`7642bac63`), not inherited — part 2's report records what a stale-profile
bound costs.
