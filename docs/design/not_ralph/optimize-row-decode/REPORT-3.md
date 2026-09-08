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

- **TPC-DS: COMPLETE. 886.0 s → 867.9 s, −2.0%** over 95 comparable queries,
  same-session pair on an otherwise idle machine. Both arms
  `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4` against the
  git-tracked PG oracle.

  A cross-session cross-check (part 2's sweep, verified same-binary —
  `0b1bd7e6d → 7642bac63` changes zero `.go` files) gave −2.3%. The two agree
  to 0.3 points, so the total is solid.

  **The prediction was wrong, and it was mine.** `DESIGN-3.md` argued TPC-DS
  would carry the *larger* half of this item because `acquireRow` was 16.6% of
  TPC-DS CPU against 8.4% of TPC-H. Measured: TPC-H **−5.6%**, TPC-DS
  **−2.0%** — well under half the TPC-H gain, not more.

  This is the **third** time in this series a CPU share was read as a forecast
  (part 1 predicted 17.94% and delivered 11.6%; part 2 predicted ≤7.9% from a
  stale profile and delivered 9.6%). It is the workstream's single most
  repeated methodological error, and the correct statement of the rule is:
  **a profile says where cycles are spent, not which cycles are on the
  critical path.** TPC-DS spends much of its time in spilling sorts, hash
  builds and I/O, so CPU removed from `acquireRow` partly exposes a different
  bottleneck instead of converting to wall time.

  **PER-QUERY ATTRIBUTION IS NOT AVAILABLE HERE, and an earlier reading of it
  was wrong.** Splitting the corpus at 20 s gives directly contradictory
  answers depending on which baseline is used:

  | subset | cross-session | same-session |
  |---|---:|---:|
  | 7 heavy queries (>20 s, ~47% of total) | +0.5% | **+3.8%** |
  | the other 88 | +3.8% | **+0.5%** |

  The totals agree (+2.3% vs +2.0%) while the attribution **inverts
  completely**. Each query is a single S-cold run in each arm, so per-query
  figures are noise-dominated and the split is an artifact. An interim note in
  this report previously concluded the gain "lives entirely in the lighter
  queries"; **that conclusion is withdrawn** — the same-session data says the
  opposite, and neither is trustworthy at that granularity.

  Individual queries behave accordingly: Q28 read +16.9% (slower) against one
  baseline and −3.5% against the other, i.e. noise. Q23 is slower in both
  (−12.7% same-session), which makes it the one candidate for a real
  regression; it is recorded, not explained, and would need repeated runs to
  confirm.

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
