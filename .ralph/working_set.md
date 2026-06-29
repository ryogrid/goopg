(idle — nothing in flight)

Last loop (#27): M0119-0004 **REPLICA IDENTITY USING INDEX** (DU-002 slice 306)
— LANDED. Completes the slice-305 deferral. Design `0119-0004-replica-identity.md`.

pg_dump emits `ALTER TABLE ONLY <t> REPLICA IDENTITY USING INDEX <idx>` at
INDEX-dump time keyed on `pg_index.indisreplident` (pg_dump.c:18186), NOT at
table-dump time. Implementation:
- catalog.go: new `catalog.Index.IsReplicaIdentity bool` field; projected to
  indisreplident in the VIRTUAL pg_index builder (`VirtualRows`, the one pg_dump
  reads) via `boolStr(idx.IsReplicaIdentity)`.
- pg18_user_catalog_rows.go: same projection in the HEAP builder
  `buildUserPGIndexRow` (restart durability / sibling-path law).
- operators_ddl.go `AlterTableReplicaIdentity` case: the `'i'` form is now
  accepted (was 0A000-rejected). `resolveReplicaIdentityIndex` validates per PG
  `check_replica_identity` (exists 42704 / unique 42809 / immediate 0A000 /
  non-expression 0A000 / non-partial 0A000 / NOT-NULL keys 42809). Sets
  tbl.ReplicaIdentity='i'; marks the chosen index + clears every other index of
  the table (relation_mark_replica_identity parity), re-syncing each changed
  index's pg_index heap row via new `resyncIndexReplicaIdentHeap`
  (stamp-old-row + writeHeapRowCanonical, the delete-old pattern from the table
  path).

Gates: TestUserPGIndexRowReplicaIdentity + TestUserPGClassRowReplicaIdentity +
TestParseAlterTableReplicaIdentity PASS; **DU-002 slices 305/306**
(`ri_index`/`ri_uidx`→USING INDEX clause present, byte-identical to real
pg_dump 18.3) PASS via TestPort_PgDumpConnectionSetup; full executor + catalog +
parser suites PASS; build clean; pgbench smoke = pre-commit hook.

NEXT loop — remaining open under M0119-0004:
- pg_dump 002–010 catalog-view parity battery (further DU-002 slices — probe
  TestPort_PgDumpConnectionSetup for the next getter-battery gap).
- extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement; see memory).
Other M0119: M0119-0002 (CLOG store swap Part B — highest blast radius,
dedicated full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).

NOTE (sibling paths): TWO pg_index row builders — VIRTUAL (`catalog.go`
VirtualRows, what pg_dump SELECTs over the wire because `tbl.VirtualRows != nil`
short-circuits the heap scan) and HEAP (`buildUserPGIndexRow`). Both projected
indisreplident from `Index.IsReplicaIdentity`. The TOAST-index synthetic row
(catalog.go ~5197) stays 'f'. `Index.IsReplicaIdentity` is in-memory only (like
`DeclaredHash`) — NOT persisted to the index-DDL WAL record; the heap re-sync
keeps a restart/standby read consistent.
