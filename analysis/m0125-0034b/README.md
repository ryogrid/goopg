# M0125-0034, C1's WITH-reference arm — measurement (2026-07-31)

Change under test: `internal/planner/joinorder.go` gains **connectivity mode**
(`orderByConnectivity`). A comma-FROM list that names a WITH reference has no
catalog row count for that item, so `reorderCommaFromByCardinality` used to
decline outright and the source order — CROSS chain and all — survived.
`tryBushyDP` declines on the same lists for an unrelated reason (leaf whitelist),
so nothing reordered them.

## SF0.5 subset probe — before/after

Before-arm for Q64 measured separately at HEAD `baf4e5cf` with a **1800 s**
budget, because the 300 s cap could not distinguish "slow" from "shape defect".

| query | before | after | note |
|---|---|---|---|
| Q30 | TIMEOUT (300 s **and** 1200 s, loop #14) | **PASS 1 s, 31 rows, ck=f47a48499fd7e070** | rows + checksum = oracle |
| Q81 | TIMEOUT | **PASS 1 s, 100 rows** | ck=n/a (saturated LIMIT window) |
| Q64 | **TIMEOUT 1848 s** | MISMATCH 32 s, goopg=0 oracle=2 | completes; answer wrong — see below |
| Q65 | TIMEOUT | TIMEOUT | declined by design (derived tables, LATERAL bound) |
| Q71 | PASS | PASS 14 s, 580 rows, ck=521a7af7606d10c1 | control, unchanged |
| Q5 | PASS | PASS 44 s, 100 rows | control, unchanged |
| Q31 | PASS | PASS 10 s, 19 rows, ck=2a74acfb556c21a7 | control, unchanged |
| Q47 | PASS | PASS 277 s, 100 rows | control, unchanged |

Reports: `probe-after/sweep-20260731-154152.txt`,
`probe-before-q64/sweep-20260731-155432.txt`.

## Q64's MISMATCH is NOT this change — it is a defect this change EXPOSED

Q64 had no answer at HEAD in 1848 s, so nothing regressed in the row-count
sense. But "it was already broken" is not something to assert; it is measured
here.

`q64body.sql` runs Q64's `cross_sales` CTE alone, grouped by `syear`:

| | groups | total rows | syear range |
|---|---|---|---|
| PG (`tpcds05`) | 5 | 26 | 1998–2002 |
| goopg | 9 | 26 | 1994–2002 |

The **same 26 rows** survive the 18-way join — the join is right. What is wrong
is `syear`, i.e. `d1.d_year`, where `d1` is one of **three aliases of
`date_dim`** (`d1` on `ss_sold_date_sk`, `d2` on `c_first_sales_date_sk`, `d3`
on `c_first_shipto_date_sk`). The years 1994–1997 goopg reports are first-sales
years, not sold years.

`alias_a.sql` / `alias_b.sql` isolate it in six relations, and settle
authorship. The two files differ only in where `customer` sits in the FROM list:

* **A** — source order `u, store_sales, d1, d2, d3, customer`. `d2`/`d3` precede
  the `customer` their equi-predicates need, so connectivity mode **fires**.
* **B** — `u, store_sales, d1, customer, d2, d3`, already cross-free, so the
  walk is a fixed point and the pass **declines**.

goopg's output for A and B is **byte-identical**, and both are wrong the same
way: `y1 = y2 = y3` on every row, where PG gives `1998 | 1993 | 1993`. The
projection reads one alias for all three. Note also that goopg emits five
separate `1993|1993|1993` groups under `GROUP BY 1,2,3` — the grouping keys are
distinct while the projected columns are not, which is the "right grouping,
wrong projection" signature M0125-0013 found in Q47's CTE body.

So: multiple aliases of the same table collapse to one in projection
resolution. It reproduces with the pass declining, in a query with no
Cartesian product, and is independent of FROM order. Filed as its own item;
it is a **silent wrong answer**, which this milestone's banner ranks above a
timeout.

## What this arm does not close

Q65's two inputs are derived aggregates, not WITH references. The parser
accepts `LATERAL` and discards it (`internal/parser/select.go`), so nothing in
the AST distinguishes a correlated derived table from an independent one, and
permuting one behind the item it references would change what the query means.
The pass therefore declines the whole list. Deferral ledger, 2026-07-31.
