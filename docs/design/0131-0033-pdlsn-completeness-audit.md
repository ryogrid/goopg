# 0131-0033 — `pd_lsn` completeness on logged change paths (M0131-S26)

**Status:** partially landed (2026-08-12). The audit below is complete for the
`Pool.MarkDirty*` family; the cross-page heap UPDATE defect it found is fixed
and covered by regression tests. The debug assertion and the `logXxx`-hook side
of the audit (`internal/initdb/open.go`) remain open — see *Deferred*.

## Why `pd_lsn` is not bookkeeping

Two independent mechanisms read a page's `pd_lsn`, and each breaks in a
different way when a logged mutation forgets to stamp it.

1. **WAL-before-data.** `Pool.flushSlots` computes
   `flushTo = max(pd_lsn over the batch)` and calls `FlushUpTo(flushTo)` before
   handing the pages to `WriteBlockAIO`
   (`internal/storage/bufpool.go`, the `flushTo` computation). A page whose
   `pd_lsn` is behind the record that mutated it can therefore reach disk
   *before* that record is durable. After a crash the data file holds a change
   with no WAL behind it — the one thing the WAL rule exists to prevent.

2. **Redo skip.** PG's `XLogReadBufferForRedoExtended` skips a record for a
   page that already contains it:
   `if (lsn <= PageGetLSN(page)) return BLK_DONE;`
   (`postgres/src/backend/access/transam/xlogutils.c`). A stale `pd_lsn` makes
   the page understate which records it holds, so redo re-applies. Whether that
   is harmless depends entirely on the record's idempotence — an xmax stamp
   survives it, a btree insert or a tuple append does not. Streaming from a base
   backup barely exercises this (pages arrive from the backup at a known LSN);
   **crash recovery does it constantly**, and since M0131-S27 a real PG replays
   goopg's records over goopg's own live pages.

The inverse error — `pd_lsn` too HIGH — is the dangerous one for redo (records
get skipped that should apply); a too-low `pd_lsn` is the dangerous one for the
WAL rule. Neither is acceptable.

## Audit: every `Pool.MarkDirty*` call site

The invariant: **a page mutated by a WAL-logged action must end the call
carrying an LSN at or past the record that describes the mutation.** Only
`MarkDirtyWithLSN*`, `MarkDirtyChangeRecord`, `MarkDirtyLogicalChange`,
`MarkDirtyForceFPI` and (as of this slice) `MarkDirtyCoveredByRecordLocked`
stamp it. Plain `MarkDirty` stamps `pd_lsn` only as a *side effect* of
`maybeEmitFPI` — i.e. only on the first touch of a checkpoint epoch. On any
later touch it advances nothing.

23 plain-`MarkDirty` call sites exist outside the pool itself. They fall into
three classes:

| class | sites | verdict |
|---|---|---|
| **A. Hook-nil fallback** — the enclosing helper picks `MarkDirtyChangeRecord`/`MarkDirtyLogicalChange` when its `logXxx` hook is wired and falls back to plain `MarkDirty` when it is not (test harnesses, pre-runtime callers). No record is emitted, so there is nothing to be behind. | `btree.go:2098, 2632, 2739, 3061-3067, 3500`; `operators_storage.go:3275, 3306, 9198, 9300, 9355, 9393`; `operators_lockrows.go:2406`; `vacuum.go:153, 183`; `operators_indexonly.go:334` | **OK** |
| **B. Deliberately unlogged mutation** — the page change is real but no WAL record describes it by design. | `operators_lockrows.go:2050, 2133` (multixact lock stamp: the heap-lock record carries one xid + strength and cannot describe a multi; lock state is transient and correctly lost on crash — `docs/design/0118-0002`); `sys_pg_database.go:271` (`datconnlimit`), `operators_vacuum_datfrozenxid.go:131` (`datfrozenxid`) — in-place shared-catalog updates | **B/multixact OK; B/pg_database is a real gap** — see *Deferred* |
| **C. Logged mutation with no stamp** | `operators_storage.go:9306` | **DEFECT — fixed here** |

