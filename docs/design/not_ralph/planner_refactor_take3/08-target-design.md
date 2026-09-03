# 08 — Target design (take3 synthesis)

How goopg gets from "a PG-shaped join search grafted into a rule-based
rewriter" to "PostgreSQL's planner, reimplemented in Go, that selects
PostgreSQL's plan for the same query on the same data" — updated for what
take2 actually landed, reverted, and ruled out.

Read [07-gap-analysis.md](07-gap-analysis.md) first for the evidence this
design responds to, and [09-verification-and-acceptance.md](09-verification-and-acceptance.md)
for how each step is measured. Take3 [01](01-pg-planning-pipeline.md)–[03](03-pg-statistics-infrastructure.md)
are the oracle; [04](04-goopg-planning-pipeline.md)–[06](06-goopg-statistics-infrastructure.md)
are the current state. This document cites them and does not repeat them.

---

## 0. Thesis

**Make the Path/RelOptInfo search the only planner, feed it PostgreSQL's
statistics and PostgreSQL's cost inputs, and delete everything that plans
around it.**

Take2 proved the thesis is reachable in slices: the search now prices with a
per-statement `PlannerSettings` (P2-01/P2-02), counts `DisabledNodes` for
join methods (P2-05), rescans correctly (P2-07), costs merges on
`mergejointuples` (`c281b0830`), pairs MCVs on the inner join (P1-15) and
ranges per variable (P1-13), restores histograms across restarts
(`f07c20b1f`), and reports real costs in EXPLAIN (P0-02/P0-03) — take3 04
§0, 05 §1, 06 §0. Take2 equally proved where the slices stop: the search
still sees an inner-join prefix (07 §3.1), carries whole tuples (07 §3.2),
and plans subqueries under defaults (07 §3.3); everything above FROM and all
of parallelism is still rules and a post-pass (07 §3.6–§3.7).

Each phase below widens what the search owns. The order is deliberate and is
defended in §2.

### 0.1 Non-goals

- **Time parity on its own.** TPC-H Q6 runs the node-for-node PG-identical
  plan and still takes 23.40 s against PG's 0.99 s serial (07 §4.1, §6). The
  per-row executor tax is catalogued in 07 §6 and out of scope here. This
  bundle targets plan parity (09 bars A1–A5) and the time movement that
  follows from it.
- **Re-litigating settled no-gos.** Multi-way hash join as a plan node,
  runtime join fusion, cost-driven join order over the old integer DP,
  penalty multipliers, NDistinct-standalone, lone bitmap double-charge
  removal — all closed with measurements (07 §5).
- **A new cost currency.** PostgreSQL's constants are the target, unchanged
  (take3 02 §1.1, §9 checklist items 1–5).

---

## 1. Design principles

Constraints, not preferences. Each was paid for (take2 §4.3, carried).

**P1 — PostgreSQL is the specification, including its mistakes.** Where
`selfuncs.c` approximates, goopg reproduces the approximation (take3 02 §7,
03 §5–§7). A better estimate that produces a different plan is a failure of
this project even when it is a better estimate. Deviations require a
committed measurement and a ledger row (09 §4.3 C7 + §4.4).

**P2 — Structure before calibration.** Every attempt to fix a slow query by
moving a cost term has failed: inferred-edge penalties (M0076), accurate
NDistinct standalone (Q5 +42%), MHJ + 100× materialisation penalty, >2M-row
build penalty (Q5 8.15 s → 600 s+) — 07 §5. Every win came from structure:
the right search space, the right candidate set, or a missing statistic.
When a query is wrong, ask which candidate was never generated (09 R4)
before asking which term is mispriced.

**P3 — No query-specific forcing, no penalty multipliers, no shape
preferences.** Established by `cost-model/15` and confirmed by M0126 (07
§5): threshold penalties make the search dodge the penalised operator by
routing work through extra passes.

**P4 — Sibling paths move together.** The two cardinality estimators, the
(now-collapsed) resolver arm list, the two NLI routes — until Phase 6
removes them (take3 04 §13 item 24; take3 06 §1.5: P1-26 collapsed
`columnStatsForChild` into one arm list so drift is impossible rather than
discouraged). A change to one twin that leaves the other is a defect even
when tests pass.

**P5 — One variable per commit, enforced by sequencing.** M0126 retired MHJ
before the order flip because one env var moved both (07 §5). Any item that
would change two planner inputs at once is split. Sibling rule: cancelling
pairs (bitmap lossy + double charge, take3 05 §3.6) move in *one* commit —
one variable, both halves.

