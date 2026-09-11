# R78 SCOPE: SEMI selectivity Step-0 (the 1.0 at estimateNLIndexJoin)

Lineage: R72 program item 1 (semi selectivity 1.0 vs PG 0.2317),
R71 mechanism (variable-side non-MCV) re-anchored at 13490 vs 57066.
The SEARCH-side machinery exists (`eqJoinSelectivitySemi`,
`internal/optimizer/joinselectivity.go:764`, via
`joinClauseSelectivityForJoin :694`, Eq arm at `:708`);
Q4's SEMI never enters the search, and the legacy estimator hard
codes 1.0:

- `estimateNLIndexJoin` (`internal/optimizer/cardinality.go:239`):
  "the join carries the outer's cardinality" — returns
  `EstimateRows(j.Outer)` for every jointype, SEMI/ANTI included.

PG rule: semi rows = outer × match fraction (`costsize.c:5594-5625`
`JOIN_SEMI: nrows = outer_rows * fkselec * jselec`;
`compute_semi_anti_join_factors` `:5090-5114` filling
`outer_match_frac`/`match_count`, with the equi core in
`eqjoinsel` (`backend/utils/adt/selfuncs.c`)).

## P0 — would-be probe (temp-instrumented, foreground, Gate-0)

At `estimateNLIndexJoin`, for Semi/Anti only, derive node-side
inputs (outer/inner key columns from the probe Keys, column stats
via `columnStatsByName`, inner rows) and call the EXISTING
`eqJoinSelectivitySemi` — log the would-be fraction beside 1.0.
No tree mutation, no estimate change; temp reverted after; EXPLAIN
byte-identity re-verified (estimates must not move under a log).

- PASS iff the probe reproduces ≈ PG 0.23 on Q4's clause
  (`l_orderkey = o_orderkey` + inner `l_commitdate<l_receiptdate`
  context as the machinery sees it) → P1 wires it in.
- BLOCKED iff the node-side inputs cannot feed the function (named
  missing piece — e.g. stats unreachable without searchCtx) →
  re-scope, no heroic derivation here.

Query: `/tmp/pp2/r71/q4.sql` EXPLAIN ×2 on the private clone
(`:55xx`); oracle fraction 13490/58222 = 0.2317 (serial fixture).

## P1 — slice (iff P0 PASS; implementation separate)

Multiply the NLI estimator's Semi/Anti output by the computed match
fraction (fix iff mis-transcribed vs PG — the 1.0 hard-code has no
PG counterpart). Keeper: estimator-level test pinning the fraction
on a fabricated SEMI NLI. Gates at slice commit: values 24/24,
sweep, pp Q4/Q22 (expect the grouping contest to move on the new
rows base), 19/22 byte-identity re-check (rows WILL move here by
design — the bar is direction-toward-PG + values green, not
byte-identity).

## Explicitly out

INNER NLI rows (still outer/cardinality — separate slice), widths
(R70-blocked, program item 2), election rules, fuzz, Q13-inner,
parallelism, cost terms. Q22 ANTI rides as read-only verification.
