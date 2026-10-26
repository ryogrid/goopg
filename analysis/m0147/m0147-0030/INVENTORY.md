# M0147-0030 — phase 2 recon: default-ON levers' OFF arms (2026-10-26)

Source: `scripts/lever-count.sh --list` at `4f7b216cd` (20 levers), minus `GOOPG_NL_BARE_REEXEC`, which is phase 0's
M0147-0023 and is held behind M0146-0010a. Two goopg-private passes with no env lever are added as recon children.
Read sites come from `git grep '"GOOPG_<L>"'`. Test-hook counts are call sites in `*_test.go` (setter function, or
direct variable references for levers with no setter). **No gate script, CI config, bench script or env file sets any
of these levers**, so every OFF arm is reachable only from tests and manual A/B runs.

Phase 2 deletes the OFF arm and keeps the shipped ON arm. Where an OFF arm is the only route a test uses to isolate a
path, the test is rebased first: onto PG's own switch where one exists (AGENT.md C4: `max_parallel_workers_per_gather
= 0` instead of `GATHER_PATHS=off/top`), onto a PG-oracle expectation, or onto a direct unit call of the remaining
arm.

| child | lever | layer | read site | OFF arm (what dies) | test hooks | rebase route | risk |
|---|---|---|---|---|---|---|---|
| 0031 | NARROW_LEG_HOOK | optimizer | joinleghook.go:49 | hook that only COUNTS legs (no node change) disabled | 8 direct refs, 1 file | none needed (OFF changes no plan) | lowest |
| 0032 | COOP_JOIN_BUILD | executor | parallel_hash_build.go:481 | producers rebuild nested shareable joins (pre-coop) | 4 direct refs, 1 file | unit tests call the coop path directly | low |
| 0033 | NL_MATERIALIZE_WORK_MEM | executor | join_nl_stream.go:52 | unbounded NL inner cache (no spill) | 3 direct refs, 1 file | ON = PG tuplestore spill past work_mem; tests pin ON | low |
| 0034 | SCAN_PREFILTER | executor | scan_prefilter.go:24 | scan deforms every column, filterOp only | SetScanPrefilterEnabled ×5 | values-identity tests become single-arm | low |
| 0035 | JOIN_SLOT_CHAIN | executor | operators_join_agg.go:1681 | pre-P1.1 probe slot flattening per pull | SetJoinSlotChainEnabled ×8 | same | low |
| 0036 | SUBPLAN_RESCAN | executor | subplan.go:36 | legacy Build/Open/Close-per-call SubPlan + CorrSubqOps registry | SetSubPlanRescanEnabled ×5 | same; check the registry dies with it | medium |
| 0037 | HASHED_SUBPLAN | executor | subplan_hash.go:48 | linear scan of cached IN slice (ON = PG hashed SubPlan) | SetHashedSubPlanEnabled ×10 | same | medium |
| 0038 | NARROW_UPPER_SORT | optimizer | upper_narrow_chain.go:124 | un-narrowed upper Sort input | 2 direct refs | single-arm | medium |
| 0039 | NARROW_UPPER | optimizer | upper_narrow_apply.go:90 | un-narrowed upper Aggregate input (also forces UPPER_SORT off) | 5 direct refs, 2 files | single-arm | medium |
| 0040 | NARROW_BUILD | optimizer | narrowoutput.go:11 | un-narrowed hash build sides | 34 direct refs, 2 files | single-arm | medium |
| 0041 | NARROW_COST_INPUTS | optimizer | narrowcostinputs.go:66 | planner costs at un-narrowed widths | setNarrowCostInputsForTest ×30 | single-arm; cost pins re-derived | medium |
| 0042 | PG_SORT_RELATION_BYTES_COST | optimizer | sort_pgrelationbytes.go:17 | goopg Datum currency for Sort (ON = PG cost_sort bytes) | setPGSortRelationBytesCostForTest ×8 | map-currency fallback only for invalid width (M0147-0021 pattern) | medium |
| 0043 | INCREMENTAL_SORT | optimizer | incrementalsortpaths.go:86 | no Incremental Sort arm in the ORDERED tournament | 5 direct refs, 1 file | PG `enable_incremental_sort` (PlannerSettings) | medium |
| 0044 | MEMOIZE | optimizer | memoize.go:34 | no Memoize insertion | SetMemoizeEnabled ×13 | PG `enable_memoize` (PlannerSettings.EnableMemoize) | medium |
| 0045 | PARALLEL | optimizer | parallel.go:45 | no Gather post-pass | SetParallelEnabled ×8 | PG `max_parallel_workers_per_gather = 0` | medium |
| 0046 | GATHER_PATHS `off`/`top` | optimizer | gatherpaths.go:80 | search may not / only at top choose a Gather | setGatherPathsModeForTest ×18, SetGatherPathsMode ×5 | PG `max_parallel_workers_per_gather = 0` (C4) | high |
| 0047 | PARTIAL_AGG_PATHS | optimizer | partialaggpaths.go:62 | retired size rule decides the Finalize/Partial split | setPartialAggPathsModeForTest ×35, SetPartialAggPathsMode ×1 | tournament-only expectations; measure reachability first (M0147-0020 recipe) | high |
| 0048 | EXISTS_TO_ANY | optimizer | exists_to_any.go:79 | correlated EXISTS stays a per-row SubPlan | SetExistsToAnyEnabled ×6 | ON is goopg's analogue of convert_EXISTS_to_ANY but unconditional (PG keeps an AlternativeSubPlan, ledger M0146-0012b): decide with the owner whether ON is "PG logic" before deleting OFF | high |
| 0049 | INDEXKEY_HARVEST | optimizer | unnest.go:38 | index-key correlations not harvested as unnest params | SetIndexKeyHarvestEnabled ×22 | measure reachability after M0147-0013 (scalar loop gone) — IN/EXISTS only now; the 2026-09-05 note says the flip is not plan-neutral | high |
| 0050 | (no env) `goopg.enable_nestloop_index` + post-search `rewriteJoinsToNLI`; post-search `jn.Algo` assignment | optimizer | nl_index_join.go, planner.go | — | — | RECON: census with `PGOPTIONS='-c goopg.enable_nestloop_index=off'`; count trees the PG-shaped search did not build | recon |

Ordering: executor kill-switches with few hooks first (their ON arm is the only production path, and they cannot move a
plan). Then the narrowing family, whose OFF arms are goopg-private. Then the levers that have a PG switch to rebase
onto. Then the two whose ON arm's PG faithfulness is itself in question (0048, 0049). The recon goes last.

Every child follows the M0147 slice protocol (fix_plan `## M0147` header):
- C6: a full-corpus trace or capture for the OFF arm's reachability before deleting it, or an inert proof;
- a flaglabels row → `retired(<id>)` where one exists;
- the full gate set.
