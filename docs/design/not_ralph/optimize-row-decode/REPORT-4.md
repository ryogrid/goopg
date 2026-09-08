# Row decode part 4 — effect verification report

Branch `optimize-row-decode`. Change under test: `9112c762c`.
Baseline: `1ab23ceb6` (the part-3 tip). Date: 2026-09-08.

## 1. Result — TPC-H

**86.88 s → 82.29 s, −5.3%. Ranges disjoint.**

| arm | pass 1 | pass 2 | rep min |
|---|---:|---:|---:|
| before, rep 1 | 86.88 | 89.46 | **86.88** |
| before, rep 2 | 89.68 | 87.71 | 87.71 |
| after, rep 1 | 82.29 | 82.34 | **82.29** |
| after, rep 2 | 83.01 | 82.34 | 82.34 |

before ∈ [86.88, 89.68], after ∈ [82.29, 83.01]. The slowest *after* run is
3.87 s faster than the fastest *before* run.

Sum of per-query minimums: **58.42 → 55.10 s, −5.7%**.

Cumulative over the four parts: **115.37 → 82.29 s, −28.7% (0.713×)**.

**Provenance verified**, because this project has previously had an arm run
silently against the wrong binary: both arms' logged SHA-256 prefixes match
their binaries on disk (`5504f121…` / `d552fcbb…`), and the before-arm's
worktree is confirmed at `1ab23ceb6`. Independent consistency check: part 4's
before-arm measured 86.88–89.68 s and part 3's after-arm measured
86.36–89.55 s on that same commit in a *separate session* — overlapping
almost exactly.

## 2. The line-level evidence: the copies are gone

The same `pprof -list` view that motivated the change, re-taken on the final
build:

| line | before | after | |
|---|---:|---:|---|
| `c := cols[i]` → `c := &cols[i]` (312-byte copy) | 5.62 s | **0.11 s** | **51× less** |
| decoder call site (48-byte `Type` by value → pointer) | 25.69 s | **16.44 s** | −9.25 s |
| `dst[i] = v` (cut C, deliberately not done) | 4.52 s | 3.87 s | ~unchanged |

Cut A did exactly what it was predicted to do. Cut B removed 9.25 s from the
call site — **more than the 48-byte copy alone accounts for**, which is why the
measured 5.3% exceeds the ~2.5% the design projected from cut A's line in
isolation. The line attribution could not separate the two cuts in advance;
the post-change profile does.

## 3. Uniform improvement — the expected signature

Per-query minimums, queries above 0.3 s:

| query | before (s) | after (s) | % |
|---|---:|---:|---:|
| Q12 | 7.03 | 6.35 | −9.7% |
| Q5 | 2.51 | 2.32 | −7.6% |
| Q18 | 18.60 | 17.54 | −5.7% |
| Q9 | 1.58 | 1.49 | −5.7% |
| Q21 | 10.01 | 9.46 | −5.5% |
| Q19 | 1.65 | 1.56 | −5.5% |
| Q13 | 3.89 | 3.69 | −5.1% |
| Q10 | 1.77 | 1.68 | −5.1% |
| Q2 | 0.58 | 0.57 | −1.7% |
| Q8, Q14 | — | — | 0.0% |

**Nothing regressed.** A uniform 5–10% is what removing a per-value cost every
scan pays should look like.

Across the series each change produced a distinct signature matching its own
mechanism — part 1 concentrated in two index-scan-heavy queries, part 2
broad-but-uneven, parts 3 and 4 flat. **Four changes, four signatures, each
consistent with its cause.** That is a stronger check on attribution than any
single percentage, and it is the reason these results are quoted with
confidence while the per-query TPC-DS splits in `REPORT-3.md` are not.

## 4. Correctness

- **TPC-H values: 24/24 MATCH** on ordered digests.
- **Units gate: 44 packages, 0 failures.** `go build ./...`, `go vet` clean.
  `-race` clean on the parts 1–4 test set.
- **`TestDecodeMissingValueAfterAddColumn`** covers the `i >= storedNatts` arm
  — the only reader of `c.MissingValue`, and a path **neither TPC corpus
  exercises**, so the corpus gates are blind to a regression there.
  Mutation-checked: making the arm yield NULL fails it.
- **`TestDecodePointerArgMatchesValueArg`** pins cut B's read-only contract.
  This matters more than it first appears: the caller passes `&c.Type` where
  `c` now points **into the caller's own column slice**, so a callee that
  mutated through the pointer would corrupt the catalog column, not a
  temporary.

## 5. Not measured

**TPC-DS was not A/B-measured for this part.** A profile of the final build was
captured instead (used in `REPORT-EXHAUSTION.md`), which answers "what is left"
but not "how much did this part give on TPC-DS". Stated rather than
extrapolated: `decodeRowRangeInfo` is a larger share of TPC-DS than of TPC-H,
so a naive reading predicts a larger gain — but `REPORT-3.md` records that
exact prediction failing, and this series has now read a CPU share as a
forecast three times. **No TPC-DS figure is claimed for part 4.**
