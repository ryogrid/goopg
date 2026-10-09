# M0146-0073 — retire the one-relation index rule's correlated arm

Status: done 2026-10-06 (`5b50c57ba`).

## Background

- `planIndexScanFromWhere` is the rule-based WHERE → index producer. It
  runs on a single-relation scope that the join search left as a bare Seq
  Scan tree.
- Its correlated arm turned `WHERE inner.col = outer.col` into an
  `IndexScan` keyed on the `OuterColumnRef`. It was priced against a
  bitmap alternative by `bitmapOverCorrelatedProbe`.
- After the M0145-0008 cutover the search gained its own producer for
  correlated keys (M0146-0015a), and the arm stopped firing on TPC-H and
  TPC-DS. M0146-0012 slice 2 kept it for one case only: an outer key from
  a literal VALUES column, which goopg typed `int8` against an `int4` index
  column. The search's `restrictionKeyUsable` refuses that uncast probe.
  The witness was regress `join`'s `(values (0,9998)) v(id,x), lateral
  (… where unique2 = v.x …)`.
- M0146-0062 now types such literals `int4`, which removes that reason.

## Change

- The column = column arm declines.
- `bitmapOverCorrelatedProbe`, now unreferenced, is deleted.
- The call-site comment now says the correlated half is retired. The
  uncorrelated half stays (M0146-0060).

## Verification

No plan changed anywhere it was measured:

- sf025 plan shapes 99/99 same;
- fire set: no fires;
- regress A/B: join, subselect, with, select, aggregates, rangefuncs,
  create_index and updatable_views are byte-identical to HEAD, and
  plpgsql moved within its flap. The arm no longer fired.

Gates:

- units PASS;
- tpch-spotcheck PASS;
- acceptance arm 24/24;
- sf025 96/96;
- ea-ratchet PASS.

## Found while verifying

- The witness's own plan differs from PG's either way. goopg plans
  `Seq Scan … Filter: (unique2 = x)` under the sublink, where PG uses
  `Index Scan using tenk1_unique2`. So the search does not build the
  correlated probe inside a sublink nested in a LATERAL item (ledgered).
- The same query returns a wrong result, identically at HEAD. goopg
  returns `0|9998|0` and `1|1000|0`; PG returns only `0|9998|0`.
  Reproduced on small tables: only the `= ANY (subquery … WHERE … = v.x
  OFFSET 0)` form inside a LATERAL item is wrong. Without OFFSET 0, and in
  scalar-subquery form, it matches PG. Filed as M0146-0079 (S2).
