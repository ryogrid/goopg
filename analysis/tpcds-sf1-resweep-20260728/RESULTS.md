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

Running timeout classification (D6), Q1–Q40:

- **both engines** (excluded from "goopg-only"): Q4
- **goopg-only, runtime unbounded above**: Q5, Q10, Q14, Q30, Q31
- **goopg-only, budget-marginal** (true runtime ≈ budget; verdict is a coin flip
  at 600 s): Q18, Q35
- **PG-only** (goopg wins): Q11
- **goopg ERROR**: Q8
- **not a goopg error** (query text invalid on PG too): Q36

## Cursor

`M0124-0001 sweep: 1-40 done; next 41-48.`
