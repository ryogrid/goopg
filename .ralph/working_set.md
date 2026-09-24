(idle — nothing in flight)

This loop: M0146-0002d recon closed (no code change). Q16's NOT IN filter is
kept off the partsupp scan for three independent reasons, filed as impl
children:
- 0002e: placement (conjunctIsLocalEligible declines every sublink; admit
  IsNonCorrelated, rebase with scopeIgnore);
- 0002f: SubPlan parallel safety (PG clauses.c:900-912), plus worker-safe
  SubPlan evaluation;
- 0002g: the hashed SubPlan EXPLAIN label.
PG's Q16 plan is saved in analysis/m0146/m0146-0002d/.

Next per the banner (M0146 continuation, file order): M0146-0002e.
M0145-0001 is still [!], pending the owner's answer.
Parked: tmp/m0122-alter-system-wip.patch.
