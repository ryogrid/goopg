# leftdeep-joins — Implementation TODO Ledger

| field | value |
| --- | --- |
| status | draft — no task started; this file is the decomposition source for fix_plan milestone filing |
| date | 2026-08-02 |
| convention | tasks are sized one-Ralph-loop each where possible; **[stage]** refers to [08-migration-and-removal.md](08-migration-and-removal.md) §2; every task lists its gate; deferral = ledger row + unchecked box, never a silent close |

## P0 — Executor pure wins [S0]

- [ ] **P0.1** Hoist `mergedKeySlot` construction to `Open` (shape-invariant
  per join); rebind `.row` per pull. Zero steady-state allocs in the seam
  microbench. Files: `internal/executor/operators_join_agg.go` (:986-1014,
  build-side call sites :590/:646/:702, probe sites :1266/:1269). Gate:
  units + spotcheck + bench.
- [ ] **P0.2** Single-pass build: fold `drainRowsBounded`'s budget into the
  build loop of `buildLazyHashTable`; delete the re-iteration
  (`rowsOp`-per-row `MaterializedSlot` allocs). Keep owned-copy discipline
  (M0097-0058). Gate: units + spotcheck + race-gate (shared-build interplay).
- [ ] **P0.3** Single-map build: planner threads key-type info on
  `planner.Join`; executor picks int64 vs string map before build; extend
  int64 path to Semi/Anti (CTID exception preserved). Delete
  `lazyHashFinalize`'s dual-map dance. Gate: units + SF0.5.

## P1 — The seam [S1]

- [ ] **P1.1** Legacy-path slot chaining per `0126-0004` (un-deferred):
  probe child slot as `lazyVirtualOut` source; rebind-on-pointer-change +
  copy-on-type-change fallback; delete `slotRow(probeSlot)` at
  `operators_join_agg.go:1254` and the vestigial `lazyKeyRow`. Env
  kill-switch `GOOPG_JOIN_SLOT_CHAIN=off`. Gate: full regress-port + SF0.5 +
  spotcheck; seam microbench 0 allocs.
- [ ] **P1.2** Worker-path exercise: the P1.1 seam under `BuildWorker`
  (`inWorker=true`) integration test — fusion's decline-in-worker precedent
  says this path diverges silently. Gate: race-gate.
- [ ] **P1.3** S1 A/B evidence run: Q3/Q10/Q18/Q7 ≤ 1.2× R0; file
  `analysis/leftdeep-joins/<date>-s1-ab.txt`. Gate: bar met or attributed
  ([09](09-verification-and-acceptance.md) §6) before P2 starts.

## P2 — Multi-column keys [S2] (planner+executor sibling pair, one commit)

- [ ] **P2.1** `planner.Join.HashKeys []JoinKeyPair`; search/pushdown fills
  all equality conjuncts; residual keeps non-equijoin only. EXPLAIN key list
  rendering. Plan-snapshot re-baseline same commit.
- [ ] **P2.2** Executor composite keys: all-int64 fixed-width pack; mixed →
  concatenated `datumKey`. Delete `reselectDegenerateHashKeys` +
  its planner pass (same commit); add a Q78-class degeneracy regression
  test (constant-pinned first key column must not degrade to one bucket).
- [ ] **P2.3** Merge-join multi-column keys from the same list
  (full-key comparator; residual only non-equijoin). Gate for P2.*: units +
  spotcheck + SF0.5 + snapshot diff reviewed.

## P3 — Hybrid hash spill [S3]

- [ ] **P3.1** `chooseHashTableSize` (shared pkg importable by planner and
  executor); goopg-width-aware (`48·c` + map overhead).
- [ ] **P3.2** Batch build/probe: hashvalue-prefixed `spillWriter` frames,
  per-batch inner/outer files, `HJ_NEED_NEW_BATCH` state in `nextLazy`,
  nbatch growth with capped give-up + WARNING.
- [ ] **P3.3** Per-query temp-file registry on `Context`; relocate to
  `<datadir>/base/pgsql_tmp/`; startup sweep; fix `spillOp.Close` unlink
  leak. Gate: injected-crash test leaves no strays.
- [x] **P3.4** Semi/Anti/LEFT per-batch semantics (batch-global
  `antiBuildHasNull`); shared-build declines when nbatch > 1.
  *(2026-08-03. `joinBatchEligible` admits Semi/Anti/probe-filling LEFT;
  `batchSkippable` carries PG's three skip rules with the fill arm;
  `prebuildSharedHashJoins` declines the SHARE rather than the SPILL.
  Build-side fill — LEFT-on-BuildLeft, RIGHT, FULL — stays with P4.2.)*
