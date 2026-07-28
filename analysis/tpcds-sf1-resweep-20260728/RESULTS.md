# TPC-DS SF=1 dual-engine re-sweep at HEAD — running results

Protocol: `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md` (M0124-0001).
This file accumulates chunk results; the merged deliverable
`analysis/tpcds-sf1-goopg-20260728.md` (confirm/refute for the 13 §13.3
projections) is written once the sweep reaches Q99.

## Provenance (fixed for the whole sweep)

| field | value |
|---|---|
| engine-id | `bba744a817f7ebdec31fd47edfed40362641dd0c c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc` |
| goopg commit at chunk 1 | `6d6bd1ea` (docs/harness only; engine trees as above) |
| engine binary | `e6774c4f5e22f2fa` (Q1–Q4 up to the Q4 restart), `8f0aac15ce9d2aa9` after — **same source**, see below |
| budget | `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1` |
| goopg | 65436, `-U postgres -d postgres`, `bench/tpcds/runtime_goopg/data` |
| PG 18.3 | 65438, `-U ryo -d tpcds`, `bench/tpcds/runtime/pgdata` |
| GC regime | `GOGC=off`, `GOMEMLIMIT=12GiB` (per `env_tpcds.sh`) |
| state | **S-cold** — `s-cold-proof.txt` (8 relations `reltuples=0 relpages=0`, `pg_stats`=0, `store_sales`=2 880 404) |

**The `*** SWEEP VOID ***` line in `chunk-1-4.txt` is a false positive of the
first-cut provenance guard and does not void the chunk.** The guard compared the
binary's sha256, and `go build` stamps `vcs.revision`/`vcs.time`/`vcs.modified`
into the image, so the docs commit `6d6bd1ea` alone moved it
`e6774c4f → 8f0aac15`. The engine source is identical across the two images:
`git diff --stat 6d6bd1ea^ 6d6bd1ea -- internal cmd` is empty,
`git status --porcelain -- internal cmd` is empty, and `go build -buildvcs=false`
yields one image (`33e7d081…`) for both. The guard now keys on `engine-id`
(committed engine trees + digest of uncommitted engine edits), which is what
`chunk-5-8.txt` prints; see design doc D4a.

## Results

Budget 600 s. "set A" = `analysis/tpcds-sf1-goopg-20260727.md` §5.2, same SF and
**same 600 s budget** — comparable under D2.

