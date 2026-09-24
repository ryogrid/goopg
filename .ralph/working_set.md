(idle — nothing in flight)

This loop: M0145-0008g landed as 70d0478bd. An expression-operand IN list is
now estimated as PG's scalararraysel. Q22's leaf is 1750 rows = PG, and its
upper plan is PG's. TPC-H categories net -2 on Q22; SF0.25 unchanged;
ea-ratchet 52/52. Q22's anti join stays hash because nestloopCost lacks
final_cost_nestloop's semi/anti arm. Filed as M0145-0008l.

Next per the banner (item 3): M0145-0008h (planner crash, S2 escalation;
check the banner for owner placement first), then 0008i, 0008j, 0008k,
0008l, then the legacy-deletion slices and the M0146 continuation (0002f).
Parked: tmp/m0122-alter-system-wip.patch.
