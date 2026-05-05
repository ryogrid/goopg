# Milestone 0044 — B-tree key support for HammerDB TPC-H schema types

**Status:** planned
**Depends on:** Milestone 0011 (B-tree NUMERIC key support — landed),
Milestone 0003 (HammerDB TPC-H workload coverage), Milestone 0043
(MHJ executor optimisations — predicate pushdown lands first so we
have a clear reference performance baseline).
**Drives:** unblocking the supplementary index set listed in
`analysis/tpch-additional-indexes.md`, enabling Index Scan plans
for the eight TPC-H predicate columns currently stuck on
sequential scan, and laying the groundwork for compound primary
keys with mixed numeric / string / temporal components.

## Context

The current B-tree implementation (v0) accepts only `int4`,
`int8`, `numeric`, and `decimal` key columns. Every other type
trips the explicit guard at
`internal/executor/operators_ddl.go:474`:

```text
ERROR: btree v0 only supports int4 / numeric keys, got %q
```

The HammerDB TPC-H schema uses three additional types that are
**common in real OLAP workloads** and that the planner would
gladly use as scan keys if indexes were available:

| Type | Tables / columns (HammerDB TPC-H) | Queries that benefit |
|---|---|---|
| `varchar(N)` | `part.p_type`, `part.p_name`, `customer.c_mktsegment`-adjacent text columns, comments | Q9 (`p_name`-style filters), Q13 (`p_type`) |
| `char(N)` | `customer.c_mktsegment`, `lineitem.l_returnflag`, `lineitem.l_linestatus`, `lineitem.l_shipmode`, `orders.o_orderpriority`, etc. | Q3 (`c_mktsegment`), Q5 (`r_name`), Q12 (`l_shipmode`) |
| `timestamp` | `orders.o_orderdate`, `lineitem.l_shipdate`, `lineitem.l_commitdate`, `lineitem.l_receiptdate` | Q1 / Q3 / Q4 / Q5 / Q6 / Q7 / Q8 / Q10 / Q12 / Q14 / Q15 / Q20 (every date-range filter) |

Run-006 / run-007 demonstrated that 8 of the 16 supplementary
indexes (`bench/tpch/extra_indexes.sql`-equivalent in
`/tmp/run_full3.sh`) fail at CREATE-INDEX time because of this
limitation. The Q9 / Q20 wall-time improvements obtainable from
M0043's predicate-pushdown / subquery-unnest work are bounded
by the SeqScan cost on `lineitem`, `part`, `orders`, and
`customer`. Until M0044 lands those tables remain SeqScan-only
in goopg.

This milestone closes that gap. **Both single-column and
compound (multi-column) indexes must work** for every type pair
the schema needs — `(p_partkey numeric, l_partkey numeric)` is
already supported, but `(c_mktsegment char(10), c_custkey numeric)`
or `(l_shipdate timestamp, l_orderkey numeric)` are not.
Likewise both **PRIMARY-KEY (unique)** and **secondary
(non-unique)** index paths must accept the new types.

## Goals

1. **Key encoding for `varchar(N)`** producing a self-terminating
   byte string that, under bytewise comparison, matches `text`
   collation order (the default `C` locale, since goopg has no
   collation framework yet). Single-column and as a component
   in compound keys.

2. **Key encoding for `char(N)`** with PostgreSQL's
   trailing-space-trimmed comparison semantics. (Unlike
   `varchar`, `char(10)` containing `'B'` and the same column
   containing `'B         '` (`'B' || space*9`) compare equal in
   PostgreSQL — the encoding must collapse them to the same
   bytes.)

3. **Key encoding for `timestamp` (without time zone)** —
   8-byte sortable encoding of the underlying microseconds-
   since-2000 representation goopg already uses internally.

4. **Compound indexes with mixed types** — every pair of
   `{int4, int8, numeric, varchar, char, timestamp}` (and beyond
   2-column ones) must produce keys that compare correctly
   under bytewise comparison. The existing
   `encodeCompositeBTreeKey` function in
   `internal/executor/operators_ddl.go` concatenates per-column
   encodings; each new encoding must be **self-terminating**
   so the concatenation is unambiguous.

