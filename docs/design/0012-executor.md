# 0012 — Executor (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

The planner (0011) produces a tree of plan nodes; the executor runs
them. Upstream PostgreSQL uses the Volcano (Iterator) model: each
operator exposes `ExecInit` / `ExecProcNode` / `ExecEnd`, and the root
operator pulls rows on demand.

References into upstream:

- `postgres/src/backend/executor/execMain.c` — `ExecutorStart`,
  `ExecutorRun`, `ExecutorEnd` lifecycle.
- `postgres/src/backend/executor/nodeSeqscan.c` — heap scan iterator.
- `postgres/src/backend/executor/execExpr.c` — expression evaluator.
- `postgres/src/backend/executor/execTuples.c` — `TupleTableSlot` and
  the Datum/null array convention for in-flight rows.

## Decision

### Layering

```
internal/executor/
    datum.go             // Datum union, null tracking, type tags
    expr.go              // expression evaluator (planner.Expr -> Datum)
    operator.go          // Operator interface (Open/Next/Close)
  operators.go         // Values, Project, Filter, Limit, Sort
  operators_index.go   // IndexScan
  operators_join_agg.go// Join + Aggregate
    operators_storage.go // SeqScan / Insert / Update / Delete
                         //   (separated so tests can run without storage)
    executor.go          // Build(plan) + Run helpers
    executor_test.go
```

The executor depends on `parser`, `planner`, `catalog`, `storage`,
`mvcc`. It is the first package in the project that ties everything
together.

### Iterator interface

Every operator is an `Operator`:

```go
type Operator interface {
    Open(ctx *Context) error
    Next() (Row, error)   // returns nil, io.EOF at end of input
    Close() error
    Schema() planner.Schema
}
```

`Row` is a `[]Datum` aligned with the operator's schema. `Datum` is a
discriminated union with `IsNull` and a small set of native carriers
(`Int64`, `Bool`, `String`, `Bytes`, `Time`); the executor doesn't
yet allocate per-row — operators reuse a single output buffer when
they can, and copy when the upstream needs to retain.

`Context` carries:
- The bind-parameter values for this Execute (indexed by `$N`).
- The active `*mvcc.Transaction` and statement snapshot.
- The buffer pool, smgr manager, catalog handles.
- The current `time.Time` snapshot for `current_timestamp` and
  friends — captured at statement start so retries see consistent
  values.

### Expression evaluator

`evalExpr(planner.Expr, row []Datum, ctx *Context) (Datum, error)`
walks a planner expression tree against the operator's input row.
Operators that produce no input (Values for SELECT 1) pass `nil` as
the row.

Constant folding happens at evaluation time, not plan time — pgbench
re-issues the same plan with different binds, so folding `$1` away
would be wrong. Function calls dispatch on `Name` against a small
in-tree registry (`current_timestamp`, `now`, `nextval` later, …).

### Operators (v0 scope)

| Operator    | Notes                                                            |
| ----------- | ---------------------------------------------------------------- |
| `Values`    | Yields each literal row in turn.                                 |
| `Project`   | Evaluates target list against the child's row.                   |
| `Filter`    | Discards child rows where the predicate isn't TRUE.              |
| `Limit`     | Consumes `Offset` rows, then yields up to `Limit` rows.          |
| `Sort`      | Fully buffers the child, then sorts under multi-key ordering.    |
| `Join`      | Nested-loop join with ON/USING/NATURAL predicates.               |
| `Aggregate` | In-memory grouping for GROUP BY + aggregate call list.            |
| `SeqScan`   | Heap-scan (block 0..N-1, slot 1..count, MVCC visibility).        |
| `IndexScan` | B-tree equality probe on encoded int4 keys.                      |
| `Insert`    | Reads child rows, marshals into HeapTuple, calls heap insert.    |
| `Update`    | Re-reads visible row, computes new image, deletes old + inserts. |
| `Delete`    | Marks visible rows dead under the current xact's xid.            |

Out of scope for v0:

- Hash join / merge join and cost-based join selection.
- Spill-to-disk for large Sort/Aggregate workloads.
- Parallel execution — single-threaded per-statement.
- Cursors / `FETCH` semantics — extended-query holds plans, but
  doesn't yet stream results across messages.

### Heap-write protocol

For Insert/Update/Delete:

1. Allocate or reuse the last block of the relation that has space.
   `storage.PageAddHeapTuple` returns `ErrNoSpaceInPage` when none;
   the executor extends via `pool.PinNew`.
2. Construct the heap tuple with `Xmin = ctx.tx.XID`, `Xmax = 0` (for
   inserts) or `Xmax = ctx.tx.XID` for the old image of an update or
   the deleted row.
3. Mark the page dirty; the WAL writer's page-LSN ordering takes
   care of durability on flush.

`Update`'s read-then-write step takes the old row's CTID and writes
the new image into a fresh slot, leaving the old slot with `xmax`
set. v0 does not emit HOT chains — every UPDATE costs one fresh
slot. This is the simplest correct shape; HOT comes with the index-
update follow-up.

### Errors

Executor errors are `*ExecError{Code, Message, Pos}` with SQLSTATE
codes carried through to the wire-protocol ErrorResponse encoder.
Common codes:

- `42883` `undefined_function` — function name unknown to the registry.
- `22P02` `invalid_text_representation` — bind parameter coercion.
- `42804` `datatype_mismatch` — type mismatch in expression.
- `XX000` `internal_error` — assertion failures, "should not happen".

### Build vs Open

Plan-tree construction is one walk; operator-tree construction
is another. `executor.Build(plan, ctx) (Operator, error)` recurses
through the plan tree and produces an operator tree. The result is
ready to `Open()` and stream rows.

This split lets the wire-protocol path build operators once at Bind
and stream rows on each Execute. The Bind/Execute split is the
extended query protocol's responsibility (0013).

### What this doc does NOT cover

- The wire-protocol Bind/Execute ferry — see 0013.
- COPY pipelines — see 0014.
- A real type system — types stay textual until milestone 7.
- A cost model — there is no costing; the planner is rule-based.

## Alternatives Considered

- **Push-based / vectorised execution.** Modern, faster on warehouse
  workloads, but pgbench's 1-row-at-a-time pattern is the worst case
  for vectorisation; pull-based Volcano is also closer to upstream
  and easier to compare against.
- **Compile to Go closures and let the runtime JIT.** Tempting; the
  closure overhead is the same order as a Go interface call, so the
  win is small for v0. Easy to retrofit when profiling justifies it.
- **Skip the executor abstraction; have each statement's run-method
  be a hand-written Go function.** Works for VACUUM/ANALYZE (which
  already have it) but doesn't compose for SELECT/UPDATE/JOIN. The
  iterator boundary pays for itself the moment we add a second
  operator that consumes another's output.

## Consequences

- The wire-protocol simple-query path can be wired straight to
  `executor.Build(plan, ctx)` and stream `DataRow` messages from
  `Operator.Next()`.
- Adding a new operator is local: implement the interface, register
  it in `Build`, write the test.
- `EXPLAIN ANALYZE` becomes a wrapping operator that records timing
  per-Next call.
