# Take 7 — expression compilation: use the compiler that already exists, and give it constant folding

**Status:** implemented and measured — results in [benchmark-results-take7.md](benchmark-results-take7.md)
**Date:** 2026-08-30
**Branch:** `perf-opt-take4`
**Baseline:** `bbe39dd4c` (take 6)
**Raw artefacts:** `tmp/take4/runs/q6-t7base/`

---

## 1. The finding that changes the design

[benchmark-results-take6.md §6](benchmark-results-take6.md) left `evalExprSlot`
as the top item and named "expression compilation (PG's `ExecReadyExpr`/JIT)"
as the fix. Before designing one, I looked for prior art — and **goopg already
has an expression compiler.**

`internal/executor/exprnode.go` (M0107-0003 Phase C.3, hardened by M0127-PS6.1/2)
compiles `optimizer.Expr` trees into a flat `[]ExprNode` slab dispatched by an
**integer kind-switch** instead of interface type assertions, which is exactly
the interpreter-side half of what `ExecReadyExpr` does in PostgreSQL
(`ExecInitExpr` → linear `ExprEvalStep` array). It supports `ExprColumnRef`,
`ExprIntConst`, `ExprBoolConst`, `ExprNullConst`, `ExprBinaryOp`, `ExprUnaryOp`,
and falls back to **`ExprAdapter`**, which delegates to `evalExprSlot` — so an
unsupported node has exactly one implementation, not two.

It has also been differentially tested: the PS6.2 parity corpus found **619
diffs** whose root cause was the `!IsNull()` vs `Kind == KindBool`
short-circuit gate. Its AND/OR short-circuit is **semantically** identical to
the interpreter's (the interpreter spells it `if / else if`, the compiled twin
a `switch`).

That is encouraging but must not be overstated, because the same M0127-PS6.2
work found **three further divergences by reading rather than by the corpus**:
the literal-0 error position, an `isFloatResultType` list that had drifted on
the `"double"` spelling, and an `overflowCodeForType` exact-string compare
where `"INT4"` "raised 22003 on one evaluator and returned 2147483648 on the
other". The corpus demonstrably does not cover the whole divergence surface, so
§5 adds a fresh differential test rather than leaning on it.

**So this round writes no new evaluator.** Building a second one — closure
trees, a bytecode VM, real JIT — would duplicate semantics that took a
619-diff corpus to get right, which is precisely the maintainability regression
the goal excludes.

Two things are wrong instead, and both are small:

