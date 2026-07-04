(idle — nothing in flight)

---

**Loop #30 (this session) — COMPLETE, committed + pushed.**

Task: M0122-0003 follow-up — real per-wait-event I/O timing collection
(the concrete next step the `resolved` `track_io_timing` ledger row named:
"a genuine I/O Timings measurement... still needs actual wall-clock
instrumentation added at each wait-event site").

Landed: `internal/activity/registry.go`'s `WaitEventEnd` now returns the
real `time.Duration` elapsed since the matching `WaitEventStart` (reads the
mono-clock `stateChange` stamp before overwriting it). `internal/storage/
bufpool.go`'s `Pool` gains `sharedReadTimeNanos` + `AddReadTimeNanos`/
`ReadTimeNanos`. `internal/initdb/open.go`'s pre-existing `OnPinDone`
closure (already gated on the pinning backend's `track_io_timing` flag)
now feeds the returned duration into `pool.AddReadTimeNanos` — no new gate
needed. `internal/executor/pgstat_io.go`'s `fetchIOStatRows` renders this
as `read_time`, and a drive-by fix wired the already-collected `written`
counter into `writes`/`write_bytes` (previously silently discarded).
Design: `docs/design/0122-0003-explain-format-xml-yaml.md` new "Real
per-wait-event I/O timing" section + cluster-status table row;
`docs/design/README.md` row extended. Ledger row appended
(`.ralph/deferral_ledger.md`, 2026-07-05, M0122-0003).

Concurrency note: a live peer `ralph_loop.sh` tree (screen `ralph`) was
active throughout this loop and landed/pushed two of its own commits
(M0122-0005 ALTER TYPE OWNER/RENAME `6bd15f3b`/`f3ecc868`, then a
stale-entry bookkeeping fix `7b6dff11`) in fully disjoint files — confirmed
via `git status` before every commit here, no conflicts. The peer's
`7b6dff11` incidentally swept up this loop's in-progress `.ralph/fix_plan.md`
edit (shared bookkeeping file, no worktree isolation) into their commit —
harmless (content was correct, now already pushed), so this loop needed no
separate `fix_plan.md` commit of its own. The peer's own working_set.md
carry (their loop #34, M0122-0005 stale-entry verification) is superseded
by this entry; see git log for that loop's detail if needed.

Next step: pick up the next M0122-0003 sub-item — `write_time` needs a new
`OnWait`/`OnDone` hook pair around `evictVictim`'s flush (no existing hook
there, unlike the read side), or move to a different fix_plan priority
(M0119-0004 pg_dump DU-002 slice, M0122-0005's remaining sub-items, or
M0122-0004's remaining `dense_rank()`/frame-clause work). Re-check `git
status` fresh at loop start — do not assume this snapshot.

Gates run: `go build ./...` clean; `go vet` clean on all 4 touched
packages; `go test -count=1 ./internal/activity/... ./internal/storage/...
./internal/executor/... ./internal/server/...` PASS (no regressions);
`go test -count=1 ./internal/initdb/...` PASS (full package, background,
~467s); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `make
ralph-state-guard` — 1 skew auto-repaired (prev loop's clean-exit marker),
exit 0 after repair.
