# PG-Shaped Binary Joins (Left-Deep + Bushy), PG-Style DP, and a Fusion-Free Join Executor — Design Bundle

| field | value |
| --- | --- |
| status | S0–S4 landed; S5 (the PG-shaped search) implemented and behind `GOOPG_PGSHAPED_DP`, **flag OFF** — **acceptance run 3 is a third documented NO-GO** ([09](09-verification-and-acceptance.md) §3.5, `analysis/leftdeep-joins/2026-08-05-p59run3-s5-acceptance.txt`), **and the first that fails on performance alone: clause 1 PASSES** (23 MATCH; the one VALUE-DIFF is Q5, the BASELINE's defect M0119-0011, where flag-ON agrees with PG 18.3 and goopg's default planner is wrong by ~24×). Flag-owned wrong answers across three runs: 4 → 2 → **0**, and TPC-DS SF0.5 under the flag returns **MISMATCH=0 CKMISMATCH=0** across 99 queries. Clause 5 PASS (third consecutive); Q9's named ≤ 170.9 s bar PASSES at 54.95 s. What remains is two named defects. **P5.9-h — the estimate collapse, now HALF CLOSED** ([09](09-verification-and-acceptance.md) §3.6): the `rows=1` was minted after the search by `EstimateRows`, which answered 1 for every index scan on the equality-probe convention — wrong for the bound-less full scan P5.4c-ii-b introduced. One arm fixed it and `parity_violations` went **6 → 0** on the five carrying queries (Q12's `orders` leaf `rows=1` → `rows=1500000`). **But the plan shapes and timings did not move**, so "the timing gap IS the estimate collapse" is refuted: what is left of -h is the COST question of why a full ordered index scan of `orders` beats a Seq Scan + sort under the flag. **P5.9-i — the 7 TPC-DS CTE/UNION-ALL aborts, CLOSED** ([09](09-verification-and-acceptance.md) §3.7): the assertion was right to fire and the arms were innocent — `reresolveJoinByName`'s `predRebind` could not tell a resolution MISS from an AMBIGUITY, so its cross-side fallback rebound a correctly-bound reference onto another CTE scan's identically-named column (`(Name, SourceTableIdx)` is not a column identity ACROSS scopes, only within one). DS05 under the flag went `ERROR=7` → `PASS=6 … ERROR=0 TIMEOUT=1`, five with PG-identical checksums. The TIMEOUT is **Q47, a NEW defect (P5.9-j)**: correct answer, 8 m 40 s against 11–13 s flag-OFF — uncovered, not caused, by -i, and the same cost class as -h's remainder. Shape moved the right way: divergences 67 → 46, matched joinrels 21 → 32. S6/S7 blocked on P5.9-h/-j |
| date | 2026-08-02 (status 2026-08-05) |
| scope | (a) restrict every emitted plan tree to **PG-shaped binary joins** — left-deep chains *and* bushy composite-composite joins, exactly the shapes PG 18.3's `join_search_one_level` can produce (`*planner.Join` only; `MultiHashJoin` deleted as a plan node), (b) replace the subset-bitmask DP with a **PostgreSQL-shaped level-wise DP** (`standard_join_search` / `join_search_one_level` analogue over `RelOptInfo` pathlists, all three phases — clause joins, bushy, last-ditch), and (c) rework the join operators so a binary hash-join cascade executes with **PG-grade efficiency** — streaming probe, zero per-level intermediate materialisation, multi-column keys, work_mem-bounded hybrid-hash spill — making both `MultiHashJoin` and the runtime fusion operator (`fusedHashJoinOp`) permanently unnecessary |
| non-goals | GEQO (genetic search) port; parallel hash **build** (leader-serial shared build stays, [parallel-query/](../parallel-query/README.md) owns it); extended statistics; bitmap heap scans; a new executor IR (`create_plan` still translates Paths to the existing `Operator` nodes) |
| baseline | PostgreSQL 18.3 under `postgres/` (read-only oracle) |
| depends on | [cost-model/](../cost-model/README.md) (the Path substrate and cost-function designs are adopted, not re-designed here); [analysis/cost-driven-second-try-200731/](../../../analysis/cost-driven-second-try-200731/README.md) (premise audit + evidence, adopted as factual record) |
| supersedes | [0034-0001-bushy-join-planning.md](../0034-0001-bushy-join-planning.md) (the bushy DP itself); [cost-model/07](../cost-model/07-cost-driven-join-order.md) §"DP over pathlists" (kept in spirit, re-shaped to PG's level-list form); [analysis/cost-driven-second-try-200731/07-cost-model-interaction.md](../../../analysis/cost-driven-second-try-200731/07-cost-model-interaction.md) §6's prohibition "no shape preference for left-deep-with-fact-outermost" (see [02](02-plan-shape-contract.md) §6 — the restriction here is a *search-space contract*, not a cost-side thumb on the scale); [0038-0001-multi-way-hash-join.md](../0038-0001-multi-way-hash-join.md) (retired on implementation of [08](08-migration-and-removal.md)) |
| directive | user (2026-08-02, amended 2026-08-03): **PG-identical join search** — plan trees are PG-shaped binary joins (left-deep + bushy) exactly as PG's `join_search_one_level` produces them, with PG's full three-phase DP; the executor must run the plans that previously needed fusion as efficiently as PG does; join-operator rework is in scope |

## Chapters

| # | file | contents |
|---|---|---|
| 01 | [01-motivation-and-evidence.md](01-motivation-and-evidence.md) | why now: the M0126 NO-GO, the MHJ/fusion dead ends, the measured cascade seam cost, and the exact queries this bundle must recover |
| 02 | [02-plan-shape-contract.md](02-plan-shape-contract.md) | the PG-shaped binary plan-shape invariant (left-deep + bushy); `BuildLeft` = PG's commuted inner/outer; why connected graphs never need an avoidable cross product; what PG-shaped binary trees simplify (relid-order canonical layout replacing the remap family) |
| 03 | [03-join-search-pg-dp.md](03-join-search-pg-dp.md) | the `standard_join_search` analogue: level-indexed joinrel lists, all three `join_search_one_level` phases (clause joins, bushy composite-composite joins, last-ditch), `RelOptInfo` + `add_path`/`set_cheapest` on the existing `path.go` substrate, join methods generated **inside** the search, collapse limits, explicit-JOIN flattening, over-limit fallback |
| 04 | [04-cost-and-cardinality.md](04-cost-and-cardinality.md) | one cost currency; rows computed once per RelOptInfo; FK-aware join selectivity (the Q9 class-(a) fix); build-side memory realism kept PG-faithful; retirement of the integer heuristic and the ad-hoc quadratic penalty |
| 05 | [05-executor-pipeline-rework.md](05-executor-pipeline-rework.md) | the fusion-free hash cascade: probe-seam de-materialisation on the legacy Build path, `mergedKeySlot` hoisting, single-pass build, single hash map, multi-column hash keys, compiled key/residual evaluation; the equivalence argument (any PG-shaped binary cascade ≡ MHJ execution pattern) |
| 06 | [06-hash-spill-and-memory.md](06-hash-spill-and-memory.md) | PG hybrid hash join: `ExecChooseHashTableSize` analogue, batch partitioning over `spillWriter`, dynamic nbatch growth, work_mem enforcement; what stays deferred (skew optimisation) |
| 07 | [07-other-join-operators.md](07-other-join-operators.md) | merge join → streaming with duplicate-group buffering; nested loop → streaming outer + Materialize inner; RIGHT/FULL as hash joins with a matched-bitmap (PG `HJ_FILL_INNER`); semi/anti/null-aware; NLI + Memoize as DP paths; parallel interplay |
| 08 | [08-migration-and-removal.md](08-migration-and-removal.md) | staged rollout behind flags, plan-snapshot re-baselines, deletion inventory for `MultiHashJoin` (28 non-test arms / 15 files) and `fusedHashJoinOp`, rollback story |
| 09 | [09-verification-and-acceptance.md](09-verification-and-acceptance.md) | acceptance bars (TPC-H SF1 22/22, per-query and total-time bars, TPC-DS SF0.5 checksum gate), the PG plan-shape parity gate, measurement hygiene, and §3.3's two-arm result-digest instrument — clause 1 is VALUE equality via `tpch-runner -diff`, not row counts |
| — | [IMPLEMENTATION-TODO.md](IMPLEMENTATION-TODO.md) | phased task ledger (P0…P6) with gates and deferral pointers |

## The design in one paragraph

goopg's join planner today emits **bushy** trees from a subset-bitmask DP
(`enumerateBushyPlans`, `internal/planner/bushy.go:722`) and then repairs the
result with post-hoc rewrite passes (`rewriteMultiWayChain`,
`rewriteJoinsToNLI`); its executor pays a per-level re-materialisation tax at
every hash-join probe seam, which is the entire reason `MultiHashJoin` (an
n-ary plan node) and `fusedHashJoinOp` (a runtime n-ary rewrite) ever existed.
Both workarounds are now dead ends by evidence: MHJ is retired
(`bushy.go:586`, commit `e85e5347`) and fusion is permanently disabled for
correctness (`analysis/cost-driven-second-try-200731/evidence/stage2-fusion-verdict.txt`).
This bundle removes the *cause* instead of re-enabling either workaround: the
planner enumerates **PG-shaped binary trees** — left-deep chains plus the
bushy composite-composite phase — through a level-wise DP that mirrors PG's
`join_search_one_level` in all three of its phases (clause joins against
initial rels, bushy joins of composite rels, and the last-ditch clauseless
fallback), and in which every join **method** is a costed path generated
inside the search (no post-DP method rewrites). The executor's binary hash
join is upgraded until a cascade is executionally identical to what MHJ
did — N hash tables built at `Open`, one streaming probe pass,
zero intermediate materialisation — plus PG's hybrid-hash spill so large
builds degrade gracefully instead of OOMing.

## Invariants (carried from the cost-model bundle, extended here)

1. **One absolute cost function everywhere** — the integer relative cost
   (`estimateJoinCost`'s `outputRows·1 + build·4 + probe·1`) is retired with
   the bushy DP; `Cost{Startup, Total}` in PG units is the only currency
   ([04](04-cost-and-cardinality.md) §1).
2. **Rows are computed once** — each `RelOptInfo` carries its row estimate;
   costing never re-walks the plan tree via `EstimateRows`
   ([04](04-cost-and-cardinality.md) §2).
3. **Plan shape is a contract, not a preference** — **binary join trees,
   PG-identical shape**: the enumerator can express exactly the tree shapes
   PG's `join_search_one_level` can produce (left-deep chains and bushy
   composite-composite joins), enforced by the enumerator's shape, never by
   penalty terms in the cost model ([02](02-plan-shape-contract.md) §6).
4. **Method selection happens inside the search** — a join order is never
   chosen under one method's cost and executed under another's
   ([03](03-join-search-pg-dp.md) §5; this is the doc-12 lesson: "order first,
   method later" caused the first C4 regression).
5. **The executor change is measured at the seam** — every de-materialisation
   stage counts **probe-side seams, not tree levels**
   ([05](05-executor-pipeline-rework.md) §2; the second-try bundle's
   correction).
6. **PG semantics for every operator** — where this bundle touches a join
   operator, the reference behaviour is the named `postgres/` function, and
   any deliberately skipped piece gets a `.ralph/deferral_ledger.md` row.

## PG fidelity (read this first)

PG's `join_search_one_level`
(`postgres/src/backend/optimizer/path/joinrels.c:73`) has **three phases**:
(1) clause joins of level-(lev−1) rels against initial rels
(`make_rels_by_clause_joins`, `joinrels.c:118`, with the per-rel clauseless
cartesian branch at `joinrels.c:120-137`), (2) **bushy joins** of composite
rel pairs (k, lev−k) for 2 ≤ k ≤ lev−2, gated on a connecting join clause or
join-order restriction (`joinrels.c:141-198`), and (3) the last-ditch
clauseless pass when a level came up empty (`joinrels.c:200-256`). This
bundle implements **all three phases**, PG-verbatim in structure — the
search space goopg enumerates is exactly PG's, so any binary tree PG can
emit, goopg can emit (modulo cost/stats fidelity, measured by the
[09](09-verification-and-acceptance.md) §4 parity gate). Two deliberate,
bounded scope choices remain (both recorded in
[03](03-join-search-pg-dp.md) §4.4): `join_is_legal` constraint inference is
not implemented in v1, so outer/semi/anti joins stay pinned opaque inputs as
a *temporary* measure, and GEQO is not ported ([03](03-join-search-pg-dp.md)
§7) — neither is a shape divergence.
