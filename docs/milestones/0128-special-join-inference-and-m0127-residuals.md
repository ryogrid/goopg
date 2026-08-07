# Milestone 0128 — Special-join inference (`join_is_legal`/`SpecialJoinInfo`) + M0127 residuals

**Status:** planned
**Filed:** 2026-08-07
**Reference plan:** `.ralph/fix_plan.md` (M0128 section)
**Design of record:** `docs/design/leftdeep-joins/` — **03** §4.4 (the v1 pin
this milestone removes) and §6 (outer-join restriction inference), **07** §5
(semi/anti in-DP), **09** §3.11/§3.17/§3.19/§3.22/§3.23 (the measured
residuals). The bundle chapters are the sole design authority for the planner
items and are referenced only, never modified. The executor items' design
authority is `docs/design/parallel-query/` — **07** §3.1 and **10-roadmap**
(P8 + the "Deliberately deferred" table). **The implementation plan (task
breakdown) is `docs/design/0128-special-join-inference-and-m0127-residuals.md`.**
**Prerequisites:** M0127 complete (P6.4, supersession stamps, is its last open
task at filing). M0128 is the priority **immediately after M0127** (user
directive 2026-08-07, recorded in the fix_plan Current Priority banner).
**Branch:** inherits the current `planner-dp-and-related-refactor` lineage and
its discipline (worktrees off pinned clean HEAD, explicit pathspec staging,
guard re-runs after rebase/handoff).

## Background

M0127 converted the `leftdeep-joins/` bundle into shipped behaviour: the
PG-shaped three-phase DP is now the **only** join-order search (P6.3 deleted
the old subset-bitmask DP, 2026-08-07, DS05 plan shapes 99/99 identical), and
both `MultiHashJoin` (P6.2) and runtime fusion (P6.1) are deleted. But the
bundle itself names the interim measures v1 shipped with, and the acceptance
sweeps surfaced residuals the milestone deliberately did not scope. This
milestone is the vehicle for both lists.

**M0127 leftovers (named by the bundle as temporary):**

1. **`join_is_legal`/`SpecialJoinInfo` inference (03 §4.4).** v1's rule —
   every outer/semi/anti join is a *pinned opaque input*, planned as today and
   entering the search only as a `PathPrebuilt` initial rel — is an explicit
   interim measure ("a temporary measure, not a design commitment",
   03 §4.4). PG admits special joins into the DP and governs them with
   `join_is_legal` ordering-constraint inference over `SpecialJoinInfo`
   entries (`postgres/src/backend/optimizer/path/joinrels.c:350`); goopg's
   `joinOrderRestricted`/`hasJoinRestriction` stubs in
   `internal/planner/joinsearchlevel.go` are reserved constant-false. 03 §4.4
   states the staged follow-ups (§6 outer-join restriction inference, 07 §5
   semi/anti in-DP) are *implementable now that the bushy phase exists* — what
   blocks them is the constraint inference, not the search shape. Landing the
   inference **unlocks `GOOPG_PGSHAPED_COLLAPSE`**: the explicit-JOIN
   flattening sub-flag soaked to two documented NO-GOs (09 §3.18/§3.19 —
   `TestNoCorpusQueryHasAnInnerOnlyJoinChain` shows all 12 TPC-DS
   explicit-JOIN queries contain an outer join, so with outer joins pinned the
   collapse can never produce an inner-only chain), verdict "run-but-NOT-
   discharged". The shape the unlock targets is an outer link **buried under
   inner joins** — TPC-DS Q78's shape.
2. **Parallel hash build and bitmap heap scan.** Both were M0127 non-goals by
   bundle README (parallel-query/ owns the parallel roadmap; bitmap machinery
   does not exist in goopg at all). parallel-query/10-roadmap defers
   *cooperative* parallel hash **build** with reopen condition "a measured
   plan where build time dominates" — the measurement itself is an M0128
   task. Bitmap heap scan (`nodeBitmapHeapscan.c`/`nodeBitmapIndexScan.c`/
   `tidbitmap.c` analogues) has no design yet and starts with one.

**Long-term residuals (measured during M0127 verification, deferred by the
bundle with resume points):**

- **One scan, two estimates; `avgVarBytes=0` (09 §3.23).** The hash build's
  memory geometry (`buildGeometry`,
  `internal/executor/operators_join_agg.go:645`) reads an estimate that
  ignores on-scan quals, and passes `avgVarBytes=0` because goopg collects no
  per-column average-width statistic (`pg_statistic.stawidth`; PG feeds
  `Plan.plan_width` from it) — so `internal/hashsize.EntryBytes` under-counts
  text-heavy builds and biases `nbatch` low.
