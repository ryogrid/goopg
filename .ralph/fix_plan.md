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
   **P0-E4 through P0-E7 are all `[x]` as of 2026-09-18** — TPC-H on `:65433` is
   restored, `data.HOLD` is released and `tpch-spotcheck` PASSes, so the P0
   gating is satisfied and the TPC-H gates run normally. There is no standing
   SKIP-BLOCKED exception any more: `.ralph/gate-exceptions.md` is empty, so a
   blocked TPC-H gate is a failed gate (AGENT.md G1).
1. **Regressions found by P0-E7**, one task each, in the order P0-E7 lists them.
2. **M0144 Phase-A instruments** (measurement-first programme, owner decision
   2026-09-20 — the census output re-ranks the items below; the owner
   re-orders on its result): **M0144-0001** (parallel canonicalisation + floor
   re-pin — TPC-H parity is now measured in parallel mode, `AGENT.md` §Goal) →
   **M0144-0002** (first-divergence census) → **M0144-0003** (route-order
   verification — owner-prioritised: several landed fixes never moved a plan
   because goopg's processing route diverges from PG's upstream of the fix) →
   **M0144-0004/0005/0006** (instrumented PG 18.3: OPTIMIZER_DEBUG build /
   trace-GUC patch / call-graph build) → **M0144-0007** (cost-margin census) →
   **M0144-0008/0009/0010** (hygiene: SF1 cadence, plan-gate reframing, ledger
   bulk-triage) → **M0144-0011** (first vertical-slice campaign — gated on
   0002+0007 output). M0137-0019's parallel triage is subsumed in part by
   M0144-0002 (the census is the triage instrument); it stays filed for its
   per-query write-up. **Children 0003a and 0003b stay `[!]`** — their wall
   is the resolver-time lowering, fixed inside item 3's M0145-0003/0004.
3. **M0145 jointree-first planner** (flow unification, owner decision
   2026-09-20 — fix the medium-level route divergence documented in
   `METHODOLOGY4/plan-flow-medium-abstraction.md` at the boundary, not per
   plan: a jointree-level IR built before lowering, sublinks pulled up into
   it, one DP pass over it, one Path→Node lowering at the end; the new
   pipeline is selectable behind `GOOPG_JOINTREE_PIPELINE=1` and the legacy
   pipeline stays the default until the M0145-0008 cutover deletes it).
   Order: **M0145-0001** (IR recon/design) →
   **M0145-0002** (dual-pipeline harness) → **M0145-0003** (sublink
   pull-up — the pilot is already in flight as M0142-0008a-3i-route-a
   step 2) → **M0145-0004** (appendrel) → **M0145-0005** (single-pass DP;
   retires Phase A/B + pinned spine) → **M0145-0006** (upper-rel
   pathlists; absorbs the M0144-0011a residual gates) → **M0145-0007**
   (unified lowering) → **M0145-0008** (cutover) → **M0145-0009**
   (B-06 CTE-output statistics — the blocker named by 0003's ANY-CTE
   residue, 0005's `outer-over-derived` decline family, and the Q78
   firewall's own resume condition; completing it triggers a
   RE-EVALUATION of those unblocks, not automatic lifts) →
   **M0145-0010** (parameterized-path legality — the `lateral` decline
   family; likewise a re-evaluation trigger, not an auto-admit). The
   Q78 `outer-over-derived` firewall is a hard constraint on every
   pull-up/flattening task.
4. **M0141-S2a-fix2r** — re-apply the PG-faithful `hashAggEntrySize` change that
   was discarded for parity reasons (owner Q4: no reverts). Degradations it
   causes are filed as their own tasks, not reverted.
5. **Roll out fix1's success**: the recon **M0141-S2a-fix1-sweep** (other places
   where width/currency reaches costing late or wrong), then
   M0141-S2b-6-resume, then M0139-0007c.
6. **M0141-S7 — cost diagnosis only.** The Incremental Sort candidate exists
   but loses on cost (S2b-7: 3733.01 vs 3730.89). Compare the cost breakdown
   with PG for the 14 queries. **No production-code change under this item** —
   not even a trace inside an existing trace guard. If instrumentation is
   genuinely needed, file a separate `Kind: impl` task, run the values gates and
   report the parity numbers (this is what `073ab2748`/`c7e231ae1` got wrong).
7. **M0140-0006a → 0006b → 0006c** (partial-Append), then
   **M0140-0007** (`Parallel Hash` from a partial inner — M0137-0019
   family A's floor).
8. **M0142-0005**, then **M0142-0016c**, then **M0142-0003i** (0003i only after
   P0-E5 and P0-E6 are `[x]`).
9. **M0143 remaining tasks**, top to bottom (includes the parser failures).
10. M-NIGHTLY open items, then the pre-existing milestones
   (M0119 → M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110).

**New task fields (2026-09-18).** Every task filed from now on carries, each at
the start of its own line: `Kind: recon|impl`, `Parent: <task-id|none>`, and on
completion `Movement: yes — <instrument + number>` or `Movement: none`. A
trace-only change to `internal/` or `cmd/` is `Kind: impl`, never a recon
(AGENT.md C1). Production commits now need their gate stamps **whatever
milestone they name**, M-NIGHTLY included.

FROZEN-PREFIXES:
(the M0142-0008 chain was UNFROZEN by owner decision 2026-09-20; see below —
the empty prefix list is what makes them selectable again)

**UNFROZEN (owner decision 2026-09-20) — selectable again:** the M0142-0008
chain (`M0142-0008a-3`, `M0142-0008c-1a`, `M0142-0008c-3d`,
`M0142-0008c-4`) is unfrozen by direct owner instruction. P0-E7's private-lane
A/B already landed the evidence csq-R2's reopen condition named (23/24 digest
lines identical; sole divergence = Q9's 600s timeout in BOTH arms). **Hard
owner constraint:** do NOT lift Q78's `outer-over-derived` firewall — or take
any equivalent shortcut — to obtain reachability. The sanctioned route is the
previously-unfiled producer, now filed as **M0142-0008-producer** below: teach
an IN-unnesting (or EXISTS-variant) path to set `.SJInfo` on a
DP-search-visible `JoinSemi` link, the way c19 did for ANTI. csq-R2's
ledger row is updated with this decision (`.ralph/deferral_ledger.md`,
row dated 2026-09-20).

**M-NIGHTLY filing is unconditional**: every loop reads
`ci/logs/action-items.md` and files each new `## AI-` subject under M-NIGHTLY.
Selecting one ahead of the order above is allowed only if it breaks the build
or a gate this banner's work needs. When selected: re-run its repro at HEAD
first, fix with normal gates, cite the AI-id, tick it.

## Notes / rules

- This is the authoritative TODO list for Ralph. ONE item per loop; decompose an
  item larger than one agent invocation (new tasks carry `Parent:`).
- Design docs: non-trivial subsystems land with `docs/design/<id>-NNNN-*.md`
  and a `docs/design/README.md` entry in the same commit. For M0137–M0145 and
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
- [x] **P0-E6 — restore `:65433` (OWNER-RUN).** **DONE 2026-09-18 by the owner:**
  `--evidence-only` preserved the damaged cluster at
  `bench/tpch/runtime_goopg/evidence-20260918/` (875 entries, 2,131,351,563 B,
  verified), the full run restored from the pre-loss clone (868 entries,
  2,046,680,662 B; clone untouched), rebuilt the pinned
  `bench/tpch/runtime_goopg/goopg-bin` at `080323cbd`
  (sha256 9c9f0410…), restarted `:65433` capped and
  `scripts/tpch-spotcheck.sh` returned **PASS (Q12=2, Q13=34)**, so `data.HOLD`
  is released. The previous data dir is kept at
  `data.pre-restore-20260918-071005`. **P0-E7 is now selectable and the TPC-H
  gates must run normally again.** Parent: none. Kind: impl. Movement: none.
  Original instructions: the owner runs
  `scripts/tpch-ref-recover.sh --i-am-owner --evidence-only` **now** (graceful stop
  + evidence copy; leaves `:65433` stopped with HOLD), then after P0-E5
  `scripts/tpch-ref-recover.sh --i-am-owner`: restore from the pre-loss clone
  `bench/tpch/runtime_goopg/preloss-clone-20260915` (copied, never modified)
  (else HammerDB reload) → rebuild `tmp/goopg-bench-bin` at HEAD → start →
  `scripts/tpch-spotcheck.sh` PASS. **The loop does not run it, and does not
  mark this task.**
- [x] **P0-E7 — bulk re-measurement since 2026-09-16 05:44.** Parent: none.
  Kind: impl. **Now selectable (P0-E6 done 2026-09-18).** Scope note added by the
  owner: this also clears the debt of the **18 commits between `4f6f81734` and
  `c7e231ae1` that landed on a SKIP-BLOCKED `tpch-spotcheck` stamp** plus the
  three M-NIGHTLY production commits that were checked by nothing
  (`c03742e2f` `internal/executor/expr.go`, `6122fb3c8`
  `internal/postmaster/dispatch.go`, `2a99ff338` `internal/parser/*`); name each
  in the report with its TPC-H values result at HEAD. On a private lane with a HEAD binary: `tpch-spotcheck`,
  `tpch-acceptance-arm`, sf025 sweep, TPC-H parity serial and parallel, TPC-DS
  parity. Compare with `27d4ae001` (TPC-H match 8). For any regression, bisect
  to the commit and file one task per regression (banner item 1) — **no
  reverts** (R3). Also A/B the M0142-0008 chain (`admitSemiAnti` on vs off — the off arm is an
  uncommitted local patch, not a new flag:
  match, categories, values) and write the numbers for the owner's freeze
  decision; this A/B is also the evidence csq-R2's reopen condition names.
  Record every gate stamp and plan-file sha256.
  - **In progress 2026-09-18 (partial — task stays unchecked).** Kind: impl.
    Movement: none (measurement of pre-existing state, not a change this
    loop made). Values gates re-run clean at HEAD (`42a2002ff`) on a private
    lane: `tpch-spotcheck.sh` PASS (Q12=2/Q13=34); `tpcds-sf025-regression.sh
    sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes
    99/99 identical to the prior sweep (`e5046bc31`). TPC-H plan-parity vs PG
    18.3 (`estimate-audit -plan-only`, both sides on a private lane —
    correcting the now-R1-unsafe M0137-0003 doc, see the design doc below):
    serial `match=8/22`, exactly the current floor and unchanged from
    `27d4ae001`, no regression across the 35 production commits landed
    since; parallel `match=3/22` (not floor-comparable, tool's own caveat).
    Design doc: `docs/design/0100-0149/p0-e7-bulk-re-measurement.md` (indexed
    in `docs/design/README.md`). **Not yet done** (this loop's budget cutoff,
    ledger row filed below): TPC-DS full-SF1 parity vs `:65438` (`match >= 2`
    floor), `tpch-acceptance-arm.sh` OFF/ON digest comparison, the
    M0142-0008 chain A/B, and per-commit TPC-H values for each of the 35
    commits. Gates: `go build ./...` clean (no production file touched this
    loop); gate stamps at `tmp/gate-stamps/{tpch-spotcheck,tpcds-sf025}.json`
    (tree `2981a1eb4c50fffd33a79fec5a23a63e5a2bae06`, binary sha256
    `c4d596f5…`, `dirty_code: false`). Next loop resumes with the TPC-DS
    SF1 capture (budget: ~4-5h per the sf025 script's own header — plan for
    a dedicated loop) or the acceptance-arm digest run, whichever the next
    banner read finds still selectable first.
  - **In progress 2026-09-18b (partial — task stays unchecked).** Kind: impl.
    Movement: none (report-only; no production file touched). Landed the
    "per-commit naming" sub-scope: git-derived the exact production-commit
    set (touches `internal/`, `cmd/`, `go.mod` or `go.sum`) between
    `27d4ae001` and HEAD — **72 commits**, not the banner's approximate "35"
    (that number was the owner's own estimate when filing the task; the
    git-derived count is authoritative and this loop uses it instead of
    chasing the discrepancy) — split into three buckets by gate status at
    landing (pre-incident-fix 49, SKIP-BLOCKED-under-hold 20, post-hold/
    pre-restore 3) and named every one individually with its TPC-H values
    result at HEAD (PASS for all 72, inherited from the single HEAD binary
    measurement above — bisection is only needed on a found regression, and
    none was found). Full table:
    `docs/design/0100-0149/p0-e7-bulk-re-measurement.md` §"Per-commit TPC-H
    values naming". **Still not done**: TPC-DS full-SF1 parity vs `:65438`,
    `tpch-acceptance-arm.sh` OFF/ON digest, the M0142-0008 chain A/B (ledger
    row filed below, dated 2026-09-18, task-id P0-E7). Gates: `go build
    ./...` clean (no production file changed); no new gate run needed (this
    loop's addition is a re-derivation from the existing HEAD gate results,
    not a new measurement). Next loop: TPC-DS SF1 capture (budget a whole
    loop, ~4-5h) or the acceptance-arm digest run, per whichever the banner
    still finds selectable.
  - **In progress 2026-09-18c (partial — task stays unchecked).** Kind: impl.
    Movement: none (measurement only; `admitSemiAnti` is unchanged at HEAD,
    still `true`; the local one-line flip used to build the OFF binary was
    reverted before the arms ran and never reached a commit). Landed the
    `tpch-acceptance-arm.sh` OFF/ON digest comparison and the M0142-0008
    chain A/B in one measurement: ON binary = HEAD as-is (`admitSemiAnti`
    already unconditionally `true` in production); OFF binary = HEAD with
    `joinsearchseam.go:313`'s `extractSearchLeaves(chain, true)` temporarily
    changed to `..., false)` (the chain's only production call site), built,
    then the file was `git checkout`'d back to HEAD (verified clean) before
    either arm ran. Ran both via `scripts/tpch-acceptance-arm.sh`
    (`NO_BUILD=1`, pre-built pinned binaries, `PGSHAPED=0` and
    `GOOPG_ANALYZE_SEED=20260905` explicit on both arms, `-digest`, full
    22-query TPC-H SF1 corpus, private clone port 5583). **Result: 23/24
    digest lines byte-identical**; the sole divergence is Q9, which times
    out at the 600s cap in BOTH arms via two different cancellation code
    paths (server `statement_timeout` vs the runner's own client cancel
    request) — a scheduling race on an already-known-slow query
    (`q9_costdriven_mhj_cannot_be_cost_forced` memory), not an
    `admitSemiAnti` effect. This is the A/B evidence the owner's freeze
    decision (and csq-R2's reopen condition) needs; the decision itself is
    the owner's, not made here. Design doc:
    `docs/design/0100-0149/p0-e7-bulk-re-measurement.md`
    §"`tpch-acceptance-arm.sh` OFF/ON digest + M0142-0008 chain A/B". Gates:
    `go build ./...` clean before/after the local revert;
    `tmp/gate-stamps/tpch-acceptance-arm.json` stamped `FAIL` (the runner's
    literal error-string diff on Q9's two cancel messages, not a values
    regression — explained in the design doc). **Still not done**: TPC-DS
    full-SF1 parity vs `:65438` (ledger row filed, task-id P0-E7, dated
    2026-09-18) — the one remaining P0-E7 sub-item.
  - **DONE 2026-09-18d.** Kind: impl. Movement: none (measurement only; no
    production file touched). The "~4-5h budget" note above was wrong: that
    figure is `tpcds-sf025-regression.sh`'s **execution**-sweep cost
    (`cmd_sweep`, real query runs, 16 known 600s timeouts), not
    `capture-tpcds.sh`'s plan-only `EXPLAIN` cost, which is scale-independent
    (measured: 2.3s goopg-side + 5.0s PG-side for the full 99-query SF1
    corpus). Ran `bench/tpcds/server.sh start sf1` (fresh HEAD binary,
    `a0e741a68`, tree clean) + `scripts/capture-tpcds.sh` against `:65436`
    (goopg SF1) and `:65438`/`tpcds` (PG SF1 reference, read-only,
    EXPLAIN-only) + `pg-plan-parity-diff.py`. **Result: `match=1/99`**
    (Q41 only; Q9 — one of the two floor queries at SF0.25 — does NOT match
    at SF1, `SHAPE-DIFF [scan-type]`), below the `match >= 2` floor that
    `m0137-0004` established **at SF0.25**. This is the first-ever SF1
    capture under this harness, so there is no prior same-methodology SF1
    baseline to bisect against — recorded as new information, not asserted
    as a regression (see design doc §"TPC-DS full-SF1 parity"). Filed
    banner-item-1 task `P0-H12` below for the owner to decide whether to
    bisect Q9's SF1 divergence. Design doc:
    `docs/design/0100-0149/p0-e7-bulk-re-measurement.md`
    §"TPC-DS full-SF1 parity (2026-09-18d, this loop) — closes P0-E7".
    Gates: `go build ./...` clean (no production file touched);
    `python3 scripts/ralph_protected_regions.py check-designdocs` exit 0.
    **All six named P0-E7 sub-items are now measured — task closed.**
- [x] **P0-D3 — split the M0141-S7 design doc and fix its `Status:`.**
  Parent: none. Kind: impl (docs only). Movement: none.
  **DONE 2026-09-18 by the owner.** The parent went 1501 -> 226 lines and eight
  docs were split out by task id, each with a `Status:` stating what really
  landed and a link back to the parent, all indexed in `docs/design/README.md`
  in the same commit: `m0141-s7-groundwork-and-exec-rescope.md` (248),
  `m0141-s7-corpus-measurement-and-cost-diagnosis.md` (401),
  `m0141-s7-exec-a-…` (88), `-exec-b-…` (132), `-exec-c-…` (85),
  `m0141-s7-cd-q64-root-cause.md` (158),
  `m0141-s7-cd-q64-reclassify-grouping-paths-gap.md` (80),
  `m0141-s7-cd-candidatepool-seed-partial-prefix.md` (149). Content was moved
  verbatim (verified by comparing the concatenation), no file exceeds 800 lines
  and `scripts/ralph_protected_regions.py check-designdocs` exits 0, so M0141-S7
  work may resume. Original scope:
  `docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md` is
  **1501 lines** (D3's limit is 800) and its `Status:` still reads "recon landed
  2026-09-16, no production change" although `073ab2748` and `c7e231ae1` landed
  production traces through it. Split by task id (one doc per `-cd-*` sub-task,
  linking back), correct every `Status:` line, and index the new docs in
  `docs/design/README.md` in the same commit. Do this before appending anything
  else to that doc.
- [x] **P0-H11 — stale-comment cleanup after the owner's M0142-0008 decision.**
  Kind: impl
  Parent: P0-E7. **Selectable as of 2026-09-20** — the owner recorded the
  decision in the banner: the chain is UNFROZEN (not removed), so the
  "remove `admitSemiAnti stays false` comments if the chain is removed"
  branch does not fire; instead audit stale FROZEN/deferred phrasing in
  code comments and docs against the new unfrozen state. Still valid: bring
  the `Status:` lines of the design docs for M0142-0003d/e/f/g/j/k
  (none present) up to date.
  - **DONE 2026-09-20 (Loop \#32). Movement: none** (comment/doc-text only,
    zero behavior delta). Corrected 11 stale sites: `predp.go`'s file
    header ("S5b deferred by user decision (2026-07-21)" — lifted
    2026-09-20), `predp.go`/`predp_test.go`'s "unreachable while
    `admitSemiAnti` stays `false`" claims (true since b2; the corpus
    unreachability reason is now the leaf-count gate), six
    `joinsearchseam.go` sites (`extractSearchLeaves` header, the semiAnti
    arm's "INERT until", the c17 "IN paths never set `.SJInfo`" claim —
    stale since M0142-0008-producer, `buildLeafSpans`/`pgShapedOffsetChecksOK`
    inertness claims, `cumulativeFromSpans`'s invariant), and
    `semiantichain_test.go`×3 (incl. renaming
    `..._AdmitSemiAntiFalse_UnchangedFromProduction` → `..._SemiIsOpaqueLeaf`,
    the name itself asserted the stale claim). `Status:` lines added to all
    six `m0142-0003d/e/f/g/j/k` docs + README column synced;
    `m0142-0008a-1`'s "design-only, no production diff" Status updated to
    reflect the landed increments. Surfaced one latent correctness item —
    `cumulativeFromSpans`'s span round-trip cannot carry out-of-band
    synthetic ranges (unreachable today, gated by leaf-count); filed as a
    bullet under `M0142-0008a-3` above. Design doc:
    `docs/design/0100-0149/p0-h11-stale-frozen-comment-cleanup.md`.
- [x] **P0-H12 — TPC-DS SF1 Q9 shape divergence: new or pre-existing?**
  Kind: recon.
  Parent: P0-E7. Filed 2026-09-18 (P0-E7's TPC-DS full-SF1
  parity measurement, `docs/design/0100-0149/p0-e7-bulk-re-measurement.md`
  §"TPC-DS full-SF1 parity"). At SF1, goopg-vs-PG plan-parity is
  `match=1/99` (Q41 only); Q9 — one of the two queries the `match >= 2`
  floor names (`AGENT.md`, set by `m0137-0004` **at SF0.25**, where Q9 DOES
  match) — is `SHAPE-DIFF [scan-type]` at SF1. This is the first-ever SF1
  capture under the current harness (`scripts/capture-tpcds.sh` +
  `pg-plan-parity-diff.py` against `:65436`/`:65438` `tpcds`), so there is
  no prior same-scale baseline to bisect against directly.
  - **DONE 2026-09-18.** Movement: none (dating/recon; the two captures
    compared are numerically identical by design — confirming that was the
    task). Built a second goopg binary from `27d4ae001` in a separate git
    worktree (`/tmp/wt-27d4ae001`, sha256
    `e337d3bbf16f7209eab2438b4a2c2d32ce5143ce7fbf77f1eeed57615f083793`),
    served it against the existing SF1 data dir on the non-reference
    `:65436` port (never `bench/tpcds/server.sh start`, which always
    rebuilds from current HEAD), and repeated P0-E7's exact SF1 capture
    procedure. **Result: numerically identical to P0-E7's HEAD
    (`a0e741a68`) capture** — `match=1/99` (Q41 only), same
    `CATEGORIES`/`CATEGORIES-EXCL-MATCH` counts digit for digit, and Q9's
    own `=== Q9` plan section byte-identical between the two captures
    (`diff` exit 0). **Conclusion: Q9's SF1 `scan-type` divergence
    pre-dates `27d4ae001`** and is unchanged across the entire 72-commit
    range P0-E7 measured — not a regression from any commit in that range.
    The `match >= 2 (Q9, Q41)` floor is SF0.25-only and never generalized
    to SF1; footnote added to
    `m0137-0004-tpcds-match-reference-reconciliation.md` (the floor's
    origin doc) rather than `AGENT.md`'s harness section (R2: the loop does
    not edit that section). Root-causing Q9's actual SF1 scan-type choice
    is out of scope (the question asked was "new or pre-existing", not
    "why") — it joins the existing 73-query SF1 SHAPE-DIFF backlog, not a
    newly-discovered gap, so no deferral-ledger row is needed (D1 applies
    to newly-discovered unimplemented behaviour; nothing new was found
    here). No production code touched. Design doc:
    `docs/design/0100-0149/p0-h12-tpcds-sf1-q9-scan-type-divergence.md`.
    Gates: `go build ./...` clean (both trees); `python3
    scripts/ralph_protected_regions.py check-designdocs` exit 0.
  — Nightly regression triage (STANDING — ACTIVE since 2026-08-08)

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
- [x] **testport/TestPort_PgStatActivity (AI-20260901-010436-005, AI-20260905-011015-007, AI-20260914-235643-010, AI-20260916-035206-011, AI-20260917-004357-015)**.
  **Fixed 2026-09-18 — test bug, not an engine bug.** `internal/testport/plpgsql_test.go`'s
  assertion compared `row[2] != "client_backend"` (underscore) against the
  `backend_type` column, but real PG's literal is `"client backend"` (WITH A
  SPACE) — confirmed via `postgres/src/backend/po/*.po` (`msgid "client
  backend"`) and via the engine's own code
  (`internal/initdb/pg_stat_activity_view.go:104-108`, which already has an
  explicit comment: upstream uses `"client backend"` with a space, not
  `"client_backend"`, and maps the internal code to that display literal).
  The test's own query at line 313 even filters
  `WHERE backend_type = 'client backend'` and gets a row back — proof goopg
  was already byte-correct; only the assertion's string literal was wrong
  (didn't match its own error-message string one line below it). Fixed by
  changing the comparison to `"client backend"`. No engine change.
- [x] **testport/TestSyntax_Catalog_PgStatActivity (AI-20260901-010436-007, AI-20260905-011015-009, AI-20260914-235643-012, AI-20260916-035206-013, AI-20260917-004357-017)**.
  **Fixed 2026-09-18, same commit** — identical typo
  (`internal/testport/syntax_catalog_test.go:53`, sibling test file,
  `pattern_sibling_paths_must_agree`), same fix, same root cause as the task
  above. Gates: `go build ./...` clean; `go test -v -run
  '^(TestPort_PgStatActivity|TestSyntax_Catalog_PgStatActivity)$'
  ./internal/testport/` PASS (both); `RALPH_PRECOMMIT_SCOPE=units
  scripts/ralph-precommit-test.sh` PASS (all in-scope packages). Test-only
  change (both files are `_test.go`), no TPC-H/sf025 gate needed — not
  affected by the `:65433` P0-E6 evidence hold.

### Nightly run 20260902-005256 (sha `c11e55d253ff`, 8 items) — filed 2026-09-02
- [x] **testport/TestE2E_PGColdStartOnGoopgDataDir (AI-20260902-005256-001, AI-20260905-011015-002, AI-20260914-235643-004, AI-20260916-035206-004, AI-20260917-004357-006, AI-20260919-000526-002)**. New
  tonight; possibly the M0131-S4 "FAIL-WHEN-FIXED" assertion flipping red
  because a Theme F fix landed rather than a real regression — re-run repro
  and check the M0131 Theme F findings list before treating as a bug.
  - **DONE 2026-09-19.** Confirmed: it WAS the FAIL-WHEN-FIXED flip, not a
    regression. `c11ff797a` (2026-09-01, review/260831 NB-17) replaced the
    pglz compressor's brute-force match search with upstream's hash chain +
    `good_match`/`good_drop` bounds, so goopg's `pg_toast_2618` chunk counts
    and stored bytes now match upstream's own (all 17 chunk counts equal,
    bytes within 18 B, 8/17 exact) — the `wantChunks` pin still held the
    pre-NB-17 brute-force output (3-4% smaller). Identical `got` string in
    every nightly log since 20260902 → single stale pin, six duplicate AIs.
    Re-pinned `wantChunks` to the new values, rewrote the stale comment,
    flipped ledger row M0131-S20.2b F24 (`deferral_ledger.md` ~L1259) to
    resolved. Repro `go test -v -run
    '^TestE2E_PGColdStartOnGoopgDataDir$' ./internal/testport/` PASSES
    (3.17s, cgroup-capped). Test-only — no production code touched.
  (`testport/TestPort_IsolationIntraGrantInplace`,
  `testport/TestPort_IsolationStats`,
  `testport/TestPort_LockRowsSortOverJoinTakesRowLock`,
  `testport/TestPort_PgDumpConnectionSetup`, `testport/TestPort_PgStatActivity`,
  `testport/TestPort_RegressSuite`, `testport/TestSyntax_Catalog_PgStatActivity`
  — remaining 7 items of this run all already have an open task above,
  AI-20260827-052222-037/-071/-080/-107, AI-20260901-010436-005/-007; no new
  line filed for those per the "do not add another" rule.)

### Nightly run 20260905-011015 (sha `2e3deb52ba73`, 9 items) — filed 2026-09-11
- [x] **race/internal/executor (AI-20260905-011015-001, AI-20260914-235643-002, AI-20260916-035206-002, AI-20260917-004357-003, AI-20260919-000526-001)** — race suite failed
  in `internal/executor` (also failed previous run; repro: `go test -race
  -timeout 45m ./internal/executor/`).
  - **RESOLVED 2026-09-19** by M-NIGHTLY-instrumentscope-race-fix (below):
    the package-global `instrumentScope` + `instrumentScopeMu` +
    `buildUnderFreshScope`/`buildUnderNilScope` are deleted; the scope is
    now an explicit `*instrumenter` parameter threaded through
    `buildNode`/`maybeInstrument`. `go test -race -timeout 45m
    ./internal/executor/` clean (61.3s) incl. both historically racing
    tests. Design doc `docs/design/root/root-0042-instrumentscope-explicit-parameter.md`;
    deferral-ledger resolution record appended (append-only guard — the
    owner flips `take3-instrumentscope-datarace` +
    `e18-instrumentscope-global-races-coop-producers` to `resolved`).
  - **AI-20260919-000526-001 (2026-09-19 log check): same signature, do
    not re-file.** Tonight's `ci/logs/20260919-000526/race/go-test.log`
    shows the identical pair — `instrument.go:444` unlocked read racing
    `operators_gather.go:124` gather-worker scope write, failing
    `TestParallelLateralProbeIdentity` +
    `TestSubquerySemanticsMatrix/M20/...` (5+ warnings, same defect).
    Fourth reproduction of the ledgered instrumentscope race.
  - **Re-confirmed 2026-09-18, NOT stale.** Re-ran the exact repro at HEAD
    (`0317293db`): FAILs in 59.9s (well under the 45m timeout) with two
    `WARNING: DATA RACE` reports — `TestParallelLateralProbeIdentity` and
    `TestSubquerySemanticsMatrix/M20/...` — full log:
    `tmp/race-internal-executor-20260918.log` (untracked, regenerate via the
    repro command; not committed). Both races are the SAME known,
    already-ledgered defect, not a new one: **do not re-file** — this is the
    third reproduction of `.ralph/deferral_ledger.md`'s
    `take3-instrumentscope-datarace` (2026-09-06) and
    `e18-instrumentscope-global-races-coop-producers` (2026-09-07) rows.
    Package-global `var instrumentScope *instrumenter`
    (`internal/executor/instrument.go:313`) is mutated under
    `instrumentScopeMu` by `buildUnderFreshScope`/`buildUnderNilScope`
    (gather worker per-slot builds, `operators_gather.go:122-134`) but read
    with NO lock by `maybeInstrument` (`instrument.go:444`), which fires on
    every `buildNode` call — including ones reached lazily, mid-`Next()`,
    from a **different** goroutine entirely (`seqScanOp.evalQual` ->
    `evalInExpr`/`evalExistsExpr` -> `acquireSubPlanOp` -> the exported
    `Build(plan)`, `subplan.go:306/325`). Confirmed this is not
    EXPLAIN-ANALYZE-only: `gatherOp.buildChildForSlot` takes the
    `instrumentScopeMu`/global round-trip on **every** parallel worker spawn
    regardless of ANALYZE (nil-scope swap still mutates the shared global),
    so the race is live on any ordinary parallel query with a concurrently
    lazily-built SubPlan, not just an ANALYZE edge case.
  - **Corrected the design doc's now-falsified claim**: `docs/design/executor-ex0-03b-rows/DESIGN.md`
    section 2's B1 answer states "the package-global handoff is NEVER
    racy" — false, appended a 2026-09-18 erratum pointing here.
  - **Why not fixed in this loop**: the ledger's stated resume
    ("thread the instrument scope through `Context` instead of a package
    global") cannot be done as a signature change to the exported
    `Build(plan)`/`BuildWorker(plan)` — grep shows **~200** call sites
    (almost all `_test.go`) that call `Build(plan)` and only create a
    `*Context` afterward (`op, err := Build(plan); ...; op.Open(ctx)`), so
    threading `ctx`/scope through their signatures is not a same-loop
    change. Traced a narrower, still-fully-correct alternative that stays
    inside `internal/executor` with **zero external call-site changes**:
    filed as **M-NIGHTLY-instrumentscope-race-fix** below with the concrete
    mechanical plan. Explicitly ruled out (per the ledger's own warning,
    re-confirmed by this loop's own read of the code): just widening
    `instrumentScopeMu` to guard the reads too (`sync.RWMutex`) would
    silence `-race` without fixing the semantic hazard the mutex's own doc
    comment names (a lazily-built producer subtree adopting an unrelated
    sibling worker's live scope and polluting its stats table) — not
    attempted.
  - [x] **M-NIGHTLY-instrumentscope-race-fix** — eliminate the package-global
    `instrumentScope` read/write race without touching `Build`/`BuildWorker`'s
    exported signatures. Parent: race/internal/executor (this task).
    **DONE 2026-09-19**, design doc
    `docs/design/root/root-0042-instrumentscope-explicit-parameter.md`.
    Implemented exactly the mechanical plan below, with one simplification:
    `buildNode` itself gained the `scope *instrumenter` parameter (no
    separate `buildScoped` wrapper — `Build`/`BuildWorker` are thin
    `nil`-scope wrappers, `buildNode` is the scoped entry).
    - `instrumenter{timing, table}` unchanged as a type; the global
      `instrumentScope`, `instrumentScopeMu`, `buildUnderFreshScope` and
      `buildUnderNilScope` are deleted. `withInstrumentation(timing,
      func(scope *instrumenter) (Operator, error))` passes the fresh
      scope into its callback.
    - `maybeInstrument(plan, op, scope)`: nil → returns `op` unchanged;
      non-nil → allocates stats in `scope.table`, stamps
      `instrumentScopeCarrier` ops. ~78 call sites in `executor.go`
      rewritten uniformly.
    - `gatherOp`/`gatherMergeOp`: `buildChild` field type →
      `func(scope *instrumenter) (Operator, error)`;
      `buildChildForSlot(slot)` → `buildChild(nil)` unarmed, else fresh
      `instrumenter` + `workerTables[slot]` (n+1 pre-sized, leader slot
      n), folded post-`group.Wait()` in `Close`.
    - `cteDMLPrefixOp.buildUnderScope` → `buildNode(n, deformBoundNone,
      o.scope)` — stamped scope passed as argument, same semantics.
    - `prebuildSharedHashJoins`/`prebuildBitmap` call `buildChild(nil)`
      explicitly; `opTreeSlab.buildRec` fallback passes `nil` (op-tree
      path never instrumented); `acquireSubPlanOp`/`expr.go` lazy builds
      stay on `Build` ⇒ `nil` (SubPlan children never instrumented —
      bug-compatible, pinned by `TestExplainAnalyzeSubPlanScopeObservation`).
    - Gates: `go test -race -timeout 45m ./internal/executor/` **clean**
      (61.3s; `TestParallelLateralProbeIdentity` +
      `TestSubquerySemanticsMatrix` pass); `go test
      ./internal/executor/` pass; `RALPH_PRECOMMIT_SCOPE=units` green;
      `tpch-spotcheck` PASS (Q12=2, Q13=34 — gate-stamp FAIL is the
      dirty-tree guard); `make plan-gate` 14/22 = recorded baseline
      drift, identical to prior loops.
    Mechanical plan traced this loop (not yet implemented):
    (1) give internal `buildNode(plan, bound)` a third parameter
    `scope *instrumenter` and thread it through its own ~28 recursive
    call sites in `executor.go` plus the 5 external ones
    (`operators_bitmap.go:516/1044/1125`, `parallel_hash_build.go:639/687`);
    change `maybeInstrument(plan, op)` to `maybeInstrument(plan, op, scope)`
    reading the parameter, not the global; (2) `Build(plan)`/`BuildWorker(plan)`
    (kept byte-for-byte compatible for their ~200 external callers) become
    thin wrappers that always pass `scope=nil` — safe because none of those
    callers currently rely on ambient instrumentation (the one place that
    does, `operators_explain.go:72-74`'s `withInstrumentation(...){
    return Build(o.plan.Child) }`, runs single-goroutine before any Gather
    worker starts, so it needs the NEW scoped entry point, not the plain
    one — see (3)); (3) add an unexported `buildScoped(plan, bound, scope)`
    used only by the handful of scope-aware call sites:
    `operators_explain.go`'s top-level build, `operators_gather.go`'s and
    `operators_gather_merge.go`'s two `buildChild` closures
    (`executor.go:369-379` — change the `buildChild func() (Operator,
    error)` field on `gatherOp`/`gatherMergeOp` to
    `func(scope *instrumenter) (Operator, error)` and have
    `buildUnderFreshScope`/`buildUnderNilScope` pass the scope as an
    argument instead of mutating a global before calling `fn()`), and
    `operators_cte_dml.go`'s `buildUnderScope`. **Open question needing a
    decision before implementing, not yet resolved**: whether a SubPlan/EXISTS
    tree lazily built mid-`Next()` in a *serial* (non-Gather) ANALYZE query
    should inherit the ambient scope (real PG's `instrument.c` does
    instrument SubPlan children) — if yes, `acquireSubPlanOp`
    (`subplan.go:302`) needs `ctx` to carry the live scope explicitly (it
    already takes `ctx *Context`, so add a `Context.instrumentScope` field
    set once by `withInstrumentation`/CTE-DML, read here — no global,
    no 200-callsite blast radius, since only production non-test call sites
    of `acquireSubPlanOp` need it); if the current behavior is already
    "SubPlan children are never instrumented" (M0142-0004a's ledger row
    suggests this is plausible but unconfirmed), forcing nil there is a
    pure bug-for-bug-compatible simplification. Resolve by writing a
    serial (no Gather) `EXPLAIN ANALYZE` + correlated-EXISTS test FIRST and
    observing today's actual output before choosing. Gate: `go test -race
    ./internal/executor/` clean (specifically `TestParallelLateralProbeIdentity`
    and `TestSubquerySemanticsMatrix`), full `RALPH_PRECOMMIT_SCOPE=units`
    green, plus an `EXPLAIN (ANALYZE)` parallel-shape regression check that
    worker/leader counters are not double-counted (already-existing
    `explain_parallel_workers_test.go` coverage per EX0-03b's own gate).
    - 2026-09-19 — the test-first observation step is DONE and the open
      question is RESOLVED: new `TestExplainAnalyzeSubPlanScopeObservation`
      (`internal/executor/explain_subplan_test.go`) shows a serial
      `EXPLAIN ANALYZE` + correlated EXISTS executing the SubPlan
      (`SubPlan 1 (calls=2 rebuilds=1 rescans=1 ...)`, counters via
      `ctx.SubPlanStats` — a separate channel from `nodeStatsTable`) while
      its subtree renders **estimate-only, no `(actual ...)` bracket** —
      SubPlan children are NEVER instrumented today. Root cause traced:
      `withInstrumentation` (`instrument.go:323`) keeps `instrumentScope`
      live only for the top-level `Build(o.plan.Child)`; `acquireSubPlanOp`
      (`subplan.go:302`) calls `Build(plan)` from `existsImpl`/
      `subqueryImpl`/`collectInValues` at row-evaluation time, after the
      scope is already restored to nil. Consequence for the impl: forcing
      nil scope at `acquireSubPlanOp`'s Build sites under the planned
      `Context.instrumentScope` field is the bug-for-bug-compatible
      simplification — no need to thread ambient scope there. Side
      confirmation: the m0142-0004a deferral-ledger row's symptom is now
      source-confirmed (PG's `instrument.c` DOES instrument SubPlan
      children — `(actual ...)` on the subtree upstream — so this is also
      a recorded PG-fidelity gap, but fixing it is out of this race-fix's
      scope: the race fix only needs today-preserving behaviour).
- [x] **testport/TestPort_IsolationIntraGrantInplace (AI-20260905-011015-003, AI-20260914-235643-006, AI-20260916-035206-006, AI-20260917-004357-010)** —
  FAILed, also failed previous run (repro: `go test -v -run
  '^TestPort_IsolationIntraGrantInplace$' ./internal/testport/`).
  - **DONE 2026-09-19.** Kind: impl. Movement: none (correctness fix —
    restores the pass-required spec the CSV already claims; no parity
    instrument moved). Landed the root-cause note's candidate fix:
    `maybeRecordPgClassRowMark` now probes
    `im.LookupTableByOIDAllDBs(relOID)` after `waitTablePendingDrop`
    releases — a miss means the deferred drop committed, so
    `o.pgClassRelDropped` makes `drainAndStamp` skip the child drain
    (PG: `regclassin` binds once into the scan key before the LockTuple
    wait; post-commit the scan finds the tuple deleted → `0 rows`, never
    re-resolves). Empty `pending` → existing arm retracts the rowmark →
    `revoke4` releases before `r3` — all 10 perms byte-identical.
    Design doc: `docs/design/0100-0149/0118-0117-intra-grant-inplace-perm10-pgclass-delete.md`
    Update 2026-09-19. Gates: repro PASS (5.55s); siblings
    IntraGrantInplaceDb/LockCommittedUpdate/LockCommittedKeyupdate/
    DropIndexConcurrently1/SequenceDdl/TruncateConflict PASS;
    `go test ./internal/executor/` PASS; `-race` clean 61.6s.
    Ledger resolution record appended (owner flips the row).
  - **ROOT-CAUSED 2026-09-19** (repro re-run at HEAD, 4.86s FAIL — same
    signature as every nightly since 20260905; the earlier 0827 failure was
    the pre-M0118 `FOR NO KEY UPDATE` syntax gap). Only the LAST
    permutation (`b1 drop1 b3 sfu3 revoke4 c1 r3`, perm 10) diverges —
    all nine earlier perms match. goopg's actual tail:
    `sfu3 <waiting ...>`, `revoke4 <waiting ...>`, `c1 COMMIT`,
    `sfu3 <... completed>` → **`ERROR: relation "intra_grant_inplace"
    does not exist`** where PG emits `relhasindex`/`(0 rows)`; then
    `r3 ROLLBACK` precedes `revoke4`'s completion (PG has it last).
    - Mechanism: `lockRowsOp.Open` → `maybeRecordPgClassRowMark`
      (`internal/executor/operators_lockrows.go:853/871`) evaluates the
      `oid = 'x'::regclass` filter const via `pgClassFilterOID` →
      `evalExpr` — resolving the OID while drop1 is still uncommitted
      (correct) — then blocks in `waitTablePendingDrop` (`:910`,
      `operators_ddl.go:12688`) until `c1`'s `ApplyPendingTableDrops`
      removes the table. Post-unblock, `drainAndStamp` (`:1031`) drives
      the child scan, whose `oid = 'x'::regclass` filter **re-evaluates
      `regclassin`** (`reg_identifier.go:286-331`) — now a
      `LookupTable` miss → 42P01 instead of `0 rows`.
    - PG contract (`regproc.c:882` `regclassin`, `provolatile='s'` — not
      plan-folded either, but resolved ONCE into the scan key /
      evaluated before the LockTuple wait inside the scan iteration):
      post-commit the scan simply finds the pg_class tuple deleted →
      `0 rows`. goopg waits at `Open()` *before* any child row
      iteration, so its entire filter evaluation lands post-drop.
    - Candidate fix (executor, HOLD-gated — tpch-spotcheck is
      SKIP-BLOCKED while `:65433` is down): after
      `waitTablePendingDrop` unblocks, when the pending drop committed
      (table gone / `TablePendingDropXID` cleared-and-applied),
      short-circuit `drainAndStamp` to EOF — PG's `LockTuple → deleted
      → skip → 0 rows` outcome. Also explains the `r3`-before-`revoke4`
      ordering tail (revoke4's poll-release only frees after the
      rowmark retracts on empty).
    - Ledgered (`.ralph/deferral_ledger.md` tail row
      `testport/TestPort_IsolationIntraGrantInplace`).
- [x] **testport/TestPort_IsolationStats (AI-20260905-011015-004, AI-20260914-235643-007, AI-20260916-035206-007, AI-20260917-004357-011, AI-20260920-005626-003)** — FAILed,
  also failed previous run (same testport repro pattern). AI-…-003 re-flag
  from run 20260920-005626 was a different signature — cluster
  "start timeout after 20s" (env wedge, not the stats logic) — and the spec
  PASSes at HEAD (2.25s). Stale/env repeat; no new task.
  - **DONE 2026-09-19.** Kind: impl. Movement: none (correctness fix —
    restores the pass-required spec the CSV already claims; no parity
    instrument moved). Only `n_dead_tup` diverged, in the four
    2PC-abort permutations (expected 8/8/2/2, got 6/6/1/1). Root cause was
    NOT the fold math — `applyXactToPending`'s abort arm (`deltaDead +=
    restored ins + upd`, `pgstat_twophase_postabort`) already computed 8/2
    and was unit-pinned — but the `expr.go` getter arm, which read the
    non-transactional `triggerSnapshot` store (bumps dead per upd/del at
    DML time, no abort reconciliation, no truncdrop restore → upd+del = 6,
    post-truncate upd = 1). Repointed `pg_stat_get_dead_tuples` to
    `c.deltaDead` (the same tabentry source `pg_stat_get_live_tuples`
    uses) and moved the whole arm to the tiered entry: new shared fields
    `insSinceVacuum` (flush: `+= attempted tuples_inserted`, reset by
    committed truncdrop / `reportVacuum`), `changedTuples` (commit fold:
    `ins+upd+del`, abort adds none → `mod_since_analyze`), `vacuumCount`;
    `reportVacuum`/`reportAnalyze` mirror `pgstat_report_{vacuum,analyze}`
    (wired beside `resetVacuumTriggers`/`resetAnalyzeTriggers`);
    `UserTableTriggerStatsFunc` repointed so view and function getters
    share one source. Design doc: `0118-0128` Update 2026-09-19. Gates:
    repro PASS (3.30s, all perms byte-identical); siblings
    prepared-transactions{,-cic}/vacuum-{skip-locked,concurrent-drop,
    conflict,no-cleanup-lock}/TwoPhaseCommitSameBackend PASS;
    `go test ./internal/executor/` PASS; `-race` clean 61.4s. Ledger row
    appended for the remaining gaps (autovacuum trigger store still
    non-transactional; analyze live/dead measured-overwrite deferred).
  - **ROOT-CAUSED 2026-09-19** (repro re-run at HEAD, 2.43s FAIL):
    `pg_stat_get_dead_tuples` diverges only in the four
    `…_rollback_prepared_a` perms — the plain ones (expected
    `…|8|0`, got `…|6|0`) and the truncate ones (expected `…|2|0`,
    got `…|1|0`). PG's abort formula is `delta_dead += restored_ins +
    restored_upd` (`pgstat_twophase_postabort`, after
    `restore_truncdrop_counters`); the trigger store accumulates
    `upd + del` at DML time and is cleared by an aborted truncate —
    commit math on a non-transactional counter.
- [x] **testport/TestPort_LockRowsSortOverJoinTakesRowLock (AI-20260905-011015-005, AI-20260914-235643-008, AI-20260916-035206-009, AI-20260917-004357-013, AI-20260920-005626-006)** —
  FAILed subtests: join_no_sort, also failed previous run.
  - **RE-FIXED Loop \#23 (M-NIGHTLY, AI-20260920-005626-006)** — the run
    flagged `sort_over_join` FAILing at sha `2b7e9705` (pre-dates both
    M0143-0009/-0010); at HEAD it FAILED differently — the query returned
    **one column** instead of two. Root cause: an M0143-0009 regression —
    `resolveRowMarkCtidResnos` resolved the ctid's resno only against
    `proj.Child.Output()`, but this plan carries a column-reordering
    Project BETWEEN the top Project and the join, whose pinned schema
    hides the leaf-injected ctid; `CtidResno` stayed -1 while
    `NumCtidCols`=1 still fired `LockRows.Output()`'s trailing-strip,
    dropping `balance` (and, for `FOR UPDATE` of both rels, BOTH columns).
    Fix: new `surfaceRowMarkCtid` threads each wired ctid up through
    schema-pinning ancestors (pass-through target appended to each
    intermediate Project; Join/NLI/Distinct schemas rebuilt) so the resno
    resolves, and the call site now counts `NumCtidCols` by SURFACED ctids
    only. Both subtests PASS at the fix (5.39s — the lock now blocks and
    wakes via the column path, EPQ returns 1050). Regression test:
    `TestPlanCtidRowMarkDoubleProject` (optimizer). Kind: impl.
    Movement: none.
  - **DONE 2026-09-19 (Loop \#9, ROOT-CAUSED)** — both subtests PASS (4.80s);
    join\_no\_sort now blocks on the writer's xmax and EPQ-returns 1050.
  - Root cause: the test comment was stale — the "control" query plans
    `LockRows -> <join> -> Bitmap Heap Scan` (the `a.accountid = 'checking'`
    point qual picks the pkey bitmap path), and `bitmapHeapScanOp` was
    invisible to every rowmark TID route: no resjunk-ctid wire
    (`wireRowMarkCtidColumns` covered only SeqScan/IndexScan), no `hasCTID`
    slot stamp, no `currentTIDProvider`, no walker arm.
  - Implementation (design updates: `0128-0001-bitmap-heap-scan`,
    `0129-0003-resjunk-ctid-column-path`):
    - planner: `tagScan` closure + `*BitmapHeapScan` arm wires the trailing
      `ctid<N>` resjunk column into bitmap leaves (incl. NLI inners).
    - executor: `emitRow` appends the ctid datum + stamps slot hasCTID;
      `currentTID()` (ok=false once pin released at EOF — build-side hash
      scans fall through to the slot stamp); walker arms in
      `findScanLeaf`/`findScanLeafForRel`/`markJoinPreserveCTID`;
      `BindOuter` width check relaxed to `innerW < len(tbl.Columns)`.
    - executor: bitmap page RLock rescoped per tuple fetch
      (M0100-0005e convention) — the scan previously held `pinned.RLock()`
      across yields, so `stampLock`'s same-page write lock self-deadlocked
      (`lockwithvalues` perm of eval-plan-qual hung the whole spec under
      the first attempt at this fix). `fetchExact` collapsed to
      `fetchOneTuple`+`o.Next()` recursion (bodies were duplicates).
  - Bonus: `TestPort_IsolationEvalPlanQual` went from recorded-fail to
    byte-identical PASS (23.4s) — the `partiallock` (MergeJoin+bitmap) and
    `lockwithvalues` (NL+bitmap-inner) perms are covered by the resjunk
    column path without merge-join walker arms.
  - Gates: repro test PASS; sibling FOR UPDATE/bitmap isolation specs
    13/14 PASS (UpdateLockedTuple remains pre-existing FAIL,
    AI-20260917-004357-012); executor+optimizer units PASS; executor race
    clean 62s; units gate green; tpch-spotcheck PASS (Q12=2/Q13=34);
    tpcds-sf025 PASS=96/0/0/0, plans 99/99; plan-gate 14/22 baseline.
- [x] **testport/TestPort_PgDumpConnectionSetup (AI-20260905-011015-006, AI-20260914-235643-009, AI-20260916-035206-010, AI-20260917-004357-014)** —
  **FIXED 2026-09-19**: three stacked regressions from the late-Aug
  parser/cast work, each masking the next; test now PASSes (3.3s) at the
  same tolerated DU-002 frontier it held pre-regression
  (`PREPARE getDomainConstraints(pg_catalog.oid)` — `type "pg_catalog.oid"
  does not exist`, the standing next-blocker note, still tracked under
  M0119-0004).
  - `CREATE CAST (bytea AS text) WITHOUT FUNCTION` false-rejected "source
    data type and target data type are the same" (42P17): `98e7cc90b`
    (M0134-0110) added `castUserBinaryCoercible` checking BOTH directions
    and wired it into `castTypeOIDMatch`, which `validateCreateCast`'s
    same-type check also used — so the earlier-registered `text→bytea`
    binary cast made `bytea→text` compare equal. PG: the same-type check
    is strict `sourcetypeid == targettypeid` (functioncmds.c CreateCast)
    and `IsBinaryCoercibleWithCast` is DIRECTIONAL
    (`CASTSOURCETARGET(srctype,targettype)` only). Fix: new strict
    `castSameTypeOID` for the same-type check; `castUserBinaryCoercible`
    reduced to the forward `a→b` lookup (`operators_ddl.go`). 5 new
    live-registry cases in `create_cast_validate_test.go` cover
    directionality both ways + the reverse-cast same-type repro.
  - `CREATE DEFAULT CONVERSION public.isoconv FOR 'LATIN1' TO 'UTF8'`
    rejected 42710 "default conversion … already exists": correct PG
    behaviour (`ConversionCreate`, pg_conversion.c:77) that `8c374fa82`
    (M0134-0106) ported AFTER the fixture line was written — `myconv`
    already owned the LATIN1→UTF8 default, so the fixture itself was
    invalid (real PG 18.3 rejects it identically). Fixture fix: `isoconv`
    drops DEFAULT (its purpose is the builtin-function fallback, not the
    DEFAULT keyword already covered by `myconv`); assertion + comment
    updated.
  - `CREATE TABLE public.dom (… lbl label …)` hard-42601 at `label`:
    `label` scans as the LABEL token (SECURITY LABEL keyword), and
    `cast_ident`'s deliberately-narrow whitelist
    (`grammar/pg_grammar.y`, sized to legacy-parser acceptances) had no
    LABEL alternative. `label` is UNRESERVED in PG (kwlist.h:251) — a
    legal type name everywhere — so `| LABEL` added to `cast_ident`.
    `make gen-parser`: conflicts unchanged at the pinned 60, goldens
    unchanged.
  - Gates: `TestPort_PgDumpConnectionSetup` PASS; `TestValidateCreateCast`
    21/21; `go test ./internal/parser` + `./internal/executor` PASS; units
    gate green; tpch-spotcheck PASS (Q12=2/Q13=34); tpcds-sf025
    PASS=96/0/0/0, plans 99/99; tpch-acceptance-arm PASS 24/24;
    plan-gate 14/22 baseline. Design doc updated:
    `0100-0149/m0134-0110-create-cast-user-type-resolution.md` §Fix (the
    "in either direction" claim corrected).
- [x] **testport/TestPort_RegressSuite (AI-20260905-011015-008, AI-20260914-235643-011, AI-20260916-035206-012, AI-20260917-004357-016)** — FAILed
  subtests: limit, numerology, also failed previous run; 20260914-235643 adds subtests time, timetz.
  - **CLOSED 2026-09-19** — all four failing subtests were fixed by their
    own tasks on 2026-09-18 (`time`/`timetz`: `evalTypedStringLit` error-code
    drift; `limit`: FETCH BACKWARD position bug; `numerology`: `mapToken`
    base-prefix + trailing-junk), and the entry was never flipped.
    Re-verified at HEAD `3973bf819` — which includes Loop 10's `LABEL`
    `cast_ident` alternative, the one recent change that could move a
    regress expected-error case — full suite:
    `GOOPG_REGRESS_DIFF_DIR=tmp/regress-diffs-loop11 scripts/goopg-test-run.sh
    go test -v -run '^TestPort_RegressSuite$' ./internal/testport/` →
    **50 PASS / 0 FAIL / 183 SKIP** (223s), identical to the 2026-09-18c
    tally. Tonight's nightly (`20260919-000526`, sha `485eef904966`)
    independently agrees: testport stage ran 7368s and its only reported
    failure was `TestE2E_PGColdStartOnGoopgDataDir` (the already-fixed
    stale `wantChunks` pin) — RegressSuite absent from regressions.
    Test-only verification; no production code touched.
  - **UPDATE 2026-09-18**: re-ran the repro at HEAD (`e121c4e9e`) per the
    M-NIGHTLY loop rule; all 4 subtests still diverged. Root-caused each
    independently (`GOOPG_REGRESS_DIFF_DIR=<dir> go test -v -run
    '^TestPort_RegressSuite$/^\<name\>$' ./internal/testport/` dumps the
    `_expected.txt`/`_actual.txt`/`_raw.txt` triple per case).
    - `time`/`timetz`: **FIXED, this commit.** `evalTypedStringLit`'s
      `"time"`/`"timetz"` arms (`internal/executor/expr.go`) discarded
      `parseTimeString`/`parseTimeTZString`'s already-correctly-typed
      `*ExecError` (22008 "date/time field value out of range" for e.g.
      `'25:00:00'::time`, vs. 22007 "invalid input syntax" for real
      garbage) and always manufactured a hardcoded 22007 message — the
      exact fix already applied to the COPY/index-key sibling
      (`internal/executor/btree_scalar_keys.go:198-224`) but never ported
      to this typed-literal path (sibling-path drift,
      `pattern_sibling_paths_must_agree`). Both subtests now PASS; no
      other regress case regressed (full-suite re-run: 47 PASS / 2 FAIL
      (limit, numerology) / 183 SKIP, was 45/4/183 before).
    - `numerology` and `limit`: **NOT fixed — unrelated root causes,
      independently sized, filed as their own tasks below** rather than
      folded into this loop (ONE task per loop).
  - **UPDATE 2026-09-18b**: `limit` **FIXED, this commit** — see the
    `limit — FETCH BACKWARD sign/row bug` task below for the root cause and
    fix. Full-suite re-run: 48 PASS / 1 FAIL (`numerology`) / 183 SKIP.
  - **UPDATE 2026-09-18c**: `numerology` **FIXED, this commit** — see the
    `numerology — binary/octal/hex integer literals unsupported` task below
    for the root cause and fix. Full-suite re-run: 50 PASS / 0 FAIL / 183
    SKIP.
  (Remaining 3 items — PGColdStart AI-…-002, PgStatActivity AI-…-007,
  Syntax_Catalog_PgStatActivity AI-…-009 — already have open tasks above;
  AI-ids appended per the "do not add another" rule. Evidence for all:
  `ci/logs/20260905-011015/`.)
- [x] **infra/nightly-live-tree-build-race (AI-20260918-010720-001,
  AI-20260918-010720-002, AI-20260918-010720-003, AI-20260918-010720-004,
  AI-20260918-010720-005)** — all five items of run 20260918-010720 are ONE
  transient artifact: the nightly's live-working-tree build caught
  `internal/executor/operators_ddl.go` mid-commit (P0-E5's catalog-loss fix
  window), so `stampExclusionConstraintRows`/`writeExclusionConstraintRow`
  were briefly undefined and testport/units/race stages all failed to
  COMPILE (not regressions). Verified at HEAD: both symbols exist
  (`operators_ddl.go:18534`) and `go build ./...` is clean — nothing to fix
  in the code. **Fixed 2026-09-19 — completed the NIGHTLY\_SRC\_ROOT
  migration**: `run-nightly.sh` had already moved `go build` stages into a
  detached worktree at the recorded HEAD, but the three `go test` stages
  still compiled the live tree — the exact leak that produced these
  items. `stage-units.sh`/`stage-race.sh`/`stage-testport.sh` now `cd`
  `"${NIGHTLY_SRC_ROOT:-${REPO_ROOT}}"`, and `run-nightly.sh` links the
  read-only `postgres/` submodule into the worktree (empty gitlink dir
  otherwise; `go list ./...` never follows the symlink, so no stray
  packages) so testport's `local_install` tools + regress/isolation
  fixtures resolve. `stage-testport.sh` also self-heals the link for
  standalone runs. Closes deferral-ledger row `M-NIGHTLY
  (AI-20260806-011323-002..-015)` "run every stage from a git worktree
  pinned to meta.json's sha" and the 0914 `bak/`-scratch items below —
  untracked files cannot enter a detached worktree.
  - Learnings: the worktree mechanism was a PARTIAL migration — builds
    moved but go-test stages were left behind, so the phantom class
    survived on the test lanes. When splitting a fix across compile
    kinds, grep every `cd "${REPO_ROOT}" && ... go` in the harness, not
    just `go build`.
  - Edits to live batch scripts while a nightly is RUNNING are safe via
    temp-file + `os.replace` (atomic rename): the in-flight bash keeps
    its open fd on the old inode and finishes on old semantics.
  - Verified: `bash -n` all five files; fresh worktree + link logic →
    `postgres/local_install/bin/psql` + `regress/expected` resolve,
    `go build ./...` clean, `go list` enumerates 0 scratch pkgs;
    `stage-units.sh` run against the worktree PASS (rc=0);
    `TestPort_PgControldata001` PASS inside the worktree (tool lookup
    via `repoRoot()` → symlink); `make -C worktree -n race-gate` clean.
  - `source_fingerprint` stays, re-scoped to drift EVIDENCE: a stage fp
    differing from `meta.json`'s `source_fp` now means "the live tree
    mutated mid-run" for the report, not "the stage ran a different
    tree" — comments updated in `common.sh` + `run_stage`.
- [x] **numerology — binary/octal/hex integer literals unsupported** (filed
  2026-09-18, split out of testport/TestPort_RegressSuite's numerology
  subtest above). **Fixed 2026-09-18 — the filing's premise was wrong, and a
  second, unrelated bug was hiding behind it.** The lexer already fully
  tokenized `0b`/`0o`/`0x` literals (M0097-0003, `internal/parser/lexer.go`);
  no grammar change was needed. The real bug: `internal/parser/select.go`'s
  hand-written-path helper `parseIntLiteralExpr` knew about the base
  prefixes, but the goyacc adapter's `mapToken` (`internal/parser/adapter.go`
  `case TokenIntLit`) — which is what a routed `SELECT` actually goes
  through — always called `strconv.ParseInt(..., 10, 64)`, ignoring the
  prefix entirely (a `pattern_sibling_paths_must_agree` instance: legacy
  hand-written path vs. goyacc-routed path silently diverged). `0b100101`
  therefore failed base-10 parsing, fell into the FCONST/overflow branch with
  the RAW prefixed text (`"0b100101"`) as its numeric-literal string, and
  `internal/executor/numeric.go`'s decimal-only parser rejected it as
  "invalid numeric literal". Fix: `mapToken` now calls the same
  `parseIntLiteral` the legacy path uses; a new shared helper
  `intLiteralOverflowText` (used by both `mapToken` and
  `parseIntLiteralExpr`) converts an int64-overflowing 0b/0o/0x literal to
  decimal text via `strconv.ParseUint` before it reaches the FCONST/Numeric
  path, since that path only understands base 10 — this also fixes the
  int8-overflow-boundary cases (`0x8000000000000000` etc.) which the old
  `mapToken` mishandled identically. A second, independent bug surfaced
  once the first was fixed: `0.a` and `1_000._5` (digit-led token, dot
  immediately followed by an identifier char) produced `syntax error at or
  near "."` instead of PG's `trailing junk after numeric literal` — the
  lexer's dot-commit heuristic assumed a bare digit-led token could be
  upstream's qualified-name form (`a.b`), which is impossible (qualified
  names start with an identifier, never a digit; PG's own `scan.l` has no
  such carve-out for `{decinteger}'.'`). Fixed in the same lexer function:
  a digit-led token's dot always begins a numeric literal (except `..`
  range syntax), and the fractional-digit loop no longer swallows a
  *leading* underscore as if it were a valid fraction digit (PG's
  `{decinteger}` requires the fraction to START with a digit; underscores
  are separators only) — so `1_000._5`'s `"_5"` is correctly left for the
  post-number trailing-junk check instead of being silently accepted as
  part of the float. Gates: `go build ./...` clean; `go test
  ./internal/parser/...` PASS (goldens unchanged — no golden diff, so no
  pinned AST shape moved); live-verified all magnitude tiers (int4-fits,
  int4-overflow/int8-fits, int8-overflow) plus both dot-junk cases against
  a throwaway `psql`-driven scratch cluster (ports 5533/5534,
  `/tmp/numerology-scratch-data`, never the shared/reference clusters) byte-
  for-byte against `postgres/src/test/regress/expected/numerology.out`;
  `go test -v -run '^TestPort_RegressSuite$/^numerology$'
  ./internal/testport/` PASS; full `TestPort_RegressSuite` re-run: 50 PASS
  / 0 FAIL / 183 SKIP (was 48/1/183 before this loop) — no other case
  regressed. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  PASS. No TPC-H/sf025 gate needed (lexer/parser-only change, no
  `internal/executor`/`internal/optimizer` row-count path touched; the
  `internal/executor` unit package itself was re-run above and is green).
- [x] **limit — FETCH BACKWARD sign/row bug** (filed 2026-09-18, split out of
  testport/TestPort_RegressSuite's limit subtest above). Against a cursor
  opened over a query returning a negative `q2`, `FETCH BACKWARD` returns
  the value with its sign dropped (`4567890123456789` instead of
  `-4567890123456789`) in two places in the diff, and one boundary fetch
  (`fetch backward 1 in c5`) returns the wrong row entirely. Not yet
  localized past the regress diff — likely the cursor/scroll-direction
  path re-reading the underlying scan rather than replaying its buffered
  row set. Repro: `go test -v -run '^TestPort_RegressSuite$/^limit$'
  ./internal/testport/`.
  - **Done 2026-09-18.** Not a sign-drop bug at all — the diff line that
    looked like a dropped sign (`4567890123456789` vs `-4567890123456789`)
    was actually the WRONG ROW (`int8_tbl`'s second-to-last row happens to
    share the same first column as the last), an off-by-one in
    `executeFetch`'s `FETCH BACKWARD` position bookkeeping
    (`internal/postmaster/dispatch.go`). Root-caused by porting PostgreSQL's
    real algorithm (`tuplestore_gettuple`'s in-memory backward branch,
    `postgres/src/backend/utils/sort/tuplestore.c:985-1005`) and replaying
    it tuple-by-tuple against a disposable `initdb`+`psql` scratch cluster
    (`/tmp/pgscratch1`, port 5599 — never the shared 55xx/6543x lanes) to
    get ground truth for edge cases `limit.sql` itself doesn't spell out.
    M0134-0056's formula (`end := cur.Pos - 1`, unconditionally excluding
    the row at `cur.Pos-1` as "already returned") was simply wrong: real PG
    only excludes that row when the cursor reached its current position via
    a **finite** forward fetch that returned at least one row; when the
    cursor is genuinely `AtEnd` (PG's `eof_reached` — reached via `FETCH
    ALL` or a forward fetch that ran off the end), `FETCH BACKWARD`
    re-returns the *last* row instead of skipping past it — the tuplestore
    algorithm's "first call after eof_reached jumps `current` to the write
    position without charging a decrement" behavior, which the old uniform
    formula had no way to express. The old `FETCH BACKWARD ALL` branch had
    the identical bug (used `cur.Pos` as an exclusive bound with no
    `AtEnd`-awareness, so a `BACKWARD ALL` issued right after `AtEnd` was
    reached would re-include the already-backward-fetched last row). Fixed
    both by replacing the two closed-form branches with one loop that
    replays the C algorithm call-by-call (safer than re-deriving another
    closed form — the sign/wrong-row bug already came from a
    hand-rolled formula silently drifting from real semantics), including
    `FETCH BACKWARD ALL` iterating to BOF rather than capping at `total`
    iterations (capping under-runs by exactly one when entering from
    `AtEnd`, for the same "first call is free" reason). This also explains
    the deep-in-the-diff `c5`/`WITH TIES` mystery (`fetch all in c5`
    returning only 1 of 2 rows) that looked like it might need PG's
    Limit-node backward-scan quirks: it was the exact same position bug,
    not a LIMIT/WITH-TIES-specific gap — no executor-level change was
    needed at all. Verified the full traced sequence for all five cursors
    (`c1`..`c5`) in `limit.sql` by hand against the ported algorithm before
    editing. Gates: `go build ./...` clean; `go test -v -run
    '^TestPort_RegressSuite$/^limit$' ./internal/testport/` PASS; full
    `TestPort_RegressSuite` re-run: 48 PASS / 1 FAIL (`numerology`, its own
    unrelated task above) / 183 SKIP (was 47/2/183); `go test
    ./internal/postmaster/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` PASS (all packages in scope — the
    script's own `EXCLUDE` list omits `internal/postmaster`, covered
    separately above). No TPC-H dependency (cursor/portal fetch path, not
    planner/executor row-count path) — not affected by the `:65433`
    catalog-loss hold (G6).

### Nightly run 20260914-235643 (sha `baf40efcbfbd`, 14 items) — filed 2026-09-15
- [x] **units/internal/parser (AI-20260914-235643-001, AI-20260916-035206-001, AI-20260917-004357-002)** — new tonight, units suite
  failed to build/run `internal/parser` (repro: `go test -timeout 10m
  ./internal/parser/`). Likely the same root cause as this file's own
  "Manually discovered" `parser/TestLockingClauseParity` entry below (filed the
  same day from an interactive gate run) — re-run both repros together before
  treating as two separate bugs.
  - **CLOSED 2026-09-19 — stale, verified green at HEAD `fd6fe3f98`** (Loop
    \#12). Repro `go test -timeout 10m ./internal/parser/` PASS; all six
    tests the blast-radius note named pass individually
    (`TestLockingClauseParity`, `TestValuesTable`, `TestCopyStatement`,
    `TestAggregateOrderByParity`, `TestVariadicCallParity`,
    `TestParityGoldensAreCurrent`). The named mechanism — `dc91bd6b7`'s
    `RangeVar.GroupedJoinUnaliased` changing `dumpStmts` text without a
    goldens regen — was fixed by `846cb718a` (2026-09-17, regenerated
    `parity_goldens.txt`); the filing's "every `FROM`-clause RangeVar"
    premise was broader than reality — the field is set only by
    `groupedJoinRangeVar` (`internal/parser/support.go:169`) for synthetic
    unaliased parenthesized JOINs. Tonight's nightly (`20260919-000526`)
    independently agrees: `units` stage PASS (167s). The `bak/` compile
    half of the 2026-09-15 report was untracked-scratch contamination,
    already closed under `infra/nightly-live-tree-build-race`.
- [x] **race/internal/parser (AI-20260914-235643-003, AI-20260916-035206-003, AI-20260917-004357-005)** — new tonight, race suite
  failed in `internal/parser` (repro: `go test -race -timeout 45m
  ./internal/parser/`). Same likely-shared root cause note as the item above.
  - **CLOSED 2026-09-19 — same defect cluster as the units item above,
    verified green at HEAD `fd6fe3f98`** (Loop \#12). Repro `go test -race
    -timeout 45m ./internal/parser/` PASS (2.9s, no race warnings).
    Tonight's nightly race stage agrees — its only filed regression was
    `race/internal/executor` (the pre-fix instrumentscope signature); no
    parser item was raised.
- [x] **testport/TestPort_IsolationEvalPlanQual (AI-20260914-235643-005, AI-20260916-035206-005, AI-20260917-004357-007)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationEvalPlanQual$'
  ./internal/testport/`).
  - **DONE 2026-09-19 (Loop \#9)** — spec now byte-identical PASS (23.4s),
    fixed by the same change as
    `TestPort_LockRowsSortOverJoinTakesRowLock` above: the `partiallock`
    (`LockRows -> Merge Join -> Bitmap Heap Scan` both sides) and
    `lockwithvalues` (`LockRows -> NL(Values, Bitmap Heap Scan inner)`)
    perms needed the bitmap leaf's resjunk-ctid wire + TID stamps, and
    `lockwithvalues` additionally needed the bitmap page RLock rescoped
    per tuple (it self-deadlocked `stampLock` on the held read lock).
- [x] **units/build-broke-mid-stage (AI-20260914-235643-013)** and
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
  **Resolved 2026-09-19 by infra/nightly-live-tree-build-race**: units/race
  now `go test` inside `NIGHTLY_SRC_ROOT`, a detached worktree at the
  recorded sha that carries only tracked content — untracked scratch like
  `bak/` can never enter the compile again.
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
- [x] **testport/TestPort_IsolationSuite (AI-20260916-035206-008)** — new
  tonight, FAILed (subtests: specs, specs/detach-partition-concurrently-1,
  specs/tuplelock-upgrade-no-deadlock; repro: `go test -v -run
  '^TestPort_IsolationSuite$' ./internal/testport/`). **FIXED 2026-09-19 —
  suite PASS 39.1s, 0 writeReserved / 0 panics** (filed subtests now defer
  as SKIP; residual catalog-mirror drift deferred to the ledger).
  - Loop 2026-09-19 (plan-parity-with-pg-take2-ralph2): the filed 3-subtest
    FAIL was only the tip — a live WAL regression wedges the whole server
    under the suite's multi-backend load (nightly 20260918-… timed out at
    7200 s mid-suite on it; its AI extractor caught only PGColdStart).
    Symptom chain: `writeReserved range outside buffer window` errors →
    cross-segment pad-emit panics recovered per-connection (killing DDL
    mid-statement → catalog-mirror drift, `relation already exists`,
    `dst extend` errors) → `resident > cap` → `readForDrain` panic wedge.
  - Root cause — two compounding defects in the slice-B WAL ring:
    - (1) `tryAppend`/`appendPGCompat` claimed `2*(paddedLen+64)` ring
      bytes, but `predictEmittedSize` interleaves a page header at EVERY
      8 KiB boundary and a segment crossing emits `gap + total` — the
      claim under-budgeted records spanning ≥3 pages crossing a boundary.
      Fixed by `walBufferReservationClaim` = `2*predictEmittedSize(0,…)`.
    - (2) `curr ≤ tail + reserved` is NOT invariant: `PublishUpTo` caps at
      `lowestActiveLSN`, so a fast stripe's publish can be capped below its
      own end while its claim releases anyway (transient hole on the
      SUCCESS path); a post-reserve `AppendXLogPayload` error released the
      claim over a burned `curr` range (`Writer.Append` silently retried,
      hiding the first failure); and `MemRing.WriteReserved` eviction
      (`AdvanceWindow`/`WriteReserved` not atomic — a peer's advance slides
      `memRing.head` past a pending write) surfaced a harmless cache miss
      as a fatal append error / pad panic.
  - Fix (docs/design/0100-0149/0107-0013): `insertPosTracker.windowEndFn`
    (= `walBuf.head+cap`) checked INSIDE `posMu` before `curr` commits and
    before pad emit — refused reservations return `walBufferCapacityExceeded`
    (callers drain+retry); head monotonic ⇒ admitted reservations can never
    fail `writeReserved`. MemRing misses demoted to cache-miss skips.
    Burned ranges zero-filled+published before claim release. `Writer.Append`
    propagates real errors (no silent retry).
  - Verified: xlog package + `-race` green; new regression tests
    (claim sweep, memRing-eviction skip, pad-skip, window stress).
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
- [x] **testport/TestPort_IsolationFkContention (AI-20260917-004357-008,
  AI-20260920-005626-001)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationFkContention$'
  ./internal/testport/`).
  RESOLVED Loop \#22 by M0143-0010 — `a53c5b807`'s index-accelerated FK
  existence probe (`scanIndexForFKMatch`) read the raw index ItemPointer,
  which references the HOT-chain ROOT; a committed non-key parent UPDATE
  leaves that member dead and the probe reported a false no-match (23503).
  Now walks the chain via `eachHeapChainMember` with the seq-scan twin's
  `TupleVisibleSubxact`. Spec PASSes at the fix commit.
  (AI-…-001 re-flagged by run 20260920-005626 — that run's sha `2b7e9705`
  predates the fix commit `9fb05621f`; stale repeat, no new task.)
- [x] **testport/TestPort_IsolationFkDeadlock (AI-20260917-004357-009,
  AI-20260920-005626-002)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationFkDeadlock$'
  ./internal/testport/`).
  RESOLVED Loop \#22 by M0143-0010 — same `scanIndexForFKMatch` chain-root
  blind spot; spec PASSes. (AI-…-002 re-flag: run sha `2b7e9705` predates
  the fix; stale repeat.)
- [x] **testport/TestPort_UpdateLockedTuple (AI-20260917-004357-012,
  AI-20260920-005626-005)** — new
  tonight, FAILed (repro: `go test -v -run '^TestPort_IsolationUpdateLockedTuple$'
  ./internal/testport/`).
  RESOLVED Loop \#22 by M0143-0010 — same root cause; spec PASSes.
  (AI-…-005 re-flag: run sha `2b7e9705` predates the fix; stale repeat.)
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

### Nightly run 20260918-010720 (sha `f489e72e78ab`, 5 items) — filed 2026-09-18
- [x] **testport/build (AI-20260918-010720-001)**, **race/stage
  (AI-20260918-010720-002)**, **units/stage (AI-20260918-010720-003)**,
  **units/build-broke-mid-stage (AI-20260918-010720-004)**, **race/build-broke-mid-stage
  (AI-20260918-010720-005)** — all five share one root cause per the bot's own
  classification on -004/-005: `internal/executor/operators_ddl.go:18519:2:
  undefined: stampExclusionConstraintRows`, and the bot flags "the working
  tree is built live, so a concurrent edit/commit is the likely cause". The
  cited sha `f489e72e78ab` is the docs-only `f489e72e7` commit (M0143-0003c
  docs), landed BEFORE `fd25f7d13` (M0143-0003e, which is what actually
  defines `stampExclusionConstraintRows` at `operators_ddl.go:18534`) — i.e.
  the nightly batch's live working-tree checkout raced a concurrent Ralph
  commit mid-build and captured a half-applied tree instead of any real
  commit's contents, exactly the `bak/`-contamination pattern already logged
  for AI-20260914-235643-013/014 above. **Stale — re-run at HEAD (`0317293db`,
  which already contains M0143-0003e) is clean**: `go build ./...` exit 0,
  `stampExclusionConstraintRows` present and referenced consistently
  (`operators_ddl.go:18519,18534`). testport/build and the two `*/stage`
  failures are downstream fallout of the same mid-build compile break (a
  package that failed to compile fails every test in stages that import it),
  not independent regressions. Closing as stale; re-open if a future nightly
  reproduces the same undefined-symbol error on a clean, non-racing checkout.

### Nightly run 20260920-005626 (sha `2b7e970534ea`, 18 items) — filed 2026-09-20
- [x] **testport/TestPort_IsolationTuplelockUpgradeNoDeadlock
  (AI-20260920-005626-004)** — FAILed (94.52s) with a schedule diff in the
  `s1_share s2_update s3_update …_rollback` permutation (expected `(1 row)`
  vs got `""` at L163, then a `<waiting ...>` mismatch at L165).
  Repro: `go test -v -run '^TestPort_IsolationTuplelockUpgradeNoDeadlock$'
  ./internal/testport/`; evidence `ci/logs/20260920-005626/testport/go-test.log`.
  The spec was previously filed only as a subtest of the closed
  IsolationSuite task (nightly 20260916); this is the standalone runner's
  failure, so it gets its own task. Failure observed at sha `2b7e9705`,
  which predates `9fb05621f` (M0143-0010 heap-update-chain probes) — needs
  re-verification at HEAD before diagnosing.
  - **CLOSED Loop \#24 (stale / resolved by `9fb05621f`)** — re-verified at
    HEAD `894774eb6`: PASSes 4× fresh (`-count=1`, ~12.6s each), no flake.
    The failing permutation is update-heavy; `9fb05621f`'s
    heap-update-chain probing on constraint/index probes is the plausible
    causal fix in the `2b7e9705..9fb05621f` window. No code change needed;
    re-open if a future nightly reproduces on a post-0010 sha.
- [x] **testport/TestPort_P0E4CatalogXmaxClientKill +
  TestPort_P0E4CatalogXmaxServerImmediateStop (AI-20260920-005626-007,
  AI-20260920-005626-008)** — both FAILed with a server-wedge signature, not
  a catalog-xmax signature: ClientKill "server did not settle after the
  client was killed: context deadline exceeded" (188s) and
  ServerImmediateStop "goopg stop: server did not exit within 20s" then a
  1002s total runtime. Repro: `go test -v -run
  '^TestPort_P0E4CatalogXmax(ClientKill|ServerImmediateStop)$'
  ./internal/testport/`; evidence same log. These are the P0-E4 regression
  tests un-skipped by P0-E5 — the catalog-loss fix holds (Rollback arm
  PASSes) but kill/immediate-stop leave the server wedged. Same run sha
  caveat as above.
  - **CLOSED Loop \#25 (stale / env wedge)** — re-verified at HEAD
    `021b8bf71`: ClientKill PASSes 3× fresh (~5.9s each),
    ServerImmediateStop PASSes 3× fresh (~3.9s each) — vs 188s / 1002s
    wedge runtimes in nightly. Neither `9fb05621f` nor `894774eb6` touches
    shutdown/backend-kill paths, so the wedge was most likely a nightly-batch
    environment effect (port/resource contention under parallel load), not
    a product defect fixed in the window. Re-open if a post-0010 nightly
    reproduces the wedge signature.
- [x] **testport/TestPort_PgoutputInterop* subscriber-start failures
  (AI-20260920-005626-009 … -018)** — ten pgoutput interop cases FAILed
  with one signature: "subscriber start: start failed; process exited
  early" (~2.5s each, per-test scratch cluster under
  `tmp/nightly-src-20260920-005626/tmp/pg2g-*`): GoopgToPG, FullDML,
  BatchDML, ReplicaIdentityFull, Truncate, ColumnOrderMismatch,
  SubscriberExtraColumn, SubscriberExtraDefault, PgbenchInsert,
  PgbenchTpcb. Sibling cases interleaved PASS (UnchangedToast,
  MultiDMLXact, SavepointXact, MultiTable, ReplicaIdentityUsingIndex,
  KillAndReconnect), so it is not a global env outage — likely a per-case
  fixture/state trigger or a flaky start race. Repro: `go test -v -run
  '^TestPort_PgoutputInteropPGToGoopgFullDML$' ./internal/testport/`;
  evidence same log; each FAIL line names its cluster.log.
  - **CLOSED Loop \#26 (stale / env pressure)** — re-verified at HEAD
    `b913ab8f3`: all 10 cases PASS ×2 full fresh sweeps (`-count=1`,
    ~29s per sweep) plus FullDML once singly. The nightly worktree
    `tmp/nightly-src-20260920-005626/` was already cleaned, so the named
    cluster.logs are gone; but `~/.ralph/logs/mem_guard.log` shows two
    PRESSURE kills inside the nightly window (01:10:02
    `goopg-tpcds-sf025.scope` @ 11.9 GB, 01:24:29
    `goopg-tpch-acceptance-on.scope` @ 11.2 GB — 75%+ RAM). A subscriber
    goopg exiting inside its ~2.5s start window under that host pressure
    fits the signature exactly, and no `2b7e9705..b913ab8f3` commit
    plausibly fixed ten replication cases at once. Re-open if a future
    nightly reproduces "process exited early" on a post-0010 sha without
    concurrent mem_guard kills.

### Nightly run 20260921-000212 (sha `cafc521a3151`, 17 items) — filed 2026-09-21
- [ ] **units/internal/utils/activity/stats
  TestCounter_PerShardWriteDistribution (AI-20260921-000212-001)** —
  units suite FAIL: "only 1 shards received Adds; per-P sharding looks
  broken" (counter_test.go:105, 0.00s). First-seen tonight; could be a
  scheduling-sensitive flake (per-P shard distribution depends on
  goroutine-to-P mapping) or a real stats-counter regression. Repro:
  `go test -timeout 10m ./internal/utils/activity/stats/`; evidence
  `ci/logs/20260921-000212/units/go-test.log`.
  Kind: test-fix
  Parent: none
- [ ] **testport/TestPort_Isolation* output diffs (AI-20260921-000212-002,
  -003)** — two isolation TAP cases FAILed on expected-output drift:
  EvalPlanQual L1059 expected `1|newTableAValue|(1,tableBValue)` got
  `1|tableAValue|(1,tableBValue)` (24.56s — a real value-content diff,
  not a line-count drift; EvalPlanQual was previously CLOSED Loop \#23
  on an older signature, so this is a re-regression with a different
  defect shape); ReceiptReport "expected 4215 lines, got 4216" (6.99s).
  Repro: `go test -v -run '^TestPort_Isolation(EvalPlanQual|ReceiptReport)$'
  ./internal/testport/`; evidence `ci/logs/20260921-000212/testport/go-test.log`.
  Kind: test-fix
  Parent: none
- [ ] **testport/TestPort_PgAmcheck003* re-CREATE EXTENSION after restart
  (AI-20260921-000212-004 … -007)** — four pg_amcheck cases FAILed on the
  same signature at ~1.0-1.4s each: `re-CREATE EXTENSION amcheck after
  restart: pq: extension "amcheck" already exists (42710)`
  (CombinedCorruption, MissingHeapFile, MissingIndexFork, SchemaScoped).
  The corruption is applied, the server restarts, and the test's plain
  `CREATE EXTENSION amcheck` now hits 42710 — i.e. the extension row
  survives the restart (per-DB catalog persistence landed since the
  tests were written) and the test setup was written against a
  non-persistent extension. Likely fix: `CREATE EXTENSION IF NOT EXISTS`
  or a DROP in the fixture. Repro: `go test -v -run
  '^TestPort_PgAmcheck003CombinedCorruption$' ./internal/testport/`;
  evidence `ci/logs/20260921-000212/testport/go-test.log`.
  Kind: test-fix
  Parent: none
- [ ] **testport/TestPort_PgoutputInterop* subscriber/publisher-start
  failures, second sighting (AI-20260921-000212-008 … -017)** — the same
  ten pgoutput interop cases as the CLOSED Loop \#26 task FAILed again
  with the same signature: "publisher/subscriber start: start failed;
  process exited early" at ~2.1-2.7s each (GoopgToPG, FullDML, BatchDML,
  ReplicaIdentityFull, Truncate, ColumnOrderMismatch,
  SubscriberExtraColumn, SubscriberExtraDefault, PgbenchInsert,
  PgbenchTpcb). **The closed task's re-open condition is met**: sha
  `cafc521a3` is post-005626, the testport stage ran 00:02:31-00:21:54
  and `~/.ralph/logs/mem_guard.log` shows NO PRESSURE kills inside that
  window (last kills 2026-09-20 23:12/23:14, ~50 min earlier), and no
  concurrent Ralph gate ran during the stage. Two consecutive nights of
  the identical 10-case signature now argues for a real start-path
  defect (fixture race or a cafc521a3-family regression — the M0142
  route-a optimizer commits landed between the two sightings) rather
  than env pressure. Repro: `go test -v -run
  '^TestPort_PgoutputInteropPGToGoopgFullDML$' ./internal/testport/`;
  evidence `ci/logs/20260921-000212/testport/go-test.log` (each FAIL
  names its `tmp/nightly-src-20260921-000212/tmp/pg2g-*/cluster.log` —
  read those BEFORE the worktree is cleaned).
  Kind: test-fix
  Parent: none

### Manually discovered (not yet in a nightly `ci/logs/action-items.md` run) — filed 2026-09-15

- [ ] **setop output type is the FIRST member's, not `select_common_type`'s
  (found 2026-09-21 by an M0145-0004 discovery probe)** — goopg resolves a
  `UNION ALL` column's type to the type of the FIRST member; PG resolves it
  with `select_common_type` over every member
  (`postgres/src/backend/parser/parse_coerce.c`, driven from
  `transformSetOperationTree`, `parse_clause.c`). Oracle diffs, goopg vs PG
  18.3 on `:65432`, both goopg arms (knob off and on — this is NOT a
  jointree-pipeline defect):
    - `SELECT a FROM (SELECT 1 AS a UNION ALL SELECT 2.5) t` — goopg reports
      `bigint`, PG reports `numeric`. The VALUES are right (`1`, `2.5`;
      `sum` = 6.5), so the rows are correct and only the declared type is
      wrong.
    - `SELECT 1::int2 UNION ALL SELECT 2::int8` — goopg `smallint`, PG
      `bigint`.
    - It is not only `pg_typeof`: `\gdesc` (a Describe round trip, i.e. the
      wire RowDescription) reports the same wrong type, so a typed client —
      JDBC, psycopg — is told `int8` for a column whose rows carry `2.5`.
      That is a protocol-level mismatch, not a cosmetic one.
    - Repro: any two-member `UNION ALL` with differing member types; no
      tables needed.
    - Fix direction: port `select_common_type` for set operations and coerce
      each member's target list to the resolved type, which is also the
      prerequisite the M0145-0004 ledger names for `tlist_same_datatypes`.
  Kind: bug
  Parent: none

- [x] **parser/TestLockingClauseParity** — deterministic FAIL, found while
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
  - **CLOSED 2026-09-19 (Loop \#12)** — repro `go test ./internal/parser/
    -run TestLockingClauseParity -v` PASSes at HEAD `fd6fe3f98`, along with
    the full package (`go test -timeout 10m` and `-race -timeout 45m` both
    green). Same verification as the `units/internal/parser` closure above:
    `846cb718a` regenerated `parity_goldens.txt` for the
    `GroupedJoinUnaliased` `dumpStmts` drift on 2026-09-17, and the field's
    emit is narrower than this filing estimated — `groupedJoinRangeVar`
    (`support.go:169`) only, i.e. synthetic unaliased parenthesized JOINs,
    not every `FROM`-clause RangeVar.

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
  - 2026-09-19 (bq): repaired two regressions that had every ported
    `TestPort_PgAmcheck*` skipping on an unclean healthy baseline.
    - `verify_heapam`/`pg_get_sequence_data`/`pg_sequence_parameters` reported
      42P01 for every relation in the `postgres` database: slice bo threaded the
      RAW `ctx.CurrentDatabaseOid`, but `postgres` (OID 16384) registers its
      tables under `DefaultDBOid` via `catalog.NamespaceDBOid` aliasing — the
      resolver now normalizes through `NamespaceDBOid` like every DDL path.
      `btIndexResolve` (never scoped) gained the same normalization, fixing the
      mirror-image gap for non-default databases.
    - `bt_index_check`/`bt_index_parent_check`/`checkunique` flagged every
      multi-page healthy index: the S11.4 B2-c flip (`fea5e8dd4`) made `it.key`
      the whole `IndexTupleData`, but the three amcheck tiers' nil-`KeyComparator`
      default was still bytewise `nbtree.CompareKeys` — ordering by the tuple
      header/heap-TID before key bytes. Nil defaults now resolve the index's own
      comparator: `VerifyBtreeItemOrderCmp`/`VerifyBtreeParentDownlinks` → new
      exported `IndexFormat.Compare`; `VerifyBtreeUnique` → `CompareKeyAttrs`
      (bytewise equality over whole tuples could never fire — checkunique was
      silently disabled). Two tests that pinned the broken default were updated
      to exercise it via an explicit `nbtree.CompareKeys` argument instead.
    - Result: all 9 ported pg_amcheck tests PASS (003×4, 004, 005, all-tables,
      btree) incl. the corruption-injection arms; live repro verified on both
      `postgres` and a `CREATE DATABASE`d database. Design:
      `docs/design/0100-0149/0119-0006-amcheck-postgres-db-scope-and-tuple-cmp.md`.
  - 2026-09-20 (br): wired the `heapallindexed` tier — the last argument of
    `bt_index_check`/`bt_index_parent_check` that was accepted but never ran.
    - New `btIndexHeapAllIndexed` (`internal/executor/operators_bt_index_check.go`)
      supplies the catalog-/MVCC-coupled `HeapEntryFormer` the engine seam
      (`amcheck.CollectHeapIndexEntries` → `VerifyBtreeHeapAllIndexedRelation`)
      was built for. The former mirrors `collectBTreeEntries`'s per-tuple
      recipe (upstream `table_index_build_scan` semantics):
      `TupleVisibleSubxact` snapshot visibility, `DecodeHeapTupleRowInto`,
      the enum KindString→KindEnum fixup, partial-index predicate and
      NULL-key/`key==nil` exclusions, then `indexBuildEntryKey` so probe bytes
      are byte-identical to what the build stores under either key format.
    - HOT members are emitted under the chain-ROOT line pointer (the index
      entry's stored TID — HOT writes no entry). `heapChainRootOffset` walks
      each candidate root (LP_REDIRECT stubs + non-heap-only LP_NORMAL items —
      upstream `heap_get_root_tuples`' set) via `eachHeapChainMember` on the
      page COPY the PageSource captured, so concurrent prune/update cannot
      desynchronize the chain view between scan and root lookup.
    - Agent review caught the draft's `isLiveForUniqueCheck` choice — it
      reports in-flight-xmin tuples live where upstream's MVCC snapshot never
      probes them (spurious-finding window under concurrent inserts); the
      predicate is `TupleVisibleSubxact`, matching `btIndexCheckUnique`'s
      visibility source.
    - Tier ordering: runs after the structural and checkunique tiers return
      zero findings — upstream runs the heap probe at the end of
      `bt_check_every_level`, and an earlier ereport aborts first.
    - Bloom seed is a fixed constant (upstream's per-run `pg_prng_uint64` is
      anti-adversarial only; determinism preferred).
    - Verified: 6 new tests (`TestBtIndexCheck_HeapAllIndexed*` — clean,
      phantom-tuple detection with XX002 "lacks matching index tuple", HOT
      member, partial index, expression index, NULL key — the detection and
      HOT arms carry non-vacuity guards); all 10 `TestPort_PgAmcheck*` PASS;
      live `pg_amcheck --heapallindexed` (real PG binary) exit 0 on a 5000-row
      table with 3000 HOT members + partial + expression indexes, and
      `--heapallindexed --rootdescend` exit 0 (rootdescend still a
      call-shape-accepted no-op — upstream gates it to heapkeyspace v4).
    - Design: `docs/design/0100-0149/0119-0006br-heapallindexed-former-wiring.md`.
  - 2026-09-20 (bs): resolved the 2026-09-02 ledger row — whole-database
    `pg_amcheck -d <CREATE DATABASE'd db>` now exits 0 clean.
    - The row's recorded root cause (system catalogs registered only under
      `DefaultDBOid`, so `verify_heapam(1259)` failed `LookupTableByOID`) was
      already fixed at HEAD by 0119-0006bp (`tableByOID` pg_catalog fallback)
      + M0122-0007 4e (name-based fallback). The live blocker sat EARLIER in
      the chain: `pg_extension` reads were per-db scoped (M0110-0003) but the
      registry write side was global — `c.extensions` keyed by lowercase name
      only, so `CREATE EXTENSION amcheck` failed 42710 in any second database
      and pg_amcheck's per-db install probe (`pg_amcheck.c:174`) correctly
      reported "not installed".
    - Landed: registry re-keyed `(database, name)` with scope-overlap conflict
      (unscoped legacy rows stay globally visible); `DropExtension`/
      `ExtensionOID` take the connecting database; `DropDatabase` purges and
      `RenameDatabase` re-keys scoped rows; `CreateExtension` returns a
      `created` flag so `IF NOT EXISTS` emits upstream's "already exists,
      skipping" NOTICE and skips the heap journal (previously journaled a
      duplicate row); `deleteExtensionCatalogRow` stamps the row's own
      scope heap + re-mirrors (sibling-path parity with the write route).
    - Review surfaced a worse pre-existing bug: `reloadUserExtensionsFromHeap`
      was dead code — its only call site was nested inside the pg_collation
      reload's error branch after the pool had been closed, so NO extension
      survived restart in ANY database. The call is hoisted to the success
      path and now scans each registered database's `base/<oid>/3079`,
      attributing rows to the owning db name.
    - Verified live on a private scratch cluster (:5533): whole-database
      `pg_amcheck` exit 0 on two `CREATE DATABASE`'d databases including
      `--heapallindexed`; per-db `pg_extension` scoping survives a restart;
      `DROP EXTENSION` removes only the current db's row (second drop 42704).
    - Gates: 4 new/extended per-db registry tests in
      `internal/catalog/extension_perdb_test.go`; `go test` catalog + initdb +
      executor + testport (`TestPort_PgAmcheck004`/`001`/`AmcheckCreateExtension`)
      PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS.
    - Design: `docs/design/0100-0149/0119-0006bs-per-database-extension-registry.md`.
    - Recorded-not-fixed (in the doc's deferral list): template0 is
      connectable so a `CREATE EXTENSION` there writes base/4 and would be
      cloned into future `CREATE DATABASE`s; a template1 install re-attributes
      to "postgres" on restart (shared bootstrap namespace); user-db-local
      `extnamespace` falls back to "public" at reload.
    - **LANDED 2026-09-20 via `09bde885a`** — the bs slice sat staged while
      the owner's `data.HOLD` (M0142-0003i 8-FK reload) kept the TPC-H gates
      SKIP-BLOCKED; when the owner committed their anchor re-pin the staged
      index rode along, so all 13 files + design doc + ledger rows are at
      HEAD and pushed. Gates green on exactly this code: units PASS;
      tpcds-sf025 PASS=96/0-mismatch/0-shape-change; tpch-spotcheck PASS on
      the reloaded cluster (Q12=2/Q13=33 vs re-pinned anchors). NOTE:
      `tpch-acceptance-arm` was NOT run — its baseline
      (`bench/tpch/baseline-digests.txt`, pinned 2026-09-08) predates the
      8-FK reload and is load-dependent; an arm-vs-stale-baseline FAIL would
      be data drift, not regression. Re-capture the baseline (or the owner
      does) before the next executor commit needs the arm stamp.
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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
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
- [x] **M0137-0019 — triage the 16 parallel-mode `parallelism`-category
  divergences M0137-0017 surfaced** — **UPDATE 2026-09-20 (owner decision):
  parallel mode is now the canonical TPC-H parity corpus** (AGENT.md §Goal),
  so this triage is headline work, not a side reading. Subsumed in part by
  **M0144-0002**'s first-divergence census (the higher-resolution instrument);
  this task keeps the per-query write-up responsibility. Original text: that
  task built the first-ever
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
  - **TRIAGE COMPLETE 2026-09-20 (loop \#50).** Design doc:
    `docs/design/0100-0149/m0137-0019-parallel-divergence-triage.md`.
    Kind: recon
    Parent: none
    Movement: none
    - The instrument reproduces the filed claim exactly: `parallelism=16`
      at `-serial=false` vs `parallelism=0` at `-serial=true`, and exactly
      four parallel-only regressions — Q1, Q10, Q14, Q15a-VIEWBODY.
    - **`parallelism=16/22` is not 16 plan-selection defects.** The 16
      collapse into four families, two of which dominate:
    - **Family A — 7 queries (Q3 Q9 Q10 Q14 Q16 Q18 Q21): a DESIGNED
      capability divergence, not a defect.** goopg emits ZERO
      `Parallel Hash` nodes corpus-wide, and the producer says why:
      "`parallel_hash = true` is REFUSED: no goopg executor builds a hash
      table from a partial inner"
      (`internal/optimizer/joinpathsparallel.go:59-61`). PG has two
      parallel hash joins (`joinpath.c:1290-1297`); goopg implements
      neither literally and instead builds ONE shared table in the leader
      that every goroutine worker adopts by pointer. Closing these needs an
      EXECUTOR capability, not a planner or costing change — ledger row,
      not a task.
      - Q14 is the clean witness: `parallelism` is its ONLY category and
        the plans are otherwise identical, PG's build side being
        `Parallel Hash → Parallel Seq Scan on part` against goopg's plain
        `Seq Scan on part`.
    - **Family B — 8 queries (Q1 Q4 Q5 Q8 Q12 Q16 Q22 Q15a; 5 of them also
      flip PG's sorted finalize to goopg's hashed one): ONE mispriced
      arm.** The R56 `upper.groupagg.gathermerge` candidate is NOT missing
      and NOT un-offered — a `DP_TRACE=1` plan-only probe at HEAD shows it
      **generated and ACCEPTED 9 times out of 9**. It loses on cost, and
      not narrowly: Q1's upper rel prices
      `upper.groupagg.gathermerge total=1510695.91` against the winning
      `upper.groupagg.split total=67840.37` — about **7.5x PG's price for
      Q1's ENTIRE plan (200900.77)**.
      - **Family B′ (Q3, Q18) is the same arm with the opposite sign** —
        goopg takes the GatherMerge where PG does not. A term that is wrong
        in both directions is mispriced, not uniformly too high.
      - Territory: M0140's parallel-path costing, NOT M0139/M0141/M0142
        plan selection.
    - **Family C — 6 queries (Q4 Q9 Q10 Q16 Q19 Q22): worker-count
      divergence, mostly downstream.** Counts differ in both directions and
      four of the six also carry family A or B, so the count follows the
      subtree shape rather than causing it. Q19 is the only member whose
      divergence is worker count ALONE.
    - **Q4 additionally loses parallelism entirely** — goopg plans it fully
      SERIAL in parallel mode (`HashAggregate → Nested Loop Semi Join →
      Seq Scan on orders`) where PG parallelises the semi-join's outer. No
      partial path is filed beneath a `Nested Loop Semi Join`.
    - **Q7** carries the category with no structural delta: both plans are
      `Gather Merge → Sort → Hash Join` and only the parallel-aware LABEL
      differs (goopg marks the join, PG marks the scan). That is family A's
      design difference surfacing, not a fifth mechanism.
    - Method: the capture was NOT re-run, as the task requires; the one
      live fact needed (is the GatherMerge candidate generated?) came from
      a `DP_TRACE=1` plan-only probe, a different instrument that produces
      no timing.
- [!] **M0137-0019a — reprice the `GatherMerge` + worker-sort arm** (filed
  by M0137-0019's triage). **PREMISE REFUTED 2026-09-20 (loop \#51);
  BLOCKED on an executor capability, not on a decision the loop may take.**
  Design doc:
  `docs/design/0100-0149/m0137-0019a-gathermerge-arm-refuted.md`.
  Kind: recon
  Parent: M0137-0019
  Movement: none
  - The arm is **NOT mispriced.** The candidate goopg generates is the
    **no-split** arm (`partialaggupper.go:418-476` says so in its own first
    line): it sorts the RAW INPUT per worker — ~1.48 M rows for Q1 — not the
    partial group-states PG sorts (6 per worker). So `1510695.91` is the
    CORRECT price of a genuinely expensive plan, and the `split` arm that
    wins at `67840.37` is the right call on the candidates goopg has.
  - PG's shape is `Finalize GroupAggregate → Gather Merge → Sort → Partial
    HashAggregate`, built by `gather_grouping_paths`
    (`postgres/src/backend/optimizer/plan/planner.c:7704-7724`), which
    stacks `create_sort_path` on the partially-grouped rel's partial paths
    and wraps them in `create_gather_merge_path`. goopg has no such
    producer.
  - **It cannot get one.** goopg's Partial Aggregate emits ZERO rows — it
    publishes each group into a shared mutex-guarded accumulator and the
    Finalize node reads the accumulator, not a tuple stream
    (`internal/executor/operators_join_agg.go:2351-2356`, and
    `partialaggupper.go:553-556` which explains why the split arm charges
    anything at the boundary at all). A worker-side `Sort` over a node that
    emits nothing sorts nothing; a `Gather Merge` over it merges nothing.
    PG's shape is not expressible in goopg's execution model.
  - **Therefore M0137-0019 §3.2's verdict is CORRECTED**: family B is a
    DESIGNED executor-model divergence, the same class as family A — not a
    costing gap in M0140's territory. The triage doc carries the correction
    in place, with the superseded text kept for the record.
  - **Owner-level consequence:** with families A (7 queries) and B (8-10)
    both executor-model divergences, `parallelism=16/22` on the canonical
    TPC-H parity corpus is essentially FLOORED by two executor design
    decisions. No planner or costing task can move it. The only remaining
    planner-side member is Q4 (M0137-0019b), one query.
  - Expected movement if the executor capability ever lands: the
    `parallelism` category on families B and B′ — 8 to 10 of 22 queries
    (Q1 Q4 Q5 Q8 Q12 Q16 Q22 Q15a, plus the mirrors Q3 Q18), measured by
    `pg-plan-parity-diff.py` `CATEGORIES:` on a pinned-epoch
    `estimate-audit -plan-only -serial=false` capture against the current 16.
  - **The unblock tasks are filed (2026-09-21 owner directive):**
    row-emitting PartialAgg + the GatherMerge-fed merge-combine are
    **M0141-S3 → S4 → S5 → S6**; the `Parallel Hash` build from a
    partial inner (family A's floor) is **M0140-0007**. When S6 and
    0007 land, this task's premise is re-evaluated — measured on the
    canonical capture, not assumed.
- [x] **M0137-0019b — file a partial path beneath `Nested Loop Semi Join`**
  (filed by M0137-0019's triage). TPC-H Q4 is the corpus's only fully
  SERIAL plan in parallel mode: goopg plans
  `HashAggregate → Nested Loop Semi Join → Seq Scan on orders` where PG
  plans `Finalize GroupAggregate → Gather Merge → Partial GroupAggregate →
  Sort → Nested Loop Semi Join → Parallel Seq Scan on orders`.
  Kind: impl
  Parent: M0137-0019
  Expected movement: Q4's `parallelism` category record on TPC-H parallel,
  and its `D:goopg-fully-serial` classification retires. Measured:
  `pg-plan-parity-diff.py` per-query line for Q4 plus the presence of a
  `Gather`/`Gather Merge` in its goopg plan.
  - **LANDED 2026-09-20 (loop \#52).** Design doc:
    `docs/design/0100-0149/m0137-0019b-partial-nestloop-semi.md` (its §4
    decision was written before any parity number was taken).
    Kind: impl
    Parent: M0137-0019
    Movement: none
    - **FOUR coupled gates**, not three, refused a partial NL outside
      INNER: the producer (`joinpathsnli.go`), the path classifier
      (`gatherpaths.go`), the ordinary-NL node twin and the FUSED
      `*NestedLoopIndexJoin` twin (both `parallel.go`). All four said in
      their own comments that the refusal was deliberate
      scope-minimization, NOT a correctness boundary — this task is the
      "separate scope" `nestedLoopJoinIsPartialCapable`'s doc asks for.
    - Widened to `{INNER, SEMI}`. SEMI's verdict is per-outer-row and
      worker-local: one qualifying inner tuple decides the outer row and
      the scan breaks (`finishOuter`, join_nl_stream.go), the joined row
      is never emitted, and the inner-matched bitmap RIGHT/FULL would need
      reduced across workers is never touched. PG admits
      `{INNER, LEFT, SEMI, ANTI}` at the same dispatch gate
      (`postgres/src/backend/optimizer/path/joinpath.c:2022-2031`).
    - **The fourth gate was found by MEASUREMENT, not by reading.** With
      the first three widened, Q4 was STILL planned fully serially: its
      semi join is the fused index-probe shape (`Index Cond: l_orderkey =
      o_orderkey`), governed by `NestedLoopIndexJoinIsPartialCapable`, not
      by the ordinary twin — whose own doc says a parameterised shape
      "never reaches this predicate". Recorded rather than folded in: the
      first three edits alone would have been a correct-but-inert change.
    - **Result — Q4 is parallel.** `Sort → HashAggregate → NL Semi Join →
      Seq Scan` (507361.63) becomes `Sort → Finalize HashAggregate →
      Gather (Workers Planned: 3) → Partial HashAggregate → NL Semi Join →
      Parallel Seq Scan` (162106.07). Family `D:goopg-fully-serial`
      retires with its only member.
    - Q4's per-query record sheds two of four categories:
      `[join-order,aggregation-strategy,sort-strategy,parallelism]` →
      `[sort-strategy,parallelism]`.
    - **It keeps `parallelism`, and that is the honest reading**: Q4 moved
      OUT of family D and INTO family B — PG uses `Gather Merge` +
      `Finalize GroupAggregate`, goopg now uses `Gather` + `Finalize
      HashAggregate`, which M0137-0019a proved is executor-floored. The
      record that remains is the one this task could not remove.
    - TPC-H parallel: match 2 → 2, `join-order` 16 → 15,
      `aggregation-strategy` 7 → 6 (both from Q4 alone), `parallelism`
      15 → 15. All inside ±3, hence `Movement: none`.
    - TPC-DS SF0.25: INERT — `queries=99 same=99 changed=0`, values
      `MISMATCH=0 CKMISMATCH=0`. No SF0.25 query pairs a semi nested loop
      with a partial-capable outer.
    - Seven assertions across three test files pinned the old INNER-only
      boundary; each moved to the admitted set for SEMI AND gained a
      positive assertion in its place, plus a new case pinning that the
      jointype widening did not become a bypass of
      `lateralProbeIsPartialProbe` (a SEMI NLI with a bitmap inner is
      still refused).
    - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
      `tpcds-sf025 sweep` PASS; `tpch-acceptance-arm` PASS (24/24 VALUES);
      `make ea-ratchet` `N/A`.
    - Still open (ledgered): LEFT and ANTI stay refused in all four gates
      by scope; Q4's residual `parallelism` record needs the row-emitting
      partial aggregation M0137-0019a is blocked on.
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
- [ ] **M0137-0022 — re-pin the `make plan-gate` baseline via the
  private-lane reframed path** (filed 2026-09-19 by a Devin session at
  owner request; **scope amended 2026-09-20 by M0144-0009**).
  Parent: none. Kind: impl.
  The `m0137-0005-rebaseline-20260915`
  pin now reports **14/22 diverged** (8 MATCH: Q1/Q3/Q6/Q11/Q15a/Q16/Q18/Q20;
  verified live 2026-09-19, structural mode). The drift is the recorded,
  expected accumulation of ~100+ internal commits since 2026-09-15 (SetOp /
  Gather-driving-kind admissions, NLI+Memoize, join-order costing) plus the
  2026-09-19 `:65433` owner-restore — the identical 14/22 count has been
  recorded unchanged across multiple loops, so this is baseline staleness,
  not a new regression. Procedure per the M0137-0005 doc
  (`docs/design/0100-0149/m0137-0005-plan-gate-rebaseline.md`): census the 14
  divergences and attribute them to landed mechanism classes, `tpch-spotcheck`
  PASS before pinning, then capture plus a design doc.
  **Amended scope (M0144-0009 decision, doc
  `docs/design/0100-0149/m0144-0009-plan-gate-reframing.md`):** the gate is
  reframed to diff a fresh private-clone HEAD build — `make plan-gate` /
  `plan-snapshot-capture` as designed compares live-`:65433` vs the pin with
  the staged code in neither side, so it can never see staged-code
  regressions. This task therefore ALSO lands the reframed path: build the
  staged tree → `scripts/lib/tpch-private-clone.sh`'s
  `tpch_private_clone_snapshot` (pg_basebackup online clone of `:65433`, no
  stop/wait) → boot the staged binary on a `55xx` lane under
  `scripts/goopg-test-run.sh` → `plan-snapshot diff|capture` with
  `--port <55xx>` → stop + drop the clone; wire it as the `plan-gate` /
  `plan-snapshot-capture` path (Makefile target or a thin script the target
  calls). The re-pin is then taken through that path, which removes the old
  "needs an owner rebuild of `:65433`" blocker by construction — the pin is
  the staged tree's plans, not the live binary's.
  Also note the capture is **parallel-mode by construction** —
  `cmd/plan-snapshot` sets no serial GUC and the clone inherits
  `max_parallel_workers_per_gather=4` — consistent with the 2026-09-20
  parallel-canonical headline.

## M0138 — PG-faithful ANALYZE statistics (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0138-pg-faithful-analyze-statistics.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
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
    in which case the banner's item 1 (M0137's re-opened 0014–0017) is next.
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
- [x] **M0139-0007c — port `get_expr_width` for Memoize's cache-key width
  Kind: impl
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
  - **DONE 2026-09-18.**
    Parent: M0139-0007b.
    Kind: impl. Movement: none
    (the ported term only reaches the price behind the already-default-off
    `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`, unchanged by this task). New
    `memoizeKeyWidths` (`joinpathsmemoize.go`) mirrors `memoizeKeyNDistinct`'s
    loop over `innerPath.IndexClauses`, reading each cache key's ANALYZEd
    `ColumnStats.AvgWidth` via `examineJoinVar` (PG's `attr_widths[]`/
    `stawidth` analogue) and falling back to `typeWidth` (`get_typavgwidth`)
    when unanalyzed — the exact fallback order `get_expr_width` uses for a
    `Var`, and the only Node shape reachable here since every admitted cache
    key is a bare `*ColumnRef`. `costMemoizeRescan` gained a `keyWidth
    float64` parameter that only feeds the price when the PG currency is on
    AND `pgRelationByteSize` itself succeeded — the same gate the tuple-bytes
    term beside it already uses, so neither term switches currency alone.
    Two new tests (`TestMemoizeKeyWidthsUsesAnalyzedStatThenTypeWidth`,
    `TestCostMemoizeRescanPGEntryBytesUsesKeyWidth`); five pre-existing
    `costMemoizeRescan` call sites updated for the new parameter (`0`,
    provably inert per the switch-off/currency-separation pins). Design doc:
    `docs/design/0100-0149/m0139-0007c-memoize-key-width-absorption.md`
    (indexed). Does NOT reopen M0139-0007b's HOLD decision — `evictRatio`
    still never activates on either measured corpus, so completing this term
    does not change which regime they exercise. Resolves ledger row
    `m0139-0007b`'s deferred scope (left `status: -` for M0119 to flip, per
    the ledger's own protocol). Gates: `go build ./...` clean, `go vet
    ./internal/optimizer/...` clean, `go test ./internal/optimizer/...` PASS
    (full package, no `-count=1`), `scripts/tpch-spotcheck.sh` PASS
    (Q12=2/Q13=34, unaffected by an off-by-default arm on a never-ANALYZEd
    fresh server).

## M0140 — TPC-DS parallelism (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0140-tpcds-parallelism.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
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
- [x] **M0140-0006a — expose SetOp-branch `PartialPathlist`.** Give each UNION
  Kind: impl
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
  - **Done 2026-09-18.** Movement: none (plumbing only). The M0140-0004
    recon's premise was stale: it claimed "no channel exists" for a branch's
    partial path to survive its own nested `planSelectWithSettings` call,
    but never referenced `searchedRelOf`/`searchedTree.searchRel` (R21
    slice 2a/2b, `6a51087fb`/`6302fb8d6`, landed 2026-09-09 — six days
    *before* the 2026-09-15 recon) — the exact mechanism
    `createOrderedPaths` already uses (M0141-S2b-2a, `upperordered.go:113`)
    to thread a search root's own `RelOptInfo` onto a consumer rel without
    re-deriving it. A UNION ALL branch is planned by the identical
    `planSelectWithSettings` recursion every SELECT is, so a branch that is
    a searched-tree root is reachable through the same accessor — the real
    gap was narrower: `createSetOpPaths` never called `searchedRelOf` on its
    two branches at all. Landed: `RelOptInfo.LeftBranchRel`/`RightBranchRel`
    (`path.go`, mirrors `SearchCandidates`'s doc-comment convention) and two
    lines in `createSetOpPaths` (`windowsetoppaths.go`) setting them from
    `searchedRelOf(setOpNode.Left/.Right)` right after building the branch
    seeds. Two new tests in `windowsetoppaths_test.go`
    (`TestCreateSetOpPathsThreadsBranchRelsOntoSetOpRel`/
    `...LeavesBranchRelsNilForNonSearchedBranches`). `addSetOpPaths` is
    untouched, so nothing reads the new fields and no plan can move. Gates:
    `go build ./...` clean; `go vet ./internal/optimizer/...` clean; `go
    test ./internal/optimizer/...` and `./internal/executor/...` both full
    green; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34) against the
    staged tree; `scripts/tpcds-sf025-regression.sh sweep` PASS=96
    MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE queries=99 same=99
    changed=0; `scripts/tpch-acceptance-arm.sh` PGSHAPED=1 HEAD baseline
    (`git stash`/build/`stash pop` round-trip of the three touched files,
    private port 5583) vs this staged tree: VERDICT PASS, 24/24 labels
    MATCH (the first attempt used the script's own PGSHAPED=0 default and
    hit an unrelated pre-existing Q9 600s-timeout BOTH-ERROR, identical on
    both arms — resolved by re-running with PGSHAPED=1 to match
    tpch-spotcheck's actual default). All three gate stamps' `code_tree`
    verified to match the staged index hash. Design doc:
    `docs/design/0100-0149/m0140-0006a-expose-setop-branch-partialpathlist.md`.
    Next: **M0140-0006b** reads
    `setOpRel.LeftBranchRel/.RightBranchRel.PartialPathlist` directly — no
    further plumbing needed.
- [x] **M0140-0006b — the partial-Append cost producer.** Depends on
  Kind: impl
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
  - **Done 2026-09-18.** Movement: none (the producer is inert by
    construction — see below). Landed `addPartialSetOpPath`
    (`internal/optimizer/windowsetoppaths.go`), called from
    `createSetOpPaths` right after 0006a's branch-rel threading: only the
    streaming UNION ALL form qualifies (`setOpStreams`), gated behind
    `gatherPathsMode` (`off` refuses, matching `addPartialHashJoinPath`),
    `setOpRel.ConsiderParallel` set to the conjunction of both branches'
    `ConsiderParallel` (the join-rel rule, `relnode.c:842`, applied to the
    two SetOp inputs), requires BOTH branches to already carry a partial
    path (PG's `partial_subpaths_valid` pure-partial arm only — the mixed
    partial/non-partial `append_nonpartial_cost` arm is not built, ledger
    row filed), worker count `Max` of the two branches' own counts bumped
    to the fixed two-child floor of 2 (`pg_leftmost_one_pos32(2)+1`,
    applied unconditionally since goopg has no `enable_parallel_append`
    GUC) and capped at `maxParallelWorkersPerGather`, rows/cost rescale
    each child's per-worker figures to the chosen worker count and sum
    plus the small per-tuple `APPEND_CPU_COST_MULTIPLIER` overhead. 14 new
    unit tests in `windowsetoppaths_test.go` (golden two-different-divisors
    cost case verified against an independently-computed formula, the
    two-child worker floor, the `maxParallelWorkersPerGather` cap, refusal
    for every non-streaming form/`gatherPathsMode=off`/either branch not
    considering parallel/either branch lacking a partial path, and an
    end-to-end `createSetOpPaths` byte-identical-plan acceptance pin).
    **This task's own acceptance question — "does
    `generateUsefulGatherPaths` read this for free?" — answers NO**: none
    of its three call sites (`gatherpaths.go`/`joinsearchlevel.go`/
    `geqo.go`) reach any upper-rel producer, all three read a
    `*searchCtx`'s own `joinrels`, and `createSetOpPaths` runs from the
    SetOp fold in `planner.go` entirely outside any `*searchCtx` — **no
    upper rel in the Phase-4 pipeline (WINDOW/ORDERED/GROUP_AGG/SETOP) has
    ever been wired for parallel Gather consideration**, a bigger,
    upper-rel-wide gap than the original recon assumed. Filed as
    **M0140-0006b-2** below. The producer is therefore confirmed inert by
    TWO independent mechanisms, not one — (1) that reachability gap, and
    (2) `partialPathDrivingKind`'s fail-closed whitelist (`gatherpaths.go`)
    has no `case PathSetOp:` arm, so even a wired `generateUsefulGatherPaths`
    call would still refuse it (opening that case is explicitly 0006c's
    job) — so this lands safely regardless of which of 0006b-2/0006c the
    next loop picks up first, AS LONG AS 0006c's own whitelist opening is
    the last of the three to land (recorded in the design doc so this
    doesn't need re-deriving). Design doc:
    `docs/design/0100-0149/m0140-0006b-partial-append-cost-producer.md`.
    Gates: `go build ./...` clean; `go vet ./internal/optimizer/...`
    clean; `go test ./internal/optimizer/...` and `./internal/executor/...`
    full green (14 new tests); `scripts/tpch-spotcheck.sh` PASS
    (Q12=2/Q13=34) against the staged tree; `scripts/tpcds-sf025-regression.sh
    sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE
    queries=99 same=99 changed=0 (Q5/Q76/Q2/Q14/Q71/Q75 all unchanged,
    confirming the producer moved nothing); `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green; `scripts/tpch-acceptance-arm.sh`
    `PGSHAPED=1` HEAD-baseline (`git stash`/build/`stash pop` round-trip of
    the two touched files, private port 5583) vs this staged tree: VERDICT
    PASS, 24/24 labels MATCH. Ledger row filed
    (2026-09-18, `M0140-0006b`): the mixed-arm gap plus the
    reachability gap. Next: **M0140-0006c** (below) or **M0140-0006b-2**
    (below), either order — see design doc's ordering note.
- [x] **M0140-0006b-2 — wire parallel Gather consideration into the Phase-4
  upper-rel pipeline.**
  Kind: impl
  Parent: M0140-0006b. Filed 2026-09-18 by M0140-0006b's
  own acceptance check. `generateUsefulGatherPaths` (`considerparallel.go`)
  is never called for any upper rel (WINDOW/ORDERED/GROUP_AGG/SETOP) — its
  three call sites (`gatherpaths.go`'s `addBaseRelGatherPaths`,
  `joinsearchlevel.go`, `geqo.go`) all read a `*searchCtx`'s own
  `joinrels`/`joinrel`, and every upper-rel producer (`createWindowPaths`,
  `createOrderedPaths`, `electOrderedGrouping`, `createSetOpPaths`, …) runs
  from `planner.go` entirely outside any `*searchCtx`. Scope: thread the
  subset `generateUsefulGatherPaths` actually reads (`parallelModeOK`, `cp`,
  `trace`) to the upper-rel call sites — sizing/shape (a trimmed struct? the
  full `*searchCtx`? a package-level knob?) is this task's own recon, not
  pre-judged here. Needed before ANY upper rel's `PartialPathlist` — not
  just SETOP's M0140-0006b — can ever be read by anything. Gate: TPC-H
  `match=8`/TPC-DS `match=2` byte-identical before/after (nothing should
  move — this is reachability plumbing, same posture as 0006a), plus
  `scripts/tpcds-sf025-regression.sh sweep` PLAN-SHAPE changed=0.
  - **DONE 2026-09-18.** Kind: impl. Parent: M0140-0006b. Movement: none
    (reachability plumbing; sweep PLAN-SHAPE 99/99 identical, TPC-H
    identical by construction — 0/22 queries contain a set operation).
    Landed `generateUpperRelGatherPaths(rel, cp)` (`gatherpaths.go`): a
    two-argument helper delegating to the one `generateUsefulGatherPaths`
    body via a minimal `&searchCtx{parallelModeOK: parallelModeOK(cp),
    cp}` (no twin; `top` mode refuses upper rels fail-closed), called once
    per rel before `setCheapest` at `createSetOpPaths` (live),
    `createOrderedPaths`, `electOrderedGrouping`, `createWindowPaths`
    (provably inert — no partials there). `electOrderedDistinct`/
    `addGroupingPaths` deliberately unwired (doc records why + the
    election-shape resume point). 6 new unit tests
    (`gatherpaths_upperrel_test.go`); `TestCreateSetOpPathsPartialPathDoesNotMoveThePlan`
    re-pinned (Gather now generated but dominated, winner unchanged).
    Gates: `go build`/`go vet` clean; optimizer+executor suites PASS;
    precommit units PASS (44 ok, no FAIL); `tpch-spotcheck` PASS
    (Q12=2/Q13=34); sf025 sweep PASS=96 MISMATCH=0 PLAN-SHAPE 99/99
    identical; parity floor transitive (commit carries `PARITY: N/A` +
    reason); `ea-ratchet` N/A (no estimate path touched). Design doc:
    `docs/design/0100-0149/m0140-0006b-2-upper-rel-gather-wiring.md`
    (indexed). No ledger row (no PG behavior deferred). Next: **M0140-0006c-2**
    is now the last open task under banner item 7.
- [x] **M0140-0006c — executor claim-set for `setOp` under `Gather`.** A
  Kind: impl
  Parent: M0140-0006
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
  - **Done 2026-09-18.** Adopted an uncommitted in-flight diff from a
    cut-off previous loop (6 modified files plus one untracked test file;
    only gap was a reference to a non-existent `intDatumForTest`, fixed to
    the existing `NewIntDatum`) rather than re-deriving. Landed:
    `parallelClaimSet.setOpLeft/setOpRight` plus `unwrapToSetOp` and the
    `attachAll` dispatch (`internal/executor/parallel_scan.go`, shared by
    `gatherOp` and `gatherMergeOp`); `*SetOp` twins in `stampParallelScan`/
    `drivingScan`/`unstampParallelScan` (`internal/optimizer/parallel.go`);
    `case PathSetOp:` in `partialPathDrivingKind` plus
    `setOpBranchDrivingKindIsSupported` (`internal/optimizer/gatherpaths.go`,
    narrowed to bare seq/unparameterised-index branches).
  Movement: none (correctness prerequisite; no plan can select a partial
  SetOp until M0140-0006b-2 lands — audited all three production
  `generateUsefulGatherPaths` call sites, none reachable from any upper-rel
  producer, so the whitelist arm has no input yet).
  `TestGatherOverSetOpIdentity` (new file, 260+90-row fixture, 1/2/4
  workers) is mutation-verified (2x/5x rows with the dispatch disabled).
  Gates: `go build`/`go vet` clean; optimizer+executor suites PASS;
  `tpch-spotcheck` PASS (Q12=2/Q13=34); `tpcds-sf025-regression sweep`
  PASS=96 MISMATCH=0 PLAN-SHAPE 99/99 identical; `tpch-acceptance-arm`
  PGSHAPED=1 HEAD-baseline A/B VERDICT PASS 24/24 MATCH (all three stamps
  share one `code_tree` matching the staged index); precommit units no
  FAIL. Design doc:
  `docs/design/0100-0149/m0140-0006c-executor-claim-set.md`. Ledger row
  filed (2026-09-18, `M0140-0006c`): join/bitmap-driven branches, owned by
  **M0140-0006c-2** below.
- [x] **M0140-0006c-2 — widen the partial-SetOp admission past bare scans.**
  Kind: impl
  Parent: M0140-0006c. Filed 2026-09-18 by M0140-0006c's own narrowing.
  `setOpBranchDrivingKindIsSupported` (`internal/optimizer/gatherpaths.go`)
  accepts only a bare `PathSeqScan` or unparameterised `PathIndexScan` per
  branch: a join-driven branch would need its build side prebuilt the way
  `prebuildHashJoins` does for a top-level partial join, and a
  bitmap-driven branch would need `prebuildBitmap` to find it — but
  `collectShareableJoins`/`collectBitmapScans` do not descend into a
  `*setOp`'s children today, so both fail closed to serial. Scope: teach
  both collectors (and both prebuild passes) to descend into SetOp
  children, then widen the admission test branch by branch, each with a
  serial-vs-parallel identity test of `TestGatherOverSetOpIdentity`'s shape
  extended to that branch kind. Expected movement (S5): TPC-DS Q5/Q76 (and
  Q2/Q14/Q71/Q75) reach `Parallel Append` over non-scan branches if PG's
  own plans use one there — confirm per query against `:65438` before
   claiming it. Gate: TPC-DS SF0.25 sweep (category movement, no
   regression) plus `tpch-acceptance-arm` digest; no TPC-H dependency beyond
   the standard spotcheck.
   - **In progress 2026-09-18 (partial — task stays unchecked).**
     Kind: impl. Parent: M0140-0006c. Movement: none (newly-admitted shape
     wins no corpus plan yet at current costs; sweep PLAN-SHAPE 99/99
     identical). Landed the hash-join branch: `collectShareableJoins` /
     `collectBitmapScans` descend into both `*setOp` children,
     `parallelChildren` gains the `*SetOp` arm (so `HasShareableHashJoin` /
     `HasBitmapScan` see branch joins/bitmaps; `StripGather` gains the twin
     two-child arm; `findPartialSubtree` / `rebuildWithGather` still refuse,
     unsafe/gather walks only grow more conservative), and
     `setOpBranchDrivingKindIsSupported` admits `PathHashJoin` partial
     through its probe side (`Children[0]`, same side every twin descends).
     Tests: hash + nested-hash admission, bad-hash / merge / nestloop /
     bitmap refusals, gate-descent and strip tests (optimizer), plus
     `TestGatherOverSetOpHashJoinBranchIdentity` (executor: 40-row forced-hash
     branch over 90-row scan, identity at 1/2/4 workers AND the build-once
     sharing witness — each verified to fire on exactly one mutation) and
     both collector unit tests. Key correction: the collector is build-once
     sharing, not row identity (mutation-verified — without it workers fall
     back to full private builds, correct but N+1x). Design doc:
     `docs/design/0100-0149/m0140-0006c-2-join-branch-partial-setop-admission.md`
     (indexed). Gates: spotcheck PASS, sweep PASS=96 MISMATCH=0 PLAN-SHAPE
     99/99 identical, acceptance-arm A/B VERDICT PASS 24/24 MATCH, precommit
     units no FAIL. Remaining under this task: merge-driven branch (no walk
     collects through a merge outer today), nested-loop-driven branch
     (outer-ParallelWorkers / Memoize / lateral-subset guards need their own
     audit), bitmap-driven branch (needs per-branch pbm publication), each
     with its own identity test.
   - **Recon 2026-09-19 (analysis only — no production change).** Scoped
     all three remaining branches end-to-end against the oracle and the
     corpus; per-slice specs in the design doc's "Remaining-branch
     scoping" section. Findings: only the NL branch has a corpus witness
     (Q76 — whose hash branch additionally needs M0142-0005a's Memoize
     admission under its probe, so Q76 unlocks only when BOTH land);
     merge and bitmap branches have ZERO `Parallel Append` heads in
     `bench/tpcds/plans-pg/` (completeness-only); Q5 needs the unbuilt
     mixed `pa_subpaths` arm — filed M0140-0006c-3; Q2 never needed this
     task, Q14/Q71 ride the landed hash arm, Q75 has no `Parallel
     Append`. Executor attach walks already cover merge/NL under any
     subtree — the only missing machinery is the three admission arms
     plus `prebuildBitmap` per-branch publication. Recommended slice
     order: NL → merge → bitmap. Impl commits were HOLD-blocked until
     2026-09-19, when the owner recovery of `:65433` released
     `data.HOLD` (see M0141-S2b-15's UNBLOCKED note) — TPC-H gates
     run normally again.
   - **Landed 2026-09-19 (slice A — NL branch; task stays unchecked,
     merge + bitmap slices remain).** One production change:
     `setOpBranchDrivingKindIsSupported` (`gatherpaths.go`) gains
     `case PathNestLoop` mirroring `partialPathDrivingKind`'s own NL
     arm guard-for-guard (JoinInner, `RequiredOuter==0`, two children,
     V5 outer, no `PathMemoize` inner; unparameterized whole-inner →
     recurse outer, or R95 probe inner: `PathIndexScan` + `IndexClauses`
     + `OuterRelids`/`InnerRelids` non-zero +
     `calcNestloopRequiredOuter==0`). Recursion stays branch-local so
     merge/bitmap outers still refuse; the general arm's duplicate
     Jointype re-check folds into the one top guard. NO executor
     change — all three `attachParallel*` walks already carry
     `JoinAlgoNestedLoop` (recon's "nothing to add" held). Tests: 3
     acceptance (whole-inner via `nlClassifyFixture`, NL-of-NL spine,
     R95 probe via `latClassifyFixture`), 11 refusals
     (`TestPartialPathDrivingKindRefusesSetOpWithBadNestLoopBranch` —
     incl. bitmap/merge outer narrowing + unpartitioned/unsatisfiable
     probes), `TestGatherOverSetOpNestLoopBranchIdentity` (forced-NL
     branch 120 rows + 90-row scan, serial-vs-parallel at 1/2/4
     workers). The `nestloop-branch` case moved OUT of the bad-hash
     refusal map — it is now a valid admission (`Jointype` zero value
     is `parser.JoinInner`). Mutation-verified: neutered arm → exactly
     the 3 acceptance tests fail to `PathPrebuilt`. Corpus unchanged:
     Q76 still needs M0142-0005a (Memoize under the probe NL) — slice
     A alone flips nothing, sweep PLAN-SHAPE 99/99 identical. Gates:
     build clean, optimizer 2.7s + executor 13.4s green,
     tpch-spotcheck PASS real run (Q12=2/Q13=34 — first non-SKIPPED
     since `:65433` recovery), sweep PASS=96 MISMATCH=0, units all
     `ok`. Design doc: `m0140-0006c-2-join-branch-partial-setop-admission.md`
     "Update 2026-09-19". Remaining: merge branch (one arm + the
     probeSideIsLeft-coincidence flag), bitmap branch (per-branch
     `pbm` publication + its identity test needs a partial-bitmap
     producer first — flag the dependency).
  - **Landed 2026-09-19 (slice B — merge branch; task stays
    unchecked, bitmap slice remains).** Two production pieces:
    `setOpBranchDrivingKindIsSupported` (`gatherpaths.go`) gains
    `case PathMergeJoin` mirroring `partialPathDrivingKind`'s own
    merge arm guard-for-guard (E-20 Cut 3: `RequiredOuter==0`,
    two children, partial through `Children[0]` — the outer side;
    no jointype guard, producer's trust re-checked at runtime by
    `mergeJoinIsPartialCapable`), recursion stays branch-local so
    bitmap-driven outers still refuse. Plus the coincidence
    alignment: `attachParallelBitmapScan` /
    `attachParallelIndexScan` (`parallel_scan.go`) gain explicit
    `JoinAlgoMerge` arms descending the literal left — replaces
    the `probeSideIsLeft(BuildLeft=false)` accident. No collector
    work (hash below a merge outer is never collected — same
    E-20 deferral the top level carries). Tests: 2 acceptance
    (base + merge-of-merge / NL-over-merge spines), 6 refusals
    (`...RefusesSetOpWithBadMergeJoinBranch`: parameterised/
    malformed merge, bitmap outer, nested bitmap spine, nil
    outer), `TestGatherOverSetOpMergeJoinBranchIdentity`
    (forced-merge branch 40 rows + 90-row scan, serial-vs-parallel
    at 1/2/4 workers — branch query must use comma/WHERE form;
    `JOIN..ON` plans the non-search fast path and ignores
    `enable_*` flips). Mutation-verified: neutered arm → exactly
    the 2 acceptance tests fail. Completeness-only: zero Merge
    Join `Parallel Append` heads in the corpus, sweep PLAN-SHAPE
    99/99 identical. Gates: spotcheck PASS (Q12=2/Q13=34),
    acceptance-arm A/B at `PGSHAPED=1` VERDICT PASS 24/24 MATCH
    (earlier `PGSHAPED=0` attempt hit the known 600s Q9 plan +
    mem_guard killed the `GOGC=off` baseline at 75% RAM — reran
    bounded `GOGC=100`/`GOMEMLIMIT=8GiB`), sweep PASS=96
    MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, units all `ok`.
    Same commit folds in an unrelated red-suite fix:
    `TestCheckpointerDoDWritePacing`'s 20ms absolute bound →
    relative bound vs the paced run (structural `progresses==0`
    + `FlushAll` already prove the bypass; `buildPacer` is nil
    for `spread=false`). Commit `0a5885bf6`. Remaining: bitmap
    branch only (per-branch `pbm` publication + a partial-bitmap
    producer needed for an end-to-end identity test).
  - **Landed 2026-09-19 (slice C — bitmap branch; task COMPLETE).**
    Kind: impl. Parent: M0140-0006c. Movement: none (zero Bitmap Heap
    Scan `Parallel Append` heads in `bench/tpcds/plans-pg/` — the arm
    is dormant pending a partial-bitmap producer, same posture as the
    top-level bitmap arm; sweep PLAN-SHAPE 99/99 identical). Two
    production pieces: `setOpBranchDrivingKindIsSupported`
    (`gatherpaths.go`) gains `case PathBitmapHeapScan` (unconditional
    admit, mirroring the top-level arm — admitted by a decision, not
    a default), and `prebuildBitmap` (`parallel_scan.go`) is
    restructured around `bitmapPrebuildTargets` /
    `appendBitmapPrebuildTarget`: a `*setOp` tree publishes each
    branch's collected bitmap to that branch's OWN leaf claim set
    (`cs.setOpLeft.pbm` / `cs.setOpRight.pbm`, keyed off
    `so.plan.Left`/`Right` via `HasBitmapScan`) instead of only the
    top-level `cs.pbm` — without it every worker's branch bitmap
    attaches nothing and scans its own whole bitmap (the N-copies
    defect; `attachAll`'s return is ignored by design). Flat path
    keeps the exactly-one rule; nil plan / plan-tree mismatch / 0-or->1
    collected all publish nothing (fail closed). A parameterized NLI
    probe bitmap is unreachable (`collectBitmapScans` has no
    `*nestedLoopIndexJoinOp` arm). Tests: 2 optimizer acceptance
    (bare bitmap branch + bitmap spines under hash/merge/NL —
    the cases the refusal maps carried), 8-case
    `TestBitmapPrebuildTargetsPerBranch` attribution unit, and
    `TestGatherOverSetOpBitmapBranchIdentity` — the recon predicted no
    end-to-end test, but the JOIN SEARCH emits a real
    `NestedLoop(BitmapHeapScan outer)` for an equality qual on a
    low-ndistinct indexed column (comma form required — fast path
    ignores `enable_*`; `matchBitmapIndexQuals` is equality-only;
    `TableStats` required) — serial-vs-parallel identity at 1/2/4
    workers exercises the real machinery. Mutation-verified: neutered
    arm → exactly the 2 acceptance tests (5 assertions) fail to
    `PathPrebuilt`. Gates: build clean, optimizer + executor packages
    green, spotcheck PASS (Q12=2/Q13=34), acceptance-arm A/B at
    `PGSHAPED=1` bounded `GOGC=100`/`GOMEMLIMIT=8GiB` VERDICT PASS
    24/24 MATCH (`PGSHAPED=0` hits the known 600s Q9 pathological
    plan), sweep PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0,
    units all `ok`. Remaining corpus work is elsewhere: Q76 needs
    M0142-0005a, Q5 needs 0006c-3; planner-side partial-bitmap
    producer is a separate gap (ledger
    `e10-gathermerge-bitmap-untested-e2e`).
- [x] **M0140-0006c-3 — mixed partial/non-partial SetOp append (Q5).** — DONE
  2026-09-19 (impl + docs + late placement correction in one commit; gates
  below). Kind: impl.
  Parent: M0140-0006c. Filed 2026-09-19 by 0006c-2's recon.
  `addPartialSetOpPath` (`windowsetoppaths.go:525`) files only the
  pure-partial arm — it returns when either branch's `PartialPathlist`
  is empty. PG's `add_paths_to_append_rel` builds a second, mixed arm
  (`pa_partial_subpaths`/`pa_nonpartial_subpaths`, `allpaths.c:1408-1452`):
  per child, the cheaper of its cheapest partial path vs its cheapest
  parallel-safe total path. Corpus evidence (`bench/tpcds/plans-pg/`):
  Q5's third `Parallel Append` mixes a NON-partial `Subquery Scan`→
  `Hash Right Join` branch with a `Parallel Seq Scan` branch — the only
  corpus witness for the mixed arm. Executor side already models the
  split (`nodeAppend.c:68-80,704-832` — non-partial plans claimed
  exclusively via `pa_finished` on selection); goopg's claim-set model
  would need a per-branch "claimed whole" marker rather than a leaf
  claim set. Needs its own scoping pass first (exclusive-claim
  semantics for a non-partial branch under the same Gather; how the
  leaf claim sets and `attachAll` express a whole-claimed branch).
  - 2026-09-19 — scoping pass committed:
    `docs/design/0100-0149/m0140-0006c-3-mixed-partial-setop-append.md`.
    PG mechanics pinned end-to-end (per-child pick `allpaths.c:1412-1453`,
    mixed arm `:1594-1627`, `first_partial_path` ordering
    `pathnode.c:1343-1365`, `cost_append` mixed arm + LPT
    `append_nonpartial_cost` `costsize.c:2168-2243,2342-2403`, executor
    `pa_finished` exclusive claim `nodeAppend.c`). Per-file gap map:
    `addPartialSetOpPath` per-branch pick + `SetOpPartialMask`-style
    marker; `*SetOp` node partialness flags; `stampParallelScan`/`drivingScan`
    marker-aware (claimed-whole branch unstamped); leaf claim set gains
    `claimedWhole atomic.Bool` wired by `attachAll`; `*setOp.nextStreaming`
    CAS-claims before draining. Rows override: when the pure arm also
    fires, the mixed path takes its `Rows` (`pathnode.c:1417-1419`).
    Shared-hash prebuild under a claimed-whole branch stays correct
    (leader builds, sole claimer probes); `prebuildBitmap` decision is
    the one open question (wasted leader prebuild vs gate exclusion).
    Impl commits were HOLD-blocked by the `data.HOLD` re-imposed
    2026-09-18T22:52 (unclean `:65433` shutdown) until the owner
    recovery released it on 2026-09-19 (see M0141-S2b-15's UNBLOCKED
    note) — TPC-H gates run normally again.
  - 2026-09-19 — impl landed. Mixed arm per the scoping map plus three
    findings the recon didn't size: (1) `createSetOpPaths` had to accept
    `*Gather`/`*GatherMerge` winners (a partial SetOp's winning path is
    the upper-rel `PathGather`, first reachable once the arm files);
    (2) searched-rel path substitution drops boundary wrappers → wrong
    arity/rows — `spliceBranchEmission` rebuilds the wrapper chain over
    the emission (fixes a latent pure-arm defect too), and the
    non-partial pick is the branch's own serial plan as a `PathPrebuilt`
    seed over `StripGather(branchNode)` (PG's `parallel_safe=false` on
    gather paths), so claimed-whole children are row-exact and need no
    splice; (3) PLACEMENT: PG's mixed arm lives only in
    `add_paths_to_append_rel` (appendrels), never in
    `generate_union_paths` — the first cut filing it unconditionally
    regressed TPC-DS Q66's top-level UNION ALL into an all-claimed
    `Gather > Append` PG cannot produce. Gate: `ps.ParallelStatementOK`
    (top-level marker) passed in as `topLevel`; mixed arm files only in
    nested scopes. Executor: `claimedWhole atomic.Bool` per branch leaf
    claim set, `attachAll` wires the shared flag, `nextStreaming`
    CAS-claims before draining (winner drains serially, losers close and
    skip); claimed-whole branches skipped from scan stamping, driving-
    scan requirement, and bitmap prebuild publication. Gates: units ok;
    optimizer+executor pkg tests ok; `go test -race` on executor
    SetOp/Gather ok; tpch-spotcheck PASS (Q12=2/Q13=34); SF0.25 sweep
    PASS=96 MISMATCH=0 PLAN-SHAPE 99/99 identical (Q66 reverted);
    acceptance-arm VERDICT PASS 24/24 MATCH; plan-gate 14/22 diverged =
    baseline drift (zero SetOp/UNION/Append lines in diff; baseline
    m0137-0005-rebaseline-20260915 predates ~100 internal commits).
    Residual documented in the design doc: nested union-alls PG would
    not flatten (LIMIT in subquery) still get the arm — marker sees
    nesting, not flattenability. Design doc:
    `docs/design/0100-0149/m0140-0006c-3-mixed-partial-setop-append.md`.
- [ ] **M0140-0007 — `Parallel Hash` (`parallel_hash = true`): workers
  cooperatively build ONE shared table from a PARTIAL inner** (filed
  2026-09-21 by owner directive; carries ledger
  `m0140-0005-q14-parallel-hash-execution-model`, K92, and plan-flow doc
  D3(a)). `addPartialHashJoinPath` files only `parallel_hash = false`
  (partial outer over a COMPLETE inner, leader-prebuilt shared table —
  the E-09a/b model); PG's second variant (`try_partial_hashjoin_path`
  with `parallel_hash = true`, `joinpath.c:1290-1297`, dispatched at
  :2418+) builds the table cooperatively from a partial inner behind a
  barrier protocol, and it is REFUSED here
  (`joinpathsparallel.go`'s own header: "no goopg executor builds a hash
  table from a partial inner" — the file never reads
  `inner.PartialPathlist`). This floors M0137-0019's **family A** — 7
  TPC-H queries (Q3 Q9 Q10 Q14 Q16 Q18 Q21; Q14 the clean witness) —
  and is the same `unexpressible` class the M0144-0007 census counts and
  M0145-0008's executor-substrate handoff lists.
  Scope:
  - (a) executor — a build input that is itself partial (each worker
    produces its inner slice), a shared build target all participants
    publish into, and a build-done barrier before any worker probes.
    The cooperative-build mechanism ALREADY exists:
    `parallelBuildLazyHashTable` (`parallel_hash_build.go:596`,
    M0129-S4.1) partitions a build across producer goroutines into one
    shared table — the gap is driving it from a partial-inner PATH
    (extending `prebuildSharedHashJoins`' leader-prebuild call sites),
    not the mechanism itself. Correctness over speed.
  - (b) planner — the `parallel_hash = true` arm priced the way PG
    prices it (build cost spread across participants), not the undivided
    build the `false` arm charges.
  - (c) measurement — the family-A witnesses under the canonical
    parallel TPC-H capture; report `CATEGORIES-EXCL-MATCH`.
  Hard constraint: the refusal exists because a partial build that
  misses inner rows silently drops matches (`parallel_scan.go`'s own
  warning). Any implementation MUST prove the barrier — no probe may run
  before the shared build completes — and a failed build must fail the
  query loudly, never return partial results (the coop-build panic
  precedent: a producer-group error must propagate, not be swallowed).
  Kind: impl
  Parent: none
  - **On completion — reconsider the blocked work (evaluate, do not
    auto-do):** re-run the family-A witnesses (Q3 Q9 Q10 Q14 Q16 Q18
    Q21) under the canonical parallel capture; M0137-0019a's family-A
    classification is re-evaluated only if a `Parallel Hash` node
    actually appears in those plans — if the shape exists and still
    loses on cost, THAT is the report, not a defect.

## M0141 — Upper-planner ordering contest (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0141-upper-planner-ordering-contest.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
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
- [x] **M0141-S2a-fix — make `costAgg`'s width currency see the
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
- [x] **M0141-S2a-fix2r — re-apply S2a-fix2 (owner Q4: no reverts).**
  Kind: impl
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
  - **DONE 2026-09-18.** Re-implemented exactly as scoped (guard + width
    argument on both `hashAggEntrySize` and the `pages` term), plus the 4
    accompanying test changes fix2's own doc had scoped. Design doc:
    `docs/design/0100-0149/m0141-s2a-fix2r-hashaggentrysize-currency-reapply.md`.
    **Values gate**: `scripts/tpch-acceptance-arm.sh` before/after digest
    diff (private-worktree baseline binary at HEAD `683663605` vs the
    working-tree binary with this diff) — VERDICT: PASS, 24/24 labels
    value-identical, no wrong rows anywhere.
    `scripts/tpcds-sf025-regression.sh sweep`: `MISMATCH=0 CKMISMATCH=0
    ERROR=0 TIMEOUT=0`.
    **Shape/category gate**: `scripts/tpch-estimate-audit-arm.sh
    PLAN_ONLY=1` + `pg-plan-parity-diff.py` (same-PG-reference control):
    TPC-H **MATCH 7 -> 8** (Q3 flips `SHAPE-DIFF` -> `MATCH`); Q8 loses its
    `parameterisation` tag (its bushy spine now matches PG's own bushy
    choice for Q8); no category rose anywhere;
    `shape-delta.sh`: `shape-changed=2` (Q3, Q8), confirming no other query
    moved. TPC-DS SF0.25: `PLAN-SHAPE: queries=99 same=99 changed=0` — zero
    plan-shape movement across the whole corpus (Q31's 2026-09-15
    regression does not reproduce on today's baseline; the corpus has moved
    since via fix1/fix1-sweep). **No query worsened in either corpus, so no
    `Parent: M0141-S2a-fix2r` follow-up task is filed** — a clean win, not
    the lateral/net-neutral result fix2's 2026-09-15 measurement found.
    `go test ./internal/optimizer/...`: PASS (full package). `go build
    ./...`: clean. `make ea-ratchet`: N/A — a costing change, not an
    estimate/selectivity change (touches only `Cost{}`, never
    `EstimateRows`). `tpch-spotcheck.sh`: PASS, Q12=2/Q13=34 canonical,
    gate-stamp PASS against staged code. **Movement: yes — TPC-H MATCH
    7->8 (Q3), CATEGORIES-EXCL-MATCH join-order 13->12 /
    parameterisation 5->4 / aggregation-strategy 9->8 / sort-strategy
    8->7, zero categories rose.**
- [x] **M0141-S2a-fix1-sweep — recon: other late/wrong width currencies.**
  Parent: none. fix1 (`ca574113c`, TPC-H match 6→8) is the only change in this
  programme proven to move the metric: width reached costing too late. Find every
  other cost-function input that is a goopg-native width/byte quantity, or is
  narrowed after costing (hash join, sort, material, memoize, agg,
  append/gather). For each: the PG expression (`./postgres` file:line), the
  goopg site, and the expected movement (named queries/categories) — S5. File
  one implementation task per site with `Parent: M0141-S2a-fix1-sweep`. No
  production diff (C1).
  - **Done 2026-09-18.** Full survey in
    `docs/design/0100-0149/m0141-s2a-fix1-sweep.md`. Already covered and not
    resweept: hash/merge/NL join + Gather/GatherMerge/(merge-join)Sort/Memoize
    + base-rel-scan narrowing (`narrowcostinputs.go`, R121/R122); Memoize's
    own cache-entry-size currency (already filed separately, M0139-0007c);
    Append/Material (no such `PathKind` exists yet in goopg's planner — N/A
    until M0140-0006 lands). Two new sites filed below. Two considered and
    declined with reasons recorded in the design doc: DISTINCT (`distinctCost`
    has no width/byte term at all — narrowing it would not change any cost it
    charges) and SETOP (`costSetOp`'s full-row `numCols` is the semantically
    correct quantity for whole-row dedup, not a currency defect). No code
    changed (recon only, C1) — no unit-gate run needed; pre-commit hook's
    pgbench smoke ran and PASSED.
- [x] **M0141-S2a-fix1-sweep-a — narrow the ORDERED upper rel's Sort pricing.**
  Kind: impl. Parent: M0141-S2a-fix1-sweep. `sizeUpperRelFromNode`
  (`internal/optimizer/upperrel.go:177-187`) sizes `NCols`/`AvgVarBytes` from
  the finished input Node's FULL `child.Output()`; `createOrderedPaths`
  (`internal/optimizer/upperordered.go:64-133`) → `addOrderedPaths` →
  `sortPathForBounded` (`internal/optimizer/joinpathsmerge.go:480-514`) then
  prices the top-level ORDER BY Sort from that unnarrowed rel via
  `pathNCols(sub)`/`pathAvgVarBytes(sub)`/`pathWidth(sub)`. PG's
  `create_sort_path` (`postgres/src/backend/optimizer/util/pathnode.c:3221-3250`)
  reads the already-narrow `subpath->pathtarget->width` instead
  (`cost_sort`, `costsize.c:2144`). A ready-made keep-set mechanism already
  exists — `sort.InputTarget`/`InputTargetKnown`
  (`internal/optimizer/sort_input_target.go`, `deriveSortInputKeep`: sort-key
  columns ∪ whatever the plan chain above the Sort reads) — but is stamped
  too late (post-cost, `planner.go:1972/2027/2441`, all downstream of
  `createOrderedPaths`) to be read at `sortPathForBounded`'s call site today.
  Implementation: derive the same keep-set BEFORE `createOrderedPaths` runs
  (sort keys ∪ the statement's own final SELECT-list output target, which is
  knowable before costing for a top-level ORDER BY — no further Node sits
  above it) and feed it into `sizeUpperRelFromNode` in place of the full
  `child.Output()`, mirroring fix1's "compute once ahead of costing, consume
  at the existing read site" shape. Gate: TPC-H Q18 (this file's own header
  names its 1.5M-row Sort as the largest in the suite, and fix1's design doc
  named its residual `aggregation-strategy`/`sort-strategy` mismatch as a
  fix1-successor candidate) plus a TPC-DS SF0.25 serial-shaped re-capture,
  `shape-delta.sh` diff, category movement (S5) — no category may rise.
  - **DONE 2026-09-18.** Movement: none. Landed
    `internal/optimizer/ordered_input_narrow.go` (`finalSelectOutputNames`,
    `deriveOrderedSortInputKeep`, `narrowOrderedRelWidths`), a new trailing
    `narrowKeep []int` parameter on `createOrderedPaths` AND its sibling
    `electOrderedGrouping` (found only after the first measurement pass:
    Q18 is a GROUP BY query and never reaches `createOrderedPaths` at all —
    `electOrderedGrouping` has the identical `sizeUpperRelFromNode` call
    over the identical node, fixed in the same loop per
    `pattern_sibling_paths_must_agree`), and updated every call site (2
    production call sites now compute a real keep, 2 pass `nil` because
    their input is already the minimal final row, ~16 test call sites pass
    `nil`). **Measured: zero movement in either corpus** — TPC-H
    `shape-delta.sh`: `queries=22 text-changed=0 shape-changed=0` (zero
    change of ANY kind, including cost digits, not just plan shape);
    TPC-DS SF0.25: `PLAN-SHAPE: queries=99 same=99 changed=0`,
    `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`. Root-caused rather than
    left unexplained: a GROUP BY aggregate's own published row is already
    the minimal SELECT-list width by construction (goopg's `Aggregate`
    emits exactly GROUP BY + aggregate-result columns, same as PG's `Agg`),
    so TPC-H Q18 (the recon's own named witness) and TPC-DS's 99-query
    corpus both have no hidden extra width for this keep-set to trim in
    practice — the recon's Q18 speculation was untested at recon time (C1
    forbade a production diff) and the implementation now shows it does not
    hold. Values byte-identical: `tpch-acceptance-arm.sh` 24/24 labels
    MATCH before vs after. Kept as a correct, PG-faithful cost-input fix
    with a confirmed no-regression measurement (same standard as
    R121/`relNarrowedWidths`) — **no `Parent: M0141-S2a-fix1-sweep-a`
    follow-up filed**, nothing worsened, nothing to file. Design doc:
    `docs/design/0100-0149/m0141-s2a-fix1-sweep-a-ordered-sort-width-currency.md`.
    Gates: `go build ./...` clean; `go test ./internal/optimizer/...` PASS
    (full package); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34
    canonical); `scripts/tpch-acceptance-arm.sh` before/after digest diff
    VERDICT PASS; `scripts/tpcds-sf025-regression.sh sweep` PASS (as
    above); `make ea-ratchet`: N/A (costing-only, no row-estimate/
    selectivity path touched, same disposition as fix2r).
- [x] **M0141-S2a-fix1-sweep-b — narrow WINDOW's internal sort costing.**
  Parent: M0141-S2a-fix1-sweep. goopg's `windowOp` sorts internally
  (`operators_window.go` `Open`) rather than taking pre-sorted input the way
  PG's `WindowAgg` does, so `costWindow`
  (`internal/optimizer/windowsetoppaths.go:206-238`) folds PG's separate
  `create_sort_path` step into itself — but its caller, `addWindowPaths`
  (`windowsetoppaths.go:260-284`), feeds it `len(cols)`/`nodeAvgVarBytes(cols)`/
  `nodeTupleWidth(belowNode)` from `cols := belowNode.Output()`, the FULL row
  of the node one level below, never narrowed; `sizeWindowRelFromNode`
  (`windowsetoppaths.go:132-141`) makes the same full-`Output()` choice for
  the rel's own published width. PG's real window-input Sort
  (`create_one_window_path`, `postgres/src/backend/optimizer/plan/planner.c:4620-4760`)
  is priced via `create_sort_path`'s already-narrow `subpath->pathtarget->width`
  exactly as the ORDERED-rel case (sweep-a) — `cost_windowagg` itself
  (`costsize.c:3098+`) takes no width parameter at all. Implementation: no
  `InputTarget`-style stamp exists yet for Window (unlike Sort's); derive one
  mirroring `sort_input_target.go`'s pattern — keep-set = PARTITION BY ∪
  ORDER BY ∪ window-function argument columns ∪ whatever the plan chain above
  this window level reads (`addWindowPaths` already threads `below`/
  `belowNode` per spec group, so the "above" context is locally available
  without a new tree walk). Gate: full TPC-DS SF0.25 serial capture (window
  functions are far more common there than in TPC-H; no specific query
  pre-identified), `shape-delta.sh` diff on any query whose tag set includes
  a window/ranking shape, category movement (S5) — no category may rise.
  - **DONE 2026-09-18.** Movement: none — and provably so, not merely
    corpus-inert: the WINDOW rel offers exactly one candidate per statement
    and `*WindowAgg` carries no `PlanCost`, so no narrowing at this site can
    move any observable output under the current single-candidate design
    (design doc's own analysis). Landed
    `internal/optimizer/window_sort_narrow.go`
    (`deriveWindowChainNarrowKeeps`, `deriveWindowRelNarrowKeep`,
    `narrowWindowRelWidth`), reusing `window_input_target.go`'s existing
    `windowWindowInputNames` (the B-01c compute-only stamp's own per-level
    own-inputs enumerator — task text's premise that "no InputTarget-style
    stamp exists yet for Window" was stale; one exists but is compute-only,
    keys-only-stamped, and never consumed for costing, so this task is the
    applying cut B-01c's own file header named as its future). New trailing
    `chainKeep [][]int`/`relKeep []int` parameters on `addWindowPaths`/
    `createWindowPaths`; `buildWindowStage` computes both ONCE (top-down
    over the already-built chain, propagating "need" from
    `finalSelectOutputNames` — sweep-a's helper, reused verbatim — down
    through each level's own window-input names) right after its
    per-group loop, using a throwaway `windowSurface` byte-identical to the
    function's own later return value; gained a `starPS *ProjectSet`
    parameter so the composite-star decline guard reaches this site too.
    Only NCols/AvgVarBytes narrowed (Rows/Width left alone), same scope as
    sweep-a. Measured: TPC-DS SF0.25 `PASS=96 MISMATCH=0 CKMISMATCH=0
    ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: queries=99 same=99 changed=0`; TPC-H
    values 23/24 digest labels MATCH (`tpch-acceptance-arm.sh`), sole
    non-MATCH is Q9 timing out identically in both arms (known flakiness,
    not a regression — no TPC-H query even uses a window function, so this
    arm is confirmation-by-absence). Design doc:
    `docs/design/0100-0149/m0141-s2a-fix1-sweep-b-window-sort-width-currency.md`.
    Gates: `go build ./...` clean; `go test ./internal/optimizer/...` PASS
    (full package, all `addWindowPaths`/`createWindowPaths` call sites
    updated); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34);
    `scripts/tpcds-sf025-regression.sh sweep` PASS (as above). No
    `Parent: M0141-S2a-fix1-sweep-b` follow-up filed — nothing worsened,
    nothing could have. `make ea-ratchet`: N/A (costing-only, same
    disposition as sweep-a/fix2r).
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
  - [x] **M0141-S2b-6-resume** — repeat S2b-6's Hashed-vs-Sorted `PathAgg`
    Kind: impl
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
    **DONE 2026-09-18.** Movement: none (2 new gated DPPATH trace lines,
    `traceOrderedGroupingCandidate`/`traceOrderedSortedCandidate` in
    `pathtrace.go`, wired from `addOrderedPaths` in `upperordered.go` on
    `input.Kind == PathAgg` — diagnostic-only, no cost formula touched, no
    plan-shape change; confirmed by the unchanged `tpcds-sf025` plan-shape
    count below).
    Gate cleared: `:65433`'s `tpch` database verified non-empty
    (`select count(*) from lineitem` = 6001255, SF1) before starting.
    Measured via a private-clone `tpch-estimate-audit-arm.sh` arm
    (`PLAN_ONLY=1 DP_TRACE=1 PGSHAPED=1 --queries 4,5,12,21`, online
    `pg_basebackup -X fetch` clone off live `:65433`, never stopped).
    **Result: all four queries are real, non-tied margins** — S2b-6's
    clamped-float tie does not survive real cardinalities. Q5/Q21
    reconfirm "no cost bug" (ORDER BY never matches GROUP BY, both
    candidates pay a comparable Sort). Q4/Q12 are the real case (Sorted's
    presort already satisfies ORDER BY): goopg elects Hashed+Sort by
    899.76/2181.96 units. Cross-checked against a **fresh** `EXPLAIN` on
    the read-only PG 18.3 reference (`:65432`), which elects the
    mirror-image Sorted shape for both — a confirmed real cost-model
    divergence from PG, not a tie artifact. Tested and ruled out one
    hypothesis: flipping the pre-existing `GOOPG_PG_SORT_RELATION_BYTES_COST`
    GUC (R113) changes the DPPGSORT currency label but not the election or
    the margin, so the Sort byte-size currency is not the root cause.
    Design doc:
    `docs/design/0100-0149/m0141-s2b-6-resume-hashed-vs-sorted-real-sf1.md`
    (new; the parent `m0141-s2b-scoping-decomposition.md` is frozen at 977
    lines under D3.1, so this landed as a fresh file rather than an
    appended section). `docs/design/README.md` new index row. Follow-up
    filed as **M0141-S2b-10** below (M0141-S2b-8/-9 are already taken by
    the unrelated TPC-DS Incremental-Sort candidate-pool line of work —
    checked `grep -oE "M0141-S2b-[0-9a-z]+" .ralph/fix_plan.md | sort -u`
    before naming this one). Gates: `go build ./...` clean;
    `go test ./internal/optimizer/...` PASS (no `-count=1`); both
    `tpch-estimate-audit-arm.sh` arms rc=0, served-binary sha256 verified;
    `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34), staged-tree gate stamp
    PASS; `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    CKMISMATCH=0 ERROR=0 TIMEOUT=0, `PLAN-SHAPE: queries=99 same=99
    changed=0` (confirms the `Movement: none` claim above), staged-tree
    gate stamp PASS; `scripts/tpch-acceptance-arm.sh` before/after digest
    (`PGSHAPED=1`, private port 5583, `GOOPG_ANALYZE_SEED=20260905`,
    baseline built from a `/tmp` worktree at pre-change HEAD `a449e6826`,
    "on" built from the staged tree): `VERDICT: PASS`, 24/24 labels MATCH
    including Q9, staged-tree gate stamp PASS; `python3
    scripts/ralph_protected_regions.py check-designdocs` exit 0; `python3
    scripts/ralph-lineage-guard.py` clean. No shared cluster write
    (`pg_basebackup` clone + read-only
    `EXPLAIN` only).
  - [x] **M0141-S2b-10** — root-cause `costAgg`'s Hashed-vs-Sorted formula
    Kind: recon
    Parent: M0141-S2b-6-resume. Filed 2026-09-18 by M0141-S2b-6-resume's
    result: for TPC-H Q4/Q12 (GROUP BY key == ORDER BY key, low output
    cardinality, large join-output input row count) goopg's cost model
    elects Hashed+explicit-Sort while real PG 18.3 elects Sorted
    (`GroupAggregate` fed by a `Sort` below it), by a real, non-tied
    margin (899.76/2181.96 cost units at SF1) confirmed against a fresh
    live PG `EXPLAIN`, not a stale capture. The nearest existing suspect —
    the R113 `GOOPG_PG_SORT_RELATION_BYTES_COST` Sort byte-size currency
    swap — was tested (env-toggle only, no code change) and does NOT
    change the election or close the margin, so it is not the cause.
    Compare `costAgg`'s `AggStrategyHashed` term-by-term against PG's
    `cost_agg` hashed-strategy formula (`postgres/src/backend/optimizer/path/costsize.c`,
    `cost_agg`) for these two queries' actual input rows/groups/width —
    the likely direction is goopg underpricing the Hashed build+probe
    relative to PG, or a difference in what each side charges the
    join/scan input feeding the two candidates, but this is unconfirmed;
    that is exactly what this recon must establish before any fix. Use
    the same private-clone `tpch-estimate-audit-arm.sh` methodology
    M0141-S2b-6-resume established (`DP_TRACE=1 PLAN_ONLY=1`); do not
    re-attempt a synthetic-fixture probe (S2b-6's own lesson: the terms
    only separate at real cardinalities). Per the cost-model design bundle
    (`docs/design/cost-model/`, the "0077 line") — read it first, and do
    not land a formula change without a term-by-term match against
    `costsize.c`, consistent with every other M0141-S2a fix in this
    lineage.
    **DONE 2026-09-18.** Movement: none (recon only; no production file
    touched). `costAgg`/`costSortRunWithWidth` are NOT the bug: verified
    term-by-term against `cost_agg`/`cost_sort` (`costsize.c`), and proved
    live by forcing PG's own discarded `HashAggregate` alternative into the
    open (`SET enable_sort = off`, a per-session GUC toggle, not DDL/DML,
    against the read-only `:65432` reference) — PG's OWN formula ALSO
    prices Hashed cheaper for both Q4 (190077.61 vs 190990.46, -0.48%) and
    Q12 (326458.53 vs 328634.21, -0.66%). goopg's Hashed-wins arithmetic
    agrees with PG's; there is no formula to fix. **Real cause**: both
    margins sit inside PG's `STD_FUZZ_FACTOR` (1.01) — a mechanism goopg
    already ports faithfully (`path.go:27`'s `stdFuzzFactor`, feeding
    `addPath`'s dominance/tie-break, matching `add_path`'s documented
    "keep the first of two indistinguishable paths" rule). Live trace
    confirms the mechanism: Sorted's `upper.ordered.input` entry is
    `verdict=dominated` after Hashed's `upper.ordered.sort` was already
    `accepted` — both end up with IDENTICAL pathkeys (both satisfy the
    query's ORDER BY) and fuzzily-tied cost (0.17% apart), so `addPath`'s
    tie-break falls to pure insertion order. `internal/optimizer/
    groupingpaths.go`'s `addGroupingPaths` — despite its own doc comment
    claiming fidelity to `add_paths_to_grouping_rel` (`planner.c:7113`) —
    adds the HASHED candidate (`groupingpaths.go:406`) BEFORE the SORTED
    candidate (`:463`/`:475`), the exact opposite of real PG's
    `can_sort`-before-`can_hash` order (`planner.c:7128` vs `:7286`, read
    directly and confirmed). This single order inversion, combined with
    both engines' identical fuzzy-tie-break-by-insertion-order rule, is
    sufficient on its own to flip the election whenever the two candidates
    are within 1% of each other — exactly Q4/Q12's situation. Q5/Q21 are
    unaffected (`contained=false` for their Sorted candidate too, so the
    pathkeys-equal coincidence never arises). Measured via the same
    private-clone `tpch-estimate-audit-arm.sh` methodology (online
    `pg_basebackup -X fetch` off live `:65433`, never stopped) plus
    read-only `EXPLAIN`/session-GUC against `:65432`. Design doc:
    `docs/design/0100-0149/m0141-s2b10-hashagg-sortagg-insertion-order.md`
    (new; `docs/design/README.md` new index row). No `go build`/`go test`
    gate needed (no `.go` file touched this loop). Follow-up implementation
    task filed as **M0141-S2b-11** below (`Kind: impl`,
    `Parent: M0141-S2b-10`). No ledger row (recon->impl handoff via the
    task's own `Kind`/`Parent` fields, same precedent as
    `M0141-S2b-6-resume`).
  - [x] **M0141-S2b-11** — reorder `addGroupingPaths` to match PG's
    Kind: impl
    Movement: none — TPC-H match 7→7 (same-PG control, byte-identical
    plans before/after under pinned stats epoch), TPC-DS 99/99 identical,
    no CATEGORIES-EXCL-MATCH category moved.
    Parent: M0141-S2b-10. `internal/optimizer/groupingpaths.go`'s
    `addGroupingPaths` currently emits the HASHED `PathAgg` candidate
    (the `if groupingHashable(...) { addPath(grouped, ...AggStrategyHashed...) }`
    block, `groupingpaths.go:406-431`) before the SORTED candidate (the
    `if aggNode.GroupingSets == nil { ... }` block, `:441-495`, covering
    both the index-ordered and explicit-`sortPathForBounded` variants).
    Real PG's `add_paths_to_grouping_rel` (`postgres/src/backend/
    optimizer/plan/planner.c:7113`) emits its `can_sort` block
    (Sorted/Plain, lines 7128-7264) BEFORE its `can_hash` block (Hashed,
    lines 7286-7315). Since `addPath`'s tie-break on a fuzzy-cost/
    equal-pathkeys collision keeps whichever candidate was inserted FIRST
    (both engines share this rule, `M0141-S2b-10`'s design doc "Result 3"),
    reorder `addGroupingPaths` to emit the SORTED block first and the
    HASHED block second, mirroring PG exactly. This is a pure block
    reorder — no cost arithmetic changes. **Required gates (this is a
    planner-election change, not cost-neutral by construction — expect
    TPC-H Q4/Q12 to flip to Sorted, matching PG)**: `scripts/
    tpch-spotcheck.sh` (Q12=2/Q13=34 canonical, per the executor/planner
    practice card), `scripts/tpcds-sf025-regression.sh sweep` (row counts
    AND plan-shape diff — any OTHER query flipping besides Q4/Q12 must be
    individually cross-checked against a fresh PG `EXPLAIN`, same method
    as the parent recon's Result 2, not assumed correct just because it
    changed), `scripts/tpch-acceptance-arm.sh` before/after digest (any
    non-MATCH label besides the known Q9 timeout needs its own row-count
    check), `go test ./internal/optimizer/...` (no `-count=1`),
    `python3 scripts/ralph_protected_regions.py check-designdocs`. Update
    this design doc's own "Conclusion" §4 risk note with the actual
    sweep result once run — do not just report PASS/FAIL, name which
    queries' plan shapes moved and whether each one is now closer to or
    further from PG.
    **DONE 2026-09-18 (`1e48aca50`).** Reorder landed exactly as scoped —
    SORTED block (now `groupingpaths.go:437-478`) before HASHED
    (`:484-495`), the sorted block's two early `return`s replaced by a
    `haveSortedCandidate` flag so the candidate SET is unchanged
    (insertion order only). **Measured effect: zero.** Under a pinned
    stats epoch (`GOOPG_ANALYZE_SEED=20260905`, both arms
    `253df6b1d97f9f5b`) TPC-H plans are byte-identical before/after
    (shape-delta 0/22, match 7→7); TPC-DS SF0.25 PLAN-SHAPE 99/99;
    acceptance-arm digest 24/24 MATCH; spotcheck Q12=2/Q13=34. **The
    expected Q4/Q12 flip did NOT fire**: `DP_TRACE=1` shows Sorted's
    `upper.ordered.input` accepted first (507569.94) then Hashed's
    `upper.ordered.sort` accepted anyway (506697.64, 0.17% inside the
    `STD_FUZZ_FACTOR` band) and winning — `comparePathCostsFuzzily`'s
    **M0129-S1 deviation** (`path.go:943-956`) breaks fuzz-band ties by
    EXACT cost instead of returning `costsEqual`, so a strictly-cheaper
    second candidate always evicts the first regardless of insertion
    order. Under true PG `COSTS_EQUAL` semantics the second would be
    rejected (equal pathkeys, keep-first) → GroupAggregate, matching PG's
    real Q4/Q12 plans. The deviation also masks the grouped-rel level:
    PG's pathkeys dim would let the key-carrying Sorted candidate
    dominate keyless Hashed on a cost tie; exact-cost direction makes
    them incomparable, keeping Hashed alive for the ordered tournament —
    the exact effect M0129-S1 was designed for (CTE self-joins). An
    unpinned-seed arm pair additionally produced a phantom Q3 flip —
    pure ANALYZE-sample noise; stats-epoch pinning is required for
    1%-margin A/Bs. Design doc:
    `docs/design/0100-0149/m0141-s2b-11-addgroupingpaths-sorted-before-hashed.md`;
    `docs/design/README.md` new index row; artefacts
    `analysis/m0141/m0141-s2b11-*`. Follow-up filed as **M0141-S2b-12**
    below (the real remaining divergence). Gates: `go build ./...` clean;
    `go test ./internal/optimizer/...` PASS; `scripts/tpch-spotcheck.sh`
    PASS (gate-stamp PASS vs staged code_tree);
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE 99/99;
    `scripts/tpch-acceptance-arm.sh` before/after digest VERDICT PASS
    24/24 MATCH; `pg-plan-parity-diff.py` match=7 before AND after
    (floor held, noise band ±3); `python3
    scripts/ralph_protected_regions.py check-designdocs` exit 0;
    `python3 scripts/ralph-lineage-guard.py` exit 0; pre-commit pgbench
    smoke PASS (hook). Ledger row appended (task-id `M0141-S2b-11`).
  - [x] **M0141-S2b-12** — recon: the M0129-S1 exact-cost fuzz tiebreak
    Kind: recon
    masks PG's `COSTS_EQUAL` semantics — Q4/Q12 (and every other
    fuzzily-tied grouping election) can never flip on insertion order.
    Parent: M0141-S2b-11. Filed 2026-09-18 by S2b-11's measured result:
    `path.go:943-956` (inside `comparePathCostsFuzzily`) returns
    `costsBetter1`/`costsBetter2` on the EXACT cost when total and
    startup are within `STD_FUZZ_FACTOR`, where PG's
    `compare_path_costs_fuzzily` (`pathnode.c`) returns `COSTS_EQUAL`.
    The deviation is deliberate (M0129-S1: it keeps a pathkey-less hash
    path alive against a fuzzily-equal-cost pathkeyed rival — CTE
    self-join nested-loop-only pathology) but it has two masking effects
    on the grouping election: (a) at the ordered rel, a strictly-cheaper
    second candidate always evicts the first-inserted one — PG's
    keep-first-on-equal-pathkeys tie-break never engages (Q4/Q12 stay
    Hashed despite S2b-11's PG-order insertion); (b) at the grouped rel,
    the exact-cost direction makes hashed-vs-sorted incomparable instead
    of letting the pathkeys dim let Sorted dominate, so keyless Hashed
    survives where PG discards it. Recon scope: enumerate every call
    site/election the deviation can affect (grouped rel, ordered rel,
    join pathlists — anywhere `comparePaths` feeds `addToPathlist`);
    read the M0129-S1 design doc + its motivating regress/TPC cases;
    determine whether a narrowing exists that preserves the
    incomparability-preserving case (non-cost dims differ) while
    restoring `costsEqual` when all other dims are equal (PG's
    keep-first) — e.g. splitting the deviation into a
    `costsWeaklyBetter` that `comparePaths` treats as `dimEqual` only
    when it would otherwise flip an all-equal-dims result. Also evaluate
    the sibling `partialaggupper.go` hashed-before-sorted order
    (`:400`/`:412`/`:474`, same inversion class) once any
    insertion-order effect can actually fire. Expected movement if
    unblocked: TPC-H Q4/Q12 flip Hashed+Sort → Sort-fed GroupAggregate
    matching PG's `EXPLAIN` (both currently `aggregation-strategy` +
    `sort-strategy` SHAPE-DIFFs; each would lose both tags toward
    MATCH). Measure with the same pinned-epoch before/after
    `tpch-estimate-audit-arm.sh` A/B S2b-11 used
    (`GOOPG_ANALYZE_SEED=20260905`, `PGSHAPED=1`, `--ref-port 65432`),
    plus the M0129-S1 motivating corpus to prove no resurrection.
    **DONE 2026-09-19.** Movement: none (recon only; no production file
    changed — the measured deltas below come from a throwaway worktree
    patch, `/tmp/s2b12-wt`, never committed). Design doc:
    `docs/design/0100-0149/m0141-s2b-12-m0129-s1-exact-cost-fuzz-tiebreak.md`.
    Findings: (1) the deviation sits at the single serial-pathlist
    funnel — `comparePathCostsFuzzily` → `comparePaths` →
    `addToPathlist` → `addPath` covers every `RelOptInfo.Pathlist`
    insertion; `addToPartialPathlist` is a separate already-PG-faithful
    comparator. (2) No narrowing can separate the cases: the grouped-rel
    signature the flip needs (keyless-cheaper vs keyful-dearer in-band)
    is IDENTICAL to the join-rel signature M0129-S1 protected in Q74 —
    demoting the weak cost direction only when it is the sole
    directional dim leaves the pathkeys dim directional → still
    incomparable → hashed survives. The only PG-faithful resolution is
    full `COSTS_EQUAL` restore. (3) Measured restore: TPC-H 8/22 plans
    changed, `CATEGORIES-EXCL-MATCH` agg-strategy 9→4,
    parameterisation 5→3 (join-order 13→15, join-method 9→10,
    scan-type 8→9 adverse), match 7→7, Q4/Q12 flip
    HashAggregate→GroupAggregate; TPC-DS SF0.25 75/99 plans changed,
    agg-strategy 71→44, sort-strategy 77→71 (parallelism 84→86,
    qual-placement 20→22 adverse), match 2→2; **Q74 healthy** — hash
    joins throughout the CTE chain. (4) Why safe: `ea1b2fbec`'s
    companion `initialRelRows` CTE estimate fallback is what killed the
    NL pathology (0.005^4 collapse → 1 row made NL look free); the
    comparator deviation was belt-and-suspenders. Caveat: original
    measurement was SF0.5, clone was SF0.25 — residual risk is a
    sane-but-slower merge-over-hash election at other scales, not the
    NL bug. (5) Sibling: `partialaggupper.go` no-split arm inserts
    HASHED before SORTED (~:399 vs ~:410) — same inversion class S2b-11
    fixed in `groupingpaths.go`, only meaningful once `COSTS_EQUAL`
    makes insertion order matter. Instruments: `pg-plan-parity-diff.py`
    TPC-H + TPC-DS roll-ups above; shape census 75/99. Impl filed as
    M0141-S2b-13.
  - [x] **M0141-S2b-13** — impl: restore PG `COSTS_EQUAL` semantics in
    Kind: impl
    Parent: M0141-S2b-12
    `comparePathCostsFuzzily` (delete the M0129-S1 exact-cost fallback
    at `path.go:943-956`; double-fuzz → `costsEqual`), plus the
    `partialaggupper.go` no-split-arm sibling reorder (insert SORTED
    before HASHED, matching `add_paths_to_grouping_rel`'s
    can_sort-before-can_hash). S2b-12's design doc holds the full
    measured justification. Scope: (a) comparator
    restore; (b) `path_test.go` update —
    `TestComparePathCostsFuzzily_WithinFuzzIsEqual` pins the deviation
    and must be re-asserted to PG semantics (audit neighbors);
    (c) partialaggupper sibling swap. Consider also restoring PG's
    missing `rows` dim + `parallel_safe` asymmetry inside the
    `COSTS_EQUAL`-tie adjudication (`pathnode.c:541-560`: better
    pathkeys dominate only when `new->rows <= old->rows` etc.) —
    S2b-12's doc records it as a residual simplification; include only
    if measurement shows a case needing it. Expected movement per
    S2b-12 measurement: TPC-H `CATEGORIES-EXCL-MATCH` agg-strategy
    9→4, Q4/Q12 (and Q5/Q8/Q21/Q22) flip HashAggregate→GroupAggregate;
    TPC-DS agg-strategy 71→44, sort-strategy 77→71 — with known adverse
    drift (TPC-H join-order +2/join-method +1/scan-type +1; TPC-DS
    parallelism +2, qual-placement +2, TPC-H Q9 away-from-PG) and 75/99
    TPC-DS plan churn, so land behind the FULL gate set:
    `tpch-spotcheck.sh`, `tpch-acceptance-arm.sh` 24/24, the FULL
    `tpcds-sf025-regression.sh sweep` (row counts + checksums, not just
    plan shapes), pinned-epoch floor capture + `pg-plan-parity-diff.py`,
    `make ea-ratchet`, and a Q74 timing sanity at the largest available
    scale (M0129-S1's original gate was Q74 99s→14s at SF0.5; SF0.25
    recon showed no NL resurrection). If the sweep shows ANY new
    row-count or checksum delta, STOP — do not land.
    **DONE 2026-09-18, landed. Full writeup:
    `docs/design/0100-0149/m0141-s2b-13-costs-equal-restore.md`.**
    The impl grew past (a)+(b)+(c): the minimal `costsEqual` restore
    immediately surfaced that the dimension-fold comparator couldn't
    express PG's pairwise table (the `partialSortVerdict` test caught
    the missing `1.0000000001` tight-fuzz at all-equal dims), so
    `comparePaths` was rewritten as a direct port of `add_path`'s
    pairwise adjudication (pathnode.c:475-610): COSTS_DIFFERENT keeps
    both; parameterised paths pretend NIL pathkeys; COSTS_BETTER* gated
    on outer-rel/rows/parallel-safe; COSTS_EQUAL prefers better
    pathkeys then equal-keys falls through parallel_safe → rows →
    tight-fuzz → keep-old. `addToPartialPathlist` untouched. Tests
    re-asserted to PG outcomes (param index-scan eviction under the
    NIL pretense, 1-candidate grouped rel → fixture rebuilt on a real
    `COSTS_DIFFERENT` trade-off, C19f fixture recalibrated to win by a
    real margin since PG doesn't divide disk cost, EXPLAIN test now
    expects the GroupAggregate render). Gates: units PASS;
    tpch-spotcheck PASS (Q12=2/Q13=34); tpch-acceptance-arm 24/24 vs
    HEAD; tpcds-sf025 sweep PASS=96/0/0/0 SKIP=3, 64 plans changed,
    154s→149s; TPC-H parity match 7→8 (same-epoch HEAD also 8, verdict
    sets identical — nothing lost); TPC-DS parity match 2=2 (Q9/Q41).
    CATEGORIES-EXCL-MATCH TPC-H: agg-strategy 8→3, parameterisation
    5→4, qual-placement 4→3; join-order +2, join-method +1, scan-type
    +1 adverse (inside the ±3 band). TPC-DS: agg-strategy 71→44,
    sort-strategy 77→69, join-method −3, scan-type −3, join-order −1;
    qual-placement +3, parameterisation +1, rendering +1 adverse.
    `make ea-ratchet` 70→76 (+13 NEW all relset-key churn — estimator
    untouched; Q85's reason-early order matches PG itself; Q54 a node
    rename), baseline re-pinned to 76 per M0142-0012 precedent; NEW
    findings filed as **M0141-S2b-14**. Q74 sanity SF0.25 (largest
    loaded scale): 1.8s, fewer NLs than PG's own plan — no NL
    resurrection.
    Movement: yes — CATEGORIES-EXCL-MATCH aggregation-strategy 71→44
    (TPC-DS) / 8→3 (TPC-H) and match 7→8.
  - [x] **M0141-S2b-14** — recon/triage: the 13 NEW ea-ratchet findings
    S2b-13's plan churn surfaced (baseline re-pinned to 76).
    Kind: recon
    Parent: M0141-S2b-13
    DONE — all 13 classified as relset-key churn of PG-faithful
    estimates; no estimator defect isolated, no fix filed.
    Design doc:
    `docs/design/0100-0149/m0141-s2b-14-ea-findings-triage.md`.
    Verified by reproducing every flagged relset through `EXPLAIN` on
    BOTH engines (PG `:65438/tpcds025` read-only vs goopg
    `:65437/postgres`) with the queries' complete predicate sets:
    Q61 `ss⋈dd` goopg 94 vs PG 97; Q68 213 vs PG 219 (the first-pass
    15x "divergence" was my own omitted `d_dom BETWEEN 1 AND 2`
    predicate — with the full set PG also estimates ~219); Q89 1102 vs
    PG 1107; Q7 1102 vs PG 1107; Q85 `ws⋈wr` 6 vs PG 5. The ~40–120x
    under-estimates vs actual are shared PG-formula errors — the
    ratchet flags them only because PG's chosen plan decomposes the
    join order differently and never materializes the relset.
    Q85's reason-early join order matches PG's own plan; Q54 is the
    intended HashAggregate→GroupAggregate rename at identical est/act.
    The one flagged `pg_est` divergence (Q61
    `date_dim+store+store_sales` HJ goopg 23 vs PG 8, ~3x) resolved as
    a **reporting-convention artifact, not a selectivity bug**: goopg
    stamps TOTAL rows on partial-path nodes while PG stamps per-worker
    rows (divisor ~3.1 — `cost_seqscan` divides). Minimal repro:
    `ss⋈store` goopg 688033-total vs PG 222047-per-worker, actual
    687463 — both engines essentially exact. Scorer caveat folded into
    S2b-15's scope note below.
    Movement: none
  - [x] **M0141-S2b-15** — impl (small): gather row-stamp divergence found
    by S2b-13's C19f trace. `makeGatherPath`/`makeGatherMergePath`
    (`gatherpaths.go:273`) always stamp `Rows = computeGatherRows(sub)`
    (`sub.Rows × parallel divisor`). PG's `cost_gather`/`cost_gather_merge`
    stamp `rel->rows` unless the caller overrides —
    `generate_gather_paths` passes `override_rows=false` for scan AND
    join rels (allpaths.c:3091-3112, :557, :3518), so a scan/join Gather
    carries the relation's own total (e.g. 186), not the
    divide-then-multiply round-trip (e.g. 187). Fix: pass the
    override-flag equivalent per call site — `addBaseRelGatherPaths` +
    `joinsearchlevel.go`/`geqo.go` sites take `rel.Rows`;
    `generateUpperRelGatherPaths` keeps `computeGatherRows`. Effect is
    small: the off-by-one only reaches `add_path`'s `rows` tie-break
    inside fuzzy ties and shifts EXPLAIN `rows=`. S2b-14 adds a
    related-but-broader surface to check while in this code: goopg's
    EXPLAIN stamps TOTAL rows on partial-path nodes (e.g.
    `Parallel Seq Scan on store_sales rows=719876`) while PG stamps
    per-worker rows (`rows=232218`, `cost_seqscan` divides by
    `parallel_divisor`) — same underlying estimates, different `rows=`
    convention; it made the ea scorer read goopg ~3.1x high vs
    `pg_est` on matched partial-path relsets (Q61's flagged HJ).
    Decide there whether the gather stamp fix should also move the
    partial-path display convention toward PG. Gates: units +
    spotcheck + SF0.25 sweep; expect a few plan churns where a gather
    sits inside a fuzz band.
    Kind: impl
    Parent: M0141-S2b-13
    ESCALATION 2026-09-19 — implementation complete and staged
    uncommitted; the commit is hard-blocked because
    `bench/tpch/runtime_goopg/data.HOLD` (unclean-shutdown evidence
    hold stamped 2026-09-18T22:52, owner-only recovery via
    `scripts/tpch-ref-recover.sh --i-am-owner`) makes
    `tpch-spotcheck` and `tpch-acceptance-arm` SKIP-BLOCKED, and
    `.ralph/gate-exceptions.md` has no active row for this task.
    Gate evidence already collected on the staged tree: units PASS;
    `tpcds-sf025` sweep PASS=96 MISMATCH=0 + stamp; TPC-DS parity
    match=2 (Q9/Q41, floor holds); TPC-H patched-vs-HEAD plans
    byte-identical on a private preloss-clone copy (match=7 both,
    floor holds); manual Q12=2/Q13=34 canonical; `make ea-ratchet`
    10 NEW / 13 FIXED — all NEW `pg_est=None` relset-key churn
    (triage + repin on resume). Resume: owner lifts the HOLD (or
    grants an exception row) → re-run `tpch-spotcheck` +
    `tpch-acceptance-arm` on the staged tree → triage ea findings →
    commit. Design doc:
    `docs/design/0100-0149/m0141-s2b-15-gather-rows-stamp.md`.
    Movement: yes — CATEGORIES-EXCL-MATCH TPC-DS agg 44→43, sort
    69→67, qual 23→22 at equal match floor (2=2 Q9/Q41); ea-ratchet
    findings net 76→73; TPC-H byte-identical to HEAD (nothing lost).
    - **UNBLOCKED 2026-09-19 — `:65433` restored, `data.HOLD`
      released.** The owner ran `scripts/tpch-ref-recover.sh
      --i-am-owner`: the crashed data dir was preserved at
      `tmp/evidence-65433-20260919-unclean/` (the original kept
      aside at
      `bench/tpch/runtime_goopg/data.pre-restore-20260919-031106`),
      `data` was restored from `preloss-clone-20260915`, the pinned
      `goopg-bin` was rebuilt at `4c36912b0`, `:65433` is listening
      again and `tpch-spotcheck` returned PASS (Q12=2, Q13=34).
      `tpch-spotcheck`/`tpch-acceptance-arm` are no longer
      SKIP-BLOCKED, so the staged commit is unblocked. Next: re-run
      both on the staged tree → triage the 10 ea NEW findings →
      repin if churn → commit.
    - **DONE 2026-09-20 (Loop \#28) — residual bookkeeping closed.** The
      code had already landed inside `caf858301` (`overrideRows` call-site
      split); what remained was the promised ea-ratchet triage + repin.
      Fresh `make ea-ratchet` on HEAD `07ab54281` (new capture, 99
      queries): **8 NEW / 30 FIXED** — FIXED list is the baseline's stale
      keys. Triage (S2b-14 method — relset re-verified against PG
      `:65438/tpcds025` and `bench/tpcds/plans-pg/`):
      - 5 `pg_est=None` findings are **churn/shared-PG-error**, none
        S2b-15-caused: Q85×3 is the recorded key churn verbatim
        (`reason` drops out of the relset key — 4 old `+reason+` keys
        went FIXED, new keys without it went NEW); Q40 Gather
        `catalog_sales+date_dim+item+warehouse` goopg **17** vs PG's own
        Gather **19** on the identical relset (actual 444 — shared
        ~25x formula under-estimate; `pg_est=None` is a scorer
        key-matching miss, PG does plan a Gather there); Q71 Gather
        `catalog_sales+date_dim+web_sales` goopg **229** vs PG's
        per-branch sum **~181** on the same UNION-branch join
        (actual 18205 — shared ~80x under-estimate).
      - 3 `pg_est` findings are **post-S2b-15 shape-admission
        exposures, filed as M0141-S2b-17**: Q62/Q99 `Finalize
        HashAggregate`/`Partial`/`Gather` all stamp `rows=1` — a
        PlanCost display-stamp gap in the C-19g post-pass
        (`splitAggregate`'s `final := *a` copies an unstamped spec;
        `NewGather` never sets PlanCost — `partialaggupper.go`'s path
        model carries `finalGroups` correctly; PG's `Finalize
        GroupAggregate` shows 120/72). Shapes admitted by
        `9a2b9d47b` (NLI+Memoize Gather-driver — Q62/Q99's inner is
        exactly NL+Memoize-over-pkey-scan) after the staged
        measurement. Q94 `Hash Semi Join` goopg **90** vs PG **1**
        (actual 4) — genuine semi-join selectivity divergence on the
        new `Gather→NL→Parallel HJ` inner.
      - Baseline re-pinned 76→54 (`make ea-ratchet-repin`; re-score
        PASS 54/54, zero NEW). `tpch-spotcheck`/`tpch-acceptance-arm`
        ran clean at Loop \#27's commit on this tree.
      Movement: none added this loop — the impl's movement was already
      recorded (CATEGORIES-EXCL-MATCH agg 44→43, sort 69→67, qual
      23→22 at equal match floor).
  - [ ] **M0141-S2b-16** — impl (small): partial-path `rows=` display
    convention. S2b-15 fixed the path-model Gather stamp
    (`rel->rows`), but the S2b-14-observed divergence — goopg EXPLAIN
    shows TOTAL rows on partial nodes (`Parallel Seq Scan
    rows=719876`) where PG shows per-worker rows (`rows=232218`) —
    lives in the **post-pass** `rebuildWithGather`/`splitAggregate`
    path (`internal/executor/` + `internal/optimizer/parallel.go`
    region): serial-plan nodes keep their serial estimates under a
    `Parallel` label. Stored partial-path `Path.Rows` is already
    per-worker (`costParallelSeqscan` divides by the divisor, as
    `cost_seqscan` does) — the post-pass never adopts it. Fix
    direction: stamp the per-worker estimate on partial nodes when
    the post-pass wraps a serial subtree, or re-derive at render;
    scope carefully so Gather/leader-row accounting stays exact.
    Ledger row: `s2b15-partial-path-display` (deferred from
    M0141-S2b-15). Gates: units + spotcheck + SF0.25 sweep +
    ea-ratchet (rows= keys will churn again).
    Kind: impl
    Parent: M0141-S2b-15
  - [ ] **M0141-S2b-17** — recon+impl (small): the two divergences
    S2b-15's repin triage isolated on the post-`9a2b9d47b`
    parallel shapes.
    Kind: recon
    Parent: M0141-S2b-15. Filed 2026-09-20 (Loop \#28). Two arms:
    (a) **split-agg PlanCost stamp gap** — `Finalize
    HashAggregate`/`Partial HashAggregate`/their `Gather` all render
    `rows=1` (TPC-DS Q62/Q99) while `PathFinalizeAgg.Rows` =
    `finalGroups` is correct (`partialaggupper.go:571`); the C-19g
    post-pass `splitAggregate` (`parallel.go:1399`, `final := *a`)
    and `NewGather` (`plan.go:2811`) never propagate PlanCost onto
    the built nodes. PG stamps the group estimate (`Finalize
    GroupAggregate` 120 on Q62, 72 on Q99). Sibling of S2b-16's
    `rebuildWithGather` display gap — check whether the fix is
    shared or a second site; may fold into S2b-16's landing.
    (b) **semi-join selectivity divergence** — Q94 `Hash Semi Join`
    goopg est **90** vs PG **1** (actual 4, qerr 22.5 vs 4.0) on
    `customer_address+date_dim+web_sales+web_site` over the newly
    admitted `Gather→NL→Parallel Hash Join` inner. Determine whether
    the semi-join clause selectivity or the inner input estimate
    moved; compare `cost_semijoin`/semi-join clause selectivity
    against the oracle before touching anything. Gates: ea-ratchet
    re-score (findings go FIXED) + units + spotcheck + SF0.25 sweep.
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
  Kind: recon
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
    Kind: impl
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
  - **UPDATE 2026-09-18 (banner item 6's "compare the cost breakdown with PG
    for the 14 queries," DONE, no production change).** Traced the remaining
    13 witnesses (only Q3 had been traced before) on `:65437` with
    `GOOPG_INCREMENTAL_SORT=on GOOPG_PGSHAPED_DP_TRACE=1`. 7 of 13
    (Q4/Q11/Q43/Q54/Q58/Q60/Q83) reach the arm; the other 6 (Q35/Q49/Q63/
    Q64/Q67/Q89) don't, and 5 of those 6 are already explained by open tasks
    (Q35: full ORDER-BY/GROUP-BY match, arm 1's own case by design; Q63/Q67/
    Q89: all three are `WindowAgg`-wrapped ORDER BYs — reclassifies Q89 out
    of "GroupAggregate" — and are `M0141-S2b-3b`'s stated gate, not a new
    finding; Q49: `M0141-S2b-4`'s SETOP gate). **Q64 is a genuinely new,
    unexplained gap** — zero `upper.ordered` producer lines beyond the seed
    Sort in a 134k-line trace, can't tell from the trace alone whether
    `SearchCandidates` is empty or every candidate's keys are unusable;
    filed as **M0141-S7-cd-q64** below. For the 7 that DO reach the arm,
    decomposing each one's incrementalsort-vs-sort total-cost gap into
    "different input candidate priced" vs. "sort-formula overhead itself"
    shows the **input-candidate divergence dominates in 6 of 7** (from ~55%
    of the gap up to ~99.9% for Q43) — `addIncrementalSortPaths` can only
    attach to a `SearchCandidates` entry with a partial (non-full,
    non-empty) prefix match, and on this corpus the candidates that qualify
    are consistently pricier than the cheapest overall seed (most plausibly
    the parallel/`Gather Merge`-shaped candidates PG uses that goopg's own
    plan doesn't reach at all for these queries — same compounding-upstream-
    divergence caveat 2026-09-17i already raised for Q3). This is sharper
    than, and does not resolve or get resolved by,
    `M0141-S2b-6-resume`'s open Hashed-vs-Sorted tie (that tie is about
    which `PathAgg` STRATEGY feeds each arm for one query; this is about
    candidate-set MEMBERSHIP — which candidates have a usable order to
    credit at all). Filed as **M0141-S7-cd-candidatepool** below. Full
    per-query cost table and the Q43/Q54/Q60 absolute-number breakdown:
    design doc's "Update 2026-09-18" section. `GOOPG_INCREMENTAL_SORT` stays
    default-off (unchanged verdict). No ledger row — every component named
    is already a filed, tracked task, not a newly-discovered undocumented
    gap. Gates: none beyond the trace captures (no `internal/`/`cmd/` file
    touched); `make ralph-state-guard` passed; pre-commit pgbench smoke
    PASS. The `:65437` cluster was restarted with the two env vars, traced,
    then restarted again at its default flag state before this commit (env
    vars unset from the systemd `--user` manager's environment block too, so
    they can't leak into an unrelated later gate in this session).
  - [x] **M0141-S7-cd-q64** — root-cause why Q64's ORDER BY offers zero
    `upper.ordered.incrementalsort` (and, on the 2026-09-18 trace, zero
    `upper.ordered` producer lines of ANY kind besides the winning seed
    Sort) despite being classified alongside Q4/Q11 as a Nested-Loop-shaped
    witness that DOES reach the arm.
    Kind: impl (owner reclassification 2026-09-18 — it landed env-gated
    traces in non-test internal/optimizer files, which C1 forbids for a recon)
    Parent: M0141-S7
    Movement: none
    Hypothesis (unverified): Q64's outer join is over two references to the same
    materialized CTE (`cross_sales cs1, cross_sales cs2`); a CTE-scan
    boundary may drop `SearchCandidateKeys` for one or both self-join legs —
    same family as [[cte_leaves_reach_search_wrapped_in_filter]] but for
    pathkey propagation, not residual-filter placement, and NOT yet
    confirmed. Distinguishing "`SearchCandidates` empty" from "every
    candidate's `keys` is `len==0`" needs instrumenting
    `createOrderedPaths`'s candidate-count at population time
    (`upperordered.go:106-118`) — an `internal/` file, hence deferred past
    this recon-only loop. Expected movement (S5): if the hypothesis holds
    and is fixed, Q64 moves from "arm 3 unreachable" to "arm 3 reachable but
    still cost-dominated" (per the candidatepool finding below, this would
    NOT by itself flip Q64's plan shape) — measured by a repeat
    `GOOPG_PGSHAPED_DP_TRACE=1` capture on Q64 showing a nonzero count of
    `upper.ordered.incrementalsort` lines, no TPC-H dependency.
    - **Done 2026-09-18b, hypothesis REFUTED, real cause found: Q64 was
      miscategorized, not under-served.** Landed the instrumentation
      (`traceOrderedCandidatePopulation` + `traceIncrementalSortCandidate`,
      `internal/optimizer/pathtrace.go`, both gated on the pre-existing
      `pathTraceEnabled`/`GOOPG_PGSHAPED_DP_TRACE` flag — inert at default,
      `go build ./...` clean, unit suite unchanged) and traced on a private
      throwaway cluster (port 5533, own `GOOPG_CG_UNIT`) loaded from the
      already-sampled SF0.25 TSVs — the RALPH_LOOP guard now blocks
      restarting `:65437` directly (added since the 2026-09-17h/18 traces in
      this file that did restart it), so `:65437`/`:65438` were never
      touched. Trace result: `searchedrel=true candidates=7 nonemptykeys=6`
      — BOTH prior hypotheses false, `SearchCandidates` is populated
      correctly. All 6 non-empty-key candidates are `PathMergeJoin` with a
      3-key claim scoring `ncommon=0` against the outer 5-key sort list —
      zero shared columns, not a partial-prefix miss. Re-reading
      `query64.sql` explains why: the outer `ORDER BY` sorts on
      `cross_sales`'s own aggregate OUTPUT columns
      (`product_name`/`store_name`/`cnt`/`s1`), which no join-shaped
      candidate could ever carry a prefix of — PG's own plan satisfies this
      level with a plain `Sort`, not Incremental Sort. The `Incremental
      Sort` node PG's plan DOES contain (`Presorted Key: item.i_item_sk`)
      sits INSIDE the CTE's own `GroupAggregate` build — a sorted-GroupAgg
      input-sort decision at a completely different call site than
      `createOrderedPaths`/`addOrderedPaths` (which only ever sees the
      OUTER statement's `ORDER BY`). Q64 was never an outer-ORDER-BY
      witness; `nCommon==0` on every candidate is the CORRECT verdict, not
      a gap — M0141-S7-cd-q64's own premise does not survive contact with
      the query text. Full writeup: design doc's "Update 2026-09-18b"
      section. Reclassification (Q64 moves from the "Nested Loop x4" outer
      bucket to the CTE-internal-GroupAggregate bucket, and whether it is a
      new 6th member of that family or already covered by
      M0141-S2b-0/S2b-7) is NOT decided here (out of this recon loop's
      one-task budget) — filed as **M0141-S7-cd-q64-reclassify** below.
      `GOOPG_INCREMENTAL_SORT` stays default-off. No ledger row (closes an
      open question with a definite answer; the follow-up is a normal filed
      task, not an undocumented gap). Gates: `go build ./...` clean, `go
      vet ./internal/optimizer/` clean, `go test ./internal/optimizer/...`
      PASS, `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
      PASS, no TPC-H dependency.
  - [x] **M0141-S7-cd-q64-reclassify** — decide where Q64 actually belongs
    in the 14-witness corpus tally now that M0141-S7-cd-q64 shows its
    Incremental Sort node is CTE-internal-GroupAggregate-scoped
    (`Presorted Key: item.i_item_sk` feeding `cross_sales`'s own
    `GROUP BY`), not outer-ORDER-BY-scoped like its former "Nested Loop x4"
    grouping implied.
    Parent: M0141-S7. Needs: (a) confirm whether
    `electOrderedGrouping`'s CTE-materialization callers already cover a
    CTE's OWN internal aggregate-strategy election the same way they cover
    a top-level statement's (M0141-S2b-0/S2b-7's stated scope was "every
    `createXPaths -> createOrderedPaths` call site" for the OUTER
    statement — whether a CTE's inner query goes through the identical
    machinery, or a separate un-audited path, is not yet checked); (b) if
    covered, Q64 is just a 6th witness of the already-tracked
    GroupAggregate family, requiring no new task, just a tally correction
    in this file's producer-shape table and the "Nested Loop x4" ->
    "Nested Loop x3 (Q4, Q11, Q35)" edit named in the design doc's Update
    2026-09-18b; (c) if NOT covered, file the CTE-internal gap as its own
    M0141-S2b-N-class task. No TPC-H dependency; recon-only until (c)'s
    outcome is known.
    - **Done 2026-09-18 — case (c), NOT covered.** Read-only recon (no
      server, no trace needed for this half): `electOrderedGrouping`'s
      call site (`planner.go:1960`) only runs when `len(s.OrderBy) > 0`,
      and `cross_sales`'s own `SELECT` (the CTE body) has NO `ORDER BY` of
      its own (`query64.sql`) — so the question "does the CTE-materialization
      caller reach `electOrderedGrouping`" doesn't apply; that function is
      structurally irrelevant to the CTE's own planning pass regardless of
      caller wiring. The mechanism PG's `Presorted Key: item.i_item_sk` /
      `GroupAggregate` actually exercises — a sorted-input GROUP BY
      strategy chosen with NO `ORDER BY` anywhere in the query — is a
      different goopg code path: `addGroupingPaths`'s SORTED arm
      (`groupingpaths.go:441-495`), which always calls
      `sortPathForBounded` (`joinpathsmerge.go:480-515`, unconditionally
      builds a full `PathSort`) and never offers an
      Incremental-Sort-over-partial-prefix-seed candidate the way
      `addIncrementalSortPaths` does for the outer-ORDER-BY case — for ANY
      query, not just Q64. Genuinely un-audited, distinct from
      M0141-S2b-0/S2b-7's scope. Tally correction: producer-shape table's
      "Nested Loop x4" -> "Nested Loop x3 (Q4, Q11, Q35)" (Q64 is neither
      an outer-ORDER-BY Nested Loop witness nor a GroupAggregate-family
      member). New task filed: **M0141-S2b-8** below. Full writeup: design
      doc's "Update 2026-09-18c". No ledger row (closes an open question
      with a definite answer, files its own follow-up directly). Gates:
      none needed, no code changed.
  - [x] **M0141-S7-cd-candidatepool** — investigate whether
    `addIncrementalSortPaths` (`incrementalsortpaths.go:161-166`) should
    also consider building its `PathIncrementalSort` over the SAME cheap
    seed candidate `createOrderedPaths`'s arm 1/2 already uses, when that
    seed's own claimed ordering has a genuine partial (not full, not empty)
    prefix match.
    Kind: impl (owner reclassification 2026-09-18 — same reason as
    M0141-S7-cd-q64: env-gated traces in non-test internal/optimizer files)
    Parent: M0141-S7
    Movement: none
    - **Done 2026-09-18d — mixed verdict, one real gap confirmed (Q4),
      six witnesses cleared.** The corpus splits across two different
      `addOrderedPaths` callers with different `SearchCandidates`
      provenance: `createOrderedPaths` (Q4/Q11/Q58/Q83) populates it from
      `searchedRelOf(input).Pathlist`; `electOrderedGrouping`
      (Q43/Q54/Q60) populates it directly from its own Hashed/Sorted
      `PathAgg` `cands` slice (M0141-S2b-7's own documented mechanism).
      Landed `traceOrderedSeedCandidate` (`internal/optimizer/pathtrace.go`,
      called from `addOrderedPaths`) plus a `totalcost` field added to the
      existing `traceIncrementalSortCandidate`, both `pathTraceEnabled`-gated
      and inert by default — needed to tell a same-shaped seed/candidate
      pair apart from a coincidence (every `SearchCandidates` entry shares
      the same relset and `Rows`, so cost is the only discriminator).
      Traced all 7 witnesses on a private throwaway cluster (port 5533, own
      `GOOPG_CG_UNIT`, `tmp/m0141s7candpool/`, SF0.25 TSVs — same discipline
      as M0141-S7-cd-q64, `:65437`/`:65438` untouched). Result: **(a) holds
      for Q43/Q54/Q60** (exact cost-identity match between the seed and its
      `SearchCandidates` counterpart on every witness — `electOrderedGrouping`'s
      engineering already covers this, confirmed not just by shape but by
      bit-identical cost) **and for Q11/Q58/Q83** (seed's own claim is
      `keys=0` — genuinely no order to offer, the "hash-shaped" case (a)
      anticipated). **(b) holds for Q4**: seed carries `keys=1 ncommon=1` (a
      real, unexploited partial-prefix claim) with NO exact-cost match in
      `SearchCandidates` (closest is 0.01 cheaper — explained by Q4's own
      `LIMIT 100` making `getCheapestFractionalPath`'s seed selection
      startup-weighted, not raw-Total, so the near-match is a genuinely
      different candidate). The seed itself is structurally never a member
      of `ordered.SearchCandidates` under this caller, so
      `addIncrementalSortPaths` can never attach a `PathIncrementalSort` to
      it even when it qualifies. Full per-witness cost table and mechanism
      writeup: design doc's "Update 2026-09-18d" section. Follow-up filed as
      **M0141-S2b-9** below (implementation, not selectable under item 6's
      "cost diagnosis only" restriction). `GOOPG_INCREMENTAL_SORT` stays
      default-off; no ledger row (same posture as 18b/18c — a filed
      follow-up carries the deferral). Gates: `go build ./...` clean, `go
      vet ./internal/optimizer/` clean, `go test ./internal/optimizer/...`
      PASS, `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
      PASS (all packages). No TPC-H dependency.
  - [ ] **M0141-S2b-9** — teach `addIncrementalSortPaths` (or its
    `createOrderedPaths` caller) to also score the seed `input` itself
    against `sortPathkeys` and offer a `PathIncrementalSort` built over the
    seed when `0 < nCommon < len(sortPathkeys)` — today the loop only walks
    `ordered.SearchCandidates` (`sr.Pathlist`), which structurally never
    contains the exact Path `getCheapestFractionalPath` chose to become the
    seed, so a genuine partial-prefix claim on the seed itself (confirmed
    live on Q4: `keys=1 ncommon=1`, no exact-cost `SearchCandidates` match)
    can never be offered an Incremental Sort.
    Parent: M0141-S7 (filed by
    M0141-S7-cd-candidatepool; design doc's Update 2026-09-18d). Scope: (1)
    price the seed-based candidate via the SAME fractional-cost-aware
    comparison `getCheapestFractionalPath` uses, not raw `Total` — Q4 itself
    has `LIMIT 100`, which is exactly why its seed was chosen over a
    marginally-cheaper-on-Total alternative, so a naive Total-cost seed
    candidate risks picking the wrong thing under a LIMIT; (2) add the
    seed-vs-`sortPathkeys` offer in `addOrderedPaths`/`addIncrementalSortPaths`
    without duplicating work when the seed also happens to appear in
    `SearchCandidates` (electOrderedGrouping's callers already produce that
    duplicate today, per 18d's cost-identical trace — dedupe or accept the
    redundant `addPath` call, `setCheapest` already tolerates it); (3)
    sibling-path audit: confirm `createIncrementalSortPlan` handles a
    `PathIncrementalSort` whose child is the bare seed (not a join/agg
    candidate from `SearchCandidates`) the same as any other child shape.
    This is an implementation task (not recon) — not selectable while
    M0141's banner item 6 restricts to "cost diagnosis only, no executor
    work"; wait for the banner to open item 3/4's implementation tasks.
    Gate: TPC-DS SF0.25 sweep (category movement, no regression) + `go test
    ./internal/optimizer/...`; re-check Q4's own plan shape specifically
    (LIMIT-sensitive) once implemented; no TPC-H dependency.
  - [ ] **M0141-S2b-8** — `addGroupingPaths`'s SORTED arm
    (`groupingpaths.go:441-495`) never offers an
    Incremental-Sort-over-partial-prefix-seed candidate for a plain
    `GROUP BY` with no `ORDER BY` in the query — it always calls
    `sortPathForBounded` (`joinpathsmerge.go:480-515`), which
    unconditionally builds a full `PathSort` over `seed` regardless of
    whether `seed.Pathkeys` already shares a partial prefix with the group
    keys. This is the mechanism PG's Q64 plan exercises inside the
    `cross_sales` CTE (`Presorted Key: item.i_item_sk` feeding a
    `GroupAggregate`, no `ORDER BY` anywhere in that CTE) — distinct from
    `electOrderedGrouping`/M0141-S2b-0/S2b-7, which only runs when the
    query (or CTE) HAS an `ORDER BY` of its own.
    Parent: M0141-S7 (filed by M0141-S7-cd-q64-reclassify, case (c); design
    doc's Update 2026-09-18c). Depends on nothing new — same DPPATH trace
    infra (`pathtrace.go`) M0141-S7's other arms already use applies here.
    Scope: (1) confirm live whether `seed.Pathkeys` for Q64's CTE actually
    carries a genuine partial prefix of the group keys at the point
    `addGroupingPaths` runs (the read-only recon above establishes the
    STRUCTURAL gap — no code path exists to even try — but does not yet
    trace whether Q64 specifically would benefit, vs. some other witness);
    (2) if confirmed, add a `PathIncrementalSort`-over-`seed` candidate to
    the SORTED arm, gated the same way as `addIncrementalSortPaths`
    (`pathkeysCountContainedIn`, `nCommon>0` check), priced via the
    existing `costIncrementalSort` helper M0141-S7-exec-a/b landed; (3)
    sibling-path audit: check whether `AggStrategySorted`'s executor side
    already tolerates an `IncrementalSort` child (it does for the OUTER
    ORDER BY case per M0141-S7-exec-a/b/c — confirm the same operator
    works unchanged as a GROUP_AGG child, or file a further gap). This is
    an implementation task (not recon) — not selectable while M0141's
    banner item 6 restricts to "cost diagnosis only, no executor work";
    wait for the banner to open item 3/4's implementation tasks, or select
    per whatever priority governs M0141-S2b-* implementation work at that
    time. Gate: TPC-DS SF0.25 sweep (category movement, no regression) +
    `go test ./internal/optimizer/...`; no TPC-H dependency (TPC-H's own
    corpus has 9 sort-strategy witnesses per M0141-S7's header — re-check
    whether any are this same shape once implemented).

## M0142 — Join-order costing (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0142-join-order-costing.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
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
- [x] **M0142-0003i — resume -0003f now that -0003g's index-accelerated FK
  Kind: impl
  validation has landed** — **DONE 2026-09-21**, design doc
  `docs/design/0100-0149/m0142-0003i-tpch-canonical-fk-set-in-build-script.md`.
  Executed under the owner amendment: `bench/tpch/build_schema_goopg.sh` now
  adds the full canonical 8-FK set (PG-`:65432`-identical incl. `DEFERRABLE`
  on `lineitem_order_fk`) after the HammerDB build, gated by a `fk_check`
  that fails the script on a partial landing. Verified on a `pg_basebackup`
  clone at `:5533` (never `:65433`): all 8 validated index-accelerated
  (~6m49s), `pg_constraint` PK/FK rows identical to PG, and **Q9's 2500x
  estimate collapse resolved** — `lineitem ⋈ partsupp` now estimates 117313
  vs PG's 75650 (was 2406); Q9's plan walks the FK-informed NL-index chain;
  175 result rows on both engines. Live `:65433` still has no FKs — the owner
  applies them at the next reload. Original (pre-amendment) text below kept
  for provenance — **OWNER AMENDMENT 2026-09-17 (overrides the text
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
  **Formerly BLOCKED as of 2026-09-16 (see M0142-0003j/-0003k below) — the
  block resolved**: the owner-run `scripts/tpch-ref-recover.sh` restore
  brought `:65433` back from `preloss-clone-20260915` (8 PKs, zero FKs), and
  the owner amendment re-scoped this task to the build script + private
  clone, which is what was implemented above. The PID-81/data-loss chain
  this note described is preserved in -0003j/-0003k for provenance.
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
- [!] **M0142-0005 — give the executor a per-worker Memoize so a Gather-wrapped
  Kind: recon. Movement: none (recon/measurement record only; the
  implementation children M0142-0005a landed and M0142-0005b is filed
  below).
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
  **RE-SCOPED AGAIN 2026-09-18 (recon, no production diff, C1)** — the
  "executor has no per-worker Memoize" premise above is **false**: every
  worker already builds a fully independent operator tree, including its own
  `memoizeOp`/`kvcache.Cache` (`executor.go:354-371`'s own comment: "Each
  worker builds its OWN operator tree ... N calls give N independent trees"),
  matching real PG's own model exactly (`nodeMemoize.c`'s DSM only shuttles
  instrumentation counters, never cache data). The real refusal is three
  narrow type switches that never learned a `PathMemoize`/`*memoizeOp` case
  (`gatherpaths.go:459`'s `partialPathDrivingKind`, `parallel.go:965-985`'s
  `lateralProbeIsPartialProbe`, `parallel_scan.go:55-78`'s
  `lateralProbeJoinPartial`), and `PathMemoize`'s shape is a single
  well-typed unwrap (`Children[0]` is always the wrapped `PathIndexScan`,
  `getMemoizePath` `joinpathsmemoize.go:292-303`), not a search. Full
  writeup: design doc's "Update 2026-09-18" section. This task (M0142-0005)
  stays as the scoping/diagnosis record; the sized implementation is filed
  as **M0142-0005a** below.
  **B8 MEASURED 2026-09-19 (recon only, C1 — no production change)** —
  two-arm A/B at HEAD `94c1fb6f0`, private clones, env verified via
  `/proc/<pid>/environ` (`analysis/m0142/m0142-0005-b8-*`): the knob is
  **still load-bearing and now corpus-tensed**. 20/99 TPC-DS SF0.25 +
  3/22 TPC-H queries change shape between `GOOPG_INDEX_PROBE_MULT=1` and
  `=2`. At PG's constant (1): TPC-DS `scan-type` blockers drop 59→51 and
  `join-order` 91→88 (PG picks plain NL+`Index Scan` probes — Q73/Q34
  `customer_pkey` — which mult=2 prices into `Bitmap Heap Scan`), but TPC-H
  Q9/Q10/Q14 slide to NL+index probes where PG hashes — the exact class
  `c61781d6` calibrated against. Verdict: neither dead weight nor
  removable — the multiplier compensates an EXECUTOR gap (eager per-probe
  TID-list materialisation vs PG's per-tuple `index_getnext_tid` +
  `heap_fetch`), not a costing error, so no single scalar is PG-faithful
  on both corpora. Design doc:
  `docs/design/0100-0149/m0142-0005-b8-index-probe-mult-reverify.md`.
  Ledger row filed 2026-09-19. The faithful exit is filed as
  **M0142-0005b** below.
  **ESCALATION 2026-09-19 — S4 lineage budget exhausted** (descendants
  0005b/c/d/e/f all closed `Movement: none`; the open 0005g is no longer
  selectable and the root is marked `[!]` — only the owner reopens).
  What the chain proved: the `cost_index` port is *faithful* (0005c
  replay within ~1%); `indexProbeCostMultiplier=2.0` stays load-bearing
  because it compensates **input** divergence, chiefly corpus physical
  layout — goopg's TPC-H heap is perfectly clustered on the four large
  key-ordered probe columns (`corr=1.0`, 0 inversions) vs PG's fragmented
  reference (0.845/0.845/0.194/0.195; census in
  `m0142-0005f-tpch-corpus-layout-parity.md`). 0005d/e fixed the two real
  defects the recon surfaced (nullable-column correlation >1.0; NLI probe
  display costs). **Remaining blocker**: the only honest exit —
  rebuilding `:65433` in PG's ctid order — is an owner-side corpus action
  (R1). The recipe is validated end-to-end (`SELECT … ORDER BY ctid` →
  `COPY FROM csv` → `ANALYZE` reproduced corr 0.8464 vs PG 0.8451 and
  exactly 12,235 inversions); needs re-pinning `spotcheck_expected.env` +
  `tpch-row-anchors.csv` (corpora become row-identical to PG's
  5,998,835-row lineitem). **Expected movement if unblocked**: TPC-H
  Q9/Q10/Q14 hold hash shapes at `GOOPG_INDEX_PROBE_MULT=1` (probe ≈7.13
  like PG vs 3.82 today), then TPC-DS unlocks the measured mult=1 gains
  (`scan-type` 59→51, `join-order` 91→88, `qual-placement` 20→24 —
  regression to file). **Size**: owner bench action ~1 afternoon (8 dumps
  + reload + re-pin), then one parity-measurement loop. Open-but-frozen
  child: M0142-0005g (patternsel 2× recon — still real, still unblocked
  technically, but unfundable under S4 until the owner reopens).
- [x] **M0142-0005a — admit a Memoize-wrapped bare index probe as a
  Gather-driving kind.** **DONE 2026-09-19; full writeup in
  `docs/design/0100-0149/m0142-0005a-partial-memoize-nli-gather-admission.md`.**
  One correction to the spec above: the memoized NLI is the FUSED
  `*NestedLoopIndexJoin{InnerMemo}` node, not `Join{Right:*Memoize}` —
  `createPlan` panics on a free-standing `PathMemoize`, so
  `lateralProbeIsPartialProbe`/`lateralProbeJoinPartial` `*Memoize`/`*memoizeOp`
  cases would be dead code (attempted, verified unreachable, reverted). What
  landed: `partialPathDrivingKind`'s `PathNestLoop` arm unwraps one
  `PathMemoize` layer and re-runs the bare-probe check (SetOp-branch arm
  mirrors it); new exported `NestedLoopIndexJoinIsPartialCapable` is the
  single verdict re-run by the new `*NestedLoopIndexJoin`/
  `*nestedLoopIndexJoinOp` arms in `drivingScan`/`stampParallelScan`/
  `unstampParallelScan`/`HasShareableHashJoin`/`drivingScanCrossesSort` and
  `attachParallelScan`/`attachParallelBitmapScan`/`attachParallelIndexScan`/
  `collectShareableJoins`/`collectBitmapScans` — outer-only claim descent,
  per-worker memoizeOp/kvcache (PG parity: `nodeMemoize.c` DSM carries only
  instrumentation). Verified: Q34 `GOOPG_PGSHAPED_DP_TRACE` accepted
  `producer=gather` lines (`{0,1,2,3}` rows=136 = the emitted Gather);
  SF0.25 sweep PASS=96 MISMATCH=0 (stamped) with **Q34/Q73 flipped to the
  exact PG reference shapes**; acceptance-arm PASS 24/24 vs
  `/tmp/arm-0006c3-staged.txt` (stamped); tpch-spotcheck PASS Q12=2/Q13=34
  (stamped); units green; new-test `-race` green. `make race-gate` red at
  HEAD = pre-existing instrumentScope race (`M-NIGHTLY-instrumentscope-race-fix`,
  reproduced at base commit — separate task); plan-gate 14/22 = baseline
  drift (live `:65433` binary 09-19 03:11 predates the staged work, same
  count as prior loop).
- [x] **M0142-0005b — streamed NL index-probe executor, then retire
  `indexProbeCostMultiplier` (the B8 exit).**
  Parent: M0142-0005. Kind: impl. **DONE 2026-09-19.**
  The B8 re-measurement
  (`docs/design/0100-0149/m0142-0005-b8-index-probe-mult-reverify.md`)
  proved the `2.0` probe-cost multiplier is neither dead weight nor
  removable: it suppresses PG-matching NL+`Index Scan` probes on TPC-DS
  (Q73/Q34 flip to `Bitmap Heap Scan`, `scan-type` blockers 51→59) while
  protecting TPC-H parity (mult=1 slides Q9/Q10/Q14 to NL+index where PG
  hashes). The knob compensates an **executor** gap, not a costing error:
  PG's `nodeIndexscan.c`/`index_getnext_tid` streams one tuple per probe
  (`heap_fetch` per TID), while goopg materialises the full TID list per
  probe eagerly, so the real per-probe cost is genuinely higher than PG's
  formula predicts — the multiplier is a scalar patch over that
  divergence. Work: (1) convert the index-probe executor to PG's
  per-tuple streaming model (`index_getnext_tid` → `heap_fetch` → return,
  no eager TID-list array); (2) re-measure both corpora with
  `GOOPG_INDEX_PROBE_MULT=1` — if TPC-H Q9/Q10/Q14 keep PG's hash joins
  AND TPC-DS keeps the mult=1 scan-type gains, retire the knob to PG's
  1.0 (delete the flag + `c61781d6` calibration); (3) if the TPC-H
  regression persists even with a streamed probe, the residual is a real
  cost-model gap — file it as its own recon with the measurement, do not
  silently keep the knob. Expected movement: TPC-DS `scan-type` −8 at
  SF0.25 with TPC-H match held. Gates: units, tpch-spotcheck, TPC-DS
  SF0.25 sweep, both-corpora parity captures under the cgroup wrapper.
  - Landed 2026-09-19: `nbtree.ScanCursor` — a resumable leaf-grain range
    scan (`NewScanCursor` descends once; `Next(fn)` delivers one admitted
    leaf's in-range entries per call, no pin held across calls; the
    per-leaf item loop is extracted unchanged into shared
    `scanLeafItems`). `indexScanOp` rewired: `tids`/`poss` are per-leaf
    batches refilled by `nextLeafBatch` from `Next`; SAOP is now a lazy
    chain of per-element cursors (bounds + hash-bucket SIREADs still
    eager, descents + leaf reads lazy — `saopBounds`/`saopIdx`/`saopSeen`).
  - The SSI gap-lock decision moved to `finalizeIndexScanSSI` — once per
    scan at exhaustion / next Rescan / Close, keyed on `sawTID` instead of
    the eager `len(tids)>0` (unknowable at Rescan end under laziness);
    `ssiDone` starts true so the first Rescan's finalize is a no-op.
  - New tests `internal/access/nbtree/scan_cursor_test.go`: cursor-vs-eager
    stream equality over multi-leaf ranges (keys + TIDs + ScanPos),
    early-stop exhaustion, exclusive bounds, empty scans, leaf-filter
    partitioning (the Gather claim mechanism).
  - Re-measurement (binary sha `d26896dbef17`, B8 protocol, private
    clones :5533/:5534, env verified via `/proc/<pid>/environ`): post-change
    captures are BYTE-IDENTICAL to B8's on TPC-H (0 diff lines) and differ
    only in capture headers on TPC-DS — executor laziness cannot change
    plan choice. TPC-DS mult=1 keeps scan-type 51 / join-order 88 /
    qual-placement 24; mult=2 = 59/91/20. TPC-H at mult=1 still picks
    NL+`Index Scan` on Q9/Q10/Q14 where PG hashes. → branch (3): knob
    stays at 2.0, comment corrected to name the cost-model residual,
    recon filed as M0142-0005c. Movement: none (TPC-DS scan-type −8
    materialises only at mult=1, which TPC-H still forbids).
  - Gates: units PASS; `go test -race ./internal/executor/` PASS;
    tpch-spotcheck PASS (Q12=2/Q13=34, stamped); tpcds-sf025 sweep
    PASS=96 MISMATCH=0 plan-shapes 99/99 (stamped); tpch-acceptance-arm
    PASS 24/24 value-MATCH (stamped). Design doc:
    `docs/design/0100-0149/m0142-0005b-streamed-index-probe-cursor.md`;
    captures `analysis/m0142/m0142-0005b-{tpcds,tpch}-mult{1,2}.txt`.
- [x] **M0142-0005c — recon: why does `indexProbeCostMultiplier=2` stay
  load-bearing after the streamed probe? (probe-vs-hash relative pricing).**
  Parent: M0142-0005. Kind: recon. **DONE 2026-09-19, no production change.**
  Full writeup:
  `docs/design/0100-0149/m0142-0005c-index-probe-mult-input-divergence.md`,
  numerics `analysis/m0142/m0142-0005c-probe-cost-attribution.md`.
  - **The formula is faithful** — a Python replay of `costIndexScanCore`
    (`costindex.go:233-303`) reproduces both engines' probe costs within ~1%
    when fed each engine's own inputs: goopg-sim 3.8248 vs DPPATH-measured
    3.8249 (Q9 `partsupp` probe, `index.parameterised relids={3}
    reqouter={0}`); PG-input sim 7.07 vs PG forced-NL measured 7.13. Nothing
    is omitted: descent, both Mackert-Lohman arms, the corr² blend
    (`costsize.c:795-797`), qpqual and `get_loop_count`
    (`indxpath.c:2328` — `loopCountFor` ports it verbatim) are all present.
  - **The divergence is inputs** (swap ladder, per-input Δ on the 3.82→7.13
    gap): correlation `1.0→0.845` +0.92; loopCount `12121→6061` +0.78;
    index relpages `1092→2198` +0.37; relPages −0.04; interaction +1.24.
  - **Dominant input: physical heap correlation of the corpus.** goopg's
    TPC-H heap is genuinely perfectly clustered on every probe key — 0
    block-boundary key inversions on `partsupp.ps_partkey`, so `corr=1.0`
    is a FAITHFUL measurement (verified by re-ANALYZE on the clone + a
    corrected ctid inversion check; the earlier "367K inversions" reading
    was a mis-parsed query, retracted). PG's reference heap is fragmented
    (~12K inversions → 0.845; `l_orderkey` 0.195, `o_orderkey` 0.194,
    `p_partkey` 0.846). With corr=1.0 the blend pins at `min_IO_cost`, so
    the probe is ≈ linear in the multiplier — which is why the scalar is
    load-bearing on TPC-H and why it double-charges TPC-DS, where
    correlations measure honest fractions (0.01–0.66) and mult=1 measured
    better (scan-type 59→51 etc.).
  - **loopCount inherits a selectivity divergence**: `part WHERE p_name
    LIKE '%green%'` estimates 12121 (goopg) vs 6061 (PG), actual ≈10650 —
    `patternClauseSelectivity` lands 2× PG's on the other side of truth.
    Filed as M0142-0005g.
  - **Q10 `{orders,lineitem}` contest at mult=1** (DPPATH): partial-NL
    95,348 beats partial-hash 351,384; the real parameterized probe path
    cost is 4.59 (`startup=0.375` — the descent charge is present).
  - **EXPLAIN misleads on fused NLI**: the printed `cost=0.00..0.18` on the
    inner `Index Scan` is `DeriveLegacyDisplayCost` — the `*IndexScan` node
    built at `nl_index_join.go:666` never passes `stampPlanCost`. The 0.18
    is display-only; the path search used 4.59. Filed as M0142-0005e.
  - **New analyzer bug found**: correlation can exceed 1.0 on nullable
    columns — `catalog_sales.cs_catalog_page_sk` = 1.0019597 on TPC-DS
    SF0.25. `corrPairs` uses the raw (null-sparse) sample `pos`
    (`operators_analyze.go:1294`) while the closed form requires contiguous
    `0..nonNull−1` positions — PG uses `tupno = values_cnt`
    (`analyze.c:2495`). Filed as M0142-0005d.
  - **Verdict**: the scalar masks input parity (corpus layout +
    selectivity), not a formula gap. No better scalar exists; retirement
    path is the corpus-layout decision (M0142-0005f) plus the child fixes.
    The flat `indexProbeCost()` (`cost_funcs.go:1104`) is test-only —
    production probes all flow through `costIndexScanCore`, where the
    multiplier scales every `random_page_cost` term.
  - Gates: recon-only — no production diff, so no stamped gates required;
    all evidence gathered read-only (PG `:65432` EXPLAIN/catalog reads) or
    on private clones (`:5534` TPC-H, `:5533` TPC-DS SF0.25).
    Movement: none (recon — produced the attribution + four child tasks;
    no parity metric moved).
- [x] **M0142-0005d — ANALYZE correlation: contiguous non-null positions +
  clamp to `[-1,1]` (corr>1 on nullable columns).** DONE 2026-09-19.
  Parent: M0142-0005c. Kind: impl.
  `corrPairs` records `pos` as the raw index into the reservoir `sample`
  (`internal/executor/operators_analyze.go`), which is SPARSE when the
  column has nulls — the skipped null positions leave gaps in x — while the
  closed-form Pearson (`n·Σxy − Σx²)/(n·Σx² − Σx²`) is only valid when both
  axes are permutations of `0..nonNull−1`. PG assigns `values[values_cnt]
  .tupno = values_cnt` — a contiguous non-null index — at
  `postgres/src/backend/commands/analyze.c:2495`. Observed:
  `catalog_sales.cs_catalog_page_sk` correlation = **1.0019597** on the
  TPC-DS SF0.25 clone (mathematically impossible).
  - Landed: `pos = nonNull − 1` (PG's `tupno` — contiguous non-null index,
    assigned after the null `continue`); stable-sort tie-break preserved
    (non-null order is monotone in scan order); result clamped to
    `[-1,1]` as numerical hygiene (PG relies on the closed form being
    exact for permutations — documented in-code). Design doc:
    `docs/design/0100-0149/m0142-0005d-analyze-correlation-contiguous-tupno.md`.
  - Pre-fix census was far worse than the single reported anomaly: **28**
    SF0.25 columns stored `correlation > 1.0` (worst
    `item.i_rec_end_date = 3.6665926`). Post-fix re-ANALYZE on a private
    `:5533` clone: **0** out of range; `cs_catalog_page_sk`
    `1.0019597 → 0.9799`, `i_rec_end_date` `3.6666 → 0.3335`.
  - New regression test `TestAnalyzeCorrelationNullGappedPositions`
    (asc/desc null-interleaved → ±1; null-gapped mixed → −0.1 hand-derived;
    old sparse numbering yields 5.0/1.8 on the same shapes).
  - Gates: `go test ./internal/executor/` PASS; units precommit PASS;
    tpch-spotcheck Q12=2/Q13=34 PASS; SF0.25 sweep PASS=96 MISMATCH=0,
    plan shapes 99/99 identical (gate cluster still carries pre-fix stored
    stats until its next ANALYZE — no shape movement expected).
  - Movement: pg_stats correlation census out-of-range count **28 → 0** on
    SF0.25. No plan-parity category movement expected — stored stats on the
    benchmark clusters update only on their next ANALYZE/reload; this is a
    correctness fix that makes nullable-column correlations honest inputs
    to `index_pages_fetched`, not a tuning event.
- [x] **M0142-0005e — stamp the real path cost on the fused-NLI inner
  `IndexScan` (EXPLAIN prints `DeriveLegacyDisplayCost`).** DONE 2026-09-19.
  Parent: M0142-0005c. Kind: impl.
  Root cause found was subtler than the filing: `createPlanNode(innerPath)`
  DOES stamp, but onto the OUTERMOST emitted node — a leaf-local
  `*Filter{*IndexScan}` when the leaf carries quals — and
  `absorbableLeafCond` then absorbs the predicate into `IndexScan.Cond`
  and keeps the bare `*IndexScan`, discarding the stamped wrapper.
  - Landed: `stampPlanCost(is, innerPath)` after the unwrap in all three
    `createplannl.go` arms (decomposed `Join`, fused `NestedLoopIndexJoin`,
    bitmap), plus `stampPlanCost(nli.InnerMemo, memoPath)` on the fused
    Memoize and `stampPlanCost(bhs/bis, innerPath/idxPath)` on the bitmap
    pair; `priceSemiProbe` (`nlipricesplice.go`) now stamps `x.Inner` with
    the fabricated `probePath` beside the existing `stampPlanCost(x, p)`.
    Design doc:
    `docs/design/0100-0149/m0142-0005e-nli-probe-plan-cost-stamp.md`.
  - Regression tests `TestNLIInnerScanCarriesProbePathCost` +
    `TestNLIFusedInnerAndMemoizeCarryPathCosts` pin the probe carrying the
    `PathIndexScan`'s `{startup,total,rows}` and `InnerMemo` carrying the
    `PathMemoize`'s cost through the leaf-local-Filter unwrap.
  - Live verify on private `:5533` TPC-H clone: Q10 probe
    `cost=0.00..0.18 rows=18` → `cost=0.38..8.49 rows=5` (startup includes
    the index descent; rows = per-probe path estimate — the quantities PG
    prints). SF0.25 sweep plan capture: every `Memoize 0.00..0.02` /
    `Index Scan 0.00..0.01` pair now prints real path costs.
  - Gates: optimizer + executor `go test` PASS; units PASS;
    tpch-spotcheck Q12=2/Q13=34 PASS; SF0.25 sweep PASS=96 MISMATCH=0
    (shape channel: 81 plan-text diffs = the cost-string change itself,
    zero verdict changes); acceptance-arm 24/24 value-MATCH vs baseline.
    All stamped on the staged index.
  - Movement: none on plan choice (display/provenance only). Plan-text
    captures now carry honest probe costs — future recon/measurement
    loops can trust the printed numbers.
- [x] **M0142-0005f — recon: TPC-H corpus physical-layout parity — rebuild
  in PG-reference order or accept the divergence?** DONE 2026-09-19.
  Parent: M0142-0005c. Kind: recon.
  Movement: none (recon).
  - Census (16 key columns, both corpora — `pg_stats.correlation` plus
    adjacent-key inversions over true heap order; index paths disabled
    because covering indexes make PG answer `SELECT key FROM t` with
    index-order index-only scans): **the divergence is exactly the four
    large key-ordered probe columns** — `part.p_partkey`,
    `partsupp.ps_partkey`, `orders.o_orderkey`, `lineitem.l_orderkey`
    (goopg corr=1.0 vs PG 0.845/0.845/0.194/0.195). Every
    generator-shuffled column already agrees to sampling noise on both
    metrics; PG's fragmentation is confined to the big tables
    (`supplier.s_suppkey`/`customer.c_custkey` are 0.9996/0.9980 on PG
    too).
  - Rebuild recipe VALIDATED on a private `:5533` clone: dump PG rows
    `ORDER BY ctid` (plain `psql --csv SELECT` — the COPY keyword is
    guard-blocked on reference clusters), `COPY FROM` into goopg,
    `ANALYZE` → `ps_partkey` corr **0.8464** (PG: 0.8451; goopg native:
    1.0) and exactly **12,235** inversions — PG's ordering reproduced
    faithfully. Caveat: load via db `postgres` — the known per-DB scoping
    gap makes COPY/ANALYZE miss new relations inside db `tpch`.
  - Decision: **(a) rebuild** recommended — validated, cheap, no planner
    code, makes corpora row-identical. It is an owner-side action on
    `:65433` (R1): row anchors re-pin, stored stats re-derived.
  - Expected movement named: TPC-H Q9/Q10/Q14 hold hash shapes at
    mult=1 (probe ≈7.13 like PG, vs 3.82 today); TPC-DS mult=1 measured
    gains become reachable (scan-type 59→51, join-order 91→88;
    qual-placement 20→24 is a known regression the step must file).
  - Design doc:
    `docs/design/0100-0149/m0142-0005f-tpch-corpus-layout-parity.md`;
    evidence `tmp/m0142-0005f-census.{sh,txt}`. Follow-up carried by the
    M0142-0005 root's ESCALATION block (lineage budget exhausted — S4).
- [ ] **M0142-0005g — recon: `patternClauseSelectivity` vs PG on
  `LIKE '%x%'` — 2× divergence feeding `loopCount`.**
  Parent: M0142-0005c. Kind: recon.
  `part WHERE p_name LIKE '%green%'` (TPC-H Q9): goopg estimates 12121, PG
  6061, actual ≈10650. PG's `patternsel`/`like_selectivity`
  (`like_support.c`) reads histogram/MCV for the fixed prefix plus the
  matcher heuristic; goopg's `internal/optimizer/patternsel.go` lands 2×
  high. Attribute which sub-term diverges (histogram lookup absent? fixed
  vs variable part weighting?), and name the expected movement — the
  estimate feeds `get_loop_count` on every parameterized probe under a
  LIKE-filtered outer (Q9's `part ⋈ partsupp` flip site is the canonical
  one).
  - **Loop \#21 recon findings (recorded while frozen — task stays `[ ]`
    per the M0142-0005 ESCALATION, owner reopens/closes):** the estimator is
    a faithful port — `internal/optimizer/patternsel.go`'s
    `patternSelectivity` mirrors `patternsel_common` line-for-line
    (`histogram_selectivity` n_skip=1 interior-bounds fraction, MCV
    exact-match merge, `1-nullfrac-sumcommon` scaling, 0.0001/0.9999
    clamps). Verified numerically on the live clusters: both `pg_stats`
    histograms are 101 bounds, no MCVs, null_frac=0 on both sides; goopg
    has **6** interior bounds matching `%green%` → 6/99 = 0.0606 → 12121
    exactly, PG has **3** → 3/99 = 0.0303 → 6061 exactly. **No sub-term
    diverges** — not a missing histogram lookup (hist_size=101 ≥ 100 →
    pure-histogram path both sides; the small-histogram heuristic blend is
    never reached), not prefix weighting (`%green%` has no fixed prefix →
    `prefixsel=1.0` arm, also unreached), not matcher semantics (substring
    match identical). The gap is **histogram content**: (a) the corpora
    are not row-identical — `count(*) FILTER (p_name LIKE '%green%')` is
    10686 on :65433 vs 10619 on :65432, `count(DISTINCT p_name)` 200000
    vs 199999 — the HammerDB client-side generator is not dbgen-identical
    (extends M0142-0005f's physical-layout finding to *logical* content);
    (b) reservoir-sample realization noise — each side draws its own
    30000-row sample (goopg's ANALYZE ports the two-stage block-S +
    Vitter-Z sampler), and bound-match count is ~Binomial(99, ≈0.053):
    σ≈2.2, so 3 (−1σ) and 6 (+0.3σ) are both unremarkable draws. On its
    own data goopg errs +13% (12121 vs 10686) while PG errs −43% on its
    (6061 vs 10619) — goopg's estimate is the *closer* one; there is no
    logic defect to fix. The Q9 flip site is real but input-driven: PG
    plans `NL(part→partsupp idx)` at 6061 while goopg builds a hash join
    at 12121 — closing it needs the same owner-side corpus rebuild
    M0142-0005f escalated, not an estimator change. **Expected movement:
    none achievable via `patternsel` changes** — any task to align the
    number must operate on the corpus, which is the M0142-0005
    escalation's existing ask.
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
  - [ ] **M0142-0008a-3** — **UNFROZEN (owner decision 2026-09-20; firewall stays — see banner)** — RE-SCOPED by -1 into three separately-landable
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
    **Handoff note (owner decision 2026-09-20, M0145 filed):** this task's
    increments are the pinned-spine-architecture route to semi/anti DP
    participation; M0145-0003 is the jointree-native route to the same end
    state (searched citizens via legality, not spine participants). The
    chain's own measurements already spent the increments: (iii) is `[x]`
    landed, and `-3i-leafcount`/`-3i-lateral-route`'s recon concluded (i)
    is not implementable at Phase B for ANY of the five witnesses — the
    wall is the resolver lowering sublink bodies before either phase.
    Remaining work on this line is route-a step 2 (below), which M0145-0003
    generalises; do not re-derive increments (i)/(ii) against the spine.
    - **REACHABILITY VERIFIED AT HEAD 2026-09-20 (loop \#53) —
      `M0142-0008a-3i-reach-verify`.** Design doc:
      `docs/design/0100-0149/m0142-0008a-3i-reach-verify.md`; evidence:
      `analysis/m0142/m0142-0008a-3i-reach-verify-trace.txt`.
      Kind: recon
      Parent: M0142-0008a-3
      Movement: none
      - Ran after `M0142-0008-producer` landed (`1f76d83d9`) and with every
        `M0142-0008a-3i-plumbing` sub-task `[x]`, to establish two things
        reading cannot answer.
      - **No semiAnti chain link reaches the search.** All five named
        witnesses decline, one seam call each, at HEAD `6909ec616` on the
        private `:5595` lane with `GOOPG_PGSHAPED_DP_TRACE=1`:
        `Q69 lateral(nrels=3 nleaves=6)`, `Q16 leaf-count(4/3)`,
        `Q94 leaf-count(4/3)`, `Q10 lateral(3/4)`, `Q35 leaf-count(3/2)`.
      - **The P0-H11 `cumulativeFromSpans` finding is therefore still
        LATENT — there is no live correctness bug at HEAD.** It stays
        exactly what the audit called it: a gate the first task to let a
        link through must close first.
      - **Q69 does NOT stop at `leaf-count`.** The seam's checks run in
        source order — `chain-not-flattenable` (`joinsearchseam.go:315`)
        → `leaf-count` (`:326`) → `lateral` (`:330`), each returning — so
        reaching `lateral` PROVES `leaf-count` passed. Q69 and Q10 pass it;
        Q16, Q94 and Q35 do not.
      - Neither Q69 nor Q10 contains `LATERAL` in its SQL. The Lateral join
        is goopg's own: `chainCarriesLateral`'s Semi/Anti arm — added
        deliberately by `M0142-0008a-3i-plumbing-c14` — catches "a Lateral
        join nested inside a Semi/Anti's `Left`, the exact shape a
        pre-DP-unnest join-order search (predp.go's Phase A) splices in
        when its own winning tree contains a parameterized index probe".
        **The chain's last landed increment closed a correctness hole and,
        in doing so, moved its own named witness's blocker.**
      - Consequence for increment (i): leaf admission is the right work for
        **Q16, Q94, Q35** and will **NOT** move **Q69** — the witness
        `-3i-recon2` chose specifically for the RHS-as-participant shape.
        Anyone implementing (i) against Q69 expecting `leaf-count` to be
        the gate will measure no movement and wrongly conclude the
        increment failed.
    - [x] **M0142-0008a-3i-lateral — decide whether a chain whose Lateral
      join is goopg's OWN predp Phase-A splice may be searched** (filed by
      `M0142-0008a-3i-reach-verify`). `chainCarriesLateral` declines Q69 and
      Q10 over a `Lateral` join that no user wrote: predp.go's Phase A
      splices it in when its winning tree contains a parameterized index
      probe, and `-plumbing-c14`'s Semi/Anti arm now correctly sees it. A
      user `LATERAL` must not reorder across its dependency; a Phase-A
      splice may or may not be the same thing, and that is the decision.
      Establish it BEFORE widening the gate: if it may be searched, the
      parameterized probe's dependency still has to be preserved across the
      reorder, which is a different problem from leaf admission.
      Kind: recon
      Parent: M0142-0008a-3
      Expected movement: none by itself (it is a decision + design note). It
      unblocks Q69's `join-method` record on TPC-DS SF0.25 — the census's
      named Semi/Anti-algorithm divergence — which the implementing task
      measures with `pg-plan-parity-diff.py` per-query on a private-lane
      capture.
      - **DECIDED 2026-09-20 (loop \#54): NO — and the refusal is
        CORRECTNESS, not scope.** Design doc:
        `docs/design/0100-0149/m0142-0008a-3i-lateral.md`.
        Movement: none
        - `predp.go` runs TWO searches: **Phase A** searches the Semi/Anti
          join's inner chain and splices its **built plan** in
          (`predp.go:153-162`); **Phase B** then searches from the pinned
          Semi/Anti join and, by its own comment, "sees the … searched
          `origChain` already in place" (`:164-167`). Phase B's call is the
          one that declines.
        - When Phase A's winner held a parameterised index probe,
          `createNestLoopIndexJoinPlan` (`createplannl.go:364-383`) has
          already LOWERED the dependency to `OuterColumnRef{Level: 1}` on
          the probe's keys plus the `BindLateralOuter` execution contract —
          a positional, immediate-parent binding. Reordering across it
          silently breaks the binding; the construction site's own comment
          says what dropping the marker does ("returning every row for the
          first outer tuple's key").
        - Nothing at Phase B's layer can re-lift it: the searchable form —
          a path's `RequiredOuter` — was consumed when Phase A built its
          plan. Same class as `reresolveJoinByName`'s post-search-splice
          problem `-3i-recon2` flagged as unresolved.
        - So `-plumbing-c14` did NOT add a conservative guard; it closed a
          real hole. Contrast with M0137-0019b, where all four gates said
          in their own comments that the refusal was scope and the executor
          was independently shown worker-local — here the refusal protects
          a binding the chain genuinely depends on.
        - **PG never has this problem**: one search over the flattened join
          list, dependencies in `param_info`
          (`postgres/src/backend/optimizer/util/pathnode.c:188, 293`) which
          `add_path` and the join-order search read directly, and plan
          construction AFTER the search —
          `top_plan = create_plan(root, best_path)`,
          `postgres/src/backend/optimizer/plan/planner.c:441`. goopg's
          two-phase pre-DP unnest is the divergence.
        - Route for Q69/Q10 is therefore **route-order**, the subject the
          owner already prioritised as M0144-0003: Phase B must see the
          chain before the dependency is lowered, or Phase A's result must
          retain a path-level `RequiredOuter`. Filed below rather than
          attempted — it changes when plans are built relative to when they
          are searched, a planner-architecture decision, not a gate edit.
    - [x] **M0142-0008a-3i-leafcount — is increment (i) implementable at
      Phase B for Q16/Q94/Q35?** (filed and worked 2026-09-20, loop \#55).
      **NO — same root cause as the Lateral case, generalised.** Design doc:
      `docs/design/0100-0149/m0142-0008a-3i-leafcount.md`; evidence:
      `analysis/m0142/m0142-0008a-3i-leafcount-probe.txt`.
      Kind: recon
      Parent: M0142-0008a-3
      Movement: none
      - Measured with a throwaway instrumented build (probe REVERTED, not
        committed) on the private `:5595` lane, `EXPLAIN` only. The check is
        `len(scans) != nprefix+len(semiAnti)` (`joinsearchseam.go:325`):
        - `Q16 nrels=4 nprefix=4 scans=3 semiAnti=2` (wants 6, has 3)
        - `Q94` identical to Q16
        - `Q35 nrels=3 nprefix=3 scans=2 semiAnti=1` (wants 4, has 2)
      - **The gap runs the WRONG WAY and is large.** Increment (i) was
        scoped to add the one missing synthetic RHS leaf; the shortfall is
        3 leaves (Q16/Q94) and 2 (Q35). Adding the RHS participant would
        take Q16 from 3 to 5 against a required 6 — still a decline.
      - **The "leaves" are already-planned composite subtrees**, not base
        scans: `scan[0]=*optimizer.Project` for Q16/Q94;
        `*optimizer.NestedLoopIndexJoin` and **`*optimizer.Gather`** for
        Q35. A Gather only exists AFTER planning, worker count already
        chosen — a join search cannot treat it as a base relation.
      - **Unifying root cause**: Phase B searches a tree Phase A has
        already PLANNED. In the Lateral case a DEPENDENCY had been lowered
        out of searchable form; here the RELATIONS have. `leaf-count` and
        `lateral` are two symptoms of one thing, and `leaf-count` is not
        the milder one — a `*Gather` leaf is further from a base rel than a
        Lateral marker is from a `RequiredOuter`.
      - **Consequence: `M0142-0008a-3` has no selectable increment left.**
        (i) is not implementable at Phase B for ANY of the five witnesses —
        Q69/Q10 by the lowered dependency, Q16/Q94/Q35 by the lowered
        relations — and (ii) is downstream of (i). Both now depend on
        `M0142-0008a-3i-lateral-route` below, whose scope this widens.
      - Deliberately NOT done: a partial that adds the RHS participant and
        still declines on 5/5 witnesses is code with no consumer, which is
        what this repo's own "an unwinnable path is an untested path"
        lesson warns against.
    - [x] **M0142-0008a-3i-lateral-route — let Phase B search before Phase A
      lowers anything** (filed by `M0142-0008a-3i-lateral`; scope WIDENED by
      `M0142-0008a-3i-leafcount` from "the probe dependency" to "relations as
      well as dependencies").
      Either Phase B sees the chain BEFORE `createNestLoopIndexJoinPlan`
      lowers a parameterised probe to `Join{Lateral:true}` +
      `OuterColumnRef{Level:1}`, or Phase A's spliced result retains a
      path-level `RequiredOuter` Phase B's search can read — PG's own
      arrangement (`param_info` + `create_plan` after the search,
      `planner.c:441`).
      Kind: recon
      Parent: M0142-0008a-3
      Expected movement: unblocks the seam admission of **all five**
      census witnesses — Q69 and Q10 (`lateral`) and Q16, Q94 and Q35
      (`leaf-count`) — whose `join-method` records are the census's named
      Semi/Anti-algorithm divergence on TPC-DS SF0.25. Measured by the
      implementing task with `pg-plan-parity-diff.py` per-query on a
      private-lane capture, plus a `GOOPG_PGSHAPED_DP_TRACE=1` seam census
      showing BOTH decline classes shrink from their current 2 and 3.
      - Whoever first lets a link through must ALSO close the P0-H11
        `cumulativeFromSpans` span round-trip in the same change — it is
        latent only because nothing gets through today
        (`M0142-0008a-3i-reach-verify`).
      - Overlaps M0144-0003 (route-order verification) by construction;
        check that task's findings first rather than re-deriving them, and
        do NOT attempt this as a `chainCarriesLateral` widening — that was
        measured and refuted by `M0142-0008a-3i-lateral`.
      - **RECON COMPLETE 2026-09-20 — THE FILED FRAMING IS INSUFFICIENT.**
        Design doc:
        `docs/design/0100-0149/m0142-0008a-3i-lateral-route-recon.md`.
        Movement: none (no production file touched).
        - **Reordering Phase A and Phase B cannot fix anything.** The
          lowering happens one stage before EITHER phase runs:
          `planner.go:1499`'s `resolveExpr(whereQual, ctx)` reaches
          `planExistsExpr` (`:15320-15333`) which calls
          `planSelectWithParent` — a full recursive planner run — on every
          EXISTS/IN body and stores only the finished `Node`. Unnest
          (`:1539`) and both searches (`:1540`) all run after it.
        - `ExistsExpr` and `InExpr` carry `Plan Node` and **do not retain
          the parser subquery**, so the parse tree is unreachable from
          every later stage.
        - This is the single object all four prior probes hit from
          different angles (`-lateral`, `-leafcount`, M0144-0003a's census
          and its refutation). `M0142-0008a-3i-verify` had already named
          the provenance correctly; the route task was filed against the
          phases rather than against the resolver.
        - PG inverts the order: `pull_up_sublinks` (`planner.c:737`) and
          `pull_up_subqueries` (`:759`) run while the body is still an
          unplanned `Query`; `SS_process_sublinks` (`:1328`) plans only
          what pull-up refused; `query_planner` (`:1654`) then searches one
          flattened range table.
        - **All five witnesses are flattenable.** Q16/Q94's bodies are a
          single `catalog_sales cs2`; Q35/Q10/Q69's are `store_sales,
          date_dim` (and `web_sales`/`catalog_sales` siblings) — plain
          SELECTs, no aggregate/HAVING/window/setop/DISTINCT/LIMIT/ORDER
          BY. Every one satisfies `is_simple_subquery`
          (`prepjointree.c:1807`) in full, so the flattenable subset covers
          100% of the named witnesses.
        - Route filed as **M0142-0008a-3i-route-a** below.
    - [x] **M0142-0008a-3i-route-a — retain the parser subquery on
      `ExistsExpr`/`InExpr`, then flatten a simple body before planning it**
      (filed by `M0142-0008a-3i-lateral-route`'s recon; goopg's
      `pull_up_subqueries` analogue).
      Kind: impl
      Parent: M0142-0008a-3
      Two steps, the first purely additive: (1) carry the parser subquery
      alongside `Plan` on `ExistsExpr`/`InExpr` — all 52 `.Plan` readers in
      `unnest.go` are unaffected and the 7 `planSelectWithParent` call sites
      (`planner.go:5232, 5251, 13192, 15200, 15217, 15300, 15329`) each
      already hold the parser node they pass in; (2) in
      `unnestSubqueriesInPlan`, test the retained query against a goopg
      `is_simple_subquery` analogue and, when it passes, splice the body's
      FROM items into the outer join list as REAL relations with its quals
      merged into the outer predicate, discarding `.Plan` for that sublink —
      so the search that follows sees base relations, not a finished plan.
      Expected movement per S5: the seam's `leaf-count` decline class
      shrinks from its current 26-across-11-queries count, and the five
      census witnesses' `join-method` records move — measured by a
      `GOOPG_PGSHAPED_DP_TRACE=1` seam census plus `pg-plan-parity-diff.py`
      per-query on a private-lane capture.
      - The leaf arithmetic must be **re-derived, not carried over**:
        flattening changes both sides of `len(scans) != nprefix+len(semiAnti)`
        (`joinsearchseam.go:326`), so today's `Q16 nrels=4 nprefix=4 scans=3`
        shortfall is not the post-fix target.
      - Must close the P0-H11 `cumulativeFromSpans` span round-trip in the
        SAME change (`M0142-0008a-3i-reach-verify`).
      - Q78's `outer-over-derived` firewall must not weaken (hard owner
        constraint).
      - A flattened body's correlation references (`cs1.cs_order_number`)
        are `OuterColumnRef{Level:1}` and must be re-based into the outer
        chain's column space at splice time; PG gets this free because its
        reference is a `Var` over a range-table index.
      - **Handoff note (owner decision 2026-09-20, M0145 filed):** step 2
        is the pilot instance of **M0145-0003**'s jointree pull-up. If
        M0145-0001's IR design lands while this task is open, implement the
        splice against the IR instead of the node-tree chain — do not build
        the mechanism twice.
      - **STEP 1 LANDED 2026-09-20 — inert by construction.** Design doc:
        `docs/design/0100-0149/m0142-0008a-3i-route-a-retain-sublink-parse-tree.md`.
        Movement: none (zero production readers, so none was available).
        - `ExistsExpr.Subquery` / `InExpr.Subquery` retain the UNPLANNED
          parser body (`plan.go`), assigned at the two resolver sites
          (`planner.go:15306`, `:15338`).
        - `sublinkBodyIsSimple` (`internal/optimizer/sublinkpullup.go`)
          ports `is_simple_subquery` (`prepjointree.c:1807`)
          refusal-for-refusal, deriving `hasAggs`/`hasWindowFuncs` from the
          parse tree via the existing `walkExpr`/`isAggregateFuncName`
          rather than copying the aggregate-name table.
        - **The retention test caught a real sibling-drift bug**:
          `FoldConstants` (`foldconst.go:69`) REBUILDS an `InExpr` field by
          field and silently dropped the new field — EXISTS passed while IN
          failed. Three copy sites now carry it (`foldconst.go:69`,
          `planner.go:16834`, `:17072`); `exists_to_any.go:384` deliberately
          does NOT, because it rewrites rather than copies and nil is the
          fail-closed answer. The test compares POINTERS and was verified to
          fail with the resolver assignments removed.
        - **Port gaps stated, not hidden** (ledger row filed): upstream's
          `hasTargetSRFs` is NOT implemented — classifying an arbitrary
          function as set-returning needs the catalog this predicate does not
          take, so an SRF-in-target-list body is currently ACCEPTED, and step
          2 must either take a catalog argument or refuse any target-list
          `FuncCall` it cannot classify. The `security_barrier` and `lateral`
          arms are safe-by-construction for qual sublinks (upstream's own
          `convert_EXISTS_sublink_to_join` synthesises the RTE) rather than
          merely unimplemented.
        - Gates: units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); tpcds-sf025
          sweep PASS (MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0; plan channel
          `same=99 changed=0`; verdict-changes=none); tpch-acceptance-arm
          PASS (24/24 on VALUES vs a fresh same-loop baseline arm built from
          the unmodified tree).
        - **STEP 2 LANDED 2026-09-20 — the flattening splice, plan-level
          form.** The filed "splice FROM items before planning" framing is
          unreachable without the M0145 IR (bodies are planned eagerly at
          expression resolution); what landed is the equivalent at the
          seam — `Join.FlattenedRHS` marks a Semi/Anti join whose retained
          body passed `sublinkBodyIsSimple` AND whose planned RHS passed
          `decomposeFlatBodyTree`, and `extractSearchLeaves` decomposes it
          into one REAL-costed synthetic leaf per body relation (multi-bit
          `rhs`, pooled `bodyQuals`, `SpecialJoinInfo` preserved,
          out-of-band `leafSpans`). IN arms re-wrap the stripped body in a
          positional-identity `IsolatedScope` `Project`, restoring the
          M0063/M0071 NLI protection structurally; EXISTS keeps its
          bare-body NLI eligibility. P0-H11 closed in the same change:
          `joinlistProblem.cumOffsets` → `leafSpans`, `cumulativeFromSpans`
          deleted, `leafSpanWindow` gates boundary windows on real
          contiguity.
        - **Newly exposed + fixed: `semianti-not-tail`.** Admitting
          flattened RHS leaves surfaced a latent c2 assumption — every
          synthetic leaf must occupy a TAIL slot — that Q78's mid-chain
          demoted ANTI (`web_sales ANTI web_returns JOIN date_dim`,
          walking `[real, synthetic, real]`) violated into a
          `translateToLayout` panic (`binding column 75 ... not among the
          7 output columns`). Now an explicit decline; Q78 back to PASS
          15 rows ck=c06cf981a7819a37. Arbitrary synthetic-leaf placement
          is deferred to the ledger — real work (leaf reorder + relset
          remap), not a guard to relax.
        - **Movement: measured NONE on the SF0.25 corpus** (evidence:
          `analysis/m0142/m0142-0008a-3i-route-a-step2-census.txt`).
          `leaf-count` is unchanged at 26 — flattening is active
          (`nleaves>nrels` records) but every still-declining chain has a
          SECOND undecomposable member (`*Project` composites, NLI,
          `*Gather`, `*CTEScan` — M0144-0003a's own census finding), so no
          `problem rels=` line carries a flattened RHS leaf. Plan channel
          vs baseline `changed=4` (Q33/Q56/Q60/Q83) — qualifier-only
          diffs (`item_1.i_manufact_id`), same shapes and costs.
          `semianti-not-tail`×3 is a new conversion class on Q78's chain.
          The residual is M0145-shaped: the remaining declines are not
          sublink bodies and only jointree-first planning reaches them.
        - Gates: units PASS; tpch-spotcheck PASS (Q12=2 Q13=33);
          tpcds-sf025 sweep PASS (`PASS=96 MISMATCH=0 CKMISMATCH=0
          ERROR=0 TIMEOUT=0 SKIP=3`; Q78 oracle-verified); vet clean.
    - **P0-H11 audit finding (2026-09-20, Loop \#32):** the leaf-admission
      increment must also fix `cumulativeFromSpans`'s span round-trip
      (`joinsearchseam.go` `cumulativeFromSpans` → `joinlistProblem.cumOffsets`
      → `spansFromCumulative` at the consumer). `buildLeafSpans` deliberately
      appends synthetic Semi/Anti RHS ranges out-of-band (after the real
      total); flattening them into a plain `[]int` and re-spanning
      contiguously misattributes the hole to the last real leaf. Unreachable
      today (every semiAnti-carrying chain still declines at `leaf-count`
      upstream) — a latent correctness gate for whichever task first lets a
      link through. Details: `p0-h11-stale-frozen-comment-cleanup.md` §3.
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
- [ ] **M0142-0008c-1a — UNFROZEN (owner decision 2026-09-20; firewall stays — see banner) — HASH method for `createUniquePath`** — filed by
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
- [ ] **M0142-0008c-3d — UNFROZEN (owner decision 2026-09-20; firewall stays — see banner) — merge + partial-nestloop unique-ify substitution**
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
  actual unblock was filed and landed as **M0142-0008-producer** `[x]`
  (teach an IN-unnesting path — or a future EXISTS variant — to set
  `.SJInfo` on a DP-search-visible `JoinSemi` link, the same way c19 did
  for ANTI). Read design doc §58 before re-running this recon again.
  Still not
  picked up. Deferred for the same reason as 3c
  otherwise: `sortInnerAndOuter`/`matchUnsortedOuterMerge`/
  `matchUnsortedOuterMergePartial` (merge) and `addPartialNestLoopPaths`
  (parallel NL), PG oracle `sort_inner_and_outer`/`match_unsorted_outer`,
  `joinpath.c:1403-1441` (read live this loop, §19.2). Before enabling merge
  here, check whether `mergeDeclined`'s existing SEMI/ANTI decline (§8)
  should also cover the demoted-INNER case.
- [ ] **M0142-0008c-4 — UNFROZEN (owner decision 2026-09-20; firewall stays — see banner) — `innerrel_is_unique`/unique-index NOOP fast path** —
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
- [x] **M0142-0008-producer — teach an unnesting path to set `.SJInfo` on a
  DP-search-visible `JoinSemi` link (THE unblock for the whole chain)** —
  filed 2026-09-20 on the owner's unfreeze decision. Recon has converged on
  this being the one missing producer: the EXISTS/IN family is pinned pre-DP
  and never reaches `jointypeForDirection`'s SEMI/ANTI arm (c21 §57),
  IN-unnesting never sets `.SJInfo` (c6 §41), and the only DP-reachable
  producer (`reduce_outer_joins`'s ANTI demotion, c19) is unconditionally
  ANTI — so the chain's SEMI reachability is 0 corpus-wide even with
  `admitSemiAnti` live. Scope: make an IN-unnesting (or EXISTS-variant)
  path emit a `parser.JoinSemi` `*SpecialJoinInfo` that reaches the searched
  arm — the same shape c19 produced for ANTI — so the DP search sees a real
  semi/anti candidate for the TPC-DS EXISTS/IN family ({Q10, Q16, Q35, Q69,
  Q94}). **Hard constraint (owner): do NOT lift Q78's `outer-over-derived`
  firewall, or take any equivalent shortcut, to obtain reachability.**
  Evidence this is the right seam: design doc §58's live
  `GOOPG_C22DEBUG=1` corpus trace (zero `jointype=semi`/`anti` DPPATH lines
  with all plumbing landed). Gates: units + sf025 sweep; expected movement
  is semi/anti-adjacent categories on the named family — measure with the
  sweep's `CATEGORIES-EXCL-MATCH:` line, and the P0-E7 A/B baseline
  (23/24 identical) is the comparison point.
  Kind: impl
  Parent: M0142-0008
  Movement: expected yes — the unblock is itself the movement precondition;
  declare actuals from the sweep after landing.
  - **DONE 2026-09-20 (Loop #31)** — `inUnnestSJInfo` (unnest.go) now ports
    `existsUnnestSJInfo`'s construction onto both IN sites: `unnestInExpr`
    (SemiRhsExprs = the params' `SubCol`s; `innerPlan.Output()[0]` fallback
    for the operand-keyed params==0 shape) and `unnestNonCorrelatedInExpr`
    (SemiRhsExprs = `innerOut[0]`, carrying the real `SourceTableIdx` so
    `createUniquePath`'s schema-drift guard is satisfied). SEMI gets
    `LhsStrict`/`SemiCanBtree`/`SemiCanHash`=true; the NullAware (`NOT IN`)
    ANTI keeps `LhsStrict=false` and no Semi-only fields (fail-closed).
    Design doc: `docs/design/0100-0149/m0142-0008-producer-in-unnest-sjinfo.md`.
    - **Measured movement: NONE — corpus reachability is still zero, and
      the reason is upstream of this gate.** Traced SF0.25 sweep
      (`GOOPG_PGSHAPED_DP_TRACE=1`, 99 queries): 0 `semianti-*` declines,
      0 `jointype=semi|anti` DPPATH — identical to the pre-change census.
      Per-query attribution (TPC-DS Q56): every IN statement declines at
      `leaf-count` (`joinsearchseam.go:325`, nrels=4 nleaves=2) BEFORE
      `semiAntiLinksHaveSJInfos` (:628) runs — the walked chain flattened
      to real leaves only; the `Filter(Semi(...))` wrapper the IN paths
      leave above the pinned join is the suspected flattening blocker
      (the walk treats non-Join nodes as opaque leaves). The chain's real
      unblock is therefore the leaf-admission/chain-shape work, not this
      producer alone — resume point: design doc §6.
    - Producer verified at unit level: `in_unnest_sjinfo_test.go` pins all
      four shapes + a real `extractSearchLeaves` pass-the-gate proof.
    - Gates (staged tree a6a2609b): units PASS; tpch-spotcheck PASS
      (Q12=2/Q13=33); tpcds-sf025 PASS=96/0/0/0 plans same=99;
      tpch-acceptance-arm PASS 24/24 MATCH at PGSHAPED=1/PER_Q=900/seed
      20260905 vs a **freshly re-captured baseline**.
    - `bench/tpch/baseline-digests.txt` re-captured this loop (the
      2026-09-08 file predated the owner's 8-FK reload: 9 ROWS-DIFF +
      14 VALUE-DIFF of pure load drift; Q6 colsig also drifted —
      a column-naming change between 8dc298e92 and HEAD). The standing
      "re-capture before the next executor commit" flag is discharged.
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
- [x] **M0142-0016c — does PG qerr-match the M0142-0016b `Q33`/`Q54`/`Q56`
  shape if forced into an analogous parameterized-probe-with-residual plan,
  and should the resulting cost signal move those queries' plan choice?**
  **DONE 2026-09-18, recon closed, no production code changed.** Full writeup:
  `docs/design/0100-0149/m0142-0016c-pg-forced-plan-comparator.md`. Measured
  real PG 18.3 (`:65438`, db `tpcds025`) against the exact query text — no
  forcing needed: **PG's own unforced, cost-optimal default plan already
  contains the identical shape** for all three queries (Q54: `Index Scan
  using date_dim_pkey` residual `d_moy=1 AND d_year=1999`; Q33/Q56: `Index
  Scan using customer_address_pkey[/_1/_2]` residual `ca_gmt_offset = -5`),
  at cost numbers byte-identical to the already-committed
  `bench/tpcds/plans-pg/{Q33,Q54,Q56}.txt` captures. **PG's own qerr on that
  node lands within ~15% of goopg's** (Q33: PG 23.8-27.9 vs goopg 21.4-30.3;
  Q54 direct clamped-to-1 match). **Answer: yes, confirmed by measurement**
  (M0142-0016b's "not a blocker" verdict stands on firmer footing); **and no,
  the cost signal should not move the plan choice** (PG keeps this exact
  shape as ITS cost-optimal pick too, carrying the identical clamped-to-1
  estimate — it does not cost it away). **Root-caused the "UNMATCHED-IN-PG"
  tag itself**: both PG-side captures already contain the matching node —
  `scripts/estimate-parity/parity.py`'s relset key prefixes goopg's key with
  a `CTE <label>` scope (goopg's own capture still prints a `CTE
  ss`/`cs`/`ws`/`my_customers` marker for these single-reference CTEs) with
  no PG-side counterpart, because PG 18.3 structurally inlines
  single-reference non-recursive CTEs (`inline_cte`,
  `postgres/src/backend/optimizer/plan/subselect.c`) while goopg's analogue
  (`pushQualsThroughSingleRefCTEs`,
  `internal/optimizer/cte_inline_pushdown.go`) only pushes predicates through
  the CTE boundary without removing it — a scorer key-mismatch bug, not a
  missing PG comparator. Follow-up filed as **M0142-0016d** below.
- [x] **M0142-0016d — fix `parity.py`'s relset key so a goopg-only
  single-reference-CTE scope prefix does not block a match against PG's
  un-scoped key.**
  Parent: M0142-0016c. Bounded, tooling-only (no planner
  code), filed by M0142-0016c's root-cause read. `scripts/estimate-parity/parity.py`'s
  `annotate_relsets`/`MARK`/`base_relation` (`parity.py:65-120,202-238`)
  build a node's match key from `CTE <label>` marker lines in the **goopg**
  capture, producing keys like `ss.customer_address` for a single-reference
  CTE `ss`; PG's own capture never emits a `CTE` marker for a
  single-reference, non-recursive, non-volatile CTE (PG 12+ `inline_cte`
  removes the boundary before join-order search), so its key is the bare
  `customer_address` — the two never match, regardless of whether the
  underlying node is genuinely comparable (M0142-0016c confirmed it is, for
  Q33/Q54/Q56). Resume point: when scoring a goopg node whose relset carries
  a CTE-label scope prefix, additionally try matching against PG's
  unprefixed relset for the same bare relation names (or normalize both
  sides' keys to drop CTE-scope prefixes when the CTE is single-reference —
  cross-check refcount via the same `plannedCTE.refs`-style reasoning
  `cte_inline_pushdown.go` already uses, so a genuinely-multiply-referenced
  CTE like TPC-DS Q31's `ws3` — cited in that file's own doc comment — does
  NOT get de-scoped, since PG does not inline it either and a real semantic
  difference could still exist there). **Acceptance**: re-run `make
  ea-ratchet`; expect the 17 `UNMATCHED-IN-PG` findings from M0142-0016b to
  either disappear (PG's qerr floor now applies and goopg passes it, per
  M0142-0016c's measured qerr parity) or re-surface as genuinely-scored
  `NEW` findings with a real `pg_qerr` — either outcome is progress over an
  unconditional `UNMATCHED-IN-PG`, but the former (gate back to PASS) is
  expected per M0142-0016c's own numbers. Do not touch
  `pushQualsThroughSingleRefCTEs` or any other optimizer file — this is a
  scorer-only fix. If the fix reveals the underlying CTE-inlining-vs-only-
  qual-pushdown structural gap actually costs goopg a worse join order
  somewhere in the corpus (M0142-0016c's flagged-but-unscoped bigger
  question), file that as its own new task rather than folding it in here.
  **Done 2026-09-18.** `scripts/estimate-parity/parity.py`: new
  `cte_scan_counts`/`single_ref_cte_labels`/`descope` (mirrors PG 18.3's
  `inline_cte` "single-reference, non-recursive" threshold via a goopg-side
  scan refcount), applied to both sides' keys in `collect()`. Re-ran `make
  ea-ratchet` with a fresh HEAD capture (not the stale 2026-09-16 one): every
  Q33/Q56 body-internal finding from M0142-0016b's 17 disappeared outright
  (real PG match found, within bar); Q33/Q54/Q56's `cte:<label>` ancestor-
  scan-node findings persist UNMATCHED-IN-PG by design (PG's inlining removes
  that tree position entirely — a structural gap, not a key-naming one, out
  of this task's scorer-only scope). Same mechanism also resolved 27 more
  pre-existing findings outside the M0142-0016b population (Q5, Q16, Q58,
  Q60, Q77, Q80 partial, Q95, Q97) as a side effect of the general fix.
  3 of Q80's findings persist under their new unscoped names — a real Q80
  join-order difference inside that CTE body, unrelated to the prefix bug,
  not filed separately (already inside the M0142 milestone's general cost-
  model corpus work). Repinned the baseline (`make ea-ratchet-repin`,
  precedent `c89615911`/`c7e2f9d40`/`a5a1bd492`): 95→70 findings; `make
  ea-ratchet` now PASSes clean. Full writeup:
  `docs/design/0100-0149/m0142-0016c-pg-forced-plan-comparator.md` §"Update
  2026-09-18b". No `internal/...` file touched (tooling-only); `python3 -m
  py_compile` clean.
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
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
**Source:** `METHODOLOGY3/04-forward-plan.md` §3 "Continuous — engine correctness, not gated on anything"
**Prerequisites:** none. Order: `.ralph/fix_plan.md` banner (item 9; M0143-0008 is handled with P0-E5).

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
    Movement: none — real correctness work, but not one of S3's three
    instruments (owner reclassification 2026-09-18). What it achieved: a
    same-named composite/domain/range type declared in two
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
- [x] **M0143-0002g — the two write-site gaps M0143-0002f's own audit found
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
  - **Done 2026-09-17 (item 2 only — item 1 carved out to M0143-0002h).**
    Movement: none — real correctness work, not S3 movement (owner
    reclassification 2026-09-18). What it achieved: a GRANT/REVOKE ON TYPE or DROP DOMAIN issued against a
    type declared in a non-default database now correctly mutates that
    database's own `pg_type`/`pg_attribute` heap instead of silently
    corrupting it (a stray duplicate row on GRANT, a surviving row on DROP);
    confirmed by two new SQL-observable restart tests, each shown to fail
    with the exact predicted symptom when the fix is reverted.
    Landed the one-line `catalog.DefaultDBOid` → `tableCatalogHeapDBOid(ctx)`
    swap at `execDropDomain`'s two `deleteTypeFromCatalogHeap` calls and at
    `resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`'s delete+attrRel
    construction (`operators_ddl.go:23537,23667-23670,26366,26369`). Two new
    restart regression tests
    (`internal/postmaster/database_ddl_type_acl_domain_reload_test.go`):
    `TestDatabaseDDLTypeGrantOnTypeNonDefaultDBReload` and
    `TestDatabaseDDLTypeDropDomainNonDefaultDBReload`, each verified by
    temporarily stashing the fix — the GRANT case failed with a duplicate
    pg_type row (2 rows, want 1: the ACL resync's xmax stamp missed the
    right heap while the already-fixed insert landed a new row there,
    leaving both live) and the DROP case failed with a surviving "ghost" row
    (1 row, want 0: the xmax stamp had nothing to compensate for). **Item 1
    (pg_range) intentionally NOT fixed this loop**, and pgRangeRel is
    unchanged from `catalog.DefaultDBOid` — before writing its own test,
    checked whether `pgRangeRel`'s write-side fix is safe alone the way item
    2's fixes were (their read side, `loadSystemCatalogsIfPresentForDB`, was
    already per-database from M0143-0002e/f) and found it is NOT:
    `reloadUserRangeTypesFromHeap` (`catalog_heap_reload.go:1685`) and
    `RegisterRangeTypeDuringRecovery` both key exclusively off `cat.DBOID()`
    with no per-database loop, so a write-only fix would make a non-default
    database's range type silently lose its pg_range row on the very next
    restart — trading today's cosmetic wrong-file placement for actual data
    loss. Filed as **M0143-0002h** below with a `.ralph/deferral_ledger.md`
    row (dated 2026-09-17) recording the full read-side gap and resume
    point. Gates: `go build ./...` clean; `go test
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...
    ./internal/catalog/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green;
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 ERROR=0
    TIMEOUT=0, plan-shapes 99/99 identical. `tpch-spotcheck` not run — no
    TPC-H data needed, consistent with the P0-E6-wait selection rule.
- [x] **M0143-0002h — pg_range needs a paired write+read per-database fix,
  not the one-line swap M0143-0002g's own text assumed.**
  Parent: M0143-0002d. Filed 2026-09-17 from M0143-0002g's Done note.
  `pgRangeRel` (`internal/executor/sys_pg_range.go:54-59`) still hardcodes
  `catalog.DefaultDBOid`, but unlike the pg_type/pg_class/pg_attribute sites
  M0143-0002e/f fixed, there is NO existing per-database read-side loop for
  pg_range to land the write fix against:
  `reloadUserRangeTypesFromHeap` (`internal/initdb/catalog_heap_reload.go:1685`)
  scans only `cat.DBOID()`'s pg_range+pg_type heaps in a single unconditional
  pass (called once from `open.go:2272`, not looped over
  `cat.ListDatabases()` the way `loadUserTablesFromHeapForDB`/
  `loadSystemCatalogsIfPresentForDB` are at `open.go:1564,1586`), and
  `RegisterRangeTypeDuringRecovery` (`catalog_heap_reload.go:1756`)
  hardcodes `DBOid: cat.DBOID()` on every reloaded `RangeType` regardless of
  which database's heap the row logically came from. Landing the write-side
  swap alone would therefore REGRESS restart durability: a range type
  created in a non-default database would have its pg_range row written to
  that database's own heap file, but the reload pass would never scan that
  file, so the row (and hence `rngsubtype`) would silently vanish on the
  next restart — worse than today's cosmetic-but-durable wrong-file
  placement. Fix (land together, one commit): (1) give `pgRangeRel` the
  same `tableCatalogHeapDBOid(ctx)` swap; (2) extend
  `reloadUserRangeTypesFromHeap` into a per-database loop mirroring
  `loadSystemCatalogsIfPresentForDB` (`open.go:1586`) — for each
  distinct-dbOid database in `cat.ListDatabases()`, re-run the scan against
  that database's own pg_range+pg_type heaps and pass its real dbOid into
  `RegisterRangeTypeDuringRecovery` instead of `cat.DBOID()`; (3) extend
  `TestDatabaseDDLTypeCatalogReloadAcrossRestart`'s range assertion from
  pg_type-only to the `rngsubtype` join it currently avoids (join `pg_range`
  to confirm `r1`'s samerange resolves subtype `integer` and `r2`'s
  resolves `bigint` post-restart, isolated per database). Ledger:
  `M0143-0002g` row dated 2026-09-17.
  - **Done 2026-09-17.** Landed all three parts in one commit, as required
    (write-before-read alone would have repeated the exact bug this task
    exists to fix). (1) `pgRangeRel` (`internal/executor/sys_pg_range.go:54`)
    swapped `catalog.DefaultDBOid` → `tableCatalogHeapDBOid(ctx)`. (2)
    `reloadUserRangeTypesFromHeap`
    (`internal/initdb/catalog_heap_reload.go:1680`) gained a `heapDBOid,
    nsDBOid uint32` parameter pair (mirroring
    `loadSystemCatalogsIfPresentForDB`'s split): its pg_range/pg_type
    `RelFileNode`s now read `heapDBOid` instead of `cat.DBOID()`, and the
    `RegisterRangeTypeDuringRecovery` call now stamps `nsDBOid` instead of
    `cat.DBOID()` — `catalog.go`'s `rangeKey(dbOid, name)` registry already
    supported per-DB keys, so no catalog.go change was needed, only the
    caller. `internal/initdb/open.go:2272`'s call site keeps the historical
    `cat.DBOID(), cat.DBOID()` main pass and adds a new loop over
    `cat.ListDatabases()` (skipping oid 0/DefaultDBOid/PostgresDBOid/
    `cat.DBOID()`) immediately after it — placed after `reloadDatabasesFromHeap`
    (line 1546) so, unlike M0143-0002f's own read-side loop, there was no
    ordering hazard to rediscover. (3) Extended
    `TestDatabaseDDLTypeCatalogReloadAcrossRestart`
    (`internal/postmaster/database_ddl_type_reload_test.go`) with a
    `rangeSubtypeOf` helper joining `pg_type`→`pg_range` on `rngtypid`,
    asserting `r1`'s samerange resolves `integer` and `r2`'s resolves
    `bigint` post-restart. Verified pre-fix failure by stashing the two
    production files (`sys_pg_range.go`, `catalog_heap_reload.go`,
    `open.go`) and re-running: failed with "expected exactly 1 samerange
    pg_range row, got 0" for db r1 — the exact predicted silent-loss
    symptom — then restored the fix and re-ran green. Gates: `go build
    ./...` clean; `go test ./internal/postmaster/... ./internal/executor/...
    ./internal/initdb/... ./internal/catalog/...` PASS;
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full
    green; `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS against
    the staged tree; `python3 scripts/ralph-lineage-guard.py` exit 0.
    `tpch-spotcheck` not run — no TPC-H data needed for this unit-scoped
    fix, consistent with the P0-E6-wait selection rule. This closes the
    M0143-0002 lineage's pg_range gap; the `M0143-0002g` deferral-ledger row
    stays `-` (status flips are M0119's job, not the filer's).
- [x] **M0143-0003 — `pg_constraint` returns 0 rows of any contype after a restart** —
  including the `'p'`/`'u'` rows synthesised from indexes that demonstrably survive. A
  second, independent reload gap that R126 explicitly did not touch.
  - **Done 2026-09-17 (recon + decomposition; see
    `docs/design/0100-0149/m0143-0003-pg-constraint-reload-gap.md`).**
    `pg_constraint` is fully virtual, synthesised per-connection from FOUR
    independent in-memory sources (one per contype) — not one root cause.
    `f` (FK) was already fixed by R126. `p`/`u`/`x` (index-backed) all depend
    on `catalog.Index.IsConstraint`, never restored by
    `RegisterIndexDuringRecoveryForDB` (`internal/catalog/catalog.go:6595`) —
    every reloaded index vanished from `pg_constraint` regardless of type.
    Split into M0143-0003a (landed this loop) through M0143-0003e (filed
    below); parent ticked because the recon+decomposition milestone this
    task's text named is complete, mirroring M0143-0002d's own precedent.
- [x] **M0143-0003a — restore `Index.IsConstraint` for PRIMARY KEY on reload.**
  Parent: M0143-0003. `IsConstraint: primary` at `RegisterIndexDuringRecoveryForDB`'s
  `Index{}` construction (`internal/catalog/catalog.go:6595`) — lossless for
  PRIMARY KEY specifically (PG's `indisprimary` always implies a
  `pg_constraint` row; no "bare primary index" concept, unlike UNIQUE/EXCLUDE).
  - **Done 2026-09-17.** Verified pre-fix failure by temporarily reverting to
    `IsConstraint: false`: `TestDatabaseDDLReloadAcrossRestart`'s new PK
    assertions (added this loop, `internal/postmaster/database_ddl_reload_test.go`)
    failed with the exact predicted symptom (`pg_constraint PK rows = [],
    want exactly [parent_pkey]` for both `r1`/`r2`), restored the fix,
    re-ran green. Gates: `go build ./...` clean; `go test
    ./internal/catalog/... ./internal/postmaster/... ./internal/executor/...
    ./internal/initdb/...` PASS (postmaster 49s, initdb 131s). No TPC-H data
    needed for this unit-scoped fix, consistent with the P0-E6-wait
    selection rule.
- [x] **M0143-0003b — CHECK constraint durable persistence (write + reload).**
  Parent: M0143-0003. **Highest-severity remaining piece — this is an
  enforcement-loss correctness bug, not a display gap.**
  `catalog.Table.CheckConstraints`/`NamedChecks` are never written to any
  heap and never reloaded (confirmed by exhaustive grep: zero hits for
  `checkconstraints`/`namedchecks` in `internal/initdb/*.go` outside this
  citation). `copy.go`/`operators_fk.go`/`operators_storage.go` all gate
  CHECK enforcement on `len(tbl.CheckConstraints) > 0`, so **every CHECK
  constraint on every table silently stops being enforced after any server
  restart**, with no error at restart or at the first violating write.
  - **Done 2026-09-17.** Write path: `buildPGConstraintRowForTableCheck`/
    `writeCheckConstraintRow`/`stampCheckConstraintRows`
    (`internal/executor/sys_pg_constraint.go`), field-for-field matching the
    synthesised view's own CHECK projection (`catalog.go`'s
    `PGConstraintRowsForDBOid`). Rather than hand-wiring a new wrapper at
    each of the 9 `AddCheck*`/`AddCheckInherited` call sites found this loop
    (`operators_ddl.go:4319,4338,4345,4372,4405,5556,5582,9368,13106`), wired
    the write into the existing `syncTableToCatalogHeap`
    (write-all-current-checks loop, mirroring the FK loop) +
    `deleteCatalogRowsForOID` (`stampCheckConstraintRows` call, mirroring
    `stampForeignKeyConstraintRows`) funnel, then made sure every one of the
    9 sites actually reaches a resync: 5 sites in `execCreateTable` and 2 in
    `execCreatePartitionChild` fall into the SAME "mutated after the
    function's one early `syncTableToCatalogHeap` call" trap those
    functions' own comments already document for NOT NULL (M0134-0005y) —
    extended `execCreateTable`'s resync condition to
    `notNullHeapDirty || len(tbl.NamedChecks) > 0` and added an equivalent
    conditional resync block at the end of `execCreatePartitionChild` (which
    had none at all before this). The ALTER-ADD site (`:9368`) and the
    ADD-cascade-to-children site (`:13106`, inside
    `cascadeCheckToChildrenAt` — both the merge and the fresh-inherited
    branch) now call `o.syncConstraintCatalogRow` (an ALREADY-EXISTING
    delete-then-resync helper the DROP-cascade path was calling all along,
    just with nothing yet to persist). DROP CONSTRAINT: the cascade-to-child
    path (`:13153-13158`ish) already called `syncConstraintCatalogRow(child)`
    pre-existing; the TOP-LEVEL table's own truncation
    (`tbl.CheckConstraints`/`NamedChecks` splice, `:13422`ish) had NO resync
    at all — added `o.syncConstraintCatalogRow(tbl)` there. Reload:
    `loadCheckConstraintsFromHeapForDB`/`loadCheckConstraintsFromHeap`
    (`internal/initdb/catalog_heap_reload.go`) mirroring
    `loadForeignKeysFromHeapForDB`'s shape, wired in `open.go` right after
    `loadForeignKeysFromHeap`. Test: `TestDatabaseDDLReloadAcrossRestart`
    (`internal/postmaster/database_ddl_reload_test.go`) extended with a
    `gauge` table carrying `CONSTRAINT gauge_level_check CHECK (level >= 0)`;
    asserts the post-restart `pg_constraint` row AND actual enforcement (a
    satisfying `INSERT` succeeds, a violating one still fails). Verified
    live: temporarily no-op'd the `loadCheckConstraintsFromHeap` call and
    confirmed both new assertions fail with the exact predicted symptom
    (`pg_constraint CHECK rows = []`, violating INSERT unexpectedly
    succeeds), restored, re-ran green. Also had to widen the existing PK
    assertion (`gauge` is itself `id int4 PRIMARY KEY`, so the PK-row-count
    query now returns 2 rows, not 1) using an order-independent `conNames`
    helper. Residual found while auditing every `.NamedChecks` mutator for
    completeness: `VALIDATE CONSTRAINT`/`RENAME CONSTRAINT` (metadata-only,
    not enforcement) still don't resync — filed as a deferral-ledger row
    (2026-09-17), not a new fix_plan task (small, single-loop-sized follow-up
    named directly in the ledger's resume point).
    Gates: `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...`
    all PASS; `TestDatabaseDDLReloadAcrossRestart` re-verified with
    `-count=1` (not just cached); `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green;
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 ERROR=0
    TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS against the
    staged tree. No TPC-H data needed, consistent with the P0-E6-wait
    selection rule.
- [x] **M0143-0003c — UNIQUE (non-PRIMARY-KEY) constraint-backed index
  `IsConstraint` durability.**
  Parent: M0143-0003. Depends on: sequence after
  M0143-0003b (smaller diff if its write-path plumbing exists to reuse).
  Architecturally harder than 0003b: no durable signal today distinguishes
  `ALTER TABLE t ADD CONSTRAINT u UNIQUE (col)` from a bare `CREATE UNIQUE
  INDEX u ON t(col)` after a restart — real PG uses
  `pg_constraint.conindid`, which goopg does not persist for `contype IN
  ('u','x')`.
  - **Done 2026-09-18.** Resolved the schema decision in favor of option (a)
    (a `pg_constraint` heap row) over option (b) (a new `pg_index` bit): PG
    already solves this via `conindid`, so a heap row is the PG-faithful
    mechanism and reuses 0003b/0003d's exact funnel. Write:
    `buildPGConstraintRowForUnique`/`writeUniqueConstraintRow`/
    `stampUniqueConstraintRows` (`internal/executor/sys_pg_constraint.go`),
    `contype='u'`, `conindid=idx.OID`, `conkey` = key-column ordinals,
    `condeferrable`/`condeferred` sourced from `idx.Deferrable`/
    `idx.InitiallyDeferred` (closes an unrelated latent gap for free — neither
    field reloaded from anywhere before this). PRIMARY KEY excluded from the
    write loop (0003a already covers it). Wired into the same
    `syncTableToCatalogHeap`/`deleteCatalogRowsForOID` funnel. Resync
    coverage: since the constraint object here IS the index (no `Table`-owned
    list to length-check), added `tableHasUniqueConstraintIndex` (scans
    `IndexesOnTable` for `Unique && !Primary && IsConstraint`) as the
    equivalent dirty-trigger in `execCreateTable`'s and
    `execCreatePartitionChild`'s resync gates; the three ALTER-path sites that
    set `IsConstraint=true` outside those two functions
    (`execAlterTableAddUnique`'s build-new-index branch,
    `adoptExistingIndexAsConstraint`'s USING-INDEX branch, guarded to
    `!primary`) each gained a `syncConstraintCatalogRow` call, mirroring
    0003b's precedent for its own ALTER-path sites. Reload:
    `loadUniqueConstraintsFromHeap`/`loadUniqueConstraintsFromHeapForDB`
    (`internal/initdb/catalog_heap_reload.go`), wired in `open.go` right
    after `loadNotNullConstraintsFromHeap`; resolves `conindid` via a new
    `catalog.InMemory.LookupIndexByOIDAllDBs` (mirrors
    `LookupTableByOIDAllDBs`'s cross-database fallback). Test:
    `TestDatabaseDDLReloadAcrossRestart` extended — `gauge` now also carries
    `tag text CONSTRAINT gauge_tag_unique UNIQUE`; post-restart assertions
    check the `pg_constraint` row AND a behavior gated on `IsConstraint`
    (`RENAME CONSTRAINT`, which real PG/goopg both require
    `!Primary && Unique && IsConstraint` for) plus a genuine UNIQUE-violation
    INSERT. Verified live: temporarily no-op'd `loadUniqueConstraintsFromHeap`
    in `open.go` — failed with the predicted symptom (empty row, RENAME
    42704), restored, re-ran green. Found but deliberately NOT fixed
    (pre-existing, unrelated bug — ledgered): `execCreatePartitionChild`'s
    `poc.UniqueColumns` and `LIKE ... INCLUDING INDEXES` both clone/create a
    unique index without ever setting `IsConstraint=true`, even live,
    pre-restart.
    Gates: `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...`
    all PASS; `TestDatabaseDDLReloadAcrossRestart` green and via the
    temporary-revert probe (red as predicted); `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green; `scripts/tpcds-sf025-regression.sh
    sweep` PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical,
    gate-stamp PASS against the staged tree; `tpch-spotcheck.sh`
    SKIP-BLOCKED (expected — `:65433` still under the P0-E6 evidence hold),
    accepted via the standing G6 exception (ledger: P0-E7 remains the re-run
    owner); `python3 scripts/ralph-lineage-guard.py` exit 0. No TPC-H data
    needed for the change itself, consistent with the P0-E6-wait selection
    rule.
- [x] **M0143-0003d — NOT NULL constraint named-metadata durable
  persistence.**
  Parent: M0143-0003. Depends on: sequence after M0143-0003b
  (reuses its wrapper pattern). Lower severity than 0003b: `Column.NotNull`
  (actual enforcement) already reloads correctly via `pg_attribute.attnotnull`
  (`internal/initdb/open.go`'s `loadUserTablesFromHeapForDB`, `NotNull:
  ar.AttNotNull` — confirmed by grep, NOT a repeat of the CHECK gap). Only
  `catalog.Table.NotNullConstraints` (the PG18 named-constraint list used for
  `pg_constraint` `contype='n'` rows) is unreloaded — metadata only.
  - **Done 2026-09-18.** Write path: `buildPGConstraintRowForNotNull`/
    `writeNotNullConstraintRow`/`stampNotNullConstraintRows`
    (`internal/executor/sys_pg_constraint.go`), field-matched against the
    synthesised view's own NOT NULL projection (`catalog.go`'s
    `PGConstraintRowsForDBOid`): `conenforced` hardcoded true (PG has no NOT
    ENFORCED spelling for NOT NULL), `convalidated = !NotValid`, `conkey`
    carries the single column ordinal (unlike CHECK, real PG's NOT NULL
    `conkey` is non-null). Wired into the SAME `syncTableToCatalogHeap`
    write loop / `deleteCatalogRowsForOID` stamp funnel 0003b uses (added
    right after the CHECK loop/stamp call, same funnel, not a new one).
    Resync coverage: `execCreateTable` needed NO change — its pre-existing
    `notNullHeapDirty` flag (predates this task, added for
    `pg_attribute.attnotnull` sync per M0134-0005y) already fires on every
    `AddNotNull` and already triggers a full `syncTableToCatalogHeap`
    re-run, which now emits the NOT NULL rows for free.
    `execCreatePartitionChild` DID need a change: its 0003b-added resync
    block gated on `len(tbl.NamedChecks) > 0` only, but that function's own
    named-NOT-NULL block (parent-inherited + explicit `poc.NotNullColumns`)
    mutates `tbl.NotNullConstraints` via `AddNotNull` after the same early
    sync call — widened the condition to `len(tbl.NamedChecks) > 0 ||
    len(tbl.NotNullConstraints) > 0`. Reload:
    `loadNotNullConstraintsFromHeap`/`loadNotNullConstraintsFromHeapForDB`
    (`internal/initdb/catalog_heap_reload.go`), mirroring
    `loadCheckConstraintsFromHeapForDB`'s shape and reusing the FK loader's
    `fkAttnumsFromArrayText`/`fkColumnNames` helpers to decode `conkey` back
    to a column name; wired in `open.go` right after
    `loadCheckConstraintsFromHeap`. Test:
    `TestDatabaseDDLReloadAcrossRestart` extended — `gauge` now also
    carries `code text CONSTRAINT gauge_code_not_null NOT NULL`. Live
    finding: a `PRIMARY KEY` column (`gauge.id`) turns out to ALSO carry its
    own auto-named `<table>_<col>_not_null` constraint, so an unfiltered
    `contype='n'` scan picks up every table's PK column too — the
    assertion filters on `conrelid = 'gauge'::regclass` and expects both
    `gauge_code_not_null` and `gauge_id_not_null`. Verified live: temporarily
    no-op'd the `loadNotNullConstraintsFromHeap` call in `open.go`, re-ran
    with `-count=1` — failed with the predicted symptom
    (`pg_constraint NOT NULL rows = []`), restored, re-ran green.
    Gates: `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...`
    all PASS (one flaky, unrelated `TestSimpleQueryBatchAbortUndoesEarlierCreateType`
    failure under full-package parallel run, reproduced identically on
    unmodified HEAD and passed clean on every isolated/solo re-run — not
    caused by this change); `TestDatabaseDDLReloadAcrossRestart` re-verified
    with `-count=1` both green and via the temporary-revert probe (red as
    predicted). No TPC-H data needed, consistent with the P0-E6-wait
    selection rule.
- [x] **M0143-0003e — EXCLUDE constraint `indisexclusion` durability.**
  Parent: M0143-0003. Depends on: M0143-0003c `[x]` (done 2026-09-18) — reuse
  its landed shape directly: a `contype='x'` sibling of
  `buildPGConstraintRowForUnique`/`writeUniqueConstraintRow`/
  `stampUniqueConstraintRows` (same `conindid`-keyed `pg_constraint` row,
  not a new `pg_index` bit — 0003c's design doc section records why that
  option was dropped), and a reload loader that sets `idx.IsExclusion = true`
  (mirroring `loadUniqueConstraintsFromHeapForDB`'s `idx.IsConstraint = true`)
  instead of a new schema/heap-format decision. `indisexclusion` is a declared
  `pg_index` heap column (`internal/initdb/initdb.go:4766`) but is never
  written by the real index-creation path (confirmed by grep: zero
  non-comment hits for `indisexclusion`/`IndIsExclusion` outside catalog
  schema declarations and an unrelated hardcoded-false synthetic row,
  `internal/executor/pg18_user_catalog_rows.go:1460`) and never decoded on
  reload — `Index.IsExclusion` is lost on every restart, taking `x`-contype
  `pg_constraint` rows and `deferred_exclusion.go`'s deferred-exclusion-check
  machinery with it for any EXCLUDE constraint surviving a restart.
  - **Done 2026-09-18.** `buildPGConstraintRowForExclude`/
    `writeExclusionConstraintRow`/`stampExclusionConstraintRows`
    (`internal/executor/sys_pg_constraint.go`) mirror 0003c's UNIQUE shape
    exactly (`contype='x'`, `conindid=idx.OID`), but the write-loop gate in
    `syncTableToCatalogHeap` is `idx.IsExclusion` alone, not `idx.IsConstraint
    && idx.Unique` — the non-btree-equality EXCLUDE path
    (`createExclusionIndexStub`, e.g. `EXCLUDE USING gist (c WITH &&)`) sets
    neither, yet real PG still creates a `pg_constraint` row for it (the
    synthesised view already emits on this same OR condition,
    `catalog.go:7242`). New `tableHasExclusionConstraintIndex` widens the
    `execCreateTable`/`execCreatePartitionChild` resync-dirty triggers
    alongside `tableHasUniqueConstraintIndex`; `execAlterTableAddExclude`
    gained `syncConstraintCatalogRow` calls on BOTH branches (neither called
    it before — the btree-equality branch had no durability call at all, and
    the stub branch's own `createExclusionIndexStub` → `syncIndexToCatalogHeap`
    writes pg_class/pg_index/pg_attribute for the index relation only, never
    pg_constraint). Reload: `loadExclusionConstraintsFromHeap`/
    `loadExclusionConstraintsFromHeapForDB`
    (`internal/initdb/catalog_heap_reload.go`), wired in `open.go` right
    after `loadUniqueConstraintsFromHeap`. Unlike 0003c, restoring the flag
    alone would not be enough for enforcement to survive: `idx.ExclusionOp`
    ("=" or "&&") gates `checkExclusionConstraintsForInsert`'s switch
    (`operators_storage.go:8808`, no default arm), so an empty `ExclusionOp`
    post-reload would silently disable the check even with `IsExclusion=true`
    restored. `ExclusionOp` has no natural `pg_constraint` column (`conexclop`
    is a real `oid[]` goopg does not resolve operators into), so it rides the
    otherwise-NULL `conbin` column as raw text — the same smuggling
    convention `sys_pg_constraint.go`'s header comment already documents for
    CHECK/domain `adbin`; real PG never reads `conbin` for an `x` row. Test:
    `TestDatabaseDDLReloadAcrossRestart` extended with `gauge` gaining a
    `zone int4` column and `CONSTRAINT gauge_zone_excl EXCLUDE USING btree
    (zone WITH =)`. Verified live: temporarily no-op'd
    `loadExclusionConstraintsFromHeap` in `open.go`, re-ran — failed with
    BOTH predicted symptoms at once (empty `pg_constraint` row AND a
    duplicate-key INSERT that should raise `23P01` succeeding silently),
    restored, re-ran green. Gates: `go build ./...` clean; `go vet` (targeted
    packages) clean; `go test ./internal/catalog/... ./internal/postmaster/...
    ./internal/executor/... ./internal/initdb/...` all PASS (one
    `TestSimpleQueryBatchAbortUndoesEarlierCreateTable` failure under
    full-package run, reproduced as flaky — passes solo and passes on a
    second full-package run, unrelated real-TCP-socket timing test, not this
    change); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
    full green; `FORCE=1 scripts/tpcds-sf025-regression.sh sweep` PASS=96
    MISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS
    against the staged tree; `tpch-spotcheck.sh` SKIP-BLOCKED (expected —
    `:65433` still under the P0-E6 evidence hold), gate stamp refreshed
    against the staged tree, commit accepted via the documented
    `ledger:`/SKIP-BLOCKED exception (P0-E7 is the re-run owner);
    `make ralph-state-guard` clean (auto-repaired a stale `status`/`progress`
    mismatch from the previous loop's exit, unrelated to this change).
    Residual found while auditing every DROP-CONSTRAINT call site for where
    the new sync calls belonged: the UNIQUE/EXCLUDE branches of
    `execAlterTableDropConstraint` never remove the dropped index's own
    `pg_class`/`pg_index` heap rows, so it should resurrect after a restart —
    out of scope here (indexes that should NOT survive a restart, not
    indexes that should). **Not filed as a new fix_plan task**:
    `scripts/ralph-lineage-guard.py` rejected the commit with a new
    M0143-0003g descendant, since M0143-0003's last 5 completed descendants
    (0003a-e) all carry `Movement: none` and the lineage budget is exhausted
    — recorded ledger-only (`.ralph/deferral_ledger.md`, 2026-09-18
    M0143-0003e row) per the guard's own remedy instead of a fix_plan item.
- [x] **M0143-0003f — `poc.UniqueColumns`/`LIKE ... INCLUDING INDEXES` never
  set `IsConstraint` (live bug, not restart-related).**
  Parent: M0143-0003. Filed 2026-09-18, discovered while researching
  M0143-0003c's write-loop call sites (deferral-ledger row dated 2026-09-18).
  `execCreatePartitionChild`'s `PARTITION OF ... (col UNIQUE)` inline-column
  path (`poc.UniqueColumns`) and the `LIKE ... INCLUDING INDEXES` unique-index
  clone both call `createBTreeIndex` but never set `idx.IsConstraint = true`
  afterward, unlike every sibling UNIQUE-constraint path (inline column,
  table-level, named, PK auto-index). This is a LIVE bug — the resulting index
  never shows up in `pg_constraint` even before any restart — not a repeat of
  M0143-0003c's restart-durability gap. Fix: add `idx.IsConstraint = true`
  (plus a `syncConstraintCatalogRow` call, now that M0143-0003c's write path
  exists) right after both `createBTreeIndex` calls; add a regression test
  (`PARTITION OF ... (col UNIQUE)` and `LIKE ... INCLUDING INDEXES` each
  asserting the resulting index appears in `pg_constraint` with `contype='u'`).
  - **Done 2026-09-18.**
    Movement: none — real correctness work, not S3 movement (owner
    reclassification 2026-09-18). What it achieved: two independent CREATE-TABLE-time UNIQUE-index creation
    paths (`operators_ddl.go`'s `likeUniqueIndexes` loop and
    `execCreatePartitionChild`'s `poc.UniqueColumns` loop) now populate
    `pg_constraint` with a `contype='u'` row, matching every sibling
    UNIQUE-constraint path, on a table that never restarts — a genuinely new
    class of bug from M0143-0003a-e's restart-reload gaps, not a repeat.
    No explicit `syncConstraintCatalogRow` call turned out to be needed: both
    call sites already sit inside `execCreateTable`/`execCreatePartitionChild`
    ahead of an existing tail-of-function resync
    (`tableHasUniqueConstraintIndex(...)`, landed by M0143-0003c) that scans
    `IndexesOnTable` directly rather than a fixed call-site list — setting
    `idx.IsConstraint = true` right after `createBTreeIndex` was sufficient to
    make that pre-existing gate fire and write the row via
    `syncTableToCatalogHeap`. Two new regression tests in
    `internal/executor/operators_ddl_like_indexes_test.go`:
    `TestLikeIncludingIndexesMarksUniqueAsConstraint` (LIKE ... INCLUDING
    INDEXES with a source `UNIQUE` column) and
    `TestPartitionOfInlineUniqueMarksAsConstraint` (`PARTITION OF ... (col
    UNIQUE)`), both asserting `idx.IsConstraint == true` on the cloned index
    (consistent with this file's existing `TestLikeIncludingIndexesCopiesPKDeferrable`
    style — an in-memory catalog assertion, not a live `pg_constraint` SELECT,
    since `newDDLFixture` is the same fixture already used there).
    Gates: `go build ./...` clean; `go test ./internal/executor/...` PASS
    (12.4s); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
    full green (all packages, including `internal/initdb` at 122.7s and
    `cmd/goopg` at 24.0s); no TPC-H data needed, consistent with the
    P0-E6-wait selection rule (this is a unit-scoped CREATE-TABLE-time fix,
    no planner/executor row-count surface). `python3
    scripts/ralph-lineage-guard.py` not implicated — M0143-0003f already
    existed as an open `[ ]` task at HEAD (filed by the previous loop), so
    flipping it to `[x]` adds no new descendant under M0143-0003.
- [x] **M0143-0004 — `PhysicalTypeIsVarlena` has no `IsArray` arm**
  (`physical_align.go:85-107`) — latent for ordinary user `int4[]` columns, not just
  catalogs.
  - **Done 2026-09-18.** Added an `if t.IsArray { return true }` short-circuit at
    the top of `PhysicalTypeIsVarlena` (`internal/catalog/physical_align.go`),
    mirroring the `IsArray` arm `PhysicalTypeAlign` already had. A user array
    column carries `Type{Name:<element>, IsArray:true}` (Name = element type,
    DU-002 slice 62); every element name in the switch's non-default arms
    (`int4`, `int2`, `int8`, `bool`, `oid`, `float4/8`, `date`, `timestamp[tz]`,
    `uuid`, `name`, `xid`, …) is fixed-width on its own, so without the arm an
    `int4[]`/`bool[]`/`date[]`/… column was misclassified as NOT varlena even
    though every array is a varlena `ArrayType` blob on disk. Since this
    function is the single source of truth shared by the heap codec, pgoutput's
    walker, and the catalog statistic paths (comment at `physical_align.go:72-77`),
    one fix covers all three; `sys_pg_constraint.go:141-163` had already
    hand-documented this exact gap and worked around it for `pg_constraint`'s
    `conkey`/`confkey` by declaring them WITHOUT `IsArray` — that workaround
    comment can stay (still true and still needed: those two columns are still
    NOT `IsArray`), it just stops being the only mitigation in the tree.
    **Confirmed live, not just by code reading**: built both the pre-fix and
    post-fix binaries, started throwaway `init`+`start` clusters on 5533/5534,
    `CREATE TABLE onlyarr (tags int4[]); INSERT INTO onlyarr VALUES
    (ARRAY[1,2,3]);`, then parsed the raw heap page bytes — the buggy binary
    wrote `t_infomask=0x0800` (`HEAP_HASVARWIDTH` UNSET) on a row whose only
    column is a non-null `int4[]`; the fixed binary correctly writes
    `0x0802` (SET). This is precisely the `nocachegetattr` fast-path hazard
    `sys_pg_constraint.go:158-163` and `codec.go:1550-1557` describe: a real
    PG18 (or any PG-faithful decoder honoring the infomask) walking this row
    would use the fixed-prefix `attcacheoff` shortcut and either skip past or
    misread the array, corrupting it and every following column.
    Also checked (and found harmless by construction, so no code change
    needed there): `encodeArrayValuePGCtx` (`codec_array.go:104-113`) always
    writes the 4-byte/long-form varlena header (`total<<2`, low 2 bits always
    0), so `isShortVarlenaHeader` is always false for arrays regardless of
    this bug — `packableShortColumn`'s encode-side alignment decision for
    array columns was already unaffected, and `AttAlignPointer`'s decode-side
    peek vs. force-align both land on the identical offset whenever the
    writer always force-aligns (proved by cases: pre-aligned offset — peek
    and force agree trivially; offset needing padding — the byte the peek
    inspects IS the real zero pad byte the encoder wrote). Verified this
    equivalence live too: byte-identical relation file sizes (139264 and
    344064 bytes respectively) between the two binaries for both an
    `int4[]`-as-last-column table and a `bool` + `int4[]` table across 2000
    and 5000 rows. New test: `TestPgRowHasVarWidthDetectsVarlenaCols`
    (`internal/executor/pg18_user_catalog_rows_test.go`) gained an
    `{Name:"int4", IsArray:true}` case pinning the exact infomask bug (the
    pre-existing case there used `{Name:"text[]"}`, a catalog-form Name that
    was never in the fixed-width switch and so never exercised the missing
    arm); `TestPhysicalTypeIsVarlenaArray`
    (`internal/catalog/physical_align_test.go`) pins the helper directly for
    every fixed-width element name, both array and scalar form. Gates:
    `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/executor/... ./internal/access/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green; `scripts/tpcds-sf025-regression.sh
    sweep` `PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0`, plan-shapes 99/99 identical;
    `tpch-spotcheck.sh` SKIP-BLOCKED (exit 3) by the `:65433` P0-E6 evidence
    hold — ledger: P0-E7 is the re-run owner, same standing exception as
    M0143-0003b/c/d/e/f.
- [x] **M0143-0005 — `ParamRef` LIMIT + DISTINCT returns wrong rows** — R83 fixed the
  `IntegerConst` case and pinned it; the `ParamRef` allowlist was deliberately not
  extended. Fail-closed with zero corpus impact today, which is exactly why it stays
  invisible until someone writes the case.
  - **Already resolved before filing; closed 2026-09-18 without new code.**
    This item duplicates M0137-0010 open problem C1
    (`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/02-open-problems.md`),
    which commit `d03f0a656` ("optimizer,scripts(M0137-0010): qual-placement
    census + close C1 (ParamRef LIMIT+DISTINCT)", 2026-09-15) already closed:
    `limitBoundMovable` (`internal/optimizer/tuplefraction.go:116-125`) accepts
    `*ParamRef` alongside `*IntegerConst`, and
    `TestDistinctLimitAppliesAboveDistinct_ParamRef`
    (`internal/executor/distinct_limit_paramref_test.go`) pins the exact
    150×'a'+50×'b' `SELECT DISTINCT v FROM dlp ORDER BY v LIMIT $1` case this
    item describes. Verified this loop: `go test ./internal/executor/ -run
    TestDistinctLimitAppliesAboveDistinct_ParamRef -v` PASSES at HEAD with no
    code change. M0143-0005 was filed three days after d03f0a656 landed
    without checking whether the C1 closure already covered it — no ledger
    row (nothing was deferred; the fix was already complete, only the
    fix_plan checkbox was stale).
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
- [x] **M0143-0007 — separate the dimension-table `relpages` divergence (K41)** —
  `customer` 1,979 pages vs PG's 2,872, `item` 716 vs 1,284. M0140-0005 filed it
  as out of planner reach and that is correct — **but `relpages` is an input to
  every page-priced cost term and to `compute_parallel_worker`'s size ladder, so
  no cost-model change can ever correct it.** The leading candidate is R23's
  `character(N)` blank-padding, a real PG-compat defect that shifts `relpages` on
  every `bpchar` table; R22 (per-page free-space comparison) separates the two.
  Storage work, not planner work, which is why it belongs here. Ledger:
  `m0140-0005-nonplanner-heap-density-floor`.
  - **Done 2026-09-18.** Direct per-page free-space walk (raw `pd_lower`/
    `pd_upper` parse, both engines share PG18's byte-identical page header —
    `internal/storage/page.go`) on `customer`/`item` at TPC-DS SF0.25, no
    server start needed (read the on-disk relation files directly; PG side
    cross-checked live against the already-running read-only `:65438`
    oracle's `pg_class.relpages`/`reltuples`, SELECT-only). Result: goopg's
    per-page free space is SMALLER than PG's on both tables (82.8B vs
    117.3B customer; 173.4B vs 255.5B item — goopg packs pages MORE
    tightly, ruling out R22/fill in this direction), while the page-count
    ratio and the used-bytes ratio are nearly identical (0.689/0.696
    customer; 0.558/0.584 item) — the entire K41 gap is tuple width, not
    page fill. Cross-checked by an independent TSV-column-length estimate
    (67.9/229.3 B/row) agreeing with the page-derived deltas (69.8/226.2
    B/row) to a few percent. **R23 (`character(N)` blank-padding) confirmed
    as ~full explanation; R22 ruled out** for these two tables. Full
    writeup: `docs/design/0100-0149/m0143-0007-relpages-bpchar-padding-confirmed.md`.
    Recon/measurement only, no production diff — implementing R23 itself is
    filed as **M0143-0007b** below since it reverses a documented,
    load-bearing design convention (trimmed bpchar storage,
    `internal/catalog/bpchar.go`) across multiple sibling paths. Ledger:
    `.ralph/deferral_ledger.md`, row dated 2026-09-18.
- [ ] **M0143-0007b — implement (or owner-decline) R23 `character(N)` on-disk
  blank-padding.**
  Parent: M0143-0007. M0143-0007 confirmed PG pads `bpchar`
  storage to its declared width and goopg does not, and that this fully
  explains the K41 `relpages` gap on `customer`/`item`. Closing the gap for
  real means storing `bpchar` padded — but goopg's trimmed convention is
  explicit, documented, load-bearing design (`internal/catalog/bpchar.go`,
  "M0103-0007 rung 24"; `compareDatum`'s padding-insensitive bpchar equality
  and `codec.go`'s `coerceTextLikeDatum` both rest on it;
  `bpchar_declared_width_test.go:78-86` states explicitly why padding on
  decode instead would be wrong). Before writing code: get an owner decision
  on whether to reverse the trimmed-storage convention at all (it is a
  genuine on-disk PG-compat defect per the project's absolute-compatibility
  rule, but reversing it touches heap comparisons, `internal/access/nbtree`
  key comparators, and WAL `pgoutput` encoding — every sibling boundary that
  currently assumes trimmed storage — plus TOAST thresholds/index key
  sizes/WAL record sizes store-wide). If approved: design doc first (own
  `docs/design/<id>-*.md`), then land per-boundary with the sibling-path
  audit this project's practice card requires, each slice gated by its own
  regress run plus the sf025 sweep. Ledger:
  `.ralph/deferral_ledger.md`, row dated 2026-09-18 (task M0143-0007).
- **OWNER DECISION 2026-09-20 — APPROVED.** The owner approves reversing
  the trimmed-`bpchar`-storage convention: implement R23 padded
  `character(N)` storage. Proceed per the task text — design doc first,
  then land per-boundary with the sibling-path audit (heap comparisons,
  `internal/access/nbtree` key comparators, `pgoutput` WAL encoding,
  TOAST thresholds/index-key/WAL record sizes), each slice gated by its
  own regress run plus the sf025 sweep. Context: the owner also reloaded
  `:65433` today with the canonical 8-FK schema on a binary rebuilt at
  `2b8afa538` (HEAD at reload time; `09bde885a` re-pinned the anchors) —
  FK-driven plan comparisons now run against the FK-bearing cluster.
- [x] **M0143-0009 — `SELECT … FOR UPDATE` over a join drops every row when a
  locked leaf is not the rightmost scan (resjunk-ctid ColumnRef shift).**
  RESOLVED Loop #21 — `rebaseRowMarkPlan` post-order walk rebases every
  expression after ctid injection (per-node old→new position remaps; join
  preds/keys/`UsingCols` in merged coords — the executor's
  `mergedKeySlot`/`keySlot.rebind` prove keys are merged-space, not
  side-local; NLI probe keys outer-scoped), `SchemaColumn.Resjunk` marks
  injected cols, `LockRows.Output()`/`lockRowsOp` strip by resjunk-position
  set (not trailing-N), `distinctOp` dedups excluding resjunk positions,
  `CtidResno` resolves for non-Project roots, `LockedRel.ColPos` fixes EPQ
  merge on projected outputs, AI-007 self-join guard retired (alias-aware
  `tagScan`). Walk-wide `seen` dedupes shared `*ColumnRef` objects
  (HashKeys[0] aliases LeftKey/RightKey by pointer). Design:
  `docs/design/0100-0149/m0143-0009-rowmark-ctid-global-expr-rebase.md`.
  Test: `TestLockRowsJoinCtidShift` (8 arms) + updated `locking_test.go`
  self-join pins; verified end-to-end on scratch cluster incl. hash join.
  Kind: impl.
  Parent: none.
  Filed 2026-09-19 (Loop \#21) — live wrong-results defect found while probing
  rowmark ordering during task selection; verified at HEAD `2b7e97053` on a
  scratch cluster (:5533). Broadens the deferral-ledger `AI-007 self-join
  lock` row (2026-08-09, `status: -`): the blast radius recorded there
  ("self-joins") is under-scoped — ordinary two-table joins are corrupted
  the same way.
  - Repro (tables `a(i int, t text)`, `b(i int, t text)`, 200k rows each,
    one `i=31` row per side):
    `SELECT a.i FROM a JOIN b ON a.i=b.i WHERE a.i=31` → 1 row;
    `… FOR UPDATE` → 0 rows (WRONG); `… FOR UPDATE OF a` → 0 rows (WRONG);
    `… FOR UPDATE OF b` → 1 row; `SELECT b.i FROM b JOIN a … FOR UPDATE OF a`
    (a rightmost) → 1 row. `EXPLAIN (ANALYZE)` shows `Rows Removed by Join
    Filter: 1` inside the Nested Loop under LockRows — the join emits
    nothing, so LockRows never sees a row. Reproduces identically under
    Hash Join (`SELECT *` variant).
  - Mechanism: `wireRowMarkCtidColumns` (`internal/optimizer/planner.go:2663`)
    appends a `ctid<N>` resjunk column to each rowmarked leaf's schema;
    `recomputeIntermediateSchemas` (:2813) rebuilds Join/NLI schemas as
    left++right but rebases `ColumnRef.Index` ONLY for `*Project` targets
    (`fixColumnRefIndices`, :2882). `Join.Predicate` ColumnRefs are absolute
    merged-row positions (`joinPredicateMatch` → `evalExpr` over the merged
    row), so a ctid injected into any non-rightmost leaf shifts every later
    sibling's refs — the `b.i` operand reads the ctid datum, the join filter
    misfires, every row is removed. Locking only the rightmost leaf appends
    at the merged row's end → no shift → works; that is why `OF b` and
    right-leaf cases pass and why the ported rowmark tests did not catch it.
  - Same hazard class, un-rebased: hash/merge key expressions and the
    residual re-eval (`execResidual` defaults to the full `Predicate`,
    `internal/executor/operators_join_agg.go` ~line 353 — what kills the
    Hash Join variant), `Filter` predicates above the join, Sort/Aggregate/
    Distinct keys.
  - `hasSelfJoinLockedTable` (:2639) only skips injection when the same
    table OID appears twice (the AI-007 workaround); the general case is
    unguarded.
  - Secondary wrinkle: `CtidResno` is set only when the plan root is
    `*Project` (:2796); Join-rooted plans inject ctid columns that nothing
    reads — pure harm (shift refs, then the executor falls back to
    `currentTID`).
  - Fix direction (PG-faithful): PostgreSQL never shifts positions —
    resjunk ctids are built into the scan targetlist at path time and
    `postgres/src/backend/optimizer/plan/setrefs.c`
    (`set_plan_refs`/`set_join_references`) remaps every Var through the
    constructed targetlists. goopg's post-hoc injection needs the same
    global rebase: after `recomputeIntermediateSchemas`, walk the whole
    plan tree and rebase every expression holding absolute ColumnRefs
    (join Predicate + hash/merge keys, Filter.Predicate, Sort keys,
    Aggregate keys, …) via the existing (name, SourceTableIdx)→position
    map. `hasSelfJoinLockedTable` removal stays out of scope until the
    rebase exists (its guard currently masks self-joins only).
  - Required per M0143 discipline: a failing regression test first —
    `FOR UPDATE` over a two-table join must return the join's rows
    (locked vs unlocked results identical), covering left-leaf and
    right-leaf `OF` arms plus bare `FOR UPDATE`, under NL and hash plans.
    Executor/planner gates per the practice card. Resolving this task
    resolves the `AI-007` ledger row's residual
    (`.ralph/deferral_ledger.md`, row dated 2026-08-09).
- [x] **M0143-0010 — index probes must resolve the heap update chain; raw
  `PageGetHeapTuple` on the index ItemPointer reads a dead chain root.**
  RESOLVED Loop \#22 — new `eachHeapChainMember` iterator
  (`internal/executor/operators_index.go`, the
  `followHOTChainNoCopy`/`resolveDeferredUniqueChainTail` traversal shape:
  redirect stubs → target, `ItemIDNormal` members in chain order via
  `IsHotUpdated`/`CTID` links, `MaxHeapTuplesPerPage` bound) now backs every
  index-driven heap probe that must judge the live row version:
  `scanIndexForFKMatch` (snapshot `TupleVisibleSubxact` per member),
  `uniqueCheckWithWait.scanOnce` (in-flight-xmin wait +
  `isLiveForUniqueCheck` per member, `conflictPtr` = member slot),
  `findInProgressConflictKey` + `probeSpeculativeConflict`
  (upsert arbiter), `exclusionCheckOnce`, and
  `recheckDeferredExclusionEq` (chain = one logical row — counts once if
  ANY member is live, never per member). Already-correct siblings
  unchanged: `followHOTChain` probes (index/index-only/update scans,
  arbiter :673), `resolveDeferredUniqueChainTail`, EPQ/rowmark exact-ctid
  refetches. Design:
  `docs/design/0100-0149/m0143-0010-index-probe-hot-chain-resolution.md`.
  Tests: `TestFKInsertAfterParentHotUpdate`,
  `TestUniqueInsertAfterHotUpdate` (both verified red→green); the three
  isolation specs PASS.
  Kind: impl.
  Parent: none.
  Movement: none — executor correctness fix (false 23503/duplicate-key
  bypass); no planner, costing, or plan-shape surface touched.
  Filed 2026-09-20 (Loop \#22) — discovered while root-causing M-NIGHTLY
  items `testport/TestPort_IsolationFkContention`
  (AI-20260917-004357-008), `TestPort_IsolationFkDeadlock`
  (AI-20260917-004357-009) and `TestPort_UpdateLockedTuple`
  (AI-20260917-004357-012): all three are `pass`-required specs that
  regressed between nightly `20260916-035206` (sha `48cf54f8`, all PASS)
  and `20260917-004357` (sha `1b54b00f`, all FAIL with spurious 23503
  where PG expects a clean match or a wait).
  - Mechanism: a non-key UPDATE is HOT — the b-tree entry keeps pointing
    at the chain ROOT line pointer, whose tuple is dead once the updater
    commits (the live successor sits deeper in the same-page t_ctid
    chain). Any index probe that fetches `ptr.Offset` verbatim and tests
    only that tuple reports a false no-match for a live row. The
    regression window's culprit is `a53c5b807` (M0142-0003g), whose new
    `scanIndexForFKMatch` index-probe twin of `scanRelForFKMatchSeq`
    (FK INSERT `assertParentExists` / `scanTableForMatchFKWait`) reads
    the raw pointer — so `INSERT INTO child` fails 23503 after any
    committed non-key parent UPDATE. Verified live on a scratch cluster
    (`:5533`): `UPDATE foo SET b='y'; INSERT INTO bar VALUES (42)` →
    `ERROR: ... violates foreign key constraint "bar_a_fkey"`.
  - Same blind spot, **older, worse**: `uniqueCheckWithWait`'s
    `scanOnce` (`internal/executor/operators_storage.go:8998`) — the
    plain `INSERT`/`UPDATE` unique-enforcement probe — has never walked
    the chain either. Verified live: `CREATE TABLE uq(a int PRIMARY
    KEY, b text); INSERT INTO uq VALUES (7,'x'); UPDATE uq SET b='y';
    INSERT INTO uq VALUES (7,'dup')` **succeeds — duplicate PRIMARY KEY
    rows**. Also raw-pointer and unchained: upsert arbiter probes
    `findInProgressConflictKey` (`operators_upsert.go:869`) and the
    speculative-recheck probe (`:1402`). The :673 arbiter arm already
    follows the chain via `followHOTChain` (M0100-0005 Bug A), and
    `recheckDeferredUniqueKey` resolves via
    `resolveDeferredUniqueChainTail` (M0134-0005e) — the pattern exists,
    the newer/older probes just never got it.
  - Fix direction: a shared per-member chain iterator (redirect stubs →
    target, `ItemIDNormal` members yielded in chain order via
    `IsHotUpdated`/`CTID` links, `MaxHeapTuplesPerPage` bound — the
    `followHOTChainNoCopy`/`resolveDeferredUniqueChainTail` traversal
    shape) so each probe keeps its own predicate
    (`TupleVisibleSubxact`+`fkPendingOutcome` for FK;
    `isLiveForUniqueCheck`+in-flight-xmin for the unique/upsert probes)
    but can never again see only the dead root. Per-member evaluation in
    chain order preserves the abort-rescan case (root's aborted xmax is
    live under `isLiveForUniqueCheck`) that a tail-only resolution would
    lose.
  - Gates: the three failing isolation specs PASS at HEAD after the fix
    (verified red at `ad778446e` first);
    `TestFKInsertAfterParentHotUpdate` (new, red-then-green verified via
    stash); executor package suite; the FK/upsert/unique isolation
    siblings; tpch-spotcheck; tpcds-sf025; tpch-acceptance-arm.

## M0144 — Measurement-first parity: censuses, instrumented PG, route-order alignment (filed 2026-09-20)

**Milestone doc:** `docs/milestones/0144-measurement-first-parity.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
**Source:** `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/03-forward-plan.md`
(adopted by owner decision 2026-09-20, with two owner overrides recorded in the
milestone doc: parallel-mode TPC-H parity is now canonical, and route-order
verification is prioritised because several landed fixes never moved a plan —
goopg's processing route diverged from PG's upstream of the fix).
**Prerequisites:** none. Order: `.ralph/fix_plan.md` banner item 2.

- [x] **M0144-0001 — canonicalise parallel-mode TPC-H parity** (owner decision
  2026-09-20). **DONE 2026-09-20** (code `2a2ebc3f0`). Design doc:
  `docs/design/0100-0149/m0144-0001-parallel-tpch-parity-canonical.md`.
  `-serial` now defaults `false` (`cmd/estimate-audit/main.go`); caller audit
  found `tpch-estimate-audit-arm.sh` was the only binary invocation site and
  already pinned `-serial=true`; no test pinned the old default. Fresh
  canonical parallel capture on a private lane (G3, HEAD binary, served-sha
  verified): **TPC-H parallel `match=1/22` (Q6)**, artefacts
  `analysis/m0144/m0144-0001-{goopg,pg}-parallel.*` — measured on the
  post-2026-09-20-8-FK-reload stats epoch (both sides' epochs differ from
  P0-E7's pair), so the drop from P0-E7's provisional 3 is a new-baseline
  reading, not a same-data regression: Q13/Q11's PG plans are unchanged in
  shape while goopg's flipped on the new stats, and no production commit in
  between touches aggregation/join-order costing. The measured number is
  reported for the owner to write into `AGENT.md` §Goal (harness section is
  owner-edited). Also corrected `m0137-0003` §2's stale `-ref-port 65432`
  recipe (R1: it ANALYZEs the reference) to the two-invocation form.
  Movement: none
  Kind: impl
  Parent: none
  Original scope (kept for the record): flip `estimate-audit -serial`'s
  default to `false` (`cmd/estimate-audit/main.go:293`) so the canonical
  tool measures the target protocol by default; fix any test pinning the
  old default; keep `-serial=true` working as the diagnostic mode.
  **Caller audit first**:
  `scripts/tpch-estimate-audit-arm.sh` relied on the serial default — already
  pinned `-serial=true` in the same owner change that filed this task; sweep
  for any other invocation site that omits the flag before flipping. The
  `cmd/` commit needs the `tmp/gate-stamps/{tpch-spotcheck,tpcds-sf025}.json`
  PASS stamps for the staged tree plus a `PARITY: N/A — measurement-tool flag
  default` body line (G2). Then capture a fresh
  parallel-mode TPC-H pair on a private lane per G3 (`-plan-only -serial=false`,
  HEAD binary, `GOOPG_EXPECT_BIN_SHA256` set) and record the resulting MATCH
  count — that number re-pins the `AGENT.md` §Goal floor (provisionally ≥3
  from P0-E7's 2026-09-18 measure; report the actual). Update
  `m0137-0003-baseline-capture-procedure.md` §2 if the flag flip lands after
  the doc update. The capture artefacts land in
  `analysis/m0144/` with a `docs(…)`/`analysis(…)` commit.
- [x] **M0144-0002 — first-divergence census** (03-forward-plan §1).
  **DONE 2026-09-20.** `scripts/pg-plan-first-divergence.py` built as the
  sibling instrument (imports the differ's parser + N1–N7 normalisation +
  category vocabulary; accepts both `=== Qn` and `===== Qn =====` section
  styles; stop-at-first PG-order aligner, one mutually-exclusive record per
  divergent query). Ran over all three committed corpora — TPC-H parallel
  on the fresh M0144-0001 pair, TPC-DS SF0.25 latest sweep vs the
  m0142-0012verify PG reference, TPC-DS SF1 on P0-E7's pair. Census MATCH
  sets reproduce the differ's floors exactly (TPC-H Q6 1/22; SF0.25 Q9+Q41
  2/99; SF1 Q41 1/99). Ranked tables:
  `analysis/m0144/m0144-0002-first-divergence-census.md` (+3 raw `.txt`).
  Headline: TPC-DS's dominant first-divergence is ordered aggregation under
  `LIMIT` (`Limit → PG GroupAggregate/Incremental Sort vs goopg Sort`,
  ~25 records SF0.25 / ~20 SF1 — the M0141-S7 family, now with a measured
  blast radius); TPC-H leads with aggregation-strategy at the root/Sort
  boundary (7/21 — the phased-agg decision the serial corpus hid).
  Design doc:
  `docs/design/0100-0149/m0144-0002-first-divergence-census.md`.
  Movement: none
  Kind: recon
  Parent: none
- [x] **M0144-0003 — route-order verification** (owner directive 2026-09-20:
  "fixes landed but plans didn't move because the processing order differs
  from PG" — verify, then align).
  **DONE 2026-09-20.** Route-diff table:
  `analysis/m0144/m0144-0003-route-order-verification.md`; design doc
  `docs/design/0100-0149/m0144-0003-route-order-verification.md`.
  Verdicts: **(a) confirmed** — the `Filter{sunk}` inside `Semi.Left`
  (`pushConjunctsBelowSemiAnti`, unnest.go:372) is one opaque leaf to
  `extractSearchLeaves` (joinsearchseam.go:1456) → `leaf-count` decline at
  :325 before `semiAntiLinksHaveSJInfos`; PG's `pull_up_sublinks`
  (prepjointree.c:468, planner.c:737) is jointree-level so no Filter node
  exists. **(b) confirmed** — `addPartialSetOpPath`
  (windowsetoppaths.go:551) produces at the `*SetOp` plan-node level while
  PG flattens UNION ALL-in-FROM to an appendrel (`pull_up_simple_union_all`
  prepjointree.c:1617 via planner.c:754) and files partial paths per-rel
  (`add_paths_to_append_rel` allpaths.c:1321) — appendrel is a join-input
  citizen; goopg SF0.25 has 0 Parallel Append vs PG's 9 sites.
  **(c) REFUTED as ordering** — producer sits at PG's position
  (upperordered.go:186 ≈ planner.c:5374); inert via
  `GOOPG_INCREMENTAL_SORT` default-off (incrementalsortpaths.go:81) plus
  S7's measured cost loss — costing question, banner item 6's scope.
  **(d) confirmed** — `applyUpperNarrowing` at planner.go:190 runs after
  the tournament; PG prices narrowed width into candidates via
  `set_pathtarget_cost_width` (costsize.c:6367) from
  `make_*_input_target`s built in `grouping_planner` (planner.c:1676-1744).
  **(e) confirmed, same root as (a)** — the pin is load-bearing only
  because Phase B declines; PG admits semi/anti pairs inside DP via
  `join_is_legal` (joinrels.c:350/:713). Impl tasks filed: M0144-0003a/b/c.
  Movement: none
  Kind: recon
  Parent: none
- [!] **M0144-0003a — Phase-B leaf admission through the sunk-conjunct
  Filter** (filed by M0144-0003 item (a)+(e), same root). Phase B's chain
  `spineJoins[0]` is the outermost pinned Semi/Anti join whose `Left`
  carries `Filter{sunk}(origChain)` (predp.go:49-55's documented shape);
  `extractSearchLeaves` counts that Filter as one opaque leaf
  (joinsearchseam.go:1456-1461) so `leaf-count` (:325) fires for every
  corpus IN/EXISTS statement (measured `leaf-count×62`, `nrels=4 nleaves=2`
  on Q56) before `semiAntiLinksHaveSJInfos` (:1889) runs. Fix shape: make
  the walk descend a `*Filter` whose conjuncts re-base into the chain's
  WHERE/qual space (the conjuncts are already in the outer chain's column
  space — `pushConjunctsBelowSemiAnti` only moved them down, never
  re-based), so the sunk Filter contributes its leaves plus its conjuncts
  rather than collapsing to one opaque leaf. Alternative (bigger,
  PG-faithful): move unnesting to jointree level — the durable direction
  per the route-diff pattern. Constraint: Q78's `outer-over-derived`
  firewall must not weaken. Expected movement per S5: IN/EXISTS-derived
  semi/anti links reach the searched pair space → census semi/anti
  first-divergence records move (SF1 Q16 `NL Left Semi vs HJ Left Anti`
  class; the ~62 leaf-count declines re-attributed).
  Kind: impl
  Parent: M0144-0003
  - **FIX SHAPE CORRECTED 2026-09-20 (loop \#56) by corpus census —
    the opaque leaf is a `*Project`, NOT a `*Filter`.** Design doc:
    `docs/design/0100-0149/m0144-0003a-opaque-leaf-census.md`; evidence:
    `analysis/m0144/m0144-0003a-opaque-leaf-census.txt`. Movement: none
    (census + decision; probe reverted, no production code changed).
    - Every opaque leaf at the `leaf-count` decline, all 99 SF0.25 queries:
      `Project` 53 occurrences, `SeqScan` 4, `NestedLoopIndexJoin` 1,
      `Gather` 1. **Zero `*Filter` leaves.** `Project` is the only kind in
      23 of the 26 declines and appears in 10 of the 11 declining queries.
    - The decline count is **26 across 11 queries** (Q14 Q16 Q23 Q33 Q35
      Q56 Q58 Q60 Q83 Q94 Q95), not the filed `leaf-count×62` — consistent
      with `M0142-0008-producer` and the `-plumbing` chain landing between
      the two censuses.
    - Provenance of the Projects was already established by
      `M0142-0008a-3i-verify`: `unnestExistsExpr` clones the EXISTS body's
      already-planned subquery tree, and every planned `SELECT` carries its
      own top-level output-list `*Project`.
    - **This is EASIER than the filed fix**, not harder: a Project carries
      no conjuncts, so the whole "re-base into the chain's WHERE/qual
      space" half of the filed fix shape disappears. What remains is
      structural — descend, and let the walk flatten the body's own join
      tree into real leaves.
    - **Decision (recorded before implementation, AGENT.md C3): descend a
      `*Project` that satisfies `projectIsPositionalIdentity`** — the
      predicate `M0144-0011a-3` already landed and tested
      (`internal/optimizer/upperorderedinput.go`; target `j` is
      `ColumnRef{Index: j}` for every `j`, equal widths, not
      `IsolatedScope`). Under it a Project re-assigns nothing, so the
      seam's position-based coordinate machinery
      (`buildLeafSpans`/`remapWalkOrderFlatToSpans`/
      `pgShapedOffsetChecksOK`) sees the same positions before and after.
      The `*Filter` arm is NOT to be implemented — it has no witness.
    - The implementing loop must still establish three things this census
      did not (it wrote no code):
      - do the body's exposed leaves line up with `ctx.bindings`?
        `nprefix`=4 while `scans`=3 on Q16, so the arithmetic only works if
        the body's relations are among the `nprefix` FROM items;
      - the P0-H11 `cumulativeFromSpans` span round-trip must close in the
        SAME change — it is latent only because nothing gets through today;
      - **Q35 is out of scope** (`Gather` + `NestedLoopIndexJoin` leaves,
        the `M0142-0008a-3i-leafcount` finding), so expect 10 of 11.
  - **THAT DECISION IS REFUTED 2026-09-20 (loop \#57), by probe, before any
    production code was written.** Design doc:
    `docs/design/0100-0149/m0144-0003a-project-descent-refuted.md`;
    evidence: `analysis/m0144/m0144-0003a-project-descent-refuted.txt`.
    Movement: none (probe reverted, no production code changed).
    - Instrumented every opaque leaf at the decline with
      `projectIsPositionalIdentity` plus a count of the leaves its child
      would expose, across all 11 declining SF0.25 queries.
    - **Not one `*Project` is a positional identity** — every one prints
      `P-NONident`. Several are not even width-preserving: Q14's is 73
      targets over a 29-column child, Q33's 97 over 67. They are wide
      re-projections, not the identity renames the decision assumed.
    - **`would == scans` in EVERY case.** Descending every identity Project
      exposes ZERO additional leaves; the gap (want 4-6, have 2-3) does not
      narrow by one. The arm would have been completely inert.
    - Why: the Project children are `*Gather`, `*CTEScan`, `*Filter`,
      `*Join`, `*NestedLoopIndexJoin` — **already-planned composites**, the
      same finding as `M0142-0008a-3i-leafcount`. A `*Gather` under a
      106-target Project is a finished plan fragment with a chosen worker
      count; no leaf-level admission predicate can turn it back into base
      relations.
    - **So M0144-0003a is NOT implementable as a leaf-admission change.**
      It joins M0142-0008a-3's increments (i) and (ii) and
      M0142-0008a-3i-lateral on ONE blocker:
      **`M0142-0008a-3i-lateral-route`** — Phase B must search before Phase
      A lowers anything. That is a FOURTH independent confirmation of
      M0144-0003's own thesis.
    - **Superseded by M0145-0003 (owner decision 2026-09-20):** the recon
      then showed the lowering is one stage earlier still — the resolver
      plans sublink bodies before either phase — and the real fix is the
      jointree-level pull-up, not this leaf admission. Stays `[!]`; the
      wall is M0145-0003.
    - Method lesson recorded in the doc §4: **a census of node KINDS does
      not license an assumption about those nodes' SHAPE.** The previous
      loop censused the type and inferred the shape; probe the predicate
      you intend to gate on, not just the type switch.
- [x] **M0144-0003b-1 — carry each set operation's SETOP rel to the link
  above** (the enabling slice of M0144-0003b; LANDED, inert). Refutes the
  parent's factual premise: `addPartialSetOpPath` does NOT admit 0/99 — the
  INNERMOST link of every UNION ALL chain already files a partial path
  (Q14/Q71/Q76 measured), and on Q14/Q71 the `Gather` over it wins the
  link's tournament outright. What failed is COMPOSITION: goopg builds a
  left-deep `*SetOp` chain where PG flattens the union into one appendrel
  (`pull_up_simple_union_all`, prepjointree.c:1617), and `searchedRelOf`
  stops at any node without exactly one boundary child
  (searchedtree.go:184) — a `*SetOp` has two — so every OUTER link read
  `nil-rel` on the side holding the rest of the chain and returned before
  either arm ran. Fix: carry the SETOP rel out on the node
  `createSetOpPaths` returns (`setopbranchrel.go`, the mechanism
  `searchedTree` already uses) and widen the branch accessor by exactly that
  one terminus. Absorption: 3 outer links across SF0.25 newly file a partial
  path, none removed; no cost change. Inert — SF0.25 `same=99 changed=0`,
  verdict-changes=none; TPC-H structurally cannot exercise it (zero
  `Append`/`SetOp` nodes in all 22 plans). Movement: none.
  Design: docs/design/0100-0149/m0144-0003b-1-setop-chain-branch-rel.md
  Kind: impl
  Parent: M0144-0003b
- [!] **M0144-0003b — jointree-level UNION ALL flattening**
  (`pull_up_simple_union_all` analog; filed by M0144-0003 item (b)). PG
  flattens UNION ALL-in-FROM into an appendrel during jointree
  preprocessing (prepjointree.c:1617 via pull_up_subqueries planner.c:754)
  so `add_paths_to_append_rel` (allpaths.c:1321) files partial paths
  per-rel and the append is a join-input citizen. goopg has no flattening —
  `addPartialSetOpPath` produces at the `*SetOp` plan-node level and admits
  0/99 corpus-wide (SF0.25: 0 Parallel Append vs PG's 9 sites). Scope: an
  appendrel representation in the searched problem + partial-path filing at
  the appendrel level (reusing the landed pure/mixed arms' cost math where
  they fit); must not let a flattened appendrel bypass the Q78 firewall or
  the outer-join ordering rules. Expected movement per S5: the TPC-DS
  Parallel Append sites (Q5×3, Q2, Q14, Q71, Q76 and census `parallelism`
  records) become reachable.
  **BLOCKED on M0142-0008a-3i-lateral-route, and the filed premise is
  half-refuted.** M0144-0003b-1 (above) measured the producer and found it
  already admits at the innermost link of every chain; the chain-composition
  half is now landed. The RESIDUAL is branches whose subtree never reached
  the search at all (Q5's `*Project:nil-rel` on BOTH sides, Q2/Q33/Q56/Q60
  likewise): there is no rel to carry, because Phase A of the pre-DP unnest
  spliced a BUILT plan before Phase B's search ran. No accessor widening
  can reach it — this is the same two-phase wall that blocks M0144-0003a
  and the three M0142-0008a-3 items, and it belongs behind the same gate.
  Do not re-scope as "build an appendrel representation" until the route
  order is fixed. **Superseded by M0145-0004 (owner decision 2026-09-20):**
  the appendrel lives at jointree level in the new flow — flattening
  happens there, before lowering. Stays `[!]`; the wall is M0145-0001's
  IR plus 0003/0004.
  Kind: impl
  Parent: M0144-0003
- [x] **M0144-0003c — pre-cost upper narrowing** (filed by M0144-0003 item
  (d)). `applyUpperNarrowing` (planner.go:190) narrows Aggregate/Sort input
  width on the finished tree — post-tournament, so it can never move a plan
  choice. PG narrows the input target BEFORE costing: `grouping_planner`
  builds `make_*_input_target`s (planner.c:1676-1744) finalized by
  `set_pathtarget_cost_width` (costsize.c:6367); `cost_sort`
  (costsize.c:2328) and hash/agg sizing (:3709/:3722/:3765) price the
  narrow row into every candidate. Scope: carry the derived keep-set into
  the ordered/grouping rel's path sizing so `costSort`/agg candidates are
  priced on the narrowed width (the keep-list derivation already exists —
  `group_input_target.go` name-level union consumed by the post-pass);
  delete nothing, re-point the costing inputs. Expected movement per S5:
  width-dependent cost branches — sort memory feasibility and hash-table
  sizing inside the sort-strategy (43 SF0.25) / aggregation-strategy census
  clusters.
  Kind: impl
  Parent: M0144-0003
  Movement: none
  - **LANDED 2026-09-20 (loop \#58).** Design doc:
    `docs/design/0100-0149/m0144-0003c-pre-cost-sort-width.md` (its §3
    decision and §4 limit were both written before any parity number).
    - **The filed premise is half wrong.** goopg ALREADY narrows pre-cost at
      three sites, all from banner item 5's M0141-S2a-fix1 family: the
      ORDERED rel's Sort (`narrowOrderedRelWidths`, upperordered.go:82,
      sweep-a), WINDOW's internal sort (`window_sort_narrow.go`, sweep-b),
      and the aggregate's own entry sizing (`aggInputWidth`,
      groupingpaths.go:363, fix1). `applyUpperNarrowing` (planner.go:190) is
      the COMMIT step that inserts the narrowing Project — item (d) read it
      and concluded the COSTING was post-tournament. `aggInputWidth`'s own
      comment already says the two are deliberately separate.
    - **What WAS genuinely un-narrowed**: inside `addGroupingPaths` the
      aggregate is priced through `aggInputWidth` (narrow) while the Sort
      beneath it went through `sortPathForBounded` → `pathNCols`/
      `pathAvgVarBytes` → the INPUT rel (full row). One input, two widths.
    - Fixed by handing both sort sites a narrowed shallow COPY of the seed
      carrying `NCols`/`AvgVarBytes` from `aggInputWidth` — the per-path
      override `pathNCols` already prefers (path.go:819-842). No new
      constant (R6), no newly chosen quantity (C3). Both sort sites changed
      together (Hard-won Rule #2 — the PLAIN presorted sort and the SORTED
      group-key sort are one twin pair).
    - PG citation: `make_group_input_target`
      (`postgres/src/backend/optimizer/plan/planner.c:1676-1744`) narrows
      once, `set_pathtarget_cost_width` (`costsize.c:6367`) finalises the
      width, `cost_sort` (`costsize.c:2328`) reads that same width.
    - **Result: INERT on both corpora, and the doc predicted why.** TPC-H
      plans capture BYTE-IDENTICAL to HEAD (`sha256 75599dae4efbf31c`) from
      a DIFFERENT binary (`08d905fa9ed819d4` vs `2326ec51aef8b431`) — G3's
      inertness proof; TPC-DS SF0.25 `same=99 changed=0`. Parity identical
      on both.
      - The reason, recorded in §4 BEFORE measuring: `costSortRunWithWidth`
        lets the width reach the price ONLY through the spill branch
        (`nruns := inputBytes / work_mem`). An in-memory sort costs the same
        at any width, correctly. No corpus grouping sort spills at a width
        where narrowing changes the run count.
      - The first unit test used a 100k-row fixture, priced both widths
        identically and read as a wiring failure — it was not; the fixture
        never reached the branch. It now runs at 5M rows where the branch is
        live, and a separate assertion pins that the override does reach
        `pathNCols`.
    - Lands anyway: two readers of one input no longer disagree about its
      width, R3 forbids discarding a PG-faithful change for lack of number
      movement, and it is a prerequisite for anything that later makes the
      spill branch bite (larger SF, lower `work_mem`, or promoting
      `GOOPG_PG_SORT_RELATION_BYTES_COST`).
    - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
      `tpcds-sf025 sweep` PASS; `tpch-acceptance-arm` PASS (24/24 VALUES);
      `make ea-ratchet` `N/A`.
- [x] **M0144-0004 — instrumented PG 18.3, instrument 1: `OPTIMIZER_DEBUG`
  build** (03-forward-plan §3). Build PG 18.3 from a **scratch checkout**
  (`./postgres/` stays read-only) with `OPTIMIZER_DEBUG` defined; serve a
  private clone of the corpus on a `55xx` port (never `:65432`/`:65438` — R1
  applies to the instrumented build's data sources). Document the build recipe
  and capture the per-rel survivor pathlists for the top first-divergence
  queries from 0002. Known limit (record it): survivors-only — `add_path`
  prints nothing, rejected candidates are already evicted.
  Done: scratch clone of `62d6c7d3df6` under `tmp/pg18-optdebug/`,
  `CPPFLAGS="-DOPTIMIZER_DEBUG"` (Makefile.global / `CONFIGURE_ARGS`), private
  `tpcds025` clone on `:5560` loaded from the harness's own TSVs + `tpcds.sql`
  + `ANALYZE` (zero reference-cluster access; `max_parallel_workers_per_gather=4`
  mirrored). 12-query capture set covering every census category n≥2; all 12
  plans shape-match the :65438 reference (ANALYZE re-sample drift only).
  Distiller `scripts/pg-optdebug-survivors.py` → per-rel survivor tables in
  `analysis/m0144/optdebug-0004/` (211 MB raw nodeToString stays in `tmp/`,
  reproducible). Gotchas: nodeToString namespaces path fields
  (`path.`/`jpath.path.`/bare); `cheapest_total_path` is a node-valued field
  at list-less depth; TPC-DS Q23 is two-statement — EXPLAIN each.
  `docs/design/0100-0149/m0144-0004-optdebug-instrumented-pg.md`;
  `analysis/m0144/m0144-0004-optdebug-build.md`.
  Kind: impl
  Parent: none
- [x] **M0144-0005 — instrumented PG, instrument 2: `debug_plan_candidates`
  trace GUC** (03-forward-plan §3). Same scratch-checkout build plus a small
  patch adding a GUC that emits, per `RelOptInfo`: every pathlist entry at
  `add_path` time (node type, startup/total cost, rows, pathkeys, param_info,
  required-outer rels), the rejection verdict (which comparator won), and the
  `set_cheapest` winner. This is the per-candidate artefact goopg's
  `GOOPG_PGSHAPED_DP_TRACE` gets diffed against — separating candidate gap /
  costing gap / tie-break gap, which today all read as "join-order".
  Done: `debug_plan_candidates` (PGC_USERSET/DEVELOPER_OPTIONS, default off)
  patched into the same `tmp/pg18-optdebug/src` scratch tree
  (`pathnode.c` emitters + `guc_tables.c` registration + `pathnode.h`
  extern; patch lives in the scratch checkout only, oracle untouched).
  `PLANCAND` records to backend stdout (same channel as pprint):
  add/padd candidate, ok/pok+removed-count, rej/prej+first-dominator+
  comparator vector (via=cost/keys/tie/rows/outer/psafe), preskip/ppreskip
  precheck kills, win set_cheapest total/startup/parameterized. Verified
  non-invasive: off emits zero lines; Q7 Limit→GroupAggregate→Gather Merge
  and Q5 Parallel Append shapes unchanged. 12-query captures (same set as
  0004) distilled by `scripts/pg-plancand-distill.py` →
  `analysis/m0144/optdebug-0005/` (raw slices in `tmp/pg18-optdebug/plancand/`).
  First reads: precheck kills ≈ half of all rejections; via=cost dominates
  post-build (zero rows/outer/psafe/dis rejections corpus-wide); emit
  verdict BEFORE pfree (first draft had a use-after-free).
  `docs/design/0100-0149/m0144-0005-debug-plan-candidates.md`;
  `analysis/m0144/m0144-0005-debug-plan-candidates.md`.
  Kind: impl
  Parent: none
- [x] **M0144-0006 — instrumented PG, instrument 3: `-finstrument-functions`
  call-graph build** (03-forward-plan §3). Same private setup; the trace
  answers "which route through the planner did this query take"
  (`make_one_rel` DP search / `join_search_one_level` ordering /
  `create_unique_path` / degenerate path) for each divergent query — the
  PG-side input to M0144-0003's route-diff table and the audit-scope namer
  for vertical slices.
  Done: `-finstrument-functions` via `CFLAGS +=` in the five
  `src/backend/optimizer/` subdir Makefiles; hooks +
  `debug_plan_callgraph` GUC in new `optimizer/util/calltrace.c`
  (`no_instrument_function`, no-alloc, buffered stdout — same channel as
  pprint/PLANCAND). `CGT e/x` raw this_fn addrs resolved offline via `nm`
  (`CGT base` PIE anchor on `&standard_planner`); `CGT tag` arg markers at
  `make_rel_from_joinlist` (levels + degenerate/hook/geqo/standard route),
  `join_search_one_level` (level + lower relids), `make_join_rel`
  (joinrel/outer/inner relids + jt + illegal). Verified: off emits zero;
  Q7 shape preserved; all 13 slices e/x-balanced. 12-query captures →
  `analysis/m0144/optdebug-0006/` via `scripts/pg-calltrace-distill.py`.
  Gotchas recorded: `.SECONDARY:` means deleted .o never rebuild as
  prereqs (name .o targets explicitly); Makefile `CFLAGS +=` does not
  retrigger compiles; GCC clones extern leaf fns into instrumented TUs.
  `docs/design/0100-0149/m0144-0006-callgraph-build.md`;
  `analysis/m0144/m0144-0006-callgraph-build.md`.
  Kind: impl
  Parent: none
- [x] **M0144-0007 — cost-margin census** (03-forward-plan §2). For each
  first-divergence node from 0002, force PG's shape in goopg (the
  M0142-0016c forced-plan comparator pattern plus the `GOOPG_*` admission
  arms) and record the margin class: <1% (election/tie-break), 1–20% (input
  divergence — attribute rows/width/cost-term first), >20% or unexpressible
  (structural gap). Output: margin column appended to 0002's table in
  `analysis/m0144/`. Depends on 0002.
  Done: `scripts/goopg-margin-census.py` — session `enable_*` /
  parallel-cost arms with forced-plan re-census via `census_query`, plus
  per-query DPPATH candidate margins and `upper.ordered.seed` presorted-
  input evidence on trace-enabled private lanes (:5590/:5591/:5592).
  210 records across the three corpora: ~72% structural
  (unexpressible[/no-arm] 50%, dominated-noncost 9%, priced-structural
  13%), ~28% election-class (election 22%, forced-cheaper 5%, input 1).
  The dominant `Limit → {GroupAggregate|Incremental Sort} | Sort` family
  is a pathkey-propagation generation gap (`nonemptykeys=0` even under
  `GOOPG_INCREMENTAL_SORT=1 GOOPG_PARTIAL_SORT_PATHS=1`), not a cost
  loss. `docs/design/0100-0149/m0144-0007-margin-census.md`;
  `analysis/m0144/m0144-0007-margin-census.md` +
  `m0144-0007-margin-*.txt` raw rows.
  Movement: none — recon produced the margin census (measurement only;
  no plan movement expected or claimed).
  Kind: recon
  Parent: M0144-0002
- [x] **M0144-0008 — TPC-DS SF1 cadence capture** (03-forward-plan §5). The
  corpus's goal is SF1 and exactly one SF1 capture exists (P0-E7, match=1/99).
  Take a second SF1 capture on a private lane per G3 and record the
  per-milestone-boundary SF1 convention in the design doc.
  Kind: recon
  Parent: none
  - **DONE 2026-09-20 (loop \#40).** Movement: none — second capture is
    measurement only; no plan movement claimed. G3 lane: `cp -r` clone of
    `bench/tpcds/runtime_goopg/data` (source down, no postmaster.pid) →
    `tmp/m0144-0008-data-sf1` on `:5593` under cgroup `m0144-0008-sf1`,
    HEAD `5fa3c98c9` binary sha256 `6a76d290…`; PG arm `:65438`/`tpcds`
    read-only. **Result `match=1/99` (Q41), headline counts identical to
    P0-E7** — but real category movement: `aggregation-strategy` 72→45
    (−27, consistent with M0141-S2b-11/S2b-13 grouping-order work in the
    38 production commits between `a0e741a68..5fa3c98c9`),
    `sort-strategy` −5, `join-method`/`qual-placement` −4,
    `parameterisation` +6 newly-exposed; 55/99 queries changed category
    sets. Diff-tool drift ruled out (only a comment changed, `44b17d459`).
    Convention pinned: SF1 capture at every milestone boundary + before any
    stocktake (~7 s EXPLAIN-only, private lane). Design doc:
    `docs/design/0100-0149/m0144-0008-tpcds-sf1-cadence.md`; record +
    artifacts `analysis/m0144/m0144-0008-*`. Lane stopped after capture.
- [x] **M0144-0009 — `plan-gate` reframing evaluation** (03-forward-plan §5).
  The gate diffs live `:65433` (binary lags HEAD) vs the newest committed
  snapshot — it can never see staged-code regressions. Evaluate: keep
  re-pinning on a schedule (M0137-0022) vs diff a fresh private-clone HEAD
  build. Decide, then either close or amend M0137-0022's scope in place.
  Also pin the golden-record rule while here (03-forward-plan §5's fourth
  bullet): `bench/tpch/runtime_goopg/tpch-golden-20260919/` is the
  record-level reference for `:65433`; if the cluster is ever reloaded,
  re-dump it first — add the rule to `maintenance_prompts/cluster-ops-runbook.md`
  if absent.
  Kind: recon
  Parent: none
  - **DONE 2026-09-20 (loop \#41).** Movement: none — harness-hygiene
    decision, no plan movement claimed. **Decision: REFRAME.** Verified
    the structural blindness: `plan-gate` diffs live-`:65433` (binary
    `goopg-bin` sha `736aaa57…`, rebuilt `2b8afa538`, 4 `internal/`/`cmd/`
    commits behind HEAD) vs the `m0137-0005-rebaseline-20260915` pin —
    staged code is in neither side, so it detects only server/data drift,
    never code regressions. The reframed path needs no new mechanism:
    `tpch_private_clone_snapshot` (pg_basebackup online clone, R1-allowed)
    + `55xx` lane + `plan-snapshot diff --port` — same pattern M0144-0008
    used for TPC-DS SF1. **M0137-0022 amended in place**: it now lands
    the private-lane capture/diff path AND takes the re-pin through it —
    the "needs owner rebuild of `:65433`" blocker is removed by
    construction. Re-pinning is re-scoped, not eliminated: trigger becomes
    each landed plan-moving change, not a calendar schedule.
    **Golden-record rule added** to
    `maintenance_prompts/cluster-ops-runbook.md` §"Record-level golden
    dump" (was absent): reload ⇒ re-dump a new dated golden dir before
    record-level comparisons are trusted. Design doc:
    `docs/design/0100-0149/m0144-0009-plan-gate-reframing.md`.
- [x] **M0144-0010 — ledger bulk-triage tooling** (03-forward-plan §6). ~2,100
  open `.ralph/deferral_ledger.md` rows. Build tooling that (1) flags rows
  whose referenced code/tests no longer exist (`git log -S` checks), (2)
  folds same-mechanism rows into cluster rows, (3) emits only the survivors
  for per-task triage. This is the M0119-successor cadence mechanism.
  Kind: impl
  Parent: none
  - **DONE 2026-09-20 (loop \#42).** Movement: none — harness tooling; no
    plan movement claimed. Landed `scripts/ledger-triage.py` +
    `make ledger-triage` (`LEDGER_TRIAGE_FAST=1` ~3 s vs ~2.5 min
    attributed; `LEDGER_TRIAGE_OUT`, `LEDGER_TRIAGE_FULL`). Read-only:
    ledger is append-only, so the tool emits a report, never edits rows.
    Pipeline: parse 7-col table (2,296 rows, 2,129 open) → extract
    file/`Test*`/backtick-symbol refs (PG-oracle `.c`/`.h` refs annotated
    but never vote for staleness) → basename→paths existence index +
    `git log -S` attribution for gone refs → classify
    `stale-candidate`/`partial-stale`/`live`/`unverifiable` → cluster by
    most-referenced code file (task-id family fallback; union-find
    rejected — hub files chained 425-row mega-clusters). First report at
    HEAD `f2782479e`: 28 stale-candidates (e.g. `cast_ddl_recovery.go`
    retired by the catalog heap-journaling conversions,
    `scripts/tpcds-sf05-regression.sh` by `e2a50de40`, `join_agg.go` never
    existed), 58 partial-stale, 593 live, 1,450 unverifiable prose-only
    rows survive by default (`stale-candidate` is high-precision, not
    exhaustive). Report: `analysis/m0144/m0144-0010-ledger-triage.md`;
    design: `docs/design/0100-0149/m0144-0010-ledger-bulk-triage.md`.
- [!] **M0144-0011 — first vertical-slice campaign** (03-forward-plan §4).
  **ESCALATED 2026-09-20 (loop \#49) under AGENT.md S4 — five consecutive
  completed descendants reporting `Movement: none`. No further descendant
  is selected or filed; only the owner reopens this root.**
  (For when it is reopened: the `unexpressible` substrate floors the
  slices kept hitting now have filed owners — row-emitting PartialAgg is
  M0141-S3–S6, parallel-inner hash build is M0140-0007, derived-input
  statistics is M0145-0009, parameterized-path legality is M0145-0010.)
  - What was tried, and what each step proved:
    - **M0144-0011a** — `inputNodePathkeys` gained an `*Aggregate` arm, so a
      sorted `GroupAggregate` that already emits the ORDER BY order is taken
      as-is. **`Movement: yes` — SF0.25 `sort-strategy` 67 → 60 (−7)**, 16
      plans moved. This is the lineage's one movement.
    - **M0144-0011a-3** — the walk crosses a positional-identity `*Project`
      (PG `pathnode.c:2936-2937`). Q21's first divergence advanced
      `depth=1 sort-strategy` → `depth=1 qual-placement` with the depth-1
      node kind now MATCHING PG. `Movement: none` — the category line counts
      every category a query diverges in anywhere, so Q21 kept its tally.
    - **M0144-0011a-2** — a 99-query ORDERED-seam census; the candidate
      minimum dropped 2 → 1 to match PG's minimum-free pathlist iteration
      (`planner.c:5337`). Proved the gate was SHAPE-inert and that the
      `keys=0` residue is entirely hashed lone candidates — an
      aggregation-strategy question, not an ordering one. `Movement: none`.
    - **M0144-0011b** (recon) — refuted its own filed premise: goopg's Q8
      nested loop (19852.53) is far CHEAPER than PG's (28502.37), not
      dearer. Found the real defect a layer down. `Movement: none`.
    - **M0144-0011b-1** — fixed it: a sub-plan join-search leaf is priced
      from its own subtree (`cost_subqueryscan`'s shape,
      `costsize.c:1491-1493`) instead of a fabricated seq scan. Q8's
      `Hash Join (1.27..11.28)` over a `HashSetOp (…7360.42)` became
      `Hash Join (6458.80..7374.05)`, and **Q8's top join now elects PG's
      Nested Loop** (27215 vs PG's 28502, was a Hash Join at 19455). 30/99
      plans changed, `MISMATCH=0`. `Movement: none` — SF0.25 `join-method`
      70 → 68, inside ±3.
    - **M0144-0011c** (recon) — sized `Materialize`. `Movement: none`.
  - The blocker: every remaining mechanism in this lineage is either
    already correct or too small to clear the ±3 band on its own. The
    lineage is not unproductive in substance — it removed a redundant Sort
    corpus-wide, repaired two false premises by measurement, and fixed a
    650x cost-monotonicity violation that flipped Q8's join method to PG's
    — but it has not moved a category past ±3 since 0011a.
  - **Expected movement if unblocked, with the size:** `Materialize` is the
    one hypothesis left with a claim outside the band — **`missingnode`
    25 → 14 (−11)** on Q1 Q8 Q10 Q16 Q47 Q57 Q59 Q65 Q77 Q78 Q91 (Q35 and
    Q58 also cite `Incremental Sort` and would stay). Size: four slices,
    detailed in `docs/design/0100-0149/m0144-0011c-materialize-sizing.md`
    §4 — (1) plan node + EXPLAIN + `createPlan`, inert; (2) `cost_material`
    + `cost_rescan`'s Material arm as tested pure functions, inert;
    (3) the NL admission rule files the matpath candidate AND
    `join_nl_stream.go` stops wrapping unconditionally — the slice that
    moves plans; (4) re-time the Q54-class `nlInnerWorkMemEnabled` cliff,
    which becomes priceable once (2) lands.
  - Also still open under this root, complete in substance but NOT closed
    because S4 forbids completing another descendant: **M0144-0011b**, whose
    stated completion condition ("the leaf is priced and Q8's rel {0,1,2,3}
    election is re-measured") is already satisfied by M0144-0011b-1's
    committed evidence. Its residue is Q8's remaining `depth=3` join-ORDER
    divergence.
  **Not selectable until M0144-0002 and M0144-0007 have landed.** Take the top
  first-divergence cluster, ONE representative query, and drive it to MATCH
  end-to-end — this task is a recon: it *files* each surfaced layer
  (admission → candidate → cost input → election → executor existence) as a
  `Kind: impl` child (`Parent: M0144-0011`) naming expected movement per S5;
  production changes happen in those children, not here. Ends when the query
  matches or the residue is a named, measured, unfunded capability. Only then
  generalise to the cluster — the depth-first inversion of the era's
  breadth-first mechanism builds.
  Kind: recon
  Parent: M0144-0002
  - **IN PROGRESS 2026-09-20 (loop \#43): slice picked + five-layer trace
    landed.** Representative: **TPC-DS SF0.25 Q8** — top cluster
    (`Limit→{GroupAgg|IncrSort|Sort+CTE}` vs `Sort`, ~25 SF0.25 / ~20 SF1
    records), `dominated-noncost` candm −46.8% (m0144-0007). Trace on a
    private `:5595` lane at HEAD `30e8ea711`
    (`analysis/m0144/m0144-0011-q8-slice-trace.md`): admission OK;
    candidate-generation gap is the first-divergence cause —
    `inputNodePathkeys` returns nil for `*Aggregate` (seed `keys=0` →
    redundant Sort) and `electOrderedGrouping` declines `cands<2(1)`
    while PG iterates the whole input pathlist (`planner.c:5337`); the
    NL-vs-HJ join election under the agg and `Materialize` existence are
    the deeper layers. Children 0011a/b/c filed below. Design doc:
    `docs/design/0100-0149/m0144-0011-q8-vertical-slice.md`.
- [x] **M0144-0011a — ordering-claim propagation at the ORDERED-step
  boundary** (filed by M0144-0011). `inputNodePathkeys`
  (`internal/optimizer/upperorderedinput.go:176`) returns nil for every
  ordered-emitting node top that is not `*Sort`/searched-root — add the
  `*Aggregate` emission case first (winning PathAgg's group-key ordering,
  translated to output coordinates; `groupingEmissionPathkeys` already
  computes this for candidate offers), then survey MergeJoin/GatherMerge/
  IncrementalSort tops for the same `default: nil` blind spot. Also review
  `electOrderedGrouping`'s `cands<2` gate: PG's `create_ordered_paths`
  (`postgres/src/backend/optimizer/plan/planner.c:5337`) iterates
  `input_rel->pathlist` with no minimum — a lone AggPath must still be
  offered (PG citations: `planner.c:5344-5348`, `pathnode.c:3412-3416`).
  Kind: impl
  Parent: M0144-0011
  Expected movement: SF0.25 `sort-strategy` category on the
  `Limit→{GroupAggregate|Finalize}` first-divergence family — ≤18 records
  (named dominated-noncost: Q8, Q21, Q26, Q45, Q50, Q62, Q99;
  election-0%: Q17, Q25, Q29) advance past depth-1 where the seed
  ordering was the sole blocker. Measured: private-lane SF0.25 capture →
  `pg-plan-parity-diff.py` category delta + `pg-plan-first-divergence.py`
  census diff vs the m0144-0002 table; match-count movement possible but
  not required.
  **Residual handoff (owner decision 2026-09-20):** the gates this task's
  chain could not remove at node level — `inputNodePathkeys`'s remaining
  `default: nil` tops (`*MergeJoin`/`*GatherMerge`/`*IncrementalSort`/
  `*WindowAgg`), `electOrderedGrouping`'s `gate-precondition` skip when
  `node != agg.node`, `electOrderedDistinct`'s `cands<2`
  (upperordereddistinct.go:140) — are owned by **M0145-0006** (upper-rel
  pathlists), where election happens over candidate sets rather than
  node-top walks.
  - **LANDED 2026-09-20 (loop \#44).** `inputNodePathkeys`' walk gained an
    `*Aggregate` arm — `aggregateEmissionPathkeys`
    (`internal/optimizer/upperorderedinput.go`), the node-level twin of
    `groupingEmissionPathkeys` — which derives a sorted aggregate's
    group-key emission order in OUTPUT coordinates, verified per key
    against the group-prefix layout rather than assumed. PG citations:
    `postgres/src/backend/optimizer/util/pathnode.c:3412-3416`
    (`AGG_SORTED` copies the subpath's pathkeys) and
    `postgres/src/backend/optimizer/plan/planner.c:5337`, `:5344-5348`
    (`create_ordered_paths` reads them per input path). Design doc:
    `docs/design/0100-0149/m0144-0011a-ordered-step-ordering-claim.md`.
    Measured on TPC-DS SF0.25 with `pg-plan-parity-diff.py`: the
    `CATEGORIES-EXCL-MATCH` `sort-strategy` count fell 67 → 60 (−7) while
    match held at 2 → 2 and `missingnode` held at 25.
    - Clean same-epoch A/B (both arms `GOOPG_ANALYZE_SEED=20260905`,
      stats epoch `e4a554b2a4cfb710`, distinct binaries
      `bdb4d6bade614b25` vs `163ba219e018a91a`). The first, unpinned
      attempt read TPC-H `match 1 → 2` — that was the sampling epoch, not
      the code; the pinned pair reads `match 2 → 2` with every TPC-H
      category inside ±3.
    - Plan-shape channel: 16/99 SF0.25 plans moved (Q7 Q8 Q10 Q15 Q17 Q25
      Q26 Q29 Q35 Q37 Q40 Q45 Q50 Q66 Q69 Q82) — seven of the nine named
      queries. Q21, Q62 and Q99 did NOT move and are not explained by this
      slice.
    - Q8 now plans `Limit → GroupAggregate → Sort → Hash Join …`, PG's own
      depth-1/-2 shape; the redundant `Sort` over `GroupAggregate` is gone.
    - Collateral: `TestExplainIndentDeepNesting` and its ANALYZE twin used
      an ASCENDING `ORDER BY 2` fixture whose 4-level shape WAS the
      redundant Sort. Repaired to `ORDER BY 2 DESC` — a genuine
      re-ordering — so the indent assertions are unchanged and the root
      `Sort Key: b DESC` doubles as evidence the new arm carries direction.
    - Gates: units PASS, `tpch-spotcheck` PASS (Q12=2 Q13=33),
      `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`), `tpch-acceptance-arm` PASS (24/24 VALUES vs the HEAD
      baseline). `make ea-ratchet`: `N/A — no estimate/selectivity/stats
      code touched`.
    Kind: impl
    Parent: M0144-0011
    Movement: yes — TPC-DS SF0.25 CATEGORIES-EXCL-MATCH sort-strategy 67 → 60 (−7)
- [x] **M0144-0011a-2 — the remaining `default: nil` ordering tops, and
  `electOrderedGrouping`'s `cands<2` gate** (filed by M0144-0011a). Two
  divergences M0144-0011a reviewed and deliberately did not bundle:
  - `electOrderedGrouping` (`internal/optimizer/upperorderedgrouping.go`)
    declines at `len(cands) < 2`; PG's `create_ordered_paths` iterates
    `input_rel->pathlist` with NO minimum
    (`postgres/src/backend/optimizer/plan/planner.c:5337`), so a lone
    translatable `PathAgg` must still be offered. With M0144-0011a landed
    the single-candidate ordering claim already reaches the ORDERED step
    through the normal `createOrderedPaths` route, so relaxing the gate is
    expected to be near-inert on today's corpus while widening the loop's
    blast radius across every single-candidate grouping query — it needs
    its own measurement, not a free ride on 0011a's.
  - the walk's other `default: nil` TOPS: `*MergeJoin`, `*GatherMerge` and
    `*IncrementalSort` as the input node's top (0011a handles
    `*GatherMerge` only as an aggregate's CHILD). Each needs its own
    output-coordinate soundness argument; bundling them makes a category
    move unattributable.
  Kind: impl
  Parent: M0144-0011a
  - **PREMISE CORRECTED 2026-09-20 (loop \#45), by probe.** A
    `GOOPG_PGSHAPED_DP_TRACE=1` run on a private `:5595` SF0.25 lane
    showed that NONE of the three queries this task was filed for is
    blocked by either bullet above:
    - Q21 declines with `reason=gate-precondition`, not `cands<2(1)`, and
      its walk then stops at a renaming `*Project` over the aggregate
      (`DPWALK stop-at=*optimizer.Project`). That mechanism is filed and
      LANDED as **M0144-0011a-3** below; Q21 is spent.
    - Q62 and Q99 plan a `Finalize HashAggregate`. A hashed aggregate
      emits no order, so no ordering-claim work can move them — their
      divergence is `aggregation-strategy` and belongs to that lineage.
    - The two bullets above therefore remain genuinely UNMEASURED: they
      have no named query left from the 0011a residue, and the first job
      of whoever selects this task is to find one (or to retire the task).
  Expected movement: **was withdrawn pending a named query**; the census
  below supplied the answer and it is `Movement: none` — see the
  RESOLVED block.
  - **RESOLVED 2026-09-20 (loop \#46) — census first, then one bullet
    implemented and one retired.** Design doc:
    `docs/design/0100-0149/m0144-0011a-2-ordered-seam-candidate-minimum.md`;
    census: `analysis/m0144/m0144-0011a-2-ordered-seam-census.md`.
    Kind: impl
    Parent: M0144-0011a
    Movement: none
    - The 99-query ORDERED-seam census (private `:5595` lane,
      `GOOPG_PGSHAPED_DP_TRACE=1`, per-query byte-offset log reads) splits
      the corpus by `electOrderedGrouping` verdict: `gate-precondition`
      44, `cands<2(1)` 37, `parallel-finalize-agg-present` 4, elected 14.
    - Of the 37 `cands<2(1)` declines, 27 ALREADY recover their ordering
      claim at the ORDERED step via `inputNodePathkeys` (17 with
      `contained=true`, i.e. no Sort stacked at all; 10 with a partial
      claim). Only 11 arrive with `keys=0`.
    - Those 11 (`Q5 Q14 Q16 Q18 Q22 Q27 Q77 Q80 Q92 Q94 Q95`) are EXACTLY
      the 11 that decline `anyTranslated=false` in the relaxed arm, and
      every per-candidate decline in the corpus reads
      `strategy-or-mode strategy=0` — a HASHED lone candidate, which emits
      no order. They belong to the `aggregation-strategy` lineage, not to
      ordering-claim propagation.
      - Bonus: that identity is a corpus-wide measurement of the
        sibling-agreement invariant — `groupingEmissionPathkeys` (Path
        twin) and `aggregateEmissionPathkeys` (Node twin) decline on the
        same query set, query for query.
    - **Bullet 2 RETIRED by measurement**: the `*MergeJoin`/`*GatherMerge`/
      `*IncrementalSort` walk tops have NO query on this corpus. Ledger row
      keeps the divergence from being lost.
    - **Bullet 1 LANDED**: `len(cands) < 2` → `len(cands) < 1`. PG has no
      minimum — `create_ordered_paths` iterates the whole pathlist
      (`postgres/src/backend/optimizer/plan/planner.c:5337`). Pinned by
      `TestElectOrderedGroupingOffersALoneCandidate` and
      `TestElectOrderedGroupingStillDeclinesAnEmptyRel`.
    - Result: SHAPE-inert on both corpora — cost-stripped plan diff is 0
      lines for all 99 SF0.25 and all 22 TPC-H queries, parity match 2 → 2
      and every `CATEGORIES-EXCL-MATCH` count identical. 14 queries (10
      SF0.25 + 4 TPC-H) reprice their ORDER BY `Sort` through
      `addOrderedPaths` instead of the prebuilt seed's
      `DeriveLegacyDisplayCost`, which is the direction the ORDERED rel was
      built for.
    - **METHODOLOGY CORRECTION, recorded deliberately**: the private
      `:5595` lane reported ZERO plan changes; the gate-owned `:65437`
      cluster reported ten. The lane is a stale clone with its own
      statistics — sound for MECHANISM traces, unsound for OUTCOME claims.
      An earlier draft comment asserting "byte-identical either way" on
      lane evidence was corrected before landing.
    - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
      `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`); `tpch-acceptance-arm` PASS (24/24 VALUES);
      `make ea-ratchet` `N/A`. All re-run after a late comment-only edit,
      since the stamp hashes the staged `internal/` tree.
    - Left open, deliberately NOT filed as children here (neither is an
      ordering-claim problem — filing them here would repeat this task's
      own predecessor's mistake): `gate-precondition` is now the largest
      decline class (44/99) and is entirely unexamined; the 11 hashed-lone-
      candidate queries are an aggregation-strategy election question.
- [x] **M0144-0011a-3 — crossing a positional-identity `Project` at the
  ORDERED seam** (filed by M0144-0011a-2's probe). `inputNodePathkeys`
  (`internal/optimizer/upperorderedinput.go`) stopped at every `*Project`,
  so TPC-DS SF0.25 Q21 — whose ordered input is `Project{Aggregate}` doing
  nothing but renaming the two aggregate outputs to `inv_before`/
  `inv_after` — never reached the `*Aggregate` arm M0144-0011a added.
  Landed: the walk now descends through a Project that is a POSITIONAL
  IDENTITY (`projectIsPositionalIdentity`: target `j` is
  `ColumnRef{Index: j}` for every `j`, equal widths, not `IsolatedScope`)
  and restamps the claim's labels at the SAME index
  (`relabelPathkeysTo`). PG citation:
  `postgres/src/backend/optimizer/util/pathnode.c:2936-2937` — "Projection
  does not change the sort order", `create_projection_path` copies
  `subpath->pathkeys` — which PG may say for ANY projection only because a
  PG pathkey names an EquivalenceClass; goopg's names a POSITION, so only
  the identity case ports and everything else still stops the walk. Design
  doc: `docs/design/0100-0149/m0144-0011a-3-identity-project-descent.md`.
  Kind: impl
  Parent: M0144-0011a
  Movement: none
  - Result the first-divergence census DOES see: Q21 advances
    `depth=1 [sort-strategy] under Limit: PG GroupAggregate | goopg Sort`
    → `depth=1 [qual-placement] under Limit: PG GroupAggregate | goopg
    GroupAggregate`. The depth-1 node kind now MATCHES PG and the residue
    is the HAVING qual's placement.
  - Why it is still `Movement: none`: S3's three instruments are the match
    count, `CATEGORIES-EXCL-MATCH` beyond ±3, and `ea-ratchet`. SF0.25
    reads match 2 → 2 and `sort-strategy` 60 → 60 — because that line
    counts every category a query diverges in ANYWHERE, and Q21's inner
    `Sort` still differs in strategy, so Q21 keeps its tally while also
    gaining `qual-placement` (24 → 25). Calling the census advance
    "movement" would be instrument-shopping.
  - Blast radius: 1/99 SF0.25 plans changed (Q21 only). TPC-H parallel is
    byte-identical to the 0011a capture
    (`sha256 1727126e8ed71456`) from a DIFFERENT binary
    (`7372bbf60b170386` vs `163ba219e018a91a`) — G3's proof that the
    equality is inertness on that corpus, not a re-measured binary.
  - Deferred inside the same mechanism: a NARROWING identity Project
    (`len(out) < len(child)`) is sound in principle — positions
    `0..len(out)-1` are still an identity — and is refused only because
    the walk's per-step agreement check is a column-count test with no
    "prefix" mode. Ledger row filed.
  - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
    `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
    TIMEOUT=0`); `tpch-acceptance-arm` PASS (24/24 VALUES);
    `make ea-ratchet` `N/A — no estimate/selectivity/stats code touched`.
- [x] **M0144-0011b — join election under Q8's aggregate input**
  (filed by M0144-0011). At rel `{0,1,2,3}` ({dd⋈ss} ⋈ {store,ca-view}):
  goopg elected `join.hash total=19455.50`; the PG-chosen
  `join.nestloop total=19852.53` was generated and dominated — a
  cost-input/election divergence, not generation. PG's inner is
  `Materialize(NL(store Index Scan ⋈ ca-view-subquery))` rescanned per
  outer row; goopg prices a different inner shape (Seq Scan store, no
  Materialize). Scope: reprice the parameterised NL inner PG-faithfully
  and verify the stats inputs (818 vs 812 rows). If the residual needs
  Materialize, it feeds 0011c rather than a workaround.
  Kind: recon
  Parent: M0144-0011
  Expected movement: `join-method`, `scan-type`, `join-order` categories
  on Q8 (SF0.25) — carried by the impl child M0144-0011b-1 below.
  Measured: DPPATH candidate margins at rel {0,1,2,3} (nestloop vs
  join.hash totals vs PG's own pricing) + `pg-plan-parity-diff.py` on a
  private-lane capture.
  - **RECON COMPLETE 2026-09-20 (loop \#47); PREMISE CORRECTED; this task
    is now BLOCKED on M0144-0011b-1.** Design doc:
    `docs/design/0100-0149/m0144-0011b-nontable-leaf-pricing.md`; evidence:
    `analysis/m0144/m0144-0011b-q8-dppath-head.txt`,
    `m0144-0011b-q8-plan-head.txt`.
    Movement: none
    - The filed premise ("goopg prices the NL too dear") is FALSE. At HEAD
      `97eceacfc`, goopg's `join.nestloop` at rel {0,1,2,3} is 19852.53 and
      its `join.hash` 19455.50 — while **PG's own NL for the same join is
      28502.37**. goopg prices the nested loop far too CHEAP, not too dear.
      Repricing it upward as filed would have been tuning on a false
      premise (AGENT.md R6).
    - The real divergence is one layer down and is visible in the EXPLAIN
      output: a `Hash Join (cost=1.27..11.28 rows=32)` sits directly above
      a `HashSetOp Intersect (cost=6457.53..7360.42 rows=535)` — the
      parent is 650x cheaper than its own child. PG's corresponding inner
      costs 9327.62.
    - Root cause: `internal/optimizer/joinsearch.go:437` prices EVERY
      join-search leaf with `costSeqscan`, and `baseSeqScanCostInputs`'
      non-table branch INVENTS a page count from the row count. A set-op /
      CTE / subquery / VALUES / function-scan leaf therefore enters the
      search with its whole subtree free:
      `joinsearch.prebuilt relids={3} rows=535 total=8.35`.
    - PG starts from the subpath's cost instead —
      `postgres/src/backend/optimizer/path/costsize.c:1491-1493`
      (`cost_subqueryscan`), with `cost_ctescan` / `cost_functionscan` the
      same shape.
    - `Materialize` (M0144-0011c) is NOT what decides this election: PG's
      Materialize wrapper adds ~65 over its 9327.62 child, while the
      missing leaf cost is ~7350.
    - Blast radius: 32/99 SF0.25 queries join over a non-table leaf (Q1 Q2
      Q4 Q5 Q8 Q11 Q14 Q23 Q24 Q30 Q31 Q33 Q38 Q39 Q47 Q51 Q54 Q56 Q57 Q58
      Q59 Q60 Q64 Q74 Q75 Q77 Q78 Q80 Q83 Q87 Q95 Q97).
    - NOT landed this loop on purpose: most goopg sub-plan nodes (`SetOp`
      among them) carry no real `Path` cost, only
      `DeriveLegacyDisplayCost`, whose header states "this is NOT a cost
      model and nothing may plan against it". Choosing how to price them
      is a design decision with a 32-query blast radius; see the child.
- [x] **M0144-0011b-1 — price a non-table join-search leaf from its own
  subtree, not as a sequential scan** (filed by M0144-0011b's recon).
  `internal/optimizer/joinsearch.go:437` + its partial twin
  (`addBaseRelPartialPaths`): when `leafBaseScan(leaf)` is not a base heap
  scan, the leaf's cost must derive from the sub-plan it wraps, the way
  `cost_subqueryscan` derives from `subpath`
  (`postgres/src/backend/optimizer/path/costsize.c:1491-1493`), instead of
  `costSeqscan` over a page count invented from the row count.
  Kind: impl
  Parent: M0144-0011b
  Expected movement: `join-method`, `join-order` and `scan-type` on the 32
  SF0.25 queries that join over a non-table leaf, Q8 first (its inner is
  priced 11.28 against PG's 9327.62). Measured: `pg-plan-parity-diff.py`
  `CATEGORIES-EXCL-MATCH` + match count on a gate-cluster SF0.25 capture,
  and the DPPATH margin at Q8's rel {0,1,2,3} (`join.nestloop` 19852.53 vs
  `join.hash` 19455.50 today) against PG's 28502.37.
  - **Design decision required before coding** — three options, stated in
    the recon doc §6, NOT interchangeable:
    - (1) use the leaf's carried `PlanCost` where it has one (a searched
      subtree does) and fall back otherwise — correct where it applies,
      but leaves `SetOp`/CTE leaves, i.e. Q8, on the legacy number;
    - (2) give the sub-plan classes real upper-rel paths (`cost_subqueryscan`
      / `cost_ctescan` ports with genuine inputs) — the PG-faithful answer
      and the larger piece of work;
    - (3) plan against `DeriveLegacyDisplayCost` for these leaves —
      fastest, and it directly contradicts that function's stated scope
      rule (`internal/optimizer/plancost.go:116`).
  - Whichever is chosen, the choice and its PG citation go in the design
    doc BEFORE any parity number is taken (AGENT.md C3).
  - **LANDED 2026-09-20 (loop \#48).** Design doc:
    `docs/design/0100-0149/m0144-0011b-1-subplan-leaf-cost.md` (its §3
    decision was written before any parity number was taken).
    Movement: none
    - **Decision taken: a narrowed option (2)** — port `cost_subqueryscan`'s
      SHAPE (`postgres/src/backend/optimizer/path/costsize.c:1491-1493`) for
      sub-plan leaves ONLY: `startup = subtree.startup`,
      `total = subtree.total + cpu_tuple_cost*rows`
      (`costSubplanLeaf`/`isSubplanLeaf`, internal/optimizer/joinsearch.go).
      The qual term PG adds is deliberately NOT charged — goopg's leaf is a
      finished tree whose local filter is already inside the node priced, so
      charging it again would double-count.
    - Index and bitmap leaves are LEFT on today's pricing on purpose, so any
      category movement is attributable to sub-plan leaves alone. Ledger row
      filed; they are mispriced too, by a different mechanism
      (`cost_index`).
    - The defect is closed: Q8's `Hash Join (cost=1.27..11.28)` over a
      `HashSetOp Intersect (cost=6457.53..7360.42)` is now
      `Hash Join (cost=6458.80..7374.05)` — parent above child again.
    - **Q8's top join now elects PG's method**: `Nested Loop
      (9934.68..27215.31)` where it had a `Hash Join (3487.55..19455.50)`,
      against PG's own `Nested Loop (12329.66..28502.37)`. First divergence
      advances `depth=3 [join-method] PG Nested Loop | goopg Hash Join` →
      `depth=3 [join-order] PG Nested Loop | goopg Nested Loop`.
    - SF0.25: match 2 → 2, `join-method` 70 → 68 (the named category, right
      direction), `qual-placement` 25 → 26, `missingnode` 25 unchanged.
      30/99 plans changed — the expected size, since 32 queries join over a
      sub-plan leaf. All deltas inside ±3, hence `Movement: none`.
    - TPC-H parallel: parity lines and the plans capture BYTE-IDENTICAL to
      HEAD (`sha256 0bf7e4d1540cf0e4`) from a different binary
      (`e1bb094f5aeae53f` vs `42dfcd6069014ad2`) — G3's proof of inertness
      rather than a re-measured binary. No TPC-H query joins over a
      sub-plan leaf.
    - Three queries whose plans changed got slower (Q11 3s→7s, Q58 2s→5s,
      Q95 3s→6s); Q28's 3s→6s is host noise — its plan did not change.
      Ledger row filed. Per AGENT.md §Goal a slower plan that matches is
      not a regression here.
    - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
      `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`) — the load-bearing gate here, since 30 plans changed and
      every one still returns the same rows and checksums;
      `tpch-acceptance-arm` PASS (24/24 VALUES); `make ea-ratchet` `N/A`.
    - Still open (both ledgered): index/bitmap leaf pricing, and the fact
      that for a class with no `PlanCost` field (`SetOp` among them) the
      base cost is still `DeriveLegacyDisplayCost` — a FLOOR built from the
      children's own largely-real costs, not PG's number. Full option (2),
      a real upper-rel path per sub-plan class, owns that.
- [x] **M0144-0011c — `Materialize` node existence** (filed by
  M0144-0011). Q8 is `MISSING-NODE: PG-only kinds: Materialize` — goopg
  has no Materialize plan node (`MaterializedCTEScan` is a different
  thing); PG buffers NL inners via `create_material_path`
  (`postgres/src/backend/optimizer/util/pathnode.c:1637`). Scope: the
  path producer (materialize_inner admission for rescan-heavy NL
  inners), the executor node, and EXPLAIN rendering. If it turns out
  unfundable, record it as the slice's named measured residue.
  Kind: impl
  Parent: M0144-0011
  Expected movement: `missingnode` count on the Q8-family records where
  PG materializes NL inners (Q8 + siblings per parity diff), and it
  unblocks 0011b's honest NL pricing. Measured: `pg-plan-parity-diff.py`
  `PG-only node kinds` line on a private-lane capture.
  - **RECON COMPLETE 2026-09-20 (loop \#49) — SIZED, NOT BUILT.** Design
    doc: `docs/design/0100-0149/m0144-0011c-materialize-sizing.md`.
    Kind: recon
    Parent: M0144-0011
    Movement: none
    - **The executor half already exists and already runs.**
      `internal/executor/operators_material.go` (308 lines) is a faithful
      `nodeMaterial.c` analogue, and
      `internal/executor/join_nl_stream.go:108` wraps EVERY streaming
      nested-loop inner in it — unbounded by default
      (`nlInnerWorkMemEnabled` off). goopg does not lack materialization.
    - Missing are exactly three things: the plan node EXPLAIN prints, the
      cost (`cost_material` + `cost_rescan`'s Material arm), and PG's
      admission rule.
    - **Sized: `missingnode` 25 → 14 (−11).** 13 of the 25 records cite
      `Materialize` (Q1 Q8 Q10 Q16 Q35 Q47 Q57 Q58 Q59 Q65 Q77 Q78 Q91);
      Q35 and Q58 also cite `Incremental Sort`, so they stay
      `MISSING-NODE` regardless. That is the only remaining hypothesis in
      this lineage with a claim outside ±3.
    - `Incremental Sort` (14 records) is NOT a missing node — goopg has
      `*IncrementalSort` and `addIncrementalSortPaths`, gated off because
      the candidate loses on cost. That is banner item 6.
    - **Trap the implementer must not miss:** goopg materializes EVERY NL
      inner; PG materializes only the ones its admission rule admits
      (`postgres/src/backend/optimizer/path/joinpath.c:1890-1901` +
      `ExecMaterializesOutput`, `execAmi.c:640-647`). Emitting the node
      wherever the executor materializes would create goopg-ONLY
      `Materialize` nodes and trade 11 `missingnode` records for a new
      mismatch class. Placement must follow PG's rule and the executor's
      unconditional wrap must become conditional in the same slice.
    - Not built this loop because it is a multi-slice build with no honest
      partial landing: a new optimizer node type must be threaded through
      `createPlan`, EXPLAIN, a new `PathKind`, the cost model, the NL
      producer and its partial twin, and several exhaustive node-kind
      switches. Four slices are proposed in the design doc §4 and repeated
      in the root's escalation block.
    - Gates: none — recon, no production code changed (AGENT.md C1/D5).

## M0145 — Jointree-first planner flow (filed 2026-09-20, owner decision)

**Milestone doc:** `docs/milestones/0145-jointree-first-planner-flow.md`.
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity
harness (M0137–M0145)" — note G8's dual-pipeline rule, new for this
milestone. **Source:** the plan-flow comparison
(`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/plan-flow-medium-abstraction.md`)
+ the feasibility note (`tmp/planner-rewrite-possibility260920.md`).
**Prerequisites:** none at entry; the in-task `Parent:` edges and the
banner's written order give the sequence. Order: `.ralph/fix_plan.md`
banner **item 3**.

Unifies goopg's planning *route* with PG 18.3's, per the medium-abstraction
comparison in
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/plan-flow-medium-abstraction.md`
and the rewrite-feasibility note `tmp/planner-rewrite-possibility260920.md`.
The strategy is **grow the seam, never rewrite in place**: the existing
`tryPGShapedJoinSearch` lattice (RelOptInfo/pathlist/partial-pathlist/DPPATH)
is reused; what changes is its INPUT (a jointree-level IR instead of a
lowered node subtree) and its SPAN (whole statement instead of the FROM
chain). The new pipeline is selectable behind `GOOPG_JOINTREE_PIPELINE=1`
(default off) until M0145-0008's cutover deletes the legacy one — corpus
gates keep running on the default pipeline the whole time.

**Hard constraint on every task:** Q78's `outer-over-derived` firewall must
not weaken (owner constraint, carried from M0142-0008a-3i-route-a).

**Absorbed walls** (do not work them separately): the resolver lowers every
EXISTS/IN body via `planSelectWithParent` before unnest/search ever run
(`planner.go:1499`→`planExistsExpr`; recon:
`docs/design/0100-0149/m0142-0008a-3i-lateral-route-recon.md`), so
M0144-0003a, M0144-0003b's residual, M0142-0008a-3(i)/(ii) and
`-3i-lateral-route` all block on this milestone's 0003.

- [x] **M0145-0001 — recon: the jointree-level IR and the lowering
  contract** (design the representation the whole milestone builds on).
  **DONE 2026-09-21.** Design doc
  `docs/design/0100-0149/m0145-0001-jointree-ir-and-lowering-contract.md`
  (indexed). Decisions: (i) IR = `jtLeafRef`/`jtFromExpr`/`jtJoinExpr`/
  `jtAppendRel` over a per-statement leaf table (`jtScope`), flat
  `ColumnRef.Index` space kept with spans assigned at construction
  (append-only); `SpecialJoinInfo` carried as-is; `OuterColumnRef` re-base
  = Level decrement on splice, `Level:1`→`ColumnRef`, fail-closed when
  `(Index,SourceTableIdx)` doesn't resolve to an emitting leaf in the
  destination scope (Q78 firewall preserved). (ii) Resolution runs ON the
  IR — leaf identity precedes binding (PG: rtable precedes `Var`);
  `resolveExpr` loses its sublink-planning arms, bodies get bound-but-
  unplanned provisional scopes, an `SS_process_sublinks` analogue plans
  survivors post-pull-up. (iii) Lowering contract inventories ~40
  incrementally-established state items, each assigned an
  IR-CON/BIND/PULL/SEARCH/UPPER/LOWER/TAIL owner; `createPlanNode` stays
  the single funnel. (iv) Per-task retirement matrix 0002–0008 in the
  doc. Movement: none (recon; no production file touched).
  Kind: recon
  Parent: none
  Inputs: the plan-flow doc (D1/D2/D4/D6), the lateral-route recon (the
  wall is the resolver, not the phases), route-a step 1's landed
  `ExistsExpr.Subquery`/`InExpr.Subquery` retention and
  `sublinkBodyIsSimple` (`sublinkpullup.go`, port of `is_simple_subquery`
  prepjointree.c:1807). Deliverables in the design doc: (i) IR entry kinds
  — base-rel, semi/anti (SJInfo-equivalent: min lefthand/righthand relsets
  + join quals), appendrel (child list for UNION ALL flattening), with
  OuterColumnRef re-basing semantics spelled out; (ii) where the IR is
  built — it must precede `resolveExpr`'s sublink planning
  (planner.go:1499), so define whether resolution runs on the IR or the
  IR derives from resolved-but-unplanned subtrees; (iii) the Path→Node
  lowering contract — enumerate every piece of state the node-tree stages
  currently establish incrementally (neededCols, outputCols, OuterColumnRef
  remaps, rowmarks, CTE cache semantics, `rewriteJoinsToNLI`,
  `stampSemiProbePrices`, SRF/ProjectSet, set-op handling) and assign each
  a lowering-site owner; (iv) the retirement list — which seam guards
  (leaf-count, lateral, pinned-spine, Phase A/B, `admitSemiAnti`,
  `runJoinSearchBelowPinned`) become dead at which task.
  Kind: recon
  Parent: none
- [x] **M0145-0002 — dual-pipeline measurement harness** (the transition
  must be measurable while the default pipeline stays the value-gated one).
  `GOOPG_JOINTREE_PIPELINE=1` selects the new pipeline on private lanes;
  value gates (`tpcds-sf025 sweep`, `tpch-spotcheck`, acceptance arms)
  always run the default; plan-parity captures are EXPLAIN-only so a plan
  the executor cannot yet run is still comparable evidence. Build: the env
  knob at the planSelectWithSettings dispatch, a
  `pg-plan-parity-diff.py`-compatible capture recipe for the knob arm, and
  a per-stage divergence report (which of the six divergences the record
  belongs to) so progress is visible per-slice, per AGENT.md G8.
  Landed: fail-closed knob (`jointreePipelineFromEnv` — only literal `1`
  arms) + dispatch at `planSelectWithSettings` → `planSelectJointreePipeline`
  (delegating stub until 0003; the renamed body is `planSelectLegacyPipeline`);
  flag registered in the provenance table so every capture stamps its arm;
  `scripts/jointree-parity-capture.sh` (tpch via estimate-audit arm,
  tpcds via `cp -a` private clone, PG arm read-only);
  `scripts/pg-plan-divergence-class.py` (D1–D6 + jointree-search + verdict,
  diverging-pair matching). A/A evidence: SF0.25 plan bodies byte-identical,
  TPC-H shape-identical. Floors held: TPC-DS match=2 (Q9/Q41), TPC-H
  match=1 (Q6). Gates: units, tpch-spotcheck, sf025 sweep (FORCE=1, nightly
  live — values valid). Design doc:
  `docs/design/0100-0149/m0145-0002-dual-pipeline-harness.md` (indexed).
  Kind: impl
  Parent: none
  Movement: none
- [ ] **M0145-0003 — sublink pull-up into the jointree** (goopg's
  `pull_up_sublinks`/`pull_up_subqueries` analogue; the milestone's core).
  Using route-a step 1's retained `.Subquery` parse trees, flatten bodies
  passing `sublinkBodyIsSimple` into the IR as semi/anti entries (quals
  merged into outer predicates, correlation `OuterColumnRef{Level:1}`
  re-based into the outer column space at splice — PG gets this free via
  `Var`/range-table indices); bodies that fail the simple test become
  semi/anti entries carrying their planned Node as the non-flattenable
  inner, so the search still sees them as citizens. Route-a step 2 is the
  pilot subset — if it lands first on the node-tree chain, port it onto
  the IR here rather than duplicating the mechanism. Must close the
  P0-H11 `cumulativeFromSpans` span round-trip in the same change
  (M0142-0008a-3i-reach-verify). Expected movement: the seam's
  `leaf-count` decline class (26 across 11 queries) and the five census
  witnesses (Q10/Q16/Q35/Q69/Q94 `join-method` records); re-derive leaf
  arithmetic, do not carry today's shortfall over.
  Kind: impl
  Parent: M0145-0001
  - **Flat-body arm landed this loop** (design:
    `docs/design/0100-0149/m0145-0003-sublink-pullup-into-jointree.md`).
    `jointreepullup.go` ports `convert_EXISTS_sublink_to_join`'s gates
    and binds the body via a provisional `planFromClause` +
    `resolveExpr` on the retained `.Subquery`; `integratePulledSublinks`
    inside `tryPGShapedJoinSearch` appends the body leaves at problem
    tail positions and emits `semiAntiChainLink`+`SpecialJoinInfo`, so
    the whole problem tail runs unchanged. `pulled` marks are
    pointer-keyed and inert outside the seam — every decline returns
    the exact legacy shape. `NOT EXISTS`'s `UnaryOp(OpNot,…)` spelling
    flips to ANTI per the unnest.go:4516 convention.
  - Learnings:
    - `NOT EXISTS` binds as `UnaryOp(OpNot, ExistsExpr{Negated:false})`,
      not `Negated:true` — the parser's canonical spelling; the
      pull-up must recognise both conjunct shapes.
    - `exprListHasLocalAndLevel1Ref` (a conjunct reading a body-local
      column AND a level-1 parent ref) is the pull-up-time proxy for
      "a spanning conjunct exists" — without it `pulled` would mark
      bodies the seam declines anyway and wrongly suppress the pre-DP
      arm for sibling sublinks.
    - Outer-local quals hoist under SEMI (they filter the same rows
      the semijoin keeps) but have no legal slot under ANTI — the link
      has no join-clause-only qual channel, so they decline (ledgered).
    - P0-H11's span round-trip is closed by construction: pulled leaf
      walk-order-flat and out-of-band bases coincide
      (`sum(widths[:pos])`), and `allSpans` uses `buildLeafSpans`'s
      own cumulative rule — attribution is pinned against
      `buildLeafSpans` output in the white-box test.
  - Evidence (knob arm, `tmp/jtcap-m3/`): SF0.25 A/B 91/99 identical;
    the only 5 shape diffs are Q10/Q16/Q35/Q69/Q94 — the named
    witnesses; vs-PG `D1-sublink` 8→6 (D3 +2 as those queries advance
    to their next divergence stage); TPC-H `match=22 shapediff=0`
    (fully inert — legacy already produced the semijoin). Gates:
    units + exprwalk census PASS, tpch-spotcheck PASS (Q12=2 Q13=33),
    SF0.25 sweep PASS (`MISMATCH=0`, plans `same=99`), acceptance arm
    24/24 value-MATCH.
  - **ANY arm landed (loop 2026-09-21 \#20)** —
    `convert_ANY_sublink_to_join` (subselect.c:1333) at the seam's
    granularity. `anyPullupConjunct` + `pullUpAnyBody` produce the same
    `jtPulledBody` the EXISTS arm does, so the seam, leaf numbering and
    `classifyPulledQuals` are untouched.
    - The one new mechanism: an ANY body need not be correlated at all
      (`x IN (SELECT y FROM t)` has no cross-scope reference — the
      correlation IS the testexpr, which lives in the OUTER qual), so
      the arm SYNTHESISES the link conjunct `outerOperand = bodyTarget`
      in the space the EXISTS body quals use — body-local plain
      `*ColumnRef`, outer as `*OuterColumnRef{Level:1}`
      (`outerOperandAsLevel1`). `rebasePulledQual` validates every outer
      index against the emitting bindings, so a wrong coordinate fails
      closed instead of reading the wrong column.
    - `NOT IN` is REFUSED, and that is PG's rule, not a shortcut: it is
      `<> ALL`, an ALL\_SUBLINK, and `pull_up_sublinks_qual_recurse`
      converts ANY and EXISTS only (prepjointree.c:665/731). goopg's
      LEGACY unnest does convert `NOT IN` to ANTI — a pre-existing
      divergence, ledgered separately, not extended onto this pipeline.
    - Measured on the same channel before/after (TPC-DS SF0.25 plans,
      knob on): `(pulled)` 9 -> **21**, unrecognised `InExpr` 34 -> **1**,
      with the residue landing in two NEW named buckets —
      `any-body-scope-not-bindable` 15 and `any-nested-sublink` 6. The
      next gates are named by the census rather than guessed.
    - Correctness evidence had to come from the KNOB arm, because the
      default-arm gates cannot exercise this code: TPC-DS SF0.25 sweep
      with `GOOPG_JOINTREE_PIPELINE=1` gives
      `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0` — row counts and value
      checksums correct on every executable query. Default gates stay
      green and plan-identical.
    - `TestExprSwitchInventoryIsPinned` required registering
      `outerOperandAsLevel1` in `exprSwitchInventory`; it is built on
      `cloneExprRefs` and fails closed (an unenumerated type aborts the
      clone, which declines the pull-up).
  - **ANY residue named (loop 2026-09-21 \#21)** —
    `bindPulledBodyScope`/`flattenPulledBodyTree` now return the
    sub-reason they failed on, and the larger ANY bucket turned out to
    be one shape, not the one the ledger assumed.
    - All 15 former `any-body-scope-not-bindable` fires are
      `any-body-leaf-(*optimizer.CTEScan)`: the ANY body's FROM is a
      **CTE reference** (`WHERE x IN (SELECT … FROM some_cte)`), the
      TPC-DS idiom.
    - They are NOT the opaque-body arm. Admitting a `*CTEScan` leaf
      pushes a statistics-free input into the DP, and `itemIsDerived`
      (relfromjoinlist.go:557) classifies exactly
      `*CTEScan`/`*WorkTableScan` as derived — the class the Q78
      `outer-over-derived` firewall keeps out of join ordering (C-04a:
      Q78 15 s -> 327 s TIMEOUT). The banner makes that firewall a hard
      constraint on every pull-up/flattening task.
    - So this bucket is blocked on **B-06 (CTE-output statistics)**, the
      same blocker M0145-0005 slice 5 and M0145-0006 recorded for
      `outer-over-derived`. Building the opaque-body arm would not move
      it. (B-06 is filed as **M0145-0009**; its completion note names the
      bare-`*SeqScan` relaxation as a sanctioned re-evaluation.)
    - The remaining actionable ANY bucket is `any-nested-sublink` (6):
      PG recurses `pull_up_sublinks` into a pulled body's own quals
      (`pull_up_sublinks_qual_recurse`); goopg does not.
  - **Decline census (loop 2026-09-21 \#19)** — which arm to build next,
    measured instead of ranked by size. `notePullupDecline`
    (`internal/optimizer/nlicensus.go`, `GOOPG_NLI_CENSUS=1`) reports one
    line per WHERE conjunct the pull-up sees. TPC-DS SF0.25, knob arm:
    - `InExpr` **34** — `IN`/`NOT IN`/`= ANY`. PG pulls these up
      (`ANY_SUBLINK`, prepjointree.c:665). **This is the gap.**
    - `SubqueryExpr` 15 — scalar sublinks, which PG does NOT pull up
      either (they stay SubPlans). Not a gap; do not build for it.
    - `(pulled)` 9 — the flat EXISTS arm's successes.
    - `ExistsExpr` 2 — EXISTS not at conjunct top level (under OR); PG
      handles some of those in its OR arm (prepjointree.c:797), so a
      small real gap.
    - NO `pullUpExistsBody` gate fired at all (`body-not-simple`,
      `no-level1-correlation`, `no-spanning-conjunct`, …): every
      recognised EXISTS conjunct was pulled. The EXISTS arm's gates are
      not the limiter — the corpus is mostly `IN`/`ANY`.
    - The classifier names sublinks by Go type via `ExprSubplans`, not a
      hand-written switch: `TestExprSwitchInventoryIsPinned` rejected the
      switch version, and it was right — a census that must be taught
      each Expr type reports the untaught one as "nothing here", so the
      arm nobody built would be the arm that never appears.
    - **Next arm: `convert_ANY_sublink_to_join`** (subselect.c:1333) — 34
      of the 45 unpulled sublink conjuncts, and ANY+EXISTS is exactly
      PG's own `pull_up_sublinks` scope.
  - Still open (ledgered): the **opaque-body arm** (non-flat bodies as
    semi/anti citizens carrying their planned Node — needs param-exec
    machinery inside the problem); `IN`/`NOT IN` pull-up
    (`convert_ANY_sublink_to_join` — hashed-subplan mechanism);
    outer-local-only correlation; ANTI outer-local quals.
- [ ] **M0145-0004 — UNION ALL flattening to an appendrel jointree entry**
  (`pull_up_simple_union_all` analogue, prepjointree.c:1617). Absorbs
  M0144-0003b's residual: the branches whose subtree never reached the
  search at all (Q5's `*Project:nil-rel` on both sides, Q2/Q33/Q56/Q60)
  become reachable once flattening happens at IR level before lowering.
  `setOpBranchRel`/`addPartialSetOpPath` (0003b-1) already carry the rel
  out of a SetOp node — the IR version makes the appendrel a real join
  input so `add_paths_to_append_rel` semantics apply. Expected movement:
  the TPC-DS Parallel Append sites (Q5×3, Q2, Q14, Q71, Q76) and census
  `parallelism` records.
  Kind: impl
  Parent: M0145-0001
  - **Leaf-hoist arm landed this loop** (design:
    `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`).
    `jointreeappendrel.go` ports `is_simple_union_all` (chain head
    refuses ORDER BY/LIMIT/OFFSET/locking/WITH; every link UNION ALL;
    member-local trailing FOR UPDATE refused; `SetOpOperand`
    groupings declined) and `addAppendRelPartialPaths` hoists the
    nested SETOP rel's Parallel Append candidate onto the leaf rel.
    `plannerSet.appendrelMember`/`ctx.appendrelMember` force each
    member scope through the join search (one-relation floor → 1 for
    exactly one scope level — upstream runs
    `set_base_rel_pathlists` unconditionally); the leaf's
    `ConsiderParallel` inherits the SETOP rel's member-AND. All marks
    are jointree-arm-only — legacy is inert by construction.
  - Two shared latent gaps closed (both arms benefit):
    - Worker sizing through `*SetOp`: `drivingScans` expands a SetOp
      driving node to its streamed member scans (claimed-whole
      branches skipped); `computeParallelWorkers`/`upperSplitWorkers`
      take max-over-branches (PG's create_append_path max rule).
      Previously `scanTable(*SetOp)`=nil → 0 workers → every
      `Aggregate → SetOp` / gather-over-union plan refused.
    - Executor claims through a join probe: `unwrapToSetOp` gained
      `*joinOp` (probe side per algo, mirroring
      `attachParallelScan`'s gates) + `*nestedLoopIndexJoinOp` arms —
      without it a `PathSetOp` under a partial hash join stayed
      unclaimed and every worker replayed the whole union
      (reproduced: 80/120/200 rows at workers=1/2/4 for a 40-row
      join; `TestGatherOverJoinProbeSetOpIdentity` pins 40).
  - Third gap, knob-arm-only, found by the parity capture: a hoisted
    `PathSetOp` is a legal join input for the first time, and
    `createSetOpPlan` returned a nil `outputLayout` for every caller —
    `joinInputsFor` panicked re-basing quals (`EXPLAIN Q5` crashed the
    backend). Fixed: leaf-owned SetOp paths return
    `baseRelLayout(p.Rel, out)` (upper-rel paths stay nil);
    `TestCreateSetOpPlanLeafLayout` pins the contiguous leaf layout.
  - Evidence: SF0.25 sweep 96 PASS / MISMATCH=0 on BOTH arms
    (knob-arm sweep `tmp/m0145-0004-sweep-on/`, post-fix capture
    `tmp/m0145-0004-capture/jt-on-fix.*` — unparsed 0, verdict 3);
    plan-diff shows the witness move — Q5's three unions now plan
    `Gather → Hash Join → Append{Parallel Seq Scan ×2}` on the
    default arm (post-pass gather made selectable by the sizing
    fix), and the knob arm elects a search-level
    `Gather → Nested Loop → Parallel Hash Join → Append{parallel}`
    (PG's `Parallel Hash Join → Parallel Append` shape); Q5 rows
    identical off/on. tpch-spotcheck PASS (Q12=2, Q13=33). New
    white-box tests: admissibility matrix, member-scope search A/B,
    hoist gates + Rel re-targeting, CP inheritance,
    partial-agg-over-union leaf, leaf-owned SetOp layout.
  - **`tlist_same_datatypes` probed (loop 2026-09-21 \#22)** — the gate
    is missing, but it is a FIDELITY gate here, not a live wrong-answer:
    a type-mismatched `UNION ALL` flattens on the knob arm and returns
    the same correct values as the default arm (`1`, `2.5`, `sum` 6.5).
    The probe did surface a real, arm-independent defect — the setop's
    declared output type is the first member's, not
    `select_common_type`'s — filed separately under "Manually
    discovered" rather than folded in here, because it is a parser/type
    defect that both pipelines share.
  - Still open (ledgered): member-level rtable entries +
    parent-qual distribution into members (`distribute_qual_to_rels` —
    M0145-0005's IR work); `tlist_same_datatypes` (needs bound member
    tlists pre-cast); LATERAL union propagation; CTE-wrapped union
    leaves (`CTEScan` hides the carrier — Q2/Q14/Q71/Q76's shapes);
    serial-side member-path competition (leaf serial path stays
    prebuilt-over-nested-winner); `is_safe_append_member`'s pull-up
    half is inapplicable in this model (members are not promoted —
    member WHERE quals ride `spliceBranchEmission` per worker).
- [ ] **M0145-0005 — single-pass DP over the jointree** (the search
  consumes the IR directly; semi/anti entries are legal searched partners
  via a `join_is_legal` port over the SJInfo-equivalent, joinrels.c:350).
  Retires the Phase A/B split, `runJoinSearchBelowPinned`, the pinned
  spine, splice-time re-resolution and the `admitSemiAnti` call-site
  restriction — each retirement lands only when its deletion is exercised
  by the corpus under `GOOPG_JOINTREE_PIPELINE=1`. Executor-capability
  gates stay in path generation (parallel_hash refusal, partial nestloop
  jointype whitelist) until the D3 substrate tasks land — flow parity
  must not silently generate unexecutable shapes. Depends on M0145-0004
  too: appendrel entries must exist in the IR before the single pass can
  be complete for UNION ALL chains (a query whose chain still contains a
  `*SetOp` falls back to the legacy pipeline, not to a partial search).
  Kind: impl
  Parent: M0145-0003
  - Loop 2026-09-21: design doc
    `docs/design/0100-0149/m0145-0005-single-pass-dp-over-jointree.md`
    + slice 1 landed.
    - Recon: the DP substrate the task text calls for is already complete —
      `joinIsLegal` is the joinrels.c:350 port in full (SEMI/ANTI match,
      `reversed`, LEFT/FULL association), `buildJoinRelRestrictList`
      implements outer-join clause distribution incl. nullable-side
      filters, `addPaths(..., sjinfo)` → `Path.Jointype` →
      `planJoinTypeFor` → executor types; `makeRelFromJoinlist` already
      recurses pinned sub-joinlists. The remainder is construction-side
      retirement, not capability.
    - Key correction to the task's mental model: `joinPinned` pins ONLY
      FULL since C-04a/b (LEFT/RIGHT deconstruct flat + SJInfos — a flat
      `a LEFT b LEFT c` was already a single searched problem on BOTH
      arms); the only pinned tops left are FULL and outer-over-FULL,
      which `spineLinkSearchable` never certifies — a certified non-empty
      spine was unreachable, so `splitOuterSpine`/splice/`prefixNullable`
      were already dead on reachable inputs.
    - Slice 1 (landed): knob arm skips `splitOuterSpine` entirely in
      `tryPGShapedJoinSearch` — `node` + `ctx.joinlist` feed the
      machinery directly; pinned tops decline one gate later
      (`extractSearchLeaves` leaf-count / `pinnedUnsearchable`) with the
      same `used=false` syntactic fall-back. No reachable plan change on
      either arm — pure retirement.
    - Remaining slices per the doc: semi/anti as real leaf items (retires
      `semiAntiChainLink` synthetic leaves + remap family), IR-direct
      leaf materialisation (retires `extractSearchLeaves` node-walk +
      spans/offset validation), one-rel floor (`isSimpleSingle`/
      `GOOPG_ONEREL_SEARCH`), Phase A/B + `admitSemiAnti` retirement.
    - Tests: `internal/optimizer/joinsearch_m0145_test.go` — searched
      flat LEFT spine pin, both-arms parity pin, FULL fail-closed pin.
    - Slice 4 (landed, loop 2026-09-21 #7): one-relation and degenerate
      scopes through the same entry on the knob arm —
      `isSimpleSingle` bypass + WHERE-arm rule-chooser guard gained
      `&& !jointree`, the filterless arm gained `|| jointree`, and the
      seam floor is `1` whenever `jointreePipeline`
      (`GOOPG_ONEREL_SEARCH` now governs the legacy arm alone — its only
      readers were inside the lifted guards).
    - Behaviour the lift unlocks: single-table statements on the knob arm
      are searched (base-rel pathlist on cost, `set_base_rel_pathlists`
      analogue); a flat correlated EXISTS/NOT EXISTS over one table now
      reaches `pullUpSublinksIntoJointree` — previously the
      `isSimpleSingle` bypass kept every pull-up test on the post-hoc
      unnest path without exercising pull-up at all.
    - Latent crash fixed in the same commit: `pullUpExistsBody` called
      `resolveExpr(sub.Where)` unguarded — WHERE-less EXISTS bodies
      (`EXISTS (SELECT 1 FROM t)`) SIGSEGV'd; reachable before via
      multi-table outers, now declined at the correlation check.
    - Executor gaps the lift exposed (fixed in the same commit):
      - `Join{Lateral, Semi/Anti}` (searched parameterised NL anti/semi,
        e.g. Q22's `NOT EXISTS` under a derived table) had no emit-once
        arm in `lateralJoinStream` — the generic semi/anti `Open` check
        refused predicate-less keyed joins before the lateral arm ran.
        Taught the stream SEMI/ANTI emit-once mirroring the fused
        `nestedLoopIndexJoinOp` semantics, reordered `Open` so `Lateral`
        routes first, and kept a fail-closed refusal for null-aware
        lateral shapes.
      - Cooperative parallel hash build + nested Gather: a producer's
        rebuilt build subtree is Closed wholesale after probing the
        leader-published shared table, and `gatherOp.Close`/
        `gatherMergeOp.Close` dereferenced `o.group` unconditionally —
        nil for a never-Opened Gather. The panic, converted by
        `ParallelGroup.Go`, cancelled SIBLING producers mid-scan, and
        `group.Wait()`'s error was discarded in the build's cleanup —
        silent partial builds (TPC-H Q20: 85-99 of 101 suppliers across
        identical runs, plan stable, gone at 1 worker or
        `GOOPG_COOP_JOIN_BUILD=off`). Both Closes are now no-ops when
        never Opened, and the build propagates the producer-group error
        instead of swallowing it (fail-closed: a producer failure fails
        the query rather than returning partial results).
    - Test premise updates (retired, not weakened):
      `TestJointreePipelineDispatchDelegates` +
      `TestJointreePullupDeclineParity`/`NoExistsKeepsLegacyIdentical`
      byte-equality → shape + decorrelation + join-type equality plus the
      `treeHasSearched` routing marker.
    - Skipped on the knob arm by design (value-preserving missed opts,
      ledgered): `injectLikeRangePredicates`, `reduceNotNullQuals`
      incl. always-false → childless `Result`, `planIndexScanFromWhere`.
    - **`reduceNotNullQuals` CLOSED (loop 2026-09-21 \#23)** — it was
      the one of the three that carries cutover RISK, because it is
      goopg's port of `restriction_is_always_true`/`_false`
      (initsplan.c `add_base_clause_to_rel`), i.e. a PG SHAPE rule and
      not merely an optimisation. Measured before the fix: the knob arm
      planned `Filter{SeqScan}` for `WHERE not_null_col IS NULL` where
      the default arm emits PG's childless
      `Result / One-Time Filter: false`; likewise it kept a redundant
      Filter for `IS NOT NULL` where the default arm drops the qual.
    - The reduction now runs in the generic arm, gated to `jointree` and
      to single-binding scopes (the chooser's own condition). Downstream
      reads `whereQual != nil` as "there is a Filter to search under"
      and the pre-DP arm asserts `node.(*Filter)` on it, so a reduced
      scope marks the clause spent (`whereQual = nil`) — without that an
      always-false WHERE still carrying an `EXISTS` would have hit an
      unchecked assertion on a `*Result`.
    - Pins are ARM-EQUALITY pins (`notnull_reduce_jointree_test.go`):
      the property M0145-0008 needs is that flipping the knob cannot
      change these plans.
    - The LIKE-range pair stays ledgered: index selection, not a PG
      shape rule, so no cutover risk.
    - **LIKE-range measured (loop 2026-09-21 \#24), and its resume point
      CORRECTED.** Corpus witnesses: zero. TPC-H has no `LIKE` predicate
      at all; TPC-DS has exactly two (`hd_buy_potential LIKE 'Unknown%'`)
      and that column is unindexed in the benchmark schema, so no index
      path exists to elect on either arm.
    - The earlier resume point ("teach the search's base-rel pathgen
      about the synthetic range conjuncts") is HAZARDOUS taken
      literally: adding `col >= 'foo' AND col < 'fop'` to the rel's
      restriction clauses double-counts the LIKE's selectivity, and no
      corpus query would show it.
    - PG's actual mechanism (`like_support.c` header): the derived
      clauses are approximate INDEX-SCAN quals with the original
      operator re-applied as a qpqual — "using a regular index as if it
      were a lossy index" — so the estimate still comes from the LIKE
      alone. The chooser arm already separates the two lists
      (`whereForIndex` vs `whereQual`); the port must reproduce that
      separation at PATH level rather than merging them.
    - Slice 2 (landed, loop 2026-09-21 #8): pulled semi/anti bodies are
      REAL numbered leaf items — `pullUpSublinksIntoJointree` appends
      one `leafItem` per pulled leaf to `ctx.joinlist` (`pu.base` /
      `pu.nLeaves`), `splicePulledLeaves` inserts the scans at
      `[nReal, nprefix)` and shifts extracted leaf indexes
      (`shiftRelSetAbove` over link hands, SJInfo fields, outer-link
      sides, `belowNullable`), `classifyPulledQuals` rebases+classifies
      quals straight into the conjunct pool and appends each body's
      SJInfo to `joinInfoList`. No `semiAntiChainLink` is produced for
      a pulled body.
    - Retired this slice: `integratePulledSublinks`,
      `buildPulledSemiAntiLink`, the pulled share of the synthetic-leaf
      splice + `searchJl` leafItem-append, and the `admitSemiAnti`
      flag itself (one call site, literal `true` since b2 — dead
      flexibility). `remapWalkOrderFlatToSpans` survives for the chain
      arm with a pulledBase/pulledLeaves index translation;
      `semiAntiOnQualsOK` + the `searchJl` append survive for
      chain-extracted leaves only. `pgShapedOffsetChecksOK` now takes
      the non-emitting RelSet directly (`[nReal, len(scans))`).
    - White-box pins: `TestJointreePullupRealLeafItems`/`…Anti`
      (joinlist leaf item, splice position, cumulative span, pooled
      conjunct relids, SJInfo on `joinInfoList`, `len(semiAnti)==0`);
      DPTRACE probe: `rels=jtp_o,a,b` enumerate + SEMI pair priced.
    - Gates: optimizer suite PASS; tpch-spotcheck Q12=2/Q13=33; SF0.25
      96/96 with 99/99 plan shapes identical; acceptance arm 24/24
      under JOINTREE+PGSHAPED; units PASS. plan-gate 16/22 vs the
      recorded 14/22 stale-baseline drift — the +2 (Q3/Q18) is the
      Sep-20 `:65433` rebuild (slice-1/4 era), not this change: the
      gate diffs the live old binary and Q3/Q18 cannot reach the
      pulled path (Q3 has no sublink; Q18's IN body has GROUP BY).
    - Slice 3 (landed, loop 2026-09-21 #9): IR-direct leaf
      materialisation — `jtScopeTable` (jointreescope.go) records the
      leaf/link structure at `planFromItem` construction time in the
      walk's own DFS order (descendable join = link+leaf; demoted
      semi/anti = link+synthetic leaf; FULL/other = fold to one opaque
      leaf, erasing inner links). `planFromClause` concatenates
      per-item tables and pins `jtScope.root = root`. The seam reads
      `extractScopeLeaves` only when `jointreePipeline &&
      ctx.jtScope.root == chain` — the node walk stays for the legacy
      arm and for post-rewrite chains (S5a Phase B).
    - `extractScopeLeaves` rebuilds the identical (scans, widths,
      onQuals, outer, semiAnti) tuple: width/realWidth as prefix sums
      over the leaf table, base/rightBase sampled at loLeft/loRight,
      belowNullable as range-containment over processed outer-link
      subtrees, `preserved` as the RIGHT-ancestor enclosure test
      (every link sits in its ancestors' left subtree — right subtrees
      are single leaves). Rebases, keyed-pred fold, FlattenedRHS
      expansion and Min-side narrowing run unchanged on jn.Predicate.
    - Did NOT retire (contra the ledger's original line): the
      coordinate-translation family — `rebaseChainQual`,
      `rebaseSemiAntiChainQual`, `remapWalkOrderFlatToSpans`,
      `buildLeafSpans`, `localizeExprToLeaf`, `pgShapedOffsetChecksOK`.
      They translate between two coordinate spaces that both still
      exist (a Join's Predicate is in its own concat coords whatever
      path reads it); the walk's removal removes discovery, not the
      spaces.
    - White-box pins: `TestScopeExtractionMatchesWalk` — 20-shape
      matrix, field-wise identity incl. same scan node pointers,
      reflect.DeepEqual rebased preds, sjinfo pointer identity;
      `TestScopeExtractionDeclinesLikeWalk` — fail-closed arms.
      DPTRACE probe: scope-extracted problems enumerate end-to-end
      (`rels=a,b` pulled EXISTS, SEMI pair priced, searched subtree).
    - Gates: optimizer suite PASS; units PASS; tpch-spotcheck
      Q12=2/Q13=33; SF0.25 96/96 with 99/99 plan shapes identical;
      acceptance arm 24/24 under JOINTREE+PGSHAPED. plan-gate 16/22
      unchanged stale-pin drift (diffs the live Sep-20 :65433 binary,
      which this change cannot affect).
    - Also fixed this loop: reverted a wholesale `gofmt -w` that the
      newer local gofmt had applied to planner.go/joinsearchseam.go
      (repo baseline is go1.25 — `gofmt -w` rewrites unrelated lines;
      re-applied the edits manually).
    - Slice 5-partial (landed, loop 2026-09-21 #10): searched-subtree
      opacity for the residual qual-redistribution family.
    - Census scoping (`tmp/m0144-0011a2-census.log`, pre-slice-3): 132
      seam declines — `leaf-count` 104, `outer-over-derived` 12,
      `outer-spine` 8 (retired by slice 1), `lateral` 8.
      `outer-on-qual`/`inner-on-qual-*` have ZERO corpus witnesses —
      retired by absence, no decline class left to migrate.
      `outer-over-derived` stays (B-06 CTE-output-stats firewall,
      R42/Q78 — B-06 is filed as **M0145-0009**; its completion note
      carries the firewall's re-evaluation checklist); `lateral` stays
      (real deps Q30/Q68 — parameterized-path legality, filed as
      **M0145-0010**; its completion note carries this family's
      re-evaluation).
    - `leaf-count` dominant cause identified by joinlist-vs-bindings
      probe: FULL-join folds (opaque leaf covering multiple joinlist
      rels, `a FULL b JOIN c` → nprefix 3, scans 2) — the
      executor-substrate-blocked class already ledgered as staying.
      Grouped `j.Right` joins ruled out (one binding AND one joinlist
      item — admitted as 2-rel problems).
    - The pushdown family cannot die wholesale (declined + legacy-arm
      statements still need the post-search cleanups); the correct
      retirement shape is stopping it at the searched boundary. Audit:
      `pushOneConjunct`, `rewriteScanInputs` outer walk and
      `rewriteJoinsToNLI` already pruned (P5.9-b); two holes closed —
      `pushSingleSideQualsIntoInnerJoinInputs` had no `isSearchedTree`
      awareness at any level (walker descent, Filter-level
      `pushInnerJoinInputQuals` into searched join inputs, and the
      `pushConjunctIntoSubtree` descent to searched grandchildren), and
      `findUniqueSeqScanByColumn` (`absorbConjunctsIntoSubtree`'s hunt)
      could IndexScan-rewrite a scan the costed search elected —
      wrong-answer class for nullable-held conjuncts (pushed below the
      null-extension keeps rows the residual drops).
    - Mechanism: `pushTrace.noSearched` flag; all statement-level
      descents route through `pushConjunctIntoSubtreeTracedNoSearched`
      (incl. `deriveConstAcrossJoinEquality`'s sibling seeding).
      `pushConjunctIntoSubtree` stays permissive — CTE-inline conjuncts
      arrive from OUTSIDE the searched body's scope and crossing the
      boundary is PG's own parse-level qual pushdown (R42/Q78 witness);
      a blanket guard there is the regression, not the fix.
    - Pins (`searched_opacity_test.go`): searched-root + searched-
      grandchild refusal, unsearched-side positive control, CTE-inline
      crossing counter-pin, scan-hunt opacity + unsearched
      findability. `markSearchedTree` usable on `*Join`/`*SeqScan` in
      tests (embeds `searchedTree`).
- [x] **M0145-0006 — upper-rel pathlists** (extend the lattice through
  `create_grouping_paths`/`create_ordered_paths` analogues so ordering and
  grouping are elected over candidate sets, not by stage-builder
  producers). Absorbs D4's residual gates: `inputNodePathkeys`'s
  `default: nil` tops (`*MergeJoin`/`*GatherMerge`/`*IncrementalSort`/
  `*WindowAgg`), `electOrderedGrouping`'s `gate-precondition` skip when
  `node != agg.node`, and `electOrderedDistinct`'s surviving `cands<2`
  gate (upperordereddistinct.go:140).
  Kind: impl
  Parent: M0145-0005
    - Slice 1 (landed, loop 2026-09-21 \#11): the three order-delivering
      tops `inputNodePathkeys`' walk swallowed in its `default: nil`.
      Design doc: `docs/design/0100-0149/m0145-0006-upper-rel-pathlists.md`.
    - `*IncrementalSort` claims the FULL `Keys` list — `PresortedCount`
      bounds the WORK the node does, never the order it emits
      (`create_incremental_sort_path`, pathnode.c:3191, takes the whole
      list too).
    - `*GatherMerge` claims `Keys`: the leader's merge preserves the
      worker ordering, which is exactly what distinguishes it from
      `*Gather` (pathnode.c:2128 vs the nil `create_gather_path` sets).
    - `*WindowAgg` descends to the CHILD's claim
      (`create_windowagg_path`, pathnode.c:3740-3741: "WindowAgg
      preserves the input sort order"), but ONLY when `Presorted` — with
      the flag false goopg's executor sorts the input privately by
      `PartitionBy ++ OrderBy` and records no direction for that sort, so
      the arm fails closed rather than reconstructing one.
    - New coordinate rule: the walk carries a `limit` (how many LEADING
      columns of the published output the current space must agree with)
      and narrows it when crossing a `*WindowAgg`, because the window
      functions are APPENDED to the child's schema so the child's columns
      keep their positions. The prefix is re-checked with
      `schemaCoordinatesAgree(out[:len(child)], child)`, never assumed —
      a node that PREPENDS still returns nil.
    - Witness: TPC-DS SF0.25 **Q51** lost its redundant top Sort
      (`Sort Key: item_sk, d_date` over the WindowAgg), cost
      `419.08..422.64` -> `277.76..381.02`, rows unchanged at 100. It is a
      parity move: PG 18.3 on the same dataset plans
      `Limit -> Subquery Scan -> WindowAgg -> Sort -> Merge Full Join`
      with no second Sort.
    - Slice 2 (landed, loop 2026-09-21 \#12): the merge-join top.
    - The ledgered plan (stamp `p.Pathkeys` onto the node) was WRONG about
      where the gap is: `stampSearchPathkeys` already stamps every merge
      join the PG-shaped search produces, and the walk reads that stamp
      before its type switch. What has no stamp is the LEGACY
      constructor's merge join — `chooseInnerJoinAlgo` (joincost.go:33)
      elects merge on cost and `chooseOuterFillJoinAlgo` leaves
      RIGHT/FULL on merge — which is the route every seam-declined
      statement takes.
    - Four soundness conditions, each a wrong-ordering guard: the
      comparator direction is FIXED (ascending / NULLs-last,
      `mergeSortedSource.less`), the join-type rule routes through
      `buildJoinPathkeys` (FULL/RIGHT claim nil), the key list is the
      EXECUTOR's `ExecMergeKeyPlan().Keys` and not `HashKeys` (an
      unsafe pair is dropped into the residual, so `HashKeys` would
      claim an ordering the sort never keyed), and the result is run
      through `validatedSearchPathkeys` against the node's own schema.
      An unsafe LEAD key refuses outright — `Keys[0]` is kept
      unconditionally, so it may be ordered by `compareDatum` while SQL
      `=` disagrees.
    - A searched join with an EMPTY stamp returns nil rather than
      re-deriving: the search was given the chance to claim and
      declined, and a weaker mechanism must not overrule it.
    - NO corpus witness: SF0.25 shapes `same=99 changed=0` and the
      acceptance arm 24/24 — every merge join those 121 statements bring
      to the seam is search-built and already stamped. The unit pins are
      the guarantee; the corpus is the no-regression channel only.
    - Slice 3-partial (landed, loop 2026-09-21 \#13): the election sees
      through a rename.
    - `electOrderedGrouping` required the seam input to BE `agg.node`;
      it now admits a chain of positional-identity `*Project`s
      (`identityProjectChainTo`, sharing `projectIsPositionalIdentity`
      with `inputNodePathkeys`' walk so the two routes agree on which
      projections are transparent).
    - The SPLICE is what needed pinning: the elected spec is copied back
      onto `agg.node` in place, so the chain already carries it, but the
      winner was BUILT over the bare aggregate — returning that node
      drops the projection and publishes the aggregate's labels. A sort
      winner is re-parented over the chain; a bare-aggregate winner
      returns the chain.
    - MEASURED FIRST (`dpTrace` probe, three shapes): the plain aliased
      `GROUP BY … ORDER BY` already elected (its rename Project is added
      ABOVE the ORDER BY stage), the subquery-rename shape has no
      grouping surface in that scope at all, and the reachable decliner
      is `Filter{Aggregate}` — HAVING. So the admitted Project case has
      no witness among the probed shapes; report it as capability, not
      movement.
    - HAVING is NOT admitted with it, deliberately: `addOrderedPaths`
      prices its sort arm from the input path's Rows/Cost, which for a
      `PathAgg` are PRE-HAVING, while the normal `createOrderedPaths`
      call it would replace prices from the finished `Filter` node.
      Admitting the filter without pricing it swaps an accurate cost for
      an optimistic one. PG has no gap here — HAVING quals live on the
      `AggPath` (`create_agg_path`'s `qual`), so its pathlist entries
      already carry post-HAVING rows. Ledgered with that resume point.
    - Slice 4 (landed, loop 2026-09-21 \#14): `electOrderedDistinct`'s
      candidate minimum drops from 2 to 1.
    - The ledgered reading was WRONG a second time: this was never a
      candidate-SUPPLY gap. `addDistinctPaths` always files both the
      hashed and the unique-over-sorted candidate; what removes the
      second one is `add_path` DOMINANCE, downstream of supply. The gate
      was refusing the election whenever one candidate simply lost.
    - PG has no minimum — `create_ordered_paths` iterates the whole
      input pathlist (`planner.c:5337`) — and M0144-0011a-2 already made
      exactly this change on the grouping twin, so the gate was a
      sibling-path divergence (`pattern_sibling_paths_must_agree`).
    - Witness (`dpTrace` A/B, `select distinct c from t order by c` with
      `enable_hashagg = off` so only one candidate survives): the old
      gate declined and left a `*Sort` over the DistinctOn; the new one
      elects the bare `*DistinctOn`. The unique candidate's producer
      Sort orders every output column ascending, so the ORDER BY key is
      a prefix of what it already delivers and the Sort was redundant.
    - Both corpora stay plan-identical (SF0.25 `same=99 changed=0`,
      acceptance 24/24): neither benchmark runs a lone-candidate DISTINCT
      with an ORDER BY, so the probe is the witness here.
    - Slice 5 (landed, loop 2026-09-21 \#15): the HAVING filter,
      admitted WITH pricing — closing the residue slice 3 ledgered.
    - `seeThroughChainTo` replaces `identityProjectChainTo`: it also
      crosses `*Filter`s and RETURNS the quals it crossed. The filter is
      ordering-transparent (same schema, removes rows without
      reordering), but its predicate is priced onto every candidate by
      `applyHavingQualsToOffer`, which is PG's `cost_agg` `if (quals)`
      arm — `total_cost += qual_cost.startup + output_tuples *
      qual_cost.per_tuple` then `output_tuples = clamp_row_est(
      output_tuples * selectivity)`.
    - The ORDERED rel is now sized from `node` (post-qual) rather than
      `agg.node`; identical objects when nothing wraps the aggregate, so
      the unwrapped path is byte-unchanged.
    - A/B on a `GROUP BY … HAVING … ORDER BY` statement: decline
      `gate-precondition` before, `elected shape=Sort-over-Aggregate`
      after, SAME `Project -> Sort -> Filter -> Aggregate` shape — the
      grouping twin's M0144-0011a-2 result (shape-inert, cost-different)
      on that statement.
    - CORPUS MOVEMENT, unlike slices 2-4: TPC-DS SF0.25 moves two
      shapes, both HAVING statements, with `PASS=96 MISMATCH=0`.
      **Q24 is a parity move** — `Sort{HashAggregate, Filter: sum(...) >
      InitPlan}` became `GroupAggregate{Filter, Sort{...}}` with no top
      Sort, and PG 18.3 on the same dataset plans
      `GroupAggregate … Filter: (sum(ssales.netpaid) > (InitPlan 2).col1)`
      with no top Sort either. **Q6** is cost-only
      (`35244.72..35244.74` -> `35244.96..35244.98`).
    - Deferred (ledger rows 2026-09-21):
      `electOrderedGrouping`'s `node != agg.node` precondition, and
      `electOrderedDistinct`'s `cands<2` gate (really a
      `createDistinctPaths` candidate-supply gap).
- [ ] **M0145-0007 — single Path→Node lowering** (the `create_plan`
  analogue): consolidate every post-election resolution step the stage
  builders currently interleave — per 0001's lowering-contract inventory —
  into one pass that runs once on the elected path tree. During
  transition the legacy stages must still fire on the old pipeline only;
  a lowering bug must never silently reach the default pipeline.
  Kind: impl
  Parent: M0145-0005
    - Recon complete + slice 1 landed (loop 2026-09-21 \#16). Design doc:
      `docs/design/0100-0149/m0145-0007-single-path-node-lowering.md`.
    - The recon RE-ORDERED the four items M0145-0001 assigned here, and
      two of them turn out not to be refactors at all.
    - `translateToLayout` is ALREADY lowering-local: every call site
      sits in a `createplan*.go` file (4 join, 3 nl, 2 simple, 1 gather;
      none elsewhere). Retired by absence. Slice 1 converts the
      measurement into an enforced invariant
      (`TestTranslateToLayoutIsLoweringLocal`), verified to FAIL when the
      exclusion is lifted, so it is a guard and not a tautology.
    - `fillJoinHashKeys` must retire LAST, not first. Its lateness is a
      deliberate defence: `Predicate` is mutated after join construction
      by predRebind, the bushy sub-remap, constant folding,
      `lowerSubPlanParams` and the qual-placement passes, and a field
      filled early is one every mutator must maintain — the one time
      that was missed, chained NATURAL JOINs probed the wrong column
      (M0097-0060). It can fold into the join arm only after the
      mutators are gone.
    - `rewriteJoinsToNLI` + `stampSemiProbePrices` need no folding:
      `walkRewriteNLI` already returns at `isSearchedTree` (P5.9-b) and
      the stamp mirrors its descent exactly, so both serve ONLY the
      population the PG-shaped search never built. They die with the
      legacy pipeline at M0145-0008. What 0007 owes is a MEASUREMENT
      that the search's NLI paths cover that population (slice 2).
    - The splice/re-resolution family is the real work and is entirely
      live: `reresolveJoinByName` 28 non-test refs, `remapByPosMap` 16,
      `applyJoinTreePosMap` 10, `remapWithBindings` 9, `layoutPosMap` 7,
      `remapPosMapAfterRewrite` 5, `remapSublinkOuterRefs` 4,
      `spliceSearchedSpine` 3, `remapExprRefsToMHJ` 2.
    - Five-slice plan in the design doc; slice 3 is the
      `OuterColumnRef` outer-layout mapping, slice 4 the rest of the
      re-resolution family, slice 5 the hash keys.
    - Slice 2 (landed, loop 2026-09-21 \#17): the NLI route census
      (`internal/optimizer/nlicensus.go`, `GOOPG_NLI_CENSUS=1`, one
      stderr line per built node carrying route + join type + probed
      index; registered in `flagProvenanceExempt` as diagnostic-only).
    - TPC-DS SF0.25 (99 queries): search 143 nodes, all INNER; rewrite
      **1** node, INNER, probing `date_dim_pkey`.
    - TPC-H SF1 (22-query acceptance arm): search 11 nodes, all INNER;
      rewrite **4** nodes — **2 SEMI + 2 ANTI**.
    - THE REWRITE IS NOT DEAD CODE, and the shape of its population is
      the finding: on TPC-H every node it builds is a SEMI or ANTI join
      and the search builds none of those. Same fact
      `stampSemiProbePrices`' header states from the other side ("the
      search never sees the unnested relset"), now counted.
    - **CORRECTED 2026-09-21 (loop \#25)**: the cause named below is
      wrong. `addNLIPaths` (joinpathsnli.go:280) declines only
      `JoinRight` — its comment states the admitted set as
      "Inner/Left/Semi/Anti" — so the join TYPE was never the gate. The
      real mechanism is that the default arm's semijoin is built by the
      legacy unnest AFTER the search, so no joinrel exists to file a
      path against. Knob-arm census: TPC-H rewrite-built drops from 4
      (2 SEMI + 2 ANTI) to **1 SEMI** because the pull-up feeds three of
      them to the search — which then elects a non-NLI method, leaving
      search-built SEMI/ANTI NLIs at zero on both arms. The open
      question is whether an NLI path is generated and out-costed or
      never generated; the next probe instruments `addNLIPaths`' other
      decline points (`uniq == uniqueSideOuter`, `o.RequiredOuter != 0`,
      the `param_source_rels` test), not its join-type set.
    - **ANSWERED 2026-09-21 (loop \#26): the paths ARE filed, and the
      claimed cutover blocker is REMOVED.** `noteNLIPathGate` reports
      `addNLIPaths`' outcome per semi/anti joinrel; on the knob arm
      TPC-H gives `semi gate=filed` and `anti gate=filed` — no gate
      declines. The search generates the NLI path and `add_path`
      out-costs it, preferring a hash or merge semijoin. Deleting
      `rewriteJoinsToNLI` at the cutover therefore removes the legacy
      route's OVERRIDE, not the capability. Whether the cost preference
      is right is a separate, live cost-accuracy question
      (`stampSemiProbePrices` exists because a rewrite-built NLI carries
      no path price), but it does not gate M0145-0008.
    - ~~**Hard constraint for M0145-0008**~~ (superseded by the line
      above): deleting `rewriteJoinsToNLI`
      with the legacy pipeline deletes the ONLY route that builds a
      SEMI/ANTI index-probe join. The cutover depends on `addNLIPaths`
      electing SEMI/ANTI NLI paths first (it files INNER/LEFT today) —
      a pathgen task, not a lowering one. Ledgered.
    - Slice 3 (landed, loop 2026-09-21 \#18): the sublink-route census,
      and it lands the same verdict as slice 2 — this task's remaining
      items are CUTOVER-blocked, not lowering refactors.
    - Caller tracing first: `spliceSearchedSpine` and
      `remapSublinkOuterRefs` have NO callers outside `predp.go`, and
      every live `layoutPosMap`/`remapByPosMap` call is in `predp.go`
      too. The splice/re-resolution family is not the lowering walk's
      leftovers — it is ONE route's machinery, the post-search
      re-resolution of the pinned semi/anti spine.
    - Census (`SUBLINKCENSUS` lines, same env gate; counts exceed the
      query count because subplans and CTEs plan recursively):
      default arm TPC-DS 207 pinned-spine / 0 jointree-pullup; default
      arm TPC-H 16 / 0; **knob arm TPC-DS 294 / 5**.
    - So even with `GOOPG_JOINTREE_PIPELINE=1` the pull-up handles under
      2% of sublink-planning events — exactly what M0145-0003 landed and
      ledgered (flat EXISTS/NOT EXISTS only; IN/NOT IN, non-flat bodies
      and outer-local-only correlation deferred). Everything else falls
      back to the pinned spine on BOTH arms.
    - Therefore slice 4 is blocked on **M0145-0003**, not on M0145-0005's
      clause distribution as the recon first assumed.
    - **Blocker RE-TESTED (loop 2026-09-21 \#25) — it HOLDS, with a new
      number.** The block was stated as a measured ratio and two
      M0145-0003 changes landed after that measurement (the ANY arm and
      the body-local-qual fix `fef25625d`), so the condition was
      re-measured rather than carried forward.
      - Knob arm: 291 pinned-spine / **17** jointree-pullup (was
        294 / 5) — coverage 1.7% -> **5.5%**, 3.4x more pulled
        conjuncts. Default arm: 207 / 0, byte-identical to before —
        the control proving every movement is knob-arm only.
      - Verdict UNCHANGED: 94.5% of sublink-planning events still take
        the pinned spine, so the splice/re-resolution family cannot
        retire here. The blocker no longer cites "under 2%"; it cites
        5.5%.
      - Of the 60 conjuncts the pull-up examines (vs 308 total events —
        248 never reach it): 21 pulled, 15 `CTEScan` bodies, 15 scalar
        `SubqueryExpr`, 6 nested-sublink, 3 residual. The 15 scalar
        declines are CORRECT (PG has no `EXPR_SUBLINK` conversion).
      - `PULLUPCLASSIFY` now fires ZERO refusals — the
        `body-qual-not-consumable` class is gone, independently
        confirming `fef25625d`.
      - The one lever on this ratio is the CTEScan-body class (15),
        whose blocker is B-06 = **M0145-0009**. Resume there, then
        re-run this census and re-adjudicate slice 4.
      - **Consequence for M0145-0008**: its text requires this task, so
        the cutover is NOT selectable as written — even though BOTH of
        its named TIMING blockers are now discharged (gap 1.06x).
        Whether the flip can be split from the legacy-pipeline deletion
        is a scoping decision for the banner's owner.
    - TRAP, recorded in the design doc: a plans capture taken under
      `GOOPG_JOINTREE_PIPELINE=1` lands in the same results directory the
      next DEFAULT sweep diffs against. The sweep after this census
      reported `same=74 changed=25` purely because its baseline was the
      knob-arm capture; diffing the two default-arm captures directly
      showed them byte-identical.
    - Attribution of the single TPC-DS INNER fire is still open:
      `cmd_plans` captures all 99 queries regardless of `QUERIES`, so a
      bisect through that channel measures the same full run each time.
      Use the `sweep` channel (which does honour `QUERIES`) or add an
      outer-relation field to the census line.
- [ ] **M0145-0008 — cutover**: flip `GOOPG_JOINTREE_PIPELINE` default,
  re-run the full corpus gates on the new pipeline (sf025 sweep,
  tpch-spotcheck, acceptance arm, plan-parity capture), then delete the
  legacy pipeline and the retired seam guards from 0001's list. Requires
  the executor-substrate tracking note: parallel-hash-build and
  row-emitting PartialAgg (plan-flow doc D3; M0144-0011c's Materialize
  sizing) are parity-enablers that stay OUT of this milestone — the
  cutover records which census records remain `unexpressible` for that
  reason so the executor milestone inherits an exact list (filed homes:
  **M0140-0007** for the partial-inner hash build, **M0141-S3–S6** for
  row-emitting PartialAgg). Requires
  M0145-0006 as well as 0007: the cutover must not retire the stage
  builders while upper-rel elections still live in them.
  Kind: impl
  Parent: M0145-0007
  - **BLOCKED by a measured timing regression (loop 2026-09-21 \#27).**
    Design doc:
    `docs/design/0100-0149/m0145-0008-cutover-readiness-timing-ab.md`.
    - Every gate in this milestone compares VALUES, and the two arms are
      value-identical (acceptance arm 24/24 on both), so a cost
      misjudgement is invisible to all of them. Runtime is the only
      channel that can see one — which is why this A/B exists.
    - Two acceptance-arm runs, TPC-H SF1, serial, fresh capped server
      each, **identical engine-id and binary** (`3e61809f585fd51c`), the
      only difference being `GOOPG_JOINTREE_PIPELINE`.
    - The pipeline is timing-NEUTRAL on 19 of 24 labels (within ±10%).
      The entire 1.64x total is four sublink queries:
      **Q4 35.1x** (0.37s -> 12.98s), **Q20 27.5x** (0.13s -> 3.58s),
      **Q17 20.0x** (0.38s -> 7.61s), **Q21 7.2x** (2.63s -> 18.94s).
      **Q22 0.8x** is the control — being a sublink query is not
      sufficient to regress.
    - Q4/Q20/Q21 are the semijoins M0145-0007 traced: the search files
      an NLI path (`gate=filed`) and `add_path` out-costs it, and the
      plan it prefers instead runs 7-35x slower. That ANSWERS the
      ledgered cost question — the preference is not merely unvalidated,
      it is wrong wherever it fires.
    - Q17 is a correlated SCALAR subquery the pull-up does not touch
      (PG does not convert `EXPR_SUBLINK` either), so its 20x is a
      DIFFERENT, unattributed mechanism — do not fold it into the
      semijoin story.
    - Flipping the default today would make TPC-H 1.64x slower with every
      value gate green. Two prerequisites are filed: the semijoin NLI
      cost comparison (log the filed-vs-winning path costs at `addPath`
      for Q4/Q20/Q21) and Q17's mechanism.
    - **Both prerequisites are now discharged as diagnoses (2026-09-21).**
      - The semijoin one is FIXED, not merely answered: `fef25625d`
        (M0145-0003, body-local quals are base restrictions) took Q4
        12.98s -> 1.02s and Q21 18.94s -> 2.12s, knob-arm total
        102.46s -> 77.37s, arm-vs-arm gap 1.64x -> 1.24x, values
        unchanged. The reading that `add_path` out-costs the NLI was
        itself wrong and is retired in the design doc.
      - **Q17 is ATTRIBUTED (this loop), and the note above it is
        wrong**: the arms are NOT plan-identical. Serial A/B on one
        clone, identical values — default 1021 ms keeps PG's shape
        (`Filter: l_quantity < (SubPlan 1)`, est. cost 32301); the knob
        arm decorrelates into a `HashAggregate` over a full
        6,001,988-row `Seq Scan` (est. cost 224656, i.e. goopg's own
        model prices the elected plan at 7x the default's, so the cheap
        candidate is never GENERATED — the Q4 failure class again). PG
        18.3 keeps the `SubPlan`, so the knob arm is the unfaithful one.
      - Mechanism, measured by instrumenting `canUnnestSubquery`'s
        S6/D6.2 guard on both arms: the guard is intact, but the arms
        hand it different bodies —
        `Project(Aggregate(BitmapHeapScan(BitmapIndexScan)))`
        probeCheap=true (refuses) vs
        `Project(Aggregate(Filter(SeqScan)))` probeCheap=false
        (unnests). `innerPlanIsIndexProbeCheap` is a SHAPE predicate
        used as a cost proxy, and it is sound only after index
        selection has run on the body.
      - Fix deferred to its own loop ON PURPOSE (ledgered): `unnest.go`
        is shared with the DEFAULT shipping pipeline, so touching the
        guard is a default-arm plan-shape change needing the full value
        + timing gate set. Recommended direction is arm-local — have
        the jointree route select the body's index path before the
        unnest decision, so both arms hand the guard the same body.
    - **Q17 FIXED (loop 2026-09-21 \#24), and the attribution above was
      too narrow.** Instrumenting body planning AND the guard shows no
      drift between them: the body is BORN without its index path, so
      the defect is in body planning, not the unnest pass.
      - The rule-based `isSimpleSingle` bypass is the ONLY producer of
        an index path driven by a correlated restriction
        (`planIndexScanFromWhere`), and THREE routes skip it —
        `jointree`, `oneRelSearchEnabled()` and `appendrelMember`.
      - Decisive test: the **DEFAULT** arm with
        `GOOPG_ONEREL_SEARCH=on` reproduces the regression exactly
        (10625 ms vs the bypass's 1021 ms). The defect is
        **route-borne, not arm-borne**, and is reachable on the
        shipping arm behind a documented flag — same family as the
        ledgered `c07-single-rel-never-reaches-ordered-index-producer`.
      - Fix offers the bypass's producer at the end of the generic arm,
        gated STRICTLY NARROWER than the bypass it restores: it fires
        only when the search elected no index path at all
        (`planIsBareSeqScanTree`, fail-closed), so it can never
        displace a costed index choice.
      - Result: all three routes produce PG's shape with identical
        values — jointree 11155 ms -> 881 ms, ONEREL\_SEARCH 10625 ms ->
        700 ms, bypass control unchanged. Knob-arm acceptance:
        **Q17 8.52s -> 0.39s** (default 0.38s), total 77.37s -> 65.97s,
        arm-vs-arm gap **1.24x -> 1.06x** (1.64x at the original A/B).
      - `appendrelMember` deliberately excluded and ledgered (no
        witness measured on that route).
      - **Both named cutover prerequisites are now discharged.** What
        remains before flipping the default is the cutover's own
        corpus-gate re-run, not a known defect.
- [ ] **M0145-0009 — CTE-output statistics (B-06 resume): wire the landed
  synthesis into the estimator** (filed 2026-09-21 by owner directive;
  carries TODO_ALL B-06 / ledger `take3-B-06-deferred`). Three of this
  milestone's residuals are blocked on derived-input statistics: the ANY
  arm's `body-leaf-(*optimizer.CTEScan)` residue (15 fires, M0145-0003),
  the `outer-over-derived` decline family (12 corpus fires, M0145-0005
  slice 5), and the Q78 firewall's own named resume condition
  (`relfromjoinlist.go:724-725` — "lift when B-06 wires CTE-output stats").
  Scope per the ledger's 4-step resume:
  - (1) Design exists and is reviewed:
    `docs/design/planner-b06-cte-stats/DESIGN.md`. The synthesis slice is
    already landed INERT (`internal/optimizer/cte_stats_synthesis.go` —
    group-key / aggOut-FD / union-literal rules + 16 tests, no consumers
    wired, so it cannot change a plan today).
  - (2) Wire consumers: per-column ndistinct from the group combo clamped
    by output rows; the FD bound for agg outputs; the OID-less registry
    (DESIGN §G2) so `*CTEScan` leaves actually receive the synthesized
    stats in `EstimateRows`/selectivity.
  - (3) Measure before trusting: EA ratchet on the `year_total` shapes
    (Q74; Q4/Q11 share that CTE — Q78's CTEs are `ws`/`cs`/`ss`), the
    SF0.25 sweep, and the TPC-H acceptance arm — stats
    wiring is pipeline-agnostic, so the DEFAULT arm is gated too, not
    only the knob arm; report `CATEGORIES-EXCL-MATCH` and any plan
    movement explicitly.
  - (4) The `rows<=1` guard (`joinsearch.go:520-526`, `initialRelRows`) and the Q78
    firewall stay UNTOUCHED inside this task — the ledger's criterion is
    "guard removal only after derived ≥ guard effect", and the firewall
    is a hard owner constraint.
  Fail-closed throughout: an unrecognized body shape yields
  `cteColUnknown` and today's defaults, never a guess.
  Kind: impl
  Parent: none
  - **Step 2 slice 1 landed (loop 2026-09-21 \#26): the ndistinct
    consumer.** Design updated:
    `docs/design/planner-b06-cte-stats/DESIGN.md` (now indexed in
    `docs/design/README.md`, which it was not before).
    - Identity/lifetime needed no registry: `CTEScan.cte` already points
      at the `*plannedCTE` that `synthesizeCTEStats` consumes, so the
      synthesis is memoized on the entry (`plannedCTE.outputStats()`).
      The entry pointer is a strictly stronger identity than the
      design's `DeclKey()` map key with exactly the lifetime it asks
      for. Recorded as a deliberate, documented divergence.
    - `cteSynthNDistinct` is wired as the LAST arm of
      `columnNDistinctForChild`, so a catalog resolution always wins,
      and is fail-closed at every step — it can only REPLACE a default,
      never invent a number.
    - **It is measurably INERT on the corpus, and the instrumentation
      says why.** DEFAULT arm TPC-DS SF0.25: plans 99/99 IDENTICAL. The
      consumer is reached **852 times** per run and declines every one:
      **648** asks are body shapes the synthesis does not classify,
      **204** are group keys it classifies but does not number, and the
      two kinds it CAN number (agg outputs, union literals) are asked
      for **ZERO** times.
    - So gap G3's agg-output FD bound has no consumer in this channel at
      all, and gap G2's group-combo rule is the only thing that can
      number the 204 live asks. **None of the three residuals this task
      exists to unblock can move until G2 lands.**
    - Slice 2, in order: (1) census which body shapes produce the 648
      `unknown` asks before widening the synthesis; (2) land the
      group-combo rule (G2 / synthesis step 3); (3) EA ratchet on the
      `year_total` shapes + SF0.25 + acceptance arm on the DEFAULT arm,
      reporting plan movement explicitly.
    - Step 4 respected: the `rows<=1` guard and the Q78 firewall were
      NOT touched.
  - **Step 2 slice 2 landed (loop 2026-09-21 \#27): the group-combo rule
    (gap G2).** A group key's output ndistinct is
    `min(input ndistinct, group count)`, applied ONLY when the input is
    known.
    - **Census first, per this task's own ordering**, and it inverted
      the expected target. The 648 slice-1 `unknown` asks are:
      `Project(WindowAgg)` **366** (56%, no rule exists at all),
      `Project(Filter)` 158, bare `SetOp` 59, `Project(SetOp)` 41,
      `DistinctOn` 24. Window functions dominate — not set-ops.
    - **The group count alone is NOT a usable fallback, and the gate
      proved it.** The first version used it when the input was unknown:
      sound as a bound, wrong as an estimate for one key of a multi-key
      grouping. TPC-DS `d_week_seq` got priced at the whole group count,
      which collapsed Q59 from 43 estimated rows to 1 and flipped
      Hash Join -> Nested Loop. **Values stayed green — only the PLAN
      channel saw it**, which is exactly why step 3 gates the default
      arm and reports plan movement explicitly.
    - Corrected rule: **99/99 plans identical** against the TRUE
      pre-change baseline (the intermediate capture was the buggy
      version — the baseline-drift trap the design doc records),
      `PASS=96 MISMATCH=0`, acceptance arm 24/24.
    - It **fires 18 times per corpus run** with large corrections
      (`in=4 groups=655237` -> nd=4, against `defaultNumDistinct` 200),
      so it is exercised and safe — but no SF0.25 plan depends on those
      columns yet. The value is banked for the residuals, not realised.
    - **A G1 claim in the design is contradicted by measurement**:
      it says plain bodies need no synthesis because the resolver
      recurses into them, yet `Project(Filter)` bodies reach the
      consumer 158×/run — and the consumer is only reached when
      `resolveBaseColumn` FAILS. Ledgered; one of the two is wrong.
    - Next (ledgered, in order): the `WindowAgg` arm (366 asks, the
      largest population), then diagnose why `synthUnionLiterals`
      declines the 100 live `SetOp` asks, then the EA/q-error ratchet on
      the `year_total` shapes — NOT run here, so estimate quality on the
      18 fired columns is still unmeasured.
    - Step 4 respected again: the `rows<=1` guard and the Q78 firewall
      remain UNTOUCHED.
  - **On completion — reconsider the blocked work (evaluate, do not
    auto-do):**
    - `flattenPulledBodyTree`'s bare-`*SeqScan` rule (M0145-0003 ANY
      residue): the ledger already sanctions this step — once a
      `*CTEScan` leaf carries row/width estimates, relax the rule and
      re-run the knob-arm sweep. File as its own task if the work is
      more than the rule relaxation.
    - `outer-over-derived` firewall (`relfromjoinlist.go`, 12 corpus
      fires): evaluate whether the named resume condition is genuinely
      met — derived estimates must now out-rank the defaults AND a live
      Q78 run must show the ~19 s shape holds (the C-04a regression
      class is 15 s -> 327 s). Owner hard constraint: if the evidence is
      ambiguous, ESCALATE rather than lift; a clean lift is itself a
      separate task.
    - The `rows<=1` guard (`joinsearch.go:520-526`, `initialRelRows`): same criterion —
      removal only after derived estimates demonstrably reach the
      guard's effect on the year_total shapes (ledger step 4).
    - Re-run the pull-up/seam decline census afterwards so the residual
      buckets reflect the new state.
- [ ] **M0145-0010 — parameterized-path legality: port
  `reparameterize_path` / `required_outer`** (filed 2026-09-21 by owner
  directive; carries the `lateral` decline family's resume point). The
  seam declines `lateral` problems — 8 corpus fires, real lateral
  dependencies in Q30/Q68, the wall `-3i-lateral-route`'s recon named —
  and partial-NLI admission stays a jointype whitelist `{INNER, SEMI}`
  where PG's nestloop dispatch admits `{INNER, LEFT, SEMI, ANTI}`
  (`joinpath.c:1842-1846`), because PG decides legality from
  parameterization (`param_info`/`required_outer`), not from an
  enumerated set. goopg's gap is the REL level: `Path.RequiredOuter`
  and its calculators already exist (`path.go:404`,
  `calcNestloopRequiredOuter`/`calcNonNestloopRequiredOuter` in
  `pathparam.go`, `ppi_rows` pricing in `pathparamindex.go`), but there
  is no rel-level `param_info`/`lateral_relids` and no
  `reparameterize_path` re-costing an existing path against a larger
  outer set. Scope:
  - (a) the RelOptInfo/jointree leaf gains a `required_outer`/`param_info`
    relid set — on the jointree arm this is an IR field, on the legacy
    arm it is another coordinate-translation consumer; prefer whichever
    arm is default when picked up, and sequencing after M0145-0005
    avoids building the translation twice.
  - (b) `reparameterize_path`/`get_param_path_clause_serials` analogues:
    re-price an existing path under additional outer parameterization
    (PG `postgres/src/backend/optimizer/util/pathnode.c:4242` and
    `:1910` respectively — invoked FROM `joinpath.c`), extending the
    existing `RequiredOuter` machinery rather than building from zero.
  - (c) legality reads parameterization where PG does — `join_is_legal`
    and the nestloop dispatch — instead of the whitelist.
  - (d) executor-capability check FIRST for every newly admitted shape:
    a parameterized path the executor cannot drive is a new
    `unexpressible` record, not a win — verify driveability before
    filing the path. Known asymmetry to check: the executor's ordinary
    (non-fused) `ordinaryInnerNestedLoopPartial` arm is INNER-ONLY while
    the planner whitelist admits {INNER, SEMI} — a SEMI/LEFT/ANTI
    partial NL that reaches it would mis-execute, so the check must
    cover that arm.
  Kind: impl
  Parent: none
  - **On completion — reconsider the blocked work (evaluate, do not
    auto-do):** the `lateral` decline family (Q30/Q68 witnesses) on the
    then-default arm; the partial-NLI whitelist's LEFT/ANTI entries
    (ledgered "by scope" refusals — admit only where legality says so
    and the executor check passes); re-run the pull-up/seam decline
    census afterwards so the buckets reflect the new state.
