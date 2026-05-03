# Milestone 0039 — Fix Planner Column-Index Alignment for Correct Join Results

**Status:** planned
**Depends on:** M0038 (multi-way hash join), M0034 (bushy DP plan), M0033 (subquery unnesting)
**Drives:** Correct query results (currently 9/22 TPC-H queries return 0 rows). Enable the MultiHashJoin operator (M0038) to use all N join keys (currently 3/4 for Q2). Unlock the memory-reduction benefits of M0036‑M0038 by producing correct plans.

## Context

The bushy DP (M0034) and subquery unnesting (M0033) pipeline constructs join
trees where `ColumnRef.Index` values in `Join.LeftKey` / `Join.RightKey` (and
similarly in `Sort`, `Aggregate`, `Project` nodes) are misaligned with the
local operator output schema. The result:

- **9/22 TPC-H queries return 0 rows** — join comparisons match no rows because
  keys point to the wrong physical columns.
- **MultiHashJoin keys are incomplete** — chain detection finds a 5‑table chain
  for Q2 but can only resolve 3 of 4 keys. The missing key (‑part‑partsupp)
  causes the MultiHashJoin to silently drop join conditions.
- **Memory‑reduction benefits of M0036‑M0038 are unrealisable** — without
  correct join results, the per‑query RSS can not be meaningfully measured or
  improved; the MultiHashJoin operator may still accumulate data for
  unresolved keys.

After the ColumnRef indices are fixed:

| Metric | Current | Target |
|--------|---------|--------|
| TPC-H queries errored | 0 (M0038‑0003) | 0 |
| TPC-H queries returning 0 rows | 9 / 22 | **0** |
| MultiHashJoin keys resolved (Q2) | 3 / 4 | **4 / 4** |
| Q2 peak RSS (SF=1 partial) | ≥ 24.8 GB | **≤ 10 GB** |

## Root Causes Identified

### 1. `pushPredicatesIntoCrossJoins` uses global indices (fallback path)

When the bushy DP does not run (disconnected components, missing stats),
the planner falls back to `pushPredicatesIntoCrossJoins` (`planner.go:348`).
This function promotes CROSS joins to hash joins by calling
`splitEqualityForHash`, which returns the WHERE‑clause ColumnRef nodes
**as‑is** — with global‑level indices (i.e. relative to the entire FROM‑clause
schema).

These global indices are invalid for the local Join schema (which is the
narrower concatenation of its two children's outputs). The executor then
accesses wrong row positions, comparing unrelated columns.

**Fix required:** After extracting the left/right key expressions, remap
their ColumnRef indices from global scope to the Join's local output schema.

### 2. ColumnRef indices in unnest‑generated nodes are stale

The subquery unnest pass (`internal/planner/unnest.go`) creates new
`Join` / `Aggregate` / `Project` nodes with `ColumnRef` pointers from the
original (pre‑unnest) plan. When bushy DP later rewrites the plan tree,
the ColumnRef indices in the unnest‑created nodes are **not** remapped
to match the new plan shape. The chain‑detection scope guards prevent
walking into unnest‑created `Aggregate` subtrees, but the parent Join
keys must still align with the rewritten child's output schema.

### 3. `buildJoinFromDP` ColumnRef remapping (partially fixed)

`remapKeyToSubset` (`bushy.go:473`) had a bug where the global column
offset only incremented for tables **in** the subset. This was **fixed**
in M0038‑0001, resolving the most obvious out‑of‑range indices for
non‑contiguous subsets. However, the E2E verification revealed that
some join key indices remain wrong (e.g. the `part‑partsupp` edge in Q2
shows indices 9/9 instead of the expected 3/4). Further investigation is
needed to determine whether the remaining error originates in
`buildJoinFromDP` itself or in a subsequent plan‑rewriting pass.

## Required Design Docs

1. `docs/design/0039-0001-planner-column-ref-fix.md` — Detailed analysis of
   the three root causes above, proposed remediation for each code path,
   verification strategy (TPC-H parity matrix + Q2 MultiHashJoin key count),
   and integration notes for `pushdown.go` / `bushy.go` / `unnest.go`.

## Definition of Done

1. **`pushPredicatesIntoCrossJoins` remap**: After picking the left/right key
   expressions, remap each `ColumnRef.Index` from global scope to the Join's
   local output schema (left‑width offset logic).

2. **Unnest‑pass ColumnRef alignment**: After bushy DP rewrites the plan tree
   (or after chain detection), verify that any `ColumnRef` nodes in
   unnest‑generated Join / Aggregate keys are still within bounds of their
   operand schemas. If not, remap.

3. **`buildJoinFromDP` follow‑up**: Confirm the M0038‑0001 `remapKeyToSubset`
   fix resolves all bushy‑DP‑originated column offsets. If a residual case
   remains (e.g. the part‑partsupp edge), fix it.

4. **TPC‑H parity**: 0 queries goopg‑errored, **0 queries returning 0 rows**
   (3→) (all 22 queries must return the expected row counts). Currently 13
   identical + 9 divergent‑0‑rows; target is ≥ 20 identical (precision
   differences for Q1/Q14 are acceptable).

5. **MultiHashJoin key completeness**: For the Q2 simplified plan, all 4 join
   keys are resolved (currently 3/4). The MultiHashJoin node correctly links
   every table in the chain.

6. **Batch configuration**: All tests use `shared_buffers=2048MB` (2 GiB) and
   `GOMEMLIMIT=20GiB` so the heap pressure is reproducible.

7. **Regression**: All 22 TPC‑H queries build and execute. Existing binary
   joins are unaffected. No new errors or row‑count regressions.

## Reference

- `internal/planner/bushy.go` — bushy DP enumeration, `remapKeyToSubset`,
  `buildJoinFromDP`, chain detection (`collectMultiHashTables`,
  `rewriteMultiWayChain`)
- `internal/planner/planner.go` — `planSelect`, `pushPredicatesIntoCrossJoins`
  at line 348, `splitEqualityForHash`
- `internal/planner/pushdown.go` — `pushOneConjunct`, `classifyConjunctSide`
- `internal/planner/unnest.go` — subquery unnesting, `clonePlanReplacingOuter`,
  `walkPlanExprs`
- `internal/executor/expr.go:305` — `compareDatum` (cross‑kind guard, now with
  fallback from M0038‑0003)
- `analysis/0038-fix-compareDatum-cross-kind.md` — documentation of the
  executor‑level safety net
- `analysis/tpch-power-test-0038-report.md` — SF=1 power test failure analysis
