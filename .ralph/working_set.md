(idle — nothing in flight)

Note (loop #32): confirmed a concurrent Ralph loop is active in this same
tree (a second `ralph_loop.sh --live --verbose` process, PID 3748426, started
Jul14, still running alongside this session's own PID 470154 started
2026-07-15 10:57 — NOT the same-process subshell-argv artifact the memory
guard usually flags). That peer committed `e9884a60` (doc 04 of the
`wal-native-pg-format` design bundle, co-authored by "Claude Opus 4.8")
between this loop's start and its first git-status check. This loop's own
work (README index updates for docs/design/README.md +
wal-native-pg-format/README.md, plus a fix_plan.md entry) was disjoint from
that commit and landed cleanly as `5e4f57af` using explicit pathspec
(`ralph_concurrent_commit_pathspec_required` pattern) — no conflict, no
stomping observed this loop, but future loops should keep checking
`git log`/`ps aux` for this peer before assuming the tree is loop-exclusive.

Next step for the WAL epic (not started, no code touched yet): implement
doc 04 §5.4's lowest-risk additive changes first — add `RmgrCLOG=3` /
`RmgrGoopgCatalog=128` consts in `internal/wal/xlog_record.go`, widen
`DecodeXLogRecordHeader`'s `Rmid > MaxKnownRmgr(=11)` reject to accept the
custom range (currently a BLOCKER per the doc — rejects 128/3/8) — verify
inert (nothing emits those rmids yet) before touching
`classifyXLogRecord`/`recovery.go` dispatch or removing the canonical family.
See `docs/design/wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md`
§4/§5 for the full staged plan and R1's "land last, incrementally" warning.

Gates run this loop: `RALPH_PRECOMMIT_SCOPE=smoke bash
scripts/ralph-precommit-test.sh` (via commit hook) PASS, 0 failed across all
3 pgbench workloads; `make ralph-state-guard` found + auto-repaired a stale
progress.json "completed" marker left by the prior loop's clean exit,
re-verified consistent after repair.

In-flight: none.
