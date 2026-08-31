# 0011-0003 — HammerDB TPC-H NUMERIC Index Validation

**Status:** accepted
**Milestone:** [0011 — B-tree NUMERIC Key Support](../../milestones/0011-btree-numeric-key-support.md)
**Spans seam:** end-to-end validation of NUMERIC B-tree CREATE INDEX
through HammerDB-shaped tables.
**Cross-links:**
[0011-0001](0011-0001-btree-numeric-key-ordering.md) (encoding contract),
[0011-0002](0011-0002-btree-numeric-build-and-uniqueness.md)
(DDL acceptance + backfill + variable-length HighKey),
[0003-0004](0003-0004-hammerdb-tpch-integration.md)
(HammerDB TPC-H integration baseline).

## Context

HammerDB's TPC-H driver
([HammerDB/src/postgresql/pgolap.tcl](../../HammerDB/src/postgresql/pgolap.tcl)
lines 511-544) issues 24 CREATE INDEX statements after the data load.
The single-column ones target NUMERIC primary or foreign-key columns:

| HammerDB index name             | DDL                                                  |
|---------------------------------|------------------------------------------------------|
| REGION_PK                       | `CREATE INDEX ... ON region (r_regionkey)`           |
| NATION_PK                       | `... ON nation (n_nationkey)`                        |
| SUPPLIER_PK                     | `... ON supplier (s_suppkey)`                        |
| PART_PK                         | `... ON part (p_partkey)`                            |
| CUSTOMER_PK                     | `... ON customer (c_custkey)`                        |
| ORDERS_PK                       | `... ON orders (o_orderkey)`                         |
| ORDER_CUSTOMER_FKIDX            | `... ON orders (o_custkey)`                          |
| PARTSUPP_PART_FKIDX             | `... ON partsupp (ps_partkey)`                       |
| PARTSUPP_SUPPLIER_FKIDX         | `... ON partsupp (ps_suppkey)`                       |
| SUPPLIER_NATION_FKIDX           | `... ON supplier (s_nationkey)`                      |
| CUSTOMER_NATION_FKIDX           | `... ON customer (c_nationkey)`                      |
| NATION_REGIONKEY_FKIDX          | `... ON nation (n_regionkey)`                        |
| IDX_LINEITEM_ORDERKEY_FKIDX     | `... ON lineitem (l_orderkey)`                       |

Pre-M0011 every one of these aborted at the executor with
`ERROR: btree v0 only supports int4 keys, got "numeric"`. M0011-0001
delivered the encoding contract and M0011-0002 wired it through DDL
+ backfill + index scan; this slice's job is to **prove the wiring is
correct end-to-end against the same TPC-H schema HammerDB sees**, so
the failure class can no longer reproduce.

The four multi-column composite indexes (`PARTSUPP_PK`, `LINEITEM_PK`,
`LINEITEM_PART_SUPP_FKIDX`) are explicitly out of B-tree v0 scope per
the milestone — they remain rejected with `0A000` and are not part of
this slice's success criteria.

## What lands

### Planner: NumericConst on the IndexScan key

`planIndexScanFromWhere` in `internal/planner/planner.go` previously
accepted only `*IntegerConst` and `*ParamRef` as the rhs of a
candidate `col = const` predicate. It now accepts `*NumericConst`
too — necessary so a query like `SELECT * FROM orders WHERE
o_orderkey = 12345` (where the literal parses as
`NumericConst("12345")` because `o_orderkey` is `NUMERIC`) actually
emits an `IndexScan` rather than falling back to a sequential
predicate filter.

The executor side already handles this — `encodeBTreeKeyForColumn`
encodes via `EncodeNumericKey` when the column type is numeric and
the literal evaluates to either `KindNumeric` or `KindInt`.

### Validation test: TPC-H-shaped end-to-end

A new `internal/executor/tpch_numeric_index_test.go` brings up a real
DDL fixture, runs the seven CREATE TABLE statements from
`internal/testutil/tpch.DDL()`, inserts a small consistent slice of
TPC-H rows, then runs the 13 single-column CREATE INDEX statements
above. Every one must succeed. After that, a couple of equality
queries probe the new indexes via the live executor stack to confirm
the encoded probe keys match the encoded backfill keys.

The test serves as the regression guard for "ERROR: btree v0 only
supports int4 keys, got 'numeric'" appearing again — a reintroduction
of the int4-only restriction would fail this test deterministically
without needing the HammerDB harness running.

### Multi-column composite index handling

Out of scope. A separate test asserts that a multi-column
`CREATE INDEX … ON lineitem (l_partkey, l_suppkey)` cleanly returns
`0A000 only single-column btree indexes are supported in v0`,
documenting the boundary so a future loop adding composite support
can flip the assertion.

## Validation outside Go (manual)

After this slice lands, `bench/tpch/run_all.sh` should reach the
"CREATING TPCH INDEXES" milestone without aborting on the four
single-column NUMERIC indexes the milestone calls out. The four
multi-column composite indexes are still rejected with `0A000` and
the run ultimately stops there — completing the full HammerDB suite
requires composite-index work that's outside M0011's scope.

## Out of scope

- Multi-column composite NUMERIC indexes (separate milestone).
- Range-predicate (`<`, `<=`, `>`, `>=`) IndexScan emission for
  NUMERIC columns — only equality is wired here, matching the
  pre-existing int4 IndexScan behaviour.
- Performance tuning of the variable-length HighKey path.
- An automated bench/tpch/run_all.sh CI hook (the script needs an
  external HammerDB tarball + Postgres install).
