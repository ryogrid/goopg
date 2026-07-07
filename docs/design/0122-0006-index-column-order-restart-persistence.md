# Index column order (ASC/DESC/NULLS) restart persistence (M0122-0006)

status: accepted
date: 2026-07-08
supersedes: none

## Source

`.ralph/fix_plan.md` M0122-0006 ("On-disk catalog persistence & shared
catalogs"), bullet "index column order (ASC/DESC/NULLS) across restart".

## Problem

`CREATE INDEX ... (col DESC, col2 ASC NULLS FIRST)` records the per-column
ordering on `catalog.Index.ColDescending`/`ColNullsFirst` (parallel to
`Columns`) for the live session, but three independent gaps meant it never
survived past that session:

1. **Live `pg_index.indoption` was always zero.** `pg_index` is a `Virtual`
   catalog table (`internal/catalog/catalog.go`); its `VirtualRows` builder
   rendered `indoption` via a hardcoded all-zero vector
   (`buildZeroVec(nkeyatts)`) regardless of the index's declared ordering.
   Any direct `SELECT indoption FROM pg_index` (e.g. tooling, pg_dump's own
   catalog queries) got the wrong answer even in the same session that ran
   the `CREATE INDEX`.
2. **The heap-persisted `pg_index` shadow row never carried it either.**
   `buildUserPGIndexRow` (`internal/executor/pg18_user_catalog_rows.go`),
   which writes the row `loadUserIndexesFromHeap` reads back after a
   *checkpointed* restart, also hardcoded a zero `indoption` int2vector.
   `catalog.DecodePGIndexPhysicalRow` (`internal/catalog/codec.go`) didn't
   even have a field to decode it into — the physical row's `indoption`
   varlena (which follows `indkey`/`indcollation`/`indclass`, all
   4-byte-aligned varlena columns) was simply never reached.
3. **The CREATE INDEX WAL record raced its own catalog mutation.** Even
   after fixing (1) and (2), a genuine *uncheckpointed* crash restart
   (`SIGKILL` with no intervening checkpoint — the actual crash-recovery
   path, not `Runtime.Close()`'s graceful shutdown checkpoint) still lost
   the ordering. `createBTreeIndex` (`internal/executor/operators_ddl.go`)
   builds a fresh `*catalog.Index` via `catalog.CreateIndex(...)` and
   immediately emits its `wal.RecordKindCreateIndex` WAL record — but
   `execCreateIndex`, its only caller with per-column ordering information,
   set `idx.ColDescending`/`ColNullsFirst` in a **separate block that ran
   after `createBTreeIndex` had already returned** (a pre-existing
   "create now, patch later" pattern documented at the same call site for
   `Fillfactor`/`ColOpClasses`/`ColCollations`/predicate/INCLUDE columns,
   all of which get **re-synced to the heap row afterward** via
   `resyncIndexHeapRow` — but nothing re-emits or corrects the already-sent
   WAL record). The result: the in-memory `idx` object and the
   checkpoint-refreshed heap row both end up correct within the same
   session, but the WAL record — the only thing a true crash-restart can
   replay from — was serialized with `ColDescending`/`ColNullsFirst` still
   nil.

(1) and (3) were reproduced live against the real `cmd/goopg` binary; (3)
specifically required a true `kill -9` with no checkpoint (verified via the
`TestCrashRecoveryReplaysWALAfterUncleanShutdown` pattern — flush WAL, close
WAL + StorageMgr directly, skip `Pool.Close`) since a plain `Runtime.Close()`
in a test performs a synchronous shutdown checkpoint (M0089-0002) that
flushes the heap row and masks the WAL-only bug.

## Change

**Live pg_index virtual builder** (`internal/catalog/catalog.go`,
`pgIndexCatalog.VirtualRows`): compute `indoption` per key column from
`idx.ColDescending`/`ColNullsFirst` (`INDOPTION_DESC=0x1`,
`INDOPTION_NULLS_FIRST=0x2`, matching `postgres/src/include/catalog/pg_index.h`)
instead of `buildZeroVec`.

**Heap-persisted pg_index row, write side**
(`internal/executor/pg18_user_catalog_rows.go`, `buildUserPGIndexRow`): same
bitmask computation, replacing the hardcoded `zeros16` for the `indoption`
column.

**Heap-persisted pg_index row, read side** (`internal/catalog/codec.go`):
`PGIndexRow` gains `IndOption []int16`; `DecodePGIndexPhysicalRow` walks
past the `indkey` varlena (already decoded) through two more 4-byte-aligned
varlena columns (`indcollation`, `indclass` — contents not needed) via new
`pgIndexAlign4`/`pgVarlenaTotalLen` helpers, then decodes `indoption`. A
decode failure at any of these new steps degrades to "no `IndOption`"
rather than a hard error, since the fields already decoded remain valid on
their own.

**`loadUserIndexesFromHeap`** (`internal/initdb/open.go`): carries
`indOption []int16` through the recovered-row struct, and builds parallel
`colDescending`/`colNullsFirst []bool` slices in lockstep with the existing
attnum → column-name filter loop (so a filtered-out `attnum` can't
desynchronize the two).

**`catalog.InMemory.RegisterIndexDuringRecovery`**: gains
`colDescending, colNullsFirst []bool` parameters, set on the freshly
constructed `*Index`. `indexRegistryRecovery` (the recovery-hook interface
in `internal/initdb/index_ddl_recovery.go`) and its `loadUserIndexesFromHeap`
call site updated to match.

**`wal.CreateIndexPayload`** (`internal/wal/recovery.go`) gains
`ColDescending`/`ColNullsFirst []bool`. `EncodeCreateIndex` appends them as
**two trailing `numCols`-byte blocks after the column-name list**,
deliberately *not* interleaved with each column's bytes — this keeps the
format append-only, so a pre-existing on-disk WAL record (predating this
field, with no trailing blocks at all) still decodes correctly.
`DecodeCreateIndex` distinguishes the two valid shapes explicitly: exactly
`0` trailing bytes (old record; every column defaults to ascending/NULLS
LAST, its true pre-M0122-0006 behavior) or exactly `2*numCols` trailing
bytes (new record); anything else is a genuine truncation error. This
backward-compatibility requirement was discovered empirically — an
interleaved-bytes first attempt broke `scripts/tpch-spotcheck.sh` against
`bench/tpch/runtime_goopg/data`'s pre-existing WAL history
(`wal: create-index payload truncated at column 0 body`) on server start.

**The actual crash-durability fix — reordering, not just plumbing**
(`internal/executor/operators_ddl.go`): `createBTreeIndex` gains
`colDescending, colNullsFirst []bool` parameters (positioned before the
existing variadic `predExpr ...planner.Expr`) and sets
`idx.ColDescending`/`ColNullsFirst` on the freshly created `*catalog.Index`
**immediately after `catalog.CreateIndex` returns, before the WAL-emission
block later in the same function**. `execCreateIndex` now computes
`colDescending`/`colNullsFirst` from `s.ColOrders` *before* calling
`createBTreeIndex` (previously computed in the post-call resync block) and
passes them in; the post-call block's now-redundant re-assignment of
`idx.ColDescending`/`ColNullsFirst` is removed (every other field it
patches — predicate, INCLUDE columns, fillfactor, opclass, collation — is
untouched, see Non-goals). The 15 other `createBTreeIndex` call sites pass
`nil, nil` (no ordering to propagate) except the two that build a child
index from an already-fully-populated parent
(`createPartitionChildIndexes`, `ATTACH PARTITION`'s index-child path),
which now propagate `parentIdx.ColDescending`/`ColNullsFirst` for the same
reason `parentIdx.Columns` is already propagated.

## Non-goals / known remaining gap (deferred)

Discovered but explicitly out of scope for this task, recorded in
`.ralph/deferral_ledger.md`: `wal.CreateIndexPayload` still does **not**
carry `HasPredicate`/`Predicate`/`IncludeColumns`/`ColOpClasses`/
`ColCollations`/`Fillfactor`/`DeduplicateItems` — none of these were ever
part of the WAL payload, only the heap-row resync path
(`resyncIndexClassHeapRow`/`resyncIndexHeapRow`, M0119-0004). They round-trip
correctly across a *checkpointed* restart (heap row wins the
`RegisterIndexDuringRecovery` dedup race) but — by the exact same mechanism
diagnosed above for `ColDescending`/`ColNullsFirst` — silently revert to
defaults across a genuine uncheckpointed crash restart. Fixing this
properly means extending the WAL payload (or the whole "create now, patch
later" pattern) for every one of those fields; scoped out here to keep this
change to the specific milestone item (index column ordering).

## Tests

- `internal/executor/pg18_user_catalog_rows_test.go`,
  `TestBuildUserPGIndexRowIndoptionRoundTrip`: `buildUserPGIndexRow` →
  `EncodeRowPG` → `catalog.DecodePGIndexPhysicalRow` round-trip for a
  3-column index with mixed ordering. Confirmed non-vacuous via `git stash`
  on `pg18_user_catalog_rows.go` alone.
- `internal/wal/index_ddl_test.go`:
  - `TestEncodeCreateIndexRoundTrip` extended with a `ColDescending`/
    `ColNullsFirst` case.
  - `TestDecodeCreateIndexBackwardCompatNoOrderBlocks` (new): a payload
    truncated to the pre-M0122-0006 shape (no trailing blocks) still
    decodes, defaulting every column to ascending/NULLS LAST.
  - `TestDecodeCreateIndexRejectsTruncated` updated to skip the one cut
    point that is — by construction — indistinguishable from a legitimate
    old-format record (documented in the test).
- `internal/initdb/index_ddl_recovery_test.go`,
  `TestCreateIndexColumnOrderingSurvivesRestartViaWAL`: uses the
  `TestCrashRecoveryReplaysWALAfterUncleanShutdown` true-crash pattern
  (flush WAL, close WAL + StorageMgr directly, skip `Pool.Close`) —
  deliberately *not* a plain `rt1.Close()`, since that performs a shutdown
  checkpoint that would let the heap-recovery path mask a WAL-only bug (as
  it did during development of this fix). Confirmed non-vacuous: failed
  with `ColDescending=[false false]` before the `operators_ddl.go`
  reordering fix landed, passes after.
- Live end-to-end verification against the real `cmd/goopg` binary (not
  automated, per project convention for scenarios without an existing
  test seam): `CREATE UNIQUE INDEX ... (a DESC NULLS LAST, b ASC NULLS
  FIRST)`, `kill -9` with no checkpoint, restart — `pg_get_indexdef` and
  `SELECT indoption FROM pg_index` both correct post-restart
  (`indoption = 1 2`, matching `INDOPTION_DESC` on column `a` and
  `INDOPTION_NULLS_FIRST` on column `b`).

## Gates run

- `go build ./...` / `go vet ./...` clean.
- `go test ./internal/wal/... ./internal/initdb/... ./internal/catalog/...
  ./internal/executor/... ./internal/planner/... ./internal/analyzer/...
  ./internal/parser/...` PASS.
- `go test -short ./...` (excluding `internal/testutil/tpch` and
  `internal/testport`) PASS, no regressions.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33) — also the regression
  test for the WAL backward-compatibility fix (failed before it, against
  `bench/tpch/runtime_goopg/data`'s pre-existing WAL history).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS
  (0 failed transactions, all 3 workloads).
