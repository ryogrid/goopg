# M0134-0086 — recursive CTE per-iteration OOM guard

## Status

Landed 2026-08-24. Safety-net fix; `with.sql` PARKED (still `failed`, no
correctness change to its pass/fail status — see "Scope" below).

## Problem

Sizing `with.sql` against the PG 18.3 oracle (`postgres/src/test/regress/sql/with.sql`)
found a query that made goopg's server RSS climb without bound (22+ GB and
rising, host-level OOM risk) instead of erroring or completing:

```sql
with recursive q as (
      select * from department
    union all
      (with recursive x as (
           select * from department
         union all
           (select * from q union all select * from x)
        )
       select * from x)
    )
select * from q limit 32;
```

This is real, PG-accepted SQL (`postgres/src/test/regress/expected/with.out:508-545`)
— PG returns exactly 32 rows, cycling through the 8-row `department` closure
four times, because `LIMIT 32` stops the pull chain early. It is testing that
a `WITH RECURSIVE` nested inside another `WITH RECURSIVE`, whose recursive
term references back out to the still-open outer query, still terminates
under a bounding `LIMIT`.

### Root cause

`recursiveUnionOp.Next()` (`internal/executor/operators_recursive_cte.go`)
implements each fixpoint iteration as: open the recursive member, drain it
**to EOF** into an `iterRows` buffer, close it, then hand the buffered rows
back to the caller one at a time on subsequent `Next()` calls. Nothing is
returned to the caller until the whole iteration has finished.

Real PostgreSQL (`nodeRecursiveunion.c`, `ExecRecursiveUnion`) instead
returns each row **as it is produced** by the recursive term — new rows are
simultaneously stored into the working-table tuplestore *and* returned
immediately to the caller. That row-at-a-time pull model is what lets an
outer `LIMIT` terminate the whole recursive tree early even when the query
graph is not naturally finite on its own.

For an ordinary (non-nested, non-mutually-referential) recursive CTE this
difference is invisible: one iteration only ever produces as many rows as
there are "frontier" nodes, a small, naturally-bounded set, so eagerly
draining one iteration costs nothing extra. It becomes fatal here because
the *inner* `WITH RECURSIVE x`'s recursive term is
`select * from q union all select * from x` — pulling `x` to EOF requires
pulling the outer, still-in-flight `q` to EOF, which (by the same "drain
to EOF" rule, recursively) can never happen, because the query graph is
genuinely infinite without lazy `LIMIT` propagation. goopg's
`maxRecursiveDepth` (1000-iteration) guard never even fires, because depth
only advances *between completed iterations* — this query is stuck forever
inside the very first iteration's drain loop.

Verified live (throwaway cgroup-capped server): a minimal reproduction —

```sql
with recursive q as (
      select 1 as n
    union all
      (with recursive x as (
           select 1 as n
         union all
           (select * from q union all select * from x)
        )
       select * from x)
    )
select * from q limit 10;
```

— grew RSS past 22 GB before being killed. Removing either self-reference
(`x`-only-refs-`x`, or `x`-only-refs-`q`) or adding an inner `LIMIT` on `x`
made it terminate immediately, confirming the "never reaches EOF within one
iteration" diagnosis.

## Fix landed this loop

A safety net, not a correctness fix: `maxRecursiveIterationRows` (new,
`internal/executor/operators_recursive_cte.go`, default 2,000,000, a `var`
so tests can shrink it) bounds how many rows a single fixpoint iteration's
drain loop may accumulate before giving up. Exceeding it raises the same
`54001`/"WITH RECURSIVE exceeded maximum recursion depth" `ExecError` the
existing `maxRecursiveDepth` guard already uses, instead of growing without
bound. This turns an unbounded RSS blow-up into a bounded, catchable error
— acceptable because 2,000,000 rows is far above any legitimate recursive
CTE in the regress corpus or realistic production use (the largest
recursive-CTE fixtures in `postgres/src/test/regress` produce at most a few
thousand rows per iteration), so no currently-passing behavior changes.

Verified live: the minimal repro above now errors in ~9s at ~800 MB RSS
(`ERROR: WITH RECURSIVE exceeded maximum recursion depth 1000`) instead of
climbing toward host OOM. Unit-level regression test
`TestRecursiveUnionCapsRunawaySingleIteration`
(`internal/executor/operators_recursive_cte_iteration_cap_test.go`) shrinks
the threshold to 200 to exercise the guard without materialising millions of
rows.

## Scope — deliberately NOT fixed this loop

The real fix is a refactor of `recursiveUnionOp.Next()` to the row-at-a-time
pull model described above (`nodeRecursiveunion.c`), which additionally
needs each CTE reference within a recursive tree to be backed by a **shared,
reentrant instance** rather than a fresh operator subtree per reference —
`select * from q` inside `x`'s recursive term must pull from the SAME
in-flight `q` evaluation the outer query is driving, not spin up a second,
independent `q` computation. That is genuine REFACTOR-tier work (new
executor coroutine/state-machine plumbing plus CTE-instance sharing across
nested `WITH` scopes) and is out of scope for a single M0134 loop, per the
established pattern (cf. `docs/design/m0134-0025-lateral-outer-colref-aggregate-crash.md`
et al.). `with.sql`'s pass/fail status is unaffected by this loop's fix —
see `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0086, for the remaining
bucket breakdown and resume point.

## Files changed

- `internal/executor/operators_recursive_cte.go` — `maxRecursiveIterationRows` guard.
- `internal/executor/operators_recursive_cte_iteration_cap_test.go` — new regression test.
