# 0127 — PG-shaped join search: Implementation Plan (leftdeep-joins bundle Ralph task breakdown)

| field | value |
| --- | --- |
| status | draft — **DESIGN ONLY**, implementation not started |
| date | 2026-08-03 |
| milestone | `docs/milestones/0127-pg-shaped-join-search.md` |
| design of record | `docs/design/leftdeep-joins/` — **This document is not the design authority.** Each task's instructions come solely from the bundle chapters (`README.md`, `01`–`09`, `IMPLEMENTATION-TODO.md`); this plan references them as "see XX §N for details." Files under the bundle directory are referenced only, never modified |
| convention | Tasks are sized for one Ralph loop (one session) completion (`.ralph/PROMPT.md` "ONE task per loop"). Each task lists its gate (completion condition). Deferral = ledger row + unchecked box; never a silent close |
| decomposition source | The `IMPLEMENTATION-TODO.md` P0–P6 structure, split to finer granularity (P5 = 9 tasks + P5.3a bushy phase, PS6 split into 2 tasks, total 34 tasks) |

## 1. Positioning

M0126 terminated as a documented no-go on 2026-08-03. This milestone converts
the `docs/design/leftdeep-joins/` bundle (user-directed 2026-08-02, amended
2026-08-03) into shipped behaviour. The bundle's stop conditions are binding
(same as M0126). The stage map (S0–S7), flags, and rollback contract are
normative at [08-migration-and-removal.md](leftdeep-joins/08-migration-and-removal.md)
§2. The acceptance bar is normative at
[09-verification-and-acceptance.md](leftdeep-joins/09-verification-and-acceptance.md)
§3. This plan is an index into both.

**Ordering principle (08 §1):** executor first, planner second, deletion last.
P0–P4 (executor stages) immediately improve the current default planner's output
and carry no planner risk. By the time P5 (DP) lands, every binary cascade it
emits runs on an already-fixed executor.

## 2. Common gate vocabulary for all tasks (09 §1, binding)

- **UNITS**: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green.
- **SMOKE**: pre-commit pgbench smoke on every commit (hook; never `--no-verify`).
- **SPOT**: `scripts/tpch-spotcheck.sh` (Q12/Q13 canonical row counts, fresh
  capped server) — every planner/executor/codec commit.
- **DS05**: `scripts/tpcds-sf05-regression.sh sweep` — **zero** row-count deltas
  and **zero** checksum deltas (against git-tracked oracle). This is the gate
  that caught fusion; it is the primary correctness instrument for E1 (seam)
  and S3 (spill). Run per stage, not just at the tail.
- **PLAN**: plan-diff (`make plan-diff LABEL=…`; verify the label against the
  current re-baseline — as of 2026-08-03 the `post-mhj-retire` lineage is
  current).
- **REGRESS**: full regress-port suite (after E1, E4, S3, S4 — M0106
  six-silent-regressions precedent).
- **RACE**: `make race-gate` (stages touching shared state — E3's build changes
  interact with `parallel_hash_build.go`; S3's temp-file registry has Close
  racing under cancellation).
- **SIBLING**: sibling-path audit explicitly enumerated in code review — E4
  (planner keys ↔ executor key encode), 06 §2.1 (planner nbatch ↔ executor
  nbatch), E5 (compiled ↔ interpreted evaluators).
- **BENCH**: seam microbench (3-level cascade, 1M synthetic probe rows —
  steady-state 0 allocs) and other 09 §7 fixtures. A tripwire, not a CI gate.

Timed measurements only on a **quiet host** (check `pgrep -af run-nightly.sh`
first), server age held constant (sweep-tail discipline). Never pass `-count=1`
to gate `go test` invocations.

## 3. Task decomposition

### P0 — Executor pure wins [S0] (3 tasks, 1 loop each)

Unconditional (no flag), pure wins. S0 exit = units + spotcheck + pgbench smoke +
stage0-style A/B with no query worse (08 §2).

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **P0.1** | Hoist `mergedKeySlot` construction to `Open` (shape-invariant per join); rebind `.row` per pull. Zero steady-state allocs in the seam microbench | IMPLEMENTATION-TODO P0.1; 05 §3 (E2) | `internal/executor/operators_join_agg.go` (:986-1014, build-side :590/:646/:702, probe-side :1266/:1269) | UNITS + SPOT + BENCH |
| **P0.2** | Single-pass build: fold `drainRowsBounded`'s budget into the build loop of `buildLazyHashTable`; delete the re-iteration (`rowsOp`-per-row `MaterializedSlot` allocs). Keep owned-copy discipline (M0097-0058) | IMPLEMENTATION-TODO P0.2; 05 §4 (E3) | `internal/executor/operators_join_agg.go` | UNITS + SPOT + RACE (shared-build interaction) |
| **P0.3** | Single-map build: planner threads key-type info on `planner.Join`; executor picks int64 map vs string map before build. Extend int64 path to Semi/Anti (CTID exception preserved). Delete `lazyHashFinalize`'s dual-map dance | IMPLEMENTATION-TODO P0.3; 05 §4 (E3) | `internal/planner/` (key-type info) + `internal/executor/operators_join_agg.go` | UNITS + DS05 |

