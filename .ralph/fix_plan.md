# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**. As of 2026-07-28
the banner puts **M0124 → M0125** (closing the TPC-DS round-2 plan, per
`docs/design/tpcds-round2-fixes/README.md` §13.5) at the top of the roadmap,
ahead of M0123 and every other milestone. **Amended 2026-07-31 (USER): M0126 —
cost-driven planning made production-viable — is inserted directly after M0125,
so the head of the roadmap is M0124 → M0125 → M0126.** **M-NIGHTLY no longer
preempts it (amended 2026-07-28): nightly items are still FILED every loop, but
they are not SELECTED until M0124, M0125 and (since 2026-07-31) M0126 close.** This banner is the sole ordering
authority — `.ralph/working_set.md`'s "NEXT LOOP" note carries state, not
priority, and does not outrank it.

## Notes / rules

- This is the authoritative TODO list for Ralph. Update it after every meaningful
  change (tick boxes, add newly-discovered follow-ups). ONE item per loop;
  decompose any item larger than a single agent invocation.
- Every non-trivial subsystem must land with (or just before) a design doc under
  `docs/design/<id>-NNNN-*.md` **and** a `docs/design/README.md` index entry —
  hard requirement, same loop.
- Deferrals: never close a task silently with a forward reference. Append one row
  to `.ralph/deferral_ledger.md` (`date | task-id | landed | deferred | resume
  point | why`) and leave the fix_plan item unchecked. **The ledger is the source
  of truth for every "DEFERRED" note below** — consult it for full context/resume
  points.
- Completed milestones are archived under `completed_milestones/` (latest:
  `completed_fix_plan_008.md`); they are reference-only, NOT actionable, and must
  not be copied back here.

## Current Priority (per 2026-07-28 directive)

**Standing FILING obligation (amended 2026-07-28 — replaces the former
"M-NIGHTLY triage items preempt everything below" exception):** every loop still
reads `ci/logs/action-items.md` and files each new `## AI-` subject as a task
under the M-NIGHTLY milestone directly below this banner. **Filing is
unconditional; selection is not.** M-NIGHTLY work is PARKED beneath M0124/M0125
and its tasks stay unchecked until both milestones close. Exactly two carve-outs
may be worked immediately, because the parked milestones cannot be *measured*
without them:

- an item that breaks the build, and
- an item that breaks a gate M0124/M0125 depend on — `scripts/tpch-spotcheck.sh`,
  the TPC-DS SF0.5 gate, `make plan-diff`, or a bench cluster
  (65432/65433/65436/65437/65438).

Everything else is filed and left unchecked. Rationale: every loop from
`ddfb035e` (root-0029) through root-0036 went to nightly triage while the TPC-DS
round-2 closeout — the measurement every M0125 task diffs against — never
started.

**⚡ 2026-07-28 directive — branch `tpcds-fix2`, priority order for this loop
(SUPERSEDES the 2026-07-18 directive below):**
> `docs/design/tpcds-round2-fixes/README.md` §13 audited the TPC-DS round-2 plan
> against itself: four of twelve phases landed as planned, four with a named gap,
> four never started; seven of nine planned deferral-ledger rows were never
> appended; and §13.3's current status is a **projection, not a measurement**.
> §13.5 lists the smallest set of actions that would close the plan. They are now
> filed as **M0124** (measurement baseline, regression-gate discharge, ledger
> debt) and **M0125** (timeout class, Q75, walker extinction), and they are the
> **top priority of this checkout**. Work in THIS order:
>
> **⚡ AMENDED 2026-07-31 (by the USER) — M0126 IS INSERTED BETWEEN M0125 AND
> THE M-NIGHTLY BACKLOG.** The order below is amended to read: WIP recovery
> (#1) → **M0124** (#2) → **M0125** (#3) → **M0126** (#4, NEW) → M-NIGHTLY
> backlog (#5) → M0123 (#6). **M0126 — cost-driven planning made
> production-viable** turns `analysis/cost-driven-second-try-200731/` into
> shipped behaviour and ends with the conditional default flip of
> `GOOPG_COST_DRIVEN_JOINORDER` (or a documented no-go; on a no-go, one
> USER-filed conditional remediation — build-side memory-aware `hashJoinCost` —
> then a re-measured final verdict). Milestone doc
> `docs/milestones/0126-cost-driven-planning-production-viability.md`; the task
> list is the `## M0126` section near the bottom of this file. It is **not**
> selected while any M0125 item is open. The numbered list below is unchanged
> and kept as filed; read it with this amendment applied.
>
> 1. **WIP recovery** — one-time; restore & resolve any pre-switch WIP (the
>    "WIP recovery" item directly under this banner) before anything else, never
>    silently drop it. (Nothing outstanding as of 2026-07-28.)
> 2. **M0124** — TPC-DS round-2 closeout. Milestone doc
>    `docs/milestones/0124-tpcds-round2-closeout-measurement-and-gate-debt.md`.
>    **↳ M0124 IS FULLY CLOSED (2026-07-30).** `M0124-0004` — the last open item —
>    was closed on its CLASSIFY branch: Q35 is **performance-only, RC-8 shape**,
>    its row count is **not recoverable by any budget** (warm floor ≈9.1 days at
>    SF=1), and it is now **M0125-0003's acceptance query**. Nothing in M0124
>    remains; **select from M0125's ordered list in step 3 below** — items 1–4
>    there are done, so the next selection is **item 5 (`M0125-0014`/`-0015`,
>    Q49/Q51 SF=1 re-measure)**. Historical detail follows.
>    **↳ NEXT TASK TO SELECT (updated 2026-07-29 late): `M0124-0001` is CLOSED,
>    and so are `-0002` (discharged this loop — `plan_snapshots/tpcds-round2-head.txt`
>    now EXISTS and is committed), `-0003`, `-0005` and `-0006`. The only M0124
>    item left is `M0124-0004` (Q35's row count). **It needs a QUIET host** —
>    its two previous readings were voided by the nightly CI batch. Check
>    `ci/batch/run-nightly.sh` is not running before selecting either; the
>    harness guards now refuse to start otherwise (`FORCE=1` overrides, and is
>    only legitimate for value/row-count work, never for a timing).** Do not select a regress/testport case instead: as of the
>    2026-07-28(b) amendment M-NIGHTLY no longer preempts;
> 3. **M0125** — TPC-DS timeout class & planner expression-walker extinction.
>    Milestone doc
>    `docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md`;
>    **↳ NEXT TASK TO SELECT (added 2026-07-29 17:25 by the USER — this list
>    OUTRANKS `.ralph/working_set.md`'s NEXT note, which currently says
>    `M0125-0020`). Take them in THIS order; do not fall back to file order:**
>    1. ~~**`M0125-0003` stage 1**~~ — **DONE**, and **stage 2 is DONE too
>       (2026-07-30)**: the bushy DP seed is wired and default-off, proven inert
>       flag-off (plan-diff 22/22 MATCH) and proven LIVE flag-on (22/22 DIFFER,
>       replacing a flat `rows=1` seed for every relation with block-derived
>       sizes). **STAGE 2's TIMED C-ARMS ARE NOW READ (2026-07-30,
>       `analysis/tpch-relsize-fallback-20260730.md`, quiet host): 1.40× on the
>       TPC-H stream, four wins to 3.4×, ZERO regressions, identical rows —
>       §D5.3's risk statement is REFUTED for stage 2 on TPC-H,** so stage 3 is
>       no longer shadow-blocked. **§D8's SF0.5 GATE AT
>       `GOOPG_RELSIZE_FALLBACK=2` IS NOW RUN TOO (2026-07-30,
>       `analysis/m0125-0003-sf05-relsize-20260730/`, quiet host, four chunks on
>       one binary): the timeout class shrinks 16 → 13, `PASS` 79 → 82, ZERO
>       MISMATCH/CKMISMATCH/ERROR, all 78 common PASSes agree on rows AND value
>       checksum, common-PASS wall time −18.8 %.** Four rescues (Q10 40 s,
>       Q69 17 s, Q67 157 s, Q47 277 s — the last also closes M0125-0013's
>       runtime half at SF0.5) against one cost: **Q72 is 1.13× slower and
>       crosses the cap** (900 s probe: off 270 s, on 305 s, so it is a budget
>       crossing, not a hang). **Both of §D8's predictions are refuted: Q72 was
>       already passing, and Q35 — this task's own acceptance query per
>       M0124-0004 — still times out, so the fallback is NOT what Q35 was
>       waiting for** (ledger row: the RC-8 re-scan class needs its own task,
>       to be filed off M0125-0026's classification). What remains owed on
>       -0003 is the W arms (measured *unconstructible* — `ANALYZE` in db
>       `tpch` errors; ledger row) and stage 3. **NEXT SELECTION: `M0125-0005`,
>       the default flip — its TPC-H *and* TPC-DS evidence are now both in and
>       both recommend it** (its remaining own work is the `tpch-spotcheck.sh`
>       wall-clock + peak-RSS re-measurement and the written decision); then
>       `M0125-0002` commit 2, stage 3, or `M0125-0026`. Original wording follows.
>       It is the ONLY designed item that can move the
>       **17-query timeout class** this milestone is named after — fix_plan calls
>       it "§13.5's highest-value item (15–16 of 21 defects); stage 1 is inert, so
>       it lands early". This banner has said "M0125-0003 (flag-off throughout) are
>       unblocked" since 2026-07-28, and only the **measurement half** is gated by
>       M0124-0002. `working_set.md` has re-classified it as "needs a four-arm
>       TIMED study → host blocker" for six consecutive loops: that is true of the
>       four-arm study, **not** of the shape-neutral stage-1 landing. Land stage 1;
>       defer the timed arms with a ledger row if the host is busy.
>    2. ~~**`M0124-0002`**~~ — **DONE 2026-07-29** (`analysis/tpch-tpcds-round2-retro-20260729.md`).
>       `plan_snapshots/tpcds-round2-head.txt` exists and is committed, so
>       **M0125-0002 / -0004 / -0005 and M0125-0003 stage 2 are unblocked** — that
>       was the largest single unblock available and it has been taken. Use
>       `make plan-diff LABEL=tpcds-round2-head`, never `plan-gate`, when a
>       *specific* baseline matters (`plan-gate` picks newest-by-mtime).
>    3. **`M0125-0012` (Q8).** UNBLOCKED: its only dependency was the *soft* one on
>       M0125-0001, which landed at `6c5c48ae`. `working_set.md`'s claim that
>       -0020 is "the last unblocked M0125 item" is FALSE — that enumeration omits
>       -0012…-0015 entirely. Q8 is the only unresolved member of round 1's nine
>       goopg-only errors and reproduces at SF0.5 in 12 s.
>    4. ~~**The full 99-query SF0.5 gate**, once~~ — **DONE 2026-07-29**,
>       `analysis/tpcds-sf05-full-gate-20260729/`. **99/99 covered at HEAD
>       `50cf7c5f`, ZERO regressions**: `PASS=75 (46 ck-verified) MISMATCH=1
>       CKMISMATCH=3 ERROR=1 TIMEOUT=15 SKIP=4`. Eight statuses differ from the
>       `sweep-20260727-214619` baseline and all eight are accounted for (Q8
>       ERROR→TIMEOUT = M0125-0012's fix; Q16/Q94/Q95 PASS→CKMISMATCH = the
>       baseline had **no** value checksum, so M0124-0005 detected three silent
>       wrong answers — one defect, **M0125-0007**; Q49/Q51 MISMATCH→PASS = fixed;
>       Q72/Q88 TIMEOUT→PASS = the 180s→300s cap, no code). The eight loops of
>       debt behind -0016 … -0021 are discharged in one pass; none of the six is
>       owed a follow-up. Run as four contiguous `QUERIES=` chunks on ONE binary
>       (~110 min total exceeds a loop's foreground budget) — the README explains
>       why chunking is sound and what it cannot see.
>    5. ~~**`M0125-0014` / `-0015`** (Q49 / Q51 SF=1 re-measure)~~ — **DONE
>       2026-07-30** on a verified quiet host, `analysis/m0125-0014-0015-q49-q51-sf1/`.
>       The SF=1 reading the items demanded closes **both**: Q49 `OK 83 s / 34
>       rows` and Q51 `OK 47 s / 100 rows`, each with a value checksum equal to
>       PG's. The SF0.5 PASS was indeed only evidence — but the full-fact-table
>       defect SF0.5 could have hidden was not there. Neither mechanism was ever
>       root-caused; both closed at STEP 0 as *measured-and-already-fixed*, the
>       M0124-0004 shape. Attribution stays where the SF0.5 bisect put it
>       (**M0125-0009**) and is NOT confirmed by this run.
>    **↳ ALL FIVE ORDERED ITEMS ARE DISCHARGED, and so is the fall-back:**
>    **`M0125-0007`** (unpadded month/day date decode) **landed 2026-07-30** —
>    `internal/pgdatetime` + every executor date/time entry point; the three
>    Q16/Q94/Q95 `CKMISMATCH` cells went from one shared wrong-answer checksum to
>    three distinct real answers. It did not turn them green, and the probe that
>    proved it also named what remains behind each, which sets the next order:
>    1. ~~**`M0125-0008`** (EXISTS + NOT EXISTS over one outer rel)~~ — **DONE
>       2026-07-30.** One derivation in `Join.Output()` (Semi/Anti publish
>       `Left.Output()` instead of a cached copy `rewriteMultiWayChain`'s
>       in-place OID re-sort had made stale). Closed **all three** CKMISMATCH
>       cells, not two: Q16 `40dbec0df91d2438`, Q94 `04afc1b69831a5ea` **and
>       Q95 `e498634c02595c29`**, each matching the SF0.5 oracle exactly.
>    2. ~~**`M0125-0023`** (Q95)~~ — **DONE 2026-07-30, same fix.** Its filing
>       premise ("no `EXISTS`, therefore not -0008") classified by SQL keyword
>       rather than by the join the planner builds; `IN (subquery)` unnests to
>       the same `JoinTypeSemi`.
>    3. ~~**`M0125-0013`** (Q47)~~ — **DONE 2026-07-30.** Closed on a refuted
>       premise: the defect was in the CTE body, not the windowed layers above
>       it — RC-1b had measured the body by ROW COUNT, and the row count was the
>       half that was already correct (332,240 = PG). The projection read other
>       relations' columns, so `rank()` returned 1 for every row and the `v2`
>       self-join on `rn = rn+1` matched nothing. One arm of
>       `buildBindingsPosMap` (`*MultiHashJoin` matched only bare scans, skipping
>       `*Filter`-wrapped leaves without advancing `off`). Q47: 0 → **100 rows =
>       oracle**. Its *runtime* verdict is deliberately NOT closed and is filed
>       as the bookkeeping half below.
>    4. ~~**`M0125-0005`**~~ — **DONE 2026-07-30: the default IS FLIPPED**
>       (stage 2). Its owed spotcheck measurement came in at **2.43× FASTER**
>       (75.0 → 30.9 s) with peak RSS unchanged and `Q12=2 / Q13=35` in all
>       five runs, so the flip made the mandatory gate cheaper rather than
>       costlier. Carried costs, which must not be dropped when this is cited:
>       **Q72 1.13× slower and unexplained**, and **Q35 still TIMEOUTs**. The
>       plan baseline is re-pinned to
>       `plan_snapshots/m0125-0005-relsize-default-stage2.txt` — **any
>       `analysis/` number predating 2026-07-30 is in a different planner
>       regime and must not be compared across this commit.**
>       ~~**NEXT SELECTION: `M0125-0002` commit 2** (`cloneExprShiftIdx`), then
>       `M0125-0003` stage 3, and **`M0125-0026`** (below).~~ *(SUPERSEDED
>       2026-07-30(b): the USER DIRECTIVE block at the end of item 3 puts
>       `M0125-0028` → `-0029` → `-0030` ahead of these; -0002 commit 2 and
>       the -0003 four-arm study follow AFTER the warm flip.)*
>       **↳ RACE, resolved 2026-07-30: `M0125-0002` commit 2 had ALREADY been
>       selected and fully gated when the (b) directive landed mid-loop, so it
>       is COMMITTED rather than discarded — see its item body. It costs the
>       warm programme nothing, because its verdict is not a timing: TPC-H
>       22/22 byte-identical, TPC-DS SF0.5 96/96 byte-identical `EXPLAIN`,
>       answer sweep 0 MISMATCH/CKMISMATCH/ERROR. Read it as regime-bound all
>       the same — it says "the conversion changes no plan **at S-cold**", and
>       `-0030`'s warm flip is exactly what makes S-cold stop being the
>       measured state. **`M0125-0028` LANDED 2026-07-30 (loop #8): ANALYZE/VACUUM
>       named targets + bare `ANALYZE;` resolve in the connection's database;
>       probe-analyze flipped (reltuples=5,997,241 in db `tpch`, and the
>       SECOND session saw it too — -0029's gap-3 mechanism is now doubtful,
>       see design §-0028a). NEXT SELECTION IS `M0125-0030`** per the (b)
>       directive, now that -0029 landed (2026-07-30, loop #9). NEXT SELECTION IS
>       `M0125-0031`** — the warm-stats planning line, now that -0030 landed
>       (2026-07-30, loop #10).
>       **↳ `M0125-0031`'s FIRST MOTION IS DONE (2026-07-30, loop #11,
>       `analysis/m0125-0031-warm-tpch-20260730.md`): the warm TPC-H sweep at HEAD
>       INVERTS round 4's sign — stream 494.0 → 420.1 s (−15.0 %), zero of round
>       4's five regressions reproduce, and its 53× loss Q8 is the largest win
>       (8.5×). Two by-products bind later loops: §D3's invariant is now MEASURED
>       (warm + flag-off vs `warm-stats-base` = 22/22 MATCH `structural` AND
>       `strict-text`, so the relsize fallback is an S-cold-only safety net), and
>       **this harness's single-run per-query noise band is ~±17 %, not ±4 %** —
>       identical plans moved 1.17×, so no sub-1.2× per-query ratio on a sub-20 s
>       query is evidence. Q21 times out in ALL FOUR arms → shape class, filed as
>       `M0125-0032`.
>       **↳ `M0125-0031`'s SECOND MOTION IS DONE TOO (2026-07-30, loop #12,
>       `analysis/m0125-0031-warm-sf05-20260730/`): the warm SF0.5 gate ran 99/99
>       on one binary and goal (a) is MEASURED, NOT MET — the timeout class goes
>       **13 → 12**, target 0, and the one mover (Q72 307 s → 308 s) merely
>       straddles the cap. ZERO members were size-starved: all 12 have now failed
>       under none / sizes-only / full-statistics cardinality, so **cardinality
>       work is exhausted as a route to goal (a)** and the remainder is plan-shape
>       work. Correctness untouched (82/82 common-PASS agree on rows AND
>       checksums); one warm regression found, Q18 117 → 251 s, filed
>       `M0125-0033`. ~~**NEXT SELECTION: `M0125-0026`**~~ — **`M0125-0026` IS
>       DONE (2026-07-31, `analysis/m0125-0026-timeout-plans/`).** It filed six
>       per-class tasks, `M0125-0034` … `-0039`, and proposed this order, which
>       this banner adopts:
>       **`M0125-0037`(i) → `M0125-0039` → `M0125-0034` → `M0125-0035` →
>       `M0125-0036` → `M0125-0037`(ii) → `M0125-0038`.**
>       The two leading items are EXPLAIN-only, small and host-independent, and
>       they come first for a measured reason rather than a stylistic one:
>       **three of the eighteen queries in -0026's own capture (Q5, Q18, Q67)
>       could not be classified** because goopg prints `*planner.SetOp` with no
>       children, and several filters print as self-comparisons
>       (`ctr_state = ctr_state`) that cannot be told apart from a real defect.
>       Every later item's evidence is read through that instrument. `-0038`
>       (no cost/cardinality propagation above base scans) is last because it is
>       the largest and overlaps the `docs/design/cost-model/` "0077 line" —
>       read that bundle before scoping it. ~~**NEXT SELECTION: `M0125-0037`
>       stage (i)**~~ — DONE 2026-07-31, and so are `-0039` (loop #6) and
>       **`-0034`'s set-operation arm (loop #7: 30 Cartesian products gone,
>       Q5/Q8/Q14/Q54/Q71 TIMEOUT → PASS, timeout class 12 → 7)**. `-0034`
>       stays UNCHECKED for its CTE-reference / derived-aggregate arm (Q30
>       Q64 Q65 Q81, 8 crosses), but that arm shares a boundary with `-0035`
>       (C2 qual placement), so **NEXT SELECTION IS `M0125-0035`** per the
>       adopted order; fold -0034's remainder into it if the diagnosis
>       converges. Original wording of -0026's line follows.
>       **↳ `M0125-0035`'s BINARY-JOIN ARM LANDED 2026-07-31 (loop #8), and its
>       mandated first step is DISCHARGED: C2 is an EXECUTION defect, not
>       costing-only** — `EXPLAIN ANALYZE` (serial, a counting instrument, so
>       valid on the loaded host) shows `date_dim` hashed at **actual rows =
>       73,049** and the join emitting **1,374,770** rows for a 275,107-row
>       answer; the MHJ arm is identical. Scan-level `Filter:` lines **5 → 71**,
>       TPC-H plan-diff **4/22 with ZERO structural change** (4 net-new scan
>       filters, 0 removed), SF0.5 **99/99: PASS=87 MISMATCH=0 CKMISMATCH=0
>       ERROR=0 TIMEOUT=8 SKIP=4, no new timeout**. **-0035 STAYS OPEN: its
>       acceptance Q78 is untouched** (its qual sits above two LEFT joins and
>       names a CTE output column). The diagnosis DID converge with -0034's open
>       arm exactly as this banner predicted — both now need the same two
>       things: the preserved-side extension for outer joins, and PG's
>       single-reference CTE inlining. ~~**NEXT SELECTION: take the shared
>       CTE/outer-join arm of `-0034`+`-0035` together**~~ — **TAKEN 2026-07-31
>       (loop #9): the OUTER-JOIN half landed and the CTE half did not.**
>       Design `docs/design/0125-0035a-preserved-side-restriction-descent.md`.
>       The pass now pushes onto the PRESERVED side of LEFT/RIGHT (safe with no
>       `nullingrels` model — see the item body) and DESCENDS the join spine to
>       the deepest node that can hold the restriction. **Q31 — one of -0035's
>       two acute members — TIMEOUT → PASS at 11 s with rows AND checksum equal
>       to the oracle**, its six `CTE Scan` nodes each carrying their own filter
>       exactly as PG places them; SF0.5 **PASS=89 TIMEOUT=6**, all 87 common
>       PASSes byte-identical in status and checksum; TPC-H plan-diff 1/22 with
>       zero structural change and a NEUTRAL timed w2 arm on a quiet host.
>       **The two open items separated rather than converged**, which is the
>       finding that sets the next order: `-0035`'s remainder is a
>       **CTE-body** problem (single-reference `cte_inline` + EC constant
>       propagation, the only route to its Q78 acceptance), while `-0034`'s 8
>       crosses were MEASURED not to move and are a **join-ORDER** problem
>       (Q64 places `date_dim d2`/`d3` before the `customer` their equi-predicate
>       needs) — so `pushOneConjunct`, the starting point -0034's body named, is
>       refuted. **NEXT SELECTION: `M0125-0036`** per the adopted order
>       (`-0037`(i), `-0039`, `-0034`'s set-op arm and `-0035`'s two placement
>       arms are all discharged); take -0034's join-order arm or -0035's CTE
>       arm after it, and read each item's own resume point — they are now
>       different subsystems. Four ledger rows 2026-07-31 (three appended, one
>       flipped to `resolved`).
>       **↳ `M0125-0036` IS DONE (2026-07-31, loop #10) for C3's EXISTS half.**
>       Design `docs/design/0125-0036-exists-to-any-hashed-subplan.md`; pass
>       `internal/planner/exists_to_any.go` (`GOOPG_EXISTS_TO_ANY=off`). goopg
>       now has PG's `convert_EXISTS_to_ANY`: an OR-ed single-equality
>       correlated EXISTS becomes an UNCORRELATED `= ANY (SubPlan n)` that the
>       Stage-11 hash probe builds once. Full 99-query SF0.5 gate on one binary:
>       **`PASS=90 (54 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=5
>       SKIP=4`** vs loop #9's `PASS=89 … TIMEOUT=6`, and diffed cell by cell
>       **exactly one of the 99 changed — `Q35 TIMEOUT (327 s) → PASS (18 s,
>       100 rows)`** — every other query identical in status AND checksum.
>       **READ THE ACCEPTANCE ROW CAREFULLY: `Q10` was ALREADY PASSING at 35 s**
>       before this change (the `GOOPG_RELSIZE_FALLBACK` flip rescued it; the
>       "TIMEOUT" label on Q10 comes from -0026's older-regime capture), so the
>       item's stated acceptance was green on arrival and Q10's contribution here
>       is 35 s → 16 s at an unchanged checksum. Q69, §C3's own control, is
>       unchanged at 15 s. TPC-H plan-diff 1/22 **and the same 1/22 with the
>       switch OFF — plan-neutral on all 22**; units PASS; `tpch-spotcheck.sh`
>       `RESULT=PASS` (Q12=2 Q13=35). The item's premise
>       ("the fix is caching, not decorrelation") was followed and then refined
>       by arithmetic rather than overruled: the outer key is unique per outer
>       row, so caching is all misses; removing the correlation is what makes
>       the set shareable, and the *result* is still hashing. **Three things
>       bind later loops: (1) Q30/Q81 are NOT closed** — they are the
>       correlated-scalar-aggregate variant, still TIMEOUT, refiled as
>       **`M0125-0041`** with an explicit warning not to assume this pass
>       generalises (the shareable object there is a grouped aggregate, not a
>       value set); **(2) a SILENT WRONG ANSWER was found while probing and is
>       filed as `M0125-0042`** — two hand-written OR-ed uncorrelated `IN
>       (subquery)` sublinks under a SEMI-over-MHJ answer 1329 where PG says
>       1294, pre-existing at HEAD and unrelated to this pass; **(3) the
>       acceptance row a task names can be blind to its own defect** — Q10's
>       oracle is 0 rows, so the first version of this pass (which read a stale
>       post-MHJ column index) PASSED Q10 while returning 0 rows for Q35's 100.
>       Seven ledger rows 2026-07-31. ~~**NEXT SELECTION: `M0125-0037` stage (ii)**
>       per the adopted order (`-0038` stays last); `-0041`/`-0042` and
>       `-0034`'s join-order arm follow, and `-0042` outranks a timeout item on
>       severity whenever the banner is next revised.~~
>       **↳ REVISED 2026-07-31 (loop #11). `M0125-0037` stage (ii) was selected
>       per this banner and then MEASURED OUT: its acceptance `Q5 → 5|OK|100` is
>       ALREADY GREEN** (`Q5 PASS 40s 100 rows` in
>       `analysis/m0125-0036-exists-to-any/sf05/`), as are Q8/Q14/Q54/Q71 —
>       `-0034`'s set-op arm had already retired the crosses stage (ii) targeted.
>       It stays unchecked because its *mechanism* claim is unverified, but it is
>       NOT where a loop should be spent; see the -0037 item body. **This is the
>       second consecutive item whose acceptance row predated its own fix** (the
>       first was -0036's Q10) — re-read the latest `sweep-*.txt` before treating
>       any -0026-era acceptance row as a target.
>       The loop therefore took **`M0125-0042`**, per this banner's own "outranks
>       a timeout on severity" note, and that is now the standing order:
>       ~~**`M0125-0042` (fix) → `M0125-0041` → `-0034`'s join-order arm →
>       `M0125-0038` (last).**~~
>       **↳ AMENDED 2026-07-31 by the USER — `M0125-0043` PREEMPTS ALL OF THEM.**
>       The new standing order inside M0125 is
>       **`M0125-0043` (benchmark-name hardcoding: `operators_ddl.go` /
>       `open.go` `SmallDimension` name tag) → `M0125-0042` (fix) →
>       `M0125-0041` → `-0034`'s join-order arm → `M0125-0038` (last).**
>       `-0043` is a *correctness/architecture* item — production planner
>       behaviour currently keys off the literal strings `"region"` / `"nation"`
>       — and the USER set it as this milestone's first pick. Its acceptance is
>       a full 22-query TPC-H run with correct results and no query over 600 s;
>       a slowdown within that budget is explicitly ACCEPTED. **The agent that
>       takes it writes `docs/design/0125-0043-smalldimension-name-tag-extinction.md`
>       in that same loop** (it does not exist yet) and the doc must list the
>       affected TPC-H query numbers. Full item body at the head of the M0125
>       section below.
>       **`M0125-0042` IS FIXED (root-caused loop #11, landed loop #12).** The
>       operand of an OR-ed `IN (subquery)` carried the right `Name` and a STALE
>       `Index`: it read `ca_zip` (a string) where `c_customer_sk` was meant, and
>       `compareEq`'s string↔int coercion answered instead of raising.
>       `internal/planner/exists_to_any.go`'s `fixInExprOperandIndex` now
>       re-resolves it by Name against the host schema; `probe35g.sql` → 1294,
>       full TPC-DS SF0.5 sweep MISMATCH=0. Diagnosis, the three independent
>       reasons nothing caught it (including **EXPLAIN itself, which prints the
>       right Name over the wrong index** — still unfixed, a separate ledger
>       row), and the fix are in the item body and
>       `docs/design/0125-0042-in-sublink-operand-stale-index.md`. **`M0125-0043`
>       is the mandatory next selection inside M0125** — this loop reached
>       `-0042` off a stale working-set baton and only landed it because it was
>       already fully verified by the time the banner amendment (`da882af6`)
>       was noticed; do not repeat that shortcut. Current timeout class:
>       **Q30 Q64 Q65 Q72 Q78 Q81** (6, `Q72` confirmed in the loop #12 sweep).
>       Four ledger rows 2026-07-31.
>       **↳ `M0125-0043` IS DONE (2026-07-31, loop #13) — the USER's preempting
>       item is discharged.** The two `region`/`nation` name lookups are gone;
>       the small-dimension property is derived from relation SIZE at plan-build
>       time and stamped on the scan leaf (`internal/planner/small_dimension.go`,
>       design `docs/design/0125-0043-smalldimension-name-tag-extinction.md`).
>       **0/22 TPC-H plans changed — byte-identical snapshots in a same-cluster
>       `git stash` A/B**, and that is the PROOF rather than a null result: the
>       before-arm has the name tag ON, so identical plans mean the size
>       derivation reproduces it exactly (Q5's `region` MHJ anchor and the
>       Q8/Q21 `shouldAttachBeforeMHJ` CANCEL guard included). Timed 22-query
>       acceptance: **21/22 ok with correct rows (Q12=2 Q13=35); Q21 timeout,
>       PRE-EXISTING** (`timeout` in both cold arms at HEAD on 2026-07-30) and
>       already filed as **`M0125-0032`** — newly established, it survives a
>       DOUBLED 600 s budget, so -0032 is a shape defect, not a crossing near
>       300 s. The catalog field survives as a fixture-only hint with **no
>       production writer**; retiring it entirely is a ledger row. **NEXT
>       SELECTION: `M0125-0041`** (Q30/Q81, -0036's correlated-scalar-aggregate
>       variant — its body warns explicitly NOT to assume -0036's pass
>       generalises), then `-0034`'s join-order arm, then `M0125-0038` last.
>       Three ledger rows 2026-07-31.
>       **↳ `M0125-0041` WAS WORKED 2026-07-31 (loop #14): its root cause is
>       found, fixed and equivalence-tested, but the ITEM STAYS OPEN because its
>       acceptance is a completing Q30 and Q30 still TIMEOUTs — at 300 s AND at
>       1200 s.** The scalar pull-up never declined; it died on
>       `clonePlanReplacingOuter`'s missing `*CTEScan` arm, so a capability gap
>       wore the costume of a policy decision (design
>       `docs/design/0125-0041-cte-scalar-sublink-decorrelation.md`, two ledger
>       rows). The residual is C1 and it is now ISOLATED: the plan lost
>       `SubPlan 1` but keeps a `Nested Loop (CROSS)` of ~2×10⁴ CTE rows × 5×10⁴
>       `customer_address` rows = 10⁹ pairs. **NEXT SELECTION: `-0034`'s
>       join-order arm** (which now also owns Q30/Q81's acceptance), then
>       `M0125-0038` last. Its first probe on this query is written down in the
>       -0041 item body: `ca_state = 'AR'` still does not reach the
>       `customer_address` scan.
>       **↳ `M0125-0034`'s JOIN-ORDER ARM LANDED 2026-07-31 (loop #15), and it
>       closed `-0041`'s acceptance with it: Q30 TIMEOUT → PASS 1 s / 31 rows /
>       ck = oracle, Q81 TIMEOUT → PASS 1 s.** Design
>       `docs/design/0125-0034a-comma-from-connectivity-order.md`. Both
>       join-order passes were declining on any comma-FROM list holding a WITH
>       reference — `tryBushyDP` on its leaf whitelist, the comma-FROM greedy on
>       "not a base table with stats" — so nothing reordered Q30/Q64/Q81 and the
>       source-order CROSS chain survived. Connectivity mode reorders for
>       cross-freedom instead of cardinality, and is a **fixed point on any
>       cross-free source order**, which is why only **4 of 99 SF0.5 cells
>       moved** (`PASS=92 TIMEOUT=2`, timeout class 6 → 2; the other 95
>       identical in status, rows AND checksum). Q72's TIMEOUT → PASS is NOT
>       claimed (no `WITH`; the documented cap-straddler).
>       **Two things bind the next loops. (1) `M0125-0044` is NEW and is a
>       SILENT WRONG ANSWER** — Q64 now completes and answers 0 where the
>       oracle says 2, because three `date_dim` aliases collapse to one in
>       projection resolution; proven pre-existing by a byte-identical A/B
>       across an arm that fires the pass and an arm that declines it, and
>       previously unreachable (Q64 does not complete at HEAD in **1848 s**).
>       **By this banner's own "a silent wrong answer outranks a timeout on
>       severity" rule, `M0125-0044` IS THE NEXT SELECTION.** (2) `-0034` stays
>       unchecked for **Q65 only**, which needs laterality recorded on
>       `parser.RangeVar` before a derived table can be admitted — a parser
>       change, not a planner one. Standing order inside M0125 is therefore
>       **`M0125-0044` → `-0034`'s Q65 remainder / `-0035`'s CTE-body arm →
>       `M0125-0038` (last)**. Two ledger rows 2026-07-31.
>       **↳ `M0125-0044` LANDED 2026-07-31 (loop #16) and is CHECKED.** Q64
>       MISMATCH → **PASS, 2 rows, ck=31f0342ff9d55c4a**, and the full 99-query
>       SF0.5 gate moved **exactly 1 of 99 cells** vs HEAD `d50c0b4a`
>       (PASS=93 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4). The
>       collapse was in the **aggregate surface**, not projection resolution
>       generally — without GROUP BY the same query projects correctly — and
>       the cause is `parserExprKey`'s deliberate qualifier-blindness
>       (M0097-0003) colliding every alias of a self-joined table onto one
>       GROUP BY slot. Design
>       `docs/design/0125-0044-groupby-alias-slot-collapse.md`, two ledger
>       rows. It also FILED **`M0125-0045`**: `aggregateCallKey` shares the
>       blind key, so `count(d1.y)` and `count(d2.y)` dedup onto one aggregate
>       slot (measured — both targets resolve to agg slot 0). That one is NOT
>       gate-reachable (no SF0.5 query aggregates two aliases of one table), so
>       it ranks below the gate-visible items. ~~**NEXT SELECTION: `-0034`'s Q65
>       remainder / `-0035`'s CTE-body arm, then `M0125-0045`, then
>       `M0125-0038` last.**~~
>       **↳ `-0034`'s Q65 REMAINDER LANDED 2026-07-31 (loop #17) and M0125-0034
>       IS CLOSED.** Laterality is now recorded on `parser.RangeVar` and
>       non-lateral derived tables enter connectivity-mode reordering; **Q65
>       TIMEOUT → PASS 17 s, 100 rows = oracle**; full SF0.5 gate PASS=93
>       MISMATCH=0, exactly 2/99 cells moved (the other is Q72's 309→314 s
>       cap-straddle, unreachable by the pass — all-`JOIN…ON`); plan-diff
>       22/22 MATCH. The gate-visible timeout class is down to **Q78 alone**,
>       which is `-0035`'s CTE-body arm (single-reference CTE inlining +
>       EC constant propagation). ~~**NEXT SELECTION: `-0035`'s CTE-body arm,
>       then `M0125-0045`, then `M0125-0038` last.**~~
>       **↳ `-0035`'s CTE-BODY ARM LANDED 2026-08-01 (loop #18) and its
>       ACCEPTANCE IS MET — M0125-0035 IS CLOSED** (arm (b) refiled as
>       `M0125-0046`, arm (c) already = `M0125-0038`). Q78 TIMEOUT → **PASS
>       24 s, 45 rows, ck = oracle**; full SF0.5 gate **PASS=95 MISMATCH=0
>       CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4** — the gate-visible timeout
>       class is EMPTY (Q72's 306 s flip is its documented straddle, not
>       claimed). Three mechanisms were needed, the third invisible until
>       the first two landed: CTE-body qual descent (refs==1, read from
>       Plan()'s tail), one-hop join-equality constant derivation onto the
>       nullable side, and degenerate hash-key re-selection (goopg hashes
>       only the FIRST equi-pair; year=1998 on both sides collapsed the
>       build side into one bucket — 245k × 30k probe walks). Design
>       `docs/design/0125-0035b-cte-body-inline-ec-const-hash-key.md`;
>       three ledger rows 2026-08-01 (multi-column hash keys, equivclass
>       transitive closure, inline_cte volatility). ~~**NEXT SELECTION:
>       `M0125-0045`, then `M0125-0046`, then `M0125-0038` last.**~~
>       **↳ `M0125-0045` LANDED 2026-08-01 (loop #19)** — contested-key
>       treatment for aggregate slots, acceptance met by unit tests + a
>       byte-identical PG-oracle diff; SF0.5 gate unchanged (PASS=95, zero
>       cell movement). ~~**NEXT SELECTION: `M0125-0046`, then `M0125-0038`
>       last.**~~
>       **↳ `M0125-0046` LANDED 2026-08-01 (loop #20)** — the filed
>       "walker disqualifies InExpr" was a misdiagnosis; the residual
>       WHERE conjunct was never in mh.Filters at all. New MHJ arm of
>       pushInnerJoinInputQuals + executor walkColumnRefs sibling relaxed
>       in lockstep; SF0.5 subset probe of 15 MHJ-heavy queries PASS=15
>       MISMATCH=0; plan-diff 5/22 all benign (+Filter on member scans),
>       row counts anchored. **NEXT SELECTION: `M0125-0038` (the last
>       open M0125 task), then M0125 closes and M0126 (cost-driven
>       planning) is next per the 2026-07-31 USER amendment.**
>       ~~its classification is now
>       the ONLY path to goal (a), it is host-independent, and it should absorb
>       Q18 (-0033) and TPC-H Q21 (-0032) into its capture set so one taxonomy
>       covers both benchmarks; the per-class fixes then file from -0034+.~~
>       *(Q18 WAS absorbed; TPC-H Q21 was not — ledger row 2026-07-31.)*
>       *(Stale
>       correction 2026-07-30: an earlier revision of this line said "take
>       -0003 stage 1 first … still unlanded" — stages 1 AND 2 are landed per
>       item 1 above; what -0003 still owes is the timed four-arm study and
>       stage 3, and stage 3 must NOT land before stage 2's timed arm is read.)*
>    **`M0125-0026` (added 2026-07-30 by the USER): capture + classify the
>    15-query timeout class** — plain-EXPLAIN-only comparison of goopg (flag-off
>    and `GOOPG_RELSIZE_FALLBACK=2`) against PG 18.3 for Q5 Q8 Q10 Q14 Q30 Q31
>    Q35 Q54 Q64 Q65 Q67 Q69 Q71 Q78 Q81, then file one planner-fix task per
>    root-cause class (M0125-0032+ — -0027 … -0031 are taken; see the numbering
>    note at M0125-0027). **It is HOST-INDEPENDENT (nothing is
>    timed), so it is the task to take when the timed items above are blocked
>    on a busy host** — do not burn a quiet-host window on it. Task body at the
>    end of the M0125 section; design
>    `docs/design/0125-0026-timeout-class-plan-comparison.md`.
>    **↳ THE OWED FULL GATE IS DISCHARGED (2026-07-30, `analysis/m0125-sf05-fullgate-20260730/`).**
>    99/99 at HEAD `e29faca9` on a verified quiet host, one binary, 10:20→12:26:
>    **`PASS=79 (49 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=16
>    SKIP=4` — ZERO correctness failures.** Five per-query changes vs the
>    2026-07-29 baseline and no change in the other direction: Q16/Q94/Q95
>    `CKMISMATCH → PASS` (M0125-0007 + -0008/-0023 — the three cells M0124-0005's
>    checksum column was added to find, now closed by *independent* measurement
>    rather than a 3-query probe), Q75 `ERROR → PASS` (M0125-0004, which also
>    clears the live `Q75,100,pinned` nightly anchor), and Q47
>    `MISMATCH → TIMEOUT` — its row defect is fixed (0 → 100 = oracle) and what
>    the gate now sees is the still-open **runtime** half of M0125-0013, so
>    `TIMEOUT` 15 → 16 is not a regression. **`M0125-0026`'s capture list is
>    therefore 16 queries, not 15** (add Q47; but it is not "budget-marginal" in
>    D6's sense — 142 s at SF=1 against a 300 s SF0.5 cap on half the data needs
>    its own reading first). First run under the closed gate-integrity trap: the
>    report carries D4a's `engine-id`/`engine-binary`, `diff=` was the empty-diff
>    digest (engine == `e29faca9` exactly), and none of the 16 restart bounces
>    tripped `*** SWEEP VOID ***`.
>    Owed independently of all three: **one full 99-query SF0.5 gate run** on a
>    quiet host. M0125-0007 is a codec change and could only get the 3-query
>    value probe, because the nightly CI batch held the host for its whole loop
>    (ledger row 2026-07-30). Fold it into whichever of the above lands first.
>    **`M0125-0020` landed at `beb7af82` before this list was written**, so its
>    ordering is moot — but the reason it was selected is not: it was chosen on
>    the false completeness claim corrected in item 3. Do not select the next
>    set-op follow-up ahead of items 1–5 on the same reasoning.
>    **USER DIRECTIVE 2026-07-30(b) — the WARM-STATISTICS programme
>    (`M0125-0028` … `-0031`, task bodies at the end of the M0125 section;
>    design `docs/design/0125-0028-warm-stats-programme.md`; "(b)" because it
>    is the day's SECOND user directive — the first filed `M0125-0026` above).** The user has
>    flipped the measurement premise: statistics must survive restarts (a
>    goopg-private mechanism is AUTHORIZED — the PG-faithfulness bar is
>    explicitly waived for this persistence), `ANALYZE <table>` must stop
>    erroring in per-DB databases, and the bench clusters get per-table
>    ANALYZE + CHECKPOINT at build time and once retroactively — after which
>    every benchmark measurement assumes WARM stats. **Selection guidance:
>    take `-0028` → `-0029` → `-0030` ahead of `M0125-0002` commit 2 and the
>    -0003 four-arm study**, because every timed S-cold measurement taken
>    before the warm flip risks being re-measured after it, and -0028/-0029
>    are precisely what makes -0003's owed W-control arms constructible
>    (ledger row 2026-07-30). `-0028` is small and host-independent; `-0030`
>    is the commit where the premise flips (new plan-diff label, timed
>    baselines reset). `-0031` (timeout elimination + warm-stats
>    optimization/stabilization) is GATED on the other three and files its
>    concrete fixes from evidence, sharing the M0125-0032+ numbering runway
>    with -0026's per-class tasks. `M0125-0026` itself is UNAFFECTED — its
>    capture stays valid and gains a free warm arm if -0029/-0030 land first.
> 4. **M-NIGHTLY backlog** — the standing nightly-triage items below. Keep FILING
>    them every loop (see the filing obligation above); work them only after M0124
>    and M0125 close, or under one of the two carve-outs.
>    **↳ AMENDED 2026-07-31: M0126 sits ABOVE this item.** M-NIGHTLY work is now
>    parked beneath **M0124 / M0125 / M0126**; the two carve-outs (build break,
>    or a break in a gate those milestones depend on) are unchanged, and
>    reversion stays automatic as soon as the banner stops naming them. The TPC-DS **Q75** nightly
>    item is the exception that is already routed: it is in the qualifying set with
>    `Q75,100,pinned` at `ci/batch/tpcds-row-anchors.csv:46` and no
>    `expected-failures.csv` entry, and RC-1b turned it into a deterministic
>    `division by zero` — that item IS **M0125-0004**, so it is worked as part of
>    M0125 and never as a second workstream.
> ~~**Every other roadmap milestone — M0123 included — is parked below M0125 until
> M0124 and M0125 are complete.**~~ — **AMENDED 2026-07-31 (USER):** every other
> roadmap milestone — M0123 included — is parked below **M0126**, and M0126 is
> itself parked below M0125. Order: M0124 → M0125 → **M0126** → M-NIGHTLY →
> M0123. M0123 keeps its own branch (`wal-pg-nodetree`)
> and resumes there once this line is closed.
>
> Dependencies, stated narrowly: **M0124-0002 gates M0125-0002/-0004 and the
> measurement half of M0125-0003** (it produces the plan snapshot they diff
> against); **M0124-0005 gates M0125-0002 and M0125-0004's acceptance** (both are
> accepted by value, and the SF0.5 oracle is row-count only today). M0125-0001
> (dead code) and M0125-0003 (flag-off throughout) are unblocked. **M0125-0003 is
> independent of M0125-0002** — an earlier draft claimed its later stages were
> blocked on the `localizeExprToLeaf` conversion, but that walker is reached only
> under the `shouldAttachBeforeMHJ` gate (`bushy.go:158`), and when the gate opens
> `attachRelationLocalFilters` already calls it, so the relation-size fallback
> wakes nothing.
>
> Two M0125 tasks move plan shape, and goopg's planner sits on a *measured*
> trade-off: enabling statistics fixed TPC-H Q5 22.8× and regressed Q22 128× /
> Q4 79× / Q8 53× / Q2 26× / Q12 4.4×, taking the serial stream 1162 → 1307 s
> (`analysis/tpch-evolution-round4-parallel-query-20260722.md` §2/§5); and the
> cost-driven planner is 4 wins / 6 regressions / 12 neutral
> (`analysis/tpch-evolution-round5-int64-hashjoin-20260724.md` §6). **Every
> regression in that §6 table came with identical row counts**, so
> `scripts/tpch-spotcheck.sh` (a Q12/Q13 row-count gate) cannot see this class.
> Planner commits in M0125 need a **timed** 22-query TPC-H power run plus
> `make plan-diff LABEL=tpcds-round2-head` — note `make plan-gate` picks the
> newest snapshot by mtime, so it cannot be pointed at a named baseline.

**⚡ 2026-07-18 directive — SUPERSEDED 2026-07-28, kept for history:**
> This checkout is on `wal-pg-nodetree` to develop **M0123 (canonical `pg_node_tree`)**
> — see `docs/milestones/0123-canonical-pg-node-tree-serialization.md` and
> `docs/design/wal-pg-identical-stream/02e §3`. Work in THIS priority order:
> 1. **WIP recovery** — first restore & resolve the stashed pre-switch WIP (the
>    "WIP recovery" item directly under this banner), never silently drop it;
> 2. **M-NIGHTLY** — the standing nightly-triage items below (they preempt as usual);
> 3. **M0123** — the S1→S4 slices at the BOTTOM of this file (S0 already landed).
> The other roadmap milestones (M0110/M0119/M0122) stay parked below M0123 until
> M0123 is complete.

Work order: **M0124 → M0125 → M0126** (this directive as amended 2026-07-31),
then **M0123**, then the
pre-existing line — **M0117 → M0118** (both complete + archived), then resume
**M0110** (its **M0119-0004/0005/0006/0007** spinoffs are the active,
in-progress form of that work), with **M0095** parked (blocked on logical
decoding). **M0120 / M0121 are CLOSED** (2026-07-04) and archived. Policy: fix
blockers in place; do NOT defer unless genuinely compelling (then record a ledger
row); commit + push at every clean, green (build + pre-commit) checkpoint.

## WIP recovery (priority #1 — before M-NIGHTLY, one-time)

<!-- Added 2026-07-18 for the wal-pg-nodetree switch; UPDATED 2026-07-19: the
     pre-switch Ralph WIP has now been un-stashed and MERGED back into the
     working tree (uncommitted) — it applied cleanly onto wal-pg-nodetree. It is
     no longer in a stash; recover it as ordinary uncommitted WIP. (The pristine
     stash commit 6d5d9115 was dropped but remains GC-recoverable via reflog.) -->

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

## M-NIGHTLY — Nightly regression triage (STANDING — FILING CONTINUES, WORK PARKED BELOW M0124/M0125 since 2026-07-28)

<!-- Standing milestone: never complete it, never archive it, keep it directly
     under the Current Priority banner. Source of work: ci/logs/action-items.md
     (regenerated by every nightly batch run; design ci/design/07-ralph-feedback.md).
     Loop rule:
       1. Read ci/logs/action-items.md (absent file = nothing to do). For each
          `## AI-` item whose `subject:` has no OPEN (unchecked) task below,
          add one task:
            - [ ] <subject> — <one-line what> (AI-<id>; repro: <cmd>)
          If an unchecked task for the same subject already exists, do NOT add
          another — append the new AI-id to that task's line instead. If only a
          CHECKED task exists for the subject, the failure REOPENED: add a new
          task and note the earlier fix didn't hold.
       2. AMENDED 2026-07-28: tasks in this milestone are FILED every loop but
          are NOT selected while the Current Priority banner parks them (today:
          below M0124/M0125). Filing is unconditional; selection follows the
          banner. Two carve-outs may be worked at once — an item that breaks the
          build, and an item that breaks a gate the banner's milestones depend on
          (tpch-spotcheck, the TPC-DS SF0.5 gate, plan-diff, a bench cluster).
          The pre-amendment rule ("these PREEMPT all other milestones") returns
          automatically once the banner stops naming M0124/M0125.
       3. Before investigating, re-run the item's repro at HEAD — the log
          reflects the last nightly run and may be stale; if it passes, check
          the task off with a "stale — already fixed" note.
       4. Fix with the normal gates (practice cards apply), cite the AI-id in
          the commit message, check the task off. The next nightly run confirms
          and drops the item from the log.
     (Tasks are added here by the in-loop agent, one per subject. This
     placeholder is a comment, not a checkbox, so the plan-complete exit
     heuristic stays live.) -->

- [ ] **testport/TestE2E_FailoverGoopgToPG** — goopg→PG heterogeneous physical
      failover FAILed (subtest `sync_remote_apply`) in nightly run
      `20260731-001201` at sha `927742f8` (AI-20260731-001201-001; repro:
      `go test -v -run '^TestE2E_FailoverGoopgToPG$' ./internal/testport/`;
      evidence `ci/logs/20260731-001201/testport/go-test.log`). FILED, NOT
      SELECTED per the 2026-07-28(b) amendment — it neither breaks the build nor
      breaks a gate M0124/M0125 depend on. First-seen 20260731 (new). Note for
      whoever takes it: the OPPOSITE direction (`TestE2E_FailoverPGtoGoopg/sync_on`,
      checked below) was a co-load-timing flake around the same sync-rep feedback
      invariant on 2026-07-20 — re-run the repro in isolation at HEAD before
      assuming a deterministic regression, and see that item's 2026-07-20 ledger
      row for the sync-feedback / last-record-replay durability edge.

- [x] **Nightly TPC-DS row anchors are DEAD — all 63 have never been checked**
      **DONE 2026-07-30 (interactive session, commit `63056c54`)** — the reader
      now uses `expected_rows`, AND the same commit fixed a second, worse fault
      found on top of it: `main()` referenced `tpcds_timings` that was local to
      `analyze()`, so **every nightly since the tpcds lane landed crashed with
      NameError before writing summary.md / action-items.md / history.jsonl**
      (action-items.md was frozen at run `20260725-011243` for five nights —
      do NOT re-file items about "stale action-items"; the mechanism is fixed).
      Verified on a copy of run `20260730-011706`: completes STATUS=fail rc=2,
      all 63 anchors load, the 15 ok queries MATCH, and a deliberately-wrong
      `Q1,999` anchor via a doctored repo-root surfaces as `tpcds/Q1-rows`
      regression — the item's acceptance probe. `test_summarize.py` 4/4 PASS.
      The next real nightly run is the live confirmation. Original filing:
      (discovered 2026-07-30 while closing `M0125-0014`/`-0015`; not from
      `action-items.md`, and **not** one of the banner's two carve-outs, so it is
      filed and left unchecked). `ci/batch/lib/summarize.py:485` builds
      `anchors_tpcds` from `r["rows"]`, but `ci/batch/tpcds-row-anchors.csv`'s
      column is **`expected_rows`** — `csv.DictReader` therefore yields no `rows`
      key, the comprehension's `if r.get("rows")` guard drops **every** row, and
      the dict is always empty. Consequence: the nightly batch has never reported
      a TPC-DS row-count regression, for any of the 63 pinned queries. **TPC-H is
      unaffected** — `ci/batch/tpch-row-anchors.csv` really does use `rows`
      (verified: 12/12 usable vs 0/61 for TPC-DS), which is why the same code
      shape works 80 lines earlier and why this went unnoticed.
      Repro: `python3 -c "import csv;r=list(csv.DictReader(open('ci/batch/tpcds-row-anchors.csv')));print(sum(1 for x in r if x.get('rows')))"` → `0`.
      Fix: read `expected_rows` in `summarize.py` (or rename the CSV column —
      prefer fixing the reader, the CSV name is the clearer one), then confirm on
      a nightly run that a deliberately-wrong anchor actually surfaces as a
      regression. Until this lands, the Q49/Q51 anchors added by M0125-0014/-0015
      protect nothing.

- [x] **Nightly TPC-H stage wedged the host for 6h45m after its sweep finished**
      — worked immediately under the banner's second carve-out (it broke the bench
      clusters AND, via the new host-quiet guards, blocked M0124-0002/-0004 from
      running at all). **DONE 2026-07-29**, design
      `docs/design/root-0037-nightly-server-shutdown-ladder.md`. Run
      `20260729-002344` finished its sweep at 02:07:15 and still held the host at
      08:50 with **no error in any log**: the stage log ends on `sweep done`, the
      server log on a *successful* `shutdown checkpoint complete`. Q13 hit the
      1200s cap; killing psql kills only the client, and exactly ONE backend
      (`pid=40`) has an `established` with no `closed`. Graceful shutdown cannot
      return until every backend has, so the process never exited and
      `stage-tpch.sh`'s **untimed** `wait ${server_pid}` inherited the hang. Fixed
      by `stop_goopg_server` (`ci/batch/lib/common.sh`): graceful 120s ->
      `-mode immediate` 60s -> SIGTERM 30s -> SIGKILL, 210s worst case, applied to
      `stage-tpch.sh` AND `stage-tpcds.sh` (identical two lines; `stage-pgbench.sh`
      already hard-killed). Liveness reads process **state**, not `kill -0` (a
      background child answers `kill -0` while a zombie). Escalations are reported
      via `progress`, so this can never present as silence again. Guarded by
      `ci/batch/lib/test_stop_ladder.sh` (5 rungs + dump capture, ~50s, no build).
- [ ] **goopg graceful shutdown hangs forever on a backend that outlives its client**
      — the engine defect underneath the wedge above; the ladder bounds the COST,
      not the CAUSE. `startClientEOFWatch` (`internal/server/eof_watch.go:113`)
      logged `client connection lost mid-query; cancelling statement` for backend
      `pid=40` and called `cancel()`; the backend never finished. `cl.OnStop`
      (`internal/server/server.go:602`) checkpointed, called `runCancel()` and
      drained the accept loop, but has **no deadline and no force-terminate step**,
      so one unresponsive backend hangs the process indefinitely. PG diverges:
      `pg_ctl stop -m fast` SIGTERMs every backend and `ProcessInterrupts` acts at
      the next `CHECK_FOR_INTERRUPTS()`. The blocking site is NOT established — the
      server was idle (0.4% CPU, 23 threads sleeping), so blocked, not spinning.
      **Get the stack before theorising**: the next occurrence now auto-saves
      `<stage>/server-goroutines.txt`; to force one, run TPC-H Q13 at SF=1 under a
      1200s cap and kill the psql client. Ledger row 2026-07-29 (root-0037); same
      family as root-0029's still-open orphaned-backend row, narrowed to one pid.

- [x] **TestPort_IsolationPreparedTransactions** — testport spec FAILed in
      nightly run 20260719-094219 (AI-20260719-094219-001; repro:
      `go test -v -run '^TestPort_IsolationPreparedTransactions$' ./internal/testport/`).
      **Stale — already fixed at HEAD.** The nightly ran at sha `c217c692`, which
      predates `f20cda39` (demote strict→defer for the runner-timing false-positive,
      the memory-noted fix). Re-run at HEAD `12969b77` PASSES (57.9s). No new work.
- [x] **regress/errors, regress/index_including, regress/portals_p2, regress/select**
      — four `TestPort_RegressSuite` cases reported "output mismatch; normalization
      rules need extension" in nightly run 20260719-094219 (AI-20260719-094219-002/
      -003/-004/-005; repro:
      `go test -v -run 'TestPort_RegressSuite/(errors|index_including|portals_p2|select)$' ./internal/testport/`).
      **Stale — all four PASS at HEAD.** Nightly sha `c217c692` predates the
      pgnodes S1/S2 + mdtablefix commits now at HEAD `12969b77`. Verified: the
      four cases (plus their suite dependencies copyselect/subselect) all PASS
      (18.8s); the normalization-rule divergence no longer reproduces. No new work.
      **RE-VERIFIED 2026-07-20** (nightly run 20260720-005224, AI-20260720-005224-002/
      -003/-004/-005 — re-reported at sha `be88fb66` ≈ HEAD `fb5de5c4`): re-ran the
      same repro in isolation at HEAD — `errors`/`portals_p2`/`select` PASS,
      `index_including` SKIPs (deferred), suite green (18.95s). NOT reopened: the
      normalization divergence only manifests in the nightly's full-suite ordering /
      co-load, never in the isolated repro the action-item prescribes. No new work;
      candidate for a `regress_suite` normalization-hardening follow-up if it persists.
- [x] **TestE2E_FailoverPGtoGoopg/sync_on** — heterogeneous PG→goopg physical
      failover zero-loss invariant FAILed in nightly run 20260720-005224
      (AI-20260720-005224-001: `sync_remote_apply zero-loss violated: count(*)=5 want 6`;
      repro: `go test -v -run '^TestE2E_FailoverPGtoGoopg$/sync_on' ./internal/testport/`).
      **Flake at HEAD — passes 4/4** (`fb5de5c4`, 8.2s each, isolated). Not a
      deterministic regression: the pgnodes S4 commits between the nightly sha and HEAD
      touch only parse-time DEFAULT folding, nothing in WAL/replication/promotion. The
      nightly host was under co-load (`mmap … MAP_HUGETLB failed, huge pages disabled`
      in pg.log) which shifts sync-rep feedback timing. The invariant itself is real
      (a `synchronous_commit=on` COMMIT must be durable on the standby before it
      returns) — see the 2026-07-20 deferral-ledger row for the sync-feedback /
      last-record-replay durability edge to chase if it recurs. No weakening of the test.
- [x] **pgbench/nightly** — nightly heavy-write stage (s=50 c=100 j=20 T=180) logged
      4 failed transactions / 19.5M (0.000%), all `current transaction is aborted,
      commands ignored` at TPC-B command 4 (AI-20260720-005224-006; repro:
      `REPO_ROOT=$PWD RUN_DIR=$(mktemp -d) bash ci/batch/stages/stage-pgbench.sh`).
      **Known limitation, not a new regression.** Command 4 (`UPDATE pgbench_branches`)
      aborts because an earlier command in the same txn hit goopg's non-FIFO tuple-lock
      path (100 clients contending 50 branch rows) and raised instead of queuing — the
      documented `goopg_dml_conflict_no_fifo_tuple_lock` / ledger 0021-0012 gap (route
      tuple locks through `tableLockMgr` for FIFO waits). 4/19.5M is the tail of that
      known edge; no separate fix here — tracked by the existing deferral row.

### Nightly run 20260725-011243 (26 items, sha `55809fbf` — a pre-`master`-merge
### tpcds-fix2 tip; HEAD at triage time `e7d9b88e`)

- [x] **units/internal/executor + race/internal/executor** — both suites failed in
      `internal/executor` (AI-20260725-011243-001/-002; repro:
      `go test -timeout 10m ./internal/executor/`). Single cause:
      `TestVerifyHeapam_LateralCommaJoinViaFastPath` ("gap #6 regressed").
      **Stale — fixed at HEAD.** Nightly sha `55809fbf` predates the `master`
      merge `27d2dae8`; the test PASSES at HEAD (0.00s), and the nightly running
      *while this triage ran* (`20260728-121843`, sha `e7d9b88e`) reports
      `units PASS` / `race PASS`. No new work.
- [x] **regress/<19 cases> — 19 phantom regressions from ONE wedged cluster**
      (AI-20260725-011243-008..-026: boolean, case, create_function_sql, errors,
      index_including, limit, numerology, partition_aggregate, portals_p2, select,
      select_distinct, select_distinct_on, select_into, tid, time, timetz,
      truncate, union, varchar). **Root-caused and FIXED — design
      `docs/design/root-0029-nightly-regress-wedge-cascade.md`.** 36 cases had
      merely burned their full 120 s budget; the harness then diffed psql's
      *truncated* transcript against the full expected `.out` and reported
      "output mismatch; normalization rules need extension". The wedge outlives
      the case (killing psql kills only the client), so 21 consecutive cases from
      `tid` fell over, and `isAlive()` never fired because a saturated server
      still answers `SELECT 1` (`server not responding`: 0 occurrences).
      Fix: `framework.ErrExecTimeout` short-circuits before the diff,
      `ExecuteSQL` honours the caller's ctx deadline, a `clusterPoisoned` flag
      restarts the cluster after any timeout, and `summarize.py` collapses the
      cascade into one `regress/suite-wedge` item. Replayed on the real log:
      26 items → 17. The wedge's own cause (orphaned backend vs. GOMEMLIMIT
      saturation) is NOT fixed — ledger row 2026-07-28, and the 9 genuine
      sub-timeout divergences are the task below.
- [ ] **regress/{boolean,case,create_function_sql,errors,index_including,limit,
      numerology,partition_aggregate,portals_p2} — genuine sub-timeout
      divergences.**
      **PARKED 2026-07-28(b) — do NOT select; below M0124/M0125 per the banner's
      filing rule. Resume point carried over from loop #8's baton so nothing is
      lost: two cases left, `portals_p2` and `select`. Try the ISOLATED run FIRST
      — `go test -v -run 'TestPort_RegressSuite/^portals_p2$' ./internal/testport/`
      (~2 s); a `SKIP` there means "output mismatch", not "not applicable", and
      that is how root-0036 was found — far cheaper than the 670 s prefix run. The
      old "these only diverge in full-suite ordering" reading is NOT true of every
      case. `/tmp/rdiff-loop6/portals_p2_{expected,actual}.txt` (if still present)
      shows PG returning 1 row per FETCH where goopg returns 2 — ~10 blocks plus
      one 3-row block, i.e. one cursor-positioning bug, not ten.**
      What survives the root-0029 reclassification of
      AI-20260725-011243-008..-026: 9 baseline-pass cases that diverged in under
      120 s. At HEAD (full-suite re-run, 2026-07-28) `errors`, `index_including`,
      `portals_p2`, `select` and `select_distinct` still diverge, while `boolean`,
      `case`, `create_function_sql`, `limit`, `numerology`, `partition_aggregate`
      and `tid` now pass; `time`/`timetz`/`truncate`/`union`/`varchar` were not
      reached (the re-run hit the 10 m go-test timeout inside `tidscan`, under
      co-load from the concurrent nightly). Earlier loops established that
      `errors`/`portals_p2`/`select` pass in ISOLATION and only diverge in
      full-suite ordering — so the remaining work is suite-ordering state leakage
      (a case mutating shared `test_setup` fixtures), not normalization rules.
      Repro: `go test -v -run 'TestPort_RegressSuite' ./internal/testport/`
      with `-timeout 60m` and `GOOPG_REGRESS_DIFF_DIR=/tmp/rdiff` to capture the
      actual diffs, then bisect the ordering dependency.
      **`errors` CLOSED 2026-07-28 — root-0031**
      (`docs/design/root-0031-pg-inherits-restart-persistence.md`), and the
      "mutating case" reading above is REFUTED for it. Bisecting the ordering
      never converges because the trigger is nondeterministic: root-0029's
      `clusterPoisoned` recovery RESTARTS the cluster after any 120 s timeout
      (frequent under nightly co-load, never in an isolated repro), and
      `pg_inherits` was a purely virtual catalog that no reload pass rebuilt —
      so every case after a restart ran with all inheritance edges gone
      (`ALTER TABLE emp RENAME COLUMN salary TO manager` *succeeded*, leaving two
      `manager` columns). Fixed by making pg_inherits heap-backed
      (`base/<dbOid>/2611`) + `loadInheritanceFromHeap`, plus the three PG-fidelity
      bugs the restart had masked (qualified `RenameTable` message, missing
      self-relation RENAME COLUMN collision check, `DROP AGGREGATE` resolving its
      argument type after the name lookup). `errors` now PASSES in full-suite
      ordering **in a run that took the restart path**; 5 ledger rows filed.
      **RE-VERIFIED 2026-07-28 (prefix through `select_distinct`, 176 cases,
      670 s) — three of the four were NEVER MEASURED.** After `misc` timed out,
      root-0029's recovery restart FAILED and all 53 remaining cases (#123
      `misc_functions` … #176 `select_distinct`) reported a phantom
      `deferred: cluster restart failed`. So `portals_p2`, `select` and
      `select_distinct` have no result at HEAD; only `index_including` produced a
      genuine `output mismatch` (cluster alive at #88). The restart failed because
      **goopg could not start after a crash** —
      `wal replay: decode at offset 771751920: invalid record header: unknown
      rmid=31` — fixed as **root-0032**
      (`docs/design/root-0032-crash-restart-wal-stream-anchoring.md`): live-run
      WAL stream anchoring, a leading-contrecord skip in both scanners (which was
      also destroying 54–97 durable records on every reopen), and PG's end-of-WAL
      semantics instead of a fatal decode error.
      **(a) MEASURED 2026-07-28** (same 176-case prefix, 622 s): with root-0032
      + root-0033 the cluster restart now SUCCEEDS — the log shows three
      `restarting the cluster` events and three `cluster recovered`, zero
      `restart failed`, so the 53 phantom `deferred: cluster restart failed`
      cases are gone. `portals_p2`, `select` and `select_distinct` therefore
      have real results at HEAD for the first time, and all three genuinely
      diverge (`output mismatch`). Diffs captured in `/tmp/rdiff-loop6`
      (regenerate with the prefix + `GOOPG_REGRESS_DIFF_DIR`).
      **(b) CLOSED 2026-07-28 — root-0034**
      (`docs/design/root-0034-float-type-alias-opt-float-reduction.md`). Not an
      index-only-scan bug despite §10's title ("names stored as cstrings in
      indexes"): the whole 378-line divergence is four lines, and the row is
      gone from a plain seq scan on a table with no index. §10's fixture is
      `CREATE TABLE nametbl (c1 int, c2 name, c3 float)` — and `float` has no
      `pg_type` entry, because PG resolves `FLOAT [ (p) ]` entirely inside the
      grammar (`gram.y` opt_float). goopg's parser stored the literal token, so
      it reached `catalog.TypeNameToOID`'s `default: return OIDText` and the
      column became **text**, while `internal/executor`'s own type tables
      (`codec.go:482`, `expr.go:3035`) list `"float"` next to float8 and encoded
      an 8-byte IEEE-754 datum. `INSERT 0 1`, then zero rows forever. Fixed by
      performing PG's reduction where PG performs it (`normalizeFloatTypeName`,
      wired into the four typmod-bearing type-name sites), with opt_float's two
      22023 errors byte-identical. `index_including` PASSES in full-suite
      ordering (88-case prefix, 244 s). Three ledger rows filed.
      **(e) `select_distinct` CLOSED 2026-07-28 — root-0036**
      (`docs/design/root-0036-select-distinct-order-by-direction.md`). The
      `USING >` and the `person*` inheritance scan in the failing query are both
      red herrings, and so is "normalization rules": the whole divergence is one
      20-row block returned in exactly reversed order, and reduced it is
      `SELECT DISTINCT p.age FROM person p ORDER BY age DESC` answering
      **ascending** while the unqualified `SELECT DISTINCT age FROM person ORDER
      BY age DESC` is correct. goopg dedups with a fixed ascending sort inside
      `distinctOp` and re-applies the user's ORDER BY with an outer Sort
      (M0097-0046); that Sort was the only carrier of direction and was dropped
      whenever its key failed to resolve — which is the common case, because
      `resolveOrderBySubstitution` rewrites a bare ORDER BY name into the
      target's own (qualified) expression while the outer context is schema-only
      and `SchemaColumn` has no table name. 7 of 8 measured shapes were wrong.
      Fixed by resolving the key against whichever surface built the target list
      and mapping it back to its select-list position
      (`distinctSortKeyOutputIndex`), plus a positional arm for star targets.
      Non-vacuous `TestDistinctHonoursOrderByDirection` (10 subtests, PG-18.3
      `want` values; 7 red with the hunk stashed); the regress case flips
      SKIP → PASS. 3 ledger rows filed. **Method note for the two below:**
      `select_distinct` DOES reproduce in isolation (1.6 s repro loop) — the
      "only diverges in full-suite ordering" reading recorded earlier applies to
      `errors`/`portals_p2`/`select`, not to every case, so always try the
      isolated `-run 'TestPort_RegressSuite/^<case>$'` first and read a SKIP as
      "output mismatch", not "not applicable".
      **Still open here:** the two remaining now-genuine divergences (a)
      exposed — `portals_p2` and `select`. They are real output mismatches, not
      restart phantoms; the loop-6 capture in `/tmp/rdiff-loop6` shows
      `portals_p2` returning 2 rows where PG returns 1 from a cursor FETCH
      (`portals_p2_expected.txt` vs `_actual.txt`, ~10 blocks), which looks like
      one cursor-positioning bug rather than ten. Work them with the isolated
      run first and the prefix method below as fallback, reading the per-case
      `*_expected.txt`/`*_actual.txt` pair rather than the suite log. Note
      root-0034 and root-0036 were each found this way and touched neither, so
      each needs its own diff.
      ~~(c) the root-0032 §5 redo failure~~ **FIXED 2026-07-28 as root-0033**
      (`docs/design/root-0033-redo-prune-redirect-only-compaction.md`): the
      PG-format prune redo arm `replayDecodedXLogHeapPrune` guarded its
      `VacuumHeapPageBySlots` repack on `len(unused) > 0`, so a **redirect-only**
      prune (the common pgbench HOT shape) skipped the compaction the runtime
      sibling `pagePruneCore` always performs — the replayed page kept the
      redirected chain root's tuple body and the next `xl_heap_update` redo hit
      `ErrNoSpaceInPage`. Same repro now reports `RESTART_OK`; (d) the harness's phantom
      `deferred:` per case after a failed restart (ledger row, same date).
      Cheap method (proven twice): run an alphabetical PREFIX of the suite up to
      the target case (`-run "TestPort_RegressSuite/^(<case1>|…|<target>)$"`,
      cases are discovered in `filepath.Glob` order) with
      `GOOPG_REGRESS_DIFF_DIR`, and ALWAYS grep the log for
      `restarting the cluster` / `restart failed` before reading anything into a
      case's result.
- [x] **server/TestRestartAfterRetention — root-0032 regressed a pass-required
      unit test.** FIXED 2026-07-28 as **root-0035**
      (`docs/design/root-0035-wal-segment-size-derived-from-stream.md`). The
      hypothesis below was wrong in its mechanism: nothing is wrong with the
      `xl_heap_insert` redo arm. The LSN in the message gives it away —
      `301990201 = 18 × 16 MiB`, while the cluster's own checkpoints
      (`17207361`, `18882449`, `segments_removed=16`) are segment 16/18 of a
      **1 MiB** stream. `readAllUncached` anchors at
      `baseOffset = firstSegNo * segmentSize`, and every recovery entry point
      passes `segmentSize = 0` → `DefaultSegmentSize`; nothing on that path ever
      learned the cluster's real size (`OpenOptions.WALSegmentSize` fed only the
      writer). LSNs 16× too large disarm redo's `pd_lsn` idempotency check, so
      startup re-applied already-applied inserts until the page overflowed. The
      bug predates root-0032 — root-0032's contrecord skip only made the path
      decode records at all. Fix derives the size from `xlp_seg_size` the way
      `pg_waldump`'s `search_directory()` does. Note for future loops:
      `RALPH_PRECOMMIT_SCOPE=units` does NOT cover `internal/server` (verified
      2026-07-28: green at HEAD while this test was red), which is why two loops
      shipped over it — the nightly batch is the only gate that sees it.
      Original triage below.
      `go test -run
      TestRestartAfterRetention ./internal/server/` fails deterministically in
      1.9 s with
      `initdb.Open: goopg: wal replay: replay record 0 lsn[301990201,301990520]:
      wal: xlog heap-insert apply: storage: not enough free space in page`.
      **Already bisected**: PASSES at `3716d5cd` (pre-root-0032), FAILS at
      `fa90714a` (root-0032) — so root-0032's `liveSegmentRunStart` /
      leading-contrecord skip changed which records replay after retention, and
      a heap-INSERT redo now lands on a page reconstructed with less free space
      than the running server's. Same shape as root-0033 but on the INSERT arm
      rather than the prune arm, so start by diffing the redo-side page
      reconstruction for `xl_heap_insert` against its runtime sibling
      (`internal/wal/recovery.go` ↔ `internal/storage/`), exactly as root-0033
      did for `xl_heap_prune`. Note `RALPH_PRECOMMIT_SCOPE=units` does NOT
      cover `internal/server` (verified 2026-07-28: the gate is green at HEAD
      while this test is red), which is why two loops shipped over it — the
      nightly batch is the only gate that sees it. Ledger row 2026-07-28.
- [x] **testport/TestPort_IsolationEvalPlanQual** — pass-required isolation spec
      `eval-plan-qual.spec` does not match PG (AI-20260725-011243-004, "also failed
      in the previous run"; repro:
      `go test -v -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/`).
      **FIXED 2026-07-28** (`docs/design/root-0030-lockrows-rescan-state.md`).
      Not an EPQ-tuple-version bug as the earlier triage guessed: `lockRowsOp`
      buffers its rows (`drained`/`pos`/`pending`) and its `Open` is the
      operator's rescan entry point, but `Close` cleared only `pending`. The
      SECOND `Open` — the one `classifySubPlan`'s `rescanCloseOpen` performs for
      the `EXISTS (… FOR UPDATE)` sublink on the EvalPlanQual recheck — therefore
      answered `Next` with EOF without re-scanning, so `EXISTS` collapsed to
      FALSE with zero `noisy_oper()` NOTICEs and `updateOp` dropped the row
      (`checking | 400` vs PG's `-800` — a silently lost update). Fix: reset
      `pending`/`pos`/`drained` at the top of `lockRowsOp.Open`, matching
      `nodeLockRows.c`, which keeps no such buffer. Spec passes (27.6 s); 21
      row-lock + 14 FK/MERGE isolation specs, units, and `tpch-spotcheck.sh`
      (Q12=2, Q13=35) all green.
- [x] **testport/{AmcheckCreateExtension, IsolationInsertConflictSpecconflict,
      IsolationPartitionDropIndexLocking, PgAmcheck002Nonesuch}** —
      (AI-20260725-011243-003/-005/-006/-007). **Stale — all 4 PASS at HEAD**
      (`e7d9b88e`: 0.66 s / 20.27 s / 1.92 s / 1.15 s, one run each). Same
      pre-merge-tip explanation as the executor items. No new work.

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

## M0124 — TPC-DS round-2 closeout: measurement baseline, gate discharge & ledger debt (filed 2026-07-28)

Milestone: `docs/milestones/0124-tpcds-round2-closeout-measurement-and-gate-debt.md`.
Source: `docs/design/tpcds-round2-fixes/README.md` §13.5 actions **1, 5, 6, 7**
(plus §13.4 item 3). **Priority #1 — this milestone holds the NEXT task to
select (2026-07-28(b) amendment); M-NIGHTLY is parked below it and only filed.**
No engine change: if a task uncovers a defect it files a ledger row and an M0125
blocker; a code change landing mid-sweep voids the sweep.

- [x] **M0124-0001 — SF=1 dual-engine re-sweep at HEAD** (§13.5 #1). **COMPLETE
      2026-07-29 — the D7 deliverable landed as
      `analysis/tpcds-sf1-goopg-20260728.md` and the engine-commit freeze
      LIFTS.** All 13 §13.3 projections tested at SF=1: **11 CONFIRMED as
      stated, 2 CONFIRMED on rows and REFUTED on values (Q50, Q46), 0 refuted
      outright.** §13.3's projected **21** goopg-only defects measure **40**:
      ERROR 2 (Q8 Q75, as projected) + TIMEOUT **17** (projected 16 — Q18
      joined; splits **15 unbounded-above** / **2 budget-marginal** Q18+Q35,
      whose verdict flips carry NO signal) + wrong-row-count 3 (Q47 Q49 Q51, as
      projected) + **wrong ANSWER behind a matching row count 18 — a class
      §13.3 could not see**, because the protocol it was written under
      classified a cell by status and row count only. Two notable confirms:
      **Q72**'s projection was SF0.5-derived and had a *contrary* SF=1
      measurement in set A (`OK 14 s`) — the projection won (`TIMEOUT 635 s`),
      a genuine ≥45× regression; **Q47** confirmed on rows but carries an
      unprojected 8.4× slowdown (17 s → 142 s, reproduces at 143 s standalone,
      query-specific, unattributed by design). **40 is a LOWER BOUND** — D6a's
      value comparison is only possible on `OK`/`OK` equal-row cells, so the 17
      timeouts, 2 errors and 3 row-mismatch cells have never been
      value-compared at any scale. **Consequences for M0125:** its baseline is
      this table, not §13.3; the largest class is now wrong answers (18), not
      timeouts (17); **M0125-0009 first** (10 queries, one-line cause, Q97 is
      the impossible-by-construction instance), **M0125-0010** second (4
      queries, independent — neither subsumes the other); never score a Q18/Q35
      verdict flip or a Q50/Q46 row-count match as a win. **M0124-0005 is now
      justified by measurement**: 18 of 99 SF=1 queries pass a row-count-only
      gate while answering wrongly. Original scope below.
      One sweep,
      both engines, uniform 600 s via `scripts/tpcds-bench-compare.sh`
      (`ENGINES="goopg pg"`). Endpoints differ per arm: goopg `-U postgres -d
      postgres` on 65436, PG `-U ryo -d tpcds` on 65438. Records the goopg commit
      (it becomes M0125's baseline), proves S-cold first (`reltuples` +
      `pg_stats` = 0), keeps `RESTART_AFTER_TIMEOUT=1`, and **ports
      `reap_pg_orphans` from `scripts/tpcds-sf05-regression.sh` — the SF=1
      harness has no orphan reap**, so a PG-side timeout leaves a backend running
      and contaminates later timings. **Budget-invariance rule:** a cell may be
      compared only to a prior sweep at the SAME budget. Note §1.4's reproduction
      environment is stale (it names the pre-reorg TPC-H ports). Reports
      confirm/refute for the 13 named §13.3 projections at SF=1 values only
      (Q88 is **TIMEOUT 660 s** at SF=1 — not the SF0.5 228 s figure). Plan
      8–10 h. Deliverable `analysis/tpcds-sf1-goopg-<date>.md`. Design
      `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md`.
      **Chunked execution (added 2026-07-28(b)) — how an 8–10 h sweep fits a
      headless loop whose Bash ceiling is 60 min.** The design doc's "one sweep,
      one budget, one commit" rule is unchanged; only the wall clock is split.
      - `scripts/tpcds-bench-compare.sh` takes a query list or range (`5-14`,
        `8,39,47`), so **a chunk is a query range**. Per-query artifacts land in
        `${TPCDS_RESULTS_DIR}/<engine>_q<N>_{result,explain}.txt` and accumulate
        across chunks by themselves.
      - **Chunk size 8**, run in the FOREGROUND with an explicit Bash `timeout`
        parameter of 55 min, stdout redirected to
        `analysis/tpcds-sf1-resweep-<date>/chunk-<lo>-<hi>.txt`. Eight queries of
        which two time out on both engines is ~45 min; shrink the next chunk if one
        overruns. Never `run_in_background` across a turn boundary (PROMPT.md
        "Headless Execution Reality").
      - **Carry the cursor in `.ralph/working_set.md`** — e.g. `M0124-0001 sweep:
        1-8, 9-16 done; next 17-24`. That baton is what makes a multi-loop task
        resumable; without it the next loop re-runs chunk 1.
      - **Sweep-integrity invariant:** the script prints `# goopg: <git log -1>` in
        every chunk header, so **all chunk headers must name the same SHA**. That is
        the machine-checkable form of "a code change landing mid-sweep voids the
        sweep" — and the concrete reason M-NIGHTLY engine fixes stay parked until
        the sweep completes. If a header disagrees, RE-RUN the affected chunks; do
        not reconcile them narratively.
      - Keep `ENGINES="goopg pg"`, `TIMEOUT_SEC=600`, `RESTART_AFTER_TIMEOUT=1`. A
        chunk boundary is equivalent to the restart the script already performs
        after every goopg TIMEOUT, so chunking does NOT violate the GC-regime rule
        in D3 of the design doc — a fresh server per chunk is more uniform, not
        less.
      - Once per sweep (not per chunk): the S-cold proof (`reltuples` + `pg_stats`
        = 0), the `reap_pg_orphans` port from `scripts/tpcds-sf05-regression.sh`,
        and the final merge into `analysis/tpcds-sf1-goopg-<date>.md` reporting
        confirm/refute for the 13 §13.3 projections.
      - **PROGRESS 2026-07-28 (loop #1 of the chunked sweep).** Once-per-sweep
        prerequisites are DONE and committed: `reap_pg_orphans` ported (design
        doc D4, verified against 65438 — 0 victims, exit 0) and the S-cold proof
        captured at `analysis/tpcds-sf1-resweep-20260728/s-cold-proof.txt`
        (8 relations `reltuples=0 relpages=0`, `pg_stats`=0, `store_sales`=2 880 404).
        Two corrections landed with them: (a) D5's original predicate
        `relnamespace='public'::regnamespace` returns `(0 rows)` on goopg, so the
        S-cold proof was VACUOUS — now `relname IN (...)`; ledger row 2026-07-28
        carries the missing `regnamespacein`; (b) **the same-SHA invariant is
        replaced by same-`engine-tree` + same-`engine-binary`** (doc D4a) —
        `git log -1` both changes on a docs commit and fails to change when
        `server.sh start` rebuilds the engine from an uncommitted worktree at a
        `RESTART_AFTER_TIMEOUT` bounce. The header now prints
        `engine-tree:`/`engine-binary: running=… on-disk=…` (running = sha256 of
        `/proc/<postmaster>/exe`; the serving image was 16 h stale at loop start)
        and `restart_goopg` prints `*** SWEEP VOID … ***` on a mid-sweep change.
        Sweep baseline: `engine-id bba744a8… c47d4ed6… diff=e3b0c44298fc`,
        `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`. Every later chunk must reprint
        that `engine-id` unchanged.
      - **Chunks 1–8 DONE** (`analysis/tpcds-sf1-resweep-20260728/`:
        `chunk-1-4.txt`, `chunk-5-8.txt`, running table `RESULTS.md`).
        All eight cells reproduce set A at the same 600 s budget — Q1 246 s/100,
        Q2 27 s/2513, Q3 15 s/31, Q4 TIMEOUT on **both** engines, Q5 goopg-only
        TIMEOUT, Q6 57 s/44 (PG 140 s), Q7 64 s/100, Q8 ERROR `column ref
        ca_zip/57 out of MaterializedSlot range 1` with the server surviving
        (confirms the §13.3 "contained, not fixed" projection). The reap earned
        its keep on the first PG timeout: Q4 left one backend running and it was
        terminated. ~~**NEXT: chunk `9-16`.**~~ done, see below.
      - **Chunks 9–16 DONE** (loop #2; `chunk-9-12.txt`, `chunk-13-16.txt`).
        All eight cells reproduce set A again — Q9 143 s/1, Q10 goopg-only
        TIMEOUT, Q11 79 s/95 with **PG timing out** (goopg wins this one),
        Q12 6 s/100, Q13 57 s/1, Q14 goopg-only TIMEOUT (PG 37 s/200 — the
        summed two-block count from harness fix 2), Q15 17 s/100, Q16 48 s/1.
        Largest elapsed delta vs set A is 25 s and lands on the 600 s-budget
        timeouts, i.e. teardown rather than query time. The range was split into
        two harness calls (`9-12`, `13-16`) to stay inside the loop's foreground
        Bash budget — both reprint the baseline `engine-id` unchanged, so under
        D4a they are one continuous sweep, not two. The reap fired again on PG's
        Q11 timeout. Running D6 classification for Q1–Q16: both-engine timeout
        Q4; goopg-only Q5/Q10/Q14; PG-only Q11; goopg ERROR Q8.
        ~~**NEXT: chunk `17-24`.**~~ done, see below.
      - **Chunks 17–24 DONE** (loop #3; `chunk-17-24.txt`, one harness call —
        no timeout in set A for this range). Seven of eight cells reproduce set A
        within 6 s: Q17 53 s/1, Q19 64 s/100, Q20 14 s/100, Q21 50 s/100,
        Q22 156 s/100, Q23 210 s/1, Q24 75 s/0 (both engines empty).
        **Q18 flipped as predicted:** set A `OK 626 s / 100`, this sweep
        `TIMEOUT 627 s / 0`. One second apart, so the query did the same work and
        only landed on the other side of the 600 s cut (cell elapsed includes the
        ≤30 s EXPLAIN capture, which is outside the timeout-guarded query — that
        is how a cell can read `OK` above the budget). Recorded as **budget
        noise, not a regression**, and not re-run.
        **D6 needs a sub-class because of it, and this is load-bearing for
        M0125:** Q18 is *budget-marginal* (true runtime known to sit within ~1 %
        of the budget), whereas Q5/Q10/Q14 were cut with their true runtime
        *unbounded above* — no run has ever seen them finish. Movement on Q18 at
        a 600 s budget is a re-rolled coin and must not be reported as a
        fix or a regression; movement on Q5/Q10/Q14 is real signal. To make Q18
        informative, classify it by measured runtime or give it a larger budget.
        Running D6 classification for Q1–Q24: both-engine Q4; goopg-only
        unbounded Q5/Q10/Q14; goopg-only **budget-marginal Q18**; PG-only Q11;
        goopg ERROR Q8. No reap this range (no PG timeout); the post-Q18 restart
        again moved the binary image (`01bb0f65…` → `22110d95…`) with `engine-id`
        unmoved — the documented `vcs.revision` stamp effect, not a source change.
      - **Chunk 4 (Q25–Q32) DONE** (2026-07-28, split into `chunk-25-28.txt` +
        `chunk-29-32.txt` because set A shows two goopg timeouts in the range;
        both calls reprint the sweep-baseline `engine-id`, so D4a holds and this
        is still ONE sweep). All eight cells reproduce set A — largest delta 5 s
        (Q27 239 → 234 s). **Q30 and Q31 are the cleanest D6 goopg-only members
        yet:** PG answers both cheaply and exactly (13 s/63 rows, 12 s/43 rows),
        goopg has never completed either in any run of either sweep, and all four
        observations (649/647 s set A, 627/629 s here) are the harness cutting a
        still-running execution — **unbounded above**, like Q5/Q10/Q14, NOT
        budget-marginal like Q18. They are therefore valid M0125 targets whose
        movement would be real signal, and they carry a PG row count to validate
        against. Running D6 classification for Q1–Q32: both-engine Q4; goopg-only
        unbounded Q5/Q10/Q14/**Q30**/**Q31**; goopg-only budget-marginal Q18;
        PG-only Q11; goopg ERROR Q8. No reap this range (no PG timeout); both
        restarts reported the same post-restart image (`46632999aa3f5c75`) —
        the stamp moves with the build commit, not per restart.
      - Chunk 5 (`chunk-33-40.txt`, Q33–Q40, no split needed) reproduces set A on
        all eight cells; largest delta 5 s (Q37 311 → 316 s) and every completed
        cell matches PG's row count, incl. Q39's 236 [230+6]. **Q35 is the second
        budget-marginal member (with Q18), NOT unbounded:** both sweeps cut it at
        the budget (651 s set A, 628 s here) but the 2026-07-26 baseline
        *completed* it at `OK 525 s`, so a later `OK` is a re-rolled coin, not a
        fix — M0125 must not score a Q35 flip as a win (it does carry a PG row
        count, 100, so a real fix is still validatable on rows). **Q36 is not a
        goopg defect**: dsqgen emits malformed text that PG rejects too, hence
        `PG_SKIP="36 70 86"`. Running D6 for Q1–Q40: both-engine Q4; goopg-only
        unbounded Q5/Q10/Q14/Q30/Q31; goopg-only budget-marginal Q18/**Q35**;
        PG-only Q11; goopg ERROR Q8; not-a-goopg-error Q36. The single restart
        (after Q35) moved the image `46632999aa3f5c75` → `9a6a5c070ad7364d` with
        `engine-id` unmoved — third live confirmation that the docs-only chunk-4
        commit re-stamps `vcs.revision` without touching the engine.
        **NEXT: chunk `41-48`.** Set A shows NO timeout in that range (slowest
        Q44 58 s), so one call, est. ~5 min. Predict the known **RC-1b row gap at
        Q47** (goopg 0 rows vs PG 100) — a correctness delta, not a timing one,
        and already-known, so it must not be filed as a new finding.
      - **Chunks 41–64 DONE** (loops #6–#8; `chunk-41-48.txt`, `chunk-49-56.txt`,
        `chunk-57-64.txt`, one harness call each, all reprinting the sweep-baseline
        `engine-id`). Per-cell detail lives in `RESULTS.md` (authoritative); the
        two conclusions that outlive the chunks: (1) the **runtime-deviation
        class opened at Q47 (8.4×) is CLOSED and empty** — chunk 49–56's decisive
        cell was **Q50, whose row gap closed 0 → 6 = PG**, proving the RC-1b fix
        `5db0a067` landed and changed plans in that family, so Q47's 17 s → 142 s
        is the cost of newly-*correct* input, not a regression. The chunk-41–48
        rule "rows didn't move ⇒ not a newly-correct plan" was **wrong** and must
        not be reused; Q47's surviving 0-vs-100 is a separate downstream defect.
        (2) Chunk 57–64 is the **first fully uneventful chunk**: all seven OK
        cells match PG's rows exactly and reproduce set A within ±3 s. Q58's 0
        rows is **not** a gap — PG returns 0 too. Running D6 for Q1–Q64:
        both-engine Q4; goopg-only unbounded Q5/Q10/Q14/Q30/Q31/Q54/**Q64**;
        goopg-only budget-marginal Q18/Q35/**Q51** (Q51 did not flip, 587 s, 13 s
        headroom); PG-only Q11; goopg ERROR Q8; not-a-goopg-error Q36. Row
        mismatches among OK queries Q1–Q64: Q47, Q49, Q51.
      - **Chunk 65–72 DONE** (loop #9; `chunk-65-72.txt`, one harness call, ~45 min,
        sweep-baseline `engine-id` reprinted). Q65/Q67/Q69/Q71 reproduce set A's
        goopg-only TIMEOUTs; Q66 (5) and Q68 (100) match PG within ±3 s of set A;
        Q70 is the known dsqgen ERROR with the PG arm skipped by design. The
        finding is **Q72 — the first cell in the re-sweep where a set-A `OK`
        becomes a `TIMEOUT`** (`OK 14 s / 0` → `TIMEOUT 635 s`, re-probed on a
        fresh server at 636 s, `probe-q72-reprobe.txt`). Server age is ruled out
        twice: the re-probe followed the harness restart, and Q66/Q68 reproduce
        set A at the same server age in this chunk. Evidence-consistent reading
        (**hypothesis, not established** — no plan diff was run): this is the
        RC-1b fix `5db0a067`, which set A §2.1 predicted would touch Q72, and
        which has now produced three family outcomes — **Q50 fixed 0 → 6 = PG,
        Q47 17 → 142 s still wrong, Q72 past the budget**. Q72's plan bottoms out
        in a 4-table MHJ (`warehouse`/`item`/`inventory`/`catalog_sales`) with
        **no `Filter` on that node**. Consequence: set A's Q72 row gap (0 vs 100)
        is **no longer observable**, so Q72 joins Q64 in the "unbounded AND
        unvalidatable" bucket — reaching it by regressing out of OK rather than
        by always having been a timeout. Any M0125 fix for Q72 must be validated
        on ROWS, not merely on completion.
        **NEXT: chunk `73-80`.** Check set A (`analysis/tpcds-sf1-goopg-20260727.md`
        §5.2, rows `^| 7[3-9]|^| 80 `) for the timeout count in range FIRST and
        size the Bash `timeout` accordingly. In this range **Q74 is a PG-side
        TIMEOUT** (652 s; goopg OK 36 s) — the only PG arm that times out here, so
        `reap_pg_orphans` will fire; budget for it and do not read it as a goopg
        result. Q78 and Q81 are goopg-only timeouts in/near the range.
      - **Chunk 73–80 DONE** (loop #10; `chunk-73-80.txt`, one harness call,
        ~35 min, exit 0, sweep-baseline `engine-id` reprinted unchanged).
        Q73/Q76/Q77/Q79/Q80 match PG on rows within ±5 s of set A; **Q78**
        reproduces its set-A goopg-only TIMEOUT (637 s) and **Q74** reproduces its
        **PG-side** TIMEOUT (638 s) while goopg answers in 34 s with PG's rows —
        the sweep's second PG-only timeout after Q11, and the first `reap_pg_orphans`
        fire in this range (1 backend terminated). The finding is **Q75 — the first
        set-A `OK` to become an `ERROR`** (`OK 47 s / 100` → `ERROR 66 s`,
        `ERROR: division by zero` at `query75.sql:67`); the server survives (Q76 ran
        next), the Q8 contained-error shape. This is the **predicted** outcome, and
        that is its value: ledger `tpcds-round2 Q75-eval-order` (2026-07-27) already
        had it deterministic 3/3 at SF0.5 and **M0125-0004** already carries the
        diagnosis, so this chunk promotes §13.3's *projection* to a *measurement* at
        SF=1 under the sweep baseline. It also completes RC-1b `5db0a067`'s **fourth**
        family outcome: **Q50 fixed 0 → 6 = PG, Q47 17 → 142 s still wrong, Q72 past
        the budget, Q75 into a contained error** — one mechanism (input stopped being
        silently zeroed), three cells that read as regressions on the verdict column
        while being strict improvements in input correctness. **Do not read set A's
        Q75 `100` as a pass**: the ledger proves the pre-fix CTE computed 1,057,469
        vs PG's 2,368,670, i.e. 100 garbage rows whose *count* matched under
        `LIMIT 100` — HEAD's loud ERROR is more honest, and this cell is the concrete
        justification for **M0124-0005**'s value checksum. Nightly `Q75,100,pinned`
        (`ci/batch/tpcds-row-anchors.csv:46`) is therefore a live break with no
        `expected-failures.csv` entry, but the TPC-DS row-anchor gate is **not** one
        of the banner's four carve-out gates, so it stays filed and unchecked under
        M0125-0004. Also repaired a chunk-9 bookkeeping gap: `RESULTS.md`'s Results
        table stopped at Q64 (the 65–72 prose landed without its rows); rows 65–72
        are backfilled from `chunk-65-72.txt`, no figure changed. Running D6 for
        Q1–Q80: both-engine Q4; goopg-only unbounded
        Q5/Q10/Q14/Q30/Q31/Q54/Q64/Q65/Q67/Q69/Q71/Q72/**Q78**; goopg-only
        budget-marginal Q18/Q35/Q51; PG-only Q11/**Q74**; goopg ERROR Q8/**Q75**;
        not-a-goopg-error Q36/Q70. Row mismatches among OK queries Q1–Q80: still
        Q47, Q49, Q51.
      - **Chunk 11 (Q81–Q88) DONE** (`chunk-81-88.txt`, ~40 min, exit 0, baseline
        `engine-id` reprinted unchanged). All eight cells reproduce set A in class
        and row count — by the harness's row-count measure, an uneventful chunk.
        It was not. Acting on chunk 10's Q75 lesson (a matching row count can hide
        a corrupt answer), this loop **diffed result VALUES against PG for every OK
        cell** — the sweep's first value-level comparison — and caught **Q87: 1 row
        on both engines, goopg `47218` vs PG `47049`**. Root-caused fully by
        read-only probe: the three input branches match PG exactly, `A except B`
        alone matches, but goopg's three-way result EXCEEDS its own two-way result
        (impossible for a left-associative set difference) and equals PG's answer
        for the right-associated reading. Trigger is **per-branch parenthesisation**:
        bare `A except B except C` is correct, `(A) except (B) except (C)` is not,
        nor is `except all`, nor the mixed chain `(A) union (B) except (C)`;
        UNION/INTERSECT-only chains are unaffected only because they are associative.
        Mechanism: `parseParenthesisedSelectStmt` sets `Parenthesized = true`
        (`internal/parser/select.go:1005`) *before* absorbing a trailing set-op
        written outside those parens (`select.go:1007-1039`), so the planner's
        left-associative flattening loop breaks early at
        `internal/planner/planner.go:696-698`. **Filed as M0125-0006 with a ledger
        row; NOT fixed — the sweep forbids any engine commit before Q99.** Two
        answer-neutral PG-compat gaps from the same diff: **Q83** numeric-division
        result scale (`0.0` vs PG `0.00000000000000000000`, i.e. no `select_div_scale`)
        and **Q82** a 1-char column-width delta consistent with a trimmed trailing
        space. New D6 note: **Q82 is budget-marginal** — it passed at 556 s with only
        44 s of headroom, the narrowest OK margin of the sweep. Running D6 for
        Q1–Q88: both-engine Q4; goopg-only unbounded
        Q5/Q10/Q14/Q30/Q31/Q54/Q64/Q65/Q67/Q69/Q71/Q72/Q78/**Q81**/**Q88**;
        budget-marginal Q18/Q35/Q51/**Q82**; PG-only Q11/Q74; goopg ERROR Q8/Q75;
        not-a-goopg-error Q36/Q70/**Q86**. Answer mismatches among OK queries
        Q1–Q88: Q47, Q49, Q51 by row count **plus Q87 by value at a matching count**.
        **NEXT: chunk `89-96`.** Read set A (`analysis/tpcds-sf1-goopg-20260727.md`
        §5.2, rows `^| 9[0-6] ` and `^| 89 `) for the timeout count in range FIRST and
        size the Bash `timeout` accordingly — count **both** engines' columns (col 1 =
        goopg, col 2 = PG; loop #10's baton undercounted 73–80 by reading only the
        goopg side). **Value-diff every OK cell** against PG (`diff` the
        `{goopg,pg}_q<N>_result.txt` pairs in
        `bench/tpcds/runtime_goopg/tpcds-results/`, normalising whitespace to
        separate psql rendering from real divergence) — this is now part of the
        per-chunk procedure, not an M0124-0005 deliverable alone.
      - **Chunk 12 (Q89–Q96) DONE** (`chunk-89-96.txt`, ~4 min, exit 0, baseline
        `engine-id` reprinted unchanged). All eight cells are `OK` on both engines
        and every row count reproduces set A — **no** new timeout/error/skip, so
        every D6 list is unchanged from Q1–Q88. By value the chunk is the sweep's
        worst: **Q94 and Q95 both return `0 / NULL / NULL`** against PG's
        `9 / 18130.71 / -9444.12` and `57 / 85887.62 / -27169.36`, at a matching
        row count of 1. Three defects root-caused this loop by read-only probe:
        (1) **unpadded date literals** — PG accepts `'2002-5-01'`, goopg's
        fixed-layout `time.Parse("2006-01-02", …)` does not; the cast path ERRORs
        but the *comparison* path silently matches 0 rows, turning a compat gap
        into a wrong answer (affects Q16/Q94/Q95) → **M0125-0007**;
        (2) **SEMI + ANTI conjunction is not a subset** — with dates padded, Q94's
        `EXISTS` alone and `NOT EXISTS` alone each match PG exactly (33/25 and
        11/9), but together goopg returns 25/18 where PG returns 11/9; a conjunct
        that *grows* the result is a hard correctness violation (the Semi/Anti
        residual ↔ source-table pair of hard-won rule #2) → **M0125-0008**;
        (3) **sibling `sum(CASE …)` aggregates collapse onto the first slot** —
        `parserExprKey`'s fallback returns `fmt.Sprintf("expr:%T", e)`
        (`internal/planner/planner.go:7484`), the Go type name with no content, so
        every `*parser.CaseExpr` hashes identically and the 2nd..Nth pivot
        aggregate is dropped as a duplicate at `planner.go:5844-5846`; **17 expr
        types** share that fallback and the same key feeds GROUP BY matching. This
        is the **third** recurrence of the failure mode already documented at
        `planner.go:6905-6909` (`count(*)` vs `count(*) FILTER`, M0097-0032)
        → **M0125-0009**. None fixed — the sweep forbids any engine commit before
        Q99. **Back-applied the value diff to the whole sweep** (chunks 1–10 were
        row-count-only): **Q16 was already wrong in chunk 2** — recorded `OK / 1`
        while returning `0` vs PG's `45`. Restricted to cells fresh this sweep,
        `OK` on both engines and equal in row count, **21 diverge by value** —
        Q2 Q7 Q16 Q21 Q26 Q27 Q28 Q39 Q40 Q43 Q46 Q50 Q59 Q62 Q66 Q68 Q79 Q83 Q87
        Q94 Q95 — none ordering-only; Q7/Q26/Q83 are the answer-neutral
        numeric-scale gap. Full per-query attribution filed as **M0124-0006**.
      - **PROGRESS 2026-07-28 (chunk 13 of 13 — Q97–Q99, the FINAL chunk).**
        `scripts/tpcds-bench-compare.sh 97-99` dual-engine, foreground, ~2 min,
        exit 0; header reprinted the sweep baseline `engine-id bba744a8…
        c47d4ed6… diff=e3b0c442` unchanged, so all 99 queries sit in ONE sweep at
        ONE budget. All 3 cells `OK` on both engines, row counts reproduce set A
        (1 / 2531 / 90), timings within noise, and **no** new timeout/error/skip
        → every D6 list CLOSES unchanged from Q1–Q96. The value diff (mandatory:
        the on-disk `q97..q99` files were STALE set-A artifacts excluded from the
        chunk-12 re-audit) makes **2 of the 3 final cells wrong answers behind a
        matching row count**: **Q97** (`392155|392155|392155` vs PG
        `541140|286927|161`) and **Q99** (cols 2–5 replicate col 1) are the
        **4th and 5th instances of M0125-0009** — no new defect; Q97 is its most
        legible instance anywhere in the sweep, since its three columns are
        disjoint by construction (a customer cannot be store-only, catalog-only,
        and both), so equal values are not merely wrong but impossible.
        **Q98's values are CORRECT** — its 5068-line raw diff is two known
        answer-neutral rendering gaps: (a) `char(n)` not blank-padded (probed:
        `octet_length(sm_type)` = 30 on PG vs **7** on goopg, both
        `character(30)`; `length()` agrees only because PG's `bpcharlen` ignores
        trailing blanks, so it is NOT evidence of correctness) — already in the
        ledger at row 2026-07-06 (M0122-0005), which now gains its first TPC-DS
        consequence but needs no new filing; and (b) the numeric-scale gap, newly
        narrowed — goopg's division rscale is right in general and short-circuits
        **only on exactly-zero results** (`0::decimal(15,2)*100/2531.00` → goopg
        `0.00` vs PG `0.00000000000000000000`, while the non-zero quotient is
        byte-identical). One false alarm ruled out: Q99's `31-INTERVAL '60 days'`
        headers appear identically in PG's output — they are in `query99.sql:7`
        itself (TPC-DS generator substitution), not a goopg aliasing bug.
        **THE SWEEP IS COMPLETE (99/99, 13/13 chunks).** The 21-cell
        value-divergence list becomes **23** (`+Q97 +Q99`; Q98 explicitly not a
        member). **NEXT: the merged deliverable
        `analysis/tpcds-sf1-goopg-20260728.md`** (confirm/refute the 13 §13.3
        projections at SF=1 values), with **M0124-0006** due before/with it. The
        engine-commit freeze LIFTS once the deliverable lands; **M0125-0009 is
        the recommended first fix** (one-line root cause, 5 queries of evidence).
      - One more guard correction landed after chunk 1 (doc D4a): the
        comparability key is `engine-id` (committed engine trees + digest of
        uncommitted engine edits), NOT the binary sha — `go build` stamps
        `vcs.revision`/`vcs.modified`, so the docs commit alone moved the image
        and the first-cut guard printed a false `*** SWEEP VOID ***` in
        `chunk-1-4.txt`. That chunk stands; `RESULTS.md` carries the proof.
- [x] **M0124-0002 — retroactive TPC-H + plan-baseline discharge for `9740fce9`**
      (§13.5 #5). **DONE 2026-07-29 — DISCHARGED, no regression attributable to
      `9740fce9`.** Report `analysis/tpch-tpcds-round2-retro-20260729.md`; raw
      output + the two harness scripts under `analysis/m0124-0002/`. Both arms
      built from HEAD `40ad746a` (arm A = the `bushy.go` hunks reverse-applied in
      worktree `tmp/wt-armA`, −95/+1; `internal/executor/expr.go` verified
      byte-identical between arms so the Q8 crash could not confound it).
      **`make plan-diff LABEL=tpcds-round2-base MODE=structural` = 22/22 MATCH** —
      `9740fce9` changes which conjuncts are remapped, not which plan is chosen,
      on every TPC-H query, which also makes the timing table like-for-like.
      22/22 queries completed on both arms with identical row counts; 12/12
      anchors (`spotcheck_expected.env` + `ci/batch/tpch-row-anchors.csv`) exact
      on both. Stream 912 s (A) vs 885 s (B). Two queries crossed the >10 %
      investigate band — Q9 −13.6 %, Q22 +14.3 % — and round 2 re-read both:
      **intra-arm spread beat the inter-arm gap in each case** (Q9 arm A alone
      202.5 → 166.3 s = 22 %; Q22 first-vs-later read inside one server = 22 %),
      so both are stream-position / page-cache artifacts. Nothing near the 25 %
      blocking band; §D5 never triggered. **`plan_snapshots/tpcds-round2-head.txt`
      is committed and is the live baseline** (captured LAST — `plan-gate` picks
      the newest by mtime and has no label parameter), with
      `tpcds-round2-base.txt` alongside as the arm-A reference. **This unblocks
      M0125-0002 / -0004 / -0005 and M0125-0003 stage 2.** §5 of the report
      retro-files §8 step 7's missing artifact for phase 2.1 (RC-1b), transcribed
      from `5db0a067` + its ledger row, not re-measured. **One deviation, ledger
      row 2026-07-29:** §D1's A/B/A/B was reduced to a full round 1 plus a
      two-query round 2, so a *uniform* drift would be invisible to it; resume
      recipe in the row. Original scope follows. Phases 1.2/1.3 landed while
      `tpch-spotcheck.sh` reported SKIPPED
      and `make plan-gate` was never run (§13.4 item 4); `ef4a65a5` rebuilt the
      cluster, so it is runnable. **Both arms build from HEAD** — arm A = HEAD
      with `9740fce9`'s `bushy.go` hunks locally reverted (its executor bounds
      check STAYS, or the Q8 crash returns and confounds the arm), arm B = HEAD —
      run A/B/A/B alternating. A literal checkout of `9740fce9` is wrong: it
      predates the cluster rebuild, and `b3493a6e`..HEAD spans four `internal/`
      commits including `095e3ab5`'s new fsync GUC, a confound in a timed A/B.
      S-cold by necessity (`ANALYZE <table>` in db `tpch` errors — ledger
      `bench-reorg ANALYZE-scope`), `GOGC=100` + `GOMEMLIMIT=12GiB` (Q21 OOMs at
      18 GiB). Use the Makefile defaults `PLAN_DB=tpch PLAN_USER=tpch`; the
      `postgres@postgres` advice is stale folklore and would capture an empty
      database. `plan-diff` **requires `LABEL=`** and diffs live-vs-stored;
      `plan-gate` picks the newest snapshot by **mtime**, so capture-then-gate on
      one arm is green by construction. Capture and **commit**
      `plan_snapshots/tpcds-round2-head.txt` **last**. Also retro-files §8 step
      7's missing `analysis/` artifact for phase 2.1. Noise band: >8 % explained,
      >25 % blocks (round-5 §3 calls 2–8 % moves unattributable). Design
      `docs/design/0124-0002-retroactive-tpch-plan-gate-discharge.md`.
- [x] **M0124-0003 — append the seven missing §10 deferral-ledger rows** (§13.5
      #6). **DONE 2026-07-29.** 13 rows appended to `.ralph/deferral_ledger.md`
      (516 → 529 lines): D2's seven §10 rows, D3's drop row, D5's five
      audit-produced rows — plus D4's `UPDATE` on the existing `pq-P10` row
      naming **M0125-0003** as the consumer of its option (b) and recording that
      option (a), actually persisting `reltuples`/`relpages`, stays deferred and
      unowned. No eighth `reltuples` row. The moot `aggregateOp work_mem` row is
      `status = resolved` with the drop in its task-id, so **no new status value
      was invented** and M0119's queue gains no phantom work. D6 render check via
      `gh api --method POST /markdown`: one table, 14 body rows, 7 cells each.
      **Every claim was re-resolved against HEAD before it was written, and six
      cites had drifted** — `open.go:2911`→`:2924`; §3.5's push/remap pair
      `planner.go:1020`→`:1012` vs `:1024`, with the compensating
      `pushSingleSourceFiltersAfterRemap` at `:1037`; the reorder's resume point
      is a *call-order* change in `planner.go`, not `mhj_input_rewrite.go`;
      grouping-sets `:3176`→`:650`; the MHJ gate's two conditions at
      `local_filters.go:171`/`:175`; `*SetOp` at `parallel.go:313`. Verification
      strengthened three rows: **ANALYZE cannot reach either `Invalidate()` call
      site by any path** (it plans to `*planner.Utility`, both sites are
      `*planner.DDL`-guarded) and `planCacheIsCacheable`'s comment names a class
      its switch omits; **`walkColumnRefsImpl`'s missing `default:` is fail-open
      in the dangerous direction** — no callback means no `onOuter()` veto, so a
      conjunct wrapping an outer ref reads single-side and can be pushed below an
      outer join, which makes **M0125-0002 nine walkers, not seven**; and the
      value-blindness row covers `ci/batch/tpch-row-anchors.csv` as well as the
      TPC-DS anchors. Follow-up observed, deliberately not fixed (Non-goals):
      nine pre-existing rows carry unescaped `|` inside code spans and already
      render with 8–21 cells — a second instance of the ledger rendering hazard,
      same `\|` discipline. Design doc now `accepted` with an execution record.
      Original scope follows. §13.2: the seven `tpcds-round2` rows that exist are
      the rows the WORK produced, not the rows §10 planned. Append: parse-time IN-list
      `select_common_type` (§5.4); the `rewriteScanInputsWithSingleTablePredicates`
      reorder (§3.5); the `shouldAttachBeforeMHJ` `SmallDimension` gate (RC-5);
      shared-scan GROUPING SETS (RC-7); EXISTS-under-OR / hashed-SubPlan caching
      (RC-8, **now including Q35**); parallelising `SetOp` (RC-9); `plancache`
      invalidation on ANALYZE (also a measurement-protocol blocker — it is why
      §8's S-warm is single-shot per process). Record an explicit **drop**
      disposition for the moot `aggregateOp work_mem` row (its §6 precondition
      never fired — Q39 was a `Quo(0,0)` panic, MemoryPeak 13.2 G under a 24 G cap,
      no `oom_kill`) rather than appending it as open, since M0119 treats the
      ledger as a work queue. Plus a `pq-P10` UPDATE naming M0125-0003 as consumer,
      and five rows the audit produced: CI row-anchor value-blindness; the two
      out-of-scope `default:`-less walkers (`walkColumnRefsImpl` `pushdown.go:362`
      and the `shiftColumnRefs` closure); `GOOPG_POSMAP_ASSERT`; phase 0.2's
      unfinished panic→`XX000` half (`server.go:780` is still the only
      `recover()`); and Q47/Q49/Q51 as three distinct defects. Row shape is the
      ledger's own 7-column header, not fix_plan.md's stale 6-column text.
      Entity-escape any literal `<table>`/`<col>`; verify via
      `gh api --method POST /markdown`. Design
      `docs/design/0124-0003-round2-deferral-ledger-completion.md`.
- [x] **M0124-0004 — recover or classify Q35's row count** (§13.5 #7).
      **CLOSED 2026-07-30 on the CLASSIFY branch** (the item's own disposition —
      "recover **or** classify"). Quiet host, HEAD `bd8c484d`: the designed solo
      SF0.5 sweep (`TIMEOUT_SEC=1800`, fresh server) = `TIMEOUT` 1964 s, the
      escalation to a **plain** SF=1 run = `rc=124` at 1974 s. Both readings are
      **valid** and supersede the void 2026-07-29 pair. The SF=1 `EXPLAIN` at HEAD
      is **byte-identical** to the 05:36 capture, so neither `beb7af82` nor
      `c26c6fc3` (M0125-0003 stage 1) flipped Q35's shape — stage 1 is
      shape-neutral here, as designed. **The 525 s history is refuted, not merely
      unreproduced:** outer cardinality
      (`customer ⋈ customer_address ⋈ customer_demographics`) = **96,562** and one
      buffer-**warm** `EXISTS` #1 evaluation floors at **8.16 s** (×4, ±0.5 %), so
      the AND-ed conjunct **alone** floors at **≈9.1 days** at SF=1 (≈4.6 days at
      SF0.5). A plan whose cheapest conjunct costs nine days did not return 100
      rows in 525 s; `651 s`/`628 s` are kill lines, not runtimes. Verdict:
      **performance-only, RC-8 shape — not a wrong answer hiding behind a
      timeout.** The SF0.5-slower-than-SF=1 anomaly **dissolves** (the sampler
      halves facts by key parity but copies dimensions whole, so the outer
      cardinality is unchanged and both scale factors are kill lines whose
      ordering carries no information). **RC-8's "measure first" is discharged for
      Q35** without a completing `EXPLAIN ANALYZE`: `Calls` = 96,562, per-call
      cost = 8.16 s. **The row count stays unrecovered and is NOT recoverable by a
      bigger budget** (900 s → 1800 s → 3600 s all sit ~3 orders of magnitude
      below the floor) — it is gated on **M0125-0003** decorrelating the RC-8
      shape, and **Q35 is that item's natural acceptance query** (first
      terminating run vs the git-tracked oracle `35|OK|100|0`). Ledger row
      2026-07-30; artefacts `analysis/tpcds-q35-m0124-0004/`; design doc
      §"Execution record (2026-07-30)". **M0124 is now fully closed.**
      Original specification follows. Q35 is the
      only query that has never produced a goopg row count. Its 2026-07-26 count
      was lost to the **PATH-loss** harness defect, NOT `tail -1` — `query35.sql`
      is a single statement and the multi-statement set is Q14/Q23/Q24/Q39. Two
      further corrections: the SF0.5 "201 s" is a **kill line, not a runtime**
      (every TIMEOUT in that sweep carries ~20 s of harness overhead above its
      budget), and Q35 **also timed out at the 300 s budget (319 s)**, so its
      SF0.5 runtime is unknown and above ~300 s. **PG's answer is already git-tracked** —
      `oracle.txt` holds `35|OK|100|0` — so this is a goopg-only question. Cheap
      path: solo SF0.5 run at 900 s on 65437 — but **a small script change is in
      scope**, because none of it is runnable today: the SF0.5 script has no
      per-query mode, `TPCDS_RESULTS_DIR` is not env-overridable (so the run
      would clobber M0124-0001's artifacts anyway), and `restart_goopg` hardcodes
      `sf1`. Respect `guard_sf1_sweep` rather than `FORCE=1`-ing it, and run
      AFTER M0124-0001 (the deliverable is a row in its report). Escalate to SF=1
      at 1800 s only on mismatch. **Must run solo from a fresh server.** Record the anomaly that Q35 completed at SF=1 (525 s)
      but never at SF0.5 on half the data. Fold in one `EXPLAIN ANALYZE` with the
      per-SubPlan counters: Q35 is an exact instance of RC-8's
      `exists(…) and (exists(…) or exists(…))` shape, so this discharges RC-8's
      "measure first" criterion for three queries at once. Outcome classifies Q35
      as performance-only (→ M0125-0003) or as a wrong answer hiding behind a
      timeout, the Q51 shape. Design
      `docs/design/0124-0004-q35-rowcount-resolution.md`.
      **PARTIALLY EXECUTED 2026-07-29 — stays OPEN; the count is still
      unrecovered.** The three script blockers are FIXED and verified (`QUERIES=`
      per-query mode via a new `query_list` shared by `sweep`/`oracle`, subset
      reports stamped `SUBSET PROBE … NOT a gate result`;
      `SF05_RESULTS_DIR`/`TPCDS_RESULTS_DIR`/`SF05_ORACLE` env-overridable with
      the oracle pinned to its git-tracked home independently of the redirect;
      `restart_goopg` → `${SF_LANE:-sf1}`). Both probes TIMED OUT (SF0.5 solo
      900 s → 921 s; SF=1 `EXPLAIN ANALYZE` 1800 s → 1846 s) and **both readings
      are VOID: the nightly CI batch was running throughout** (fired 00:23:44,
      TPC-H stage at 112% CPU / 7.5 GiB RSS on the 16-core host). Neither
      harness had a host-quiet guard; both now refuse to start under
      `ci/batch/run-nightly.sh`/`ci/batch/stages/`, after fixing a `ps | grep`
      self-match via `bench_foreign_procs` (`bench/tpcds/env_tpcds.sh`).
      **Two things a later loop must NOT re-derive.** (1) The
      SF0.5-slower-than-SF=1 anomaly is **not a plan flip** — the plans are
      byte-identical at both scale factors and `customer` holds 100,000 rows at
      both (the sampler halves facts by key parity, copies dimensions whole), so
      the outer cardinality multiplying all three SubPlans is unchanged. (2)
      RC-8's "measure first" is **half discharged**: the shape is confirmed
      (`$0 = ss_customer_sk` is a nested-loop **Filter**, not an index cond — so
      each of three `EXISTS` re-scans a whole fact table per outer row), but the
      `Calls`/`CacheMisses` counters need a COMPLETING `EXPLAIN ANALYZE`.
      **Resume:** on a quiet host, `QUERIES=35 TIMEOUT_SEC=1800
      RESTART_AFTER_TIMEOUT=0 SF05_RESULTS_DIR="$PWD/analysis/tpcds-q35-m0124-0004"
      bash scripts/tpcds-sf05-regression.sh sweep`; on a second TIMEOUT escalate
      to SF=1 with a **plain** run, NOT `EXPLAIN ANALYZE` (per-tuple
      instrumentation inside three per-row SubPlans is itself a large multiplier
      and confounds the 525 s/628 s comparison). Artefacts
      `analysis/tpcds-q35-m0124-0004/`; ledger rows 2026-07-29 ×2.
      **Side effect for M0125:** the SF0.5 sweeps of 2026-07-29 00:47 and 03:38
      (including the M0125-0009 fix's) were taken under the nightly — their
      row-count verdicts survive, their TIMEOUT verdicts and timings do not.
      M0124-0001's SF=1 re-sweep is clean (finished 34 min before the fire).
- [x] **M0124-0005 — add a value checksum to the SF0.5 oracle** (§13.4 item 3).
      **DONE 2026-07-29.** `scripts/tpcds-result-checksum.py` (new),
      `cmd_oracle` switched to a **plain** PG run emitting
      `q|status|rows|ck|secs`, `cmd_sweep` teaching the fatal `CKMISMATCH`
      verdict and a `PASS=N (x ck-verified, y ck=n/a)` summary; `oracle.txt`
      re-captured with the normalisation contract in its header. 4-column
      oracles still load (degrade to row-count-only) so no other checkout
      breaks. **D5 #1 PASSED — all 99 entries match the pinned fixture on
      status and rows**, but only after it caught a row-dropping parse bug:
      psql emits its blank line AFTER the `(N rows)` tally, so a blank line
      inside a block is a data row (a single column holding NULL), and Q23/Q92
      were being counted as 0 rows instead of 1. **D5 #2 PASSED** — Q39 (the
      float case) passes with a matching ck, no `ck=n/a` query misfires, and
      the sweep's five `CKMISMATCH`es are all hand-confirmed real wrong
      answers (Q16/Q94/Q95 `0|NULL|NULL` vs PG's values; Q87 23837 vs 23762;
      Q97 395879 vs 35). Q16 is the proof: M0124-0006 needed a bespoke SF=1
      investigation to find it, the gate now flags it in 24 s. Coverage: **57
      of 95 OK queries carry a checksum, 38 are `ck=n/a`** — the n/a rule is a
      conservative saturation over-approximation (ledger row, resume point
      recorded). **D5 #3 (pre-RC-1b Q75 replay) deferred** — ledger row
      2026-07-29; it needs an old binary + its own cluster load and is a
      hypothesis check, discharged in substance by the five live catches.
      Sweep `analysis/tpcds-sf05-ck-m0124-0005/` (taken under the nightly with
      `FORCE=1`: value verdicts hold, **timings and the TIMEOUT set do not**).
      Original statement of the task follows.
      The gate is row-count only and structurally blind to "right count, wrong
      values" — Q75 PASSed for weeks with 100 rows while its CTE computed
      1,057,469 against PG's 2,368,670, hidden by `LIMIT 100`. Filed as a task,
      not a deferral, because M0125-0002 and M0125-0004 are both accepted at this
      gate and both change which rows reach a join or filter. Extend `oracle.txt`
      from `q|status|rows|secs` to `q|status|rows|ck|secs`, deriving `rows` and
      `ck` from the SAME PG run. **Float normalisation to 12 significant digits is
      mandatory** — ledger `tpcds-round2 stddev-precision` records goopg's
      `stddev_samp` diverging from PG's `sqrt_var` in the last 1–2 digits on 235
      of 236 Q39 rows, so a naive byte checksum flags Q39 immediately. `ck = n/a`
      is a first-class value for a `LIMIT` over a non-total `ORDER BY`; do NOT
      sort-then-hash, which would silently accept a wrong ordering. New
      `CKMISMATCH` verdict kept distinct from `MISMATCH`. **The capture method must change**: `cmd_oracle` derives rows from
      `EXPLAIN (ANALYZE)`, which emits a plan and NO tuples — there is nothing to
      checksum — so switch to a plain execution and *prove* the new row counts
      equal the pinned fixture. Gate-of-the-gate: re-run pre-RC-1b Q75 and record
      the outcome; `CKMISMATCH` is expected, but the evidence covers the CTE
      aggregate, not the `LIMIT 100` window, so a PASS there is a finding about
      the window rather than a broken checksum.
      Design `docs/design/0124-0005-sf05-oracle-checksum-column.md`.
- [x] **M0124-0006 — attribute the 23 value-divergent OK cells of the re-sweep**
      — **DONE 2026-07-29.** Tool `scripts/tpcds-value-diff.py` (graded
      normalisation, design D6a); verdicts in
      `analysis/tpcds-sf1-resweep-20260728/RESULTS.md` §M0124-0006. **No cell is
      ordering-only. 5 of 23 are not defects**: Q7/Q26/Q27/Q83 are the
      exactly-zero-quotient scale renderer (**Q27 newly added** — it was
      unattributed), and **Q39 is float8 accumulation order** (relative 1.4e-16),
      not a collapse — it leaves the M0125-0009 acceptance set. The other 18:
      **M0125-0009** ×10 (Q2 Q21 Q40 Q43 Q50 Q59 Q62 **Q66** Q97 Q99 — Q66 newly
      confirmed via its 48 inner `sum(CASE)` siblings), **M0125-0007** ×3 (Q16
      Q94 Q95), **M0125-0006** ×1 (Q87), and **M0125-0010 ×4 — a NEW defect filed
      this loop** (Q28 Q46 Q68 Q79: `remapSubqueryColumnRefs` binds sibling
      aggregates by function name, `planner.go:2468`). Original text follows.

- [ ] ~~**M0124-0006 — attribute the 21 value-divergent OK cells of the re-sweep**~~
      (raised by M0124-0001 chunk 12; **due before the merged deliverable**). The
      sweep's headline finding — "row counts reproduce set A" — is now known to be
      much weaker than "goopg agrees with PG". Chunks 1–10 were checked on row
      counts only; value diffing began at chunk 11, and back-applying it exposed
      **Q16 wrong since chunk 2** (`OK / 1 row`, goopg `0` vs PG `45`). Restricted
      to cells fresh this sweep, `OK` on both engines, and equal in row count,
      **21 diverge by value and none are ordering-only**:
      `Q2 Q7 Q16 Q21 Q26 Q27 Q28 Q39 Q40 Q43 Q46 Q50 Q59 Q62 Q66 Q68 Q79 Q83 Q87
      Q94 Q95` — **updated by chunk 13 to 23 cells, `+Q97 +Q99`** (both attributed
      to M0125-0009; **Q98 is NOT a member** — its values are correct and its diff
      is rendering-only). Sampling attributes some already — Q87 → M0125-0006;
      Q16/Q94/Q95 → M0125-0007; Q43/Q50/Q66/**Q97**/**Q99** (and probably Q2/Q39)
      → M0125-0009; Q7/Q26/Q83 are
      the answer-neutral numeric-scale gap (no `select_div_scale`) — **chunk 13
      narrowed this one**: goopg's division rscale is correct in general and
      short-circuits **only when the result is exactly zero** (`0.00` vs
      `0.00000000000000000000`; the non-zero quotient is byte-identical), so the
      attribution to look for is a zero fast-path, not a missing rscale rule.
      A second answer-neutral renderer also inflates diffs and must not be
      mistaken for a value divergence: **`char(n)` is not blank-padded**
      (`octet_length` 7 vs PG 30 on `character(30)`; ledger row 2026-07-06,
      M0122-0005) — normalise whitespace per field before classifying — but the rest
      are **unattributed**, and an unattributed value divergence may be a defect
      nobody has filed. Method: `diff <(norm goopg) <(norm pg)` per cell,
      classifying each as (a) an existing filed defect, (b) answer-neutral
      rendering/scale, or (c) NEW — file it. Record the verdict per query in
      `RESULTS.md`. **Do not re-run the sweep for this**; the result files are on
      disk. Note Q97–Q99's files are STALE (set A) and must be excluded until
      chunk 13 runs — excluded is not the same as agreeing, and the same caveat
      applies to any cell whose file predates 2026-07-28.
      Design: fold into `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md`
      (extend D-series with a value-comparison rule) rather than a new doc.

## M0125 — TPC-DS timeout class & planner expression-walker extinction (filed 2026-07-28)

Milestone: `docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md`.
Source: `docs/design/tpcds-round2-fixes/README.md` §13.5 actions **2, 3, 4**.
**Priority #4, after M0124.** M0125-0002/-0004 diff against
`plan_snapshots/tpcds-round2-head.txt` (M0124-0002) and are accepted with
M0124-0005's checksums; M0125-0001 and M0125-0003 stage 1 are unblocked.

**Read before picking any task here.** Two of these move plan shape, and goopg's
planner sits on a *measured* trade-off. Enabling statistics fixed TPC-H Q5 22.8×
(415.2 → 18.2 s) and regressed **Q22 128×, Q4 79×, Q8 53×, Q2 26×, Q12 4.4×**,
taking the serial stream **1162 → 1307 s** (round-4 §2/§5). The cost-driven
join-order planner is **4 wins / 6 regressions / 12 neutral** — Q2 18.8× and Q8
4.1× faster, but Q5 and Q21 hang, Q9 times out, Q10 11.4×, Q18 4.3× — and ships
OFF by default (round-5 §6). **Every regression in that table came with identical
row counts**, so `scripts/tpch-spotcheck.sh` cannot see this class. Plan-shape
commits need a **timed** 22-query TPC-H run plus `make plan-diff
LABEL=tpcds-round2-head`. Round-5's *absolute* seconds are not a valid baseline
(the fix bundle moved the stream 1086 → 325 s with no plan changes) — M0124-0002
arm B is. M0125-0002's gate budget alone is ~12–20 h.

> **⚡ DISCHARGED 2026-07-31 (loop #13) — `M0125-0043` IS DONE.** Its design doc
> `docs/design/0125-0043-smalldimension-name-tag-extinction.md` exists and is
> indexed, and it lists the affected TPC-H query numbers BY MEASUREMENT: the
> nine candidates by text (Q2 Q5 Q7 Q8 Q9 Q10 Q11 Q20 Q21) are confirmed, and
> the set that actually moves is **empty — 0/22, byte-identical plans**. The
> timed 22-query acceptance is 21/22 correct with **Q21 timeout, pre-existing
> and filed as `M0125-0032`** (see the item body). **NEXT SELECTION INSIDE
> M0125 IS `M0125-0041`**, per the standing order below (`-0043` → `-0042` →
> `-0041` → `-0034`'s join-order arm → `-0038` last), since `-0042` landed in
> loop #12. The historical wording of this block follows.
>
> **⚡ AMENDED 2026-07-31 (loop #14) — `M0125-0041` HAS BEEN WORKED; NEXT
> SELECTION IS `M0125-0034`'s join-order arm.** -0041's root cause is fixed and
> equivalence-tested (the scalar pull-up never declined — it died on
> `clonePlanReplacingOuter`'s missing `*CTEScan` arm), but **the item stays
> UNCHECKED**: its acceptance is a completing Q30, and Q30 still TIMEOUTs at
> both 300 s and 1200 s on the C1 Cartesian product that `-0034` owns. Do not
> re-select `-0041` to "finish" it by more decorrelation work — the measurement
> says the remaining factor is join order. Design
> `docs/design/0125-0041-cte-scalar-sublink-decorrelation.md`.
>
> **⚡ SELECT THIS FIRST WITHIN M0125 (added 2026-07-31 by the USER).**
> **`M0125-0043` (benchmark-name hardcoding in `operators_ddl.go` / `open.go`)
> is the top-priority item of this milestone** and outranks every other M0125
> item — including the `M0125-0042` → `-0041` → `-0034` → `-0038` standing order
> recorded in the Current Priority banner, which is hereby amended to run
> **`M0125-0043` first**. Take it on the next selection; resume an already
> in-flight task first, then come here.
> **The design doc is NOT pre-written: the agent that starts `M0125-0043`
> creates `docs/design/0125-0043-smalldimension-name-tag-extinction.md` as part
> of that same loop** (and indexes it in `docs/design/README.md`), per the
> AGENT.md rule that a non-trivial subsystem change lands its design doc in the
> same commit. The doc MUST list the TPC-H query numbers affected by the change.

- [x] **M0125-0043 — remove the hardcoded TPC-H table names from
      `operators_ddl.go` / `open.go` and keep TPC-H correct and in-budget**
      **DONE 2026-07-31 (loop #13).** Design
      `docs/design/0125-0043-smalldimension-name-tag-extinction.md`; new pass
      `internal/planner/small_dimension.go`. Both production writers of
      `catalog.Table.SmallDimension` are GONE — the property is derived from
      relation SIZE at plan-build time and stamped on
      `SeqScan.SmallDim`/`IndexScan.SmallDim` (beside `EstRelRows`, for the
      recorded reason that consumers take only a `Node` and have no catalog in
      scope). Threshold 1024 deliberately equals `smallAnchorRowsThreshold`;
      cold, the never-analyzed 10-page floor puts `region`/`nation` at 170
      estimated rows. **The A/B is the result: a same-cluster `git stash` A/B
      on the loaded SF=1 cluster produced BYTE-IDENTICAL plan snapshots,
      0/22 changed** (`plan_snapshots/m0125-0043-{before,after}.txt`) — a
      POSITIVE result, since the before-arm has the name tag ON, so identical
      plans prove the size derivation reproduces it exactly, Q5's `region` MHJ
      anchor and the Q8/Q21 `shouldAttachBeforeMHJ` CANCEL guard included. The
      nine candidates by text (Q2 Q5 Q7 Q8 Q9 Q10 Q11 Q20 Q21) are confirmed as
      the candidate set; the set that actually moves is EMPTY. Timed 22-query
      acceptance (arm c2, `PER_Q=600`, quiet host,
      `analysis/m0125-0043-tpch-20260731/c2.tsv`): **21/22 ok with correct row
      counts (Q12=2 Q13=35); Q21 timeout.** Q21 is PRE-EXISTING — `timeout` in
      both cold arms at HEAD on 2026-07-30 (c1 305 s, c2 366 s), already filed
      as **`M0125-0032`**, and unreachable by this change on a byte-identical
      plan — but newly established: **at 600 s it STILL times out**, so -0032
      is a shape defect, not a budget crossing near 300 s (ledger row).
      `shouldAttachBeforeMHJ` took a signature change (it now receives the leaf
      scans). One non-obvious sibling path was found by auditing construction
      sites: `unnest.go`'s Index→Seq correlated-probe demotion dropped the tag.
      `catalog.Table.SmallDimension` survives as an explicit hint with NO
      production writer (catalog-only TPC-H fixtures have no heap to measure);
      retiring the field entirely is a ledger row. Three ledger rows 2026-07-31.
      Original wording follows.
      (filed 2026-07-31 by the USER; **highest priority inside M0125**).
      goopg branches production planner behaviour on **literal TPC-H table
      names**. Two sites, and they are the sibling pair that must change
      together (Hard-won Rule #2 — CREATE path ↔ catalog-reload path):
      - `internal/executor/operators_ddl.go:3374-3377` — at `CREATE TABLE`:
        `switch strings.ToLower(s.Name.Name) { case "region", "nation":
        tbl.SmallDimension = true }` (tagged M0054-0010, comment openly says
        "canonical TPC-H tiny tables: region 5 rows, nation 25 rows").
      - `internal/initdb/open.go:2941` — at catalog reload from the pg_class
        heap: `SmallDimension: tr.RelName == "region" || tr.RelName == "nation"`.
      These are the **only** two writers of `catalog.Table.SmallDimension`
      (`internal/catalog/catalog.go:410-418`) in non-test code;
      `internal/initdb/catalog_cache.go:43/89/145` merely persists the flag and
      `internal/testutil/tpch/tpch.go:34` sets it for fixtures. Everything else
      in the repo that names a benchmark table is a **comment only** — verified
      2026-07-31 across `internal/` and `cmd/`: no TPC-DS identifier
      (`store_sales`, `date_dim`, …) reaches executable code at all, and the
      TPC-H names in `bushy.go` / `unnest.go` / `nl_index_join.go` /
      `equiv_class.go` / `joinorder.go` / `operators_analyze.go` are prose
      explaining Q9/Q20/Q21 motivation. **So this one flag is the entire
      benchmark-name-hardcoding surface.**
      **Goal:** the flag must stop being a name lookup. Replace it with a
      name-independent signal (relation size / `ANALYZE` reltuples / block-count
      fallback à la `GOOPG_RELSIZE_FALLBACK`, or retire the flag where the cost
      model already subsumes it — `internal/planner/pathgen.go:44-49` states the
      hash-join build-side orientation "wins automatically", *"retiring the
      SmallDimension name-tag as the primary rule (design ch. 06 §2.1)"*, so
      the cost-model line is a candidate substitute, not a from-scratch design).
      **Acceptance:** with zero benchmark table names left in executable code,
      the full 22-query TPC-H stream runs to completion with **canonical row
      counts and correct values**, no query exceeding a **600 s** timeout.
      **Per the USER: being slower is acceptable as long as nothing times out**
      — this is a correctness/architecture cleanup, not a perf task, so a
      measured slowdown inside budget is an accepted outcome and must be
      recorded rather than worked around.
      **Consumers to audit before touching the writers** (each reads the flag
      and can flip plan shape): `internal/planner/cardinality.go:140/163/186-206`
      (`isSmallDimension`, `IsSmallDimensionSide`), `bushy.go:194/1383-1384`,
      `pushdown.go:305-306`, `local_filters.go:140/175`,
      `equiv_class.go:216/230/253`, `inner_join_qual_pushdown.go:74/310/324`,
      `executor/parallel_hash_build.go:20`.
      **Known hazard, do not rediscover it the hard way:**
      `shouldAttachBeforeMHJ` (`local_filters.go:154-180`) gates on "the FROM
      list contains at least one SmallDimension-flagged table", and its own
      comment records that **without that guard M0077-0001 Slice A regressed
      Q8 / Q21 from PASS to CANCEL**. A naive deletion of the flag therefore
      re-opens a measured regression; the replacement signal must be available
      at the same point in planning, or that gate needs its own substitute.
      Note also that `costDrivenJoinOrder` already short-circuits this gate,
      which is a hint about where the clean answer lives.
      **Design doc — the working agent writes it, it does not exist yet:**
      `docs/design/0125-0043-smalldimension-name-tag-extinction.md`, indexed in
      `docs/design/README.md` in the same commit. **It MUST name the TPC-H query
      numbers affected.** Starting set, from which of the 22 canonical queries
      reference `nation` / `region` at all (`internal/testutil/tpch/tpch.go`
      lines 112-133, checked 2026-07-31): **Q2, Q5, Q7, Q8, Q9, Q10, Q11, Q20,
      Q21** — the other thirteen (Q1, Q3, Q4, Q6, Q12–Q19, Q22) never mention
      either table. The doc must confirm or correct that list **by measurement**
      (a plan A/B), not by inheriting it, and call out Q5 (leans on filtered
      `region` as its MHJ anchor) and Q8 / Q21 (the CANCEL regression above)
      as the three at real risk.
      **Bar:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` +
      `scripts/tpch-spotcheck.sh` (canonical Q12=2 / Q13=35) + `make plan-diff
      LABEL=tpcds-round2-head` + a **timed 22-query TPC-H run** — this is a
      plan-shape change, and per this milestone's preamble every regression in
      the historical table came with *identical row counts*, so the spot-check
      alone cannot see this class. The TPC-DS SF0.5 gate is required too, since
      the flag feeds shared planner code that TPC-DS also traverses. Deferral
      ledger row if any consumer is left on a name-derived signal.

- [x] **M0125-0004 — Q75 join-residual evaluation order** (§13.5 #3). *First: it
      is a live CI break* — `Q75,100,pinned` at `ci/batch/tpcds-row-anchors.csv:46`
      with no `expected-failures.csv` entry, so M-NIGHTLY preempts for it and that
      item IS this task. RC-1b made Q75's `all_sales` CTE exactly correct and
      thereby exposed a pre-existing divergence: goopg evaluates
      `CAST(curr.sales_cnt)/CAST(prev.sales_cnt) < 0.9` as the hash-join residual
      per matched pair **before** the outer Filter's `d_year` equalities exclude
      the `sales_cnt = 0` group, where PG attaches single-relation quals to
      `baserestrictinfo` (`distribute_restrictinfo_to_rels`) and cost-orders the
      rest (`order_qual_clauses`). Fix: the inner-join sibling of
      `pushOuterQualsIntoLaterals` (`internal/planner/pushdown.go:132`), run AFTER
      `remapWithBindings` with positional name validation (RC-1b's lesson),
      **duplicating rather than moving** the conjunct so the result set is
      unchanged by idempotence while the error behaviour changes intentionally,
      placing the `Filter` on the join INPUT never inside the twice-referenced CTE
      body, and scoped to **inner joins over CTE/derived-table inputs only** so it
      cannot re-open the `shouldAttachBeforeMHJ` Q8/Q21 PASS→CANCEL regression.
      Decline on non-INNER joins: PG 18.3 no longer has `check_outerjoin_delay`
      (removed in the PG 16 nullingrels rework) and goopg has no nullingrels model.
      **Blast radius is not zero** — TPC-H Q15 (`q15_main.sql`) joins `supplier` with the
      `revenue0` view, which may expand to a derived table, so a TPC-H plan hunk
      triggers the full timed run; and an empty plan diff is NOT claimed (adding a
      `Filter` is a plan change). Verify by **value**, not row count. Ledger
      `tpcds-round2 Q75-eval-order`. Design
      `docs/design/0125-0004-q75-join-residual-evaluation-order.md`.
      **DONE 2026-07-30.** D1 landed as `internal/planner/inner_join_qual_pushdown.go`
      (`pushSingleSideQualsIntoInnerJoinInputs`), called from `planSelect` after the
      last `applyJoinTreePosMap`. **Q75's SF0.5 output is byte-identical to PG** (100
      rows, `diff` clean — verified by VALUE as required, since `ck=n/a` under a
      saturated `LIMIT`), so the `Q75,100,pinned` anchor now passes on its intended
      meaning and needs no `expected-failures.csv` entry. Blast radius **measured**:
      a 99-query EXPLAIN A/B makes the firing set exactly seven (Q4/Q11/Q31/Q39/Q64/
      Q74/Q75) and the entire delta is added `Filter:` lines on CTE-scan inputs — no
      join reordered, no node kind changed. SF0.5 value gate on those seven:
      MISMATCH=0 CKMISMATCH=0 ERROR=0. **The Q15 concern is discharged by
      measurement, not argument** — `make plan-diff` is 22/22 MATCH *including*
      `Q15a-VIEWBODY`, so the conditional timed TPC-H power run is not triggered.
      Q31/Q64 TIMEOUT and Q4's oracle is itself TIMEOUT, so three of the seven carry
      no value verification; an A/B with the call line disabled reproduced Q31/Q64 at
      332s/333s vs 332s/336s, clearing this change of causing them (ledger row,
      2026-07-30 — re-run on a quiet host). D3 (cost-ordered residual conjuncts),
      `*FuncCall` pushes, and base-relation-leaf scoping are deferred with rows.
- [x] **M0125-0001 — `internal/planner/exprwalk.go` + exhaustiveness gate** (§13.5
      #4, phase 1.1). One `exprChildSlots` child-slot primitive over `plan.go`'s
      **32** concrete `Expr` types (the marker is unexported, so the set is
      closed), three distinctly-named drivers (walk / rewrite-in-place /
      clone-and-rewrite — conflating them compiles and silently drops the rewrite,
      and `remapByPosMap` clones while `remapOuterRefsInSubplan` mutates), and a
      per-caller `scopePolicy` covering the four behaviours real call sites rely on
      (signal / veto / ignore / descend). Three typing traps:
      `MultiAssignSubqElem.Row` is statically `*MultiAssignSubqRow` not `Expr`;
      inner-scope children are `Node` in a different coordinate space (a Semi/Anti
      inner plan must NOT be remapped); and a scope-opening node reports ZERO
      `Expr` slots, so classify by slot kind, never by `len(kids)`. Ship a `go/ast`
      test asserting set equality **in both directions** between `plan.go`'s
      `exprNode()` receivers and the type-switch cases, so a 33rd type is a build
      failure instead of a wrong answer. Add the §2.6 pins never written
      (`bushy_remap_test.go` holds only
      `TestBuildJoinFromDP_NonAscendingSubsetKeyRemap` from `65dd185a`) covering
      all 18 `remapByPosMap` arms plus a double-remap pin. **No call site
      converted**, so no TPC-H run. Note §13.4 item 5's "eleven remain partial" is
      an arithmetic slip — four have been hand-touched, so **seven** is the live
      figure. Design
      `docs/design/0125-0001-exprwalk-driver-and-exhaustiveness-gate.md`.
      **DONE 2026-07-29.** `internal/planner/exprwalk.go` (`exprChildSlots` over all
      32 types + `shallowCloneExpr`; drivers `walkExprRefs` /
      `rewriteExprRefsInPlace` / `cloneExprRefs`; four-value `scopePolicy`), the
      `go/ast` gate, and D6's 26 remap pins. **No call site converted** — `go vet`
      reports all three drivers unused, which is the positive evidence that plan
      shape cannot move, so no TPC-H run. **The gate was proved to FAIL before being
      trusted**, twice: deleting the `*CollateExpr` arm, and declaring a 33rd `Expr`
      type OUTSIDE `plan.go` — the latter validates D5's scan-every-package-file
      requirement, since a `plan.go`-only parse would have passed and the gate would
      have been worthless. Both probes reverted. Four deviations from the draft
      (D1's `slotExprList` collapsed to per-element slots; D3's per-node callbacks
      reduced to a per-call enum; drivers return `bool` because a veto must be
      observable; leaves cloned not shared) are recorded with rationale in the design
      doc's execution record. **Two findings the next loop must not re-derive: (1)**
      `remapByPosMap` is ALREADY complete — 18 arms + 14 childless leaves = 32 —
      confirming M0125-0002's re-base as a genuine no-op step with no absent arms;
      **(2) M0125-0002's walker inventory is WRONG.** `walkExprTree`
      (`unnest.go:1152`) is a further generic `Expr` walker with the same fail-open
      `default:`, outside §2.4's seven (already nine after M0124-0003). §0 says
      **fourteen**, §13.4 says seven, and neither figure has been re-derived from
      source — do that FIRST or M0125-0002 closes against a list that was never
      right. Two ledger rows appended (`exprwalk-node-side`; the walker-inventory
      correction).
- [ ] **M0125-0003 — `GOOPG_RELSIZE_FALLBACK` relation-size fallback** (§13.5 #2,
      phase 6.1). **STAGE 1 LANDED 2026-07-29, flag-off and inert** — the
      arithmetic (`estimateRelSize`), the `reltuples < 0` sentinel
      (`catalog.TableStats.Analyzed`), the live block count
      (`InMemory.RelationBlocks`, installed from the pool in `initdb.Open`), the
      staged knob, and the stage-1 consumer (`seqScanRows` via
      `SeqScan.EstRelRows`). Verified against PG 18.3 on four measured
      relations and reproduced EXACTLY; see the design doc's "Implementation
      record" and five ledger rows dated 2026-07-29.
      **STAGE 2 LANDED 2026-07-30, also default-off** — `bushySeedRowCounts`
      (`internal/planner/bushy.go`, extracted from `enumerateBushyPlans`) adds
      the fallback as a third tier under the DP's singleton seed, through the
      new single gated entry point `relSizeFallbackRows(stage, cat, tbl)` that
      `stage1RelSizeRows` now delegates to as well. Verified by plan SHAPE in
      BOTH flag states, which is contention-immune: flag-off `make plan-diff
      LABEL=tpcds-round2-head` = **22/22 MATCH**, `GOOPG_RELSIZE_FALLBACK=2` =
      **22/22 DIFFER** (`analysis/m0125-0003-stage2/plan-diff-stage2-on.txt`).
      The magnitude is the finding: an S-cold server used to seed **`rows=1` for
      every relation**, so the DP ranked join orders on no cardinality signal at
      all; it now seeds block-derived sizes within 0.37–1.01× of SF=1 truth
      (`nation` at 20.8× is the 10-page floor, upstream's behavior). Q9 newly
      reaches `Gather`/`Workers Planned: 4` — untimed, and round-4's five
      regressed queries remain the watch list.
      **STAGE 2's TIMED C-ARMS ARE READ, 2026-07-30, on a verified quiet host**
      — `analysis/tpch-relsize-fallback-20260730.md`, harness
      `scripts/tpch-relsize-arm.sh` (new; §D7's first implementation, and the
      only shape that *can* express an arm, because the flag is read once from
      the SERVER's env in `planner.init()` — there is no GUC). 21 comparable
      TPC-H queries **693.8 s → 494.0 s, −28.8 % (1.40×), four wins (Q9 3.29×,
      Q12 3.43×, Q10 2.58×, Q7 1.32×), ZERO regressions**, row counts identical
      in both arms, one binary (`5b87cf4b53780639`) across all 45 executions.
      **§D5.2's pre-registration was wrong in both directions:** none of round
      4's five regressed, **Q12 — round 4's 4.4× LOSS — is the second-largest
      win**, and **Q5, the named expected win, did not move** (0.99×; M0077 had
      already fixed it, so this cluster runs it at 66.7 s cold, not 415 s).
      §D5.2's qualification 1 is the operative fact — round 4 supplied
      selectivity AND sizes, this supplies sizes only — so **§D5.3's risk
      statement is refuted for stage 2 on this workload** and is NOT
      transferable to stage 3. Three things the arm could not close, each with a
      ledger row: **Q21 TIMEOUTs in BOTH arms** (300 s and 600 s caps, 14.2–14.8
      GB VmHWM) and **does not honour cancellation** — round-5 §6's non-cancelling
      defect on the *default* planner, not just the cost-driven one; **W1/W2 are
      unconstructible**, measured by the harness's new `probe-analyze` mode
      (`ANALYZE` in db `tpch` errors *relation does not exist*, and stats are
      per-connection while `cmd/tpch-runner` opens one connection per query), so
      §D3's W1 = W2 invariant stays unmeasured; and the absolute seconds carry a
      `MEM_HIGH` below the true working set, so they are not cross-report
      comparable (the A/B is, both arms sharing it).
      **§D8's TPC-DS ARM IS READ TOO, 2026-07-30, quiet host** —
      `analysis/m0125-0003-sf05-relsize-20260730/`, the full 99-query SF0.5 gate
      at `GOOPG_RELSIZE_FALLBACK=2`, four contiguous chunks on ONE binary and one
      `engine-id`. **`PASS=82 (50 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=13 SKIP=4` against the off arm's `PASS=79 … TIMEOUT=16`** — the
      timeout class this milestone is named after shrinks by **4 of 16**, and
      **every one of the 78 queries that pass in both arms agrees on row count
      AND value checksum**: a suite-wide join-order change that altered no
      answer, which is the statement M0124-0005's checksum column was added to
      make. Common-PASS wall time **2273 s → 1845 s (−18.8 %)**, 27 of the 28
      queries that move by ≥5 s or ≥1.25× faster (Q43 11×, Q52 8×, Q40 6.3×,
      Q88 2.11×), one slower (Q21 1.74×). Rescues: **Q10 40 s, Q69 17 s,
      Q67 157 s, Q47 277 s** — Q47 also closes the *runtime* half of M0125-0013
      at SF0.5. The off arm was **reused, not re-run**, licensed by an empty
      `git diff e29faca9..HEAD -- '*.go'` plus the identical D4a `engine-id`
      empty-diff digest in both reports; the gate now also prints
      `# planner-flags:` (every flag, even unset) so an arm is a positive
      statement in the artefact instead of the operator's memory.
      **Both of §D8's pre-registered signals are REFUTED, one backwards:**
      Q72 did not "resolve" — it was already passing at 276 s and the flag makes
      it **1.13× slower** (900 s probe: off `PASS 270 s`, on `PASS 305 s`, 100
      rows both), so its `PASS → TIMEOUT` is a **budget crossing of a marginal
      query, not a hang** (0124-0001 §D6's class; a TIMEOUT cell may never be
      read as "unbounded" without a second budget); and **Q35 — this task's own
      acceptance query per M0124-0004 — still times out at 300 s with the flag
      on. The relation-size fallback is NOT what Q35 was waiting for**, and its
      RC-8 per-`EXISTS` re-scan class now needs its own task, to be filed off
      M0125-0026's classification pass (ledger row). Two ledger rows carry
      Q72's unexplained 13 % and Q35's non-fix; a third records that the TPC-H
      harnesses still stamp no planner flags.
      **Still owed, which is why this box is unchecked:** stage 3
      (`estimateBaseRelInfo.baseRows`, `cardinality.go:139`) and the W arms. The
      flag accepts `3` today and yields stage-2 behavior. **`M0125-0005`'s
      evidence is now COMPLETE in both benchmark families and both recommend the
      flip** — it is the next selection, and its commit must name Q72's 1.13× as
      a known cost rather than claim "no regressions".
      **Read stage 2's timed arm BEFORE stage 3 lands**:
      stage 3 makes `filteredRows` positive cold and therefore SHADOWS the
      stage-2 tier at the DP seed (recorded in `bushySeedRowCounts`'s doc
      comment and a ledger row). A fourth, unstaged consumer was discovered —
      `reorderCommaFromByCardinality` (`joinorder.go:89-93`) bails out entirely
      when any table lacks `Stats.RowCount`, so the greedy comma-FROM reorder is
      still blind at S-cold; own ledger row, deliberately not folded in. En route it corrected `typeWidth`, which was not
      `get_typavgwidth` (missing the UTF8 encoding factor AND the whole sliding
      scale: `varchar(20)` read 24 vs PG's 58) — a 2.4× width error is a 2.4×
      row-estimate error on the char/varchar-heavy schemas this serves.
      §13.5's highest-value item (15–16 of 21 defects); stage 1 is
      inert, so it lands early. `tableRows` (`cardinality.go:89`) returns
      `Stats.RowCount`, which `loadStatisticsFromHeap` (`initdb/open.go:3454` —
      §7.1's `:3433` is stale) leaves 0 after every restart. Model on
      **`table_block_relation_estimate_size`** (`postgres/src/backend/access/table/
      tableam.c`, reached via `heapam_estimate_rel_size`) — NOT `plancat.c`'s index
      branch: density = `(usable_bytes_per_page * fillfactor / 100) / tuple_width`
      then `clamp_row_est`; `curpages = 10` only when `curpages < 10 && reltuples <
      0 && !relhassubclass`; `curpages == 0 ⇒ tuples = 0`. goopg has no "never
      analyzed" sentinel, so decide and document the empty-analyzed-table trigger.
      Reuse `ParallelSettings.BlocksForTable`; no package global. **Staged by
      consumer** (1 = probe-side, shape-neutral; 2 = + DP seed, where round-4's
      regressions live; 3 = + `baseRows`), because one flag switching all three
      gives one number and no attribution. Stages are **not** blocked on M0125-0002 (see the
      directive above; the walker is gate-shadowed at that site after all). **§7.1's mitigation is unexecutable as written**:
      every TPC-H run in this repo ANALYZEs first, so the fallback provably cannot
      fire and both arms are identical — "no difference" would mean "not
      exercised". Measure four arms {no-ANALYZE, ANALYZE} × {off, on} per stage;
      only no-ANALYZE-on is interesting. Pre-register round-4's five regressed
      queries as the watch list and Q5 as the expected win; make no quantitative
      prediction, since round 4 supplied full selectivity while this supplies only
      sizes — a third regime nobody has measured. Use round-5 §6's per-query
      isolated harness: a mis-ordered star query was measured NOT to honour
      cancellation (server pinned ~10 GB RSS), so a plain sweep can wedge the host.
      Never measure together with `costDrivenJoinOrder`. Note `pg_class.reltuples`
      reads `Stats.RowCount` directly (`internal/catalog/catalog.go:6946`) and
      CANNOT be fixed here. Phase 6.2 out of scope (B3: does not fix Q64 alone).
      Design `docs/design/0125-0003-relsize-fallback-and-tpch-stats-tradeoff.md`.
- [ ] **M0125-0002 — convert the seven remaining walkers, one per commit** (§13.5
      #4, phase 2.2).
      **STEP 0 DONE 2026-07-30 — the inventory is re-derived from source and now
      gate-pinned**, which M0125-0001's execution record required before any
      conversion ("§0 says fourteen, §13.4 says seven, and neither figure has
      been re-derived from source — do that FIRST"). All three figures were
      wrong: `internal/planner/exprwalk_inventory_test.go` censuses every type
      switch over an `Expr` in package `planner` and finds **64 sites — 2
      exprwalk primitives, 50 recursive-and-incomplete walkers (the RC-1a
      class, 2–25 of 32 arms), 12 non-recursive classifiers**. The seven were
      selected by blast radius, which scopes a conversion soundly and sizes a
      defect class not at all; this task's scope is UNCHANGED (same eight
      commits, same order) but it covers **8 of 50 = 16 %**, and each
      conversion now closes by DELETING its pin (set equality is enforced in
      both directions, so a new hand-written walker fails the build and a
      converted one fails until the pin goes). Gate proved to fail both ways
      before being trusted. Arm counts are comments, not assertions — pinning
      them would make every band-aid arm look like progress. The census also
      found **two fail-opens that COLLIDE rather than no-op** (worse: a wrong
      answer, not a missed optimisation) — filed as `M0125-0024`, deliberately
      not fixed here. Design doc §"STEP 0".
      **COMMIT 1 of 8 DONE 2026-07-30 — `remapByPosMap` re-based onto
      `rewriteExprRefsInPlace`, and D2 row 1's empty-plan-diff prediction
      HELD: 22/22 TPC-H MATCH under `make plan-diff
      LABEL=tpcds-round2-head MODE=structural`.** Three things the design
      left open were resolved by reading the pins rather than the prose:
      (a) the driver is **`rewriteExprRefsInPlace`, not `cloneExprRefs`** —
      the §2.6 pin comment guessed the latter, but
      `TestRemapByPosMap_IdentityMapSharesNodes` requires an identity remap
      to leave the node SHARED and a whole-tree clone replaces even the
      root; (b) D3's "plan slots **ignore**" taken literally would have
      **dropped the `remapOuterRefsInSubplan` calls** and reintroduced
      TPC-H Q21's wrong-outer-column defect — the walker has TWO kinds of
      inner plan needing OPPOSITE treatment (`InExpr.Plan` already remapped
      vs `Exists`/`Subquery`/`ArraySubquery`/`MultiAssignSubq*` needing
      Level-1 translation) and a per-driver `scopePolicy` cannot express a
      per-type split, so `scopeIgnore` + a bottom-up `Rewrite` dispatch
      owns it (`scopeDescend`+`OnScope` cannot: `OnScope` gets the `Node`
      with no parent context); (c) the missing `default:` is a **panic**,
      matching PG's `elog(ERROR, "unrecognized node type: %d")` in
      `nodeFuncs.c:2667`/`:3743` — ledger row 2026-07-30 records the
      un-PG-faithful half (a bare panic instead of `ereport` with a
      SQLSTATE, reaching the client as `XX000` only via `server.go`'s one
      `recover()`). **The census pin DEMOTED (`walkerPending` →
      `nonRecursiveClassifier`) instead of being deleted**, which corrects
      this item's own "each conversion closes by DELETING its pin": the
      census keys a switch by its ENCLOSING FUNCTION and closures count, so
      the six-arm dispatch inside `Rewrite` keeps the site in the census.
      Deletion is the audit signal only for walkers whose switch vanishes
      entirely; forcing one here would mean an `if`-chain of type
      assertions (gaming the gate) or a renamed helper (same switch, new
      key). Four new pins in `remap_arms_test.go`: subplan `Args` are
      same-scope (nothing pinned it, and the driver reaches them through
      slots), containers are not cloned, and an unenumerated type panics at
      the root and nested. **D4 item 4's SF0.5 arm is OWED, not run** —
      ledger row 2026-07-30: the `ci/batch` nightly held the host (load
      ~10, TPC-DS stage mid-flight at ~11 GB RSS on 65435) and a concurrent
      99-query sweep would have risked the memory guard SIGKILLing that
      stage; it must run before commit 2. **↳ THAT ARM IS DISCHARGED, and
      not by re-running it: M0125-0003 §D8's 99-query SF0.5 sweep ran at
      HEAD `e29faca9`, which CONTAINS commit 1 (`da6d2c0c`), with 0
      MISMATCH/CKMISMATCH/ERROR.**
      **COMMIT 2 of 8 DONE 2026-07-30 — `cloneExprShiftIdx` re-based onto
      `cloneExprRefs`, and D2 row 2's "it does move plans" is REFUTED by
      measurement** (`analysis/m0125-0002-c2-sf05-plans-20260730/`, quiet
      host). TPC-H **22/22 MATCH in `MODE=strict-text`** — byte-identical,
      not merely structural — and TPC-DS SF0.5 **96/96 byte-identical
      `EXPLAIN`**. The 20 newly admitted kinds (`*IsNullExpr` — the RC-1a
      shape itself — `*CaseExpr`, `*RowExpr`, `*IsBoolExpr`,
      `*ExtractExpr`, `*CollateExpr`, `*IsDistinctFromExpr`, a Plan-less
      `*InExpr`, and the row-independent leaves) are common in TPC-DS text
      and evidently never reach THIS site: the conjuncts arriving on an
      inner `Filter{SeqScan}` at a Semi/Anti/Inner join were already inside
      the old 12-arm set on both benchmarks. **The SF0.5 answer sweep ran
      anyway and had to**, because the plan gate is blind to the one thing
      the conversion changed on the queries it does touch: the old arms
      REBUILT `*BinaryOp`/`*UnaryOp`/`*FuncCall` from stale field lists and
      **dropped `BinaryOp.ResultType`, `FuncCall.Variadic` and
      `FuncCall.ReturnType` on every hoisted conjunct** — a silent
      type-metadata loss that `EXPLAIN` renders identically because it
      prints predicates by name. 99 cells: **PASS 83 / TIMEOUT 12 /
      MISMATCH 0 / CKMISMATCH 0 / ERROR 0**, all 50 checksums equal to the
      `m0125-0003-sf05-relsize-20260730` baseline. **The one differing cell,
      `Q72 TIMEOUT 307 s → PASS 313 s`, is a cap flap and MUST NOT be cited
      as a rescue** — the newer run is SLOWER, still over the 300 s cap, and
      Q72's plan is one of the 96 byte-identical ones. Q72 stays
      M0125-0005's unexplained 1.13×. **Completeness ≠ admission:**
      `exprChildSlots` reports `*OuterColumnRef` and `*CTIDExpr` as
      childless leaves (correct), so a conversion driven only by "the
      primitive knows this type" would have ADMITTED both — the first is a
      correlation above the join, the second reads the OUTER side's ctid
      because `seqScanOp` injects block/offset into the scanned row's slot.
      Both vetoed explicitly; the veto pin was proved to fail with the arm
      removed. Census pin DEMOTED (RC-1a 48 → 47) for commit 1's reason.
      **Two deliberate D4 deviations, both with ledger rows: the timed
      22-query TPC-H run was NOT executed** (byte-identical plans ⇒ any
      number would be host noise; it becomes mandatory again at the first
      commit with a non-empty plan diff) **and `LABEL=tpcds-round2-head` was
      retargeted to `m0125-0005-relsize-default-stage2`** — the former
      predates the relsize default flip, which moves 22/22 TPC-H plans by
      itself. **Every remaining commit in this series must use the
      retargeted label.** A ledger row also records that PG does NOT decline
      the shapes goopg vetoes: it parameterises them via nestloop params
      (`paramassign.c: assign_nestloop_param_var`). **Fixed in passing:
      every SF0.5 report captured after M0125-0005 said
      `GOOPG_RELSIZE_FALLBACK=unset(off)` when unset now means stage 2 — the
      M0125-0011 defect class in labelling form; now `unset(2)`.**
      **Next in THIS task is commit 3 (`visitColumnRefs`) — but per the USER
      DIRECTIVE 2026-07-30(b) the next SELECTION is `M0125-0028`, not commit 3.**
      When commit 3 is taken it must use
      `LABEL=m0125-0005-relsize-default-stage2` (never `tpcds-round2-head`,
      which predates the relsize flip, and never bare `plan-gate`, which picks
      newest-by-mtime), and it carries the timed TPC-H run the moment its plan
      diff is non-empty — commit 2 excused that run only because its diff was
      byte-empty.
      Original scope follows. `visitColumnRefsForTable` (`bushy.go:415`),
      `visitColumnRefsByName` (`:1653`), `visitColumnRefs` (`:2932`),
      `conjunctIsLocalEligible` (`local_filters.go:89`), `localizeExprToLeaf`
      (`:268`), `cloneExprShiftIdx` (`nl_index_join.go:777`), `exprSide`
      (`planner.go:5059`) — plus re-basing `remapByPosMap` and giving it the
      `default:` it still lacks. **This is a plan-SHAPE change**: `extraInScans`
      (`bushy.go:1625`) starts `allMatched := true` and only falsifies it from
      inside the callback, so a conjunct of unenumerated kinds is admitted into
      `MultiHashJoin.Filters` **by accident** — completing the walker *removes*
      predicates. TPC-H blast radius is **{Q2, Q5, Q7, Q8, Q9}** (≥5 FROM items referencing
      `region`/`nation`, so they pass `shouldAttachBeforeMHJ`, whose comment records
      "Without the SmallDim guard, Slice A regresses Q8 / Q21 from PASS to
      CANCEL"). Order: `remapByPosMap` re-base FIRST (the only genuinely
      no-op step, pinned by 0125-0001's 18-arm table) → `cloneExprShiftIdx` →
      `visitColumnRefs` → `visitColumnRefsForTable` → `exprSide` →
      `conjunctIsLocalEligible`+`localizeExprToLeaf` (ONE commit — producer/
      consumer pair) → `visitColumnRefsByName` last. **Only commit 1 carries an
      empty-diff expectation** — `cloneExprShiftIdx` is a fail-closed admission
      test whose completion OPENS the NLI inner-unwrap, `visitColumnRefs`
      rewrites join-predicate indices, and `visitColumnRefsForTable` feeds
      `tableForCol` and hence local-filter partitioning AND join-edge
      classification. Commits 2–8 carry the full timed run. Per commit: units +
      `plan-diff LABEL=tpcds-round2-head` + timed 22-query TPC-H + SF0.5 with
      checksums on first/last/any-hunk commit; revert rather than fix forward.
      Do NOT claim "the walker class is extinct" — `walkColumnRefsImpl` and the
      `shiftColumnRefs` closure stay out of scope with a ledger row. Design
      `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md`.
- [x] **M0125-0024 — two expression-identity fail-opens that COLLIDE**
      **DONE 2026-07-30.** Both fixed on one new driver. `exprwalk.go` gained a
      third complete-over-32-types primitive (`exprSelfKey` — the node's OWN
      identity-bearing fields) and a fourth driver over `exprChildSlots`
      (`exprIdentityKey(e, pol)`). It returns a **decidability flag, not a
      bool**, because the two callers translate "undecidable" in **opposite**
      directions, and that asymmetry is the whole safety argument:
      `planExprContentKey` keys an unkeyable node **per pointer** so it can
      never share an aggregate state slot, while `exprEqual` returns **not
      equal** so it can never assert two expressions are one (a false negative
      there is at worst a diagnosable `42P10`). Both functions lost their own
      type switches, so **both census pins are DELETED, not demoted** — unlike
      commit 1's `remapByPosMap`, no per-type dispatch survives (RC-1a class
      50 → 48, census 64 → 63).
      Three findings the filing did not have: **(1)** the collision is reachable
      from ordinary SQL, because `*BinaryOp` was one of the 28 unenumerated
      types — `ua(a + b)` and `ua(a - b)` shared a slot. **(2)** the `%T%v`
      fallback in `exprEqual` was wrong in **both** directions, not one: it
      printed nested pointers as ADDRESSES (structurally equal expressions read
      unequal) *and* printed `pos` (the same literal at another offset read
      unequal), where PG's `equal()` excludes location outright
      (`COMPARE_LOCATION_FIELD` is a no-op in `equalfuncs.c`). **(3)** the
      `*ColumnRef` divergence resolves toward `exprEqual`, i.e. **`Index`
      alone** — `SchemaColumn.SourceTableIdx` documents zero as "unknown /
      derived" (`plan.go:27-37`), so it is auxiliary disambiguation metadata
      with a hole rather than a `varno`, and both callers compare expressions
      resolved against the SAME coordinate space where `Index` already IS the
      identity. Including it could only split one column into two.
      Gates: every pin proved to FAIL at `da6d2c0c` and pass after (including
      the value pin, where both calls got `SharedStateSlot=0`); units suite;
      `plan-diff LABEL=tpcds-round2-head MODE=structural` **22/22 MATCH** (the
      laxer `pathKeyEqual` moved no plan); pgbench smoke via the hook. Value
      gate is `internal/planner/agg_state_sharing_test.go`, plus
      `TestExprIdentitySiblingsAgree` — the pair invariant that makes a future
      divergence a test failure. Two ledger rows own what is left: the executor
      half of the value gate (a real `CREATE AGGREGATE` driving the
      leader/follower state copy) and `exprIdentityKey` still ACCEPTING
      `scopeIgnore`, which is a wrong-answer policy for an identity question.
      **↳ THE EXECUTOR HALF IS CLOSED (2026-07-30, next loop) and it upgraded
      the verdict on this fix — BOTH directions are user-visible.** New file
      `internal/executor/agg_state_sharing_value_test.go` (design §5.1) drives
      the leader/follower copy end to end through a real `CREATE AGGREGATE`
      over a real plpgsql sfunc. The fixture the ledger row budgeted for was
      not needed: `executeSFuncCall` already falls back to
      `executeStoredRoutine` and `RAISE NOTICE` reaches `ctx.Notices`, so the
      test **counts sfunc invocations** (3 shared / 6 unshared over 3 rows)
      rather than inferring them, which pins the M0097-0035 sharing
      optimisation from the other side too. At `da6d2c0c`
      `SELECT ua_sum(a+b), ua_sum(a-b)` returned **`(77, 77)`** where PG
      returns `(77, -63)` — one row of plausible numbers with column 2 echoing
      column 1, the exact shape no row-count gate in this programme could see.
      **The same file discharges the OTHER owed half, and it was not a
      no-op:** goopg at `da6d2c0c` **rejected** `SELECT DISTINCT ON (CASE …) …
      ORDER BY CASE …` with `42P10`, a statement PG 18.3 accepts (verified
      against the oracle on 65438 — three rows, identical to HEAD). So the
      laxer `exprEqual` removed a **real spurious error**: this was a
      wrong-ERROR fix as well as a wrong-answer fix. That ledger row is
      flipped to `resolved`; only the `scopeIgnore` row remains. One NEW ledger
      row came out of reading that path: an sfunc that raises is never
      propagated — `executeSFuncCall` discards each candidate's error and
      reports `42883 … does not exist`, and the DISTINCT leader pre-compute
      (`operators_join_agg.go:1754`) drops the error and keeps the stale state,
      i.e. a silently wrong number where PG aborts the statement.
      Original filing follows.
      (discovered by M0125-0002's STEP 0 census, 2026-07-30; ledger row same
      date). Both are outside the seven, and both are wrong-answer risks rather
      than missed optimisations, because they under-enumerate an *identity*
      function: unenumerated types are not skipped, they are **conflated**.
      **(a) `planExprContentKey` (`internal/planner/planner.go:7027`, 4 of 32
      arms)** keys aggregate STATE-SHARING equality and its `default:` returns
      `fmt.Sprintf("%T", e)` — the type name alone — so any two distinct
      expressions of one unenumerated type (two different `*CaseExpr`s, two
      `*CastExpr`s, two `*SubqueryExpr`s) share a key and are treated as the
      same aggregate argument. This is M0097-0032's shape, where a dropped
      FILTER collapsed `count(*) FILTER (WHERE …)` onto `count(*)` and the
      filtered count reported the unfiltered total — a shipped wrong answer.
      **(b) `exprEqual` (`:11950`, 5 of 32 arms)** backs DISTINCT ON / ORDER BY
      matching and falls back to `fmt.Sprintf("%T%v", …)` comparison, which its
      own comment concedes is "pointer-safe only for primitives": for an
      unenumerated type holding pointers it compares ADDRESSES, so structurally
      equal expressions read unequal. Independently, (b)'s `*ColumnRef` arm
      compares only `Index` while (a)'s compares `SourceTableIdx/Index`, so two
      refs from different source tables at the same index are equal to one and
      distinct to the other — the sibling-divergence class in a pair nobody had
      noticed was a pair. Both are `walkerPending` pins in
      `exprwalk_inventory_test.go`. Fix each on `exprwalk.go`'s drivers with a
      stated `scopePolicy`, and gate by VALUE (M0124-0005 checksums) not row
      count: an aggregate-sharing or DISTINCT change is invisible to a
      row-count gate. Needs a design doc
      (`docs/design/0125-0024-expression-identity-collisions.md`) before code —
      the aggregate-sharing path is where the shipped-wrong-answer precedent
      lives.
- [x] **M0125-0025 — a raising aggregate support function must abort the
      statement** (M0125-0024's third ledger row, 2026-07-30). **DONE
      2026-07-30.** Selected because every other open M0125 item needs a TIMED
      run on a quiet host and `ci/batch/run-nightly.sh` (PID 3541516) had held
      it for 5h33m; this one is accepted purely BY VALUE. The row claimed a code
      path rather than an observed wrong answer — it is an observed wrong
      answer. A user-defined aggregate whose `SFUNC`/`COMBINEFUNC`/`FINALFUNC`
      **raised** had its error discarded and the query answered anyway: at
      `0de1b404` `SELECT p_rsum(a) FROM raise_t` returned **`1`** (the sum of
      the rows transitioned before the raise) where PG 18.3 aborts
      `ERROR: boom 2`; the shared-DISTINCT-slot form returned `(1, 1)`; a
      raising `FINALFUNC` returned `NULL`; a raising `COMBINEFUNC` returned the
      un-combined partial state `6`. Right shape, right type, plausible number —
      M0125-0024's class, invisible to every row-count gate here. **Two
      independent swallows:** `executeSFuncCall` discarded each candidate's
      error (`_ = rerr`, twice) and synthesised `42883 … does not exist`, so a
      PRESENT routine that FAILED was reported MISSING — which is what hid the
      second swallow — and then all seven call sites read
      `if serr == nil { state = newState }`. The three transition loops
      (`applyAgg`, `finishAgg`'s DISTINCT dedup, `Open`'s leader/follower
      pre-compute) are separate code and each swallowed independently. **A
      blanket propagate would have been wrong** and that is the design's one
      real decision: `executeSFuncCall` doubles as the lookup for the built-in
      sfuncs it models inline and is called for slots an aggregate never
      declared, so its `42883` is a NORMAL outcome. The two modes became
      decidable and propagate in OPPOSITE directions — `errSFuncNotFound` →
      swallow, `sfuncRaised()` → propagate — the same shape M0125-0024's
      `exprIdentityKey` needed. `finishAgg` gained `(Datum, error)` by lifting
      its built-in tail verbatim into `finishBuiltinAgg` (no re-indentation,
      ~103 returns untouched). Gates: 7 new subtests in
      `internal/executor/agg_sfunc_error_propagation_test.go`, every `want`
      MEASURED against the PG 18.3 oracle on 65438 inside a rolled-back
      transaction (reference DB verified byte-unchanged); `P0001` matches PG's
      SQLSTATE exactly; `TestMissingSFuncStillFallsThrough` pins the half that
      must not change. Two gaps deferred with ledger rows, one found BY the
      gate: the two `windowOp` sites are plumbed but **unreachable** (the v0
      analyzer rejects a user-defined aggregate in `OVER (...)` with `0A000`
      before the executor is involved, though PG accepts it), and `applyAgg`'s
      `evalExprSlot` calls for `Arg2`/`ExtraArgs` swallow identically, calling
      the sfunc with FEWER arguments than declared. Design
      `docs/design/0125-0025-sfunc-error-propagation.md`.
- [x] **M0125-0005 — flip the `GOOPG_RELSIZE_FALLBACK` default** (§13.5 #2 rider).
      **DONE 2026-07-30 — FLIPPED. The default is stage 2**
      (`internal/planner/relsize.go`, `defaultRelSizeFallbackStage`); design
      `docs/design/0125-0005-relsize-fallback-default-flip.md` Execution
      section, written decision
      `analysis/m0125-0005-spotcheck-20260730/README.md`. It flipped because
      §D5.3's regression prediction is REFUTED: **none of round 4's five
      pre-registered queries regressed** and Q12, its 4.4× loss, is a 3.4× win.
      This task's own owed measurement — **`tpch-spotcheck.sh` wall clock AND
      peak RSS in both states** — is discharged: two alternating runs per arm
      plus one post-flip confirmation, **75.0 s → 30.9 s (2.43×)**, Q12
      62.38 → 19.61 s, Q13 unmoved, **`Q12=2 / Q13=35` in all five runs**.
      **Peak RSS is unchanged, reported as "indistinguishable" not "improved"**
      — the off arm's own two readings differ by 1125 MB, more than any
      off-vs-on gap; the on arm is reproducible to 3 MB. The gate now reports
      its own cost (`planner-flags:` line + query-phase wall clock + cgroup v2
      `memory.peak`). **The cost is carried, not buried: TPC-DS Q72 is 1.13×
      slower (270 → 305 s), crosses the 300 s cap, and is unexplained — this
      flip is NOT "no regressions"** — and **Q35, the acceptance query, still
      times out**, so the flip is not sold as fixing it. Stage 3 is NOT folded
      in (§I8 shadowing). Plan baseline re-pinned:
      `plan_snapshots/m0125-0005-relsize-default-stage2.txt`, because the flip
      moves **22/22** TPC-H plans (16 estimate-only, 6 structural — Q7 Q9 Q10
      Q11 Q12 Q21) and `plan-gate` reads the newest snapshot, so the next
      planner commit would have seen the flip as a regression; post-flip
      plan-gate is 22/22 MATCH. Two ledger rows appended (the SF=1-for-SF0.5
      substitution; **phase 6.2's greedy-join-order row, which this task was
      told to update and which had never been written**), and the RC-5 row's
      resume point now records its second precondition as satisfied. Original
      wording follows.
      Separate commit, separate decision, so §7.3 RC-5's reopen criterion ("after
      the flag defaults on") has an owner. Requires: the C1→C2 table for every
      stage with the pre-registered watch list checked; a TPC-DS SF=1 sweep at both
      flag states; `tpch-spotcheck.sh` re-measured for wall clock **and peak RSS**
      in both states (it runs S-cold and Q12 is one of the regressed cells, so a
      careless flip degrades the gate every future commit must pass); and a written
      decision. **"Measured, and deliberately not flipped" is a successful
      completion** — `costDrivenJoinOrder` is the precedent. On landing, update the
      RC-5 and phase-6.2 ledger rows whose criteria this satisfies. Design
      `docs/design/0125-0005-relsize-fallback-default-flip.md`.
      **↳ INPUT AVAILABLE 2026-07-30 (`analysis/tpch-relsize-fallback-20260730.md`
      §6): the TPC-H half of this task's evidence is DONE for stage 2 and it
      recommends the flip** — 21 comparable queries 693.8 s → 494.0 s (1.40×),
      four wins to 3.4×, **zero regressions**, identical rows; none of round 4's
      five regressed and Q12 (its 4.4× loss) is a 3.4× win. Three riders the
      report states and this task must honour: **(1)** ~~the TPC-DS side is NOT
      done~~ — **DONE 2026-07-30, and it RECOMMENDS THE FLIP**
      (`analysis/m0125-0003-sf05-relsize-20260730/`): SF0.5 timeout class
      **16 → 13**, `PASS` 79 → 82, **zero** MISMATCH/CKMISMATCH/ERROR, all 78
      common PASSes agreeing on rows **and** value checksum, common-PASS wall
      time −18.8 %. Four rescues (Q10 40 s, Q69 17 s, Q67 157 s, Q47 277 s)
      against **one measured cost this task must carry into its commit message
      and NOT describe as "no regressions": Q72 is 1.13× slower (270 s → 305 s
      at a 900 s budget) and crosses the 300 s cap.** Two of §D8's predictions
      are refuted — Q72 was already passing, and **Q35, this task's acceptance
      query, still times out**, so the flip must not be sold as fixing it; **(2)** the report covers **stage 2 only** — stage 3 is
      unimplemented and §I8 records that it *shadows* this tier, so its own arms
      are required; **(3)** the spotcheck re-measurement above is still owed, and
      the good news is that Q12, one of its two queries, is a 3.4× win cold.
      Also inherited, arm-independent: **TPC-H Q21 TIMEOUTs at S-cold on today's
      default** (>600 s, 14.2–14.8 GB VmHWM) **and does not honour
      cancellation** — pre-existing, not caused by the flag, own ledger row; do
      not let the flip be blamed for it, and do not let it block the flip.
- [x] **M0125-0006 — set-operation chains re-associate right when branches are
      parenthesised** (discovered by M0124-0001 chunk 11, ledger row 2026-07-28).
      **A wrong-answer defect, not a performance one**, and the first one this
      programme found by value rather than by row count: TPC-DS Q87 returns
      `47218` against PG's `47049` while both return exactly 1 row, so the SF0.5
      oracle, the nightly row anchors and the harness's own comparison are all
      structurally blind to it. SQL-standard and PG associate equal-precedence set
      operators LEFT to right; goopg does so only when the branches are bare.
      Confirmed-wrong forms: `(A) except (B) except (C)`, the same with
      `except all`, and mixed chains such as `(A) union (B) except (C)`
      (`{1,2,3}` vs PG `{1,2}`). UNION-only and INTERSECT-only chains are
      unaffected *only* because those operators are associative — do not read
      their passing as coverage.
      **Mechanism (already root-caused, no re-diagnosis needed):**
      `parseParenthesisedSelectStmt` sets `innerSel.Parenthesized = true`
      (`internal/parser/select.go:1005`) **before** greedily absorbing a trailing
      set-op written *outside* those parentheses (`select.go:1007-1039`). The node
      then carries both `Parenthesized == true` and its own `SetOp`, and the
      planner's left-associative flattening loop breaks early at
      `if rightStmt.Parenthesized { break }`
      (`internal/planner/planner.go:696-698`), planning `A EXCEPT (B EXCEPT C)`.
      `Parenthesized` (`internal/parser/ast.go:861-867`) is overloaded against its
      own doc comment.
      **Fix in the parser, not the planner**: `Parenthesized` must describe the
      node as it stood at the closing paren, so the absorbing node may not claim
      the user's parentheses covered an operator that appeared after them. PG
      cannot express this bug at all — `select_with_parens` is a *leaf* operand in
      `gram.y`, so `transformSetOperationStmt`
      (`postgres/src/backend/parser/analyze.c`) always receives a left-deep tree.
      A planner-side patch at planner.go:696 would work but preserves the
      ambiguous flag; prefer the faithful shape.
      **Accept by VALUE**: the four-form matrix above as parser/planner unit tests,
      plus TPC-DS Q87 asserted at `47049`. Sibling-path check per Hard-won Rule #2 —
      `parseSelect` (select.go:292-295) and `parseValuesSelect` (select.go:91-94)
      attach trailing chains too, and the FROM-subquery and scalar-subquery paths
      (select.go:1372-1400, 2892-2906) repeat the same walk-to-rightmost idiom;
      audit all of them before declaring the class closed. Executor side is
      `internal/executor/operators_setop.go`. Design
      `docs/design/0125-0006-setop-chain-associativity.md`.
      **DONE 2026-07-29.** Root cause confirmed exactly as filed. The obvious
      repair — clear the lying flag — was **refuted before any code changed** by a
      PG-verified probe: `X UNION (A EXCEPT B) UNION C` is `{2,3,9}`, but fully
      left-deep it is `{2,9}`. The parentheses wrap a **PREFIX** of the chain, so
      the boundary must be a COUNT, not a bool. Fix = `SelectStmt.ParenBranches`
      (branches inside the parens; 0 = the parens covered the whole chain),
      **reset at the closing `')'`** — load-bearing for `((B) EXCEPT (C))`, whose
      outer paren genuinely does cover the operator the inner level absorbed —
      consumed by `parenBoundary` + `setOpSegment.cutAt`, which cuts the chain at
      the boundary instead of breaking. The existing save/clear/restore passes are
      re-keyed from "every segment but the last" onto `cutAt`; the plan-cache
      restore discipline is unchanged.
      **Cutting correctly exposed a second, PRE-EXISTING defect**: goopg's set-op
      fold has no operator precedence at all (`gram.y:825-826` = `%left UNION
      EXCEPT` then `%left INTERSECT`), so bare `A UNION B INTERSECT C` is already
      wrong at HEAD. Before this change the *parenthesised* spelling was right by
      accident, because flattening stopped early — so `setOpBindsTighter` declines
      the cut when the trailing operator binds tighter. That is locally correct
      precedence, and it is what makes this change non-regressing.
      **Accepted BY VALUE against PG 18.3, not by row count.** TPC-DS Q87
      `47218 -> 47049` = PG, measured on the SAME SF=1 data dir with the pre-fix
      binary rebuilt from `6c5c48ae` (1 row in both cases — no row-count gate could
      ever have seen it). 17 executor by-value cases
      (`internal/executor/setop_paren_assoc_test.go`) + 9 parser AST pins
      (`internal/parser/setop_paren_assoc_test.go`). **The gate was proved to FAIL
      before being trusted**: copied verbatim into a worktree at `6c5c48ae`, 10
      subtests FAIL while every non-regression pin (both precedence directions,
      explicit right grouping, nested-paren reset, compound-grouping-preserved,
      both associative controls) PASSES — the suite discriminates the defect, not
      the diff. Sibling-path audit per Hard-won Rule #2 done as a 30-statement
      goopg-vs-PG differential: derived table (the Q87 shape), CTE, scalar
      subquery, nested/triple parens and trailing ORDER BY all correct.
      **Four surviving divergences were each confirmed IDENTICAL on pre-fix HEAD**,
      so none is a regression; all four are filed below with ledger rows
      (M0125-0016..-0019).
- [x] **M0125-0016 — the set-op fold has no operator precedence** (discovered by
      M0125-0006 2026-07-29, ledger row 2026-07-29). PG `gram.y:825-826` declares
      `%left UNION EXCEPT` then `%left INTERSECT`, so INTERSECT binds tighter.
      goopg folds the flat segment list left-deep regardless, so **with no
      parentheses anywhere** `A UNION B INTERSECT C` is planned
      `(A UNION B) INTERSECT C`: goopg `{3}` vs PG `{1,3}` (A={1,3} B={2,3} C={3}).
      Pre-existing — verified identical on pre-fix HEAD. M0125-0006 added a local
      precedence guard at the paren boundary only (`setOpBindsTighter`), so the
      *parenthesised* spelling is correct; the bare spelling is not.
      **Fix**: in `planSelect`'s set-op fold (`internal/planner/planner.go`), group
      maximal INTERSECT runs before the UNION/EXCEPT left-fold. Two couplings must
      be re-based on the grouped tree rather than the flat segment index —
      `wrapSetOpBranchWithCasts` unifies types against the ACCUMULATED left
      operand, and `InnerSegmentCount`'s sort/limit placement is defined on the
      flat index. **Accept by VALUE**: a bare-chain matrix in both precedence
      directions plus the M0125-0006 parenthesised matrix as a non-regression pin.
      Planner change -> full pre-commit bar.
      **DONE 2026-07-29.** Design `docs/design/0125-0016-setop-operator-precedence.md`.
      Fix is a two-level precedence climb over the already-flattened segment list:
      `planSegment` (the existing cut/plan/restore dance, lifted out of the loop
      body verbatim), `applySetOp` (column-count check + `wrapSetOpBranchWithCasts`
      **re-based on the accumulated left operand** — with grouping that is no longer
      always the leftmost branch), and `foldSetOpRange`, where a leading INTERSECT
      run attaches directly to the accumulator and each later UNION/EXCEPT operand
      first absorbs the maximal INTERSECT run that follows it.
      **The second coupling turned out to be a correctness constraint, not just a
      re-base**: `InnerSegmentCount` is a hard PRECEDENCE BARRIER, because the
      user's parentheses grouped those segments explicitly —
      `(A UNION B ORDER BY 1) INTERSECT C` must not become `A UNION (B INTERSECT
      C)`. Folding each side of the barrier with a separate `foldSetOpRange` call
      gives that *and* preserves the invariant `wrapSetOpSortLimit` depends on
      (`left` == the inner compound at the wrap point), which is why the fix is a
      range fold rather than a pre-pass over the segment list.
      **Accepted BY VALUE against PG 18.3 (port 65438)**: 17 cases in
      `internal/executor/setop_precedence_test.go` — both precedence directions,
      `ALL` on either operator, multi-link and mid-chain INTERSECT runs, the
      UNION/EXCEPT tie, the barrier with `ORDER BY` and `ORDER BY … LIMIT`, and
      explicit parens overriding precedence the other way. **Proved to FAIL before
      being trusted**: copied verbatim into a worktree at `70e1ca61`, 8 subtests
      FAIL while all four controls and the entire barrier suite PASS there.
      M0125-0006's matrix passes unchanged.
      **The full 99-query SF0.5 gate was NOT run** — its guard refuses under the
      live nightly batch, and although `FORCE=1` is legitimate for a
      row-count/checksum gate, the sweep's ~21 GB Q5 peak does not fit beside the
      wedged nightly server (7.5 GB of an 18 GB budget). The set-op subset
      (Q8 Q14 Q23 Q38 Q49 Q87) was run instead: Q23/Q38/Q49/Q87 checksums are
      **byte-identical** to the HEAD sweep `sweep-20260729-123114.txt`, and Q8's
      ERROR + Q14's TIMEOUT are pre-existing and unchanged. TPC-DS cannot reach
      this defect at all — Q8/Q14/Q38 are its only INTERSECT users and every chain
      is homogeneous, hence associative. **A quiet-host loop should still run the
      full gate once**, for the same reason M0124-0002 is waiting on one.
- [x] **M0125-0017 — `ORDER BY`/`LIMIT` inside a parenthesised FIRST branch is
      hoisted to the whole set-op result, silently dropping branches** (discovered
      by M0125-0006 2026-07-29, ledger row 2026-07-29). `(A ORDER BY 1 LIMIT 2)
      UNION ALL (C)` returns `{1,2}` where PG returns `{1,2,9}` — the entire
      `UNION ALL` branch vanishes, because the LIMIT is applied to the union
      instead of to the parenthesised branch. Pre-existing (identical on pre-fix
      HEAD). The COMPOUND case already works via `InnerSegmentCount` (M0097-0044);
      only the single-branch case is broken, because
      `internal/parser/select.go:1036` records `InnerSegmentCount` only when the
      parenthesised content was already a compound and `InnerSegmentCount == 0` is
      the "unset" sentinel, so a one-branch inner group cannot be expressed.
      **Resume**: M0125-0006's new `ParenBranches` is exactly 1 in this case and is
      the natural carrier; consume it at `planner.go`'s `innerBoundary`.
      **Accept by VALUE** ({ORDER BY only, LIMIT only, both} x {parenthesised
      right branch, bare right branch}). Parser/planner change -> full pre-commit bar.
      **DONE 2026-07-29** — design doc
      `docs/design/0125-0017-setop-head-branch-sort-limit.md`. Fix =
      `SelectStmt.InnerSortLimit`, one bit that makes `InnerSegmentCount`'s 0
      mean "boundary after the FIRST branch" instead of "unset"; the planner
      consumes it by NOT clearing the sort/limit before planning the head
      branch, so `planSelect` applies them below the `SetOp` (which also lets a
      leaf branch sort by a non-output expression, as PG does). No fold change
      needed — a precedence barrier at segment 0 is vacuous. Also fixed a
      latent plan-cache hazard in the same lines: the `InnerSegmentCount` path
      blanked `s.OrderBy`/`Limit`/`Offset` without restoring them, so a second
      `Plan()` of the same AST produced an unlimited plan. 18 executor + 2
      planner + 7 parser cases, **proved to fail 8 executor subtests and both
      planner invariants at `19d844b4`**. TPC-DS cannot reach the defect (a
      reflection walk over all 99 query files found zero `InnerSortLimit`
      nodes). Two PG behaviours deferred with ledger rows (2026-07-29): an
      outer ORDER BY colliding with the head boundary, and a trailing clause
      after the `')'` discarding the inner one — see M0125-0020 below.
- [x] **M0125-0018 — IN-list and EXISTS reject a parenthesised set-op chain as an
      operand** (discovered by M0125-0006 2026-07-29; DONE 2026-07-29, design
      `docs/design/0125-0018-setop-chain-as-query-operand.md`, ledger rows
      2026-07-29). Landed wider than filed. The probe found a **third** sibling
      the report never named — `x = ANY ((A) UNION (B))`, `parseAnyTail`, same
      one-token lookahead — and a **quiet** half of the same root cause:
      `x IN ((SELECT multirow))` parsed as a one-element VALUE LIST holding a
      scalar subquery and raised `21000 more than one row returned by a
      subquery` where PG answers the IN (`select 1 in ((select 1 union select
      2))` = `t` on the oracle). Peeling the `(` run is NOT a sufficient test:
      `((select 1),(select 2))`, `((select 1)::int)` and `((select 1) + 1)` are
      expressions in PG. The discriminator is what follows the group's matching
      `)` — `,`/`::`/operator ⇒ expression; `)`/set-op/`ORDER`/`LIMIT`/`OFFSET`/
      `FOR` ⇒ query. Fix = `selectWithParensAhead()` (non-consuming depth walk)
      + `parseQueryOperandWithParens()` at all three sites, delegating to
      `parseParenthesisedSelectStmt` so the operand inherits M0125-0016's
      precedence and M0125-0017's head-branch sort/limit. EXISTS stays strict
      (`EXISTS ((1))` still errors — gram.y gives it no expression alternative).
      20 executor + 16 parser tests, **proved to fail 14 + 10 subtests at
      `74f4b264`** with every control green. **TPC-DS cannot reach it** — zero of
      99 SF0.5 query files match `(in|exists|any|all)\s*\(\s*\(`. Gates: units
      PASS, `tpch-spotcheck.sh` PASS (Q12=2, Q13=35), pgbench smoke via hook.
- [x] **M0125-0019 — `string_agg(x, ',' ORDER BY x)` ignores the aggregate's own
      ORDER BY** (discovered by M0125-0006 2026-07-29; DONE 2026-07-29, design
      `docs/design/0125-0019-aggregate-own-order-by.md`, two ledger rows
      2026-07-29). Root cause was a **sibling asymmetry inside one `switch`**:
      `applyAgg` has exactly two order-sensitive built-in branches and only
      `array_agg` captured its keys — Hard-won Rule #2 verbatim. The clause was
      intact at every upstream stage (parser `FuncCall.OrderBy`, M0125-0009's
      `funcCallTailKey`, planner `AggregateCall.OrderBy` with `NullsFirst`
      already defaulted by `sortByNullsFirst`). Fix = deferred concatenation
      (`strElems`/`strDelims`/`strElemKeys`, live only when the call has an
      ORDER BY, so the common path allocates nothing new) + `aggOrderBySortedIdx`,
      one stable comparator now SHARED by both branches. The delimiter is the
      subtle part: it is a per-row argument and PG emits the RIGHT-hand row's
      own delimiter, so delimiters ride the same permutation as the values
      (oracle-verified: `string_agg(n,d order by n)` over `('c','|'),('a','+'),
      ('b','*')` = `a*b|c`). Also closed a LATENT second defect —
      `planner.AggregateIsOrderSensitive` already allowed a parallel plan under
      an ordered `string_agg` on the premise that the aggregate sorts its own
      input, which was false until now, so such a plan could shuffle
      differently on every run. 17 by-value subtests, **13 proved to fail at
      `6088e41b` with all three controls green there**. TPC-DS cannot reach it
      (zero of the 100 query files use `string_agg`/`array_agg`/`json_agg`/
      `xmlagg`). Two ledger rows deferred → M0125-0021 (bytea held as text) and
      the five order-sensitive aggregates with no `applyAgg` branch at all.
- [x] **M0125-0021 — a `bytea` literal was carried as TEXT, so `encode()`
      returned an empty string and `length()` counted escape characters**
      (discovered by M0125-0019 2026-07-29; DONE 2026-07-29, design
      `docs/design/0125-0021-pg-faithful-bytea-value.md`, three ledger rows
      2026-07-29). `'\xaabb'::bytea` was the six-character **string**
      `\xaabb`, not the two bytes. Two symptoms are loud (`length`=6 vs 2;
      `order by b` sorted by the backslash), the third is not and is why this
      was a defect rather than a gap: `encode` is *the* way a caller hex-dumps
      a bytea, so a stub returning `''` instead of `42883` made a hex dump
      silently produce nothing. Root cause = `evalCast` had **no `bytea` arm**,
      so the cast fell through the pass-through default and kept `KindString`;
      `encodeValuePG` had the same hole from the other side. `KindBytes` and
      `decode()` already existed — what was missing was any path from a literal
      into it. Fix = `internal/executor/bytea.go` (transliterations of
      `byteain`, `hex_decode_safe`, `esc_decode`, `pg_base64_decode`,
      `hex_encode`, `esc_encode`, `pg_base64_encode`, `byteaout`) wired in
      **sibling pairs**: cast ↔ storage encoder, `decode(…,'hex')` ↔
      `'\x…'::bytea`, `<bytea>::text` ↔ the wire renderer, and executor Kind ↔
      `planner.exprType`'s advertised column type (a `KindBytes` datum typed
      `text` reaches psql as raw bytes). The comparator arm is load-bearing:
      once a column holds two bytes, `where b = '\xaabb'` would have matched
      **nothing** — a wrong number turned into a silently empty result set.
      Three upstream subtleties pinned: `encode(…,'escape')` is `esc_encode`
      (NUL/high-bit/backslash only), NOT `byteaout`'s escape mode;
      `pg_base64_encode` wraps at 76 chars so its own output must be decodable
      (Go's `base64.StdEncoding` rejects those newlines); hex errors are
      `22023` while `byteain`'s escape pass raises `22P02`. Also removed three
      pre-existing `decode()` deviations. **Accepted BY VALUE**: a 27-statement
      matrix through psql against a throwaway goopg server vs the PG 18.3
      oracle (port 65438) diffed **byte-identical**, plus 40 by-value subtests.
      Three ledger rows deferred: `int → bytea` casts, the `bytea_output` GUC,
      and the remaining bytea operators (`position`/`overlay`/`trim`/
      `get_byte`/`set_byte`/`bit_length`).
- [x] **M0125-0020 — the set-op chain is now a TREE; the three annotations are
      retired** (discovered by M0125-0017 2026-07-29; DONE 2026-07-29, design
      `docs/design/0125-0020-setop-chain-as-tree.md`, three ledger rows
      2026-07-29). goopg stored a set-op chain as a linked list
      (`SelectStmt.SetOp.Right`) whose head doubled as its leftmost operand, so
      a parenthesised head branch and the whole compound were ONE node sharing
      ONE `OrderBy`/`Limit`/`Offset` slot, and `parseParenthesisedSelectStmt`
      absorbed a set-operator written AFTER the `')'` into the parenthesised
      query's own chain. The two filed shapes now match PG
      (`(A ORDER BY 1 LIMIT 2) UNION ALL (C) ORDER BY 1 DESC` = `9,2,1`;
      `((A ORDER BY 1 LIMIT 2) UNION ALL C) LIMIT 1` keeps both limits), and the
      HEAD worktree probe found the damage was **wider than filed** — three more
      shapes returned wrong ROWS, not merely a wrong order (`… ORDER BY 1 DESC
      LIMIT 2` gave `9,3` for `9,2`; `((A LIMIT 1) UNION ALL (G LIMIT 1)) ORDER
      BY 1 DESC` gave the single row `3` for `2,1`). Fix = a **grouping node**
      (`SelectStmt.SetOpOperand`), built only when a set-operator or
      ORDER/LIMIT/OFFSET follows the `')'`; token CONSUMPTION is unchanged, only
      where clauses are STORED, which is why the whole existing
      -0006/-0016/-0017 matrix stayed green unmodified. `Parenthesized` is
      sharpened to "nothing followed the `')'`". Retires `ParenBranches`,
      `InnerSegmentCount`, `InnerSortLimit`, `parenBoundary`, the paren-boundary
      half of `cutAt`, and `headSortLimit`/`innerBoundary`/`sortLimitConsumed`;
      `setOpBindsTighter` survives with `foldSetOpRange` as its only caller.
      A grouping node has no target list, so the analyzer gained
      `setOpLeftmostBranch` at 4 sites and 8 structural walkers + 2
      simple-SELECT gates learned to descend through it. Accepted BY VALUE:
      13 new executor subtests, **proved to fail 5 at `8ce216dd`** with every
      control green, plus a 27-statement psql matrix **byte-identical** to PG
      18.3. **TPC-DS CAN reach this one** (unlike -0018/-0019/-0021): Q87, Q14
      and Q23 have parenthesised operands followed by a set-operator. Gates:
      units PASS, `tpch-spotcheck.sh` PASS (Q12=2, Q13=35), SF0.5 subset probe
      over the ten set-op queries PASS=6 ck-verified / MISMATCH=0 / ERROR=0 with
      Q87+Q23 checksum-identical to the `sweep-20260729-123114` baseline,
      pgbench smoke via hook. Deferred (ledger): `CREATE VIEW v AS (SELECT …)
      UNION …` is a pre-existing view-body parser gap, `renameColumnInSelect`
      still skips a set-op right branch, and the FULL 99-query SF0.5 gate is
      still owed — but its six-loop blocker (the wedged nightly server) CLEARED
      during this loop.
- [x] **M0125-0007 — date input rejects unpadded month/day, and the comparison
      path fails SILENTLY** (discovered by M0124-0001 chunk 12, ledger row
      2026-07-28). PG's `DecodeDate`/`ParseDateTime`
      (`postgres/src/backend/utils/adt/datetime.c`) accept 1-or-2-digit month and
      day fields; goopg parses with a fixed Go layout
      `time.Parse("2006-01-02", …)` (`internal/executor/expr.go:2874`, sibling
      `parseDateFields` at `internal/pgnodes/datum.go:974`) and requires
      zero-padding. **Two sibling paths disagree, which is the real defect**:
      `cast('2002-5-01' as date)` / `date '2002-5-01'` / `'2002-5-01'::date` all
      raise `invalid input syntax for type date`, but `d_date = '2002-5-01'`
      raises nothing and **matches 0 rows**. A compat gap that errors is loud; one
      that silently returns the empty set is a wrong answer — TPC-DS Q94 and Q95
      report `0 / NULL / NULL` at a matching row count of 1, and Q16 did the same
      undetected since chunk 2 (`0` vs PG `45`). Single-digit *day*
      (`'2002-05-1'`) is affected identically.
      **Accept by VALUE**: `select '2002-5-01'::date` = `2002-05-01`; the
      comparison and cast paths agree on every form; TPC-DS Q16/Q94/Q95 asserted
      against PG (Q95 = `57 / 85887.62 / -27169.36`; Q94 needs M0125-0008 too).
      Sibling-path check per Hard-won Rule #2 — audit **every** date/time entry
      point together (executor cast, implicit coercion in `codec.go:346`, COPY's
      `copy_text.go:818`, `pgnodes/datum.go:974`), and cover timestamp/time as
      well: the same fixed-layout idiom likely rejects unpadded hours. Prefer a
      shared PG-faithful field decoder over per-site `time.Parse` layouts.
      Design `docs/design/0125-0007-pg-faithful-date-field-decode.md`.
      ~~**Blocked until the sweep reaches Q99**~~ — **UNBLOCKED 2026-07-29**: the
      full 99-query SF0.5 gate ran (`analysis/tpcds-sf05-full-gate-20260729/`)
      and reached Q99. It also supplies the **value-level** evidence this item
      previously lacked: all three of Q16/Q94/Q95 now report the *identical*
      goopg checksum `512b5fdab820c47b` (the `0/NULL/NULL` answer) against three
      *different* oracle checksums (`40dbec0df91d2438` / `04afc1b69831a5ea` /
      `e498634c02595c29`) — one defect, three queries, and a ready-made
      acceptance signal: the fix must change that one goopg ck to three
      distinct values matching the oracle. Executor/codec change, so it requires
      `tpch-spotcheck.sh` + the SF0.5 gate per the pre-commit bar, plus
      the full regress-port suite (Hard-won Rule #5 — this is a codec change).
      **↳ DONE 2026-07-30** (`analysis/m0125-0007/README.md`, design
      `docs/design/0125-0007-pg-faithful-date-field-decode.md`). New leaf package
      `internal/pgdatetime` normalises PG's ISO numeric spellings — unpadded
      month/day/hour/minute/second plus the surrounding-whitespace trim — in ONE
      place, with `DecodeNumber`'s own `flen >= 3` floor on the leading field so
      the DateStyle-dependent forms stay loud errors instead of becoming
      silently-wrong dates. Every executor entry point routes through it: the
      `date` and `timestamp`/`timestamptz` cast cases and `pg_input_is_valid`
      (`expr.go`), and `parseTimeString` / `parseTimeTZString` /
      `parseCopyTimestamp` (`copy_text.go`), which funnel COPY TEXT, `codec.go`'s
      date encode and `tryParseStringAs`. `internal/pgnodes` needed nothing — its
      `parseDateFields` was the lenient sibling all along. **A second silent wrong
      answer was found and fixed on the way**: `ts_col = '2002-05-01 03:04:05'`
      also matched nothing, fully padded, because `tryParseStringAs` tried
      `parseTimeString` first and it anchors the stripped time at 1970-01-01.
      **Acceptance: the predicted signature landed exactly** — the one goopg
      checksum `512b5fdab820c47b` became three distinct ones and `0 / NULL / NULL`
      became real numbers — but NOT the oracle's three, because a second defect
      sits behind each, and the probe says which: **Q16 `63` and Q94 `7` now
      OVER-count** PG's `23` and `2` (the EXISTS+NOT EXISTS shape = **M0125-0008**,
      which did not previously name Q16), while **Q95 `5` UNDER-counts** PG's `23`
      and contains no EXISTS at all (two `IN (subquery)` over a CTE — a different
      mechanism, filed as **M0125-0023**). Gates: units PASS;
      `tpch-spotcheck.sh` PASS (Q12=2, Q13=35); regress-port quick set + the six
      datetime suites diffed against a HEAD-`337526b1` worktree binary — 1/52 PASS
      on both, every per-test diff byte-identical bar a clock-dependent `uuidv7`
      test. **The full 99-query SF0.5 gate is still OWED** (ledger row): the
      nightly CI batch held the host all loop, so only the 3-query value probe ran.
      Five deferral rows record what PG still accepts and goopg does not, the
      still-silent `d_date = 'garbage'`, the 22007-vs-22008 split, and two
      pre-existing wrong answers this reproduction surfaced (dates outside Go's
      `time.Time` nanosecond range round-trip wrong — `'0002-01-01'::date` gives
      `1755-08-30`; a plain `timestamp` applies an explicit offset instead of
      discarding it).
- [x] **M0125-0008 — EXISTS + NOT EXISTS on the same outer relation yields a
      NON-SUBSET result** (discovered by M0124-0001 chunk 12, ledger row
      2026-07-28). With TPC-DS Q94's date literals padded so M0125-0007 is out of
      the way, each correlated subquery is correct **alone** — base joins 33 rows
      / 25 distinct orders (= PG), `+ EXISTS (… ws_warehouse_sk <> …)` 33/25
      (= PG), `+ NOT EXISTS (web_returns …)` 11/9 (= PG) — but **together goopg
      returns 25/18 where PG returns 11/9**. 25 is not a subset of the 11 that the
      anti-join alone admits: adding a conjunct *grew* the result, so a residual
      predicate is being dropped or mapped to the wrong source relation when a
      SEMI and an ANTI join coexist over one outer rel. This is precisely the
      "Semi/Anti residual ↔ source-table mapping" sibling pair named in Hard-won
      Rule #2. PG control: the padded query returns `9 | 18130.71 | -9444.12`,
      byte-identical to the unpadded run, so padding does not confound it.
      **Start at** the semi/anti residual + `SourceTableIdx` mapping in
      `internal/planner/` join construction (the M0077 Q21 work touched the same
      mapping) and the anti-join operator in `internal/executor/`.
      **Accept by VALUE**: the four-row isolation matrix above as a planner/executor
      test (each subquery alone AND the conjunction), the monotonicity invariant
      (result of `base + p + q` ⊆ result of `base + q`) asserted directly, and
      TPC-DS Q94 = `9 | 18130.71 | -9444.12`. Design
      `docs/design/0125-0008-semi-anti-conjunction-residual.md`.
      **Blocked until the sweep reaches Q99**; planner/executor change → full
      pre-commit bar (`tpch-spotcheck.sh`, SF0.5 gate, `make plan-diff`).
      **↳ UNBLOCKED, and Q16 now belongs here too (2026-07-30, M0125-0007's
      acceptance probe).** With the date defect gone, both EXISTS+NOT EXISTS
      queries over-count at SF0.5: **Q16 returns `63 | 319602.45 | -91294.46`
      against PG's `23 | 93334.17 | -35323.69`**, and **Q94 returns
      `7 | 10534.30 | 7178.64` against PG's `2 | 5037.18 | 1067.82`** — the same
      conjunction-grows-the-result signature, on the catalog side. Add Q16's
      values to the acceptance set; its four-row isolation matrix has not been
      taken yet. Q95 is NOT this defect (it under-counts and has no EXISTS) —
      see M0125-0023.
      **↳ CLOSED 2026-07-30.** Design
      `docs/design/0125-0008-semi-anti-conjunction-residual.md` (indexed). Root
      cause was NOT a dropped residual or a `SourceTableIdx` mis-map: a Semi/Anti
      join publishes the OUTER row only, and every writer set its cached `schema`
      to a *copy* of `Left.Output()` — then `rewriteMultiWayChain` OID-sorted the
      subtree below the pinned spine **in place**, leaving that copy a stale
      PERMUTATION. Widths still matched so nothing detected it; the damage came
      from `reresolveJoinByName` re-resolving keys BY NAME against
      `j.Left.Output()`, which for a semi/anti join *stacked on another one* was
      the phantom layout — the upper join's key landed on `dsk` instead of `ord`,
      matched nothing, and (anti) passed every probe row through. Hence the sharp
      bisect: correct at 1–2 base tables, wrong from **3** (the MHJ-packing
      threshold), and reversing the conjuncts moved the failure to whichever join
      ended up on top. Fix is one derivation in `internal/planner/plan.go`
      (`Join.Output()` returns `Left.Output()` for Semi/Anti) rather than a fifth
      refresh site. **Q16 and Q94 both PASS the SF0.5 oracle's exact value
      checksums** (`40dbec0df91d2438`, `04afc1b69831a5ea`). The predicted
      four-row isolation matrix WAS taken and shipped as tests. **The item's
      "Q95 is NOT this defect" note was wrong** — see M0125-0023.
- [x] **M0125-0009 — `parserExprKey` fallback keys on the Go TYPE NAME, collapsing
      sibling aggregates** (discovered by M0124-0001 chunk 12, ledger row
      2026-07-28). **One-line root cause, wide blast radius.** Aggregate dedup
      keys are built by `aggregateCallKey` → `parserExprKey`
      (`internal/planner/planner.go:6891`, `:7425`), whose fallback is
      `return fmt.Sprintf("expr:%T", e)` (**`planner.go:7484`**) — the Go type
      name, carrying **no expression content**. Every `*parser.CaseExpr` therefore
      hashes to the identical key, so the 2nd..Nth `sum(CASE …)` in one SELECT are
      discarded as duplicates (`planner.go:5844-5846`) and every sibling pivot
      column reads the **first** aggregate's slot. Reproducer:
      `select sum(case when d_day_name='Sunday' then 1 else 0 end),
      sum(case when d_day_name='Monday' then 1 else 0 end) from date_dim`
      → goopg `10435|10435`, PG `10435|10436`. Controls that pin it (all correct
      in goopg): distinct agg function names, `sum(d_dom+1), sum(d_dom+2)`
      (`BinaryOp` has a real case), and `sum(col), sum(CASE …)` (mixed shapes).
      **17 expression types share the fallback** — `CaseExpr`, `ExtractExpr`,
      `InExpr`, `RowExpr`, `SubqueryExpr`, `ExistsExpr`, `IntervalLit`,
      `ArrayConstructorExpr`, `ArraySubqueryExpr`, `ArraySubscriptExpr`,
      `CollateExpr`, `IsBoolExpr`, `GroupingCall`, `TypedStringLit`,
      `DefaultMarker`, `IndirectionStar`, `PartitionRangeBoundKeyword` — so the
      class is far broader than CASE, and **the same key feeds GROUP BY matching**
      (see the M0097-0003 comment at `planner.go:7443`), so grouping by two
      distinct CASE expressions is suspect too. This is the **third** recurrence of
      one failure mode: `planner.go:6905-6909` documents `count(*)` vs
      `count(*) FILTER (WHERE …)` collapsing identically (M0097-0032), and
      M0097-0003 the ColumnRef case. **Fix the fallback, not another special
      case** — make the default path either recurse structurally over all
      `parser.Expr` children or fail loudly (an unknown expr type must never
      silently compare EQUAL to a different instance of the same type); a
      deparse-based or reflective key would close all 17 at once. Add an
      exhaustiveness test so a newly added Expr type cannot re-open this.
      **Accept by VALUE**: the reproducer + control matrix as planner unit tests,
      one test per previously-unhandled type, and the TPC-DS pivot queries
      (Q43/Q50/Q66/**Q97**/**Q99**/Q2/Q39) asserted against PG.
      **Chunk 13 (2026-07-28) added Q97 and Q99 as the 4th and 5th instances**,
      raising the evidence to five queries. Q97 is the most legible instance in
      the sweep and the sharpest acceptance case: its three columns
      (`store_only`, `catalog_only`, `store_and_catalog`) are **disjoint by
      construction**, so goopg's `392155|392155|392155` (PG:
      `541140|286927|161`) is not merely wrong but internally impossible — assert
      the disjointness invariant, not just the literal triple. Q99's five ship-lag
      buckets show the same shape with col 1 correct and cols 2–5 replicating it
      (`1231|1231|1231|1231|1231` vs PG `1231|1228|1289|0|0`), which pins the
      "reads the FIRST aggregate's slot" mechanism directly.
      **M0124-0006 (2026-07-29) settled the evidence set at TEN queries** — Q2 Q21
      Q40 Q43 Q50 Q59 Q62 Q66 Q97 Q99 — with two corrections to the earlier guess.
      (a) **Q39 is NOT an instance**: its `cov` columns differ by a relative
      1.4e-16 (`…82042` vs `…82044`), i.e. float8 accumulation order, not a
      collapse — drop it from the acceptance set. (b) **Q66 IS an instance**
      despite its outer aggregates being `sum(<plain column>)` (a working
      control): its *inner* derived table holds **48 `sum(CASE …)` siblings**
      (`query66.sql:56+`) that collapse there, and the outer sums then faithfully
      add twelve already-identical columns. The tell is that goopg's
      `jan_net…dec_net` equal `jan_sales` **exactly** — every one of the 48
      collapsed onto the first `sum(CASE)` in that subquery. Q66 is therefore the
      widest-blast-radius acceptance case (34 wrong columns in 5 rows).
      **Do not confuse this with M0125-0010** (filed 2026-07-29): that one
      collapses sibling aggregates *by function name* through a FROM-subquery and
      needs no `CASE`; this one collapses `CASE` expressions and needs no
      subquery. Neither subsumes the other and both are live.
      Design `docs/design/0125-0009-parser-expr-key-structural.md`.
      **Sweep precondition SATISFIED 2026-07-28** (the sweep reached Q99; 99/99
      measured) — the engine-commit freeze lifts once M0124-0001's merged
      deliverable lands, which is the only remaining gate on starting this.
      Planner change → full pre-commit bar.
      Likely the single highest-value fix in the TPC-DS programme: it silently
      corrupts every pivot-style aggregate query while keeping row counts intact.
      **DONE 2026-07-29.** Fallback replaced by a reflective structural walk over
      exported fields (`internal/planner/exprkey.go`), skipping the unexported
      `pos` — the analogue of PG `equalfuncs.c`'s `COMPARE_LOCATION_FIELD` no-op,
      and load-bearing: without it `GROUP BY <case>` would start raising a
      spurious 42803. Nested nodes route back through `parserExprKey` so the
      ColumnRef normalisation still applies at depth; maps render sorted for
      determinism; cycles are path-marked. Two explicit cases leaked the same way
      and were folded in — `FuncCall` dropped FILTER/OVER/in-arg ORDER BY/WITHIN
      GROUP/VARIADIC (so `string_agg(x,',' ORDER BY a)` collapsed with
      `… ORDER BY b`; `funcCallTailKey` now serves both `parserExprKey` and
      `aggregateCallKey`, subsuming M0097-0032's one-off), and `CastExpr` dropped
      `Typmods`. Exhaustiveness gate is two tests in
      `internal/planner/exprkey_test.go`: a source scan of `exprNode()` receivers
      that fails when a new Expr type is unregistered (goopg's answer to PG's
      `elog(ERROR, "unrecognized node type")`), and a per-field test asserting
      every exported field changes the key — exemptions must be declared with a
      reason and a *stale* exemption fails too. Against the OLD key that test
      enumerates **40+ field-level collapses**. **Measured at SF=1 (65436 vs
      65438), all ten evidence queries re-run:** Q2/Q40/Q43/Q59 byte-identical to
      PG; Q50/Q62/Q99 value-identical (differing only by the known `char(n)`
      blank-padding gap — Q99 is now `1231|1228|1289|0|0` = PG, was
      `1231|1231|1231|1231|1231`); flat reproducer `10435|10436|10436` = PG.
      **Q21 and Q66 still diverge for an INDEPENDENT reason** — both wrap their
      aggregates in a FROM-subquery, so `remapSubqueryColumnRefs` rebinds every
      target to the first `sum` slot; that is **M0125-0010**, and the two defects
      compose (each needs both fixes). This is the prediction in the "do not
      confuse this with M0125-0010" note above, confirmed by measurement.
      **Q97's collapse is gone** (`392155|177135|1553910`, was
      `392155|392155|392155`) and its residual gap was isolated to a NEW defect —
      **M0125-0011** below. Design
      `docs/design/0125-0009-parser-expr-key-structural.md`.

- [x] **M0125-0010 — FROM-subquery Project remap binds sibling aggregates by
      FUNCTION NAME, so `select * from (select sum(a), sum(b) …) d` returns
      `sum(a)` twice** (discovered by M0124-0006 2026-07-29, ledger row
      2026-07-29). **One-line root cause, wrong answers, row counts intact.**
      Minimal reproducer, no `CASE`, no `GROUP BY`, on the SF=1 clusters:
      `select * from (select sum(d_dom) a, sum(d_year) b from date_dim) d;`
      → goopg `1149021|1149021`, PG `1149021|146061700`. The identical flat query
      (`select sum(d_dom), sum(d_year) from date_dim`) is **correct**.
      Root cause: `remapSubqueryColumnRefs` (`internal/planner/planner.go:2450`)
      is called **only** from the FROM-subquery path (`planSubqueryRangeVar`,
      `planner.go:3158`). For every `Project` target that is a bare `ColumnRef` it
      rebuilds the index by matching the **column name** against the child output
      schema — `strings.EqualFold(cr.Name, sc.Name)` with `break` on the first hit
      (**`planner.go:2468`**). An `Aggregate` names its output columns after the
      aggregate *function*, so two `sum`s yield two child columns both named
      `sum` and every target binds to the first slot. The pass's own comment calls
      it a "safety-normalisation" for outer-resolve-context leakage; it is
      unsound as written because **an output schema's names are not unique**.
      Control matrix (all probed on 65436 vs 65438, read-only): flat / top-level
      `GROUP BY` / CTE (`with x as (…) select * from x`) / different function
      names (`sum,avg`; `sum,count(*),avg`) / non-`ColumnRef` target
      (`max(a)+0`) / aggregates *outside* a `UNION ALL` derived table are **all
      correct**; FROM-subquery with two same-named aggregates is wrong **even
      with an explicit `d(x,y)` column list**, and selecting `d.b` alone returns
      `a` — so it is the *inner* plan that is wrong, not outer name resolution.
      `count(x)` vs `count(distinct x)` also collapse, because `DISTINCT` does not
      change the output column name (`count(distinct …)` alone is correct:
      probed 134220|18480 = PG).
      **Fix the matching, not the names** — the remap must be positional, or must
      refuse to rewrite when the child schema has duplicate names, or must key on
      the target's identity rather than its label. Silently binding to the first
      of N equally-named columns is the same failure mode as M0125-0009,
      M0097-0032 and M0097-0003: *an ambiguous key resolved by taking the first
      match*. Verify first whether the pass is still needed at all — if the leakage
      it guards against is gone, deleting it is the better fix; if it is still
      needed, add a regression test for the leakage case before changing it.
      **Evidence (4 TPC-DS queries, all `OK`/`OK` with matching row counts)**:
      **Q28** (`count`/`count distinct` pair wrong in all six cross-joined blocks;
      `avg` correct), **Q46** (`profit` = `amt`), **Q68** (`extended_tax` and
      `list_price` both = `extended_price`), **Q79** (`profit` = `amt`).
      **Accept by VALUE**: the reproducer + the full control matrix above as
      planner unit tests, plus Q28/Q46/Q68/Q79 asserted against PG. Row-count
      gates cannot see this class — `scripts/tpch-spotcheck.sh` and the SF0.5
      oracle both pass today.
      Design: extend `docs/design/0125-0009-parser-expr-key-structural.md` with a
      second section (same failure mode, different key) rather than a new doc.
      Planner change → full pre-commit bar. Blocked by the same engine-commit
      freeze as M0125-0009 (lifts when M0124-0001's merged deliverable lands).
      **UNBLOCKED 2026-07-29** (freeze lifted; M0125-0009 landed). **Evidence
      grew to SIX queries**: re-running the M0125-0009 acceptance set at SF=1
      showed **Q21** and **Q66** still divergent *after* the CASE collapse was
      fixed, and both are this shape — Q21 is
      `select * from (… sum(case…) inv_before, sum(case…) inv_after …) x`
      (goopg `1516|1516`, PG `1516|2833`) and Q66's inner derived table holds the
      48 `sum(CASE …)` siblings whose now-distinct slots are re-collapsed by the
      remap (34 replicated columns in 5 rows — the widest blast radius on record).
      The two defects **compose**: Q21/Q66 need both fixes, so neither can be
      graded by "does the query match PG" alone. **This is now the top of the
      value-divergence queue.**
      **CLOSED 2026-07-29.** Fix = `remapSubqueryColumnRefs` is now
      **verify-then-repair**: a bare-`ColumnRef` target whose existing index is
      in range AND names the column the ref asks for is left untouched (the only
      branch that can tell two same-named child columns apart, so it must run
      before any name search); only an out-of-range index, or one naming a
      different column — the actual M0097-0058 leakage signature — is re-derived
      by name. A plan dump with the pass disabled proved the **pre-remap indices
      were already correct**: the pass was causing the damage, not repairing it.
      A *positional* remap (which the pass's own doc comment claimed to
      implement) was rejected — it breaks any `Project` that reorders or subsets
      its child (`select b, a from t`). Gate = 3 tests in the new
      `internal/planner/subquery_remap_test.go`; against the old code 4 of the 6
      control-matrix rows fail, `group by` as the partial collapse `[0 1 1]`.
      The third test is the M0097-0058 leakage-repair guard, without which the
      fix could be "simplified" into deleting the pass and reintroducing the
      original index-out-of-bounds crash. **Measured at SF=1 (65436 vs 65438):
      all six carrier queries now match PG** — reproducer and Q21 byte-identical,
      Q28/Q46/Q66/Q68/Q79 identical modulo the separate `char(n)` padding gap.
      Q21 and Q66 needed BOTH this and M0125-0009. Artifacts
      `analysis/m0125-0010-acceptance/`; design = §9 of
      `docs/design/0125-0009-parser-expr-key-structural.md`; ledger row
      2026-07-29 records the undiagnosed leak the pass still guards.
- [x] **M0125-0011 — FULL OUTER JOIN drops all but the FIRST conjunct of its ON
      condition** (discovered by M0125-0009's acceptance run, 2026-07-29, ledger
      row 2026-07-29). Isolated on the SF=1 clusters from TPC-DS **Q97**, whose
      residual divergence survived the M0125-0009 fix. Probe matrix (goopg 65436
      vs PG 65438, read-only, `ssci`/`csci` = Q97's two CTEs):

      | probe | goopg | PG |
      |---|---|---|
      | `count(*)` of each CTE | `548694 / 287769` | `548694 / 287769` |
      | `ssci JOIN csci ON (customer_sk AND item_sk)` | `161` | `161` |
      | `ssci FULL OUTER JOIN csci ON (customer_sk)` | `2131274` | `2131274` |
      | `ssci FULL OUTER JOIN csci ON (customer_sk AND item_sk)` | **`2131274`** | **`836302`** |

      The inputs agree, the INNER join on both keys agrees, and the single-key
      FULL OUTER JOIN agrees exactly — only the two-conjunct FULL OUTER JOIN
      diverges, and it returns *precisely the single-key number*, so the second
      equality is being dropped rather than mis-evaluated. PG's `836302` is
      `548694 + 287769 − 161`, the full-outer identity for 161 matches, which
      independently confirms the reference side. (`sum(case …)` totals sit `8074`
      below the row count on BOTH engines — rows with NULL `customer_sk` on both
      sides match no CASE arm; not a defect, do not chase it.)
      **Start at** the FULL OUTER JOIN construction in `internal/planner/` — how
      a multi-conjunct `ON` is split into join keys vs residual, and whether the
      residual is attached at all for the FULL variety (the INNER path clearly
      keeps it, since the inner join returns PG's 161). Then the full-outer
      operator in `internal/executor/`. Related sibling pair from Hard-won Rule
      #2: Semi/Anti residual ↔ source-table mapping (see M0125-0008).
      **Accept by VALUE**: the four-row probe matrix above as a planner/executor
      test (a two-key FULL OUTER JOIN must NOT equal its single-key counterpart),
      plus TPC-DS Q97 = `541140|286927|161`. Unlike M0125-0009 this one *changes
      the row count*, so the SF0.5 gate and the nightly anchors can see it —
      check whether Q97's anchors need re-pinning once fixed.
      Design `docs/design/0125-0011-full-outer-join-on-conjunct-drop.md`.
      Planner/executor change → full pre-commit bar (`tpch-spotcheck.sh`, SF0.5
      gate, `make plan-diff`).
      **DONE 2026-07-29.** Cause was NOT in the planner: `splitEqualityForHash`
      correctly returns *a* key and leaves the whole ON clause in
      `Join.Predicate`; the executor's `runMergeJoin` then **never read
      `Join.Predicate` at all**, so every conjunct after the first was dropped.
      Fourth instance of Hard-won Rule #2 — nested-loop, lateral and hash all
      call `joinPredicateMatch`, only the merge twin omitted it (which is why
      the INNER probe was already right). The bug was **wider than filed**:
      RIGHT OUTER JOIN is routed to `JoinAlgoMerge` too and was wrong the same
      way. It also *lost* rows, not just invented them — the `cmp<0`/`cmp>0`
      arms only null-extend rows whose KEY found no partner, so a row whose key
      matched but whose residual failed could not be emitted at all. Fix
      mirrors `nodeMergejoin.c` (`EXEC_MJ_JOINTUPLES` → `ExecQual(joinqual)`,
      then `MJ_FILL_OUTER`/`MJ_FILL_INNER`). Accepted by value: the four-row
      probe matrix above now matches PG on **all** rows (two-key FOJ `836302`,
      was `2131274`) and Q97 = `541140|286927|161`. Gates: 24-cell PG
      differential (4 join types × 6 ON shapes incl. NULL keys and
      non-equality residuals, 24/24 match); new
      `TestExecMergeJoinAppliesResidualConjuncts` (fails 5/5 subtests pre-fix);
      units suite; `tpch-spotcheck.sh` PASS (Q12=2, Q13=35); full SF0.5 sweep;
      `make plan-diff`.
      **The SF0.5 gate DOES see it, as predicted** — full 99-query sweep went
      `PASS=73 CKMISMATCH=5` -> `PASS=74 CKMISMATCH=4`, with **Q97
      `CKMISMATCH -> PASS` the ONLY per-query change of 99** (baseline
      `analysis/tpcds-sf05-ck-m0124-0005/sweep/sweep-20260729-064607.txt`), and a
      same-harness A/B reads pre-fix `ck=5687f61d9fdd4f93` vs post-fix
      `ck=65725195ebe13a3b` (= the oracle). **No Q97 anchor re-pinning needed** —
      the oracle was already right; goopg moved onto it.
      **⚠ Gate-integrity trap found en route (ledger row 2026-07-29).** An
      earlier probe appeared to show the SF0.5 gate was BLIND to this defect —
      it was an artifact, and the wrong conclusion was nearly recorded here.
      `scripts/tpcds-sf05-regression.sh`'s **sweep path never rebuilds
      `tmp/goopg-bench-bin`**: line 256 guards the build with `[[ -x
      "${GOOPG_BIN}" ]] ||` and sits in `load-goopg`, not `sweep`. So editing or
      `git stash`-ing a source file changes nothing about what the sweep runs —
      it measures whatever binary was last left in `tmp/` (possibly the nightly
      batch's). **A source-level A/B against this gate is meaningless without an
      explicit `go build -o tmp/goopg-bench-bin ./cmd/goopg` in each arm, and a
      green sweep after an edit does not prove the edit was exercised.**
      (`bench/tpcds/server.sh` *does* rebuild unconditionally, so which entry
      point started the server decides whether your change is under test.)
      **↳ TRAP CLOSED 2026-07-30** (that ledger row flipped `resolved`): `sweep`
      now calls `sf05_ensure_bin` and builds unconditionally (`SF05_NO_BUILD=1`
      opts out and the report then declares the binary's provenance unknown), the
      header carries design **0124-0001 D4a**'s three fields verbatim
      (`# goopg:` / `# engine-id:` / `# engine-binary: running=… on-disk=…`)
      rather than a second ad-hoc format, the three definitions are SHARED from
      `bench/tpcds/env_tpcds.sh` (`bench_engine_id`, `bench_engine_bin_sha`,
      `bench_running_engine_sha`; `scripts/tpcds-bench-compare.sh` delegates), and
      `sf05_guard_engine_stable` closes the restart hole — `RESTART_AFTER_TIMEOUT`
      and the crash-restart re-exec `${GOOPG_BIN}` *as it is then*, so an
      `engine-id` change writes `*** SWEEP VOID: engine source changed
      mid-sweep ***` into the report and an image-only change gets D4a's benign
      docs/tracker-rebuild note. Plus two guards on the shared-path hazard
      itself: rebuilding `tmp/goopg-bench-bin` is REFUSED while `ci/batch`/the
      SF=1 harness runs from it (`FORCE=1` does **not** waive this — it waives
      timing contamination, a different question), and `GOOPG_BIN` is
      env-overridable so a loop can build its own image. Verified by four direct
      guard calls (inactive / source-changed / image-only / unchanged) and two
      subset probes; the 2026-07-30 full gate below is its first real run. The
      **TPC-H** half of the same trap is untouched and is a NEW ledger row
      (2026-07-30): no `tpch-spotcheck.sh`, TPC-H bench script or `ci/batch`
      stage stamps engine provenance at all.
      Remaining real gap, filed as the other ledger row: `Join` still carries ONE
      key pair against PG's mergeclause **list**, so multi-key merge joins are
      now correct but less selective than PG.

> **Scope note (2026-07-29).** The four tasks below (**M0125-0012 … -0015**) adopt
> the last four TPC-DS defects that had **no owning task** — they existed only as
> deferral-ledger rows, which M0119 consumes as a *backlog*, not as a schedule.
> `docs/milestones/0125-*.md` previously listed Q47/Q49/Q51 under **Out of scope**
> with "all get a ledger row from M0124-0003 so they are not orphaned"; that was
> written before M0124-0001 measured the full SF=1 board and before the ledger
> row `tpcds-round2 q47-q49-q51` established they are **three distinct defects**
> ("All three are separate fix_plan items, **not one**"). The milestone doc is
> updated in the same commit. Q8 was never in that out-of-scope list — it simply
> had no task at all, despite being the sole unresolved member of round 1's
> nine-error set.
>
> **Gate visibility, measured — not assumed.** An earlier draft of this note
> claimed all four are visible to "the SF0.5 gate and the nightly anchors". Only
> half of that is true, and the same commit that filed these tasks records
> M0125-0011's measured negative result that a row-count change is *not*
> automatically detectable. Checked against
> `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260729-093056.txt` (HEAD)
> and `ci/batch/tpcds-row-anchors.csv`:
>
> | | SF0.5 gate at HEAD | nightly anchors |
> |---|---|---|
> | Q8 | **sees it** — `ERROR 12s` | **absent** |
> | Q47 | **sees it** — `MISMATCH 43s goopg=0 oracle=100` | **absent** |
> | Q49 | **PASS 25 rows, ck matches** — blind | **absent** |
> | Q51 | **PASS 100 rows**, `ck=n/a` — blind | **absent** |
>
> The anchor CSV pins 61 queries and contains **none of these four** (it does
> contain `Q75`, which is why that one is a live CI break and these are not).
> Closing any of them therefore requires **adding** an anchor, not re-pinning one.
>
> **Q49 and Q51 flipped MISMATCH → PASS at SF0.5 when M0125-0009 landed**
> (`sweep-20260729-004730` at `7a7a2639` vs `sweep-20260729-033758` at
> `3fbce36a`); no completion note or ledger row records that. **Neither has been
> re-measured at SF=1 since**, so the first step of both tasks is a measurement,
> not a fix — see -0014 / -0015.
>
> None of the four depends on M0124-0005's checksum column for *acceptance*
> (each differs in rows or errors), but Q47 and Q51 carry `ck=n/a` in the SF0.5
> oracle (a `LIMIT` over a non-total `ORDER BY`), so SF0.5 cannot value-accept
> them either — value acceptance is SF=1 only. All four are planner/executor
> changes, so `make plan-diff` applies with M0125-0004's `r5-default` fallback
> until M0124-0002 lands the `tpcds-round2-head` label.
>
> **File order is NOT work order.** The recommended sequence is in the
> milestone's "Interleaving rule": **-0014 / -0015 (measure first, may close
> outright) → -0013 → -0006 / -0007 → -0012 → -0008**. In particular
> **M0125-0012 has a soft dependency on M0125-0001** — if the driver has not
> landed, take another item rather than hand-rolling a fifth walker.

- [x] **M0125-0012 — Q8: a `ColumnRef` below a FROM-subquery `Project` keeps its
      OUTER-scope index** (round-1 §4.2 fix #8, ledger row `tpcds-round2 Q8`
      2026-07-27). **The only unresolved member of round 1's nine goopg-only
      errors**, and the sole `ERROR` in the SF=1 sweep that is not Q75.
      Measured `ERROR 26 s` at SF=1 against PG's `OK 0 s / 0 rows`
      (`analysis/tpcds-sf1-resweep-20260728/RESULTS.md` row 8):
      `column ref ca_zip/57 out of MaterializedSlot range 1`; **server survives**
      (verified `select 1` after) so this is contained, not a crash.
      **Cheap reproduction: SF0.5 reproduces the identical ERROR in 12 s**
      (`tpcds-results-sf05/sweep-20260729-093056.txt`) against 26 s + EXPLAIN at
      SF=1 — iterate there.
      **What already landed, so it is not re-attempted:** `9ddbc679`'s
      `containsSetOp` guard protects `pushdown.go:241`, `pushdown.go:264` and
      `planner.go:2078` — it never protected the remap path, which is why Q8
      kept failing after it. `9740fce9` then gave `buildBindingsPosMap`
      (`internal/planner/bushy.go`) its `SetOp`/`RecursiveUnion`/`WorkTableScan`/
      `WindowAgg`/`ProjectSet`/`OrdinalityWrap`/`RowsFrom`/`IndexOnlyScan`
      opaque-leaf arms plus a decline-on-unknown `default:`, and bounds-checked
      `*MaterializedSlot`/`*Slot` in `evalExprSlot`
      (`internal/executor/expr.go:353`) — that pair is what turned the panic
      into a contained `XX000`.
      **Mechanism of the residual (already root-caused, do not re-diagnose):**
      `ca_zip` = 57 is a **global FROM-order** index reaching a **1-column**
      `MaterializedSlot` — the INTERSECT-in-FROM subquery's own `Project` scope,
      which `buildBindingsPosMap` never governs. `remapSubqueryColumnRefs`
      (`internal/planner/planner.go`) rewrites only `Project` **targets**; a
      `ColumnRef` inside a `Filter` predicate *below* that `Project` is left
      holding the outer-scope index.
      **Fix direction:** extend the subquery-scope remap past `Project` targets
      to `Filter.Predicate`, `Join.LeftKey/RightKey` and
      `Aggregate.GroupExprs/Aggs`. **⚠ Compose with M0125-0010, do not revert
      it** — that task deliberately narrowed the pass to *verify-then-repair*
      (leave a target whose index is in range AND names the right column;
      re-derive only the out-of-range / wrong-name leakage signature). Every new
      node kind must keep the verify-first branch or this becomes the **fourth
      recurrence** of "ambiguous key resolved by taking the first match" — the
      fifth *instance*, on the numbering `2e09250b` established when it called
      M0125-0010 the "Third recurrence, after M0097-0003/-0032 and M0125-0009".
      Prefer routing the new arms
      through **M0125-0001**'s driver if it has landed; hand-rolling a fifth
      walker is exactly the copy-paste family M0125-0001/-0002 exist to delete.
      **⚠ Acceptance trap — "0 rows" is NOT an acceptance criterion.** PG
      returns **0 rows** for Q8 at SF=1, so any bug that yields an empty result
      also "matches PG". Accept instead on: (a) no `ERROR`, (b) rows = PG's 0,
      **and (c)** a discriminating probe — the same INTERSECT-in-FROM shape with
      predicates relaxed until PG returns a non-empty set, asserted equal by
      value on both engines. **The probe is only valid if it FAILS on pre-fix
      HEAD** with the same `column ref … out of MaterializedSlot range` error:
      relaxing predicates can change the plan into one that never touches the
      defective path, and a probe that already passes proves nothing. (Repo
      precedent: M0125-0011's gate fails 5/5 subtests pre-fix; root-0036's fails
      7 of 8 with the hunk stashed.) Add the probe as a planner/executor unit
      test so the discriminator outlives the task.
      Repro: `scripts/tpcds-bench-compare.sh 8`.
      Design `docs/design/0125-0012-q8-subquery-scope-index-remap.md`.
      Planner/executor change → full pre-commit bar (`tpch-spotcheck.sh`, SF0.5
      gate, `make plan-diff`).
      **DONE 2026-07-29.** ⚠️ **The "Mechanism of the residual … do not
      re-diagnose" paragraph above is REFUTED** — keep it only as the record of
      what was believed. Measuring the planned tree shows the outer `Filter`'s
      `ca_zip` index (57 at SF=1, 9 in the replica) was **already correct**, and
      no `Filter` below the subquery `Project` carried a stale ref at all. The
      failing ref is the V1 subquery's **own `Project` target** above the
      1-column `SetOp`, which `remapSubqueryColumnRefs` had numbered correctly
      as `ca_zip/0`; `applyJoinTreePosMap` (`bushy.go`) then descended into that
      `Project` — it stopped only at `IsolatedScope` wrappers — and rewrote the
      target with the OUTER bindings' posMap, which matched the outer binding
      that also starts at 0 (`store_sales`) and returned its **MHJ-reordered**
      offset. So 57 is a join-order offset, not the "global FROM-order index"
      the ledger read it as. Fix = stop at every join-tree `Project`, matching
      `buildBindingsPosMap`'s `collect` ("Extend it to all Projects … and
      stop") — the build half had been generalised and the apply half had not
      (Hard-won Rule #2). **D1/D2/D3 were not implemented**: no fifth walker was
      hand-rolled and M0125-0010's verify-then-repair narrowing is untouched.
      Acceptance trap honoured: a **non-empty** doll-house probe verified
      byte-identical on PostgreSQL 18.3 (`alpha|5`, `beta|7`), shipped as
      `internal/planner/q8_subquery_scope_posmap_test.go` (structural: every
      `Project` target addresses its own child) and
      `internal/executor/q8_subquery_scope_remap_test.go` (values), **both
      proved to FAIL pre-fix** with `ca_zip/6 … out of MaterializedSlot range 1`,
      plus a shape guard that skips loudly if the defective plan stops being
      produced. **Residual deferred (ledger 2026-07-29):** Q8 leaves the ERROR
      class and joins the **timeout class** — the `substr` residual is evaluated
      on a CROSS join above the full three-way MHJ with `d_qoy`/`d_year`
      unpushed, so at SF0.5 it exceeded a 1500 s client budget (elapsed 1633 s)
      where it previously errored at ~11 s. Pre-existing plan-quality defect, not a regression: the fix changes
      one `ColumnRef` index and no plan shape.
- [x] **M0125-0013 — Q47: a SECOND defect downstream of the CTE body RC-1b
      repaired** — **DONE 2026-07-30.** The premise was wrong in a precise and
      instructive way: the second defect is NOT above the CTE, it is in the CTE
      body, in the half RC-1b never measured. RC-1b checked the body by ROW
      COUNT and the row count was already right — goopg's 4-way join produces
      **332,240 rows, exactly PG's**, with every join predicate and filter
      correct. Only the PROJECTION read the wrong columns (`i_category` returned
      `s_store_sk`, `d_year` returned `s_county`, `d_moy` returned `s_zip`). So
      `GROUP BY i_category` grouped on `s_store_sk` (6 groups vs 11), 6-column
      and 4-column `GROUP BY` both collapsed to the same 29,617, and
      `rank() over (partition by …)` partitioned on columns unique per row —
      making **`rn` 1 for every row** (PG: 1..14) and `v1.rn = v1_lag.rn + 1`
      the unsatisfiable `1 = 2`. The windowed self-join layers were never
      broken; they were fed a permutation. Root cause: `buildBindingsPosMap`'s
      `*MultiHashJoin` arm matched only bare scans, so a leaf wrapped in a
      `*Filter` by `pushSingleSourceFiltersIntoMHJTables` (Q47's multi-column OR
      disjunction, pushed into `date_dim`) was skipped silently — recording no
      entry AND never advancing `off`. Design
      `docs/design/0125-0013-mhj-posmap-filtered-leaf.md` (indexed); 2 regression
      tests verified RED with the fix reverted; 3 ledger rows.
      **Q47 now returns exactly 100 rows = the SF0.5 oracle.**
      **STILL OPEN, split out as bookkeeping:** the STEP-0 runtime verdict
      (set A `OK 17 s` → HEAD `OK 142 s`) — no timing on this host can settle
      it, see the 2026-07-30 ledger row. Original text follows.

- [ ] **M0125-0013 (bookkeeping half) — Q47's 8.4x runtime verdict**
      (ledger rows `tpcds-round2 q47-q49-q51` 2026-07-29 and M0125-0013
      2026-07-30; §13.4 item 2). The ROW defect is closed — Q47 returns 100
      rows = oracle as of 2026-07-30. What remains is purely a documentation
      contradiction about its RUNTIME, and it is a bookkeeping repair of
      M0124-0001's deliverable, not engine work.
      set A `OK 17 s` → HEAD `OK 142 s` (8.4x, reproduced standalone at 143 s,
      `analysis/tpcds-sf1-resweep-20260728/diag-q47-rerun.txt`). Two primary
      sources disagree and this item should close the gap: `RESULTS.md` chunk
      49–56 and the RC-1b ledger row read it as the **expected cost** of newly
      non-empty input ("14s->143s confirms real work"), while the merged
      deliverable `analysis/tpcds-sf1-goopg-20260728.md` §3.2/§6 still calls it
      "bounded but **unattributed**" (it reproduces chunk 41–48's superseded
      reasoning). Record the verdict in whichever document is wrong.
      **Needs a QUIET host** (`pgrep -af run-nightly.sh` first): every timing
      taken on 2026-07-30 was under the nightly CI batch at load ~10. Take the
      `EXPLAIN` diff against set A's plan BEFORE any further plan-shape change,
      since the comparison is only interpretable while the plan is unchanged.
      Structural evidence now favours the RC-1b reading — post-fix `v1` yields
      54,915 groups (was 29,617 mis-grouped) and `rank()` yields 14 distinct
      values (was 1), so `v2`'s three-way self-join does strictly more real
      work than any pre-RC-1b measurement — but that is an argument, not a
      measurement, so the item stays open.
      **↳ NEW DATAPOINT 2026-07-30 (full SF0.5 gate,
      `analysis/m0125-sf05-fullgate-20260730/`): Q47 went `MISMATCH → TIMEOUT`.**
      The row fix registered exactly as predicted below, and the runtime this item
      is about now exceeds the gate's 300 s cap at SF=**0.5** — half the data, more
      than twice the SF=1 standalone reading of 142 s. That is a second
      independent sighting of the 8.4× and it pushes the interpretation toward
      "the plan is wrong", not "the work is real", but it is still not the timed
      EXPLAIN comparison this item asks for. Q47 is now the 16th member of
      M0125-0026's capture set; take its reading there rather than duplicating it.
      **Accept by VALUE**: Q47 = 100 rows = PG **and** values equal PG at SF=1.
      The SF0.5 gate sees the row gap today, so it will register the fix; the
      **nightly anchors will not** — `ci/batch/tpcds-row-anchors.csv` pins 61
      queries and contains no Q47 row, so closing this means **adding** an anchor,
      not re-pinning one.
      Design `docs/design/0125-0013-q47-q49-q51-three-distinct-defects.md` (§ Q47).
      Planner/executor change → full pre-commit bar.
- [x] **M0125-0014 — Q49: re-measure at SF=1, then resolve or classify the
      30-vs-34 row gap** — **DONE 2026-07-30, CLOSED AT STEP 0 as
      *measured-and-already-fixed*.** goopg at SF=1 on HEAD `f3f31d87` returns
      **34 rows = PG** with an **identical value checksum** (`63ace0d888e86982`;
      `tpcds-value-diff.py` = `RENDERING-ONLY (numeric scale)`), in `OK 83 s`.
      The `OK 79 s / 30` reading this item was written against is superseded.
      Anchor `Q49,34,pinned` added, and Q49 added to `stage-tpcds.sh`'s sweep
      order — **but see the M-NIGHTLY item above: the TPC-DS anchor mechanism is
      dead, so the anchor is inert until that lands.** No mechanism was ever
      identified; the candidates below are retired unanswered, not solved.
      Evidence `analysis/m0125-0014-0015-q49-q51-sf1/`; design doc § Q49
      "Execution record"; ledger rows updated (`tpcds-round2 Q49` → resolved). (round-2 §5a, ledger rows `tpcds-round2 Q49` 2026-07-27
      and `tpcds-round2 q47-q49-q51` 2026-07-29). Shaped like M0124-0004
      ("recover or classify") because the premise moved after it was written.
      **Unchanged by RC-1b, which disproves its provisional RC-1b-family
      attribution** — a second defect, not a variant of Q47's.
      **⚠ STEP 0 — the SF=1 number is stale.** Q49 flipped `MISMATCH 24/25` →
      `PASS 25` at SF0.5 the moment **M0125-0009** landed
      (`sweep-20260729-004730` at `7a7a2639` vs `sweep-20260729-033758` at
      `3fbce36a`; HEAD's `sweep-20260729-093056` still PASSes, checksum matching),
      and **nobody recorded that** — no completion note, no ledger row. Q49 has
      **not** been re-measured at SF=1 since -0009/-0010/-0011 landed. Measure it
      first: if SF=1 now returns 34 = PG by value, this task closes as
      *measured-and-already-fixed* and its ledger rows get an UPDATE naming
      M0125-0009 — a legitimate completion, not a skipped one. Only if the gap
      survives does the diagnosis below apply.
      Shape: three `UNION ALL` branches (web / catalog / store), each ranking a
      derived ratio and filtering `return_rank <= 10 or currency_rank <= 10`.
      goopg returns exactly **30** — suspiciously exactly 3 × 10, which is what a
      collapse of the two-rank `OR` into a single rank filter would produce.
      **Ruled out by probe, do not re-test:** `rank()` peer-tie handling.
      `rank`, `dense_rank` and `row_number` over a tied `ORDER BY` are
      byte-identical to PG (`1,1,3,3,3,6` on both), so the `<= 10` filter is not
      silently degrading to `row_number` semantics.
      **Remaining candidates** (§5a): (1) the `decimal(15,4)` division producing
      `return_ratio` / `currency_ratio` — a precision difference reorders ties and
      changes which rows sit at rank 10; (2) the mixed
      `store_sales sts LEFT OUTER JOIN store_returns sr … , date_dim` shape, i.e.
      the outer-join-plus-comma-join form §2.3 flags as unverified.
      **The cheap SF0.5 reproduction is GONE.** §5a's one-row gap (24 vs 25) was
      the recommended bisect target; it no longer reproduces (see STEP 0), so
      SF0.5 is a *regression* gate for Q49 now, not a *detection* gate — the same
      distinction M0125-0011 established by measurement. Any new minimal repro
      has to be constructed, not inherited.
      **Accept by VALUE**: 34 rows = PG and values equal PG **at SF=1**.
      "25 rows at SF0.5" is NOT an acceptance signal — HEAD already satisfies it.
      No Q49 row exists in `ci/batch/tpcds-row-anchors.csv`, so add one on close.
      Design `docs/design/0125-0013-q47-q49-q51-three-distinct-defects.md` (§ Q49).
      Planner/executor change → full pre-commit bar.
- [x] **M0125-0015 — Q51: re-measure at SF=1, then resolve or classify the
      0-vs-100 row gap** — **DONE 2026-07-30, CLOSED AT STEP 0 as
      *measured-and-already-fixed*.** goopg at SF=1 on HEAD `f3f31d87` returns
      **100 rows = PG** with an **identical value checksum** (`443e242cfab22c02`;
      `tpcds-value-diff.py` = `RENDERING-ONLY (bpchar/width)`), in **`OK 47 s`**
      — the runtime this item required be reported. **The budget-marginality
      warning is retired**: 553 s of headroom under the 600 s cut, not 13 s, so
      Q82 (44 s) is again the sweep's narrowest `OK` margin. The 587 s → 47 s
      drop is most likely the `LIMIT 100` finally saturating rather than a speed
      fix — consistent, unverified, not claimed. Anchor `Q51,100,pinned` added
      (inert, same caveat as -0014). "Third distinct defect" is now a **dropped**
      question, not an answered one — no mechanism was ever found.
      Evidence `analysis/m0125-0014-0015-q49-q51-sf1/`; design doc § Q51
      "Execution record"; ledger row `tpcds-round2 q47-q49-q51` updated (stays
      open for Q47). (ledger row `tpcds-round2 q47-q49-q51` 2026-07-29,
      §13.4 item 2). Same "resolve or classify" shape as -0014, for the same reason.
      **Also unchanged by RC-1b.** §13.4 item 2 calls it "a **third** distinct
      defect" on that basis, but the later measurements deliberately kept the
      question open — `RESULTS.md` chunk 49–56 says its RC-1b family membership
      "stays **probable, unproven**", and the M0124-0001 ledger row says "**either
      Q51 is a different defect or it shares Q47's downstream one**". Treat
      "distinct" as the leading hypothesis, not as settled. No mechanism is
      claimed; §13.3 records only the shape, "a wrong answer that had been hiding
      behind a timeout", the same shape M0124-0004 names for Q35.
      **⚠ STEP 0 — the SF=1 number is stale, exactly as for -0014.** Q51 flipped
      `MISMATCH 0/100` → `PASS 100` at SF0.5 when **M0125-0009** landed, and still
      PASSes at HEAD; unrecorded anywhere. **Re-measure at SF=1 against
      M0124-0001's sweep row (`OK 587 s / 0` vs PG `OK 1 s / 100`) BEFORE assuming
      a mechanism** — one ~590 s observation may close this task outright.
      Note SF0.5 cannot value-accept it either: its oracle entry is
      `51|OK|100|n/a|1`, i.e. `ck=n/a`.
      **⚠ Budget-marginal on the `OK` side — 13 s of headroom under the 600 s cut,
      the NARROWEST `OK` margin of the sweep (Q82's 44 s is the next-narrowest).**
      Any fix that
      adds work can flip it to `TIMEOUT` and *mask* a correct row count. Time it
      explicitly and report the runtime alongside the rows; if it crosses the
      budget, raise the budget for the acceptance run rather than declaring the
      fix a regression (`analysis/tpcds-sf1-goopg-20260728.md` §5).
      **Accept by VALUE**: 100 rows = PG and values equal PG **at SF=1**, with the
      measured runtime recorded. "100 rows at SF0.5" is NOT an acceptance signal —
      HEAD already satisfies it. No Q51 row exists in
      `ci/batch/tpcds-row-anchors.csv`, so add one on close.
      Design `docs/design/0125-0013-q47-q49-q51-three-distinct-defects.md` (§ Q51).
      Planner/executor change → full pre-commit bar.
- [x] **M0125-0023 — TPC-DS Q95 UNDER-counts: two `IN (subquery)` over the same
      CTE drop rows PG keeps** (discovered 2026-07-30 by M0125-0007's acceptance
      probe; ledger row same date). With the unpadded-date defect gone, Q95
      returns **`5 | 11180.00 | -6205.20`** at SF0.5 where PG returns
      **`23 | 45031.03 | -1282.36`** (goopg ck `663cec31dac6449c`, oracle ck
      `e498634c02595c29`). It is **not** M0125-0008: Q95 contains no `EXISTS` at
      all, and it loses rows rather than gaining them. Its two gates are
      `ws1.ws_order_number in (select ws_order_number from ws_wh)` and
      `ws1.ws_order_number in (select wr_order_number from web_returns, ws_wh
      where wr_order_number = ws_wh.ws_order_number)`, both over the same
      self-joined CTE `ws_wh` — so the suspects are IN-subquery → semi-join
      conversion when the inner side is a CTE reference, and CTE re-evaluation
      across two references in one WHERE. **First step is the isolation matrix**
      M0125-0008 already demonstrates the value of: run the base joins alone,
      then `+ first IN`, then `+ second IN`, on BOTH engines, and find which
      conjunct first diverges — the monotonicity direction (goopg ⊆ PG here)
      says a predicate is over-restricting, the opposite of Q94's.
      **Accept by VALUE**: Q95 = `23 | 45031.03 | -1282.36` at SF0.5 (checksum
      equal to the oracle), plus the isolation matrix as a planner/executor test.
      Design `docs/design/0125-0023-in-subquery-over-cte-under-count.md`.
      **↳ CLOSED 2026-07-30 by M0125-0008's fix — this item's premise was
      wrong.** "It is not M0125-0008: Q95 contains no `EXISTS` at all" tracked
      the KEYWORD, not the mechanism. `IN (subquery)` unnests to the very same
      `JoinTypeSemi`, so Q95's two `IN` gates over one outer relation are exactly
      the stacked semi/anti shape M0125-0008 describes; it under-counted rather
      than over-counted only because the neutered conjunct was a SEMI join
      (which then admits everything downstream of it) rather than an ANTI join.
      No separate design doc was written — the mechanism is documented in
      `docs/design/0125-0008-semi-anti-conjunction-residual.md` §5. Q95 now
      **PASSES the SF0.5 oracle's exact checksum `e498634c02595c29`**, which is
      this item's own stated acceptance bar. Lesson recorded because it cost a
      filed task: classify a subquery defect by the JOIN the planner builds, not
      by the SQL keyword that produced it.
      Planner/executor change → full pre-commit bar (`tpch-spotcheck.sh`, the
      SF0.5 gate, `make plan-diff LABEL=tpcds-round2-head`).

- [x] **M0125-0026 — the 15-query timeout class: capture both engines' EXPLAIN,
      classify, and file the planner fixes** (added 2026-07-30 by the USER;
      design `docs/design/0125-0026-timeout-class-plan-comparison.md` — a work
      plan, deliberately not a fix design).
      **↳ DONE 2026-07-31 (`analysis/m0125-0026-timeout-plans/`).** All three
      acceptance conditions met: **18 queries × 3 arms captured and committed**
      (12 hard warm-gate members + Q72 + the four the size fallback answers +
      Q18), the **classification table with per-class arithmetic** is in that
      directory's `README.md`, and **six per-class tasks are filed as
      `M0125-0034` … `-0039`** with a proposed selection order. Results in one
      paragraph: two of the doc's five suspects are **REFUTED** — (b) join order
      without a cardinality signal, because `GOOPG_RELSIZE_FALLBACK=0` and `=2`
      give **byte-identical plans on all 18** once relations are ANALYZEd
      (`plan.go:580`), which is `M0125-0031`'s runtime finding reproduced at
      plan level; and (d) "a CTE referenced N times runs N times", because
      `cteScanOp` + `ctx.CTERowCache` materialize once per CTE name and replay
      (`executor.go:75-85`) — the N-fold EXPLAIN repetition is Stage-A
      clone-per-consumer labelling only. What the capture found instead:
      `Nested Loop (CROSS)` — a genuine Cartesian product — **14 times across 8
      of 18 queries, at every site where a join input is a subquery** (C1); and
      **2 of 68 `Filter:` lines sit on a `Seq Scan`** where PG puts them
      routinely, so `date_dim` is hashed at 73,049 rows instead of 71 (C2,
      RC-5 generalized). Three queries (Q5, Q18, Q67) could **not** be
      classified: goopg prints `*planner.SetOp` with no children, so their
      entire plan body is invisible — filed as `M0125-0037`(i), and it gates
      `M0125-0033` too. Not captured: TPC-H Q21 (ledger row 2026-07-31; the
      TPC-H clusters were down and standing them up is not host-independent, so
      it stays with `M0125-0032`). Original body follows.
      With the correctness class closed,
      the 15 SF0.5 timeouts (Q5 Q8 Q10 Q14 Q30 Q31 Q35 Q54 Q64 Q65 Q67 Q69 Q71
      Q78 Q81) are the ONLY remaining goopg-specific class — PG answers each in
      0–16 s on the same data and query text (`tpcds-results-sf05/oracle.txt`,
      last field), goopg exceeds 300 s on all 15, and **no artifact has ever put
      goopg's EXPLAIN next to PG's for any of them**.
      **↳ AMENDED AGAIN 2026-07-30 by M0125-0003's §D8 arm
      (`analysis/m0125-0003-sf05-relsize-20260730/`), which SUPERSEDES the
      sixteen-query amendment below: at `GOOPG_RELSIZE_FALLBACK=2` the class is
      THIRTEEN — `Q5 Q8 Q14 Q30 Q31 Q35 Q54 Q64 Q65 Q71 Q72 Q78 Q81`. Q10, Q47,
      Q67 and Q69 are ANSWERED by the fallback (40 s / 277 s / 157 s / 17 s) and
      need no root-cause class; `Q72` joins, and it is the most informative
      member of the whole list — the only query in either benchmark family where
      the fallback COSTS time (1.13×, 270 s → 305 s at a 900 s budget), so its
      two goopg plans side by side are the one available picture of the
      fallback's downside. Capture all three arms for the union (17 queries) if
      cheap, but classify against the thirteen: a root-cause class for a query
      the flag already fixes is wasted work, and `M0125-0005` is expected to
      make flag-on the default. **Q35 is now known NOT to be fixed by the
      fallback** (still `TIMEOUT` in both arms), which makes its RC-8 re-scan
      class this task's highest-value output — the follow-up task for it is
      filed off this classification (ledger row 2026-07-30).**
      **↳ Earlier amendment 2026-07-30 by the full gate run
      (`analysis/m0125-sf05-fullgate-20260730/`): the class is SIXTEEN queries —
      add `Q47`.** M0125-0013 fixed its row count (0 → 100 = oracle) and the
      query now does the real work, which does not fit the 300 s SF0.5 cap; the
      board is otherwise unchanged (`MISMATCH=0 CKMISMATCH=0 ERROR=0`), so the
      class grew by a *repaired* query, not a regression. Capture Q47 in all
      three arms with the rest, but do NOT pre-classify it as unbounded: its only
      completion reading is 142 s at SF=1 (M0125-0013's bookkeeping half), so a
      300 s cap on half the data is not obviously a hard cut, and 0124-0001 §D6
      forbids reporting budget-marginal and unbounded members as one class.
      Steps (all in the doc):
      **(1) capture** — plain `EXPLAIN`, execution FORBIDDEN (`EXPLAIN ANALYZE`
      on goopg is banned for this set: these are the queries that do not
      finish), three arms into `analysis/m0125-0026-timeout-plans/`: goopg
      default (`goopg-off/`), goopg `GOOPG_RELSIZE_FALLBACK=2`
      (`goopg-relsize2/` — shape-only, so it does NOT confound M0125-0003's
      owed timed study; it splits the 15 between "sizes alone fix it" and "the
      shape is wrong"), PG 18.3 (`pg/`, `psql -p 65438 -U ryo -d tpcds05`).
      Server via `bench/tpcds/server.sh start sf05` or the cgroup wrapper —
      **this step is host-independent and may be taken while the nightly holds
      the host** (nothing is timed). **(2) classify** each query against the
      doc's suspects — RC-8 rescan-per-outer-row (Q10/Q30/Q35/Q69/Q81
      candidates; Q35's measured 96,562 × 8.16 s ≈ 9.1 days is the class's one
      hard number, and `0124-0004` §D4's rule stands: `CacheMisses ≈ Calls` ⇒
      hashed SubPlan caching, not decorrelation), rows=1 join order (the arm-ON
      diff isolates it), RC-5 unfiltered MHJ costing (aggravator), CTE
      referenced N× = body repeated (Q31 ×6 / Q14 / Q64 / Q78 — check
      `internal/planner/with.go`), missing TopN pushdown through window/rollup
      (Q67/Q65). A query fitting none of the suspects is a finding.
      **(3) arithmetic** — per class, Q35-method estimates (outer_rows ×
      per-rescan cost, |build| × |probe|) from PG-side cardinalities showing why
      300 s is unreachable; one significant digit. **(4) file** one planner-fix
      task per CLASS as M0125-0032+ (numbering note at -0027: -0027 … -0031 are
      taken) with the plan evidence and acceptance =
      first-ever completion checked against the git-tracked oracle row (rows +
      ck where present; Q35's `35|OK|100|0` is already M0125-0003's acceptance
      row — coordinate, don't duplicate), and propose their selection order in
      the banner. Acceptance for THIS item: the 15×3 plan capture committed,
      the classification table with per-class arithmetic in the analysis
      README, and the per-class fix tasks filed. This item changes NO planner
      code, so the plan-shape bar does not apply; the fix tasks it files
      inherit the full bar (plan-diff `LABEL=tpcds-round2-head`, timed TPC-H on
      a quiet host, full SF0.5 gate).

- [ ] **M0125-0027 — the SF=1 harness reports a DEAD SERVER as `OK`**
      (found 2026-07-30 while validating M0125-0011's provenance follow-up;
      ledger row 2026-07-30. **Numbering note (updated 2026-07-30): `M0125-0028`
      … `-0031` are taken by the warm-statistics programme (user directive —
      see below), so M0125-0026's per-class planner tasks AND M0125-0031's
      evidence-filed fixes both file from `M0125-0032` onward; whichever files
      first takes the next free id. **`-0032` (TPC-H Q21, shape class) and
      `-0033` (TPC-DS Q18, warm regression) are now taken by -0031's two
      measurement motions — the next free id is `M0125-0034`.**) `scripts/tpcds-bench-compare.sh:138` ends its status ladder in a
      catch-all `else status="OK"; rows=$(wc -l <<<"$qout")`. A psql that never
      connected emits no `ERROR:`/`FATAL:` prefix and no `(N rows)` block, so it
      falls into that arm and is recorded as **`OK`, with the line count of the
      connection-error text as its ROW COUNT.** Measured, not reasoned:
      with nothing listening on 65436, `scripts/tpcds-bench-compare.sh 99`
      printed `goopg Q99  OK  0s  2` while
      `bench/tpcds/runtime_goopg/tpcds-results/goopg_q99_result.txt` contains
      only `psql: error: connection to server at "127.0.0.1", port 65436
      failed: Connection refused`. This is the same class as 0124-0001 §D6a ("a
      matching row count is not agreement"), one step worse: **no execution at
      all can present as a successful cell**, and this is the harness that
      produced M0124-0001's SF=1 board, which every M0125 SF=1 reading cites.
      **Fix**: `qex` is already captured — a non-zero exit that is neither 124
      nor an `ERROR:`-bearing output must not be `OK`. Add a distinct status
      (`NOCONN`/`UNKNOWN`) carrying `qex` and the first output line, and keep the
      catch-all `OK` only for `qex == 0` (a 0-row `\d`-style output legitimately
      has no `(N rows)` block). The SF0.5 gate does NOT share the bug — its
      ladder ends in a checksum/row-count comparison against the oracle, and a
      connection failure there mismatches — but it detects it only as a
      MISMATCH, so give it the same explicit arm.
      **Also owed as verification, not assumption**: re-read
      `analysis/tpcds-sf1-goopg-20260728.md` / `analysis/tpcds-sf1-resweep-20260728/`
      for `OK` cells with a suspiciously tiny row count at ~0 s — the signature
      of this defect — before any further SF=1 claim leans on that board. It is
      NOT yet known whether any published cell is affected.
      Design: extend `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md`
      (§D4a's neighbourhood — report integrity), no new doc.
      Harness-only change → shell syntax + a direct probe with the server down
      (the exact reproduction above) + one with it up.

- [x] **M0125-0028 — `ANALYZE <table>` resolves in the connection's database,
      not the shared catalog** — **DONE 2026-07-30 (loop #8).**
      `expandAnalyzeTargets` + VACUUM's named-target twin + `relationStillExists`
      resolve per-connection (`ctxPlanCatalog`/`LookupTableByOIDAllDBs`); fact (b)
      is FIXED not ledgered — bare `ANALYZE;` now covers every relation of the
      current database via new `catalog.UserTableHandles` (live handles;
      other-session temp + non-owned skipped silently like upstream). 3 pins in
      `internal/executor/analyze_dbid_routing_test.go`, each proven to fail
      pre-fix (42P01 / silent no-op / silent skip). Acceptance flipped:
      `probe-analyze` → `ANALYZE lineitem` in db `tpch` OK, reltuples=5,997,241,
      visible to a second session (see design §-0028a — the 2026-07-23
      cross-connection symptom did NOT reproduce; -0029 gap 3 re-verify, don't
      assume). Gates: units suite PASS, tpch-spotcheck PASS (Q12=2/Q13=35,
      33.0 s). 3 ledger rows: db-wide VACUUM still DefaultDBOid + deep-copy
      (stats writes lost), bare ANALYZE can't reach heap system catalogs,
      `VACUUM <missing>` should be 42P01. Original body follows.
      (USER DIRECTIVE 2026-07-30(b), item 2 of 4;
      design `docs/design/0125-0028-warm-stats-programme.md`; closes ledger row
      `bench-reorg ANALYZE-scope` 2026-07-27). In db `tpch`,
      `SELECT count(*) FROM lineitem` returns 5,997,241 while `ANALYZE lineitem`
      raises 42P01 `relation "lineitem" does not exist`: `expandAnalyzeTargets`
      (`internal/executor/operators_analyze.go:145`) calls `cat.LookupTable(name)`
      directly instead of the per-connection DB-scoped resolution SELECT uses
      (the `DBName→Context` threading). This is also why HammerDB's final
      "GATHERING SCHEMA STATISTICS" step fails, and it is the FIRST of the two
      measured blockers that made M0125-0003's W1/W2 control arms
      unconstructible (ledger row 2026-07-30 — this task is its named resume
      point). While in the file, resolve fact (b) of the design doc: bare
      `ANALYZE;` returns `nil` targets ("catalog-wide form not supported yet",
      `operators_analyze.go:169-177`) despite a docstring claiming upstream
      parity — with per-DB resolution in hand, implement PG's
      every-table-in-the-current-database semantics or write the explicit
      ledger row; a silent no-op with an upstream-parity docstring is not a
      permitted end state. Acceptance: regression test (CREATE DATABASE d →
      CREATE TABLE + rows in d → `ANALYZE <name>` in d succeeds and populates
      stats); `scripts/tpch-relsize-arm.sh probe-analyze` flips from
      *relation does not exist* to populated `RowCount` on the bench cluster.
      Engine change: units suite + `scripts/tpch-spotcheck.sh`.

- [x] **M0125-0029 — statistics survive a restart, for EVERY database, visible
      to EVERY connection** (USER DIRECTIVE 2026-07-30(b), item 1 of 4 — with
      an explicit waiver: this persistence does NOT have to be PG-spec-
      faithful; design `docs/design/0125-0028-warm-stats-programme.md` §-0029).
      Three measured gaps close together because acceptance is end-to-end:
      **(1) per-DB routing** — `persistStatsToPGStatistic`
      (`operators_analyze.go:184`) hardcodes `DBOid: catalog.DefaultDBOid` and
      `loadStatisticsFromHeap` (`internal/initdb/open.go:3479`) reads only
      `cat.DBOID()`, so per-DB tables (`tpch`/`tpcds`/`tpcds05`) never round-trip;
      **(2) the size itself** — the startup reload restores per-column stats
      but `RowCount`/`Pages` stay 0 by design (ledger `pq-P6`: pg_statistic has
      no reltuples slot, and goopg's `pg_class` is VIRTUAL — `reltuples` is
      rendered live from `t.Stats.RowCount`, `catalog.go:6977` — so PG's home
      for the count does not exist here; THIS is where the user's waiver
      applies: persist RowCount/Pages via a goopg-private mechanism, e.g. the
      existing goopg-private catalog-DDL WAL record + startup-replay pattern,
      or a private row/fork beside the pg_statistic heap — decide and cite
      in-task; the PG-scannable pg_statistic rows themselves must stay
      PG18-canonical, additive only); **(3) cross-connection visibility** —
      ANALYZE in one connection was measured invisible to another connection's
      planning (2026-07-23, TPC-H bench server :65433, pre-rebuild; the record
      does not name the database) even though `SetTableStats` mutates the
      shared `catalog.Table`; suspected mechanism is the per-connection
      materialization of per-DB catalog views — root-cause and fix, because
      restored-at-startup stats a new session cannot see are worthless.
      Acceptance (one probe, all three gaps): ANALYZE the 8 TPC-H tables by
      name in db `tpch` → restart → NEW connection → `pg_class.reltuples` > 0
      for all 8 AND an EXPLAIN row-estimate/join-order change proves the
      planner consumed them, with ZERO re-ANALYZE after restart; then
      `W_ARM_OK=1 scripts/tpch-relsize-arm.sh w1`/`w2` measures §D3's
      "flag-on == flag-off when ANALYZEd" invariant at last. That w-arm run is
      NOT executable today even with this task's engine fixes alone: the
      harness's `ARM_ANALYZE` performs no ANALYZE (it only gates on
      `W_ARM_OK`), and its guard text still demands a `cmd/tpch-runner
      -analyze` flag — post-this-task a documented one-time per-table psql
      ANALYZE before the run suffices (stats durable + globally visible), so
      IN SCOPE here: give the harness that step (or the documented pre-step)
      and retire the stale guard text. Relation to
      M0112: facts (1)/(2) live in M0112's landed code — this task repairs its
      routing and fills its gap under the waiver; M0112 stays open as the
      PG-faithful end-state and is NOT marked complete here. Durability rules
      apply: the restart probe runs on a durable-config cluster (no
      `--no-sync`/fsync-off). Gates: units + tpch-spotcheck + the restart
      acceptance above.

- [x] **M0125-0030 — bench clusters get warm statistics + CHECKPOINT: build
      scripts AND a one-shot warm-up of the standing clusters** (USER DIRECTIVE
      2026-07-30(b), item 3 of 4; DEPENDS ON -0028 and -0029; design
      `docs/design/0125-0028-warm-stats-programme.md` §-0030). Script changes —
      per-table `ANALYZE <name>` over the benchmark tables + `CHECKPOINT` after
      load: `bench/tpch/build_schema_goopg.sh` (HammerDB's own ANALYZE step
      starts working once -0028 lands — verify, don't duplicate; add the
      CHECKPOINT), `scripts/tpcds-load.sh` (SF=1; already claims
      "Schema + COPY + ANALYZE" — verify it actually populates under
      -0028/-0029), `scripts/tpcds-sf05-regression.sh load-goopg` (SF0.5).
      One-shot warm-up of the three standing clusters: 65433 (`tpch@tpch`,
      8 tables), 65436 (`tpcds`, 25 tables), 65437 (SF0.5, 25 tables) —
      ANALYZE each table by name, CHECKPOINT, then **restart and verify the
      stats survived** (that restart is -0029's acceptance probe in situ).
      All server starts through the cgroup cap / lifecycle scripts.
      **This is the commit where the measurement premise flips — record the
      consequences in the same commit:** (a) row-count gates must NOT move —
      Q12/Q13 spotcheck canonical counts and the SF0.5 oracle are
      statistics-independent, so any row change after warm-up is a real
      defect, never a re-pin; (b) plan baselines DO move — capture a new
      plan-diff label (`warm-stats-base`) immediately after the warm-up, and
      treat every prior label as a different stats regime; (c) timed baselines
      reset — every S-cold `analysis/` number stops being comparable, and
      analysis docs state their stats regime explicitly from here on.

- [ ] **M0125-0031 — the warm-stats planning line: eliminate the TPC-DS
      timeout class, then optimize and stabilize TPC-H/TPC-DS runtimes**
      (USER DIRECTIVE 2026-07-30(b), item 4 of 4; **GATED on -0028 … -0030**;
      design `docs/design/0125-0028-warm-stats-programme.md` §-0031). Goal
      state per the directive, under the warm-statistics premise -0030
      establishes: **(a)** the SF0.5 gate's goopg-only TIMEOUT class → **0**
      (baseline: **13** under the flipped default — `99d83714` §D8 measured
      `GOOPG_RELSIZE_FALLBACK=2` rescuing Q10/Q69/Q67/Q47 and Q72 joining,
      and `d4071df4` made that flag the default; the 16 at `e29faca9` is the
      superseded flag-OFF figure. Q4 stays excluded — the PG oracle itself
      times out — and Q36/Q70/Q86 stay dsqgen-artifact SKIPs); **(b)** TPC-H and
      TPC-DS runtime reduction and plan stabilization. **The first motion is a
      re-measurement, not a fix**: round-4 §2/§5 measured stats-ON fixing
      TPC-H Q5 22.8× while regressing Q22 128×, Q4 79×, Q8 53×, Q2 26×,
      Q12 4.4× — stream 12 % SLOWER; under the warm premise that trade-off is
      the default behavior, so re-run the warm TPC-H power sweep at HEAD
      (the planner has since gained int64 keys, the §2 veto, composite-NLI
      keep, and the relsize line — round-4's absolute numbers do not
      transfer) and fix what still regresses. Expect the timeout class to
      SPLIT: size-starved members may complete outright under warm stats;
      shape-class members (RC-8 rescan-per-outer-row, CTE×N re-execution,
      missing TopN pushdown — M0125-0026's suspects a/d/e) will not, because
      no cardinality input fixes a rescan-per-outer-row shape. File the
      concrete fixes from evidence as M0125-0032+ (shared numbering runway
      with -0026's per-class filings — whichever files first takes the next
      free id; coordinate with -0026's classification rather than duplicating
      it, and add the free warm arm to -0026's capture if it has not run yet).
      Every fix here is a plan-shape commit and inherits the full bar against
      the NEW label: plan-diff `LABEL=warm-stats-base`, timed 22-query TPC-H
      on a quiet host, full SF0.5 gate.
      **↳ FIRST MOTION DISCHARGED 2026-07-30 (loop #11) —
      `analysis/m0125-0031-warm-tpch-20260730.md`, design §-0031a.** The warm
      TPC-H sweep at HEAD ran all four §D5.1 arms on a quiet host (one binary,
      44 executions, per-query restart): **c1 693.8 → c2 494.0 → w1 413.3 →
      w2 420.1 s**, i.e. **warm is 1.18× FASTER than S-cold at the shipped
      default**, rows identical to the c-arms everywhere. **Round 4's trade-off
      does not exist at HEAD**: none of Q22/Q4/Q2/Q12 regresses and Q8 (its 53×
      loss) is the largest win at 8.5×; Q5's 22.8× win is gone because M0077
      already fixed Q5. So (b)'s TPC-H work is optimization, not repair, and it
      is scoped by measurement to **seven queries = 69 % of the stream**
      (Q5 60.2, Q9 52.7, Q4 41.9, Q18 37.2, Q15 34.9, Q7 33.3, Q17 30.7) plus
      Q21. Three findings that bind later loops: **§D3's invariant is MEASURED**
      (warm + `GOOPG_RELSIZE_FALLBACK=0` vs `warm-stats-base` = 22/22 MATCH in
      `structural` AND `strict-text` → the relsize line is an S-cold-only safety
      net, and §D5.1's W-arms + ledger row 594 are discharged); **the harness's
      single-run per-query noise band is ~±17 %** (identical plans moved 1.17×),
      so the table's "Q10 1.24× slower" is NOT a regression claim; and **Q21
      times out in all four arms** → shape class, filed as `M0125-0032`.
      **↳ GOAL (a) IS NOW MEASURED, AND IT IS NOT MET (2026-07-30, loop #12) —
      `analysis/m0125-0031-warm-sf05-20260730/README.md`, design §-0031b.** The
      warm SF0.5 gate ran all 99 queries on a quiet host (five chunks, ONE binary
      `fdd0c6e199182fbb`, private path so the nightly's shared bin was untouched,
      same arm `relsize=unset(2)` as the baseline): **PASS=83 TIMEOUT=12
      MISMATCH=0 CKMISMATCH=0 ERROR=0 SKIP=4** against the baseline's **PASS=82
      TIMEOUT=13**. The target is 0. The single status change is **Q72
      `TIMEOUT 307s` → `PASS 308s`, which is not a rescue** — both sit on the
      300 s cap and this arm's own 900 s standalone probe put Q72 at 305 s. The
      12 hard members (Q5 Q8 Q14 Q30 Q31 Q35 Q54 Q64 Q65 Q71 Q78 Q81) are
      IDENTICAL to the baseline's. **The predicted split split entirely to one
      side: ZERO members were size-starved.** Every member has now failed under
      three cardinality regimes — none, sizes-only, and full statistics with MCVs
      and histograms — so **no further cardinality work can move goal (a)**, and
      the whole remaining path is plan-shape work through `M0125-0026`'s
      classification and the per-class tasks it files from -0032+. Two
      by-products: **warm statistics change no answers** (all 82 common-PASS
      queries agree on row count AND value checksum, 50 ck-verified — the
      TPC-DS half of -0030's "row-count gates must not move"), and **one real
      warm regression, Q18 117 s → 251 s (2.1×)**, filed as `M0125-0033`.
      **STILL OWED by -0031: goal (a)'s outcome (0 timeouts) — but via -0026/-0032+,
      not via cardinality — and goal (b)'s actual fixes.** Both measurement
      motions are now discharged; what remains under this umbrella is repair.

- [ ] **M0125-0032 — TPC-H Q21 is the shape-class timeout: it survives BOTH
      cardinality regimes** (filed 2026-07-30 by M0125-0031's first motion;
      evidence `analysis/m0125-0031-warm-tpch-20260730.md` §4). Q21 exceeds every
      budget in **all four** §D5.1 arms — S-cold/off 612 s, S-cold/stage-2 672 s,
      WARM/off 381 s, WARM/stage-2 384 s (300 s cap) — with peak RSS 14.4 GB,
      i.e. it pins the 15 GB cgroup ceiling. Both cardinality inputs are therefore
      **ruled out as the fix**: relation sizes alone (c1→c2) and full statistics
      with MCVs/histograms (c2→w2) each fail to rescue it, which is exactly the
      split -0031 predicted for shape-class members. It is TPC-H's ONLY member of
      the timeout class. Next step: capture plain `EXPLAIN` (never `EXPLAIN
      ANALYZE` — banned for the timeout set) for Q21 at HEAD against PG 18.3 and
      classify the shape; M0077 already tuned Q21 once via `SourceTableIdx`
      (0→381 rows), so start from that note and from the RC-8
      rescan-per-outer-row / NOT EXISTS-over-`lineitem`-self-join suspects.
      Coordinate the classification with `M0125-0026` — Q21 is the TPC-H
      instance of the same question its capture asks of the 16 TPC-DS members,
      and one shared root-cause taxonomy is the point.

- [ ] **M0125-0033 — TPC-DS Q18 is 2.1× SLOWER under warm statistics** (filed
      2026-07-30 by M0125-0031 goal (a); evidence
      `analysis/m0125-0031-warm-sf05-20260730/README.md` §"Runtime"). At SF0.5,
      300 s cap, same arm (`GOOPG_RELSIZE_FALLBACK=2`), Q18 goes **117 s S-cold
      → 251 s warm**; its full history is `156 s` (relsize off) → `117 s`
      (relsize=2) → `251 s` (warm), i.e. **warm statistics cost Q18 more than the
      relation-size fallback ever won it**. It is the ONLY query outside the
      noise band moving the wrong way — removing it inverts the sweep's aggregate
      from "warm 2.7 % slower" to "warm 3.2 % faster" — and its answer is
      unchanged (100 rows, `ck=n/a` in both arms), so this is a pure plan-cost
      defect, not a correctness one. It also does not threaten the gate: 251 s is
      inside the 300 s budget. Next step: capture plain `EXPLAIN` for Q18 on the
      warm SF0.5 cluster and against the PG 18.3 oracle (`tpcds05` on :65438),
      and diff the goopg plan against a stats-blind capture — `M0125-0026`'s
      instrument already does exactly this for the timeout class, so add Q18 to
      its capture set rather than building a second harness. Q18 is also one of
      the seven TPC-H queries -0031(b) is scoped to (37.2 s there), so a single
      root cause may serve both benchmarks. Bar for the fix: plan-diff
      `LABEL=warm-stats-base`, timed TPC-H, full SF0.5 gate.
      **↳ CAPTURED 2026-07-31 by M0125-0026** (`goopg-warm/q18.txt`,
      `pg/q18.txt`). It did not yield a cause: goopg's whole Q18 plan is four
      lines because the body sits under an opaque `*planner.SetOp` node — this
      item is BLOCKED on `M0125-0037`'s EXPLAIN half.

<!-- The six items below were filed 2026-07-31 by M0125-0026's classification.
     Evidence for every one of them: analysis/m0125-0026-timeout-plans/README.md
     and the three plan arms beside it. Selection order is proposed in that
     README §"Step 4" and mirrored in the Current Priority banner. -->

- [x] **M0125-0034 — C1: goopg emits a Cartesian product whenever a join input
      is a subquery** (filed 2026-07-31 by M0125-0026; evidence
      `analysis/m0125-0026-timeout-plans/README.md` §"The dominant mechanism").
      `Nested Loop (CROSS)` (`planner.JoinTypeCross`) appears **14 times across
      8 of the 18 captured queries**, always with the equi-join predicate
      demoted to a `Filter` on the CROSS node or its parent — and at every one
      of the eight sites at least one input is a set operation
      (`*planner.SetOp`), a CTE reference, or a derived aggregate. Joins between
      two base relations are correct everywhere in the capture. PG produces a
      hash/merge/index join at all eight. Worst instances: Q64 crosses two full
      `date_dim` copies (7.3×10⁴ each) onto the fact stream (≈5×10¹⁴ row-pairs);
      Q54 crosses a ≈1×10⁶-row SetOp body with `item` and `date_dim` (≈1×10¹⁵).
      Members: **Q8 Q14 Q30 Q54 Q64 Q65 Q71 Q81** — the largest class, and the
      first fix to take. Suspected site: the join-order DP / qual-to-joincond
      conversion where an input is not a base relation
      (`internal/planner/joinorder.go`, `bushy.go`,
      `inner_join_qual_pushdown.go` already special-cases `*CTEScan`).
      Acceptance: **Q71 completes and matches the oracle row
      `71|OK|580|521a7af7…`** (rows + ck) — the least-deep member. Bar:
      plan-diff `LABEL=tpcds-round2-head`, timed 22-query TPC-H on a quiet host,
      full SF0.5 gate.
      **↳ THE SET-OPERATION ARM IS DONE (2026-07-31, loop #7).** Design
      `docs/design/0125-0034-setop-join-promotion.md`; tests
      `internal/executor/setop_join_promotion_test.go` (4); re-capture
      `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0034/`; artefacts
      `analysis/m0125-0034/`. **Acceptance MET: `Q71 PASS 580 rows
      ck=521a7af7606d10c1`** = the oracle row exactly. Three stacked defects,
      each sufficient alone: (1) `collectScanOutputNames` had no `*SetOp` case,
      so `allColumnRefNamesInScope` judged every conjunct spanning the set
      operation out of scope and `pushOneConjunct` declined **before reaching
      its own guard** — the primary cause, and a reminder that an
      under-enumerated permissive check fails as a silent missed optimisation;
      (2) M0097-0058's blanket `containsSetOp` bailout, whose premise is
      REFUTED here — `SetOp.Output()` IS the narrow schema and the executor
      pads a `leftWidth+rightWidth` keyRow before evaluating either join key,
      so `index out of range [57] with length 1` cannot arise at the join
      node; (3) uncovered by fixing the first two, `pickInnerScanForNLI`'s
      left-as-inner flip emits `outer ++ inner` and was **unreachable** for a
      set operation until the promotion was restored — Q71 planned
      `Append ++ item` while `sum(ext_price)` stayed bound to `item ++ Append`
      and errored `aggregate sum requires numeric argument in v0`. Measured:
      **30 `Nested Loop (CROSS)` nodes eliminated; Q5 Q8 Q14 Q54 Q71 all
      TIMEOUT → PASS** with oracle-identical rows AND checksums; the SF0.5
      timeout class goes **12 → 7**. Regression surface swept EXHAUSTIVELY
      rather than sampled — all 21 set-operation TPC-DS queries run, the 15
      non-rescued unchanged (`PASS=15 MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`, every ck equal to the 2026-07-30 full-gate baseline).
      Gates: units PASS; `tpch-spotcheck.sh` `RESULT=PASS` (Q12=2 Q13=35);
      plan-diff vs `warm-stats-base` 10/22 DIFFER but **every changed line is
      M0125-0039's column qualification** — zero structural change, so this
      task is TPC-H-plan-inert; re-pinned
      `plan_snapshots/m0125-0034-setop-join-promotion.txt`. The nightly CI
      batch held the host all loop (`load ≈ 9.9`), so **no second below is a
      timing** and the "timed 22-query TPC-H on a quiet host" half of the bar
      is NOT discharged; the full 99-query SF0.5 gate is likewise not re-run
      (the 21-query set-operation sweep is the complete reachable surface).
      **THIS ITEM STAYS OPEN for C1's other arm: Q30 Q64 Q65 Q81 still emit 8
      crosses** where the join input is a CTE reference or a derived aggregate
      (counts unchanged 1/4/2/1, all four still TIMEOUT). `collectScanOutputNames`
      already has `*CTEScan`/`*Aggregate` cases, so the name walk is NOT their
      blocker — start by capturing why `pushOneConjunct` declines on Q64.
      **↳ THAT STARTING POINT IS REFUTED (2026-07-31, loop #9,
      `docs/design/0125-0035a-preserved-side-restriction-descent.md` §6).** The
      shared arm was taken with -0035 as this banner directed, and it MEASURED
      the CROSS count rather than assuming it: `pushSingleSideQualsIntoInnerJoinInputs`
      now admits `JoinTypeCross`, and **zero of the 8 crosses moved.** They are a
      join-**ORDER** defect, not a qual-placement one — in Q64 the two crosses are
      `date_dim d2` and `date_dim d3`, whose equi-predicates
      (`customer.c_first_sales_date_sk = d2.d_date_sk` + the `d3` twin) are demoted
      to a `Filter` TWO levels above them because the enumeration places `d2`/`d3`
      BEFORE `customer`, the relation their predicate needs. `pushOneConjunct` is
      behaving correctly: a side-spanning conjunct is one it must never move.
      **Resume in `internal/planner/joinorder.go` / `bushy.go`**, starting with
      whether the 18-relation FROM list trips a collapse/greedy threshold and falls
      back to FROM order (PG's analogues: `join_collapse_limit` 8, `geqo_threshold`
      12). Ledger row 2026-07-31.
      Four ledger rows 2026-07-31 (this arm; the untouched `JOIN … ON` guard;
      the NLI flip declined-not-fixed, where the flipped shape is PG's OWN
      plan; the enumeration's remaining blind kinds).
      **↳ THE JOIN-ORDER ARM LANDED 2026-07-31 (loop #15).** Design
      `docs/design/0125-0034a-comma-from-connectivity-order.md`; tests
      `internal/planner/joinorder_connectivity_test.go` (8); evidence
      `analysis/m0125-0034b/`. **The resume point above named the right file
      and the wrong reason.** It asked whether an 18-relation FROM list trips a
      collapse/greedy threshold — it does (`tryBushyDP`: `len(tables) > 12`),
      but that is not what stranded Q30/Q81, whose lists hold THREE items. The
      actual cause is shared by all of them and sits in **both** join-order
      passes at once, neither of them deciding anything: `tryBushyDP` whitelists
      its leaves to `*SeqScan`/`*IndexScan`/`*MultiHashJoin` because
      `buildBindingsPosMap` keys on scan identity, and
      `reorderCommaFromByCardinality` bailed the instant a FROM item was not a
      base table carrying `Stats.RowCount` — and a WITH reference is not in the
      catalog. Nothing reordered these lists at all, so the source-order CROSS
      chain survived. Same shape as `M0125-0041`: a capability gap wearing the
      costume of a cost decision.
      **Fix:** separate two objectives one precondition had fused. A missing row
      count blocks *ranking* connected orders; it does not block telling a
      connected order from a disconnected one, and a Cartesian product is only
      ever the second question. New `orderByConnectivity` runs when the list
      holds a WITH reference and makes NO cost claim — ranking there would mean
      inventing a number rather than reading one (that is `M0125-0038`). Ties
      break on source order, which buys the property that bounds the blast
      radius: **a cross-free source order is a fixed point**, so the pass
      rewrites a FROM list *if and only if* the source order contains a cross
      the join graph could have avoided. The parser-level seam is what makes
      this safe — it permutes before column resolution, so no resolved
      `ColumnRef.Index` needs remapping, which is exactly the machinery whose
      absence forced `tryBushyDP`'s whitelist.
      **Measured:** full 99-query SF0.5 gate on one binary, three chunks —
      **`PASS=92 MISMATCH=1 CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4`**, timeout
      class **6 → 2** (`Q65`, `Q78`). Diffed cell by cell against loop #14's
      `sweep-20260731-121447.txt`: **exactly 4 of 99 cells moved, the other 95
      identical in status, rows AND checksum.** `Q30 TIMEOUT (at 300 s and at
      1200 s) → PASS 1 s, 31 rows, ck=f47a48499fd7e070`; `Q81 TIMEOUT → PASS
      1 s, 100 rows`. **Q72's TIMEOUT → PASS 309 s is explicitly NOT claimed** —
      Q72 has no `WITH`, so the mode cannot fire on it; it is the cap-straddler
      the banner already documents. TPC-H is inert **by construction, not by
      sampling**: the mode needs a name the catalog does not know and the TPC-H
      query set contains no `WITH … AS (` at all; `tpch-spotcheck.sh`
      `RESULT=PASS` (Q12=2 Q13=35, query-phase 32.2 s), units PASS.
      **Q64 went TIMEOUT → MISMATCH and that is a defect this arm EXPOSED, not
      one it caused** — measured, not assumed: at HEAD Q64 does not complete in
      **1848 s**, and an A/B differing only in where `customer` sits in the FROM
      list (one arm fires the pass, one is a fixed point so it declines) returns
      **byte-identical** wrong output. Three `date_dim` aliases collapse to one
      in projection resolution; filed as **`M0125-0044`**, a silent wrong
      answer, which this banner ranks above a timeout.
      ~~**THIS ITEM STAYS OPEN for Q65 only.** Its inputs are derived aggregates,
      not WITH references, and the parser accepts `LATERAL` and discards it
      (`internal/parser/select.go`), so nothing in the AST can prove a derived
      table uncorrelated and the pass must decline the whole list. Resume point:
      record laterality on `parser.RangeVar`, then admit non-lateral derived
      tables as opaque relations exactly as WITH references are. Ledger row
      2026-07-31.~~
      **↳ THE Q65 ARM LANDED 2026-07-31 (loop #17) — ITEM CLOSED.** Design
      `docs/design/0125-0034a-comma-from-connectivity-order.md` §7; tests
      `internal/planner/joinorder_connectivity_test.go` (now 12, four new);
      gate artefacts `analysis/m0125-0034c/gate/`. The resume point above was
      followed exactly: `parser.RangeVar.Lateral` is set at BOTH accept sites
      (`parseRangeVar` and the `JOIN LATERAL` path, which consumes the keyword
      before `parseRangeVar` can see it), and the blanket `rv.Subquery != nil`
      decline splits three ways — table functions still decline the whole list
      (in PG the LATERAL keyword is *noise* before a function item, so its
      absence proves nothing), `Lateral` derived tables decline, and a
      non-lateral derived table is admitted as an opaque relation forcing
      connectivity mode (PG rejects unmarked sibling references with "invalid
      reference to FROM-clause entry", so the unmarked form is provably
      independent — the same standing as a WITH reference). **Measured: Q65
      TIMEOUT → PASS 17 s S-cold, 100 rows = the oracle.** Full 99-query SF0.5
      gate, one binary, three chunks: **PASS=93 MISMATCH=0 CKMISMATCH=0
      ERROR=0 TIMEOUT=2 SKIP=4**; cell-by-cell vs loop #16, **exactly 2 of 99
      cells moved** — Q65's rescue, and Q72 PASS 309 s → TIMEOUT 314 s, which
      is NOT this change *by construction* (Q72 is all `JOIN … ON`, declined at
      the `fe.Joins` guard before this loop's code runs; it is the documented
      cap-straddler at 307/309/314 s across three loops). Timeout class is now
      **Q78** (-0035's CTE-body arm) + the Q72 straddle. TPC-H: spotcheck PASS
      (Q12=2 Q13=35), plan-diff vs `m0125-0044-after` **22/22 MATCH**. Two
      ledger motions: the Q65 row flipped to `resolved`; a new row records
      that LATERAL *evaluation* (per-outer-row correlated rescan) remains
      unimplemented — only the join-order pass reads the new flag.

- [x] **M0125-0035 — C2: single-table qualifiers are attached to the join node,
      never to the base scan, and never pushed into a producing subquery**
      (filed 2026-07-31 by M0125-0026; evidence same README §"C2 is pervasive").
      Across the 18 goopg plans, **2 of 68 `Filter:` lines sit on a `Seq Scan`**
      (both inside scalar SubPlans); the other 66 sit on join nodes. Concrete
      cost: goopg hashes all **73,049** `date_dim` rows and applies `d_year=…`
      afterwards where PG's `Parallel Seq Scan on date_dim Filter: ((d_moy>=3)
      AND (d_moy<=6) AND (d_year=2001))` yields **71** — a **1000×** larger
      build side on the dimension every fact table joins to, in nearly every
      member of the class. This is RC-5 confirmed and generalized
      (`internal/planner/local_filters.go:154` hardcodes `SmallDimension` to
      `region`/`nation`, so no TPC-DS relation can ever qualify). Acute forms:
      **Q31** — PG attaches the per-reference filter to each of six `CTE Scan`
      nodes, goopg attaches exactly one (`ws3`) and hoists the other five into a
      single conjunction on the top join; **Q78** — `ss_sold_year = 1998` never
      reaches `date_dim`, so all three channel CTEs aggregate every year where
      PG pushes `d_year = 1998` into all three. Members: all 18 as an
      aggravator; acute in **Q31 Q78 Q8 Q47**. **First step is not a fix:**
      determine whether the `Multi-Way Hash Join` operator pre-filters its build
      side at RUNTIME despite the plan text — an `EXPLAIN` cannot settle that,
      and the answer decides whether this is a costing-only defect or an
      execution one. Acceptance: **Q78 completes and matches `78|OK|45|8f67acff…`**.
      Bar: as -0034.
      **↳ FIRST STEP DISCHARGED + BINARY-JOIN ARM LANDED 2026-07-31 (loop #8).**
      Design `docs/design/0125-0035-c2-single-table-qual-placement.md`; evidence
      `analysis/m0125-0035-c2-qual-placement/`; re-capture
      `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0035/`.
      **The determination: C2 is an EXECUTION defect, not costing-only.** Serial
      `EXPLAIN ANALYZE` (a COUNTING instrument — valid while the nightly held the
      host; no timing is claimed): `store_sales ⋈ date_dim WHERE d_year = 2002`
      hashes `date_dim` at **actual rows = 73,049** (the whole table) and the
      join emits **1,374,770** rows for a **275,107**-row answer. The MHJ arm is
      the same shape — `customer_address` hashed at 50,000, 96,562 rows emitted
      for 11,049. The code agrees: `multiHashJoinOp.Open` drains every build
      child through `drainRowsCtx` and hashes all of it, then `partitionFilters`
      evaluates a build-table qual at STEP time, after the hash exists. Both
      answers equal PG, so what is wrong is the WORK, not the result.
      **Why it was stranded:** both placement passes declined. Slice A needs ≥5
      tables AND a `SmallDimension` relation — a hardcoded name-tag set only for
      `region`/`nation`, so no TPC-DS relation can EVER qualify. M0125-0004's
      `pushSingleSideQualsIntoInnerJoinInputs` excluded base-relation leaves by
      borrowing Slice A's risk — **and that borrowing was wrong**: Slice A MOVES
      a conjunct before DP enumeration (hence "Q8 / Q21 PASS → CANCEL"), while
      that pass runs LAST and DUPLICATES, so the join order is already fixed and
      admitting a leaf changes only what a scan EMITS.
      **Landed:** `innerJoinPushEligibleInput` accepts `*SeqScan`/`*IndexScan`/
      `*IndexOnlyScan`, with `LeafLocal` set to match the leaf-local coordinate
      space and a fail-closed idempotence guard (Q69 was printing its
      `d_year`/`d_moy` conjuncts TWICE — a subtree is re-walked once per
      enclosing scope). `bench/tpch/env_goopg.sh` also gained the `GOOPG_BIN`
      override `bench/tpcds/env_tpcds.sh` took on 2026-07-30, so the mandatory
      spotcheck gate no longer clobbers the nightly's shared binary mid-run.
      **Measured:** scan-level `Filter:` lines **5 → 71** across the 18-query
      capture; Q69's join estimate 525,809,623 → 287,921 (wrong by 1,826×);
      spotcheck `RESULT=PASS` (Q12=2 Q13=35, and Q12 is one of the changed
      plans); plan-diff vs `m0125-0034-setop-join-promotion` **4/22 DIFFER
      (Q12 Q14 Q16 Q20) with ZERO structural change** — every node-kind line
      appears an even number of times, and the delta is exactly **4 net-new
      scan-level filters and 0 removed**, which is property 2
      (duplicate-never-move) confirmed on real plans; full 99-query SF0.5 gate
      in four chunks on one binary **PASS=87 (53 ck-verified) MISMATCH=0
      CKMISMATCH=0 ERROR=0 TIMEOUT=8 SKIP=4** — **zero correctness failures and
      NO new timeout**, all 8 already-filed (Q30 Q64 Q65 Q81 = -0034's open arm;
      Q18 = -0033; Q35 = -0003; Q31 + Q78 = this item). Sweep run `FORCE=1`
      under the nightly — legitimate for row/checksum work per the banner, but
      **every wall clock in those reports is contaminated** (Q47 306 s and Q72
      325 s reported PASS above the nominal 300 s cap for that reason).
      **STAYS OPEN — the acceptance is NOT met.** Three arms remain, one ledger
      row each: **(a)** the pass declines every OUTER join, and Q78's
      `ss_sold_year = 1998` sits above two `Hash Join (LEFT)` on a CTE OUTPUT
      column — it needs the preserved-side extension (safe without a
      `nullingrels` model) plus PG's single-reference CTE inlining (PG 12+
      `cte_inline`), which is how `d_year = 1998` reaches `date_dim` in PG's
      plan; Q31's `ws3` is referenced 6× and is the multiply-referenced control.
      **This is the SAME boundary as -0034's open CTE arm — take them
      together.** **(b)** the MHJ arm: `pushSingleSourceFiltersIntoMHJTables`
      disqualifies any conjunct containing an `InExpr`, so an IN-list stays on
      the MHJ node; small, but its walker is a planner/executor SIBLING PAIR.
      **(c)** the costing half is untouched — the DP still sees unfiltered
      base-rel sizes, so join ORDER is still chosen for full tables
      (`M0125-0038` / the cost-model "0077 line").
      **↳ ARM (a) IS HALF-DISCHARGED 2026-07-31 (loop #9) — the preserved-side
      extension AND the descent landed; the CTE-body half did not.** Design
      `docs/design/0125-0035a-preserved-side-restriction-descent.md`; evidence
      `analysis/m0125-0035a-preserved-side-descent/`; re-capture
      `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0035a/`. Two
      restrictions were retired, each with its own pin: **INNER-only became
      preserved-side-only** (for `A LEFT JOIN B` and a restriction naming only
      `A`, every `A` row reaches the output matched or null-extended and the
      restriction cannot read `B`, so pushing it discards exactly the rows the
      residual would have — no `nullingrels` model needed, which is what the old
      decline was waiting for; the NULLABLE side still declines, FULL declines,
      SEMI/ANTI decline because their `Output()` is `Left`'s layout alone, and
      CROSS is admitted), and **immediate-child-only became descent to the
      deepest holder** (`pushConjunctIntoSubtree`, PG's
      `distribute_restrictinfo_to_rels` placement). **Measured: the two ACUTE
      members named in this item's own body are exactly the two plans that
      change.** Q31 — all six `CTE Scan` nodes now carry their own
      `(d_qoy = N) AND (d_year = 1999)`, which is PG's placement — goes
      **TIMEOUT → PASS at 11 s, 19 rows, `ck=2a74acfb556c21a7` = the oracle**.
      Q78's `ss_sold_year = 1998` descends to `CTE Scan on ss`. Full 99-query
      SF0.5 gate on a QUIET host, four chunks on one binary: **PASS=89 (54
      ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=6 SKIP=4**, and
      **all 87 common PASSes are identical in status AND value checksum** vs
      loop #8. Q18 also went TIMEOUT → PASS (266 s) and that is deliberately
      **NOT claimed here** — its plan is byte-identical across the change and
      loop #8's reading was taken under the live nightly, so host quietness is
      the economical explanation; it stays filed as `M0125-0033`. TPC-H:
      plan-diff vs `m0125-0035-c2-qual-placement` **1/22 DIFFER (Q17) with ZERO
      structural change** — one added scan-level filter, and `part`'s reaching
      its scan drops the join estimate 5,997,241,000 → 149,931; the owed TIMED
      w2 arm ran on the quiet host and is **neutral** (stream 395.5 → 389.1 s,
      Q17 0.84×, both inside `M0125-0031`'s measured ±17 % band) with
      **identical row counts on all 21 completing queries**; `tpch-spotcheck.sh`
      `RESULT=PASS` (Q12=2 Q13=35). **STILL OPEN — the acceptance is STILL NOT
      met.** Q78's qual now lands on the CTE *reference*, which filters the
      CTE's OUTPUT after the aggregate, so all three channels still aggregate
      every year. Reaching `date_dim` needs **single-reference CTE inlining**
      (PG 12+ `cte_inline`, `subselect.c::inline_cte`; Q31's 6×-referenced `ws3`
      is the control that must NOT inline) and then **equivalence-class constant
      propagation** (`equivclass.c`) to carry the constant to `ws`/`cs`. Arms
      (b) and (c) remain untouched. Three ledger rows 2026-07-31; the earlier
      "declines every OUTER join" row is flipped to `resolved`.
      **↳ DONE 2026-08-01 (loop #18) — the CTE-body half landed and the
      acceptance is MET: Q78 TIMEOUT → PASS 24 s, 45 rows,
      `ck=8f67acff3895183f` = the oracle.** Three mechanisms, in
      `internal/planner/cte_inline_pushdown.go` +
      `inner_join_qual_pushdown.go`: (1) single-reference CTE-body qual
      descent (PG `inline_cte` + `subquery_push_qual`; refs==1 gate read
      only from Plan()'s tail because the body Node AND the executor's
      CTERowCache are shared between references; Q31's `ws3` control
      unchanged); (2) `deriveConstAcrossJoinEquality`, a bounded
      `equivclass.c`: a descending `col = const` seeds `col' = const`
      through the join's own equality onto the OTHER input — including the
      NULLABLE side, safe with no nullingrels model because a nullable row
      failing the derived constant can never match, and property 2 keeps
      the residual above; (3) `reselectDegenerateHashKeys`: with the plan
      text already PG-identical, Q78 still burned >900 s because
      `splitEqualityForHash` keys on the FIRST equi-pair — after (1)+(2)
      pin `sold_year` to 1998 on both sides, the whole build side shared
      ONE bucket (245,587 probes × ~30k entries). PG hashes every pair;
      goopg now re-picks a non-pinned pair, result-neutral since the
      executor enforces the full Predicate per match. Full SF0.5 gate
      **PASS=95 (57 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0
      SKIP=4**, exactly two cells moved vs loop #17 (Q78 claimed; Q72's
      306 s flip is its straddle, NOT claimed), all 93 common PASSes
      identical in rows and checksum; spotcheck PASS; plan-diff
      `m0125-0044-after` 22/22 MATCH (zero TPC-H plan change); units
      green. Pins `internal/planner/cte_inline_pushdown_test.go`. Design
      `docs/design/0125-0035b-cte-body-inline-ec-const-hash-key.md`;
      three ledger rows 2026-08-01. Arm (b) → `M0125-0046` below; arm (c)
      = `M0125-0038`.

- [x] **M0125-0046 — MHJ IN-list qual placement: `pushSingleSourceFiltersIntoMHJTables`
      disqualifies any conjunct containing an `InExpr`** (refiled 2026-08-01 from
      M0125-0035 arm (b) at its closure; original evidence README §"C2 is
      pervasive"). An IN-list restriction stays on the MultiHashJoin node instead
      of reaching the member scan that could pre-filter its build input. Small,
      but the walker is a planner/executor SIBLING PAIR — both sides must change
      together (Hard-won Rule #2). Bar: spotcheck + SF0.5 subset probe of the
      MHJ-heavy members, plan-diff. **↳ DONE 2026-08-01 (loop #20), with the
      diagnosis CORRECTED: the planner walker admits literal IN-lists (since
      M0061) and the mh.Filters push handles them — the conjunct was never in
      `mh.Filters` at all. It sits in the residual `*Filter` ABOVE the MHJ,
      which neither MHJ pass reads and which the binary sibling
      (`pushInnerJoinInputQuals`) declined for want of a `*Join` child. Fix:
      new `pushResidualQualsIntoMHJTables` arm (fail-closed walkExprRefs
      attribution by cumulative offset + positional name check, clone-shift to
      leaf-local, descend via `pushConjunctIntoSubtree`; property-2 duplicate);
      `pushSingleSourceFiltersIntoMHJTables`'s wrapper now stamps `LeafLocal`
      so the two passes compose into one wrapper. The EXECUTOR half of the
      filed claim WAS real: `multi_hash_join.go::walkColumnRefs` vetoed every
      InExpr (literal IN-lists → leafFilters detour) and silently skipped
      Cast/IsNull/IsBool/IsDistinctFrom/Collate/Row (an IS-NULL-only filter
      read as constant = probe-time eval with unbound columns); it now mirrors
      the planner walker and gains a fail-closed `default: onOuter()`. §2
      probe: MHJ emits 11,049 = the answer (was 96,562), count = PG oracle.
      Gates: units PASS; spotcheck PASS (Q12=2 Q13=35); SF0.5 subset probe
      Q7,10,26,27,30,31,34,35,47,50,69,72,73,79,96 → PASS=15 MISMATCH=0
      CKMISMATCH=0 (Q72 straddled 300 s on the nightly-saturated host, PASS
      solo at 600 s; FORCE=1, no timings claimed); plan-diff vs
      m0125-0044-after 5/22 DIFFER (Q2 Q3 Q10 Q11 Q21), every diff only
      +Filter lines under MHJ member scans with zero structural change, and
      row counts proven: Q3=11521 Q10=20501 Q11=819 Q21=405 = the pinned
      anchors, Q2 (no anchor) md5-identical at 455 rows between a HEAD
      worktree binary and the fix on the same data. Tests
      `internal/planner/mhj_residual_qual_pushdown_test.go`,
      `internal/executor/mhj_inlist_filter_test.go`. Design
      `docs/design/0125-0046-mhj-residual-inlist-qual-placement.md`; ledger:
      row 2026-07-31 (arm b) flipped resolved, two rows appended 2026-08-01
      (duplicate-not-move vs PG's distribute_restrictinfo_to_rels; FuncCall
      veto pending a provolatile model). The *Join-spine-descent-into-MHJ row
      (2026-07-31) stays open — this fix covers the Filter-directly-above-MHJ
      shape, which is every measured case.**

- [x] **M0125-0036 — C3: a correlated SubPlan is re-evaluated per outer row with
      no hashing or caching** (filed 2026-07-31 by M0125-0026; evidence same
      README §"C3"). **↳ DONE 2026-07-31 for the EXISTS half; Q30/Q81 are NOT
      closed and are refiled below as `M0125-0041`.** Design
      `docs/design/0125-0036-exists-to-any-hashed-subplan.md`; pass
      `internal/planner/exists_to_any.go` (kill switch `GOOPG_EXISTS_TO_ANY=off`);
      tests `internal/planner/exists_to_any_test.go`; evidence
      `analysis/m0125-0036-exists-to-any/`. The item's own instruction — "do not
      pre-commit to decorrelation, the fix is hashed-SubPlan caching" — was
      followed and then REFINED by the arithmetic: per-correlation-key caching
      cannot reach this shape at all, because the outer key `c_customer_sk` is
      unique per outer row and **every call is a miss by construction**. What
      makes the set shareable is removing the correlation, which is exactly what
      PG's own plan does via `convert_EXISTS_to_ANY` (subselect.c:1731) — and the
      *result* is still hashing, through the machinery goopg already had
      (`executor/subplan_hash.go`). **Acceptance MET, but not the way the row reads:
      `Q10` was ALREADY PASSING at 35 s at the loop-#9 baseline** — the
      `GOOPG_RELSIZE_FALLBACK` flip (`M0125-0005`) had rescued it, and the
      "TIMEOUT" label on Q10 comes from -0026's capture in an earlier planner
      regime. Q10 now runs in 16 s with the same checksum, and **the query that
      actually moves is Q35: TIMEOUT (327 s) → PASS (18 s, 100 rows = oracle)**
      (coordinated with `M0125-0003`, not duplicated, per this item). Full
      99-query SF0.5 gate: **`PASS=90 (54 ck-verified) MISMATCH=0 CKMISMATCH=0
      ERROR=0 TIMEOUT=5 SKIP=4`** vs loop #9's `PASS=89 … TIMEOUT=6`; diffed
      cell by cell, **exactly one of the 99 changed** and all 89 common PASSes
      agree in status AND checksum. Gates: units PASS; `tpch-spotcheck.sh`
      `RESULT=PASS` (Q12=2 Q13=35); TPC-H plan-diff 1/22 (Q17) **and the same
      1/22 with the switch OFF, so the change is plan-neutral on all 22** —
      Q17 belongs to M0125-0035a. **Two silent traps, both worth not
      rediscovering:** (1) the operand index taken verbatim from the body's
      `OuterColumnRef` is STALE after MHJ packing (which OID-re-sorts its output
      while treating a sublink as opaque) and made Q35 return **0 rows instead of
      100** — `resolveHostOperandIdx` re-resolves it, and note that **Q10's own
      acceptance row could not have caught this, because Q10's oracle IS 0 rows**;
      (2) a join's predicate row is `left ++ right`, which is not `Output()` for
      SEMI/ANTI. Scope is bounded by NULL semantics, not by taste: `IN` is
      three-valued where EXISTS is two-valued, so only qual positions reached
      through AND/OR convert and a negated EXISTS never does (upstream's
      `isTopQual`/`unknownEqFalse`). Original wording follows. goopg renders Q10/Q35 as `Hash Join (SEMI)` with
      `Filter: (EXISTS(SubPlan 1) OR EXISTS(SubPlan 2))`, each SubPlan an
      uncached correlated hash join; PG renders the identical query as
      `Filter: ((ANY (c_customer_sk = (hashed SubPlan 2).col1)) OR (ANY (… =
      (hashed SubPlan 4).col1)))` — **hashed**, built once. The control is
      **Q69**, whose three EXISTS are `and not exists … and not exists`: goopg
      unnests those into a proper `Hash Join (ANTI)`/`(SEMI)` chain and Q69
      completes. So the trigger is specifically the **`OR` of `EXISTS`**, which
      cannot become a semi-join, and goopg has nothing between "unnest to a
      semi-join" and "re-execute per row". Q30/Q81 are the correlated-scalar-agg
      variant of the same gap. Arithmetic: outer ≈1×10⁵ × (`web_sales`
      3.6×10⁵ + `catalog_sales` 7.2×10⁵) ≈ 1×10¹¹ row-touches; Q35's measured
      8.16 s per outer row ⇒ ≈**9 days at SF=1**. Members: **Q10 Q35 Q30 Q81**.
      `0124-0004` §D4's rule stands and PG's own plan corroborates it: if
      `CacheMisses ≈ Calls` the fix is **hashed-SubPlan caching, not
      decorrelation** — do not pre-commit to decorrelation. Acceptance:
      **Q10 completes and matches `10|OK|0|1f18d650…`**. **Q35 is already
      `M0125-0003`'s acceptance row — coordinate, do not duplicate it.** Bar: as
      -0034.

- [ ] **M0125-0037 — C4: set operations are opaque to EXPLAIN and to the
      planner** (filed 2026-07-31 by M0125-0026; evidence same README §"C4").
      **↳ STAGE (i) IS DONE (2026-07-31).** Design
      `docs/design/0125-0037-explain-set-operations.md`; tests
      `internal/executor/explain_setop_test.go`; re-capture
      `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0037/`. `describePlan`
      and `planChildren` — the two functions all three EXPLAIN renderers drive
      off — now carry a `*planner.SetOp` case. PG's vocabulary was captured from
      18.3 on `:65438` rather than inferred: `UNION ALL` → `Append` (PG builds
      **no** SetOp node), `INTERSECT`/`EXCEPT [ALL]` → `HashSetOp <cmd>` with two
      direct children, and JSON keeps `Node Type: SetOp` plus separate
      `Strategy`/`Command` properties. A left-deep UNION ALL chain flattens into
      one `Append`, matching PG. Acceptance met: Q5 4 → 128 plan lines, Q18 → 91,
      Q67 → 94, Q14 → 815, and **all three unclassifiable queries now have a
      class** — Q5 is C1+C2 (`Nested Loop (CROSS)` between the
      `store_sales ∪ store_returns` Append and `date_dim`, with the `d_date`
      range on the join Filter so `date_dim` costs 73,049 where PG's yields 8).
      Q18/Q67 exposed a class -0026 could not see, filed below as `M0125-0040`.
      One deliberate divergence + one pre-existing indent divergence are ledger
      rows (2026-07-31), not silent. **Stage (ii), the planner half, stays open
      and is a separate later selection** — the banner's order is unchanged, so
      the next selection is `M0125-0039`.
      goopg prints `*planner.SetOp` — a raw Go type name — **with no children**.
      Q5, Q14 and Q18 therefore have four-line plans with the entire query body
      invisible. Seven of eighteen captured queries contain one (Q5 ×1, Q8 ×1,
      Q14 ×5, Q18 ×1, Q54 ×1, Q67 ×1, Q71 ×1), and **three of them (Q5, Q18,
      Q67) could not be classified at all** — this item is what unblocks them
      and `M0125-0033`. Two halves, and they are separable:
      **(i) the EXPLAIN half** — teach `internal/executor/operators_explain.go`
      to descend into the set operation's branches and to name the node the way
      PG does (`Append`/`HashSetOp`); small, host-independent, no planner
      change, and every later item's evidence is unreadable without it.
      **(ii) the planner half** — the set-op node appears on one side of a
      `Nested Loop (CROSS)` in Q8, Q14, Q54 and Q71, so the DP's inability to
      see through it is the likely *proximate* cause of C1 at those sites; fixing
      it may retire part of -0034. Take (i) FIRST and on its own. Acceptance for
      (i): the captured `goopg-warm/q5.txt` shape is fully expanded on a re-run
      and the three unclassified queries get a class. Acceptance for (ii):
      **Q5 completes and matches `5|OK|100`**. Bar for (ii): as -0034; (i) is
      EXPLAIN-only and needs only the unit gate plus the commit smoke.
      **↳ STAGE (ii)'s ACCEPTANCE ROW IS ALREADY GREEN — MEASURED 2026-07-31
      (loop #11), before selecting it.** The latest SF0.5 sweep
      (`analysis/m0125-0036-exists-to-any/sf05/`) has **`Q5 PASS 40s 100 rows`**,
      and Q8/Q14/Q54/Q71 — the other four queries whose `Nested Loop (CROSS)`
      motivated this stage — all PASS too. `-0034`'s set-operation arm (loop #7:
      30 Cartesian products gone, Q5/Q8/Q14/Q54/Q71 TIMEOUT → PASS) had already
      retired the C1 crosses that stage (ii) was written to fix, so its stated
      acceptance was green on arrival. **This is the SECOND consecutive item
      whose acceptance row predated its own fix** (the first was -0036's Q10);
      -0026's acceptance rows are all older than -0005/-0007/-0008/-0034/-0035a
      and must be re-read against the latest `sweep-*.txt` before selection.
      Left UNCHECKED deliberately: what is *measured* is the acceptance query,
      not the item's mechanism claim ("the DP cannot see through a set-op node").
      A later loop that wants to close -0037 should either verify that claim
      directly or restate the acceptance — it must not re-derive the fix from a
      timeout that no longer exists. The current timeout class is **Q30 Q64 Q65
      Q78 Q81** (5), none of which is a set-op C1 shape.

- [x] **M0125-0038 — C5: no cost or cardinality propagation above base scans**
      (filed 2026-07-31 by M0125-0026; evidence same README §"C5"). Every
      non-leaf node in all 18 goopg plans renders `cost=0.00..0.00 rows=1`,
      while leaves carry real warm statistics (`Seq Scan on public.store_sales
      (stats) … rows=1439608`). Where a join estimate IS produced it equals the
      Cartesian product of its inputs: Q10's SubPlan 1 reports
      `rows=131280740`, and 359,432 (`web_sales`) × 365.25 (`date_dim` after
      `d_year`) = 131,280,738 — **the equi-join key contributes no selectivity
      at all**. Consequence: the join-order DP chooses among shapes it has
      costed as identical, which is why warm statistics changed no plan
      (`M0125-0031`) and why `GOOPG_RELSIZE_FALLBACK=0` and `=2` produce
      byte-identical plans on all 18 queries. C5 is the reason C1–C3 are never
      *corrected* by costing, and the reason no further cardinality-side work
      can reach goal (a). This is the largest item of the six and overlaps the
      `docs/design/cost-model/` "0077 line" design bundle — **read that bundle
      before scoping it**; it may be the trigger to promote that design to
      implementation rather than a standalone fix. No acceptance query of its
      own: accepted when C1's and C2's fixes hold under plan-diff
      `LABEL=tpcds-round2-head` without a hand-written override.

- [x] **M0125-0039 — diagnostics: EXPLAIN renders column references
      unqualified, so real correlations print as self-comparisons** (filed
      2026-07-31 by M0125-0026; evidence same README §"C4", closing paragraph).
      goopg prints `Filter: (ctr_state = ctr_state)` for Q30/Q81 where PG prints
      `ctr1.ctr_state = ctr_state`; likewise `(cd_marital_status <>
      cd_marital_status)` (Q64), `(d_week_seq = d_week_seq)` (Q72), and Q31's
      top-level `(d_qoy = 1 AND … AND d_qoy = 2 AND … AND d_qoy = 3)`. These are
      almost certainly distinct columns of a wide join tuple, but **nothing in
      the output distinguishes that from a genuinely unsatisfiable predicate** —
      which is exactly the reading a triage loop needs. Small, host-independent,
      EXPLAIN-only. Fix: qualify column references with their relation
      alias in the EXPLAIN expression printer
      (`internal/executor/operators_explain.go`), matching PG's `ruleutils.c`
      behaviour of qualifying whenever the name is ambiguous in scope.
      Acceptance: Q30's filter prints as `ctr1.ctr_state = ctr2.ctr_state`, plus
      a unit test in `internal/executor` over a self-joined relation. Bar: unit
      gate + commit smoke; no plan-shape bar (renders only).
      **↳ DONE 2026-07-31** (`docs/design/0125-0039-explain-column-qualification.md`).
      Upstream's rule was captured from the oracle, not inferred, and it turned
      out to be two mechanisms, not one: explain.c splits the prefix decision by
      node kind (`show_scan_qual` renders a scan's `Filter:` BARE, while
      `show_upper_qual`/`show_sort_group_keys` prefix once
      `es->rtable_size > 1`), and ruleutils.c's `get_parameter` forces
      prefixing for a Param's expansion — goopg's `OuterColumnRef`. Both are
      implemented in `internal/executor/explain_names.go` +
      `operators_explain.go`; nothing outside `internal/executor` changed.
      **Acceptance met and then some: Q30 and Q81 now print
      `Filter: (ctr1.ctr_state = ctr_state)`, BYTE-IDENTICAL to PG 18.3 on
      :65438** (the item's predicted `ctr1.ctr_state = ctr2.ctr_state` was an
      inference — PG leaves the local side bare because it is a scan qual, and
      goopg now reproduces that asymmetry). Q64 → `(cd1.cd_marital_status <>
      cd2.cd_marital_status)`, Q72's join filter names `d1`/`d2`. Arm:
      `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0039/`; 8 tests in
      `internal/executor/explain_qualify_test.go`.
      Two findings worth carrying forward. **(1) A confidently wrong qualifier
      would be worse than no qualifier**, and the naive implementation produced
      one: `planner.go`'s `nextSourceIdx` restarts at 1 for every query level,
      so an outer subquery binding collides with a base relation inside it
      (probe: `(a.s1 <> a.s2)` where `s2` came from `b`). Contained by a
      column-membership guard and a match-uniqueness gate; the real fix is a
      globally unique range-table id (PG's `varno`) and is planner work — two
      ledger rows, 2026-07-31. **(2) Q31 stays PARTIAL** for that same reason
      (2 of 12 conjuncts qualify). VERBOSE-forced prefixing and `Output:` line
      qualification are also still open (second ledger row).

- [ ] **M0125-0040 — C6: `ROLLUP` is expanded into a UNION ALL of one aggregate
      branch per grouping level, each re-running the whole join subtree**
      (filed 2026-07-31 by `M0125-0037`(i); evidence
      `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0037/q18.txt` and
      `q67.txt`, which only became readable once the set-op node did). Neither
      query contains a `union all` in its SQL text — the `Append` is goopg's own
      grouping-sets expansion, which is why this class was invisible to
      `M0125-0026`. Measured fan-out at SF=0.5: **Q18 — 4 HashAggregate branches,
      5 `Multi-Way Hash Join`s, 5 full `catalog_sales` scans (720,657 rows
      each); Q67 — 8 branches, 9 MHJs, 9 full `store_sales` scans (1,439,608 rows
      each)**, with no shared or materialized subtree between branches. PG
      computes every level in ONE pass over ONE scan: Q18's PG plan is a single
      `GroupAggregate` with five stacked `Group Key:` lines (the last `Group
      Key: ()`), and Q5's PG plan a `MixedAggregate` with `Hash Key` lines
      (`postgres/src/backend/executor/nodeAgg.c`, `AGG_MIXED`; planner side
      `preprocess_grouping_sets` / `consider_groupingsets_paths` in
      `planner.c`). This is the most likely proximate cause of both timeouts and
      of the Q18 warm regression tracked as `M0125-0033` — an 8× multiplier on a
      1.44 M-row join is not something cardinality or join-order work can
      recover. Two candidate fixes, cheapest first: (a) materialize the common
      subtree once and let the N branches read it (a `Memoize`/CTE-style share —
      keeps the expansion, removes the re-scan), or (b) implement PG's real
      multi-level `AGG_MIXED`/`AGG_SORTED` grouping-sets aggregate, which is the
      faithful answer and also fixes the `Group Key: ()` grand-total row shape.
      Acceptance: Q67's plan shows ONE scan of `store_sales`, and Q18 and Q67
      both complete inside the warm gate's budget with matching row counts.
      Bar: as `M0125-0034` (plan-diff `LABEL=tpcds-round2-head` + the SF0.5
      regression gate), because (a) and (b) are both executor/planner changes.

- [ ] **M0125-0041 — C3's second half: a correlated SCALAR-aggregate subquery is
      re-evaluated per outer row** (filed 2026-07-31 by `M0125-0036`, which
      closed C3's EXISTS half and deliberately did not touch this one).
      Members: **Q30 and Q81**, both still `TIMEOUT` at SF=0.5 after -0036
      (measured 345 s and 350 s, `analysis/m0125-0036-exists-to-any/README.md`).
      `M0125-0026` §C3 called them "the correlated-scalar-agg variant of the
      same gap" and priced them at CTE(≈2×10⁴) × `customer_address` 5×10⁴ = 1×10⁹
      pairs, each surviving row re-scanning the CTE ⇒ ≈**2×10¹³** — five orders
      above a 300 s budget, and §C3 lists C1 as compounding it.
      **Why -0036 does not reach them and what that implies for the fix:** its
      conversion turns a correlated EXISTS into an uncorrelated ANY, which is
      only sound because EXISTS asks a *set-membership* question. A correlated
      `(SELECT avg(x) … WHERE x.k = outer.k)` asks a per-group question, so the
      shareable object is not a value set but the **grouped aggregate** — i.e.
      the transformation is the scalar-sublink pull-up goopg already has
      (`unnest.go`'s GROUP BY aggregate + hash join), and the question is why it
      declines here. Start by finding out: instrument or read
      `subqueryANDReachable` / the scalar path's guards against Q30's actual
      plan before designing anything, because -0034's stated starting point was
      refuted the same way. Do NOT assume the -0036 pass generalises.
      Acceptance: **Q30 completes and matches `30|OK|31|f47a48499fd7e070`**.
      Bar: as `M0125-0034`.
      **↳ ROOT CAUSE FOUND AND FIXED 2026-07-31 (loop #14) — but the item STAYS
      OPEN, because its acceptance is a completing Q30 and Q30 still TIMEOUTs.**
      Design doc `docs/design/0125-0041-cte-scalar-sublink-decorrelation.md`;
      two ledger rows 2026-07-31. The filing's instruction ("find out why the
      pull-up declines") was right and its answer is that **it never declined**:
      a probe printing every gate's verdict for the Q30 skeleton shows
      `canUnnestSubquery`=true, `avg` NULL-on-empty, the `*1.2` target strict,
      the conjunct AND-reachable, 1 param / 0 residuals, inner not probe-cheap.
      The pull-up then died in `clonePlanReplacingOuter`, which had **no switch
      arm for `*planner.CTEScan`** — and a clone failure is a *bail*, so a
      capability gap looked exactly like a policy decision, for every query
      whose correlated sublink reads a CTE. Fixed by adding `*CTEScan` (body
      shared verbatim — `WITH` is not `LATERAL`, so the correlation always sits
      in a `Filter` above it, and sharing is what lets the executor's name-keyed
      `ctx.CTERowCache` materialize the body once) + `*MaterializedCTEScan`,
      guarded by `planSubtreeHasOuterRefDeep` (a body with an outer ref must not
      be shared: it would collide with an un-rewritten consumer under the same
      CTE name), with matching arms in the sibling `planCloneSupported`.
      Equivalence-tested (pull-up on vs off, control arm asserts the SubPlan
      path really ran, NULL correlation key included), not merely shape-tested.
      **The remaining factor is C1 = `M0125-0034`, and it is now isolated:**
      Q30's plan lost `SubPlan 1` but retains `Nested Loop (CROSS)` between the
      ~2×10⁴-row CTE and a full 5×10⁴-row `customer_address` scan = **10⁹ pairs**,
      each driving an index probe into `customer`. Q30 measured TIMEOUT at BOTH
      **300 s and 1200 s** — a shape defect, not a budget crossing (same verdict
      class as Q21/`M0125-0032`). §C3 had already named C1 as compounding this
      class; the 2×10¹³ price is now factored, and this task removed its
      CTE-rescan factor. **Next probe for whoever takes -0034 here:**
      `ca_state = 'AR'` is a single-table local filter that would shrink
      `customer_address` ~50×, yet it still sits in the top `Filter` beside the
      (formerly sublink-bearing) conjunct instead of on the scan — find out
      whether the sublink's presence in that predicate is what kept it there.
      Also recorded (ledger): goopg's parser rejects `WITH` inside a sublink
      where PG accepts it, which is why the outer-ref guard has no test.
      **⚠ GATE HYGIENE, affects the bar for the next plan-shape task:
      `make plan-diff LABEL=tpcds-round2-head` now reports 22/22 diverged, with
      OR WITHOUT this change** — the same-cluster stash A/B shows the live plans
      are byte-identical in both arms, so the label is STALE, not regressed. The
      divergence is systematic (the snapshot has bare `Seq Scan on public.orders`
      and no `Gather`; every live plan carries `(stats)`, real estimates and
      parallel workers), i.e. a baseline captured S-cold against a cluster the
      warm-stats programme (`M0125-0028`…`-0030`, which made the bench build
      scripts ANALYZE) has since warmed. **Re-capture the label before relying on
      it**; until then a stash A/B is the only meaningful TPC-H plan-shape
      evidence. Gates this loop: full SF0.5 sweep PASS=89 MISMATCH=0 CKMISMATCH=0
      ERROR=0 TIMEOUT=6 SKIP=4 with **all 99 cells identical in status, rows and
      checksum** to the pre-change baseline; TPC-H plan A/B 0/22;
      `tpch-spotcheck` PASS (Q12=2 Q13=35); units gate PASS.

- [x] **M0125-0042 — two OR-ed uncorrelated `IN (subquery)` sublinks over-match
      under a SEMI join over an MHJ** (filed 2026-07-31 by `M0125-0036`, found
      while probing; **pre-existing at HEAD, not caused by that change**).
      Reproducer and both arms in `analysis/m0125-0036-exists-to-any/`
      (`probe35g.sql` vs `p2.sql`), SF=0.5 against the PG 18.3 oracle on
      `:65438` db `tpcds05`:
      **one** hand-written `c.c_customer_sk IN (SELECT …)` under the same
      MHJ+SEMI shape answers **377 = PG**, but **two** of them OR-ed answer
      **1329 where PG says 1294** — an over-match of 35, i.e. rows are admitted
      that neither arm should admit. The EXISTS→ANY pass never fires on this
      query (its only EXISTS is a top-level conjunct, which that pass declines),
      and the *converted* form of the same predicate answers 1294 correctly —
      which localises the defect to how a hand-written `InExpr` is indexed or
      unnested, NOT to the shared executor probe. First suspicion, by analogy
      with -0036's own trap: `visitColumnRefs` DOES descend into `*InExpr`
      (unlike `*ExistsExpr`/`*OuterColumnRef`, bushy.go:422), so an MHJ re-sort
      may shift one arm's Operand while `isUnnestableNonCorrelatedIn` reshapes
      the other. Verify that before designing. Note this is a **silent wrong
      answer**, so it outranks a timeout in severity even though no gate query
      currently exercises it. Acceptance: `probe35g.sql` answers 1294, plus a
      regression test at the planner or executor level. Bar: as `M0125-0034`.
      **↳ ROOT-CAUSED 2026-07-31 (loop #11); FIXED 2026-07-31 (loop #12).**
      Design `docs/design/0125-0042-in-sublink-operand-stale-index.md`
      (`## The fix` + `## Bar for the fix — MET`), evidence
      `analysis/m0125-0042/` (nine probes + both traces).
      `internal/planner/exists_to_any.go`: new `fixInExprOperandIndex` /
      `resolveHostColumnIdx` re-resolve a SubPlan-bearing `InExpr`'s operand
      by Name against the host node's schema, wired into the existing
      `rewriteExistsToAnyNode` walk (now unconditional — only the EXISTS→ANY
      conversion itself stays behind `GOOPG_EXISTS_TO_ANY`). `probe35g.sql`
      → 1294, `pAA.sql` → 377, both = PG. Four new unit pins in
      `exists_to_any_test.go`. Full bar run and PASS: units,
      `tpch-spotcheck.sh` (Q12=2/Q13=35), `make plan-diff
      LABEL=m0125-0005-relsize-default-stage2` (22/22 DIFFER, confirmed
      byte-identical with/without this change — pre-existing ANALYZE-stats
      drift, not a regression), full 99-query TPC-DS SF0.5 sweep
      (PASS=89 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=6 SKIP=4 — the 6
      timeouts are the pre-existing M0125-0038-scoped performance class).
      **Process note:** this loop selected `M0125-0042` per the stale
      working-set baton before re-checking the Current Priority banner,
      which the same-day commit `da882af6` had already amended to put
      `M0125-0043` first inside M0125. The fix was already root-caused,
      implemented and fully gate-verified by the time this was caught, so it
      was landed rather than discarded; **`M0125-0043` is the mandatory next
      selection**, not a fresh choice.
      **The filed framing understates the defect: only 10 of goopg's 314 rows
      appear in PG's 377** — the answer sets are nearly DISJOINT and merely
      similar in size, so "an over-match of 35" is a coincidence of cardinality.
      Bisected by measurement: the SEMI join is exact (EXISTS-only = 11996 = PG,
      and all 314 emitted rows re-check as satisfying it), the value set is
      exact (a CONSTANT operand over the same two sublinks = 11996 = PG, and
      only 10 of 314 satisfy the IN), each arm alone is exact (377 / 950), and
      the OR-ed pair WITHOUT the EXISTS is exact (11127) — **so the operand is
      what is wrong**, and `A OR A` / `A OR ∅` are both wrong (314), so it is
      one arm mis-evaluating, not an OR-combination defect.
      **Mechanism:** the `InExpr.Operand` ColumnRef carries the correct `Name`
      and a STALE `Index` — bound at **13** (`c_customer_sk` under `ca ++ c`),
      **9** by `remapWithBindings` (under `cd ++ c`), where runtime needs **22**
      (under `ca ++ cd ++ c`). Index 9 of the executed 40-wide layout is
      **`ca_zip`, a string**; `compareEq`'s string↔int coercion answers instead
      of raising, so a ZIP that numerically appears among the customer keys
      admits the row. `posMap(27) = 9`, i.e. the remap treats 9 as final and is
      NOT the broken part. **The item's filed first suspicion is REFUTED** —
      `visitColumnRefs` descending into `*InExpr` is correct and is not the
      cause; verify-before-designing paid off exactly as the item asked.
      **Why nothing catches it (three independent maskings, all worth keeping in
      mind for the fix):** (1) `reresolveJoinByName` — the ONLY by-Name rebind —
      is driven from `applyJoinTreePosMap`'s `*Join` arm, and at remap time the
      outer tree is `Filter → MultiHashJoin` with no `*Join`, so it never fires;
      (2) a SINGLE `IN` is masked because it unnests to a semi-join whose keys
      rebind by Name, and the Name is right — only the OR-ed form, which cannot
      unnest, consumes the raw Index; (3) **EXPLAIN masks it too**, printing
      `c.c_customer_sk` from the Name while the executor reads index 9, so no
      plan reader can see this class of defect.
      Ruled out BY MEASUREMENT, do not re-test: the hashed probe
      (`GOOPG_HASHED_SUBPLAN=off` reproduces 314/1329 identically), parallelism
      (`max_parallel_workers_per_gather=0` identical; deterministic across runs),
      and **a synthetic minimal reproducer** — 6 tiny tables with OIDs ordered so
      the MHJ re-sorts, SubPlan bodies made joins, projected column moved off
      index 0 — which reaches a **structurally IDENTICAL plan and still answers
      correctly**. The trigger is the binding history, not the plan shape, so the
      regression test must assert the planner's operand index, not just a result
      from a small fixture.
      **RESUME POINT:** generalise `resolveHostOperandIdx`
      (`internal/planner/exists_to_any.go`, M0125-0036) to hand-written
      `InExpr` operands — after the last remap pass, re-resolve the operand
      ColumnRef of every SubPlan-bearing `InExpr` by Name against the host
      node's output schema, under a `findUniqueColumnIndex` unique-match guard
      (leave the index untouched when absent/ambiguous; M0125-0039 showed a
      confidently wrong qualifier is worse than none, and `SourceTableIdx` still
      collides across query levels). Do NOT descend into the sublink's own
      `Plan` (inner scope; `scopeIgnore`/`slotInnerPlan` already draw that line).
      Bar: units + `tpch-spotcheck.sh` + TPC-H plan-diff + the full 99-query
      SF0.5 gate. Acceptance: `probe35g.sql` → 1294 AND `pAA.sql` → 377.
      **Adjacent latent defect found while reading, NOT the cause of this bug
      and not yet filed as its own item:** `remapByPosMap`'s inner-plan switch
      (bushy.go) handles `*ExistsExpr`, `*SubqueryExpr`, `*ArraySubqueryExpr`
      and `MultiAssignSubq*` but has **no `*InExpr` arm**, so a *correlated*
      `IN (subquery)`'s `OuterColumnRef`s are never translated through `posMap`.
      Fold it into this fix or file it when the fix lands (ledger row).

- [x] **M0125-0044 — a SILENT WRONG ANSWER: multiple aliases of the same table
      collapse to one alias in projection resolution** — **FIXED and landed
      2026-07-31.** The collapse was in the AGGREGATE SURFACE, not in projection
      resolution generally: without GROUP BY the same query projects correctly.
      `parserExprKey` drops a `ColumnRef`'s qualifier on purpose (GROUP BY `c`
      must satisfy SELECT `t.c` — M0097-0003), so every alias of a self-joined
      table hashes to one key, `groupByExpr[key] = idx` keeps only the LAST
      GROUP BY item, and `resolveExprAfterAggregate` consults that name-keyed
      map BEFORE its own correct index-keyed path. Grouping was never wrong
      (`GroupExprs` holds two distinct resolved refs), which is why every
      row-count gate stayed green. Fix leaves `parserExprKey` alone and adds a
      qualifier-preserving key (`qualifiedGroupKey`,
      `internal/planner/groupby_alias_key.go`) consulted only where the first is
      contested — "contested" = already bound to a DIFFERENT slot, so
      `GROUP BY a, a` is correctly not ambiguous. A contested key that places
      nothing is abandoned, not fallen back on, so `SELECT d3.y` under
      `GROUP BY d1.y, d2.y` now raises PG's 42803 instead of another alias's
      value. **Recorded negative result:** matching resolved exprs against
      `Aggregate.GroupExprs` with `exprEqual` was implemented and is WRONG —
      those are indexed against the child schema, which join reordering
      permutes (observed: a `d2.y` twin remapped from index 5 to 1). Measured:
      Q64 → `64|OK|2|31f0342ff9d55c4a`; full 99-query SF0.5 gate PASS=93
      MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4 with **exactly 1 of 99
      cells moved** vs HEAD `d50c0b4a`; TPC-H 22/22 plans MATCH
      (`m0125-0043-after`), `tpch-spotcheck.sh` PASS, units PASS. Design
      `docs/design/0125-0044-groupby-alias-slot-collapse.md`; evidence
      `analysis/m0125-0044/`; tests
      `internal/planner/groupby_alias_collapse_test.go`. Two ledger rows
      2026-07-31. (filed 2026-07-31 by
      `M0125-0034`'s connectivity arm; evidence
      `analysis/m0125-0034b/README.md` §"Q64's MISMATCH", probes
      `analysis/m0125-0034b/{q64body,alias_a,alias_b}.sql`).
      When a FROM list names the same table more than once under different
      aliases, goopg projects ONE alias's column value for all of them. TPC-DS
      **Q64 answers 0 rows where the oracle says 2**: its `cross_sales` CTE
      joins `date_dim` three times (`d1` on `ss_sold_date_sk`, `d2` on
      `c_first_sales_date_sk`, `d3` on `c_first_shipto_date_sk`) and selects
      `d1.d_year as syear, d2.d_year as fsyear, d3.d_year as s2year`. goopg and
      PG return the **same 26 rows** — the 18-way join is correct — but goopg
      spreads them over 9 `syear` values (1994–2002) against PG's 5
      (1998–2002), reporting first-sales years as sold years. The outer query
      then filters `cs1.syear = 1999 and cs2.syear = 2000`, so the wrong years
      empty the answer.
      **Reduced to six relations** in `alias_a.sql`: `y1 = y2 = y3` on every
      row where PG gives `1998 | 1993 | 1993`. Note the query also emits five
      separate `1993|1993|1993` groups under `GROUP BY 1,2,3` — **the grouping
      keys are distinct while the projected columns are not**, which is the
      same "right grouping, wrong projection" signature `M0125-0013` found in
      Q47's CTE body and is the strongest single clue: start where the GROUP BY
      key expressions and the target-list expressions are resolved against the
      scan layout, not in the join.
      **Ruled out BY MEASUREMENT, do not re-test:** it is NOT the connectivity
      reorder that exposed it. `alias_a.sql` and `alias_b.sql` differ only in
      where `customer` sits in the FROM list — arm A fires the pass, arm B is a
      fixed point so the pass declines entirely — and goopg's output is
      **byte-identical** in both. It reproduces with no Cartesian product in
      the plan and is independent of FROM order.
      **Severity:** this is a silent wrong answer, which this milestone's
      banner ranks above a timeout item. It was previously unreachable — Q64
      TIMEOUTs at HEAD even with a **1848 s** budget — and is now reachable by
      a 20-second query, which is the only reason it is visible at all.
      Acceptance: `alias_a.sql` matches PG column-for-column, and Q64 →
      `64|OK|2|<oracle ck>` in the SF0.5 gate. Bar: units +
      `tpch-spotcheck.sh` + TPC-H plan-diff + the full 99-query SF0.5 gate.

- [x] **M0125-0045 — the same qualifier-blind key collapses AGGREGATE slots:
      `count(d1.y)` and `count(d2.y)` dedup onto one** (filed 2026-07-31 by
      `M0125-0044`, which fixed the GROUP-BY-key half of the identical cause).
      `aggregateCallKey` builds its dedup key from `parserExprKey`, whose
      ColumnRef arm drops the table qualifier, so two aggregates over two
      aliases of one table hash equal and `buildAggregateStage`'s
      `if _, exists := aggByKey[k]; exists { continue }` discards the second.
      **Measured, not inferred:** a planner probe on
      `SELECT count(d1.y), count(d2.y) FROM fact, dim d1, dim d2 WHERE …`
      resolves BOTH targets to agg slot 0. PostgreSQL keys aggregate equality
      on the resolved argument Vars (`equal()` over `Aggref->args`,
      `postgres/src/backend/nodes/equalfuncs.c`), which separates them by
      varno. Same right-cardinality/wrong-values signature as -0044,
      M0125-0013 and M0125-0009. Resume point: give `aggregateCallKey` the
      contested-key treatment -0044 gave GROUP BY — detect a collision on
      `parserExprKey` where `qualifiedGroupKey` differs, and key those calls on
      the qualified form. **No SF0.5 query exercises this**, so the gate can
      neither prove nor disprove a change: acceptance must be a planner unit
      test plus a PG-oracle diff on a hand-written query, NOT the sweep. Bar:
      units + `tpch-spotcheck.sh` + the full 99-query SF0.5 gate (as a
      no-regression check, not as evidence). Ledger row 2026-07-31.
      **↳ FIXED and landed 2026-08-01 (loop #19).** The -0044 contested-key
      treatment applied to aggregates: `qualifiedAggregateCallKey` (same
      reflective qualifier walk, over the whole FuncCall), collection dedups
      on the qualified key, `buildAggregateStage` marks blind keys whose
      qualified forms differ (`aggregateAmbiguous`) and keys contested calls
      via `aggregateByKeyQual`, both resolution sites (post-aggregate +
      havingAgg outer-ref) dispatch contested calls through it. Acceptance
      met: 4 planner unit tests (aggregate_alias_collapse_test.go) +
      byte-identical PG-oracle diff on hand-written asymmetric-NULL data
      (count(d1.y)=3 vs count(d2.y)=1, three query shapes). No-regression:
      units, tpch-spotcheck PASS, full SF0.5 gate PASS=95 MISMATCH=0
      CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4 with ZERO cell movement vs loop
      #18. Design `docs/design/0125-0045-aggregate-alias-slot-collapse.md`;
      one ledger row 2026-08-01 (PG merges by resolved-form equality; the
      parser-form key can split count(y)/count(t.y) of one binding —
      redundant slot, never a wrong answer).

## M0126 — Cost-driven planning made production-viable (filed 2026-07-31)

Milestone: `docs/milestones/0126-cost-driven-planning-production-viability.md`.
Source: `analysis/cost-driven-second-try-200731/` — README (verdict), **09**
(stages + the UNITS/SMOKE/SPOT/PLAN/DS05/DIFF gate vocabulary every item below
uses), **10** (kill switches + rollback), **07** (cost-model interaction). The
bundle is the design of record; `docs/design/0126-*` are thin implementation
specs.
**Priority: immediately after M0125 (filed by the USER 2026-07-31)** — above
the M-NIGHTLY backlog and above M0123. See the amended Current Priority banner.
Not selected while any M0125 item is open.

**Read before picking any task here.** (1) Dropping MultiHashJoin is not a
neutral refactor: `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md:189-196`
records Q5/Q21 HANG, Q9 timeout, Q10 11.4×, Q18 4.3×, Q7 1.9× — and Q2 18.8× /
Q8 4.1× the other way. The direction is not predictable from the code change,
so it is measured per commit, and `scripts/tpch-spotcheck.sh` (Q12/Q13 row
counts) would have passed every completing one of those green. (2) **Q5
contains no MultiHashJoin** (verified — `analysis/cost-driven-second-try-200731/evidence/judge-verifications-20260731.txt`
V1/V7): the worst regression in the evidence set is an ORDER failure, not a
fusion failure. (3) DS05 content-verifies **57 of 99** queries (42 are
`ck=n/a`); the DIFF harness is the primary correctness instrument, not a
supplement — record every DS05 result as "57/99 content-verified, 42/99
count-only". (4) Every implementation task runs in a **git worktree off pinned
clean HEAD**, staged by explicit pathspec, never `git add -A`, and re-runs its
own named guard test after any rebase or handoff (bundle 10 §6).

**Acceptance bar for the milestone (measured by M0126-0012, re-measured by
-0013 if triggered):** TPC-H SF1 **22/22 complete** with zero hang/OOM/timeout;
total wall time within **+20 %** of the FASTER of the pinned R0 integer-planner
baseline (captured by -0001, before anything lands) and a contemporaneous
final-HEAD integer arm; **no single query worse than 2×** the faster of those
two baselines; TPC-DS SF0.5 gate with **zero row-count and zero checksum
deltas**. If the bar is not met, -0012 completes as a **documented no-go** that
triggers -0013; a no-go is a successful completion — an unmeasured outcome is
the only failure.

**Conditionality is entered by measurement, not judgement.** Three decision
forks (plus -0004 gated by -0003's interim A/B, and -0009/-0010 gated by
-0008's bar check):
-0005's ~1.5× decision skips the fusion band (-0006/-0007) entirely — the
bundle's best outcome; -0012 flips the default or records the no-go; -0013
(USER-filed remediation) fires only on that no-go. A fork not entered is a
recorded outcome, never a silent skip — each un-entered conditional task owes a
deferral-ledger row (-0013's row must name bundle 07 §7, whose argument — the
planner otherwise never learns the cascade is expensive — survives a passing
bar).

- [ ] **M0126-0001 — Pre-measurement confound removal + pinned R0 baseline.** Capture the R0 acceptance-bar baseline FIRST (timed
      22-query TPC-H SF1 at default config on a verified quiet host →
      `analysis/cost-driven-second-try-200731/evidence/r0-baseline.txt`, plus
      `make plan-snapshot-capture LABEL=m0126-base`), then land bundle Stage −1
      (packer guard `len(keys) != len(scans)-1` in `collectMultiHashTables`,
      `internal/planner/bushy.go` — fails closed, declines to pack) and Stage
      −1b (`VirtualSlot.Materialize()` at `internal/executor/slot.go:167-169`
      must clone arena-backed Datums like `drainRowsBounded` does) as two
      separate correctness commits, never folded into a perf commit.
      Design `docs/design/0126-0001-packer-key-guard-and-slot-clone.md`.
      Acceptance: guard + clone landed as two commits; both R0 artefacts
      committed BEFORE either. A PLAN diff from the guard means that query was
      returning wrong rows — record it prominently as a bug fix.
      Bar: UNITS + SMOKE + SPOT + PLAN + DS05 per code commit (the R0 capture
      commit is docs/evidence only).
- [ ] **M0126-0002 — `EstimateRows` `*MultiHashJoin` arm + plan re-baseline.**
      `internal/planner/cardinality.go:38+` has no `*MultiHashJoin` case, so
      every packed MHJ estimates **0 rows** and every ancestor's
      `BuildLeft`/algorithm decision above a packed chain is taken on that zero
      — in the default configuration, today (`buildLeft` needs BOTH sides > 0,
      `bushy.go:1375`). Add the arm (estimate consistently with the `*Join`
      arm's method), hand-review the EXPECTED plan diffs, re-capture the
      snapshot. **Blocking: no -0005 measurement before this is baselined** —
      otherwise a packing A/B moves two variables.
      Design `docs/design/0126-0002-mhj-cardinality-arm-and-plan-rebaseline.md`.
      Acceptance: no MHJ estimates 0 rows; every PLAN hunk enumerated and
      classified (improvement/regression/neutral) in the commit message;
      snapshot re-captured; DS05 rows/checksums unchanged.
      Bar: UNITS + SMOKE + SPOT + PLAN (diffs expected, hand-reviewed) + DS05.
- [ ] **M0126-0003 — Live-path de-materialisation + slot-taking hash-key
      evaluator.** Stage 0a-live: `*VirtualSlot` fast path in
      `Slot.fillFromTupleSlot` (`internal/executor/opnode.go:129-150`) reading
      `v.Get(i)` straight into `s.Cells` — kills the `acquireRow` + double copy
      on the live path (`joinOpKernelNext`, `opnode.go:868-876`). Stage 0b:
      extract `evalHashKeyDatumSlot` (from `evalHashKeyDatum`,
      `operators_join_agg.go:960-968`, which takes a `Row` and cannot be reused
      as-is) and evaluate keys against a `VirtualSlot` over
      `{realSide, nullOtherSide}`, deleting the per-probe-row `lazyKeyRow`
      memcpy (`:653-659`, `:1219-1232`) — build loops and probe path switch in
      the SAME commit (sibling-path rule). 0b is a hard prerequisite of -0006.
      Design `docs/design/0126-0003-live-path-dematerialisation-and-slot-key-eval.md`.
      Acceptance: two commits (0a-live, 0b); zero plan diffs; never folded with
      any cost-model change.
      Bar: UNITS + SMOKE + SPOT + PLAN (ZERO diffs — any diff is a failure) +
      DS05.
- [ ] **M0126-0004 — Legacy `Build`-path slot chaining.** **CONDITIONAL on
      -0003's interim A/B showing the legacy path still carries bench
      traffic** (the decider is -0003 alone — -0005 runs after this task) (expected IN: `buildRec` migrates no `Aggregate`, so every
      aggregate-topped TPC-H star runs its joins under legacy `Build` — bundle
      02 §9). Hold the child's `TupleSlot` as a source of the join's output
      `VirtualSlot`; the F7 contract is binding — the child does NOT return a
      stable slot object (`lazyVirtualOut` / `lazyOuterOnlySlot` / fresh
      `Materialize()` / fresh `asSlot`), so re-bind the source per probe pull
      with a copy fallback, and ship a fan-out test (multiple matches per probe
      row). If the slab serves every benchmarked query, close as
      measured-unnecessary with a ledger row — no speculative lifetime work.
      Design `docs/design/0126-0004-legacy-build-path-slot-chaining.md`.
      Acceptance: chaining + fan-out test landed, or the measured-unnecessary
      close with its ledger row.
      Bar: UNITS + SMOKE + SPOT + PLAN (zero diffs) + DS05.
- [ ] **M0126-0005 — Stage 0 A/B + fusion go/no-go decision.** No code. TPC-H
      SF1 A/B (65433) with/without -0003(+-0004), **`mhjPackingEnabled` forced
      off** (`SetMHJPackingEnabled`, `bushy.go:582-587`), matched server age /
      GOGC / GOMEMLIMIT, plus an MHJ-on reference arm →
      `analysis/cost-driven-second-try-200731/evidence/stage0-ab.txt`. **Derive
      the packing query set at the measurement HEAD by EXPLAIN** — never
      inherit a historical list (F15). **DECISION FORK: cascade within ~1.5× of
      fused MHJ on the packing set → SKIP -0006 and -0007 entirely** (ledger
      rows; the bundle's best outcome); else they are IN with the residual gap
      quantified.
      Design `docs/design/0126-0005-stage0-ab-and-fusion-decision.md`.
      Acceptance: evidence file committed with per-query times, HEAD SHA, env,
      and the written decision; an unwritten decision is an incomplete task.
      Bar: SPOT per arm + DS05 at the measurement HEAD.
- [ ] **M0126-0006 — Fusion scaffolding + differential harness (switch OFF).**
      **CONDITIONAL on M0126-0005** ("a large gap remains"). `buildEnv`
      threading through `Build`/`buildRec` (root, `inWorker` from
      `newGatherOp`'s closure `executor.go:213-219`, under-instrumentation flag
      from `explainOp.Open`, switch state, memoised Q0; `Build(plan)` stays a
      wrapper — the largest single piece, budget it); new
      `internal/executor/fused_hash_join.go` (`tryFuseHashCascade` per bundle
      05 Q0–Q9 fail-closed, `fusedHashJoinOp` per 04 §5-7 incl. C15 re-entrant
      Open), called FIRST in the `*planner.Join` arm of BOTH builders; KS1
      `GOOPG_RUNTIME_JOIN_FUSION` (default OFF) + KS2 `…_MIN_LEVELS=3`; a
      `collectShareableJoins` case or a never-coexist assertion (F4);
      decline-reason counters (R10). Nine named tests incl.
      `TestFusedCascadeMatchesUnfused` (ordered-text DIFF) and
      `TestFusedSchemaElementWiseIdentity` (width alone must not gate — F1).
      Design `docs/design/0126-0006-fusion-scaffolding-and-differential-harness.md`.
      Acceptance: all nine tests present and green; with the switch off every
      gate is bit-identical to the pre-task run (no-op in production by
      construction).
      Bar: UNITS + SMOKE + SPOT + PLAN + DS05 (all bit-identical) + DIFF.
- [ ] **M0126-0007 — Fusion enablement and measurement.** **CONDITIONAL on
      M0126-0006.** No new code: KS1 on in the measurement environment only.
      **F12 trap: force `SetMHJPackingEnabled(false)` for every measurement —
      never via `GOOPG_COST_DRIVEN_JOINORDER=1`, which conflates order into the
      A/B.** Deliver the six-item matrix: DIFF over the whole corpus; DS05 zero
      row AND checksum deltas; SPOT; a low-`work_mem` spill run identical
      fused/unfused with non-zero temp files both sides (C8/R4); SF1 A/B →
      `evidence/stage2-ab.txt` + decline histogram; SMOKE (R11). "Leave the
      switch off permanently" is a legitimate recorded completion. KS1 flips
      off without debate on any DS05/SPOT/DIFF delta, any new hang/OOM in a
      previously-completing query, or any new pg-regress diff (bundle 10 §4).
      Design `docs/design/0126-0007-fusion-enablement-measurement.md`.
      Acceptance: matrix complete with zero correctness deltas, and either a
      measured win exceeding Stage 0's or the recorded off-permanently verdict.
      Bar: DIFF + DS05 + SPOT + low-work_mem run + SF1 A/B + SMOKE.
- [ ] **M0126-0008 — Cost-driven order re-validation with symmetric
      timeouts.** The 2026-07-24 A/B pair used 600 s vs 300 s and is invalid as
      a comparison. Re-run `GOOPG_COST_DRIVEN_JOINORDER=1` vs default at
      post-Stage-0 HEAD, SAME timeout both arms, matched fresh servers, →
      `evidence/stage3-order-ab.txt` with the per-query table
      `query | R0 s | integer s | cost-driven s | ratio | bar verdict
      (clause-by-clause)`. Tests 07 §5's hypothesis: if Q9 collapses with no
      planner change, the order was never wrong — the executor was. Prior
      failure set to watch: Q5/Q21 HANG, Q9 timeout, Q7/Q10/Q18 2–11×.
      **FORK: zero bar-clause failures → skip -0009/-0010** (ledger rows).
      Design `docs/design/0126-0008-cost-driven-order-symmetric-revalidation.md`.
      Acceptance: evidence file with clause-by-clause verdicts, not narrative.
      Bar: SPOT per arm + DS05 at measurement HEAD.
- [ ] **M0126-0009 — Order-failure attribution (diagnosis only, bounded).**
      **CONDITIONAL: -0008 leaves ≥1 query failing.** Per failing query: ONE
      attribution pass, ≤2 measured probes, `EXPLAIN ANALYZE` both arms, verdict
      naming exactly one of — (a) cardinality estimate (cost-model 14's §2-§5
      thesis is refuted, do not re-test), (b) join-order preference (doc 15),
      (c) build-side memory not modelled (EXPECTED for Q5/Q9/Q21 — the planner
      has no work_mem analogue; routes to -0013's evidence), (d) executor
      per-row cost surviving Stage 0. NO code changes. Q5 is (a)/(b)/(c) by
      construction — it contains no MHJ. Unattributed-after-budget → ledger row
      + no-go input to -0012.
      Design `docs/design/0126-0009-order-failure-attribution.md`.
      Acceptance: one `evidence/order-attribution-Q<N>.txt` per failing query +
      a summary table (query → class → routing).
      Bar: evidence committed; SPOT hygiene after probe servers.
- [ ] **M0126-0010 — Bounded order-quality / cardinality fixes.**
      **CONDITIONAL: -0009 produced ≥1 class-(a)/(b) attribution.** ≤1 fix per
      query, its own commit, A/B'd on its own query AND the full 22, reverted
      if it does not move its own query. HARD PROHIBITIONS (07 §6 / doc 15): no
      new penalty multiplier on cost totals, no shape preference, no global
      NDistinct rewrite. STOP: bar met, or 2 attempts/query, or 4 landed
      commits total — then close with residuals named and ledger rows filed.
      Class-(c) work is NOT attempted here (it belongs to -0013; mixing it in
      makes every A/B two-variable).
      Design `docs/design/0126-0010-bounded-order-quality-fixes.md`.
      Acceptance: each landed fix moved its own query; reverted attempts keep
      their measurements under `evidence/`.
      Bar: UNITS + SMOKE + SPOT + PLAN (cost-driven-arm diffs hand-reviewed) +
      DS05 + per-query timed A/B, per commit.
- [ ] **M0126-0011 — Retire `MultiHashJoin` as a plan node (default off, code
      retained).** **UNCONDITIONAL (sequencing-gated: -0005 decided and -0007
      green/declined/skipped — both resolve on every path); the bundle's
      "packing queries no longer regress" precondition is demoted to a
      REPORTING obligation (record any residual regression's magnitude; it
      feeds -0012's verdict) because -0012 structurally requires this task
      (single-variable A/B).** Flip
      `mhjPackingEnabled` default → false (`bushy.go:580`); KEEP
      `rewriteMultiWayChain`, the node and `multi_hash_join.go` in-tree,
      reachable via `SetMHJPackingEnabled`, ≥1 full nightly cycle (deleting
      code and changing behaviour in one commit is unbisectable). Four-step
      snapshot procedure (bundle 06 §5): PLAN green → flip → HAND-REVIEW every
      diff (each must be exactly one MHJ node expanding into N−1 Hash Joins
      over the same scans) → `make plan-snapshot-capture LABEL=post-mhj-retire`.
      Ship `scripts/pg-plan-shape-diff.sh` REPORT MODE ONLY. Settle
      `generateMultiHashJoinPath` (`pathgen.go:100-105`) in writing. **MUST
      precede -0012** (single-variable A/B — `GOOPG_COST_DRIVEN_JOINORDER=1`
      sets `mhjPackingEnabled=false` as a side effect, `bushy.go:18-21`).
      Design `docs/design/0126-0011-mhj-plan-node-retirement.md`.
      Acceptance: default flipped; diffs hand-reviewed and enumerated; new
      baseline captured; MHJ code still reachable; the pathgen decision written.
      Bar: UNITS + SMOKE + SPOT + DS05 + PLAN (hand review) + full TPC-H SF1
      sweep vs `sf1-r5-default-cb37d166.txt` and the -0005 prediction.
- [ ] **M0126-0012 — Acceptance measurement + conditional default flip.**
      Terminal fork, AFTER -0011. Measure ALL FOUR bar clauses at final HEAD vs
      R0 (protocol = -0008's: symmetric timeouts, quiet host, matched fresh
      servers) → `evidence/acceptance-run-1.txt`, clause-by-clause. **Bar met →
      flip**: cost-driven join order default-on (`bushy.go:13-21`, env var
      becomes opt-out; note — do not delete — the now-redundant
      `mhjPackingEnabled=false` side-effect), re-snapshot, update every "ships
      off by default" statement (enumerated in the commit message). **Bar
      missed → documented no-go that TRIGGERS -0013** (failing clauses,
      residual queries, their -0009 attributions). Both outcomes are successful
      completions; an unmeasured outcome is the only failure. No partial flip.
      Design `docs/design/0126-0012-cost-driven-order-default-flip.md`.
      Acceptance: `acceptance-run-1.txt` with all four clauses judged; then the
      flip commit or the no-go document.
      Bar: full timed TPC-H SF1 acceptance run + DS05 (zero deltas) + SPOT +
      PLAN re-snapshot with hand review + UNITS + SMOKE.
- [ ] **M0126-0013 — Build-side memory-aware hash costing (conditional
      remediation, filed by the USER 2026-07-31) + bar re-check.**
      **CONDITIONAL: fires ONLY on an -0012 no-go.** The Q5/Q9/Q21 HANGs are
      not a duration problem — the planner has NO work_mem/hash_mem analogue
      and picks enormous build sides unpenalised (`hashJoinCost`,
      `internal/planner/cost_funcs.go:100-112`, omits batching by its own
      comment). Add the hash-table byte estimate + work_mem-overrun
      penalty/spill cost — the analogue of PG `initial/final_cost_hashjoin`
      (`postgres/src/backend/optimizer/path/costsize.c:4134,4160`) +
      `ExecChooseHashTableSize` (`nodeHash.c:658`), budget =
      `work_mem × hash_mem_multiplier` (`get_hash_memory_limit`,
      `nodeHash.c:3622`). `goopg_hash_entry_width_multiplier` default 6.0
      (48-byte Datum realism) applied to the MEMORY/SPILL DECISION ONLY — never
      the cost total (doc 15's GOOPG_MAT_MULT lesson); a unit test proves a
      non-spilling join's total is bit-identical under multiplier changes. Max
      2 commits, never with an executor change. Then RE-RUN -0012's measurement
      protocol unchanged → `evidence/acceptance-run-2.txt` with a delta column
      vs run 1, and re-judge: pass → execute -0012's flip path; fail → the
      milestone's final documented no-go. If -0012 passed and this never fires:
      close as not-triggered with a ledger row naming bundle 07 §7 and a
      successor owner — a silent skip is a bookkeeping defect.
      Design `docs/design/0126-0013-build-side-memory-aware-hash-costing.md`.
      Acceptance: either (model landed with the multiplier-placement unit test
      green, default-config plans byte-identical, cost-driven-arm diffs
      enumerated, acceptance-run-2 recorded with deltas, final verdict written)
      or (the not-triggered close with its 07 §7 ledger row).
      Bar: UNITS + SMOKE + SPOT + PLAN (default arm ZERO diffs) + DS05 + the
      re-run acceptance protocol.

## Archived — complete (see `completed_milestones/completed_fix_plan_009.md`)

M0117 (CLOG ↔ PostgreSQL subsystem alignment), M0118 (Upstream Isolation Spec
Suite Pass-Through), M0120 (WordPress WP-CLI verification execution + evidence),
M0121 (WordPress WP-CLI verification remediation).

## Archived — complete (see `completed_milestones/completed_fix_plan_008.md`)

M0096 (RC isolation feature impl + spec pass), M0100 (RC isolation runtime
closure / 21-spec pass), M0102 (heterogeneous streaming-replication +
SIGKILL-failover E2E), and the two completed Maintenance fixes
(MAINT-STATEGUARD-RECONCILE, MAINT-TPCH-RELOAD). Earlier milestones:
`completed_fix_plan_001.md` .. `completed_fix_plan_007.md`.

---

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

Design: `docs/design/0095-0003-*`. Goal: port the client-tools-tap suite and the
engine features its `t.Skip`'d scripts need. (`pg_ctl` 001–004 already PASS.)

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

- [ ] **M0095-0003** — `pg_basebackup` 010/011/020 PASS (backup execution,
      `-X stream`/`-X fetch`, manifest + SHA-family checksums, in-place tablespace,
      `READ_REPLICATION_SLOT`). **Remaining:** `030 recvlogical` — blocked on logical
      decoding (not implemented; tracks with the logical-replication milestone / D-004).
      Deferred: on-disk `pg_tablespace` heap visibility (independent shared-catalog
      runtime write — see ledger). **Not actionable until logical decoding lands.**

## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by M0094
(recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.
Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP** — `001_basic` ported (DU-001, CLI-only).
      `002–010` (schema dump, dump/restore round-trip, parallel, filter-file,
      connstr) DEFERRED on broad catalog-view parity + round-trip; being advanced
      one catalog gap at a time via the self-promoting
      `TestPort_PgDumpConnectionSetup` guard (CSV row DU-002, slice-by-slice).
      Design `0110-0001-pg-dump-tap-port.md`. **2026-07-06:** the guard now also
      probes the actual dump+restore round trip (pipe `pg_dump`'s stdout into
      `psql` against a fresh `CREATE DATABASE`). Found + fixed the `xmloption`
      GUC gap (every pg_dump archive opens with `SET xmloption = content;`).
      That probe then surfaced the REAL remaining blocker for 002–010: goopg's
      `catalog.InMemory` has no per-database namespace at all (`CreateDatabase`
      only registers a name; every object store — tables/schemas/collations/
      etc. — is one flat server-wide map), so a dump can never restore into a
      genuinely separate database. This is milestone-scale (per-database
      catalog + storage isolation throughout `internal/catalog`), not a slice
      — see the 2026-07-06 deferral-ledger row for the resume point. Until that
      lands, further DU-002 slices should keep targeting catalog-view parity
      (the round-trip probe stays a soft `t.Logf`, not a hard gate).
- [ ] **M0110-0002 — pg_waldump TAP** — `001_basic` CLI tier ported (WD-001);
      WAL-format readability guarded by W-001 (`TestPort_WALPgWaldumpCompat`).
      **Remaining (WD-002, deferred):** `002_save_fullpage` — needs goopg to emit
      PG-decodable FPI/heap WAL with backup blocks (+ hash/gin/gist/spgist/brin AMs
      for the server tier). Design `0110-0002-*`.
- [ ] **M0110-0003 — pg_amcheck TAP** — `001_basic` (AC-001) + `002_nonesuch`
      (AC-002) ported; CREATE SCHEMA + user-schema table restart-durability enablers
      landed. **Remaining (AC-003, deferred):** `003_check`, `004_verify_heapam`,
      `005_opclass_damage` — need `verify_heapam()` SRF + opclass catalog parity +
      index AMs. (2026-07-07: the `datconnlimit=-2` invalid-DB filter sub-section is
      now fully closed, both its SQL-visibility half — M0119-0006 AC-002 — and its
      connect-time-enforcement half — M0119-0006 AC-002 residual #1 follow-up;
      **2026-07-07, same day:** positive `datconnlimit` connection-count throttling
      (residual #2) is also now closed — `activity.ActivityRegistry.CountByDatName`
      + a `Server.handleStartup` check reject a non-superuser connection once a
      database's live connection count exceeds its configured limit, mirroring
      `postinit.c`'s `CheckMyDatabase`/`CountDBConnections` (FATAL `53300`). AC-002
      now has zero remaining residuals; per-role `rolconnlimit` throttling (a
      separate PG mechanism) remains untracked, per the matching ledger row.) Design
      `0110-0003-*`.

## M0119 — Deferral-Ledger Backlog Consumption (filed 2026-06-29)

Milestone: `docs/milestones/0119-deferral-ledger-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`.ralph/deferral_ledger.md`. Goal: drive every open (`status = -`) ledger row to
closure — implement the deferred scope, or verify it already landed and mark the
row `resolved`.

**Per-task rule (applies to every M0119 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<source-id>-NNNN-*.md` and index it in `docs/design/README.md`, and
(2) have that design doc pass an agent review. Implementation starts only after
the reviewed design doc exists. (The triage task M0119-0001 was doc-only, exempt.)

**Already landed (see git history / deferral ledger):** M0119-0001 triage
(2026-06-29: 224 open rows → 178 resolved, 46 remain), M0119-0002 (CLOG tail),
M0119-0003 (initdb options — empty backlog), M0119-0008 (isolation residual —
only the infeasible `deadlock-parallel` spec remains), M0119-0009 (UPDATE/DELETE
conflict-wait), plus the landed sub-slices of -0004 (NULLS NOT DISTINCT
enforcement + upsert arbiter) and -0005 (pg_waldump WD-003/WD-004 canonical
prune-WAL round-trip). The four open items below carry the remaining unbuilt scope.

- [ ] **M0119-0004 — pg_dump 002–010 TAP** (source: M0110-0001). Schema dump,
      dump/restore round-trip, parallel, filter-file, connstr — advance the
      catalog-view parity battery slice-by-slice (guard
      `TestPort_PgDumpConnectionSetup`; resume = next catalog getter gap tracked in
      `.ralph/working_set.md` / ledger). Two general SQL-engine gaps surfaced here
      remain: deferred-constraint *checking at COMMIT* (goopg checks immediately)
      and any residual dump-fidelity items.
_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

- [ ] **DU-002 next blocker — `invalid column numbering in table "nninh4"`**
      (source: TestPort_PgDumpConnectionSetup, M0122-0007 4e). pg_dump errors on
      a pg_attribute attnum-ordering / column-numbering gap for the inheritance
      test table `nninh4` (dropped/inherited columns). Not a registry-scoping
      collision — a different subsystem (pg_attribute physical attnum order).
      Repro: `go test -v -run '^TestPort_PgDumpConnectionSetup$'
      ./internal/testport/`; inspect the emitted pg_attribute rows for nninh4.
- [ ] **M0119-0005 — pg_waldump server tier** (source: M0110-0002). `002_save_fullpage`
      (WD-003) + live `pg_waldump --rmgr=Heap2` round-trip DONE. **Still open:** only
      `001_basic.pl`'s server-dependent tier (per-rmgr/relation/block filtering) —
      needs hash/gin/gist/spgist/brin index AMs.
- [ ] **M0119-0006 — pg_amcheck server tier** (source: M0110-0003). `002_nonesuch`
      … `005_opclass_damage`; `CREATE EXTENSION amcheck` + `verify_heapam()` SRF on
      top of `internal/amcheck` + opclass catalog parity. Largest open cluster
      (~29 ledger rows): index AMs, `box`/`int4range`/`int4[]` types, STORAGE
      EXTERNAL TOAST corruption, and the heapallindexed heap-scan producer.

- [ ] **M0119-0007 — pg_basebackup recvlogical** (source: M0095-0003). `030 recvlogical`
      — blocked on logical decoding (tracks the logical-replication milestone / D-004).

> This task list is **seeded, not exhaustive.** M0119-0001 triage plus every future
> deferral-ledger entry (any new `status = -` row) feed additional M0119 tasks over
> time; the milestone's living nature means it need not be complete at filing.

## M0122 — Unimplemented-Feature Backlog Consumption (filed 2026-07-04)

Milestone: `docs/milestones/0122-unimplemented-feature-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`unimplemented_feat.json` (repo root; 181 entries generated 2026-07-02 from the
commit log). Goal: drive every `open` feature entry to closure — implement the
deferred scope, or verify it already landed and mark the entry `resolved`.

**⚠️ Verify-before-implement (READ FIRST):** `unimplemented_feat.json` is a
2026-07-02 snapshot and **may list features that are already implemented** — 24
entries have an `unclear`/absent `code_audit` and 61 have an open matching ledger
row (7 overlap both). When you pick up ANY M0122 task, FIRST re-verify each
candidate against current HEAD (grep/read code, probe a live goopg, check
ledger/fix_plan/git log). If it already exists, set the entry's `status` to
`resolved` (cite the proof) and DO NOT re-implement. Only build genuinely-missing
scope.

**Per-task rule (applies to every M0122 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<id>-NNNN-*.md` and index it in `docs/design/README.md`, and (2) have
that design doc pass an agent review. Implementation starts only after the
reviewed design doc exists. (The triage task M0122-0001 is doc-only, exempt.)
Tracking field = a per-entry `status` (`open`/`resolved`) added by M0122-0001,
mirroring M0119's ledger `status` column.

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

- [ ] **M0122-0003 — EXPLAIN output & pg_stat instrumentation** (~7, partial).
      FORMAT XML/YAML **done** (2026-07-04, loop #8) — design:
      `docs/design/0122-0003-explain-format-xml-yaml.md`.

- [ ] **M0122-0006 — On-disk catalog persistence & shared catalogs** (~8).
      Persistent `pg_index` heap, index column order (ASC/DESC/NULLS) across
      restart, `pg_tablespace` visibility, `pg_database.datconnlimit` write

- [ ] **M0122-0007 — DDL / admin commands / ctl / GUC config** (~14). CREATE/DROP
      DATABASE full DDL, REINDEX, tablespaces, ALTER FUNCTION/COLUMN,
      planner/jit GUC stubs. (`goopg reload`/SIGHUP and `goopg restart` done.)
      **Remaining M0122-0007 items:** CREATE/DROP DATABASE physical storage
      isolation (template copy on CREATE, real directory removal on DROP —
      the architectural item), `WITH (FORCE)` connection-termination (no
      cancel-backend mechanism), REINDEX CONCURRENTLY physical rebuild.

  - [x] `unimplemented_feat #135 (pg_get_expr)` (2026-07-10, this loop) —
      **fixed the live `pg_index.indpred`/`indexprs` NULL-sentinel bug and
      narrowed the entry.** `catalog.InMemory.PGIndexRowsForDBOid`
      (`internal/catalog/catalog.go`) hardcoded `indexprs`/`indpred`/
      `indcoloptions` to `""` — for a `text` column that reads back as a
      non-NULL empty string, not SQL NULL. This diverged from the executor
      heap-row twin `buildUserPGIndexRow`
      (`internal/executor/pg18_user_catalog_rows.go`, already correct) and
      broke two live-SQL behaviors: `indpred IS NOT NULL` (the canonical
      partial-index probe tools use) matched EVERY index, and
      `pg_get_expr(indpred, indrelid)` returned `''` instead of the WHERE
      predicate on a partial index (and `''` instead of NULL on a plain one).
      Now emits `VirtualNull` for non-partial `indpred`, `idx.PredicateString`
      for partial, and `VirtualNull` for `indexprs`/`indcoloptions`, mirroring
      the heap twin exactly (sibling-path sync). Also established that
      pg_get_expr's pass-through is architecturally correct for goopg — every
      populated pg_node_tree column (adbin/conbin/relpartbound) stores
      pre-formatted deparsed SQL text, not a serialized node tree, so no
      reconstruction is needed. New E2E regression tests
      `TestPgIndexIndpredPartialVsPlain` (through `pg_get_expr`) +
      `TestPgIndexRowsIndprIndexprsNullSentinel` (direct row-cell guard)
      in `internal/executor/pg_index_indpred_test.go`; `code_audit` narrowed
      in `unimplemented_feat.json`; deferral-ledger row appended for the one
      remaining open slice (expression-index `indexprs` never populated from
      `Index.ColExprs` — no client path other than a direct
      `pg_get_expr(indexprs)` reads it; psql \d / pg_dump use
      `pg_get_indexdef`). Gates: `go build ./...`/`go vet` clean; `go test
      ./internal/catalog/... ./internal/executor/...` PASS;
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS
      (0 failed, all 3 workloads).
  - [x] `unimplemented_feat #135 (pg_get_expr, indexprs slice)` (2026-07-10,
      follow-up loop) — **closed the live-path expression-index `indexprs` gap
      the prior row deferred.** Added shared
      `catalog.IndexExprsText(idx) (string, bool)`
      (`internal/catalog/catalog.go`): joins `idx.ColExprStrings[i]` for each
      expression key column (`Columns[i]==""`, ordinal-0 in `indkey`) verbatim,
      comma-separated, returning `("", false)` when none so the caller emits
      `VirtualNull`. Wired into `PGIndexRowsForDBOid`, so
      `pg_get_expr(indexprs, indrelid)` on an expression index now returns the
      deparsed text (byte-matched to PG 18.3: `lower(b)`, `(a + c), upper(b)`,
      `(a * c)`, NULL for a plain index). The natural deparse in
      `ColExprStrings` already carries the right parens — an earlier draft that
      reused `buildIndexDefString`'s `indexKeyIsBareFuncCall` rule
      double-wrapped binexprs into `((a + c))` and was corrected to a verbatim
      join. **Heap twin deliberately NOT changed:** `buildUserPGIndexRow` still
      writes `indexprs=NULL` because `DecodePGIndexPhysicalRow` infers `indpred`
      from the bytes after `indoption` assuming `indexprs` is NULL (two
      consecutive nullable varlenas, no tuple null bitmap available to the
      decoder) — writing it would corrupt an expression index's `indpred` on
      restart. Deferred (ledger row 2026-07-10) to a null-bitmap-aware decoder.
      Tests: `internal/executor/pg_index_indexprs_test.go`
      (`TestPgIndexIndexprsExpressionIndex` E2E + `TestIndexExprsTextParenAndNullRules`
      unit); design doc `docs/design/0122-0019-*` Follow-up section + README.
      Gates: `go build`/`go vet` clean; `go test ./internal/catalog/...
      ./internal/executor/...` PASS; `scripts/tpch-spotcheck.sh` PASS
      (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke` PASS (0 failed, 3 workloads).

  - [x] `unimplemented_feat #5(b) (multi-field / HH:MM:SS interval literals)`
      (2026-07-11, this loop) — **closed the prior interval-literal row's
      deferred item (b): multi-field and time-body interval literals now parse
      end-to-end** (`interval '1 day 05:00:00'`, `interval '1 year 2 mons 3
      days 04:05:06.789'`, bare `interval '05:00:00'`/`'04:05'`/`'100:00:00'`,
      `interval '1 day 2 hours 3 minutes 4 seconds'`) — exactly the shapes
      goopg's own `formatInterval`/`intervalout` emits, so goopg can re-parse
      its own interval output. Hoisted the pure interval-body math into a new
      `internal/parser/interval.go` (`ParseIntervalMagnitude`,
      `IntervalUnitToParts`, new `ParseIntervalBody` tokenizer — mirrors PG
      `DecodeInterval`: `<magnitude> <unit>` pairs in any order interleaved
      with `[+-]HH:MM[:SS[.ffffff]]` time words, each field carrying its own
      sign; accepts intervalout abbreviations `mon(s)`/`min(s)`/`sec(s)`/
      `hr(s)`) as the **single source of truth** for both sibling paths:
      `evalIntervalLit` (typed literal) and `parseIntervalCastString`
      (`::interval`/CAST, now a one-line `parser.ParseIntervalBody` delegate).
      Multi-field bodies decode once into `IntervalLit.PreMonths/PreDays/
      PreMicros` (`PreComputed`, threaded through 2 `planner.go` conversions +
      `plpgsql_runtime.go`). Byte-for-byte vs PG 18.3. Deferred (ledger
      2026-07-11): bare-number default-unit (`interval '5'`→seconds),
      week/decade/century, single-letter units, full interval-typmod grammar.
      Tests: `interval_subday_test.go` `TestMultiFieldIntervalLiterals` +
      sibling-path guard `TestParseIntervalBodySingleFieldMatchesUnitToParts`;
      `TestIntervalCastFromStringInvalidSyntax` updated. Design doc
      `docs/design/0003-0006-*` new Follow-up + README row. Gates: build/vet
      clean; executor/parser/planner/analyzer suites PASS;
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke (hook).

- [ ] **M0122-0008 — Auth / roles / multi-DB isolation / encoding** (~6). SASLprep
      / channel binding / `scram_iterations`, RBAC + `SET SESSION AUTHORIZATION`,
      encoding constraints during bootstrap/runtime.
      **RBAC for INSERT/UPDATE/DELETE landed (2026-07-05, this loop,
      M0097-0040):** `dmlPrivilegePermitted` (`internal/executor/
      operators_storage.go`) checks the existing `tableACLs`/
      `HasTablePrivilege` store (TRUNCATE/MAINTAIN already consulted it;
      plain DML never did) pre-lock in `insertOp`/`updateOp`/
      `deleteOp.Open`, raising `42501` for a non-superuser, non-owner role
      missing the matching GRANT. FK-cascade deletes and the logical-
      replication apply worker write heap pages directly and are
      unaffected. Tests: `internal/executor/storage_dml_test.go`'s
      `TestDMLRequiresTablePrivilege`. Design:
      `docs/design/0118-0039-truncate-conflict-privilege-model.md` Follow-up
      section; `unimplemented_feat.json` M0097-0040 updated in place.
      **`SELECT` enforcement landed (2026-07-05, same day):**
      `seqScanOp.Open`/`indexScanOp.openPrep`/`indexOnlyScanOp.Open` now call
      `dmlPrivilegePermitted(ctx, tbl, "SELECT")`, with a
      `catalog.IsSystemRelation(tbl.OID)` carve-out that always permits
      SELECT on pg_catalog/information_schema (no pg_init_privs-equivalent
      default-ACL seeding exists). Tests:
      `TestSeqScanRequiresSelectPrivilege`,
      `TestIndexScansRequireSelectPrivilege`,
      `TestSystemCatalogSelectAlwaysPermitted`. Design doc Follow-up section
      extended; `unimplemented_feat.json` updated in place.
      **View-owner privilege check landed (2026-07-06):** `execCreateView`
      now stamps the creating role as `Owner` (previously every view was
      silently owned by the bootstrap superuser); new
      `planner.tagViewOwnerScans` (`internal/planner/view_privilege.go`)
      tags every scan inside an inlined view's plan tree with the view
      owner's role (skipped under `WITH (security_invoker = true)`, now
      actually enforced for the first time); `dmlPrivilegePermittedAs`
      lets the three SELECT-gated scan operators check that tagged role
      instead of the querying session's own. `GRANT SELECT ON view TO
      role` alone (no base-table grant) now works. Tests:
      `internal/planner/view_privilege_test.go`,
      `internal/executor/storage_dml_test.go`'s
      `TestScanOperatorsUseViewOwnerPrivilegeOverride`,
      `internal/executor/view_owner_privilege_test.go`. Design:
      `docs/design/0118-0039-truncate-conflict-privilege-model.md` Follow-up
      section; ledger row (resolved). **Still open (ledger, scope
      boundary):** the view's own ACL is never checked against the
      querying role (no plan node represents "scan the view itself"), so a
      role with zero grants anywhere can still read a view whose owner has
      base-table access — needs a preliminary per-statement RTE-style
      permission pass, materially larger than this follow-up.
      SASLprep/channel binding/`scram_iterations`, encoding constraints.
      **`scram_iterations` wired into password hashing landed (2026-07-08,
      this loop):** the GUC (`internal/config/defaults.go`, registered
      since earlier but never read anywhere) is now actually consulted by
      `CREATE`/`ALTER ROLE ... PASSWORD 'plain'` — `auth.NewSCRAMSecret`'s
      hardcoded `scramDefaultIterations` (4096) call site is replaced with
      a new `auth.NewSCRAMSecretWithIterations(pw, iterations)` sibling,
      and `applyRoleAttrOptions` (`internal/server/role_ddl.go`) now takes
      the same `currentGUCResolver` its two callers already had in scope
      (previously only used for `SET ... FROM CURRENT`); a new
      `resolveScramIterations` helper reads the live `scram_iterations`
      value. The auth/verification side needed no change — `scram.go:326`'s
      server-first-message already renders `s.secret.Iterations` parsed
      back out of the stored verifier, not a constant, so it was already
      correct; only the write path was pinned to the default. Tests:
      `internal/server/role_ddl_scram_iterations_test.go`
      (`TestCreateAlterRolePasswordHonorsScramIterationsGUC`), confirmed
      non-vacuous via `git stash`. Design: `docs/design/
      root-0021-role-auth-persistence.md` new "Follow-up: `scram_iterations`
      GUC wired into password hashing" section; `docs/design/README.md`
      root-0021 row extended. Gates: `go build ./...`/`go vet ./...` clean;
      `go test ./internal/server/... ./internal/auth/... ./internal/config/...`
      PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
      failed transactions, all 3 workloads). SASLprep and TLS channel
      binding remain fully unimplemented in this cluster (separate,
      larger slices — SASLprep needs a Unicode-normalization dependency
      not currently in `go.mod`; channel binding needs TLS
      tls-server-end-point wiring).
      **SASLprep landed (2026-07-08, this loop):** ported `pg_saslprep`
      (`postgres/src/common/saslprep.c`, RFC 4013) to
      `internal/auth/saslprep.go`, including its exact algorithm quirk
      (prohibited-output/bidi checks run against the mapped-but-pre-NFKC
      codepoints, not the final normalized output) and its six Unicode
      range tables, mechanically extracted from the C source by a one-off
      script into `internal/auth/saslprep_tables.go` (not hand-transcribed,
      to guarantee byte-identical data — 396+360+36+... range pairs).
      NFKC normalization added via a new `golang.org/x/text` dependency
      (`unicode/norm.NFKC`, NOT `secure/precis.OpaqueString`, which is
      NFC per RFC 8265 — a different, non-upstream-compatible form).
      Wired into `auth.NewSCRAMSecretWithIterations` (mirrors
      `pg_be_scram_build_secret`) and
      `SCRAMSecret.VerifySCRAMSecretFromPassword` (mirrors
      `scram_verify_plain_password`), both falling back to the raw
      password on SASLprep failure like upstream. The live SCRAM
      handshake itself needed no change — it never re-derives from a
      plaintext password, only checks the client's proof against the
      already-prepped stored secret. Tests:
      `TestPGSASLPrepRFC4013Examples`/`TestPGSASLPrepInvalidUTF8`/
      `TestSCRAMSecretNormalizesEquivalentUnicodeForms`
      (`internal/auth`) plus a differential e2e test against a REAL
      libpq client — `TestE2E_SASLPrepMatchesRealLibpqClient`
      (`internal/testport`), since lib/pq's own Go SCRAM client does no
      SASLprep at all (confirmed by reading its `scram` package), so only
      real `psql` (linked against upstream's own saslprep.c) meaningfully
      proves cross-implementation byte parity; a role's password
      containing U+2168 ROMAN NUMERAL NINE, stored via `CREATE ROLE`,
      authenticates against the plain ASCII canonical form "IX" over a
      real SCRAM handshake. Added `cluster.PSQLWithPassword` test-infra
      helper (`internal/testutil/cluster/cluster.go`) since none of the
      existing psql helpers allowed a non-empty `PGPASSWORD`. Design:
      `docs/design/0049-0003-scram-sha-256.md` new §3.1 + README row.
      Deferral-ledger row appended (channel binding — the other named gap
      — remains open, needs TLS wiring that doesn't exist anywhere in the
      server yet, a materially larger separate slice). Gates:
      `go build ./...`/`go vet ./...` clean; `go mod tidy` clean;
      `go test ./internal/auth/... ./internal/server/...` PASS; targeted
      `internal/testport` e2e SCRAM/role tests PASS;
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS
      (0 failed transactions, all 3 workloads).
- [ ] **M0122-0009 — WAL / recovery / crash-consistency infra** (~16). WAL segment
      recycling, `WALInsertLock` array (parallel inserts), MultiXact WAL,
      `pg_subtrans` truncation. Gate: `-race` + recovery E2E (WAL practice card).
      **`pg_subtrans` truncation landed (2026-07-09, this loop):** the bucket's
      one previously-untouched item with no prior progress notes.
      `internal/mvcc/subxact_visibility.go`'s `SubxactMap` (in-memory
      `parents`/`aborted` maps) and `internal/mvcc/subxact_slru.go`'s
      `SubtransSLRU` (`pg_subtrans/` SLRU mirror, M0117-0003) had no removal
      primitive at all — both grew without bound for the lifetime of a
      cluster, a gap the M0117-0003 design doc's own "Known follow-ups"
      section had already flagged and left for later. New
      `SubtransSLRU.TruncateBefore(oldestXact)` unlinks segment files whose
      highest page strictly precedes `oldestXact`'s SLRU page (new
      `SubtransPagePrecedes`, `CLOGPagePrecedes`'s twin scaled to
      `subtransXactsPerPage`), mirroring `clog.go`'s `truncateSLRUSegments`
      (reuses the same-package `parseSLRUSegName` helper). New
      `SubxactMap.Truncate(oldestXact)` prunes both in-memory maps
      (wraparound-safe via `storage.XIDPrecedes`) and calls through to the
      SLRU when persistence is enabled; nil-safe when it isn't. New
      `CheckpointerConfig.TruncateSubtransFn` (`internal/wal/checkpointer.go`)
      invoked from `runCheckpoint` right after `TruncateCLOGFn`, same
      best-effort/non-fatal error treatment. `internal/initdb/open.go` wires
      it to the identical `horizon = min(datfrozenxid, OldestXmin)`
      computation `TruncateCLOGFn` already uses — safe because any subxid
      below that horizon's top-level xact already has a direct CLOG
      `Committed`/`Aborted` status (never `SubCommitted`), so its parent link
      is never consulted again; reusing the existing, already-tested horizon
      avoids introducing a second, subtly-different computation. No WAL
      record emitted — matches upstream `TruncateSUBTRANS`, which PG likewise
      never WAL-logs (`pg_subtrans` is disposable across a crash;
      `StartupSUBTRANS` just zeroes it on restart — goopg's restore-on-restart
      choice per M0117-0003 is an orthogonal, deliberate divergence unrelated
      to this fix). Tests: `TestSubtransSLRUTruncateBefore`/
      `TestSubxactMapTruncate`/`TestSubxactMapTruncateNoPersistence`
      (`internal/mvcc/subxact_truncate_test.go`),
      `TestCheckpointerCallsTruncateSubtransFn`/
      `TestCheckpointerTruncateSubtransFnErrorIsNonFatal`
      (`internal/wal/checkpointer_test.go`). Design:
      `docs/design/0122-0009-pg-subtrans-truncation.md` (new);
      `docs/design/0117-0003-pg-subtrans-restore-on-restart.md`'s "Known
      follow-ups" section updated to point at it; `docs/design/README.md`
      index updated (both the new row and the 0117-0003 row's stale
      follow-up note). Gates: `go build ./...` clean; `go vet`/`go test`
      clean+PASS across `internal/mvcc`/`internal/wal`/`internal/initdb`
      (the `internal/initdb` package test takes ~5 min, ran to completion);
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
      failed transactions, all 3 pgbench workloads).
      **WAL segment recycling landed (2026-07-09, next loop):**
      `Writer.RemoveOldSegments` previously unlinked every obsolete segment;
      upstream recycles some of them (rename into a future segment slot,
      `RemoveXlogFile`/`InstallXLogFileSegment`) so a later `openSegment`
      skips its own create+zero-fill+directory-fsync. New `Config.MinWALSize`
      (wired from the previously-unread `min_wal_size` GUC via
      `internal/initdb/open.go`'s `OpenOptions.WALMinSize`, read in
      `cmd/goopg/main.go` the same way `max_wal_size` already is) caps how
      many of the newest obsolete segments `state.removeOldSegments`
      (`internal/wal/writer.go`) recycles via the new `recycleSegmentFile`
      helper (rename + zero-fill + fsync, reusing `preallocateSegment`) vs
      unlinks; `<= 0` (default) disables recycling, byte-identical to prior
      behaviour. The recycle target is the lowest free segment slot at or
      after the keep segment (mirrors upstream's `find_free` scan, never
      clobbers a live/already-recycled segment). Diverges from upstream by
      zero-filling the recycled segment (upstream leaves old content as-is,
      relying on per-record CRC to bound recovery scans) because goopg's
      `reader.go` graceful-EOS heuristic checks for an all-zero tail instead
      — an unzeroed recycled segment's leftover well-formed old record would
      pass CRC validation and be misread as live WAL. `SlotAwareRetainer.Retain`
      (`internal/wal/retention.go`) threads the new `recycled` count through
      to its summary log (`segments_recycled` alongside `segments_removed`).
      Tests: `TestRemoveOldSegmentsRecyclesUpToMinWALSize` (confirms recycled
      files are genuinely zero, not stale content — the load-bearing
      correctness check), `TestRemoveOldSegmentsRecycleCapExceedsObsoleteCount`
      (`internal/wal/retention_test.go`); pre-existing `TestRemoveOldSegments*`
      tests (implicit `MinWALSize=0`) continue to pin the recycling-disabled
      default. Design: `docs/design/0122-0009-wal-segment-recycling.md` (new,
      cites upstream `xlog.c` source); `docs/design/README.md` index updated.
      Deferral-ledger row filed: only the `min_wal_size` floor half of
      upstream's `XLOGfileslop` sizing is implemented, not the
      checkpoint-distance-estimate/`max_wal_size`-ceiling halves. Gates:
      `go build ./...`/`go vet ./...` clean; `go test`/`go test -race` PASS
      across `internal/wal`, `internal/mvcc`; `go test ./internal/initdb/...`
      PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
      failed transactions, all 3 pgbench workloads). **Still open in this
      bucket (at that point):** `WALInsertLock` array (parallel inserts),
      MultiXact WAL, eager next-segment lookahead.
      **Eager next-segment lookahead landed (2026-07-09, next loop):**
      closes the `unimplemented_feat.json` M0007 entry left over from the
      original 0007-0001 preallocation design (deferred there as "gives
      lower commit-path tail latency at rollover but adds a background
      goroutine"). `state.openSegment(segNo)` (`internal/wal/writer.go`) now
      calls a new `state.eagerPreallocSegment(segNo+1)` right after handling
      `segNo` itself, spawning a background goroutine that zero-fills a
      `<segfile>.eager<pid>.tmp` file and durably links it into place
      (`os.Link`, EEXIST-tolerant no-clobber — mirrors upstream
      `XLogFileInit`'s temp-then-link pattern) so a genuine rollover usually
      finds the next segment already preallocated instead of paying for it
      synchronously; new `state.eagerInFlight`/`eagerMu` dedupe concurrent
      triggers for the same segment, `state.eagerWG` lets `close()` wait for
      any still-running job before tearing down `s.files`. Found and fixed a
      real correctness hazard this exposed on the way: `detectWritePos`
      (consulted only at writer-reopen time, e.g. after a restart) used to
      trust every non-last on-disk segment as "fully used" via file size
      alone, content-scanning only the literal highest-numbered file — a
      crash between eagerly preallocating `segNo+1` and the writer ever
      really reaching it leaves a fully zero, never-written `segNo+1` file
      *above* the genuinely partially-written `segNo`, which the old logic
      would silently overshoot past (trusting `segNo` as full while
      content-scanning the empty phantom instead). Fixed by walking backward
      from the highest segNo, trimming any segment that is both full-size
      and scans as entirely empty, before running the existing (otherwise
      unchanged) last-segment scan logic — the full-size guard is what keeps
      this from misclassifying a genuine short/legacy empty-last segment
      (already handled correctly, unchanged). Also fixed a pre-existing
      pg_waldump test (`TestPGWaldumpParsesEmittedWAL`) that the new second
      on-disk segment file exposed: bare `pg_waldump -p walDir -s .. -e ..`
      (no explicit filename) auto-detects `WalSegSz` by opening "any
      WAL-looking file" via unordered `readdir()` (`identify_target_directory`
      / `search_directory`, `pg_waldump.c`), which can hand it the all-zero
      segment 1 and misread its zeroed long-page-header as `xlp_seg_size=0`
      — a pre-existing upstream pg_waldump quirk (real PG WAL directories
      have the same kind of preallocated future segment during normal
      operation), fixed by naming the exact start segment as a positional
      argument, the standard unambiguous invocation form. Tests:
      `internal/wal/writer_detect_test.go`'s new
      `TestDetectWritePos_IgnoresEagerPhantomFutureSegment` (confirmed
      non-vacuous by reverting the trim loop — fails with the exact
      predicted writePos overshoot); `internal/wal/wal_test.go`'s
      `TestPreallocationCounters` updated to `w.stateRef.eagerWG.Wait()`
      before each assertion and re-derive the new one-segment-ahead expected
      totals (was implicitly relying on the background goroutine losing a
      race it had no guaranteed way to lose). **Independent review caught a
      genuine bug in the first cut:** `close()`'s `eagerWG.Wait()` ran
      *before* `flushUpTo`, but with `Config.WALBuffers > 0` (the default)
      `flushUpTo` can itself be the first caller of `openSegment` for a
      segment (buffered bytes never drained until Close), which then kicks
      off a brand-new eager job with zero chance to have started before that
      earlier `Wait()` already returned — `Close()` could return while a
      background goroutine was still writing into the WAL directory. Fixed
      by moving `Wait()` to after `flushUpTo` (the last remaining
      `openSegment` caller inside `close()`). New test
      `TestClose_WaitsForEagerJobTriggeredByItsOwnFlush`
      (`writer_detect_test.go`, confirmed non-vacuous — fails ~95% of runs
      with the ordering reverted, a real race not a rare corner case).
      Design:
      `docs/design/0007-0001-wal-segment-preallocation.md` new "Follow-up
      (2026-07-09): eager next-segment lookahead" section;
      `docs/design/README.md` row updated; `unimplemented_feat.json`'s
      matching M0007 entry flipped to `resolved` (task_id retagged
      `M0122-0009`). No deferral-ledger row needed — nothing new was left
      unimplemented (the pre-existing `posix_fallocate` deferral,
      unaffected by this loop, was already tracked in the design doc before
      this change). Gates: `go build ./...` clean; `go test`/`go test -race`
      PASS across `internal/wal`; `go test ./internal/initdb/...` PASS (no
      regression in Init+Open+restart recovery, ~5 min); `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
      scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
      pgbench workloads). **Still open in this bucket:** `WALInsertLock`
      array (parallel inserts), MultiXact WAL.
      **2026-07-09 (next loop) — reconciliation, no code change:** verified
      the `WALInsertLock` array line item is in fact already fully landed
      (M0107-0007 slice B, `docs/design/0107-0007ah-wal-tryappend-rwmutex.md`
      / `0107-0007aj-wal-segment-cross-reservation.md` and ~28 sibling design
      docs `0107-0007a`..`0107-0007aj`) — it was a stale leftover in this
      bucket's summary line, not real remaining work. Confirmed by code
      reading, not just docs: `internal/wal/padded_mutex.go`'s
      `appendLockSet` is an 8-stripe `[8]paddedMutex` array
      (`appendLockStripes = 8`, matching PG's `NUM_XLOGINSERT_LOCKS` /
      `WALInsertLocks[]`, `xlog.c`/`xlog.h`), genuinely wired (not dead code)
      into every hot append path via `stripe_writer_core.go`'s
      `c.locks`/`stripeAppend`/`stripeAppendBuild`/`stripeAppendBuiltEmitted`,
      selected per-caller by `stripeForProcNum(procNum)`. `writer.go`'s
      `tryAppend` fast path takes `state.appendMu.RLock()` (shared) then the
      one stripe lock via `AppendXLogPayload`, so up to 8 concurrent
      backends genuinely append into disjoint WAL-buffer regions in
      parallel; only the replica WAL-apply path (`appendRaw`, sequential by
      nature — a single WAL receiver, matching upstream) and
      checkpoint/recovery resets take the exclusive `Lock()`. Re-ran the
      three tests that pin this concurrency model at HEAD (unmodified):
      `go test -race -run
      'TestConcurrentTryAppendProceedsInParallel|TestTryAppendRLockDoesNotBlockSiblings|TestConcurrentAppendAcrossSegmentBoundariesNoOverflow'
      ./internal/wal/...` — all 3 PASS. No fix_plan/deferral-ledger row
      needed (nothing was actually missing); this bucket's remaining named
      item is `MultiXact WAL` only. Surveyed that one too before choosing
      this reconciliation instead: `internal/multixact/` is an explicitly
      unwired, in-memory-only primitive (package doc: "the risky hot-path
      integration ... lands in later loops on top of this verified
      primitive") — no SLRU-backed offsets/members store, no xmax-stamping
      wiring, no WAL record kinds at all (`grep -rn Multixact
      internal/wal/*.go` only hits two placeholder comments). WAL-logging
      multixact creation presupposes a durable multixact SLRU exists to
      protect first — that foundation doesn't exist yet, and building it
      plus wiring it into the tuple-header hot path is multi-loop,
      feature-sized work on the same class of hot path (xmax) that has
      already cost this project many multi-loop corruption-hunt threads
      (see the `M-NIGHTLY (AI-20260709-010336-082)` btree thread above) —
      correctly left deferred rather than rushed into one loop.
      **`max_wal_size` ceiling + `CheckPointDistanceEstimate` — done
      (2026-07-09, next loop, closes the deferral-ledger row from the
      original WAL segment recycling loop):** the bucket's other named
      sizing gap. New `computeSpareSegments` (`internal/wal/writer.go`)
      ports upstream's `XLOGfileslop` (xlog.c) formula as segment counts
      relative to the retention keep-segment rather than absolute
      LSN/segNo math (behaviourally equivalent, avoids needing goopg's
      1-based LSN encoding to line up bit-for-bit with upstream's 0-based
      `XLogSegNo` arithmetic); new `Checkpointer.CheckPointDistanceEstimate()`
      ports `UpdateCheckPointDistanceEstimate`'s jump-up-immediately/
      decay-slowly (90/10) EMA verbatim, fed from each `runCheckpoint`
      cycle's redo-LSN delta. New `Writer.RemoveOldSegmentsWithEstimate` +
      `SlotAwareRetainer.CheckPointDistanceEstimateFn`/`CompletionTarget`
      wire it through Retain; `cmd/goopg/main.go` reads `max_wal_size`
      (new `wal.Config.MaxWALSize` via `initdb.OpenOptions.WALMaxSize`,
      default 1024 MB matching upstream) and `checkpoint_completion_target`
      the same way `min_wal_size`/`checkpoint_completion_target` already
      feed the checkpointer's other knobs. The pre-existing
      `RemoveOldSegments` public API is unchanged behaviourally — it now
      forwards to the same formula with both new inputs zeroed, proven to
      reduce to the original `ceil(MinWALSize/SegmentSize)` floor exactly
      (every pre-existing test using it, e.g.
      `TestRemoveOldSegmentsRecyclesUpToMinWALSize`, still passes
      unmodified). Tests:
      `TestComputeSpareSegmentsMatchesMinWALSizeFloorWhenNoEstimate`/
      `TestComputeSpareSegmentsGrowsWithDistanceEstimate`/
      `TestComputeSpareSegmentsCapsAtMaxWALSize`/
      `TestRemoveOldSegmentsWithEstimateHonoursDistanceAndMax`/
      `TestSlotAwareRetainerUsesCheckPointDistanceEstimateFn`
      (`internal/wal/retention_test.go`),
      `TestCheckpointerUpdatesCheckPointDistanceEstimate`
      (`internal/wal/checkpointer_test.go`, pins the jump-up/decay-down
      shape across real `CheckpointNow()` cycles without asserting exact
      byte counts). Design: `docs/design/0122-0009-wal-segment-recycling.md`
      new "Follow-up (2026-07-09)" section; `docs/design/README.md` row
      updated; deferral-ledger row flipped to `resolved`, new row appended
      closing it. Gates: `go build ./...`/`go vet ./...` clean; `go test`/
      `go test -race ./internal/wal/...` PASS; `go test
      ./internal/initdb/... ./cmd/goopg/...` PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
      scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
      pgbench workloads). **M0122-0009's WAL-segment-recycling sizing
      sub-bucket now has no known open gap; MultiXact WAL remains the
      bucket's sole open item** (multi-loop, feature-sized — see the
      survey directly above).
- [ ] **M0122-0010 — Concurrency: buffer pool & btree locking** (~17, LARGE).
      Lehman/Yao crab-walk, `splitMu` removal, storage-pool pin-count race,
      re-enable the `-race` gate. Gate: race detector mandatory.
      **2026-07-09 loop — fixed the internal-page sibling-relink
      cross-connection race** (continuation of the M-NIGHTLY
      AI-20260709-010336-082 pgbench-reopen thread's closing note: "a
      future structural-write path added without the same re-validation
      discipline... should be treated as suspect until it's audited the
      same way"). Audited `internal/access/btree/btree_vacuum.go`'s
      remaining structural-mutation call sites for the exact bug class
      just fixed there (leaf sibling-relink using a stale unlocked
      `liveSibling` capture instead of a fresh re-derivation under the
      write-side `pinW`) and found the IDENTICAL gap one level up:
      `unlinkEmptyInternalPage` (WAL path) and
      `unlinkEmptyInternalPageFPI` (FPI fallback) — used by
      `maybeCascadeEmptyInternal` to unlink a vacuumed-empty internal
      page — both computed `leftLive`/`rightLive` via the same unlocked
      pre-pass and wrote them verbatim, exposed to the same cross-
      connection splice-then-stomp corruption `bt.splitMu` cannot
      prevent (per-`*BTree`-Go-instance only, not cross-connection).
      Fixed both to re-derive the live neighbour via a fresh
      `liveSibling` walk inside the same `pinW` hold that performs the
      write, mirroring the leaf-level fix exactly. New regression test
      `TestUnlinkEmptyInternalPagePreservesConcurrentSplice`
      (`internal/access/btree/btree_vacuum_internal_race_test.go`)
      deterministically reproduces the race with no goroutines needed:
      builds a real 3-level (root/internal/leaf) tree via `BulkCreate`
      (n=900000, same recipe as the existing
      `TestVacuumIndexPagesCascadesEmptyInternalPage`), captures a
      target internal page's real live prev/next exactly like
      `maybeCascadeEmptyInternal` does, splices a synthetic live page in
      between (simulating a same-window concurrent split on a different
      connection), then invokes the low-level unlink with the STALE
      pre-splice prev/next and asserts the splice survives instead of
      being stomped. Confirmed non-vacuous via `git stash` on
      `btree_vacuum.go` alone (fails pre-fix with the exact "stale stomp
      regression" symptom the test asserts against). Design doc
      `docs/design/0055-0003-btree-page-deletion-and-recycling-protocol.md`
      new §2.5; `docs/design/README.md` row extended. Gates: `go build
      ./...` clean; `go test ./internal/access/btree/...
      ./internal/amcheck/... ./internal/executor/...` PASS; `go test
      -race ./internal/access/btree/...` PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33). **New gap found while fixing the above,
      deferred (ledger row appended 2026-07-09):**
      `applyParentDownlinkRemoval` (shared by both the leaf and
      internal-page unlink WAL paths) removes the parent's downlink
      purely by a previously-captured slot INDEX, with no re-validation
      at write time that the item still at that index is the intended
      child's downlink — the exact index-drift race
      AI-20260706-201855-001 fixed for the intra-instance case (there
      `splitMu` closed it), but NOT for a concurrent split racing from a
      DIFFERENT connection's instance on the same parent page. This is
      the epic's next concrete resume point (see the ledger row's
      "resume point" column for the exact fix shape); the larger
      `splitMu` removal / Lehman-Yao crab-walk items in this bucket
      remain untouched by this loop.
      **2026-07-09 loop (same day, continuation) — fixed the
      `applyParentDownlinkRemoval` index-drift race named above.**
      Changed the function's signature from
      `(parentBlk storage.BlockNumber, removeSlot uint16, lsn
      storage.LSN)` to `(parentBlk, childBlk storage.BlockNumber, lsn
      storage.LSN)`: instead of trusting a slot index resolved well
      before the removal actually runs (WAL emission + sibling-relink
      writes happen in between), it now re-scans the parent's CURRENT
      item list for `it.ptr.Block == childBlk` under the same `pinW`
      that performs the removal — mirrors the §2.5 sibling-relink fix
      pattern and `findParentDownlinkByBlock`'s existing by-block
      matching, self-correcting if a cross-connection split raced in,
      idempotent no-op if the downlink was already removed by a racing
      unlink. Both call sites (`unlinkEmptyLeaf`'s and
      `unlinkEmptyInternalPage`'s WAL-emitting paths, lines ~408/~981)
      now pass the child block (`leaf.blk`/`blk`) instead of
      `req.ParentRemoveSlot`; the WAL record's own `ParentRemoveSlot`
      field is untouched (crash replay is single-threaded, so the
      stale-index concern is live-apply-only). New regression test
      `TestApplyParentDownlinkRemovalIgnoresStaleIndex`
      (`internal/access/btree/btree_vacuum_parent_downlink_race_test.go`)
      deterministically reproduces the drift (no goroutines needed):
      resolves a target leaf's parent slot on a real 2-level tree
      (`BulkCreate`, n=3000), splices a synthetic live downlink into
      the front of the parent's item list (shifting the target's true
      position by one, so the pre-splice stale slot now points at a
      different, live "victim" child), then invokes the fixed removal
      keyed on the target's block and asserts: the target's downlink is
      gone, the victim's downlink survives (proving no
      wrong-item-by-stale-index deletion), and the spliced item
      survives untouched. Confirmed non-vacuous via `git stash` on
      `btree_vacuum.go` alone — the test fails to even COMPILE pre-fix
      (`cannot use targetBlk (BlockNumber) as uint16 value`), a stronger
      signal than a runtime assertion failure. Design doc
      `docs/design/0055-0003-btree-page-deletion-and-recycling-protocol.md`
      new §2.6; `docs/design/README.md` row updated. Deferral ledger row
      dated 2026-07-09 (`M0122-0010`, "applyParentDownlinkRemoval...")
      flipped to `resolved`. Gates: `go build ./...` clean; `go test
      ./internal/access/btree/... ./internal/amcheck/...
      ./internal/executor/...` PASS; `go test -race
      ./internal/access/btree/...` PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
      scripts/ralph-precommit-test.sh` PASS (0 failed txns, all 3
      workloads). **Standing gap unchanged (not this loop's scope):**
      `bt.splitMu` is still not a real cross-connection mutex — this
      fix (like §2.5's) tolerates that by re-validating at the
      individual write site; the larger `splitMu` removal / Lehman-Yao
      crab-walk items in this bucket remain untouched.
- [ ] **M0122-0012 — Perf infra: vectorization / slot-pipeline / harness** (~19,
      ARCHITECTURAL). Borrow-semantics allocation rewrite, plannode migration,
      vectorized FilterOp/SeqScanOp, plan cache, HammerDB SF1 validation.
- [ ] **M0122-0013 — Physical/streaming replication & standby** (~10, EPIC/blocked).
      Streaming-replication epic (~25 sub-items), cascading replication,
      `STANDBY_SNAPSHOT_READY` transition.
- [ ] **M0122-0014 — Logical replication / decoding / subscription** (~11, EPIC).
      pgoutput DELETE identity, subscriber apply worker, DDL replication. Blocked
      on logical decoding (tracks D-004; overlaps M0119-0007 — dedupe).
- [ ] **M0122-0015 — Test-suite porting: amcheck / verify_heapam / pg_dump** (~8).
      `verify_heapam()` SRF + opclass parity, AC-002..005, pg_dump 002-010.
      **Overlaps M0119-0004/0006 — the triage assigns each item to ONE milestone;
      do not double-work.**
## WAL native → PG-format rework (design bundle `docs/design/wal-native-pg-format/`)

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

- [ ] **Nightly whole-suite regression batch — implementation** (~6). Design is
      DONE and committed: `analysis/tests-overview-260706/` (test-landscape
      snapshot) → `ci/design/` (6-doc architecture: S0 preflight → S1 two
      parallel lanes [units+race / testport+pgbench-smoke] → S2 solo TPC-H →
      S3 summary, plus a `flock`-guarded resident scheduler hooked from
      `~/.ralph/ralph_loop.sh`). Indexed in `docs/design/README.md` (Design
      Bundles). **Nothing under `ci/batch/` exists yet** — next step is to
      implement `ci/batch/run-nightly.sh` + `lib/common.sh` + the `stage-*.sh`
      scripts per `ci/design/01-architecture.md`'s layout, starting with S0
      preflight (cheapest to verify standalone) before wiring the two S1
      lanes. Low priority relative to the M0122 PG-compat buckets above — pick
      up only when no M0122/M0119 item is in flight, since this is
      Ralph-tooling, not user-facing PG compatibility.

## M0123 — Canonical `pg_node_tree` serialization (branch `wal-pg-nodetree`)

**Priority: DEMOTED 2026-07-28, renumbered 2026-07-28(b) when M-NIGHTLY was
parked, renumbered again 2026-07-31 when M0126 was filed: the order is
WIP-recovery (#1), M0124 (#2), M0125 (#3), **M0126** (#4), the M-NIGHTLY
backlog (#5), and M0123 (#6). Superseded wording kept below for history:
~~the order is WIP-recovery (#1), M0124 (#2), M0125 (#3), the M-NIGHTLY
backlog (#4), and M0123 (#5)~~; and before that: After WIP-recovery (#1),
M-NIGHTLY (#2), M0124 (#3) and M0125 (#4), M0123 is #5.** It remains the active focus of branch
`wal-pg-nodetree`, but this checkout (`tpcds-fix2`) closes the TPC-DS round-2
plan first — see the Current Priority banner and
`docs/design/tpcds-round2-fixes/README.md` §13.5. Milestone doc:
`docs/milestones/0123-canonical-pg-node-tree-serialization.md`; design:
`docs/design/wal-pg-identical-stream/02e-content-fidelity-and-durability.md §3`.

Goal: a canonical PG18 `pg_node_tree` serializer (new `internal/pgnodes` leaf
package: resolver + `outfuncs` + `readfuncs` + binary datum encoding) so a real
PG18 standby can EVALUATE/QUERY goopg's user column DEFAULTs (`pg_attrdef.adbin`),
extended-statistics expressions (`pg_statistic_ext.stxexprs`), and views
(`pg_rewrite.ev_action`, `pg_class.relhasrules=true`). goopg has NO OID-resolved
node tree today (name-based AST; analyzer only type-checks; runtime resolves by
name), so this is a resolver + serializer + datum codec, not just an `outfuncs`
port. Phased S0→S4; each slice = one gated commit (build/vet + touched-package
units + testport + `TestE2E_FailoverGoopgToPG`, plus the slice's standby assertion).

**Invariants (do NOT skip — see 02e §3):** graceful degradation is MANDATORY
(`unsupported.go` all-or-nothing subset check → unsupported shape falls back to
SQL text, and views additionally keep `relhasrules=false`; never FATAL, never
partial-emit). `relhasrules=true` is per-table (`catalog.Table.RuleIsCanonical`)
and HARD-coupled to a canonical `ev_action` (a non-parseable one FATALs PG's
relcache). Verification is ADVERSARIAL: the standby COMPUTES and the result is
asserted `==` goopg's own (not merely "replays without FATAL"). Datum traps:
by-value sign-extension (negative int4 → all-`0xFF` high bytes; oid zero-extends),
signed-char decimal wire form, text 4-byte varlena header, numeric reuses goopg's
existing encoder, `constcollid=100` / `consttypmod=n+4`.

- [x] M0123-S0 — forward operator/proc OID indexes from the existing seed data
      (`catalog.LookupOperatorForNode(spelling,leftOID,rightOID)` /
      `catalog.LookupProcForNode(name,argOIDs)`); the 799-row pg_operator seed was
      relocated to `internal/catalog`; pseudo-type collisions guarded by a
      round-trip check. LANDED (`10d26374`); pinning test in
      `internal/catalog/pg_node_oid_lookup_test.go` (15 operators, 6 procs,
      negatives), deterministic.
- [x] M0123-S1 — created the `internal/pgnodes` leaf package: `ir.go` (scalar IR:
      `Const`/`FuncExpr`/`OpExpr`/`RelabelType`/`CoerceViaIO`/`SQLValueFunction`),
      `datum.go` (`Const` value ↔ raw PG datum bytes + typed constructors),
      `outfuncs.go` (IR → S-expression, field order mirrors `outfuncs.c` per tag),
      `readfuncs.go` (`pg_strtok`/`nodeRead` port; unsupported tag = clean error).
      Gate: `pgnodes_test.go` pins `Out` byte-for-byte against **real PG18.3
      `pg_attrdef.adbin` goldens** captured from a live server (`adbin ==
      nodeToString`), then `Read → DeepEqual → re-Out` round-trip — 20 subtests
      green (all datum traps: negative int4 sign-extend, oid RelabelType,
      text varlena header, int8-max, bool short-len, OpExpr, FuncExpr, null Const).
      NO resolver/writer wired yet (S2). Design doc `0123-0001-pgnodes-scalar-
      serializer.md` + README index + ledger row (2026-07-19). LANDED.
- [ ] M0123-S2 — SUB-SLICE 1 LANDED (2026-07-19): `resolver_expr.go`
      (`ResolveExpr`: goopg `parser.Expr` → scalar IR via S0 `LookupOperatorForNode`)
      + `rebuild.go` (`Rebuild`: IR → goopg AST for reload) + `unsupported.go`
      (`SupportsExpr` all-or-nothing shape check), all pure `internal/pgnodes`
      additions (no wiring), gated by `resolver_expr_test.go` (10 subtests:
      canonical-Out pins for int4/neg-int4/bigint/text, `40+2`→OpExpr forward-
      resolution, full resolve→Out→Read→Rebuild→re-resolve round-trip). Supported:
      int4/int8 literals (make_const magnitude typing), unary-minus fold (doNegate,
      all-0xFF sign-ext), text literals in text context, binary OpExpr. Design doc
      `0123-0002-pgnodes-scalar-resolver.md` + README index + ledger row.
      SUB-SLICE 2 part (a) — `FuncExpr` resolution — **LANDED + VALIDATED
      (2026-07-19)**: `cmd/gen-pg-proc-data -names` now emits a
      `pgProcRetTypeByOID` leaf map, `catalog.ProcResultType` reads it, and
      `resolveFuncCall`/`rebuildFuncExpr` handle `parser.FuncCall`. The resolver
      code shipped with sub-slice 1 (`e85ccb53`) but left its `SupportsExpr`
      test asserting `upper('x')` was unsupported → HEAD `go test
      ./internal/pgnodes/` was RED. This loop captured a live PG18.3 `adbin`
      golden for `b text DEFAULT upper('x')` (funcid 871 / funcresulttype 25 /
      collid 100), confirmed the resolver matches it byte-for-byte, and
      reconciled the test (`upper('x')` now a supported case + a golden Out pin +
      a resolve→Rebuild→re-resolve round-trip case). HEAD green again.
      SUB-SLICE 2 parts (b)(c) — canonical `pg_attrdef.adbin` writer + reload —
      **LANDED (2026-07-19)**: new `pgnodes.ResolveForColumn` (exact-type-match
      gate) drives `canonicalAttrdefText` in the `writeAttrdefRow` funnel
      (`internal/executor/sys_pg_attrdef.go` / `operators_ddl.go`); the reload
      `rebuildAttrdefExpr` (`internal/initdb/catalog_heap_reload.go`)
      discriminates on the leading `{` → `pgnodes.Read`→`Rebuild`, else
      `parser.ParseExpr`. `adbin` stored as a plain `string` (nodeToString is
      pure ASCII — no `NewBytesDatum`/codec change). Gate: fast units
      (`TestResolveForColumn`, `TestCanonicalAttrdefText`, `TestRebuildAttrdefExpr`)
      + PG18.3 byte goldens + full `internal/initdb` + `TestE2E_FailoverGoopgToPG`.
      Design `0123-0003-pgnodes-attrdef-writer-reload-wiring.md`.
      SUB-SLICE 2 DEFERRED (ledger 2026-07-19, both orthogonal to node-tree
      serialization): (1) the adversarial **standby-EVAL** E2E is blocked by
      `pg_attrdef` catalog completeness — a real PG18 standby can't build a usable
      `pg_attrdef` tupledesc from goopg's streamed `pg_attribute` (relid 2604 has
      no usable `adbin` column) and `AttrDefaultFetch` opens the unmaterialized
      `adrelid/adnum` index (OID 2656); needs bootstrap `pg_attribute` completion
      + 2656/2657 index materialization first. (2) canonical **`stxexprs`** is
      blocked on a `List` IR node (`stxexprs` is a `List` of trees, `(...)` not
      `{...}`) — arrives with S3/S4.
- [ ] M0123-S3 — SUB-SLICE 1 LANDED (2026-07-19): the pure `internal/pgnodes`
      query-tree **codec** (no wiring), mirroring how S1 landed the scalar codec
      before S2's resolver. New IR `Query`/`RangeTblEntry`/`RTEPermissionInfo`/
      `FromExpr`/`RangeTblRef`/`TargetEntry`/`Var`/`Alias` (`ir_query.go`) + two
      new wire primitives (`Bitmapset` `(b ...)`; String value node `"col"`) +
      `outfuncs_query.go` (full ~45-field `Query` skeleton in `outfuncs.c` order,
      `OutRuleAction` outer `(...)` wrapper) + `readfuncs_query.go` (inverse, and
      the shape gate: `readQuery` validates every fixed field, `readRangeTblEntry`
      rejects non-`RTE_RELATION`/`tablesample`/`securityQuals`). Gate:
      `query_roundtrip_test.go` — `Out(Read(golden)) == golden` byte-for-byte
      against 2 live-captured PG18.3 `ev_action` goldens (view w/ `WHERE` qual;
      view w/ computed `upper()` + no qual) + structural spot-check
      (`selectedCols==[8 9]`, qual `OpExpr.opno==521`) + `hasAggs true` rejection.
      `go test ./internal/pgnodes/` + `go vet` green. Design
      `0123-0004-pgnodes-query-serializer.md`.
      SUB-SLICE 2 part (a) — the resolver — **LANDED (2026-07-19)**:
      `resolver_query.go` (`ResolveViewQuery`: goopg `*parser.SelectStmt` +
      `RelationResolver` → IR `Query` for single-base-relation SELECT views;
      computes `Var` varno/varattno, the `selectedCols` `+7` bias
      (`-FirstLowInvalidHeapAttributeNumber`), `resorigtbl/resorigcol`, `resname`,
      the fixed `RTE_RELATION`/`AccessShareLock`/`ACL_SELECT`/`perminfoindex=1`
      skeleton; the `OpExpr`/`FuncExpr` builders `buildOpExpr`/`buildFuncExpr`/
      `funcCallGuard` were extracted from S2's `resolver_expr.go` so scalar +
      query-scoped resolvers build byte-identical nodes; pure leaf, NO wiring).
      Gate `resolver_query_test.go`: resolve→`OutRuleAction` == both live PG18.3
      goldens byte-for-byte + resolve→`Out`→`Read`→re-`Out` round-trip +
      structural spot-check + 10-case `ErrUnsupported` matrix; `go test
      ./internal/pgnodes/` + `go vet` green.
      SUB-SLICE 2 part (b) — the reload inverse — **LANDED (2026-07-19)**:
      `rebuild_query.go` (`RebuildViewQuery(*Query) (*parser.SelectStmt, error)`),
      the query-tree analogue of S2's scalar `Rebuild`. Self-describing (no
      `RelationResolver`): FROM name = the single RTE `eref.aliasname`, column
      names = that `eref.colnames`, so `Var.varattno`→`colnames[attno-1]`.
      Fixed point resolve→`Out`…`Read`→`RebuildViewQuery`→resolve reproduces the
      input `Query` byte-for-byte (`rebuildTarget` emits an explicit alias only
      when `resname` differs from the forward `targetName` auto-derivation — the
      exact inverse). Refactor: `rebuild.go`'s `rebuildOpExpr`/`rebuildFuncExpr`
      made recursion-injectable (`*With(node, rec)`) so the query scope reuses
      the identical opno/funcid reconstruction with `Var`-aware recursion. Gate
      `rebuild_query_test.go`: both goldens resolve→rebuild→re-resolve→
      `OutRuleAction` == golden byte-for-byte + rebuilt-AST structural check +
      producer/reader-mismatch matrix; `go test ./internal/pgnodes/` + `go vet`
      + `go build ./...` green. Design 0123-0004 §"Sub-slice 2b" + README index.
      SUB-SLICE 2 part (c) — the ENGINE WIRING — LANDED (2026-07-19):
      `catalog.Table.RuleIsCanonical` field; executor `viewRelationResolver`
      (pgnodes.RelationResolver over the live catalog) + `viewColumnCanonicalType`
      (atttypid/typmod/collation read back from buildUserPGAttributeRow so a Var's
      vartype can't drift from the standby's pg_attribute); `canonicalViewEvAction`
      resolves a plain view's ev_action to canonical `({QUERY...})` bytes else SQL
      text; `syncTableToCatalogHeap` sets `RuleIsCanonical` BEFORE
      buildUserPGClassRow (load-bearing ordering — the streamed pg_class heap
      row is the standby's relhasrules source). relhasrules reads the flag in BOTH
      the heap row (`pg18_user_catalog_rows.go`) and the virtual builder
      (`catalog.go:6978`); system/info-schema stay false. Reload
      `rebuildViewFromEvAction` discriminates leading `({` →
      ReadRuleAction->RebuildViewQuery (restores the flag) else parser.Parse.
      Gates: TestViewColumnCanonicalType/TestViewAttrIndexConstants (executor),
      TestRebuildViewFromEvAction (initdb), TestPort_ViewsSurviveRestart
      (relhasrules=true survives restart), TestE2E_FailoverGoopgToPG (a real PG18
      standby reports relhasrules=true and pg_get_viewdef PARSES the canonical
      ev_action via stringToNode + deparses it back to the exact SELECT). Design
      0123-0004 sub-slice 2c. DEFERRED (ledger 2026-07-19): row-level standby eval
      — a direct `SELECT * FROM v` on the promoted standby still fails 42809
      (rewriter uses relcache rd_rules, not the pg_rewrite scan pg_get_viewdef
      uses; copied pg_internal.init caches a ruleless entry). Next: S4 coverage OR
      the rd_rules standby-eval unblock.
- [ ] M0123-S4 — coverage + hardening: more datum types (numeric, timestamptz,
      more), `CASE`/`BoolExpr`/`NullTest` in target lists, more operators; and the
      byte-diff oracle gate (goopg's emitted `ev_action`/`adbin` `==` real-PG18's
      for the identical DDL, `:location` normalized). Decompose into sub-slices;
      each its own gated commit.
      SUB-SLICE 1 LANDED (2026-07-19): canonical **`BoolExpr` (AND/OR/NOT) +
      `NullTest` (IS [NOT] NULL)** scalar nodes — codec + resolver + rebuild in
      one commit (encode↔decode↔resolve↔rebuild). `bool`-typed column DEFAULTs
      (`bool DEFAULT (a IS NOT NULL)`, `DEFAULT (x AND y)`) now emit canonical
      `pg_attrdef.adbin` via the already-wired `ResolveForColumn`→
      `canonicalAttrdefText` path (was SQL-text fallback). Reproduced PG's
      `makeAndExpr` n-ary flattening (`(a AND b) AND c` → one 3-arg BoolExpr;
      parenthesised right stays nested) + the exact left-nested rebuild inverse
      (fixed point). `BoolExpr` custom_read_write `:boolop` bare token
      (and/or/not). Gate `internal/pgnodes/bool_null_test.go` (green): 6
      live-captured PG18.3 adbin goldens byte-for-byte + Read round-trip +
      resolve→Rebuild→re-resolve DeepEqual + nested-right + bad-boolop reject.
      Design `0123-0005-pgnodes-bool-null-scalar.md`.
      SUB-SLICE 2 LANDED (2026-07-19): VIEW-QUERY bool/null wiring. The three
      scalar helpers (`resolveBoolBinary`/`resolveBoolNot`/`resolveNullTest`)
      became thin wrappers over recursion-injectable `*With` variants
      (`scopedResolve` fwd, `func(Node)(parser.Expr,error)` rebuild), mirroring
      how 0123-0004 sub-slice 2b made rebuildOpExpr/rebuildFuncExpr injectable.
      `queryScope.resolveExpr` (resolver_query.go) now dispatches BooleanConst /
      IsNullExpr / UnaryOp{OpNot} / BinaryOp{OpAnd|OpOr} through them, and
      `viewRebuildScope.rebuildExpr` (rebuild_query.go) adds BoolExpr/NullTest
      cases — so a multi-condition view qual (`... WHERE src IS NOT NULL AND
      client > 0`) now emits a CANONICAL ev_action + relhasrules=true (was
      SQL-text fallback). Gates (GREEN): `internal/pgnodes/view_bool_null_test.go`
      (2 live PG18.3 goldens: v3 AND/NULLTEST/OPEXPR, v4 OR/nested-NOT/NULLTEST —
      forward + codec round-trip + rebuild fixed point + structure) and
      `TestE2E_FailoverGoopgToPG` (new b5c_view2: a real PG18 standby reports
      relhasrules=true + pg_get_viewdef PARSES the bool/null ev_action). Design
      0123-0005 §"Sub-slice 2" + README index.
      SUB-SLICE 3 LANDED (2026-07-19): canonical **`numeric` (OID 1700) Const
      datums**. A decimal/scientific literal now packs into the on-disk
      `NumericData` varlena (`datum.go` `parseNumericVar`=set_var_from_str+strip_var,
      `varlena`=make_result_opt_error: short/long header + int16 NBASE=10000
      digits, all little-endian) byte-for-byte identical to PG18.3's adbin/
      ev_action; `decodeNumericVar`+`text`(=get_str_from_var) invert for rebuild
      preserving dscale trailing zeros (`100.50`≠`100.5`). Wired `*parser.NumericConst`
      (+ folded negative via `OpUnaryNeg`→doNegate) into BOTH the scalar and
      query-scoped resolvers/rebuild. Gate `internal/pgnodes/numeric_test.go`
      (green): 6 live scalar adbin goldens (100.50/0.001/9999.9999/π-digits/-2.5/1E-10)
      + a live `vn` view ev_action golden (`amount > 100.50 AND rate < 0.001`),
      each forward byte-for-byte + codec round-trip + resolve→Rebuild→re-resolve
      fixed point. DISCOVERY: integer-valued numeric defaults (`DEFAULT 0`,
      `DEFAULT 12345`) are int4 wrapped in an `int4_numeric` cast FuncExpr
      (funcid 1740), NOT numeric Consts — still SQL-text (deferred). Design
      0123-0005 §"Sub-slice 3".
      SUB-SLICE 4a LANDED (2026-07-19): the implicit **`int`→`numeric` cast
      FuncExpr** (closes the sub-slice-3 discovery). A bare integer literal in a
      numeric column context now resolves to an implicit-cast `FuncExpr`
      (`int4_numeric` funcid 1740 / `int8_numeric` funcid 1781, funcformat 2)
      byte-for-byte identical to PG18.3's `adbin` — `resolveIntLiteral` wraps the
      int4/int8 Const via new `wrapIntToNumericCast` when `expected==OidNumeric`
      (negative fold before the cast); `rebuild.go` `isImplicitIntToNumericCast`
      +`rebuildFuncExprWith` rebuild it back to the inner integer literal (fixed
      point). `numeric DEFAULT 0/12345/-5/5000000000` now emit canonical
      `pg_attrdef.adbin` via the already-wired `ResolveForColumn`→
      `canonicalAttrdefText` path (was SQL-text fallback). Gate
      `internal/pgnodes/numeric_cast_test.go` (5 live PG18.3 adbin goldens:
      forward byte-for-byte + ResolveForColumn accepts + codec round-trip +
      resolve→Rebuild→re-resolve fixed point + rebuilt-shape + int-context
      no-wrap guard); sibling gates reconciled (resolver_expr_test /
      sys_pg_attrdef_test / catalog_heap_reload_attrdef_test flip the numeric-int
      case to canonical); `TestE2E_FailoverGoopgToPG` green. Design 0123-0005
      §"Sub-slice 4a".
      SUB-SLICE 4b LANDED (2026-07-19): canonical **`timestamptz` (OID 1184)
      Const datums**. A `timestamptz` column DEFAULT literal now folds to a
      canonical by-value `int64` Const of μs-since-2000 (constlen 8, consttype
      1184) byte-for-byte identical to PG18.3's `pg_attrdef.adbin` (PG folds an
      "unknown" string literal to the target type at parse time via
      coerce_type→timestamptz_in, so adbin is a folded Const not a cast). Uses
      PG's exact integer `date2j`/`j2date` Julian-day math (datum.go
      `NewTimestamptzConst`/`parseTimestamptzMicros`/`formatTimestamptzUTC`); the
      `resolver_expr.go` StringConst case + `rebuild.go` rebuildConst gain a
      timestamptz branch (fixed point). DETERMINISTIC subset only (explicit
      offset / `Z` / `epoch`); a TimeZone-dependent form (no offset / bare date)
      degrades to SQL text. Gate `internal/pgnodes/timestamptz_test.go` (4 live
      PG18.3 adbin goldens + parser math table + graceful-degradation matrix) +
      executor `TestCanonicalAttrdefText` (timestamptz-literal + no-offset
      cases); `TestE2E_FailoverGoopgToPG` green. Design 0123-0005 §"Sub-slice 4b".
      SUB-SLICE 5 LANDED (2026-07-19): canonical **`BOOLEANTEST`
      (`x IS [NOT] TRUE/FALSE/UNKNOWN`)** SCALAR node — a dedicated `BooleanTest`
      (primnodes.h, 6-value ordinal enum), booltesttype a PLAIN INT
      (WRITE_ENUM_FIELD), stored unfolded in adbin. ir.go/outfuncs.go/readfuncs.go
      + resolver_expr.go (`*parser.IsBoolExpr`→resolveBooleanTest[With],
      `booleanTestType` flag→ordinal) + rebuild.go (exact inverse, out-of-range
      reject). Gate `internal/pgnodes/booleantest_test.go` (6 live PG18.3 adbin
      goldens, one per ordinal). Design 0123-0005 §"Sub-slice 5".
      SUB-SLICE 6 LANDED (2026-07-19): **`BOOLEANTEST` in the VIEW-query path** —
      routed `queryScope.resolveExpr` (`*parser.IsBoolExpr`→resolveBooleanTestWith)
      + `viewRebuildScope.rebuildExpr` (`*BooleanTest`→rebuildBooleanTestWith)
      through the sub-slice-5 injectable `*With` builders, so a view
      `WHERE (x) IS [NOT] TRUE/FALSE/UNKNOWN` emits canonical ev_action
      (was SQL text). Two dispatch arms only; no new IR/codec. Gate
      `internal/pgnodes/view_bool_null_test.go` (2 new live PG18.3 ev_action
      goldens v5 IS TRUE / v6 IS NOT FALSE) + executor Rewrite/View tests.
      Design 0123-0005 §"Sub-slice 6".
      SUB-SLICE 7 LANDED (2026-07-19): canonical **`CASEEXPR`/`CASEWHEN`
      (searched form)** — a column DEFAULT `CASE WHEN cond THEN result …
      [ELSE result] END` now resolves to a canonical CaseExpr (was SQL text).
      ir.go `CaseExpr`/`CaseWhen` + outfuncs/readfuncs CASEEXPR/CASEWHEN dispatch;
      resolver_expr.go `*parser.CaseExpr`→resolveCaseExpr(+`…With` recursion),
      mirroring transformCaseExpr for the searched form (WHEN conds→bool, all
      results+ELSE same non-collatable casetype, casecollid 0, omitted ELSE →
      typed NULL Const via newNullConst); rebuild.go `*CaseExpr`→rebuildCaseExpr
      (NULL defresult ↔ omitted ELSE = fixed point). datum.go caseTypeMeta
      allowlist. Gate `internal/pgnodes/case_test.go` (5 live PG18.3 adbin
      goldens + degradation matrix) + executor `TestCanonicalAttrdefText`
      reconciled (case-expr/case-no-else flipped canonical). Design 0123-0005
      §"Sub-slice 7".
      SUB-SLICE 8 LANDED (2026-07-19): **`CASEEXPR` in the VIEW-query path** —
      routed `queryScope.resolveExpr` (`*parser.CaseExpr`→resolveCaseExprWith)
      + `viewRebuildScope.rebuildExpr` (`*CaseExpr`→rebuildCaseExprWith) through
      the sub-slice-7 injectable `*With` builders, so a view `WHERE CASE WHEN …
      THEN … [ELSE …] END` emits canonical ev_action (was SQL text). Two dispatch
      arms only; no new IR/codec (searched-form / same-casetype / caseTypeMeta
      guards live in resolveCaseExprWith). Gate `internal/pgnodes/view_bool_null_test.go`
      (2 new live PG18.3 ev_action goldens: v7 one-WHEN+ELSE bool, v8
      two-WHENs+omitted-ELSE→typed-NULL defresult; forward + codec round-trip +
      rebuild fixed point + v7/v8 structural asserts) + `TestE2E_FailoverGoopgToPG`
      (new b5c_view3: a real PG18 standby reports relhasrules=true +
      pg_get_viewdef PARSES the CASE ev_action). Design 0123-0005 §"Sub-slice 8".
      SUB-SLICE 9 LANDED (2026-07-19): canonical **`DISTINCTEXPR`
      (`a IS [NOT] DISTINCT FROM b`)** SCALAR node — a `bool DEFAULT
      (a IS [NOT] DISTINCT FROM b)` now resolves to a canonical DISTINCTEXPR
      (was SQL text). PG's make_distinct_op re-tags a make_op `=` OpExpr as
      T_DistinctExpr (same struct), so `type DistinctExpr OpExpr` + shared
      out/read field helpers (outOpExprFields/readOpExprFields) give a
      byte-identical codec; `IS NOT DISTINCT FROM` = a NOT BOOLEXPR wrapping the
      DISTINCTEXPR. resolver_expr.go `*parser.IsDistinctFromExpr`→
      resolveDistinctFrom(+…With); buildDistinctExpr reuses buildOpExpr for `=`;
      rebuild.go `*DistinctExpr`→rebuildDistinctExpr(+…With) (NOT wrapper rebuilds
      via existing BoolExpr NOT arm → fixed point). Gate
      `internal/pgnodes/distinct_test.go` (5 live PG18.3 adbin goldens: int/NOT-
      wrapper/text-collid100/numeric/bool) + executor default/attrdef siblings
      green. Design 0123-0005 §"Sub-slice 9".
      SUB-SLICE 10 LANDED (2026-07-19): DISTINCTEXPR view-query wiring.
      SUB-SLICE 11 LANDED (2026-07-19): `IS [NOT] DISTINCT FROM NULL`→NullTest
      rewrite (make_nulltest_from_distinct).
      SUB-SLICE 12 LANDED (2026-07-19): CASE simple form (`CASE operand WHEN …`)
      via a CaseTestExpr placeholder.
      SUB-SLICE 13 LANDED (2026-07-19): CASE **cross-type result coercion**
      (`select_common_type`) — a mixed-result CASE (searched OR simple) now folds
      via the numeric-family common type: types drawn from {int4,int8,numeric}
      that include numeric → casetype numeric, each integer result wrapped in the
      implicit int4_numeric(1740)/int8_numeric(1781) cast FuncExpr, byte-identical
      to PG18.3 (un-const-folded). New selectCaseCommonType/coerceCaseResult in
      resolver_expr.go; resolve now collects all results first, selects the common
      type, then coerces each. Rebuild reuses the sub-slice-4a int→numeric cast
      unwrap → fixed point. Gate case_test.go (4 live goldens: cast-on-WHEN,
      simple-form, cast-on-ELSE, multi-arm int8+int4); sibling sys_pg_attrdef_test
      case-mixed flipped canonical; degrade test now covers int4+int8-no-numeric +
      text. Design 0123-0005 §"Sub-slice 13".
      SUB-SLICE 14 LANDED (2026-07-19): CASE **cross-FAMILY integer coercion**
      (int4+int8-no-numeric→int8) — the last member of the exact-integer/numeric
      family. selectCaseCommonType now returns the WIDEST family member present
      (numeric>int8>int4; none is a preferred type so PG's walk always widens);
      coerceCaseResult gains the int4→int8 arm via new wrapInt4ToInt8Cast (implicit
      int8(int4) cast FuncExpr, funcid 481 / funcresulttype 20 / funcformat 2, from
      pg_cast.dat castcontext 'i'), byte-identical to PG18.3 un-const-folded.
      rebuild.go isImplicitInt4ToInt8Cast unwraps it (fixed point). Gate case_test.go
      (4 live goldens: cast-on-WHEN, cast-on-ELSE, simple-form, multi-arm two-casts);
      degrade test swapped its now-canonical int4+int8 case for int4+float8 (common
      float8, outside family → SQL text); added OidFloat8=701. Design 0123-0005
      §"Sub-slice 14".
      SUB-SLICE 15 LANDED (2026-07-19): CASE **cross-FAMILY float coercion**
      (float4+float8→float8) — the binary-float family. selectCaseCommonType
      restructured to classify results into two disjoint families and fold only a
      within-family mix (exact-integer/numeric {int4,int8,numeric} OR float
      {float4,float8}→float8; float8 is a preferred type). coerceCaseResult gains
      the float4→float8 arm via new wrapFloat4ToFloat8Cast (implicit float8(float4)
      cast FuncExpr, funcid 311 / funcresulttype 701 / funcformat 2, from
      pg_cast.dat castcontext 'i'), byte-identical to PG18.3 un-const-folded.
      rebuild.go isImplicitFloat4ToFloat8Cast unwraps it (fixed point). Gate
      case_test.go (3 live goldens from table cf: cast-on-WHEN, cast-on-ELSE,
      multi-arm two-casts — float results produced by float4()/float8() conv funcs
      since there is no float literal leaf); added OidFloat4=700 + float
      caseTypeMeta. Design 0123-0005 §"Sub-slice 15".
      SUB-SLICE 16 LANDED (2026-07-19): UNIFIED cross-FAMILY CASE coercion (any
      int/numeric/float → float8). selectCaseCommonType rewritten from two disjoint
      families to ONE walk over PG's numeric type category {int4,int8,numeric,
      float4,float8}; float8 is the category's PREFERRED type so it wins whenever a
      float8 result is present (common type = float8 > numeric > int8 > int4).
      coerceCaseResult gains int4/int8/numeric→float8 arms via new wrapToFloat8Cast
      (float8(int4)=316 / float8(int8)=482 / float8(numeric)=1746, all funcformat 2,
      castcontext 'i'); rebuild isImplicitToFloat8Cast unwraps them (funcformat==2
      guard is load-bearing — same OIDs appear funcformat 0 for explicit float8(int)
      conversion calls). Scope boundary: a float4-but-no-float8 mix has common type
      float4 + an OUTER float8(float4) column cast (unmodeled → degrade). Gate
      case_test.go (4 live goldens tables ucf/ucf5: int4/int8/numeric→float8 +
      three-family int4+float4+float8; degrade case swapped int4_float8_no_numeric→
      float4_common_no_float8). Design 0123-0005 §"Sub-slice 16".
      BYTE-DIFF ORACLE (adbin) LANDED (2026-07-19): new
      internal/testport/oracle_pgnodes_adbin_test.go
      (TestOraclePgnodesAdbinBytesMatchPG). For each of 25 canonical
      (column-type, DEFAULT-expr) cases it CREATE TABLEs the default on a LIVE
      PG18 (pgcluster.New+Start), reads back pg_attrdef.adbin::text, normalizes
      `:location N`→`-1`, and asserts pgnodes.ResolveForColumn→Out is
      byte-identical (SQL-text fallback on a PG-canonical case = failure). Spans
      every S4-canonical family: int4/int8/text/numeric Consts (decimal/sci/neg),
      int4→numeric cast, upper() FuncExpr, timestamptz literal, BoolExpr/NullTest/
      OpExpr (+3-arg AND flatten), BooleanTest, DistinctExpr (int+text), CaseExpr
      (searched+simple, int→numeric / int4→int8 coercion). Cases drawn from
      existing pgnodes goldens so the added value is a LIVE oracle (catches
      hand-capture drift + auto-covers future types), not a fresh assertion.
      Gated: -short + GOOPG_SKIP_PGNODES_ORACLE + pgcluster.Available; ≈1.3s.
      All 25 GREEN vs PG18.3. Design 0123-0005 §"Byte-diff oracle gate (adbin)".
      BYTE-DIFF ORACLE (ev_action) LANDED (2026-07-19): new
      internal/testport/oracle_pgnodes_ev_action_test.go
      (TestOraclePgnodesEvActionBytesMatchPG) — the query-tree analogue. Seeds
      one shared bench_log(client int, src text) on a live PG18, then for each of
      13 canonical view cases CREATE VIEWs the SELECT, reads back
      pg_rewrite.ev_action::text, normalizes :location→-1, and asserts
      pgnodes.ResolveViewQuery→OutRuleAction is byte-identical (ErrUnsupported on
      a PG-canonical case = failure). The piece the adbin path lacks: a LIVE
      RelationResolver (liveRelationResolver) that reads the base relation's real
      relid/relkind (pg_class) + full column list (pg_attribute attname/attnum/
      atttypid/atttypmod/attcollation via string_agg+QueryScalar) from the SAME
      cluster, so goopg's Var/RTE OIDs match PG's ev_action regardless of catalog
      OID drift (no baked 16384). Cases mirror pgnodes view goldens (v/v2 +
      v3–v13): OpExpr, computed FuncExpr target, BoolExpr AND/OR/NOT, NullTest,
      BooleanTest, CaseExpr searched+simple, DistinctExpr (+NULL-operand rewrite).
      All 13 GREEN vs PG18.3 (1.25s); -short SKIP verified; build/vet/gofmt clean.
      Design 0123-0005 §"Byte-diff oracle gate (ev_action)".
      SUB-SLICE 17 LANDED (2026-07-19): simple-form CASE **WHEN-value implicit
      coercion** (numeric operand + int4 value). PG's make_op coerces the value up
      to the operand type when no native cross-type `=` operator exists: a numeric
      operand has no `numeric=int4` op, so PG picks numeric_eq (opno 1752) and wraps
      the int4 value in the implicit int4_numeric (1740, funcformat 2) cast; the
      CaseTestExpr placeholder stays un-coerced. NO resolver change — resolveCase\
      WhenCond already resolves the value with the operand type as its expected
      type (resolveIntLiteral applies the same cast, buildOpExpr picks the exact
      op), so this slice makes the intentional-but-untested path GUARANTEED: 2 live
      PG18.3 scalar adbin goldens (case_test.go `simple_numeric_operand_int_when_\
      coerce{,_multi}`, table sd) through golden/codec/rebuild-fixed-point loops +
      2 live-oracle cases (oracle_pgnodes_adbin_test.go, now 27). Comment on
      resolveCaseWhenCond documents the make_op coercion model + the un-modeled
      native-cross-type-operator boundary. Design 0123-0005 §"Sub-slice 17";
      ledger 2026-07-19 (int8/explicit-cast operand deferral). Gates GREEN: full
      pgnodes pkg, adbin oracle 27/27 vs PG18.3 (1.29s), build/vet/gofmt clean.
      SUB-SLICE 18 LANDED (2026-07-19): simple-form CASE **WHEN-value NATIVE
      cross-type operator** (closes the sub-slice-17 boundary). resolveCaseWhenCond
      now resolves the WHEN value at its NATURAL type (`rec(when, 0)`) and models
      PG make_op's two phases: (1) if a native `=` operator matches (operandType,
      valType) directly — incl. cross-type int8=int4 (opno 416, int84eq) / int4=int8
      (opno 15, int48eq) — use it with the value UN-coerced; (2) else coerce the
      value up to the operand type via coerceCaseResult (sub-slice 17's numeric path
      is unchanged — natural int4 Const + int4_numeric cast == old expected-type
      resolution, byte-identical, no golden churn). Gate case_test.go
      (`simple_int8_operand_int4_when_native` CASE 5000000000 WHEN 1…, opno 416;
      `simple_int4_operand_int8_when_native` CASE 1 WHEN 5000000000…, opno 15) via
      golden/codec/rebuild loops + 2 live-oracle cases (oracle_pgnodes_adbin_test.go,
      now 29, all byte-identical vs PG18.3). Design 0123-0005 §"Sub-slice 18".
      SUB-SLICE 19 LANDED (2026-07-19): explicit integer **`::type` cast**
      (int2/int4/int8). PG stores `expr::inttype` as a COERCE_EXPLICIT_CAST
      (funcformat 1) FuncExpr naming the pg_cast conversion function (int2(int4)=314
      / int8(int4)=481 / int4(int8)=480 / int2(int8)=714), KEPT verbatim in adbin —
      the funcformat-1 sibling of the implicit-cast helpers (funcformat 2, same
      OIDs); a cast to the operand's own type is a no-op (bare Const). New
      resolver_expr.go `resolveCastExpr`/`isIntegerType`/`integerCastFuncid`
      (`*parser.CastExpr` arm; operand resolved at NATURAL type so magnitude typing
      picks the source), operandTypmodCollid gains a `*FuncExpr` arm (typmod -1 /
      collid funccollid) so a simple-form CASE with an EXPLICIT-cast operand
      (`CASE 5::int8 WHEN 1 …`) emits canonical bytes — closing the "explicit-cast
      operand simple CASE" item; rebuild.go `explicitIntegerCastTypeName` rebuilds
      it to a `::type` CastExpr (funcformat==1 guard; fixed point) vs the implicit
      481/funcformat-2 unwrap. Gate `internal/pgnodes/cast_test.go` (7 live PG18.3
      goldens + degradation matrix) + oracle_pgnodes_adbin_test.go now 36 cases,
      all byte-identical vs PG18.3; TestE2E_FailoverGoopgToPG + initdb/executor
      attrdef siblings green. Design 0123-0005 §"Sub-slice 19".
      SUB-SLICE 20 LANDED (2026-07-19): explicit **numeric↔integer `::type` cast**
      (extends sub-slice 19's funcformat-1 machinery across the int/`numeric`
      boundary). `5.5::int4`/`::int8`/`::int2`, `5::numeric`, `9999999999::numeric`,
      `(-2.5)::int4` now emit canonical `pg_attrdef.adbin` (was SQL text). PG stores
      each as a COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr naming the pg_cast conv
      func — numeric_int4=1744/numeric_int8=1779/numeric_int2=1783 (numeric→int),
      int4_numeric=1740/int8_numeric=1781 (int→numeric) — operand resolved at its
      NATURAL type first (decimal→numeric Const, int→int4/int8 Const). resolver_expr.go
      isIntegerType→isNumericFamilyType (accepts numeric target) + integerCastFuncid→
      numericFamilyCastFuncid (6 cross-boundary arms); rebuild.go explicitInteger\
      CastTypeName→explicitCastTypeName (numeric arms → type name; funcformat==1 guard
      still separates the implicit 1740/1781 funcformat-2 unwrap). rebuildConst's
      existing numeric arm handles the negative `(-2.5)` fold (fixed point). Gate
      cast_test.go (6 live PG18.3 goldens + degrade matrix now numeric→float8) +
      oracle_pgnodes_adbin_test.go now 42 cases all byte-identical vs PG18.3 (1.45s);
      TestE2E_FailoverGoopgToPG + initdb/executor attrdef siblings green. Design
      0123-0005 §"Sub-slice 20".
      SUB-SLICE 21 LANDED (2026-07-19): explicit **float-family `::type` casts**
      (float4/float8) — extends sub-slices 19/20's funcformat-1 machinery across the
      binary-float boundary. All six types (int2/int4/int8/numeric/float4/float8) are
      TYPCATEGORY_NUMERIC members with a pg_cast conversion function, so any `expr::T`
      between them is a COERCE_EXPLICIT_CAST (funcformat 1) FuncExpr kept in adbin.
      `5::float4`/`5::float8`/`5.5::float4`/`5.5::float8`/`9999999999::float4`/`::float8`
      + nested `(5.5::float8)::int4` now emit canonical pg_attrdef.adbin (was SQL text).
      resolver_expr.go isNumericFamilyType accepts float4/float8; numericFamilyCastFuncid
      gains the full float matrix (int→float 236/318/652/235/316/482, numeric↔float
      1745/1746/1742/1743, float↔float 311/312, float→int 238/319/653/237/317/483).
      rebuild.go explicitCastTypeName gains float arms (funcformat==1 guard load-bearing
      — 311/316/482/1746 also appear funcformat-2 as the implicit CASE→float8 coercion).
      NO new node/codec. Gate cast_test.go (7 live PG18.3 goldens + degrade matrix now
      text→float8) + oracle_pgnodes_adbin_test.go now 49 cases all byte-identical vs
      PG18.3 (1.52s); TestE2E_FailoverGoopgToPG + attrdef siblings green. Design
      0123-0005 §"Sub-slice 21".
      SUB-SLICE 23 LANDED (2026-07-20): IMPLICIT **numeric column length coercion**
      (`coerce_type_typmod`) — closes sub-slice 22's degrade for the common case. A
      `numeric(p,s)` column DEFAULT whose stored value does NOT already carry that
      typmod (`numeric(10,2) DEFAULT 5.5`/`0`/`5000000000`/`5.5::numeric(8,1)`, incl.
      `numeric(10,0) DEFAULT 5.5`) now wraps the resolved node in the funcformat-**2**
      sibling of `numeric(numeric,int4)`=1703 with the COLUMN's packed typmod Const,
      byte-identical to PG18.3 (a live probe showed the working-set "RelabelType" note
      was imprecise — RelabelType is ONLY the bare-`numeric`-column case). resolver_expr.go
      `ResolveForColumnTypmod` rewritten around coerce_type_typmod (wrap iff
      targetTypmod>=0 and != the node's own typmod via new numericNodeTypmod +
      wrapNumericLengthCoercion); rebuild.go `isImplicitNumericLengthCoercion` (funcid
      1703 funcformat 2, 2 args) joins the implicit-cast unwrap block so the wrap
      rebuilds invisibly like pg_get_expr (fixed point). NO executor change — the writer
      already threads the column typmod (sub-slice 22). Gate
      `internal/pgnodes/numeric_lencoerce_test.go` (6 live PG18.3 goldens + no-wrap/degrade
      guard) + oracle_pgnodes_adbin_test.go now 57 cases all byte-identical vs PG18.3
      (1.58s); executor attrdef siblings green. Design 0123-0005 §"Sub-slice 23"; ledger
      2026-07-20 (bare-numeric-column RelabelType still deferred).
      SUB-SLICE 24 LANDED (2026-07-20): bare-`numeric`-column typmod'd DEFAULT
      **`RelabelType`** (closes sub-slice 23's last numeric degrade). A bare `numeric`
      column (atttypmod -1) whose DEFAULT carries a length typmod (`numeric DEFAULT
      5.5::numeric(8,1)`/`5::numeric(8,1)`/`5000000000::numeric(8,1)`/`(-2.5)::numeric(8,1)`)
      now wraps the resolved node in an IMPLICIT `RelabelType` (relabelformat 2,
      resulttypmod -1, resultcollid 0) that strips the exposed typmod back to the column's
      -1 — `coerce_type_typmod`'s no-op branch (target typmod -1 ⇒ the numeric() length
      coercion would do nothing, so PG emits a RelabelType not a func call), byte-identical
      to PG18.3 (live-probed). resolver_expr.go `ResolveForColumnTypmod` bare-numeric branch
      wraps via new `wrapNumericRelabelToBare` instead of degrading; rebuild.go new
      `case *RelabelType`→`rebuildRelabelType` unwraps the implicit form invisibly like
      pg_get_expr (fixed point; explicit relabelformat≠2 rejected). The `RelabelType` IR
      node + RELABELTYPE codec already existed from S1 — only resolver/rebuild wiring was
      missing. NO executor change. Gate `internal/pgnodes/numeric_relabel_test.go` (4 live
      PG18.3 goldens + codec/rebuild loops + no-wrap guard) + oracle_pgnodes_adbin_test.go
      now 59 cases all byte-identical vs PG18.3 (1.65s); numeric_lencoerce_test/cast_test
      reconciled (bare-numeric typmod'd cast flipped degrade→canonical); executor attrdef
      siblings green. Design 0123-0005 §"Sub-slice 24" + README index. ALL numeric column/
      typmod DEFAULT shapes now canonical.
      SUB-SLICE 25 LANDED (2026-07-20, committed `be88fb66`): EXPLICIT bare-`numeric`
      cast `RelabelType` (relabelformat 1) — the visible-syntax counterpart of 24's
      implicit form; see design 0123-0005 §"Sub-slice 25".
      SUB-SLICE 26 LANDED (2026-07-20): canonical **`date` (OID 1082) `Const` datums**.
      A `date` column DEFAULT literal (`d date DEFAULT '2024-03-15'`) now folds to a
      by-value `DateADT` `Const` (int32 days-since-2000, constlen 4) byte-for-byte
      identical to PG18.3's `pg_attrdef.adbin` — `date_in` is TimeZone-INDEPENDENT so
      (unlike timestamptz) any plain ISO date literal folds deterministically; the only
      guard is calendar validity (`j2date∘date2j` round-trip rejects month 13 / Feb 30).
      datum.go `OidDate`/`NewDateConst`/`parseDateDays`/`formatDate` (reuses the existing
      `date2j`/`j2date`/`parseDateFields` math); resolver_expr.go StringConst gains a date
      arm parallel to timestamptz; rebuild.go rebuildConst gains an OidDate case (fixed
      point). Engine wiring unchanged (`TypeNameToOID("date")`==1082 already routes it).
      Gate `internal/pgnodes/date_test.go` (5 live PG18.3 goldens + math table + graceful
      degradation) + oracle_pgnodes_adbin_test.go now **64 cases** all byte-identical vs
      LIVE PG18.3 + executor `TestCanonicalAttrdefText` date-lit/date-lit-invalid. Design
      0123-0005 §"Sub-slice 26". DEFERRED (ledger): non-ISO / special date input forms
      (`infinity`/`-infinity`, BC years, `DateStyle`-dependent `MM/DD/YYYY`, textual month).
      SUB-SLICE 27 LANDED (2026-07-20): explicit **`::date` / `::timestamptz` cast of a
      string literal**. `d date DEFAULT '2024-03-15'::date` (and the timestamptz form) now
      folds to the SAME by-value Const as the bare-literal column-context form — PG's
      coerce_type folds an unknown-type literal via stringTypeToConst→the type input
      function with NO cast node, so the adbin is byte-identical (closes the asymmetry
      where the bare form was canonical but the explicit-cast form degraded). resolver_expr.go
      `resolveCastExpr` gains a leading date/timestamptz string-fold arm (parseDateDays /
      parseTimestamptzMicros; non-string operand / invalid literal / TZ-dependent form /
      typmod'd target → ErrUnsupported). NO IR/codec/rebuild change (the folded Const is
      identical to the column-context form; rebuildConst's existing OidDate/OidTimestamptz
      arms invert it via the column-scoped fixed point). Gate `internal/pgnodes/datetime_
      cast_test.go` (3 goldens resolved with UNKNOWN context + cast==bare-fold pair +
      degradation matrix + column-scoped reload fixed point) + oracle_pgnodes_adbin_test.go
      now **29 cases** all byte-identical vs LIVE PG18.3 (PG confirms the bare-Const store)
      + executor `TestCanonicalAttrdefText` date-cast/tstz-cast/tstz-cast-notz. Design
      0123-0005 §"Sub-slice 27".
      SUB-SLICE 28 LANDED (2026-07-20): string-literal cast folds to **bool / int2 /
      int4 / int8** (closes sub-slice 27's `'123'::int4`/`'t'::bool` deferral). An
      unknown-type STRING literal coerced to bool/int2/int4/int8 — explicit `::T` cast
      OR typed column context (`col int4 DEFAULT '123'`) — folds at parse time to a
      by-value Const via the type input function (int4in/int8in/int2in/boolin), with NO
      cast node, byte-identical to PG18.3. New shared `foldStringLiteralConst` routes
      BOTH sibling paths (resolve's StringConst arm + resolveCastExpr string block).
      datum.go: NewInt2Const + parseIntFromString (decimal subset of pg_strtoint) +
      parseBoolLiteral (parse_bool_with_len port) + pgTrimSpace. rebuild.go: OidInt2 →
      STRING literal (re-folds via foldStringLiteralConst; a bare IntegerConst would
      resolve to int4 and break the fixed point). KEY BOUNDARY: bare integer `int2
      DEFAULT 5` is an int4→int2 cast FuncExpr (funcid 314), NOT an int2 Const — only
      the unknown-STRING form folds, so foldStringLiteralConst fires only on
      *parser.StringConst and resolveIntLiteral is untouched. Gate
      `internal/pgnodes/string_cast_test.go` (6 goldens + codec + cast==bare-fold pairs
      + bool-spelling table + non-string-operand boundary + degradation matrix +
      column-scoped reload fixed point) + oracle_pgnodes_adbin_test.go now **67 cases**
      all byte-identical vs LIVE PG18.3 + executor `TestCanonicalAttrdefText` 6 str-cast/
      str-col cases; cast_test/resolver_expr_test sibling reconciliations. Design
      0123-0005 §"Sub-slice 28"; ledger 2026-07-20 (text/numeric/float/oid string folds
      + bare-integer→int2 cast deferred).
      SUB-SLICE 29 LANDED (2026-07-20): string-literal cast folds to **text / numeric**
      (closes sub-slice 28's text/numeric deferral). An unknown-type STRING literal
      coerced to text/numeric — explicit `::T` cast (`'foo'::text`, `'5.5'::numeric`) OR
      typed column context (`col numeric DEFAULT '5.5'`) — folds at parse time via
      textin/numeric_in to a by-value Const with NO cast node, byte-identical to PG18.3.
      Two new arms in `foldStringLiteralConst`: OidText (NewTextConst, verbatim, always
      ok) + OidNumeric (NewNumericConst(pgTrimSpace(s)) — reuses proven numeric datum;
      `'5.5'::numeric` == bare `5.5`, `'5.50'` keeps dscale 2). No rebuild change (text→
      StringConst, numeric→NumericConst already re-fold to the fixed point). Gate
      `internal/pgnodes/string_text_numeric_cast_test.go` (4 goldens + codec + MatchesBare
      pairs incl. `'5.5'::numeric==5.5` + RebuildRoundTrip + NaN/Infinity/bad-degrade
      matrix) + oracle_pgnodes_adbin_test.go now **72 cases** all byte-identical vs LIVE
      PG18.3 + executor `TestCanonicalAttrdefText` 4 cases. Design 0123-0005 §"Sub-slice
      29"; ledger 2026-07-20 (NaN/Infinity numeric specials + typmod'd `'5.5'::numeric(10,2)`
      + oid/float string folds deferred).
      SUB-SLICE 29b LANDED (2026-07-20): numeric specials `'NaN'`/`'Infinity'`/`'-Infinity'`
      `::numeric` (and numeric column DEFAULT) now fold to a canonical digitless
      NUMERIC_SPECIAL 6-byte varlena (n_header 0xC000/0xD000/0xF000) instead of degrading —
      byte-identical to LIVE PG18.3. `datum.go`: `numericVar.special` field + `parseNumericSpecial`
      (exact numeric_in pre-set_var_from_str recognition: unsigned NaN, ±Inf via sign, case-
      insensitive prefix-only, trailing-whitespace-only) + `varlena()`/`decodeNumericVar`/
      `specialText()`; `rebuild.go` OidNumeric arm → StringConst spelling (fixed point).
      Gate `string_text_numeric_cast_test.go` (+3 goldens, SpecialsFold 10-spelling matrix,
      BadDegrade reject matrix) + oracle now **75 cases** byte-identical vs LIVE PG18.3 +
      executor `str-numeric-nan` flipped canonical. Design 0123-0005 §"Sub-slice 29b";
      ledger 2026-07-20 (typmod'd `'5.5'::numeric(10,2)` + oid/float string folds still open).
      SUB-SLICE 29c LANDED (2026-07-20): string-literal cast folds to **oid / float4 /
      float8** (closes sub-slice 28/29's oid/float deferral). An unknown-type STRING literal
      coerced to oid/float4/float8 — explicit `::T` cast (`'5'::float8`, `'5'::oid`) OR typed
      column context (`col float8 DEFAULT '5.5'`) — folds at parse time via oidin/float4in/
      float8in to a by-value Const with NO cast node, byte-identical to PG18.3. Three new arms
      in `foldStringLiteralConst`. datum.go NewOidConst (32-bit unsigned → ZERO-extends the
      datum word) / NewFloat8Const (raw IEEE double bits) / NewFloat4Const (32-bit float bits
      SIGN-extend, so `(-2.5)::float4` fills the high word with 0xFF, like a negative int4) +
      parseOidFromString (unsigned-decimal subset) + parseFloat8/4FromString (finite-decimal
      subset sharing isDecimalFloatText; both PG strtod/strtof and Go ParseFloat are correctly
      rounded → identical bits). rebuild.go: each folds back to a StringConst (decimal for oid,
      FormatFloat 'g'/-1 shortest round-trip for floats) → re-folds to the fixed point. Gate
      `internal/pgnodes/string_float_oid_cast_test.go` (8 live PG18.3 goldens + codec +
      cast==col-context pairs + rebuild fixed point + BadDegrade matrix incl. non-finite
      Inf/NaN) + oracle_pgnodes_adbin_test.go now **85 cases** all byte-identical vs LIVE
      PG18.3; resolver_expr_test/cast_test siblings reconciled. Design 0123-0005 §"Sub-slice
      29c".
      REMAINING: typmod'd string numeric cast (`'5.5'::numeric(10,2)`);
      the bare-integer→int2 implicit cast
      FuncExpr (`int2 DEFAULT 5`); float4-common (no float8) CASE mix (needs
      int/numeric→float4 arms + outer column cast); operator-driven view-qual coercion
      (unblocks int2/timestamptz literals inside a view WHERE); other length types
      (`varchar(N)`=CoerceViaIO, `timestamp(N)`, `bit(N)`); broader date input forms
      (`infinity`, BC years, DateStyle-dependent).
