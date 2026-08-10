Task: M0130-S11.5d-2 — `XLOG_BTREE_UNLINK_PAGE` content parity.
COMPLETE, committed, pushed.

Key finding: the two page-deletion records COMPOSE through the half-dead leaf's
dummy high key — phase 2 reads `leaftopparent` out of it and rebuilds it (block
3, WILL_INIT, via the same `ReplayMarkHalfDeadLeaf` phase 1's block 0 uses), so
a subtree of arbitrary depth is unlinked one record per level. A deleted page is
defined by its contents exactly as a half-dead one is: no line pointers,
pd_lower covering one `BTDeletedPageData`, pd_upper closed against pd_special.
`BTP_HAS_FULLXID` is why the sibling link fixes needed new PG-level helpers —
the legacy `BT*` flag round-trip has no counterpart for that bit and drops it,
which would turn the safexid into garbage for `BTPageIsRecyclable`.

Landed: `EncodeBtreeUnlinkPagePG` + `BtreeUnlinkPagePGRequest` (36-byte main
data; size is offsetof-derived, NOT sizeof=40; both alignment holes are wire
format; every structural field read off a page; refuses a rightmost target
because redo reads block 2 unconditionally), `xlogBtreeUnlinkPageMeta` 0x90,
new `internal/access/btree/pgpagedel.go` (`ReplayUnlinkTargetPage`,
`PGDeletedPageSafeXid`, `ReplayUnlinkLeftSibling`, `ReplayUnlinkRightSibling`),
`replayDecodedXLogBtreeUnlinkPage` + dispatch arm (blocks 1/0/2/3/4, upstream's
lock order, per-limb pd_lsn, FPI fallback).

Guards: internal/wal/btree_unlinkpage_pg_test.go (+4),
internal/access/btree/replay_pagedel_test.go (+3). Gates: wal/btree/storage/
amcheck/initdb PASS; units PASS; wal+btree -race PASS; pgbench smoke PASS
(commit hook). tpch-spotcheck / TPC-DS SF0.5 NOT run — replay-only, no emit
site, no on-disk shape change.

Ledger row: goopg has NO safexid concept at all — its deletion stamps BTDeleted
and recycles without upstream's `BTPageIsRecyclable` XID horizon, so blocks 3/4
and the BTDeletedPageData have encode+replay coverage but no runtime producer.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5d-3 — rewire the page-deletion emit sites** (upstream's
   pin-left/target/right protocol, the retarget-and-delete parent mutation, the
   dummy top-parent high key, and the safexid horizon — see both ledger rows),
   then S11.5b-2, then S11.6 (flip relhasindex).

In-flight: none.
