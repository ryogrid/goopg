Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. Root cause LOCUS narrowed to buffer-pool eviction
(internal/storage/bufpool.go), NOT the btree package. Fast (~1s) reliable
in-process repro built and committed (skipped by default). See fix_plan.md
task + deferral ledger's 2nd 2026-07-08 row for full detail.

Files: internal/amcheck/verify_nbtree_realtree_test.go (NEW
`buildRealTreeConcurrent` helper + NEW `TestVerifyBtreeEngineSilentOnRealConcurrentContended`,
currently `t.Skip`'d — un-skip to reproduce/verify a fix), .ralph/fix_plan.md
+ .ralph/deferral_ledger.md (this loop's update/row). internal/access/btree/btree.go
was temporarily instrumented with a debug write-log hook during investigation
— FULLY REVERTED, no diff remains there. No production code changed this loop.

Key symbols to instrument next: internal/storage/bufpool.go — claimVictim,
evictVictim, pinLoad, flushSlot. NOT internal/access/btree (ruled out: every
lost item genuinely reaches insertItemSorted; pageItems/parseItem/
parsePostingRaw all correctly copy key bytes into fresh slices, no aliasing).

Hypothesis/Findings: (1) 64 concurrent goroutines calling bt.Insert on a
SHARED narrow key range (n=200000, keyRange=50000, mirrors pgbench's
uniform-random-aid UPDATE churn on pgbench_accounts_pkey — goopg has no HOT
update so every UPDATE re-inserts) with a SMALL buffer pool
(storage.PoolConfig{Slots: 64}) loses 7-16 real leaf entries EVERY run (100%
of ~8 runs). (2) The IDENTICAL workload with Slots: 2048 (no eviction needed
for the ~530-block working set) loses ZERO — this is the load-bearing signal
that isolates the bug to the eviction/flush path, not btree split logic.
(3) A debug hook logging every insertItemSorted call (temporary, reverted)
confirmed lost items DO get written successfully at insert time (no error,
item present in the page right after write per the hook log) but vanish from
the FINAL on-disk tree — i.e. a write gets lost SOMEWHERE AFTER a successful
in-memory insert, consistent with an eviction/flush race dropping a dirty
page's content (or losing track of which slot is dirty) rather than a
double-write/overwrite bug. (4) A prior bug in exactly this area
(evictVictim's bufmap.Delete-before-flush race) was already fixed in commit
510615b4 for M0056-followup-multiwriter-flake — this is very likely a
sibling/remaining race in the same claimVictim/evictVictim/pinLoad machinery,
NOT yet identified at the line level. (5) Also noticed but NOT fixed
(confirmed harmless dead code, out of scope): btree.go lines ~1338 and ~2047
compare op.Next against literal `0` instead of storage.InvalidBlockNumber
(0xFFFFFFFF) — every real page-construction site uses InvalidBlockNumber, so
this comparison is always false and the rightmost-leaf insert-cache
(tryInsertOnCachedRightmost) never actually engages. Confirmed NOT the
data-loss cause (a permanently-dead fast path can't lose data). Worth a
trivial fix in some future loop (pure perf, restores an intended
optimization) but not urgent.

Next step: un-skip TestVerifyBtreeEngineSilentOnRealConcurrentContended
(remove the `t.Skip(...)` line — leave the `testing.Short()` skip), confirm
it still reproduces at Slots: 64 (it does, per this loop), then binary-search
the pool-size threshold (try 128, 256, 512, 1024...) to find where the loss
stops — a narrower threshold shrinks the eviction-frequency window the race
needs, which should make hand-tracing claimVictim/evictVictim/pinLoad's
concurrent interleavings tractable. Add targeted debug hooks THERE (not in
btree.go) this time — e.g. log every (tag, victimIdx, wasDirty) claimVictim
decision and every flushSlot call, then check whether some dirty page's flush
gets skipped, races a concurrent re-load of the same tag, or a pin-count
race lets a dirty page get evicted (not just chosen as victim) while still
being written. Once root-caused and fixed: un-skip the test permanently as
its regression guard, then re-run the ORIGINAL pgbench-based repro
(ci/batch/stages/stage-pgbench.sh, s=50 c=100 j=20 T=180) to check whether
the nightly "empty internal page" abort was the same bug.

Gates run this loop (all PASS): go build ./...; go vet ./internal/amcheck/...
./internal/access/btree/...; go test ./internal/amcheck/...
./internal/access/btree/... ./internal/storage/... (new test skipped by
design, not counted as a pass); make ralph-state-guard (self-repaired a
stale completed-marker from a prior loop's clean exit, now consistent).
Did NOT run the full ralph-precommit-test.sh / tpch-spotcheck.sh pre-commit
gates this loop since no production code is being committed (test-file +
markdown bookkeeping only) — the next loop that lands an actual bufpool fix
MUST run those before committing per the Hard-won Rules.

In-flight: none — all temporary probe files (internal/amcheck/zz_probe_test.go,
internal/access/btree/zz_probe2_test.go) and the temporary debugInsertLog
hook in btree.go were deleted/reverted before finishing this loop. No
background processes left running.
