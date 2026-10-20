# M0119 — visibility-map changes in PG's WAL form

Status: slice 1 landed 2026-10-21 (clears ride heap records, and redo honours
them). Slice 2 (sets as XLOG\_HEAP2\_VISIBLE, native records retired) is
open.

## 1. Gap

goopg logs every visibility-map (VM) change as its own rmgr-128 record
(`RecordKindHeapVisible`, M0146-0063), from the map's WAL hook
(`initdb.Open`). The record has no block reference and no tablespace. A PG
standby cannot replay rmgr 128 and stops there.

PG has no standalone VM-clear record:

- **A set** is `XLOG_HEAP2_VISIBLE`:
  - block 0 is the VM page, block 1 the heap page;
  - the body is `xl_heap_visible{snapshotConflictHorizon, flags}`;
  - redo also sets `PD_ALL_VISIBLE`.
- **A clear** rides the heap record that caused it, as a flag bit. heap\_insert,
  heap\_delete and heap\_update set it when `PageIsAllVisible` held, under
  the buffer lock:
  - `XLH_INSERT_ALL_VISIBLE_CLEARED`;
  - `XLH_DELETE_ALL_VISIBLE_CLEARED`;
  - `XLH_UPDATE_OLD/NEW_ALL_VISIBLE_CLEARED`.

  Redo clears the bits first, "even if the heap page is already up-to-date",
  then `PageClearAllVisible` when it redoes the page.

So emitting sets in PG's form alone would be worse than today. A PG standby
would set VM bits and `PD_ALL_VISIBLE` from goopg's sets, but never clear
them, because goopg's clears are still rmgr-128 records. Its scans would
trust a stale all-visible page and return wrong results. Clears therefore go
first.

## 2. Two latent defects found

1. goopg's heap records never set the flags, even though it emits PG-format
   insert, delete and update records.
2. goopg's redo of PG's insert, delete and update ignored the flags.
   Multi-insert redo cleared only `PD_ALL_VISIBLE`. A goopg standby of a real
   PG primary therefore kept a stale all-visible VM bit after PG cleared it,
   and an index-only scan there skipped the heap for a changed page.

## 3. Slice 1

**Redo** (xlog/recovery.go):

- `redoClearAllVisible` is `visibilitymap_clear(VALID_BITS)`. A relation with
  no VM fork has nothing to clear, and the fork is not created (smgr's
  O\_CREATE trap).
- The insert, multi-insert, delete and update arms run it before their image
  restore or page redo, whatever that redo decides.
- `clearPageAllVisible` clears `PD_ALL_VISIBLE` on each redone page:
  - update clears the old page for `OLD_…` and the new page for `NEW_…`;
  - a same-page update clears its one page for `OLD_…`.

**Emission:**

- `EncodeHeapInsertPGVis`, `EncodeHeapDeletePGVis`,
  `EncodeHeapHotUpdatePGVis` and `EncodeHeapUpdatePGVis` set PG's bits.
- The old encoders keep their signatures and pass false.
- `initdb.Open`'s heap WAL hooks pass `vm.AllVisible(rel, blk)`, read at log
  time. Every executor site logs the heap change and then calls
  `VM.ClearBlock`, under the same page lock, so the bits at log time are the
  ones this change clears. That is PG's `PageIsAllVisible` test.
- The VM is now constructed before the hooks.
- The native clear record is still written after the heap record. It is
  redundant for goopg's replay, and slice 2 retires it.

## 4. Verified with PG's own tool

`TestPgWaldumpHeapRecordsCarryVMCleared` runs PG 18.3's `pg_waldump` on
goopg's WAL after this sequence:

```
VACUUM; INSERT
VACUUM; DELETE
VACUUM; HOT UPDATE
```

- The three changes after each VACUUM carry the cleared bit: `INSERT flags:
  0x09`, `DELETE flags: 0x03` and `HOT_UPDATE flags: 0x11`.
- None of the ten seed inserts carries it.

## 5. Pins

- `TestPGHeapRecordsClearVMBits` covers PG-format insert, delete, HOT
  update, cross-page update, and an insert whose heap page is skipped
  (pd\_lsn past the record). In each case the VM bits and `PD_ALL_VISIBLE`
  are cleared. A flagless insert keeps them. Dropping the insert arm's
  `redoClearAllVisible` fails two subtests.
- `TestHeapEncodersSetVMClearedFlags` checks each encoder's bit, and only
  when asked.
- `TestPgWaldumpHeapRecordsCarryVMCleared` (§4).

## 6. Slice 2 (open), and other gaps (ledgered)

- **Slice 2.** Emit sets as `XLOG_HEAP2_VISIBLE` through the assembler, with
  the VM page as block 0 and the heap page as block 1. Then retire the native
  set and clear records once every clear has its heap record:
  - VACUUM's corrective clear needs no WAL in PG (`visibilitymap_clear`
    alone);
  - TRUNCATE and DROP ride smgr records;
  - a length-versioned decode of the native record stays for old WAL.
- **Row locks.** The lock record is still goopg-native (`EncodeHeapLock`),
  so the frozen-only clear cannot carry `XLH_LOCK_ALL_FROZEN_CLEARED` yet.
- **Multi-insert.** goopg emits no PG multi-insert record, so
  `ALL_VISIBLE_CLEARED` on multi-insert is replay-only.
- **`PD_ALL_VISIBLE` at runtime.** goopg never sets it at runtime; only redo
  of PG's VISIBLE record does.
- **Delete records.** goopg sets `XLH_DELETE_CONTAINS_OLD_TUPLE` on every
  delete; PG logs the old tuple only for logical decoding.
- **Conflict horizon.** The native set record carries no
  snapshotConflictHorizon. Slice 2 needs one for a PG standby's query
  conflicts.
