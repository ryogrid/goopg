Loop #34 landed doc 04 §5.4's SECOND additive slice (still not the whole
rework): `internal/wal/pg_xlog_decode.go` gained `xlogHeap2PruneOnAccess=0x10`
/ `xlogHeap2PruneVacuumScan=0x20` / `xlogHeap2PruneVacuumClean=0x30`
(RM_HEAP2_ID opcodes, confirmed against
`postgres/src/include/access/heapam_xlog.h:60-62`) — these are exactly the 3
opcodes doc §3.1's mapping table cites for `HeapVacuum`/`HeapPruneOpt`/
`HeapFreeze`; no other HEAP2 opcode is referenced anywhere in §3, so none
else were added. Verified inert (grepped: no other reference to the new
const names in the tree). Gates: `go build ./...` clean (info-level
unused-const diagnostic only, same class as pre-existing `slotOffPersistency`
in `slots_pg.go`), `go vet ./internal/wal/...` clean, `go test
./internal/wal/...` and `go test -race ./internal/wal/...` full-package
green. Ledger row appended (deferral_ledger.md, WAL doc-04 §5.4 second
slice); fix_plan.md WAL section updated with a new `[x]` bullet + explicit
next-step; design doc 04 §5.4 second bullet marked "Landed 2026-07-15"
inline (old bullet struck through, kept for history).

Next step for the WAL epic: `internal/wal/format.go` — build the
`recordKindToRmgrInfo` mapping table (doc §3's FULL table, every
`RecordKind`→`(rmid,info)` row, both §3.1 PG-analog and §3.2 custom-rmgr)
and rewrite `classifyXLogRecord` to use it, retiring `xlogInfoDefault` as
the catch-all. Read doc §3 in full before starting — this is the first
slice that changes what gets *emitted* (no longer purely additive/inert
like the two consts-only slices this loop and last). AFTER that:
`internal/wal/recovery.go`'s `replayDecodedXLogRecord` dispatch rework
(doc §4) — this remains the actual risk (R1 critical: land last,
incrementally, full G-crash before/after; native heap/btree bodies must
reach the native `replayX` functions, not the FPI-only decoded arms, or
mutations silently drop on recovery). See
`docs/design/wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md`
§3/§4/§5/§8 for the complete staged plan.

Concurrent peer loop note (R3 in the doc, carried forward — re-check at
next loop start): checked `ps aux`/git log this loop, no collision
observed; the tree still carries unrelated uncommitted noise from other
processes (`.ralph/progress.json`'s own state-guard repair — fine, part of
this loop's own gate; `analysis/tpch-explain-baseline.md`, `ci/logs/
launch.log` modified but NOT touched by this loop; untracked `postgres`,
`analysis/perf-optimize3/runs/...`, `kaitai-struct-dash*.txt`,
`weekly_loc.csv`/`.png` — all pre-existing, unrelated, left untouched).
Commit will use explicit pathspec covering only this loop's files, per
`ralph_concurrent_commit_pathspec_required` pattern.

Gates run this loop: `go build ./...`, `go vet ./internal/wal/...`,
`go test ./internal/wal/...`, `go test -race ./internal/wal/...` all PASS;
`make ralph-state-guard` found + auto-repaired the same recurring stale
progress.json "completed" marker (clean-exit artifact from a prior loop),
re-verified consistent after repair.

In-flight: none.