### P1 — The seam [S1] (3 tasks, 1 loop each)

S1 is behind `GOOPG_JOIN_SLOT_CHAIN` (default ON, env kill-switch OFF only).
Exit = full regress-port + TPC-H SF1 sweep + DS05; Q3/Q10/Q18/Q7 ≤ 1.2× R0
(8.46 / 6.04 / 27.58 / 25.13 s).

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **P1.1** | Legacy-path slot chaining (un-defer M0126-0004): probe child slot as `lazyVirtualOut` source; rebind-on-pointer-change + copy-on-type-change fallback. Delete `slotRow(probeSlot)` (:1254) and vestigial `lazyKeyRow`. Env kill-switch `GOOPG_JOIN_SLOT_CHAIN=off` | IMPLEMENTATION-TODO P1.1; 05 §2 (E1, F7 contract: child does not return stable slots) | `internal/executor/operators_join_agg.go` | REGRESS full + DS05 + SPOT + BENCH (seam 0 alloc) |
| **P1.2** | Worker-path exercise: integration test of the P1.1 seam under `BuildWorker` (`inWorker=true`) — fusion's decline-in-worker precedent warns this path diverges silently | IMPLEMENTATION-TODO P1.2 | `internal/executor/` (worker test) | RACE |
| **P1.3** | S1 A/B evidence run: Q3/Q10/Q18/Q7 ≤ 1.2× R0, remaining queries ≤ 1.2× pre-S1 HEAD. Artefact `analysis/leftdeep-joins/<date>-s1-ab.txt`. Do not start P2 until bar met or attributed (09 §6) | IMPLEMENTATION-TODO P1.3; 09 §2/§6 | Artefact file only (no code) | Timed TPC-H SF1 A/B + SPOT per arm |

### P2 — Multi-column keys [S2] (3 tasks, 1–2 loops each; P2.1/P2.2 are a sibling pair)

S2 is plan-affecting → plan-snapshot re-baseline **same commit**.
`reselectDegenerateHashKeys` is deleted at P2.2 (the single-equi-pair degeneracy
workaround introduced by M0125-0035b is replaced by true multi-column keys).

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **P2.1** | `planner.Join.HashKeys []JoinKeyPair`: search/pushdown fills all equality conjuncts; residual keeps non-equijoin only. EXPLAIN key-list rendering. Plan-snapshot re-baseline same commit | IMPLEMENTATION-TODO P2.1; 05 §5 (E4 planner half); 02 §2 (BuildLeft = commutation) | `internal/planner/` (Join struct, search/pushdown, EXPLAIN) | UNITS + SPOT + DS05 + PLAN (snapshot diff reviewed) |
| **P2.2** | Executor composite keys: all-int64 fixed-width pack; mixed → concatenated `datumKey`. Delete `reselectDegenerateHashKeys` + its planner pass same commit. Add Q78-class degeneracy regression test (constant-pinned first key column must not degrade to one bucket) | IMPLEMENTATION-TODO P2.2; 05 §5 (E4 executor half); memory: `goopg_hash_join_single_key_degeneracy` | `internal/executor/operators_join_agg.go` + `internal/planner/` (deletion) | UNITS + SPOT + DS05 + SIBLING (planner keys ↔ executor encode) |
| **P2.3** | Merge-join multi-column keys from the same list (full-key comparator; residual only non-equijoin) | IMPLEMENTATION-TODO P2.3; 07 §2 | `internal/executor/` (merge join) + `internal/planner/` | UNITS + SPOT + DS05 + PLAN |

### P3 — Hybrid hash spill [S3] (5 tasks, 1–2 loops each)

