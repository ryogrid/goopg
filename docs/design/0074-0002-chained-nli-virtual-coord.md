# Design 0074-0002 — Chained-NLI virtual-coord propagation through SlotView

**Milestone:** M0074-0002
**Status:** **PARTIAL — conservative infrastructure landed
2026-05-10; planner-side rebind DEFERRED to M0075.**
The M0074 session landed the forward-compat surface
(`VirtualCol(col)` accessor on `*VirtualSlot`,
defensive bounds check in `evalExprSlot`'s ColumnRef
arm) but did NOT land the planner-side rebind that
fixes Q9's bimodal mode-1 (7 rows). Reasoning: the
M0072-0002 attempt at the same problem (with full
SourceTableIdx-aware rebind) caused a runtime explosion
(Q9 cancelled at 380-600 s with 0 rows produced). The
true structural fix needs evidence-based debugging
(EXPLAIN comparison + per-outer match-set analysis) that
this session couldn't perform. Q9 stays at the bimodal
mode-1 baseline (7 rows); the conservative
infrastructure makes future debugging easier (clear
error vs silent wrong reads). See M0075 candidate.
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0071-0009 (`SchemaColumn.SourceTableIdx`);
M0072-0001 (commit `c16f3f2`) — slot-aware BindOuter;
SUPERSEDES (does not depend on) M0072-0002 design
(reverted at 2026-05-09 as runtime-explosion risk).

## Context

Q9 in TPC-H is bimodal at M0073-final:
- mode-1: 7 rows / 220 s (silent FN — wrong row count
  but completes within budget)
- mode-2: 175 rows / 1030 s (correct but slow)

M0072-0001 fixed the row count mechanism via slot-aware
`BindOuter` for single-NLI cases. M0072-0002 attempted to
also fix the chained-NLI case (Q9 has `lineitem l1 NLI
lineitem l2 NLI ...`) via a planner-side rebind shortcut.
It was reverted because the rebind landed on a high-
cardinality column → selectivity collapsed → runtime
explosion at 380-600 s with 0 rows produced.

The structural defect: when a NestedLoopIndexJoin's outer
is itself an NLI, the outer's output is a `*VirtualSlot`
with per-column `(sourceIdx, sourceCol)` runtime
coordinates (slot.go:117-119). The inner IndexScan's
predicate-bound `ColumnRef.Index` is a flat schema
position from pre-rewrite. `evalExprSlot` reads
`slot.Get(flatIdx)` which on a VirtualSlot does
`s.cols[flatIdx]` indirection — but the planner-bound
flat index doesn't match the runtime virtualCol layout
in chained-NLI scenarios where the outer's schema was
re-composed during planning.

VirtualSlot already has the right machinery
(slot.go:128-130):
```go
func (s *VirtualSlot) Get(col int) Datum {
    c := s.cols[col]
    return s.sources[c.sourceIdx].Get(int(c.sourceCol))
}
```

The fix is making sure the planner emits the correct
`col` index that resolves to the right `(sourceIdx,
sourceCol)` pair for chained-NLI.

## Goals

- Q9 produces **175 rows DETERMINISTICALLY** at every
  query execution. No more bimodal behaviour.
- Q21 still produces 381 rows (single-NLI invariant
  from M0071-0009 must not regress).
- All other queries: zero row-count change.
- Wall-time compression is OUT OF SCOPE per user
  selection 2026-05-10. Q9 may stay > 1100 s; only
  determinism + correctness matters.

## Non-goals

- **Re-attempting M0072-0002's rebind shortcut.** The
  rebind landed on a high-cardinality column which
  caused runtime explosion. M0074-0002 takes a different
  structural approach.
- **Wall-time compression.** Carry to M0075.
- **Refactoring single-NLI BindOuter.** M0072-0001's
  fix stays untouched.

## Proposed implementation

### Two-part fix

**Part 1 — Executor-side virtualCol resolution at
`evalExprSlot` ColumnRef boundary:**

Today's evalExprSlot (expr.go:100-112) reads
`slot.Get(x.Index)` whether the slot is a `rowSlotView`
or a `*VirtualSlot`. For chained-NLI cases, the planner
must communicate the target `(sourceIdx, sourceCol)`
explicitly so the executor doesn't have to guess.

Approach: extend `*VirtualSlot` with a `Resolve(col int)
virtualCol` accessor (returns `s.cols[col]`); the
executor evaluates ColumnRef the same way it does today
(via Get), but the planner is now responsible for ensuring
`x.Index` is the runtime position in the VirtualSlot's
cols array — not the pre-rewrite flat schema position.

**Part 2 — Planner-side ColumnRef binding for chained-NLI:**

When the inner IndexScan's predicate references a column
that originates from a chained-NLI outer, the planner
must:
1. Walk the outer's runtime composition (NLI joinBuf
   schema = sources × per-source schemas).
2. Locate the source schema column via
   `(Name, SourceTableIdx)` from M0071-0009.
3. Map (source schema column) → `(sourceIdx, sourceCol)`
   in the outer's VirtualSlot composition.
4. Set the inner ColumnRef's `Index` to the position in
   the outer's flat virtualCol array that resolves
   through this `(sourceIdx, sourceCol)`.

Use `findColumnIndexByNameAndSource` from M0071-0009
(`internal/planner/bushy.go:1610-1618`) as the
disambiguation primitive.

### File modifications

- **`internal/executor/slot.go`** — add accessor:
  ```go
  // VirtualCol returns the runtime coordinate (sourceIdx,
  // sourceCol) for the col-th output column of this
  // VirtualSlot.
  func (s *VirtualSlot) VirtualCol(col int) (sourceIdx, sourceCol int) {
      c := s.cols[col]
      return int(c.sourceIdx), int(c.sourceCol)
  }
  ```

