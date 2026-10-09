# M0146-0046 — `ctid` through Index Scan and Index Only Scan

Status: done (2026-10-03). Parent: M0146 (banner item 2a, third S2 batch).

## The defect

`ctid` is evaluated from the slot's carried TID (`hasCTID` /
`ctidBlock` / `ctidOff`). That is set by `seqScanOp` and
`bitmapHeapScanOp`, but not by `indexScanOp`, so `SELECT ctid, a FROM zc
WHERE a BETWEEN 5 AND 6` returned NULL under an Index Scan (PG 18.3:
`(0,5)`, `(0,6)`).

Separately, the index-only producers counted only the table's own columns
as needed. A statement reading `ctid` could therefore elect an Index Only
Scan, which has no heap tuple at all. TPC-DS SF0.25's `count(ctid) FROM
item` returned 0 (PG 18000). PG never does this: `check_index_only`'s
attrs_used includes system attributes, and no index covers them.

## The fix

- **Index Scan.** `indexScanOp.Next` stamps the emitted slot with the
  HOT-resolved live TID: the version whose columns it decoded, the same
  one `currentTID` reports to LockRows.
- **Index Only Scan.** `neededColumnsOfRel` (pathindexonly.go) appends a
  synthetic, never-covered entry for each heap system column the
  statement reads for the relation: `ctid`, `xmin`, `xmax`, `cmin`,
  `cmax`, `tableoid`. Every index-only producer (restriction, full-index,
  parameterised, skip) then declines, because they all go through
  `indexCoversColumns`. The legacy `tryPromoteIndexOnlyScan` already
  required plain index-column targets.

## Verification

- Scratch probes against PG 18.3 are identical: the Index Scan `ctid`
  pair, `count(ctid)` (no Index Only Scan), `ctid` of an UPDATEd row,
  and a ctid-equality lookup.
- `TestIndexScanCarriesCtid` pins them.
- Regress A/B: `tidscan`, `tid`, `tidrangescan` and `update` are
  byte-identical; `delete` still passes.
- Gates: units, spotcheck, sweep 96/96, fire set (no plan changes), TPC-H
  arm.

## Residuals

- The other system columns' **values** under an index scan (`xmin` etc.)
  were not audited here; only their effect on index-only election is
  covered.
