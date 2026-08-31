# 0079-0001 — Index DDL WAL recovery

| field | value |
| --- | --- |
| status | accepted |
| date | 2026-05-11 |
| scope | wal, catalog, executor, storage, initdb |
| related | 0030-0002 (DDL WAL records), 0030-0003 (heap-driven catalog
load), 0017 (data directory), 0002-0003 (redo records) |

## 1. Problem statement

A pgbench measurement (`bench/pgbench-compare/run_comparison.sh`,
SF=100, 100 clients, 100 threads, 180 s, standard TPC-B-like) on the
M0077-final binary produced **0.86 TPS** — a ~70× regression from the
M0076-final-era baseline of ~60 TPS on the same parameters. Goroutine
profiles taken mid-run showed every connection blocked inside
`internal/storage.Pool.Pin / Unpin / Prefetch` while the planner-side
`EXPLAIN SELECT abalance FROM pgbench_accounts WHERE aid = 1` was
returning a **Seq Scan on a 10M-row table**.

Root cause: the on-disk catalog snapshot
(`<DataDir>/global/pg_catalog.json`) had `tables: 4, indexes: 0` —
even though the index relfiles (16405 / 16406) for
`pgbench_accounts_pkey` and friends were on disk. A fresh
`pgbench -i` against goopg DOES create the indexes correctly at
runtime; they are visible to the planner during the same session. But
they disappear from the catalog after a non-graceful restart because:

1. `Runtime.SaveCatalog()` (the JSON snapshot writer) is called from a
   single `defer` in `cmd/goopg/main.go:331`. SIGKILL, OOM, or panic
   bypasses it.
2. `loadUserTablesFromHeap` in `internal/initdb/open.go:1242`
   reconstructs user TABLES from the `pg_class` heap relation on
   restart — but **only entries with `relkind == "r"`**. Indexes
   (`relkind == "i"`) are skipped.
3. `syncIndexToCatalogHeap` in `internal/executor/operators_ddl.go`
   writes only a `pg_class` row for the index. There is no
   `pg_index` heap relation in goopg, so the column list, unique
   flag, primary flag, and owning-table OID would be lost even if a
   reader scanned `pg_class` for `relkind='i'`.

The btree pages and the index relfile itself are restored correctly
by physical WAL replay (`RecordKindHeapInsert`,
`RecordKindBtreeInsert`, `RecordKindBtreeSplit`,
`RecordKindSmgrCreate`); the gap is purely the in-memory catalog
state that the planner consults.

The pre-fix behaviour was therefore: post-restart, the planner has no
record of `pgbench_accounts_pkey`, EVERY `WHERE aid = :aid` falls
back to a Seq Scan on the 10M-row heap, and 100 concurrent Seq Scans
serialise on the buffer-pool mutex.

## 2. What WAL already covers (record-level recovery)

Before getting to the fix, this section pins what is **already
correct** — the user's broader concern was whether the heap-side
record updates that maintain index entries are also crash-recoverable.
Audit summary:

| Operation | WAL record | Replay path | Status |
| --------- | ---------- | ----------- | ------ |
| Heap INSERT | `RecordKindHeapInsert` (4) | `replayHeapInsert` | ✓ logical, idempotent via `pd_lsn` |
| Heap DELETE / UPDATE old-image stamp | `RecordKindHeapDelete` (6) | `replayHeapDelete` | ✓ logical, idempotent |
| Heap HOT update | `RecordKindHeapHotUpdate` (13) | `replayHeapHotUpdate` | ✓ atomic old-stamp + new-insert |
| Heap row lock | `RecordKindHeapLock` (10) | `replayHeapLock` | ✓ idempotent |
| Heap VACUUM (page prune) | `RecordKindHeapVacuum` (7) | `replayHeapVacuum` | ✓ slot-list logical |
| Heap opportunistic prune | `RecordKindHeapPruneOpt` (14) | `replayHeapPruneOpt` | ✓ same shape as vacuum |
| Btree non-split insert | `RecordKindBtreeInsert` (5) | `replayBtreeInsert` | ✓ logical, idempotent via `pd_lsn` |
| Btree split | `RecordKindBtreeSplit` (3) | `replayBtreeSplit` | ✓ atomic left + right |
| Btree VACUUM (item removal + leaf unlink) | full-page image | `replayPageImage` | ✓ via `markDirtyWithPageRecord` |
| Smgr file creation | `RecordKindSmgrCreate` (11) | `replaySmgrCreate` | ✓ recreates relfile |
| Smgr truncate | `RecordKindSmgrTruncate` (12) | `replaySmgrTruncate` | ✓ |

