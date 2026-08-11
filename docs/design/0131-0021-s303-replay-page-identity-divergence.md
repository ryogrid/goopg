# M0131-S30.3 — a crashed cluster refuses to start because replay and the
# runtime disagree about *which physical page* a HOT update touched

Status: diagnosis (root cause narrowed, not yet fixed)
Predecessor: `docs/design/0131-0020-crash-recovery-row-loss-confirmed.md`
Repro (preserved on disk): `/tmp/s30_3_repro/data` — copy it and start goopg on
the copy; startup fails in ~35 s, deterministically, with

```
goopg start: goopg: wal replay: wal: replay record 826236 lsn[154662577,154662656]:
wal: xlog heap-update add new tuple: storage: not enough free space in page
```

This is run B of the S30 crash probe (`analysis/crashprobe30.sh`, scale 5,
16 clients, SIGKILL 30 s in). The data directory survived, so the failure is now a
deterministic, offline, ~35 s experiment instead of a 4-minute stochastic one.

## What the record actually says

goopg's decode and PostgreSQL's own `pg_waldump` agree exactly (the WAL is
PG-framed, so the upstream tool is usable as an independent oracle here):

```
rmgr: Heap  len 78, tx: 41067, lsn: 0/0937F6B0, desc: HOT_UPDATE
      old_xmax: 41067, old_off: 1, flags: 0x10, new_xmax: 0, new_off: 2,
      blkref #0: rel 1663/5/16407 blk 130
```

`16407` is `pgbench_tellers`. So the record claims: on block 130, the tuple in
line pointer **1** was HOT-updated and the new version landed in line pointer **2**.

## What the page looks like

Read straight off the crashed cluster, before any replay:

```
block 130 of 1663/5/16407:  pd_lsn=123846600  pd_lower=764  pd_upper=792
                            185 line pointers, ALL LP_NORMAL, 28 bytes free
                            pd_prune_xid=25495       (relation has 287 blocks)
```

`pd_lsn=123846600` is *exactly* the end LSN of the last WAL record that touches
this page (record 707555, a `HEAP DELETE`). Replaying the whole 826236-record
prefix into a copy of the cluster leaves the page bit-for-bit in that same shape —
replay is faithful; it is not the party that got the page wrong.

Between record 707555 (LSN 123 846 600) and the failing record (LSN 154 662 577)
— about 120 000 records — **no WAL record of any kind references rel 16407
block 130**: no insert, no prune, no full-page image, no truncation.

## Why this is a page-identity bug, not a free-space bug

The tempting reading is "the runtime pruned the page and the prune record is
missing". That reading is wrong, and the two page primitives prove it:

* `storage.PageAddHeapTuple` (`internal/storage/heap.go:537`) **never reuses a
  free line pointer** — it always appends at `PageLinePointerCount()+1`.
* `storage.VacuumHeapPageBySlots` (`heap.go:~940`) marks dead slots LP_UNUSED and
  repacks the tuple bodies, but **never shrinks the line-pointer array**
  (`pd_lower` is untouched).

So no amount of pruning can make the next insert land in slot 2 on a page that has
185 line pointers. `new_off: 2` can only be produced by a page whose line-pointer
count was **1** at the moment the runtime added the tuple — i.e. a freshly
`PageInit`-ed page holding a single tuple. The runtime therefore modified a page
that was not (or was not yet) the block 130 the record names, and its old tuple at
slot 1 has no insert record on that block either.

The failure at replay is the downstream symptom: replay dutifully applies the
record to the real block 130, which is full, and `PageAddHeapTuple` returns
`ErrNoSpaceInPage`, which `replayDecodedXLogHeapUpdate`
(`internal/wal/recovery.go:3125`) turns into a fatal startup error. Even if that
arm were made tolerant, applying the record there would be *wrong* — the update
belongs to some other physical page.

## Ruled out by experiment (do not re-test)

* **Missing prune records.** Prune records exist and decode fine
  (`rmid=9` = Heap2, `info=0x10` = `XLOG_HEAP2_PRUNE_ON_ACCESS`; 47 929 in this
  stream, 161 of them on rel 16407). Note the rmid numbering: `9` is Heap2,
  `10` is Heap, `11` is **Btree** — an earlier reading of this histogram that put
  prune records at rmid 11 was mis-numbered.
* **A prune could explain `new_off: 2`.** It cannot; see the two primitives above.
* **Unlogged relation truncation in the window.** All 7 `XLOG_SMGR_TRUNCATE`
  records in the stream sit at record index ≤ 504 370, far below the window.
* **A decode bug in goopg's reader.** `pg_waldump` produces the identical block
  reference, offsets and flags.
* **Replay corrupting the page itself.** Pre-replay and post-prefix-replay page
  images agree.

## Where to look next

The question is now narrow: *why did the runtime hold a one-line-pointer page for
`{rel 1663/5/16407, block 130}`?* Two candidates, in order of suspicion:

1. **Buffer-pool tag/content aliasing** — a slot reused for a new tag whose
   contents were not (re)loaded, so the backend wrote into a page belonging to a
   different block while the WAL record carried the pinned tag. Sixteen concurrent
   clients extending and churning a small, heavily updated relation is the load
   that would expose it.
2. **An `IsNew`-driven silent re-init** — `Pin` on a block whose file was shorter
   than the block number returns a zero page, which the insert path treats as new
   and `PageInit`s. goopg already has a related sharp edge on record
   (`goopg smgr O_CREATE recreates removed files`: `NBlocks`/`Pin` on a removed
   fork silently recreates it empty). A relation file that lost length without a
   logged truncation would produce exactly this.

Cheapest decisive probe: add a temporary invariant in the HOT-update emit path
(`tryApplyHOTUpdate`, `internal/executor/operators_storage.go:3555`) that logs
`{rel, blk, PageLinePointerCount, newSlot, page pd_lsn}` whenever `newSlot` is far
below the page's historical high-water mark, and a matching check at `Pool.Pin`
that the loaded page's `pd_lsn`/contents are consistent with the tag. Run the
30 s pgbench load from `analysis/crashprobe30.sh`; no crash is needed, because the
divergence is created during normal running — the crash only *reveals* it.

## Gate for the eventual fix

Unchanged: `RUNS=3 bash analysis/crashprobe30.sh` must print `OVERALL: PASS`.
The preserved directory gives the fix a second, faster gate: starting goopg on a
copy of `/tmp/s30_3_repro/data` must succeed, and afterwards block 130 of
`pgbench_tellers` must be a page the runtime could actually have produced.
