# 0044-0005 — Index-scan Planner Integration for New Key Types

**Status:** draft
**Parent milestone:** M0044
**Date:** 2026-05-04

## 1. Objective

Once the storage layer accepts varchar / char / timestamp /
mixed-compound B-tree indexes (0044-0001 / 0044-0002 / 0044-0003 /
0044-0004), the planner must actually *use* those indexes when
the user query has a sargable predicate on the indexed column(s).
Without this step the new indexes exist on disk but the planner
keeps emitting SeqScan, defeating the purpose.

## 2. Sargability rules

A predicate is **sargable** against a B-tree index iff:

- It is `=` / `<` / `<=` / `>` / `>=` / `BETWEEN` / `IS NULL`-
  free range expression (NULLs are not indexed in v0).
- It is a conjunction of leading-column predicates: column 1
  must have an `=` predicate before column 2 can use a range
  predicate, etc. (Standard B-tree index-key access pattern.)
- The index's column type and the literal type are
  **comparison-compatible** under the new encoding rules.

Today the planner short-circuits sargability whenever the
indexed column is anything other than `int4` / `int8` / `numeric`,
because of guards mirroring `isSupportedBTreeKeyType`. M0044-0005
relaxes those guards to match the new type set.

## 3. Concrete planner edits

### 3.1 Index-eligibility predicate

Every place in the planner that decides "can this index serve
this filter?" is gated on the indexed column's type. Find these
sites by grep — the canonical entry points are:

- `internal/planner/access_path.go::canUseIndex` (or similar) —
  type compatibility check.
- `internal/planner/index_scan.go::buildIndexProbe` — translates
  literal expressions into probe-key Datums.
- `internal/planner/range_scan.go` (if separate) — translates
  half-open intervals into `(min, max)` probe-key pairs.

The eligibility predicate gains parity with
`isSupportedBTreeKeyType`. Anything the storage layer accepts as
a key, the planner is permitted to use.

### 3.2 Probe-key construction

For each new type the planner must:

- **varchar / char**: build the probe Datum as
  `Datum{Kind: KindString, String: literal}` and route the same
  bytes through `encodeBTreeKeyForColumn` as backfill does. For
  `char(N)` the planner does **not** pre-pad — encoding handles
  the trim.
- **timestamp**: parse the literal via the existing
  `parseTimestampLiteral` (used by `evalTypedStringLit`),
  package the result as `KindTime` with the microseconds-since-
  2000 carrier, and route through `EncodeTimestamp`.

Range scans use the same construction for the `min` and `max`
endpoints and call `tree.RangeScan(min, max)`.

### 3.3 Cost model

The cost model is shared between numeric and the new types —
SeqScan cost vs IndexScan cost is a pure function of cardinality
estimates and predicate selectivity. The new types do not
require new cost-model machinery; the existing
`internal/planner/cost.go` selectivity estimator is type-
agnostic above the storage layer.

## 4. Non-goals

- **Bitmap index scans / multi-index OR-elimination**: future
  optimization, out of scope.
- **Partial indexes** (with predicate): out of scope.
- **Expression indexes** (e.g., `LOWER(col)`): out of scope.
- **Substring / LIKE predicates**: B-tree cannot serve
  `LIKE '%foo%'`; even `LIKE 'foo%'` requires a partial-string
  prefix match that the planner's standard B-tree path supports
  natively as a range scan `[foo, fop)`. **This case is in
  scope** for M0044-0005 but only via the existing prefix-range
  rewrite — no new operator.

## 5. Verification

- Unit tests in `internal/planner/access_path_test.go` assert
  that for each new type, a query with `WHERE col = literal`
  picks IndexScan over SeqScan when an index exists, and that
  range predicates pick RangeScan with the right bounds.
- Integration test
  `internal/executor/index_scan_tpch_test.go` builds a TPC-H
  schema with the supplementary indexes and runs a representative
  predicate from each affected query (Q3, Q6, Q12, Q14, Q15,
  Q19), asserting the EXPLAIN output mentions IndexScan rather
  than SeqScan.
- `TestTPCHResultParity` regression gate.
- run-008 power-test wall times confirm the planner is using the
  indexes (Q3 / Q6 / Q14 / Q15 / Q19 must improve ≥ 30 % vs
  run-007 baseline).