S3 is `work_mem`-honouring ON, `GOOPG_HASH_SPILL=off` escape. Exit = Q21 SF1
completes (cgroup cap) + forced-spill byte-identical (09 §2).

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **P3.1** | `chooseHashTableSize` (shared pkg importable by both planner and executor); goopg-width-aware (`48·c` + map overhead) | IMPLEMENTATION-TODO P3.1; 06 §2.1; 04 §4 | New shared pkg (referenced from planner + executor) | UNITS + SPOT |
| **P3.2** | Batch build/probe: hashvalue-prefixed `spillWriter` frames, per-batch inner/outer files, `HJ_NEED_NEW_BATCH` state in `nextLazy`, nbatch growth + capped give-up + WARNING | IMPLEMENTATION-TODO P3.2; 06 §2.2-2.4 | `internal/executor/operators_join_agg.go` + spill substrate | UNITS + DS05 + RACE |
| **P3.3** | Per-query temp-file registry on `Context`; relocate to `<datadir>/base/pgsql_tmp/`; startup sweep; fix `spillOp.Close` unlink leak. Injected-crash test leaves no strays | IMPLEMENTATION-TODO P3.3; 06 §3 | `internal/executor/` (spill registry) + `internal/server/` | UNITS + crash-injection test |
| **P3.4** | Semi/Anti/LEFT per-batch semantics (batch-global `antiBuildHasNull`); shared build declines when nbatch > 1 | IMPLEMENTATION-TODO P3.4; 06 §2.5 | `internal/executor/operators_join_agg.go` | UNITS + DS05 + RACE |
| **P3.5** | EXPLAIN `Batches:`/memory lines; forced-spill identity test (low `work_mem` Q3 byte-identical to default). Artefact `analysis/leftdeep-joins/…-s3-spill.txt` | IMPLEMENTATION-TODO P3.5; 06 §4; 09 §2 | EXPLAIN + test + artefact | Q21 SF1 completes (capped) + DS05 zero deltas + RACE |

### P4 — Other join operators [S4] (4 tasks, 1–2 loops each)

S4 is per-operator; plan-affecting parts follow S5's flag. Exit = regress-port
outer-join files green + DS05.

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **P4.1** | Streaming merge join (duplicate-group buffering + overflow file); delete full-drain `runMergeJoin`/`buildMergeSide` accumulation | IMPLEMENTATION-TODO P4.1; 07 §2 | `internal/executor/` (merge join) | UNITS + REGRESS + DS05 |
| **P4.2** | Hash outer-fill: per-batch matched bitmap; RIGHT sweep; FULL = LEFT fill + sweep; planner legality matrix update (RIGHT/FULL hash paths). Regress-port outer-join files green | IMPLEMENTATION-TODO P4.2; 07 §3 (PG `HJ_FILL_INNER`) | `internal/executor/operators_join_agg.go` + `internal/planner/` | REGRESS outer-join files + DS05 |
| **P4.3** | `Materialize` operator (plan node + path + rescan replay, memory→spill); NL join streams outer, inner under Materialize. Delete drain-both `runNestedLoop` buffering and `concatRows`-per-pair | IMPLEMENTATION-TODO P4.3; 07 §4 | `internal/executor/` (materialize, nested loop) + `internal/planner/` | UNITS + SPOT + DS05 |
| **P4.4** | Lateral: outer streams (per-outer re-execution kept), output no longer accumulates into `o.rows` | IMPLEMENTATION-TODO P4.4; 07 §4 | `internal/executor/` (nested loop) | UNITS + DS05 |

### P5 — The DP [S5] (9 tasks + P5.3a, 1–2 loops each)

