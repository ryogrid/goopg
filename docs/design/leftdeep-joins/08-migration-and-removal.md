# 08 — Migration, Flags, and the Removal Inventory

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| principle | executor first, planner second, deletion last; every stage independently shippable and independently revertible; the tree is never committed in a state where the default plan path is worse than the previous commit's on the standing gates |

## 1. Ordering rationale

The executor stages (E1–E5) improve the **current** default planner's output
immediately — the MHJ-retirement regressions (Q3/Q10/Q18/Q7) are seam costs
in today's binary cascades. They carry no planner risk and de-risk the DP
switch: by the time the new enumerator lands, the plans it emits (all
binary cascades) already run on a fixed executor. Reversed ordering would
couple a planner regression surface to an executor regression surface in
one diff — the M0125-0002 lesson (direction unpredictable per query; must
measure per commit).

## 2. Stages and flags

| stage | content | flag / default | gate to advance |
|---|---|---|---|
| **S0** | E2 (`mergedKeySlot` hoist) + E3 (single-pass single-map build) | none — unconditional (pure wins) | units + spotcheck + pgbench smoke; stage0-style A/B shows no query worse |
| **S1** | E1 (legacy-path seam de-materialisation) | `GOOPG_JOIN_SLOT_CHAIN` default ON, env kill-switch OFF only | full regress-port + TPC-H SF1 sweep + TPC-DS SF0.5 checksum gate; Q3/Q10/Q18/Q7 vs R0 ≤ 1.2× |
| **S2** | E4 (multi-column keys, planner+executor) | plan-affecting → plan-snapshot re-baseline in same commit | spotcheck + SF0.5 + Q78-class degeneracy probe; `reselectDegenerateHashKeys` deleted same commit |
| **S3** | [06](06-hash-spill-and-memory.md) hybrid hash spill | `work_mem`-honouring ON; `GOOPG_HASH_SPILL=off` escape | Q21 SF1 completes under cgroup cap; no-spill plans byte-identical results |
| **S4** | [07](07-other-join-operators.md) §§2–4 (streaming merge, hash outer-fill, Materialize+NL) | per-operator, plan-affecting parts follow S5's flag | regress-port outer-join files; SF0.5 |
| **S5** | the new PG-shaped DP ([03](03-join-search-pg-dp.md) — all three `join_search_one_level` phases, including the bushy phase of §4.3) + cost binding ([04](04-cost-and-cardinality.md)) | **FLIPPED ON 2026-08-06 (M0127-P5.9)** — `GOOPG_PGSHAPED_DP` was OFF while soaking and is now the default; it survives as a KILL-SWITCH, where only the exact string `0` restores `tryBushyDP` (unset means ON), which is this row's rollback story until S7. `GOOPG_COST_DRIVEN_JOINORDER` is retired: the env hook is deleted, while `costDrivenJoinOrder` and `SetCostDrivenJoinOrder` stay with the old DP until S7 deletes it. Evidence: [09](09-verification-and-acceptance.md) §3.10 (run 4, five clauses), §3.13 (clause 6 measured) and §3.14 (the flip, and the 24 unit tests it moved). The collapse-ON pass of this row's gate has NOT been run and gates the COLLAPSE flip, not this one — see §3.14. The collapse-limit wiring ([03](03-join-search-pg-dp.md) §6) gets its **own sub-flag** (`GOOPG_PGSHAPED_COLLAPSE`) and soaks separately: it changes *which statements enter the search at all* (explicit-JOIN flattening), and coupling that population change to the enumerator swap would make S5 regressions unattributable | the full [09](09-verification-and-acceptance.md) bar, including Q9 — run once with collapse OFF (comma-FROM population only), then with collapse ON |
| **S6** | E5 (compiled key/residual eval) | none (behaviour-neutral) | units + parity spot-diffs on expression corpora |
| **S7** | deletion (§4) | none — deletions only after S5 default-ON has survived ≥ 1 clean nightly cycle | nightly green; grep-clean inventory below |

