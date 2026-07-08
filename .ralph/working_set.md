Task: M-NIGHTLY pgbench/nightly-reopen-20260709 (AI-20260709-010336-082) —
4th loop. This loop's fix (VACUUM sibling-relink race) is DONE + committed.
Follow-on: a NEW corruption class (block-4026 high-key overrun) surfaced by
this loop's own verification repro and is NOT fixed yet — next loop's task.

Files:
- internal/access/btree/btree_vacuum.go — FIXED: `unlinkEmptyLeaf` /
  `unlinkEmptyLeafFPI` no longer blindly write the leftLive/rightLive values
  from the unlocked `liveSibling` pre-pass; both now re-derive the live
  neighbour via a FRESH `liveSibling` walk (from the sibling's CURRENT
  on-disk Prev/Next) INSIDE the same pinW hold that performs the write.
- internal/access/btree/btree.go — NOT touched; `insertIntoBlock` (~line
  2114) is the prime suspect for the block-4026 finding below.

Key symbols: unlinkEmptyLeaf/unlinkEmptyLeafFPI (fixed), liveSibling
(reused), insertIntoBlock (next suspect), splitMu (per-*BTree-instance,
confirmed by an earlier loop to NOT serialize cross-connection — root
enabling condition for both this loop's fixed bug and the new finding).

Findings: root-caused THIS loop by code reading alone (no new
instrumentation needed) — prior loop's block-678 bug was VACUUM's
unlinkEmptyLeaf stomping a concurrent split's correct sibling relink with a
stale precomputed value; splitMu didn't stop it because each connection
opens its own *BTree instance. Fixed + verified via a fresh repro (isolated
port 5533, `pgbench -i -s 10 --no-vacuum` then `pgbench -c 60 -j 12 -T 30`
racing a `VACUUM pgbench_accounts` loop every 0.3s): 0 failed txns, no more
Prev/Next mismatches post-run. That SAME repro found a NEW, different bug:
`bt_index_check` reports "high key invariant violated ... block 4026" —
direct dump confirmed block 4026 (internal, level=1) has its LAST downlink
key exceeding its own HighKey. Root cause hypothesis: `insertIntoBlock` has
no Lehman-Yao "move right" check — it never verifies the target block's
current HighKey against the item before inserting, so a concurrently-split
target (leaf or a stale ancestor in `path[]`) can receive an out-of-range
item. NOT fixed — bigger blast radius (hottest insert path), needs its own
loop with live-instrumentation verification before landing.

Next step: add a move-right pre-check in insertIntoBlock right after
pinW(blk)/before pageHasSpaceFor, using the same HighKey boundary test
VerifyBtreeItemOrder already uses (internal/amcheck/verify_nbtree.go:220-229)
to DETECT this; step right via op.Next on failure instead of inserting in
place. Full repro recipe + detailed forensics are in the deferral ledger row
dated 2026-07-09 (task-id "M-NIGHTLY (AI-20260709-010336-082, 3rd pgbench
reopen)", status resolved, "deferred"/"resume point" columns) and in
fix_plan.md's matching M-NIGHTLY bullet. Data dir preserved (gitignored) at
tmp/perf-optimize/reopen-verify-data for the next loop to skip the repro.

Gates run: go build clean; go test ./internal/access/btree/...
./internal/amcheck/... ./internal/executor/... PASS; tpch-spotcheck.sh PASS
(Q12=2/Q13=33); make ralph-state-guard OK (self-repaired stale marker).

In-flight: none. goopg on 5533 stopped cleanly; no lingering systemd scope.
