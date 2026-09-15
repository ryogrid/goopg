# goopg Fix Plan

## Current Priority

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless this banner
or a dependency forces another order**. **This banner is the sole ordering
authority** — `.ralph/working_set.md`'s "NEXT LOOP" note carries state, not
priority, and does not outrank it.

### Selection order (rewritten 2026-09-14, user directive)

**The plan-parity milestone group M0137–M0143 is the highest-priority work.**
Its goal is the one in `.ralph/specs/GOAL_AND_REQUIREMENTS.md`: every currently
executable TPC-H and TPC-DS query produces **the same plan as PG 18.3**, reached
by the same statistics, the same costing and the same planning logic — never by
forcing shapes. The group completes
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md`
with the owner's two decisions of 2026-09-14 applied.

**Before selecting any M0137–M0143 task, read `AGENT.md` §"Plan-parity harness —
applies ONLY to M0137–M0143".** It is binding and it carries the goal, the two
owner decisions, the reading order for the prior phase's evidence, the list of
known-stale claims, and what every task report must contain.

### Re-ordered 2026-09-15 after the first-pass review

36 tasks completed, **goal metric unmoved** (TPC-H 6/22, TPC-DS 2/99) and
TPC-DS categories net worse (525 -> 540). The review
(`tmp/METHODLOGY3_RALPH_CHECK0915/`) found one dominant reason and it decides
this ordering: **narrowing runs after costing, so M0139's landed work cannot
move a single plan.** Unblocking that comes before starting anything new.

Select in this order:

1. **M0141-S2a-fix and M0139-0007 — the costing-order unblock. TOP PRIORITY.**
   These are the same defect seen from two sides: narrowing/width information
   reaches the cost model too late (or in the wrong currency) to affect plan
   selection. Until one of them lands, **M0139's entire +671 lines contribute
   nothing to the metric and M0141's remaining slices cannot be judged.**
   Take `M0139-0007` first (it establishes the absorption principle the
   S2a-fix then applies), unless its scoping recon says otherwise.
   **RESOLVED 2026-09-15: both halves are now landed-and-decided.**
   M0141-S2a-fix1 landed (TPC-H `match` 6→8); fix2 was attempted and declined
   (measured net-neutral/negative, reverted). M0139-0007's three filed pieces
   — the recon, 0007a (R108/R113 measurement, both HOLD), 0007b (Memoize
   absorption, HOLD) — are all complete; only the narrow M0139-0007c follow-up
   (Memoize's un-absorbed per-key width term, not on the critical path) is
   still open. **The next loop should select item 2 below (M0137's re-opened
   0014–0017)** unless a later banner edit says otherwise.
2. **M0137's re-opened tasks (0014–0017).** Instrument and orphan-mechanism
   debt found by the review. 0015 is one line and 0016's gate is already
   satisfied. **0017 matters more than its size suggests**: TPC-H plans are
   captured `-serial` only, so `parallelism` is measured *out* of the headline
   6/22 and one of the nine categories is currently unscoreable.
   **RESOLVED 2026-09-15: item 2 is fully closed (0014, 0015, 0016, 0017 all
   `[x]`).** 0017 landed the parallel-mode PG baseline and measured
   `parallelism=16/22`, `match=2/22` at `-serial=false` — see
   `docs/design/0100-0149/m0137-0017-serial-and-parallel-capture.md`. Its own
   follow-up (triage the 16 divergences) is filed separately as **M0137-0019**
   (listed under the M0137 milestone section below, not in this fixed
   0014-0017 set) — it is new investigative work sized like M0141/M0142's
   remaining slices, not a re-opened harness debt, so it does not inherit
   item 2's priority; the next loop applies normal banner order to it.
   **The next loop should select item 3 below** (M0138 0007-0009 /
   M0140-0006) unless a later banner edit says otherwise.
3. **M0138 — PG-faithful ANALYZE statistics** (0007–0009: the orphaned
   `numeric` `avg_width`, the category-shift bisect, the correlation banding
   re-open) and **M0140 — TPC-DS parallelism** (0006: the partial-Append
   producer that M0140-0004 deferred without an owner).
   **RESOLVED 2026-09-15: item 3 is fully closed.** M0138 0007-0009 were
   already `[x]` before this loop. M0140-0006 is closed as a decomposition
   (design doc `docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md`):
   the recon's own sizing (three pieces, each a whole C-19-series slice) made
   blind implementation a one-task-per-loop violation, so it is split into
   **M0140-0006a/0006b/0006c** (filed under the M0140 milestone section
   below) — none selected yet. **The next loop should select item 4 below**
   (M0141's remaining slices / M0142) unless a later banner edit says
   otherwise, or pick up M0140-0006a as the first of the new sub-tasks if it
   judges that a better use of the banner's ordering (item 4's M0142-0004 is
   itself just a re-measurement, so either is a reasonable next pick).
4. **M0141's remaining slices** (S2b, S3–S6, and **S7 — Incremental Sort**,
   which 14 TPC-DS queries need before they can match at all) and **M0142 —
   Join-order costing** (0003c onward, plus 0004–0008 promoted from the ledger).
   Take **M0142-0004** first inside that milestone: it is a re-measurement, and
   until it runs, nothing is known about how large TPC-DS's row-estimate error
   still is — all four cuts the ledger named have since landed and the "3–5
   orders out" figure predates every one of them.
5. **M0143 — Engine correctness carry-overs.** Gated on nothing; select it
   whenever everything above is blocked. **Still 7/7 untouched.**
6. Then M-NIGHTLY's own open items, then the pre-existing milestones
   (M0119 -> M0122 -> M0131 -> M0134 -> M0135/M0136 -> M0095/M0110).

**Three owner decisions of 2026-09-15 bind every M0137–M0143 task below** (full text in
`AGENT.md` §"Further owner decisions"): **B1** `minimize_datum`/packed retention
is **NO-GO** and out of scope — do not re-propose it; **B2** the direction (same
statistics, same plan) is unchanged, but irreducible goopg/PG representation
differences must be **absorbed** by giving the cost model the PG-equivalent
quantity rather than goopg's native one — this is not tuning, and it is the
route that replaces packed retention; **B3** `GOOPG_GATHER_PATHS=all` stays
default-on and may not be reverted on category count alone.

**M-NIGHTLY: filing stays unconditional, selection does not.** Every loop still
reads `ci/logs/action-items.md` and files each new `## AI-` subject under the
M-NIGHTLY milestone below — that obligation is unchanged and outranks
everything, because it costs minutes and preserves nightly-regression
visibility. **But M-NIGHTLY items are no longer selected ahead of M0137–M0143.**
Two carve-outs still preempt: an item that breaks the build, and an item that
breaks a gate the plan-parity group depends on.

**M-NIGHTLY selection rule (applies when an M-NIGHTLY item is selected, per
ci/design/07-ralph-feedback.md §B):**
1. Before investigating, re-run the item's repro at HEAD — the log reflects the
   last nightly run and may be stale.
2. Fix with the normal gates (practice cards apply), cite the AI-id in the
   commit message, check the task off.
3. The next nightly run confirms and drops the item from the log.

## Notes / rules

- This is the authoritative TODO list for Ralph. Update it after every meaningful
  change (tick boxes, add newly-discovered follow-ups). ONE item per loop;
  decompose any item larger than a single agent invocation.
- Every non-trivial subsystem must land with (or just before) a design doc under
  `docs/design/<id>-NNNN-*.md` **and** a `docs/design/README.md` index entry —
  hard requirement, same loop. **Carve-out for M0137–M0143:** in that group the
  design doc is written **when the task is selected**, not before the milestone
  starts — see `AGENT.md` §"Plan-parity harness". The same-loop, same-commit
  indexing requirement is unchanged.
- Deferrals: never close a task silently with a forward reference. Append one row
  to `.ralph/deferral_ledger.md` (`date | task-id | landed | deferred | resume
  point | why`) and leave the fix_plan item unchecked. **The ledger is the source
  of truth for every "DEFERRED" note below** — consult it for full context/resume
  points.
- Completed milestones are archived under `completed_milestones/` (latest:
  `completed_fix_plan_012.md`); they are reference-only, NOT actionable, and must
  not be copied back here.


## M-NIGHTLY — Nightly regression triage (STANDING — ACTIVE since 2026-08-08)

Standing milestone: never complete it, never archive it, keep it directly
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
   2. Before investigating, re-run the item's repro at HEAD — the log
      reflects the last nightly run and may be stale; if it passes, check
      the task off with a "stale — already fixed" note.
   3. Fix with the normal gates (practice cards apply), cite the AI-id in
      the commit message, check the task off. The next nightly run confirms
      and drops the item from the log.
(Tasks are added here by the in-loop agent, one per subject. This
placeholder is a comment, not a checkbox, so the plan-complete exit
heuristic stays live.)

### Nightly run 20260901-010436 (sha `d93fb9edc669`, 7 items) — filed 2026-09-01
- [ ] **testport/TestPort_PgStatActivity (AI-20260901-010436-005, AI-20260905-011015-007, AI-20260914-235643-010)**.
- [ ] **testport/TestSyntax_Catalog_PgStatActivity (AI-20260901-010436-007, AI-20260905-011015-009, AI-20260914-235643-012)**.

### Nightly run 20260902-005256 (sha `c11e55d253ff`, 8 items) — filed 2026-09-02
- [ ] **testport/TestE2E_PGColdStartOnGoopgDataDir (AI-20260902-005256-001, AI-20260905-011015-002, AI-20260914-235643-004)**. New
  tonight; possibly the M0131-S4 "FAIL-WHEN-FIXED" assertion flipping red
  because a Theme F fix landed rather than a real regression — re-run repro
  and check the M0131 Theme F findings list before treating as a bug.
  (`testport/TestPort_IsolationIntraGrantInplace`,
  `testport/TestPort_IsolationStats`,
  `testport/TestPort_LockRowsSortOverJoinTakesRowLock`,
  `testport/TestPort_PgDumpConnectionSetup`, `testport/TestPort_PgStatActivity`,
  `testport/TestPort_RegressSuite`, `testport/TestSyntax_Catalog_PgStatActivity`
  — remaining 7 items of this run all already have an open task above,
  AI-20260827-052222-037/-071/-080/-107, AI-20260901-010436-005/-007; no new
  line filed for those per the "do not add another" rule.)

### Nightly run 20260905-011015 (sha `2e3deb52ba73`, 9 items) — filed 2026-09-11
- [ ] **race/internal/executor (AI-20260905-011015-001, AI-20260914-235643-002)** — race suite failed
  in `internal/executor` (also failed previous run; repro: `go test -race
  -timeout 45m ./internal/executor/`).
- [ ] **testport/TestPort_IsolationIntraGrantInplace (AI-20260905-011015-003, AI-20260914-235643-006)** —
  FAILed, also failed previous run (repro: `go test -v -run
  '^TestPort_IsolationIntraGrantInplace$' ./internal/testport/`).
- [ ] **testport/TestPort_IsolationStats (AI-20260905-011015-004, AI-20260914-235643-007)** — FAILed,
  also failed previous run (same testport repro pattern).
- [ ] **testport/TestPort_LockRowsSortOverJoinTakesRowLock (AI-20260905-011015-005, AI-20260914-235643-008)** —
  FAILed subtests: join_no_sort, also failed previous run.
- [ ] **testport/TestPort_PgDumpConnectionSetup (AI-20260905-011015-006, AI-20260914-235643-009)** —
  FAILed, also failed previous run.
- [ ] **testport/TestPort_RegressSuite (AI-20260905-011015-008, AI-20260914-235643-011)** — FAILed
  subtests: limit, numerology, also failed previous run; 20260914-235643 adds subtests time, timetz.
  (Remaining 3 items — PGColdStart AI-…-002, PgStatActivity AI-…-007,
  Syntax_Catalog_PgStatActivity AI-…-009 — already have open tasks above;
  AI-ids appended per the "do not add another" rule. Evidence for all:
  `ci/logs/20260905-011015/`.)

### Nightly run 20260914-235643 (sha `baf40efcbfbd`, 14 items) — filed 2026-09-15
- [ ] **units/internal/parser (AI-20260914-235643-001)** — new tonight, units suite
  failed to build/run `internal/parser` (repro: `go test -timeout 10m
  ./internal/parser/`). Likely the same root cause as this file's own
  "Manually discovered" `parser/TestLockingClauseParity` entry below (filed the
  same day from an interactive gate run) — re-run both repros together before
  treating as two separate bugs.
- [ ] **race/internal/parser (AI-20260914-235643-003)** — new tonight, race suite
  failed in `internal/parser` (repro: `go test -race -timeout 45m
  ./internal/parser/`). Same likely-shared root cause note as the item above.
- [ ] **testport/TestPort_IsolationEvalPlanQual (AI-20260914-235643-005)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationEvalPlanQual$'
  ./internal/testport/`).
- [ ] **units/build-broke-mid-stage (AI-20260914-235643-013)** and
  **race/build-broke-mid-stage (AI-20260914-235643-014)** — `[infra]`, not
  regressions per the nightly bot's own classification: 1 package failed to
  *compile* in each stage, first error
  `bak/explain_bitmap_index_cond_test.go:30:64: undefined: explainNames`.
  `bak/` is untracked scratch (git status `?? bak/`, not part of any commit),
  so a clean checkout is unaffected; the nightly runner builds the live
  working tree, so this is contamination from stray untracked files, not a
  code regression. Re-run repro at HEAD before investigating further — `go
  build ./...` is clean as of this filing (2026-09-15); `bak/`'s `_test.go`
  only breaks a `go vet`/`go test` walk, not `go build` itself, which the
  nightly bot's own repro line does not distinguish.
  (Remaining 8 items of this run — race/internal/executor AI-…-002,
  testport/TestE2E_PGColdStartOnGoopgDataDir AI-…-004,
  testport/TestPort_IsolationIntraGrantInplace AI-…-006,
  testport/TestPort_IsolationStats AI-…-007,
  testport/TestPort_LockRowsSortOverJoinTakesRowLock AI-…-008,
  testport/TestPort_PgDumpConnectionSetup AI-…-009,
  testport/TestPort_PgStatActivity AI-…-010,
  testport/TestPort_RegressSuite AI-…-011,
  testport/TestSyntax_Catalog_PgStatActivity AI-…-012 — already have open
  tasks above; AI-ids appended per the "do not add another" rule. Evidence
  for all: `ci/logs/20260914-235643/`.)

### Manually discovered (not yet in a nightly `ci/logs/action-items.md` run) — filed 2026-09-15
- [ ] **parser/TestLockingClauseParity** — deterministic FAIL, found while
  running the M0137-0001 pre-commit gate (`RALPH_PRECOMMIT_SCOPE=units
  scripts/ralph-precommit-test.sh`; unrelated to that task's scripts/docs-only
  diff). `internal/parser/ast.go`'s `RangeVar.GroupedJoinUnaliased` field
  (added by `dc91bd6b7` "fix(planner): preserve grouped USING bindings for
  lateral", 2026-09-13) is now emitted by the parser on every `FROM`-clause
  `RangeVar`, but `yacc_locking_test.go`'s hand-written `want` AST literals
  predate that field and don't set it, so every locking-clause case now
  reads as "AST drift". Repro: `go test ./internal/parser/ -run
  TestLockingClauseParity -v`. Likely fix: either the test's comparison
  should ignore `GroupedJoinUnaliased` (it is not what the test is checking)
  or its `want` literals need the field added — not investigated further,
  out of scope for M0137-0001.
  - Blast radius correction (found running the M0138-0004 pre-commit gate,
    2026-09-15): the same drift is NOT limited to
    `TestLockingClauseParity` — the full `RALPH_PRECOMMIT_SCOPE=units` run
    shows 60 failing test functions in `go test ./internal/parser/...`
    (e.g. `TestValuesTable`, `TestCopyStatement`,
    `TestAggregateOrderByParity`, `TestVariadicCallParity`,
    `TestParityGoldensAreCurrent`, …) plus the pre-existing
    `github.com/goopg/goopg/bak` build failure (untracked scratch, not a
    real regression) — every hand-written `want` AST literal in the
    package that builds a `RangeVar` in a `FROM` clause is affected, not
    just the locking-clause cases. Still unrelated to M0138-0004's
    executor-only diff (`internal/executor/operators_analyze*.go`); `go
    test ./internal/executor/... ./internal/optimizer/...` is fully green.

## Archived — complete (see `completed_milestones/completed_fix_plan_012.md`)

M0130 (Cluster-directory compat with PG 18.3 + PG physical replication).

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

- [ ] **M0095-0003**.
## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by M0094
(recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.
Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP**.
- [ ] **M0110-0002 — pg_waldump TAP**.
- [ ] **M0110-0003 — pg_amcheck TAP**.
## M0119 — Deferral-Ledger Backlog Consumption (filed 2026-06-29)

Milestone: `docs/milestones/0119-deferral-ledger-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`.ralph/deferral_ledger.md`. Goal: drive every open (`status = -`) ledger row to
closure — implement the deferred scope, or verify it already landed and mark the
row `resolved`.

**Selection rule (see the Current Priority banner): pick a M0119 task ONLY when
no milestone above M0119 in the priority order has a remaining task that should
be done** (unchecked, not parked or deferred, prerequisites met). M0119 is the
terminal drain of the deferral ledger — it never runs ahead of feature/build work
in any higher-priority milestone.

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
prune-WAL round-trip — M0119-0005 is now fully landed, no open bullet
remains). The one open item below carries the remaining unbuilt scope. It was
the whole file's active task between 2026-09-01 and 2026-09-14; **since
2026-09-14 the banner ranks M0137–M0143 above it.**

- [ ] **M0119-0006 — pg_amcheck server tier**.
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

- [ ] **M0122-0008 — Auth / roles / multi-DB isolation / encoding**.
- [ ] **M0122-0009 — WAL / recovery / crash-consistency infra**.
- [ ] **M0122-0010 — Concurrency: buffer pool & btree locking**.
- [ ] **M0122-0012 — Perf infra: vectorization / slot-pipeline / harness**.
- [ ] **M0122-0013 — Physical/streaming replication & standby**.
- [ ] **M0122-0014 — Logical replication / decoding / subscription**.
- [ ] **M0122-0015 — Test-suite porting: amcheck / verify_heapam / pg_dump**.

## M0131 — Bidirectional cluster-directory cold-start + real-PG system-view hosting (filed 2026-08-11)

> **Demoted from top priority by the 2026-08-13 user directive**, and demoted
> again on 2026-09-14 when the plan-parity group M0137–M0143 took the head of the
> banner. This section sits in the middle of the file for append-safety only —
> **document order does NOT reflect priority.** The `## Current Priority` banner
> at the top of this file is the sole ordering authority: work M0131's remaining
> tasks below the plan-parity group, alongside the other pre-existing
> milestones.

**Milestone doc:** `docs/milestones/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
**Implementation plan:** `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
**Source:** M0130 Acceptance-bar item 1 (never discharged — every M0130 acceptance item was proven through the `pg_basebackup` lane, not a cold start); `docs/design/0130-0002-pg-class-heap-persistence.md` §"Remaining for full reverse-path parity" items 1-3 (no ledger rows, contrary to the filing rule); deferral-ledger rows #428, #490, #995, #996.

**Filing rule (inherited from M0130):** no task deferred without a ledger-recorded strong reason; subtasks inline in fix_plan; every non-trivial subsystem lands its design doc (draft → accepted) within M0131.

**Citation precedence:** the ten `0131-000N-*.md` sub-design docs re-verified every citation used here against the repo and the oracle. **Where a sub-doc and this section disagree, the sub-doc wins.** Corrections already folded in below: S1's blast radius is six unregistered GUC names, not four; S2 needs a new `ControlFileData` field decode before it can read pg_control; S4's "three deltas" reduce to one; `tupdesc.c:105` is an `elog(ERROR)` not a FATAL; `pg_stat_activity` and `pg_settings` swap S9 buckets. Ledger references written `#NNN` are LINE NUMBERS in `.ralph/deferral_ledger.md` — that file has no ID column.

**Three corrections this milestone carries (diagnosed at filing 2026-08-11):**
1. Ledger rows #428/#995/#996 blame *"a goopg-built `pg_internal.init` leaves `rd_rules` empty"* and prescribe populating it. **That fix is not expressible.** Upstream `load_relcache_init_file` (`postgres/src/backend/utils/cache/relcache.c:6443-6453`) sets `rd_rules = NULL` unconditionally ("Rules and triggers are not saved"); `write_relcache_init_file` never serialises them; views never pass `RelationIdIsInInitFile`; and `StartupXLOG` deletes every init file at `postgres/src/backend/access/transam/xlog.c:5633` before any backend reads one. The real causes are S5 and S6 below.
2. Those rows name index **2620**. 2620 is `pg_trigger`. The index `RelationBuildRuleLock` scans is **2693** (`pg_rewrite_rel_rulename_index`, `postgres/src/include/catalog/pg_rewrite.h:57`).
3. `copyInitFiles` (`internal/testport/e2e_failover_goopg_to_pg_test.go:808-844`, 3 call sites) is inert — its own adding commit `30b0716f` (2026-05-17, subject ends "add copyInitFiles workaround") admits *"PG's load_relcache_init_file still rejects the file silently"*, and it was superseded next day by `c09d519e` ("step 3cq proper"). S10 deletes it.

