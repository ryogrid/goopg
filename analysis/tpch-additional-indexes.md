# TPC-H Additional Index Set (Phase 3 of run-006/run-007 orchestration)

**Date:** 2026-05-04
**Context:** part of the HammerDB end-to-end runs documented in
`tpch-hammerdb-run-006.md` and the upcoming run-007 report.

## Background

HammerDB's `pgolap.tcl::CreateIndexes` builds 16 indexes for
TPC-H (8 PRIMARY KEY constraints + 8 secondary FK-style indexes).
Those are correct for vanilla PostgreSQL but they are **not the
full set the typical TPC-H workload expects** — the canonical
HammerDB index set covers join keys but misses many of the
predicate columns that turn a sequential scan into an index scan
in queries like Q3 (`o_orderdate`), Q6 (`l_shipdate`), Q15
(`l_shipdate`), Q12 (`l_receiptdate`, `l_commitdate`), Q13
(`p_type`), and the multi-segment customer scan in Q3
(`c_mktsegment`).

The run-006 / run-007 orchestration scripts (`/tmp/run_full3.sh`,
Phase 3) therefore add a **supplementary** set of 16 indexes via
`psql` after HammerDB's schema build completes. The list is:

| # | Statement | Column type | Status on goopg today |
|---|---|---|---|
| 1 | `CREATE INDEX … ON nation (n_regionkey)` | numeric | ✅ |
| 2 | `CREATE INDEX … ON part (p_type)` | varchar | ❌ btree v0 unsupported |
| 3 | `CREATE INDEX … ON part (p_size)` | numeric | ✅ |
| 4 | `CREATE INDEX … ON supplier (s_nationkey)` | numeric | ✅ |
| 5 | `CREATE INDEX … ON customer (c_nationkey)` | numeric | ✅ |
| 6 | `CREATE INDEX … ON customer (c_mktsegment)` | char | ❌ btree v0 unsupported |
| 7 | `CREATE INDEX … ON orders (o_custkey)` | numeric | ✅ |
| 8 | `CREATE INDEX … ON orders (o_orderdate)` | timestamp | ❌ btree v0 unsupported |
| 9 | `CREATE INDEX … ON lineitem (l_orderkey)` | numeric | ✅ |
| 10 | `CREATE INDEX … ON lineitem (l_partkey)` | numeric | ✅ |
| 11 | `CREATE INDEX … ON lineitem (l_suppkey)` | numeric | ✅ |
| 12 | `CREATE INDEX … ON lineitem (l_shipdate)` | timestamp | ❌ btree v0 unsupported |
| 13 | `CREATE INDEX … ON lineitem (l_commitdate)` | timestamp | ❌ btree v0 unsupported |
| 14 | `CREATE INDEX … ON lineitem (l_receiptdate)` | timestamp | ❌ btree v0 unsupported |
| 15 | `CREATE INDEX … ON partsupp (ps_partkey)` | numeric | ✅ |
| 16 | `CREATE INDEX … ON partsupp (ps_suppkey)` | numeric | ✅ |

