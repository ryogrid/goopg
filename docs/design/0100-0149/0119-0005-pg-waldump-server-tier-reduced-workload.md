# 0119-0005 — pg_waldump 001_basic.pl server tier (reduced heap/btree workload)

Status: accepted
Milestone: M0119-0005 (source M0110-0002)
Date: 2026-08-14
Oracle: PostgreSQL 18.3 — `postgres/src/bin/pg_waldump/t/001_basic.pl` lines 80–323

## Goal

Port the **server-dependent tier** of `001_basic.pl` — the set of assertions that
drive the upstream `pg_waldump` binary over a real cluster's WAL and check its
per-rmgr / per-relation / per-block / `--limit` / `--fullpage` / `--stats`
filtering plus its WAL-range argument handling. The CLI-only tier is already
ported (`TestPort_PgWaldump001Basic`, CSV WD-001); this closes the deferred
WD-002 half.

The upstream workload exercises hash/gin/gist/spgist/brin indexes (plus the
`jsonb`/`point` types they need). goopg implements none of those access methods,
so the full workload cannot run. But **none of the filtering assertions depend on
those rmgrs**: the only rmgr-specific assertion is `--rmgr Btree` (goopg emits
RM_BTREE), and the rest assert the *shape* of `pg_waldump`'s output regardless of
which rmgrs produced it. The design doc for the CLI tier (`0110-0002`) already
sanctioned this path:

> rewrite the server tier against a reduced heap/btree-only workload if a
> narrower compatibility check is judged sufficient.

The broader prerequisite that previously blocked *all* of this work — goopg's WAL
being structurally undecodable by `pg_waldump` — is now resolved: the native→PG
WAL rewrite (Phase A + Phase B, B0–B5) landed 2026-07-16…19, and
`TestPort_WALPgWaldumpCompat` (row W-001) is live and passing at HEAD, driving a
large catalog-DDL workload through the upstream binary with no structural error.
So a reduced heap/btree workload is both sufficient and decodable today.

## Reduced workload

Dropped from upstream: `USING hash` (`i1b`), the `gin` index over `jsonb`
(`gin_idx`), the `gist`/`spgist` indexes over `point`, the `brin` index
(`brin_idx` + `brin_summarize_range`/`brin_desummarize_range`), and
`pg_logical_emit_message`. Kept:

```sql
-- heap, btree, sequence
CREATE TABLE t1 (a int, b text);
CREATE INDEX i1a ON t1 USING btree (a);
INSERT INTO t1 VALUES (1, 'one'), (2, 'two');
DELETE FROM t1 WHERE b = 'one';

-- abort (Transaction rmgr)
START TRANSACTION;
INSERT INTO t1 VALUES (3, 'three');
ROLLBACK;

-- unlogged / init fork
CREATE UNLOGGED TABLE t2 (x int);
CREATE INDEX i2 ON t2 USING btree (x);
INSERT INTO t2 SELECT generate_series(1, 10);

-- heap2 (VACUUM) + a forced FPI for --fullpage
VACUUM;
CHECKPOINT;
UPDATE t1 SET b = 'two' WHERE a = 2;
```

Deviation notes (vs upstream `GENERATED ALWAYS AS IDENTITY`): the identity
column is replaced by a plain `int` column — the sequence WAL it would produce
is not asserted anywhere, and `serial`/identity adds an unrelated dependency.
`VACUUM` (upstream runs it too) yields `PRUNE_VACUUM_SCAN` (Heap2) records, and
the `CHECKPOINT; UPDATE` forces an FPI on the first post-checkpoint page
mutation (the checkpointer's per-page FPI epoch — `maybeEmitFPI`,
`internal/storage/bufpool.go:2419`, `needsImage` redo-barrier at
`bufpool.go:1090`) so the `--fullpage` assertion is non-vacuous. The unlogged
table stays in the workload for upstream fidelity (it emits `XLOG_SMGR_CREATE`),
but its init-fork WAL is **not** asserted — see the dropped `--fork init` row
below.

## Assertions ported

Two groups, mirroring upstream.

### A. Filtering options (`test_pg_waldump` helper)

| upstream assertion | port |
|---|---|
| no opts → every line `^rmgr: \w` | every non-empty line matches `^rmgr: ` |
| `--limit 6` → exactly 6 lines | line count == 6 |
| `--fullpage` → every line has `FPW` | every line matches `\bFPW\b`, **plus** a non-vacuity guard (≥1 line) |
| `--stats` → "WAL statistics" on stdout, no `rmgr:` lines | same |
| `--stats=record` → same | same |
| `--rmgr Btree` → every line `^rmgr: Btree` | same, **plus** non-vacuity (≥1 line) |
| `--relation <spc>/<db>/<t1>` → every blkref that rel | every blkref matches the t1 locator, **plus** non-vacuity (≥1 line) |
| `--relation <i1a> --block 1` → every blkref `blk 1` | same, **plus** non-vacuity (≥1 line) |

