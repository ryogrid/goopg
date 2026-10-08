# M0146-0120 — isunique recurses through sub-select levels

Status: done 2026-10-09 (e61861b70). Parent: M0146-0009.

## Problem

TPC-DS Q44 at SF0.25 matched PG 18.3's shape except for the two
`item_pkey` probes. PG wraps each one in a Memoize; goopg probed without
one. A trace of `getMemoizePath` showed the cache key `asceding.item_sk`
with goopg's 200-distinct default (`SELFLAG_USED_DEFAULT`).
`costMemoizeRescan` then replaces the estimate with `calls`, which means
no reuse, so Memoize lost.

## PG behaviour

`examine_simple_variable` (selfuncs.c, RTE\_SUBQUERY arm) behaves
differently by level:

- **A level with a lone GROUP BY or DISTINCT key on the column** marks
  `isunique` and stops.
- **A level with any other grouping, a set operation or grouping sets**
  stops without it.
- **A level that neither groups nor de-duplicates** recurses into the
  sub-select its target Var reads, when that target is a plain Var.

`get_variable_numdistinct` then gives an isunique column the leaf's tuples.

Q44's `asceding.item_sk` reads v11, a window query over v1, and v1 groups
by `ss_item_sk` alone. So PG estimates about 5431 distinct keys.

## Change

Two gaps, both in internal/optimizer/joinselectivity.go:

1. **Recursion.** `loneKeyPositions` read only the leaf body's top level.
   `derivedColumnIsUnique` walks the planned body the way PG's recursion
   walks the parse tree.
   - It crosses bare-column Project targets, a WindowAgg's pass-through
     region, Filter, Sort, IncrementalSort, Limit, Gather, GatherMerge,
     Materialize, SubqueryScan, CTEScan, and either side of a Join or
     NestedLoopIndexJoin, using `resolveBaseColumn`'s coordinate rule.
   - It stops **true** at a lone whole-mode GROUP BY key, or at the only
     DISTINCT / DISTINCT ON column.
   - It stops **false** at any other grouping, a set operation, an
     expression target or a base relation.
2. **Stripped leaves.** `derivedLeafUniqueCols` had no arm for a derived
   leaf whose trivial Subquery Scan was stripped, which reaches the search
   as the sub-select's own top Project. It now accepts a Project. Over a
   projected base scan the walk answers nil.

## Verification

- **Test.** `TestDerivedLeafUniqueThroughWindowLevel` fails at HEAD. It also
  checks that a window output, a projected base scan and a two-key GROUP BY
  are not marked.
- **TPC-DS fire set** (results identical). Q44 becomes a full MATCH at
  SF0.25: match 47 → 48, join-order 43 → 42, parameterisation 23 → 22. No
  other query fires. SF1 is unchanged, because Q44 hash-joins there in both
  engines.
- **Estimates and TPC-H.** ea-ratchet 9/9; TPC-H plans are byte-identical.
- **Regress A/B** (19 files). Neutral; the only change is the known
  row-order flip.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered (ledgered)

PG stops the recursion below a `security_barrier` subquery. It still
notices a DISTINCT or GROUP BY at that level, but does not dig further. The
planned body does not carry the flag, so goopg's walk crosses it.
