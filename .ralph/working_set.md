(idle — nothing in flight)

Last loop: **M0127 / S7-gate loop #5. Ran the designed prefix bisect for
`TestPort_IsolationEvalPlanQual` (AI-20260806-011323-001), the last open
engine-side blocker. RESOLVED by diagnosis — no code change.**

1. **There is no poisoning predecessor.** EPQ is the 69th of 351 top-level
   tests; all 68 predecessors in nightly order PASS at HEAD (172 s), and PASS
   again under `stage-testport.sh`'s exact cgroup env (`GOOPG_MEM_HIGH=6G
   MAX=8G GOMEMLIMIT=5GiB`). The "order-dependent on a test outside the
   isolation family" conclusion is falsified.
2. **It was never order-dependent.** At the nightly's own sha `23dcc60e` the
   test fails STANDALONE in 22 s with the same L1001 `lockwithvalues` diff.
   The three earlier loops tested a HEAD that already contained the fix.
3. **`git bisect` over `23dcc60e..2d300d14` (22 revisions) → `b92582fb`, the
   M0127-P5.9 default flip itself, is the first fixed commit.** Flag A/B at
   HEAD confirms the mechanism, not just the coordinate: `GOOPG_PGSHAPED_DP=0`
   reproduces the identical L1001 diff, the default PASSes.
4. **The record's `23dcc60e = the P5.9 flip` was wrong** — the flip landed
   after the nightly started, and the nightly builds the LIVE tree, so its
   stamped sha is preflight's. Non-compile twin of the phantom-regression race
   (ci/design/04 §C.1). Ledger row filed to stamp sha-start/sha-end.
5. **The flip MASKS, it does not repair.** `markJoinPreserveCTID`
   (`operators_lockrows.go:430`) has no arm for `multiHashJoinOp` /
   `fusedHashJoinOp`, so under the legacy enumerator FOR UPDATE over those
   nodes takes no tuple lock (root-0038's mechanism, different shape). Carried
   forward as an M-NIGHTLY task; retired by construction when P6.1/P6.2 delete
   both nodes.

Files: `.ralph/fix_plan.md` (item → [x], new masked-defect task, S7 status
amendment), `.ralph/deferral_ledger.md` (2 rows),
`docs/design/leftdeep-joins/09-verification-and-acceptance.md` §3.24,
`analysis/m0127-epq-bisect/` (bisect script + evidence logs).

Gates run: no Go source changed this loop, so no UNITS/SPOT/DS05 — the delta is
trackers + docs + evidence. Repro/A-B evidence IS the verification: 5 passing
EPQ runs at HEAD (alone, pre68, pre68 under nightly cgroup env, 2 bisect
arms), 1 failing at `23dcc60e` alone, 1 failing at HEAD with
`GOOPG_PGSHAPED_DP=0`. pgbench smoke via the pre-commit hook.

NEXT LOOP (banner: M0124 closed → M0125 → **M0127** → M-NIGHTLY → M0123).
**Read `ci/logs/action-items.md` FIRST.** If it is a NEW run and `status: pass`,
the S7 gate is met and **M0127-P6.1 (delete fusion) is selectable** — that is
the intended next task. If it is still run `20260806-011323`, no nightly has
run: the gate is unmet on missing evidence, not on a known defect (all 4 genuine
items of that run are now FIXED or non-reproducing), so pick the topmost open
M0125 item instead and leave P6.1 unchecked.

In-flight: none.
