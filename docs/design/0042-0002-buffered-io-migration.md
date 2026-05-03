# 0042-0002 — Buffered-I/O migration (drop O_DIRECT)

**Status:** draft
**Parent milestone:** M0042
**Depends on:** `0042-0001-pg-io-survey.md`
**Date:** 2026-05-04

## 1. Objective

Remove every `O_DIRECT` / `O_DSYNC` / page-aligned-RMW code path
from goopg's WAL writer and storage manager, so the system runs
on plain buffered I/O exactly the way upstream PostgreSQL runs by
default. Durability comes from `fsync` / `fdatasync` boundaries,
not from bypassing the OS page cache.

This is a deliberate retraction of part of M0010-0001 (WAL direct
I/O write path) and the `Manager.AlignedIO` toggle in
`internal/storage/`. The motivation is twofold: the survey doc
(`0042-0001`) shows upstream's default is buffered, and the
direct-I/O paths add a non-trivial maintenance surface (RMW
loops, alignment scratch, Linux-only probes, fsync semantics
diverging by syscall path) for no observed durability or
performance win on the workloads goopg targets.

## 2. What this changes

### 2.1 WAL writer
- `internal/wal/direct_io_linux.go` — delete.
- `internal/wal/direct_io_other.go` — delete.
- `internal/wal/writer.go`:
  - Remove `enableDirectIO`, `writeAtDirectIO`, `directIOActive`,
    direct-I/O scratch buffers, RMW pread/overlay/pwrite loop.
  - `openSegment` opens with `O_RDWR | O_CREATE`, no
    direct-I/O flip.
  - `writeAt` is the only write primitive: it calls
    `file.WriteAt(p, off)` and returns; no alignment, no
    chunking driven by direct-I/O constraints. (AIO Submit/Wait
    paths can stay if they exist for reasons unrelated to
    `O_DIRECT`.)
  - Counters specific to direct-I/O (`directWritesTotal`,
    `tailRMWWritesTotal`) are retired.
- `internal/config/defaults.go` — remove the `wal_direct_io`
  GUC entry and any reference in `internal/config/parser.go`.

### 2.2 Storage / buffer pool
- `internal/storage/direct_io_linux.go` — delete.
- `internal/storage/direct_io_other.go` — delete (if present).
- `internal/storage/smgr.go`:
  - Drop `setDirectIOIfRequested`.
  - `Manager.openFile` opens with `O_RDWR | O_CREATE` only.
  - `ManagerConfig.AlignedIO` field deleted; if any test or
    integration sets it, those call sites are also updated to
    drop the field.
- `internal/storage/arena.go`:
  - The buffer pool's arena page-aligns its slabs to satisfy
    `O_DIRECT`. After this milestone the alignment is no longer
    required for I/O correctness, but page alignment is still
    a reasonable allocator hygiene; **keep** the alignment, just
    drop the comment that ties it to direct-I/O.

### 2.3 Tests
- Any test that toggles `wal_direct_io=on` or `AlignedIO: true`
  is rewritten to assert the buffered path or deleted if
  redundant. The rest of the suite must stay green:
  - `go test ./internal/wal/...` — green.
  - `go test ./internal/storage/...` — green.
  - `go test ./internal/testutil/tpch -run TestTPCHResultParity`
    — still **identical=22 divergent=0 errored=0**.
  - `go test ./...` — green (only the pre-existing `tmp/`
    scratch dir is excluded).

### 2.4 Documentation
- Mark `docs/design/0010-0001-wal-direct-io-write-path.md`,
  `0010-0003-wal-direct-io-observability-and-operations.md`
  as **superseded** by this doc. The
  `0010-0002-walsender-in-memory-wal-handoff.md` doc is
  orthogonal and stays accepted.
- `docs/design/0007-0002-fdatasync-commit-path.md` continues
  to describe the durability primitive the buffered path
  relies on; reference it explicitly here.

## 3. Out of scope

- Replacing `fdatasync` with a different durability primitive
  — that is M0009 / M0026 territory.
- Removing the WAL ring buffer (`wal_buffers`); buffered I/O
  needs the ring just as much as direct I/O did, for the WAL
  insertion-lock parallelism described in `0042-0001` §6.

## 4. Verification

| Step | Expected |
|------|----------|
| `go test ./internal/wal/... -count=1` | green |
| `go test ./internal/storage/... -count=1` | green |
| `go test ./internal/testutil/tpch -run TestTPCHResultParity` | identical=22, divergent=0, errored=0 |
| `go test ./internal/testutil/tpch -run TestRunTPCHQueriesAgainstSyntheticData` | 22/22 |
| `go test ./...` | clean (pre-existing `tmp/` excluded) |
| `make ralph-state-guard` | OK |
| `git grep O_DIRECT internal/` | no hits |
| `git grep AlignedIO internal/` | no hits |
| `git grep wal_direct_io internal/` | no hits |

## 5. Reference

- `docs/design/0042-0001-pg-io-survey.md` §2 (WAL writes — buffered
  by default upstream), §3.1 (data files — buffered upstream).
- `internal/wal/writer.go`,
  `internal/wal/direct_io_linux.go`,
  `internal/storage/smgr.go`,
  `internal/storage/direct_io_linux.go`.
- Superseded: `docs/design/0010-0001-wal-direct-io-write-path.md`.