| Q | goopg | rows | PG | rows | set A goopg | verdict |
|---|---|---|---|---|---|---|
| 1 | OK 246 s | 100 | OK 206 s | 100 | OK 250 s / 100 | rows = PG; stable |
| 2 | OK 27 s | 2513 | OK 1 s | 2513 | OK 28 s / 2513 | rows = PG; stable |
| 3 | OK 15 s | 31 | OK 0 s | 31 | OK 18 s / 31 | rows = PG; stable |
| 4 | TIMEOUT 622 s | 0 | TIMEOUT 616 s | 0 | TIMEOUT 644 s | times out on **both** engines — excluded from "goopg-only" (D6) |
| 5 | TIMEOUT 621 s | 0 | OK 1 s | 100 | TIMEOUT 649 s | goopg-only timeout; unchanged |
| 6 | OK 57 s | 44 | OK 140 s | 44 | OK 59 s / 44 | rows = PG; goopg 2.5× faster than PG here |
| 7 | OK 64 s | 100 | OK 2 s | 100 | OK 65 s / 100 | rows = PG; stable |
| 8 | **ERROR 26 s** | 0 | OK 0 s | 0 | ERROR 24 s | `ERROR: column ref ca_zip/57 out of MaterializedSlot range 1`; **server survives** (verified `select 1` after) — confirms the §13.3 projection "ERROR, contained, not fixed" |
| 9 | OK 143 s | 1 | OK 2 s | 1 | OK 146 s / 1 | rows = PG; stable |
| 10 | TIMEOUT 624 s | 0 | OK 11 s | 1 | TIMEOUT 649 s | goopg-only timeout; unchanged |
| 11 | OK 79 s | 95 | **TIMEOUT 623 s** | 0 | OK 77 s / 95 | **PG-only timeout** — goopg completes what PG cannot at this budget; unchanged from set A |
| 12 | OK 6 s | 100 | OK 0 s | 100 | OK 6 s / 100 | rows = PG; stable |
| 13 | OK 57 s | 1 | OK 0 s | 1 | OK 62 s / 1 | rows = PG; stable |
| 14 | TIMEOUT 625 s | 0 | OK 37 s | 200 [100+100] | TIMEOUT 647 s | goopg-only timeout; unchanged (PG row count is the summed two-block value — harness fix 2) |
| 15 | OK 17 s | 100 | OK 0 s | 100 | OK 15 s / 100 | rows = PG; stable |
| 16 | OK 48 s | 1 | OK 1 s | 1 | OK 48 s / 1 | rows = PG; stable |
| 17 | OK 53 s | 1 | OK 2 s | 1 | OK 52 s / 1 | rows = PG; stable |
| 18 | **TIMEOUT 627 s** | 0 | OK 0 s | 100 | **OK 626 s / 100** | **budget-marginal flip** — same work as set A, opposite verdict; see below |
| 19 | OK 64 s | 100 | OK 0 s | 100 | OK 69 s / 100 | rows = PG; stable |
| 20 | OK 14 s | 100 | OK 0 s | 100 | OK 17 s / 100 | rows = PG; stable |
| 21 | OK 50 s | 100 | OK 2 s | 100 | OK 47 s / 100 | rows = PG; stable |
| 22 | OK 156 s | 100 | OK 5 s | 100 | OK 162 s / 100 | rows = PG; stable |
| 23 | OK 210 s | 1 [1+0] | OK 6 s | 1 [1+0] | OK 208 s / 1 | rows = PG; stable |
| 24 | OK 75 s | 0 [0+0] | OK 0 s | 0 [0+0] | OK 75 s / 0 | rows = PG (both empty); stable |
| 25 | OK 53 s | 0 | OK 2 s | 0 | OK 55 s / 0 | rows = PG (both empty); stable |
| 26 | OK 34 s | 100 | OK 0 s | 100 | OK 35 s / 100 | rows = PG; stable |
| 27 | OK 234 s | 100 | OK 1 s | 100 | OK 239 s / 100 | rows = PG; stable |
| 28 | OK 88 s | 1 | OK 2 s | 1 | OK 89 s / 1 | rows = PG; stable |
| 29 | OK 55 s | 1 | OK 0 s | 1 | OK 53 s / 1 | rows = PG; stable |
| 30 | TIMEOUT 627 s | 0 | OK 13 s | 63 | TIMEOUT 649 s | goopg-only timeout; **unbounded above** — unchanged |
| 31 | TIMEOUT 629 s | 0 | OK 12 s | 43 | TIMEOUT 647 s | goopg-only timeout; **unbounded above** — unchanged |
| 32 | OK 11 s | 1 | OK 0 s | 1 | OK 10 s / 1 | rows = PG; stable |
| 33 | OK 68 s | 100 | OK 1 s | 100 | OK 69 s / 100 | rows = PG; stable |
| 34 | OK 34 s | 374 | OK 0 s | 374 | OK 35 s / 374 | rows = PG; stable |
| 35 | **TIMEOUT 628 s** | 0 | OK 1 s | 100 | TIMEOUT 651 s | goopg-only timeout, **budget-marginal** (Q18 sub-class, not unbounded) — see below |
| 36 | ERROR 0 s | 0 | SKIP | — | ERROR 0 s | dsqgen artefact — the query text fails on PG too, so it is **not** a goopg error; PG_SKIP list |
| 37 | OK 316 s | 0 | OK 0 s | 0 | OK 311 s / 0 | rows = PG (both empty); stable |
| 38 | OK 40 s | 1 | OK 3 s | 1 | OK 41 s / 1 | rows = PG; stable |
| 39 | OK 181 s | 236 [230+6] | OK 5 s | 236 [230+6] | OK 179 s / 236 | rows = PG on both blocks; stable (set A's "fixed this round" holds) |
| 40 | OK 38 s | 100 | OK 0 s | 100 | OK 42 s / 100 | rows = PG; stable |
| 41 | OK 8 s | 1 | OK 2 s | 1 | OK 8 s / 1 | rows = PG; stable |
| 42 | OK 17 s | 10 | OK 1 s | 10 | OK 15 s / 10 | rows = PG; stable |
| 43 | OK 16 s | 6 | OK 0 s | 6 | OK 18 s / 6 | rows = PG; stable |
| 44 | OK 59 s | 10 | OK 0 s | 10 | OK 58 s / 10 | rows = PG; stable |
| 45 | OK 20 s | 14 | OK 0 s | 14 | OK 18 s / 14 | rows = PG; stable |
| 46 | OK 43 s | 100 | OK 0 s | 100 | OK 43 s / 100 | rows = PG; stable |
| 47 | **OK 142 s** | **0** | OK 3 s | 100 | **OK 17 s / 0** | known RC-1b row gap (unchanged, still 0 vs 100) **but an 8.4× runtime deviation** — reproduced at 143 s; see below |
| 48 | OK 50 s | 1 | OK 0 s | 1 | OK 52 s / 1 | rows = PG; stable |
| 49 | OK 79 s | 30 | OK 1 s | 34 | OK 82 s / 30 | known Q49 row gap (30 vs 34), unchanged; runtime reproduces (−3 s) |
| 50 | OK 19 s | **6** | OK 0 s | 6 | **OK 15 s / 0** | **row gap CLOSED — 0 → 6 = PG.** The RC-1b fix landed after set A; runtime essentially unchanged (+4 s) |
| 51 | OK 587 s | 0 | OK 1 s | 100 | OK 597 s / 0 | row gap unchanged (0 vs 100); **budget-marginal, did NOT flip** (13 s headroom, −10 s vs set A) |
| 52 | OK 16 s | 100 | OK 0 s | 100 | OK 15 s / 100 | rows = PG; stable |
| 53 | OK 48 s | 100 | OK 0 s | 100 | OK 44 s / 100 | rows = PG; stable |
| 54 | TIMEOUT 632 s | 0 | OK 0 s | 0 | TIMEOUT 657 s | goopg-only timeout; unbounded above; unchanged from set A |
| 55 | OK 16 s | 73 | OK 0 s | 73 | OK 18 s / 73 | rows = PG; stable |
| 56 | OK 66 s | 100 | OK 0 s | 100 | OK 67 s / 100 | rows = PG; stable |
| 57 | OK 108 s | 100 | OK 1 s | 100 | OK 109 s / 100 | rows = PG; stable (−1 s) |
| 58 | OK 42 s | 0 | OK 0 s | 0 | OK 41 s / 0 | rows = PG (**both** engines return 0); stable |
| 59 | OK 32 s | 100 | OK 1 s | 100 | OK 33 s / 100 | rows = PG; stable |
| 60 | OK 67 s | 100 | OK 0 s | 100 | OK 67 s / 100 | rows = PG; stable (exact) |
| 61 | OK 123 s | 1 | OK 0 s | 1 | OK 120 s / 1 | rows = PG; stable (+3 s) |
| 62 | OK 9 s | 100 | OK 0 s | 100 | OK 12 s / 100 | rows = PG; stable (−3 s) |
| 63 | OK 44 s | 100 | OK 0 s | 100 | OK 43 s / 100 | rows = PG; stable |
| 64 | TIMEOUT 632 s | 0 | OK 0 s | 8 | TIMEOUT 654 s / 0 | goopg-only timeout; unbounded above; reproduces set A (−22 s, both cut at budget) |
| 65 | TIMEOUT 636 s | 0 | OK 0 s | 100 | TIMEOUT 659 s / 0 | goopg-only timeout; unchanged |
| 66 | OK 36 s | 5 | OK 1 s | 5 | OK 34 s / 5 | rows = PG; stable |
| 67 | TIMEOUT 637 s | 0 | OK 6 s | 100 | TIMEOUT 654 s / 0 | goopg-only timeout; unchanged |
| 68 | OK 44 s | 100 | OK 1 s | 100 | OK 41 s / 100 | rows = PG; stable |
| 69 | TIMEOUT 630 s | 0 | OK 1 s | 100 | TIMEOUT 658 s / 0 | goopg-only timeout; unchanged |
| 70 | ERROR 0 s | 0 | SKIP | — | ERROR 0 s / 0 | dsqgen artefact — query text invalid on PG too, so the PG arm is skipped by design; **not a goopg defect** |
| 71 | TIMEOUT 634 s | 0 | OK 0 s | 1129 | TIMEOUT 652 s / 0 | goopg-only timeout; unchanged |
| 72 | **TIMEOUT 635 s** | 0 | OK 2 s | 100 | OK 14 s / 0 | **first set-A `OK` → HEAD `TIMEOUT`**; re-probed fresh at 636 s; set A's 0-row gap now unobservable — see "Chunk 65–72" below |
| 73 | OK 34 s | 3 | OK 0 s | 3 | OK 34 s / 3 | rows = PG; stable |
| 74 | OK 34 s | 100 | **TIMEOUT 638 s** | 0 | OK 36 s / 100 | **PG-only timeout** — goopg completes what PG cannot at this budget; unchanged from set A; the reap terminated 1 orphaned PG backend |
| 75 | **ERROR 66 s** | 0 | OK 2 s | 100 | OK 47 s / 100 | **first set-A `OK` → HEAD `ERROR`**: `ERROR: division by zero` (query75.sql:67); server survives (Q76 ran next); this is the *predicted* M0125-0004 / ledger `tpcds-round2 Q75-eval-order` outcome — see "Chunk 73–80" below |
| 76 | OK 34 s | 100 | OK 0 s | 100 | OK 36 s / 100 | rows = PG; stable (RC-1b hold confirmed a second time) |
| 77 | OK 47 s | 44 | OK 0 s | 44 | OK 50 s / 44 | rows = PG; stable |
| 78 | TIMEOUT 637 s | 0 | OK 2 s | 100 | TIMEOUT 651 s / 0 | goopg-only timeout; unbounded above; reproduces set A |
| 79 | OK 34 s | 100 | OK 0 s | 100 | OK 34 s / 100 | rows = PG; stable |
| 80 | OK 164 s | 100 | OK 1 s | 100 | OK 169 s / 100 | rows = PG; stable |
| 81 | TIMEOUT 637 s | 0 | OK 59 s | 100 | TIMEOUT 653 s / 0 | goopg-only timeout; unbounded above; reproduces set A |
| 82 | OK 556 s | 2 | OK 1 s | 2 | OK 576 s / 2 | rows = PG **and values = PG**; **narrowest OK margin of the sweep** — 44 s of headroom under the 600 s budget, so its `OK` is budget-marginal |
| 83 | OK 6 s | 22 | OK 0 s | 22 | OK 7 s / 22 | rows = PG; **values differ in rendering only** — goopg prints the `*_dev` numerics as `0.0` where PG prints `0.00000000000000000000` (numeric-division result scale); numerically equal |
| 84 | OK 5 s | 18 | OK 0 s | 18 | OK 5 s / 18 | rows = PG; byte-identical output |
| 85 | OK 9 s | 2 | OK 1 s | 2 | OK 7 s / 2 | rows = PG; byte-identical output |
| 86 | ERROR 0 s | 0 | SKIP | — | ERROR 0 s / 0 | **not a goopg error** — dsqgen artefact (`syntax error … expected ')' after SELECT`), fails on PG too; joins Q36/Q70 |
| 87 | **OK 35 s** | 1 | OK 2 s | 1 | OK 36 s / 1 | **row count matches but the ANSWER IS WRONG**: goopg `47218` vs PG `47049`. Root-caused this loop to a parser set-op associativity defect — see "Chunk 81–88" below |
| 88 | TIMEOUT 638 s | 0 | OK 1 s | 1 | TIMEOUT 660 s / 0 | goopg-only timeout; unbounded above; reproduces set A |
| 89 | OK 43 s | 100 | OK 1 s | 100 | OK 41 s / 100 | rows = PG; values match after whitespace normalisation |
| 90 | OK 19 s | 1 | OK 1 s | 1 | OK 19 s / 1 | rows = PG; byte-identical output |
| 91 | OK 5 s | 1 | OK 0 s | 1 | OK 5 s / 1 | rows = PG; byte-identical output |
| 92 | OK 5 s | 1 | OK 0 s | 1 | OK 4 s / 1 | rows = PG; byte-identical output |
| 93 | OK 31 s | 0 | OK 0 s | 0 | OK 31 s / 0 | rows = PG; byte-identical output (empty) |
| 94 | **OK 23 s** | 1 | OK 1 s | 1 | OK 24 s / 1 | **row count matches, ANSWER WRONG**: goopg `0 / NULL / NULL` vs PG `9 / 18130.71 / -9444.12`. **Two independent defects** — see "Chunk 89–96" below |
| 95 | **OK 62 s** | 1 | OK 32 s | 1 | OK 60 s / 1 | **row count matches, ANSWER WRONG**: goopg `0 / NULL / NULL` vs PG `57 / 85887.62 / -27169.36`; unpadded-date-literal defect (same as Q94's first) |
| 96 | OK 29 s | 1 | OK 0 s | 1 | OK 31 s / 1 | rows = PG; byte-identical output |

Chunk 1–8 reproduces set A on every cell, so nothing between the two sweeps
changed Q1–Q8 behaviour. `reap_pg_orphans` was **not** idle: PG's Q4 timeout left
one backend running and the reap terminated it (`chunk-1-4.txt`), i.e. every
later PG timing in this sweep is the first that is not contaminated by it.

Chunk 9–16 (`chunk-9-12.txt`, `chunk-13-16.txt`) likewise reproduces set A on
every cell — status, row count and elapsed time all within run-to-run noise
(largest delta 25 s, on the two 600 s-budget timeouts where the excess is
teardown, not query, time). The reap fired once more, on PG's Q11 timeout.
The range was split into two harness invocations (9–12, 13–16) purely to stay
inside the loop's foreground Bash budget; both print the same `engine-id`
baseline, so they are one continuous sweep under D4a.

Chunk 17–24 (`chunk-17-24.txt`) reproduces set A on seven of eight cells (largest
delta 6 s, both directions). The eighth, **Q18, is the one predicted flip**: set A
recorded `OK 626 s / 100`, this sweep records `TIMEOUT 627 s / 0`. The two
elapsed figures are one second apart, so the query did the *same* amount of work
in both runs — what differs is only which side of the 600 s cut it landed on.
Elapsed here is wall time for the whole cell (query + the ≤30 s EXPLAIN capture,
which is outside the timeout-guarded query), which is why a cell can read
`OK` at an elapsed figure above the budget at all.

This makes Q18 a **budget-marginal** query, and it must not be merged into the
D6 goopg-only timeout class without that qualifier. The distinction is
load-bearing for M0125: Q5, Q10 and Q14 were cut at 600 s with their true
runtime *unbounded above* (no run of this sweep or set A has ever seen them
finish), whereas Q18's true runtime is **known** to sit within ~1 % of the
budget. A change that moves Q18 across the line therefore says nothing about
whether goopg got faster — only that it re-rolled a coin — while the same
movement on Q5/Q10/Q14 would be real signal. Any future re-sweep that reports
"Q18 fixed" or "Q18 regressed" at a 600 s budget is reporting noise; raising the
budget for Q18 specifically (or classifying it by measured runtime rather than by
verdict) is the only way to make that cell informative.

The reap did not fire in this range (no PG timeouts). The goopg restart after
Q18's timeout again moved the binary image hash (`01bb0f65…` → `22110d95…`) with
`engine-id` unmoved — the same `vcs.revision` stamp effect documented above for
chunks 5–8 and 9–12, not a source change.

Chunk 25–32 was run as two harness calls (`chunk-25-28.txt`, `chunk-29-32.txt`)
because set A shows two goopg timeouts in the range and the split keeps each call
inside one foreground budget; both reprint the sweep-baseline `engine-id`
unchanged, so D4a still holds and this is one sweep.

Every one of the eight cells reproduces set A — largest delta 5 s among the six
OK cells (Q27, 239 → 234 s), and the two timeouts land on the same verdict with
the usual sub-budget excess. **Q30 and Q31 are the cleanest members of the D6
goopg-only class so far**: PG answers both cheaply and exactly (13 s / 63 rows,
12 s / 43 rows), goopg has never completed either in any run of either sweep, and
the pair has now been observed at 649/647 s (set A) and 627/629 s (here) — all
four figures are the harness cutting an execution that was still running, so the
true runtime is **unbounded above** in the Q5/Q10/Q14 sense, not budget-marginal
like Q18. Any future change that lands Q30 or Q31 under the budget is therefore
real signal, and unlike Q18 the cell carries a PG row count to validate against.

The reap did not fire in this range (no PG timeouts). Both goopg restarts — after
Q30 and after Q31 — reported the same post-restart image (`46632999aa3f5c75`),
which is the expected steady state: the `vcs.revision` stamp moves once when the
commit under which the binary was built changes, not on every restart.

Chunk 33–40 (`chunk-33-40.txt`) needed no split — set A shows a single goopg
timeout in the range — and reproduces set A on all eight cells. Largest delta
among the six OK cells is 5 s (Q37, 311 → 316 s); every completed cell matches
PG's row count exactly, including Q39's two-block 236 [230+6].

**Q35 is the second member of the budget-marginal sub-class** (with Q18), not a
new unbounded-above timeout. Both sweeps cut it at the budget (651 s set A,
628 s here), but the 2026-07-26 baseline **completed** it at `OK 525 s` — set A
already flagged it as a "borderline flip" for exactly that reason. So its true
runtime sits just under-to-just-over 600 s and the verdict is a coin flip: an
`OK` in a later chunk would be a re-rolled coin, **not** evidence of a fix. The
distinction matters for M0125, which must not count a Q35 flip as a win. Unlike
Q18, Q35 does carry a usable PG row count (100), so a genuine fix can still be
validated on rows.

**Q36 is not a goopg defect.** goopg reports `ERROR 0 s` and the harness `SKIP`s
PG via `PG_SKIP="36 70 86"` because the dsqgen-generated query text is malformed
and PostgreSQL rejects it too. It is excluded from the D6 goopg-ERROR class (that
class holds Q8 alone so far).

The reap did not fire in this range (no PG timeouts). The single goopg restart —
after Q35 — moved the binary image from `46632999aa3f5c75` to `9a6a5c070ad7364d`
with `engine-id` unmoved, which is the **third** live confirmation of the
harness's provenance rule: the chunk-4 commit (`3798b57e`, docs/tracker only)
landed between chunks, so the rebuild re-stamped `vcs.revision` even though
`git diff HEAD -- internal cmd` is empty. Had the binary sha been the
comparability key, this chunk would have falsely read "SWEEP VOID".

Chunk 41–48 (`chunk-41-48.txt`) needed no split — set A shows no timeout in the
range — and ran in ~6 min, exit 0, header reprinting the sweep baseline
`engine-id` unchanged (still ONE sweep under D4a). No timeout, no restart, no
reap. Seven of eight cells reproduce set A within ±2 s and match PG's row count
exactly. The eighth is Q47.

**Q47 is the first cell in Q1–Q48 to deviate from set A on RUNTIME rather than
on a timeout verdict: `OK 17 s` → `OK 142 s`, an 8.4× slowdown.** It is not
noise and not sweep-tail collapse:

- It **reproduces**: an immediate goopg-only re-run (`diag-q47-rerun.txt`) gave
  `OK 143 s`, 1 s from the sweep cell.
- It is **query-specific**, so it cannot be server age / GC thrash under
  `GOGC=off`: the same server, at the same age, in the same chunk, returned
  Q44 59 s (set A 58 s), Q46 43 s (43 s) and Q48 50 s (52 s). A GC-pressure
  effect would have inflated those too.
- The **row count did not move**: still 0 vs PG's 100. The known RC-1b gap is
  intact — this is a runtime deviation *on top of* an unfixed wrong answer, not
  the cost of a newly-correct plan.

Cause is bounded but **not** established (deliberately not root-caused here —
this task is the measurement baseline). Set A's stated base is `ee86594e` with
commits `b3493a6e`, `21301982`, `9740fce9`; ten further engine commits have
landed since, and the prime suspect is `5db0a067`
*"fix(planner): RC-1b — push MHJ single-source filters AFTER the bindings
remap"* (2026-07-27), which post-dates set A and touches **Q47's own RC-1b
family**. A filter pushed to a different point changes what the MHJ probes, so
an 8× cost move with an unchanged wrong answer is the shape that commit could
produce. `cecdab97` (SELECT DISTINCT ORDER BY, 2026-07-28) is a second, weaker
candidate.

**Action for M0125, filed not worked:** the other RC-1b members — Q49, Q50 and
the probable member Q51 — all fall in the next chunk (49–56) and all carry set A
runtimes (82 s, 15 s, 597 s). If they show the same runtime inflation, the cause
is the RC-1b commit and not Q47; if they do not, Q47 needs its own EXPLAIN diff
against set A. Q51 matters most: it completed at `OK 597 s` in set A, i.e. it is
budget-marginal already, so any inflation at all flips it to TIMEOUT and it must
**not** be scored as a new regression class without this context.

### Chunk 49–56 — the Q47 question, answered

Chunk 49–56 (`chunk-49-56.txt`) ran ~29 min, exit 0, header reprinting the sweep
baseline `engine-id` unchanged (still ONE sweep under D4a). One goopg-only
timeout (Q54, as set A predicted) triggered the scripted restart; no reap. Seven
of eight cells reproduce set A within ±4 s.

**The prediction above was answered, but not the way it was framed.** The
decision rule was "if Q49/Q50/Q51 inflate too, the cause is `5db0a067`; if not,
Q47 needs its own EXPLAIN diff." They did **not** inflate — Q49 79 s (set A
82 s), Q50 19 s (15 s), Q51 587 s (597 s). Yet the correct conclusion is *not*
that `5db0a067` is exonerated, because the same chunk produced the decisive
evidence in a different cell:

**Q50's row gap closed: 0 → 6 = PG**, with its runtime unmoved. Set A recorded
Q50 as an RC-1b wrong answer with the fix *designed but deferred*
(`analysis/tpcds-sf1-goopg-20260727.md` §2.1). It is now correct. So `5db0a067`
did land, and it did change plans in the RC-1b family — the effect is simply not
uniform across the family, which is why Q49/Q51 look untouched.

That resolves Q47 by cross-reference, and **the ledger already said so**:
`.ralph/deferral_ledger.md` (row `tpcds-round2 RC-1b`, 2026-07-27) records the
fix as landed and verified, with residual (a) reading *"Q47 full query still 0 vs
100 — its CTE body is now correct (661185), so a SECOND defect sits downstream in
the windowed self-join layers, which until now never received real input
(14s->143s confirms real work)"*. This sweep's 17 s → 142 s is that same
14 s → 143 s, measured independently at SF=1.

**Therefore Q47 is NOT a regression and the runtime-deviation class opened in
chunk 41–48 should be closed.** Chunk 41–48's reasoning — *"the row count did not
move … so this is a runtime deviation on top of an unfixed wrong answer, not the
cost of a newly-correct plan"* — was wrong on its second clause. The final row
count stayed 0 for a **different, downstream** reason; Q47's CTE body went from
0 rows to 661185, so the windowed self-join layers above it did real work for the
first time. An 8.4× runtime increase is the *expected* cost of that, not a
performance regression. Q47 remains a wrong answer (0 vs 100) tracked as RC-1b
residual (a); it is not a new finding of this sweep.

Two cells worth recording precisely:

- **Q51 did not flip.** It was flagged as the chunk's main flip risk at set A's
  `OK 597 s`; it came in at `OK 587 s`, i.e. 13 s of headroom, still
  budget-marginal and still the wrong answer (0 vs 100). Its RC-1b family
  membership stays *probable, unproven* — the fix that corrected Q50 did not
  move Q51's rows.
- **Q54** reproduces set A's goopg-only timeout (632 s vs 657 s, both cut at the
  budget). PG answers it in 0 s with 0 rows, so the row count is not evidence
  either way; classified unbounded above.

