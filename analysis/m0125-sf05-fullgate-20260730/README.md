# TPC-DS SF0.5 full gate — 2026-07-30, HEAD `e29faca9`

**Verdict: the gate PASSES. 99/99 queries covered, ZERO correctness failures.**

```
PASS=79 (49 ck-verified, 30 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=16 SKIP=4
```

Report: [`sweep-COMPLETE-20260730-102025.txt`](sweep-COMPLETE-20260730-102025.txt)
(driver log: [`driver-complete-run.txt`](driver-complete-run.txt) — `.txt`, not
`.log`, because `.gitignore:22` ignores `*.log` and this is evidence).
10:20 → 12:26, one binary, one server lineage, 122.5 min of query time on a
**verified quiet host** (`pgrep -af run-nightly.sh` empty, load 1.66 at start;
the nightly's next fire was ~14 h away).

This run discharges the standing "one full 99-query SF0.5 gate on a quiet host"
obligation that had been owed since 2026-07-29 — four consecutive loops landed
correctness fixes (M0125-0007, -0008, -0023, -0013, -0004) whose only measured
evidence was a per-query probe.

## What changed vs the 2026-07-29 baseline

Baseline: `analysis/tpcds-sf05-full-gate-20260729/merged-sweep-head-50cf7c5f.txt`
(`PASS=75 (46 ck-verified) MISMATCH=1 CKMISMATCH=3 ERROR=1 TIMEOUT=15 SKIP=4`).
**Five per-query changes, of 99 — every one is an expected consequence of a
landed fix, and there are no changes in the other direction:**

| query | baseline → now | attribution |
|---|---|---|
| Q16 | `CKMISMATCH` → **PASS** `ck=40dbec0df91d2438` | M0125-0008 (Semi/Anti `Join.Output()`), on top of M0125-0007 |
| Q94 | `CKMISMATCH` → **PASS** `ck=04afc1b69831a5ea` | M0125-0008 |
| Q95 | `CKMISMATCH` → **PASS** `ck=e498634c02595c29` | M0125-0008 / -0023 (`IN (subquery)` unnests to `JoinTypeSemi`) |
| Q75 | `ERROR` → **PASS** (100 rows) | M0125-0004 (Q75 join-residual evaluation order) — also clears the live `Q75,100,pinned` nightly anchor |
| Q47 | `MISMATCH` → **TIMEOUT** | M0125-0013 fixed the ROW defect (0 → 100 rows = oracle); the query now does the real work and no longer fits the 300 s SF0.5 budget |

So the three CKMISMATCH cells that M0124-0005's checksum column existed to find
are closed **by independent measurement**, not by the 3-query probe that was all
the previous loops could take.

### Q47 is the one status that got "worse", and it is not a regression

Q47's row defect is fixed; what the gate now sees is its **runtime**, which is
the still-open bookkeeping half of M0125-0013 (`8.4x`, set A `OK 17 s` → HEAD
`OK 142 s` at SF=1). At SF0.5 the same work exceeds the 300 s cap, so Q47 joins
the timeout class and `TIMEOUT` goes 15 → 16. Two consequences for later loops:

- **M0125-0026's capture list should be 16 queries, not 15** — Q47 belongs in the
  plain-EXPLAIN comparison (Q5 Q8 Q10 Q14 Q30 Q31 Q35 **Q47** Q54 Q64 Q65 Q67 Q69
  Q71 Q78 Q81).
- Q47 is *not* a "budget-marginal" member in D6's sense (0124-0001 §D6): its
  SF=1 standalone reading is 142 s against a 300 s SF0.5 cap on half the data, so
  the cut is not within ~5 % of a known completion. It needs its own reading
  before being called unbounded.

## Provenance — the first run under the closed gate-integrity trap

The header is the point of this run as much as the verdict is. It carries design
**0124-0001 D4a**'s three fields, now shared with the SF=1 harness:

```
# goopg: e29faca9 ralph(plan)+docs: M0125-0026 — …
# engine-id: 5a45a42d33ef58ff9de0bcf5f82aa6ccfb66ddd5 c47d4ed683a0ac63d56c7f755e70892a635f3a42  diff=e3b0c44298fc
# build: rebuilt from tree
# engine-binary: running=ca653634810c2821 on-disk=ca653634810c2821 (…/tmp/goopg-sf05-bin)
```

`diff=e3b0c44298fc` is the empty-diff digest: the working tree's uncommitted
changes at run time were **shell/docs only**, so the engine is exactly
`e29faca9`'s code. `running == on-disk` across the whole run, and none of the
**sixteen** `RESTART_AFTER_TIMEOUT` bounces (one per timeout) printed
`*** SWEEP VOID ***` — the
guard that makes that statement checkable is new in this run
(`sf05_guard_engine_stable`; see `docs/design/0125-0011-…` §"The trap is closed").

`GOOPG_BIN` was pointed at a private `tmp/goopg-sf05-bin` rather than the shared
`tmp/goopg-bench-bin`, so this run could not have been perturbed by — or have
perturbed — any TPC-H/nightly harness that builds into the shared path.

## The dead chunked attempt in this directory (do NOT cite its numbers)

`sweep-20260730-0{74649,81731,84134,92607}.txt` + `run-chunks.sh` (and a
`driver.log` that stays untracked, per the `*.log` rule above) are the earlier,
**abandoned** `FORCE=1` chunked attempt from the same day. They
are kept only as the record of why the run was re-done, and they are invalid as
evidence:

- it ran beside the nightly CI batch (`FORCE=1`), so **all seconds are void**;
- chunk Q1–30 has four `ERROR` cells (Q5/Q8/Q14/Q30) that are **host artifacts** —
  the server was `Killed` under memory pressure, not a query error (see
  `driver.log`);
- chunk Q54–72 was killed mid-Q88, so the board was never complete.

The complete run above supersedes all four chunks.
