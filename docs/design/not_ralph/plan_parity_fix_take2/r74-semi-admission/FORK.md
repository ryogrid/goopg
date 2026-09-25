# R74 P0 verdict: the fork is `unnestExistsExpr` (unnest.go:4078/:4360)

Binary: `/tmp/pp2/r73/goopg-r73attr`→`/tmp/pp2/r74/goopg-r74attr`
(HEAD `7a27d44` + TEMP env-gated stderr, reverted after). Cluster:
private clone `/tmp/pp2/clone-tpch-r65` `:5533`. Query
`/tmp/pp2/r71/q4.sql` EXPLAIN ×3 total, all byte-identical to
`/tmp/pp2/r72/q4-plan1.txt`.

## Fork trace (SCOPE P0 items 1–3)

| probe | result |
|---|---|
| `R74UNNEST site=exists` (`unnest.go:4360` in `unnestExistsExpr :4078`) | FIRES: `type=5 orows=1500000 irows=2000418` — builds hash-SEMI `*Join` in the legacy tree (outer still unfiltered 1.5M at unnest time) |
| `R74UNNEST site=corr/noncorr` (`:3180/:3314`) | silent — IN-subquery sites, not EXISTS |
| `R74NLI` (`tryBuildNLI :318`) | FIRES: `type=5 outer=*Filter orows=57066 inner=*Filter irows=2000418` — rewrites to the unpriced NLI after the date filter applies |
| `R74SJ` (`collapse.go` semi sjinfo) | silent — EXISTS is not a FROM-clause join, so deconstruction never builds a semi sjinfo |

Fork, named: `unnestExistsExpr` consumes the EXISTS subquery into a
legacy `Join{SEMI, Hash}` before any search joinrel exists for the
orders⋈lineitem relset; `walkRewriteNLI` skips searched trees only,
so the legacy join is rewritten to an unpriced NLI; the search never
sees the relset (no sjinfo → no joinrel → no path → no price → R73
display seam). The orows shift (1.5M at unnest → 57k at NLI) is the
date filter applying between the two passes.

## P1 slice menu (no implementation)

- (i) Admit the unnested SEMI joinrel to the search when the inner
  is a parameterised index probe: build sjinfo for the unnested
  relset, file through `addNLIPaths`, emit via `createNestLoopPlan`
  (stamped). Blast radius: every EXISTS/NOT-EXISTS query (Q4/Q21/Q22
  + TPC-DS); joinrel sizing for semi exists (`joinrelsize.go`) but
  the search has never priced a SEMI — election behavior downstream
  (R72) re-opens.
- (ii) Price the NLI node post-hoc (stamp a computed NL cost at
  rewrite time). Blast radius: small, but prices a node the search
  did not choose — two cost truths; elections still blind.
- Recommendation: (i). (ii) preserves the exact blindness R72/R73
  diagnosed (elections pricing numbers no path compared). Q22-ANTI
  rides (i) as verification; Q13-inner admission stays separate.

Temp fully reverted; optimizer builds clean; tree clean.
