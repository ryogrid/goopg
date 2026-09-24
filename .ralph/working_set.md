(idle — nothing in flight)

Loop #52 closed M0146-0002 (Parallel Hash): slices 1 (171c5d58a, executor)
and 2 (cf02e88b9, planner + label) landed, and TPC-H Q14 matches. Slice 3
attributed Q16's residual `parallelism` to the NOT IN route: the legacy
unnest turns NOT IN into an anti join outside the search, and the post-pass
Gather cannot elect Parallel Hash. Filed as M0146-0002b.

Next per the banner (item 3's M0146 continuation, file order): M0146-0002b
(recon: NOT IN anti-join census + NULL-bearing value check), then M0146-0002a
(recon: Q12/Q21/Q4 category regressions). M0145-0001 is still [!], pending
the owner's answer to the 2026-09-24 lineage escalation.

Parked: tmp/m0122-alter-system-wip.patch (M0122 ALTER SYSTEM).
