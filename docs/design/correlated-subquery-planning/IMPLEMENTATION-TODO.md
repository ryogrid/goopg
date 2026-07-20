# IMPLEMENTATION-TODO — correlated-subquery-planning, phases S0–S3

| field | value |
| --- | --- |
| status | in-progress |
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
| 1 | S0-1 EXPLAIN rendering | S0 | ch.06 §6 | [ ] |
| 2 | S0-2 SubPlan counters | S0 | V6 | [ ] |
| 3 | S0-3 semantics matrix + SF1 baseline | S0 | V1, V2, V4, W1 | [ ] |
| 4 | S1a live-bug guards | S1 | D3.0 (F1, F2, F3) | [ ] |
| 5 | S1b NOT-IN executor fix | S1 | F4 | [ ] |
| 6 | S1c collector fix | S1 | D3.0 (IndexScan.Key) | [ ] |
| 7 | S2a operator resets | S2 | D4.2 (prereq) | [ ] |
| 8 | S2b param slots | S2 | D4.1 | [ ] |
| 9 | S2c SubPlan handles | S2 | D4.2 + cacheability gate | [ ] |
| 10 | S2d cache + lifecycle | S2 | D4.4, D4.5 | [ ] |
| 11 | S3 hashed SubPlan | S3 | D4.3 | [ ] |
| 12 | FINAL measurement + report | — | V4/V5 | [ ] |

Order: 1 → 2 → 3 → 4 → 5 → 6 sequential (ch.03 §2.5 mandates guards before the collector
fix); 7 independent, landed early to soak alone; 8 → 9 → 10 → 11 sequential; 12 last.

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
- commit: _(filled in by Stage 3)_

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
- commit: _(filled in by Stage 6)_

## Stage 6 — S1c: D3.0 collector fix (`IndexScan.Key` harvest)  [ ]

- [ ] harvest equality `Key`/`Keys[i]` correlations (range `LowKey`/`HighKey` still bail)
- [ ] `clonePlanReplacingOuter` emits `SeqScan` instead of a circular self-probe IndexScan
- [ ] post-clone assertion: zero `OuterColumnRef` remains in the pulled-up inner plan
- [ ] P1–P6 probe regression tests + index-toggle determinism test
- [ ] MHJ no-re-collapse / no-residual-duplication invariants (ch.03 §9)
- [ ] Q4 / Q21 / Q22 plan-shape assertions
- [ ] probes re-run and archived as `evidence/unnest-probes-<newhead>.txt`
- gates:
      units / spotcheck (**tripwire**) / plan-gate (review → recapture `csq-s1-collector`)
      perf: P-S1a (Q4 ≤ 3 s), P-S1b (Q22 ≤ 1 s) — _pending_
- commit: _pending_

## Stage 7 — S2a: operator reset prerequisites  [ ]

- [ ] `limitOp.Open` resets `skipped` / `emitted` / `inTiesPhase` / `tieKeyVals`
- [ ] `seqScanOp` re-open rewinds without leaking `sctx` / scan ring or re-recording SIREAD
- [ ] double-`Open` unit tests for Limit / SeqScan / Sort(Close+Open)
- gates: units / spotcheck / pgbench-hook — _pending_
- commit: _pending_

## Stage 8 — S2b: D4.1 param slots  [ ]

- [ ] `ExecParamRef` planner node (distinct from `ParamRef` = PARAM_EXTERN)
- [ ] `ParParam` / `Args` on sublink nodes; depth-tracked lowering pass; Level ≥ 2 forwarding
- [ ] `Context.ParamExec` + `ParamDirty`
- [ ] EXPLAIN renders correlated refs as `$N` (PG-faithful)
- gates: units / spotcheck / plan-gate / pgbench-hook — _pending_
- commit: _pending_

## Stage 9 — S2c: `subPlanHandle` rescan-not-rebuild  [ ]

- [ ] per-rooted-operator rescan policy (IndexScan/Aggregate re-Open; SeqScan rewind;
      Sort/MHJ/NLI Close+Open)
- [ ] cacheability classifier: volatile function or `LockRows` inner ⇒ never cached,
      never rescan-skipped (ch.07 M13; PG-fidelity blocker fix)
- [ ] kill switch `GOOPG_SUBPLAN_RESCAN=off`
- [ ] all three eval sites routed through handles
- gates: units / spotcheck / **race-gate** / V6 shows rebuilds→0 / pgbench-hook — _pending_
- commit: _pending_

## Stage 10 — S2d: projected cache keys + lifecycle  [ ]

- [ ] `kvcache` hash+LRU library with byte accounting (reused by Stage 11)
- [ ] projected cache keys (expr pointer + parParam datums) — fixes the correlated
      `collectInValues` key-collision wrong-results hazard
- [ ] retire `CorrSubqOps` / `CorrSubqHashMaps` / `SubqueryCacheScope`
- [ ] `CloseSubPlans()` at statement end (idempotent); WorkMem budget, `WorkMem == 0` = unlimited
- gates: units / spotcheck / race-gate / P-S2 / pgbench-hook — _pending_
- commit: _pending_

## Stage 11 — S3: hashed SubPlan  [ ]

- [ ] two-hashtable NULL-correct hash execution for uncorrelated IN / NOT IN SubPlans
- [ ] hashability gate (estimated rows × width vs WorkMem); linear scan becomes fallback
- [ ] kill switch `GOOPG_HASHED_SUBPLAN=off`
- [ ] M1 / M2 / M10 exercised through the hashed path (unnest knob off)
- gates: units / spotcheck / race-gate / P-S3 / pgbench-hook — _pending_
- commit: _pending_

## Stage 12 — FINAL: measurement and report  [ ]

- [ ] full 22-query capped SF1 runs: before (Stage 3 baseline) / after / PG 18.3 reference
- [ ] report written to `analysis/tpch-csq-s0s3-verification-<YYYYMMDD>.md`
- [ ] bundle refresh: ch.01 scoreboard, dossier gate labels, `evidence/` archives
- [ ] `.ralph/deferral_ledger.md` rows for every deliberate exclusion
- [ ] this file closed out with all gate lines filled
- gates: full V5 sweep — _pending_
- commit: _pending_

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
