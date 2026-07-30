# M0125-0005 — the written decision: flip the `GOOPG_RELSIZE_FALLBACK` default

Date: 2026-07-30. Host: quiet (load ~0.9, no nightly batch, no bench servers).
Design: `docs/design/0125-0005-relsize-fallback-default-flip.md`.

This is evidence item 4 of that design's "Required evidence" list: a written
decision naming **what was traded for what**.

## Decision

**FLIP.** Unset `GOOPG_RELSIZE_FALLBACK` now means stage 2. `=0` restores the
old planner exactly.

## What was traded for what

**Gained:**

- TPC-DS SF0.5 timeout class **16 → 13**, `PASS` 79 → 82, common-PASS wall
  clock **−18.8 %** — with **zero** MISMATCH / CKMISMATCH / ERROR and all 78
  common PASSes agreeing on rows *and* value checksum.
- TPC-H 21-query stream **1.40×** (693.8 s → 494.0 s), zero regressions,
  identical rows.
- `tpch-spotcheck.sh`, the gate every commit pays, **2.43× faster**
  (75.0 s → 30.9 s), identical row counts, peak memory unchanged.

**Paid:**

- **TPC-DS Q72: 1.13× slower** (270 s → 305 s at a 900 s budget, 100 rows both
  arms), which crosses the SF0.5 gate's 300 s cap and turns its cell into a
  `TIMEOUT`. A budget crossing, not a hang. **Unexplained.**
- Every pre-2026-07-30 benchmark number in `analysis/` is now in a different
  planner regime and must not be compared across this commit.
- 22 / 22 TPC-H plans move (16 estimate-only, 6 structural), so the plan
  baseline had to be re-pinned.

**Explicitly NOT gained** — two pre-registered predictions were refuted, and
neither may be claimed:

- Q72 did not "resolve"; it was already passing and got slower.
- **Q35, the acceptance query, still times out.** The fallback is not what it
  was waiting for.

## Why "flip" rather than "measured and deliberately not flipped"

The latter was a legitimate outcome of this task, with `costDrivenJoinOrder` as
precedent. It was not chosen because the specific hazard that justified
shipping the flag off — `0125-0003` §D5.3's prediction that the fallback
imports round 4's statistics regressions into every un-ANALYZEd server — was
measured and **did not occur**. None of the five pre-registered regressed
queries regressed; Q12, round 4's 4.4× loss, is a 3.4× win. Three independent
workloads moved the same direction and no correctness delta appeared in any of
them.

## Files here

| file | what |
|---|---|
| `spotcheck-off-r{1,2}.txt` | gate at `GOOPG_RELSIZE_FALLBACK` unset, **pre-flip** (= stage 0) |
| `spotcheck-on-r{1,2}.txt` | gate at `GOOPG_RELSIZE_FALLBACK=2`, pre-flip |
| `spotcheck-default-after-flip.txt` | gate at unset, **post-flip** — the end-to-end proof the default reaches a real server (Q12 62 s → 18.83 s with no env var set) |
| `plan-diff-vs-preflip.txt` | `make plan-diff LABEL=tpcds-round2-head` — 22/22 diverged |
| `plan-gate-after-flip.txt` | `make plan-gate` vs the new baseline — 22/22 MATCH |
| `plandiff-server.log` | bench server log for the two plan runs — **local only, not committed** (`.gitignore:22` ignores `*.log` repo-wide) |

Arms were run **alternating** off/on/off/on, not blocked, so host drift could
not be mistaken for the effect. The measurement numbers are tabulated in the
design doc's Execution section rather than duplicated here.
