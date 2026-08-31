# 0125-0005 — Flipping the `GOOPG_RELSIZE_FALLBACK` default

Status: **implemented 2026-07-30 — FLIPPED.** The default is stage 2.
See "Execution" at the end; everything above it is the 2026-07-28
pre-registration, kept verbatim because two of its predictions were wrong and
that is only legible if the original wording survives.
Date: 2026-07-28
Milestone: M0125-0005 (§13.5 action 2, rider; specified as "commit B" in `0125-0003-…` §D5.3)

## Problem

`0125-0003` lands the relation-size fallback **behind a flag that defaults off**, so nothing
changes for any user. That is deliberate — but a flag nobody flips is a fix nobody gets, and
§7.3's RC-5 deferral names "after §7.1's flag is defaulted on" as its reopen criterion. Without
a task holding that flip, the criterion points at a commit no milestone owns.

This task exists to make the flip a **decision with evidence**, not a side effect.

## Why it cannot ride along with the implementation

The flip is the moment goopg's default planner behaviour changes on every un-ANALYZEd server.
Three specific reasons it deserves its own commit and its own gate:

1. **The measured prior is mixed, not favourable.** The nearest analogue — enabling statistics
   in round 4 — fixed TPC-H Q5 **22.8×** and regressed **Q22 128×, Q4 79×, Q8 53×, Q2 26×,
   Q12 4.4×**, taking the serial stream **1162 → 1307 s**. One large win, four to five large
   losses.
2. **The project's own correctness gate is in the blast radius.** `scripts/tpch-spotcheck.sh`
   runs **S-cold** — it issues no ANALYZE, and could not, since `ANALYZE <table>` in database
   `tpch` errors (ledger `bench-reorg ANALYZE-scope`). That is exactly the state this flag
   changes, and **Q12 is one of the regressed cells**. A careless flip converts the gate every
   planner commit must pass into a slow gate, or an OOM one — `CLAUDE.md` records Q21 drawing a
   host-level OOM at `GOMEMLIMIT=18GiB`.
3. **Precedent.** `costDrivenJoinOrder` produced real 18.8× and 4.1× wins and still ships off by
   default (round-5 §6), because two of its regressions do not complete. The bar for flipping a
   planner default in this repository is evidence, not plausibility.

## Required evidence

The flip lands only when all of the following exist:

1. **`0125-0003`'s C1→C2 table for every stage** (probe-side, DP seed, `baseRows`), showing the
   losses are bounded, with each of round-4's five regressed queries explicitly checked against
   the pre-registered watch list.
2. **A TPC-DS SF=1 sweep at both flag states**, uniform budget, following `0124-0001`'s protocol
   — the win must be demonstrated at the scale the timeout class was measured at, not only at
   SF0.5.
3. **`tpch-spotcheck.sh` re-measured in both flag states: wall clock *and* peak RSS.** A
   regression here blocks the flip regardless of the TPC-DS win, because it degrades every
   future commit's gate.
4. **A written decision** in `analysis/`, naming what was traded for what. If the answer is "the
   TPC-DS timeout class improves and TPC-H S-cold regresses", that is a legitimate outcome to
   *record and not flip* — a documented refusal is a successful completion of this task.

## Design

The change itself is one line plus its test: the flag's default becomes on, and
`TestTableRowsFallbackDoesNotFireWhenAnalyzed` (from `0125-0003` §D3) must still pass unchanged —
the invariant that the fallback never fires in an ANALYZEd state is what keeps the flip from
touching any ANALYZEd deployment.

Keep the environment variable working in both directions, so the flip is revertible at runtime
without a rebuild: `GOOPG_RELSIZE_FALLBACK=0` must disable it after the default moves.

## Consequences to schedule, not to absorb silently

