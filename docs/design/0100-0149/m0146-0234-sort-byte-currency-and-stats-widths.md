# M0146-0234 — sorts are priced in PG's byte currency, and narrowed widths come from ANALYZE

Status: slices 1 and 2 landed 2026-10-20. Slice 2 is the joinrel `attr_needed` narrowing (§8). Parent: M0146-0214.
Evidence: `analysis/m0146/m0146-0234/`.

## 1. Record

On TPC-DS SF1 Q64, GEQO eval 41 is 0.75% cheaper on goopg than on PG (M0146-0214 slice 6, design §11). Two Sorts
explain it:

- **Join 2** `(b 1 9 10)` sorts `store_sales` on `ss_cdemo_sk`. goopg priced a disk sort (1190592); PG's fits in
  work\_mem (389568.13).
- **Join 9** sorts a 2.2 × 10⁹-row partial hash join for a partial merge.
  - goopg priced the Sort at width 664 against PG's ≈96. That made its partial merge 6.32 × 10¹⁰ against PG's
    6.20 × 10¹⁰.
  - So the partial hash join survived `add_partial_path`. PG's own merge is fuzzily equal to it and has better
    pathkeys, so PG discards the hash.

## 2. PG

- **`cost_tuplesort` (costsize.c).** It sizes the input as `relation_byte_size(tuples, width)`:
  `MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader)` per tuple, where `width` is the input path's
  `pathtarget->width`.
- **`set_rel_width` (costsize.c).** A base rel's target width is the sum, over the attributes needed above the scan,
  of `get_attavgwidth` (pg\_statistic.stawidth, ANALYZE's average width). `get_typavgwidth` is used only for an
  attribute with no statistic.

## 3. goopg before

- **Sort bytes.** Sorts were sized with `hashsize.EntryBytes(ncols, avgVarBytes)`, the executor's hash-entry model.
  The PG currency existed behind `GOOPG_PG_SORT_RELATION_BYTES_COST=1` (R113). M0139-0007a held it default-off on
  2026-09-15 because it measured no category movement.
- **Narrowed widths.** The narrowed cost width (`relNarrowedWidths`, R121) was `TupleWidth` of the kept columns.
  That is type-only, so `char(n)` and `varchar(n)` columns counted at their declared sliding-scale width.

## 4. Change

- **R113 is default-on** (`sort_pgrelationbytes.go`); `"0"` opts out. With it, the standalone `store_sales` Sort
  costs exactly PG's 389568.13..396768.01.
- **The grouping sort seed carries the narrowed PG width** (`aggInputPGWidth`), next to the narrowed NCols. PG's
  `make_group_input_target` narrows the width the Sort reads.
- **`relNarrowedWidths` sums each kept column's ANALYZE `AvgWidth`** (`ColVarBytes`), falling back to the type width
  only when a column has none. Join 9's Sort width goes from 664 to 155.

## 5. Result

| instrument | before (327aace26) | slice 1 |
|---|---|---|
| SF1 Q64 GEQO evals before the first differing tour, worth off > 1e-3 | 14 | 6 |
| … off > 1e-4 | 33 | 29 |
| … largest error | 0.0089 | 0.0057 |
| SF1 Q64 eval 41 / eval 90 error | 0.0075 / 0.0061 | 0.00098 / 6.6e-7 |
| TPC-DS fire-set match, SF0.25 / SF1 | 98 / 94 | 98 / 94 |
| TPC-H plan parity | 22/22 | 22/22 |

No final plan changed except SF1 Q64, which reshuffles through GEQO and still diverges, now at depth 5 instead of 4.
Its fired result is byte-identical. The SF0.25 sweep shows no plan-shape changes.

## 6. Residuals

- **The joinrel target (`build_joinrel_tlist`).** PG keeps an attribute in a joinrel's target only while
  `attr_needed` names a relation outside the joinrel. goopg's `NeededCols` is one statement-wide name set, and a
  join's narrowed width is the sum of its children's. Join 9's Sort is therefore priced at 155 where PG's is 96.
- **Non-table leaves.** These still narrow with type widths. PG's `set_subquery_size_estimates` takes each output
  column's width from the subquery's own target list.
- **`costSortRun`'s width-less callers** keep the EntryBytes currency, because they have no width to give.

## 7. Gates

- units, TestPort\_RegressSuite and TestPort\_Isolation\* pass.
- tpch-spotcheck passes, and the acceptance arm reports values identical to baseline.
- TPC-H parity capture: 22/22. tpcds-sf025: PASS=99. tpcds-fireset: PASS.
- Pins: TestRelNarrowedWidthsNarrowsToNeededColumns now asserts the stawidth sum (mutation-checked).
  TestSortSeedNarrowsWithTheAggregate and TestCostWindowPresortedCreditIsInertAtZero were updated for the PG
  currency.

## 8. Slice 2 (2026-10-20): the joinrel target, after build\_joinrel\_tlist

PG keeps a base relation's attribute in a joinrel's target only while `attr_needed` names a relation outside the
joinrel (relnode.c `build_joinrel_tlist`). That means the query's output above the tree, or a join clause that
reaches past the joinrel. A join key consumed inside the joinrel drops out.

**What goopg did.** Its base rels narrow to one statement-wide name set, and a join path summed its two children's
triples. A key used only by a join inside the joinrel therefore stayed in the joinrel's width.

**The change** (`joinrelNarrowedWidths`, computed once when the joinrel is created):
- **The keep set** is the above-tree names (`outputCols`), plus every column name read by a search clause whose
  relids touch both the joinrel and a relation outside it.
- **The width** is a sum over the joinrel's published base members (`publishedMembers`: a SEMI/ANTI join's RHS is
  not published, per `joinPublishesInner`). Each member contributes its kept columns' `set_rel_width` widths,
  restricted to that keep set.