Each task lands dark behind `GOOPG_PGSHAPED_DP` (OFF while soaking).
Collapse-limit wiring gets its own sub-flag `GOOPG_PGSHAPED_COLLAPSE` (P5.8).
Coexistence rules during soak are 08 §3: searched roots are tagged, legacy
passes skip tagged subtrees, `reconcileNLILayout` asserts no-op on searched trees.

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **P5.1** | `joinrels` level lists + relset map over `RelOptInfo`; `buildInitialRels` with `PathPrebuilt` leaves (subquery/CTE/VALUES/pinned unnest — closes the leaf-whitelist gap. Also the successor to M0125-0037(ii)) | IMPLEMENTATION-TODO P5.1; 03 §1-§2 | `internal/planner/` (new DP substrate) | UNITS + PLAN (default arm ZERO diff) |
| **P5.2** | restrictInfo list + `hasRelevantJoinClause`; equivalence-class selectivity rule (inferred edges: admissible, no double-count) | IMPLEMENTATION-TODO P5.2; 03 §3; 04 §5 | `internal/planner/` | UNITS + PLAN (ZERO diff) |
| **P5.3** | `joinSearchOneLevel` phases 1+3 (clause joins against initial rels; disconnected cartesian; last-ditch); `makeJoinRel` with PG's outer/inner printing convention | IMPLEMENTATION-TODO P5.3; 03 §4.1-§4.2 (`joinrels.c:118`, `:200-256`) | `internal/planner/` | UNITS + SPOT + PLAN |
| **P5.3a** | Phase 2 — bushy joins, PG-verbatim (03 §4.3, `joinrels.c:141-198`): k-loop to halfway, clauseless-rel skip (:170-172), mirror-half `first_rel` rule (:174-177), `have_relevant_joinclause` pair gate (:190-191). Pair-count verification (03 §7 arithmetic, after connectivity filter) | IMPLEMENTATION-TODO P5.3a; 03 §4.3 | `internal/planner/` | UNITS + pair-count verification test |
| **P5.4** | `addPathsToJoinrel`: hash (both build sides), NLI+Memoize parameterised paths, merge (via pathkeys), NL fallback (jointype-legal only, 03 §5.3; FULL-without-usable-clause error contract), qual placement at lowest covering level, deterministic tie-break. Parameterisation discipline (03 §9: param-aware `setCheapest`, `PATH_PARAM_BY_REL` refusal, `ppiRows`). NLI binding contract (03 §5.2: shared eligibility fn; constructor failure on DP-chosen path = loud planner error) | IMPLEMENTATION-TODO P5.4; 03 §5 | `internal/planner/` (path generation) | UNITS + SPOT + DS05 |
| **P5.5** | `createPlan` arms for all live PathKinds → existing Nodes; **search-boundary coordinate map** (03 §10: canonical relid-order layout — one map composed from the final relset, or a relid-reordering root Project; ColumnRef-in-schema plan-time assertion); pinned-spine re-resolution consumes the map; searched-subtree tagging so legacy passes skip; `reconcileNLILayout` no-op assertion | IMPLEMENTATION-TODO P5.5; 03 §10; 02 §3 | `internal/planner/` (create_plan equivalent) + `internal/executor/` | UNITS + SPOT + DS05 + PLAN (snapshot re-baseline same commit) |
| **P5.6** | `calcJoinrelSize` + FK-superkey generalisation + eqjoinsel + FK clamp (04 §3.1-3.3); delete quadratic build penalty; estimate audit tooling (09 §5 — Q9 chain must show final joinrel ≤ 10²× actual) | IMPLEMENTATION-TODO P5.6; 04 §3; 09 §5 | `internal/planner/cardinality.go` lineage | UNITS + DS05 + estimate audit run |
| **P5.7** | nbatch-aware `hashJoinCost` (shared sizing fn); Startup/Total split for LIMIT-over-join | IMPLEMENTATION-TODO P5.7; 04 §4; 06 §5 | `internal/planner/cost_funcs.go` | UNITS + PLAN (default arm ZERO diff) |
| **P5.8** | Wire collapse limits with PG's actual semantics (03 §6: flat comma lists are always ONE problem; limits govern sub-joinlists and explicit JOINs only; =1 pin semantics); explicit INNER JOIN flattening behind its own sub-flag `GOOPG_PGSHAPED_COLLAPSE` (soaked separately from the enumerator, 08 §2); outer joins stay pinned until `join_is_legal` inference lands (03 §4.4). Delete the 12-table bail-out | IMPLEMENTATION-TODO P5.8; 03 §6 | `internal/planner/` (collapse) + `joinorder.go` (prepare for sequencer demotion) | UNITS + DS05 (sub-flag OFF/ON both arms) |
| **P5.9** | S5 acceptance run: full 09 §3 bar (collapse OFF → ON, two passes) + plan-shape ratchet baseline (§4) + estimate audit (§5); flag flip or documented no-go. Artefact `analysis/leftdeep-joins/…-s5-acceptance.txt` | IMPLEMENTATION-TODO P5.9; 09 §3-§5; 08 §2 | Artefact + flag-flip commit | Full acceptance bar |

### PS6 — Compiled key/residual evaluation [S6] (2 tasks, 1 loop each)

Behaviour-neutral, no flag. Sibling-path audit (compiled ↔ interpreted) is the
release gate (09 §1).

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **PS6.1** | Compile `HashKeys[i]` accessors and the residual conjunction to `ExprNode` at `Open` (`internal/executor/exprnode.go`); `ExprAdapter` fallback for unsupported kinds | IMPLEMENTATION-TODO PS6.1 (first half); 05 §6 (E5) | `internal/executor/exprnode.go` + `operators_join_agg.go` | UNITS + BENCH (no alloc regression) |
| **PS6.2** | compiled ↔ interpreted sibling audit + parity spot-diff (expression corpora, including overflow corpus — 0097-0037 precedent) | IMPLEMENTATION-TODO PS6.1 (second half); 09 §1 SIBLING | Tests + audit artefact | parity corpus + BENCH |

