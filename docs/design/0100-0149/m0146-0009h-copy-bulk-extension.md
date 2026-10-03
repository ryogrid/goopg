# M0146-0009h — COPY FROM extends the relation through a BulkInsertState

Status: done (2026-10-04, `df1274b81`). Parent: M0146-0009.

## The divergence

The two TPC-DS SF0.25 loads hold nearly the same tuples, but PG's relations
end in empty pages:

| table | PG data pages + empty | PG relpages | goopg relpages before |
|---|---|---|---|
| item | 1238 + 46 | 1284 | 1242 |
| date\_dim | 1405 + 19 | 1424 | 1405 |
| store\_sales | 12933 + 3 | 12936 | — |

Those pages are the whole parallel seq-scan cost gap on Q42/Q52.

## PG behaviour

`CopyFrom` (`./postgres/src/backend/commands/copyfrom.c`) inserts through
one `BulkInsertState` per target relation for the whole statement. The
state's pin is released only when the target partition changes.

- **CIM\_MULTI.** Rows are buffered in a `CopyMultiInsertBuffer`, which is
  flushed once it holds `MAX_BUFFERED_TUPLES` (1000) rows or
  `MAX_BUFFERED_BYTES` (65535) bytes of input lines (`line_buf.len`, the
  line without its end).
  - `heap_multi_insert` (heapam.c) prepares the whole batch first.
  - It then fills pages in order. Each time a tuple needs a new page it asks
    `RelationGetBufferForTuple` for one, passing `num_pages` as
    `heap_multi_insert_pages` of the rest of the batch.
- **CIM\_SINGLE.** A BEFORE or INSTEAD OF row insert trigger, or a volatile
  default other than `nextval`, forces single-row mode. That mode inserts
  through the same kind of state, one page at a time.
- **Page choice with a bistate** (`RelationGetBufferForTuple`, hio.c):
  1. the current page;
  2. the pages the last bulk extension left unused, `next_free` to
     `last_free`;
  3. the FSM;
  4. `RelationAddBlocks`.
- **`RelationAddBlocks`** extends by `num_pages`, raised to
  `already_extended_by` and capped at 64.
  - It keeps the extra pages as `next_free`.
  - It enters the pages beyond `num_pages` into the FSM.
  - The pages the load never reaches stay empty at the end of the relation.
- **`VACUUM`** gives such a tail back only if `should_attempt_truncation`
  (vacuumlazy.c) passes: at least `REL_TRUNCATE_MINIMUM` (1000) empty pages,
  or at least 1/`REL_TRUNCATE_FRACTION` (1/16) of the relation.

## Change

- **Writer split.** `writeHeapRowReturning` is now `prepareHeapTuple`
  (TOAST, encode and stamp: `heap_prepare_insert`) followed by
  `placeHeapTuple` (page choice).
- **`bulk_insert.go`.** `bulkInsertState` holds `current_buf`, `next_free`,
  `last_free` and `already_extended_by`, plus the batch's page plan
  (`npages`, `npages_used`, `starting_with_empty_page`). Its methods:
  - `place` and `getBuffer`: the page-choice order above;
  - `extend`: `RelationAddBlocks`;
  - `heapMultiInsertPages`;
  - `copyUsesMultiInsert`: the CIM\_MULTI test.

  `placeHeapTuple` uses this path when `ctx.bulkInsert` is set for its
  relation.
- **FSM requests.** These round up to the FSM's 32-byte category
  (`FSM_CAT_STEP`). goopg's FSM stores exact `pd_upper - pd_lower`, so the
  query `roundup32(need) + line pointer` asks exactly what PG's category
  search asks.
- **`CopyFromExecutor`.**
  - It buffers and flushes batches. `Finish`, and the binary trailer, flush
    the last batch.
  - It counts a buffered row as inserted when it accepts it.
  - In CIM\_SINGLE mode it writes through the same state.
  - Table sync calls `Finish` on any writer that has one.
- **`VACUUM`.**
  - It applies `should_attempt_truncation`. Before, it truncated any empty
    tail.
  - A truncation now drops the removed blocks from the FSM
    (`FSM.TruncateRel`, as `FreeSpaceMapPrepareTruncateRel`). Before, the
    stale entries sent the next insert past EOF ("short read at block").
    pgbench's load runs `COPY` then `VACUUM ANALYZE`, so it hit this on
    every run once loads ended in a tail.

## Verified against PG 18.3

Fresh COPY loads of the TPC-DS SF0.25 files, relation pages:

| table | PG | goopg now |
|---|---|---|
| item | 1284 | 1284 |
| date\_dim | 1424 | 1424 |
| customer | 2872 | 2872 |
| store\_sales | 12936 | 12936 |
| pgbench\_accounts (scale 1) | 1640 | 1640 |

Unit fixture (20000 rows): multi-insert 208 pages, single-row 256. After
`VACUUM`, PG keeps 208 and truncates 256 to 200; goopg does the same.

Tests:

- `TestCopyBulkExtensionMatchesPGRelpages`: fails before, with 200 and 200
  pages.
- `TestVacuumTailTruncationThreshold`: also fails without the FSM
  truncation.

Plan inputs on the bench clusters change only after the owner reloads them;
the loop cannot. Until then the fire set cannot show movement.

## Not done (ledgered)

- **Data pages.** `item` fills 1242 data pages on goopg against PG's 1238,
  so some goopg tuples are longer. `pg_column_size` cannot show where:
  goopg's omits the varlena header (181 where PG reports 185).
- **FSM order.** goopg's FSM returns the lowest qualifying block, while PG's
  `fsm_search` starts at `fp_next_slot`.
- **No extension-lock waiters.** goopg counts none, so it never applies the
  `extend_by_pages × waitcount` multiplier.
- **No `TABLE_INSERT_SKIP_FSM`.** PG sets it for a relation created or
  truncated in the same transaction (wal\_level minimal); goopg doesn't.
- **Error context.** A flush error does not report the failing row's line
  (`CopyMultiInsertBufferFlush` sets `cur_lineno` per buffered row).
- **VACUUM `hastup`.** goopg's VACUUM counts a page holding only dead line
  pointers as non-empty. A deleted-out tail is therefore not truncated in
  the same pass, where PG's `hastup` ignores LP\_DEAD items.

Found while measuring, filed as S2 wrong-results tasks:

| task | defect |
|---|---|
| M0146-0051 | ctid is lost through a parallel plan: `count(DISTINCT ctid)` returns 0 |
| M0146-0052 | `ORDER BY ctid DESC` returns ascending order |
| M0146-0053 | `'(999,9)'::text > '(1241,10)'::text` is false |
| M0146-0054 | COPY leaves a column NULL when it cannot evaluate the column's volatile default |
| M0146-0055 | COPY fires no BEFORE ROW triggers |
