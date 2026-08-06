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
- [x] **P5.4b-ii-b-2** Memoize paths (`get_memoize_path`, joinpath.c:674) and
  the §5.2 constructor binding contract.
  *(DONE 2026-08-06 — `internal/planner/joinpathsmemoize.go` (new)
  + `joinpaths.go`, `joinpathsnli.go`, `joinrelsize.go`, `path.go`,
  `createplan.go`, `createplannl.go`. Memoize is a PATH, not an attachment:
  `getMemoizePath` wraps a parameterised inner, `cost_memoize_rescan`
  (costsize.c:2541) prices the rescan, and `addNLIPaths` offers the bare and the
  wrapped candidate to `addPath` exactly as `match_unsorted_outer` does
  (:1965-1986). The new `PathMemoize` kind has no `createPlan` arm — goopg's
  cache is `NestedLoopIndexJoin.InnerMemo`, a field on the join — so the NLI arm
  unwraps it and keys the cache on the ALREADY-TRANSLATED probe keys. The
  binding contract turned out to be discharged in a different form than filed:
  `walkRewriteNLI` skips a searched subtree, so `tryBuildNLI` is not the
  searched constructor at all; `createNestLoopIndexJoinPlan` is, its declines
  are panics, and eligibility is shared at the PRODUCER
  (`pickIndexCoveringAllLeadingColumns`, `addParameterizedIndexPaths`). Safety
  property: a defaulted ndistinct becomes `calls` (:2592), so on a stats-free
  server the wrapper is strictly more expensive and can never win `addPath`.
  4 ledger rows. UNITS + SPOT PASS.)*
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
- [x] **P5.5-b** `IndexPath.indexclauses` (pathnodes.h:1846) on goopg's `Path`,
  in INDEX-COLUMN order — the second half of the index carrier, ledgered at
  P5.5-a. *(DONE 2026-08-04, `internal/planner/pathindexclauses.go` + `path.go`,
  `pathparamindex.go`, `pathindexordered.go`. A parameterised index path was
  built from `bound []paramIndexClause` and then DISCARDED them once cost and
  rows were computed, but `createPlan` needs them twice over:
  `fix_indexqual_references` (createplan.c:5121) builds the scan's keys from the
  list, and `is_redundant_with_indexclauses` (createplan.c:3075) uses it to DROP
  those clauses from the node's filter quals — which is why the carrier holds the
  `*restrictInfo` by identity and not just the probe value. **The order is the
  whole difficulty**: PG's list is ordered by index column because
  `build_index_paths`' outer loop runs over `indexcol` ("this order is depended
  on by btree", indxpath.c:1042), goopg's `bound` is in the search's CANDIDATE
  order, and `IndexScan.Keys[i]` binds `Index.Columns[i]` positionally — so a
  verbatim copy would compare the wrong pair of columns on any composite index
  whose clauses arrived out of order. `indexPathClauses` therefore loops over
  `idx.Columns` and looks the clause up per column, PG's own loop shape; a sort
  would be a second statement of the same fact that a later edit could
  contradict (rule #2). Its keys agree with
  `pickIndexCoveringAllLeadingColumns`' ordered list by construction, since both
  read the same first-wins clause set in the same column order — the path cannot
  be costed for one probe and built as another. Two narrowings are ledgered
  rather than written, each a shape goopg's executor cannot represent: PG's list
  is NONdecreasing in `indexcol` (`x > 1 AND x < 5` gives two clauses at one
  column) while goopg carries one equality per column, and PG's gapped/prefix
  probe (`amoptionalkey`) is DECLINED outright — nil, not a shortened list, since
  a shortened list silently re-indexes every position after the gap. The ordered
  unparameterised path carries an EMPTY list, which is pathnodes.h:1817's "an
  empty indexclauses list implies a full index scan" and not an omission. Still
  inert. 3 ledger rows. 7 new tests. Bar met: UNITS + SPOT.)*
- [x] **P5.5-c** `create_indexscan_plan` (createplan.c:3006) — the FIRST real
  `createPlan` arm, consuming the carrier P5.5-a/-b landed. *(DONE 2026-08-04,
  `internal/planner/createplanindex.go` + `createplan.go`, `path.go`,
  `joinsearch.go`, `pathparamindex.go`, `pathindexordered.go`. The seam problem
  is not the index but everything else the leaf carries: PG's arm reaches the
  relation, alias, target list and `baserestrictinfo` through `RelOptInfo`'s
  range-table entry, and goopg's search-only rel has none of that. The answer is
  to record WHAT THE SEARCH WAS HANDED — `RelOptInfo.baseLeaf` carries the leaf
  `Node` `buildInitialRels` received (the search boundary's half of 03 §10's
  coordinate map: what a base relid MEANS), and the arm re-emits from it:
  the SCHEMA is the leaf's (a fresh target list would renumber the columns under
  every clause), the ALIAS survives (self-join disambiguation, the M0062-0002
  finding), and the local quals survive because goopg's `*IndexScan` has no
  `qpqual` — they live in `*Filter` wrappers above the scan, which
  `indexScanLeafFor` peels, remembers, and reproduces as NEW nodes over the
  replacement scan (the originals are matched by pointer identity elsewhere;
  `LeafLocal` rides along or a posMap pass renumbers leaf-local ColumnRefs).
  The same `indexScanLeafFor` is the eligibility gate at BOTH index-path
  producers: a leaf that cannot be rebuilt must not have an index path COSTED
  over it, or the DP prices a plan the builder then refuses — one predicate,
  two callers, no drift (rule #2). Every arm precondition panics with the
  specific wrong answer behind it; the dangerous one is a parameterised path
  with an EMPTY clause list, since empty means FULL INDEX SCAN
  (pathnodes.h:1817) and building it would silently turn a costed point probe
  into a whole-relation scan. `Key` vs `Keys` follows the executor's own
  convention (single-column probes use `Key`, nl_index_join.go:656). Still
  inert. 2 ledger rows. 9 new tests. Bar met: UNITS + SPOT.)*
- [x] **P5.5-d** `create_seqscan_plan` (createplan.c:2910) + `create_sort_plan`
  (createplan.c:2177) — the two structurally simple arms. *(DONE 2026-08-04,
  `internal/planner/createplansimple.go` + `createplan.go`,
  `createplanindex.go`. The seq-scan arm is the index arm's mirror over the
  SAME leaf resolver — `indexScanLeafFor` is renamed `scanLeafFor` and its
  rewrapper generalised from `*IndexScan` to `Node`, since the predicate now
  serves two arms; `scanIdentity` gains the four `*SeqScan`-only fields
  (EstRelRows, LockParentOID, SkipIfVanished, InheritParentOID) so the rebuild
  is LOSSLESS, honouring the struct's stated purpose that a field added to the
  node is a compile-visible edit there. The arm rebuilds a FRESH node even when
  the leaf's base scan already is a `*SeqScan` (the emitted tree must never
  alias nodes the pipeline still owns — `attachRelationLocalFilters` matches by
  pointer), and DEMOTES an `*IndexScan` leaf when the search costed the
  sequential scan cheaper. Its panics: a parameterised seq scan is
  undischargeable, claimed pathkeys are an ordering a heap scan does not
  deliver, index detail means a costed probe was mislabelled. The sort arm is
  the first arm with a CHILD path — it recurses via `createPlan`, so it must
  exist before P5.5-e's join arms (P5.4c's merge paths carry `PathSort`
  children) — translating `PathKey.SortAsc` to the executor's `SortKey.Desc`
  by negation, NullsFirst unchanged. Deliberately NOT reproduced, each
  ledgered or filed: CP_SMALL_TLIST width trim before the sort
  (createplan.c:2188 — goopg nodes have no tlist negotiation); the
  `prepare_sort_from_pathkeys` EC/resjunk column resolution (goopg's Sort
  evaluates the key EXPRESSION against child rows; coordinate correctness is
  P5.5-f's map assertion); `generateScanPaths` does not yet gate on
  `scanLeafFor` (it is C3.2 test-only — the gate must be applied when C4
  wires it into the live search, or the DP prices a seq path over a subquery
  leaf and the builder panics). Still inert. 2 ledger rows. 7 new tests
  (`createplansimple_test.go`). Bar met: UNITS + SPOT.)*
- [x] **P5.5-e-i** the coordinate carrier + `create_hashjoin_plan`
  (createplan.c:4633) — the first join arm. *(DONE 2026-08-04,
  `internal/planner/createplanjoin.go` + `createplan.go`, `createplansimple.go`,
  `path.go`, `joinsearch.go`. A join is the first arm that MERGES two schemas,
  which makes the coordinate question unavoidable: every `restrictInfo.clause`
  is written in pre-search BINDING coordinates — not incidentally, but because
  `relidsOfExpr` DECIDES a clause's relset by bucketing its `ColumnRef.Index`
  against exactly those offsets — while the emitted tree is a cost-chosen
  reordering, so a clause copied across unchanged keys on whichever column
  happened to land at that index. Resolved by carrying the map through the
  recursion: `createPlanNode(p) (Node, outputLayout)` where `outputLayout[i]` is
  output column i's binding coordinate, built from `RelOptInfo.baseOffset`
  (recorded beside `baseLeaf` at `buildInitialRels` — `baseLeaf` says what a
  relid MEANS, `baseOffset` where it USED TO BE), passed through unchanged by a
  sort (which moves rows, not columns) and concatenated by a join in the same
  statement that concatenates the schema, so it cannot drift from the tree.
  `translateToLayout` is `set_join_references` (setrefs.c:2557) at goopg's
  fidelity — one renumbering onto the merged row rather than PG's
  OUTER_VAR/INNER_VAR split, since goopg's executor evaluates one merged row —
  built on `cloneExprRefs` so the search's own clause nodes are never mutated,
  stepping over inner plans (`scopeIgnore`, agreeing with `relidsOfExpr`) and
  refusing `*OuterColumnRef`/`*CTIDExpr`, neither of which is positional.
  `BuildLeft` is deliberately never set: `generateHashJoinPaths` adds both
  orientations as separate paths and `add_path` keeps the cheaper, so the build
  side was already decided BY COST in the child order the arm receives — setting
  it here would be the uncosted name-tag rule 06 §2.1 retires. The predicate
  carries every key equality as well as the residual, matching `buildJoinFromDP`
  + `attachExtraEdgesLocal`: goopg hashes on one pair and evaluates `Predicate`
  per matched pair, so a key omitted from it is enforced by nothing (the Q9
  multi-equality case). Panics: <2 children; no hash key; a parameterised hash
  path (undischargeable — a hash join propagates rather than binds, and no
  producer builds one today); a non-equijoin key; a key whose sides do not match
  the join (`clause_sides_match_join`, joinpath.c:2205); a child whose
  coordinates are unknown; a clause naming a column outside the join. 2 ledger
  rows (PG's `joinqual`/`qpqual` split folded into one `Predicate` — equivalent
  for inner joins, a wrong answer the moment an outer join enters the search;
  SubPlan arguments not re-based). 1 inventory pin
  (`createplanjoin.go:translateToLayout`). Still inert. 8 new tests
  (`createplanjoin_test.go`). Bar met: UNITS + SPOT.)*
- [x] **P5.5-e-ii-a** `create_mergejoin_plan` (createplan.c:4444) — the second
  join arm. *(DONE 2026-08-04, `internal/planner/createplanjoin.go` +
  `createplan.go`, `createplansimple.go`. The prologue P5.5-e-i wrote inline was
  lifted into `joinInputsFor` / `joinInputs.keyPairs` / `joinInputs.joinPredicate`
  in the same commit, so both arms build the merged row from ONE piece of code —
  two arms concatenating schema and layout separately would drift, and the drift
  is a wrong-column join that still runs. What is merge-only is two things.
  (1) **The key list IS the sort order.** `sortInnerAndOuter` concatenates the
  merge key GROUPS in the pathkey order it chose, and goopg's merge executor
  sorts each side by the key TUPLE in `Join.HashKeys` order
  (`mergeSideKeyExprs` → `mergeSortedSource.less`), so that list is
  `outersortkeys`/`innersortkeys` rather than a set of clauses — `keyPairs`
  preserves the order it is given, and the keys must become `Predicate`
  conjuncts in that same order because `fillJoinHashKeys` REBUILDS the published
  list from `Predicate` at the tail of `Plan()`. A group serving several clauses
  contributes several pairs with EC-equivalent outer operands, so the tuple can
  be longer than `outersortkeys` — PG's shape too
  (`find_mergeclauses_for_outer_pathkeys`, pathkeys.c:1670). (2) **The explicit
  `PathSort` children are ABSORBED, not emitted.** PG materialises a Sort here
  because `nodeMergejoin` requires sorted input; goopg's `JoinAlgoMerge`
  operator sorts BOTH inputs itself, unconditionally (`openMergeJoin`), so
  emitting the child Sort would sort each side twice — a cost
  `tryMergeJoinPath` never charged. `absorbMergeSort` steps over the `PathSort`
  and emits its child, which is coordinate-neutral (a sort passes its layout
  through) and reproduces the costed plan exactly. Its guard, and the arm's
  result-ordering guard, are for P5.4c-ii's ordered inputs: goopg's merge
  comparator is fixed ascending / NULL-keyed-rows-last, so any other claimed
  ordering is a promise the emitted node will not keep. Also fixed here, a
  latent P5.5-d defect the merge arm made reachable: `createSortPlan` emitted
  its `PathKey.Expr`s UNTRANSLATED, so a sort over a rel that was not first in
  binding order ordered by whichever column sat at that index — now re-based
  onto the child's layout through the same `translateToLayout`, rule #2. Panics:
  <2 children; no merge clause; a parameterised merge path; a non-ascending or
  nulls-first result pathkey; a non-ascending or nulls-first absorbed sort key.
  Still inert. 4 new tests (`createplanjoin_test.go`) + 1 strengthened
  (`createplansimple_test.go`). 2 ledger rows. Bar met: UNITS + SPOT.)*
