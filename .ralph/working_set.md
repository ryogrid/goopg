Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
corruption. 6th consecutive loop. LANDED a real, verified concurrency
fix this loop (kept, committed) — but it does NOT fully close the
nightly item; a SECOND, separate root cause was found and is the
clear next step.

Files changed (committed): `internal/access/btree/btree_vacuum.go`
(`unlinkEmptyLeaf` now holds `bt.splitMu` across its whole body).
`.ralph/fix_plan.md` + `.ralph/deferral_ledger.md` bookkeeping.

Key symbols: `unlinkEmptyLeaf` (btree_vacuum.go:209, FIXED),
`resolveParentDownlink` (:354), `applyParentDownlinkRemoval` (:478,
removes by captured SLOT INDEX — the bug this loop fixed by adding
splitMu), `removeDownlinkFromParent` (:691, removes by BLOCK NUMBER
match — NOT affected by the index-drift bug, used by the FPI-fallback
path `unlinkEmptyLeafFPI`). `VacuumIndexPages` (:31) — never had
`splitMu` coverage at all before this loop's fix.

Findings this loop:
1. FIXED: `unlinkEmptyLeaf`'s WAL-emitter branch captured a parent
   downlink's slot INDEX, then removed-by-index several statements
   later with no lock held across the gap — a concurrent Insert split
   on the same parent shifts the index, so vacuum deletes the WRONG
   live child's downlink while `leaf.blk`'s own downlink survives and
   gets its block recycled anyway → a later reader follows the stale
   downlink into reused/foreign content → "item length mismatch".
   Fix: wrap `unlinkEmptyLeaf`'s body in `bt.splitMu.Lock()/Unlock()`
   (matches the existing "splitMu serialises all structural changes"
   invariant, which previously didn't actually cover vacuum). No
   deadlock risk: `VacuumIndexPages`'s only production caller
   (`internal/executor/operators_vacuum.go` `vacuumIndexes`) never
   runs from inside `Insert`/`finishSplit`.
2. NOT FIXED — this fix alone does not close the nightly gate. Re-ran
   `REPO_ROOT=$PWD RUN_DIR=$(mktemp -d) NIGHTLY_PGBENCH_PORT=5570 bash
   ci/batch/stages/stage-pgbench.sh` post-fix: still fails, now on
   command 1 (pgbench_accounts UPDATE) instead of command 5, same
   "item length mismatch keyLen=0/9 total=37" signature.
3. Built and validated a MUCH CHEAPER manual repro (~1-2 min instead
   of ~15 min) that narrows this to a SECOND, distinct root cause:
   ```
   go build -o /tmp/goopg-diag ./cmd/goopg
   /tmp/goopg-diag init -D /tmp/diag-data
   /tmp/goopg-diag start -D /tmp/diag-data --listen 127.0.0.1:5590 \
     --hba /tmp/diag-data/pg_hba.conf &
   export PATH="$PWD/postgres/local_install/bin:$PATH"
   export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:$LD_LIBRARY_PATH"
   pgbench -i -s 10 -h 127.0.0.1 -p 5590 -U postgres postgres   # ~60s
   pgbench -c 20 -j 8 -T 30 -h 127.0.0.1 -p 5590 -U postgres postgres
   ```
   A single-client SELECT-only pass immediately after load is CLEAN
   (ruling out a deterministic bulk-load-time bug in `BulkCreate`/
   `deduplicateToRawItems`, which was this loop's first alternate
   hypothesis given "keyLen=9 total=37" exactly matches a 4-TID
   posting item per M0118-0130's math — `marshalPosting` is ONLY ever
   called from `bulkload.go`, since the steady-state insert path's
   `appendTIDToPosting`/`promoteSingleToPosting` in posting.go are
   confirmed dead code, unused per `go vet`). But `-c 20 -j 8 -T 30`
   reproduces within 30s — with a DIFFERENT symptom: "btree: empty
   internal page" (`findChildBlockDirect`'s `count == 0` check,
   btree.go) on the pgbench_branches UPDATE (10 rows — matches the
   long-standing "no-HOT → constant duplicate-key churn on tiny
   tables" theory from loop 5).

Hypothesis for the SECOND bug (empty internal page): audited
`btree_vacuum.go` for internal-page cascade deletion (mirroring PG's
`_bt_pagedel` walking up the parent chain when a page's last child is
removed) — there is NONE. `applyParentDownlinkRemoval`/
`removeDownlinkFromParent` both happily leave a parent internal page
with 0 items once every one of its leaf children has been individually
vacuum-unlinked (very plausible for a tiny, heavily-churned 10-row
branches index that's split enough times to grow a multi-level tree).
Nothing ever re-checks or deletes/merges a 0-item internal page, so
the next descent through it deterministically raises "empty internal
page" — independent of any race, a missing FEATURE not a bug.

Next step: implement recursive internal-page deletion in
`internal/access/btree/btree_vacuum.go` — when a downlink removal
(`applyParentDownlinkRemoval` or `removeDownlinkFromParent`) drops a
parent's item count to 0 AND that parent is not the root, the parent
page itself must be unlinked from ITS OWN parent too (recursively),
analogous to `unlinkEmptyLeaf`'s handling for leaves. The tree-becomes-
fully-empty case is already handled separately (`isTreeEmpty`/
`resetToEmptyRoot` in `VacuumIndexPages`). Use the cheap scale=10/c=20
repro above (NOT the 15-min nightly-scale one) to iterate quickly:
first write a focused unit test proving an internal page CAN reach 0
downlinks under repeated leaf-level vacuum unlinking (probably needs a
tight page-fill / many-small-leaves setup, or drive it via the manual
repro + amcheck), then implement the cascade, matching PG's
`_bt_pagedel` (`postgres/src/backend/access/nbtree/nbtpage.c`) for the
recursion shape. Budget real care here — this is genuine new
structural-deletion logic in a subsystem with a documented history of
rushed fixes causing new panics (`M0055-0004-followup-stage2-splitmu-
removal` comment, btree.go:601-613) — do not rush it in a single loop
if it starts looking risky; a clean partial (e.g. detect-and-log
rather than auto-cascade) is better than a broken cascade.

Gates run this loop: `go build ./...` clean; `go test -count=1
./internal/access/btree/... ./internal/amcheck/...` PASS; `go test
-count=1 ./internal/executor/... -run Vacuum` PASS; `make
ralph-state-guard` — run before finalizing, see status block for
result. Full nightly-scale pgbench repro run twice post-fix (both
failed, as documented above — expected, since this loop's fix doesn't
address the empty-internal-page cause). No executor/planner/codec
change, so no TPC-H spotcheck required beyond the above.

In-flight: none. All diagnostic artifacts removed this loop: `/tmp/
goopg-diag` binary, `/tmp/diag-data` datadir (server force-killed then
directory rm -rf'd, confirmed gone), `/tmp/diag-*.log` files. The
nightly-scale repro's own `RUN_DIR`/datadir under `tmp/goopg-nightly-
pgbench-data-*` were cleaned up by the stage script's own teardown
(confirmed absent). The separate, unrelated live nightly CI batch
(`ci/batch/run-nightly.sh`, started ~00:13 today, currently on its
tpch stage at port 65434) was observed running concurrently — NOT
touched, left running; a stale `goopg-nightly-pgbench.scope` systemd
unit left over from an earlier session attempt was cleared via
`systemctl reset-failed` before this loop's own manual pgbench-stage
runs (safe — distinct from the live nightly's own, different-named
scope).
