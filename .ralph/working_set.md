(idle — nothing in flight)

M0129-S2 COMPLETE (pending commit): `deleteWithUsing` EPQ retry loop
(`operators_storage.go:6447`). Four fixes landed:
(a) Full EPQ retry loop replacing the silent-skip `isConcurrentlyUpdated`
    block — mirrors `deleteOp.Next` stamp-phase EPQ (epqWait →
    epqFollowHOT → epqFollowChain → re-evaluate → retry stamp);
(b) `epqFollowHOT`/`epqFollowChain` pass `nil` pred (matching
    `updateWithFrom` pattern) — the USING predicate is re-evaluated
    separately on the combined row;
(c) `usingPortion` now ALWAYS captured (not gated on RETURNING) so
    EPQ re-evaluation has the USING-table columns;
(d) `epqRetryLimit`/40001, deadlock, moved-partition, RR/SSI
    `epqXmaxSettled`, and TM_SelfModified arms all wired.

Verified: concurrent UPDATE → DELETE...USING blocks, follows ctid chain,
deletes updated version (DELETE 2, 0 rows remain). Isolation spec
(`postgres/src/test/isolation/specs/delete-using-epq.spec`) created
but expected output not yet generated (needs PG reference run).

Gates: UNITS PASS, SPOT PASS (Q12=2/Q13=35).

Next step: M0129-S3 (sort-spill ctid) or M0129-S6 (resjunk-ctid column
path) — S6 subsumes S3's defect class per plan doc §4.
In-flight: none
