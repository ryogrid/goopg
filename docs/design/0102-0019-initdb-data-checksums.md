# 0102-0019 — Data-page checksum engine + initdb `--data-checksums` (M0102-0010)

Status: accepted; user-facing `--data-checksums` enablement landed; both flip-gates (a recovery/FPI-replay, b standby-read/physical-replication) pass — default-ON flip deferred to a dedicated regress+TPC-H-gated loop

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

## User-facing `--data-checksums` enablement (landed)

A bootable checksummed cluster needs `pd_checksum` set on **every** page the
bootstrap writes. The bootstrap has **~40 distinct direct `os.WriteFile`
page-write sites** (across `initdb.go`, `btree_index_bootstrap.go`,
`pg_tablespace_bootstrap.go`, `pg_proc_proname_args_nsp_index_bootstrap.go`)
plus the Manager-written `pg_type`/`pg_class`/`pg_attribute`. Missing even one
site yields a cluster whose pg_control claims checksums while a catalog page
carries none → verification failure on first read.

### Chosen approach: one offline stamp pass, not a 40-site thread

Rather than thread the cluster's checksum flag through every one of those
scattered sites — where a single missed site is a silent unbootable-cluster
bug, and the threading churns ~140 call sites — goopg stamps checksums in **one
offline pass after all bootstrap writes complete**, exactly as upstream's
`pg_checksums --enable` tool does (`postgres/src/bin/pg_checksums`). This is the
same operation PostgreSQL itself uses to convert a live cluster to checksums;
applying it at the end of `Init` is its natural analogue.

`internal/initdb/checksum_bootstrap.go` `stampClusterChecksums(dataDir)`:

- Walks `<dataDir>/global/` and `<dataDir>/base/<db>/`.
- For each file whose name matches `relFileNamePattern`
  (`^[0-9]+(_(fsm|vm|init))?(\.[0-9]+)?$` — the goopg analogue of
  pg_checksums' `parse_filename_for_nontemp_relation`), reads it, runs every
  `BlockSize` block through `checksumRelationData(raw, true)`, and rewrites it
  in place. `os.WriteFile` preserves the existing file's mode (perm applies
  only on creation), so a later `-g/--allow-group-access` relax pass still owns
  the final mode.
- **Cluster metadata that is not a relation page file** — `PG_VERSION`,
  `pg_filenode.map`, `pg_internal.init`, `pg_control`, config files, and
  CLOG/WAL (outside base/ & global/) — is named non-numerically (or lives
  elsewhere) and so is never matched. The strict numeric-relfilenode filter is
  what makes "stamp everything" safe: it cannot corrupt a CRC-protected
  metadata file by mistaking it for pages.

`Init` calls it (guarded by `opts.DataChecksums`) right after `writePgControl`
and before the trailing fsync, so the checksummed bytes are flushed durably.
**When `opts.DataChecksums` is false (the default) the pass never runs and the
bootstrap is byte-identical to before** — the structural guard, not a
per-byte equivalence claim, is the invariant.

### Routing primitive — `checksumRelationData`

`stampClusterChecksums` is built on the loop-#30 primitive
`checksumRelationData(raw []byte, enabled bool) []byte`: a copy with
`pd_checksum` stamped into every `BlockSize` block (`storage.PageSetChecksumCopy`),
block number **derived from the block's byte offset within the file** (block N
at offset `N*BlockSize`), matching exactly how the runtime smgr verifies a
block on read (`smgr.go`: `off/BlockSize`). New (all-zero) pages are passed
through unchanged, exactly as upstream `PageIsNew` skips them. The per-block
numbering and never-mutate-input invariants are proven in isolation by
`internal/initdb/checksum_bootstrap_test.go`.

### CLI flags