**P6 — Every deferral gets a ledger row.** `.ralph/deferral_ledger.md`,
7 columns, upstream citation, concrete resume point. Take2 closed P7-03 with
11 rows (`take2-*`, take3 04 §0 note); new deferrals follow the same format.

**P7 — A plan-shape change is timed on both suites.** 09 R1/R2: TPC-H SF=1
and TPC-DS SF0.5, fresh server per arm. The per-query 2× gate is necessary
but not sufficient — the sweep TOTAL arm is complementary (07 §7 item 6).

---

## 2. The central structural decision

### 2.1 What "one planner" means here

PostgreSQL's planner is a single pipeline: `subquery_planner` preprocesses,
`query_planner` builds `RelOptInfo`s and searches, `grouping_planner` adds
upper-rel paths, `create_plan` translates the winner (take3 01 §2–§4, §11).
Every decision affecting the output is a **path choice priced by a cost
function**. goopg's is two pipelines joined at `tryJoinSearch`
(`joinsearchseam.go:168`; take3 04 §0). The target is PostgreSQL's: one
pipeline where the only thing that decides a plan is `addPath`
(`path.go:600`, now with `producer` + `DPPATH` provenance; take3 04 §6, §8).

Reached by **growing the search's coverage until the legacy path is
unreachable, then deleting it** — not by rewriting the planner in one
motion. Each widening is gated by a plan diff plus timings. The legacy tree
stays as fallback until Phase 6, when it is removed because nothing reaches
it. Deletion attempts before that point fail by measurement (P6-03 Q20 6.5×,
P6-04 Q4 12.5×; 07 §3.8) — the search must first select those shapes on its
own merits.

### 2.2 The seam, precisely (updated)

Take2's two-mechanism account holds at HEAD (take3 04 §0, §4.1, §4.4; 07
§3.1): the collapse default (n-way explicit JOIN → n−1 two-relation
problems) and the outer-join peel + whole-statement decline
(`splitOuterSpine`, `extractSearchLeaves` INNER/CROSS-only,
`makeRelFromJoinlist` pinned-outer decline). A third decline (mixed comma +
`ON`-join predicated inner link at non-zero offset) holds **[carried]**
(take3 04 §4.4). New since take2: the seam now applies
`inferEquivClassConstants` (constant half only, 470× probe win; take3 04 §0,
§4.3); `RelSet` is `uint32` / `maxSearchRels` 32 (P3-09, capability not plan
change); GEQO all-seven-knobs per-statement but unreachable in practice
(take3 04 §6); sub-problem collapse now named as P2-02b's co-blocker
alongside width (take3 04 §6).

### 2.3 Why the phases are ordered this way

The take2 objection stands (take2 08 §2.3): a wider search is not safe until
the numbers ranking it are right (Round-4: ANALYZE fixed Q5 23× and broke
five queries). Take3 refines the ordering with take2's measurements:

- **P0 instruments first** — without EXPLAIN costs (landed P0-02/P0-03) and
  a node-level goopg-vs-PG diff (open P0-05/06/07), no later phase is
  falsifiable. P0 changes no planner behaviour (`changed=0`).
- **P1 statistics next** — but judged by the **estimate ratchet, not
  per-item timing**: three A/Bs showed absent-stats restoration moved time
  (−10.5%) while inaccurate-stats refinement did not (P1-13 +0.45%,
  P1-14/P1-25 +0.88%; take3 06 preamble). P4-01 is promoted ahead of the
  P1 tail and all of Phase 3 on the width evidence (take2 TODO priority
  change; 07 §3.2) — the binding constraint is on the **cost** side.
- **P2 cost inputs** — plumbing largely landed; remainder is the BootVal
  correction (ordered after P0-12 + P4-01 + propagation), the SAOP path gap,
  the MCV-frequency half, and the `disabled_nodes` remainder (§5).
- **P3 search coverage**, now that ranking is trustworthy — with P3-04
  explicitly unblocking P1-18 (take2 TODO P1-18; 07 §3.1). **Ordering bar
  (§10.7): P3-02/03/04 do not start before P4-01 lands** — P3 ranks a
  wider candidate set, and ranking it with the known 14–39× width
  overcharge (07 §3.2) is the exact failure this section forbids. P4-01
  is necessary but not sufficient (take2 `FINDING-p401-alone-is-not-enough`:
  narrowing alone takes `orders` 128→64 batches, not 1; 48 B/column
  co-dominant) — which is why P3 must not run on pre-P4-01 numbers.