Theme A — Reverse cold start (goopg on a PG-initdb'd directory):
Theme B — Forward cold start (real PG on a goopg-created directory):
Theme C — Real PG hosted on goopg evaluates views (closes "goopg cannot host a real PG that reads any system view"):
Theme F — Findings measured by M0131-S4 (filed 2026-08-11; each is locked into `TestE2E_PGColdStartOnGoopgDataDir` in the FAIL-WHEN-FIXED direction, so landing one turns that assertion red until it is inverted):
Theme D — Hygiene:
Theme F — Crash-state cluster-directory interchange (added 2026-08-11, user directive; removes Themes A/B's clean-shutdown precondition):

**Why:** S3 and S4 both assert `DB_SHUTDOWNED` before handing the directory over, per `0130-0002` §"WAL replay constraint". Two engines are not interchangeable on a directory if the interchange only works when the previous engine exited politely. **Theme F's filing investigation found that each direction already LOSES COMMITTED DATA today** — S11 and S12 are live bug fixes, not features, and land before everything else in the theme.
**Bounds established at filing (do NOT re-plan these):** WAL segment zeroing is a non-issue — goopg zero-fills recycled segments (`internal/wal/writer.go:2369-2379`) where upstream's `InstallXLogFileSegment` does not, so goopg is strictly safer; `CheckRequiredParameterValues` is a no-op in crash recovery (every branch gated on `ArchiveRecoveryRequested`, `xlog.c:5429`/`:5442`); and empty `pg_twophase`/`pg_commit_ts`/`pg_multixact` are all fine forward (`PrescanPreparedTransactions` over an empty dir returns `nextXid`, which is what `StartupSUBTRANS` wants; `StartupCommitTs` is gated on `track_commit_timestamp=false`; `TrimMultiXact` succeeds because initdb's zeroed `pg_multixact/offsets/0000` placeholder is load-bearing). Unlogged relations having no `_init` fork is a "too durable" divergence, not corruption — ledger it, do not absorb it here.
**Theme design:** `docs/design/0131-0012-crash-state-cluster-dir-interchange.md`. Theme F is independent of Themes C/D and may proceed in parallel.

- [ ] **M0131-S24 — MultiXact: durable `pg_multixact` SLRU + `multixact_redo`** — DEFERRED.

## Archived — complete (see `completed_milestones/completed_fix_plan_012.md`)

M0132 (Explicit transactions across the extended query protocol), M0133 (information_schema on disk).

## M0134 — regress-sql `failed`/`not-tried` test-case digestion (filed 2026-08-15)

**Priority: historical.** M0134 ranked next after M-NIGHTLY by user directive of
2026-08-15, and **was declared EXHAUSTED on 2026-09-01** with no remaining
selectable work. **Since 2026-09-14 the `## Current Priority` banner ranks the
plan-parity group M0137–M0143 first; M0134 sits below it with the other
pre-existing milestones.** Milestone doc:
`docs/milestones/0134-regress-sql-failed-not-tried-digestion.md`.

**Per-task discipline (binding, from the milestone doc):**
1. **Design note when a task is selected.** Before implementing, write a design
   note under `docs/design/<task-id>-NNNN-short-slug.md` (`draft` → `accepted`)
   recording the SQL surface, the goopg↔PG 18.3 divergence, the root cause, and
   the PG-oracle citation; index it in `docs/design/README.md`.
2. **Update the inventory CSV when status changes.** When a task's
   implementation changes a case's `status`, update the row in
   `docs/test-port/postgres-oracle-target-inventory.csv` in the same commit:
   **if the status changes to `pass`, the `pass_required` column must also be
   set to `yes`** (in addition to `status → pass`, `rationale` naming the
   verification). Run `make check-testport-inventory`.
3. The four `failed` cases flagged "possible regression, verify" (`mvcc`,
   `reindex_catalog`, `select_having`, `select_implicit`) are re-run at HEAD
   first; a stale pass is flipped to `pass` with a note, not implemented against.

189 cases, one task each. **Filed** as 87 `failed` (M0134-0001..0087, CSV order)
then 102 `not-tried` (M0134-0088..0189, CSV order); **RENUMBERED 2026-08-19 by user
directive** (see below). Per-case gate: `scripts/pg-regress-runner.sh <case>`.

**Priority renumbering — 2026-08-19 (user directive).** Eighteen cases the user
named as higher-value were pair-swapped into the block **M0134-0006..0023**, and
the sixteen tasks they displaced took the vacated numbers in ascending order. The
other 155 tasks keep their filed numbers. **The consequence, stated so no later
loop assumes otherwise: the "`failed` = 0001..0087 / `not-tried` = 0088..0189"
band invariant no longer holds** — e.g. `select_parallel` (`not-tried`) is now
0008 and `float4` (`failed`) is now 0153. Each task line carries its own
`` `failed` ``/`` `not-tried` `` word, which is the authority; the ID band is not.
The new 0006..0023, in the order the user listed them: `select_having` (was 0066),
`select_implicit` (0067), `select_parallel` (0166), `select_views` (0068),
`predicate` (0153), `subselect` (0071), `update` (0082), `insert` (0033), `mvcc`
(0048), `join` (0036), `create_table` (0010), `hash_index` (0027), `create_index`
(0008), `indexing` (0031 — the user's "index.sql", which does not exist upstream),
`stats` (0171), `vacuum` (0084), `window` (0085), `write_parallel` (0187). The
listed `select.sql`, `delete.sql` and `sysviews.sql` already carry CSV status
`pass` and so have no M0134 task to promote. Full old→new table:
`docs/milestones/0134-regress-sql-failed-not-tried-digestion.md`.

- [ ] **M0134-0001 — aggregates.sql** — regress-sql `failed`.
- [ ] **M0134-0002 — alter_table.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0003 — arrays.sql** — regress-sql `failed`.
- [ ] **M0134-0004 — cluster.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0004-a — `CREATE DATABASE ... TEMPLATE` drops table owners**.
- [ ] **M0134-0005 — constraints.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0008 — select_parallel.sql** — PARKED.
- [ ] **M0134-0009 — select_views.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0010 — predicate.sql** — PARKED.
- [ ] **M0134-0011 — subselect.sql** — PARKED.
- [ ] **M0134-0012 — update.sql** — regress-sql `failed`.
- [ ] **M0134-0013 — insert.sql** — regress-sql `failed`.
- [ ] **M0134-0014 — mvcc.sql** — PARKED.
- [ ] **M0134-0015 — join.sql** — PARKED.
- [ ] **M0134-0016 — create_table.sql** — regress-sql `failed`.
- [ ] **M0134-0018 — create_index.sql** — PARKED.
- [ ] **M0134-0019 — indexing.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0020 — stats.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0021 — vacuum.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0022 — window.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0023 — write_parallel.sql** — PARKED.
- [ ] **M0134-0024 — generated_virtual.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0025 — groupingsets.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0026 — guc.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0027 — copy.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0028 — horology.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0029 — identity.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0030 — incremental_sort.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0031 — copy2.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0032 — inherit.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0033 — create_procedure.sql** — regress-sql `failed`.
- [ ] **M0134-0034 — insert_conflict.sql** — regress-sql `failed`.
- [ ] **M0134-0035 — interval.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0036 — create_table_like.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0037 — join_hash.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0038 — json.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0039 — jsonb.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0040 — jsonb_jsonpath.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0041 — jsonpath.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0042 — lock.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0043 — matview.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0044 — merge.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0045 — misc.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0046 — misc_functions.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0047 — multirangetypes.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0048 — create_view.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0049 — numeric.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0050 — numeric_big.sql** — regress-sql `failed`.
- [ ] **M0134-0052 — partition_join.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0053 — partition_prune.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0054 — plancache.sql** — regress-sql `failed`.
- [ ] **M0134-0055 — plpgsql.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0056 — portals.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0057 — prepared_xacts.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0058 — random.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0059 — rangefuncs.sql** — regress-sql `failed`.
- [ ] **M0134-0060 — rangetypes.sql** — regress-sql `failed`.
- [ ] **M0134-0061 — regex.sql** — regress-sql `failed`.
- [ ] **M0134-0063 — returning.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0064 — rowtypes.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0065 — rules.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0066 — date.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0067 — domain.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0069 — sequence.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0070 — strings.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0071 — equivclass.sql** — regress-sql `failed`.
- [ ] **M0134-0072 — temp.sql** — regress-sql `failed`.
- [ ] **M0134-0073 — tidrangescan.sql** — regress-sql `failed`.
- [ ] **M0134-0074 — tidscan.sql** — regress-sql `failed`.
- [ ] **M0134-0075 — timestamp.sql** — regress-sql `failed`.
- [ ] **M0134-0076 — timestamptz.sql** — regress-sql `failed`.
- [ ] **M0134-0077 — transactions.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0078 — triggers.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0079 — tuplesort.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0080 — txid.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0081 — updatable_views.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0082 — explain.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0083 — uuid.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0084 — expressions.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0085 — fast_default.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0086 — with.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0087 — xid.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0088 — alter_generic.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0089 — alter_operator.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0090 — amutils.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0141 — memoize.sql** — PARKED.
- [ ] **M0134-0142 — misc_sanity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0143 — money.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0145 — object_address.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0146 — oidjoins.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0147 — opr_sanity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0148 — password.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0149 — path.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0157a — parser: statement nodes reporting `Pos() == 0`**.
- [ ] **M0134-0157b — non-deterministic function overload resolution**.
- [ ] **M0134-0158a — publication grammar subset + `pg_relation_is_publishable`**.
- [ ] **M0134-0158b — `~~`/`!~~`/`~~*`/`!~~*` are not operators in goopg**.
- [ ] **M0134-0159 — regproc.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0159a — reg\* function-style casts are echo stubs**.
- [ ] **M0134-0159b — the `to_reg*` soft-error family is undispatched**.
- [ ] **M0134-0159c — goopg's system B-tree cannot split**.
- [ ] **M0134-0160 — reloptions.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0160a — the WITH clause is a map, so PG's list semantics are lost**.
- [ ] **M0134-0160b — `ALTER … SET (reloptions)` applies 4 of the 24 options it validates**.
- [ ] **M0134-0161 — replica_identity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0162a**.
- [ ] **M0134-0162b**.
- [ ] **M0134-0162c**.
- [ ] **M0134-0163 — rowsecurity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0163a — row-level security is never enforced at scan time**.
- [ ] **M0134-0163b — `pg_policies` system view does not exist**.
- [ ] **M0134-0163c — `CREATE POLICY … AS <bogus>` is accepted instead of erroring**.
- [ ] **M0134-0164 — sanity_check.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0164a — `pg_index` describes no bootstrap system-catalog index**.
- [ ] **M0134-0165a — `client_min_messages` rejects upstream's hidden `info`/`debug` aliases**.
- [ ] **M0134-0165b — `plpgsql.sql` is nondeterministic at `select * from f1(42)`**.
- [ ] **M0134-0166a — hyperbolic / degree-trig / gamma / error-function family entirely unimplemented**.
- [ ] **M0134-0166b — `@` (float8abs) and `&#124;/` (dsqrt) prefix operators are unlexed**.
- [ ] **M0134-0166c — `trunc`/`ceil`/`ceiling`/`floor` of a large float8 overflow through int64**.
- [ ] **M0134-0166d — float8 arithmetic is evaluated in decimal, not float64**.
- [ ] **M0134-0167a — no SP-GiST access method**.
- [ ] **M0134-0167b — explicit `ASC` / `NULLS LAST` still accepted on orderless AMs**.
- [ ] **M0134-0167c — capability gate not applied to the constraint-side index paths**.
- [ ] **M0134-0167d — `pg_get_indexdef` prints a Go value dump for a COLLATE in a partial-index predicate**.
- [ ] **M0134-0168 — sqljson.sql** — PARKED.
- [ ] **M0134-0168a — SQL/JSON constructor & predicate family (whole subsystem)**.
- [ ] **M0134-0168b — `\d` lists a partitioned table's per-partition FK constraints under "Referenced by:"**.
- [ ] **M0134-0168c — reg\* input errors report the cast position, not the literal's**.
- [ ] **M0134-0168d — `regtype`/`regrole`/`regnamespace` string casts still fall through on a miss**.
- [ ] **M0134-0168e — `pg_get_viewdef('nosuch'::regclass)` returns empty instead of raising**.
- [ ] **M0134-0168f — the whole `to_reg*` builtin family is missing**.
- [ ] **M0134-0169 — sqljson_jsontable.sql** — PARKED.
- [ ] **M0134-0169a — `pg_get_viewdef` echoes a view's raw source text instead of re-deparsing it**.
- [ ] **M0134-0169b — decide `copy_inner`'s `select_bare` against `gram.y`**.
- [ ] **M0134-0170 — sqljson_queryfuncs.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0171 — foreign_key.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0172 — stats_ext.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0173 — stats_import.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0174 — subscription.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0174a — **.
- [ ] **M0134-0174b — **.
- [ ] **M0134-0174c — **.
- [ ] **M0134-0174d — **.
- [ ] **M0134-0174e — **.
- [ ] **M0134-0175 — tablesample.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0175b — a LATERAL outer column cannot be used as a sample argument**.
- [ ] **M0134-0175c — TABLESAMPLE on a view or CTE is silently honoured instead of raising 42809**.
- [ ] **M0134-0175d — sample arguments are not coerced to float4, and bool→int has no cast arm**.
- [ ] **M0134-0175e — a second `FETCH FIRST` on an already-scrolled SCROLL CURSOR returns 0 rows**.
- [ ] **M0134-0176 — tablespace.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0176a — `ALTER {TABLE|INDEX|MATERIALIZED VIEW} ALL IN TABLESPACE` is unparsed**.
- [ ] **M0134-0176b — `pg_tablespace_location()` is catalogued but has no handler**.
- [ ] **M0134-0178 — tsdicts.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0179 — tsearch.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0180 — tsrf.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0181 — tstypes.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0182 — type_sanity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0183 — typed_table.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0186 — without_overlaps.sql** — PARKED.
- [x] **M0134-0187 — generated_stored.sql** — regress-sql `failed`.
- [x] **M0134-0188 — xml.sql** — regress-sql `not-tried` (PARKED).
- [x] **M0134-0189 — xmlmap.sql** — regress-sql `not-tried` (PARKED).

## M0135 — SQL/JSON `jsonpath` subsystem (filed 2026-08-20, from M0134-0039/0040/0041 sizing)

Design: `docs/design/m0135-0001-jsonpath-subsystem.md`. goopg has no jsonpath
(SQL/JSON path language) lexer, parser, canonical pretty-printer, or evaluator
anywhere — only `pg_type`/`pg_proc` catalog scaffolding for the `jsonpath` type
and the `jsonb_path_*` function family. Confirmed the dominant root cause of
three regress-sql cases (M0134-0039 `jsonb.sql`'s `@@`/`@?` bucket, M0134-0040
`jsonb_jsonpath.sql` 99.9% of its diff, M0134-0041 `jsonpath.sql` 100% of its
diff via a different failure shape — type-I/O canonicalization, not uncalled
functions). Selection order **within M0134's normal ordering** — not
auto-prioritized ahead of M0134's remaining single-file tasks; select per the
`## Current Priority` banner when it names M0135, or opportunistically if a
loop lands on one of the three still-open regress files and wants to unblock it
properly instead of parking a fourth time.

- [ ] **M0135-S1 — jsonpath lexer + parser + canonical pretty-printer (type I/O only)**.
- [ ] **M0135-S2 — jsonpath evaluator core**.
- [ ] **M0135-S3 — wire `jsonb_path_*` functions + `@?`/`@@` operators**.
- [ ] **M0135-S4 — `pg_input_is_valid`/`pg_input_error_info('jsonpath')` soft-error surfacing**.

## M0136 — `tsvector`/`tsquery` core type engine (filed 2026-09-01, from M0134-0181 sizing)

Design: `docs/design/0100-0149/m0134-0181-tstypes-sizing.md`. goopg has NO
`tsvector`/`tsquery` type kernel anywhere — only `pg_type`/`pg_proc` catalog
scaffolding. `SELECT '1'::tsvector` "succeeds" only because goopg falls back
to an opaque-type text passthrough for a type with no registered I/O
function, not because a real parser exists; calling `tsvectorout(...)`
explicitly errors `function tsvectorout does not exist`. This milestone is
narrower and more foundational than the already-parked M0134-0178/0179
dictionary/stemmer gap (ledger row 0178a, `postgres/src/backend/tsearch/`):
it needs no tokenizer, stemmer, or `pg_ts_config`/`pg_ts_dict` lookup — only
the type itself, its operators, and its non-dictionary utility functions.
Landing S1 alone unblocks a real re-measurement of M0134-0181 (`tstypes.sql`)
and is a genuine prerequisite for the *result* type of `to_tsvector` once
0178a eventually lands. Selection order **within M0134's normal ordering**
— not auto-prioritized ahead of M0134's remaining single-file tasks; select
per the `## Current Priority` banner when it names M0136, or opportunistically
if a loop lands on `tstypes.sql`/`tsdicts.sql`/`tsearch.sql`/`tsrf.sql` and
wants to unblock the shared type-kernel gap properly.

- [ ] **M0136-S1 — `tsvector`/`tsquery` type kernel (parse + canonical output only)**.
- [ ] **M0136-S2 — `tsvector` comparison + editing/utility functions**.
- [ ] **M0136-S3 — `@@` match operator + `<->` phrase-distance operator**.
- [ ] **M0136-S4 — `ts_rank`/`ts_rank_cd` scoring**.
## M0137 — Parity measurement harness and instrument repair (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0137-parity-measurement-harness-and-instrument-repair.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Source:** `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md` Phase 0 (M2/M3/M5/M6)

**First in the plan-parity group, and a prerequisite for the other six.** Every
other milestone is judged by measurement, and the instruments have decayed: the
K18 `$$`-tempfile trap produced false structural readings in four rounds (R122,
R123, R124, R128) and is still live; captures are hand-stamped or unstamped;
stats-epoch drift measured at 1.31x on Q9 exceeds most effects being claimed;
`make plan-gate` has been a standing opt-out since R65. K91's rule is what this
milestone restores — a harness must fail loudly, and an A/B must prove which
binary answered; where it cannot, the result is not evidence.

**Per-task discipline:** design note written **when the task is selected**
(`docs/design/<task-id>-NNNN-short-slug.md`, indexed in `docs/design/README.md`
in the same commit). No new `rNNN-*` round directories; raw artefacts go under
`analysis/m0137/`. Every instrument change lands with a test or a recorded
before/after proving the defect it closes.

- [x] **M0137-0001 — promote the parity capture pipeline into `scripts/` and fix the K18 `$$` trap** — two divergent copies exist inside round directories
  (`plan_parity_fix_take2/methodology/` and `.../r2-instrument/`), both embedding `$$`
  in the temp filename, which lands in Q36/Q70/Q86's psql ERROR text and makes any two
  runs diff spuriously. Land one canonical copy under `scripts/`, fix the trap at
  source, and add a test that two consecutive captures of an unchanged binary diff
  empty. Retire the round-directory copies from every procedure.
  - DONE 2026-09-15: `scripts/capture-tpch.sh` + `scripts/capture-tpcds.sh` landed
    (5-arg `<port> <db> <user> <out> <hdr>` signature, `REPO_ROOT`/`PG_BIN`-relative
    PATH like `pg-oracle-diff.sh`, TPC-DS sections normalised to `=== Qn` at capture
    time). Scratch filename now derives from `$(basename "$OUT")`, never `$$`.
    `scripts/capture-idempotent-test.py` stubs `psql` (no live cluster) and asserts
    two captures into the same `$OUT` are byte-identical; manually verified it fails
    against a reintroduced `$$` trap. `METHODOLOGY.md` §4.1 updated to point at the
    new scripts; round-directory copies left as frozen history, not edited.
    Design doc `docs/design/0100-0149/m0137-0001-capture-pipeline-k18-fix.md`.
    Deferred: TPC-H query source (`TPCH_QUERY_DIR`) still defaults to an untracked
    `/tmp` path — ledger row filed 2026-09-15, resume point M0137-0003.
- [x] **M0137-0002 — machine-stamp every capture artefact** — binary path, inode,
  serving-PID `/proc/<pid>/exe` verification, flag arm, pinned GUCs and stats epoch,
  written by the tool. R122 §10's own words are the defect: the headline arm
  attribution "rests entirely on filename convention". This is the gap that let a
  flag-OFF run be cited as ON evidence (R120 B1).
  - DONE 2026-09-15: `scripts/lib/capture-stamp.sh` (new) sourced by both
    capture scripts, reusing `scripts/lib/bench-engine-id.sh` and
    `scripts/planner-flags.sh` (the TPC-DS SF0.25 sweep's already-proven
    provenance machinery) instead of a new format. Appends `# engine-id:`,
    `# repo-head:`, `# planner-flags:`, `# pinned-GUCs:`, `# engine-binary:`,
    `# stats-epoch:` to every capture header. Both scripts gained an optional
    6th `[datadir]` arg so `# engine-binary:` can read `<datadir>/postmaster.pid`
    and report `pid=/pid-alive=/path=/inode=/sha=`; omitted, it reads
    `UNKNOWN(no datadir given)` rather than guessing. `# stats-epoch:` hashes
    `(relname, n_live_tup)` over `pg_stat_user_tables` (a fingerprint, not
    `last_analyze` — goopg's is always NULL) via a K18-safe deterministic
    scratch file. Every field degrades to an explicit `UNKNOWN(reason)`,
    verified for 3 failure modes. `capture-idempotent-test.py` extended
    2->4 tests (stamp-field presence + populated-vs-UNKNOWN binary line).
    Design doc `docs/design/0100-0149/m0137-0002-capture-machine-stamp.md`.
    Deferred to M0137-0006: turning the stats-epoch stamp into a checked/
    enforced step rather than a passive artefact field.
- [x] **M0137-0003 — write the canonical baseline-capture procedure** — record that
  TPC-H baselines come from `estimate-audit -plan-only`, **not** `capture-tpch.sh`
  (which opens a fresh session per query and never ANALYZEs, so it captures TPC-H
  plans on empty stats); and that `-serial` defaults **true**
  (`cmd/estimate-audit/main.go:285`), so TPC-H `parallelism 0` is measured *out*, not
  solved. Include the TPC-DS procedure and the pinned GUCs for both corpora.
  - DONE 2026-09-15: `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`
    — replaces AGENT.md's deleted "plan-parity-take2 work appendix" (bootstrap
    PATH/`PGPASSWORD`, port/db/user/password table for both corpora), gives
    the exact `estimate-audit -plan-only` command for TPC-H and
    `capture-tpcds.sh` invocation for TPC-DS, and cites the direct 2026-09-14
    probe (`TODO.md:4861-4875`) establishing WHY TPC-DS is unaffected by the
    same per-connection-stats trap that hits TPC-H (its load-time ANALYZE
    persists durably; TPC-H's does not for a fresh session). Live-validated
    the exact TPC-H command against a throwaway private server end-to-end
    (schema+sample data via `tpch.DDL()`/`tpch.SampleInserts()`, not a shared
    `:6543x` cluster).
  - Also resolved the M0137-0001 ledger deferral on `capture-tpch.sh`'s
    untracked `TPCH_QUERY_DIR`: decided the script is demoted to a
    non-baseline convenience tool (off the critical path now that
    `estimate-audit` owns TPC-H baselines), and added a fail-fast
    `TPCH_QUERY_DIR`/`TPCH_Q15A_FILE` existence check so a missing corpus
    errors once instead of emitting 22 silent "MISSING QUERY FILE" sections.
    `.ralph/deferral_ledger.md` row flipped to `resolved`.
  - Updated `AGENT.md`'s two dangling pointers to the deleted appendix (now
    point at the new doc) and `METHODOLOGY.md` §4.1 (added the TPC-H caveat
    inline, since it is still cited as the live measurement-pipeline doc).
  - Left open, explicitly not this task's job: the TPC-DS `match=2` vs
    `match=1` reference question (M0137-0004) and turning the stats-epoch
    stamp into a checked step (M0137-0006).
- [x] **M0137-0004 — reconcile the TPC-DS `match=2` vs `match=1` discrepancy** — both
  figures come from the same capture-script family; the difference is the **reference
  and the session GUCs** (live PG `:65438` vs the committed `bench/tpcds/plans-pg`
  fixture), not the tool. Declare one canonical reference and rule on the fixture's
  standing the way K9 rules on the TPC-H one. Stop the programme quoting both numbers.
  - DONE 2026-09-15: re-measured with the current canonical `scripts/capture-tpcds.sh`
    (post K18-fix, post machine-stamping) against goopg SF0.25 `:65437` — **both**
    live PG `:65438` and the committed `bench/tpcds/plans-pg` fixture now give
    `match=2` (Q9, Q41). R128's `match=1` (captured with the since-retired
    `methodology/capture-tpcds.sh`, pre-fix/pre-stamping) does not reproduce at HEAD.
  - The only per-query divergence between the two references is Q2 and Q75 (both
    non-MATCH either way): PG's own plan for these two flips between the fixture's
    2026-09-11 capture and today's live capture (Hash Join/HashAggregate/Gather vs
    Merge Join/GroupAggregate/Gather Merge) — PG-side tied-cost-margin oscillation
    (N26's class), not a goopg-side change.
  - Ruling: canonical reference = live PG `:65438` via `scripts/capture-tpcds.sh`.
    Unlike K9's TPC-H fixture (categorically stale/serial), `bench/tpcds/plans-pg`
    is not categorically wrong — it currently reproduces the same match count as
    live PG — so it stays a valid secondary/regression reference (e.g. `make
    plan-gate`) but is demoted from co-canonical to corroborating for parity-count
    claims. AGENT.md's "Success criterion" floor updated to TPC-DS `match >= 2`;
    `m0137-0003`'s doc's open-question caveat dropped.
  - Design doc `docs/design/0100-0149/m0137-0004-tpcds-match-reference-reconciliation.md`.
    `METHODOLOGY3/` left untouched (frozen 2026-09-14 stocktake). No production
    code touched; no ledger row (Q2/Q75 is evidence about oracle stability, not an
    unimplemented PG behaviour in goopg).
- [x] **M0137-0005 — re-baseline `make plan-gate`** — it is a goopg-vs-committed-goopg
  baseline pin (`Makefile:431-453`), **not** a diff against live PG, and its baseline
  has not been refreshed across ~125 rounds of intentional plan change. Land the
  re-baseline as its own commit with the 20 diverging queries adjudicated or
  explicitly carried, so the gate is a live signal again.
  - DONE 2026-09-15: confirmed the stale baseline (`warm-pin-20260905.txt`, mtime-newest
    of 18 files) DIFFERed on exactly 20/22 queries as the line claimed. Node-type census
    attributed the drift to already-landed mechanism classes (parallel/`Gather`
    adoption, index-scan narrowing, `Memoize`, agg-strategy flips); a handful of
    same-node-count-but-DIFFER queries flagged for M0137-0010's qual-placement census
    rather than root-caused here. `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=34)
    before pinning. Captured `plan_snapshots/m0137-0005-rebaseline-20260915.txt`;
    `make plan-gate` now 22/22 MATCH. Design doc
    `docs/design/0100-0149/m0137-0005-plan-gate-rebaseline.md`. No production code
    touched; no ledger row (instrument re-pin, not a discovered PG incompatibility).
- [x] **M0137-0006 — make the stats-epoch declaration a checked step** — a values
  sweep re-samples statistics and opens a new epoch; R120 attributed two apparent
  "worsenings" to drift rather than the flag and left a rule no tool enforces.
  Every A/B artefact declares its epoch on both arms, and re-taking the OFF baseline
  after a sweep becomes a verified step rather than a remembered one.
  - DONE 2026-09-15: new `scripts/check-stats-epoch.sh <file> <file> [...]` reads
    each artefact's `# stats-epoch:` line and exits 0 only if all present/known/
    identical; exit 1 (both values printed) on a mismatch or an `UNKNOWN(reason)`
    epoch on either side; exit 2 if a file has no stamp at all. Also stamped the
    previously-unstamped canonical TPC-H tool `cmd/estimate-audit` (behind
    `scripts/tpch-estimate-audit-arm.sh`'s `PGSHAPED=0`/`PGSHAPED=1` A/B runs) with
    the SAME `sha256(relname|n_live_tup)`-first-16-hex fingerprint formula
    `scripts/lib/capture-stamp.sh` uses (M0137-0002), computed from its own
    already-open connection, degrading to `UNKNOWN(reason)` rather than `fatal()`.
    Cross-tool formula equality pinned by `TestHashStatsEpochRowsMatchesCaptureStampFormula`
    (hand-verified against the bash formula). `scripts/check-stats-epoch-test.py`
    (8 tests, incl. an end-to-end run through the real stubbed-`psql`
    `capture-tpch.sh`) plus 4 new Go tests in `cmd/estimate-audit/main_test.go`.
    Live-validated against a real throwaway goopg server (M0137-0003's probe
    precedent): two untouched-server captures MATCH on a real hash; after
    doubling `nation`'s rows + `ANALYZE nation`, a third capture's epoch changed
    and the checker reported MISMATCH — the exact live defect class this task
    closes. `m0137-0003`'s procedure doc gains §4a naming this as the required
    pre-diff step. Design doc
    `docs/design/0100-0149/m0137-0006-stats-epoch-checked-step.md`. Named out of
    scope: pure-timing arm runners (`tpch-acceptance-arm.sh`) that never touch a
    plan/estimate artefact — a different instrument/question from N23's. No
    Makefile/precommit/CI wiring (matches every sibling regression test in this
    milestone, all run manually). No production planner/executor/catalog code
    touched.
- [x] **M0137-0007 — give each lane a private clone and a private port** —
  shared-resource contention on `:65433` is the stated reason for most deferred gates
  (`tpch-spotcheck.sh` was deferred four rounds running while being the only gate that
  caught the Q13 33-vs-34 wrong-rows bug). This is an infrastructure problem with an
  infrastructure fix.
  - Landed 2026-09-15: `docs/design/0100-0149/m0137-0007-private-clone-per-lane.md`.
    Found the defect is wider than the cited example — FOUR scripts
    (`tpch-spotcheck.sh`, `tpch-relsize-arm.sh`, `tpch-estimate-audit-arm.sh`,
    `tpch-acceptance-arm.sh`) all independently `stop -D`/`start -D` the same shared
    `bench/tpch/runtime_goopg/data`/`:65433`. New `scripts/lib/tpch-private-clone.sh`
    generalises `ci/batch/stages/stage-tpch.sh`'s already-proven snapshot-clone
    pattern; all four scripts now run on their own private port (5580-5583) and
    private clone dir under `tmp/`, touching the shared cluster only via a passive
    wait-then-copy (never stop/start).
  - Live-validated end-to-end against the real 2 GB `:65433` cluster, including a
    reproduced contention scenario (started a real server on `:65433`, ran spotcheck
    with a short clone-wait against it) proving the shared server survives untouched
    where the old code would have killed it via an unconditional `stop -D`.
  - Out of scope, not ledgered: TPC-DS captures (no server stop/start, so the acute
    hazard doesn't apply) and the M0137-0003 baseline-capture tools
    (`estimate-audit -plan-only`/`capture-tpch.sh` deliberately target the shared
    cluster itself — they measure ITS state).
- [x] **M0137-0008 — build `INDEX-by-query.md` and `INDEX-by-mechanism.md` over the
  round corpus** — one row per TPC-H/TPC-DS query and per mechanism, naming the rounds
  that touched it and their standing verdict, so a scope can cite prior work instead of
  re-deriving it. R127 was withdrawn on nine findings, three fatal, every one refuted
  by a document already on disk; the written "grep the directory first" warning was
  authored *before* R130 and still did not work. Make citing the index a scope gate.
  - DONE 2026-09-15: both files landed at
    `docs/design/not_ralph/plan_parity_fix_take2/INDEX-by-query.md` (23 TPC-H +
    86 TPC-DS query rows, oldest→newest round list + last verdict) and
    `INDEX-by-mechanism.md` (14 mechanism-tag sections, 6-59 rounds each) —
    alongside the round directories they index, matching where `AGENT.md`'s
    harness section already expected them. Built from six parallel read-only
    subagents (one per ~20-round slice of all 126 `rN-*` directories) each
    extracting title/queries/mechanisms/verdict from `REPORT.md`/`DESIGN.md`
    without reading the large `*.plans.txt`/`*.diff.txt` dumps, then a
    one-shot Python regroup script (not committed — one-shot tool, not a
    generator). One data-quality fix needed: 3 rounds folded a TPC-DS
    control-query mention into the free-text TPC-H cell; fixed by capping
    the TPC-H extraction to `Qn`, n<=22 (TPC-H's real range). Design doc
    `docs/design/0100-0149/m0137-0008-round-corpus-index.md`. No production
    code touched; no ledger row (retrieval tooling over already-written
    reports).
- [x] **M0137-0009 — retire the flag and ledger debt** — delete
  `GOOPG_HASHAGG_WIDTH_CURRENCY` (R124 §7 resolved its promote-or-delete to **delete**;
  it ships default-OFF and net-negative), state the default-off arm cap in the harness,
  and close or delete each ledger item carried unretired for ten or more rounds.
  - DONE 2026-09-15 (flag + cap clauses): `internal/optimizer/hashagg_widthcurrency.go`
    and its test file deleted; `cost_funcs.go`'s HashAggregate spill arm permanently
    reverted to the flag's former OFF arithmetic (bare `inAvgVarBytes` as the tuple
    width, a known residual PG-currency divergence the doc comment now names,
    pointing at K65/K66 ncols-narrowing as the real fix); `flaglabels.go` moves the
    flag into `flagProvenanceRetired["GOOPG_HASHAGG_WIDTH_CURRENCY"] = "M0137-0009"`
    (the same pattern used for the five prior retired flags); `scripts/planner-flags.env`
    regenerated. `AGENT.md`'s harness "Known-stale claims" bullet now states R121 §6's
    never-landed norm verbatim (a default-off cost arm needs an explicit expiry; past
    ~4 at once is debt) and corrects the count to **two** arms at HEAD
    (`GOOPG_PG_HASH_TUPLE_SPILL_COST`, `GOOPG_PG_SORT_RELATION_BYTES_COST`).
    `go build`/`go test ./internal/optimizer/...` clean. Design doc
    `docs/design/0100-0149/m0137-0009-retire-hashagg-width-currency.md`.
  - The third clause (ten-plus-round ledger carry, `03-process-retrospective.md` P7)
    is SPLIT OUT to **M0137-0013** below rather than attempted in the same loop — see
    the design doc's "What landed" §3 for why (ten independent per-item
    determinations across `TODO.md`'s round history is campaign-sized, a different
    kind of task from a single already-adjudicated flag deletion; the harness's own
    R121-vs-R108/R113/R120 distinction warns against flattening the two).
- [x] **M0137-0011 — root-cause the second display/estimator seam (C3/K63)** — R76 P1
  saw Q22 display rows 16,666 against a stamped 18,200 and called it "a second
  estimator seam"; R77 took the post-pass branch and noted "gates did not complain",
  and it was never diagnosed. K63 is the general form: goopg's EXPLAIN reports a scan
  cost the planner did not use (4.5x on Q12), which "corrupts every cost-based
  artefact including `plan-gate MODE=semantic-cost` and estimate audits". This is an
  **instrument** defect and therefore belongs in this milestone, not in a costing one.
  - DONE 2026-09-15 (recon only, no production diff): reproduced R37's Q12 capture
    byte-for-byte at HEAD, then instrumented `stampPlanCost`
    (`internal/optimizer/plancost.go`) and `explainCostFields`
    (`internal/executor/operators_explain.go`) with a temporary env-gated trace
    against the live `bench/tpch` 65433 lane (fully reverted, `git diff --stat`
    empty on both files). Root cause: `SeqScan` embeds `PlanCost`
    (`plan.go:642`) but `Filter` does not (`plan.go:1531-1569`) — a base-local
    filtered scan (`lineitem` in Q12) reaches `buildInitialRels` wrapped as
    `Filter{Child: SeqScan}`, so `stampPlanCost`'s `n.(planCostSetter)`
    assertion silently fails on it (traced: the correct 271,421.24 is stamped
    onto the Filter and then read back `CostSet=false` at render time from the
    SAME pointer), and EXPLAIN falls back to `DeriveLegacyDisplayCost`'s
    childless-leaf formula (no page cost) — R37's exact "legacy model" number.
    Confirms K62 (display-only, cannot move a plan-choice category) while
    widening the blast radius from one query's symptom to every base-local-
    filtered scan reaching `buildInitialRels`. Fix shape named (embed
    `PlanCost` on `Filter`) but not implemented — filed as
    `.ralph/deferral_ledger.md` row `m0137-0011-filter-node-missing-plancost-embed`.
    Design doc `docs/design/0100-0149/m0137-0011-c3-k63-display-seam-root-cause.md`.
- [x] **M0137-0012 — file ledger rows for the four unowned carry-overs** — one row each,
  with mechanism and resume point, so they stop being invisible: **B6** (no Memoize on
  the NL probe path R59 repriced; Q72 4s -> 320s TIMEOUT), **B8**
  (`indexProbeCostMultiplier = 2.0` — parity vs wall-clock, 2–3x slower at `mult = 1`;
  sibling K73 `Join.FromOuterReduction`), **B10** (`corr = 0` fallback pricing every
  such index scan at `max_IO_cost`, plus R30's synthesised index geometry), and
  **O15** (`*Gather` crossing still deliberately excluded from `pushConjunctTraced` —
  the same class as R56's Q78 defect, which M0137-0010's census will *detect* but
  nothing currently schedules *fixing*). Filing only; no code change.
  - DONE 2026-09-15: four rows appended to `.ralph/deferral_ledger.md`
    (`m0137-0012-b6-no-memoize-nl-probe`, `m0137-0012-b8-indexprobe-multiplier-parity-vs-wallclock`,
    `m0137-0012-b10-corr-zero-fallback-max-io-cost`,
    `m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced`), each re-derived
    against the tree at HEAD (exact file/line resume points) rather than copied
    verbatim from `METHODOLOGY3/02-open-problems.md`. Recon task, no production
    change. Design doc `docs/design/0100-0149/m0137-0012-unowned-carryover-ledger-rows.md`.
- [x] **M0137-0010 — add the qual-placement census and the duplicate-sensitive values
  check** — both justified by bugs that shipped: R56's Q78 lost three `Filter:` lines
  with sweep checksums still passing, and R83's Limit-below-Unique truncation was
  masked on current data only because `78 < 100`. The census counts `Filter:` /
  `Index Cond:` lines per query and diffs them between arms; it becomes the gate every
  M0139 slice runs.
  - DONE 2026-09-15: `scripts/qual-placement-census.py` (+ `-test.py`, 8 tests)
    landed — a same-engine-arm-pair census (no PG oracle needed), report-only
    sibling `pg-plan-parity-diff.py` cannot fill since it only diffs
    (goopg, PG) pairs. Exits nonzero (a real gate, per K91) on any
    `Filter:`/`Index Cond:` count change or a query present in only one arm.
    Live-validated against a real on-disk flag-flip A/B
    (`analysis/planner-refactor-take3/c06-flip-remeasure-20260907/`),
    `mismatch=0`. Also closed C1 (`02-open-problems.md`): added the
    ParamRef duplicate-sensitive synthetic case 04's M5 item 2 named
    (`TestDistinctLimitAppliesAboveDistinct_ParamRef`), confirmed it failed
    at HEAD as predicted, then fixed `limitBoundMovable`
    (`internal/optimizer/tuplefraction.go`) to also accept `*ParamRef`
    (a bound LIMIT value is exactly as position-independent as a literal
    for this purpose) so `LIMIT $1` now moves above `DISTINCT` exactly like
    `LIMIT 100`. `go build`/`go test ./internal/optimizer/...
    ./internal/executor/...` clean. Design doc
    `docs/design/0100-0149/m0137-0010-qual-placement-census-and-duplicate-check.md`.
    No ledger row (both deliverables landed in full).
- [x] **M0137-0013 — close or delete the ten-plus-round ledger carries (filed
  2026-09-15, split out of M0137-0009; DONE 2026-09-15)** — `03-process-retrospective.md`
  P7's "Ledger carry" finding: *"the same items appear verbatim across ten-plus rounds:
  R51 items 2–3, R52 §4.2, R54 follow-ups, '#6', R61-#4, the NLI staleness comment,
  R55 §3 tie-break, F3 procost, the Q8 gap, AGG_MIXED"* — and R67 §3's own
  legislation against the pattern went unenforced (*"the carry continued"*).
  Traced all ten through `TODO.md`'s round log and `02-open-problems.md`. Result
  (full determinations in
  `docs/design/0100-0149/m0137-0013-ledger-carry-determinations.md`): 3 items
  discharged by later rounds — **(a) close** (R52 §4.2 by R54's Redesign LANDED
  2026-09-11; 2 of R54's 3 follow-ups by R55/R56 LANDED 2026-09-11); 1 item
  superseded by broader instrumentation — **(c) delete** (R51's DP-trace
  re-verification ask, superseded by R53 Step-0's DPTRACE machinery); 6 items
  already carry ONE permanent non-duplicative home in `02-open-problems.md`
  (N45, N50, B4/O9) — **(a) close the carry**, no new filing; 1 item was a real,
  previously-unfiled gap — **(b) new `.ralph/deferral_ledger.md` row**
  `m0137-0013-nli-semi-anti-match-fraction-gap` (`estimateNLIndexJoin` never
  applies `semiJoinMatchFraction` for SEMI/ANTI, unlike its sibling
  `estimateJoin`, confirmed live at HEAD). No `TODO.md`/`rNNN-*` round
  directory edited. No production planner/executor/catalog code touched.
- [x] **M0137-0014 — automate the seam-decline census** — DONE 2026-09-15
  (`docs/design/0100-0149/m0137-0014-seam-decline-census-tool.md`). New
  `scripts/seam-decline-census.py` (+ `-test.py`) aggregates
  `traceSeamDecline`'s `seam-decline reason=<class>` lines from one or more
  `GOOPG_PGSHAPED_DP_TRACE=1` logs into a `SEAM-DECLINE-CENSUS:
  timeout=<label> logs=<n> classes=<k> declines=<total>` report plus a
  sorted per-class count list. `--timeout` is a required free-text label
  (censuses are only comparable at equal timeouts, so the tool refuses to
  let a report omit it). Report-only (exit 0); exit 2 on an unreadable log
  or missing `--timeout`. Verified via `--self-test` (4/4), the `unittest`
  companion (7/7), and a live cross-check against a real committed capture
  (`analysis/planner-refactor-take3/c06-q13-diagnosis-20260907/evidence/dppath-on.txt`)
  matching the manual `grep|sort|uniq -c` fallback byte-for-byte. No
  production code touched; no new ledger row (pure tooling).
- [x] **M0137-0015 — embed `PlanCost` in `optimizer.Filter`** — DONE
  2026-09-15 (`docs/design/0100-0149/m0137-0015-filter-plancost-embed.md`).
  `Filter` now embeds `PlanCost`, mirroring `SeqScan`'s embed; no other code
  changed (`stampPlanCost`'s `planCostSetter` funnel and
  `legacyDisplayChildren`'s pre-existing `*Filter` fallback arm both already
  handled a carrier). Pinned by a new regression test
  (`TestCreatePlanNode_StampsCostOnFilterWrappedPrebuiltLeaf`) and
  live-verified: Q12's `Seq Scan on lineitem` now renders the search's own
  `costSeqscan` number instead of the `DeriveLegacyDisplayCost` fallback.
  Also closes the `*IndexScan`-with-residual-filter case M0137-0011 flagged
  as unaudited (no separate fix needed — `IndexScan` already embeds
  `PlanCost`, and the rebuild path produces the same `Filter` type).
  Corpus-wide blast radius (previously unmeasured): 20/22 TPC-H, 85/99
  TPC-DS queries carry a base-local-filtered scan, per the committed
  PG-oracle plans. Closes `.ralph/deferral_ledger.md` row
  `m0137-0011-filter-node-missing-plancost-embed` (now `resolved`).
- [x] **M0137-0016 — add the `*Gather` arm to `pushConjunctTraced` (O15)** —
  DONE 2026-09-15
  (`docs/design/0100-0149/m0137-0016-gather-arm-pushconjuncttraced.md`).
  Added a bare pass-through `*Gather` case to `pushConjunctTraced`'s switch
  (`internal/optimizer/inner_join_qual_pushdown.go`): recurses into
  `x.Child` and rewraps, leaving `st.proven` untouched since `Gather.Output()
  == Child.Output()` (no coordinate shift, unlike `*Join`; no remap-failure
  surface, unlike `*Project`). Pinned by three new tests
  (`internal/optimizer/pushdown_gather_crossing_test.go`): crosses to the
  correct leaf, proof survives the crossing, composes with a multi-level
  join spine. Corpus-wide blast radius (measured from the committed
  PG-oracle plans): TPC-H 0/22, TPC-DS 37/99 queries place a filter below a
  Gather in PG's own reference plans — no category-count claim made per
  M0140-0003's standing caution. Closes `.ralph/deferral_ledger.md` row
  `m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced` (now
  `resolved`).
- [x] **M0137-0017 — capture plans in BOTH serial and parallel modes** — DONE
  2026-09-15. Ran `estimate-audit -plan-only` with `-serial=true` and
  `-serial=false` against the live TPC-H clusters (goopg `:65433`, PG
  `:65432`), building for the first time a parallel-mode PG reference
  (`bench/tpch/plans-pg/` stays serial-only/non-canonical per K9). Committed
  `analysis/m0137/m0137-0017-{serial,parallel}.*`. Result: `parallelism` —
  read `0` in every prior report because the serial control arm measures it
  *out* — is now scoreable at **16/22**, the largest category besides
  `join-order`; the serial headline's 6/22 match set drops to **2/22**
  (only Q6, Q11 survive) once parallel workers are allowed. Design:
  `docs/design/0100-0149/m0137-0017-serial-and-parallel-capture.md`. Does
  NOT attempt to close any divergence — follow-up is **M0137-0019** below,
  per the completion rule's two-artefact requirement (also
  `.ralph/deferral_ledger.md` row `m0137-0017-parallel-mode-divergence`).
- [x] **M0137-0018 — bring `make ea-ratchet` into this group's gate set** —
  **DONE 2026-09-15**, design doc
  `docs/design/0100-0149/m0137-0018-ea-ratchet-rescore-and-harness-standing.md`.
  Re-scored at HEAD (`4c5c13905`): 99/99 clean capture, `FINDINGS: 140` vs the
  2026-09-07 baseline's 178 (99 FIXED, 61 NEW, `EA-RATCHET: FAIL`). Tracing the
  delta before trusting it as a regression signal found it contaminated: the
  SF0.5->SF0.25 dev-gate migration (`e2a50de40`, 2026-09-11) moved the goopg
  corpus and every PG plan fixture but never re-pinned `ea-baseline.txt`, so
  every ratchet run since has compared across two corpus scales. Resolved by
  re-pinning a fresh, scale-consistent baseline
  (`analysis/planner-refactor-take3/c20a-estimator-census-20260915/`, 140
  findings) and repointing `scripts/estimate-parity-gate.sh`'s `EA_BASELINE`
  default at it; also fixed three stale `SF0.5`/`~1h` mentions in `Makefile`.
  Added `make ea-ratchet` to `AGENT.md`'s harness measurement section as a
  third table (distinct from values-gates and plan-parity capture/score).
  Ledger row `m0137-0018-ea-ratchet-stale-baseline` (resolved). No production
  planner/executor/catalog code touched.
- [ ] **M0137-0019 — triage the 16 parallel-mode `parallelism`-category
  divergences M0137-0017 surfaced** — that task built the first-ever
  parallel-mode PG baseline and measured `parallelism=16/22`,
  `match=2/22` at `-serial=false` (committed:
  `analysis/m0137/m0137-0017-parallel.*`, diffed by
  `scripts/pg-plan-parity-diff.py`; design doc
  `docs/design/0100-0149/m0137-0017-serial-and-parallel-capture.md`). It
  deliberately made no attempt to close any of them. Per-query, decide
  whether each divergence is a plan-selection defect already in M0139's /
  M0141's / M0142's territory, or a Gather-placement/costing gap specific
  to the parallel path (M0140's `GOOPG_GATHER_PATHS=all` territory) —
  start from the 4 queries that regress specifically when parallelism is
  turned on (Q1, Q10, Q14, Q15a-VIEWBODY: MATCH at `-serial=true`,
  SHAPE-DIFF at `-serial=false`), since those isolate a parallelism-only
  cause most cleanly. Do not re-run the capture; the artefacts are already
  committed and stats-epoch-pinned. Ledger:
  `m0137-0017-parallel-mode-divergence`.
- [x] **M0137-0020 — pin `GOOPG_ANALYZE_SEED` in the TPC-DS capture harness**
  (filed by M0138-0008, 2026-09-15) — **DONE 2026-09-15.** The fix does NOT
  land inside `scripts/capture-tpcds.sh` as the task title/deliverable
  assumed: that script only opens client `psql` sessions and never runs
  `ANALYZE` (the TPC-DS load procedure ANALYZEs once, durably, at load time,
  against whatever seed the already-running server process was started
  with), and `GOOPG_ANALYZE_SEED` is a package-level var goopg reads exactly
  once at server-process startup (`operators_analyze.go:704-733`) — an
  `export` inside the capture script would be silently inert against a real
  TPC-DS cluster. Landed instead in `bench/tpcds/env_tpcds.sh` (sourced by
  `bench/tpcds/server.sh` before every goopg TPC-DS server start, and by
  every other `tpcds-*.sh` script): `export
  GOOPG_ANALYZE_SEED="${GOOPG_ANALYZE_SEED:-20260905}"`, same default/
  rationale as `tpch-acceptance-arm.sh`. `scripts/capture-tpcds.sh` and
  `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md` §3
  updated to point at the new pin site. Verified the wiring read-only
  (`unset GOOPG_ANALYZE_SEED; source bench/tpcds/server.sh status` picks up
  the default; an explicit override is preserved) without starting or
  touching either shared cluster — the underlying
  export-before-server-start-implies-reproducible-sample mechanism was
  already proven live at full 99-query scale by M0138-0008, so that
  expensive experiment was not re-run. Design doc:
  `docs/design/0100-0149/m0137-0020-pin-analyze-seed-tpcds-server.md`.
  Deferral ledger: `m0138-0008-category-shift-bisect` row flipped to
  `resolved`.
  - Out of scope, filed as **M0137-0021** below (ledger row
    `m0137-0020-recapture-headline-unowned`): the shared `:65436`/`:65437`
    clusters were loaded before this pin landed and keep wall-clock-seeded
    statistics until next reload, and the milestone review's "TPC-DS
    categories net worse, 525->540" headline still has not been
    re-measured with a pinned seed.
- [ ] **M0137-0021 — re-measure the "TPC-DS categories 525->540" headline
  with the seed pinned** (filed by M0137-0020, 2026-09-15) — the milestone
  review's regression headline was taken with an unpinned
  `GOOPG_ANALYZE_SEED` and, per M0138-0008, carries an unknown amount of
  ±1..2-per-category noise on at least 5 queries (Q26/Q45/Q48/Q51/Q97) from
  reservoir-sampler seed variance alone. M0137-0020 landed the pin
  (`bench/tpcds/env_tpcds.sh`, default `20260905`) but the shared TPC-DS
  clusters (`:65436`/`:65437`) were already loaded before it landed, so
  their on-disk statistics are still wall-clock-seeded. Deliverable: reload
  (or privately clone, per M0138-0008's method) the TPC-DS SF0.25 cluster
  with the pin in effect, recapture the full `CATEGORIES:`/
  `CATEGORIES-EXCL-MATCH:` lines against the same corpus the review used,
  and republish the headline with a stated stats epoch. Ledger:
  `m0137-0020-recapture-headline-unowned`.

## M0138 — PG-faithful ANALYZE statistics (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0138-pg-faithful-analyze-statistics.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Source:** the owner's answer of 2026-09-14 to `METHODOLOGY3/04-forward-plan.md` §1.2 Question 2
**Prerequisite:** M0137.

**The owner directed that goopg reproduce PG's estimates, errors included,
because otherwise identical plan generation is impossible.** Two consequences:
04 §1.2's proposed `PARITY-BLOCKED-BY-ORACLE-ERROR` rule is **rejected**, and
**R79's verdict (b) — "keep the superior statistics" — is OVERTURNED**; do not
cite it to decline sampling work. Q9 stays in the parity target and M0142
becomes scopeable.

**Frame it correctly: this is not error-injection.** The goal statement already
requires the same plan reached by the **same statistics**, so goopg's sampler is
a divergence from PG and removing it is PG-faithfulness work. **Port
`acquire_sample_rows` and let its output be whatever it is** — never tune a
constant toward a target number, never special-case a query, never add a fudge
factor. A task whose diff contains a constant chosen to make an estimate match
is rejected. If a faithfully ported sampler still disagrees with PG, that is a
finding to record, not a gap to paper over.

**Per-task discipline:** design note when the task is selected, indexed in the
same commit; cite the PG oracle by `file:function`; same-epoch A/B only (this
milestone moves estimates corpus-wide); values gates bind — large plan churn is
expected and acceptable, wrong rows are not. **Time every query whose plan
changed**: moving an estimate toward PG is the class that took TPC-DS Q72 from
4s to a 320s TIMEOUT (B6 — goopg has no Memoize on the NL probe path PG plans
that shape with), and B8's `indexProbeCostMultiplier = 2.0` and B10's `corr = 0`
fallback both shift index-probe pricing corpus-wide. See `AGENT.md` §"The
toward-oracle hazard"; a slower matching plan lands with a ledger row, an
unmeasured one does not.

- [x] **M0138-0001 — divergence census (recon, no production change)** — measure and
  record exactly where goopg's ANALYZE differs from PG's `acquire_sample_rows`
  (`postgres/src/backend/commands/analyze.c:1199`). Already upstream-faithful:
  `upstreamDefaultStatsTarget = 100`, the `targrows = target * 300` multiplier
  (`internal/executor/operators_analyze.go:581-589`), the Algorithm-R reservoir, the
  physical-order re-sort, and the Duj1 estimator. The measured divergence is the
  **block representation** — goopg scans every block, PG samples random blocks. Produce
  a per-column divergence table over both corpora; no code change in this task.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0001-analyze-divergence-census.md`.
  Live `pg_stats` capture over all four bench clusters (TPC-H SF1 + TPC-DS SF0.25,
  goopg vs PG) reconfirmed the block-representation gap and surfaced three
  previously-unnamed divergences — `RowCount`/`reltuples` (goopg exact vs PG
  extrapolated from `bs.m` sampled blocks), correlation tie-break (PG's
  `tupnoLink`-deterministic tie order vs goopg's unspecified `sort.Slice` order,
  live-correlated with a `[0.09,0.16]` correlation band on 36/118 TPC-DS columns
  vs PG's 7/119), and `pg_stats.avg_width` (PG falls back to fixed `typlen` for
  non-varlena types, goopg has no such fallback — `avg_width=0` on 32/61 TPC-H and
  70/121 TPC-DS columns). Three ledger rows filed (`m0138-0001-*`), each citing a
  resume point in M0138-0002 or M0138-0004. No production code touched.
- [x] **M0138-0002 — port PG's block sampler and two-stage row selection** —
  `BlockSampler_Init`/`BlockSampler_Next`
  (`postgres/src/backend/utils/misc/sampling.c:39,64`) plus
  `reservoir_init_selection_state` / `reservoir_get_next_S` / `sampler_random_fract`
  as `analyze.c:1228-1290` drives them, including PG's `pg_prng` sequence, so the
  sampled TID set matches PG's for a fixed seed on a shared relation.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0002-block-sampler-two-stage-row-selection.md`.
  New `internal/executor/analyze_block_sampler.go` ports `pgPRNGState`
  (xoroshiro128**), `blockSampler` (Algorithm S) and `reservoirState`
  (Algorithm Z) line-for-line from `sampling.c`; `analyzeRelationWith`'s block
  loop now visits only sampled blocks and its row selection uses PG's
  skip-count reservoir instead of Algorithm R. Resolved the M0138-0001
  `reltuples` scope question (ledger row flipped to `resolved`): confirmed
  VACUUM has its own separate reltuples path, so `RowCount` now
  unconditionally extrapolates via `floor((liverows/bs.m)*totalblocks+0.5)`,
  degrading to exact when every block is sampled -- every pre-existing
  small-table ANALYZE unit test stayed green under that degenerate path.
  `AvgWidth`'s denominator moved from `RowCount` to sampled-live-row-count to
  match. New tests in `analyze_block_sampler_test.go` (block sampler +
  reservoir primitives, plus a 60k-row integration case confirming actual
  block skipping). Dead-row tracking deferred, no consumer yet (ledger row
  `m0138-0002`). Gates: `go build ./...` clean;
  `go test ./internal/executor/...` full package green;
  `RALPH_PRECOMMIT_SCOPE=units` green except the pre-existing, already-filed,
  confirmed-unrelated `internal/parser` `TestLockingClauseParity` AST-drift
  failure (verified via `git stash` of this task's files reproducing the same
  failure without them). Corpus-wide TPC-H/TPC-DS re-measurement intentionally
  deferred to M0138-0005 (declared-epoch requirement).
- [x] **M0138-0003 — verify `stadistinct` parity end to end; do NOT re-implement what
  exists** — goopg stores upstream's one signed `stadistinct` as **two** fields,
  `NDistinct` (absolute) and `NDistinctFrac`
  (`internal/catalog/catalog.go:1880-1903`), but `ColumnStats.StaDistinct()`
  (`catalog.go:1942`) **already** reconstructs PG's signed convention including
  upstream's 10% absolute-to-fraction switch (`analyze.c:2650-2658`), and it is already
  consumed at the `pg_statistic` heap row, the `pg_stats` view and
  `joinselectivity.go:235`. So the convention is not the gap. The task is to confirm
  the switch fires where PG's does **on the sample M0138-0002 now produces**, and that
  every consumer reads the reconstructed value rather than a bare `NDistinct` (the
  take2 P2-09 class of bug, where a scaling column read as absolute-zero). R78's
  witness: goopg `l_orderkey` `-0.1956` vs PG's absolute `347537` — re-measure it and
  report. Land a change only where a real divergence is found.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0003-stadistinct-parity-verification.md`.
  Consumer audit confirmed all three call sites already go through
  `StaDistinct()` (no bare-`NDistinct` reads). Re-measured R78's witness on a
  HEAD build (carrying M0138-0002) against the live TPC-H bench pair
  (:65433/:65432): goopg's `l_orderkey` ndistinct moved from the pre-M0138-0002
  `-0.1956` frac (≈1.17M, 3.4x off) to `327804` absolute, landing inside PG's
  own unpinned 3-run noise band (`336410`-`366886`). Five more spot-checked
  columns matched PG within the same noise, including both engines reporting
  `-1` for a unique PK (confirms the 10% switch fires identically). No
  production change: the gap was M0138-0002's block-sampler fix, not a
  `stadistinct`-convention bug, so no diff earns landing per the milestone's
  anti-tuning rule.
- [x] **M0138-0004 — MCV, histogram and correlation from the shared sample** — apply
  PG's `compute_scalar_stats` / `compute_distinct_stats` selection rule to the sample
  M0138-0002 produces, so every slot is computed from the same rows PG would have seen.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0004-mcv-histogram-correlation-selection-rule.md`.
  Built on a prior loop's uncommitted WIP found already in flight (AvgWidth typlen
  fallback, correlation tie-break via `sort.SliceStable`, MCV bucket tie-break by
  ascending value on a count tie — resolves the two open M0138-0001 ledger rows for
  correlation tie-break and avg_width). Found and fixed three more divergences by
  reading `compute_scalar_stats` (`analyze.c:2402-2919`) line-by-line: (1) the
  "complete MCV list" shortcut wrongly fired whenever `len(buckets) <= statsTarget`,
  regardless of whether any distinct value was a singleton — PG's `track_cnt ==
  ndistinct` can only hold when EVERY distinct value repeated, since `track[]` never
  gains a singleton entry at all; fixed to `nmultiple == len(buckets) &&
  len(buckets) <= statsTarget && stats.StaDistinct() > 0`. (2) `analyzeMCVList`'s
  candidate list was capped by the total distinct count instead of `nmultiple`
  (PG's `track_cnt`), padding its significance walk with singleton noise; fixed to
  cap by `nmultiple`. (3) the histogram deduped adjacent equal boundary values under
  a comment claiming PG does too — checked against `analyze.c:2806-2836` and that's
  false, PG stores raw evenly-spaced values with no distinctness check, and the
  selectivity consumer already implements PG's own `binfrac = 0.5` equal-bounds
  fallback (`selfuncs.c:1234-1237`); fixed by removing the dedup. Three new
  regression tests pin all three fixes with hand-derived expected values (one
  probed against the pre-fix code via a throwaway copy to confirm the behavior
  actually changed before committing to it). Non-orderable-kind `compute_distinct_stats`
  (bytea/interval) deliberately left unported — no TPC-H/TPC-DS column exercises it
  (ledger row `m0138-0004`). Found two orphaned bench servers (`:65433`/`:65437`)
  left running by the interrupted prior loop's own measurement work; reaped via
  `goopg stop -D` (not `pkill`) so `scripts/tpch-spotcheck.sh` could get a
  quiescent snapshot. Gates: `go build ./...`, `go test
  ./internal/executor/... ./internal/optimizer/...` clean;
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green for both
  touched packages (pre-existing unrelated `internal/parser` 60-test AST-drift and
  `bak/` build failure — see the "Blast radius correction" note above);
  `scripts/tpch-spotcheck.sh` `RESULT=PASS` (Q12=2/Q13=34, canonical anchors).
  Corpus-wide plan/timing re-measurement is M0138-0005's job, not repeated here.
- [x] **M0138-0005 — corpus re-measure at a declared epoch** — commit a per-column
  statistics diff vs PG 18.3 over both corpora, then the plan and category movement it
  causes. Every remaining disagreement is explained or filed as a ledger row. Expect
  large plan churn; the values gates are the bar.
  - **DONE 2026-09-15.** Measurement-only (no production change). Design doc
    `docs/design/0100-0149/m0138-0005-corpus-remeasure-declared-epoch.md`.
  - Plan/category movement vs the milestone-filing baseline
    (`METHODOLOGY3/README.md` "post-R128"): **TPC-H byte-identical** — same
    match set (Q1/Q6/Q10/Q11/Q14/Q15a), all nine category counts unchanged.
    **TPC-DS verdict tuple unchanged** (`2/69/0/25/3/0`, reproduces
    `TODO.md:4845-4849`'s R120 baseline exactly) but `join-order` and
    `qual-placement` each +1 with no verdict flip — one unidentified query's
    plan structure moved sideways; ledger row filed, not bisected (99-query
    corpus, recon-scoped budget).
  - Per-column `pg_stats` diff over M0138-0001's column set, re-run post-fix:
    `n_distinct` sign/order-of-magnitude agreement now near-total (TPC-H
    60/61, TPC-DS 120/120 sign match) — the corpus-wide confirmation the
    earlier spot-checks predicted.
  - **Important correction**: the correlation-banding symptom M0138-0001
    found (`[0.09,0.16]` band on high-duplicate-density FK columns) **still
    reads 36/120 vs PG's 7/120 after M0138-0004's tie-break fix landed** —
    unchanged from the pre-fix 36/118 vs 7/119 reading. Re-verified the
    tie-break mechanism is correctly ported (source-level re-check: both
    engines sort the reservoir into physical TID order before computing
    correlation, and PG's own `tupno` is the post-sort array index, exactly
    what goopg's `pos` already is) — the fix itself is genuine and stays
    landed, but it does not explain the corpus symptom it was credited with
    resolving. **Reopened** the `.ralph/deferral_ledger.md` `m0138-0001`
    correlation row (flipped from `resolved` back to `-`) with a corrected
    resume point: a synthetic controlled-layout test to distinguish a
    remaining goopg bug from a genuine TPC-DS-loader physical-layout
    artifact between the two storage engines.
  - New finding: `avg_width=0` still reproduces for goopg's **fast-path**
    `numeric` (int64 mantissa inline, no heap arena) — M0138-0004's typlen
    fallback correctly only covers fixed-width types, but `numeric`'s
    measured-payload branch (`datumVariablePayloadWidth`'s `KindNumeric`
    case) only measures the slow (big-numeric) path. Reproduces on 28/61
    TPC-H columns (every numeric-typed key/price/cost column — HammerDB
    declares TPC-H keys `numeric`) and 17/120 TPC-DS columns. Ledger row
    filed; needs PG's actual numeric-varlena size formula, not a literal
    constant (anti-tuning rule).
  - Gates: `go build ./...` unaffected (no source changed this loop). No
    values-gate re-run needed — no production code touched.
- [x] **M0138-0006 — re-measure Q9's estimate against R130's table and reconcile the
  ANALYZE seed (DONE 2026-09-15)** — Q9's actual output is 175 rows; goopg estimated 97
  and PG estimates 60,125. **Moving toward PG's 60,125 is the expected result** and is
  the empirical test of the Question 2 decision; if it does not move, that is the
  finding and M0142's premise must be re-examined before slices are scoped. In the same
  task, reconcile `GOOPG_ANALYZE_SEED`'s determinism with PG's own sequence — retire it,
  or document why both must coexist.
  - Re-measured at HEAD (`estimate-audit -plan-only -queries 9`, pinned
    `GOOPG_ANALYZE_SEED=20260905`, live `:65433`/`:65432` lanes): goopg's estimate moved
    **97 → 146** — closer to the actual 175, **not toward PG's 60,125**. The
    pre-registered prediction did NOT hold; M0142's premise needs re-examination, now
    with a narrowed starting hypothesis instead of an open-ended one (see below).
  - Leaf-level check: the `partsupp ⋈ part` join (filtered `p_name LIKE '%green%'`)
    estimates now agree within 1.2x between engines (12,121/48,484 goopg vs
    10,101/40,404 PG) — the underlying per-column statistics converged (consistent with
    M0138-0005's corpus-wide `n_distinct` finding). The 344x top-level gap is therefore
    NOT a residual statistics-precision gap of the kind M0138 targets.
  - Hypothesis for M0142-0001/-0002 (not adjudicated here — recon-scoped, and forcing
    shapes is against the milestone's goal): the gap is **join-shape-driven**. goopg
    drives Q9 with FK-indexed Nested Loops (each probe correctly `rows=1`); PG runs an
    all-Hash-Join chain where `eqjoinsel`/`calc_joinrel_size_estimate`
    (`postgres/src/backend/utils/adt/selfuncs.c:2280`,
    `postgres/src/backend/optimizer/path/costsize.c:5501`) compound independent
    per-join selectivities down a correlated FK chain with no extended statistics.
    Reproducing PG's estimate likely requires goopg to choose PG's Hash-Join shape
    first — a join-order/method question, not an ANALYZE-precision one.
  - `GOOPG_ANALYZE_SEED` reconciliation: confirmed against PG's actual source
    (`analyze.c:1227`'s `pg_prng_uint32(&pg_global_prng_state)`, a process-global PRNG
    with no reproducibility knob) — PG has nothing to retire the variable in favour of.
    goopg's unset/zero path already reproduces PG's "fresh per-backend draw" property
    exactly; the pinned path is a harness-only determinism knob with no PG counterpart.
    **Verdict: keep, no code change** — the existing code comment was already correct.
  - Design doc:
    `docs/design/0100-0149/m0138-0006-q9-estimate-remeasure-and-seed-reconcile.md`.
    Ledger row filed narrowing M0142-0001/-0002's starting hypothesis (see
    `.ralph/deferral_ledger.md`, task-id `m0138-0006`).
  - Gates: `go build ./...` unaffected (no source changed). No values-gate re-run
    needed — no production code touched this loop.
  - **M0138 is now fully landed and measured** (all six tasks [x]) — M0142's
    prerequisite gate is satisfied; M0142-0001 becomes selectable.
- [x] **M0138-0007 — give `numeric` columns a real `avg_width`** — M0138-0005
  measured that the `numeric` fast path leaves `avg_width=0`, affecting **28 of
  61 TPC-H columns and 17 of 120 TPC-DS columns**, and filed a ledger row with no
  owner. HammerDB's TPC-H declares every primary and foreign key `NUMERIC`, so
  this hits the join columns the whole programme turns on. Resume point in the
  row: `operators_analyze.go:1159-1174` plus PG's `numeric_size` arithmetic. A
  width of zero is not a PG-faithful statistic — this is squarely M0138's remit.
  - **DONE 2026-09-15.** The ledger row's own guessed formula (`NUMERIC_HDRSZ`
    plus digits) turned out to be the wrong PG function — that is
    `numeric_maximum_size`'s typmod-derived worst case (already used
    elsewhere, `relsize.go`'s `numericHeaderSize`, for the planner's row-width
    *upper bound*), not what `compute_scalar_stats` measures. Verified against
    live PG 18.3 instead: `VARSIZE_ANY` of the raw on-heap Datum —
    `pg_column_size(l_quantity)=5` for `18`, `pg_stats.avg_width=8` for
    `l_extendedprice`, both reproduced exactly by (1-or-4-byte short/long
    varlena header) + (PG NumericData's own 2-or-4-byte internal header) +
    (2 bytes per stripped base-10000 digit). New `numericFastPathOnDiskWidth`
    reuses `internal/nodes.NumericBodyFromText` (the same `numeric_in` port
    `codec.go` already uses for the heap's on-disk numeric form) instead of
    re-deriving digit-grouping. Sibling-path check: `spill.go`'s
    `estimatedRowBytes` correctly stays at `+0` for this arm (in-memory
    footprint, a different, already-documented-as-divergent quantity from the
    on-disk stats ruler) — its cross-check test's coincidental equality for
    this one case was removed with an explanatory comment, not silently left
    to bit-rot. Design doc:
    `docs/design/0100-0149/m0138-0007-numeric-avg-width.md`. Ledger row
    `m0138-0005` (numeric avg_width) flipped `resolved`. Gates: `go build
    ./...`; `go test ./internal/executor/... ./internal/optimizer/...` clean
    (new `TestNumericFastPathOnDiskWidth`, updated
    `TestDatumVariablePayloadWidth` + `TestEstimatedRowBytesCountsEnumAndBigNumeric`);
    `RALPH_PRECOMMIT_SCOPE=units` green except the pre-existing, already-filed
    `internal/parser` `GroupedJoinUnaliased` AST-drift (60 test functions,
    confirmed unrelated); `scripts/tpch-spotcheck.sh` `RESULT=PASS`
    (Q12=2/Q13=34, canonical anchors — no plan/category shift observed at
    this scale).
- [x] **M0138-0008 — bisect the category shift M0138 caused** — **DONE
  2026-09-15.** Built goopg at the pre-M0138-0002 commit (`0e97c94b3`) and at
  M0138-0004 (`44085c324`), loaded a private SF0.25 dataset with each (shared
  `:65437` cluster untouched), diffed against the live PG reference. Four
  unpinned trials found `join-order`/`qual-placement`/three other categories
  move run-to-run at a FIXED commit from ANALYZE reservoir-seed variance alone
  (`Q26`, `Q45`, `Q48`, `Q51`, `Q97` each flip a category tag) — the same
  noise mechanism M0138-0009 found for `correlation`, now shown to also flip
  plan-parity verdict categories, this milestone group's own success metric.
  Pinning the existing (but here-unused) `GOOPG_ANALYZE_SEED` knob made
  captures reproducible; with it pinned, the shift reproduces in M0138-0005's
  original direction and narrows to exactly `Q21` (join-order, a
  cardinality-estimate-driven join-spine change) and `Q48` (qual-placement, a
  `store` access-path change). Design doc:
  `docs/design/0100-0149/m0138-0008-category-shift-bisect.md`. Follow-up
  **M0137-0020** (below, filed in this loop) owns pinning the seed in the
  capture harness itself. Ledger row: `m0138-0008-category-shift-bisect`.
- [x] **M0138-0009 — re-open the correlation banding finding** — **RESOLVED
  2026-09-15, NOT A DEFECT.** Ran the synthetic-table comparison the row
  prescribed in two parts. (1) `internal/testport/m0138_correlation_synthetic_test.go`
  loaded a hand-constructed periodic column identically on goopg and real PG
  18.3 (server-side `COPY FROM file`, N below `targrows` so ANALYZE fully
  scans, no reservoir randomness): both engines produced BYTE-IDENTICAL
  correlation (`0.095866`), refuting a computation bug and a simple-load
  physical-order divergence. (2) `internal/executor/operators_analyze_test.go:TestAnalyzeReservoirSeedCausesCorrelationVarianceOnPeriodicFK`
  then varied only goopg's reservoir-sampler RNG seed at a realistic
  subsampling ratio and reproduced a correlation spread (`[0.05,0.18]`) wider
  than the census's own `[0.09,0.16]` banding from seed variance alone.
  Conclusion: ordinary reservoir-sampling variance on a periodic/
  low-duplicate-density column, equally present in PG's own independently
  -seeded sampler — nothing to port, nothing to fix. Ledger row `m0138-0001`
  (correlation) flipped `resolved`; no follow-up task filed. Design doc:
  `docs/design/0100-0149/m0138-0009-correlation-banding-synthetic-isolation.md`.

## M0139 — Executor-side narrowing / projection pushdown (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0139-executor-side-narrowing-projection-pushdown.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Source:** the owner's answer of 2026-09-14 to `METHODOLOGY3/04-forward-plan.md` §1.1 Question 1 — **(a) build it**
**Prerequisite:** M0137 (M0137-0010's qual-placement census gates every slice here).

**Scope is projection pushdown only.** The packed retention format
(`PackedTuple`/`PackedSlot`) stays declined and needs an owner decision informed
by S3's measurement; a `Datum` re-layout below 48 B is declined and nobody
proposes it. `minimize_datum/README.md` states `Datum` **stays exactly 48 bytes**
— so "the `DatumBytes` half" is the wrong label for the retention format and must
not be used (`05-work-estimate.md` §1 calls confusing the two "the single most
likely way to misprice this work").

**The structural blocker is located:** inside a join tree there is no `*Project`
above the scan at all — the join reads the scan directly, so there is nowhere to
hang a projection. The machinery for *applying* a narrowing already exists and
ships default-ON, but in **three separate files**: `GOOPG_NARROW_BUILD` in
`internal/optimizer/narrowoutput.go` (hash build side and merge input),
`GOOPG_NARROW_UPPER` in `upper_narrow_apply.go:90`, `GOOPG_NARROW_UPPER_SORT` in
`upper_narrow_chain.go:124`. **`attr_needed` is NOT the blocker** — two earlier
diagnoses said it was and both were wrong.

**Argue this campaign on Q4 and the executor gap, not on Q9.**
`internal/optimizer/entrywidth.go:38-48` carries a measured comment headed "WHAT
THIS DOES NOT BUY": Q9's `nbatch` is non-monotone in entry width (4 at 112..194,
2 only at 96..111, back to 4 below 96), commit `2e15b8ca3` records that a packed
retention format would make Q9's batching **worse**, and R129 measured the lever
that comment names as parity-inert.

- [x] **M0139-S1 — a hook point inside the join tree** (gated on M0137-0010) — pre-registered prediction:
  **no parity movement; the pass fires N > 0 times**. A slice that predicts a match
  flip is mis-scoped. Do not re-derive that `attr_needed` is the blocker.
  - DONE 2026-09-15: recon narrowed the gap — `joinInputsFor` already narrows
    a hash join's inner/build side and both merge-join sides; only a hash
    join's outer/probe side and both nested-loop sides (plain + NLI) were
    never reached. `narrowPlanOutput` itself already declines to wrap a
    no-op cut, so the hook could not be "an identity Project" (it would be
    silently absorbed) — it had to be a new call site instead. Landed
    `internal/optimizer/joinleghook.go`'s `narrowJoinLeg`, gated by new
    default-ON flag `GOOPG_NARROW_LEG_HOOK`, called from `joinInputsFor` on
    both legs of every join kind. **Unconditionally a decline for S1**: it
    counts every currently-unhooked eligible leg but returns the pair
    byte-identical to its input in every case — "no parity movement" is
    guaranteed by construction, not merely predicted, and proven directly
    by `TestNarrowJoinLegDeclinesButCounts` plus a live two-table-join test
    (`TestNarrowJoinLegFiresOnLiveJoinSearch`, fires 1 time, plan shape
    identical hook-on vs off via `unaDump`). M0139-S2 reuses
    `narrowBuildInput`'s existing keep-set derivation at this hook rather
    than duplicating it. Gates: `go build`/`go test` clean for
    `internal/optimizer`/`internal/executor`; `RALPH_PRECOMMIT_SCOPE=units`
    green except the pre-existing, already-filed `internal/parser`
    AST-drift + `bak/` build failure; `scripts/tpcds-sf025-regression.sh
    sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0.
    `scripts/tpch-spotcheck.sh` **could not run** — shared `:65433` was up
    the whole task (peer-owned, not to be stopped) and the private-clone
    snapshot step requires it fully down; retried 3x over ~15 min, still
    busy. Not a values-safety gap given the mechanical no-op proof; re-run
    once the shared server frees up. Design doc:
    `docs/design/0100-0149/m0139-s1-join-leg-hook.md`. No ledger row (pure
    goopg-internal plumbing).
- [x] **M0139-S2 — narrow scan output at the new hook** (gated on M0137-0010) — reuse the existing
  narrowing rather than duplicating it. It lives in three files, not one:
  `narrowoutput.go` (`GOOPG_NARROW_BUILD`, hash build + merge input),
  `upper_narrow_apply.go:90` (`GOOPG_NARROW_UPPER`) and `upper_narrow_chain.go:124`
  (`GOOPG_NARROW_UPPER_SORT`).
  - DONE 2026-09-15: `narrowJoinLeg` (joinleghook.go) now reuses
    `narrowBuildInput`'s exact chain (`joinKeepSet`→`buildKeepSet`→
    `neededKeepSet`→`narrowPlanOutput`) instead of always declining;
    signature grew `*Path` + `nliInner bool`. Safety for NL legs: the
    tightest tier (`joinKeepSet`) correctly reports unknown under an NL
    (deriveJoinKeepsAt never stamps JoinKeep there) and falls through to
    the two join-kind-agnostic, over-inclusive fallback tiers. ONE case is
    structurally forbidden regardless of keep-set: an NLI's INNER slot is
    typed concretely (`*IndexScan`/`*BitmapHeapScan`), so wrapping it in a
    `*Project` is a plan-time panic, not a narrower plan — caught LIVE by
    two pre-existing tests
    (`TestQ2DecorrelatedGroupKeyResolvesInAggregateInput`,
    `TestDerivedTableUnderIndexNLReturnsRows`) before the `nliInner`
    exclusion (covers both `"PathNestLoop(NLI)"` and
    `"PathNestLoop(NLI-bitmap)"`) was added. Landing real narrowing
    legitimately changed plan shape wherever a previously-unwrapped leg
    had something to drop, so 5 pre-existing regression tests needed
    their hard-coded build-count oracle re-derived (via throwaway probe
    tests, deleted before commit), not loosened:
    `TestSlice3LiveQ9ShapeDerivation` (5→7 builds),
    `TestSlice3LateralDeclinesDerivation` + executor twin
    `TestOwnedBuildPoisonCorrAboveDecline` (1→2),
    `TestSlice3DerivedTableAliasMapping` (1→2),
    `TestSlice3SelfJoinInDerivedTable` (one 6-col combined build splits
    into two 3-col builds), `TestPlanJoinPicksHashAlgo` (a NULL-pad
    restoration Project can now sit below the top SELECT Project; fixed
    by reusing `findFirstJoin` instead of a single type assertion).
    Gates: `go build ./...`; `go test ./internal/optimizer/...
    ./internal/executor/... ./internal/testutil/tpch/...
    ./internal/postmaster/...` clean; `RALPH_PRECOMMIT_SCOPE=units`
    green except the pre-existing, already-filed `internal/parser`
    AST-drift; **qual-placement census on the full TPC-DS SF0.25 corpus,
    99/99 queries, mismatch=0** (private clone `tmp/goopg-m0139s2-bin`);
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3, non-blocking status-delta
    verdict-changes=none total-delta=-1.7% (no timing regression).
    `scripts/tpch-spotcheck.sh` could not run — shared `:65433` up the
    whole task (peer-owned); not a values-safety gap given the
    corpus-agnostic mechanism plus the clean TPC-DS gates; re-run
    opportunistically. Design doc:
    `docs/design/0100-0149/m0139-s2-narrow-join-leg-output.md`. No
    ledger row (goopg-internal executor plumbing).
- [x] **M0139-S3 — measure the residue against K67's floor** (gated on M0137-0010) — even narrowed to one
  column goopg is 72 B/row -> 103 MB and still spills at `work_mem=64MB` where PG is
  22 B/row -> 31 MB. Report the post-pushdown figure as a **number**, not an argument.
  - DONE 2026-09-15 (recon, no production diff): K67's "72 B/row -> 103 MB,
    narrowed to one column" was analytical (`EntryBytes(1,0)`), never
    measured, and predates S1/S2. A throwaway probe
    (`internal/testutil/tpch/zz_probe_m0139s3_test.go`, deleted before
    commit) built a private disposable cluster from HEAD (never touching
    the shared, peer-owned `:65433` server — whose binary is stale and
    shows zero narrowing on Q12) with `internal/testutil/cluster` +
    `scaleLoader` (20,000 real-DDL orders/lineitem rows), then ran
    `EXPLAIN (VERBOSE)`, `EXPLAIN (ANALYZE, VERBOSE)`, and a
    `pg_stats.avg_width` read for Q12. Confirmed the hook fires (Hash
    Join `Output:` narrows 16+9 columns down to 7), but the real
    orders-side retained set is **`{o_orderkey, o_orderpriority}` — 2
    columns, not K67's assumed 1** (the join key must stay for
    probe-time verification even though nothing above the join
    references it), with real `avg_width(o_orderpriority)=8.3701` (not
    K67's assumed 0). Formula `EntryBytes(2, 8.3701) ~= 128.37 B/row`,
    cross-checked against the executor's own measured `Buckets: 32768
    Batches: 1 Memory Usage: 4044kB` (subtracting the bucket-table term
    reproduces 128.4 B/row). Extrapolated to SF=1's 1.5M orders the same
    way K67 did (entries only): **~193 MB vs K67's 103 MB and PG's
    unchanged 22 B/row -> 31 MB** — S1/S2's real narrowing made the
    measured residue *larger* than the campaign's own anchor, not
    smaller. Still spills at PG 18's default `work_mem=64MB x
    hash_mem_multiplier=2.0` = 128 MB budget vs 192.6 MB of entries.
    Feeds M0139-0006's owner decision; does not itself decide
    `minimize_datum` (still NOT APPROVED TO START). Design doc:
    `docs/design/0100-0149/m0139-s3-k67-residue-measurement.md`. No
    ledger row (no PG-incompatibility surfaced).
- [x] **M0139-0004 — re-measure the "duplicate hash build map" premise, then act on
  what you find** — `minimize_datum/05-work-estimate.md` §1.5 (quoting `02` §3) prices a
  "delete the duplicate build map" win at "one commit", claiming `lazyHash` **and**
  `lazyIntHash` are both maintained for ~2x peak build memory.
  - DONE 2026-09-15 (recon, no production diff): confirmed the premise **refuted**
    at HEAD. `buildLazyHashTable` decides the lane once, before the first row,
    from the plan's static key types (`operators_join_agg.go:651`);
    `presizeLazyHash` allocates exactly one of the two maps (pinned by
    pre-existing `join_presize_test.go` tests); every insert commits to one
    lane per call; the only place both maps are ever non-nil is
    `demoteIntHash`'s own transient copy loop — a rare fallback for a static
    int64-key promise turning out false mid-build, not a standing property of
    the routine path (pinned by `TestPresizedIntTableStillDemotes` and
    `dense_build_cut2_test.go`). The parallel/cooperative build path reuses
    the same presize/insert functions rather than re-deriving the lane logic.
    Corrected `minimize_datum/05-work-estimate.md` §1.5's table row and the
    originating claim in `02-goopg-current-representation.md` §3 (both now
    marked refuted, citing the design doc). Does not decide or advance
    `minimize_datum` (still NOT APPROVED TO START) — removes one stale input
    from a document feeding a future owner decision. Design doc:
    `docs/design/0100-0149/m0139-0004-duplicate-hash-map-refutation.md`. No
    `.ralph/deferral_ledger.md` row (not a PG-incompatibility, out of that
    ledger's scope; refutation recorded in the design docs directly, per the
    M0139-S3 precedent).
- [x] **M0139-0005 — re-measure Q4's grouping election** — R81 located the divergence
  one rel above the grouping contest, in `electOrderedGrouping`
  (`upperorderedgrouping.go:148`), on a startup ratio against `stdFuzzFactor = 1.01`:
  goopg 1.0086 (inside fuzz, tie-break picks hashed) vs PG 1.0118 (outside, sorted
  wins). Report whether the narrowed widths move that ratio across the band.
  - DONE 2026-09-15 (recon, no production diff): a throwaway probe
    (private HEAD cluster, 20,000 orders, deleted before commit) plus
    temporary debug instrumentation in `electOrderedGrouping` and
    `narrowJoinLeg` (both fully reverted — `git diff --stat` empty at HEAD)
    found **zero narrowing anywhere in Q4's subtree**: `narrowJoinLeg` is
    never called at all for this query. Root cause: Q4's `EXISTS` is
    decorrelated by `unnestExistsExpr` (`unnest.go:4078`), which builds the
    physical `Join{SEMI, Hash}` node directly on the raw parse tree, before
    any search joinrel exists — the same fork METHODOLOGY3 F11/K63
    (R73-R77) already named for the costing symptom ("no SEMI path is ever
    filed... before any search joinrel exists, so no sjinfo -> no joinrel
    -> no path -> no price") — so it never reaches
    `createHashJoinPlan`/`joinInputsFor`, the one call site
    `narrowJoinLeg` is wired into. **Verdict: the ratio has not moved and
    cannot move under the current mechanism** — Q4's grouping input width
    is byte-identical to before M0139-S1/S2, independent of scale, so a
    bigger-scale re-measurement would not be informative until the bypass
    itself is fixed (a materially larger task, out of this recon's scope).
    Secondary finding: at 20,000-order scale the two ORDERED-rel
    candidates aren't even fuzzy-tied (one dominates outright in
    `addPath`) — a separate scale-sensitivity note, not pursued further.
    Feeds M0139-0006's owner packet alongside S3's residue finding — both
    point at "the join-leg hook doesn't reach every join the search can
    produce," from opposite ends. Design doc:
    `docs/design/0100-0149/m0139-0005-q4-grouping-ratio-remeasurement.md`.
    No ledger row: the narrowing-bypass finding is a corollary of the
    already-tracked F11/K63/`m0137-0011` lineage, not a newly discovered
    PG-incompatibility on its own.
- [x] **M0139-0006 — put the packed-retention decision to the owner** (DONE
  2026-09-15) — carried S3's measured residue, `entrywidth.go`'s
  non-monotonicity finding, and the two blockers the `minimize_datum` review
  itself raised into one packet. **Not implemented** — `minimize_datum`
  remains NOT APPROVED TO START; this task only poses the owner question.
  - Packet contents: (1) S3's Q12 residue — 128.4 B/row measured (≈193 MB at
    SF=1) vs K67's analytical 72 B/row (103 MB) floor, ~5.8× PG's 22 B/row
    (31 MB), worse than budgeted, not better. (2) `entrywidth.go`'s
    non-monotonicity (`minimize_datum/TODO_ALL.md` "D-05 prereq #1"): entry
    width 194→120 B/row left `NBatch` unchanged (4→4); 2-batch threshold is
    ≤111.8 B/row; a further cut to 63 B/row lands back on 4 — batch count is
    governed by `MapSlotBytes`, not monotonically by entry size, so a packing
    win is not guaranteed to reduce spilling without re-deriving the batching
    geometry. (3) The review's own B2/B3 (packing alone was estimated to
    close only ~5× of a ~48× gap; narrowing — M0139's own mechanism — owns
    the rest) and B5 (take3 13 §8.2 sequencing: EX1-before-geometry, now
    concretely satisfied by M0139-S1/S2/S3 — the one thing that changed since
    the review, but it clears only the sequencing gate, not effectiveness).
  - Owner question posed explicitly, not answered: authorize `minimize_datum`
    now that sequencing is clear, given packing was independently estimated
    to close only a fraction of the residue, or hold it out-of-scope for a
    milestone group whose success metric is plan-structure parity, not
    memory footprint.
  - Design doc:
    `docs/design/0100-0149/m0139-0006-packed-retention-owner-decision.md`.
  - No ledger row: no new PG-incompatibility surfaced, this is a synthesis
    of existing evidence, not a new discovery.
  - **All six M0139 tasks (S1, S2, S3, -0004, -0005, -0006) are now DONE.**
    M0139 has no further open items; the next M0139-family work (if any)
    would come from a future owner decision on `minimize_datum`, which is
    out of scope until that decision is made.
- [x] **M0139-0007 — absorb the irreducible width difference into the cost model
  (owner decision B2)** — **TOP PRIORITY with M0141-S2a-fix.** M0139-S1/S2 landed
  real narrowing and moved **zero** plans, because `applyUpperNarrowing`
  (`planner.go:189`) runs after cost and strategy are decided in
  `planStmtWithSettings` (defined `planner.go:210`, called `:141`); measured proof is that TPC-H costs
  are byte-identical pre/post M0139 while only `width=` shrank. Packed retention
  is **NO-GO** (B1), so the route is B2's absorption principle: give the cost
  model the **PG-equivalent** width — what PG's own tuple representation would
  be for the same logical row — while the executor keeps allocating goopg's real
  `48*ncols + 24 + avgVar`. **This is not tuning**: it feeds PG's formula PG's
  input, rather than bending a goopg number until the output matches. Start with
  a scoping recon (no production diff) that names every site where a goopg-native
  quantity currently reaches a PG-derived cost formula — `hashsize.EntryBytes`
  into the hash-join spill decision is the witness, `MapSlotBytes` and the sort
  footprint are the other candidates — then slice. Each absorption site needs a
  design-doc justification and a test pinning the two currencies apart. Expect a
  slower executor and unchanged values; a matching plan that runs slower is
  **not** a regression.
  - **Recon DONE 2026-09-15, no production diff** (design doc
    `docs/design/0100-0149/m0139-0007-absorption-scoping-recon.md`, full
    inventory table). Findings:
    - The task's own witness site is **half-absorbed already**:
      `GOOPG_NARROW_COST_INPUTS` (R121/R128) landed the build-entry-footprint
      half default-ON since R128 (moved TPC-H `join-method` 10→9); only the
      **batch/spill decision** half remains — and that half's absorption is
      **already built**, just never measured or promoted:
      `hashjoin_pggeometry.go`/`hashjoin_pgtuplesizing.go` port PG's packed
      `HashJoinTuple` sizing behind `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108,
      default-off).
    - A second, parallel arm exists for **Sort's** spill decision (which also
      feeds WindowAgg's internal sort via `costWindow`):
      `sort_pgrelationbytes.go` ports PG's `relation_byte_size` behind
      `GOOPG_PG_SORT_RELATION_BYTES_COST` (R113, default-off). Neither R108
      nor R113 has been measured against the post-M0137–M0142 corpus.
    - HashAggregate's width currency is a **settled, already-decided
      divergence** (R120 built a fix, R124 measured it net-neutral paired with
      narrowing, M0137-0009 deleted the flag) — do **not** reopen without new
      evidence.
    - A genuinely **new, unabsorbed** site: Memoize's entry-byte estimate
      (`joinpathsmemoize.go:133-139`) uses goopg's map currency where PG's
      `cost_memoize_rescan` (`postgres/src/backend/optimizer/path/
      costsize.c:2541`) uses `relation_byte_size` (already ported in-tree as
      `pgRelationByteSize`, directly reusable) plus
      `ExecEstimateCacheEntryOverheadBytes` (not yet ported, small named PG
      function).
    - Bitmap heap scan's entry sizing (`costbitmap.go:tbmEntryBytes`) is
      planner/executor self-consistent within goopg, not a cross-currency
      case — not sliced further.
  - Two bounded next slices filed below: **M0139-0007a** (measure/adopt the
    two already-built R108/R113 arms) and **M0139-0007b** (port Memoize's
    currency). Ledger row appended (task-id `m0139-0007`).
- [x] **M0139-0007a — measure and adopt/hold the two already-built R108/R113
  absorption arms.** `GOOPG_PG_HASH_TUPLE_SPILL_COST` (hash-join spill/batch
  decision) and `GOOPG_PG_SORT_RELATION_BYTES_COST` (Sort spill decision, also
  feeds WindowAgg) both already port a named PG formula (file:line cited in
  their source comments) but have never been measured against the
  post-M0137–M0142 corpus. Measure each independently first (row 3 of
  M0139-0007's inventory feeds row 1's competing Sort-based plan shapes, so a
  combined flip could move a plan for a reason neither arm alone explains),
  decide adopt/hold per the plan-parity metric, one design doc per arm,
  following the `GOOPG_GATHER_PATHS` promotion precedent
  (`docs/design/0100-0149/m0140-0003-gather-paths-flip-lands-default-on.md`).
  - **DONE 2026-09-15, both arms measured, both HOLD (design docs
    `docs/design/0100-0149/m0139-0007a-hash-tuple-spill-cost-measurement.md`,
    `docs/design/0100-0149/m0139-0007a-sort-relation-bytes-cost-measurement.md`).**
    Single HEAD binary (env vars read once at package-var init, so on/off
    share one build); measured independently, never combined. **TPC-H: both
    arms byte-identical to the current-HEAD-default baseline for all 22
    queries** (`match=8` unchanged either way, `shape-delta.sh`
    `shape-changed=0` on both, same-PG-reference control against fix1's
    committed capture to strip PG-side ANALYZE sampling noise). **TPC-DS:
    both arms leave every one of the 9 category counts unchanged**
    (`match=2` unchanged); R108 moves Q64's cost digits, R113 moves Q4's and
    Q64's, but all three stay `MISSING-NODE` (PG plans them with Incremental
    Sort or an order-of-magnitude-different estimate) before and after, so no
    category tag is affected — verified via `pg-plan-parity-diff.py
    --verbose`, not assumed from the totals agreeing. **Decision: HOLD for
    both, stay default-off.** No plan-parity upside anywhere to weigh a
    promotion against (unlike `GOOPG_GATHER_PATHS`, which had a genuine bug
    fix to weigh against its category rise); B2 also flags both arms'
    direction as the risky one — PG's tuple/row currency is smaller than
    goopg's real executor allocation for both hash-join spill and Sort spill,
    so promoting either needs the TPC-H SF=1 execution acceptance-arm gate
    first, not run here because nothing in this measurement justifies
    running it. Neither arm deleted (both are correctly-derived PG-formula
    ports, kept as controls). Ledger row appended (task-id `m0139-0007a`).
    M0139-0007's recon inventory rows 1 and 3 are now fully resolved; only
    M0139-0007b (Memoize) remains open below as the one remaining sub-task of
    the banner's top-priority "M0141-S2a-fix and M0139-0007 — costing-order
    unblock" line — M0141-S2a's fix1/fix2 pair is landed-and-decided, and of
    M0139-0007's three filed pieces (recon, 0007a, 0007b) only 0007b is still
    open. The next loop should take M0139-0007b next (still inside the
    top-priority group) unless its own recon surfaces a reason to defer it,
    in which case the banner's item 2 (M0137's re-opened 0014–0017) is next.
- [x] **M0139-0007b — port PG's Memoize entry-byte currency** (DONE
  2026-09-15). Gave `joinpathsmemoize.go`'s `costMemoizeRescan` a new
  default-off arm, `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` (R108/R113-shaped):
  `pgRelationByteSize(tuples, pathWidth(innerPath))` (reused verbatim from
  R113) plus a newly-ported `pgMemoizeEntryOverheadBytes`
  (`ExecEstimateCacheEntryOverheadBytes`, `nodeMemoize.c:1171-1176`, derived
  from the actual `MemoizeEntry`/`MemoizeKey`/`MemoizeTuple` C struct sizes:
  24+24+16·tuples). Pinned by 3 new tests
  (`internal/optimizer/memoize_pgentrybytes_test.go`).
  - **Measured: byte-identical on both corpora.** TPC-H's one Memoize node
    (`rows=1 width=490`) and TPC-DS SF0.25's 13 Memoize nodes (all `rows=1`)
    price identically in both arms — the byte currency only reaches the final
    cost via `evictRatio`, and every candidate's `estCacheEntries` swamps
    `ndistinct` under a 64 MB `work_mem` regardless of currency. Same
    "arm never reaches its own memory-constrained regime" shape 0007a found
    for R108/R113. **Decision: HOLD, stays default-off.**
  - Deferred: the per-key `get_expr_width` term is not absorbed (no goopg
    per-expression width statistic exists yet) — ledger row `m0139-0007b`,
    follow-up filed as **M0139-0007c** below.
  - Design doc: `docs/design/0100-0149/m0139-0007b-memoize-entry-bytes-absorption.md`.
  - **With this, all three of M0139-0007's filed pieces (recon, 0007a, 0007b)
    are resolved** — the banner's "M0141-S2a-fix and M0139-0007" line's
    `M0139-0007` half is complete.
