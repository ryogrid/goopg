# Durable MultiXact SLRU + `multixact_redo` — the last unavoidable missing rmgr

**Status:** draft
**Date:** 2026-08-11
**Milestone:** M0131 (Theme F, S24)

## Problem

`RM_MULTIXACT_ID` (rmid 6) is the only genuinely unavoidable missing resource
manager in the reverse (PG → goopg) crash path. Every other missing rmid is
either an index AM goopg does not implement (12 Hash, 13 GIN, 14 GiST, 16 SPGist,
17 BRIN — S25), a defensible no-op (19 ReplicationOrigin, 21 LogicalMessage), a
refusal (18 CommitTs), or extension-only (20 Generic) — see S23/S25. MultiXact is
none of those: it is produced by **ordinary concurrency on ordinary tables**, it
is **independent of `wal_level`** (`MultiXactIdCreateFromMembers` writes its
`XLOG_MULTIXACT_CREATE_ID` unconditionally,
`postgres/src/backend/access/transam/multixact.c:885-889`), and today it decodes
fine and then hits `replayDecodedXLogRecord`'s outer `default:`
(`internal/wal/recovery.go:2525`) — goopg refuses to start.

### When PG actually emits a multixact record

Every tuple-level producer funnels through `compute_new_xmax_infomask`
(`postgres/src/backend/access/heap/heapam.c:5324-5589`); VACUUM adds two more.

| scenario | multi? | why |
|---|---|---|
| single session `SELECT … FOR SHARE` on an unlocked row | **no** | `old_infomask & HEAP_XMAX_INVALID` short-circuits to a plain xid xmax (`heapam.c:5341-5350`) |
| two sessions `FOR SHARE` on the same row | **yes** | second locker takes the `TransactionIdIsInProgress(xmax)` branch → `MultiXactIdCreate` (`heapam.c:5571`) |
| `FOR UPDATE` + `FOR KEY SHARE` from different xacts | **yes** | same branch; `get_mxact_status_for_lock` gives the two members different statuses |
| `UPDATE` of a row locked by a **live** xact | **yes** | `heapam.c:5571`, `is_update = true` |
| `UPDATE`/lock of a row whose updater **committed** | **yes** | the updater must be preserved as a member — `heapam.c:5464` (`HEAP_XMAX_COMMITTED`) and `heapam.c:5571`'s sibling at `:5545` |
| row already carrying a multi, plus one new locker | **yes (Expand)** | `MultiXactIdExpand` (`heapam.c:5439`) rebuilds the set |
| **two concurrent sessions inserting children of the same parent** | **yes** | FK RI takes `FOR KEY SHARE` on the parent row; two concurrent share-lockers is exactly case 2. **The commonest real-world source** |
| single-session FK insert | **no** | one locker → plain xid xmax |
| `VACUUM` freeze of a multi-bearing tuple | **yes** | `FreezeMultiXactId` → `MultiXactIdCreateFromMembers` (`heapam.c:7009`) when survivors remain |
| `VACUUM`'s horizon advance | **yes** | `vac_update_datfrozenxid` → `TruncateMultiXact` (`postgres/src/backend/commands/vacuum.c:1983`) → `XLOG_MULTIXACT_TRUNCATE_ID` (`multixact.c:3473`) |
| offsets/members SLRU page rollover | **yes** | `ZeroMultiXactOffsetPage` / `ZeroMultiXactMemberPage` emit `ZERO_OFF_PAGE` / `ZERO_MEM_PAGE` (`multixact.c:2136-2162`) |

**Correction to the Theme F citation:** it points at `multixact.c:~3446` for "the
ZERO variants". Line 3441-3447 is `WriteMZeroPageXlogRec`, the shared *writer
helper*; the actual emission sites are `ZeroMultiXactOffsetPage`
(`multixact.c:2136-2146`) and `ZeroMultiXactMemberPage` (`:2152-2162`). The
`~889 CREATE_ID` and `~3473 TRUNCATE_ID` citations are exact.

## Design

### What goopg has

- `internal/multixact/multixact.go` (365 LOC) — the semantic core: `Status`,
  lock-mode conflict matrix (`StatusesConflict` `:212`, `MembersConflict` `:227`),
  `GetUpdateXid` `:248`, `HasLockers` `:260`, `HintBits` `:330`.