Running timeout classification (D6), Q1–Q56 (49–56 contributed one goopg-only
timeout, Q54 — already present in set A — and no new ERROR):

- **both engines** (excluded from "goopg-only"): Q4
- **goopg-only, runtime unbounded above**: Q5, Q10, Q14, Q30, Q31, Q54
- **goopg-only, budget-marginal** (true runtime ≈ budget; verdict is a coin flip
  at 600 s): Q18, Q35, Q51 (completes at 587 s — marginal but on the OK side in
  both sweeps)
- **PG-only** (goopg wins): Q11
- **goopg ERROR**: Q8
- **not a goopg error** (query text invalid on PG too): Q36

The **runtime-deviation** class opened in chunk 41–48 is **CLOSED, empty**. Its
sole member Q47 was reclassified by chunk 49–56: the 8.4× is the cost of the
landed RC-1b fix feeding real input (661185 rows, was 0) into Q47's windowed
self-join layers, not a performance regression. Q47 stays tracked as an RC-1b
**wrong answer** (0 vs 100, residual (a) in the ledger), which is a pre-existing
row-mismatch, not a finding of this sweep.

Row mismatches vs PG among OK queries, Q1–Q56: Q47 (0/100), Q49 (30/34), Q51
(0/100). Set A's list over the same range was Q47, Q49, **Q50**, Q51 — Q50 has
since been fixed.

