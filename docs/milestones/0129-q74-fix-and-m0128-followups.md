# Milestone 0129 — Q74 fix (M0128-P0.1 second half) + M0128 verdict follow-ups + residual-ledger burn-down

**Status:** accepted
**Filed:** 2026-08-08 (user directive — see the fix_plan Current Priority
banner)
**Reference plan:** `.ralph/fix_plan.md` (M0129 section)
**Implementation plan (authoritative task decomposition):**
`docs/design/0129-q74-fix-and-m0128-followups.md`
**Prerequisites:** none — M0124/M0125/M0127/M0128/M0123 are all CLOSED (banner
`f18d3014`). **M0129 is the top-priority milestone** (user directive
2026-08-08), ahead of the M-NIGHTLY backlog; the standing M-NIGHTLY *filing*
obligation still applies to every loop.
**Branch:** inherits the current lineage and its discipline (worktrees off
pinned clean HEAD, explicit pathspec staging, guard re-runs after
rebase/handoff).

## Background

M0128 closed 2026-08-08 with every item `[x]`, but three classes of work
remain, all named by primary sources (deferral-ledger rows and design docs —
never silently dropped):

**1. A live, default-ON performance regression with attribution done and the
fix pending.** TPC-DS SF0.5 Q74 went PASS 11–14 s → PASS ~86–99 s at the
M0127-P5.9 flag flip (`b92582fb`); SF1 runs ~290–329 s in nightlies.
M0128-P0.1 completed the attribution (ledger row 2026-08-07): the hash join
paths **are generated** for the 4-way CTE self-join, the cost model ranks
hash (~842) < merge (~2100) < NL (~8.4M), yet the final plan is nested loops
at all four levels — a non-cost-driven path-selection rejection. The resume
point (`addPathsToJoinrel`, `internal/planner/joinpaths.go:139`) and the three
hypotheses are recorded in the ledger row. The fix is this milestone's first
task.

**2. M0128's verdict follow-ups.** (a) The parallel-hash-build reopen
condition was measured **MET** (M0128-P2.1: build time 12.6–41.0 % for
medium/large dimensions) — the two candidate implementations announced as
"P2.1a/P2.1b" in fix_plan were never actually filed as tasks; this milestone
files them. (b) Bitmap heap scan shipped (P2.2–P2.4) with paths generated but
**always rejected by `add_path`** — BitmapAnd/Or (`choose_bitmap_and` port)
and a recorded "paths survive in the selectivity region" proof are open, plus
the design doc's 8 deferral rows (`docs/design/0128-0001-bitmap-heap-scan.md`
§6). (c) The resjunk-ctid **column path** was disabled after landing
(self-join column misalignment, `16419556`) — the durable PG-faithful fix
(intermediate-node full propagation) is open. (d) The P5.1 name-dedup made
Q2/Q8/Q17/Q18/Q22 clause-6 *adjudicable* but the re-adjudication measurement
was never run.

**3. Residual ledger rows from earlier milestones.** `deleteWithUsing` has no
EvalPlanQual (silent skip on concurrent update; M0125-0055 row); the command
counter is routine-nesting granularity with no per-tuple `cmin`/`cmax` (the
fence-map retirement line; M0125-0055 second row); `reduce_outer_joins` keeps
four residual halves (M0128-P4.1 row); the sort-spill path still drops the
lockRows ctid side-channel (root-0038 row); executor errors never emit the
`FieldPosition` wire field (M0127-PS6.2 row).

## Goals

1. **Q74 fixed** — the PG-shaped DP chooses hash join for the CTE self-join
   levels again; root cause recorded; regression guard landed.
2. **Cooperative parallel hash build implemented** — producer/consumer
   scan+filter parallelisation first (P2.1a), then a genuinely concurrent
   build (P2.1b) or a recorded not-needed verdict after P2.1a's measurement.
3. **Bitmap machinery made live** — BitmapAnd/Or path generation, at least
   one recorded query where a bitmap path survives `add_path` and wins on
   measured time, and the design doc's 8 deferral rows burned down (or, where
   a hard blocker exists, explicitly recorded with the blocker named).
4. **Row-lock durability** — `FOR UPDATE` cannot lose its tuple lock over a
   spilling sort, and the resjunk-ctid column path is re-enabled with
   intermediate-node propagation (the PG `preprocess_targetlist` shape).
5. **EPQ completeness** — `DELETE … USING` performs EvalPlanQual (wait,
   re-fetch, re-evaluate, delete the successor) instead of silently skipping.
6. **Command-counter completion** — per-statement counter plus per-tuple
   `cmin`/`cmax`, retiring the fence maps — or a documented, strong-reasoned
   no-go. Design doc lands **within M0129** either way.
7. **`reduce_outer_joins` residuals** — strictness catalog, ON-clause
   propagation, LEFT→ANTI, and the RIGHT→LEFT flip half (including the parser
   representation work it requires).
8. **`FieldPosition` on the wire** — executor errors carry the PG error
   position field; the regress-runner normalisation is dropped and the
   baseline re-captured.
