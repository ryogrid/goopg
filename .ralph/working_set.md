Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
corruption. 7th consecutive loop. LANDED and VERIFIED the second of
(at least) three root causes this loop. The nightly item is STILL
open — a third, distinct root cause reproduces only at full
c=100/j=20/s=50 scale.

Files changed (committed): `internal/access/btree/btree_vacuum.go`
(new `maybeCascadeEmptyInternal`/`unlinkEmptyInternalPage`/
`unlinkEmptyInternalPageFPI`; `resolveParentDownlink`/
`findParentDownlinkByBlock`/`removeParentDownlinkByBlock` now also
return the root..parent ancestor chain). New test
`internal/access/btree/btree_vacuum_internal_cascade_test.go`
(`TestVacuumIndexPagesCascadesEmptyInternalPage`). `.ralph/fix_plan.md`
+ `.ralph/deferral_ledger.md` bookkeeping (2 new rows).

Key symbols: `maybeCascadeEmptyInternal` (btree_vacuum.go, loops
upward unlinking non-root internal pages whose item count hits 0),
`unlinkEmptyInternalPage`/`unlinkEmptyInternalPageFPI` (WAL/FPI twin
of `unlinkEmptyLeaf`/`unlinkEmptyLeafFPI` but for a cascaded internal
page — no BTHalfDead phase-1 marker, see crash-safety note in the
function doc + ledger). `ancestorPath` is now threaded out of
`resolveParentDownlink` (root..parent inclusive) so a caller whose
downlink removal empties the parent can keep cascading without
re-deriving the chain (impossible once the parent holds 0 items).

Findings this loop:
1. FIXED + VERIFIED: implemented the internal-page cascade that the
   6th loop's own next-step called for. New regression test builds a
   genuine 3-level tree (n=900000 int4 keys via BulkCreate — probed
   empirically: n=250000 first reaches a 3-level tree but with only 2
   root downlinks; n=900000 gives >=4, letting the test pick a
   non-edge middle child), empties one whole non-root internal page's
   leaf subtree in one `VacuumIndexPages` call, and asserts the tree
   stays fully readable + the cascaded page is unlinked from its
   parent. Confirmed the test FAILS on pre-fix code (`git stash` the
   fix, rerun — fails with "cascaded internal page ... still live")
   and PASSES post-fix, so it is a real regression test.
2. VERIFIED against the real repro: re-ran the cheap scale=10/c=20
   manual repro from the 6th loop (build /tmp/goopg-diag, init, start
   on 127.0.0.1:5590, `pgbench -i -s 10`, then `pgbench -c 20 -j 8`)
   for 30s then again for 60s (90s total) — 0 failed transactions
   both times, no "empty internal page" / "item length mismatch" in
   the server log (previously failed within 30s pre-fix). All
   diagnostic artifacts removed after (binary, data dir, logs, PID
   killed and confirmed gone).
3. STILL OPEN — third root cause at full scale: re-ran the
   AUTHORITATIVE repro post-fix: `REPO_ROOT=$PWD RUN_DIR=$(mktemp -d)
   NIGHTLY_PGBENCH_PORT=5570 bash ci/batch/stages/stage-pgbench.sh`
   (s=50 c=100 j=20 T=180x3, port 5570 to stay clear of the live
   nightly batch's own 65434). FAILED AGAIN — same original signature
   (`btree: item length mismatch keyLen=9 total=37`, occasionally
   keyLen=0 or keyLen=7460) on the very first workload
   (pgbench_accounts UPDATE), ~78/100 clients aborting within ~30s.
   Neither the splitMu fix (6th loop) nor this loop's cascade fix
   touches this — it needs the higher c=100/j=20/s=50 concurrency the
   cheap manual repro doesn't reach. `RUN_DIR` (a `/tmp` mktemp dir)
   was inspected then removed (`$RUN_DIR/pgbench/pgbench.log` +
   `server.log`, no leftover goopg process on 5570, no leftover data
   dir under `tmp/goopg-nightly-pgbench-data-*` — script's own
   teardown handled cleanup, matching the pattern from prior loops).

Hypothesis for the THIRD bug: NOT yet re-derived this loop (out of
budget) — the 5th/6th loops (see fix_plan.md M-NIGHTLY item, "2026-07-07
update #3" entry, and its own deferral ledger rows) already spent
several loops on this exact same symptom via a DIFFERENT repro
(`TestMultiWriterStress_M0055_Phase_C`), and reached a REFUTED "unlocked
write" hypothesis (confirmed zero -race reports across ~1180
iterations) redirected toward "a logic bug in properly-synchronized
code, triggered only by the FULL heap+index+MVCC stack under
UPDATE-heavy contention on tiny shared tables (TPC-B's
pgbench_branches/pgbench_tellers) — something insert-only/disjoint-key
tests structurally cannot exercise". That thread's flagged-but-not-yet-
inspected candidates: `internal/access/btree/posting.go` dedup/
posting-list code, and the item/line-pointer length encoding in
`insertItemSorted`/`pageItems`/`findChildBlockDirect`.

Next step: resume the 5th/6th-loop investigation thread (NOT a new
one) using their cheap ~15-min repro recipe: build `bin/goopg-race`
(`go build -race ./cmd/goopg`), init, start with
`GORACE=log_path=...`, `pgbench -i -s 50`, then `pgbench -T 80 -c 100
-j 20 -P 5` — fails within the run. Inspect the two flagged candidates
above first (posting-list dedup on hot/shared pages under concurrent
UPDATE churn; length-encoding paths) before opening any new
instrumentation thread. Full history/rationale in fix_plan.md's
M-NIGHTLY item (search "update #3") and its matching deferral ledger
rows (2026-07-07, "MAJOR REDIRECTION").

Gates run this loop: `go build ./...` clean. `go test -count=1
./internal/access/btree/... ./internal/amcheck/...` PASS. `go test
-count=1 ./internal/executor/... -run Vacuum` PASS. `go test -race
-count=1 ./internal/access/btree/...` PASS (18.6s, no race reports).
Manual scale=10/c=20 repro clean x2 (30s + 60s). Full nightly-scale
`stage-pgbench.sh` re-run: still RED (third, distinct root cause, see
above — expected, not a regression from this loop's fix). `make
ralph-state-guard` — run before finalizing, see status block for
result. No executor/planner/codec change, so no TPC-H spotcheck
required beyond the above.

In-flight: none. All diagnostic artifacts removed: `/tmp/goopg-diag`
binary + `/tmp/diag-data` datadir (server force-killed via captured
PID, directory rm -rf'd, confirmed gone), the `stage-pgbench.sh`
RUN_DIR (`/tmp/tmp.jOKznQ0AXh`, a plain mktemp scratch dir — its own
teardown already killed the port-5570 server and removed the nightly
data dir; I additionally rm -rf'd the RUN_DIR itself after extracting
the log excerpts above). The separate, unrelated live nightly CI batch
(`ci/batch/run-nightly.sh`, PID 264733, started ~00:15 today) was
observed still running its TPC-H stage on port 65434 throughout this
loop — NOT touched, left running.
