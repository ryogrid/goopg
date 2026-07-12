# perf-optimize3 — 04: Improvement candidates (design-level, ranked)

Ranked by expected write-path impact ÷ blast radius. These are directions with
sizing, not designs; each candidate that goes forward should get its own design
doc + agent review before implementation.

## C1 — Incremental canonical heap WAL records (drop the per-record FPI) — HIGHEST

**Change**: encode canonical **heap** records with tuple-level main data
(PG's `xl_heap_insert`/`xl_heap_update` layouts, which the builders already
partially construct) and attach a page image **only** on the first
modification of a page since the last checkpoint — the decision the buffer
pool already tracks (`maybeEmitFPI` / `fpiSinceCheckpoint`), mirroring PG's
`page_lsn <= RedoRecPtr` test in `XLogRecordAssemble`. The user-index btree
leaf insert already works exactly this way (`MarkDirtyChangeRecord` +
item-level `EncodeBtreeInsert`) — it is the in-repo template. Fold the
current **double logging** (canonical FPI record + native logical record per
heap write) into a single stream as part of the same change.

**Expected**: WAL/txn 33 KB → ~1–2 KB (bounded below by PG's 1.8–2.9 KB);
removes ~75 % of `memmove` CPU from write statements; shrinks drain volume per
group cycle ~15×; attacks the write-statement gaps (5–7×) and, via faster
statements and cycles, widens commit groups. Note it does **not** shrink the
per-call fsync floor (see 02 M1) — its END improvement comes through width and
drain, so it composes with, rather than replaces, C2. Device-independent win
(bytes + CPU). This is the single largest lever on the whole gap.

**Blast radius**: recovery/replay (`internal/wal/replay.go`, decoder,
classifier), physical standby (a real PG standby must replay these records —
today's FPI-only records are what PG-compat replay relies on), pg_waldump
parity, crash-recovery tests. On-disk WAL **format** stays PG-canonical; what
changes is which PG record shapes goopg emits. Must keep: torn-page safety ⇒
FPI-on-first-touch-per-checkpoint is not optional, and `full_page_writes=off`
stays unsupported. Sizeable but bounded — the wal-backend-flush slicing model
(record-kind-by-record-kind, replay-tested per slice) applies directly.

## C2 — Remove the commit-path CLOG fsync — SECOND

**Change**: make `applyGroupBatchLocked`'s write-back non-durable (write to
the SLRU segment file without fsync, or skip write-back entirely and leave
pages dirty in the CLOG buffer pool), fsyncing pg_xact only at checkpoint and
on page eviction — PG's `SlruPhysicalWritePage` discipline. Crash-safety
argument: every commit's WAL record is already durable before the group ack
(sync path) or before the CLOG page write (async path's existing
`flushWALBeforeWriteLocked` barrier); recovery replays commit records into
CLOG, so a lost unfsynced CLOG page is always reconstructible.

**Expected on this host**: removes a serialized ~6.3 ms fsync from roughly
every other commit group (6,734 in 60 s) — a large cycle-time cut here, but
**device-dependent**: on low-fsync-latency storage the win shrinks toward
zero (see 02 M2 storage dependence). It also unloads the shared disk queue.

**Crash-safety, concretely**: the machinery already exists. Recovery runs a
second pass that re-stamps CLOG from WAL commit/abort records
(`replayCLogFromWAL`, `internal/initdb/xact_recovery.go`, M0106-0013), and the
checkpointer flushes dirty CLOG pages before advancing the redo LSN
(`FlushCLOGFn`, `internal/wal/checkpointer.go`) — so an unfsynced CLOG page
lost in a crash is always reconstructible. (Do not be misled by
`internal/wal/recovery.go`'s "crash-recovery is a no-op for XactCommit"
comment — that describes the *first* pass; the CLOG re-stamp is the second.)
This makes C2's blast radius smaller than it first appears.

**Blast radius**: crash-recovery proof + tests (kill-9 matrices already
exist), keep the async-commit barrier ordering on the eviction path
(`writePageToDisk` keeps its barrier+fsync).

## C3 — B-tree on-access dead-entry cleanup (LP_DEAD / simple deletion)

**Change**: (a) scans mark entries whose heap tuples are dead-to-all as
LP_DEAD (goopg's index scans already visit the heap for visibility — the
signal exists at exactly PG's `kill_prior_tuple` point); (b) before splitting
(`insertIntoBlock`'s space check), purge LP_DEAD entries and retry the insert
(`_bt_simpledel_pass` analog) — reusing the existing rewrite machinery the
posting-dedup path already has.

**Expected**: stops the pkey doubling (+166 MB/2 min → ~0), eliminates the
~8 %-of-txns split rate and its FPI-heavy WAL, keeps descents/scans at
steady-state depth — fixes the *degrades-over-time* property. Secondary win
for the read path on updated tables.

**Blast radius**: btree page format (one bit per line pointer — the item
header has spare bits), concurrency with the Lehman-Yao paths, VACUUM
interaction, amcheck parity. Medium.

## C4 — Read-path constant factor (applies 1:1 to write statements too)

Directions, smaller individual wins: per-connection plan/operator-tree cache
keyed on query text (pgbench repeats 4 shapes; `opOpen` alone is 16.5 % of
`-S` CPU), coalesce protocol writes so a simple-query cycle issues one
`write(2)` instead of per-message flushes (~18 % `WriteReadyForQuery` +17 %
socket writes), snapshot-capture fast path (4.4 % in `-N`).

**Expected**: chips at the 1.96× read gap; every point gained here also raises
the write path's arrival rate (widening commit groups).

## Observability gaps found while measuring (cheap, do alongside)

- `pg_stat_wal` is a zero stub — wire `wal_records`/`wal_bytes`/`wal_fpi` (the
  writer already counts drains) and fsync counts (`walBufferCounters.fsyncCount`).
- `pg_current_wal_lsn()` exists in the catalog but has no runtime handler.
- `pg_stat_activity` wait events are empty during backend WAL flush — set
  `LWLock:WALWrite`/`IO:WalSync` around `flushUpToBackend`'s acquire/xlogWrite
  (hooks `OnWALSync`/`OnWALSyncDone` already exist).
- `pg_stat_user_tables.n_tup_upd`/`n_tup_hot_upd` are zero — the HOT-rate
  comparison in this bundle had to be inferred from file sizes.

## C5 — Pipelined commit groups (smaller, structural)

The block profile shows commit waits split across two serialized points (WAL
write lock, CLOG flushMu). A pipelining lever the other candidates only
partially subsume: let the next WAL group form and its CLOG batch stage while
the previous fsync is in flight (PG gets this for free from
LWLockAcquireOrWait + per-bank SLRU locks). Worth a design sketch after C1/C2
land; measure first whether the residual serialization still matters.

## Sequencing recommendation

C2 first **on this host class** (small, self-contained, big END cut at
multi-ms fsync floors — but verify the fsync floor on target hardware first;
on fast NVMe C1 leads outright), then C1 (the big one, sliced,
device-independent), C3 in parallel with C1 (different subsystem), C4
opportunistically.
Re-run `scripts/run_rw50.sh` after each landing; the AUX probe
(`aux2_fsync_probe.sh`) verifies each mechanism's signature directly
(bytes/txn for C1, fsync counts for C2, pkey growth for C3).
