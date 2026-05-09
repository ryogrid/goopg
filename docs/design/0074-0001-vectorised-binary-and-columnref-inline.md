# Design 0074-0001 — Vectorised evalBinary + ColumnRef inline

**Milestone:** M0074-0001
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0073-0003 (commit `58efeb0`) — OpCode int8
enables jump-table dispatch; M0074-0006 — int64 fast-path
helpers reused by vectorised numeric arms.

## Context

Q5 CPU pprof at M0073-final shows:
- `evalExprSlot` 25.83 % flat / 68.68 % cum
- `evalBinary` 10.39 % flat / 33.72 % cum

`evalExprSlot` (expr.go:52-133) is a 14-arm type switch.
ColumnRef sits at line 100, reachable only after 12
other arms are tested (CaseExpr, SubqueryExpr, InExpr,
ExistsExpr, TypedStringLit, IntervalLit, ExtractExpr,
IntegerConst, NumericConst, StringConst, NullConst,
BooleanConst, ParamRef). For Q5's predicate `l_orderkey =
o_orderkey AND l_shipdate >= ... AND l_shipdate < ...`,
ColumnRef is the dominant case but pays full dispatch
cost.

`evalBinary` (expr.go:163-265) is per-row. Each Q5 batch
of ~1000 rows × 4 columns × 2 operands = ~8000
`evalExprSlot` calls + ~4000 `evalBinary` calls. Each
binary call goes through OpCode switch + null check +
type promotion + arithmetic/comparison.

A vectorised entry point operating column-wise over a
Datum array unlocks ≥ 30 % CPU reduction on Q5's per-page
batches. The arena infrastructure from M0073-0002+0004
already provides per-page Datum batches.

## Goals

- **Goal A — ColumnRef fast-path in evalExprSlot.** Hoist
  `case *planner.ColumnRef:` to the first switch arm
  (ahead of CaseExpr). Q5's hot path saves 12 type-test
  comparisons per call.
- **Goal B — Vectorised `evalBinaryBatch` entry.** New
  function:
  ```go
  func evalBinaryBatch(op parser.OpCode,
                        lefts, rights []Datum,
                        out []Datum) error
  ```
  Operates column-wise over parallel arrays. Initial
  vectorisable arms: OpEq, OpLt, OpGt, OpLe, OpGe, OpNe
  (numeric int64 fast path from M0074-0006), OpAdd,
  OpSub, OpAnd, OpOr.
- **Goal C — `seqScanOp` predicate batch path.** When
  the operator's filter is a single BinaryOp whose entire
  subtree is vectorisable, materialise one decoded batch
  (via existing arena), call `evalBinaryBatch`, mask
  survivors. Falls back to per-row `evalBinary` for
  non-vectorisable predicates.
- **Goal D — Eligibility detector.** `canVectoriseBinary(op,
  lkind, rkind) bool` — conservative whitelist; expand
  later as new arms land.

## Non-goals

- **Full columnar batching of all operators**
  (filterOp, projectOp, hashAgg, sortOp). M0074-0001 lands
  the entry point + seqScanOp predicate; the rest of the
  pipeline still runs row-at-a-time. M0075 candidate.
- **SIMD intrinsics.** Plain Go batch loops — Go's
  compiler may auto-vectorise some. Hand-tuned SIMD via
  cgo / asm is M0075+.
- **LIKE / Concat batched.** String-handling arms have
  variable-cost; defer.
- **Subquery / In / Exists / FuncCall batched.** Have
  side effects (ctx.OuterRows push/pop) — never
  vectorisable.

## Proposed implementation

### Goal A — ColumnRef hoist

Reorder `evalExprSlot` (expr.go:52-133):

```go
func evalExprSlot(e planner.Expr, slot SlotView, ctx *Context) (Datum, error) {
    // Fast-path: ColumnRef is the dominant case in Q5.
    // Hoisted to first arm to skip 12 type-test comparisons.
    if cref, ok := e.(*planner.ColumnRef); ok {
        if slot == nil {
            return Datum{}, &ExecError{...}
        }
        if rs, ok := slot.(rowSlotView); ok {
            if cref.Index < 0 || cref.Index >= len(rs) {
                return Datum{}, &ExecError{...}
            }
        }
        return slot.Get(cref.Index), nil
    }
    // Existing 13-arm switch for non-ColumnRef cases.
    switch x := e.(type) {
    case *planner.OuterColumnRef: ...
    ...
    }
}
```

Alternative: keep the switch and just move the
`case *planner.ColumnRef:` arm to the top. The early-
return form is faster (no switch dispatch overhead) but
duplicates the arm body. Bench-decide; default to the
early-return form per Go inlining heuristics.

### Goal B — `evalBinaryBatch`

```go
// evalBinaryBatch evaluates op over parallel arrays.
// len(lefts) == len(rights) == len(out). Caller-provided
// out is NOT pre-zeroed; arms must write all positions.
//
// Returns error only on the first encountered eval
// failure; subsequent positions in out may be undefined.
// Three-valued NULL logic preserved per arm.
func evalBinaryBatch(op parser.OpCode,
                      lefts, rights []Datum,
                      out []Datum) error {
    n := len(lefts)
    if len(rights) != n || len(out) != n {
        return errors.New("evalBinaryBatch: array length mismatch")
    }
    switch op {
    case parser.OpEq:
        for i := 0; i < n; i++ {
            out[i] = compareEqBatch(lefts[i], rights[i])
        }
    case parser.OpLt: ...
    case parser.OpAdd:
        for i := 0; i < n; i++ {
            // Numeric int64 fast-path from M0074-0006 reused.
            v, err := numericAddFast(lefts[i], rights[i])
            if err != nil { return err }
            out[i] = v
        }
    ...
    }
    return nil
}
```

The body is intentionally simple loops over Datum arrays
— Go's compiler can auto-vectorise the int64 numeric
path. NULL operands are handled per-element (test-and-
write NullDatum).

### Goal C — `seqScanOp` predicate batch path

Today's seqScanOp.Next() decodes one row via
`DecodeRowIntoArena`, evaluates the predicate via
`evalExprSlot`, returns survivor or advances. The batch
path:

1. After arena page Reset, decode an entire heap-page
   worth of rows (~1000 rows) into a Row[] batch (or
   Datum[][] for column-major).
2. Build column-major arrays for the columns referenced
   in the predicate.
3. Walk the predicate AST; for each BinaryOp whose
   children are ColumnRef(s) or constants, call
   `evalBinaryBatch` with the column arrays.
4. AND/OR results compose via bitmask.
5. Iterate survivors row-by-row to feed downstream
   operator.

This requires changing the seqScanOp's hot loop. Stage
defensively: only enable batch path when
`canVectoriseExpression(filter)` returns true; otherwise
keep the current row-at-a-time path.

### Goal D — Eligibility detector

```go
func canVectoriseExpression(e planner.Expr) bool {
    switch x := e.(type) {
    case *planner.ColumnRef, *planner.IntegerConst,
         *planner.NumericConst, *planner.StringConst,
         *planner.BooleanConst, *planner.NullConst:
        return true
    case *planner.BinaryOp:
        return canVectoriseBinary(x.Op, ...) &&
               canVectoriseExpression(x.Left) &&
               canVectoriseExpression(x.Right)
    }
    return false
}

