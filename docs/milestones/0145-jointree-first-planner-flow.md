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
| M0145-0010 | impl | Parameterized-path legality — rel-level `param_info`/`lateral_relids` + `reparameterize_path` analogue (`pathnode.c`), extending the existing `Path.RequiredOuter` machinery: the `lateral` decline family (8 corpus fires, Q30/Q68) and the partial-NLI whitelist's LEFT/ANTI entries re-evaluated on completion, not auto-admitted; every newly admitted shape passes an executor-capability check first |
| M0145-0011 | impl | Measured re-evaluation of the derived-input blockers (proposal `tmp/blocker-re-think-260912/01-proposal.md` 案A): diagnostic firewall-bypass flag (production change → `Kind: impl` per C1) + knob-arm E1 (Q78 + `outer-over-derived` fires) and E2 (`*CTEScan` leaf admission — downstream of E1 since pulled bodies become Semi/Anti SJIs). Produces evidence for an owner decision; lands no relaxation |
| M0145-0012 | impl | Retire the `rows<=1` CTE fallback (`initialRelRows`, `joinsearch.go:520-526`) — a goopg-only divergence (PG's `clamp_row_est` keeps the collapse): demonstrate `derived >= guard effect` on the corpus fires or wire a mechanism that makes it hold, then remove. Sequenced after 0011's evidence |
| M0145-0013 | impl | Admit pulled `*CTEScan` leaves at the seam (`pulled-leaf-not-scan`/`flat-leaf-not-scan`, 30 fires — Q14/Q23/Q95): Table-less `rangeBinding` + statistics-free `estimateBaseRelInfo` arm + third-site audit. E2's resume point; problems then wait on 0018 at the firewall |
| M0145-0014 | impl | Recurse the pull-up into pulled bodies' quals (`any-nested-sublink`, 6) — PG's `pull_up_sublinks_qual_recurse` re-runs on pulled-up quals after each splice |
| M0145-0015 | impl | Name the residual decline class + cover the PG-reachable subset under OR/NOT positions (PG recurses AND, converts `NOT EXISTS`, does NOT recurse OR — buried EXISTS is `convert_EXISTS_to_any`, a different path) |
| M0145-0016 | impl | `semianti-not-tail` (3): leaf reorder + relset remap for non-tail synthetic leaves — check 0005 slice (a) subsumption first |
| M0145-0017 | impl | Census the never-reached sublink population (~248 of 308 events: sublinks outside top-level WHERE conjuncts) by clause position; extend pull-up to ON-qual reach for the PG-pullable subset only |
| M0145-0018 | impl | Relax the `outer-over-derived` firewall — owner GO 2026-09-21; LAST: only after 0013 lands and a fresh E1 re-verification (SF0.25 + SF1) keeps relaxed plans clean; then remove `problemPairsOuterWithDerived` + the diagnostic flag, full default-arm gates |
| M0145-0019 | recon | Nested-loop costing for a derived inner — 0018's NO-GO resolution path (option (c), (b) as interim): cost-diff recon on the instrumented PG naming the divergence; the fix is the follow-on task it files, then 0018's E1 re-runs |
| M0145-0020 | impl | Port `examine_simple_variable`'s non-recursive CTE arm (`selfuncs.c:5737-5912`) — 0012's named prerequisite; unblocks the `rows<=1` fallback retirement |
| M0145-0021 | impl | Harness: SF1 fire-set gate template for firewall/estimation/cost-model tasks (0018 showed SF0.25-green can mask a 60x+ SF1 regression) |
| M0145-0022 | impl | Harness: plan-shape election + wall-clock regression channel on the SF0.25 sweep (values stay identical while shape/clock regress — the invisible class) |
| M0145-0023 | impl | Harness: flow-convergence instrument — route-ratio + decline-bucket trend log, observability only, not a movement instrument |

Dependencies: 0001 → {0003, 0004} → 0005 → {0006, 0007} → 0008.
0002 is independent and should land early so every later task is measurable.
0010 is sequenced after the chain: it is planner machinery (not executor
substrate, so it is inside this milestone where 0008's note puts executor
work outside), but doing it after 0005 avoids building required_outer
translation on the legacy arm's coordinate machinery that 0005 retires.

**Owner GO 2026-09-22 re-opened the 0001 lineage** (the loop-#60
escalation is answered CONTINUE): the measured downstream walls sequence
first — 0009, then 0019 (0018's unblock), then 0020 (0012's unblock) —
superseding the earlier note that sequenced 0009 after the flow chain.
When 0019 lands, 0018's fresh E1 re-verification re-runs; 0018 executes
the relaxation only if it passes at both scales. The flow-completion
chain 0004 → 0005 → 0007 → 0008 resumes after that, still under the
firewall constraint until 0018 executes it. 0021/0022/0023 are harness
work and may run any time.
0011/0012 are the blocker's re-definition path after 0009's census
exhausted the column channel — 0011 measures, 0012 retires the divergence
once the criterion holds. 0013–0017 are the decline-bucket filings from the
2026-09-21 census audit — each removes one named pull-up/seam decline class
so the pinned-spine ratio keeps shrinking toward the 0008 cutover; the
banner sequences them in the listed order, and the only hard dependency is
0013 → 0018. 0018 is the owner-approved `outer-over-derived` relaxation and
is deliberately LAST: it must land only after 0013 makes the
pulled-CTE-body shapes reachable and a fresh E1 re-verification shows the
relaxed plans stay clean.

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