- `internal/multixact/store.go` (254 LOC) — the allocator/resolver: a
  `map[MultiXactId][]Member` (`byID`) plus a set-dedup index (`bySet`), with
  `Create` `:127`, `CreateFromMembers` `:140`, `Expand` `:188`, `Members` `:112`.
  Its own header calls it *"the in-memory analog of PostgreSQL's MultiXact SLRU
  (pg_multixact/offsets + pg_multixact/members)"* and records two deliberate
  divergences: an unbounded global dedup cache, and **ids are never
  truncated/vacuumed**.
- The seeding seam `NewStoreAt(next MultiXactId)` (`store.go:88`), documented as
  *"Use this to seed the allocator from pg_control's nextMulti"* — with **no
  caller**: `cmd/goopg/main.go:560` always calls `NewStore()`. (M0131-S20.4 wires
  it; S24 needs it too.)
- `pg_multixact/{offsets,members}` exist on disk but hold a single zero page each,
  written once by `bootstrapSLRUPlaceholders` (`internal/initdb/initdb.go:1371-1384`,
  paths at `:1375-1376`) purely so `pg_basebackup` does not skip an empty
  directory. **Nothing ever writes them again.**

So the engine is real and process-local; the persistence is a placeholder.

### What it needs: a durable offsets + members SLRU

Two SLRUs with different geometries:

| | key | page layout | per page |
|---|---|---|---|
| `pg_multixact/offsets` | MultiXactId | `MultiXactOffset` (u4) per id | `BLCKSZ/4` = 2048 (`multixact.c:110`) |
| `pg_multixact/members` | MultiXactOffset | 20-byte *member groups*: 4 status bytes + 4 `TransactionId`s | 409 groups × 4 = 1636 members (`multixact.c:148-156`) |

**Why `internal/mvcc/clog_bufferpool.go` cannot simply be reused.** Its locate
math is hard-wired to a fixed 2-bits-per-key packing: `clogBitsPerXact = 2`,
`clogXactsPerByte = 4`, `clogXactsPerPage = BlockSize * 4 = 32768`
(`internal/mvcc/clog.go:546-548`), and both `getStatus` (`clog_bufferpool.go:302`)
and `setStatus` (`:321`) compute `shift := (xidInPage % clogXactsPerByte) *
clogBitsPerXact` inline. Offsets are 32-bit-per-key (a different constant, same
shape), but **members are variable-length runs**: resolving one MultiXactId means
reading `offsets[multi]` and `offsets[multi+1]` — possibly on two different pages,
possibly in two different segments — then walking `nmembers` entries across the
members SLRU with the flag/xid split of `MXOffsetToFlagsOffset` (`multixact.c:186`) /
`MXOffsetToMemberOffset` (`:206`). No amount of parameterising
`clogBitsPerXact` gets there.

What *does* transfer, unchanged in shape:
- segment naming and page→file math — `segPathForPage`
  (`clog_bufferpool.go:175-179`: `%04X` of `pageNo / slruPagesPerSegment`, byte
  offset `pageInSeg * BlockSize`), with `slruPagesPerSegment = 32`
  (`internal/mvcc/clog.go:549`);
- the LRU slot array, pin/evict and dirty-writeback loop
  (`pinPageLocked` `:221`, `evictVictimLocked` `:206`, `flushDirty` `:429`);
- the wraparound-aware page comparison — `CLOGPagePrecedes`
  (`internal/mvcc/clog.go:127-137`), whose multixact twin is
  `MultiXactOffsetPrecedes` / the `SlruPagePrecedesUnitTests` contract
  (`multixact.c:2042`, `:3430-3435`).

**This would be goopg's third hand-rolled SLRU.** `internal/mvcc/subxact_slru.go`
(309 LOC) already re-derives the same constants independently rather than sharing
CLOG's — `subtransPagesPerSegment = 32` at `subxact_slru.go:27`, duplicating
`slruPagesPerSegment` at `clog.go:549`, with its own `SubtransPagePrecedes`
(`subxact_slru.go:249`) duplicating `CLOGPagePrecedes`. **Extract a shared
abstraction as part of S24** — a small `slru` package parameterised on
`(dir, bytesPerEntry|customLocate, pagesPerSegment, pagePrecedes)` — and port CLOG
and subtrans onto it, rather than adding a third copy. This is the single largest
non-obvious cost in the slice and the reason it is estimated at ~4 loops.

### `multixact_redo`

Four opcodes (`postgres/src/include/access/multixact.h:68-71`), redo at
`postgres/src/backend/access/transam/multixact.c:3481-3610`:

| opcode | hex | redo |
|---|---|---|
| `XLOG_MULTIXACT_ZERO_OFF_PAGE` | 0x00 | zero + write the offsets page, **unless** it was pre-initialised (`multixact.c:3488-3514`) |
| `XLOG_MULTIXACT_ZERO_MEM_PAGE` | 0x10 | zero + write the members page (`:3515-3531`) |
| `XLOG_MULTIXACT_CREATE_ID` | 0x20 | `RecordNewMultiXact(mid, moff, nmembers, members)` + advance `nextMXact`/`nextOffset`/`oldestXid` (`:3532-3576`) |
| `XLOG_MULTIXACT_TRUNCATE_ID` | 0x30 | advance `oldestMulti` and unlink truncated segments (`:3577-3608`) |

The `CREATE_ID` body is `xl_multixact_create{mid, moff, nmembers}` followed by
`nmembers × MultiXactMember` (`multixact.c:816`, `:885-889`).

**The one piece of genuine cross-record state in the entire missing-rmgr set is
`pre_initialized_offsets_page`** (`multixact.c:383`, a file-scope
`static int64 … = -1`). `RecordNewMultiXact` sets it (`:969`) when, during
recovery, it has to implicitly initialise the *next* offsets page because the WAL
was generated by an older minor version that did not pre-set the next multixid's
offset. `multixact_redo` then consumes it in two places: `ZERO_OFF_PAGE` **skips
the zeroing** when `pre_initialized_offsets_page == pageno` (`:3500-3513`), and
`CREATE_ID` logs and clears it if still set (`:3539-3552`). Skipping this flag
double-zeroes a live offsets page — silently discarding every multixact offset
already written to it. Any goopg port must carry the same state across records in
the replay driver, not inside a per-record function.

### Two adjacent defects that need ledger rows regardless

**(a) goopg's emit side stamps a multi xmax with no WAL record at all.** Four
producer sites write `PageSetHeapTupleXmaxMulti` and then only `MarkDirty`:
`internal/executor/operators_lockrows.go:2040` (`stampMultiLock`) and `:2126`
(`stampMultiUpdaterLock`); `internal/executor/operators_storage.go:3468`
(`carryForwardLockersToNewTuple`) and `:3485` (`stampUpdaterXmaxNonHOT`). The
justifying comment is at `operators_lockrows.go:2044-2051`:

> *"MultiXact membership is process-shared in-memory state, not yet persisted
> through the heap-lock WAL record (which carries a single xid + strength, so
> logging one record for the strongest holder would mis-describe the multi on
> replay). … Lock-only multixact state is transient — the holders' transactions do
> not survive a crash — so losing it on recovery is correct."*

**That justification holds for lock-only multis and fails for the
updater-bearing ones.** `stampUpdaterXmaxPreservingLockers`
(`operators_storage.go:3353-3369`) explicitly appends the writer as an *updater*
member (`:3362`, `updaterMemberStatus(keysUpdated)`) before calling
`CreateFromMembers`, and `stampMultiUpdaterLock`
(`operators_lockrows.go:2080-2135`) combines a share locker with a **committed**
updater. A committed updater's effect survives the crash; the MultiXactId naming
it does not. After a goopg restart the tuple's xmax is a MultiXactId no store can
resolve. **This is a defect in goopg's own crash recovery today**, not only in the
reverse path, and it is independent of whether S24 lands.

**(b) An unresolvable multi xmax silently hides rows.** `internal/mvcc/visibility.go:125-147`
returns `false` (invisible) when `mxs == nil` or `Members(...)` reports `!ok`,
with the comment *"never expose a version whose successor may already be
committed"* — a defensible fail-safe, but silent. `internal/storage/prune.go:97-107`
(`TupleDeadToAll`) and `internal/storage/freeze.go:83-92` take the matching
skip-rather-than-error path. So a PG-authored multi xmax that goopg cannot resolve
makes rows **vanish** instead of raising. Combined with (a), a goopg crash can do
the same to goopg's own rows.

Worth noting for the fix: the visibility comment claims *"(No producer emits
non-lock-only multis yet, so this is unreachable today.)"* — **stale**, per (a).
`stampUpdaterXmaxPreservingLockers` is exactly such a producer.

Both defects want their own ledger rows even if S24 is deferred: (a) because it is
a live data-visibility bug, (b) because "invisible" is the wrong failure mode for
"cannot resolve".

### Scoping: is S24 in M0131 or not?

The trigger is **concurrency**, and the decision belongs to S28's workload:

- If the S28 reverse-crash E2E is **single-session** — one psql doing COPY,
  VACUUM, `FOR UPDATE`, TRUNCATE, SAVEPOINT, index inserts — then no
  `MultiXactIdCreate` path is reachable (row 1 of the trigger table), no
  `RM_MULTIXACT_ID` record is written, and S16–S23 + S25 are sufficient. S24 can
  be deferred behind a **precise re-arm trigger**: *"the first time an S28 variant
  runs two concurrent sessions, or any FK-bearing workload with concurrent child
  inserts."*
- If S28 uses **two concurrent `FOR SHARE` sessions or concurrent FK child
  inserts**, S24 is mandatory — goopg will refuse to start on the directory.

**Recommendation: keep S28 single-session for the first landing and defer S24**,
then add a second S28 variant (`_concurrent`) that deliberately produces a
multixact and is expected to fail until S24 lands — an executable re-arm trigger
rather than a prose one. Defects (a) and (b) are ledgered and fixed independently
and are *not* gated on this choice. **Whichever way it goes, record the decision
explicitly in the milestone plan — this must not be settled by omission**, because
"the E2E happened not to be concurrent" is indistinguishable from "multixact is
handled" in a green test run.

## Guards

1. Round-trip unit tests on the new offsets/members SLRU: write `n` multixacts
   with 1..8 members each across a page and a segment boundary, reopen from disk,
   assert every member set resolves byte-identically. Include a set whose members
   straddle the offsets page boundary (2048 ids) and the members page boundary
   (1636 members).
2. Byte-level fixture test against a real PG `pg_multixact/offsets/0000` and
   `members/0000` captured via `internal/testutil/pgcluster` after two concurrent
   `FOR SHARE` sessions: goopg's reader resolves the same member sets PG's
   `pg_get_multixact_members()` reports.
3. `multixact_redo` per-opcode tests over PG-captured records, including the
   `pre_initialized_offsets_page` interaction: a `CREATE_ID` that implicitly
   initialises page *N+1* followed by a `ZERO_OFF_PAGE` for *N+1* must leave the
   page's contents intact, not re-zeroed.
4. Shared-SLRU refactor: `internal/mvcc` CLOG and subtrans suites stay green
   unchanged after being ported onto the extracted abstraction — that is the
   regression bar for the extraction itself.
5. `NewStoreAt` is actually called: a test that a `pg_control` with
   `nextMulti = k` yields `Store.Next() == k` after `Open()` (today
   `cmd/goopg/main.go:560` hardcodes `NewStore()`).
6. Defect (a): a goopg-only crash test — session A `FOR KEY SHARE`, session B
   no-key `UPDATE` (producing an updater-bearing multi), `kill -9`, restart,
   assert the row is visible with the right value. **Expected to fail before the
   emit-side fix**; it is the reproducer for the ledger row.
7. Defect (b): an unresolvable multi xmax raises a diagnosable error rather than
   silently returning "invisible" from `visibility.go`.
8. If S24 lands: the S28 `_concurrent` reverse-crash E2E starts goopg cleanly on a
   PG directory crashed mid-`FOR SHARE`, and every committed row matches the
   pre-kill capture.
9. UNITS + SMOKE green.

## References

- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
  §"Theme F" S24 — the decomposition this doc expands
- `docs/design/0118-0002-multixact-tuple-lock-subsystem.md` — the master
  MultiXact doc. Its status line (lines 3-7) lists *"WAL persistence + spec
  promotion deferred"*, item 4 of §"Remaining" (`:1256-1265`) states the
  updater-bearing case and `pg_multixact` SLRU parity *"need a real record +
  `Store` seeding from `pg_control.nextMulti`"*, and `:1292-1293` records
  `multixact-no-forget` as blocked on exactly that. S24 is that item.
- `docs/design/0131-0015-pg-wal-opcode-coverage.md` — S21a's
  `XLOG_HEAP2_LOCK_UPDATED` can carry a MultiXactId xmax and lands here
- `docs/design/0117-0006-clog-slru-buffer-pool.md` — the CLOG buffer pool this
  slice must generalise rather than copy
- `postgres/src/include/access/multixact.h:68-71` (opcodes), `:155` (`multixact_redo`)
- `postgres/src/backend/access/transam/multixact.c:110` (offsets geometry),
  `:148-156` (members geometry), `:383`/`:969`/`:3500`/`:3539`
  (`pre_initialized_offsets_page`), `:3481-3610` (`multixact_redo`)
- `postgres/src/backend/access/heap/heapam.c:5324-5589` (`compute_new_xmax_infomask`),
  `:7009` (`FreezeMultiXactId`)
- memory: `goopg_dml_conflict_no_fifo_tuple_lock`, `m0118_0005_fk_group_closed`,
  `pattern_sibling_paths_must_agree`
