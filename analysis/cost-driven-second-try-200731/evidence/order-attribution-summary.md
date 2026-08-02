# M0126-0009 — Order-failure attribution summary

## Classification

| Query | Reg | Class | Detail | Routing |
|-------|-----|-------|--------|---------|
| TPC-H Q5 | 0.14x WIN | — | SmallDimension + NLI override working as designed | — |
| TPC-H Q7 | 1.81x | (a) cardinality | Multi-table join cardinality overestimation | -0010 |
| TPC-H Q9 | 30x+ | (a) cardinality | FK-chain cardinality explosion; each successive FK join multiplies estimate | -0010 (PRIMARY) |
| TPC-H Q10 | 1.92x | (a) cardinality | Likely same FK-chain mechanism (Q10 has lineitem ⋈ orders ⋈ customer ⋈ nation ⋈ region) | -0010 |
| TPC-H Q11 | 1.56x | (a) cardinality | Subquery/CTE cardinality misestimation (Q11 has partsupp ⋈ supplier ⋈ nation + subquery) | -0010 |
| TPC-H Q18 | 1.25x | (a) cardinality | Mild — large-table join cardinality | -0010 (low priority) |
| TPC-H Q21 | 1.37x | (a) cardinality | Multi-way join cardinality + anti-join selectivity | -0010 |
| TPC-DS Q11 | 18x+ | (a) cardinality | CTE GROUP BY cardinality off by 6-orders-of-magnitude + 4-way self-join compounding | -0010 (DS anchor) |

**All regressions are class (a) — cardinality estimate.** Zero class (b), (c), or (d).

## Mechanism

The root cause is the same across all failing queries: **ndistinct-based join
selectivity without FK constraint awareness**.

When a join chain has N tables connected by FK-PK relationships, the estimator
treats each join column as an independent random variable. The resulting
cardinality estimate is the product of N independent selectivity factors,
causing O(error^N) blowup for N-level chains.

Q9 is the clearest demonstrator:
```
nation(25) × supplier(10K) / ndistinct(nationkey) = 1250      (OK)
1250 × lineitem(6M) / ndistinct(suppkey) = 37M                 (~6x off)
37M × partsupp(800K) / ndistinct(partkey+suppkey) = 1.5e11    (massive explosion)
1.5e11 × orders(1.5M) / ndistinct(orderkey) = 1.1e15           (further explosion)
1.1e15 × part(200K) / ndistinct(partkey) = 5.9e15              (absurd)
```

The default MHJ plan masks this by packing the large tables into a single
multi-way hash join whose estimate is equally wrong (1.8e14) but whose output
is then reduced by highly selective dimension-table joins (part filter, etc.),
bringing the final estimate down to 3 rows.

## Candidates for M0126-0010

1. **FK-aware join selectivity** — When col A is a FK referencing col B (PK),
   `join_rows = max(|outer|, |inner|)` rather than the ndistinct product formula.
   This is the most principled fix and should address Q7, Q9, Q10, Q11, Q18, Q21.

2. **Join-key ndistinct cap** — `join_rows ≤ max(outer_rows, inner_rows)` for
   any equijoin (not just FK). Coarser but simpler; may hide real cross-join
   issues.

3. **CTE cardinality inheritance** — When a CTE has GROUP BY, use the GROUP BY
   key ndistinct as an upper bound on output rows, rather than estimating from
   the input relation sizes.

Priority order: (1) > (3) > (2). All are within M0126-0010 constraints (no
global NDistinct rewrite, no penalty multipliers, no shape preference).

4 commits budget. Q9 is the primary target; each fix must pass the full 22-Q
A/B gate.