- **`reduce_outer_joins` inner demotion (09 §3.22).** The RIGHT→LEFT flip
  half is unrepresentable (`parser.FromExpr` is a Base RangeVar + a flat
  `[]JoinExpr`), but the **reduction** half — demoting an outer join to inner
  when a strict qual on the nullable side makes the null-extension dead — is
  portable and is a pessimization fix, never a wrong answer.
- **EXPLAIN alias ambiguity (09 §3.11).** Q2/Q8/Q17/Q18/Q22 scan one relation
  name twice without an alias; the printed pairing cannot be adjudicated under
  acceptance clause 6. PG dedups range-table names
  (`ruleutils.c` `select_rtable_names_for_explain`,
  `postgres/src/backend/utils/adt/ruleutils.c:3855`).
- **`Rows Removed by …` absent (09 §3.17).** No `Rows Removed by Filter` /
  `Rows Removed by Join Filter` line exists at all; the structured formats
  carry no qual properties either.
- **Q74 unattributed regression.** TPC-DS SF0.5 Q74 went PASS 11–14 s
  (2026-08-04 sweeps) to PASS ~81–93 s since M0127-P5.9-i — a stable ~7×
  slowdown with correct rows/checksum, never attributed (09 §6 protocol
  applies).
- **lockRows spill-time side-channel loss (ledger root-0038).** `lockRowsOp`
  reconstructs the TID from operator-tree shape
  (`internal/executor/operators_lockrows.go` `findScanLeafForRel`/
  `markJoinPreserveCTID`); the walkers know ~8 of ~70 operator types, so a
  `spillOp` (or materialize/memoize/gather/lateral/…) between LockRows and the
  scan leaf degrades `FOR UPDATE` to an **unlocked pass-through with no
  error**. The durable fix is PG's resjunk-ctid rowmark
  (`preprocess_targetlist` junk attributes).

## Goals

1. **Special-join inference** — port `SpecialJoinInfo` construction and
   `join_is_legal`/`have_join_order_restriction` so outer (LEFT, then FULL)
   and semi/anti joins enter the DP under PG's legality rules; the 03 §4.4
   pin relaxes to PG's actual scope.
2. **`GOOPG_PGSHAPED_COLLAPSE` verdict with the pin removed** — re-run 09
   §3.19's measurement protocol once outer links are searchable; flip the flag
   or record a third, final no-go with the interim-measure excuse gone.
3. **Executor features** — a measured verdict on cooperative parallel hash
   build (parallel-query/10-roadmap's reopen condition), and bitmap heap scan
   shipped behind measurement or documented as a no-go with its design landed.
4. **Estimate integrity** — per-column average-width stats collected and fed
   to hash sizing; the one-scan/two-estimates gap closed so the executor reads
   the post-qual estimate.
5. **Outer-join strength reduction** — the portable `reduce_outer_joins`
   half (outer→inner demotion).
6. **EXPLAIN fidelity** — range-table name dedup making Q2/Q8/Q17/Q18/Q22
   adjudicable under clause 6; `Rows Removed by Filter`/`by Join Filter`
   lines.
7. **Attributions and safety nets** — Q74's ~7× regression attributed and
   fixed or ledgered; `FOR UPDATE` can never silently degrade to unlocked
   pass-through (hard error), with the resjunk-ctid durable fix landed or
   decomposed.

## Acceptance bar

1. TPC-DS SF0.5 **zero row-count and checksum deltas** across the milestone
   (plan-shape changes allowed only where a task's own bar adjudicates them:
   outer-join orders, reduced joins, deduped aliases, new EXPLAIN lines).
2. TPC-H SF1 **22/22 complete**, total ≤ 1.2× pinned R0 (493.31 s), **Q9 ≤
   170.9 s** — the M0127 S5 bounds, retained as the no-regression floor.
3. **COLLAPSE verdict recorded** per 09 §3.19 with outer joins searchable
   (flip ON, or third documented no-go — a measured either-way outcome is
   success; an unmeasured one is the only failure).
4. **Parallel-hash-build verdict recorded** against the roadmap's reopen
   condition; if GO, identity over the join corpus + race-gate + TPC-H
   Q9/Q17/Q19 measurement (10-roadmap P8 gates).
5. **Q74 attributed** (fix landed, or ledger row with resume point and a
   measured bound).
6. `EXPLAIN (ANALYZE)` on a filtered scan shows `Rows Removed by Filter`;
   Q2/Q8/Q17/Q18/Q22 plans are clause-6 adjudicable.
7. A `FOR UPDATE` over a spilling join tree **errors loudly** instead of
   returning unlocked rows.
8. `avgVarBytes` is fed from a collected `stawidth`-equivalent; the
   `operators_join_agg.go:645` comment is retired and the
   `TestExplainAnalyzeHashJoinReportsGrownBatches` tripwire adjudicated.

A documented, attributed no-go on items 3–4 is a successful outcome (09 §6);
silence is not.