Rollback story: S0/S2/S6 revert by commit; S1/S3 flip their env switch; S5
flips `GOOPG_PGSHAPED_DP` OFF, restoring the current `tryBushyDP` enumerator
(goopg's subset-bitmask DP, which is itself bushy-capable) **which is not
deleted until S7**. Both enumerators coexist behind the flag during the soak —
the `tryBushyDP` entry point stays callable, exactly how
`mhjPackingEnabled`/`SetMHJPackingEnabled` kept MHJ revivable after
M0126-0011.

## 3. Coexistence rules during the soak

- Non-searched shapes (explicit JOIN trees under collapse limits until §6 of
  [03](03-join-search-pg-dp.md) is on; subquery interiors; unnest pins) keep
  the current pipeline **including** `rewriteJoinsToNLI` and the qual-
  placement passes — those passes must not double-fire on searched subtrees
  (searched roots are tagged; the passes skip tagged subtrees).
  **Landed (M0127-P5.9-b):** seven skips, all keyed on `isSearchedTree`. Three
  RENUMBER a tree and were done at P5.5-f-ii-a (`buildBindingsPosMap`'s
  collector, `applyJoinTreePosMap`, `reconcileNLILayout`); four REWRITE one and
  are done here (`pushOneConjunct` — hence `pushPredicatesIntoCrossJoins`,
  `walkRewriteNLI` — hence `rewriteJoinsToNLI`, `rewriteMultiWayChain`,
  `rewriteScanInputsWithSingleTablePredicates`). The rule they enforce is about
  COORDINATES, not duplication: each addresses a join tree in the statement's
  FROM-cumulative space, and only a searched tree's ROOT is in that space
  ([03](03-join-search-pg-dp.md) §10).
  **Amended (M0127-P5.9-c): EIGHT skips.** The eighth is `remapTopProjection`
  (bushy.go), and its omission is what made the P5.9 acceptance run a no-go —
  see [09](09-verification-and-acceptance.md) §3.2. It belongs to neither of the
  two families above, which is why the P5.9-b audit missed it: it does not
  rewrite a join tree and it does not renumber one, it renumbers the WRAPPERS
  above one. It nonetheless reaches into a searched subtree, because it locates
  the tree to derive its posMap from by walking down past `*Project` / `*Sort`
  wrappers — and the search boundary is a `*Project` (or, for an elided
  boundary over a sorted root, a `*Sort`). The `collect`-side guard cannot
  help: it is never asked about the root. The generalised rule is therefore
  **any pass that DESCENDS THROUGH a node kind the boundary can be, not only a
  pass that rewrites one**; the standing tripwire for the class is
  `assertSearchedBoundariesIntact` at the tail of `Plan()`.
- `reconcileNLILayout` (`planner.go:99`) keeps running until S7 confirms no
  searched plan needs it (the canonical relid-ordered layouts of
  [02](02-plan-shape-contract.md) §3 should make it a no-op on searched
  trees — assert, don't assume: debug-mode check that it changed nothing on
  a searched tree, log+file a bug if it did).
- Plan snapshots: S2 and S5 each re-baseline `plan_snapshots/` in the same
  commit with the diff summarised in the commit message (the
  `post-mhj-retire.txt` precedent).

## 4. Deletion inventory (S7)

**Planner** — with the old subset-bitmask DP (not the new PG-shaped DP,
which stays):
`enumerateBushyPlans`, `enumerateSubsets`, `enumerateSplits`,
`dp map[uint16]dpEntry`, `estimateJoinCost` + integer
weights, `attachUnusedCrossEdges`, `bushySeedRowCounts`, the
`len(tables) > 12` cap, `IsSmallDimensionSide` build pinning
(`buildJoinFromDP`), `chooseInnerJoinAlgo` for searched joins,
`joinorder.go`'s authority role (file shrinks to the over-ceiling
sequencer).

**The layout/remap family splits in two** under the canonical relid-ordered
layout ([02](02-plan-shape-contract.md) §3):
- *Subset-internal* machinery — `dpEntry.layout` and the per-subset remap
  pair (`remapKeyToLayout`, `mergeSubsetLayouts`) — **is deleted**: with the
  canonical layout, a joinrel's column order is a pure function of its
  relset, so no enumeration-time layout state survives, and under bushy
  shapes there is no prefix-sum composition it could be replaced by anyway.
