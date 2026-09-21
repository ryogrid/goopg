# M0145 — Jointree-first planner flow

**Filed:** 2026-09-20 (owner decision). **Status:** planned.
**Basis:** `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/plan-flow-medium-abstraction.md`
(the six medium-level divergences) + `tmp/planner-rewrite-possibility260920.md`
(feasibility assessment) + `docs/design/0100-0149/m0142-0008a-3i-lateral-route-recon.md`
(the wall is the resolver, not the phases).

## Why this milestone exists

Four independent tasks (M0142-0008a-3 increments (i)/(ii), `-3i-lateral`,
M0144-0003a, M0144-0003b's residual) hit one wall: **goopg's join search
consumes a lowered plan-node subtree, while PG's consumes a jointree**. The
pinned Semi/Anti spine, Phase A/B split, leaf-count/lateral admission gates
and splice-time re-resolution are all artefacts of that misplaced boundary —
none has a PG counterpart. Continuing to patch per-plan divergences leaves
the ~72%-structural first-divergence class (M0144-0007) untouched.

The fix is not a rewrite: it is **moving the boundary**. The
`tryPGShapedJoinSearch` lattice (RelOptInfo, pathlists, partial pathlists,
DPPATH provenance) already implements PG's search; M0145 gives it a
jointree-level input and statement-wide span, then a single Path→Node
lowering replaces the interleaved stage-builder resolutions.

## Strategy

- **Parallel pipeline, not in-place rewrite.** `GOOPG_JOINTREE_PIPELINE=1`
  selects the new pipeline (precedent: `GOOPG_PGSHAPED_DP`, unnestPreDP).
  Default stays the legacy pipeline until the 0008 cutover; all value gates
  keep running on it (AGENT.md G8).
- **PG as the spec, refusal-for-refusal.** IR entries map to PG's jointree
  nodes; `join_is_legal` (joinrels.c:350) is the legality port target;
  `is_simple_subquery` (prepjointree.c:1807) is already ported as
  `sublinkBodyIsSimple`.
- **Hard constraint:** Q78's `outer-over-derived` firewall must not weaken
  in any pull-up/flattening task (owner constraint).

## Tasks

| id | kind | summary |
|---|---|---|
| M0145-0001 | recon | Jointree-IR design + the Path→Node lowering contract + retirement list for seam guards |
| M0145-0002 | impl | Dual-pipeline harness: `GOOPG_JOINTREE_PIPELINE` knob, EXPLAIN-only parity capture for the new arm, per-divergence-class reporting |
| M0145-0003 | impl | Sublink pull-up into the jointree (`pull_up_sublinks`/`pull_up_subqueries` analogue); pilot = route-a step 2; absorbs M0144-0003a + the lateral-route wall |
| M0145-0004 | impl | UNION ALL → appendrel at jointree level (`pull_up_simple_union_all` analogue); absorbs M0144-0003b's residual |
| M0145-0005 | impl | Single-pass DP over the jointree; semi/anti as legal searched citizens; retires Phase A/B + pinned spine + splice re-resolution |
| M0145-0006 | impl | Upper-rel pathlists (grouping/ordered/window/distinct elections over candidate sets); absorbs M0144-0011a's residual gates |
| M0145-0007 | impl | Single Path→Node lowering pass (create_plan analogue) consolidating all post-election resolution |
| M0145-0008 | impl | Cutover: default flip, full gate suite on the new pipeline, legacy pipeline + dead guard deletion, executor-substrate handoff list |
| M0145-0009 | impl | CTE-output statistics (B-06 resume — TODO_ALL B-06 / ledger `take3-B-06-deferred`): wire the landed inert synthesis (`cte_stats_synthesis.go`, design `docs/design/planner-b06-cte-stats/`) into the estimator. On completion the blocked unlifts are RE-EVALUATED, not auto-done: `flattenPulledBodyTree`'s bare-`*SeqScan` rule (0003's ANY-CTE residue), the `outer-over-derived` firewall (its own named resume condition — owner hard constraint, ambiguous evidence escalates), and the `rows<=1` guard |

Dependencies: 0001 → {0003, 0004} → 0005 → {0006, 0007} → 0008.
0002 is independent and should land early so every later task is measurable.
0009 is independent of the flow work (it is a statistics task, not a flow
task); it is sequenced last so its estimates land on the cutover pipeline,
but nothing in 0003–0008 is gated on it — the three consumers only need it
before their own unblocks are re-evaluated.

## Handoffs from M0137–M0144

- **M0142-0008a-3i-route-a** step 2 (flattening splice) is the pilot of
  0003 — implement against the IR if 0001 lands first. Step 1
  (`.Subquery` retention + `sublinkBodyIsSimple`) landed `6d2c6b9ad`.
- **M0144-0003a / 0003b** stay `[!]` — superseded by 0003/0004.
- **M0142-0008a-3** increments (i)/(ii)/(iii) are the pinned-spine route to
  the same end state; re-verify which remain meaningful after 0005.
- **M0144-0011a** residual gates → owned by 0006.
- **Executor substrate is OUT of scope** and unchanged in priority:
  parallel-hash-build (`joinpathsparallel.go:59-61` refusal), row-emitting
  PartialAgg (`ctx.PartialAggStates` model), Materialize node
  (M0144-0011c sizing). These are parity-enablers the new flow will
  *generate shapes for*; admission gates stay until they land. M0145-0008's
  cutover hands the executor milestone an exact `unexpressible` list.
- M0144-0004/0005/0006 (instrumented PG) remain valid measurement support —
  the instrumented tree's OPTIMIZER_DEBUG output compares against the new
  pipeline's DPPATH trace.

## Acceptance

- TPC-H SF1 and TPC-DS SF0.25/SF1 corpora pass all value gates under the
  new pipeline (0008 gate).
- Plan-parity category deltas measured against the M0144-0002/0007 census
  baselines — specifically: `leaf-count`/`lateral` seam-decline classes
  approaching zero, `join-method` records on the five witnesses
  (Q10/Q16/Q35/Q69/Q94), Parallel Append sites reachable, ordered-step
  residual gates gone.
- The legacy pipeline is deleted, not merely defaulted-off.