- **P4 upper planner (PathTarget first)**, **P5 parallel**, **P6 deletion**.

---

## 3. Phase 0 — Instruments (complete what take2 started)

Take2 landed P0-01, P0-02, P0-03, P0-04b, P0-04c, P0-04d, P0-09, P0-11,
P0-12 (take3 04 §0). Remainder:

**P0-05/06/07 plan-parity capture/diff/baseline.** New capture/diff mode
covering both suites with the PG side committed as a fixture (like the SF0.5
oracle) so the diff runs without live PG. Comparison over the normalised
plan tree (node type incl. `Parallel` prefix, relation/index, join
type/method, sort/agg strategy, child order); costs/rows/widths/times in a
separate column. Verdicts `MATCH` / `SHAPE-DIFF` / `MISSING-NODE` / `ERROR` /
`TIMEOUT`; nine-category taxonomy (`join-order`, `join-method`,
`scan-type`, `parameterisation`, `aggregation-strategy`, `sort-strategy`,
`parallelism`, `qual-placement`, `rendering`); declared normalisation policy
(strip PG's standalone `Hash` nodes — executor structure, not planning
choice; take2 09 §3.1). Committed artifacts
(`bench/tpch/plans-pg/`, `bench/tpcds/plans-pg/`); mode report-only with a
pinned mismatch budget. R3 applies to the instrument itself: renderer arm
per node type, test fails on new node type without EXPLAIN arm. Full spec:
take2 09 §3.1.

**P0-04 EXPLAIN suffix numbering.** Align `explain_names.go` dedup with
`select_rtable_names`, then re-measure spine `shape_mismatches` = 46 (07
§2.2; take3 04 §11). P0-04e (JSON `Project`/`Filter` wrapper collapse) is
tree-shape, not estimate — does not block the parity instrument; schedule
with P0-04.

**P0-08 stale pin.** Three coordinated edits in one commit: commit
`plan_snapshots/take2-p0-<date>.txt`; update `stage-tpch.sh:234`'s `LABEL=`;
update `summarize.py:689` (take2 TODO P0-08 correction: nightly calls
`plan-diff`, not `plan-gate`; `plan-gate`'s `ls -t` selects
`warm-stats-base.txt`, not `m0077-final`).

**P0-13 collapse flip as positive control.** Pre-registered blast radius
(TPC-H `changed=0`, exactly Q72/Q75 moved; take3 04 §0): an instrument
reporting anything else measures itself wrong. Re-opens the voided NO-GO
(take2 §4.2; take3 04 §0). Zero dependency on P1/P2. Gate: parity both
suites + Q72/Q75 timing.

**P0 exit:** parity instrument produces a committed baseline roll-up for
both suites (bars A1/A2 set as `baseline + N`, 09 §4.1), and `PP changed=0`
against pre-P0 goopg capture — P0 moves no plan.

---

## 4. Phase 1 — Statistics fidelity (finish the tail; ratchet, not timing)

Target: the planner reads what PG's planner would read (take3 03 oracle).
Take2 closed P1-01, P1-03, P1-03b, P1-05, P1-07, P1-08, P1-11b, P1-11c,
P1-12, P1-13, P1-14, P1-15, P1-19, P1-20, P1-25, P1-26, P1-28, P1-29,
verified P1-04, P1-09, P1-17 satisfied, declined P1-06, blocked P1-18,
P1-21 (take3
06 §0). Remainder, in landing order:

**P1-11 TOAST.** Wide-text histograms dropped + size-row loss (`orders`,
`customer`, `partsupp`). **Re-measure first** post-`f07c20b1f` — the wide
case "may now behave differently" (take3 06 §2.11). Acceptable interim:
bounded-width with ledger row. Gate: full regress (catalog format),
re-init data dir.

**P1-14b remaining estimators.** General `scalararraysel`, `patternsel`
(LIKE/regex beyond the access-path prefix), `rowcomparesel`,
`booltestsel`, `var_eq_non_const`; align every `DEFAULT_*` with take3 03
§5.1 (`DEFAULT_EQ_SEL` 0.005, `DEFAULT_INEQ_SEL` 1/3, range-ineq 0.005,
`DEFAULT_NUM_DISTINCT` 200, unk/not-unk 0.005/0.995). Judge by estimate
ratchet.