### The defect (class C)

`updateHeapRowCanonicalPG`'s cross-page branch mutates two pages and emits
**one** `xl_heap_update` covering both: the new version is appended to a fresh
page, and the old version's page gets the xmax + forward-`t_ctid` stamp
(`PageStampUpdatedOldTuple`). Only the new page went through
`MarkDirtyLogicalChange`; the old page got a plain `MarkDirty`. Once that page
had already been imaged in the current checkpoint epoch — the normal state for
a hot catalog page — its `pd_lsn` stayed behind the record. Both failure modes
above then apply to the xmax stamp.

The same-page branch (`:9198`) is unaffected: there the single mutated page *is*
the one passed to `MarkDirtyLogicalChange`.

### The fix

`Pool.MarkDirtyCoveredByRecordLocked(s, lsn)` (`internal/storage/bufpool.go`) is
the explicit primitive for the **secondary page of a multi-page record**:

- `maybeEmitFPI(s)` first, exactly as plain `MarkDirty` would — the page still
  owes its own first-touch image;
- raise `pd_lsn` to `lsn`, **never lower it** (`maybeEmitFPI` may have just
  stamped a larger image LSN);
- **do not** advance `nativeImageLSN`. No image of *this* page exists at the
  record's LSN, so moving the FPI watermark would suppress the page's next
  first-touch image — the distinction from `MarkDirtyWithLSNLocked`, whose
  callers log their own image-bearing multi-page records (btree split).

The caller captures the record's LSN out of the emitter closure and passes it;
with the hook unwired (`recLSN == 0`) the plain `MarkDirty` fallback stays, since
no record was written.

### Tests

- `internal/storage/pdlsn_secondary_page_test.go` —
  `TestMarkDirtyCoveredByRecordStampsSecondaryPage` (already-imaged epoch: LSN
  raised, no second image, watermark untouched, never rewound) and
  `TestMarkDirtyCoveredByRecordEmitsFirstTouchImage` (first touch still owes the
  FPI; resulting `pd_lsn` is the max of image and record LSN).
- `internal/executor/pdlsn_cross_page_update_test.go` —
  `TestCrossPageCatalogUpdateStampsOldPageLSN` drives the real cross-page branch
  with the epoch state that hid the bug. Verified fail-when-broken: reverting
  the call site reports `old page pd_lsn = 1001, still behind the covering
  xl_heap_update at 1002`.

## Deferred (item stays unchecked)

1. **Debug assertion.** No mechanised guard yet distinguishes class A (benign)
   from class C (defect) at runtime — the audit above is by inspection, so a new
   call site can reintroduce the bug silently. Intended shape: an env-gated
   (`GOOPG_PDLSN_ASSERT=1`) report in plain `MarkDirty` when WAL hooks are wired,
   after splitting class B onto an explicit `MarkDirtyUnlogged(s, reason)` so the
   deliberate sites stay quiet.
2. **`logXxx` hook audit.** `internal/initdb/open.go`'s hook bodies were not
   audited for records that name blocks beyond the one the caller stamps — the
   same multi-page shape as the defect above, one layer down.
3. **Unlogged in-place shared-catalog updates** (class B, `pg_database`):
   `datconnlimit` and `datfrozenxid` are written into the heap page with no WAL
   record at all, so they survive a crash only when the epoch's FPI happened to
   capture them. This is a durability gap in its own right, not a `pd_lsn` gap.
4. **`MarkDirtyHint` is correctly exempt** and stays so: hint bits are
   recomputable from `pg_xact`, PG does not WAL-log them
   (`MarkBufferDirtyHint`), and stamping `pd_lsn` there would self-invalidate the
   FPI re-verify. The hint path pays for the exemption with `hintFlushBarrier`
   instead. No logged path shares the exemption — that was the question this
   audit had to answer, and the answer is the table above.
