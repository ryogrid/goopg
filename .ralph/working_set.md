Task: WAL preallocation counters/observability follow-up (M0007,
`unimplemented_feat.json` entry "Counters for WAL preallocation metrics and
monitoring."). COMPLETE and committed this loop (`81607874`).

Files: internal/wal/writer.go (`walBufferCounters` struct gains
`segmentsPreallocated`/`preallocatedBytes` stats.Counter fields; `openSegment`
bumps both in the existing `wasNew` branch right after `preallocateSegment`
succeeds; new `Writer.SegmentsPreallocated()`/`Writer.PreallocatedBytes()`
accessors next to the `FsyncCount`/`FsyncTimeNanos` pattern); internal/wal/
wal_test.go (new `TestPreallocationCounters`: disabled→both 0, enabled→1
after first segment, no double-count on re-open of same segment, 2 after
forced rollover into segment 1); internal/initdb/wal_io_views.go (two new
`pg_stat_wal_io` columns `wal_segments_preallocated_total`/
`wal_init_zero_bytes_total`, inserted after `wal_buffers_flush_drain_bytes`
and before `format_version` so existing positional-index tests stay valid);
internal/initdb/wal_io_views_test.go (column-order comment updated; new
`TestStatWALIOPreallocationCounters` on/off subtests); docs/design/
0007-0001-wal-segment-preallocation.md (new "Follow-up (2026-07-08)" section
replacing the stale "Counters / observability... deferred" bullet) +
docs/design/README.md (0007-0001 row updated to match); unimplemented_feat.json
(surgical 2-field edit only, per house rule — status→resolved, code_audit
rewritten to cite the new symbols; verified valid JSON via json.load after).

Key symbols: `walBufferCounters.segmentsPreallocated`/`.preallocatedBytes`
(writer.go); `Writer.SegmentsPreallocated`/`Writer.PreallocatedBytes`
(writer.go); `state.openSegment`'s `wasNew` branch (the single increment
site); `registerStatWALIOView` (wal_io_views.go).

Findings: the design doc's original blocker — "they want a shared stats sink
which doesn't yet exist" (written 2026-04-29) — was already resolved by later
loops: `internal/stats.Counter` (per-P sharded additive counter) landed for
the M0013-0003 WAL-buffer drain counters and M0122-0003's `fsyncCount`, both
living in this exact same `walBufferCounters` struct. This loop just added
two more fields to an already-proven pattern — no new infra needed. Confirmed
non-vacuous via `git stash` on the 4 changed non-test files: both new test
files failed (wal package: compile error, undefined methods; initdb package:
index-out-of-range panic reading column 9 of an unmodified 9-column row)
without the implementation.

Next step: pick a fresh item next loop. Un-investigated candidates from
unimplemented_feat.json (grep `"status": "open"` + `"confidence": "high"`):
"parsePrimaryConninfo sslmode/password" (task_id `m0005`, PARTIAL per existing
code_audit — user/application_name already landed, only sslmode/password
remain, plus a stale doc comment); "restart command for goopg ctl control
socket" (task_id None, currently a documented stub — scope-check first
whether a foreground v0 server can meaningfully support `restart` before
committing); "Implement signal-based configuration refresh (SIGHUP-style) for
the reload command" (task_id None — the wal_sync_method GUC follow-up two
loops ago noted this blocks live-reconfiguration of every `ContextSigHup` WAL
GUC, including wal_sync_method itself); "eager next-segment lookahead for WAL
preallocation" (task_id M0007, sibling of this loop, PARTIAL-flagged in the
adjacent unimplemented_feat.json entry — background-goroutine scope, read
that entry's `code_audit` at ~line 496 first). Also worth a look:
"isfinite() function is not implemented" (m0097-0004, likely a tiny
self-contained builtin) and "min_parallel_table_scan_size/
min_parallel_index_scan_size GUCs accepted but not acted upon" (m0097-0003).

Gates run: go build ./... clean. go vet ./... clean. go test ./internal/wal/...
(full package, includes new TestPreallocationCounters) PASS. go test
./internal/initdb/... (full package, 231s, includes new
TestStatWALIOPreallocationCounters) PASS. scripts/tpch-spotcheck.sh PASS
(Q12=2/Q13=33). RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh
PASS (0 failed, all 3 workloads) — run standalone, then again as the actual
git pre-commit hook on the real commit. make ralph-state-guard: 1 benign
issue auto-repaired (same recurring status/progress clean-exit-vs-in_progress
reconciliation noted every prior loop — not new, do not chase it).

In-flight: none. Nightly-triage: `ci/logs/action-items.md` mtime (2026-07-07
03:52) predates this loop's start (2026-07-08 07:54) — no new triage needed,
skip re-running the `## AI-` scan next loop unless that file's mtime has
moved past 2026-07-08 07:54. The independent nightly-batch run
(`ci/logs/20260708-064334`, sha=2e435e91) was mid-flight at this loop's start
(preflight/units/race stages present, pgbench/testport/tpch not yet reached)
and is not something this loop started or should wait on — check
`ci/logs/latest/stages/` next loop; if `pgbench/` has landed clean, that's
the confirmation the M-NIGHTLY pgbench/nightly bullet (fix_plan.md line ~64)
has been waiting on per [[goopg_nightly_ci_batch]] memory — archive/drop it
then.
