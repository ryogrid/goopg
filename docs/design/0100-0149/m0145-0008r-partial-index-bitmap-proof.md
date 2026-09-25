# M0145-0008r: a bitmap scan uses a partial index only when the query proves its predicate

Status: **LANDED 2026-09-25** (`f56d94e86`). Task: `.ralph/fix_plan.md`
M0145-0008r (Kind: impl, Parent: M0145-0008). This was a wrong-results
defect, owner-placed first in banner item 2a. Evidence:
`analysis/m0145/m0145-0008r/`.

## Defect

The must-pass regress case `portals_p2` returned `(0 rows)` for
`SELECT * FROM onek2 WHERE unique1 = 50`; PG returns one row.
`create_index` creates `onek2_u1_prtl ON onek2(unique1) WHERE stringu1 < 'B'`,
and goopg planned a Bitmap Heap Scan through that partial index with
`Recheck Cond: (stringu1 < 'B')`.

`buildOneBitmapPath` (`internal/optimizer/pathbitmap.go`) admitted every
partial index on the premise that appending the predicate to the heap recheck
keeps the scan correct. The premise is false. A row the predicate excludes
has no index entry, so the bitmap never produces it and no recheck can bring
it back.

The other index producers already declined an unproven partial index:
- `addOneOrderedIndexPath`;
- `addOneRestrictionIndexPath`;
- the index-only producer;
- `findBTreeIndexForColumn`.

The defect surfaced after M0145-0008's legacy deletion. That removed the
single-table WHERE bypass, which declined unproven partial indexes, so
single-table queries now reach the bitmap producer.

## What PG does

`check_index_predicates` sets `index->predOK` only when the restriction
clauses imply the index predicate (`predicate_implied_by`,
`postgres/src/backend/optimizer/util/predtest.c`). `create_index_paths` skips
any partial index without `predOK` (`postgres/src/backend/optimizer/path/indxpath.c`).
An index with a proven predicate may yield a bitmap path even without index
clauses.

## Change

- `buildOneBitmapPath` still resolves the predicate, but declines unless one
  of the leaf's conjuncts proves it (`partialPredicateProvenBy`).
- The proof is `provePartialIndexPredicate`, the narrow M0134-0017b
  `Var op Const` prover the other producers use.
- The "recheck keeps it correct" comment is gone.
- `TestBuildOneBitmapPath_PartialIndexNeedsProvenPredicate` replaces the test
  that pinned the old premise. It checks three cases:
  - no restriction: no path;
  - a restriction that does not imply the predicate: no path;
  - the predicate itself as a restriction: a path is built.

## Verified

- **Minimal repro:** a 1000-row table with the partial index. `unique1 = 51`,
  whose `stringu1` is `'Z'`, returned 0 rows before and returns its row now;
  the plan is a Seq Scan.
- **Full `TestPort_RegressSuite`:** `portals_p2` and `select` PASS. `union`
  is still red; that is M0145-0008s, next in item 2a. `create_index` is a
  deferred skip.
- **Gates:**
  - units: PASS.
  - `tpch-spotcheck`: PASS.
  - acceptance arm: 24 MATCH.
  - `tpcds-sf025`: 96 PASS, 99/99 shapes the same.
  - fire-set (SF0.25 + SF1): PASS, fires none.
  - pgbench smoke: PASS.

  The benchmark schemas have no partial indexes.

Movement: none. This is a wrong-results fix with no corpus plan change.

## Not ported (ledgered)

goopg has no general `predicate_implied_by`. A partial index whose predicate
is implied only by a stronger or combined clause still declines where PG
would use it; for example, `unique1 < 20` does not prove `unique1 < 50`. That
costs plans, not correctness.
