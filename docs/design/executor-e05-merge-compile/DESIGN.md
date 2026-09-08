# E-05 (EX4-02) — Merge residual + key compilation

Status: accepted. Implements TODO_ALL.md E-05. Design: take3 13 §6
(EX4-02). The hash arm is DONE (M0127-PS6.1: `initExecKeys` →
`compileExecExprs` → node lists + `execResidualNode`, `evalFastExpr`
at use); this is its merge twin. No planner dependency.

## 1. Objective

Compile `mergeResidualMatch` and the merge-side key evaluators through
the same slab (`exprnode.go`, `ExprAdapter` fallback contract). Gate:
twin-parity per arm; values + pin (R8 values-diff mandatory —
join-adjacent).

## 2. Sites (two, one commit)

- **Residual** (`join_merge_key.go:106`): `evalExpr(o.mergeResidual,
  row, o.ctx)` per joined pair → `evalFastExpr` on a row slot.
  Nil-residual short-circuit (→true) stays in front.
- **Keys** (`join_merge_stream.go:260`, `mergeSortedSource.fill`):
  `evalExprSlot(e, slot, ...)` per key per row → `evalFastExpr` with
  per-side node lists. NULL-key semantics (`nullKey` flag, outer-side
  emission) stay exactly as today — only the eval dispatch changes.

## 3. Mechanism (mirror PS6.1, separate slab)

- New fields on `joinOp`: `mergeExprs exprTreeSlab`,
  `mergeResidualNode int32`, `mergeKeyNodesL/R []int32`. SEPARATE slab
  from `execExprs` by discipline (not by type — cross-reading would
  compile fine): single algo per `Open` (`openMergeJoin` runs
  `initMergeKeys`, never `initExecKeys`; the `ensureExecKeys` sites
  are hash-build-only, unreachable from `nextMerge`), and both slabs
  rebuild unconditionally (`[:0]`-reset in `initMergeKeys`, like
  `compileExecExprs` — covers the NLI-inner re-Open case). Fourth
  exec-slab build site noted for completeness:
  `joinPredicateMatchSlot` (lazy `compileExecExprs`); harmless under
  separate slabs.
- `initMergeKeys` compiles after resolving (same-split discipline):
  node lists from `mergeSideKeyExprs(true/false)` in plan order,
  residual node from `o.mergeResidual`. `newMergeSortedSource` reads
  the compiled lists off `o` (it already receives `o` + `isLeft`).
- Residual eval needs a slot over the concatenated pair row.
  UNCONDITIONALLY hoisted (not "if the arm fires"): hold ONE
  DEDICATED reusable `MaterializedSlot` on the operator — not `o.slot`
  (`nextMerge` reuses it for output; sharing aliases emitted-row
  storage across `Next` calls) — and rebind `.row` per pair (same
  pattern `mergeSortedSource.fill` uses; `Width()` auto-tracks,
  `Get` reads through, no cached state). Fresh `hasCTID=false`
  forever (rebind sets only `.row`; a TID-stamped slot would diverge
  from today on `CTIDExpr`). Schema nil, asserted (not conditional):
  `evalFastExpr` ColumnRef is index-only; names surface via
  `n.orig` error text only; `FuncCall` resolves via catalog/ctx;
  `OuterColumnRef` via `ctx.OuterRows`; legacy Row arms via
  zero-copy `slotToRow`. No arm reads `slot.Schema()`.
- Fallback is structural: `buildExpr` demotes uncompilable nodes to
  `ExprAdapter` (delegates to `evalExprSlot`), so exotic residuals
  evaluate identically, only slower. No per-site fallback branches.

## 4. Safety contract

- Wrapper logic UNTOUCHED: residual nil→true, NULL→false +
  `joinFilterRemoved` counting, key-NULL → `nullKey` path. The diff is
  pure dispatch (interpreted → compiled) at two call sites.
- Twin-parity per arm, on the EXTENDED `expr_sibling_parity_test.go`
  outcome harness (panic + SQLSTATE + pos + message — the
  `join_compiled_key_test.go:40-80` value-only comparison cannot
  deliver error parity, and PS6.2's AND/OR-on-non-bool precedent is
  exactly the merge-residual risk class): `parityAllSlots` incl.
  `mergedKeySlot` VirtualSlot + nil-schema MaterializedSlot, over a
  merge-space corpus — `NULL AND TRUE/FALSE`, `NULL OR x`,
  div-by-zero/overflow error-POSITION parity, volatile `FuncCall`
  same per-row eval count, `OuterColumnRef` error parity,
  `CTIDExpr`-reads-NULL parity, out-of-range ColumnRef error-text
  parity (`join_compiled_key_test.go:88-127` pattern, extended to the
  residual slot), `MergeWholeRowRef`, outer-ref panic parity.
  Volatile `FuncCall` excluded from value parity by construction
  (separate evaluations can never agree — falls to ExprAdapter on
  both twins, same call, same count; inspected, not outcome-tested).
- NOEXPR PIN (inversion hazard): `evalFastExpr(noExpr)` is NullDatum
  → wrapper NULL→false, but nil-residual must short-circuit →true
  BEFORE eval. Explicit parity test: routing nil through
  build→noExpr→eval must still emit all rows (an implementer who
  drops the short-circuit zeroes every all-equi merge join). Key
  side safe by construction (`newMergeSortedSource` pre-rejects nil
  keys via `errMergeKeyNil`) — stated, not tested.
- Sibling rule: interpreted expressions (`mergeKeys`,
  `mergeResidual`, `mergeSideKeyExprs`) stay as the source of truth;
  nodes are derived artifacts. Never edit one without the other.

## 5. Gate

- Twin-parity tests per arm (new).
- Optimizer + executor suites + units scope green.
- R8 values-diff BOTH suites (TPC-H `-digest`/`-diff` clean-vs-dirty
  arms; TPC-DS SF0.5 `CKMISMATCH=0`) — with EXPLAIN-driven
  confirmation that merge joins actually execute in both (or a
  forced-merge regression query), else R8 may never touch the new
  code.
- Plan-shape pin (no planner change — expect zero moves; any move is
  a bug) + spotcheck.
- Timing arm advisory; ALLOC arm MANDATORY: per-pair delta ≤ 0
  (`BenchmarkJoinResidualEval`-pattern on merged-space VirtualSlot +
  hoisted MaterializedSlot) — the hoist is unconditional, so the arm
  asserts it rather than discovering whether it was needed.
