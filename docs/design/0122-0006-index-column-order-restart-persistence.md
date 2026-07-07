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

## Follow-up (2026-07-08): index properties beyond ordering

The original fix's own deferral-ledger row flagged a wider version of gap
(3): `wal.CreateIndexPayload` carried only `OID/TableOID/Schema/Name/
Method/Columns/Unique/Primary/ColDescending/ColNullsFirst` — a partial
index's `WHERE` predicate, `INCLUDE` columns, per-column opclass/
collation overrides, `WITH (fillfactor=…)`, `WITH (deduplicate_items=…)`,
and `NULLS NOT DISTINCT` were all still set on `idx` in a caller's
*post-call* resync block, after `createBTreeIndex`'s WAL record had
already been built — the exact same "WAL emitted too early" shape gap
(3) above fixed for `ColDescending`/`ColNullsFirst`, just for a wider set
of fields.

### Fix

- **`wal.CreateIndexPayload`** (`internal/wal/recovery.go`) gained
  `HasPredicate`, `PredicateString`, `IncludeColumns`, `ColOpClasses`,
  `ColCollations`, `Fillfactor`, `DeduplicateItems`, `NullsNotDistinct`.
  Encoded as a new, **optional** trailing "extension" block
  (`encodeCreateIndexExtension`/`decodeCreateIndexExtension`), appended
  after the `ColDescending`/`ColNullsFirst` blocks only when at least one
  of these fields is non-default (`hasCreateIndexExtension`) — a plain
  index's WAL record stays byte-identical to before this follow-up, so
  every pre-existing backward-compat test needed no changes. `Decode`
  now accepts three payload generations: `remaining == 0` (pre-M0122-0006,
  no order blocks), `remaining == 2*numCols` (M0122-0006, order blocks
  only), `remaining > 2*numCols` (this follow-up, order blocks + a
  self-describing extension block: a flags byte, `int32` fillfactor,
  then the optional predicate string / INCLUDE list / per-column
  opclass and collation lists).
- **`createBTreeIndex`** (`internal/executor/operators_ddl.go`) gained an
  optional trailing `props *btreeIndexProps` parameter (replacing the old
  bare `predExpr ...planner.Expr` variadic — the predicate expression is
  now one field of the new struct). All 8 new properties are set on `idx`
  immediately after creation, in the same block as `ColDescending`/
  `ColNullsFirst`, strictly BEFORE the WAL record is built — the identical
  ordering discipline the original fix established. Only the two call
  sites that actually declare these properties needed updating:
  `execCreateIndex`'s direct `CREATE INDEX` path and
  `createPartitionChildIndexes` (partition-child index echo, which now
  also propagates the parent's `ColOpClasses`/`ColCollations` — a small
  pre-existing gap of its own, since nothing computed those before). The
  other 14 `createBTreeIndex` call sites (constraint-backed PK/UNIQUE/
  EXCLUDE indexes with no such properties) are unaffected — they simply
  omit the new optional argument, as they already omitted the old
  `predExpr` variadic.
- **`catalog.RegisterIndexDuringRecovery`** and the `indexRegistryRecovery`
  interface (`internal/initdb/index_ddl_recovery.go`) gained the same 8
  parameters, set directly on the newly-registered `*catalog.Index`
  literal. `replayIndexDDLRecords` (pure-WAL driver) passes the decoded
  payload's values straight through. `loadUserIndexesFromHeap`
  (heap-based driver) passes their zero values — the heap-decode side of
  this gap (pg_index's `indclass`/`indcollation`/`indexprs`/`indpred`
  content is still never decoded; `buildUserPGIndexRow` still writes them
  as all-zero/NULL) is a separate, already-known residual (see the ledger
  row below), not something this call regresses.
