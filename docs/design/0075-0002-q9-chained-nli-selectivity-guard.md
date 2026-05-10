# Design 0075-0002 — Q9 chained-NLI rebind WITH per-outer selectivity guard

**Milestone:** M0075-0002
**Status:** **PARTIAL — selectivity guard landed
2026-05-10; M0072-0002 hang prevented but Q9 row-count
target NOT met.** Empirical result: the guard correctly
rejects the unsafe rebind that hung Q9 in the M0072-0002
attempt; Q9 remains at the bimodal mode-1 baseline (7
rows / 239 s) instead of regressing. Q21 single-NLI win
preserved at 381 rows. The 100-row stretch target for
M0075-0002 is NOT achieved — that requires unlocking
GOOD rebinds that the current threshold + NDistinct-
based estimate doesn't recognise. M0076-0005 candidate:
combine the guard with the M0075-0001 equivalence-class
synthesis (also currently disabled) so the join graph
has additional edges; AND/OR refine the cardinality
estimate to differentiate selective from non-selective
columns more accurately.
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0071-0009 — `SchemaColumn.SourceTableIdx`,
`findColumnIndexByNameAndSource` (`bushy.go:1610`);
M0074-0002 (commit `bdee869`) — `VirtualCol(col)`
accessor + defensive bounds check; M0072-0002 design
(reverted) — context for the failure mode.

## Context

Q9 is bimodal at M0074-final: 7 rows / ~212 s
(mode-1 baseline) vs 175 rows / 1030 s (mode-2 correct).
M0072-0002 attempted the planner-side rebind for chained-
NLI but was reverted because the rebind landed on a
high-cardinality column → per-outer match-set explosion
→ Q9 hung at 380-600 s with 0 rows.

M0074-0002 landed forward-compat infrastructure but did
NOT attempt the rebind. The structural piece missing is
**per-outer selectivity guard** that aborts the rebind
when the resolved column would explode the match-set.

Research (Explore agent, 2026-05-10):
- M0064 gate at `nl_index_join.go:399` already fires for
  `*MultiHashJoin` outers (uses `findUniqueColumnIndex`
  by Name only; the M0072-0002 attempt extended it for
  `*NestedLoopIndexJoin` outers using
  `findColumnIndexByNameAndSource`).
- The M0072-0002 implementation correctly resolved
  `(Name, SourceTableIdx)` to a column — the failure
  was that the resolved column had high cardinality at
  runtime (chained-NLI shape with self-joins where
  multiple aliases match the same supplier or part).
- No existing per-outer selectivity check infrastructure;
  must be added.
- `EstimateRows` at `cardinality.go::EstimateRows` is
  the per-node card estimate dispatcher.

## Goals

- Extend the rebind block at `nl_index_join.go:399` to
  fire for `*NestedLoopIndexJoin` outers (M0072-0002's
  goal).
- Use `findColumnIndexByNameAndSource` (M0071-0009)
  for SourceTableIdx-aware disambiguation.
- Add per-outer selectivity guard:
  ```
  perOuterEst = EstimateRows(inner-after-rebind) /
                EstimateRows(outer)
  if perOuterEst > nliSelectivityThreshold (default 100):
      ABORT rebind, keep original index
  ```
- **Q9 ≥ 100 rows DETERMINISTICALLY** post-Commit F
  (≥ 175 stretch).
- Q21 still = 381 rows (single-NLI invariant from
  M0071-0009 must not regress).
- All other queries: zero row-count change.

## Non-goals

- **Wall-time compression target** — per Phase-5
  handover, M0075-0002 targets determinism, not
  wall time. Q9 may still take > 1100 s.
- **Threshold tuning beyond 100** — initial constant;
  M0076 candidate for adaptive threshold.
- **MCV histogram improvements** — selectivity guard
  accuracy is bounded by current cardinality estimates.

## Proposed implementation

### Rebind block extension at `nl_index_join.go:399`

