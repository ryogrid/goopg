# Working Set — 2026-09-28, end of Loop #21

Task: **M0146-0009c** (recon — decompose the 5 NEW ea-ratchet findings). DONE.
Files: `analysis/m0146/m0146-0009c/README.md`, evidence `tmp/m0146-0009c/`
(q85/q78/q83 {goopg,pg}-ea.txt + probe85/probe83 .sql/.txt + server-trace.log),
`docs/design/0100-0149/m0146-0009c-ea-findings-decomp.md` + index row,
`.ralph/fix_plan.md` (0009c [x], M0146-0009e filed), `.ralph/deferral_ledger.md`
(3 rows → resolved).

Findings:
- Q85 ×3 = PG-shared. All cascade from `HJ {ws⋈wr} rows=1`; standalone probe
  of same relset/predicates = rows=1 on BOTH engines; PG's own plan also ends
  rows=1. DP trace: PG-order joinrels exist in goopg's space ({ca+wr+ws}
  npaths=6 cheapest=nli), lost by epsilon. `created=0` = "pair did not
  first-create the joinrel", NOT "no path". No fix filed.
- Q78 = PG-shared. MLJ 1,407 vs PG's same-node 1,371; both ~90x under 123,049.
- Q83 = REAL gap → **M0146-0009e** (impl, Kind: impl, Parent: M0146-0009c):
  lone-`GROUP BY` CTE output cols never get isunique/tuples in
  examineJoinVar's `!ok` arm (`subqueryUniqueOutput` is pulled-ANY-only,
  joinsearchseam.go:1034; examineJoinVar joinselectivity.go:163-178). PG's
  RTE_SUBQUERY lone-groupClause arm (selfuncs.c:5876-5883) gives nd=leaf
  tuples → est 10 vs goopg's nd=200 → est 1. Q95 `cte:ws_wh` likely same class.

Next step: per banner — **M0146-0009e** (impl above) or **M0146-0009d**
(ea-ratchet must fail on scored==0/all-ERROR + verify port server identity).

Gates run: none (recon — no production code; private lanes only).
In-flight: none. Private :5541 server (goopg-c0009c.scope) stopped; clone
`tmp/c20a/data-sf025` retained for the next loop's EA/probe work.
