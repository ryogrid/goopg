# Take 7 results — expression compilation, and the parallel-only bug the review caught

**Status:** implemented and measured
**Date:** 2026-08-30
**Branch:** `perf-opt-take4`
**Baseline:** `bbe39dd4c` (take 6)
**Design:** [design-take7.md](design-take7.md) (agent-reviewed; §7 there)

---

## 1. Summary

| | take 6 | **take 7** | change |
|---|---:|---:|---:|
| Q6 serial | 4.563 s | **3.792 s** | **1.20×** |
| Q6 parallel | 1.009 s | **0.838 s** | **1.20×** |
| instructions / row | 12,432 | **11,192** | 1.11× |
| allocations / query | 798,094 | 798,196 | **flat** (as designed) |

Result bit-identical (`102513054.4896`); `tpch-spotcheck` PASS (Q12 = 2,
Q13 = 34); `-race` clean; unit gate 43/43 packages.

Against PostgreSQL, Q6 parallel is now **4.1×** (0.838 s vs 0.203 s), from
25.9× at the start of this series.

**No new evaluator was written.** goopg already had a compiled expression
representation (`exprnode.go`, M0107-0003 / M0127-PS6.x): a flat `[]ExprNode`
slab dispatched by integer kind-switch, with `ExprAdapter` delegating every
unsupported kind back to `evalExprSlot`. This round gave it constant folding
and pointed the hot path at it. Building a second evaluator — closures,
bytecode, JIT — would have duplicated semantics that took a 619-diff parity
corpus to get right, which is the maintainability regression the goal excluded.

---

## 2. What changed

| # | change | file |
|---|---|---|
| 1 | `buildExprCtx(e, ctx)` — `buildExpr` plus **constant folding**. `ctx == nil` is byte-identical to before, so the eight existing callers are unaffected. | `internal/executor/exprnode.go` |
| 2 | `ExprConstVal` node kind + `foldConstant`, which declines to fold on error **and** on session-dependence | `internal/executor/exprnode.go` |
| 3 | `seqScanOp` compiles its prefilter predicate in `Open` (where a `*Context` exists) and `evalPrefilter` runs `evalFastExpr` | `internal/executor/operators_storage.go`, `scan_prefilter.go` |

On Q6 this folds 7 constant nodes into 5 pre-computed `Datum`s — most
importantly `'1994-01-01'::date + '1 year'::interval`, which goopg was
evaluating **six million times per query** (`addTimeInterval`, 4.48 % of all
CPU). PostgreSQL folds the same expression in `eval_const_expressions`.

---

## 3. Measurements

### 3.1 Wall clock — alternating A/B, fresh server per arm

| round | mode | take 6 | take 7 | speedup |
|---|---|---:|---:|---:|
| 1 | serial | 4.845 s | **3.839 / 3.868 s** | 1.26× |
| 2 | serial | 4.536 / 4.563 s | **3.777 / 3.792 s** | 1.20× |
| 1 | parallel | 1.016 s | **0.875 / 0.861 s** | 1.17× |
| 2 | parallel | 1.010 / 1.009 s | **0.832 / 0.844 s** | 1.20× |

Ranges disjoint in every round and mode. Measured **1.20×** against the
design's predicted 1.15–1.20× — at the top of the band, which is the first time
in this series an estimate has landed inside its own range rather than under or
over it.

### 3.2 Instructions per row — both arms back-to-back

| | take 6 | take 7 |
|---|---:|---:|
| per-query serial (settled) | 6.183 / 6.162 / 6.055 s | 5.857 / 5.835 / 5.849 s |
| `instructions:u` (60 s) | 729,884,292,975 | 689,149,590,690 |
| rows scanned in window | 58.71 M | 61.58 M |
| **instructions / row** | **12,432** | **11,192** |
| IPC | 2.50 | 2.52 |
| CPUs utilised | 1.765 | 1.684 |

Instructions/row falls **1.11×** against a wall-clock 1.20×. As in take 6 the
gap is IPC (2.50 → 2.52) plus less background GC thread time (1.765 → 1.684
CPUs); these `perf` runs are also profiler-attached and slower in absolute
terms than §3.1's, so §3.1 is the wall-clock number and §3.2 is the ratio.

### 3.3 Allocations

798,094 → 798,196 per query — **unchanged**, exactly as
[design §4](design-take7.md) predicted. Folding removes non-allocating work
(`parseNumericFast*` returns scalars, `NewTimeDatum` does not allocate). This
was booked as a regression check, not a target, and it passes as one.

