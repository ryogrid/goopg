# 0011 — Planner and Catalog Seam (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

The parser (0010) produces an AST; the executor (0012, next) consumes
plan nodes. The planner is the bridge: it takes a `parser.Stmt`,
resolves names against the catalog, and emits a tree of plan nodes
the executor can run.

Upstream PostgreSQL splits this work three ways:

- `postgres/src/backend/parser/analyze.c` — semantic analysis.
- `postgres/src/backend/optimizer/plan/planner.c` — `standard_planner`
  drives the search.
- `postgres/src/backend/optimizer/path/costsize.c` — costing inputs.

For pgbench's workload (point lookups, simple sequential scans, four
DML shapes) we don't need a cost-based optimizer — the access path is
unambiguous. v0 ships a single-pass rule-based planner that maps each
statement shape to a fixed plan template and lets the executor handle
runtime selection.

## Decision

### Layering

```
internal/catalog/      // in-memory catalog seam
    catalog.go          // Catalog interface + InMemoryCatalog impl
    catalog_test.go

internal/planner/      // parser.Stmt -> plan tree
    plan.go             // Node interface + plan node types
    planner.go          // Plan(parser.Stmt, Catalog) -> Node
    planner_test.go
```

The planner depends on `parser` and `catalog`. The executor (0012)
will depend on `planner`. The catalog package has no dependency on
either; it's a leaf.

### Catalog seam

The catalog is the source of truth for "what tables and columns
exist". For v0 it's a pure in-memory map populated by the executor
when CREATE TABLE / DROP TABLE are run. Persistence to `pg_class` /
`pg_attribute` arrives with the system-catalog work in milestone 7.

```go
type Catalog interface {
    LookupTable(name parser.ObjectName) (*Table, bool)
    LookupColumn(table *Table, name string) (*Column, bool)

    // CreateTable / DropTable mutate the catalog. The executor calls
    // them when it processes CreateTableStmt / DropTableStmt.
    CreateTable(name parser.ObjectName, cols []Column) (*Table, error)
    DropTable(name parser.ObjectName) error

    // RelFileNode returns the storage manager identity for a table.
    // The catalog assigns OIDs at CreateTable time.
    RelFileNode(table *Table) storage.RelFileNode
}

type Table struct {
    Schema   string
    Name     string
    Columns  []Column
    OID      uint32 // assigned by Catalog at CreateTable time
}

type Column struct {
    Name      string
    Type      Type
    NotNull   bool
    Ordinal   int  // 0-based position in the heap tuple
}

type Type struct {
    Name string  // "int4", "text", "char", "timestamp", …
    Args []int64 // typmod (e.g. char(22) -> [22])
}
```

`Type` deliberately stays string-shaped for v0 — the type system is a
follow-up. The executor will cast based on `Type.Name` until then.

### Plan nodes

