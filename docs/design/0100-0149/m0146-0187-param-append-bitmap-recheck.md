# M0146-0187 — a parameterised Append member's bitmap probe prints as Recheck Cond

Status: closed 2026-10-14.

## Problem

TPC-DS Q54 drives an item nested loop into a parameterised Append. One member
is a Bitmap Heap Scan on catalog_sales probed by `catalog_sales_pkey`:

- goopg printed the probe clause as `Filter: (cs_item_sk = item.i_item_sk)`;
- PG 18.3 prints `Recheck Cond: (cs_item_sk = item.i_item_sk)`.

After M0146-0184 this was Q54's only divergence at SF0.25; the classifier
counts it as qual-placement.

## Diagnosis

PG's Bitmap Heap Scan carries two clause lists:

- `bitmapqualorig`, the index condition re-evaluated for lossy pages,
  printed as `Recheck Cond:`;
- `qpqual`, the other restrictions, printed as `Filter:`.

goopg's `bindParamProbe` (paramjoin.go) rebinds a parameterised member's
probe keys onto the nested loop's outer row. A lateral join has no merged
outer++inner row for `BitmapQual`'s coordinates, so the function keeps the
recheck on the heap scan's `Cond`, which is evaluated per tuple, and clears
`BitmapQual`. Execution is correct, because the recheck holds on exact pages
anyway. EXPLAIN, though, renders `Cond` as `Filter:`.

The single-scan bitmap arm (M0146-0012 slice 1) already puts its probe in
the Recheck Cond. Only this path did not.

## Change

- `BitmapHeapScan.ProbeRecheckN` (plan.go) counts the leading AND-conjuncts
  of `Cond` that are the probe's recheck.
- `bindParamProbe` places the recheck conjuncts first, sets the count, and
  appends the scan's existing `Cond` after them. Execution still evaluates
  all of `Cond` on every tuple.
- EXPLAIN (`operators_explain.go`, the BitmapHeapScan arm) splits `Cond` at
  the count when `BitmapQual` is empty. The leading part prints as
  `Recheck Cond:`; the rest, plus any attached filter, prints as `Filter:`.

## Evidence

Files are in `analysis/m0146/m0146-0187/`.

- **Q54 at SF0.25:** the EXPLAIN (COSTS OFF) text is identical to PG's.
- **Fixture:** a two-member UNION ALL probed per outer row by bitmap scans,
  with a per-member restriction. PG 18.3 prints each member's
  `Recheck Cond: (k = o.k)` and `Filter: ((v % 3) = 0)`, and goopg now
  prints the same text. Results are equal (666 rows, the same sum).
- **Fire set:** SF0.25 match 75 → 76 (Q54) and qual-placement 5 → 4. SF1 is
  unchanged; its Q54 still differs in join order and Gather Merge.
- **SF0.25 sweep:** 99 of 99 pass, with Q54 the only change.
- **TPC-H:** the acceptance arm is value-identical.
- **Test:** `TestExplainParamAppendBitmapRecheck` checks PG's plan text and
  fails with the split disabled.

## Not ported (ledgered)

- goopg evaluates the recheck on every tuple, where PG evaluates it only on
  lossy pages. Results are identical; only the per-tuple cost differs.
- SF1 Q54's remaining differences (join order of store and customer_address
  versus store_sales, Gather Merge versus Sort over Gather) are not filed:
  the SF0.25 record, which the banner prefers, now matches.