- [x] **P5.5-e-ii-b** `create_nestloop_plan` (createplan.c:4322) — the
  nested-loop arms (`PathNestLoop` plain and NLI). *(DONE 2026-08-04,
  `internal/planner/createplannl.go` (new) + `createplan.go`,
  `createplanindex.go`, `pathparamindex.go`, `joinpathsnli.go`. The prologue is
  P5.5-e-ii-a's, reused verbatim; three things are this arm's own.*
  *(1) **One path kind, two executor nodes.** `PathNestLoop` is produced by
  `addNestLoopPath` (plain — keys on nothing, every clause residual) and by
  `addNLIPaths` (the inner is a parameterised index path). goopg's nodes for
  those are `*Join{JoinAlgoNestedLoop}` and `*NestedLoopIndexJoin`, which is a
  different TYPE, not a flag — its `Inner` is a `*IndexScan` because the driver
  calls `Rescan` on it. The arm dispatches on the INNER CHILD's
  `RequiredOuter`, the same fact PG dispatches on when it emits `NestLoopParam`,
  and it is read off the child rather than carried as a second field that could
  disagree with it.*
  *(2) **The parameter-binding contract is TWO coordinate spaces, not one** —
  the finding that makes this arm different from hash and merge, where every
  expression lives on the merged row. `indexScanOp.Rescan` (operators_index.go:345)
  evaluates `IndexScan.Key`/`Keys` against the slot the parent bound
  (`nestedLoopIndexJoinOp.outerMS`), which holds the OUTER ROW ALONE — the inner
  row does not exist yet, producing it is what the probe is for. So the probe
  keys are re-based onto the outer layout (the prefix of the merged one, taken
  rather than re-derived so the two maps cannot disagree) while the residual
  `Predicate` is re-based onto the merged layout, which is what the operator's
  `virtualOut` spans. On a two-relation query with the outer first in binding
  order the two spaces COINCIDE, so a single-space arm passes every small test
  and reads the wrong column the moment the search reorders the join;
  `createplannl_test.go` puts the outer second for exactly that reason. No key
  conjuncts are folded into the NLI predicate — unlike a hash bucket, an index
  probe enforces its keys exactly.*
  *(3) **goopg's residual DROP must be NARROWER than PG's, and the old one was
  a wrong answer.** `create_nestloop_path` (pathnode.c:2478-2500) drops every
  clause movable into the parameterised inner, because a PG parameterised path
  really does apply them all: movability builds `ppi_clauses`, the index
  consumes what it can, and `create_indexscan_plan` puts the remainder in the
  scan's `qpqual`. goopg's parameterised index path carries only the equalities
  in `Path.IndexClauses`, and goopg's `*IndexScan` has NO qual field — so
  `b.y > a.x` at inner `{b}` under parameterisation `{a}` was movable, dropped
  from the join residual, and enforced by nothing. `nestloopResidualClauses` now
  drops only clauses the probe demonstrably enforces (`probeEnforcedClauses`,
  by `restrictInfo` identity — the same list `createPlan` turns into
  `IndexScan.Keys`, so one definition serves costing and building). The EC half
  of `is_redundant_with_indexclauses` is deliberately not reproduced:
  `selectivityClauses` already reduced each class to one member, and the
  asymmetry decides the residue (keeping a redundant qual costs an evaluation,
  dropping a live one deletes a restriction).*
  *Also: `addParameterizedIndexPaths` now declines a leaf with `*Filter`
  wrappers (`scanLeafIsBare`), because `NestedLoopIndexJoin.Inner` cannot carry
  them and hoisting them onto the residual is the D6.3b Q9 blowup — the same
  producer/consumer agreement P5.5-c established for non-scan leaves. Memoize
  stays nil: there is no `PathMemoize`, so inserting one here would be an
  uncosted opinion. Panics: ≠2 children; hash keys on a nested loop; a
  parameterised result; a parameterised non-index inner; an outer that does not
  supply the inner's parameterisation; lost index-column order; a probe with no
  key; a wrapped inner leaf. Still inert. 6 new tests
  (`createplannl_test.go`) + 1 rewritten (`joinpathsnli_test.go`). 3 ledger
  rows. Bar met: UNITS + SPOT.)*
- [x] **P5.5-f-i** the search boundary: 03 §10's coordinate map and the one
  node that makes it invisible above the search root
  (`internal/planner/createplanroot.go`).
  *(DONE 2026-08-04. `createPlanAtSearchRoot(p, bindingWidth)` is now the only
  `createPlan` entry point a search caller may use — `createPlanNode` returns
  the search's own cost-chosen column order, which is right for a child of
  another join arm and wrong for anything else. **The finding that decided
  §10's open variant:** at the search root, §10's canonical RELID order and the
  pre-search BINDING order are the same sequence — `buildInitialRels` assigns
  relid `1<<i` to FROM item `i` with an ascending `baseOffset`, and the root's
  relset is the FULL set. So the reordering `Project` is not a way around the
  canonical layout, it IS that layout materialised at the one place §10
  requires it observable, and it collapses the boundary map to the identity for
  every consumer above: the enclosing tree needs no rewrite and the map
  survives only as that node's target list. Elided when the search left the
  columns where the bindings put them (the leading left-deep case).
  **Second finding:** `bindingWidth` must be a PARAMETER, not `len(layout)` — a
  FROM item that never entered the search yields a root that is
  self-consistent and permutation-clean judged against its OWN width, and
  missing columns the enclosing tree still references; that is the M0097-0058
  shape, and it is detectable only from outside. §10's tripwire is real code
  now (`assertColumnRefsWithinSchema`) but applied to the boundary node alone.
  2 ledger rows — PG adds NO node here (`set_upper_references`, setrefs.c:2214,
  renumbers upper Vars in place), and the tripwire's one-node scope. Still
  inert. 8 new tests (`createplanroot_test.go`). Bar met: UNITS + SPOT.)*
