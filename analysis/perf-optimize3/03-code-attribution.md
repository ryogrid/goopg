# perf-optimize3 — 03: Code attribution (goopg ↔ PostgreSQL)

Each measured mechanism, tied to the exact code on both sides. goopg paths are
at commit `e453e3f2`; PG paths are `postgres/src/backend/...` in the vendored
18.3 tree.

## M1 — FPI-per-record canonical WAL

**goopg**

- `internal/catalog/canonical.go` — `buildCanonicalSingleFPIBody(rel, blk,
  page, mainData)` (canonical.go:460) builds the record body around a full
  8 KB page image; used by `BuildCanonicalHeapInsertPayload` (comment:
  *"flags=0: XLH_INSERT_CONTAINS_NEW_TUPLE not needed when FPI restores the
  page"*), `BuildCanonicalHeapInplacePayload`, and the delete/prune builders
  (canonical.go:116/165/278/335). The btree-insert builder (:212) exists but
  serves only **system-catalog** index inserts
  (`sys_catalog_index_insert.go`), not the user-index hot path.
- `internal/executor/operators_storage.go` — `emitCanonicalHeapHotUpdate`
  pins the page, makes an **8 KB copy** (`make` + `copy`), and calls
  `PgCanonicalHeapInplace` → one FPI per HOT update.
  `emitCanonicalHeapInsert` / `emitCanonicalHeapPruneLocked` do the same for
  inserts and prunes — **unconditionally** (gated only on
  `ctx.LogCanonical != nil`). Each such heap write is additionally logged as a
  **native logical record** via `MarkDirtyChangeRecord` (FPI-gated), i.e. the
  hot path double-logs every heap modification (operators_storage.go
  ~3400/3404). Profile: these assemblies are ~75 % of `memmove`, which is
  11.3 % of total `-N` CPU.
- The once-per-checkpoint FPI machinery exists and the **user-index btree
  leaf insert uses it**: `tryInsertNoSplit` → `Pool.LogBtreeInsert`
  (`EncodeBtreeInsert`, item bytes only) under
  `MarkDirtyChangeRecord`/`fpiSinceCheckpoint`
  (`internal/storage/bufpool.go:1820-1864`). Btree **splits**, however, log
  left+right+sibling full page images (`EncodeBtreeSplit`,
  `internal/initdb/open.go:435`). The canonical **heap** emitters above never
  consult this machinery.

**PostgreSQL**

- `access/transam/xloginsert.c` — `XLogRecordAssemble`: a backup block is
  attached only if `page_lsn <= RedoRecPtr` (first modification since the last
  checkpoint) or `forcePageWrites`; otherwise the record carries just its main
  data.
- `access/heap/heapam.c` — `log_heap_update` builds `xl_heap_update` with
  prefix/suffix suppression against the old tuple (tens of bytes for a
  same-page `abalance` change); `heap_insert`'s `xl_heap_insert` is similarly
  compact.
- With `checkpoint_timeout=24h`, `pg_stat_wal` on the measured run shows FPIs
  amortize to near zero per txn; measured 1,801–2,853 B/txn total.

## M2 — commit-path CLOG durability

**goopg**

- `internal/mvcc/clog_groupcommit.go` — `applyGroupBatchLocked` (the CLOG
  group-commit leader) performs the batch's status writes, then an *eager
  durable write-back*.
- `internal/mvcc/clog_bufferpool.go` — `flushDirty`: collects dirty pages by
  segment, runs the async-commit WAL barrier (`flushWALBeforeWriteLocked`),
  writes pages, and **fsyncs each touched pg_xact segment** — on the commit
  path. (`writePageToDisk` likewise fsyncs on eviction.) Measured: 6,734
  fsyncs / 60 s, avg 6.29 ms.
- The per-XID direct-file path with fsync (`clog.go`
  `mirrorToSLRUUnlocked`) survives only for a sibling-path equivalence test,
  not the runtime path.

**PostgreSQL**

- `access/transam/clog.c` — `TransactionIdSetPageStatus` sets bits in the
  SLRU shared buffer under `XactSLRULock`; **no I/O**.
- `access/transam/slru.c` — `SlruPhysicalWritePage` (with its own
  XLogFlush-before-write barrier) runs at **checkpoint or page eviction**, not
  at commit. Crash recovery re-derives CLOG from WAL commit records.
- Commit durability = `RecordTransactionCommit` (`access/transam/xact.c`) →
  `XLogFlush(recptr)` — one durable write, the WAL itself.

## M3 — B-tree dead entries never reclaimed on access

**goopg**

- `internal/access/btree/btree.go` — `Insert` → `tryInsertNoSplit` →
  (`errNeedsSplit`) → `insertIntoBlock` → split via `pinNewOrRecycled` (one
  fresh/recycled block per split). There is **no LP_DEAD bit, no
  kill-prior-tuple, no pre-split dead-entry purge** anywhere in the package
  (`grep -i 'lp_dead|kill|simple.?deletion'` → empty); `dedupConsolidate`
  (posting.go) merges duplicate keys into posting lists but only sees the
  items handed to a rewrite — committed-dead TIDs are indistinguishable from
  live ones without a visibility check, which the btree layer never does.
  Cleanup happens only in `btree_vacuum.go` (VACUUM).
- `internal/executor/operators_storage.go` — non-HOT updates call
  `maintainUniqueIndexesForInsert` → `BTree.Insert` with the new TID; the old
  TID's entry stays.

**PostgreSQL**

- `access/nbtree/nbtutils.c` — `_bt_killitems`: scans mark index entries
  whose heap tuples were dead LP_DEAD (`kill_prior_tuple`).
- `access/nbtree/nbtinsert.c` — `_bt_delete_or_dedup_one_page` →
  `_bt_simpledel_pass`: before splitting, purge LP_DEAD items (and
  heap-hinted candidates); only if nothing frees enough space does it dedup,
  and only then split. Result: pgbench pkey size stays constant.

## M4 — per-query engine tax (read path)

**goopg** (`-S` profile, CPU-bound at 10.8 cores)

- `internal/server/` `dispatchSimpleQueryViaExecutor` → `executeOneSimpleStmt`
  (24.9 % cum): parse + plan + **operator tree construction per query**
  (`executor.OpIterator.Open`/`opOpen` 16.5 %).
- `internal/protocol/` `FrameWriter.Flush` / `WriteReadyForQuery` ~18 % —
  a `write(2)` per protocol message boundary; socket syscalls 17 %.

**PostgreSQL** — simple protocol also re-parses per query, but its per-query
constant factor is lower (plan cache for pgbench's repeated shapes via generic
plans does not apply in simple protocol either — the difference is raw
constant-factor C vs Go executor construction, plus batched message flushing).
This mechanism is why even the read path is 1.96×; it is not write-specific.

## Cross-check: why these four suffice

END-latency reconstruction from the mechanisms is magnitude-consistent with
measurement (01 § consistency check, incl. its mixed-regime caveat): cycle ≈
3.8 ms fdatasync + amortized ~3.5 ms CLOG fsync (M2) + drain ≈ 8–9 ms; width
~6 → END in the mid-teens to low-20s ms (measured 21.2). Statement-time gaps
(UPDATE 5.1×/INSERT 6.8× vs SELECT 1.25×) match M1's per-record assembly +
M3's index traffic on top of M4's 2×.
