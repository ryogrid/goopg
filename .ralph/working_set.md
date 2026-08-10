Task: M0130-S11.5c — `XLOG_BTREE_VACUUM` (RM_BTREE content parity).
COMPLETE, committed, pushed.

Key finding: this was the one FPI-only btree record that was NOT unreplayable
by real PG — `btree_xlog_vacuum` dereferences `xlrec` only inside its
BLK_NEEDS_REDO arm, which an applied image skips. It still lied to every
reader that does not replay it (pg_waldump's btree_desc prints
ndeleted/nupdated off the end of a zero-length main-data area). Both forms
now carry the struct.

Landed: `EncodeBtreeVacuumPG(rel, blk, prePage, page, deleted)` → main data
`xl_btree_vacuum{ndeleted, nupdated}` + block-0 deleted offset array (no
image), or `{0,0}` + apply-image. The FORM IS DECIDED BY ASKING THE TWO PAGES:
`btree.CheckVacuumDelete` replays the offsets against the pre-vacuum page and
compares items/high key/opaque with what VACUUM wrote; mismatch ⇒ image. That
covers, without enumerating them at the emit site, the three real divergences
(posting lists — goopg expands per TID and re-marshals survivors separately,
upstream's `xl_btree_update`/nupdated half; the page that went empty — VACUUM
also stamps BTDeleted|BTHalfDead; the dedup-recovery CONSOLIDATION reuse).
New `internal/access/btree/pgvacuum.go` (ReplayVacuumDelete = upstream
PageIndexMultiDelete + garbage-hint clear; CheckVacuumDelete). Replay
`replayDecodedXLogBtreeVacuum` refuses nupdated > 0 by name. Hook signature
`LogBtreeVacuumFunc` grew prePage + deleted (initdb/open.go, both btree call
sites, 3 test files).

Guards: internal/wal/btree_vacuum_pg_test.go,
internal/access/btree/pgvacuum_test.go, and btree_vacuum_wal_test.go's capture
hook now runs CheckVacuumDelete on the offsets VacuumIndexPages computes and
fails if NO emission named any. Gates: btree/wal/storage/amcheck/initdb PASS;
units PASS; pgbench smoke PASS (commit hook). tpch-spotcheck / TPC-DS SF0.5
NOT run — no on-disk shape change, no REINDEX debt.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5d — `XLOG_BTREE_UNLINK_PAGE`**, the last record in S11.5 and
   the one with a documented reason to stay native (an emit-time FPI can be
   stale against a concurrent split on another *BTree — the PG-faithful form
   needs incremental link patches). Then S11.5b-2 / S11.5b-3, then S11.6
   (flip relhasindex).

In-flight: none.
