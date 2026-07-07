Task: M0122-0007 — implemented `goopg restart` (was a hard stub returning
exit 1 "not yet implemented"). COMPLETE and committed this loop (b01eae13).

Files: cmd/goopg/main.go (runRestart now delegates to new
runRestartWithStarter(args, stdout, stderr, start) — parses -D/-config/
-listen/-hba/-mode/-t, stops the -D instance only if control.ProcessAlive
says its postmaster.pid PID is actually live, waits up to -t seconds via
the same poll-loop pattern as runStop, then calls start(startArgs, ...)
with -D/-listen always set and -config/-hba only when non-empty — -listen
defaults to the stopped instance's own ListenAddr from its postmaster.pid
when not given explicitly, falling back to 127.0.0.1:5432 only when there
was no live instance to read it from), cmd/goopg/main_test.go (new
TestRunRestartWithStarter, 4 subtests: missing -D exits 2 without
invoking the starter; no postmaster.pid starts straight away with the
127.0.0.1:5432 default; a stale pidfile (dead PID) is treated as
not-running; a genuinely live instance — real control.Listener +
control.WritePIDFile + a real `sleep 60` child process as the stand-in
server — gets stopped (OnStop kills+Waits the child so ProcessAlive
actually flips false, not just Kill() alone which leaves a zombie) before
the fake starter runs, and its listen address carries through unchanged;
TestSubcommandStubsAreReachable's "restart" case updated 1->2 to match
the new -D-required contract), docs/design/root-0001-architecture-overview.md
(new "`goopg restart` (2026-07-08)" paragraph under the pg_ctl-mapping
table), docs/design/README.md (root-0001 row extended), .ralph/fix_plan.md
(M0122-0007's summary line + a new "done" writeup under it, "Still open"
trimmed to the remaining ~12 items now that both goopg reload/SIGHUP
(last loop) and goopg restart (this loop) are done).

Key symbols: runRestartWithStarter (cmd/goopg/main.go) — the testable
core; runRestart (cmd/goopg/main.go) is just runRestartWithStarter wired
to the real runStart. control.ProcessAlive/control.ParsePIDFile/
control.Send (internal/control/control.go) — reused verbatim from
runStop's existing pattern, no changes to that package.

Findings: v0's server never daemonizes (goopg start blocks the calling
process in the foreground), so a "restart" can't fork a replacement the
way pg_ctl does — the only sane implementation is stop-then-exec-start-
in-the-same-process, which is what landed. Testing the live-stop branch
needed a REAL killable OS process (not just a fake PID) because
control.ProcessAlive uses kill(pid,0), which succeeds against a zombie
that hasn't been Wait()'d yet — Kill() alone in the test's OnStop handler
left the sleeper process stuck as a zombie and the restart's poll loop
timed out at the full 30s default before I added the Wait() call. Live
e2e against the real cmd/goopg binary (start on 127.0.0.1:65498, `goopg
restart -D <datadir>` with no -listen) confirmed the PID actually changed
(1922289 -> 1922570) while `goopg status` kept reporting the same
127.0.0.1:65498 address unprompted.

Next step: pick the next task. M-NIGHTLY is clean (ci/logs/action-items.md
unchanged since 20260707-000712, all 8 items resolved — re-verify at next
loop start per the standing rule since this loop didn't need to touch it).
Candidates carried from prior loops, still open: (a) M0122-0006's
opclass/collation OID resolution gap (indclass/indcollation real OID
resolution, live AND heap-restore paths) and its pg_tablespace-visibility
item (flagged "defer indefinitely" in the ledger on 2026-06-15 — re-read
that row before committing a loop to it, the fix_plan bullet may be
stale); (b) M0122-0007's remaining ~12 items: CREATE/DROP DATABASE full
DDL, REINDEX (check internal/executor/operators_reindex.go's current
state first — may already be more complete than the fix_plan bullet
implies), tablespaces, ALTER FUNCTION/COLUMN, planner/jit GUC stubs; (c)
M0122-0008 (SASLprep/channel binding/scram_iterations still open; RBAC
mostly done per 2026-07-05/06 notes, view's-own-ACL gap remains,
materially larger); (d) M0119-0004/0005/0006/0007 per the Current
Priority banner (M0119-0004 pg_dump DU-002 residual, M0119-0005
hash/gin/gist/spgist/brin AM gap, M0119-0006 pg_amproc dispatch gap —
check overlap with candidate (a)'s opclass work before picking both).

Gates run: go build ./... clean; go vet ./cmd/... ./internal/control/...
clean; go test ./cmd/... ./internal/control/... ./internal/server/... PASS.
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0
failed, all 3 workloads, both the manual pre-verification run and the
git-hook run at commit time). Live e2e: real cmd/goopg binary, goopg
init/start/restart/status/stop against a scratch data dir on
127.0.0.1:65498. make ralph-state-guard: 2 benign issues auto-repaired
(identical pattern to every prior loop — status/progress
running-vs-completed reconciliation).

In-flight: none. Manual verification data dir (/tmp/goopg-restart-verify)
was fully torn down (server stopped, directory removed) before this loop
ended.
