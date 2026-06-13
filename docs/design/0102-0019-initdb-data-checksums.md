# 0102-0019 — Data-page checksum engine + initdb `--data-checksums` (M0102-0010)

Status: accepted (infrastructure); user-facing `--data-checksums` option deferred

## Context

`M0102-0010` tracks closing the gap between goopg's `init` command and
upstream `initdb`'s option set. The last remaining option is
`-k`/`--data-checksums` (and its negation `--no-data-checksums`). PostgreSQL
18 defaults data-page checksums **on**; `001_initdb.pl` asserts
`pg_controldata` reports `Data page checksum version: 1` by default and `0`
with `--no-data-checksums`.

A faithful implementation is **not** "write `1` into pg_control's
`data_checksum_version`". That field is a promise: every 8 KiB data page on
disk carries a valid `pd_checksum` (an FNV-1a variant over the page with the
block number mixed in), and any reader — including a PostgreSQL standby
streaming from goopg — verifies it on read and **fails the read** on
mismatch. Faking the control-file field while leaving pages unchecksummed
produces a cluster that fails verification on its very first catalog read.

So data-page checksums are a whole-engine feature touching the hottest path
in the system (every page read and write). This design splits it into a
**reusable engine layer** (landed here) and the **initdb user-facing
enablement** (deferred — see "Deferred" below).

## Upstream reference

- Checksum algorithm: `postgres/src/include/storage/checksum_impl.h`
  (`pg_checksum_block`, `pg_checksum_page`, `N_SUMS`=32, FNV prime,
  `g_checksum_base_offsets`).
- Page set/verify: `postgres/src/backend/storage/page/bufpage.c`
  (`PageSetChecksumCopy`, `PageSetChecksumInplace`, `PageIsVerifiedExtended`).
- Control field: `postgres/src/include/catalog/pg_control.h`
  (`ControlFileData.data_checksum_version`, `PG_DATA_CHECKSUM_VERSION`=1).
- initdb option: `postgres/src/bin/initdb/initdb.c` (`-k`, `data_checksums`).
- Read-side verify + `ignore_checksum_failure`:
  `postgres/src/backend/storage/buffer/bufmgr.c` (`ReadBuffer_common`,
  `PageIsVerifiedExtended` → "page verification failed ... invalid page in
  block %u of relation %s", `ERRCODE_DATA_CORRUPTED`).

## What landed this loop (engine layer)

### 1. Checksum primitive — `internal/storage/checksum.go`

- `PageChecksum(page, blkno) uint16` — port of `pg_checksum_page` (the
  FNV-1a block checksum, block number mixed in, reduced to a never-zero
  uint16). Already present from a prior loop; unit-tested for determinism,
  pd_checksum exclusion, and sensitivity to data / block-number / pd_flags.
- `PageSetChecksumCopy(page, blkno) []byte` — returns a fresh copy of the
  page with `pd_checksum` set, leaving the caller's (shared, possibly
  concurrently-read) buffer untouched. A new/all-zero page is returned
  verbatim (new pages carry no checksum).
- `VerifyPage(page, blkno) bool` — the checksum half of
  `PageIsVerifiedExtended`; an all-zero (new) page is always valid.

### 2. Storage Manager wiring — `internal/storage/smgr.go`

`ManagerConfig` gains `ChecksumsEnabled`, `IgnoreChecksumFailure`, and
`OnChecksumFailure`. The checksum logic lives at the **`relFile` level**, the
single lowest seam that both the synchronous path (`readBlock`/`writeBlock`/
`extend`/`extendBatch`) and the AIO engine path (`ReadAt`/`WriteAt`) funnel
through, and where the block number is always known (notably `extend`
assigns it internally):

