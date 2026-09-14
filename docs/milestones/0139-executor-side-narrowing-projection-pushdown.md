# Milestone 0139 — Executor-side narrowing (projection pushdown)

**Status:** planned
**Filed:** 2026-09-14 (user directive answering
`METHODOLOGY3/04-forward-plan.md` §1.1 Question 1 with **(a) build it**)
**Priority placement:** third in the plan-parity group. Independent of M0140 —
per the owner's **(c) = Go**, TPC-DS work proceeds whether or not this milestone
is in flight. See the `## Current Priority` banner in `.ralph/fix_plan.md`.
**Reference plan:** `.ralph/fix_plan.md` (M0139 section)
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Prerequisites:** M0137 (the qual-placement census in M0137-0010 gates every
slice here — narrowing a scan's output is precisely the class of change that
silently drops a filter).

## The decision this milestone implements

`METHODOLOGY3/README.md` §3 established that TPC-H's two closest queries — Q9
(1 category from MATCH) and Q4 (2 categories) — block on the same missing
capability, and that **every cheaper lever aimed at them is measured and
rejected**: forcing Q4's semi rows (R71, "Rows theory DEAD"), wiring semi-join
selectivity (R78, reachable bound 1.28x not the needed ratio), declaring all
eight TPC-H foreign keys (R125/R126 + step-(d) recon: match 6->6, three
categories *worse*), narrowing the planner's cost inputs corpus-wide
(R120–R124, shipped R128: +2 category instances, no flip), and correcting the
hash bucket charge (R129, parity-inert).

The owner chose **(a): build executor-side narrowing.**

## Scope — and what is NOT in scope

`METHODOLOGY3/02-open-problems.md` §B1 splits the capability into three pieces
with different owners. **Only the first is in this milestone:**

| half | reach | approval |
|---|---|---|
| **projection pushdown** — narrow what scans emit inside a join tree | planner + create-plan; `Datum` unchanged | **in scope; not covered by the `minimize_datum` decline** |
| packed retention format (`PackedTuple`/`PackedSlot`) | ~70 non-test sites | **declined**; owner decision required, informed by S3's measurement |
| a `Datum` re-layout below 48 B | ~3,090 non-test sites | declined by take3 13 §10; nobody proposes it |

**Name the second half correctly.** `minimize_datum/README.md` states that
`Datum` *"does not shrink — it stays exactly 48 bytes and stays the working
format"*; what was declined is *a new row representation*.
`minimize_datum/05-work-estimate.md` §1 warns that confusing the re-layout with
the retention format is *"the single most likely way to misprice this work"*, so
**"the `DatumBytes` half" is the wrong label** and must not be used.

## The structural blocker, already located

Inside a join tree **there is no `*Project` above the scan at all** — the join
reads the scan directly, so there is nowhere to hang a projection. The machinery
for *applying* a narrowing already exists and ships default-ON, but in **three
separate files**, not one: `GOOPG_NARROW_BUILD` in
`internal/optimizer/narrowoutput.go` (hash build side and merge input),
`GOOPG_NARROW_UPPER` in `upper_narrow_apply.go:90`, and
`GOOPG_NARROW_UPPER_SORT` in `upper_narrow_chain.go:124`. What is missing is an
attachment point at the leaf.

**`attr_needed` is NOT the blocker.** Two earlier diagnoses said it was and both
were wrong; the promotion is schema-preserving. Slice 1 must not re-derive that.

## Argue this campaign on Q4 and the executor gap — not on Q9

A measured caveat that appears nowhere in the earlier summaries and that anyone
funding this work must read first. `internal/optimizer/entrywidth.go:38-48`
carries a permanent comment headed **"WHAT THIS DOES NOT BUY"**: correcting the
entry width does **not** change Q9's batch count, because `nbatch` is 4 at entry
112..194 alike, drops to 2 only in the narrow 96..111 window, and **returns to 4
below 96** as the bucket array doubles to 100.7 MB and takes back more than the
rows gave up — *"the lever on this witness is MapSlotBytes, not the entry."*
Commit `2e15b8ca3` adds that a packed retention format would make Q9's batching
**worse**, and R129 measured that named lever, `MapSlotBytes` 48->96, as
**parity-inert**.

So Q9's case here is measurably non-monotone and weaker than Q4's. The case
rests on **Q4** (R81: the election turns on a startup ratio against
`stdFuzzFactor = 1.01` — goopg 1.0086 inside fuzz, PG 1.0118 outside — with the
semi output at 448 B against PG's 16 B, *"widths ratio 28x exceeds the rows
ratio 16.6x"*) and on **the executor gap** (R97: goopg is 4.1x slower than PG on
TPC-H SF1, and the row-width gap is the prime suspect for the worst cliffs —
TPC-DS Q61 70x, Q58 26x, Q55 23x).

Two further blockers the `minimize_datum` review itself raised, which the owner
should see before the packed-retention decision: *"the premise was modelled, and
the measured answer is different — **and smaller**"*, and *"sequencing violates
take3 13 §8.2."*

## Per-task discipline (READ FIRST — binding)

1. **Design note when the task is selected**, indexed in
   `docs/design/README.md` in the same commit. Overrides
   `docs/milestones/README.md` §"Workflow Per Milestone" step 2 for this group.
2. **Slice 1 carries no parity prediction.** Its pre-registered prediction is
   *"no parity movement; the pass fires N > 0 times"*. A slice that predicts a
   match flip is mis-scoped. K67 already measured the residue: even narrowed to
   one column goopg is 72 B/row -> 103 MB and **still spills** at
   `work_mem=64MB`, where PG is 22 B/row -> 31 MB — so projection pushdown alone
   is necessary and very likely not sufficient.
3. **Every slice is gated on the qual-placement census** (M0137-0010).
4. **Do not implement the packed retention format.** S3's measurement goes to
   the owner as a decision request; `minimize_datum` is NOT APPROVED TO START.

## Definition of Done

- A hook point exists inside the join tree and fires on a stated, non-zero
  number of corpus queries.
- Scans inside join trees emit narrowed rows, with the existing narrowing
  machinery reused rather than duplicated (it lives in `narrowoutput.go`,
  `upper_narrow_apply.go` and `upper_narrow_chain.go`).
- The residue is **measured** against K67's 72 B/row floor and reported as a
  number, not argued.
- That number is put to the owner as the packed-retention decision request.
- Q4's startup ratio is re-measured against `stdFuzzFactor = 1.01` and reported
  whether or not it crosses.
- The "duplicate build map" premise is **re-measured**. `minimize_datum/05`
  §1.5 prices its deletion at "one commit", but at HEAD `lazyHash` and
  `lazyIntHash` read as **mutually exclusive lanes**
  (`operators_join_agg.go:1238-1254` returns after filing into the int map;
  `demoteIntHash` at `:1325-1338` migrates then nils it), so there may be no
  ~2x to recover. Either the win is measured and taken, or the refutation is
  recorded as a ledger row and `minimize_datum/05` §1.5 is corrected.
- No values regression on either corpus; the qual-placement census is clean on
  every slice.
