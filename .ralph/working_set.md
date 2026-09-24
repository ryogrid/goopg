(idle — nothing in flight)

This loop: M0145-0008d landed as e32442db1. Grouping follows PG's
processed_groupClause (direction copy + ORDER BY prefix reorder). The probe
is identical to PG. Gates all green, including ea-ratchet 52/52.
Filed:
- M0145-0008h (impl, S2 escalation): a planner panic kills the server on the
  upstream aggregates.out:3158 query (GROUP BY pk ORDER BY a dependent column,
  with array_agg ORDER BY). Reproduces at HEAD.
- M0145-0008i (impl): multi-relation remove_useless_groupby_columns (Q18's
  five keys → PG's two).

Next per the banner (item 3): M0145-0008e (jointree lowering narrows join
tuples, Q18), then 0008f/g, then 0008h/i unless the owner re-places 0008h,
and the legacy-deletion slices. The M0146 continuation (0002f) after.
Parked: tmp/m0122-alter-system-wip.patch.
