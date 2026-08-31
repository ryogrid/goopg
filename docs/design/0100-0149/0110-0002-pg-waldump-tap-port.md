# 0110-0002 — pg_waldump TAP test port (001_basic CLI tier)

> **perf-optimize3-dash S4 note (2026-07-13)**: WD-003/WD-004 are deferred
> (assert-skip) under the native-only WAL default; resume = GOOPG_WAL_CANONICAL=on + C1.

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

## 2026-07-03: `TestPort_PgWaldump002SaveFullpage` un-skipped (WD-003)

The `xl_prev` blocker above was resolved in an earlier loop (`prevRecPtr`
seeding fix, `internal/wal/writer.go`). Re-running the test after that fix
still found zero extracted full-page images, for two reasons investigated and
fixed this loop:

1. **The HOT-update path never emitted a PG-canonical FPI.** goopg's canonical
   (PG-decodable) WAL emission — `catalog.PgCanonicalHeapInsert`/
   `PgCanonicalHeapDelete`, wired via `ctx.LogCanonical` — was already called
   from every insert/delete site (`internal/executor/operators_storage.go`),
   proven working for the test's `CREATE TABLE ... AS SELECT` (100 canonical
   `INSERT` FPIs, confirmed against real `pg_waldump` output). But
   `tryApplyHOTUpdate` (~line 3300) only called `markHeapHotUpdateDirty`,
   goopg's *native* opaque WAL record — no canonical sibling. Since
   `test_table` has no index, the test's `UPDATE test_table SET a = a + 1`
   always takes the HOT path (same-page tuple rewrite, no index maintenance),
   so every one of its 100 row updates was invisible to `pg_waldump`. Fixed
   with a new `emitCanonicalHeapHotUpdate` (`operators_storage.go`, next to
   `emitCanonicalHeapInsert`/`emitCanonicalHeapDelete`) that re-pins the page
   after the HOT stamp+insert and calls the existing
   `catalog.PgCanonicalHeapInplace` (XLOG_HEAP_INPLACE) — already used
   elsewhere for the `datfrozenxid` runtime write (M0117-0008 Part B). A HOT
   update rewrites the whole page in place (xmax stamp + new tuple, same
   block), so "restore the whole page from an FPI" is exactly the right
   semantics; no new canonical record type was needed.
2. **The test's own relation-locator resolution was wrong.** It read the DB
   component of `TBLSPC/DB/RELNODE` from `SELECT oid FROM pg_database WHERE
   datname = current_database()`, but `pg_database.oid` is a documented
   legacy display placeholder (`"16384"`, the `FirstNormalObjectId` constant —
   see `catalog.go`'s `pgDatabase.VirtualRows` comment: changing it to the real
   value once broke pg_dump's subscription round-trip) for every non-template
   database. The value goopg's WAL/storage layer actually writes into block
   references (`detectCatalogDBOID`'s scan of the physical `global/1262` heap
   at startup) is only observable on disk as the `base/<dbOid>/` directory
   name. `pgamcheck004_port_test.go` had already hit and worked around the
   identical gap (`findHeapFile`); this test now globs
   `base/*/<relnode>` the same way instead of trusting the SQL column.

Verified independently of the Go test: a manual `goopg init`/`start`, `psql`
`CREATE TABLE ... AS SELECT`/`CHECKPOINT`/`UPDATE`, `stop`, then the real
`postgres/local_install/bin/pg_waldump --save-fullpage --relation <locator>`
round-trip extracts 200 correctly-named, correctly-LSN-ordered full-page-image
files (100 `INSERT` + 100 `INPLACE`).

**CSV**: `WD-002` narrowed to just the still-deferred `001_basic.pl`
server-dependent tier (hash/gin/gist/spgist/brin AMs, unrelated to this fix).
New row `WD-003` = `port` / `pass_required=yes` for `002_save_fullpage.pl`.

**Not investigated this loop** (see deferral ledger): whether
`markHeapPruneOptDirty` (`operators_storage.go` ~2714, opportunistic page
pruning) has the same "HOT-class path skips canonical FPI" gap. Pruning
reclaims dead space without changing live tuple data, so it may not matter for
crash/standby correctness the way the HOT-update gap did, but it was flagged
and not checked.

## 2026-07-03: prune/VACUUM canonical-WAL audit (confirmed, not fixed)

Followed up on the flag above. Confirmed the gap exists, and it is bigger than
the flag anticipated:

- **Opportunistic prune** (`tryApplyHOTUpdate`'s page-full fallback,
  `operators_storage.go` ~3225-3234 → `markHeapPruneOptDirty` ~2714) calls
  only `pool.LogHeapPruneOpt()` — goopg's native `RecordKindHeapPruneOpt`
  (`internal/wal/recovery.go:105`). No `ctx.LogCanonical` call anywhere in the
  function.
- **Real `VACUUM`** (`operators_vacuum.go`'s `vacuumOp.Next` →
  `vacuum.VacuumWithOptions`, `internal/vacuum/vacuum.go:56`) has the identical
  gap, and is architecturally the more important case since VACUUM is the
  common real-world trigger for pruning. `internal/vacuum` has no
  `LogCanonical`-shaped parameter at all today.
- Ruled out `maybeEmitFPI`/`logFPI` (`internal/storage/bufpool.go:1182`) as an
  implicit safety net — it also only emits a goopg-native record
  (`RecordKindPageImage`, `wal.EncodePageImage`), not a PG rmgr-decodable one.

Net effect: a page whose only WAL activity since its last canonical touch is
pruning/VACUUM is invisible to `pg_waldump` in every mode (not just
`--save-fullpage`), and a real PG18 standby attached via `ctx.LogCanonical`
would drift from the primary's line-pointer/redirect state (live tuple data
is unaffected).

