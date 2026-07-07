(idle — nothing in flight)

M-NIGHTLY (AI-20260707-000712-001) race/internal/wal: FIXED and committed
this loop. Root cause: `state.appendPGCompat`'s Path B (state-loop slow
path for the PG-compat WAL append, used when `tryAppend`'s fast path falls
back) computed its pre-`AppendXLogPayload` headroom check as
`reserveSize - walBuf.free()`, omitting the `walBuf.reservedBytes.Load()`
subtraction the Path A/B gate (`needsDrain`, a few lines above in the same
function) already applies. `tryAppend`'s fast path (RLock — runs fully
concurrently with Path B; only Path A takes `appendMu.Lock()`) claims
`reserveSize` bytes via CAS-protected `walBuf.tryReserve` before its own
`AppendXLogPayload` call, and those claimed bytes aren't reflected in
`resident()`/`free()` until `PublishUpTo` runs — so Path B's stale
`free()`-only check could conclude "enough room" while a concurrent
`tryAppend` claim pushed the combined footprint past `cap`, surfacing as
`errWALBufferReservedOutOfRange` from `writeReserved`
(`TestConcurrentAppendAcrossSegmentBoundariesNoOverflow`). Repro needed
`GOMAXPROCS=2 -cpu=2` (plain `-race` alone: 5/5 pass, didn't reproduce);
at `-cpu=2`: ~1/5 single runs, confirmed at `-count=15`. Fixed
(internal/wal/writer.go, `state.appendPGCompat`) by having Path B
claim/release its `reserveSize` via the same `tryReserve`/
`releaseReservation` CAS pair `tryAppend` uses (loop: drain by
`reserveSize - (free() - reservedBytes)` then retry `tryReserve` until it
succeeds) instead of a plain `free()` comparison. Verified non-vacuous
(revert fails within -count=15 at cpu=2, fix passes 15/15 + separate
8/8). Gates: build clean; `go test -race ./internal/wal/` PASS (3 fresh
runs); `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`
PASS (0 failed pgbench transactions, standard/simple-update/select-only).
Design doc updated: docs/design/0107-0007aj-wal-segment-cross-reservation.md
"2026-07-07 update" section (the original "Concurrency correctness" proof
missed this exact gap) + docs/design/README.md index entry appended.
fix_plan.md AI-20260707-000712-001 bullet checked off with full detail.

Noted but not touched this loop: `.gitignore` shows an uncommitted diff
(adds `!ci/logs`/`!ci/logs/action-items.md` exceptions) that predates this
loop's start (mtime 09:18:58, before my first tool call) and is unrelated
to the WAL fix — left alone per the "concurrent Ralph commits" pattern
(some other process, likely the nightly ci/batch tooling, appears to have
committed `ci/logs/action-items.md` itself mid-session as commit
0a4988e3 without following up with the .gitignore tweak). Not mine to
resolve; a future loop can fold it in if it recurs.

Remaining open M-NIGHTLY items (untouched, still queued, in
ci/logs/action-items.md / fix_plan.md): testport/TestPort_IsolationEvalPlanQual
(AI-20260707-000712-002), testport/TestPort_IsolationEvalPlanQualTrigger
(AI-20260707-000712-003), tpch/Q21-error (-005), tpch/Q15b-MAIN-explain
(-006), tpch/Q9-timeout (-007), tpch/Q20-timeout (-008). The pgbench/nightly
btree keyLen-mismatch item (AI-20260706-201855-001, fixed loop 17 via
8ebb71cd) is still checked-but-unarchived pending tonight's nightly run
confirming it stays clean.

Next step: pick the next M-NIGHTLY item (suggest
testport/TestPort_IsolationEvalPlanQual, AI-20260707-000712-002 — re-run
`go test -v -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/`
at HEAD first per the standing "may be stale" rule before investigating).

In-flight: none. No servers/binaries/data dirs left running; the
pgbench smoke gate's temp data dir under tmp/ was cleaned up by the
script itself.