- **`internal/executor/expr.go::evalExprSlot`** — the
  ColumnRef arm doesn't need to change; `slot.Get(x.Index)`
  on a VirtualSlot already does the right thing. The fix
  is to ensure `x.Index` is correct. Add a defensive
  bounds check + clearer error message for chained-NLI
  scenarios that don't yet have the planner fix:
  ```go
  case *planner.ColumnRef:
      if slot == nil { ... }
      if vs, ok := slot.(*VirtualSlot); ok {
          if x.Index < 0 || x.Index >= vs.Width() {
              return Datum{}, &ExecError{
                  Code: "XX000", Pos: x.Pos(),
                  Message: fmt.Sprintf(
                      "column ref %s/%d out of VirtualSlot range %d (chained-NLI?)",
                      x.Name, x.Index, vs.Width()),
              }
          }
          return vs.Get(x.Index), nil
      }
      // existing rowSlotView + generic slot.Get paths
      ...
  ```

- **`internal/executor/operators_index.go::indexScanOp`**
  — at `BindOuter` time, when the outer slot is
  `*VirtualSlot`, record the outer's column composition
  so `lookupKey` can resolve any planner-bound ColumnRef
  index correctly. Add `o.outerVirtualMap []virtualCol`
  field; populate on BindOuter; consult in lookupKey
  if the planner emits a sentinel.

- **`internal/planner/nl_index_join.go`** (or wherever
  chained-NLI ColumnRef binding happens) — when an NLI's
  inner predicate `ColumnRef` references a column whose
  source table appears in the chained outer's schema,
  resolve the runtime index via
  `findColumnIndexByNameAndSource(outerSchema, name,
  sourceTableIdx)`.

### Algorithm sketch

```
resolveChainedNLIColumnRef(cr *planner.ColumnRef,
                            outerSchema planner.Schema)
    int /* runtime index in outer's VirtualSlot.cols */ {
    // cr.SourceTableIdx is set when cr originated from
    // a catalog table column (M0071-0009).
    if cr.SourceTableIdx == 0 {
        // Derived column; defer to existing flat-index
        // path. (Conservative: don't touch.)
        return cr.Index
    }
    // Find the runtime position of (cr.Name, cr.SourceTableIdx)
    // in the outer's recomposed schema.
    return findColumnIndexByNameAndSource(
        outerSchema, cr.Name, cr.SourceTableIdx)
}
```

## Verification

Pre-commit gate (M0074 stricter post-D):
- **Q9 = 175 rows DETERMINISTICALLY** (5 consecutive
  runs at SF=1 must all return 175).
- Q21 = 381 rows (single-NLI invariant from M0071-0009
  preserved).
- Q12=2, Q13=35, Q22=7.
- 21-query sweep: row-count parity for all other queries.
- New chained-NLI unit test PASS.

New tests:
- `internal/executor/chained_nli_virtual_coord_test.go` —
  synthesizes a 3-table chained-NLI workload with
  composite keys + self-join Name collision; pins the
  output row count.
- `internal/testutil/tpch/q9_pin_test.go` (NEW or
  extended) — Q9 row count = 175 deterministically; runs
  at SF=1 with cancel-after=1100 s. Use `t.Skip` if
  dataset isn't available, but validate row count when
  it is.

If Q9 stays at 7 (or worse, drops to 0 like M0072-0002):
revert immediately and re-evaluate. Treat as research
finding, not regression.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Planner-side resolution routes ColumnRef to wrong (sourceIdx, sourceCol) → row count drops to 0 (M0072-0002 repeat) | Q9 = 175 hard floor blocks commit; Q21 = 381 must remain. Test against single-NLI baseline first. |
| R2 | Resolution misses chained-NLI cases not exercised by Q9/Q21 | Conservative default: only emit virtualMap when outer schema is identifiable as VirtualSlot at plan time; fall back to flat-index for unrecognised shapes. |
| R3 | Executor reads stale coord during Rescan | Integration test exercises chained NLI with ≥ 100 outer rows + composite key. |
| R4 | Q9 wall time worsens beyond 1100 s | Best-effort target only; M0074-0002 mandate is determinism, not compression. Document if it lands. |

## Migration plan

Single commit (Commit D in M0074):
1. Add `VirtualCol(col)` accessor on `*VirtualSlot`.
2. Defensive bounds check + clearer error in
   `evalExprSlot` ColumnRef arm.
3. Planner-side `resolveChainedNLIColumnRef` helper
   using `findColumnIndexByNameAndSource`.
4. Wire into chained-NLI ColumnRef binding (likely in
   `internal/planner/nl_index_join.go` or
   `bushy.go`).
5. Optional: `outerVirtualMap` field on indexScanOp
   for runtime sanity checks (defensive, can be no-op
   in the common case).
6. Land tests.
7. Verify gate: Q9 = 175 + 21-query sweep parity.

If gate fails → REVERT IMMEDIATELY. Do not chase.

## References

- `docs/design/0072-0002-chained-nli-rebind.md` — the
  reverted approach. Read for the runtime-explosion
  failure mode.
- `internal/executor/slot.go:111-150` — VirtualSlot type.
- `internal/executor/operators_nljoin.go:127, 219, 224`
  — NLI driver + BindOuter dispatch.
- `internal/executor/operators_index.go:166, 379` —
  indexScanOp BindOuter signature + lookupKey call.
- `internal/planner/bushy.go:1610-1618` —
  findColumnIndexByNameAndSource (M0071-0009).
