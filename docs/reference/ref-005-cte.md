# REF-005: CTE / WITH Clause

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### MATERIALIZED / NOT MATERIALIZED

PostgreSQL materialises CTE results by default (the CTE body runs
once and its result is shared among all consumers). This acts as
an **optimisation fence** — the planner cannot push predicates
into or pull predicates out of a CTE.

`WITH ... AS NOT MATERIALIZED` overrides this behaviour, inlining
the CTE body into the consuming query like a subquery or derived
table.

goopg always inlines CTE bodies (equivalent to `NOT MATERIALIZED`).

### SEARCH and CYCLE Clauses (PG 14+)

PostgreSQL supports two clauses for recursive CTEs:

**SEARCH** — controls traversal order:
```sql
WITH RECURSIVE r AS (
    SELECT ... UNION ALL SELECT ...
) SEARCH DEPTH FIRST BY id SET order_col
```

**CYCLE** — detects cycles and stops:
```sql
WITH RECURSIVE r AS (
    SELECT ... UNION ALL SELECT ...
) CYCLE id SET is_cycle USING path
```

goopg does not support SEARCH or CYCLE.

### Recursive CTE WorkTableScan

PostgreSQL's recursive CTE executor uses two plan nodes:

- **RecursiveUnion** — drives the fixpoint iteration:
  1. Execute the non-recursive (anchor) term → `working table`.
  2. Execute the recursive term with the working table as input
     → `new working table`.
  3. Repeat until `new working table` is empty.
- **WorkTableScan** — reads rows from the current working table.
  Used inside the recursive term to reference the CTE name.

goopg implements the same nodes with the same semantics.

### CTE in DML

PostgreSQL supports CTEs in INSERT/UPDATE/DELETE:

```sql
WITH deleted AS (DELETE FROM t WHERE id = 1 RETURNING *)
INSERT INTO t_audit SELECT * FROM deleted;
```

goopg may support this (the parser handles WITH for all
statement types), but it is not tested.

## goopg Improvement Analysis

### P2: SEARCH and CYCLE

Implement SEARCH (DEPTH/BREADTH FIRST) and CYCLE detection for
recursive CTEs. These are essential for graph traversal queries.

### P2: CTE Materialisation

Add a `Materialized` flag to the CTE plan node. When set,
materialise the CTE result once and share among consumers.
When unset (default in goopg), inline as currently.

## References

- goopg: `internal/planner/with.go`
- goopg: `internal/executor/operators_recursive_cte.go`
- PG CTE: `postgres/src/backend/parser/parse_cte.c`
- PG recursive union: `postgres/src/backend/executor/nodeRecursiveunion.c`
- PG worktable scan: `postgres/src/backend/executor/nodeWorktablescan.c`