- [ ] **M0139-0007c — port `get_expr_width` for Memoize's cache-key width
  term.** `costMemoizeRescan`'s per-key contribution
  (`hashsize.EntryBytes(nkeys, 0)`, both currencies) still stands in for PG's
  `get_expr_width` sum over `mpath->param_exprs`
  (`postgres/src/backend/optimizer/path/costsize.c:2566-2567`) because goopg
  has no per-expression average-width statistic wired to this site. Needs a
  per-column/per-expression width lookup (candidate: extend
  `pathAvgVarBytes`/`typeWidth`-style catalog lookups to a bare `*ColumnRef`
  cache key, since `getMemoizePath`'s gates already guarantee every key is
  one) before it can be absorbed rather than approximated. Not currently
  gating the banner's top-priority line — pick up per the milestone's normal
  ordering.

## M0140 — TPC-DS parallelism (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0140-tpcds-parallelism.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Source:** `METHODOLOGY3/04-forward-plan.md` Phase 1 Campaign A
**Prerequisite:** M0137. **Independent of M0139** per the owner's "(c) split the goal = Go".

`parallelism` blocks **87 of 99** TPC-DS queries: PG plans 66 of 99 in parallel
where goopg plans serial, **zero the other way**, and `Parallel Hash` appears 314
times in PG's plans and 0 in goopg's. Three corrections already narrowed the
lever — **K80** (`addPartialHashJoinPath` already sets `ParallelAware: true`; the
arm is dead solely because `GOOPG_GATHER_PATHS` is default-OFF), **K82** (Q14 is
byte-identical under the flip, so the tracks are independent), **K92**
(relabelling goopg's leader-prebuild as `Parallel Hash` would be a
misdescription, i.e. the forcing the goal forbids).

