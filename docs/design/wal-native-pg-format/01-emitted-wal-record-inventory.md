# 01 — Emitted WAL record inventory

| Field  | Value                                                    |
| ------ | -------------------------------------------------------- |
| Status | draft                                                    |
| Date   | 2026-07-15                                               |
| Scope  | Every WAL record kind goopg **currently emits** (on disk and over the network) |

This document enumerates the records goopg **constructs and appends** — the
*emit set*. Records that goopg can only *decode* (for its own recovery, for
`pg_waldump` parity, or for PG-standby replay) but never writes are listed
separately in the appendix so the boundary of the emit set is unambiguous.

## Architecture: two record families, one append entry point

Every WAL record is written through the single entry point
`wal.Writer.Append(payload []byte)`. The first payload byte selects the family:

- **Native `RecordKind` records** — `payload[0]` is a goopg `RecordKind` byte
  (constants: `internal/wal/recovery.go:21`–`1387`). These are **always
  emitted**, regardless of any knob, and are what goopg's own crash recovery
  replays. On disk, `classifyXLogRecord` (`internal/wal/format.go:217-255`)
  tags every native record `RM_XLOG` with `info = 0xF0` (`xlogInfoDefault`), so
  a stock PG standby safely skips them. The 88-byte checkpoint payload is the
  one native record mapped to a real PG opcode
  (`RM_XLOG` / `XLOG_CHECKPOINT_SHUTDOWN 0x00`).
- **PG-canonical records** — `payload[0] == 0xFE` (`RecordKindCanonical`,
  `recovery.go:1387`), a 7-byte envelope `kind(1)|rmgr(1)|info(1)|xid(4)`
  wrapping a genuine PG `XLogRecord` body (`internal/catalog/canonical.go`).
  `classifyXLogRecord` / `wrapXLogMainData` unwrap it into a real
  `RM_*` / `info` record a PG 18 standby can replay. **Gated by
  `GOOPG_WAL_CANONICAL` (default OFF)**, `internal/initdb/open.go`
  (`emitCanonicalDefault`). When on, canonical records are emitted **alongside**
  the native ones — not instead of them.

The **parity target of this bundle is the native family**: doc 03 specifies the
PG 18.3 layout each native record must adopt. The canonical family is today's
partial, opt-in parity path and doubles as a reference implementation.

---

## Section A — Native records with a PG 18.3 analog (doc-03 targets)

These are the records to be brought to byte-parity. Each is emitted as a native
`RecordKind`; the "PG analog" column is the `RM_*` / `XLOG_*` record it must
become. Emit sites below are the `Log*` closures in `internal/initdb/open.go`
(each calls `walWriter.Append`), plus the checkpointer.

