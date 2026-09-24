(idle — nothing in flight)

This loop: M0145-0008b recon closed (no code). The owner answer 281924499
restored M0145-0001 to [x] and ordered 0008b → 0008c → 0008d.
- Q18 (9.9 s legacy → 19.9 s jointree): unnarrowed join tuples on the jointree
  lowering (M0145-0008e) and a HashAggregate 3-4x slower than PG under PG's own
  election (M0145-0008f).
- Q22 (0.51 s → 1.38 s): the expression IN-list estimate (1/3 vs PG 0.035)
  prices out PG's NL anti (M0145-0008g).
Evidence: analysis/m0145/m0145-0008b/.

Next per the banner (item 3, owner order): M0145-0008c (recon: PG-shared
attribution for the flip's new ea-ratchet Q95 key), then 0008d; after that
the new 0008e/f/g and the legacy-deletion slices, then the M0146 continuation
(0002f next there).
Lineage: the owner's bounded exemption lets the loop self-append completed
M0145-0001 descendants to the root LINEAGE-BASELINE line while the deletion
slices are open. Not needed yet.
Parked: tmp/m0122-alter-system-wip.patch.