Carry, do not rediscover: **K20** — `GOOPG_GATHER_PATHS` gates *partial paths
only, not parallelism*; the post-pass emits a Gather regardless, so there is no
setting that yields a serial plan.

- [x] **M0140-0001 — re-measure the failing test set under the `GOOPG_GATHER_PATHS`
  flip at HEAD** (recon, no production change; DONE 2026-09-15) — do **not** inherit
  the R43-era "5 failing, 4 needing adjudication" list; it is ~87 rounds stale and
  at least `TestSlice3LiveQ9ShapeDerivation` was re-baselined by R51 nine rounds
  later. The ledger's own adjacent note binds: re-measure before relying on any
  prior round's figures.
  - Ran all four named tests (`TestSplitEqualityForHashMultiKey/searched_enumerator`,
    `TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`,
    `TestOwnedBuildPoisonPrebuiltBoundary`) both at the current default
    (`GOOPG_GATHER_PATHS` unset) and under `GOOPG_GATHER_PATHS=all`. None of the
    tests set the env var internally, so the flip is applied purely via the
    process environment — exactly what a default-on flip would change.
  - Result: **all four PASS at the current default and all four FAIL under the
    flip** — the identical four names R43 measured, none independently fixed by
    the intervening ~87 rounds. The prerequisite list is exactly as large today
    as it was at R43; it is stale in dating, not in content.
  - Failure-mode read: the three optimizer/executor narrow-build tests
    (`TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`,
    `TestOwnedBuildPoisonPrebuiltBoundary`) look like one shared mechanism —
    partial-path admission changes which join-tree shape wins the search before
    the narrowing pass runs, so their exact narrow-build column-set pins no
    longer match. The multi-key hash-join test fails for a different reason
    (falls back to Nested Loop instead of hash-joining under the flip).
  - No adjudication against PG performed here — that is M0140-0002's job, per
    the recon-task boundary. No ledger row: the divergence is between two
    goopg-internal planner features, not a newly discovered PG-incompatibility.
  - Design doc: `docs/design/0100-0149/m0140-0001-gather-paths-flip-failing-set-remeasure.md`.
