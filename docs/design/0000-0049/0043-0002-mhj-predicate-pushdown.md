# 0043-0002 — MHJ Predicate Pushdown into Chain Steps

**Status:** superseded
**Superseded by:** [leftdeep-joins/](leftdeep-joins/) — MHJ retired (M0127).
**Parent milestone:** M0043
**Date:** 2026-05-04

## 1. Objective

Close M0043-0002 by partitioning `MultiHashJoin.Filters` across the
chain steps according to the deepest table each filter references,
and evaluating each filter at the earliest moment its referenced
columns are bound. A filter that fails aborts the current cursor
prefix immediately, preventing the wasteful expansion of every
deeper step.

After this landing, TPC-H Q9 — the regression case from
`analysis/tpch-hammerdb-run-006.md` — must finish in single-digit
minutes on full SF=1, and `TestTPCHResultParity` must remain
identical=22, divergent=0, errored=0.

## 2. Problem

M0043-0001 fixed the heap-explosion symptom of Q9 by replacing the
catastrophic `expandChain()` full-Cartesian materialisation with a
lazy per-call iterator. RSS now stays bounded (run-006 measured
11 GB vs. the 19 GB run-005 explosion), but Q9 still timed out at
> 16 minutes because the executor still does
**every Cartesian combination** of multi-row hash matches before
checking any residual filter.

The Q9 path is illustrative. The bushy DP flattens the join tree
into an MHJ over `lineitem` (probe), `orders`, `partsupp`,
`supplier`, `nation`, `part`. The `partsupp ↔ lineitem` join has
two equalities — `ps_suppkey = l_suppkey` and `ps_partkey = l_partkey`.
The MHJ key set carries one of them; the other lives in
`MultiHashJoin.Filters` (the `extras` channel in
`internal/planner/bushy.go:564-679`). The build-side hash table for
`partsupp` is keyed on `ps_suppkey` only, so for each `lineitem`
probe each suppkey lookup returns multiple partsupp rows, of which
**at most one** matches `ps_partkey = l_partkey`. With M0043-0001's
leaf-only filter eval the executor:

- Picks one of those partsupp matches,
- Expands through `orders` (potentially many),
- Expands through `nation` (one),
- Builds the leaf row,
- Then evaluates `ps_partkey = l_partkey` and almost always rejects.

Multiplied by 6 M lineitems, the per-probe wasted work dominates
the total Q9 runtime. The right place to evaluate
`ps_partkey = l_partkey` is **immediately after `partsupp` is
bound**, before expanding into `orders` and `nation`.

## 3. Approach

For each filter `f` in `o.plan.Filters`, compute `bindStep(f)` =
the smallest step index `s` such that every column `f` touches is
bound either by the probe (table `ProbeTable`, bound at `−1`) or
by some chain step `≤ s`. Partition the filter list by `bindStep`
into:

- `probeFilters` — `bindStep == −1`, evaluate immediately after
  `advanceProbe()`. A failing probe-filter skips the entire probe
  row.
- `stepFilters[s]` — `bindStep == s`, evaluate immediately after
  the step-`s` match is copied into `lazyOut`. A failing step-
  filter aborts the current cursor prefix; the executor advances
  `cursor[s]` to the next match without touching deeper steps.
- `leafFilters` — escape hatch for filters whose `walkColumnRefs`
  walk hits an `OuterColumnRef`, `SubqueryExpr`, `ExistsExpr`, or
  `InExpr`. Those are evaluated at the leaf (after every step is
  bound), preserving the M0043-0001 contract.

Evaluation moves earlier in the cursor walk; semantics do not
change. INNER-join semantics still hold: a leaf row is emitted iff
every step matches *and* every filter passes — pushing a filter
earlier only changes when we discover a violation, never which
violations exist.

## 4. Algorithm

### 4.1 Computing the bind-step table

In `Open()`, after `o.tableOff` and `o.keySteps` are populated,
build:

```
bindStepOfTable []int  // length nTables
bindStepOfTable[ProbeTable] = -1
for s, k := range keySteps:
    bindStepOfTable[k.hashTblIndex] = s
```

Tables not in either category retain a sentinel (-2 say); a filter
referencing them gets routed to `leafFilters` as a defensive
fallback (we don't expect this in practice — the planner never
emits filters pointing outside the MHJ subset).

### 4.2 Mapping output-row index → table

The output schema layout is the concatenation
`Tables[0].Output() ++ Tables[1].Output() ++ … ++ Tables[N-1].Output()`,
and `tableOff[i]` is the prefix sum of widths. To find the source
table of an absolute index `idx`, search for the unique `t` with
`tableOff[t] ≤ idx < tableOff[t] + len(nulls[t])`. With ≤ 6 tables
in TPC-H this is a trivial linear scan; partitioning happens once
per `Open()`, never on the hot path.

### 4.3 Per-filter classification

For each `f` in `plan.Filters`:

```
indices := []int{}
outOfScope := false
walkColumnRefs(f,
    func(idx int) { indices = append(indices, idx) },
    func()         { outOfScope = true })

if outOfScope:
    leafFilters = append(leafFilters, f); continue

required := set of unique tables(idx) for idx in indices
if required is empty:                       // 1<2 et al.
    probeFilters = append(probeFilters, f); continue

bindStep := max(bindStepOfTable[t] for t in required)
if bindStep == -1:
    probeFilters = append(probeFilters, f)
else:
    stepFilters[bindStep] = append(stepFilters[bindStep], f)
```

