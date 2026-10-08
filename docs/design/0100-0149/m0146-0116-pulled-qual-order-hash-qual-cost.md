# M0146-0116 — pulled-up quals precede the parent's; hash_qual_cost prices expression keys

Status: done 2026-10-09 (d2bbb8b73). Parent: M0146-0065 (root M0146-0007).

## Problem

TPC-DS Q2 was the corpus's only D6-cte record at both scales. Its CTE
`wswscs` is referenced twice, through the FROM subqueries `y` (1998) and
`z` (1999), joined by `d_week_seq1 = d_week_seq2 - 53`. M0146-0065 ledgered
the main query's join order as the remaining difference:

| | join order |
|---|---|
| PG 18.3 | `wswscs ⋈ ((date_dim_1 ⋈ wswscs_1) ⋈ date_dim)` |
| goopg | `date_dim_1 ⋈ (wswscs_1 ⋈ (date_dim ⋈ wswscs))` |

PG prices goopg's order at 5449 against 5278 for its own (EXPLAIN on
`:65438` with the order forced). goopg found its own order cheaper for two
reasons.

1. **The class member.** goopg's middle join hashed on
   `(wswscs_1.d_week_seq - 53) = wswscs.d_week_seq`, where PG's equates
   `date_dim.d_week_seq`. The CTE column has no statistics, so its bucket
   size uses the 200-distinct default. date\_dim's restricted column gives
   PG a larger bucket, and so a dearer probe.
2. **The probe charge.** goopg charged one `cpu_operator_cost` per hash
   clause in the bucket walk. PG charges `cost_qual_eval` of the clauses,
   and here that is two operators, `-` and `=`.

## PG behaviour

- **Member order.** `deconstruct_recurse` (initsplan.c) processes a
  FromExpr's items left to right before distributing its own quals. A
  pulled-up subquery is a FromExpr item of its parent, so its WHERE is
  distributed first, a nested body's before its parent's.
  `process_equivalence` appends members in that order, and
  `generate_join_implied_equalities_normal` equates the first outer member
  to the first inner member. For Q2 the class is `[date_dim.d_week_seq,
  wswscs.d_week_seq, (wswscs_1.d_week_seq - 53)]`.
- **Probe cost.** `final_cost_hashjoin` charges
  `hash_qual_cost.per_tuple * outer_rows * clamp_row_est(inner_rows *
  innerbucketsize) * 0.5`, where `hash_qual_cost = cost_qual_eval(hashclauses)`.
  The unmatched-probe term of an inner-unique join pays the same per-tuple
  cost at `0.05`.

## Change

- **Qual order.**
  - `resolvePulledDerived` (derivedpullup.go) still resolves bodies
    innermost-first, since a body sees its children's outputs. It now
    returns their quals in post-order, in FROM order, each body after its
    own children.
  - The planner (planSelect's WHERE arm) combines the pulled quals ahead
    of the parent's WHERE.
- **Probe cost.**
  - `hashClausesPerTuple` (subplan\_cost.go) is `qualEvalOps` over the hash
    clauses, times `cpu_operator_cost`.
  - The four hash-join call sites pass it as `hashJoinInputs.hashQualCost`.
  - `hashJoinCost` uses it in the three bucket-walk terms. Zero falls back
    to the old one-per-clause charge.
  - The `cpu_operator_cost * num_hashclauses` hashing charge on both inputs
    is `initial_cost_hashjoin`'s own term and is unchanged.

A plain `a = b` key costs exactly what it did before, so only expression
keys reprice.

## Verification

- **Scratch probe** (PG 18.3 and goopg, a Q2-shaped schema).
  - The qual order alone makes the top join PG's
    `w.d_week_seq = dd.d_week_seq`.
  - Both fixes together give PG's plan node for node: goopg 3361.63
    against PG's 3362.80.
  - A pulled single-relation Filter prints PG's
    `((a > 1) AND (b < 2) AND (a < 5))`.
- **Tests.**
  - `TestPulledSubqueryQualsPrecedeParentQuals` fails at HEAD, which prints
    `m116a.a = m116b.a` on top where PG has `m116b.a = m116a.a`.
  - `TestHashQualCostChargesExpressionKeys`.
- **TPC-DS fire set** (results identical). Q2 is a full MATCH at both
  scales.

  | scale | match | join-order | scan-type | D6-cte |
  |---|---|---|---|---|
  | SF0.25 | 44 → 45 | 46 → 45 | 25 → 24 | 1 → 0 |
  | SF1 | 35 → 36 | 51 → 50 | 33 → 32 | 1 → 0 |

  Q59 fires too. Its top hash clause now names `d.d_week_seq`, the member
  PG uses for that class, and its cost rises by 6. Its categories are
  unchanged.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (19 files). Neutral, apart from one join row now in PG's
  order.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet (9/9) all PASS.

## Not covered (ledgered)

- **Residual join quals.** `joinQualPerTuple` still charges one
  `cpu_operator_cost` per residual join conjunct. PG's `qp_qual_cost` is
  `cost_qual_eval` and counts each operator.
- **ON clauses of a split chain.** The ON clauses of an inner join chain
  that `splitInnerJoinChainForPullup` splits still follow every pulled
  body's WHERE. PG distributes a JoinExpr's ON when it reaches that
  JoinExpr in FROM order.