### Chunk 57–64 — the first fully uneventful chunk of the sweep

Chunk 57–64 (`chunk-57-64.txt`) ran ~18 min, exit 0, header reprinting the sweep
baseline `engine-id` unchanged (still ONE sweep under D4a). One goopg-only
timeout (Q64) triggered the scripted restart; no reap was needed.

This is the **first chunk in the whole re-sweep that produced no new finding at
all**, and that is itself the result worth recording:

- **All seven OK queries match PG's row count exactly** (Q57 100, Q58 0, Q59 100,
  Q60 100, Q61 1, Q62 100, Q63 100). No new row mismatch enters the sweep-wide
  list, which still stands at Q47/Q49/Q51 for Q1–Q64.
- **Every OK runtime reproduces set A within ±3 s** (max deviation Q61 +3 s and
  Q62 −3 s, both well inside run-to-run noise; Q60 is exact at 67 s). Nothing
  here resembles the Q47 8.4× signal that opened and then closed the
  runtime-deviation class — that class stays **empty**.
- **Q58's 0 rows is not a defect.** Both engines return 0 at SF=1, i.e. the
  query's date/item predicates simply select nothing from this dataset. It is
  scored `rows = PG`, not a row gap; the pattern to avoid is reading a bare
  "goopg returned 0" as a wrong answer without checking PG's own count (the
  mistake that mis-framed Q47 in chunk 41–48).
