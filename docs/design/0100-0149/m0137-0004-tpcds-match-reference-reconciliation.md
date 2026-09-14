# M0137-0004 — reconciling TPC-DS `match=2` vs `match=1`

Status: accepted (landed 2026-09-15)

## Context

`METHODOLOGY3/README.md`'s executive summary (N22, `02-open-problems.md:428`)
named an unreconciled pair of TPC-DS parity numbers: `match=2` from
`r2-instrument/capture-tpcds.sh` on goopg `:65437` against **live** PG
`:65438` (R108, R113), and `match=1` from the same script family against the
committed `bench/tpcds/plans-pg` fixture (R128). R128 additionally reported
that `match=1` reproduced against **all three** in-tree references it tried.
M0137-0003 declined to settle this and named it explicitly as this task's job
(`m0137-0003-baseline-capture-procedure.md` §"What was not done"). The task
line asks for two things: declare one canonical reference, and rule on the
`bench/tpcds/plans-pg` fixture's standing the way K9 rules on the TPC-H
sibling (`bench/tpch/plans-pg/` — stale and serial, never a parity target).

This is a recon task per the harness's "Way of working": measurement plus a
design note, no production change.

## Measurement (2026-09-15, repo-head `243443944` + this loop's WIP)

Both captures used the **current, canonical** `scripts/capture-tpcds.sh`
(M0137-0001's promoted copy, post K18-`$$`-fix, post M0137-0002 machine
stamping) — never the retired `methodology/`/`r2-instrument/` copies R108/
R113/R128 used, which the harness forbids resurrecting.

1. Started goopg TPC-DS SF0.25 (`bench/tpcds/server.sh start sf025`, port
   `:65437`, db `postgres`), live PG already up on `:65438` (db `tpcds025`).
2. `scripts/capture-tpcds.sh 65437 postgres postgres <out> <hdr>
   bench/tpcds/runtime_goopg/data-sf025` — goopg capture.
3. `scripts/capture-tpcds.sh 65438 tpcds025 ryo <out> <hdr>
   bench/tpcds/runtime/pgdata` — live-PG capture, same tool, same pinned GUCs
   (`work_mem=64MB max_parallel_workers_per_gather=4`).
4. Diffed the goopg capture against (a) the live-PG capture and (b) the
   committed `bench/tpcds/plans-pg/` fixture, with
   `scripts/pg-plan-parity-diff.py`.

Result — **both references agree: `match=2` (Q9, Q41)**, right now:

```
vs live PG :65438  : queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
vs plans-pg fixture: queries=99 match=2 shapediff=67 unparsed=0 missingnode=27 error=3 timeout=0
```

Per-query verdicts differ in exactly **two** places, and neither is a MATCH
flip — Q2 and Q75 are `SHAPE-DIFF` against live PG but `MISSING-NODE` against
the fixture (a stricter verdict, not a laxer one). Category counts move by
±1–2 in `aggregation-strategy`, `sort-strategy`, `scan-type` and
`qual-placement` as a result, matching the size of the discrepancy
`METHODOLOGY3` recorded, but the **match count itself is identical either
way**.

### Root cause of the Q2/Q75 divergence: PG's own plan, not goopg's

Both queries' goopg plan is byte-identical between the two comparisons (same
capture). What differs is the **PG side**: the fixture (captured 2026-09-11,
`e2a50de40`) and today's live capture (2026-09-15) hold two genuinely
different plans for the same query against presumably-equivalent SF0.25 data:

- **Q2**: fixture PG picks `Merge Join` / `Finalize GroupAggregate` /
  `Gather Merge` / `Sort`; live PG today picks `Hash Join` /
  `Finalize HashAggregate` / `Gather` / `Parallel HashAggregate`. Top-level
  join method and aggregation strategy both flip.
- **Q75**: same family of flip (hash-based vs sort-based execution for the
  same aggregate), independently confirmed by diffing the two PG-side plan
  texts directly.

This is PG-side reference oscillation, not a goopg regression or a tooling
bug — the same phenomenon `METHODOLOGY3/02-open-problems.md` N26 already
documented for TPC-H Q8 (live PG flips MISSING-NODE ↔ SHAPE-DIFF between
same-day captures from PG-side stats drift). F7 (`01-what-we-learned.md`)
already establishes the mechanism generally: PG's `STD_FUZZ_FACTOR 1.01`
genuinely decides real plans at tiny exact cost margins, so ordinary
autovacuum/ANALYZE drift on the PG-side cluster between two capture sessions
is enough to tip a tied hash-vs-merge or hash-vs-group election either way.
Nothing about this implicates goopg's planner, and no code change follows
from it.

### Why `match=1` doesn't reproduce today