**Dropped: `--fork init`.** goopg emits no init-fork block-reference WAL at all
— no `INIT_FORKNUM` emit exists anywhere in `internal/wal/`, unlogged mutations
deliberately skip WAL (`MarkDirtyUnlogged`, `internal/storage/bufpool.go:2148`),
and the only relation-create WAL (`XLOG_SMGR_CREATE`, `EncodeSmgrCreatePG`) has
main-data only with no block ref. Upstream produces init-fork lines from
`smgr_bulk_start_rel(rel, INIT_FORKNUM)` during unlogged index/heap builds
(`postgres/src/backend/access/nbtree/nbtree.c:185`), a mechanism goopg lacks. A
vacuous-green `--fork init` (empty result passing a grep) is worse than omitting
the row, so it is dropped and recorded in the deferral ledger as an
unlogged-init-fork engine gap.

The non-vacuity guards are an addition, not upstream behaviour: `grep` over an
empty line list vacuously passes, and a filter that silently matched nothing
(e.g. `--rmgr Btree` if goopg stopped emitting btree WAL) would otherwise read
as green. The upstream workload guarantees a non-empty result set for each
option; the reduced workload asserts the same explicitly. (`--rmgr Btree`'s
non-vacuity comes from the two `INSERT INTO t1` statements' first dirty leaf
page — `XLOG_FPI` then `RM_BTREE INSERT_LEAF` — not the empty-table index build.)

### B. WAL-range argument handling

| upstream assertion | port |
|---|---|
| `pg_waldump foo bar` → `could not locate WAL file "foo"` | same |
| `pg_waldump <start_seg>` → runs | same (positional segment file) |
| `pg_waldump <start_seg> bar` → `could not open file "bar"` | same |
| `pg_waldump <start_seg> <end_seg>` → runs | same |
| `pg_waldump --path <data_dir>` → `no start WAL location given` | same |
| `pg_waldump --path <data_dir> --start <lsn> --end <lsn>` → runs | same, LSN derived from segment names |
| `pg_waldump --path <data_dir> --start <lsn>` (no end) → `error in WAL record at` | same (falls off end) |
| `pg_waldump --quiet <start_seg>` → no output | same |
| `pg_waldump --quiet --path <data_dir> --start <lsn>` → error shown | same |
| `--start <lsn+1>` on a segment → `first record is after` info message | same |

## Deviations / workarounds (all forced by goopg gaps, documented for later)