9. **Clause-6 re-adjudication recorded** for Q2/Q8/Q17/Q18/Q22.

## Task list (summary — the design/0129 plan doc is authoritative)

| task | what | source |
|---|---|---|
| S1 | Q74 path-selection fix (attribution's second half) | ledger 2026-08-07 M0128-P0.1 |
| S2 | `deleteWithUsing` EPQ | ledger 2026-08-06 M0125-0055 |
| S3 | sort-spill ctid side-channel carry | ledger 2026-08-06 root-0038 |
| S4 | cooperative parallel hash build (S4.1 = P2.1a, S4.2 = P2.1b) | parallel-query/10-roadmap + IMPLEMENTATION-TODO |
| S5 | bitmap burn-down (8 subtasks over the §6 deferral rows + the survival proof) | `0128-0001-bitmap-heap-scan.md` §6 |
| S6 | resjunk-ctid column path re-enable | ledger 2026-08-06 root-0038 (2nd row; disable event 2026-08-08) / M0128-P6.1 |
| S7 | clause-6 re-adjudication measurement (Q2/Q8/Q17/Q18/Q22) | ledger 2026-08-07 M0128-P5.1 |
| S8 | statement-granularity command counter + per-tuple cmin/cmax | ledger 2026-08-06 M0125-0055 (2nd row) |
| S9 | `reduce_outer_joins` residuals (4 subtasks) | ledger 2026-08-07 M0128-P4.1 |
| S10 | `ExecError.Pos` → wire `FieldPosition` + regress re-baseline | ledger 2026-08-06 M0127-PS6.2 |

**Filing rule for this milestone (user directive 2026-08-08):** no task item
is deferred without a strong reason recorded in the deferral ledger; every
item's subtasks are listed inline in the fix_plan task body; every non-trivial
subsystem lands its design doc (status `draft` → `accepted`) **within M0129**
— a design doc punted past the milestone is a milestone failure.

## Acceptance bar

1. **Q74:** SF0.5 Q74 ≤ 20 s (from ~86–99 s) with byte-identical output
   (7 rows, ck `2ffc13c77bf53028`); the final plan shows Hash Join at the CTE
   self-join levels; the root cause and fix are recorded in the ledger row and
   the fix_plan item; a regression guard test exists.
2. **No silent regressions:** TPC-DS SF0.5 zero row/checksum deltas across
   the milestone; the plan channel stays 99/99 same except where a task's own
   bar adjudicates movement (S1's Q74 fix, S5 bitmap paths, S9 demotions).
   TPC-H SF1 22/22 within the standing band (≤ 1.2× R0 = 493.31 s; Q9 ≤
   170.9 s).
3. **Parallel hash build:** each of S4.1/S4.2 lands with the 10-roadmap P8
   gates (identity over the join corpus; race-gate under a probe-heavy
   workload; TPC-H Q9/Q17/Q19 measurement) — or S4.2 records a measured
   not-needed verdict after S4.1.
4. **Bitmap:** EXPLAIN evidence of a chosen BitmapAnd/Or plan, and a recorded
   measurement (query, plan, time) where a bitmap path survives `add_path`
   and beats both the index-scan and seq-scan alternatives.
5. **Row locks:** a `FOR UPDATE` over a sort forced past `work_mem` still
   blocks (guard test); the re-enabled column path passes eval-plan-qual
   `partiallock`/`lockwithvalues` and a self-join regression test.
6. **EPQ:** an isolation spec proves concurrent-UPDATE × `DELETE … USING`
   waits and deletes the successor version; no silent skip remains on that
   path.
7. **Wire protocol:** psql renders `LINE n: … ^` for a failing executor
   expression; the regress suite is re-baselined and green.
8. **Clause 6:** a recorded adjudication for all five queries
   (Q2/Q8/Q17/Q18/Q22) under `analysis/`.
9. **Hygiene:** every task's subtasks are inline in fix_plan.md; zero items
   closed by deferral without a ledger row stating the strong reason; all
   design docs listed below exist with status `accepted` (or the task itself
   is open).

## Required design docs

| doc | status | covers |
|---|---|---|
| `docs/design/0129-q74-fix-and-m0128-followups.md` | created at filing | the authoritative task decomposition (all S-tasks) |
| `docs/design/0129-0001-command-counter-and-cmin-cmax.md` | **within M0129 (S8.1)** | heap-header `cmin`/`cmax`, per-statement `CmdID`, fence-map retirement |
| cooperative parallel hash build design (under `docs/design/parallel-query/` or `0129-0002-*`) | **within M0129 (S4.1, before code)** | producer/consumer build; concurrent-build shape |
| resjunk-ctid schema-propagation design (`0129-0003-*`) | **within M0129 (S6, before code)** | where the ctid column is injected and how every intermediate node's schema carries it |

Small S5/S9 subtasks may ride the implementation-plan doc (per the repo rule,
a design doc is required for every *non-trivial subsystem*; single-function
changes with unit tests may cite this plan instead).
