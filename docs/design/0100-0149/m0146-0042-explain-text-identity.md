# M0146-0042 — EXPLAIN text-identity burn-down

Umbrella for the `rendering` class: EXPLAIN text differences on plans whose
shape already matches PG 18.3. The instrument is text-identical, meaning
plans equal to PG's line for line once costs are stripped. Children 0042a
(`m0146-0042a-rebuilt-candidate-ec-orientation.md`) and 0042b (same doc)
are done.

## Slice — varchar keys in Hash / Merge Cond (2026-10-05, `fb26f7dc6`)

- varchar has no `=` of its own, so `make_op` compares two varchar keys as
  text, and `get_oper_expr` shows each side's RelabelType:
  `((v1.s_store_name)::text = (v2.s_store_name)::text)`.
- goopg's Join Filter already printed this (`formatTextCastOperands`), but
  the key-pair renderer `formatJoinKeyCond` printed bare columns. It now
  applies the same rule; char(n) keys keep bpchar's operator and stay bare.
- `TestHashJoinBpcharRendersHashCondOnly`'s varchar expectations were not
  PG's and are corrected.
- Result: TPC-DS Q47 and Q57 are text-identical at both scales. SF0.25
  35 → 37, SF1 25 → 27.

## Slice — constant folding of a pulled sublink body (2026-10-05, `7eed1a031`)

- PG runs `eval_const_expressions` over the whole query, sublinks included,
  before `pull_up_sublinks`. A pulled-up EXISTS or IN body's WHERE
  therefore reaches `order_qual_clauses` already folded: `d_moy BETWEEN 3
  AND 3+3` is `(d_moy >= 3) AND (d_moy <= 6)`.
- goopg folded the statement's own WHERE (`planSelect`) but not a body
  resolved by the jointree sublink pull-up (`jointreepullup.go`, the EXISTS
  and ANY arms). Unfolded, `(d_moy <= (3 + 3))` cost two operators, so the
  stable cost sort put it after `(d_year = 2001)`. Both arms now run
  `foldQualConstants` over the resolved body WHERE.
- Test: `TestPulledSublinkBodyFoldsConstants` (EXISTS; EXISTS AND (EXISTS OR
  EXISTS); IN). Each expects PG 18.3's `Filter: ((m >= 3) AND (m <= 6) AND
  (y = 2001))`.
- Result: Q10 is text-identical at both scales (SF0.25 37 → 38, SF1
  27 → 28). The EC-equality ordering needed no change; the cost sort
  orders correctly once the bound is folded.
- Estimates: folding also fixes the scan's selectivity. The Q69 date\_dim
  filter now estimates 52 rows (PG 54; previously 71).
- Q69 lost its SF1 match (34 → 33). Both scales now put the catalog\_sales
  anti-join below the web\_sales one, where PG does the reverse.
  - With anti-join match fractions of `inner_rows / N`, the leading
    nested-loop term of the two orders is identical:
    `o·(i_web·i_cat/N − i_cat·i_web/N) = 0`. The election turns on
    second-order rounding.
  - The old 71-row estimate tipped it PG's way. The same tie at SF0.25
    was already routed by M0146-0005 to M0146-0014 as COSTTIE (ledger row
    2026-10-05).
  - CATEGORIES-EXCL-MATCH moved for Q69 only: SF0.25 join-method 24→25,
    parameterisation 25→26, parallelism 28→29 (the web anti-join becomes a
    Nested Loop over Materialize); SF1 join-order 52→53, qual-placement
    10→11.
- Gates: units, tpch-spotcheck, acceptance arm 24/24, sf025 (Q10, Q69
  changed), fire set, ea-ratchet, regress A/B over 14 files (pointer
  noise only).
- Evidence: `analysis/m0146/m0146-0042/slice-const-fold-q10-q69.txt`.

## Remaining classes (census of MATCH queries, 2026-10-05)

| class | queries | PG | goopg |
|---|---|---|---|
| ~~a BETWEEN bound is not folded~~ (done, `7eed1a031`) | Q10 (both scales), Q69 (SF1) | `(d_moy >= 3) AND (d_moy <= 6) AND (d_year = 2001)` | `(d_moy >= 3) AND (d_year = 2001) AND (d_moy <= (3 + 3))` |
| order of two constant EC equalities | Q31 | `(d_year = 1999) AND (d_qoy = 3)` | reversed |
| column qualification missing | Q8 (both), Q46, Q79 (SF1) | `a1.ca_zip`, `customer.c_customer_sk`, `store.s_city` | bare |
| a reference through an elided subquery / CTE / Append | Q56, Q75 | `sum((sum(store_sales.ss_ext_sales_price)))`, `date_dim.d_year` | `sum(ss.total_sales)`, `curr_yr.d_year` |
| alias suffix numbering | Q8, Q56, Q58, Q75 | numbered over the final flattened range table (`set_rtable_names`) | numbered in RTID allocation order |
| a Sort Key detail | Q43 (SF1) | | |
