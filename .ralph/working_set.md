(idle — nothing in flight)

Last loop (#58) COMPLETE + committed: M0118-0009 `intra-grant-inplace` PROMOTED
(design 0118-0117). All 10 permutations byte-identical to PG 18.3;
`TestPort_IsolationIntraGrantInplace` strict. NOT an enabler — full promotion.

Perm 10 (`b1 drop1 b3 sfu3 revoke4 c1 r3`); `drop1` = locally-adapted
`DELETE FROM pg_class WHERE relname=…` (virtual-catalog tuple delete). Six pieces:
1. catalog `tablePendingDropXID` store (Set/Get/Clear) = pg_class delete xmax.
2. `deleteOp.tryPgClassCatalogDelete`/`pgClassDeleteTargetOID`
   (operators_storage.go): DELETE FROM pg_class in a txn → records delete xmax +
   defers removal to COMMIT via AddPendingTableDrop.
3. `maybeRecordPgClassRowMark` (operators_lockrows.go): also `waitTablePendingDrop`
   (shared core `waitPgClassTupleXID`); retracts rowmark on 0-row drain
   (ClearPgClassRowMark).
4. `waitForPgClassRowMarks` → `waitPgClassRowMarkReleased` (operators_ddl.go):
   POLLS mark presence (not WaitForXID) + keeps WFG deadlock check, so revoke4
   unblocks at sfu3's release (before r3).
5. REVOKE re-checks LookupTableByOID post-wait → XX000 cache lookup failed.
6. plpgsql EXCEPTION fixes (LATENT BUGS, broad reach): parseTopBlock/
   parseNestedBlock now set ExceptionBlock.TryBody (handlers were DEAD);
   handler binds SQLERRM/SQLSTATE (setPlpgsqlFrameVar); RAISE WARNING →
   AddWarning (WARNING severity).

Remaining M0118-0009: `stats` (pg_stat_* cumulative subsystem, Effort-L) — only
remaining intra-grant work is done. Other failing M0118 specs (distinct unbuilt
subsystems): index-only-bitmapscan (EXPLAIN DECLARE CURSOR + BitmapOr),
predicate-gin/gist (GIN/GiST AMs), deadlock-parallel (lock-group),
fk-partitioned-1/2 (ATTACH PARTITION + partitioned-FK).
