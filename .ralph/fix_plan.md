# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**. As of 2026-07-28
the banner puts **M0124 → M0125** (closing the TPC-DS round-2 plan, per
`docs/design/tpcds-round2-fixes/README.md` §13.5) at the top of the roadmap,
ahead of M0123 and every other milestone. **M-NIGHTLY no longer preempts it
(amended 2026-07-28): nightly items are still FILED every loop, but they are not
SELECTED until M0124 and M0125 close.** This banner is the sole ordering
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
> 1. **WIP recovery** — one-time; restore & resolve any pre-switch WIP (the
>    "WIP recovery" item directly under this banner) before anything else, never
>    silently drop it. (Nothing outstanding as of 2026-07-28.)
> 2. **M0124** — TPC-DS round-2 closeout. Milestone doc
>    `docs/milestones/0124-tpcds-round2-closeout-measurement-and-gate-debt.md`.
>    **↳ NEXT TASK TO SELECT: `M0124-0001`, chunk `89-96`
>    (`scripts/tpcds-bench-compare.sh 89-96`; chunks 1–88 are DONE). See that
>    task's "Chunked execution" note — the sweep is deliberately split across
>    loops, and the authoritative cursor is the one in `RESULTS.md`.** Do not select a regress/testport case instead: as of the
>    2026-07-28(b) amendment M-NIGHTLY no longer preempts;
> 3. **M0125** — TPC-DS timeout class & planner expression-walker extinction.
>    Milestone doc
>    `docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md`;
> 4. **M-NIGHTLY backlog** — the standing nightly-triage items below. Keep FILING
>    them every loop (see the filing obligation above); work them only after M0124
>    and M0125 close, or under one of the two carve-outs. The TPC-DS **Q75** nightly
>    item is the exception that is already routed: it is in the qualifying set with
>    `Q75,100,pinned` at `ci/batch/tpcds-row-anchors.csv:46` and no
>    `expected-failures.csv` entry, and RC-1b turned it into a deterministic
>    `division by zero` — that item IS **M0125-0004**, so it is worked as part of
>    M0125 and never as a second workstream.
> **Every other roadmap milestone — M0123 included — is parked below M0125 until
> M0124 and M0125 are complete.** M0123 keeps its own branch (`wal-pg-nodetree`)
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

Work order: **M0124 → M0125** (this directive), then **M0123**, then the
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
- [ ] **M0124-0002 — retroactive TPC-H + plan-baseline discharge for `9740fce9`**
      (§13.5 #5). Phases 1.2/1.3 landed while `tpch-spotcheck.sh` reported SKIPPED
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
- [ ] **M0124-0003 — append the seven missing §10 deferral-ledger rows** (§13.5
      #6). §13.2: the seven `tpcds-round2` rows that exist are the rows the WORK
      produced, not the rows §10 planned. Append: parse-time IN-list
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
- [ ] **M0124-0004 — recover or classify Q35's row count** (§13.5 #7). Q35 is the
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
- [ ] **M0124-0005 — add a value checksum to the SF0.5 oracle** (§13.4 item 3).
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

- [ ] **M0125-0004 — Q75 join-residual evaluation order** (§13.5 #3). *First: it
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
- [ ] **M0125-0001 — `internal/planner/exprwalk.go` + exhaustiveness gate** (§13.5
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
- [ ] **M0125-0003 — `GOOPG_RELSIZE_FALLBACK` relation-size fallback** (§13.5 #2,
      phase 6.1). §13.5's highest-value item (15–16 of 21 defects); stage 1 is
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
      #4, phase 2.2). `visitColumnRefsForTable` (`bushy.go:415`),
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
- [ ] **M0125-0005 — flip the `GOOPG_RELSIZE_FALLBACK` default** (§13.5 #2 rider).
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
- [ ] **M0125-0006 — set-operation chains re-associate right when branches are
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
      **Blocked until the M0124-0001 sweep reaches Q99** (no engine commit may land
      before then); it is a parser/planner change, so it additionally requires
      `tpch-spotcheck.sh` + the SF0.5 gate + `make plan-diff` per the pre-commit bar.
- [ ] **M0125-0007 — date input rejects unpadded month/day, and the comparison
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
      **Blocked until the sweep reaches Q99**; executor/codec change, so it
      requires `tpch-spotcheck.sh` + the SF0.5 gate per the pre-commit bar, plus
      the full regress-port suite (Hard-won Rule #5 — this is a codec change).
- [ ] **M0125-0008 — EXISTS + NOT EXISTS on the same outer relation yields a
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
- [ ] **M0125-0011 — FULL OUTER JOIN drops all but the FIRST conjunct of its ON
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
parked: the order is WIP-recovery (#1), M0124 (#2), M0125 (#3), the M-NIGHTLY
backlog (#4), and M0123 (#5). Superseded wording kept below for history: After
WIP-recovery (#1), M-NIGHTLY (#2),
M0124 (#3) and M0125 (#4), M0123 is #5.** It remains the active focus of branch
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