- [x] **M0140-0002 — adjudicate whatever genuinely fails against PG 18.3** (DONE
  2026-09-15) — R14's precedent applies: ask the oracle, the test can be wrong.
  Do not change planner behaviour to satisfy a pin the oracle contradicts.
  - **Scope correction to M0140-0001**: a full `go test
    ./internal/optimizer/... ./internal/executor/...` sweep under the flip
    finds **seven** failures, not the four M0140-0001 named by re-running only
    the R43-era list. Three new: `TestPartialPathIsNeverTheFinalPath`,
    `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput`,
    `TestSlice3CorrelatedBodyDeclinesParentAware`. M0140-0003 must gate on a
    full-package sweep, not a named list, or it will under-count again.
  - Hit a `go test` result-cache false-PASS while measuring
    `TestOwnedBuildPoisonPrebuiltBoundary` under the flip (no `-count=1`); every
    number in the design doc was re-confirmed with `-count=1` after first
    observing it without. Record for future GOOPG_GATHER_PATHS measurement
    rounds.
  - **`TestSplitEqualityForHashMultiKey/searched_enumerator` — FIXED, was a
    test-harness bug.** Probed the actual plan: under the flip it correctly
    chooses `JoinAlgoHash` with both keys set, wrapped in a `Gather` the
    shared `visit()` test helper (`q21_live_test.go`) had no `case *Gather`
    for (production code's `boundaryWalkChildren` was already fixed for the
    identical reason under R11). Added `case *Gather`/`*GatherMerge` to
    `visit()`; confirmed PASS under the flip, unaffected at default, full
    `go test ./internal/optimizer/...` (default) still green.
  - **`TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`,
    `TestOwnedBuildPoisonPrebuiltBoundary`** — stale shape/optimization pins,
    not a correctness bug. Adjudicated against a live PG 18.3 `EXPLAIN` of the
    real TPC-H Q9 query (SF=1, `bench/tpch` port 65432): PG's actual plan
    (orders joined last via *Parallel* Hash Join under a `Gather`; lineitem
    joined via Nested Loop + Index Scan, never hashed) matches **neither**
    goopg arm, so "closer to PG" cannot decide between them — Q9's join-order
    divergence predates this flip (K26/R51-53/R68, gated to M0142). Concrete
    effect: a filter-column-drop optimization declines to fire once its leaf
    sits under a Gather-admitted ancestor (one extra column carried, no data
    loss, no wrong reads). Re-pin all three when M0140-0003 lands the flip.
  - **`TestPartialPathIsNeverTheFinalPath`** — the safety property (no
    partial path ever reaches the *chosen final* plan) still holds under the
    flip; only a secondary staging-order assertion is stale, because the flip
    also activates K80's `addPartialHashJoinPath` producer, not gated behind
    the pass (`C-19d`) the fixture assumed was the sole source. Re-pin at
    M0140-0003.
  - **`TestQ2DecorrelatedGroupKeyResolvesInAggregateInput` /
    `TestSlice3CorrelatedBodyDeclinesParentAware`** — real, most consequential
    finding: TPC-H Q2's scalar-aggregate decorrelation **declines entirely**
    under the flip (falls back to a per-outer-row `SubPlan`) — not proven
    incorrect (SubPlan results are still right), but flagged as the strongest
    candidate for a root-cause fix before M0140-0003 lands the flip
    default-on. Root cause not isolated this loop (adjudication, not a fix).
  - No ledger row: all seven failures are goopg-internal mechanism
    interactions (partial-path admission vs. narrowing / decorrelation / a
    test walker), not newly discovered PG-incompatibilities.
  - Design doc: `docs/design/0100-0149/m0140-0002-gather-paths-flip-failing-set-adjudication.md`.
