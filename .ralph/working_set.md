(idle — nothing in flight)

Loop #131 CLOSED **M0131-S31** — the highest-severity open bug (silent wrong
answers on ordinary traffic). Root-caused, fixed, tested, documented.

**Root cause:** `pagePruneCore` (`internal/storage/prune.go`) did
`if item.Flags != ItemIDNormal { continue }`, so an `ItemIDRedirect` root left by
an EARLIER prune was never re-pointed — while the SAME pass reclaimed the (dead,
HEAP_ONLY) slot that redirect addressed. Chain severed: the btree entry resolves
to an LP_UNUSED slot, the row disappears from every index scan while a seq scan
still returns it. Two HOT updates of one row on a page that prunes suffice.
Upstream `heap_prune_chain` starts from redirected roots for this exact reason.
Fixing it exposed a second defect: `pruneChainTip` followed `t_ctid.Offset` past
a dead member whose update was NON-HOT (successor on another block) — the offset
read as a local slot could point the redirect at a FOREIGN live row (`WHERE
id=104` returned two rows in the first build). Both arms fixed, so
`PagePruneOpt` and `PageVacuumPrune` share the repair.

**How it was found (method worth reusing):** binary-search the workload down to a
deterministic repro, then flip suspects off one at a time in a rebuilt binary.
Order that worked: pgbench → single psql session → 15 serial statements
(`analysis/idxprobe2.sh`, ids 101..105 updated 1..5 times) → kill-list disabled
(not it) → call-site prints (non-HOT path never reached; all updates HOT) →
prune disabled (all rows reachable) → print the PruneResult, which showed
`redirects=[[102 227]]` followed by `unused=[227]`.

**Verified:** idxprobe.sh 5576→0 unreachable, idxprobe3.sh 4335→0,
idxprobe2.sh clean, `REINDEX INDEX t_pkey` OK (it was the same defect).

Gates run: units suite PASS (`RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`, warm cache); `scripts/tpch-spotcheck.sh`
RESULT=PASS (Q12 rows=2, Q13 rows=35); `make ralph-state-guard` clean after
self-repair; pgbench smoke via the commit hook.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012), all already filed under M-NIGHTLY; nothing new.

NEXT LOOP: re-read the `## Current Priority` banner (M-NIGHTLY, then M0131).
M0131-S30 still retains its one unmeasured claim (raw `count(*)` loss under a
kill that provably fires) — note that S31's fix may well have been its whole
substance.

In-flight: none.
