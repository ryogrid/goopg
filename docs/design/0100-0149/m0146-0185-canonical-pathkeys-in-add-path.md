# M0146-0185 — add_path compares pathkeys across an equivalence class

Status: closed 2026-10-14.

## Problem

TPC-DS Q77's web channel joins two grouped subqueries (`ws`, `wr`) on
`wp_web_page_sk` with a LEFT join. At SF0.25:

- PG 18.3 aggregates each side over a nested loop driven by an Index Only
  Scan on `web_page_pkey`, with a Materialize inner. The sides come out
  sorted, so PG merge-left-joins them.
- goopg seq-scanned `web_page` under hash or merge joins and sorted. Since
  M0146-0183 its top join had flipped to a Hash Left Join on a 0.10
  near-tie, which moved CATEGORIES-EXCL-MATCH join-method from 9 to 10.

## Diagnosis

The `wr` subquery run standalone reproduces it.

**PG side.** A PG 18.3 OPTIMIZER_DEBUG build (tmp/pgdbg) dumped the pathlist
of the `{web_returns, date_dim, web_page}` joinrel. It holds only two nested
loops:

- an unsorted one, 0.30..2351.05;
- one sorted on the join key, 0.44..2362.49.

With materialize disabled, PG shows the merge join it discarded:
2346.60..2347.00, also sorted on the join key. Its total is equal to the
sorted nested loop's within `STD_FUZZ_FACTOR`, and its startup is far
worse. Both are the same canonical pathkey — the EC {wr_web_page_sk,
wp_web_page_sk} — so `add_path` keeps only the nested loop.

**goopg side.** The DP trace shows goopg built the same pair:

- a merge join, 2318.82..2319.23, sorted by `wr_web_page_sk`;
- a nested loop, 0.44..2330.71, over `index.ordered` web_page, sorted by
  `wp_web_page_sk`.

goopg's pathkeys are syntactic: `pathKeyEqual` compares expressions. The
two keys were PATHKEYS_DIFFERENT, both paths survived, and the merge won on
total.

PG's pathkeys are canonical (`make_canonical_pathkey`, pathkeys.c): one
pathkey per equivalence class. Within a joinrel that enforces the class's
equality, every member orders the rows identically.

## Change

- `comparePathkeysDimRel(rel, a, b)` (pathkeys.go) treats two keys as equal
  when they have the same direction, nulls placement and grouping
  nullability, and their expressions are equal or are members of one
  equivalence class the rel enforces.
- `add_path`'s pairwise comparison (`comparePaths`), the partial-path loop
  (`addToPartialPathlist`) and `setCheapest`'s pathkeys tie-break use it.
- The class test is the existing within-rel one,
  `pathkeyUsefulness.equivalentWithin` (M0146-0005dv). The containment
  siblings (`countContainedIn`, `pathkeysContainedInRel`) already use it, so
  "is this path sorted?" and "are these two orderings the same?" agree.
- `RelOptInfo.pathkeyClass` carries the test. It is set only on a join rel
  whose problem has no null-extending special join (`ecReduce`, the gate
  the class reduction uses), where a class's members carry equal values in
  every row.

## Evidence

Files are in `analysis/m0146/m0146-0185/`, including the parser scripts for
the OPTIMIZER_DEBUG dump.

- **Standalone `wr`:** goopg now plans PG's GroupAggregate over a nested loop
  with an index scan on `web_page_pkey` outer and a Materialize inner
  (0.44..2330.87).
- **Q77 at SF0.25:** the web channel's top join is PG's Merge Left Join
  again, and the `wr` side has PG's shape.
- **Fire set at SF0.25:**
  - Q77 loses join-method and Q58 loses qual-placement.
  - Q4 flips from MATCH to SHAPE-DIFF: match 76 → 75, join-order 17 → 18,
    sort-strategy 10 → 11.
  - Q4 is a cost tie. Its unsorted nested-loop chain plus a full Sort
    (187607.10) beats the merge-driven sorted chain under an Incremental
    Sort by about 0.2 of about 187600, roughly 1e-6. The change alters which
    intermediate sorted paths survive, as canonical pathkeys do in PG; which
    side wins then turns on micro-cost differences (the COSTTIE class).
- **Fire set at SF1:** match stays at 64. join-method 16 → 14,
  sort-strategy 19 → 17, qual-placement 8 → 6, and Q4 goes from 5
  categories to 2.
- **SF0.25 sweep:** 99 of 99 pass.
- **TPC-H:** the acceptance arm is value-identical.
- **Regress A/B:**
  - `partition_join`, `subselect`, `aggregates`, `union` and
    `incremental_sort` are identical.
  - `join` differs only in the row order of a query with no ORDER BY. That
    query's unmatched-row order from a Hash Right Join varies between runs
    of the same binary, in both directions.
- **Test:** `TestAddPathCanonicalPathkeysEvictFuzzyTiedMerge` fails with the
  syntactic comparison restored.

## Not ported (filed / ledgered)

- **M0146-0188.** goopg's index-only path carries no pathkeys
  (`indexonly … pathkeys=0`), so PG's ordered Index Only Scan on
  `web_page_pkey` appears in goopg as an Index Scan.
- **M0146-0189.** Q77's `ws` side still splits into a parallel partial
  aggregate, where PG aggregates over NL(Index Only Scan, Material(Gather)).
- Canonical pathkeys elsewhere: merge-key matching (`joinpathsmerge.go`
  `pathKeyEqual`) and the node-level containment checks in
  createplansimple.go still compare syntactically. Classes are not consulted
  under null-extending outer joins, where PG 16+ separates members by
  nullingrels.
- Q4 at SF0.25 is a COSTTIE flip; it is not filed.
