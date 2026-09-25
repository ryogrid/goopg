# M0144-0011a-2 — ORDERED-seam candidate census (TPC-DS SF0.25, 99 queries)

Raw traces: `m0144-0011a-2-census-baseline.txt` (HEAD arm),
`m0144-0011a-2-census-relaxed.txt` (`cands<1` arm). Both captured on the
private `:5595` lane (`tmp/c20a/data-sf025`) at HEAD `ee38581b1` with
`GOOPG_PGSHAPED_DP_TRACE=1`, one `psql` per query, server log read by byte
offset so each query's trace is exactly delimited.

Columns: `decline` = `electOrderedGrouping`'s rel-level verdict; `seed keys` =
the `keys=` / `contained=` the ORDERED step's seed actually arrived with
(`producer=upper.ordered.seed`), i.e. what `inputNodePathkeys` derived from
the finished node after the loop declined.

| query | decline | seed keys | contained |
|---|---|---|---|
| Q1 | gate-precondition | 0 | false |
| Q2 | gate-precondition | 0 | false |
| Q3 | cands<2(1) | 3 | false |
| Q4 | gate-precondition | 0 | false |
| Q5 | cands<2(1) | 0 | false |
| Q6 | gate-precondition | 1 | false |
| Q7 | cands<2(1) | 1 | true |
| Q8 | cands<2(1) | 1 | true |
| Q9 | (elected) | - | - |
| Q10 | cands<2(1) | 8 | true |
| Q11 | gate-precondition | 0 | false |
| Q12 | gate-precondition | 0 | false |
| Q13 | (elected) | - | - |
| Q14 | cands<2(1),gate-precondition | 0,0 | false,false |
| Q15 | cands<2(1) | 1 | true |
| Q16 | cands<2(1) | 0 | false |
| Q17 | cands<2(1) | 3 | true |
| Q18 | cands<2(1) | 0 | false |
| Q19 | cands<2(1) | 4 | false |
| Q20 | gate-precondition | 0 | false |
| Q21 | gate-precondition | 2 | true |
| Q22 | cands<2(1) | 0 | false |
| Q23 | gate-precondition | 0 | false |
| Q24 | gate-precondition,gate-precondition | 0,0 | false,false |
| Q25 | cands<2(1) | 4 | true |
| Q26 | cands<2(1) | 1 | true |
| Q27 | cands<2(1) | 0 | false |
| Q28 | (elected) | - | - |
| Q29 | cands<2(1) | 4 | true |
| Q30 | gate-precondition | 0 | false |
| Q31 | gate-precondition | 0 | false |
| Q32 | (elected) | - | - |
| Q33 | (elected) | 1,0 | false,false |
| Q34 | gate-precondition | 0 | false |
| Q35 | cands<2(1) | 6 | true |
| Q36 | (elected) | - | - |
| Q37 | cands<2(1) | 3 | true |
| Q38 | (elected) | - | - |
| Q39 | gate-precondition,gate-precondition | 0,0 | false,false |
| Q40 | cands<2(1) | 2 | true |
| Q41 | gate-precondition | 0,1,1 | false,true,true |
| Q42 | cands<2(1) | 3 | false |
| Q43 | parallel-finalize-agg-present | 0 | false |
| Q44 | gate-precondition | 0 | false |
| Q45 | cands<2(1) | 2 | true |
| Q46 | gate-precondition | 0 | false |
| Q47 | gate-precondition | 0 | false |
| Q48 | (elected) | - | - |
| Q49 | gate-precondition | 0 | false |
| Q50 | cands<2(1) | 10 | true |
| Q51 | gate-precondition | 0 | false |
| Q52 | cands<2(1) | 3 | false |
| Q53 | gate-precondition | 0 | false |
| Q54 | (elected) | 1,0 | false,false |
| Q55 | cands<2(1) | 2 | false |
| Q56 | (elected) | 1,0 | false,false |
| Q57 | gate-precondition | 0 | false |
| Q58 | gate-precondition | 0 | false |
| Q59 | gate-precondition | 0 | false |
| Q60 | (elected) | 1,0 | false,false |
| Q61 | gate-precondition | 0 | false |
| Q62 | parallel-finalize-agg-present | 0 | false |
| Q63 | gate-precondition | 0 | false |
| Q64 | gate-precondition | 0 | false |
| Q65 | gate-precondition | 0 | false |
| Q66 | cands<2(1) | 8 | true |
| Q67 | gate-precondition | 0 | false |
| Q68 | gate-precondition | 0 | false |
| Q69 | cands<2(1) | 5 | true |
| Q70 | (elected) | - | - |
| Q71 | cands<2(1) | 4 | false |
| Q72 | cands<2(1) | 3 | false |
| Q73 | gate-precondition | 0 | false |
| Q74 | gate-precondition | 0 | false |
| Q75 | gate-precondition | 0 | false |
| Q76 | (elected) | 5,0 | true,false |
| Q77 | cands<2(1) | 0 | false |
| Q78 | gate-precondition | 0 | false |
| Q79 | gate-precondition | 0 | false |
| Q80 | cands<2(1) | 0 | false |
| Q81 | gate-precondition | 0 | false |
| Q82 | cands<2(1) | 3 | true |
| Q83 | gate-precondition | 0 | false |
| Q84 | gate-precondition | 0 | false |
| Q85 | cands<2(1) | 1 | false |
| Q86 | (elected) | - | - |
| Q87 | (elected) | - | - |
| Q88 | (elected) | - | - |
| Q89 | gate-precondition | 0 | false |
| Q90 | gate-precondition | 0 | false |
| Q91 | cands<2(1) | 5 | false |
| Q92 | cands<2(1) | 0 | false |
| Q93 | cands<2(1) | 1 | false |
| Q94 | cands<2(1) | 0 | false |
| Q95 | cands<2(1) | 0 | false |
| Q96 | parallel-finalize-agg-present | 0 | false |
| Q97 | (elected) | - | - |
| Q98 | gate-precondition | 0 | false |
| Q99 | parallel-finalize-agg-present | 0 | false |