- [x] **P3.5** EXPLAIN `Batches:`/memory lines; forced-spill identity test
  (low `work_mem` Q3 byte-identical to default). Gate for P3.*: Q21 SF1
  completes capped; SF0.5 zero-delta; race-gate.
  *(2026-08-03. `HashJoinStats` per plan node on `Context`, published from
  `hashBatchState`; `formatHashJoinInfoLine` is `show_hash_info` verbatim,
  attached to the Hash Join because goopg has no `Hash` node. S3 exit met:
  `analysis/leftdeep-joins/2026-08-03-s3-spill.txt` — Q21 SF1 rc=0 in 132 s
  capped, Q3 at 512 kB (nbatch 512) byte-identical to Q3 at 6 GB (nbatch 1).
  **S3 CLOSED.**)*

## P4 — Other join operators [S4]

- [x] **P4.1** Streaming merge join (group buffering + overflow file);
  delete full-drain `runMergeJoin`/`buildMergeSide` accumulation.
  *(2026-08-04. `join_merge_stream.go`: each side is a `mergeSortedSource`
  — a `work_mem`-bounded external key-sort over the merged-key slot, N-way
  merged back — and `mergeJoinStream` is PG's state machine with the inner
  equal-key group as the only buffer, overflowing to a spill file past the
  budget. `runMergeJoin`/`buildMergeSide`/`mergeKeyedRow` deleted; `o.rows`
  is never touched. Emission order is byte-identical to the array
  implementation by construction, including NULL-keyed rows last, which is
  what the forced-spill identity test (work_mem=1 vs unbounded, 4 join
  types × 2 residual regimes) pins. Inputs are still sorted BY the operator
  — pathkey-fed inputs are P5 — and the emit is still `concatRows`, not the
  E1 slot seam: 4 ledger rows.)*
- [x] **P4.2** Hash outer-fill: matched bitmap per batch; RIGHT sweep;
  FULL = LEFT fill + sweep; planner legality matrix update (RIGHT/FULL
  hash paths). Regress-port outer-join files green.
  *(2026-08-04. `join_outer_fill.go`: `fillProbeSide`/`fillBuildSide` derived
  from Type + the EFFECTIVE build side, per-bucket `[]bool` bitmap written
  AFTER the residual, sweep per batch in `nextLazy`'s probe-EOF arm,
  NULL-keyed build rows retained and swept last. `batchSkippable` grows
  rule 1's INNER arm; every outer orientation now batches. The planner's
  merge PIN becomes a merge DEFAULT via `chooseOuterFillJoinAlgo`, gated
  behind `GOOPG_HASH_OUTER_JOIN=1` — an unconditional flip reorders
  unordered rows and costs regress `join` 210 diff lines, because
  `costInnerMerge` prices an 11-row sort like a real one while PG picks
  Merge Right/Full Join there. Default flip is P5's, with doc 04's cost
  currency: 2 ledger rows.)*
- [x] **P4.3** `Materialize` operator (plan node + path + rescan replay,
  memory→spill); NL join streams outer, inner under Materialize; delete
  drain-both `runNestedLoop` buffering and `concatRows`-per-pair.
  *(2026-08-04. `operators_material.go`: `materialBuffer` (work_mem-resident
  prefix + one sequentially-replayed overflow file) under `materializeOp`,
  which fills LAZILY and keeps PG's `eof_underlying` resume so a keyless
  Semi/Anti early-out cannot truncate the inner side. `join_nl_stream.go`
  replaces `runNestedLoop` with PG's `nodeNestloop.c` shape: outer streams,
  inner under the Materialize, RIGHT/FULL swept via an ORDINAL bitmap, and
  the predicate evaluated against one reusable merged buffer so allocation
  tracks output rather than N×M. The `planner.Materialize` plan node, its
  path and the EXPLAIN line are NOT here — PG places `Material` by
  `cost_rescan`, which needs doc 04's cost currency, so the node is P5.4's
  and the operator is executor-constructed meanwhile. The inner cache's
  work_mem bound is likewise implemented and tested but gated OFF
  (`GOOPG_NL_MATERIALIZE_WORK_MEM=1`) on measurement, not caution: DS05 Q54
  is a nested loop over a 1.44M-row `store_sales` seq scan, the bounded
  cache spills, and replaying it per outer tuple went 144 s → TIMEOUT;
  unbounded it runs in 95 s — faster than the drain-both path — because
  `costInnerNestLoop` has no `cost_rescan` term to steer away from the plan:
  3 ledger rows.)*
