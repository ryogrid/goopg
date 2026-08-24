# M0134-0131 — stored-routine call-depth guard (infinite_recurse.sql)

## Problem

`postgres/src/test/regress/sql/infinite_recurse.sql` creates a
self-recursive `LANGUAGE sql` function and calls it:

```sql
create function infinite_recurse() returns int as
  'select infinite_recurse()' language sql;
select infinite_recurse();
```

Upstream PostgreSQL raises `54001 stack depth limit exceeded` — the C-stack
depth check (`check_stack_depth`, `postgres/src/backend/utils/misc/stack_depth.c`)
polls the current stack pointer against `max_stack_depth` on every function
call and aborts the query cleanly, well before the process's real stack
limit is at risk.

goopg had no equivalent check. Each nested call recursed through the Go
call stack:

```
evalStoredRoutineFuncCall → executeStoredRoutine → executeSQLRoutine
  → optimizer.Plan / Build → op.Open/Next → evalFuncCall
  → evalStoredRoutineFuncCall → ...
```

with no bound, until the goroutine's stack grew past Go's default max
goroutine stack size (1 GiB) and the runtime issued a **fatal, unrecoverable
"stack overflow"** — unlike a Go panic, this cannot be caught by any
`recover()` in the call chain; it terminates the whole process. The regress
client observed this as "server closed the connection unexpectedly" instead
of a catchable `54001` error.

## Fix

Added `Context.RoutineDepth` (`internal/executor/context.go`) — a plain
`int` counter on the per-statement `Context`. `executeStoredRoutine`
(`internal/executor/plpgsql_runtime.go`, the single entry point both
`executeSQLRoutine` and `executePLpgSQLRoutine` are dispatched through)
increments it on entry, defers a decrement, and raises

```go
&ExecError{Code: "54001", Pos: pos, Message: "stack depth limit exceeded"}
```

once the counter exceeds `maxRoutineCallDepth` (2000).

The counter threads through recursion correctly without any extra plumbing
because both `executeSQLRoutine` and `executePLpgSQLRoutine` build their
child `Context` via `*child = *ctx` before recursing into the function
body — the just-incremented depth is copied forward by value at every
level, exactly mirroring what a real stack-pointer check would observe.

`maxRoutineCallDepth=2000` is chosen to sit comfortably above any
legitimate recursion depth exercised by the regress suite (plpgsql control
tests recurse well under 100 levels) while triggering long before Go's
1 GiB default goroutine stack limit is actually at risk — a single
`executeStoredRoutine` → `executeSQLRoutine` → `optimizer.Plan`/`Build`
frame chain is well under 1 MiB even at conservative estimates, so 2000
levels stays multiple orders of magnitude short of the real ceiling.

## Why not a real stack-pointer check

PostgreSQL's `check_stack_depth` compares the current C stack pointer
against a captured base pointer — a technique available because C stack
frames are contiguous and their addresses are directly observable.
Go's growable, non-contiguous "segmented"-style stacks (they are actually
contiguous per-goroutine but grow via copy, and the runtime does not expose
a stable, cheap-to-read "current depth" primitive to normal code) make the
equivalent unavailable without `unsafe`/runtime-internal tricks. A logical
call-depth counter is the standard Go substitute and is sufficient here:
the goal is only to bound recursion before the goroutine stack grows
unboundedly, not to reproduce PG's exact trip point.

## Scope / what this does NOT cover

- Recursion through other executor paths that don't go through
  `executeStoredRoutine` (e.g. a pathological expression evaluator
  recursion, or deeply nested subqueries) is unguarded. `WITH RECURSIVE`
  already has its own `maxRecursiveDepth`/`maxRecursiveIterationRows` guard
  (`internal/executor/operators_recursive_cte.go`, M0097-0006/M0134-0086) —
  unrelated mechanism, same failure family.
- Trigger functions and other routine-invocation call sites that don't run
  through `executeStoredRoutine` are not covered by this counter; if a
  trigger recursion bug surfaces, either route it through
  `executeStoredRoutine` or add an analogous counter at its entry point.

## Test

`scripts/pg-regress-runner.sh --verbose infinite_recurse` → 100% parity
(was 0%, "server closed the connection unexpectedly" before the fix).
CSV row `postgres/src/test/regress/sql/infinite_recurse.sql` flipped
`not-tried` → `pass` / `pass_required=yes`.
