# Milestone 0003 — HammerDB TPC-H Workload Coverage

**Status:** planned
**Depends on:** Milestone 0001. Strongly benefits from Milestone 0002 (concurrent B-tree improves multi-VU scaling).
**Drives:** SQL surface and planner depth well beyond what pgbench requires.

## Context

Milestone 0001 targeted `pgbench`, a TPC-B–like OLTP workload that exercises concurrent point reads/writes against a small handful of tables. TPC-H is fundamentally different: 22 analytical queries against a star schema, stressing joins, subqueries, aggregates, and the cost-based planner. Passing TPC-H is the right next checkpoint because it forces the executor and planner to grow up before any further OLTP performance work.

HammerDB is the chosen driver: it speaks the libpq-compatible PostgreSQL protocol, requires no license, and is widely used. Its TPC-H implementation does not depend on stored procedures, which keeps it within `GOAL_AND_REQUIREMENTS.md`'s non-goals (stored procedures are explicitly out of scope). The agent must verify this assumption when porting and record any HammerDB-specific deviations in a design doc.

## In Scope

### Schema, Constraints, and Loader

- The eight TPC-H tables (`region`, `nation`, `supplier`, `customer`, `part`, `partsupp`, `orders`, `lineitem`) must be createable with the column types HammerDB's PostgreSQL build script generates.
- The data-loading path HammerDB uses (typically `COPY` or batched `INSERT`s) must work end-to-end at scale factor 1.
- Primary keys, foreign keys, and the indexes HammerDB creates must be supported. Foreign key enforcement may be omitted if necessary, but the decision and justification must be recorded in a design doc.

### Query Coverage

The 22 TPC-H queries collectively require:

- 3- to 7-way joins, with the planner choosing among nested-loop, hash, and sort-merge joins.
- Both correlated and uncorrelated subqueries; `EXISTS`, `NOT EXISTS`, `IN`, `NOT IN`.
- Aggregate functions (`SUM`, `AVG`, `COUNT`, `MIN`, `MAX`) with `GROUP BY` and `HAVING`.
- `ORDER BY`, `LIMIT` / `FETCH FIRST`.
- `CASE` expressions.
- Date and interval arithmetic, `EXTRACT`.
- Views, where HammerDB uses them.

### Planner and Executor

- Cost-based planner with cardinality estimates good enough that no TPC-H query degenerates to a Cartesian product or repeated nested-loop on a large table.
- Hash join and sort-merge join executors.
- Hash aggregate and sort-then-group aggregate paths.
- `ANALYZE` produces statistics that meaningfully feed the planner. The set of statistics gathered (n_distinct, MCV lists, histograms) should follow upstream's model closely enough that operators can reason about it; reference `postgres/src/backend/commands/analyze.c` and `postgres/src/backend/utils/adt/selfuncs.c`.

## Out of Scope

- TPC-H scale factors above SF10. Tune for SF1–SF10 first; performance work for larger scales is a later milestone.
- Parallel query execution. Single-threaded per-backend execution is acceptable; HammerDB scales by virtual-user count, not by intra-query parallelism.
- Materialized views and partitioning. Useful but not required for the 22 queries.
- Plan caching beyond what M1 already provides for prepared statements.

## Required Design Docs

- `0003-0001-planner-overview.md` (if not already produced under M1; otherwise extend the existing one)
- `0003-0002-join-executors.md`
- `0003-0003-statistics-and-cardinality.md`
- `0003-0004-hammerdb-tpch-integration.md` (records anything HammerDB-specific that goopg has to accommodate, including the verification that no stored procedures are in HammerDB's TPC-H path)

## Reference
- HammerDB's source code is cloned under `./HammerDB/` for reference. The TPC-H schema build script is at `HammerDB/tpch/postgres/ddl.sql` and the 22 queries are at `HammerDB/tpch/postgres/queries/`.

## Definition of Done

1. HammerDB's PostgreSQL TPC-H schema build script completes successfully against goopg at SF1.
2. All 22 TPC-H queries (Q1 through Q22 in HammerDB's wording) execute end-to-end and return result sets.
3. For each query, results are byte-identical or otherwise verified-equivalent to the same query against an upstream PostgreSQL on the same generated data set.
4. HammerDB's TPC-H "Power Test" run completes without errors at SF1.
5. `EXPLAIN` output for each of the 22 queries shows a sane plan: no Cartesian products, joins selected by reasonable cost estimates, aggregates grouped efficiently.
6. All required design docs merged with status `accepted`.
