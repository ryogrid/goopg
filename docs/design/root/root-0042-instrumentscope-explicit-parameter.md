# root-0042 — Executor instrumentation scope as an explicit parameter (the package-global race)

status: accepted
date: 2026-09-19
area: executor / EXPLAIN ANALYZE instrumentation / concurrency
supersedes: none
related: `docs/design/executor-ex0-03b-rows/DESIGN.md` §5 erratum,
`.ralph/deferral_ledger.md` `take3-instrumentscope-datarace` +
`e18-instrumentscope-global-races-coop-producers` (both resolved here),
`.ralph/fix_plan.md` M-NIGHTLY-instrumentscope-race-fix

## Summary

EXPLAIN ANALYZE's node-instrumentation scope was a **package global**
(`var instrumentScope *instrumenter`) swapped under a mutex by every
scope-handoff site — but `maybeInstrument` **read** it with no lock on
every `buildNode` call, including lazy builds reached mid-`Next()` from
a different goroutine (`seqScanOp.evalQual` → `evalInExpr`/`evalExistsExpr`
→ `acquireSubPlanOp` → `Build`). The race reproduced five times in
nightly CI (`race/internal/executor`, `TestParallelLateralProbeIdentity`
+ `TestSubquerySemanticsMatrix/M20`), was live on any parallel query
with a concurrently lazily-built SubPlan — not just ANALYZE — and was
twice ledgered.

Fix: the scope is now a **plain function argument**. `buildNode(plan,
bound, scope)` and `maybeInstrument(plan, op, scope)` thread it through
the whole dispatch; `Build`/`BuildWorker` keep their exported signatures
as thin `nil`-scope wrappers for their ~200 external callers; every
handoff site passes the scope it means instead of mutating shared state.
The global, `instrumentScopeMu`, `buildUnderFreshScope`, and
`buildUnderNilScope` are deleted — there is no shared mutable state left
to race on, and no mutex.

## Why a parameter, not a wider lock or Context threading

The ledger's own warning ruled out widening `instrumentScopeMu` to guard
reads: that silences `-race` while leaving the semantic hazard the
mutex's own doc comment named — a lazily-built producer subtree adopting
an unrelated sibling worker's live scope and polluting its stats table
(double-counted EXPLAIN ANALYZE counters, or an instrumented bitmap
prebuild that is never closed so its loops leak). The hazard is not
ordering, it is **ambiguity**: a global cannot express "this build wants
NIL" and "that build wants fresh-table" at the same time.

Threading through `Context` was the ledger's first sketch but does not
fit the actual call shape: ~200 callers (almost all tests) do
`op, err := Build(plan)` *before* constructing a `*Context`, and several
scope decisions are needed at sites that legitimately have no Context at
all (the `prebuildSharedHashJoins`/`prebuildBitmap` throwaway-tree
closures). A parameter is the only mechanism that makes each site's
intent explicit at the call itself.

## What changed

- `instrumenter{timing, table}` is unchanged as a type; it is now created
  by `withInstrumentation` (top-level EXPLAIN ANALYZE build) or by
  `buildChildForSlot` (per execution site) and passed down, never
  published.
- `buildNode(plan, bound, scope)` — third parameter threaded through
  every recursive arm and every `maybeInstrument` call (~78 sites,
  rewritten uniformly).
- `maybeInstrument(plan, op, scope)` — nil scope returns `op` unchanged;
  non-nil allocates stats in `scope.table` and stamps
  `instrumentScopeCarrier` ops with the scope object.
- `Build`/`BuildWorker` — `buildNode(plan, deformBoundNone, nil)`.
  Byte-for-byte compatible for all external callers.
- `gatherOp`/`gatherMergeOp` — `buildChild` field type becomes
  `func(scope *instrumenter) (Operator, error)`. `setInstrumentScope`
  stores the build-time scope (immutable thereafter — workers read it
  freely). `buildChildForSlot(slot)` returns `buildChild(nil)` when
  unarmed, else builds under a **fresh** `instrumenter{timing:
  o.scope.timing, table: make(nodeStatsTable)}` and files the table into
  `workerTables[slot]` — `n+1` pre-sized slots (workers `0..n-1`, leader
  `n`), disjoint-index writes from concurrent goroutines, folded by
  `foldGatherWorkerStats` in `Close` **after** `group.Wait()` supplies
  the happens-before edge.
