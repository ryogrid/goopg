# M0146-0234 — sorts are priced in PG's byte currency, and narrowed widths come from ANALYZE

Status: slice 1 landed 2026-10-20; the task stays open for the joinrel `attr_needed` narrowing. Parent: M0146-0214.
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
