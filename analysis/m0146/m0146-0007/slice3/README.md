# M0146-0007 slice 3 (M0146-0007c): the CTE push descends into a NestedLoopIndexJoin

TPC-DS Q78's `ws` CTE body joins `date_dim` as the parameterised inner of a
NestedLoopIndexJoin. `pushConjunctTraced` had no arm for that node, so
`ws_sold_year = 1998` reached neither the date_dim probe nor the move, and
`ws` kept `Subquery Scan on ws  Filter: (ws_sold_year = 1998)`.

## Change

`pushConjunctIntoNLI`, on CTE-path descents only (`pushTrace.cteMove`):
- A conjunct reading only outer columns descends into Outer, the
  preserved side for INNER, LEFT, SEMI and ANTI.
- One reading only inner columns of an INNER join is ANDed into the
  probe's `Cond` (IndexScan / IndexOnlyScan), in the scan's coordinates.
  That is the `Filter:` PG prints on the inner Index Scan. The probe is
  mutated in place, so the aliasing Memoize stays consistent.
- The move proof uses the *Join arm's containment rule (the conjunct's
  source identities inside the entered side). An existing equal copy
  clears it.

## Results

`q78-residual.txt`: no `Subquery Scan` remains in Q78, and `ws`'s
`d_year = 1998` sits on the inner Index Scan as in PG. Q78's SF0.25 record
stays at the same node (depth 2 under Sort) but moves from `join-order`
to `qual-placement`; SF1 is unchanged. Row counts, TPC-H and regress are
unchanged (`gates.txt`).