Not a blocker for any currently `pass_required` test — `WD-003` above exercises
CTAS + HOT-UPDATE only, no VACUUM. No code changed this loop; implementing the
fix (a new `catalog.PgCanonicalHeapPrune`, mirroring `PgCanonicalHeapInplace`'s
single-full-page-image approach, plus wiring into both call sites above) is a
new-capability addition — a public API change on `vacuum.VacuumWithOptions`
touching every caller, not a copy-paste. Full trace + concrete resume point in
the deferral ledger (task-id `M0119-0005`, row dated 2026-07-03, immediately
after the WD-003 row).

## 2026-07-03: prune/VACUUM canonical-WAL fix (LANDED)

Implemented the resume point from the audit above. New `catalog.PgCanonicalHeapPrune`
/ `BuildCanonicalHeapPrunePayload` (`internal/catalog/canonical.go`) encodes a
PG-canonical `XLOG_HEAP2_PRUNE_ON_ACCESS` (0x10) or `XLOG_HEAP2_PRUNE_VACUUM_SCAN`
(0x20) record under `RM_HEAP2_ID` (9) — a **distinct** rmgr from `RM_HEAP_ID`
(10, used by insert/delete/inplace), matching PG's own rmgr split
(`postgres/src/include/access/rmgrlist.h`). The main-data section is the
minimal `xl_heap_prune{reason(1), flags(1)}` (`SizeOfHeapPrune`, no
`XLHP_HAS_CONFLICT_HORIZON`/`XLHP_HAS_REDIRECTIONS` bits set) — verified
against `heap_xlog_prune_freeze`
(`postgres/src/backend/access/heap/heapam_xlog.c`) that once the block
reference carries a full-page image, `XLogReadBufferForRedoExtended` returns
`BLK_RESTORED` and the redo function returns **before** parsing any block-data
sub-records (freeze plans / redirected / dead / unused offset arrays) at all —
so, exactly like `PgCanonicalHeapInplace`/`PgCanonicalHeapDelete`'s established
simplification, none of those arrays need encoding.

Two call sites wired, both only when `ctx.LogCanonical`/`opts.LogCanonical` is
non-nil (an absent hook is a total no-op, matching every other `emitCanonical*`
helper):

- **Opportunistic prune** — new `emitCanonicalHeapPruneLocked` (unexported,
  `operators_storage.go`) is called from `tryApplyHOTUpdate`'s page-full
  fallback immediately after `markHeapPruneOptDirty` succeeds, **while the
  page's content lock from the enclosing HOT-update is still held**. It
  deliberately does NOT re-Pin/Lock the slot like the sibling
  `emitCanonicalHeapInsert`/`emitCanonicalHeapHotUpdate`/`emitCanonicalHeapDelete`
  helpers (all called after their caller has released the lock) — re-locking
  the same already-held slot would deadlock. It instead copies the
  already-locked slot's current (post-prune) page bytes directly. `onAccess`
  is hardcoded `true` at this call site (`PRUNE_ON_ACCESS`).
