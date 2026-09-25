# R74 SCOPE: SEMI search-admission Step-0 (find the fork)

Lineage: R73 ATTRIBUTION (display-seam verdict — no SEMI path filed,
NLI carrier-UNSET). The price cannot be fixed because there is no
price: Q4's EXISTS-SEMI never enters the search. This Step-0 locates
the exact fork; implementation is a separate slice.

## What is already known (no re-measurement)

- The NLI arm is WILLING: `addNLIPaths`
  (`internal/optimizer/joinpathsnli.go:270`) refuses only
  `parser.JoinRight` (`:281-283`); Semi/Anti ride the same costing
  (`:341`) as Inner.
- The search is semi-AWARE: `joinrelsize.go:139/:289/:389` size semi
  joinrels; `specialjoin.go:204-245` builds semi sjinfo;
  `joinsearchlevel.go:98/:160/:228` handle semi SJs.
- The node is legacy-built: `rewriteJoinsToNLI`
  (`internal/optimizer/nl_index_join.go:89`) / `tryBuildNLI` (`:318`)
  convert a `*Join` SEMI into the unpriced `*NestedLoopIndexJoin`.
- R73 proved the negative (no SEMI path filed ×4 EXPLAINs); it did
  not locate the fork — which producer builds Q4's SEMI `*Join`, and
  why no search joinrel exists for it.

## P0 — fork trace (temp-instrumented, foreground, Gate-0)

Binary: clean-HEAD + TEMP env-gated stderr ONLY (revert before any
slice commit). Cluster: private TPC-H SF=1 clone + 55xx port.
Query `/tmp/pp2/r71/q4.sql` EXPLAIN ×2, byte-identity required.

1. Log at `tryBuildNLI` (`nl_index_join.go:318`): which SEMI `*Join`
   it rewrites (children, rows) + caller chain (who built the Join —
   collapse? unnesting producer? planner fallback?).
2. Log semi sjinfo fate: whether a `JoinSemi` sjinfo is constructed
   for Q4's EXISTS and whether a search joinrel is ever requested
   for the orders⋈lineitem relset (joinsearchlevel semi sites as
   trace anchors).
3. Verdict: named fork — the function/condition where the search
   path is not taken, with file:line.

## P1 — slice menu (no implementation)

From the fork, enumerate admission routes (e.g. build the SEMI
joinrel through the search when the inner is a parameterised index
probe; vs price the NLI node post-hoc), each with blast-radius note
(Q22-ANTI rides the same fork — read-only here) and a
recommendation. The slice implementation is separate work.

## Gates & bounds

- Temp reverted, builds clean, EXPLAIN byte-identity re-verified.
- values/sweep/pp gates run at slice time, not Step-0.
- Explicitly out: widths, selectivity/rows, election rules, fuzz,
  Q13-inner, parallelism, any cost-term change. Q22 ANTI read-only.