All `CREATE INDEX` statements are issued with `IF NOT EXISTS`, so
the FK-style indexes that HammerDB already created (e.g.
`lineitem(l_orderkey)`) silently no-op. The run-006 timing for
this phase was **2,179 s ≈ 36 min**, dominated by the three
new lineitem indexes (`l_partkey`, `l_suppkey`, `l_orderkey` if
the FK didn't already cover it) — each needs a sort-and-write
pass over 6 M rows.

## Why some of these matter for the power test

The TPC-H queries that benefit most from the supplementary set:

- **Q1 / Q6 / Q15** — date-range filter on `lineitem.l_shipdate`
  is the entire selectivity. With an index on `l_shipdate`, the
  planner could replace the SeqScan with a range Index Scan
  (modest win on SF=1, large win at higher scale).
- **Q3** — `c_mktsegment = 'BUILDING'` filters customer down to
  ~20 % of rows; an index on `c_mktsegment` lets the planner
  start from a tight customer set and probe joins outward.
- **Q12 / Q14 / Q19** — receipt/commit/ship date predicates;
  same pattern as Q6.
- **Q9 (Product Type Profit Measure)** — driven by
  `p_name LIKE '%green%'`. Goopg's planner already pushes this
  into the part SeqScan, but a hypothetical index on
  `p_name` (trigram or similar) is not in the supplementary set
  — `LIKE '%substring%'` is not a sargable B-tree predicate.

## Why some failures are silently tolerated

The orchestration script sets `set -uo pipefail` but **omits**
`set -e` so individual `CREATE INDEX … IF NOT EXISTS` failures
do not abort the run. Each error prints
`ERROR: btree v0 only supports int4 / numeric keys, got "<type>"`
to the log and the script proceeds. This is intentional — we
explicitly want the build to make as much progress as possible,
even on incomplete index support.

The 8 indexes that fail today are tracked under
`docs/milestones/0011-btree-numeric-key-support.md` (B-tree
NUMERIC key support). Extending the B-tree to handle
`varchar` / `char` / `timestamp` keys is the natural follow-up;
none of those are in critical-path for the M0043-0002 scope
(predicate pushdown into MHJ) which is what run-006 / run-007
were exercising.

## Source of the supplementary set

The list comes directly from `/tmp/run_full3.sh`, which is the
orchestration script the agent generates each run rather than a
checked-in artifact. The exact SQL block is reproduced below for
reference (kept here so future runs can be replayed without
needing the temporary script):

```sql
CREATE INDEX IF NOT EXISTS idx_nation_regionkey   ON nation   (n_regionkey);
CREATE INDEX IF NOT EXISTS idx_part_type          ON part     (p_type);
CREATE INDEX IF NOT EXISTS idx_part_size          ON part     (p_size);
CREATE INDEX IF NOT EXISTS idx_supplier_nationkey ON supplier (s_nationkey);
CREATE INDEX IF NOT EXISTS idx_customer_nationkey ON customer (c_nationkey);
CREATE INDEX IF NOT EXISTS idx_customer_mktsegment ON customer (c_mktsegment);
CREATE INDEX IF NOT EXISTS idx_orders_custkey     ON orders   (o_custkey);
CREATE INDEX IF NOT EXISTS idx_orders_orderdate   ON orders   (o_orderdate);
CREATE INDEX IF NOT EXISTS idx_lineitem_orderkey  ON lineitem (l_orderkey);
CREATE INDEX IF NOT EXISTS idx_lineitem_partkey   ON lineitem (l_partkey);
CREATE INDEX IF NOT EXISTS idx_lineitem_suppkey   ON lineitem (l_suppkey);
CREATE INDEX IF NOT EXISTS idx_lineitem_shipdate  ON lineitem (l_shipdate);
CREATE INDEX IF NOT EXISTS idx_lineitem_commitdate ON lineitem (l_commitdate);
CREATE INDEX IF NOT EXISTS idx_lineitem_receiptdate ON lineitem (l_receiptdate);
CREATE INDEX IF NOT EXISTS idx_partsupp_partkey   ON partsupp (ps_partkey);
CREATE INDEX IF NOT EXISTS idx_partsupp_suppkey   ON partsupp (ps_suppkey);
```

## Open questions

1. Should the supplementary index set be **checked in** as a
   `bench/tpch/extra_indexes.sql` so different runs apply an
   identical baseline? Currently every run regenerates it from
   the orchestration script.
2. Is it worth landing **partial-key support** (e.g.,
   `varchar(N)` truncated to a fixed prefix as a B-tree key) so
   `c_mktsegment` and `p_type` can be indexed without full
   variable-length B-tree work? That would be a quick win for
   Q3.
3. The HammerDB native set already creates `lineitem(l_orderkey,
   l_linenumber)` as the PK. Should we drop the redundant
   `idx_lineitem_orderkey` (single-column) when it duplicates the
   leading PK column? Goopg currently builds it; PostgreSQL would
   also create it (the planner can't use a multi-column PK as a
   single-column index in all cases), so keeping the redundancy
   is fine.
