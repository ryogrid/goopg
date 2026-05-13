# Milestone 0101 — WAL pg_waldump Compatibility: PG-Compatible Format by Default

**Status:** accepted
**Filed:** 2026-05-13
**Implements:** M0014 (PostgreSQL-Compatible WAL On-Disk Format) — focused specifically on pg_waldump parseability as the acceptance gate
**Reference plan:** `.ralph/fix_plan.md` (M0101 section)

## Context

All goopg clusters currently write WAL in the **legacy flat format** (magic `0x200E`,
no XLOG page headers). Attempting to parse these files with PostgreSQL's `pg_waldump`
fails immediately:

```
pg_waldump: error: invalid WAL segment size in WAL file "000000000000000000000000" (2621440 bytes)
pg_waldump: detail: The WAL segment size must be a power of two between 1 MB and 1 GB.
```

Root cause: `wal.Config.PageHeaders` defaults to `false`, so the writer uses the
pre-M0014 flat len+CRC32-IEEE stream. The PG-compatible format (magic `0xD118`,
`XLP_LONG_HEADER` flag, proper `xlp_seg_size`, `xlp_tli=1`) is already fully
implemented in `internal/wal/` (`Config.PageHeaders=true` path, M0014-0003), but
it is never activated in production.

The structural format between goopg and PostgreSQL is already aligned:

| Field | goopg byte offset | PostgreSQL byte offset |
|---|---|---|
| `xlp_magic` | 0 | 0 |
| `xlp_info` | 2 | 2 |
| `xlp_tli` | 4 | 4 |
| `xlp_pageaddr` | 8 | 8 |
| `xlp_rem_len` | 16 | 16 |
| (padding) | 20 | 20 |
| `xlp_sysid` | 24 | 24 |
| `xlp_seg_size` | 32 | 32 |
| `xlp_xlog_blcksz` | 36 | 36 |

Rmgr IDs also match PostgreSQL (XLOG=0, Xact=1, Storage=2, Heap2=9, Heap=10, Btree=11).
**The only changes needed are at the configuration and cluster-init layer.**

## In Scope

1. Enable `PageHeaders: true` by default for all new clusters.
2. Generate and persist a `system_identifier` (random `uint64`) during cluster
   initialization; wire it into `wal.Config.SystemID`.
3. Add a `pg_waldump` compatibility test that parses freshly emitted WAL and
   verifies zero parse errors.
4. Validate the long page header fields (`xlp_magic=0xD118`, `xlp_seg_size=16MiB`,
   `xlp_xlog_blcksz=8192`, `xlp_tli=1`, `XLP_LONG_HEADER` flag in `xlp_info`)
   against a PG-compatible binary installed at
   `./postgres/local_install/bin/pg_waldump`.

## Out of Scope

- Migration of existing legacy-format clusters (fail-fast diagnostic on detect is
  sufficient for v0; full `pg_upgrade`-style migration is M0014's long-tail work).
- New WAL record payload format changes (Rmgr payloads remain as-is).
- WAL compression, encryption, or `wal_level = logical` changes.
- Cross-major WAL compatibility (target: PostgreSQL 18, the version in `./postgres/`).

## Definition of Done

1. A freshly initialized goopg cluster writes WAL with:
   - `xlp_magic = 0xD118` on every page header.
   - `xlp_info` has `XLP_LONG_HEADER (0x0002)` set on every segment-boundary page.
   - `xlp_seg_size = 16,777,216` (16 MiB, `0x01000000`) in every long header.
   - `xlp_xlog_blcksz = 8192` in every long header.
   - `xlp_tli = 1` on all pages.
2. `./postgres/local_install/bin/pg_waldump <segment>` exits without error for at
   least one representative WAL segment emitted by a running goopg cluster.
3. `TestPort_WALPgWaldumpCompat` (new in `internal/testport/`) passes: starts a
   cluster, runs a workload (CREATE TABLE + INSERT), stops cleanly, runs
   `pg_waldump --quiet` on each segment in `pg_wal/`, asserts exit code 0.
4. `go test ./internal/wal/...` remains fully green (no regression to legacy format
   tests that still exercise `PageHeaders=false`).
5. `gofmt -l .` empty; `go vet ./...` clean; `make ralph-state-guard` passes.

## Required Design Docs

- `docs/design/0101-0001-wal-page-header-compat-default.md` — what changes at the
  config/init layer, system_identifier lifecycle, PageHeaders=true activation.
- `docs/design/0101-0002-wal-pg-waldump-validation-test.md` — test design, pg_waldump
  invocation, acceptable vs. failing output, integration with the oracle test framework.