- [x] **M0140-0003 — land the `GOOPG_GATHER_PATHS` flip on the category metric** —
  pre-register **no match flip**: R43 rev 3 measured TPC-H `parallelism` 18->16 under
  the flip with no new match, and K38 measured Gather 42->104 / `Parallel Hash` 0->167
  at `all` with parity not improving. The category movement is the success criterion.
  (DONE 2026-09-15)
  - **Root-caused and fixed** the Q2-decorrelation decline M0140-0002 flagged as
    the blocker to isolate before landing: `clonePlanReplacingOuter` (`unnest.go`)
    had no `*Gather`/`*GatherMerge` case, so a partial path won inside a scalar
    subquery's own inner plan made the correlation-substitution clone bail with
    "unsupported plan node", silently keeping Q2's aggregate a per-outer-row
    `SubPlan`. Fixed with a single-child recursion arm, the same bug class as
    R11's `boundaryWalkChildren` fix and M0140-0002's `visit()` fix. Two wrong
    hypotheses instrumented and refuted first (see the design doc's numbered
    list) before finding the real bail point.
  - **Flipped the default**: `gatherPathModeFromEnv`'s unset/empty case now
    resolves to `all` (was `off`); `off`/`false` still reproduce the pre-flip
    arm. Updated `gatherpaths.go`'s header comment, `flaglabels.go`'s two
    provenance comments, `docs/design/planner-c19d-gather-paths/DESIGN.md` §5
    (dated addendum, history preserved), and regenerated
    `scripts/planner-flags.env` (`TestFlagProvenanceEnvIsGenerated` caught the
    initial miss).
  - **Re-pinned the four stale tests** M0140-0002 pre-adjudicated as safe:
    `TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`,
    `TestOwnedBuildPoisonPrebuiltBoundary` (post-flip shape), and
    `TestPartialPathIsNeverTheFinalPath` (join-rel-population timing check now
    scoped to `gatherPathsMode==off`; its unconditional final-tree safety
    property is untouched). Full optimizer+executor sweep green in all three
    arms (default/`off`/`all`).
  - **Measured, both arms, same commit, same stats epoch**: TPC-DS SF0.25 vs
    live PG — match held at the canonical 2 (no new match, none lost),
    `parallelism` 86->85, other categories absorbing the reshaped joins; a
    clean off-vs-all diff shows 50/99 queries shape-changed, every one tagged
    `parallelism`; values gate `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
    TIMEOUT=0`. TPC-H: canonical serial scoreboard protocol confirmed inert to
    the flip (`parallelism=0`, `match=6` unmoved — partial-path admission needs
    `max_parallel_workers_per_gather>0`); the non-serial diagnostic protocol
    (matching R43/K38's own measurement) reproduces `parallelism 17->16, no new
    match` at HEAD live against PG — independently reproducing R43 rev 3's
    historical `18->16` finding. `tpch-spotcheck.sh` PASS (`Q12=2/Q13=34`) under
    the new default.
  - Pre-commit gate's only failures (`internal/parser` golden-fixture drift,
    untracked `bak/` build failure) are pre-existing and unrelated — the former
    already filed as nightly `AI-20260914-235643-001` under M-NIGHTLY, before
    this task began.
  - Design doc: `docs/design/0100-0149/m0140-0003-gather-paths-flip-lands-default-on.md`.
- [x] **M0140-0004 — partial-Append producer (K43)** — there are currently zero
  partial paths on join rels via that route; PG uses Parallel Append in six TPC-DS
  queries and only Q5 and Q76 miss. (Recon done, DEFERRED 2026-09-15 — the
  milestone DoD explicitly allows "or its absence is a filed ledger row with a
  resume point" as the disposition.)
  - Confirmed the K43 framing: Q5/Q76 both hinge on `UNION ALL` chains, and the
    live PG oracle capture shows `Parallel Append` wrapping exactly those
    branches.
  - The gap is deeper than the task description implied (a K80-style
    "flip a flag on an already-written mechanism"): goopg has **no cost-based
    Append path producer at all**. `createSetOpPaths` (the real UNION-ALL
    producer, `windowsetoppaths.go:259`) offers exactly one candidate per its
    own header note, which already states "an APPEND path over an appendrel is
    its own item".
  - Root structural blocker: each UNION ALL branch is planned to a **finished,
    opaque `Node`** (`planner.go:1114`'s `planSelectWithSettings`) before
    `createSetOpPaths` ever sees it — `seedPathForNode` wraps a `Node`, not a
    `RelOptInfo`, so a branch's own partial/parallel candidate has no channel
    to reach the SetOp rel's `PartialPathlist`.
  - Landing a real producer needs (1) branches exposed with a `PartialPathlist`
    instead of a finished `Node` — a change to the shared SetOp-branch entry
    point used by every SetOp query in the suite, not a Q5/Q76-scoped edit —
    (2) a new partial-Append producer mirroring `addPartialHashJoinPath`'s
    shape with PG's `cost_append` partial-path arithmetic, and (3) new
    executor claim logic: a subagent recon found the executor's `setOp` node
    has NO worker-partitioning at all (unlike `parallel_scan.go`'s claim-set
    machinery for base scans), so wrapping it in a `Gather` today would
    **duplicate every row N-fold** — a correctness gap, not just a missing
    optimization. Each is comparable in size to an entire prior C-19-series
    slice; out of reach for one loop. Two prior-phase rounds
    (`r32-targetlist-subplan-display`, `r33-subquery-parallel-pass`)
    independently reached the same "full round of its own" conclusion.
  - No production change, no test pins moved. Ledger row:
    `m0140-0004-partial-append-producer`. Design doc:
    `docs/design/0100-0149/m0140-0004-partial-append-producer-recon-and-defer.md`.
- [x] **M0140-0005 — file the two out-of-reach items as ledger rows** — Q14's third
  category (K92: needs PG's real partial-inner execution model, "NOT cheap") and the
  non-planner floor (K14/K15 heap density; K41's unexplained dimension-table `relpages`
  divergence, `customer` 1,979 vs 2,872 and `item` 716 vs 1,284). Each row names the
  mechanism and what would unblock it; neither is silently dropped. (DONE 2026-09-15
  — filing only, no production change; both items were already fully investigated in
  the prior phase's record, this task files them.)
  - K92 (Q14's `parallelism` mismatch): PG's `Parallel Hash` needs workers building a
    shared hash from a partial inner path behind a barrier; goopg's `joinOp`
    deliberately drains the build side once on the leader before fan-out
    (`parallel_scan.go`'s own comment warns the alternative "would silently drop
    matches"). Not a labelling fix — needs new executor machinery (shared/DSM-
    equivalent build target + barrier), comparable in size to a C-19-series slice.
  - K14/K15/K41 (non-planner heap-density floor): `relpages` is a planner *input*, no
    cost-model change can fix a wrong input. K39 already closed the fact-table
    direction (`store_sales` 0.4% off PG); K41 (dimension tables, `customer`
    1,979 vs 2,872, `item` 716 vs 1,284) stays open and unexplained — `character(N)`
    blank-padding (R23) is the leading candidate mechanism but was never isolated as
    K41's specific cause. Unblock: direct per-page free-space comparison, then R22
    (heap fill) / R23 (blank-padding) — on-disk work, out of the planner remit.
    **K41 now has an owner** (2026-09-15): `M0143-0007`, filed under the engine
    carry-overs because it is an on-disk defect, not a planner one. K14/K15 stay
    deferred.
  - Ledger rows: `m0140-0005-q14-parallel-hash-execution-model`,
    `m0140-0005-nonplanner-heap-density-floor`.
  - Design doc:
    `docs/design/0100-0149/m0140-0005-q14-third-category-and-nonplanner-floor-filing.md`.
  - Superseded 2026-09-15: M0140-0006 re-opens the producer K43 deferred, so
    the milestone is **5 of 6**, not closed.
- [x] **M0140-0006 — partial-Append producer (K43), the implementation** —
  M0140-0004 did the recon and filed a ledger row, but under the old DoD wording
  that was enough to close the task, leaving the producer an orphan. The recon is
  valuable and must be read first: each UNION ALL branch is folded into a
  finished opaque `Node` at `planner.go:1114` before `createSetOpPaths` sees it,
  so there is no route to a `PartialPathlist`; and **wrapping today's `setOp`
  node in a `Gather` would silently duplicate every row** because it has no
  claim-set analogue of `parallel_scan.go`. That row-duplication defect is a
  correctness bug in its own right and must be fixed before or with the producer.
  **Scope from the recon's own correction, not from the task title it replaces**:
  the "only Q5 and Q76 miss" framing is wrong — `m0140-0004-…-recon-and-defer.md:35-40`
  records that Q2/Q14/Q71 also emit a plain serial `Append` and merely carry a
  compensating `Gather`/`Gather Merge` elsewhere, and a 2026-09-15 count of
  goopg's 99 TPC-DS plans finds **`Parallel Append` zero times**. Treat all six
  as missing. The six-query list itself is reference-dependent — the committed
  fixture `bench/tpcds/plans-pg/` shows five (Q2/Q5/Q14/Q71/Q76) and the SF0.25
  capture shows six (adding Q75); settle which reference is canonical under
  M0137-0004/0005 before quoting a denominator.
  - **DONE 2026-09-15 as decomposition, not implementation** (design doc
    `docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md`). Re-grounded
    the M0140-0004 recon against current line numbers
    (`planner.go:1106-1153`, `windowsetoppaths.go:258-293`, both unchanged in
    shape) and reconfirmed its own sizing claim: landing a real producer needs
    three structurally independent pieces, each comparable to a whole prior
    C-19-series slice, and the shared SetOp-branch fold is dense with
    precedence-correctness invariants (`M0125-0016`) that a blind edit risks
    breaking across every SetOp query in both suites, not just Q5/Q76 — a
    one-task-per-loop violation to attempt blind. Split into three loop-sized
    sub-tasks below: **M0140-0006a** (branch `PartialPathlist` exposure, pure
    plumbing), **M0140-0006b** (the partial-Append cost producer), **M0140-0006c**
    (executor claim-set for `setOp` under `Gather` — correctness prerequisite,
    must land before or with 0006b going live). No production code changed.
    Ledger row appended (task-id `m0140-0006`).
- [ ] **M0140-0006a — expose SetOp-branch `PartialPathlist`.** Give each UNION
  ALL branch (`planner.go:1106` `planSegment`, folded via `applySetOp`/
  `foldSetOpRange` at `planner.go:1120-1208`) a route to retain a `RelOptInfo`
  with a `PartialPathlist` instead of collapsing straight to a finished `Node`
  via `planSelectWithSettings` (`planner.go:1114`), so `createSetOpPaths`
  (`windowsetoppaths.go:258-293`) has something other than `seedPathForNode`'s
  opaque-`Node` wrap (`windowsetoppaths.go:298`) to read a partial candidate
  from. Pure plumbing: no new cost producer, no new executor node — the field
  can go unread until 0006b exists. **Acceptance:** TPC-H (`match=8`) and
  TPC-DS (`match=2`) both byte-identical before/after
  (`shape-delta.sh shape-changed=0`) — nothing should select a partial path
  yet. Read `docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md`
  first.
- [ ] **M0140-0006b — the partial-Append cost producer.** Depends on
  M0140-0006a. The `addPartialHashJoinPath` counterpart
  (`joinpathsparallel.go:82`'s shape): seed `setOpRel.PartialPathlist` from
  0006a's branch partial paths, priced on PG's `cost_append` partial-path
  arithmetic (`postgres/src/backend/optimizer/path/costsize.c:2250`,
  streaming-UNION-ALL comment already cited by `windowsetoppaths.go:337-339`'s
  serial arm), gated behind `gatherPathsMode` (`gatherpaths.go`) the same way
  K80 is. Verify `generateUsefulGatherPaths` (`considerparallel.go`) reads the
  new `PartialPathlist` for free as part of this task's own acceptance (expected
  per the original recon, unverified). **Must not be promoted default-on, nor
  exercised by any gate that could select it, until M0140-0006c lands** — see
  0006c for why. Re-measure Q5/Q76 (and Q2/Q14/Q71/Q75 per the six-query
  denominator note above) via `scripts/tpcds-sf025-regression.sh`.
- [ ] **M0140-0006c — executor claim-set for `setOp` under `Gather`.** A
  correctness prerequisite, not an optimization, and independent of
  0006a/0006b's path-search work. `gatherOp` (`internal/executor/
  operators_gather.go`) has each worker build its own full copy of the child
  subtree — safe for a base scan only because `parallel_scan.go`'s
  `ParallelGroup`/`claimed()`/`claimLeaf`/`parallelClaimSet` hand each worker a
  distinct block range. `setOp` (`operators_setop.go:32` `newSetOp`) has no
  equivalent claim logic — `Open` streams both children to completion
  unconditionally. Landing 0006b without this first would make every parallel
  worker replay both entire UNION ALL branches once `gatherPathsMode` picks the
  new partial-Append candidate — silent row duplication, a wrong-answer defect
  (Hard-won Rule #1). **Must land before or with 0006b's flag going live.**

## M0141 — Upper-planner ordering contest (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0141-upper-planner-ordering-contest.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Source:** `METHODOLOGY3/04-forward-plan.md` Phase 1 Campaign B
**Prerequisites:** M0137, **and this milestone's own S0.**

The named lever for `aggregation-strategy` (TPC-DS 69, TPC-H 10) and, through
the same mechanism, `sort-strategy` (76 / 9) — K24 calls it the largest single
item in the workstream. **It is scoping-gated, not scheduled**, because the
record refuses to size it: `METHODOLOGY3/02` §B4 calls it "named and unscoped",
K96 says the shape PG picks is unreachable by construction, and K97 records R45's
fix as rejected on review as architecturally impossible, naming the real item as
PG's `AGGSPLIT_INITIAL_SERIAL` / `AGGSPLIT_FINAL_DESERIAL` — a multi-round
executor programme of unstated size. This is the weakest link in the owner's
"(c) split the goal" decision, and S0 exists to price it.

Constraints to start from, not rediscover: goopg's Partial aggregate emits
**zero rows** (K97), `aggregateOp` already sorts for determinism so incidental
ordering is not a pathkey (K97), the upper planner receives a finished `Node`
rather than the join rel's paths and K23/K12(B) share that root cause (K24), and
PG's preference is **pathkey-driven, not spill-driven** — R120 already proved the
spill route is net-negative.

- [x] **M0141-S0 — scoping recon (measurement only, no production change)** — DONE
  2026-09-15, design doc
  `docs/design/0100-0149/m0141-s0-aggsplit-programme-scoping-recon.md`. No
  production change. Corrects K96/K97's framing rather than just sizing it:
  goopg already has a working Partial/Finalize split (`AggMode`,
  `combineAggRuntime`'s per-aggregate combine rules, `AggregateIsDecomposable`'s
  whitelist, two independent plan producers — do not rebuild any of this). What
  is genuinely missing is one combination, `AggStrategySorted ×
  AggMode∈{Partial,Final}` (PG's `Finalize GroupAggregate <- Gather Merge <-
  Partial GroupAggregate`), and it is **structurally incompatible** with
  today's design, not just absent: today's Partial mode emits zero rows via an
  in-process side-channel accumulator specifically so `Gather` stays
  aggregation-agnostic, but `GatherMerge` must interleave real per-group rows
  by sort key — the side channel has none to interleave, so the row-emitting
  path (plus `aggRuntime` serialize/deserialize, PG's `aggserialfn`/
  `aggdeserialfn`, deliberately never built) is a second mechanism needed
  alongside the first, not a fix to it.
  - **Decisive scoping fact**: TPC-H's canonical scoring protocol runs
    `-serial=true` (`max_parallel_workers_per_gather=0` on both engines), so
    **none** of TPC-H's `aggregation-strategy`(10)/`sort-strategy`(9) can
    involve the missing Gather-Merge machinery — it's a serial
    Hashed-vs-Sorted cost/path-selection question over machinery
    (`openSorted`, the sorted `PathAgg` candidate, `createplansimple.go`'s
    existing `Strategy` wiring) that already exists. TPC-DS's 69/76 is an
    unseparated mix of that same serial question and genuinely parallel cases
    already gated behind M0140's own open floor (K92, K41).
  - Filed six slices below. **S1 is selectable now** (cheap, no
    prerequisite beyond M0137); **S3-S6** (the real multi-round AGGSPLIT
    machinery) are gated on S1/S2's live measurement showing a
    parallel-shaped residual large enough to justify them — do not start S3
    on K96/K97's say-so alone.

- [x] **M0141-S1 — serial Hashed-vs-Sorted audit (selectable now)** — DONE
  2026-09-15, design doc
  `docs/design/0100-0149/m0141-s1-serial-aggstrategy-audit.md`. No production
  change. **Action 1** (resolve the `plan.go:1346-1348` vs
  `createplansimple.go:173,219` contradiction): the comment is stale/wrong.
  `groupingpaths.go:addGroupingPaths` already runs a genuine cost-based
  Hashed-vs-Sorted `PathAgg` contest via `addPath`; `createAggPlan`/
  `createFinalizeAggPlan` copy the winner's `AggStrategy` onto the executor
  node (`out.Strategy = p.AggStrategy`); `operators_join_agg.go:2222`'s
  `openSorted` dispatch guard's `Mode == AggModeSimple` clause is
  unconditionally true under `-serial=true` — so the serial path is wired
  end to end, no missing link. Open for S2: *why* the contest doesn't pick
  PG's shape (cost-term mismatch vs candidate never generated), not whether
  it runs. **Action 2** (live capture + split): fresh capture against the
  live bench clusters confirmed TPC-H's 10 aggregation-strategy/9
  sort-strategy tags are serial-shaped by construction (`parallelism=0`
  under `-serial=true` on both engines — match=6 floor held). For TPC-DS
  (SF0.25, `capture-tpcds.sh`, parallelism NOT forced off —
  `max_parallel_workers_per_gather=4`), classified each of 83 union-tagged
  queries by whether **PG's own reference plan** contains a
  `Partial`/`Finalize (Group)?Aggregate` or `Gather Merge` marker: 51 are
  AGGSPLIT-touched (stays gated per S0's entry gate + M0140's parallelism
  floor), **32 are fully serial in PG's own plan** — in S1/S2 scope right
  now (list: Q1, Q2, Q8, Q10, Q18, Q22, Q23, Q24, Q26, Q30, Q31, Q32, Q42,
  Q45, Q49, Q52, Q53, Q54, Q55, Q56, Q59, Q60, Q63, Q69, Q71, Q80, Q81, Q83,
  Q91, Q92, Q93, Q94; match=2 floor held). Materially sharper than S0's
  "unseparated mix" placeholder. Caveat: marker search, not a structural
  proof the marker attaches to the query's aggregate specifically — S2
  should re-verify per query it actually works.
- [x] **M0141-S2 — serial Hashed-vs-Sorted mechanism trace** — DONE
  2026-09-15, design doc
  `docs/design/0100-0149/m0141-s2-serial-hashed-sorted-mechanism-trace.md`.
  No production change beyond the one-line `plan.go:1343-1349` comment
  correction S1 diagnosed as stale (landed). Traced TPC-H's 10
  `aggregation-strategy` queries against S1's committed captures (no new live
  capture): both candidates are **always** generated (S1's open question
  resolves to uniformly "(a) priced-and-lost", never "(b) never generated"),
  but "priced-and-lost" splits into two unrelated mechanisms neither of which
  is the bounded local patch S2 was scoped to expect. **Mechanism (A)** (Q3
  only): un-narrowed `Hash Join` output inflates `costAgg`'s R3 spill-arm
  width term, over-charging HASHED — a known, already-decided tradeoff
  (R3 reinstated the spill arm over this exact TPC-H objection to fix a
  larger TPC-DS regression); gated on **M0139** (executor-side narrowing),
  do not touch the spill arm before then. **Mechanism (B)** (the majority:
  Q4, Q5, Q8, Q12, Q21, Q22 confirmed; Q13/Q18 mixed, not yet per-node-traced):
  goopg's local cost is not wrong in isolation, but the top-level `ORDER BY`
  step (`createOrderedPaths`/`addOrderedPaths`, `upperordered.go:64-128`)
  consumes a single pre-collapsed `Node` from the GROUP_AGG rel rather than
  its `Pathlist`, so the joint "Sorted-agg-skips-the-top-Sort vs
  Hashed-agg-plus-top-Sort" comparison PG makes never happens — this is
  METHODOLOGY3's already-named F15/K12(B)/K24 root cause ("the upper planner
  receives a finished `Node`, not the join rel's paths ... the largest single
  item in the workstream"), confirmed here as the *dominant* mechanism (6/8
  classified queries) for this corpus specifically, not a new local gap.
  **S2 does not land a fix**: mechanism (A) is M0139-gated, mechanism (B)
  needs the same upper-planner Pathlist-not-Node surgery K24 already scoped
  as the workstream's largest item — landing either here would be forcing a
  shape or violating "one task per loop". Filed as two gated resume points
  below (S2a, S2b) instead of closing S2 with a patch.
- [x] **M0141-S2a — mechanism (A) re-measure (Q3, and check Q10/Q13/Q18)** — DONE
  2026-09-15, design doc
  `docs/design/0100-0149/m0141-s2a-mechanism-a-remeasure-post-m0139.md`. No
  production change. M0139 landed in full (all six slices DONE) since S2
  gated this task on it; live re-capture against the same bench clusters
  shows Q3/Q13/Q18 **byte-for-byte unchanged** from S1/S2's capture — same
  costs to two decimal places, same Sorted/Hashed choice, confirmed against
  a serving binary verified (behaviorally, via a known-narrowed two-table
  join) to carry M0139's narrowing. Root-caused: `applyUpperNarrowing`
  (M0139's narrowing entry point) runs at `Plan()`'s tail
  (`planner.go:189`), strictly *after* `planStmtWithSettings` (`:141`) has
  already built, costed and strategy-decided the full tree via
  `createGroupingPaths`/`addGroupingPaths` (`:1760` ->
  `groupingpaths.go:340`) — live `EXPLAIN (VERBOSE)` shows the Hash Join's
  `Output:` list genuinely narrowed to 7 columns while its cost/width
  figures stay identical to pre-M0139. **M0139, as landed and sequenced,
  structurally cannot feed back into `costAgg`'s spill-arm currency for any
  query** — S2's gating assumption ("re-measure once M0139 lands") is
  refuted, not merely unconfirmed. R124 §7 (pre-M0139) already tested an
  adjacent "corrected currency + ncols narrowing" pairing and found it
  measured identical to the currency fix alone — a second, independent data
  point against "narrow, then re-measure" being sufficient. Also: Q10 (named
  by a unit test's doc comment, not by S1's own TPC-H list) already matches
  PG at SF=1 — the test's flip is a small-scale artefact, not a live
  mismatch; not pursued further. Filed **M0141-S2a-fix** below rather than
  attempting the fix here (two coupled changes: move/preview narrowing
  before cost time, and correct the width currency — R124 already falsified
  "currency alone").
- [ ] **M0141-S2a-fix — make `costAgg`'s width currency see the
  post-narrowing input** — `aggInputWidth` (`groupingpaths.go:327`) must read
  a width reflecting what `applyUpperNarrowing`/`narrowJoinLeg` would produce
  for the aggregate's input, evaluated *before or during*
  `createGroupingPaths`'s cost contest, not after `Plan()` returns
  (`planner.go:189`'s current position). Two coupled changes, not a
  one-line patch: (1) move narrowing earlier or compute a narrowing preview
  at cost time (planner-search-order surgery — scope which is cheaper before
  starting); (2) pair it with a corrected `inAvgVarBytes` currency (R120's
  `hashAggTupleWidth` shape — R124 §7 already falsified "currency fix
  alone", so this cannot be reinstating the deleted
  `GOOPG_HASHAGG_WIDTH_CURRENCY` verbatim). Re-measure Q3/Q13/Q18 (and the
  TPC-DS 51-query AGGSPLIT-touched set, out of scope until M0140's floor)
  after. Needs a dedicated scoping pass before attempting — size which half
  is cheaper first, per M0141-S2a's finding.
  **Prerequisite reading, binding**: `AGENT.md` §"B2 — same statistics, same
  plan; absorb what cannot be made identical", and `M0139-0007`, which
  establishes the absorption principle half (2) applies. Half (2) *is* an
  absorption site, so it inherits both of B2's operational rules — derive the
  substituted width before taking any parity number, and cite the
  `./postgres/` `file:line` whose expression it ports. A width chosen because
  it made the parity number look better is tuning and is rejected.
  - **Scoping done 2026-09-15**, design doc
    `docs/design/0100-0149/m0141-s2a-fix-scoping-recon.md`. No production
    change. **Half (1) is the cheap half, and it is not planner-search-order
    surgery**: `agg.InputTarget []int` (a NAME-derived keep-list) is already
    stamped inside `buildAggregateStage` (`planner.go:8219`) *before*
    `createGroupingPaths`'s cost contest runs (`:1760`), and
    `addGroupingPaths` already receives `aggNode` — the "preview at cost
    time" half (1) asked for already exists, unused, at the exact call site
    `aggInputWidth` runs from. B2 derivation (PG's `cost_agg`/
    `hash_agg_entry_size`, `pathnode.c:3430` /`nodeAgg.c:1701,3701-3703`)
    confirms `agg.InputTarget` is the correct PG-equivalent substitute for
    PG's `subpath->pathtarget->width`. Split into **M0141-S2a-fix1** (wire
    `agg.InputTarget` into the three `aggInputWidth` call sites — cheap,
    attempt first, alone) and **M0141-S2a-fix2** (the `hashAggEntrySize`
    fixed-overhead currency correction — gated on fix1's own measured
    result; R124 §7's prior "currency + narrowing" pairing never had a
    live-at-cost-time preview, so it is not dispositive against retrying
    once fix1 supplies one).
- [x] **M0141-S2a-fix1 — wire `agg.InputTarget` into `aggInputWidth`'s three
  call sites** (`groupingpaths.go:344`, `partialaggpaths.go:338`,
  `partialaggupper.go:327`): when `agg.InputTargetKnown`, compute
  `(ncols, avgVarBytes)` from the KEPT columns (`agg.Child.Output()` indexed
  by `agg.InputTarget`) instead of the full `child.Output()`; fall back to
  today's full-width behavior when unknown. Pin with a test asserting the
  cost-preview currency and the executor's actually-committed-or-declined
  narrowing stay deliberately separate (B2's "pinned by a test" rule — see
  the design doc's correctness note on why a declined Hashed-strategy commit
  does not invalidate the preview). Re-measure Q3/Q13/Q18 (TPC-H) and the
  TPC-DS 32-query serial-only set from M0141-S1 after. No currency-formula
  change in this slice — isolate half (1)'s effect before pairing with fix2.
  **DONE 2026-09-15** — landed exactly as scoped, plus the two other real
  call sites the recon's Finding 3 didn't enumerate individually
  (`groupingpaths.go`'s index-ordered variant passes its own narrowed
  `idxSpec`; `partialsortpaths.go`'s bare-`*Sort` site correctly takes the
  unchanged nil-agg fallback, no `Aggregate` in scope there). Pinned by
  `internal/optimizer/agginputwidth_test.go` (2 tests). **Measured on a
  private clone/port only — shared `:65432/:65433/:65437/:65438` clusters
  never restarted**: TPC-H `match` 6 -> 8 (Q3, Q13 flip `SHAPE-DIFF` ->
  `MATCH`, exactly the named queries; Q18 partially improves, drops
  `sort-strategy`); `aggregation-strategy` 10->8, `sort-strategy` 9->6,
  `join-order` 14->12, no category regressed. TPC-DS `match` floor held at
  2; `Q31` (inside M0141-S1's named 32-query serial-shaped set) drops
  `aggregation-strategy`/`sort-strategy`/`parallelism`; those three
  categories fall by 1 each corpus-wide, no category regressed.
  `shape-delta.sh`: TPC-H shape-changed={Q3,Q13,Q18} (exactly the task's
  set); TPC-DS shape-changed={Q31 (real), Q78 (cost-digits-only, verdict
  unchanged)}. Full method, artefacts and the "what every task must
  contain" census: `docs/design/0100-0149/m0141-s2a-fix1-agg-input-width-preview.md`.
- [x] **M0141-S2a-fix2 — `hashAggEntrySize` fixed-overhead currency
  correction** (gated on M0141-S2a-fix1 landing and being measured): add the
  missing `MAXALIGN(SizeofMinimalTupleHeader) + tupleWidth` fixed-overhead
  term PG's `hash_agg_entry_size` (`nodeAgg.c:1701-1730`) charges alongside
  the variable payload, re-derived per B2 (not a verbatim reinstatement of
  the deleted `GOOPG_HASHAGG_WIDTH_CURRENCY`, per M0141-S2a-fix's own text).
  Attempt only after fix1's isolated result is known.
  - **ATTEMPTED and MEASURED 2026-09-15, decision: HOLD — not adopted.**
    Design doc `docs/design/0100-0149/m0141-s2a-fix2-hashaggentrysize-currency-attempt.md`.
    Implemented exactly as scoped: `costAgg`'s spill arm's guard changed to
    `inNcols > 0 || inAvgVarBytes > 0` and its width argument to
    `hashsize.EntryBytes(inNcols, inAvgVarBytes)` (was bare `inAvgVarBytes`),
    restoring "Arm C" (fixed-width inputs now price a real 48·ncols+24
    footprint). Measured clean against a same-PG-reference control (TPC-H,
    to strip PG-side sampling noise between independent captures) and a
    matching-stats-epoch comparison (TPC-DS): **TPC-H `match` unchanged at
    8**, one query (Q18) changes shape LATERALLY (+1 `sort-strategy`, -1
    `rendering`, stays `SHAPE-DIFF`); **TPC-DS `match` unchanged at 2**, one
    query (Q31) changes shape and REGRESSES three categories
    (`aggregation-strategy`/`sort-strategy`/`parallelism`, +1 each) back to
    byte-identical with the PRE-fix1 baseline — fix2 exactly cancels fix1's
    one TPC-DS gain. No `MATCH` lost anywhere (the hard non-regression floor
    is unaffected), but no net category-movement gain either, and TPC-DS
    nets negative with no compensating story (unlike B3's kept regression,
    which had a genuine bug fix to weigh against it). This reproduces R124
    §7's "measured net-neutral" verdict for the identical currency
    correction a second time, now WITH fix1's live-at-cost-time `inNcols`
    preview the task text hoped would change the outcome — it did not.
    **Code reverted** (`git checkout --` on `cost_funcs.go` and its four
    accompanying test files) after measurement, matching M0137-0009's
    "DELETE rather than carry another round" precedent rather than
    introducing a new flag. 4 measurement artefacts committed
    (`analysis/m0141/m0141-s2a-fix2-*`). Treat `hashAggEntrySize`'s currency
    as closed for this milestone group absent new evidence — do not
    re-attempt without a different substitution or a different mechanism.
- [ ] **M0141-S2b — GROUP_AGG rel publishes Pathlist, not Node, to the
  ORDER BY step** — the K24 "ordering contest, slice 3" surgery: change
  `createOrderedPaths`'s callers (every `createXPaths` -> `createOrderedPaths`
  call site in `planner.go`) to hand it the producing rel's `Pathlist`
  instead of a pre-collapsed `Node`, and change `addOrderedPaths`
  (`upperordered.go:116-128`) to run its own cost contest per candidate
  (`pathkeysContainedIn` match = no Sort; otherwise + `sortPathForBounded`)
  rather than assuming a single input. This is the fix mechanism (B) in
  M0141-S2's finding needs — real multi-call-site upper-planner surgery, size
  it as its own scoping task before attempting it (K24 already calls this the
  workstream's largest single item; do not attempt in one sitting). Needs
  M0141-S2 (done, see above) for the concrete TPC-H query list motivating it.
- [ ] **M0141-S3 — Partial-Sorted row emission** — a second Partial-mode code
  path (`Strategy = AggStrategySorted`) emitting real rows (group key + one
  serialized-state column per aggregate) instead of merging into
  `aggPartialAccum`; reuses `combineAggRuntime`'s existing rules; leaves the
  hash-strategy accumulator path untouched. **Entry gate: needs S1/S2's live
  measurement to show a TPC-DS-specific, genuinely parallel-shaped residual
  large enough to justify it.**
- [ ] **M0141-S4 — `aggRuntime` serialize/deserialize** — PG's
  `aggserialfn`/`aggdeserialfn`, bounded to the decomposable whitelist's actual
  pointer surface (`numericSum`, `intSx`/`intSxx *big.Int`,
  `numericSx`/`numericSxx *big.Rat`, `userState`, the float/bool/count/sum
  scalars — `DISTINCT`/`WITHIN GROUP`/`array_agg`/`string_agg` are already
  refused by `AggregateIsDecomposable`). Needs S3 (defines the transport
  shape).
- [ ] **M0141-S5 — GatherMerge-fed Finalize-Sorted** — a new merge-combine
  executor operator consuming the key-interleaved stream `GatherMerge`
  produces from S3/S4's rows, folding same-key runs across workers via
  `combineAggRuntime` before finalizing (today's Finalize path only handles
  "drain the Gather to EOF, read the whole accumulator", which is wrong for a
  merge-ordered stream where a key can recur). Needs S3+S4.
- [ ] **M0141-S6 — wire and measure** — pick which of the two existing
  producers (`parallel.go`'s legacy pass, or `partialaggupper.go`'s costed-path
  version — note its own unresolved 7/22-vs-12/22 regression against the
  legacy pass, out of scope to fix here) hosts the new shape; fix the
  `Aggregate`/`GroupAggregate (N keys)` EXPLAIN mislabel (doc
  `parallel-query/06` §4.1 — both currently render as a hash aggregate
  regardless of `Strategy`); re-measure the full corpus. Needs S5.
- [ ] **M0141-S7 — re-adjudicate and implement Incremental Sort** — **verified
  2026-09-15: PG emits `Incremental Sort` in 14 of the 99 TPC-DS reference plans
  (`bench/tpcds/plans-pg/`), and goopg has no implementation at all** — the only
  occurrences in `internal/` are **7 hits across 7 files, every one a comment or
  a test string**: `windowsetoppaths.go:23`, `createplansimple.go:267`,
  `groupagg_indexorder.go:18`, `groupagg_indexorder_test.go:129`,
  `executor/sort_presorted_test.go:5`, `estimateaudit/spine.go:176`, and a live
  map key `"Incremental Sort": false` in `estimateaudit/parity_test.go:58`.
  **No executor or planner node implements it.** Those 14
  queries are therefore **structurally unable to MATCH**, whatever the costing
  does. The ledger row `take3-C-14-dropped` declined it, but **on the previous
  goal**: its stated reasons were performance-shaped ("TPC-H has no LIMIT",
  "0/100 TPC-DS sorts spill", "0.015% of the corpus") and it says outright that
  "plan parity alone is NOT sufficient grounds". **Under the current goal that
  judgement inverts** and the row needs re-adjudicating before implementation.
  Port `create_incremental_sort_path` and the executor node; PG oracle
  `postgres/src/backend/optimizer/path/pathkeys.c` + `nodeIncrementalSort.c`.
  Owner of the `sort-strategy` category (TPC-DS 76 / TPC-H 9).

## M0142 — Join-order costing (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0142-join-order-costing.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Source:** `METHODOLOGY3/04-forward-plan.md` Phase 3, unblocked by the Question 2 answer
**Prerequisites:** **M0138** (landed and measured) and M0137.

`join-order` is the largest category on both corpora — **TPC-H 14, TPC-DS 89** at
the 2026-09-14 baseline (`estimate-audit -plan-only -serial` for TPC-H, live PG
`:65438` for TPC-DS). The R51-era figures quoted below are **18 / 95**, measured on
the pre-R66 capture protocol against different references; the two are not
commensurable and must not be differenced. Re-measure rather than subtract.
**The candidate half is already open at HEAD**: `joinsearchseam.go:461` calls
`inferTransitiveEqualities` unconditionally and R51 adjudicated both Slice-3
tests against PG, so **K26 §9.3's "constants-only" description is a pre-R51
snapshot — do not schedule against it.** Only the costing half remains. R51 also
measured what opening candidates buys on its own: TPC-H `join-order` 18->18,
TPC-DS 95->95.

Three attribution rounds converged independently on pricing (R53 and R68 on Q9;
R96 on Q96, margin 157.50 = 0.68%), and **every pricing round then terminated
blocked** — R70 on fragility, R89 C2 on missing inputs, R98 UNOBSERVABLE, R99
ORACLE INVALID. That is why this milestone opens with a recon.

Margins here are fractions of a percent and the elections are exact: `add_path`
uses a 1% fuzzy comparator but `setCheapest` takes the exact raw minimum on both
engines. **Instrument the term; never infer it from the sum** — K61 and K64 were
both wrong for exactly that reason.

**Two unowned inputs sit directly under this milestone and must be re-measured
before any costing conclusion:** B10's `indexCorrelationFor` returning 0 when the
leading column has no correlation slot (which prices **every** such index scan at
`max_IO_cost`) and R30's residue that ANALYZE never visits indexes, so
`estimateIndexGeometry` **synthesises** relpages/reltuples/tree_height. M0138-0004
populates correlation slots as a side effect. Also carry B8: at
`indexProbeCostMultiplier = 1` the DP picks PG-shaped NL plans that run 2–3x
slower — the knob is the known parity-vs-runtime conflict, and changing it is a
cross-layer programme that has never been scoped.

- [x] **M0142-0001 — entry recon: is the pricing blockage still the same one?**
  (measurement only) — DONE 2026-09-15, see
  `docs/design/0100-0149/m0142-0001-entry-recon-pricing-blockage-post-m0138.md`.
  Corpus counts essentially unchanged (TPC-H join-order 14→14, TPC-DS 89→90,
  floors held) but that hides per-query movement. **Verdict, split by the two
  queries the blocked rounds were actually about**: **Q9/B2 — blockage MOVED**
  (was a narrow-margin-instrumentation question, R53/R68; is now a join-search
  topology-generation question — both engines share the first join
  `partsupp⋈part` then diverge in relset choice at every later step, so there
  is no longer "the same shape priced differently" for a margin audit to
  attach to). **Q96/B5 — blockage UNCHANGED** (PG-side observability gap,
  R98/R99, orthogonal to M0137-M0140). Slices filed below.
- [x] **M0142-0002 — re-measure Q9's join order after M0138** — DONE, folded
  into M0142-0001's task/doc above (same commit): estimate half already
  covered by M0138-0006 (97→146, still 344x below PG's 60,125, not "toward
  PG"); this task added the join-order/category half (single-category
  `SHAPE-DIFF [join-order]`, topology-divergence trace). Not run as a
  separate task since the recon already produced the report M0142-0002 asked
  for.
- [x] **M0142-0003a — join-search candidate trace (env-gated instrumentation,
  measurement only)** — DONE 2026-09-15, see
  `docs/design/0100-0149/m0142-0003a-q9-joinsearch-candidate-trace.md`. The
  proposed trace point turned out to already exist
  (`GOOPG_PGSHAPED_DP_TRACE=1` — `joinsearchtrace.go`'s `DPTRACE pair/cost`
  lines plus `pathtrace.go`'s `DPPATH` per-path partition attribution, both
  landed by the prior R53 Step-0/slice-1 rounds), so the task ran it live
  against Q9 post-M0138 instead of building anything. **Finding 1: PG's
  topology is fully enumerated at every level (L2–L6), never declined — not
  a completeness gap.** **Finding 2: at L6, PG's own chain's cheapest
  candidate (`nestloop.index`, +orders onto PG's own L5) renders identically
  to goopg's winner (`join.hash`, +nation) at two-decimal precision (both
  `80099.64`) yet is marked `dominated`** — a near-exact tie, not a clear
  cost gap; per-step marginal costs differ hugely (orders-last ~33 marginal
  on PG's cheaper L5 base vs nation-last ~2.6 marginal on goopg's pricier L5
  base) and happen to land almost on top of each other. Resolves
  M0142-0003b's fork: costing-term branch, not completeness.
- [x] **M0142-0003b — L6 tie-break precision: does goopg's chosen plan
  actually win on true cost?** DONE 2026-09-15
  (`docs/design/0100-0149/m0142-0003b-q9-l6-tie-break-precision.md`).
  Bumped `DPPATH`'s `formatPathLine` (`internal/optimizer/pathtrace.go`)
  from `%.2f` to `%g` for `startup`/`total`/`inputtotal` (matches the
  sibling `DPTRACE cost` channel's existing precision; env-gated, no
  default-path change) and re-ran -0003a's Q9 recon. **The L6 tie is
  EXACT**: goopg's winning `join.hash` and PG's own chain's `nestloop.index`
  candidate both total `108806.04332442369` bit-for-bit, despite different
  (input, marginal) compositions. goopg's own partition is registered FIRST
  at level 6 (`DPTRACE pair created=1`); PG's chain arrives later
  (`created=0`) and is rejected by `addToPathlist`'s exact-tie dominance
  check — which itself ports PG's real `compare_path_costs_fuzzily`/
  `STD_FUZZ_FACTOR` verbatim, not a goopg shortcut. **Verdict: goopg does
  not win on true cost (settles the fork: no clear cost gap), and the
  tie-break mechanism is not the defect either (it's PG-faithful) — the
  load-bearing divergence is DP enumeration ORDER at level 6, not a costing
  term (B8/B10 not implicated here).** Files M0142-0003c below as the
  concrete follow-on.
- [ ] **M0142-0003c — level-6 enumeration-order parity vs PG's
  `join_search_one_level`** — filed by -0003b's finding. goopg's own
  partition is registered first at Q9's level 6 (`created=1` in `DPTRACE
  pair`); PG's chain's equivalent pairing arrives later and loses an exact
  cost tie to `addToPathlist`'s (PG-faithful) first-registered-wins
  dominance rule. Real PG's planner produces PG's shape as Q9's winner,
  which is only consistent with that same dominance rule if real PG's
  `join_search_one_level` (`postgres/src/backend/optimizer/path/joinrels.c`)
  visits/registers a PG-chain-equivalent relset pairing before goopg's
  chain's equivalent at the analogous level. Concrete next step: instrument
  or read goopg's level-6 relset-pair generation loop
  (`internal/optimizer/joinsearch*.go`) and compare its visitation order
  against `join_search_one_level`'s — same relation set, same clause
  connectivity, does the two engines' iteration order over candidate pairs
  actually differ, and if so does reordering it (to match PG's) flip which
  partition gets registered first and thus which candidate wins the tie?
  This is a genuinely new question (not previously scoped by K26/R53/R79 —
  those examined connectivity/completeness/cost, not iteration order), so
  size it as a recon task first (measurement only) before attempting any
  reordering fix — an enumeration-order change is exactly the class of
  planner-search-order surgery the practice card warns can move OTHER
  queries' plans sideways or worse (K50: any structural reordering can flip
  candidates already matching PG). Lower-priority secondary thread from the
  same task: -0003b's Finding 1 left open why the two candidates' bit-exact
  tie holds despite differently-composed (input, marginal) pairs — informative
  but not required to resolve -0003c.
- [x] **M0142-0004 — re-measure TPC-DS's row-estimate error at HEAD** — the
  ledger row `take3-rowest-collapse-diagnosed` (`.ralph/deferral_ledger.md:2120`,
  2026-09-06) named four cuts in order — **B1, A1, A2, A3** — and pinned the
  headline "3–5 orders out" (Q22 9,460,201 vs PG 11,987 = 789x; 22 of 100 Sort
  inputs estimate 1) *before any of them*. **Verified 2026-09-15: all four have
  since landed.** B1 is `joinkeyproof.go:248` (`case *NestedLoopIndexJoin:`,
  commit `9b43c67f3`, its own comment recording Q99 720,657 -> 90 exact and
  Q62 359,432 -> 150 exact, pinned by `TestResolverFamilyArmListsAgree`); A1 is
  the `s2 += cs.NullFrac` term at `rangequery.go:211-215`; A2 is
  `rangeOpSelectivityStats` (`selectivity.go:322`), MCV-first as PG's
  `scalarineqsel` is; A3 is the `parser.JoinAnti` transplant at
  `reduce_outer_joins.go:141`. **DONE 2026-09-15**, design doc
  `docs/design/0100-0149/m0142-0004-tpcds-rowest-remeasure-at-head.md`.
  Analyzed the `make ea-ratchet` artifacts M0137-0018 had already captured at
  this same HEAD (no planner/executor/catalog diff since) rather than
  re-running the ~10min capture for an unchanged code state. **Distribution**:
  605 nodes scored, 140 flagged at `qerr>=10` — 20 findings >=1000x, 41 at
  100x-1000x, 79 at 10x-100x, max 14,500x (Q47) — roughly 1-4 orders out for
  the ~23% of nodes missing by 10x+; the old single-anecdote "3-5 orders"
  headline's own subject (Q22) now has zero findings at qerr>=10. **B2
  reclassified**: the missing `*Append` arm in `resolveBaseColumn` is still
  structurally absent (confirmed by grep) but no longer reproduces at its
  original witness (Q76 has zero findings now) — latent, not closed, no
  live symptom to chase. **Two new mechanisms filed as M0142-0004a/0004b**
  (below, under this same M0142 milestone) from evidence this pass surfaced
  that the ledger never named: unique-pkey `IndexScan` nodes hard-coded to
  `est=1` where PG's own estimate for the same index is also far from 1
  (C1), and a 3-way CTE `UNION ALL`'s `estimateSetOp`("Append") landing at
  `est=3` against actuals up to 1557 (C2). Both filed recon-scoped (root
  cause not yet attributed) per this milestone's own precedent
  (M0142-0003a/0003b/0003c). **Note the gate gap**:
  `scripts/pg-plan-parity-diff.py`'s nine categories have no `rows=`
  dimension, so estimate error is invisible to every parity gate — it
  reaches the metric only indirectly, by changing plan shape. `make
  ea-ratchet` is the instrument that scores it directly.
- [x] **M0142-0004a — recon: is the unique-pkey `IndexScan` `est=1` finding a
  planner bug or a loops-vs-total capture artifact?** Filed by M0142-0004.
  **DONE 2026-09-15, and FIXED (not just diagnosed)** — the hypothesis was
  half right: `loops>1` is exactly the trigger, but the bug is neither in
  `indexScanRows` nor in the capture/scoring script (`scripts/estimate-parity/parity.py`
  already documents and implements PG's per-loop-average convention
  correctly, lines 48-52). It is in the goopg **ENGINE's own EXPLAIN ANALYZE
  renderer**: `operators_explain.go`'s text line (former :2472-2475), JSON
  field (former :2718 `obj["Actual Rows"] = s.rowsOut`), and per-worker line
  (former :2606) all printed the raw CUMULATIVE `rowsOut` counter
  (`instrument.go`'s `nodeStats.rowsOut`, which accumulates across every
  Open/Next cycle and is never reset on re-Open) instead of dividing by
  `loops`. PG's own `rows=` is `instrument->ntuples / nloops`
  (`postgres/src/backend/commands/explain.c:1835,1901`, confirmed by
  reading the oracle source directly) — a PER-LOOP average, and the
  renderer's own pre-existing comment (`operators_explain.go:2444`, still
  there) already stated this PG convention correctly without the code
  implementing it. Verified with the q34 witness the task named: `Index Scan
  using store_pkey`, raw capture line `(actual rows=9969.00 loops=10082)` —
  9969/10082 = 0.99, matching goopg's own `est=1` almost exactly (qerr would
  drop from 9969x to ~1x), confirming this was never a cardinality-estimator
  defect. **Fix landed**: added `rowsPerLoop(rowsOut, loops)` (divides,
  loops<=0 falls back to the raw value to avoid NaN on an unexecuted node)
  and wired all three call sites through it; added `round2` for the
  JSON/XML/YAML path (`encoding/json` prints a float64 at full precision,
  unlike the text path's free `%.2f` from `fmt`) matching PG's
  `ExplainPropertyFloat(..., ndigits=2, ...)` (`explain_format.c:250`).
  Tests: `TestRowsPerLoopIsPGsPerLoopAverage`, `TestRound2MatchesExplainPropertyFloatNdigits2`
  (`internal/executor/explain_analyze_test.go`) pin the two helpers directly
  — an end-to-end SQL regression test was attempted first via a correlated
  scalar SubPlan (`t1.b > (SELECT t2.b FROM t2 WHERE t2.a=t1.a LIMIT 1)`)
  but the SubPlan's inner subtree renders with NO `(actual ...)` annotation
  at all under this harness (see the new deferral-ledger row below), so it
  could not exercise the fix; a GUC-forced Nested Loop
  (`SET enable_hashjoin=off`) was also tried but `SET` is unsupported at
  this raw-executor test-fixture layer. Gates: full `internal/executor`
  suite green, `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34). **User-
  visible impact beyond the estimator census**: any real `EXPLAIN (ANALYZE)`
  over a correlated subplan or NL/NLI inner side previously printed a
  `rows=` inflated by up to `loops`x versus real PG's output for the
  identical plan — a genuine PG-compatibility defect this task's own
  "fixed at HEAD" framing (from M0142-0004) did not anticipate finding.
  Follow-up filed as **M0142-0004c** (below): re-run `make ea-ratchet` to
  confirm the C1 findings collapse post-fix, since this task fixed root
  cause without re-capturing the artifact that named it. Design doc:
  `docs/design/0100-0149/m0142-0004a-explain-analyze-loops-average.md`.
