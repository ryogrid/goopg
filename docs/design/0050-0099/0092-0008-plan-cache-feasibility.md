# Design 0092-0008 — plan-cache feasibility (deferred)

**Status:** DEFERRED. No code change in M0092 scope.
**Milestone:** [M0092](../../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Background

A plan cache would skip `parser.Parse + planner.Plan +
executor.Build` for repeated SQL text. The per-query
parse/plan/build cost is ~2 KB allocation + ~150-300 µs
CPU. At 437 TPS that's ~875 KB/s alloc + many µs/query —
potentially material if cache hit rate is high.

## Why deferred (Explore-agent verdict)

### 1. pgbench's simple-query path won't benefit

pgbench's `-S` script is `SELECT abalance FROM
pgbench_accounts WHERE aid = :aid;` — pgbench
**client-side substitutes** `:aid` with the random integer
before sending. Each query has a different literal value
baked into the SQL text:

```
SELECT abalance FROM pgbench_accounts WHERE aid = 42;
SELECT abalance FROM pgbench_accounts WHERE aid = 9999;
SELECT abalance FROM pgbench_accounts WHERE aid = 543210;
...
```

A byte-for-byte SQL cache key would produce **0 % hit
rate** on this workload.

### 2. Literal normalization is complex

To make the cache hit, we'd need a normalized cache key
that ignores literal values:

```
template: "SELECT abalance FROM pgbench_accounts WHERE aid = ?"
literal:  [42] / [9999] / [543210] / ...
```

This requires:
- Parser to annotate literal AST nodes.
- Planner to extract literals into a parameter list and
  produce a literal-free plan.
- Executor to substitute literals from the per-query
  parameter list at runtime.

That's PG's prepared-statement machinery rebuilt. Multi-
week effort with subtle bugs (parameter-position tracking,
type-coercion caching invalidation, etc.).

### 3. Planner output has mutable state

`planner.Plan`'s output has mutable cache fields
(`TypedStringLit.CachedValid`, `IntervalLit.Cached*`)
that are populated lazily during execution. Reusing a
plan across queries requires either:
- Deep-copy the plan tree on cache hit (defeats the
  optimization).
- Reset the cache fields after each execution.
- Confine the cache fields to per-query scratch state
  (refactor).

None are minimal.

### 4. Build cost is small relative to execution

For pgbench's simple SELECT:
- Build cost: ~thin wrapper instantiation.
- Parse + Plan: ~150-300 µs (the actual analysis work).

At 437 TPS / 19 ms latency, parse+plan is ~1.5 % of query
time. Even eliminating it entirely barely moves TPS.

### 5. Other M0092 sub-milestones are higher leverage

Per the post-M0092 alloc profile, the residual is
broadly distributed (Materialize cloneRowOwned,
SlotFromRow, PageGetHeapTuple, protocol cells slice,
LookupGoroutine). Each of M0092-0004 / 0005 / 0006 / 0007
attacks a specific site that's bigger than parse+plan.

## Future use-case for a plan cache

A plan cache would be VALUABLE for:

- **Extended-protocol (Bind/Execute) workloads.** Clients
  send Parse once, Bind+Execute many times. goopg's
  current extended-protocol path treats Parse as a no-op
  (Parse+Plan+Build runs on every Execute). Wiring the
  extended path to share state across Executes is the
  natural target.
- **OLTP workloads with parameterised queries** that
  consistently send the same SQL template (the API-server
  pattern).

These deserve their own milestone (M0093 candidate when
prioritised). They'd reuse the extended-protocol's
infrastructure rather than the simple-query's.

## Decision

**No plan cache in M0092.** Document this analysis here
so the work isn't redone in a future investigation.

If a future milestone tackles extended-protocol prepared
statements, this design doc becomes part of the
background reading for that milestone.

## Cross-references

- `bench/pgbench-compare/results/20260511_goopg_select-only_m0092_summary.md`
  (the user-facing summary that filed this question).
- `internal/parser/ast.go` (Stmt type immutability).
- `internal/planner/plan.go:97-126` (TypedStringLit.Cached*
  fields).
- `internal/server/dispatch.go` (simple-query path).
- `internal/server/dispatch_extended.go` (extended-query
  path — would consume a cache when implemented properly).