`cmd/goopg`'s `init` registers `-k`/`--data-checksums` and
`--no-data-checksums` (upstream initdb's `-k` and its negation).
`--no-data-checksums` overrides `-k` and is the current goopg default.

### Remaining: default-ON flip

PG 18 defaults checksums **on** (`001_initdb.pl` asserts
`Data page checksum version: 1` by default). goopg keeps the default **off**
for now; flipping it is deferred until two validation gates pass — a checksummed
cluster must (a) replay its WAL, full-page images included, after an unclean
shutdown, and (b) stream to a PG standby cleanly.

**Gate (a) — recovery / FPI replay — DONE.**
`internal/initdb/recovery_test.go`
`TestCrashRecoveryReplaysChecksummedClusterCleanly` runs the SIGKILL /
WAL-replay sequence on a `DataChecksums=true` cluster (build a multi-page btree
→ force WAL durable → drop the Manager + WAL writer without flushing the dirty
pool → reopen, which replays) and then proves every recovered page is
checksum-valid two ways: the Phase-4 btree reads go through the checksum-enabled
Manager (a bad replayed page would surface as `*ChecksumError`, not a wrong
answer), and a Phase-5 on-disk walk re-verifies every populated block's
`pd_checksum` under the `off/BlockSize` convention. This is the architectural
proof that the FPI restore path
(`wal/recovery.go` `restoreDecodedXLogBlockImage` → `writeBlockOrExtend` →
`Manager.WriteBlock` → `checksummedForWrite`) recomputes the checksum for each
replayed block rather than writing a stale image verbatim or bypassing the
checksum write seam.

**Gate (b) — standby-read / physical replication — DONE.**
`internal/testport/e2e_checksum_replication_test.go`
`TestE2E_ChecksumStreamingGoopgToPG` validates the strongest form of the gate: a
checksummed goopg primary streaming to a **real PostgreSQL** standby that
verifies `pd_checksum` on read. This proves goopg's FNV-1a page-checksum bytes
are **byte-identical** to what upstream PG computes — the cross-implementation
compatibility gate (a) cannot cover (gate (a) only proves goopg verifies its own
checksums).

Sequence: init a goopg primary with `--data-checksums` (`InitArgs` on the
`cluster` harness — a new additive option); fill a table with ~4 000 rows
spanning ~115 heap pages and `CHECKPOINT` so those checksummed pages are flushed
to disk **before** the clone and land before the backup's redo point (so WAL
replay never rewrites them with PG's own checksums); `pg_basebackup -X stream`
the cluster to a real PG data dir (PG copies goopg's version-1 `pg_control`, so
PG turns checksum verification on); start the PG standby and let it stream; then
on the standby assert `SHOW data_checksums = on` and seq-scan the whole table
(`count(*)` = 4 000 and `sum(length(payload))`). A bad checksum on any page would
abort the scan with `invalid page in block N of relation ...` — exactly how
upstream's own checksum tests detect a corrupt page — so a clean full scan with
the right row count is the proof. (We do not read `pg_stat_database.checksum_failures`:
the standby is real PG on goopg's *bootstrapped* catalog, which does not define
PG's `pg_stat_*` system views; the seq-scan is the canonical signal regardless.)

**Both gates now pass.** The remaining work is the one-line default flip itself
(`init`'s `dataChecksums` default false → true), which is deferred to a dedicated
loop: flipping the default changes the on-disk format of **every** new cluster,
so it must be gated by the full regress-port suite + a TPC-H re-load/spot-check
(per the M0106 "codec/format change → re-run full suite" lesson) and a sweep of
every test/bench data dir that would need re-init. Tracked under M0102-0010.

## Testing

- `internal/storage/checksum_test.go` — primitive properties (existing).
- `internal/storage/checksum_io_test.go` (new) — Manager round-trip with
  checksums on (Extend→ReadBlock verifies clean, caller buffer not mutated),
  corruption detection (`*ChecksumError`), `IgnoreChecksumFailure` +
  `OnChecksumFailure`, disabled = byte-identical, `ExtendBatch` per-block
  checksums, new-page skip.
- `internal/initdb/data_checksums_test.go` — `buildPgControl` writes
  version 1/0; **`TestInitDataChecksumsBootstrapsVerifiablePages`** is the e2e
  boot test: inits with `DataChecksums=true`, asserts pg_control version 1,
  then verifies every relation page under base/ & global/ carries a valid
  checksum (off/BlockSize convention) — a missed file fails the test — and
  reads block 0 of pg_type/pg_class/pg_attribute through a checksummed
  Manager (the production read path).
- `internal/initdb/recovery_test.go` **`TestCrashRecoveryReplaysChecksummedClusterCleanly`**
  — recovery/FPI-replay gate (a) for the default-ON flip: SIGKILL + WAL replay on
  a `DataChecksums=true` cluster, then proves every recovered page is
  checksum-valid via both the checksum-enabled Manager read path and an on-disk
  `VerifyPage` walk.
- `internal/initdb/checksum_bootstrap_test.go` — `checksumRelationData`
  per-block numbering / no-input-mutation / transposition-rejection.
- `internal/testport/e2e_checksum_replication_test.go`
  **`TestE2E_ChecksumStreamingGoopgToPG`** — standby-read/physical-replication
  gate (b) for the default-ON flip: a `--data-checksums` goopg primary streams
  (via `pg_basebackup -X stream`) to a real PG standby that verifies goopg's
  `pd_checksum` on every page read; a full seq-scan returning the exact row
  count with `data_checksums = on` proves byte-level checksum compatibility.
  Skipped under `-short` / `GOOPG_SKIP_M0102_E2E` / missing PG binaries.
- `cmd/goopg/main_test.go` `TestInitCommandDataChecksums` — the `-k` /
  `--data-checksums` / `--no-data-checksums` flags drive pg_control's
  `data_checksum_version`; `--no-data-checksums` overrides `-k`.
- Gates: `go build ./...`, `go vet`, `gofmt`, full `internal/initdb`,
  `go test -race ./internal/storage ./internal/wal`, TPC-H Q12/Q13 spot-check.
