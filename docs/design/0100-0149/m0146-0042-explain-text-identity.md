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

## Remaining classes (census of MATCH queries, 2026-10-05)

| class | queries | PG | goopg |
|---|---|---|---|
| a BETWEEN bound is not folded, and an EC equality is not moved last | Q10 (both scales), Q69 (SF1) | `(d_moy >= 3) AND (d_moy <= 6) AND (d_year = 2001)` | `(d_moy >= 3) AND (d_year = 2001) AND (d_moy <= (3 + 3))` |
| order of two constant EC equalities | Q31 | `(d_year = 1999) AND (d_qoy = 3)` | reversed |
| column qualification missing | Q8 (both), Q46, Q79 (SF1) | `a1.ca_zip`, `customer.c_customer_sk`, `store.s_city` | bare |
| a reference through an elided subquery / CTE / Append | Q56, Q75 | `sum((sum(store_sales.ss_ext_sales_price)))`, `date_dim.d_year` | `sum(ss.total_sales)`, `curr_yr.d_year` |
| alias suffix numbering | Q8, Q56, Q58, Q75 | numbered over the final flattened range table (`set_rtable_names`) | numbered in RTID allocation order |
| a Sort Key detail | Q43 (SF1) | | |
