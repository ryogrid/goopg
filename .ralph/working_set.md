(idle — nothing in flight)

Loop #139 CLOSED **M0131-S30.1** (fix + guards + design doc committed).

**Resumed WIP:** loop #138 left an uncommitted `internal/wal/reader.go` skip +
`reader_segment_tail_gap_test.go`. Verified before trusting it (per
`ralph_verify_background_agent_hardoff_before_commit`): the test FAILED with the
WIP fix applied, and the WIP fix was itself wrong.

**What was actually wrong:** the sub-header segment-tail gap is real (Path B,
`reserveEmittedAndPublish` cannot fit its `XLOG_NOOP` pad below 24 bytes), but
the OTHER writer path — `appendPGCompat` Path A (`Config.WALBuffers == 0`,
oversized record, ring drain) — has NO boundary re-land and emits records that
legitimately straddle the boundary. An unconditional skip drops those. Fix
landed: skip only when `recordStartsAt` (header decode + `xl_tot_len` bounds +
`xl_crc` over the reassembled record) says the bytes are not a record.

**Guards** (`internal/wal/reader_segment_tail_gap_test.go`, fixture picks the
writer path via `Config.WALBuffers`): 2× Path-B gap (zeroed + 0xff stale) +
1× Path-A straddle. Validated both directions (skip disabled → gap tests fail
with the production `64 records read`; skip unconditional → straddle test
fails).

**Gates:** `go test -race ./internal/wal/...` PASS; units suite PASS;
storage/mvcc PASS; `RUNS=2 analysis/crashprobe30.sh` after `go build -o
bin/goopg` → **zero row loss (500000/500000 both runs), zero early end-of-WAL**
(pre-fix binary minutes earlier: 490984/500000 + `padding bytes nonzero
(0xff 0x06)` at lsn=117440505). Still `OVERALL: FAIL` on the atomicity
invariant only. NOTE: crashprobe30 does NOT rebuild `bin/goopg` — build first
or you measure the old binary (cost me one full probe run).

**Filed this loop:** S30.6 (the two append paths disagree about segment-boundary
layout — writer unification, ledger row 2026-08-11) and S30.7 (crash still tears
transactions: `sum(abalance) != sum(history.delta)`, both directions).

**NEXT LOOP (per the M0131 banner):** S30.2 (an early end-of-WAL must refuse to
start, not WARN-and-open) or S30.7 (torn transactions — the only remaining
crashprobe30 failure). S30.4 also untouched.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012), all already filed under M-NIGHTLY; nothing new.

In-flight: none.
