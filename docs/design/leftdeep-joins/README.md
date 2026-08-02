# Left-Deep Binary Joins, PG-Style DP, and a Fusion-Free Join Executor — Design Bundle

| field | value |
| --- | --- |
| status | draft — **DESIGN ONLY**, implementation not started |
| date | 2026-08-02 |
| scope | (a) restrict every emitted plan tree to **left-deep binary joins** (`*planner.Join` only; `MultiHashJoin` deleted as a plan node), (b) replace the bushy subset-bitmask DP with a **PostgreSQL-shaped level-wise DP** (`standard_join_search` / `join_search_one_level` analogue over `RelOptInfo` pathlists), and (c) rework the join operators so a binary hash-join cascade executes with **PG-grade efficiency** — streaming probe, zero per-level intermediate materialisation, multi-column keys, work_mem-bounded hybrid-hash spill — making both `MultiHashJoin` and the runtime fusion operator (`fusedHashJoinOp`) permanently unnecessary |
| non-goals | GEQO (genetic search) port; parallel hash **build** (leader-serial shared build stays, [parallel-query/](../parallel-query/README.md) owns it); extended statistics; bitmap heap scans; a new executor IR (`create_plan` still translates Paths to the existing `Operator` nodes) |
| baseline | PostgreSQL 18.3 under `postgres/` (read-only oracle) |
| depends on | [cost-model/](../cost-model/README.md) (the Path substrate and cost-function designs are adopted, not re-designed here); [analysis/cost-driven-second-try-200731/](../../../analysis/cost-driven-second-try-200731/README.md) (premise audit + evidence, adopted as factual record) |
| supersedes | [0034-0001-bushy-join-planning.md](../0034-0001-bushy-join-planning.md) (the bushy DP itself); [cost-model/07](../cost-model/07-cost-driven-join-order.md) §"DP over pathlists" (kept in spirit, re-shaped to PG's level-list form); [analysis/cost-driven-second-try-200731/07-cost-model-interaction.md](../../../analysis/cost-driven-second-try-200731/07-cost-model-interaction.md) §6's prohibition "no shape preference for left-deep-with-fact-outermost" (see [02](02-plan-shape-contract.md) §6 — the restriction here is a *search-space contract*, not a cost-side thumb on the scale); [0038-0001-multi-way-hash-join.md](../0038-0001-multi-way-hash-join.md) (retired on implementation of [08](08-migration-and-removal.md)) |
| directive | user (2026-08-02): plan trees are left-deep binary only; the DP searches the PG way, not bushy; the executor must run the plans that previously needed fusion as efficiently as PG does; join-operator rework is in scope |

## Chapters

| # | file | contents |
|---|---|---|
| 01 | [01-motivation-and-evidence.md](01-motivation-and-evidence.md) | why now: the M0126 NO-GO, the MHJ/fusion dead ends, the measured cascade seam cost, and the exact queries this bundle must recover |
| 02 | [02-plan-shape-contract.md](02-plan-shape-contract.md) | the left-deep binary plan-shape invariant; `BuildLeft` = PG's commuted inner/outer; why connected graphs never need an avoidable cross product; what the restriction deletes (layout remapping, MHJ coordinate round-trip) |
| 03 | [03-join-search-pg-dp.md](03-join-search-pg-dp.md) | the `standard_join_search` analogue: level-indexed joinrel lists, `join_search_one_level` restricted to its clause-join phase, `RelOptInfo` + `add_path`/`set_cheapest` on the existing `path.go` substrate, join methods generated **inside** the search, collapse limits, explicit-JOIN flattening, over-limit fallback |
| 04 | [04-cost-and-cardinality.md](04-cost-and-cardinality.md) | one cost currency; rows computed once per RelOptInfo; FK-aware join selectivity (the Q9 class-(a) fix); build-side memory realism kept PG-faithful; retirement of the integer heuristic and the ad-hoc quadratic penalty |
| 05 | [05-executor-pipeline-rework.md](05-executor-pipeline-rework.md) | the fusion-free hash cascade: probe-seam de-materialisation on the legacy Build path, `mergedKeySlot` hoisting, single-pass build, single hash map, multi-column hash keys, compiled key/residual evaluation; the equivalence argument (left-deep cascade ≡ MHJ execution pattern) |
| 06 | [06-hash-spill-and-memory.md](06-hash-spill-and-memory.md) | PG hybrid hash join: `ExecChooseHashTableSize` analogue, batch partitioning over `spillWriter`, dynamic nbatch growth, work_mem enforcement; what stays deferred (skew optimisation) |
| 07 | [07-other-join-operators.md](07-other-join-operators.md) | merge join → streaming with duplicate-group buffering; nested loop → streaming outer + Materialize inner; RIGHT/FULL as hash joins with a matched-bitmap (PG `HJ_FILL_INNER`); semi/anti/null-aware; NLI + Memoize as DP paths; parallel interplay |
| 08 | [08-migration-and-removal.md](08-migration-and-removal.md) | staged rollout behind flags, plan-snapshot re-baselines, deletion inventory for `MultiHashJoin` (28 non-test arms / 15 files) and `fusedHashJoinOp`, rollback story |
| 09 | [09-verification-and-acceptance.md](09-verification-and-acceptance.md) | acceptance bars (TPC-H SF1 22/22, per-query and total-time bars, TPC-DS SF0.5 checksum gate), the PG plan-shape parity gate, measurement hygiene |
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
planner enumerates **only left-deep binary trees** through a PG-shaped
level-wise DP in which every join **method** is a costed path generated inside
the search (no post-DP method rewrites), and the executor's binary hash join
is upgraded until a left-deep cascade is executionally identical to what MHJ
did — N base-relation hash tables built at `Open`, one streaming probe pass,
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
3. **Plan shape is a contract, not a preference** — left-deep binary is
   enforced by the enumerator's shape, never by penalty terms in the cost
   model ([02](02-plan-shape-contract.md) §6).
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

## Deliberate divergence from PostgreSQL (read this first)

PostgreSQL's `join_search_one_level` **does** consider bushy plans — the
explicit bushy phase at `postgres/src/backend/optimizer/path/joinrels.c:141`
joins pairs of composite rels. This bundle deliberately **omits that phase**
(user directive): goopg enumerates only the clause-join phase against initial
rels (`make_rels_by_clause_joins`, `joinrels.c:118`), i.e. strictly left-deep
trees. Everything else follows PG's structure, so re-admitting the bushy
phase later is a bounded, additive change (one extra loop in
[03](03-join-search-pg-dp.md) §4.3), not a rewrite. The cost of the
divergence is bounded and analysed in [02](02-plan-shape-contract.md) §5
(the Q5 build-side-subjoin case is the known casualty; the measured stake is
small next to what the executor stages recover).
