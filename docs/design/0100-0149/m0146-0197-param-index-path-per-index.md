# M0146-0197 — a parameterised index path for every eligible index

Status: done 2026-10-16 — every eligible index gets its own parameterised
path, for both the leading-prefix and the skip-scan probe shapes.

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

## Skip-scan half (2026-10-16)

`skipRunFor` is the per-index skip-probe rule from `pickIndexSkipRun`,
unchanged. `pickIndexSkipRun` keeps its ranking for its own tests.
`eligibleSkipRuns` lists every passing index, and `addOneParameterizedSkipPath`
builds a path for each through `addParamSkipPathFor`. Each path keeps its own
`maxSkipProbeRows` and lifetime guards. PG 18 builds the skip array into each
index's own path (`build_index_paths`; `btcostestimate`'s `num_sa_scans`,
`./postgres/src/backend/utils/adt/selfuncs.c:7444`).

**Test.** `TestParameterizedSkipPathsCostEveryEligibleIndex`: two indexes skip
one column and bind `l_suppkey`, and the smaller one must win on cost. The old
single pick builds only the large one, at 66.52.

**Measured** against the M0146-0178 baselines (`analysis/m0146/m0146-0197/skip/`):
plan text byte-identical on the reloaded TPC-H corpus (15/22) and on SF0.25
(80/99), and the fire-set SF1 A/B found no changed query. Inert today.

## Residual

The legacy `tryBuildNLI` rewrite (`nl_index_join.go`) still picks one index
through `pickIndexCoveringLeadingPrefix`. It is a pre-search rewrite, not a path
producer.
