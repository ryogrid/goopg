# C-06 re-measured after C-06s — the premise is INVERTED

Date 2026-09-07. Binary: current `plan-narrowing-and-etc` HEAD (includes
C-06s `922a3f444`). Cluster: TPC-H SF=1 bench cluster, port 65433, db `tpch`,
`GOOPG_ANALYZE_SEED=20260905`, fresh cgroup-capped server per arm.

## What C-06 was blocked on

> the flip is NOT plan-neutral. TPC-H Q13 moves, **and the OFF plan is the
> PG-parity one** — `Index Only Scan using customer_pk` + `Hash Left Join`
> (cost 66,218) vs the ON path's `Index Scan` + `Merge Left Join` (338,223).

Retiring `GOOPG_PGSHAPED_COLLAPSE` would therefore have deleted the only
reachable spelling of a PG-shaped Q13.

## What is true now

C-06s taught the search PG's `JOIN_RIGHT`, and that changed which arm is
PG-shaped. Two `estimate-audit -plan-only` captures, one per flag value:

| arm | Q13 plan | printed cost |
|---|---|---:|
| **ON (shipped default)** | `Hash Right Join`, `Hash Cond: (orders.o_custkey = customer.c_custkey)`, `Index Only Scan using customer_pk` | 124,999 |
| OFF (`=0`) | `Hash Left Join`, `Hash Cond: (customer.c_custkey = orders.o_custkey)`, `Index Only Scan using customer_pk` | 60,308 |

**PG 18.3's own captured plan** (`bench/tpch/plans-pg/Q13.txt`):

```
->  Hash Right Join  (cost=5781.42..56243.29 rows=1484859 width=12)
      Hash Cond: (orders.o_custkey = customer.c_custkey)
      ->  Seq Scan on orders ...
      ->  Hash ...
            ->  Index Only Scan using customer_pk on customer ...
```

So **the ON arm is now the PG-parity plan** — same join type, same hash
condition orientation, same index-only scan. The OFF arm is not.

Only Q13 differs between the arms (21 diff lines, all in the Q13 section;
the other 21 queries are byte-identical).

## And the ON arm is faster

Three reps per arm, fresh capped server each, rows identical (34, matching
the pinned `Q13_EXPECTED=34`):

| arm | reps | best | median |
|---|---|---:|---:|
| **ON** | 4.489 / 5.399 / 4.785 s | **4.49** | **4.78** |
| OFF | 9.241 / 5.819 / 6.662 s | 5.82 | 6.66 |

1.30x on best-of-3, 1.39x on median. The OFF arm's spread is wide (9.24 high),
so the honest claim is the direction, not the precise factor — but the ranges
do not overlap at the low end and the direction is unambiguous.

## Consequence

C-06's stated blocker is discharged and **reversed**: the OFF path now holds a
plan that is neither PG-shaped nor faster. Note the cost numbers still are not
comparable across arms — per the C-06 diagnosis (`0195cca47`), on the OFF arm
the join never enters the search and is priced by the plan-tree estimator,
which is the `c20a-two-estimators` surface seen from the other side. That is
why the arm with the HIGHER printed cost is the faster one.

What this does NOT do by itself: make the flip byte-identical. The gate as
written ("byte-identical plans for the flip") still fails on Q13, so retiring
the flag remains a deliberate decision rather than a gate pass — but the
argument for keeping it has gone from "it holds the only PG-shaped plan" to
"it holds a slower non-PG plan".
