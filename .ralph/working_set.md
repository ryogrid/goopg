(idle — nothing in flight)

This loop: M0145-0008e landed as d7dad3c1a, with its premise corrected. The
jointree arm DOES narrow. Q18 was losing its whole join search to
`seam-decline reason=residual-hits-pad`: the non-pulled-up grouped IN made
the pad check's name walk partial. The pad check now steps over an
uncorrelated inner plan. Q18 went from 13.17 s to 9.53 s (legacy 9.94 s).
The 0008b design doc now carries a Correction section.
Filed M0145-0008j (EXPLAIN drops one of two stacked Filters; pre-existing).

Next per the banner (item 3): M0145-0008f (HashAggregate build throughput,
Q18 semi body 12 s vs PG 3.3 s), then 0008g (expression IN-list selectivity,
Q22), 0008h (planner crash, S2 escalation, awaiting owner placement),
0008i, 0008j, then the legacy-deletion slices and the M0146 continuation.
Parked: tmp/m0122-alter-system-wip.patch.
