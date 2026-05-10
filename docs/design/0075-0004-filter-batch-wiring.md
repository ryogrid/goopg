# Design 0075-0004 — filterOp predicate batch wiring

**Milestone:** M0075-0004
**Status:** **DEFERRED to M0076** alongside M0075-0003.
The batch wiring requires Materialize-each-row before
adding to the per-batch buffer (R1 mitigation in this
design's risk register), and Materialize behaviour
across batch/page boundaries is currently fragile —
exposed by M0075-0003's silent-regression revert at
2026-05-10. Re-attempting the batch wiring before the
M0076-0001 retention-site audit is unsafe; the same
arena slot-reuse aliasing that broke M0075-0003 would
manifest in the batch path's per-row Materialize calls.
M0074-0001's `evalBinaryBatch` + detectors stay landed
as forward-compat surface; the wiring is the deferred
piece. M0076-0002 (planned).
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0074-0001 (commit `3bc631d`) —
`evalBinaryBatch`, `canVectoriseBinary`,
`canVectoriseExpression` at
`internal/executor/expr_batch.go`.

## Context

M0074-0001 landed the vectorised-eval infrastructure
(`evalBinaryBatch(op, left[], right[], out[])` +
detector helpers) but no operator hot loop calls it.
Phase-6 §3 noted Go's type-switch dispatch is already
efficient at the macro %; the per-row → batch transition
is the actual perf lever.

Research (Explore agent, 2026-05-10) found:
- The filter is in `filterOp` (a wrapping operator),
  NOT seqScanOp. SeqScan in `plan.go:333-338` has no
  Filter field; predicates flow as `Filter(SeqScan)`.
- Batch wiring belongs in `filterOp.Open` /
  `filterOp.Next`.
- `canVectoriseExpression` already excludes
  non-amenable predicates (FuncCall, In, Exists,
  SubqueryExpr, CaseExpr, Extract).
- Typical TPC-H page has ~40 rows (8 KB page /
  ~200 byte row). The arena Reset boundary at per-page
  (M0073-0004) aligns with batch boundaries.
- Transposition (row-major → column-major) is a simple
  inner loop: `cols[j][i] = decodedRows[i][j]`. For
  40 rows × 10 columns = 400 assignments per batch —
  negligible vs. decode cost.

## Goals

- When `filterOp.Predicate` passes
  `canVectoriseExpression`, batch-decode input
  operator's output (~40 rows per batch); build
  column-major Datum arrays for the columns the
  predicate references; call `evalBinaryBatch` per
  binary op; AND the result bitmasks; emit survivors
  row-at-a-time downstream.
- Falls back to per-row `evalExprSlot` when predicate
  isn't vectorisable.
- Q5 / Q12 / Q13 wall time delta ≤ 110 % of M0074-final
  (best case: 30-50 % drop on filter-heavy queries).

## Non-goals

- **Wiring evalBinaryBatch into other operators** —
  hashAgg / sortOp / projectOp etc. M0076 candidate.
- **SIMD intrinsics** — pure Go batch loops only.
- **Plan-level changes** — the planner-bound
  filterOp.Predicate stays as-is.
- **Predicate decomposition / rewriting** — the
  walker accepts the predicate as-is and either
  vectorises the entire tree or falls back.

## Proposed implementation

### filterOp state extension

```go
type filterOp struct {
    // ... existing fields ...
    plan      *planner.Filter
    input     Operator
    
    // M0075-0004: batch-eval state. Populated when
    // canVectoriseExpression(plan.Predicate) returns
    // true at Open() time.
    canBatch    bool
    batchSize   int        // typically 40
    batchRows   []TupleSlot // pulled from input each fillBatch
    surviving   int        // index of next survivor to emit
    
    // Pre-allocated buffers (avoid per-batch alloc).
    leftCol     []Datum
    rightCol    []Datum
    outBatch    []Datum
}
```

### Open

```go
func (o *filterOp) Open(ctx *Context) error {
    if err := o.input.Open(ctx); err != nil {
        return err
    }
    o.ctx = ctx
    o.canBatch = canVectoriseExpression(o.plan.Predicate)
    if o.canBatch {
        o.batchSize = 64 // power-of-two, slightly bigger than typical page
        o.batchRows = make([]TupleSlot, 0, o.batchSize)
        o.leftCol = make([]Datum, o.batchSize)
        o.rightCol = make([]Datum, o.batchSize)
        o.outBatch = make([]Datum, o.batchSize)
    }
    return nil
}
```

### Next (batch path)

```go
func (o *filterOp) Next() (TupleSlot, error) {
    if !o.canBatch {
        return o.nextPerRow() // existing path
    }
    for {
        // Emit next survivor from current batch if any.
        if o.surviving < len(o.batchRows) {
            s := o.batchRows[o.surviving]
            o.surviving++
            return s, nil
        }
        // Refill: pull up to batchSize rows from input.
        o.batchRows = o.batchRows[:0]
        for i := 0; i < o.batchSize; i++ {
            slot, err := o.input.Next()
            if err == EOF {
                break
            }
            if err != nil {
                return nil, err
            }
            // Materialize each row before adding to batch
            // — necessary because the input's per-batch
            // arena may Reset before we emit.
            ms := slot.Materialize()
            o.batchRows = append(o.batchRows, ms)
        }
        if len(o.batchRows) == 0 {
            return nil, EOF
        }
        // Evaluate predicate on the batch.
        if err := o.evalPredicateBatch(); err != nil {
            return nil, err
        }
        o.surviving = 0
    }
}
```

### evalPredicateBatch

For predicates of shape `cref OP const` or
`cref1 OP cref2` (the most common TPC-H filter shapes),
the implementation is:

```go
func (o *filterOp) evalPredicateBatch() error {
    n := len(o.batchRows)
    if n == 0 {
        return nil
    }
    // Walk the predicate AST recursively, building
    // a survivor mask via column-array eval.
    mask, err := o.evalExprBatchMask(o.plan.Predicate, n)
    if err != nil {
        return err
    }
    // Compact survivors in-place.
    out := o.batchRows[:0]
    for i := 0; i < n; i++ {
        if mask[i] {
            out = append(out, o.batchRows[i])
        }
    }
    o.batchRows = out
    return nil
}

func (o *filterOp) evalExprBatchMask(e planner.Expr, n int) ([]bool, error) {
    switch x := e.(type) {
    case *planner.BinaryOp:
        if x.Op == parser.OpAnd {
            lMask, err := o.evalExprBatchMask(x.Left, n)
            if err != nil { return nil, err }
            rMask, err := o.evalExprBatchMask(x.Right, n)
            if err != nil { return nil, err }
            for i := 0; i < n; i++ { lMask[i] = lMask[i] && rMask[i] }
            return lMask, nil
        }
        if x.Op == parser.OpOr {
            // similar; OR
        }
        // Comparison / arithmetic: build operand columns
        // and call evalBinaryBatch.
        leftCol := o.buildOperandColumn(x.Left, n)
        rightCol := o.buildOperandColumn(x.Right, n)
        if err := evalBinaryBatch(x.Op, leftCol, rightCol, o.outBatch[:n]); err != nil {
            return nil, err
        }
        mask := make([]bool, n)
        for i := 0; i < n; i++ {
            mask[i] = !o.outBatch[i].IsNull() && o.outBatch[i].BoolValue()
        }
        return mask, nil
    }
    // Fallback: per-row eval (shouldn't happen if
    // canVectoriseExpression returned true).
    mask := make([]bool, n)
    for i := 0; i < n; i++ {
        v, err := evalExprSlot(e, o.batchRows[i], o.ctx)
        if err != nil { return nil, err }
        mask[i] = !v.IsNull() && v.BoolValue()
    }
    return mask, nil
}

// buildOperandColumn populates leftCol or rightCol for
// the n batch rows, filling from a ColumnRef or constant.
func (o *filterOp) buildOperandColumn(e planner.Expr, n int) []Datum {
    switch x := e.(type) {
    case *planner.ColumnRef:
        for i := 0; i < n; i++ {
            o.leftCol[i] = o.batchRows[i].Get(x.Index)
        }
        return o.leftCol[:n]
    case *planner.IntegerConst:
        d := Datum{Kind: KindInt, Int: x.Value}
        for i := 0; i < n; i++ { o.leftCol[i] = d }
        return o.leftCol[:n]
    // ... other constant types ...
    }
    // Should not reach here if canVectoriseExpression
    // returned true.
    return nil
}
```

(The dual `leftCol` / `rightCol` allocation in the
struct is a simplification — the actual implementation
needs separate buffers for nested ANDs to avoid
clobbering. M0076 may add proper SSA-style buffer
allocation. For the initial commit, allocate-per-call
is acceptable since the allocation is amortised across
~40 rows.)

## Verification

Pre-commit gate (M0075 standard):
- 21-q SF=1 sweep PASS: zero row-count change. Filter
  behaviour must be byte-for-byte identical to per-row.
- Q5 / Q12 / Q13 wall-time delta ≤ 110 % of post-Commit
  C. Best case: 30-50 % drop on filter-heavy queries.
- Q9 ≥ 7 (mode-1 baseline).

New tests in `internal/executor/filter_batch_test.go`:
- `TestFilterBatchEquivalencePerRow` — synthesize a
  table with ~100 rows; compare batch vs per-row
  survivor sets; must match exactly.
- `TestFilterBatchNullThreeValuedLogic` — NULL-mixed
  predicates; batch path must propagate NULL → false
  in the survivor mask.
- `TestFilterBatchEligibilityFallback` — predicate
  with FuncCall / SubqueryExpr falls back to per-row
  (`canVectoriseExpression` returns false).
- `TestFilterBatchAndOrCombination` — `(a < 5 AND
  b > 10) OR c = 7`; ANDed and ORed sub-masks combine
  correctly.
- `TestFilterBatchSmallBatch` — batch with < batchSize
  rows (input EOF mid-batch) emits all survivors before
  returning EOF.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Arena lifetime: survivors retained past input's next batch's arena Reset → garbage read | `Materialize()` each row before adding to batch — copies arena bytes out (via cloneRowOwned in M0073-0004). |
| R2 | Batch path bitmask emit overhead exceeds eval savings | Profile per-page; can be reverted independently of M0074-0001 infra. |
| R3 | Eligibility detector misclassifies → vectorised path runs on subquery / FuncCall → side-effect bug | Conservative whitelist in canVectoriseExpression; recursive walk; default to per-row. |
| R4 | NULL three-valued logic differs between batch and per-row | Pin via TestFilterBatchNullThreeValuedLogic against per-row baseline. |
| R5 | Column-major buffer aliasing in nested ANDs (leftCol clobbered by sub-call) | Initial implementation: allocate per-call per-operand buffers (allocation amortised over batch); M0076 SSA-style buffer pool. |

## Migration plan

Single commit (Commit D in M0075):
1. Add canBatch + buffers fields to filterOp.
2. Detect vectorisable predicate at Open.
3. Add fillBatch + evalPredicateBatch + evalExprBatchMask
   + buildOperandColumn methods.
4. Land tests.
5. Verify gate: SF=1 sweep + filter_batch_test PASS.
6. Profile Q5/Q12/Q13 to confirm wall-time delta.

If wall time regresses on any query: revert the wiring;
keep the `canBatch` flag default-false; M0076 re-attempts
with refined eligibility logic.

## References

- `internal/executor/expr_batch.go` — `evalBinaryBatch`,
  `canVectoriseBinary`, `canVectoriseExpression`
  (M0074-0001).
- `internal/executor/operators.go::filterOp` — current
  per-row impl (target of M0075-0004).
- `internal/planner/plan.go::Filter` — planner-bound
  Filter node.
- `internal/executor/slot.go::MaterializedSlot.Materialize` —
  retention-boundary copy used by R1 mitigation.
