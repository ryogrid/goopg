Task: M0122-0003 EXPLAIN (MEMORY) — text + JSON output (COMPLETE)

Files:
- internal/mctx/mctx.go: Added lifetimeCounters struct (totalAllocated/currentBytes/peakBytes);
  ensureLC() init guard; Usage() (allocated, peak int64) getter; tracking in
  Alloc/AllocAligned/allocBytes/growChunk; Reset zeroes currentBytes;
  pointer-based (lc *lifetimeCounters) to keep Context ≤96 B
- internal/executor/instrument.go: Added memAllocated/memPeak/memBase*/memSeeded
  to nodeStats; sctx *mctx.Context on instrumentedOp; seed in Open(); diff in
  accountBuffers() (outside pool-nil guard)
- internal/executor/operators_explain.go: formatMemoryLine (TEXT: "Memory:
  used=NkB  allocated=NkB"); opts.Memory gate in walkPlanAnalyzeFiltered;
  "Memory Used"/"Memory Allocated" JSON properties in planToJSONWithStats
  (outside opts.Buffers block)
- internal/executor/explain_memory_test.go: NEW — 9 tests (formatMemoryLine,
  SQL-level smoke, JSON presence/absence, instrumentedOp diffing, nil-Mctx)
- .ralph/fix_plan.md: M0122-0003 marked [x] complete

Key symbols:
- mctx.lifetimeCounters: totalAllocated, currentBytes, peakBytes
- mctx.Context.ensureLC(), Usage() (allocated, peak int64)
- nodeStats.memAllocated, memPeak, memBase*, memSeeded
- instrumentedOp.sctx *mctx.Context
- formatMemoryLine(s *nodeStats) string

Hypothesis/Findings:
- EXPLAIN (MEMORY) is done: TEXT per-node "Memory: used=NkB  allocated=NkB",
  JSON "Memory Used"/"Memory Allocated" in kB, matching PG's show_memory_counters
  format. Counters are lifetime (never reset) and diffed per-node (nested-
  stopwatch semantics, same as WAL and BUFFERS).
- Simple SeqScan doesn't use mctx for tuple decode (DecodeHeapTupleRow passes
  nil), so the Memory line may be suppressed (formatMemoryLine returns "" when
  counters are zero). Hash joins, index scans, and expression evaluation DO
  allocate through mctx — those nodes will show non-zero memory.
- Deferred: EXPLAIN (MEMORY) without ANALYZE (would need planner memory
  tracking — goopg's planner uses Go heap, not mctx).

Next step:
- Next priority per banner: M0119 unchecked items (M0119-0005 pg_waldump server
  tier — needs index AMs; M0119-0006 pg_amcheck server tier — needs index AMs
  + opclass parity; both blocked on foundation work)
  OR M0122-0006 (On-disk catalog persistence) / M0122-0007 (DDL/admin commands)

Gates run:
- go build ./...: OK
- go test (mctx, executor): ALL PASS (incl. 9 new explain_memory tests)
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, 409153 txns)
- ralph-state-guard: PENDING (run before status block)

In-flight: none