1. **LSN values derived from segment filenames, not `pg_walfile_name()` /**
   **`pg_current_wal_insert_lsn()`.** goopg seeds `pg_walfile_name` /
   `pg_switch_wal` in `pg_proc` but does not execute them (noted in the
   `002_save_fullpage` port). A PG segment filename
   `ttttttttxxxxxxxxyyyyyyyy` encodes its start LSN deterministically
   (`XLogFromFileName`, `postgres/src/include/access/xlog_internal.h:200`,
   `XLogSegmentsPerXLogId = 256` at 16 MB):
   `startLSN = (logid << 32) | (seg << 24)` where `logid = hex(seg[8:16])`,
   `seg = hex(seg[16:24])`. We enumerate non-all-zero segments via the existing
   `listWALSegments` + `segmentIsAllZero`. **Three precise rules** (each a trap
   found in review):
   - `--start` for the A-group runs = the **first non-all-zero segment's exact
     start LSN (offset 0)** — an offset-0 start is what suppresses the
     `first record is after` info message that would otherwise pollute `stderr`
     and break the A-group "no stderr" assertions.
   - `--end` = `startLSN(last_nonzero_segment) + 16 MB` — one segment *past* the
     last real segment. If the workload fits in one segment (it usually does),
     `startLSN(first) == startLSN(last)` and a naive `--end = startLSN(last)`
     yields an empty range, failing every A-group assertion.
   - The `+1` byte offset is applied **only** on the B-group `first record is
     after` row, where the info message is the expected outcome.

2. **DB component of the relation locator globbed, not read from
   `pg_database.oid`.** That column is a legacy display placeholder
   (`firstNormalObjectOID`) for non-template databases; the real on-disk OID
   is only observable as the `base/<dbOid>/` directory name. Reuse the
   `findHeapFile` / savefullpage glob workaround. The tablespace component is
   fixed at `1663` (pg_default — the only value goopg's WAL decoder accepts).

3. **Relnode via `pg_relation_filenode('<rel>'::regclass)`**, which goopg
   serves (`internal/executor/expr.go:9726`, relation OID == filenode for
   non-temp relations), rather than `pg_class.oid` — same choice as the
   savefullpage port.

4. **All-zero preallocated tail segment skipped** (`segmentIsAllZero`): with
   `wal_init_zero=on` the writer eagerly zero-fills the next segment, and the
   upstream binary fatals on it exactly as it would on a real PG cluster when
   pointed at it *directly* (`invalid WAL segment size … 0 bytes`). In `--path`
   mode walking off the end the reader errors inside the current segment's
   zero-fill (`error in WAL record at`), which is what the falling-off-the-end
   B-row asserts. Skipping the tail loses no coverage (no records).

5. **Multi-statement batch through a single `psql -c`** — the existing W-001
   workload already does this, and goopg's simple-query path runs an explicit
   transaction block within one message (`internal/server/dispatch.go:622`;
   `internal/server/dispatch_batch_atomicity_test.go:111` proves
   `CREATE TABLE; BEGIN; CREATE TABLE; ROLLBACK;` in one `-c`).

## CSV

- `WD-002` → `port` / `pass_required=yes`: the server tier of `001_basic.pl`
  (per-rmgr/per-relation/per-block filtering + WAL-range handling), ported as
  `TestPort_PgWaldump001BasicServerTier` against the reduced heap/btree
  workload. Two omissions, each deferred (ledger): the full workload's
  hash/gin/gist/spgist/brin index rmgrs (goopg implements none of those AMs,
  and no filtering assertion targets them) and the `--fork init` assertion
  (goopg emits no init-fork block-ref WAL).

## Verification

- `go test -v -run TestPort_PgWaldump001BasicServerTier ./internal/testport/` (through `scripts/goopg-test-run.sh`).
- `go run ./cmd/gen-oracle-port-status` regenerates the `.md` view.
- `gofmt -l` clean on the new test file; `go vet ./internal/testport/` clean.

## Deferral

1. The reduced workload omits hash/gin/gist/spgist/brin index rmgrs (and the
   `jsonb`/`point` types they require) because goopg does not implement those
   access methods. No filtering assertion in `001_basic.pl`'s server tier
   targets those rmgrs — the only rmgr-specific assertion is `--rmgr Btree`,
   which goopg satisfies — so the filtering coverage is complete for every
   record type goopg actually emits.

2. The `--fork init` assertion is dropped because goopg emits no init-fork
   block-reference WAL (no `INIT_FORKNUM` emit; unlogged mutations skip WAL).
   The resume point is `smgr_bulk_start_rel`-equivalent init-fork bulk-init
   during unlogged index/heap builds — a separate engine gap, out of scope here.

Neither blocks a later loop from extending the workload verbatim once those AMs
or init-fork bulk-init land.

## Implementation notes (2026-08-14)

Five deltas from the draft, each forced by a goopg gap discovered while driving
the port:

1. **A statement after an explicit block's COMMIT/ROLLBACK failed in the same
   simple-query message** ("mvcc: unknown transaction"): `applyTransactionVerb`
   finalized the message transaction but left the dispatch loop's
   `autoCommit`/`tx` pointing at the dead transaction, so the next statement ran
   against it. Fixed in `internal/server/dispatch.go` (re-arm a fresh RC
   transaction + `autoCommit=true` when `!autoCommit && !connTx.InExplicit()`
   after a statement, mirroring `PLpgSQLCommitChain`), pinned by
   `TestSimpleQueryBatchStatementAfterBlockEndRunsInFreshTransaction`.

2. **The workload is split into three `psql -c` messages** (pre / abort-block /
   post), not one. goopg's simple-query dispatch runs ONE transaction per Query
   message, so a single `…; START TRANSACTION; INSERT; ROLLBACK; …` batch rolls
   the pre-BEGIN statements back with the block (a deliberate, tested deviation
   — `TestSimpleQueryBatchExplicitBeginUndoesEarlierAutocommitCreateTable` pins
   it). Splitting preserves the upstream workload's *semantics* without
   disturbing that documented model.

3. **`--end` is derived from pg_waldump's own "invalid record length at X/X"
   report**, not `startLSN(last)+16 MiB` (which overshoots into the zero-filled
   tail and aborts the run). The exact end position makes every `--start/--end`
   run stop cleanly (exit 0).

4. **Relnodes come from `pg_class.oid`** (== the filenode), not
   `pg_relation_filenode`, whose `LookupTableByOID` arm does not resolve
   indexes — so the index `i1a` could not be resolved that way. Matches the
   upstream .pl's own `SELECT oid FROM pg_class WHERE relname = …`.

5. **The positional-segment "runs" assertions tolerate the end-of-WAL exit.**
   The upstream workload fills whole 16 MiB segments, so `pg_waldump <seg>`
   exits 0; goopg's reduced workload leaves the final segment partial, so
   pg_waldump prints every record then aborts on the zero-fill ("invalid record
   length … got 0"). We assert the positive half (records were read, `--quiet`
   suppressed output, "first record is after" fired) and tolerate that exit.
