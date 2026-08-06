(idle — nothing in flight)

Last loop: **M0127-S7 gate MEASURED — the nightly was run on demand instead of
waited for, and the gate now has exactly ONE named, bisected blocker.**

1. Five loops recorded "no nightly has run since". That was a wait on the 01:00
   scheduler, not on evidence: `make nightly-batch` is the documented manual
   entrypoint and shares the scheduled firing's run lock. Ran a full cycle at
   19:20 JST: **run `20260806-191958`, sha `758ac76e`, 70 min, `status: fail`,
   10 items** (was 18).
2. Stage table: preflight/units/race/pgbench/tpch/tpcds **pass**, only
   `testport` fails ⇒ `status: pass` is attainable. The 14-phantom class did not
   recur (no commit overlapped the run).
3. **8 of the 10 items are one `regress/suite-wedge`** at `multirangetypes` +
   truncated-output casualties (numerology, portals_p2, select, select_into,
   text, truncate, union, varchar). The wedge case MOVED from
   aggregates/jsonb/misc ⇒ cluster/resource condition, not a per-case defect.
4. **Sole engine-side blocker: `TestPort_IsolationEvalPlanQual`** — and it is a
   NEW failure, not the one loop #5 fixed. Loop #5 stands (`47b4aed5` PASSES
   standalone, verified in a worktree). New diff is **L696 on
   `wx1 updwctefail c1 c2 read`** + `delwctefail`: PG raises `ERROR: tuple to be
   updated was already modified by an operation triggered by the current
   command` (`TM_SelfModified`, nodeModifyTable.c); goopg returns rows (1475 vs
   1468). Deterministic 22 s, **flag-independent** (`GOOPG_PGSHAPED_DP` ON/OFF
   both fail) ⇒ not join-search related.
5. **Bisected in 5 tests over `47b4aed5..758ac76e`:** 1547b38a/2af216ba/d8a25def
   PASS, **276e7eda FAIL** (M0125-0052), 6cd5872c/78bd04a4 FAIL. M0125-0052 is
   CORRECT — it made an outer DML see its own CTE's writes — and that removed
   the accidental agreement masking a gap goopg never implemented.

Files: `.ralph/fix_plan.md` (S7 gate sixth-loop amendment + 2 new M-NIGHTLY
items), `.ralph/deferral_ledger.md` (2 rows), design
`leftdeep-joins/09-verification-and-acceptance.md` §3.25 + README index.

Gates run: full nightly cycle `20260806-191958` (the loop's deliverable);
EvalPlanQual standalone at HEAD (FAIL, both flag states) and across 5 bisect
revisions; `make ralph-state-guard` OK after auto-repair; pgbench smoke via the
commit hook. No Go code changed this loop.

NEXT LOOP (banner: M0124 closed → M0125 closed → M0127 → M-NIGHTLY → M0123).
**Implement PG's `TM_SelfModified` error** — filed under M-NIGHTLY but named as
S7's sole engine-side blocker, so it is on the banner's critical path: add the
self-modified check to the DML/EPQ seam M0125-0052 touched (compare the tuple's
writer command id to the current command id; raise
`ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION` before applying). Do NOT revert
M0125-0052. Then re-run `make nightly-batch`; if `status: pass`, take
M0127-P6.1 (delete fusion).

In-flight: none.
