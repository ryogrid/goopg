# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**. As of 2026-07-28
the banner puts **M0124 → M0125** (closing the TPC-DS round-2 plan, per
`docs/design/tpcds-round2-fixes/README.md` §13.5) at the top of the roadmap,
ahead of M0123 and every other milestone. **Amended 2026-07-31 (USER): M0126 —
cost-driven planning made production-viable — is inserted directly after M0125,
so the head of the roadmap is M0124 → M0125 → M0126.** **Amended 2026-08-03:
M0126 is CLOSED as a documented no-go (milestone-terminal); M0127 — PG-shaped
join search — is filed as its successor and inserted directly after M0125, so
the head of the roadmap is M0124 → M0125 → M0127.** **M-NIGHTLY no longer
preempts it (amended 2026-07-28): nightly items are still FILED every loop, but
they are not SELECTED until M0124, M0125 and (since 2026-08-03) M0127 close.** This banner is the sole ordering
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
> **⚡ AMENDED 2026-08-03 (filed by the panel / USER directive 2026-08-02,
> amended 2026-08-03) — M0127 IS INSERTED BETWEEN M0125 AND THE M-NIGHTLY
> BACKLOG; M0126 IS CLOSED.** The order below is amended to read: WIP recovery
> (#1) → **M0124** (#2, closed) → **M0125** (#3) → **M0127** (#4, NEW) →
> M-NIGHTLY backlog (#5) → M0123 (#6). **M0126 — cost-driven planning made
> production-viable — is TERMINAL as of 2026-08-03** (documented no-go,
> `evidence/acceptance-run-2.txt`; all 13 tasks `[x]`). **M0127 — PG-shaped
> join search** turns the `docs/design/leftdeep-joins/` design bundle (the
> M0126-0013 successor) into shipped behaviour: the executor stages S0–S4
> (seam de-materialisation, multi-column keys retiring
> `reselectDegenerateHashKeys`, hybrid-hash spill, streaming merge/outer-fill/
> Materialize) land first on the current planner's output, then the PG-shaped
> three-phase DP (`GOOPG_PGSHAPED_DP`, replacing `GOOPG_COST_DRIVEN_JOINORDER`)
> at S5, compiled key/residual eval at S6, and the S7 deletion of
> `MultiHashJoin` + fusion + the old subset-bitmask DP. Milestone doc
> `docs/milestones/0127-pg-shaped-join-search.md`; implementation plan
> `docs/design/0127-pg-shaped-join-search.md`; the task list is the
> `## M0127` section below the M0126 section. It is **not** selected while any
> open M0125 item that M0127 needs first is open (exprwalk commits 5–8 =
> M0125-0002, M0125-0047, M0125-0013; M0125-0040 ROLLUP is an independent
> track outside the M0127 bundle, not a prerequisite). The M0125 items marked
> numbered list below is unchanged and kept as filed; read it with this
> amendment applied.
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
>       **↳ SUPERSEDED 2026-08-03 (loop #40). `M0125-0038` IS `[x]`, and
>       M0126 IS CLOSED (terminal no-go) — read the 2026-08-03 amendment at
>       the head of this banner, not this line. `M0125-0002` (loop #39) and
>       `M0125-0047` (loop #40) are both discharged, so of the three M0125
>       items M0127 waits on, exactly one remains: `M0125-0013`'s
>       BOOKKEEPING half (Q47's 8.4x runtime verdict — a documentation
>       contradiction between `RESULTS.md` chunk 49-56 / the RC-1b ledger row
>       and `analysis/tpcds-sf1-goopg-20260728.md` §3.2/§6, not engine work).
>       **↳ SUPERSEDED 2026-08-03 (loop #41): `M0125-0013`'s bookkeeping half
>       IS `[x]`.** All three M0125 items M0127 waited on are discharged, so
>       **M0127 IS NOW OPEN and is the NEXT SELECTION.** Two facts it inherits:
>       Q47 is measured at **537.55 s vs PG 3.38 s (159×)** with a
>       byte-identical answer, and ~485 s of that is one self-join whose hash
>       key degenerates to `i_category` (10 distinct over 63,745 rows, vs 5,667
>       for the 4-key composite PG merge-joins on) — i.e. the single-key hash
>       deferral `M0125-0011`/`M0125-0035` is worth far more than those rows
>       implied, and Q47 is its sharpest witness.
>       ~~**NEXT SELECTION: `M0125-0013` (bookkeeping half) — it NEEDS A QUIET
>       HOST** (`pgrep -af run-nightly.sh` first; every timing taken on
>       2026-07-30 was under the nightly batch at load ~10), **then M0127
>       opens.**~~ `M0125-0040` (ROLLUP) is an independent track OUTSIDE the
>       M0127 bundle and is explicitly not a prerequisite; the other open
>       M0125 items (`-0003` stage 3, `-0031`, `-0032`, `-0033`, `-0037`,
>       `-0041`) are likewise not on M0127's waiting list — `-0032` (Q21) was
>       absorbed INTO M0127-P3.2. One instrument fact `M0125-0047` leaves for
>       M0127: the plan-snapshot nondeterminism floor that made every
>       single-sweep A/B suspect is now MEASURED AT ZERO for the join-order
>       passes (3 restarts x 96 SF0.5 queries byte-identical), and the fix
>       converges on the plans the baselines already hold, so no baseline
>       needs re-pinning.**
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
> **AMENDED 2026-08-03:** M0126 is closed (documented no-go); every other
> roadmap milestone — M0123 included — is parked below **M0127**, and M0127 is
> itself parked below M0125. Order: M0124 → M0125 → **M0127** → M-NIGHTLY →
> M0123. M0123 keeps its own branch (`wal-pg-nodetree`) and resumes there once
> this line is closed.
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

Work order: **M0124 → M0125 → M0126 → M0127** (this directive as amended
2026-07-31; M0126 closed 2026-08-03, M0127 filed as its successor per the
2026-08-03 amendment — M0127 is selected after the M0125 items it needs are
closed, and M0126 contributes nothing further),
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
- [ ] **testport/TestPort_IsolationEvalPlanQual — REOPENED** — the root-0030
      fix (checked item above) did not hold: FAILed in nightly runs 20260801,
      `20260802-014405`, and `20260803-013955`
      (AI-20260802-014405-001, AI-20260803-013955-003,
      AI-20260804-005028-001, AI-20260805-014309-001 — five nights running;
      repro:
      `go test -v -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/`;
      evidence `ci/logs/20260802-014405/testport/go-test.log`).
      **VERIFIED FAILING AT HEAD `e13d6c6f` in isolation (2026-08-02 loop #31,
      21 s)** — not a co-load flake. The diff is DIFFERENT from root-0030's:
      around L1027 the expected `step partiallock_ext: <... completed>` marker
      is missing and output is one line short (1467 vs 1468) — goopg did NOT
      BLOCK on `partiallock_ext` where PG blocks (the step completed
      immediately instead of waiting). First failure night (20260801) is the
      nightly at/after the M0126-0003 slot-path re-land (`d197365c`) — bisect
      candidates: `5c1c0e21` (0a VirtualSlot fast path), `d197365c` (0b re-land).
      FILED, NOT SELECTED per the 2026-07-28(b) amendment.
- [ ] **regress/{btree_index,char,delete,int2,int4,int8,limit,numerology,
      portals_p2,prepare,select,select_into,text,union,varchar} — 15 baseline-pass
      cases diverged in ONE night, all "output mismatch; normalization rules
      need extension"** (AI-20260802-014405-002..-016, all first-seen 20260802;
      recurred 20260803 as AI-20260803-013955-004..-018 and again 20260804 as
      AI-20260804-005028-002..-016 — the SAME 15 subjects three nights running,
      each time alongside the suite-wedge below;
      evidence `ci/logs/20260802-014405/testport/go-test.log`). **Six sampled
      cases (btree_index, char, int2, select, text, union) PASS in ISOLATION at
      HEAD `e13d6c6f` (2026-08-02 loop #31, 3.2 s total)** — so this is almost
      certainly the known phantom-divergence-after-cluster-restart pattern
      (cf. the closed "19 phantom regressions from ONE wedged cluster" item),
      downstream of the suite-wedge item below, NOT 15 real regressions.
      Subjects limit/numerology/portals_p2/select overlap the older parked
      suite-ordering task above — do not double-work them. Triage the wedge
      first; only if a case still diverges in a wedge-free full-suite run does
      it become real. FILED, NOT SELECTED per the 2026-07-28(b) amendment.
- [ ] **regress/suite-wedge — aggregates/jsonb/misc hit the 120 s per-case
      timeout (0 baseline-pass), longest unbroken run 1 case from `aggregates`**
      (AI-20260802-014405-017, first-seen 20260802; recurred 20260803 as
      AI-20260803-013955-019 and 20260804 as AI-20260804-005028-017, same 3
      cases aggregates/jsonb/misc every time; repro:
      `go test -v -run 'TestPort_RegressSuite/aggregates' ./internal/testport/`;
      evidence `ci/logs/20260802-014405/testport/go-test.log`). Output truncated
      ⇒ NOT an output divergence — investigate what wedged the cluster at
      `aggregates` (orphaned backend holding locks, or GC-thrashing server).
      Likely the ROOT CAUSE of the 15 phantom divergences above (same night).
      Note the nightly ran while the interactive M0126 acceptance measurement
      held the host (armB Q9 600 s timeout ~16:27 JST — but the nightly ran at
      01:44 JST, so co-load with the 04:14 ralph attempt is NOT the story;
      check the run's launch window against host state first). FILED, NOT
      SELECTED per the 2026-07-28(b) amendment.
- [x] **units/internal/executor + race/internal/executor — REOPENED, new causes**
      — both lanes failed in nightly `20260803-013955` at sha `1a589c23`
      (AI-20260803-013955-001/-002, both first-seen tonight; evidence
      `ci/logs/20260803-013955/{units,race}/go-test.log`). Triaged 2026-08-03
      loop #35 — TWO distinct causes, do not conflate:
      (a) **units: stale — already fixed at HEAD.** The only failure is
      `TestMHJParallelNoDuplicates`, the known e85e5347 miss; the nightly sha
      `1a589c23` predates the fix `4fb87456` (loop #34). Next nightly should
      clear the units lane; no work.
      (b) **race: GENUINE data race, still present at HEAD.**
      `buildEnvInFlight` (`internal/executor/executor.go:35-41`, introduced by
      M0126-0006) is a package-level global written by EVERY
      `buildWithEnv` call, and `BuildWorker` is called concurrently from
      Gather workers (`operators_gather.go:210` → `executor.go:29`), so any
      parallel-query worker build races (12 FAILs: TestPartialFinalizeIdentity,
      TestPartialAggregateRefusals, TestPartialAggregateAccumulatorRetracted —
      all "race detected during execution of test"). Beyond the detector, it is
      a correctness hazard: concurrent workers can observe each other's env
      (root/fusionCfg) in `tryFuseHashCascade`, and the deferred prevEnv
      restore is order-dependent across goroutines. The single-threaded
      save/restore was fine when M0126-0006 landed behind the cost-driven flag;
      the parallel-worker path hits it unconditionally. Fix direction: thread
      the env as an explicit parameter / builder receiver through the build
      recursion instead of a package global (NOT a quick patch — every
      recursive Build site in the operator constructors participates). Until
      fixed the nightly race lane stays red every night. FILED, NOT SELECTED
      per the 2026-07-28(b) amendment — the race lane is not on the banner's
      carve-out gate list and units/tpch-spotcheck/SF0.5 all pass at HEAD.
      **↳ (b) FIXED 2026-08-03 by M0127-P1.2** (whose own bar is RACE, and
      which could not be met while this was red — the item was reached as
      P1.2's blocker, not selected out of order). **The "NOT a quick patch"
      prediction was wrong, and why is worth keeping:** the global never
      needed to survive the recursion. `buildWithEnv` sets it at the top of
      the switch and the ONLY read in that function is the `*planner.Join`
      arm's `tryFuseHashCascade`, which runs before any recursive `Build`
      could overwrite it — so the value read was always the one this very
      invocation stored, i.e. already a local in disguise. Making it an actual
      local (`executor.go`) is behaviour-identical and removes the sharing.
      The second read, `buildRec`'s Join arm, is now the explicit nil field
      `opTreeSlab.env`: `BuildFast` is a top-level entry (dispatch's
      simple-query path) with no `buildWithEnv` frame above it, so the global
      it used to read there was ALWAYS nil — fusion has never been reachable
      from the slab path, only from the extended-protocol `executor.Build`
      one. `make race-gate` now passes end-to-end (EXIT=0, all packages),
      first time since M0126-0006 landed. Nightly's race lane should clear on
      the next run; item (a) remains stale-only.
      **CHECKED OFF 2026-08-03 (M0127-P3.2 loop).** Both causes are discharged
      and re-confirmed at HEAD by that task's own bar: UNITS green (item (a)'s
      `TestMHJParallelNoDuplicates` fixed by `4fb87456`) and `make race-gate`
      green across every package (item (b)). Left for the next nightly run to
      confirm from its own log.

### Nightly run 20260805-014309 (2 items, sha `ce027cee` — status fail)

**The run shrank 17 → 2.** The 15 `regress/*` "output mismatch" subjects and the
`regress/suite-wedge` item filed for 20260802/03/04 are ABSENT tonight, which is
the first independent evidence for the phantom-divergence-downstream-of-the-wedge
reading recorded above (they were never 15 regressions). Their tasks stay open —
one clean night is not a fix — but do not re-file them per night.

- [x] **pgbench/nightly — recurrence of the documented non-FIFO tuple-lock tail**
      (AI-20260805-014309-002, first-seen tonight; evidence
      `ci/logs/20260805-014309/pgbench/pgbench.log`). **1 failed transaction**,
      same signature as the closed 20260720 item above: `client 61 script 0
      aborted in command 4 query 0: ERROR: current transaction is aborted,
      commands ignored`. Command 4 is TPC-B's `UPDATE pgbench_branches`; 100
      clients contend 50 branch rows and goopg raises instead of queuing —
      `goopg_dml_conflict_no_fifo_tuple_lock` / deferral row 0021-0012 (route
      tuple locks through `tableLockMgr` for FIFO waits). Checked off as a
      recurrence, not new work: 1 aborted txn is a *smaller* tail than the
      4/19.5M already triaged, the error string and command index are identical,
      and the fix is the existing ledger row, not a new task. Re-open only if a
      night shows a different command index or a non-abort error class.
- [ ] **testport/TestPort_IsolationEvalPlanQual (AI-20260805-014309-001)** —
      fifth consecutive night; already tracked by the REOPENED item above, which
      now carries this run's id. No separate task.

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
      (`estimateBaseRelInfo.baseRows`, `cardinality.go:139`) and the W arms.
      **Re-scope note 2026-08-03 (M0127):** do NOT land stage 3 into the old
      DP before checking it against M0127's P5.1/P5.6 — the new search
      computes base rows once per `RelOptInfo` (`leftdeep-joins` 04 §2) and
      P5.1's `buildInitialRels` redefines where the fallback feeds; the
      fallback's documented role is an S-cold safety net (inert warm by its
      `RowCount > 0` early return), and stage 3's consumer may be superseded
      by the rows-once design rather than extended. Re-evaluate at M0127-P5.1.
      The
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
- [x] **M0125-0002 — convert the seven remaining walkers, one per commit** (§13.5
      #4, phase 2.2). **CLOSED 2026-08-03 — all eight walkers converted in seven
      commits; see COMMIT 7 below.**
      **↳ M0127 PREREQUISITE (2026-08-03): commits 5–8 are the walker
      stabilisation M0127's qual plumbing builds on.** The M0127 banner lists
      this item among the four M0125 items M0127 waits on, and M0127-P5.2
      (restrictInfo + `hasRelevantJoinClause`) is specified as the first live
      consumer of the exprwalk primitives. Commits 1–4 are landed; keep the
      commit 5 (`exprSide`) → 6 (`conjunctIsLocalEligible` +
      `localizeExprToLeaf`) → 7 (`visitColumnRefsByName`) order, with the
      full timed run + SF0.5 sweep on any hunk.
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
      **COMMIT 3 of 8 DONE 2026-08-03 — `visitColumnRefs` re-based onto
      `walkExprRefs` (`scopeIgnore`; unknown panics), and D2 row 3's "changes
      which refs get re-resolved" is REFUTED by a STRONGER instrument than a
      plan diff**: TPC-H A/B 22/22 byte-identical (== `post-mhj-retire`),
      SF0.5 EXPLAIN A/B 96/96 byte-identical, AND a divergence probe (both
      walker bodies run side-by-side in a measurement-only binary) logged
      **zero visited-set deltas across all 118 planned queries** — needed
      because EXPLAIN prints Name over Index (M0125-0042) and Index mutation
      is this commit's only behavioural surface, so a byte-identical plan
      diff alone cannot excuse the answer sweep here. **Census pin DELETED
      (the first deletion in the series — no dispatch switch survives)**;
      11 newly-visited kinds pinned in `visit_refs_arms_test.go`, each
      proved to fail against the old walker first. Timed TPC-H run skipped
      again (ledger row); SF0.5 answer sweep not owed (zero hunks + probe;
      the old body was read-only so commit 2's metadata-loss concern does
      not arise). **Label note: `m0125-0005-relsize-default-stage2` is now
      itself stale — `e85e5347` (M0126-0011, MHJ retire) moved 19/22 TPC-H
      plans; commits 4–8 must diff against `post-mhj-retire` or,
      preferably, a same-cluster A/B.** Evidence
      `analysis/m0125-0002-c3-plans-20260803/`. En route, the units gate
      was found RED at HEAD (`TestMHJParallelNoDuplicates`, missed by
      `e85e5347`'s test opt-ins) and repaired in `4fb87456`.
      **COMMIT 4 of 8 DONE 2026-08-03 — `visitColumnRefsForTable` re-based
      onto `walkExprRefs` (`scopeIgnore`; unknown panics), and D2 row 4's
      "first-order shape mover" prediction is REFUTED by measurement**:
      TPC-H A/B 22/22 byte-identical (== `post-mhj-retire` lineage), SF0.5
      EXPLAIN A/B 96/96 byte-identical, AND a `tableForCol` divergence
      probe (both walker bodies computing the table attribution
      side-by-side in a measurement-only worktree binary) logged **zero
      `C4DELTA` disagreements across all 118 planned queries** — so every
      partitioning/join-edge decision the sole consumer makes is
      unchanged on both benchmarks. The headline semantic change —
      `col IN (subquery)` now attributes to col's table instead of -1
      (the old arm returned before visiting ANYTHING when Plan != nil) —
      is pinned as a deliberate behaviour pin in
      `visit_refs_for_table_arms_test.go` (`TestTableForCol_InSubquery…`)
      but evidently never fires on either benchmark today. Census pin
      DELETED (second deletion; RC-1a 48 → 47); 11 newly-visited kinds +
      preserved arms + scope declines + panic pinned, each proved to fail
      against the old walker first (15 failing subtests recorded). The
      dead empty-callback call in `extraInScans` (`bushy.go:1703`) was
      removed — pure traversal with a no-op callback, walker #7's site is
      untouched. Timed TPC-H run + SF0.5 answer sweep skipped (ledger
      row; zero hunks + zero-delta probe — the walker is read-only, no
      commit-2-style metadata loss is possible). **En-route discovery,
      filed as `M0125-0047` below: SF0.5 Q85's plan alias order
      (`cd1`/`cd2`) is restart-nondeterministic with an IDENTICAL binary**
      — surfaced as a probe-arm-only EXPLAIN diff, confirmed by 3
      restarts of the same after-binary (2 runs cd2-first, 1 run
      cd1-first). Evidence `analysis/m0125-0002-c4-plans-20260803/`.
      **COMMIT 5 of 8 DONE 2026-08-03 — `exprSide` re-based onto
      `walkExprRefs` (`scopeVeto`; unknown stays `sideMixed`, NOT a panic —
      this walker has always failed CLOSED, so a decline costs an
      optimisation, never a wrong answer), and D2 row 5's "expect hunks"
      prediction is REFUTED by measurement.** The instrument had to be
      extended first: `exprSide`'s ONLY caller is `splitEqualityForHash`,
      and **goopg's EXPLAIN never prints hash keys** (`grep -c 'Hash Cond'`
      = 0 over all 22 TPC-H + 96 SF0.5 plans), so a change in WHICH conjunct
      becomes `LeftKey`/`RightKey` is invisible to a plan A/B unless it also
      flips the printed algorithm — commit 3's hole in a new place. A
      divergence probe on the CONSUMER (both `exprSide` bodies computing
      `splitEqualityForHash`'s `(leftKey, rightKey, ok)` triple, live path
      keeping the OLD answer) logged **0 `C5DELTA` / 0 `C5SIDE` over 232
      calls** (223 TPC-DS + 9 TPC-H) — the COMPLETE live decision population,
      not a sample, with a `C5CALL` positive control so the zero cannot be
      vacuous. TPC-H A/B 22/22 byte-identical (before arm re-derived
      `m0125-0002-c4-after.txt` byte-for-byte); SF0.5 EXPLAIN A/B 96/96.
      **`M0125-0047` fired on its first use and the protocol held:** the lone
      differing SF0.5 cell was q85's `cd1`/`cd2` alias tie-swap; the before
      binary restarted 3× and the after binary restarted 4× produced
      byte-identical plans (md5 `b1bc99cf`), so the captured before-arm was
      the outlier and the hunk is instrument noise. Census pin DEMOTED, not
      deleted (RC-1a 47 → 46) — a two-arm dispatch survives in the `Visit`
      closure. `expr_side_arms_test.go` pins newly-classified containers (one
      case per kind), row-independent leaves (`ExecParamRef`/`TableOidExpr`/
      `Merge*` join the ParamRef class), every preserved arm, both classes of
      preserved decline (`scopeVeto` on inner plans — a one-sided `Args` must
      NOT rescue a `SubqueryExpr` — plus the explicit `*OuterColumnRef` /
      `*CTIDExpr` vetoes, which a completeness-driven conversion would have
      ADMITTED since `exprChildSlots` correctly calls both childless leaves),
      the fail-closed unknown, and the headline semantic pin: `(l IS NULL) = r`
      now yields a hash key pair instead of being stranded on the NL path.
      Timed TPC-H run + SF0.5 answer sweep skipped (ledger row; zero hunks +
      zero-delta probe over the complete consumer population, and the walker
      is read-only returning an enum). Evidence
      `analysis/m0125-0002-c5-plans-20260803/`.
      **COMMIT 6 of 8 DONE 2026-08-03 — the PAIR: `conjunctIsLocalEligible`
      re-based onto `walkExprRefs` and `localizeExprToLeaf` onto
      `cloneExprRefs` (both `scopeVeto`), and D2 row 6's shape-move
      prediction is REFUTED by measurement.** One commit because
      `partitionConjunctsForJoinPlanning` MOVES an eligible conjunct out of
      `joinConjuncts`, so the producer's admission is a promise the consumer
      can rebase it — split, the window is a dropped or mis-indexed
      predicate. Unknown handling is deliberately ASYMMETRIC: the producer
      declines (a decline costs an optimisation, never a wrong answer —
      commit 5's treatment, not commit 3/4's panic), the consumer PANICS (it
      cannot decline; the conjunct has already left `joinConjuncts`).
      **The latent defect closed:** `WHERE t.a IS NULL` on a binding with
      `offset > 0` under `shouldAttachBeforeMHJ` was judged eligible by a
      walk that produced ZERO callbacks (the 9-arm switch never descended
      `*IsNullExpr`), moved into `locals`, then returned UN-REBASED by a
      consumer whose trailing pass-through ("Constants … no ColumnRef")
      covered 7 of 32 kinds — a leaf `Filter` carrying FROM-cumulative
      indices, i.e. **the wrong column**. Commit 4 widened its reachability
      (a complete `tableForCol` attributes `t.a IS NULL` to a binding where
      the old one answered −1) rather than creating it. Measurement: TPC-H
      A/B **22/22 byte-identical** (before arm re-derived
      `m0125-0002-c5-after.txt` byte-for-byte — the instrument is stable
      across loops), SF0.5 EXPLAIN A/B **96/96**, and a divergence probe on
      BOTH functions at ALL THREE live call sites logged **0 `C6ELIG` / 0
      `C6LOC` / 0 `C6ABORT` over 277 eligibility + 175 localization calls**
      across 118 planned queries, with `C6CALL`/`C6LOCC` positive controls.
      The probe was mandatory: eligibility IS visible in the plan text (a
      leaf `Filter` appears/disappears) but the `Index` rebase is NOT
      (M0125-0042 — EXPLAIN prints names), so the probe compares localized
      trees by `exprIdentityKey`, which includes `Index`. Commit 2's
      metadata-loss class cannot arise — `shallowCloneExpr` is a whole-struct
      copy. **Census pins moved BOTH ways in one commit, a series first:**
      `conjunctIsLocalEligible` DEMOTED (its veto dispatch survives in the
      `Visit` closure), `localizeExprToLeaf` DELETED (no switch survives);
      RC-1a 46 → 45. 48 new pin subtests in
      `local_filter_arms_test.go`, each proved to FAIL against the old
      bodies first, including `TestLeafLocalPairAgreesOnEveryExprKind` —
      the pair invariant over all 32 kinds, which is what makes the
      consumer's panic unreachable by construction rather than by argument.
      **Ledger rows: (a)** the timed TPC-H run + SF0.5 answer sweep were
      skipped a fourth time, and the per-commit obligation is CONVERTED into
      one cumulative timed run owed at **commit 8** (covering commits 2–8 as
      a block; it reverts to per-commit if commit 7 moves a plan) — commit 5
      declared them mandatory here on the premise that the fail-open would
      remove predicates, and the measurement discharged the premise;
      **(b)** goopg's now-total decline of subquery-bearing conjuncts is
      broader than PG, which places a SubPlan-bearing qual at a baserel by
      relid set (`initsplan.c: distribute_qual_to_rels`) and refuses
      subplans only for pushdown INTO a subquery RTE (`allpaths.c:3934`,
      `qual_is_pushdown_safe`). Evidence
      `analysis/m0125-0002-c6-plans-20260803/` (incl. `probe-source.md`).
      **COMMIT 7 of 8 DONE 2026-08-03 — `visitColumnRefsByName` re-based onto
      `walkExprRefs` (`scopeSignal`), and D2 row 7's "largest and least
      predictable effect" is REFUTED: no plan moved on either benchmark.**
      This is the commit that CHANGED A SIGNATURE rather than an arm set. Its
      three consumers — `extraInScans`, `allColumnRefNamesInScope`,
      `pushOuterQualsIntoLaterals` — never read the callback stream; each seeds
      a verdict `true` and falsifies it only from inside the callback, so a
      conjunct built entirely from unenumerated kinds produced ZERO callbacks
      and returned a vacuous `true`. For `extraInScans` that is an ADMISSION,
      not a missed optimisation: the conjunct is captured into
      `MultiHashJoin.Filters` and evaluated on the MHJ output row. The walker
      now returns whether the name test COVERED the expression, and D3's
      inversion is `return total && allMatched`. **"Opaque" is wider than D3's
      inner plans, and that widening is this commit's design decision:** a name
      test cannot certify anything that reads row data WITHOUT NAMING the
      column it reads, so `*OuterColumnRef` (names a different scope),
      `*CTIDExpr` (`seqScanOp` injects the scanned row's block/offset),
      `*MergeWholeRowRef` (composite from ctx) and an empty-`Name` `*ColumnRef`
      (`Name` is "for diagnostics" and IS empty on some construction paths —
      the old body skipped those silently) clear `total` alongside the scope
      crossing and the unknown type. `*ParamRef`/`*ExecParamRef`/
      `*TableOidExpr`/`*MergeActionExpr` stay total: they read no row column.
      **`pushOuterQualsIntoLaterals` now takes TWO escapes that must not be
      merged** — its existing `!allIn && len(leftNames) > 0` means "cannot
      enumerate the NODE's columns" and falls back on the index verdict;
      `!total` means "cannot enumerate the CONJUNCT", where that fallback is
      worthless (`classifyConjunctSide` rides `walkColumnRefsImpl`, which has no
      `default:` either, so an unenumerated kind is invisible to BOTH tests and
      a conjunct wrapping an `*ArraySubqueryExpr` reads as conclusively
      `sideLeft` on its other operand alone).
      **Measurement, and it took four sweeps instead of two.** TPC-H A/B
      **22/22 byte-identical** AND byte-identical against `post-mhj-retire` —
      so the CUMULATIVE TPC-H diff across commits 1–7 is empty, not just this
      one's. SF0.5 EXPLAIN A/B first read 95/96 with TPC-DS **Q85** swapping its
      two `customer_demographics` aliases between join positions of identical
      cost; **that hunk is the INSTRUMENT, not the change**: `before` vs
      `before2` = 96/96, `after2` vs `before` = 96/96, `after2` vs `after` =
      95/96 differing only at Q85 (the same binary produced both orderings),
      three fresh single-query starts per binary gave the same ordering all six
      times, and the divergence probe logged **0 verdict deltas** at all three
      call sites while planning Q85. This is `M0125-0047` measured properly, and
      it carries a consequence worth more than the commit: **the plan-snapshot
      instrument accepted commits 2–6 on a SINGLE sweep per arm and has an
      unquantified nondeterminism floor** — ledger row, with "sweep 3× on one
      binary and diff pairwise" as the resume point.
      **Census pin DEMOTED** (the three-arm veto survives in the `Visit`
      closure); RC-1a 45 → 44. 18 pins in `visit_refs_byname_arms_test.go`,
      each proved to FAIL against the old body first — by reproducing that body
      under `_c7old` names, because the signature change means the pins cannot
      be COMPILED against the pre-conversion source the way commits 3–6 were.
      **Ledger rows: (a)** the cumulative timed TPC-H execution power run that
      commit 6 scheduled for exactly this commit was NOT run; a planning-time
      A/B was run instead (22 queries × 5 `EXPLAIN` sweeps in one session with
      `\timing`: 4.41 → 4.54 ms, ~6 µs/query, within-arm spread wider than the
      between-arm delta) because the plan set is byte-identical and what the
      conversions actually changed is planning cost, which an execution run
      would bury under 20-minute scans — execution arm owed at milestone close;
      **(b)** the Q85 instrument finding above; **(c)** the whole name-matching
      scope test is a goopg-only construct — PG carries `Relids` bitmapsets and
      answers containment with `bms_is_subset` (`initsplan.c:
      distribute_qual_to_rels`), which is immune to BOTH fail-opens goopg's
      version has, including a same-column-name-in-two-tables one that no pin
      here closes and that TPC-H's `p_`/`ps_` prefixes hide. Evidence
      `analysis/m0125-0002-c7-plans-20260803/`.
      **THE SERIES IS COMPLETE**: all eight walkers named in §2 are on the
      exprwalk primitives, RC-1a's pinned population is 50 → 44, and M0127-P5.2
      has the stable base it waits on.
      ~~**Next in THIS task is commit 7 of 8 (`visitColumnRefsByName`) — the
      LAST and largest, and the one D2 row 7 names as the least predictable:
      its consumer `extraInScans` starts `allMatched := true` and only
      falsifies from inside the callback, so completing it removes conjuncts
      from `MultiHashJoin.Filters` directly. D3 predetermines its policy:
      plan slots SIGNAL, and the caller must treat "an opaque child exists"
      as NOT matched, inverting today's vacuous `true`. Assume the timed run
      + SF0.5 answer sweep are owed until the diff proves empty.**~~
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
      **↳ M0127 PREREQUISITE (2026-08-03): this verdict must be recorded
      BEFORE M0127's plan-shape changes land.** The item itself warns the
      EXPLAIN diff vs set A is only interpretable while the plan is unchanged,
      and M0127 P5 re-plans every query — take the quiet-host reading before
      M0127-P5.1 starts. It is bookkeeping, not engine work, and is one of the
      four M0125 items the M0127 banner waits on (exprwalk commits 5–8,
      -0047, -0040, -0013).

- [x] **M0125-0013 (bookkeeping half) — Q47's 8.4x runtime verdict** —
      **DONE 2026-08-03, SETTLED BY MEASUREMENT.** Quiet host verified
      (`pgrep -af run-nightly.sh` empty, load 0.28–1.21 vs the load ~10 every
      2026-07-30 timing was taken under), HEAD `374dc60e`, both engines back to
      back. Evidence `analysis/m0125-0013-q47-verdict/`.
      **By-value acceptance MET**: Q47 = **100 rows, byte-identical to PG** at
      SF=1. **Runtime: goopg 537.55 s vs PG 3.38 s = 159×** — so the 142 s this
      item argues about is itself superseded (and the SF0.5 `TIMEOUT` sighting
      is explained, not anomalous).
      **Both primary sources are REFUTED, in opposite directions.** `RESULTS.md`
      chunk 49–56's *"the 8.4× is the expected cost of real work, Q47 is NOT a
      regression"* cannot survive a 3.4 s PG reading — and neither can the
      `tpcds-round2 RC-1b` ledger row's *"(14s->143s confirms real work)"* it
      cites; the merged deliverable's §3.2 had the right **direction** for a
      since-falsified **reason** ("the row count did not move" — it moved to 100
      and the query got *slower*), and §6 item 2's "bounded but unattributed" is
      wrong on both words. Verdicts written into all four documents (both
      analysis reports, design doc § Q47, `docs/design/README.md`); the two
      refuted ledger rows are marked in place.
      **The runtime is ATTRIBUTED and needs no Q47-specific task.** The CTE is
      neither the cost nor recomputed (`cteScanOp.Open` keys `ctx.CTERowCache`
      on the CTE *name*, so `v1`/`v1_lag`/`v1_lead` share one evaluation even
      though EXPLAIN renders the body 3×; standalone **52.28 s / 63,745 rows**),
      leaving ~485 s in the `v2` three-way self-join — whose hash key degenerates
      to `i_category` because `splitEqualityForHash` returns only the FIRST
      disjoint equality. Measured over v1: `i_category` **10** distinct,
      `i_brand` 704, `s_store_name` 6, `s_company_name` **1**, vs **5,667** for
      the 4-key composite PG merge-joins on = a **~567× over-scan per probe**,
      twice. That is the pre-existing single-`LeftKey`/`RightKey` deferral
      (ledger `M0125-0011` / `M0125-0035`), **not** an RC-1b regression: RC-1b
      only made an already-defective join *reachable*, which is why runtime went
      17 s (empty) → 142 s (partial) → 537 s (fully correct) as the answer
      improved. **Q47 is now the sharpest known benchmark witness for that
      deferral** — hand it to M0127/M0125-0035, not to a new task.
      Anchor **`Q47,100,pinned` ADDED** to `ci/batch/tpcds-row-anchors.csv`.
      *(Original task text below.)*
      **M0125-0013 (bookkeeping half) — Q47's 8.4x runtime verdict**
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

- [x] **M0125-0027 — the SF=1 harness reports a DEAD SERVER as `OK`** —
      **DONE 2026-08-03 (loop #33).** Only `qex==0` may be `OK` now: a non-zero
      exit that is neither 124 nor `ERROR:`-bearing becomes `NOCONN`
      (output contains `connection to server`) or `UNKNOWN`, carrying
      `exit=<qex>` + first output line; a goopg-lane `NOCONN` bounces the
      server like a TIMEOUT (same `RESTART_AFTER_TIMEOUT` knob) so a dead
      server cannot cascade. Measured repro: dead port →
      `goopg Q99  NOCONN  0s  0  exit=2 psql: error: connection to server …`
      (was `OK 0s 2`); happy-path control `pg Q3 OK 0s 31` against live 65438.
      The SF0.5 script got the owed explicit arms and one step more: not just
      the sweep (`cmd_sweep`'s ERROR arm now also fires on `rc != 0`, routing
      connection-refused through the pg_isready-probe-and-restart path) but
      also `cmd_oracle`, where the same catch-all could capture the error
      text's "row count" into the GIT-TRACKED oracle as `OK` — new
      `PG_NOCONN` status, consumed by the sweep's existing `!= "OK"` skip arm.
      **The owed board audit is done and the board is CLEAN**: every
      `OK` cell at ≤1 s with ≤5 rows in the 16 resweep chunks is a PG cell
      with a genuine `(N rows)` block (the two `rows=2` candidates Q82/Q85
      spot-checked against their result files — real 2-row results); ZERO
      goopg cells bear the signature; the merged report's cells all carry
      substantial runtimes or `= PG` value checks; Q99's real cell is
      `OK 23 s / 90 = PG` (chunk-97-99 — its result FILE was overwritten by
      the 2026-07-30 discovery probe, the only defect artefact in the wild).
      No published SF=1 claim is affected. Design: §D6b added to
      `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md` per this
      item's "no new doc" directive; README row 0125-0027 points there.
      *(Original item body follows.)*
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
      **[→ M0127: absorbed 2026-08-03]** — both remaining motions (goal (a)'s
      outcome, goal (b)'s fixes) are M0127's own acceptance bar: timeout-class
      elimination and the Q3/Q10/Q18/Q7/Q9/Q21 recoveries are the S1/S3/S5
      exit gates of `docs/design/leftdeep-joins/` (09 §2-§3; 01 §6), so this
      umbrella closes by reference when M0127-P1.3/P3.5/P5.9 land. Keep this
      box unchecked as the standing record; do not select it as a standalone
      repair track. (Historical motions below are discharged as recorded.)
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
      cardinality regimes** **[→ M0127: absorbed 2026-08-03]** — Q21's
      completion is M0127's S3 exit gate by name (`leftdeep-joins` 06:
      hybrid-hash spill; 09 §2: "Q21 completes at SF1 under the standard
      cgroup cap") and clause 1 of the S5 acceptance bar (22/22 complete,
      09 §3); 01 §6(3) counts "Q21 stops OOMing at SF1 without MHJ" among the
      bundle's recovery set. The plain-EXPLAIN classification this item asks
      for is a useful P3 prerequisite, not a standalone fix track — fold it
      into M0127-P3.2's design loop. (filed 2026-07-30 by M0125-0031's first motion;
      evidence `analysis/m0125-0031-warm-tpch-20260730.md` §4; nightly keeps
      confirming: `tpch/Q21-timeout` AI-20260802-014405-018, also failed the
      previous run, and again 20260803 as AI-20260803-013955-020 — same
      subject, no separate M-NIGHTLY task). Q21 exceeds every
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

- [ ] **M0125-0033 — TPC-DS Q18 is 2.1× SLOWER under warm statistics**
      **[→ M0127: absorbed 2026-08-03]** — Q18 is in the S1 exit bar's named
      set ("Q18 ≤ 1.2× its R0 27.58 s", `leftdeep-joins` 09 §2) and in
      01 §6(1)'s recovery list; the seam de-materialisation (M0127-P1.1) and
      the executor stages S0–S2 are the mechanism this item's fix would have
      had to build. The Q18 EXPLAIN capture this item already owes was
      blocked on M0125-0037's EXPLAIN half (done); fold the capture into
      M0127-P1.3's S1 A/B instead. (filed
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

- [x] **M0125-0037 — C4: set operations are opaque to EXPLAIN and to the
      planner** **[→ M0127: stage (ii) absorbed 2026-08-03]**
      **↳ CLOSED 2026-08-04 by M0127-P5.1's landing**, exactly as this item
      pre-authorised below ("Close on P5.1's landing; nothing in stage (ii) is
      owed before it"). `buildInitialRels`
      (`internal/planner/joinsearch.go`) admits a set-op / subquery / CTE /
      VALUES leaf as an initial rel with a `PathPrebuilt` path instead of
      abandoning the search, so the DP is no longer blind to a rel behind a
      set-op node; stage (i) (the EXPLAIN half) has been done since
      2026-07-31 and the acceptance row `Q5 5|OK|100` green since then. No
      new work was performed for this item this loop. — stage (i)
      (EXPLAIN half) is done; stage (ii)'s mechanism claim ("the DP cannot see
      through a set-op node") is closed by M0127-P5.1's `PathPrebuilt` leaves
      (subquery/CTE/VALUES/pinned-unnest admission — `IMPLEMENTATION-TODO`
      P5.1 "closes the leaf-whitelist gap"), and its acceptance row
      (`Q5 5|OK|100`) has been green since 2026-07-31 by independent
      measurement. Close on P5.1's landing; nothing in stage (ii) is owed
      before it. (filed 2026-07-31 by M0125-0026; evidence same README §"C4").
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

- [x] **M0125-0047 — goopg's plan is restart-NONDETERMINISTIC for self-joined
      identical relations: SF0.5 Q85 tie-swaps `cd1`/`cd2` between server
      starts of the SAME binary** **DONE 2026-08-03 (loop #40)** (filed 2026-08-03 by M0125-0002 commit 4;
      evidence `analysis/m0125-0002-c4-plans-20260803/README.md` §"q85").
      Q85 scans `customer_demographics` twice (aliases cd1/cd2, identical
      estimated rows); which alias lands in which join slot flips across
      restarts — 3 restarts of one binary produced cd2-first twice and
      cd1-first once. PG's planner is deterministic (`add_path` tie-breaks
      are stable given identical inputs), so any flip is a PG-divergence and,
      worse, an INSTRUMENT hazard: every EXPLAIN-based A/B in this repo
      (plan-snapshot, the SF0.5 capture, `make plan-diff`) can report a
      phantom hunk on Q85-shaped queries — commit 4's arms happened to
      agree, and the flip surfaced only in the probe arm. Suspect: map
      iteration order in join-graph construction or a non-stable sort over
      equal-cost/equal-size candidates (tie-break lacks a total order over
      table indices). Next step: plan Q85 in-process ~20× in a unit test to
      find the unstable site; fix = deterministic tie-break (compare FROM
      indices last), then 3-restart re-check. NOT one of the banner's
      carve-outs (the gates PASS; the flip is rare) — parked under M0125
      as instrument-integrity debt for the remaining walker commits 5–8,
      which lean on byte-identical EXPLAIN A/Bs.
      **↳ M0127 PREREQUISITE (2026-08-03): close this BEFORE M0127-P5.4.**
      The M0127 banner lists this item among the four M0125 items M0127 waits
      on, and M0127-P5.4's "deterministic tie-break" is specified to build on
      this item's fix — the 09 §4 plan-shape ratchet (a pinned mismatch
      budget that must not grow across commits) cannot exist while plans flip
      Q85-style across restarts. The unit-test search for the unstable site
      (plan Q85 in-process ~20×) is host-independent and can run anytime.
      **↳ CLOSED 2026-08-03 (loop #40).** Design
      `docs/design/0125-0047-joinorder-tiebreak-determinism.md`; evidence
      `analysis/m0125-0047/`. The unstable site is `pickNextByEdge`
      (`internal/planner/joinorder.go`), the greedy comma-FROM reorder's
      "take the smallest edge-connected relation" step: it ranked candidates
      while ranging over `edges[j]`, a `map[int]struct{}`, with a **strict**
      `rowCounts[k] < rowCounts[best]`. A strict comparison keeps the
      incumbent on a tie, so the winner was whichever candidate Go's
      per-`range` randomiser yielded first — the tie-break had no total order
      over relation indices. The suspicion filed with this item ("map
      iteration order … or a non-stable sort") was right on the first branch;
      there is no non-stable sort (`sort.Slice` appears nowhere in the
      planner's non-test files). A query that scans one table twice makes the
      tie **unavoidable** — the two aliases are the same relation, so their
      statistics are identical by construction and no `ANALYZE` can separate
      them. Fix = compare FROM indices last, which resolves ties to source
      order and matches the rule `smallestUnused` and `orderByConnectivity`
      already used, so all three pickers in the file now share one tie-break.
      **Audited and found already deterministic:** `smallestUnused` (slice
      walk), `orderByConnectivity` (`k < next` is total — its documented
      "cross-free source order is a fixed point" property holds *because* of
      that), and the bushy DP (`g.edges` is a slice, subsets/splits come from
      the `enumerateSubsets`/`enumerateSplits` generators, `dp` is lookup-only).
      **Measured:** 96-query SF0.5 EXPLAIN capture, 3 restarts of the fixed
      binary → **all 96 byte-identical pairwise** (the item's acceptance), and
      **before vs after 96 byte-identical** — the fix converges on the plan
      the baselines ALREADY contain (Q85 keeps its `cd2`-first shape,
      `6fb943ca2c7aa936`), so **no snapshot needs re-pinning and no earlier
      A/B is invalidated**. A 10-restart Q85 probe reproduced the flip at HEAD
      (1 divergence in 10 pre-fix; 0 in 10 post-fix) — the two binaries differ
      in nothing but that comparison, so it reads causally. **Two things bind
      later loops:** (1) the restart probe is UNDERPOWERED and must not be
      cited as a determinism gate — at the observed ~10% flip rate, 10 clean
      restarts happen by chance ~35% of the time; the proof is the unit test,
      which samples Go's per-`range` randomiser 200× in-process at zero
      restart cost (ledger row). (2) **This is NOT a proof that the planner is
      deterministic overall** — the audit covered the join-order passes, not
      every map; `TestPlanQ85IsDeterministic` (100 whole-`Plan()` runs
      compared by an alias-bearing REFLECTIVE fingerprint, because the
      existing `planShapeString` renders scans as `x.Table.Name` and
      `cd1`/`cd2` share one `*catalog.Table`, so it prints the two
      permutations identically and cannot see this defect at all) is the
      harness shape a corpus-wide sweep should generalise, and M0127-P5.4's
      plan-shape ratchet is its consumer (ledger row). All four guards in
      `internal/planner/joinorder_determinism_test.go` were proven to FAIL
      against the pre-fix body before the fix landed.
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
      **↳ INDEPENDENT OF M0127 (2026-08-03): neither absorbed nor a
      prerequisite.** C6 is aggregate-side (grouping-sets expansion), outside
      the leftdeep-joins bundle's scope (its out-of-scope list: no
      `AGG_MIXED`/`AGG_SORTED` port). It may proceed before,
      during, or after M0127 on its own track. Tracked via the M0125 section, not the M0127
      wait list — it must not be silently closed. Its Q18/Q67 runtime
      linkage is re-measured under M0127-P1.3/P5.9 (see the -0033 skip
      note), but the ROLLUP fan-out fix itself is this item's alone.

- [ ] **M0125-0041 — C3's second half: a correlated SCALAR-aggregate subquery is
      re-evaluated per outer row** **[→ M0127: residual absorbed 2026-08-03]**
      — the decorrelation root cause is fixed (loop #14); the remaining factor
      is C1 = the `Nested Loop (CROSS)` shape, which M0127's P5 DP is the
      documented successor of (`leftdeep-joins` 03 §4-§5: join methods are
      costed paths generated inside the search — no post-DP rewrites; M0126-0013
      named the bundle as the Q9-enumeration successor). Q30/Q81 complete
      inside the SF0.5 gate under M0127's S5 acceptance (09 §3 clause 4's
      zero-delta instrument covers them). The `ca_state = 'AR'` descent probe
      this item names is a useful P5.4 qual-placement input — fold it in, do
      not build a standalone join-order fix. (filed 2026-07-31 by `M0125-0036`, which
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

**⚑ MILESTONE TERMINAL 2026-08-03 (loop #31) — COMPLETE AS DOCUMENTED NO-GO.**
All 13 tasks are closed. `GOOPG_COST_DRIVEN_JOINORDER` remains DEFAULT OFF:
acceptance run 1 (`evidence/acceptance-run-1.txt`) failed clauses 1–3 on Q9
alone and triggered -0013; the -0013 remediation (2 commits) did not fix Q9
and newly regressed Q5 into hang-class, so run 2
(`evidence/acceptance-run-2.txt`) is the milestone's final documented no-go —
per the acceptance-bar paragraph above, a measured no-go is a successful
completion. What ships ON by default from this milestone: MHJ retirement
(-0011, `mhjPackingEnabled=false`), the estimateJoin max(l,r) fallback cap
(-0010), and the Stage-0 executor de-materialisation (-0003). Fusion (KS1) is
permanently off (-0007, DS05 correctness). Successor design:
`docs/design/leftdeep-joins/` (user-directed, 2026-08-02).

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

- [x] **M0126-0001 — Pre-measurement confound removal + pinned R0 baseline.** Capture the R0 acceptance-bar baseline FIRST (timed
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
- [x] **M0126-0002 — `EstimateRows` `*MultiHashJoin` arm + plan re-baseline.**
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
- [x] **M0126-0003 — Live-path de-materialisation + slot-taking hash-key
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
- [x] **M0126-0004 — Legacy `Build`-path slot chaining.** **CLOSED 2026-08-03
      without implementation — deferred 2026-08-01 (loop #25 ledger row: deep
      nextLazy rewrite judged too fragile after 0b's Q12=0 scare), then
      ABSORBED by the `docs/design/leftdeep-joins/` bundle (05-executor-
      pipeline-rework carries the slot-chaining work as part of the probe-seam
      de-materialisation; the milestone is terminal per -0013's final no-go,
      so no further M0126 measurement depends on it).** Original filing:
      **CONDITIONAL on
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
- [x] **M0126-0005 — Stage 0 A/B + fusion go/no-go decision.** No code. TPC-H
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
- [x] **M0126-0006 — Fusion scaffolding + differential harness (switch OFF).**
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
- [x] **M0126-0007 — Fusion enablement and measurement.** **DONE 2026-08-02 —
      `evidence/stage2-fusion-verdict.txt`: KS1 PERMANENTLY OFF** (DS05 Q14
      returns 100 rows vs oracle 200 with fusion ON — correctness delta, KS1
      flips off without debate per bundle 10 §4; fusion code stays in-tree,
      never enabled; "leave the switch off permanently" is a recorded
      completion per the task spec). Root-cause hypothesis (fused-cascade
      duplicate-elimination/key-collision) recorded in the verdict file; the
      leftdeep-joins bundle makes `fusedHashJoinOp` deletable (S7 inventory).
      Original filing: **CONDITIONAL on
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
- [x] **M0126-0008 — Cost-driven order re-validation with symmetric
      timeouts.** **DONE 2026-08-02 — `evidence/stage3-order-ab.txt`.** 600 s
      symmetric, 65433, fresh servers. Q5 prior HANG **REFUTED as asymmetry
      artifact** (7.1× WIN, 8.08 s); Q9 PATHOLOGICAL (30 min+, killed);
      regressions Q7 1.81× / Q10 1.92× / Q11 1.56× / Q18 1.25× / Q21 1.37×;
      row counts all match. Fork ENTERED → -0009/-0010.
      Original filing: The 2026-07-24 A/B pair used 600 s vs 300 s and is invalid as
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
- [x] **M0126-0009 — Order-failure attribution (diagnosis only, bounded).**
      **DONE 2026-08-02 — `evidence/order-attribution-{summary.md,Q9.txt,
      residuals.txt,ds05-sampling.txt}`.** All regressions attributed
      class-(a) cardinality (FK-chain ndistinct-product explosion; Q9 the
      demonstrator: 5.9e15 estimate), routed to -0010. (-0012's run-1 later
      RE-classified Q9's surviving residual as class-(c) build-side memory,
      which triggered -0013 — both classifications are on record.)
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
- [x] **M0126-0010 — Bounded order-quality / cardinality fixes.**
      **DONE 2026-08-02 — commit `be1f88d5`** (cap estimateJoin fallback at
      max(l,r), preventing the FK-chain explosion; 1 landed commit of the
      4-commit budget; STOP taken at -0012's measurement). Q9 not rescued by
      cardinality alone (see -0012/-0013).
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
- [x] **M0126-0011 — Retire `MultiHashJoin` as a plan node (default off, code
      retained).** **DONE 2026-08-02 — commit `e85e5347`** (`mhjPackingEnabled`
      default → false; MHJ code retained and reachable via
      `SetMHJPackingEnabled`; SPOTCHECK PASS). **UNCONDITIONAL (sequencing-gated: -0005 decided and -0007
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
- [x] **M0126-0012 — Acceptance measurement + conditional default flip.**
      **DONE 2026-08-02 — `evidence/acceptance-run-1.txt`: documented NO-GO,
      TRIGGERED -0013.** Clauses 1–3 FAIL on Q9 alone (600 s+ vs 52.18 s
      baseline, 11.5×; 21/22 pass everything); clause 4 deferred as moot.
      Q9's residual re-classified class-(c) build-side memory. No flip;
      cost-driven stays default-off.
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
- [x] **M0126-0013 — Build-side memory-aware hash costing (conditional
      remediation, filed by the USER 2026-07-31) + bar re-check.**
      **DONE 2026-08-03 (loop #31) — FINAL NO-GO, milestone-terminal.**
      Both budget commits landed 2026-08-02 (`c63f8023` DP large-build
      penalty >2M rows; `e13d6c6f` inner_pages I/O charge in hashJoinCost;
      SPOTCHECK PASS both, default-arm plans unchanged). Bar re-check
      `evidence/acceptance-run-2.txt` (per -0012's protocol, deviations
      recorded in-file): **Q9 UNCHANGED at hang-class AND Q5 NEWLY
      REGRESSED 8.15 s → 600 s+** — the penalties re-ranked the winning
      Q5 order out (the DP now routes the 6M-row lineitem⋈orders
      intermediate through two probe passes to dodge build charges).
      Run 2 strictly worse than run 1 (2 timeouts vs 1); clauses 1–3 FAIL,
      clause 4 moot. `GOOPG_COST_DRIVEN_JOINORDER` remains DEFAULT OFF.
      Arm A' control: 22/22 in 589.0 s, canonical row counts. Successor:
      `docs/design/leftdeep-joins/` (join-search restructure; absorbs the
      Q9 enumeration blocker). Two ledger rows 2026-08-03 (revert-or-keep
      of the two -0013 commits; uncancellable cost-driven joins).
      Original filing: **CONDITIONAL: fires ONLY on an -0012 no-go.** The Q5/Q9/Q21 HANGs are
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

## M0127 — PG-shaped join search: PG-identical join search + fusion-free join executor (filed 2026-08-03)

Milestone: `docs/milestones/0127-pg-shaped-join-search.md`. Implementation
plan (the authoritative task decomposition): `docs/design/0127-pg-shaped-join-search.md`.
Design of record: `docs/design/leftdeep-joins/` — README (scope + invariants),
**01** (measured evidence + the exact recovery set), **02** (plan-shape
contract), **03** (the `standard_join_search`/`join_search_one_level` analogue,
all three phases), **04** (one cost currency, rows once, FK-aware selectivity),
**05** (the fusion-free hash cascade), **06** (hybrid hash spill), **07** (the
other join operators), **08** (stages/flags/removal inventory), **09**
(verification + acceptance). **The bundle is the design; the task bodies below
cite bundle sections instead of restating them.** Do not re-derive what the
bundle settles. Never modify `docs/design/leftdeep-joins/` itself.
**Priority: immediately after M0125's M0127-needed items** (filed by the
panel per the user directive 2026-08-02, amended 2026-08-03; successor to the
terminal M0126 no-go) — above the M-NIGHTLY backlog and above M0123. See the
amended Current Priority banner. Not selected while M0125-0002 (exprwalk
commits 5–8), M0125-0047, or M0125-0013 is open (M0125-0040 ROLLUP is an
independent track outside bundle scope; see its item body).

**Acceptance bar (09 §3 is normative):** TPC-H SF1 **22/22 complete** (zero
hang/OOM/timeout/row-count mismatch); total wall time **≤ 1.2×** the faster of
pinned R0 (493.31 s) and a contemporaneous integer arm; **no query > 2× R0 —
Q9 explicitly ≤ 170.9 s** (2 × R0's 85.46 s); TPC-DS SF0.5 **zero row and
checksum deltas**; **no `MultiHashJoin` in any emitted plan; fusion never
triggers**; **bushy capability** — every bushy spine PG 18.3 can produce,
goopg can produce (parity-gate spine diff; no `expected-bushy` category).
Stage gates: S1 exit = Q3/Q10/Q18/Q7 each ≤ 1.2× their R0 times
(8.46 / 6.04 / 27.58 / 25.13 s; R0 total 493.31 s); S3 exit = Q21 completes at
SF1 under the cgroup cap at default `work_mem` + forced-spill run byte-identical
to no-spill. A documented no-go with attribution (09 §6) is a successful S5
outcome; an unmeasured outcome is the only failure.

**Read before picking any task here.** (1) **Executor first, planner second,
deletion last** (08 §1): the M0125-0002 lesson — regression direction is
unpredictable per query, measure per commit; the executor stages improve the
CURRENT default planner's output immediately (the MHJ-retirement seam costs
Q3/Q10/Q18/Q7). (2) **Every P5 task lands dark** behind `GOOPG_PGSHAPED_DP`
(OFF while soaking, flipped ON as the acceptance event); it **replaces**
`GOOPG_COST_DRIVEN_JOINORDER` (retired at S5, 08 §6). Collapse-limit wiring has
its own sub-flag `GOOPG_PGSHAPED_COLLAPSE` and soaks separately (P5.8).
(3) **Soak coexistence (08 §3):** non-searched shapes keep the current pipeline
including `rewriteJoinsToNLI` and qual-placement passes; searched roots are
tagged and the passes skip tagged subtrees; `reconcileNLILayout` must be a no-op
on searched trees (assert, don't assume). (4) **S2 and S5 each re-baseline
`plan_snapshots/` in the same commit** with the diff summarised in the commit
message. (5) Every implementation task runs in a **git worktree off pinned clean
HEAD**, staged by explicit pathspec, never `git add -A`, and re-runs its own
named guard test after any rebase or handoff. (6) Timed measurements need a
quiet host and constant server age (sweep-tail discipline); DS05 results record
as "57/99 content-verified, 42/99 count-only".

**M0125 items absorbed by this milestone (marked `→ M0127` in their bodies, do
not select as standalone tracks):** M0125-0031 (warm-stats planning line —
remaining repair = this milestone's acceptance), M0125-0032 (Q21 — S3 exit by
name), M0125-0033 (Q18 warm regression — S1 exit set), M0125-0037 stage (ii)
(set-op DP visibility — P5.1 `PathPrebuilt` leaves), M0125-0041's residual
(C1 CROSS shape — P5 DP). **Closed mechanisms this milestone deletes:**
M0125-0035b's `reselectDegenerateHashKeys` → P2.2 (same commit + Q78-class
degeneracy regression test); M0126-0011's MHJ node/operator → P6.2;
M0126-0006/-0007's fusion → P6.1; M0126-0004's slot-chaining deferral →
un-deferred at P1.1. Supersession stamps at P6.4 (0034-0001, 0038-0001,
cost-model/09 §3, 0043/0063/0125/0126 MHJ chapters).

- [x] **M0127-P0.1 — `mergedKeySlot` hoist to `Open`.** Shape-invariant per
      join; rebind `.row` per pull; zero steady-state allocs in the seam
      microbench. IMPLEMENTATION-TODO P0.1; 05 §3 (E2). Files:
      `internal/executor/operators_join_agg.go` (:986-1014, build :590/:646/:702,
      probe :1266/:1269). Bar: UNITS + SPOT + BENCH.
      **↳ DONE 2026-08-03.** `mergedKeySlotCache` (two per `joinOp`:
      `lazyBuildKeySlot`, `lazyProbeKeySlot`) holds the hoisted `VirtualSlot`;
      all five call sites call `rebind`, which swaps one interface word and
      rebuilds only on a `(realWidth, nullWidth, realOnLeft)` change — the
      child schemas fix those at `Open`, so the build loops' empty-schema
      `width == 0` fallback is the only mid-loop rebuild and fires at most
      once. Seam microbench `BenchmarkMergedKeySlotSeam` **4.10 ns/op, 0
      allocs** vs the uncached arm's **185.8 ns/op, 344 B, 5 allocs** (the
      null `Row`, its `MaterializedSlot`, the `[]virtualCol`, the sources
      slice, the `VirtualSlot`). Guards: `join_merged_key_slot_test.go`
      (cached-vs-fresh equality both orientations, source rebinding, shape
      change, 0-alloc `AllocsPerRun`). UNITS PASS, SPOT PASS (Q12=2/Q13=35),
      SMOKE via hook. Out of scope by design (05 §3): `fused_hash_join.go`'s
      two call sites — fusion dies at P6.1. Progress log: design doc §6.
- [x] **M0127-P0.2 — single-pass build.** Fold `drainRowsBounded`'s budget into
      `buildLazyHashTable`'s build loop; delete the re-iteration
      (`rowsOp`-per-row `MaterializedSlot` allocs). Keep owned-copy discipline
      (M0097-0058). IMPLEMENTATION-TODO P0.2; 05 §4 (E3). Bar: UNITS + SPOT +
      RACE (shared-build interplay).
      **↳ DONE 2026-08-03.** The two build loops moved into
      `joinOp.buildLoopLeft` / `buildLoopRight`, which pull straight from the
      child, key off the P0.1 hoisted slot and insert — no `drainRowsBounded`,
      no re-iteration, no `MaterializedSlot` per build row, and no temp-file
      round trip. The drain's owned-copy became `ownedBuildRow`
      (`rowHasArena` → `cloneRowOwned`, else the O(width) struct copy) and now
      runs only for rows that survive the NULL-key check. **`ctx.WorkMem` went
      with the drain deliberately** — it bounded the intermediate `[]Row`, never
      the hash table it fed (every spilled row was read straight back in and
      inserted), so peak memory is unchanged; real work_mem enforcement is the
      batched hybrid hash at P3.2, whose stated prerequisite is this shape.
      Ledger row `2026-08-03 M0127-P0.2` records the gap vs
      `nodeHash.c: ExecHashIncreaseNumBatches`. Guards
      (`join_single_pass_build_test.go`): both loops over a child that reuses
      ONE buffer (the M0097-0058 aliasing class the drain used to absorb),
      INNER + SEMI lanes + BuildLeft orientation + NullAware bookkeeping;
      verified they bite (stub `ownedBuildRow` → all three fail). UNITS PASS;
      SPOT PASS (Q12=2 / Q13=35, 18.4 s query phase vs P0.1's 32.3 s, peak
      10,332 MB vs 10,767 MB — single uncontrolled runs); SMOKE via hook.
      **RACE: `make race-gate` is red at clean HEAD too** for the unrelated
      pre-existing `buildEnvInFlight` global (M0126-0006) already filed in
      M-NIGHTLY above — reproduced in a HEAD worktree; every race frame is
      `buildWithEnv`, none in the new build loops. Progress log: design doc §6.
- [x] **M0127-P0.3 — single-map build.** Planner threads key-type info on
      `planner.Join`; executor picks int64 vs string map before build; extend
      int64 path to Semi/Anti (CTID exception preserved); delete
      `lazyHashFinalize`'s dual-map dance. IMPLEMENTATION-TODO P0.3; 05 §4 (E3).
      Bar: UNITS + DS05.
      **↳ DONE 2026-08-03.** `planner.Join.HashKeysAreInt64`
      (`internal/planner/join_hashkey.go`) types both key exprs — `exprType`,
      falling back to the merged key column space for a ColumnRef whose `Type`
      was left zero — and `buildLazyHashTable` sets `lazyHashIsInt` from it
      BEFORE the first build row. `lazyHashInsertDatum` fills that map only;
      `lazyHashFinalize` and `lazyBuildAllInt64` are gone. The dual-map build
      cost every int-keyed join a second full copy of its build side held
      *simultaneously* with the first (the string map was freed at finalize —
      after peak). Semi/Anti now reach the int64 lane (the old INNER-only gate
      existed only because they never ran finalize); the CTID build is the
      exception and stays on the string map, since `lazyHashCTID` is keyed in
      lockstep with it. `numeric` is deliberately excluded from the int64
      promise (values, not the type, decide representability there) — ledger
      row `2026-08-03 M0127-P0.3`; `demoteIntHash` re-keys mid-build if a
      typed-integer column ever yields a non-int64 datum, exactly (because
      `datumKey(KindInt(v))` *is* `canonicalNumericKey(v, 0)`). Guards:
      `join_hashkey_test.go` (real SQL → plan → every hash join reports true,
      incl. mixed int widths; text/numeric false; nil/non-hash/missing-key
      guards) and `join_single_map_build_test.go` (the OTHER map is never
      allocated; Semi + Anti lanes; demotion preserves every row and payload).
      UNITS PASS. **DS05: MISMATCH=0 / CKMISMATCH=0 / ERROR=0 across all 99** —
      Q1-Q72 (66 PASS, `sweep-20260803-114208.txt`) + Q73-Q99 (26 PASS,
      `sweep-20260803-122614.txt`). The first run aborted after Q72 on a
      transient `systemd-run` scope-name collision (previous scope not yet
      released when the post-timeout restart re-created it → 180 s readiness
      timeout), NOT a code failure; the tail was re-run as a subset probe, so
      the two halves together are the coverage rather than one stamped gate.
      Q47/Q72 TIMEOUT = the known boundary pair (263-308 s vs the 300 s cap;
      Q72 alone reads 273/263/308/480 s/TIMEOUT across five prior sweeps at
      unchanged code). SMOKE via the commit hook. Progress log: design doc §6.
      **P0 is now CLOSED — next is P1.1.**
- [x] **M0127-P1.1 — legacy-path slot chaining (the M0126-0004 deferral,
      un-deferred).** Probe child slot as `lazyVirtualOut` source; rebind on
      pointer change + copy fallback on type change; delete `slotRow(probeSlot)`
      at :1254 and the vestigial `lazyKeyRow`. Env kill-switch
      `GOOPG_JOIN_SLOT_CHAIN=off`. IMPLEMENTATION-TODO P1.1; 05 §2 (E1; F7
      contract — children do not return a stable slot object; fan-out test
      required). Bar: full REGRESS + DS05 + SPOT + seam microbench 0 allocs.
      **↳ DONE 2026-08-03.** `bindProbe` binds the probe child's own
      `TupleSlot` into `lazyVirtualOut.sources[lazyProbeSrcIdx]` on **every**
      pull, and `outerOnlyEmit` composes the Semi/Anti emit through a new
      `lazyOuterOnlyOut` VirtualSlot instead of `lazyOuterOnlySlot`. Both
      `slotRow(probeSlot)` sites and the `lazyRow` / `lazyKeyRow` fields are
      gone. **F7 is handled structurally, not by a type check:** rebinding
      unconditionally per pull is correct for *any* concrete slot the child
      returns, so there is nothing to detect — the copy fallback exists for the
      kill switch and for a slot that cannot serve the composed shape. The one
      place the width test changes an observable result is the Semi/Anti emit,
      where the probe slot IS the whole tuple: an over-wide probe keeps its
      pre-P1.1 width via the copy rather than being silently narrowed to
      `len(o.schema)` (ledger row `2026-08-03 M0127-P1.1`; PG cannot produce
      that shape because `ExecHashJoin` projects through `ps_ProjInfo`).
      The aliasing is safe by control flow only — a probe row is pulled after
      every match of the previous one has drained — so `bindProbe` asserts
      exactly that (`lazyActive` must be false) rather than trusting it.
      Guards (`join_slot_chain_test.go`): the mandatory fan-out test, run over
      FOUR probe-child slot shapes including one that **rotates shape per
      pull** (the F7 case), plus Semi/Anti, both LEFT null-pad exits
      (hash-level and predicate-level), the over-wide copy fallback, the
      kill-switch arm, and the rebind assertion. **Seam: 0 B / 0 allocs**
      (`BenchmarkProbeSeam/chained` 58.7 µs per 1024-row pass) vs the
      kill-switch arm's **172,179 B / 2,048 allocs, 221.3 µs** — 2 allocations
      per probe row removed, 3.8× on the seam; `TestProbeSeamZeroAllocs` pins
      it with `AllocsPerRun`. REGRESS (full `TestPort_RegressSuite`) PASS in
      659 s; UNITS PASS; SPOT PASS (Q12=2 / Q13=35, 17.8 s query phase, peak
      11,594 MB); **DS05 MISMATCH=0 / CKMISMATCH=0 / ERROR=0 across all 99**
      (Q1-Q72 + a Q73-Q99 subset probe — the first run died after Q72 on the
      same post-TIMEOUT `systemd-run` scope collision P0.3 hit, and Q47/Q72 are
      the known 300 s boundary pair). SMOKE via the commit hook.
      **Not in scope:** the slab/`buildRec` path (`fillFromTupleSlot` already
      had its VirtualSlot fast path from M0126-0003) and `fused_hash_join.go`,
      which dies at P6.1.
- [x] **M0127-P1.2 — worker-path exercise.** The P1.1 seam under `BuildWorker`
      (`inWorker=true`) integration test — fusion's decline-in-worker precedent
      says this path diverges silently. IMPLEMENTATION-TODO P1.2. Bar: RACE.
      **↳ DONE 2026-08-03.** `join_worker_path_test.go` asserts three claims
      that do not imply one another: (1) the seam **engages** under
      `BuildWorker` — structural, not by result, because a declined seam
      returns identical rows (that is the copy fallback's whole design, and
      the reason fusion's `inWorker` decline went unnoticed); (2) rows
      produced by a chained emit and **retained** across later pulls survive
      `MaterializeForTransfer` + `AssertTransferable` — the worker batches 256
      rows before sending, so a probe source the next pull overwrites corrupts
      every row but the last, while a serial consumer formatting each row on
      arrival never notices; (3) real-Gather identity over the P8 corpus in
      BOTH seam arms × leader-participation on/off × 2 and 4 workers — the
      leader-off arm is the one where EVERY row crosses the goroutine
      boundary. All three bite: `GOOPG_JOIN_SLOT_CHAIN=off` fails (1), and a
      stubbed shallow `MaterializeForTransfer` fails (2) and (3) (`got
      "NULL|NULL|NULL"`, `"2|d-2"` vs `"200|d-0"` — `VirtualSlot.Row()` hands
      back a POOLED row, so the corruption is real, not theoretical).
      **The exercise found the divergence it was written to look for, in the
      BUILD path rather than the seam:** `buildEnvInFlight` — see the
      M-NIGHTLY race item above, fixed here, `make race-gate` green
      end-to-end for the first time since M0126-0006. The seam itself did NOT
      diverge in a worker. Gates: RACE (`make race-gate` EXIT=0, all
      packages; the same executor tests were red at HEAD with every frame in
      `buildWithEnv`), UNITS PASS, SPOT PASS (Q12=2 / Q13=35, 17.8 s query
      phase, peak 11,597 MB), SMOKE via the commit hook. Ledger row
      `2026-08-03 M0127-P1.2` records the un-audited remainder: only this one
      global was removed, not the package's build/exec-time globals as a
      class. Progress log: design doc §6.
- [x] **M0127-P1.3 — S1 A/B evidence run.** Q3/Q10/Q18/Q7 ≤ 1.2× R0; no other
      query > 1.2× vs pre-S1 HEAD; file `analysis/leftdeep-joins/<date>-s1-ab.txt`.
      No code. Bar: bar met or attributed (09 §6) **before P2 starts**.
      IMPLEMENTATION-TODO P1.3; 09 §2.
      **↳ DONE 2026-08-03 — bar ATTRIBUTED (the gate's second leg), evidence
      `analysis/leftdeep-joins/2026-08-03-s1-ab.txt` + 5 raw artefacts.**
      pre-S1 HEAD = `766d2cdb`; S1 = `99951944`. **Clause (2) met outright: not
      one query in the suite is slower under S1 than under pre-S1 HEAD** (max
      ratio 1.00); total 619.26 s → 360.82 s (0.58×), = 0.73× R0's 493.31 s.
      Clause (1): Q7 PASSES (0.74× R0); **Q3 (1.98×), Q10 (2.51×), Q18 (1.26×)
      MISS — attributed class (b) plan shape, INHERITED, not caused by S1.**
      Two facts exclude S1: (i) EXPLAIN for all 22 queries is **byte-identical**
      between arms, estimates included → every S1 delta is class (d) at constant
      plan, and all are wins or a wash; (ii) the deficit is there with S1 absent
      — pre-S1 HEAD alone is 4.08×/3.89×/2.92× R0 on Q3/Q10/Q18. The cause is
      **MHJ de-selection between R0's HEAD and pre-S1 HEAD**: R0's snapshot has
      9 `Multi-Way Hash Join` nodes (Q2/Q3/Q7/Q9/Q10/Q11/Q18/Q21), both arms
      today have zero, and the estimates moved off R0's degenerate `rows=1` —
      the M0125 estimate work stopped picking the shape R0 was pinned to. S1
      still recovered 68/147/48/86 % of the Q3/Q7/Q10/Q18 deficit from the
      executor alone. **The residual is the bundle's thesis, not S1's bug:**
      09 §3 item 5 requires ZERO MHJ at S5 and P6.2 deletes it, so restoring
      R0's shape would move away from the endpoint. **Q3/Q10/Q18 are carried
      into P5 as named regression witnesses** (P5.3a bushy enumeration +
      P5.6/P5.7 sizing/cost are the tasks that must clear them). Order control:
      arms ran pre-S1(cold) → S1 → pre-S1(warm); the warm replicate reproduced
      the cold one within 7.1 % on every query > 0.5 s, and S1 is compared
      against the warm one. 22/22 complete in all three arms, row counts
      identical across arms and to R0. No code landed. **P2 may start.**
- [x] **M0127-P2.1 — `planner.Join.HashKeys []JoinKeyPair`.** Search/pushdown
      fills all equality conjuncts; residual keeps non-equijoin only; EXPLAIN
      key-list rendering; plan-snapshot re-baseline same commit.
      IMPLEMENTATION-TODO P2.1; 05 §5 (E4 planner side). Bar: UNITS + SPOT +
      DS05 + PLAN (snapshot diff reviewed).
      **↳ DONE 2026-08-03.** `JoinKeyPair` + `Join.HashKeys` publish EVERY
      usable equi-pair (PG's `hashclauses`/`mergeclauses`), where goopg has
      carried exactly one pair since M0003. New
      `internal/planner/join_hash_keys.go`. **The list is derived by ONE late
      pass at the tail of `Plan()`, not filled at the nine construction
      sites** — six later passes rewrite key/predicate expressions in place
      (`reresolveJoinByName`'s predRebind, bushy `subRemap`, `FoldConstants`,
      `lowerSubPlanParams`, qual placement, `reselectDegenerateHashKeys`), so
      an early field is one all six must maintain, and the one time that was
      missed a shared ColumnRef pointer got mutated under the keys
      (M0097-0060). `HashKeys[0]` is `(LeftKey, RightKey)` **by pointer**;
      extras are cloned. `splitEqualityForHash` now delegates to the shared
      `forEachEqualityForHash` core (behaviour byte-identical), so the
      single-pair and full-list views cannot drift —
      `TestSplitEqualityForHashMatchesFirstPair` is the anti-drift guard.
      `(*Join).Residual()` is the non-equijoin projection P2.2 consumes.
      EXPLAIN grew `Hash Cond:` / `Merge Cond:` (`formatJoinKeyCond`),
      upstream's `show_upper_qual` shape incl. the `rtable_size > 1`
      prefixing rule and `make_ands_explicit`'s `((a = b) AND (c = d))` —
      goopg emitted NO join condition line at all before this, which is why
      M0125-0035b's degeneracy bug looked PG-identical while running
      quadratically. **`Predicate` is deliberately NOT trimmed** (the
      executor is still single-key; trimming would drop the second equality
      from enforcement) — ledger row `2026-08-03 M0127-P2.1`, discharged by
      P2.2. Gates: **PLAN re-baseline `plan_snapshots/m0127-p21-hashkeys.txt`
      — IDENTICAL to `m0125-0002-c7-after` once the new key-condition lines
      are filtered out, i.e. ZERO plan-shape change across all 22 TPC-H
      queries**; 61 `Hash Cond:` lines, **2 true multi-column lists** (Q9's
      and Q20's partsupp⋈lineitem, the exact shape M-NIGHTLY
      tpch/Q20-timeout named). UNITS PASS. SPOT PASS (Q12=2 / Q13=35, 15.6 s
      query phase, peak 10,230 MB). **DS05 MISMATCH=0 / CKMISMATCH=0 /
      ERROR=0 across all 99** (Q1-Q72 `sweep-20260803-162954.txt` 67 PASS +
      Q73-Q99 `sweep-20260803-171238.txt` 26 PASS; the tail is a stamped
      subset probe after the same post-TIMEOUT `systemd-run` scope collision
      P0.3/P1.1 hit; Q47/Q72 = the known 300 s boundary pair). SMOKE via the
      commit hook. `TestExprSwitchInventoryIsPinned` (M0125-0001's RC-1a
      gate) forced the sublink descent onto `walkExprRefs` + `scopeDescend`
      instead of a hand-written 5-arm Expr switch. **Next is P2.2, the
      executor half of the sibling pair.** Progress log: design doc §6.
- [x] **M0127-P2.2 — executor composite keys.** All-int64 fixed-width pack;
      mixed → concatenated `datumKey`. **Delete `reselectDegenerateHashKeys` +
      its planner pass (same commit)**; add a Q78-class degeneracy regression
      test (constant-pinned first key column must not degrade to one bucket).
      IMPLEMENTATION-TODO P2.2; 05 §5 (E4 executor side). Bar: UNITS + SPOT +
      DS05 + SIBLING (planner keys ↔ executor key encode).
      **↳ DONE 2026-08-03.** New `internal/planner/join_exec_keys.go`
      (`Join.ExecHashKeyPlan` → `{Keys, Residual, Int64Keys}`) and
      `internal/executor/join_composite_key.go`. `reselectDegenerateHashKeys`
      and its three helpers plus the `Plan()` call site are GONE.
      **The executor's key list is deliberately NARROWER than `HashKeys`.**
      `HashKeys` stays the plan's truth (what EXPLAIN renders); a pair may be
      folded into the KEY only where `datumKey` equality provably agrees with
      `=`, because goopg has one canonicalisation where PG has a hash opfamily
      per type. `pairIsHashSafe` requires both sides to resolve to the same
      non-array scalar type from a whitelist (machine ints are one family).
      The exclusions are wrong-results risks, not missed optimisations —
      **float4/float8 are stored as TEXT datums** (`floatTextDatum(PGFloatOut
      (...))`), so `-0.0` and `0.0` are `=`-equal but key differently; enum and
      toast-pointer datums have NO `datumKey` arm and would collide into one
      key, which with the conjunct also dropped from the residual would
      OVER-emit. A declined pair keeps today's behaviour exactly (out of the
      key, in the residual). Encoding: n int64s packed big-endian into a fixed
      8n-byte buffer (probe lookups as `m[string(buf)]` → zero allocations),
      else **length-prefixed** `datumKey` parts — the prefix, not this
      package's usual NUL separator, is what makes it injective, since a text
      `datumKey` can itself contain NUL. `demoteCompositeIntKeys` mirrors
      `demoteIntHash`. NULL is componentwise. `joinPredicateMatchSlot` now
      evaluates `execResidual`, discharging the FIRST half of ledger row
      `2026-08-03 M0127-P2.1` (the physical `Predicate` trim is the second
      half and stays open). NullAware `NOT IN` stays single-key — ledger.
      Gates: **DS05 MISMATCH=0 / CKMISMATCH=0 / ERROR=0 across all 99, and
      Q78 — the query the retired pass existed for — PASSes in 19 s / 45 rows
      / matching checksum** (`sweep-20260803-174443.txt` Q1-Q72 68 PASS +
      `sweep-20260803-182129.txt` Q73-Q99 26 PASS TIMEOUT=0; Q72 TIMEOUT at
      317 s is the known 300 s boundary pair). **PLAN: `make plan-diff
      LABEL=m0127-p21-hashkeys` MATCH on all 22** — retiring the reselect pass
      moved no key in TPC-H, so P2.1's baseline stands. UNITS PASS. SPOT PASS
      (Q12=2 / Q13=35, 16.5 s, peak 11,573 MB). SMOKE via the commit hook.
      The degeneracy witness asserts 64 buckets for 64 rows sharing a pinned
      lead key — a row-count test cannot see this defect, the results were
      always right. **Next is P2.3, merge-join multi-column keys.**
      Progress log: design doc §6.
- [x] **M0127-P2.3 — merge-join multi-column keys** from the same list
      (full-key comparator; residual non-equijoin only). IMPLEMENTATION-TODO
      P2.3; 07 §2. Bar: UNITS + SPOT + DS05 + PLAN.
      **DONE 2026-08-03.** `planner.Join.ExecMergeKeyPlan` +
      `internal/executor/join_merge_key.go`; `mergeKeyedRow.key Datum` →
      `keys []Datum` over one flat backing array. The planner half was
      already P2.1's (`fillOneJoinHashKeys` fills `JoinAlgoMerge`, EXPLAIN
      renders `Merge Cond:` over the list) — the executor was still reading
      `plan.LeftKey`/`RightKey`, so grouping on the lead column alone made a
      pinned lead ONE group whose cartesian product was walked pair by pair
      (M0125-0011 / Q97 is the recorded instance; its residual re-check kept
      the answer right at O(n·m)). Now the group IS the match set and
      `mergeResidual` is nil on an all-equijoin join = PG's empty `joinqual`.
      `pairIsHashSafe` governs the fold-in for merge too (same one-
      canonicalisation-vs-opfamily question); ledger row `2026-08-03
      M0127-P2.3` records that `compareDatum`'s `KindString` arm content-
      sniffs (pg_lsn / UUID / composite / array literal), which the dropped
      conjunct now widens from the lead key to every folded text pair.
      Gates: UNITS PASS; SPOT PASS (Q12=2 / Q13=35, 18.1 s); PLAN MATCH 22/22
      against `m0127-p21-hashkeys` (no re-baseline needed); DS05 MISMATCH=0 /
      CKMISMATCH=0 / ERROR=0 across all 99 (Q97 checksum unchanged; timings
      flat vs P2.2 — no DS05 query is currently governed by this class).
      **S2 IS CLOSED; the next M0127 selection is P3.1.**
- [x] **M0127-P3.1 — `chooseHashTableSize`** (shared pkg importable by planner
      and executor); goopg-width-aware (`48·c` + map overhead).
      IMPLEMENTATION-TODO P3.1; 06 §2.1; 04 §4. Bar: UNITS + SPOT.
      **DONE 2026-08-03.** New leaf package `internal/hashsize`:
      `Choose(ntuples, ncols, avgVarBytes, memLimit) Sizing{NBuckets, NBatch,
      SpaceAllowed, EntryBytes}` plus `EntryBytes` / `EffectiveMemLimit`. It is
      a PACKAGE, not a function, because the import direction forces it: the
      executor imports the planner and the planner imports the executor
      nowhere, so the one rule both must obey has to sit below both. PG gets
      this for free — `final_cost_hashjoin` and `ExecHashTableCreate` call the
      same `ExecChooseHashTableSize` (`nodeHash.c:658`) — and a cost model that
      believes a build fits while the executor spills is precisely the
      sibling-path class this project keeps paying for.
      **The width substitution is the content, not a detail.** PG measures an
      entry as `HJTUPLE_OVERHEAD + MAXALIGN(SizeofMinimalTupleHeader) +
      MAXALIGN(tupwidth)`; goopg holds `map[K][]Row` over `[]Datum` at 48
      bytes per Datum, so the same row costs `48·c + 24` and a bucket slot
      costs 48 where PG's is an 8-byte pointer. PG's constants would predict
      `nbatch` low by about the width ratio — the trap `04 §4` names — and
      `TestChooseGoopgWidthForcesBatchesPGWouldNotSee` pins one instance
      (300k × 10 cols: ~2.9 MB of MinimalTuple, ~150 MB of Datum). Three PG
      subtleties are carried deliberately: the 1024-bucket floor, the
      re-derivation of nbuckets from the FULL budget once multi-batch is
      forced (buckets sized for a memory-full table, not for ntuples), and the
      closing walk-back. One PG assertion became a clamp —
      `Assert(bucket_bytes <= hash_table_bytes/2)` holds for 8-byte pointers,
      but 48-byte slots make "buckets alone exhaust work_mem" reachable.
      **First consumer: `joinOp.presizeLazyHash`** (`operators_join_agg.go`) —
      the build table is allocated with its bucket count already chosen
      instead of a nil map doubling its way up a multi-million-row build. Rows
      come from `planner.EstimateRows(o.plan.Left/Right)` (the executor cannot
      count a side it has not drained); the presize is capped at
      `maxPresizeBuckets = 1<<20` because an estimate is not a measurement,
      and the 1024-bucket floor is read as "no information" (every unANALYZEd
      relation returns it) so tiny/unknown builds presize nothing. The
      FOR-UPDATE ctid build is left alone — it materialises first and its
      result sets are small by construction. `NBatch` is computed and IGNORED:
      honouring it means partitioning at insert time, which is P3.2.
      **Four ledger rows** (`2026-08-03 M0127-P3.1`): `hashJoinCost` still
      does not call `Choose` (the batch-I/O term moves plans; 06 §5 / P5.7 own
      it); `avgVarBytes` is 0 because goopg collects no per-column width
      statistic, biasing NBatch low; the walk-back's `FileBufferBytes = 8192`
      assumes a per-batch write buffer `spillWriter` does not have yet; and
      `maxPresizeBuckets` is a goopg-only cap that P3.2 should delete.
      Gates: UNITS PASS; SPOT PASS (Q12=2 / Q13=35, 17.0 s, peak 11,461 MB —
      vs 18.1 s / 11,573 MB at P2.3, within noise). No plan surface touched,
      so `m0127-p21-hashkeys` stays the PLAN baseline for S3.
      **Next M0127 selection is P3.2 (batch build/probe).**
      Progress log: design doc §6.
- [x] **M0127-P3.2 — batch build/probe.** Hashvalue-prefixed `spillWriter`
      frames, per-batch inner/outer files, `HJ_NEED_NEW_BATCH` state in
      `nextLazy`, nbatch growth with capped give-up + WARNING. Fold
      M0125-0032's Q21 plain-EXPLAIN classification into this design loop.
      IMPLEMENTATION-TODO P3.2; 06 §2.2-2.4. Bar: UNITS + DS05 + RACE.
      **DONE 2026-08-03.** New `internal/executor/join_batch.go`
      (`hashBatchState`) + a hashed frame on the shared spill primitive
      (`WriteRowHashed` / `ReadRowHashedInto`). `ctx.WorkMem` now BOUNDS a
      hash join instead of describing it: batch 0 stays in the map, later
      batches are inner/outer file pairs, `batchno = (hash >> log2(nbuckets))
      & (nbatch-1)`, and probe EOF becomes "this batch is done" via
      `nextBatch` (= `HJ_NEED_NEW_BATCH`). PG's skip rules 2 and 3 come with
      their counters (`origNBatch`, `nbatchOutstart`) — a one-sided batch is
      skippable UNLESS nbatch has grown since the file was written, or its
      rows are lost silently. Growth is `ExecHashIncreaseNumBatches` including
      the freeze (`nfreed == 0 || nfreed == ninmemory`), evicting per KEY
      rather than per tuple because every row under one map key shares that
      key's hash.
      **Two decisions carry the risk.** (1) The state is installed even when
      the geometry says NBatch == 1 — goopg's estimates are absent far more
      often than PG's, and an under-estimate is exactly the case that
      overruns memory, so the bound must come from growth, not the estimate
      (single-batch cost: one add + one compare per build row; the routing
      hash is skipped entirely while nbatch == 1). (2) Routing hashes the
      key's CANONICAL bytes (`appendCanonicalNumericKey`, split out of
      `canonicalNumericKey`), never "the int64 if it is one": the executor
      can fall from the int lane to the string lane mid-build
      (`demoteIntHash`), and routing by one canonical form while filing under
      another sends equal keys to different batches — a lost match, not an
      error. Scope: INNER + single-key + private build; LEFT/Semi/Anti and
      the composite lane are P3.4, and `prebuildSharedHashJoins` sets
      `noBatch`. `spillWriter` gained the `hashsize.FileBufferBytes` write
      buffer the P3.1 walk-back already priced.
      **Six ledger rows** (`2026-08-03 M0127-P3.2`): the four declined join
      shapes, the composite lane, the shared-build implicit decline, the
      un-retired `maxPresizeBuckets` (nbatch bounds resident ROWS, not the
      bucket ARRAY), no temp-file registry (P3.3), and no EXPLAIN counters
      (P3.5).
      Gates: UNITS PASS; RACE PASS (`make race-gate`, all packages); SPOT
      PASS (Q12=2 / Q13=35, 15.8 s, peak 10,224 MB vs 17.0 s / 11,461 MB at
      P3.1); DS05 across slices 1-50 / 51-72 / 73-99 MISMATCH=0 CKMISMATCH=0
      ERROR=0 (Q72's TIMEOUT is pre-existing — 300 s here vs 315 s / 317 s on
      earlier commits). No plan surface touched; `m0127-p21-hashkeys` stays
      the PLAN baseline.
      **Next M0127 selection is P3.3 (temp-file registry).**
      Progress log: design doc §6.
- [x] **M0127-P3.3 — temp-file registry.** Per-query registry on `Context`;
      relocate to `<datadir>/base/pgsql_tmp/`; startup sweep; fix `spillOp.Close`
      unlink leak. Injected-crash test leaves no strays. IMPLEMENTATION-TODO
      P3.3; 06 §3. Bar: UNITS + crash-injection test.
      **DONE 2026-08-03.** New leaf package `internal/pgtemp` (PG's
      `PG_TEMP_FILES_DIR`/`PG_TEMP_FILE_PREFIX`, `Dir`/`EnsureDir`/
      `FilePattern`/`RemoveStrayFiles`) + `internal/executor/tempfiles.go`
      (`tempFileRegistry` on `Context`). Spill files became the STATEMENT's
      property instead of each operator's good intentions: the registry is
      allocated by `NewContext`, shared BY POINTER with every parallel worker
      (`NewWorkerContext`) and with the `synthCtx := *ctx` copies, and
      `executeOneSimpleStmt` / the extended dispatch `defer
      ctx.ReleaseSpillFiles()`. Operators still unlink eagerly (sortOp chunks,
      `hashBatchState.discard`, and now `spillOp.Close` — the leak 06 §3 named)
      and deregister when they do, so a 1024-batch join does not hold 1024
      paths to statement end; what the registry adds is that ownership no
      longer DEPENDS on Close being reached.
      **The relocation is what makes the sweep possible.** Files moved from
      `os.TempDir()`/`goopg-spill-*.tmp` to
      `<datadir>/base/pgsql_tmp/pgsql_tmp<pid>.*`; the prefix is load-bearing,
      not cosmetic — PG's `RemovePgTempFilesInDir` (fd.c) filters on it so a
      sweep can never mistake a neighbouring file for a stray. `Server.Run`
      sweeps before `close(s.ready)`, on the reasoning that a live backend
      unlinks at statement end, so anything present at startup is by definition
      a crash leftover.
      **Sibling-path find:** the extended protocol never set `ectx.DataDir`
      (the simple path always did), so extended-protocol spills would have
      resolved outside the cluster even after the relocation.
      **Four ledger rows** (`2026-08-03 M0127-P3.3`): `temp_tablespaces`
      unimplemented (one directory, no per-tablespace sweep arm);
      `temp_file_limit`/`log_temp_files` accounting absent (the registry knows
      paths, never bytes); release point is the STATEMENT where PG's is the
      RESOURCE OWNER (safe today only because cursors materialise — P4.3's
      `Materialize` must revisit it); PG's `RemovePgTempRelationFiles` not
      ported (goopg temp-relation files carry no backend id to recognise).
      Also flips P3.2's "no per-query registry" row to `resolved`.
      Gates: UNITS PASS; RACE PASS; SPOT PASS. Six new tests, incl. the
      crash-injection gate `TestStartupSweepReclaimsCrashedQueryFiles` (four
      files survive a context that never releases; the sweep reclaims all four)
      and `TestWorkerContextSharesTheLeaderRegistry`.
      **Next M0127 selection is P3.4 (Semi/Anti/LEFT per-batch semantics).**
      Progress log: design doc §6.
- [x] **M0127-P3.4 — Semi/Anti/LEFT per-batch semantics** (batch-global
      `antiBuildHasNull`); shared build declines when nbatch > 1.
      IMPLEMENTATION-TODO P3.4; 06 §2.5. Bar: UNITS + DS05 + RACE.
      **DONE 2026-08-03.** `joinBatchEligible` admits SEMI, ANTI and the
      probe-filling LEFT on one sentence from 06 §2.5: **a probe row belongs to
      exactly one batch, and so does every build row that could match it** —
      equal keys hash equal, so they route together, and every per-probe-row
      verdict (emit-at-most-once, emit-iff-no-match, null-pad-on-miss) is
      decidable inside the row's own batch. That is why NOT ONE LINE of
      `nextLazy`'s emit logic changed. The admitted set is character-for-
      character `hashJoinIsPartialCapable`'s (planner/parallel.go): the same
      "per-probe-row verdict is worker-local" property decides which joins may
      take a partial probe side and which may be partitioned by batch.
      **What did have to change is the SKIP rule.** P3.2 dropped any batch
      empty on one side; under LEFT/ANTI that silently discards every probe row
      in an outer-only batch. `batchSkippable` now states PG's three rules as a
      table (nodeHashjoin.c:1141-1160) with rule 1's fill arm
      (`probeFillsUnmatched`). Rule 1's INNER-only arm is deliberately absent —
      it belongs to the RIGHT/FULL unmatched sweep goopg does not have (P4.2),
      which is also why a LEFT join built on the LEFT side is still declined.
      `antiBuildHasNull`/`antiBuildRows` needed no work: the build loop
      maintains them before any row is routed, so they are batch-global by
      construction and NOT IN's short-circuits fire before any probe.
      **The shared build now declines the SHARE, not the SPILL.** P3.2's
      `noBatch` made it the one hash build in the executor with no work_mem
      bound at all, because `captureSharedBuild` freezes the in-memory table
      alone — publishing a spilled build hands each worker one partition. The
      decline is taken twice: from the ESTIMATE before the build (common case
      wastes no pass) and from the MEASUREMENT after it (goopg's estimates are
      absent often enough that only growth bounds anything), with
      `buildGeometry` factored out of `presizeLazyHash` so the presize, the
      batch state and the decline cannot disagree about whether a build fits.
      **The cost is real and was isolated, not averaged away.** SPOT rows PASS
      (Q12=2 / Q13=35) but the query phase went 15.7 s → 28.3 s. A three-arm
      A/B on the same host splits it exactly: HEAD Q12 11.58 / Q13 4.14;
      shared-decline alone Q12 15.84 / Q13 3.89; full P3.4 Q12 16.39 / Q13
      11.89. So Q12's +4.3 s is N private builds replacing one shared one, and
      Q13's +7.8 s is its LEFT join (`customer ⟕ orders`, 1.5M-row build) now
      honouring `work_mem` — at goopg's 512 MB default, i.e. 128× PG's, so PG
      spills this join harder than goopg now does. Both are the design's
      mandate (06 §6 / 06 §2), the alternative being the unbounded builds this
      milestone exists to close, so they are LEDGERED for the S1/S5 acceptance
      measurement rather than tuned away here.
      **Three ledger rows** (`2026-08-03 M0127-P3.4`): the build-side fill arm
      (RIGHT/FULL/LEFT-on-BuildLeft → P4.2), the repeated private builds (only
      real parallel hash fixes it, 06 §6), and `hashJoinCost` still not pricing
      the nbatch I/O term (→ P5.7) now that more join types spill. Also flips
      two P3.2 rows to `resolved` (LEFT/Semi/Anti decline; shared-build
      `noBatch`).
      Gates: UNITS PASS; RACE PASS (`make race-gate`, all packages); DS05
      across slices 1-50 / 51-75 / 76-99 MISMATCH=0 CKMISMATCH=0 ERROR=0 over
      all 99 (Q72's TIMEOUT pre-existing); SPOT PASS on rows with the timing
      regression above. Three new tests, incl. the negative-controlled
      `TestFillingJoinsKeepOuterOnlyBatches` (fill arm disabled ⇒ LEFT emits 72
      of 1,239 rows). No plan surface touched; `m0127-p21-hashkeys` stays the
      PLAN baseline.
      **Next M0127 selection is P3.5 (EXPLAIN `Batches:` + forced-spill
      identity; S3 exit evidence).**
      Progress log: design doc §6.
- [x] **M0127-P3.5 — EXPLAIN `Batches:`/memory lines + forced-spill identity
      test** (low `work_mem` Q3 byte-identical to default). S3 exit evidence:
      Q21 SF1 completes capped; file `analysis/leftdeep-joins/…-s3-spill.txt`.
      IMPLEMENTATION-TODO P3.5; 06 §4; 09 §2. Bar: Q21 SF1 (capped) + DS05
      zero-delta + RACE.
      **DONE 2026-08-03. S3 (P3.1–P3.5) CLOSED.** `HashJoinStats` map on
      `Context` keyed by plan node (the `SubPlanStats`/`MemoizeStats` shape);
      `hashBatchState.publish()` max-merges into it when the geometry is
      chosen, when nbatch doubles and at close — peak memory flushed at close
      rather than maintained in `insertBuildRow`'s per-row path.
      `formatHashJoinInfoLine` is `show_hash_info` (explain.c) VERBATIM,
      including that PG prints BOTH originals once EITHER count moved, and
      `BYTES_TO_KILOBYTES`'s round-up. The line hangs off the **Hash Join**,
      not off a Hash node, because goopg has none — the build lives inside
      `joinOp`.
      **S3 exit: both clauses MET**
      (`analysis/leftdeep-joins/2026-08-03-s3-spill.txt`) — Q21 at SF1 rc=0 in
      132 s / 405 rows / peak VmHWM 16.7 GB inside the 20G/24G cap; Q3 at
      `work_mem=512kB` (nbatch 512, grown from 256) **byte-identical** to Q3 at
      6 GB (nbatch 1), 11,521 rows, sha256 `066af3df…dc16dc8`.
      **Finding for P5.7:** goopg's 512 MB default is NOT a no-spill setting —
      Q3's lineitem build still reports `Batches: 8 (originally 4)  Memory
      Usage: 475137kB` there and needs 6 GB to reach one batch, because a build
      row is `[]Datum` (48 B/column) where PG's is a packed MinimalTuple. At
      SF1 with default settings a TPC-H hash join SPILLS, so an nbatch-blind
      `hashJoinCost` will keep choosing hash.
      Five new tests (`internal/executor/join_batch_explain_test.go`), three of
      them driving the property through SQL — that `SET work_mem` reaches the
      batch state at all is not something P3.2's operator-level fixtures could
      assert. **Three ledger rows** (`2026-08-03 M0127-P3.5`): non-TEXT EXPLAIN
      formats emit nothing; a parallel worker's counters die with the worker
      (PG merges via `SharedHashInfo`); and the two shape divergences (no Hash
      node; batch-ineligible joins print no line at all).
      Gates: UNITS PASS; RACE PASS (all packages); DS05 slices 1-50 / 51-99
      MISMATCH=0 CKMISMATCH=0 ERROR=0 over all 99 (Q72 TIMEOUT pre-existing).
- [x] **M0127-P4.1 — streaming merge join** (duplicate-group buffering +
      overflow file); delete full-drain `runMergeJoin`/`buildMergeSide`
      accumulation. IMPLEMENTATION-TODO P4.1; 07 §2. Bar: UNITS + REGRESS +
      DS05.
      **DONE 2026-08-04. S4 is OPEN; P4.2 is next.** The merge join held
      THREE full copies of its working set — both children drained into
      `[]Row` by `Open`, both keyed sides `sort.SliceStable`-d into a second
      pair of arrays by `buildMergeSide`, and the ENTIRE output appended into
      `o.rows` before `Next` returned its first tuple — for an operator whose
      upstream analogue holds one tuple per side plus the current inner
      group. `internal/executor/join_merge_stream.go` replaces all three.
      Each side is a **`mergeSortedSource`**: a key-ordered stream whose
      resident set is bounded by `work_mem`, chunks past the budget sorted
      and written to a spill run and freed, the runs N-way merged on the way
      out — `sortOp`'s M0068-0006 shape, keyed on the merge key TUPLE instead
      of a SortKey list, because the key expressions live in the merged
      left++right space and only a `mergedKeySlot` (the P0.1 hoisted cache)
      can present a bare child row in it. That also retires
      `buildMergeSide`'s `concatRows`-per-row padding. The join is
      **`mergeJoinStream`**, PG's `EXEC_MJ_SKIP` advance with the INNER
      equal-key group as the only buffer: resident while it fits `work_mem`,
      overflowing to a spill file replayed once per outer row (and once more
      for the RIGHT/FULL sweep, over a `groupMatched` bitmap sized by row
      COUNT so it stays small even when the rows are on disk).
      **Emission order is byte-identical to the array implementation by
      construction** — that is why a NULL key is carried as a per-row flag
      that sorts AFTER every real key rather than being filed in a side list:
      a stream cannot come back for a side list, but it can be told the null
      rows are last, and a four-state tail then walks
      left-real → right-real → left-null → right-null holding one row per
      side. The guard that matters is the **forced-spill identity test**
      (P3.5's hash-side property applied to merge): `work_mem=1` — every
      input row in its own run, the 4-row inner group overflowed after its
      first — against unbounded, over all four join types × both residual
      regimes, compared row-by-row. Disabling the group replay's rewind makes
      it fail. Six tests total, incl. a 40,000-row join that asserts `o.rows`
      stays empty for the whole drain and a Close-releases-every-spill-file
      test. **Four ledger rows** (`2026-08-04 M0127-P4.1`): the operator
      still SORTS its own inputs (07 §2's pathkey-fed premise is P5's, not
      this slice's); the emit is still `concatRows`, not the E1 composed-slot
      seam; goopg has no operator-level mark/restore, so a group is buffered
      where PG rewinds (schedule with P4.3, which brings `Materialize`); and
      the merge path's dead ctid side-channel is now structurally absent
      rather than one missing assignment.
      Gates: UNITS PASS; DS05 PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 over
      all 99 (Q72 TIMEOUT pre-existing); REGRESS zero delta vs a HEAD
      worktree baseline on `join`/`join_hash`/`select`/`subselect`/`union`
      (diff bodies byte-identical after path normalisation); SPOT PASS
      (Q12=2 / Q13=35, 28.0 s). Progress log: design doc §6.
- [x] **M0127-P4.2 — hash outer-fill.** Matched bitmap per batch; RIGHT sweep;
      FULL = LEFT fill + sweep; planner legality matrix update (RIGHT/FULL hash
      paths). Regress-port outer-join files green. IMPLEMENTATION-TODO P4.2;
      07 §3 (PG `HJ_FILL_INNER`). Bar: REGRESS outer-join files + DS05.
      **DONE 2026-08-04. P4.3 is next.** The hash join could not null-pad a
      side that matched nothing, and that gap — not any semantics — is why
      RIGHT/FULL were pinned onto merge and inherited its sort of BOTH inputs.
      `internal/executor/join_outer_fill.go` adds PG's `HJ_FILL_INNER` half
      next to the `HJ_FILL_OUTER` goopg already had. Both are named for their
      ROLE (`fillProbeSide`/`fillBuildSide`) and derived from `Type` + the
      EFFECTIVE build side — Semi/Anti are forced build-right inside
      `buildLazyHashTable`, so the raw `BuildLeft` lies — which is what lets
      **RIGHT reuse the LEFT fill path unchanged** (build the non-preserved
      left, probe the preserved right, no sweep); FULL is both halves and pays
      for the sweep. Three properties each got their own test: the bitmap is
      written AFTER the residual predicate (an early mark silently drops
      residual-rejected build rows from the sweep); the sweep runs PER BATCH in
      `nextLazy`'s probe-EOF arm while that table is still resident, so
      `joinBatchEligible` accepts every outer orientation and `batchSkippable`
      grows rule 1's INNER arm; and a NULL-keyed build row — dropped by every
      build loop since a NULL key has no canonical bucket form — is retained
      and swept, where PG gets there via `hashtable->keepNulls`.
      **Planner: the merge PIN became a merge DEFAULT, gated OFF.**
      `chooseOuterFillJoinAlgo` prices hash against merge (never nested loop —
      legal but it drains both inputs) behind `GOOPG_HASH_OUTER_JOIN=1`. The
      gate is a MEASUREMENT, not caution: an unconditional flip keeps every row
      but reorders unordered ones and moves regress `join` **210 diff lines
      further** from upstream, because `costInnerMerge` charges an 11-row sort
      like a real one while PG 18.3 — asked directly on J1_TBL/J2_TBL — picks
      Merge Right/Full Join there. The default flip belongs to P5 with doc 04's
      cost currency. **Three ledger rows** (`2026-08-04 M0127-P4.2`):
      NULL-key build rows are held unbounded instead of spilling like PG's
      keepNulls tuples; the planner default is gated; and parallel hash join
      still refuses RIGHT/FULL for want of a shared matched bitmap
      (PG 16+ `ExecParallelScanHashTableForUnmatched`).
      Gates: UNITS PASS; REGRESS **zero delta** vs a HEAD-worktree baseline on
      `join`/`join_hash`/`select`/`subselect`/`union`; DS05 run with the gate
      ON so the new path is exercised. Progress log: design doc §6.
- [x] **M0127-P4.3 — `Materialize` operator** (plan node + path + rescan replay,
      memory→spill); NL joins stream outer, inner under Materialize; delete
      drain-both `runNestedLoop` buffering and `concatRows`-per-pair.
      IMPLEMENTATION-TODO P4.3; 07 §4. Bar: UNITS + SPOT + DS05.
      **DONE 2026-08-04.** The nested loop is the executor's *universal
      fallback* — every join that is neither hash nor merge lands on it — and
      it ran entirely inside `Open`: both children drained to `[]Row`, all
      N×M candidate pairs built with `concatRows`, every survivor pushed into
      `o.rows` before `Next` returned its first tuple. Three copies of the
      join's data for an operator `nodeNestloop.c` runs with one tuple per
      side. The missing piece was never the loop but the absence of a
      **rescannable** subtree: an `Operator` can be walked once, and the inner
      side must be walked once per outer tuple. `operators_material.go` adds
      it — `Materialize`, PG's `nodeMaterial.c` analogue: `materialBuffer` is
      the `work_mem`-resident prefix plus one sequentially-replayed overflow
      file, `materializeOp.Rescan` replays the cache without touching the
      child. `join_nl_stream.go` is then PG's shape directly (outer streams,
      inner under the Materialize, emit as `Next` asks) and `runNestedLoop`
      is deleted. Two properties are load-bearing, not incidental: the cache
      fills **lazily** and keeps PG's `eof_underlying` resume, because a
      keyless Semi/Anti breaks out of its inner scan on the first qualifying
      tuple and an eagerly-draining Materialize would have silently truncated
      the inner side for every later outer tuple; and the unmatched-inner
      bitmap is indexed by **ordinal** (replay order is insertion order), so
      the RIGHT/FULL sweep needs no key and no map — and that sweep drains the
      cache itself, which is the path a RIGHT join over an *empty* outer side
      takes. `concatRows`-per-pair dies with it: the predicate is evaluated
      against one reusable merged buffer, so allocation now tracks OUTPUT, not
      N×M. The spill reader is opened when the writer is created, on an empty
      file, because P3.3's ledger row named the hazard (registry releases at
      the statement, PG at the resource owner) and an fd survives an unlink
      where a path does not. **Deferred, 3 ledger rows:** the
      `planner.Materialize` plan node + path + EXPLAIN line (PG places
      `Material` by `cost_rescan`, which needs doc 04's currency → P5.4, so
      the operator is executor-constructed meanwhile); `mergeJoinStream`'s
      hand-rolled twin of `materialBuffer` (P4.1's ledger row #3, still open);
      and `costInnerNestLoop` having no `cost_rescan` term, so a plan whose
      inner spills is priced as if the replay were free (→ P5.7).
      **The third deferral is a MEASURED decline, not caution.** The inner
      cache's `work_mem` bound is implemented and unit-tested but ships OFF
      (`GOOPG_NL_MATERIALIZE_WORK_MEM=1`), because the first full DS05 sweep
      found exactly one regression and it was that bound: **Q54** plans a
      nested loop whose inner is a 1.44M-row `store_sales` seq scan (~1.6 GB
      as `[]Datum`), so the bounded cache spills and every outer tuple replays
      the whole file with full datum decoding — 144 s → TIMEOUT. Unbounded,
      Q54 runs in **95 s with a matching checksum**, i.e. *faster* than the
      144 s the drain-both implementation took: the streaming outer and the
      reused slot/pair buffers are a net win once the spill is out of the way.
      PG never meets that wall because `cost_rescan` prices a materialized
      inner at `cpu_operator_cost` per tuple **plus `seq_page_cost` over the
      spilled pages**, which is what makes `final_cost_nestloop` lose to hash
      or merge there — so the bound is a plan-quality cliff until the planner
      can see it, the same trade P4.2's `GOOPG_HASH_OUTER_JOIN` gate records.
      Gates: UNITS PASS; SPOT PASS (Q12=2 / 15.2 s, Q13=35 / 11.5 s, query
      phase 27.9 s, peak 10,840 MB); DS05
      `analysis/leftdeep-joins/2026-08-04-p43-ds05-sweep.txt`. Six new tests
      separate the two independently-falsifiable claims (replay-without-
      re-execution vs. lazy-outer/once-read-inner); the join's OUTPUT is
      already covered from the other direction, since `join_outer_fill_test.go`
      uses the nested loop as the ORACLE for the hash path.
      **Next M0127 selection is P4.4 (lateral: outer streams, output no longer
      accumulates into `o.rows` — the last `o.rows` user in `joinOp`).**
      Progress log: design doc §6.
- [x] **M0127-P4.4 — lateral: outer streams** (per-outer re-execution stays),
      output no longer accumulates into `o.rows`. IMPLEMENTATION-TODO P4.4;
      07 §4. Bar: UNITS + DS05. **DONE 2026-08-04.**
      `internal/executor/join_lateral_stream.go`: `lateralJoinStream`, a
      two-phase machine in `nlJoinStream`'s shape — pull one outer tuple,
      re-open the right subtree under it, walk that subtree one tuple at a
      time, emit as `Next` asks, predicate evaluated against a reusable
      outer++inner buffer. The LEFT null-pad keys on `matched` alone (the
      eager `len(rightRows) == 0 || !matched` was the same predicate spelled
      with a drained array). What streaming FORCED: correlation-context
      hygiene — a streaming inner side yields to the PARENT between tuples, so
      the `ctx.OuterRows` push (and the per-outer-tuple `CTERowCache`) is
      installed and removed around each individual right-side call rather than
      held for a whole iteration, or a parent's own `OuterColumnRef` would
      resolve against this join's outer tuple. **With LATERAL — the last
      writer — converted, `joinOp.rows`/`idx` and the writer-less
      `leftCTIDs`/`rowSourceLeft` ctid side-channel are DELETED, and `Next`'s
      array tail with them: every arm streams.** All four P4 tasks have now
      landed, but **S4's stage exit is NOT met**: §3 requires the regress-port
      outer-join files green, and those stay pinned to merge until P4.2's
      `GOOPG_HASH_OUTER_JOIN` default flip, which needs doc 04's cost
      currency and is P5's. Gates: UNITS PASS;
      SPOT PASS (Q12=2 / 15.8 s, Q13=35 / 11.4 s, query phase 28.7 s, peak
      11,662 MB); DS05 PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 (Q72,
      pre-existing) SKIP=4, **all 94 passing queries byte-identical to the
      P4.3 sweep in row count AND checksum**, no query slower by >20%, total
      2310 s → 2332 s
      (`analysis/leftdeep-joins/2026-08-04-p44-ds05-sweep.txt`). **Two ledger
      rows** (`2026-08-04 M0127-P4.4`): PG rescans a LATERAL RHS with changed
      `PARAM_EXEC` values instead of re-executing it (goopg has no parameter
      machinery, so `Materialize`/Memoize still cannot sit under a LATERAL
      RHS — P5.4); and a LATERAL join drops the outer relation's ctid, so
      `FOR UPDATE OF <outer>` above one cannot stamp a tuple lock
      (pre-existing, preserved deliberately).
- [x] **M0127-P5.1 — `joinrels` level lists + relset map over `RelOptInfo`;**
      `buildInitialRels` incl. `PathPrebuilt` leaves for subquery/CTE/VALUES/
      pinned-unnest rels (closes the leaf-whitelist gap — also closes
      M0125-0037 stage (ii)). IMPLEMENTATION-TODO P5.1; 03 §1-§2. Bar: UNITS +
      PLAN (default arm ZERO diffs — inert flag-off).
      **↳ DONE 2026-08-04.** New `internal/planner/joinsearch.go`. `searchCtx`
      carries PG's two indexes over one set of rels — `joinrels
      [][]*RelOptInfo` (`root->join_rel_level`, allpaths.c:3475-3496) and
      `relMap map[RelSet]*RelOptInfo` (`join_rel_hash`). They are two VIEWS of
      one thing, so `addRel` **derives** the level from
      `bits.OnesCount16(relids)` rather than taking it as an argument and
      rejects a duplicate relset: two `RelOptInfo`s over one relset would split
      the pathlist `addPath` prunes within, and a rel filed at the wrong level
      would let phase 2 pair it with itself. `finalRel` states PG's
      one-rel-at-the-top contract (allpaths.c:3508-3512) as a returned error,
      not an assert — P5.3's answer to a failed search is the syntactic shape.
      **`buildInitialRels` is where the leaf-whitelist gap closes**: it takes
      the same three positionally-aligned per-FROM-item slices `tryBushyDP`
      assembles (bushy.go:184-196) and admits EVERY item, where the old DP
      abandons reordering for the whole statement on one VALUES/CTE/subquery
      leaf (bushy.go:116-123). Rows are `filteredRows` for a base-table leaf
      (post-local-filter, 03 §2) and `EstimateRows(leaf)` for every other
      class — a subquery binding's `catalog.Table` is synthetic and its count
      means nothing (the guard pins a VALUES rel at 3 and a CTE rel at its
      body's 4242; both read 1 if `filteredRows` is believed) — floored at 1,
      since a 0-row initial rel makes every join above it free. Each rel gets
      one `PathPrebuilt` over the already-chosen leaf (carried whole so P5.5's
      createPlan re-emits an `*IndexScan` leaf as an index scan rather than
      demoting it) whose COST is re-derived via `costSeqscan`/`estScanPages` in
      the search's own currency, because two cost models must not meet inside
      one `addPath` comparison. **Inert twice over**: no `planSelect`
      reference, and `GOOPG_PGSHAPED_DP` defaults OFF (pinned by a test).
      Gates: UNITS PASS; **PLAN 22/22 MATCH** vs `m0127-p21-hashkeys`
      (structural, live 65433). 7 tests in `joinsearch_test.go` separating the
      two-indexes-agree invariant from the leaf admission; mutation-checked
      (dropping the duplicate check / the non-table row rule each fail their
      own guard). **One ledger row** (`2026-08-04 M0127-P5.1`): a base rel
      contributes no per-index scan paths and no `PATH_PARAM_BY_REL`
      parameterised paths — PG's `create_index_paths` — so the search cannot
      trade access methods as the order changes; blocked on there being no
      `cost_index` in the search's currency, resume at P5.4.
      **Next M0127 selection is P5.2** (restrictInfo list +
      `hasRelevantJoinClause`; equivalence-class selectivity rule).
      Progress log: design doc §6.
- [x] **M0127-P5.2 — restrictInfo list + `hasRelevantJoinClause`;**
      equivalence-class selectivity rule (inferred edges admissible, no
      double-count). IMPLEMENTATION-TODO P5.2; 03 §3; 04 §5. Bar: UNITS + PLAN
      (ZERO diffs).
      **↳ DONE 2026-08-04.** New `internal/planner/joinrestrict.go`.
      `restrictInfo` generalises `joinEdge` (bushy.go:40) from a PAIR of FROM
      positions to a `relids RelSet` (PG's `required_relids`): the edge list is
      structurally blind to a qual spanning three relations, a relset is not,
      so `a.x = b.y + c.z` is one clause with three bits instead of something
      silently dropped. Non-equality join quals are kept as well — P5.4 has to
      PLACE them and can only place clauses the search knows about — and the
      key split is stored as `leftRelids`/`rightRelids`, which is what lets
      that same three-rel equality be a legal hash/merge clause keying {a}
      against {b,c}. Single-rel quals are deliberately excluded: P5.1 already
      folded their selectivity into the initial rel's row count.
      **The finding: 03 §3 defined `hasRelevantJoinClause` as "intersects both
      sides AND is covered by their union", and the oracle does not.**
      `have_relevant_joinclause` (joininfo.c:39) is two `bms_overlap` tests
      with NO coverage requirement; the coverage test is a DIFFERENT function,
      `build_joinrel_restrictlist` (relnode.c), because a qual is applied at
      the lowest level that can EVALUATE it. Under the coverage reading a pair
      connected only by a three-rel qual is not "relevant", so phase 1 refuses
      to form it and it reaches the search only via cartesian/last-ditch — a
      different enumeration from PG's. Now two predicates
      (`hasRelevantJoinClause` / `clausesFor`); 03 §3 corrected against the
      oracle. `selectivityClauses` is 04 §5:
      `generate_join_implied_equalities_normal` emits exactly ONE clause per EC
      per (outer, inner) split, so an EC of n members yields n−1 clauses over a
      tree, never C(n,2) — with `a=b`, `b=c`, inferred `a=c`, two clauses cross
      the {a,b}⋈{c} boundary and charging both squares one restriction. That
      double-count is what the ×2.0 `inferredEdgePenalty` (bushy.go:67) was
      compensating for in the COST dimension rather than the cardinality one;
      inferred clauses carry no penalty here and `inferred` survives only as
      the tie-break for which member carries the selectivity. Class ids are
      dense in `compareColumnIdent` order, not map order — the id picks that
      member, so a map-ordered id would move plans between identical runs.
      Gates: UNITS PASS; **PLAN 22/22 MATCH** vs `m0127-p21-hashkeys`
      (structural, live 65433). 8 tests in `joinrestrict_test.go` (incl. a
      20-run id-determinism guard and a refuse-don't-guess guard on foreign
      coordinates); mutation-checked — adding coverage to
      `hasRelevantJoinClause` and removing the EC dedup each fail their own
      guard. **One ledger row** (`2026-08-04 M0127-P5.2`): goopg CLASSIFIES the
      conjuncts it is handed and never SYNTHESISES a clause from a class the
      way `create_join_clause` does, so an EC edge `inferAnchoredEqualities`
      declined to emit stays invisible to the search; resume at P5.4/P5.6.
      Inert as P5.1 was (no `planSelect` reference, `GOOPG_PGSHAPED_DP` OFF).
      **Next M0127 selection is P5.3** (`joinSearchOneLevel` phases 1+3 +
      `makeJoinRel`); its `hasNoJoinClauseAtAll` gate landed here with the
      list. Progress log: design doc §6.
- [x] **M0127-P5.3 — `joinSearchOneLevel` phases 1+3** (clause joins against
      initial rels; disconnected cartesian; last-ditch); `makeJoinRel` with
      PG's outer/inner printing convention (03 §4.4). IMPLEMENTATION-TODO P5.3;
      03 §4.1-§4.2 (`joinrels.c:118`, `:200-256`). Bar: UNITS + SPOT + PLAN.
      **↳ DONE 2026-08-04** — `internal/planner/joinsearchlevel.go` (+ 9 tests).
      The finding: PG's clause/clauseless branch is per OLD REL
      (`joinrels.c:96`), not per pair, and that placement is what confines the
      level-2 `first_rel` offset (`:112-116`) to the clause branch — the
      clauseless branch deliberately re-pairs both directions (PG's own note,
      `:127-136`). 03 §4.1's pseudocode moves the branch inside the inner loop,
      which enumerates identically EXCEPT for that offset; the code follows the
      oracle and records the equivalence inline. `makeJoinRel` = find-or-create
      over P5.1's relset map + `populate_joinrel_with_paths`' JOIN_INNER arm
      (`:809-816`): one rel per relset, sized once, both outer/inner orders
      offered as paths — 03 §4.4's printing convention enforced structurally.
      P5.6 sizing and P5.4 path generation stay behind a `joinRelBuilder` seam
      so the task is verified on the pair SEQUENCE alone. Phase 2's insertion
      point is marked between phases 1 and 3 (phase 3's emptiness test must see
      the bushy pairs) = P5.3a. **One ledger row** (`2026-08-04 M0127-P5.3`):
      goopg has no dummy-rel concept, so PG's `is_dummy_rel` /
      `restriction_is_constant_false` short circuit is absent; resume at P5.4.
      Still inert — no `planSelect` call site, `GOOPG_PGSHAPED_DP` OFF.
      Gates: UNITS PASS; PLAN 22/22 MATCH vs `m0127-p21-hashkeys`; SPOT PASS
      (Q12=2, Q13=35).
- [x] **M0127-P5.3a — phase 2, bushy joins, PG-verbatim** (03 §4.3,
      `joinrels.c:141-198`): k-loop to the halfway point, clauseless rel skip
      (:170-172), mirror-half `first_rel` rule (:174-177),
      `have_relevant_joinclause` pair gate (:190-191). Pair-count verification
      against 03 §7's arithmetic (connectivity-filtered).
      IMPLEMENTATION-TODO P5.3a. Bar: UNITS + pair-count verification test.
      **↳ DONE 2026-08-04** — ~40 lines in `internal/planner/joinsearchlevel.go`
      at the point P5.3 marked. PG's inner block (:182-194) is
      `make_rels_by_clause_joins` verbatim, so the phase reuses P5.3's helper
      and adds only the halfway break, the mirror-image offset and the
      clauseless skip. Phase 2 has NO clauseless else-branch (unlike phase 1) —
      a bushy pair needs a connecting clause, PG's planning-time defence
      (:144-146) and what makes 03 §7's no-GEQO policy tenable. With phases 1+2
      the enumeration is complete in PG's sense, and that is now verified
      ARITHMETICALLY: on a complete clause graph the search must make exactly
      (3ⁿ − 2ⁿ⁺¹ + 1)/2 `makeJoinRel` calls (03 §7's closed form), tested for
      n=2..7 — the check a fixed chain sequence cannot make, because
      connectivity masks most of the space. The finding: the clauseless-rel
      skip is UNOBSERVABLE in v1 (a rel with no join clause cannot satisfy the
      clause-only pair gate for any partner), mutation-confirmed; kept verbatim
      with the redundancy recorded at the site because `has_join_restriction`
      makes it live at P5.8. **One ledger row** (`2026-08-04 M0127-P5.3a`):
      landing the full bushy space is what makes the absence of GEQO real —
      PG switches at 12 rels, goopg searches to its 16-rel `RelSet` ceiling.
      P5.3's `…IsLeftDeepOnly` guard is FLIPPED to
      `TestJoinSearchFourRelChainOffersBushyPair`. Still inert — no `planSelect`
      call site, `GOOPG_PGSHAPED_DP` OFF. Gates: UNITS PASS (PLAN/SPOT not
      re-run: no call site + flag OFF ⇒ no plan and no row can move).
- [x] **M0127-P5.4a — `addPathsToJoinrel`, the unparameterised core.**
      **DONE 2026-08-04 (loop #64)**, `internal/planner/joinpaths.go` (+
      `pathgen.go` / `cost_funcs.go` / `path.go`). PG's per-PAIR
      `clause_sides_match_join` key/residual split (joinpath.c:2205), hash
      paths keyed on the full usable equality set (05 §5), an unconditional
      plain nested loop, and qual placement carried on the `Path`
      (`HashKeys`/`Residual`) so it is costed. The split is per pair and not
      per clause: `a.x = b.y + c.z` keys {a} against {b,c}, so it is a hash
      key at ({a},{b,c}) and an ordinary qual at ({a,b},{c}) — the same
      clause, both placements correct, both reachable in one search. "Lowest
      covering level" was already `clausesFor`'s coverage rule and is now an
      invariant, not an example: over every spanning shape of a 3-relation
      triangle each clause is applied exactly once. The nested loop is
      unconditional because phase 1's clauseless branch and phase 3 offer
      pairs with an EMPTY clause list and `joinSearch` treats an empty
      pathlist as a hard failure. Deterministic tie-break rides M0125-0047's
      rule via `addPath`: a self-join's two aliases are identical by
      construction, so both hash orientations tie and the incumbent (the
      first-offered order) wins. `generateHashJoinPaths` was refactored to a
      single-orientation primitive so ONE hash-path generator exists —
      `makeJoinRel` already calls per direction. Nothing calls it from
      `planSelect` (`sizeJoinRel` is P5.6's); `GOOPG_PGSHAPED_DP` OFF. 4
      ledger rows. Bar met: UNITS (SPOT/DS05 not applicable — no `planSelect`
      call site, flag OFF, so no plan and no row can move).
- [x] **M0127-P5.4b-i — the 03 §9 parameterisation discipline.**
      **DONE 2026-08-04 (loop #65)**, `internal/planner/pathparam.go` (+
      `path.go` / `pathgen.go` / `joinpaths.go`). Landed AHEAD of the paths it
      governs, which was the forced ordering the P5.4a ledger row named: a
      parameterisation-blind consumer meeting its first `RequiredOuter` path
      produces an unbuildable plan, not a slow one. Rule 1 — `setCheapest` is
      `set_cheapest` (pathnode.c:272) in full: unparameterised-only cheapest
      slots, `CheapestParameterized` with the cheapest unparameterised path
      prepended, best-parameterised fallback filling the total slot but never
      the startup one, plus the two arms a reimplementation would plausibly
      get wrong — subset comparison runs BEFORE cost, and incomparable
      parameterisations keep the incumbent rather than picking the cheaper.
      Rule 2 — `pathParamByRel` (PATH_PARAM_BY_REL, joinpath.c:46) refuses
      both directions in `addPathsToJoinrel`, for different reasons: an outer
      parameterised by the inner is impossible in any join order; an inner
      parameterised by the outer belongs to the NLI arm (P5.4b-ii). Rule 3 —
      PG's `ppi_rows` needs no new field because PG carries it in
      `path->rows`, so the rule is a discipline on the COST primitives, which
      now read the child PATH's `Rows` and never `child.Rel.Rows`. **The
      finding is a fourth rule 03 §9 does not enumerate:** a join path
      computes its own `RequiredOuter` from its children's, and for a nested
      loop that is a SUBTRACTION (`calc_nestloop_required_outer`,
      pathnode.c:2592) — a nested loop DISCHARGES an inner parameterised by
      the outer, so an NLI subtree over unparameterised inputs is itself
      unparameterised, which is exactly what lets it be a hash-join input
      instead of being refused by rule 2. `generateNLIPath` had declared
      `RequiredOuter: inner.Relids`, reading the field as "what I depend on
      below" when it means "what I still need from above", and naming a
      relation the joinrel contains. Still inert — no `planSelect` call site,
      `GOOPG_PGSHAPED_DP` OFF. 1 ledger row. Bar met: UNITS (SPOT/DS05 not
      applicable — no call site, flag OFF, so no plan and no row can move).
- [x] **M0127-P5.4b-ii-a — parameterised BASE INDEX paths.**
      **DONE 2026-08-04 (loop #66)**, `internal/planner/pathparamindex.go`
      (+ `joinsearch.go`). P5.4b-ii's own named first sub-step, split out and
      landed alone for the reason P5.4b itself was split: the NLI arm iterates
      `inner.CheapestParameterized`, which is empty in every query until some
      path carries a `RequiredOuter`, so path and consumer are separately
      falsifiable. Lands `create_index_paths`' join arm (indxpath.c:446) per
      base rel — the equijoin clauses whose inner operand is a bare column of
      THIS rel, one candidate parameterisation per distinct outer relset, the
      longest B-tree index those clauses fully cover, one `PathIndexScan` with
      a `RequiredOuter`. **The finding: `ppi_rows` does not need P5.6's
      `eqjoinsel`, which had looked like a blocking dependency.**
      `get_parameterized_baserel_size` (costsize.c:5379) passes
      `varRelid = rel->relid` to `clauselist_selectivity`, which forces every
      clause to be estimated as a RESTRICTION on this rel — so the answer is
      `var_eq_non_const` (selfuncs.c): non-null fraction over the rel's own
      ndistinct, clamped to MCV[0]'s frequency because a uniformly-drawn probe
      value cannot be commoner than the commonest one. No both-sides join
      estimator is consulted anywhere. PG's `rel->tuples * sel(param ∪
      baserestrict)` is algebraically goopg's `rel.Rows * sel(param)` since
      `rel.Rows` already carries the baserestrict selectivity — and that form
      cannot double-count the local quals. A fully-bound unique index
      short-circuits to one row (PG's `vardata->isunique`). Cost is built FROM
      `indexProbeCost` rather than beside it, so the
      `indexProbeCostMultiplier` calibration is not duplicated (04 §1's
      one-currency rule). It is a THIRD step between `buildInitialRels` and
      `joinSearch` because it reads the clause list, mirroring PG, where
      `set_base_rel_pathlists` runs only after `deconstruct_jointree`. Index
      eligibility calls `pickIndexCoveringAllLeadingColumns` — the NLI
      constructor's own function, the first half of 03 §5.2's binding
      contract. Still inert: no `planSelect` call site, `GOOPG_PGSHAPED_DP`
      OFF. 2 ledger rows. Bar met: UNITS (SPOT/DS05 not applicable — no call
      site, flag OFF, so no plan and no row can move).
- [x] **M0127-P5.4b-ii-b-1 — parameterised JOIN paths: the NLI arm.**
      **DONE 2026-08-04 (loop #68)**, `internal/planner/joinpathsnli.go`
      (+ `joinpaths.go`, `pathparamindex.go`, `pathgen.go`).
      `match_unsorted_outer`'s loop over
      `innerrel->cheapest_parameterized_paths` (joinpath.c:1949-1975),
      `try_nestloop_path`'s admission test (:882-889) with
      `allow_star_schema_join` (:363), and `create_nestloop_path`'s
      restrict-clause drop (pathnode.c:2478-2500). **It closes the hole
      P5.4b-i knowingly opened**: a pair whose inner cheapest-total is
      parameterised by the outer had NO path at all, and now has exactly the
      one PG recovers here — hash cannot bind the parameter and a plain nested
      loop would price the rescan as if it were free, which is why PG drops
      the pair at :1874 specifically to re-cost it from the inner PATH's own
      cost. `addPathsToJoinrel`'s two PATH_PARAM_BY_REL refusals were split to
      match PG's control flow: they have different scopes and folding them
      into one condition was what hid the arm-shaped hole. Also landed, from
      the ii-a ledger row: PG's **pairwise-union parameterisations**
      (`consider_index_join_outer_rels`, indxpath.c:531-583 — snapshot rule,
      subset skip, equivalence-class skip, `10 * considered_clauses` valve),
      which is the only way a COMPOSITE index equated to two DIFFERENT outer
      rels is ever fully bound. **The C1-era `generateNLIPath` is RETIRED** —
      it charged a flat `indexProbeCost` per outer row regardless of which
      inner path was rescanned, and two NLI path constructors is exactly the
      drift 03 §5.2's one-constructor rule forbids. **The finding: admitting
      only fully-discharged parameterisations buys an invariant the rest of
      the search silently leans on** — every JOIN path in the search is
      unparameterised, so `Path.Rows == Rel.Rows` for every join path and the
      only parameterised paths in play are base index scans, which is what
      lets `addNestLoopPath`/`addHashJoinPath` set `Rows: joinRel.Rows`
      without a `ppi_rows` of their own. Still inert: no `planSelect` call
      site, `GOOPG_PGSHAPED_DP` OFF. 2 ledger rows. Bar met: UNITS
      (SPOT/DS05 not applicable — no call site, flag OFF, so no plan and no
      row can move).
- [ ] **M0127-P5.4b-ii-b-2 — Memoize paths + the §5.2 binding contract:**
      `get_memoize_path` (joinpath.c:562) — wrap an NLI inner in a cache when
      the outer key's distinct count makes it pay — plus the NLI binding
      contract (shared eligibility fn with `tryBuildNLI`; constructor failure
      on a DP-chosen path = loud planner error). Split out of P5.4b-ii-b
      because both need a built `*Join` NODE rather than a Path —
      `tryBuildNLI` analyses one — so they attach to P5.5's `createPlan` arms,
      not to path generation. goopg already has the executor operator
      (`internal/executor/operators_memoize.go`); what is missing is the
      path-level eligibility and cost. IMPLEMENTATION-TODO P5.4b-ii-b-2; 03
      §5.2. Bar: UNITS + SPOT + DS05.
      **↳ DEPENDENCY-DEFERRED 2026-08-04 (loop #69), not skipped.** The item's
      own body states the blocker: both halves need a built `*Join` NODE, and
      `createPlan` does not build one for a searched subtree until **P5.5**.
      Per this file's own selection rule ("topmost unchecked item *unless* the
      banner or a DEPENDENCY forces another order") loop #69 took the next
      dependency-free P5 item instead (`P5.4c-i`). Re-select this AFTER P5.5
      lands its `createPlan` arms; nothing else about it has changed.
- [x] **M0127-P5.4c-i — `sort_inner_and_outer`, the merge arm that sorts BOTH
      inputs.** **DONE 2026-08-04 (loop #69)**, `internal/planner/joinpathsmerge.go`
      (+ `joinpaths.go`, `path.go`). PG has TWO merge arms and they need
      different things, which is where P5.4c splits: this one
      (joinpath.c:1357) takes the two cheapest-total paths and sorts both, so
      it needs NOTHING from its inputs and is expressible in full today;
      `generate_mergejoin_paths` (:1564) exploits an already-ordered outer and
      is dead until some path carries pathkeys. Landed with it: the
      **per-equivalence-class sort-key reduction**
      (`select_outer_pathkeys_for_merge`, pathkeys.c:1697-1704) — at ({a,b},{c})
      with `a.x = c.x` and `b.x = c.x` the two clauses are ONE restriction,
      `a.x = b.x` already holds inside the outer, so one sort key orders it for
      both while both stay merge clauses; the **pair-local key orientation**
      (the same clause faces the other way when the sides swap, exactly as
      `isKeyableFor`'s split does — trusting `leftKey` to be the outer operand
      is a WRONG PLAN, not a slow one); the **one-path-per-ordering loop**
      (:1447-1466), whose point is that this join's OUTPUT order decides whether
      a merge above it needs a sort at all; `build_join_pathkeys`
      (pathkeys.c:1295); and `try_mergejoin_path`'s sort-skip (:1091-1097) and
      still-parameterised refusal (:1073-1081 — merge has no
      `allow_star_schema_join` escape, so P5.4b-ii-b-1's "every join path is
      unparameterised" invariant is untouched). **`PathSort` finally has a
      producer**: the Sort is an explicit child path rather than PG's
      `MergePath.outersortkeys` field — same plan, same cost, and what 03 §5.3
      asks for by name. **The finding: the sort-SKIP branch is the CONSUMER
      half of P5.4c-ii and was worth landing before its producer** — written
      and tested here against a hand-ordered rel, it means the next slice adds
      ordered index paths and gets the saving, instead of adding both halves of
      an interface neither side has exercised. Still inert: no `planSelect`
      call site, `GOOPG_PGSHAPED_DP` OFF. 3 ledger rows. Bar met: UNITS
      (SPOT/DS05 not applicable — no call site, flag OFF, so no plan and no row
      can move).
- [x] **M0127-P5.4c-ii-a — `build_index_pathkeys`: the ordering a B-tree index
      path delivers.** DONE 2026-08-04 (`internal/planner/pathkeysindex.go` +
      `pathparamindex.go`). The first of the three pieces P5.4c-ii was one
      item for. PG's `build_index_pathkeys` (pathkeys.c:740) with each of its
      loop rules pinned separately: INCLUDE columns excluded (:763-764),
      per-column `reverse_sort`/`nulls_first` (:775-776), backward inversion of
      BOTH (:770-774), STOP-not-skip on an unusable column (:815-822),
      non-orderable AM (:748 — for goopg also `USING hash`, which rides the
      B-tree substrate but is not orderable in PG), and `pathkey_is_redundant`'s
      already-in-list half (:800). Wired into `addOneParameterizedIndexPath`,
      the only index-path constructor there is, so `addPath`'s pathkey dimension
      stops being a constant `dimEqual` — PG passes the same `useful_pathkeys`
      to the parameterised path as to the plain one (indxpath.c:750-800).
      **The finding: this does NOT unblock the merge arm.** `addMergeJoinPath`
      refuses a parameterised path (joinpath.c:1073-1081) and every ordered
      index path today is parameterised, so an ordered merge OUTER still needs
      the unparameterised arm — now split out as P5.4c-ii-b. IMPLEMENTATION-TODO
      P5.4c-ii-a; 03 §5.3, 04 §2.1. 4 ledger rows. Bar met: UNITS.
- [x] **M0127-P5.4c-ii-b — UNPARAMETERISED ordered index paths.** DONE
      2026-08-04 (`internal/planner/costindex.go` + `pathindexordered.go`,
      `cost_funcs.go`). `build_index_paths`' `useful_pathkeys != NIL` arm
      (indxpath.c:750-800) over a real `cost_index` (costsize.c:520): an index
      path with the index's ordering, NO index quals and an **empty
      `RequiredOuter`** — the only shape `try_mergejoin_path` accepts
      (joinpath.c:1073-1081), so P5.4c-i's sort-skip branch finally has a
      producer. `cost_index`'s `loop_count == 1` arm is transcribed whole:
      Mackert-Lohman `index_pages_fetched` (costsize.c:906) in both regimes,
      `genericcostestimate` reduced to the no-qual case, `btcostestimate`'s
      50 × `cpu_operator_cost` descent charged at STARTUP (what lets an
      ordered scan beat a sort under LIMIT), and the csquared interpolation
      between all-random and one-random-then-sequential I/O. **The one-currency
      discipline 04 §1 demanded is met by tying it to the EXISTING
      `indexProbeCostMultiplier` on every random-page term** rather than
      calibrating a second model — a test pins that raising the knob scales
      both. `indexCorrelationFor` returns 0 because goopg collects no
      `STATISTIC_KIND_CORRELATION`, which is what PG itself charges for a
      missing slot; consequence: an ordered scan prices at `max_IO_cost` and
      survives only on its pathkeys (pinned by a test). Two gate findings:
      `has_useful_pathkeys` reduces to its join-clause arm (§10 carries no
      query/group pathkeys), and building the ordering + truncating it to the
      merge-useful prefix is provably ONE left-to-right loop, because with
      syntactic pathkeys there is no EC object to name a column no clause
      mentions. `effective_cache_size` joins `costParams` in PAGES with its own
      drift guard. `addBaseRelIndexPaths` combines both halves of
      `create_index_paths` so a caller cannot wire up one. Still inert. 7
      ledger rows — one ("`Path` names no index or scan direction") is a stated
      **P5.5 prerequisite**. IMPLEMENTATION-TODO P5.4c-ii-b; 03 §5.3, 04 §1.1.
      Bar met: UNITS + SPOT.
- [x] **M0127-P5.4c-ii-c — `generate_mergejoin_paths`.** DONE 2026-08-04
      (`internal/planner/joinpathsmergeouter.go` + `joinpathsmerge.go`,
      `joinpaths.go`). **P5.4c is CLOSED.** joinpath.c:1564 plus the merge half
      of `match_unsorted_outer` (:1998-2013), wired at PG's arm-2 position —
      between `sort_inner_and_outer` and the hash arm — so a merge over an
      already-ordered outer wins an exact tie against a hash path exactly as it
      does in PG. **It iterates `outer.Pathlist`, and that is the slice**: an
      ordered index path is by construction NOT the cheapest total (P5.4c-ii-b:
      `indexCorrelationFor` is 0, so it prices at `max_IO_cost` and survives
      `addPath` only on its pathkeys), so an arm keyed to `CheapestTotal` would
      find nothing at all. Three behaviours transcribed because each changes
      which plan wins: the mergeclause list is a **PREFIX** of the outer's
      ordering and stops at the first unserved position
      (`find_mergeclauses_for_outer_pathkeys` — an outer sorted `(x,y)` joined
      only on `y` is unusable, not usable on `y`, because a merge cannot skip a
      leading sort column); the clause list is **TRUNCATED** to reach a cheaper
      presorted inner, searched on BOTH cost axes under PG's strictly-cheaper
      rule (a shorter prefix demotes a merge clause to per-tuple work, so it
      must buy something); and the result carries the outer's **FULL** ordering
      rather than the merge keys, which is the compounding effect the arm exists
      for. **Two findings that changed code, not just docs.** (1) A truncated
      merge must **demote its dropped clauses to residual** — PG carries the
      whole restrictlist to plan time and `create_mergejoin_plan` subtracts,
      while goopg fixes the key/residual split during path generation (03 §5.4),
      so a dropped merge clause would have been evaluated by NOTHING: a wrong
      answer, not a slower plan. Running the demotion through `qualEvalCost` is
      also what puts a price on the trade the strictly-cheaper rule weighs.
      (2) **One outer sort key can owe SEVERAL inner sort keys** (`a.x = c.x AND
      a.x = c.y` is one outer key and two inner ones; both stay merge clauses,
      so an inner sorted only by `c.x` would be handed to an operator comparing
      on `(c.x, c.y)`) — P5.4c-i's one-inner-key-per-group model could not
      express it, and `mergeInnerSortKeys` is now the single
      `make_inner_pathkeys_for_merge` BOTH merge arms use, so the siblings
      cannot drift (Rule #2). **PG's materialize-inner decision has NO goopg
      analogue**, and that is a finding rather than an omission: PG's mergejoin
      rewinds the inner with mark/restore so `final_cost_mergejoin` must decide
      whether to interpose a `Material`, while `mergeJoinStream.bufferGroup`
      (`internal/executor/join_merge_stream.go:616`) already buffers each inner
      equal-key group unconditionally, spilling past `work_mem`. Consequences:
      any presorted inner path is consumable here regardless of kind, no
      `PathMaterial` is introduced (it would double-buffer), and the COST of
      that buffering — PG's `rescanratio` plus the group file — is charged by
      nothing and is LEDGERED rather than guessed. The jointype gauntlet
      (`nestjoinOK` / `useallclauses`) and the FULL-without-usable-clause
      contract are ledgered, not written as dead branches:
      `addPathsToJoinrel` carries no jointype to switch on while 03 §4.4 pins
      non-INNER outside the search. Still inert (`GOOPG_PGSHAPED_DP` OFF, no
      `planSelect` caller). 3 ledger rows. 10 new tests.
      IMPLEMENTATION-TODO P5.4c-ii-c; 03 §5.3. Bar met: UNITS + SPOT. DS05 not
      applicable and not run — the arm adds paths to a search with no caller, so
      no plan and no row can move.
      **Next M0127 selection is P5.5** (`createPlan` arms + the 03 §10
      search-boundary coordinate map), whose stated prerequisite is the
      P5.4c-ii-b ledger row: `Path` names neither its index nor its scan
      direction.
- [x] **M0127-P5.5-a — `IndexPath.indexinfo` + `indexscandir` on `Path`.** DONE
      2026-08-04 (`internal/planner/pathindexcarrier.go` + `path.go`,
      `pathparamindex.go`, `pathindexordered.go`). The stated PREREQUISITE of
      P5.5, ledgered at P5.4c-ii-b: goopg's `Path` is one flat struct with a
      `Kind` discriminator, and `PathIndexScan` recorded the ordering, cost and
      rows of a specific index scan without recording WHICH index produced
      them — so the DP could choose a path that no `*IndexScan` node can be
      built from. `ScanDirection` reproduces PG's exact -1/0/+1 encoding
      (access/sdir.h:24), which buys the zero value as "not an index path" and
      so needs no second discriminator on the flat struct. The direction is
      carried although only `ForwardScanDirection` is ever produced, for the
      same reason `DisabledNodes` is carried at a constant 0: a path that does
      not SAY its direction silently means forward, and adding the backward arm
      later would then be a change to every reader rather than to the producer.
      The invariant with teeth — the recorded direction and the recorded
      pathkeys must describe the SAME scan, since `build_index_pathkeys`
      inverts direction AND null placement (pathkeys.c:770-774) — is held
      STRUCTURALLY: `indexPathOrdering` returns the pair and is the only way
      either constructor obtains either half, so the two cannot drift (rule
      #2). `IndexPath.indexclauses` is deliberately NOT carried, ledgered with
      the finding that blocks a verbatim copy: PG's list is in index-column
      order while goopg's `bound` is in candidate order, and the executor's
      `IndexScan.Keys[i]` binds `Index.Columns[i]` positionally. Still inert.
      2 ledger rows. 6 new tests. IMPLEMENTATION-TODO P5.5-a; 03 §10; 04 §1.1.
      Bar met: UNITS + SPOT. DS05 not applicable — the fields are written by a
      search with no `planSelect` caller and `GOOPG_PGSHAPED_DP` is OFF, so no
      plan and no row can move.
- [x] **M0127-P5.5-b — `IndexPath.indexclauses` on `Path`, in INDEX-COLUMN
      order.** DONE 2026-08-04 (`internal/planner/pathindexclauses.go` +
      `path.go`, `pathparamindex.go`, `pathindexordered.go`). The second half of
      the index carrier, ledgered at P5.5-a: a parameterised index path was built
      from `bound []paramIndexClause` and then DISCARDED them once cost and rows
      were computed. `createPlan` needs them twice —
      `fix_indexqual_references` (createplan.c:5121) builds the scan's keys from
      the list, and `is_redundant_with_indexclauses` (createplan.c:3075) uses it
      to DROP those same clauses from the node's filter quals, which is why the
      carrier holds the `*restrictInfo` by identity and not just the probe value.
      **The ORDER is the whole difficulty.** PG's list is ordered by index column
      because `build_index_paths`' outer loop runs over `indexcol` ("this order
      is depended on by btree", indxpath.c:1042); goopg's `bound` is in the
      search's CANDIDATE order — the order the user wrote the join conditions in —
      and the executor's `IndexScan.Keys[i]` binds `Index.Columns[i]`
      POSITIONALLY, so a verbatim copy would make a composite probe compare the
      wrong pair of columns: a wrong answer, not a slow plan. `indexPathClauses`
      holds the order STRUCTURALLY by looping over `idx.Columns` and looking the
      clause up per column (PG's own loop shape) rather than by sorting, which
      would be a second statement of the same fact that a later edit could
      contradict (rule #2). Its keys agree with
      `pickIndexCoveringAllLeadingColumns`' ordered list by construction — both
      read the same first-wins clause set in the same column order — so the path
      cannot be costed for one probe and built as another. Two narrowings are
      ledgered rather than written, each a shape goopg's executor cannot express:
      PG's list is NONdecreasing in `indexcol` (`x > 1 AND x < 5` puts two
      clauses on one column) while goopg carries one equality per column, and
      PG's gapped/prefix probe (`amoptionalkey`) is DECLINED outright — nil, not
      a shortened list, because a shortened list silently re-indexes every
      position after the gap. The unparameterised ordered path carries an EMPTY
      list, which is pathnodes.h:1817's "an empty indexclauses list implies a
      full index scan" and not an omission. Still inert. 3 ledger rows. 7 new
      tests. IMPLEMENTATION-TODO P5.5-b; 03 §10; 04 §1.1. Bar met: UNITS + SPOT.
      DS05 not applicable — the field is written by a search with no `planSelect`
      caller and `GOOPG_PGSHAPED_DP` is OFF, so no plan and no row can move.
- [x] **M0127-P5.5-c — `create_indexscan_plan`: the first real `createPlan`
      arm.** DONE 2026-08-04 (`internal/planner/createplanindex.go` +
      `createplan.go`, `path.go`, `joinsearch.go`, `pathparamindex.go`,
      `pathindexordered.go`). The consumer of the carrier P5.5-a/-b landed: the
      arm that turns a chosen `PathIndexScan` back into the `*IndexScan` node
      goopg's executor runs (createplan.c:3006). The difficulty is not the index
      but everything else the leaf carries — PG's arm reaches relation, alias,
      target list and `baserestrictinfo` through `RelOptInfo`'s range-table
      entry, and goopg's search-only rel knows none of that. Resolved by
      recording WHAT THE SEARCH WAS HANDED: `RelOptInfo.baseLeaf` carries the
      leaf `Node` `buildInitialRels` received (the search boundary's half of
      03 §10's coordinate map — what a base relid MEANS), and the arm re-emits
      from it. The schema is the LEAF's (a synthesised target list would
      renumber columns under the quals that reference them); the alias survives
      (self-join disambiguation, M0062-0002); the local quals survive as
      re-created `*Filter` wrappers — goopg's `*IndexScan` has no `qpqual`
      field — rebuilt as NEW nodes because the originals are matched by POINTER
      identity elsewhere, with `LeafLocal` intact or a posMap pass renumbers the
      predicate's leaf-local ColumnRefs. `indexScanLeafFor` is ONE predicate
      with two callers (rule #2): the arm's resolver and the eligibility gate
      now applied at BOTH index-path producers, so a leaf that cannot be rebuilt
      as an index scan never has an index path COSTED over it — otherwise the
      DP prices a plan the builder then refuses. Preconditions panic with the
      wrong answer named; the dangerous case is a parameterised path with an
      EMPTY clause list, because empty means FULL INDEX SCAN (pathnodes.h:1817)
      and building it would silently turn a costed point probe into a
      whole-relation scan. Still inert (`GOOPG_PGSHAPED_DP` OFF, no `planSelect`
      caller). 2 ledger rows (join-arm residual drop via
      `is_redundant_with_indexclauses`; `*IndexOnlyScan` leaf declined). 9 new
      tests (`createplanindex_test.go`). IMPLEMENTATION-TODO P5.5-c; 03 §10;
      02 §3. Bar met: UNITS + SPOT. DS05 not applicable — the arm is reachable
      only from the inert search, so no plan and no row can move.
- [x] **M0127-P5.5-d — `create_seqscan_plan` + `create_sort_plan`: the two
      structurally simple arms.** DONE 2026-08-04
      (`internal/planner/createplansimple.go` + `createplan.go`,
      `createplanindex.go`). The seq-scan arm (createplan.c:2910) is the index
      arm's mirror over the SAME leaf resolver: `indexScanLeafFor` is renamed
      `scanLeafFor` and its rewrapper generalised from `*IndexScan` to `Node`
      (one predicate now serving two arms, rule #2), and `scanIdentity` gains
      the four `*SeqScan`-only fields (EstRelRows, LockParentOID,
      SkipIfVanished, InheritParentOID) so the rebuild is LOSSLESS — honouring
      the struct's stated purpose that a `*SeqScan` field addition is a
      compile-visible edit there. The arm rebuilds a FRESH node even when the
      leaf's base scan already is a `*SeqScan` (the emitted tree must never
      alias nodes the pipeline still owns — `attachRelationLocalFilters`
      matches leaves by POINTER identity) and DEMOTES an `*IndexScan` leaf when
      the search costed the sequential scan cheaper. Panics: a parameterised
      seq scan is undischargeable (nothing above it can ever bind
      `RequiredOuter`); claimed pathkeys are an ordering a heap scan does not
      deliver (a merge above would trust it and emit wrong rows); index detail
      means a costed probe was mislabelled. The sort arm (createplan.c:2177) is
      the first arm with a CHILD path — it recurses via `createPlan`, which is
      why it lands BEFORE P5.5-e's join arms: P5.4c's merge paths already carry
      `PathSort` children (`sortPathFor`), so the merge arm cannot be written
      until this one exists. Key translation is direction-only
      (`PathKey.SortAsc` negated into the executor's `SortKey.Desc`,
      NullsFirst carried through). 2 ledger rows (`generateScanPaths` lacks the
      shared `scanLeafFor` gate — must be applied at C4 wiring;
      CP_SMALL_TLIST width trim before the sort has no analogue). Still inert
      (`GOOPG_PGSHAPED_DP` OFF, no `planSelect` caller). 7 new tests
      (`createplansimple_test.go`). IMPLEMENTATION-TODO P5.5-d; 03 §3, §5.3,
      §10. Bar met: UNITS + SPOT. DS05 not applicable — the arms are reachable
      only from the inert search, so no plan and no row can move.
- [x] **M0127-P5.5-e-i — the coordinate carrier + `create_hashjoin_plan`: the
      first join arm.** DONE 2026-08-04 (`internal/planner/createplanjoin.go` +
      `createplan.go`, `createplansimple.go`, `path.go`, `joinsearch.go`). The
      scan and sort arms could ignore coordinates — a scan's schema is its
      leaf's, a sort moves rows not columns — but a join MERGES two schemas and
      cannot emit a key without answering the question. And the question has
      teeth: every `restrictInfo.clause` is written in pre-search BINDING
      coordinates, not incidentally but because `relidsOfExpr` DECIDES a
      clause's relset by bucketing its `ColumnRef.Index` against exactly those
      offsets, while the emitted tree is a cost-chosen reordering — so a clause
      copied across unchanged keys on whichever column happened to land at that
      index, which runs and returns wrong rows. Resolved by carrying the map
      through the recursion rather than re-deriving it:
      `createPlanNode(p) (Node, outputLayout)`, `outputLayout[i]` = output
      column i's binding coordinate, seeded by `RelOptInfo.baseOffset`
      (recorded beside `baseLeaf` at `buildInitialRels` — `baseLeaf` says what a
      relid MEANS, `baseOffset` where it USED TO BE), passed through by a sort
      and concatenated by a join in the SAME statement that concatenates the
      schema, so it cannot drift from the tree. `translateToLayout` is
      `set_join_references` (setrefs.c:2557) at goopg's fidelity — one
      renumbering onto the merged row, not PG's OUTER_VAR/INNER_VAR split, since
      goopg's executor evaluates one merged row — on `cloneExprRefs` so the
      search's own clauses are never mutated, `scopeIgnore` to agree with
      `relidsOfExpr` (rule #2), refusing the two non-positional references.
      `BuildLeft` is never set: `generateHashJoinPaths` adds both orientations
      as paths and `add_path` keeps the cheaper, so the build side was decided
      BY COST in the child order — a second opinion here is the uncosted
      name-tag rule 06 §2.1 retires. 2 ledger rows (`joinqual`/`qpqual` split
      folded into one `Predicate` — equivalent for inner joins, wrong the moment
      an outer join enters the search; SubPlan args not re-based). 1 inventory
      pin. Still inert (`GOOPG_PGSHAPED_DP` OFF). 8 new tests. Bar met: UNITS +
      SPOT. DS05 not applicable — reachable only from the inert search.
- [x] **M0127-P5.5-e-ii-a — `create_mergejoin_plan`: the second join arm, and
      the sort nodes goopg must DELETE where PG creates them.** DONE 2026-08-04
      (`internal/planner/createplanjoin.go` + `createplan.go`,
      `createplansimple.go`). P5.5-e-i's prologue was lifted into
      `joinInputsFor` / `keyPairs` / `joinPredicate` in the same commit so both
      arms build the merged row from ONE piece of code — two arms concatenating
      schema and layout separately drift, and the drift is a wrong-column join
      that still runs. Merge-only fact 1: **the key list IS the sort order.**
      `sortInnerAndOuter` concatenates the key GROUPS in the pathkey order it
      chose and `mergeSideKeyExprs` sorts each side by the tuple in
      `Join.HashKeys` order, so that list is `outersortkeys`/`innersortkeys`,
      not a set — the arm preserves the given order AND folds the keys into
      `Predicate` in it, because `fillJoinHashKeys` rebuilds the published list
      from `Predicate` at the tail of `Plan()`, so re-ordering there re-orders
      the SORT. Merge-only fact 2: **the explicit `PathSort` children are
      absorbed.** PG materialises a Sort here because `nodeMergejoin` requires
      sorted input; goopg's `JoinAlgoMerge` operator sorts both inputs itself,
      unconditionally (`openMergeJoin`), so emitting the child Sort sorts each
      side twice — a cost `tryMergeJoinPath` never charged. `absorbMergeSort`
      steps over it (coordinate-neutral: a sort passes its layout through) and
      the arm refuses any descending/nulls-first result or absorbed-sort key,
      because goopg's merge comparator is fixed ascending/NULL-keys-last — a
      standing guard for P5.4c-ii's ordered index paths. Also fixed: a latent
      P5.5-d defect this arm made reachable — `createSortPlan` emitted its
      pathkey expressions UNTRANSLATED, so a sort over a rel not first in
      binding order ordered by whichever column sat at that index; now re-based
      through the same `translateToLayout` (rule #2). 2 ledger rows. Still inert
      (`GOOPG_PGSHAPED_DP` OFF). 4 new tests + 1 strengthened. Bar met: UNITS +
      SPOT.
- [x] **M0127-P5.5-e-ii-b — `create_nestloop_plan`: the nested-loop arms.**
      DONE 2026-08-04 (`internal/planner/createplannl.go` new, +
      `createplan.go`, `createplanindex.go`, `pathparamindex.go`,
      `joinpathsnli.go`). One path kind, TWO executor nodes: the arm dispatches
      on the INNER CHILD's `RequiredOuter` (the same fact PG dispatches on when
      it emits `NestLoopParam`), so `addNestLoopPath`'s plain loop becomes a
      `*Join{JoinAlgoNestedLoop}` and `addNLIPaths`' pair becomes a
      `*NestedLoopIndexJoin` — a different TYPE, not a flag, because its `Inner`
      is a `*IndexScan` the driver calls `Rescan` on. **The finding: an NLI is
      the first node here whose expressions live in TWO coordinate spaces.**
      `indexScanOp.Rescan` evaluates the probe keys against the slot the parent
      bound — the OUTER row alone, since the inner row does not exist yet — while
      the residual is evaluated through `virtualOut` over `outer ++ inner`. So
      the keys are re-based onto the outer layout (taken as the PREFIX of the
      merged one, not re-derived) and the residual onto the merged one. On a
      two-rel query with the outer first in binding order the two spaces
      coincide, so a single-space arm builds a runnable node and probes the
      wrong column the moment the search reorders the join — the tests put the
      outer second for exactly that reason. **Second finding, a live defect in
      the producer:** `nestloopResidualClauses` reproduced PG's movability drop,
      but PG may drop on movability alone only because `ppi_clauses` +
      `qpqual` really do apply every movable clause down there. goopg's
      parameterised inner applies only `Path.IndexClauses` and its `*IndexScan`
      has no qual field, so `b.y > a.x` was dropped from the join residual and
      enforced by NOTHING. Narrowed to `probeEnforcedClauses` (by `restrictInfo`
      identity, against the same list `createPlan` turns into `IndexScan.Keys`);
      the EC half is not reproduced because `selectivityClauses` already reduced
      each class to one member. Also: `addParameterizedIndexPaths` now declines
      a `*Filter`-wrapped leaf (`scanLeafIsBare`) — `NestedLoopIndexJoin.Inner`
      cannot carry it and hoisting is the D6.3b Q9 blowup. 3 ledger rows. Still
      inert (`GOOPG_PGSHAPED_DP` OFF). 6 new tests + 1 rewritten. Bar met:
      UNITS + SPOT.
- [x] **M0127-P5.5-f-i — the search boundary: 03 §10's coordinate map, and the
      one node that makes it invisible above the search root.** DONE 2026-08-04
      (`internal/planner/createplanroot.go` new).
      `createPlanAtSearchRoot(p, bindingWidth)` is now the only `createPlan`
      entry point a search caller may use: `createPlanNode` returns the search's
      own cost-chosen column order, which is correct for a child of another join
      arm and wrong for everything else — and the enclosing tree (top Project
      targets, retained Filters, Sort keys, Aggregate arguments, the pinned
      unnest spine) is written in PRE-SEARCH BINDING coordinates because that is
      the space `planSelect` resolved it in. **The finding that decided the
      variant 03 §10 left open:** at the search root, §10's canonical RELID
      order and the pre-search BINDING order are THE SAME SEQUENCE —
      `buildInitialRels` assigns relid `1<<i` to FROM item `i` and records an
      ascending `baseOffset`, and the root's relset is the FULL set. So the
      reordering `Project` is not a way around the canonical layout, it IS that
      layout materialised at the one place §10 requires it observable; it
      collapses the boundary map to the identity for every consumer above (the
      enclosing tree needs no rewrite at all), and the map survives in exactly
      one place — that node's target list. It is elided when the search left the
      columns where the bindings put them, the leading left-deep case.
      **Second finding, from a test that failed:** `bindingWidth` must be a
      PARAMETER, not `len(layout)`. A FROM item that never entered the search
      yields a root that is entirely self-consistent and permutation-clean when
      judged against its OWN width, while missing columns the enclosing tree
      still references — the M0097-0058 shape exactly, and detectable only from
      outside. `boundaryMap` refuses holes, out-of-range coordinates and
      duplicates against the caller's number. §10's plan-time tripwire is real
      code now (`assertColumnRefsWithinSchema`, turning that class from an
      execution-time slice panic into an attributable planner bug) but is
      applied to the boundary node alone. 2 ledger rows: PG adds NO node here
      (`set_upper_references`, setrefs.c:2214, renumbers upper Vars in place, so
      the boundary `Project` is a node PG never prints), and the tripwire's
      one-node scope. Still inert (`GOOPG_PGSHAPED_DP` OFF). 8 new tests
      (`createplanroot_test.go`). IMPLEMENTATION-TODO P5.5-f-i; 03 §10; 02 §3.
      Bar met: UNITS + SPOT.
- [x] **M0127-P5.5-f-ii-a — the searched-subtree tag: making the legacy
      layout-correction family skip, and finding out that half of it already
      did.** DONE 2026-08-04 (`internal/planner/searchedtree.go` new).
      `searchedTree` is a one-bit embedded tag on the seven node kinds
      `createPlanAtSearchRoot` can return as a root; `markSearchedTree` PANICS
      on any other kind, because the failure mode of a future arm returning an
      untaught root kind is a SILENTLY untagged subtree — a plan that runs and
      returns wrong rows. Three skips consume it: `buildBindingsPosMap`'s
      collector treats a tagged root as an opaque leaf (advance past its width,
      record no scan entry, so every binding inside it falls through the
      returned closure unchanged — the identity, which is the truth),
      `applyJoinTreePosMap` returns at one, `reconcileNLILayout` returns at one.
      **The measured finding that reshaped the task:** the boundary `Project`
      was ALREADY opaque to the whole family — not for search reasons, but
      because M0125-0012 (TPC-DS Q8) made every `*Project` in a join tree a
      scope boundary on both sides of the map so that build and apply stop at
      the same nodes. A probe confirms `buildBindingsPosMap` returns nil over it
      and no target moves. The hole was the **elided** root: with the columns
      already in binding order there is no Project to stop at and both passes
      walk into a bare `*Join`. The numeric half of that is provably harmless
      (identity layout ⇒ identity map, since `collect`'s DFS order over a join
      IS its output order) — but `applyJoinTreePosMap`'s `*Join` arm calls
      `reresolveJoinByName` and `reconcileNLILayout` is name resolution end to
      end, and those rebind the searched joins' keys by NAME over a layout
      derived by COORDINATE one node earlier. **Second finding:**
      `assertSearchedTreeNeedsNoReconcile` (the no-op assertion — it runs the
      real pass over the join tree below the boundary and panics if any
      `ColumnRef` moved) is weaker evidence than it looks: it abstains on an
      unnamed operand, on an ambiguous name, and on everything the pass does not
      rebind — and the P5.5-e fixtures build operands with `col(i)`, i.e.
      UNNAMED, so reusing them would have made every assertion pass vacuously.
      The tests supply their own named-clause helper. 2 ledger rows. 10 new
      tests (`searchedtree_test.go`), two of which pin PRE-task behaviour so a
      later simplification can see which half was already covered. Still inert
      (`GOOPG_PGSHAPED_DP` OFF). IMPLEMENTATION-TODO P5.5-f-ii-a; 03 §10; 02 §3.
      Bar met: UNITS + SPOT.
- [x] **M0127-P5.5 — `createPlan` arms for all live PathKinds → existing
      Nodes** (closed by its last sub-item, P5.5-f-ii-b: the pinned-spine
      re-resolution consumes the boundary map and the tripwire is widened from
      the boundary node to the enclosing tree). DONE 2026-08-04
      (`internal/planner/enclosingtree.go` new, `predp.go` call site).
      When the subtree spliced under the pinned spine carries the
      searched-subtree tag, `assertSpineConsumesIdentityBoundaryMap` checks
      column-by-column (Name, SourceTableIdx) that the boundary republished the
      concatenation the spine was resolved against, and the re-resolution
      returns without rebinding — the skip is now proved, not argued.
      **The finding that decided how it is written:** reading
      `layoutPosMap == nil` as "the map is the identity" would have been WRONG.
      That helper returns nil for two different reasons — "identical, nothing to
      remap" and "widths differ, refuse to remap rather than corrupt" — so a
      boundary that lost or gained a column takes the second door and is
      indistinguishable from success while the enclosing tree goes on
      referencing columns that moved (the M0097-0058 shape through a different
      door). The assertion compares the schemas itself and never consults `pm`.
      The tripwire is `assertEnclosingTreeColumnRefs` over ONE switch
      (`enclosingNodeScopeOf`) answering which expressions / against what width /
      which children continue the walk, because those are one fact about a node
      (rule #2): a `*Join`'s predicate and BOTH keys index the merged
      `Left ++ Right` row even for Semi/Anti — whose `Output()` is Left only, so
      checking against `Output()` would reject every legal right-side key on the
      pinned spine — and a `*NestedLoopIndexJoin` is descended on the OUTER side
      only, its inner probe keys living in the outer's coordinate space.
      **Second finding:** with 53 node kinds, a partial walk that stops at
      unenumerated kinds checks NOTHING and returns normally whenever the kind it
      stops at sits on the path to the searched subtree — P5.5-f-ii-a's vacuity
      finding one level up, and harder to see because a tree walk looks
      exhaustive. The guard is therefore on the partiality, not the enumeration:
      a stop is not a panic, but the walk must REACH a searched subtree or the
      assertion fails naming every kind it stopped at. 2 ledger rows (the
      partial enumeration + `walkPlanExprs`'s missing
      `Aggregate.Passthrough`/`AggregateCall.Filter`/`WindowFunc` expressions;
      and `pushOneConjunct` as the fourth legacy family member not taught about
      the tag). 12 new tests (`enclosingtree_test.go`). Still inert
      (`GOOPG_PGSHAPED_DP` OFF). IMPLEMENTATION-TODO P5.5-f-ii-b; 03 §10; 02 §3.
      Bar met: UNITS + SPOT. DS05 + PLAN re-baseline not applicable — the whole
      change is reachable only from a tagged node, and only
      `createPlanAtSearchRoot` tags, which nothing calls from `planSelect`; no
      plan can move.
- [ ] **M0127-P5.6 — `calcJoinrelSize` + FK-superkey generalisation + eqjoinsel +
      FK clamp** (04 §3.1-3.3; the Q9 class-(a) fix); delete the quadratic build
      penalty; estimate-audit tooling (09 §5 — Q9's chain ≤ 10²× at the final
      joinrel). Re-evaluate M0125-0003 stage 3 here (rows-once per RelOptInfo,
      04 §2). IMPLEMENTATION-TODO P5.6; 04 §3; 09 §5. Bar: UNITS + DS05 +
      estimate audit run. **DECOMPOSED into -a … -e below** (04 §3's remedy set
      is four mechanisms and a measurement, in a mandatory order).
- [x] **M0127-P5.6-a — the per-clause selectivity substrate: `examine_variable`,
      `get_variable_numdistinct`, `eqjoinsel`'s no-MCV arm.** DONE 2026-08-04
      (`internal/planner/joinselectivity.go` new,
      `internal/catalog/catalog.go` `ColumnStats.StaDistinct` new, its two
      existing open-coded copies switched over).
      The compounding P5.6 exists to end has a specific source: the legacy
      `estimateJoinCost` divides |L|·|R| by the PRODUCT of every spanning
      edge's per-side NDV (bushy.go:1266-1301), where PG divides by ONE
      ndistinct — the LARGER of the two sides' — per clause, because
      upstream's estimate is the MINIMUM of two upper bounds rather than a
      per-edge product. Over a chain of correlated equalities the product
      charges one restriction several times; that is the same double-count
      04 §5 says the ×2.0 `inferredEdgePenalty` was papering over, in the cost
      dimension instead of the cardinality one where the error lives.
      **The finding that decided the dispatcher:** the OPERATOR decides which
      estimator runs, `isEquijoin` does not. `a.x = b.y + c.z` is an equality
      that splits into no two one-sided operands, so it can key no hash join
      and the flag is false — but PG still prices it with `eqjoinsel`, and
      sending it to `clause_selectivity`'s 0.5 fall-through would charge 100×
      the 0.005 upstream charges, on every joinrel above it. What the flag
      does govern is only which OPERANDS are examined: `restrictInfo.leftKey`
      is the canonical left of the SPLIT, which the builder is free to have
      taken from the clause's right-hand side, so pairing `bo.Left` with
      `ri.leftRelids` would read one relation's column against another
      relation's statistics.
      **Second finding:** goopg splits upstream's one signed `stadistinct`
      into `NDistinct` + `NDistinctFrac`, and the reduction back to PG's
      convention was open-coded twice already (the pg_statistic heap row and
      the pg_stats view). A third copy inside the estimator is precisely the
      sibling shape where the planner plans on a different number than the one
      it publishes to the user, so it became `ColumnStats.StaDistinct` and all
      three read it (rule #2). Resolution is by column NAME, not by
      `ColumnRef.Index` — the search's clauses live in the pre-search
      concatenation's coordinate space (03 §10), so a positional read of
      `Stats.Columns` would pick a different column for every relation that is
      not first in the FROM list. 3 ledger rows (eqjoinsel's MCV arm;
      `vardata->isunique`, which is P5.6-b's own mechanism;
      `examine_variable`'s subquery / expression-operand arms). 17 tests
      (`joinselectivity_test.go`). Still inert (`GOOPG_PGSHAPED_DP` OFF and
      `sizeJoinRel` still has no production implementation).
      IMPLEMENTATION-TODO P5.6-a; 04 §3.2. Bar met: UNITS. DS05 + SPOT + PLAN
      not applicable — no production caller exists, so no plan can move.
- [x] **M0127-P5.6-b — `calcJoinrelSize` + the concrete `joinRelBuilder`.**
      DONE 2026-08-04 (`internal/planner/joinrelsize.go` new;
      `selectivityClauses`' winner rule factored out into
      `oneClausePerEquivClass`; `examineJoinVar`'s operand resolution factored
      into `resolveJoinVarColumn`, which the superkey test reads too).
      04 §3.1's FK/unique-superkey generalisation over clause SUBSETS driving
      04 §2's rows-once discipline: `sizeJoinRel` at find-or-create time,
      before any path is generated, through `searchJoinRelBuilder` — the
      concrete builder that binds sizing to `addPathsToJoinrel` and closes the
      last seam `makeJoinRel` left open.
      **The finding that shaped the mechanism:** the no-fan-out cannot be a
      divisor bolted onto the per-clause estimate; it has to be PG's
      remove-and-substitute. `get_foreign_key_join_selectivity` takes the
      covered clauses OUT of the restriction list and puts one `1/raw-tuples`
      in their place, and it is the REMOVAL that stops those clauses being
      charged a second time by eqjoinsel. On a composite key the difference is
      not cosmetic: the per-column marginals price the join at
      `1/nd_a · 1/nd_b`, on the test's `partsupp` shape 2.5e6× tighter than the
      `1/800000` the key implies — the compounding P5.6 exists to end.
      **Second finding (the asymmetry that is easy to get backwards):** a
      UNIQUE index makes its OWN relation the key side, but a declared FK makes
      its relation the CHILD, so the divisor is the PARENT's raw count
      (`1.0/ref_tuples`, costsize.c:5847). The legacy `uniqueNoFanoutRawCount`
      divides by whichever table carried the constraint, which on a
      fact-to-dimension join divides the fact table's own cardinality out of
      the estimate; ledgered rather than fixed, because that estimator belongs
      to the other cost model and P6.3 deletes it. Three further upstream
      properties reproduced deliberately: the RAW (not filtered) divisor,
      whole-key cover (`⊆` tests the KEY's columns — extra equated columns stay
      residual and are charged on top), and a clause consumed at most once.
      4 ledger rows (joinrel width = sum of input widths vs
      `build_joinrel_tlist`; `vardata->isunique` still unset in
      `examineJoinVar`; the legacy child-divisor defect; the `nconst_ec`
      correction). 12 tests (`joinrelsize_test.go`). Still inert
      (`GOOPG_PGSHAPED_DP` OFF, no production caller).
      IMPLEMENTATION-TODO P5.6-b; 04 §2, §3.1. Bar met: UNITS. DS05 + SPOT +
      PLAN not applicable — no production caller exists, so no plan can move.
- [x] **M0127-P5.6-c — the clamp discipline** (04 §3.3): the FK-implied bound
      when a validated FK covers the join, with M0126-0010's `max(l,r)` cap
      kept beside it for the non-FK fallback. Landed 2026-08-04 as
      `keyImpliedRowsBound` + two clamps in `calcJoinrelSize`
      (`internal/planner/joinrelsize.go`).
      The two clamps are deliberately different in kind. The key-implied bound
      is a COUNTING argument — a proven key means each row of the other side
      matches at most one row of the key side, so the output cannot exceed the
      other side's rows, whatever the selectivities multiplied out to — and it
      is therefore generalised beyond declared FKs to every key P5.6-b's
      superkey pass proves. It is taken only when the key relation is the WHOLE
      of its side: inside a multi-relation input a lower join may already have
      duplicated its rows, and the counting argument then gives nothing
      (ledgered). With consistent inputs the product lands exactly ON the
      bound, so what the clamp catches is the two disagreeing — a key side
      whose row estimate has outgrown its ANALYZE-time raw count divides by a
      tenth of what it will read and claims a 10× fan-out from a join that
      cannot fan out at all.
      The `max(l,r)` cap is a heuristic with NO upstream counterpart
      (`calc_joinrel_size_estimate` never bounds an inner join by its inputs),
      so it fires only where M0126-0010's does: nothing proven AND every
      residual clause priced by a selfuncs.h constant. That condition is PG's
      `*isdefault` finally consumed — `eqJoinSelectivityExt` reports the flag
      of the side whose ndistinct actually divided, not the disjunction, so an
      equality against one unanalysed operand is still a measurement when the
      other side's million distinct values won the denominator. Capping a
      measured estimate would truncate genuine many-to-many joins; capping a
      clause-less join would truncate a cross product.
      `superkeyJoinSelectivity` now returns a `superkeyEstimate` (sel,
      residual, fired, rowsBound) because "a key fired" cannot be recovered
      from the selectivity afterwards. 7 new tests, incl. the two that pin the
      soundness restriction and the not-capped cases. Still inert
      (`GOOPG_PGSHAPED_DP` OFF, no production caller). 2 ledger rows: the
      multi-rel key side, and the cap's divergence from upstream (it dies with
      eqjoinsel's MCV arm). IMPLEMENTATION-TODO P5.6-c; 04 §3.3.
      Bar met: UNITS. DS05 + SPOT + PLAN not applicable — no plan can move
      while the sizer has no production caller.
- [x] **M0127-P5.6-d — delete the quadratic build penalty** (bushy.go:632),
      once 04 §4's honest batch-I/O term prices what it stood in for.
      IMPLEMENTATION-TODO P5.6-d. Bar: UNITS + DS05.
      **UNBLOCKED 2026-08-05 by M0127-P5.7-a**: the honest term now exists and
      the penalty's sibling inside `hashJoinCost` (M0126-0013's
      `seq_page_cost × innerRows/100`) is already gone. What remains at
      `costJoinCandidate` is the separate `largeBuildThreshold` quadratic
      overshoot, which the `NBatch > 1` charge now covers — and covers better,
      since 2 M rows is a fixed row count while the real threshold depends on
      the row's width. Note when doing it: the penalty lives on the
      `costDrivenJoinOrder` arm, so DS05 will show no movement unless the sweep
      runs with that flag ON.
      **DONE 2026-08-05.** The block is gone; `costJoinCandidate`'s hash cost is
      now exactly `hashJoinCost`. The width point above is the measured one:
      against the 512 MB default budget a 4 M-row 1-column build FITS and the
      row-count form charged it 40 000, while a 1 M-row 40-column build spills
      to 4 batches and it charged nothing. `TestCostJoinCandidateLargeBuildPressure`
      is replaced by `TestCostJoinCandidateHasNoRowCountPenalty` (equality with
      the bare cost function — fails if a penalty is re-added) and
      `TestCostJoinCandidateStillDetersHugeBuilds` (the defence survives via the
      spill term). No deferral row: upstream has no such penalty, so nothing PG
      does is left unimplemented. Bar met: UNITS + SPOT. DS05 not run — as the
      note predicted, the arm is OFF by default, so the default planner is
      byte-identical; 04 §4.2 records the same scope note for benchmark readers.
- [x] **M0127-P5.6-e-i — the estimate-audit INSTRUMENT + pre-flip baseline**
      (09 §5.1/§5.2): `cmd/estimate-audit` + `internal/estimateaudit`. One
      `EXPLAIN ANALYZE` per query supplies both sides (cost `rows=` and
      `actual rows=`); the unit of audit is the JOINREL, not the node; the
      binary exits non-zero on a violation, so it is instrument and tripwire
      in one. **The finding that shaped it: the audit was unrunnable on the
      query 09 §5 names.** goopg does not propagate worker instrumentation
      out of a `Gather` (upstream merges it in `execParallel.c`
      `ExecParallelRetrieveInstrumentation`), and Q9 plans entirely below
      one — the first run measured `(no ANALYZE)` for every joinrel of 10 of
      12 queries. Hence `--serial`. Two further conditions are load-bearing:
      per-connection ANALYZE stats (`--warm-stats`, ONE session for the whole
      run — a pooled connection measures the blind planner) and goopg's
      CUMULATIVE `actual rows=` where PG prints the per-loop average (a
      PG-calibrated reader multiplying by `loops` inflates every nested-loop
      inner node by exactly the loop count). Baseline (legacy planner, all 22
      queries, 12 min): five joinrels over 10³, worst Q18's final SEMI at
      2.5 × 10⁷ over; **Q9's final joinrel 124.7× over** — just outside §5's
      ≤ 10² bar, its three outermost joinrels all carrying the same estimate
      while the actual collapses 19× across them. Output committed under
      `analysis/leftdeep-joins/` (+ a provenance README). 12 tests. 4 ledger
      rows. IMPLEMENTATION-TODO P5.6-e-i; 09 §5.1/§5.2. Bar met: UNITS + the
      audit run. DS05 not applicable — new tool + new package with no
      importer in the engine; zero planner/executor lines changed.
- [x] **M0127-P5.6-e-ii — close the two class-(a) causes the baseline
      isolated** (09 §5.2): a SEMI/ANTI joinrel priced at its outer input
      verbatim (`calc_joinrel_size_estimate`'s JOIN_SEMI arm, costsize.c —
      Q18/Q20/Q22), and a joinrel's non-equi restriction contributing no
      selectivity (Q19's three-branch OR, Q3's re-applied `Filter:`); then
      re-run the audit. Q9's ≤ 10² bar is P5.9's to certify on the post-flip
      planner. Landed in `internal/planner/cardinality.go` +
      `selectivity.go`: the JOIN_SEMI/JOIN_ANTI arms with `eqjoinsel_semi`'s
      no-MCV match fraction (incl. its asymmetric nd2-to-inner-rows clamp),
      and `clauseSelectivity` over the conjuncts `HashKeys` does not answer —
      BOTH-sided ones only, since a single-sided conjunct is a
      baserestrictinfo already priced into the component rel even though
      goopg leaves a copy on the join. Audit: Q19 328 705× → 13.1× under,
      Q20-final 891× → 9.5× under, Q21 499× → 9.7× under, Q22 643× → 1.8×,
      Q4 485× → 7.3×; Q18 halved; **Q9 unchanged at 124.7×**; no new
      violations (09 §5.3, `analysis/leftdeep-joins/2026-08-04-p56eii*`).
      **The finding that shaped the scope:** the join-key ndistinct lookup is
      ALSO wrong (`RightKey.Index` is a MERGED index resolved against the
      right child's own schema, so the right side of an equi-join never
      entered `max(nd)`), and correcting it was measured as a large net
      REGRESSION — Q9's final 124.7× → 176 424× over — because a saturated
      ANALYZE ndistinct compounds and because supplying `nd` removes the
      M0126-0010 cap. Spun out as P5.6-e-iii with the rejected run kept as
      evidence. IMPLEMENTATION-TODO P5.6-e-ii. Bar met: UNITS + DS05
      (PASS=94/MISMATCH=0, identical to the two prior sweeps) + SPOT
      (Q12=2, Q13=35) + the audit run.
- [x] **M0127-P5.6-e-iii — de-saturate ANALYZE's ndistinct, then fix the
      join-key coordinate space.** Haas–Stokes in `compute_distinct_stats`
      terms (`internal/executor/operators_analyze.go`, upstream analyze.c):
      goopg stores the SAMPLE's distinct count, so a 1.5 M-row unique key
      reads as ≈ 30 000 and every join above it divides by a number 50× too
      small. Only then resolve `LeftKey`/`RightKey` in the merged left‖right
      space and give `columnNDistinctForChild` its `*Join` arm (its
      `columnStatsForChild` twin already has one — the divergence is
      deliberate and commented at both ends). Re-examine the M0126-0010 cap
      in the same loop: it bounds a join at max(|l|,|r|) only on the
      nd-unavailable path, so it silently disappears the moment nd resolves.
      Evidence: `analysis/leftdeep-joins/2026-08-04-p56eii-postfix.txt`,
      09 §5.3. IMPLEMENTATION-TODO P5.6-e-iii. Bar: UNITS + DS05 + audit run.
      **DONE 2026-08-04.** Landed: `executor.ndistinctEstimate` (mirrors
      `compute_scalar_stats`'s three ndistinct branches, analyze.c:2588-2648);
      `NDistinct`/`NDistinctFrac` are now two renderings of ONE estimate and
      `StaDistinct()` picks between them with upstream's 10%-of-rows rule;
      `estimateJoin`'s equi arm reads the right key through
      `rightKeyNDistinct`; `columnNDistinctForChild` gained its `*Join` arm and
      the divergence tripwire is retired. Cap re-examined and deliberately
      left fallback-only (it stands in for upstream's FK `fkselec`; a real
      many-to-many join legitimately exceeds max(|l|,|r|)). Audit violations
      **5 → 2** — Q3 2967× → 10.4×, Q5 447× → 1.5×, Q7 1190× under → 1.4×,
      Q8 20.7× → 1.3×, Q17 7.5× → 1.0×, Q18 1.26e7× → 42837×, Q20-inner
      1311× → 129×. Two regressions FILED, not papered over (09 §5.4 + three
      ledger rows): SEMI/ANTI collapse to `est=1` (Q21 final ANTI is a new
      violation, 9.7× → 4003× under; needs `eqjoinsel_semi`'s MCV arm) and
      **Q9 UNMEASURED** (93.9 s → >150 s). Bar met: UNITS + DS05 (PASS=94/
      MISMATCH=0/CKMISMATCH=0, identical to the three prior sweeps) + SPOT
      (Q12=2, Q13=35) + the audit run.
- [x] **M0127-P5.6-f-pre — the FK/unique evidence P5.6-f needs does not
      survive a restart (found + fixed 2026-08-04).** P5.6-f's second half
      reads a UNIQUE index or a declared FK; goopg's TPC-H bench cluster
      (db `tpch`) has **0 of each** against the PG 18.3 reference's **16
      indexes and 8 constraints**. Root cause is a composed regression, not a
      load failure: 4e follow-up 39 deferred index rows on the ground that
      `RecordKindCreateIndex(20)`/`DropIndex(21)` still carried them, and B5
      Slice A later retired 20/21 on the premise that
      `loadUserIndexesFromHeap` had replaced them — but that reload scans ONE
      database, and the write went to a different one, so `CREATE INDEX` /
      `PRIMARY KEY` / `ADD CONSTRAINT` on any non-default database was durable
      nowhere. Landed: per-DB routing on both sides + `Index.DBOid` on the
      recovery path (without which the index came back as metadata that no
      longer ENFORCED — a UNIQUE index accepting duplicates after restart).
      `TestDistinctDatabaseIndexSurvivesRestartInOwnNamespace`; design
      `0122-0018` new section; ledger row dated 2026-08-04. Bar met: UNITS +
      SPOT (Q12=2, Q13=35) + DS05 + the four affected packages.
      **Forward-only — see P5.6-f's step 0.**
- [x] **M0127-P5.6-f — multi-key equi-join pricing + `fkselec`, the two
      halves that must land together (DONE 2026-08-04).** Q9's
      `l_suppkey = ps_suppkey AND l_partkey = ps_partkey` was priced on ONE
      pair while `Join.Residual()` excluded BOTH. Landed together, as the item
      required: `joinEquiPairs` folded over every pair AND the same list
      excluded by `joinResidualSelectivity`; plus
      `get_foreign_key_join_selectivity` (costsize.c:5651) for the legacy
      estimator in `internal/planner/joinkeyproof.go`, reading uniqueness
      evidence stamped on the leaf scans (`SeqScan/IndexScan.UniqueKeys`,
      stamped where `SmallDim` already is, through the planner's own `cat`).
      Step 0 was done FIRST and is recorded: the eight UNIQUE indexes the PG
      reference declares were re-created on the bench cluster (they survive a
      restart — first real-cluster validation of the P5.6-f-pre fix) and the
      audit was RE-BASELINED on them
      (`analysis/leftdeep-joins/2026-08-04-p56f-baseline-idx.txt`), which
      reports the identical two violations as the index-free `p56eiii` run —
      so the whole delta is the planner change. Result: violations 2 → 2,
      **no joinrel worse**, Q9's target joinrel 479 779 280 (80× over) →
      **5 997 241, exact**; Q20 d3 12.2× → 3.1× over, d2 SEMI 125× → 31.7×
      over. 09 §5.5; three ledger rows dated 2026-08-04. Bar met: UNITS +
      SPOT (Q12=2, Q13=35) + DS05 + the re-baselined audit run.
      **Q9 is measurable again — at 291.8 s, not inside the audit's 150 s.**
      Its cardinality defect is closed; the residue is class (b) and is
      P5.6-f-ii below.
- [x] **M0127-P5.6-f-ii — the legacy join-order SEARCH does not use
      `estimateJoin`, so P5.6-f never reached plan shape.** Measured, not
      inferred: with Q9's joinrel now EXACT its plan is byte-identical, still
      applying the 5.3 %-selective `part` filter ABOVE three hash joins that
      each carry the full 5 997 241 rows (PG filters `part` first and index-
      scans lineitem via `lineitem_part_supp_fkidx`). The cause is a second
      cardinality implementation: `estimateJoinCost` (bushy.go:1257). Its
      PRODUCTION branch — the integer DP, `costDrivenJoinOrder` OFF — computes
      `ndv` as the maximum NDistinct over EVERY column of the edge's two
      tables, ignoring the join key entirely; the multi-edge enumeration and
      superkey probe beside it (`crossEdgesBetween` +
      `uniqueNoFanoutRawCount`) are gated on the flag M0126 closed as a no-go
      and left OFF, and that probe's FK arm divides by the CHILD's raw count
      where upstream divides by the PARENT's (costsize.c:5847). Resume point:
      enumerate `crossEdgesBetween` unconditionally and run a
      `joinkeyproof.go`-shaped prover over `g.tables` (the catalog is already
      in scope there). Ledger row dated 2026-08-04. Bar: UNITS + DS05 +
      PLAN + audit run with Q9 inside the 150 s timeout.
      **DONE 2026-08-05.** The named cause was real and NOT sufficient; two
      more were found by instrumenting the DP, and all three had to land
      together. (1) `graphJoinKeyDivisor` (joinkeyproof.go) is
      `superkeyJoinEstimate`'s algorithm arm-for-arm in the join-GRAPH
      coordinate space, and now feeds BOTH search modes — `uniqueNoFanoutRawCount`
      is deleted with its child-vs-parent FK inversion. (2) **A `joinEdge`'s key
      `ColumnRef.Index` is in GLOBAL FROM-list coordinates** (Q5's `c_nationkey`
      is `Index: 16` against an 8-column `customer`), so `accurateKeyDistinct`
      returned 0 for every join key in the query and `sideKeyDistinct` served
      the table-wide max instead — or, in range by accident, answered with
      `n_comment` for `n_nationkey`. Third instance of the P5.6-e-ii `RightKey`
      class; fixed by resolving through the NAME (`edgeColName` preference
      inverted, `tableColumnIndex` added). (3) `accurateKeyDistinct` now renders
      through `StaDistinct()` rather than multiplying `NDistinctFrac`
      unconditionally. **The rejected half-fix is kept as evidence**
      (`2026-08-05-p56fii-halfway.txt`): adding only the proof made Q5's
      `lineitem ⋈ supplier` truthful while its rival `customer ⋈ supplier` kept
      reading 10 000 against 60 000 000, the DP took the cartesian product, and
      Q5 went 65.9 s → over the timeout. A search selects on comparisons, so a
      partially truthful estimator is a new defect, not half a fix. Result:
      violations 2 → 2, no joinrel worse, **Q9 UNMEASURED (>150 s) → 6.3× over**
      (inside its ≤100× override) and **291.8 s → 16.6 s**; **zero runtime
      regressions** over 22 queries, Q5 65.9→17.1 s, Q7 38.9→27.2 s,
      Q21 125.1→90.5 s, stream total 546.8→445.1 s (0.81×). 09 §5.6; two ledger
      rows dated 2026-08-05. Bar met: UNITS + SPOT (Q12=2, Q13=35) + DS05
      (PASS=94/MISMATCH=0/CKMISMATCH=0/ERROR=0/TIMEOUT=1 Q47/SKIP=4, summary
      identical to the four prior sweeps) + PLAN (19/22 diverged as intended,
      re-pinned to `plan_snapshots/m0127-p56fii.txt`, then 22/22 MATCH) + the
      audit run. **P5.9 can now certify Q9's ≤10² runtime bar as well as its
      cardinality.**
- [x] **M0127-P5.6-f-iii — the TPC-DS SF0.5 gate's single TIMEOUT hopped
      from Q72 to Q47 (2026-08-04), unattributed.** The sweep's summary line
      is identical to the three before it (PASS=94, MISMATCH=0, CKMISMATCH=0,
      ERROR=0, TIMEOUT=1, SKIP=4) — correctness did not move — but Q47 went
      31 s → 332 s timeout while Q72 went 328 s timeout → 166 s, Q57 15 s →
      81 s and Q53 28 s → 6 s. Swings in both directions plus a hopping
      victim match the documented sweep-tail confound (a server that just ran
      a timeout query sits at GOMEMLIMIT with GOGC=off and thrashes). Checked,
      not assumed: Q47's only multi-pair join is the 5-pair `v1 ⋈ v1` between
      two CTE scans whose ndistinct resolves on NEITHER side, so it lands on
      the same `defaultEqSelectivity` fallback as before (`EXPLAIN` shows
      `rows=1`); every other joinrel is single-pair and unchanged by
      construction. Resume: run the sweep twice at this commit and once at
      `HEAD~1` and see whether the TIMEOUT hops without a code change.
      Ledger row dated 2026-08-04.
      **↳ ANSWERED 2026-08-05 — the confound hypothesis is REFUTED and the
      filed "checked, not assumed" paragraph above is WRONG on its own terms.**
      The TIMEOUT does not hop without a code change: it was moved by
      **`ce027cee` (P5.6-f)**. Answered without the three ~1 h sweeps the
      resume point asked for. (1) *Step function, not noise*: eight consecutive
      sweeps hold the old regime, four hold the new, spread ±3 s. (2) *The
      confound cannot reach Q47*: it runs at position 47, BEFORE Q72 — in the
      old regime no timeout had yet occurred, in the new one Q47 is itself the
      first — and a fresher post-restart server cannot explain Q57 getting 5×
      SLOWER. (3) *Solo, quiet host, `TIMEOUT_SEC=900`*: **Q47 523 s**, Q57
      81 s, both reproducing the new regime outside any sweep tail (the
      hypothesis predicted ≈31 s). (4) *Bisect on a 2.3 G COPY of the cluster*
      so the live dir was never at risk: `30293f78` 31 s, `29daeb72` 30 s with
      a **byte-identical plan**, HEAD 523 s — and the old binary on TODAY's
      data is fast, exonerating the cluster data too. `29daeb72..ce027cee` is
      exactly one commit. The boundary sweep is labelled `29daeb72` only
      because its header says `[tree DIRTY in Go sources]` / `diff=129e691bd41a`
      — that binary was `29daeb72` + uncommitted P5.6-f WIP. **Read the `diff=`
      field before the commit subject when attributing a sweep.**
      **Mechanism**: Q47's outermost join has FIVE equi-pairs; P5.6-f folds
      every pair under independence (`cardinality.go:457-483`,
      `sel /= pairNDistinct` multiplied across pairs), two of them strongly
      correlated (`i_category`↔`i_brand`, `s_store_name`↔`s_company_name`), so
      the joinrel collapses and the plan degrades from a 5-pair **Hash Join**
      to a **Nested Loop with no join condition**. The filed claim that this
      join "lands on the same `defaultEqSelectivity` fallback as before" is
      exactly what the plan diff disproves. **P5.6-f stays** — net win (+Q72
      timeout→166 s, +Q53 28→6 s, +Q9's exact joinrel), correctness never moved
      (`PASS=94 MISMATCH=0 CKMISMATCH=0`, Q47 returns its 100 oracle rows in
      every regime); what it lacks is PG's correlation defence. Successor
      **M0127-P5.6-f-iv**. `analysis/m0127-p56fiii/README.md`; 09 §5.15;
      1 ledger row dated 2026-08-05.
- [x] **M0127-P5.6-f-iv — REFUTED as filed: PG has no functional-dependency arm
      for JOIN clauses.** The item asked for a correlation damper on
      `internal/planner/cardinality.go:465-483`, citing
      `dependencies_clauselist_selectivity` / `statext_clauselist_selectivity`.
      Checked against the oracle, that citation does not reach join clauses:
      `clauselist_selectivity_ext` (clausesel.c) gates the whole
      extended-statistics branch on `find_single_rel_for_clauses`, which returns
      `NULL` as soon as any clause carries two relids — so the dependency arm
      **never runs on a join clause list** in any PG that has this gate.
      Extended statistics are a *restriction*-clause mechanism upstream.
      Measured confirmation: plain `EXPLAIN` of `query47.sql` against the PG
      18.3 SF0.5 oracle (:65438) estimates **both** correlated 5-pair joins at
      `rows=1` — PG collapses them exactly as goopg does. Implementing the item
      would have added a non-PG heuristic under an upstream citation.
      **What actually differs is the size of the join's INPUTS**: `CTE Scan on
      v1` is 7 643 in PG and **18** in goopg, so PG refuses the nested loop
      (rescanning 7 643 rows is expensive) where goopg accepts it. That 425×
      under-estimate predates P5.6-f — `30293f78` carries the same 18 and still
      picks the Hash Join — so P5.6-f only tipped an already-mispriced
      comparison. Isolated to a **pushed-down restriction being charged a second
      time at the join above it** (five-row probe table in the notes: no
      restriction ⇒ factor 1.0; any restriction ⇒ the factor the scan already
      applied). Doc 09 **§5.17** + a correction box on §5.15;
      `analysis/m0127-p56fiv/README.md`; §6 of `analysis/m0127-p56fiii/README.md`
      retracted in place; 2 ledger rows. Successors **P5.6-f-vi** / **-f-vii**
      below. Bar: UNITS (no Go source changed).
- [x] **M0127-P5.6-f-vi — a pushed-down restriction is priced twice.** The
      real Q47 defect (§5.17). A join above a filtered scan is multiplied by the
      selectivity the scan **already applied**: on SF0.5, the row-preserving
      `store_sales ⋈ store` (unique `s_store_sk`, 12 rows) returns its left
      input unchanged with no `date_dim` restriction present (1 439 608 →
      1 439 608) and is scaled by ≈1/205 with `d_year = 2000`, ≈1/205 with
      `d_dom = 15`, ≈0.505 with `d_year > 1999` — each time the scan's own
      factor. PG does not (2 583 → 2 465). This is the double-count
      `joinResidualSelectivity`'s header says it prevents, and it under-sizes
      **every join above every filtered scan**, not just Q47's.
      Already ruled out: `exprSide` is correct in isolation (`col = const` →
      `sideLeft`, `col = col` → `sideMixed`), so the residual guard as written is
      not the leak. Resume: `internal/planner/cardinality.go` `estimateJoin`'s
      pair loop — check whether `joinEquiPairs` →
      `splitAllEqualitiesForHash` admits a `col = const` conjunct as an
      equi-pair (note the `d_year > 1999` row is neither `1/nd` nor
      `defaultEqSelectivity`, so that arm alone cannot explain every row).
      Instrument, and the acceptance test: a planner unit test building a join
      whose left input is a `*Filter` over a scan with the same conjunct still
      in `Predicate`, asserting the estimate equals the unfiltered estimate
      scaled **once**. Bar: UNITS + the estimate audit + DS05 (named-victim
      TIMEOUT-set diff, not a `TIMEOUT=` count) — this moves plan shape broadly,
      so capture `plans` before and after.
      **LANDED 2026-08-05.** The resume point above was wrong and is corrected
      in doc 09 **§5.18**: `estimateJoin` returns the CORRECT 726 987 for the
      probe's `store` join (server instrumentation), so its pair loop,
      `splitAllEqualitiesForHash` and the residual guard are all exonerated.
      The real node is one higher — `pushInnerJoinInputQuals`
      (`inner_join_qual_pushdown.go`) DUPLICATES a single-relation restriction
      onto its relation and leaves `f.Predicate` untouched ("property 2"), so
      the estimator charged the same conjunct at the leaf Filter AND at the
      residual Filter above the join. Fix: `Filter.PushedBelow` records the
      duplication and `filterSelectivity` (`cardinality.go`) skips those
      conjuncts — upstream needs no such list because
      `distribute_restrictinfo_to_rels` MOVES the clause. Both duplicating
      passes stamped (binary + MHJ arms); the two moving siblings checked and
      left alone. Measured: the probe's `store` join is row-preserving in all
      three regimes (was ×1/205 and ×0.505); Q47's `v1` 18 → 3 626 vs PG's
      7 643. Gates: UNITS green; DS05 sweep `sweep-20260805-101345.txt` exit 0,
      per-query verdicts BYTE-IDENTICAL to the `f05b5329` baseline, named
      TIMEOUT set still exactly `{Q47}` — **50 of 99 plan shapes changed** (the
      previous sweep changed 0), so the blast radius is real and cost 0
      verdicts. Estimate audit
      `analysis/leftdeep-joins/2026-08-05-p56fvi-postfix.txt`: no new
      violation, per-joinrel moves ±1–4 %, and the corpus's one standing
      violation (Q18's final SEMI) IMPROVED 25 182× → 23 433×. That check was
      load-bearing, not a formality — the fix removes a downward correction, so
      every affected estimate moves UP into a corpus already dominated by
      over-estimates. It did NOT un-time-out Q47; that stays open under -f-vii
      and the successor below. New tests
      `internal/planner/cardinality_pusheddown_test.go` (3).
- [x] **M0127-P5.6-f-viii — Q47 still takes the nested loop with `v1` at
      3 626.** CLOSED 2026-08-05 by **M0127-P5.6-f-vii**, at the resume point
      this item named. The `CTE Scan on v1 rows=6` outer was 6 because the
      `v1` body's 6-key GROUP BY was estimated `child/2` = 3 626 rather than
      7 252; with `estimate_num_groups` the body is 7 252 (PG 7 643), the outer
      is 12, and the `Nested Loop (INNER) rows=1958` is now a `Hash Join
      (INNER, build=left) rows=7252`. **Q47 completes in 12 s** (was TIMEOUT at
      300 s), 100 rows against the PG oracle, and the DS05 named TIMEOUT set is
      EMPTY for the first time since §5.15. No rescan-cost term was needed —
      the alternative hypothesis in this item ("the rescan is unpriced") was
      not reached and stays unmeasured; doc 09 §5.19. Original text below.
      With -f-vi landed the 425× under-estimate is gone (18 → 3 626
      against PG's 7 643) and Q47 *still* TIMEOUTs at 300 s: the top block
      plans `Nested Loop (INNER) rows=1958` over a 5-pair `Hash Join rows=108`
      and a `CTE Scan on v1 v1_lag rows=3626`, where PG picks a Merge Join.
      So the plan flip is NOT wholly downstream of the `v1` size — doc 09
      §5.17's chain was necessary, not sufficient (§5.18 closing paragraph).
      Resume: the `CTE Scan on v1 rows=6` outer (post-`d_year = 2000 AND
      avg_monthly_sales > 0` filter) is what makes rescanning 3 626 rows look
      free; price the rescan, or check whether that outer's own 6 is a second
      instance of the same double-charge class through a `*CTEScan`. Do
      -f-vii first — one variable per measurement, per §6. Bar: UNITS + the
      estimate audit + DS05 (named-victim TIMEOUT-set diff).
- [x] **M0127-P5.6-f-vii — `estimateAggregate` is `child/2`, upstream is
      `estimate_num_groups`.** LANDED 2026-08-05 (doc 09 §5.19).
      `estimateNumGroups` (`internal/planner/cardinality.go`) is the port of
      `estimate_num_groups` (selfuncs.c:3449): unique variables per grouping
      expression, per-relation product of ndistinct clamped to that relation's
      tuples (÷10 above one variable, floored at the largest single ndistinct),
      the Yao/Dell'Era restriction correction (new `relFilteredRows` recovers
      upstream's `rel->rows` from the plan tree), the product across relations,
      and the closing clamp to `input_rows` — which is what the old `child/2`
      was a crude stand-in for. Also ports `get_variable_numdistinct`'s
      no-stats tail and `clamp_row_est`. The item was filed as explicitly NOT
      load-bearing for Q47 and it is what **closed Q47** (see -f-viii). Gates:
      UNITS green; estimate audit `2026-08-05-p56fvii.txt` — no new violation,
      all TPC-H joinrels <1 % except Q20 which IMPROVED (30.2× → 24.9× over),
      Q18's standing violation 23 433× → 23 015×; DS05 sweep
      `sweep-20260805-112902.txt` exit 0, **TIMEOUT=0**, PASS 94 → 95,
      MISMATCH=0 CKMISMATCH=0 ERROR=0. 10 new tests
      (`internal/planner/cardinality_numgroups_test.go`). Four upstream
      refinements ledgered, not faked: EC de-duplication (step 3), extended-stats
      multivariate ndistinct, the boolean short-circuit, the volatile arm — plus
      the standing sibling gap on `estimateSetOp` / `*Distinct` / `*DistinctOn`,
      which still run no group estimate.
- [x] **M0127-P5.6-f-v — the DS05 sweep must diff the TIMEOUT SET, not its
      cardinality.** §5.15's regression survived four sweeps because
      `TIMEOUT=1` is invariant to WHICH query timed out; the summary line was
      byte-identical across a 17× re-pricing. P5.6-g-i-b gave the gate a
      plan-shape channel, which covers plan drift but is non-blocking and was
      not read per-query either.
      **LANDED 2026-08-05.** `scripts/tpcds-sweep-diff.py` + a `delta [OLD
      [NEW]]` subcommand + a `sweep` tail stage directly under the SUMMARY
      (before the slower plan pass): the per-query status/runtime vector is
      diffed against the previous **full** report and printed by name —
      `TIMEOUT +Q47 -Q72`, `SLOWER Q57 15s->81s (5.4x)`. **The input is the
      sweep report itself**, in the format `cmd_sweep` already prints, so no
      new artefact exists and all ~90 archived reports are valid baselines
      retroactively. Four limits, printed in the channel's header every run
      (never silent): both arms compare the INTERSECTION (a query absent from
      one side is named once as ONLY-OLD/ONLY-NEW, not counted as leaving the
      PASS set); TIMEOUT readings are the CAP and are excluded from the runtime
      arm (the verdict arm already names them); a runtime move needs ≥2× AND
      ≥5 s on the larger side (integer seconds make 1s→3s "3×"); and the
      default baseline SKIPS subset probes, which are stamped "NOT a gate
      result" and would compare 3 queries while staying silent about 96.
      Non-blocking like the plan channel — performance still cannot fail this
      gate. **Validated by replay over all 87 adjacent pairs of the archived
      corpus** (zero crashes, 17 pairs report a verdict change); on the pair
      whose SUMMARY lines are byte-identical it prints `TIMEOUT +Q47 -Q72`,
      `PASS +Q72 -Q47` and `SLOWER Q57 15s->81s (5.4x)` — both §5.15 victims,
      from artefacts that already existed the night it landed. Not filed as a
      deferral (no PG behaviour involved), but two limits are stated in 09
      §5.16: it compares ADJACENT runs, so sub-2×-per-run creep stays unnamed,
      and it still cannot FAIL the gate on a traded timeout — that needs a
      curated per-query budget file, deliberately not filed while the timeout
      set is still moving under active planner work. 09 §5.16;
      `bench/tpcds/README.md`. Gates: UNITS green (cached; no Go source
      changed) + one full DS05 sweep with the channel live
      (`sweep-20260805-090258`: PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=1 SKIP=4, delta `verdict-changes=none`, one runtime move Q83
      7s→3s — the correct reading for a harness-only commit).
- [x] **M0127-P5.6-g — `eqjoinsel_semi`'s MCV arm + the `(1 - nullfrac1)`
      factor.** Both landed verbatim from selfuncs.c in
      `internal/planner/cardinality.go`: the matched-MCV frequency mass is
      exact, the nd heuristic prices only the uncertain remainder with both
      distinct counts discounted by the match count, the inner list is
      truncated to the clamped `nd2`, each inner MCV is consumed once, and
      `CLAMP_PROBABILITY` guards both terms. Statistics are read through
      `resolveBaseColumn` (which now publishes the whole `*catalog.ColumnStats`)
      rather than `columnStatsForChild`, so the match fraction cannot depend on
      which scan the planner picked. 13 tests.
      **Measured NO-OP on TPC-H** — violations 2 → 2, both bit-identical; every
      other joinrel moved under 5 % in both directions on INNER joins this
      change cannot reach, which is ANALYZE's sampling noise and is now the
      documented noise floor for the instrument. A no-op is indistinguishable
      from a broken wire, so both halves were proven on real data with the same
      binary: **20 → 5 010 against an actual 5 010** once the inner has an MCV
      list (uniform data gets none — upstream discards a list whose values are
      not more common than average), and **1 000 → 750 against an actual 750**
      at 25 % outer nulls. TPC-H's near-uniform surrogate keys and NOT NULL
      join columns cannot exercise either.
      **The finding that reframes the milestone: the premise was wrong, and the
      oracle says so.** Both violations were re-measured on the PG 18.3
      reference. Q21's ANTI — **PG estimates `rows=1` too** against the same
      actual 4003: `neqjoinsel` returns `1 - nullfrac` for SEMI/ANTI by
      documented design, and the eq clause is a self-join on
      `lineitem.l_orderkey` so `nd1 = nd2` in every branch including the new
      one. Closing it means diverging from PG; it is an audit override, not an
      estimator task. Q18's SEMI is a real divergence of another mechanism —
      PG dedups the `GROUP BY`-unique subquery to an INNER join (117 159 est)
      while goopg keeps the SEMI and lands on the **0.5 punt** (2 998 620 =
      exactly half the outer) because `resolveBaseColumn` has no
      `*HashAggregate` arm. And at 1 674× on Q18 **PG itself trips the audit's
      1 000× tripwire**, so the absolute factor is the wrong acceptance bar for
      a PG-parity milestone. Q19's `d1` is an INNER join and was never this
      item's; Q16 moved 1397 → 1401 (noise). Successors P5.6-g-ii / -g-iii.
      2 ledger rows. IMPLEMENTATION-TODO P5.6-g; 09 §5.7.
      Bar met: UNITS + SPOT (Q12=2, Q13=35) + audit run + the two real-data
      probes. **DS05 NOT RUN** — the gate self-refuses while the nightly CI
      batch holds the host (`FATAL: the nightly CI batch is running`), and the
      batch was mid-run with a wedged testport stage for this loop's whole
      duration. Carried in `.ralph/working_set.md` with the exact command.
      **↳ DISCHARGED 2026-08-05 by M0127-P5.6-g-i**: no row/checksum change,
      and this commit moves exactly ONE TPC-DS plan (Q83) — the nullable-key
      premise for the carry is measured false (09 §5.10).
- [x] **M0127-P5.6-g-i — run the DS05 gate for P5.6-g *and* P5.6-g-iv.**
      DONE 2026-08-05, on a host the nightly batch had cleared (05:18). The
      sweep owed **three** commits a gate, not two: the last DS05 baseline is
      `ce027cee` (P5.6-f) and `4b820ab8` (P5.6-f-ii) landed after it, ungated.
      **Gate: `PASS=94 (57 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=1 SKIP=4` — identical to the baseline line for line**, including
      every value checksum and the single Q47 TIMEOUT. Total seconds 1828 →
      1788, NOT claimed (the baseline's back half ran under the nightly).
      Then a whole-corpus `EXPLAIN` A/B/C/D at `ce027cee` → `4b820ab8` →
      `8ce056ff` → `f8338a09`, S-cold, noise floor measured at zero (same
      binary twice = byte-identical plans for all 99): **P5.6-f-ii moved 74 of
      99 plans, P5.6-g moved 1 (Q83), P5.6-g-iv moved 4 (Q13, Q41, Q48, Q85)**;
      75 net, nothing changed and changed back. **The premise that raised this
      item is measured false** — `(1 - nullfrac1)` + the MCV arm move estimates
      but almost never the search's choice; the corpus-wide re-ordering belongs
      to P5.6-f-ii, the commit that taught the search to read the join key at
      all, and 74 plans moving with zero rows moving is the strongest evidence
      available that it is a re-ordering and not a semantic change. Q41 is
      `canonicalizeQual`'s pure case (`(A∧X)∨(A∧Y)` → `A∧(X∨Y)` in one filter);
      Q13 is the load-bearing one (three join clauses + `ca_country` hoisted
      out of all three OR arms → hash joins where a nested loop carried the
      disjunction, PG's own Q13 shape). 27 of 99 texts contain an OR vs 2 in
      all of TPC-H. Evidence `analysis/leftdeep-joins/2026-08-05-p56gi-*`
      (README + both sweep reports + four corpus plan captures + the capture
      script); doc 09 §5.10; IMPLEMENTATION-TODO P5.6-g-i. No ledger row: this
      loop implemented nothing and left no PG behaviour unimplemented.
      Successor **M0127-P5.6-g-i-b** below.
- [x] **M0127-P5.6-g-i-b — give the DS05 gate a plan-shape channel.**
      DONE 2026-08-05. `scripts/tpcds-plan-diff.py` (per-query diff of two plan
      captures) + `scripts/tpcds-sf05-regression.sh`: a `plans` subcommand and a
      tail stage on `sweep` that writes `plans-<stamp>.txt` beside
      `sweep-<stamp>.txt` and appends `=== PLAN-SHAPE: queries=99 same=N
      changed=N … ===` with the changed query list. **The primary bar is not
      weakened and cannot be**: a sweep run with `PLAN_DIFF` pointed at a
      nonexistent file still exits 0, and the report says the channel is
      non-blocking. The capture is the FULL corpus even under `QUERIES=` (a plan
      file exists to be diffed against every other one; 14 s for all 99), and it
      stamps the planner-flags arm label — a plan diff across different flags is
      meaningless. **Bar met, without re-running the ~1 h sweep**: three
      consecutive captures at one commit → `changed=0` each time, and against
      P5.6-g-i's four committed corpus captures (the format is deliberately
      diff-compatible with `2026-08-05-p56gi-capture.sh`) the new gate path
      reproduces 09 §5.10's attribution exactly — D `f8338a09` → 0 (same Go code,
      different harness/dir), C `8ce056ff` → 4 (Q13 Q41 Q48 Q85), B `4b820ab8` →
      5 (+Q83), A `ce027cee` → 75. One real defect found on the way: psql stamps
      errors with the script PATH, so Q36/Q70/Q86 — the dsqgen artefacts whose
      block is an error message, not a plan — were three permanent false
      positives on any results-dir change; the diff tool canonicalises the prefix
      to `psql:<script>:<line>:`. Evidence
      `analysis/leftdeep-joins/2026-08-05-p56gib-README.md`; doc 09 §5.11;
      IMPLEMENTATION-TODO P5.6-g-i-b. No ledger row: harness-only, no PG
      behaviour left unimplemented. Successor stays **M0127-P5.6-g-ii**.
      ~~Original filing:~~
      P5.6-g-i's four corpus captures were built by hand for that loop, and a
      74-query plan change passed the gate in silence: it compares row counts
      and checksums only. That is the right PRIMARY bar and must not be
      weakened — the ask is a second, non-blocking column. Concretely: an
      `EXPLAIN`-only pass (every statement prefixed, so Q14/23/24/39's second
      statement is never executed) writing one plan file per run beside the
      sweep report, plus a diff against the previous run's file in the summary.
      The noise floor is already known to be zero at S-cold (measured
      2026-08-05), so a diff there is signal, not flake. Start from
      `analysis/leftdeep-joins/2026-08-05-p56gi-capture.sh`, which is that pass
      in throwaway form. Bar: two consecutive sweeps at the same commit report
      zero plan diffs, and one at a different commit reports the expected set.
- [x] **M0127-P5.6-g-ii — the `*HashAggregate` arm, and Q18's real shape.**
      DONE 2026-08-05, and the item as filed was the wrong half of itself. The
      arm alone measures worse because **upstream does not have it**:
      `examine_simple_variable` (selfuncs.c) reaches
      `if (subquery->groupClause)`, sets `vardata->isunique` when the referenced
      output is the sole grouping column, and returns "cannot go further"
      *without* a statistics tuple — what crosses a grouping node is
      UNIQUENESS, never a distribution (grouping mashes the MCV list and
      histogram; it cannot destroy one-row-per-group). The consumer is
      `get_variable_numdistinct`'s negative `stadistinct`, i.e. the grouped
      relation's own row count. Landed as `resolvesToGroupUniqueColumn` /
      `groupUniqueNDistinct` (joinkeyproof.go) consumed **only** by
      `columnNDistinctForChild`; `resolveBaseColumn` still refuses to walk a
      grouping node and a test pins that MCVs do not leak up. DISTINCT /
      DISTINCT ON are the same upstream test's other halves.
      **Q18 42 837× → 24 242×** (2 998 620 → 1 696 939 — the `5 997 241 × 0.5`
      punt is gone), parity excess vs PG 8.0× → 4.5×. It stays the corpus's one
      absolute violation, but the residual is now attributable: goopg's
      `l_orderkey` ndistinct is *more* accurate than PG's (1 210 559 vs
      ~339 000, truth 1 500 000), which makes its post-HAVING inner 3.6× larger.
      **The second half was measured INERT, not skipped**:
      `reduce_unique_semijoins` fires in PG's Q18 plan, but for an inner unique
      on the join key `inner_rows` = nd2, so the INNER and SEMI formulas agree
      term for term — it buys join-order freedom, not a number. Ledger row.
      **Found and fixed what the arm exposed:** `estimateJoin` had no
      outer-join arm at all — LEFT/RIGHT/FULL took the INNER product and
      stopped before `calc_joinrel_size_estimate`'s "the output must be at
      least as large as the non-nullable input"; TPC-DS Q77 estimated 885 rows
      for a join whose outer alone is 8 885. 12 tests.
      Bar met: UNITS + **DS05 sweep identical to baseline**
      (`PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4`; 12 of 99
      plans moved, zero rows; stream 2 116 s → 2 074 s with Q80 41→14 s,
      Q40 16→2 s, Q78 29→17 s) + audit run (violations 2 → 1, no joinrel
      worse). Evidence `analysis/leftdeep-joins/2026-08-05-p56gii-*`; doc 09
      §5.12; IMPLEMENTATION-TODO P5.6-g-ii. 3 ledger rows. Successor:
      **M0127-P5.6-g-v** below.
- [x] **M0127-P5.6-g-v — Q18's residual, which is NOT a HAVING problem.**
      DONE 2026-08-05, and the answer is "neither engine differs on HAVING".
      The one `EXPLAIN` the item prescribed settled it: PG 339 423 → **113 141**
      and goopg 1 150 720 → **383 573** are both exactly ÷3 — DEFAULT_INEQ_SEL
      over an aggregate neither engine has statistics for, upstream via
      `cost_agg`'s `clauselist_selectivity(quals)` scaling of `output_tuples`,
      goopg via the `*Filter` wrapper over the `*Aggregate`. **The HAVING
      mechanism is already identical; the whole 3.39× gap is the group
      estimate**, and goopg's ndistinct is the *more* accurate one (1 150 720
      vs PG's 339 423 against a truth of 1 500 000 — PG is 4.4× LOW). Q18's
      inner is bigger than PG's *because goopg's statistics are better*, so
      closing the gap would mean degrading them: **closed with no estimator
      change.** Q18's standing violation is inherent to pricing an aggregate
      blind, shared with upstream.
      **The measurement found a real defect elsewhere — in the instrument.**
      goopg splits a qual from the rows it filters: the predicate rides a
      `*Filter` wrapper that `walkPlanFiltered` collapses onto the child below
      it, and the collapsed line printed `EstimateRows(child)` — the PRE-qual
      count — beside a `Filter:` the estimator had already applied. Upstream
      cannot have this gap (`set_baserel_size_estimates` stores `rel->rows`
      post-`clauselist_selectivity`; `cost_agg` sets `path->rows` post-HAVING).
      The estimator was always right — a *parent* reads `EstimateRows(*Filter)`
      and sees the filtered count, which is why a `Gather` over a filtered scan
      was correct while the scan under it was not. Fixed by rendering the
      collapsed wrapper's own estimate: `lineitem` filtered scan
      **5 997 241 → 1 689 312** (PG 1 673 754), `nation` 25 → 4 (PG 5), TPC-DS
      `date_dim WHERE d_year = 2000` **73 049 → 365** (PG 365, one year of
      days). This is P5.6-g-iii-class, not cosmetic: `estimateaudit` parses
      that field (`nodeLineRe`) and §5.11's DS05 plans channel captures it, so
      **every capture taken before this commit reports unfiltered sizes for
      filtered relations** — including the row-count half of M0125-0026's
      "`date_dim` is costed at 73 049" evidence (C2's qual-*placement* finding
      stands; its row-count reading was a renderer artifact).
      Bar met: UNITS green; audit **1 violation (Q18), unchanged** from the
      p56gii baseline with every joinrel diff sub-1 % ANALYZE noise and none
      worse; DS05 `plans` **95 of 99 changed, but with `rows=` normalised the
      diff is 6 lines — a psql header width, nothing else**, i.e. zero
      structural movement (the proof it cannot reach plan selection). 3
      regression tests, each verified failing without the fix. Evidence
      `analysis/leftdeep-joins/2026-08-05-p56gv-postfix.*`; doc 09 §5.13;
      IMPLEMENTATION-TODO P5.6-g-v. 2 ledger rows. Successor:
      **M0127-P5.6-g-vi** below.
- [x] **M0127-P5.6-g-vi — re-read the pre-fix plan-text conclusions.**
      **DONE 2026-08-05, no code change, and every audited conclusion
      SURVIVES.** The two DS05 corpus captures bracketing `20e17fa5` are
      line-aligned (5 962 lines each), so a positional diff gives the blast
      radius as a measurement rather than an argument: **836 of 3 283 node
      lines carrying `rows=` changed — 25.5 %, across 96 of 99 queries** — and
      the split is clean: **836 of the 966 lines that carry a `Filter:` detail
      moved, against 0 of the 2 317 that do not.** That makes the rule for
      reading any pre-fix capture exact and cheap: **a `rows=` is trustworthy
      iff its node line has no `Filter:` beneath it.** Where it is wrong it is
      badly wrong (overstatement median **9×**, p90 **18 000×**, max
      **1 920 800×**), and it reaches **join nodes**, not just scans — Q1's
      `Hash Join (INNER) … Filter: (date_dim.d_year = 2000)` went `rows=716` →
      `rows=3`, so P5.6-g-v's "join nodes carry no collapsed `*Filter`" is a
      TPC-H fact, not a general one.
      **Verdicts** (each checked against the capture the finding itself
      cites): **M0125-0026 C2** (pervasive form *and* the Q5 form),
      **M0125-0038 (C5)**, **M0125-0040 (C6)**, **M0125-0031**, and the
      `estimate-audit` joinrel conclusions of doc 09 §5.3–§5.12 — **all
      survive; none needs re-deriving.** The audit ones by direct
      re-measurement (P5.6-g-v's run: 1 violation, Q18, unchanged); the rest
      because every line they quote is bare. Not luck: C2/C5/C6 are *about*
      relations goopg failed to filter, so the numbers they cite are precisely
      the ones the renderer had no filter to mis-scale.
      **One correction, and it runs opposite to the filing.** This item was
      filed saying C2's row-count claim "is already known to be an artifact" —
      **it is not.** C2's own measurement is that **66 of 68** qualifying
      `Filter:` lines sit on a join node, so the `date_dim` scans carry no
      filter and 73 049 is what the estimator genuinely used: the row-count
      claim is faithful for exactly the reason the placement claim is true.
      P5.6-g-v's blanket wording is narrowed in place (09 §5.13 now carries a
      `↳ NARROWED by §5.14` pointer). The only corrupted captures are C2's two
      named exceptions — the Q14/Q54 scalar-SubPlan `date_dim` scans printing
      `rows=73049` beside a scan-level `Filter:` — cited for placement, not
      rows. Separately, **C5 corroborates the fix instead of falling to it**:
      its `365.25` for `date_dim after d_year` appears nowhere in that plan
      text (the line reads 73 049) — it is `73 049 × 0.005` (`DEFAULT_EQ_SEL`),
      recovered by *dividing* the join estimate, i.e. C5 observed the estimator
      holding the post-qual number the renderer hid, days before P5.6-g-v
      proved it.
      Pre-fix captures are deliberately **not** re-captured; they stay as the
      historical record and the rule is recorded so it is not re-derived a
      third time. Doc 09 **§5.14**; IMPLEMENTATION-TODO P5.6-g-vi; working
      `analysis/leftdeep-joins/2026-08-05-p56gvi-README.md` (carries the
      reproducible classifier script). No ledger row: bookkeeping over an
      instrument, no PG behaviour left unimplemented. Bar met — docs-only, so
      the commit-hook pgbench smoke plus `make ralph-state-guard` is the whole
      gate set that applies.
- [x] **M0127-P5.6-g-iii — fix the acceptance instrument, not the estimator.**
      DONE 2026-08-05. `estimateaudit.Q21AntiJoinMax` (5 000×) beside Q9's bar,
      both now rendering their justification into the artifact instead of being
      bare numbers; and 09 §4's ratchet restated as **per-joinrel parity**
      (`internal/estimateaudit/parity.go`): a joinrel is its base-relation SET
      (`RelOptInfo.relids`) rebuilt from the printed plan, so two engines that
      reached it by different join ORDERS still compare, and the ratchet fires
      only on excess > 10× AND goopg's own factor > 100×. `--from-plans` /
      `--reference` / `--ref-port` apply a new instrument to old committed
      evidence — this run replayed the P5.6-g capture, so NO estimator code
      changed. Absolute violations 2 → 1 (Q21 is measured parity: 4 003× vs
      PG's own 4 178×, excess 1.0×). Baseline pinned:
      `parity_violations=1 shape_mismatches=67`. Evidence
      `analysis/leftdeep-joins/2026-08-05-p56giii-parity{.txt,.pg.plans.txt,-README.md}`,
      docs 09 §4.1 + §5.8. Bar met: UNITS + audit run with the parity column.
      Successors: **P5.6-g-iv** below and a ledger row (2026-08-05) for the
      EXPLAIN rendering gap the gate exposed.
- [x] **M0127-P5.6-g-iv — Q19, the only estimator defect TPC-H can prove.**
      DONE 2026-08-05. The answer to "which step collapses" was none of the
      three: the defect was one level earlier, in a preprocessing pass goopg
      never had. **goopg did not run PG's `canonicalize_qual`**
      (`process_duplicate_ors`, prepqual.c), so Q19's thrice-repeated join
      clause `p_partkey = l_partkey` stayed inside every OR arm — charged once
      as the equi-join key and again per arm at DEFAULT_EQ_SEL — and the three
      single-relation conjuncts common to all arms (`l_shipmode IN`,
      `l_shipinstruct =`, `p_size >= 1`) stayed trapped where NOTHING priced
      them. M0058-0004's `commonEquijoinsAcrossOr` had already computed the
      same intersection and used it for the join EDGE only.
      Landed: `internal/planner/qual_canonical.go` (`canonicalizeQual`), applied
      in `planSelect` at upstream's placement, parse tree not mutated;
      `strictParserExprKey` (exprkey.go) as the equality test, because
      `parserExprKey` drops table qualifiers and would hoist across `a.x = 1` /
      `b.x = 1`. 9 tests. Q19 `{lineitem,part}` est 1 → 309 vs actual 131
      (131.0× → 2.4×), parity excess 126.5× → 2.3×, `parity_violations=0
      shape_mismatches=0`; the plan now carries PG's own shape (both scans
      filtered, reduced OR at the join). Q12 — the only other OR-bearing TPC-H
      query — bit-identical. Evidence
      `analysis/leftdeep-joins/2026-08-05-p56giv{.txt,.plans.txt,-README.md}`
      (+ the Q19-only `-q19` pair); doc 09 §5.9. Bar met and exceeded: UNITS +
      audit with the parity column + **tpch-spotcheck PASS** (Q12 rows=2,
      Q13 rows=35). **DS05 NOT RUN** — nightly batch held the host all loop;
      carried on P5.6-g-i, and it matters MORE for this item than for P5.6-g
      because TPC-DS has many OR-bearing queries. **↳ DISCHARGED 2026-08-05 by
      M0127-P5.6-g-i**: no row/checksum change; 4 of the 27 OR-bearing TPC-DS
      plans move (Q13, Q41, Q48, Q85), Q13 into PG's own hash-join shape. 1 ledger row (3 deferrals:
      constant handling, UPDATE/DELETE quals, `extract_restriction_or_clauses`).
      Watch list still open from g-iii (>10× the reference, under the 100×
      floor): Q16 84.9× vs 2.0×, Q20 32.1× vs 1.1×, Q14 12.4× vs 1.0×.
- [ ] **M0127-P5.7 — nbatch-aware `hashJoinCost`** (shared sizing fn);
      Startup/Total split for LIMIT-over-join. IMPLEMENTATION-TODO P5.7; 04 §4;
      06 §5. Bar: UNITS + PLAN (default arm ZERO diffs). **DECOMPOSED into -a
      (the spill term, pricing) and -b (the LIMIT fraction, selection) below —
      they touch different halves of the search and have different blast
      radii.**
- [x] **M0127-P5.7-a — the spill term: `hashJoinCost` prices the geometry the
      executor will actually build.** DONE 2026-08-05. `hashJoinCost`
      (`internal/planner/cost_funcs.go`) now takes a `hashJoinInputs` struct and
      calls `hashsize.Choose` — the same function, with the same argument shape,
      that `joinOp.buildGeometry` calls at run time — then applies upstream's
      batch I/O charge verbatim when it answers `NBatch > 1`
      (costsize.c:4239-4248): `seq_page_cost × innerPages` at STARTUP (the inner
      is written during the build) and `seq_page_cost × (innerPages +
      2 × outerPages)` at run. `spillPages` is `page_size` with
      `relation_byte_size` replaced by `hashsize.EntryBytes`, so the pages
      charged and the bytes the geometry solved for come from one model.
      **It replaced M0126-0013's unconditional `seq_page_cost × innerRows/100`**,
      which cited costsize.c:4166 for a page charge upstream does not make
      there — PG charges pages only under `numbatches > 1`, and for the SPILL
      rather than the resident table. Being monotone in `innerRows`, the
      stand-in penalised a 6 M-row build that fits `work_mem` exactly as much as
      one that does not, which is the distinction that decides the plan
      (`TestHashJoinCost_SpillDependsOnFitNotOnSize`).
      **The width that crosses the boundary is the finding:** PG hands
      `ExecChooseHashTableSize` a byte width because its entry is a packed
      MinimalTuple; goopg's entry is a `[]Datum` of 48-byte structs, so its size
      follows the COLUMN count — and the executor passes `len(schema)`. The
      planner had no column count, so `RelOptInfo.NCols` is new (leaf schema for
      a base rel, sum over inputs for a join rel, since a join row is its inputs
      concatenated). Feeding the existing byte-valued `Width` would have sized
      the same build ~25× differently on the two sides of the sibling-path rule.
      4 new tests. IMPLEMENTATION-TODO P5.7-a; 04 §4.1; 06 §5. **4 ledger rows**
      (per-session `work_mem` never reaches the planner; `spillPages` prices the
      in-memory footprint not the narrower batch-file encoding; `nbatch` not
      exposed on `Path` for EXPLAIN; the LIMIT fraction → -b).
      Bar met: UNITS. PLAN not applicable and the reason is structural, not a
      skip: both `hashJoinCost` callers are behind OFF-by-default gates
      (`costDrivenJoinOrder`, and the PG-shaped DP, which has no `planSelect`
      caller at all), so the default arm has zero *reachable* plan movement.
- [x] **M0127-P5.7-b — the LIMIT fraction: nothing SELECTED on startup cost.**
      DONE 2026-08-05. `internal/planner/tuplefraction.go` ports
      `preprocess_limit` (planner.c:2577) → `get_cheapest_fractional_path`
      (planner.c:6617) → `compare_fractional_path_costs` (pathnode.c:127).
      `preprocessLimit` derives PG's `tuple_fraction` from the `*Limit` above
      the join, `searchCtx.tupleFraction` carries it, and the new
      `searchCtx.finalPath()` — upstream's
      `best_path = get_cheapest_fractional_path(final_rel, root->tuple_fraction)`
      (planner.c:437) — is now the only value a caller may hand
      `createPlanAtSearchRoot`, because reading `finalRel().CheapestTotal`
      directly discards the fraction silently.
      **The finding: the fraction is TWO mechanisms and only the pair moves a
      plan.** Beside selection there is RETENTION — the new
      `RelOptInfo.ConsiderStartup` (`consider_startup`, relnode.c:211/707, set
      from `tuple_fraction > 0` on every rel the search creates) enforced in
      `comparePathCostsFuzzily`'s two "different" arms, which is where upstream
      puts it (pathnode.c:178-183): a total-cost loser may not survive on good
      startup cost alone unless a fraction will ever be asked for. Without it a
      fractional selection has nothing to choose from. goopg had NEITHER half,
      so it was wrong in both directions at once — it behaved as if
      `consider_startup` were permanently true, keeping fast-start paths PG
      prunes (a nested loop at total 183 beside a hash at 11.6, kept only for
      its zero startup), and then always selected on total cost anyway. Three
      existing tests were asserting on exactly such paths and now state the
      fast-start regime they were really testing in.
      Upstream's absolute/fractional overload is kept end to end:
      `preprocessLimit` can only emit the absolute count (it knows the count,
      not the result size), `getCheapestFractionalPath` converts it against
      `CheapestTotal.rows` at the moment of use, and
      `compareFractionalPathCosts` folds anything outside (0,1) back onto the
      plain total-cost order so an unconverted count degrades to today's answer.
      5 tests; acceptance is `TestLimitOverJoinMovesTheChosenPath` (a 10 000-row
      joinrel offered hash startup 500/total 900 and loop startup 0/total 20 000
      — crossing at ≈2.55% — chooses hash with no LIMIT and does not even retain
      the loop, the loop under `LIMIT 100`, hash again under `LIMIT 5000`).
      IMPLEMENTATION-TODO P5.7-b; 04 §4.3. **3 ledger rows** (no
      `estimate_expression_value` const-fold on the LIMIT expression, so
      `LIMIT 5+5` / `LIMIT $1` take the 10% punt; `consider_param_startup`
      unreachable behind 03 §4.4's pin; no production PRODUCER of a fraction
      yet). Bar met: UNITS. PLAN not applicable for the same structural reason
      as P5.7-a — every consumer is behind an OFF-by-default gate
      (`GOOPG_PGSHAPED_DP`, and the search has no `planSelect` caller at all),
      so the default arm has zero reachable plan movement.
- [x] **M0127-P5.8 — collapse limits with PG's actual semantics.**
      DONE 2026-08-05. `internal/planner/collapse.go` ports the JOINLIST half of
      `deconstruct_recurse` (initsplan.c:1148-1452) — and only that half: the
      `JoinDomain`s, `qualscope`/`inner_join_rels`/`nonnullable_rels` sets and
      the `JoinTreeItem` list phase 2 walks have no reader in goopg, which
      places quals in the pre-search pipeline and does not let outer joins into
      the search at all. `deconstructJointree` is computed inside
      `planFromClause`/`planFromRangeVars` onto `resolveContext.joinlist`.
      **The finding: neither GUC is a search-size cap, and reading them as one
      would have re-introduced the greedy pre-reorder for wide comma joins**
      (03 §6's documented Q2 failure mode). PG applies them to the join tree's
      SHAPE: `sub_members <= 1` collapses every single-baserel FROM item
      unconditionally, so `FROM a,…,o` is one 15-way problem with both limits
      at 8; `from_collapse_limit` governs merging MULTI-relation sub-joinlists
      and `join_collapse_limit` governs explicit JOIN constructs, nothing else.
      A cap belongs at the `RelSet` ceiling (`maxSearchRels`), a representation
      limit, not a user knob. Two more findings: **the =1 pin does not bite
      until the third relation** — at `a JOIN b` the "cannot combine" branch
      emits `[a, b]`, identical to the collapsed answer, because a one-element
      side is unwrapped rather than wrapped (initsplan.c:1428-1436), so =1
      orders the syntactic tree's own nodes and never forbids commuting a
      single join; and a joinlist leaf is a direct `resolveContext.bindings`
      subscript, which holds only because the pass runs in the two functions
      where the FROM walk that numbers leaves IS the walk that appends bindings.
      Outer joins take upstream's FULL treatment verbatim
      (`list_make1(list_make2(l,r))`, initsplan.c:1414-1418) per 03 §4.4, RIGHT
      included because goopg has no `reduce_outer_joins`. 8 tests; acceptance is
      `TestFlatCommaListIsOneProblemAtAnyWidth`. IMPLEMENTATION-TODO P5.8;
      03 §6.1 (new). **The 12-table bail-out is deliberately NOT deleted**: the
      TODO line said to, but 03 §7 says it dies *with the bushy DP* (P6.3) and
      §7 is right — it guards the OLD 3ⁿ subset-bitmask DP (3¹⁶ ≈ 43 M splits),
      which is still the production path, so deleting it now would hand that DP
      13-16-relation queries it cannot finish. §7 and the `maxSearchRels`
      comment now say so explicitly. **2 ledger rows** (per-session collapse
      GUCs do not reach `Plan`, so `SET join_collapse_limit = 1` is still a
      no-op in a real session; no joinlist CONSUMER yet — that is P5.9).
      Bar met: UNITS. DS05/PLAN not applicable for the same structural reason as
      P5.7-a/-b: `GOOPG_PGSHAPED_COLLAPSE` is OFF, so production joinlists pin
      explicit JOINs exactly as today, and nothing reads the result under either
      flag setting — the default arm is byte-identical.
- [ ] **M0127-P5.9 — S5 acceptance run + flag flip.** The full 09 §3 bar (run
      once with collapse OFF, then with collapse ON) + plan-shape ratchet
      baseline (§4) + estimate audit (§5); flip `GOOPG_PGSHAPED_DP` ON and
      retire `GOOPG_COST_DRIVEN_JOINORDER`, or record the documented no-go.
      File `analysis/leftdeep-joins/…-s5-acceptance.txt`. IMPLEMENTATION-TODO
      P5.9; 09 §3-§5; 08 §2. Bar: the full acceptance bar.
- [ ] **M0127-PS6.1 — compile `HashKeys[i]` accessors and the residual
      conjunction to `ExprNode` at `Open`** (`internal/executor/exprnode.go`);
      `ExprAdapter` fallback for unsupported kinds. IMPLEMENTATION-TODO PS6.1
      (first half); 05 §6 (E5). Bar: UNITS + BENCH (no alloc regression).
- [ ] **M0127-PS6.2 — compiled ↔ interpreted sibling audit + parity spot-diffs**
      on expression corpora incl. the overflow corpus (0097-0037 precedent).
      IMPLEMENTATION-TODO PS6.1 (second half); 09 §1 SIBLING. Bar: parity corpus
      + BENCH. (The release gate for E5.)
- [ ] **M0127-P6.1 — delete fusion** (`fused_hash_join.go` 707 lines, hook
      `executor.go:160-163`, env vars, orphan-export check —
      `IsCanonicalKeyEquality` has no other caller). IMPLEMENTATION-TODO P6.1;
      08 §4 "Fusion". Bar: grep-clean + UNITS + SPOT.
- [ ] **M0127-P6.2 — delete MultiHashJoin** (fresh grep inventory at S7 time;
      2026-08-02 count ~34 arms / 18 files: node, packer
      `rewriteMultiWayChain`/`collectMultiHashTables`, `mhj_input_rewrite.go`,
      posmaps, cost/cardinality arms, executor op `multi_hash_join.go`,
      EXPLAIN arms, `generateMultiHashJoinPath`, flags). IMPLEMENTATION-TODO
      P6.2; 08 §4 "MultiHashJoin". Bar: after S5-ON survives a clean nightly
      cycle; grep-clean + UNITS + SPOT + DS05.
- [ ] **M0127-P6.3 — delete the old subset-bitmask DP + layout/remap family**
      (`enumerateBushyPlans`/`enumerateSubsets`/`enumerateSplits`/
      `dp map[uint16]dpEntry`, `estimateJoinCost` + integer weights,
      `attachUnusedCrossEdges`, `bushySeedRowCounts`, the 12-table cap,
      `IsSmallDimensionSide` pinning, `chooseInnerJoinAlgo` searched,
      `dpEntry.layout`/`remapKeyToLayout`/`mergeSubsetLayouts`); demote
      `joinorder.go` to the over-limit sequencer. **`buildBindingsPosMap`/
      `applyJoinTreePosMap` held back** until the 03 §10 boundary map is proven
      in production (08 §4 — the S7 change most likely to regress).
      IMPLEMENTATION-TODO P6.3; 08 §4. Bar: grep-clean + UNITS + SPOT + DS05.
- [ ] **M0127-P6.4 — supersession stamps + ledger rows.** 0034-0001, 0038-0001,
      cost-model/09 §3 allowance, 0043/0063/0125/0126 MHJ chapters get
      `superseded by: leftdeep-joins/` headers; README index status flips;
      ledger rows for every deliberately-skipped PG behaviour (GEQO, skew
      buckets, SpecialJoinInfo in-DP — `join_is_legal`-inference-dependent
      marker —, shared spilling builds, full join_order_restriction inference).
      IMPLEMENTATION-TODO P6.4; 08 §5. Bar: doc review.

**Order:** P0.1→P0.3 (S0) → P1.1→P1.3 (S1; P1.3's bar gates P2) →
P2.1→P2.3 (S2) → P3.1→P3.5 (S3) → P4.1→P4.4 (S4) → P5.1→P5.9 incl. P5.3a
(S5, each 1–2 loops) → PS6.1→PS6.2 (S6) → P6.1→P6.4 (S7, only after S5-ON
survives a clean nightly cycle). No M0127 task may be selected while any
M0125 item marked as a prerequisite above is open (Current Priority banner).

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