- **Q64 reproduces set A's goopg-only timeout** (632 s vs 654 s, both cut at the
  600 s budget). Unlike Q54, PG *does* return a non-empty answer here (8 rows in
  0 s), so goopg's 0 is uninformative about correctness — the query never
  produced output before the cut. Classified **unbounded above**; whether Q64
  also has a row gap cannot be decided until it completes under a larger budget.

Running timeout classification (D6), Q1–Q64 (57–64 contributed one goopg-only
timeout, Q64 — already present in set A — and no new ERROR):

- **both engines** (excluded from "goopg-only"): Q4
- **goopg-only, runtime unbounded above**: Q5, Q10, Q14, Q30, Q31, Q54, **Q64**
- **goopg-only, budget-marginal** (true runtime ≈ budget; verdict is a coin flip
  at 600 s): Q18, Q35, Q51
- **PG-only** (goopg wins): Q11
- **goopg ERROR**: Q8
- **not a goopg error** (query text invalid on PG too): Q36

Row mismatches vs PG among OK queries, Q1–Q64: unchanged at Q47 (0/100), Q49
(30/34), Q51 (0/100).

### Chunk 65–72 — Q72 crosses the budget: the RC-1b fix's third outcome

Chunk 65–72 (`chunk-65-72.txt`) ran ~45 min, exit 0, header reprinting the sweep
baseline `engine-id` unchanged (still ONE sweep under D4a). Five goopg-only
timeouts, each triggering the scripted restart; no reap was needed (no PG arm
timed out).

Seven of the eight cells reproduce set A exactly in verdict:

- **Q65, Q67, Q69, Q71** reproduce set A's goopg-only TIMEOUTs (636/637/630/634 s
  vs 659/654/658/652 s, all cut at the 600 s budget). PG answers all four in
  ≤ 6 s (100, 100, 100, 1129 rows), so as with Q64 goopg's 0 is uninformative
  about correctness — these are **unbounded above and unvalidatable**.
- **Q66 (36 s / 5) and Q68 (44 s / 100)** match PG's row count exactly and
  reproduce set A within ±3 s (34 s, 41 s). These two matter methodologically:
  each ran immediately after a timeout-and-restart, so they demonstrate that the
  `RESTART_AFTER_TIMEOUT=1` guard is working in this chunk and that server age is
  **not** confounding its timings.
- **Q70** is `ERROR` on goopg with the PG arm skipped by design (`PG skip: 36 70
  86`). This reproduces set A and is **not a goopg defect** — the dsqgen-generated
  query text fails on PG too.

**Q72 is the finding, and it is the first cell in the whole re-sweep where a set-A
`OK` becomes a `TIMEOUT`** — a D6 *class* change, not merely a runtime deviation
like Q47:

| | set A (2026-07-27) | this re-sweep | re-probe, fresh server |
| --- | --- | --- | --- |
| goopg Q72 | OK 14 s / **0** rows | **TIMEOUT 635 s** / 0 | **TIMEOUT 636 s** / 0 |
| PG Q72 | OK 2 s / 100 | OK 2 s / 100 | — |

The re-probe (`probe-q72-reprobe.txt`, `ENGINES=goopg`, same 600 s budget) was run
on the server the harness had just restarted after Q72's own timeout, i.e. a
**fresh** server, and reproduced within 1 s. Combined with Q66/Q68 above this
rules out the sweep-tail-collapse / server-age confound that set A's §2 warns
about — the one that once produced a false Q6/Q7 regression. Q72 is genuinely
slower at HEAD than in set A.

**This is almost certainly the RC-1b fix, not a new regression.** Set A §2.1
names Q72 as a *probable* RC-1b member ("MHJ filter push-down uses two coordinate
spaces → Q47, Q50 and probably Q72") while explicitly holding the fix back; the
chunk 49–56 ledger row then established that `5db0a067` "push MHJ single-source
filters AFTER the bindings remap" landed *after* set A. That single fix has now
produced **three different outcomes across its family**:

| query | set A | HEAD | reading |
| --- | --- | --- | --- |
| Q50 | 0 rows (wrong) | **6 = PG** | fixed outright |
| Q47 | OK 17 s / 0 | OK 142 s / 0 | newly-correct input, still wrong downstream |
| Q72 | OK 14 s / 0 | **TIMEOUT** / 0 | newly-correct input exceeds the budget |

The captured plan (`goopg_q72_explain.txt`) is consistent with this: the bottom
node is a **4-table Multi-Way Hash Join over `warehouse`, `item`, `inventory`,
`catalog_sales` carrying no `Filter` at that node**, with the surviving
predicates sitting on the outer nested loops. At SF=1 `inventory` is ~11.7 M rows
against `catalog_sales` ~1.4 M, so an MHJ that no longer prunes early is expected
to grind. Set A's fast 14 s / 0 rows was the *wrong* answer arriving quickly
because a misplaced filter pruned everything; correcting the placement bought
correctness of input at the cost of the budget. **This is a hypothesis consistent
with all evidence gathered, not an established root cause** — confirming it needs
a plan diff against set A's Q72 plan, which is deferred past Q99 by the
sweep-integrity invariant.

Consequence for bookkeeping: **set A's recorded Q72 row gap (0 vs PG 100) is no
longer observable at HEAD.** Q72 joins Q64 in the "unbounded AND unvalidatable"
bucket that D6 cannot express — but it arrives there from the opposite direction,
by regressing out of the OK class rather than by always having been a timeout.

Running timeout classification (D6), Q1–Q72 (65–72 contributed five goopg-only
timeouts, four of them already in set A plus the new Q72, and no new ERROR):

- **both engines** (excluded from "goopg-only"): Q4
- **goopg-only, runtime unbounded above**: Q5, Q10, Q14, Q30, Q31, Q54, Q64,
  **Q65, Q67, Q69, Q71, Q72**
- **goopg-only, budget-marginal** (true runtime ≈ budget; verdict is a coin flip
  at 600 s): Q18, Q35, Q51
- **PG-only** (goopg wins): Q11
- **goopg ERROR**: Q8
- **not a goopg error** (query text invalid on PG too): Q36, **Q70**

Row mismatches vs PG among OK queries, Q1–Q72: unchanged at Q47 (0/100), Q49
(30/34), Q51 (0/100) — Q72's former gap is now masked by its timeout.

### Chunk 73–80 — Q75 errors out, exactly as predicted; RC-1b's fourth outcome

Chunk 73–80 (`chunk-73-80.txt`) ran ~35 min, exit 0, header reprinting the sweep
baseline `engine-id bba744a8… c47d4ed6… diff=e3b0c44298fc` unchanged, so D4a
still holds and this remains ONE sweep.

**Bookkeeping repair first:** the chunk-9 loop wrote the "Chunk 65–72" prose but
never appended its eight rows to the Results table above, which stopped at Q64.
Rows 65–72 are backfilled in this loop from `chunk-65-72.txt`; no figure changed,
the table simply now matches the prose.

Seven of eight cells reproduce set A in verdict. Q73/Q76/Q77/Q79/Q80 all match PG
on rows and sit within ±5 s of set A; **Q78** reproduces its set-A goopg-only
TIMEOUT (637 s vs 651 s, both cut at budget) and stays unbounded above; **Q74**
reproduces its **PG-side** TIMEOUT (638 s vs 652 s) while goopg answers in 34 s
with PG's row count — only the second PG-only timeout in the sweep after Q11, and
the first place in this range where `reap_pg_orphans` fired (1 backend
terminated), so no later PG timing in the chunk is contaminated by it.

The eighth cell is the finding: **goopg Q75 goes `OK 47 s / 100` → `ERROR 66 s`,
`ERROR: division by zero` at `query75.sql:67`.** The server survives — Q76 ran
immediately afterwards and completed normally — so this is the contained-error
shape already seen at Q8, not a crash.

This is **not a new discovery, and that is the point.** Ledger row
`tpcds-round2 Q75-eval-order` (2026-07-27) already recorded this error as
deterministic 3/3 on the SF0.5 stack, and it is filed as **M0125-0004** with a
diagnosis: with RC-1b's now-correct `all_sales` CTE a `d_year=2003` group with
`sales_cnt = 0` genuinely exists (PG has it too), and goopg evaluates the
side-mixed conjunct `CAST(curr.sales_cnt)/CAST(prev.sales_cnt) < 0.9` as the
hash-join residual per matched pair *before* the outer Filter's `d_year` equalities
can exclude that pair, where PG pushes the single-relation quals to scan level
first. What this chunk contributes is the promotion of §13.3's **projection to a
measurement**, at SF=1, inside the sweep-baseline engine — which is the entire
purpose of M0124-0001.

It also completes a fourth outcome for the RC-1b fix `5db0a067`, whose family now
reads: Q50 fixed outright (0 → 6 = PG), Q47 newly-correct input but still the
wrong answer (17 → 142 s), Q72 pushed past the budget, and Q75 pushed into a
contained error. All four share one mechanism — the input stopped being silently
zeroed — and three of the four look like regressions on the verdict column while
being strict improvements in input correctness.

Q75 deserves one further note, because the naive reading of the table is wrong.
Set A's `OK 47 s / 100` was **not** a passing cell: the same ledger row proves the
pre-fix CTE computed 1,057,469 against PG's 2,368,670, i.e. 100 garbage rows that
matched PG's row *count* and nothing else, with `LIMIT 100` hiding the corruption.
HEAD's loud `ERROR` is therefore more honest than set A's silent pass, and this
cell is the concrete justification for **M0124-0005** (a value checksum in the
SF0.5 oracle) — a row-count-only gate is structurally blind to exactly this.

Consequence for the nightly lane: `Q75,100,pinned` at
`ci/batch/tpcds-row-anchors.csv:46` has no `expected-failures.csv` entry, so this
is a live nightly break. Under the 2026-07-28 amendment M-NIGHTLY is PARKED and
the TPC-DS row-anchor gate is **not** one of the four carve-out gates
(`tpch-spotcheck.sh`, the SF0.5 gate, `make plan-diff`, the bench clusters), none
of which this touches — the sweep itself ran clean. So it stays filed and
unchecked as M0125-0004; no engine change may land before Q99 regardless.

### Chunk 81–88 — every class reproduces set A, and the first *value-level* wrong answer

All eight cells reproduce set A's class and row count (largest timing delta 22 s,
on the two 600 s-budget timeouts where the excess is teardown, not query, time).
Q81/Q88 re-time as goopg-only timeouts, Q86 re-confirms as the dsqgen artefact,
Q83/Q84/Q85/Q87 are stable `OK`s. By the row-count measure the harness records,
the chunk is uneventful.

