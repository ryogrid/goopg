(idle — nothing in flight)

M0131-S21a-1 LANDED (loop #155) — the RECOGNITION layer of S21a.

Files: `internal/wal/{pg_xlog_decode.go,pg_assembled_emit.go,recovery.go}`,
`internal/initdb/{xact_recovery.go,open.go}`, tests in
`internal/wal/{reader_fail_closed_test.go,xlog_replay_test.go}` +
`internal/initdb/xact_recovery_test.go`.

What landed:
- RM_HEAP2 masked with `xlogHeapOpMask` 0x70 (was 0xF0) — `heap2_redo` itself
  switches on `info & XLOG_HEAP_OPMASK` (heapam_xlog.c:1229); upstream ORs
  INIT_PAGE into a MULTI_INSERT onto a fresh page, so every COPY is 0xD0.
- Recognised no-ops w/ citations: HEAP_TRUNCATE, HEAP2_NEW_CID,
  XACT_ASSIGNMENT/INVALIDATIONS, STANDBY_LOCK/INVALIDATIONS. STANDBY_LOCK
  alone refused the start on any PG tail containing DDL.
- 2PC opcodes refuse loudly (ErrUnsupportedRecord + "two-phase commit").
- **XLOG_NEXTOID now really applies**, via a two-pass split: recognised as a
  page no-op in `replayDecodedXLogRecord`, applied by new
  `replayNextOIDFromWAL` (initdb) called from `Open` after the pg_control seed.
  New `wal.EncodeXLogNextOidPG`/`DecodeXLogNextOid`.

4 new guards, ALL proven fail-when-broken by scripted reverts. One pre-existing
test corrected: `TestApplyRecordRejectsUnknownDecodedXLogStandbyRecord` used
0x20 as "unknown"; now 0x30 (PG really leaves it undefined).

2 ledger rows: S21a-2's whole page-mutating set still refused; and the
max-vs-set deviation from upstream's OID-wraparound-safe "believe the record
exactly" (goopg's allocator is monotone, no wraparound path).

Gates: internal/wal PASS + `-race` PASS, internal/initdb PASS (75 s), UNITS
precommit PASS, pgbench smoke via the commit hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
items already filed under M-NIGHTLY (fix_plan.md:766) — parked per banner.

Next loop (banner = M-NIGHTLY filing, then M0131): **M0131-S21a-2**, the page
work — `XLOG_HEAP2_MULTI_INSERT` 0x50 first (~70% reuse from
`replayHeapMultiInsert`, recovery.go:3842; needs the xl_heap_multi_insert /
xl_multi_insert_tuple block-0 decoder + INIT_PAGE + offsets[]), then
`XLOG_HEAP_LOCK` 0x60 and `XLOG_HEAP2_VISIBLE` 0x40 (0% reuse — goopg's
HeapVisible replay is an explicit no-op), plus the zero-extend at
`replayDecodedXLogHeapInsert`'s replay-gap `default:`. Each landing shrinks
S28's skip. Design: `docs/design/0131-0015-pg-wal-opcode-coverage.md` §S21a.

In-flight: none.