**P1-16 Q9 re-diagnosis.** Recorded single-`nd` explanation retired (take3
06 §7.7); close ledger rows 779/781/784 as stale; file what
`estimate-audit` actually shows on the final joinrel. Gate: audit bar on Q9.

**P1-22/23/24 extended statistics.** Build during ANALYZE (ndistinct,
dependencies, MCV into `pg_statistic_ext_data`), then
`statext_clauselist_selectivity` + `estimatedclauses` bitmap, dependencies
and MCV selectivity, `choose_best_statistics`,
`statext_is_compatible_clause` (take3 03 §9.2–§9.3), then
`estimate_multivariate_ndistinct` for GROUP BY (take3 03 §8.1). Largest new
subsystem in Phase 1; may split; required because correlated predicates are
where independence fails and TPC-DS is built from them. Note PG's
NullTest-MCV-only rule (dependencies has no NullTest branch; take3 03 §9.3).

**P1-27 CTE-agg.** Plain-CTE case already resolves (take3 06 §8.5); gap is
**aggregated** outputs (`year_total` → 0.005/conjunct, same as PG) +
goopg's helping `rows ≤ 1` guard. Replacing the guard needs genuinely
derived statistics (propagation through aggregation); removing it without
them regresses. Larger than worded; keep open with ledger row if deferred.

**P1-10 expression-index stats** (`compute_index_stats`; take3 06 §2.6–2.8)
and **P1-02 remainder** (tree height via `_bt_getrootheight` analogue;
partial-index `indexTuples`; take3 05 §3.5b). Two commits: P1-02 first,
then P1-10 (one variable per commit, P5 §1).

**P1-18 outer/semi/anti sizing.** Port `calc_joinrel_size_estimate`'s
jointype switch (take3 02 §6) — but only after P3-04: the probe reports
`nrels=1, pairs=0` (take2 TODO P1-18; 07 §3.1), so before the search can
see a non-inner join the switch is dead code. Designed here, executed
with Phase 3 (§6.2).

**P1-21 `max(outer,inner)` fallback cap — KEEP.** The cap sits in the
unmeasurable fallback (`cardinality.go:655-666`) which P1-15 cannot reach
(take3 05 §6.2; take2 TODO P1-21): the deletion precondition is not met,
and deleting it now inflates big inputs with no evidence. Verify-and-ledger
as a keep decision, one commit.

**P1-30 endpoint probe + MCV widening** (`get_variable_range`,
take3 06 §4.3: histogram-endpoint drift for monotonic keys) and **P1-31
text scalars** (`convert_string_to_scalar` + network variants, take2 TODO
P1-11b still-open half; take3 06 §5.3/§12.3). Judge both by the estimate
ratchet, not timing.

**P1 exit:** estimate ratchet monotone (09 bar B), parity mismatch budget
does not grow, no query >1.2×, S-cold/WARM gap narrows (take2 09 §6.5 arms
c2/w2 as reference).

---

## 5. Phase 2 — Cost-model completeness (close the remainder in order)

### 5.1 P2-02b `work_mem` BootVal 512 MB → 4 MB — ordered after P0-12 + P4-01 + propagation

State: bench clusters aligned (P0-12, +62% honest control; take3 04 §12.4).
P2-02b purely performance post-`13d53603f` (+23.1%, entirely Q9+Q7; values
correct, 24 MATCH; take3 04 §12.4). Split in isolation: width ~87% / lost
Gather ~13% (take2 `impl/MEASUREMENT-p202b-width-vs-gather.md`; 07 §3.2).
Also requires settings propagation (07 §3.3 + 04 §12.3:
`planSelectWithParent` still re-defaults; take3 04 §12.3) — and propagation
itself must land *after* P4-01 width is fixed or each slice pays the same
regression (take2 FINDING-planner-settings CONSEQUENCE). Lands alone, both
suites timed, expect plans to move. Gate: `TestSampleConfigCoversRegistry` +
`hashsize.DefaultMemLimitBytes` trio moves together (GUC BootVal,
`postgresql.conf.sample`, executor fallback — take2 TODO P2-02b note).

Propagation is three slices, one caller family per commit, each after
P4-01 (§10.1): **P2-02d** derived-table (`(SELECT …) AS alias`) sites,
**P2-02e** set-operation operands, **P2-02f** scalar-subquery sites
(07 §3.3; take3 04 §12.3). The reverted mechanical attempt threaded from
the wrong scope — hand-thread only, one caller at a time.