- [x] **P5.5-f-ii-a** searched-subtree tagging so the `buildBindingsPosMap` /
  `applyJoinTreePosMap` family skips, plus the `reconcileNLILayout` no-op
  assertion. *(DONE 2026-08-04, `internal/planner/searchedtree.go` new.
  `searchedTree` is an embedded one-bit tag on the seven node kinds
  `createPlanAtSearchRoot` can return as a root; `markSearchedTree` PANICS on
  any other kind, so a future arm that returns a new root kind cannot leave a
  silently untagged subtree. **The measured finding that reshaped the task:**
  HALF of this was already true and nobody had written it down. M0125-0012
  (TPC-DS Q8) made every `*Project` in a join tree an opaque scope boundary on
  BOTH sides of the map — `collect` advances past one, `applyJoinTreePosMap`
  returns at one — so the boundary `Project` inherited its protection for free;
  a probe confirms `buildBindingsPosMap` returns nil over it and no target
  moves. The hole is the **ELIDED** root: when the search's order already is
  binding order there is no Project to stop at and both passes walk into a bare
  `*Join`. The numeric half of that is provably harmless (identity layout ⇒
  identity map, since `collect`'s DFS order over a join IS its output order) —
  but `applyJoinTreePosMap`'s `*Join` arm calls `reresolveJoinByName`, and
  `reconcileNLILayout` is name resolution end to end, and those rebind the
  searched joins' keys by NAME over a layout derived by COORDINATE one node
  earlier. **Second finding:** that name-resolution check is not free evidence.
  It abstains on an unnamed operand (`reresolveJoinByName` returns immediately),
  on an ambiguous name (-1, left alone), and on everything it does not rebind —
  and the P5.5-e unit fixtures build clause operands with `col(i)`, i.e.
  UNNAMED, so reusing them would have made every assertion pass vacuously.
  `searchedtree_test.go` supplies its own named-clause helper. 2 ledger rows.
  10 new tests, two of which pin PRE-task behaviour so a later simplification
  can see which half was already covered. Still inert.)*
- [x] **P5.5-f-ii-b** pinned-spine re-resolution consumes the boundary map;
  `assertColumnRefsWithinSchema` widened from the boundary node to the whole
  enclosing tree. *(DONE 2026-08-04, `internal/planner/enclosingtree.go` new +
  `predp.go` call site. When the subtree spliced under the pinned spine carries
  the searched-subtree tag, `assertSpineConsumesIdentityBoundaryMap` checks
  column-by-column that the boundary republished the concatenation the spine was
  resolved against, and the re-resolution returns without rebinding.
  **The finding that decided how it is written:** reading `layoutPosMap == nil`
  as "the map is the identity" would have been WRONG — that helper returns nil
  for two different reasons, "identical" and "widths differ, refuse to remap",
  so a boundary that lost a column takes the second door and is
  indistinguishable from success while the enclosing tree references columns
  that moved. The assertion compares the schemas itself and never consults `pm`.
  The tripwire is `assertEnclosingTreeColumnRefs` over one switch
  (`enclosingNodeScopeOf`) answering which expressions / against what width /
  which children — a `*Join`'s predicate and both keys index the MERGED row even
  for Semi/Anti (whose `Output()` is Left only), and a `*NestedLoopIndexJoin` is
  descended on the outer side only. **Second finding:** with 53 node kinds, a
  partial walk that stops at unenumerated kinds checks NOTHING and returns
  normally whenever the kind it stops at sits on the path — P5.5-f-ii-a's
  vacuity finding one level up, and harder to see because a tree walk looks
  exhaustive. The guard is therefore on the partiality: a stop is not a panic,
  but the walk must REACH a searched subtree or the assertion fails naming every
  kind it stopped at. 2 ledger rows (`walkPlanExprs` misses
  `Aggregate.Passthrough` / `AggregateCall.Filter` / `WindowFunc.Args|Filter` /
  frame offsets; `pushOneConjunct` is the fourth legacy family member and is not
  taught about the tag). 12 new tests (`enclosingtree_test.go`). Still inert.)*
- [x] **P5.6** `calcJoinrelSize` + FK-superkey generalisation + eqjoinsel +
  FK clamp ([04](04-cost-and-cardinality.md) §3.1-3.3); delete quadratic
  build penalty; estimate audit tooling
  ([09](09-verification-and-acceptance.md) §5). Decomposed, because 04 §3's
  remedy set is four mechanisms and a measurement, not one edit.
  **CLOSED 2026-08-06.** Every sub-item below is done, the stated acceptance is
  MEASURED, and the one residue the roll-up carried in its own body — "re-evaluate
  M0125-0003 stage 3 (rows-once per RelOptInfo, [04](04-cost-and-cardinality.md)
  §2)" — is discharged in [04](04-cost-and-cardinality.md) §2.1: stage 3 is not
  a fourth staged flag consumer, because rows-once removes the second consumer it
  would have shadowed, and its placement is now `applyRelSizeFallback` at the
  search seam, in `estimate_rel_size` → `set_baserel_size_estimates` order. That
  is a no-op S-cold (the reliability gate) and load-bearing post-restart (restored
  column statistics with no `RowCount`). Acceptance, from the default-arm audit run
  `analysis/leftdeep-joins/2026-08-05-p59run4-audit-off.txt`: **Q9's final joinrel
  6.3× against the ≤10² bar** (`est=1999060 actual=316264`), `parity_violations=0`
  over 21 matched joinrels. One absolute tripwire remains, Q18 at 25 526×, and it
  is exactly what §4.1's parity ratchet was introduced for — PG 18.3 is at
  5 386×/9 428× on its own shapes for the same query (P5.6-g-iii/-g-v).
  4 tests (`relsize_baserel_placement_test.go`); 1 ledger row. Bar met: UNITS +
  audit (re-read, not re-run — no plan-reachable behaviour changed S-cold) + SPOT.
  - [x] **P5.6-a** the per-clause substrate: `examine_variable`,
    `get_variable_numdistinct` and `eqjoinsel`'s no-MCV arm over
    `restrictInfo` operands. *(DONE 2026-08-04 —
    `internal/planner/joinselectivity.go` + `catalog.ColumnStats.StaDistinct`.
    The compounding this exists to end is specific: the legacy
    `estimateJoinCost` divides |L|·|R| by the PRODUCT of every spanning edge's
    per-side NDV (bushy.go:1266-1301) where PG divides by ONE ndistinct — the
    LARGER of the two sides' — per clause, because upstream's estimate is the
    MINIMUM of two upper bounds, not a per-edge product. **The finding that
    shaped the dispatcher:** the operator decides, `isEquijoin` does not.
    `a.x = b.y + c.z` is an equality that splits into no two one-sided
    operands, so it can key no hash join and `isEquijoin` is false — but PG
    still prices it with `eqjoinsel`, and routing it to
    `clause_selectivity`'s 0.5 fall-through instead would charge 100× the
    0.005 upstream charges, on every joinrel above it. What the flag governs
    is only which OPERANDS are examined, since pairing `bo.Left` with
    `ri.leftRelids` — the two are free to be different sides — would read one
    relation's column against another's statistics. **Second finding:** goopg
    splits upstream's one signed `stadistinct` into `NDistinct` +
    `NDistinctFrac`, and the reduction back to PG's convention was
    open-coded at the pg_statistic heap row and the pg_stats view; a third
    copy in the estimator is the shape where the planner silently plans on a
    different number than the one it shows the user, so it became
    `ColumnStats.StaDistinct` and all three now read it. 3 ledger rows
    (eqjoinsel's MCV arm; `vardata->isunique`, which is P5.6-b's own
    mechanism; `examine_variable`'s subquery/expression arms). 17 tests
    (`joinselectivity_test.go`). Still inert — `sizeJoinRel` has no
    production implementation until P5.6-b.)*
  - [x] **P5.6-b** `calcJoinrelSize` + the concrete `joinRelBuilder`:
    04 §3.1's FK/unique-superkey generalisation over clause SUBSETS
    (`get_variable_numdistinct`'s `isunique` arm, replacing
    `uniqueNoFanoutRawCount`'s edge-list form) driving the rows-once
    discipline of 04 §2. *(DONE 2026-08-04 —
    `internal/planner/joinrelsize.go`: `calcJoinrelSize`,
    `superkeyJoinSelectivity` and `searchJoinRelBuilder`, the concrete builder
    that finally binds sizing to `addPathsToJoinrel` at `makeJoinRel`.
    **The finding that shaped the mechanism:** the no-fan-out cannot be a
    divisor bolted onto the per-clause estimate, it has to be PG's
    remove-and-substitute — `get_foreign_key_join_selectivity` takes the
    covered clauses OUT of the restriction list and puts one `1/raw-tuples`
    in their place, and it is the removal that stops those clauses being
    charged a second time by `eqjoinsel`. On a two-column key the difference
    is not cosmetic: the marginals price the join at `1/nd_a · 1/nd_b`, which
    on the test's `partsupp` shape is 2.5e6× tighter than the `1/800000` the
    key actually implies. **The asymmetry that is easy to get backwards:** a
    UNIQUE index makes its OWN relation the key side, but a declared FK makes
    its relation the CHILD, so the divisor is the PARENT's raw count
    (`1.0/ref_tuples`, costsize.c:5847) — the legacy
    `uniqueNoFanoutRawCount` divides by whichever table carried the
    constraint, which on a fact-to-dimension join divides the fact table's own
    cardinality out of the estimate (ledgered; it dies with P6.3). Three
    further upstream properties reproduced deliberately: the RAW (not
    filtered) divisor, whole-key cover (`⊆` is the test on the KEY's columns —
    extra equated columns stay residual and are charged on top), and a clause
    consumed at most once. `selectivityClauses`'s EC winner rule was factored
    into `oneClausePerEquivClass` rather than copied, because the sizer is
    handed the joinrel's FULL restriction list and has to apply the same rule
    from the other direction (rule #2). 4 ledger rows (joinrel width = sum of
    input widths vs `build_joinrel_tlist`; `vardata->isunique` still unset in
    `examineJoinVar`; the legacy child-divisor defect; `nconst_ec`). 12 tests
    (`joinrelsize_test.go`). Still inert — `GOOPG_PGSHAPED_DP` is OFF and
    nothing calls `joinSearch` from `planSelect`.)*
  - [x] **P5.6-c** 04 §3.3's clamp discipline: the FK-implied bound, and the
    `max(l,r)` non-FK fallback cap kept beside it. `keyImpliedRowsBound` +
    the two clamps in `calcJoinrelSize`; `superkeyJoinSelectivity` now answers
    a `superkeyEstimate` (sel, residual, fired, rowsBound) because "a key was
    proven" cannot be recovered from the selectivity afterwards, and
    `joinClauseSelectivityExt`/`eqJoinSelectivityExt` carry PG's `*isdefault`
    out to the fallback condition. Bound taken only when the key relation is
    the whole of its side (2 ledger rows: the multi-rel case, and the cap's
    absence from upstream). 7 new tests. Bar met: UNITS.
  - [x] **P5.6-d** delete the quadratic build penalty (bushy.go:632) once
    04 §4's honest batch-I/O term prices what it was standing in for.
    *(DONE 2026-08-05, the loop after P5.7-a supplied that term. §1's
    delete-list had two entries for one missing term; P5.7-a retired the one
    inside `hashJoinCost`, this one retires `costJoinCandidate`'s. **Why it is
    not a like-for-like swap:** the penalty's threshold was a fixed ROW COUNT
    and spilling is decided in BYTES, so against the 512 MB default budget it
    was wrong in both directions — a 4 M-row single-column build fits and was
    charged 40 000 anyway, a 1 M-row 40-column build spills to 4 batches and
    was charged nothing. Two tests replace the one that pinned the penalty:
    `TestCostJoinCandidateHasNoRowCountPenalty` (the DP's hash cost is now
    EXACTLY `hashJoinCost` — fails if any penalty returns) and
    `TestCostJoinCandidateStillDetersHugeBuilds` (the defence survives via the
    spill term). No ledger row: nothing PG does is left unimplemented by this —
    upstream has no such penalty, which is the point. Bar met: UNITS. DS05 not
    run and not skipped: `costJoinCandidate` is reachable only under
    `costDrivenJoinOrder`, OFF by default, so the default arm is byte-identical
    — 04 §4.2.)*
  - [x] **P5.6-e-i** the estimate-audit INSTRUMENT + the pre-flip baseline
    ([09](09-verification-and-acceptance.md) §5.1/§5.2). `cmd/estimate-audit`
    + `internal/estimateaudit`: one `EXPLAIN ANALYZE` per query supplies both
    sides of the comparison, the unit of audit is the joinrel, and the binary
    exits non-zero on a violation so it is instrument and tripwire at once.
    *(DONE 2026-08-04. **The finding that shaped the tool:** the audit is
    unrunnable on the query §5 names. goopg never propagates worker
    instrumentation out of a `Gather` — upstream merges it in
    `execParallel.c` `ExecParallelRetrieveInstrumentation` — and TPC-H Q9
    plans entirely below one, so the first run measured `(no ANALYZE)` for
    every joinrel of 10 of 12 queries. Hence `--serial`. Two more conditions
    are load-bearing for the same reason: per-connection ANALYZE stats
    (`--warm-stats`, one session for the whole run) and goopg's cumulative —
    not per-loop-average — `actual rows=`, which a PG-calibrated reader would
    multiply by `loops`. **Baseline, legacy planner, all 22 queries:** five
    joinrels over 10³, worst Q18's final SEMI at 2.5 × 10⁷ over; **Q9's final
    joinrel 124.7× over (est 39 447 200 vs 316 264 actual)** — just outside
    §5's ≤ 10² bar, with its three outermost joinrels all carrying the SAME
    estimate while the actual collapses 19× across them. 12 tests
    (`internal/estimateaudit/audit_test.go`). 4 ledger rows: the Gather gap,
    the per-loop divergence, `InitPlan`/`SubPlan` joins uninstrumented even
    in serial, and P5.6-e's acceptance check itself deferred to P5.9.)*
  - [x] **P5.6-e-ii** close the two class-(a) causes the baseline isolated
    (09 §5.2) — a SEMI/ANTI joinrel priced at its outer input verbatim
    (`calc_joinrel_size_estimate`'s JOIN_SEMI arm, costsize.c), and a
    joinrel's non-equi restriction contributing no selectivity (Q19's OR,
    Q3's re-applied `Filter:`) — then re-run the audit. Q9's ≤ 10² bar is
    P5.9's to certify, on the post-flip planner.
    *(Landed 2026-08-04, `internal/planner/cardinality.go` +
    `selectivity.go`. 09 §5.3 and
    `analysis/leftdeep-joins/2026-08-04-p56eii-README.md` carry the
    before/after. Q19 328 705× → 13.1× under, Q20-final 891× → 9.5× under,
    Q21 499× → 9.7× under, Q22 643× → 1.8×, Q4 485× → 7.3×; Q9 unchanged at
    124.7×; no new violations. 10 tests. The residual pricing needed
    `columnStatsForChild` to resolve through a join AND to remap through a
    Project's targets — the latter a silent sibling divergence that had been
    answering with another column's MCV list. Spun out: **P5.6-e-iii**.)*
  - [x] **P5.6-e-iii** de-saturate ANALYZE's ndistinct (Haas–Stokes,
    upstream `compute_distinct_stats`, analyze.c), THEN resolve the join keys
    in the merged left‖right coordinate space they are written in and let
    `columnNDistinctForChild` resolve through a join. The order is forced by
    measurement, not preference: the coordinate correction alone was run and
    rejected (09 §5.3, `2026-08-04-p56eii-postfix.txt`) — it made every
    joinrel it touched more accurate and Q9's final 124.7× → 176 424× over,
    because a saturated `nd` compounds up the chain and because supplying
    `nd` removes the M0126-0010 `max(|l|,|r|)` cap, which fires only on the
    nd-unavailable path. Bar: UNITS + DS05 + audit run.
    **DONE 2026-08-04** — `executor.ndistinctEstimate` +
    `rightKeyNDistinct` on the equi arm + the `*Join` arm on
    `columnNDistinctForChild`; cap left fallback-only. Violations 5 → 2
    (09 §5.4). Two regressions spun out rather than absorbed:
    **P5.6-f** (multi-key pricing + `fkselec`; owns Q9, which went
    UNMEASURED) and **P5.6-g** (`eqjoinsel_semi`'s MCV arm; owns the
    SEMI/ANTI `est=1` collapse).
- [x] **P5.6-f** price EVERY `HashKeys` pair (`clauselist_selectivity`) AND
  add the `get_foreign_key_join_selectivity` analogue — the two halves must
  land together; the first alone estimates Q9's 2-pair join at ≈2 rows.
  P5.9 cannot certify Q9's ≤10² bar until this lands. Bar: UNITS + DS05 +
  audit run with Q9 measurable.
  **DONE 2026-08-04** — `joinkeyproof.go` (the superkey prover +
  `resolveBaseColumn`) + `joinEquiPairs` on both the estimator and the
  residual + `SeqScan/IndexScan.UniqueKeys` stamped where `SmallDim` is.
  Preceded by step 0: the eight UNIQUE indexes re-created on the bench
  cluster and the audit re-baselined on them (09 §5.5). Violations 2 → 2, no
  joinrel worse, Q9's target joinrel 80× over → EXACT, Q20 12.2× → 3.1×.
  Q9 measurable again at 291.8 s but still over the audit's 150 s: its
  cardinality is closed, its SHAPE is not, and the reason is a separate
  mechanism — spun out as **P5.6-f-ii**.
- [x] **P5.6-f-ii** the legacy join-order SEARCH does not use `estimateJoin`.
  `estimateJoinCost`'s production branch (bushy.go, `costDrivenJoinOrder`
  OFF) computes `ndv` as max NDistinct over EVERY column of the edge's two
  tables, ignoring the join key; the multi-edge + superkey arm beside it is
  gated on the flag M0126 left OFF, and its FK case divides by the CHILD's
  raw count where upstream divides by the parent's (costsize.c:5847). Result:
  P5.6-f reaches every printed estimate and the search not at all, which is
  why Q9's exact joinrel still yields a plan applying the 5.3 %-selective
  `part` filter ABOVE three full-cardinality hash joins. Bar: UNITS + DS05 +
  audit run with Q9 inside the 150 s timeout.
  **DONE 2026-08-05 (09 §5.6).** That cause was real and insufficient. Two
  more, both found by instrumenting the DP: a `joinEdge`'s key
  `ColumnRef.Index` is in GLOBAL FROM-list coordinates, so
  `accurateKeyDistinct` returned 0 for every join key in Q5 (or, in range by
  accident, `n_comment`'s count for `n_nationkey`) — the P5.6-e-ii `RightKey`
  class a third time; and it bypassed `StaDistinct()`. All three landed
  together because the proof ALONE was measured worse than the bug: Q5's
  `lineitem ⋈ supplier` went truthful while `customer ⋈ supplier` kept reading
  10 000 against 60 000 000, and Q5 went 65.9 s → over the timeout
  (`2026-08-05-p56fii-halfway.txt`). `uniqueNoFanoutRawCount` deleted for
  `graphJoinKeyDivisor`, now on both search modes. Violations 2 → 2, no
  joinrel worse, **Q9 UNMEASURED → 6.3× over and 291.8 s → 16.6 s**, zero
  runtime regressions, stream 546.8 s → 445.1 s (0.81×). Two ledger rows
  (uncovered-edge selectivity; the coordinate space fixed at the reader, not
  the writer). PLAN re-pinned to `plan_snapshots/m0127-p56fii.txt`.
- [x] **P5.6-f-iii** the DS05 gate's single TIMEOUT "hopped" Q72 → Q47.
  **DONE 2026-08-05 (09 §5.15) — the sweep-tail-confound reading is REFUTED;
  the TIMEOUT was MOVED by `ce027cee` (P5.6-f) itself.** Answered without the
  three ~1 h sweeps the item asked for: an eight-sweep/four-sweep step function
  (±3 s within regime, not noise); the confound is structurally unable to reach
  Q47, which runs at position 47 BEFORE Q72 and is itself the first timeout in
  the new regime, and a fresher post-restart server cannot explain Q57 getting
  5× SLOWER; solo quiet-host runs at `TIMEOUT_SEC=900` giving **Q47 523 s** and
  Q57 81 s outside any sweep tail where the hypothesis predicted ≈31 s; and a
  bisect against a 2.3 G COPY of the cluster (live dir never at risk) giving
  `30293f78` 31 s, `29daeb72` 30 s with a byte-identical plan, HEAD 523 s —
  the old binary on TODAY's data is fast, exonerating the cluster data too.
  `29daeb72..ce027cee` is one commit; the boundary sweep only *looks* like
  `29daeb72` because its header says `[tree DIRTY in Go sources]` /
  `diff=129e691bd41a` (that binary was `29daeb72` + uncommitted P5.6-f WIP).
  Mechanism: P5.6-f folds EVERY equi-pair under **independence**
  (`cardinality.go:457-483`), and Q47's outermost join has five, two of them
  strongly correlated (`i_category`↔`i_brand`,
  `s_store_name`↔`s_company_name`), so it degrades from a 5-pair `Hash Join`
  to a `Nested Loop` with no join condition. **P5.6-f stays** — net win
  (+Q72 timeout→166 s, +Q53 28→6 s, +Q9), correctness never moved. Successors
  **P5.6-f-iv** (the functional-dependency arm: PG's `clauselist_selectivity` /
  `dependencies.c`, which goopg lacks — it landed only the FK arm) and
  **P5.6-f-v** (the sweep must diff the TIMEOUT SET, not its cardinality:
  `TIMEOUT=1` stayed byte-identical across a 17× re-pricing for four sweeps —
  **landed 2026-08-05**, §5.16).
  `analysis/m0127-p56fiii/README.md`; 1 ledger row.
- [x] **P5.6-g** `eqjoinsel_semi`'s MCV arm + the `(1 - nullfrac1)` factor.
  **DONE 2026-08-05** — both arms landed verbatim from selfuncs.c (matched-MCV
  mass exact, nd heuristic on the discounted remainder only, `CLAMP_PROBABILITY`,
  the factor on every branch incl. the punt), reading statistics through
  `resolveBaseColumn` rather than `columnStatsForChild` so the answer does not
  depend on which scan the planner picked. 13 tests.
  **Measured no-op on TPC-H** (violations 2 → 2, bit-identical; everything else
  inside ANALYZE's ±5 % sampling noise), and both halves separately proven on
  real data: 20 → 5 010 against actual 5 010 when the inner has an MCV list,
  1 000 → 750 against actual 750 at 25 % outer nulls.
  **The premise was wrong, and the oracle says so** (09 §5.7): PG 18.3
  estimates `rows=1` for Q21's anti-join too — `neqjoinsel` returns
  `1 - nullfrac` for SEMI/ANTI by design — so Q21 is an audit override, not an
  estimator defect; and Q18's SEMI is the **0.5 punt** caused by a missing
  `*HashAggregate` arm in `resolveBaseColumn`, which neither new arm reaches.
  Successors **P5.6-g-ii** / **P5.6-g-iii**. Bar met: UNITS + SPOT + audit run
  + two real-data probes. DS05 blocked by the live nightly batch (the gate
  self-refuses); carried.
- [x] **P5.6-g-i** the carried TPC-DS SF0.5 gate, run 2026-08-05 on a host the
  nightly batch had cleared — and it turned out to owe **three** commits a gate,
  not two: `4b820ab8` (P5.6-f-ii) also landed after the last DS05 baseline
  (`ce027cee`). Result: `PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1
  SKIP=4`, identical to the baseline line for line — not one row count and not
  one of the 57 value checksums moved. Whole-corpus `EXPLAIN` captures at all
  four arms then attributed the plan churn exactly (noise floor measured at
  zero: the same binary twice gives byte-identical plans for all 99):
  **P5.6-f-ii moved 74 of 99 plans, P5.6-g moved 1 (Q83), P5.6-g-iv moved 4**
  (Q13, Q41, Q48, Q85). The reason the item was raised in priority — TPC-DS's
  nullable join keys should let `(1 - nullfrac1)` move plan shape — is
  **measured false**; the corpus-wide re-ordering is the search finally reading
  the join key (§5.6), 74 plans moved with zero rows moved. Evidence
  `analysis/leftdeep-joins/2026-08-05-p56gi-*`;
  [09](09-verification-and-acceptance.md) §5.10. Successor filed: the gate has
  no plan-shape channel of its own.
- [x] **P5.6-g-i-b** the DS05 gate's plan-shape channel, landed 2026-08-05 —
  P5.6-g-i's successor, and the answer to "74 plans moved and the gate said
  nothing". `scripts/tpcds-plan-diff.py` + a `plans` subcommand and a sweep tail
  stage in `scripts/tpcds-sf05-regression.sh`: one `EXPLAIN`-only pass over all
  99 (nothing executes), `plans-<stamp>.txt` written beside `sweep-<stamp>.txt`,
  and a per-query diff against the previous capture appended to the report.
  **Strictly a second column** — row counts and checksums still decide the
  verdict, and a sweep run with a deliberately broken `PLAN_DIFF` still exits 0.
  Full corpus always, even under `QUERIES=` (14 s). Validated without re-running
  the sweep: three consecutive captures at one commit → `changed=0`, and against
  P5.6-g-i's four committed corpus captures the new path reproduces §5.10's
  table exactly (D→0, C→4 = Q13/41/48/85, B→5 = +Q83, A→75). Found and fixed one
  real defect — psql's error prefix embeds the script PATH, so Q36/Q70/Q86 (the
  dsqgen artefacts, whose block is an error not a plan) were three permanent
  false positives on any directory change. Evidence
  `analysis/leftdeep-joins/2026-08-05-p56gib-README.md`;
  [09](09-verification-and-acceptance.md) §5.11.
- [x] **P5.6-g-ii** the `*HashAggregate` arm for `resolveBaseColumn`, and Q18's
  real shape. **DONE 2026-08-05** — and the item as filed was the wrong half of
  itself. The arm alone measures worse because **upstream does not have it**:
  `examine_simple_variable` (selfuncs.c) hits `if (subquery->groupClause)`, sets
  `vardata->isunique` when the referenced output is the sole grouping column,
  and returns "cannot go further" *without* a statistics tuple. What crosses a
  grouping node is UNIQUENESS, never a distribution. Landed as
  `resolvesToGroupUniqueColumn` / `groupUniqueNDistinct` consumed only by
  `columnNDistinctForChild`, with `resolveBaseColumn` still refusing to walk a
  grouping node and a test pinning that MCVs do not leak up; DISTINCT /
  DISTINCT ON are the same upstream test's other halves.
  **Q18 42 837× → 24 242×** (2 998 620 → 1 696 939, the `× 0.5` punt gone),
  parity excess vs PG 8.0× → 4.5×; still the corpus's one violation, but the
  residual is now attributable to goopg's *more* accurate `l_orderkey`
  ndistinct making its post-HAVING inner 3.6× larger than PG's.
  **`reduce_unique_semijoins` measured INERT** at goopg's join order (for a
  unique inner, `inner_rows` = nd2 and the INNER and SEMI formulas agree term
  for term); it buys join-order freedom, not a number — ledger row.
  **Found and fixed what it exposed: `estimateJoin` had no outer-join arm** —
  LEFT/RIGHT/FULL took the INNER product and stopped before
  `calc_joinrel_size_estimate`'s "at least as large as the non-nullable input".
  Q77 estimated 885 rows for a join whose outer is 8 885. 12 tests.
  Bar met: UNITS + **DS05 sweep `PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=1 SKIP=4`, identical to baseline** (12 of 99 plans moved, zero rows;
  stream 2 116 s → 2 074 s, Q80 41→14 s, Q40 16→2 s, Q78 29→17 s) + audit run
  (violations 2 → 1, no joinrel worse). Evidence
  `analysis/leftdeep-joins/2026-08-05-p56gii-*`;
  [09](09-verification-and-acceptance.md) §5.12. 3 ledger rows.
- [x] **P5.6-g-v** Q18's residual, which is NOT a HAVING problem.
  **DONE 2026-08-05** — the prescribed `EXPLAIN` settled it: PG 339 423 →
  **113 141** and goopg 1 150 720 → **383 573** are both exactly ÷3, i.e.
  DEFAULT_INEQ_SEL over an aggregate neither engine has statistics for
  (upstream via `cost_agg`'s `clauselist_selectivity(quals)` scaling of
  `output_tuples`, goopg via the `*Filter` over the `*Aggregate`). **The
  HAVING mechanism is already identical; the whole 3.39× gap is the group
  estimate, and goopg's ndistinct is the MORE accurate one** (1 150 720 vs
  339 423, truth 1 500 000 — PG is 4.4× low). Closing the gap would mean
  degrading goopg's statistics, so the item **closes with no estimator
  change**; Q18's violation is inherent to pricing an aggregate blind and is
  shared with upstream.
  **What the measurement found instead — an instrument defect.** goopg splits
  a qual from the rows it filters: the predicate rides a `*Filter` wrapper
  that `walkPlanFiltered` collapses onto the child below it, and the collapsed
  line printed `EstimateRows(child)`, the PRE-qual count, beside a `Filter:`
  the estimator had already applied. Upstream cannot have this gap — the qual
  and the rowcount share one struct. The estimator was always right (a
  *parent* reads `EstimateRows(*Filter)`, which is why a `Gather` over a
  filtered scan was correct while the scan under it was not); only the
  rendered line lied, by exactly the selectivity. Filtered `lineitem` scan
  **5 997 241 → 1 689 312** (PG 1 673 754), `nation` 25 → 4 (PG 5), TPC-DS
  `date_dim WHERE d_year = 2000` **73 049 → 365** (PG 365). P5.6-g-iii-class:
  `estimateaudit` parses that field and §5.11's plans channel captures it, so
  every pre-fix capture reports filtered relations at unfiltered size.
  Bar met: UNITS + audit (**1 violation, Q18, unchanged**; all joinrel diffs
  sub-1 % ANALYZE noise, none worse) + DS05 `plans` (**95 of 99 changed, but
  with `rows=` normalised the diff is 6 lines — a psql header width — i.e.
  zero structural movement**). 3 regression tests, each verified failing
  without the fix. Evidence
  `analysis/leftdeep-joins/2026-08-05-p56gv-postfix.*`;
  [09](09-verification-and-acceptance.md) §5.13. 2 ledger rows.
  Successor **P5.6-g-vi** (re-read pre-fix plan-text conclusions).
- [x] **P5.6-g-vi** re-read the conclusions drawn from pre-fix plan text.
  Done 2026-08-05, **no code change** ([09](09-verification-and-acceptance.md)
  §5.14; working
  `analysis/leftdeep-joins/2026-08-05-p56gvi-README.md`). The two DS05 corpus
  captures bracketing `20e17fa5` are line-aligned, so a positional diff gives
  the blast radius exactly: **836 of 3 283 node lines (25.5 %) changed, across
  96 of 99 queries** — and the split is clean, **836 of 966** `Filter:`-carrying
  lines moved against **0 of 2 317** bare ones. **The rule for reading any
  pre-fix capture is therefore exact: a `rows=` is trustworthy iff its node
  line has no `Filter:` detail.** Where it is wrong it is badly wrong
  (overstatement median 9×, p90 18 000×, max 1 920 800×), and it covers **join
  nodes** too — Q1's `Hash Join … Filter: (d_year = 2000)` went 716 → 3, so
  P5.6-g-v's "join nodes carry no collapsed `*Filter`" is a TPC-H fact, not a
  general one. **Verdict: M0125-0026 C2 (both forms), M0125-0038 (C5),
  M0125-0040 (C6), M0125-0031 and the §5.3–§5.12 audit joinrel conclusions all
  SURVIVE; none needs re-deriving** — the audit ones by direct re-measurement,
  the rest because the lines they quote are bare (C2/C5/C6 are *about*
  relations goopg failed to filter). **One correction, running the other way:**
  P5.6-g-v's "the row-count half of M0125-0026's `date_dim` 73 049 was an
  artifact" is too broad — C2 measured 66 of 68 quals on join nodes, so those
  scans carry no filter and 73 049 is what the estimator genuinely used; only
  C2's two named exceptions (Q14/Q54 scalar SubPlans) are corrupted, and they
  are cited for placement, not rows. C5's `365.25 = 73 049 × DEFAULT_EQ_SEL`
  was *divided out* of the join estimate, i.e. C5 independently observed the
  estimator holding the post-qual number the renderer hid. Pre-fix captures
  stay as-is; the rule is recorded so it is not re-derived a third time.
  No ledger row: bookkeeping over an instrument, no PG behaviour left
  unimplemented.
- [x] **P5.6-g-iii** the audit's bar itself: a Q21 per-query override (PG is
  equally wrong there, by design), and §4's ratchet restated as per-joinrel
  parity against the PG 18.3 reference rather than an absolute factor — PG
  trips the current 1 000× tripwire on Q18 at 1 674×.
  Landed 2026-08-05 (09 §4.1 + §5.8): `estimateaudit.Q21AntiJoinMax` beside
  Q9's bar (both now carry their justification into the artifact), and
  `internal/estimateaudit/parity.go` — joinrel identity = the base-relation
  SET (`RelOptInfo.relids`) reconstructed from the printed plan, so different
  join ORDERS still compare; ratchet fires on excess >10× **and** goopg's own
  factor >100×. `--from-plans` / `--reference` / `--ref-port` let a new
  instrument be applied to old committed evidence. Baseline pinned:
  `parity_violations=1 shape_mismatches=67`; absolute violations 2 → 1.
  Successors **P5.6-g-iv** (Q19, the only proved estimator defect) and a
  ledgered EXPLAIN rendering gap (no `lineitem_1` deduplication).
  Bar met: UNITS + an audit run reporting the parity column.
- [x] **P5.6-g-iv** Q19 `{lineitem,part}` closed 2026-08-05 — and the collapsing
  step was none of the three candidates. It was a preprocessing pass goopg never
  had: **PG's `canonicalize_qual` / `process_duplicate_ors` (prepqual.c)**, which
  hoists the conjuncts common to every arm of an OR out of the OR before the qual
  is distributed. Without it Q19's thrice-repeated join clause was priced once as
  the equi-join key and again per arm at DEFAULT_EQ_SEL, and the three
  single-relation conjuncts common to all arms were priced nowhere at all.
  Landed as `internal/planner/qual_canonical.go` (`canonicalizeQual`) applied in
  `planSelect` at upstream's placement, with `strictParserExprKey` (exprkey.go)
  as the equality test — `parserExprKey` normalises table qualifiers away and
  would hoist across `a.x = 1` / `b.x = 1`. 9 tests. est 1 → 309 vs actual 131,
  parity excess 126.5× → 2.3×, `parity_violations=0`; Q12 (the only other
  OR-bearing TPC-H query) bit-identical. [09](09-verification-and-acceptance.md)
  §5.9. Bar met: UNITS + audit (parity column) + tpch-spotcheck PASS. **DS05
  carried on P5.6-g-i** — it is where this pass's blast radius actually gets
  measured, since TPC-DS has many OR-bearing queries and TPC-H has two.
- [x] **P5.6-f-v** the DS05 gate's status-delta channel, landed 2026-08-05 —
  P5.6-f-iii's harness successor, and the answer to "a 17× re-pricing hid
  behind four byte-identical SUMMARY lines". `scripts/tpcds-sweep-diff.py` + a
  `delta [OLD [NEW]]` subcommand and a `sweep` tail stage placed directly under
  the SUMMARY (before the slower plan pass): the per-query **status/runtime
  vector** is diffed against the previous FULL report and printed by name —
  `TIMEOUT +Q47 -Q72`, `SLOWER Q57 15s->81s (5.4x)`. **The input is the sweep
  report itself**, in the format `cmd_sweep` already prints, so no new artefact
  exists and all ~90 archived reports are valid baselines retroactively.
  **Strictly a third column** — rows and checksums still decide the verdict,
  and the channel swallows its own failures. Four limits, printed in its header
  every run rather than assumed: intersection-only comparison (a query absent
  from one side is named as ONLY-OLD/ONLY-NEW, not counted as leaving PASS);
  TIMEOUT readings excluded from the runtime arm (they are the cap, not a
  runtime); ≥2× AND ≥5 s on the larger side (integer seconds make 1s→3s "3×");
  and the default baseline skips SUBSET PROBES, which are stamped "NOT a gate
  result". Validated by replay over all 87 adjacent pairs of the archived
  corpus — zero parse failures, 17 verdict changes, and on the pair whose
  SUMMARY lines are byte-identical it prints `TIMEOUT +Q47 -Q72` plus Q57's
  5.4×, i.e. both §5.15 victims out of artefacts that already existed that
  night. Bar met: UNITS + one full DS05 sweep with the channel live
  (`PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4`,
  `verdict-changes=none`, one runtime move Q83 7→3 s).
  [09](09-verification-and-acceptance.md) §5.16.
- [x] **P5.7-a** nbatch-aware `hashJoinCost` via the shared sizing fn. DONE
  2026-08-05. `hashJoinCost` takes a `hashJoinInputs` struct and calls
  `hashsize.Choose` — the executor's own function with the executor's own
  argument shape — then applies upstream's batch I/O charge verbatim when it
  answers `NBatch > 1` (costsize.c:4239-4248: `innerPages` at startup,
  `innerPages + 2·outerPages` at run). It REPLACES M0126-0013's unconditional
  `seq_page_cost · innerRows/100`, which cited a page charge upstream does not
  make (PG charges pages only under `numbatches > 1`, and for the spill rather
  than the resident table) and which, being monotone in `innerRows`, could not
  draw the fits/does-not-fit distinction that decides the plan.
  New `RelOptInfo.NCols`: goopg's hash entry is a `[]Datum` of 48-byte structs,
  so its size follows the COLUMN count, not the byte width `Width` carries, and
  the executor passes `len(schema)` — feeding `Width` here would have sized the
  same build ~25× differently on the two sides of the sibling-path rule.
  Deferred (2 ledger rows, 2026-08-05): per-session `work_mem` does not reach
  the planner (fixed at the executor's 512 MB fallback, so the two agree at the
  default and only there), and `spillPages` prices the in-memory footprint
  rather than the narrower batch-file encoding. `nbatch` is still not exposed
  on the `Path` for EXPLAIN. Bar met: UNITS; PLAN not applicable — both
  `hashJoinCost` callers are behind OFF-by-default gates (`costDrivenJoinOrder`,
  and the PG-shaped DP, which has no `planSelect` caller at all), so the default
  arm has zero reachable plan movement.
  [04](04-cost-and-cardinality.md) §4.1, [06](06-hash-spill-and-memory.md) §5.
- [x] **P5.7-b** Startup/Total split for LIMIT-over-join. *(DONE 2026-08-05.
  `internal/planner/tuplefraction.go`: `preprocessLimit` (`preprocess_limit`,
  planner.c:2577) derives PG's `tuple_fraction` from the `*Limit` above the
  join, `searchCtx.tupleFraction` carries it, and `searchCtx.finalPath()` is
  `get_cheapest_fractional_path` (planner.c:6617) over the final rel — the only
  value a caller may hand `createPlanAtSearchRoot`, because reading
  `CheapestTotal` discards the fraction. The finding: the fraction is TWO
  mechanisms and only the pair moves a plan — selection, plus RETENTION through
  the new `RelOptInfo.ConsiderStartup` (`consider_startup`, relnode.c:211/707)
  enforced in `comparePathCostsFuzzily`'s two "different" arms (pathnode.c:
  178-183). goopg had behaved as if `consider_startup` were permanently true,
  keeping fast-start paths PG prunes, and then always selecting on total cost;
  three existing tests were asserting on such paths and now state the fast-start
  regime they meant. Absolute-vs-fractional overload kept: `preprocessLimit`
  emits the absolute count, `getCheapestFractionalPath` converts it against
  `CheapestTotal.rows`, and `compareFractionalPathCosts` folds anything outside
  (0,1) onto the total-cost order. Acceptance:
  `TestLimitOverJoinMovesTheChosenPath` — hash with no LIMIT, loop under
  `LIMIT 100`, hash again under `LIMIT 5000`. 5 tests. Deferred (3 ledger rows):
  no `estimate_expression_value` const-fold on the LIMIT expression,
  `consider_param_startup` unreachable behind 03 §4.4's pin, and no production
  producer of a fraction yet. Still inert — no `planSelect` call site,
  `GOOPG_PGSHAPED_DP` OFF. Bar met: UNITS. PLAN not applicable for the same
  structural reason as P5.7-a: every consumer is behind an OFF-by-default gate.
  **↳ Both "still inert" claims EXPIRED 2026-08-06** when P5.9 flipped
  `GOOPG_PGSHAPED_DP` on by default, and the third ledger row is DISCHARGED:
  P5.9-b's `searchTupleFraction` is the production producer. See the P5.7
  roll-up below.)*
  [04](04-cost-and-cardinality.md) §4.3.
- [x] **P5.7** (roll-up) nbatch-aware `hashJoinCost` + the LIMIT Startup/Total
  split. *(CLOSED 2026-08-06. Both sub-items were already `[x]`; what the
  roll-up owed was its BAR — "PLAN, default arm ZERO diffs" — whose premise
  expired between the sub-items landing (2026-08-05 12:20/12:47) and this read.
  P5.9 flipped `GOOPG_PGSHAPED_DP` ON at 2026-08-06 02:22, so the zero-diff
  containment check can no longer be stated: the default arm is now supposed to
  move plans. What discharges P5.7 instead is that both halves were in the tree
  the flip's own acceptance measured — run 4 (`23dcc60e`, 01:04) and the
  default-arm audit (Q9 final joinrel 6.3×, parity_violations=0) both post-date
  them. Same commit swept the 28 planner files whose "Still inert" headers had
  outlived the flip, `cost_funcs.go` — where `hashJoinCost` lives — among them.
  Bar met: UNITS + SPOT.)* [04](04-cost-and-cardinality.md) §4.4.
- [x] **P5.8** Collapse limits wired with PG's actual semantics (03 §6:
  flat comma lists are always ONE problem; limits govern sub-joinlists and
  explicit JOINs only; =1 pin semantics); explicit INNER JOIN flattening
  behind its own sub-flag `GOOPG_PGSHAPED_COLLAPSE` (soaked separately from
  the enumerator, 08 §2); outer joins stay pinned until `join_is_legal`
  constraint inference lands (03 §4.4).
  DONE 2026-08-05 — `internal/planner/collapse.go` ports the joinlist half of
  `deconstruct_recurse` (initsplan.c:1148-1452); computed in
  `planFromClause`/`planFromRangeVars` onto `resolveContext.joinlist`, read by
  nobody until P5.9. 8 tests; acceptance is
  `TestFlatCommaListIsOneProblemAtAnyWidth`. See 03 §6.1 for the three
  findings. **The 12-table bail-out is NOT deleted here** — 03 §7 says it dies
  *with the bushy DP* (P6.3), and it must: it guards the old 3ⁿ subset-bitmask
  DP, which is still the production path. Ledger rows: per-session collapse
  GUCs unreachable; no joinlist consumer yet.
