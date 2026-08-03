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

- [ ] **P5.1** `joinrels` level lists + relset map over `RelOptInfo`;
  `buildInitialRels` incl. `PathPrebuilt` leaves for subquery/CTE/VALUES/
  pinned unnest rels (closes the leaf-whitelist gap).
- [ ] **P5.2** restrictInfo list + `hasRelevantJoinClause`; equivalence-class
  selectivity rule (inferred edges: admissible, no double-count).
- [ ] **P5.3** `joinSearchOneLevel` phases 1+3 (clause joins against initial
  rels; disconnected cartesian; last-ditch); `makeJoinRel` with PG's
  outer/inner printing convention (03 §4.4).
- [ ] **P5.3a** Phase 2 — bushy joins, PG-verbatim (03 §4.3,
  `joinrels.c:141-198`): k-loop to the halfway point, clause-less rel skip
  (`:170-172`), mirror-half `first_rel` rule (`:174-177`),
  `have_relevant_joinclause` pair gate (`:190-191`). Pair-count
  verification against 03 §7's arithmetic (connectivity-filtered).
- [ ] **P5.4** `addPathsToJoinrel`: hash (both build sides), NLI+Memoize
  parameterised paths, merge via pathkeys, NL fallback (jointype-legal only,
  03 §5.3; FULL-without-usable-clause error contract), qual placement at
  lowest covering level; deterministic tie-break. Parameterisation
  discipline (03 §9): param-aware `setCheapest`, `PATH_PARAM_BY_REL`
  refusal for hash/merge inputs, `ppiRows`. NLI binding contract (03 §5.2):
  shared eligibility fn; constructor failure on a DP-chosen path = loud
  planner error.
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
