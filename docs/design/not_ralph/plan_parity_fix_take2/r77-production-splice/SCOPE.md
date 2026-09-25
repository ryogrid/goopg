# R77 SCOPE: production NLI price splice

Lineage: R76 P0 (number emerges via production helpers) + P1 (TEMP
stamp measures Q4 on a real base, 19/22 identical, values/sweep
deferred here). This slice ships the splice without temp.

## P0 — stamp-site check (code reading + one temp run iff unclear)

R76 stamped inside `tryBuildNLI`; Q22 displayed rows (16666) missed
the stamped rows (18200) — the stamped instance may not be the
EXPLAIN-read instance (`maybeAttachMemoize` wrap, Filter-promotion
rebuild, or a later pass). Determine by reading; one temp run only
if the reading is inconclusive.

- If the instance is stable: stamp in `tryBuildNLI` (SEMI/ANTI
  success), cp threaded as a new param (12 test sites + ~10
  same-file `walkRewriteNLI` recursive sites, all mechanical) — or
  via a post-pass if threading proves invasive.
- If a later pass replaces the node: stamp in an end-of-pipeline
  post-pass over the final tree (carrier-unset SEMI/ANTI NLI nodes
  only), cp = `ps.costParams()` once at `rewriteJoinsToNLI` entry —
  zero `tryBuildNLI` signature churn either way. Prefer the post-pass
  on ties: one call site, fail-closed (price failure → node stays
  legacy-priced, today's numbers).

## P1 — implement (no temp)

Pricing = R76 probe promoted verbatim (production helpers,
joinrel rows = legacy SEMI output estimate, `numQualOps` =
unbound-movable count, cp from ps) + stamp through `stampPlanCost`.
Fail-closed at every step: unresolvable table/stats/index/clauses,
no filed SEMI path, or any error → no stamp (today's display).
Keeper: rewrite-shape test asserting a carrier-unset SEMI NLI node
emerges carrier-set with Total ≥ outer through the post-pass (or
in-situ variant per P0); R75 harness keeper stays.

## Gates (slice commit bar)

- values 24/24 + TPC-DS SF0.25 sweep PASS=96 all-zero.
- pp: Q4 delta required toward PG; Q21/Q22 read-only; Q13 untouched.
- 19/22 byte-identity re-verified (only EXISTS family moves,
  costs-only + the recorded rows notes).

## Explicitly out

Same bounds as R76 (join order, cost terms, hash-SEMI,
selectivity/rows, widths, election rules, fuzz, Q13-inner,
parallelism) + Q22-seam root-causing beyond the stamp-site check
(investigate only if gates complain).