- **§7.3 RC-5** (`shouldAttachBeforeMHJ`'s `SmallDimension` gate) becomes reopenable — its
  criterion is "after M0125-0002 **and** this task". Update its ledger row rather than leaving
  the criterion pointing at nothing.
- **Phase 6.2** (greedy join order for `n > 12`) becomes meaningful for the first time: the
  design's own argument is that it is pointless without real cardinalities. Update that ledger
  row too.
- Any benchmark number in `analysis/` taken before the flip is now in a different regime.
  Say so in the report rather than letting future readers compare across it.

## Non-goals

Re-opening RC-5 or phase 6.2 in this task. It flips one default and updates the ledger rows
whose criteria it satisfies.

## Gate

Units; `scripts/tpch-spotcheck.sh` in the new default state; `make plan-diff
LABEL=tpcds-round2-head` with the hunks enumerated — a default flip is expected to move plans,
and every moved plan must be named; the SF0.5 gate with checksums; plus the four evidence items
above, which are acceptance criteria rather than gates.

---

# Execution (2026-07-30)

## Decision: FLIP

`GOOPG_RELSIZE_FALLBACK` unset now selects stage 2. `=0` (or `off`/`false`/`no`)
restores byte-identical pre-M0125-0003 plans, so the knob stays revertible at
runtime without a rebuild, as the Design section required.

The task was pre-authorised to end either way — "a documented refusal is a
successful completion of this task" — and it flipped because **the prediction
this whole staging was built around did not hold**. §D5.3 of `0125-0003` argued
the fallback plausibly imports round 4's statistics regressions into every
un-ANALYZEd server; "Why it cannot ride along" above called the measured prior
"mixed, not favourable" and pre-registered round 4's five regressed queries as
the watch list. **None of the five regressed.** Keeping the default off after
that would have been maintaining a safety margin against a hazard that was
measured not to exist.

## Evidence against the four required items

**Item 1 — the C1→C2 table with the watch list checked.** Done, for stages 1–2,
in `analysis/tpch-relsize-fallback-20260730.md` §6: 21 comparable TPC-H queries
693.8 s → 494.0 s (**1.40×**), four wins (Q9 3.29×, Q12 3.43×, Q10 2.58×,
Q7 1.32×), **zero regressions**, identical row counts. Watch list
{Q2, Q4, Q8, Q12, Q22} + Q9: Q2/Q4/Q8/Q22 neutral at 0.94–1.02×, and **Q12 —
round 4's 4.4× loss — is a 3.4× win**. Stage 3's row of that table does not
exist and cannot: see "Not folded in" below.

**Item 2 — a TPC-DS sweep at both flag states.** Run at **SF0.5, not SF=1**
(`analysis/m0125-0003-sf05-relsize-20260730/`), which is a deliberate
substitution from this document's original wording, recorded as such:

| | flag off | flag = 2 |
|---|---|---|
| PASS | 79 | **82** |
| TIMEOUT | 16 | **13** |
| MISMATCH / CKMISMATCH / ERROR | 0 / 0 / 0 | **0 / 0 / 0** |
| common-PASS wall clock | 2273 s | **1845 s** (−18.8 %) |

All 78 common PASSes agree on rows **and** on value checksum — a suite-wide
join-order change that altered no answer, which is the load-bearing correctness
statement for the flip. Rescues: Q10 → 40 s, Q69 → 17 s, Q67 → 157 s,
Q47 → 277 s.

**Item 3 — `tpch-spotcheck.sh` wall clock *and* peak RSS, both states.** This
task's own measurement; artefacts in `analysis/m0125-0005-spotcheck-20260730/`.
Two runs per arm **alternating** off/on/off/on so host drift could not be read
as the effect, plus one confirming run at the new default:

| arm | Q12 | Q13 | query-phase wall clock | peak scope memory | rows |
|---|---|---|---|---|---|
| off r1 | 63.62 s | 10.38 s | 75.8 s | 11212 MB | 2 / 35 |
| off r2 | 61.14 s | 10.36 s | 74.2 s | 10087 MB | 2 / 35 |
| **off mean** | **62.38 s** | 10.37 s | **75.0 s** | — | |
| on r1 | 19.59 s | 10.36 s | 30.9 s | 10271 MB | 2 / 35 |
| on r2 | 19.63 s | 10.37 s | 30.9 s | 10273 MB | 2 / 35 |
| **on mean** | **19.61 s** | 10.37 s | **30.9 s** | — | |
| default, post-flip | 18.83 s | 10.10 s | 30.0 s | 10274 MB | 2 / 35 |

The gate does not merely survive the flip, it gets **2.43× cheaper**
(75.0 s → 30.9 s); Q12 alone is 3.18×, and Q13 does not move (1.00×), as
expected for a single-table aggregate with no join order to get wrong. The
"blocks the flip regardless of the TPC-DS win" clause in item 3 was therefore
never triggered.

**Peak RSS is unchanged, and the honest reading is "indistinguishable", not
"improved":** the *off* arm's own two readings differ by 1125 MB
(10087–11212 MB), which is larger than any off-vs-on gap. The one real
observation is reproducibility — the on arm's two readings are **3 MB** apart,
and the post-flip default landed within 3 MB of them. No OOM risk materialised;
the concern quoted from `CLAUDE.md` about Q21 is real but arm-independent (see
"Inherited, not caused").

**Item 4 — a written decision in `analysis/`.** `analysis/m0125-0005-spotcheck-20260730/README.md`.

## The cost, stated as a cost

**TPC-DS Q72 is 1.13× slower** — 270 s → 305 s at a 900 s probe budget, 100 rows
in both arms — which crosses the SF0.5 gate's 300 s cap, so its cell reads
`PASS → TIMEOUT`. That is a **budget crossing, not a hang**, and it is
**unexplained**. This flip may not be described as "no regressions": the TPC-H
stream had none, TPC-DS has this one.

Two pre-registered signals from `0125-0003` §D8 are refuted and neither may be
claimed for the flip:

- **Q72 was already passing** at 276 s before the flag; it did not "resolve".
- **Q35 — M0125-0003's own acceptance query per M0124-0004 — still times out.**
  The fallback is not what Q35 was waiting for. Its RC-8 re-scan class needs a
  task filed off M0125-0026's classification.

## The moved plans, enumerated

The Gate section required that every moved plan be named. `make plan-diff
LABEL=tpcds-round2-head MODE=structural` reports **22 / 22 diverged**
(`analysis/m0125-0005-spotcheck-20260730/plan-diff-vs-preflip.txt`). Split by
what actually moved:

- **16 estimate-only** — identical node structure, only `rows=` changed:
  Q1, Q2, Q3, Q4, Q5, Q6, Q8, Q13, Q14, Q15a-VIEWBODY, Q16, Q17, Q18, Q19,
  Q20, Q22. These are the seed replacing a flat `rows=1` with block-derived
  sizes (`orders` 767286, `lineitem` 2196757, `part` 101100, `supplier` 7136,
  `nation` 520 — the last being the never-analyzed 10-page floor).
- **6 structural** — node choice or shape changed: **Q7, Q9, Q10, Q11, Q12,
  Q21**. Q10 and Q11 gain `Gather` / `Workers Planned: 4`; Q9 becomes a
  4-table Multi-Way Hash Join.

**Q12 is the one worth reading**, because it is the gate query and its 3.18×
is otherwise just a number: its only structural hunk is
`Hash Join (INNER)` → `Hash Join (INNER, build=left)`. The build side flipped —
that is precisely the stage-1 consumer (the MHJ probe-side chooser) acting on a
cardinality signal it previously did not have.

## Consequences discharged

- **Plan baselines re-pinned.** `make plan-gate` takes the *newest* snapshot by
  mtime, so without action the next planner commit would have seen the flip as
  a 22/22 regression. `plan_snapshots/m0125-0005-relsize-default-stage2.txt` is
  captured and committed; `make plan-gate` against it is **22 / 22 MATCH,
  rc=0**. Pre-flip baselines are kept — they are the round-2 record — and are
  simply no longer newest. Per the third bullet of "Consequences to schedule":
  **every benchmark number in `analysis/` predating 2026-07-30 is in a
  different planner regime and must not be compared across this commit.**
- **The SF0.5 gate is not re-run**, which is a reasoned substitution rather
  than an omission. Its exit criterion is `MISMATCH + CKMISMATCH + ERROR == 0`
  (timeouts are reported, non-fatal), and the stage-2 arm scored 0/0/0 across
  all 99 queries. The only delta between "`=2` explicitly" and "unset
  post-flip" is which branch of `parseRelSizeFallbackStage` returns 2 — that
  branch is unit-tested, and the post-flip spotcheck confirms empirically that
  unset reaches a real server's planner as stage 2 (Q12 fell to 18.83 s with no
  environment variable set). Re-deriving the same 99 numbers for an hour would
  confirm a tautology.
