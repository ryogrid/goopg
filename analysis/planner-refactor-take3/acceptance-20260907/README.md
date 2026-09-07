# C-21 / P7-01 — acceptance run verdict

## 6. Conditions (mandatory header)

| | |
|---|---|
| date | 2026-09-07 |
| **before arm** | `d93fb9edc` — *release baseline 2026-09-01, NOT the branch point* |
| **after arm** | branch tip (`0d4e934c2` era), binary `goopg-tip` |
| provenance check | `GOOPG_NARROW_UPPER` present in tip (B-01c compiled in), absent in before |
| suites | TPC-H SF=1 (db `tpch`), TPC-DS SF0.5 |
| settings | `shared_buffers=2GB`, `work_mem=64MB`, `GOGC=100 GOMEMLIMIT=12GiB`, cgroup-capped, `GOOPG_ANALYZE_SEED=20260905` |
| flags | `GOOPG_PGSHAPED_DP=1` (shipped default), fresh server per arm |

**Attribution caveat, load-bearing.** The merge-base of the branch and `master`
is `53e000801`, which is **333 commits after** `d93fb9edc`. So before→tip spans
**706 commits, of which only 373 belong to this workstream**. Every figure
below is **release-over-release since 2026-09-01**, not this workstream's
delta.

**Parallelism differs by artifact class.** Timing arms run with parallelism
**enabled** (`max_parallel_workers_per_gather=4`). Plan-parity captures run
with it **suppressed** (`estimate-audit --serial` default), so `plans-pg/`
contains zero `Gather` nodes and A1/A2 are serial-control-arm statements.

---

## Bars

### B1 — correctness floor: **PASS**

- TPC-H WARM: **all 22 `ordered=` value hashes IDENTICAL** between arms.
- TPC-DS SF0.5, both arms: **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0**.

No row-count, ordering or checksum regression anywhere. This is the
non-negotiable bar and it holds.

### B2 — timing ceilings: **FAIL (2 violations), against a large net gain**

| regime | before | tip | factor |
|---|---:|---:|---:|
| **TPC-H WARM (22q)** | **274.18 s** | **96.72 s** | **0.353× (2.83× faster)** |
| **TPC-H S-cold (21q)** | **284.78 s** | **111.93 s** | **0.393× (2.54× faster)** |

Violations of the "no query slower than 1.2×" rule:

| query | regime | before | tip | factor |
|---|---|---:|---:|---:|
| **Q1** | S-cold | 7.08 s | **15.51 s** | **2.19×** |
| Q11 | both | 0.11 s | 0.16 s | 1.45× (absolute delta 0.05 s — at the resolution floor) |

Q1 S-cold is a genuine violation and is stated as such. Q11 is 50 ms on a
0.11 s query and should not be read as a regression signal.

### A1 — TPC-H plan parity (serial): **improved by 1**

| arm | match | shapediff | missingnode | error |
|---|---:|---:|---:|---:|
| before | 5 | 15 | 2 | 0 |
| tip | **6** | **14** | 2 | 0 |

### A2 — TPC-DS plan parity (serial): **unmoved**

| arm | match | shapediff | missingnode | error |
|---|---:|---:|---:|---:|
| before | 0 | 35 | 61 | 3 |
| tip | **0** | 34 | **62** | 3 |

**Match stays 0 and `missingnode` gets one worse.** TPC-DS parity did not move.

### A3 — MISSING-NODE: TPC-H 2 (unchanged), TPC-DS 61 → 62 (worse by 1)

### A4 — join-spine parity: **not separately measured** in this run; the
category counts above are the only spine evidence captured. Recorded as not
run rather than omitted.

### A5 / B3 — estimate ratchet: **FAIL on both arms, but improved**

| arm | verdict | new findings |
|---|---|---:|
| before | FAIL | **50** |
| tip | FAIL | **40** |

Both arms fail the ratchet outright. The tip arm carries **ten fewer** new
estimate findings than the release baseline, so estimate quality moved in the
right direction while remaining below the bar. This is the **first time the EA
ratchet has ever executed** — the ledger row `take3-ea-ratchet-never-ran`
recorded it as having no Makefile target, and `make ea-ratchet` now exists
(`Makefile:616`).

### B4 — engine time against PG (directional, NOT an acceptance bar)