So heap modifications that are followed by btree updates (INSERT,
UPDATE-with-indexed-column, DELETE-then-VACUUM) ARE fully recovered
at the record level. The pre-existing tests
(`internal/wal/recovery_test.go`,
`internal/access/btree/btree_test.go`) lock those paths.

The remaining gap is purely the **catalog metadata** that lets the
planner discover the index in the first place — that is what
M0079-0001 fixes.

### Optimization gaps (not bugs)

Two places fall back to FPI rather than logical records, which is
correct (FPI is always replayable) but verbose:

- Btree single-item delete (e.g., DROP INDEX entry replaced by
  re-CREATE). Currently bundled into the FPI emitted by VACUUM.
- Btree page unlink during VACUUM phase 2. FPI'd via
  `markDirtyWithPageRecord` — correct, but a logical
  `RecordKindBtreeUnlink` would reduce WAL volume.

Neither is a recovery hole; they are M0080+ optimisations.

## 3. Fix shape: a CreateDatabase-style catalog WAL record

PostgreSQL solves the same class of problem (DDL → catalog state →
recovery) by writing pg_class / pg_index / pg_attribute heap rows AND
having a comprehensive recovery scan over those heap relations.
goopg has the heap-driven recovery for tables (`pg_class` +
`pg_attribute`) but not for indexes — and adding a `pg_index` heap
relation today would require:

- a system OID assignment,
- column schema for it,
- migration logic for clusters that don't already have the relfile,
- changes to `loadUserTablesFromHeap` to also do an index-side scan.

That work is in scope for a future milestone. As an immediate fix
that unblocks pgbench (and any other workload that relies on indexes
surviving a crash), this design uses the same pattern goopg already
uses for `CREATE / DROP DATABASE`: a custom WAL record carrying the
full catalog metadata, replayed into the in-memory catalog after
physical recovery finishes.

The `RecordKindCreateDatabase` (18) / `RecordKindDropDatabase` (19)
records were introduced for `CREATE DATABASE` (M0054-0001) precisely
because v0 has no per-database file namespace — there is no on-disk
representation to recover from. This same shape applies to indexes
(no pg_index relation), so the fix mirrors that template exactly.

## 4. Implementation

### 4.1 New WAL records (`internal/wal/recovery.go`)

```go
RecordKindCreateIndex byte = 20
RecordKindDropIndex   byte = 21

type CreateIndexPayload struct {
    OID      uint32
    TableOID uint32
    Schema   string
    Name     string
    Method   string
    Columns  []string
    Unique   bool
    Primary  bool
}
type DropIndexPayload struct {
    OID    uint32
    Schema string
    Name   string
}
```

Encoding format (CREATE):
```
kind(1) | oid(4) | tableOid(4) | unique(1) | primary(1) |
schemaLen(2) | nameLen(2) | methodLen(2) | numCols(2) |
schema | name | method | colName0Len(2) | colName0 | ... |
colNameKLen(2) | colNameK
```

Round-trip + truncated-payload + wrong-kind tests are pinned by
`internal/wal/index_ddl_test.go`.

### 4.2 Physical replay is a no-op

```go
case RecordKindCreateIndex, RecordKindDropIndex:
    // Catalog-only record. Heap pages and btree pages already
    // restored by other record kinds; the catalog-side replay
    // happens in internal/initdb after physical recovery.
    return false, nil
```

Same shape as `RecordKindCreateDatabase`. Test pinned by
`TestApplyRecordSkipsCreateAndDropIndex`.

### 4.3 Catalog-side recovery hooks (`internal/catalog/catalog.go`)

```go
func (c *InMemory) RegisterIndexDuringRecovery(
    schema, name string, tableOID uint32,
    cols []string, unique bool, method string, primary bool, oid uint32,
)
func (c *InMemory) UnregisterIndexDuringRecovery(schema, name string)
```

Differs from `CreateIndex` in three ways:

1. The OID comes from the WAL record, not `nextOID++` — the
   recovered catalog entry must map to the same on-disk relfile that
   physical replay just restored.
2. Idempotent: re-applying a record whose Index already exists is a
   no-op (a JSON snapshot may have captured it on a previous
   graceful shutdown).
3. `nextOID` is advanced past the recovered OID so subsequent
   allocations don't collide.

Pattern lifted from `RegisterDatabaseDuringRecovery` /
`UnregisterDatabaseDuringRecovery` (M0054-0001).

### 4.4 Recovery driver (`internal/initdb/index_ddl_recovery.go`)

```go
func replayIndexDDLRecords(walDir string, cat catalog.Catalog) error
```

Walks the WAL once after physical recovery, decodes every
`RecordKindCreateIndex` / `RecordKindDropIndex`, and applies it via
the catalog hook. Mirrors `replayDatabaseDDLRecords`. Order matters:
a CREATE followed by a DROP for the same name must not resurrect the
index, so the driver walks records in stream order.

