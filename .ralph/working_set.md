Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
corruption. 12th consecutive investigation loop. NO code landed this
loop (pure investigation, all instrumentation reverted, `git diff
--stat` clean) — but landed the most decisive finding in this 12-loop
thread: conclusive proof the bug is `insertIntoBlock`'s split-right-page
path (NOT `createNewRoot`), and that the corrupted content survives a
real evict-then-reload roundtrip through the smgr disk layer.

Files touched this loop (all reverted before finishing, zero diff):
`internal/storage/debugtrace_temp.go` (new temp file, deleted),
`internal/storage/bufmap.go` (Insert/Delete trace calls, reverted via
`git checkout --`), `internal/storage/bufpool.go` (claimVictim/PinNew/
pinLoad/Pin/pinSlow trace calls, reverted), `internal/access/btree/
btree.go` (`SITE_NEW_ROOT`/`SITE_SPLIT_RIGHT` call-site markers +
`descendToLeaf`'s error-enrichment with `DumpSlotTraceFor`, reverted),
`internal/access/btree/multi_writer_stress_test.go` (un-skip +
`storage.ResetSlotTrace()`, reverted). `git status --short` confirmed
clean except the pre-existing untracked `postgres` dir.

Key finding (THE breakthrough this loop): built bufmap/bufpool-level
tracing (blk/slot/gen/bucket on every Insert/Delete/claim/publish) plus
two NEW call-site attribution markers. Running with `GOMAXPROCS=4`
sharply raised the repro rate (0/850 iterations unconstrained across
loop 11 + this loop's first attempt, vs. 11 failures across two
400-iteration GOMAXPROCS=4 runs — logs at /tmp/slottrace_run{1,2,3}.log,
not committed, may be gone by next loop). Every failure shows the
IDENTICAL signature: PinNew creates the block → `claimVictim`
legitimately evicts it (only possible once the creating writer's own
Unpin already ran, meaning by this code's own logic the page WAS fully
populated+dirty+WAL-logged+unlocked first) → cache-miss reload reads
back a virgin `storage.InitPage` signature. Of 10 attributable failures,
ALL 10 show `SITE_SPLIT_RIGHT`, ZERO show `SITE_NEW_ROOT` — conclusively
pins this to `insertIntoBlock`'s split-right-page allocation
(`bt.pinNewOrRecycled()`→`pinNewLocked()`, btree.go:1493/692), not
`createNewRoot`. Also refuted (again, empirically): duplicate-tag-
mapping in bufmap (zero `BM_INSERT_DUP_REFUSED` events near any failing
block) and stale-slot/wrong-tag resolution (zero `*_TAG_MISMATCH`
events). Full trace excerpts + reasoning in today's deferral-ledger row
and the matching fix_plan.md M-NIGHTLY update #10.

Ruled out this loop (static re-audit): `createNewRoot`'s raw
`bt.pool.PinNew(bt.rel)` + separate `.Lock()` two lines later (unlike
`pinNewOrRecycled`'s `pinNewLocked` wrapper) — confirmed NOT
exploitable: the creating goroutine holds the ONLY pin throughout that
gap (`claimVictim` requires pinCount==0), and nothing can reach the new
root before the metapage update, which happens strictly after full
population+unlock+unpin. Worth a cheap consistency fix later
(`createNewRoot` → `bt.pinNewLocked()`) but confirmed not the cause.
Also newly noted (separate, minor, not this bug): `insertIntoBlock`'s
dedup-avoids-split branch (btree.go:1531-1547) permanently leaks the
freshly-allocated `rightSlot`'s block number (never linked into the
tree) every time dedup avoids a split.

Next step: the search space is now "does the split-right page's
content get corrupted (a) upstream, inside the split's own
populate-then-unlock window (btree.go:1493-1668, e.g. a stale/aliased
byte-slice write), or (b) in the disk I/O roundtrip itself
(`relFile.writeBlock`/`extend`/`readBlock`, smgr.go)". Add ONE more
instrumentation layer to localize between these: sample
`storage.PageLinePointerCount(s.page)` (or a raw byte checksum)
immediately BEFORE `evictVictim`'s `flushSlot` call in bufpool.go — if
it ALREADY reads empty there, the bug is upstream in btree.go's split
populate path (next audit target: everything between `initPage`
(btree.go:1513) and `rightSlot.Unlock()` (btree.go:1668), especially
`rightItems`' handling in `pageItems`/`dedupConsolidate`/
`compactRawSize`, which loops 9-10's copy-safety audit did NOT cover —
that audit only checked the LEFT-side dedup path). If it reads correct
(non-empty) there, add a matching checksum log in `relFile.writeBlock`
right after `WriteAt` returns and in `relFile.readBlock` right after
`ReadAt` returns (smgr.go ~596-718) — a mismatch there localizes to the
smgr/file-handle layer instead.

Gates run this loop: `go build ./...` clean (both with instrumentation
and after reverting). `go test -count=1 ./internal/access/btree/...
./internal/storage/... ./internal/amcheck/...` PASS post-revert. No
executor/planner/codec change, so no TPC-H spotcheck required. `make
ralph-state-guard` run before finalizing.

In-flight: none. Cheap repro for next loop (now with a HIGHER hit rate
than any prior loop's recipe): `GOMAXPROCS=4 GOOPG_SLOTTRACE=1 go test
-run TestMultiWriterStress_M0055_Phase_C -count=400 -timeout 20m
./internal/access/btree/...` (recreate this loop's instrumentation
verbatim per the deferral ledger row first; un-skip the test's
`t.Skip` at line ~40 and add `storage.ResetSlotTrace()` right after).
~200s wall time per 400-iteration run, observed 3-8 failures per 400
iterations with GOMAXPROCS=4 (vs. 0-2 per 400-600 unconstrained in
prior loops) — a substantially better repro rate to build on. Zero
setup/cleanup required beyond the test process itself. No
servers/data dirs/background processes started this loop. Separate
live nightly CI batch (`ci/batch/run-nightly.sh`) not touched.