From the matched 2026-09-06 capture (`bench/tpch/timings/`, parallelism on,
both engines at the same settings): **TPC-H total goopg 104.9 s against PG
14.4 s = 7.3×**, against the directional destination of ≤3.0×. Per-query the
spread is 0.4× (Q17, goopg faster) to 33× (Q19).

TPC-DS SF0.5: goopg 1114 s (99 completed) against PG 585 s (98 completed, 1
failed). **Not comparable as printed** — PG's Q4 times out and is excluded from
its total while goopg completes it in 25 s.

### C1–C7 — hygiene: **PASS**

C1 no benchmark-identifying rule added. C2 no new penalty multiplier or
one-query constant. C3 both suites timed for shape changes. C4 fresh
cgroup-capped server per arm. C5 no `-count=1` in any gate. C6 no
`--no-verify` (the `-n` bypass is the owner's standing instruction for this
workstream and the pre-commit hook ran on the pgbench smoke). C7 every
deferral carries a ledger row.

---

## What got worse — explicit statement

1. **Q1 S-cold 7.08 → 15.51 s (2.19×)** — the one substantive timing
   violation. Not diagnosed in this run.
2. **TPC-DS aggregate +9.1%** (932 s → 1017 s on the sweep's own status-delta).
   The comparison baseline is the sweep's previous report, not necessarily the
   `d93fb9edc` arm, so the attribution is unsettled.
3. **TPC-DS plan parity did not move** — match 0 → 0, `missingnode` 61 → 62.
4. **The estimate ratchet fails on both arms** (50 → 40 findings).
5. **Q11 1.45×**, absolute 0.05 s — reported for completeness, not a signal.
6. **The `Parallel Hash` gap** — goopg parallelises only the outer scan of a
   parallel hash join; the build cannot be split. Evidenced in
   `../q9-parallel-plans-20260907/`.

## Negative results kept verbatim

- The first `after` arm was built, probed, found to be missing C-06 and B-01c,
  and **discarded**; its TPC-DS sweep was killed mid-run rather than completed,
  because a completed run would have produced a plausible number for the wrong
  tree.
- The EA tip arm initially **reused a stale capture** and was re-run against a
  fresh capture path. The figure above is from the re-run.
- A sweep artifact's `# goopg:` header names the **checkout**, not the binary.
  The tip sweep therefore self-describes as `f213b6f5d` while testing a binary
  containing `0d4e934c2`; `# engine-binary:` is the authoritative line.

## Verdict

**B1 passes. B2 fails on Q1 S-cold against a 2.5–2.8× net improvement. A1
improves by one query, A2 does not move, A5/B3 fail on both arms while
improving. B4 remains 7.3× against a ≤3.0× destination.**

Bundle acceptance (A1–A5 + B1–B3 + C) is therefore **NOT met**: A2, A5, B2 and
B3 do not clear. The workstream's measurable outcome is a large runtime
improvement with values held exactly, and plan parity essentially unchanged on
the larger corpus.

## Per-query, TPC-H WARM

| query | before (s) | tip (s) | factor |
|---|---:|---:|---:|
| Q1 | 3.33 | 2.90 | 0.87x |
| Q2 | 1.06 | 0.71 | 0.67x |
| Q3 | 5.07 | 2.39 | 0.47x |
| Q4 | 1.67 | 1.71 | 1.02x |
| Q5 | 39.80 | 3.68 | 0.09x |
| Q6 | 0.68 | 0.74 | 1.09x |
| Q7 | 27.21 | 4.77 | 0.18x |
| Q8 | 0.43 | 0.47 | 1.09x |
| Q9 | 73.08 | 9.54 | 0.13x |
| Q10 | 4.05 | 2.49 | 0.61x |
| Q11 | 0.11 | 0.16 | 1.45x |
| Q12 | 14.34 | 13.46 | 0.94x |
| Q13 | 7.57 | 4.12 | 0.54x |
| Q14 | 1.03 | 0.51 | 0.50x |
| Q16 | 1.60 | 0.34 | 0.21x |
| Q17 | 0.47 | 0.53 | 1.13x |
| Q18 | 74.57 | 31.40 | 0.42x |
| Q19 | 2.73 | 1.99 | 0.73x |
| Q20 | 1.28 | 1.33 | 1.04x |
| Q21 | 13.43 | 12.86 | 0.96x |
| Q22 | 0.67 | 0.62 | 0.93x |
| **TOTAL** | **274.18** | **96.72** | **0.353×** |
