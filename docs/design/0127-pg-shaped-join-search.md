# 0127 — PG-shaped join search: Implementation Plan (leftdeep-joins bundle Ralph task breakdown)

| field | value |
| --- | --- |
| status | **in progress** — implementation started 2026-08-03; P0.1 landed (see §6). The bundle chapters remain design-only; this row tracks the plan's execution |
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

## 6. Progress log

One row per landed task. Records what shipped and the evidence, so a later
loop does not re-derive it; the bundle files stay untouched (they are the
design, not the tracker).

| task | date | landed | evidence |
|---|---|---|---|
| **P1.2** | 2026-08-03 | The worker-path exercise for the P1.1 seam (`join_worker_path_test.go`). Three claims, none implying another: (1) the seam **engages** under `BuildWorker` — asserted structurally (`lazyChainProbe`, and `lazyProbeSrc` not being the join's own copy slot), because a declined seam returns byte-identical rows and would therefore be invisible to any result-based test; that is exactly how fusion's `env.inWorker` decline (C10/F4) can be live in every serial test and dead in every parallel one. (2) A chained emit's rows survive **retention across later pulls** through `MaterializeForTransfer` + `AssertTransferable`. This is the property the serial pipeline never tests: a serial consumer formats each row as it arrives, whereas `gatherOp.runWorker` accumulates 256 rows before sending, so a probe source the next pull overwrites corrupts every row but the last. (3) Real-Gather identity over the P8 corpus × both seam arms × leader-participation on/off × {2,4} workers — the leader-off arm forces EVERY row across the goroutine boundary. **What the exercise found is in the BUILD path, not the seam:** `buildWithEnv` kept its `buildEnv` in a package global (`buildEnvInFlight`, M0126-0006) that every Gather worker writes from its own goroutine while the leader builds its own child tree — the data race that had made `make race-gate` red since M0126-0006 landed. The M-NIGHTLY triage had predicted a large refactor ("every recursive Build site participates"); the global turned out to be a local in disguise, because the only read in `buildWithEnv` is the `*planner.Join` arm, which runs before any recursive `Build` could overwrite it. It is now an actual local. The second reader, `buildRec`'s Join arm, became the explicit nil field `opTreeSlab.env` — `BuildFast` is a top-level entry with no `buildWithEnv` frame above it, so the global it read there was always nil: **fusion has never been reachable from the simple-query slab path**, only from the extended-protocol `executor.Build` path. The seam itself did not diverge in a worker. | **RACE: `make race-gate` EXIT=0 across all packages** — first green since M0126-0006; the same executor tests were red immediately before this change, with every frame in `buildWithEnv` (`internal/executor` alone went from 12+ "race detected" failures to `ok … 10.4 s`). All three new tests were verified to bite: `GOOPG_JOIN_SLOT_CHAIN=off` fails claim (1) at the "seam is OFF in a worker-built join" assertion; a stubbed shallow `MaterializeForTransfer` fails (2) with `got "NULL\|NULL\|NULL"` and (3) with `got "2\|d-2", want "200\|d-0"` — `VirtualSlot.Row()` hands back a **pooled** row, so the corruption those two guard against is mechanical, not hypothetical. UNITS PASS. SPOT PASS (Q12 rows=2 / Q13 rows=35, 17.8 s query phase, peak 11,597 MB). SMOKE via the commit hook. Ledger row `2026-08-03 M0127-P1.2`: one global was removed, the executor package's build/exec-time globals were **not** audited as a class — PG cannot have this defect at all (worker = process), so goopg carries a hazard with no upstream analogue and no systematic check. |
| **P1.1** | 2026-08-03 | The probe seam no longer materialises. `nextLazy` used to open every probe row with `r := slotRow(probeSlot)`; for a `*VirtualSlot` child — which is what every interior seam of a left-deep chain hands back under the 02 contract — that is `VirtualSlot.Row()`: a pooled `acquireRow` plus a width-wide 48-byte-Datum copy, and the pooled row was **never released**. `bindProbe` now binds the child's own `TupleSlot` straight into `lazyVirtualOut.sources[lazyProbeSrcIdx]`, and the Semi/Anti emit composes through a new `lazyOuterOnlyOut` VirtualSlot instead of filling `lazyOuterOnlySlot`. `slotRow(probeSlot)` is gone from both sites, along with the `lazyRow` field it fed and the vestigial `lazyKeyRow`. Kill switch `GOOPG_JOIN_SLOT_CHAIN=off` (`joinSlotChainOn`, read once per `ensureLazyVirtual`, same idiom as `GOOPG_HASHED_SUBPLAN`). **F7 turned out to need no type detection.** The bundle's mitigation is "rebind on pointer change, fall back to a copy when the concrete type changes"; rebinding *unconditionally on every pull* is correct for any slot the child returns, so there is nothing left to detect — the copy fallback survives only for the kill switch and for a slot that cannot serve the composed shape. Of those two, exactly one changes an observable result: on the Semi/Anti emit the probe slot **is** the whole tuple, so an over-wide probe child keeps its pre-P1.1 emitted width via the copy instead of being silently narrowed to `len(o.schema)`. That divergence is recorded, not fixed (ledger `2026-08-03 M0127-P1.1`) — PG cannot produce the shape at all, because `ExecHashJoin` returns `ExecProject(ps_ProjInfo)` and a hash join's tuple therefore always carries the node's own result descriptor. Aliasing the child's storage is safe **by control flow only** (a probe row is pulled after every match of the previous one has drained); nothing in the type system enforces it, so `bindProbe` asserts `!lazyActive` rather than trusting it, per 02 §2.4's honesty note. | Seam: `BenchmarkProbeSeam/chained` **0 B, 0 allocs/op**, 58.7 µs per 1024-probe-row pass, vs `/off` (the pre-P1.1 seam, kept runnable by the kill switch) **172,179 B, 2,048 allocs/op**, 221.3 µs — two allocations per probe row removed, 3.8× on the seam. `TestProbeSeamZeroAllocs` pins the 0 with `AllocsPerRun`; both arms run on the P0.3 int64 key lane so the string lane's per-row `datumKey` does not sit on top of the measurement. Correctness guards in `join_slot_chain_test.go` run **every** case over four probe-child slot shapes — reused buffer, the child's own shared `*VirtualSlot`, a fresh slot per call, and one that **rotates shape per pull** (F7 itself): 0126-0004 §3's mandatory fan-out test (one probe row, three matches — a seam that let the probe source drift shows up as duplicated values, not as a row count), Semi and Anti, both LEFT null-pad exits (hash-level miss and predicate-level miss, which take different code paths to the same composition), the over-wide copy fallback, the kill-switch arm asserting the copy path is still reachable and produces identical output, and the rebind assertion. **REGRESS: full `TestPort_RegressSuite` PASS, 659.4 s** — the gate 09 §2 names for E1 (the M0106 six-silent-regressions precedent). UNITS PASS. SPOT PASS (Q12 rows=2 in 12.51 s / Q13 rows=35 in 4.03 s; query phase 17.8 s, peak 11,594 MB). **DS05: MISMATCH=0 / CKMISMATCH=0 / ERROR=0 across all 99** — Q1-Q72 (67 PASS, 3 SKIP, 2 TIMEOUT; `analysis/m0127-p11/ds05-sweep.log`) plus Q73-Q99 (26 PASS, 1 SKIP, 0 TIMEOUT; `analysis/m0127-p11/ds05-sweep-73-99.log`). The first run died after Q72 on the **same** post-TIMEOUT restart hazard P0.3 hit: the transient `goopg-tpcds-sf05.scope` is still loaded when the restart re-creates it, `systemd-run` fails and readiness times out at 180 s — not a code failure, and the documented recovery (re-run the tail as a stamped SUBSET PROBE, `QUERIES="$(seq 73 99)"`) is what the second log is. The two halves together are the coverage, not one stamped gate result. The two TIMEOUTs are Q47 and Q72 — the known boundary pair, 263-312 s against the 300 s cap, flipping either way across runs at unchanged code (ledger `M0125-0013` attributes Q47's runtime to the single-key hash join, not to any seam). Cleaned up before the tail run: an orphaned `TestPort_RegressSuite` goopg server (ppid=1, 395 MB, data dir already removed by `t.TempDir`, so `goopg stop` could not reach it) — the leak class recorded in `goopg_orphaned_test_servers_leak_ram`. SMOKE via the commit hook. |
| **P0.3** | 2026-08-03 | The build allocates ONE map. `planner.Join.HashKeysAreInt64` (new `internal/planner/join_hashkey.go`) types both key expressions — `exprType` first, falling back to the merged (Left ++ Right) key column space for a ColumnRef whose `Type` was left zero — and `buildLazyHashTable` sets `lazyHashIsInt` from it before the first row arrives. `lazyHashInsertDatum` inserts into that map only; `lazyHashFinalize` and `lazyBuildAllInt64` are deleted. The dual-map dance cost every int-keyed join (i.e. nearly every join in both benchmarks) a full second copy of its build side, held simultaneously with the first — the string map was freed only at finalize, which is *after* peak memory had been reached. **Semi/Anti now reach the int64 lane**: the old `JoinTypeInner` gate existed only because semi/anti never ran the finalize step that committed the choice, and with the choice made up front there is nothing to opt out of (their NullAware counters and emit-once logic never read the key representation). **The CTID build is the one exception** and is pinned to the string map, because `lazyHashCTID` is a `map[string]` keyed in lockstep with it. The type decision is conservative — `numeric` is excluded even though `datumToInt64Key` accepts scale-0 numerics, because for numeric it is the values, not the type, that decide representability — and `demoteIntHash` is the safety net under it: a promise the datums break re-keys the int table into the string table mid-build rather than dropping a key it cannot represent (exact, because `datumKey(KindInt(v))` *is* `canonicalNumericKey(v, 0)`). | `join_hashkey_test.go` plans real SQL over a real catalog and asserts every hash join in a 3-way integer join reports true — including mixed widths (int8=int4, int4=int2), which a "both sides same type" check would reject. This is the coverage of record for a **performance cliff**: if the answer silently flips to false, every TPC-H join drops back to a canonical key string per probe row (the M0043-0003 cost) and no row-count or plan-shape gate would notice. Text and numeric keys assert false; guards cover non-hash algos, missing keys and an untyped ColumnRef with no children. `join_single_map_build_test.go` asserts the *other* map is never allocated (a "rows are findable" test passes with both maps populated), covers the Semi and Anti lanes, and drives `demoteIntHash` directly: rows inserted before and after the demotion must land under the same canonical string key, with their own payloads. UNITS PASS. DS05: **MISMATCH=0, CKMISMATCH=0, ERROR=0 across all 99** — Q1-Q72 in `sweep-20260803-114208.txt` (66 PASS) and Q73-Q99 in `sweep-20260803-122614.txt` (26 PASS). The Q1-Q72 run aborted after Q72 on a transient `systemd-run` scope-name collision (the previous scope had not been released when the post-timeout restart re-created it → 180 s readiness timeout), not a code failure; the remainder was re-run as a subset probe, so the two halves together are the coverage, not one stamped gate result. Q47/Q72 TIMEOUT: both are the known boundary pair, historically 263-308 s against the 300 s cap and flipping either way across runs at unchanged code (Q72 alone reads 273/263/308/480 s/TIMEOUT across five prior sweeps); not timed as a controlled A/B here. |
| **P0.2** | 2026-08-03 | The build is a single pass. `buildLazyHashTable` no longer calls `drainRowsBounded` and then re-iterates the drained operator; the two build loops moved into `joinOp.buildLoopLeft` / `buildLoopRight`, which pull straight from the child, key the row off the hoisted merged-key slot and insert it. What the re-iteration cost per build row: one `MaterializedSlot` (`rowsOp.Next` → `asSlot`) plus a full second traversal of the build side, and — when the drain spilled — a temp-file write + read + decode. The owned-copy the drain used to perform moved into the loop as `ownedBuildRow` (`rowHasArena` → `cloneRowOwned`, else the O(width) struct copy), and it now runs only for rows that survive the NULL-key check. **The `ctx.WorkMem` budget went with the drain, deliberately:** it bounded only the intermediate `[]Row`, never the hash table it fed — every spilled row was read straight back in and inserted, so peak memory was the finished table either way. Real work_mem enforcement is the batched hybrid-hash spill (06 / P3.2), which partitions at insert time and therefore *requires* this shape; the gap is ledger row `2026-08-03 M0127-P0.2`. | New guards in `join_single_pass_build_test.go` drive both loops over a child that hands out ONE reused buffer (the M0097-0058 / M0073-0004 aliasing class the drain used to absorb) — INNER (int64-eligible) and SEMI (string-map) lanes plus the BuildLeft orientation, and NullAware bookkeeping (`antiBuildRows` counts the NULL-key row, `antiBuildHasNull` set, key not inserted). Verified the guards bite: with `ownedBuildRow` stubbed to return the row unchanged, all three report `payload "three", want "one"`. UNITS PASS; SPOT PASS (Q12 rows=2 in 13.22 s / Q13 rows=35 in 4.38 s, **18.4 s** query phase vs P0.1's 32.3 s, peak 10,332 MB vs 10,767 MB — one uncontrolled run each, directionally consistent with dropping a pass); SMOKE via the commit hook. RACE: `make race-gate` is red at HEAD for an unrelated **pre-existing** reason — the `buildEnvInFlight` global (`executor.go:35-41`, M0126-0006), filed under M-NIGHTLY; reproduced identically in a clean-HEAD worktree, and every race frame in the log is `buildWithEnv` — zero frames in the new build loops. |
| **P0.1** | 2026-08-03 | `mergedKeySlotCache` (`operators_join_agg.go`) holds one hoisted merged-key `VirtualSlot` per side (`joinOp.lazyBuildKeySlot` / `lazyProbeKeySlot`); the five build/probe call sites call `rebind` instead of `mergedKeySlot`. Rebind swaps one interface word in `slot.sources`; it rebuilds only when `(realWidth, nullWidth, realOnLeft)` changes, which the child schemas fix at `Open` — the build loops' `width == 0 && len(row) > 0` empty-schema fallback is the only mid-loop shape change and can fire once. `mergedKeySlot` itself is kept as the constructor (and as the microbench's uncached arm). | Seam microbench `BenchmarkMergedKeySlotSeam` **4.10 ns/op, 0 B, 0 allocs/op** vs `…Uncached` **185.8 ns/op, 344 B, 5 allocs/op** (45×; the five allocations are the null `Row`, its `MaterializedSlot`, the `[]virtualCol`, the sources slice and the `VirtualSlot`). Guards in `join_merged_key_slot_test.go`: cached-vs-fresh slot equality both orientations, source rebinding (a stale source would key every row off the first row), shape-change rebuild, and a 0-alloc `AllocsPerRun` assertion. UNITS PASS; SPOT PASS (Q12 rows=2 / Q13 rows=35, 32.3 s query phase); SMOKE via the commit hook. **Not in scope:** `fused_hash_join.go`'s two `mergedKeySlot` call sites (:186, :280) stay per-row — fusion is deleted at P6.1 (05 §3 says so explicitly); `buildHashRightWithCTID`'s per-row `SlotFromRow` is a pre-existing alloc on the FOR-UPDATE-only path, untouched. |