### 5.2 SAOP (P2-09a path, then P2-09b cost batch)

`num_sa_scans` blocked on a missing *path*: no ScalarArrayOp index path
(`IN` → seq scan + filter; take3 04 §5, 05 §3.3). **P2-09a** builds the
path first (index `= ANY` with `num_sa_scans` descents + `ceil(pages/3)`
clamp, take3 02 §3.4), gated on path existence/shape on IN-list fixtures;
**then P2-09b** lands the **batched** `btcostestimate` remainder (descent
confirmed present; per-tuple qual cost reverted +3.3% lands with the batch;
take3 05 §3.4) with acceptance on the **aggregate sweep TOTAL**, not the
per-query gate (07 §7 item 6).

### 5.3 MCV-frequency half (P2-11b; P2-11 landed the ndistinct half)

`estimateHashBucketSize` covers the ndistinct fraction (take3 05 §5.7);
plumb the inner key's MCV list to the cost site for the skew term (take3 02
§5.4: `estimate_hash_bucket_stats`, clamp [1e-6,1]). Per-orientation
closures as landed. Gate: TPC-H best-of-bundle discipline + TPC-DS
aggregate; plan-change attribution before believing per-query moves (07 §7
item 7).

### 5.4 `disabled_nodes` remainder (P2-16a…e, one family per commit)

Join methods live (P2-05); port the remaining setters per take3 02 §1.2 in
one commit per family — **P2-16a** sort + material, **P2-16b**
agg-hashed/mixed, **P2-16c** gather-merge — each with its GUC-effect test
(09 §5 P2 row). **P2-16d** retires producer-skipping for scans where PG
counts instead of gating (hard gates stay gates:
index-only/TID-except-CURRENT-OF/memoize/incremental-sort; take3 01 §12,
02 §1.2). **P2-16e** retires `enable_nestloop_index` (no upstream
counterpart) once NLI is an ordinary parameterised-nestloop path (§6.4),
with its own gate. This makes the family A/B-able during later phases.

**P2 exit:** every cost GUC changes a plan (09 P2 row; P2-02's remaining
four need index/parallel fixtures — file, don't claim); parity budget does
not grow; no query >1.2×.

---

## 6. Phase 3 — Search coverage (the structural phase)

### 6.1 Name → relid resolution (P3-01 prerequisite)

`makeSpecialJoinInfo` (`specialjoin.go:54`, called from `collapse.go:416`) runs before Vars carry relation
indexes; `parser.ColumnRef` carries names (take2 TODO P3-01). Two routes:
thread a name-to-leaf map from FROM items into deconstruction (smaller,
keeps phase order), or deconstruct after resolution (what PG effectively
does; take3 01 §4.1: Vars carry `varno` by `deconstruct_jointree` time).
Either is unobservable until §6.2 consumes it — P3-01 alone moves no plan.
Partial fix **unsafe** (underestimate = wrong answers; fall back to `syn`
on any uncertainty).

### 6.2 Jointree / SJI / `join_is_legal` (P3-02/03/04)

Port `deconstruct_jointree` (PG16+ OJ relids, `varnullingrels`, clone
clauses), `make_outerjoininfo` (full field set incl. `LhsStrict`,
`Commute*`, `Ojrelid`, `SemiOperators`/`SemiRhsExprs`), `distribute_qual_to_rels`
with `check_outerjoin_delay` (placing, not copying — supersedes
`pushInnerJoinInputQuals` copying, ledger 802), and `join_is_legal`
consulting real SJIs so the DP builds outer/semi/anti joinrels directly
(take3 01 §4.1–§4.2, §6). Delete `splitOuterSpine` + `pinnedOuter()`
decline (P3-04); mixed comma + `LEFT JOIN` becomes **one** search problem;
TPC-DS Q72 is the witness. **P3-04 unblocks P1-18**: port
`calc_joinrel_size_estimate`'s jointype switch (INNER/LEFT/FULL/SEMI/ANTI
rows table, take3 02 §6) only after the search can see a non-inner join —
before that it is dead code (take2 TODO P1-18).

### 6.3 Collapse retirement (P3-05), pathkeys (P3-06), parameterisation (P3-07/08)

