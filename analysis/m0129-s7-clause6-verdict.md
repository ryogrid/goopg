# M0129-S7 — Clause-6 Re-Adjudication Verdict

**Date:** 2026-08-08
**Label:** `m0129-s7-clause6`
**Run:** `PGSHAPED=1 DP_TRACE=1 PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh m0129-s7-clause6`
**Artifacts:** `analysis/leftdeep-joins/m0129-s7-clause6.txt`, `.plans.txt`

## Background

M0128-P5.1 fixed the EXPLAIN node-label name dedup in `explain_names.go` (the
`_1`/`_2` suffix disambiguation). Before the fix, Q2/Q8/Q17/Q18/Q22 were
excluded from clause-6 adjudication with an `N ambiguous` marker because
goopg's plan labels couldn't distinguish repeated relation names, making the
spine-pairing comparison against PG impossible.

## Results

All five queries now have adjudicable spine pairings:

| Query | Status | Notes |
|-------|--------|-------|
| Q2 | ✅ Adjudicable | goopg BUSHY d1 (CROSS-QUERY-LEVEL — SubPlan boundary, not a partition); both arms have distinct pairings |
| Q8 | ✅ Adjudicable | goopg BUSHY d4 + d10; PG BUSHY d10 is CLAUSE-6-CANDIDATE — enumerated by goopg's search (OFFERED, phase=2 lev=6) |
| Q17 | ✅ Adjudicable | goopg d1 `{lineitem+part} ⋈ {lineitem_1}` vs PG d1 `{lineitem} ⋈ {lineitem_1+part}` — different join orders, both enumerated |
| Q18 | ✅ Adjudicable | All pairings at d2/d3/d4 are `both` (goopg and PG agree) |
| Q22 | ⚠️ Semantic ambiguity | `~` tilde marker: "a relation scanned twice without an alias; pairing printed, excluded from candidates". `customer` appears twice in the subquery without an alias to disambiguate. This is a **semantic** ambiguity (relation reuse), NOT the `N` rendering-gap ambiguity that P5.1 fixed. |

**Zero `N ambiguous` markers across all 22 queries.** The rendering gap is fixed.

## Spine Summary

```
queries compared: 22; pairings: 30 matched, 29 goopg-only, 27 PG-only
bushy spine chosen by: goopg 6 (Q2, Q7, Q8, Q9, Q10, Q20) | PG 3 (Q7, Q8, Q20)
ambiguous (relation scanned twice without alias): 1 (Q22)
CLAUSE-6-CANDIDATE: 1 (Q8)
```

## Enumeration Verdict

```
controls (goopg's OWN bushy pairings): 6/6 all OFFERED
controls set aside as CROSS-QUERY-LEVEL: 1 (Q2)
candidates (PG-only bushy pairings): 1/1 offered by goopg search
VERDICT: every PG-only bushy partition WAS enumerated — divergence is cost/stats, not enumeration
Clause 6 passes.
```

## Conclusion

M0128-P5.1's rendering fix is confirmed effective. The five queries that were
previously blocked by `N ambiguous` are now adjudicable. Q22's residual
ambiguity is a semantic issue (relation scanned twice without alias), not a
rendering gap, and was already present before P5.1.

**Clause 6 re-adjudication: COMPLETE. Ledger row 2026-08-07 M0128-P5.1 resolved.**