- **Write** (`writeBlock`, `extend`, `extendBatch`, engine `WriteAt`): when
  enabled, the bytes written are a checksummed copy (`PageSetChecksumCopy`,
  or per-block `SetChecksum` in `extendBatch`'s freshly-built batch buffer).
  The caller's buffer is never mutated.
- **Read** (`readBlock`, engine `ReadAt`): when enabled, the page is verified
  after the read. On mismatch `OnChecksumFailure` fires and a
  `*ChecksumError` is returned, unless `IgnoreChecksumFailure` downgrades it
  to a non-fatal event (mirrors the `ignore_checksum_failure` GUC).

When `ChecksumsEnabled` is false (the default) the path is **byte-identical**
to before: a single bool check at the top of each method, no copy, no verify,
no allocation. Verified by `TestChecksumDisabledIsByteIdentical` and the
TPC-H Q12/Q13 spot-check (Q12=2 / Q13=33, unchanged).

### 3. Control-file plumbing — `internal/control/pgcontrol.go`

`ControlFileData` exposes `DataChecksumVersion` (offset 252), decoded on read
and preserved on the decode→mutate→encode `UpdateControlFile` round-trip. The
initdb writer (`internal/initdb/pgcontrol.go` `buildPgControl`/`writePgControl`)
threads a `dataChecksums bool` and writes `1`/`0` into offset 252.

### 4. Runtime / recovery enablement

- `internal/initdb/open.go`: `Open` reads `data_checksum_version` from
  pg_control **before** constructing the storage Manager and sets
  `ChecksumsEnabled` accordingly. A version-0 cluster (the goopg default)
  gets the byte-identical fast path; a version-1 cluster gets read-verify +
  write-checksum for free.
- `internal/wal/recovery.go`: `ReplayFromDir` reads the same field so FPI
  replay rewrites pages with valid checksums on a checksummed cluster. (The
  runtime uses `ReplayFromDirWithMgr` with the already-configured Manager.)

## Deferred — user-facing `--data-checksums` (remaining M0102-0010 work)

`Options.DataChecksums` exists as the seam, but **`Init` rejects it** with
"data-page checksums are not yet supported". Reason: a bootable checksummed
cluster needs `pd_checksum` set on **every** page the bootstrap writes, and
the bootstrap has **~38 distinct direct `os.WriteFile` page-write sites with
no shared helper**:

- `internal/initdb/initdb.go`: `bootstrapSharedCatalogPlaceholders`,
  `bootstrapMappedLocalCatalogHeaps`, `bootstrapPostgresRoleWithPassword`,
  `writeMultiPageHeapRows` (pg_proc/pg_operator/… multi-page heaps),
  `bootstrapPostgresDatabase` (pg_database + ~50 btree placeholders).
- `internal/initdb/btree_index_bootstrap.go`: ~30 per-index bootstrappers
  (each writes a metapage + leaf page(s)).
- (`bootstrapSystemCatalogs` already writes via the Manager — pg_type /
  pg_class / pg_attribute would be checksummed automatically.)

Missing even one site yields a cluster whose pg_control claims checksums
while a catalog page carries none → verification failure on first read.
Doing this correctly and **exhaustively** — ideally by routing the direct
writers through a single checksum-aware helper, plus an end-to-end test that
inits with `--data-checksums` and reads every catalog relation's block 0
through a checksummed Manager — is its own loop. The PG-18 default-ON parity
(and the `001_initdb.pl` default-version-1 assertion) ride on that same work.

## Testing

- `internal/storage/checksum_test.go` — primitive properties (existing).
- `internal/storage/checksum_io_test.go` (new) — Manager round-trip with
  checksums on (Extend→ReadBlock verifies clean, caller buffer not mutated),
  corruption detection (`*ChecksumError`), `IgnoreChecksumFailure` +
  `OnChecksumFailure`, disabled = byte-identical, `ExtendBatch` per-block
  checksums, new-page skip.
- `internal/initdb/data_checksums_test.go` (new) — `buildPgControl` writes
  version 1/0; `Init` rejects `DataChecksums=true` before creating the dir.
- Gates: `go build ./...`, `go vet`, `gofmt`, full `internal/initdb`,
  `go test -race ./internal/storage ./internal/wal`, TPC-H Q12/Q13 spot-check.
