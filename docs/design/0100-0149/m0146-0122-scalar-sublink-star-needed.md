# M0146-0122 — a scalar sublink's star no longer voids the needed-column set

Status: done 2026-10-09 (7bbbe52de). Parent: M0146-0019.

## Problem

After M0146-0121, TPC-DS Q23 at SF0.25 differed from PG 18.3 in one place.
In the `best_ss_customer` CTE, PG reads `customer` with an
`Index Only Scan using customer_pkey` as the hash inner. goopg seq-scanned
it at full width (324).

The CTE's HAVING clause is
`sum(...) > (95/100.0) * (SELECT * FROM max_store_sales)`. goopg's
needed-column collector (`neededColumnNames`, pathindexonlyneed.go) walked
the scalar sublink's body as a whole and reached its `*` target. That hit
the `default:` arm, which declines the statement's entire set. With no
known set, `addIndexOnlyPaths` offers nothing.

## PG behaviour

`transformExpr` expands an unqualified `*` inside an EXPR sublink with
`ExpandColumnRefStar`, which uses the namespace of the current query level
only. Every Var it makes has `varlevelsup = 0`, so it reads the sublink's
own FROM items and never an outer relation. `build_base_rel_tlists`
attributes Vars per RTE, so such a star adds nothing to the outer
relations' `attr_needed`.

## Change

`scalarBodyForColumns` drops a scalar sublink's unqualified star targets
before the body is walked. It is the scalar twin of `existsBodyForColumns`.

Dropping them is safe for two reasons:

- The sublink is planned by its own `planSelect`, which computes its own
  needed set.
- An EXPR sublink never joins the outer search: `pull_up_sublinks` converts
  only ANY / EXISTS, and goopg's scalar unnest is off by default and runs
  after the search.

Two cases still decline the set:

- **A qualified star (`t.*`).** It may name an outer relation, as a
  whole-row reference.
- **An IN sublink's star.** It is the pulled-up semijoin's key, so the
  outer search reads it.

## Verification

- **Test.** `TestScalarSublinkStarKeepsNeededSet` fails at HEAD. It also
  pins both declining cases.
- **TPC-DS fire set** (Q23 executed in both arms, results identical).
  - SF0.25: Q23 becomes a full MATCH, moving match 48 → 49 and scan-type
    24 → 23.
  - SF1: Q23 fires on cost only; its plan text is unchanged.
- **TPC-H.** Plans byte-identical; acceptance arm values identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25
  sweep (96/96) and ea-ratchet (7 findings, as at HEAD) all PASS.

## Not covered (ledgered)

Some star shapes inside sublinks still void the whole set. PG attributes
each one to the sublink's own RTEs:

- a qualified star that names the sublink's own FROM item;
- a star inside `ARRAY(SELECT * …)`.
