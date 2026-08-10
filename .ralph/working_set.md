Task: M0130-S11.5d-3a — the primary adopts upstream's retarget-and-delete
parent mutation. COMPLETE, committed, pushed.

Key finding: the rightmost-child refusal RETIRES a mechanism rather than adding
one. `_bt_lock_subtree_parent` refuses to delete a page whose downlink is its
parent's LAST item, and the last item of an internal page IS its rightmost
child — so a downlink removal can no longer take an internal page to zero items
at all. AI-20260706-201855-001's "empty non-root internal page" becomes
structurally unreachable instead of repaired after the fact by
`maybeCascadeEmptyInternal`. The cascade regression test now asserts the
stronger invariant directly (no live internal page anywhere holds zero items).

Landed: `ReplayParentRetargetByChild` + `PGFindDownlinkOffset` +
`ErrParentRightmostChild` (internal/access/btree/replay.go) shared by ALL THREE
former call sites — `applyParentDownlinkRemoval` (WAL path),
`removeDownlinkFromParent` (FPI fallback), and `replayBtreeUnlinkPage`'s parent
limb, which now ignores the record's advisory `ParentRemoveSlot` and locates by
child block. Pre-emit refusal via `parentDownlinkIsRightmostChild` in both
`unlinkEmptyLeaf` and `unlinkEmptyInternalPage` (before ANY mutation, before the
emitter branch) + `abandonHalfDeadLeaf` reverting VACUUM's phase-1 marking.

Guards: internal/access/btree/parent_retarget_test.go (+4, incl. end-to-end
VacuumIndexPages proving the parent's last child is left empty-but-live,
unflagged, still downlinked). Gates: btree/amcheck/wal/storage/initdb PASS;
units PASS; btree -race PASS; pgbench smoke PASS (commit hook).
tpch-spotcheck / TPC-DS SF0.5 NOT run — btree vacuum/replay only, no planner or
codec path.

Ledger rows (2): (1) upstream's OTHER arm of the refusal — the only-child case
where `_bt_pagedel` deletes the whole subtree upward, one record per level;
goopg abandons instead, so those pages are never reclaimed and the multi-level
deletion blocks 3/4 exist for stays unreachable. (2) an OLD native unlink record
replayed by this binary yields a different (still searchable) parent page.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5d-3b — rewire the emit PROTOCOL** (pin left/target/right,
   compute, emit, write; then emit the two PG records in place of the native
   one, and write the dummy top-parent high key when marking a leaf half-dead),
   then S11.5d-3c (safexid recycle horizon), S11.5b-2, then S11.6.

In-flight: none.
