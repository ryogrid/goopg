# M0148\-0001c3a12 — row\-comparison index quals

Parent: M0148\-0001c3a6. PG reference:
- indxpath.c match\_rowcompare\_to\_indexcol / expand\_indexqual\_rowcompare;
- nbtree's SK\_ROW\_HEADER scan keys \(nbtutils.c \_bt\_check\_rowcompare\);
- selfuncs.c rowcomparesel and scalarineqsel's NULL arm.

## Slice 1 — Index Scan / Index Only Scan

### The gap

- `(thousand, tenthous) > (NULL, 5)` over an index on `(thousand,
  tenthous)` planned a Seq Scan with the comparison as a Filter.
- PG makes it an index qual: `Index Scan … Index Cond: (ROW(thousand,
  tenthous) > ROW(NULL::integer, 5))`.

### The design

- **Path:** restrictionRowCompare matches a conjunct `(c0, …, ck) op (v0,
  …, vk)`:
  - the left row's columns are the index's leading columns, in order and
    ascending; a row on the right is commuted;
  - the right row's elements are constants or NULL;
  - op is an inequality.

  It yields an indexPathClause flagged rowCompare. Its op / key are the
  lossy inclusive bound on c0 \(`>=` for `>` / `>=`, `<=` for `<` / `<=`\),
  where nbtree's row key starts the scan; local is the comparison. The
  plain and index\-only producers place it in restriction order, with no
  trailing array beside it, and plainRestrictionBindsOnlyPrefix counts it.
- **Plan:** IndexScan / IndexOnlyScan gain `RowCompare` \(the comparison\)
  and `RowCompareLow` \(whether LowKey or HighKey is its bound\), plus an
  IndexCondRowCompare order item.
- **Executor:**
  - IndexScan builds a multi\-column trailingKeyCheck \(newIndexRowCheck:
    key columns 0..k decoded per entry, the comparison rewritten onto
    them\), so an entry it rejects is never fetched;
  - IndexOnlyScan applies it as its trailing predicate over the covered
    row.

  The conjunct leaves the Filter. Results are exact either way: the bound
  is lossy and the check exact.
- **Cost:** rangeIndexSelectivity prices the clause by the comparison
  \(rowcomparesel over the leading pair\). rowCompareSelectivity now
  returns 0 for a NULL constant, as eqsel / scalarineqsel do, so `(…) >
  (NULL, 5)` estimates one row and takes PG's Index Scan at the same cost
  \(`0.29..8.28`\).
- **EXPLAIN:** rangeCondWithRowCompare prints the comparison whole —
  index columns left, commuted as PG's expand\_indexqual\_rowcompare
  does, NULL labelled with its column's type — beside the column's other
  bound in restriction order.

### Verification

- **Scratch PG:** plan and cost identical for the witness. Rows identical
  over NULL\-bearing data, every direction, a commuted form, NULL leading
  and trailing constants, plain and forced\-index plans, and LIMIT.
- **TestRowCompareIndexQual:** the plan and 4 row results; the plan fails
  with the producer disabled.
- **Regress:** create\_index 2500→2483.
- **Gates:**
  - units, tpch\-spotcheck, sf025 same=99;
  - fire\-set fires=none on sf025, sf1 and TPC\-H;
  - acceptance 24 MATCH;
  - TestPort\_Isolation ok.

### Not covered

- **Bitmap form:** PG often prefers `Bitmap Heap Scan … Recheck Cond:
  (ROW(…) > ROW(…))` with the same Index Cond; goopg has no bitmap row
  comparison, so it picks the Index Scan.
- **Prefix and DESC:** a row comparison behind an equality prefix, or over
  DESC index columns.
- **Recheck:** PG truncates a comparison whose later columns are not index
  columns \(expand\_indexqual\_rowcompare\) and rechecks the rest.
- **btree\_index's pg\_proc witnesses:** these also need
  M0148\-0001c3a7 \(catalog scans\).