- **Real VACUUM** — `vacuum.VacuumOptions` gained a `LogCanonical
  catalog.LogCanonicalFunc` field (a new import of `internal/catalog` into
  `internal/vacuum`; no cycle, catalog does not import vacuum). `vacuumCore`'s
  existing dead-tuple-reclamation block (`internal/vacuum/vacuum.go`, the
  `if reclaimed > 0` arm) emits the canonical record right after its existing
  `MarkDirtyChangeRecord`/`logPrune` call, using `xid=InvalidTransactionID (0)`
  — VACUUM has no live user transaction of its own to stamp, and (as traced
  above) a standby restoring the whole page from the FPI never consults the
  record's `xl_xid` — with `onAccess=false` (`PRUNE_VACUUM_SCAN`).
  `operators_vacuum.go`'s `vacuumOp.Next` threads `o.ctx.LogCanonical` into the
  `vacuum.VacuumOptions{}` literal it already builds.

Tests: `TestBuildCanonicalHeapPrunePayload` (both `onAccess` cases, byte-layout
pin, `internal/catalog/canonical_test.go`), `TestPgCanonicalHeapPrune_NilLogFn`;
`TestVacuumWithOptionsEmitsCanonicalPruneRecord` /
`TestVacuumWithOptionsNilLogCanonicalIsNoop` (`internal/vacuum/vacuum_test.go`);
`TestOpportunisticPruneEmitsCanonicalWAL` (`internal/executor/prune_test.go`,
reuses `TestOpportunisticPruneReclaims`'s exact fixture/repro with
`ctx.LogCanonical` wired, asserting an `XLOG_HEAP2_PRUNE_ON_ACCESS` record was
emitted for the opportunistic prune, distinct from the same update's own
`XLOG_HEAP_INPLACE` HOT-update record).

Gates: `go build ./...`/`go vet ./...` clean; `gofmt -l` clean on touched
files; `go test -race ./internal/wal/... ./internal/mvcc/...` PASS; full
`internal/catalog`+`internal/vacuum`+`internal/executor`+`internal/initdb`+
`internal/planner`+`internal/server` suites PASS (no regression);
`scripts/tpch-spotcheck.sh` PASS; pgbench smoke = pre-commit hook.

Still open (unchanged from the audit): `001_basic.pl`'s server-dependent tier
(hash/gin/gist/spgist/brin AMs) remains unported.

## 2026-07-04: live `pg_waldump --rmgr=Heap2` round-trip + temp-table N/A confirmation

Closed both items the prune/VACUUM fix above left open.

1. **Live round-trip (WD-004).** New `TestPort_PgWaldumpVacuumPruneRoundtrip`
   (`internal/testport/pgwaldump_vacuum_prune_test.go`) creates a table,
   deletes half its rows, runs `VACUUM` (exercising
   `vacuum.VacuumOptions.LogCanonical`), stops the cluster, and runs the real
   upstream `pg_waldump --rmgr=Heap2` binary against the resulting WAL
   segment(s). Asserts (a) no structural decode error (same "incorrect
   prev-link" / "invalid magic number" / "incorrect resource manager" guard
   `TestPort_WALPgWaldumpCompat` (W-001) already uses) and (b) the output
   contains a `PRUNE_VACUUM_SCAN` record whose `blkref` names this table's
   exact `spc/db/relnode` locator. Live-verified (×3, no flake): pg_waldump
   printed `rmgr: Heap2 ... desc: PRUNE_VACUUM_SCAN , isCatalogRel: F,
   blkref #0: rel 1663/5/16403 blk 0 FPW` — confirms
   `BuildCanonicalHeapPrunePayload`'s FPI-only encoding
   (`internal/catalog/canonical.go`) round-trips through a real PG18 reader,
   not just the unit-level payload assertions from the prior loop. This is
   NOT a standby-replay test (no second goopg instance consumes the WAL as a
   physical standby) — only that a vanilla PG18 tool reads the bytes. CSV:
   new `WD-004` row (`postgres-oracle-port-status.csv`, `utility`/`port`/
   `pass_required=yes`, mirroring how `W-001` covers general WAL readability
   without being tied to one upstream `.pl` file); `target-inventory.csv`
   unchanged (same precedent as `W-001` — no entry, since neither is a port
   of one specific upstream TAP file).
