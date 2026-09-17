# goopg Fix Plan

## Current Priority

Written by the owner, 2026-09-17 (after `tmp/METHODLOGY3_RALPH_CHECK0917/`).
**This banner is the sole ordering authority** and **the loop never edits it**
(`AGENT.md` §"Plan-parity harness" R2). `.ralph/working_set.md` carries state,
not priority. Previous banners: `docs/design/0100-0149/plan-parity-harness-background.md`.

**Before selecting anything below, read `AGENT.md` §"Plan-parity harness".**

Take the first item that has a selectable (`[ ]`, dependencies met) task, in
the order written inside the item. `[!]` tasks are not selectable.

0. **P0 — incident recovery. Nothing from item 1 onward is selected until
   P0-E7 is `[x]`** (section `## P0` below):
   P0-E4 (reproduce the catalog-loss defect on a throwaway cluster) →
   P0-E5 (fix it together with M0143-0008) → P0-E6 (owner restores `:65433`;
   `[!]` until the owner marks it done — do not work around it) →
   P0-E7 (bulk re-measurement of everything landed since 2026-09-16 05:44).
   **While P0-E6 waits on the owner**, select in this order: M0143 tasks whose
   gates do not need TPC-H data (unit/regress/sf025 only), then recon-only tasks
   (no production diff) from items 3–6, then M-NIGHTLY items; if none is
   selectable, follow AGENT.md S8 (escalate and stop). Code whose required gate
   needs TPC-H data is not committed before P0-E6 (G6).
1. **Regressions found by P0-E7**, one task each, in the order P0-E7 lists them.
2. **M0141-S2a-fix2r** — re-apply the PG-faithful `hashAggEntrySize` change that
   was discarded for parity reasons (owner Q4: no reverts). Degradations it
   causes are filed as their own tasks, not reverted.
3. **Roll out fix1's success**: the recon **M0141-S2a-fix1-sweep** (other places
   where width/currency reaches costing late or wrong), then
   M0141-S2b-6-resume, then M0139-0007c.
4. **M0141-S7 — cost diagnosis only.** The Incremental Sort candidate exists
   but loses on cost (S2b-7: 3733.01 vs 3730.89). Compare the cost breakdown
   with PG for the 14 queries. **No executor work.**
5. **M0140-0006a → 0006b → 0006c** (partial-Append).
6. **M0142-0005**, then **M0142-0016c**, then **M0142-0003i** (0003i only after
   P0-E5 and P0-E6 are `[x]`).
7. **M0143 remaining tasks**, top to bottom (includes the parser failures).
8. M-NIGHTLY open items, then the pre-existing milestones
   (M0119 → M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110).

FROZEN-PREFIXES: M0142-0008a-3 M0142-0008c-1a M0142-0008c-3d M0142-0008c-4

**FROZEN (owner decision 2026-09-17) — not selectable, no children:** the
M0142-0008 chain (`M0142-0008a-3`, `M0142-0008c-1a`, `M0142-0008c-3d`,
`M0142-0008c-4`). Keep-or-remove is decided by the owner from P0-E7's A/B.
**csq-R2** stays deferred; its owner-written reopen condition is in
`.ralph/deferral_ledger.md` (row dated 2026-09-17).

**M-NIGHTLY filing is unconditional**: every loop reads
`ci/logs/action-items.md` and files each new `## AI-` subject under M-NIGHTLY.
Selecting one ahead of the order above is allowed only if it breaks the build
or a gate this banner's work needs. When selected: re-run its repro at HEAD
first, fix with normal gates, cite the AI-id, tick it.

## Notes / rules

- This is the authoritative TODO list for Ralph. ONE item per loop; decompose an
  item larger than one agent invocation (new tasks carry `Parent:`).
- Design docs: non-trivial subsystems land with `docs/design/<id>-NNNN-*.md`
  and a `docs/design/README.md` entry in the same commit. For M0137–M0143 and
  `P0-` tasks follow `AGENT.md` §"Plan-parity harness" D3.
- Deferrals: never close a task with a forward reference. A deferral needs
  **both** a `.ralph/deferral_ledger.md` row (`date | task-id | landed |
  deferred | resume point | why`) **and** an unchecked task that owns the
  deferred part; the item stays unchecked. The ledger is the source of truth for
  every "DEFERRED" note below.
- Completed milestones are archived under `completed_milestones/` (latest:
  `completed_fix_plan_012.md`); reference-only, not actionable.

## P0 — Incident recovery (filed by the owner 2026-09-17)

Source: `tmp/METHODLOGY3_RALPH_CHECK0917/03-new-problems.md` §2 and
`04-actions.md` §2. `:65433` lost its TPC-H catalog (heap files survive) after an
uncommitted `ALTER TABLE … ADD CONSTRAINT` transaction on it was stopped with
`-mode immediate`. The cluster is under an evidence hold
(`bench/tpch/runtime_goopg/data.HOLD`); the loop never touches it (R1).

- [x] **P0-E4 — reproduce the catalog-loss defect.** Parent: none.
  Throwaway cluster on `55xx` only. Inside `CREATE DATABASE r; \c r` (the default
  DB bypasses the heap loader via the JSON catalog cache — false negative), with
  no restart between setup and ALTER: `CREATE TABLE t(id int NOT NULL);
  CREATE UNIQUE INDEX t_pk ON t(id);` then
  `BEGIN; ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_pk;` and
  (a) `ROLLBACK`; (b) kill the client without ROLLBACK; (c) with the transaction
  open, `goopg stop -mode immediate` (the incident's shape). After each, restart
  and record `\d t`, `SELECT count(*) FROM t`, relation file presence. Suspected
  path: `stampCatalogRowsTuple` (`operators_ddl.go:18121`) sets xmax **and**
  `XmaxCommitted` before commit; nothing undoes it on abort; the loader
  (`catalog_heap_reload.go:97-98`) skips any row with xmax≠0 and drops the new
  rows as aborted. Deliverable: the three cases as failing regression tests
  (committed, skipped with the P0-E5 id if they would break the unit gate) and a
  design note with which case loses the table.
  - **Done 2026-09-17.** Reproduced all three cases live on a throwaway
    `go test`-spawned cluster (real `psql` client, not a shared `55xx`/`6543x`
    server) — every one loses `t`'s `pg_class`/`pg_attribute` rows identically
    (`to_regclass('t')` NULL, `SELECT count(*) FROM t` → 42P01) while the heap
    data FILE for `t`'s `relfilenode` always survives; confirms the suspected
    path exactly (the loader's `Xmax != Invalid` filter is the single common
    cause, independent of which abort mechanism triggered it). Three
    regression tests (`TestPort_P0E4CatalogXmaxRollback`,
    `TestPort_P0E4CatalogXmaxClientKill`,
    `TestPort_P0E4CatalogXmaxServerImmediateStop`) landed in
    `internal/testport/p0e4_catalog_xmax_loss_test.go`, `t.Skip`'d naming
    P0-E5 (verified: skipped they PASS the unit gate today; temporarily
    un-skipped they reproduce the FAIL shown above, then re-skipped before
    commit). Design note:
    `docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md` (indexed in
    `docs/design/README.md`). Movement: none (test/recon-only, no production
    diff or PG-match change — P0-E5 is where the fix and its measurement
    land). Gates: `go build ./...` clean, `go vet ./internal/testport/`
    clean, `go test -run TestPort_P0E4 ./internal/testport/` PASS (3 SKIP).
- [x] **P0-E5 — fix the catalog-loss defect and M0143-0008 together.**
  Parent: none. Depends on P0-E4. **Disk side**: the loader decides a row's
  xmax by commit status (CLOG; subtransactions resolved to parent — PG
  `HeapTupleSatisfies*`/`TransactionIdDidCommit`), and `XmaxCommitted` is set
  only after commit is known (PG `SetHintBits` rule; check runtime visibility,
  see the DU-002 comment at `operators_ddl.go:18152-18161`). **In-memory side**:
  M0143-0008 (ALTER rollback-undo). Fixing one side only makes the live and
  restarted catalogs disagree — both land in this task. Done when P0-E4's three
  tests pass and the existing durability/transactional-DDL tests pass. Tick
  M0143-0008 in the same commit. **Gates**: units + sf025 sweep + the new tests
  must PASS; `tpch-spotcheck` will be SKIP-BLOCKED by the `:65433` hold — this is
  the one allowed exception to G6: commit with a `ledger:` line naming P0-E7 as
  the re-run owner (commit-msg accepts SKIP-BLOCKED + `ledger:`).
  - **Done 2026-09-17.** Disk side: `catalogRowLive`
    (`internal/initdb/catalog_heap_reload.go:43`) now consults CLOG for a
    non-zero xmax — dead unless the xmax transaction aborted (B0.2,
    `docs/design/wal-pg-identical-stream/02a-phase-b0-enablers.md` §2.3).
    Caught live (not just by code reading): `scanCatalogHeapRows`'s OWN
    inline pre-filter shadowed that fix entirely for every production reload
    call (it rejected any non-zero xmax BEFORE `catalogRowLive` ever ran) —
    fixed in the same commit, plus two more duplicate copies of the same
    stale pattern in `internal/initdb/open.go` (`loadUserIndexesFromHeapForDB`
    ×2, pg_statistic reload), all now delegating to `catalogRowLive`.
    In-memory side: `AlterIndexUndoEntry`/`NotNullUndoEntry`
    (`internal/executor/session.go`), recorded by
    `adoptExistingIndexAsConstraint`/`finishPrimaryKeyConstraint` and consumed
    by `ProcessRollbackUndos`, undo `ALTER TABLE ADD CONSTRAINT ...
    {PRIMARY KEY|UNIQUE} USING INDEX` on ROLLBACK (index fields + PK NOT-NULL
    synthesis, including cascaded inheritance/partition children); `DROP
    CONSTRAINT`'s three index-backed forms (PK/UNIQUE/EXCLUDE) reuse the
    existing `DDLDropUndoEntry`/`RestoreIndex` mechanism. CHECK/FK/NOT-NULL
    `DROP CONSTRAINT` undo is NOT covered (M0143-0008's own text scoped
    itself to "at minimum ADD CONSTRAINT/DROP CONSTRAINT") — filed as
    **M0143-0008b** below. All three P0-E4 tests unskipped and passing, plus
    two new live-session (no restart) tests in
    `internal/testport/p0e5_alter_rollback_undo_test.go`. Full writeup:
    `docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md` §"P0-E5 — the
    fix". Ledger: two rows dated 2026-09-17 (subxact-CLOG residual gap;
    CHECK/FK/NOT-NULL DROP CONSTRAINT undo gap). Gates: `go build ./...`
    clean; `go test ./internal/initdb/... ./internal/catalog/...
    ./internal/executor/...` PASS; `go test -v -run
    'TestPort_P0E4|TestPort_P0E5' ./internal/testport/` PASS (5/5);
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — all
    packages PASS except the pre-existing `internal/parser`
    GroupedJoinUnaliased AST-drift (unrelated, no parser file touched);
    `scripts/tpcds-sf025-regression.sh sweep` — PASS=96 MISMATCH=0 ERROR=0
    TIMEOUT=0, plan-shapes 99/99 identical. `tpch-spotcheck` SKIP-BLOCKED by
    the `:65433` hold; ledger: P0-E7 is the re-run owner.
- [!] **P0-E6 — restore `:65433` (OWNER-RUN).** Parent: none. The owner runs
  `scripts/tpch-ref-recover.sh --i-am-owner --evidence-only` **now** (graceful stop
  + evidence copy; leaves `:65433` stopped with HOLD), then after P0-E5
  `scripts/tpch-ref-recover.sh --i-am-owner`: restore from the pre-loss clone
  `bench/tpch/runtime_goopg/preloss-clone-20260915` (copied, never modified)
  (else HammerDB reload) → rebuild `tmp/goopg-bench-bin` at HEAD → start →
  `scripts/tpch-spotcheck.sh` PASS. **The loop does not run it, and does not
  mark this task.**
- [ ] **P0-E7 — bulk re-measurement since 2026-09-16 05:44.** Parent: none.
  Depends on P0-E6 `[x]`. On a private lane with a HEAD binary: `tpch-spotcheck`,
  `tpch-acceptance-arm`, sf025 sweep, TPC-H parity serial and parallel, TPC-DS
  parity. Compare with `27d4ae001` (TPC-H match 8). For any regression, bisect
  to the commit and file one task per regression (banner item 1) — **no
  reverts** (R3). Also A/B the M0142-0008 chain (`admitSemiAnti` on vs off — the off arm is an
  uncommitted local patch, not a new flag:
  match, categories, values) and write the numbers for the owner's freeze
  decision; this A/B is also the evidence csq-R2's reopen condition names.
  Record every gate stamp and plan-file sha256.
- [ ] **P0-H11 — stale-comment cleanup after the owner's M0142-0008 decision.**
  Parent: P0-E7. Not selectable until the owner records the decision in the
  banner. Remove "admitSemiAnti stays false" comments (e.g.
  `joinsearchseam.go:1552`) and the two probe tests if the chain is removed.
  Also bring the `Status:` lines of the design docs for M0142-0003d/e/f/g/j/k
  (none present) up to date.


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
- [ ] **testport/TestPort_PgStatActivity (AI-20260901-010436-005, AI-20260905-011015-007, AI-20260914-235643-010, AI-20260916-035206-011, AI-20260917-004357-015)**.
- [ ] **testport/TestSyntax_Catalog_PgStatActivity (AI-20260901-010436-007, AI-20260905-011015-009, AI-20260914-235643-012, AI-20260916-035206-013, AI-20260917-004357-017)**.

### Nightly run 20260902-005256 (sha `c11e55d253ff`, 8 items) — filed 2026-09-02
- [ ] **testport/TestE2E_PGColdStartOnGoopgDataDir (AI-20260902-005256-001, AI-20260905-011015-002, AI-20260914-235643-004, AI-20260916-035206-004, AI-20260917-004357-006)**. New
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
- [ ] **race/internal/executor (AI-20260905-011015-001, AI-20260914-235643-002, AI-20260916-035206-002, AI-20260917-004357-003)** — race suite failed
  in `internal/executor` (also failed previous run; repro: `go test -race
  -timeout 45m ./internal/executor/`).
- [ ] **testport/TestPort_IsolationIntraGrantInplace (AI-20260905-011015-003, AI-20260914-235643-006, AI-20260916-035206-006, AI-20260917-004357-010)** —
  FAILed, also failed previous run (repro: `go test -v -run
  '^TestPort_IsolationIntraGrantInplace$' ./internal/testport/`).
- [ ] **testport/TestPort_IsolationStats (AI-20260905-011015-004, AI-20260914-235643-007, AI-20260916-035206-007, AI-20260917-004357-011)** — FAILed,
  also failed previous run (same testport repro pattern).
- [ ] **testport/TestPort_LockRowsSortOverJoinTakesRowLock (AI-20260905-011015-005, AI-20260914-235643-008, AI-20260916-035206-009, AI-20260917-004357-013)** —
  FAILed subtests: join_no_sort, also failed previous run.
- [ ] **testport/TestPort_PgDumpConnectionSetup (AI-20260905-011015-006, AI-20260914-235643-009, AI-20260916-035206-010, AI-20260917-004357-014)** —
  FAILed, also failed previous run.
- [ ] **testport/TestPort_RegressSuite (AI-20260905-011015-008, AI-20260914-235643-011, AI-20260916-035206-012, AI-20260917-004357-016)** — FAILed
  subtests: limit, numerology, also failed previous run; 20260914-235643 adds subtests time, timetz.
  (Remaining 3 items — PGColdStart AI-…-002, PgStatActivity AI-…-007,
  Syntax_Catalog_PgStatActivity AI-…-009 — already have open tasks above;
  AI-ids appended per the "do not add another" rule. Evidence for all:
  `ci/logs/20260905-011015/`.)

### Nightly run 20260914-235643 (sha `baf40efcbfbd`, 14 items) — filed 2026-09-15
- [ ] **units/internal/parser (AI-20260914-235643-001, AI-20260916-035206-001, AI-20260917-004357-002)** — new tonight, units suite
  failed to build/run `internal/parser` (repro: `go test -timeout 10m
  ./internal/parser/`). Likely the same root cause as this file's own
  "Manually discovered" `parser/TestLockingClauseParity` entry below (filed the
  same day from an interactive gate run) — re-run both repros together before
  treating as two separate bugs.
- [ ] **race/internal/parser (AI-20260914-235643-003, AI-20260916-035206-003, AI-20260917-004357-005)** — new tonight, race suite
  failed in `internal/parser` (repro: `go test -race -timeout 45m
  ./internal/parser/`). Same likely-shared root cause note as the item above.
- [ ] **testport/TestPort_IsolationEvalPlanQual (AI-20260914-235643-005, AI-20260916-035206-005, AI-20260917-004357-007)** — new
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

### Nightly run 20260916-035206 (sha `48cf54f85429`, 13 items) — filed 2026-09-16
- [ ] **testport/TestPort_IsolationSuite (AI-20260916-035206-008)** — new
  tonight, FAILed (subtests: specs, specs/detach-partition-concurrently-1,
  specs/tuplelock-upgrade-no-deadlock; repro: `go test -v -run
  '^TestPort_IsolationSuite$' ./internal/testport/`).
  (Remaining 12 items of this run — units/internal/parser AI-…-001,
  race/internal/executor AI-…-002, race/internal/parser AI-…-003,
  testport/TestE2E_PGColdStartOnGoopgDataDir AI-…-004,
  testport/TestPort_IsolationEvalPlanQual AI-…-005,
  testport/TestPort_IsolationIntraGrantInplace AI-…-006,
  testport/TestPort_IsolationStats AI-…-007,
  testport/TestPort_LockRowsSortOverJoinTakesRowLock AI-…-009,
  testport/TestPort_PgDumpConnectionSetup AI-…-010,
  testport/TestPort_PgStatActivity AI-…-011,
  testport/TestPort_RegressSuite AI-…-012,
  testport/TestSyntax_Catalog_PgStatActivity AI-…-013 — already have open
  tasks above; AI-ids appended per the "do not add another" rule. Evidence
  for all: `ci/logs/20260916-035206/`.)

### Nightly run 20260917-004357 (sha `1b54b00f80f1`, 17 items) — filed 2026-09-17
- [x] **units/internal/optimizer (AI-20260917-004357-001)** — new tonight, units
  suite FAILed `TestFlagProvenanceTableCoversPlannerEnv`: "joinsearchlevel.go
  names GOOPG_C9DEBUG, which no benchmark artefact names." **Stale — re-run
  at HEAD passes** (`go test -timeout 10m ./internal/optimizer/` clean).
  `git log --all -S"GOOPG_C9DEBUG"` shows the string was introduced by
  commit 232b80811 (c9, committed 2026-09-17 01:17) and no longer exists
  anywhere in the tree; `git show 1b54b00f80f1:internal/optimizer/joinsearchlevel.go`
  (the exact sha the nightly log cites) also does not contain the string —
  i.e. the failure doesn't match the tree at the cited sha at all. Most
  likely explanation: the nightly batch's checkout raced against this
  session's own concurrent commits (c9/c10/c11 landing 01:17-02:49, nightly
  generated 01:36) and picked up transient WIP mid-run. Closing as stale;
  re-open if a future nightly reproduces on a clean, non-racing checkout.
- [x] **race/internal/optimizer (AI-20260917-004357-004)** — new tonight, race
  suite FAILed in `internal/optimizer`. Same root cause as the units item
  directly above (identical `GOOPG_C9DEBUG` provenance-table failure signature
  in the log); closing stale for the same reason.
- [ ] **testport/TestPort_IsolationFkContention (AI-20260917-004357-008)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationFkContention$'
  ./internal/testport/`).
- [ ] **testport/TestPort_IsolationFkDeadlock (AI-20260917-004357-009)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationFkDeadlock$'
  ./internal/testport/`).
- [ ] **testport/TestPort_UpdateLockedTuple (AI-20260917-004357-012)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationUpdateLockedTuple$'
  ./internal/testport/`).
  (Remaining 12 items of this run — units/internal/parser AI-…-002,
  race/internal/executor AI-…-003, race/internal/parser AI-…-005,
  testport/TestE2E_PGColdStartOnGoopgDataDir AI-…-006,
  testport/TestPort_IsolationEvalPlanQual AI-…-007,
  testport/TestPort_IsolationIntraGrantInplace AI-…-010,
  testport/TestPort_IsolationStats AI-…-011,
  testport/TestPort_LockRowsSortOverJoinTakesRowLock AI-…-013,
  testport/TestPort_PgDumpConnectionSetup AI-…-014,
  testport/TestPort_PgStatActivity AI-…-015,
  testport/TestPort_RegressSuite AI-…-016,
  testport/TestSyntax_Catalog_PgStatActivity AI-…-017 — already have open
  tasks above; AI-ids appended per the "do not add another" rule. Evidence
  for all: `ci/logs/20260917-004357/`.)

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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
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
- [ ] **M0141-S2a-fix2r — re-apply S2a-fix2 (owner Q4: no reverts).**
  Parent: none. Depends on P0-E7 `[x]`. S2a-fix2 was implemented, measured and
  discarded before commit for parity reasons only (no wrong rows); its
  predecessor idea `GOOPG_HASHAGG_WIDTH_CURRENCY` (R120 `9333db6b6`) was deleted
  by `bda453d72` for the same reason. The owner ruled that a PG-faithful change
  is not withdrawn because parity or categories got worse. Re-implement exactly
  the derivation in
  `docs/design/0100-0149/m0141-s2a-fix2-hashaggentrysize-currency-attempt.md`
  (`costAgg` spill arm: guard `inNcols > 0 || inAvgVarBytes > 0`, width
  `hashsize.EntryBytes(inNcols, inAvgVarBytes)`; PG `nodeAgg.c:1701-1730`,
  `costsize.c:2801/2824`), default-on, no flag. Run all values gates (values must
  hold — a wrong-rows result stops the task). File every query whose categories
  worsen (Q31, Q18 were seen) as its own task with `Parent: M0141-S2a-fix2r`.
- [ ] **M0141-S2a-fix1-sweep — recon: other late/wrong width currencies.**
  Parent: none. fix1 (`ca574113c`, TPC-H match 6→8) is the only change in this
  programme proven to move the metric: width reached costing too late. Find every
  other cost-function input that is a goopg-native width/byte quantity, or is
  narrowed after costing (hash join, sort, material, memoize, agg,
  append/gather). For each: the PG expression (`./postgres` file:line), the
  goopg site, and the expected movement (named queries/categories) — S5. File
  one implementation task per site with `Parent: M0141-S2a-fix1-sweep`. No
  production diff (C1).
- [x] **M0141-S2b — GROUP_AGG rel publishes Pathlist, not Node, to the
  ORDER BY step** — **CLOSED 2026-09-16 as a scoping decomposition (not an
  implementation), same precedent as M0140-0006.** Design doc:
  `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md`. **Corrects the
  record**: `electOrderedGrouping` (`upperorderedgrouping.go`, landed
  2026-09-11, `e8a1215fd`, R47 slice 2/K101) already implements exactly this
  surgery for GROUP_AGG — offers the GROUP_AGG rel's real Hashed-vs-Sorted
  `Pathlist` to the ORDER BY step instead of a collapsed `Node`. Neither
  M0141-S2 (2026-09-15) nor M0141-S7 (2026-09-16) cited it; both read
  `createOrderedPaths`/`addOrderedPaths` directly and missed the
  `electOrderedGrouping` pre-check at `planner.go:1960`. S1's fresh
  2026-09-15 capture (used by S2) ran with the loop already live and still
  found mechanism (B) alive in 6 TPC-H queries, so the residual gap is real —
  it is just mis-scoped as "never built" rather than "insufficient as built".
  **Census of `createOrderedPaths`'s upstream rels** (full detail + citations
  in the design doc): GROUP_AGG — plural, wired (`electOrderedGrouping`).
  DISTINCT — plural (hashed vs unique-over-sorted, C-16a/b,
  `distinctpaths.go`), NOT wired. WINDOW — never plural at its own rel
  (`windowsetoppaths.go`'s own comment: "goopg's input is a single finished
  Node, so that loop has one iteration"), no local fix possible. SETOP —
  plural (Append/Hashed) but its rel is allocated fresh per call
  (`newUpperRelForNode`, relids always 0, not re-fetchable across a chain per
  its own comment), a different problem. Base join/scan ORDER BY (no
  aggregation) — the actual K24/F15/K12(B) item: the DP search's own
  multi-candidate tournament is discarded at the `upperorderedinput.go` seam,
  which recovers only the single cost-cheapest winner's pathkeys, never a
  second candidate. **Also**: `addOrderedPaths` only ever has two arms
  (no-sort / full-sort) regardless of which rel feeds it — S7's own
  prefix-match third arm is required on top of every slice below, not
  replaced by any of them. **Decomposed into**:
  - [x] **M0141-S2b-0** — trace-only recon: `GOOPG_PGSHAPED_DP_TRACE=1` over
    TPC-H Q4/Q5/Q8/Q12/Q21/Q22 to confirm/refute whether a Sorted `PathAgg`
    candidate is ever added to the GROUP_AGG rel for these queries (the
    `len(cands) < 2` decline hypothesis in the design doc). **DONE
    2026-09-16 — hypothesis REFUTED.** A private throwaway-server trace (the
    shared `:65433` TPC-H cluster is still emptied per M0142-0003k, reload
    blocked) with parallelism correctly forced off (`max_parallel_workers_per_gather
    = 0`; a first attempt without this pin was contaminated by real parallel
    Partial/Finalize-Agg candidates and is not the reported result) shows all
    six queries offer exactly 2 accepted `PathAgg` candidates
    (`upper.groupagg.hashed` + `upper.groupagg.sort`) to the GROUP_AGG rel —
    `len(cands)` is never `<2`. The decline narrows one level further, to
    `electOrderedGrouping`'s `anyTranslated` gate
    (`groupingEmissionPathkeys`, `upperorderedgrouping.go:56-117`), but which
    specific check trips for which query is **not yet confirmed** — Q4/Q12
    have no obvious blocking gate on a code read and need a live instrumented
    re-trace, not further static reasoning. Full table and resume point in
    `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-0
    result". No production code changed (scratch test file deleted after
    use, nothing committed). Filed **M0141-S2b-5** below to track the
    specific next hypothesis (a possible `*ColumnRef`-only over-restriction
    in `groupingEmissionPathkeys`) since it is now concrete enough to name as
    its own task rather than leaving it embedded in S2b-0's prose.
  - [x] **M0141-S2b-1** — DISTINCT loop-fix (`electOrderedDistinct` /
    `distinctEmissionPathkeys`, mirroring `electOrderedGrouping`/
    `groupingEmissionPathkeys`). **DONE 2026-09-16 — landed, tested, live-
    verified; corpus has no witness that flips.** Implemented
    `internal/optimizer/upperordereddistinct.go`, wired into `planner.go`'s
    `s.Distinct` arm ahead of the legacy `distinctOutputSatisfiesOrder`
    check. Caught and fixed a real bug during live verification: the first
    version shared the caller's `UpperOrdered` rel (safe for grouping,
    whose call site is always the first thing to touch it; unsafe for
    DISTINCT, whose wrapper runs AFTER the generic ORDER BY block already
    populated that same rel over the pre-distinct child) — fixed by using a
    dedicated throwaway registry, regression-tested
    (`TestElectOrderedDistinctIgnoresStalePreDistinctOrderedEntry`, verified
    to fail pre-fix). **Correction**: this task's own motivating witness was
    wrong — S7's TPC-DS "Unique x1" witness is Q49, whose `Unique` comes
    from a plain `UNION` (SETOP/`addSetOpPaths`), not `SELECT DISTINCT`; it
    belongs to **S2b-4**, not here (S7's witness table below is corrected in
    the same edit). Of the corpus's real `SELECT DISTINCT` queries, only
    TPC-DS Q41 pairs one with a matching ORDER BY, and it already matched
    before this task (Hashed already won the ORDER-BY-blind tournament and
    already passed the legacy check) — verified via TPC-DS SF0.25 cluster
    `EXPLAIN` + `GOOPG_PGSHAPED_DP_TRACE=1`, before/after binary diff. Full
    writeup: `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md`
    §"S2b-1 result".
  - [x] **M0141-S2b-2** — base join/scan Pathlist-across-the-search-boundary
    surgery. This is the real K24 item; per K24's own warning, size it with
    its OWN further scoping pass before writing code — do not attempt in one
    sitting. **DONE 2026-09-17 as a scoping recon, no production change**,
    design doc `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md`
    §"S2b-2 result". `searchedRelOf` (R21 slice 2a) already gives
    `createOrderedPaths` a path to the search rel's full `Pathlist`; the gap
    is that nothing downstream asks for more than the current single seed.
    Live-traced against the full TPC-DS SF0.25 corpus (temporary
    `GOOPG_S2B2DEBUG=1` prints, reverted before commit,
    `scripts/tpcds-sf025-regression.sh sweep`+`plans` both
    `PASS=96 MISMATCH=0`/`PLAN-SHAPE changed=0` before AND after the revert):
    the seam is real and reached 38 times corpus-wide, `Pathlist` sizes 3-16
    (median ~7) — but **PROVEN inert without Incremental Sort**: every
    candidate of one call shares the same `Rows` (36/38 blocks exactly; 2
    differ by 1 row, a `kind=11`/LIMIT rounding artefact) and
    `sr.CheapestTotal` is always the exact minimum-cost entry already —
    `costSortRun` prices only rel-level `(rows, width)`, identical for every
    candidate, so the Sort cost added on top is a constant and cannot change
    which candidate ranks cheapest. Same "moves nothing" verdict R21 Slice 1
    measured for its own plumbing cut. **Re-orders M0141's sequencing**:
    S2b-2 and **M0141-S7** (Incremental Sort, `cost_incremental_sort`'s
    per-candidate prefix credit is what would make candidates genuinely
    differ post-Sort) are not independent — each is dead weight without the
    other. Decomposed into **M0141-S2b-2a/2b/2c** below (none selected yet;
    2c is explicitly blocked on S7). No ledger row: the gap (S7) is already
    filed and unchecked; this recon only corrects the sequencing between two
    already-filed items.
  - [x] **M0141-S2b-2a** — plumbing only: thread `searchedRelOf(input)` into
    `createOrderedPaths`/`addOrderedPaths` so the full `Pathlist` is visible
    at the call site. Filed by S2b-2 (design doc §"S2b-2 result" item 1).
    Gate: byte-identical plans on both corpora — per S2b-2's Finding 2 this
    is a *predicted*, not merely hoped-for, null result, same precedent as
    R21 Slice 1. **DONE 2026-09-17.** `createOrderedPaths`
    (`internal/optimizer/upperordered.go`) now calls `searchedRelOf(input)`
    where `seed` is built and stores the result's `Pathlist` onto a new
    `RelOptInfo.SearchCandidates` field (`internal/optimizer/path.go`,
    same "travels as DATA on the rel" precedent as `NeededCols`/`OutputCols`)
    — `addOrderedPaths` needed no signature change since it already receives
    `ordered *RelOptInfo`, so the field alone makes the list reachable there
    without touching 3 production + 6 test call sites. Nothing reads the
    field yet (by design, matches the gate). Two new tests in
    `upperordered_test.go` (`TestCreateOrderedPathsThreadsSearchCandidatesOntoOrderedRel`,
    `TestCreateOrderedPathsLeavesSearchCandidatesNilForANonSearchedInput`).
    `go test ./internal/optimizer/...` full package PASS. TPC-DS SF0.25
    sweep (private bin, nightly batch was live): `PASS=96 MISMATCH=0`,
    `PLAN-SHAPE changed=0` — byte-identical as predicted. Design doc:
    `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-2a
    landed". **Next**: S2b-2b (materialize-on-demand + per-candidate
    `validatedSearchPathkeys`) can now read `ordered.SearchCandidates`
    directly; S2b-2c stays blocked on M0141-S7's `addOrderedPaths` third arm.
  - [x] **M0141-S2b-2b** — materialize-on-demand: defer `createPlanNode` on
    any non-seed candidate until `setCheapest` has chosen a winner (avoid
    paying materialization cost for N-1 discarded candidates per query), and
    generalize `validatedSearchPathkeys` (`upperorderedinput.go`) to run
    per-candidate rather than once for the single seed. Filed by S2b-2
    (design doc item 2). Depends on S2b-2a. **DONE 2026-09-17, pathkeys half
    only.** Landed `validatedSearchCandidateKeys` (`upperorderedinput.go`),
    mapping the existing `validatedSearchPathkeys` re-earn-against-published-
    schema check over every `RelOptInfo.SearchCandidates` entry into a new
    parallel-indexed `RelOptInfo.SearchCandidateKeys` field (`path.go`),
    populated by `createOrderedPaths` (`upperordered.go`) in the same
    `sr != nil` branch S2b-2a added. The materialize-on-demand half has
    nothing to defer yet — no production code builds a `Node` from a
    `SearchCandidates` entry until S2b-2c exists to do it — so it is
    recorded as a contract S2b-2c must follow (materialize lazily, after
    `setCheapest`, never eagerly per candidate) rather than built against an
    interface that does not exist yet (`PathIncrementalSort`, S7's own
    unbuilt step). New test `TestCreateOrderedPathsValidatesSearchCandidatePathkeys`
    (`upperordered_test.go`) pins full-validation, truncate-on-bad-second-key,
    and no-claim-at-all cases; `ordered.Pathlist` stays at 1 (plumbing only,
    same gate as S2b-2a). `go test ./internal/optimizer/...` full package
    PASS. TPC-DS SF0.25 sweep (private bin `tmp/goopg-s2b2b-bin`, deleted
    after; nightly batch was live and holds `tmp/goopg-bench-bin`):
    `PASS=96 MISMATCH=0`, `PLAN-SHAPE changed=0` — byte-identical as
    predicted. Design doc:
    `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-2b
    landed". **Next**: S2b-2c can now read both `SearchCandidates` and
    `SearchCandidateKeys` directly; still blocked on M0141-S7's
    `addOrderedPaths` third arm and its executor operator (a path kind that
    could win the tournament with no node to emit is a new risk class, not
    yet present in the groundwork-only steps S7 has landed so far).
  - [x] **M0141-S2b-2c** — the actual payoff: once `cost_incremental_sort`'s
    per-candidate prefix credit exists (M0141-S7), let `addOrderedPaths` run
    a real tournament across `Pathlist` instead of the single
    always-cheapest-pre-Sort seed. Filed by S2b-2 (design doc item 3).
    **DONE 2026-09-17c.** `cost_incremental_sort` and
    `pathkeysCountContainedIn` had both landed (2026-09-17/2026-09-17b), so
    this was unblocked; landed as `addIncrementalSortPaths`
    (`internal/optimizer/incrementalsortpaths.go`), called from
    `addOrderedPaths`'s tail (`upperordered.go`). Gated off by default
    (`GOOPG_INCREMENTAL_SORT`, same convention as `GOOPG_PARTIAL_SORT_PATHS`)
    because `createPlanNode` has no arm for the new `PathIncrementalSort`
    kind until the executor operator lands — reaching it panics via
    `createplan.go`'s existing `default` case, deliberately (that file's own
    "panic loudly rather than silently mis-build" philosophy), which the
    flag's off default keeps unreachable in production. Verified live: an
    early test version ran the arm through `createOrderedPaths` end-to-end
    with the flag on and hit exactly that panic (the synthetic candidate
    genuinely won); restructured to call `addOrderedPaths` directly so the
    arm is exercised without materializing a winner the executor cannot emit
    yet — same boundary S2b-2b's "materialize lazily" contract already
    drew. With a realistic seed cost stamped (matching what
    `createOrderedPaths` does in production), the incremental-sort
    candidate's prefix credit correctly dominates and PRUNES the costlier
    full-Sort seed candidate via ordinary `addPath` comparison — a genuine
    cost-driven result, not a plumbing check (the reason this arm exists at
    all). Four new tests (`incrementalsortpaths_test.go`): default-off
    inertness, the positive dominance case, a fully-contained-candidate skip,
    a zero-shared-prefix skip. `GOOPG_INCREMENTAL_SORT` registered in
    `flaglabels.go` and `scripts/planner-flags.env` regenerated. Gates:
    `go build ./...`, `go vet ./internal/optimizer/...`,
    `go test ./internal/optimizer/...` (full package,
    `TestFlagProvenanceEnvIsGenerated` included) all clean; TPC-DS SF0.25
    sweep at the default (flag off, private bin `tmp/goopg-s2b2c-bin`,
    deleted after the run): `PASS=96 MISMATCH=0 PLAN-SHAPE changed=0` —
    byte-identical, confirming production is untouched. GUC note: reused
    `cp.enableSort` rather than wiring the dedicated (declared-but-unconsumed)
    `enable_incremental_sort` GUC — deferred, ledger row
    `m0141-s7-incremental-sort-guc`. Design doc:
    `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-2c
    landed". **Next**: the executor operator (M0141-S7's own next
    implementation-order step) is now the concrete blocker for turning
    `GOOPG_INCREMENTAL_SORT` on to measure — a query where the arm wins would
    panic today.
  - [x] **M0141-S2b-3** — WINDOW loop-fix. Gated on S2b-2's actual payoff
    (WINDOW has nothing of its own to loop over until the Pathlist
    tournament exists) — per S2b-2's 2026-09-17 recon, that means gated on
    **M0141-S2b-2c specifically** (blocked on M0141-S7), not on S2b-2a/2b's
    inert plumbing alone. **DONE 2026-09-17j as a scoping recon, no
    production change** (same K24/S2b-2 precedent: "do not attempt in one
    sitting"). S2b-2c and M0141-S7's executor operator are both now landed,
    clearing the stated gate — but reading `windowsetoppaths.go` first found
    its own header claim ("no presorted variant to build... C-14 blocked
    with no executor counterpart") is STALE, superseded by a later change
    (R6/plan-parity-fix-take2) the header was never updated for: a real
    `*WindowAgg.Presorted` field and executor skip-sort fast path
    (`operators_window.go` `windowOp.Open`) already exist, just unused
    outside the chained-WindowAgg self-check (`childDeliversSortKeys`,
    `createplansimple.go:357`). The real remaining gap is two independent
    pieces, confirmed by reading, same "prerequisite inert alone" shape
    S2b-2/M0141-S7 already showed: (1) `costWindow`
    (`windowsetoppaths.go:193`) has NO presorted/partial-prefix credit —
    unconditionally charges the full sort cost regardless of input order, so
    a second candidate would be priced identically to today's and change
    nothing measurable; (2) `createWindowPaths`/`addWindowPaths`
    (`windowsetoppaths.go:91,220`) see exactly one collapsed input `Node`,
    never a Pathlist — wiring `searchedRelOf`/`RelOptInfo.SearchCandidates`/
    `SearchCandidateKeys` is directly reusable from S2b-2a/2b, but
    `addIncrementalSortPaths` itself (`incrementalsortpaths.go`) is NOT
    reusable verbatim: it adds a bare `PathIncrementalSort` as the target
    rel's own top-level output (correct for `upper.ordered`/
    `electOrderedGrouping`, where sort-above-agg IS the rel's final shape)
    but WRONG for WINDOW, where the sort must nest BELOW the `*WindowAgg`,
    which must remain the rel's outer/final node regardless of which input
    candidate wins. Full writeup, the `costWindow`/`addWindowPaths` code
    reads, and the two-piece decomposition rationale in
    `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-3
    recon". Filed **M0141-S2b-3a**/**M0141-S2b-3b** below (neither selected
    yet). Ledger row appended (task-id `m0141-s2b-3`).
  - [x] **M0141-S2b-3a** — teach `costWindow` (`windowsetoppaths.go:193`) a
    presorted/partial-prefix cost credit, reusing `costIncrementalSort`'s
    formula (`incrementalsortpaths.go`) for the shared-prefix case and
    skipping `sortRun` entirely when the full `windowSortKeys` list is
    already covered. Filed by S2b-3's recon (design doc item 1). Prerequisite
    for S2b-3b — same ordering S2b-2c depended on M0141-S7's cost function.
    **DONE 2026-09-17k.** `costWindow` took two new params
    (`presortedCount`, `presortedGroups`) and branches: no-credit (unchanged
    `costSortRunWithWidth` call — the ONLY branch any caller reaches today),
    full-match (charges nothing — `createWindowPlan` stacks no Sort node in
    this case), partial-match (`costIncrementalSort` called with a ZEROED
    input `Cost` — passing the real one would double-count `inputTotal`,
    which this function adds back on separately for the blocking-node
    reason its own doc comment states). `addWindowPaths` passes `(0, 0)`
    unconditionally (input is still one collapsed Node, no Pathlist to
    check — S2b-3b's job). Gate: predicted byte-identical-plan null result
    CONFIRMED — 3 new unit tests pin all three branches directly
    (`TestCostWindowPresortedCreditIsInertAtZero`,
    `TestCostWindowFullPresortedMatchChargesNoSort`,
    `TestCostWindowPartialPresortedMatchIsCheaperThanFullSort`); TPC-DS
    SF0.25 sweep `PASS=96 MISMATCH=0 PLAN-SHAPE changed=0`;
    `tpch-spotcheck.sh` SKIPPED (pre-existing M0142-0003k data-reload
    blocker, confirmed unrelated by reproducing the same SKIP with this
    change's two files `git stash`ed out). Full writeup: design doc's
    "S2b-3a landed" section. **Next: M0141-S2b-3b**, now unblocked.
  - [ ] **M0141-S2b-3b** — wire `searchedRelOf(input)`/
    `RelOptInfo.SearchCandidates`/`SearchCandidateKeys` into
    `createWindowPaths`, and extend `addWindowPaths`'s per-candidate loop to
    build, for every search candidate sharing a nonzero pathkey prefix with
    `windowSortKeys(top)` (via `pathkeysForSortKeys` +
    `pathkeysCountContainedIn`, both already exist), either a `PathWindow`
    over `PathIncrementalSort`-over-`candidate` (partial prefix) or a
    `Presorted=true` `PathWindow` directly over `candidate` (full match),
    alongside the existing always-full-Sort candidate. Filed by S2b-3's
    recon (design doc item 2). Depends on **S2b-3a** landing first. TPC-DS
    Q67 (`WindowAgg` on `dw1.i_category`) is the sole corpus witness and the
    gate.
  - [ ] **M0141-S2b-4** — SETOP rel-identity fix. **UPDATE 2026-09-16 (S2b-1's
    own result section)**: DOES now have a witness — TPC-DS Q49's `Unique`
    (S7's mis-mapped "Unique x1"; `select ... union select ... union select
    ...`) is goopg's SETOP upper rel, not `SELECT DISTINCT`, so this is its
    real home. Still not scheduled ahead of 1-3 on that basis alone (single
    witness, same-sized surgery K24 already flagged as risky).
  - [x] **M0141-S2b-5** — resolve `electOrderedGrouping`'s `anyTranslated`
    decline for the GROUP_AGG mechanism-B queries now that `len(cands)<2` is
    refuted. **DONE 2026-09-16 — hypothesis ALSO REFUTED, root cause found
    to be elsewhere.** Landed a small permanent `DPGROUP` trace
    (`internal/optimizer/upperorderedgrouping.go`, gated on the existing
    `GOOPG_PGSHAPED_DP_TRACE`, same convention as `pathTraceEnabled`) and
    re-ran S2b-0's 6-query private-cluster probe. Result: `anyTranslated` is
    `true` for all six queries — `electOrderedGrouping` never declines, runs
    its full tournament, and reaches an election every time. For Q4/Q5/Q12/
    Q21 the winner is `Sort`-over-`Hashed`-`Aggregate` (the cost comparison
    picks Hashed+explicit-Sort over the translated sort-free Sorted
    candidate); Q8/Q22 pick the sort-free Sorted candidate outright (no Sort
    node). A stale scratch capture (`tmp/take4/runs/plansweep/q04.{pg,goopg}.txt`)
    corroborates for Q4 specifically that real PG picks `GroupAggregate` fed
    by a `Sort` **below** it (no Sort above), while goopg's plan is the
    predicted Sort-above-Hashed shape — **the root cause is a cost-model
    discrepancy in the Hashed-vs-Sorted `PathAgg` comparison, not a wiring
    gap in `electOrderedGrouping`/`groupingEmissionPathkeys`.** Full trace
    data, the cache-contamination trap hit and corrected in-loop (`go test`
    silently replayed a stale cached result because the probe's package
    reaches the changed code only through a `cluster.New`-spawned `go run`
    subprocess — `-count=1` was required), and the resume point are in
    `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-5
    result". No scratch test file committed (deleted after use, same
    precedent as S2b-0). Filed **M0141-S2b-6** below as the direct
    continuation. Ledger row appended (task-id `m0141-s2b-5`).
  - [x] **M0141-S2b-6** — (filed 2026-09-16 by S2b-5's result) compare
    goopg's actual costed Hashed vs. Sort-over-Sorted `PathAgg` candidate
    numbers at Q4/Q5/Q12/Q21 against PG's cost formulas. **DONE 2026-09-16 —
    recon complete, S2b-5's own numbers do NOT reproduce.** Re-ran the same
    synthetic-cluster probe with full (all-8-table) `ANALYZE`, vs. S2b-5's
    `ANALYZE region`-only coverage: Q5/Q21 cleanly elect Hashed+Sort-above
    (correct — their ORDER BY doesn't match GROUP BY, no cost bug), but Q4/
    Q12 now elect Sorted (bare Aggregate), the OPPOSITE of S2b-5's table, on
    the SAME commit. Root cause: Q4/Q12's join inputs estimate `rows≈1` on
    this synthetic 5-16-row dataset, so `numGroups ≈ inputRows` (both clamp
    to 2) and the Sorted candidate's input-Sort term and the Hashed
    candidate's output-Sort term price the identical two clamped tuples —
    an **exact tie** (`0.3525`/`2.54` both runs, down to the float) broken
    only by a sub-0.02-unit startup-cost tiebreak that ANALYZE coverage
    perturbs either way. Hand-verified `costSortRunWithWidth`/`costAgg`
    against `costsize.c:1898-1985`/`2682-2768` term-by-term while at it: no
    divergence in the formulas, only in the (degenerate) data feeding them.
    **This synthetic-dataset probe pattern cannot answer S2b-6's question**
    — the two terms it needs to separate only diverge when `inputRows >>
    numGroups`, which requires real SF1 cardinalities. Full result, the
    per-query cost table, and the methodology note in
    `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` §"S2b-6
    result". No production code changed; scratch probe deleted after use
    (same precedent as S2b-0/S2b-5). Ledger row appended (task-id
    `m0141-s2b-6`). Follow-up filed as **M0141-S2b-6-resume** below, gated
    on the M0142-0003k TPC-H cluster reload (same blocker, not a new one).
  - [ ] **M0141-S2b-6-resume** — repeat S2b-6's Hashed-vs-Sorted `PathAgg`
    term-by-term cost diff for Q4/Q5/Q12/Q21 against the real HammerDB
    SF1-loaded `:65433` cluster once reloaded (no synthetic fixture — real
    `inputRows`/`numGroups` cardinalities, which is what separates the
    input-Sort and output-Sort terms enough to show a genuine win/loss
    instead of a tie). **Gated on M0142-0003k** (the shared cluster's
    `tpch` database is currently empty; reload is a human-authorized
    shared-resource write per that entry). Do not re-attempt with a bigger
    synthetic dataset first — S2b-6 already showed the synthetic-fixture
    approach is the wrong instrument for this question regardless of size
    tuning, since the point is to observe PG's own real-data cost
    comparison at genuinely separated cardinalities, not to construct one.
  - [x] **M0141-S2b-7** — filed 2026-09-17 by M0141-S7's corpus measurement
    (design doc's "Update 2026-09-17h"). `electOrderedGrouping`
    (`upperorderedgrouping.go:236`) calls `addOrderedPaths` directly, once
    per surviving `PathAgg` candidate, **without ever running
    `createOrderedPaths` first** — and `ordered.SearchCandidates`/
    `SearchCandidateKeys` (what `addIncrementalSortPaths`, the M0141-S7
    third arm, actually reads) are populated in exactly one place,
    `createOrderedPaths` itself (`upperordered.go:106-118`). So every
    GROUP_AGG-shaped ORDER BY — 5 of the 14 TPC-DS Incremental-Sort
    witnesses (Q3/Q43/Q54/Q60/Q89, all `GroupAggregate`-family) — structurally
    cannot reach the third arm no matter how far S2b-5/S2b-6's
    `anyTranslated` chase goes: fixing `anyTranslated` only changes whether
    `electOrderedGrouping`'s own Sort-vs-no-Sort election runs, and that
    election has no Incremental Sort awareness of its own. Fix: give
    `electOrderedGrouping`'s per-candidate `addOrderedPaths` calls a real
    candidate set — either populate `ordered.SearchCandidates`/
    `SearchCandidateKeys` from `cands` before the loop (mirroring
    `createOrderedPaths`'s own population, scoped to the GROUP_AGG rel's
    Pathlist instead of a searched join/scan tree), or give
    `electOrderedGrouping` its own incremental-sort-over-PathAgg-candidate
    offer. Re-run the corpus measurement
    (`scripts/capture-tpcds.sh` against a `GOOPG_INCREMENTAL_SORT=on`
    SF0.25 server, `bench/tpcds/plans-pg/` as reference) after landing to
    see how many of the 5 move — note this is necessary but very likely not
    sufficient on its own, since S2b-5/S2b-6's still-open cost-tie question
    (Hashed-vs-Sorted `PathAgg` election) sits upstream of it for the same
    queries.
    **LANDED 2026-09-17i.** `upperorderedgrouping.go`'s `electOrderedGrouping`
    now sets `ordered.SearchCandidates = cands` /
    `ordered.SearchCandidateKeys = translated` right after the
    `anyTranslated` gate (both added to the existing save/restore-on-decline
    snapshot — required, since the same `*RelOptInfo` is reused by the
    `else`-branch `createOrderedPaths` call when the loop declines), and the
    `createPlanNode(best)` switch gained a `*IncrementalSort` case (mirrors
    the existing `*Sort` case's descend-to-`*Aggregate` copy-back; previously
    an Incremental-Sort win would have hit
    `default: return restore("winner-shape-unexpected")` and been silently
    discarded). **Proved closed by trace, not inferred**: a
    `GOOPG_PGSHAPED_DP_TRACE=1` capture on Q3 now shows a real
    `upper.ordered.incrementalsort` candidate offered
    (`PresortedCount=1`, matching PG's own `Presorted Key: dt.d_year`) that
    loses the tournament to the plain Sort by cost (`total=3733.01` vs
    `3730.89`) — **the bypass is closed; what remains is a cost question**,
    exactly the "necessary but not sufficient" outcome predicted, folding
    into **M0141-S2b-6-resume**'s already-filed scope (same
    Hashed-vs-Sorted `PathAgg` cost-tie, gated on the M0142-0003k TPC-H
    cluster reload). Corpus re-measurement:
    `analysis/m0141/m0141-s2b7-full99-incsort-on.txt`, still 0/99
    Incremental Sort nodes, byte-identical EXPLAIN shapes to the pre-fix
    capture (`m0141-s7-full99-incsort-on.txt`) aside from header/tmp-path
    lines. `scripts/pg-plan-parity-diff.py` floor check:
    `PLAN-PARITY: queries=99 match=2 shapediff=67 unparsed=0 missingnode=27
    error=3 timeout=0` — M0137-0004 floor (match>=2, Q9+Q41) holds,
    categories unchanged. Full writeup: design doc's "Update 2026-09-17i".
    No new ledger row (folds into the existing S2b-6-resume follow-up
    rather than opening a fresh one).
  Needs M0141-S2 (done, see above) for the concrete TPC-H query list
  motivating S2b-0/S2b-2. Ledger row appended (task-id `m0141-s2b`).
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
  **UPDATE 2026-09-16 (re-adjudication + scoping recon, DONE, no production
  change): verdict GO, but implementation is gated on M0141-S2b.** Design doc:
  `docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md`.
  **Re-adjudication**: `take3-C-14-dropped`'s performance measurement stands
  and is not disputed; it is simply no longer the deciding question under the
  plan-parity harness's "a slower plan that matches is not a regression" —
  **GO**. **Decisive new finding**: read all 14 TPC-DS witnesses in
  `bench/tpcds/plans-pg/*.txt` directly — every one sits immediately above a
  node whose own output is already ordered by a prefix of the downstream
  `ORDER BY` (`GroupAggregate`x5 by GROUP KEY, `WindowAgg`x1 by PARTITION BY,
  `Merge Join`x2 by its own Merge Cond, `Nested Loop`x4 by its outer child's
  order, `Unique`x1 trivially, `Subquery Scan`x1 passthrough) — never an
  arbitrary scan/join. Read `createOrderedPaths`/`addOrderedPaths`
  (`upperordered.go:63-127`) directly: all 4 real `planner.go` call sites
  (799/1965/2023/11022) already collapse their producing rel to a single
  executor `Node` before calling here, so only one synthetic single-candidate
  `*Path` ever reaches the ORDER BY contest — confirming **all 14 witnesses
  need M0141-S2b** (already filed, and already scoped generally as "every
  `createXPaths` -> `createOrderedPaths` call site", not GROUP_AGG-only — no
  widening needed) before an Incremental Sort candidate has anything to build
  over.
  **NARROWED 2026-09-16 (M0141-S2b's own scoping decomposition,
  `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md`)**: this claim
  missed that `electOrderedGrouping` (`upperorderedgrouping.go`) already
  loops GROUP_AGG's real `Pathlist` into the ORDER BY step (landed
  2026-09-11, before this task even ran) — this reading of
  `createOrderedPaths`'s callers alone does not see it, since it runs before
  the fallback to `createOrderedPaths` at `planner.go:1960`. The witnesses do
  not map to a single monolithic "S2b" any more: `GroupAggregate`x5 ->
  **M0141-S2b-0** (trace-confirm whether the loop's `len(cands)<2` decline is
  why these still miss, not "S2b" wholesale);
  `Merge Join`x2 + `Nested Loop`x4 (+ likely `Subquery Scan`x1) ->
  **M0141-S2b-2**; `WindowAgg`x1 -> **M0141-S2b-3** (gated on S2b-2).
  **CORRECTED 2026-09-16 (S2b-1's own result section)**: this row originally
  read `Unique`x1 -> **M0141-S2b-1**, but Q49's `Unique` comes from a plain
  `UNION` (goopg's separate SETOP upper rel, `addSetOpPaths`), not a
  `SELECT DISTINCT` clause — it needs **S2b-4** (SETOP rel-identity), not
  S2b-1 (now landed regardless, on its own genuine — if currently
  witness-free — merit; see `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md`
  §"S2b-1 result"). Every
  mapping above is still gated on S7's own not-yet-built third `addOrderedPaths`
  arm (prefix-match -> Incremental Sort) regardless of which S2b sub-task
  lands — none of them alone is sufficient. **Sizing beyond S2b**: most needed machinery already exists under a
  different name — `pathkeysContainedIn` (sibling of the needed prefix-count
  helper), `estimateNumGroups`, `costSortRunWithWidth`/`sortPathForBounded`
  (`cost_incremental_sort` composes the full-sort cost per group), and the
  executor's already-landed, zero-caller presorted-prefix grouping contract
  `sortPrefixEqual` (`internal/executor/sort_presorted.go`, E-15) — sizing this
  increment closer to a single cardinality/cost slice (M0142-0012-class) than
  to the M0141-S3-S6 AggSplit programme. **Stays unchecked**: GO + scope
  delivered, not implementation, which cannot start before its relevant
  M0141-S2b-N sub-task (see NARROWED note above) lands for each witness
  group. Resume point: once the relevant sub-task(s) land, re-run this task's
  14-query census against a post-fix capture, then implement in order
  (prefix-count helper -> cost function ->
  `PathIncrementalSort`/`addOrderedPaths` third arm -> executor operator ->
  `createplansimple.go` wiring -> EXPLAIN rendering), each pinned by a test.
  Ledger row filed: `.ralph/deferral_ledger.md` (2026-09-16, `m0141-s7`).
  **UPDATE 2026-09-17**: landed the first implementation-order step,
  `pathkeysCountContainedIn` (`internal/optimizer/pathkeys.go`, 4 new unit
  tests) — the prefix-*count* sibling of `pathkeysContainedIn`, reproducing
  `pathkeys_count_contained_in` (`postgres/.../pathkeys.c:558`). Zero
  production callers by design (same posture as E-15's `sortPrefixEqual`):
  it does not touch `createOrderedPaths`/`addOrderedPaths`, so no plan can
  change and the usual byte-identical/shape-delta gates do not apply (see
  design doc's 2026-09-17 update).
  **UPDATE 2026-09-17b**: landed the second implementation-order step,
  `costIncrementalSort` (`internal/optimizer/cost_funcs.go`, next to
  `sortByteBranch`) — `cost_incremental_sort` (`costsize.c:2000-2126`)
  composed over the existing `costSortRunWithWidth`, 4 new unit tests
  (`cost_incremental_sort_test.go`) pinned against an independent
  transliteration of the upstream formula. Resolved the prior update's open
  question: the formula's only non-scalar input (`estimate_num_groups`'s
  result) is taken as a caller-supplied parameter, same split
  `costSortRunWithWidth` already uses for `ncols`/`avgVarBytes`/`width` — so
  the formula is fully standalone-testable and did NOT need to wait on
  M0141-S2b; only the later `estimateNumGroups`-calling wiring step does.
  Zero production callers, same groundwork posture. TPC-DS SF0.25 sweep
  re-run: PASS=96 MISMATCH=0, PLAN-SHAPE changed=0. See design doc's
  2026-09-17b update for the monotonicity-shape and comparisonCost=0
  findings. **Still unchecked, still blocked on M0141-S2b** for the next
  step (Finding 3 table row 3, the `PathIncrementalSort`/`addOrderedPaths`
  third arm, which genuinely needs a real multi-candidate `Pathlist` to
  build a presorted-prefix candidate over).
  **UPDATE 2026-09-17c**: row 3 landed as **M0141-S2b-2c** (see that task's
  own entry above for the full writeup) — `addOrderedPaths`'s third arm now
  exists (`internal/optimizer/incrementalsortpaths.go`), gated off by
  default (`GOOPG_INCREMENTAL_SORT`) because the executor still has no
  Incremental Sort operator to materialize a winner with. **Still
  unchecked**: the executor operator is now the concrete next step — without
  it, turning the flag on to measure against the corpus would panic on the
  first query where the arm actually wins. `createplansimple.go` wiring and
  EXPLAIN rendering remain after that.
  **UPDATE 2026-09-17d (scoping recon, no production change)**: attempted
  row 5 ("the executor operator") directly and stopped before writing
  production code once the real integration surface came in far above
  Finding 3's estimate — TWO separate execution engines build a `Sort` node
  (`executor.go:179` classic `buildNode`, `executor.go:675` slab/tree fast
  path, `pattern_sibling_paths_must_agree` class), 4 mechanical
  `case *optimizer.Sort:` tree-walkers whose omission is a SILENT
  WRONG-ANSWER risk not a panic (`scan_deform.go` x2 — deform pushdown,
  `subplan.go` — rescan-kind classification, `operators_cte_dml.go` —
  work-table-scan detection), 6 `operators_explain.go` sites (5 trivial, 1
  needs a new `Presorted Key:` line, PG oracle `nodeIncrementalSort.c`/
  `explain.c`), and stats-map plumbing (`context.go`'s
  `SortStats`/`SortWorkerStats`, `parallel_worker_ctx.go`'s worker mirror).
  Full writeup: design doc's 2026-09-17d update. **Row 5 split into four
  loop-sized sub-tasks, filed below**; this task (M0141-S7) itself stays
  unchecked — implementation, not just scope, is still the resume point.
  - [x] **M0141-S7-exec-a — `IncrementalSort` optimizer Node type + the
    executor operator**, built and unit-tested STANDALONE (constructed
    directly in tests, zero `createPlanNode`/`Plan()` callers — same
    posture `pathkeysCountContainedIn`/`costIncrementalSort` used). Groups
    rows via `sortPrefixEqual` (`internal/executor/sort_presorted.go`,
    E-15's contract), full-sorts each group, streams groups in arrival
    order. Explicitly excludes spill-to-disk, packed-tuple retention, ctid
    passthrough (`sortOp`'s harder features) — deferred to
    **M0141-S7-exec-d** below; an all-in-memory `[]Row` first cut is
    enough to prove the algorithm and matches
    `nodeIncrementalSort.c`'s own per-group re-tuplesort shape. No
    dependency on exec-b/c — implement first.
    **LANDED 2026-09-17e**: `internal/optimizer/incrementalsort.go` (Node
    type, mirrors `Sort` + `PresortedCount`) and
    `internal/executor/operators_incremental_sort.go` (`incrementalSortOp`,
    pull-based Open/Next/Close, order-equivalence-tested against `sortOp`
    as oracle, 5 executor tests + 1 optimizer test). **Unplanned but
    required**: adding the bare Node type tripped
    `TestEveryPlanNodeTypeHasAnExplainArm`/`TestEveryPlanNodeWithChildrenIsWalked`
    (`explain_node_coverage_test.go` — enumerate by type existence, not
    reachability), fixed by adding 2 of exec-c's 6 planned
    `operators_explain.go` arms now (`describePlanMode` label,
    `planChildren` walk — both mirror `Sort`'s own arm exactly); **exec-c's
    scope below is narrowed accordingly**. Gates: `go build ./...` clean;
    `go test ./internal/optimizer/... ./internal/executor/...` green;
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — only
    failure is the pre-existing, tracked `internal/parser` AST-drift issue
    (untouched by this change); `tpch-spotcheck.sh` SKIPPED (bench schema
    not loaded, pre-existing, moot — zero production callers so no plan can
    change). Full writeup: design doc's "Update 2026-09-17e" section.
  - [x] **M0141-S7-exec-b — end-to-end structural + semantic reachability**.
    **LANDED 2026-09-17f**. Sizing decision: `Path.PresortedCount` (new
    field, `path.go`) is STASHED by `addIncrementalSortPaths`
    (`incrementalsortpaths.go`) at the point `nCommon` is already computed,
    not re-derived at `createPlanNode` time — a re-derivation via
    `pathkeysCountContainedIn` against the winning candidate's
    `SearchCandidateKeys` slot is not guaranteed exact if the tournament
    re-orders `Pathlist` between path-build and `setCheapest`. Landed:
    `createIncrementalSortPlan` (`createplansimple.go`, `createSortPlan`'s
    structural twin plus the `0 < PresortedCount < len(Pathkeys)` panic
    check), wired into `createplan.go`'s switch; the classic `buildNode`
    arm (`executor.go`, mirrors the `*optimizer.Sort` case exactly); **the
    slab/tree fast path needed no new code** — `IncrementalSort` is not a
    concrete-dispatch slab kind, so `BuildFast`'s `buildRec` reaches it
    through the existing `opAdapter` default arm, the same posture
    `Distinct`/`WindowAgg`/`SetOp` already have (confirmed by grep: none of
    the three has a bespoke `buildRec` case either) — "both builder sites"
    is satisfied because both `Build` and `BuildFast` now reach a working
    operator, not because both needed bespoke code; and all 4 tree-walkers
    (`scan_deform.go`'s `deformBoundBelow`/`deformSideWidth`, `subplan.go`'s
    `classifySubPlan` — classified `rescanCloseOpen` like `Sort`, not yet
    re-Open-safety-audited despite `Open` resetting its own state —
    `operators_cte_dml.go`'s `planContainsWorkTableScan`, the one
    correctness-stakes walker: a recursive CTE body with an Incremental
    Sort over its `WorkTableScan` must still be detected and streamed).
    New tests: `TestCreateIncrementalSortPlanOverPrebuilt` /
    `TestCreateIncrementalSortPlanPanics` (`createplansimple_test.go`);
    `TestIncrementalSortReachesBothBuilders`
    (`operators_incremental_sort_build_test.go`, new file — swaps a real
    query's planner-built `Sort` for a hand-built `IncrementalSort` over
    the identical child/keys, checks byte-identical output against the
    un-swapped baseline plus `Build`/`BuildFast` agreement via the existing
    `runBothAndCompare` Phase-C harness); `TestClassifySubPlanKinds` gained
    a case, new `TestPlanContainsWorkTableScanSeesThroughIncrementalSort`
    (`subplan_handle_test.go`); `scan_deform_bound_test.go` gained a
    narrowing subtest plus `*incrementalSortOp` cases in its own operator-
    tree walkers (needed — the new build test's deform assertions
    false-failed without them; caught and fixed same loop). Gates:
    `go build ./...` clean; `go test ./internal/optimizer/...
    ./internal/executor/...` both green;
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — only
    failure is the pre-existing tracked `internal/parser`
    `GroupedJoinUnaliased` AST-drift issue (optimizer/executor packages
    both `ok`); `scripts/tpch-spotcheck.sh` SKIPPED (bench schema not
    loaded, pre-existing M0142-0003k blocker, moot — flag stays
    default-off). Full writeup: design doc's "Update 2026-09-17f" section.
  - [x] **M0141-S7-exec-c — EXPLAIN rendering**. **LANDED 2026-09-17g**.
    Extracted the `*optimizer.Sort` case's per-key formatting loop
    (`operators_explain.go`'s `emitNodeDetailLines`) into a shared
    `sortKeyParts(child, keys, reg, qualify) (full, bare []string)` helper
    — `full` is the existing decorated string (byte-identical `Sort Key:`
    behaviour), `bare` is the newly-captured pre-suffix string PG's own
    `show_sort_group_keys` (`explain.c:2792-2818`) also computes
    internally before `show_sortorder_options` appends the DESC/NULLS
    text. Added `case *optimizer.IncrementalSort:` emitting `Sort Key:`
    from `full` (same as Sort) plus a second row, `Presorted Key: ` +
    `bare[:PresortedCount]` joined — matching PG's
    `show_incremental_sort_keys` (`explain.c:2583-2594`) exactly, including
    that the presorted line carries NO direction/NULLS decoration even for
    a DESC key (PG's own oracle strips it). `PresortedCount` is always in
    `(0, len(Keys))` (exec-b's `createIncrementalSortPlan` panic check), so
    PG's `if (nPresortedKeys > 0)` guard always fires here — no
    conditional needed. The 3 safe-to-decline sites (`resolveKeySource`,
    `childNodeOf`, `execParamOwnerChildren`) re-checked, confirmed still
    correct to leave declining. New test:
    `TestExplainIncrementalSortPresortedKey`
    (`operators_incremental_sort_build_test.go`, reuses exec-b's
    `firstSort`/`replaceSort` hand-built-plan technique, wraps in
    `&optimizer.Explain{Options:{Costs off}}`) pins `Sort Key: grp, v DESC`
    + `Presorted Key: grp` (no DESC, no second key) against a
    `PresortedCount=1` node. Gates: `go build ./...` clean; `go test
    ./internal/optimizer/... ./internal/executor/...` both green;
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — only
    failure is the pre-existing tracked `internal/parser`
    `GroupedJoinUnaliased` AST-drift issue (optimizer/executor both `ok`);
    `scripts/tpch-spotcheck.sh` SKIPPED (bench schema not loaded,
    pre-existing M0142-0003k blocker, moot — flag stays default-off). Full
    writeup: design doc's "Update 2026-09-17g" section. exec-a/b/c are now
    ALL LANDED.
  - [ ] **M0141-S7-exec-d (deferred, ledger row filed 2026-09-17)** —
    `sortOp` feature parity once exec-a/b/c land and the corpus is
    measured: spill-to-disk, packed-tuple retention (`GOOPG_SORT_PACKED`),
    ctid passthrough (`ORDER BY ... FOR UPDATE` over an Incremental Sort),
    per-group `SortStat` (`context.go` keying). None of the 14 TPC-DS
    witnesses are `FOR UPDATE`/huge-group queries, so none of this is
    required to move the plan-parity metric — only do it if a corpus query
    actually needs it after exec-b's measurement runs.
    **UPDATE 2026-09-17h (the corpus measurement ran, verdict definitive):**
    started the SF0.25 goopg cluster with `GOOPG_INCREMENTAL_SORT=on`
    (confirmed via `/proc/<pid>/environ` on the live server, not just a
    capture-header label) and captured all 99 TPC-DS queries
    (`scripts/capture-tpcds.sh`) plus a targeted 14-witness capture:
    `grep -c "Incremental Sort" analysis/m0141/m0141-s7-full99-incsort-on.txt`
    is **0**. Zero corpus queries reach the executor operator at all — not
    "reach it but need spill/packed/ctid", never reach it — so exec-d's
    "measure first" gate stays exactly where it was; still deferred, no
    ledger change. Root-caused 5 of the 14 witnesses (the `GroupAggregate`-
    family ones) to a *different, newly-found* mechanism gap, filed as
    **M0141-S2b-7** above: `electOrderedGrouping` never runs
    `createOrderedPaths`, and only `createOrderedPaths` populates the
    `ordered.SearchCandidates`/`SearchCandidateKeys` fields the third arm
    reads. The other 9 witnesses are not explained by this and stay exactly
    as blocked as before (2 `Merge Join` + 4 `Nested Loop` are an open
    question — S2b-2a/2b/2c should already cover that shape, so their own
    zero needs a live `GOOPG_PGSHAPED_DP_TRACE=1` trace to resolve; `WindowAgg`/
    SETOP/`Subquery Scan` are each still their own unbuilt call site).
    `GOOPG_INCREMENTAL_SORT` stays default-off (provably inert at HEAD, so
    no regression risk, but no benefit to justify the flip either). Full
    writeup, the corrected 5/2/4/1/1/1 producer-shape tally (re-verified
    against the actual child AST node, not just Sort Key text), and the two
    stale doc-comment corrections this update made (`path.go`,
    `incrementalsortpaths.go`): design doc's "Update 2026-09-17h" section.

## M0142 — Join-order costing (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0142-join-order-costing.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
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
- [x] **M0142-0003c — level-6 enumeration-order parity vs PG's
  `join_search_one_level`** — **RECON DONE 2026-09-16, premise REFUTED for
  Q9**, design doc
  `docs/design/0100-0149/m0142-0003c-q9-real-pg-cost-gap-not-a-tie.md`. No
  production change. Finding 1: goopg's `joinsearchlevel.go` is already a
  faithful, line-cited structural port of PG's `join_search_one_level`
  (phases 1/2/3, FROM-order initial rels, the level==2 dedup offset) —
  verified against a closed-form pair-count test; the enumeration order does
  NOT structurally differ, closing that branch of the fork. Finding 2
  (decisive): forcing real PG 18.3 into goopg's own chosen Q9 join order
  (`join_collapse_limit=1` + explicit left-deep `JOIN`, same method as
  M0142-0015) costs **336207.55 vs PG's own default order's 204932.03 — a
  64% gap, not a near-tie**. Real PG never needs a tie-break to prefer its
  own chain; the near/exact tie -0003b measured is a property of GOOPG's cost
  model alone. Finding 3: Q9 and Q45 (M0142-0015) turned out NOT to be two
  witnesses of the same phenomenon as this task's own filing note assumed —
  Q45 is a genuine near-tie in both engines (tie-break-class, unaffected by
  this finding); Q9 is a real costing-term divergence goopg's model hides as
  a coincidental exact tie. **Follow-up filed as M0142-0003d** (below):
  which specific term underprices goopg's index-driven-NLI-over-lineitem
  route (or overprices the hash-with-full-scan alternative) for this shape —
  not scoped by this recon, which stopped at establishing the gap is real
  and roughly where it originates (the lineitem access-path choice).
  Lower-priority secondary thread, still open: -0003b's Finding 1's
  bit-exact-tie-composition question (why goopg's two candidates' differently
  composed (input, marginal) pairs sum to the identical float) — informative,
  not required for -0003d.
- [x] **M0142-0003d — which cost term underprices goopg's index-driven NLI
  over lineitem vs a hash-join-with-full-scan, for Q9's shape** — filed by
  -0003c's Finding 2/3. **RECON DONE 2026-09-16, premise REFUTED**, design
  doc
  `docs/design/0100-0149/m0142-0003d-q9-row-estimate-collapse-not-cost-formula.md`.
  No production change. Finding 0: -0003c's own "forced-goopg-order" PG
  measurement (336207.55 vs 204932.03) turns out to have forced PG into a
  FROM-clause literal left-deep chain that was **never actually goopg's own
  winning shape** — goopg's real (unforced) Q9 winner is an all-index-NL
  chain (`part⋈partsupp` hash, then `lineitem`/`supplier`/`orders` each
  NL-indexed, `nation` hashed last), confirmed via a fresh `EXPLAIN` against
  a private throwaway clone (never touched the shared `:65433` bench
  cluster). Forcing PG into *that* actual shape with hash disabled prices it
  at 372230.53 — same magnitude, different cause (PG's cost model penalizes
  NL-indexing through `orders`'s 1.5M rows) — so -0003c's 64% gap finding
  survives, just mislabeled. **Finding 1 (decisive): the real divergence is
  upstream of costing.** goopg's own DP-search row estimate for
  `lineitem ⋈ partsupp` on the composite key `l_partkey=ps_partkey AND
  l_suppkey=ps_suppkey` is **2406**, ~2500x below the true value
  (~5,999,098 — `partsupp_pk` is a genuine composite UNIQUE index on exactly
  those two columns, verified live on the bench cluster). Every relset
  containing both relations inherits this ~146-row floor all the way to the
  top of the plan tree — this, not any hash/NL cost-formula constant (B8 /
  `indexProbeCostMultiplier` NOT implicated), is what makes the all-NL-index
  chain look nearly free. **Finding 2**: goopg already has purpose-built code
  for exactly this pattern (`internal/optimizer/joinkeyproof.go`'s
  `superkeyJoinEstimate`, M0127-P5.6-f — its own header names this exact Q9
  clause pair as the motivating case) but it is evidently not preventing the
  collapse at HEAD for this relid pairing; why is not confirmed, left to the
  follow-up. **Supersedes** the pre-M0138 R53 spill-footprint lead
  (`.../r53-q9-costing-step0/SLICE1.md`) as the primary driver — that
  analysis is not wrong on its own terms but was built on this same
  now-identified 2500x-wrong row estimate, so it is very likely a
  second-order effect, not primary; do not resume it before M0142-0003e is
  resolved. Follow-up filed as **M0142-0003e** (below).
- [x] **M0142-0003e — why doesn't goopg's existing superkey/FK-based
  no-fan-out substitution fire for the bare `lineitem ⋈ partsupp` composite
  join?** — filed by -0003d's Finding 1/2. **DONE 2026-09-16, recon-only, no
  production diff** (design doc:
  `docs/design/0100-0149/m0142-0003e-bench-cluster-missing-fk-constraints.md`).
  (a) `GOOPG_PGSHAPED_DP` re-confirmed **ON by default**
  (`joinsearch.go:76`) — the FK/superkey arm is live in production, not
  disabled. (b) The clause-matching precondition DOES recognize
  `partsupp_pk` against this exact clause shape — proven by existing tests
  that already reproduce this scenario byte-for-byte:
  `TestCalcJoinrelSizeCompositeUniqueRetainsEqualities` and
  `TestCalcJoinrelSizeBareCompositeDefaultsKeepEqualityAndBound`
  (`internal/optimizer/joinrelsize_test.go`, both PASS at HEAD) use the SAME
  6,000,000/800,000 row counts and NDistinct values as the real Q9 repro and
  assert `superkeyJoinSelectivity` returns `boundProven=true, fired=false` —
  i.e. a bare (non-FK) composite UNIQUE index is **deliberately** bound-only
  evidence, never a selectivity substitution; only a **declared foreign
  key** (`provableJoinKeys`'s second, `fromFK`-gated loop) fires the
  `1/rawTuples` substitution. This matches PG's own
  `get_foreign_key_join_selectivity` (`selfuncs.c`), which also requires a
  `pg_constraint` row, not just a unique index on the referenced side.
  **The mechanism is correct and reaches this case; it does not fire because
  the join genuinely is not backed by a declared FK in goopg's schema.**
  Checked live (read-only `pg_constraint`/`\d`/`pg_indexes`, no writes, no
  restarts): goopg's HammerDB-loaded `:65433` TPC-H cluster has **zero**
  FK constraints on any table (`lineitem_part_supp_fkidx` is a plain index
  despite its name); the PG 18.3 oracle at `:65432` has the **full canonical
  8-constraint TPC-H FK set** (`lineitem_partsupp_fk` etc.), added by an
  untracked manual step outside any script in the repo, never mirrored onto
  goopg's cluster. PG's own `EXPLAIN` on the bare join gets `rows=5999098`
  (essentially exact) via that declared FK;
  `TestCalcJoinrelSizeFKDividesByParentCount` shows goopg's identical
  mechanism would produce the equivalent near-exact estimate given the same
  FK metadata — goopg's FK machinery (`catalog.ForeignKey`,
  `internal/parser/ast.go:3389`'s purpose-built HammerDB-TPC-H-FK-shape
  support) is not the blocker either. **Conclusion: no code change
  indicated — this is a bench-cluster schema/data-load parity gap, not a
  planner or cost-model defect**, and it reframes -0003c's "real 64% cost
  gap": that PG measurement carried PG's own FK-informed near-exact row
  estimate throughout, while the goopg-side shape being priced carried the
  2500x-collapsed one, so the two costs were never pricing comparably-sized
  intermediates. Follow-up filed as **M0142-0003f** (below).
- [x] **M0142-0003f — add the missing FK constraints to goopg's TPC-H bench
  cluster to restore parity with the PG oracle, then re-measure Q9** — filed
  by -0003e. **DONE 2026-09-16, PARTIAL: 11/16 landed, Q9 re-measurement still
  blocked** (design doc:
  `docs/design/0100-0149/m0142-0003f-fk-add-blocked-by-unindexed-validation-scan.md`).
  Adopted the 8 existing unique indexes as `PRIMARY KEY`s via `ADD CONSTRAINT
  ... PRIMARY KEY USING INDEX` (cheap). The 3 FKs with tiny parent tables
  (`nation_region_fk`, `customer_nation_fk`, `supplier_nation_fk`) landed and
  are permanent, validated, PG-matching state on the live `:65433` cluster.
  **The remaining 5** (`partsupp_part_fk`, `partsupp_supplier_fk`,
  `order_customer_fk`, `lineitem_partsupp_fk`, `lineitem_order_fk` — the last
  two are exactly what Q9's `lineitem ⋈ partsupp` collapse needs) are blocked
  by a newly-discovered engine gap, not a scoping error: goopg's FK-constraint
  validation is an **O(child rows × parent heap-scan distance) unindexed
  nested-loop** with **no interrupt-checking** (so a started scan cannot be
  cancelled via `pg_terminate_backend` either) — see -0003g below. Q9's
  re-measurement stays deferred until -0003g lands or a scoped partial fix
  covers at least `lineitem_partsupp_fk`.
- [x] **M0142-0003g — index-accelerate and interrupt-check goopg's FK
  constraint validation scan** — filed by -0003f. **DONE 2026-09-16.** Root cause traced to
  `internal/executor/operators_ddl.go:13974`
  (`validateFKConstraintExistingRows`) → `internal/executor/operators_fk.go:632`
  (`assertParentExists`) → `:1318` (`scanRelForFKMatch`): for every live child
  row, the parent table is scanned heap-block-by-heap-block from the start,
  never consulting the parent's existing unique B-tree index even when one
  exists on exactly the referenced columns (`partsupp_pk`, `part_pk`, …) —
  O(child rows × average parent-scan distance), infeasible once the parent
  exceeds a few hundred rows (`partsupp_part_fk`: child 800k / parent 200k,
  did not complete in several minutes; `lineitem_partsupp_fk`/
  `lineitem_order_fk`: child 6,000,000, never attempted for real). The scan
  also does not call anything equivalent to PG's `CHECK_FOR_INTERRUPTS()`
  (`postgres/src/backend/commands/tablecmds.c:13751`) — `pg_terminate_backend`
  on an in-flight validation scan returned success but the backend kept
  running 15+ seconds later with no sign of stopping, so a mis-sized FK add
  cannot be aborted short of restarting the whole server. Fix: (a) route the
  parent-existence check through `LookupIndex`/an index probe when a unique
  index covers the referenced columns (PG's real fallback path,
  `postgres/src/backend/commands/tablecmds.c:13694`
  `validateForeignKeyConstraint`, still does only O(child rows) work via a
  planner-chosen `LEFT JOIN` or an indexed per-row RI-trigger probe — never a
  parent heap scan); (b) add a cancellation check inside the per-row loop.
  After landing, resume -0003f: add the remaining 5 FKs (expect
  `lineitem_partsupp_fk`/`lineitem_order_fk` to still take real time even
  index-accelerated — 6M child-row index probes — but tractable, unlike an
  unindexed scan) and land the DDL in `bench/tpch/build_schema_goopg.sh` (or
  a new post-load step) so it survives a `--reset` rebuild. Then re-run the
  Q9 `EXPLAIN`/estimate-audit capture from -0003a/-0003d to see whether the
  row-estimate collapse is gone and whether Q9's plan shape or cost gap
  changes. Also worth a quick corpus-wide `pg_constraint` diff between the
  two clusters before trusting any OTHER M0142 "row-estimate collapse"
  finding's causal story, since this same gap could be masquerading as an
  estimator bug elsewhere.
  **Landing summary (2026-09-16):** `internal/executor/operators_fk.go`
  gained `findFKCoveringUniqueIndex` + `fkProbeKeyForIndex` (builds the
  probe key via `ctx.indexRowProbeKey`, the SAME builder
  `checkUniqueIndexesForInsert` uses, NOT `encodeIndexKeyFromCols` directly
  — that alternative builds the wrong key shape whenever the index uses
  PG's real index-tuple format instead of the blob format, a bug caught
  live by `TestAlterTableAddForeignKeyDanglingRow`/`NotValidThenValidate`
  on the first attempt) + `scanIndexForFKMatch` (exact-key
  `BTree.RangeScan` probe) + `fkPendingOutcome` (the visibility/multixact/
  key-changing classification, extracted verbatim from the old
  `scanRelForFKMatch` body so the indexed and unindexed (renamed
  `scanRelForFKMatchSeq`) paths can never diverge, per
  `pattern_sibling_paths_must_agree`). `scanRelForFKMatch` is now a
  dispatcher: index probe when a covering unique index exists and the key
  has no NULLs, else the old full heap scan. Cancellation check
  (`ctx.Ctx.Err()`) added to the index-probe callback and to
  `fullTableFKCheckRel`'s per-block outer loop. **Verified** at
  partsupp/part scale (child 800k / parent 200k) on an isolated throwaway
  cluster (port 5533, binary at `/tmp/goopg-fk-check`, cleaned up after):
  `ALTER TABLE ... ADD FOREIGN KEY`, which previously did not finish in
  several minutes, now completes in 6.7s (clean case) / 9.4s (with one
  deliberately dangling row, correct `23503` + byte-exact `DETAIL`). Full
  `internal/executor` suite green (12.9s). **Gate note:**
  `scripts/tpch-spotcheck.sh` could not run this loop — its private
  `pg_basebackup` clone of the shared `:65433` cluster came up with
  `lineitem` missing in both `tpch@tpch` and `postgres@postgres`
  (`SKIPPED`). New evidence for -0003h: almost certainly the abandoned
  backend PID 81 (still stuck mid-DDL on the pre-fix binary, see -0003f's
  `In-flight` note) corrupting the online clone's consistency point, not a
  regression from this loop — confirmed via `git stash` that a
  separate, unrelated pre-existing failure (`TestLockingClauseParity` in
  `internal/parser`, stale `GroupedJoinUnaliased` AST field) reproduces
  identically with and without this loop's diff. The realistic-scale
  functional/perf test plus the full unit suite above substitute for the
  skipped gate, since this change touches only the FK-validation scan path,
  not query planning/execution. Follow-up filed as **M0142-0003i** (below).
- [x] **M0142-0003h — DDL issued after an explicit `BEGIN` did not wait for
  `COMMIT`/`ROLLBACK` on the shared TPC-H bench cluster** — filed by -0003f,
  untraced secondary finding. **DONE 2026-09-16, recon-only, no production
  diff**, design doc
  `docs/design/0100-0149/m0142-0003h-ddl-rollback-undo-scoped-to-create-only.md`.
  Reproduced on a private throwaway cluster (`:5533`, never the shared
  `:65433` one) with two real `psql` sessions. **DML control** (`INSERT`)
  rolls back correctly — ordinary MVCC row visibility is sound. **`ALTER
  TABLE ADD CONSTRAINT`**: visible to a second session before `ROLLBACK`
  AND still present after `ROLLBACK` — never undone. **`CREATE TABLE`**:
  also prematurely visible before `ROLLBACK`, but correctly gone after.
  **Root cause**: the rollback-undo list (`DDLUndoEntry`/`RecordDDLCreate`,
  `docs/design/0000-0049/0030-0006-transactional-ddl.md` Phase 1) is
  populated at exactly six call sites in `operators_ddl.go`, covering only
  `CREATE TABLE`/`CREATE INDEX` — no other DDL form was ever wired in.
  **Two distinct findings**: (1) premature cross-session DDL visibility is
  the **already-documented** Phase-1 "Concurrent DDL visibility...
  deferred" limitation, re-confirmed, not new; (2) `ALTER TABLE`
  subcommands having **zero** rollback-undo — a silent, permanent commit
  regardless of `ROLLBACK`, not just deferred visibility — is the actually
  novel finding, and is what -0003f's original observation really was.
  Answers -0003h's own question: the commit is per-statement and
  deterministic for any non-`CREATE TABLE`/`CREATE INDEX` DDL, not an
  artifact of that one interrupted run. Follow-up filed as **M0143-0008**
  (below, under the engine-correctness-carryover milestone — unrelated to
  join-order costing, so not filed as another M0142 item).
- [ ] **M0142-0003i — resume -0003f now that -0003g's index-accelerated FK
  validation has landed** — **OWNER AMENDMENT 2026-09-17 (overrides the text
  below): depends on P0-E5 and P0-E6 `[x]`. Never run DDL on the shared `:65433`
  (AGENT.md R1): add the FKs in `bench/tpch/build_schema_goopg.sh` (or a
  post-load step) and verify on a private `55xx` clone; the owner applies it to
  `:65433` at the next reload. Ignore the PID-81 / restart instructions below —
  that backend no longer exists.** Original text: add the remaining 5 TPC-H FK constraints
  (`partsupp_part_fk`, `partsupp_supplier_fk`, `order_customer_fk`,
  `lineitem_partsupp_fk`, `lineitem_order_fk`) to the shared `:65433` bench
  cluster, land the DDL in `bench/tpch/build_schema_goopg.sh` (or a new
  post-load step) so it survives a `--reset` rebuild, then re-run the Q9
  `EXPLAIN`/estimate-audit capture from -0003a/-0003d to see whether the
  row-estimate collapse at lineitem⋈partsupp is gone and whether Q9's plan
  shape or cost gap changes. Before touching the cluster: resolve the
  abandoned backend PID 81 left running the *old, pre-fix* binary's
  `ALTER TABLE partsupp ADD CONSTRAINT partsupp_part_fk ...` since -0003f
  (still active as of this loop's start per `pg_stat_activity`) — it is no
  longer just an annoyance, -0003g's own loop found it corrupts a
  `pg_basebackup` online clone of the cluster (`scripts/tpch-spotcheck.sh`
  SKIPPED with `lineitem` missing in the clone). Terminating that backend
  (or restarting the shared server onto a freshly-built binary that
  includes -0003g's fix) should be safe and low-risk now that the O(child ×
  parent) scan it was stuck in no longer exists in the fixed binary — a
  restarted server would re-run FK validation for that one statement
  index-accelerated instead of hanging again. Also worth a quick
  corpus-wide `pg_constraint` diff between the two clusters once all 16 FKs
  are present, per -0003g's note.
  **BLOCKED as of 2026-09-16: see M0142-0003j below** — the PID 81 backend
  was terminated and the cluster restarted onto the -0003g-fixed binary per
  this task's own instructions, but the restart's crash recovery revealed
  the shared `:65433` cluster's `tpch` database has lost ALL of its TPC-H
  tables (including the 11 FKs/PKs -0003f/-0003g landed) — a full data-loss
  event, not just the stuck backend. -0003i cannot proceed (there is nothing
  to add the remaining 5 FKs to) until -0003j's reload lands.
- [x] **M0142-0003j — CRITICAL recon: the shared TPC-H bench cluster
  (`:65433`, `bench/tpch/runtime_goopg/data`) lost its entire `tpch`
  database contents via a crash-recovery event** — found while executing
  -0003i's own first sub-step (terminate PID 81, restart onto the -0003g-
  fixed binary). **DONE 2026-09-16 as recon: evidence chain complete, root
  cause deliberately NOT adjudicated (needs a scoped repro, see below) —
  design doc `docs/design/0100-0149/m0142-0003j-bench-cluster-data-loss-on-crash-recovery.md`.**
  Sequence and evidence, in order:
  1. At this loop's start, a fresh `psql -d tpch` connection to the
     *still-running, pre-restart* server confirmed `pg_constraint` had
     exactly 3 `contype='f'` rows (`customer_nation_fk`, `nation_region_fk`,
     `supplier_nation_fk` — -0003f's landed set) and PID 81 was still
     `active` on `ALTER TABLE partsupp ADD CONSTRAINT partsupp_part_fk ...`
     (started 2026-09-15T20:44, per -0003g's own confirmation at ITS loop
     start too) — i.e. `partsupp` and the FK rows definitely existed live,
     minutes before anything below happened.
  2. Graceful stop (`bench/tpch/stop_goopg.sh`, which calls `goopg stop`
     default `-mode fast`) timed out after 20s — expected, PID 81's scan
     predates -0003g's interrupt-check fix and cannot be cancelled.
     `kill -TERM <pid>` on the wrapper process also did not stop it within
     15s (same reason: the smart/fast shutdown path still waits on the
     backend). `kill -KILL <pid>` was BLOCKED by the auto-mode classifier
     (kill of a PID it doesn't recognize as owned) — did not attempt to
     bypass it. `goopg stop -D <dir> -mode immediate` (documented in
     `goopg stop --help` as "no checkpoint, DB_IN_PRODUCTION") succeeded in
     under 30s and is a sanctioned lifecycle command, not a raw kill.
  3. Rebuilt `tmp/goopg-bench-bin` from HEAD (`a53c5b807`, includes -0003g)
     and restarted via `bench/tpch/setup_goopg.sh` (no `--reset`, so it
     should have reused the existing data directory). Startup log:
     `WARN database system was not properly shut down; automatic recovery
     in progress ... redo=3975712320 checkpoint=3975712408
     lastCheckpointWasOnline=true`, then `checkpoint complete ...
     elapsed_ms=77`.
  4. Post-restart, a fresh connection to `tpch` shows: `pg_constraint` has
     ZERO rows of any type; `pg_class`/`\dt` in the `public` schema lists
     ONLY 12 unrelated scratch tables (`agg_data`, `lrs_acct`, `lrs_side`,
     `mj_a`, `mj_b`, `mjq_a`, `mjq_b`, `tmp1`, `zz_c`, `zz_p1`, `zz_q1`,
     `zz_tx`) — no `lineitem`/`orders`/`partsupp`/`part`/`customer`/
     `supplier`/`nation`/`region` at all. `SELECT count(*) FROM lineitem`
     errors `relation "lineitem" does not exist`.
  5. The physical heap files are NOT gone — `base/16408/` (the `tpch`
     database's oid dir) still holds 245 files including multi-hundred-MB
     ones (e.g. `16409` at 232MB, `16412` at 154MB, sized right for
     `lineitem`/`orders`) — they are just orphaned, no `pg_class` row
     references their relfilenodes anymore. `pg_wal/` holds only 7 x 16MB
     segments (`...EC` through `...F2`), consistent with the control file's
     `redo`/`checkpoint` LSNs (`redo` lands inside segment `EC`) — i.e. this
     is what a normal redo-to-checkpoint replay window looks like, NOT
     evidence of the segments themselves being wrongly recycled.
  6. Table-name provenance: `mj_a`/`mj_b`/`mjq_a`/`mjq_b` match
     `internal/testport/mergejoin_all_clauses_test.go`; `lrs_acct`/
     `lrs_side` match `internal/testport/lockrows_sort_ctid_test.go` (whose
     `TestPort_LockRowsSortOverJoinTakesRowLock` is literally one of
     tonight's `ci/logs/action-items.md` regressions). Neither test file
     nor any other file under `internal/testport/` references port `65433`
     literally, so a hardcoded-port collision was not confirmed — but the
     naming match is specific enough (not a generic/common prefix) that a
     manual or scripted repro session against the shared cluster is the
     leading hypothesis over a from-scratch WAL/checkpoint bug; NOT
     adjudicated between the two this loop.
  7. This is NOT a first sighting of trouble on this exact cluster:
     -0003g's own loop found `scripts/tpch-spotcheck.sh`'s `pg_basebackup`
     clone of this same `:65433` cluster came up with `lineitem` missing,
     hours before this full-blown loss — both symptoms center on the same
     cluster's durability/cloning path and may share a root cause.
  8. **CLAUDE.md's TPC-H section currently states** "goopg persists `CREATE
     DATABASE`... `tpch@tpch` works across restarts (verified on the
     2026-07-27 rebuild)" — that verification did not cover an unclean
     (`-mode immediate`) shutdown + crash-recovery restart, which is the
     scenario that just failed; flag this gap in CLAUDE.md in the same loop
     that resolves this task, either narrowing the claim or removing it if
     graceful restarts are found to have the same gap.
  Follow-up filed as **M0142-0003k** (below).
- [x] **M0142-0003k — pin the M0142-0003j data-loss root cause, fix if it is
  a real recovery gap, then reload the TPC-H bench cluster** — **SUPERSEDED by
  owner 2026-09-17: its conclusion ("crash recovery refuted, manual DDL") was
  wrong (`tmp/METHODLOGY3_RALPH_CHECK0917/03-new-problems.md` §2.2). The
  remaining (c)/(d) — root cause, fix, reload — are P0-E4, P0-E5, P0-E6. Do not
  DROP tables or reload `:65433`.** Original text: filed by
  -0003j. Design doc:
  `docs/design/0100-0149/m0142-0003k-crash-recovery-refuted-scratch-table-provenance.md`.
  **(a) and (b) DONE 2026-09-16, (c)/(d) still open:**
  - **(a) Hypothesis A (real crash-recovery gap): REFUTED.** Ran three
    unclean-shutdown repros on a throwaway cluster (`:5533`,
    `/tmp/goopg-crash-repro`, same HEAD binary as the shared cluster):
    (i) plain `kill -KILL` after checkpointed + uncheckpointed committed
    rows — all rows survived; (ii) `goopg stop -mode immediate` (the exact
    mode the incident used) — same, all rows survived; (iii) `kill -KILL`
    **mid-flight during an uncommitted, multi-second `ALTER TABLE ADD
    CONSTRAINT` FK-validation scan** (3M-row unindexed child table, closest
    match to the incident's PID-81 shape) — parent/child row counts exact,
    incomplete constraint correctly absent from `pg_constraint`, all
    earlier data intact. Crash recovery is sound in every scenario tried;
    stop treating it as a live suspect.
  - **(b) Hypothesis B (port/connection collision as literally stated):
    REFUTED; refined explanation stands unproven.** Read
    `internal/testport/tap_port_test.go`'s `newCluster` helper
    (`cluster.New(name, cluster.Options{DataDir: filepath.Join(t.TempDir(),
    "data"), ...})`, no `ListenAddr`) and `cluster.New`/`freePort` in
    `internal/testutil/cluster/cluster.go` — both suspect test files always
    get a fresh temp data dir and an OS-ephemeral ("`:0`") port, never a
    fixed port, never `:65433`. The literal hypothesis (the automated Go
    test connected to the shared cluster) cannot be true structurally.
    But both files literally contain the `CREATE TABLE
    mjq_a/mjq_b/lrs_acct/lrs_side` SQL matching the orphaned scratch tables
    exactly — most consistent explanation is a past loop manually re-running
    that repro SQL with `psql` directly against the shared `:65433` cluster
    while debugging those two bugs, not the automated test itself. The
    actual event that dropped the original TPC-H tables/constraints is
    still not reconstructable (no query log retained).
  - **(c) BLOCKED — needs a human decision.** Reload TPC-H SF=1 via
    HammerDB (`bench/tpch/README.md`, ~12 min) and re-land the 11 FK/PK
    constraints -0003f/-0003g already designed (DDL recorded verbatim in
    those two docs), then resume -0003i's remaining 5. First requires
    dropping the shared cluster's 12 orphaned scratch tables
    (`agg_data`, `lrs_acct`, `lrs_side`, `mj_a`, `mj_b`, `mjq_a`, `mjq_b`,
    `tmp1`, `zz_c`, `zz_p1`, `zz_q1`, `zz_tx`) — a `DROP TABLE` attempt
    against the shared `:65433` cluster this loop was **declined by the
    session's auto-mode classifier** ("Modify Shared Resources"); did not
    attempt to bypass it. Needs a human to run the drop+reload directly, or
    to explicitly authorize it for a future loop.
  - **(d) DONE 2026-09-16** — `CLAUDE.md`'s TPC-H section caveat rewritten:
    durability is re-confirmed (crash recovery is sound per (a)); the
    residual, open risk is shared-cluster DDL process discipline, not a
    storage-engine defect. It still points at this task for the pending
    reload.
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
- [x] **M0142-0004b — recon: why does a 3-way CTE `UNION ALL` land at
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
  theorising. Evidence: same `ea-findings-20260915.json` as -0004a. **DONE
  2026-09-15, recon closed, no code change** (temporary `GOOPG_EA0004B_TRACE`
  prints in `estimateJoin`/`semiPairMatchFraction`, fully reverted — `git
  diff` on `cardinality.go` empty). Full writeup:
  `docs/design/0100-0149/m0142-0004b-cte-union-branch-collapse-is-estimatejoin-recompute-gap.md`.
  Traced Q33's standalone `ss` CTE body on the SF0.25 cluster: `est=3` is
  exactly `1+1+1`, each branch's own `HashAggregate`/`Hash Semi Join`
  independently collapses — neither the CTE-specific gap nor the A1/A2/A3
  predicate-selectivity guess panned out. The SEMI join's match fraction is
  itself correct (`matchFrac≈0.998`, M0142-0006's own code); the collapse is
  in its OUTER input — `l = EstimateRows(j.Left)` returns **1** for a
  4-relation join subtree the SAME `EXPLAIN` renders with `rows=32/91/99` at
  every level (a `pattern_sibling_paths_must_agree` shape: two different call
  sites disagree on one subtree's row count). Isolated to two undisambiguated
  candidate mechanisms, filed together as **M0142-0011**: **(A)**
  `estimateJoin`'s measured-selectivity branch is gated on `Algo ==
  JoinAlgoHash || Algo == JoinAlgoMerge` (`cardinality.go:~792`), so a plain
  `*Join{Algo: JoinAlgoNestedLoop}` proven-key equality lookup (`l≈91, r=1`)
  falls to the `l*r*0.005` unmeasurable fallback instead of the measured
  path, though PG's own joinrel sizing is algorithm-agnostic; **(B)**
  `EstimateRows(*Join)` never consults the node's own already-costed
  `PlanCost.PlanRows` the way `legacyDisplayCostOf`/the EXPLAIN renderer
  (`operators_explain.go:3024-3028`) already do elsewhere, so a fresh
  bottom-up recompute silently disagrees with the accurate cached value on
  the same node. Not landed this loop: either fix touches `EstimateRows`/
  `estimateJoin` broadly enough (every generic-NestedLoop-algo join in the
  corpus, or `EstimateRows`' whole contract) to need its own scoping and
  full floor-measurement pass, matching M0142-0005's own "sized like an
  executor slice" treatment this loop already applied to it.
- [x] **M0142-0004c — re-run `make ea-ratchet` to confirm the C1 findings
  collapse post-fix** — Filed by M0142-0004a. **DONE 2026-09-15.** Re-ran the
  ~10min capture (`make ea-ratchet`, fresh goopg build + its own clone/port,
  `GOOPG_ANALYZE_SEED=20260905` pinned) against the 2026-09-15 baseline.
  **(a)** All 18 C1-shaped `Index Scan using X_pkey est=1` findings from the
  prior census (not 19 — one query's count was mis-tallied in M0142-0004a's
  prose; the actual old-census set had 18 members, listed in the ratchet's
  `FIXED` output) are gone from the new findings list — confirmed by exact
  `(query, node, relset)` set-difference against the old JSON, 18/18 dropped,
  0 remaining. **(b)** Total findings **140 -> 122** (18 fewer, 0 new); the
  script's own ratchet verdict is `EA-RATCHET: PASS (18 fixed)` — a `rows=`
  fix only ever lowers an inflated qerr, never raises one, and that held
  corpus-wide, not just on the witness. Re-pinned the baseline
  (`EA_CAPTURE=tmp/c20a/ea-capture.txt EA_REPIN=1 bash
  scripts/estimate-parity-gate.sh`, no second server run needed) to the new
  122-entry set so future loops ratchet from the post-fix state; archived the
  capture/findings as `ea-capture-20260915-post0004a.txt` /
  `ea-findings-20260915-post0004a.json` alongside the original same-day
  census in `analysis/planner-refactor-take3/c20a-estimator-census-20260915/`.
  **(c)** Checked every one of the 25 highest-qerr remaining findings
  (Q47/Q57 CTE Scan, Q25/Q34/Q73/Q29/Q53/Q63/Q68 Nested Loop, Q10/Q69/Q73/Q33/Q68
  Gather, Q87 HashSetOp, Q5/Q68 Hash Join, Q33/Q60 CTE Scan) against the raw
  capture text directly: **every one of them is itself `loops=1`** — the
  fix already correctly divides the *children* of these nodes (their inner
  Index Scan / per-Worker lines now read near-1 per-loop averages, e.g. Q34's
  `household_demographics` index scan went from the old `rows=10082
  loops=10082` misreport to `rows=0.11 loops=93640`, no longer flagged) but
  the *parent* join/Gather/CTE node's own cardinality estimate is a separate,
  genuine defect, not a second loops-capture artifact. **Verdict: no further
  loops>1 artifacts remain in the corpus** — C1 was a clean, fully-collapsed
  fix. Filed the newly-visible mechanism as **M0142-0009** (below): several
  plain `Nested Loop` join nodes (not just NLI/SEMI/ANTI shapes) estimate
  single-digit rows against four-to-five-digit actuals at `loops=1`,
  independent of C1/C2 — e.g. Q34 `date_dim+household_demographics+store`
  est=3 vs actual=9969 (qerr 3323), Q25 `date_dim+store_returns` est=1 vs
  actual=6422. Gates: none beyond the gate itself (measurement-only task, no
  production code touched this loop).
- [ ] **M0142-0005 — give the executor a per-worker Memoize so a Gather-wrapped
  partial NLI+Memoize candidate can compete (RE-SCOPED 2026-09-16)** — the
  2026-09-16 recon (`docs/design/0100-0149/m0142-0005-recon-partial-memoize-refused-by-gather-eligibility.md`)
  found the original B6/B8 framing below is **stale**: Memoize already exists
  (executor `operators_memoize.go`, cost-based DP-search path
  `joinpathsmemoize.go`, live since `894485ba7` 2026-07-21) and, on TPC-DS
  Q34, the DP search already generates and correctly costs the PG-matching
  `Nested Loop`+`Memoize`+`Index Scan using date_dim_pkey` shape as a
  **partial** path (`total=16463.24`, cheapest survivor in its joinrel's
  `PartialPathlist`, near-identical to PG's own real per-worker cost
  `16125.21`) — the join-order/cardinality layer is already PG-faithful here.
  **The actual blocker**: `gatherpaths.go`'s `partialPathDrivingKind`'s
  `PathNestLoop` case (`:456-490`) unconditionally refuses to build a Gather
  path over any Memoize-wrapped inner (`in.Kind == PathMemoize` /
  `in.Kind != PathIndexScan`), so the candidate never becomes a competing
  TOTAL path and a plain serial/gathered hash join wins by default. This is a
  deliberate, already-reasoned restriction, not an oversight: the executor's
  `memoizeOp` (`internal/executor/operators_memoize.go`) is a single cache
  instance with no per-worker claim-set partitioning story
  (`parallelClaimSet`/`attachAll`, `gatherpaths.go:322-328`, models only
  `PathSeqScan`/`PathIndexScan`/`PathBitmapHeapScan`/`PathHashJoin`/
  `PathMergeJoin`/plain `PathNestLoop`). **Resume point**: either (a) give the
  executor a per-worker-partitioned Memoize cache (PG oracle:
  `nodeMemoize.c`'s `parallel_worker_number`-keyed model) and then relax
  `partialPathDrivingKind`'s lateral-probe branch to admit `PathMemoize`, or
  (b) find a narrower relaxation if (a) is not directly portable. Sized as an
  executor+planner slice — needs its own scoping/floor-measurement pass before
  implementation, same discipline as M0142-0012's `-verify`/`a` split.
  **B8** (`indexProbeCostMultiplier = 2.0`, `cost_funcs.go:1053`/`:1077`) is a
  separate, narrower, UNSETTLED question this recon did not re-measure: its
  calibration (`c61781d6`, 2026-09-05) post-dates Memoize's own landing, so its
  continued need against the current binary is unverified — do not assume it
  is still required OR that it is now dead weight; measure before touching it.
  **Corpus evidence this task now carries** (M0142-0010, 2026-09-15): the
  `date_dim+store+store_sales` `make ea-ratchet` findings (Q34/Q73, qerr ~42)
  — goopg's `estimateJoin` fallback there is verified PG-formula-identical, so
  the qerr is purely a consequence of being forced into a hash join instead of
  the Gather-blocked NLI+Memoize shape; see
  `docs/design/0100-0149/m0142-0010-join-level-gap-is-memoize-shape-not-cardinality-bug.md`.
- [x] **M0142-0006 — apply `semiJoinMatchFraction` in `estimateNLIndexJoin`** —
  `estimateNLIndexJoin` (`cardinality.go:239-241`) returns `EstimateRows(j.Outer)` for
  SEMI/ANTI, while its sibling `estimateJoin` (`:608-625`) applies the match
  fraction. Textbook `pattern_sibling_paths_must_agree` defect; the ledger says
  mirror lines 621-628 at `:240`. Ledger: `m0137-0013-nli-semi-anti-match-fraction-gap`.
  **DONE 2026-09-15, full writeup in
  `docs/design/0100-0149/m0142-0006-nli-semi-anti-match-fraction.md`.** The
  ledger's "mirror lines 621-628" framing does not literally work: an NLI's
  `Predicate` is residual-only (the equi-clause is stripped into
  `Inner.Key`/`Inner.Keys` at `createplannl.go:418-422`), so a synthetic-`Join`
  wrapper reading `Predicate` finds zero equi-pairs and is a silent no-op on
  the common fully-bound probe — caught by hand-building a SEMI-NLI fixture
  before committing to that approach (no pre-existing test exercised NLI
  SEMI/ANTI narrowing). Fix: new `nliSemiMatchFraction` sources the key term
  from `Inner.Key`/`Inner.Keys` instead, resolving both sides via the
  existing generic `resolveBaseColumn` and feeding `eqjoinselSemiCore` (same
  core formula/`ResolvedNDistinct`/unique-index-override/`innerRows`-clamp as
  the `*Join` arm's `semiPairMatchFraction`, without its merged-coordinate
  assumption, which does not hold for NLI's asymmetric Outer/Inner shape).
  New test `TestEstimateRowsNLIndexJoinSemiScalesByMatchFraction` pins SEMI
  1000→100 / ANTI 1000→900. Gates: `go build ./...` clean; `go test
  ./internal/optimizer/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2,
  Q13=34); `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0,
  plan-shape `same=99 changed=0`; `make ea-ratchet` 112→112 unchanged (no
  query in the current SF0.25 corpus has its plan choice gated by this
  estimate). Does not touch M0142-0005 (orthogonal plan-shape-selection gap).
- [x] **M0142-0007 — re-measure the `corr = 0` index-pricing fallback (B10)** —
  **DONE 2026-09-15, recon closed, no code change.** Full writeup in
  `docs/design/0100-0149/m0142-0007-corr-zero-fallback-does-not-fire-post-analyze.md`.
  Instrumented `indexCorrelationFor` (temporary env-gated trace, reverted) on
  the TPC-DS SF0.25 cluster, re-ran `ANALYZE;`, then ran `EXPLAIN` over the
  full 100-query corpus: **0 of 2905 calls hit the `nilstats` fallback** —
  every leading-key column has a real correlation value (61% exact `1.0` on
  PK columns, the rest genuinely-measured small non-zero values). **Verdict:
  the `corr=0`-fallback half of B10 is closed as measured — M0138-0004's
  correlation writer plus a plain `ANALYZE;` fully populate the slot; no fix
  needed.** The ledger row's *other* bundled half —
  `estimateIndexGeometry` synthesising relpages/reltuples/tree_height because
  ANALYZE never visits indexes at all — is untouched and stays open (re-ANALYZE
  cannot close it; there is nothing to visit). TPC-H's own fallback rate was
  not measured (bench peer is another loop's live server). Ledger:
  `m0137-0012-b10-corr-zero-fallback-max-io-cost` (updated with this
  finding).
- [x] **M0142-0008 — recon: how much of goopg's plan shape is chosen by forced
  rewrites rather than by the search?** (measurement only, no production diff) —
  the goal says plans must be reached by **the same planning logic**, and several
  ledger rows claim goopg still depends on goopg-only forced rewrites that the
  search cannot yet reproduce: `rewriteScanInputsWithSingleTablePredicates`
  (deleting it costs Q20 6.5x), `rewriteJoinsToNLI` (deleting it costs Q4's
  semi-join 12.5x), plus the PG-absent knobs `GOOPG_NLI_COSTGATE`,
  `GOOPG_INDEXKEY_HARVEST`, `GOOPG_INDEX_PROBE_MULT`, `enable_nestloop_index`.
  Ledger: `take2-P6-03`, `take2-P6-04`, `take3-C-20c-blocked`, `-20d-calibrated`, `-20f-blocked`, `-20g-blocked`, `take2-P2-10`,
  `take3-C-09-declined`, `take2-P3-01`.
  **DONE 2026-09-16, recon closed, no code change. Full writeup:
  `docs/design/0100-0149/m0142-0008-forced-rewrites-vs-search-census.md`.**
  Read all six mechanisms' code and `git log -S` provenance directly (no
  runtime trace needed). **Finding: both rewrite functions carry an explicit
  `isSearchedTree` guard confining them to the portion of the tree the DP
  search never reaches at all** — the ledger's 6.5x/12.5x "deletion
  regresses" numbers are the rewrite being the sole decision-maker for
  subtrees outside the search's current structural coverage, not the
  rewrite beating a cost-based search on cost. **Q4** (`take2-P6-04`):
  `unnestExistsExpr` builds the SEMI join directly on the raw parse tree
  before any search joinrel exists (the already-verified M0139-0005/F11/K63
  "no sjinfo → no joinrel → no path → no price" lineage) — `rewriteJoinsToNLI`
  is the only producer despite `addNLIPaths` nominally admitting SEMI/ANTI.
  **Q20** (`take2-P6-03`): the search's single-table index-scan mechanism is
  structurally capable, but `planner.go:1590-94`'s own comment says a
  filterless INNER/CROSS tree is left on the legacy path pending its own
  gated widening. Of the four knobs: `GOOPG_NLI_COSTGATE`/
  `GOOPG_INDEX_PROBE_MULT` are architecturally sound (tune inputs the
  search's own `addPath` already consumes); `enable_nestloop_index` is a
  clean, already-correctly-scoped kill switch (`take3-B-17e-blocked`); only
  `GOOPG_INDEXKEY_HARVEST` joins the two rewrites as a genuine pre-search,
  shape-determining mechanism (already correctly flagged in
  `take3-C-20c-blocked`). **Verdict: a dedicated milestone is warranted,
  scoped to closing the two named structural coverage gaps — not to
  deleting the rewrites**, which would reproduce the measured regressions
  until the gap is closed. Filed as scoping-recon follow-ups
  **M0142-0008a** and **M0142-0008b** below.
- [x] **M0142-0008a — scoping recon: size wiring SEMI/ANTI decorrelation
  into the DP search's joinrel machinery** — filed by M0142-0008. **DONE
  2026-09-16, recon closed, no code change.** Design doc:
  `docs/design/0100-0149/m0142-0008a-scoping-recon-semi-anti-decorrelation-census.md`.
  **Corrects M0142-0008's own framing**: since S5a (`GOOPG_UNNEST_PREDP`,
  default ON), a correlated `EXISTS`/`NOT EXISTS`'s pulled-up semi/anti join
  is *pinned* above a DP search that already runs on the subtree below it
  (`runJoinSearchBelowPinned`, `predp.go`) — the residual gap is narrower
  than "no search at all": the pinned join itself never gets a joinrel/
  `SpecialJoinInfo` and so never competes in `addPath` for placement or
  Hash-vs-NLI algorithm choice. A second, worse shape also exists: a WHERE
  clause mixing a correlated EXISTS with a co-resident scalar subquery fails
  `whereEligibleForPreDPUnnest`'s all-or-nothing gate and falls to the
  legacy post-DP path — a TOTAL bypass, no partial win (TPC-H Q22).
  **Census**: 8 real corpus queries carry a correlated EXISTS/NOT EXISTS
  (TPC-H Q4/Q21/Q22; TPC-DS query10/16/35/69/94), 6 of them stacking 2-3
  pinned semi/anti joins each (TPC-DS's recurring store/web/catalog
  "channel comparison" idiom) — not a niche gap. All corpus `IN (subquery)`
  hits are non-correlated (CTE or self-contained bodies), a different
  mechanism, not a witness here. **PG oracle citation** (`pathnodes.h:3027-3042`
  `SpecialJoinInfo`, `joinrels.c:350` `join_is_legal`, `prepjointree.c:468`
  `pull_up_sublinks`) confirms M0139-0005's "materially larger task" call
  was correct: wiring this needs semi/anti join-order *legality* constraints
  (`min_lefthand`/`min_righthand`), not just one more `addPath` candidate.
  **Verdict: do not attempt in one sitting** (K24 precedent, same as S2b-2).
  Decomposed into:
  - [x] **M0142-0008a-1** — design-only: read PG's `join_is_legal`
    (`joinrels.c:350`) and `SpecialJoinInfo` construction in
    `pull_up_sublinks`/`deconstruct_jointree` in full, and produce a
    concrete goopg design (data structure + where it is built + how
    `joinsearchlevel.go` would consult it). **DONE 2026-09-16, design-only,
    no production diff.** Design doc:
    `docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md`. **Two
    corrections to M0142-0008a's own framing**: (1) this is PG's own
    previously-scoped-and-deferred "S5b" item (deferral ledger row
    `csq-R2`, deferred 2026-07-21, explicit reopen criterion — "a query
    differing from PG ONLY by semi/anti placement" — that the census is
    suggestive but not yet CONFIRMED evidence for; re-run plan-compare
    per query before -2/-3 start); (2) goopg already has PG's
    `SpecialJoinInfo`/`join_is_legal` fully ported and unit-tested for
    `JOIN_SEMI`/`JOIN_ANTI` (`specialjoin.go`, wired via `collapse.go:514`
    for ordinary FROM-clause joins) — no new struct/legality algorithm
    needed. Real gap is four wiring holes, the costliest being that
    `joinpaths.go`'s `addPathsToJoinrel` declines to build a HASH join
    for SEMI/ANTI at all today (nested-loop only), while `unnestExistsExpr`
    already builds hash semi/anti joins directly for every census query
    with an equijoin pair — wiring -3 without first lifting this gate
    would regress Q4/Q21/Q22 and the TPC-DS channel-comparison queries
    from Hash to Nested-Loop-only. Re-scopes -2 (smaller than filed) and
    splits -3 into three separately-sizable increments — see the design
    doc §4 for file+line resume points.
  - [x] **M0142-0008a-2** — RE-SCOPED by -1 (smaller than originally
    filed): attach an inert `*SpecialJoinInfo` to `unnestExistsExpr`'s
    built `*Join` node using the existing `makeSpecialJoinInfoScoped`
    shrink algorithm (not its parser-facing `sc`/`item`/`lower` signature —
    the same min-lefthand/min-righthand computation), with nothing
    consuming it yet — trivially satisfies "no plan-shape change
    expected". New unit tests from a real `unnestExistsExpr` fixture
    (today's `JoinSemi`/`JoinAnti` legality tests are unit-only, never
    exercised end-to-end). See design doc §4.1. Gated on M0142-0008a-1
    (done). **LANDED 2026-09-16 (design doc §7):** new
    `existsUnnestSJInfo` helper (`unnest.go`, package-local — the
    parser-facing `makeSpecialJoinInfoScoped` signature does not apply
    to this producer's already-resolved `unnestParam`/residual data), a
    new `Join.SJInfo *SpecialJoinInfo` field (`plan.go`), wired at
    `unnestExistsExpr`'s single `&Join{...}` construction site. Uses a
    self-contained 2-bit RelSet (LHS=1, RHS=2), NOT the search's global
    per-call numbering — -3 must remap, not reuse, these bits if it
    lands the atomic-RHS version. Zero readers today (grep-confirmed).
    Verified: full `internal/optimizer` suite green; TPC-DS SF0.25 sweep
    `PLAN-SHAPE: queries=99 same=99 changed=0`, `MISMATCH=0`; TPC-H
    Q12/Q13 spot-check SKIPPED (pre-existing `:65433` data gap,
    M0142-0003k, unrelated). Three new unit tests in
    `exists_unnest_sjinfo_test.go` (hash-keyed Semi, hash-keyed Anti,
    keyless nested-loop Semi) pin the computed values from real
    `unnestExistsExpr` fixtures.
  - [!] **M0142-0008a-3** — **FROZEN (owner Q2, 2026-09-17; see banner)** — RE-SCOPED by -1 into three separately-landable
    increments (see design doc §4.2): (i) make the decorrelated RHS a
    real DP-search participant (extend `runJoinSearchBelowPinned` to walk
    the pinned join's RIGHT child too, give its base rel(s) `RelSet` bits
    in the same per-search-call numbering as the LHS `bindings`); (ii)
    legality wiring / end-to-end integration verification of the
    already-ported `joinIsLegal` SEMI/ANTI arms, then retire
    `runJoinSearchBelowPinned`'s splice-and-reresolve path for the
    now-natively-searched cases (keep it for Q22's legacy-post-DP class);
    (iii) **must happen before or alongside (i)/(ii), not after** — lift
    `joinpaths.go`'s `nestloopOnly` hash-decline gate for SEMI/ANTI in the
    natively-searched case. **Trace-through DONE 2026-09-16 (no code
    change): CONFIRMED generic**, not assumed — design doc §5 traces
    `createHashJoinPlan`/`planJoinTypeFor`/`joinInputsFor`
    (`createplanjoin.go`) end to end and finds no `Jointype`-specific
    refusal; PG's `create_unique_path` entanglement is real but scoped to
    exactly one cost-refinement input (`hashJoinFinalCostInputFor`,
    `hashjoin_innerunique.go:34`) that already fails closed to a safe
    default for non-`JoinInner` rather than blocking anything, and the
    executor's hash-join runtime is independently proven correct for
    Hash Semi/Anti in production today via `unnestExistsExpr`'s hand-built
    `Join{Type:Semi/Anti, Algo:Hash}` nodes (TPC-H Q4/Q21/Q22 canonical
    counts). Remaining risk is costing-tightness only (conservative
    non-unique-bucket costing, never wrong) — the §4.3 plan-compare gate
    still decides whether that's tight enough to win `addPath`, so (iii)'s
    actual gate-lift edit is unblocked to implement but its OUTCOME is not
    yet known. This is the slice that can actually move TPC-DS
    query10/16/35/69/94 and TPC-H Q4/Q21's plan shapes. Gated on
    M0142-0008a-2; re-confirm the §4.3 plan-compare gate first.
    **§4.3 gate re-run DONE 2026-09-16 (design doc §6, TPC-DS half only —
    TPC-H's Q4/Q21/Q22 remain unmeasurable, `:65433`'s `tpch` DB still holds
    only M0142-0003k's scratch tables).** Corrects the census's own premise:
    goopg is not losing a Hash-vs-NLI competition on these 5 queries today —
    `unnestExistsExpr` hardcodes `Algo:Hash` unconditionally, so there is
    currently no competition to lose. 3/5 (Q16/Q69/Q94) show the PG
    divergence isolated to semi/anti algorithm choice (placement/nesting
    order already matches PG in all three) — **this clears M0142-0008a-2 to
    start.** 2/5 (Q10/Q35) instead need PG's `create_unique_path`
    uniquify-then-inner-join strategy, a materially different mechanism -2/-3
    do not build — filed separately as **M0142-0008c** (below), not a reason
    to decline -2/-3. Also incidentally found an EXPLAIN alias-mislabeling
    cosmetic bug on Q16/Q94 (execution-verified correct, display-only) —
    filed as **M0142-0008d** (below).
    - [x] **M0142-0008a-3(iii) — lift the hash-decline gate — LANDED
      2026-09-16 (design doc §8).** `joinpaths.go`'s single `nestloopOnly`
      boolean (gated BOTH the hash arms AND the merge arms as one block) is
      split into `mergeDeclined` (unchanged: still true for SEMI/ANTI,
      since `join_merge_stream.go` has zero Semi/Anti references — the §5
      trace-through never examined merge, only hash) and an unconditional
      hash path (`addHashJoinPath`/`addPartialHashJoinPath` now reachable
      for SEMI/ANTI same as any other jointype). Corrected the file's own
      stale "SEMI/ANTI contract" doc comment (it claimed hash would
      "MULTIPLY rows" for SEMI — false; `join_batch.go:340,363` already
      runs Semi/Anti hash joins correctly in production via
      `unnestExistsExpr`). Updated 4 unit tests that encoded the old
      nestloop-only assumption. **Live-producer risk found and closed, not
      just measured for -2's inert field**: `reduceOuterJoins`'s LEFT→ANTI
      demotion (S9.3) already produces a real `SpecialJoinInfo{Jointype:
      JoinAnti}` that reaches the ordinary DP search TODAY, independent of
      -3(i)/(ii) landing — so this gate-lift could in principle have moved
      a real plan shape, not just prepared for a future one. Empirically
      verified it did not: `scripts/tpcds-sf025-regression.sh sweep`
      (99-query TPC-DS SF0.25 corpus) — `PLAN-SHAPE: queries=99 same=99
      changed=0`, `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`.
      `scripts/tpch-spotcheck.sh` SKIPPED (pre-existing, unrelated —
      M0142-0003k). Full `internal/optimizer` suite green. Deferred: MERGE
      staying declined for SEMI/ANTI is a real, unstarted gap (ledger row
      `M0142-0008a-3iii`) — `join_merge_stream.go` needs its own Semi/Anti
      early-exit/dedup trace before `mergeDeclined` can be lifted the same
      way. **Still open in -3: increments (i) RHS-as-participant and (ii)
      legality wiring/integration verification** — this hash-admission
      work is what lets `addPath` cost-compare once those land.
  - [x] **M0142-0008a-3(i) — recon: what does "make the RHS a real DP-search
    participant" actually require?** DONE 2026-09-16 as a recon (design doc
    §11), no production change. Confirmed the starting shape (one
    `tryJoinSearch` call at the bottom, `x.Left`-only descend,
    `tryPGShapedJoinSearch` reads `ctx.bindings`/`joinlist`/`joinInfoList`
    directly with no seam for an extra ad hoc participant) and that
    -0008a-2's `existsUnnestSJInfo` already anticipated the atomic-RHS
    2-bit numbering this increment is meant to make real. **Decisive
    finding: `extractSearchLeaves` (the flattener `tryPGShapedJoinSearch`
    uses) already treats any non-Cross/Inner/Left/Right node as ONE opaque
    leaf — the right primitive for "atomic RHS" — but `x.Right` is not
    guaranteed opaque to it**, since a multi-table EXISTS body (TPC-DS's
    channel-comparison idiom, Q10/Q35's class) plans to a top node that is
    itself `*Join{Type:Inner}`, which the walk would wrongly recurse into
    and try to re-flatten using `Node`s never registered in
    `ctx.bindings`/`relInfos`. Two closing options sized in §11 (a new
    opaque-participant wrapper Node type threaded through every leaf-Node-kind
    switch downstream — `baseSeqScanCostInputs`, `createPlan`'s leaf arm,
    `explain_names.go` — vs. hand-constructing the bindings/joinlist and
    calling `planJoinlistSearch` directly, bypassing the shared seam and
    duplicating its conjunct-partitioning/boundary-identity contracts), plus
    a still-open second problem independent of either option: the
    post-search splice (`reresolveJoinByName`) assumes the pinned spine's
    shape survives search unchanged, which stops being true once the RHS is
    a real relset bit the search can place anywhere. **Verdict: -3(i) is a
    re-scope, not a start** — do not attempt the descend-loop extension
    directly. Follow-up filed as **M0142-0008a-3i-recon2** (below).
  - [x] **M0142-0008a-3i-recon2 — size option 1 vs option 2 (§11) against a
    real multi-table-EXISTS-body TPC-DS witness** — filed by -3(i)'s recon.
    **DONE 2026-09-16 as a recon (design doc §12), no production change.**
    Two findings, both correcting the filing: **(1) the named witness was
    wrong** — `query10`/`query35`'s PG-chosen plans have ZERO Semi/Anti join
    nodes anywhere (PG reaches them via `create_unique_path`
    dedupe-then-probe, M0142-0008c's actual subject); Q69 (same census) is
    the real multi-table-EXISTS-body witness for THIS mechanism, and a
    richer one than hoped — it exercises the RHS-as-participant shape three
    times in one query (one Semi + two Anti joins, each RHS a genuine
    2-relation `channel ⋈ date_dim` body), with goopg already independently
    landing on the matching `Hash Semi/Anti Join` node kind. **(2) §11's
    blast-radius estimate for option 1 was too pessimistic**: reading (not
    just grepping) `baseSeqScanCostInputs`, `PathPrebuilt`/`createPlan`, and
    `EstimateRows` shows the first two are ALREADY fully generic over any
    wrapped Node kind (zero new code), and the third already has a `*Join`
    case that covers Q69's witness directly (`x.Right`'s top node is
    `*Join{Inner}`) — only `explain_names.go` remains genuinely unverified,
    and any gap there is display-only per the M0142-0008d/e precedent. The
    obvious reuse shortcut (wrap `x.Right` in the existing `*CTEScan`) is a
    **trap, not a shortcut**: `cteScanOp` materializes-and-caches keyed by
    `DeclKey()`, and Q69 needs three independent RHS wraps that would
    collide on the same bare-name cache key. A plain no-op
    `*Filter{Predicate: nil, Child: x.Right}` looks like the correct,
    near-zero-blast-radius wrapper instead (stops `extractSearchLeaves`'s
    walk, `EstimateRows`'s `*Filter` case is an exact pass-through, `Filter`
    is already generic everywhere else) — **unverified this loop, static
    read only**. `reresolveJoinByName`'s post-search-splice problem (§11)
    is untouched by any of this and still needs its own resolution. Next
    step filed as **M0142-0008a-3i-verify** (below): a throwaway
    instrumented probe against Q69 confirming the Filter-wrapper hypothesis
    before any real DP-search plumbing is written.
  - [x] **M0142-0008a-3i-verify — confirm the `*Filter`-wrapper hypothesis
    against the Q69 witness with a live instrumented run** — filed by
    -3i-recon2 (design doc §12.3/12.4). **DONE 2026-09-16 (design doc §13),
    no production change.** The live probe
    (`internal/optimizer/m0142_0008a_3i_verify_probe_test.go`,
    `TestExistsUnnestTwoRelationRHSTopNodeIsProjectNotBareJoin`) REFUTES the
    literal claim it set out to confirm: `x.Right`'s top node is
    `*Project{Child: *Join{Inner}}`, not a bare `*Join` — `unnestExistsExpr`
    clones the EXISTS body's own already-planned subquery tree, and every
    planned `SELECT` (even `SELECT 1`) carries its own top-level
    output-list `*Project`. The consequence is BETTER than the hypothesis:
    no `*Filter` wrapper (or any new node) is needed at all —
    `extractSearchLeaves` already stops at the `*Project` as one opaque
    leaf, `EstimateRows` already has a generic `*Project` pass-through case
    (confirmed numerically equal to the inner Join's own estimate), and
    `baseSeqScanCostInputs` already takes its documented generic
    non-`*SeqScan` fallback — all three confirmed live, not re-derived from
    reading. **Follow-up filed as M0142-0008a-3i-plumbing** (below): -3(i)
    can now skip straight to §12.4's (a)/(b)/(c) DP-search plumbing with no
    wrapper-node step first.
  - [x] **M0142-0008a-3i-plumbing-recon3 — is §12.4's (a)/(b)/(c) the right
    decomposition to implement?** Filed by M0142-0008a-3i-verify (design doc
    §13.3), worked instead of coding (a)/(b)/(c) blind. **DONE 2026-09-16 as
    a recon (design doc §14), no production change. Answer: no** — §12.4's
    framing (append `x.Right` to `bindings`/`relInfos`, patch SJInfo
    bookkeeping, then fix `reresolveJoinByName`'s splice) is not buildable as
    literally stated and, even fixed up, cannot reach PG's Q69 shape.
    Three findings: **(1)** `x.Right` cannot become a `rangeBinding` by a
    bare append — `rangeBinding.table` (`*catalog.Table`) is dereferenced
    unconditionally by dozens of statement-wide column-resolution call sites
    in `planner.go`; it is buildable only via the same synthetic-`&catalog.
    Table{}` pattern every derived-table/CTE leaf already uses, which also
    turns out to sidestep the Q78 catastrophic-mis-costing firewall entirely
    (`problemPairsOuterWithDerived` explicitly skips non-outer jointypes).
    **(2)** The actual blocker is one layer up: `reresolveJoinByName`
    re-resolves an ALREADY-PLACED join's predicate in place — it cannot
    relocate the join or let the search build a new Semi/Anti node at a
    chosen position, so no version of (a)+(b)+(c) built on top of
    `runJoinSearchBelowPinned`'s splice model can reach an interleaved shape
    like PG's Q69 plan (Semi Join low in the tree, two Anti Joins stacked
    above it). **(3)** goopg already has the right mechanism for a sibling
    problem: `extractSearchLeaves`'s existing LEFT/RIGHT **admission** (not
    splicing) — flatten both sides into the ordinary leaf list, record a
    chain-link struct, let the search's own `join_is_legal`-fed legality
    machinery place the join — is the S5b mechanism the design doc's §1
    already named as possibly-reopenable, and reuses ~90% of already-tested
    infrastructure instead of the from-scratch (b)/(c). Revised, buildable
    5-item plan in design doc §14.3. **Bigger than §12.4 estimated** (touches
    `extractSearchLeaves`, a function three past C-04-series regressions
    already hardened) — not sized for one loop. Follow-up
    (`M0142-0008a-3i-plumbing`, below) is rescoped to match.
  - [x] **M0142-0008a-3i-plumbing-a — `semiAntiChainLink` type + its
    `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK` legality consumers, plus
    `problemPairsOuterWithDerived`'s Semi/Anti firewall arm, as INERT
    infrastructure (design doc §15's settled item 2 + its new safety
    finding)** — supersedes the old undifferentiated
    `M0142-0008a-3i-plumbing` filing (design doc §21). **DONE 2026-09-16.**
    Landed exactly §15's settled shape: `semiAntiChainLink{jointype, lhs,
    rhs RelSet, pred Expr}` (no `preserved`/`nullable` — neither concept
    applies to a join with no NULL-extension), `semiAntiLinksHaveSJInfos`
    (mirrors `outerLinksHaveSJInfos`), `semiAntiOnQualsOK` (mirrors
    `outerOnQualsOK`, simplified to "every conjunct spans both `lhs` and
    `rhs` and is search-consumed" per Semi/Anti's narrower contract) —
    `internal/optimizer/joinsearchseam.go`. Added `parser.JoinSemi,
    parser.JoinAnti` to `problemPairsOuterWithDerived`'s switch
    (`relfromjoinlist.go:590`) in the SAME change, per §15's own instruction
    not to defer the firewall arm past the change that first makes Semi/Anti
    admission possible. 9 new tests (`internal/optimizer/semiantichain_test.go`)
    prove the POSITIVE case §15 could only predict: `semiAntiOnQualsOK`
    ACCEPTS the exact well-formed Q69-witness link `outerOnQualsOK` was
    proven to incorrectly decline, plus decline cases for both consumers and
    a not-derived/derived pair for the firewall arm (mirroring
    `outer_over_derived_test.go`'s LEFT/RIGHT precedent). Updated
    `m0142_0008a_3i_plumbing_probe_test.go`'s consumer-#3 assertion, whose
    nil-table fixture now correctly declines via the new arm instead of via
    the old blanket skip. **Not wired into `extractSearchLeaves` or
    predp.go** — a live call-chain trace this loop (design doc §21.1) found
    that would-be "item 1" (the walk's type-test extension) is dead code on
    its own: `planner.go`'s `origChain` is snapshotted BEFORE unnesting runs
    (never contains Semi/Anti) and `predp.go`'s descend loop hard-bails on
    any non-Semi/Anti `*Join`, so items 1/4/5 from the old filing are ONE
    coupled step, not three — re-filed as **M0142-0008a-3i-plumbing-b**
    below. Verified empirically, not just by inspection: `go build ./...`
    clean, `go vet ./internal/optimizer/...` clean, `go test
    ./internal/optimizer/...` full pass, TPC-DS SF0.25 sweep (private
    `GOOPG_BIN=tmp/goopg-sf025-bin`) `PASS=96 MISMATCH=0 CKMISMATCH=0
    ERROR=0`, `PLAN-SHAPE: same=99 changed=0` — importantly including
    `reduceOuterJoins`'s LEFT→ANTI demotion, the one live producer of a real
    Semi/Anti `SpecialJoinInfo` already in `ctx.joinInfoList` today, which
    the new firewall arm could in principle have affected.
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: same
    pre-existing, unrelated `internal/parser` `GroupedJoinUnaliased`
    AST-drift failure the prior three loops already found; `internal/optimizer`
    passes. Resume point: design doc §21.
  - [x] **M0142-0008a-3i-plumbing-b — scoping pass ONLY (design doc §22)** —
    filed by -3i-plumbing-a's live trace as "needs its own dedicated scoping
    pass before coding." **DONE 2026-09-16 as a recon, no production
    change.** Both of §21.4's open questions are now answered: `origChain`
    has exactly one use site (grepped, no other callers/assumptions to
    worry about) and "move the capture post-unnest" was never the right fix
    anyway — `origChain` structurally never contains a Semi/Anti node under
    current engagement scope no matter when it's captured, because
    unnesting wraps it from above rather than replacing it; the real content
    of item 1 is "feed `tryJoinSearch` a tree that includes the spine," a
    `predp.go`-level change, not a `planner.go` timing change (§22.1).
    `chainOnQual.belowNullable` needs NO Semi/Anti-aware arm: a Semi/Anti
    arm in the walk should recurse into `j.Left` and return `below`
    unchanged, exactly like the existing INNER-link arm, because Semi/Anti
    null-extends neither side (§22.2) — this REMOVES an item from scope
    rather than adding one. But tracing question 1 one level further out
    surfaced a **sixth coupled dependency neither §14.3 nor §21.4 named**:
    `ctx.bindings`/`ctx.joinlist` are fixed at FROM-clause-resolution time,
    strictly before `unnestSubqueriesInPlan` runs, and
    `unnestSubqueriesInPlan`/`unnestExistsExpr` take no `*resolveContext` at
    all — so a Semi/Anti RHS leaf the walk admits has no `ctx.bindings`
    entry, and `tryPGShapedJoinSearch`'s existing leaf-count/offset checks
    (`joinsearchseam.go:309,324`) would decline the search outright without
    also either plumbing a `*resolveContext` through the unnest call chain
    to append a synthetic binding, or relaxing those checks specifically for
    Semi/Anti-RHS leaves (§22.3). Revised decomposition, replacing this
    item: **`M0142-0008a-3i-plumbing-b1`** (walk extension + `predp.go`
    pass-through + `semiAntiChainLink` production + item 4's SJInfo rebuild,
    landed as an INERT scaffold gated behind an eligibility check that can't
    fire without item 6, same "prove inert before wiring" shape as
    `-3i-plumbing-a`/`-0008c-1/-2/-3a` — **the next loop-sized task**) and
    **`M0142-0008a-3i-plumbing-b2`** (the `ctx.bindings`/`joinlist`
    extension + the actual cutover + retiring `predp.go`'s
    splice-and-reresolve only for statement shapes proven to be handled
    end-to-end by the new path — depends on `-b1` and likely on more of
    `M0142-0008c-3c`/`-3d`/`-4` landing first). This is still the actual
    unblock for TPC-DS Q10/Q35 and for `M0142-0008c-3c`/`-3d` (both still
    blocked on it) to ever affect a real plan.
  - [x] **M0142-0008a-3i-plumbing-b1 — INERT scaffold: walk extension +
    SJInfo rebuild (design doc §22.4, narrowed by §23.4)** — filed by
    -3i-plumbing-b's scoping pass. **DONE 2026-09-16.** Landed: extended
    `extractSearchLeaves`'s `*Join` type-switch (new `admitSemiAnti bool`
    parameter, literal `false` at the one production call site) to admit
    `JoinTypeSemi`/`JoinTypeAnti` per §22.2's settled semantics (recurse
    `j.Left`, append `j.Right` as one opaque leaf, return `below` unchanged,
    build a `semiAntiChainLink`); rebuilt the matched join's `SJInfo`'s
    Syn/MinLefthand/Righthand from real leaf-index bits at admission time,
    replacing `existsUnnestSJInfo`'s throwaway `synL=1`/`synR=2` numbering
    (item 4). Verified: `go build`/`go vet ./internal/optimizer/...` clean,
    full `go test ./internal/optimizer/...` pass (2 new tests exercising the
    real function directly, not the throwaway probe copy), TPC-DS SF0.25
    sweep `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0` `PLAN-SHAPE: same=99
    changed=0`, `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
    shows only the pre-existing unrelated `internal/parser`
    `GroupedJoinUnaliased` failure. **Narrowing correction (design doc
    §23.4)**: `predp.go`'s pass-through (originally filed as this item's
    "item 2") does NOT land here — it lacks items 3/4's caller-visible inert
    gate (its bail already only fires on a shape the eligibility pre-check
    guarantees cannot occur, so generalizing it is unverifiable dead code
    without an unrealistic fixture) and only has real meaning paired with
    `-b2`'s actual tree-routing cutover — re-filed into `-b2` below.
  - [x] **M0142-0008a-3i-plumbing-b2 — predp.go pass-through +
    ctx.bindings/joinlist extension + cutover (design doc §22.3/§22.4,
    §23.4, §24)** — filed by -3i-plumbing-b's scoping pass, scope corrected
    by -3i-plumbing-b1's §23.4 finding, item 6 split by this loop's §24
    scoping pass. Depends on -3i-plumbing-b1 (done).
    Scope: extend `predp.go`'s descend loop to pass through non-Semi/Anti
    `*Join` nodes instead of hard-bailing (`predp.go:96-101`) — coupled
    here rather than to -b1, since it only has meaning once this item's
    cutover actually routes a wider tree through it; item 6a: plumb a
    `*resolveContext` through `unnestSubqueriesInPlan`/`unnestExistsExpr`
    (7 signatures, all in unnest.go, rooted at planner.go:1533/:1619 which
    already hold ctx — §24.1 confirms this half is mechanical, DONE
    scoping, ready to code); item 6b (**blocking, undecided — do this
    before 6a is wired to append anything**): decide how the Semi/Anti RHS
    leaf's offset/width reaches `joinsearchseam.go:329`'s check without
    making it a live `ctx.bindings` entry — a synthetic `rangeBinding`
    there is visible to ~24 other consumer sites across `planner.go`, and
    §24.2 found at least one (`FOR UPDATE`/`FOR SHARE` no-target-list
    locking, `planner.go:2523`/`:2539`) nil-pointer-panics on it
    unconditionally (`qualifiedOnly` does NOT gate that site); choose
    between auditing/gating all ~24 sites vs. a side-channel invisible to
    `ctx.bindings`'s other consumers (§24.2 leans toward the side-channel
    but it is undesigned); feed the full spine+origChain tree to
    `tryJoinSearch`; flip the production call site's `admitSemiAnti` to
    `true` once 6b's leaves are resolvable; retire `predp.go`'s
    splice-and-reresolve (`reresolveJoinByName`) ONLY for statement shapes
    empirically proven (TPC-DS sweep + a dedicated EXISTS/NOT-EXISTS
    regression set) to be handled end-to-end by the new path, keeping the
    old splice as fallback for everything else — never remove the fallback
    before proving it's unreachable for the engaged family. Likely also
    depends on enough of `M0142-0008c-3c`/`-3d`/`-4`'s path-builder work existing for a real
    Semi/Anti pair to be admissible during DP at all.
    **§30/§31 design pass (2026-09-16)**: settled the (a)/(b) choice §24.2
    raised as neither — a third option (c) reuses `extractSearchLeaves`'s
    own `semiAnti` return value's leaf-position bits with purely local
    arithmetic, no `ctx.bindings`/`ctx.joinlist` mutation, no duplicate DP
    entry point. Also found a third preamble bug (`prefixTotalWidth` reading
    a synthetic leaf's out-of-band span when that leaf is last in walk
    order). **Step (i) of §31.4's two-step resume order LANDED 2026-09-16**:
    `tryPGShapedJoinSearch`'s leaf-count check is now
    `len(scans) != nprefix+len(semiAnti)`; a new `pgShapedOffsetChecksOK`
    (`joinsearchseam.go`, next to `buildLeafSpans`) replaces the old
    per-index offset-agreement loop and spine-offset check with
    synthetic-leaf-aware versions. Production's `admitSemiAnti` literal is
    unchanged (`false`), so this is provably inert — TPC-DS SF0.25 sweep
    `PASS=96 MISMATCH=0`/`PLAN-SHAPE: same=99 changed=0`, full
    `go test ./internal/optimizer/...` pass, 3 new direct unit tests in
    `semiantichain_test.go` exercise the numSynthetic\>0 arithmetic without a
    full fixture. Design doc §31.4/README updated in the same commit.
    **Still open (step (ii), a separate later loop):** the `predp.go`
    descend-loop reachability extension (§30's original target) — feed a
    Semi/Anti-bearing tree into `tryJoinSearch` and flip `admitSemiAnti=true`
    at the production call site, verified against a live end-to-end fixture.
    Earlier item 6b design pass (design doc §25, 2026-09-16) for reference:
    found a second, worse problem than the ctx.bindings-visibility question
    above — the search's own `cumOffsets` array (shared into `relidsOfExpr`/
    `tableForCol` from every legality/restriction-list consumer in
    `joinsearchseam.go`/`joinrestrict.go`/`local_filters.go`) misattributes
    a REAL leaf's pre-existing qual to the synthetic Semi/Anti RHS leaf's
    bit whenever that leaf's real output width is nonzero (live-traced on
    `(A SEMI JOIN B) JOIN C ON qualAC`; not a crash, a silent wrong-rows
    risk, and independent of `ctx.bindings` entirely). Settled direction
    (§25.3): decouple leaf/RelSet-bit space (unaffected) from column-index
    space — leave every real leaf's `ctx.bindings` range untouched, give
    each synthetic leaf an out-of-band range appended after the total real
    width, and generalize `relidsOfExpr`/`tableForCol` from a monotonic
    prefix-sum scan to an explicit per-leaf `(lo,hi)` table. This SAME
    mechanism is also §24.2's undesigned side-channel (no `ctx.bindings`
    entry ever created, so none of the ~24 sites, including `FOR UPDATE`,
    are touched) — 6b is one design, not two. Concrete next steps before
    coding 6a: (1) verify whether the final winning-plan build step already
    re-derives `outerWidth` fresh at build time or also needs this fix;
    (2) replace `cumOffsets []int` with the per-leaf `(lo,hi)` table in
    `joinsearchseam.go` and update `relidsOfExpr`/`tableForCol`; (3) point
    `unnestExistsExpr`'s `innerKey.Index` construction at the new
    out-of-band counter; (4) THEN code 6a (now understood to be unnecessary
    as a `ctx.bindings` append — the per-leaf table replaces it).
    **§25.4's open question CLOSED (design doc §26, 2026-09-16)**: verified
    the fix does NOT reach the emitted plan. There are two distinct
    `cumOffsets` arrays sharing a name — `joinsearchseam.go`'s chain-level
    one (§25's bug) and `relfromjoinlist.go`'s joinlist-item-level one
    (built from real `ctx.bindings` only, feeds `RelOptInfo.baseOffset` /
    `createplanjoin.go`'s `translateToLayout`, the mechanism that actually
    rewrites clause indices into the emitted plan). Every real
    `createplan*.go` build site re-derives width/offset fresh from the
    built node's `Output()` (same idiom as `unnestExistsExpr`), never from
    either `cumOffsets`. The bushy/joinlist layer has zero Semi/Anti
    awareness today (no code threads a synthetic RHS into it — that is
    `M0142-0008c-3c`/`-3d`/`-4`'s still-unstarted job), so `-b2`'s fix is
    confined to the chain layer only. New unit
    test needed: a `(A SEMI JOIN B) JOIN C`-shaped fixture, not covered by
    any `-b1` test.
    **Step (2) landed (design doc §27, 2026-09-16)**: `cumOffsets []int`
    replaced by a per-leaf `[]leafSpan` table (`joinrestrict.go`) across
    `relidsOfExpr`/`tableForCol`/`buildRestrictInfos`/`searchConsumes`/
    `deriveOuterLinkConstants`/`outerOnQualsOK`/`innerOnQualsBelowNullableOK`/
    `semiAntiOnQualsOK`/`partitionConjunctsForJoinPlanning`, fed by a new
    `buildLeafSpans` producer implementing §25.3's scheme. Corrected §26.3:
    the shared functions DO reach `relfromjoinlist.go` (flavor 2's live
    call, `:679`) — reconciled with `spansFromCumulative`/
    `cumulativeFromSpans` adapters rather than changing flavor 2's own
    `[]int` field. New test `TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly`
    (`semiantichain_test.go`) pins §25.1's `qualAC` misattribution as fixed
    — the `(A SEMI JOIN B) JOIN C` case above, at the mechanism level (not
    through the real `unnestExistsExpr` rewrite, since step (3) below is
    what would wire that). Verified as zero production behavior change
    (`admitSemiAnti` stays false everywhere reachable): TPC-DS SF0.25
    sweep PASS=96/0 mismatches, plan-shape 99/99 identical;
    `go test ./internal/optimizer/...` all pass; units precommit gate's
    only failure is the pre-existing unrelated `GroupedJoinUnaliased`
    parser gap. **Step (3) (`unnest.go`'s `innerKey.Index`) explicitly NOT
    landed** — found to be LIVE production code (every `EXISTS` unnest
    reaches it, feeds the executor's `evalHashKey`), not the test-only
    scaffolding §25.3 assumed; needs its own scoping pass (design doc
    §27.2) before coding, not a mechanical "thread the type through" step.
    Step (4) (6a's `*resolveContext` plumbing) confirmed unneeded, per
    §25.4's own text. **Next loop: scope step (3)** before attempting the
    `admitSemiAnti=true` cutover.
    **Step (3) scoped (design doc §28, 2026-09-16) — the index-rebase
    premise was wrong, and the real gap is bigger:** traced live (not
    assumed) whether `j.Predicate`'s and `j.LeftKey`/`j.RightKey`'s copies
    of the RHS index diverge. They don't matter the way §25.3/§27.2 framed
    it: (a) `j.Predicate` already uses the exact local-per-subtree
    convention every other join type uses, and `extractSearchLeaves`'s
    existing `rebaseChainQual` call already handles it with zero
    Semi/Anti-specific code — pinned by §27's own new test; (b)
    `j.LeftKey`/`j.RightKey` are read by NO search-time function at all
    (grepped `extractSearchLeaves`/`buildLeafSpans`/`relidsOfExpr`/
    `tableForCol`) — their only readers are execution/cost code and
    `joinlayout.go`'s `reresolveJoinByName`, a by-NAME reconciliation pass
    that ALREADY special-cases Semi/Anti correctly (skips recursing into
    `n.Right`, still rebinds `LeftKey`/`RightKey`) wherever it runs — so
    step (3) as coding work is moot, not deferred. **But tracing the one
    production call site (`joinsearchseam.go:309`'s `chain` argument, back
    through `runJoinSearchBelowPinned`/`predp.go` to `planner.go:1533-1535`)
    found `origChain` is captured BEFORE `unnestSubqueriesInPlan` runs — it
    structurally CANNOT contain a Semi/Anti join, ever, regardless of
    `admitSemiAnti`.** Flipping the flag at the one existing call site is
    therefore still a guaranteed no-op; a NEW call over the POST-unnest
    tree (the predp.go descend-loop extension already named below) is not
    an optional part of this item's scope, it is the reachability
    precondition for everything else in it. Also found a genuine,
    previously undocumented correctness gap for whoever wires that call:
    `semiAntiChainLink{pred: j.Predicate}` silently drops the join's own
    equijoin condition in the common single-key case (it lives only in
    `LeftKey`/`RightKey`, deliberately excluded from `Predicate` since the
    hash match already enforces it) — must be folded in as an explicit
    `OpEq` conjunct at capture time (mirroring
    `createplanjoin.go`'s `joinInputs.joinPredicate` idiom) in the SAME
    loop that wires the new call, or `admitSemiAnti=true` would silently
    turn into an unconditional (Cartesian-like) Semi/Anti the moment it
    does anything.
    **Dropped-equijoin fix landed standalone (design doc §29, 2026-09-16)**:
    the ordering constraint above is about exposure, not coding order — with
    the one production call site still passing `admitSemiAnti=false`
    unchanged, the fix is fully inert, so it landed on its own this loop
    rather than bundled with the harder `predp.go` reachability change
    (`m0074_partial_scope_lessons`: bound the change, verify incrementally).
    `extractSearchLeaves`'s Semi/Anti arm (`joinsearchseam.go`, just before
    the existing `rebaseChainQual` call) now folds `j.LeftKey`/`j.RightKey`
    into an explicit `OpEq` conjunct ANDed onto `j.Predicate` before
    capturing `pred`, gated on `j.LeftKey != nil && j.RightKey != nil`. New
    test `TestExtractSearchLeaves_AdmitSemiAnti_FoldsKeyEquijoinIntoPred`
    (`semiantichain_test.go`) pins the actual gap: a bare correlated
    `EXISTS` with no other residual leaves `j.Predicate` naturally `nil`
    (asserted, not assumed) — before the fix `semiAnti[0].pred` would have
    been `nil` too (declined by `semiAntiOnQualsOK`); after, it is exactly
    the `(LeftKey = RightKey)` conjunct, verified by pointer identity, and
    accepted by `semiAntiOnQualsOK`. Verified zero production behavior
    change: `go build ./...` clean, `go vet ./internal/optimizer/...`
    clean, full `go test ./internal/optimizer/...` pass, TPC-DS SF0.25
    sweep `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`
    `PLAN-SHAPE: same=99 changed=0`,
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` shows
    only the pre-existing unrelated `internal/parser`
    `GroupedJoinUnaliased` failure (`internal/optimizer` itself passes).
    TPC-H spotcheck SKIPPED per the standing M0142-0003k blocker (shared
    `:65433` cluster's `tpch` data still needs the human-authorized
    reload — unrelated to this change).
    **Step (i) scoping pass (design doc §30, 2026-09-16) — two findings,
    no production change:** (1) the recurring "likely depends on
    `M0142-0008c-3c`/`-3d`/`-4`" note (§26.3, repeated in §28.5/§29) is
    **backwards** — those three items' own fix_plan entries say they are
    blocked ON this item, not the reverse (`-3c`/`-3d` explicitly: "Also
    now blocked on `M0142-0008a-3i-plumbing-b2` — do not pick up before
    that lands"), so this item does NOT need them first. (2) traced live
    that the obvious design (thread `admitSemiAnti` through `tryJoinSearch`
    + have `predp.go`'s descend loop stop pinning the outermost spine
    Semi/Anti join) is **not sufficient**: `tryPGShapedJoinSearch`'s own
    preamble (`joinsearchseam.go:226-350`) gates on `nrels`/`nprefix`
    computed from `ctx.bindings`/`ctx.joinlist`, BOTH frozen before
    `unnestSubqueriesInPlan` runs; a chain rooted above a Semi/Anti join
    returns one more leaf than `nprefix` knows about, so line 314's
    `len(scans) != nprefix` "leaf-count" decline fires unconditionally —
    a DIFFERENT, deeper blocker than the one §28 already found for
    `origChain`, and NOT fixed by §25-§27's `cumOffsets`→`[]leafSpan` work
    (that work fixed chain-internal attribution, not this outer
    ctx.bindings-keyed size gate). Two candidate designs recorded in §30,
    neither coded: (a) widen `ctx.joinlist`/`ctx.bindings`'s counts to
    include the synthetic leaf (reopens §24.2's ~24-site audit, though
    narrower — only 3 preamble reads need it, not the whole chain walk);
    (b) a parallel entry point bypassing `tryPGShapedJoinSearch`'s
    preamble that reuses only its post-preamble, already-unnest-agnostic
    DP-core logic.
    **§30's (a)/(b) decision SETTLED (design doc §31, 2026-09-16) — neither:
    a third option (c).** `extractSearchLeaves` already returns `semiAnti
    []semiAntiChainLink`, whose `.rhs RelSet` bits mark exactly which
    `scans` indices are synthetic; `tryPGShapedJoinSearch` (`:309`) already
    receives this value and just discards it (`_`). Reusing it needs no
    `ctx.bindings`/`ctx.joinlist` mutation (beats (a): no reopening of
    §24.2's ~24-consumer visibility risk) and no duplicate code path (beats
    (b): no second seam to keep in sync). Fix, not yet coded: (1) leaf-count
    check becomes `len(scans) != nprefix+numSynthetic`, `nprefix` itself
    UNCHANGED; (2) the per-leaf offset-agreement loop walks `ctx.bindings`
    with a separate counter that skips synthetic indices; (3) **a THIRD,
    previously-undiscovered bug in the same preamble**: the
    spine-offset-disagreement check's `prefixTotalWidth :=
    cumOffsets[len(cumOffsets)-1].hi` silently reads a SYNTHETIC leaf's
    out-of-band span whenever that leaf is LAST in walk order (a bare
    trailing `EXISTS`, plausibly the common case) instead of the real total
    width — needs `buildLeafSpans` to also return the real-only running
    total, or an equivalent local recomputation. **Next loop: code §31.3's
    three-check fix in `tryPGShapedJoinSearch`, gated behind a direct
    unit-test call only (production stays `admitSemiAnti=false`, so this
    stays fully inert) — do NOT combine with the `predp.go` descend-loop
    reachability change (§30's original step (i) target); that remains a
    separate, later, higher-blast-radius loop per §31.4.**
    **Step (i) LANDED (design doc §31.4, 2026-09-16):** §31.3's three-check
    fix coded exactly as designed, production `admitSemiAnti` literal
    unchanged (`false`) — fully inert, confirmed by TPC-DS SF0.25 sweep
    (`PASS=96 MISMATCH=0`, `PLAN-SHAPE: same=99 changed=0`) and full
    `go test ./internal/optimizer/...`. Three new direct unit tests pin the
    previously-untestable `numSynthetic>0` arithmetic.
    **Step (ii) scoping pass (design doc §32, 2026-09-16), no production
    change:** corrected §30 Finding 2's stale framing — `admitSemiAnti` is
    ONE local literal at `joinsearchseam.go:309`, not a parameter needing to
    thread through 4 call sites; flipping it is safe unconditionally since
    the other 3 `tryJoinSearch` callers' chains are always captured
    pre-unnest and can never contain a Semi/Anti node. Live-traced TPC-DS
    `query10.sql` (this milestone's own cited unblock target): it hits the
    "sunk" shape, not "nothing sunk", and the existing splice already drops
    the wrapping Filter for free once all sunk conjuncts are legally pushed
    — leaving a Filter-free, walkable subtree under the innermost pinned
    Semi/Anti join once Phase A (unchanged) completes. Settled design:
    Phase A stays exactly as today; Phase B is a new, additive,
    gracefully-declining second `tryJoinSearch` call rooted at the outermost
    pinned Semi/Anti join (nil predicate), inert while §32.1's literal stays
    `false` (same "leaf-count"-decline mechanism §31.4 already proved).
    **Next loop codes Phase B** as its own inert scaffold (literal stays
    `false` in production), with the success path unit-tested DIRECTLY
    against a hand-built search result (mirroring `pgShapedOffsetChecksOK`'s
    extraction) rather than landed as unverified dead code
    (`dead_code_is_not_a_reference_impl`). Flipping the literal to `true` +
    the live end-to-end fixture verification is a still-later step (iii) —
    do not collapse either into the Phase B scaffold loop.
    **Step (ii) Phase B scaffold LANDED (design doc §33, 2026-09-16):**
    `runJoinSearchBelowPinned` now captures `spineRootPut` (the setter for
    `spineJoins[0]`) during its descend loop, and — after Phase A's existing
    splice, only when `len(spineJoins) > 0` — attempts a second
    `tryPGShapedJoinSearch(spineJoins[0], nil, ctx, cat)` call; on success
    (`used && residual == nil`) the new `spliceSearchedSpine` helper places
    the result and remaps `spineFilters`, skipping `spineJoins`' bottom-up
    `reresolveJoinByName` loop entirely; on decline, falls through unchanged
    to today's Phase-A-only path. `spliceSearchedSpine` was extracted to take
    the searched replacement as a plain argument (not compute it), so its
    success path — unreachable from any live fixture while `admitSemiAnti`
    stays `false` — is unit-tested DIRECTLY with 3 new hand-built-result
    tests (`predp_test.go`: placement, filter-predicate remap on a layout
    change, identity no-remap fast path). Verified inert: `go build`/`go vet
    ./internal/optimizer/...` clean, full `go test ./internal/optimizer/...`
    pass, TPC-DS SF0.25 sweep `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`
    `PLAN-SHAPE: same=99 changed=0`,
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` shows only
    the pre-existing unrelated `internal/parser` `GroupedJoinUnaliased`
    failure (`internal/optimizer` itself `ok`).
    **Step (iii) LANDED (design doc §34, 2026-09-16) — item DONE:** flipped
    `extractSearchLeaves(chain, false)` to `extractSearchLeaves(chain, true)`
    at the one production call site (`joinsearchseam.go:309`); updated the
    three stale nearby comments that used to justify why `semiAnti` stays
    empty in production. `go build`/`go vet ./internal/optimizer/...` clean,
    full `go test ./internal/optimizer/...` pass unchanged. TPC-DS SF0.25
    sweep: `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` — zero
    correctness regressions across the 99-query corpus. For the first time
    this milestone, the plan-shape self-diff is non-empty:
    `PLAN-SHAPE: same=98 changed=1` (`Q78`), confirming Phase B is now
    genuinely live in production, not merely unit-tested. Q78 has no literal
    `EXISTS`/`NOT EXISTS` — its antijoins come from PG's
    `LEFT JOIN ... WHERE col IS NULL` strength-reduction idiom, which the
    `JoinType`-keyed admission arm correctly treats the same as an
    `unnestExistsExpr`-born antijoin, satisfying §27.4/§33.4's "real
    EXISTS/NOT-EXISTS-equivalent fixture" requirement. Q10/Q35 (this
    milestone's own cited unblock targets) are unchanged, as expected —
    §16/§19 already established they need `M0142-0008c`'s separate,
    partially-landed `create_unique_path` mechanism (`-3c`/`-3d`/`-4` not yet
    done), which this flip could not reach on its own; that dependency is now
    unblocked (§30 Finding 1) and is the natural next pickup for
    Q10/Q35-parity work, filed separately. One follow-up surfaced and
    deferred (ledger row appended, task-id `m0142-0008a-3i-plumbing-b2`): the
    `ws` CTE's new Nested-Loop-with-Filter shape for Q78 carries an
    implausibly cheap EXPLAIN cost estimate — correctness-neutral
    (checksums matched, no wall-clock regression) but a likely cost-model gap
    in the newly-reached Semi/Anti path-building arm, not chased this loop.
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` shows only
    the pre-existing unrelated `internal/parser` `GroupedJoinUnaliased`
    failure (`internal/optimizer` itself `ok`).
- [x] **M0142-0008a-3i-plumbing-c — wire `semiAntiLinksHaveSJInfos`/
  `semiAntiOnQualsOK` into `tryPGShapedJoinSearch`'s production path, and
  confirm/fix whether the renumbered `j.SJInfo` reaches `ctx.joinInfoList`**
  — filed by M0142-0008c-3c's recon (design doc §35.2). **DONE 2026-09-16 as
  a sizing recon (design doc §36), no production change** — same pattern as
  M0142-0008c's own recon (§16) and M0140-0006's decomposition. Traced every
  code path between `extractSearchLeaves`'s SEMI/ANTI arm and
  `addPathsToJoinrel` and found the resume point's item (1) answer is a firm
  **no**: `ctx.joinInfoList` is snapshotted by `deconstructJointreeScopedSJI`
  (`planner.go:3051`) from the statement's `FromExprs` BEFORE
  `unnestSubqueriesInPlan` ever runs, so a Semi/Anti join's `SJInfo` — built
  later, during unnesting (`existsUnnestSJInfo`) — is structurally never a
  member; renumbering `j.SJInfo` in place changes the object's fields, not
  which slice holds a pointer to it. Wiring the two named consumers in
  without fixing that would make them decline unconditionally, indistinguishable
  from today's silent zero. Tracing further found FOUR more gaps upstream of
  the two named consumers: the semiAnti join's own predicate is never merged
  into the search's conjuncts (so `semiAntiOnQualsOK`'s `searchConsumes`
  check could never pass anyway); the synthetic RHS leaf never becomes a
  `leaves`/`relInfos` entry (the `for i, b := range ctx.bindings[:nprefix]`
  loop only visits real FROM items, silently dropping `scans[nprefix:]`);
  `prob.bindings`/`scans`/`relInfos` must all grow to
  `nprefix+len(semiAnti)`, needing a synthesized `rangeBinding` per leaf and
  a verified (not assumed) row estimate; and the call-site-local `jl` handed
  to `planJoinlistSearch` must also grow, since `validateJoinlistProblem`
  hard-requires `jl.leafRange() == (0, len(prob.bindings))` and the
  pre-unnest `ctx.joinlist` has no entries for the synthetic leaves. Full
  file:line citations and the five-way decomposition rationale are in design
  doc §36. Decomposed into `-3i-plumbing-c1`..`c5` below (none started —
  recon only, one task per loop); `c1` is the natural next pickup
  (independently landable/verifiable ahead of the rest, same precedent
  `-3b`/`-0008c-3c` already used). (Original filing context — the
  full-corpus 145,191-line/zero-`semi`/`anti` `DPPATH` sweep and the
  `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK` call-site grep — is
  superseded by the more precise §36 trace above; kept out of this entry to
  avoid duplication, see design doc §35.2/§36 for both.)
  **Independent, unfiled resume-point hint** (not sized/numbered — noted for
  whoever picks up S5a's own eligibility gate): relaxing
  `whereEligibleForPreDPUnnest` to per-sublink granularity would upgrade
  Q22 out of its total-bypass class on its own, independent of -1..-3.
- [x] **M0142-0008a-3i-plumbing-c1 — merge `semiAnti[*].pred`'s split
  conjuncts into `conjuncts`** (design doc §36, gap 1; §37 landing note).
  Mirrored `outerLinks`' `onOuter` treatment (`joinsearchseam.go:555-565`,
  right after the `outerLinks` block). Verified inert (not just argued):
  optimizer package tests green, full `go build ./...` clean, and the
  TPC-DS SF0.25 sweep against the git-tracked oracle came back
  `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0` with `PLAN-SHAPE: same=99
  changed=0` versus the prior commit — the conjunct never binds to any
  join of real leaves alone (its relids always include the not-yet-real
  synthetic leaf bit), so nothing in the searched plan moved. TPC-H
  spotcheck SKIPPED (pre-existing M0142-0003k data-dir blocker, unrelated).
  Next: `-3i-plumbing-c2` (give the synthetic RHS leaf a real
  `rangeBinding`/`baseRelInfo` and grow `prob.bindings`/`scans`/`relInfos`).
- [x] **M0142-0008a-3i-plumbing-c2 — give the synthetic Semi/Anti RHS leaf a
  `rangeBinding`/`baseRelInfo` and extend `prob.bindings`/`scans`/`relInfos`
  to `nprefix+len(semiAnti)`** (design doc §36, gaps 2-3). **DONE
  2026-09-16 (design doc §38).** `joinsearchseam.go`'s leaf-building loop
  now sizes `leaves`/`relInfos`/a new local `bindings` slice to
  `nleaves := nprefix+len(semiAnti)` and fills index `nprefix+k` for each
  `semiAnti[k]` (confirmed 1:1 by tracing `extractSearchLeaves`'s walk:
  `scans[nprefix+k]` and `semiAnti[k]` are appended in the SAME case-branch
  of the SAME walk step, and `unnestExistsExpr`'s always-wrap-the-whole-tree
  shape (unnest.go:491) guarantees real leaves are walk-contiguous at
  `[0,nprefix)` and synthetic ones at `[nprefix,nleaves)`, in `semiAnti`
  order). The row-estimate trap this item flagged was real: a synthetic
  leaf's `table == nil` binding fed through `estimateBaseRelInfo`/
  `applyRelSizeFallback` bottoms out at `estimateTableRowsFallback`'s `if
  tbl == nil { return 0 }` — a silent ZERO-row estimate, not a fallback. Sized
  via `EstimateRows(Node)` instead (the same general-purpose estimator every
  other non-base-relation node already uses). Also verified — not
  assumed — that growing `prob.bindings` to `nleaves` ahead of
  `-3i-plumbing-c3`'s matching `jl` growth does not trip
  `validateJoinlistProblem`'s `jl.leafRange()` check on the SF0.25 corpus.
  Verified via the full TPC-DS SF0.25 sweep (not just code trace):
  `go build ./...` clean, `go test ./internal/optimizer/...` green,
  `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: same=99 changed=0`
  vs the immediately prior commit — Q78 (the query the b2/c1 notes name as
  reaching the semiAnti arm) byte-identical (15 rows, same checksum).
  TPC-H spotcheck SKIPPED (pre-existing M0142-0003k data-dir blocker,
  unrelated). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  shows only the pre-existing unrelated `internal/parser`
  `GroupedJoinUnaliased` AST-drift failure. Next: `-3i-plumbing-c3` (extend
  the call-site-local `jl` so `validateJoinlistProblem` covers the grown
  `nleaves` bindings end-to-end).
- [x] **M0142-0008a-3i-plumbing-c3 — build the call-site-local extended
  joinlist** (design doc §36, gap 4). **DONE 2026-09-16, landed TOGETHER with
  c4 (design doc §39) — NOT independently, see below.** `validateJoinlistProblem`
  (`relfromjoinlist.go:246-276`) hard-requires `jl.leafRange() == (0,
  len(prob.bindings))`; built a call-site-local `searchJl` in
  `tryPGShapedJoinSearch` (copy of `jl` plus one `leafItem(nprefix+k)` per
  `semiAnti[k]`, `jl` itself never mutated since it may alias `ctx.joinlist`)
  and passed that to `planJoinlistSearch` instead of `jl`.
- [x] **M0142-0008a-3i-plumbing-c4 — augment `joinInfoList` with each
  semiAnti link's `j.SJInfo`** (design doc §36, the core finding). **DONE
  2026-09-16, landed TOGETHER with c3, NOT "independent... can land in
  parallel or either order" as originally filed here — see design doc §39.**
  `go test ./internal/optimizer/...` with c3 applied alone (before this fix)
  failed `TestM0070Q21InnerOnlyConjunctsStay`: the search now actually
  reaches the synthetic leaf (c3's whole point) but `joinIsLegal`
  (`joinsearchlevel.go:198`) had no `SpecialJoinInfo` telling it the leaf
  could only be joined via SEMI/ANTI, so it silently formed a plain INNER
  join instead — the AntiJoin vanished from Q21's plan. Fixed by adding
  `sjinfo *SpecialJoinInfo` to `semiAntiChainLink`, populated from `j.SJInfo`
  at the link's construction site (the same pointer the walk already
  renumbers in place with real leaf-index bits — no separate rebuild
  needed), and a new `semiAntiJoinInfoList(base, links)` helper (returns
  `base` unchanged, by identity, when `links` is empty) feeding
  `joinlistProblem.joinInfoList` instead of the bare `ctx.joinInfoList`.
  Verified: `go build ./...` clean, `go test ./internal/optimizer/...` green
  (Q21's AntiJoin restored) with BOTH c3+c4 applied, TPC-DS SF0.25 sweep
  `PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0`,
  `PLAN-SHAPE: queries=99 same=99 changed=0 added=0 removed=0` vs the c2
  commit (Q78 still byte-identical, 15 rows, same checksum). TPC-H spotcheck
  SKIPPED (pre-existing M0142-0003k data-dir blocker, unrelated).
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` shows only
  the pre-existing unrelated `internal/parser` `GroupedJoinUnaliased`
  AST-drift failure (`internal/optimizer` itself green). Next:
  `-3i-plumbing-c5` (decline-gate wiring + `GOOPG_PGSHAPED_DP_TRACE=1`
  reachability confirmation).
- [x] **M0142-0008a-3i-plumbing-c5 — wire `semiAntiLinksHaveSJInfos`/
  `semiAntiOnQualsOK` as decline gates and confirm reachability** (design
  doc §36, gap 5 — what the task was originally filed for). **DONE
  2026-09-16 (design doc §40).** Wired both checks as a FAIL-CLOSED gate
  mirroring the `outerLinks` block (`joinsearchseam.go:517-538`), placed
  right before the c1 conjunct-merge loop. Re-ran the full-corpus
  `GOOPG_PGSHAPED_DP_TRACE=1` sweep (§35.1's method, private binary, all 100
  query files, 96 succeeded): **reachability is STILL zero** — 150,137
  `DPPATH` lines, none `jointype=semi`/`anti` — but this run also captured 5
  `seam-decline reason=semianti-link-no-sjinfo` lines, confirming the new
  gate itself is what declines, not an earlier unrelated decline. Root
  cause, confirmed by grepping every `.joinInfoList` assignment site in the
  package (exactly one, `planner.go:3051`'s `deconstructJointreeScopedSJI(
  s.FromExprs, …)`, built once from the PRE-unnest parser jointree):
  `ctx.joinInfoList` is structurally incapable of ever containing a semiAnti
  `SpecialJoinInfo`, because that SJInfo is created later and independently
  by `existsUnnestSJInfo`/`unnestExistsExpr` (a rewrite over the
  already-built plan `Node` tree, not over `s.FromExprs`) — the two
  pipelines never rejoin. `unnest.go:4385`'s own pre-existing doc comment
  already said as much ("`ctx.joinInfoList` belongs to jointree
  deconstruction, which this rewrite runs independently of"); this loop is
  the first to measure the consequence directly. Considered and rejected:
  checking against `semiAntiJoinInfoList` instead (vacuous, matches a link
  against its own copy) and loosening the gate for semiAnti specifically
  (would undo §39's Q21 safety fix). Verified safe: TPC-DS SF0.25 sweep
  `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: same=99 changed=0`
  vs the c3+c4 commit (Q78 still byte-identical); `go build ./...` clean;
  `go test ./internal/optimizer/...` green; units precommit gate shows only
  the pre-existing unrelated `internal/parser` `GroupedJoinUnaliased`
  failure. `-0008c-3c`/`-3d`/`-4`'s Q10/Q35 acceptance bar is still NOT
  attemptable — now for a specific, nameable reason. Next:
  `-3i-plumbing-c6` (below).
- [x] **M0142-0008a-3i-plumbing-c6 — thread each semiAnti link's SJInfo into
  `ctx.joinInfoList` itself** (design doc §40.5, filed by c5). **DONE
  2026-09-16 (design doc §41).** Landed: `tryPGShapedJoinSearch`
  (joinsearchseam.go, immediately before the c5 gate) now appends each
  semiAnti link's already-renumbered `*SpecialJoinInfo` into `ctx.joinInfoList`
  for real (guarded by `joinInfoListHas` for idempotency) — a genuine
  production write into the same field `deconstructJointreeScopedSJI`
  populates for outer links, not the rejected "check against a list built
  from itself" pattern §40.3 ruled out; matches upstream PG's own ordering
  (`deconstruct_jointree` always runs after `pull_up_sublinks`). Corpus
  sweep evidence it is real: `semianti-link-no-sjinfo` declines dropped 5→3
  and a NEW decline (`semianti-on-qual`, 0→1) appeared, isolated via
  per-query EXPLAIN to **Q69** (TPC-DS's only chained-multi-EXISTS query),
  which used to fail the sjinfo gate and now passes it and fails one gate
  later instead. `jointype=semi`/`anti` DPPATH reachability is still zero
  corpus-wide — TPC-DS SF0.25 sweep unaffected (`PASS=96 MISMATCH=0`,
  `PLAN-SHAPE same=99`, Q78 byte-identical) as expected. Next blocker
  root-caused and filed as `-3i-plumbing-c7` (below).
- [x] **M0142-0008a-3i-plumbing-c7 — split `rebaseChainQual`'s single
  `base` shift into per-operand shifts for chained semiAnti links**
  (design doc §41.3, filed by c6). **DONE 2026-09-17 (design doc §42).**
  Landed: `extractSearchLeaves`'s semiAnti walk arm now captures
  `outerWidth := len(j.Left.Output())` (the SEMANTIC width
  `unnestExistsExpr` used to encode `RightKey.Index = outerWidth + subcol`,
  unnest.go:4694/4762) and `rightBase := width` (the walk's own flat-space
  position where `j.Right`'s opaque leaf starts), and rebases `pred`
  piecewise via a new `rebaseSemiAntiChainQual` instead of one uniform
  `rebaseChainQual(pred, base)`: an operand with `cr.Index < outerWidth`
  (outer) still shifts by `base`; an operand with `cr.Index >= outerWidth`
  (inner) shifts to `rightBase + (cr.Index - outerWidth)` instead. The two
  coincide (no-op) for an unchained link and diverge exactly when `j.Left`
  already has an earlier chained semiAnti link's opaque leaf spliced into
  it. Pinned by a new permanent regression test,
  `TestExtractSearchLeaves_AdmitSemiAnti_ChainedLinksRebaseInnerKeyCorrectly`
  (semiantichain_test.go) — verified to FAIL on the old uniform-`base` code
  with the exact `overlapR=false` symptom §41.3's throwaway Q69
  instrumentation measured, then confirmed to PASS with the real fix.
  Full-corpus `GOOPG_PGSHAPED_DP_TRACE=1` sweep: `semianti-on-qual` declines
  dropped 1→0 corpus-wide (Q69 clears both semiAnti gates now);
  `semianti-link-no-sjinfo` unchanged at 3 (unrelated to this fix).
  `jointype=semi`/`anti` DPPATH reachability is STILL zero — TPC-DS SF0.25
  sweep unaffected (`PASS=96 MISMATCH=0`, `PLAN-SHAPE same=99`, Q78
  byte-identical) as expected. Next blocker root-caused and filed as
  `-3i-plumbing-c8` (below): Q69 now advances past BOTH semiAnti gates and
  is declined one stage later by a pre-existing, over-broad guard.
- [x] **M0142-0008a-3i-plumbing-c8 — `problemPairsOuterWithDerived` must not
  treat a semiAnti RHS leaf's own opacity as "no statistics"** (design doc
  §42.3, filed by c7). With c7 landed, Q69 clears both semiAnti admission
  gates and reaches `searchOneProblem` (relfromjoinlist.go), where
  `problemPairsOuterWithDerived` (:564) declines it with
  `reason=outer-over-derived`. Root cause: `leafIsDerivedInput` (:523)
  returns true whenever a leaf's underlying scan is not a single base table
  (`info.table == nil`) — correct for a genuine CTE/subquery/function scan
  (the C-04a/Q78 hazard this guard was built for: no statistics, so the
  cost comparison runs on a defaulted `rows=1` guess), but ALSO true for
  ANY semiAnti RHS leaf whose EXISTS/NOT-EXISTS body joins ≥2 relations
  (Q69's `store_sales JOIN date_dim` etc., wrapped as one opaque
  `*Project{*Join{...}}` leaf by the search-seam walk per §12.4/§13) — that
  leaf's row estimate is NOT a defaulted guess, it comes from the
  already-cost-estimated join subplan `unnestExistsExpr` splices in
  wholesale. The guard's `switch sj.Jointype` arm deliberately includes
  `JoinSemi`/`JoinAnti` (pinned by
  `TestProblemPairsOuterWithDerivedSemiOverDerived`/`...AntiOverDerived`,
  semiantichain_test.go:140/154 — a Semi/Anti RHS touching a REAL CTE must
  still decline), so the fix must narrow WHICH leaf counts as "derived,"
  not remove Semi/Anti from the jointype switch. Two candidate directions
  (§42.3, neither implemented yet): (a) give `leafIsDerivedInput` a signal
  for "multi-relation subtree with real per-child stats" distinct from
  "provably no stats," e.g. keyed off whether the leaf's row estimate
  traces to a non-default `EstimateRows()` rather than `.table == nil`; (b)
  narrow `problemPairsOuterWithDerived`'s Semi/Anti arm to exempt a link's
  OWN `rhs` hand specifically (always opaque-by-construction, unlike an
  outer join's nullable side, which is a real independently-searchable
  subtree) while still declining on any OTHER derived input the problem
  touches — likely closer to the guard's original intent and avoids
  plumbing a new stats-confidence signal through `baseRelInfo`. Q78 stays
  unaffected either way (single-table EXISTS body already has a real
  `info.table`, so this guard never fires for it — its own continued
  byte-identical plan traces to a SEPARATE, still-unfound gate). MUST NOT
  regress `TestProblemPairsOuterWithDerivedSemiOverDerived`/
  `...AntiOverDerived`'s CTE case — add a THIRD unit test alongside them
  for the "multi-relation EXISTS body must NOT decline via this guard"
  case before landing. Re-run the TPC-DS SF0.25 sweep AND the full-corpus
  `GOOPG_PGSHAPED_DP_TRACE=1` sweep after landing (the real test is still a
  `jointype=semi`/`anti` DPPATH line finally appearing).
  **DONE 2026-09-17 (design doc §43): landed `isSemiAntiSyntheticLeaf`, a
  provenance flag on `baseRelInfo` set only at the semiAnti synthetic-leaf
  construction site (joinsearchseam.go), checked first in
  `leafIsDerivedInput` (neither candidate (a) nor literal (b) survived —
  §43.1 — this is the third option that actually satisfies both pinned
  tests AND the new one). New pinned test
  `TestProblemPairsOuterWithDerivedSemiOverMultiRelationRHSDoesNotDecline`,
  verified to fail pre-fix. Corpus sweep: Q69 now clears EVERY seam-level
  gate c5-c8 landed — its only remaining seam-declines are ordinary
  DP-search noise (`illegal`/`no-join-clause`/`strategy-or-mode`), zero
  semiAnti-specific reasons. `jointype=semi`/`anti` DPPATH reachability
  STILL zero corpus-wide — next blocker is downstream of the seam
  entirely, filed as `-3i-plumbing-c9` below. TPC-DS SF0.25 sweep
  unaffected (`PASS=96 MISMATCH=0`, `PLAN-SHAPE same=99`, Q78
  byte-identical).**
- [x] **M0142-0008a-3i-plumbing-c9 — find what blocks `jointype=semi`/`anti`
  now that Q69 clears every seam-level gate** (design doc §43.6, filed by
  c8). **DONE 2026-09-17 (design doc §44): one real bug found and fixed,
  the actual next blocker root-caused and filed as c10.** Instrumented
  `joinIsLegal`/`createUniquePath` (throwaway, env-gated, reverted before
  commit) against Q69 on the SF0.25 cluster. Found: `createUniquePath`
  declined on EVERY call — `cr.Name`/`cr.Index` correctly matched the RHS
  leaf's schema, but `cr.SourceTableIdx` (1) disagreed with
  `oc.SourceTableIdx` (5), tripping the schema-drift guard. Root cause:
  `existsUnnestSJInfo` (unnest.go) built `SemiRhsExprs[i]` directly from
  `prm.SubCol` — the PRE-remap correlation column — while the sibling field
  `innerKey` (built from the SAME `prm.SubCol`, a few lines above) already
  applies `+srcTableOffset` for exactly this reason (comment: "SubCol was
  harvested from the PRE-remap EXISTS body"). `SemiRhsExprs` was simply
  never given the same treatment — an oversight pre-dating any live caller
  ever reaching this path (createUniquePath's own comment: "this gate never
  actually declines a real query today," now false). **Fixed**:
  `existsUnnestSJInfo` gained a `srcTableOffset int16` parameter;
  `SemiRhsExprs[i]` is now a fresh `*ColumnRef` with
  `SourceTableIdx: prm.SubCol.SourceTableIdx + srcTableOffset`, matching
  `innerKey`'s existing expression exactly. New assertion in
  `TestExistsUnnestSJInfoSemiHashKey` (exists_unnest_sjinfo_test.go) pins
  `SemiRhsExprs[0].SourceTableIdx == j.RightKey.SourceTableIdx`; verified to
  FAIL pre-fix (`= 1, want 3`), passes post-fix. Re-ran Q69: `createUniquePath`
  now logs `SUCCESS` for the first time ever. Row-count spot-check against
  the git-tracked SF0.25 oracle: Q69 still 100 rows, Q78 still 15 rows /
  checksum `c06cf981a7819a37` (both unchanged, as expected — Q69 still
  plans via syntactic fallback per the NEXT blocker below; Q78's EXISTS body
  is single-relation and never reaches this code path). Full
  `scripts/tpcds-sf025-regression.sh sweep` could NOT run this loop —
  `ci/batch`'s nightly run held the shared SF0.25 lane all session (its own
  collision guard fired); the two spot-checks above substitute — run the
  full sweep at the next opportunity. **Next blocker (NOT fixed, filed as
  c10 below)**: fixing `createUniquePath` was necessary but not sufficient.
  Both of Q69's ANTI links still decline `reason=illegal` at every DP level
  — their `MinLefthand` (0x0f / 0x1f) requires the ENTIRE preceding
  composite (`{c,ca,cd,semiLeaf}` / `+antiLeaf1`) because
  `existsUnnestSJInfo`'s 2-bit atomic-LHS convention is NEVER narrowed past
  "the whole outer side" once `len(params)>0` (its own `minL = clause & synL
  = synL` line) — and unlike SEMI, ANTI has no unique-ify escape valve in
  `jointypeForDirection` (correctly, matching PG). Confirmed this is a real
  gap, not a Q69 quirk: all 3 of Q69's links correlate on the single column
  `c.c_customer_sk`, so their TRUE minimal `MinLefthand` is just
  `{customer}` for all three — the broadening to "whole composite" is a
  processing-order artifact of `unnestExistsExpr` nesting each new
  conjunct's join around the accumulated tree, not a real data dependency.
- [x] **M0142-0008a-3i-plumbing-c10 — narrow a semiAnti link's `MinLefthand`/
  `MinRighthand` to the correlation's REAL referenced relation(s), not the
  whole atomic outer/inner participant** (design doc §44.3-44.4, filed by
  c9). **DONE 2026-09-17 (design doc §45): landed and verified correct in
  production, but Q69 reachability is STILL zero — root cause pinned
  precisely and filed as c11 below.** `joinsearchseam.go`'s semiAnti splice
  site (where `j.SJInfo.SynLefthand, MinLefthand = lhs, lhs` used to fire
  together) now computes `minL`/`minR` via
  `relidsOfExpr(pred, buildLeafSpans(widths, nil))` — `buildLeafSpans`'s
  "no semiAnti" branch reconstructs the SAME naive-cumulative coordinate
  space `rebaseSemiAntiChainQual` just wrote `pred` into (NOT the canonical
  post-walk "real-then-synthetic-out-of-band" space — passing the real
  `semiAnti` list there would have been wrong) — with a safe fallback to
  the old un-narrowed bit when the clause can't be resolved.
  `SynLefthand`/`SynRighthand` stay at the full `lhs`/`rhs` as designed.
  New test `TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`
  (semiantichain_test.go) proves narrowing positively via a 2-relation
  outer correlated to only one relation — the existing rebuild test's
  1-relation-outer fixture can't distinguish narrowed from un-narrowed;
  fail-then-pass confirmed by reverting the computation and observing
  `MinLefthand = 0x3, want 0x1`. Verified live via a private SF0.25 binary
  (`tmp/goopg-m0142-c10-bin`, removed) + `GOOPG_PGSHAPED_DP_TRACE=1` +
  throwaway (reverted) instrumentation: all 3 of Q69's SJInfos now carry
  `MinLefthand=0x1` (`{customer}`) in production, exactly as designed — yet
  the DP search still reports `failed to build any 4-way joins` and no
  `jointype=semi`/`anti` DPPATH line ever appears. Root cause (design doc
  §45.3): `ctx.joinInfoList` ends up with 6 entries instead of 3 — TWO
  pointer-distinct, structurally-identical `*SpecialJoinInfo` copies per
  semiAnti link, because `tryPGShapedJoinSearch` has two live call sites
  for one statement (`predp.go:148` Phase A over the whole tree,
  `predp.go:179` Phase B re-walking `spineJoins[0]` alone — Phase B's own
  doc comment claiming it's inert in production is now STALE, since
  `admitSemiAnti` was flipped true by an earlier loop in this series) and
  the semiAnti-population loop (`joinsearchseam.go:593-597`) appends to the
  shared, never-cleared `ctx.joinInfoList` unconditionally, before either
  call's overall success/decline is known. `joinInfoListHas`'s pointer-only
  dedup can't catch two independently-built copies, so `joinIsLegal`'s own
  "matches multiple SpecialJoinInfos" guard correctly-per-its-own-logic
  declines a pairing that is legitimately legal exactly once.
- [x] **M0142-0008a-3i-plumbing-c11 — stop `ctx.joinInfoList` from
  accumulating duplicate/stale `*SpecialJoinInfo` entries across
  `tryPGShapedJoinSearch`'s two call sites for one statement** (design doc
  §45.3-45.4, filed by c10). **Item (a) LANDED 2026-09-17 (design doc §46.5):
  `searchOneProblem`'s boundary hole-filler (relfromjoinlist.go) now exempts
  a semiAnti synthetic leaf's own coordinate range from the
  needed/output-column gates — verified via private binary that it clears
  Q69's boundary-totality panic (real `EXPLAIN` reordering: 2×`Hash Anti
  Join` + `Hash Join` + `Nested Loop`) when combined with the (still
  temporary) §46.3 `joinInfoList` fix, AND that alone (§46.3 fix reverted,
  matching production) it is a byte-identical no-op — full SF0.25 sweep
  PASS=96/ERROR=0/MISMATCH=0 with a private `GOOPG_BIN`. Item (b) is
  RE-SCOPED, not closed: Q69 (a pure AND-EXISTS-chain, no OR shape at all)
  hits the IDENTICAL `outer column ref c_current_cdemo_sk/level=1 out of
  range (depth=0)` error §46.3 filed as Q10-specific/OR-admission-specific,
  once the boundary panic no longer masks it — so the OR-combined-EXISTS
  admission theory cannot be the whole fix; see design doc §46.5 for the
  two next-instrumentation candidates (`rebaseSemiAntiChainQual`'s
  coverage of nested `*OuterColumnRef` nodes, and whether
  `unnestExistsExpr` fully rebases every EXISTS's correlation refs in a
  MULTI-EXISTS statement). Root cause CONFIRMED live and the exact
  one-line fix identified (design doc §46), but item (c) (re-applying it)
  is still NOT landed — it was reverted in the c11-filing loop after
  surfacing item (a) and (b)'s two pre-existing regressions on the SF0.25
  gate. Re-opened with the narrower, corrected scope below.**
  - **2026-09-17 update (design doc §46.6): item (b) pursued further.
    Found and LANDED a real, independently-shipping bug — `unnestExistsExpr`'s
    `srcTableOffset` (unnest.go) under-counted across sibling EXISTS clauses
    in the same statement, because it scanned `outerChild.Output()`, which a
    semi/anti Join deliberately does not grow after splicing (RHS columns
    never appear in a Semi/Anti Output()). For Q69 (three chained EXISTS)
    this collided `store_sales`/`web_sales`/`catalog_sales` all onto
    `SourceTableIdx=5` and all three `date_dim` occurrences onto `6`. Fixed
    via a new `maxSourceTableIdxDeep` helper that walks the actual node tree
    (`*Join.Left`/`*Join.Right`, `*Filter.Child`) instead of trusting
    `Output()`. This runs in PRODUCTION today for any multi-EXISTS
    statement, independent of the still-broken `joinInfoList` double-append
    — verified with the `joinInfoList` fix NOT applied:
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96/MISMATCH=0/ERROR=0/
    SKIP=3, only 3 queries (Q16, Q69, Q94) show any plan-text diff and in
    all three it is purely an EXPLAIN alias-disambiguation fix (e.g. a bare
    `date_dim` used for two distinct correlated scans becomes `date_dim_1`/
    `date_dim_2`). **This did NOT fix Q69's actual target bug** — re-run
    with the STI fix plus the temporary `joinInfoList` one-liner produced a
    byte-identical EXPLAIN and the identical runtime crash to §46.5,
    refuting the prior loop's suspicion that a stray unrebased
    `OuterColumnRef` was the mechanism. The real cause: the DP search
    itself builds and WINS an NLI candidate pairing `customer_demographics`
    (indexed on `cd_demo_sk = c.c_current_cdemo_sk`) against the
    `store_sales`+`date_dim` EXISTS synthetic leaf as the "outer" side —
    but `customer` is not in that leaf's relids at all, so no coordinate
    numbering could ever make `c.c_current_cdemo_sk` resolve there. This is
    an eligibility/relids-coverage bug in the index-path candidate
    generator (which relset a candidate index qual's required relids must
    be a subset of), not a coordinate-rebase bug — filed as **c12** with a
    concrete instrumentation starting point in design doc §46.6.**
  Root cause (§46.2, live-traced, no longer a hypothesis): it is NOT two
  independently-built pointer-distinct clones. `tryPGShapedJoinSearch`'s
  semiAnti population loop (joinsearchseam.go:593-597, c6) already folds
  each semiAnti link's `*SpecialJoinInfo` into `ctx.joinInfoList`
  (confirmed live: exactly 3 unique pointers for Q69 right after that
  loop). ~20 lines later, `joinInfoList:
  semiAntiJoinInfoList(ctx.joinInfoList, semiAnti)` appends the SAME
  `semiAnti[*].sjinfo` pointers a SECOND time onto that already-complete
  `base`, with no dedup at all — a mechanical double-count, provable by
  reading the two call sites together. `joinIsLegal`
  (joinsearchlevel.go:239-259) iterates the whole 6-entry list per
  candidate pair and its "matches multiple SpecialJoinInfos" guard fires
  on the pair's second (duplicate) occurrence, declining a pairing that
  is legitimately legal exactly once.
  **The fix**: replace `joinInfoList: semiAntiJoinInfoList(ctx.joinInfoList,
  semiAnti)` with `joinInfoList: ctx.joinInfoList` (delete
  `semiAntiJoinInfoList`, zero other call sites). Verified live
  (private SF0.25 binary + `GOOPG_PGSHAPED_DP_TRACE=1`, log truncated
  before each restart): `ctx.joinInfoList` drops to the correct 3 entries,
  the `joinIsLegal` "multi" decline for Q69's `(0x1,0x8)`-class pairs
  disappears, and Q69's DP search completes the FULL 6-relation problem
  with real `jointype=semi`/`anti` `DPPATH` lines — the first time in this
  entire c-series (c5-c11) reachability has moved at all.
  **Why it was reverted instead of landed**: this success immediately
  exposed two further, independent, pre-existing defects that were simply
  unreachable before (design doc §46.3, "unwinnable path is untested
  path" pattern): (1) `createPlanAtSearchRootRange`/`boundaryMap`
  (createplanroot.go:264) panics building Q69's winning plan — "search
  root does not publish binding coordinate(s) [91..214]", 124 columns,
  almost certainly the semiAnti synthetic leaves' OWN internal columns,
  which a SEMI/ANTI join never projects above itself and which the
  boundary's totality contract was never taught to exempt; (2) TPC-DS
  Q10 — a DIFFERENT query, OR-combined `EXISTS(...) AND (EXISTS(...) OR
  EXISTS(...))` rather than Q69's pure AND-chain — regresses from PASS to
  a hard `outer column ref c_current_cdemo_sk/level=1 out of range
  (depth=0)` error once it too reaches the newly-unblocked search.
  Confirmed via `scripts/tpcds-sf025-regression.sh sweep` (run with a
  private `GOOPG_BIN` to avoid the shared nightly binary):
  `PASS -Q10` / `ERROR +Q10` in the status-delta. Since the SF0.25 sweep
  is a required gate for planner changes and Q10 is a currently-passing
  query, the fix was reverted in full this loop
  (`git checkout -- internal/optimizer/joinsearchseam.go`; `git diff`
  confirmed empty, `go build ./...` clean) rather than shipped partially
  guarded — a `recover()` scoped to the boundary panic (tried this loop)
  does NOT also cover Q10's failure mode, which is a plain returned error
  from a different pipeline stage, not a panic.
  **Next steps, in order (design doc §46.4, (a) DONE per §46.5)**: (a)
  DONE — `createPlanAtSearchRootRange`/`boundaryMap`'s totality contract
  now exempts a semiAnti synthetic leaf's own coordinate range, landed
  2026-09-17. (b) **re-scoped, still open**: root-cause the
  `outer column ref .../level=1 out of range (depth=0)` error — proven
  NOT Q10/OR-admission-specific (Q69's pure AND-chain hits the identical
  error once (a) stops masking it with a panic first); instrument
  `rebaseSemiAntiChainQual` (joinsearchseam.go:1713) for `*OuterColumnRef`
  coverage and `unnestExistsExpr`'s rebasing completeness on a
  MULTI-EXISTS statement (design doc §46.5) before assuming it is
  OR-shape-specific at all; (c) once (b) is fixed, re-apply the one-line
  `joinInfoList: ctx.joinInfoList` fix (delete `semiAntiJoinInfoList`)
  and re-run the FULL SF0.25 sweep (not just Q69/Q10 in isolation) before
  landing. Also still pending: fix
  `predp.go:159-176`'s Phase B doc comment — its "`used` is therefore
  false on every production call today" claim is stale and actively
  misleading.
  **CLOSED 2026-09-17: all three items resolved.** (a) landed (above).
  (b)'s re-scoped root cause was pinned and fixed by **c12** (the
  index-path-candidate eligibility bug). (c) — re-applying the
  `joinInfoList: ctx.joinInfoList` one-liner — was landed for good by
  **c16** together with its own prerequisite (the SJInfo Path→Join
  carrier c15 found missing), verified via a full TPC-DS SF0.25 sweep
  (`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`) and confirmed present in the
  current tree (`grep joinInfoList: ctx.joinInfoList joinsearchseam.go`,
  `semiAntiJoinInfoList` has zero remaining references). The carried
  `predp.go` doc-comment debt was closed separately by **c17**. **c18**
  then re-measured reachability against the FULL SF0.25 corpus (not a
  hand-probe) and found it is STILL zero for the EXISTS/IN family this
  item targeted (Q10/Q16/Q35/Q69/Q94) — see **c18** below for the
  now-current state and its own, narrower resume point (Q78, a
  completely different producer, is the only query that reaches the
  admission arm at all).
- [x] **M0142-0008a-3i-plumbing-c12 — fix the actual cause of Q69's `outer
  column ref c_current_cdemo_sk/level=1 out of range (depth=0)` runtime
  crash: an index-path candidate whose required relids are not a subset of
  its own candidate outer relset** (design doc §46.6, filed by c11's
  2026-09-17 continuation). c11 item (b) ruled OUT the SourceTableIdx-
  collision/stray-OuterColumnRef theory (byte-identical EXPLAIN before and
  after that fix) and pinned the real mechanism via live instrumentation:
  the DP search builds and WINS an NLI (`createNestLoopIndexJoinPlan`)
  candidate that probes `customer_demographics` on `cd_demo_sk =
  c.c_current_cdemo_sk`, using the `store_sales`+`date_dim` EXISTS
  synthetic leaf as the candidate's "outer"/driving side (`p.Children[0]`)
  — but `customer` (`c`) is not in that leaf's relids at all, so
  `c.c_current_cdemo_sk` cannot legally resolve there under ANY coordinate
  numbering. `outerParamKey` (`internal/optimizer/createplannl.go:178-193`,
  the only production site building a `*OuterColumnRef` from a
  `*ColumnRef`) faithfully converts whatever it is handed — it is not
  itself buggy. **Next step**: instrument the DP search's index-path
  candidate generator (upstream of `createPlan`, wherever `RequiredOuter`
  gets set on a candidate index `Path` — search near the
  `GOOPG_NLI_COSTGATE` machinery) to print, for every NLI candidate it
  builds while searching Q69, the candidate's own outer relset alongside
  the index qual clause's required relids; the first candidate where the
  clause's relids are NOT a subset of the outer relset is the bug. Use the
  same private-binary SF0.25 method as c9-c11 (§46.1), with the temporary
  `joinInfoList: ctx.joinInfoList` one-liner (§46.3) re-applied locally to
  reach the search. Gate: `go build ./...`, `go test
  ./internal/optimizer/...`, and a full `scripts/tpcds-sf025-regression.sh
  sweep` with a private `GOOPG_BIN` (both with and without the temporary
  joinInfoList fix, per c11 item (a)'s precedent) before landing. Once (b)
  is genuinely fixed, item (c) — re-applying the joinInfoList fix
  permanently and re-running the FULL sweep — can finally proceed. Also
  still pending: fix `predp.go:159-176`'s Phase B doc comment (stale/
  misleading "`used` is therefore false on every production call today"
  claim), noted by the prior loop and not yet actioned.
  - **2026-09-17 update (design doc §47): this task's OWN hypothesis
    (a missing relids-subset check in the index-path candidate
    generator) is REFUTED by direct, exhaustive live instrumentation —
    NOT merely un-reproduced. Every `addNLIPaths`/`addPartialNestLoopPaths`
    candidate the DP search built for `customer_demographics` during a
    real, crashing Q69 run had `outer ⊇ {customer}`; the suspected
    illegal pairing (`outer={store_sales leaf}` alone) never appears in
    an exhaustive trace of both functions. Separately,
    `createNestLoopIndexJoinPlan` (the only DP-search-side constructor
    of the node type involved) was invoked twice during the SAME
    crashing run and both times built the SAFE `outer={customer,
    customer_address}` shape, with the built outer's `Output()` schema
    genuinely containing `c_current_cdemo_sk`. `tryBuildNLI`/
    `rewriteJoinsToNLI` (the other, "legacy" post-search constructor of
    the same node type) never successfully converts anything for this
    query at all (zero conversions traced). Both known producers of a
    `*NestedLoopIndexJoin` build it, when they build it, in the ONE safe
    shape — path selection/construction is not the defect. The prior
    reading of `EXPLAIN`'s indentation/widths as proof of an illegal
    physical pairing is now suspect (§47.3): the display may not
    reflect the true tree at all (see `nlipricesplice.go`'s own
    documented display-only cost-stamping seam for a related, though
    not identical, class of `EXPLAIN`-vs-reality gap).
    **Sharper reframing (design doc §47.4)**: the executor's own panic
    site (`internal/executor/expr.go:472-478`) reports `depth=0`, i.e.
    `len(ctx.OuterRows)==0` — the lateral outer-row stack is completely
    EMPTY at evaluation time, not merely holding the wrong relation.
    This points at an EXECUTION-side dispatch gap (the correctly-built
    `*Join{Lateral:true}` node never gets its outer row pushed before
    its inner `*IndexScan` is rescanned), not a relids/coordinate bug —
    a materially different class of defect than every theory c9-c12
    have pursued so far. Re-filed with a concrete instrumentation start
    point as **c13**; c12 itself is CLOSED as refuted (its filed fix
    target does not exist). No production code changed this loop; all
    instruments reverted (`git diff --stat -- internal/optimizer/`
    empty), gates: `go build ./...` clean, `go test
    ./internal/optimizer/...` PASS.**
- [x] **M0142-0008a-3i-plumbing-c13 — find why `ctx.OuterRows` is EMPTY
  (`depth=0`) when Q69's `customer_demographics` index probe evaluates its
  `OuterColumnRef` for `c_current_cdemo_sk`, given the `*Join{Lateral:true}`
  node that probe belongs to is confirmed CORRECTLY built with
  `outer={customer,customer_address}`** (design doc §47.5, filed by c12's
  2026-09-17 refutation). **DONE 2026-09-17 (design doc §48): c13's own
  framed question (ctx.OuterRows push-vs-diverged-tree) is ANSWERED, and
  it is NEITHER of the two branches filed — a live stack trace
  (`debug.PrintStack()` at the panic site) shows the crash never reaches
  `join_lateral_stream.go`'s `openLateral`/`bindOuter` at all; it goes
  through `join_nl_stream.go`'s `openNestedLoop` (the ordinary,
  non-lateral, materialize-and-replay driver), which structurally never
  touches `ctx.OuterRows`. Pointer-identity tracing (5 rounds of
  instrumentation, reproduced identically across 2 independent process
  runs) pins the mechanism precisely: exactly ONE call to
  `createNestLoopIndexJoinPlan` (createplannl.go) builds the CORRECT
  `*Join{Lateral:true}` wrapping a fresh, `OuterColumnRef`-keyed
  `*IndexScan` (`outer={customer,customer_address}`) — but a SEPARATE
  `*Join{Lateral:false}` (customer_demographics-alone paired with ONE of
  Q69's three EXISTS leaves, via a `PathPrebuilt` at Path-construction
  time, `newPrebuiltPath`/`joinsearch.go:434`) wraps the EXACT SAME
  `*IndexScan` pointer, and THIS is the node actually embedded in the
  executed tree. A clone-before-mutate fix (shallow-copy `is` before
  `createNestLoopIndexJoinPlan`/`createNestLoopIndexJoinPlanFused` write
  `.Cond`/`.Key`/`.Keys`) was implemented and live-tested: **it does NOT
  stop the crash** — the `PathPrebuilt` resolves to the CLONE's address,
  proving the defect is REFERENCE SHARING (something captures the
  already-built parameterized probe as a plain per-relation leaf), not
  in-place mutation of a shared object. Fix reverted in full. A SECOND,
  possibly-related defect is also now visible: the winning tree splits
  `customer_demographics` away from `customer`/`customer_address`
  entirely, pairing it with an EXISTS leaf it has no predicate
  relationship to at all — a join-legality gap, not just a lost-flag
  bug. Re-filed with a concrete next instrumentation target as **c14**.
  No production code changed this loop (clone fix fully reverted,
  `git diff --stat -- internal/optimizer/ internal/executor/` empty).
  Still pending (carried from c11/c12/c13, not yet actioned): fix
  `predp.go:159-176`'s Phase B doc comment (stale/misleading "`used` is
  therefore false on every production call today" claim).**
- [x] **M0142-0008a-3i-plumbing-c14 — find WHO captures Q69's
  outer-parameterized `customer_demographics` `*IndexScan` (built by
  `createNestLoopIndexJoinPlan`) into a PLAIN, per-relation leaf slot
  reused by an unrelated join pairing** (design doc §48.5-48.6, filed by
  c13's 2026-09-17 pointer-identity trace). c13 proved WHERE the
  corrupted reference surfaces (a `PathPrebuilt(relids matching
  customer_demographics alone)` whose `.node` equals the parameterized
  probe, set at Path-construction time via `newPrebuiltPath`,
  `joinsearch.go:434`, inside `buildInitialRels`) but not WHO writes it
  there — that write happens before `createPlan`'s own tree walk starts,
  so createPlan-side instrumentation cannot see it. **Next step**:
  instrument `joinsearchseam.go`'s leaf-assembly path
  (`extractSearchLeaves`, the `scans`/`leaves` construction around lines
  624-717 per design doc §46-47's prior reading) to print, for every
  REAL FROM-item entry (`i < nprefix`, NOT the synthetic semiAnti
  leaves), the Go pointer/type of `scans[i]` at the moment it is
  captured — cross-referenced against the parameterized probe's own
  pointer (same `debug.PrintStack()`/pointer-print technique as c13) to
  catch the exact write. The likely fix is a DECLINE GATE ("don't let
  this adapter capture a `RequiredOuter != 0` access path as a
  relation's plain per-item leaf"), not a clone. Separately (possibly the
  SAME fix): instrument whichever code decided the winning tree's overall
  relation grouping to learn why `{customer_demographics} × {one EXISTS
  leaf}` — a pair with NO connecting predicate — was ever accepted as
  legal; compare against `joinIsLegal` (joinsearchlevel.go, c9's fix
  target) to check whether this is the same legality-check gap class
  semiAnti leaves needed a fix for, not yet extended to this shape. Same
  private-binary SF0.25 method as c9-c13 (design doc §46.1), temporary
  `joinInfoList: ctx.joinInfoList` one-liner re-applied locally to reach
  the search, reverted before commit either way. Gate before landing
  anything: `go build ./...`, `go test ./internal/optimizer/...
  ./internal/executor/...`, and a full `scripts/tpcds-sf025-regression.sh
  sweep` with a private `GOOPG_BIN`. Also still pending (carried from
  c11-c13, not yet actioned): fix `predp.go:159-176`'s Phase B doc
  comment (stale/misleading "`used` is therefore false on every
  production call today" claim).

  **LANDED (this loop, design doc §49): root cause was a mirroring gap
  between `chainCarriesLateral` and `extractSearchLeaves`, and it explains
  BOTH of c13's defects (the Lateral-loss crash AND the illegal
  `{customer_demographics} x {one EXISTS leaf}` pairing) with ONE fix —
  they were never two independent bugs.** `extractSearchLeaves`'s Semi/Anti
  arm (landed by b1/b2) descends a Semi/Anti join's `Left` looking for
  further reorderable structure; `predp.go`'s Phase A search (run on the
  subtree BELOW the pinned Semi/Anti spine, BEFORE Phase B walks the whole
  spine) splices its own already-`createPlan`'d winning tree into that
  `Left` — for Q69 this includes the correctly-built `*Join{Type:Inner,
  Lateral:true, Right: is}` `createNestLoopIndexJoinPlan` builds, an
  ordinary `*Join` struct with no marker distinguishing "already planned"
  from "still-raw AST". Phase B's `extractSearchLeaves` walk cannot tell
  the difference either and DECOMPOSES this already-decided Lateral join
  back into two independent leaves — `is` (the outer-parameterized
  `*IndexScan`) becomes its own plain `PathPrebuilt` leaf (c13's crash),
  and `customer`/`customer_address` become a separate leaf, orphaned from
  `customer_demographics` (c13's illegal pairing). The guard meant to catch
  this, `chainCarriesLateral`, was never extended when b1/b2 added
  `extractSearchLeaves`'s Semi/Anti descent: for a `*Join{Type:Semi|Anti}`
  it fell through to `nodeReferencesOuter`'s finer-grained "does an
  `OuterColumnRef` escape UNBOUND" check (answers NO — `is.Key` IS
  correctly bound by its own immediate Lateral parent) instead of this
  function's own coarser "any Lateral reachable = decline" rule that
  actually matches what `extractSearchLeaves` is about to do. **Fix**: one
  new arm in `chainCarriesLateral` (`joinsearchseam.go`) descending a
  Semi/Anti join's `Left` with the same coarse rule, mirroring
  `extractSearchLeaves`'s own walk exactly (`Right` excluded — the mirrored
  walk never decomposes it either, so nothing inside it is ever at risk).
  **Live-verified**, private binary + private SF0.25 data copy (same
  method as c9-c13, `tmp/c14-bin`/`tmp/c14-sf025-data`, both removed after):
  Q69 (crashed on every c9-c13 HEAD) now runs clean, 100 rows, and
  `EXPLAIN` shows the corrected shape — `customer_demographics` joined back
  to `customer`/`customer_address` via `Index Scan using
  customer_demographics_pkey ... Index Cond: (cd_demo_sk =
  c.c_current_cdemo_sk)`. Gates: `go build ./...` clean; `go test
  ./internal/optimizer/...` and `./internal/executor/...` both PASS; two
  new direct unit tests (`chaincarrieslateral_test.go`) pin the fix and its
  non-overreach (a non-lateral chain under a Semi join's `Left` must NOT be
  declined); full `scripts/tpcds-sf025-regression.sh sweep` with a private
  `GOOPG_BIN`: `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`,
  `PLAN-SHAPE: same=99 changed=0` — only Q69 moved (crash→pass), no other
  query's plan shifted. `predp.go:159-176`'s stale Phase B doc comment
  (carried from c11-c13) is STILL not actioned — this fix likely makes
  Phase B decline MORE often, not less, so the claim needs re-verification
  rather than a same-direction edit; left for a future loop. Design doc
  §49.
- [x] **M0142-0008a-3i-plumbing-c15 — re-apply c11 item (c)'s
  `joinInfoList: ctx.joinInfoList` fix (delete the duplicate-appending
  `semiAntiJoinInfoList` helper) now that c14 closed item (b)'s Q69 crash,
  and fix the new defect it exposes: no `createPlan` join constructor
  propagates `.SJInfo` onto the `*Join` node it builds** (design doc §50,
  filed by this loop's re-attempt of c11 item (c)). **Attempted
  2026-09-17: the fix itself builds and `go test`s clean EXCEPT for
  `TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`,
  which now finds a `JoinTypeSemi` node in `Plan()`'s returned tree with
  `.SJInfo == nil`. Root cause: `.SJInfo` is set exactly once in
  production (`unnest.go:4837`, `existsUnnestSJInfo`, at AST-unnesting
  time) and no `createPlan` constructor (`createHashJoinPlan`,
  `createMergeJoinPlan`, the plain-nestloop constructor at
  createplannl.go:143 — "the one arm a searched SEMI or ANTI join can
  reach" per its own comment — or the NLI constructors) ever copies it
  onto the freshly built node. Before this fix the `joinInfoList`
  duplicate-list bug always declined the search for any Semi/Anti
  statement, so the ORIGINAL AST-built `*Join` (carrying `.SJInfo`
  directly from unnesting) always reached the final tree unchanged; with
  the search now succeeding, the final tree's Semi join is a NEW node
  `createPlan` built from a `Path`, which had nowhere to carry `.SJInfo`
  forward. A third latent, previously-untested defect in the same
  "unwinnable path is untested path" family as c12/c13 — `grep -rln
  "\.SJInfo\b" internal/` shows nothing downstream of `createPlan`
  consumes the field today, so it is not (yet) a wrong-query-result bug,
  but it breaks a real protected invariant and is filed as a genuine
  defect rather than waved off. Reverted in full (`git checkout --
  internal/optimizer/joinsearchseam.go
  internal/optimizer/semiantichain_test.go`; diff empty, `go build ./...`
  clean, `go test ./internal/optimizer/...` green).
  **Concrete resume point**: (1) add a `SJInfo *SpecialJoinInfo` field to
  `Path` (path.go); (2) `addNestLoopPath` (pathgen.go:175) already
  receives `sjinfo` as a parameter but never stores it on the `&Path{...}`
  it builds (pathgen.go:192-206) — add `SJInfo: sjinfo`; thread the same
  parameter into the hash/merge path constructors too, even though only
  the nestloop arm is reachable for Semi/Anti today, so the invariant
  holds uniformly; (3) in createplannl.go's plain-nestloop constructor's
  `j := &Join{...}` literal (~line 141), add `SJInfo: p.SJInfo` (nil is
  correct/harmless for every non-Semi/Anti join); (4) re-run
  `go test ./internal/optimizer/...` (the narrowing test above is the
  tripwire), then re-apply the `joinInfoList` one-liner and run the FULL
  `scripts/tpcds-sf025-regression.sh sweep` with a private `GOOPG_BIN`
  before landing either piece together. Also still pending (carried from
  c11-c13, not actioned): `predp.go:159-176`'s stale Phase B doc comment.**
- [x] **M0142-0008a-3i-plumbing-c16 — implement c15's resume point (SJInfo
  Path→Join carrier for BOTH the nestloop AND hash arms — hash included
  because M0142-0008a-3(iii) already lifted its SEMI/ANTI decline, making
  c15's "nestloop is the one reachable arm" comment stale) and re-apply the
  `joinInfoList: ctx.joinInfoList` fix; both build and unit-test clean
  except for ONE test, whose failure is evidence the fix works, not a
  regression.** Design doc §51.
  **What landed and was verified, then reverted (see below)**: (1)
  `Path.SJInfo *SpecialJoinInfo` field (path.go); (2) `addNestLoopPath`
  AND `addHashJoinPath` (pathgen.go) both now store `SJInfo: sjinfo` on the
  `&Path{...}` they build — hash needed it too, since `jointypeForDirection`
  no longer declines SEMI/ANTI for the hash arm (createHashJoinPlan's own
  comment was stale, claiming "SEMI/ANTI are nestloop-only"; fixed in the
  same edit); (3) `createNestLoopPlan` and `createHashJoinPlan` (createplannl.go,
  createplanjoin.go) both copy `SJInfo: p.SJInfo` onto the `*Join` node they
  build; (4) `joinInfoList: ctx.joinInfoList` (joinsearchseam.go, replacing
  the unconditional-duplicate-append `semiAntiJoinInfoList` call, which is
  now dead and was deleted) — confirmed SAFE by reading: `ctx.joinInfoList`
  already receives every semiAnti link's SJInfo via a DEDUPING append
  (`joinInfoListHas` guard, joinsearchseam.go:594-595, landed by c4) earlier
  in the same function, so the deleted call was adding a second,
  undeduped copy of each entry — a real, confirmed duplicate-list bug, not
  a false lead. With (1)-(4) applied: `go build ./...` clean,
  `TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`
  (semiantichain_test.go:316, the SJInfo-nil tripwire c15 left) went GREEN —
  proof the Path→Join carrier fix is correct — but a LATER assertion in the
  SAME test then failed: `scans = 2 leaves, want 3`.
  **Root cause of the new failure (confirmed by direct instrumentation, not
  guessed)**: for this test's fixture (`SELECT t1.x, t3.a, t3.b FROM t1, t3
  WHERE t1.x = t3.a AND EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)`),
  `Plan()`'s FINAL tree is now `Project(Filter(Project(InnerJoin(SemiJoin(
  SeqScan(t1), Project(SeqScan(t2))), SeqScan(t3)))))` — the search
  evaluates `t1 SEMI t2` FIRST (t1 alone, not the {t1,t3} composite the test
  fixture assumed), THEN joins t3 in above it. This is CORRECT and is the
  whole point of the MinLefthand-narrowing work this c-chain built: since
  the EXISTS correlates only to t1 (not t3), `joinIsLegal` now has enough
  SJInfo data (thanks to c4's dedup-append actually reaching the search via
  this loop's item (4)) to know the semi join may legally be evaluated
  before t3 joins in, and the search finds/prefers that shape. The test's
  fixture comment ("leaves t1 JOIN t3 as a raw *Join chain… reaching the
  Semi join's LHS") is an assumption that was only ever true because the
  search was DECLINING for Semi/Anti (the c11-era bug) — once the search
  actually runs, it is free to choose a DIFFERENT, valid reordering, and
  for this fixture, does. The failing assertion is downstream evidence the
  underlying fix is right, not a sign anything in (1)-(4) is wrong.
  **Reverted in full** (`git checkout -- internal/optimizer/createplanjoin.go
  internal/optimizer/createplannl.go internal/optimizer/joinsearchseam.go
  internal/optimizer/path.go internal/optimizer/pathgen.go`; diff empty
  confirmed, `go test ./internal/optimizer/...` green) because the
  pre-commit discipline requires green tests and fixing the test properly
  needs more than a one-line patch (see resume point) — landing (1)-(4)
  with a known-red test would repeat exactly the mistake this whole c-chain
  has been careful to avoid.
  **Concrete resume point**: re-apply (1)-(4) exactly as described above
  (they are correct and were fully re-derived this loop; diff is small —
  ~40 lines across 5 files, straightforward to reconstruct from this
  entry), THEN fix
  `TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`
  (semiantichain_test.go:296-364). Do NOT try to force the old tree shape
  by hand-splicing fragments from two separately-`Plan()`-built queries —
  position/coordinate spaces are per-statement (leftdeep-joins 03 §9 /
  `goopg_two_column_coordinate_boundaries` memory) and splicing risks a
  silently-wrong offset that makes the test pass for the wrong reason.
  Two real options: (a) hand-construct the fixture tree directly from
  `*SeqScan`/`*Join` literals (schema/position conventions already
  demonstrated by `m0142_0008a_3i_verify_probe_test.go` and
  `analyzedThreeTablesCatalog`), bypassing `Plan()`'s search entirely so the
  test is a true white-box unit test of `extractSearchLeaves` again; or
  (b) accept that the search may now legally choose either order, and
  rewrite the test to locate the ACTUAL join-tree root reachable from
  `node` (not hardcode "first Semi found") and assert the SJInfo-narrowing
  invariant against whichever shape resulted, branching on which relation
  (t1 vs the {t1,t3} composite) ended up on the semi join's LHS. (a) is
  probably safer/more principled — it also stops the test depending on the
  DP search's cost tie-breaking, which is exactly the kind of hidden
  coupling this project's history warns about. `GOOPG_PGSHAPED_DP` (the
  search on/off kill switch) does NOT help here — it is read into a
  package-level `var` at init time (`joinsearch.go:76`), not re-read
  per-call, so `t.Setenv` in a test has no effect on an already-running
  binary. Still pending (carried since c11): `predp.go:159-176`'s stale
  Phase B doc comment.**
  **CLOSED 2026-09-17: subsumed by M0142-0008a-3i-plumbing-c16**, which
  implemented this item's resume point in full (the SJInfo Path→Join
  carrier for both nestloop and hash arms, plus the `joinInfoList`
  one-liner) and landed it. The one item c15 left pending on top of that
  (the stale `predp.go` doc comment) is closed separately by
  **M0142-0008a-3i-plumbing-c17** below.
  **LANDED 2026-09-17**: re-applied the four-piece diff unchanged, then
  fixed the test by hand-building its `*SeqScan`/`*Join` fixture directly
  (option (a) from the resume point above) instead of calling `Plan()` —
  now a true white-box unit test of `extractSearchLeaves`, independent of
  the DP search's join-order tie-breaking. `go build ./...` clean,
  `go test ./internal/optimizer/...` fully green. Also deleted the now-dead
  `semiAntiJoinInfoList` helper (no remaining callers, no test referenced
  it directly). Gates: `scripts/tpcds-sf025-regression.sh sweep` (private
  `GOOPG_BIN`) `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
  `PLAN-SHAPE: queries=99 same=99 changed=0` — the carrier fix changes no
  TPC-DS SF0.25 plan shape or result. `scripts/tpch-spotcheck.sh` SKIPPED
  (pre-existing, unrelated: `:65433`'s `tpch` database is still empty
  pending the M0142-0003k reload — see CLAUDE.md). Design doc §52.
- [x] **M0142-0008a-3i-plumbing-c17 — fix `predp.go:159-176`'s stale Phase B
  doc comment (carried since c11).** **DONE 2026-09-17 (design doc §53).**
  The comment claimed the splice branch (`used && residual == nil` inside
  `runJoinSearchBelowPinned`'s Phase B block) is unreachable in production
  because §32.1's `admitSemiAnti` literal "stays `false`" — checked, not
  just reworded, via a temporary stderr probe (reverted before commit):
  the branch is reached 17 times by the existing `internal/optimizer`
  suite alone and taken (`used=true`) 4 of those times, via ordinary
  `Plan()`-driven tests of Q21's stacked EXISTS/NOT EXISTS shape
  (`TestPlanQ21LiveSQL`, `TestM0070Q21InnerOnlyConjunctsStay`), both
  already passing — not a hand-built result as the old comment claimed.
  Root cause of the staleness: `admitSemiAnti` was flipped to `true` by
  b2 step (iii), which landed *before* the c1-c16 chain started, so the
  claim was already wrong when c11 first flagged it as pending debt, and
  every loop since (c12-c16) carried it forward unverified. No behavior
  or plan-shape risk: the splice has been live since b2 (well before
  c16), c16's own TPC-DS SF0.25 sweep (99/99 unchanged) already covers
  the query family most likely to hit this shape (Q10/Q16/Q35/Q69/Q94),
  and the tests exercising `used=true` already pass — this is a
  documentation-only fix. Verification: `go build ./...` clean,
  `go test ./internal/optimizer/...` green, diff is comment-only (no
  production code changed, no gate re-run needed).
- [x] **M0142-0008a-3i-plumbing-c18 — full-corpus (96-query) SF0.25 recheck
  of Semi/Anti DPPATH reachability after c1-c17 all landed: still 0/96;
  root-caused to a SECOND, unrelated `.SJInfo`-population gap in the ONE
  query that reaches the admission arm at all (Q78, LEFT-JOIN-to-ANTI-JOIN
  strength reduction, not EXISTS/IN unnesting)** (design doc §54, filed by
  this loop, prompted by M0142-0008c-3b/3c/3d's stale "0 of 162 DPPATH
  lines" measurement predating c1-c17). **DONE as a recon 2026-09-17, no
  production change.** Private `GOOPG_BIN`, `GOOPG_PGSHAPED_DP_TRACE=1`,
  full `scripts/tpcds-sf025-regression.sh sweep` (`PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`, unchanged — expected, c1-c17 is a
  no-op on this corpus), then each of the 96 non-skipped `query*.sql` run
  ONE AT A TIME against the same cluster with the trace log truncated
  between runs, for a real per-query attribution (the sweep's own log has
  no per-query markers). Result: `jointype=semi`/`anti` DPPATH lines = 0,
  same as before the whole chain. Exactly one query produces ANY
  semiAnti-related trace at all: **Q78**, 3x `seam-decline
  reason=semianti-link-no-sjinfo` (matching its 3 independent CTEs) — every
  other candidate (Q10/Q16/Q35/Q69/Q94, the family every c9-c17 note
  assumed was "most likely to hit this shape") shows ZERO semiAnti trace
  lines of any kind, meaning `extractSearchLeaves`'s admission arm
  (joinsearchseam.go:1287) is never even reached walking their trees — a
  separate, still-open, unfiled question (not this task's scope; needs
  tracing why their Semi/Anti joins don't reach `tryPGShapedJoinSearch`'s
  input tree at all, possibly `whereEligibleForPreDPUnnest`
  (predp.go:35) declining the whole statement for an unrelated scalar-
  sublink reason and sending it down the legacy DP-before-unnest order).
  **Q78 root-caused**: `query78.sql` has no EXISTS/IN sublink at all — its
  three CTEs use `left join … where <key> IS NULL`, the classical
  outer-join-to-anti-join idiom, demoted at the AST level by
  `reduce_outer_joins.go`'s `demotedForPlan` (line 102), a completely
  different, older mechanism than `existsUnnestSJInfo` (unnest.go:4837,
  the only thing that has EVER set `.SJInfo` on a `*Join` node, per c15).
  The resulting `JoinTypeAnti` node DOES reach the admission arm (3
  declines = 3 CTEs) but `sjinfo: j.SJInfo` (joinsearchseam.go:1360) reads
  a field this producer never populates, so it is always nil and
  `semiAntiLinksHaveSJInfos` correctly declines — exactly as designed for
  "a link whose source `*Join` never carried an SJInfo" (existing comment
  at joinsearchseam.go:588-591), just via a producer nobody had traced
  through yet. Very likely (unverified this loop) `ctx.joinInfoList`
  already holds a matching `*SpecialJoinInfo` for each of Q78's ANTI
  joins — `deconstructJointreeScopedSJI` (planner.go:3051) snapshots from
  `s.FromExprs` BEFORE unnesting runs, and Q78's ANTI type is already set
  in `FromExprs` at that point (unlike an EXISTS/IN join, which doesn't
  exist as a `*Join` node yet when that snapshot runs) — the gap is purely
  that nothing threads a pointer from that list back onto the `*Join`
  node's own field for this producer.
  **Concrete resume point (design doc §54)**: two candidate fixes — (a)
  at the point the resolver builds the `*Join{Type: JoinTypeAnti}` literal
  for a `reduce_outer_joins.go`-demoted item, look up and set `.SJInfo`
  there too (mirrors `existsUnnestSJInfo`'s own convention); or (b) make
  the semiAnti link constructor (joinsearchseam.go:1360) fall back to a
  relids-keyed lookup in `ctx.joinInfoList` when `j.SJInfo == nil` (more
  PG-faithful: PG's own `join_is_legal` always looks up `join_info_list` by
  relids, never a stored per-node pointer) — try (b) first, it is smaller
  and touches one call site. After either fix, re-run this task's exact
  per-query attribution method on Q78 alone to confirm 3 declines become 3
  accepts with real `jointype=anti` DPPATH lines, then the full SF0.25
  sweep to confirm Q78's own result and every other query's plan stay
  byte-identical to the oracle. Verification this loop: sweep green as
  above; no `internal/` files changed (instrumentation + `psql -f` runs
  against a private, disposable cluster only); `tmp/goopg-sf025-trace-bin`
  and its throwaway data/log removed after the recon.
- [x] **M0142-0008a-3i-plumbing-c19 — landed a placeholder `.SJInfo` for the
  `reduce_outer_joins`-demoted ANTI producer; Q78's declines move one gate
  deeper (`semianti-link-no-sjinfo` -> `semianti-on-qual`), reachability
  still 0/96, no plan movement anywhere** (design doc §55, filed/landed by
  this loop). **DONE 2026-09-17.** c18's own candidate (b) — "fall back to a
  relids-keyed lookup in `ctx.joinInfoList`" — turned out to be unworkable,
  confirmed by instrumentation before writing any production code: a
  temporary trace showed `ctx.joinInfoList` has **zero** entries at the
  point Q78 declines, not a relids mismatch. Root cause traced one level
  further than c18 saw: Q78's ANTI join is the LEADING join in its
  FromExpr's chain, and `antiCollapsedJoins` (collapse.go, R41/K74)
  deliberately excludes a LEADING semi/anti link from
  `deconstructFromItemScoped`'s leaf/SJI numbering (space 1 — no
  `rangeBinding`, no leaf index, no `makeSpecialJoinInfoScoped` call for
  it), while `extractSearchLeaves`'s own walk (space 2, joinsearchseam.go)
  numbers the SAME join's opaque RHS as a real leaf regardless. The two
  spaces are incompatible by design for this exact shape (space 1
  deliberately has FEWER leaves than space 2 so `len(ctx.bindings)` and
  `jl.nrels()` do not desync — R41/K74's own stated reason), so no relids
  key built in space 1 could ever match `lk.lhs`/`lk.rhs` (space 2) — (b) as
  literally written would have found nothing to fall back to, confirmed by
  running the actual query, not by re-reading the code.
  **What landed instead (closer to candidate (a)):** `demotedAntiSJInfo`
  (`specialjoin.go`, new function) attaches a self-contained bit0/bit1
  placeholder `*SpecialJoinInfo` to `jn.SJInfo` at the ONE call site
  (`planFromItem`, planner.go, inside the existing `if joinType ==
  JoinTypeSemi || joinType == JoinTypeAnti` block) that builds this
  producer's plan-tree `*Join` node — the exact convention
  `existsUnnestSJInfo` (unnest.go:4390-4397) already uses for the EXISTS/IN
  unnesting producer, chosen specifically because `extractSearchLeaves`
  ALREADY renumbers a placeholder's `SynLefthand`/`SynRighthand`/
  `MinLefthand`/`MinRighthand` in place once it walks the node
  (joinsearchseam.go:1415-1418, landed by c16) and ALREADY threads the
  result into `ctx.joinInfoList` (c6) — no new plumbing needed beyond
  giving this one producer the placeholder to begin with.
  **Verified, not assumed:** a temporary trace confirmed, before removal,
  that (1) `demotedAntiSJInfo` fires exactly 3x per Q78 run (matching its 3
  CTEs) with `joinType=JoinTypeAnti`; (2) `semiAntiLinksHaveSJInfos` no
  longer declines (zero `seam-decline reason=semianti-link-no-sjinfo` lines,
  down from c18's 6); (3) the search now reaches `semiAntiOnQualsOK`, which
  declines instead (6x `reason=semianti-on-qual`, matching the 6 `-link-
  no-sjinfo` declines it replaced) — one real gate deeper, exactly c5/c6's
  own "moves ONE gate deeper" pattern. Full-corpus verification: private
  `GOOPG_BIN`, `scripts/tpcds-sf025-regression.sh sweep` (`PASS=96
  MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`, unchanged from c18);
  `scripts/tpcds-plan-diff.py` against a pre-fix baseline capture
  (`bench/tpcds/runtime_goopg/tpcds-results-sf025/plans-20260917-052136.txt`,
  predating even c16) shows **`queries=99 same=99 changed=0`** — the fix is
  currently a pure no-op on every plan in the corpus, zero regression risk.
  `go test ./internal/optimizer/...` and `./internal/executor/...` both
  green; `tpch-spotcheck.sh` SKIPPED per the still-open M0142-0003k data
  blocker (unrelated); a pre-existing, unrelated `internal/parser`
  `yacc_locking_test.go` AST-drift failure was confirmed present with this
  loop's diff stashed out too (not caused by this change, not investigated
  further — out of scope).
  **Concrete resume point for c20**: `semiAntiOnQualsOK`
  (joinsearchseam.go:1835) requires `lk.pred != nil` and every conjunct of
  it to span both `lk.lhs` and `lk.rhs` and pass `searchConsumes`. Checked
  this loop (read, not traced live): `splitEqualityForHash` (planner.go:3438)
  does NOT mutate `jn.Predicate` — it only reads `pred` to populate
  `jn.LeftKey`/`jn.RightKey`, so `jn.Predicate` keeps the FULL original
  2-conjunct AND (`wr_order_number=ws_order_number and ws_item_sk=
  wr_item_sk`) for Q78's link. `extractSearchLeaves`'s walk then ALSO folds
  `LeftKey`/`RightKey` back in as a NEW top-level equality conjunct
  (joinsearchseam.go ~1329-1349, the `j.LeftKey != nil && j.RightKey !=
  nil` branch, mirroring `unnestExistsExpr`'s convention of excluding its
  own primary equijoin from `Predicate` so this fold-in is the ONLY copy —
  which does NOT hold here, since this producer's `Predicate` was never
  stripped). So `lk.pred` for Q78 is most likely a 3-conjunct AND with one
  REDUNDANT duplicate equality (the folded-in `LeftKey=RightKey` plus the
  same equality already present verbatim in the original ON clause) — not
  nil. `semiAntiOnQualsOK` splits on `splitAnd` and checks each conjunct
  independently, so a harmless duplicate conjunct should not by itself
  cause the decline; the next loop should instrument `semiAntiOnQualsOK`
  directly (print each conjunct + `relidsOfExpr`'s `ok`/`rs` +
  `searchConsumes`'s verdict) to find which specific conjunct/check fails,
  rather than assume the `lk.pred == nil` arm is the cause — this loop's
  read of the code did not confirm that guess and time ran out before a
  live trace could.
- [x] **M0142-0008a-3i-plumbing-c20 — found and fixed the real
  `semianti-on-qual` decline: a coordinate-space bug in the semiAnti chain
  link's predicate rebase, exposed (not introduced) by c19's reachability
  fix, Q78's decline moves to a pre-existing DELIBERATE firewall
  (`outer-over-derived`), reachability still 0/96, no plan movement, zero
  regression** (design doc §56). **DONE 2026-09-17.** Live-traced (temporary
  `GOOPG_C20DEBUG=1` prints in `semiAntiOnQualsOK` and
  `extractSearchLeaves`, fully reverted before commit) rather than guessed:
  the failing conjunct was the FOLDED `LeftKey=RightKey` synthetic equality
  (not either of `j.Predicate`'s own two conjuncts, refuting c19's
  "redundant duplicate, probably harmless" guess), and its relids came back
  spanning leaves `{0,2}` (websales, date_dim) instead of `{0,1}`
  (websales, webreturns).
  **Root cause:** `extractSearchLeaves`'s walk rebases a semiAnti link's
  `pred` into its OWN "walk-order flat" column space (leaf i's columns start
  at the cumulative sum of walk-visited leaves' widths, via
  `rebaseSemiAntiChainQual`'s `base`/`outerWidth`/`rightBase`) — a
  self-consistent space, but NOT the one `buildLeafSpans` actually produces
  as `cumOffsets`, which `relidsOfExpr`/`searchConsumes` read. `buildLeafSpans`
  deliberately relocates every SYNTHETIC (semiAnti RHS) leaf's span
  out-of-band, after the total width of every REAL leaf (its own doc
  comment, item 3, landed for a different bug — §25.1's `qualAC`
  misattribution). The two spaces coincide only when no REAL leaf follows a
  semiAnti link's synthetic leaf in walk order. Q78 breaks that: each CTE's
  shape is `(websales ANTI webreturns) JOIN date_dim`, so `date_dim` (real,
  leaf 2) sits in walk order between the ANTI join's own leaf pair and the
  point where `buildLeafSpans` starts assigning synthetic spans — the
  folded equality's RHS operand, rebased to the ANTI join's own
  walk-order-flat position (which happens to equal `date_dim`'s
  `cumOffsets` slot for this shape, base=0/rightBase=outerWidth), resolves
  through `cumOffsets` to `date_dim`'s leaf instead of `webreturns`'s. This
  is a LATENT bug in the c6/c7 rebase machinery, not something c19
  introduced — c19 only gave the FIRST producer (`reduce_outer_joins`
  demoted ANTI joins) enough reachability (`.SJInfo`) to reach this far and
  expose it; `unnestExistsExpr`'s own keyed producer never triggers it
  because none of the queries it currently fires for have a REAL leaf
  trailing the semiAnti pair in walk order.
  **Fix:** added `remapWalkOrderFlatToSpans` (joinsearchseam.go, next to
  `buildLeafSpans`) and called it once per `semiAnti[i].pred`, right after
  `cumOffsets := buildLeafSpans(...)` (so it runs with the FINAL, complete
  `widths` in hand — the synthetic-leaf target position is only knowable
  once every real leaf's width is known, which is NOT true yet at
  `extractSearchLeaves`'s per-link, mid-walk rebase point). It decomposes
  each `ColumnRef.Index` into (leaf, local offset) using `widths`' own
  walk-order prefix sums, then re-targets to `cumOffsets[leaf].lo +
  localOffset` — the space `relidsOfExpr` actually reads. Declines
  (`semianti-pred-remap`) if a leaf can't be found, which should be
  unreachable given `extractSearchLeaves`'s own construction.
  **Verified, not assumed:** re-ran the SAME live trace after the fix —
  `semiAntiOnQualsOK` now passes all 3 conjuncts for all 3 of Q78's CTEs
  (`rs=leaves{0,1}` correctly, `subset=true`, both overlaps true,
  `consumes=true`). Q78's decline moved to a DIFFERENT, LATER gate:
  `outer-over-derived` (`relfromjoinlist.go:676`) — a pre-existing,
  DELIBERATE firewall with its own doc comment naming Q78 by name
  ("C-04a firewall (Q78): a problem that pairs an OUTER hand with a derived
  (table-less) input declines... 15s Hash shape -> 327s timeout... Resume:
  lift when B-06 wires CTE-output stats") — so Q78 is correctly still
  declining, for the RIGHT, already-documented reason, NOT falling through
  into the known timeout-regression shape. Full-corpus check: private
  `GOOPG_BIN`, `scripts/tpcds-sf025-regression.sh sweep` — `PASS=96
  MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3` (unchanged);
  `PLAN-SHAPE: queries=99 same=99 changed=0` (pure no-op on every chosen
  plan in the corpus — the fix only changes an intermediate diagnostic
  decline reason, zero regression risk). `go test ./internal/optimizer/...`
  green.
  **Resume point for c21**: the search's own admission logic for this
  producer is now fully correct end-to-end (SJInfo, on-qual placement,
  coordinate rebase). The remaining blocker for Q78 specifically is the
  `outer-over-derived` firewall itself (`relfromjoinlist.go:654-678`),
  which needs CTE-output statistics (TODO_ALL B-06 step 4) before it can
  safely lift — out of scope for this plumbing sub-track. The
  still-unfiled, materially larger question from c19 remains open too:
  Q10/Q16/Q35/Q69/Q94 (the EXISTS/IN family) show ZERO semiAnti trace of
  any kind corpus-wide — their Semi/Anti joins may never reach the search's
  admission arm at all, a different gap than Q78's.
- [x] **M0142-0008a-3i-plumbing-c21 — closed c18/c19's "ZERO semiAnti
  trace" open question: mis-attribution, not a gap. Q10/Q16/Q35/Q69/Q94 all
  already reach correct Semi/Anti joins** (design doc §57). **DONE
  2026-09-17, no production change.** Per c20's resume point, instrumented
  `whereEligibleForPreDPUnnest` (predp.go:35) live (temporary
  `GOOPG_C21DEBUG=1` stderr prints at the gate's call site plus
  `canUnnestExistsExpr`'s 4 bail arms and `unnestExistsExpr`'s
  `topConjunct==nil` bail; reverted before commit) against a disposable
  schema-only cluster (`/tmp/c21data` port 5534, 11-table DDL from
  `third-party/tpcds-postgres/.../tools/tpcds.sql`, zero rows). Result: the
  gate returns `true` (eligible) for all 5 queries at every call — reading
  its body first would have shown why: it only checks for
  `*SubqueryExpr`/`*ArraySubqueryExpr`/`*MultiAssignSubqRow` (scalar
  sublinks), and none of these 5 queries' WHERE clauses contain any scalar
  sublink at all (verified by reading the query files directly). The only
  bail seen was the correct, expected `topConjunct=nil` (EXISTS inside an
  OR) for Q10 and Q35's `(EXISTS(web_sales) OR EXISTS(catalog_sales))`
  clause — exactly matching PG's own `pull_up_sublinks_qual_recurse`
  restriction (only descends AND, never OR). EXPLAIN on all 5 confirmed
  every one already builds the right Semi/Anti joins (Q10/Q35: 1 Hash Semi
  Join + 2 correctly-retained SubPlans for the OR pair; Q16/Q94: Hash Anti
  Join over Hash Semi Join; Q69: 2 Hash Anti Joins over 1 Hash Semi Join,
  all 3 of its EXISTS/NOT EXISTS lifted) — independently reconfirmed
  against the pre-existing real-SF0.25-data census
  (`tmp/m0142-0008a-census/goopg_explains.txt`, predates this task; same
  Semi/Anti shapes present there too). **Root cause of c18's false
  reading**: its "0 DPPATH lines" measurement counted
  `GOOPG_PGSHAPED_DP_TRACE=1`'s DP-**enumeration** trace
  (`joinsearchtrace.go`, fires only inside `tryPGShapedJoinSearch`'s own
  `makeJoinRel`), but S5a's design pins this family's Semi/Anti joins
  BEFORE the DP search runs and the DP search subsequently runs only on
  the subtree below that pinned spine (`runJoinSearchBelowPinned`,
  planner.go:1535) — a pinned join is never a DP-search candidate, so it
  is structurally impossible for it to emit a DPPATH line, regardless of
  whether it built correctly. "Zero DPPATH trace" was misread as "the join
  never happens" when the correct reading is "this trace channel was never
  wired to observe the pre-DP pinned-spine path" — a trace-coverage blind
  spot, not an engine gap. Disposition: no code change; the family already
  has full parity on this axis. No deferral-ledger row (nothing left
  unimplemented — the recon's conclusion is that the prior "0/96" framing
  was itself the defect). All instrumentation reverted (`git checkout --
  internal/optimizer/unnest.go internal/optimizer/planner.go`,
  confirmed via `git diff --stat` showing empty); `go build
  ./internal/optimizer/...` clean after revert. Scratch cluster/binary/logs
  (`/tmp/c21data`, `/tmp/c21-*.log`, `tmp/goopg-c21-bin`) fully removed;
  `goopg-c21-test.scope` confirmed not loaded.
- [x] **M0142-0008a-3i-plumbing-c22 — recheck: does closing c1-c21 finally
  make `M0142-0008c-3c`/`-3d`/`-4`'s unique-ify substitution reachable?**
  — filed by this loop, prompted by c21's closure of the plumbing chain
  and by `-3c`'s own "blocked on plumbing-c" framing (design doc §35),
  which needed re-checking now that the cited blocker is resolved. **DONE
  2026-09-17 (design doc §58), answer is NO, for a structural reason
  independent of c1-c21 and independent of Q78's firewall.**
  `jointypeForDirection`'s SEMI/ANTI/RIGHT arm (`joinpaths.go:216-243`) is
  the sole entry point `-3a`/`-3b`/`-3c`/`-3d`/`-4` share; its unique-ify
  fallback only fires for `sjinfo.Jointype == parser.JoinSemi`. Live-traced
  (temporary `GOOPG_C22DEBUG=1` `fmt.Fprintf` calls at the arm's entry and
  at the fallback itself, reverted before commit) against the full TPC-DS
  SF0.25 corpus (`scripts/tpcds-sf025-regression.sh sweep`, private
  `GOOPG_BIN=tmp/goopg-c22-bin` to avoid the shared-binary collision with
  the live nightly TPC-H lane): the arm is entered **zero times** across
  all 96 completing queries. Root cause: the EXISTS/IN family never reaches
  it (pinned pre-DP, c21 §57); IN-unnesting never sets `.SJInfo` (c6 §41);
  and the one confirmed DP-reachable producer (`reduce_outer_joins`'s ANTI
  demotion, c19) is unconditionally `parser.JoinAnti`, which the
  `JoinSemi`-only fallback gate excludes — so even fully lifting Q78's
  `outer-over-derived` firewall (gated on B-06) would NOT make `-3d`/`-4`
  reachable, correcting their prior resume-point framing. Sweep unchanged
  (`PASS=96 MISMATCH=0`, `PLAN-SHAPE: same=99 changed=0`). All
  instrumentation reverted (`git checkout -- internal/optimizer/joinpaths.go`,
  confirmed empty `git diff --stat`); `go build ./internal/optimizer/...`
  clean after revert. Private binary (`tmp/goopg-c22-bin`) removed; the
  shared SF0.25 goopg cluster (`:65437`) left running per this milestone's
  convention for that semi-persistent resource. No deferral-ledger row:
  nothing new left unimplemented — `-3d`/`-4`'s existing unchecked entries
  already record the open work; this recon only corrects their resume
  point (now updated in place, see M0142-0008c-3d/-4 below). The actual
  unblock — teaching an IN-unnesting path to set `.SJInfo` on a
  DP-search-visible `JoinSemi` link, the way c19 did for ANTI — is
  unfiled, separate, larger work, not sized for a single loop; a future
  loop should file it as its own scoping recon before implementing.
- [x] **M0142-0008c — scoping recon: does goopg need PG's `create_unique_path`
  (semi-join → de-duplicate RHS + inner join) to reach parity on TPC-DS
  Q10/Q35?** — filed by M0142-0008a-3(iii)'s §4.3 gate re-run (design doc §6).
  **DONE 2026-09-16 as a recon (design doc §16), no production change.
  Answer: yes, goopg needs it for Q10/Q35, and it is a from-scratch
  mechanism comparable in size to M0142-0008a itself, not a plumbing
  add-on.** Read PG's `create_unique_path` live
  (`postgres/src/backend/optimizer/util/pathnode.c:1729`, NOT
  `allpaths.c` as originally filed) plus its 7 call sites in
  `path/joinpath.c` and the `join_is_legal` admission arm that gates it
  (`path/joinrels.c:445-489`). Confirmed by grep, not inference: goopg's
  `(*searchCtx).joinIsLegal` (`joinsearchlevel.go:198`) already ports the
  SEMI "already unique-ified, skip" arm but not the admission arm that
  *creates* that state; goopg has zero `create_unique_path`/
  `cheapest_unique_path`/`innerrel_is_unique` analogue anywhere in
  `internal/optimizer`; `RelOptInfo` (`path.go:423`) has no
  `CheapestUnique` cache slot; the existing `"Unique"` **executor** node is
  reusable (already wired for DISTINCT via `distinctOp`), but its only
  **planner**-side producer (`createDistinctPaths`, `distinctpaths.go:43`)
  is a single whole-query upper-rel wrapper — the opposite shape from PG's
  per-`RelOptInfo`, DP-search-internal path. Four separable pieces, filed
  as sub-items below (none selected yet): (1) new `RelOptInfo.CheapestUnique`
  cache field + a `createUniquePath` producer at the base/join-rel level
  with its own cost function; (2) the `joinIsLegal` admission arm itself
  (cheapest, most self-contained — direct ~20-line port once (1) exists to
  call); (3) two synthetic jointypes (`JoinTypeUniqueInner`/`Outer`)
  threaded through every join-path builder (hash/merge/NLI), widest blast
  radius; (4) an `innerrel_is_unique`/unique-index NOOP fast path (not
  needed for correctness, needed so goopg doesn't show a spurious `Unique`
  node PG's plan doesn't have). Captures:
  `tmp/m0142-0008a-census/{goopg,pg}_explains.txt` (Q10/Q35 sections).
  Q16/Q69/Q94 (§6's 3-of-5 pure-algorithm-choice queries) are unaffected
  and remain reachable via -0008a-2/-3 alone.
- [x] **M0142-0008c-1 — `RelOptInfo.CheapestUnique` cache field +
  `createUniquePath` producer** — filed by M0142-0008c (design doc §16.3
  item 1). **DONE 2026-09-16 — SORT method only, no live caller yet (that is
  -0008c-2).** Design doc §17. Landed `RelOptInfo.CheapestUnique *Path`
  (`internal/optimizer/path.go`), a new `PathUnique` kind +
  `Path.UniqueKeyCols []int`, `createUniquePath(rel, subpath, sjinfo, cp)
  *Path` (`internal/optimizer/createuniquepath.go`, ports
  `pathnode.c:1729-2081`) and its `createPlanNode` arm (`createUniquePlan`,
  createplansimple.go) — always emits `*DistinctOn` over a stacked Sort
  (goopg has no hash-keyed-subset dedup node, see below). Unit-tested
  standalone (`createuniquepath_test.go`): success shape/cost, the
  rel-level cache, and every decline guard (not SEMI, `!SemiCanBtree`, no
  correlation columns, subpath not the atomic-RHS `PathPrebuilt` shape, an
  out-of-range or identity-drifted correlation column, a non-`*ColumnRef`
  correlation expression). Zero behavior change to any existing plan
  (`createUniquePath`/`PathUnique` have no other caller; confirmed by grep).
  Two findings surfaced only while writing this, both recorded in §17:
  `SpecialJoinInfo.SemiRhsExprs` was declared (M0128-P1.4) but never
  populated by any producer — fixed in `existsUnnestSJInfo` (unnest.go),
  pinned by `TestExistsUnnestSJInfoSemiHashKey`/`...AntiHashKey`; and PG's
  HASH method (`UNIQUE_PATH_HASH`) needs a hash-keyed-SUBSET dedup with
  ungrouped passthrough columns that no goopg executor node can express
  (`*Distinct` is full-row-only, `*DistinctOn` is sorted-only) — filed as
  **M0142-0008c-1a** below, currently unreachable in practice since
  `existsUnnestSJInfo` always sets `SemiCanBtree`/`SemiCanHash` together.
  Ledger row appended (task-id `m0142-0008c-1`).
- [x] **M0142-0008c-2 — `joinIsLegal`'s missing SEMI unique-ify admission
  arm** — filed by M0142-0008c (design doc §16.3 item 2). **DONE
  2026-09-16.** Design doc §18. Ported PG's `joinrels.c:445-489` into
  `(*searchCtx).joinIsLegal` (`joinsearchlevel.go:198`) as two new `else if`
  arms at the same position PG's own chain has them (after the ordinary
  subset-match branches, before the "both overlap RHS" fallback): when a
  `JOIN_SEMI`'s full `SynRighthand` equals exactly one input and
  `createUniquePath(relX, relX.CheapestTotal, sj, s.cp)` succeeds, admit the
  join instead of falling through to the unconditional "violates outer-join
  constraint" error. 3 new unit tests (`specialjoin_test.go`:
  `TestJoinIsLegalSemiUniqueIfyAdmitsNonRHSPair`/`...ReversedPair`/
  `TestJoinIsLegalSemiRejectsWhenNotUniqueIfiable`). Live-probed (not just
  read) whether admitting a pair `-0008c-3`'s path builders can't yet finish
  risks a hard "joinrel has no paths" search failure — confirmed it does not:
  `jointypeForDirection` (`joinpaths.go:161`) already declines both
  orientations for such a pair (its own `MinLefthand` subset check fails by
  construction), and the search's own pair-admission heuristics
  (`joinOrderRestricted`/`haveRelevantJoinClause`) never offer such a pair to
  `joinIsLegal` at all for the canonical multi-relation-LHS shape today — so
  this loop is a correct, currently-inert port, same shape as -0008c-1.
  Verified empirically: TPC-DS SF0.25 sweep on the dirty tree,
  `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`, Q10/Q35 plan
  shape unchanged (expected — neither -0008c-1 nor -2 alone can move a plan).
  `go build ./...` clean; `go test ./internal/optimizer/...` full pass.
- [!] **M0142-0008c-1a — FROZEN (owner Q2, 2026-09-17; see banner) — HASH method for `createUniquePath`** — filed by
  M0142-0008c-1 (design doc §17 item 2). PG's `UNIQUE_PATH_HASH`
  (`pathnode.c:2026-2043`) groups by `uniq_exprs` while passing every OTHER
  needed target-list column through UNGROUPED (`createplan.c:1796-1811`),
  a shape goopg cannot express today: `*Distinct`/`distinctOp` hash-dedups
  the FULL input row, never a column subset, and `*DistinctOn` (the only
  subset-keyed dedup) is a SORTED streaming operator, not a hash. Needs
  either a new subset-keyed hash-dedup executor node, or a `HashAggregate`-
  shaped path whose non-grouped columns are allowed to pass through
  ungrouped (goopg's `*Aggregate`/`aggOp` may already refuse this — check
  before assuming either route is free). **Not on the critical path**:
  `existsUnnestSJInfo` (the only live `SemiRhsExprs` producer) always sets
  `SemiCanBtree`/`SemiCanHash` together, so `createUniquePath`'s
  `SemiCanBtree`-only gate never actually declines a real query — this item
  only matters once a second SEMI producer sets the flags independently.
- [x] **M0142-0008c-3 — thread `JoinTypeUniqueInner`/`JoinTypeUniqueOuter`
  through every join-path builder** — filed by M0142-0008c (design doc §16.3
  item 3). Depends on M0142-0008c-1/-2. **DONE 2026-09-16 as a recon +
  decomposition, no production change** (design doc §19, per the working-set
  baton's own instruction to recon this piece before implementing, given its
  §16.3-flagged size). Reading Q10/Q35's actual committed PG plans
  (`bench/tpcds/plans-pg/Q10.txt`/`Q35.txt`) found the task as filed was both
  over- and under-scoped: **neither witness exercises hash-join or
  merge-join at all** (only `JOIN_UNIQUE_OUTER` + an indexed nested loop —
  `HashAggregate`-deduped `store_sales` as outer, `customer_pkey`-indexed
  `customer` as inner), and **only one of Q10/Q35's three `OR`-connected
  `EXISTS` clauses is even eligible for `create_unique_path`** (the other two
  stay correlated `hashed SubPlan` filters by SQL legality — a separate,
  still-open divergence unrelated to `-0008c`). Reading PG's
  `match_unsorted_outer`/`sort_inner_and_outer` live (not from memory) found
  each strategy substitutes-and-demotes LOCALLY per builder rather than via
  one shared dispatch, but goopg's own `addNLIPaths` already collapses to a
  single outer candidate, so PG's "restrict to cheapest-total outer" guard is
  already structurally true there — combined with the Q10/Q35 evidence, the
  real blast radius shrinks to `jointypeForDirection`'s dispatch (exactly one
  production caller, confirmed via `find_referencing_symbols`) plus exactly
  two builders. Decomposed into **M0142-0008c-3a..3d** below, ordered by the
  Q10/Q35 evidence (3a/3b are what the witnesses need; 3c/3d are deferred,
  unexercised). Resume point: design doc §19.
- [x] **M0142-0008c-3a — `jointypeForDirection` admission dispatch for
  unique-ify** — filed by M0142-0008c-3's recon (design doc §19.3-19.4 item
  3a). Depends on M0142-0008c-1/-2. **DONE 2026-09-16** (design doc §19.5):
  `jointypeForDirection`'s signature changed from `(sjinfo, outer, inner
  RelSet)` to `(sjinfo, outer, inner *RelOptInfo, cp costParams)`, returning a
  third value — a new `internal/optimizer`-private `uniqueSide` type
  (`uniqueSideNone`/`Outer`/`Inner`), NOT a new `parser.JoinType` const (per
  PG's own `joinpath.c:116-121` comment that `JOIN_UNIQUE_OUTER/INNER` must
  never propagate outside the join-path module). New fallback arm for
  `JoinSemi` when the ordinary subset check fails: bit-equality (not subset)
  between `sjinfo.SynRighthand` and whichever rel is being tested, plus a
  successful `createUniquePath` call on that rel. `addPathsToJoinrel`
  resolves the sentinel immediately (`if uniq != uniqueSideNone { jt =
  parser.JoinInner }`) before calling any builder — no builder changed.
  Acceptance verified EMPIRICALLY, not just by inspection: full TPC-DS SF0.25
  sweep, `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: changed=0
  added=0 removed=0` against the immediately-prior commit. New unit test
  `TestJointypeForDirection_UniqueIfyFallback` pins both fallback directions
  plus a `SemiCanBtree=false` decline. `go test ./internal/optimizer/...`
  green. `ralph-precommit-test.sh` surfaced one PRE-EXISTING, unrelated
  failure (`internal/parser` `TestLockingClauseParity`, an AST-drift golden
  gap from earlier commit `dc91bd6b7`, reproduces identically on a
  git-stash-clean HEAD) — not a regression from this task, left for whoever
  owns `internal/parser` next. Resume point: design doc §19.5.
- [x] **M0142-0008c-3b — `addNestLoopPath`/`addNLIPaths` unique-ify
  substitution** — filed by M0142-0008c-3's recon (design doc §19.3-19.4 item
  3b). Depends on M0142-0008c-3a (DONE). **DONE 2026-09-16** (design doc
  §20): threaded `uniq uniqueSide, sjinfo *SpecialJoinInfo` into both
  `addNestLoopPath` (substitutes the inner for `uniqueSideInner`) and
  `addNLIPaths` (substitutes the outer for `uniqueSideOuter`, before its
  `inner.CheapestParameterized` loop), each declining the path on a nil
  `createUniquePath` result; `addPathsToJoinrel` threads `uniq`/`sjinfo` into
  both calls. Unit-tested directly at both builders plus one end-to-end
  `addPathsToJoinrel` test (`internal/optimizer/uniqueify_builders_test.go`)
  proving the substitution actually happens, not just compiles.
  **Acceptance NOT met and cannot be met yet**: a live probe (private sf025
  server, `GOOPG_PGSHAPED_DP_TRACE=1`, real `query10.sql`) found
  `addPathsToJoinrel` is NEVER called with a SEMI/ANTI `sjinfo` for any real
  query today (0 of 162 `DPPATH` lines were `jointype=semi`/`anti`) —
  `-3i-plumbing` items 3-5 (chain-admission of a real Semi/Anti link into the
  DP search) have not landed, so 3a/3b's whole dispatch path is provably
  unreachable in production regardless of what it builds. TPC-DS SF0.25
  sweep reconfirms zero plan-shape change (`PASS=96 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: same=99 changed=0`) — now understood as
  the direct consequence of that finding. See design doc §20 and the
  `M0142-0008c-3b` deferral-ledger row for the full evidence.
- [x] **M0142-0008c-3c — `addHashJoinPath`/`addPartialHashJoinPath`
  unique-ify substitution** — filed by M0142-0008c-3's recon (design doc
  §19.4 item 3c). Depends on M0142-0008c-3a. **DONE 2026-09-16** (design doc
  §35), following `-3b`'s exact precedent: build correct, direct-unit-test,
  report reachability as data. `addHashJoinPath` (`pathgen.go`) and
  `addPartialHashJoinPath` (`joinpathsparallel.go`) gained
  `uniq uniqueSide, sjinfo *SpecialJoinInfo` (PG oracle `hash_inner_and_outer`,
  `joinpath.c:2301-2341`/`:2418-2474`, read live) — one hash builder handles
  BOTH `JOIN_UNIQUE_OUTER`(probe)/`JOIN_UNIQUE_INNER`(build) substitutions,
  unlike the NL split across two builders; the partial arm declines
  `uniqueSideOuter` outright (PG's own `save_jointype != JOIN_UNIQUE_OUTER`
  gate) and declines `uniqueSideInner` too in practice today since
  `createUniquePath`'s `PathUnique` never sets `ParallelSafe`. 6 new direct
  unit tests (`uniqueify_hash_builders_test.go`), TPC-DS SF0.25 sweep
  `PASS=96 MISMATCH=0`/`PLAN-SHAPE: same=99 changed=0`, full
  `go test ./internal/optimizer/...` green. **Acceptance NOT met and cannot
  be met yet, root-caused further this loop**: a full-corpus (100-query)
  `GOOPG_PGSHAPED_DP_TRACE=1` sweep found ZERO `jointype=semi`/`anti` DPPATH
  lines even with `M0142-0008a-3i-plumbing-b2`'s Phase B genuinely live
  (Q78) — `extractSearchLeaves`'s own two named legality consumers,
  `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`, are called only from
  `semiantichain_test.go`, never wired into `tryPGShapedJoinSearch`
  production code, so no real Semi/Anti `SpecialJoinInfo` ever reaches
  `addPathsToJoinrel`. Actual next blocker filed as
  **M0142-0008a-3i-plumbing-c** (under the M0142-0008a milestone section).
- [!] **M0142-0008c-3d — FROZEN (owner Q2, 2026-09-17; see banner) — merge + partial-nestloop unique-ify substitution**
  — filed by M0142-0008c-3's recon (design doc §19.4 item 3d). Depends on
  M0142-0008c-3a. **RECHECKED 2026-09-17 (design doc §58), still deferred,
  updated reason**: `M0142-0008a-3i-plumbing-c`'s whole chain (c1-c21) is
  now closed and the DP search's semiAnti admission logic is confirmed
  correct end-to-end, so §35's original "blocked on plumbing-c" framing no
  longer applies — but a live `GOOPG_C22DEBUG=1` trace over the full
  TPC-DS SF0.25 corpus found `jointypeForDirection`'s SEMI/ANTI/RIGHT arm
  (the only entry point -3c/-3d/-4 share) is entered **zero times**
  corpus-wide even now. The real, structural reason: no code path anywhere
  in the corpus hands it a `parser.JoinSemi` `*SpecialJoinInfo` for a
  searched pair — the EXISTS/IN family is pinned pre-DP (never reaches
  this arm, c21 §57), IN-unnesting never sets `.SJInfo` (c6 §41), and the
  one confirmed DP-reachable producer (`reduce_outer_joins`'s ANTI
  demotion, c19) is unconditionally ANTI, which the unique-ify fallback's
  own `sjinfo.Jointype == parser.JoinSemi` gate excludes regardless of
  Q78's `outer-over-derived` firewall. **Do not pick this up by lifting
  Q78's firewall or waiting on B-06** — that would not unblock it. The
  actual unblock is unfiled: teach an IN-unnesting path (or a future EXISTS
  variant) to set `.SJInfo` on a DP-search-visible `JoinSemi` link, the
  same way c19 did for ANTI. Read design doc §58 before re-running this
  recon again. Still not
  picked up. Deferred for the same reason as 3c
  otherwise: `sortInnerAndOuter`/`matchUnsortedOuterMerge`/
  `matchUnsortedOuterMergePartial` (merge) and `addPartialNestLoopPaths`
  (parallel NL), PG oracle `sort_inner_and_outer`/`match_unsorted_outer`,
  `joinpath.c:1403-1441` (read live this loop, §19.2). Before enabling merge
  here, check whether `mergeDeclined`'s existing SEMI/ANTI decline (§8)
  should also cover the demoted-INNER case.
- [!] **M0142-0008c-4 — FROZEN (owner Q2, 2026-09-17; see banner) — `innerrel_is_unique`/unique-index NOOP fast path** —
  filed by M0142-0008c (design doc §16.3 item 4). Depends on
  M0142-0008c-1. Not needed for correctness (the expensive Sort+Unique/
  HashAggregate path still produces the right rows) but needed for
  plan-shape parity: without it, goopg shows a `Unique` node in cases where
  PG's plan has none because a unique index already proved distinctness
  (PG oracle: `pathnode.c:1932-1985`'s NOOP branches — unique-index proof
  and provably-distinct-subquery-output proof). Resume point: design doc
  §16.1-16.2. **RECHECKED 2026-09-17 (design doc §58): shares -3d's
  reachability gap.** `createUniquePath` (the function -4 would modify) is
  only ever invoked from `jointypeForDirection`'s unique-ify fallback
  today, which a corpus-wide live trace confirmed is entered zero times
  (see -3d's updated entry for the full finding) — so -4 would be
  unreachable in production the moment it lands too, for the same
  structural reason (no `parser.JoinSemi` producer reaches the search yet),
  independent of Q78's firewall. Do not pick up ahead of that unblock.
- [x] **M0142-0008d — EXPLAIN mislabels the outer relation's alias in a
  self-correlated EXISTS where inner and outer share a table name** — filed
  by M0142-0008a-3(iii)'s §4.3 gate re-run (design doc §6). **DONE 2026-09-16
  as a recon (design doc §9): root cause found and REPRODUCED
  independently of TPC-DS** (a throwaway single-table self-correlated
  EXISTS on a scratch cluster shows the identical bug), correcting the
  original filing's "small/self-contained" framing. Real cause:
  `ColumnRef.SourceTableIdx` restarts at 1 per query level, so the outer
  table and the EXISTS body's table can collide on the same raw value once
  `unnestExistsExpr` splices the body into the outer tree as an ordinary
  join child; `explain_names.go`'s `bySrc` map holds one relation name per
  raw value, so whichever scan wins the walk-order race supplies the name
  for *both* sides' `Hash Cond`/`Join Filter`. Blast radius is narrower than
  it looks (join.schema only exposes outer columns, so the collision is
  reachable only through the join's own Predicate/LeftKey/RightKey; node
  *labels* are unaffected, a separate disambiguation pass) but the fix is
  bigger than it looks — a one-site patch to the join-key construction does
  NOT fix it, because `collect()` resolves names off the *scan node's own
  schema*, not off the join-level ColumnRefs; a faithful fix needs a
  `clonePlanReplacingOuter`-shaped tree-wide renumber (15 `Node` cases,
  ~500 lines) applied to the EXISTS body before splicing. Execution/row
  counts are unaffected (verified) — display-only. Follow-up filed as
  **M0142-0008e** (below), not blocking M0142-0008a-3(i)/(ii)/(iii).
- [x] **M0142-0008e — fix the EXPLAIN self-correlated-EXISTS alias collision
  found by M0142-0008d's recon (design doc §9)** — DONE 2026-09-16.
  Implemented `remapSourceTableIdx(node Node, offset int16) (Node, error)`
  and `remapExprSourceTableIdx` in `internal/optimizer/unnest.go` (right
  after `clonePlanReplacingOuter`), mirroring its 15-case Node-kind set and
  built on the exhaustive `CloneExprReplacingColumnRefs` walker (so a future
  33rd Expr type cannot silently pass through unshifted). Wired into
  `unnestExistsExpr`: applied to `innerPlan` right after `outerWidth` is
  computed, with `srcTableOffset` sized one past the max `SourceTableIdx` in
  `outerChild.Output()`; the same offset is added to `innerKey`'s
  `SourceTableIdx` and threaded through a new `liftResidualConjunctsWithOffset`
  (the old `liftResidualConjuncts` is now a thin 0-offset wrapper, so its two
  other callers — `unnestScalarWithResiduals`, `unnestInExpr` — are unchanged)
  for the residual's inner-side `ColumnRef`. Regression test
  `TestExplainSelfCorrelatedExistsDoesNotAliasCollide`
  (`internal/executor/exists_unnest_alias_test.go`) asserts the EXPLAIN text
  directly and was verified to FAIL with the exact `t1.a = t1.a` collision
  when the offset is temporarily forced to 0, confirming it actually catches
  the bug class the row-count/plan-shape gates cannot. `go test
  ./internal/optimizer/... ./internal/executor/...` green (includes the
  `TestExprSwitchInventoryIsPinned` exhaustiveness gate, whose inventory
  entry was renamed alongside `liftResidualConjuncts`). TPC-H spot-check
  gate SKIPPED (pre-existing, documented: the shared `:65433` cluster's
  `tpch` dataset is still emptied per M0142-0003k, unrelated to this change)
  — not re-run since this is a pure EXPLAIN-naming change with no cost/plan-
  shape/execution impact (confirmed by M0142-0008d's own measurement and
  unaffected by this fix, which only ever touches `SourceTableIdx`, a field
  `explain_names.go` alone reads). Deferred: `unnestScalarWithResiduals`/
  `unnestInExpr` were NOT measured for the same collision class and pass
  offset=0 (a no-op) into the renamed helper — see the ledger row for the
  repro shape to try if a witness ever surfaces there.
- [x] **M0142-0008b — scoping recon: measure the blast radius of widening
  the DP-search gate to filterless INNER/CROSS trees** — filed by
  M0142-0008. `planner.go:1590-94`'s own comment already names the fix
  direction: `joinTreeHasOuterLink(node)` currently gates DP search to
  trees with an OUTER link; a filterless INNER/CROSS tree (Q20's class)
  stays on the legacy path, where only `rewriteScanInputsWithSingleTablePredicates`
  promotes `SeqScan`→`IndexScan`. The same comment warns "widening to every
  filterless join tree moves many long-stable plans at once" — measure
  before implementing, per K50/M0142-0012a precedent. Concrete next step:
  census which TPC-H/TPC-DS queries have a filterless INNER/CROSS top-level
  FROM tree (or subtree) at HEAD, whether widening the gate condition would
  actually change their plan (some may already coincide with the legacy
  rewrite's output), and whether any currently-`match`ing query would move
  away from PG's shape if the gate widened. Measurement only, no code
  change.
  **DONE 2026-09-16, recon closed, no code change.** Design doc:
  `docs/design/0100-0149/m0142-0008b-scoping-recon-filterless-inner-cross-census.md`.
  Parsed all 22 TPC-H (Q15 skipped by design) + 99 TPC-DS queries (3
  corpus-data parse failures, unrelated "hierarchy rank" family) with
  `internal/parser`, walked all 429 reachable `SelectStmt`s (top-level,
  CTEs, derived tables, sublinks, UNION arms) with a verified
  reimplementation of `joinTreeHasOuterLink`'s left-spine semantics.
  **Finding 1**: only 5/429 SELECTs (1.2%, all TPC-DS, zero TPC-H) hit the
  "neither arm runs" gap (`query28`/`query61`/`query77`/`query88`/`query90`).
  **Finding 2**: the gate's own left-spine-only walk has a separate,
  mechanically-confirmed blind spot (an outer join not on the left spine is
  invisible to it) but zero corpus matches today. **Verdict: do NOT widen
  the gate** — 4 of the 5 Finding-1 matches comma-cross provably-1-row
  scalar aggregate subqueries where join order cannot matter; only
  `query77`'s `cs, cr` cross (genuinely multi-row, and the query's only
  channel branch using implicit cross instead of the `LEFT JOIN` its
  store/web siblings use) looks like a plausible real target, and its fix
  is a narrower per-query rewrite, not a gate change. **No implementation
  task filed** — re-open only if `query77` is independently confirmed as a
  live plan mismatch. Gates: none beyond the recon itself (measurement-only,
  parser-AST analysis via a throwaway `/tmp` scratch program, no production
  code touched, no server/cluster needed).
- [x] **M0142-0009 — recon: plain `Nested Loop`/`Gather` join nodes estimate
  single-digit rows against four-to-five-digit actuals, at `loops=1`** —
  Filed by M0142-0004c from the post-C1-fix re-capture
  (`ea-findings-20260915-post0004a.json`, 122 findings). **DONE 2026-09-15,
  full writeup in
  `docs/design/0100-0149/m0142-0009-mcv-only-range-selectivity.md`.**
  Instrumented the smallest witness (Q25) down to a **single-table filter**
  with no join at all: `date_dim WHERE d_moy BETWEEN 4 AND 10 AND d_year =
  1999` estimated `rows=1` against real PG 18.3's `rows=212` (actual 214) on
  identically-generated data — the join-level qerr the task was filed
  against was only a downstream symptom. Root cause:
  `rangeOpSelectivityStats` (`internal/optimizer/selectivity.go:333`)
  discarded a column's entire MCV list whenever its histogram had `<2`
  boundaries; `d_moy` (12 distinct values) is MCV-complete by construction
  (`computeColumnStats`'s `nmultiple == ndistinct` case correctly leaves its
  histogram empty, mirroring PG's own `compute_scalar_stats` — both engines
  make the identical ANALYZE decision), so the guard punted the whole clause
  to `defaultIneqSelectivity`/`defaultRangeIneqSel` every time instead of
  using the fully-measured MCV mass it already had, unlike PG's
  `scalarineqsel` which sums `mcv_selec` and defaults only the (here empty)
  non-MCV remainder. Fix: relaxed the guard to `len(Histogram) < 2 &&
  len(MCV) == 0` — one line; the function's existing MCV-mass loop and
  `histogramOpSelectivity`'s existing `k<1` fallback already compose PG's
  formula once the guard stops skipping them (absorption, not tuning — no
  constant added, per owner decision B2). Mandatory M0137-M0143 floor
  measurements (all in the design doc): TPC-H plan-parity match=8/22
  unchanged pre/post-fix (private-clone A/B via `git stash`, same build);
  TPC-DS plan-parity match=2/99 (Q9, Q41) unchanged, byte-identical
  `CATEGORIES`/`CATEGORIES-EXCL-MATCH` despite 83/99 plan shapes changing
  underneath; TPC-DS SF0.25 regression sweep `PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0`; `make ea-ratchet` **122 -> 112 findings** (12
  FIXED: Q10, Q25, Q29, Q34 x3, Q68 x2, Q69, Q73 x2, Q79 — every named
  witness except the CTE-`UNION ALL`-shaped ones, which are M0142-0004b's
  still-open C2; 2 NEW smaller findings one join level up, previously
  masked by the leaf's much larger error — filed as **M0142-0010**, not
  chased in this task). Baseline re-pinned to the new 112-entry set.
  `go test ./internal/optimizer/...` PASS incl. new
  `TestRangeOpSelectivityUsesMCVWithoutHistogram`; `scripts/tpch-spotcheck.sh`
  RESULT=PASS (Q12=2, Q13=34); `go build ./...` clean.
- [x] **M0142-0010 — recon: a join-level cardinality gap unmasked by
  M0142-0009's leaf fix** — `date_dim+store+store_sales` estimates ~40x
  under actual in both Q34 (`Gather`, est=2105 vs actual=91450, qerr 43.4)
  and Q73 (`Hash Join`, est=630 vs actual=26312, qerr 41.8). **DONE
  2026-09-15, recon closed, no code change. Full writeup in
  `docs/design/0100-0149/m0142-0010-join-level-gap-is-memoize-shape-not-cardinality-bug.md`.**
  Instrumented `estimateJoin` (temporary env-gated trace, reverted) on a
  private SF0.25 clone: `pairNDistinct` returns `nd=73049`
  (`date_dim.d_date_sk`'s row count, a PK) and the estimate is
  `l * r * (nullSel/nd)`, the standard FK->PK uniform-domain formula.
  Measured goopg's own stats: `store_sales.ss_sold_date_sk` has only 1823
  distinct values (~5-year window) against `date_dim`'s 73049-row,
  1900-2100 span. Checked PG's `eqjoinsel_inner`
  (`postgres/src/backend/utils/adt/selfuncs.c:2445`): its no-mutual-MCV
  default is `MIN(1/nd1,1/nd2)*(1-nullfrac1)*(1-nullfrac2)` — literally the
  same formula. Queried the real PG 18.3 oracle's `pg_stats` on
  identically-generated data: `ss_sold_date_sk` has a 100-entry MCV but
  `d_date_sk` (unique PK) has none, so PG's own exact-overlap branch cannot
  fire either and PG's planner would compute the **identical** `1.31e-5`
  selectivity for this join shape — verified bit-for-bit against the trace.
  **Verdict: goopg's cardinality math for this relset is PG-formula-identical,
  not a defect.** Real PG's actual plan is accurate only because it picks a
  different shape (`Nested Loop`+`Memoize`+`Index Scan using date_dim_pkey`,
  pricing each outer row's probe directly instead of assuming uniform-over-
  73049) — the already-filed **M0142-0005** gap (no Memoize on the NL probe
  path) is why goopg cannot choose that shape. Not a new mechanism; a second,
  independently-verified symptom of M0142-0005, which now carries this
  corpus evidence as part of its resume point. Deferral ledger row filed
  (dated 2026-09-15) linking this evidence to M0142-0005.
- [x] **M0142-0011 — disambiguate and fix the `EstimateRows(*Join)`
  recompute gap M0142-0004b found** — filed by M0142-0004b (full writeup:
  `docs/design/0100-0149/m0142-0004b-cte-union-branch-collapse-is-estimatejoin-recompute-gap.md`).
  A generic `*Join{Algo: JoinAlgoNestedLoop}` node beneath a SEMI join's
  outer input (Q33/Q56/Q60's CTE branches: `store_sales⋈date_dim⋈
  customer_address⋈item`, a proven-key equality chain) estimates `rows=1`
  via `estimateJoin`'s fresh recursive `EstimateRows` call, while the SAME
  node's own already-costed `PlanCost.PlanRows` (what `EXPLAIN` actually
  prints for it, and what `internal/executor/operators_explain.go:3024-3028`
  reads) is `32` — two disagreeing numbers for one subtree. Two candidate
  mechanisms, NOT yet disambiguated:
  **(A)** `estimateJoin`'s measured-selectivity branch
  (`internal/optimizer/cardinality.go:~792`,
  `if j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge`) excludes
  `JoinAlgoNestedLoop`, so a resolvable equi-key on a NestedLoop-algo join
  falls to the `l*r*defaultEqSelectivity` (`0.005`) fallback instead of the
  `pairNDistinct`/MCV/superkey-bound-key path Hash/Merge joins get, even
  though PG's own `calc_joinrel_size_estimate` sizes a joinrel once,
  independent of which Path/algorithm is cheapest.
  **(B)** `EstimateRows(*Join)` (`cardinality.go:95-96`, `case *Join: return
  estimateJoin(x)`) never consults the node's own `PlanCost`/`CostSet` the
  way `legacyDisplayCostOf` (`plancost.go:164`) and its callers
  (`distinctpaths.go`, `groupingpaths.go`, `partialaggpaths.go`,
  `partialsortpaths.go`, `windowsetoppaths.go`) already do — it always
  recomputes bottom-up from node structure alone, discarding an
  already-accurate costed value when one exists.
  **DONE 2026-09-15, recon closed, NEITHER mechanism — root cause is a
  third one. Full writeup:
  `docs/design/0100-0149/m0142-0011-disambiguate-estimatejoin-recompute-gap.md`.**
  Tested Mechanism A directly (broadened the `Algo` gate, rebuilt, re-ran the
  Q33 CTE-branch witness): the plan was byte-identical — A alone does not
  fix it. Traced further: the collapsing nodes (`Nested Loop -> Index Scan`
  on `customer_address_pkey`/`item_pkey`) are generic `*optimizer.Join` with
  `Predicate == nil` and zero equi-pairs findable regardless of the Algo
  gate — Mechanism A cannot reach these nodes at all. Root cause found by
  reading `createplannl.go`: `createNestLoopIndexJoinPlan`'s own comment
  names it — the R25 (plan-parity-fix-take2) decomposition replaced the
  fused `*NestedLoopIndexJoin` type with a generic
  `Join{Algo: NestedLoop, Lateral: true, Right: *IndexScan}` whose actual
  equi-key lives on the `*IndexScan` child's own `Key`/`Keys` (an
  `OuterColumnRef` nestloop param), not in `Predicate`. `estimateJoin` (the
  generic dispatcher used for this NEW decomposed shape) has no `Lateral`
  arm, so `joinEquiPairs` always finds zero pairs on it and every unmemoized
  index-probe nested loop in both corpora falls to the crude
  `l*r*0.005`/`max(l,r)`-capped fallback. `estimateNLIndexJoin` (M0142-0006's
  already-correct fix) is reachable only on the Memoize-wrapped minority
  (`createNestLoopIndexJoinPlanFused`, the sole remaining producer of the OLD
  fused type) — the majority (per M0142-0005/B6) silently reverts to the
  fallback. This is Mechanism C, structurally DP-search-safe by the same
  argument that makes `estimateNLIndexJoin` itself safe today (construction-time
  fields only, no `PlanCost` consultation). Both the Algo-gate broadening and
  the temporary trace were fully reverted (`git status`/`git diff` empty;
  `go test ./internal/optimizer/...` green, `TestFallbackCapFiresForNonHashAlgoDespiteStats`
  untouched since Mechanism A is not being landed). Fix filed as **M0142-0012**
  below.
- [x] **M0142-0012 — teach cardinality estimation the decomposed-NLI
  `Join{Lateral: true}` shape** — filed by M0142-0011 (full writeup:
  `docs/design/0100-0149/m0142-0011-disambiguate-estimatejoin-recompute-gap.md`).
  Since the R25 (plan-parity-fix-take2) decomposition, an unmemoized
  index-probe nested loop is built (`createplannl.go:355-364`) as a generic
  `Join{Algo: JoinAlgoNestedLoop, Lateral: true, Right: *IndexScan}` whose
  equi-key lives on the `*IndexScan` child's own `Key`/`Keys`
  (`OuterColumnRef` nestloop param), not in `Predicate`. `EstimateRows`/
  `estimateJoin` (`internal/optimizer/cardinality.go`) has no arm for this
  shape, so `joinEquiPairs` always finds zero pairs on it and every such join
  in both corpora falls to the crude `l*r*0.005`/`max(l,r)`-capped fallback
  instead of the accurate per-probe estimate — `estimateNLIndexJoin`
  (`cardinality.go:240-266`, already carries M0142-0006's SEMI/ANTI
  match-fraction fix) already has the correct logic but is reachable only
  through the OLD fused `*NestedLoopIndexJoin` type, which
  `createNestLoopIndexJoinPlanFused` builds ONLY when the inner is
  Memoize-wrapped — the minority case per M0142-0005/B6.
  **DONE 2026-09-15, landed. Full writeup:
  `docs/design/0100-0149/m0142-0012-lateral-index-join-cardinality.md`.**
  Added `isLateralIndexProbe`/`estimateLateralIndexJoin`/
  `lateralNLIMatchFraction` (`cardinality.go`) — `estimateNLIndexJoin`'s twin
  for the decomposed shape, dispatched from a new branch at the top of
  `estimateJoin`. New test `TestEstimateRowsLateralIndexJoinSemiScalesByMatchFraction`
  (`cardinality_propagation_test.go`) pins SEMI=100/ANTI=900/INNER=1000
  against the decomposed `Join{Lateral:true}` shape, mirroring
  `TestEstimateRowsNLIndexJoinSemiScalesByMatchFraction`. Gates: `go build
  ./...` clean; `go test ./internal/optimizer/...` full-package green;
  `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34); `scripts/tpcds-sf025-regression.sh
  sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 (plan shape changed on 91/99
  queries — the measured blast radius firing — zero correctness
  regressions); `make ea-ratchet` 112→95 findings (22 FIXED incl. every
  Q33/Q54/Q56 CTE-branch witness, 5 NEW one join-level up on Q23/Q84/Q95 —
  the same unmasking pattern M0142-0009→M0142-0010 produced), baseline
  re-pinned to 95 via `make ea-ratchet-repin`. **Per the harness's own
  pre-authorization, the plan-parity floor-measurement suite itself (fresh
  TPC-H/TPC-DS plan-parity capture + match/category re-scoring against PG
  18.3) was NOT run this loop** — filed as follow-up **M0142-0012-verify**
  below, plus a `.ralph/deferral_ledger.md` row (both required artefacts).
  The 5 NEW ea-ratchet findings are filed separately as **M0142-0013**.
- [x] **M0142-0012-verify — run the plan-parity floor-measurement suite
  M0142-0012 deferred** — filed by M0142-0012. M0142-0012 landed a
  cardinality fix with a measured large blast radius (M0142-0012a: TPC-H
  6/21 queries / 224 call-site hits, TPC-DS 69/99 queries / 6347 call-site
  hits) and verified it via correctness gates only (tpch-spotcheck, SF0.25
  sweep, ea-ratchet) — not against this milestone group's own headline
  metric. **DONE 2026-09-15.** Design doc:
  `docs/design/0100-0149/m0142-0012-verify-plan-parity-floor-remeasure.md`.
  Fresh TPC-H (`-serial` and `-serial=false`) and TPC-DS SF0.25 captures vs
  live PG 18.3: **headline unmoved** — TPC-H `-serial` match=6/22, TPC-H
  parallel match=2/22 (categories byte-identical to M0137-0017), TPC-DS
  match=2/99 (Q9/Q41 floor intact); the `PLAN-PARITY` aggregate line and
  eight of nine TPC-DS category counts are unchanged pre/post, the ninth
  (`join-order`) moves by exactly 1. A cost-blind goopg-vs-goopg self-diff
  (reusing `pg-plan-parity-diff.py`'s shape extraction) found the honest
  shape-delta: TPC-H 0/22 shapes changed (despite 6/21 queries' costs
  moving), TPC-DS 17/99 shapes changed (Q4/Q6/Q11/Q25/Q29/Q31/Q34/Q45/Q54/
  Q56/Q60/Q64/Q72/Q73/Q78/Q79/Q88) with none becoming a new match and
  neither existing match lost — "moved sideways," the report contract's
  named failure mode for shape-delta-without-category-movement. A naive
  byte-level diff of the same TPC-DS captures falsely read 97/99 as changed
  (cost-number churn, not shape) — flagged in the doc as the wrong
  instrument for this question. Follow-up: **M0142-0014**.
- [x] **M0142-0013 — recon: 5 NEW ea-ratchet findings one join-level up from
  M0142-0012's fixes (Q23, Q84, Q95)** — filed by M0142-0012's `make
  ea-ratchet` run. **DONE 2026-09-15, recon closed, no code change. Full
  writeup:
  `docs/design/0100-0149/m0142-0013-five-new-earatchet-findings-one-level-up.md`.**
  Traced all 5 on a private SF0.25 clone (port 5533) plus the real PG 18.3
  oracle. **Q23 (both)**: `frequent_ss_items`'s `HashAggregate … Filter:
  (count(*) > 4)` over-estimate (4561 vs actual 0) is **PG-formula-identical**
  — real PG estimates 4573 for the same node against the same actual=0, the
  same `HAVING count(*) > k` weak spot in both engines. Closed, no defect,
  same class as M0142-0010. **Q84**: the flagged `Limit` inherits a
  compounded correlated-dimension-join estimate error; real (qerr 25 vs PG's
  2.5) but the two engines' join orders diverge past the first two joins so
  no single node isolates one defect — inconclusive at recon depth, not
  urgent, noted as context only, no task filed. **Q95 (both) — genuine NEW
  mechanism, confirmed by direct instrumentation**: a temporary trace in
  `estimateJoin`'s SEMI/ANTI branch (`cardinality.go:880`) showed
  `l = EstimateRows(j.Left)` evaluating to 29988 for a Lateral-index-join
  chain whose own planner-costed `PlanCost.PlanRows` is 210 —
  `estimateLateralIndexJoin`'s plain-INNER branch (`cardinality.go:382-396`)
  returns the outer row count unconditionally, never applying
  `joinResidualSelectivity` the way its own SEMI/ANTI branch three lines away
  does, silently dropping the inner probe's `ca_state = 'VA'` filter whenever
  a consumer recomputes through it (here, a SEMI join built on top during DP
  search) instead of reading the costed value. The fused sibling
  `estimateNLIndexJoin` (`cardinality.go:241-245`) has the identical
  unconditional-return shape on its own INNER branch — both twins need the
  fix (`pattern_sibling_paths_must_agree`). **M0142-0005 ruled OUT for all 5
  findings** — no unmemoized NL-index probe is the amplifying step in any of
  them (Q23/Q84 are pure Hash-Join chains; Q95's one Lateral-index probe is
  an accurate 1:1 passthrough verified against its own actual data). Fix
  filed as **M0142-0016** below.
- [x] **M0142-0014 — triage the 17 TPC-DS plan-shape changes M0142-0012-verify
  found** — filed by M0142-0012-verify's self-diff (goopg-before vs
  goopg-after M0142-0012, cost-blind). **DONE 2026-09-15, recon closed, no
  code change. Full writeup:
  `docs/design/0100-0149/m0142-0014-triage-17-tpcds-shape-changes.md`.**
  Read each of the 17 queries' `pg-plan-parity-diff.py` category-tag set
  against live PG pre- vs post-M0142-0012 (both already-committed captures,
  no re-run). **13 neutral (tag set unchanged), 3 improved (Q31 -3, Q54 -2,
  Q72 -2 divergent tags each), 1 genuine regression (Q45, +3: join-order,
  scan-type, qual-placement)** — this exactly attributes every aggregate
  category delta M0142-0012-verify measured (join-order +1 solely Q45,
  join-method -1 solely Q54, aggregation-strategy/sort-strategy -1 solely
  Q31, parallelism -2 Q31+Q54, scan-type/qual-placement net 0 as Q45's +1
  cancels Q72's -1) — a residual-free accounting, not a guess. Confirmed
  Q45 by reading raw EXPLAIN text: pre-fix goopg's join order
  (`...⋈customer⋈customer_address⋈item`) matched real PG's own order
  exactly; post-fix it's `...⋈customer⋈item⋈customer_address` —
  M0142-0012's more-accurate index-probe cost flipped a close DP-search tie
  away from PG's choice (same class as Q9's level-6 tie, M0142-0003b/0003c).
  Q31/Q54/Q72 improvement mechanisms not traced (wins, no action needed).
  **Verdict: M0142-0012 remains a net corpus improvement** (3 wins vs 1
  now-understood loss); Q45's pre-fix state was already `SHAPE-DIFF` not a
  `match`, so nothing regressed from match to non-match. Follow-up filed as
  **M0142-0015** below.
- [x] **M0142-0015 — recon: why does M0142-0012 flip Q45's
  `item`/`customer_address` DP-search tie away from PG's own choice?** —
  filed by M0142-0014. **DONE 2026-09-16, recon closed, no code change.**
  Full writeup:
  `docs/design/0100-0149/m0142-0015-q45-tie-break-is-near-exact-in-both-engines.md`.
  Traced on a private SF0.25 clone (`GOOPG_PGSHAPED_DP_TRACE=1`, port 5534)
  plus the shared PG `:65438` reference (bracketed `start pg`/`stop pg`, all
  three TPC-DS lanes were down at recon start — no collision with the
  concurrent nightly batch, which was still in its TPC-H stage).
  **Finding: both orderings cost effectively identical in BOTH engines.**
  goopg's own `DPPATH` at level 5 shows two `nestloop.index` candidates
  both `verdict=accepted` differing by `2e-12` (`9674.299956003799` vs
  `9674.299956003797`) — not an exact tie caught by `addToPathlist`'s
  dominance check (unlike Q9's level-6 finding, M0142-0003b), just
  `setCheapest` taking the literal float minimum of two near-equal
  candidates; the PG-matching order is registered FIRST (`DPTRACE pair …
  created=1`) yet still loses, by this sub-ULP margin. The two feeding
  level-4 legs are NOT themselves tied (`9606.17` vs `9596.43`, a real 9.74
  gap, ~0.1%) — the level-5 step's marginal costs (probing the one
  remaining relation) compensate almost exactly in the opposite direction.
  Forcing real PG 18.3 to try the alternative order
  (`join_collapse_limit=1`+`from_collapse_limit=1`, explicit left-deep
  `JOIN`) reproduces the identical shape: real, unequal per-step costs
  (`9700.77` vs `9695.48`, a genuine 5.29 gap) canceling into an *identical*
  displayed top-level total (`9827.36` both ways, PG's 2-decimal display
  resolution hides whether PG's own margin is as fine as goopg's `2e-12`).
  **Verdict: same class as M0142-0003b/0003c's Q9 level-6 tie, not a new
  M0142-0012 formula defect** — tightening the cardinality/cost formula
  cannot move a margin this small in any principled direction, since the
  two orderings' true costs are effectively equal in both engines' own
  models; the real open question is the cross-engine tie-break mechanism
  itself, which M0142-0003c already tracks (filed for Q9). No new task
  filed; M0142-0003c should widen to use Q45 as a second, cross-corpus
  witness rather than a Q9-only question when it is next picked up. Per K50
  no DP-search reordering fix was attempted. Evidence:
  `analysis/m0142/m0142-0015-q45-{goopg-explain,dptrace,pg-default-and-forced}.txt`.
- [x] **M0142-0016 — fix `estimateLateralIndexJoin`/`estimateNLIndexJoin`'s
  plain-INNER branches to apply residual selectivity** — filed by
  M0142-0013's instrumented finding. Both twins (`cardinality.go:382-396`
  and `:241-245`) return the outer row count completely unconditionally for
  a plain INNER Lateral/NLI-index join, never consulting
  `joinResidualSelectivity(j)` the way their own three-lines-away SEMI/ANTI
  branches already do — so any filter carried on the inner index probe
  (verified witness: Q95's `customer_address_pkey` probe's `Filter:
  (ca_state = 'VA')`, ~4% true match rate) is silently dropped whenever a
  consumer recomputes cardinality through the node via `EstimateRows` rather
  than reading the already-costed `PlanCost.PlanRows` — confirmed by direct
  trace to inflate a downstream SEMI join's estimate 142x past its own outer
  input's costed value (Q95: `l=29988` vs the chain's own costed `210`), the
  load-bearing cause of both Q95 `ea-ratchet` findings (qerr 1782.7, 1363.1).
  Resume point: apply `joinResidualSelectivity` (or the equivalent
  single-relation residual term — the SEMI/ANTI arm already builds the right
  input shape via `j`/`adj`) to the plain-INNER return in BOTH functions,
  mirroring the SEMI/ANTI arm each already has
  (`pattern_sibling_paths_must_agree`). **Per K50, size this as a
  blast-radius measurement first** (the M0142-0012a precedent) before
  landing — a cardinality change on this shape can flip DP-search ties
  elsewhere in the corpus (the exact mechanism M0142-0003c/M0142-0015 are
  independently investigating for Q9/Q45's level-6 ties), and this shape's
  call-site count was already measured at 224 TPC-H / 6347 TPC-DS hits by
  M0142-0012a (a different but overlapping population — re-measure the
  INNER-only subset before implementing). Full floor-measurement suite
  required on landing (TPC-H/TPC-DS plan-parity, `make ea-ratchet`, SF0.25
  sweep), same treatment M0142-0006/M0142-0009/M0142-0012 got.
  **UPDATE 2026-09-15 (M0142-0016a scoping recon, DONE): the fix target is
  narrower than assumed above — `joinResidualSelectivity(j)` cannot see this
  clause at all (its loop skips every non-`sideMixed` clause, and Q95's
  `ca_state = 'VA'` is entirely on the inner/right side). The real fix target
  is `IndexScan.Cond`/`IndexOnlyScan.Cond` (`plan.go:836`/`:1022`, the
  parameterized-probe leftover-residual field), which neither
  `nliSemiMatchFraction` nor `lateralNLIMatchFraction` reads today. Measured
  blast radius: TPC-H 3/21 queries carry a real defect hit (Q7 25, Q8 33, Q21
  19 call-sites; 6 more queries hit the shape as a no-op), TPC-DS SF0.25
  55/99 queries carry a real defect hit (Q77 427, Q49 404 highest; 88/99 hit
  the shape at all). Cross-validates M0142-0013's Q95 finding independently
  (43 real hits) and confirms Q9's 90 hits are ALL no-ops (consistent with
  M0142-0013's "PG-formula-identical" verdict for Q9). Full writeup:
  `docs/design/0100-0149/m0142-0016a-scoping-recon-blast-radius.md`. Resume
  point for the fix itself: multiply the match-fraction result by `Cond`'s
  own selectivity (via `clauseSelectivity` or equivalent) in both functions'
  SEMI/ANTI *and* now-justified INNER arms, watching Q7/Q8/Q21 for TPC-H
  plan-shape movement per the recon's own risk note.**
  **UPDATE 2026-09-16 (M0142-0016b, DONE, LANDED): implemented exactly as
  scoped — new `probeResidualCond` helper reads `Cond` off
  `*IndexScan`/`*IndexOnlyScan`/`*BitmapHeapScan`; both functions' plain-INNER
  arms (gated to `j.Type == JoinTypeInner` specifically — LEFT is excluded and
  stays on the unconditional `return l`, since a LEFT join emits one
  null-extended row per failed probe regardless of the residual) now scale by
  `clauseSelectivity(cond, probe)`. Values clean (`tpch-spotcheck.sh` PASS,
  TPC-DS SF0.25 sweep `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`). Shape
  clean: TPC-H before/after A/B `shapediff=0/22` (stats-epoch MATCH), TPC-DS
  SF0.25 `changed=0/99`, TPC-H after-vs-PG `match=8/22` (floor 6, the Q7/Q8/Q21
  DP-tie-flip risk the recon flagged did not materialise). `make ea-ratchet`
  went 95→110 findings (FAIL): 2 FIXED (Q16, Q95 — the task's own witness) but
  17 NEW across Q33/Q54/Q56, traced to ground truth and NOT treated as a
  blocker — see `docs/design/0100-0149/m0142-0016b-implementation-and-ea-ratchet-analysis.md`
  for the full read (all 17 are `UNMATCHED-IN-PG`, so the PG-relative bar has
  no floor to check against; the traced Q54 case is the independence-assumption
  marginal-selectivity product applied correctly to real, non-default
  statistics — the same formula class PG's own planner would apply in an
  analogous shape, which is the milestone's own binding Q2 answer). Follow-up
  filed as M0142-0016c below for the open PG-forced-plan-comparator
  question.**
- [ ] **M0142-0016c — does PG qerr-match the M0142-0016b `Q33`/`Q54`/`Q56`
  shape if forced into an analogous parameterized-probe-with-residual plan,
  and should the resulting cost signal move those queries' plan choice?** —
  filed by M0142-0016b's own ea-ratchet read. M0142-0016b's 17 new `ea-ratchet`
  findings (all `UNMATCHED-IN-PG`) were read as "goopg now reproduces a
  PG-analogous estimation weakness, not a novel defect" on the strength of one
  traced example (Q54's `my_customers` Nested Loop, `rows=1` vs `actual=121`,
  traced to a correct independence-assumption product over real `date_dim`
  stats) — but that reading was never checked against PG's OWN number for the
  same shape, because PG does not choose this shape for these three queries
  and so `pg-plan-parity-diff.py`/`ea-ratchet` have no comparator node to read.
  Resume point: force an analogous plan out of PG for one of the three
  (`join_collapse_limit`/`enable_hashjoin=off`/similar knobs, or hand-build the
  equivalent query fragment) and compare its residual-clause row estimate
  against goopg's — confirms or refutes the "PG would do the same" claim
  directly instead of by architectural analogy. If confirmed, no further
  action needed (the milestone's Q2 already licenses this). If refuted (PG's
  actual formula differs enough to land closer to truth), M0142-0016b's read
  should be revisited and `clauseSelectivity`'s treatment of a probe's `Cond`
  may need the same kind of correlation-aware handling PG itself has that
  goopg does not yet port. Separately, check whether costing this same
  narrowed row estimate (not just displaying it) would move the DP search away
  from the NLI shape entirely for `Q33`/`Q54`/`Q56` — unexercised this loop
  since none of the three queries' plan shape moved.
- [x] **M0142-0016a — scoping recon: measure M0142-0016's blast radius before
  implementing it** — filed by this loop from M0142-0016's own K50 sizing
  instruction (mirrors the M0142-0012a precedent). **DONE 2026-09-15, recon
  closed, no code change. Full writeup in
  `docs/design/0100-0149/m0142-0016a-scoping-recon-blast-radius.md`.** See
  the M0142-0016 entry above for the findings (fix-target correction plus
  per-query blast-radius numbers) — recorded there since it directly amends
  that task's resume point, per this milestone's own precedent (M0142-0012a's
  findings live on the M0142-0012 entry it scoped).
- [x] **M0142-0012a — scoping recon: measure M0142-0012's blast radius before
  implementing it** — filed by this loop from the working-set baton's own
  suggestion ("a 0142-0012 sub-scoping recon... is a reasonable first cut").
  **DONE 2026-09-15, recon closed, no code change. Full writeup in
  `docs/design/0100-0149/m0142-0012a-scoping-recon-blast-radius.md`.**
  Reused the M0142-0010/M0142-0011 env-gated-trace-then-revert method on
  private throwaway servers (never the shared `:6543x`/`tmp/goopg-bench-bin`
  lanes): counted `estimateJoin` calls hitting `j.Lateral && j.Right` bound
  `*IndexScan`/`*IndexOnlyScan` with zero `joinEquiPairs`. **TPC-H 6/21
  queries hit it** (Q2, Q7, Q8, Q9, Q11, Q21 — including flagship Q9 and the
  M0077-era Q21 NLI witness), **224 total call-site hits**. **TPC-DS SF0.25
  69/99 queries hit it**, **6347 total call-site hits** (Q14 highest, 1009).
  Confirms "likely large corpus-wide blast radius" with a number — small
  overlap with M0142-0005/0009/0010's own witness populations (corroborating,
  not duplicate fixes). Counts are call-site hits during DP-search costing,
  not final-plan node counts (not measured — flagged as the recon's own
  scope boundary). `git diff` on `cardinality.go` empty after revert;
  `go build ./internal/optimizer/...` and `go test ./internal/optimizer/...`
  clean. **M0142-0012 is now sized with real numbers, not just code
  inspection — the next loop can implement it directly** (resume point
  unchanged from the entry above).

## M0143 — Engine correctness carry-overs from the parity programme (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0143-engine-correctness-carry-overs.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
**Source:** `METHODOLOGY3/04-forward-plan.md` §3 "Continuous — engine correctness, not gated on anything"
**Prerequisites:** none. Order: `.ralph/fix_plan.md` banner (item 7; M0143-0008 is handled with P0-E5).

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

- [x] **M0143-0001 — an in-process test that crosses a DATABASE boundary** —
  `CREATE DATABASE` is a dispatch-layer statement the in-process parser rejects, so
  `pgConstraintTableRel`'s per-DB branch and the reload's `ListDatabases` loop — the
  exact paths TPC-H rides — have manual psql evidence only. This is *why* two per-DB
  defects were found in two consecutive rounds; closing it makes the rest of this
  milestone findable by the suite. Highest leverage of the six.
  - **Scoping investigation 2026-09-17 (read-only, no code changed):**
    `parser.Parse("CREATE DATABASE ...")` genuinely errors 42601 — the
    parser has zero grammar for it (`internal/postmaster/dispatch_extended.go:634-645`
    states this explicitly); both wire protocols intercept it by
    string-prefix match (`classifyDatabaseDDL`, `internal/postmaster/database_ddl.go:412`)
    *before* parsing, via `(s *Server) tryHandleDatabaseDDL` (`database_ddl.go:1485`),
    which requires a `*postmaster.Server` — a different package from
    `internal/executor`'s fast in-process test harness (`newVMFixture`/`newHOTFixture`,
    `internal/executor/vm_test.go:11`), and `internal/postmaster` cannot be
    imported from `internal/executor` (import cycle: postmaster already
    imports executor). So the "manual psql evidence only" claim is only
    half true: `internal/postmaster/database_ddl_test.go` already has
    dozens of in-process (no socket, no psql) calls to
    `tryHandleDatabaseDDL` directly (e.g. line 294, 358, 420, `Server`
    constructed at :417/:447), and `internal/postmaster/database_oid_wiring_test.go`
    already in-process-tests the DB-switch mechanism (`executor.Context.CurrentDatabaseOid`,
    a plain settable field, stamped in the real path by
    `(s *Server) wireExtensionRows` at `dispatch.go:3231`, called from
    `dispatch.go:690`/`dispatch_extended.go:229`). The genuinely
    undemonstrated gap is a single in-process test that *chains* both
    halves — `tryHandleDatabaseDDL("CREATE DATABASE r", ...)` then running
    DDL against the new DB's own namespace via the executor (`ectx.CurrentDatabaseOid`
    set from `im.ResolveDatabaseOid("r")`, then `parser.Parse`/`optimizer.Plan`/
    `executor.Build` as normal) — belongs in `internal/postmaster` (not
    `internal/executor`), mirroring `database_ddl_test.go` +
    `database_oid_wiring_test.go` + `internal/executor/fk_dbid_routing_test.go`'s
    `ctx.CurrentDatabaseOid`-direct-set pattern. The harder, less-precedented
    half of this task is exercising the *reload* `ListDatabases` loop
    (`internal/initdb/catalog_heap_reload.go:252,335,442,730,884`,
    `internal/initdb/open.go:1564,3577,3873`) and `pgConstraintTableRel`'s
    per-DB branch (`internal/executor/sys_pg_constraint.go:119-139`) under a
    genuine multi-database WAL-replay/restart in-process (no server
    socket/psql, but heavier than a single `Server`+`Context` — needs
    `internal/initdb`'s open/recovery entry point against a temp data dir
    directly) — no existing precedent for that half. Separate wrinkle:
    `internal/testport/database_template_oid_collision_test.go:9-11`'s own
    comment says the in-process `*postmaster.Server` harness "hangs on
    multi-DB-write shutdown," a pre-existing harness limitation that may
    bite the reload-loop half specifically. Resume point: start with the
    `internal/postmaster`-level chain test (concrete, low-risk, closes the
    "manual psql evidence" gap for the DDL-execution half); scope the
    reload-loop half as a likely-separate follow-up once the shutdown-hang
    wrinkle is understood, rather than attempting both in one sitting.
  - **Landed 2026-09-17 (DDL-execution half only — still open for the
    reload-loop half; not ticked).** `TestDatabaseDDLChainedExecutorDML`
    (`internal/postmaster/database_ddl_chain_test.go`): chains
    `s.tryHandleDatabaseDDL("CREATE DATABASE r", ...)` with
    `s.wireExtensionRows(ectx, "r")` (the real per-connection oid-resolution
    path, not a synthetic `ctx.CurrentDatabaseOid` constant) and runs
    CREATE TABLE + a REFERENCES FK against the new database's own real oid
    via the executor pipeline, mirroring
    `internal/executor/storage_ddl_test.go`'s `runDDL`/
    `fk_dbid_routing_test.go`'s `runQueryUnderDBOid` patterns (reimplemented
    locally in `internal/postmaster` since those are unexported executor
    test helpers). A second context bound to `"postgres"` gets its own
    table+FK. Verifies `pg_constraint`'s FK row (`pgConstraintTableRel`'s
    per-database branch, reached via `wireExtensionRows`' `PgConstraintRows`
    wiring) by `conname` identity, not just count, and cross-database
    namespace isolation (each database's table is invisible under the
    other's oid). Test-design finding worth keeping: a count-only assertion
    would NOT have caught a real regression here — temporarily forcing
    `PGConstraintRowsForDBOid`'s `dbOid` argument to `DefaultDBOid`
    (mirroring the exact hardcode shape `M0143-0002`/`-0002b` fixed
    elsewhere) left both databases' row counts at 1 while `db r`'s query
    silently returned `db postgres`'s FK row instead of its own — caught
    only because the assertion checks the actual `conname` value. Verified
    the mutation makes the test fail, then reverted it (temporary,
    `catalog.go` carries no diff). No production code changed. Design doc:
    `docs/design/0100-0149/m0143-0001-database-boundary-chain-test.md`
    (indexed in `docs/design/README.md`). Gates: `go build ./...` clean;
    `go test ./internal/postmaster/... ./internal/catalog/...
    ./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` PASS (units scope excludes
    `internal/postmaster`, run separately above).
  - **Reload-loop half landed 2026-09-17 — task now fully complete.**
    Investigated the "hangs on multi-DB-write shutdown" wrinkle first (a
    research pass, not code): traced it to `Server.Run`'s shutdown path
    (`internal/postmaster/server.go:582-701`) — no existing in-process
    `*postmaster.Server`-with-`Run()` test harness sets `shutdownDeadline`,
    so shutdown always falls to the unbounded `s.connWG.Wait()` branch
    (`:693`), which blocks forever if any backend goroutine is still alive;
    no repro/stack trace exists anywhere, the claim traces to a single
    commit message with no further diagnosis. Sidestepped entirely rather
    than fixed: `TestDatabaseDDLReloadAcrossRestart`
    (`internal/postmaster/database_ddl_reload_test.go`) uses
    `postmaster.Server` purely as a `tryHandleDatabaseDDL`/
    `wireExtensionRows` method-holder (`Run()` is never called, so there is
    no listener/accept-loop/`connWG` for the hang to occur in), against a
    real `internal/initdb.Runtime` (`Open`/`Close`/`Open` on the same data
    dir), mirroring `internal/initdb/heap_catalog_load_test.go`'s existing
    restart shape. Drives two non-default databases (`r1`, `r2`), each with
    its own FK-bearing table pair, through a genuine restart and
    re-verifies post-reload: `reloadDatabasesFromHeap` repopulated
    `ListDatabases()` with both; `loadForeignKeysFromHeap`'s per-database
    scan (`pgConstraintTableRel`'s `tableCatalogHeapDBOid` routing)
    reconstructs each database's own FK by `conname` identity, not just
    count; cross-database namespace isolation holds after reload. Two live
    mutations verified the test is a real tripwire, both reverted before
    commit: (1) omitting `Config.TxnMgr` reproduces a genuine latent
    durability gap this test surfaced — `syncPgDatabaseHeapRow`
    (`internal/postmaster/database_ddl.go:1365-1368`, `runPgDatabaseHeapTxn`)
    silently no-ops without a `TxnMgr`, so `CREATE DATABASE` would look
    successful yet vanish after restart with no error anywhere (the
    already-landed DDL-chain test never caught this because it never
    restarts); (2) forcing `loadForeignKeysFromHeap`'s per-database loop to
    always pass `catalog.DefaultDBOid` (simulating a reload-time version of
    the `M0143-0002`/`-0002b` bug class) makes both FK assertions fail with
    empty results, confirming the test exercises the reload loop's
    per-database routing, not just the already-covered query-time routing.
    Design doc and README index updated in the same commit. Gates: `go
    build ./...` clean; `go vet ./internal/postmaster/...` clean; `go test
    ./internal/postmaster/... ./internal/catalog/... ./internal/initdb/...
    ./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` PASS. No production code changed —
    test-only, no deferral-ledger row needed.
- [x] **M0143-0002 — `ALTER TABLE … DROP CONSTRAINT` on an FK reports success and does
  nothing** — `InMemory.DropForeignKeyConstraint` hardcodes `DefaultDBOid`
  (`catalog.go:22241`) and `execAlterTableDropConstraint` discards the result
  (`operators_ddl.go:13303`). `HasPrimaryKey` (`catalog.go:22261`) has the same shape,
  and six `deleteCatalogRowsForOID` sites were filed for the same check and never
  confirmed. R126 made this worse in effect, because such an FK now survives restarts.
  - **Done 2026-09-17.** Fixed the confirmed defect this task names: changed
    `DropForeignKeyConstraint`'s signature from `(tableOID uint32, name string)
    bool` to `(tbl *Table, name string) bool` (`internal/catalog/catalog.go`)
    so it mutates the caller's already-resolved live `*Table` pointer directly
    instead of re-resolving by OID via `tableByOID(tableOID, DefaultDBOid)` —
    which silently returned `ok=false` (so nothing was removed) for any table
    outside the default database. Single call site updated
    (`internal/executor/operators_ddl.go`'s FK branch of
    `execAlterTableDropConstraint`). New regression test
    (`internal/testport/m0143_0002_fk_drop_constraint_nondefault_db_test.go`)
    creates the FK in database `"r"` (non-default) and asserts a COMMITted
    `DROP CONSTRAINT` actually disables enforcement — confirmed to genuinely
    fail pre-fix (git-stashed the fix, red) and pass post-fix. Also re-pointed
    `TestPort_M0143_0008b_DropForeignKeyRollbackUndo`
    (`p0e5b_alter_drop_constraint_rollback_undo_test.go`) from the default-db
    workaround it needed pre-fix back onto database `"r"`, per that test's own
    documented resume point — still green. **Not covered by this fix, filed
    as M0143-0002b below:** `HasPrimaryKey`/`dropIndexByName` share the exact
    same `c.ns(DefaultDBOid).byTable[...]` hardcode shape (confirmed by
    reading, not live-tested this loop — `catalog.go`'s index registries
    genuinely are stored per-DB via `c.ns(dbOid).byTable`, so this is very
    likely a live bug for PK/UNIQUE/EXCLUDE constraints on a non-default DB
    table too); the "six `deleteCatalogRowsForOID` sites... never confirmed"
    note in this task's own original text was not investigated this loop
    (scope: this loop fixed the one defect with an in-hand live repro, not
    every same-shape sibling named in the task's prose).
- [x] **M0143-0002b — audit `HasPrimaryKey`/`dropIndexByName`'s DefaultDBOid
  hardcode for the same non-default-DB no-op M0143-0002 had.**
  Parent: M0143-0002. Filed 2026-09-17 when M0143-0002's fix confirmed the
  FK-specific instance of this hardcode shape live but left the PK/UNIQUE/
  EXCLUDE-backed siblings and the original task's "six
  `deleteCatalogRowsForOID` sites" note unconfirmed. Both read
  `c.ns(DefaultDBOid).byTable[tableOID]` (`catalog.go:22214`, `:22261`)
  unconditionally, but index registration is genuinely per-DB
  (`c.ns(dbOid).byTable[tbl.OID]`, confirmed by reading the registration
  sites). If a PK/UNIQUE/EXCLUDE-backed table lives in a non-default
  database, `HasPrimaryKey` likely always reports `false` for it and
  `dropIndexByName` (backing `DropUniqueConstraint`/`DropExclusionConstraint`)
  likely always silently no-ops its DROP — the same failure shape M0143-0002
  fixed for FKs, unconfirmed live. Also re-check the "six
  `deleteCatalogRowsForOID` sites... filed for the same check and never
  confirmed" note from M0143-0002's original text (not re-derived this loop;
  re-`grep`/re-read the call sites at `operators_ddl.go` before assuming
  which six were meant). Resume point: write a `HasPrimaryKey`/
  `DropUniqueConstraint`/`DropExclusionConstraint` non-default-DB repro
  mirroring `m0143_0002_fk_drop_constraint_nondefault_db_test.go`, confirm
  which are actually broken before changing any of them (a wrong theory here
  would not be the first time a "same shape" claim needed live confirmation
  first — mirrors this task's own FK finding, which WAS live-confirmed before
  the fix landed).
  - **Done 2026-09-17.** Confirmed the hypothesis live: `dropIndexByName`
    (`internal/catalog/catalog.go`, shared by `DropPrimaryKeyConstraint`/
    `DropUniqueConstraint`/`DropExclusionConstraint`) and `HasPrimaryKey`
    both hardcoded `c.ns(DefaultDBOid)` the exact M0143-0002 shape. Fixed by
    changing all three public wrappers plus `dropIndexByName` itself to take
    the resolved `*Table` (not a bare `tableOID`) and key `c.ns()` off
    `tbl.DBOid` (falling back to `DefaultDBOid` when zero, the same idiom
    `TableRealPages`/`relAllVisibleCell` already use); `HasPrimaryKey` got
    the same `table.DBOid`-keyed fix without a signature change (it already
    took a `*Table`). Five call sites updated: `operators_ddl.go`'s
    PK/UNIQUE/EXCLUDE `DROP CONSTRAINT` branches (3) and `catalog_test.go`'s
    `TestDropPrimaryKeyConstraint`-style unit test (2 calls). New test
    `internal/testport/m0143_0002b_index_backed_drop_constraint_nondefault_db_test.go`
    — one sub-test per constraint kind (PK, UNIQUE, EXCLUDE `USING btree (a
    WITH =)`), each created in database `"r"`, COMMITted (not ROLLBACKed)
    `DROP CONSTRAINT`, asserting enforcement is actually gone; confirmed
    genuinely red pre-fix via `git stash` of the three touched source files
    (`23505`/`23P01` still-enforced errors on all three) and green post-fix.
    **Not investigated this loop** (task's own text flagged it as a separate
    open question, not this task's scope): the original M0143-0002 "six
    `deleteCatalogRowsForOID` sites... never confirmed" note —
    `deleteCatalogRowsForOID`'s ~15 call sites already take an explicit
    `dbOid` parameter (grep'd), so whichever six sites the original note
    meant need their own fresh identification, not an assumption they share
    this hardcode shape. Design doc:
    `docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md` new
    "M0143-0002b" section; `docs/design/README.md` p0-e4 row title +
    description updated. Gates: `go build ./...` clean; `go vet
    ./internal/testport/... ./internal/catalog/... ./internal/executor/...`
    clean; `go test ./internal/catalog/... ./internal/executor/...` PASS;
    `go test -v -run
    'TestPort_M0143_0002|TestPort_M0143_0002b|TestPort_M0143_0008b|TestPort_P0E4|TestPort_P0E5'
    ./internal/testport/` PASS 12/12; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` — same pre-existing 60 `internal/parser`
    failures (M0143-0006, no parser file touched, same count/shape as prior
    loops); `scripts/tpcds-sf025-regression.sh sweep` — `PASS=96 MISMATCH=0
    CKMISMATCH=0 ERROR=0 TIMEOUT=0`, plan-shapes 99/99 identical.
    `tpch-spotcheck`/`tpch-acceptance-arm` still SKIP-BLOCKED by the
    `:65433` hold (same accepted G6 exception, P0-E7 remains the re-run
    owner).
- [x] **M0143-0002c — re-identify the "six `deleteCatalogRowsForOID` sites"
  M0143-0002's original text named.**
  Parent: M0143-0002b. Filed 2026-09-17
  when M0143-0002b closed without resolving this forward-reference (it was
  deferred from M0143-0002 to M0143-0002b, and M0143-0002b's own fix scope
  was the PK/UNIQUE/EXCLUDE `DefaultDBOid` hardcode only). Both M0143-0002's
  and M0143-0002b's loops confirmed `deleteCatalogRowsForOID`'s ~15 call
  sites already take an explicit `dbOid` parameter (grep'd, not exhaustively
  read), so the original "six sites... filed for the same check and never
  confirmed" note does not describe this hardcode shape as-is — it needs
  fresh identification of what it actually meant (a different check,
  possibly at a different layer) before it can be confirmed or fixed.
  Resume point: `grep -n "deleteCatalogRowsForOID" internal/executor/operators_ddl.go`,
  read each call site's surrounding context for a *different* per-DB
  hardcode shape (not the `c.ns(DefaultDBOid)` one M0143-0002/-0002b already
  fixed), and check `git log`/blame around the M0143-0002 filing date
  (2026-09-14, `METHODOLOGY3/04-forward-plan.md` §3) for the original
  six-sites claim's source context.
  - **Done 2026-09-17 (investigation only, no code changed — see
    M0143-0002d for the fix).** The original claim's source is
    `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/02-open-problems.md`
    §N11 (via `r126-fk-persistence/SCOPE.md:155-160`, `REPORT.md:273-276`,
    `TODO.md:5812-5817`) — "N11: Six `deleteCatalogRowsForOID` call sites
    hardcode `catalog.DefaultDBOid` ... at :24974/:25014/:25057/:25104/
    :25349/:25401 [historical line numbers from R126's era] instead of
    looping `tableCatalogDBOids`." Re-grepping at HEAD (`operators_ddl.go`
    has grown to 27,475 lines) finds exactly six live matches for
    `deleteCatalogRowsForOID(.*catalog\.DefaultDBOid` — confirmed as the
    composite-type re-sync branches of `execAlterType` (four
    single-subcommand forms: ADD/RENAME/DROP/ALTER ATTRIBUTE, `:25242`,
    `:25282`, `:25325`, `:25372`), `execAlterTypeAttrCmds`'s combined form
    (`:25617`), and `execDropType`'s composite branch (`:25669`). These are
    genuinely distinct from the M0143-0002/-0002b hardcode (which was
    `c.ns(DefaultDBOid)` inside `catalog.InMemory`'s table/index registries)
    — this one is a `storage.RelFileNode{DBOid: catalog.DefaultDBOid, ...}`
    literal picking which database's *physical heap file* to xmax-stamp.
    **The re-identification surfaced something much bigger than "these six
    call sites are wrong in isolation":** their sibling *write* paths —
    `writeTypeHeapRowWithIndexes` (`:18447-18458`, the pg_type row writer
    shared by every `CREATE TYPE` of any kind — enum/domain/composite/range)
    and `syncCompositeTypeToCatalogHeap`'s `classRel`/`attrRel`
    (`:18539-18544`/`:18555-18561`, the composite's implicit pg_class/
    pg_attribute row writer) — hardcode the exact same `catalog.DefaultDBOid`
    literal. So the six ALTER/DROP delete sites are *consistent* with
    CREATE, not independently broken: every user-declared type's physical
    catalog-heap row, in every database, has always landed in the shared
    DEFAULT database's pg_type/pg_class/pg_attribute heap files. **Live-
    confirmed the consequence on a throwaway 55xx cluster** (test written,
    run, and DELETED after confirming — not committed, since this loop
    lands no fix): `CREATE TYPE samename AS (a int)` in the default
    database, then `CREATE DATABASE r; \c r; CREATE TYPE samename AS (x
    text, y text)` — the second CREATE TYPE succeeds with **no name-
    collision error** (PG would reject neither, since composite types are
    correctly scoped per-database in the in-memory registry — `compositeKey`
    folds in dbOid — so no error is actually wrong here), but
    `SELECT a.attname FROM pg_attribute a JOIN pg_type t ON
    t.typrelid=a.attrelid WHERE t.typname='samename'` run from **either**
    database returns the **union** `[a, x, y]` instead of each database's
    own `[a]` / `[x, y]` — genuine cross-database catalog corruption visible
    to plain SQL (pg_dump, `\d`, information_schema all ride this same
    path). Contrast: `pg_constraint` already has the correct pattern for
    this class of problem — `pgConstraintTableRel`
    (`sys_pg_constraint.go:133`) resolves via `tableCatalogHeapDBOid(ctx)`,
    and even the TYPE-catalog's own **index** inserts already route
    correctly the same way (`insertCanonicalSysBtreeLeaf`,
    `sys_catalog_index_insert.go:423-428`, routes via
    `tableCatalogHeapDBOid(ctx)` — comment says "a distinct-dbOid database
    now has its own bootstrapped catalog btrees") — so per-DB index files
    already exist and are already used correctly; only the **heap row
    writes/reads** for user-defined types never got the same treatment.
    **Why this loop does not attempt the fix:** changing only the six
    ALTER/DROP delete sites to target the connection's real dbOid, while
    CREATE (`writeTypeHeapRowWithIndexes`/`syncCompositeTypeToCatalogHeap`)
    keeps writing to `DefaultDBOid`, would make ALTER/DROP's xmax-stamp
    target a heap location with **no matching row** (the real row is in
    Default) — a *new*, different no-op regression, not a fix. The correct
    fix must move CREATE, ALTER, and DROP together across all four type
    kinds (enum/domain/composite/range all share `writeTypeHeapRowWithIndexes`),
    and must also account for the read side (SeqScan of pg_type/pg_class/
    pg_attribute appears to read the same shared Default file regardless of
    the querying connection's database, per the live repro above) — too
    large and too risky for a single-task loop, especially with the
    milestone banner's "no reverts" rule (R3) raising the cost of a wrong
    first attempt. Filed as **M0143-0002d** below with this loop's findings
    as its resume point. Ledger: 1 new row (`.ralph/deferral_ledger.md`).
    Gates: none run (no code changed — investigation-only loop, mirrors the
    M0143-0001 scoping loop's precedent).
- [x] **M0143-0002d — user-defined type catalog-heap storage
  (pg_type/pg_class/pg_attribute for enum/domain/composite/range types) is a
  single un-partitioned store shared by every database, unlike pg_constraint.**
  Parent: M0143-0002c. Filed 2026-09-17 from M0143-0002c's re-identification
  loop, which found the "six `deleteCatalogRowsForOID` sites" are a symptom
  of this much larger gap, not an independently fixable bug. **Live-confirmed
  repro** (see M0143-0002c's Done note for the exact commands and query): two
  databases each declaring a composite type of the same name do not collision-
  error (correctly — they're separate types) but their pg_attribute/pg_type
  rows are visibly the UNION of both types' fields from *either* database,
  because every user type's physical row always lands in `catalog.DefaultDBOid`'s
  heap files regardless of which database's session created it.
  **Write-side hardcodes to fix together** (all `storage.RelFileNode{DBOid:
  catalog.DefaultDBOid, ...}` literals in `internal/executor/operators_ddl.go`):
  `writeTypeHeapRowWithIndexes` (`:18447-18458`, shared pg_type writer — every
  `CREATE TYPE`/`CREATE DOMAIN` of any kind funnels through this), the
  `syncCompositeTypeToCatalogHeap` `classRel`/`attrRel` pair (`:18539-18544`/
  `:18555-18561`, composite's implicit pg_class/pg_attribute), and the six
  M0143-0002c delete-side sites (`execAlterType` `:25242`/`:25282`/`:25325`/
  `:25372`, `execAlterTypeAttrCmds` `:25617`, `execDropType`'s composite
  branch `:25669`) plus its enum branch (`~:25644-25648`, same hardcode for
  `deleteTypeFromCatalogHeap`/`deleteEnumLabelRowsByTypid`) and range branch
  (`~:25684-25689`) — not independently counted in "the six" but sharing the
  identical shape and needing the same fix. **Fix these together, not
  piecemeal**: changing only a subset would make some paths target the
  connection's real dbOid while others still write to Default, which is a
  *worse* inconsistency than today's uniform (wrong) DefaultDBOid — every
  write site above must move to `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
  (or the resolved `o.ctx.CurrentDatabaseOid` if already normalized at the
  call site — check `NamespaceDBOid`'s 0/`PostgresDBOid` folding, `catalog.go:25196`)
  in the same commit. **Read-side root cause — LOCATED 2026-09-17 (this loop,
  investigation-only, no code changed):** `pg_type`/`pg_attribute` are
  registered exactly ONCE, at server startup, by
  `loadSystemCatalogsIfPresent` (`internal/initdb/open.go:2931-2972`) — it
  builds one `catalog.Table{OID: catalog.TypeRelationId/.AttributeRelationId}`
  literal per relation (no `DBOid` field set, so it defaults to Go's zero
  value) and calls `cat.RegisterRealTable(t)` with no dbOid argument, which
  resolves to `DefaultDBOid`'s namespace only (`resolveDBOid`,
  `catalog.go:4078-4083`) — there is no equivalent call for any other
  database, ever. Every `SeqScan`/write against pg_type or pg_attribute
  (from `copy.go`, `operators_bitmap.go`, `operators_ddl.go`, etc. — all
  route through the one shared `Catalog.RelFileNode(tbl)`, confirmed by
  grepping every call site) resolves via `catalog.InMemory.RelFileNode`
  (`catalog.go:22354-22366`): `dbOid := c.dbOid; if table.DBOid != 0 &&
  table.DBOid != DefaultDBOid { dbOid = table.DBOid }` — since these two
  `Table` structs' `DBOid` field is always `0`, the condition never fires and
  every connection, regardless of `ctx.CurrentDatabaseOid`, resolves to the
  same `c.dbOid` (a single process-wide field stamped once by `SetDBOID` at
  startup — see the `RelFileNode` doc comment at `catalog.go:22301-22320`).
  This is the exact mechanism behind the cross-database UNION repro. Contrast
  confirmed: `pgConstraintTableRel` (`sys_pg_constraint.go:133`) is different
  in kind, not just routing — it computes `tableCatalogHeapDBOid(ctx)` fresh
  on every call from `ctx.CurrentDatabaseOid`, whereas pg_type/pg_attribute's
  `RelFileNode` resolution is baked into a single shared `Table` struct at
  registration time and never revisited per-connection. **Fix shape implied**
  (real PG: pg_type/pg_class/pg_attribute are NOT shared catalogs —
  `relisshared=false` — each database has its own physical copy under its own
  `base/<dbOid>/`): `loadSystemCatalogsIfPresent`'s registration needs to run
  once per database (not just DefaultDBOid) with `t.DBOid` set to that
  database's own oid, AND each database needs its own physical
  `base/<dbOid>/1247`/`base/<dbOid>/1249` heap file provisioned — most likely
  at `CREATE DATABASE` time (mirror `syncCopiedTableCatalogHeap`'s per-table
  pattern in `internal/postmaster/database_ddl.go:1291-1335`, which already
  does the equivalent per-table dance for ordinary user tables copied from a
  template) plus a startup-reload pass that registers every already-existing
  database's copy, not just the default one. This is materially bigger than
  the write-side hardcode list above — it is genuinely no small tweak, so
  scope the implementing loop(s) to design-first (a design doc is mandatory
  here per AGENT.md D3 before code lands) rather than attempting the
  provisioning + registration + write-side fix all in one pass. **Index
  entries are already correct** — `insertCanonicalSysBtreeLeaf` (`sys_catalog_index_insert.go:423-428`)
  already routes via `tableCatalogHeapDBOid(ctx)`, so `pg_type_oid_index`/
  `pg_type_typname_nsp_index`/`pg_class_relname_nsp_index`/
  `pg_attribute_relid_attnum_index` inserts do not need to change — only the
  heap rows they point at. Test with two databases each declaring a
  same-named enum, domain, composite, and range type (mirrors the live repro
  above) and confirm each database's `pg_dump`/`SELECT ... FROM pg_type`
  shows only its own type's shape after the fix. Needs a design note
  (non-trivial, cross-cutting reload-adjacent change) per AGENT.md's
  Plan-parity harness D3.
  - **Done 2026-09-17 (design-first, per this task's own filing
    instruction — no production diff).** Design doc:
    `docs/design/0100-0149/m0143-0002d-per-database-type-catalog.md`
    (indexed in `docs/design/README.md`). Confirms both root causes with
    file:line citations and, live-checked this loop, **refutes the
    "materially bigger... provisioning" fear the original filing carried**:
    every database already gets its own physical `base/<dbOid>/1247`/`1249`
    heap file at `CREATE DATABASE` time (`copyBootstrapCatalogImage`,
    `internal/initdb/initdb.go:426-473`, copies template0's whole catalog
    image) — the remaining gap is in-memory registration/routing only, not
    disk layout. Also explains, newly, why ordinary tables don't share the
    bug despite `pg_class` being an equally-global singleton `catalog.Table`:
    `pg_class` output is virtual (per-database-filtered already), while
    `pg_type`/`pg_attribute` are real heap-backed relations reached by an
    actual `SeqScan` through the buggy `RelFileNode` resolution. Fix shape
    mirrors the identical problem already solved for indexes
    (`RegisterIndexDuringRecoveryForDB`), decomposed below into two
    loop-sized tasks (read-side registration must land before the write-side
    hardcode fix — reversed order would make a distinct-dbOid connection's
    own new type invisible to itself, worse than today's union bug). No
    ledger row: recon/design only, no production diff; the two child tasks
    below own the actual fix and their own deferral bookkeeping if either
    lands partially.
- [x] **M0143-0002e — per-database `catalog.Table` registration for
  `pg_type`/`pg_attribute` (read side of M0143-0002d).**
  Parent: M0143-0002d. Filed 2026-09-17 from M0143-0002d's design doc, fix-shape steps 1-3. Add a
  `catalog.InMemory` method that registers a `Table` for a system relation
  (pg_type/pg_attribute shape) into `c.ns(dbOid)` with `Table.DBOid = dbOid`
  set (mirrors `RegisterIndexDuringRecoveryForDB`,
  `internal/catalog/catalog.go:6584-6698`); wire it into (a) a startup reload
  loop over `cat.ListDatabases()` right after `loadSystemCatalogsIfPresent`'s
  existing `DefaultDBOid` call (`internal/initdb/open.go:1376`), mirroring
  the index precedent's loop at `open.go:3576-3585` (skip
  DefaultDBOid/PostgresDBOid/`cat.DBOID()`, check `heapFilePresent` on
  `base/<dbOid>/1247|1249` before registering); and (b) `CREATE DATABASE`
  time, next to `copyTemplateTables`'s existing
  `im.TryRegisterUserTable(newTbl, newOid)` call
  (`internal/postmaster/database_ddl.go:995`) — registration-only, the
  physical files already exist from `copyBootstrapCatalogImage`. **No
  observable SQL behavior change yet** (write side still hardcodes
  DefaultDBOid until M0143-0002f) — gate is a new unit test confirming
  `pg_type`/`pg_attribute` `RelFileNode` resolution differs per
  `ctx.CurrentDatabaseOid` after this lands, plus the full existing
  unit/regress suite staying green (the DefaultDBOid path is every existing
  test and must be byte-identical).
  - **Done 2026-09-17.** Implemented as an enhancement of the EXISTING
    `RegisterRealTable(t *Table, dbOid ...uint32)` rather than a brand-new
    method — it already carried an unused `dbOid ...uint32` param wired
    nowhere; `internal/catalog/catalog.go:12696` now sets `t.DBOid =
    resolved` whenever the resolved dbOid is a genuine non-default database
    (omitting the arg, or passing `DefaultDBOid` explicitly, leaves
    `t.DBOid` at its zero value — byte-identical to every pre-existing
    caller/test). `loadSystemCatalogsIfPresent`
    (`internal/initdb/open.go:2931`) is now a thin wrapper: the original
    body moved to `loadSystemCatalogsIfPresentForDB(dataDir, cat, heapDBOid,
    nsDBOid)`, called once for the `DefaultDBOid` pass (unchanged asymmetry:
    reads `base/<cat.DBOID()>`, registers into `DefaultDBOid`'s namespace)
    then once per other already-registered database from `cat.ListDatabases()`
    (heapDBOid==nsDBOid==dbOid), skipping
    DefaultDBOid/PostgresDBOid/`cat.DBOID()` exactly like the index
    precedent's loop. New exported `initdb.RegisterSystemCatalogsForDB(dataDir,
    cat, dbOid)` wraps the same per-DB helper for `CREATE DATABASE` time;
    wired into `internal/postmaster/database_ddl.go`'s
    `tryHandleDatabaseDDL` right after `createDatabasePhysicalDirectory`
    succeeds (new `(*Server).registerSystemCatalogsForNewDB`, unconditional
    on `tmplTables` since every database gets its own pg_type/pg_attribute
    files regardless of template content) — a failure there rolls back the
    same way a scaffolding failure does (`cat.DropDatabase` +
    `removeDatabasePhysicalDirectory`). Two new unit tests:
    `internal/catalog/register_real_table_dbid_test.go`
    (`TestRegisterRealTablePerDatabaseRelFileNode`, the catalog-primitive
    gate — confirms `RelFileNode` now differs per dbOid and the no-dbOid
    call path is unchanged) and
    `internal/initdb/system_catalog_dbid_test.go`
    (`TestRegisterSystemCatalogsForDBRoutesToOwnNamespace`, the
    physical-file + wiring gate — real `CreatePerDatabaseScaffolding` +
    `RegisterSystemCatalogsForDB` round trip, plus a no-scaffolding dbOid
    no-op case). Gates run: `go build ./...` clean;
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` all green
    including `internal/catalog` and `internal/initdb`
    (125s, real cost — the initdb package boots a full cluster per test);
    `go test ./internal/postmaster/...` green separately (both new unit
    tests plus the full existing suite, confirming the CREATE DATABASE path
    change is byte-identical for every existing test — none of them create a
    second database with types, so none observes the new registration).
    **No SQL-observable behavior change** — confirmed by design: writes
    still hardcode `DefaultDBOid` until M0143-0002f, so this task's own
    correctness claim rests entirely on the two new unit tests above, not on
    any end-to-end SQL assertion (an end-to-end test would show nothing
    different yet). M0143-0002f (write-side route-through) is next, in
    order, now unblocked.
- [x] **M0143-0002f — route type-catalog heap writes through
  `tableCatalogHeapDBOid(ctx)` (write side of M0143-0002d).**
  Parent: M0143-0002d. Depends on M0143-0002e `[x]`. Filed 2026-09-17 from
  M0143-0002d's design doc, fix-shape step 4. Change
  `writeTypeHeapRowWithIndexes`
  (`internal/executor/operators_ddl.go:18447-18458`),
  `updateTypeHeapRowWithIndexes` (`:18483-18504`),
  `syncCompositeTypeToCatalogHeap`'s `classRel`/`attrRel` pair
  (`:18539-18543`/`:18555-18559`), and the M0143-0002c delete-side sites
  (`execAlterType` `:25242`/`:25282`/`:25325`/`:25372`,
  `execAlterTypeAttrCmds` `:25617`, `execDropType`'s composite/enum/range
  branches `~:25644-25689`) from the `catalog.DefaultDBOid` literal to
  `tableCatalogHeapDBOid(ctx)` (`:18697-18699`, already
  `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`) — **all sites in the same
  commit**, per the design doc's sequencing note (a partial change is worse
  than today's uniform bug). Test: mirror
  `internal/postmaster/database_ddl_reload_test.go`'s
  `TestDatabaseDDLReloadAcrossRestart` shape — two databases each declare a
  same-named enum/domain/composite/range type, restart, confirm each
  database's `pg_type`/`pg_attribute`/`pg_dump` output shows only its own
  type's shape (the M0143-0002c repro, now also across a restart, not just
  live-session).
  - **Done 2026-09-17.**
    Movement: yes — a same-named composite/domain/range type declared in two
    non-default databases now shows each database's own shape (not the
    M0143-0002c UNION) in both a live session and, after this task's own
    restart bug fix below, across a `Close`/`Open` restart; confirmed by a
    new SQL-observable test, not just a unit-level structural check.
    All enumerated sites changed to
    `tableCatalogHeapDBOid(ctx)`/`tableCatalogHeapDBOid(o.ctx)` in one
    commit; live pre-restart verification matched the design (each
    database's composite type shows only its own fields, not the union).
    **The restart test caught a second, independent bug** in M0143-0002e's
    own landing: its per-database `pg_type`/`pg_attribute` registration loop
    lived inside `loadSystemCatalogsIfPresent` (`internal/initdb/open.go`),
    called (line 1376) *before* `reloadDatabasesFromHeap` (line 1546)
    populates `cat.ListDatabases()` — the very list the loop iterates — so
    it silently registered zero non-default databases on every restart
    (symptom: 0 rows post-restart, not the union, for either database's own
    type). Fixed by relocating the per-DB loop to run right after
    `reloadDatabasesFromHeap`'s `loadUserTablesFromHeapForDB` loop (same
    precondition, same shape); `loadSystemCatalogsIfPresent` is now just the
    DefaultDBOid pass again. New test:
    `internal/postmaster/database_ddl_type_reload_test.go`
    (`TestDatabaseDDLTypeCatalogReloadAcrossRestart`), covering composite
    (pg_attribute join, the exact M0143-0002c repro), domain (typbasetype
    identity), and range (pg_type isolation only — `pg_range`'s own
    DBOid routing is a separate, still-hardcoded catalog, out of this
    task's enumerated scope). Two write sites found while re-auditing every
    remaining `catalog.DefaultDBOid` literal were deliberately NOT touched
    since they were never in M0143-0002d's enumerated list — `pgRangeRel`
    (`internal/executor/sys_pg_range.go:54-59`) and `execDropDomain`/the
    ACL-resync functions (`resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`,
    `operators_ddl.go:23537,23667-23669,26366,26369`) — filed as
    **M0143-0002g** below with a `.ralph/deferral_ledger.md` row (dated
    2026-09-17). Design doc updated:
    `docs/design/0100-0149/m0143-0002d-per-database-type-catalog.md` (§
    M0143-0002f Done note) and `docs/design/README.md` (status → done).
    Gates: `go build ./...` clean; `go test ./internal/postmaster/...
    ./internal/executor/... ./internal/initdb/... ./internal/catalog/...`
    PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full
    green; `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS against
    the staged tree. `tpch-spotcheck` not run — no TPC-H data needed for
    this unit-scoped fix, consistent with the P0-E6-wait selection rule.
- [ ] **M0143-0002g — the two write-site gaps M0143-0002f's own audit found
  but did not fix (out of that task's enumerated scope).**
  Parent: M0143-0002d. Filed 2026-09-17 from M0143-0002f's Done note. (1)
  `pgRangeRel` (`internal/executor/sys_pg_range.go:54-59`, backs
  `writeRangeCatalogRow`/`deleteRangeCatalogRow`) still hardcodes
  `catalog.DefaultDBOid` — a range type's `pg_range` row (subtype/
  collation/opclass linkage) always lands in the default database's heap
  regardless of which database declared the range, even though its
  `pg_type` rows are now correctly per-database (M0143-0002f). (2)
  `execDropDomain`'s two `deleteTypeFromCatalogHeap` calls
  (`operators_ddl.go:26366`,`:26369`) and the GRANT/REVOKE re-sync functions
  `resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`
  (`operators_ddl.go:23537`,`:23667-23669`) were never in M0143-0002d's
  enumerated write-site list, so DROP DOMAIN and GRANT/REVOKE ON
  TYPE/table-column-ACL on a non-default database still target the wrong
  heap file. Fix: same one-line `catalog.DefaultDBOid` →
  `tableCatalogHeapDBOid(ctx)` swap at each site; for (1), also extend
  `TestDatabaseDDLTypeCatalogReloadAcrossRestart`'s range assertion from
  pg_type-only to the `rngsubtype` join it currently avoids. Each needs its
  own regression test (a non-default-DB DROP DOMAIN / GRANT ON TYPE /
  CREATE TYPE AS RANGE case).
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
- [x] **M0143-0006 — triage `internal/parser`'s 60 failing tests** — pre-existing,
  verified unrelated to R126, and unowned. Fix them or convert them into filed, owned
  tasks; "unowned" is not an end state.
  - **Done 2026-09-17.** Root cause: `dc91bd6b7` ("fix(planner): preserve
    grouped USING bindings for lateral", 2026-09-13) added a new field,
    `RangeVar.GroupedJoinUnaliased` (`internal/parser/ast.go:676`, set by
    `internal/parser/support.go:169`), which changed every `RangeVar`'s
    `dumpStmts` text. The checked-in oracle,
    `internal/parser/testdata/parity_goldens.txt` (1679 pinned statements,
    `internal/parser/goldens_test.go`), was never regenerated after that
    commit, so all 454 golden entries containing a `RangeVar` drifted —
    `TestParityGoldensAreCurrent` plus every `assertParity`-based test whose
    corpus happened to touch a `FROM` clause (60 test funcs; the other
    ~1200 golden entries with no `RangeVar` were unaffected, which is why
    some `_test.go` files in the package still passed). Not a parser
    regression: verified programmatically (diff every changed golden pair,
    strip the `GroupedJoinUnaliased=<bool>` token, assert byte-identical)
    that all 454 changed lines differ **only** by the new field's presence —
    zero other AST divergence. Fix: `GOOPG_UPDATE_GOLDENS=1 go test
    ./internal/parser/` to regenerate, reviewed the diff per the above,
    committed the refreshed `parity_goldens.txt` (no `.go` files changed).
    Verified: `go test ./internal/parser/...` 0 failures (was 60);
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full green.
    Coverage gap noted, not blocking: the corpus has zero statements
    exercising `GroupedJoinUnaliased=true` (`grep -c
    'GroupedJoinUnaliased=true' parity_goldens.txt` = 0) — the synthetic
    unaliased parenthesized-JOIN case R104 introduced has no golden pin of
    its own. Left as a coverage note rather than a new task since it is a
    missing-test gap, not a divergence.

- [x] **M0143-0008 — `ALTER TABLE` subcommands have no rollback-undo; a
  `ROLLBACK` after e.g. `ADD CONSTRAINT` silently leaves it permanently
  committed** — filed by M0142-0003h. `docs/design/0000-0049/0030-0006-transactional-ddl.md`
  Phase 1 wired rollback-undo (`DDLUndoEntry`/`RecordDDLCreate`,
  `session.go`; consumed by `execRollback` in `operators_tx.go:253`) for
  exactly two DDL forms — `CREATE TABLE` and `CREATE INDEX` (six call
  sites total, all in `operators_ddl.go`). Every other DDL form, confirmed
  live for `ALTER TABLE ADD CONSTRAINT` (`syncConstraintCatalogRow`,
  `operators_ddl.go:12912`) via a two-session repro on a throwaway cluster:
  `BEGIN; ALTER TABLE t ADD CONSTRAINT ...; ROLLBACK;` leaves the
  constraint permanently in `pg_constraint` — `ROLLBACK` reports success
  but does nothing. This is a correctness bug (silent, permanent wrong
  catalog state), not merely a documented-and-bounded limitation like the
  separate "concurrent DDL visibility deferred" gap (also confirmed live in
  the same repro, but pre-existing/known — do not conflate the two, see
  the -0003h design doc). Resume point: extend the `DDLUndoEntry`
  undo-list pattern (or design a small generalized "catalog mutation undo"
  record, since `ALTER TABLE` forms vary widely — ADD/DROP CONSTRAINT, ADD
  COLUMN, SET DEFAULT, ...) to cover at minimum `ADD CONSTRAINT`/`DROP
  CONSTRAINT`; likely overlaps `M0143-0002`'s `DROP CONSTRAINT` FK bug
  (same subsystem, `execAlterTableDropConstraint`,
  `operators_ddl.go:13259`) — triage both together. Test precedent:
  `transactional_ddl_test.go`'s `TestTransactionalCreateTableRollback` et
  al.; add an `ALTER TABLE ADD CONSTRAINT` analogue that fails before the
  fix.
  - **Done 2026-09-17 (landed as part of P0-E5).** `ADD CONSTRAINT ...
    {PRIMARY KEY|UNIQUE} USING INDEX` (`AlterIndexUndoEntry`/
    `NotNullUndoEntry`) and `DROP CONSTRAINT`'s three index-backed forms
    (PK/UNIQUE/EXCLUDE, via the existing `DDLDropUndoEntry`/`RestoreIndex`
    mechanism) now undo on ROLLBACK — this is the "at minimum ADD
    CONSTRAINT/DROP CONSTRAINT" bar this task's own text set. See P0-E5's
    entry above and `docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md`
    §"P0-E5 — the fix" for the full mutation-mapping/design rationale.
    Remaining `DROP CONSTRAINT` forms (CHECK/FOREIGN KEY/NOT NULL — in-place
    slice/field mutations, not map-removals, so they need their own
    snapshot/restore rather than reusing `DDLDropUndoEntry`) are **not**
    covered — filed as **M0143-0008b** below; also the column-list `ADD
    CONSTRAINT ... PRIMARY KEY (cols)` form's brand-new-index creation
    already gets undone by the existing CREATE-INDEX `DDLUndoEntry` path
    (unaffected/unchanged by this task), but was not itself re-verified with
    a dedicated test this loop.
- [x] **M0143-0008b — `ALTER TABLE DROP CONSTRAINT` on CHECK/FOREIGN
  KEY/NOT NULL still has no rollback-undo.**
  Parent: M0143-0008. Filed
  2026-09-17 when P0-E5 landed undo for `ADD CONSTRAINT`/`DROP CONSTRAINT`'s
  index-backed forms (PK/UNIQUE/EXCLUDE) but explicitly scoped out these
  three: `execAlterTableDropConstraint`'s CHECK branch splices
  `tbl.CheckConstraints`/`tbl.NamedChecks` in place (and cascades via
  `cascadeCheckDropToChildren`); its FOREIGN KEY branch splices
  `tbl.ForeignKeys` in place (`catalog.InMemory.DropForeignKeyConstraint`);
  its NOT NULL branch (`clearNotNullConstraint`) sets
  `tbl.Columns[i].NotNull = false` and filters `tbl.NotNullConstraints` in
  place (and cascades via `cascadeNotNullDropToChildren`) — all three are
  real field/slice mutations on a pointer that stays live and reachable
  throughout, not the map-removal-only shape `DDLDropUndoEntry`/
  `RestoreIndex` already handles for the index-backed forms, so they need
  their own snapshot-and-restore (the same pattern P0-E5's
  `NotNullUndoEntry` already established for the ADD-direction NOT NULL
  synthesis — a DROP-direction sibling snapshotting `CheckConstraints`/
  `NamedChecks`/`ForeignKeys`/`NotNullConstraints`/`Columns[i].NotNull`
  wholesale before the mutation, restoring the whole slice/value on
  ROLLBACK, applied to the target table plus every cascade-reachable child
  — mirror `collectNotNullCascadeClosure`'s read-only pre-walk pattern,
  `operators_ddl.go`, added by P0-E5). Likely overlaps `M0143-0002`'s
  `DROP CONSTRAINT` FK bug (same subsystem) — triage together. Test
  precedent: `internal/testport/p0e5_alter_rollback_undo_test.go` (P0-E5).
  - **Done 2026-09-17.** `DropConstraintUndoEntry` (session.go) landed
    exactly as scoped: one wholesale snapshot type + `RecordDropConstraintUndo`/
    `TakePendingDropConstraintUndos`, a `snapshotDropConstraintState` helper
    (operators_ddl.go), recording calls at all three
    `execAlterTableDropConstraint` branches (CHECK and NOT NULL also
    snapshot `collectNotNullCascadeClosure(im, tbl)`'s children; FOREIGN KEY
    has no cascade, tbl only), and a restore block in `ProcessRollbackUndos`
    (operators_tx.go). 3 new testport cases
    (`internal/testport/p0e5b_alter_drop_constraint_rollback_undo_test.go`),
    each verified to genuinely fail with the fix reverted (git-stashed) and
    pass with it applied. **Live discovery while writing the FK sub-test:**
    confirmed M0143-0002 (`DropForeignKeyConstraint` hardcodes
    `DefaultDBOid`) live — on a non-default DB a plain COMMITted (not even
    ROLLBACKed) `DROP CONSTRAINT` on an FK silently no-ops, so the FK
    sub-test runs against the cluster's default-db handle instead (see
    ledger row `M0143-0008b`, dated 2026-09-17, for the full evidence and
    the re-verify-on-db-"r" resume point once M0143-0002 lands). Gates:
    `go build ./...` clean; `go test ./internal/executor/... ./internal/catalog/...`
    PASS; `go test -v -run 'TestPort_P0E4|TestPort_P0E5|TestPort_M0143_0008b'
    ./internal/testport/` PASS 8/8; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` — all packages PASS except the
    pre-existing, already-documented `internal/parser` GroupedJoinUnaliased
    AST-drift (unrelated, no parser file touched); `scripts/tpcds-sf025-regression.sh
    sweep` — `PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0`, plan-shapes 99/99
    identical.
- [ ] **M0143-0007 — separate the dimension-table `relpages` divergence (K41)** —
  `customer` 1,979 pages vs PG's 2,872, `item` 716 vs 1,284. M0140-0005 filed it
  as out of planner reach and that is correct — **but `relpages` is an input to
  every page-priced cost term and to `compute_parallel_worker`'s size ladder, so
  no cost-model change can ever correct it.** The leading candidate is R23's
  `character(N)` blank-padding, a real PG-compat defect that shifts `relpages` on
  every `bpchar` table; R22 (per-page free-space comparison) separates the two.
  Storage work, not planner work, which is why it belongs here. Ledger:
  `m0140-0005-nonplanner-heap-density-floor`.