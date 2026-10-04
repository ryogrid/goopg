# M0146-0012 closure census: multi-relation EXISTS bodies (2026-10-05)

Question: after slices 1–2, does any benchmark or regress query need a
multi-relation EXISTS body's correlated conjunct to sink to a leaf? That is
the slice-1 exclusion, kept because goopg's EXISTS→ANY pass reads
correlation off the body's top quals.

## PG 18.3

- Plan fixtures `bench/tpcds/plans-pg/` and `bench/tpch/plans-pg/`: grep
  for SubPlans, hashed or not.
  - The non-hashed SubPlans are all scalar sublinks:
    - TPC-DS Q1, Q6, Q30, Q32, Q41, Q81, Q92;
    - TPC-H Q2, Q17, Q20.
  - Every multi-relation EXISTS is either pulled up to a semi join or
    hashed: TPC-DS Q10/Q35 (`ANY (c_customer_sk = (hashed SubPlan 2).col1)
    OR ANY (… hashed SubPlan 4 …)`), Q45, TPC-H Q16.
- Regress `subselect.out` / `join.out`: no non-hashed SubPlan over a join
  whose correlation is a leaf probe. The one join-bodied SubPlan
  (join.out:9303) is a scalar sublink over outer joins. PG 18 prints only
  the chosen side of an AlternativeSubPlan, so the census reads the
  chosen plans.

## goopg (SF0.25 capture `plans-20261005-055254.txt`)

The non-hashed SubPlans are the same scalar set (Q1, Q30, Q32, Q41, Q81,
Q92). The EXISTS in Q10/Q35/Q45 are hashed, as in PG.

## Disposition

- There is no witness. PG keeps a per-row EXISTS only when
  AlternativeSubPlan (`make_subplan`, `subselect.c`; choice in
  `setrefs.c`) prices the correlated plan under the hashed one, and goopg
  has no AlternativeSubPlan. Filed as **M0146-0012b**, unselected until a
  query shows the shape.
- The rule's correlated half (slice 2) waits on M0146-0062: filed as
  **M0146-0012c**.
- M0146-0012 closes on slices 1–2:
  - TPC-H Q2 1.34 s → 0.20 s, PG's SubPlan;
  - TPC-DS Q32/Q92 SubPlans match PG's;
  - aggregation-strategy −2 at both scales;
  - TPC-DS Q41 18.2 s → 10–12 s;
  - `flattenStrandedSeqScanFilters` deleted.
