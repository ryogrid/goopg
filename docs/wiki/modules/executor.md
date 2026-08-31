# Module: `internal/executor`

The execution engine. Runs goopg's plan trees (`optimizer.Node`) with a
PostgreSQL-style Volcano iterator model (`Open → Next → EOF → Close`). It
compiles plan nodes into operators (seq/index scan, filter, project, join, agg,
sort, limit, DML, DDL, utility), evaluates expressions over per-row `Datum`
values, drives the MVCC heap-write/stamp machinery for INSERT/UPDATE/DELETE, and
executes stored routines (PL/pgSQL, SQL-language, C-stub, `internal`), `CALL` /
`DO`, triggers, and the VACUUM/ANALYZE/checkpoint/transaction verbs.

There are **two sibling engines**: a legacy `Build`/`Run` producing an
`Operator` tree, and a GC-pointer-free fast path `BuildFast`/`BuildFastIterator`
over flat int32-indexed slabs. The fast path is the live server entry point
(`postmaster/dispatch.go:2964`).

## Key Files

- `executor.go` (647) — `Build`/`BuildFast` dispatch (`optimizer.Node` → operator/slab).
- `expr.go` (21,152) — interpreted evaluator `evalExprSlot` + every SQL builtin
  body (to_char, regexp, extract, casts, interval/time arithmetic, subqueries).
- `exprnode.go` (592) — compiled expression tree `ExprNode`/`exprTreeSlab` + `evalFastExpr`.
- `opnode.go` (904) — fast-path operator tree `OpNode`/`opTreeSlab`, `Slot`,
  `OpIterator`, `opOpen`/`opNext` dispatch, `BuildFastIterator`.
- `operators_storage.go` (9,999) — `seqScanOp`/`insertOp`/`updateOp`/`deleteOp`:
  heap writes, HOT updates, xmax stamping, EPQ retry chains, unique/exclusion checks.
- `operators_join_agg.go` (4,987) — `joinOp` (lazy hash / merge / nested-loop /
  lateral), `aggregateOp` (hash + sorted grouping, user sfunc dispatch).
- `operators_ddl.go` (27,132) — `ddlOp`, one ~50-arm `Next` switch over every
  parser DDL statement.
- `plpgsql_runtime.go` (3,752) — stored-routine dispatch by language +
  PL/pgSQL statement interpreter (`executePLpgSQLStmt`).
- `context.go` (1,930) — the per-statement `Context` struct, `NewContext`,
  `CommitTransaction`, lock helpers.
- `datum.go` (890) — 48-byte `Datum` value carrier, `DatumKind`/`TimeSubtype`.
- `session.go` (1,039) — `Session` interface (`BasicSession`,
  `NewAutocommitUndoSession`): txn state, savepoints, DDL undo, deferred checks.
- `hashsize/` — leaf package: hash-join geometry (`nbuckets`/`nbatch`), shared by
  planner and executor.
- `kvcache/` — leaf package: byte-budgeted LRU backing correlated-subquery caches.

## Public API

Entry points (`executor.go`, `opnode.go`):

```go
func Build(plan optimizer.Node) (Operator, error)          // legacy builder
func BuildFast(plan optimizer.Node) (*opTreeSlab, int32, error)
func BuildFastIterator(plan optimizer.Node) (*OpIterator, error) // live dispatch path
func Run(op Operator, ctx *Context) ([]Row, error)         // test/drain helper
func RunFast(tree *opTreeSlab, rootIdx int32, ctx *Context) ([]Row, error)
```

Interfaces and core types:

```go
type Operator interface { Open(ctx *Context) error; Next() (TupleSlot, error);
                          Close() error; Schema() optimizer.Schema }
type RowCounter interface { RowsAffected() int64 }         // feeds "INSERT 0 N" tags
var EOF, ErrSelfTerminate
type Context struct{ /* per-statement state */ }           // NewContext() *Context
type Datum struct{ /* 48-byte value carrier */ }
type Row = []Datum
type ExecError struct { Code, Message, Detail, Hint, Context string; Pos int;
                        ConditionName string }              // SQLSTATE carrier
type Session interface{ /* isolation, savepoints, DDL undo, deferred checks */ }
```

Key `Context` methods: `CommandCounterIncrement`, `SetCmdCounter`,
`CommitTransaction`, `AddNotice`/`TakeNotices`, `acquireRelLock`,
`acquireTupleLock`, `MaterializeWriterXID`, `DidWrite`, `SetParamExec`.

## Internal structure

- **Two engines** — `Build` recursively constructs an `Operator` tree
  (`buildNode`); `BuildFast` builds an `opTreeSlab` (flat `[]OpNode` with int32
  child indices) plus a parallel `exprTreeSlab`. Non-migrated ops are wrapped in
  an `OpAdapter` forwarding the legacy interface.
- **Operator naming** — constructors `new<PlanType>Op` returning unexported
  `<lower>Op` structs; storage ops cache a `storage.RelFileNode` + `*catalog.Table`.
- **DDL** — one `ddlOp` with a ~50-arm switch, each arm an `exec*` method.
- **Expression eval** — interpreter `evalExprSlot` (giant type switch) vs
  compiled `evalFastExpr`; `ExprAdapter` falls back to the interpreter.
- **State ownership** — `Context` is created per statement by dispatch; parallel
  workers share `SharedHashBuilds`, `PartialAggStates`, and a single `tempFiles`
  registry (a pointer, because `Context` is value-copied at a few sites).
- **Subdirs** — `hashsize/` must import neither executor nor optimizer (cycle);
  `kvcache/` is an unthread-safe byte-budgeted LRU.

## Dependencies

- **Used by** `internal/postmaster` (wire protocol), `internal/replication`,
  `internal/backup`, `internal/initdb`, `internal/access/nbtree`,
  `internal/catalog`, `internal/storage`, `internal/parser/analyzer`.
- **Uses** `internal/optimizer` (plan node types — direction is executor →
  optimizer), `internal/parser`, `internal/catalog`, `internal/storage`,
  `internal/access/{nbtree,transam,common/pglz,amcheck}`,
  `internal/utils/{mmgr,misc,activity,adt/*}`, `internal/pl/plpgsql`,
  `internal/commands/vacuum`, `internal/nodes`, plus leaf `hashsize`/`kvcache`.

## Notable patterns / gotchas

- **Sibling evaluators must agree** — `evalExprSlot` and `evalFastExpr` are
  documented twins with past divergences (float arithmetic, AND/OR short-circuit
  gated on `Kind == KindBool`, int2/int4 overflow). Same for `Build` vs
  `BuildFast.buildRec`.
- **Slot lifetime contract** — producer slots are stable only until the next
  `Next`; consumers that retain must call `Materialize()`.
- **`hashsize` import invariant is load-bearing** — violating it introduces an
  `optimizer → executor` cycle.
- **Context copied by value** at FK/partition-DDL sites — hence `tempFiles` must
  be a pointer.
- **Command counter** — per-statement `CmdID`; `CommandCounterIncrement` before
  execution mirrors PG's per-statement CCI.
- **`Datum` subtype discipline** — `KindTime` carries five SQL types via
  `TimeSubtype`; forgetting it in a serializer silently degrades `date` → `timestamp`.
- **EPQ** — concurrently-updated tuples trigger EvalPlanQual retries
  (`maxEPQRetries`, `epqWait`, `epqFollowChain`).
- **PL/pgSQL is interpreted here** over `internal/pl/plpgsql` ASTs; language
  dispatch is `dispatchStoredRoutineByLanguage` (plpgsql → interpreter, sql →
  recursive executor, c → stub, internal → builtin map, else 42704).
