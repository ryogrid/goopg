# 0021-0002 — Row-Lock Planner Metadata + LockRows Plan Node

**Status:** accepted (planner slice — runtime executor lands in
M0021-0003)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
**Spans seam:** Planner plan-tree, LockRows wrapper node, EXPLAIN
render labels.
**Cross-links:**
[0021-0001](0021-0001-for-update-parser-analysis-and-ast.md)
(parser + analyzer slices),
[root-0011](../../root/root-0011-planner.md) (planner scaffolding),
[0017-0002](0017-0002-upsert-planner-and-arbiter-selection.md)
(parallel planner-only slice precedent).

## Context

M0021-0001 step 1 introduced the parser AST for
`SELECT … [FOR UPDATE | FOR SHARE [OF …] [NOWAIT | SKIP LOCKED]]`.
Step 2 wired analyzer validation against the FROM-clause range
variables. This slice produces an **executable plan node** that
carries the resolved per-relation locking intent forward —
runtime row-lock acquisition lands in M0021-0003 (Stage A
blocking) / M0021-0003 (NOWAIT / SKIP LOCKED).

## Plan node shape

```go
type LockStrength int

const (
    LockStrengthForUpdate LockStrength = iota + 1
    LockStrengthForShare
)

type LockWaitPolicy int

const (
    LockWaitBlock LockWaitPolicy = iota
    LockWaitNoWait
    LockWaitSkipLocked
)

type LockedRel struct {
    Table      *catalog.Table
    Alias      string
    Strength   LockStrength
    WaitPolicy LockWaitPolicy
}

type LockRows struct {
    pos   int
    Child Node
    Locks []LockedRel
}
```

`LockRows.Output()` returns the child's schema unchanged —
locking is a side effect on storage, not a row-shape
transformation. Pre-M0021 SELECTs never produce a LockRows node
so existing tests stay byte-for-byte unchanged.

## Plan generation

`planSelect` ends with a Project node as before. When
`s.Locking != nil`, the planner wraps the Project in a LockRows
carrying the resolved Locks slice. The wrapping is the very last
step so all the existing per-clause work (Filter / Aggregate /
Sort / Limit / Project) lives below LockRows in the tree —
matches upstream's "LockRows at the top" placement.

`resolveLockedRels(s, ctx)` walks each parsed clause and produces
one LockedRel per (clause, FROM-clause range variable) in the
effective target set:

- Empty `Targets` (bare `FOR UPDATE`) → one LockedRel per
  binding in the resolveContext (every FROM rel).
- Non-empty `Targets` → one LockedRel per name, looked up via
  `findBindingByName` (alias-shadows-table, mirrors the
  analyzer's matcher).

`lockStrengthFromParser` / `lockWaitPolicyFromParser` are simple
parser→planner enum conversions kept symmetric so future
extensions (FOR NO KEY UPDATE / FOR KEY SHARE) are one constant
away.

Deduplication / strongest-wins is not implemented — multiple
clauses targeting the same rel produce duplicate LockedRels.
The Stage A executor will fold them when it lands; until then
the duplicates are harmless because no lock is acquired.

## Executor gate

`executor.Build` rejects any `*planner.LockRows` with:

```go
case *planner.LockRows:
    return nil, fmt.Errorf("row-level locking execution is not supported in v0 (Stage A executor lands in M0021-0003)")
```

This is the now-familiar two-step gate (mirrors M0017-0002 →
M0017-0003): the planner produces a fully-formed LockRows so
EXPLAIN works against the locked SELECT, but Build refuses to
silently drop the locking intent at runtime.

## EXPLAIN integration

`describePlan` and `planChildren` learn the new node:

- `case *planner.LockRows: return "LockRows"` — single-line
  label mirroring upstream.
- `planChildren` returns `[]planner.Node{p.Child}` so EXPLAIN
  recurses into the wrapped plan tree.

A future VERBOSE-only extension can add per-relation Strength /
WaitPolicy detail; for now the bare label keeps EXPLAIN output
narrow.

## Tests

`internal/planner/locking_test.go`:

- `TestPlanSelectForUpdateWrapsLockRows` — root node is
  *LockRows with one LockedRel pointing at the FROM table,
  default WaitPolicy=Block.
- `TestPlanSelectForUpdateOfAlias` — `OF a` produces a single
  LockedRel for the aliased rel (the unaliased `h` is not
  locked).
- `TestPlanSelectForUpdateNoTargetLocksAllRels` — bare
  `FOR UPDATE` over a 2-rel FROM produces 2 LockedRels.
- `TestPlanSelectForUpdateNoWaitPropagates` — parser→planner
  enum conversion is intact for NOWAIT.
- `TestPlanSelectForShareStrength` — read-intent variant.
- `TestPlanSelectWithoutLockingNoWrapper` — rollout guardrail:
  SELECTs without locking clauses produce the bare plan tree
  as before.

Full `go test ./...` green.

## Out of scope

- Stage A executor: actual row-lock acquisition before yielding
  rows, blocking under contention, lock release on transaction
  boundary — M0021-0003.
- Wait-policy runtime semantics (NOWAIT 55P03 fail, SKIP LOCKED
  silent drop) — M0021-0003.
- Deadlock-aware row-lock waits, observability counters —
  M0021-0004.
- Strongest-wins merge of duplicate LockedRels across clauses —
  lands with the Stage A executor.
- VERBOSE EXPLAIN detail (per-rel Strength / WaitPolicy lines) —
  cosmetic follow-up.
