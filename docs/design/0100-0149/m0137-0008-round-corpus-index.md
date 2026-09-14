# M0137-0008 — INDEX-by-query.md / INDEX-by-mechanism.md over the round corpus

Status: accepted
Date: 2026-09-15
Milestone: M0137 — Parity measurement harness and instrument repair

## Problem

`METHODOLOGY3/README.md` §7 named knowledge retrieval as the programme's fourth
pathology: "R127 was withdrawn on 9 findings, 3 fatal, every one refuted by
evidence already on disk in this directory — its author opened none of the
eight prior Q4 rounds." The standing "grep the directory before scoping"
warning predates R130 and did not work either — R130 needed three revisions,
one of which proposed a cut that had landed 642 commits earlier with a
permanent source comment headed "WHAT THIS DOES NOT BUY". `AGENT.md`'s
plan-parity harness section already gates entry into a round directory on
citing `INDEX-by-query.md` / `INDEX-by-mechanism.md` "(built by M0137-0008)" —
this task builds them.

## What landed

Two new files under `docs/design/not_ralph/plan_parity_fix_take2/`, the round
corpus's own directory (so a reader who has just opened `r0-baseline` etc. via
`ls` finds the index alongside it, and so relative round-name links need no
path prefix):

- **`INDEX-by-query.md`** — one row per query (23 TPC-H incl. Q15a/Q15b, 86
  TPC-DS) that at least one round investigated as its subject, columns
  `query | rounds (oldest→newest) | last verdict on record`. A closing
  "Corpus-wide" section lists the 19 rounds whose extraction found no
  single-query subject (pure census/instrument/design rounds).
- **`INDEX-by-mechanism.md`** — one section per mechanism/category tag (the
  same nine-category vocabulary `METHODOLOGY3`'s per-query census already uses
  — join-order, join-method, aggregation-strategy, sort-strategy, scan-type,
  parameterisation, qual-placement, rendering, parallelism — plus four
  cross-cutting tags this index adds: costing/width, statistics/ANALYZE,
  correctness-bug, instrument/harness, and a residual `other`), each a table
  of `round | title | verdict`. Section sizes range from 6 rounds
  (parameterisation) to 59 (costing/width) and 46 (instrument/harness) — the
  two axes the process retrospective already named as the programme's
  dominant residual and its dominant time-sink.

Both files carry a header naming their scope (all 126 `rN-*` directories under
this directory, `r0-baseline` .. `r130-q9-joinorder-remeasure`; the missing
numbers r18/r23/r24/r80/r114/r129 were never allocated as round directories,
not a gap in the extraction) and pointing at `METHODOLOGY3/01-what-we-learned.md`
/ `02-open-problems.md` for narrative context this index deliberately omits —
it answers "which rounds touched query/mechanism X", not "what is currently
true about X".

## How it was built

Reading and hand-tabulating 126 round reports serially does not fit one
loop's context budget, and re-deriving the summary from scratch is exactly
the retrieval failure this task exists to fix. Six parallel Explore
(read-only) subagents each covered a contiguous ~20-round slice
(`r0`..`r26`, `r27`..`r46`, `r47`..`r66`, `r67`..`r91`, `r92`..`r109`,
`r110`..`r130`), reading each round's `REPORT.md` (falling back to
`DESIGN.md` or any `*.md` present; several early/late rounds have no report
doc at all and are marked `(no report doc)`) and returning a structured
extraction: title, TPC-H queries touched, TPC-DS queries touched, mechanism
tags, one-line verdict — explicitly *not* reading the large `*.plans.txt` /
`*.diff.txt` data dumps, which would have blown the context budget for no
retrieval value. The six extractions were concatenated and run through a
small Python script (`/tmp/build_index.py`, not committed — a one-shot
build tool, not a maintained generator) that parses the pipe-delimited rows,
extracts `Qn`/`Qna` tokens via regex, and emits the two index files by
regrouping the same 126-row dataset along the query axis and the mechanism
axis. One data-quality fix was needed after the first pass: three source
rows (`r54`, `r59`, `r82`/`r86`/`r91`/`r97`) had a TPC-DS control query
(e.g. `Q84`, `Q41`) folded into the free-text TPC-H cell alongside a
`(control)`/`(controls)` qualifier; since TPC-H has exactly 22 base queries,
the TPC-H extraction filters to `Qn` with `n <= 22`, which removes the
leakage without hand-editing the six agents' raw output.

## What did not change

No production planner/executor/catalog code — this is a documentation/index
task, the third of its kind in M0137 after M0137-0001 (capture pipeline) and
M0137-0005 (plan-gate baseline). No ledger row: the index is retrieval
tooling over already-written round reports, not a discovered PG-incompatibility.
The 126 round directories and `METHODOLOGY3/` are left untouched (frozen
history, same rule M0137-0004 applied to `METHODOLOGY3/`). The two index
files are hand-copied outputs of the one-shot script, not wired into any
`make regen-*` target — there is no ongoing round-directory production (per
the harness's "Do not create `rNNN-*` directories" rule), so there is nothing
for a regenerator to stay in sync with; a future M0137–M0143 task that adds
new `analysis/m01NN/` artefacts does not feed this index and was never asked
to.

## Verification

- Both files are pure markdown tables; spot-checked five queries
  (`INDEX-by-query.md` Q4, Q9, Q41, Q96, Q12) and two mechanism sections
  (`join-order`, `costing/width`) against the fix_plan's own M0137-0007..0009
  prose and against `METHODOLOGY3/README.md`'s cited rounds (R71/R78/R81/R127
  for Q4's "rows theory DEAD" chain; R130 for Q9's ground-truth inversion) —
  all match.
- Row counts: 126 rounds in, 126 rounds represented in each mechanism section
  total (`sum` of per-section counts exceeds 126 because a round can carry
  multiple tags, expected). TPC-H query coverage 23/23 possible tokens
  (Q1-Q22 plus the Q15a/Q15b split used by some captures); TPC-DS 86/99
  (the remaining 13 TPC-DS queries were never the stated subject of any
  round — consistent with `METHODOLOGY3`'s own count of ~50 smaller/untouched
  items in `02-open-problems.md`).
- No `go build`/`go test` gate applies (no Go source touched). `make
  ralph-state-guard` run before the status block per the loop's standard
  discipline.