func canVectoriseBinary(op parser.OpCode,
                         lkind, rkind DatumKind) bool {
    switch op {
    case parser.OpEq, parser.OpLt, parser.OpGt,
         parser.OpLe, parser.OpGe, parser.OpNe,
         parser.OpAdd, parser.OpSub,
         parser.OpAnd, parser.OpOr:
        return true
    }
    return false
}
```

Conservative. Excludes LIKE / Concat / Mul / Div /
Subquery / In / Exists / FuncCall / Case / Extract.

## Verification

Pre-commit gate (M0074 stricter post-E):
- Q5 CPU pprof rerun (480 s capture into
  `pprof-data/m0074-0001/q5.cpu.prof`):
  - `evalBinary` cum CPU **≤ 15 %** (was 33.72 %).
  - `evalExprSlot` cum CPU ≤ 50 % (was 68.68 %).
- 21-query sweep: zero row-count change.
- Q12=2, Q13=35, Q21=381, Q22=7, Q9=175 (post-Commit D).

New tests:
- `internal/executor/binary_batch_test.go` — parallel-
  array eval pinning every vectorisable arm against per-
  row baseline (1000 random pairs each).
- `internal/executor/binary_batch_null_test.go` — three-
  valued NULL logic (NULL operand on each side); must
  match per-row.
- `internal/executor/seq_scan_batch_test.go` — exercise
  the batch predicate path with a synthesized varchar
  table; survivor count must match per-row baseline.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Vectorised path mishandles NULL → wrong row count | Three-valued NULL pinned in test against per-row baseline; eligibility detector excludes any expression touching NULL-sensitive arms. |
| R2 | Eligibility detector misclassifies → vectorised path runs on subquery / FuncCall → side-effect bug | Conservative whitelist; recursive expression walk; default to per-row. |
| R3 | ColumnRef hoist changes evaluation order in subtle case (e.g. ColumnRef with side effect) | None today: ColumnRef is pure read; no side-effect arms in evalExprSlot. Documented invariant. |
| R4 | Batch path makes Q5 SLOWER if Go compiler doesn't auto-vectorise | Bench locally before commit; can be reverted independently of ColumnRef hoist if so. |
| R5 | seqScanOp batch path retains decoded Datums past arena Reset → garbage read | Reset boundary unchanged; survivors are emitted before next page advance, so retention is intra-page only. |

## Migration plan

Single commit (Commit E in M0074):
1. Hoist ColumnRef in evalExprSlot.
2. Add `evalBinaryBatch` + `canVectoriseBinary` +
   `canVectoriseExpression`.
3. Wire seqScanOp's predicate eval to the batch path
   when eligible.
4. Land tests.
5. Verify gate: Q5 pprof + 21-q sweep parity.

If Q5 evalBinary cum CPU doesn't drop ≥ 50 %: investigate
+ retry; M0074-0001 win is best-effort. Q9 = 175 must
still hold (M0074-0002 invariant).
