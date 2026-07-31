# 0126-0006 — fusion scaffolding, decision function, and the differential harness (switch OFF)

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-31 |
| task | M0126-0006 — **CONDITIONAL on M0126-0005** ("a large gap remains") |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage 1, **03** (contract C1–C15), **04** (site + data structures), **05** (predicate Q0–Q9), **10** KS1/KS2 — read them first; this doc does not restate them |
| depends on | `0126-0003` (which ships `evalHashKeyDatumSlot` — a hard prerequisite), `0126-0005` (the trigger) |

## 1. Scope

Build the runtime-fusion machinery with the kill switch **off**, so production
behaviour is bit-identical by construction. Three pieces: (1) `buildEnv`
plumbing through `Build`/`buildRec` — plan root, `inWorker` (set by
`newGatherOp`'s closure, `executor.go:213-219`), the under-instrumentation flag
(set by `explainOp.Open`, `operators_explain.go:57-64`), resolved switch state,
memoised Q0 — with `Build(plan)` retained as a wrapper; **this is the largest
single piece, it touches every arm of two large switches — budget it
explicitly**; (2) `internal/executor/fused_hash_join.go` — `tryFuseHashCascade`
(bundle 05 Q0–Q9, fail-closed) and `fusedHashJoinOp` (bundle 04 §5-7, C15
re-entrant `Open`), called as the **first statement of the `*planner.Join` arm
in BOTH builders**; (3) KS1 `GOOPG_RUNTIME_JOIN_FUSION` (env, default OFF) and
KS2 `GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS=3` — a session GUC is unreachable at
`Build` (no session, no `*Context`; bundle 04 §1.1 / 10 KS1).

## 2. Files and symbols touched

| file | symbol | change |
|---|---|---|
| `internal/executor/executor.go:21` (`Build`), `:424` (`buildRec`), `:535-547` (Join arm) | both builders | thread `*buildEnv`; call `tryFuseHashCascade` first in both Join arms |
| `internal/executor/fused_hash_join.go` | new | predicate + operator |
| `internal/executor/parallel_hash_build.go:119-150` | `collectShareableJoins` | a `fusedHashJoinOp` case **or** an assertion that fusion and shared builds never coexist (F4) |
| decline-reason counters | new, behind a debug env var | R10 — a design that never fires must be visible |

## 3. Tests (all in this task's commits)

| test | asserts |
|---|---|
| `TestJoinStructFieldCountGuard` | Q7 struct-drift guard |
| `TestFusionKeyCoordinateSpace` | Q3 merged-space check, 3-level cascade |
| `TestFusionPrefixBoundedness` | Q5 on every fused plan |
| `TestExplainInvariantUnderFusion` | EXPLAIN text identical fused/unfused |
| `TestFusedCascadeMatchesUnfused` (**DIFF**) | ordered output byte-for-byte, whole join corpus |
| `TestFusedSchemaElementWiseIdentity` | Q6 clause 3 — width alone must NOT be the gate (F1) |
| `TestFusedCascadeRescan` | C15 — correlated SubPlan forces `rescanCloseOpen` (`subplan.go:223-230`) |
| `TestBothBuildersAgree` | R5 — same root operator kind via both builders |
| `TestFusionDeclinesOnLockRows/OnGather/OnOuterJoin/OnLateral/OnNullAware` | Q0/Q2 fail-closed paths |

## 4. Gates

UNITS, SMOKE, SPOT, PLAN, DS05 — **all bit-identical to the pre-task run**
(switch off ⇒ no-op in production by construction). DIFF present and green.

## 5. Stop / decision conditions

Conditional on -0005. **Not-triggered close:** if -0005's decision skips the
fusion band, close this task with a `.ralph/deferral_ledger.md` row citing the
-0005 decision line in `evidence/stage0-ab.txt` — never a silent skip. Stop:
any deviation from the pre-task run with the switch off is a defect in the
plumbing, not the operator — fix before proceeding.

## 6. Rollback

Bundle 10 §1: nothing to revert — the switch is off; blast radius zero.

## 7. What this doc deliberately does not decide

Whether fusion is ever enabled (that is -0007's measurement), and the semantic
contract itself (bundle 03 is normative; the tests above are its enforcement).