**It is not.** Because Q75 had just proved that a matching row count can hide a
corrupt answer, this loop diffed the actual result *values* of every `OK` cell
against PG for the first time in the sweep. That caught **Q87**: 1 row on both
engines, but goopg answers `47218` where PG answers `47049`. A row-count gate —
including the current SF0.5 oracle and the nightly anchors — is blind to it.

Q87 is `count(*)` over `(A except B except C)`. The divergence was isolated to
the set operation itself, not the inputs:

| probe | goopg | PG |
|---|---|---|
| branch A / B / C cardinalities | 47428 / 31680 / 11744 | identical |
| `A except B` alone | 47117 | 47117 |
| full `A except B except C` | **47218** | 47049 |

goopg's three-way answer (47218) is *larger* than its own two-way `A except B`
(47117), which is impossible for a left-associative set difference — and it
equals PG's answer for the **right**-associated reading `A except (B except C)`
exactly. Minimal repro, confirmed on both engines this loop:

```sql
-- A={1,2,3} B={2} C={3};  PG: {1}   goopg: {1,3}
select count(*) from ((select …A) except (select …B) except (select …C)) t;
```

The trigger is **per-branch parenthesisation**, not the subquery context:

| form | goopg | PG |
|---|---|---|
| `A except B except C` (bare branches) | ✅ `{1}` | `{1}` |
| `(A) except (B) except (C)` | ❌ `{1,3}` | `{1}` |
| `(A) except all (B) except all (C)` | ❌ | — |
| `(A) union (B) except (C)` (mixed chain) | ❌ `{1,2,3}` | `{1,2}` |
| `(A) intersect (A) intersect (B)` | ✅ | — (associative, cannot expose it) |

Root cause (read this loop, **not** fixed — no engine change may land before
Q99): the parser always emits a right-linked chain and the *planner* re-associates
it left. `parseParenthesisedSelectStmt` sets `innerSel.Parenthesized = true`
(`internal/parser/select.go:1005`) **before** greedily absorbing a trailing set-op
written *outside* those parentheses (`select.go:1007-1039`). So in
`(A) except (B) except (C)` the node `B` carries both `Parenthesized == true` and
`B.SetOp = {EXCEPT, C}`. The planner's flattening loop then hits
`if rightStmt.Parenthesized { break }` (`internal/planner/planner.go:696-698`),
stops after one segment, and plans `B EXCEPT C` recursively — yielding
`A EXCEPT (B EXCEPT C)`. `Parenthesized` is overloaded: documented
(`internal/parser/ast.go:861-867`) as "the whole compound was wrapped in parens",
but set on a node that later absorbs operators that were never inside them.
UNION-only and INTERSECT-only chains are unaffected purely because those
operators are associative; EXCEPT / EXCEPT ALL and mixed chains are not.

Blast radius in TPC-DS: only `query87.sql` changes answer. `query14.sql` and
`query38.sql` also chain set operators, but both chain INTERSECT/UNION, which are
associative. Filed as **M0125-0006** with a ledger row; the fix is a parser/planner
change and is therefore blocked behind Q99 by the sweep protocol.

Two lesser value-level findings from the same diff, both PG-compat gaps that do
**not** change any answer:

- **Q83** — goopg renders the `*_dev` numerics as `0.0`, PG as
  `0.00000000000000000000`. Numerically equal; goopg's numeric-division result
  *scale* does not follow PG's `select_div_scale` (`postgres/src/backend/utils/adt/numeric.c`).
- **Q82** — values match after whitespace normalisation; the two outputs differ
  only in psql's computed column width for `i_item_desc` (goopg 1 char narrower),
  consistent with a trailing space being trimmed somewhere in the varchar path.

Running timeout/error classification (D6), Q1–Q88 (81–88 contributed two
goopg-only timeouts and one further not-a-goopg-error, and no new class):

- **both engines** (excluded from "goopg-only"): Q4
- **goopg-only, runtime unbounded above**: Q5, Q10, Q14, Q30, Q31, Q54, Q64,
  Q65, Q67, Q69, Q71, Q72, Q78, **Q81**, **Q88**
- **goopg-only, budget-marginal** (true runtime ≈ budget; verdict is a coin flip
  at 600 s): Q18, Q35, Q51, **Q82** (556 s — passed, but with 44 s of headroom)
- **PG-only** (goopg wins): Q11, Q74
- **goopg ERROR**: Q8, Q75
- **not a goopg error** (query text invalid on PG too): Q36, Q70, **Q86**

Answer mismatches vs PG among OK queries, Q1–Q88: Q47 (0/100), Q49 (30/34),
Q51 (0/100) by row count, plus **Q87 by value at a matching row count** — the
first of its kind in the sweep, and the reason value diffing is now part of the
per-chunk procedure rather than an M0124-0005 deliverable alone. Q75 does not
join them (it left the OK class entirely), but its set-A row *match* is likewise
known to have been value-corrupt.

## Chunk 89–96 — two new wrong-answer defects, and a sweep-wide value re-audit

All 8 cells are `OK` on both engines and every row count reproduces set A, so by
the pre-chunk-11 criterion this chunk was uneventful. By **value** it is the
worst chunk of the sweep: Q94 and Q95 both return `0 / NULL / NULL` against PG's
real aggregates, at a matching row count of 1.

**Defect 1 — unpadded date literals (Q16, Q94, Q95).** PG accepts single-digit
month/day fields (`'2002-5-01'`); goopg does not. Two *sibling paths disagree*,
which is what made this silent:

| form | goopg | PG |
|---|---|---|
| `cast('2002-5-01' as date)`, `date '2002-5-01'`, `'2002-5-01'::date` | **ERROR** `invalid date … parsing time "2002-5-01" as "2006-01-02"` | `2002-05-01` |
| `d_date = '2002-5-01'` (implicit coercion) | **0 rows, no error** | 1 row |
| `d_date = '2002-05-01'` (padded control) | 1 row | 1 row |
| `d_date = '2002-05-1'` (single-digit *day*) | **0 rows** | 1 row |

