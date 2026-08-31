# 0131-0011 — Move `HEAP_HOT_UPDATED` / `HEAP_ONLY_TUPLE` into `t_infomask2`

**Milestone:** M0131 — Bidirectional cluster-directory cold-start + real-PG
system-view hosting
**Task:** M0131-S11 (filed 2026-08-11 by M0131-S3, which measured the gap)
**Status:** accepted — landed 2026-08-11
**Related:** `0131-0003-reverse-coldstart-e2e.md` §Findings (the measurement),
`0130-0002-pg-class-heap-persistence.md` (the reverse-path programme)

## The defect

PostgreSQL keeps both HOT bits in the tuple header's **`t_infomask2`** field:

- `HeapTupleHeaderSetHotUpdated` writes `tup->t_infomask2 |= HEAP_HOT_UPDATED`
  — `postgres/src/include/access/htup_details.h:550`, read back at `:542`.
- `HeapTupleHeaderSetHeapOnly` writes `tup->t_infomask2 |= HEAP_ONLY_TUPLE`
  — `:568`, read back at `:562`.

goopg defined the identical bit *values* (`0x4000` / `0x8000`,
`internal/storage/heap.go`) but wrote and read them against **`t_infomask`**.
Both fields exist and are encoded on disk in PG's layout — `t_infomask2` at
tuple offset `[18:20]`, `t_infomask` at `[20:22]` — so the two engines wrote
structurally valid pages that the other engine misread.

### Reverse direction (goopg reads a PG page) — measured

`TestE2E_GoopgColdStartOnPGDataDir` (M0131-S3) HOT-updates row `id = 9` from an
upstream backend, stops PG cleanly, and serves the same `$PGDATA` from goopg.
`followHOTChain` (`internal/executor/operators_index.go:54`) tested
`Infomask & HeapHotUpdated == 0` on the chain root, saw zero because PG had put
the bit in the *other* field, declared the chain ended, and **both** index scans
(primary key and the `label` index) returned zero rows — while a sequential scan
over the same page, which never consults the flag, returned the row correctly.
The un-UPDATEd row `id = 7` resolved through the same index, proving the miss was
HOT-specific and not a failure to read PG's btree at all.

### Forward direction (a hosted PG reads a goopg page) — by inspection, worse

In PG's `t_infomask`, `0x4000` and `0x8000` are `HEAP_MOVED_OFF` and
`HEAP_MOVED_IN`. `postgres/src/backend/access/heap/heapam_visibility.c:183` and
`:202` branch on exactly those bits to derive visibility from `t_xvac` — the
pre-9.0 binary-upgrade path. A PG backend reading a goopg HOT chain therefore
does not merely lose rows; it decides visibility from a field goopg never
initialises. This is a blocker for **M0131-S4**, whose workload contains
`UPDATE`.

## The change

The bits move to `t_infomask2` and every access goes through four accessors on
`storage.HeapTupleHeader`, so the encode and decode siblings cannot drift apart
again (Hard-won Rule #2):

| accessor | upstream counterpart |
|---|---|
| `IsHotUpdated()` | `HeapTupleHeaderIsHotUpdated` (`htup_details.h:542`) |
| `SetHotUpdated()` | `HeapTupleHeaderSetHotUpdated` (`:550`) |
| `IsHeapOnly()` | `HeapTupleHeaderIsHeapOnly` (`:562`) |
| `SetHeapOnly()` | `HeapTupleHeaderSetHeapOnly` (`:568`) |

`IsHotUpdated` omits upstream's xmin/xmax hint screening; the one caller that
needs it (`internal/amcheck`) applies it locally, exactly as before.

**Writers (S11.1).** `storage.PageStampHotOldTuple` and
`storage.PageStampHotOldTupleMulti` are raw-page writers: each now
read-modify-writes `p[off+18:off+20]` for the HOT bit and leaves the
xmax-classification bits in `p[off+20:off+22]`. The multi variant already
touched `t_infomask2` for `HEAP_KEYS_UPDATED`, so the HOT bit joins that
statement. `internal/executor/operators_storage.go` (the HOT new-version
encoder) calls `SetHeapOnly()`. **No third writer exists**: WAL replay reaches
the page through the same two `storage` functions
(`internal/wal/recovery.go:2896`, `:4277`), for both goopg-native and
PG-format `xl_heap_update` records, so replay moved with them.

**Readers (S11.2/S11.3).** `followHOTChain` and `followHOTChainNoCopy` and the
dead-to-all chain walk (`operators_index.go:54`, `:97`, `:150`);
`storage.PageVacuumPrune`'s HOT-only-vs-chain-root decision
(`internal/storage/prune.go:160-163`); the upsert arbiter's HOT follow
(`operators_upsert.go:705`); `isConcurrentlyUpdated` (`operators_storage.go`);
and `internal/amcheck/verify_heapam.go`, which inspects raw page bytes and so
gained a `readInfomask2` beside `readInfomask`.

`SetNatts` and the `HeapNattsMask` (`0x07FF`) readers are unaffected — the two
bits sit far above the natts field, and every existing `t_infomask2` write in
the tree is a read-modify-write that preserves the rest of the field. That was
verified, not assumed.

**Test lock-in (S11.5).** `TestE2E_GoopgColdStartOnPGDataDir`'s inverted block
— which asserted that both index scans return **zero** rows and instructed the
S11 loop to invert it — is now the positive assertion: both
`WHERE id = 9` and `WHERE label = 'label-9'` must return the PG-written row
contents. The `id = 7` control assertion stays, so a future regression can still
be told apart from a wholesale btree-read failure.

Unit tests that stamped the bits by hand were moved to the accessors rather than
to the new field name, so they follow the field if it ever moves again. The
`internal/amcheck` test helper `setInfomask` splits the two HOT bits out to
`t_infomask2` internally, leaving its ~20 call sites untouched.

## Scope limit — no in-place upgrade

Every page every existing goopg `$PGDATA` has already written carries the bits
in the wrong field. This change does not migrate them, and there is no upgrade
path: after it lands, a HOT chain written by an older goopg reads as a chain end
(rows visible by seq scan, missing by index scan) and a HOT-only successor reads
as an ordinary tuple to the pruner. **The bench clusters (65433 / 65436 / 65437)
and any operator directory need a reload**, exactly like the nbtree breaks under
M0130-0011. A ledger row records this; the M0131 gates all `initdb` fresh, so
nothing in CI catches it.

## Gates

- `internal/storage`, `internal/executor`, `internal/amcheck`, `internal/wal`
  unit suites.
- `TestE2E_GoopgColdStartOnPGDataDir` — the measurement that filed the task,
  now asserting the closed gap; and the whole `^TestE2E_` family.
- `scripts/tpch-spotcheck.sh` — heap/visibility change, Hard-won Rule #1.
- `RALPH_PRECOMMIT_SCOPE=units` + the mandatory pgbench smoke.

## What this does not close

`heapam_visibility.c`'s `HEAP_MOVED_OFF`/`HEAP_MOVED_IN` path is now merely
unreachable on goopg pages rather than mis-triggered; goopg still never writes
`t_xvac` and has no `VACUUM FULL`-with-move code, which is correct for PG ≥ 9.0
but means the forward-direction claim above is established by inspection of
upstream, not by a hosted-PG measurement. **M0131-S4** is the measurement, and
it is now unblocked.
