# M0145-0008ac — a CTE reference in a sublink body is pulled up as a base rel

Status: done (2026-10-03, `c317b037b`; ea-ratchet re-pin `ralph(M0145-0008ac)`).
Parent: M0145-0008aa. Regression filed: M0145-0008af.

## PG behaviour

`pull_up_sublinks` (`./postgres/src/backend/optimizer/prep/prepjointree.c`)
turns `x IN (SELECT … FROM cte …)` into a semi join whose RHS holds the
sublink body's FROM items. A CTE reference is just another RTE there: a
`CTE Scan` base rel of the pulled-up jointree, joined in any order the
search likes.

## goopg before

M0145-0011 scope (c) admitted a `*CTEScan` leaf into the pulled body's
flat splice (`flattenPulledBodyTree`, jointreepullup.go) only behind the
default-off `GOOPG_PULLUP_CTE_LEAF`. M0145-0013 built the seam arm that
binds and prices such a leaf.

The promotion was held on 2026-09-25 (owner option (i)). Q95 timed out at
SF1: its semi inner rescanned a 3M-row hash join per outer row, where PG
binds a parameter into a hash join over an index probe. The blocker was
"parameterised inner paths through a join", which M0146-0049d built:

- d1: ExecHashJoin's empty-inner exit;
- d2: a statement-level CTE materialised once under a LATERAL;
- d3: parameterised hash join paths bound into the nested loop.

## Change

- `flattenPulledBodyTree` admits a `*CTEScan` leaf unconditionally.
- The knob variable is deleted, and `GOOPG_PULLUP_CTE_LEAF` is retired at
  M0145-0008ac in `flaglabels.go` and `scripts/planner-flags.env`.
- `TestFlattenPulledBodyTreeAdmitsCTELeaf` replaces the gate test.
- `TestSemiJoinSizeClampedByInnerJoin` reads the IN's first join. goopg now
  plans PG's exact `Hash Join(zc, Hash(HashAggregate(CTE Scan f)))` at
  PG's rows=15015.

## Measured effect

- **Q95:** the semi inner is PG's `Hash Join(CTE Scan ws_wh_1,
  Hash(Index Scan web_returns_pkey, wr_order_number = ws1.ws_order_number))`.
  Cost goes 258193 → 142781 at SF0.25 (PG 141550). No timeout at either
  scale.
- **Fire set:** fires Q14, Q23 and Q95; no timeouts introduced. Matches
  are flat (38 at SF0.25, 29 at SF1). CATEGORIES-EXCL-MATCH:
  - SF0.25: join-method 28→27, aggregation-strategy 17→16, parallelism
    35→34; parameterisation 27→28 and rendering 11→13 went the other way.
  - SF1: join-method 28→27; qual-placement 6→8 went the other way.
- **Values:** SF0.25 sweep 96/96, TPC-H arm 24/24. Regress with,
  subselect, join, union, select, aggregates and equivclass are identical
  to HEAD.
- **ea-ratchet:** 8 new keys over the new relation sets, every one
  PG-shared at the nearest scope. They were re-pinned under G4's PG-shared
  extension, with the per-key table in
  `analysis/m0145/m0145-0008ac/ea-repin-attribution.md`.

## Runtime regression (filed M0145-0008af)

The SF0.25 sweep reads Q14 8s → 39s and Q95 3s → 7s. EXPLAIN ANALYZE puts
both on the parameterised probe:

| query | goopg probe | PG probe |
|---|---|---|
| Q14 (16173 probes of store_sales) | Bitmap Heap Scan, ~0.7 ms each | Index Scan on store_sales_pkey, 0.06 ms each |
| Q95 (22 probes of web_returns_pkey on its non-leading column) | ~85 ms each | Index Only Scan with skip scan, ~1 ms each |

So goopg is 24.9s on Q14's first statement against PG's 4.8s, and 9.0s on
Q95 against PG's 2.8s. Two parts:

- the probe choice: goopg prices a parameterised index or index-only
  probe at about twice PG's cost, so a bitmap probe wins (named in
  M0146-0049's S4 escalation);
- the per-probe executor cost of the bitmap and skip-scan probes.

Under R3 the change stays and the regression is a task.