```go
// Old (M0064 + M0074-0002 partial):
_, outerIsMHJ := outerNode.(*MultiHashJoin)
if outerIsMHJ {
    rebindIndexKeysByName(scan, outerNode.Output())
}

// New (M0075-0002):
_, outerIsMHJ := outerNode.(*MultiHashJoin)
_, outerIsNLI := outerNode.(*NestedLoopIndexJoin)
if outerIsMHJ || outerIsNLI {
    proposed := proposeRebind(scan, outerNode.Output())
    if proposed != nil &&
       passesSelectivityGuard(proposed, outerNode, scan, cat) {
        applyRebind(scan, proposed)
    }
}
```

### proposeRebind

Walks the IndexScan's Key / LowKey / HighKey expressions
to find ColumnRefs, resolves each via
`findColumnIndexByNameAndSource`. Returns nil if all
indices already match (no rebind needed) or if any
ColumnRef cannot be resolved unambiguously.

```go
type RebindPlan struct {
    keyMappings   map[*ColumnRef]int // colref → new Index
    estimatedRows int64               // post-rebind inner
}

func proposeRebind(
    scan *IndexScan,
    schema Schema,
) *RebindPlan {
    plan := &RebindPlan{keyMappings: make(map[*ColumnRef]int)}
    needsRebind := false
    for _, expr := range scan.AllKeyExprs() {
        walk(expr, func(node Expr) {
            cref, ok := node.(*ColumnRef)
            if !ok {
                return
            }
            newIdx, ok := findColumnIndexByNameAndSource(
                schema, cref.Name, cref.SourceTableIdx, 0)
            if !ok {
                return // can't resolve; skip
            }
            if newIdx != cref.Index {
                plan.keyMappings[cref] = newIdx
                needsRebind = true
            }
        })
    }
    if !needsRebind {
        return nil
    }
    return plan
}
```

### passesSelectivityGuard

The critical safety check:

```go
const nliSelectivityThreshold = 100.0

func passesSelectivityGuard(
    proposed *RebindPlan,
    outerNode Node,
    scan *IndexScan,
    cat Catalog,
) bool {
    outerEst := EstimateRows(outerNode, cat)
    if outerEst <= 0 {
        outerEst = 1
    }
    
    // Estimate the inner IndexScan rows post-rebind.
    // The rebind changes which column drives the index
    // probe, which can change the per-probe selectivity.
    innerEstAfter := estimateInnerAfterRebind(scan, proposed, cat)
    
    perOuter := float64(innerEstAfter) / float64(outerEst)
    return perOuter <= nliSelectivityThreshold
}

func estimateInnerAfterRebind(
    scan *IndexScan,
    proposed *RebindPlan,
    cat Catalog,
) int64 {
    // Apply the rebind hypothetically to a clone of
    // scan.Key / LowKey / HighKey, then compute
    // selectivity via the existing pipeline.
    clonedScan := cloneIndexScan(scan)
    applyRebind(clonedScan, proposed)
    return EstimateRows(clonedScan, cat)
}
```

### applyRebind

Mutates the original scan's Key / LowKey / HighKey
ColumnRef indices according to `proposed.keyMappings`.

```go
func applyRebind(scan *IndexScan, proposed *RebindPlan) {
    for _, expr := range scan.AllKeyExprs() {
        walk(expr, func(node Expr) {
            cref, ok := node.(*ColumnRef)
            if !ok {
                return
            }
            if newIdx, hit := proposed.keyMappings[cref]; hit {
                cref.Index = newIdx
            }
        })
    }
}
```

### Threshold rationale

The M0072-0002 postmortem documented that the failure
mode produced per-outer match-sets in the thousands
(causing the cancel budget to be exceeded). 100 is
conservative — well below the failure threshold but
permissive enough to allow legitimate rebinds where
the resolved column has reasonable per-outer
selectivity.

If 100 turns out too restrictive (Q9 stays at 7 even
with rebind because the selectivity guard rejects the
correct rebind), the threshold can be raised in a
follow-up; the safe regression mode (Q9 = 7) is
preserved.