- [x] **P5.9-a** `make_rel_from_joinlist` — the joinlist's CONSUMER (03 §6.2),
  the piece P5.9 cannot run without: a joinlist had no reader and the search
  protocol existed only as a sequence each test re-assembled.
  DONE 2026-08-05 — `internal/planner/relfromjoinlist.go`:
  `planJoinlistSearch` / `makeRelFromJoinlist` (allpaths.c:3352) walk the
  joinlist, recurse on sub-lists, and run ONE problem per non-singleton list
  through `buildInitialRels` → `addBaseRelIndexPaths` → `joinSearch` →
  `finalPath` → the boundary. A sub-joinlist is planned separately and enters
  its parent as one `PathPrebuilt` leaf; `createPlanAtSearchRootRange`
  (createplanroot.go) is `createPlanAtSearchRoot` over a `[base, base+width)`
  window so the permutation check runs on a sub-problem's slice too. Clause
  placement needs no pass: each problem builds its list with per-ITEM
  `cumOffsets`, so an intra-item clause collapses to one bit (`relLevel < 2`,
  dropped — already placed below) and a clause reaching out of a sub-problem
  is declined there and placed by the parent. 10 tests; acceptance is
  `TestPlanJoinlistSearchPinnedSubproblemIsItsOwnSearch` (with the unpinned
  control arm that makes it non-vacuous). Still inert — no `planSelect` call
  site, `GOOPG_PGSHAPED_DP` OFF. Bar met: UNITS. 2 ledger rows: the
  pathlist-and-rows collapse at a sub-problem boundary; no residual-conjunct
  accounting yet (P5.9-b's job).
- [x] **P5.9-b** the `planSelect` seam: `tryBushyDP` /
  `runJoinSearchBelowPinned` hand `resolveContext.joinlist` +
  `preprocessLimit`'s fraction to `planJoinlistSearch` under
  `GOOPG_PGSHAPED_DP`, and decide which conjuncts the search consumed before
  the residual `Filter` above it is rebuilt.
  **DONE 2026-08-05 (03 §6.3)** — `internal/planner/joinsearchseam.go`:
  `tryPGShapedJoinSearch`, entered from the FIRST line of `tryBushyDP` so both
  join-search positions reach it through one door and the old DP stays the
  fallback S5's rollback needs. The fraction rides on
  `resolveContext.tupleFraction`, set in `planSelect` from the UNRESOLVED
  LIMIT/OFFSET (`searchTupleFraction`) because the `*Limit` node is built ~350
  lines later and resolving early would plan a `LIMIT (SELECT …)` subquery
  twice. Residual: `searchConsumes` asks `buildRestrictInfos` whether THIS
  conjunct becomes a clause — re-deriving it would drop an OR-of-ANDs, which
  reaches two relations but is not what the producer emits. **Three findings.**
  (i) the two cardinality entry points had different tier ladders and the search
  was on the shorter one — `estimateBaseRelInfo` has no `estimate_rel_size`
  fallback, so on a cold server every initial rel floors at 1 row and the cost
  model then correctly prefers a NESTED LOOP where the legacy pipeline built a
  hash join unconditionally; the seam applies the tier locally and the shared
  entry point is ledgered. (ii) local quals must enter the LEAF before the
  search, not the tree after it: `attachRelationLocalFilters` matches by pointer
  identity and P5.5-c's index arm rebuilds a leaf, so a qual attached afterwards
  can be attached to nothing; this needed one arm in `initialRelRows`
  (`leafBaseScan`), since a filter-wrapped base table was otherwise re-estimated
  by `EstimateRows` — a second, different selectivity over the same predicate.
  (iii) LATERAL has to be declined explicitly: `extractScans` flattens the CROSS
  chain that carries the marker, so the search would silently reorder around a
  dependency that is not a clause. Four `isSearchedTree` skips added (08 §3 now
  fully enforced, seven total). 9 tests; acceptance is
  `TestPGShapedSeamResidualIsWhatTheSearchDidNotPlace` (three conjunct classes,
  three destinations, asserted together — the failure mode is a conjunct that
  reaches none). Bar met: UNITS + SPOT (Q12 2 rows / Q13 35 rows). 3 ledger
  rows. Flag still OFF; the flag-off arm is byte-identical by construction (the
  seam declines on its first line and nothing else reads the new state).
- [x] **P5.9** S5 acceptance run per [09](09-verification-and-acceptance.md)
  §3 + plan-shape ratchet baseline (§4) + estimate audit (§5); flag flip or
  documented no-go.
  **↳ FLIPPED 2026-08-06 (09 §3.14).** `GOOPG_PGSHAPED_DP` defaults ON and
  survives as a kill-switch (`=0` only); `GOOPG_COST_DRIVEN_JOINORDER`'s env
  hook is retired. The evidence is run 4 (§3.10, five clauses) plus §3.13's
  clause-6 measurement. What the flip cost inside the tree: 24 standing unit
  tests that had been green through all four acceptance runs, every one of them
  a legacy REWRITE-RULE assertion on a fixture with no statistics — the rules
  do not read row counts, the search does. Four were a genuine harness gap
  (`newDDLFixture` installed no block-count sizer, so 4 000 rows on disk planned
  as one) and are fixed; the rest are pinned to the kill-switch arm with
  searched-arm counterparts added. The production worry — a populated,
  never-ANALYZEd relation planned blind into nested loops — was MEASURED on a
  live server and does not occur: both arms size it from block counts and plan
  the same hash join. Gates: full units, `tpch-spotcheck` PASS (Q12=2, Q13=35),
  pgbench smoke. The DS05 arm could not run (nightly CI batch holds the host)
  and the run-4 measurement of the same configuration stands in the interim.
  3 ledger rows; the collapse-ON pass is filed separately.
  **Run 1 executed 2026-08-05 — DOCUMENTED NO-GO** (09 §3.1;
  `analysis/leftdeep-joins/2026-08-05-p59-s5-acceptance.txt`). Flag stays OFF;
  the item stays open because P5.9 is the flip. Clause 5 passed (zero
  `MultiHashJoin`, zero fusion, both arms); clause 1 failed on four counts and
  clause 3 on two, and every clause-1 failure is the ONE defect below. §4/§5
  and the DS05 gate were deliberately not run — they score plan QUALITY on a
  build whose plans compute the wrong answer. Re-run the whole bar after
  P5.9-c/-d/-e. **-c, -d and -e are now done: the re-run is blocked only on
  P5.9-f (the Q17 correctness defect that -e attributed), and it must be driven
  by `scripts/tpch-acceptance-arm.sh` with `-digest` on both arms, discharging
  clause 1 through `tpch-runner -diff` (09 §3.3).**
  **Run 2 executed 2026-08-05 at HEAD `c00db762` — SECOND DOCUMENTED NO-GO**
  (09 §3.4; `analysis/leftdeep-joins/2026-08-05-p59run2-s5-acceptance.txt`),
  and the first run of this bar a clean checkout can reproduce. Clause 1:
  four failures → two (`22 MATCH, 1 ROWS-DIFF, 1 VALUE-DIFF`) — Q7/Q8/Q9 and
  Q17 all MATCH on values. Clause 5 PASS again. Clause 2 FAIL 1.36×, clause 3
  FAIL on Q7/Q9/Q10/Q18, **but Q9's named ≤ 170.9 s bar PASSES at 53.56 s**.
  Clauses 4/6 again not reached. The two surviving cells split: **Q2 is the
  flag's (→ P5.9-g); Q5 is the BASELINE's (→ M0119-0011)** — flag-ON agrees
  with PG 18.3, the default path is wrong by ~24×. Run 3 after P5.9-g.
  **Run 3 executed 2026-08-05 at HEAD `1964333a` — THIRD DOCUMENTED NO-GO**
  (09 §3.5; `analysis/leftdeep-joins/2026-08-05-p59run3-s5-acceptance.txt`),
  and the first that fails on PERFORMANCE ALONE. **Clause 1 PASSES**: 23 MATCH,
  1 VALUE-DIFF, and that cell is Q5, whose digests are byte-identical to run 2's
  on both arms so run 2's PG adjudication carries — 4 flag-owned failures → 2
  → **0**. Clause 5 PASS (third). Clause 2 FAIL 1.362×; clause 3 FAIL on
  Q10 3.91 / Q9 3.13 / Q18 2.47 / Q7 2.07 / Q12 2.07, **Q9's ≤ 170.9 s bar
  PASSES at 54.95 s**. Clause 4 (DS05, flag ON, first ever): **MISMATCH=0
  CKMISMATCH=0** but 7 ERROR + 5 TIMEOUT → FAIL. Clause 6 PARTIAL.
  **§4/§5 ran for the first time and named the timing gap**: parity violations
  0 (OFF) → 6 (ON), every one a joinrel the PG-shaped search sizes at `rows=1`
  against actuals of 5 869–1 999 080, and those five queries ARE the clause-3
  failures. Two-table reproducer + resume point in 09 §3.5; successors P5.9-h
  (estimate collapse) and P5.9-i (the DS05 assertion).
- [x] **P5.9-g** The decorrelated GROUP BY key was recorded in the scope it was
  FOUND in, not the one it is READ in. DONE 2026-08-05 (09 §5.22) — and not
  where this item predicted. At 4-under-5 the splice's
  `LeftKey`/`RightKey`/`Predicate` and the `= min` residual are all correct;
  P5.9-f's `outerWidth` fix generalised. The defect is one level down, inside
  the decorrelated `HashAggregate`: its GROUP BY key and its aggregate
  ARGUMENT were in different coordinate scopes. `SubCol` is recorded wherever
  the correlation is collected — the Filter walk records the conjunct's space
  (= the aggregate's input for a top-level Filter), but `harvestIndexKeyParams`
  records a LEAF-relative `is.Output()` position and its walk never
  accumulates an offset. Left-deep and unprojected, partsupp is Q2's first
  inner relation so `ps_partkey/0` agreed by accident; P5.9-c's rotated map
  puts partsupp at 14, `ps_partkey/0` reads `r_regionkey`, and every European
  row groups under the single key 3. Fix: `resolveSubColInSchema` resolves
  `SubCol` in the schema the consumer indexes (identity → name +
  `SourceTableIdx` → nil BAIL to the SubPlan), applied at
  `buildUnnestedSubquery`'s GROUP BY and the sibling
  `unnestScalarWithResiduals`' two `leftWidth + SubCol.Index` sites. The
  reproducer NEEDS the TPC-H PKs — without them the correlation stays in a
  Filter and both arms agree on a fixture that cannot fail. Tests
  `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput`,
  `TestResolveSubColInSchema`. Bar MET: UNITS + SPOT + DS05 (PASS=95,
  MISMATCH=0, plans 99/99 same) + Q2 arms on ONE binary
  (`c8fe0d352d75b67e`) → `tpch-runner -diff` `Q2 MATCH rows=455`
  **VERDICT: PASS**, with PG 18.3 agreeing tuple-for-tuple on the fixture.
  3 ledger rows. **Run 3 of the bar is unblocked.**
- [x] **P5.9-h** The clause 2/3 timing gap — **RE-SPECIFIED at run 3 (09 §3.5)
  as an ESTIMATE COLLAPSE, no longer a search for a bisect.** Run 3 measured
  (Q10 3.91×, Q9 3.13×, Q18 2.47×, Q7 2.07×, Q12 2.07×, total 1.362×) and the
  §4 parity ratchet ran on both arms for the first time: `parity_violations`
  0 (OFF) → 6 (ON), and every violation is a joinrel the PG-shaped search sizes
  at `rows=1` (Q9 316 264×, Q10 114 106× twice, Q12 31 354×, Q5 7 411×,
  Q7 5 869×) where the OFF arm is within 1.4–6.3× of actual. The five queries
  carrying violations ARE the clause-3 failures.
  **Reproducer: Q12** — two relations, no search-order confound. Flag ON, its
  outer input is `Index Scan using orders_pk on orders` with NO index condition
  (a full ordered scan the search adds for merge-join sortedness) carrying
  `rows=1`; flag OFF the same relation is a `Seq Scan` at `rows=1500000` and
  the join estimates 21 154 (actual 31 354).
  **HALF LANDED 2026-08-05 (09 §3.6) — the bisect answered NEITHER branch.**
  The search's own numbers were right: `addOneOrderedIndexPath` sets
  `Path.Rows = rel.Rows` and `makeJoinRel` sizes off that. The 1 was minted
  after the search, by `EstimateRows` (`cardinality.go`), which answered 1 for
  EVERY `*IndexScan`/`*IndexOnlyScan` on the equality-probe convention — wrong
  for the bound-less full scan P5.4c-ii-b introduced. One arm now returns
  `tableRows(Table)` when no `Key`/`Keys`/`LowKey`/`HighKey` is bound.
  Re-measured on the five carrying queries: `parity_violations` **6 → 0**,
  Q12's `orders` leaf `rows=1` → `rows=1500000`, its joinrel est 1 → 46 001
  (actual 31 354).
  **REMAINING, and it refutes §3.5's headline:** plan shapes and timings are
  byte-identical before and after (Q12 20.83 s → 20.21 s). The timing gap was
  NOT the estimate collapse. The five queries still plan a Merge Join over a
  full ordered index scan of `orders` where the OFF arm plans a Hash Join over
  a Seq Scan; whether reading 1.5 M rows through the PK index to save a sort is
  worth it is a COST question — `costIndexScan` at `selectivity = 1.0`
  (pathindexordered.go) vs `costSeqscan` plus the avoided sort — and is what is
  left of this item. `joinsearchlevel.go:324-330`'s `rows < 1` clamp is NOT
  implicated and needs no change.
  **CLOSED 2026-08-05 by P5.9-k (09 §3.9)** — and the named suspect was
  innocent: `costIndexScan` is roughly right, the defect was the missing
  external-merge term on the OTHER side of the comparison. ON/OFF on the five
  carrying queries 2.61× → **1.007×**.
  **Q18 is NOT in this class** and must not be bisected with it: its final
  joinrel is ~23 400× over in BOTH arms (OFF est=1 568 274, ON est=1 642 632,
  actual 70), so its 2.47× is a plan choice made on an equally bad estimate.
- [x] **P5.9-i** `assertSearchedTreeNeedsNoReconcile` fires on 7 TPC-DS
  queries under the flag. **DONE 2026-08-05 (09 §3.7) — the disagreement was
  the CHECKER's.** `reresolveJoinByName`'s `predRebind` resolves a predicate
  operand against the side its index suggests and falls back to the other side
  on a -1, but `resolveSide` returned -1 both for "the name is not here" (a
  miss, where crossing over is right) and for "the name is here twice" (an
  ambiguity, where crossing over is a guess). `SourceTableIdx` does not
  separate them across scopes: M0071-0009 added it for Q21's three `lineitem`
  aliases, three range-table entries of ONE scope, while Q83's three `item_id`s
  each descend from `item.i_item_id` inside a SEPARATE WITH arm and every arm
  numbers its own range table — so all three carry the same source identity.
  The correct side then answers -1 and the other side answers with its single
  match, rebinding a correctly-bound reference onto another relation's column
  of the same name: a predicate comparing a column to itself, i.e. a cross
  product, and a SILENT wrong answer on the untagged cost path since
  M0071-0009. Fix: `lookupColumnIndexByName` /
  `lookupColumnIndexByNameAndSource` (bushy.go) report the duplicate case
  separately and `predRebind` abstains on it; the miss fallback is untouched
  (`TestReresolveStillCrossesSidesOnAPlainMiss` pins it), and the two old
  helpers survive as wrappers so the forced-side rebind sites are unchanged.
  Measured (DS05 subset sweep, flag ON): `ERROR=7` → `PASS=6 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=1`, five of the six with PG-identical value
  checksums. **Q47 is the TIMEOUT and is a NEW defect, not this one's
  remainder** — correct 100 rows in 8 m 40 s against 11–13 s flag-OFF; filed
  as P5.9-j.
- [x] **P5.9-j** Q47 costs ~40× under the flag (NEW at P5.9-i, 09 §3.7).
  **DONE 2026-08-05 (09 §3.8) — one cost term charged on the wrong tuple
  count.** Not a search-order defect and not an estimate defect: the 1-row
  estimate for `{v1,v1_lag}` (four stats-less equalities over CTE scans ⇒ four
  `DEFAULT_EQ_SEL`s ⇒ 7 193² × 0.005⁴ → clamp 1) is what **PG estimates too**,
  verified against the oracle on the same data. Reduce the query and the
  threshold is on ARITY, not on columns: three join keys hash, four fall to a
  nested loop. At the top pair the hash costs 968.55 and the loop 968.53 — the
  loop wins by 0.02 because its outer is that 1-row rel, then rescans 7 193
  inner rows per actual outer row. `final_cost_nestloop` charges
  `cpu_per_tuple` on `ntuples = outer_path_rows * inner_path_rows`, commented
  in place as "number of tuples processed (not number emitted!)"; goopg splits
  that sum, the qual half already rode the cross product, and the
  `cpu_tuple_cost` half was landing on the join's OUTPUT rows — smallest
  exactly on the plans the term exists to deter. Fix: `nestloopCost`
  (`cost_funcs.go`) charges `cpu_tuple_cost * outerRows * innerRows` with PG's
  one-tuple clamp on each side, and `innerRows` is threaded to the three call
  sites (`addNestLoopPath` / `addNLIPaths` pass the inner PATH's own count, the
  legacy bushy NLI-delegation site passes the per-probe 1). The hash and merge
  siblings are untouched — PG charges those on `hashjointuples` /
  `mergejointuples`, which really are output counts. Measured: Q47 flag-ON
  8 m 40 s → **13 s**, ON subset `PASS=6 TIMEOUT=1` → **`PASS=7 TIMEOUT=0`**,
  OFF subset unchanged with identical checksums. Tests:
  `internal/planner/nestloop_ntuples_test.go` (5, incl. the clauseless-pair
  counterweight). NOT fixed and still owed: goopg's CTE scans publish no
  pathkeys, which is the real reason it cannot reach the free merge PG picks
  here (375.55, no sort) — P5.4c-ii's; and the 1-row collapse is PG-faithful
  but still 7 193× off actuals on both arms — §4.1's ratchet.
  Historical statement of the original 40× report: correct answer (100 rows,
  matching the oracle row count; the oracle carries `ck=n/a` because its LIMIT
  window saturates), 8 m 40 s timed alone on a freshly restarted SF0.5 server
  versus 11–13 s on the flag-OFF arm. ON/OFF plan pair:
  `bench/tpcds/runtime_goopg/tpcds-results-sf05/plans-20260805-222627.txt`
  (ON) against `plans-20260805-220059.txt` (OFF).
- [x] **P5.9-k** The clause 2/3 timing gap is a MISSING cost term, not a
  mispriced one (successor to P5.9-h's cost half, 09 §3.9). **DONE
  2026-08-05.** `costSortRun` implemented only `cost_sort`'s comparison term;
  its own comment justified the omission with "TPC-H sorts are small dimension
  outputs", which this phase invalidates — a merge join sorts a JOIN INPUT, and
  Q12's is 5 997 241 `lineitem` rows (~4.7 GB). The hash rival in the same
  `addPath` comparison HAS been charged its spill since P5.7-a, so one operator
  was billed 1 326 616 for spilling those bytes and the other 0: the asymmetry
  design 04 §1 forbids, as a whole missing term rather than a constant. Fix:
  `cost_tuplesort`'s disk branch reproduced term for term (`npages`, `nruns`,
  `log_runs` via `tuplesort_merge_order` with MINORDER 6 / MAXORDER 500,
  `2*npages*log_runs` accesses at ¾ seq + ¼ random), sized through
  `hashsize.EntryBytes` — the SAME byte model `spillPages` uses for the hash
  side — and `ncols == 0` suppresses the disk term exactly as a zero
  `innerCols` does in `hashJoinCost`. PG's `tuples < 2 ⇒ 2` clamp adopted,
  replacing a `return Cost{}`. `sortPathFor` threads `relNCols(sub.Rel)`.
  Measured (five carrying queries, one binary, both arms one session):
  Q7 26.71→**16.29**, Q9 54.95→**15.86**, Q10 22.93→**5.65**,
  Q12 20.79→**9.82**, Q18 74.71→**29.79**; ON/OFF **2.61× → 1.007×**; all
  digests unchanged; Q12 now Hash Join over two Seq Scans; §5 audit ON arm
  reduced to the OFF arm's single pre-existing Q18 violation;
  §4 `parity_violations=0`. Tests:
  `internal/planner/cost_sort_external_test.go` (6, incl. the one-currency
  invariant against `spillPages`). NOT fixed, filed: goopg's merge operator
  sorts BOTH inputs unconditionally (`newMergeSortedSource`), so
  `tryMergeJoinPath`'s `pathkeysContainedIn` sort-skip credit is a fiction at
  run time — the last one left in the merge arm's cost.
  Historical statement of the original defect follows. Q11, Q31, Q47, Q57, Q58,
  Q74, Q83 abort at plan time — `searchedtree.go:205`, reached from
  `createPlanAtSearchRootRange` (createplanroot.go:130) via
  `searchOneProblem` — each with a distinct layout disagreement:
  `ca_county 0→8`, `customer_id 0→12`, `customer_id 0→20`, `i_category 0→16`,
  `i_category 0→18`, `item_id 0→4`, `item_id 2→0`. The P5.5-f-ii-a cross-check
  is doing its job: it converts a wrong-column plan into a dead connection
  (the panic is recovered per-connection at server.go:801; the server stays
  up), which is why clause 4 shows `ERROR=7` and `MISMATCH=0`. All seven are
  TPC-DS's CTE/UNION-ALL family, where one base relation is scanned repeatedly
  under different aliases inside separate WITH arms — a shape TPC-H's 22
  queries never produce, which is why three acceptance runs on TPC-H alone
  never saw it. Reproduce: `GOOPG_PGSHAPED_DP=1 bench/tpcds/server.sh start
  sf05`, then `psql -p 65437 -f
  bench/tpcds/runtime_goopg/tpcds-data/queries/query47.sql`.
- [x] **P5.9-l** Clause 6 has no instrument — build the spine/pairing channel
  §4 names (NEW at P5.9 run 4, 09 §3.10). Run 4 passed clauses 1, 2, 3, 4 and 5
  with zero defects attributed to `GOOPG_PGSHAPED_DP`, and the flip is held by
  clause 6 alone. §4 specifies the check as "verified through the §4 parity
  gate's spine diff"; `cmd/estimate-audit` has **zero** occurrences of "bushy"
  or "spine". Its parity channel compares per-joinrel estimates and labels
  one-sided relsets `SHAPE (…-only joinrel)` — a relset says which base
  relations are underneath a node, never how they were PAIRED, and clause 6 is
  a pairing question. Measured directly for run 4: PG 18.3 chooses a bushy
  spine on exactly three of the 22 (Q7, Q8, Q20) and goopg on none, in either
  arm; PG's Q7 partition is `{customer+lineitem+n2+orders} ⋈ {n1+supplier}`
  against goopg's `{lineitem+n1+n2+orders+supplier} ⋈ customer`. Not a failure
  by itself — the clause admits cost/stats-driven divergence and hard-fails
  only on a shape the search cannot EXPRESS — but "enumerated and lost on cost"
  and "never enumerated" predict the identical observable, so the run cannot
  tell them apart. Phase 2 (`joinsearchlevel.go:171-222`) is `joinrels.c:141-198`
  term for term and `TestJoinSearchFourRelChainOffersBushyPair` /
  `TestJoinSearchBushyIsClauseOnly` / `TestJoinSearchPairCountMatchesClosedForm`
  prove the mechanism — **on a synthetic 4-relation chain**, not on these
  partitions. Build: a search-level channel that records, per query, the
  joinrel pairings the DP actually built (phase 1 vs phase 2 provenance, both
  sides' relsets), and a comparator that asks whether PG's chosen partition is
  among them; wire it into `scripts/tpch-estimate-audit-arm.sh` so the ratchet
  and the spine diff come from one arm. Bar: clause 6 answered by measurement
  on Q7, Q8 and Q20 — pass (divergence attributed to cost or stats, admitted
  under the ratchet) or a named gap in the bushy phase. Then P5.9 re-runs
  clause 6 alone and flips or attributes.
  **↳ SPLIT 2026-08-06. Both halves are DONE (P5.9-l-i, P5.9-l-ii below) and
  clause 6 was measured GREEN 2026-08-06 (09 §3.13), so this umbrella item is
  discharged by them.**
- [x] **P5.9-l-i** The spine/pairing channel, built and measured (09 §3.11).
  DONE 2026-08-06. `internal/estimateaudit/spine.go` computes, for every join
  node of a captured plan, the relsets of its immediate children, and classes
  the node bushy iff both children — after descending through the single-child
  pipeline nodes between them (`Hash`, `Materialize`, `Sort`, `Gather`,
  `Memoize`, aggregation; written as an arity rule, not a label whitelist) — are
  themselves joins. `SpineDiff`/`CountSpine`/`RenderSpine` join the two engines'
  chosen spines per pairing and name the clause-6 candidates. It renders from
  `cmd/estimate-audit` whenever a `--reference` is present, so
  `scripts/tpch-estimate-audit-arm.sh` needed **no change** — one arm, one
  artifact, the §4 ratchet and the spine diff together.
  **The measurement refuted run 4's manual reading.** Applied offline
  (`--from-plans`) to run 4's committed plans: the ON arm chooses a bushy spine
  on SIX of the 22 (Q2, Q7, Q8, Q9, Q10, Q20), not none — and on **Q20 it
  chooses PG's bushy partition exactly**, `{nation+supplier} ⋈
  {lineitem+part+partsupp}`, which is the first evidence that phase 2 builds and
  `add_path` keeps a bushy pair over a real five-relation TPC-H relset rather
  than a synthetic 4-rel chain. Every spine number moves toward PG under the
  flag: pairings matched 13 → 24, PG-only 44 → 33, goopg-only 45 → 32, bushy
  2 → 6. Two clause-6 candidates remain and only two — PG's bushy top on Q7
  (`{customer+lineitem+n2+orders} ⋈ {n1+supplier}`) and Q8
  (`{lineitem+orders+part} ⋈ {customer+n1+region}`). Evidence:
  `analysis/leftdeep-joins/2026-08-06-p59l-spine-{on,off}.txt` + `-README.md`.
- [x] **P5.9-l-ii** The SEARCH-side half: enumeration provenance (NEW
  2026-08-06, 09 §3.11). P5.9-l-i reads CHOSEN spines on both sides, so for Q7
  and Q8 "enumerated by the DP and lost on cost" and "never enumerated" still
  predict the identical observable. Record what `makeJoinRel` was actually
  offered: per join problem, every `(outer relset, inner relset, phase)` triple
  the enumerator produced, with the relid → relation-name map that makes it
  comparable to a plan's relset strings; export it on a channel an arm run can
  harvest (an env-gated trace into the server log is the cheap option — the
  audit tool already has the label to key it on). Then test membership of the
  two candidate partitions directly. Q20's matched bushy pairing is the
  positive control: whatever the channel says about Q7/Q8, it must show Q20's
  partition enumerated. Bar: clause 6 discharged — either both partitions were
  enumerated (divergence is cost/stats, admitted under the §4 ratchet, clause 6
  passes and P5.9 flips) or the bushy phase has a named gap on them (a new
  slice). Files: `internal/planner/joinsearchlevel.go`, `joinsearch.go`,
  `cmd/estimate-audit/`, `scripts/tpch-estimate-audit-arm.sh`.
  **↳ THE CHANNEL IS BUILT (2026-08-06, 09 §3.12); the item stays open on the
  MEASUREMENT.** Writer: `internal/planner/joinsearchtrace.go`, gated on
  `GOOPG_PGSHAPED_DP_TRACE=1` (nil `searchCtx.trace` when off, so production is
  untouched), emitting one whole-block `DPTRACE` write per join problem to
  stderr — the relid → name map, every offered `(outer, inner, phase)` triple
  with a `created` bit, and every pair the connectivity gate declined with its
  reason. Reader: `internal/estimateaudit/enumtrace.go` +
  `estimate-audit --enum-trace <server log>` (`DP_TRACE=1` in the arm script
  passes it), which derives the partitions to adjudicate from the P5.9-l-i
  spine diff and answers `OFFERED` / `DECLINED` / `SIDE-NOT-BUILT` /
  `NOT-ENUMERATED` / `NO-TRACE`. Two decisions are load-bearing: the pair key
  is `SpineJoin.PairKey`'s string byte for byte and names follow `leafRel`'s
  alias-first rule (else Q7's two `nation` scans collapse on one side only),
  and goopg's OWN bushy pairings are derived as controls — a control that is
  not `OFFERED` prints `VERDICT: HARNESS FAULT` and voids the run.
  Unit-tested on both sides (5 planner tests, 6 audit tests) and smoke-verified
  in a live server (`analysis/leftdeep-joins/2026-08-06-p59lii-dptrace-*`): a
  bushy chosen plan recorded at `phase=2` with `created=0`, alias `n1`
  preserved, an unconnected partition adjudicated `SIDE-NOT-BUILT`.
  **↳ MEASURED 2026-08-06 — CLAUSE 6 PASSES (09 §3.13).** `PLAN_ONLY=1
  DP_TRACE=1 PGSHAPED=1 scripts/tpch-estimate-audit-arm.sh
  2026-08-06-p59lii-enum-on --queries 7,8,20`: `enum_controls=2/2
  enum_controls_oos=1 enum_candidates_offered=2/2 enum_problems=3
  enum_malformed=0`. Both candidates were OFFERED at `phase=2` with
  `created=false` — the search can express both shapes and lost them on cost,
  which §4's ratchet admits. Evidence and an offline re-derivation recipe:
  `analysis/leftdeep-joins/2026-08-06-p59lii-enum-on*`.
  Two instrument changes the run forced. **`--plan-only`** (arm: `PLAN_ONLY=1`)
  runs plain `EXPLAIN` and omits §5 and the §4 parity column rather than
  printing them empty — a §5 table of `actual=? (no ANALYZE)` rows ends in a
  clean verdict, the one way the artifact could lie — which took the run from a
  power run to four minutes and, having no timing to protect or spoil, exempted
  it from the arm's nightly-batch refusal that had blocked this measurement for
  two loops. **`CROSS-QUERY-LEVEL`** classes a pairing whose sides were planned
  at different query levels: goopg's Q20 plan prints `{nation+supplier} ⋈
  {lineitem+part+partsupp}` across a SubPlan boundary, and scoring that as a
  control voided the first run with `HARNESS FAULT`. Out of scope as a control
  (counted, never silently dropped); a clause-6 FAILURE as a candidate, sharper
  than `NOT-ENUMERATED` — the shape is unreachable, not merely unchosen.
  A failing IN-SCOPE control still voids the run.
- [x] **P5.9-q** No test tied a gate's provenance label to the default it names,
  and the same defect had shipped twice (NEW at P5.9-n, 09 §3.15). **DONE
  2026-08-06 (09 §3.16).** `sf05_planner_flags_line` hand-wrote the label for an
  UNSET variable — a claim about a **Go default** living in a **bash printf** —
  so M0125-0005's `GOOPG_RELSIZE_FALLBACK` flip and M0127-P5.9's
  `GOOPG_PGSHAPED_DP` flip each left every later artefact stating the OPPOSITE of
  the regime it measured, the second one mis-stamping the acceptance run of the
  flip itself. The chain is now
  `internal/planner/flaglabels.go` → `cmd/gen-planner-flag-labels` →
  `scripts/planner-flags.env` (generated, checked in) →
  `scripts/planner-flags.sh: planner_flags_body`, sourced by BOTH the SF0.5 gate
  and `tpch-spotcheck.sh`. Every label is computed by the same resolver
  production uses at process start (`pgShapedDPFromEnv`,
  `parseRelSizeFallbackStage`, `memoizeFromEnv`, `unnestPreDPFromEnv`, … —
  several factored out of their `init()` here); nothing restates a default.
  Four guards (`flaglabels_test.go`), two verified by negative probe: the
  checked-in fragment must equal what the defaults render (the stated bar); each
  `unset(<tok>)` must round-trip through the flag's own parser, so the artefact
  is a runnable instruction; every `os.Getenv("GOOPG_*")` in the package must be
  stamped or exempt with a reason; and neither gate may hand-write `unset(`.
  The coverage guard's first finding: the stamp named **6** flags, the planner
  reads **12** — `GOOPG_EXISTS_TO_ANY`, `GOOPG_UNNEST_PREDP`,
  `GOOPG_INDEXKEY_HARVEST`, `GOOPG_NLI_COSTGATE`, `GOOPG_HASH_OUTER_JOIN`,
  `GOOPG_MHJ_PACKING_OFF` were named by no artefact goopg had ever captured, and
  `tpch-spotcheck.sh` named no enumerator flag at all. The six pre-existing
  labels are byte-identical before and after, so the capture corpus stays
  comparable; the line grows to the right. Ledgered: the registry covers
  `internal/planner` only, so executor kill-switches (`GOOPG_HASHED_SUBPLAN`)
  re-open the same hole one layer down.
- [x] **P5.9-o** EXPLAIN printed no `Join Filter:` line (NEW at P5.9, 09 §3.14).
  **DONE 2026-08-06 (09 §3.17).** `Hash Cond:` (P2.1) made the join's KEY
  visible and stopped one conjunct short: `ON jl.a = jr.a AND jl.v < jr.w`
  printed the second conjunct **nowhere**, on either arm, so the conjunct the
  executor re-checks per candidate match could not be read against PG's output
  for the same query. `formatJoinFilter`
  (`internal/executor/operators_explain.go`) emits it in upstream's slot —
  after `Hash Cond:`/`Merge Cond:`, before the node's own `Filter:`, the order
  `ExplainNode` uses for T_HashJoin / T_MergeJoin / T_NestLoop alike — and asks
  `ExecHashKeyPlan`/`ExecMergeKeyPlan` for the split, i.e. **the same methods
  the executor uses** to decide what it re-checks. `joinqual` is
  `list_difference(joinclauses, hashclauses)` upstream (`createplan.c`), which
  is the same subtraction; the property that follows is that every conjunct
  prints exactly ONCE, and a residual that printed but was not evaluated would
  be the same invisibility mirrored. Byte-verified against a throwaway
  PostgreSQL 18.3 cluster on four shapes (one conjunct; two, as
  `((jl.v < jr.w) AND (jl.b <> jr.b))`; all-equijoin two-key, where PG prints
  no line at all; merge join). Unpins `TestExplainQualifiesUpperFilter` back
  onto the DEFAULT enumerator — the pin existed because the only shape a search
  arm leaves at the join node is the cross-relation residual, and that line did
  not exist. Ledgered: ANALYZE's `Rows Removed by …` counters (goopg emits none
  anywhere), the structured formats (no qual properties at all), and
  `NestedLoopIndexJoin`'s mixed-provenance `Predicate`, which keeps `Filter:`
  deliberately.
- [x] **P5.9-c** The search boundary publishes a ROTATED coordinate map — the
  P5.9 blocker. DONE 2026-08-05. The producer was innocent: the layout,
  `boundaryMap` and `projectToBindingOrder` are all correct, and the rotation is
  applied AFTER the boundary by `remapTopProjection` (bushy.go), which locates
  the join tree to derive its posMap from by walking down past `*Project` /
  `*Sort` wrappers — and the boundary IS a `*Project`. It therefore built the
  map from a node inside the searched subtree (so `collect`'s guard never
  fired) and applied it to the boundary's own target list, composing two
  permutations. Fix: `isSearchedTree` guard on that descent — the eighth member
  of 08 §3's skip list, and the first that neither rewrites nor renumbers a join
  tree. The proposed `boundaryMap` strengthening was NOT done and is explicitly
  refuted in 09 §3.2: it is a producer-side check and the producer was right.
  The consumer-side invariant replaces it — `assertSearchedBoundariesIntact`
  (createplanroot.go) at the tail of `Plan()`: a boundary target names the very
  column it addresses, so a later permutation moves the indices and leaves the
  names behind. 09 §3.2; 03 §10 (2026-08-05 amendment); 08 §3 (amended).
  Bar met: UNITS + `internal/planner/joinsearchboundary_test.go` (fails without
  the fix) + SPOT (Q12 2 rows, Q13 35 rows).
- [x] **P5.9-d** Result-digest mode for `cmd/tpch-runner` + a two-arm diff.
  DONE 2026-08-05 (09 §3.3;
  `analysis/leftdeep-joins/2026-08-05-p59d-digest-selfdiff.txt`). `-digest`
  emits three digests per result set — `colsig` (column names), `ordered`
  (rows in scan order), `unordered` (the wrapping SUM of per-row hashes: a
  MULTISET digest, sum not XOR so a duplicated row cannot cancel itself) — and
  `-diff A.log B.log` compares two arms on them. Fields are length-prefixed,
  not delimited: a text column can contain any delimiter, so `("a","b")` and
  `("ab","")` would otherwise collide. `NO-DIGEST` and `BOTH-ERROR` are
  FAILING verdicts — a run without `-digest` must not read as "everything
  matched", which is exactly how run 1's five corrupt queries passed.
  `rows=N` deliberately stays the last token on the line: `tpch-spotcheck.sh`,
  `stage-tpch.sh` and `tpch-relsize-arm.sh` all extract it with an
  end-of-line-anchored regex, so digests go BEFORE it and `-digest` composes
  with the existing gates instead of disarming them
  (`TestOKLineKeepsRowsTerminal`). Also promoted `scripts/tpch-acceptance-arm.sh`
  out of `tmp/` — run 1's driver was untracked, so the protocol 09 §3.1
  documents could not be re-executed from a clean checkout.
  Bar met: UNITS + the OFF-arm self-diff **24/24 MATCH** (repeated across four
  server processes and two engine images; the tie-prone 10k-20k-row results
  Q3/Q10/Q16/Q15a matched on the ORDERED digest too, so a clean run yields no
  spurious `ORDER-DIFF`). Cost ~2 % of arm wall time. Ledger row: the diff
  compares two goopg arms, never the PG oracle.
- [x] **P5.9-e** Q17 never hung — a Gather swallowed its error. DONE 2026-08-05
  (bar met by its second clause, an attributed finding; 09 §5.20;
  `analysis/leftdeep-joins/2026-08-05-p59e-q17-hang.txt`). Profiled per the
  item's own instruction instead of re-reading EXPLAIN: 19 goroutines, ZERO
  workers, RSS flat, 0.8 % CPU, the statement parked in `gatherOp.Close`'s
  drain — a park, not the spin a degenerate hash join would show, so the
  single-key-degeneracy hypothesis is REFUTED. `gatherOp.Open` started the
  goroutine that closes `o.ch` LAST, so its three error returns left a live
  channel with no closer and `Close` drained forever, delivering neither rows
  nor the error. Fix: `startChannelCloser` (idempotent, after the last
  `group.Go`) invoked on every path out of `Open`;
  `TestGatherCloseTerminatesAfterOpenError` fails all three arms without it.
  `gatherMergeOp` is structurally immune (per-worker `defer close`) and
  unchanged. With the error surfaced, flag-ON Q17 **errors at 28.73 s** —
  `column ref l_quantity/30 out of VirtualSlot range 27` (expr.go:366) — while
  the flag-OFF control on the same engine takes 33.17 s to succeed, so there is
  no Q17 timing regression at all and the "157×" figure is withdrawn. The
  residue is a correctness defect, filed as P5.9-f.
- [x] **P5.9-f** The decorrelated aggregate is a foreign coordinate scope, and
  the splice's join key relied on a repair pass. DONE 2026-08-05 — **two
  independent defects**, the second uncovered by fixing the first.
  (1) `buildBindingsPosMap`'s `collect` DESCENDED into a join-input
  `*Aggregate` while its twin `applyJoinTreePosMap` has always STOPPED at one;
  with the flag on, the searched outer side records no entries, so the
  decorrelated `HashAggregate`'s lineitem CLONE became the first and only
  `lineitem` entry — at offset 25 — and the residual's `l_quantity/4` was
  remapped to `l_quantity/29` against a 27-wide slot. Fix: opaque leaf
  (`off += len(x.Output())`), the third instance of "build and apply must stop
  at the same nodes" (`*Project` M0125-0012, `*SetOp`/`*WindowAgg` RC-2); the
  descent was also advancing `off` by the CHILD's width, `*WindowAgg`'s
  original defect. (2) With the remap correctly declining,
  `reresolveJoinByName` stopped running and flag-ON Q17 returned **0 rows** vs
  the control's 5: `unnestSubquery` built the splice's `RightKey` with the
  inner-relative index `0` while its `Predicate`/`LeftKey` used merged
  coordinates, so the executor's merged-slot key eval hashed
  `part.p_partkey` against `lineitem.l_orderkey`. Latent since the splice was
  written, masked because the name-rebind repaired it on every path that
  reached it. Fix: build it at `outerWidth`. Reproducer is a 3000-row fixture
  (~1 s), not the 28 s SF1 arm. Regression tests:
  `TestQ17DecorrelatedAggregateCoordinates` (both flag settings),
  `TestBuildBindingsPosMapStopsAtJoinInputAggregate`.
  [09](09-verification-and-acceptance.md) §5.21;
  `analysis/leftdeep-joins/p59f/`. Gate MET: one binary, both arms,
  `tpch-runner -diff` → `Q17 MATCH rows=1`, **VERDICT: PASS** (33.46 s ON /
  32.98 s OFF). Also UNITS, SPOT, and DS05 (PASS=95, MISMATCH=0, plan shapes
  99/99 identical) — both fixes change flag-OFF planning for every
  correlated-aggregate decorrelation. **P5.9's full bar re-run is unblocked.**

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