- `idx.Predicate` (the parsed `parser.Expr` AST, as opposed to
  `PredicateString`) is deliberately **not** threaded through WAL/
  recovery: nothing reads it again after `CREATE INDEX` finishes — the
  build-time row filter runs once, and `pg_get_indexdef`/pg_dump render
  from `PredicateString`. It remains set only for the live session (kept
  in each call site's now-much-smaller post-call block), so it is `nil`
  after a WAL-only crash-restart recovery — harmless given nothing
  consults it post-restart.

### Tests

- `internal/wal/index_ddl_test.go`: `TestEncodeCreateIndexExtensionRoundTrip`
  (new fields round-trip), `TestEncodeCreateIndexOmitsExtensionBlockWhenDefault`
  (plain index emits no extension bytes), `TestDecodeCreateIndexBackwardCompat
  OrderBlocksOnlyNoExtension` (gen1 record — order blocks, no extension —
  still decodes with every new field at zero), `TestDecodeCreateIndexRejects
  TruncatedExtension` (every cut point inside the extension block errors).
- `internal/initdb/index_ddl_recovery_test.go`,
  `TestCreateIndexExtendedPropertiesSurviveRestartViaWAL`: the same
  true-crash pattern as `TestCreateIndexColumnOrderingSurvivesRestartViaWAL`
  (flush WAL, close WAL+StorageMgr directly, skip `Pool.Close`), for
  `CREATE UNIQUE INDEX ext_idx ON ext (a, b COLLATE "C" text_pattern_ops)
  INCLUDE (c) WITH (fillfactor=70, deduplicate_items=off) NULLS NOT
  DISTINCT WHERE (a > 0)`. Confirmed non-vacuous: temporarily reverted the
  WAL payload to omit the new fields (leaving `idx.*` correctly set
  in-memory) and confirmed all 8 assertions failed as expected, then
  restored the fix.
- Live end-to-end verification against the real `cmd/goopg` binary: the
  exact statement above, `kill -9` with no checkpoint, restart —
  `pg_get_indexdef` and `pg_index.indoption`/`indnullsnotdistinct`/
  `indclass`/`indcollation` all correct post-restart.

### Gates run (this follow-up)

- `go build ./...` / `go vet ./...` clean.
- `go test ./internal/wal/... ./internal/catalog/... ./internal/executor/...
  ./internal/initdb/... ./internal/planner/... ./internal/analyzer/...
  ./internal/parser/... ./internal/server/...` PASS.
- `go test -short ./...` (excluding `internal/testutil/tpch` and
  `internal/testport`) PASS.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS
  (0 failed transactions, all 3 workloads).

### Still deferred

The heap-recovery driver's decode-side gap noted above (`pg_index`'s
`indclass`/`indcollation`/`indexprs`/`indpred` content is never decoded
from the heap row, and `PGIndexRow`/`DecodePGIndexPhysicalRow` have no
`indnullsnotdistinct` field either, so a *checkpointed* restart still
loses opclass/collation/predicate/INCLUDE/NULLS-NOT-DISTINCT — only a
genuine uncheckpointed crash is covered by this follow-up's WAL path)
remains open; see the deferral ledger for the resume point.

## Follow-up 2 (2026-07-08): checkpointed-restart heap decode

Closes 3 of the 5 fields left open by the "still deferred" note above:
predicate, INCLUDE columns, and NULLS NOT DISTINCT now survive a
*checkpointed* restart (a plain graceful `Close()`/`stop`, which flushes
the pg_index heap row and recovers via `loadUserIndexesFromHeap`, not WAL
replay). ColOpClasses/ColCollations (`indclass`/`indcollation` real OID
resolution) remain open — see "Still deferred" below.

### Fix

- **`buildUserPGIndexRow`** (`internal/executor/pg18_user_catalog_rows.go`)
  no longer hard-codes `indnkeyatts == indnatts`: it now computes
  `nkeyatts = len(idx.Columns)` and `natts = nkeyatts +
  len(idx.IncludeColumns)`, and appends the INCLUDE columns' attnums to
  `indkey` after the key columns' (mirroring real PG's `indkey` layout —
  key columns first, then INCLUDE columns). Previously INCLUDE columns
  were entirely absent from the physical `indkey` vector, so there was no
  way to reconstruct them from the heap row at all. `indpred` is now
  written as `idx.PredicateString` (as PG varlena text) when
  `idx.HasPredicate`, instead of always `NULL`. `indexprs` stays `NULL`
  unconditionally — goopg has no expression-index support
  (`catalog.Index.Columns` holds only plain column names), so no code
  path can ever populate it; this is not a residual, just an accurate
  reflection of current capability.