- P3-05: retire `GOOPG_PGSHAPED_COLLAPSE` once P3-04 makes it the only
  jointree path; `from_collapse_limit`/`join_collapse_limit` take upstream
  meaning incl. `= 1` preserves order (take3 01 §12).
- P3-06: `standard_qp_callback` analogue (`query_pathkeys = group ?:
  window ?: longer(distinct, sort) ?: setop`; take3 01 §4.4) on the planner
  context (§8 sequencing: needs P2-01, landed); complete
  `has_useful_pathkeys` so ORDER BY/GROUP BY motivate index paths (take3 04
  §9).
- P3-07: `param_source_rels` (hard-coded 0; take3 04 §7) with
  `allow_star_schema_join` semantics (take3 01 §7).
- P3-08: `reduce_unique_semijoins` after clearing SEMI left-only `Output()`
  re-indexing (ledger 794).

### 6.4 NLI as an ordinary path; pre-search heuristic deletion

Retire `rewriteJoinsToNLI` once `addNLIPaths` covers its cases — P3-11
showed the search loses narrowly (0.05%–12%; 07 §2.2), so deletion waits
for the search selecting NLI on its merits (remaining `btcostestimate` +
hash terms), not for a targeted NLI adjustment (which at these margins is
benchmark-tuning). Delete `reorderCommaFromByCardinality` remainder once
§6.2 lands, own commit, both suites timed (behaviour already gone per P3-12;
take3 04 §2).

**P3 exit:** every PG-only join spine OFFERED at its level (DPPATH
per-producer OFFERED/ACCEPTED, take3 04 §6) or named reason via
`estimate-audit --enum-trace` (09 §2.1); Q72-class queries (mixed comma +
`LEFT JOIN` FROM lists; Q72 witness, full list enumerated by the
enum-trace run and recorded in the phase verdict) one search problem;
`join-order` diffs strictly decrease.

---

## 7. Phase 4 — Upper planner as paths (`PathTarget` first)

**P4-01 `PathTarget` + projection first** — the width root cause (07 §3.2).
P4-01 executes before Phase 3 (§10.7). Per-path targets
(`make_group_input_target`, `make_window_input_target`,
`make_sort_input_target`, `apply_scanjoin_target_to_paths`; take3 01 §3)
with `setrefs`-style fixup at `create_plan` time. P4-01b's lesson is binding:
narrowing the leaf's `Output()` without moving the coordinate space returns
wrong answers (Q2/Q5 0 rows, Q18 wrong tuples); the projection must be a
path property with fixup, not a leaf swap (take2 TODO P4-01b; take3 04 §3).
Gate on **values** (`tpch-runner -digest` + `-diff`, all items — row counts
passed Q18 broken), `NBatch` 2→1 on the witness (TPC-H Q9 hash build:
EXPLAIN `Batches:` at `work_mem` 64 MB, S-cold, 09 §6 header), `DPPATH`
join.hash total below mergejoin's 754717 (take2 `impl/P4-A-pathtarget.md`
§8), plus both-suites PP + timing (09 §4.3 C3). Threshold correction: goopg
narrowed width is ≈100, not PG's 6 (declared NUMERIC keys; P4-A §8 R2).

Then, in order: P4-02 upper `RelOptInfo`s (`GROUP_AGG`, `WINDOW`,
`DISTINCT`, `ORDERED`, `FINAL`; take3 01 §3); P4-03 promote `PathSort` to a
real upper-rel path (has arm, only merge-child today; take3 04 §3); P4-06
`create_grouping_paths` sorted+hashed+spill priced by `cost_agg` (take3 02
§4.4), retiring the three grouping rules; P4-07 `create_distinct_paths`
(depends on P1-25, landed); P4-04 bounded/top-N sort (`limit_tuples` arm —
largest recorded `ORDER BY … LIMIT` win); P4-05 Incremental Sort
(`create_incremental_sort_path`) — BLOCKED, no executor counterpart
exists: resume after executor support (ledger `take2-P2-08` class),
excluded from the A3-closure promise until then; P4-08 `tuple_fraction` end-to-end (take3 01 §1:
fraction ≥1 ÷ rows, parameterised excluded, `disabled_nodes` first);
P4-09 window + set-op paths. Grouping sets (`groupingsets.go` interaction),
`remove_useless_joins`, `reduce_outer_joins.go` interaction, and
FROM-subquery pull-up coverage each need a scoped item before the phase
starts (P4-00; take2 08 §11.1 — still unscheduled).