2. **Temp-table opportunistic prune — confirmed N/A.** Verified directly
   against `postgres/src/include/utils/rel.h`'s `RelationNeedsWAL` macro:
   `(RelationIsPermanent(relation) && ...)` — a temp relation
   (`relpersistence == RELPERSISTENCE_TEMP`) never satisfies
   `RelationIsPermanent`, so real PG issues **zero** WAL for any temp-relation
   change, prune included. `pruneTouchedTempPages`
   (`internal/executor/operators_indexonly.go:284`) is gated
   `!o.plan.Table.Temp { return }` (temp-only) and should therefore stay
   exactly as-is — adding `emitCanonicalHeapPruneLocked`-style canonical
   emission there would itself be the divergence from PG, not fixing one.
   No code change; this closes the open question as documented fact rather
   than an unconfirmed guess.

Gates: `go build ./...`/`go vet ./...` clean; `gofmt -l` clean on the new
test file; `TestPort_PgWaldumpVacuumPruneRoundtrip` PASS ×3 (no flake, run
through `scripts/goopg-test-run.sh`); `go run ./cmd/gen-oracle-port-status`
regenerated `postgres-oracle-port-status.md` from the CSV.

## 2026-07-11 (loop #29): eager-preallocation all-zero segment breaks the round-trip (M-NIGHTLY AI-20260711-011536-003)

The nightly flagged `TestPort_PgWaldumpVacuumPruneRoundtrip` as a regression:
`pg_waldump --rmgr=Heap2` reported the structural error `invalid WAL segment
size in WAL file "000000010000000000000002" (0 bytes)` on the *trailing*
segment, even though segment 1 decoded the `PRUNE_VACUUM_SCAN` record cleanly.

**Root cause — not a WAL-format bug.** The M0122-0009 eager next-segment
preallocation (`internal/wal/writer.go` `eagerPreallocSegment` /
`eagerPreallocWorker`, landed `ff27f01d` 2026-07-09) zero-fills the *next*
segment (`…002`, a full 16 MiB of zeros) the moment segment 1 opens for real
use, and that phantom persists across a clean shutdown — exactly as real
PostgreSQL's `XLogFileInit` does under `wal_init_zero=on` (the default). Both
on-disk segments are the full 16 MiB (confirmed by `os.Stat`); `…002` is simply
entirely zeros because it was never written. `pg_waldump` derives the WAL
segment size from the long-page-header field `xlp_seg_size`, which the writer
only stamps when a segment first becomes the insert target — so on an all-zero
preallocated segment that field is 0 and pg_waldump fatally aborts with
"invalid WAL segment size (0 bytes)". **Real pg_waldump errors identically** on
an all-zero preallocated segment; goopg's bytes are PG-faithful.

**Fix (test fidelity only, no production change).** Skip all-zero preallocated
segments before feeding them to pg_waldump, via a new `segmentIsAllZero` helper
in `pgwaldump_vacuum_prune_test.go`. Such a segment carries no records, so
skipping loses no coverage. The identical latent bug in the sibling W-001
`TestPort_WALPgWaldumpCompat` (`wal_pg_waldump_test.go`) — which was simply not
run in tonight's partial `NIGHTLY_STAGES` batch — was fixed in the same loop
(sibling-path rule). `pgwaldump_savefullpage_test.go` only treats "incorrect
prev-link" as fatal, so it was already tolerant and needed no change.

The two other nightly items (`TestPort_IsolationTimeouts`,
`TestPort_IsolationTuplelockUpgradeNoDeadlock`) are co-load timing flakes, not
regressions — both PASS 3/3 standalone at HEAD; the isolation runner decides
blocking purely by a 300 ms timeout (no `pg_locks` probe), so nightly CPU
contention can spuriously trip them.

Gates: `go vet ./internal/testport/` clean; `TestPort_PgWaldump*` + W-001 PASS.