- *Search-boundary* translation — `buildBindingsPosMap` and
  `applyJoinTreePosMap` — is **held back** until the [03](03-join-search-pg-dp.md)
  §10 boundary map is proven in production. They are today's
  search-boundary translation and the pinned-spine re-resolution consumes
  them; deleting them before the replacement is validated is the S7 change
  most likely to regress. Bushy makes the hold-back sharper, not looser:
  the replacement is a single relset permutation (canonical relid order →
  syntactic order) absorbed by a root `Project`, with no chain-order
  simplification available.
(`costJoinCandidate`'s quadratic penalty is NOT in this inventory — it is
already deleted at S5 per [IMPLEMENTATION-TODO](IMPLEMENTATION-TODO.md)
P5.6.)

**MultiHashJoin** — ~34 non-test `case *MultiHashJoin:` arms across 18
files at the 2026-08-02 grep (re-inventory at S7 time; counts drift
upward), notably:
plan node (`plan.go:1170`), packing (`rewriteMultiWayChain`,
`collectMultiHashTables`, `bushy.go:1549-1868`), `mhj_input_rewrite.go`
(903 lines), `remapExprRefsToMHJ`/`buildMHJPosMap` (`bushy.go:2081`/`:2385`),
`pushResidualQualsIntoMHJTables`, `estimateMultiHashJoin`
(`cardinality.go:173`), `multiHashJoinCost` (`cost_funcs.go:188`),
`generateMultiHashJoinPath` (`pathgen.go:105` — settling the 0126-0011 §3
disposition question: deleted), executor operator (`multi_hash_join.go`,
696 lines) + `executor.go:301` arm, EXPLAIN arms
(`operators_explain.go:1386`, `:1562`), `tryBushyDP` leaf-whitelist entry,
`mhjPackingEnabled`/`SetMHJPackingEnabled`/`GOOPG_MHJ_PACKING_OFF`.

**Fusion** — `fused_hash_join.go` (707 lines: `fusedHashJoinOp`,
`tryFuseHashCascade`, `buildEnvInFlight` threading), the `executor.go:160-163`
hook, `GOOPG_RUNTIME_JOIN_FUSION*` env vars, and the planner exports that
exist only for it (`IsCanonicalKeyEquality` — verify no other caller at S7).

**Deferred-deletion tests**: every test asserting MHJ/fusion behaviour is
deleted or re-pointed at the cascade in the same commit; the SF0.5 oracle
and TPC-H anchors are plan-shape-independent (row counts/checksums) and
survive unchanged.

## 5. Documentation obligations (same-commit, per repo rule)

- `docs/design/README.md`: bundle bullet under `## Design Bundles` (added
  with this bundle); status flips (`draft` → `accepted`) as stages land.
- Supersession stamps at S7: [0034-0001](../0034-0001-bushy-join-planning.md),
  [0038-0001](../0038-0001-multi-way-hash-join.md), and the MHJ chapters of
  the 0043/0063/0125/0126 lines get `superseded by: leftdeep-joins/` headers
  (never deleted);
  [cost-model/09](../cost-model/09-verification-and-acceptance.md) §3's
  "MultiHashJoin where PG uses a left-deep hash cascade" allowance is struck.
- Every deliberately-skipped PG behaviour named in chapters 03–07 (GEQO,
  skew buckets, SpecialJoinInfo semi/anti in-DP, shared spilling builds,
  full join_order_restriction inference) gets its
  `.ralph/deferral_ledger.md` row when its stage lands — the ledger, not
  this bundle, is the resume-point authority. The semi/anti-in-DP and
  join_order_restriction rows carry the **`join_is_legal`-inference-dependent**
  marker ([03](03-join-search-pg-dp.md) §4.4): the bushy shape capacity they
  need is implemented ([03](03-join-search-pg-dp.md) §4.3), so neither is
  blocked on search shape anymore — what blocks them is `join_is_legal`
  constraint inference, and neither may be resumed as a standalone
  increment before that lands.

## 6. Interaction with live M0126 state

This bundle **becomes** the successor to M0126's open tail: -0013's
"join-enumeration improvement or fusion-operator integration" residual
resolves to the former; the M0126-0004 deferral un-defers as stage S1. The
`GOOPG_COST_DRIVEN_JOINORDER` flag and its documentation retire at S5 (its
acceptance protocol — symmetric timeouts, order-attribution taxonomy,
class-(a)/(c) analysis — is inherited by [09](09-verification-and-acceptance.md)
§6 unchanged). Nothing here modifies M0124/M0125 scope.