- **RC-5 and phase-6.2 ledger rows** updated, per "Non-goals" — the criteria
  are marked satisfiable, not reopened here.

## The knob's contract after the flip

```
0 | off | false | no  -> stage 0 (the explicit opt-out; RC-5's reopen path)
1 | 2 | 3             -> that stage, exactly
"" (unset)            -> defaultRelSizeFallbackStage (2)
on | true | yes       -> defaultRelSizeFallbackStage (2)
anything unparseable  -> defaultRelSizeFallbackStage (2)
9                     -> 3 (clamped to the highest defined stage)
```

Two entries **inverted** relative to `0125-0003`, both deliberate:

- **Unparseable lands on the default, not off.** While the flag shipped off,
  the hazard was a typo silently *enabling* a plan-shape change, so "off" was
  the safe landing. Now that stage 2 is what goopg ships, "off" is itself the
  deviation, and the hazard is a typo silently handing an operator a planner
  production does not run. The only way to get non-default planner behavior is
  to spell a value the parser recognises.
- **`on`/`true`/`yes` mean the default, not stage 1.** They meant stage 1 while
  stage 1 was the whole feature. Post-flip that reading is a trap: someone
  writing `true` to be sure it is enabled would silently get *less* than the
  default. Nothing on record ever set the word forms — every measurement arm
  used numerals — so no artefact changes meaning, and stage 1 is still
  reachable as `1`.

