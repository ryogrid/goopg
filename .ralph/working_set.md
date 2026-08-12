(idle — nothing in flight)

M-NIGHTLY `AI-20260813-005117-008` (`race/internal/wal
TestCheckpointerVolumeTrigger`) fixed and committed.

Selection note: the previous baton claimed "M0131's only unchecked items are
S9/S24 (both deferred)" — half wrong, and worth re-checking rather than
inheriting. S24 IS explicitly deferred (design 0131-0016, re-arm trigger is a
skipped S28 subtest). S9 is NOT deferred as a whole: it ran to 80/80 pg_catalog
views on 2026-08-12, and only its last sub-slice S9.4 (`information_schema`) was
deferred — into M0133, which the banner files but deliberately does NOT promote.
Net effect is the same (M0131 has nothing selectable, M0130 has zero unchecked
items, so M-NIGHTLY is selectable) but the reasoning differs. Verify milestone
state from the ledger, not from the previous baton's summary.

**The finding worth carrying: the fix_plan's own hypothesis was wrong, and the
item's "confirm before treating it as a checkpointer bug" clause is what caught
it.** The item was filed as a load-sensitive 2 s-deadline flake, by analogy to
the already-fixed `internal/mctx TestMultipleChunks`. It is an unsynchronised
start instead: `Checkpointer.Run` seeds `volumeAnchor` from `WrittenLSN()`
*inside its own goroutine*, while the test that spawned it immediately appends 16
records. A late-scheduled `Run` anchors AFTER those appends, so the writer sits
level with the anchor and the volume trigger can never fire — raising the
deadline would have done nothing. Filing an item by analogy to a previously-fixed
item is a hypothesis, not a diagnosis.

Second finding: `-cpu=1` is a cheap, decisive substitute for "a co-loaded nightly
host". It forced 6/20 failures with the byte-identical nightly message (2.02 s
each) on the old code, and 20/20 passes on the new. Prove a scheduling flake by
constraining GOMAXPROCS before reaching for load simulation or repeat counts.

Third: reordering a lifecycle hook can be the fix. `OnLoopStart` fired as Run's
first statement, so nothing could wait for the loop to be *armed*. Moving both
hooks below the volume-ticker arming makes "started" observable and useful,
with no production change (the only consumer is `initdb.Open`'s
`activity.SetCurrentGoroutine`).

Ledger row filed: the same seeding pattern survives in PRODUCTION, where
`NewCheckpointer` (`initdb.Open:1815`) and `Run` (`cmd/goopg/main.go:806`) are far
apart — WAL appended in between, incl. the end-of-recovery checkpoint, is
absorbed into the anchor and widens the first `max_wal_size` window. Self-limiting
(superseded at the first checkpoint), so it was not fixed here.

Next candidates (all M-NIGHTLY, selectable): `TestPort_IsolationEvalPlanQual`
(REOPENED a 3rd time — two prior fixes did not hold; understand why before a 3rd);
`TestPort_IsolationInsertConflictDoUpdate4` (its no-`4` sibling PASSES → a
per-permutation divergence); PredicateHash / ReceiptReport (open since
2026-08-11, 3 AI-ids each).

Gates: `go build ./...` clean; `go test -race ./internal/wal/` PASS (9.99 s);
`-race -cpu=1 -count=20` on the target test 20/20 PASS (probe, one-off);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (incl. a fresh
`internal/initdb` 236 s — the hooks' production consumer); pgbench smoke PASS via
the commit hook; `make ralph-state-guard` OK.

In-flight: none.