- [x] **P4.4** Lateral: outer streams (per-outer re-execution stays), output
  no longer accumulates into `o.rows`.
  *(2026-08-04. `join_lateral_stream.go`: `lateralJoinStream`, a two-phase
  machine (pull one outer tuple + re-open the right subtree under it → walk
  that subtree one tuple at a time) with `nlJoinStream`'s reusable pair
  buffer, so a rejected candidate allocates nothing and the emitted row is the
  only copy. The LEFT null-pad now keys on `matched` alone — the eager form's
  `len(rightRows) == 0 || !matched` was the same predicate spelled with a
  drained array. What streaming FORCED, and what the eager form got for free:
  correlation-context hygiene. The old loop could hold `ctx.OuterRows` pushed
  for a whole iteration because that iteration ran to completion inside
  `Open`; a streaming inner side yields to the PARENT between tuples, so the
  binding is installed and removed around each individual right-side call
  (`Open`, each `Next`, `Close`) — `bindOuter`/`unbindOuter`. The
  per-outer-tuple `CTERowCache` rides the same window, which additionally
  stops the lateral's CTE materialisations leaking into the enclosing scope
  (the eager loop left the last iteration's cache installed on return).
  With LATERAL — the last writer — converted, `joinOp.rows`/`idx` and the
  never-repopulated `leftCTIDs`/`rowSourceLeft` ctid side-channel are DELETED,
  and `Next`'s array tail with them: every arm now streams. Note this does
  NOT close S4: the stage exit also wants the regress-port outer-join files
  green, which stays gated on P4.2's `GOOPG_HASH_OUTER_JOIN` flip (P5).
  2 ledger rows.)*

## P5 — The DP [S5] (each task lands dark behind `GOOPG_PGSHAPED_DP`)

- [x] **P5.1** `joinrels` level lists + relset map over `RelOptInfo`;
  `buildInitialRels` incl. `PathPrebuilt` leaves for subquery/CTE/VALUES/
  pinned unnest rels (closes the leaf-whitelist gap).
  *(DONE 2026-08-04 — `internal/planner/joinsearch.go`. `searchCtx` holds
  `joinrels [][]*RelOptInfo` (PG's `join_rel_level`) and `relMap
  map[RelSet]*RelOptInfo` (PG's `join_rel_hash`) as TWO indexes over one set
  of rels: `addRel` derives the level from `bits.OnesCount16(relids)` rather
  than taking it as an argument, and rejects a duplicate relset, so the two
  cannot disagree about where a rel lives. `buildInitialRels` takes the same
  three per-FROM-item slices `tryBushyDP` already assembles and admits EVERY
  item — the whitelist gap closes here — with rows post-local-filter for a
  base table and `EstimateRows(leaf)` for every other leaf class, floored at
  1. Each rel gets one `PathPrebuilt` over the already-planned leaf, re-costed
  via `costSeqscan` over `estScanPages`: the node is carried whole so P5.5's
  createPlan can re-emit an index-scan leaf, but its cost comes from the
  search's own currency, never inherited. Per-index and parameterised paths
  are deferred to P5.4 — 1 ledger row. Nothing calls it: gated behind
  `GOOPG_PGSHAPED_DP` (default OFF, pinned by a test) and unreferenced from
  `planSelect`. UNITS PASS; PLAN 22/22 MATCH vs `m0127-p21-hashkeys`.)*
- [x] **P5.2** restrictInfo list + `hasRelevantJoinClause`; equivalence-class
  selectivity rule (inferred edges: admissible, no double-count).
  *(DONE 2026-08-04 — `internal/planner/joinrestrict.go`. `restrictInfo`
  generalises `joinEdge` (bushy.go:40) from a PAIR of FROM positions to a
  `relids RelSet`, so a three-rel qual is one clause with three bits instead of
  something the edge list cannot express; non-equality join quals are kept too,
  since P5.4's qual placement has to put them somewhere. The key split is
  stored as relsets (`leftRelids`/`rightRelids`), which is what lets
  `a.x = b.y + c.z` be a legal hash/merge clause keying {a} against {b,c}.
  **The task's real content is that §3's one-liner had fused two different PG
  rules**: `have_relevant_joinclause` (`joininfo.c:39`) is two `bms_overlap`
  tests with NO coverage requirement — the enumerator's "worth joining?"
  heuristic — while `build_joinrel_restrictlist` (`relnode.c`) IS a subset
  test, because a qual is applied at the lowest level that can evaluate it.
  They are now `hasRelevantJoinClause` and `clausesFor`, and 03 §3 is corrected
  to match the oracle. `selectivityClauses` is 04 §5: per
  `generate_join_implied_equalities_normal` an equivalence class emits exactly
  ONE clause per (outer, inner) split ("we can equate any one outer member to
  any one inner member"), so an EC of n members yields n−1 clauses over a whole
  tree, never C(n,2) — the double-count the ×2.0 `inferredEdgePenalty` was
  compensating for in the cost dimension instead of the cardinality one where
  the error lives. Class ids are dense and sorted by `compareColumnIdent`, not
  map order: the id decides which member carries the selectivity, so a
  randomised id would move plans between identical runs. 1 ledger row (goopg
  classifies the conjuncts it is handed; it never SYNTHESISES a clause from an
  EC the way `create_join_clause` does). Nothing calls it: `GOOPG_PGSHAPED_DP`
  is still OFF and P5.3's enumerator is the first consumer. UNITS PASS; PLAN
  22/22 MATCH vs `m0127-p21-hashkeys`.)*
- [x] **P5.3** `joinSearchOneLevel` phases 1+3 (clause joins against initial
  rels; disconnected cartesian; last-ditch); `makeJoinRel` with PG's
  outer/inner printing convention (03 §4.4).
  *(Landed 2026-08-04, `internal/planner/joinsearchlevel.go`. PG's branch is
  per OLD REL, not per pair — `old_rel->joininfo != NIL || has_eclass_joins ||
  has_join_restriction` at joinrels.c:96 — and that placement is what makes the
  level-2 `first_rel` offset (:112-116) apply to the clause branch ONLY; the
  clauseless branch deliberately re-pairs both directions (PG's own note,
  :127-136). 03 §4.1's pseudocode pushes the branch inside the inner loop,
  which is the same enumeration except for that offset, so the code follows the
  oracle's shape and the doc's equivalence is recorded inline. `makeJoinRel` is
  find-or-create over the P5.1 relset map plus PG's two-call
  `populate_joinrel_with_paths` tail (:809-816) — one RelOptInfo per relset,
  sized ONCE from the first pair that reaches it, with both outer/inner orders
  offered as paths: that is 03 §4.4's printing convention enforced
  structurally. Sizing (P5.6) and path generation (P5.4) stay behind a
  `joinRelBuilder` seam so this task is verified on the pair SEQUENCE alone.
  1 ledger row (no dummy-rel concept: PG's `is_dummy_rel` /
  `restriction_is_constant_false` short circuit is absent). Still nothing calls
  it from `planSelect`; `GOOPG_PGSHAPED_DP` OFF. UNITS PASS; PLAN 22/22 MATCH;
  SPOT PASS (Q12=2, Q13=35).)*
- [x] **P5.3a** Phase 2 — bushy joins, PG-verbatim (03 §4.3,
  `joinrels.c:141-198`): k-loop to the halfway point, clause-less rel skip
  (`:170-172`), mirror-half `first_rel` rule (`:174-177`),
  `have_relevant_joinclause` pair gate (`:190-191`). Pair-count
  verification against 03 §7's arithmetic (connectivity-filtered).
  *(Landed 2026-08-04, `internal/planner/joinsearchlevel.go`, ~40 lines
  between phases 1 and 3. PG's `:182-194` is `make_rels_by_clause_joins`
  verbatim — non-overlap, then `have_relevant_joinclause ||
  have_join_order_restriction` — so the phase reuses P5.3's helper and adds
  only the k-loop, the halfway break and the mirror-image offset. Neither
  iterated list can grow while phase 2 runs: `makeJoinRel` appends at `lev`
  and both k and lev−k are strictly below it. With phase 2 in place the
  enumeration is complete in PG's sense — every unordered split of a level's
  relset into two non-empty parts is reachable — and that is now checked
  arithmetically, not by example: on a complete clause graph the search must
  make exactly (3ⁿ − 2ⁿ⁺¹ + 1)/2 `makeJoinRel` calls, 03 §7's closed form,
  verified for n = 2..7. The clause-less rel skip (`:170-172`) is a pure
  short-circuit while `hasJoinRestriction` is constant false — a rel with no
  join clause cannot satisfy the clause-only pair gate for any partner — so
  no test can observe it; it is kept verbatim and the redundancy is recorded
  at the site, because the `has_join_restriction` disjunct makes it
  semantically live the moment restrictions enter. 1 ledger row: landing the
  full bushy space is what makes the absence of GEQO real (PG switches at 12
  rels, goopg searches to its 16-rel `RelSet` ceiling). Still nothing calls
  it from `planSelect`; `GOOPG_PGSHAPED_DP` OFF. UNITS PASS.)*
- [x] **P5.4a** `addPathsToJoinrel`, the unparameterised core (03 §5.1, §5.3,
  §5.4; `joinpath.c:124`): the per-pair key/residual split
  (`clause_sides_match_join`, `:2205`), hash paths with the FULL usable
  equality set as a multi-column key (05 §5), an unconditional plain nested
  loop, and qual placement carried ON the path.
  *(Landed 2026-08-04, `internal/planner/joinpaths.go` +
  `pathgen.go`/`cost_funcs.go`/`path.go`. The split is per PAIR, not per
  clause, and that is the whole content of the task: `a.x = b.y + c.z` keys
  {a} against {b,c}, so it is a hash key at the pair ({a},{b,c}) and an
  ordinary qual at ({a,b},{c}) — the same clause, both placements correct,
  both reachable in one search. Placement itself (WHICH join applies a
  clause) was already `clausesFor`'s coverage rule, and the "lowest covering
  level" property is now stated as an invariant rather than an example: over
  every spanning shape of a 3-relation triangle each clause is applied
  exactly once. `Path` gained `HashKeys`/`Residual` so the placement is
  COSTED — a nested loop pays its quals on the full cross product, a hash
  join only on the tuples that survived the key match, which is what stops a
  cartesian nested loop from looking free. The nested loop is generated
  unconditionally because phase 1's clauseless branch and phase 3's
  last-ditch pass both offer pairs with an EMPTY clause list, and
  `joinSearch` treats an empty pathlist as a hard failure. `addPath` is left
  to break the mirror-image tie: a self-join's two aliases are statistically
  identical by construction, both hash orientations cost the same, and
  keeping the incumbent resolves it to the first-offered order (M0125-0047's
  rule). `generateHashJoinPaths` was refactored to a single-orientation
  primitive so there is ONE hash-path generator — `makeJoinRel` already
  calls per direction. Still nothing calls it from `planSelect`
  (`sizeJoinRel`, the other half of the `joinRelBuilder` seam, is P5.6's and
  a stand-in sizer would be a second cost model); `GOOPG_PGSHAPED_DP` OFF.
  4 ledger rows — merge paths, parameterised paths (with the param-BLIND
  `setCheapest` they would corrupt), the jointype gauntlet, and
  `cost_qual_eval`'s expression walk. UNITS PASS.)*
- [x] **P5.4b-i** The parameterisation DISCIPLINE (03 §9), landed ahead of the
  paths it governs. *(DONE 2026-08-04 — `internal/planner/pathparam.go` +
  `path.go` / `pathgen.go` / `joinpaths.go`. Rule 1: `setCheapest` is
  `set_cheapest` (pathnode.c:272) in full — unparameterised-only cheapest
  slots, `CheapestParameterized` with the cheapest unparameterised path
  prepended, and the best-parameterised fallback filling the total slot but
  never the startup one. Its two non-obvious arms are reproduced: subset
  comparison runs BEFORE cost, and incomparable parameterisations keep the
  incumbent rather than picking the cheaper. Rule 2: `pathParamByRel` is
  PATH_PARAM_BY_REL (joinpath.c:46) and `addPathsToJoinrel` refuses both
  directions — an outer parameterised by the inner is impossible in any join
  order, an inner parameterised by the outer belongs to the NLI arm that is
  P5.4b-ii's. Rule 3: PG's `ppi_rows` needs no new field because PG carries it
  in `path->rows`, so the rule is a discipline on the COST primitives, which
  now read the child PATH's `Rows` and never `child.Rel.Rows`. And the fourth
  thing 03 §9 does not enumerate: a join path computes its own `RequiredOuter`
  from its children's, which for a nested loop is a SUBTRACTION
  (`calc_nestloop_required_outer`, pathnode.c:2592) — `generateNLIPath` had
  declared `RequiredOuter: inner.Relids`, reading the field as "what I depend
  on below" when it means "what I still need from above", and naming a
  relation the joinrel contains. Still inert — no `planSelect` call site,
  `GOOPG_PGSHAPED_DP` OFF. UNITS PASS.)*
- [x] **P5.4b-ii-a** Parameterised BASE INDEX paths — P5.1's deferred half, and
  this sub-item's own first step rather than a prerequisite someone else
  supplies.
  *(DONE 2026-08-04 — `internal/planner/pathparamindex.go` (+ `joinsearch.go`).
  `create_index_paths`' join arm (indxpath.c:446) per base rel: the equijoin
  clauses whose inner operand is a bare column of THIS rel, one candidate
  parameterisation per distinct outer relset, the longest B-tree index those
  clauses fully cover, and one `PathIndexScan` with a `RequiredOuter` — the
  first path in the search to have one, which is what makes P5.4b-i's
  discipline reachable instead of merely tested. `ppi_rows` needed no
  `eqjoinsel`: `get_parameterized_baserel_size` (costsize.c:5379) passes
  `varRelid = rel->relid`, which forces every clause to be estimated as a
  RESTRICTION on this rel, so the answer is `var_eq_non_const` — non-null
  fraction over the rel's own ndistinct, clamped to MCV[0] — and no
  both-sides join estimator is consulted. PG's `rel->tuples * sel(param ∪
  baserestrict)` equals goopg's `rel.Rows * sel(param)` because `rel.Rows`
  already carries the baserestrict selectivity. A fully-bound unique index
  short-circuits to one row (PG's `vardata->isunique`). Cost is built FROM
  `indexProbeCost` so the `indexProbeCostMultiplier` calibration is not
  duplicated. It is a separate step between `buildInitialRels` and
  `joinSearch` because it reads the clause list — PG's own ordering
  (`set_base_rel_pathlists` runs after `deconstruct_jointree`). Index
  eligibility calls `pickIndexCoveringAllLeadingColumns`, the NLI
  constructor's own function: the first half of §5.2's binding contract.
  Still inert — no `planSelect` call site, `GOOPG_PGSHAPED_DP` OFF. 2 ledger
  rows. UNITS PASS.)*
- [x] **P5.4b-ii-b-1** Parameterised JOIN paths: the NLI arm itself.
  *(DONE 2026-08-04 — `internal/planner/joinpathsnli.go`
  (+ `joinpaths.go`, `pathparamindex.go`, `pathgen.go`).
  `match_unsorted_outer`'s loop over `innerrel->cheapest_parameterized_paths`
  (joinpath.c:1949-1975), `try_nestloop_path`'s admission test (:882-889) with
  `allow_star_schema_join` (:363) and the constant-empty `param_source_rels`
  that 03 §4.4's INNER-only pin derives, and `create_nestloop_path`'s
  restrict-clause drop (pathnode.c:2478-2500) so a clause already enforced as
  an index qual is not charged again on the cross product. This CLOSES the
  hole P5.4b-i knowingly opened: a pair whose inner cheapest-total is
  parameterised by the outer had no path at all, and now has exactly the one
  PG recovers here. `addPathsToJoinrel`'s two PATH_PARAM_BY_REL refusals were
  split apart to match PG's control flow — an outer parameterised by the inner
  kills the direction outright, an inner parameterised by the outer kills only
  the hash and plain-NL arms. Also landed: PG's pairwise-UNION
  parameterisations (`consider_index_join_outer_rels`, indxpath.c:531-583,
  with the snapshot rule, the subset skip, the equivalence-class skip and the
  `10 * considered_clauses` valve), which is what finally binds a COMPOSITE
  index from two different outer rels — the ii-a ledger row's deferral, landed
  with its consumer as planned. The C1-era `generateNLIPath` is RETIRED: it
  charged a flat `indexProbeCost` per outer row regardless of the inner path,
  and one NLI constructor is the §5.2 rule. Still inert — no `planSelect` call
  site, `GOOPG_PGSHAPED_DP` OFF. 2 ledger rows. UNITS PASS.)*
- [ ] **P5.4b-ii-b-2** Memoize paths (`get_memoize_path`, joinpath.c:562) and
  the §5.2 constructor binding contract: shared eligibility fn with
  `tryBuildNLI`; constructor failure on a DP-chosen path = loud planner error.
  Both need a Node rather than a Path — `tryBuildNLI` analyses a built `*Join`
  — so they bind to P5.5's `createPlan` arms, not to path generation.
- [x] **P5.4c-i** `sort_inner_and_outer` (joinpath.c:1357) — the merge arm that
  sorts BOTH inputs, plus the pathkey machinery it needs.
  *(DONE 2026-08-04, `internal/planner/joinpathsmerge.go` + `joinpaths.go`,
  `path.go`. Per-equivalence-class sort-key reduction
  (`select_outer_pathkeys_for_merge`, pathkeys.c:1697-1704), pair-local key
  orientation, the one-path-per-ordering loop (:1447-1466),
  `make_inner_pathkeys_for_merge`'s direction copy (:1911-1915),
  `build_join_pathkeys` (pathkeys.c:1295) as the join's output ordering,
  `try_mergejoin_path`'s sort-skip (:1091-1097) and still-parameterised refusal
  (:1073-1081), and `create_sort_path`/`cost_sort` as an explicit child
  `PathSort` — the kind's first producer. Split from P5.4c-ii because this arm
  needs NOTHING from its inputs, while the other one is dead until some path
  carries pathkeys. Still inert — no `planSelect` call site,
  `GOOPG_PGSHAPED_DP` OFF. 3 ledger rows. UNITS PASS.)*
- [x] **P5.4c-ii-a** `build_index_pathkeys` (pathkeys.c:740) — the ordering a
  B-tree index path delivers, recorded on the path.
  *(DONE 2026-08-04, `internal/planner/pathkeysindex.go` +
  `pathparamindex.go`. PG's loop rules each pinned separately: INCLUDE columns
  excluded (:763-764), per-column `reverse_sort`/`nulls_first` (:775-776),
  backward inversion of BOTH (:770-774), STOP-not-skip on an unusable column
  (:815-822), `sortopfamily == NULL` for a non-orderable AM (:748, which for
  goopg also means `USING hash` — built on the B-tree substrate but not
  orderable in PG), and `pathkey_is_redundant`'s already-in-list half (:800).
  Wired into `addOneParameterizedIndexPath`, the only index-path constructor
  that exists, so `addPath`'s pathkey dimension stops being a constant
  `dimEqual` — PG passes the same `useful_pathkeys` to the parameterised path
  as to the plain one (indxpath.c:750-800). `paramIndexClause` now carries the
  inner `*ColumnRef` beside the column name, because goopg's syntactic pathkeys
  must BE the expression the clauses carry. Split from ii-b because the
  UNPARAMETERISED ordered index path the merge arm actually needs also needs
  `cost_index`, and 04 §1 forbids a second independently-calibrated index cost
  model being smuggled in beside a pathkey builder. Still inert — no
  `planSelect` call site, `GOOPG_PGSHAPED_DP` OFF. 4 ledger rows. UNITS PASS.)*
- [x] **P5.4c-ii-b** UNPARAMETERISED ordered index paths — `build_index_paths`'
  `useful_pathkeys != NIL` arm (indxpath.c:750-800) over `cost_index`
  (costsize.c:520) with the index-correlation model. This, not P5.4c-ii-a, is
  what makes an ordered MERGE OUTER reachable: `addMergeJoinPath` refuses a
  parameterised path outright (joinpath.c:1073-1081), so today's ordered index
  paths — all parameterised — can never satisfy its sort-skip branch.
  *(DONE 2026-08-04, `internal/planner/costindex.go` +
  `pathindexordered.go`, `cost_funcs.go`. `cost_index` transcribed for the
  `loop_count == 1` arm: Mackert-Lohman `index_pages_fetched`
  (costsize.c:906) in both regimes, `genericcostestimate`'s index-side charge
  reduced to the no-qual case, `btcostestimate`'s 50 × `cpu_operator_cost`
  descent (charged at startup, which is what lets an ordered scan beat a sort
  under LIMIT), and the csquared interpolation between the all-random and
  one-random-then-sequential I/O cases. Tied to the EXISTING
  `indexProbeCostMultiplier` on every random-page term rather than to a second
  calibration — 04 §1.1. `indexCorrelationFor` returns 0 because goopg has no
  `STATISTIC_KIND_CORRELATION` slot, which is what PG itself charges for a
  missing slot, so an ordered scan prices at `max_IO_cost` and survives only on
  its pathkeys. The generation gate is `has_useful_pathkeys`' join-clause arm
  alone (§10 has no query/group pathkeys), and building-plus-truncating the
  ordering is provably ONE left-to-right loop. `effective_cache_size` joins
  `costParams` (in PAGES, as PG's variable is) with its own drift guard.
  Combined entry point `addBaseRelIndexPaths` so a caller cannot wire up one
  half of `create_index_paths`. Still inert — no `planSelect` call site,
  `GOOPG_PGSHAPED_DP` OFF. 7 ledger rows, one of which (`Path` names no index)
  is a stated PREREQUISITE of P5.5. UNITS + SPOT PASS.)*
- [x] **P5.4c-ii-c** `generate_mergejoin_paths` (joinpath.c:1564) inside
  `match_unsorted_outer`: the merge arm that exploits an ALREADY-ordered outer
  instead of sorting, with mergeclause-list truncation and the materialize-inner
  decision. *(DONE 2026-08-04 — `internal/planner/joinpathsmergeouter.go` +
  `joinpathsmerge.go`, `joinpaths.go`. **P5.4c is CLOSED.** Wired at PG's arm-2
  position, between `sort_inner_and_outer` and the hash arm, so a merge over an
  ordered outer wins an exact tie against a hash path as it does in PG. It
  iterates `outer.Pathlist` and that is the slice: an ordered index path is never
  the cheapest total (P5.4c-ii-b — `indexCorrelationFor` is 0, so it prices at
  `max_IO_cost` and survives only on its pathkeys), so a `CheapestTotal`-keyed arm
  would find nothing. Three transcribed behaviours, each of which changes which
  plan wins: the mergeclause list is a PREFIX of the outer's ordering and STOPS at
  the first unserved position (`find_mergeclauses_for_outer_pathkeys` — an outer
  sorted `(x,y)` joined only on `y` is unusable, not usable on `y`); the clause
  list is TRUNCATED on both cost axes under PG's strictly-cheaper rule; and the
  result carries the outer's FULL ordering, not the merge keys, which is what lets
  a merge above it skip its own sort. Two findings that changed code: a truncated
  merge must **DEMOTE its dropped clauses to residual** — PG subtracts them at
  plan time, goopg splits key/residual during path generation (03 §5.4), so a
  dropped merge clause would have been evaluated by nothing, a wrong answer rather
  than a slow plan — and **one outer sort key can owe SEVERAL inner sort keys**
  (`a.x = c.x AND a.x = c.y`), which P5.4c-i's one-inner-key-per-group model could
  not express; `mergeInnerSortKeys` is now the single builder both arms use.
  **PG's materialize-inner decision has no goopg analogue at all**:
  `mergeJoinStream.bufferGroup` already buffers each inner equal-key group,
  spilling past `work_mem`, so the choice PG makes per PLAN goopg makes per GROUP
  in the executor — any presorted inner is consumable, no `PathMaterial` is wanted
  (it would double-buffer), and the cost of that buffering is ledgered rather than
  guessed. The jointype gauntlet and the FULL contract are ledgered, not written:
  `addPathsToJoinrel` carries no jointype to switch on. Still inert. 3 ledger rows.
  10 new tests. Bar met: UNITS + SPOT.)*
- [x] **P5.5-a** `IndexPath.indexinfo` + `indexscandir` (pathnodes.h:1845/1849)
  on goopg's `Path` — the stated PREREQUISITE of P5.5, ledgered at P5.4c-ii-b.
  *(DONE 2026-08-04, `internal/planner/pathindexcarrier.go` + `path.go`,
  `pathparamindex.go`, `pathindexordered.go`. goopg's `Path` is one flat struct
  with a `Kind` discriminator, and `PathIndexScan` recorded the ordering, cost
  and rows of a specific index scan without recording WHICH index produced
  them — so the DP could choose a path no `*IndexScan` node can be built from.
  `ScanDirection` reproduces PG's exact -1/0/+1 encoding (access/sdir.h:24),
  which buys the zero value as "not an index path" and needs no second
  discriminator on the flat struct. The direction is carried even though only
  `ForwardScanDirection` is ever produced, for the reason `DisabledNodes` is
  carried at a constant 0: a path that does not SAY which direction it means is
  one that silently means forward, and the backward arm would then be a change
  to every reader rather than to the producer. The one invariant with teeth —
  the recorded direction and the recorded pathkeys must describe the SAME scan,
  since `build_index_pathkeys` inverts direction AND null placement
  (pathkeys.c:770-774) — is held structurally: `indexPathOrdering` returns the
  pair and is the ONLY way either constructor obtains either half, so they
  cannot drift (rule #2). `IndexPath.indexclauses` is deliberately NOT carried
  and is ledgered with the finding that blocks a verbatim copy: PG's list is in
  index-column order while goopg's `bound` is in candidate order, and the
  executor's `IndexScan.Keys[i]` binds `Index.Columns[i]` positionally. Still
  inert. 2 ledger rows. 6 new tests. Bar met: UNITS + SPOT.)*
- [ ] **P5.5** `createPlan` arms for all live PathKinds → existing Nodes;
  **search-boundary coordinate map** (03 §10: relid-order canonical
  layout — one map composed from the final relset, or a relid-reordering
  root Project; ColumnRef-in-schema plan-time assertion); pinned-spine
  re-resolution consumes the map; searched-subtree tagging so legacy
  passes skip; `reconcileNLILayout` no-op assertion on searched trees.
- [ ] **P5.6** `calcJoinrelSize` + FK-superkey generalisation + eqjoinsel +
  FK clamp ([04](04-cost-and-cardinality.md) §3.1-3.3); delete quadratic
  build penalty; estimate audit tooling
  ([09](09-verification-and-acceptance.md) §5).
- [ ] **P5.7** nbatch-aware `hashJoinCost` (shared sizing fn); Startup/Total
  split for LIMIT-over-join.
- [ ] **P5.8** Collapse limits wired with PG's actual semantics (03 §6:
  flat comma lists are always ONE problem; limits govern sub-joinlists and
  explicit JOINs only; =1 pin semantics); explicit INNER JOIN flattening
  behind its own sub-flag `GOOPG_PGSHAPED_COLLAPSE` (soaked separately from
  the enumerator, 08 §2); outer joins stay pinned until `join_is_legal`
  constraint inference lands (03 §4.4). Delete the 12-table bail-out.
- [ ] **P5.9** S5 acceptance run per [09](09-verification-and-acceptance.md)
  §3 + plan-shape ratchet baseline (§4) + estimate audit (§5); flag flip or
  documented no-go.

## P-S6 — Compiled key/residual evaluation [S6]

- [ ] **PS6.1** Compile `HashKeys[i]` accessors and the residual conjunction
  to `ExprNode` at `Open` (`internal/executor/exprnode.go`); `ExprAdapter`
  fallback for unsupported kinds. Sibling-path audit compiled ↔ interpreted
  is the release gate ([09](09-verification-and-acceptance.md) §1): parity
  spot-diffs on expression corpora incl. the overflow corpus
  (0097-0037 precedent). Gate: units + parity corpus + seam microbench (no
  alloc regression).

## P6 — Deletion [S7, after S5-ON survives a clean nightly cycle]

- [ ] **P6.1** Delete fusion (`fused_hash_join.go`, hook, env vars, orphan
  exports check).
- [ ] **P6.2** Delete MultiHashJoin (fresh grep inventory; expect ~28 arms /
  15 files: node, packer, `mhj_input_rewrite.go`, posmaps, cost/cardinality
  arms, executor op, EXPLAIN arms, `generateMultiHashJoinPath`, flags).
- [ ] **P6.3** Delete the old subset-bitmask DP + the per-subset
  layout/remap family (`dpEntry.layout`, `remapKeyToLayout`,
  `mergeSubsetLayouts`) + integer cost + `IsSmallDimensionSide` pinning +
  `chooseInnerJoinAlgo` (searched); demote `joinorder.go` to over-limit
  sequencer. Held back until the 03 §10 relid-order boundary map is
  proven: `buildBindingsPosMap`/`applyJoinTreePosMap` (08 §4).
- [ ] **P6.4** Supersession stamps (0034-0001, 0038-0001, cost-model/09 §3
  allowance, 0125/0126 MHJ chapters); README index status flips; ledger
  rows for every deliberately-skipped PG behaviour (GEQO, skew buckets,
  SpecialJoinInfo in-DP — now unblocked by the bushy phase, still gated on
  `join_is_legal` inference —, shared spilling builds, full
  join_order_restriction inference).

## Standing deferral pointers (file rows when their stage lands)

GEQO (03 §7) · skew buckets (06 §6) · parallel/shared spilling builds (06 §6)
· hash-agg spill (06 §6) ·
semi/anti in-DP (07 §5 — **implementable now that the bushy phase exists
(03 §4.3); staged behind `join_is_legal` constraint inference**) ·
outer-join `join_order_restriction` inference (03 §6 — same standing: the
bushy phase is in place, so this awaits `join_is_legal` inference only) ·
tuplestore mark/restore merge join (07 §2) · Materialize under merge inner
(07 §4) · session cost-GUC threading (04 §1, pre-existing C3.2/C4 TODO) ·
slab `Aggregate` migration (05 §2, independent track) ·
`buildBindingsPosMap`/`applyJoinTreePosMap` deletion (08 §4 — held until
the 03 §10 boundary map is proven)