R128 (2026-09-13) measured `match=1` via the retired `methodology/
capture-tpcds.sh`, before the K18 `$$` fix and before per-capture provenance
stamping landed (M0137-0001, M0137-0002 — both 2026-09-15). Today's
measurement, using the fixed/stamped tooling, gets `match=2` against every
reference tried, including the same committed fixture R128 used. The most
parsimonious reading is that `match=1` was an artefact of the
since-superseded capture tooling (the K18 trap, or the absence of
provenance stamping letting a wrong arm/epoch go unnoticed) rather than a
standing reference disagreement — but this cannot be re-confirmed by
re-running the retired scripts, since the harness explicitly forbids
resurrecting them (`capture-tpcds.sh`'s own header: "do not resurrect them or
cite them in a new procedure"). The finding that stands on today's evidence
is simply: **as of HEAD, `match=1` does not reproduce; `match=2` does,
against both references.**

## Ruling

**Canonical reference: live PG `:65438`, captured via
`scripts/capture-tpcds.sh`.** This is already what `m0137-0003-baseline-
capture-procedure.md`'s worked example uses; this task lifts the caveat that
doc carried ("do not cite this doc as having settled the reference
question").

**`bench/tpcds/plans-pg`'s standing — NOT the same ruling as K9's TPC-H
sibling.** K9 found the TPC-H fixture **categorically** wrong: stale and
serial while live PG plans TPC-H in parallel, a systematic, permanent bias
that inflated every TPC-H MISSING-NODE count to 9 when the true count was 0.
The TPC-DS fixture is not that: it is periodically refreshed (last:
`e2a50de40`, 2026-09-11, alongside the SF0.25 migration), pinned with the
same GUCs `capture-tpcds.sh` pins in session, and — as measured above —
currently reproduces the **same match count** as a live capture taken four
days later. Its only demonstrated divergence from live PG is ordinary
plan-election noise on two non-matching queries, not a systematic bias.

So the fixture **remains a valid secondary reference** — useful for
`make plan-gate`'s pinned regression check and for any comparison run without
a live PG cluster up — but it is **demoted from co-canonical to
corroborating**: a fixture-based match/category count is a cross-check, not
independent evidence of the programme's standing, because PG's own plan for
a query can differ between the fixture's capture time and any later live
capture (Q2, Q75, this task). **Any report of the parity match count must
cite a live-PG capture; a fixture-only figure is not sufficient to move the
recorded standing.** The fixture's own commit message note "re-capture only
on query/dataset change" is still the right cadence for `plan-gate`'s
regression-pin purpose, but a category-level parity claim drawn from the
fixture should be corroborated against a fresh live capture first, precisely
because — unlike a match/no-match verdict, which per this measurement is
stable — the categorisation is not.

**The floor, and stopping the double-quote.** The programme's TPC-DS
non-regression floor is **`match >= 2`** (Q9, Q41), verified against live PG
today. `AGENT.md`'s "Success criterion" section, which named this task as the
thing that would settle the floor, is updated accordingly (§ below). Going
forward, cite `match=2` for TPC-DS's current standing; do not requote
`match=1` as a live alternative — it is a superseded-tooling reading that
does not reproduce at HEAD.

## What was not done (scope boundary)

- **No production planner/executor/catalog code touched** — this is a recon
  task per the M0137 charter (measurement + design note, no production
  diff).
- **`METHODOLOGY3/` is not edited.** It is a frozen 2026-09-14 stocktake of
  the prior phase (its own README: "supersedes nothing... written on branch
  ... at HEAD ..."), the same category of artefact the harness's round
  directories are — conclusions belong in this design doc, not retroactively
  folded into the stocktake. `AGENT.md` and `m0137-0003`'s own doc, both
  "live" procedural documents, are the ones updated.
- **The Q2/Q75 PG-side oscillation itself is not investigated further** —
  it is evidence about the oracle's stability at tied-cost margins (N26's
  class), not a goopg gap, and no ledger row follows from it: there is
  nothing PG-incompatible to fix on goopg's side.
- **The stats-epoch declaration is still not a checked step** — M0137-0006,
  unaffected by this task.

## Verification

- `scripts/capture-tpcds.sh` invoked twice (goopg `:65437`, live PG `:65438`)
  against the running SF0.25 clusters; both captures and their
  `pg-plan-parity-diff.py` outputs are reproducible via the commands in
  §"Measurement" above. Scratch captures written under `/tmp/m0137-0004/`,
  not committed (same precedent as M0137-0003's throwaway-server check).
- Per-query verdict diff (`diff` of the two `pg-plan-parity-diff.py` outputs
  sorted by query number) confirms exactly Q2 and Q75 differ, both
  non-MATCH on both sides.
- goopg SF0.25 server stopped after the measurement
  (`bench/tpcds/server.sh stop sf025`); no state left running.
