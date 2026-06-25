(idle — nothing in flight)

Last loop (#61) COMPLETE + committed: M0118-0005/0009 `fk-partitioned-1`
**PROMOTED** to pass (design 0118-0120) — the concurrent Class B slice closed.
`DELETE FROM ppk1` issued while `ALTER TABLE pfk ATTACH PARTITION pfk1` is still
UNCOMMITTED now blocks `<waiting ...>` behind the attach and errors only once it
commits. All 18 active perms byte-identical to PG 18.3; strict
`TestPort_IsolationFkPartitioned1`.

Mechanism: deferred ATTACH records its XID in new `catalog.pendingAttachXID`
(child OID→XID, set when parent has FKs via MaterializeWriterXID, cleared on
COMMIT in ApplyPendingPartitionAttaches + ROLLBACK in execRollback). The
referenced-side delete check `enforceFKOnDeletePartitionAncestor` now retries
over `fkDeleteAncestorPass`: a referencing row for the deleted key in a
not-yet-registered partition with an active foreign PendingAttachXID →
WaitForXID + snapshot refresh + retry; the re-run sees the registered partition,
skips the clone, ROOT pfk names the 23503. Models PG's held-to-commit
SELECT FOR KEY SHARE without synthesising a heap lock (goopg cross-stmt blocking
rides WaitForXID).

Files: internal/catalog/catalog.go, internal/executor/{operators_ddl.go,
operators_tx.go, operators_fk.go}, internal/testport/isolation_port_test.go,
docs/test-port CSV+md, docs/design/0118-0120 + README.

NEXT remaining M0118 (all Effort-L unbuilt subsystems): fk-partitioned-2,
index-only-bitmapscan (real Bitmap Heap Scan + BitmapOr / EXPLAIN DECLARE
CURSOR), predicate-gin/gist (int[]/point + GIN/GiST AMs), predicate-hash
(coarse SIREAD over-detects), deadlock-parallel (lock-group), stats (pg_stat_*
cumulative subsystem). `fk-partitioned-2` is the natural next pick — same
partitioned-FK machinery (probe first to scope the divergence).
