# Design 0072-0002 — Chained-NLI IndexScan key rebind (Q9 row-count fix)

**Milestone:** M0072-0002
**Status:** **DEFERRED to M0073** (implementation attempt
landed on `gc-oriented-refactor` produced a runtime
explosion — Q9 cancelled at 600s with no rows; reverted
2026-05-09. Root cause: even with
`findColumnIndexByNameAndSource`, the IndexScan key
ColumnRef in the chained-NLI shape lands on a low-
selectivity column at runtime, producing a per-outer
match-set that doesn't fit within the cancel budget. The
fix requires full virtual-coord propagation so each slot
addresses its own `(sourceIdx, sourceCol)` mapping rather
than a flat `ColumnRef.Index`.)
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (revert landed before
implementation commit; retained as design history)
**Depends on:** M0071-0009
(`SchemaColumn.SourceTableIdx`),
M0072-0001 (slot-aware BindOuter — recommended but not strictly
required), M0073 (virtual-coord propagation — new milestone).

## Context

TPC-H Q9 returns 7 rows on goopg SF=1 (target ~175). The
defective shape is:

```
Sort
└─ Aggregate
   └─ Project
      └─ NestedLoopIndexJoin     ← chained NLI (the inner side
         outer: NestedLoopIndexJoin    is itself an NLI)
         inner: IndexScan
                 Key: ColumnRef(idx=N)  ← stale schema position
```

The planner-bound `ColumnRef.Index` for the inner IndexScan's
Key expression is computed against the **outer's original
schema**, before any intermediate operator reordering. When
the outer is itself an NLI, runtime row layout matches the
schema (`outer.Output() ++ inner.Output()` per M0072-0001's
slot composition), but the planner-bound index may have been
fixed at a moment in plan rewriting when the schema was
different. The result: `slot.Get(N)` returns a Datum from the
wrong column at runtime.

The M0064 defensive gate at `internal/planner/nl_index_join.go:399`
mitigates this for MHJ outers (where OID-sort always
reorders). The gate fires `_, outerIsMHJ := outerNode.(*MultiHashJoin)`
and rebinds the IndexScan Key.ColumnRef.Index by Name against
`outerNode.Output()`. For NLI outers, no rebind happens —
the comment at l.390-398 explicitly says "downstream
remappers leave indices in pre-rewrite order intentionally."

M0067-0003 attempted to remove the gate naively (let all
outers rebind) and Q9 went 7 → 1 rows because `findUniqueColumnIndex`
by Name alone can't disambiguate when multiple aliases share
a column name (e.g. `l_suppkey` appears twice in Q9's
chained `lineitem` references).

**M0071-0009 introduced `SchemaColumn.SourceTableIdx`** and
the disambiguation helper
`findColumnIndexByNameAndSource(schema, name, sourceTableIdx,
offset)`. This gives a stable rebind contract that
M0067-0003 didn't have: when the IndexScan's Key.ColumnRef
carries `SourceTableIdx ≠ 0`, the rebind can resolve to the
correct column even with duplicate Names.

## Goals

- Extend the `nl_index_join.go:399` rebind block to fire for
  `*NestedLoopIndexJoin` outers, not just `*MultiHashJoin`.
- Use `findColumnIndexByNameAndSource` (M0071-0009) for the
  rebind so duplicate Names are disambiguated correctly.
- Pin Q9 row count ≥ 90 (target ~175) without regressing
  any other query.

## Non-goals

- **Full virtual-coord propagation through slot.** That would
  change `ColumnRef.Index` semantics (move from "flat schema
  position" to "(sourceIdx, sourceCol)") and rewire the
  entire executor's column-resolution layer. M0073 candidate.
- **Removing the `outerIsMHJ` gate entirely.** This design
  *adds* `outerIsNLI` to the same block; it does not delete
  the existing MHJ branch. The MHJ rebind is independently
  validated.
- **Q9 ≥ 175 rows guarantee.** The target is ~175 but Q9's
  ground truth depends on the rebind landing on the
  semantically correct column for every chained-NLI shape.
  ≥ 90 rows is the gate; 175 is the aspiration.

## Proposed interface

### nl_index_join.go:399 extension

```go
// Old (M0064):
if _, outerIsMHJ := outerNode.(*MultiHashJoin); outerIsMHJ {
    rebindIndexKeysByName(scan, outerNode.Output())
}

// New (M0072-0002):
_, outerIsMHJ := outerNode.(*MultiHashJoin)
_, outerIsNLI := outerNode.(*NestedLoopIndexJoin)
if outerIsMHJ || outerIsNLI {
    rebindIndexKeysByNameAndSource(scan, outerNode.Output())
}
```

### rebindIndexKeysByNameAndSource

```go
// rebindIndexKeysByNameAndSource walks the IndexScan's Key /
// LowKey / HighKey expressions and re-resolves every
// ColumnRef.Index against `schema` using
// findColumnIndexByNameAndSource. ColumnRefs whose
// SourceTableIdx is 0 (unknown / derived) fall back to the
// legacy Name-only lookup (`findUniqueColumnIndex`). The
// rebind is idempotent: when an Index already matches the
// resolved position, it's a no-op.
//
// Failure mode: when the rebind cannot resolve unambiguously,
// the function leaves the index unchanged (matches the
// existing M0064 contract for the MHJ rebind).
func rebindIndexKeysByNameAndSource(
    scan *IndexScan, schema Schema)
```

The implementation walks the Expr tree (the same walker
pattern used by `bushy.go::predRebind` at l.1708-1733) and
applies the rebind to every `ColumnRef`. The
`SourceTableIdx`-aware lookup uses
`findColumnIndexByNameAndSource` from `bushy.go`
(M0071-0009).

### Disambiguation invariant

For Q9 specifically:
- The chained NLI outer is `lineitem l1 NLI lineitem l2`
  (or similar). Each `lineitem` alias gets its own
  `SourceTableIdx` at bind time per M0071-0009's
  `planFromClause` threading.
- The IndexScan's Key references `l_suppkey` (or whichever
  joined column). `ColumnRef.SourceTableIdx` was set at
  bind time to the SourceTableIdx of the relevant alias.