`walkColumnRefs` already exists at
`internal/planner/pushdown.go:215-249`; it visits every
`ColumnRef.Index` and surfaces an `out-of-scope` flag for
`OuterColumnRef`/`SubqueryExpr`/`ExistsExpr`/`InExpr`. Reusing it
means we don't duplicate Expr-walk logic.

### 4.4 Recursive iteration with filter checks

`Next()` becomes a thin loop around two helpers:

- `initStepHelper(s)` — find the first cursor configuration for
  steps `s..lastStep` that yields a leaf where every filter (step
  filters at every level + leaf filters) passes. Returns
  `(true, nil)` on success with `lazyOut` populated; `(false, nil)`
  when no configuration exists.
- `advanceFrom(s)` — given that all cursors are currently at a
  valid emit, find the *next* valid configuration. Increments
  `cursor[s]`; on success copies match into `lazyOut`, evaluates
  step-filters at level `s`, and recursively calls
  `initStepHelper(s+1)`. On failure (cursor exhausted at level
  `s`, or no valid deeper combination), returns
  `advanceFrom(s − 1)`.

Both helpers reuse a small `evalFilters([]Expr) (bool, error)`
shim that loops over `evalExpr` (already used by the M0043-0001
`applyAndFilter`).

Recursion depth is bounded by `len(keySteps)`, which is `nTables −
1`. For TPC-H this is ≤ 5. The Go call-stack handles that
trivially; any stack pressure shows up as a clear regression in
the unit test rather than as a crash.

The leaf check (filters with bindStep > all steps, i.e., none —
those land in `leafFilters` only via the out-of-scope escape
hatch) happens inside `initStepHelper(len(keySteps))`, which is
the recursion base case. The base case still returns `(true, nil)`
when the leaf filters all pass and `(false, nil)` when at least
one rejects.

## 5. Edge cases

- **Constant filter (no `ColumnRef`)**: `1 < 2`, `'x' = 'x'`. The
  `indices` list is empty after `walkColumnRefs`. We route to
  `probeFilters`, which is evaluated once per probe — cheap, and
  preserves semantics. (It's tempting to evaluate at `Open()`-
  time and short-circuit globally, but that complicates the
  shape of `Next()` for negligible benefit; deferring to per-probe
  is fine.)
- **Subquery / OuterColumnRef inside a filter**: `walkColumnRefs`
  invokes `onOuter`; we route to `leafFilters` and evaluate at
  the leaf. This matches the existing M0043-0001 behaviour
  exactly — no behaviour change for these cases.
- **Filter references a table not in the MHJ**: the planner side
  (`bushy.go::extraInScans`) prevents this; the executor treats
  it as `leafFilters` defensively.
- **Filter with side effects**: TPC-H expressions are pure
  (BinaryOp, UnaryOp, FuncCall, CaseExpr, ExtractExpr — all of
  goopg's exec evaluators are deterministic). Pushdown is
  semantics-preserving for any pure filter. If a future filter
  carries side effects (e.g., a non-deterministic function), the
  executor would still emit a correct result; only the *count*
  of evaluations would differ, which is allowed.

## 6. Verification

1. New unit test
   `internal/executor/multi_hash_join_test.go::TestMultiHashJoinPredicatePushdown`
   builds a 3-table MHJ over `A⨝B⨝C` with a residual filter
   referencing both `B` and `C`. The synthetic data is shaped so
   that the filter rejects the majority of leaf combinations.
   Assertions:
   - Result rows match a baseline computed without pushdown.
   - A counter wrapped around the synthetic filter increments
     **strictly fewer times** than `|A| · |Bmatches| · |Cmatches|`
     (the leaf-eval upper bound).

2. `go test ./internal/executor/... -count=1 -short` — green.

3. `go test ./internal/testutil/tpch/... -run
   TestTPCHResultParity -count=1` — `identical=22 divergent=0
   errored=0`. This is the primary correctness gate.

4. `go test ./internal/testutil/tpch/... -run
   TestRunTPCHQueriesAgainstSyntheticData -count=1` — 22/22 pass.

5. End-to-end: rerun the HammerDB power test against a pre-loaded
   SF=1 cluster (`bench/tpch/run_power_test_goopg.sh`) and capture
   Q9 wall time. Acceptance: Q9 finishes in single-digit minutes;
   the test gets at least to Q20 (next query in HammerDB's order).

6. `make ralph-state-guard` — passes.

The wall-time delta from run-006 (Q9 timeout) to the new run will
be captured in `analysis/tpch-hammerdb-run-007.md` once the
implementation lands.

## 7. Out of scope

- **Numeric/bytes-keyed hash tables** to retire `datumKey()`'s
  string conversion cost. Tracked separately as a future
  M0043-0003 candidate.
- **Compound-key MHJ build**, e.g., partsupp keyed on
  `(ps_suppkey, ps_partkey)` to dissolve the
  `ps_partkey = l_partkey` residual entirely. Requires planner
  changes; out of scope for this loop.
- **Outer-join semantics** — MHJ is INNER-only by construction.
- **Pushing single-table predicates** already absorbed into
  `SeqScan.Filter` upstream of the MHJ. Those never enter
  `MultiHashJoin.Filters`.
