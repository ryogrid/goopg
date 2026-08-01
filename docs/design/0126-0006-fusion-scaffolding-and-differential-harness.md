# 0126-0006 — fusion scaffolding, decision function, and the differential harness (switch OFF)

| field | value |
| --- | --- |
| status | **LANDED** |
| date | 2026-08-02 |
| task | M0126-0006 — **CONDITIONAL on M0126-0005** ("a large gap remains") |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` 04 (site + data structures), 05 (predicate Q0–Q9), 10 (KS1/KS2) |
| depends on | `0126-0003` (shipped `evalHashKeyDatumSlot` — hard prerequisite), `0126-0005` (the trigger) |

## 1. What landed

Three pieces:

1. **`buildEnv` plumbing** — `buildWithEnv(plan, inWorker)` extracted from `Build`;
   `Build(plan)` wraps `buildWithEnv(plan, false)`; `BuildWorker(plan)` wraps
   `buildWithEnv(plan, true)` for Gather/GatherMerge worker closures. The
   `buildEnv` carries root, `inWorker`, `fusionCfg`, and memoised Q0.

2. **`fused_hash_join.go`** — `tryFuseHashCascade` (Q0–Q6 predicate, fail-closed)
   + `fusedHashJoinOp` with full `Open()` (hash-table build) and `Next()`
   (odometer). Called as the first statement of the `*planner.Join` arm in BOTH
   `Build` and `buildRec`.

3. **Kill switches** — `GOOPG_RUNTIME_JOIN_FUSION` (env, default OFF) and
   `GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS=3`. A session GUC is unreachable at
   `Build` (no session, no `*Context`; bundle 04 §1.1).

## 2. Files

| file | role |
|---|---|
| `internal/executor/fused_hash_join.go` | `tryFuseHashCascade`, `fusedHashJoinOp`, `buildEnv`, helpers (~420 loc) |
| `internal/executor/executor.go` | `buildWithEnv` extraction, `BuildWorker`, Gather/GatherMerge closures |
| `internal/executor/fused_hash_join_test.go` | 25 unit tests |
| `internal/planner/pushdown.go` | Exported `SplitAnd` wrapper |
| `internal/planner/bushy.go` | Exported `IsCanonicalKeyEquality` wrapper |

## 3. buildEnv threading

```
Build(plan)          → buildWithEnv(plan, false)   // legacy + leader
BuildWorker(plan)    → buildWithEnv(plan, true)    // Gather/GatherMerge workers
buildWithEnv         → create env, call buildDispatch (the switch)
```

`buildEnv` fields:

- `root planner.Node` — plan root for Q0 walk
- `inWorker bool` — set true by `BuildWorker`; gates fusion in parallel workers (C10/F4)
- `fusionCfg` — resolved once from env vars
- `q0` — memoised root-walk result (LockRows/Gather/MHJ)

## 4. Qualification predicate (Q0–Q6, fail-closed)

| check | what | rationale |
|---|---|---|
| Q0 | Root has no LockRows (C9), no MHJ, no Gather (C10/F4 via inWorker), not instrumented (C11/C12) | Global preconditions, memoised once per Build |
| Q1 | Chain depth ≥ `MIN_LEVELS` (default 3) | Amortise fusion overhead |
| Q2 | Per-level: INNER, Hash, !Lateral, !NullAware, !BuildLeft, no USING | Only the plain hash-cascade shape |
| Q3 | Both keys are `*ColumnRef` with indices in the bound prefix | Simple key shape |
| Q4 | Every residual conjunct's `ColumnRef`s are in the bound prefix | Predicate evaluable against fused output |
| Q6 | Width identity + element-wise `SchemaColumn` identity (F1) | Guards against stale schema permutations |

### Chain collection (fixed from initial scaffolding)

The initial scaffolding traversed top-down with `runningWidth=0`, which broke
on the first level. Fix: collect candidates top-down → determine `probeWidth`
from the innermost join's `Left.Output()` → reverse → validate bottom-up with
`runningWidth = probeWidth + Σ widths[0..i-1]`.

## 5. fusedHashJoinOp

```
fusedHashJoinOp
├── levels[0..k-1]          // innermost → outermost
│   ├── plan / probeKey / buildKey / width / offset / residual
│   ├── buildOp             // built Right subtree
│   ├── ht / intHT / htIsInt // hash table (populated in Open)
│   ├── slot                // *MaterializedSlot (rebound per match)
│   └── matches / cursor    // odometer state
├── probeOp / probeMatSlot   // probe subtree + rebound slot
└── out                     // VirtualSlot: sources = [probe, build[0], …, build[k-1]]
```

### Open()

Build order innermost-first (levels[0]…levels[k-1]):

1. Open `buildOp` → `drainRowsBounded` (respecting `WorkMem`) → close
2. Drain into `ht`/`intHT` with int64 fast-path (`datumToInt64Key`)
3. Cancel check every 4096 rows (C7)
4. Build `VirtualSlot`: source 0 = probe, source 1+i = build_i
5. Open `probeOp`

### Next() — the odometer

Depth-first walk over the match space:

```
loop:
  if !active: pull probe row → bind probeMatSlot → lookup level 0 ht → active=true, curLevel=0
  if curLevel < 0: active=false; continue            // probe row exhausted
  if cursor[curLevel] exhausted: curLevel--; continue // back off
  bind slot[curLevel].row = match; if residual fails → continue
  if curLevel == k-1: emit out                        // outermost
  curLevel++ → compute probe key from out → lookup next ht
