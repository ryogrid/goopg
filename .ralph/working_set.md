Task: M0130-S11.5d-3b — the page-deletion emit PROTOCOL. COMPLETE, committed.

Key finding: the pre-3b code was not merely "less faithful" — the primary
deliberately wrote something OTHER than what it logged. It walked the sibling
chain unlatched, emitted the record with that walk, then re-derived each link
again under the write pin (AI-20260709-010336-082: a split on another
connection's *BTree splices a live page in, and stamping the stale walk stomped
that split's relink). Write right, record wrong — and `btree_xlog_unlink_page`
rebuilds the siblings FROM the record with nothing to re-derive from.

Landed: `acquireUnlinkPins` + `unlinkPins` + `liveSiblingLinked` +
`errUnlinkChainUnstable` (internal/access/btree/btree_vacuum.go). Latch order
parent → left → target → right: left→right is `_bt_split`'s own direction, and
the parent FIRST is goopg-specific — since S11.5b-3 an internal split pins a
lower-level page while holding this level's latches, so goopg latches top-down
where upstream latches bottom-up. Re-derivation replaced by VALIDATION under the
latches (retry the walk if a link moved) with a bounded give-up that abandons
the deletion, plus an outright refusal for a dead run looping back onto the
target (double-latch = self-deadlock). Rightmost-child refusal now reads the
LATCHED parent. Dead helpers deleted: applyOpaqueMutation,
applyParentDownlinkRemoval, readLeafFlagsAfterUnlink,
readInternalFlagsAfterUnlink (race test retargeted to
removeDownlinkFromParent). New `storage.Slot.TryRLock`.

Guards: internal/access/btree/unlink_protocol_test.go (+2) — latch-held-at-emit
asserted from INSIDE the emitter hook (where a PG encoder will read its page
images), plus record-equals-page for every logged link/flag; and the cyclic
refusal. Gates: btree/wal/storage/initdb/amcheck PASS; btree -race PASS; units
PASS; pgbench smoke PASS (commit hook). tpch-spotcheck / TPC-DS SF0.5 NOT run —
btree vacuum/replay only, no planner or codec path.

Ledger rows (2): (1) the FPI fallbacks keep the pre-3b one-page-at-a-time shape;
upstream has one protocol regardless of logging path. (2) on give-up goopg
un-marks the leaf where upstream leaves it half-dead for a later `_bt_pagedel` —
goopg cannot, having no top-parent tuple to resume from.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5d-3b-2 — swap in the two PG records** (mark-halfdead +
   unlink-page, dummy top-parent high key, drop `ParentRemoveSlot`), then
   S11.5d-3c (safexid recycle horizon), S11.5b-2, then S11.6.

In-flight: none.
