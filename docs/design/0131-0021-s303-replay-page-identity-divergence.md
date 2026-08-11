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

## Update 2026-08-11 (loop #134): the probe was built and BOTH prime suspects
## came back negative — but a third, unlogged page mutation was found

### The probe

`internal/storage/pageident_probe.go` (temporary, gated on
`GOOPG_PAGEIDENT_PROBE=1`) exploits the two invariants this document derived:

* for a given `(rel, block)` the heap line-pointer **count only ever grows**
  (`PageAddHeapTuple` appends at `count+1`; `VacuumHeapPageBySlots` never
  shrinks the array), so a count below the high-water mark previously seen for
  that tag proves the slot's *bytes* belong to a different block than its *tag*
  → `PAGEIDENT-REGRESS`;
* a block number is handed out by `extend`/`extendBatch` exactly once, so a
  hand-out of a block that already carried tuples → `PAGEIDENT-REEXTEND`.

It observes at every disk read, every disk write, every pre-eviction flush, and
inside `tryApplyHOTUpdate` under the content lock. Only heap pages are checked:
a btree page legitimately loses line pointers on split (`pd_special != BlockSize`
is the filter — without it the first run reported 336 benign split hits,
`lp=205 high=407`, on the pgbench indexes).

Driver: `analysis/pageident_probe.sh` (same load as `crashprobe30.sh`, no kill).

### Result: negative on both suspects

| run | load | heap `PAGEIDENT-REGRESS` | `PAGEIDENT-REEXTEND` |
|---|---|---|---|
| `analysis/pageident_probe.sh` | scale 5, 16 clients, 45 s, no crash (1805 TPS) | 0 | 0 |
| `RUNS=1 PORT=5536 analysis/crashprobe30.sh` | the original probe, kill at 30 s — **reproduced the loss (497287/500000 rows, 2713 missing)** | 0 | 0 |

So buffer-pool tag/content aliasing did **not** occur in a run that did lose
committed rows. Suspect 2 is dead on inspection rather than measurement:
`relFile.readBlock` returns `ErrShortRead` for `blk >= nblocks` and `pinLoad`
propagates that error — there is no path that publishes a silently `PageInit`-ed
page for a block past EOF. (`goopg smgr O_CREATE recreates removed files` applies
to a whole missing *fork*, not to a short one.)

### What the probe surfaced instead: an UNLOGGED page mutation in this very path

`tryApplyHOTUpdate`'s orphan-cleanup arm (`operators_storage.go:3746`) calls
`storage.PageRemoveHeapTuple(s.Page(), newSlot)` when the old-slot stamp fails —
and emits **no WAL record at all** for that mutation, while the `PagePruneOpt`
that usually *caused* the failure was logged (`markHeapPruneOptDirty`).
`PageRemoveHeapTuple` does not merely blank the line pointer: when the removed
slot is the last one it **shrinks `pd_lower`** (`heap.go:697-699`), i.e. it
reduces the page's line-pointer count. The slot is already dirty from the logged
prune, so the mutation reaches disk — and replay, which never saw it, rebuilds a
*larger* page than the runtime had. That is the S30.3 signature exactly: the
runtime and the WAL disagreeing about a page's line-pointer array, with no record
to explain the difference.

It shrinks by one line pointer per occurrence, so it does not by itself explain
`185 → 1`; it is filed as its own defect (**M0131-S30.5**) rather than as the
S30.3 root cause. Upstream has no unlogged heap-page mutation of this kind:
`PageRepairFragmentation` truncates trailing unused line pointers only inside a
vacuum/prune that is itself WAL-logged under a cleanup lock
(`postgres/src/backend/storage/page/bufpage.c`).

### Where S30.3 stands

Still open, and the search space is now smaller. Remaining candidates, in order:

1. the unlogged mutation above, or another like it, applied repeatedly (the probe
   would only catch it once the page is re-read or re-flushed — a page mutated
   and then lost to the crash is invisible to `postWrite`/`postRead`);
2. the emit side writing a `new_off` that is not the `newSlot` it used
   (`markHeapHotUpdateDirty`'s encoding is not yet independently verified against
   the runtime value — worth one assertion);
3. replay applying a correctly-decoded record to a page it rebuilt differently
   earlier in the stream (i.e. the divergence starts at an EARLIER record for
   block 130, not at 826236).

Next probe: extend `PageIdentityObserve` to fire inside `PageRemoveHeapTuple`
itself and at `markHeapHotUpdateDirty` (asserting `new_off == newSlot` and
`newSlot == PageLinePointerCount(page)`), so an emit-time inconsistency is caught
before the crash can hide it.

## Update 2026-08-11 (loop #135) — S30.5 FIXED: the orphan cleanup is now an exact undo

The unlogged mutation found above is closed. The fix is neither of the two
candidates the defect was filed with (emit a one-slot prune record / stop
shrinking `pd_lower`); both miss that **the append is unlogged too**.

`tryApplyHOTUpdate`'s orphan arm does `PageAddHeapTuple` → (stamp fails) →
`PageRemoveHeapTuple`. Neither call emits WAL, so the pair does not need a
record at all — it needs to be a *no-op on the page*. It was not:
`PageAddHeapTuple` moves both `pd_lower` (+4) and `pd_upper` (−MAXALIGN(len)),
while `PageRemoveHeapTuple` restored only `pd_lower`. Every occurrence therefore
leaked one tuple's worth of free space out of a page that the WAL says still has
it — a runtime/WAL divergence with no record to explain it, i.e. the S30.3
signature in miniature.

`PageRemoveHeapTuple` (`internal/storage/heap.go`) now also raises `pd_upper`
back over the item body when the removed slot is the last line pointer *and* its
body sits exactly at `pd_upper` — precisely the state left by an append, so the
call becomes an exact inverse. Interior removals are unchanged (blank the
pointer in place; sliding the array would renumber later slots, which is
`VacuumHeapPageBySlots`' job). The vacated bytes fall back inside
`[pd_lower, pd_upper)` — PG's FPI hole (`xloginsert.c XLogRecordAssemble`) — so
they are not part of the page image on either side.

Guards: `TestPageRemoveHeapTupleUndoesAppend` (fails on the pre-fix file with
`pd_upper=8056 want=8096`) and `TestPageRemoveHeapTupleInteriorSlotKeepsUpper`
in `internal/storage/heap_test.go`.

One divergence of the same family remains open in this arm and is recorded in
the deferral ledger: when the multi-xact stamp of the OLD slot succeeds and the
following `PageSetHeapTupleCmax` fails, the old tuple's `xmax` stays mutated
with no WAL behind it. That path is not undone here.

S30.3 itself is unchanged: candidate 1 above is now partly discharged (this was
one such mutation, and it is one line pointer per occurrence — not `185 → 1`),
leaving candidates 2 and 3 as the next probes.
