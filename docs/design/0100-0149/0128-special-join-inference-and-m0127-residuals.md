# 0128 — Special-join inference + M0127 residuals: Implementation Plan (Ralph task breakdown)

| field | value |
| --- | --- |
| status | **planned** — filed 2026-08-07; no task started |
| date | 2026-08-07 |
| milestone | `docs/milestones/0128-special-join-inference-and-m0127-residuals.md` |
| design of record | `docs/design/leftdeep-joins/` for the planner items (**03** §4.4/§6, **07** §5, **09** §3.11/§3.17/§3.19/§3.22/§3.23 — referenced only, never modified) and `docs/design/parallel-query/` for the executor items (**07** §3.1, **10-roadmap** P8 + "Deliberately deferred"). Bitmap heap scan has **no design yet** — P2.2 writes it (`0128-0001-bitmap-heap-scan.md`) before any implementation, per the repo's design-doc-first rule. **This document is not the design authority**; it is an index into the cited sections |
| convention | Tasks are sized for one Ralph loop (one session) completion (`.ralph/PROMPT.md` "ONE task per loop"). Each task lists its gate. Deferral = ledger row + unchecked box; never a silent close. Where a task proves larger than one loop, split it at selection time and record the split here and in fix_plan |
| decomposition source | The M0128 milestone doc's Background/Goals lists (user directive 2026-08-07), mapped onto the bundle chapters' named follow-ups (03 §4.4's "staged follow-ups … implementable now that the bushy phase exists") |

## 1. Positioning

M0127 (PG-shaped join search) closed its implementation stages with P6.3
(2026-08-07): the PG-shaped three-phase DP is the only join-order search,
`MultiHashJoin` and runtime fusion are deleted, and DS05 recorded plan shapes
99/99 identical across the deletion. What remains is everything the bundle
explicitly deferred plus everything its verification measured but did not
scope. This milestone is sequenced **immediately after M0127** (Current
Priority banner, user directive 2026-08-07); M0127-P6.4 (supersession stamps)
is M0127's last open task and is not an M0128 dependency beyond ordering.

**Ordering principle (inherited from 08 §1):** attribution and safety nets
before behaviour change; planner inference before the flag verdict it unlocks;
measurement before executor features whose reopen condition is a measurement.

## 2. Common gate vocabulary for all tasks

Same vocabulary as M0127 (09 §1, binding): **UNITS**
(`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`), **SMOKE**
(pre-commit pgbench hook; never `--no-verify`), **SPOT**
(`scripts/tpch-spotcheck.sh`, Q12=2/Q13=35 canonical, fresh capped server),
**DS05** (`scripts/tpcds-sf05-regression.sh sweep`; zero row/checksum deltas),
**PLAN** (`make plan-diff LABEL=…` against the current re-baseline),
**RACE** (`make race-gate` for shared-state stages), **SIBLING** (sibling-path
audit enumerated in review), plus `make ralph-state-guard` before every
finish. Timed measurements on a quiet host with server age held constant
(sweep-tail discipline). Never `-count=1` in a gate's `go test`. Every
implementation task runs in a git worktree off pinned clean HEAD, staged by
explicit pathspec, re-running its own named guard after any rebase/handoff.

## 3. Task decomposition

### P0 — Attribution and safety first (2 tasks, 1 loop each)

- **M0128-P0.1 — Q74 attribution.** TPC-DS SF0.5 Q74: PASS 11–14 s (the nine
  2026-08-04 sweeps with a Q74 row) → PASS ~81–93 s stable since M0127-P5.9-i
  (first slow sweep 2026-08-05 22:26; `sweep-20260807-122645`: 88 s, 7 rows,
  ck `2ffc13c77bf53028` identical to every pre-regression sweep) — a ~7×
  slowdown
  with correct output, never attributed. Apply 09 §6: bisect across the P5.9
  sub-commits (i is the first suspect by timing, not by evidence), capture
  EXPLAIN (ANALYZE) on both arms, then fix or ledger with a measured bound and
  resume point. Bar: attribution to a specific commit + fix or ledger row;
  DS05.
- **M0128-P0.2 — lockRows hard-error safety net.** Ledger root-0038:
  `lockRowsOp`'s `findScanLeafForRel`/`markJoinPreserveCTID` walkers know ~8
  of ~70 operator types; a `spillOp` (or materialize/memoize/gather/
  indexOnlyScan/lateral…) between LockRows and the scan leaf degrades
  `FOR UPDATE` to an unlocked pass-through **with no error**. This task is the
  audit + the net, not the durable fix: enumerate every operator the walker
  can meet, classify pass-through vs barrier, and make unclassifiable/unknown
  shapes **error loudly** instead of silently unlocking. Resume point
  `internal/executor/operators_lockrows.go`. Bar: UNITS + SPOT + DS05 + a
  regression test that a spilling/interposed shape errors.

### P1 — `join_is_legal`/`SpecialJoinInfo` inference [the 03 §4.4 pin removal] (5 tasks, 1–2 loops each)

The milestone spine. 03 §4.4 is the design authority; PG references are
`postgres/src/backend/optimizer/path/joinrels.c` (`join_is_legal` :350,
`have_join_order_restriction` :1066) and
`postgres/src/backend/optimizer/plan/initsplan.c`
(`deconstruct_jointree`'s SpecialJoinInfo construction).

- **M0128-P1.1 — `SpecialJoinInfo` representation + construction.** Port the
  struct's semantic fields **as PG 18.3 defines them**
  (`postgres/src/include/nodes/pathnodes.h:3031-3053`: min/syn
  lefthand+righthand, jointype, ojrelid, the `commute_above_l/r` +
  `commute_below_l/r` Relids — the 18.3 replacement for the PG≤15
  `delay_upper_joins`, which no longer exists upstream —, lhs_strict, plus
  semi/anti's `semi_can_btree`/`semi_can_hash`/`semi_operators`/
  `semi_rhs_exprs` where goopg's operator inventory makes them meaningful) and
  build them during jointree deconstruction for LEFT/FULL/SEMI/ANTI
  constructs. **Data only —
  no search-behaviour change**; the pinned-opaque rule stays in force until
  P1.2. Bar: UNITS + SPOT + DS05 (zero deltas by construction) + unit tests
  pinning construction for nested special joins (LEFT-over-LEFT, FULL, SEMI,
  ANTI, mixed).
- **M0128-P1.2 — `join_is_legal` + `have_join_order_restriction` for LEFT
  joins.** Replace the reserved constant-false `joinOrderRestricted`/
  `hasJoinRestriction` stubs (`internal/planner/joinsearchlevel.go`; 03 lines
  112/163) with real inference over P1.1's entries — LEFT joins only, per
  `join_is_legal`'s LJO arm (commute within the RHS, delay rules). Outer
  joins stop being blanket-pinned: legal orders enter the DP, illegal ones
  stay excluded, and 03 §4.2's error contract takes its PG condition-only
  form. Bar: UNITS + SPOT + DS05 (zero row/checksum deltas; plan-shape changes
  adjudicated per query — only outer-join queries may change, and only to
  PG-legal orders) + a legality-matrix unit test ported from PG's rules.
- **M0128-P1.3 — FULL joins + outer-join clause distribution.** 03 §6:
  `build_joinrel_restrictlist` for special joins (join clauses vs filter
  clauses on the nullable side), the FULL arm of `join_is_legal`, and the
  identity/dedup rules that keep restricted levels from duplicating joinrels.
  Bar: as P1.2, plus a FULL-join nesting unit test set.
- **M0128-P1.4 — semi/anti in-DP.** 07 §5: unpin the semi/anti spine; SJE
  arms of `join_is_legal`; `semi_can_hash`/`semi_can_btree` consumed by path
  generation. Retires the semi/anti-in-DP ledger row that M0127-P6.4 files
  with the "`join_is_legal`-inference-dependent" marker (0127 plan doc
  convention; the row does not exist until P6.4 lands — this task can only
  close it, and M0128 selection is gated on P6.4 anyway). Bar:
  as P1.2; DS05 semi/anti-heavy queries (Q4/Q21/Q22-class) adjudicated.
- **M0128-P1.5 — `GOOPG_PGSHAPED_COLLAPSE` verdict with the pin removed.**
  09 §3.19's protocol, re-run: with outer links searchable, the two prior
  NO-GOs' premise ("all 12 TPC-DS explicit-JOIN queries contain an outer
  join", so no inner-only chain can ever form while outer joins are pinned)
  no longer holds. Re-measure COLLAPSE ON vs OFF on the TPC-DS corpus — the
  target shape is an outer link buried under inner joins (Q78). Bar: the 09
  §3.19 measurement recorded + flag flipped ON as default or a third
  documented no-go with attribution; DS05 + SPOT.

### P2 — Executor features [M0127 non-goals, per bundle README] (4 tasks)

- **M0128-P2.1 — parallel hash build: the reopen-condition measurement.**
  parallel-query/10-roadmap defers cooperative parallel hash **build** with
  "Reopen when: a measured plan where build time dominates". This task *is*
  the measurement: EXPLAIN (ANALYZE) sweep over TPC-H SF1 + TPC-DS SF0.5 for
  build-dominated hash joins (build time ≫ probe time), on a quiet host with
  server age held constant. If a build-dominated plan exists → record GO and
  decompose the implementation (parallel-query/07 §3.1; gates per 10-roadmap
  P8: identity over the join corpus, RACE, TPC-H Q9/Q17/Q19 measurement) into
  follow-up tasks. If none exists → document the measurement and re-defer
  (ledger row); that is a successful outcome. Note: the leader-serial shared
  build (`internal/executor/parallel_hash_build.go`, M0127-P3.4) already
  exists; this item is only the *cooperative* half. Bar: the measurement
  write-up + verdict; no code gate if NO-GO.
- **M0128-P2.2 — bitmap heap scan: design doc.** goopg has zero bitmap
  machinery (no BitmapIndexScan/BitmapHeapScan/BitmapAnd/BitmapOr, no TID
  bitmap). Write `docs/design/0128-0001-bitmap-heap-scan.md`: PG-faithful
  `tidbitmap.c` (exact/lossy pages), `nodeBitmapIndexScan.c`/
  `nodeBitmapHeapscan.c` analogues, path generation + costing
  (`costsize.c` bitmap paths), index-AM glue, and the EXPLAIN ANALYZE
  exact/lossy heap-block counters. Design-only, status `draft`; README index
  entry in the same commit (hard requirement). Bar: doc review (agent review
  per this milestone's filing precedent).
- **M0128-P2.3 — bitmap executor: TID bitmap + the four nodes.** Per the P2.2
  design. Lossy-page `Recheck` semantics included. Bar: UNITS + RACE + unit
  tests over exact/lossy transitions; the nodes are planner-invisible until
  P2.4.
- **M0128-P2.4 — bitmap planner: path + cost + index glue.** Per the P2.2
  design; path generation under the PG-shaped search, costed in the same
  currency. Bar: UNITS + SPOT + DS05 (zero row/checksum deltas; plan changes
  adjudicated) + PLAN + a TPC-H A/B on the queries PG itself plans bitmap
  paths for.

### P3 — Estimate integrity [09 §3.23] (2 tasks, 1 loop each)

- **M0128-P3.1 — per-column average-width stats → `avgVarBytes`.** ANALYZE
  collects a `stawidth` equivalent (PG feeds `Plan.plan_width` from
  `pg_statistic.stawidth`); the planner threads it to hash-join plan width;
  `buildGeometry` (`internal/executor/operators_join_agg.go:645`) stops
  passing 0 into `internal/hashsize.EntryBytes` (`48·ncols + 24 +
  avgVarBytes`). Retire the `avgVarBytes=0` comment. **Adjudicate the
  tripwire**: `TestExplainAnalyzeHashJoinReportsGrownBatches` was written
  against the under-count and is expected to change verdict once widths are
  real — its disposition (fixed / re-pinned / retired with reason) is part of
  this task. Bar: UNITS + SPOT + DS05 + a unit test that a text-heavy build
  now sizes `nbatch` from real widths.
- **M0128-P3.2 — one scan, one estimate.** 09 §3.23: `seqScanRows` ignores
  on-scan quals, so the executor's hash sizing reads the pre-qual estimate
  while the planner priced the post-qual one. Make the build-side geometry
  read the post-qual estimate (thread the search's filtered count, or apply
  `baserestrictinfo` selectivity in `seqScanRows` — pick per the 09 §3.23
  analysis). Bar: UNITS + SPOT + DS05 + a regression test pinning that the
  executor's chosen `nbatch` tracks the filtered estimate.

### P4 — Outer-join strength reduction [09 §3.22] (1 task)

- **M0128-P4.1 — `reduce_outer_joins`: the reduction half.** Demote an outer
  join to inner when a strict qual above it constrains the nullable side
  (PG `postgres/src/backend/optimizer/prep/prepjointree.c`
  `reduce_outer_joins`). The RIGHT→LEFT flip half is unrepresentable in
  `parser.FromExpr` (Base RangeVar + flat `[]JoinExpr`) and stays out — the
  ledger row records that split. Pessimization fix, never a wrong answer; 09
  §3.22 is the authority. Bar: UNITS + SPOT + DS05 (plan-shape changes only
  where a demotion fires; adjudicated per query) + unit tests for the
  strict-qual demotion matrix.

### P5 — EXPLAIN fidelity [09 §3.11/§3.17] (2 tasks, 1 loop each)

- **M0128-P5.1 — EXPLAIN range-table name dedup.** Port PG 18.3's
  `ruleutils.c` `select_rtable_names_for_explain`
  (`postgres/src/backend/utils/adt/ruleutils.c:3855` — 09 §3.11 cites the
  pre-rename name `select_rtable_names`) so a relation scanned twice
  without an alias prints two distinguishable names. Q2/Q8/Q17/Q18/Q22 become
  adjudicable under acceptance clause 6 (09 §3.11). Bar: UNITS + SPOT + DS05
  plan channel (alias changes only on the ambiguous set) + the five queries
  re-adjudicated under clause 6 with the outcome recorded.
- **M0128-P5.2 — `Rows Removed by Filter` / `by Join Filter`.**
  Executor-side counters (per-scan qual rejects, per-join residual rejects)
  rendered by EXPLAIN ANALYZE in the text format; the structured formats
  (JSON/XML/YAML — currently no qual properties at all, 09 §3.17) are in
  scope if the property plumbing is mechanical, else ledgered from this task.
  Bar: UNITS + SPOT + DS05 + an EXPLAIN golden test showing both line kinds.

### P6 — Durable row-lock identity (1 task, decompose at selection)

- **M0128-P6.1 — resjunk-ctid rowmark (durable lockRows fix).** PG's
  mechanism: `preprocess_targetlist` adds junk `ctid` attributes for rowmarked
  relations, so TID identity rides the tuple, not the operator-tree shape —
  immune to spill/materialize/memoize/gather interposition by construction
  (ledger root-0038, resume point
  `internal/executor/operators_lockrows.go`). Expected to decompose at
  selection (planner junk-attr plumbing; executor slot carriage; the eight
  existing walker arms' retirement; P0.2's hard-error net becomes unreachable
  and is removed). Bar at completion: UNITS + SPOT + DS05 + an isolation
  `FOR UPDATE` test over an interposed shape + the root-0038 ledger row
  closed.

## 4. Dependency and ordering notes

- **P0 before everything** (P0.1 is a live 7× regression on a gate-corpus
  query; P0.2 is a silent wrong-answers-class safety net). P0.2 is also the
  precondition for any later claim that `FOR UPDATE` is safe while P6.1 is
  open.
- **P1.1 → P1.2 → P1.3** strictly; **P1.4** needs P1.2 (the inference
  machinery) but not P1.3; **P1.5** needs P1.3 (the Q78 shape is an outer link
  under inner joins — FULL/LEFT legality must exist before the collapse can
  be measured honestly) and is the milestone's flagship verdict.
- **P2.1 is one measurement loop** and gates only its own follow-ups; P2.2 →
  P2.3 → P2.4 strictly (design-doc-first rule).
- **P3.1 before P3.2** (both touch `buildGeometry`'s inputs; landing the
  width stat first isolates the estimate-source change).
- P4.1, P5.1, P5.2 are independent of each other and of P1; P5.1 should land
  before any task that re-adjudicates ambiguous plans (P1.2's DS05
  adjudication benefits but does not depend).
- No M0128 task may be selected while M0127-P6.4 is open (Current Priority
  banner: M0128 follows M0127).

## 5. Evidence conventions

As M0127: sweep artefacts under `bench/tpcds/runtime_goopg/tpcds-results-sf05/`,
analysis write-ups under `analysis/` with the task id in the path, ledger rows
for every deferral (7-column format), and tombstone comments — never silent
deletions — for anything removed. DS05 results record as "57/99
content-verified, 42/99 count-only" where counts are cited.

## 6. Progress log

(newest first; one row per landed task, mirroring the M0127 plan's record
table)

| date | task | result |
| --- | --- | --- |
| 2026-08-07 | — | milestone filed (docs + fix_plan + indexes); no task started. **Agent-reviewed at filing** (fact-check pass over every citation): findings reflected — P1.1 ports the PG 18.3 `SpecialJoinInfo` field set exactly (`commute_above/below` replace the PG≤15 `delay_upper_joins`, which no longer exists upstream); P1.4's ledger-row reference reworded (the row is written by M0127-P6.4, not yet present); `select_rtable_names` corrected to 18.3's `select_rtable_names_for_explain` (the stale name is the bundle's own, 09 §3.11); Q74's pre-regression range corrected to 11–14 s (nine 2026-08-04 sweeps); P5.1/P5.2 titles aligned with fix_plan; the parallel-query/10-roadmap "Parallel `MultiHashJoin`" deferred row marked moot (MHJ deleted by M0127-P6.2) |
| 2026-08-07 | P1.1 | `SpecialJoinInfo` representation + construction landed (ea797e7d): PG 18.3 fields (`commute_above_l/r`, `commute_below_l/r` Relids, `lhs_strict`, `semi_*`) built during `deconstructJointree` for LEFT/FULL/SEMI/ANTI; pin stays. UNITS PASS, SPOT PASS (Q12=2/Q13=35), DS05 zero deltas. |
| 2026-08-07 | P1.2 | `join_is_legal` + `joinOrderRestriction` + `hasJoinRestriction` for LEFT joins landed (f1ed2de5): replaces constant-false stubs with real inference over P1.1's entries; PG-legal orders enter DP, illegal excluded. UNITS PASS, SPOT PASS, DS05 zero deltas. |
| 2026-08-07 | P1.3 | FULL joins + `buildJoinRelRestrictList` landed (02d9d49a): `build_joinrel_restrictlist` for special joins (join vs filter on nullable side), FULL arm of `join_is_legal`, identity/dedup for restricted levels. UNITS PASS, SPOT PASS, DS05 zero deltas. |
| 2026-08-07 | P1.4 | Semi/anti in-DP landed: `JoinSemi`/`JoinAnti` AST constants, `joinIsLegal` SEMI/ANTI arms (unique-ified skip), `semiQualCapabilities`, `semi_can_hash`/`semi_can_btree` optimistic population. UNITS PASS, SPOT PASS. |
| 2026-08-07 | P1.5 | **COLLAPSE verdict — third NO-GO, with a progression.** Protocol per 09 §3.19 re-run post-P1.3: DS05 plan capture COLLAPSE ON vs OFF → `same=97 changed=2` (Q72, Q75). Focused DS05 sweep ON → `PASS=2 MISMATCH=0` (both correct). SPOT PASS. **The flag works correctly**, changing plans only for the same two queries the outer-spine peel already handles (09 §3.20). The remaining blocker is unchanged: `TestNoCorpusQueryHasAnInnerOnlyJoinChain` reports zero INNER-only chains in either corpus — the flag's unique value (flattening explicit INNER/CROSS chains) is still untested by production data. **Verdict: `GOOPG_PGSHAPED_COLLAPSE` stays default OFF.** This is not a no-op NO-GO — it is the first measurement where the flag actually moved plans and those plans were correct. The condition to re-test is still `TestNoCorpusQueryHasAnInnerOnlyJoinChain` going red (a measurable INNER-only chain appears), OR Q78's buried outer-link shape becomes reachable through `SpecialJoinInfo` reordering (03 §4.4 — not yet; the outer join is still pinned by `joinPinned`, not merely constrained). Artefacts: `plans-20260807-{182007,182046,182124}.txt`, `sweep-20260807-182124.txt`. |
| 2026-08-07 | P3.1 | **per-column average-width stats → `avgVarBytes` landed.** ANALYZE `computeColumnStats` now computes `AvgWidth` from sampled non-null datums via `datumVariablePayloadWidth`; `ColumnStats.AvgWidth` is written as real `stawidth` to pg_statistic (replacing placeholder `8`), restored at startup, and projected through the pg_stats view. The planner sums per-column `AvgWidth` into `RelOptInfo.AvgVarBytes` (base rels from stats, join rels as sum); `hashJoinCost` passes it to `hashsize.Choose`; `createHashJoinPlan` stores it on `Join.AvgVarBytes`; `buildGeometry` reads it instead of hardcoding 0. The tripwire test `TestExplainAnalyzeHashJoinReportsGrownBatches` adjudicated: it remains the no-stats safety net (fixture has no ANALYZE, so `AvgVarBytes`=0 and growth still fires). New unit tests: `TestDatumVariablePayloadWidth` (13 kinds) and `TestAnalyzePopulatesAvgWidth` (direct `computeColumnStats` call). UNITS PASS, SPOT PASS (Q12=2/Q13=35), DS05 PASS (95/99, zero row/checksum/plan deltas). |
