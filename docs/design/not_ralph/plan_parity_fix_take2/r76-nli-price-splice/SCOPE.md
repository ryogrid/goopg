# R76 SCOPE: NLI price splice at the rewrite site

Lineage: R75 SPIKE (admission needs no new machinery — SEMI NLI
files through existing arms with the PG inequality holding). The
remaining gap is positional: the arms run in-harness, while Q4's
SEMI is built legacy at `unnestExistsExpr` and rewritten at
`rewriteJoinsToNLI` (`planner.go:1638`) with no price. This slice
splices a real arm-priced number onto that node — it does not move
the node, reorder any join, or change any cost term.

## P0 — probe (temp-instrumented, foreground, Gate-0)

At the `tryBuildNLI` SEMI success site (`nl_index_join.go:318`
family), with the FINAL planned children in hand:

1. Construct lightweight rels from the planned children (outer
   cost/rows from its carried display cost; inner probe path
   re-derived for the parameter-bound index scan) + semi sjinfo over
   the pair, and price via the existing `addNLIPaths` arm.
2. Assert the filed SEMI NLI satisfies Total ≥ outer Total; log the
   number beside the current seam number (570.66).
3. Outcomes, both committable: PASS (a real number emerges —
   proceed to P1) or BLOCKED (the named un-constructible piece,
   e.g. probe-path re-derivation outside the search — re-scope,
   no heroic construction inside this slice).

No tree mutation in P0 (log only); temp reverted after; EXPLAIN
byte-identity re-verified.

## P1 — measurement + gates (iff P0 PASS)

Stamp the P0 number through the `stampPlanCost` funnel
(`createplan.go:50`) onto the rewritten NLI node (carrier set —
R73 seam closed by construction), then measure Q4 live against the
serial oracle (`/tmp/pp2/r69/r69bpg.pg.plans.txt` §Q4 = live `:65432`
with `max_parallel_workers_per_gather=0`: GroupAggregate
192121.20..192222.42, semi 191195.81): report SEMI price vs PG
191195.81, grouping
re-election (R72 hashed/sorted contest on a real base), full-plan
pp verdict. Gates: values 24/24, sf025 sweep, pp Q4 delta required
toward PG, Q21/Q22 read-only, Q13 untouched, non-target
byte-identity.

## R77 — implementation (iff P1 clean)

Production splice (no temp): construct + price + stamp at the
rewrite site, keeper test extending `semiadmission_test.go` to the
rewrite shape, full gate suite. Separate SCOPE if P1 surprises.

## Explicitly out

Join-order changes (semi stays where unnest put it — PG's pull-up
puts Q4's semi at top too), cost-term changes (frozen: `nestloopCost`
`cost_funcs.go:723` via the NLI call site `joinpathsnli.go:341`),
hash-SEMI shapes (stay legacy), selectivity/rows, widths, election
rules, fuzz, Q13-inner, parallelism.