| goopg record (kind #) | native encoder | emit site | PG 18.3 analog (RMGR / opcode) |
| --- | --- | --- | --- |
| `RecordKindPageImage` (1) | `EncodePageImage` | `open.go:460` | `RM_XLOG` / `XLOG_FPI` (full-page image) |
| `RecordKindCheckpoint` (2) | `EncodeCheckpoint` / `EncodeCheckpointCompat` | `checkpointer.go:576` / `:574`, append `:578` | `RM_XLOG` / `XLOG_CHECKPOINT_SHUTDOWN 0x00` (or `_ONLINE 0x10`) |
| `RecordKindBtreeSplit` (3) | `EncodeBtreeSplit` | `open.go:475` | `RM_BTREE` / `XLOG_BTREE_SPLIT_L 0x30` / `_R 0x40` |
| `RecordKindHeapInsert` (4) | `EncodeHeapInsert` | `open.go:489` | `RM_HEAP` / `XLOG_HEAP_INSERT 0x00` |
| `RecordKindBtreeInsert` (5) | `EncodeBtreeInsert` | `open.go:499` | `RM_BTREE` / `XLOG_BTREE_INSERT_LEAF 0x00` |
| `RecordKindHeapDelete` (6) | `EncodeHeapDelete` | `open.go:511` | `RM_HEAP` / `XLOG_HEAP_DELETE 0x10` |
| `RecordKindHeapVacuum` (7) | `EncodeHeapVacuum` | `open.go` (`LogHeapVacuum`) | `RM_HEAP2` / `XLOG_HEAP2_PRUNE_VACUUM_SCAN 0x20` |
| `RecordKindXactCommit` (8) | `EncodeXactCommit` | `open.go:992` | `RM_XACT` / `XLOG_XACT_COMMIT 0x00` |
| `RecordKindXactAbort` (9) | `EncodeXactAbort` | `open.go:995` | `RM_XACT` / `XLOG_XACT_ABORT 0x20` |
| `RecordKindHeapLock` (10) | `EncodeHeapLock` | `open.go:605` | `RM_HEAP` / `XLOG_HEAP_LOCK 0x60` |
| `RecordKindSmgrCreate` (11) | `EncodeSmgrCreate` | `open.go:641` | `RM_SMGR` / `XLOG_SMGR_CREATE 0x10` |
| `RecordKindHeapHotUpdate` (13) | `EncodeHeapHotUpdate` | `open.go:629` | `RM_HEAP` / `XLOG_HEAP_HOT_UPDATE 0x40` |
| `RecordKindHeapPruneOpt` (14) | `EncodeHeapPruneOpt` | `open.go:617` | `RM_HEAP2` / `XLOG_HEAP2_PRUNE_ON_ACCESS 0x10` |
| `RecordKindBtreeVacuum` (22) | `EncodeBtreeVacuum` | `open.go:535` | `RM_BTREE` / `XLOG_BTREE_VACUUM 0xC0` |
| `RecordKindBtreeUnlinkPage` (23) | `EncodeBtreeUnlinkPage` | `open.go` (`LogBtreeUnlinkPage`) | `RM_BTREE` / `XLOG_BTREE_UNLINK_PAGE 0x80` / `_META 0x90` |
| `RecordKindBtreeNewRoot` (24) | `EncodeBtreeNewRoot` | `open.go:567` | `RM_BTREE` / `XLOG_BTREE_NEWROOT 0xA0` |
| `RecordKindBtreeMarkPageHalfDead` (25) | `EncodeBtreeMarkPageHalfDead` | `open.go` (`LogBtreeMarkPageHalfDead`) | `RM_BTREE` / `XLOG_BTREE_MARK_PAGE_HALFDEAD 0xB0` |
| `RecordKindHeapFreeze` (26) | `EncodeHeapFreeze` | `open.go:594` | `RM_HEAP2` / `XLOG_HEAP2_PRUNE_VACUUM_CLEANUP 0x30` (freeze plans) |
| `RecordKindXactCommitInval` (32) | `EncodeXactCommitInval` | `open.go` (`LogXactCommitInval`, see `:1629`) | `RM_XACT` / `XLOG_XACT_COMMIT 0x00` with `XACT_XINFO_HAS_INVALS` |
| `RecordKindClogTruncate` (33) | `EncodeClogTruncate` | `open.go` (`LogClogTruncate`, see `:1645`) | `RM_CLOG` / `CLOG_TRUNCATE 0x10` |
| segment pad | `buildSegmentPadRecord` | `internal/wal/segment_pad.go:55`, emitted from `writer.go` `emitSegmentPad` | `RM_XLOG` / `XLOG_NOOP 0x20` (already a genuine PG record) |

> The `XLOG_PARAMETER_CHANGE` record is emitted in canonical (0xFE) form
> directly — see Section C. It has no separate native `RecordKind`.

---

## Section B — goopg-private catalog-DDL records (NO PG 18.3 WAL analog)

goopg does not keep classic heap-backed `pg_catalog` system tables for most
catalog state, so it journals DDL as **bespoke records**. PostgreSQL instead
WAL-logs catalog changes as ordinary **heap-tuple operations** on `pg_catalog`
relations (`XLOG_HEAP_INSERT` / `_UPDATE` / `_DELETE` on the catalog's
`RelFileLocator`). There is therefore **no 1:1 PG record** for these, and they
are **out of scope for doc 03**. They are catalogued here for completeness.

Constants: `internal/wal/recovery.go`. Emit sites are in
`internal/executor/operators_ddl.go`, `internal/server/*_ddl.go`,
`internal/executor/operators_sequence.go`, `internal/wal/statistics_ddl.go`,
`internal/wal/schema_alter_ddl.go` (all via `WAL.Append(wal.Encode<X>(...))`).

| Domain | Record kinds (# from `recovery.go`) | representative emit site |
| --- | --- | --- |
| Database | CreateDatabase(18), DropDatabase(19), AlterDatabaseSetConfig(73), ResetConfig(74), ResetAllConfig(75) | `internal/server/database_ddl.go:1282` (create), `:1384` (drop) |
| Index | CreateIndex(20), DropIndex(21), RenameIndex(94) | `internal/executor/operators_ddl.go:10110` (create), `:6687` (drop) |
| Schema | CreateSchema(34), DropSchema(35) | `internal/executor/operators_ddl.go:16359` (create) |
| Sequence | SequenceState(65), DropSequence(66) | `internal/executor/operators_sequence.go:462` |
| Role | RoleState(67), DropRole(68), AlterRoleRename(72), AlterRoleSetConfig(76)/ResetConfig(77)/ResetAllConfig(78), GrantRoleMembership(79), RevokeRoleMembership(80) | `internal/server/role_ddl.go:166` |
| Function | CreateFunction(61), DropFunction(62), AlterFunctionRename(63)/Flags(64)/Owner(121)/SetSchema(122)/Config(123) | `internal/executor/operators_ddl.go:11343` |
| Publication / Subscription | CreatePublication(50)…AlterSubscriptionOwner(55) | `internal/executor/operators_ddl.go:920` (create publication) |
| Statistics | CreateStatistics, DropStatistics, AlterStatistics* | `internal/executor/operators_ddl.go:17708` |
| Column defaults | ColumnDefaults(69) | `internal/executor/operators_ddl.go` |
| Type system | CreateRangeType(81)/Drop(82)/Rename(117)/Owner(118), CreateDomain(119)/Drop(120) | `internal/executor/operators_ddl.go` |
| Operator system | CreateOperator(83)/Drop(84), OperatorFamily(85/92), OperatorClass(86/87), AmOp(88/89), AmProc(90/91) | `internal/executor/operators_ddl.go` |
| Access method | CreateAccessMethod(70), DropAccessMethod(71) | `internal/executor/operators_ddl.go` |
| Cast / Conversion / Transform / Collation | Cast(38/39), Conversion(40/41), Transform(36/37), Collation(42–45,93) | `internal/executor/operators_ddl.go` |
| Aggregate | CreateAggregate(46), AlterRename(47), Drop(48), AlterOwner(49) | `internal/executor/operators_ddl.go` |
| Event trigger | CreateEventTrigger(56)…AlterOwner(60) | `internal/executor/operators_ddl.go` |
| Tablespace / Foreign server / User mapping | Tablespace(124/125), ForeignServer(126/127), UserMapping(128/129) | `internal/server/*_ddl.go` |
| Views / MatViews | CreateMatView(102), CreateView(103) | `internal/executor/operators_ddl.go` |
| Text search | TSDict(104/105,114,115,116), TSConfig(106–113) | `internal/wal/schema_alter_ddl.go` |

(Full ordered list is the `RecordKind* byte = N` block in `recovery.go`, values
1–129 with gaps at 95–101.)

---

## Section C — PG-canonical records emitted under `GOOPG_WAL_CANONICAL`

When the knob is on, these real-PG records are emitted **in addition** to the
native ones. Builders are in `internal/catalog/canonical.go`. This is today's
partial parity path and the reference for doc 03. Note that canonical records
are currently **FPI-only** (a single full-page-image block with truncated
main-data) — doc 03 also flags the gap between this and a full PG record.

| PG record (RMGR / opcode) | canonical builder | representative emit site |
| --- | --- | --- |
| `RM_HEAP` / `XLOG_HEAP_INSERT 0x00` | `PgCanonicalHeapInsert` (`canonical.go:82`) | `operators_storage.go:8360`, `operators_ddl.go:13455` |
| `RM_HEAP` / `XLOG_HEAP_DELETE 0x10` | `PgCanonicalHeapDelete` (`canonical.go:234`) | `operators_storage.go:8447` |
| `RM_HEAP` / `XLOG_HEAP_INPLACE 0x70` | `PgCanonicalHeapInplace` (`canonical.go:135`) | `operators_storage.go:8390`, `operators_vacuum_datfrozenxid.go:132` |
| `RM_HEAP2` / `XLOG_HEAP2_PRUNE_ON_ACCESS 0x10` / `_VACUUM_SCAN 0x20` | `PgCanonicalHeapPrune` (`canonical.go:296`) | `operators_storage.go:8419`, `internal/vacuum/vacuum.go:170` |
| `RM_BTREE` / `XLOG_BTREE_INSERT_LEAF 0x00` | `PgCanonicalBtreeInsert` (`canonical.go:179`) | `sys_catalog_index_insert.go:300`, `sys_catalog_btree_split.go:416`, `sys_catalog_btree_multilevel.go:371` |
| `RM_XACT` / `XLOG_XACT_COMMIT 0x00` / `XLOG_XACT_ABORT 0x20` | `PgCanonicalXactCommit` / `…Abort` (`canonical.go:367`/`377`) | `open.go:1020`, `:1029` |
| `RM_XLOG` / `XLOG_PARAMETER_CHANGE 0x60` | `EncodeParameterChange` (`internal/wal/parameter_change.go:100`) | `ReportParameters` (`parameter_change.go:83`) ← `open.go:2236` |
| `RM_XLOG` / `XLOG_CHECKPOINT_SHUTDOWN`/`_ONLINE` | `EncodeCheckpointCompat` | `checkpointer.go:574` (when `PGCompatCheckpoints=true`) |

---

## Section D — pgoutput logical-replication messages (network transfer)

Not WAL-file records but the **network** emit surface — the logical-replication
stream a subscriber consumes. Encoder `internal/wal/pgoutput.go`; this is
**pgoutput protocol version 1** and all multi-byte integers are **big-endian**
(contrast the little-endian on-disk WAL). Message-type bytes at
`pgoutput.go:41-49`; driven by the WAL `Classify` decoder
(`internal/wal/classifier.go:35`).

| Message | byte | producer | notes |
| --- | --- | --- | --- |
| Begin | `'B'` | `pgoutput.go:129` | final_lsn + commit_time + xid |
| Commit | `'C'` | `pgoutput.go:143` | flags + commit_lsn + end_lsn + commit_time |
| Relation | `'R'` | `writeRelation` (`pgoutput.go:190`) | rel_oid, schema, name, replident, columns |
| Insert | `'I'` | `writeInsert` (`pgoutput.go:218`) | rel_oid + `'N'` TupleData |
| Delete | `'D'` | `writeDelete` (`pgoutput.go:232`) | rel_oid + `'O'`/`'K'` TupleData |
| Update | `'U'` | `writeUpdate` (`pgoutput.go:254`) | rel_oid + optional `'O'`/`'K'` + `'N'` TupleData |
| Truncate | `'T'` | (`pgoutput.go`) | nrelids + option_bits + relids |

(`'O'` / `'K'` / `'N'` are old / key / new tuple sub-markers **inside** the
Insert/Update/Delete bodies, not standalone messages.)

**Current limitations vs PG (detailed in doc 03):** no `Type` (`'Y'`),
`Origin` (`'O'` top-level), streaming (`'S'`/`'E'`/`'A'`), or two-phase
messages; replica identity hard-coded `DEFAULT`; `atttypmod` always `-1`;
values always text (`'t'`), never binary.

---

## Appendix — decode-only kinds (defined + decodable, never emitted)

Constants and decoders exist for goopg recovery / `pg_waldump` / standby-replay
parity, but goopg has **zero production emit sites** for these. Included so the
emit-set boundary is explicit.

**Native `RecordKind`s with no non-test caller of their encoder:**
`RecordKindSmgrTruncate` (12), `RecordKindXactAssignment` (15),
`RecordKindXactRollbackTo` (16), `RecordKindXactSubAbort` (17),
`RecordKindHeapUpdate` (27) — goopg emits HeapHotUpdate or Delete+Insert
instead, `RecordKindHeapMultiInsert` (28), `RecordKindHeapVisible` (29),
`RecordKindBtreeReusePage` (30), `RecordKindBtreeMetaCleanup` (31).

**PG RMGR types decoded for parity but never emitted:**
`RM_STANDBY` / `XLOG_RUNNING_XACTS 0x10` (decode/replay only,
`recovery.go:9257`; constant `pg_xlog_decode.go:32`), `XLOG_RELMAP_UPDATE`
(`RelationMapUpdateMap` is a stub returning nil, `canonical.go:225`),
`XLOG_CLOG` (goopg emits native `ClogTruncate` instead). Various
`XLOG_BTREE_*` / `XLOG_HEAP2_*` decode constants in `pg_xlog_decode.go` have no
canonical builder.
