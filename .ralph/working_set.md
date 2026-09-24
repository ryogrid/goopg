(idle — nothing in flight)

This loop: M0145-0008c done. The ea-ratchet at HEAD had 3 NEW keys: Q95 (the
flip's) and two Q14 keys (from M0146-0002e, bb90e51a4, which had not run the
ratchet). All three are PG-shared at PG's nearest scopes. Repinned under G4's
extension as a standalone non-code commit; make ea-ratchet PASS 52 vs 52.
Evidence: analysis/m0145/m0145-0008c/.
Lesson: run `make ea-ratchet` for optimizer changes too (ledgered as an owner
gate-list item). Port 5534 must be free.

Next per the banner (item 3, owner order): M0145-0008d (grouping pathkeys
adopt the ORDER BY direction, impl), then 0008e/f/g and the legacy-deletion
slices, then the M0146 continuation (0002f).
Parked: tmp/m0122-alter-system-wip.patch.
