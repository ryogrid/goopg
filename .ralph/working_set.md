(idle — nothing in flight)

Loop #33 landed doc 04 §5.4's FIRST additive slice (not the whole rework):
`internal/wal/xlog_record.go` gained `RmgrCLOG=3`/`RmgrGoopgCustomBase=128`/
`RmgrGoopgCatalog=128` consts and `DecodeXLogRecordHeader`'s reject guard
widened from `Rmid > MaxKnownRmgr` to `Rmid > MaxKnownRmgr && Rmid <
RmgrGoopgCustomBase` — clears the header-decode BLOCKER doc 04 flagged.
New `TestDecodeAcceptsGoopgCustomRmgrRange`. Verified inert (grepped: nothing
emits Rmid=3 or Rmid>=128 yet). Gates: `go build ./...` clean, `go vet
./internal/wal/...` clean, `go test ./internal/wal/...` full-package green,
`go test -race ./internal/wal/...` green. Ledger row appended (deferral_
ledger.md, WAL doc-04 §5.4 slice); fix_plan.md WAL section updated with a new
`[x]` bullet + explicit next-step; design doc 04 §5.4 marked "Landed
2026-07-15" inline (old bullet struck through, kept for history).

Next step for the WAL epic: `internal/wal/pg_xlog_decode.go` — add HEAP2
opcode consts (`xlogHeap2PruneOnAccess=0x10`/`_VacuumScan=0x20`/
`_VacuumCleanup=0x30`, cited doc 03 §5). Still additive/inert, no dispatch
rewrite. AFTER that: `internal/wal/format.go`'s `recordKindToRmgrInfo`
mapping table + `classifyXLogRecord` rewrite (doc §3), then
`internal/wal/recovery.go`'s `replayDecodedXLogRecord` dispatch rework
(doc §4) — this last piece is the actual risk (R1 critical: land last,
incrementally, full G-crash before/after; native heap/btree bodies must
reach the native `replayX` functions, not the FPI-only decoded arms, or
mutations silently drop on recovery). See
`docs/design/wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md`
§4/§5/§8 for the complete staged plan.

Concurrent peer loop note (still relevant, R3 in the doc): at loop start,
`ps aux` showed the peer `ralph_loop.sh --live --verbose` (PID 3748426,
started Jul14) still alive alongside this session's own runner. No commit
collision observed this loop (checked git log before committing); the
tree also carries unrelated uncommitted noise from other processes —
`.ralph/progress.json`'s pre-loop diff (own state-guard repair, included in
this loop's commit is fine), plus `analysis/tpch-explain-baseline.md`,
`ci/logs/launch.log` (modified but NOT touched by this loop — left as-is),
and untracked `postgres`, `analysis/perf-optimize3/runs/...`,
`kaitai-struct-dash*.txt`, `weekly_loc.csv`/`.png` (all pre-existing,
unrelated to this loop — left untouched). Commit used explicit pathspec
covering only this loop's 5 files, per `ralph_concurrent_commit_pathspec_
required` pattern.

Gates run this loop: `go build ./...`, `go vet ./internal/wal/...`,
`go test ./internal/wal/...`, `go test -race ./internal/wal/...` all PASS;
`make ralph-state-guard` found + auto-repaired a stale progress.json
"completed" marker (same class as prior loops' clean-exit artifact),
re-verified consistent after repair.

In-flight: none.
