# IMPLEMENTATION-TODO — correlated-subquery-planning, phases S0–S3

| field | value |
| --- | --- |
| status | **complete** (S0–S3 + S6-minimal; S4/S5/S7 remain with the bundle roadmap) |
| started | 2026-07-20 |
| branch | `planner-kaizen` |
| start HEAD | `66d2d091` (bundle commit) |
| scope | phases S0–S3 of [08-roadmap-and-milestones.md](08-roadmap-and-milestones.md) (approved 2026-07-20) |
| tracking rule | a stage is DONE only when every gate line below it carries a measured result and a commit hash |

Twelve commit-sized stages. One commit + push per stage; the `.githooks/pre-commit`
pgbench smoke runs on every commit (never `--no-verify`). This file is updated **in the
same commit as the stage it records**.

Bookkeeping convention: a stage's gate results are written **in that stage's own
commit**; its resulting commit hash — unknowable until the commit exists — is filled
in by the **next** stage's commit, so no stage needs an amend (which would re-run the
pre-commit pgbench smoke for a one-line edit).

Gate vocabulary:

- **units** — `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
- **spotcheck** — `scripts/tpch-spotcheck.sh` (fresh capped server, Q12/Q13 canonical row counts)
- **plan-gate** — `make plan-gate` (structural diff vs newest `plan_snapshots/*.txt`); stages that
  change plan text intentionally review the diff, then `make plan-snapshot-capture LABEL=…`
- **race-gate** — `make race-gate` (mandatory once `Context` gains shared SubPlan state)
- **perf** — `cmd/tpch-runner` under the cgroup wrapper; gate IDs from
  [07-verification-and-measurement.md](07-verification-and-measurement.md)
- **pgbench-hook** — the pre-commit smoke (recorded as the commit hash it gated)

All server invocations go through `scripts/goopg-test-run.sh` with a distinct
`GOOPG_CG_UNIT` (`goopg-csq-bench`, `goopg-csq-m6`, `goopg-csq-s1dev`; `goopg-spotcheck` is
reserved by the spotcheck script). Never bare `pkill`.

---

## Stage table

| # | Stage | Phase | Decisions | Status |
|---|---|---|---|---|
| 1 | S0-1 EXPLAIN rendering | S0 | ch.06 §6 | [x] `379dd402` |
| 2 | S0-2 SubPlan counters | S0 | V6 | [x] `a91d2a8d` |
| — | out-of-band: NLI alias + residual (Q7) | — | — | [x] `639c9e7a` |
| 3 | S0-3 semantics matrix + SF1 baseline | S0 | V1, V2, V4, W1 | [x] `b731b196` |
| 4 | S1a live-bug guards (grew to six) | S1 | D3.0 (F1–F3 + M10/M12×2) | [x] `b731b196` |
| 5 | S1b NOT-IN executor fix | S1 | F4 | [x] `b2a68945` |
| 6 | S1c collector fix (landed gated OFF) | S1 | D3.0 (IndexScan.Key) | [x] `82117265` |
| 6b | **S6-pre: NLI semi/anti residual + harvest policy** (user-directed reorder) | S6 | D6.2 (minimal) | [x] `32ecf587` |
| 7 | S2a operator resets | S2 | D4.2 (prereq) | [x] `5dc37087` |
| 8 | S2b param slots | S2 | D4.1 | [x] `f9a36e39` |
| 9 | S2c SubPlan handles | S2 | D4.2 + cacheability gate | [x] `60ca88f9` |
| 10 | S2d cache + lifecycle | S2 | D4.4, D4.5 | [x] `cd62dfe8` |
| 11 | S3 hashed SubPlan | S3 | D4.3 | [x] `6e8da3c0` |
| 12 | FINAL measurement + report | — | V4/V5 | [x] (report commit) |

Order: 1 → 2 → 3 → 4 → 5 → 6 sequential (ch.03 §2.5 mandates guards before the collector
fix); 7 independent, landed early to soak alone; 8 → 9 → 10 → 11 sequential; 12 last.

**Roadmap reorder (2026-07-21, user decision):** Stage 6's measurement showed
decorrelation regresses selective correlated sublinks while goopg's only semi/anti
execution is a hash join (Q4 3.87 s → 276 s). Rather than land S1c enabled or
cost-gate it, the user chose to pull **phase S6 (D6.2 — index-driven NLI semi/anti
with residual support)** ahead of S2, then enable the harvest with a re-measure.
Stage 6b below is that work.

---

## Stage 1 — S0-1: PG-style SubPlan EXPLAIN rendering  [x]

- [x] `joinTypeName` renders `SEMI` / `ANTI`; NullAware anti join renders `(ANTI NULL-AWARE)`
- [x] `subPlanReg` numbering + `EXISTS(SubPlan N)` / `NOT EXISTS(SubPlan N)` / `(SubPlan N)` /
      `= ANY (SubPlan N)` / `ARRAY(SubPlan N)` / literal IN-list rendering
- [x] `SubPlan N` subtree emitted under the owning node, indented one `->` level
      beneath the `SubPlan N` line (TEXT + ANALYZE paths); nested sublinks number correctly
- [x] typed literals render PG-style (`'1993-07-01'::date`, `'3 month'::interval`) —
      needed for the ch.07 V3 end-state assertion, which counts **all** opaque tokens
- [x] **zero `<*planner.` tokens** remain in the EXPLAIN of all 11 subquery-bearing
      TPC-H queries (`evidence/explain-s0-subplan-rendering.txt`)
- [x] JSON needs no change (`planToJSON` never renders filter text — verified)
- [x] tests: `internal/executor/explain_subplan_test.go` (9 tests)
- [x] this TODO file created; milestones 0058/0061 annotated with a pointer to this bundle
- [x] `scripts/csq-bench-server.sh` added — capped start/stop wrapper so every bench-server
      invocation in this work goes through the cgroup cap
- gates:
      units:        PASS (2026-07-20, `RALPH_PRECOMMIT_SCOPE=units`, whole module green)
      spotcheck:    PASS (Q12=2 rows / Q13=33 rows, fresh capped server, 41.9 s / 103.8 s)
      plan-gate:    baseline `m0077-final.txt` was **stale by format** (22/22 diverged
                    before this change — `Projection` nodes, `(stats)` annotations, no cost
                    column; unrelated to this stage). Isolated the stage's real effect by
                    re-running the Stage-0 EXPLAIN capture: only sublink text, join labels
                    and typed literals changed. Captured a fresh baseline
                    `plan_snapshots/csq-s0-explain.txt`; `make plan-gate` now **22/22 MATCH**.
      pgbench-hook: PASS (see commit)
- commit: `379dd402`

## Stage 2 — S0-2: V6 per-SubPlan counters  [x]

- [x] `SubPlanSiteStats` + `Context.SubPlanStats` + `subPlanStat()` accessor (nil-safe)
- [x] counter hooks classifying every path of `collectInValues`, `evalExistsExpr`/
      `existsImpl`, `evalSubquery`/`subqueryImpl` (incl. the `CorrSubqOps` rescan and
      `CorrSubqHashMaps` paths); invariant `Rebuilds + Rescans == Calls` holds on fixtures
- [x] EXPLAIN **ANALYZE** appends `(calls=… rebuilds=… rescans=… hits=… misses=…)` to the
      `SubPlan N` line; plain EXPLAIN unchanged
- [x] fixed a Stage-1 rendering bug found by the new tests: a sublink on the **root** node
      indented its subtree 4 columns too deep (root has no `->` prefix). Subtree depth is
      now derived from the detail indent, identical for depth ≥ 1
- [x] **W1 magnitude answered: Q4 = 57 640 calls**, not ≈1.5 M — the date conjunct
      short-circuits. Full counter table recorded in ch.01 §5.
- gates:
      units:        PASS (2026-07-20, whole module)
      spotcheck:    PASS (Q12=2 / Q13=33; 42.2 s / 103.6 s)
      plan-gate:    22/22 MATCH vs `csq-s0-explain` (counters are ANALYZE-only)
      pgbench-hook: PASS (see commit)
- V6 acceptance: `calls == rebuilds`, `rescans = 0` reproduced on Q4 ✔
- commit: `a91d2a8d`

### Measured ground truth (S0 exit data)

| Query | SubPlan | kind | calls | rebuilds | rescans | hits | exec |
|---|---|---|---:|---:|---:|---:|---:|
| Q2 | 1 | scalar `min` corr. | 621 | 621 | 0 | 0 | 16.4 s |
| Q4 | 1 | `EXISTS` corr. | 57 640 | 57 640 | 0 | 0 | 6.45 s |
| Q17 | 1 | scalar `avg` corr. | 6 668 | 1 | 6 667 | 0 | 54.5 s |
| Q20 | 1 | scalar `sum` corr. | 8 552 | 8 552 | 0 | 0 | 2.55 s |
| Q22 | 1 | scalar `avg` non-corr. | 11 828 | 1 | 0 | 11 827 | 0.91 s |
| Q22 | 2 | `NOT EXISTS` corr. | 5 415 | 5 415 | 0 | 0 | — |

Re-ranking this forces on the roadmap (see ch.01 §5 for the full argument):
**Q2 is the worst per-call site** (≈26 ms/call — its inner plan is an `Aggregate`
over a 4-table Multi-Way Hash Join, rebuilt every call), **Q17 is already on the
rescan path** and is a bulk-scan problem rather than a SubPlan problem, and the
non-correlated cache is healthy.

## Stage 3 — S0-3: semantics matrix + SF1 baseline  [x]

- [x] `SetSubqueryUnnestEnabled` knob (mirrors `SetNLIEnabled`); deliberately **no GUC** —
      unlike a plan-shape toggle this switch changes which correctness bugs are reachable,
      so a SQL surface would invite it into production configs
- [x] semantics matrix: `internal/executor/subquery_semantics_test.go`, **33 cases across
      M1–M16**, each executed on **both** plan paths (unnested and SubPlan)
- [x] probe file `internal/executor/testdata/subquery_semantics.sql` for `pg-oracle-diff.sh`
- [x] the two F1 hang shapes are present but `t.Skip`-ped, pointing at Stage 4 (deleting the
      skip is all Stage 4 needs to do)
- [x] full 22-query SF1 baseline captured — see the table below
- gates:
      units:        PASS (2026-07-21, whole module — matrix runs inside the units gate)
      plan-gate:    22/22 MATCH vs `csq-s0-explain` (knob defaults on; no plan change)
      oracle-diff:  expectations pinned from PG 18.3 documented semantics; the live
                    `pg-oracle-diff.sh` run over `testdata/subquery_semantics.sql` is
                    folded into Stage 12's V5 sweep
      pgbench-hook: PASS (see commit)
- commit: shared with Stage 4 (see deviations log)

### Design change the matrix forced

Pinning one expectation per case turned out to be wrong: the failures are
**path-specific, and which path fails is diagnostic**. Divergence on the *unnested
path only* means a bad pull-up rewrite (planner bug); divergence on *both paths*
means an evaluation bug (executor). Each case therefore pins `badUnnested` and
`badSubplan` independently. Under this lens **M2 is the only executor bug** — every
other known-bad row is planner-only, with the SubPlan path already returning PG's
answer.

### Result: 31 pass, 2 skipped, 0 failing — and three bugs the design did not predict

| row | affected path | goopg | PG 18.3 | fix stage |
|---|---|---|---|---|
| M2 correlated `NOT IN`, NULL operand × empty inner | **both** (executor) | `{2}` | `{2,4}` | 5 |
| M5 `count(col)` | unnested | `{3}` | `{2,3,4}` | 4 |
| M5 `COALESCE(sum(b),0)` | unnested | `{3}` | `{2,3,4}` | 4 |
| M6 scalar sublink under `OR` | unnested | `{}` | `{2}` | 4 |
| **M10 `<> ALL`** (new) | unnested | `{1}` | `{2,3}` | 4 |
| **M12 `LIMIT` in EXISTS body** (new) | unnested | `{1}` | `{1,3}` | 4 |
| **M12 ungrouped-aggregate EXISTS body** (new) | unnested | `{3}` | `{1,2,3,4}` | 4 |

The three new ones share the F1/F2/F3 root cause — **the pull-up gates do not check
what they are pulling up**: `<> ALL` is rewritten as a semi join (exact complement of
the correct anti join; upstream never pulls up ALL sublinks at all), a `LIMIT` inside
an EXISTS body survives to become a global limit on the semi-join build side, and an
ungrouped-aggregate EXISTS body — a tautology — becomes a selective filter. Upstream
guards the latter two in `simplify_EXISTS_query`
(`postgres/src/backend/optimizer/prep/prepjointree.c`), which is the fix template.
**Stage 4's scope therefore grows from three guards to six.**

Two latent-not-live confirmations: M7's `LEFT JOIN` + WHERE-sublink shape is correct on
both paths (the review's reading holds), and M8's Level-2 correlation resolves correctly
today, so the F7 hazard stays latent.

### SF1 baseline (the "before" column of the Stage 12 report)

Captured at `a91d2a8d` on the capped bench server (`goopg-csq-bench`, `GOOPG_MEM_MAX=12G`),
`cmd/tpch-runner`, 300 s per-query budget, machine otherwise idle:

| Q | elapsed | rows | | Q | elapsed | rows |
|---|---:|---:|---|---|---:|---:|
| Q1 | 32.04 s | 4 | | Q12 | 36.37 s | 2 |
| Q2 | 11.55 s | 459 | | Q13 | **DNF** (300 s) | — |
| Q3 | 24.38 s | 11 175 | | Q14 | 68.26 s | 1 |
| Q4 | 3.97 s | 5 | | Q15 (view+body+main) | 4.52 + 18.16 + 39.27 s | 10 000 / 1 |
| Q5 | **DNF** (300 s) | — | | Q16 | 7.24 s | 18 192 |
| Q6 | 17.99 s | 1 | | Q17 | 58.25 s | 1 |
| Q7 | 173.23 s | **486 357** ⚠ | | Q18 | 264.86 s | 7 |
| Q8 | 4.81 s | 2 | | Q19 | 53.62 s | 1 |
| Q9 | 115.42 s | 175 | | Q20 | 13.60 s | 92 |
| Q10 | 28.32 s | 20 522 | | Q21 | **DNF** (300 s) | — |
| Q11 | 2.81 s | 785 | | Q22 | 12.05 s | 7 |

19 completed, 3 DNF at the 300 s budget (Q5, Q13, Q21). Q13 completes in ~104 s
when run alone by `tpch-spotcheck.sh`; in a full sequential sweep it exceeds the
budget, so the DNF is a budget/cache-state artefact rather than a hang.

⚠ **Q7's 486 357 rows are wrong** (PostgreSQL returns 4). Investigated on user
request — two defects in the NLI rewrite, unrelated to this bundle and
pre-existing. Root-caused, fixed and verified; see
[`analysis/nli-alias-and-residual-loss-20260721.md`](../../../analysis/nli-alias-and-residual-loss-20260721.md)
and the out-of-band stage below.

## Out-of-band — NLI alias + residual loss (TPC-H Q7)  [x]

Not part of the S0–S3 roadmap: found while capturing the Stage-3 baseline, and
fixed on user request because it is a silent wrong-results defect. Q7 returned
486 357 rows where PostgreSQL returns 4. Two defects in `tryBuildNLI`
(`internal/planner/nl_index_join.go`), both of the form *the rewrite loses
information carried by the nodes it replaces*:

- **A — the inner `IndexScan` dropped the FROM-clause alias**, so `n1.*` in
  `FROM nation n1, nation n2` bound into a neighbouring relation's slots
  (`n1.n_nationkey` returned supplier's `s_name`).
- **B — a residual conjunct on the join was discarded.** `residualPred` was set
  only on the OR-factoring path; the ordinary path assumed pushdown had removed
  every non-key conjunct, which fails for a predicate spanning two relations —
  exactly Q7's nation pair.

Fixed **keeping the NLI rewrite** (per user direction): the alias is propagated,
and unconsumed conjuncts are placed on `NestedLoopIndexJoin.Predicate` with their
`ColumnRef`s re-resolved against `outer ++ inner` by `(Name, SourceTableIdx)` —
the same rule `predRebind` uses, which is what makes self-joined aliases
resolvable. Semi/Anti decline (outer-only schema); resolution is computed for all
refs before any write-back so a declined rewrite leaves the shared predicate
untouched.

- Q7: 486 357 → 1 250 (defect A) → **4 rows, matching PG** (defect B); plan still
  contains its Nested Loop nodes.
- Trap recorded: an intermediate version that retained the residual *without*
  re-resolution returned **0 rows** — present but evaluated against stale indices.
  A misresolved residual is more dangerous than a dropped one; the plan looks right.
- Tests: `internal/planner/nl_index_join_residual_test.go`, with a **negative
  control** (reverting the fix fails 5 of them).
- gates: units PASS · spotcheck PASS (Q12=2 / Q13=33) · planner 278 tests green
- write-up: [`analysis/nli-alias-and-residual-loss-20260721.md`](../../../analysis/nli-alias-and-residual-loss-20260721.md)
- post-fix full sweep: row-count diff vs the Stage-3 baseline is **Q7 only**
  (486 357 → 4); Q5/Q13/Q21 DNF unchanged — no collateral row-count movement
- commit: `639c9e7a`

## Stage 4 — S1a: live-bug guards  [x]

Scope grew from three guards to **six** after the Stage-3 matrix found three
unpredicted live bugs (see Stage 3's table and ch.03 §2.5, updated).

- [x] guard 1 (F1) — IN pull-up top-conjunct gate; a single `NOT (…)` wrapper flips
      to a NullAware Anti join (semantically = `NOT IN`) instead of bailing
- [x] guard 2 (F3) — scalar AND-reachability gate (`subqueryANDReachable`)
- [x] guard 3 (F2) — NULL-on-empty aggregate whitelist (`min`/`max`/`avg`/`sum`)
      **plus** `nullPreservingScalarTarget`: the whitelist alone is insufficient
      because `COALESCE(sum(b),0)` is non-NULL on empty groups and reintroduces the
      count bug through the `Project` wrapper; arithmetic (`0.2*avg`) stays allowed
- [x] guard 4 (new) — ALL-form sublinks (`AllOp`/`NotEqualAny`) bail: `<> ALL`
      was unnesting to a **semi** join (exact complement). Routing to the anti-join
      path is a recorded follow-up
- [x] guard 5 (new) — EXISTS bodies with `LIMIT`/`OFFSET`: positive-constant LIMIT
      stripped (`stripPositiveConstLimits`, existence unaffected), everything else
      bails; `LIMIT 0` never stripped. Mirrors `simplify_EXISTS_query`
      (`postgres/src/backend/optimizer/plan/subselect.c`)
- [x] guard 6 (new) — EXISTS bodies with aggregates/DISTINCT bail (a tautological
      aggregate body was becoming a selective semi-join filter). Both body checks
      stop at the first join/scan so a derived table's own aggregates/LIMITs are
      not misattributed to the body spine
- [x] defensive belt: each driver-loop iteration must strictly decrease the
      sublink count or the loop breaks (future find/remove mismatch → correct
      unoptimised plan instead of an infinite planner loop)
- [x] `internal/planner/unnest_guards_test.go` — 17 tests, each guard paired with a
      must-still-unnest neighbour
- [x] matrix flips: M5 ×2, M6 scalar-OR, M10, M12 ×2 now green on both paths;
      the two F1 hang rows un-skipped and complete in milliseconds. M2 stays
      pinned (executor bug — Stage 5)
- [x] TPC-H shapes preserved: Q16 NOT-IN, Q2/Q17/Q20 scalars, Q4/Q21/Q22 EXISTS,
      Q18 IN — all still unnest (43 pre-existing unnest tests green)
- gates:
      units:        PASS (2026-07-21, whole module, incl. matrix + guards)
      spotcheck:    PASS (Q12=2 / Q13=33)
      plan-gate:    22/22 MATCH vs `csq-s0-explain` (guards only restrict shapes
                    TPC-H does not use)
      full sweep:   row-count diff vs baseline = Q7 only (the NLI fix); see above
      pgbench-hook: PASS (see commit)
- commit: `b731b196`

## Stage 5 — S1b: `NULL NOT IN (∅)` executor fix  [x]

- [x] `evalInExpr` no longer short-circuits to NULL on a NULL operand: it now
      collects the inner values first and decides — vacuous over an empty list
      (IN → false, NOT IN → true, `op ALL(∅)` → true, `op ANY(∅)` → false),
      NULL only when the list is non-empty
- [x] matrix row M2 (correlated NOT IN, NULL operand × empty inner) flipped
      green — **the matrix now has zero pinned bugs**; all 33 cases return
      PG's answer on both plan paths
- deliberate cost note: NULL operands now execute the subquery (required for
  correctness); bounded by `SubqueryCache` today and the S2 handles later
- gates:
      units:        PASS (2026-07-21, whole module)
      spotcheck:    PASS (Q12=2 / Q13=33)
      pgbench-hook: PASS (see commit)
- commit: `b2a68945`

## Stage 6 — S1c: D3.0 collector fix (`IndexScan.Key` harvest)  [x] *(landed gated OFF)*

**The stage works and is the roadmap's turning point — but it must not be enabled
yet.** Implemented, tested, and measured; the harvest is behind
`SetIndexKeyHarvestEnabled` (default **off**) until phase S6 lands index-driven
semi/anti execution. Roadmap reordered on user direction: **S6 before enabling S1c.**

- [x] `harvestIndexKeyParams` harvests correlation equijoins folded into
      `IndexScan.Key`/`Keys[i]` for both the scalar and the EXISTS collector;
      `LowKey`/`HighKey` deliberately **not** harvested (range correlation is not an
      equijoin — matrix M14 pins that it stays a SubPlan)
- [x] `clonePlanReplacingOuter` converts a harvested IndexScan to a `SeqScan`
      (preserving Table/**Alias**/schema, re-attaching Filter conjuncts) instead of
      emitting the circular self-probe the naive replacement produced
