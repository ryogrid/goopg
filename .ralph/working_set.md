(idle — nothing in flight)

Loop #54 landed M0146-0002c (c2339a072): NOT IN now plans as a SubPlan, as
PG does. Values, TPC-DS plans and the TPC-H categories are unchanged
(Movement: none). Q16's filter lands on the join instead of the partsupp
scan: M0146-0002d (recon). Also filed M0146-0015 (recon): a pre-existing
regress subselect query runs >1 h at HEAD (nested EXISTS/NOT EXISTS over
tenk1) and blocks the subselect comparison.

Next per the banner (item 3's M0146 continuation, file order): M0146-0002d,
then M0146-0002a; M0146-0015 sits later in file order. M0145-0001 is still
[!], pending the owner's answer.

Parked: tmp/m0122-alter-system-wip.patch (M0122 ALTER SYSTEM).