```

Cancel check every 4096 odometer steps (C7).

### Reused helpers (no copies)

| concern | symbol | location |
|---|---|---|
| Key evaluation | `evalExprSlot` | `expr.go:353` |
| Key datum → string/int64 | `datumKey` / `datumToInt64Key` | `operators_join_agg.go` |
| Null-padded key slot | `mergedKeySlot` | `operators_join_agg.go` |
| Bounded drain + spill | `drainRowsBounded` | `spill.go:342` |
| Slot composition | `NewVirtualSlot` / `SlotFromRow` | `slot.go` |

## 6. Kill switches (bundle 10)

| switch | default | effect |
|---|---|---|
| `GOOPG_RUNTIME_JOIN_FUSION` | OFF | `tryFuseHashCascade` returns false immediately |
| `GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS` | 3 | Minimum cascade depth |

## 7. Tests (25 unit tests)

- **Predicate decline**: switch off, inWorker, LockRows, MHJ, instrumented, nil plan, Q0 not run
- **Q0 walk**: LockRows, Gather, MHJ direct + recursive through Filter
- **Expression walkers**: visit count, early stop, BinaryOp/FuncCall children
- **Bound checks**: OuterColumnRef, SubqueryExpr, ExistsExpr all decline
- **Config**: defaults, "1"/"true"/"bad" values, custom minLevels
- **Structural**: Operator interface, field count guards
- **VirtualSlot**: column coordinate mapping
- **Env**: lifecycle setup/restore

Integration tests (`TestFusedCascadeMatchesUnfused`, `TestFusedCascadeRescan`,
`TestBothBuildersAgree`, `TestExplainInvariantUnderFusion`) deferred to M0126-0007
(require a running server + switch ON).

## 8. Gates (switch OFF — verified)

- [x] UNITS PASS (all packages)
- [x] SPOT PASS (Q12=2, Q13=35)
- [ ] SMOKE (pgbench) — commit hook
- [ ] DIFF + DS05 + PLAN — deferred to M0126-0007

## 9. Remaining for M0126-0007

- Integration tests (MatchesUnfused, Rescan, BothBuildersAgree)
- F4 assertion (`collectShareableJoins` never coexists with fusion)
- Decline-reason counters (R10)
