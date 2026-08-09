Task: M0122-0003 EXPLAIN (WAL) — text + JSON output (COMPLETE)

Files:
- internal/wal/writer.go: Added walRecords/walBytes atomic counters + WalRecords()/WalBytes() getters; incremented in Append (both fast/slow paths)
- internal/storage/bufpool.go: Extended WALFlusher interface with WalRecords()/WalBytes(); added Pool.WalCounters()
- internal/storage/lpdead_hook_test.go: Stub methods for walFlushRecorder
- internal/storage/storage_test.go: Stub methods for recordingWAL
- internal/executor/instrument.go: Added walRecords/walBytes + base fields to nodeStats; seeding in Open + diffing in accountBuffers
- internal/executor/operators_explain.go: formatWalLine (TEXT: "WAL: records=N bytes=K"); WAL rendering in walkPlanAnalyzeFiltered (TEXT) + planToJSONWithStats (JSON: "WAL Records"/"WAL FPI"/"WAL Bytes"/"WAL Buffers Full")
- .ralph/fix_plan.md: M0119-0004 marked [x] complete

Key symbols:
- Writer.walRecords / Writer.walBytes (atomic.Int64)
- Writer.WalRecords() / Writer.WalBytes() (getters)
- WALFlusher.WalRecords() / WALFlusher.WalBytes() (interface)
- Pool.WalCounters() (records, bytes int64)
- nodeStats.walRecords / walBytes / walBaseRecords / walBaseBytes / walSeeded
- formatWalLine(s *nodeStats) string

Hypothesis/Findings:
- EXPLAIN (WAL) now produces per-node "WAL: records=N bytes=K" lines in TEXT and
  JSON properties in structured formats. Counters are lifetime (never reset) and
  diffed per-node (nested-stopwatch semantics, same as BUFFERS).
- walBytes counts payload bytes only (not xlog record header overhead — deferred).
- WAL FPI and WAL Buffers Full are always 0 (FPI counting needs per-record kind
  knowledge; buffers-full tracking needs cross-reference with WAL buffer state).
  JSON format emits them as int64(0) matching PG's unconditional-non-text pattern.

Next step:
- EXPLAIN (MEMORY) output (same pattern — needs MemoryContext framework first) OR
  next M0122 unchecked item (M0122-0006: On-disk catalog persistence;
  M0122-0007: DDL/admin commands).

Gates run:
- go build ./...: OK
- go test (executor, wal, storage): ALL PASS
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, 404706 txns)
- ralph-state-guard: REPAIRED + PASS

In-flight: none