- Lazy builds — `cteDMLPrefixOp` (the only non-Gather carrier) keeps its
  stamped scope in a field and `buildUnderScope(n)` now does
  `buildNode(n, deformBoundNone, o.scope)` — identical instrumentation
  semantics to the old save/restore, minus the shared mutation. Under a
  Gather each worker's own `cteDMLPrefixOp` copy is stamped with that
  worker's fresh scope, so nested DML builds land in the right per-slot
  table.
- Throwaway prebuilds — `prebuildSharedHashJoins` and
  `parallelClaimSet.prebuildBitmap` call `buildChild(nil)` explicitly.
  Their trees must not register stats in any worker/leader table (the
  bitmap tree is never even `Close`d, so instrumented loops would leak).
- `BuildFast`/`opTreeSlab.buildRec` — the op-tree slab path is never
  instrumented (EXPLAIN ANALYZE builds through the legacy `buildNode`),
  so its adapter fallback passes `nil`.
- SubPlan lazy builds — `acquireSubPlanOp` and the `expr.go` evaluator
  sites stay on `Build(plan)` ⇒ `nil` scope. Resolved open question:
  `TestExplainAnalyzeSubPlanScopeObservation` (written test-first in the
  prior loop) pins that SubPlan children are **never** instrumented
  today — the top-level scope dies when the initial `Build` returns,
  before `Open`/`Next` run — so `nil` is bug-for-bug compatible, not a
  behaviour change. (PG's `instrument.c` *does* instrument SubPlan
  children; that fidelity gap stays ledgered under m0142-0004a, out of
  scope for a race fix.)

## Race-freedom argument

- No package-global scope exists; there is nothing shared to read.
- Each `nodeStatsTable` is written only by the goroutine that owns its
  build (top-level: the EXPLAIN ANALYZE caller; worker `i`: worker
  goroutine `i`; leader: the Open caller). `maybeInstrument`'s map write
  happens on that same goroutine.
- `workerTables` is pre-sized before `group.Go`; per-slot stores are
  disjoint-index writes to a fixed slice — no reallocation, safe.
- `foldGatherWorkerStats` reads the tables only after `group.Wait()`,
  which is the happens-before edge for every worker's writes.
- `o.scope`/`o.buildChild` are written once during the serial build
  phase (before `Open`), read-only afterwards.
- A `cteDMLPrefixOp` instance's `o.scope` is likewise write-once at
  stamp time; concurrent access would require concurrent `Open`/`Next`
  on one operator instance, which the executor never does.

## Verification

- `go test -race -timeout 45m ./internal/executor/` — **clean** (61.3s);
  the two historically racing tests (`TestParallelLateralProbeIdentity`,
  `TestSubquerySemanticsMatrix`) pass. This closes the five nightly
  reproductions of `race/internal/executor`.
- `go test ./internal/executor/` — pass (12.9s).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — green.
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2, Q13=34; gate-stamp FAIL is
  the dirty-tree guard, not a query failure).
- `make plan-gate` — 14/22 diverged, identical to the recorded
  baseline drift at HEAD (baseline `m0137-0005-rebaseline-20260915`
  predates ~100 internal commits; the live `:65433` binary predates
  HEAD). This change cannot alter plan shapes — it touches only
  instrumentation plumbing, and plan-snapshot diffs plain `EXPLAIN`
  output which never enters `withInstrumentation`.
- Worker/leader counter double-counting: covered by the existing
  `explain_parallel_workers_test.go` gate (per-site fresh tables folded
  once, post-join) — unchanged semantics.

## Follow-ups (not this task)

- SubPlan children uninstrumented where PG instruments them — the
  m0142-0004a PG-fidelity gap; would need a `Context`-carried scope at
  the `acquireSubPlanOp`/`expr.go` lazy-build sites.
- `pg_get_constraintdef` empty-renderer gap noted during M0142-0003i —
  unrelated, cosmetic.
