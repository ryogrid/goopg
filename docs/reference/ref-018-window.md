# REF-018: Window Functions

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### Frame Clause Types

PostgreSQL supports five frame boundary types:

| Type | Clause | Example |
|------|--------|---------|
| ROWS | Physical row offset | `ROWS BETWEEN 5 PRECEDING AND 5 FOLLOWING` |
| RANGE | Logical offset within partition | `RANGE BETWEEN 10 PRECEDING AND 10 FOLLOWING` |
| GROUPS | Group offset | `GROUPS BETWEEN 1 PRECEDING AND 1 FOLLOWING` |

goopg only supports the default frame (`RANGE BETWEEN UNBOUNDED
PRECEDING AND CURRENT ROW`).

### LEAD and LAG

`LEAD(expr, offset, default)` and `LAG(expr, offset, default)`
access rows at a physical offset within the partition. They are
implemented via spooling: the executor reads all rows in the
partition into a buffer, then emits them with the LEAD/LAG
values computed from the buffer.

goopg does not implement LEAD or LAG.

### Aggregate Functions with OVER

Any aggregate function (SUM, AVG, COUNT, etc.) can be used with
`OVER (PARTITION BY … ORDER BY …)` to compute a window aggregate.
For example:

```sql
SELECT sum(amount) OVER (PARTITION BY account ORDER BY date)
FROM transactions;
```

goopg does not support aggregate functions with OVER.

### WINDOW Clause

The `WINDOW` clause names a window specification for reuse:

```sql
SELECT sum(x) OVER w, avg(x) OVER w
FROM t
WINDOW w AS (ORDER BY y)
```

goopg does not support the WINDOW clause.

### EXCLUDE (PG 12+)

PostgreSQL 12+ supports frame exclusion:

- `EXCLUDE CURRENT ROW` — exclude the current row from the frame.
- `EXCLUDE GROUP` — exclude all rows with the same ORDER BY
  values as the current row.
- `EXCLUDE TIES` — include rows with the same ORDER BY values
  but exclude peers outside the frame.

goopg does not support EXCLUDE.

## goopg Improvement Analysis

### P2: LEAD / LAG

Implement LEAD and LAG by spooling partition rows into a slice.
On each row emission, look ahead (LEAD) or behind (LAG) by the
specified offset.

### P3: Aggregate Window Functions

Extend the executor's aggregate infrastructure to support OVER
(PARTITION BY). When a window clause is present, evaluate the
aggregate over the current frame instead of the current group.

## References

- goopg: `internal/executor/operators_window.go`
- PG window: `postgres/src/backend/executor/nodeWindowAgg.c`
- PG window funcs: `postgres/src/backend/utils/adt/windowfuncs.c`