- **Application.** `narrowJoinWidths` stamps that triple on every path of the joinrel, in place of the children's
  sum (rule 3).
- **Declines.** It falls back to the children's sum when the problem is not output-eligible, a clause cannot be
  inventoried, or a published member does not narrow.
- **Name sets.** Like every narrowing input, the keep set is a set of names, so a name kept for one alias keeps
  every same-named column. That errs toward over-keeping.

**Measured on SF1 Q64:**
- Join 9's outer Sort width goes from 155 to 99–103 (PG 96).
- Before the first tour that differs from PG's, no eval is now off by more than 10⁻³ (was 1). The largest error is
  0.00067 (was 0.0046); eval 173 is 0.00022 (was 0.0046), and eval 41 is 0.00012 (was 0.00098).
- Only two worth-order inversions against PG remain before tour 262: eval 96 against 138, and eval 159 against 248.
  Both come from errors around 10⁻⁵.
- Fire-set 98/94 and TPC-H 22/22 are unchanged, and no fired query's plan changed at either scale.

**Residuals.**
- Base rels still narrow to the statement-wide set, which includes columns read only by their own restriction
  quals. PG's base `reltarget` excludes those.
- Non-table leaves still use type widths.

## 9. Slice 3 (2026-10-20): the base rel target, after build\_base\_rel\_tlists

**PG.** `build_base_rel_tlists` and `distribute_qual_to_rels` add to a base rel's reltarget only the Vars a relation
outside it needs: the final target list, and every Var of a clause whose relids span more than one rel. A column
read only by the rel's own restriction quals is evaluated at the scan and never emitted. `date_dim` filtered on
`d_year` and `d_moy` and joined on `d_date_sk` has a reltarget of `d_date_sk` alone.

**goopg.** Slice 2 applied this rule to joinrels. Base rels still narrowed to the statement-wide needed set, WHERE
included, so their cost triple counted restriction-only columns.

**Change.** `attrNeededAbove(relids)` factors slice 2's keep set out of `joinrelNarrowedWidths`: the above-tree
names plus every column of a search clause that touches both the rel and a relation outside it.
`baseRelNarrowedWidths` applies it to a base rel's kept columns, and `narrowBaseRelCostWidths` stamps that triple.
A reltarget that keeps nothing (`count(*)` over restrictions) cannot be represented in the triple, so it keeps the
statement-wide narrowing.

**Result.** The keep set is computed and applied. On TPC-DS Q3 all three base rels are eligible. No captured plan
moves:

- TPC-DS fire set: SF0.25 and SF1 plans are text-identical, costs included, between baseline and candidate.
- TPC-H parity stays 22 / 22, and the sf025 plan shapes stay the same.

No instrument consumes a base rel's width visibly on these corpora:

- The joins above already price with the slice-2 joinrel target, which never held restriction-only columns.
- A hash build over a small dimension is one batch, so its width does not change cost.
- EXPLAIN renders widths from the unnarrowed schema (M0146-0042).

**Pin.** `TestBaseRelNarrowedWidthsDropsRestrictionOnlyColumns`: a rel whose only outside need is its join key
keeps that key alone (width 4 of 12). With nothing needed above, it keeps the statement-wide set.

**Remaining (task open).** A non-table leaf (subquery, CTE) still takes its column widths from type widths. PG uses
the subquery's target-list widths (`set_subquery_size_estimates`, `set_cte_size_estimates`).

