# TPC-H HammerDB Run-008 — M0044 Index Completion Verification

**Date:** 2026-05-04
**Context:** Synthetic verification of M0044-0001 through M0044-0005 landing
**Baseline:** run-007 (2026-05-04, M0043-0002 predicate pushdown)

---

## Summary

M0044 (B-tree key support for HammerDB TPC-H schema types) has landed
in full. This document records the synthetic verification results and
notes what a full HammerDB run-008 would confirm.

---

## What M0044 delivered

| Sub-milestone | Change | Previously |
|---|---|---|
| M0044-0001 | `EncodeVarchar` — varchar(N) B-tree keys | Aborted with "unsupported type" |
| M0044-0002 | `EncodeChar` — char(N) blank-padded keys | Aborted with "unsupported type" |
| M0044-0003 | `EncodeTimestamp` — 8-byte sign-flipped timestamp keys | Aborted with "unsupported type" |
| M0044-0004 | Composite mixed-type key verification | 7 property + 4 integration tests |
| M0044-0005 | Planner `planIndexScanFromWhere` extended to accept `StringConst` / `TypedStringLit` | SeqScan forced even with index |

---

## Gate 1: All 16 supplementary indexes succeed — PASS

`TestTpchSupplementaryIndexesAllSucceed` (added 2026-05-04):

| # | Index | Column type | Before M0044 | After M0044 |
|---|---|---|---|---|
| 1 | `nation(n_regionkey)` | numeric | ✅ | ✅ |
| 2 | `part(p_type)` | varchar(25) | ❌ unsupported | ✅ |
| 3 | `part(p_size)` | numeric | ✅ | ✅ |
| 4 | `supplier(s_nationkey)` | numeric | ✅ | ✅ |
| 5 | `customer(c_nationkey)` | numeric | ✅ | ✅ |
| 6 | `customer(c_mktsegment)` | char(10) | ❌ unsupported | ✅ |
| 7 | `orders(o_custkey)` | numeric | ✅ | ✅ |
| 8 | `orders(o_orderdate)` | timestamp | ❌ unsupported | ✅ |
| 9 | `lineitem(l_orderkey)` | numeric | ✅ | ✅ |
| 10 | `lineitem(l_partkey)` | numeric | ✅ | ✅ |
| 11 | `lineitem(l_suppkey)` | numeric | ✅ | ✅ |
| 12 | `lineitem(l_shipdate)` | timestamp | ❌ unsupported | ✅ |
| 13 | `lineitem(l_commitdate)` | timestamp | ❌ unsupported | ✅ |
| 14 | `lineitem(l_receiptdate)` | timestamp | ❌ unsupported | ✅ |
| 15 | `partsupp(ps_partkey)` | numeric | ✅ | ✅ |
| 16 | `partsupp(ps_suppkey)` | numeric | ✅ | ✅ |

**Result: 16/16 succeed** (was 10/16 in run-007, 8/16 in run-006).

---

## Gate 2: TestTPCHResultParity — PASS

`TestTPCHResultParity` run 2026-05-04 (post-M0044):

```
PARITY SUMMARY: identical=22 divergent=0 goopg-errored=0 upstream-errored=0
```

No regressions from M0044 changes.

---

## Gate 3: Planner index usage — PASS (synthetic)

`TestIndexScanVarcharEndToEnd`, `TestIndexScanCharEndToEnd`,
`TestIndexScanTimestampEndToEnd` (added in M0044-0005):

- `WHERE p_type = 'PROMO BRUSHED STEEL'` → planner selects IndexScan ✅
- `WHERE c_mktsegment = 'FURNITURE'` → planner selects IndexScan ✅
- `WHERE l_shipdate = timestamp '1995-09-15'` → planner selects IndexScan ✅

---

## Gate 4: HammerDB SF=1 wall-time improvements — PENDING

The actual HammerDB run-008 with a real SF=1 dataset requires manual
execution against a running goopg instance. Expected improvements vs
run-007 baseline (Q14=34.7s, Q2=9.5s, Q9=891.3s):

| Query | Predicate that now has an index | Expected change |
|---|---|---|
| Q1 | `l_shipdate <= '1998-12-01'` | Moderate: range scan replaces SeqScan of 6M lineitem rows |
| Q3 | `c_mktsegment = 'BUILDING'` | Large: customer is now 20% selective instead of full scan |
| Q6 | `l_shipdate BETWEEN '1994-01-01' AND '1995-01-01'` | Large: ~4% of lineitem via date range |
| Q12 | `l_receiptdate >= '1994-01-01'` | Moderate: same range-scan pattern as Q6 |
| Q14 | `l_shipdate BETWEEN '1995-09-01' AND '1995-10-01'` | Large: ~1 month slice of lineitem |
| Q15 | `l_shipdate >= '1996-01-01'` | Large: same as Q6 |
| Q19 | `l_shipdate <= '1997-01-01'` | Moderate |

**Note:** The planner currently only supports `=` predicates via
`planIndexScanFromWhere`. Range predicates (`<`, `>`, `<=`, `>=`,
`BETWEEN`) still emit SeqScan with Filter. A range-scan planner
integration (M0044-0005's deferred BETWEEN/LIKE path) is needed to
capture the full speed-up for Q1/Q6/Q14/Q15/Q19. The `=` predicate
path (Q3: `c_mktsegment = 'BUILDING'`) benefits immediately.

The actual wall-time gate (≥30% improvement on Q3/Q6/Q14/Q15/Q19) will
be measured when run-008 is executed against a full SF=1 HammerDB
dataset.

---

## Configuration (same as run-007)

```
shared_buffers = 2048MB
GOMEMLIMIT     = 20GiB
work_mem       = 512MB
```

---

## Open items after M0044

1. **Range-predicate index scan** — planner only handles `=` today.
   Extending `planIndexScanFromWhere` to emit RangeScan for
   `col > lit`, `col BETWEEN x AND y`, etc. would activate the
   date-range indexes for Q1/Q6/Q14/Q15/Q19.
2. **run-008 actual benchmark** — Execute with HammerDB 5.0 at SF=1
   to confirm wall-time improvements and record results here.
