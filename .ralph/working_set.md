(idle — nothing in flight)

M0132-S13 DONE this loop (uncommitted as of writing; commit follows). Prepared-
statement cache landed (`docs/design/0132-0005-...`): Execute skips
`parser.Parse` on a plan-cache hit (`dispatch_extended.go`), Describe reads the
plan from `s.pc` on `sessionPlanCatalog` (`extended.go`). Proven: c=50 `-S`
prepared −22.3% → −1.07% (parity). The "prepared > simple" A/B assertion is
structurally UNSATISFIABLE — goopg's SIMPLE path also reads `s.pc`
(M0098-0005), so prepared has no plan advantage to win back (PG simple re-plans
every time, hence PG prepared +11..24%). Ledger row M0132-S13 carries the
resume point + why. M0132 is now FULLY CLOSED (S1–S13 all [x]).

Next per banner: M0131's remaining unchecked items, then M0130's, then M0119
then M0122 (top-to-bottom). Re-read the `## Current Priority` banner before
selecting — it is the sole ordering authority.
