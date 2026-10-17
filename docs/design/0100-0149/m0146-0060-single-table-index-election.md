# M0146-0060 — the single-table index election is costed

Status: landed 2026-10-13 (`4557c69d4`).
Parent: M0146-0009. The hold on M0146-0061 and M0145-0008ag lifted on
2026-10-13.

## Problem

`planIndexScanFromWhere`, the single-table rule-based index producer
(planner.go's `isSimpleSingle && planIsBareSeqScanTree` arm), replaced the
costed search's Seq Scan with a heuristic Index Scan whose cost
(0.00..0.99) never competed.

Regress `aggregates`' `agg_sort_order WHERE c2 < 100` (99 of 100 rows) shows
the effect: PG 18.3 keeps the Seq Scan (cost 2.25), and goopg elected
`Index Scan using agg_sort_order_c2_idx`.

The 2026-10-05 recon (`analysis/m0146/m0146-0060/findings.md`) found that
gating the rule exposed two search gaps: no range bitmap path (M0146-0061)
and index probes priced at twice PG's cost (M0145-0008ag). Both are now
closed.

## What the rule was still covering

The re-run (`tmp/m0060/probe.sql`, analyzed tables, against PG 18.3) showed
the costed search matching PG without the rule on equality, varchar, char,
timestamp ranges and the `dsc` ranges. It did not cover these:

| shape | PG 18.3 | search without the rule |
|---|---|---|
| prepared `aid = $1` | Index Scan acc\_pkey | Parallel Seq Scan |
| `id IN (7, NULL)` | Index Scan | Seq Scan |
| `name LIKE 'name123%'` | Index Scan (`~>=~`/`~<~`) | Seq Scan |
| `regclass` column `= 'x'` | Index Scan | no index path |
| partial index proven by the qual | Index Scan / Index Only Scan | not built (restriction and IOS producers declined every partial index) |

## Change

- **The rule fires only for a LIKE prefix.** It runs when
  `injectLikeRangePredicates` adds a range: PG derives those index quals in
  `match_pattern_prefix` (`./postgres/src/backend/utils/adt/like_support.c`),
  and the search has no such path. Every other uncorrelated restriction is
  the costed search's call.
- **The search's index keys follow PG's pseudo-constant rule**
  (`match_clause_to_indexcol`, `./postgres/src/backend/optimizer/path/indxpath.c`).
  The other operand may be anything that has no Var of the relation and is
  not volatile:
  - a client bind parameter (`ParamRef`, PG's PARAM\_EXTERN);
  - a constant cast, which PG folds to a Const of the column type
    (`eval_const_expressions`). An implicit coercion of a literal probes with
    the literal itself, because the btree key encoder converts it against
    the column's type, array columns included, as the rule did
    (`coercedLiteralKey`). Any other constant cast must already produce the
    column's type (`castTargetIsColumnType`).
- **A NULL IN-list element** no longer stops the list from being a
  ScalarArrayOp index qual. The executor skips it.
- **Proven partial indexes.** The restriction and index-only producers now
  admit a partial index whose predicate a conjunct proves
  (`partialIndexUsable`, the bitmap producer's `partialPredicateProvenBy`,
  PG's `predOK`).
- **`USING hash` indexes.** goopg builds them on a btree with `DeclaredHash`.
  They now answer equality only: no range, skip or SAOP qual, and never an
  Index Only Scan. PG's hash AM has no ordering, `amsearcharray` is false
  and `amcanreturn` is NULL (`hashhandler`,
  `./postgres/src/backend/access/hash/hash.c`). Before this, the rule had
  pre-empted the search's index-only path on the hash index in regress
  `create_index`.

## Tests

- **Migrated tests.** 28 unit tests had an index shape as their subject and
  ran on stat-less in-memory catalogs, where the search rightly keeps a Seq
  Scan. (A real server sizes a never-vacuumed relation at 10 pages, as PG
  does, and elects PG's plan there.) They now request the shape with
  `enable_seqscan` / `enable_bitmapscan` off — PG's own plan under those
  settings, checked case by case (`planIndexSubject`, `planOneIndexScan`,
  `indexOnlySettings`).
- **Expectations corrected to PG:**
  - a one-element IN is `= 7` (transformAExprIn);
  - a two-sided range keeps both bounds in the Index Cond with no residual
    Filter.
- **Toggle test.** `TestScanMethodTogglesGateTheScanShape` gets analyzed
  statistics and passes the planner settings the wire path also sets
  (dispatch.go).
- **New test.** `TestSingleTableIndexElectionIsCosted` (postmaster) covers
  agg\_sort\_order, prepared `$1`, `IN (7, NULL)` and the LIKE prefix. It fails
  on HEAD.

## Verification

- **Probes.**
  - With the change, goopg elects PG's plans for every shape in
    `tmp/m0060/probe.sql` and `p4.sql`, except the partial index noted
    below.
  - agg\_sort\_order matches PG exactly, including the Seq Scan's cost of
    2.25.
- **Regress A/B** (27 cases, same list on both builds): `hash_index` now
  PASSes, `create_index` −1, `subselect` +1 (NOTICE order). The rest are
  identical.
- **Isolation.** The suite run three times per build shows timing churn in
  both directions. The seven specs that moved by two or more of three runs
  all pass their strict dedicated tests three times out of three.
- **Gates.** Units; postmaster; tpch-spotcheck; the acceptance arm (values
  identical); sf025 PASS=99; ea-ratchet PASS. The TPC-DS fire set and the
  TPC-H corpus show no plan change (fires=none).

## Residuals

All are in the deferral ledger.

- The rule still overrides cost for a LIKE prefix until the search derives
  the prefix range quals itself.
- A partial index whose predicate implies a qual: PG drops that qual from
  the index quals (`indrestrictinfo`), while goopg keeps it as a key. PG
  also prefers an Index Only Scan on `pt_part` for `flag = 3 AND id < 100`,
  where goopg uses a bitmap; this predates the change.
- Found while probing, and filed as M0146-0173: `timestamp + interval`
  prints a spurious `.000000`; `int * interval`, `timestamp + (text)::interval`,
  `timestamp + make_interval(...)` and one-argument `to_timestamp(float)`
  raise errors.
