(idle — nothing in flight)

M0131-S21a-2 PART 5 LANDED (loop #160) — `CLOG_ZEROPAGE` (RM_CLOG, 0x00).
Commit pending push to `make-db-cluster-compat`.

Files: `internal/wal/{pg_xlog_decode.go,pg_assembled_emit.go,recovery.go}`,
new `internal/wal/clog_zeropage_pg_test.go`, design
`docs/design/0131-0015-pg-wal-opcode-coverage.md` §"S21a-2 … part 5"
(+ opcode-matrix row + `docs/design/README.md` index line +
`.ralph/fix_plan.md` S21 note update).

Key symbols: `replayDecodedXLogClogZeroPage`, `DecodeXLogClogZeroPage`,
`clogSLRUPagesPerSegment` (duplicated from `mvcc.slruPagesPerSegment` —
`wal` cannot import `internal/mvcc`, reverse import already exists for the
WAL-flush hook).

What landed:
- `WriteZeroPageXlogRec`'s opcode, fired once per 32768 XIDs right before
  the first commit/abort into a fresh CLOG page. Unlike `CLOG_TRUNCATE` it
  needs no `*mvcc.CLog`/catalog handle, so it replays directly in the
  PHYSICAL pass (`initdb/open.go:380`, well before `mvcc.OpenCLog` at
  `:1006`) rather than deferring to `replayCLogFromWAL`'s initdb second
  pass.
- Writes a zero-filled BLCKSZ page into `pg_xact/<%04X of pageno/32>` at
  offset `(pageno%32)*BLCKSZ`, creating the segment (+ MkdirAll the dir,
  defensive) if absent.
- Why an arm is needed even though goopg's own `clogBufferPool` fault-in
  already zero-fills a missing/short segment (so dropping the record was
  ACCIDENTALLY harmless for goopg's own reads): upstream
  `SimpleLruReadPage` hard-errors on a missing segment — a real PG standby
  cold-starting on this cluster, or `pg_resetwal`/`amcheck` reading
  `pg_xact/` directly, expects the segment to physically exist.
- 2 guards (`internal/wal/clog_zeropage_pg_test.go`): low-segment pageno
  creates+zero-fills segment 0000; high-segment pageno (65) creates 0002
  without touching 0000. Both proven fail-when-broken by a scripted revert
  of the dispatch case (commented out the case body, confirmed both tests
  fail with "segment file not created", restored, confirmed pass).

Gates: `internal/wal` PASS + `-race` PASS, `internal/storage` PASS, UNITS
precommit PASS (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`).
Pgbench smoke runs via the commit hook (not yet committed as of this write —
commit happens right after this file).

Nightly triage: `ci/logs/action-items.md` still on run `20260812-005501`
(same as last loop — no new nightly run landed this loop); all 4 `## AI-`
items already filed under M-NIGHTLY per prior loop's confirmation, nothing
new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): **S21a-2 part 6** —
`SMGR_TRUNCATE` 0x20 (native `replaySmgrTruncate` exists at
`recovery.go:~5732` but truncates to ZERO blocks / decoder carries no
`blkno`+fork-bitmask — PG's `xl_smgr_truncate` needs both a new PG decoder
and a partial-length truncate primitive in `storage.Manager`; see design
doc §"RM_SMGR" for the exact gap), then `HEAP2_REWRITE`'s loud refusal
(0x00 RM_HEAP2, out-of-scope VACUUM-FULL-with-logical-slot — needs an
`ErrUnsupportedRecord`-style message, not real redo). Then S21b (btree,
~6 opcodes, gated on S16 which is already done). Each landing shrinks
S28's self-arming skip further.

In-flight: none.
