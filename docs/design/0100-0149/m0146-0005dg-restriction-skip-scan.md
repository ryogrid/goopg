# M0146-0005dg: a constant on a non-leading btree column drives a skip scan

Status: landed 2026-10-02 (banner item 3, M0146-0005 slice 113).

## Defect

PG 18 binds `col = const` on a NON-leading btree column as an index qual.
It fills each unbound leading column with a skip array (nbtpreprocesskeys.c
`_bt_skiparray`) and prices one descent per distinct skipped value
(btcostestimate's `num_sa_scans`). On the TPC-DS SF0.25 data:

| query | PG 18.3 | goopg before |
|---|---|---|
| `select * from inventory where inv_item_sk = 100` (`inventory_pkey` = `(inv_date_sk, inv_item_sk, inv_warehouse_sk)`) | `Index Scan using inventory_pkey`, `Index Cond: (inv_item_sk = 100)`, cost 1814.67 | Seq Scan, 42167.50 |

goopg built skip probes only for the parameterised arm (M0146-0005v, NLI
inners), and its lowering refused an unparameterised one.

## Change

- `restrictionSkipRun` (pathindexrestrict.go) is the unparameterised twin
  of `pickIndexSkipRun`. It finds the contiguous run of local `col =
  const` conjuncts starting at the first bound column (≥ 1). It declines
  in four cases:
  - the leading column is bound (the prefix arm's domain);
  - an expression or DESC column sits in the skipped prefix;
  - an unbound key column may hold NULLs (`indexSkipNullSafe`), because
    the byte-key btree keeps no NULL-keyed entry;
  - any other probe shape (equality prefix, SAOP, leading range) bound the
    index first.
  The equality matcher is shared with the prefix arm
  (`restrictionEqualityOn`).
- `addOneRestrictionIndexPath` prices the run with `skipScanDescents`,
  i.e. `num_sa_scans` with PG's two reverts (default ndistinct, more
  descents than index pages). A revert prices the index side over the
  whole index. The path carries `IndexSkipPrefix`.
- The createplanindex.go lowering accepts an unparameterised skip probe
  whose clauses are all local, and drops them from the reinstated Filter.
- `tryPromoteIndexOnlyScan` declines a skip probe. Copying `Keys` without
  `SkipPrefix` re-aimed `inv_item_sk = 100` at the leading `inv_date_sk`
  and returned no rows; this was measured, and the test pins it.

The executor's skip scan (`rescanSkip`) already took any Keys shape, so
constant keys needed no executor change.

## Verification

`TestRestrictionSkipScanMatchesPG` (executor) drives the planner with seq
and bitmap scans off. It checks:

- the lowered `IndexScan{SkipPrefix: 1}` and its row, for both a
  heap-reading target list and a covering one;
- that a nullable leading column gets no skip.

It fails without the arm, and returns 0 rows without the promotion guard.

On the SF0.25 clone, every probe value matched PG: the counts and sums of
`inv_item_sk = 100`, `… AND inv_warehouse_sk = 2`,
`ss_ticket_number = 12345`, `sr_ticket_number = 12345`, and an ORDER BY
LIMIT.

Regress A/B:

- `btree_index`: both `Index Cond: (id = 55)` lines on `btree_tall_tbl`
  now match PG (the base printed `Filter: (id = 55)`).
- `create_index`, `select`, `inherit`, `partition_prune`, `aggregates` and
  `subselect` are unchanged; `join` shows only its known row flap.

Gates: units, spotcheck, sweep 96/96, arm, fire set (no TPC-DS plan
changed — the corpus has no constant-driven skip probe), ea-ratchet.

## Left open (ledgered)

- PG makes a covering skip probe an Index Only Scan (`btree_tall_tbl`,
  `select inv_item_sk …`). goopg keeps an Index Scan, because the
  index-only producer only consumes leading equality prefixes and the
  promotion now declines.
- PG walks the skip scan backward for `ORDER BY t DESC, id DESC`. goopg
  adds a Sort.
- Costs differ through the known families: the probe multiplier,
  synthesised index geometry, and the omitted log2 descent term. For
  example, `inv_item_sk = 100 AND inv_warehouse_sk = 2` costs 450.32 in
  goopg and 1110.17 in PG.
- A leading range or SAOP combined with a later equality is not combined
  into one skip probe.
