# Row decode part 2 — effect verification report

Branch `optimize-row-decode`. Change under test: `0b1bd7e6d`.
Baseline: `4bfadc38a` (the part-1 tip), **not** master — so this measures the
*incremental* gain of part 2 and stacks on part 1's 11.6% rather than
restating it. Date: 2026-09-08.

## 1. Result — TPC-H

**115.37 s (master) → 101.60 s (part 1) → 91.90 s (part 2).**
Part 2 alone: **101.60 → 91.90 s, −9.6% (0.904×), ranges disjoint.**

| arm | pass 1 | pass 2 | rep min |
|---|---:|---:|---:|
| part-1 baseline, rep 1 | 101.68 | 101.63 | **101.63** |
| part-1 baseline, rep 2 | 102.51 | 101.60 | **101.60** |
| part-2, rep 1 | 91.90 | 94.64 | **91.90** |
| part-2, rep 2 | 93.26 | 93.75 | **93.26** |

baseline ∈ [101.60, 102.51], part-2 ∈ [91.90, 94.64]. The slowest part-2 run
is 6.96 s faster than the fastest baseline run, so the ranges are disjoint by a
clear margin.

Summing per-query minimums: **68.19 → 62.41 s, −8.5%**.

Cumulative against master, both parts: **115.37 → 91.90 s, −20.3% (0.797×)**.

## 2. The improvement is broad, where part 1's was concentrated

Per-query minimum across measured passes, queries above 0.3 s:

| query | part 1 (s) | part 2 (s) | delta | % |
|---|---:|---:|---:|---:|
| Q18 | 22.01 | 19.95 | −2.06 | −9.4% |
| Q12 | 9.15 | 7.21 | −1.94 | −21.2% |
| Q21 | 11.28 | 10.66 | −0.62 | −5.5% |
| Q13 | 4.17 | 3.92 | −0.25 | −6.0% |
| Q19 | 1.97 | 1.82 | −0.15 | −7.6% |
| Q5 | 2.91 | 2.76 | −0.15 | −5.2% |
| Q1 | 2.65 | 2.55 | −0.10 | −3.8% |
| Q3 | 1.31 | 1.22 | −0.09 | −6.9% |
| Q22 | 0.62 | 0.60 | −0.02 | −3.2% |
| Q8 | 0.42 | 0.41 | −0.01 | −2.4% |
| Q7 | 3.48 | 3.48 | 0.00 | 0.0% |
| Q16 | 0.33 | 0.33 | 0.00 | 0.0% |

**Nothing regressed.** The shape differs from part 1 in a way that matches the
change: part 1 removed a cost specific to `indexScanOp`, so 12.91 s of its
12.89 s total landed on just Q18 and Q12. Part 2 removes a cost from the decode
loop **every scan shares**, so the gain is spread across almost every query.
Two different fixes, two different signatures, each consistent with its own
mechanism.

## 3. Correctness

- **TPC-H values: 24/24 MATCH on ordered digests** (`tpch-runner -diff`
  between a baseline arm and a part-2 arm). Values, not row counts.
- **Executor suite green**, `go vet` clean, full `go build ./...` clean.
- **Two mutation-checked tests**, both added by this change:
  - `TestColTypeInfoVarlenaMatchesLive` pins `colTypeInfo.isVarlena` against
    the live `catalog.PhysicalTypeIsVarlena` over 34 type spellings. Mutated to
    the tidier-looking `attLen == -1`, it fails on **exactly** `char`, `tid`,
    `money`, `macaddr` and `macaddr8` — the five the design predicted, no more
    and no fewer. Those disagree with `typlen` deliberately, and substituting
    would have moved on-disk offsets for them: a wrong-answer change wearing a
    performance change's clothes.
  - `TestColInfoMatchesDetectsMismatch` pins the positional precondition — a
    memo resolved from a different column list than the decoder walks would
    decode column `i` with column `j`'s descriptor. Mutated to always-true, it
    fails.

## 4. The predicted upper bound was WRONG, and in the interesting direction

`DESIGN-2.md` §4 predicted "~7.9% addressable on TPC-H" and called it an upper
bound. **The measured result is 9.6%, above that bound.**

That is not beating physics; it means **the bound was computed against a stale
profile**. The shares were read from `tpch-cold.pprof`, captured at
`cd637dd5b` — *before* part 1 landed. Part 1 removed 21.09 s of enum-lookup
work from `indexScanOp`, which re-weighted everything beneath it: the same
absolute decode saving is a larger *fraction* of a 101.60 s suite than of a
115.37 s one, and the operator mix moved as well.