5. **Index-scan planner integration** — once a varchar / char /
   timestamp index exists, the planner must actually use it for
   equality and range predicates (`p_type = 'STANDARD'`,
   `l_shipdate >= '1995-01-01' AND l_shipdate < '1996-01-01'`).
   Today the planner emits only `IndexScan` for numeric/integer
   key types because the type guard short-circuits earlier.

6. **Symmetric encoding** — backfill (table → index build) and
   probe (literal → index lookup) must produce identical bytes
   for equal values. The same `encodeBTreeKeyForColumn`
   abstraction extends to the new types so the symmetry
   guarantee is automatic.

## Non-goals

- **Locale-aware text comparison**: goopg has no collation
  framework. `varchar` keys use byte-wise (`C` locale) ordering.
  PostgreSQL's `LC_COLLATE='C'` produces the same ordering, so
  this is parity-correct on a default-collation cluster.
- **`text` (unbounded) keys**: not used by the HammerDB TPC-H
  schema. Easy to add later — the varchar encoding scheme
  generalises.
- **Other temporal types** (`date`, `time`, `timestamptz`,
  `interval`): again not used by HammerDB TPC-H. The encoding
  approach generalises (date → 4-byte day count, etc.) and can
  land in a follow-up.
- **Functional / expression indexes**: still v0 — index keys
  remain raw column values.
- **GIN / GiST / hash access methods**: out of scope; only
  `btree` is in v0.

## Required Design Docs

- `0044-0001-varchar-key-encoding.md` — variable-length
  byte-wise encoding with a 0x00 terminator and 0x01 escape; how
  it composes inside multi-column keys.
- `0044-0002-char-key-encoding.md` — fixed-N input, trailing-space
  trim before encoding; collapses to the varchar layout
  thereafter.
- `0044-0003-timestamp-key-encoding.md` — 8-byte big-endian
  sign-flipped microseconds-since-epoch (mirrors `EncodeInt8`).
- `0044-0004-compound-mixed-types.md` — verifies that
  concatenating self-terminating encodings of any
  `{int4, int8, numeric, varchar, char, timestamp}` produces a
  key whose bytewise comparison matches SQL multi-column
  ordering.
- `0044-0005-index-scan-planner-integration.md` — extends the
  planner's index-eligibility predicate so `=` / `<` / `>` /
  `<=` / `>=` / `BETWEEN` against the new types pick up the
  index.

## Definition of Done

1. `CREATE INDEX … ON part (p_type)` succeeds (today errors).
2. `CREATE INDEX … ON customer (c_mktsegment)` succeeds.
3. `CREATE INDEX … ON lineitem (l_shipdate)` succeeds.
4. `CREATE INDEX … ON lineitem (l_shipdate, l_orderkey)`
   succeeds — compound mixed types.
5. `CREATE UNIQUE INDEX … ON part (p_name, p_partkey)` succeeds
   — unique compound mixed types.
6. New unit tests under `internal/access/btree/` cover encode +
   compare for each new type, including round-tripping through
   `RangeScan`.
7. New integration test under
   `internal/executor/storage_ddl_*.go` builds a TPC-H-shape
   schema with all 16 supplementary indexes and verifies row
   counts via index scan.
8. `TestTPCHResultParity` still identical=22 divergent=0 errored=0.
9. `analysis/tpch-hammerdb-run-008.md` documents an SF=1 power
   test re-run with all 16 supplementary indexes built; Q3 / Q6
   / Q14 / Q15 / Q19 wall times must improve by **≥ 30 %**
   relative to run-007 to confirm the planner is actually using
   the new indexes.

## Workflow

1. Land 0044-0001 (varchar) — encoding + unit tests + DDL guard
   relaxation. Single-column varchar index works.
2. Land 0044-0002 (char) — trim-pad logic on top of varchar
   encoding.
3. Land 0044-0003 (timestamp) — 8-byte encoding mirroring int8.
4. Land 0044-0004 (compound mixed) — exhaustive composite-key
   test matrix.
5. Land 0044-0005 (planner integration) — IndexScan eligibility
   for the new types; range predicates trigger
   `RangeScan(min,max)`.
6. Re-run end-to-end (run-008 report) — bench delta + parity
   gate.

Each landing is a self-contained commit on `perf-analysis`.