### P6 — Deletion [S7] (4 tasks, 1 loop each)

Only after S5 default-ON survives ≥ 1 clean nightly cycle. The deletion
inventory is normative at 08 §4 (re-acquired via grep at S7 time).
`buildBindingsPosMap`/`applyJoinTreePosMap` are **held back** until the 03 §10
boundary map is proven in production (08 §4, the single most regression-prone
change in S7).

| ID | Content | Reference | Files | Gate |
|---|---|---|---|---|
| **P6.1** | Delete fusion: `fused_hash_join.go` (707 lines), hook (`executor.go:160-163`), env vars, planner-side orphan-export check (verify `IsCanonicalKeyEquality` has no other callers) | IMPLEMENTATION-TODO P6.1; 08 §4 "Fusion" | Delete `internal/executor/fused_hash_join.go` + hook/env | grep-clean + UNITS + SPOT |
| **P6.2** | Delete MultiHashJoin (fresh grep inventory at S7 time; ~34 arms/18 files as of 2026-08-02): node, packer (`rewriteMultiWayChain`/`collectMultiHashTables`), `mhj_input_rewrite.go`, posmaps, cost/cardinality arms, executor op (`multi_hash_join.go` 696 lines), EXPLAIN arms, `generateMultiHashJoinPath`, flags (`mhjPackingEnabled`/`GOOPG_MHJ_PACKING_OFF`) | IMPLEMENTATION-TODO P6.2; 08 §4 "MultiHashJoin" | 15+ files as above | after nightly green + grep-clean + UNITS + SPOT + DS05 |
| **P6.3** | Delete old subset-bitmask DP + related family: `enumerateBushyPlans`/`enumerateSubsets`/`enumerateSplits`/`dp map[uint16]dpEntry`, `estimateJoinCost` + integer weights, `attachUnusedCrossEdges`, `bushySeedRowCounts`, `len(tables) > 12` cap, `IsSmallDimensionSide` pinning, `chooseInnerJoinAlgo` (searched); delete subset-internal layout/remap family (`dpEntry.layout`, `remapKeyToLayout`, `mergeSubsetLayouts`); demote `joinorder.go` to over-limit sequencer. **Hold back `buildBindingsPosMap`/`applyJoinTreePosMap`** | IMPLEMENTATION-TODO P6.3; 08 §4 "Planner" / "layout/remap family" | `internal/planner/bushy.go` et al. | grep-clean + UNITS + SPOT + DS05 |
| **P6.4** | Supersession stamps (0034-0001, 0038-0001, cost-model/09 §3 allowance, 0043/0063/0125/0126 MHJ chapters); README index status flips; ledger rows for each deliberately-skipped PG behaviour (GEQO, skew buckets, SpecialJoinInfo in-DP — `join_is_legal`-inference-dependent marker —, shared spilling builds, full join_order_restriction inference) | IMPLEMENTATION-TODO P6.4; 08 §5 | Docs + `.ralph/deferral_ledger.md` | Doc review |

## 4. Dependency and ordering notes

- **P1.3's A/B artefact is a prerequisite for starting P2** (S1 exit bar, 09 §2).
- **P2.1/P2.2 are a sibling pair** (planner keys ↔ executor key encode), one commit.
- **Each P5 task fundamentally shows zero plan diff in the default arm** (proof
  of inertness behind flag OFF). P5.5 does a snapshot re-baseline same commit.
- **P5.8 follows P5.3** (collapse changes *which statements enter the search*;
  coupling it to the enumerator swap would make S5 regressions unattributable —
  08 §2).
- **P6 only after S5 default-ON survives ≥ 1 clean nightly cycle** (08 §2 S7).
- **Concurrent remaining M0125 items**: exprwalk commits 5–8 (M0125-0002) must
  complete first as the substrate for P5.5's searched-subtree tagging.
  M0125-0047 (deterministic tie-break) must close first as it shares design
  with P5.4's deterministic tie-break. M0125-0040 (ROLLUP) is an independent
  track outside bundle scope.

## 5. Evidence conventions

- All artefacts committed under `analysis/leftdeep-joins/` (09 §2 naming
  convention): `<date>-s1-ab.txt`, `<date>-s3-spill.txt`,
  `<date>-s5-acceptance.txt`, estimate audit (09 §5), parity gate mismatch
  records (ratchet).
- Timed measurements on quiet host, server age held constant, symmetric timeouts.
- Each stage's no-go/attribution follows the 09 §6 taxonomy ((a) cardinality /
  (b) plan shape / (c) cost-model realism / (d) executor); constant changes
  are not admitted without class diagnosis.
