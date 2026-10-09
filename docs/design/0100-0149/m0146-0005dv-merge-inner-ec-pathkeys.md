# M0146-0005dv — a merge join's inner ordered by another class member needs no Sort

Status: done (2026-10-04, `cfc8e4f56`). Parent: M0146-0005. Filed by slice
115.

## The divergence

In TPC-DS Q47 and Q57, the top Merge Join's inner is itself a Merge Join.
PG puts a Materialize on that inner join and starts the top merge at cost 0.
goopg fed the inner bare, and the top merge started at the inner's total:
258.10 in Q47 at SF0.25, against PG's 0.00.

Repro (enable\_hashjoin = enable\_nestloop = off):
`WITH v AS MATERIALIZED (SELECT k, v, rank() OVER (PARTITION BY k ORDER BY
v) rn FROM t ORDER BY k) SELECT … FROM v v0, v vl, v vd WHERE v0.k = vl.k
AND … AND v0.k = vd.k AND … AND v0.v = 77`.

| | top Merge Join | inner |
|---|---|---|
| PG 18.3 | 0.00..337.90 | Materialize 0.00..225.20 → Merge Join |
| goopg before | 225.19..337.87 | Merge Join, bare |
| goopg now | 0.00..337.85 | Materialize 0.00..225.18 → Merge Join |

## Cause

The top merge clause is `vd.k = v0.k`, so the inner `vl ⋈ v0` has to be
ordered by `v0.k`. That join is ordered by `vl.k`, its outer's key. PG's
pathkeys point at an equivalence class (`make_canonical_pathkey`), and
`vl.k`, `v0.k` and `vd.k` are one class, so `pathkeys_contained_in` is true
and no Sort is added. `final_cost_mergejoin` then sees an unsorted inner
that cannot mark/restore and forces `materialize_inner`.

goopg's pathkeys carry an expression, and `pathkeysContainedIn` compared
`vl.k` with `v0.k` syntactically. So `tryMergeJoinPath` priced an explicit
Sort over the inner join. The merge plan absorbs a Sort child, so it is
invisible in EXPLAIN. And a Sort can mark/restore, so no Material was
elected.

## Change

- **The class arm.** `pathkeyUsefulness`, built per joinrel, now records
  the equivalence-class members whose clause lies inside the rel.
  `equivalentWithin(a, b)` reports whether `a` and `b` belong to one class
  the rel enforces, so ordering by one means ordering by the other.
- **The check.** `pathkeysContainedInRel(rel, keys, required)` is
  `pathkeys_contained_in` with that class arm. It is used where the merge
  paths decide whether a side needs a sort:
  - `tryMergeJoinPath`, on both sides;
  - `generateMergeJoinPaths`' seed for the cheapest inner;
  - `getCheapestPathForPathkeys`.

Values cannot change: goopg's merge executor sorts each input on its merge
key itself.

## Effect

- **Fire set:** Q47 and Q57 match PG at both scales.
  - Matches: SF0.25 39 → 41, SF1 30 → 32.
  - CATEGORIES-EXCL-MATCH: join-order SF0.25 52 → 50, SF1 58 → 56;
    parameterisation SF0.25 29 → 27, SF1 36 → 34.
  - Q64's plan moved at SF0.25, but its verdict did not change. No timeouts.
- **Other gates:** ea-ratchet PASS, sweep 96/96, TPC-H arm 24/24, regress 18
  planner cases identical to HEAD.

Test: `TestMergeJoinInnerJoinMaterialized` fails before the change.

## Not done (ledgered)

The other merge-path matchers still compare expressions:

- `findMergeClausesForOuterPathkeys` (PG
  `find_mergeclauses_for_outer_pathkeys`);
- `trimMergeClausesForInnerPathkeys` (PG
  `trim_mergeclauses_for_inner_pathkeys`);
- `add_path`'s pathkey dominance (`comparePathkeysDim`).

So an outer ordered by one class member does not yet offer merge clauses
written on another.
