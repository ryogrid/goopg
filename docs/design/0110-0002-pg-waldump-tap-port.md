# 0110-0002 — pg_waldump TAP test port (001_basic CLI tier)

Status: accepted (partial)
Milestone: M0110-0002
Date: 2026-06-13

## Goal

Port the upstream `postgres/src/bin/pg_waldump/t/001_basic.pl` TAP test into a
Go test under `internal/testport/`, following the incremental tier strategy
established by M0110-0001 (pg_dump 001_basic).

## What 001_basic.pl contains

The upstream test has two clearly separable tiers:

1. **CLI option-handling tier** (upstream lines 10-77). Pure
   argument-parser/built-in-table behaviour, decided before any WAL file is
   opened — no server required:
   - `program_help_ok` / `program_version_ok` / `program_options_handling_ok`
   - "no arguments" and "too many command-line arguments"
   - invalid argument values for `--block`, `--fork`, `--limit`,
     `--relation`, `--rmgr`, `--start`, `--end`
   - `--rmgr=list` exact resource-manager listing

2. **Server-dependent tier** (upstream lines 80-323). Spins up a cluster and
   runs DDL exercising heap/btree/**hash/gin/gist/spgist/brin** indexes,
   tablespaces, logical messages and relmap changes, then runs pg_waldump over
   the produced segments asserting per-rmgr / per-relation / per-block /
   `--limit` / `--fullpage` / `--stats` filtering.

## Decision

Port **tier 1** as `TestPort_PgWaldump001Basic`
(`internal/testport/pgwaldump_port_test.go`). It drives the upstream pg_waldump
binary shipped unchanged in `postgres/local_install/bin`; goopg reuses it
verbatim, so this tier validates the CLI surface the rest of the suite depends
on and provides a presence/behaviour regression guard for the bundled binary.

It reuses the `PostgreSQL::Test::Utils` mirrors already added for pg_dump
(`programHelpOk` / `programVersionOk` / `programOptionsHandlingOk` /
`commandFailsContaining`) and adds one new helper, `commandLikeMatching`
(mirror of `command_like`: exit 0 + stdout predicate), used for the exact
`--rmgr=list` assertion. The rmgr list is pinned to the PG 18.3 table; the
upstream comment's "if you add an rmgr, update this" note applies in lockstep.

**Defer tier 2** under CSV row `WD-002`. goopg does not implement the hash,
gin, gist, spgist and brin access methods the workload requires, so the test
cannot be made to pass without large unrelated feature work. The WAL-format
readability that tier 2 would prove (upstream pg_waldump parsing goopg
segments) is *already* covered for goopg's supported record types by
`TestPort_WALPgWaldumpCompat` (CSV row `W-001`, the M0101-0003 gate), so the
deferral leaves no compatibility coverage gap for implemented features.

## CSV rows

- `WD-001` → `port` / `pass_required=yes`: `001_basic.pl` CLI tier =
  `TestPort_PgWaldump001Basic`.
- `WD-002` → `defer` / `pass_required=no`: server tier of `001_basic.pl` +
  `002_save_fullpage.pl`; blocked on hash/gin/gist/spgist/brin access methods.

## Verification

- `gofmt -l` clean; `go vet ./internal/testport/` clean.
- `go test -v -run TestPort_PgWaldump001Basic ./internal/testport/` → PASS.
- `go run ./cmd/gen-oracle-port-status` regenerated the `.md` view.

## Resume point

Promote `WD-002` to `port` once goopg gains the index access methods the
server-tier workload needs (and `--save-fullpage` FPI extraction for
`002_save_fullpage.pl`), or rewrite the server tier against a reduced
heap/btree-only workload if a narrower compatibility check is judged
sufficient.

## 2026-06-14 (loop #17): `002_save_fullpage.pl` ported as a self-promoting reproduction + a real blocker uncovered

`TestPort_PgWaldump002SaveFullpage`
(`internal/testport/pgwaldump_savefullpage_test.go`) ports
`002_save_fullpage.pl`. It is feasible against goopg in principle — goopg
emits PG-compatible FPIs (the checkpointer's per-page FPI epoch fires one on
the first post-`CHECKPOINT` page mutation), writes a full PostgreSQL
`RelFileLocator` per block reference (`spc=1663`, `db=<oid>`,
`relNumber=<reloid>`, matching `pg_relation_filenode`), and the upstream
`pg_waldump` binary is reused unchanged. The test drives the full
`--save-fullpage --relation` extraction and asserts the upstream filename
format + page-LSN ≤ file-LSN ordering.

It currently **skips with a precise blocker** because driving it surfaced two
previously-hidden facts:

1. **goopg's on-disk WAL is not walkable by `pg_waldump`.** goopg stores
   `xl_prev` as its internal **1-based** LSN, while `pg_waldump` anchors the
   record position 0-based on the segment file name. The mismatch is a constant
   `+1` (observed: `incorrect prev-link 0/1000029 at 0/10000A0`, expected
   `0/1000028`). Chain validation aborts at the *second* record, so no FPI is
   ever reached. Origin: `internal/wal/writer.go` forms the record start
   1-based (`start = writePos + leading + 1`, ~L1346/L1491) and the
   insert-position tracker carries that value through as `xl_prev`
   (`internal/wal/insert_pos.go` `reserveLocked`: `t.prev = old`);
   `encodeRecordXLog` (`internal/wal/format.go:263`) writes it verbatim even
   though its own comment says the caller must pass the 0-based RecPtr
   (`start-1`). Fixing it is a coordinated WAL **encode↔decode** change
   (goopg's own recovery reads the 1-based value back, and the M0102 walsender
   must stay consistent), so it belongs in a dedicated WAL-correctness loop.

2. **`TestPort_WALPgWaldumpCompat` (row `W-001`) is silently red.** goopg's WAL
   segment file names are now native PostgreSQL format (TLI=1 prefix, e.g.
   `000000010000000000000001`), but W-001 still expects plain hex segment
   *numbers* (`strconv.ParseUint(name,16,64)`, which overflows for 24 hex
   chars) and rewrites a timeline alias — so it `t.Fatal`s at "no WAL segments
   found" before it ever runs `pg_waldump`. Oracle tests are excluded from the
   default `go test ./...` run, which is why this went unnoticed. The claim
   earlier in this doc that W-001 covers on-disk readability is therefore
   **currently void**: once W-001's segment discovery is fixed it will hit the
   *same* prev-link blocker. Fixing W-001 belongs to M0101-0003 and is filed in
   the deferral ledger.

Resume: emit `xl_prev` 0-based on disk in `internal/wal` (encode + decode
siblings, re-verify M0102 replication + recovery E2E + re-init), then un-skip
`TestPort_PgWaldump002SaveFullpage` and repair `W-001`.
