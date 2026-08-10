Task: M0130-S11.5d-3b-2 — swap in the two PG page-deletion records. COMPLETE, committed.

Landed: `unlinkEmptyLeaf` emits upstream's PAIR inside the S11.5d-3b latch
section — `EncodeBtreeMarkPageHalfDeadPG` then `EncodeBtreeUnlinkPagePG` — and
the native `RecordKindBtreeUnlinkPage`/`…MarkPageHalfDead` PRODUCERS are retired
(decode+replay stay, for old WAL). The primary applies every mutation through
the redo functions themselves (ReplayMarkHalfDeadLeaf incl. the dummy top-parent
high key, ReplayParentRetargetByChild, ReplayUnlinkTargetPage,
ReplayUnlink{Left,Right}Sibling) — required because both records are WILL_INIT
with NO block data: the standby rebuilds the pages from 20/36 bytes of main data
alone. One indirection: the phase-2 encoder reads the links off the target PAGE,
but goopg relinks the nearest LIVE siblings, so the emit site builds the
POST-mutation image with ReplayUnlinkTargetPage, encodes from it, then copies
those bytes onto the pinned page. Three refusals added (no parent downlink;
rightmost target; standalone internal page — its cascade was already unreachable
after S11.5d-3a). storage request types now carry PAGES; ParentRemoveSlot gone.

Files: internal/access/btree/btree_vacuum.go, internal/storage/bufpool.go,
internal/initdb/open.go, internal/wal/btree_pagedel_producer_test.go (NEW),
internal/access/btree/{unlink_protocol_test.go,btree_vacuum_wal_test.go},
docs/design/0130-0012-rm-btree-wal-content-parity.md + README.md +
wal-pg-identical-stream/IMPLEMENTATION-TODO.md (A8-unlinkpage → [x]).

Gates: btree/wal/storage/initdb/amcheck/vacuum PASS; btree -race PASS; units
suite PASS (40 ok, 0 FAIL); pgbench smoke PASS (commit hook). tpch-spotcheck /
TPC-DS SF0.5 NOT run — btree vacuum/replay only, no planner or codec path.

Ledger rows (2): (1) a deletion is now TWO records, so a crash between them
leaves a half-dead leaf with no parent downlink — upstream resumes it via
`_bt_pagedel`, goopg's CompleteDeferredDeletions re-enters unlinkEmptyLeaf which
now refuses (leaks a block, does not corrupt). (2) no multi-level subtree
deletion.

Env note: `./postgres` (the 18.3 oracle) is a REAL directory again, not the old
`../postgres` symlink; it briefly looked missing mid-loop during someone's git
operation on it. Do not re-create a symlink over it.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5d-3c — the safexid recycle horizon** (stamp a real safexid via
   ReplayUnlinkTargetPage, gate recycleBlock on BTPageIsRecyclable), then
   S11.5b-2, then S11.6.

In-flight: none.