We model logical plan nodes (the executor lowers them to physical
operators directly — there's no separate physical plan in v0). Each
node carries an `Output` schema so callers can address columns by
ordinal without re-resolving names.

| Node           | Inputs              | Output              | Notes                        |
| -------------- | ------------------- | ------------------- | ---------------------------- |
| `SeqScan`      | (heap relation)     | all heap columns    | full table scan              |
| `IndexScan`    | (relation + index)  | all heap columns    | v0: only for `col = const`   |
| `Filter`       | child               | child's output      | applies a predicate          |
| `Project`      | child               | targetlist columns  | computes target expressions  |
| `Sort`         | child               | child's output      | ORDER BY                     |
| `Limit`        | child               | child's output      | LIMIT/OFFSET                 |
| `Insert`       | (rows or child)     | RETURNING columns   | writes to a heap relation    |
| `Update`       | child               | RETURNING columns   | overwrites visible rows      |
| `Delete`       | child               | RETURNING columns   | marks visible rows dead      |
| `Values`       | (literal rows)      | row schema          | feeds Insert when no SELECT  |
| `DDL`          | —                   | none                | CREATE/DROP/TRUNCATE/ALTER   |
| `Transaction`  | —                   | none                | BEGIN/COMMIT/ROLLBACK        |
| `Utility`      | —                   | none                | VACUUM/ANALYZE/SHOW/SET      |

Each node has a `Pos()` (inherited from the parser) so executor
errors can reference the original SQL position.

### Planning rules (v0)

For each statement shape:

1. **SELECT**:
   - Resolve `From[0]` to a `*catalog.Table` (single-table only in v0).
   - Build a `SeqScan` over it (IndexScan promotion is rule-based —
     activated when `Where` is `col = const`/`col = $N` and `col`
     has a B-tree index of the right type).
   - Wrap `Filter` if `Where != nil`.
   - Wrap `Sort` if `OrderBy != nil`.
   - Wrap `Limit` if `Limit != nil` or `Offset != nil`.
   - Wrap `Project` if the target list isn't `*` or doesn't already
     project the full row — the executor's protocol-write path needs
     a fixed output schema.
2. **INSERT**: build a `Values` from the literal rows; emit `Insert`
   with the resolved target table and column ordinals.
3. **UPDATE**: `SeqScan` the target → `Filter(Where)` → `Update` with
   per-column assignment expressions resolved against the scan
   schema.
4. **DELETE**: `SeqScan` → `Filter(Where)` → `Delete`.
5. **CREATE TABLE / DROP TABLE / TRUNCATE / ALTER TABLE / CREATE
   INDEX / DROP INDEX**: emit a `DDL` node carrying the original
   statement; the executor's DDL path handles them directly.
6. **BEGIN / COMMIT / ROLLBACK**: emit a `Transaction` node with the
   verb.
7. **VACUUM / ANALYZE / SHOW / SET / RESET**: emit a `Utility` node.

Name resolution is done in one bottom-up pass:

- `parser.ColumnRef` → `(tableIndex, colOrdinal)` against the
  `FromList` of the enclosing SELECT/UPDATE/DELETE.
- `parser.StarExpr` → expanded into one `ColumnRef` per column of the
  source relation.
- `parser.ParamRef` → kept as-is; the executor binds parameter values
  at `Execute` time.

Errors are `*PlanError{Pos, Code, Message}` with `Code` from the
generated SQLSTATE table (e.g. `42P01` `undefined_table`,
`42703` `undefined_column`).

### What v0 does NOT cover

- Cost-based path selection / join ordering. Pgbench is single-table.
- Subqueries, CTEs, set operations.
- GROUP BY / HAVING / aggregates over multiple rows.
- Index selection beyond the simple `col = const` rule.
- View resolution; views require catalog support that lands later.
- Prepared-statement plan caching. Each Bind currently re-plans.

### Concurrency

The catalog and planner are pure functions over their inputs. The
in-memory catalog uses a `sync.RWMutex` so DDL serialises against
read traffic; v0 doesn't yet have a transaction-aware catalog —
DDL is committed-or-rolled-back at the catalog level the moment the
statement runs.

## Alternatives Considered

- **Skip the planner, generate execution code directly from the AST.**
  Tempting at v0 scale but the executor will need the same shape later
  (when JOINs and aggregates land). Splitting now keeps the parser
  free of executor-shaped code.
- **Vendor `pg_query`'s analyzer + use upstream's `Plan` nodes.**
  Same trade-off as the parser: massive surface area, Cgo, mismatched
  names. The plan-node names here line up with upstream's
  `nodeTag`s so a future port stays mechanical.
- **Persist the catalog in `pg_class`/`pg_attribute` from day one.**
  Bootstrapping that requires the executor to be able to insert into
  system tables before there's a planner — circular. v0 keeps the
  catalog in memory and writes a one-shot persistence layer once the
  rest of milestone 6 lands.

## Consequences

- The executor can be built against a stable plan-node interface
  rather than the parser AST directly.
- DDL has a clean place to mutate catalog state.
- `EXPLAIN` becomes a thin formatter over the plan tree once it's
  needed.