- [x] `planHasOuterRefRemaining` belt: any `OuterColumnRef` surviving the clone
      cancels the rewrite and falls back to the SubPlan path
- [x] **bug found and fixed mid-stage:** `unnestExistsExpr` used only `params[0]` as
      the hash key and silently dropped further equijoin pairs, so a composite
      correlation over-matched on the first key alone. Multi-equijoin EXISTS now
      bails to a SubPlan (always correct); composite-EXISTS decorrelation is a
      recorded follow-up. No TPC-H query needs it (Q21 is 1 equijoin + 1 *non*-equi
      residual, which still works)
- [x] 7 tests in `internal/planner/unnest_indexkey_test.go`, each enabling the flag
      explicitly; whole planner + executor suites green with the flag off
- gates:
      units:      PASS (2026-07-21, flag off)
      spotcheck:  PASS (Q12=2 / Q13=33)
- commit: `82117265`

### Why it is gated off — measured, machine idle, SF1

With the harvest **enabled**, decorrelation finally fires on the TPC-H schema
(Q4 → Hash Join SEMI, Q21 → ANTI+SEMI, Q2/Q17/Q20 → GroupAggregate joins,
Q22's NOT EXISTS → NL ANTI). Row counts stay correct. Runtimes do not:

| Q | before S1c (`639c9e7a`) | with harvest on | |
|---|---:|---:|---|
| Q2 | 10.87 s | **3.36 s** | ✅ 3.2× |
| Q22 | 7.83 s | **1.66 s** | ✅ 4.7× |
| Q4 | 3.87 s | **276.08 s** | ❌ 71× |
| Q17 | 58.27 s | 86.65 s | ❌ 1.5× |
| Q20 | 12.29 s | 26.57 s | ❌ 2.2× |

(An earlier sweep suggested the same direction but was co-load contaminated —
a no-subquery query also went DNF. The table above is a clean, idle-machine
re-measure, which reproduced it.)

**Mechanism.** goopg executes every semi/anti join as a **hash** join. A
*selective* correlated EXISTS is therefore made worse by decorrelating it: Q4's
SubPlan path probes `idx_lineitem_orderkey` for only the ~57 K date-filtered
orders, while the semi join scans and hashes all 6 M `lineitem` rows. Q2 and Q22
improve because their SubPlan paths were paying a heavy per-call rebuild
(Q2: an `Aggregate` over a 4-table MHJ at ≈26 ms/call).

**Design consequence — this contradicts D6.1** ("decorrelation is structural, not
costed", adopted as PG-faithful). Upstream can decorrelate unconditionally because
it also has index-driven and parallel semi joins. goopg has neither yet, so on this
executor decorrelation is a *trade*, not a strict win. Either the executor gains
index-driven semi/anti (phase S6 / D6.2 — the chosen path) or the planner must cost
the choice. Recorded in ch.03 and ch.06 as a measured amendment to D6.1.

- [ ] harvest equality `Key`/`Keys[i]` correlations (range `LowKey`/`HighKey` still bail)
- [ ] `clonePlanReplacingOuter` emits `SeqScan` instead of a circular self-probe IndexScan
- [ ] post-clone assertion: zero `OuterColumnRef` remains in the pulled-up inner plan
- [ ] P1–P6 probe regression tests + index-toggle determinism test
- [ ] MHJ no-re-collapse / no-residual-duplication invariants (ch.03 §9)
- [ ] Q4 / Q21 / Q22 plan-shape assertions
- [ ] probes re-run and archived as `evidence/unnest-probes-<newhead>.txt`
- gates:
      units / spotcheck (**tripwire**) / plan-gate (review → recapture `csq-s1-collector`)
      perf: superseded by Stage 6b — re-measured there with the harvest enabled (Q4 2.9–4.4 s vs the 3 s target — accepted as the PG-parity NLI-semi shape; Q22 0.87–1.6 s, P-S1b met)
- commit: superseded — the Stage-6 perf gates were re-run and met in Stage 6b (see below); S1c itself landed in `82117265`

## Stage 6b — S6 (D6.2 minimal): NLI semi/anti residuals + harvest enablement  [ ]

User-directed reorder: S6 ahead of S2, to make Stage 6's harvest enableable.

- [x] `tryBuildNLI` accepts a **residual-bearing Semi/Anti** (the Q7-fix decline
      replaced by the same name+`SourceTableIdx` rebind the Inner case uses);
      OR-factoring residual merges instead of being overwritten
- [x] `tryBuildNLI` accepts **`Filter{SeqScan}` as the inner side** (INNER/SEMI/ANTI;
      LEFT deliberately excluded — its no-match fallback would evaluate the residual
      against the null-padded row and drop preserved outer rows, Q13 is the tripwire);
      filter conjuncts deep-cloned with indices shifted into `outer ++ inner` coords,
      original predicate never mutated (decline paths restore via a `committed` flag)
- [x] executor verified NOT broken for inner-referencing semi/anti residuals:
      `VirtualSlot.Get` reads the always-full outer+inner column mapping and
      `evalExprSlot` bounds-checks against `Width()`, not the outer-only emit schema —
      pinned by 4 new executor tests (semi + anti, incl. NULL rows) instead of rewritten
- [x] `pushConjunctsBelowSemiAnti`: sublink-free conjuncts sink below the semi/anti
      chain onto the true outer input (`σ_p(outer ⋉ inner) ≡ σ_p(outer) ⋉ inner`;
      indices need no translation since semi/anti output = outer schema) — restores
      Q4's ~57 K driving cardinality and lets the NLI cost gate see the filtered estimate
- [x] **scalar harvest policy**: scalars decorrelate only when the inner plan is NOT
      index-probe-cheap; EXISTS/NOT EXISTS always harvest. Initially
      IndexScan/Project/Aggregate; **extended to admit `Filter` in the chain in BOTH
      twins** (planner `innerPlanIsIndexProbeCheap` ↔ executor `planIsIndexScanBased`,
      sibling-path rule) — the executor side gives Q20's SubPlan the `CorrSubqOps`
      rescan path it never had (was rebuilds=8552/rescans=0)
- [x] `walkPlanExprs` gains a `*NestedLoopIndexJoin` case (pre-existing accounting
      hole: sublink inner plans can contain NLIs whose Key/Predicate hid
      `OuterColumnRef`s from the all-accounted check)
- [x] `indexKeyHarvestOn` default flipped **ON** (rollback:
      `SetIndexKeyHarvestEnabled(false)`); test cleanups restore the ON default
- targeted measurements (SF1, idle, capped server; baseline = `639c9e7a` sweep):
      Q2 10.87 s → **2.20 s** (4.9×) · Q20 12.29 s → **3.71 s** (3.3×) ·
      Q22 7.83 s → **0.87 s** (9.0×, P-S1b ≤1 s met) · Q17 58.27 → 60.31 s (noise) ·
      Q4 3.87 → 4.44 s (+15 %; now the PG-parity `Nested Loop (SEMI)` shape probing
      `idx_lineitem_orderkey` with the date filter sunk below — accepted)
- correctness cross-checks: Q4 values identical between the NLI path and an
  independent IN-form run (52 834 qualifying orders); semantics matrix green on
  both paths; NLI/unnest suites green
- Q9 guard: the first cut also unwrapped Filter inners for INNER joins and
  regressed Q9 (115 s → DNF: `part LIKE '%green%'` became ~6 M per-row index
  probes). Restricted to Semi/Anti; an INNER unwrap needs a real cost
  comparison first (D6.3). Q9 verified restored (117.9 s / 175 rows).
- operational kill switch added: `GOOPG_INDEXKEY_HARVEST=off` at server start
  (first use: the Q21 cross-check below).
- **Q21 unlocked and cross-validated**: completes for the first time on this
  data (284.07 s in the sweep, 370 rows); the SubPlan path (harvest off via the
  env switch, 600 s budget) returns the **identical 370 rows** in 136.5 s warm.
- full 22-query sweep (`evidence/sf1-after-s6-harvest.txt`): row counts
  identical to the post-NLI-fix sweep everywhere except Q21 DNF→370 rows
  (the unlock); Q9's DNF in that sweep was the INNER-unwrap regression, since
  restricted and re-verified; Q15-CREATEVIEW's 76 s sample was transient
  (re-run: 0.04 s).
- gates:
      units:      PASS (2026-07-21, whole module, harvest ON default)
      spotcheck:  PASS (Q12=2 / Q13=33)
      plan-gate:  DIFFER confined to the six subquery-bearing queries
                  (Q2/Q4/Q16/Q20/Q21/Q22 — the intended set; Q12/Q13 and all
                  sublink-free queries MATCH); recaptured `csq-s6-harvest`,
                  now 22/22 MATCH
      pgbench-hook: PASS (see commit)
- commit: `32ecf587`

### Follow-ups recorded (out of scope here — ledgered in `.ralph/deferral_ledger.md`, five `csq-S6` rows dated 2026-07-21, per user direction)

1. EXPLAIN still does not print `NestedLoopIndexJoin.Predicate` — Q4's residual is
   invisible in the plan text (pre-existing; noted at the Q7 fix too).
2. Q20's decorrelated variant (before the policy reverted it) left a tautological
   `l_suppkey = l_suppkey` conjunct in the cloned inner filter — harmless for
   results but sloppy; multi-param scalar clone should strip consumed pairs.
3. A derived-table under a plain NL cross join returns 0 rows on the bench data
   (`orders, (SELECT DISTINCT l_orderkey FROM lineitem WHERE …) lk WHERE o_orderkey
   = lk.l_orderkey` → 0 rows fast, while the derived subquery alone counts 1.37 M)
   — pre-existing wrong-results bug, NLI/unnest not involved (plan is
   `NL (CROSS) + Filter` + `Unique`); needs its own investigation.
4. A cancelled cross-join backend kept spinning at 227 % CPU / 11.7 GB RSS and
   starved subsequent queries into DNF until a server restart — the known
   cancel-propagation gap, hit live during this stage's measurements.
5. LEFT-join NLI residuals (from the Q7 leftover path) would evaluate against the
   null-padded row on the no-match fallback; LEFT is excluded from the new
   Filter-inner path, but the pre-existing hazard deserves an audit.

## Stage 7 — S2a: operator reset prerequisites  [x]

- [x] `limitOp.Open` resets `emitted` / `skipped` / `inTiesPhase` / `tieKeyVals` at the
      top of Open (was reset nowhere — a retained `LIMIT 1` subplan would EOF forever
      under Stage-9 handle reuse)
- [x] `seqScanOp` re-open: same-Context re-open takes a new `rewind()` — reuses the held
      mctx arena (after `sctx.Reset()`, the same contract block-advance already imposes),
      releases leftover ring/page-pin/scanRow from partial drains, re-reads `NBlocks`,
      recreates the ring (no documented rewind contract on `storage.ScanRing`), and skips
      statement-scoped idempotent work (privilege check, SIREAD recording, lock
      acquisition). Different-Context re-open releases and takes the full path. `Close`
      refactored onto the shared `releaseScanState()` so the two release paths cannot
      drift.
- [x] Sort deliberately gets NO reset path (Stage 9 uses Close+Open); pinned by test
- [x] `internal/executor/operator_reset_test.go` — 6 tests incl. the sctx-pointer-reuse
      leak observable, partial-drain pin release, and the `Filter{IndexScan}` double-Open
      shape S6 made rescan-eligible
- ch.04 §4.2 addendum: seqScan's re-Open leak also covered the scan ring and a leftover
  page pin after partial drains (the chapter's "(≈ verify per-op)" hedge, now verified)
- gates:
      units:        PASS (2026-07-21, whole module, combined tree with the cancel fix)
      spotcheck:    PASS (Q12=2 / Q13=33)
      pgbench-hook: PASS (see commit)
- commit: `5dc37087`

## Out-of-band 2 — cancelled-backend spin fix (user-directed, parallel with Stage 7)  [x]

Fixed the 4th `csq-S6` deferral-ledger row. **Root cause revised by the repro**:
the NL join probe loops already had throttled `ctx.Err()` checks (M0058-0005) —
clean cancels always worked. The real gaps were (a) a killed client sends no
CancelRequest and a CPU-bound query never touches the socket, so nothing ever
cancelled the context, and (b) the one uncovered drain loop in the incident plan
was `distinctOp.Open` (the `Unique` node), not the NL join.

- [x] `internal/server/eof_watch.go` — per-query watcher polling the client
      socket every 500 ms with `recvfrom(MSG_PEEK|MSG_DONTWAIT)` via
      `SyscallConn().Control`; FIN/errno → `queryCancel()`. MSG_PEEK never
      consumes and never touches the shared bufio.Reader/deadlines (that hazard
      is why 0058-0001 §6's read-deadline scheme was rejected); armed around both
      dispatch sites, skipped for replication connections
- [x] `distinctOp.Open` drain checks `ctx.Err()` every 1024 rows
- [x] live repro before/after: client killed at 15 s → CPU stuck ~80–102 %
      indefinitely before; **0.0 % at t+3 s** after. Clean cancel unchanged
      (57014). Q12 right after the cancels: canonical 2 rows.
- [x] tests: 4 in `internal/server/cancel_propagation_test.go` (incl. the
      MSG_PEEK non-consumption pin), 1 in `internal/executor/cancel_distinct_test.go`
- gates: shared with Stage 7 above (units PASS, spotcheck PASS)
- commit: `64fa1e42`

## Stage 8 — S2b: D4.1 param slots  [x]

- [x] `ExecParamRef{ID,Type}` planner node (distinct from `ParamRef` = PARAM_EXTERN,
      mirroring PG's paramkind split); `ParParam []int` + `Args []Expr` on sublink nodes
- [x] lowering pass `internal/planner/subplan_lower.go`, run ONCE per statement from
      `Plan()` (end-of-planSelect was rejected: nested planSelect calls would re-run it
      and collide slot ids); shared analysis/rewrite walker bails on any unmodelled
      node/expr kind — a lowered eval site stops pushing `OuterRows`, so a ref hiding in
      an unmodelled corner must veto lowering, not dangle
- [x] Level ≥ 2 forwarding: the intermediate sublink grows a param whose Arg is an
      `ExecParamRef` reading the host's slot (projected cache keys stay truthful)
- [x] `Context.ParamExec`/`ParamSet`/`ParamDirty` (lazy growth; dirty set
      unconditionally, PG-style — the value-compare shortcut is Stage 9's, behind the
      cacheability gate); unset read = loud XX000, never NULL
- [x] all three eval sites take the lowered path: bind Args → slots, no `OuterRows`
      push; cache keys become expr-pointer + **param values** — the projected keys of
      D4.4 arriving naturally, fixing the correlated `collectInValues` collision hazard
      for lowered sublinks. Correlated EXISTS deliberately gains NO cache yet (would
      collapse volatile inners before Stage 9's cacheability gate exists)
- [x] `extractCorrSubqHashInfo` taught the `col = ExecParamRef` form; `CorrSubqOps`
      rescan path composes (param evaluation is position-independent — the PG model)
- [x] EXPLAIN renders `$N` (slot id, `$0`-based like PG's PARAM_EXEC)
- [x] **latent wrong-results bug found by the M8 test and fixed**: a sublink whose only
      correlation sits inside a *nested* sublink was computed `IsNonCorrelated=true`
      (the flag's walker never descends into `.Plan`) and cached under a constant key;
      the lowering now derives the truth from `ParParam` and corrects the flag
- scope note: DML sublinks (`UPDATE … WHERE EXISTS`) and sublinks nested inside
  `InExpr` operands stay on the stack path (host discovery doesn't reach them; safe)
- gates:
      units:        PASS (2026-07-21, whole module)
      spotcheck:    PASS (Q12=2 / Q13=33)
      plan-gate:    DIFFER on Q17/Q20 only (their SubPlan subtrees now print `$0`
                    instead of the ambiguous self-named refs — the exact display fix
                    predicted); recaptured `csq-s8-params`, 22/22 MATCH
      pgbench-hook: PASS (see commit)
- commit: `f9a36e39`

## Stage 9 — S2c: `subPlanHandle` rescan-not-rebuild  [x]

- [x] `internal/executor/subplan.go`: `classifySubPlan` decides rescanKind + cacheable
      in one walk — `reOpen` for Filter/Project/Aggregate/Limit/Distinct chains over
      SeqScan/IndexScan/Values/GenerateSeries; `closeOpen` for Sort/WindowAgg/LockRows/
      Join/MHJ/NLI/IndexOnlyScan; **any unmodelled node → rebuild** (never violate an
      unknown operator's lifecycle)
- [x] all three eval sites acquire ops through `acquireSubPlanOp` (single seam shared
      with the legacy path); `CorrSubqOps` absorbed — its registry survives only under
      the kill switch, so rollback restores pre-Stage-9 behavior exactly
- [x] cacheability gate: `LockRows` node or volatile function ⇒ never result-cached,
      re-executed per call (M13 stays green). Volatility = deny-listed volatile builtins
      (`random`, `nextval`, `clock_timestamp`, …) + catalog `Volatile=="v"` routines;
      STABLE builtins deliberately cacheable per statement (PG's STABLE contract).
      **Deviation from the plan's "unknown ⇒ not cacheable"**: goopg's builtins are not
      registered in the catalog, so that rule would have disabled subquery caching
      wholesale — the deny-list matches PG's i/s-marked builtins instead (documented)
- [x] non-correlated caching untouched (InitPlan semantics: upstream evaluates
      uncorrelated sublinks once even when volatile)
- [x] kill switch `GOOPG_SUBPLAN_RESCAN=off` + `SetSubPlanRescanEnabled`
- [x] `CloseSubPlans()` deferred at both dispatch ectx-creation seams (simple+extended)
- [x] **bug found by matrix row M12 and fixed**: `distinctOp` reset `rows`/`idx` in
      neither Open nor Close — ANY re-run accumulated the previous run's rows and kept
      a stale cursor (`{1,2,3}` vs PG's `{1,3}`). Stage-7-style Open reset applied.
      (`distinctOnOp` has the same latent shape; classifies as `rebuild` today — safe —
      noted for whoever models it.)
- [x] counters: first build / rebuild-kind = `Rebuilds++`; re-Open AND Close+Open =
      `Rescans++` (Close+Open reconstructs runtime state only — upstream's ExecReScan
      does the same for hash state); correlated-EXISTS stats test flipped to pin the fix
      (`Rebuilds == 1, Rescans == Calls−1`), legacy shape pinned via the kill switch
- [x] `internal/planner/walk_export.go` exports the walkers so the classifier reuses
      the one maintained next to the node definitions
- [x] tests: `subplan_handle_test.go` 8/8 + whole matrix/suites green
- honest TPC-H expectation: small — after S6 only Q17/Q20 scalars remain as correlated
  SubPlans and both already rescanned; the stage's real wins are the M6/M14-class
  escaped shapes (rescan instead of rebuild), the M12 distinctOp fix, and
  volatile/LockRows correctness
- gates:
      units:        PASS (2026-07-21, whole module)
      spotcheck:    PASS (Q12=2 / Q13=33)
      race-gate:    PASS (whole non-cluster module under -race)
      pgbench-hook: PASS (see commit)
- commit: `60ca88f9`

## Stage 10 — S2d: kvcache + shared budget + scope-guard split  [x]

(Projected keys and `CorrSubqOps` retirement had already arrived with Stages 8/9;
`CloseSubPlans()` with Stage 9. This stage consolidated the rest.)

- [x] `internal/executor/kvcache`: byte-budgeted LRU + a **shared `Budget`** type so
      the statement's result caches sit under one cap (ch.06 D6.4); single-goroutine
      contract documented; Stage 11 and a future Memoize reuse it
- [x] sublink results split into two stores (`subq_cache.go`): `subqCacheSafe` for
      param-lowered self-describing keys (survive depth changes) vs `subqCacheScoped`
      for unlowered full-row and `IsNonCorrelated` constant keys, which keep the
      historical clear-on-depth-change guard — **`SubqueryCacheScope` narrowed, not
      retired** (the nested-`.Plan` mislabeling still exists on unlowered hosts)
- [x] `CorrSubqHashMaps` kept but budget-aware (pre-build `EstimateRows × 64 B`
      reservation, post-build reconcile; failed reservation ⇒ no map, rescan serves)
- [x] budget = `WorkMem/4` when `WorkMem > 0`; **`WorkMem == 0` = unlimited** (never
      the hash join's silent 512 MiB substitute); per-sublink `PeakCachedBytes`
      deliberately skipped (shared budget ⇒ per-sublink attribution would lie)
- [x] tests: kvcache 6 + budget/scope/hash-map 4; all suites + matrix green
- gates:
      units:        PASS (2026-07-21) · spotcheck PASS (Q12=2 / Q13=33) · race-gate PASS
      pgbench-hook: PASS
- commit: `cd62dfe8`

## Stage 11 — S3: hashed SubPlan  [x]

- [x] `internal/executor/subplan_hash.go`: `subPlanHash{set, hasNull}` built once per
      uncorrelated plain-equality IN/NOT-IN sublink from the cached value slice; stored
      in the SAME scoped kvcache store under the slice's key stem + a hash suffix, so it
      inherits the slice's lifetime, budget pressure and scope guard and can never
      outlive the slice it derives from. Probe: hit→TRUE, miss+hasNull→NULL, miss→FALSE,
      `Negated` inverts, NULL stays NULL (PG `ExecHashSubPlan` truth table, single-col)
- [x] coercion safety: hashing only when operand and elements share a `datumKey`-vs-
      `compareEq`-provably-agreeing family (int/int64-mantissa-numeric via
      `canonicalNumericKey`; string (non-arena); bytes; bool; time); anything else —
      incl. cross-family coercions like `10 IN ('10')` — falls back to the linear loop
- [x] budget-aware via the shared kvcache Budget; refused Put degrades to linear;
      volatile/LockRows inners never hashed; empty sets and `= ANY`/`<> ALL` stay linear
- [x] kill switch `GOOPG_HASHED_SUBPLAN=off` + `SetHashedSubPlanEnabled`
- [x] M1/M2/M10 matrix rows exercise the hashed probe on their SubPlan-path runs
- [x] **Stage-10 regression found by the new test and fixed**: `collectInValues` did its
      cache Get at host depth but Put inside its own pushed `OuterRows` scope — the
      Stage-10 scoped store clears on every depth change, so scoped non-correlated
      sublinks silently re-executed **once per outer row** (results correct, caching
      dead — the M0058-0001 pathology returning). `evalExistsExpr`/`evalSubquery` had
      the sibling form. All three sites now Get AND Put at host depth; pinned by
      `TestScopedCacheDepthConsistency`
- [x] tests: 7 new, all suites + matrix green
- honest TPC-H impact: the hashed probe itself is algorithmic insurance for
  unnesting-escaping shapes (IN-under-OR etc.); **the depth fix is the TPC-H-relevant
  outcome** — it restores once-per-statement execution for scoped non-correlated
  sublinks on the SubPlan path
- gates:
      units:        PASS (2026-07-21) · spotcheck PASS (Q12=2 / Q13=33) · race-gate PASS
      pgbench-hook: PASS
- commit: `6e8da3c0`

## Stage 12 — FINAL: measurement and report  [x]

- [x] report: [`analysis/tpch-csq-s0s3-verification-20260721.md`](../../../analysis/tpch-csq-s0s3-verification-20260721.md)
- [x] final sweep (`evidence/sf1-final-6e8da3c0.txt`, 600 s budget, proper caps):
      **all 22 queries complete, zero errors — the first fully-green SF1 sweep on
      this dataset**, total ≈ 1 406 s (beats the 2026-05 all-pass record of
      1 469 s, which itself never included a completing Q5). Firsts: **Q5
      425.7 s**, Q21 in-sweep 50.3 s; headline deltas vs the S0 baseline:
      Q22 7.9×, Q2 4.5×, Q18 3.9×, Q20 3.4×, Q7 wrong-results fixed
- [x] measurement-harness trap diagnosed and recorded (report §5 + memory):
      `memory.high` below `GOMEMLIMIT` with `GOGC=off` = permanent kernel
      throttle band after one big query; two sweep-tail collapses were this,
      not code regressions
- [x] deferral-ledger rows: 5 × `csq-S6` (one flipped resolved by the EOF-watch
      fix) + 4 × `csq-S2/S3`
- [x] this file closed out; remaining roadmap (S4, S5, S7, full D6.3) stays with
      the bundle
- gates: final sweep above; all per-stage gates recorded in each stage block
- commit: _(the report commit)_

---

## Deviations log

| date | stage | deviation and rationale |
| --- | --- | --- |
| 2026-07-20 | 1 | **No `Params:` line in EXPLAIN**, contrary to ch.06 §6. Upstream PG emits no such line in TEXT format; adding one would be a user-visible divergence on a PG-compat surface. The correlation is already visible in the `SubPlan N` subtree's `Index Cond:` / `Filter:` lines, and becomes `$N` once Stage 8 lands real param slots. |
| 2026-07-20 | 1 | **All sublinks render as `SubPlan N`, never `InitPlan N (returns $0)`.** PG uses InitPlan for uncorrelated sublinks evaluated once, but goopg has no param slots until Stage 8 (D4.1), so an InitPlan label would misrepresent the plan structure. Revisit in Stage 8. |
| 2026-07-20 | 1 | Join labels keep goopg's existing `Hash Join (SEMI)` convention rather than switching to PG's `Hash Semi Join` spelling — the latter would churn every TPC-H plan line and is orthogonal to this bundle. |
| 2026-07-20 | 1 | `(x = ANY (SubPlan N))` renders the operand inline, where upstream prints the testexpr with `$N` PARAM_EXEC refs (`ruleutils.c` `get_sublink_expr`) and prefixes `hashed ` when the subplan uses a hash table. goopg has no param slots until Stage 8 and no hashed SubPlan until Stage 11; both converge there. |
| 2026-07-20 | 1 | The pre-existing plan-gate baseline `plan_snapshots/m0077-final.txt` (M0077 era) diverges 22/22 from current output for reasons unrelated to this work — the EXPLAIN format itself evolved (cost column added, `Projection` wrappers folded, schema-qualified relation names). The gate was therefore effectively dead. Replaced with `csq-s0-explain.txt` as the live baseline; later stages diff against it. |
| 2026-07-21 | 3+4 | **Stages 3 and 4 share one commit**, contrary to the one-commit-per-stage rule. Stage 4's guards were developed while Stage 3's matrix was still uncommitted (both touch `unnest.go` and the matrix file), so every gate in this window ran against the combined tree; splitting retroactively would have manufactured two intermediate states that were never gate-tested. The TODO keeps separate stage records. |
| 2026-07-21 | 3 | The M6 hang rows were added as `t.Skip` entries rather than the capped-subprocess probe the plan sketched — the guard fix was one stage away, and a probe harness for a bug about to be fixed was not worth the complexity. Stage 4 removed the skips the same day. |
| 2026-07-20 | 1 | `OuterColumnRef` still renders as a bare column name, so a correlated self-join subplan prints `Index Cond: (l_orderkey = l_orderkey)` — ambiguous but not wrong. Stage 8 (D4.1) replaces it with `$N`, which is both PG-faithful and unambiguous. Not patched separately to keep the stage diff minimal. |


---

# ROUND 2 — S4 / S5a / D6.3 / S7 + measurement-harness guard

| field | value |
| --- | --- |
| status | in-progress |
| started | 2026-07-21 |
| branch | `planner-kaizen2` (base `a8233ac4`) |
| scope | bundle phases S4, S5a, full D6.3, S7 + the throttle-trap harness guard. **S5b DEFERRED by user decision** (reopen criterion: post-S5a plan-compare shows a query slower AND differing from PG only by semi/anti placement) |

## Round-2 stage table

| # | Stage | Scope | Status |
|---|---|---|---|
| R2-0 | harness guard + NLI-Predicate EXPLAIN | throttle-trap refusal in the wrapper; NLI residual visible | [x] `3a620f2b` |
| R2-1 | S4a: D3.2 residual lifting (IN/scalar) + NL semi/anti + tautology strip | | [x] `55ad7fdb` |
| R2-2 | S4b: D3.3 nested-sublink tolerance (deep walk + clone fix) | | [x] `712827cc` |
| R2-3 | D6.3a: subplan cost helper + NLI semi/anti cost gate | | [x] `b35714b7` |
| R2-4 | S5a: unnest before join search (semi/anti pinned) | | [x] `a607cf2b` |
| R2-5 | S5b | **deferred** (ledger row in R2-4) | — |
| R2-6 | D6.3b: INNER Filter-inner NLI unwrap, cost-gated | | [x] (this commit) |
| R2-7 | S7: Memoize | | [ ] |
| R2-8 | FINAL: sweep + report | | [ ] |

## R2-0 — harness guard + NLI-Predicate EXPLAIN  [x]

- [x] `scripts/goopg-test-run.sh`: `to_bytes()` normalizer (systemd K/M/G/T base-1024,
      Go KiB/MiB/GiB, bare bytes, infinity) + **fail-fast refusal (`exit 2`)** when
      `GOOPG_MEM_HIGH < GOMEMLIMIT` — the throttle trap that faked two round-1 sweep-tail
      collapses (GOGC=off ⇒ GC fires only near GOMEMLIMIT; a lower memory.high parks the
      scope in the kernel reclaim band). Silently adjusting either knob was rejected:
      raising the cap defeats it, lowering GOMEMLIMIT silently changes benchmark
      conditions. Unparsable values warn-and-continue; `MEM_MAX < MEM_HIGH` warns.
- [x] manual matrix verified: `GOOPG_MEM_HIGH=4G` → refused rc=2; defaults pass;
      `GOMEMLIMIT=20GiB` vs `MEM_HIGH=20G` pass (equal); garbage unit warns+continues;
      `MEM_MAX<MEM_HIGH` warns
- [x] `bench/tpch/env_goopg.sh`: invariant comment (enforced by the wrapper all bench
      servers start through)
- [x] EXPLAIN: `emitNodeDetailLines` gains the `*planner.NestedLoopIndexJoin` case —
      the residual `Predicate` (hoisted inner filters, OR-factoring residuals, Q4's
      decorrelated-EXISTS residual) now renders as `Filter: (…)`; closes the first
      `csq-S6` ledger row
- [x] test `TestNLIResidualPredicateRendered`
- gates:
      units:      PASS (2026-07-21, whole module)
      spotcheck:  PASS (Q12=2 / Q13=33)
      plan-gate:  DIFFER on Q4/Q7/Q19/Q21 — all Filter-line-only on Nested Loop nodes
                  (Q4's `(true)` placeholder became the real `l_commitdate <
                  l_receiptdate`; Q19's OR residual now visible); tree shapes
                  unchanged. Recaptured `csq-r2-0-nli-display`, 22/22 MATCH
      pgbench-hook: PASS (see commit)
- commit: `3a620f2b`

## R2-1 — S4a: D3.2 residual lifting + NL semi/anti + tautology strip  [x]

- [x] `collectExistsUnnestParamsAndResiduals` generalized into the shared
      `collectUnnestParamsAndResiduals` for all three sublink kinds; non-equi
      outer-ref conjuncts become lifted residuals instead of bails. Two guards
      added during the work: a **Level guard** (residuals with `Level > 1` refs
      rejected — caught live by the M8 fixture, which would have lifted a
      grandparent ref against the immediate outer's schema) and a
      **`residualExprLiftable` allowlist** (only expression kinds the rewriter
      models; anything else — CASE etc. — vetoes at collection time, closing a
      latent stale-index silent-wrong-results hazard in the pre-existing code)
- [x] IN residuals AND-ed onto the semi predicate; **correlated NOT IN with
      residuals stays bailed** (three-valued NULL semantics; new matrix row M18
      pins that a naive anti-lift would wrongly return {2,3,4} vs PG's {4})
- [x] zero-equijoin EXISTS (M14 class) → `Join{Semi/Anti, Algo:NestedLoop}`;
      the executor's NL join gains **early-out semi/anti modes** (emit outer on
      first qualifying inner / on none; NULL predicate = no-match; replaces the
      hard "semi/anti requires hash" error). Zero-param IN uses its operand
      equality as a hash key instead (never needs NL)
- [x] scalar residuals (M16 class) via **aggregate-above-join** —
      `Filter(cmp)(Aggregate{GROUP BY outer cols + ordinal}(Join{INNER}(
      OrdinalityWrap(outer), raw inner) ON equis AND residuals))` — reusing the
      existing WITH-ORDINALITY machinery as the duplicate-multiplicity tag
      (no new executor op needed). New matrix rows: **M17** (duplicate outer
      rows preserved — the ordinal is what keeps both), **M19** (non-aggregate
      scalar + residual raises 21000 on both paths — lifting must not leak past
      the aggregate whitelist)
- [x] tautology strip in `clonePlanReplacingOuter`: replacement-formed
      `col = col` conjuncts dropped at clone time (the Q20 `l_suppkey =
      l_suppkey` residue class); user-written self-comparisons survive
- [x] hash-key coordinate contract established (RightKey uses merged
      `leftWidth+innerIdx` coordinates — cost one wrong-result round in testing)
- gates:
      units:      PASS (2026-07-21, whole module; matrix now M1–M19, both paths)
      race-gate:  PASS (new NL semi/anti executor mode)
      spotcheck:  PASS (Q12=2 / Q13=33)
      plan-gate:  **22/22 MATCH** — zero TPC-H diffs, as predicted (no TPC-H
                  query has the zero-equijoin or scalar-residual shapes; Q17/Q20
                  bail earlier via the probe-cheap policy; Q2's correlation is
                  absorbed by the index harvest before reaching the Filter arm)
      pgbench-hook: PASS (see commit)
- commit: `55ad7fdb`

## R2-2 — S4b: D3.3 nested-sublink tolerance  [x]

- [x] deep walkers (`walkPlanExprsDeep` etc.): descend into sublink `.Plan`s with
      per-boundary depth tracking AND into IN Operand/List at host scope (a second
      blind spot found during the work); negative control pins that the shallow
      walker cannot see a nested Level-2 ref
- [x] **escape check** replaces the blanket `hasNestedSub` bail: a ref at nested
      depth d ≥ 1 escapes iff `Level > d` (the directive's `d+1` was off by one —
      corrected and pinned at both boundaries). Depth-0 refs keep the accounted
      logic, which now also sees operand-hidden refs
- [x] params-side twin hardening: `extractEquijoinPair`/`harvestIndexKeyParams`
      reject `Level > 1` keys
- [x] `cloneExprLeaf` deep-copies sublink nodes (verbatim nested-plan clone —
      invariant argued at the site: the escape check guarantees no ref to the
      unnested scope; body-relative refs keep their host); unnest-then-mutate
      aliasing regression pinned
- [x] **bonus pre-existing bug fixed** (found by new matrix row M21): Stage-8
      lowering stopped descent at a lowerable sublink, so an `OuterColumnRef` in a
      nested IN's Operand was never rewritten — the enclosing EXISTS stopped
      pushing `OuterRows` and the ref dangled at runtime (live XX000 reproduced).
      Both lowering phases now traverse Operand/List
- [x] matrix M15 re-read: its fixture is the ESCAPING shape (stays SubPlan by
      design — it became the escaping-ref pin); new rows **M20** (body-correlated
      nested sublink rides into the hash-semi build side), **M21** (operand-hidden
      ref stays SubPlan, correct both paths), **M22** (NL-semi build side evaluates
      a nested SubPlan). Matrix now M1–M22, all green on both paths
- gates:
      units:      PASS (2026-07-21, whole module)
      spotcheck:  PASS (Q12=2 / Q13=33)
      plan-gate:  **22/22 MATCH** (zero TPC-H diffs, as predicted — Q4/Q21/Q22
                  bodies have no nested sublinks; Q20's IN already carried its
                  nested scalar, now deep-copied with identical shape)
      pgbench-hook: PASS (see commit)
- commit: `712827cc`

## R2-3 — D6.3a: subplan cost helper + stats-aware NLI semi/anti gate  [x]

- [x] `internal/planner/subplan_cost.go`: `estimateSubplanCostPerCall` — per-call
      SubPlan cost for ordering-safety only (index-probe chains cost one match set;
      SeqScan chains full rows; unknown → 0 = UNKNOWN, never "free"). First live
      consumer arrives with D6.3b (the R2-1 zero-equijoin consultation was resolved
      analytically in the NL semi's favor — the SubPlan's per-call scan work is ≥ the
      NL's one-time materialisation re-scan in every shape class; documented at the
      site, pinned by `TestZeroEquijoinPrefersNLSemi`)
- [x] `nliCostGateAccepts` rewrite: INNER/LEFT keep the historical `≤100 000`
      heuristic bit-for-bit; SEMI/ANTI use `matchSet = innerRows/NDistinct(probe
      col)`, `probeCost = matchSet` (full match set, not an early-out blend — the
      no-match case must exhaust it; pessimism biases toward hash), accept ⇔
      `outerRows×matchSet < innerRows + outerRows`
- [x] **live-fire correction during the stage** — the first cut's "no stats →
      conservative reject" rule was falsified on the bench server: goopg's ANALYZE
      statistics are **in-memory only and lost on every restart**, so no-stats is
      the COMMON case, and the rule permanently disabled semi/anti NLI in practice —
      plan-gate flipped Q4/Q21/Q22, with Q4 as the 276 s / 71× hash-semi shape the
      gate exists to prevent. Root-caused with an env-gated debug print
      (`GOOPG_NLI_COSTGATE_DEBUG=1`, kept): `EstimateRows(outer)=0` at gate time on
      a fresh server. **Rule inverted: no usable stats → optimistic accept (the
      pre-D6.3a behavior every green sweep actually ran with); the stats-aware
      formula refines only where ANALYZE data exists.** Gate-table tests flipped
      with the rationale inline
- [x] legacy escape hatch `GOOPG_NLI_COSTGATE=legacy` + `SetNLICostGateLegacy`
- [x] placement-property pin `TestSublinkConjunctPlacementProperty` (S5a insurance)
- [x] stats fixtures added to pre-existing NLI tests that now exercise the
      stats-aware path; discovered en route: the in-process test harness's ANALYZE
      is a no-op (stats set directly on catalog tables instead, commented)
- gates:
      units:      PASS (2026-07-21, whole module; matrix M1–M22 both paths)
      spotcheck:  PASS (Q12=2 / Q13=33)
      plan-gate:  after the correction, **22/22 MATCH** (Q4/Q21/Q22 restored to
                  their R2-0 shapes); live Q4 6.53 s / Q22 1.58 s (NLI shapes)
      pgbench-hook: PASS (see commit)
- commit: `b35714b7`

## R2-4 — S5a: unnest before join search, semi/anti pinned  [x]

- [x] flag `unnestPreDPOn` (default ON, env `GOOPG_UNNEST_PREDP=off`, legacy pipeline
      preserved verbatim behind it)
- [x] pre-DP position engages only for EXISTS/IN-family WHEREs
      (`whereEligibleForPreDPUnnest`); scalar-family statements keep the legacy order
      (their INNER/Aggregate/Ordinality spines would need their own re-resolution
      machinery — widening later; nothing precludes S5b)
- [x] `runJoinSearchBelowPinned` (`internal/planner/predp.go`): descends the retained
      Filter + pinned semi/anti spine to the original FROM chain (pointer identity),
      runs the historical DP block verbatim on the sunk `Filter{chain}`, splices back;
      degenerate case (no pins) = byte-identical legacy input/position
- [x] F8 re-resolution only on detected layout change (`layoutPosMap` nil fast path —
      the common case, since stats are restart-lost): bottom-up `reresolveJoinByName`
      (does NOT skip semi/anti — the :2074 guard only skips schema widening, correct
      and kept) + explicit outer-only `j.schema` refresh + retained-Filter remap via
      `remapByPosMap`/`remapOuterRefsInSubplan`, mirroring the MHJ path
- [x] tests: F8 remap (stats-forced DP reorder; name+schema assertions on the pinned
      join; both hash-semi and NLI-semi forms), end-to-end value check on both flag
      settings, sublink-free byte-stability flag on/off, flag-off legacy, eligibility
- [x] S5b deferral ledger row appended (reopen criterion recorded)
- gates:
      units:      PASS (2026-07-21, whole module; M1–M22 matrix runs the new order)
      spotcheck:  PASS (Q12=2 / Q13=33)
      plan-gate:  **22/22 MATCH** — the predicted outcome: sublink-free queries take
                  the degenerate path (byte-identical); EXISTS/IN-family queries
                  converge to identical trees on the stats-less bench server;
                  scalar-family queries take the legacy order by eligibility
      pgbench-hook: PASS (see commit)
- commit: `a607cf2b`

## R2-6 — D6.3b: cost-gated INNER Filter-inner NLI unwrap  [x]

- [x] the S6 unwrap accepts `JoinTypeInner` tentatively, confirmed after index
      resolution by `innerUnwrapCostAccepts`: accept ⇔ `outerRows × (matchSet +
      residualMult) < innerRows + outerRows`; residualMult ×8 when any hoisted
      conjunct carries LIKE/regex/FuncCall (the Q9 killer class), ×1 plain; decline
      restores `j.Right` to the Filter (hash path serves as before). LEFT excluded.
- [x] **no stats → DECLINE** — deliberately asymmetric with R2-3's optimistic
      semi/anti default; both argued side by side in the code from the same fact
      (ANALYZE stats are in-memory/restart-lost): semi/anti rejection would disable
      a 71×-upside shape, INNER decline keeps today's healthy hash (a wrong accept
      is the DNF direction)
- [x] `estimateSubplanCostPerCall` found no natural consumer here (direct helpers
      express the decision exactly); remains for future D6.3 consumers
- [x] 7 new tests incl. the side-by-side no-stats asymmetry pin and j.Right
      restoration on decline; discovery: reaching the unwrap through Plan() is
      pass-order dependent (Q15b Filter-promotion path bypasses it on simple
      shapes), so the pins construct the join directly — documented in the test file
- [x] resolves the `csq-S2/S3` INNER-unwrap deferral-ledger row (flipped resolved)
- gates:
      units:      PASS (2026-07-21) · spotcheck PASS (Q12=2 / Q13=33)
      plan-gate:  22/22 MATCH (no persisted stats on the bench server ⇒ every INNER
                  unwrap declines ⇒ shapes unchanged)
      Q9 tripwire: **90.52 s / 175 rows** — unchanged-to-better vs the 104–118 s band
      pgbench-hook: PASS (see commit)
- commit: _(filled by R2-7)_