- **`catalog.PGIndexRow`/`DecodePGIndexPhysicalRow`**
  (`internal/catalog/codec.go`) gained `IndNKeyAtts` (decoded from the
  previously-ignored offset-10 `indnkeyatts` field), `IndNullsNotDistinct`
  (offset 13, between `indisunique`@12 and `indisprimary`@14 — the byte
  was already being skipped correctly by the existing offset arithmetic,
  just never read into the struct), and `IndHasPred`/`IndPred`. The
  `indpred` decode exploits a specific, checked invariant: `indexprs`
  (the column immediately before it) is proven always `NULL` (see above),
  and goopg's physical tuple encoder (`encodeRowPG`) writes zero bytes for
  a `NULL` column — so any bytes remaining in the tuple after `indoption`
  belong entirely to `indpred`, letting its presence be inferred from data
  length alone, with no null-bitmap plumbing needed (some callers of
  `DecodePGIndexPhysicalRow`, e.g. `operators_ddl.go`'s
  `stampCatalogRows`, only have the raw data bytes on hand, not the
  tuple's null bitmap). A new `decodePGIndexVarlenaText` helper decodes
  both the short (1-byte header) and long (4-byte header) PG varlena text
  forms, mirroring `internal/executor/codec.go`'s `varlenaTextBytes`
  encoder. **If expression-index support is ever added, this
  length-based inference breaks** and both `indexprs`/`indpred` will need
  real null-bitmap-aware decoding.
- **`loadUserIndexesFromHeap`** (`internal/initdb/open.go`) Pass 2/3 now
  carry `IndNKeyAtts`/`IndNullsNotDistinct`/`IndHasPred`/`IndPred` through
  to `RegisterIndexDuringRecovery`, and split the decoded `indkey` at
  `indnkeyatts`: the first `nkeyatts` attnums resolve to `colNames` (key
  columns, as before), the rest resolve to `IncludeColumns`.

### Tests

- `internal/initdb/index_ddl_recovery_test.go`,
  `TestCreateIndexPredicateAndIncludeColumnsSurviveCheckpointedRestart`:
  uses a *graceful* `rt1.Close()` (checkpointed restart, exercising
  `loadUserIndexesFromHeap`) for `CREATE UNIQUE INDEX ext2_idx ON ext2
  (a, b) INCLUDE (c) NULLS NOT DISTINCT WHERE (a > 0)`, asserting
  `HasPredicate`/`PredicateString`/`IncludeColumns`/`NullsNotDistinct` all
  survive. Deliberately does NOT assert `ColOpClasses`/`ColCollations`
  (still open). Confirmed non-vacuous via `git stash` on the 3 impl files:
  all 4 assertions fail with the exact pre-fix zero-value symptom.
- Live end-to-end verification against the real `cmd/goopg` binary: a
  graceful `stop`/`start` (not `kill -9`) for `CREATE UNIQUE INDEX
  ext3_idx ON ext3 (a, b) INCLUDE (c) NULLS NOT DISTINCT WHERE (a > 0)` —
  `pg_get_indexdef` and `pg_index.indnatts`/`indnkeyatts`/`indkey`/
  `indnullsnotdistinct` all correct post-restart.

### Gates run (this follow-up)

- `go build ./...` / `go vet ./...` clean.
- `go test ./internal/wal/... ./internal/catalog/... ./internal/executor/...
  ./internal/initdb/... ./internal/planner/... ./internal/analyzer/...
  ./internal/parser/... ./internal/server/...` PASS.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS
  (0 failed transactions, all 3 workloads).

### Still deferred

`ColOpClasses`/`ColCollations` (`indclass`/`indcollation` real per-column
OID resolution) remain unresolved after a checkpointed restart — and, in
fact, on the *live* (no restart) path too: `pg_index`'s `VirtualRows`
builder (`internal/catalog/catalog.go`, ~line 7660) only fills `indclass`
via a hard-coded per-Go-type-name default-opclass switch and always
zeroes `indcollation`, ignoring `idx.ColOpClasses`/`ColCollations`
entirely regardless of restart. `pg_get_indexdef`/`\d` are unaffected,
since they render from the in-memory `Index` struct's name-string fields
directly, never through `pg_index`'s numeric columns. Fixing this needs a
real opclass-name→OID and collation-name→OID resolver covering the full
builtin universe (not just the small `builtinRangeSubtypeOpclasses` set
used by range-type defaults), plus the reverse OID→name lookup, wired
into both the live `VirtualRows` builder and
`buildUserPGIndexRow`/`DecodePGIndexPhysicalRow`. See the deferral
ledger's 2026-07-08 "follow-up 2 of 2" row for the full resume point.