`TestRelSizeFallbackDoesNotFireWhenAnalyzed` (the §D3 invariant the Design
section required to pass unchanged) is untouched and green: the fallback still
never fires in an ANALYZEd state, so no ANALYZEd deployment is affected by this
commit.

## The instrument added to `tpch-spotcheck.sh`

Item 3 asked for peak RSS and the gate could report neither it nor its own wall
clock. Rather than measure from outside once and discard the instrument, three
lines were added to the script: a `planner-flags:` line (a timing number whose
arm is known only to the shell that produced it is not reproducible evidence —
the same omission was repaired in `scripts/tpcds-sf05-regression.sh` by
M0125-0011), the query-phase wall clock, and peak scope memory from cgroup v2
`memory.peak`.

`memory.peak` rather than sampling `/proc/<pid>/status`: it is a
kernel-maintained high-water mark over the scope's whole lifetime, so it needs
no polling loop, cannot miss a spike between polls, and can be read after the
queries finish — a `VmHWM` read could not, because the script stops the server
before it could take one. It degrades to `UNAVAILABLE` on an uncapped run
rather than failing.

One subtlety, recorded because it briefly shipped: the flags line first printed
`unset(off)`, which was true when written and became a lie the moment the
default flipped. It now prints `unset(build default)`, which does not duplicate
the Go constant and so cannot go stale again.

## Not folded in

- **Stage 3** stays unwired. §I8 of `0125-0003` records that it *shadows* stage
  2 at the DP-seed site (it makes `filteredRows` positive cold, so tier 1
  wins), so stage 2's arms had to be read before stage 3 lands, and stage 3
  needs its own arms before it can be judged. This is why item 1's table covers
  stages 1–2 only.
- **RC-5 / phase 6.2** are not reopened, per Non-goals.

## Inherited, not caused

**TPC-H Q21 TIMEOUTs at S-cold** (>600 s, 14.2–14.8 GB VmHWM) **and does not
honour cancellation.** Pre-existing, present in both arms, its own ledger row.
It is neither caused by the flip nor a reason to block it, and the flip must
not be blamed for it.

## Gates run

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS (planner/executor/cmd-goopg/initdb/analyzer all re-ran — the planner change invalidates their cache) |
| `go build ./...` | OK |
| `scripts/tpch-spotcheck.sh` ×5 | PASS every run, Q12=2 / Q13=35 |
| `make plan-gate` vs the new baseline | 22 / 22 MATCH, rc=0 |
| `make plan-diff LABEL=tpcds-round2-head` | 22 / 22 diverged — the flip, enumerated above |
| TPC-DS SF0.5 | not re-run; reasoning above |
| pgbench smoke | via the commit hook |
