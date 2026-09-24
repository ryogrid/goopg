(idle — nothing in flight)

Loop #53 closed recon M0146-0002b: the NOT IN → anti join conversion is
value-correct (6 NULL cases identical to PG 18.3) and reaches only TPC-H Q16.
Follow-up filed: M0146-0002c (impl), which drops the legacy unnest's Negated
IN arm so NOT IN plans as a hashed SubPlan; expected to move Q16.

Next per the banner (item 3's M0146 continuation, file order): M0146-0002c,
then M0146-0002a (Q12/Q21/Q4 category regressions). M0145-0001 is still [!],
pending the owner's answer.

Parked: tmp/m0122-alter-system-wip.patch (M0122 ALTER SYSTEM).
