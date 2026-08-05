# 2026-08-05 — P5.6-f-vii: `estimate_num_groups`, and the empty TIMEOUT set

Instruments:

- `cmd/estimate-audit --label 2026-08-05-p56fvii --port 65433 --db tpch
  --timeout 150s` → `2026-08-05-p56fvii.txt` (+ `.plans.txt`), against the
  TPC-H goopg cluster (SF=1, HammerDB load) started under the cgroup cap at
  `GOGC=100 GOMEMLIMIT=12GiB`. Baseline: `2026-08-05-p56fvi-postfix.txt`.
- `scripts/tpcds-sf05-regression.sh sweep` →
  `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260805-112902.txt`
  (+ `plans-20260805-112902.txt`). Baseline: `sweep-20260805-101345.txt`.

The narrative is doc 09 §5.19. This file records only what the artefacts say.

## 1. The change

`estimateAggregate` (`internal/planner/cardinality.go`) delegated to a new
`estimateNumGroups`, the port of `estimate_num_groups`
(`postgres/src/backend/utils/adt/selfuncs.c:3449`). What it replaced was two
rules, not one:

- multi-key GROUP BY → `child / 2`;
- single bare-ColumnRef GROUP BY → the column's whole-table NDistinct, **with
  no clamp**, so a grouped scan of 5 surviving rows could claim 200 groups.

## 2. Q47 — the query the item said it would not fix

| node | before | after | PG 18.3 |
|---|---|---|---|
| `HashAggregate (6 keys)` (`v1` body) | 3 626 | 7 252 | 7 643 |
| `CTE Scan on v1` (post-filter outer) | 6 | 12 | — |
| top block | `Nested Loop rows=1958` over `Hash Join rows=108` | `Hash Join (INNER, build=left) rows=7252` | Merge Join |
| verdict | TIMEOUT (300 s) | **PASS, 12 s, 100 rows** | — |

7 252 is not a new formula: six keys over 7 252 input rows cannot make more
than 7 252 groups, so it is the `input_rows` clamp. The CTE body is scanned
three times (`v1`, `v1_lag`, `v1_lead`), so halving it halved the apparent cost
of every rescan — the exact resume point M0127-P5.6-f-viii had written down.

## 3. DS05 sweep (`sweep-20260805-112902.txt`)

```
=== SUMMARY: PASS=95 (57 ck-verified, 38 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4 ===
PASS       +Q47
TIMEOUT    -Q47
=== PLAN-SHAPE: queries=99 same=40 changed=59 added=0 removed=0 ===
```

Per §5.16 the delta is named, not counted: one verdict change, and it is Q47.
`TIMEOUT=0` is the first such sweep since §5.15. Zero runtime moves ≥ 2×.

## 4. Estimate audit (`2026-08-05-p56fvii.txt`)

Exit 1, which is the *unchanged* standing state: Q18's final SEMI is the corpus's
only violation and it improved again, 23 433× → 23 015× over. No new violation.

Every other joinrel moves under 1 % (ANALYZE sampling noise between runs) except
Q20, which improves materially because its inner aggregate no longer feeds a
halved row count into the joins above it:

| Q20 joinrel | before | after | actual |
|---|---|---|---|
| `d2` Hash Join (SEMI) | 77 462 (30.2× over) | 63 875 (24.9× over) | 2 568 |
| `d3` Hash Join (INNER) | 715 931 (3.0× over) | 561 004 (2.4× over) | 236 624 |

## 5. What is NOT in this change

Four `estimate_num_groups` refinements, each ledgered with a resume point:
equivalence-class de-duplication (step 3), `estimate_multivariate_ndistinct`,
the boolean short-circuit, and the volatile-grouping-expression arm. Plus the
standing sibling gap: `estimateSetOp`'s non-ALL `/2` and the `*Distinct` /
`*DistinctOn` arms still run no group estimate — deliberately, so this sweep
measures one variable.
