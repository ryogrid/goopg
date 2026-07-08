Task: M-NIGHTLY pgbench/nightly-reopen-20260708 (AI-20260708-064334-001) —
IN PROGRESS, not complete. Landed a real amcheck false-positive fix this loop;
2 leads still open (see fix_plan.md task + deferral ledger 2026-07-08 row).

Files: internal/amcheck/verify_nbtree.go (VerifyBtreeItemOrder relaxed to
non-decreasing keys), internal/amcheck/verify_nbtree_test.go (renamed
DuplicateKeysViolation -> DuplicateKeysAllowed + new
DecreaseAfterDuplicateViolation test), docs/design/0110-0005-verify-heapam-engine.md
(new subsection), docs/design/README.md (0110-0005 row appended), .ralph/fix_plan.md
+ .ralph/deferral_ledger.md (this loop's row/task update). All committed.

Key symbols: btree.CompareKeys (key-only, no TID tiebreak anywhere in the
engine — this is the root fact behind the fix), VerifyBtreeItemOrder,
btree.PageItemKeys.

Hypothesis/Findings: (1) amcheck's item-order check was too strict (assumed
upstream's heapkeyspace TID-tiebreak model) — FIXED, verified, committed.
(2) Original nightly runtime symptom ("btree: empty internal page" pgbench
transaction abort) was NEVER reproduced this loop (only tried 2x25s windows;
nightly stage uses T=180x3) — still open, unconfirmed whether already fixed
by the 2026-07-07 fixes (510615b4/8ebb71cd, both ancestors of the nightly
commit) or still live. (3) NEW lead found: pgbench_accounts_pkey shows a
genuine, narrowly-targeted "high key invariant violated" on 2 internal
(level=1) blocks where only the LAST item exceeds the page's own high key —
consistent with a downlink misrouted during an internal-page split. This is
reproducible via: fresh initdb, pgbench -i -s 50 once, pgbench -c 100 -j 20
-T 25 -P 5 TWICE back-to-back, then bt_index_check('pgbench_accounts_pkey',
true) — much cheaper than the intermittent runtime-abort repro.

Next step: pick ONE of the two open leads next loop. Recommended: lead (3)
first (deterministic repro already in hand) — instrument insertIntoBlock's
internal-page-split path (internal/access/btree/btree.go ~1420-1660, under
bt.splitMu) using the repro above to catch the exact moment a downlink lands
on the wrong side of a split. If that's fixed and lead (2) still doesn't
reproduce at full T=180 scale, mark the fix_plan task complete (stale/fixed)
per rule 3.

Gates run this loop (all PASS): go build ./...; go vet; go test
./internal/amcheck/... ./internal/access/btree/... ./internal/executor/...
./internal/testport/... (amcheck+btree suites, full); RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh; scripts/tpch-spotcheck.sh (Q12=2/Q13=33);
make ralph-state-guard (self-repaired a stale completed-marker, now consistent).

In-flight: none — all temp servers/probe files/data dirs from this loop were
stopped and removed before finishing (goopg-reopen-check-2/3 systemd scopes,
/tmp/goopg-reopen-check, internal/access/btree/zz_probe_test.go).