---

## 4. The bug the review caught

The design review found a **parallel-only wrong-answer bug in the
implementation as designed**, and it is the most valuable thing this round
produced.

`gatherOp` Opens the leader's child with the session `Context`, but each worker
with a `NewWorkerContext` whose `GetSetting` is **deliberately nil**. So
`timeZoneFromCtx` returns the session TimeZone in the leader and `""` in every
worker. Folding a session-sensitive constant at `Open` would therefore freeze
**two different values** into two plans running the same query.

I verified it is real rather than theoretical. A zone-less `TIMESTAMPTZ`
literal, folded under `TimeZone=Asia/Tokyo` vs a worker Context:

```
sessInt = 1788058800000000000   (2026-08-30 03:00 UTC)
blindInt = 1788091200000000000  (2026-08-30 12:00 UTC)   ← nine hours apart
```

The fix reuses the rule the codebase already applies to its own literal cache
("when `usedSession` fired … it must not be written to `x.CachedTime`") but
makes it self-verifying instead of type-specific: **evaluate the constant twice
— once with the caller's Context, once with a settings-blind copy, which is
exactly what a worker sees — and fold only if they agree.** Nothing needs to
know which types read the session.

**And the first version of that fix was itself wrong.** It compared
`Datum.Format()`, which renders a KindTime as *wall-clock* text — so the two
instants above print identically under some settings, and the check would have
declared them equal and folded the wrong constant. The comparison is now on the
raw Datum representation. `TestFoldingDeclinesSessionDependentValues` pins the
whole thing and fails if a session-dependent literal is ever folded.

---

## 5. Tests

| test | what it pins |
|---|---|
| `TestCompiledFoldedExprMatchesInterpreter` | 12 predicate shapes × 5 rows; compiled+folded must equal `evalExprSlot` on value, NULL-ness and error text. Includes the non-boolean-left-of-AND shape behind the PS6.2 corpus's 619 diffs, and an all-NULL row. |
| `TestFoldingDeclinesSessionDependentValues` | §4. Fails if a zone-less TIMESTAMPTZ folds. |
| `TestFoldingDeclinesOnError` | A failing constant (`1/0`) must not fold, so the error stays at row time. |
| `TestFoldingActuallyFolds` | Positive control: asserts an `ExprConstVal` actually appears, that `ctx == nil` folds nothing, and that the folded slab is smaller. Without it the three above would pass just as happily if folding never fired. |

Gates: unit 43/43, `tpch-spotcheck` PASS, `go test -race` clean, pre-commit
pgbench smoke on every commit.

---

## 6. What is left

| item | note |
|---|---|
| `filterOp` still interprets | §3.2 compiled only the prefilter. The `filterOp` above re-evaluates the same predicate on the ~2 % of surviving rows via `evalExprSlot` — ~0.33 pp. Compiling it means giving `buildRec` a `Gather` arm so parallel plans stop falling back to legacy `Build`, which is a planner-shape change, not an expression one. |
| dispatch win was the soft half | The design predicted 5–9 % from replacing the type switch and flagged in-tree counter-evidence (`BenchmarkJoinKeyEval`: one extra itab lookup made a compiled arm *slower*; the interpreter already hoists `ColumnRef`). The measured 1.20× is consistent with most of the win coming from folding, not dispatch. |
| shared plan-node mutation | `evalExprSlot` writes `CachedTime`/`CacheValid` on plan nodes shared by leader and workers. Pre-existing, benign (equal values), concentrated rather than introduced by folding. Recorded, not fixed. |
| planner-side folding | PostgreSQL folds in `eval_const_expressions` and *shows it in EXPLAIN*. goopg still prints the unfolded expression. Doing it PG's way would rewrite `plan_snapshots` text; deliberately deferred. |

## 7. The series

| | start | take 4 | take 5 | take 6 | **take 7** | PG 18.3 |
|---|---:|---:|---:|---:|---:|---:|
| Q6 parallel | 5.235 s | 2.784 s | 1.210 s | 1.009 s | **0.838 s** | 0.203 s |
| gap to PG | 25.9× | 13.7× | 6.0× | 5.0× | **4.1×** | 1.0× |
| allocations / query | 291.6 M | 60.1 M | 18.8 M | 0.80 M | **0.80 M** | — |
