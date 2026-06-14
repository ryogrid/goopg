Task: MAINT-STATEGUARD-RECONCILE — root-cause + fix the every-loop
`make ralph-state-guard` failure. LANDED loop #11.

What it was: NOT concurrent-loop corruption. ppid analysis showed a single
`--live` loop (child 3900929 ppid=3813356 = the portable_timeout subshell). The
driver writes progress={"status":"completed"} after EVERY clean claude exit
(~/.ralph/ralph_loop.sh:~1832, "Clear progress file") — "completed" = this loop's
claude finished, NOT project done. The next loop sets status=running without
touching progress, so steady state during any loop is status=running (loop N) +
progress=completed (loop N-1 marker). The guard flagged that normal transient as
corruption; prior loops' manual progress restore got stomped on next exit →
recurred every loop (#5-#11).

Fix: new autoRepair rule in cmd/validate-ralph-state/main.go — complement of the
stale-status rule — when progress=completed is NOT newer than a running status by
> max-skew, reconcile progress→in_progress (live status authoritative). Guard now
self-heals via `-fix`; no manual edits. Genuine completion unaffected (status is
completed/graceful_exit, not running).

Files: cmd/validate-ralph-state/main.go (+rule), main_test.go (3 tests; replaced
TestAutoRepairNoopWhenNotStale which pinned the buggy noop),
docs/design/root-0018-ralph-state-guard-prev-loop-marker-reconcile.md (new) +
README.md index row, .ralph/fix_plan.md (MAINT note). Memory
concurrent_ralph_loops_corrupt_tree.md updated (Loop #11 entry supersedes Loop #10
manual-restore advice).

Key symbols: autoRepair, validate (cmd/validate-ralph-state/main.go).

Gates run loop #11: gofmt -l (clean), go vet (clean), go test -count=1
./cmd/validate-ralph-state/ PASS, make ralph-state-guard RC=0 (self-healed,
REPAIRED 1 fix → OK).

CONTAMINATION (unchanged, NOT mine): the 18 foreign-WIP modified files
(catalog.go, operators_lockrows.go, parser/ddl.go, planner/*, analyzer, dispatch,
mvcc/subxact_visibility, …) + untracked gen_override_test.go + .claude/worktrees/*
remain a static foreign snapshot. Do NOT git add -A. Commit ONLY the guard-fix
files above.

Next step: resume the M0095-0003 FEATURE increment — pg_basebackup `-X fetch`
(WAL-fetch path): parse BASE_BACKUP `WAL` boolean option (basebackup.go
baseBackupOptions + parseBaseBackupOptionList), then after the data-dir tar append
in-range WAL segments under pg_wal/ with goopg→PG 24-char segment-name conversion
(mirror basebackup.c includewal lines 408-520; reuse replication.go conversion).