## Verification

Pre-commit gate (M0075 STRICTER post-F):
- **Q9 ≥ 100 rows DETERMINISTICALLY** (5 consecutive
  runs at SF=1 must all return ≥ 100).
- Q21 = 381 rows (single-NLI invariant from M0071-0009
  preserved).
- Q12 = 2, Q13 = 35, Q22 = 7.
- 21-q sweep: row-count parity for all other queries.

New tests:
- `internal/planner/nl_index_join_test.go`:
  - `TestM0075ChainedNLIRebindAccepts` — chained-NLI
    shape where rebind would land on a low-cardinality
    column (e.g., orders.o_orderdate after filter);
    selectivity guard passes; rebind applied.
  - `TestM0075ChainedNLIRebindRejectsHighCardinality`
    — rebind would land on high-cardinality column
    (mirrors M0072-0002 failure shape); selectivity
    guard fails; rebind aborted; original index
    preserved.
  - `TestM0075ChainedNLIRebindThresholdBoundary` —
    pin behaviour at exactly the threshold.
- `internal/testutil/tpch/nli_parity_test.go::
  TestNLIResultParityCompositeKey` — extend assertion
  to `rows ≥ 100`.
- `internal/testutil/tpch/q9_pin_test.go` (NEW) — Q9
  cluster-backed test pinning ≥ 100 rows
  DETERMINISTICALLY at SF=1.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Selectivity guard threshold (100) is wrong → Q9 stays at 7 OR regresses | Pre-commit gate runs Q9; hard floor at ≥ 100 rows. If broken, raise threshold (or revert and split per-Kind). |
| R2 | EstimateRows inaccurate (no MCV histogram on the relevant columns) | Default-row estimates fall back to defaultEqSelectivity (0.005) — conservative; under-estimate triggers guard rejection (safe — keeps original index, Q9 stays at 7 baseline). |
| R3 | Q21 (single-NLI win from M0071-0009) regresses | Pre-commit gate runs Q21 with hard floor at 381; M0071-0009 invariant test extended. |
| R4 | The rebind landing on a low-cardinality column at runtime that the cardinality estimator misjudged as high → guard rejects valid rebind | Same as R2 — conservative miss is safe; Q9 stays at baseline. |
| R5 | M0072-0002 hang re-occurs (cancel timeout exceeded) | Selectivity guard is the structural defence; if it fails to prevent the hang, the test surface should catch it within 1100 s timeout — revert immediately. |
| R6 | Other chained-NLI shapes (Q21 isn't chained-NLI; only Q9 is) regress unexpectedly | 21-q sweep parity for all queries; specifically pin Q21=381. |

## Migration plan

Single commit (Commit F in M0075):
1. Add `proposeRebind`, `passesSelectivityGuard`,
   `applyRebind`, `estimateInnerAfterRebind`,
   `cloneIndexScan` helpers.
2. Extend `nl_index_join.go:399` rebind block with
   `outerIsNLI` branch.
3. Land tests.
4. Verify gate: Q9 ≥ 100 + 21-q sweep parity.

If Q9 falls below 100 rows, hangs, or drops to 0
(M0072-0002 failure mode): **revert immediately**.
Document the threshold as the M0076 carry-forward
("100 was wrong; need adaptive logic OR per-Kind
guards"). Do NOT chase further in this session.

## References

- `docs/design/0072-0002-chained-nli-rebind.md` —
  reverted attempt; carries the failure-mode
  postmortem.
- `internal/planner/nl_index_join.go:399` — M0064
  rebind block (extension target).
- `internal/planner/bushy.go:1610` —
  `findColumnIndexByNameAndSource` (M0071-0009).
- `internal/planner/cardinality.go::EstimateRows` —
  per-node estimation dispatcher.
- `internal/testutil/tpch/nli_parity_test.go:102-107`
  — Q9 cluster test.
- `internal/executor/slot.go::VirtualSlot.VirtualCol`
  — M0074-0002 forward-compat accessor.
