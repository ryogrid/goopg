# TPC-DS SF0.5 — the full 99-query gate, finally run (2026-07-29)

**Result: 99/99 covered at HEAD `50cf7c5f`, ZERO regressions against the
`sweep-20260727-214619` baseline.** Eight per-query statuses differ from that
baseline and every one of them is accounted for below; none is a regression.

This discharges the standing gate debt named as item 4 of the `.ralph/fix_plan.md`
Current Priority banner ("The full 99-query SF0.5 gate, once"). The debt had
accumulated across **eight loops**: `M0125-0016`/`-0017` were accepted on 6- and
4-query subsets, `M0125-0018`/`-0019`/`-0021` ran zero SF0.5 queries, and the
2026-07-29 attempt (`sweep-20260729-181319`) reached only Q53 of 99 in 57 minutes
before its session's wall-clock cap.

## How it was run

The gate is ~110 minutes end-to-end, which exceeds a single loop's foreground
command budget. It was therefore run as **four contiguous `QUERIES=` chunks on
one binary**, back to back on a quiet host, and merged:

| chunk | report | wall |
|---|---|---|
| Q54–72 | `sweep-20260729-210715.txt` | 21:07 → 21:51 |
| Q73–99 | `sweep-20260729-215120.txt` | 21:51 → 22:13 |
| Q1–30 | `sweep-20260729-221359.txt` | 22:14 → 22:57 |
| Q31–53 | `sweep-20260729-225808.txt` | 22:58 → 23:16 |

Merged into `merged-sweep-head-50cf7c5f.txt` (99 rows, one per query).

**Why the chunking is sound, and where it is weaker than one run.** Each chunk
is `sf05_goopg_start`-ed fresh and runs S-cold, which is the gate's own
determinism contract (design §7.1: goopg loses `TableStats.RowCount` on restart,
so S-cold is the only reproducible state). `RESTART_AFTER_TIMEOUT=1` already
bounces the server mid-chunk after every timeout, so a chunk boundary is the
same event the gate performs internally 15 times anyway. The one thing chunking
does *not* reproduce is a full-length single-process heap history — a leak that
only manifests after ~90 minutes of continuous serving would be invisible here.
The script stamps each chunk "SUBSET PROBE — NOT a gate result" and that stamp
is correct per-file; the merged artifact is the gate result, and this note is
the record of how it was assembled.

Every chunk ran `TIMEOUT_SEC=300` (the current default), against the 2026-07-27
baseline's `180`. That difference is load-bearing for two of the eight diffs —
see Q72/Q88 below.

## Totals

```
PASS=75 (46 ck-verified, 29 ck=n/a)  MISMATCH=1  CKMISMATCH=3  ERROR=1  TIMEOUT=15  SKIP=4
```
Baseline 2026-07-27: `PASS=74 MISMATCH=3 ERROR=2 TIMEOUT=16 SKIP=4` (row-count
only — that run predates the value checksum, so it had no CKMISMATCH class at
all).

The **17-query timeout class** M0125 is named after now reads **15** at SF0.5:
Q5 Q8 Q10 Q14 Q30 Q31 Q35 Q54 Q64 Q65 Q67 Q69 Q71 Q78 Q81. (Q4 skips because
the *oracle* timed out; Q36/Q70/Q86 are dsqgen artefacts.)

## The eight diffs, each accounted for

| query | baseline | HEAD | why |
|---|---|---|---|
| Q8 | ERROR | TIMEOUT | **Expected.** `M0125-0012` (`50cf7c5f`) fixed the `ca_zip/57 out of MaterializedSlot range` remap defect, moving Q8 out of the error class into the timeout class. Predicted verbatim by the previous loop's ledger row; **not** a regression. |
| Q16 | PASS | CKMISMATCH | **Detection upgrade, not a regression.** The baseline compared row counts only; both engines return 1 row, so it passed trivially. `M0124-0005`'s value checksum now sees the answer: goopg `512b5fdab820c47b` vs oracle `40dbec0df91d2438`. Already filed as **M0125-0007**. |
| Q94 | PASS | CKMISMATCH | Same as Q16 — goopg `512b5fdab820c47b` vs oracle `04afc1b69831a5ea`. **M0125-0007.** |
| Q95 | PASS | CKMISMATCH | Same as Q16 — goopg `512b5fdab820c47b` vs oracle `e498634c02595c29`. **M0125-0007.** |
| Q49 | MISMATCH | PASS | Fixed since the baseline; now ck-verified `ccb0983b810cf1d0`. |
| Q51 | MISMATCH | PASS | Fixed since the baseline (row count 100 = saturated LIMIT, so `ck=n/a`). |
| Q72 | TIMEOUT | PASS | **Threshold artefact.** Completes in 263 s, i.e. above the baseline's 180 s cap and below today's 300 s. No code fixed this. |
| Q88 | TIMEOUT | PASS | Same as Q72 — completes in 236 s, ck-verified `272de7cd629d9033`. |

**Q16/Q94/Q95 all report the identical goopg checksum `512b5fdab820c47b`**, which
is the `0 / NULL / NULL` answer M0125-0007 already describes. One defect, three
queries — not three findings.

## What this changes for the milestone

1. **The gate debt is discharged.** M0125-0016 … -0021 were all landed without a
   full-gate run; this sweep covers every one of them at once and finds nothing
   broken. No follow-up is owed for those six items.
2. **M0124-0005's value checksum earned its keep on first full use.** It converted
   three silent row-count passes into three detected wrong answers. The baseline's
   `PASS=74` was overstated by exactly 3.
3. **M0125-0014 / -0015 (Q49 / Q51) now PASS at SF0.5.** This is *evidence*, not a
   discharge: both items are specified as **SF=1** re-measurements, and SF0.5 is a
   key-parity half-sample, so a defect that needs the full fact table can hide
   here. Take the SF=1 reading before ticking either.
4. **Q75 remains the only ERROR** (`M0125-0004`), and Q47 the only row-count
   MISMATCH (goopg 0 vs oracle 100) — both already filed, both unchanged.
