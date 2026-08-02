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
- [ ] **P3.4** Semi/Anti/LEFT per-batch semantics (batch-global
  `antiBuildHasNull`); shared-build declines when nbatch > 1.
- [ ] **P3.5** EXPLAIN `Batches:`/memory lines; forced-spill identity test
  (low `work_mem` Q3 byte-identical to default). Gate for P3.*: Q21 SF1
  completes capped; SF0.5 zero-delta; race-gate.

## P4 — Other join operators [S4]

- [ ] **P4.1** Streaming merge join (group buffering + overflow file);
  delete full-drain `runMergeJoin`/`buildMergeSide` accumulation.
- [ ] **P4.2** Hash outer-fill: matched bitmap per batch; RIGHT sweep;
  FULL = LEFT fill + sweep; planner legality matrix update (RIGHT/FULL
  hash paths). Regress-port outer-join files green.
- [ ] **P4.3** `Materialize` operator (plan node + path + rescan replay,
  memory→spill); NL join streams outer, inner under Materialize; delete
  drain-both `runNestedLoop` buffering and `concatRows`-per-pair.
- [ ] **P4.4** Lateral: outer streams (per-outer re-execution stays), output
  no longer accumulates into `o.rows`.

## P5 — The DP [S5] (each task lands dark behind `GOOPG_LEFTDEEP_DP`)

- [ ] **P5.1** `joinrels` level lists + relset map over `RelOptInfo`;
  `buildInitialRels` incl. `PathPrebuilt` leaves for subquery/CTE/VALUES/
  pinned unnest rels (closes the leaf-whitelist gap).
- [ ] **P5.2** restrictInfo list + `hasRelevantJoinClause`; equivalence-class
  selectivity rule (inferred edges: admissible, no double-count).
- [ ] **P5.3** `joinSearchOneLevel` phases 1+3 (clause joins; disconnected
  cartesian; last-ditch); `makeJoinRel` with composite-left convention.
- [ ] **P5.4** `addPathsToJoinrel`: hash (both build sides), NLI+Memoize
  parameterised paths, merge via pathkeys, NL fallback (jointype-legal only,
  03 §5.3; FULL-without-usable-clause error contract), qual placement at
  lowest covering level; deterministic tie-break. Parameterisation
  discipline (03 §9): param-aware `setCheapest`, `PATH_PARAM_BY_REL`
  refusal for hash/merge inputs, `ppiRows`. NLI binding contract (03 §5.2):
  shared eligibility fn; constructor failure on a DP-chosen path = loud
  planner error.
- [ ] **P5.5** `createPlan` arms for all live PathKinds → existing Nodes;
  **search-boundary coordinate map** (03 §10: single π prefix-sum map or
  identity-restoring root Project; ColumnRef-in-schema plan-time
  assertion); pinned-spine re-resolution consumes the map;
  searched-subtree tagging so legacy passes skip; `reconcileNLILayout`
  no-op assertion on searched trees.
- [ ] **P5.6** `calcJoinrelSize` + FK-superkey generalisation + eqjoinsel +
  FK clamp ([04](04-cost-and-cardinality.md) §3.1-3.3); delete quadratic
  build penalty; estimate audit tooling
  ([09](09-verification-and-acceptance.md) §5).
- [ ] **P5.7** nbatch-aware `hashJoinCost` (shared sizing fn); Startup/Total
  split for LIMIT-over-join.
- [ ] **P5.8** Collapse limits wired with PG's actual semantics (03 §6:
  flat comma lists are always ONE problem; limits govern sub-joinlists and
  explicit JOINs only; =1 pin semantics); explicit INNER JOIN flattening
  behind its own sub-flag `GOOPG_LEFTDEEP_COLLAPSE` (soaked separately from
  the enumerator, 08 §2); outer joins stay pinned (03 §4.4). Delete the
  12-table bail-out.
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
- [ ] **P6.3** Delete bushy DP + layout/remap family + integer cost +
  `IsSmallDimensionSide` pinning + `chooseInnerJoinAlgo` (searched);
  demote `joinorder.go` to over-limit sequencer.
- [ ] **P6.4** Supersession stamps (0034-0001, 0038-0001, cost-model/09 §3
  allowance, 0125/0126 MHJ chapters); README index status flips; ledger
  rows for every deliberately-skipped PG behaviour (bushy phase, GEQO, skew
  buckets, SpecialJoinInfo in-DP, shared spilling builds, full
  join_order_restriction inference).

## Standing deferral pointers (file rows when their stage lands)

bushy phase (03 §4.3) · GEQO (03 §7) · skew buckets (06 §6) · parallel/shared
spilling builds (06 §6) · hash-agg spill (06 §6) ·
semi/anti in-DP (07 §5 — **bushy-phase-dependent**) ·
outer-join `join_order_restriction` inference (03 §6 —
**bushy-phase-dependent**) · tuplestore mark/restore merge join (07 §2) ·
Materialize under merge inner (07 §4) · session cost-GUC threading (04 §1,
pre-existing C3.2/C4 TODO) · slab `Aggregate` migration (05 §2, independent
track) · `buildBindingsPosMap`/`applyJoinTreePosMap` deletion (08 §4 — held
until the 03 §10 boundary map is proven)
