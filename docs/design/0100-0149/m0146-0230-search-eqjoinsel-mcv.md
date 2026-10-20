# M0146-0230 — the join search estimates equality joins with eqjoinsel_inner's MCV branch

Status: done 2026-10-19. Parent M0146-0214 (TPC-DS Q64, GEQO-RNG). Evidence: `analysis/m0146/m0146-0230/`.

## 1. Record

M0146-0214 slice 3 compared every GEQO tour of SF0.25 Q64 with PG's. The first large worth gap is a row estimate:
joining customer\\_demographics cd2 under `cd1.cd_marital_status <> cd2.cd_marital_status` gave 1536640 rows
(1 − 1/5) where PG has 1536621. PG's neqjoinsel is 1 − eqjoinsel, and eqjoinsel\\_inner (selfuncs.c) takes its MCV
branch whenever both operands carry an MCV list. The imported statistics carry those lists.

## 2. Two estimators, one missing the branch

- The **plan-node estimator** (cardinality.go `eqjoinselInnerMCV`, take2 P1-15) already ported the MCV branch.
- The **join search's per-clause estimator** (joinselectivity.go `eqJoinSelectivityExt`) ported only the no-MCV
  branch, `(1 − nf1)(1 − nf2) / max(nd1, nd2)` (ledger 2026-08-04, M0127-P5.6-a). This is the one that elects
  plans: DP and GEQO, including tour worths.
- Its semi arm already shares `eqjoinselSemiCore` with the plan side, so only the inner arm was missing.

Two sibling estimators that disagree let the search elect plans that the node estimate then prices differently.

## 3. Change

- `eqjoinselInnerMCVCore(st1, st2, nd1, nd2, bp)` (cardinality.go) is the arithmetic, extracted unchanged:
  - pair the MCV lists, each entry used at most once (indexed, not nested);
  - compute matchprodfreq and the matched and unmatched frequencies;
  - other = 1 − nullfrac − matched − unmatched;
  - compute totalsel1 and totalsel2 per PG and take their minimum;
  - bp pairs bpchar values on their trimmed form (bpchareq).
- `eqjoinselInnerMCV` (plan side) resolves its operands and calls it.
- `eqJoinSelectivityExt` (search side) calls it first when both `joinVarStats.stats` carry MCV lists, with
  `getVariableNumDistinct`'s nd1 and nd2, and reports the result as measured. The no-MCV branch stays otherwise.
  `<>` (neqjoinsel's inner arm) and the SEMI/ANTI clamp `Ssemi ≤ N2 · Sinner` both read it, as PG's do.

## 4. Verification

- Pin: TestSearchEqJoinUsesBothSidesMCVLists. It checks a hand-built pair of MCV lists against PG's formula,
  worked independently of the code: 0.27, where the no-MCV branch gives 0.25. It also checks that the plan-side
  core agrees, that the no-MCV branch is kept when one side has no list, and that bpchar values pair. It is
  mutation-checked.
- Q64 (SF0.25), tour 2 traced against the PG replica: the cd2 join is now 1536621 rows, PG's number. The next
  difference in that tour is a cost, not an estimate. Its merge join with `item` has equal rows (939), but goopg's
  startup is 20675.72 against PG's 18896.82 and its total 106647.34 against PG's 106969.50. That goes back to
  M0146-0214.
- Fire set, the queries whose plans changed:
  - At SF0.25 (Q16, Q33, Q56, Q59, Q60, Q72, Q94, Q95), row estimates identical to PG's went from 220 to 247 of 250.
  - At SF1 (Q16, Q24, Q56, Q59, Q60, Q95) they went from 170 to 195 of 203.
  - Plan shapes are unchanged: all still MATCH, except SF1 Q72 at depth 7 as before. Parity is held at SF0.25 97/99
    and SF1 94/99.
- Gates:
  - units, TestPort\\_RegressSuite, tpch-spotcheck and TestPort\\_Isolation\\* (the timing-based family, run because
    selectivity changed) pass, as does the acceptance arm (values identical).
  - The sf025 sweep is PASS=99 with 8 plans changing text.
  - tpcds-fireset passes, with all 14 fired results byte-identical.

## 5. Left as is

When the MCV arithmetic yields a selectivity of 0 or less, the shared core keeps the plan side's existing fallback
to the no-MCV branch. PG instead returns the 0 and clamps the row count later (clamp\\_row\\_est). Ledger row
2026-10-19.