Recorded rather than quietly dropped, because the failure mode generalises:
**a profile-derived bound is only valid against the build it was profiled on.**
Part 1's own prediction (17.94% bound, 11.6% delivered) was measured against a
profile of its own build and behaved as expected. This one was not.

## 5. TPC-DS — the prediction held: −9.2%

**Predicted before measuring** (and left in the previous revision of this
document so it could not be retrofitted): TPC-DS *should* move here, unlike
part 1, because `isVarlena` fixes a cost on the sequential-scan path that
dominates TPC-DS. The falsification condition was stated as "if TPC-DS comes
back at approximately zero, that refutes §2.1 of the design."

It did not come back at zero.

| | part-1 baseline | part 2 | delta |
|---|---:|---:|---:|
| 95 comparable queries, total | **977.8 s** | **888.3 s** | **−89.6 s, −9.2%** |

Both sweeps: `PASS=95 (57 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0 SKIP=4` against the git-tracked PG oracle. The values gate is clean
on both arms, which is required regardless of timing.

Largest movers:

| query | part 1 (s) | part 2 (s) | delta | % |
|---|---:|---:|---:|---:|
| Q18 | 147.2 | 122.6 | −24.5 | −16.7% |
| Q79 | 74.7 | 63.3 | −11.4 | −15.3% |
| Q14 | 94.4 | 86.1 | −8.4 | −8.8% |
| Q46 | 33.1 | 26.0 | −7.0 | −21.3% |
| Q88 | 48.6 | 42.7 | −5.9 | −12.1% |
| Q28 | 26.3 | 21.6 | −4.7 | −17.8% |
| Q48 | 9.3 | 6.1 | −3.2 | −34.1% |

**Two limits on this number, both real:**

1. **The arms were NOT alternated.** Each sweep takes about an hour, so the
   two ran back to back rather than interleaved as the TPC-H arms were. Drift
   over that window is a confound I cannot exclude here the way I could for
   TPC-H. The direction and rough magnitude are supported; the second digit is
   not.
2. **Each query is a single S-cold run**, so per-query figures are noisy. Four
   queries moved the *wrong* way by more than 15% (Q22 +31%, Q7 +27%, Q78
   +20%, Q5 +17%), totalling about 10 s against the 89.6 s gain. On single
   readings at this granularity that is noise, not a regression signal — but
   it is listed rather than suppressed, and it is why the per-query table
   should not be read as a per-query result.

**A number NOT to quote.** The sweep's own banner printed
`TOTAL 1143s → 887s (−22.4%)` for part 2. That comparison is against
`f0c9f36e7`, a stored run from the *previous* workstream, so it conflates
everything landed since that commit plus part 1 plus part 2. **It is not part
2's effect.** The −9.2% above is the like-for-like pair, both arms run today
on the same idle machine. Quoting the −22.4% would repeat precisely the
three-baselines error §1.2 of `PERF-REPORT-20260905.md` was written to stop.

## 5a. Both corpora, both parts

| | master | part 1 | part 2 | part 2 alone | cumulative |
|---|---:|---:|---:|---:|---:|
| TPC-H suite | 115.37 s | 101.60 s | **91.90 s** | −9.6% | **−20.3%** |
| TPC-DS SF0.5 (95 q) | not measured | 977.8 s | **888.3 s** | −9.2% | — |

TPC-DS was not measured against master, so no cumulative figure is claimed for
it. Part 1's design predicted TPC-DS would show ~nothing from that change, and
it was not measured then either; that remains an untested prediction rather
than a confirmed null.

## 6. Method

Conditions match `PERF-REPORT-20260905_ja.md` §2: same host,
`shared_buffers = 2GB`, `work_mem = 64MB`, parallelism at 4 workers, goopg on
65433, `tpch@tpch`, `tpch-runner -digest`, 600 s per-query timeout.

Per arm: fresh memory-capped server (`GOGC=100`, `GOMEMLIMIT=12GiB`, own cgroup
unit), one warm pass discarded, two measured passes.

**Arms alternated** (baseline, part-2, baseline, part-2) rather than blocked,
so drift over the ~25-minute run cannot masquerade as an effect. Both binaries
built from the same tree at the same time; each arm's log records its
binary's SHA-256 prefix so an arm cannot be silently attributed to the wrong
build.

Raw data: `measurements/`.