The comparison path silently matching nothing is worse than the cast path's
error: it converts a compat gap into a wrong answer. Root cause is a Go
fixed-layout parse (`time.Parse("2006-01-02", …)`, `internal/executor/expr.go:2874`;
sibling `internal/pgnodes/datum.go:974 parseDateFields`) where PG uses
`ParseDateTime`/`DecodeDate` (`postgres/src/backend/utils/adt/datetime.c`), which
accepts 1-or-2-digit fields. Affected TPC-DS queries are exactly those whose text
carries an unpadded literal: `query16.sql` (`'2002-4-01'`), `query94.sql`
(`'2002-5-01'`), `query95.sql` (`'2001-4-01'`). Filed **M0125-0007**.

**Defect 2 — SEMI + ANTI conjunction is not a subset (Q94).** With the dates
padded so defect 1 is out of the way, goopg *still* disagrees with PG. Each
subquery is correct **alone**; only their conjunction breaks:

| Q94 predicate set (dates padded) | goopg rows / distinct orders | PG |
|---|---|---|
| base joins only | 33 / 25 | 33 / 25 ✅ |
| base + `EXISTS (… ws_warehouse_sk <> …)` | 33 / 25 | 33 / 25 ✅ |
| base + `NOT EXISTS (web_returns …)` | 11 / 9 | 11 / 9 ✅ |
| base + **both** (full Q94) | **25 / 18** | 11 / 9 ❌ |

goopg's 25 rows are **not a subset of the 11** that `NOT EXISTS` alone yields —
adding a conjunct grew the result, so this is a hard correctness violation, not a
tie-break or ordering artefact. This is the Semi/Anti residual ↔ source-table
mapping pair named in the project's hard-won rule #2. PG control: the padded
query returns `9 | 18130.71 | -9444.12`, identical to unpadded, so padding is
semantically neutral and does not confound the isolation. Filed **M0125-0008**.

**Defect 3 — sibling `sum(CASE …)` aggregates collapse onto the first slot.**
Found by back-applying the value diff to the whole sweep (below). goopg emits the
*first* pivot column's value in every sibling pivot column:

```
Q43 goopg: able | AAAAAAAACAAAAAAA | 517884.59 | 517884.59 | 517884.59 | …(×7)
Q43 pg   : able | AAAAAAAACAAAAAAA | 517884.59 | 469230.50 | 505832.67 | …
Q50 goopg: … | 67 | 67 | 67 | 67 | 67
Q50 pg   : … | 67 | 48 | 61 | 66 | 98
```

Minimal reproducer and the controls that pin it:

| query | goopg | PG |
|---|---|---|
| `sum(case … 'Sunday' …), sum(case … 'Monday' …), sum(case … 'Tuesday' …)` | `10435\|10435\|10435` ❌ | `10435\|10436\|10436` |
| same with `GROUP BY d_year` | `53\|53` ❌ | `53\|52` |
| `sum(case … d_dom …), sum(case … d_moy …)` (different source cols) | `2400\|2400` ❌ | `2400\|6200` |
| `sum(case …), count(case …)` (different agg funcs) | ✅ | — |
| `sum(d_dom+1), sum(d_dom+2)` (arith exprs) | ✅ | — |
| `sum(d_dom), sum(case …)` (mixed shapes) | ✅ | — |

Root cause is exact and one line: aggregate dedup keys come from
`aggregateCallKey` → `parserExprKey` (`internal/planner/planner.go:6891`, `:7425`),
whose fallback is `return fmt.Sprintf("expr:%T", e)` (**`planner.go:7484`**) — the
Go *type name only*, with no expression content. Every `*parser.CaseExpr`
therefore hashes to the identical key `expr:*parser.CaseExpr`, so the second and
later `sum(CASE …)` are dropped as duplicates (`planner.go:5844-5846`) and all
sibling columns read the first aggregate's slot. The controls above are exactly
what that predicts: `BinaryOp` and `ColumnRef` have real cases in the switch, so
they discriminate; distinct agg *function names* differ earlier in
`aggregateCallKey`. **17 expression types hit that fallback** — `CaseExpr`,
`ExtractExpr`, `InExpr`, `RowExpr`, `SubqueryExpr`, `ExistsExpr`, `IntervalLit`,
`ArrayConstructorExpr`, `ArraySubqueryExpr`, `ArraySubscriptExpr`, `CollateExpr`,
`IsBoolExpr`, `GroupingCall`, `TypedStringLit`, `DefaultMarker`, `IndirectionStar`,
`PartitionRangeBoundKeyword` — so the class is broader than CASE, and the same key
feeds GROUP BY matching (see the `M0097-0003` comment at `planner.go:7443`). This
is the **third** recurrence of one failure mode: `planner.go:6905-6909` records
`count(*)` vs `count(*) FILTER (WHERE …)` collapsing for the same reason
(M0097-0032). Filed **M0125-0009**.

### Sweep-wide value re-audit (back-application of the chunk-11 procedure)

Chunks 1–10 were checked on row counts only; value diffing began at chunk 11. Q16
proved that gap is not theoretical — it was recorded `OK / 1 row` in chunk 2 while
returning `0` against PG's `45`. So the diff was back-applied to every retained
result file. Restricting to cells that are **fresh this sweep, `OK` on both
engines, and equal in row count**, and separating ordering from content
(`diff <(norm x|sort) <(norm y|sort)`), **21 cells diverge by value**:

`Q2 Q7 Q16 Q21 Q26 Q27 Q28 Q39 Q40 Q43 Q46 Q50 Q59 Q62 Q66 Q68 Q79 Q83 Q87 Q94 Q95`

None are ordering-only. Sampling classifies them into the defects above plus the
known numeric-scale gap (Q7/Q26/Q83 are `0.00` vs `0.00000000000000000000`,
answer-neutral). A full per-query attribution of the remaining cells is **not**
done here — it is a task in its own right, filed as **M0124-0006**, and it must
land before the merged deliverable, because the sweep's headline claim ("row
counts reproduce set A") is now known to be a much weaker statement than
"goopg agrees with PG".

Caveat on scope: the back-audit covers only queries whose result files are fresh
(mtime 2026-07-28). Q97–Q99 have not run in this sweep; their on-disk files are
stale and were excluded, not counted as agreeing.

Running timeout/error classification (D6), Q1–Q96 — chunk 89–96 contributed **no**
new timeout, error, or PG-skip cell, so every D6 list is unchanged from Q1–Q88.
The answer-mismatch list is not: it gains **Q94** and **Q95** by value, and the
re-audit adds the 21-cell list above.

## Cursor

`M0124-0001 sweep: 1-96 done; next 97-99 (final chunk).`