1. **Nothing in this plan is compiled at all.** `evalFastExpr` accounts for
   **0.02 s of 166.45 s** of Q6 CPU — 0.012 %.

   An earlier draft explained that as "the compiled filter is starved of rows".
   That was wrong, and the real reason is more consequential: `buildRec` has
   **no `Gather` arm**, so a Gather falls to `OpAdapter` and its whole partial
   plan — `Filter` *and* `SeqScan* — is built by the legacy `Build`
   (`executor.go` Gather arm → `BuildWorker` → `buildNode`). The compiled
   predicate at `executor.go:504` is therefore **unreachable for any query
   under a Gather**, which is every parallel query. The `filterOp` Q6 actually
   runs holds a raw `optimizer.Expr` and calls `evalExprSlot` directly
   (`operators.go:565`) — visible in the profile as a direct 2.66 s (4.15 %)
   edge into `evalExprSlot`, alongside `(*seqScanOp).evalPrefilter`'s 61.05 s
   (95.30 %).

   So this round is not "switch the hot path onto an idle compiler"; it is
   "give the hot path a compiled expression for the first time".
2. **The compiler does not constant-fold.** PostgreSQL folds in
   `eval_const_expressions` at plan time; its Q6 plan prints the *folded*
   `'1995-01-01 00:00:00'::timestamp`. goopg's prints
   `('1994-01-01'::date + '1 year'::interval)` and **re-evaluates that addition
   once per row, six million times per query.**

---

## 2. What that costs, measured

30 s CPU profile at `bbe39dd4c`, parallel Q6. `evalExprSlot` is 38.49 % cum /
**16.70 % flat**; the flat part is the interface type-switch that
`exprnode.go` exists to remove.

Re-evaluated constants, all reachable only because folding is absent:

| symbol | time | % of total CPU | what it is |
|---|---:|---:|---|
| `evalBinary` → `addTimeInterval` | 7.46 s | **4.48 %** | `'1994-01-01'::date + '1 year'::interval`, per row |
| `evalTypedStringLit` | 3.85 s | **2.31 %** | the two date literals — **already memoized**, so this is the call plus Datum reconstruction, *not* re-parsing |
| `parseNumericFastScale` | 1.98 s | **1.19 %** | `0.04` / `0.060000000000000005` — genuinely re-parsed; `NumericConst` has no cache |
| `evalIntervalLit` | 0.22 s | 0.13 % | `'1 year'` — also already memoized |
| `parseNumericFastInt` | 0.10 s | 0.06 % | `24` — genuinely re-parsed |
| | | **≈ 8.2 %** | |

**An important correction to the framing.** `evalTypedStringLit` and
`evalIntervalLit` are *already* memoized on the planner node
(`x.CacheValid`/`x.CachedTime`); `pprof -list` puts 2.93 s of
`evalTypedStringLit`'s 3.23 s flat on the cached-return line with nothing below
it. So 2.44 of the 8.2 points are **not** re-parsing — they are the cost of
reaching a cache: a call, a type-switch arm, and rebuilding a Datum, six
million times. Only the two `parseNumericFast*` lines (1.25 %) are genuine
re-parsing. Folding removes both kinds, but §1's "PG folds, goopg re-parses"
slogan is only accurate for a quarter of the total; the rest is "goopg
re-*dispatches*".

Plus the type-switch dispatch each constant node costs inside the 16.70 %,
which folding removes outright by deleting the nodes.

Q6's predicate is **21 nodes**, of which **7 are constant**: 6 literals
(2 `TypedStringLit`, 1 `IntervalLit`, 2 `NumericConst`, 1 `IntegerConst`) and
1 constant `BinaryOp`. There are **no `CastExpr` nodes** — goopg plans
`'1994-01-01'::date` as a single `TypedStringLit`, which is why
`evalTypedStringLit` and not `evalCast` appears in the profile. Folding
collapses those 7 into **5** pre-computed `Datum`s, leaving **14** non-constant
nodes (4 `AND`, 5 comparisons, 5 `ColumnRef`).

> **These percentages are from `q6-t7base` and are not comparable to take 6's.**
> take-6 quoted `evalExprSlot` flat at 14.19 %, this doc at 16.70 %; they are
> different builds and different profiles. Only same-profile comparisons are
> meaningful.

---

## 3. Design

Two changes, both additive.

### 3.1 Constant folding in `buildExpr`, opt-in via a Context

```go
func (s *exprTreeSlab) buildExprCtx(e optimizer.Expr, ctx *Context) int32
```

`buildExpr(e)` becomes `buildExprCtx(e, nil)`. **With `ctx == nil` the behaviour
is byte-identical to today**, so the eight existing call sites
(`executor.go:504/518/532/533/538` and `join_composite_key.go:114/121/123` —
an earlier draft said four and missed the join key/residual compiler entirely)
are untouched and cannot regress.

New node kind `ExprConstVal`, carrying the folded value in a
`constVal *Datum` field.

> An earlier draft proposed a `consts []Datum` side-slice on the slab with an
> index in `payload`, and claimed that kept `ExprNode` at 72 bytes. **That is
> not implementable**: `exprTreeSlab` is `type exprTreeSlab []ExprNode`, a bare
> slice with nowhere to hang a field, and turning it into a struct would change
> every signature the same paragraph promises not to touch. The pointer costs
> what the side-slice was meant to avoid: `ExprNode` goes **72 → 80 bytes**.
> `Datum` is 48 bytes against a 40-byte payload, so it cannot ride inline
> either way.

**The folding rule.** `exprFoldable` is a **whitelist** — it names the kinds it
admits (`ColumnRef` is *not* among them) and returns false for everything else,
so an unrecognised expression kind costs performance, never correctness. (The
earlier draft phrased this as a blacklist — "foldable if it contains no
ColumnRef, no OuterColumnRef, …" — and then asserted whitelist semantics. Only
the whitelist is safe, and that is what is implemented.)

A whitelisted subtree is then handed to `foldConstant`, which **declines in two
cases**:

1. **The evaluation errors.** Folding a failing constant would move the error
   from row time to build time, changing when the statement fails and whether
   earlier rows were already returned. PostgreSQL's `eval_const_expressions`
   declines for the same reason.

2. **The value depends on session settings.** This is the one that nearly
   shipped as a bug, and it is parallel-only. `gatherOp` Opens the leader's
   child with the session `Context` but each worker with a `NewWorkerContext`
   whose `GetSetting` is deliberately nil; `timeZoneFromCtx` returns `""` for
   such a Context. A zone-less `TIMESTAMPTZ` literal therefore reads as the
   session TimeZone in the leader and the default zone in every worker —
   **measured here as instants nine hours apart** (`1788058800000000000` vs
   `1788091200000000000`). Folding would freeze those two different constants
   into two plans running the same query.

   Rather than enumerate which types consult the session — a list that would
   drift — the check is **self-verifying**: evaluate once with the caller's
   Context and once with a settings-blind copy of it, which is exactly what a
   worker sees, and fold only if the two agree. This mirrors the rule
   `evalTypedStringLit` already applies to its own cache ("when `usedSession`
   fired … it must not be written to `x.CachedTime`/`x.CacheValid`").

   The comparison is on the **raw** Datum, not `Format()`. That detail is
   load-bearing: `Format()` renders a KindTime as wall-clock text, so the two
   nine-hours-apart instants above both print `2026-08-30 12:00:00` under some
   settings. A `Format()`-based comparison — which the first implementation
   used — would have declared them identical and folded the wrong constant.

### 3.2 Give the scan prefilter the compiled path

`seqScanOp` builds a **per-operator** slab in `Open`, after the reopen/rewind
early-return so a same-Context rescan reuses it, and `evalPrefilter` calls
`evalFastExpr`. `Open` is where a real `*Context` exists, which §3.1 needs; and
each parallel worker builds its own operator tree, so the slab is
goroutine-private.

**Blast radius, stated plainly:** `seqScanOp` and its `Open` are shared by
*both* builders, so this is **not** limited to the fast path. Every
prefilter-armed scan on every path gets the compiled, folded predicate —
including the extended query protocol, plpgsql, subplans, FK checks, COPY,
MERGE and CTAS. An earlier draft claimed the legacy `Build` path "has no slab
and uses `evalExprSlot` exactly as today"; there is no such split. The
`ctx == nil ⇒ unchanged` guarantee in §3.1 protects the *other eight*
`buildExpr` callers, not this one — this one is genuinely new behaviour
everywhere, which is why §5's differential test and the spotcheck matter more
than the compile-time argument.

Fallback remains for `noExpr` (nothing compiled), in which case the interpreter
runs as before.

### 3.3 What this deliberately does not do

- **No JIT, no bytecode VM, no closure tree.** The existing slab already gives
  the integer-dispatch win; a third representation would add a semantics
  surface without adding a mechanism.
- **No plan-time folding.** PostgreSQL folds in the planner and *shows it in
  EXPLAIN*, which is strictly more PG-faithful. goopg's plan snapshots are
  compared as text by `make plan-diff` / `plan-gate`, so folding in the planner
  would rewrite the recorded plan text for every affected query at once.
  Executor-side folding gets the whole runtime win with zero plan churn. The
  PG-faithfulness gap is real and is recorded here rather than hidden: goopg's
  `EXPLAIN` will keep printing the unfolded expression.

  (Caveat on "zero churn": Q6's snapshot already reports DIFFER against the
  current committed baseline, so for this query the baseline is stale
  independently of anything here.)

---

## 4. Expected effect

| source | share |
|---|---:|
| constant re-evaluation removed (§2) | ≈ 8.2 %, **less** the ~0.33 pp that `filterOp` keeps paying on survivors (§3.2 folds only the prefilter) |
| dispatch: `evalExprSlot` flat 16.70 % over 14 remaining non-constant nodes | soft; see below |
| **total** | **≈ 13–17 %** |

Amdahl gives **1.15–1.20×**.

**The dispatch half is a guess, and there is in-tree evidence against it.**
`exprnode.go` itself records that in `BenchmarkJoinKeyEval` a single extra itab
lookup (~1.4 ns/eval) "alone made the compiled key arm SLOWER than the
interpreter it replaces". And the interpreter already hoists `ColumnRef` ahead
of its type switch (M0074-0001), so the most common node kind never pays the
full arm sequence the estimate assumes. The honest position is that the
constant-folding half is well grounded and the dispatch half could plausibly be
near zero; §6 therefore accepts on the *measured* outcome.

**No allocation win is claimed.** Take 6 already removed 95.8 % of allocations,
and what folding removes is non-allocating work (`parseNumericFast*` returns
scalars; `NewTimeDatum` does not allocate). Post-change allocation is a
**regression check, not a target**.

## 5. Correctness

| risk | mitigation |
|---|---|
| Compiled and interpreted twins disagree | `TestCompiledFoldedExprMatchesInterpreter` — a 12-shape corpus × 5 rows, comparing value, NULL-ness and error text, including the non-boolean-left-of-AND shape that caused the PS6.2 diffs and a NULL row. Not leaning on the PS6.2 corpus alone (§1). |
| Folding moves an error from row time to build time | `foldConstant` declines on error; `TestFoldingDeclinesOnError`. **Note this is currently inert at the only caller**: the prefilter discards its errors and lets `filterOp` raise them. Rule 3 is insurance for the next opt-in caller, not active protection today — worth stating rather than counting as a live mitigation. |
| **Leader and workers fold different constants** | `foldConstant`'s settings-blind second evaluation (§3.1 rule 2); `TestFoldingDeclinesSessionDependentValues` pins it with a zone-less TIMESTAMPTZ and fails if it ever folds. |
| Folding never actually fires (a vacuous green) | `TestFoldingActuallyFolds` — positive control asserting an `ExprConstVal` appears, that `ctx == nil` produces none, and that the folded slab is smaller. |
| Whitelist admits something non-deterministic | Fails closed; `FuncCall` excluded wholesale rather than judged per function. |
| Concurrency | Per-operator slab. **But note a pre-existing hazard the slab does not address**: `evalExprSlot` writes `CachedTime`/`CacheValid` on *shared* plan nodes, and leader plus workers call `Open` concurrently over the same pointers. Folding does not introduce this — the per-row interpreter writes the same fields — but it concentrates every goroutine's write into one instant at Open. The values written are equal, so it is benign in practice; recorded because §5 should not claim a row is closed when it is not. |
| Silent row-count regression | `scripts/tpch-spotcheck.sh` (Q12 = 2, Q13 = 34); Q6 bit-identical; `go test -race`. |

## 6. Acceptance

1. Q6 result bit-identical: `102513054.4896`.
2. Unit gate, `tpch-spotcheck`, `-race` all green.
3. Differential test: compiled == interpreted on the corpus, including NULLs
   and the short-circuit shapes that produced the PS6.2 diffs.
4. Measurable wall-clock improvement on Q6, alternating A/B, ranges disjoint.
   **No allocation regression.**


---

## 7. Review record

Adversarial agent review, 2026-08-30, against the pre-implementation draft:
**3 critical, 7 major, 8 minor**. All fixed above. One of them was a real bug
about to ship.

| # | finding | resolution |
|---|---|---|
| **C1** | "The `Filter` predicate *is* compiled … the compiled path is real but nearly idle." False. `buildRec` has no `Gather` arm, so a Gather's whole partial plan is built by legacy `Build`; `executor.go:504` is unreachable for any parallel query, and Q6's `filterOp` calls `evalExprSlot` directly. | §1 rewritten. The round is "give the hot path a compiled expression for the first time", not "switch onto an idle compiler". |
| **C2** | **A real correctness bug.** `gatherOp` Opens the leader with the session Context and each worker with a `NewWorkerContext` whose `GetSetting` is nil, so a session-sensitive constant folds to *different values* in leader and workers — a parallel-only wrong answer. | Closed by `foldConstant`'s settings-blind second evaluation. Verified the hazard is real: a zone-less TIMESTAMPTZ folds to instants **nine hours apart**. Pinned by `TestFoldingDeclinesSessionDependentValues`. **Also caught a flaw in my own first fix** — it compared `Format()`, which renders wall-clock and printed both instants identically; the comparison is now on the raw Datum. |
| **C3** | The `consts []Datum` side-slice is not implementable (`exprTreeSlab` is a bare slice) and the "no growth" claim is false. | §3.1 corrected: `constVal *Datum`, `ExprNode` 72 → 80 bytes, stated as a cost rather than avoided. |
| **M1** | "the non-fast `Build` path with no slab" — no such split exists; `seqScanOp.Open` is shared, so the change reaches the extended protocol, plpgsql, subplans, FK checks, COPY, MERGE, CTAS. | §3.2 now states the real blast radius and that the `ctx == nil` guarantee protects the *other* callers, not this one. |
| **M2** | Four `buildExpr` call sites → actually **eight**, including the join key/residual compiler. | Corrected. |
| **M3** | "re-parsed per row" is false for `evalTypedStringLit` and `evalIntervalLit` — both are memoized; 2.93 s of 3.23 s flat sits on the cached-return line. | §2 corrected: 2.44 of the 8.2 points are re-*dispatch*, not re-parse. Only the 1.25 % of `parseNumericFast*` is genuine re-parsing. |
| **M4** | "11 constant nodes … `CastExpr`/`TypedStringLit` wrappers" — actually **7**, and there are **no `CastExpr` nodes**; `evalCast` has zero samples. | Corrected. |
| **M5** | "~10 remaining nodes" → **14**. | Corrected. |
| **M6** | The concurrency row ignored that `evalExprSlot` writes `CachedTime`/`CacheValid` on *shared* plan nodes while leader and workers Open concurrently. | Recorded as a pre-existing hazard that folding concentrates but does not introduce. |
| **M7** | "619 diffs from 2 root causes … character-identical". The corpus is credited with one root cause; three further divergences were found by *reading*. And the two short-circuits are `if/else if` vs `switch`. | Corrected to "semantically identical", and §5 no longer leans on the corpus — it adds a fresh differential test. |
| m1–m8 | 0.02 s is 0.012 % of 166.45 s CPU, not of 30 s; the rescan-reuse claim depends on placement after the rewind early-return; rule 1 was written as a blacklist while asserting whitelist semantics; `filterOp` still re-evaluates on survivors (~0.33 pp survives); rule 3 is inert at the current caller; the dispatch estimate has in-tree counter-evidence (`BenchmarkJoinKeyEval`, `ColumnRef` hoisting); Q6's plan snapshot is already stale vs baseline; 16.70 % and take-6's 14.19 % are different builds. | All corrected in place. |

Claims verified **correct**: the `ExprAdapter` delegation and supported kind
list; every profile figure quoted in §2; that `'1994-01-01'::date + '1 year'::interval`
is a genuine constant subtree re-evaluated per row; the 21-node count and the
5-`Datum` fold target; `Datum` = 48 B and pre-change `ExprNode` = 72 B;
`buildExpr(e) == buildExprCtx(e, nil)`; that each parallel worker builds its own
operator tree; that `Open` is the first place a `*Context` exists; that no
allocation win should be claimed; and that planner-side folding would rewrite
`plan_snapshots` and disturb `plan-gate`.
