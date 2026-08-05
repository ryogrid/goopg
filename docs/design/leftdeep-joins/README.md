# PG-Shaped Binary Joins (Left-Deep + Bushy), PG-Style DP, and a Fusion-Free Join Executor — Design Bundle

| field | value |
| --- | --- |
| status | S0–S4 landed; S5 (the PG-shaped search) implemented and behind `GOOPG_PGSHAPED_DP`, **flag OFF** — **acceptance run 4 passes FIVE of the bar's six clauses and is held by the sixth, which has no instrument** ([09](09-verification-and-acceptance.md) §3.10, `analysis/leftdeep-joins/2026-08-05-p59run4-s5-acceptance.txt`). Run 4 is the first run with **no defect attributed to the flag anywhere in its evidence**. Clause 1 PASS (23 MATCH; the one VALUE-DIFF is Q5, the BASELINE's defect M0119-0011, where flag-ON agrees with PG 18.3 and goopg's default planner is wrong by ~24×; digests byte-identical to runs 2 and 3, so that adjudication carries). Clause 2 **PASS 0.982×** — the ON arm is FASTER than the contemporaneous integer arm (355.14 s vs 361.59 s), where runs 1–3 all read 1.36×. Clause 3 **PASS**, worst real query 1.36× (Q2), and run 3's five failures fall to Q10 0.99×, Q9 0.96×, Q18 1.04×, Q7 0.96×, Q12 1.02×; Q9's named ≤ 170.9 s bar passes at **15.83 s**. Clause 4 **PASS** — TPC-DS SF0.5 under the flag is `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`, cell-for-cell the flag-OFF baseline, after P5.9-i retired the 7 ERRORs and P5.9-j/-k the 5 TIMEOUTs. Clause 5 PASS (fourth consecutive). The §4 ratchet's `parity_violations` went **6 → 0** on the ON arm. **Clause 6 — bushy-plan capability — is UNDISCHARGED, and it is a gate on the harness before it is a gate on the planner:** §4 specifies the check as "verified through the §4 parity gate's spine diff" and `cmd/estimate-audit` has zero occurrences of "bushy" or "spine". Its `SHAPE (…-only joinrel)` labels compare RELSETS; clause 6 asks about PAIRINGS. Measured directly for run 4: PG 18.3 chooses a bushy spine on exactly Q7, Q8 and Q20 of the 22 and goopg on none, in either arm — which is not a failure by itself, since the clause hard-fails only on a shape the search cannot EXPRESS and phase 2 (`joinsearchlevel.go:171-222`) is `joinrels.c:141-198` term for term. But "enumerated and lost on cost" and "never enumerated" predict the identical observable, so run 4 cannot tell them apart. **→ P5.9-l-i BUILT that channel 2026-08-06 (`internal/estimateaudit/spine.go`, rendered from `cmd/estimate-audit` on any run with a `--reference`; [09](09-verification-and-acceptance.md) §3.11), and its first measurement REFUTES the manual reading above:** the flag-ON arm chooses a bushy spine on SIX of the 22 (Q2, Q7, Q8, Q9, Q10, Q20), not none, and on **Q20 it chooses PG's bushy partition exactly** (`{nation+supplier} &#8904; {lineitem+part+partsupp}`) &mdash; the first evidence that phase 2 builds and `add_path` keeps a bushy pair over a real five-relation TPC-H relset rather than a synthetic 4-rel chain. Every spine number moves toward PG under the flag (pairings matched 13 &rarr; 24, PG-only 44 &rarr; 33, goopg-only 45 &rarr; 32). **Two clause-6 candidates remain and only two** &mdash; PG's bushy top on Q7 and Q8 &mdash; and for those, "enumerated and lost on cost" vs "never enumerated" is still undecidable from a chosen plan. **→ P5.9-l-ii BUILT the search-side half 2026-08-06** (`internal/planner/joinsearchtrace.go` writes a `DPTRACE` block per join problem under `GOOPG_PGSHAPED_DP_TRACE=1` &mdash; every `(outer relset, inner relset, phase)` triple `makeJoinRel` was offered, plus every pair the connectivity gate declined and why; `internal/estimateaudit/enumtrace.go` parses it back through `estimate-audit --enum-trace <server log>` and adjudicates each of §3.11's candidates as `OFFERED` / `DECLINED` / `SIDE-NOT-BUILT` / `NOT-ENUMERATED` / `NO-TRACE`; [09](09-verification-and-acceptance.md) §3.12). Its pair key is `SpineJoin.PairKey`'s string byte for byte and its relation names follow `leafRel`'s alias-first rule, so the two channels' units are literally the same; goopg's OWN bushy pairings are derived as CONTROLS, and a failing control prints `VERDICT: HARNESS FAULT` and voids every candidate verdict in that run &mdash; what stops an unharvested log from reading as a planner defect. Verified live on a throwaway 4-relation cluster: the chosen plan is bushy, the trace records that partition at `phase=2` with `created=0` (a phase-1 pair reached the top relset first &mdash; itself the proof that "relset built" and "partition offered" are different questions), the alias `n1` survives, and an unconnected partition adjudicates to `SIDE-NOT-BUILT` with the side named (`analysis/leftdeep-joins/2026-08-06-p59lii-dptrace-*`). **The TPC-H measurement of Q7/Q8 with Q20 as control did NOT run** &mdash; the nightly CI batch held the host and an arm run beside it contaminates both &mdash; so clause 6 stays UNDISCHARGED; the next step is `DP_TRACE=1 PGSHAPED=1 scripts/tpch-estimate-audit-arm.sh <label> --queries 7,8,20`, then P5.9 re-runs clause 6 alone and flips or attributes |
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
