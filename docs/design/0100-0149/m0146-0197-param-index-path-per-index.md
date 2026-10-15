# M0146-0197 — a parameterised index path for every eligible index

Status: open — the leading-prefix half landed 2026-10-15. The skip-scan half
still picks a single index.

## Problem

PG's `create_index_paths` → `build_index_paths`
(`./postgres/src/backend/optimizer/path/indxpath.c`) builds a path for every
usable index of a relation, parameterised paths included, and `add_path` keeps
the cheapest. goopg's `addOneParameterizedIndexPath` built one parameterised
leading-prefix path per parameterisation, from `pickIndexCoveringLeadingPrefix`:
the index binding the most columns, with ties going to the first index by name
(`IndexesOnTable` sorts by name). Two indexes on the same column were never
chosen between by cost.

The witness is from M0146-0195, on a private TPC-H clone. A compact twin of
`order_customer_fkidx` (1563 pages) priced at 4.65 per probe, against 7.07 for
the bloated original (28617 pages). The twin was never even considered until
its name sorted first.

## Change

- `leadingPrefixProbeKeys` (`nl_index_join.go`) is the per-index eligibility
  rule, unchanged:
  - a non-partial btree;
  - the leading column bound (PG's `amoptionalkey` rule, indxpath.c:1029);
  - unbound trailing key columns NOT NULL.

  `pickIndexCoveringLeadingPrefix` keeps using it, so the legacy `tryBuildNLI`
  rewrite still picks one index.
- `eligibleLeadingPrefixIndexes` lists every index that passes.
- `addOneParameterizedIndexPath` builds a path for each eligible index through
  `addParamIndexPathFor`, then offers the skip candidate once, as before.
  `createPlan` builds the probe from the path's own `IndexInfo`
  (`createplanindex.go`), so the costed index is the executed one.

**Test.** `TestParameterizedPathsCostEveryEligibleIndex`: two indexes on
`o_custkey`; the large one (50000 pages) sorts first and the small one (500
pages) wins on cost. With the old single pick the large one is the only path,
priced at 58.61.

## Measurement

Captures with a clean-HEAD (`be711c43e`) and a patched binary
(`analysis/m0146/m0146-0197/`):

| corpus | match | plan text |
|---|---|---|
| TPC-H | 12 → 12 | byte-identical |
| TPC-DS SF0.25 | 80 → 80 | byte-identical |

The fire-set gate's SF1 A/B found no changed query either. On today's corpora
no relation has two eligible leading-prefix indexes whose costs differ at a
decision point, so the change is inert (C6 evidence: identical captures). It
becomes active wherever two indexes cover the same probe, for example once the
TPC-H fk indexes are compact.

## Residual

The skip-scan arm (`addOneParameterizedSkipPath` → `pickIndexSkipRun`) still
builds one skip path, on one index. PG 18 builds a path per index there too
(the skip array is part of each index's own path, `btcostestimate`). Resume
point: iterate the skip-eligible indexes the same way.