Wired in `internal/initdb/open.go` immediately after
`loadUserTablesFromHeap` so the owning table is always present when
the index is registered.

### 4.5 Generic change-record hook (`internal/storage/bufpool.go`)

The executor's CREATE INDEX / DROP INDEX paths need to emit WAL
records but the `executor` package does not currently import `wal`
directly. The existing pattern threads such hooks through
`storage.PoolConfig` (e.g. `LogHeapInsert`, `LogSmgrCreate`); this
design adds:

```go
type PoolConfig struct {
    // ...
    LogChangeRecord func(payload []byte) (LSN, error)
}

func (p *Pool) LogChangeRecord(payload []byte) (LSN, error)
```

The hook is wired in `internal/initdb/open.go` to call
`walWriter.Append(payload)`.

### 4.6 Executor emit sites (`internal/executor/operators_ddl.go`)

`createBTreeIndex` (success path) emits
`wal.EncodeCreateIndex(wal.CreateIndexPayload{...})` after the
catalog mutation and the existing `syncIndexToCatalogHeap`. On WAL
emit failure, the catalog mutation and the on-disk relfile are both
rolled back so memory and durable state agree.

`execDropIndex` emits `wal.EncodeDropIndex(...)` after the catalog
mutation. The catalog mutation is irreversible at that point;
emit failures degrade to a logged error rather than re-creating the
index.

## 5. Migration / compatibility

- **Old clusters** without the new WAL records: `replayIndexDDLRecords`
  walks the WAL and finds no matching kinds; recovery proceeds
  unchanged. Tables continue to load via `loadUserTablesFromHeap`;
  indexes that were captured by a graceful `SaveCatalog` continue to
  load via `loadCatalogSnapshot`. The fix is additive.
- **New clusters** with the new WAL records but a missing JSON
  snapshot: `loadCatalogSnapshot` is a no-op, `loadUserTablesFromHeap`
  recovers tables, `replayIndexDDLRecords` recovers indexes. This is
  the pgbench-after-crash scenario the fix targets.
- **Crash mid-CREATE-INDEX**: the WAL record is appended AFTER the
  catalog mutation and the heap sync. If the crash precedes the WAL
  flush, recovery proceeds without the index entry — same as the
  pre-fix behaviour but with the in-memory mutation reverted (no
  half-state). If the crash happens after the WAL flush, recovery
  finds the record and re-registers the index; the relfile and btree
  pages have been restored by the existing record kinds.

## 6. Tests

| File | What it pins |
| ---- | ------------ |
| `internal/wal/index_ddl_test.go` | encode/decode round-trip, truncation rejection, kind-byte guard, ApplyRecord no-op |
| `internal/initdb/index_ddl_recovery_test.go` | `TestCreateIndexSurvivesRestartViaWAL`, `TestDropIndexSurvivesRestartViaWAL`, `TestCreateIndexRecoveredOIDDoesNotCollide` |

The recovery tests delete the JSON snapshot before reopening — the
unhappy-path scenario the fix targets — and assert the index is back
with the correct metadata, that DROP suppresses the resurrection,
and that the recovered OID does not collide with subsequent
allocations.

## 7. Acceptance

This slice is acceptable when:

1. `go test ./internal/wal/... ./internal/catalog/... ./internal/initdb/...
   ./internal/executor/... ./internal/storage/...` all green.
2. After a non-graceful close (no `SaveCatalog`), reopening the
   cluster reconstructs every index from WAL with the correct OID,
   table linkage, column list, unique flag, primary flag, and method.
3. Re-running `bench/pgbench-compare/run_comparison.sh` against a
   freshly-`pgbench -i`-initialised cluster produces TPS in the same
   range as the M0076-era baseline (~60 TPS) instead of the 0.86 TPS
   surfacing case.

## 8. Out of scope

- A real `pg_index` heap relation. Building one is a logical next
  step that aligns goopg more closely with PostgreSQL's catalog and
  enables `pg_indexes` / `pg_index` SELECTs from user code without a
  virtual-view detour. It also collapses the WAL-record payload into
  the existing `pg_class` / `pg_index` heap recovery scan instead of
  a separate sidecar. Tracked for M0080.
- A logical `RecordKindBtreeDelete` / `RecordKindBtreeUnlink`. The
  current FPI fallback is correct; these are WAL-volume
  optimisations.
- Periodic `SaveCatalog` writes (e.g., once per checkpoint). The WAL
  records make periodic snapshots unnecessary for correctness, but a
  cooperative snapshot per checkpoint would still cap the WAL replay
  time on restart. M0080 candidate.
