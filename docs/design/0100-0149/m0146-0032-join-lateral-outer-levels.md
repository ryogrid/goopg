# M0146-0032: a JOIN LATERAL sees the comma items before it one level further out

Status: landed 2026-10-02 (banner item 2a, S2).

## Defect

regress join.sql:

    select * from int8_tbl a, int8_tbl x
      left join lateral (select a.q1 from int4_tbl y) ss(z) on x.q2 = ss.z

goopg evaluated `a.q1` as `x.q1`. It returned `z = 4567890123456789` where
`x.q1 = x.q2 = 4567890123456789`, and the wrong number of rows (45, where
PG returns 57).

`planFromItem` built the JOIN LATERAL right side's resolve context with
`mergeResolveContexts(lateralCtx, leftCtx)`. That flattened the comma items
already planned (`a`) and the join's own left input (`x`) into one level-1
schema, `[a.q1, a.q2, x.q1, x.q2]`, so `a.q1` became level-1 column 0.

At run time the lateral join's `openLateral` pushes only its LEFT row
(`x`). Level-1 column 0 is therefore `x.q1`. The enclosing comma join never
became lateral to push `a`, because nothing in its right item referenced an
outer level.

## Change

The right side's context is now the left input's context at level 1, with
the earlier comma items (`lateralCtx`) as its `parent` at level 2. In PG
terms these are two separate scope levels. So:

- `a.q1` resolves as a level-2 outer reference. `nodeReferencesOuter` sees
  it escape the inner lateral join, so the comma join becomes lateral and
  pushes `a`'s row.
- `x.*` references stay level 1, supplied by the join's own left-row
  push.
- `mergeResolveContexts` had no other caller and is removed.

## Verification

`TestJoinLateralRefersToEarlierFromItem` (executor) checks two queries
against PG 18.3:

- the regress query returns 57 rows, 40 with a non-NULL `z`, and every
  non-NULL `z` equals `a.q1` (the old code gets 45 rows, 10 mismatched);
- `(select a.q1 + x.q1)` mixes both levels in one expression and matches
  PG's rows.

A probe also matched PG byte for byte on those two queries plus a
JOIN LATERAL `generate_series(1, a.q1 % 3)`, a function reaching an
earlier comma item.

- Regress A/B: `join` shrinks 15100 → 15058 diff lines (46 fixed, only
  the known row flap and Materialize/Output reorders on the other side).
  `rangefuncs` and `subselect` are unchanged.
- Gates pass: units, spotcheck, sweep 96/96, arm (values identical), fire
  set (no TPC-DS plan changed) and ea-ratchet.

## Left open (ledgered)

goopg plans the de-lateralised join without PG's PlaceHolderVar
(`(a.q1)` evaluated at the `y` scan). PG's plan is a Merge Left Join over
`Output: (a.q1)`. The results now agree; the plan shape and the PHV
rendering do not.
