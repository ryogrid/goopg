# BorrowRow Volcano Row-Lifetime Optimization (M0059)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| milestone  | 0059 — Executor BorrowRow Optimization |
| supersedes | — |

## 1. Problem

The Volcano executor's `Next()` contract currently mixes owned and
borrowed tuple flow, but borrow propagation is limited to a subset of
safe edges. Many operators still clone rows defensively even when the
parent consumes rows immediately and does not retain them.

This creates avoidable allocation pressure:

- repeated `cloneRow(...)` on hot tuple paths,
- per-row slice churn in operators that could reuse buffers,
- elevated GC scan/load for join and aggregate workloads.

The existing behavior is safe, but costlier than necessary for
streaming pipelines.

## 2. Scope

### In scope

- Expand BorrowSemantics usage on safe parent-child edges.
- Remove redundant copies in hot operators where row lifetime permits.
- Add regression tests that pin ownership/borrowing correctness.
- Validate improvements with benchmark/profile evidence.

### Out of scope

- SQL semantics changes.
- Replacing required materialization at retaining operators.
- New planner-level query transformation work unrelated to tuple
  lifetime.

## 3. Current Behavior

### 3.1 Contract surface

`OwnedRow` is the default. `BorrowedRow` is opt-in via `SetBorrow` on
Borrowable operators.

### 3.2 Partial propagation

Build-time borrow propagation exists on selected wrappers, but not all
safe chains are consistently marked, and some operators still clone on
paths where parent consumption is immediate.

### 3.3 Materialization boundaries

Retaining operators (sort/hash-build/materializing paths) require owned
rows and must remain correctness boundaries where borrowing does not
cross.

## 4. Proposed Change

### 4.1 Row lifetime matrix

Classify every executor operator into one of three classes:

1. Pass-through consumers (consume row and pull next immediately).
2. Compute-only emitters (can return borrowed buffers if caller is
   borrow-safe).
3. Retaining/materializing operators (must enforce owned rows).

The matrix is the authority for propagation decisions.

### 4.2 Borrow propagation policy

- During Build wiring, mark child as `BorrowedRow` when the parent is
  class (1) or borrow-safe class (2) without retention.
- Do not propagate through class (3) boundaries.
- Keep instrumentation wrappers transparent to borrow propagation.

### 4.3 Operator-level optimization targets

Phase-in copy removal for operators identified as hot by pprof baselines.
For each operator:

- add or refine `SetBorrow` handling,
- return borrowed buffers when safe,
- keep owned fallback for retaining callers,
- pin behavior with ownership regression tests.

### 4.4 Safety invariants

- Borrowed row content may be invalidated by next `Next()` call.
- Owned rows remain stable across subsequent `Next()` calls.
- Any operator retaining rows must force owned semantics at its input
  boundary.

## 5. Affected Code Paths

- `internal/executor/operator.go`
- `internal/executor/executor.go`
- `internal/executor/operators.go`
- `internal/executor/operators_storage.go`
- `internal/executor/operators_index.go`
- `internal/executor/operators_indexonly.go`
- `internal/executor/operators_nljoin.go`
- `internal/executor/spill.go`
- `internal/executor/borrow_test.go`

The exact subset touched per sub-task is tracked in `.ralph/fix_plan.md`
under M0059.

## 6. Testing

- Unit tests for borrow contract correctness by operator class.
- Regression tests for owned-row stability under retaining parents.
- End-to-end query tests on representative TPC-H shapes.
- No-parity-regression check against existing compatibility tests.

## 7. Measurement Plan

- Capture before/after allocation and GC hotspots on selected TPC-H
  queries.
- Report at least:
  - total alloc bytes delta,
  - top allocation symbols,
  - GC-heavy symbol share delta,
  - wall-clock impact for selected queries.

## 8. Risks and Mitigations

- Risk: accidental aliasing from borrowed rows retained by parent.
  Mitigation: explicit class-(3) boundaries + ownership regression tests.
- Risk: performance gain hidden by unrelated plan differences.
  Mitigation: fixed query set and reproducible profiling windows.