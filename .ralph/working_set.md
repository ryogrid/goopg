(idle — nothing in flight)

This loop: M0146-0002e landed as bb90e51a4. Uncorrelated SubPlan restrictions
now sit on their base relation, as in PG: Q16's NOT IN is on the partsupp
scan, and the InitPlan quals of Q22 and TPC-DS Q6 are on their scans. A bare
ANY `x IN (SELECT …)` stays in the residual (PG pulls it up); without that
exception TPC-DS Q95 timed out at SF1, which the fire-set gate caught.
Category counts did not improve (TPC-H parameterisation 6→7 via Q22;
SF0.25 Q6 +1 net). Filed M0146-0002h (SubPlan selectivity 0.5).

Next per the banner (M0146 continuation, file order): M0146-0002f (SubPlan
parallel safety + worker-safe evaluation, so Q16 can elect Parallel Hash),
then 0002g (hashed label / InitPlan render position), then 0002h.
M0145-0001 is still [!], pending the owner's answer.
Parked: tmp/m0122-alter-system-wip.patch.