**P4 exit:** `aggregation-strategy` / `sort-strategy` diffs strictly
decrease; no correctness delta (values, not counts).

---

## 8. Phase 5 — Parallelism in the path model (costed, not forced)

`consider_parallel` per rel (P5-01); `create_plain_partial_paths` populating
`PartialPathlist` (`computeParallelWorkers` moves into generation) (P5-02);
eligibility as property not arm list — P5-03 extends `drivingScan` to plain
`IndexScan`/bitmap (index-only already admitted; Parallel Index Scan counterpart; take2 §3.6;
take3 04 §10); `generate_useful_gather_paths` (`Gather`/`Gather Merge`
priced by `cost_gather`/`cost_gather_merge`, take3 02 §3.6) with
`createPlanNode` arms (P5-04); parallel hash as `parallel_aware` path (P5-06,
executor counterpart the parallel hash build/probe,
`parallel_hash_build.go`, with a consumer check in the gate); partial agg
paths replacing `splitAggregate` (depends on P4-06) (P5-07); re-decide
`Gather Merge → Sort → Parallel scan` by **cost** (P5-05; record as permitted
divergence with the committed q16/q10/q13 measurement if leader-side still
wins; take2 §8); retire `MaybeAddGather` (P5-08). Note the ordering trap
measured early: at small budgets the plan moves onto index-driven joins the
old post-pass cannot drive — Phase 5 recovery is downstream of plan shape,
which is downstream of width (take2 MEASUREMENT-p202b §Finding; 07 §3.2).

**P5 exit:** `parallelism` diffs strictly decrease; serial control arm
unchanged.

---

## 9. Phase 6 — Single-planner deletion (with must-not-delete oracles)

1. **P6-01** one cardinality estimator: delete legacy
   `estimateJoin`/`EstimateRows` + `joinkeyproof.go` mirror; everything reads
   `calcJoinrelSize`. Prerequisite: EXPLAIN `rows=` from the path (P0-02
   remainder) + legacy consumers gone (P4).
2. **P6-02** `PathTarget` + range table replacing `baseLeaf`/`baseOffset`;
   delete `joinlayout.go` remapping + `createplanroot.go` boundary
   assertions. Deletes the largest silent wrong-answer class (Q8/Q9 0-rows,
   M0077 type mismatch). Gate: value-level diff, never counts (risk R5).
3. **P6-03/P6-04 deletions stay blocked** until the search selects those
   shapes on its merits (Q20 6.5× correlated-SubPlan degeneration; Q4 12.5×
   semi-join; 07 §3.8). **P6-05 must NOT be done**: `reconcileNLILayout` is
   the oracle for the live `assertSearchedTreeNeedsNoReconcile` tripwire on
   every searched plan (take3 04 §2.2) — deleting it removes the check, not
   the code. If the pass is ever removed, replace the oracle first.
4. **P6-06** retire flags one per commit (P6-06a, P6-06b, P6-06c, P6-06d,
    P6-06e:
    `GOOPG_INDEXKEY_HARVEST` (a), `GOOPG_INDEX_PROBE_MULT` (b),
    `GOOPG_HASH_OUTER_JOIN` (c), `GOOPG_NLI_COSTGATE` (d), `GOOPG_PGSHAPED_DP`
    (e) last), regenerating `planner-flags.env` each time, each with a
    before/after parity roll-up + timing table. (`GOOPG_RELSIZE_FALLBACK`
    staging already retired take2-P1-05; `GOOPG_PGSHAPED_COLLAPSE`
    retires once in P3-05, not here.)
    `GOOPG_HASH_OUTER_JOIN` measured safe (CKMISMATCH=0) but a wash (+1 s) —
    not flipped; re-measure after the `btcostestimate` batch (P2-09b;
    take2 TODO P6-06). `GOOPG_PGSHAPED_DP`'s off path unretirable while
    legacy rewrites are load-bearing.
5. **P6-07** `setrefs` phase if P6-02 shows the executor still needs
   explicit resolution.
6. **P6-08** `RestrictInfo` caching (planning-speed, not plan-quality;
   filed here per the P1-12 record).

**P6 exit:** byte-identical plans to pre-deletion arm on both suites, or
every difference explained and timed.

---

## 10. Sequencing constraints (explicit)