- [ ] **M0142-0004b — recon: why does a 3-way CTE `UNION ALL` land at
  `est=3` against actuals up to 1557x higher?** Filed by M0142-0004. Q33,
  Q56, Q60 each union three CTE branches (`cs`, `ss`, `ws`, each a filtered
  fact-table query) and the resulting `Append`/`SetOp` node estimates
  exactly `3` rows against actuals of 405/1452/1557 (135x/484x/519x); Q49's
  `HashSetOp Union` estimates 1 against 12 actual. `est=3` for a 3-branch
  union is the signature of each branch independently collapsing to ~1 and
  `estimateSetOp` (`cardinality.go:208`) summing `1+1+1` — that function's
  own UNION/INTERSECT/EXCEPT arithmetic already matches PG's `prepunion.c`
  (see its header comment), so the defect is upstream of it, in how each
  CTE branch's own row count is estimated. Concrete next step: instrument
  `EstimateRows` on `cs`/`ss`/`ws`'s standalone plan (before the union wraps
  them) for Q33 and find which node in that subtree collapses to 1 — likely
  candidates given this milestone's other findings are a CTE-specific
  estimation gap (see memory `cte_leaves_reach_search_wrapped_in_filter`) or
  a heavy-filter selectivity underestimate the A1/A2/A3 cuts did not reach
  for this predicate shape. Do not guess the mechanism from the outside;
  the practice card's own history (`goopg_swallowed_error_in_rewrite_driver`,
  five wrong hypotheses) is a standing warning to instrument before
  theorising. Evidence: same `ea-findings-20260915.json` as -0004a.
