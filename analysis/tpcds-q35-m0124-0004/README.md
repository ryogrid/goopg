# M0124-0004 — Q35 row-count probe artefacts

## Result (2026-07-30): Q35 is CLASSIFIED performance-only; the count stays unrecovered

Q35 has **never completed on goopg at any scale factor**, and cannot, with this
plan, in any budget worth spending. The 2026-07-26 `OK, 525 s` reading is
**refuted** — not merely unreproduced.

| quantity | value |
|---|---|
| outer cardinality (`customer ⋈ customer_address ⋈ customer_demographics`) | **96,562** |
| one `EXISTS` #1 evaluation, buffer-**warm**, ×4 | 8.20 / 8.16 / 8.18 / 8.16 s |
| ⇒ `EXISTS` #1 alone, SF=1 | 96,562 × 8.16 s ≈ **9.1 days** |
| ⇒ same at SF0.5 (facts halved, outer unchanged) | ≈ **4.6 days** |

A plan whose *cheapest* conjunct floors at nine days did not return 100 rows in
525 s. `651 s` / `628 s` are kill lines, not runtimes.

**The SF0.5-slower-than-SF=1 "anomaly" dissolves**: the sampler halves facts by
key parity but copies dimensions whole, so the outer cardinality is unchanged and
only the inner re-scan halves. Both scale factors are timeouts; the wall-clock
ordering of two kill lines carries no information.

## Readings

Everything from **2026-07-30 is VALID** — taken on a verified quiet host (no
`run-nightly`, no `ci/batch/stages/`, no goopg server, load 0.99 falling, 26 GiB
free). Everything from 2026-07-29 05:xx–06:xx is **VOID**: the nightly CI batch
was running throughout (fired `2026-07-29T00:23:44`; TPC-H stage at 112 % CPU /
7.5 GiB RSS on the 16-core host). The void pair is kept so a later loop does not
re-derive it, not as evidence about Q35.

| file | what | verdict |
|---|---|---|
| `sweep-20260729-232335.txt` | solo SF0.5 sweep, fresh server, `TIMEOUT_SEC=1800` | `TIMEOUT` 1964 s — **valid** |
| `goopg-sf1-plain-20260729.txt` | SF=1 **plain** run, fresh server, 1800 s | `rc=124` at 1974 s — **valid** |
| `goopg-sf1-explain-head-bd8c484d.txt` | plain `EXPLAIN` at SF=1, HEAD | valid — **byte-identical** to the 05:36 capture |
| `sweep-20260729-051827.txt` | solo SF0.5 sweep, `TIMEOUT_SEC=900` | `TIMEOUT` 921 s — **void** |
| `goopg-sf1-explain-analyze.txt` | SF=1 `EXPLAIN (ANALYZE, TIMING OFF)`, 1800 s | `rc=124` at 1846 s — **void** |
| `goopg-sf05-explain.txt` | plain `EXPLAIN` at SF0.5 | valid — plan shape |
| `goopg-sf1-explain.txt` | plain `EXPLAIN` at SF=1 | valid — identical shape to SF0.5 |

The HEAD `EXPLAIN` matching the 05:36 capture means neither `beb7af82` (set-op)
nor `c26c6fc3` (M0125-0003 stage 1, relation width) flipped Q35's shape —
stage 1 is shape-neutral here, as designed.

## Why the count is not recoverable by a bigger budget

900 s → 1800 s → 3600 s all sit ~3 orders of magnitude below the floor. The
count becomes obtainable only when the per-row re-scan does, i.e. when
**M0125-0003** decorrelates the RC-8 shape (`$0 = ss_customer_sk` lands as a
nested-loop **Filter**, so each of three `EXISTS` re-scans a whole fact table per
outer row). Q35 is the natural acceptance query for that item: the first run
that terminates should be checked against the git-tracked oracle `35|OK|100|0`.

**RC-8's "measure first" is discharged for Q35** without a completing
`EXPLAIN ANALYZE`: the two counters it wanted are `Calls` = the outer
cardinality **96,562** (every outer row calls every SubPlan; the OR pair
short-circuits, the AND-ed one does not) and a per-call cost of **8.16 s**.

Full analysis and resume point:
`docs/design/0124-0004-q35-rowcount-resolution.md`
§"Execution record (2026-07-30)".