1. P2-02b after P0-12 (landed) **and** P4-01 **and** the P2-02d/e/f
   settings-propagation slices (§5.1; 07 §3.3 + 04 §12.3); each
   propagation slice after P4-01 (take2 FINDING CONSEQUENCE).
2. P1-18 after P3-04, executed with Phase 3 (take2 TODO P1-18);
   P2-08/P2-10 after Phase 3/4 consumers exist (take3 05 §4.9, §5.1).
3. P2-09a path before P2-09b cost batch; P2-09b lands whole with aggregate
   acceptance (§5.2); bitmap-style cancelling pairs in one commit
   (take3 05 §3.6).
4. P4-01 before derived-table propagation (P2-02d) before P2-02b (take2
   P4-A rev 3/4 sequencing; 07 §3.2 + §3.3).
5. P6-03/04 only after search selects NLI/corr-subplan shapes (07 §3.8);
   P6-05 never without a replacement oracle.
6. One variable per commit (P5 §1); every flip its own commit with
   before/after parity roll-up + timing table for moved plans.
7. P4-01 before Phase 3: P3-02/03/04 do not start before P4-01 lands
   (§2.3 safety rule — wider search is not safe until the numbers
   ranking it are right).

---

## 11. Risk register (per phase)

| phase | risk | evidence | mitigation |
|---|---|---|---|
| P0 | control mis-fires; stale pins contaminate | `m0077-final` 22/22 diverged nightly; `ls -t` selects wrong snapshot (take2 TODO P0-08) | three-edit coordinated re-pin; pre-registered Q72/Q75 blast radius |
| P1 | every-estimate-at-once move (Round-4) | ANALYZE fixed Q5 23×, broke five queries (take2 08 §2.3) | per-item diffs; ratchet not timing; both suites |
| P1 | TOAST/catalog blast radius | catalog-format-adjacent (take2 09 §4 item 7) | full regress-port; re-init data dir |
| P2 | BootVal correction looks catastrophic | 512 MB → 64 MB already +62% (07 §2.1) | ordered group (§10.1); lands alone, both suites timed |
| P2 | faithful term regresses aggregate | qual cost +3.3% with green per-query gates (07 §7.6) | batch + TOTAL-arm acceptance |
| P3 | underestimate permits forbidden orders | wrong-answer class, not slow-plan (take2 TODO P3-01) | `min = syn` fallback on any uncertainty; value-level gates |
| P3 | wider search finds worse plan (ranking wrong) | C4 Q8 200→21 s with Q9 27 s→250 s+ (take2 08 risk R3) | stats+cost precede search (§2.3); per-query diff+timing |
| P4 | projection faster-and-wrong | P4-01b Q2/Q5 0 rows, Q18 wrong tuples (take2 TODO P4-01b) | path-property + setrefs mechanism; digest+diff values gate |
| P5 | eligibility without shape recovery | workers allowed recovers only ~4 s at 4 MB (MEASUREMENT-p202b) | shape (width) first; cost comparison not permission |
| P6 | deleting compensation without cause-fix | Q20 6.5×, Q4 12.5× (07 §3.8) | byte-identical gate; tripwire oracles kept (P6-05) |
| all | sweep variance read as regression | Q76/Q12 byte-identical "regressions" (take2 TODO P2-11) | plan-change attribution rule (07 §7.7) |
| all | noise swamps signal | ±17% single-run band (take2 09 §6.3) | totals for suites; repeats or >1.2× per query |

---

## 12. Migration, kill switches, rollback

Kill switches temporary, each with named retirement (§9 item 4). Lesson from
`GOOPG_COST_DRIVEN_JOINORDER`/`GOOPG_PGSHAPED_DP`: long-lived default-off
means two planners ship and the off arm rots. Each plan-selection change
gets one flag, default off, flipped in its own commit after its acceptance
arm, deleted one commit later. Rollback is the flag until deleted,
`git revert` after. Phase 6 deletions irreversible → gated on byte-identical
plans. Concurrency with the Ralph loop: stage by explicit pathspec, never
`git add -A`; prefer a worktree off clean HEAD for multi-file phases (take2
08 §10, carried).

---

## 13. Out of scope

07 §6 executor residuals (Datum width, probe-seam re-materialisation, spill
path, sort speed, skew buckets, uncancellable loops) with pointers — plan
work does not promise them. TOAST touched only as far as statistics need
(§4 P1-11). `goopg_relstats` standby invisibility accepted, ledgered under
M0112 (take2 08 §4.3, carried).

(End of file)