- The rebind resolves `(Name="l_suppkey", SourceTableIdx=K)`
  against `outerNode.Output()` — finds the unique column
  that matches both, even when multiple aliases share the
  Name.

## Migration plan

Single-stage: extend the gate condition + introduce the new
rebind helper in one commit (Commit C per the M0072 plan).
The behaviour change is gated behind the new `outerIsNLI`
condition; pre-existing MHJ rebind is untouched.

## Verification

**Pre-commit gate** (per M0072 plan): Q12=2, Q13=35, Q21≥100,
21-query sweep. Must hold.

**Q9 specific:**
- **EXPLAIN Q9 before/after**:
  ```sh
  ./tpch-runner --queries=9 --explain
  ```
  Capture the chained-NLI's IndexScan Key.ColumnRef.Index
  before and after the change. The rebind should produce a
  different, name-resolved index that matches the runtime
  layout.
- **Q9 row count gate**:
  - Required: `rows ≥ 90`.
  - Hard floor: `rows ≥ 7` (the current baseline). If the
    rebind drops Q9 below 7, **revert immediately** — that
    is the M0067-0003 regression mode.
  - Target: `rows ~ 175`.
- **`TestNLIResultParityCompositeKey`** at
  `internal/testutil/tpch/nli_parity_test.go:102-107` —
  cluster-backed regression test; extend assertion to
  `rows ≥ 90`.
- **`TestM0072ChainedNLIRebind`** (NEW) at
  `internal/planner/nl_index_join_test.go` — synthesises
  a chained-NLI plan (two `lineitem` aliases) and asserts
  the inner IndexScan's Key.ColumnRef.Index is rebound to
  the correct outer-schema position via
  `findColumnIndexByNameAndSource`.

## Implementation outcome (2026-05-09)

The implementation landed on a feature branch with
`outerIsNLI || outerIsMHJ` extending the rebind block at
`internal/planner/nl_index_join.go:399`, using
`findColumnIndexByNameAndSource` from M0071-0009 plus the
SourceTableIdx-aware "already bound" guard. Pre-commit gate
result:

- Q12=2 ✓
- Q13=35 ✓
- Q21=381 ✓
- **Q9 cancelled at 380s → cancelled again at 600s; runtime
  observed >18 minutes in tpch-runner before kill.** No
  result emerged. The runtime did not crash; the inner-loop
  `ctx.Err()` checks fire as expected, but the per-outer
  match-set produced after rebind is large enough to exceed
  any reasonable cancel budget.

Root cause: the SourceTableIdx-aware lookup correctly
disambiguates self-join Name collisions, but the rebind
target column is high-cardinality at runtime (chained-NLI
shape with `lineitem` self-joins where multiple aliases
match the same supplier or part). The IndexScan probe
expands per-outer instead of narrowing.

The change was reverted; M0072-0002 is documented here as
history. The cleaner fix is **M0073: full virtual-coord
propagation through SlotView** — each slot reads
`(sourceIdx, sourceCol)` from its own per-operator mapping,
making schema position structurally equivalent to runtime
position regardless of intermediate operator reorderings.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Q9 7→1 regression returns | The fix uses `findColumnIndexByNameAndSource`, which M0067-0003 didn't have. Pre-commit gate runs Q9; hard floor at ≥ 7 rows. If broken, revert. **Outcome: triggered the cancel-budget regression mode (Q9 hung); reverted, deferred to M0073.** |
| R2 | Q21 (M0071-0009 win) regression — Q21 also relies on the predRebind path | The rebind helper extends `findColumnIndexByNameAndSource` (the same utility M0071-0009 uses). Q21 must stay at ≥ 100 rows; gate. |
| R3 | Chained NLI 3+ deep — the rebind only walks one level of outer | The walker is recursive on NestedLoopIndexJoin outers (the outer's outer is an NLI too); same `outerIsNLI` test fires. Pin via `TestM0072ChainedNLIRebind` with a 3-deep synthesised plan. |
| R4 | Other queries silently regress (the M0064 gate was added for a reason) | 21-query sweep at every commit; full row-count check; no >10% wall-time regression vs HEAD. |
| R5 | Subquery / OuterColumnRef expressions in IndexScan keys (rare) | Walker preserves OuterColumnRef and ParamRef without rebinding (mirrors `predRebind`). Only `ColumnRef` indices rebind. |

## References

- `docs/handover/2026-05-09-tpch-status-phase3.md` §3.2 —
  Q9 chained-NLI explanation.
- `internal/planner/nl_index_join.go:369-413` — M0064 rebind
  block (extension target).
- `internal/planner/bushy.go::findColumnIndexByNameAndSource`
  — M0071-0009 utility (reused).
- `internal/planner/bushy.go::reresolveJoinByName::predRebind`
  (l.1708-1733) — walker pattern reused.
- `internal/planner/source_table_idx_test.go` — pins
  M0071-0009 invariants; M0072-0002 must preserve.
- `internal/testutil/tpch/nli_parity_test.go:102-107` —
  cluster-backed Q9 test; M0072-0002 acceptance.
- `analysis/tpch-m0067-baseline-2026-05-08.md` — M0067-0003
  7→1 regression notes; the cautionary precedent.
