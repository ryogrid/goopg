# pg_catalog Views (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [0003-0004-hammerdb-tpch-integration.md](0003-0004-hammerdb-tpch-integration.md) |
| Supersedes | —                                                      |

## Problem

HammerDB's `checkschema` step probes the v0 catalog through
upstream's `pg_catalog` views to verify each TPC-H table has at
least one index after `CreateIndexes` has run:

```sql
SELECT tablename, indexname FROM pg_indexes WHERE tablename = '<t>';
```

When the result set is empty HammerDB raises
`TPROC-H Schema check failed ... no indices` and aborts. Without
`pg_indexes`, the entire run fails before the workload starts —
so the milestone bullets that depend on a successful schema
check (`buildschema`, the Power Test, the per-query
verification) couldn't proceed regardless of how well planning
or execution worked downstream.

## Decisions

### `pg_catalog.pg_indexes` as a virtual view

A second virtual table joins `pg_catalog.pg_class` (already
seeded since the pgbench loops). Same pattern: `Virtual=true`,
a `VirtualRows() [][]string` provider iterates the catalog's
`byTable` map. Columns mirror upstream's view definition:

- `schemaname text`
- `tablename text`
- `indexname text`
- `tablespace text` (always empty in v0)
- `indexdef text` (always empty in v0)

The provider walks user (non-virtual) tables, then their
indexes, in deterministic key order so output is stable across
calls (helpful for tests and EXPLAIN). HammerDB only reads
`tablename` and `indexname`, so the empty `tablespace` /
`indexdef` strings don't affect its check.

### Implicit `pg_catalog` search_path

`LookupTable` previously matched the schema strictly, so
`SELECT FROM pg_indexes` (unqualified, as HammerDB writes it)
missed `pg_catalog.pg_indexes`. Upstream PG resolves this via
the `search_path` GUC, whose default value implicitly prepends
`pg_catalog`. v0 doesn't yet honor a configurable
`search_path`, so we added a narrow fallback: when an
unqualified lookup misses, retry under the `pg_catalog` schema.
This affects only catalog-served names; user tables stay where
they are because they always match the strict path first.

The same fallback also fixes any future `pg_class` /
`pg_settings` / etc. queries that the user writes without a
schema prefix.

## Verification

`TestPgIndexesView` pins:
- The view exists at `pg_catalog.pg_indexes`.
- Unqualified `pg_indexes` resolves through the search_path
  fallback.
- `VirtualRows()` returns one row per index on each user
  table with the expected (tablename, indexname) values.

End-to-end against `goopg start -D <dir>` with upstream psql 18.3:

```sql
CREATE TABLE region (r_regionkey int4, r_name text);
CREATE TABLE nation (n_nationkey int4);
CREATE INDEX region_pk ON region (r_regionkey);
CREATE INDEX nation_pk ON nation (n_nationkey);

SELECT tablename, indexname FROM pg_indexes WHERE tablename = 'region';
-- region | region_pk

SELECT schemaname, tablename, indexname FROM pg_indexes ORDER BY tablename, indexname;
-- public | nation | nation_pk
-- public | region | region_pk

SELECT relname FROM pg_class WHERE relname = 'region';
-- region                       (regression check — pg_class still works)
```

## Out of scope (deferred)

- A configurable `search_path` GUC. v0 hardwires the
  `pg_catalog`-fallback rule.
- `indexdef` / `tablespace` reconstruction. v0 emits empty
  strings; upstream pg_indexes would synthesise
  `CREATE INDEX ... ON ... USING btree (...)` from
  `pg_index.indkey`, which v0 doesn't track.
- Other pg_catalog views HammerDB doesn't probe yet
  (`pg_namespace`, `pg_attribute`, `pg_index`).