- [ ] **M0142-0004c — re-run `make ea-ratchet` to confirm the C1 findings
  collapse post-fix** — Filed by M0142-0004a. M0142-0004a fixed
  `operators_explain.go`'s EXPLAIN ANALYZE `rows=` (text/JSON/per-worker) to
  divide by `loops` instead of printing the cumulative total
  (`rowsPerLoop`/`round2`, commit TBD), root-causing all 19 of the C1
  `Index Scan using X_pkey est=1` findings from the 2026-09-15 census as a
  measurement bug, not a cardinality-estimator bug — verified on one
  witness (q34 `store_pkey`: 9969/10082=0.99≈est=1) but not yet
  re-captured corpus-wide. This task is pure measurement: re-run
  `make ea-ratchet` (fresh `ea-capture-<date>.txt` +
  `ea-findings-<date>.json`, same harness M0137-0018/M0142-0004 used) and
  confirm (a) all 19 C1-shaped findings drop below the `qerr>=10` flag
  threshold, (b) the 140-finding total shrinks accordingly with no new
  findings appearing (a `rows=` fix should only ever LOWER a qerr that was
  inflated by the bug, never raise one), (c) whether any of the 100x-1000x
  or >=1000x findings were ALSO loops>1 artifacts not yet named — the 2026-09-15
  pass only picked the two cheapest new mechanisms (C1, C2) to file, not an
  exhaustive per-finding `loops=` audit. Evidence base:
  `analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-findings-20260915.json`.
- [ ] **M0142-0005 — break the Memoize / probe-multiplier interlock (B6+B8)** —
  two ledger rows that lock each other. **B6**: goopg's executor has no Memoize
  on the NL probe path R59 repriced, so pricing probes PG-faithfully took
  TPC-DS Q72 from 4 s to a 320 s TIMEOUT (unfixed since R59; carried through
  R60–R62). **B8**: `indexProbeCostMultiplier = 2.0` (`cost_funcs.go:1053`, value at `:1077`)
  deliberately departs from PG because the executor materialises the whole TID
  list eagerly — at `mult = 1` the DP picks PG-shaped NL plans that run 2–3x
  slower (Q7 5.86 s -> 15.72 s). **Neither can move alone**: PG-faithful probe
  pricing without Memoize produces Q72-class timeouts. Resume points:
  `nl_index_join.go:127`, `joinpathsnli.go:313,498`, `cost_funcs.go:1053`.
  Sibling K73 (`Join.FromOuterReduction`) retires with B8. Expect an executor
  slice; per the harness a matching plan that runs slower is not a regression,
  so the Memoize half may be scoped for correctness of the *cost*, not speed.
- [ ] **M0142-0006 — apply `semiJoinMatchFraction` in `estimateNLIndexJoin`** —
  `estimateNLIndexJoin` (`cardinality.go:239-241`) returns `EstimateRows(j.Outer)` for
  SEMI/ANTI, while its sibling `estimateJoin` (`:608-625`) applies the match
  fraction. Textbook `pattern_sibling_paths_must_agree` defect; the ledger says
  mirror lines 621-628 at `:240`. Ledger: `m0137-0013-nli-semi-anti-match-fraction-gap`.
- [ ] **M0142-0007 — re-measure the `corr = 0` index-pricing fallback (B10)** —
  `indexCorrelationFor` (`costindex.go:479-495`) returns 0 when the leading
  column has no correlation slot, pricing **every** such index scan at
  `max_IO_cost` (the fully-random Mackert-Lohman bound); and ANALYZE never visits
  indexes, so `estimateIndexGeometry` **synthesises** relpages/reltuples/
  tree_height rather than measuring them. Both shift index-probe pricing
  corpus-wide and both sit under this milestone's decisions. Re-ANALYZE the bench
  clusters first and establish whether the fallback actually fires at HEAD —
  M0138-0004 populated correlation slots as a side effect. Ledger:
  `m0137-0012-b10-corr-zero-fallback-max-io-cost`.
- [ ] **M0142-0008 — recon: how much of goopg's plan shape is chosen by forced
  rewrites rather than by the search?** (measurement only, no production diff) —
  the goal says plans must be reached by **the same planning logic**, and several
  ledger rows claim goopg still depends on goopg-only forced rewrites that the
  search cannot yet reproduce: `rewriteScanInputsWithSingleTablePredicates`
  (deleting it costs Q20 6.5x), `rewriteJoinsToNLI` (deleting it costs Q4's
  semi-join 12.5x), plus the PG-absent knobs `GOOPG_NLI_COSTGATE`,
  `GOOPG_INDEXKEY_HARVEST`, `GOOPG_INDEX_PROBE_MULT`, `enable_nestloop_index`.
  Ledger: `take2-P6-03`, `take2-P6-04`, `take3-C-20c-blocked`, `-20d-calibrated`, `-20f-blocked`, `-20g-blocked`, `take2-P2-10`,
  `take3-C-09-declined`, `take2-P3-01`.
  **Scope this as a measurement first, because a 2026-09-15 survey overstated
  the case**: it reported that SEMI/ANTI "never enter the DP search", but
  `parser.JoinSemi` is in fact handled at `joinpaths.go:195,265` and
  `joinsearchlevel.go:98,160,228`. M0139-0005's narrower and verified finding is
  that **no SEMI path is ever filed through `addPath`**. Establish which is true
  at HEAD, per query, before proposing any milestone — the harness forbids
  scoping from a dated claim without re-measuring (`git log -S` the mechanism
  first). Deliverable: a per-query census of which plan nodes came from the
  search vs from a forced rewrite, and a verdict on whether a dedicated
  milestone is warranted.

## M0143 — Engine correctness carry-overs from the parity programme (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0143-engine-correctness-carry-overs.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Source:** `METHODOLOGY3/04-forward-plan.md` §3 "Continuous — engine correctness, not gated on anything"
**Prerequisites:** none. Select these whenever everything above is blocked.

Real engine defects the parity programme discovered as a side effect. The case
for treating them as a milestone rather than footnotes: **two genuine wrong-rows
bugs were found by parity work, not by the correctness gates** — the Q13
33-vs-34 Memoize-over-RIGHT-JOIN bug (R63/R64, latent since 2026-09-03, and that
round's own gate should have caught it) and the Limit-below-Unique truncation
(R83, masked on current data only because `78 < 100`).

**Per-task discipline:** each fix lands with a test that fails before it — every
item here exists because nothing in the suite crossed the boundary that would
have caught it. These are not parity tasks: no category movement is expected or
reported, and the values and unit gates are the bar.

- [ ] **M0143-0001 — an in-process test that crosses a DATABASE boundary** —
  `CREATE DATABASE` is a dispatch-layer statement the in-process parser rejects, so
  `pgConstraintTableRel`'s per-DB branch and the reload's `ListDatabases` loop — the
  exact paths TPC-H rides — have manual psql evidence only. This is *why* two per-DB
  defects were found in two consecutive rounds; closing it makes the rest of this
  milestone findable by the suite. Highest leverage of the six.
- [ ] **M0143-0002 — `ALTER TABLE … DROP CONSTRAINT` on an FK reports success and does
  nothing** — `InMemory.DropForeignKeyConstraint` hardcodes `DefaultDBOid`
  (`catalog.go:22241`) and `execAlterTableDropConstraint` discards the result
  (`operators_ddl.go:13303`). `HasPrimaryKey` (`catalog.go:22261`) has the same shape,
  and six `deleteCatalogRowsForOID` sites were filed for the same check and never
  confirmed. R126 made this worse in effect, because such an FK now survives restarts.
- [ ] **M0143-0003 — `pg_constraint` returns 0 rows of any contype after a restart** —
  including the `'p'`/`'u'` rows synthesised from indexes that demonstrably survive. A
  second, independent reload gap that R126 explicitly did not touch.
- [ ] **M0143-0004 — `PhysicalTypeIsVarlena` has no `IsArray` arm**
  (`physical_align.go:85-107`) — latent for ordinary user `int4[]` columns, not just
  catalogs.
- [ ] **M0143-0005 — `ParamRef` LIMIT + DISTINCT returns wrong rows** — R83 fixed the
  `IntegerConst` case and pinned it; the `ParamRef` allowlist was deliberately not
  extended. Fail-closed with zero corpus impact today, which is exactly why it stays
  invisible until someone writes the case.
- [ ] **M0143-0006 — triage `internal/parser`'s 60 failing tests** — pre-existing,
  verified unrelated to R126, and unowned. Fix them or convert them into filed, owned
  tasks; "unowned" is not an end state.

- [ ] **M0143-0007 — separate the dimension-table `relpages` divergence (K41)** —
  `customer` 1,979 pages vs PG's 2,872, `item` 716 vs 1,284. M0140-0005 filed it
  as out of planner reach and that is correct — **but `relpages` is an input to
  every page-priced cost term and to `compute_parallel_worker`'s size ladder, so
  no cost-model change can ever correct it.** The leading candidate is R23's
  `character(N)` blank-padding, a real PG-compat defect that shifts `relpages` on
  every `bpchar` table; R22 (per-page free-space comparison) separates the two.
  Storage work, not planner work, which is why it belongs here. Ledger:
  `m0140-0005-nonplanner-heap-density-floor`.