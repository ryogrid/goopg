# ANALYZE Statistics (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [root-0011-planner.md](root-0011-planner.md), [root-0012-executor.md](root-0012-executor.md), [0016-vacuum-and-analyze.md](root-0016-vacuum-and-analyze.md) |
| Supersedes | —                                                      |

## Problem

The cost-based planner item in M0003 needs cardinality
estimates. v0's existing `vacuum.Analyze` returned
table-level row count + average width, but didn't write the
result anywhere reachable by the planner. ANALYZE the SQL
statement was a no-op (utilityNoOp).

This loop closes both gaps: ANALYZE drives a real stats
collector, and the catalog now has fields the planner can
read.

## Upstream reference

- `postgres/src/backend/commands/analyze.c` — `do_analyze_rel`,
  the per-relation driver upstream uses.
- `postgres/src/include/catalog/pg_statistic.h` — per-column
  stat shape (`stadistinct`, `stanullfrac`, `stawidth`, MCV /
  histogram arrays).
- `postgres/src/include/catalog/pg_class.h` — `reltuples`,
  `relpages`.

## Decisions

### Per-table + per-column, no histograms / MCV in v0

The catalog gains:

```go
type TableStats struct {
    RowCount int64        // upstream's reltuples
    Pages    int          // upstream's relpages
    AvgWidth float64      // bytes-per-row average
    Columns  []ColumnStats
}

type ColumnStats struct {
    NDistinct int64       // upstream's stadistinct (positive form)
    NullFrac  float64     // upstream's stanullfrac
}
```

Histograms and MCV lists are intentionally out of scope. v0's
cost model can do useful work with NDistinct alone — the
join-cardinality estimate `|A| * |B| / max(NDistinct(A.k),
NDistinct(B.k))` is correct under the upstream uniform-
distribution assumption.

### Full-table scan, not sampling

Upstream PG samples up to 30,000 rows by default (controlled
by `default_statistics_target`). v0 does a full-table scan,
which is correct but slow on large relations. Sampling is a
follow-up optimisation; correctness comes first.

### `analyzeRelation` walks the heap directly

The collector lives in
`internal/executor/operators_analyze.go` rather than the
`internal/vacuum` package because it needs the executor
codec to decode tuple bodies into typed Datums. The flow:

1. Begin a fresh ReadCommitted transaction; capture a
   snapshot.
2. For each block in the relation, pin it through the buffer
   pool, read every LP_NORMAL line pointer.
3. Visibility-filter via `mvcc.TupleVisible`.
4. `DecodeRow(tbl.Columns, t.Data)` produces a Datum row.
5. Per-column distinct-set tracking via the existing
   `datumKey` canonicalisation. Per-column null counter for
   NullFrac.
6. After the walk, divide totals to populate AvgWidth /
   NullFrac.
7. Roll the transaction back; the snapshot is the only state
   it touched.

`analyzeOp` (the new operator) calls `analyzeRelation` per
target table and stores the result via
`Catalog.SetTableStats`.

### SQL ANALYZE wired through the executor

Previously `*planner.Utility` carrying an `AnalyzeStmt` was
routed to `utilityNoOp`. Now the executor's Build dispatch
type-checks the inner Stmt: if it's `AnalyzeStmt`, instantiate
`analyzeOp`; otherwise fall back to the no-op (VACUUM still
goes through that path until its full-relation prune is
wired through a stmt-driven entry).

### Null-targets behaviour: skip, don't crash

`ANALYZE;` (no target list) is upstream's "every user table"
shape. v0's InMemory catalog doesn't yet expose a public
iterator, so the bare form analyses nothing — same as
before. Listing target relations is the documented use.

## Verification

End-to-end against `goopg start -D <dir>` with upstream psql
18.3:

```
CREATE TABLE t (id INT, label TEXT);
INSERT INTO t VALUES (1,'a'),(2,'a'),(3,'b'),(4,'b'),(5,'c'),(6,'c'),(7,'c');
ANALYZE t;     -- returns "ANALYZE"
SELECT * FROM t WHERE id = 5;
-- 1 row (verifies the table is still queryable post-ANALYZE)
```

`TestAnalyzeRelationPopulatesStats` pins the in-process
behaviour: 7 rows seeded with 3 distinct labels yield
`RowCount=7`, `Columns[0].NDistinct=7` (id), and
`Columns[1].NDistinct=3` (label).

## Out of scope (deferred to subsequent loops)

- Sampling. Upstream uses ANALYZE sampling targets;
  full-table scan is the v0 baseline.
- MCV (most-common-values) lists. Needed for skewed-data
  selectivity.
- Equi-width or equi-depth histograms. Needed for range
  predicates.
- Catalog-snapshot persistence: Stats live in memory. Across
  server restart, ANALYZE has to run again. The catalog-
  persistence machinery serialises Tables but not their
  Stats yet.
- Cross-column / multivariate stats (CREATE STATISTICS
  upstream).
- ANALYZE's own progress reporting / per-row sample noise.

## Cross-references

- M1 vacuum/analyze design:
  [root-0016-vacuum-and-analyze.md](root-0016-vacuum-and-analyze.md).
- Cost-based planner (the consumer of these stats — still
  open as of this loop).
- Hash-join algorithm choice (will use NDistinct for
  build-side selection):
  [0003-0002-join-executors.md](0003-0002-join-executors.md).
