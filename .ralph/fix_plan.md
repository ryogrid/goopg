# goopg Fix Plan

## Current Priority

Written by the owner, 2026-09-17 (after `tmp/METHODLOGY3_RALPH_CHECK0917/`).
**This banner is the sole ordering authority** and **the loop never edits it**
(`AGENT.md` §"Plan-parity harness" R2). `.ralph/working_set.md` carries state,
not priority. Previous banners: `docs/design/0100-0149/plan-parity-harness-background.md`.

**Before selecting anything below, read `AGENT.md` §"Plan-parity harness".**


*Slimmed 2026-10-15 (owner): OWNER DECISIONS history,
discharged items' narratives and the UNFROZEN detail moved
to `completed_milestones/completed_fix_plan_013.md`. Item
numbering is unchanged — S7 rank inheritance and S9's
item-10 boundary still apply.*

### Selection rules

Consolidated here 2026-09-24 (owner consolidation; these moved from
`AGENT.md` §S / §"Loop discipline" and keep their S-numbers for citation
stability — S3–S5, filing/completion discipline, remain in `AGENT.md` §S;
**do not add a `## ` heading inside this banner**: the guards slice the
banner at the next `## ` line).

- **S1** The banner is the only ordering authority. Take the first item
  that has a selectable (`[ ]`, dependencies met) task, in the order
  written inside the item. `[!]` tasks are not selectable.
- **S2** A newly found defect that loses data, returns wrong results or
  corrupts a shared resource: file it as a task, add an escalation block
  naming it, and continue with the banner. The owner places it in the
  banner (normally at position 0). Do not reorder the banner yourself.
- **S6** Tasks under the banner's `FROZEN-PREFIXES:` or marked
  `[!] FROZEN` are not selected, re-statused, deleted or given children.
- **S7** An open task the banner does not name is selectable only when
  every banner item is exhausted or blocked — **except** a task filed
  under a milestone or task family the banner DOES name (a `Parent:` /
  filed child of a named item's family): it inherits that item's rank
  **and its gates and constraints** — a child of a `[!]`/held item, a
  task gated on the M0145-0008 flip (M0146 below), or a family under an
  item's scope-limited constraint (e.g. item 6's diagnosis-only) stays
  unselectable/constrained exactly as its named ancestor is. An item is
  "live" while it still has at least one selectable task; a held item is
  not live. When several items name the family, the task takes the rank
  of the item whose stated scope it belongs to; when ambiguous, the
  LOWEST-ranked of the candidate items (conservative).
  *(Amended 2026-09-24, owner decision — see OWNER DECISIONS 2026-09-24:
  the unamended rule ranked every unlisted task below M-NIGHTLY, which
  stranded parity-family work filed under named milestones.)*
  *(Amended 2026-10-13, owner decision — see OWNER DECISIONS 2026-10-13:*
  **correctness-only carve-out.** *The loop's focus is TPC-H/TPC-DS plan
  parity. A descendant whose work is executor/catalog/session
  correctness only — wrong results, durability, privilege,
  client-tool or regress-suite conformance — with no plan-instrument
  rationale (no first-divergence record, no TPC-H/TPC-DS query named,
  expected `Movement:` on the plan instruments — match count,
  text-identical, CATEGORIES-EXCL-MATCH, ea-ratchet — not `regress`
  or `none`) does NOT inherit rank; it queues under the unnamed-task
  fallback below item 10 unless the item's member list explicitly
  names it. `regress` stays a Movement instrument — it records
  honest movement and keeps lineages unblocked; it never confers
  rank. Filing such findings remains required (record, don't drop);
  placing them is owner-only. Applied immediately to M0146-0153,
  0154 and M0146-0160 through 0166.)*
- **Ties and exhaustion:** among tasks the banner leaves equally ranked —
  and, when every banner item is exhausted/blocked, among the unnamed
  tasks S7 then admits — take the topmost unchecked task in file order.
- **S8 Nothing selectable** (all remaining work blocked on the owner):
  write one escalation block into `.ralph/working_set.md` listing each
  blocker, and end the loop without code changes. Do not invent work.
- **S9 Parity replenishment** (owner directive 2026-10-13; this pass is
  owner-delegated — S8's "do not invent work" does not apply to it):
  when no item 0-9 holds a selectable ACTIONABLE task — the standing
  census root M0146-0014 is a filing channel, not stock, and does not
  count; burn-down parents (today: M0146-0042, M0146-0007) count only
  while they hold an ungated actionable slice or a fileable census
  record of their own; nor do S7-carve-out/below-item-10 descendants
  — run ONE replenishment pass BEFORE item 10:
  (1) Census freshness — reuse the newest
  `analysis/m0146/m0146-0014/routing-*/` artifact iff its ROUTING.md's
  recorded capture sha is an ancestor of HEAD AND no commit since
  touches `internal/optimizer`, `internal/planner`, `internal/executor`
  or `*cost*|*stat*|*selfuncs*` paths AND no corpus reload is recorded
  after the artifact (e.g. the 2026-10-13 SF1 stale-load reload — a
  corpus change does not move HEAD); otherwise capture fresh
  (`scripts/jointree-parity-capture.sh` per corpus) and re-run
  `scripts/pg-plan-parity-diff.py --verbose`. If the captures cannot
  run (e.g. the source server is live and the capture refuses), item
  10 is live FOR THIS LOOP.
  (2) A record is ACTIONABLE when it is non-MATCH and its census route
  is not a live (`[ ]` or `[!]`) task, not a waived/owner residual
  (COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES, STALE-SF1), and not
  the leaf-count/lateral classes owned by M0145-0003/0010. For TPC-H,
  each non-MATCH query is actionable unless a live task names it.
  (3) Rank the differ's nine categories by actionable-record count
  summed across all three corpora; file exactly ONE task —
  `Parent: M0146-0014`, `Kind: impl` — citing corpus + query id +
  category + current verdict + expected instrument. Scope: first step
  records where goopg's plan selection diverges from PG 18.3 (the
  first-divergence point); then implement in order and drive the query
  to MATCH. Prefer the SF0.25 record when a query diverges at both
  scales (SF1 measurement is stale-load-compromised). The filed task
  inherits item-3 rank under S7 (named family + plan-instrument
  rationale) — it is selected before item 10.
  (4) Bounds — ONE filing per pass, never a batch. A pass that files
  nothing leaves a dated `exhausted at <sha>` note in M0146-0014's
  entry — the note MUST list every open `[ ]` task ranked items 0-9
  and the gate making it non-actionable, as `id(gate)` tokens using
  the closed vocabulary `channel`, `below-item-10`, `owner-<what>`,
  `blocked-on <id>` (e.g. `M0146-0178(owner-reload SF0.25)`), so an
  owner gate such as a pending corpus reload is auditable in the
  record — and re-runs only after HEAD advances past that sha in the
  planner-relevant paths above — otherwise item 10 is live for that
  loop. A replenishment task that closes `Movement: none` must name
  why its premise/record was stale; after TWO consecutive such closes
  without a landed fix, stop filing and write the S8 escalation.
FILEABLE-ROOTS: M0146-0014
(owner directive 2026-10-13 — filings under the standing census root
are exempt from the Rule-A lineage budget: the sweep's purpose IS
filing findings, and a `none`-streak of routing sweeps must not freeze
the parity supply. This IS a true exemption — it supersedes the
"not an exemption" note on the 2026-10-13 LINEAGE-BASELINE pin for
this root, which stays as the historical record. M0146-0014 stays
`[ ]`; flipping it to `[!]` is a guard violation — the owner de-lists
it to hold the lineage.)
- **M-NIGHTLY** filing is unconditional (the paragraph at this section's
  tail); **selecting** one ahead of the order above is allowed only when
  it breaks the build or a gate this banner's work needs.

0. **P0 — incident recovery: DISCHARGED.** P0-E4..E7 all `[x]`
   (2026-09-18; archive `completed_fix_plan_013.md`).
   `.ralph/gate-exceptions.md` stays empty — a blocked TPC-H gate is a
   failed gate (AGENT.md G1).
1. **Regressions found by P0-E7: DISCHARGED** (drained; archive 013).
2. **M0144 Phase-A instruments: DISCHARGED** (milestone complete;
   archive `completed_fix_plan_014.md`; children 0003a/0003b closed by
   owner disposition 2026-09-23).
2a. **Post-cutover correctness and gate-red batch: DISCHARGED** (owner
   decision 2026-10-13 — all five batches drained). **Rank inheritance
   ended 2026-10-13:** correctness-only descendants (M0146-0160..0166 and
   later filings) carry no item rank — the S7 carve-out queues them under
   the unnamed-task fallback below item 10 unless the owner places them.
   Findings still file (record, don't drop).
3. **M0146 post-cutover plan parity** — the current focus (TPC-H/TPC-DS
   plans identical to PG 18.3). Standing members and owner decisions in
   force:
   - **M0146-0014** parity-closure sweep — the S9 filing channel
     (FILEABLE-ROOTS above; a channel, not stock).
   - **M0146-0042** EXPLAIN text-identity burn-down — owner-scoped:
     `rendering`-category slices file under 0042, not 0005. Stock only
     per the S9 burn-down clause.
   - **M0146-0009** statistics/cardinality children are interleaveable:
     selectable whenever a slice's first-divergence is statistics-driven.
   - **M0146-0007** regress-fidelity burn-down — remaining slices queue
     below item 10 under the S7 carve-out (note in its entry).
   - **Next item-3 work (owner decision 2026-10-15): M0146-0178 slice
     2** — `IndexRealPages` catalog-database resolution (`searchCtx.cat`
     threaded into `estimateIndexGeometry`); owner gate cleared
     2026-10-15 (SF0.25 reload). TPC-H corpus realigned to PG :65432's
     row set the same day — `routing-realign-20261015` (12/22 MATCH) is
     the TPC-H actionable pool; Q5's >900s divergent-plan timeout is a
     recorded finding.
   - M0146-0005dp stays re-opened (its 0049/0049e gate landed).
   - Held: M0146-0010a, M0146-0035 `[!]`.
   - History — the M0145 chain narrative, M0146-internal ordering
     decisions through 2026-10-14 (0005/0006 sequencing, 0049 re-open,
     0068 multiplier retirement, queue drains): archive 013.
4. **M0141-S2a-fix2r: DISCHARGED** (`[x]`; degradations file as their own
   tasks, never reverted).
5. **M0141-S2a-fix1-sweep → M0141-S2b-6-resume → M0139-0007c:
   DISCHARGED** (all `[x]`).
6. **M0141-S7: DISCHARGED** (closed `[x]` 2026-10-06; exec-d residual
   stays a recorded ledger deferral).
7. **M0140-0007 — `Parallel Hash` (`parallel_hash = true`): HELD `[!]`**
   (0006a/b/c landed).
8. **M0142-0005 — per-worker Memoize: HELD `[!]`** (0016c, 0003i landed).
   The **M0142-0008 chain is UNFROZEN** (owner decision 2026-09-20) —
   0008a-3 / 0008c-1a / 0008c-3d / 0008c-4 stay `[ ]` but PARKED: the
   producer landed and post-producer reachability re-measured ZERO; the
   remaining walls are the leaf-count/lateral classes owned by
   M0145-0003/0010, so these are NOT selectable work (archive 013).
9. **M0143 remaining tasks: DISCHARGED** (milestone complete; archive 014).
10. M-NIGHTLY open items, then the pre-existing milestones
    (M0119 → M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110).
    (Reachable only after the S9 pass above files nothing.)

**New task fields (2026-09-18).** Every task filed from now on carries, each at
the start of its own line: `Kind: recon|impl`, `Parent: <task-id|none>`, and on
completion `Movement: yes — <instrument + number>` or `Movement: none`. A
trace-only change to `internal/` or `cmd/` is `Kind: impl`, never a recon
(AGENT.md C1). Production commits now need their gate stamps **whatever
milestone they name**, M-NIGHTLY included.

**`Movement:` standardised (owner decision 2026-10-02).** Every DONE/`[x]`
entry ends with exactly one `Movement:` line in one of these forms:
`Movement: yes — <instrument> <before>→<after>` (instrument = the metric
that moved, e.g. `TPCH match 10→11`, `SF0.25 text-identical 25→26`,
`ea-ratchet 10→9`, `regress join 14868→14730`), or `Movement: none — <one-line reason>` (`recon`,
`test-only`, `instrument artefact — <why invisible>`, `parity held`,
`correctness fix — no plan instrument`). Instrument-artefact `none`s name
why the instruments cannot see the change (the LINEAGE-BASELINE pins show
why this matters for S4's budget).

FROZEN-PREFIXES:
(empty — the M0142-0008 chain was UNFROZEN by owner decision 2026-09-20;
see item 8 and archive 013)

LINEAGE-BASELINE: M0145-0001 M0145-0003 M0145-0006 M0145-0014 M0145-0015 M0145-0016 M0145-0017 M0145-0004a M0145-0005 M0145-0007 M0145-0027 M0145-0028 M0145-0004 M0145-0026 M0145-0026a M0145-0029 M0145-0030
(owner pin(s) — resolves the S4 exhaustion on root `M0145-0001`; full pin history in archive 013)

LINEAGE-BASELINE: M0142-0005 M0142-0005b M0142-0005c M0142-0005d M0142-0005e M0142-0005f
(owner pin(s) — resolves the S4 exhaustion on root `M0142-0005`; full pin history in archive 013)

LINEAGE-BASELINE: M0144-0002 M0144-0011a-2 M0144-0011a-3 M0144-0011b M0144-0011b-1 M0144-0011c
(owner pin(s) — resolves the S4 exhaustion on root `M0144-0002`; full pin history in archive 013)

LINEAGE-BASELINE: M0146-0009 M0146-0009c M0146-0009d M0146-0009e M0146-0009f M0146-0009k
(owner pin(s) — resolves the S4 exhaustion on root `M0146-0009`; full pin history in archive 013)

LINEAGE-BASELINE: M0146-0049 M0146-0049c M0146-0049d M0146-0049d1 M0146-0049d2 M0146-0049d3
(owner pin(s) — resolves the S4 exhaustion on root `M0146-0049`; full pin history in archive 013)

LINEAGE-BASELINE: M0146-0005 M0146-0111 M0146-0112 M0146-0113 M0146-0114 M0146-0115
(owner pin(s) — resolves the S4 exhaustion on root `M0146-0005`; full pin history in archive 013)

LINEAGE-BASELINE: M0146-0007 M0146-0119 M0146-0123 M0146-0124 M0146-0129 M0146-0145
(owner pin(s) — resolves the S4 exhaustion on root `M0146-0007`; full pin history in archive 013)

LINEAGE-BASELINE: M0146-0055 M0146-0076 M0146-0077 M0146-0080 M0146-0081 M0146-0082
(owner pin(s) — resolves the S4 exhaustion on root `M0146-0055`; full pin history in archive 013)

LINEAGE-BASELINE: M0122-0015 M0122-0015b M0122-0015c M0122-0015d M0122-0015e M0122-0015g
(owner pin(s) — resolves the S4 exhaustion on root `M0122-0015`; full pin history in archive 013)

LINEAGE-BASELINE: M0146-0014 M0146-0148 M0146-0014c M0146-0149 M0146-0150 M0146-0014d
(owner pin(s) — resolves the S4 exhaustion on root `M0146-0014`; full pin history in archive 013)

**UNFROZEN (owner decision 2026-09-20):** the M0142-0008 chain —
see item 8. The Q78 `outer-over-derived` firewall constraint on this
chain is MOOT (M0145-0018 landed `[x]`); reachability was re-measured
ZERO post-producer, so its parked entries stay non-selectable until the
leaf-count/lateral classes move. Full text: archive 013.

OWNER DECISIONS history → `completed_milestones/completed_fix_plan_013.md`
(2026-09-22, 09-23, 09-24, 09-25, 09-27, 10-02, 10-03, 10-06, 10-12,
10-12 evening, 10-13, 10-13 (b) evening, 10-13 (c) parity self-
replenishment, 10-15, 10-15 (b) TPC-H corpus realign).

OWNER DECISIONS 2026-10-15 (c) — acceptance-arm unblock:
- The M0146-0178 G1 escalation is resolved by an `XFAIL` pin in
  `bench/tpch/baseline-digests.txt` + digestdiff support (option (b),
  narrowed: only the baseline-pinned error passes as `EXPECTED-ERROR`;
  recovery forces `XFAIL-RESOLVED` → re-pin). Option (a) was inert —
  gate-exceptions waive only SKIP-BLOCKED, and the arm returns FAIL;
  option (c) deadlocked — the Q5 plan fix itself needs the arm.
- :65433 TPC-H index compaction: REINDEX is unavailable (M0146-0196 —
  reproduced on :65433), so the cluster was reloaded by INSERT replay
  under a 7b356ec04 binary — a NEGATIVE result: identical index sizes
  (117050 / 28617 / 41512 pages). The remaining bloat driver is open
  under M0146-0195; compacting the ref indexes goes through an 0196 fix
  + REINDEX TABLE.
- M0146-0196 stays below item 10 (S7 carve-out confirmed).
- M0146-0178 de-escalated `[!]`→`[ ]`; resume = apply the parked
  slice2.patch, re-run the arm, commit.

**M-NIGHTLY filing is unconditional**: every loop reads
`ci/logs/action-items.md` and files each new `## AI-` subject under M-NIGHTLY.
Its selection condition lives in this section's **Selection rules**
(item-10 order plus the build/gate exception). When selected: re-run its
repro at HEAD first, fix with normal gates, cite the AI-id, tick it.

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
- **DATA LOSS 2026\-09\-22 \(loop \#6\), recorded so the next loop does not
  trust absent state.** A scripted edit in this loop truncated
  `.ralph/fix_plan.md` to zero bytes \(the Python `open\(path,'w'\)` ran before
  the assertion that aborted the edit\). The file was restored from the
  committed HEAD copy plus that loop's own edits, so **every UNCOMMITTED
  fix\-plan edit made by loops before it is gone**. What the pre\-truncation
  copy carried, from this loop's own reading of it:
  - 16 `testport/TestPort_*` nightly rows flipped `[ ]` -> `[x]` as "stale
    nightly failure, repro PASSes at HEAD" — regenerate by re\-running each
    `AI-` id's repro, not by trusting this note.
  - `testport/TestPort_PgoutputInterop*` \(subscriber/publisher\-start\),
    `M0119-0006`, "schemas are registered process\-wide" and the template1
    rows flipped to `[!]` with blocker text.
  - `M0140-0007` and `M0141-S7` flipped to `[!]` \(their escalations survive
    in their design docs, which were NOT lost\).
  - `M0122-0008`/`-0009`/`-0013` and two ACL rows flipped to `[x]` — note that
    their code is still UNCOMMITTED working\-tree WIP, so those marks were
    ahead of the tree either way.
  Nothing committed was lost; the deferral ledger and every design doc are
  intact. Any fix\-plan edit worth keeping should be committed in the loop that
  makes it.
- Completed milestones are archived under `completed_milestones/` (latest: `completed_fix_plan_015.md`); reference-only, not actionable.
  `completed_fix_plan_012.md`); reference-only, not actionable.

## M-NIGHTLY — standing regression triage (STANDING — ACTIVE since 2026-08-08)

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

### Manually discovered (not yet in a nightly `ci/logs/action-items.md` run)

_(P0 incident tasks, nightly-run logs and completed manually-discovered
tasks archived → `completed_milestones/completed_fix_plan_013.md`.)_

## Archived — complete (see `completed_milestones/completed_fix_plan_013.md`)

P0 — Incident recovery (all `[x]` 2026-09-18); M-NIGHTLY run logs
20260901-010436 .. 20260928-004845; completed manually-discovered
tasks; banner history (OWNER DECISIONS, discharged items, pins'
notes, UNFROZEN detail).

## Archived — complete (see `completed_milestones/completed_fix_plan_012.md`)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


M0130 (Cluster-directory compat with PG 18.3 + PG physical replication).

## Archived — complete (see `completed_milestones/completed_fix_plan_009.md`)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


M0117 (CLOG ↔ PostgreSQL subsystem alignment), M0118 (Upstream Isolation Spec
Suite Pass-Through), M0120 (WordPress WP-CLI verification execution + evidence),
M0121 (WordPress WP-CLI verification remediation).

## Archived — complete (see `completed_milestones/completed_fix_plan_008.md`)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


M0096 (RC isolation feature impl + spec pass), M0100 (RC isolation runtime
closure / 21-spec pass), M0102 (heterogeneous streaming-replication +
SIGKILL-failover E2E), and the two completed Maintenance fixes
(MAINT-STATEGUARD-RECONCILE, MAINT-TPCH-RELOAD). Earlier milestones:
`completed_fix_plan_001.md` .. `completed_fix_plan_007.md`.

---

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


Design: `docs/design/0095-0003-*`. Goal: port the client-tools-tap suite and the
engine features its `t.Skip`'d scripts need. (`pg_ctl` 001–004 already PASS.)

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

- [ ] **M0095-0003**.
## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by M0094
(recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.
Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP**.
- [ ] **M0110-0002 — pg_waldump TAP**.
- [ ] **M0110-0003 — pg_amcheck TAP**.
## M0119 — Deferral-Ledger Backlog Consumption (filed 2026-06-29)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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
2026-09-14 the banner demoted it; its live rank is whatever the `## Current
Priority` banner at the head of this file currently says (item 10 tail as of
2026-09-24).**

- [!] **M0119-0006 — pg_amcheck server tier**.
  - **ESCALATION 2026\-10\-12 \(lineage budget, Rule A\)\.** The last five
    completed descendants \(template1 namespace collision, current\_database
    in template1, template1 extension restart, per\-database schemas,
    creation\-namespace 3F000\) all carry `Movement: none`, so the root is
    parked `[!]` until the owner re\-opens it \(`LINEAGE\-BASELINE:
    M0119\-0006 ...`\)\.
    - What was attempted: per\-database catalog scoping and DDL namespace
      validation found while making pg\_amcheck / pg\_dump work across
      databases — each closed a measured cross\-database leak or a lax DDL
      path, verified against PG 18\.3 probes\.
    - What each step proved: the family is catalog correctness with no TPC
      instrument; `Movement: none` is structural\.
    - Remaining blocker: the lineage rule itself\. Owner call: re\-baseline
      or close the family\.
    - Expected movement if unblocked: none on match / CATEGORIES /
      ea\-ratchet; gains show in regress \(namespace, create\_schema,
      alter\_generic\) and pg\_dump round\-trips\.
    - Remaining size — open child already filed: the one\-object DROP CASCADE
      NOTICE wording\. **Not filed** \(guard\-refused; recorded in the
      2026\-10\-12 ledger rows\):
      - `CREATE TABLE pg\_catalog\.x` is accepted; PG raises 42501
        \(heap\_create, allow\_system\_table\_mods\)\.
      - the schema registry folds names to lower case: `"Mixed"` renders as
        `mixed` and collides with `mixed`\.
      - NOTICEs carry SQLSTATE 00000 \(PG: 42P06 / 42P07 on "already
        exists, skipping"\)\.
      - unqualified CREATE with no existing schema on search\_path is not
        checked \(PG 3F000 "no schema has been selected to create in"\)\.
      - bootstrap schemas are shared across databases \(DROP SCHEMA public
        affects all\); GRANT ON SCHEMA / pg\_proc pronamespace lookups are
        unscoped\.
    - **RESOLVED 2026\-10\-12 \(owner, delegated\): the hold STANDS — this
      family stays parked `[!]`.** Its children are old\-format entries
      without task ids; the guard parses title fragments as pseudo\-ids
      \(`current\_database\(\)`, `` `pg\_database\.datacl` ``\), which
      punctuation\-normalising pins cannot name safely\. The not\-filed
      residues above stay ledgered deliberately; anything worth doing can
      be re\-filed `Parent: none` with a real id and owner\-placed\.
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
    - Recorded-not-fixed (in the doc's deferral list): ~~template0 is
      connectable so a `CREATE EXTENSION` there writes base/4 and would be
      cloned into future `CREATE DATABASE`s~~ **FIXED 2026-09-22, see the bu
      slice below**; a template1 install re-attributes to "postgres" on
      restart (shared bootstrap namespace — **ROOT-CAUSED 2026-09-22; a first
      write-up called it independent of the template1 namespace collision,
      which a postgres-side control REFUTED the same day: it IS that
      collision. Re-filed below and marked `[!]` behind it**); ~~user-db-local `extnamespace`
      falls back to "public" at reload~~ **FIXED 2026-09-22, see the bw
      slice below — and the recorded symptom understated it.**
  - 2026-09-22 (bw): **per-database `pg_namespace` reload**, closing the
    second of the bs slice's three recorded-not-fixed items.
    Movement: none — SF0.25 `PLAN-SHAPE same=99 changed=0`; a startup-reload
    fix, which none of S3's three instruments measures.
    - **The recorded symptom was the tip of a bigger bug.** The item read
      "user-db-local `extnamespace` falls back to public at reload". Measuring
      it showed the extension was the SYMPTOM: the SCHEMA ITSELF vanished. A
      `CREATE SCHEMA` inside a `CREATE DATABASE`d database did not survive a
      restart at all, so `reloadUserExtensionsFromHeap` could not resolve its
      OID and silently fell back to `public`.
    - **Root cause**: `reloadUserSchemasFromHeap` scanned only
      `cat.DBOID()` — the connecting catalog's own pg_namespace heap. A user
      database's rows sat unread in `base/<thatDbOid>/2615`. Tables in the
      same database reloaded fine (they have their own per-database pass),
      which is exactly what disguised this as an extension-only problem.
    - **The first fix attempt was wrong and the live check caught it.**
      Copying `reloadUserExtensionsFromHeap`'s per-database loop straight
      into `reloadUserSchemasFromHeap` changed nothing, because that pass
      runs at `open.go:1397` while `reloadDatabasesFromHeap` is at 1546 —
      `ListDatabases()` is still EMPTY there. The extension reload only works
      because it runs at 2401, after the database list exists.
    - Fixed by SPLITTING rather than moving: the early pass keeps reading the
      connecting catalog's heap (the ts_dict/ts_config reloads that follow it
      need the schema OID map populated, and they run before 1546), and a new
      `ReloadUserDatabaseSchemasFromHeap` runs immediately after the
      pg_database reload for every other database. Moving the whole pass late
      would have broken those dependents.
    - Verified live end to end on a fresh cluster: schema `uext` and
      `extnamespace` both survive a restart in a `CREATE DATABASE`d database;
      both were wrong before.
    - Regression test `TestPort_PerDatabaseSchemaSurvivesRestart` asserts BOTH
      halves. The extension's namespace alone would pass if the reload merely
      guessed the right name; the schema's own existence proves the heap was
      read. Verified non-vacuous — removing the new pass fails both
      assertions, including the `public` fallback.
    - **Recorded, NOT fixed** (ledgered): schemas are registered
      process-wide, so a `CREATE SCHEMA` in one database is visible from
      another — measured directly, not inferred. This pass deliberately
      restores the RUNTIME behaviour rather than inventing scoping in the
      reload: a schema that silently disappears across a restart is closer to
      data loss than one that is over-visible, and making the two paths agree
      is the prerequisite for scoping them together later. That scoping is the
      same M0122-0007 family as the template1 collision escalation.

- [!] **M0122-0008 — Auth / roles / multi-DB isolation / encoding**.
  Kind: impl
  Parent: none
  - **ESCALATION 2026\-10\-12 \(lineage budget, Rule A\)\.** The last five
    completed descendants \(ALTER SYSTEM, SET value lists, SET LOCAL outside
    a block, proconfig/setconfig names, SET TIME ZONE INTERVAL\) all carry
    `Movement: none`, so the lineage guard refuses new children and the
    root is parked `[!]` until the owner re\-opens it
    \(`LINEAGE\-BASELINE: M0122\-0008 ...` in the banner\)\.
    - What was attempted: GUC front\-end conformance — each step closed a
      wrong\-value or wrong\-error divergence measured against PG 18\.3
      probes and regress diffs\.
    - What each step proved: the family is correctness work with no TPC
      instrument by nature \(no TPC\-H/DS query sets a GUC\); `Movement:
      none` is structural, not a sign of stalled progress\.
    - Remaining blocker: the lineage rule itself, which this family can
      never satisfy\. Owner call: re\-baseline, or close the family\.
    - Expected movement if unblocked: none on match / CATEGORIES /
      ea\-ratchet; gains show in regress diffs \(horology, guc\) and probes\.
    - Remaining size — open children already filed: GUC range\-error
      wording, GUC registry divergence from PG 18\.3, SHOW column case,
      COPY in non\-default databases \(M0122\-0015a\), and others below\.
      **Not filed** \(guard\-refused; recorded in the 2026\-10\-12 ledger
      row\):
      - to\_char\(timestamptz\) formats in UTC regardless of TimeZone
        \(`evalToChar`; formatting\.c converts with the session zone\)\.
      - SET of a SIGHUP / POSTMASTER parameter raises 22023 instead of
        55P02 \(three `cannot be changed now` sites in guc\.go /
        session\.go\)\.
      - initdb writes no host time zone \(select\_default\_timezone\);
        the TimeZone boot value is UTC where PG\'s is GMT\.
    - The S2 wrong\-results defect this probe also found \(function SET
      clauses never applied\) is filed as M0146\-0155 with `Parent: none`
      for owner placement\.
    - **RESOLVED 2026\-10\-12 \(owner, delegated\): the hold STANDS — this
      family stays parked `[!]`.** Same old\-format problem as M0119\-0006:
      its children\'s parsed ids are title fragments \(`ALTER`, `SET` …\)
      and `SET` is ambiguous under first\-occurrence matching, so a
      LINEAGE\-BASELINE here would be fragile\. The not\-filed residues
      stay ledgered; re\-file `Parent: none` if one is worth doing\. Its
      S2 finding M0146\-0155 was owner\-placed into item 2a today\.
  - 2026\-09\-24 \(ralph2 loop \#48\): **the `ssl\*` GUC family, as the
    oracle's no\-SSL build defines it** \(`0853825e4`\).
    Movement: none — GUC registry conformance; no planner path touched.
    Design: `docs/design/0100-0149/0122-0008-ssl-guc-family-nossl-build.md`.
    - Re\-verified the auth group first: SASLprep and `scram\_iterations`
      are already done; channel binding is the remaining scope, and it
      needs TLS.
    - The oracle is built WITHOUT SSL \(`USE\_OPENSSL` undefined\). It
      answers SSLRequest with 'N', as goopg does, but registers all 17
      `ssl\*` GUCs with the no\-SSL defaults and rejects `ssl = on`.
      goopg registered none, so `SHOW ssl` failed.
    - Landed: all 17 registered; `check\_ssl` on SET, at boot and on
      reload; `CheckFn` runs for every type; new `FlagNoShowAll` also
      hides `is\_superuser`, `session\_authorization` and
      `default\_with\_oids` from SHOW ALL, as PG does; 16 pg\_settings rows
      byte\-identical to the oracle.
    - Gates: units, tpch\-spotcheck, tpcds\-sf025, acceptance arm; regress
      guc and sysviews identical to a HEAD baseline.
    - Found and filed separately: ALTER SYSTEM is a no\-op; GUC range
      errors use goopg's own wording.
  - 2026\-09\-23 \(ralph2 loop \#8\): **`GRANT … TO PUBLIC WITH GRANT
    OPTION` refused with 0LP01 on every grant path** \(`75140193f`\),
    closing the divergence the M0122\-0008a fixture ledgered.
    Movement: none — ACL DDL conformance; SF0.25 `PLAN-SHAPE same=99`.
    Design: `docs/design/0100-0149/0122-0008-grant-option-to-public.md`.
    - PG: `merge_acl_with_grant` \(`aclchk.c:208`\), shared by every
      `ExecGrant_*` and ALTER DEFAULT PRIVILEGES. goopg: one predicate
      `catalog.GrantOptionToPublic`, checked BEFORE any ACL write \(the
      store is not transactional\) on the five typed executor paths and
      the postmaster autocommit fast path.
    - Verified on a private PG 18.3: nine autocommit forms identical
      \(SQLSTATE, message, line\); controls \(role grantee, REVOKE GRANT
      OPTION FOR … FROM PUBLIC\) accepted on both.
    - Gates: units, tpch\-spotcheck, SF0.25 sweep, acceptance arm,
      `TestPort_RegressSuite` — all PASS.
    - Still diverges inside an explicit transaction \(CompatNoop path;
      the M0134\-0069 ledger gap\) — ledgered.
  - 2026-09-22: **`GRANT … ON DATABASE` now reaches databases other than the
    connected one**, consuming the `unimplemented_feat.json` entry
    *"Implement multi-database support so a single server instance can manage
    multiple independent databases"* (marked `resolved`; its cited evidence
    was `operators_ddl_database_acl.go` single-database only).
    Movement: none — SF0.25 `PLAN-SHAPE same=99 changed=0`; a DDL/catalog
    conformance fix, which none of S3's three instruments measures.
    Design: `docs/design/0100-0149/0122-0008-cross-database-grant-on-database.md`.
    - **The defect, measured against a PG 18.3 oracle**:
      `GRANT CONNECT ON DATABASE otherdb TO r1` from a session connected to
      `postgres` reported `GRANT` and left `otherdb.datacl` NULL, where PG
      leaves `{=Tc/postgres,postgres=CTc/postgres,r1=c/postgres}`. An unknown
      database name succeeded **silently** instead of raising `3D000` — the
      worse half, since a typo granted nothing and said so in no way a client
      could detect.
    - **A v0 assumption that outlived its premise.** The code compared every
      named database against `ctx.CurrentDatabase`; its own comment justified
      this with *"goopg v0 has a single logical connected database"*. goopg
      now creates, persists and routes real per-database catalogs, and
      upstream's `ExecGrant_Database` (`aclchk.c`) resolves names against the
      SHARED `pg_database` with no reference to `MyDatabaseId`.
    - **Fixed as a sibling pair (Hard-won Rule \#2)** — fixing only the writer
      would have stored an ACL no `SELECT` could show:
      - writer: every name resolved through `catalog.ResolveDatabaseOid`, the
        whole list before any is applied, so one bad name changes nothing
        before it errors; the per-database body moved to
        `applyDatabaseACLChange` so "only the live database" can no longer be
        a property of the statement rather than of the loop driving it;
      - reader: the `pg_database` virtual row builder renders `datacl` for
        every row instead of only `postgres`.
    - **Why `ResolveDatabaseOid` is the right key for both halves**: it
      returns `DBOID()` for `"postgres"` — the key the single-database code
      already used — so the connected database's behaviour is unchanged BY
      CONSTRUCTION, not by coincidence. That is what makes it safe to touch
      the row every connection reads.
    - Live verification: cross-db GRANT, cross-db REVOKE, the multi-name list
      form, the `3D000` refusal and the connected-db path all match PG 18.3;
      `global/1262` gains a row version per resync, so an attached standby
      sees the ACL too.
    - Non-vacuity checked on BOTH halves: neutralising the writer fails
      exactly the three cross-database arms; neutralising the reader fails the
      render test while the writer's tests stay green.
    - Gates: units; `internal/executor` + `internal/catalog`; tpch-spotcheck
      Q12=2/Q13=33; tpcds-sf025 `PLAN-SHAPE same=99 changed=0`; pgbench smoke.

- [!] **TLS server and SCRAM\-SHA\-256\-PLUS channel binding** \(filed
  2026\-09\-24 by the ssl GUC slice\). **Owner call.**
  Kind: impl
  Parent: M0122-0008
  - The PG 18.3 oracle and its psql/libpq are built without SSL, so a goopg
    TLS server would diverge from the reference and could not be checked
    against it. Decide whether goopg targets an OpenSSL\-built PG here \(and
    rebuild an SSL oracle\) or stays with the no\-SSL build.
  - Resume point: `handleStartup`'s SSLRequest arm \(internal/postmaster/server.go\),
    `connTypeFor` \(ConnHostSSL\), then `scram.go`'s `p=` branch and the
    mechanism list.
  - **OWNER ANSWER 2026\-09\-25 \(delegated\): stay no\-SSL.** The PG
    18.3 oracle is built without SSL, so a TLS server and channel
    binding could not be checked against it — unverifiable divergence is
    worse than an honest gap. The task stays `[!]` until an SSL\-enabled
    PG 18.3 oracle exists to measure against; the resume point stands.

- [ ] **goopg\'s GUC registry diverges from PG 18\.3\'s parameter set**
  \(measured 2026\-10\-10 against the oracle\'s pg\_settings via
  `pg\_gucs\_gen.go`\)\.
  Kind: bug
  Parent: M0122-0008
  - 191 PG parameters are not registered \(SET / SHOW reject them\)\.
  - 7 goopg\-only names: `max\_predicate\_locks\_per\_page`,
    `\_per\_relation`, `\_per\_xact` are misspellings of PG\'s
    `max\_pred\_locks\_per\_\*`; `vacuum\_multixact\_freeze\_max\_age`,
    `wal\_sender\_memory\_buffer`, `enable\_nestloop\_index`,
    `enable\_opportunistic\_prune` do not exist in PG\.
  - 13 context mismatches; privilege\-relevant: `commit\_delay`,
    `compute\_query\_id`, `track\_io\_timing` are user\-settable in goopg but
    superuser in PG\. Others: `autovacuum\_max\_workers`,
    `autovacuum\_work\_mem`, `bgwriter\_delay`, `bgwriter\_lru\_maxpages`,
    `io\_workers`, `restore\_command`, `wal\_writer\_delay`,
    `wal\_writer\_flush\_after` \(goopg postmaster, PG sighup\),
    `wal\_init\_zero` \(postmaster vs superuser\), `wal\_sender\_timeout`
    \(sighup vs user\)\.
  - First step: fix the three privilege\-relevant contexts and the three
    misspelled names; check each renamed/re\-contexted variable\'s consumer
    \(declared\-but\-unconsumed risk\)\.
  - Slice 1 done 2026\-10\-14: `commit\_delay`, `compute\_query\_id` and `track\_io\_timing` are now
    PGC\_SUSET; `max\_predicate\_locks\_per\_\{xact,relation,page\}` are now PG\'s
    `max\_pred\_locks\_per\_\{transaction,relation,page\}` with guc\_tables\.c\'s ranges \(10/INT\_MIN/0 to
    INT\_MAX\)\. postgresql\.conf\.sample follows PG\'s lines\.
    - Consumer check: the three predicate\-lock GUCs have no consumer \(nothing calls
      `mvcc\.Manager\.SetPredicateLockLimits`\); ledgered\. The three SUSET parameters keep their consumers \(startup
      `boolGUC`/`intGUC`, the track\_io\_timing OnChange hook\)\.
    - Test `TestGUCRegistryPrivilegeContextsAndPredLockNames`; `TestPredicateLockGUCDefaults` follows the PG names\.
    - Gates: units, postmaster, tpch\-spotcheck, TPC\-H acceptance arm, sf025 PASS=99; regress guc / sysviews identical\.
    - Remaining: 191 unregistered PG parameters, 4 goopg\-only names, 10 other context mismatches \(listed above\)\.
  - Slice 2 done 2026\-10\-14: a reload now reaches the walwriter, bgwriter and group\-commit settings
    \(`initdb\.Runtime\.ApplyWriterSettings`, called from cmd/goopg\'s reload hook\)\.
    - `wal\_writer\_delay`, `wal\_writer\_flush\_after`, `bgwriter\_delay` and `bgwriter\_lru\_maxpages` are now
      PGC\_SIGHUP\. The two delays carry PG\'s `ms` unit, so a PG\-written `bgwriter\_delay = 50ms` applies\.
    - `commit\_delay` / `commit\_siblings` were reloadable in the registry, but no reload reached the WAL writer;
      it does now\.
    - `bgwriter\_lru\_maxpages = 0` stops only the writes, as in PG\. The goroutine stays, so a reload can raise it\.
    - `autovacuum\_max\_workers` and `autovacuum\_work\_mem` are now PGC\_SIGHUP, and `wal\_sender\_timeout` is
      PGC\_USERSET\. All three are unconsumed; ledgered\.
    - Kept PGC\_POSTMASTER, ledgered: `io\_workers`, `restore\_command` and `wal\_init\_zero` are read once at
      start\. The `\*\_flush\_after` unit gap is also ledgered\.
    - Live probe: after `goopg reload`, the log shows `changed=\[bgwriter\_delay wal\_writer\_delay
      bgwriter\_lru\_maxpages commit\_delay\]` with no warning\.
    - Tests: `TestApplyWriterSettingsReachesRunningLoops`, `TestBgwriterRunsWithZeroLRUMaxPages`,
      `TestBgwriterReloadedSettingsTakeEffect` and `TestGUCRegistryReloadableContexts`\.
    - Gates: units, postmaster, tpch\-spotcheck, sf025 PASS; regress guc / sysviews identical to HEAD\.
    - Remaining: 191 unregistered PG parameters; 4 goopg\-only names; `io\_workers` / `restore\_command` /
      `wal\_init\_zero` contexts \(need live consumers first\)\.
  - Slice 3 done 2026\-10\-14: the four `\*\_flush\_after` parameters carry PG\'s block units\.
    - SHOW gives `1MB` / `256kB` / `512kB` / `0`, identical to the scratch PG 18\.3 oracle\.
    - `wal\_writer\_flush\_after` now stores pages \(default 128, max INT\_MAX\)\. cmd/goopg converts it to bytes
      \(`walWriterFlushAfterBytes`\)\.
    - `checkpoint\_flush\_after` / `bgwriter\_flush\_after` now reach the pool on reload
      \(`ApplyWriterSettings`\)\.
    - Ledgered: `wal\_buffers` is still bytes; PG uses XBLOCKS with \-1 = auto\.
    - Test `TestFlushAfterGUCsUseBlockUnits`; the reload test covers the pool thresholds\.
    - Gates: units, postmaster, tpch\-spotcheck, sf025 PASS; regress guc / sysviews identical\.
    - Remaining: 191 unregistered PG parameters; 4 goopg\-only names; `io\_workers` / `restore\_command` /
      `wal\_init\_zero` contexts; `wal\_buffers` units\.
  - Slice 4 done 2026\-10\-14: `wal\_buffers` is counted in XLOG\_BLCKSZ pages with PG\'s boot \-1 auto\-tune\.
    - `cmd/goopg` `resolveWALBuffers` applies PG\'s rule: shared\_buffers/32, capped at one segment, at least 8;
      manual values below 4 become 4\.
    - The resolved count is stored back, so SHOW shows `4MB` at the 128MB default\. It was `16777216`\.
    - The byte\-valued bench / analysis scripts now use `MB`\.
    - Ledgered: a reload warns for every postmaster entry even when it is unchanged \(PG warns only on a change\)\.
      `pg\_settings` lists only 58 rows\.
    - Test `TestResolveWALBuffersFollowsPG`\.
    - Gates: units, tpch\-spotcheck, sf025 PASS; regress guc / sysviews identical\.
    - Remaining: 191 unregistered PG parameters; 4 goopg\-only names; `io\_workers` / `restore\_command` /
      `wal\_init\_zero` contexts\.
  - Slice 5 done 2026\-10\-14: goopg\'s own knobs live in the `goopg\.` custom\-variable namespace\.
    - Renamed: `goopg\.wal\_sender\_memory\_buffer`, `goopg\.enable\_nestloop\_index` and
      `goopg\.enable\_opportunistic\_prune`\.
    - The bogus `vacuum\_multixact\_freeze\_max\_age` is dropped\.
    - `autovacuum\_multixact\_freeze\_max\_age` takes PG\'s 400000000 \[10000, 2000000000\]\. It was 4000000000\.
    - Test `TestRegistryNamesArePGOrGoopgNamespaced`: every registered name is PG\'s or `goopg\.`\-prefixed\.
    - Default/range audit vs PG pg\_settings: 37 differences, mostly formatting\. Ledgered:
      - non\-plan ones;
      - the plan\-shaping ones \(`work\_mem` 512MB, `max\_parallel\_workers\_per\_gather` 4, partitionwise on, jit
        off\) as an owner decision\.
    - Gates: units, postmaster, tpch\-spotcheck, sf025 PASS; regress guc / sysviews identical\.
    - Remaining:
      - 191 unregistered PG parameters;
      - `io\_workers` / `restore\_command` / `wal\_init\_zero` contexts;
      - the non\-plan default/range differences\.
  - Slice 6 done 2026\-10\-14: defaults and ranges that do not shape plans now follow guc\_tables\.c\.
    - `autovacuum\_vacuum\_max\_threshold` is 100000000 \[\-1, INT\_MAX\]\. The launcher\'s cap now follows
      autovacuum\.c: 0 caps at zero and \-1 disables the cap\.
    - Defaults: `vacuum\_multixact\_failsafe\_age` 1600000000, `compute\_query\_id` auto\.
    - Maxima: `io\_workers` 32, `bgwriter\_lru\_maxpages` INT\_MAX/2, `scram\_iterations` /
      `wal\_sender\_timeout` INT\_MAX, and the cost GUCs DBL\_MAX\.
    - Tests: `TestGUCDefaultsAndRangesFollowPG` and `TestAutovacuumVacuumMaxThresholdFollowsPG`\.
    - Ledgered:
      - `io\_max\_concurrency` auto: PG\'s value is per\-backend, goopg\'s a global cap;
      - the launcher ignores the per\-table max\-threshold reloption\.
    - Gates: units, postmaster/autovacuum, tpch\-spotcheck, sf025 PASS; regress guc / sysviews identical\.
    - Remaining:
      - 191 unregistered PG parameters;
      - `io\_workers` / `restore\_command` / `wal\_init\_zero` contexts;
      - `io\_max\_concurrency`;
      - OWNER: the plan\-shaping defaults\.
  - Slice 7 done 2026\-10\-14: the PGC\_INTERNAL preset parameters clients read are registered\.
    - `max\_identifier\_length`, `max\_function\_args`, `max\_index\_keys`, `segment\_size` \(1GB\),
      `wal\_block\_size`, `debug\_assertions`, `data\_checksums` and `huge\_pages\_status`\.
    - cmd/goopg records `data\_checksums` from pg\_control and sets `huge\_pages\_status` = off at start\.
    - A SET refused by context now uses set\_config\_option\'s message per context and SQLSTATE 55P02\. It was
      always "cannot be changed now" under 22023\.
    - Output is byte\-identical to the scratch PG for SHOW and the refusals, SQLSTATE included\.
    - Tests: `TestPresetOptionsAreReported`, `TestSetRefusalFollowsContext` and `TestRecordClusterStateGUCs`\.
    - Ledgered:
      - `data\_directory` / `config\_file` / `hba\_file` / `ident\_file` need a GUC\_SUPERUSER\_ONLY read gate first;
      - `shared\_memory\_size` / `num\_os\_semaphores` have no goopg equivalent;
      - FUNC\_MAX\_ARGS / INDEX\_MAX\_KEYS are advertised but not enforced;
      - the startup packet still refuses backend\-start contexts\.
    - **Not selectable \(found 2026\-10\-14\):** the root M0122\-0008 has been `\[!\]` since 2026\-10\-12 \(lineage
      budget; the owner\'s "hold STANDS"\)\. Under S7 its children are held too\. Slices 1–7 were selected from a
      stale baton\. Stop here until the owner re\-opens the root\.
    - Remaining: 178 unregistered PG parameters\. Several have semantics \(`transaction\_read\_only`,
      `quote\_all\_identifiers`, `transform\_null\_equals`, `array\_nulls`\), so they must not be registered
      inert\.

- [ ] **Read\-only transactions are not enforced; `default\_transaction\_read\_only` is ignored** \(found 2026\-10\-14
  during the GUC registry work\)\.
  Kind: bug
  Placement: S2 defect awaiting owner placement \(writes accepted where PG raises 25006\)\. It is filed without a
  parent because M0122\-0008\'s lineage budget is exhausted \(ralph\-lineage\-guard rule A\)\.
  - Repro on a fresh cluster:
    - `BEGIN READ ONLY; INSERT INTO t VALUES \(1\);` succeeds\. PG 18\.3 raises 25006 "cannot execute INSERT in
      a read\-only transaction" \(`PreventCommandIfReadOnly` / `ExecCheckXactReadOnly`, executor/execMain\.c\)\.
    - `SET default\_transaction\_read\_only = on; INSERT …` COMMITS the row\. PG sets XactReadOnly from
      DefaultXactReadOnly in StartTransaction \(xact\.c\)\.
    - `BEGIN; SET TRANSACTION READ ONLY; INSERT …` succeeds, and SET TRANSACTION answers tag `OK` \(PG: `SET`\)\.
    - `SHOW transaction\_isolation` inside `BEGIN ISOLATION LEVEL SERIALIZABLE` prints `read committed`\.
      `transaction\_read\_only` / `transaction\_deferrable` / `default\_transaction\_deferrable` are not
      registered at all\.
  - goopg tracks the BEGIN flag \(`Session.SetReadOnlyTxn`\), but only nextval/setval consult it\.
  - First step: enforce read\-only in the DML/DDL entry points per ExecCheckXactReadOnly; seed it from
    default\_transaction\_read\_only at transaction start; register the transaction\_\* characteristic GUCs as
    views of the live transaction state \(check\_transaction\_read\_only / check\_XactIsoLevel rules\)\.
  - Expected instrument: regress `transactions` \(read\-only cases\) and `guc`; a postmaster test of each repro\.

- [ ] **A rolled\-back DROP of a partition, an inheritance tree or a CASCADE target loses the table** \(found
  2026\-10\-14 during M0122\-0009 S6b\)\.
  Kind: bug
  Placement: S2 defect awaiting owner placement \(data loss: a rolled\-back DROP removes the table\)\. Filed without a
  parent \(M0122\-0009 lineage past the Movement\-none budget\)\.
  - Repro on a fresh cluster, each followed by a read of the table:
    - `CREATE TABLE p\(a int\) PARTITION BY RANGE \(a\); CREATE TABLE p1 PARTITION OF p FOR VALUES FROM \(0\) TO
      \(1000\); INSERT …; BEGIN; DROP TABLE p; ROLLBACK;` → `p` has 0 rows and `p1` no longer exists;
    - `BEGIN; DROP TABLE par CASCADE; ROLLBACK;` with an inheritance child → the child no longer exists;
    - `BEGIN; DROP TABLE plain CASCADE; ROLLBACK;` with a dependent view → `plain` no longer exists\.
    PG 18\.3 restores all of them \(catalog changes are transactional\)\.
  - Since M0122\-0009 S6b the storage survives \(removal waits for commit\), but the catalog entries are removed during
    the statement and only a ROLLBACK TO SAVEPOINT restores them \(`RecordDDLDrop` is written only at savepoint depth
    \> 0\); a top\-level ROLLBACK does not\. The deferred path \(`AddPendingTableDrop`\) covers only a plain table
    with no partition or inheritance relatives and no temp shadow\.
  - First step: record the `DDLDropUndoEntry` at every depth and restore it in the top\-level ROLLBACK path
    \(`ProcessRollbackUndos`\), including the cascaded objects \(views, partitions, inheritance children\)\.
  - Expected instrument: a testport case per repro; regress `alter\_table` / `inherit` rollback cases\.

- [ ] **A CREATE TABLE inside a rolled\-back savepoint survives** \(found 2026\-10\-14 during M0122\-0009 S6c\)\.
  Kind: bug
  Placement: S2 defect awaiting owner placement \(wrong results: an object exists that the transaction never
  created\)\. Filed without a parent \(M0122\-0009 lineage past the Movement\-none budget\)\.
  - Repro on a fresh cluster: `BEGIN; SAVEPOINT s; CREATE TABLE sp\(a int\); INSERT INTO sp VALUES \(1\); ROLLBACK TO
    s; SELECT count\(\*\) FROM sp; COMMIT;` → the SELECT returns 0 rows instead of 42P01, and `sp` still exists after
    COMMIT\. PG 18\.3 removes it at ROLLBACK TO \(the creation is part of the subtransaction\)\.
  - Cause: `RecordDDLCreate` entries carry no savepoint depth, and `execRollbackTo` undoes only drops
    \(`RollbackDDLDropsToDepth`\); creates are undone only by a top\-level ROLLBACK \(`ProcessRollbackUndos`\)\.
  - First step: record the savepoint depth on `DDLUndoEntry`, undo the creates above the target depth in
    `execRollbackTo`, and then remove a create entry\'s storage at commit when its subtransaction aborted
    \(`endPendingRelDeletes` currently forgets it\)\.
  - Expected instrument: a testport case of the repro; regress `transactions` savepoint DDL cases\.

- [ ] **`pg\_relation\_filepath\(\)` does not exist** \(found 2026\-10\-14 during M0122\-0009 S6b\)\.
  Kind: bug
  Parent: none
  - `SELECT pg\_relation\_filepath\(\'t\'\)` → 42883\. PG returns `base/<db>/<relfilenode>` \(dbsize\.c\)\.
  - First step: an executor builtin from the catalog\'s RelFileNode \(tablespace, database, relfilenode\)\.

- [ ] **Boolean predicate columns are advertised as `text`** \(found 2026\-10\-14 during M0122\-0009 S7\)\.
  Kind: bug
  Placement: S2 defect awaiting owner placement \(wrong result type on the wire: a typed client decodes `t` as a
  string\)\. Filed without a parent \(M0122\-0009 lineage past the Movement\-none budget\)\.
  - Repro on a fresh cluster, `\\gdesc` of
    `SELECT EXISTS \(SELECT 1\), 1 IN \(SELECT 1\), 1 IN \(1,2\), 1 = ANY \(ARRAY\[1\]\), 1 < ALL \(SELECT 2\),
    NULL IS NULL, \'a\' ~ \'a\', true IS TRUE, 1 IS DISTINCT FROM 2`: every column is `text`\. PG 18\.3 types each
    `boolean`\. `NOT EXISTS`, `EXISTS … OR false`, `BETWEEN`, `LIKE` and row `=` are already `boolean`\.
  - Cause: the planner\'s `exprType` \(`internal/optimizer/planner\.go`\) has no arm for `\*ExistsExpr` / `\*InExpr`,
    and its `UnaryOp` / `BinaryOp` arms miss IS \[NOT\] NULL, IS TRUE, regex match and IS DISTINCT FROM; the
    fallback is `unknown`, which goes out as text \(OID 25\)\.
  - First step: add the arms \(bool for every predicate node\), then sweep the remaining predicate shapes with
    `\\gdesc` against PG\.
  - Expected instrument: regress cases whose psql alignment depends on the column type; a wire\-type test\.

- [ ] **GUC range errors use goopg's own wording** \(measured 2026\-09\-24\):
  `SET geqo\_effort = 11` reports `value 11 out of range [1, 10]`; PG
  reports `11 is outside the valid range for parameter "geqo\_effort"
  (1 .. 10)` \(guc.c parse\_and\_validate\_value\). The same holds for the
  real\-valued GUCs.
  Kind: bug
  Parent: M0122-0008

- [x] **M0122-0009 — WAL / recovery / crash-consistency infra**.
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0122\-0009a — REINDEX CONCURRENTLY swaps the rebuilt index file in with no WAL** \(filed 2026\-10\-13 by the
  M0122\-0009 summarizer design review\)\. `swapRelationPhysicalFile` \(`internal/executor/operators\_ddl\.go` \~:692\)
  renames the shadow build over the live relfilenode with `os\.Rename`\. Its comment says no WAL record is needed
  because the rename is atomic\.
  - Why it breaks: a standby, or a PITR restore from a base backup taken before the REINDEX, keeps the OLD index
    file, then replays post\-REINDEX index records and FPIs written against the NEW layout\. The result is a mixed,
    corrupt index → wrong results there\.
  - PG instead builds the new index under a NEW relfilenode, WAL\-logged \(`index\_concurrently\_build`; the build
    logs its pages\), and swaps catalog entries \(`index\_concurrently\_swap`, `catalog/index\.c`\)\.
  Kind: bug
  Parent: M0122\-0009
  - Rank: below item 10 under the S7 carve\-out \(correctness, no plan instrument\) unless the owner places it\.
    Escalated in the baton as a wrong\-results\-on\-standby defect \(S2\)\.
  - First step: reproduce with a physical standby or a base backup \+ replay across a REINDEX CONCURRENTLY, then
    move to PG\'s new\-relfilenode \+ catalog\-swap shape, or WAL\-log the swapped file \(log\_newpage\_range\-style\)\.
- [ ] **M0122\-0009b — ALTER TABLE/INDEX … SET TABLESPACE copies the file with no WAL** \(filed 2026\-10\-13 by the
  M0122\-0009 summarizer design review\)\. `relocateRelationPhysicalFile` \(`operators\_ddl\.go` \~:609\) copies and
  fsyncs the main fork under the SAME relfilenode, in the new tablespace\.
  - PG assigns a new relfilenode and WAL\-logs the copied pages \(`RelationCopyStorage` → `log\_newpage`,
    `catalog/storage\.c`\), so a standby or PITR replay recreates the moved relation\.
  - Without WAL, a replica never sees the move\. Moving a relation away and back inside one backup window also
    leaves stale pages at the original path\.
  Kind: bug
  Parent: M0122\-0009
  - Rank: below item 10 \(S7 carve\-out\) unless the owner places it\.
  - First step: a standby/replay repro across SET TABLESPACE; then a new relfilenode plus WAL\-logged copy, as PG\.
- [ ] **M0122\-0009c — VACUUM tail truncation writes no XLOG\_SMGR\_TRUNCATE in production** \(filed 2026\-10\-13 by the
  M0122\-0009 summarizer design review\)\. `vacuum\.go` \~:368 truncates through `bufpool\.go` \~:1433, but the
  `LogSmgrTruncateTo` hook is only set in tests\. The comment calling a missing hook "a no\-op" is wrong: the file is
  still truncated, just unlogged\.
  - PG logs XLOG\_SMGR\_TRUNCATE before truncating \(`RelationTruncate`, `catalog/storage\.c`\), so a standby
    truncates too\. The WAL summarizer also needs it to set the limit block\.
  Kind: bug
  Parent: M0122\-0009
  - Rank: below item 10 \(S7 carve\-out\) unless the owner places it\. Parity, not corruption: the extra tail blocks
    on a replica are past the relation\'s end\.
  - First step: wire `LogSmgrTruncateTo` in the production pool setup \(`internal/initdb/open\.go`\) and pin it with
    a recovery test\.
- [ ] **In\-process WAL writer timeline switch at promotion, then XLOG\_END\_OF\_RECOVERY** \(filed 2026\-10\-14 at the
  M0122\-0009 close\)\.
  Kind: impl
  Parent: none
  - Today promotion \(`cmd/goopg/standby\.go` `finalizePromotion`, `promoteAfterRecovery`\) bumps the timeline in
    pg\_control and the history file but leaves the running WAL writer on the old TLI until restart\. PG switches at
    once: the new TLI\'s first segment is a copy of the old one up to the switch point \(`XLogInitNewTimeline`\), and
    `CreateEndOfRecoveryRecord` writes `XLOG\_END\_OF\_RECOVERY` as its first record\.
  - Why it matters: the WAL summarizer\'s first range on the new TLI needs that boundary \(design
    `0122\-0009\-wal\-summarizer\.md` §4\.4 item 7\); WAL written after promotion carries the wrong TLI until restart\.
  - First step: a `Writer` timeline switch \(close the current segment, copy it to the new TLI\'s name up to the switch
    LSN, continue there\), then emit the record\.
  - Expected instrument: a promotion test reading the new TLI\'s segment with `pg\_waldump`\.
- [ ] **Visibility\-map changes as XLOG\_HEAP2\_VISIBLE** \(filed 2026\-10\-14 at the M0122\-0009 close\)\.
  Kind: impl
  Parent: none
  - goopg logs every VM change as a native rmgr\-128 `RecordKindHeapVisible` record with no block reference and no
    tablespace \(design §4\.4 item 2\)\. A PG standby cannot replay rmgr 128\.
  - PG logs a set as `XLOG\_HEAP2\_VISIBLE` \(block 0 = VM page, block 1 = heap page\); clears ride the heap records\'
    `\*\_ALL\_VISIBLE\_CLEARED` flags\.
  - First step: emit sets as `XLOG\_HEAP2\_VISIBLE` through the assembler and route goopg replay to the existing decoded
    arm; keep a length\-versioned decode of the native record for old WAL\.
  - Expected instrument: `pg\_waldump` decoding goopg\'s VACUUM WAL; the walsummarizer VM checks stay green\.
- [ ] **XLogInsert / XLogFlush rename** \(backlog entry, M0042\-0003; filed 2026\-10\-14 at the M0122\-0009 close\)\.
  Kind: impl
  Parent: none
  - Placement: owner decision\. About 234 call sites across 20 files, with no behaviour change\. goopg\'s
    `Writer\.Append\(payload\)` takes a pre\-encoded record; it is not PG\'s `XLogBeginInsert` / `XLogRegisterData` /
    `XLogInsert` protocol, so a rename alone would mislabel it\. Recommendation: close as won\'t\-do\.

- [x] **M0122-0010 — Concurrency: buffer pool & btree locking**.
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0122\-0010a — B\-tree concurrency residuals against PG's nbtree protocol** \(filed 2026\-10\-15 when
  M0122\-0010 closed\)\. None loses data today; each is a fidelity gap in how goopg's lock\-coupled protocol differs
  from upstream\.
  - Fast root: the metapage's fast root always equals the true root; PG keeps `btm\_fastroot` below a chain of
    single\-page levels \(`\_bt\_getroot`, `\_bt\_insertonpg`'s split\_only\_page, INSERT\_META\)\.
  - Free list per handle: pages one handle's vacuum frees are reused only by that handle; PG records them in the FSM,
    shared by every backend \(`\_bt\_allocbuf`, `RecordFreeIndexPage`\)\.
  - Deletion latch scope: goopg holds parent \+ left \+ target \+ right for one page; PG's two phases hold target \+
    parent \(`\_bt\_mark\_page\_halfdead`\), then left \+ target \+ right \(`\_bt\_unlink\_halfdead\_page`\)\.
  - `\_bt\_rightsib\_halfdeadflag` is not ported \(goopg skips dead runs instead\)\.
  - The split path releases the right half before the parent search; PG's `\_bt\_insert\_parent` releases `rbuf` only
    after `\_bt\_getstackbuf` returns\.
  Kind: impl
  Parent: M0122\-0010
  - Rank: below item 10 under the S7 carve\-out \(correctness/fidelity, no plan instrument\) unless the owner places it\.
  - First step: the free list → FSM move \(the only residual with an observable cost: index bloat across backends\)\.
- [ ] **M0122-0012 — Perf infra: vectorization / slot-pipeline / harness**.
  - **First batch\-filter slice LANDED 2026\-09\-22 \(loop \#11\)** as
    `d6e42a7f7`, **as infrastructure with NO performance claim**.
    Movement: none. Design:
    `docs/design/0100-0149/0122-0012-vector-filter-pipeline-recon.md`.
    - **Reachability, measured**: a temporary `FILTEROPCENSUS` counter over the
      whole TPC\-DS SF0.25 sweep recorded **363** `filterOp` opens — 292
      `BinaryOp` shapes the admission rule declines, 66 `BooleanConst`, and
      **5** on the batch path \(**1.4%**\). Neither corpus's plan capture holds a
      standalone `Filter` operator node at all: a `Filter` above a scan is
      absorbed into `seqScanOp`, so every open sits above a join or another
      non\-scan child.
    - **Speed, measured**: A/B on the served shape \(400k\-row join, filter
      above it, same binary, batch toggled by a temporary env switch\) gives
      medians **219 ms ON vs 217 ms OFF** — inside the noise. Rule \#4's case
      exactly; the doc says in as many words not to cite this as a speedup.
    - **Semantics, measured**: NULL three\-valued filtering agrees three ways —
      batch ON, batch OFF and PG 18.3 all give `1/30`, `1`, `0` on the same
      data. That is the sibling\-path check a second evaluator owes.
    - The slice's worth is the ownership boundary the scan\-resident successor
      needs \(`TestM0122VectorBatchRetainsReusedSlotBeforeNext` against a child
      that reuses its output buffer, plus the admission pin\). The performance
      case belongs to that successor, where the 292 declined opens and the
      absorbed scan quals live.
    - Gates: executor units; tpch\-spotcheck Q12=2/Q13=33; tpcds\-sf025
      `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`; acceptance arm
      24 MATCH; full `TestPort_RegressSuite` with an unchanged failing set;
      pgbench smoke.
    - No ledger row: performance infrastructure, not a PostgreSQL semantic
      omission \(the doc states this\).
  - **Scan\-qual hot\-path guards LANDED 2026\-10\-16 \(loop \#36\)**.
    Movement: none — perf only. The S3 instrument is value/plan identity:
    sf025 `PASS=99 PLAN-SHAPE same=99`, fire\-set `fires=none` \(SF0.25 84,
    SF1 73\), and ea\-ratchet 1→1. Same design doc, "Slice 2".
    - Profile of a filtered SF1 `store_sales` scan: `evalQual` ~44%,
      `decodeRowRangeInfo` ~41%. The `evalFastExpr` BinaryOp arm now tests the
      concat/bpchar helpers' own preconditions before calling them; the numeric
      special\-spelling check gets an exact ASCII first\-byte gate
      \(`TestNumericSpecialSpellingGateIsExact`\).
    - Measured: the scan went from ~1970 ms to ~1590 ms. TPC\-H acceptance arm:
      24 MATCH, 40.28 s → 39.73 s.
    - `unimplemented_feat.json` M0076\-0002 \(filterOp batch wiring\)
      re\-verified and marked resolved by `d6e42a7f7`. The SeqScanOp batch
      entry stays open; it is the next M0122\-0012 slice and targets
      `decodeRowRangeInfo`.
  - **By\-value integer deform arm LANDED 2026\-10\-16 \(loop \#37\)**.
    Movement: none — perf only. The S3 instrument is value/plan identity:
    sf025 `PASS=99 PLAN-SHAPE same=99`, fire\-set `fires=none`, ea\-ratchet
    PASS, and full `TestPort_RegressSuite` PASS. Same design doc, "Slice 3".
    - `perf annotate`: 47.8% of `decodeRowRangeInfo` sat on the reload of the
      stack\-returned 48\-byte `Datum`, a store\-forwarding stall.
      `colTypeInfo.intWidth` now lets int2/int4/int8 decode in place \(PG
      `fetch_att`\), pinned by `TestDeformIntFastArmMatchesDecoder`.
    - Measured: the filtered SF1 `store_sales` scan went from ~1590 ms to
      ~1250 ms \(−21%\). TPC\-H arm: values identical, 39.73 s → 38.91 s.
    - Ruled out: an `OutputStyle`\-by\-pointer variant measured ~1%, inside
      the noise, and was not kept.
    - Next lever: the same stall on non\-integer columns \(numeric\) in the
      generic decoder.
  - **In\-place numeric deform arm LANDED 2026\-10\-16 \(loop \#38\)**.
    Movement: none — perf only. The S3 instrument is value/plan identity:
    sf025 `PASS=99 PLAN-SHAPE same=99`, fire\-set `fires=none`, ea\-ratchet
    PASS, and full `TestPort_RegressSuite` PASS. Same design doc, "Slice 4".
    - `colTypeInfo.isNumeric`: `decodeRowRangeInfo` calls
      `decodePhysicalPGVarlena` \+ `NumericInt64FromStoredPayload` directly and
      writes in place. Every declined payload takes the generic arm unchanged.
      Pinned by `TestDeformNumericFastArmMatchesDecoder` \(mutation\-checked\).
    - Measured: the SF1 filtered scan went from ~1250 ms to ~1148 ms \(−8%\).
      TPC\-H arm: values identical, 38.91 s → 36.56 s \(Q1 4.71 → 3.89 s\).
    - Next lever: expression evaluation — `evalFastExpr` 27.5%, `evalBinary`
      8%, `compareDatum` 6.5%.
- [ ] **M0122-0013 — Physical/streaming replication & standby**.
  - **Standby recovery\-checkpoint mode LANDED 2026\-09\-22 \(loop \#12\)** as
    `e646499b0`. Movement: none — recovery path; `PLAN-SHAPE same=99 changed=0`.
    Design: `docs/design/0000-0049/0005-0001-streaming-replication-architecture.md`.
    - The bug: a physical standby ran the ORDINARY checkpoint path, appending a
      checkpoint record to WAL and advancing the local tail — the very position
      the walreceiver resumes `START_REPLICATION` from, and one the primary
      owns. The next request then pointed past \(or into\) the primary's stream.
    - The fix: a recovery mode set at startup, cleared at promotion, in which
      `runRecoveryFlush` does the durability half \(buffers, CLOG, data\-file
      sync, same ordering\) and writes NO WAL.
    - **The pin is one test, and it is named**:
      `TestE2E_PhysicalReplicationCatchesUpAfterStandbyCrash` FAILS with the
      mode neutralised \(35 s catch\-up timeout\). The other three new E2Es
      \(primary restart, promotion across restart, promotion after primary
      crash\) pass either way — coverage, not the pin. All run on
      `replcluster`'s durable `SyncInit: true` config.
    - **A logged note became an assertion.**
      `TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart` previously only
      LOGGED whether a restarted standby resumed streaming, on the grounds that
      "the v0 harness does not re-establish the walreceiver stream after a
      standby restart". That note WAS this bug; the test now fails if the
      standby stays stale.
    - **Ledgered gap**: PG's `CreateRestartPoint` also advances `pg_control`
      \(`checkPoint`, `checkPointCopy`, `minRecoveryPoint`\) from the last
      REPLAYED checkpoint record. goopg's recovery flush leaves `pg_control`
      untouched — safe, but a restarted standby replays from an older position
      than PG would. Resume point in the ledger row.
    - Gates: xlog units + `-race`; initdb units; four replication E2Es plus the
      attach roundtrip; tpch\-spotcheck Q12=2/Q13=33; tpcds\-sf025 `PASS=96
      MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`; pgbench smoke.
- [ ] **M0122-0014 — Logical replication / decoding / subscription**.
  - **MESSAGE wire half LANDED 2026\-09\-22 \(loop \#13\)** as `8170a6f29`.
    Movement: none — replication wire format.
    Design: `docs/design/0100-0149/0122-0014-pgoutput-message-wire.md`.
    - The decoder REJECTED pgoutput's `M` kind, so a goopg subscriber could not
      consume an otherwise valid upstream stream once the publisher enabled
      messages; the publisher had no frame API at all.
    - **Verified against the oracle, not just the source.** PG 18.3 on a
      private `wal_level=logical` cluster emits
      `4d01000000000206b200676f6f707265666978000000000b68656c6c6f2d676f6f7067`
      for `pg_logical_emit_message(true,'gooprefix','hello-goopg')` read back
      through `pg_logical_slot_peek_binary_changes(… 'messages','true')`, and
      `PgOutput.Message` reproduces that byte string exactly.
    - The apply worker treats a decoded MESSAGE as a **no\-op**: a plugin
      channel must not abort the surrounding subscriber transaction.
    - **Forward\-compat boundary recorded in code**: protocol v2 inserts a
      4\-byte xid between `M` and the flags byte while streaming. goopg
      requests `proto_version 1` only, so the v1 layout is right for what it
      negotiates — but a v2 negotiation must EXTEND the arm, never inherit it.
    - **Still open \(ledgered\)**: SQL\-level `pg_logical_emit_message`. It
      needs `RM_LOGICALMSG` records created, classified and delivered in
      transactional order, whose blocker is `ReorderBuffer.Commit`'s
      homogeneous `[]Change` queue — draft design
      `0122-0014-logical-message-producer.md`.
    - Gates: xlog + executor units; `-race` on xlog; the full
      `TestPort_PgoutputInterop` suite against real PG; tpch\-spotcheck
      Q12=2/Q13=33; tpcds\-sf025 `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`; acceptance arm 24 MATCH; pgbench smoke.
- [ ] **M0122-0015 — Test-suite porting: amcheck / verify_heapam / pg_dump**.
  - **RE-OPENED 2026-10-12 evening (owner, delegated):** `[!]`→`[ ]` plus
    LINEAGE-BASELINE pin of 0015b/c/d/e/g in the banner. Real child ids
    make the pin safe; `regress` is now a Movement instrument (0015g
    itself moved foreign_data 2459→2428), so future members can write
    honest `Movement: yes`.
  - **ESCALATION 2026\-10\-12 \(lineage budget, Rule A\)\.** The last five
    completed children \(0015b subq\_op OPERATOR, 0015c PREPARE types, 0015d
    pg\_proc per database, 0015e FDW per database, 0015g
    postgresql\_fdw\_validator\) all carry `Movement: none`, so the root is
    parked `[!]` until the owner re\-opens it \(`LINEAGE\-BASELINE:
    M0122\-0015 ...`\)\.
    - What was attempted: making pg\_dump / pg\_restore work against goopg as
      the 005\_pg\_dump\_filterfile port demanded — grammar, PREPARE, catalog
      scoping and FDW option validation, each verified against PG 18\.3\.
    - What each step proved: client\-tool parity has no TPC instrument;
      `Movement: none` is structural\.
    - Remaining blocker: the lineage rule\. Owner call: re\-baseline or close\.
    - Expected movement if unblocked: none on match / CATEGORIES /
      ea\-ratchet; gains show in pg\_dump ports and regress foreign\_data\.
    - Remaining size — open children already filed: 0015f \(`= ANY
      \(non\-array\)` accepted\), 0015a \(COPY in non\-default databases; also
      unblocks the 23 unported 005 assertions\)\. **Not filed**
      \(guard\-refused; in the 2026\-10\-12 ledger rows\): user\-defined FDW
      validators are never invoked; ALTER SERVER / ALTER USER MAPPING OPTIONS
      are unimplemented; a WARNING preceding an ERROR in the same statement is
      lost; CREATE FOREIGN TABLE reports the command tag `CREATE TABLE`;
      foreign\-catalog heaps are single\-database across restarts\.
  - [ ] **M0122-0015f — `x = ANY \(non\-array\)` is accepted**
    Kind: impl
    Parent: M0122-0015
    Found 2026\-10\-12 while testing M0122\-0015b\.
    - **Measured**: `SELECT 1 = ANY \(1\)` returns `t`; PG raises 42809 `op
      ANY/ALL \(array\) requires array on right side` \(parse\_oper\.c
      make\_scalar\_array\_op\)\. Same for the OPERATOR\(\.\.\.\) spelling\.
    - First step: reject a non\-array right operand in the analyzer\'s
      ANY/ALL typing\.
  - [ ] **M0122-0015a — `COPY` resolves relations in the DEFAULT database, so
    `pg_dump` of any user-created database has no data**
    Kind: impl
    Parent: M0122-0015
    Discovered 2026\-09\-22 (loop \#21) while porting `010_dump_connstr.pl`.
    - **Measured**: inside any `CREATE DATABASE`\-created database,
      `COPY t1 TO stdout` raises `relation "t1" does not exist` while
      `SELECT count(*) FROM t1` in the SAME session returns rows. Both the
      unqualified and the `public.`\-qualified spellings fail, so it is not
      schema resolution. A plainly\-named `plaindb` reproduces it, so it is
      not the exotic name the port was exercising.
    - **Consequence**: `pg_dump <userdb>` emits schema and then dies at the
      first `COPY public.<t> TO stdout`. Every existing pg\_dump port in the
      package dumps the `postgres` database, which is why nothing caught it.
    - **Root cause**: `internal/postmaster/copy.go` \(~:150\-195\) builds the
      standalone `COPY ... TO STDOUT` path's `executor.Context` BY HAND and
      never assigns `CurrentDatabase`/`CurrentDatabaseOid`; catalog lookups
      then fall back to the default database. The normal query path sets it at
      `internal/postmaster/dispatch.go:3238`.
    - **Third occurrence of the same shape** — the identical hand\-built
      context was caught missing session GUC hooks \(M0119\-0006\) and session
      identity \(M0134\-0009 R5\). Both were patched field\-by\-field; the
      durable fix is to construct it from the same path the batch dispatch
      uses. Sibling audit belongs in that change, not a fourth patch.
    - ~~**Blocked from landing** by the `commit-msg` fire\-set `set -e` abort
      \(`maintenance_prompts/commit-msg-fireset-arm-owner-action.md`\):
      `internal/postmaster/` is outside the fire\-set scope, so the fix cannot
      be committed until the owner repairs the hook.~~ **Hook repaired
      2026\-09\-22 — this task is committable again.** The port and the ledger
      rows landed; the fix itself is still open and is the durable\-fix
      sibling\-audit described above.
  - [!] **BLOCKED — OWNER ACTION NEEDED \(2026\-09\-22, loop \#17\)**. The
    foreign\-table durability work below is complete, fully gated and
    **staged**, but `git commit` cannot land it: the `commit-msg` hook exits
    **1 with no message** for any commit that stages a non\-test `.go` file
    OUTSIDE the fire\-set scope.
    - Cause \(traced with `bash -x .githooks/commit-msg`\): the hook runs under
      `set -euo pipefail`, and its fire\-set arm is
      `printf … | scripts/fireset-scope.sh --stdin >/dev/null 2>&1` followed by
      `fireset_rc=$?`. `fireset-scope.sh` returns **1** for "out of scope" —
      the COMMON case — so `set -e` ends the hook at that line and
      `fireset_rc` is never read; the `case` arms below it are unreachable for
      rc 1.
    - Why it surfaced only now: M0145\-0021a \(`1181baf79`\) added the arm this
      week, and every commit since has been either docs\-only \(`gated_go=0`\)
      or under `internal/optimizer` \(rc 0\). This commit —
      `internal/executor` + `internal/initdb` — is the first to take the rc\-1
      path.
    - Suggested one\-line repair, semantics preserved \(the fail\-closed rc>=2
      arm untouched\): initialise `fireset_rc=0` and append `|| fireset_rc=$?`
      to the pipeline.
    - **The loop did not apply it**: `.githooks/` is harness surface and the
      edit was correctly refused for an agent editing its own gate. Escalated
      per the "if a gate cannot run, mark `[!]` and escalate" rule. Re\-checked
      loop \#18 — the arm is still unrepaired, and the guard names the file
      owner\-only explicitly \(`H4 … harness mechanism file … owner-only`\), so
      the loop will keep escalating rather than retrying.
    - Standalone reproduction \(loop \#18, touches no harness file\):
      `set -euo pipefail` \+ a function returning 1 in place of
      `fireset-scope.sh`, then `rc=$?` — prints the line BEFORE the pipeline
      and exits 1 without ever reaching the `rc=$?` read. Confirms the abort
      is `set -e` on the pipeline, not anything inside `fireset-scope.sh`
      \(which behaves correctly: rc 1 = out of scope\).
    - **Blast radius is repo\-wide, not loop\-specific**: the arm runs before
      the `RALPH_LOOP` gate, so this blocks the OWNER's commits too, for any
      commit staging a non\-test `.go` file outside
      `internal/optimizer|planner` and `*cost*|*stat*|*selfuncs*` — i.e. all
      ordinary `internal/executor`, `internal/initdb`, `internal/catalog`,
      `internal/storage` work. Only docs\-only, test\-only and fire\-set\-scope
      commits can land today.
    - **Second\-order effect worth knowing before the repair lands**: gate
      stamps hash the STAGED tree, so while this deliverable sits in the
      index no OTHER task can be gated \(its stamp would cover these files
      too, or the tree/index mismatch would stamp FAIL\). The loop therefore
      cannot simply "work something else" without either unstaging this work
      or riding it into an unrelated commit — both worse than waiting.
    - State left for the owner: the eleven files are **staged** and the commit
      message is at `/tmp/ftmsg.txt`; `git commit -F /tmp/ftmsg.txt` lands it
      once the hook is repaired. Every gate stamp is PASS against exactly this
      staged tree.
  - **Foreign\-table catalog durability FIXED 2026\-09\-22 \(loop \#17\)** —
    the blocker the previous loop ledgered, which made `8deb60881`'s
    no\-handler refusal unreachable on any restarted cluster. Movement: none
    \(neither corpus has a foreign table\); design doc
    `0122-0015-foreign-table-catalog-durability.md`.
    - **The divergence**: after a clean stop/start, `pg_foreign_table` was
      empty, `pg_class.relkind` had degraded `'f'` -> `'r'`, and
      `SELECT * FROM t0` returned **zero rows** where PG 18.3 raises 55000.
      The servers survived \(B3.4 gave them a heap row\); the table's
      association with them did not.
    - Three causes: `buildUserPGClassRow` had no `'f'` arm \(the persisted row
      disagreed with the VIRTUAL pg\_class renderer, which always derived
      `'f'` from `ForeignServerName`\); the reload filter accepted only
      `{r,m,v,S}`; and `pg_foreign_table` was rendered purely virtually from a
      field with **no heap representation at all**.
    - Fix: a real `pg_foreign_table` \(3118\) heap row + 3119 index entry
      written through the single `syncTableToCatalogHeap` funnel, stamped on
      DROP/re\-sync via `deleteCatalogRowsForOID`, and reloaded LAST in
      `reloadForeignDataFromHeap` — after the servers whose OID it reverses
      and after the user tables it re\-attaches to. Both orderings are
      load\-bearing.
    - `TestEverySysBtreeInsertPathIndexHasSplitKeyMeta` caught the missing
      `keyMetaForSysBtree` entry for 3119, which would have worked until the
      leaf\-root filled and then failed the split. That guard did its job.
    - `TestPort_PgDump003ForeignDataNoHandler` now RESTARTS between the DDL
      and the dumps — upstream does not, but without it the test passed while
      the only state a real cluster ever has was broken. Non\-vacuity checked:
      removing the `'f'` reload arm fails exactly the new assertion.
    - **Ledgered, not fixed**: the four foreign\-data catalogs are still
      pinned to `DefaultDBOid` \(inherited B3.4 scope; `pg_foreign_table` is
      the first keyed by a per\-database relation OID, so the mismatch is now
      observable\), and `CREATE FOREIGN TABLE` still returns the tag
      `CREATE TABLE`.
    - Gates: initdb + catalog + executor units; both 003 ports; full
      `TestPort_RegressSuite` with an unchanged failing set \(only
      `partition_aggregate`\); tpch\-spotcheck Q12=2/Q13=33; tpcds\-sf025
      `PASS=96 … TIMEOUT=0`, `PLAN-SHAPE same=99 changed=0`; pgbench smoke.
      No optimizer/planner file staged, so G9 fire\-set and the acceptance arm
      do not apply.
  - **`003_pg_dump_with_server.pl` is now FULLY ported 2026\-09\-22 \(loop
    \#16\)**, and the second half needed a production fix first: `8deb60881`.
    Movement: none — a refusal on a relkind neither corpus contains.
    - **The divergence**: goopg accepted the FDW/server/foreign\-table DDL and
      modelled it correctly \(`relkind='f'`, a `pg_foreign_table` row,
      `fdwhandler=0`\), then let a `SELECT` return **zero rows**. PostgreSQL
      raises SQLSTATE **55000** `foreign-data wrapper "…" has no handler` from
      `GetFdwRoutineByServerId` \(`foreign.c:403`\). A user asking `pg_dump`
      for foreign data from an unusable server got an empty dump and exit 0.
    - Refused at **plan** time, where upstream refuses: `EXPLAIN` fails on PG
      and now here too. Fail\-open — only a wrapper positively known to have
      `fdwhandler = 0` can refuse.
    - **The lookup key was the real trap.** `CREATE SERVER` registers under
      `NamespaceDBOid(conn db)` while the catalog's `DBOID()` returns the real
      database OID — measured `DBOID()=5` against a registry key of
      `DefaultDBOid`, so the first version of the check silently declined on
      every default\-database server. It converts through `NamespaceDBOid` now,
      as the registrar does.
    - Non\-vacuity checked: neutralising the refusal fails
      `TestPort_PgDump003ForeignDataNoHandler`'s first assertion with
      "exited 0" — the exact prior behaviour.
    - **Two more divergences found and ledgered, not fixed**: foreign\-table
      metadata is NOT durable \(after a restart `pg_foreign_table` empties and
      `relkind` degrades `'f'` -> `'r'`, so the refusal cannot fire\), and
      `CREATE FOREIGN TABLE` returns the command tag `CREATE TABLE`.
    - Corrected this loop: the previous ledger row named
      `ERRCODE_FDW_UNABLE_TO_CREATE_EXECUTION`; the oracle says **55000
      ERRCODE\_OBJECT\_NOT\_IN\_PREREQUISITE\_STATE**.
    - Gates: optimizer + executor units; both 003 ports; tpch\-spotcheck
      Q12=2/Q13=33; tpcds\-sf025 `PASS=96 … TIMEOUT=0`; acceptance arm
      24 MATCH; **tpcds\-fireset two\-scale gate PASS** \(the new G9
      requirement — `introduced=none` at both scales\); full
      `TestPort_RegressSuite` with an unchanged failing set; pgbench smoke.
  - **`003_pg_dump_with_server.pl` first section PORTED 2026\-09\-22 \(loop
    \#15\)** as `TestPort_PgDump003NewlineInDatabaseName`. Movement: none —
    test porting; TAP coverage 46.2% -> 47.3% \(client\-tools\-tap\), total
    42.6% -> 42.8%.
    - Upstream's first section is a **security** property: a database name may
      contain a newline, and `pg_dumpall` interpolates a per\-database
      connection string into the script it emits. PostgreSQL's guard is
      `appendShellString` \(`src/fe_utils/string_utils.c`\), which refuses the
      argument. All three upstream assertions are ported — nonzero exit, the
      `shell command argument contains a newline` stderr, and **no `^attack`
      line**, which is the one that would catch a pre\-escaped name that
      satisfied the guard while still splitting the script.
    - What it proves about goopg \(the binary is upstream's\): goopg accepts
      `CREATE DATABASE "regress_<newline>attack"` and reports the name
      verbatim through `pg_database`, so the real pg_dumpall's protection
      engages against goopg exactly as against PostgreSQL.
    - **The second section is NOT ported, and the reason is a measured
      divergence** rather than an unported fixture. Against identical DDL,
      PostgreSQL raises `ERROR: foreign-data wrapper "dummy" has no handler`
      for a `SELECT` on a handler\-less foreign table while goopg returns zero
      rows, so upstream's `pg_dump --include-foreign-data` FAILURE assertion
      sees exit 0 here. goopg models the objects correctly \(`relkind='f'`, a
      `pg_foreign_table` row, `fdwhandler=0`\); the executor\-side handler
      lookup is what is missing. Ledgered with its resume point, plus the
      smaller `CREATE FOREIGN TABLE` command\-tag divergence found beside it.
  - **Inventory promotion 2026\-09\-22 \(loop \#14\): three rows were STALE, and
    the published coverage was understating reality.** Movement: none — no
    production code; the tests already existed and passed.
    - `003_check.pl`, `004_verify_heapam.pl` and `005_opclass_damage.pl` were
      all `status=not-tried, pass_required=no` while six ported tests exercise
      them and pass \(`TestPort_PgAmcheck003CombinedCorruption`,
      `…003MissingIndexFork`, `…003MissingHeapFile`, `…003SchemaScoped`,
      `…004VerifyHeapam`, `…005OpclassDamage`\). Verified by running them
      before touching the CSV.
    - Worse than a status typo: the generated coverage docs carried the
      auto\-default rationale **"Upstream binary pg\_amcheck is outside goopg's
      current server\-focused scope"** for files that have working ports — a
      statement the tests refute.
    - Promoted to `port` / `pass_required=yes` with rationales that name the
      Go tests AND state the subset boundary, following the convention
      AC\-001 / BB\-020 / RW\-001 / WD\-001 already use for tiered ports. The
      remainder \(unsupported index AMs, box/int4range/int4\[\] types, STORAGE
      EXTERNAL TOAST corruption, multi\-database orchestration, and 004's
      MVCC/TOAST tiers\) stays deferred under **AC\-003**, this suite's
      remainder tracker, whose rationale already enumerates it.
    - `make regen-testport` moves client\-tools\-tap coverage **42.9% ->
      46.2%** and the total **42.0% -> 42.6%**; TAP port count 47 -> 50.
      `make check-testport-inventory` passes.
    - **Scanned for the same defect elsewhere**: of 119 non\-port TAP file
      rows, only three cite a ported test — and the other two are CORRECT.
      `002_save_fullpage.pl` and `200_connstr.pl` have ports that currently
      **SKIP** \(the WAL native\-format work and its documented tag\), and a
      skipped test cannot be `pass_required=yes`. The third,
      `pg_dump/t/002_pg_dump.pl`, is only cited for its FIXTURES by
      `pgdump_connsetup_test.go` — the script itself is genuinely unported, so
      `not-tried` stands.

## M0131 — Bidirectional cluster-directory cold-start + real-PG system-view hosting (filed 2026-08-11)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


M0132 (Explicit transactions across the extended query protocol), M0133 (information_schema on disk).

## M0134 — regress-sql `failed`/`not-tried` test-case digestion (filed 2026-08-15)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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
  - [ ] **`CREATE FUNCTION f\(x int = 1\)` is a syntax error** \(found
    2026\-10\-10\)\. gram.y `func\_arg\_with\_default` accepts
    `func\_arg \'=\' a\_expr` as a synonym for DEFAULT; goopg\'s grammar has
    only DEFAULT \(window\.sql and polymorphism\.sql use the `=` spelling\)\.
    Kind: bug
    Parent: M0134-0022
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
- [ ] **M0134-0163d — `pg_policies` prints a Go value dump for a column** (filed
  2026-10-02, owner review of the 1002 progress report): rowsecurity.sql output
  shows a pointer-like value (`&{64 0x…}`) where PG prints the policy's text
  columns. The view exists since M0131-S9.3e bootstrapped `pg_policy` (0163b's
  "does not exist" is stale), so this is a render defect in the view's
  expression columns — same class as 0167d's `pg_get_indexdef` dump.
  Kind: impl
  Parent: M0134-0163
  - First step: `select * from pg_policies` on a throwaway cluster, find which
    column renders the Go value (likely a `pg_node_tree`/qual column needing
    deparsed text), then route it through the deparser like 0167d did.
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
## M0135 — SQL/JSON `jsonpath` subsystem (filed 2026-08-20, from M0134-0039/0040/0041 sizing)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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

## Archived — complete (see `completed_milestones/completed_fix_plan_014.md`)

M0138 (PG-faithful ANALYZE statistics (filed 2026-09-14)).

## Archived — complete (see `completed_milestones/completed_fix_plan_014.md`)

M0139 (Executor-side narrowing / projection pushdown (filed 2026-09-14)).

## M0140 — TPC-DS parallelism (filed 2026-09-14)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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

- [!] **M0140-0007 — `Parallel Hash` (`parallel_hash = true`): workers
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
  - **Scoping recon done (loop 2026-09-21 \#36), no production change.**
    Design doc:
    `docs/design/0100-0149/m0140-0007-parallel-hash-shared-build.md`
    (the task's first, indexed).
    - **The task's premise is understated.** `parallelBuildLazyHashTable`
      exists, but it is **leader-inserted** (producers only scan+filter
      and ship batches; the calling goroutine does every hash insert,
      running the serial build loop) and **scan-driven**
      (`coopDrivingScan` must find a `*SeqScan` through
      Filter/Project/hash-Join, else it refuses). It parallelises
      READING the build side; it does not consume a partial inner's
      per-worker output, which is what PG's `parallel_hash = true` does.
    - So "drive it from a partial-inner PATH" is a producer-model
      difference, not a call-site extension.
    - **Consequence — the work splits in two**, and which witnesses fall
      where is UNMEASURED: (i) an inner bottoming out in a `SeqScan`
      could drive the EXISTING builder to get PG's plan SHAPE with a
      different execution model — an honest increment if labelled as
      such; (ii) any other shape needs the real worker-inserted build +
      barrier.
    - **Next step is that measurement**, not code: per family-A witness
      (Q3 Q9 Q10 Q14 Q16 Q18 Q21), does the inner reach a `SeqScan` via
      `coopDrivingScan`?
    - **Test design is constrained by the hard constraint**: a partial
      build that misses inner rows silently drops matches — the same
      class as the 2026-09-21 parallel SEMI wrong answer, where values
      stayed plausible and only a parallel-vs-serial identity comparison
      saw it. The pin must be an IDENTITY test per witness shape.
  - **Step 1 MEASURED (loop 2026-09-21 \#37), no production change.**
    - **Finding 1 — no bucket (ii) exists on this corpus.** goopg's hash
      build side is a bare `Seq Scan` on ALL SEVEN witnesses, so
      `coopDrivingScan` reaches every one. The worker-inserted shared
      build + barrier port is **not required** for family-A parity.
    - **Finding 2 — three witnesses are not parallel-hash problems
      alone.** PG builds its `Parallel Hash` over a **Nested Loop**
      (Q9), a **Hash Join** (Q21) and a **Parallel Hash Join** (half of
      Q10) where goopg builds over a scan. Those differ from PG in join
      order / build-side choice too, so `parallel_hash = true` alone
      will not make them match — they must NOT be counted as family-A
      wins by this task.
    - **Revised scope for a first implementation: Q14 and Q16 ONLY** —
      the two witnesses where goopg and PG build over the SAME relation
      (`part`) with a bare scan on both sides.
    - Q3 and Q18 need their build-side relation checked against PG's
      first (goopg builds over `orders`, PG over `customer`).
    - Known, quantified deferral: driving the existing builder gives
      PG's plan SHAPE with a **leader-inserted** build, not PG's
      worker-inserted one. Label it as such; do not call it the port.
    - Method: both sides read from ONE fresh parallel-mode SF1 capture
      pair; the checked-in `bench/tpch/plans-pg/` fixtures were NOT used
      (stale + serial, ledger `parity-reference-fixtures-are-invalid`).
  - **Step 2 STOPPED before implementation (loop 2026-09-21 \#38), no
    production change — the capability already exists.**
    - `prebuildSharedHashJoins` -> `buildLazyHashTable` ->
      `parallelBuildLazyHashTable` already fires: instrumented on the
      SF1 clone over Q14+Q16, `COOPBUILD entered scanTable=part` — the
      build relation of BOTH witnesses. **One shared hash table is
      already built cooperatively by N producer goroutines splitting the
      build scan.**
    - So what still differs from PG is only (1) WHERE the build happens
      (PG inside the Gather from a partial inner behind a barrier; goopg
      before fan-out over a complete inner, then shared) and (2) the
      plan LABEL (`Parallel Hash` vs `Hash`). Both divide the inner SCAN
      across goroutines already.
    - **The planned implementation was DECLINED.** Filing
      `parallel_hash = true` with a COMPLETE inner would move the plan
      text toward PG WITHOUT changing execution — a label asserting a
      model the engine does not use, strictly worse than the current
      honest divergence.
    - **OWNER DECISION REQUIRED**: re-scope as an explicit fidelity item
      (buys plan text, not throughput, and should sit behind something
      that needs a partial inner for a real reason), or close —
      recording that the shared-build capability exists and that Q9/Q21/
      half-Q10 are join-order divergences this task could never fix.
    - If re-scoped, the partial-inner port is the real work and must be
      pinned by a parallel-vs-serial IDENTITY test per shape.
  - > ## ESCALATION 2026-09-23 (ralph2 loop \#6) — marked `[!]`, OWNER DECISION NEEDED
    >
    > The OWNER DECISION REQUIRED above \(2026\-09\-21, loop \#38\) was never
    > reflected in the task state, so every later loop re\-read this entry
    > as selectable. Nothing implementable remains without the owner:
    > goopg ALREADY builds one shared table cooperatively
    > \(`prebuildSharedHashJoins` → `parallelBuildLazyHashTable`, Q14/Q16\),
    > and the only change a loop could land alone — filing
    > `parallel_hash = true` over a COMPLETE inner — would print PG's label
    > over an execution model goopg does not use. Options: \(a\) re\-scope
    > as a fidelity item porting a genuinely partial inner \(build inside
    > the Gather, shared publication, build\-completion barrier, error
    > propagation\), pinned per shape by a parallel\-vs\-serial identity
    > test — expected movement: Q14/Q16 leave TPC\-H `parallelism`, not a
    > promised match; \(b\) close, recording the existing capability and
    > that Q9/Q21/half\-Q10 are join\-order divergences. The executor
    > capability inventory \(`m0145-0008-executor-capability-inventory.md`,
    > member 1\) confirms the shape is never generated today.
    >
    > **OWNER ANSWER 2026-09-23 \(delegated, scope M0137–M0145\): OPTION
    > \(a\) — re-scope as a fidelity item, re-filed as M0146-0002.**
    > Under the plan-parity goal "print PG's shape" IS the deliverable,
    > but only when the execution model actually is PG's — so the task
    > carries the genuinely-partial-inner port \(build inside the Gather,
    > shared publication, build\-completion barrier, error propagation\)
    > AND the label together, each pinned by per-shape parallel-vs-serial
    > identity tests. The escalation's caveat stands and is copied into
    > M0146-0002: Q9/Q21/half-Q10 are join-order divergences, not family-A
    > members — they are M0146-0005's territory. This entry stays `[!]`
    > as a pointer; the live work is M0146-0002.
  - **On completion — reconsider the blocked work (evaluate, do not
    auto-do):** re-run the family-A witnesses (Q3 Q9 Q10 Q14 Q16 Q18
    Q21) under the canonical parallel capture; M0137-0019a's family-A
    classification is re-evaluated only if a `Parallel Hash` node
    actually appears in those plans — if the shape exists and still
    loses on cost, THAT is the report, not a defect.

## M0141 — Upper-planner ordering contest (filed 2026-09-14)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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

- [x] **M0141-S2a-fix2r — re-apply S2a-fix2 (owner Q4: no reverts).**
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0141-S2b — GROUP_AGG rel publishes Pathlist, not Node, to the
  (completed sub-entries archived → `completed_milestones/completed_fix_plan_015.md`)
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
    - **Re\-checked 2026\-09\-24 \(ralph2 loop \#32\): witness gone.** Q67\'s
      WindowAgg input is `Sort \(i\_category, sumsales DESC\)` in both goopg\'s
      and PG\'s plans. Its remaining divergences are the top Incremental Sort
      \(the parked M0141\-S7 line\) and the grouping\-sets strategy. Kept open
      because the capability gap is real \(ledger row dated 2026\-09\-17,
      M0141\-S2b\-3\), but it has no corpus witness to gate on until one
      appears.
  - [ ] **M0141-S2b-4** — SETOP rel-identity fix. **UPDATE 2026-09-16 (S2b-1's
    own result section)**: DOES now have a witness — TPC-DS Q49's `Unique`
    (S7's mis-mapped "Unique x1"; `select ... union select ... union select
    ...`) is goopg's SETOP upper rel, not `SELECT DISTINCT`, so this is its
    real home. Still not scheduled ahead of 1-3 on that basis alone (single
    witness, same-sized surgery K24 already flagged as risky).
    - **Design slice 2026\-09\-24 \(ralph2 loop \#32\):**
      `docs/design/0100-0149/m0141-s2b-4-union-distinct-decomposition.md`.
      - Witnesses on the current tree: Q49 and Q75, the only two queries
        with a distinct UNION. goopg plans both as nested binary
        `HashSetOp Union`s. PG plans Q49 as `Unique → Sort → Append\(3\)` and
        Q75 as `Unique → Merge Append` over presorted children.
      - Root: no port of `plan_union_children`\'s flattening, and no sorted or
        Merge Append distinct candidate \(`generate_union_paths`,
        prepunion.c:676\).
      - Sliced into S2b\-4a \(flatten\), 4b \(election\), 4c \(Merge Append\),
        filed below. This parent closes when all three land.
  - [ ] **M0141-S2b-4e — presorted branch paths for the Merge Append arm**
    \(filed 2026\-09\-24 by S2b\-4c; **UNBLOCKED 2026\-09\-27 — the recorded
    blocker M0145\-0008 \(cutover\) has landed; the resume condition in the
    BLOCKED note is met**\). PG\'s `build\_setop\_child\_paths`
    offers each UNION child\'s cheapest path sorted on the union pathkeys,
    reusing an already\-sorted path \(index scan, Gather Merge over a partial
    path, Incremental Sort over a partially sorted one\). goopg sorts every
    branch\'s finished plan explicitly, so the merge candidate costs more
    than PG\'s. Witness TPC\-DS Q75 \(PG: Unique → Merge Append over Gather
    Merge children; goopg: hashed\). Design doc §"Not ported \(filed as
    S2b\-4e\)".
    - **BLOCKED 2026\-09\-24 \(ralph2 loop \#36\)** on M0145\-0008 \(cutover\)
      / ledger `c07\-single\-rel\-never\-reaches\-ordered\-index\-producer`.
      - Witness corrected: PG plans Q75\'s UNION as `HashAggregate → Gather
        → Parallel Append` at SF0.25 and SF1, not a Merge Append. goopg
        already has that structure; its remaining gap is the S2b\-4d label.
      - Built and not landed: re\-plan each simple branch with `ORDER BY
        1..n` \(scratch RTID scope\), use it when cheaper than the explicit
        Sort. Inert on the default arm: a single\-table ORDER BY never gets
        an ordered index path \(`Sort → Index Scan using item\_pkey` where
        PG plans `Index Only Scan`\). No plan moved \(union.sql, Q49/Q75,
        a large two\-table probe where PG also sorts\), so it would only add
        planning time.
      - Resume after M0145\-0008; witness: upstream union.sql tenk1
        enable\_hashagg=off cases \(PG: Merge Append over Index Only Scans\).
    Blocked\-by: M0145-0008
    Movement: none
    Kind: impl
    Parent: M0141-S2b-4
- [ ] **M0141-S3 — Partial-Sorted row emission** — a second Partial-mode code
  path (`Strategy = AggStrategySorted`) emitting real rows (group key + one
  serialized-state column per aggregate) instead of merging into
  `aggPartialAccum`; reuses `combineAggRuntime`'s existing rules; leaves the
  hash-strategy accumulator path untouched. **Entry gate: needs S1/S2's live
  measurement to show a TPC-DS-specific, genuinely parallel-shaped residual
  large enough to justify it.** **Adopted by M0146-0003 (owner disposition
  2026-09-23): this chain runs under M0146, gated on the M0145-0008 flip —
  do not select standalone before it.**
- [ ] **M0141-S4 — `aggRuntime` serialize/deserialize** — PG's
  `aggserialfn`/`aggdeserialfn`, bounded to the decomposable whitelist's actual
  pointer surface (`numericSum`, `intSx`/`intSxx *big.Int`,
  `numericSx`/`numericSxx *big.Rat`, `userState`, the float/bool/count/sum
  scalars — `DISTINCT`/`WITHIN GROUP`/`array_agg`/`string_agg` are already
  refused by `AggregateIsDecomposable`). Needs S3 (defines the transport
  shape). Runs under M0146-0003, gated on the M0145-0008 flip.
- [ ] **M0141-S5 — GatherMerge-fed Finalize-Sorted** — a new merge-combine
  executor operator consuming the key-interleaved stream `GatherMerge`
  produces from S3/S4's rows, folding same-key runs across workers via
  `combineAggRuntime` before finalizing (today's Finalize path only handles
  "drain the Gather to EOF, read the whole accumulator", which is wrong for a
  merge-ordered stream where a key can recur). Needs S3+S4. Runs under
  M0146-0003, gated on the M0145-0008 flip.
- [ ] **M0141-S6 — wire and measure** — pick which of the two existing
  producers (`parallel.go`'s legacy pass, or `partialaggupper.go`'s costed-path
  version — note its own unresolved 7/22-vs-12/22 regression against the
  legacy pass, out of scope to fix here) hosts the new shape; fix the
  `Aggregate`/`GroupAggregate (N keys)` EXPLAIN mislabel (doc
  `parallel-query/06` §4.1 — both currently render as a hash aggregate
  regardless of `Strategy`); re-measure the full corpus. Needs S5. Runs
  under M0146-0003, gated on the M0145-0008 flip.
- [x] **M0141-S7 — re-adjudicate and implement Incremental Sort** — **verified
  (completed sub-entries archived → `completed_milestones/completed_fix_plan_015.md`)
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
## M0142 — Join-order costing (filed 2026-09-14)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


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
  - **OWNER DISPOSITION 2026-09-23 (delegated, scope M0137–M0145):**
    stays `[!]`, with the two open threads split by scope. (1) The
    per-worker Memoize / `partialPathDrivingKind` resume — planner +
    executor work — is adopted as **M0146-0004** (M0142-0005b remains
    its filed child; M0146-0004 carries the scoping pass this entry
    requires). (2) The `indexProbeCostMultiplier` honest exit is a
    `:65433` reference-cluster rebuild in PG's ctid order (R1) — a
    cluster-lifecycle action that is NOT inside the delegated scope and
    stays with the real owner. M0142-0005g likewise stays frozen.
- [x] **M0142-0008a — scoping recon: size wiring SEMI/ANTI decorrelation
  (completed sub-entries archived → `completed_milestones/completed_fix_plan_015.md`)
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
  picked up. Deferred for the same reason as 3c — **and re-verified
  post-producer 2026-10-13 (owner note): reachability is still ZERO;
  the residual blocker is the `leaf-count`/`lateral` class owned by the
  M0145-0003 line. Parked, not selectable.**
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
  **Post-producer note 2026-10-13 (owner): producer `[x]` landed and
  reachability re-measured ZERO — residual blocker is the leaf-count /
  lateral class owned by the M0145-0003 line; parked, not selectable.**
## Archived — complete (see `completed_milestones/completed_fix_plan_014.md`)

M0143 (Engine correctness carry-overs from the parity programme (filed 2026-09-14)).

## Archived — complete (see `completed_milestones/completed_fix_plan_014.md`)

M0144 (Measurement-first parity: censuses, instrumented PG, route-order alignment (filed 2026-09-20)).
- [x] **M0144-0002 — first-divergence census** (03-forward-plan §1).
  (full entry archived → `completed_milestones/completed_fix_plan_014.md`)
- [x] **M0144-0011 — first vertical-slice campaign** (03-forward-plan §4).
  (full entry archived → `completed_milestones/completed_fix_plan_014.md`)
- [x] **M0144-0011c — `Materialize` node existence** (filed by
  (full entry archived → `completed_milestones/completed_fix_plan_014.md`)

## Archived — complete (see `completed_milestones/completed_fix_plan_014.md`)

M0145 (Jointree-first planner flow (filed 2026-09-20, owner decision)).

## M0146 — Post-cutover plan parity (filed 2026-09-23, owner decision)

_(completed entries archived → `completed_milestones/completed_fix_plan_015.md`)_


**Milestone doc:** `docs/milestones/0146-post-cutover-plan-parity.md`.
(The M0145-0008 flip landed 2026-09-24 — the original "unselectable until
the flip" gate is discharged; this milestone is the live item-3 burn-down.
Sequencing: the S9 replenishment rule in the banner.)

- [x] **M0146-0005 — join-order / candidate-pool divergence burn-down**
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146-0007 — `inline_cte` single-reference CTE inlining**
  (impl). Port PG's `inline_cte` (prepjointree.c): a single-ref,
  non-recursive CTE is inlined into the jointree so its quals and
  statistics reach the search — divergence class D6. Operates on the
  jointree IR, so it is jointree-arm work by construction; check
  interaction with `pushQualsThroughSingleRefCTEs` (overlapping but
  shallower) and fold or keep per measurement.
  Kind: impl
  Parent: none
  - Design doc `docs/design/0100\-0149/m0146\-0007\-inline\-cte.md`.
  - Slice 1 landed 2026\-09\-26 as M0146\-0007a \(below\); 0007b and the
    ledgered items remain.
  - Slice 6 landed 2026\-10\-04 as M0146\-0007f \(multi\-reference
    `NOT MATERIALIZED`\); the open items are the 2026\-10\-04 ledger row\.
  - Slice 7 landed 2026\-10\-04 as M0146\-0007g \(row marks and WITH
    queries\)\.
  - Slice 8 landed 2026\-10\-04 as M0146\-0007h \(pseudoconstant function
    quals gate their scope\)\.
  - Slice 9 landed 2026\-10\-04 as M0146\-0007i \(nested pull\-up\)\.
  > ## ESCALATION 2026\-10\-04 \(S4 lineage budget\) — M0146\-0007 held \[\!\]
  > The five most recent completed descendants \(0007f, 0057, 0007g, 0007h,
  > 0007i\) all carry `Movement: none`\. Each one was a real PG\-fidelity
  > port with a regress witness:
  > - 0007f: NOT MATERIALIZED inlined per reference;
  > - 0057: a wrong\-results fix;
  > - 0007g: row marks and WITH queries;
  > - 0007h: pseudoconstant function gating;
  > - 0007i: nested pull\-up\.
  >
  > None can move an S3 instrument\. TPC\-DS and TPC\-H write no NOT
  > MATERIALIZED CTE, lock no rows, carry no function\-only pseudoconstant,
  > and nest no simple subquery, so the fire set was flat for all five\.
  > - **What each step proved:** the inline\_cte gate, its reference\-site
  >   planning and its row\-mark rules now follow PG on every regress
  >   witness found; the D6\-cte census record count is 0 at both scales\.
  > - **Blocker:** no remaining residue has a TPC\-DS or TPC\-H witness\.
  >   The open items are the 2026\-10\-04 ledger rows \(0007f–0007i\): star
  >   targets in pulled bodies, alias\-list pull\-up, correlated per\-reference
  >   bodies, constant\-FALSE dummy rels, immutable folding, and others\.
  > - **Expected movement if reopened:** regress `subselect` and `with` plan
  >   text only; no TPC\-DS category\.
  > - **Size:** each item is a one\-loop slice\.
  >
  > Owner: reopen \(or re\-pin a LINEAGE\-BASELINE\) to continue regress
  > fidelity here, or leave it held\.

  **RESOLVED 2026-10-06 (owner, delegated): re-opened `[!]`→`[ ]`, no pin
  needed.** Two independent grounds. (1) Under the 2026-10-03 `\-`
  normalisation the root's mechanical last-5 is NOT exhausted — the
  window holds M0146-0021 (Parent: M0146-0007, a nonstandard non-`none`
  Movement value), so S4 never blocked filings here mechanically.
  (2) The hold's premise — "no remaining residue has a TPC-DS or TPC-H
  witness" — is voided by M0146-0065 (TPC-DS Q2's inlined-CTE UNION ALL
  appendrel at both scales), which stays filed under this root. The
  ledgered regress-fidelity residue (0007f–0007i rows) resumes under
  normal item-3 order.
  **Superseded 2026-10-13 (S7 carve-out):** the remaining residue is
  regress-fidelity only — "expected movement: regress `subselect`/`with`
  plan text only; no TPC-DS category" — so open residue under this root
  ranks below item 10. Select a residue slice only when a corpus query or
  named regress case calls for it; M0146-0065 (the Q2 TPC witness) is
  already `[x]`.
  > ## ESCALATION 2026\-10\-10 \(S4 lineage budget\) — M0146\-0007 held \[\!\] again
  > The five most recent completed descendants \(0119, 0123, 0124, 0129, 0145\) all carry `Movement: none`\.
  > M0146\-0145 \(this loop, 04e2eee04\) spent the last slot\.
  > - **What was tried / proved:** each was a regress\-witnessed PG\-fidelity port of inline\_cte / pull\-up behaviour\.
  >   0145 makes a pulled body\'s `\*` over a shared/MATERIALIZED CTE or the recursive worktable expand to the CTE\'s
  >   planned columns, so regress subselect\'s recursive `z` filters the WorkTable Scan as PG \(−2 diff lines\)\.
  > - **Blocker:** none of the remaining residue has a TPC\-DS or TPC\-H witness; the fire set was flat for all five, so
  >   no S3 instrument can move\.
  > - **Remaining residue** \(ledger rows 2026\-10\-04 for 0007f\-0007i, 2026\-10\-10 for 0145\): column\-alias lists on a
  >   reference in the star pass; correlated per\-reference bodies; non\-recursive members of a WITH RECURSIVE list;
  >   var\-free join quals after pull\-up as One\-Time Filters; join\-operand derived items inside a pulled body; EC member
  >   order in pulled join clauses; LEFT JOIN qual placement in pulled bodies\.
  > - **Expected movement if reopened:** regress `subselect` / `with` / `join` plan text only; no TPC\-DS or TPC\-H
  >   category\.
  > - **Size:** one loop per residue item\.
  >
  > Owner: reopen \(or re\-pin a LINEAGE\-BASELINE\) to continue regress fidelity here, or leave it held\.

  **RESOLVED 2026-10-12 (owner, delegated — second hold): re-opened
  `[!]`→`[ ]` plus a LINEAGE-BASELINE pin of 0119/0123/0124/0129/0145.**
  Same instrument-artefact class as the 10-06 reopen — regress-witnessed
  fidelity slices whose only instrument is now a recognised one
  (`regress`, added today). Standing note for the next spend: if the
  post-pin five are again all regress-only `none`, the family yields to
  lower items rather than re-pinning a third time.

- [x] **M0146-0009 — statistics/cardinality burn-down** (impl). Work
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0146-0010 — `Materialize` node** (impl; M0144-0011c's sizing is
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [!] **M0146\-0010a — retire the legacy bare\-inner replay wrap** \(filed
  2026\-09\-28 by M0146\-0010\)\. PG re\-executes a bare NL inner; goopg still
  caches it unless `GOOPG\_NL\_BARE\_REEXEC=1`\. Flip the default once a
  unique\-ified CTE semi\-inner can lead the join \(M0142\-0008c\-2\) — TPC\-DS
  Q14 statement 2 goes 11 s → \>300 s under strict re\-exec until then\.
  Kind: impl
  Parent: M0146-0010
  - First step: re\-time Q14 stmt2 with `GOOPG\_NL\_BARE\_REEXEC=1` after
    M0142\-0008c\-2 lands; if PG\'s `unique\(cross\_items\)` lead is elected,
    flip the default and run the fire set at both scales\.
  - **Re\-timed 2026\-10\-05, held \[\!\].** M0142\-0008c\-2 has landed, but
    the strict arm still breaks the SF0\.25 sweep\. Design doc
    `m0146\-0010\-materialize\-node\.md` §7; evidence
    `analysis/m0146/m0146\-0010ab/`\.
    - Q4, Q11 and Q14 time out \(15/11/39 s → 300 s\)\.
    - Q74 goes 32 → 157 s, and Q5, Q7 and Q31 run 2–3× slower\.
    - Cause in Q4/Q11: a chain of Nested Loops over filtered `CTE Scan on
      year\_total` inners, estimated at 9–29 rows\. The compat cache holds
      each filtered inner once; strict re\-execution reads 1\.19M CTE rows
      per outer row\. PG hash\-joins them\.
  > ## ESCALATION 2026\-10\-05 — M0146\-0010a held on the year\_total CTE join estimates
  > Unblock when the Q4/Q11 `year\_total` joins no longer nest\-loop over
  > filtered CTE scans \(their outer estimate and join method; Q4/Q11 routing:
  > SORT at SF0\.25, HASHSPILL M0141\-S2a\-fix2r\-a at SF1\), and Q14 statement
  > 2\'s inner is PG\'s probe chain\. Then re\-run the strict sweep\.
- [x] **M0146-0012 — correlated restrictions as base-rel index quals**
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0012b — a multi\-relation EXISTS body\'s correlated
  conjunct is a leaf restriction, with PG\'s AlternativeSubPlan choice**
  \(filed 2026\-10\-05 by M0146\-0012\)\. PG plans an EXISTS sublink twice
  in `make\_subplan` \(`./postgres/src/backend/optimizer/plan/subselect\.c`\):
  - as the correlated EXISTS, where `col = $n` is a base restriction and
    may drive an index probe;
  - as `convert\_EXISTS\_to\_ANY`\'s hashed ANY, built from the parse tree
    before planning;
  - and `setrefs\.c` keeps the cheaper for the expected number of calls.
  goopg runs EXISTS→ANY after planning, reading the correlation off the
  body\'s top quals \(`exists\_to\_any\.go`\), so slice 1 keeps EXISTS
  bodies\' correlated conjuncts above the join \(TPC\-DS Q35 lost its hashed
  ANY when they sank\)\.
  Kind: impl
  Parent: M0146\-0012
  - No witness today: TPC\-H, TPC\-DS and regress `subselect`/`join` have
    no per\-row multi\-relation EXISTS in PG\'s plans \(closure census\)\.
    Select it when a corpus query or regress case shows one\.
  - First step: move the EXISTS→ANY rewrite ahead of body planning \(the
    parse\-level form PG uses\), plan both bodies, and keep the cheaper
    per expected calls; then admit the correlated conjuncts as leaf
    restrictions in the EXISTS body \(`scalarSublinkBody`\'s twin\)\.
  - 2026\-10\-13 witness re\-check: regress `memoize` has one \(`postgres/src/test/regress/expected/memoize\.out`
    \~line 420\)\. An `EXISTS \(SELECT 1 FROM tenk1 t1 JOIN tenk1 t2 \.\.\. OFFSET 0\)` body plans as a per\-row
    `EXISTS\(SubPlan 1\)` whose correlated conjuncts are leaf Filters \(`t0\.two <> four` on t2, `t0\.ten = twenty` on
    t1\), under a Nested Loop with Memoize\. No hashed alternative appears there\.
    - TPC\-H/TPC\-DS PG plans still show no EXISTS SubPlan, so the witness is regress\-only\.
    - Rank: below item 10 under the S7 carve\-out \(same rule as M0146\-0175\)\.
- [x] **M0146\-0028 — pull simple FROM\-clause subqueries into the parent
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [!] **M0146\-0035 — WRONG RESULTS / DATA LOSS: an online TPC\-H clone
  loses database `tpch` and role `tpch`** \(filed 2026\-09\-29 by
  M0146\-0005ap; S2\)\. `tpch\_private\_clone\_snapshot` \(pg\_basebackup of the
  live bench cluster\) produced clones whose first start lists only
  postgres/template0/template1 and rejects role `tpch` \(09:37 and ~09:50\),
  while the arm\'s own clones at 09:32 and 09:41 had them\. The source still
  lists `tpch` in memory \(up since 2026\-09\-24\); the on\-disk
  `global/1262` row for `tpch` is `xmin=5`, `xmax=0`, infomask 0x803, and
  xid 5 is COMMITTED in `pg\_xact/0000` of both the source and the clones,
  so the heap row is visible — the listing comes from another store\. A
  restarted arm clone also lost `tpch` \(base/16408 present, row absent from
  `pg\_database`\)\. If the source keeps the same state on disk, its next
  restart would lose `tpch` too\. Evidence
  `analysis/m0146/m0146\-0035/`\.
  Kind: bug
  Parent: none
  - First step: find which store the per\-database catalog list is loaded
    from at startup \(goopg\-private catalog WAL/replay vs the 1262 heap\) and
    why the `tpch` entry is missing from it in a clone taken after a
    checkpoint\.
  - Recon 2026\-10\-02 \(banner item 2a\)\. Design
    `docs/design/0100\-0149/m0146\-0035\-clone\-catalog\-loss.md`; evidence
    `analysis/m0146/m0146\-0035/recon\-20261002\.txt`\.
    - Store: databases and roles reload from the shared heaps through
      `scanCatalogHeapRows`\. The only exit for a decodable, xmax\-free row
      is `catalogRowLive` seeing xmin Aborted\. Open runs
      `MarkUnknownAsAborted` before the reload, so an Unknown lane for
      xid 5 would drop both rows and persist the loss\.
    - Ruled out: decode \(a probe decodes the tpch row\), xid reuse
      \(`XidGen\.SetNext` is monotonic\), redo start \(the shipped pg\_control
      names the backup\-start checkpoint\), torn `pg\_xact` copy, and code
      changes since the incident\.
    - Not reproduced in 13 clone starts: today's arm clone ×3, and 6
      online clones of a busy throwaway source ×2
      \(`clone\-harness\.sh`\)\. The :65433 source restarted 2026\-10\-01
      and kept `tpch`\.
    - Landed: a shared\-catalog reload row that is rejected, or fails to
      decode, now logs a WARN with catalog, slot, xmin and CLOG status
      \(test `shared\_catalog\_reject\_log\_test\.go`\)\. The next clone
      that loses `tpch` names its cause\.
    - Next: search clone start logs for `shared catalog reload` WARNs.
      Until a reproduction, re\-run `clone\-harness\.sh` with the source
      under CLOG\-page\-0 churn \(new xids every few ms\) and with
      `-X stream`\.
    - Side finding: `txid\_current\(\)` returns 0, filed as M0146\-0043
      \(S2\)\.
  - Follow\-up 2026\-10\-02 \(loop \#34\): `clone\-harness\.sh` gained
    `CHURN=pgbench` \(4 clients committing single\-row inserts, CLOG page\-0
    churn\) and `XMODE=stream` \(`pg\_basebackup \-X stream`\)\. Both variants:
    12/12 clone starts kept `tpch`\. Total 37 clean starts on current code\.
  - **BLOCKED \(`\[\!\]`\) on recurrence** — not reproducible, with the
    diagnostic WARN in place\. Unblock when a clone start logs
    `shared catalog reload: row rejected by xmin status` \(or a decode
    WARN\) for `pg\_database`/`pg\_authid`, or by owner direction\. Banner
    item 2a continues with M0146\-0039\.

  > ## ESCALATION 2026\-09\-29 \(S2\) — online TPC\-H clones lose database `tpch`
  >
  > A private clone of the TPC\-H bench cluster can come up without its
  > `tpch` database and role, although the files and the committed heap row
  > are present; the TPC\-H gates that clone the cluster may then fail to
  > connect, and a restart of the source may lose the database the same
  > way\. Filed and not selected ahead of the banner, per S2; the owner
  > decides its placement\. The loop did not touch the source cluster
  > \(reads and pg\_basebackup only\)\.

- [ ] **M0146-0014 — parity-closure sweep** (recon; the milestone's
  exit report). Re-run the first-divergence census on both corpora and
  prove every remaining record is either assigned to a live task above
  or presented to the owner as a named, measured, waived residual —
  "no unnamed first-divergence records" is the acceptance bar.
  Kind: recon
  Parent: none
  > ## ESCALATION 2026\-10\-13 \(S4 lineage budget\) — M0146\-0014 held \[\!\]
  > The last five completed descendants \(M0146\-0148, 0014c, 0149, 0150, 0014d\) all carry `Movement: none`\. The
  > routing refreshes \(0014a–d\) are recons by construction; their movement lands in the tasks they file\.
  > - **What was attempted / proved:** 0014d \(this loop\) routed all 84 first\-divergence records at code `7e6d8b13f`
  >   \(SF0\.25 match 70, SF1 44\); every record has a live task or a named owner residual
  >   \(`analysis/m0146/m0146\-0014/routing\-20261013/ROUTING\.md`\)\.
  > - **Blocker:** the guard forbids filing the five new mechanism findings below as descendants\. They are recorded
  >   here, unfiled, with their evidence; each is a TPC\-DS plan\-instrument task \(expected movement: SF0\.25 match\)\.
  > - **Expected movement if re\-opened:** F1 Q32/Q92 \(both scales, a regression since M0146\-0130\), F2 Q60, F3 Q24,
  >   F4 Q70 — up to \+5 SF0\.25 matches; F5 is a cost\-model correctness item\.
  > - **Size:** each finding is a one\-to\-two\-loop slice\.
  >
  > Owner: re\-open \(or pin a LINEAGE\-BASELINE\) so F1–F5 can be filed under M0146\-0014, or place them elsewhere\.
  > RESOLVED 2026\-10\-13 \(OWNER DECISIONS 2026\-10\-13\): re\-opened `\[\!\]`→`\[ \]` with the banner
  > LINEAGE\-BASELINE pin \(0148/0014c/0149/0150/0014d\); F1–F5 filed as **M0146\-0176 through M0146\-0180**
  > \(Parent: M0146\-0014, item\-3 order 0176→0177→0178→0179→0180\)\.
  > **Standing channel 2026\-10\-13 \(c\):** this root is the permanent census/filing
  > channel (banner S9); FILEABLE-ROOTS exempts its filings from the lineage
  > budget and it stays `[ ]` — it is never `[!]`-marked for exhaustion.
  >
  > - **0014d\-F1 — a correlated SubPlan conjunct is applied unpriced above the join \(TPC\-DS Q32, Q92\)**: Since M0146\-0130 \(`a6905e365`\) the conjunct `ws\_ext\_discount\_amt > \(SubPlan 1\)` is not
  >   in the \{web\_sales, item\} join restrictlist; goopg applies it as a top `Filter:` that no join path pays for\. Q92
  >   SF0\.25 elects a nested loop at 2277\.04 = 2221\.05 \+ 160 x 0\.35, with no charge for 160 SubPlan calls at 138\.7 each\.
  >   PG keeps it as the inner\-unique hash join\'s Join Filter, paid on 3 matched rows: 8941\.29\. Q32 has the same
  >   fingerprint\. Before 0130 goopg had PG\'s shape for Q92 \(8994\.36\)\. Both scales\.
  >   - PG: `distribute\_qual\_to\_rels` \(initsplan\.c\) places the clause at relids \{web\_sales, item\}; `cost\_qual\_eval`
  >     charges the SubPlan\'s `per\_call\_cost` in `final\_cost\_hashjoin` / `final\_cost\_nestloop` \(costsize\.c\)\.
  >   - First step: dump where Q92\'s conjunct is placed \(`correlatedScalarSublinkLeaf` / the seam choosing leaf, join
  >     clause or top Filter\) and confirm it is missing from the \{item, web\_sales\} restrictlist; keep a two\-rel
  >     correlated\-SubPlan conjunct as a join restrictinfo so every join path pays `joinQualEvalCost`\.
  > - **0014d\-F2 — the ordered\-paths step offers a full Sort beside the Incremental Sort \(TPC\-DS Q60\)**: PG\'s `create\_ordered\_paths` \(planner\.c, the presorted\_keys arm\) adds only an
  >   Incremental Sort when the input path has a non\-empty presorted prefix; goopg\'s `addOrderedPaths` also adds a full
  >   Sort, which wins Q60 SF0\.25 by about 0\.6 \(0\.002%\)\.
  >   - First step: in goopg\'s ordered\-paths arm skip the full\-Sort candidate for an input whose presorted prefix is
  >     non\-empty, as PG does; re\-run the fire set \(Q4 may share the rule\)\.
  > - **0014d\-F3 — index\-only\-scan heap\-fetch term and int4 index density \(TPC\-DS Q24\)**: Q24 SF0\.25 elects a Parallel Index Only Scan on `store\_returns\_pkey` \(1441\.32, no heap\-fetch I/O\)
  >   where PG keeps a Parallel Seq Scan \(1653\.62\); PG\'s index\-only scan must carry \~212 more, i\.e\. heap fetches
  >   \(`cost\_index` allvisfrac, costsize\.c\)\. Full index\-only scans on `item\_pkey` / `customer\_pkey` price \+101 / \+255
  >   higher in PG\. Separately, goopg\'s int4 pkeys hold \~458 tuples per page, past PG\'s maximum for 16\-byte tuples
  >   \(40 vs \~52 pages for item\_pkey\)\.
  >   - First step: read\-only `relpages, relallvisible` for store\_returns, item, customer on :65438 \(tpcds025\) and a
  >     private SF0\.25 clone; PG `SET enable\_seqscan=off; EXPLAIN SELECT sr\_item\_sk, sr\_ticket\_number FROM
  >     store\_returns`; then decide whether goopg\'s relallvisible \(VACUUM VM accounting\) or the index density differs\.
  > - **0014d\-F4 — a Hash Semi Join with a pulled subquery rel as the hashed inner \(TPC\-DS Q70\)**: PG elects `Hash Semi Join` \(38304\.53\) hashing the 2\-row `tmp1` subquery under the
  >   main join\'s Gather; goopg elects a Nested Loop with tmp1 outer and a Materialize over the Gather \(38320\.63\)\. PG\'s
  >   arm built from goopg\'s own inputs would cost \~38262, so goopg likely never generates it\. Both scales\.
  >   - First step: instrument `addPath` on the joinrel \{store\_sales, d1, store, tmp1\} for Q70 SF0\.25 and list every hash
  >     candidate; if none hashes tmp1, find the gate \(`hash\_inner\_and\_outer` JOIN\_SEMI, joinpath\.c\)\.
  > - **0014d\-F5 — a Parallel Append is priced below its own first child \(TPC\-DS Q76\)**: Q76 SF0\.25: goopg\'s Parallel Append costs 14166\.13 over a first child of 17941\.59; PG\'s `cost\_append`
  >   \(costsize\.c, parallel\-aware arm\) sums the partial children to 37614\.25\. The query totals are 15612 vs 39026\. It
  >   does not decide Q76\'s first divergence but distorts any Append\-vs\-other choice\. Also seen: Q5\'s 2\-key Hash Right
  >   Join adds \~4049 over its inputs where PG adds \~945 \(`approx\_tuple\_count` / inner\-unique\)\.
  >   - First step: decompose goopg\'s Parallel Append cost for Q76 against PG\'s `cost\_append` partial arm \(per\-child
  >     cost, parallel divisor, the non\-partial rule M0146\-0137 changed\)\.
  - S9 replenishment pass 2026\-10\-14 \(census `analysis/m0146/m0146\-0014/routing\-20261014/`, capture at
    `82c287bc7`\): item 3 exhausted \(0042\'s rendering records sit under join\-order first divergences; 0007 / 0012b /
    0067 / 0175 rank below item 10\)\. Top actionable first\-divergence category: sort\-strategy \(17, 15 after COSTTIE
    waivers\)\. Filed M0146\-0190 \(TPC\-DS SF0\.25 Q26\)\.
  - S9 replenishment pass 2026\-10\-14, second \(census `analysis/m0146/m0146\-0014/routing\-20261014b/`, capture at
    `adaed8786`, after M0146\-0190 closed with movement\): item 3 exhausted again\. sort\-strategy 14 records, 10
    actionable after waivers; join\-order 14, 3 actionable\. Filed M0146\-0191 \(TPC\-DS SF0\.25 Q38\)\.
  - S9 pass 2026\-10\-14, third \(same census, after M0146\-0191 closed as a PG\-internal tie\): each remaining
    sort\-strategy record was checked for PG\-side stability on tmp/pgdbg first \(Q33 and Q72 are stable but span 4 and 6
    categories; SF1 Q18 is STALE\-SF1\)\. Filed M0146\-0192 \(TPC\-H Q22, initplan Param parallel safety\)\.
  - S9 pass 2026\-10\-14, fourth \(fresh census `analysis/m0146/m0146\-0014/routing\-20261014c/` at `b580d2344`, after
    M0146\-0192 landed\): sort\-strategy 5 actionable, aggregation\-strategy 4, parallelism 4, join\-order 3\. Filed
    M0146\-0193 \(TPC\-DS SF0\.25 Q33\)\.
  - S9 pass 2026\-10\-14, fifth \(fresh census `analysis/m0146/m0146\-0014/routing\-20261014d/` at `9129d26f0`\):
    sort\-strategy and qual\-placement tie at 3 actionable records after removing measured ties \(Q16, Q39, Q45, Q61,
    Q69, Q76\)\. Filed M0146\-0194 \(TPC\-DS SF0\.25 Q72, PG\-stable, 2% margin\)\.
  - S9 pass 2026\-10\-14, sixth — **exhausted at `9129d26f0`** \(census `routing\-20261014d`, still fresh: no
    planner\-relevant commit since\)\. After M0146\-0194 closed as a PG\-internal near\-tie, the remaining records were
    held to a proven\-mechanism bar, and none passed:
    - Q19 is PG\-stable but sits 0\.2% off \(18929\.90 vs 18967\.96\);
    - SF1 Q69 is sample\-sensitive at SF0\.25;
    - TPC\-H Q9, Q10, Q18, Q20 and Q21 sit on loads whose statistics differ \(lineitem 6001988 vs 5998990 rows;
      Q20 88111 vs 95012, Q21 165965 vs 269864 — cost levels driven by the estimates, not one mechanism\)\.
    No task was filed, so item 10 is live for the loop; re\-run after HEAD advances in the planner\-relevant paths\.
  - S9 re\-check 2026\-10\-14 — **exhausted at `ccdbe68f4`** \(an executor DDL\-NOTICE commit\): fresh captures give the
    same first\-divergence records at all three corpora as `routing\-20261014d` \(SF0\.25 81/99, SF1 66/99, TPC\-H
    14/22\), so the proven\-mechanism verdict stands and item 10 stays live\.
  - S9 re\-check 2026\-10\-14 — **exhausted at `db93173ed`** \(WAL summarizer S4a added a plan node to
    `internal/optimizer`\)\. Census `routing\-20261014e`:
    - TPC\-DS: the commit\'s own fire\-set captures at both scales \(no changed plan\)\.
    - TPC\-H: a fresh capture\.
    - All three corpora give records identical to `routing\-20261014d` \(SF0\.25 81/99, SF1 66/99, TPC\-H 14/22\), so
      no task is filed and item 10 stays live\.
  - S9 re\-check 2026\-10\-14 — **exhausted at `d4c7966e9`** \(WAL summarizer S4b gave an optimizer plan node a
    `Func` field\)\. Census `routing\-20261014f` uses that commit\'s fire\-set captures and a fresh TPC\-H capture\.
    The records are identical to `routing\-20261014e` at all three corpora \(81/99, 66/99, 14/22\)\. No task is filed\.
  - S9 re\-check 2026\-10\-14 — **exhausted at `91842230a`** \(the summarizer\'s pg\_stat\_io cell changed
    internal/executor\)\. Census `routing\-20261014g` is a fresh capture of all three corpora\. The records are identical
    to `routing\-20261014f` \(81/99, 66/99, 14/22\)\. No task is filed\.
  - S9 re\-check 2026\-10\-14 — **exhausted at `b2c66c076`** \(the `pg\_current\_wal\_\*lsn` builtins and their `pg\_lsn`
    `exprType` arm changed internal/executor and internal/optimizer\)\. Census `routing\-20261014h` is a fresh capture of
    all three corpora\. The records are identical to `routing\-20261014g` \(81/99, 66/99, 14/22\)\. No task is filed\.
  - S9 re\-check 2026\-10\-14 — **exhausted at `7dfe93df3`** \(S6b\'s pending relation deletes changed internal/executor\)\.
    Census `routing\-20261014i` is a fresh capture of all three corpora\. The records are identical to
    `routing\-20261014h` \(81/99, 66/99, 14/22\)\. No task is filed\.
  - S9 pass 2026\-10\-16 at `24ca1e37a` \(optimizer commits \+ TPC-H reload since 2026\-10\-15\) — **filed M0146\-0198**
    \(join\-method, Q43\); `routing\-20261016/ROUTING\.md`\. Matches: TPC-H 15/22, SF0\.25 80/99, SF1 67/99\.
  - S9 pass 2026\-10\-15 at `a00fb8110` \(corpora changed: SF0\.25 reload, TPC\-H realign\) — **filed M0146\-0195**
    \(join\-method, 4 actionable\); `routing\-20261015/ROUTING\.md`\. Q56/Q60 routed to M0141\-S2b\-4e\.
  - S9 re\-check 2026\-10\-14 — **exhausted at `c3dcb0f5f`** \(S6c\'s abort\-time relation deletes changed
    internal/executor\)\. Census `routing\-20261014j` is a fresh capture of all three corpora\. The records are identical
    to `routing\-20261014i` \(81/99, 66/99, 14/22\)\. No task is filed\.
  - S9 pass 2026\-10\-16 — **exhausted at `08ea8482e`**\. Census `routing\-20261016l` on the M0146\-0211 captures \(code equal to
    `1d2a14aa6`\): TPC\-H 20/22, SF0\.25 84/99, SF1 73/99\. No actionable record\. SF0\.25 Q84 is M0146\-0019 \(M0146\-0212\);
    SF1 Q68/Q18 are M0146\-0019/COSTTIE; scan\-type is M0146\-0019\. Open items\-0\-9 tasks and their gates:
    - M0146\-0014\(channel\), M0146\-0042\(channel\)\.
    - M0146\-0007\(below\-item\-10\), M0146\-0012b\(below\-item\-10\), M0146\-0067\(below\-item\-10\),
      M0146\-0175\(below\-item\-10\)\.
    - M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0196/0206\(below\-item\-10\)\.
    - M0146\-0205\(owner\-placement S2\)\.
    - M0146\-0178\(owner\-close RELPAGES\)\.
    - M0141\-S2b\-3b\(below\-item\-10\), M0141\-S2b\-4e\(below\-item\-10\), M0141\-S2b\-4\(blocked\-on M0141\-S2b\-4e\),
      M0141\-S7\-exec\-d\(below\-item\-10\)\.
    - M0141\-S3/S4/S5/S6\(owner\-close superseded by M0146\-0003 \[x\]\)\.
    - M0142\-0008a\-3/0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\.
    - The largest parity lever left is the owner corpus VACUUM \(M0146\-0019\): TPC\-H Q13/Q16, SF0\.25 Q23/Q84, SF1 Q68/Q18\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `12ba97662`** \(M0122\-0012 slice 2 changed internal/executor\)\.
    Census `routing\-20261016m`: a fresh TPC\-H `jointree\-parity\-capture\.sh` capture, plus the TPC\-DS fire\-set candidate
    captures of that commit's tree. The first\-divergence records in all three corpora are byte\-identical to
    `routing\-20261016l` \(20/22, 84/99, 73/99\)\. No task is filed. The open items\-0\-9 tasks and their gates are unchanged
    from the `08ea8482e` note above\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `2be3922a4`** \(M0122\-0012 slice 3 changed internal/executor\)\.
    Census `routing\-20261016n` uses a fresh TPC\-H capture plus the fire\-set\-s3 candidate captures\. The records are
    byte\-identical to `routing\-20261016m` \(20/22, 84/99, 73/99\)\. No task is filed; the gates are unchanged\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `aa56ea7e6`** \(M0122\-0012 slice 4 changed internal/executor\)\.
    Census `routing\-20261016o` uses a fresh TPC\-H capture plus the fire\-set\-s4 candidate captures\. The records are
    byte\-identical to `routing\-20261016n` \(20/22, 84/99, 73/99\)\. No task is filed; the gates are unchanged\.
- [ ] **M0146-0042 — EXPLAIN text-identity burn-down** (impl; owner
  decision 2026-10-02, split out of M0146-0005's scope — see banner
  item 3 and OWNER DECISIONS 2026-10-02). Work the `rendering`-category
  first-divergence census toward zero: every remaining EXPLAIN text
  difference between goopg and PG 18.3 on the TPC-H / TPC-DS corpora —
  operator-name rendering, `Output:` list qualification, alias/dollar-tag
  printing, `~~`/operator spelling, SubPlan/CTE labels, and any residual
  text-identical gap that is not a plan-SHAPE difference (those stay in
  0005 and its siblings). Owns the `text-identical` instrument
  (`SF0.25 26/99`, `SF1 16/99` at the split point); each slice follows
  the established slice-N convention (one divergence class, gated,
  `Movement:` line per the standardised format). Slices that turn out
  to need shape work route back to 0005/its named siblings, not here.
  Kind: impl
  Parent: M0146-0005
  - **Held `\[\!\]` 2026\-10\-09** by the S4 lineage escalation in M0146\-0005 \(its last five completed descendants, 0111–0115, all carry `Movement: none`\)\. Only the owner reopens it\.
  - **RE-OPENED 2026\-10\-12 \(owner, delegated\):** M0146\-0005's pin
    \(LINEAGE\-BASELINE 0111–0115\) resolves the exhaustion this hold
    proxied\.
  - Slice 2026\-10\-05 \(`fb26f7dc6`\): varchar join keys print their
    RelabelType to text in Hash/Merge Cond, as the Join Filter already did;
    TPC\-DS Q47/Q57 text\-identical at both scales \(SF0\.25 35 → 37, SF1
    25 → 27\)\. Design `docs/design/0100\-0149/m0146\-0042\-explain\-text\-identity\.md`,
    with a census of the remaining text classes \(BETWEEN folding \+ EC order
    Q10/Q69, Q31 EC order, qualification Q8/Q46/Q79, elided\-node references
    Q56/Q75, alias numbering Q8/Q56/Q58/Q75\)\.
  - Slice 2026\-10\-05 \(`7eed1a031`\): a pulled\-up EXISTS/IN body WHERE
    is constant\-folded \(PG's eval\_const\_expressions runs before
    pull\_up\_sublinks\); Q10 text\-identical at both scales \(SF0\.25 37 → 38,
    SF1 27 → 28\)\. Q69 SF1 match lost \(34 → 33\): its two anti\-joins are an
    exact leading\-term cost tie, now decided like SF0\.25's, which is
    already routed to M0146\-0014 as COSTTIE \(ledger row\)\.
Movement: yes — PLAN\-PARITY match SF1 34 → 33 \(Q69 cost tie, negative\); CATEGORIES\-EXCL\-MATCH SF1 join\-order 52 → 53, SF0\.25 join\-method 24 → 25 \(Q69\); text\-identical SF0\.25 37 → 38, SF1 27 → 28
  - Slice 2026\-10\-06 \(`7b5892287`\): a relation's EC equalities print in
    EquivalenceClass creation order \(process\_equivalence replay,
    generate\_base\_implied\_equalities order\), and a regenerated
    `const = col` prints as `col = const`; Q31 text\-identical at SF0\.25
    \(38 → 39\), regress join\.sql "Don't remove SJ" plan = PG\. Resolves part
    \(1\) of the M0146\-0005co ledger row\.
Movement: none — instrument artefact — text\-only; text\-identical SF0\.25 38 → 39, SF1 28 → 28
  - Slice 2026\-10\-06 \(`fa61c41a7`\): EXPLAIN columns qualify through an
    unpulled subquery whose Subquery Scan is elided \(columnIn tries every
    relation the scope binds to the source index; a sort key walks
    positionally into the Sort's input\) and, in a join residual, through an
    INTERSECT/EXCEPT to a kept Subquery Scan's alias; Q46/Q79
    text\-identical at SF1 \(28 → 30\); Q8/Q14/Q23 closer at both scales\.
Movement: yes — CATEGORIES\-EXCL\-MATCH SF0\.25 rendering 11 → 10; text\-identical SF1 28 → 30
  - Slice 2026\-10\-06 \(`95c4f5581`\): COALESCE/NULLIF/GREATEST/LEAST print
    as keywords \(get\_rule\_expr\); a CTE body's group key resolves
    positionally through a UNION's dedupe before the statement\-wide name
    fallback \(TPC\-DS Q75 no longer prints the consumer's `curr\_yr`\)\.
Movement: yes — CATEGORIES\-EXCL\-MATCH SF1 rendering 12 → 11
  - Slice 2026\-10\-06 \(`eef2d1762`\): a genuine set operation's branches are
    range\-table levels of their own \(plan\_set\_operations; setrefs adds
    each leaf at its SubqueryScan in plan\-walk order\), so EXPLAIN's `\_N`
    suffixes follow PG's; Q8 text\-identical at both scales, Q38/Q75 closer\.
Movement: none — instrument artefact — text\-only; text\-identical SF0\.25 39 → 40, SF1 30 → 31
  - Slice 2026\-10\-06 \(`f894f0c27`\): an unprinted copy of a twice\-referenced
    CTE body claims no node labels \(explainNames\.collect follows
    collectCTEHoist\); Q75's item suffixes agree with its column
    qualifiers, Q14 closer \(160 → 148 diff lines at SF1\)\.
Movement: none — instrument artefact — text\-only; Q75 diff lines SF0\.25 6 → 2, Q14 128 → 116 / 160 → 148
  - Slice 2026\-10\-06 \(`2f9990d50`\): a typmod'd CASE result labels its NULL
    `NULL::numeric` \(coerce\_to\_common\_type uses typmod \-1\); an ambiguous
    NestLoop param name narrows by binding id before the statement\-wide
    fallback; Q43 text\-identical at SF1 \(31 → 32\), Q56 closer\.
    Remaining MATCH\-query text residue: Q56/Q71 aggregate arguments through
    an unpulled UNION ALL, Q75 `\(\(expr\)\)` computed keys, Q45 SubPlan block
    position \(SF1\)\.
Movement: none — instrument artefact — text\-only; text\-identical SF1 31 → 32, SF0\.25 40 → 40
  - Slice 2026\-10\-06 \(`ad7a20114`\): a node's SubPlans print after its
    children, its InitPlans before \(explain\.c ExplainNode order\); Q45
    text\-identical at SF1 \(32 → 33\), Q6/Q32/Q92 closer\.
Movement: none — instrument artefact — text\-only; text\-identical SF1 32 → 33, SF0\.25 40 → 40
  - Slice 2026\-10\-08 \(`1ff32da82`\): join conditions over a GroupAggregate\'d derived table deparse
    through the aggregate that produced each column\.
    - A bare Hash/Merge Cond key walks the join row\.
    - A computed column prints as `\(expr\)` via `resolveKeySource`, which now crosses a Materialize\.
    - An aggregate over an aggregate nests \(`chaseAggregateResultArgs`\)\.
    - TPC\-DS Q65 is a full MATCH at both scales; Q44/Q78/Q95 lines now equal PG\'s\.
Movement: yes — PLAN\-PARITY match SF0\.25 42 → 43, SF1 33 → 34 \(Q65\); CATEGORIES\-EXCL\-MATCH qual\-placement 12 → 11 at both scales
  - Slice 2026\-10\-08 \(`3b9456d12`\): a Sort Key aggregate over a UNION ALL of grouped CTEs deparses its argument
    through the member\'s own aggregate\.
    - `sortKeyParts` chases the call\'s arguments\.
    - `resolveKeySource`\'s Project arm takes a computed result across a query\-level boundary\.
    - Q33/Q56/Q60 lines now equal PG\'s, and Q56 is text\-identical at SF0\.25\.
    - Remaining MATCH text residue at SF0\.25: Q75\'s computed group key needs PG\'s double parentheses\.
Movement: yes — CATEGORIES\-EXCL\-MATCH rendering SF0\.25 10 → 9, SF1 11 → 9
  - Slice 2026\-10\-08 \(`bce8f29c0`\): a UNION dedupe\'s computed group key prints get\_variable\'s extra
    parentheses \(`\(\(expr\)\)`\)\.
    - The Distinct arm wraps a key whose chase crossed the set operation; `inputIsSetOp` looks through Gather\.
    - Q75 is text\-identical at SF0\.25 \(1 → 0 lines\), one line closer at SF1\.
    - Every SF0\.25 MATCH query is now text\-identical\.
Movement: none — instrument artefact — text\-only; Q75 text\-identical at SF0\.25, categories unchanged
  - Slice 2026\-10\-08 \(`6340c1671`\): a Finalize aggregate\'s Group Key prints its Partial\'s text
    \(`aggGroupKeyText`, `partialAggregateBelow`\); Q76\.
    - Rendering class census, recorded in the design doc: Q39, Q54, Q66, Q67, Q71, Q77\.
Movement: yes — CATEGORIES\-EXCL\-MATCH rendering 9 → 8 at both scales \(Q76\)
  - Slice 2026\-10\-08 \(`c7063a0a3`\): a key deparses through a GROUP BY column the planner pruned
    \(an Aggregate Passthrough position\)\.
    - Q66\'s `year` now prints `date_dim\.d\_year`\.
Movement: yes — CATEGORIES\-EXCL\-MATCH rendering 8 → 7 at both scales \(Q66\)
  - Slice 2026\-10\-08 \(`413c69918`\): a key chase reads through a Finalize aggregate into its Partial
    \(`finalizeGroupPairs`, positional\)\.
    - Q77\'s `ss\.s\_store\_sk` now prints `store\.s\_store\_sk`, and its Merge Cond equals PG\'s\.
Movement: yes — CATEGORIES\-EXCL\-MATCH rendering 7 → 6 at both scales \(Q77\)
  - Slice 2026\-10\-08 \(`666a5a87a`\): a key chase stops at a Subquery Scan PG keeps \(alias\-qualified, quoted\)\.
    - Q71 `sum\(\"\*SELECT\* 3\".ext\_price\)`; Q44/Q49/Q67 window and sort keys now equal PG\'s\.
    - Pinned chases skip the relation\-identity check; the Join arm compares ids only within one numbering\.
Movement: yes — CATEGORIES\-EXCL\-MATCH rendering 6 → 4 at both scales \(Q71, Q67\)
  - Slice 2026\-10\-08 \(`d20998581`\): a computed group key\'s columns deparse through a kept Subquery Scan
    \(`chaseKeyExprColumns`, gated by `keyExprReachesKeptScan`\)\.
    - Q54\'s three key lines now equal PG\'s; its `my\_customers` CTE naming remains\.
Movement: none — instrument artefact — text\-only; rendering unchanged \(Q54 keeps a record on my\_customers naming\)
  - Slice 2026\-10\-13 \(`b5bf2cfec`\): a SubPlan\'s PARAM\_EXEC argument resolves first in the plan node that
    evaluates it \(find\_param\_referent\); TPC\-DS Q6 prints `i\.i\_category`, not `$0`, on the parameterised item\_pkey
    probe, and is text\-identical at both scales \(SF0\.25 64 → 65, SF1 40 → 41\)\.
Movement: yes — PLAN\-PARITY match SF0\.25 70→71, SF1 44→45; CATEGORIES\-EXCL\-MATCH parameterisation SF0\.25 13→12, SF1 26→25; text\-identical SF0\.25 64→65, SF1 40→41
  - Slice 2026\-10\-13 \(`52bf18eac`\): a Memoize cache key deparses against the parameterising loop\'s outer row; TPC\-DS
    Q44 prints `v11\.item\_sk` \(text\-identical SF0\.25 70 → 71\)\.
    - The text\-only queue is empty: Q59 waits on EquivalenceClass\-ordered join clauses \(plan state, ledgered
      2026\-10\-13\); Q78\'s rendering rides its cost\-tie join\-order swap\.
Movement: none — instrument artefact — text\-only \(Q44 already MATCH\); text\-identical SF0\.25 70 → 71
  - Slice 2026\-10\-13 \(`854083b44`\): a key chase passes a sublink\-bearing HAVING Filter when it is on a plain group
    key of the aggregate below; TPC\-DS Q14\'s rollup Sort Key prints `item\.i\_brand\_id` \(text\-identical SF0\.25 69 → 70\)\.
    - Found: Q59\'s Hash Cond conjunct order is EquivalenceClass order in PG \(join restrictlist\); ledgered \(plan state, not
      an EXPLAIN patch\)\.
Movement: none — instrument artefact — text\-only \(Q14 already MATCH\); text\-identical SF0\.25 69 → 70
  - Slice 2026\-10\-13 \(`8944f2bff`\): a key through a Finalize/Partial aggregate\'s pruned GROUP BY column deparses
    to the target\-list Var; TPC\-DS Q66 text\-identical at both scales \(SF0\.25 68 → 69, SF1 44 → 45\)\.
Movement: none — instrument artefact — text\-only \(Q66 already MATCH\); text\-identical SF0\.25 68 → 69, SF1 44 → 45 is not an S3 instrument
  - Slice 2026\-10\-13 \(`549070713`\): a computed Sort/Window key deparses each aggregate output column through the
    Agg\'s target list; TPC\-DS Q36/Q86 text\-identical at both scales \(SF0\.25 66 → 68, SF1 42 → 44\)\.
Movement: yes — text\-identical SF0\.25 66→68, SF1 42→44; CATEGORIES\-EXCL\-MATCH rendering SF0\.25 2→1, SF1 4→3
  - Slice 2026\-10\-13 \(`8094e1dd6`\): an omitted CASE ELSE prints `NULL::bpchar` / `character varying` / `real` /
    timestamp types \(format\_type\(type, \-1\)\); TPC\-DS Q36/Q86/Q70 Sort Keys match PG\'s CASE text; aligned lines SF0\.25
    3680 → 3683, SF1 3097 → 3100\. Q36/Q86 still differ in the Window clause \(GROUPING deparse, next slice\)\.
Movement: none — instrument artefact — text\-identical flat \(Q36/Q86 keep the Window\-clause difference\); aligned lines \+3 per scale
  - Slice 2026\-10\-14: InitPlan numbers follow the subquery RTE order \(set\_base\_rel\_sizes plans subquery RTEs in
    range\-table order\); `reservePGPlanIDs` orders nested query levels by their sublinks\' source position
    \(`nestedLevelsInPlanningOrder`\)\. TPC\-DS Q58 matches at both scales\. Test
    `TestInitPlanNumbersFollowSubqueryRTEOrder`\. Found by the S9 census at `6912d6e65` \(Q58\'s record was a
    parameterisation\-labelled numbering difference with no other category\)\.
Movement: yes — PLAN-PARITY match SF0.25 80 -> 81, SF1 65 -> 66 (Q58); CATEGORIES-EXCL-MATCH parameterisation SF0.25 10 -> 9, SF1 13 -> 12
  - Slice 2026\-10\-13 \(`d2b0ba67d`\): a key above a UNION ALL appendrel deparses through the Append\'s first child
    and stops at a kept `"\*SELECT\* n"` member \(set\_deparse\_plan / get\_variable\); TPC\-DS Q5 text\-identical at both
    scales \(SF0\.25 65 → 66, SF1 41 → 42\); Q14 10 → 2 differing lines\.
Movement: yes — PLAN\-PARITY match SF0\.25 71→72, SF1 45→46; CATEGORIES\-EXCL\-MATCH join\-order SF0\.25 22→21, SF1 44→43; text\-identical SF0\.25 65→66, SF1 41→42
  - Routed 2026\-10\-13 by M0146\-0014d \(next slices\): TPC\-DS Q5 \(done, `d2b0ba67d`\) — a Var above an Append resolves at its first child
    \(`Subquery Scan on "\*SELECT\* 2"`, ruleutils\.c set\_deparse\_plan / get\_variable\), where goopg descends into the
    subquery; Q6 \(done, `b5bf2cfec`\) — a SubPlan\'s PARAM\_EXEC prints its referent \(`i\.i\_category`, find\_param\_referent\) where goopg
    prints `$0`\. Both scales; `analysis/m0146/m0146\-0014/routing\-20261013/agent\-groupB\.md`\.
  - Slice 2026\-10\-08 \(`1745ad42f`\): key chases pass through a DISTINCT dedupe; a bitmap Recheck param
    renders as its Index Cond sibling\.
    - Q54\'s `my\_customers\.\*` now prints `customer\.\*`, and Q49\'s first key is `\(\'web\'::text\)`\.
Movement: yes — CATEGORIES\-EXCL\-MATCH rendering 4 → 3 at both scales \(Q54\)
- [x] **M0146\-0080 — WRONG RESULTS: PL/pgSQL RAISE parameters that are
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0146\-0081 — WRONG RESULTS: a writable\-CTE INSERT reports the
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0146\-0084 — WRONG RESULTS: regress polymorphism\'s `testpolym`
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0146\-0165 — functions with OUT parameters are incomplete** \(filed 2026\-10\-12 by M0146\-0084\)\.
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0167 — user\-function DEFAULT arguments are never applied** \(filed 2026\-10\-13 by
  M0146\-0165\)\. `CREATE FUNCTION g\(a int, b int DEFAULT 2\) RETURNS int AS \$\$ SELECT \$1 \+ \$2 \$\$ LANGUAGE sql;
  SELECT g\(1\)` raises `function g does not exist`; PG 18\.3 returns 3 \(plpgsql the same\)\. Overload resolution
  \(`callArgTypesForCandidate`\) requires the exact input count and no caller fills `ArgDefaults`
  \(func\_get\_detail / expand\_function\_arguments in parse\_func\.c / clauses\.c\)\. Probe:
  `tmp/m0165/probe\.sql` \(f7\).
  Kind: bug
  Parent: M0146\-0165
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: let a candidate with `pronargdefaults` match `n` inputs when `n >= NumInputArgs \- ndefaults`,
    then append the evaluated trailing defaults before binding \(both SQL and PL/pgSQL paths\)\.
- [ ] **M0146\-0168 — a non\-SETOF SQL function whose final query returns several rows raises 21000**
  \(filed 2026\-10\-13 by M0146\-0165\)\. `CREATE FUNCTION mr\(\) RETURNS int AS \'select generate\_series\(1,3\)\'
  LANGUAGE sql; SELECT mr\(\)` — PG 18\.3 returns 1 \(the first row of the final query; functions\.c
  postquel\_getnext stops after one row for a non\-set result\); goopg raises `SQL function mr returned more
  than one row`\.
  Kind: bug
  Parent: M0146\-0165
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: `executeSQLRoutine`\'s last\-statement loop — keep the first row and drop the second\-row check\.
- [x] **M0146\-0086 — WRONG RESULTS: `text \|\| bool` renders the bool as
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0166 — WRONG RESULTS: `inet::text` and `text \|\| inet` omit the mask length** \(filed
  2026\-10\-13 by M0146\-0086\)\. PG 18\.3: `\'1\.2\.3\.4\'::inet::text` → `1\.2\.3\.4/32`, `\'::1\'::inet::text` →
  `::1/128`, `\'x\' \|\| \'1\.2\.3\.4\'::inet` → `x1\.2\.3\.4/32` \(the text cast is `network\_show`, not
  `inet\_out`\); goopg prints `1\.2\.3\.4` for all three\. `concat\(\'x\', i\)` correctly keeps `x1\.2\.3\.4`\.
  Probe: `tmp/m0086/probe3\.sql`\.
  Kind: bug
  Parent: M0146\-0086
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: an inet datum is an untyped `KindString` at run time — find the evalCast arm that
    casts to text/varchar and render an inet/cidr source through `network\_show` \(always `/bits`\) using
    the static source type; then give `concatModeOf` the operand\'s static type so `\|\|` does the same
    in both evaluator twins\.
- [x] **M0146\-0076 — WRONG RESULTS: DML statement\-level triggers do not
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0146\-0055 — WRONG RESULTS: COPY FROM fires no BEFORE ROW
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0146\-0009p — a parameterised probe\'s rows ignore its scan
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0141\-S2a\-fix2r\-c — plan\-schema types carry no typmod, so
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0153 — a GENERATED ALWAYS identity column accepts an explicit
  value on plain INSERT** \(filed 2026\-10\-10 by M0141\-S2a\-fix2r\-c\)\. `create
  table g\(id int generated always as identity, t text\); insert into g\(id,t\)
  values \(5,\'x\'\)` succeeds on goopg; PG 18\.3 raises `cannot insert a
  non\-DEFAULT value into column "id"` \(rewriteTargetListIU,
  rewriteHandler\.c\)\. No restart involved\. Ledger row M0134\-0029
  \(2026\-08\-20\) states plain INSERT raises it — a regression or a
  narrower original fix\.
  Kind: impl
  Parent: M0141\-S2a\-fix2r\-c
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: bisect the M0134\-0029 claim \(regress `identity` case\) and
    locate the INSERT\-path identity check\.
- [ ] **M0146\-0154 — after a CLEAN restart an identity/serial sequence resumes
  32 values ahead** \(filed 2026\-10\-10 by M0141\-S2a\-fix2r\-c\)\. One insert,
  clean stop/start, insert: goopg returns 34, PG 18\.3 returns 2\. PG writes
  `last\_value`/`log\_cnt` to the sequence page and only crash recovery
  replays the SEQ\_LOG\_VALS\-ahead WAL value; goopg restores its
  every\-32\-nextval snapshot \(ledger M0119\-0004 root\-0020\) on every start\.
  Kind: impl
  Parent: M0141\-S2a\-fix2r\-c
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: persist the in\-memory counter at clean shutdown \(or keep
    the sequence relation page current\) and restore the snapshot only on
    crash recovery\.
- [x] **M0146\-0155 — WRONG RESULTS: a function\'s SET clauses are never
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [x] **M0146\-0157 — WRONG RESULTS: a SECURITY DEFINER routine runs as its
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0161 — WRONG RESULTS: table GRANTs \(relacl\) are lost on restart** \(filed
  2026\-10\-12 by M0146\-0157; S2\)\. At HEAD `0fbe34f51`'s parent: `CREATE ROLE ro; CREATE TABLE
  sx\(v int\); GRANT SELECT ON sx TO ro` shows `relacl = \{postgres=arwdDxtm/postgres,ro=r/postgres\}`;
  after a clean restart `relacl` is empty and `SET ROLE ro; SELECT \* FROM sx` is denied\. A
  REVOKE from PUBLIC presumably reverts the same way \(privilege widening\)\. PG: relacl is a
  pg\_class column, durable\.
  Kind: impl
  Parent: M0146\-0157
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: find where GRANT ON TABLE writes relacl \(postmaster grant\_ddl\.go
    tryRecordTableGrant / the pg\_class heap row\) and whether loadUserTablesFromHeap reads the
    relacl column back; probe REVOKE \.\.\. FROM PUBLIC and column grants too\.
  > \#\# ESCALATION 2026\-10\-12 \(S2\) — M0146\-0161 loses granted privileges on restart
  > Filed under M0146\-0157\. RESOLVED 2026\-10\-13 (OWNER DECISIONS 2026\-10\-13): NOT placed — queues below item 10 under the S7 correctness carve\-out\.
- [x] **M0146\-0158 — PGC\_SUSET parameters can be SET by a non\-superuser**
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0162 — WRONG RESULTS: a non\-superuser that logs in directly runs with superuser
  rights** \(filed 2026\-10\-12 by M0146\-0158; S2\)\. `CREATE ROLE lu LOGIN; CREATE TABLE privt\(a
  int\)`; connecting as `lu` \(no SET ROLE\), `SELECT count\(\*\) FROM privt` succeeds \(PG: permission
  denied for table privt\), and SET of superuser\-only parameters is allowed\. `connTx\.NonSuperuserRole`
  is set only by SET ROLE / SET SESSION AUTHORIZATION, and every ACL gate keys on it; `is\_superuser`
  correctly reads off\. Ledgered since 2026\-07\-09 \(M0122\-0007 DROP DATABASE row: "plain LOGIN without
  SET ROLE resolves to bootstrap superuser — systemic actingRole RBAC gap"\) but never filed\.
  Kind: impl
  Parent: M0146\-0158
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: at connection setup \(server\.go runPostStartupLoop, beside connTx\.SessionUser\), seed
    NonSuperuserRole with the login role when it is not a superuser \(catalog IsSuperuser\); then audit
    RESET ROLE / RESET SESSION AUTHORIZATION \(they must restore the login role, not ""\) and run the
    testport families that log in as other roles\.
  > \#\# ESCALATION 2026\-10\-12 \(S2\) — M0146\-0162 grants superuser rights to every login role
  > Filed under M0146\-0158\. RESOLVED 2026\-10\-13 (OWNER DECISIONS 2026\-10\-13): NOT placed — queues below item 10 under the S7 correctness carve\-out\. Severity\-high RBAC hole; first candidate for the next correctness batch\.
- [x] **M0146\-0156 — DATA LOSS: a function created in a CREATE DATABASE\'d
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0160 — DATA LOSS: catalog objects other than routines in a CREATE DATABASE\'d
  database are lost on restart** \(filed 2026\-10\-12 by M0146\-0156; S2\)\. After one clean
  restart in `CREATE DATABASE dx`: `CREATE SEQUENCE sq` is gone \(`relation "sq" does not exist`\),
  a domain\'s CHECK no longer applies \(`\(\-1\)::posint` succeeds\), and a serial column\'s default
  is gone \(INSERT fails with a not\-null violation\)\. Every reload in
  `internal/initdb/catalog\_heap\_reload\.go` that builds its RelFileNode from `cat\.DBOID\(\)` scans
  only the default database: sequences, domains, enums, casts, operators, collations,
  conversions, opfamilies/opclasses \+ amop/amproc, transforms, event triggers, publications,
  FDWs / servers / user mappings / foreign tables, text\-search dicts/configs, access methods\.
  PG: every per\-database catalog is that database\'s heap, durable\.
  Kind: impl
  Parent: M0146\-0156
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: per object kind, apply the `reloadUserRoutinesFromHeap` pattern \(default heap,
    then each `cat\.ListDatabases\(\)` database with the schema\-reload skip set\), stamping the
    database where the registry is per\-database \(sequences live in the per\-database table
    namespace; domains/enums carry DBOid\); start with sequences and domains \(wrong results\)\.
  > \#\# ESCALATION 2026\-10\-12 \(S2\) — M0146\-0160 loses data
  > Sequences, domain constraints and serial defaults in any CREATE DATABASE\'d database vanish
  > on restart\. Filed under M0146\-0156\. RESOLVED 2026\-10\-13 (OWNER DECISIONS 2026\-10\-13): NOT placed — queues below item 10 under the S7 correctness carve\-out\.
- [x] **M0146\-0060 — the single\-table rule\-based index producer overrides
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0067 — PlaceHolderVar\-wrapped FROM\-subquery pull\-up**
  \(filed 2026\-10\-05 by M0146\-0028's closure\)\. `pull\_up\_simple\_subquery`
  wraps substituted outputs in PlaceHolderVars when the parent uses grouping
  sets \(`REPLACE\_WRAP\_ALL`\), when the subquery sits on the nullable side
  of an outer join, and for LATERAL references under a join
  \(`pullup\_replace\_vars\_callback`, prepjointree\.c\)\. goopg has no
  PlaceHolderVar, so `parentFromAdmitsDerivedPullup` declines
  grouping\-sets parents and `splitInnerJoinChainForPullup` declines outer
  links\.
  Kind: impl
  Parent: M0146\-0028
  - Rank: below item 10 under the S7 carve\-out — no TPC witness \(M0146\-0028 §14\); expected
    `Movement:` is regress/`none`\. Select when a corpus query or regress case names it\.
  - **Held `\[\!\]` 2026\-10\-09** by the S4 lineage escalation in M0146\-0005 \(its last five completed descendants, 0111–0115, all carry `Movement: none`\)\. Only the owner reopens it\.
  - **RE-OPENED 2026\-10\-12 \(owner, delegated\):** M0146\-0005's pin
    \(LINEAGE\-BASELINE 0111–0115\) resolves the exhaustion this hold
    proxied\.
  - No witness today: no TPC\-H or TPC\-DS query pulls up a simple subquery
    in these positions \(M0146\-0028 §14\)\. Select it when a corpus query or
    regress case needs it\.
  - First step: a PlaceHolderVar expression \(phexpr \+ phrels \+
    phnullingrels\) evaluated at its eval\_at level, the way
    `add\_placeholders\_to\_base\_rels` / `fix\_placeholder\_input\_needed\_levels`
    place it\.
- [x] **M0146\-0126 — WRONG RESULTS: COALESCE does not resolve its arguments\' common type**
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146\-0163 — now\(\) is typed timestamp, not timestamptz** \(filed 2026\-10\-12 by
  M0146\-0126\)\. `pg\_typeof\(now\(\)\)` is `timestamp without time zone` on goopg, `timestamp with
  time zone` on PG 18\.3 \(pg\_proc prorettype 1184\); it also skews the COALESCE mismatch error text\.
  Kind: impl
  Parent: M0146\-0126
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: find the now / current\_timestamp / transaction\_timestamp arms of exprType and
    funcCallResultType and the value they produce; probe comparisons and casts against PG\.
- [ ] **M0146\-0164 — float8 division prints 15 significant digits** \(filed 2026\-10\-12 by
  M0146\-0126\)\. `select 1::float8 / 3` prints `0\.333333333333333` on goopg; PG 18\.3 prints
  `0\.3333333333333333` \(float8out shortest round\-trip, extra\_float\_digits=1\)\.
  Kind: impl
  Parent: M0146\-0126
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out, OWNER DECISIONS 2026\-10\-13\)\.
  - First step: trace the datum the division operator produces and the output formatter it reaches
    \(PGFloatOut is the reference; a %\.15g path is suspected\)\.
- [x] **M0146-0172 — TPC\-DS SF1 Q72 runs 65x slower since M0146\-0009p: goopg elects the item\-only inventory route**
  (full entry archived → `completed_milestones/completed_fix_plan_015.md`)
- [ ] **M0146-0173 — timestamp / interval arithmetic: wrong output and spurious errors** \(filed 2026\-10\-13 by
  M0146\-0060\)\. Against PG 18\.3: `SELECT timestamp \'2020\-01\-01\' \+ interval \'5 minutes\'` prints
  `2020\-01\-01 00:05:00\.000000` \(PG `2020\-01\-01 00:05:00`\); `g \* interval \'1 minute\'` raises `operator \* requires
  numeric operands`; `timestamp \+ \(text\)::interval` and `timestamp \+ make\_interval\(mins => g\)` raise `operator \+ requires
  numeric operands`; one\-argument `to\_timestamp\(double\)` raises `requires exactly 2 arguments`\. Probe
  `tmp/m0060/p3\.sql`\. Same class as M0146\-0040 \(`date \+ integer`\)\.
  Kind: bug
  Parent: M0146\-0060
  - Rank: below item 10 — correctness\-only, no owner placement \(S7 carve\-out\)\.
  - First step: the BinaryOp `\+` / `\*` typing arms for timestamp/interval and int/interval \(PG timestamp\_pl\_interval,
    interval\_mul, mul\_d\_interval\) and the timestamp output path for a folded constant\.
- [ ] **M0146-0175 — the ordered index producer builds a clauseless twin of an index that has restriction clauses**
  \(filed 2026\-10\-13 by M0146\-0172\)\. PG\'s build\_index\_paths builds ONE path per index and adds the clauseless
  ordered one only when no clause matches \(indxpath\.c\)\. goopg\'s `addOneOrderedIndexPath` \(pathindexordered\.go\) always
  builds it, so regress join\'s `j2\.id1 = any \(array\[1\]\)` now ties the SAOP path exactly \(8\.17 each, PG ties at
  12\.17\) and keeps the `ANY` as a Filter where PG prints `Index Cond`\.
  Kind: impl
  Parent: M0146\-0172
  - Rank: below item 10 — regress\-only witness, no TPC query named \(S7 carve\-out\)\.
  - First step: skip the ordered producer for an index the restriction producer already built a clause path for
    \(the same rule `restrictionPathIsIndexOnly` applies to its index\-only twin\), then re\-run the fire set\.
- [x] **M0146-0195 — TPC-H lineitem order-key probe: PG's parameterised `idx_lineitem_orderkey_fkidx` Nested Loop, goopg's hash join \(Q3/Q7 join-method, Q5 scan-type\)**
  \(filed 2026-10-15 by the M0146-0014 S9 pass, `analysis/m0146/m0146-0014/routing-20261015/ROUTING.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census: join-method is the top actionable category \(4: TPC-H Q2, Q3, Q7; SF0\.25 Q43\)\. Records on the
    realigned TPC-H corpus \(`routing-realign-20261015`\), verdict SHAPE-DIFF:
    - Q3 depth 3 under Gather: PG Nested Loop with `Index Scan using idx\_lineitem\_orderkey\_fkidx` \(0\.43\.\.1\.58,
      9 rows\); goopg Parallel Hash Join over a Parallel Seq Scan on lineitem\.
    - Q7 depth 4 under Hash Join: same probe in PG \(0\.43\.\.1\.20\); goopg Parallel Hash Join\.
    - Q5 depth 7 \(scan\-type\): PG probes `idx\_lineitem\_orderkey\_fkidx` \(3\.31\), goopg `lineitem\_pk` \(7\.77\)\.
      Q5's goopg plan runs past the 600s digest cap — the error `bench/tpch/baseline\-digests\.txt` pins, which
      makes the acceptance arm unpassable \(M0146\-0178 escalation\); a fix here is the owner's option \(c\) path\.
    - Same queries: goopg's `order\_customer\_fkidx` probes price \~0\.65 above PG's \(2\.21 vs 1\.56, 2\.29 vs 1\.64\)\.
    - Q2 and SF0\.25 Q43 are the reverse direction \(PG Hash Join, goopg Nested Loop\); fold in only if the first
      step finds the same cost term\.
  - First step: on a private TPC-H clone, list goopg's lineitem paths for Q3's orders⋈lineitem join \(is the
    parameterised `idx\_lineitem\_orderkey\_fkidx` path generated, and at what cost\) against PG's cost\_index terms
    \(costsize\.c: index pages, tree height, correlation, Mackert\-Lohman\); check whether M0146\-0178 slice 2's
    index\-page resolution changes the TPC-H figures\.
  - Expected instrument: TPC-H PLAN-PARITY match 12 → up to 15 \(Q3, Q5, Q7\); CATEGORIES-EXCL-MATCH join-method and
    scan-type down\.
  - Gate note: any production change here needs the acceptance\-arm PASS stamp, unpassable since 2026\-10\-15
    until the owner acts \(M0146\-0178 escalation\)\.
  - **2026\-10\-15 first step done; root cause fixed** \(design `docs/design/0100\-0149/m0146\-0195\-btree\-dedup\-before\-split\-allocation\.md`\)\.
    - Not a cost formula: goopg's TPC-H fk indexes are 7\-18x PG's after the realign \(`idx\_lineitem\_orderkey\_fkidx`
      117050 vs 16468, `order\_customer\_fkidx` 28617 vs 4121; pre\-realign 9549 / 1563\); 86\-92% of their leaf blocks
      are empty, allocated pages\.
    - Cause: the insert split allocated its right page before dedup/purge and abandoned it when dedup made room;
      the live page never passes `popRecyclableBlock` under a recycle horizon, so the next split extended again\.
      Fixed by allocating after the dedup decision \(nbtinsert\.c:998 before :1720\)\. Test
      `TestDuplicateInsertsLeakNoPages` \(HEAD: 480/541 leaf blocks empty\)\. Scratch: 4\-way duplicate insert build
      5423 → 607 blocks = CREATE INDEX\.
    - Landable without the arm \(internal/access/nbtree only\): units, nbtree \+ `\-race`, tpch\-spotcheck, sf025 PASS=99\.
    - \(Superseded below: the owner REINDEX request is withdrawn\.\)
    - **Owner 2026\-10\-15:** the acceptance\-arm blocker is cleared \(XFAIL pin — see M0146\-0178 RESOLVED\), so the
      next step below \(cost\_index term decompose\) is committable once it lands\.
    - **Owner 2026\-10\-15 \(reload executed, NEGATIVE RESULT\):** :65433 was reloaded by INSERT replay under a
      binary carrying 7b356ec04 \(and d7c05acde\) — TRUNCATE created fresh index files, yet the fk indexes came back
      at the SAME sizes \(`idx\_lineitem\_orderkey\_fkidx` 117050, `order\_customer\_fkidx` 28617, `lineitem\_pk`
      41512\)\. The dedup\-before\-split fix does not cover whatever path bloated these indexes on the real corpus —
      the remaining bloat driver is a live question for this task \(the compact\-twin measurement says it matters:
      12→14 when compact twins are used\)\. Route to compact the REF cluster's indexes: fix M0146\-0196 first
      \(REINDEX/DROP INDEX resolve only DefaultDBOid — reproduced live on :65433\), then `REINDEX TABLE`\.
    - Residual: delete \+ VACUUM \+ reinsert still doubles an index — vacuumed pages sit on the deleting handle's free
      list, PG's FSM is shared \(M0122\-0010a\)\.
  - **2026\-10\-15 private\-clone measurement: index size does NOT drive Q3/Q5/Q7** \(`analysis/m0146/m0146\-0195/`\)\.
    - `tpch\-estimate\-audit\-arm\.sh` gained `PRE\_AUDIT\_SQL` \(SQL on the arm's private clone before the audit\)\.
    - As\-is clone: match 12/22\. Clone with compact twins of the 8 fk indexes \(`CREATE INDEX \.\.\._rb`, names mapped
      back\): match 11/22 — Q3/Q5/Q7 unchanged, Q11 loses its match, Q18/Q21 diverge further\.
    - Q5's orders probe keeps `order\_customer\_fkidx` at 2\.29/probe either way \(PG 1\.64\): the gap is another
      `cost\_index` term, not index pages\. The owner REINDEX request is WITHDRAWN — it would not move Q3/Q5/Q7 and
      Q11's current match rests on today's bloated sizes \(goopg fk indexes 6\-8x PG; goopg's bulk build is denser
      than PG's\)\.
    - Found: REINDEX / DROP INDEX cannot resolve relations in a CREATE DATABASE'd database → M0146\-0196\.
    - Next: decompose one probe's cost term by term against PG's `cost\_index` \(costsize\.c\) — Q5/Q7's
      `order\_customer\_fkidx` probe \(goopg 0\.43\.\.2\.21 rows=16, PG 0\.43\.\.1\.56\): index pages fetched,
      Mackert\-Lohman heap pages with loop\_count, correlation, cpu terms; this is a costing change, so landing it
      needs the acceptance arm \(M0146\-0178 escalation\)\.
  - **2026\-10\-15 CORRECTION: index bloat IS the Q3/Q5/Q7 driver** — the previous bullet's measurement was invalid\.
    - goopg builds one parameterised index path per parameterisation \(`pickIndexCoveringLeadingPrefix`: most bound
      columns, ties → first by name\); the `\_rb` twins tied with the bloated originals and were never costed\.
    - Probe cost reproduced exactly: `order\_customer\_fkidx` probe from \~30000 customers PG 4\.98 vs goopg 7\.07; the
      2\.09 gap is genericcostestimate's loop\-amortised index\-page term \(PG 4121 pages → 0\.55, goopg 28617 → 2\.63\)\.
      A compact twin named to sort first is chosen at 4\.65, as predicted\.
    - Re\-measured with twins `a\_<name>` \(sort first, mapped back\), HEAD \+ M0146\-0178 slice\-2 binary:
      TPC-H match 12 → 14; Q3, Q5, Q7 MATCH; join\-method 5 → 3, scan\-type 8 → 5, parallelism 3 → 1; Q10 loses its
      match \(goopg's bulk\-built twins, 1563 pages, are denser than PG's 4121\)\.
    - Owner request \(replaces the withdrawn one\): reload `:65433` by INSERT with a binary carrying 7b356ec04, so the
      fk indexes come out without the leak \(REINDEX is unavailable there, M0146\-0196\)\. Expected: Q3/Q5/Q7 MATCH\.
    - Filed M0146\-0197: build a parameterised path per eligible index, as `build\_index\_paths` does\.
  - **2026\-10\-15 ESCALATION \(owner\): the `:65433` reload did NOT run on the fixed binary** — its negative result
    \(index sizes unchanged, 117050 / 28617 / 41512\) does not test 7b356ec04\.
    - The server process serving `:65433` \(pid 472797, started \~17:29\) executes
      `bench/tpch/runtime\_goopg/goopg\-bin\.285df1201` \(`readlink /proc/<pid>/exe`\) — a build from 285df1201,
      before the fix \(7b356ec04, 17:00\)\. A new `goopg\-bin` was written at 17:27, but the running server's exe is
      the old one\.
    - After the reload, `order\_customer\_fkidx` \(relfilenode 16452\) shows the same pattern as before: 26378 of
      28617 leaf blocks hold only a high key — the dedup\-leak signature \(8 of 9 empty\)\.
    - TRUNCATE is not the cause: `truncateTableAndPartitions` truncates each index file and rebuilds it empty
      \(`operators\_ddl\.go`\)\.
    - Owner action: restart `:65433` on a binary built at or after 7b356ec04 \(confirm with `readlink
      /proc/<pid>/exe`\), then repeat the INSERT reload\. Expected per the clone measurement: fk indexes near
      CREATE INDEX size, TPC-H Q3/Q5/Q7 toward MATCH\. \(REINDEX stays unavailable, M0146\-0196\.\)
    - **RESOLVED 2026\-10\-15 \(owner\)**: `:65433` stopped via `goopg stop \-mode fast`, `goopg\-bin` replaced with a
      clean\-worktree build of 7e9900a35 \(sha256 8c47fc59…; old kept as `goopg\-bin\.1781cc20b`\), restarted via
      `ref\-clusters\-ensure\.sh \-\-only 65433` — `readlink /proc/561965/exe` confirms the new binary\. TRUNCATE \+
      17,320\-statement INSERT replay; all 8 tables match PG row counts\. Index relpages: `idx\_lineitem\_orderkey\_fkidx`
      117050 → 16641 \(PG 16468\), `order\_customer\_fkidx` 28617 → 2220 \(PG 4121\) — the dedup fix works on the
      real corpus\. **Q5 now completes in \~1s \(was 900s timeout\)** and its digest is bit\-identical to PG :65432;
      EXPLAIN picks `idx\_lineitem\_orderkey\_fkidx`/`order\_customer\_fkidx` as PG does\. Baseline Q5 re\-pinned to
      the real digest \(XFAIL retired — the next arm run would have fired XFAIL\-RESOLVED anyway\)\.
      tpch\-spotcheck Q12=2/Q13=35 PASS\.
  - **2026\-10\-15 CLOSED — Q3, Q5 and Q7 MATCH on the reloaded corpus\.** After the owner's redo on a 7e9900a35 build
    \(419dc5c9e\), a clean\-HEAD parallel plan\-only TPC-H capture \(`analysis/m0146/m0146\-0178/residual\-workers/m0178r\-tpch\-head\*`\)
    against PG's `routing\-realign\-20261015`: match 12 → 15; CATEGORIES\-EXCL\-MATCH join\-method 5 → 2, scan\-type 8 → 4,
    join\-order 5 → 3, parallelism 3 → 1\. Q10 keeps its match \(insert\-built indexes are PG\-sized, unlike the clone's
    bulk\-built twins\)\. Q2 and SF0\.25 Q43 \(the reverse direction\) stay as separate census records\.
Movement: yes — TPCH match 12→15; CATEGORIES-EXCL-MATCH scan-type 8→4
- [ ] **M0146\-0196 — REINDEX and DROP INDEX cannot resolve relations in a CREATE DATABASE'd database** \(filed
  2026\-10\-15 by M0146\-0195\)\. On a private clone of the TPC-H cluster, connected to db `tpch`: `REINDEX TABLE
  region`, `REINDEX TABLE public\.region`, `REINDEX INDEX region\_pk` and `DROP INDEX nation\_regionkey\_fkidx`
  all fail "does not exist", while `SELECT count\(\*\) FROM region` works and `CREATE INDEX nation\_regionkey\_fkidx`
  reports "already exists"\. PG resolves both against the current database \(`RangeVarGetRelidExtended`\)\.
  Kind: impl
  Parent: M0146\-0195
  - Rank: below item 10 under the S7 carve\-out \(maintenance correctness, no plan instrument\) unless the owner
    places it\. It blocks index maintenance on every non\-default database, including the bench clusters\.
    **Owner confirms 2026\-10\-15: below item 10 stands** — it gates owner maintenance only, and the Q5 pin removes
    the operational pressure that motivated raising it\.
  - First step: `internal/executor/operators\_reindex\.go` and the DROP INDEX path — find the lookup that ignores
    the session database \(same family as M0125\-0028's bare\-ANALYZE/VACUUM fix\)\. Executor change: needs the arm\.
- [x] **M0146-0210 — TPC\-DS SF1 Q54: goopg's semi\-join Unique estimates 1 row from a 2\-row input, and the join order above it differs from PG's \(qual\-placement\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016k/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at 3f5f73822 \(routing\-20261016j still fresh\): qual\-placement SF1 Q54 is the only actionable record left;
    SF1 Q18 was re\-routed to COSTTIE by M0146\-0209\. Verdict SHAPE\-DIFF, first divergence depth 6 under the inner
    GroupAggregate\. SF0\.25 Q54 is MATCH\.
  - PG: `Unique` \(2 rows\) over `Sort` over `Gather` \(2\), then `⋈ customer\_address`, then `⋈ Materialize\(store\)` with
    the ca/store Join Filter, then `⋈ Gather\(store\_sales ⋈ date\_dim\)` on `c\_customer\_sk = ss\_customer\_sk`\. goopg:
    `Unique` \(1 row\) over `Gather Merge` \(2\), then `⋈ Gather\(store\_sales ⋈ date\_dim\)`, then `⋈ customer\_address`, then
    `⋈ store`\. Top Nested Loop: PG 82996\.61, goopg 82979\.02\.
  - Lead: PG sizes the semi\-join unique\-ification with `estimate\_num\_groups` over the RHS expressions, clamped to the
    input's 2 rows\. goopg's 1 suggests it sizes from the per\-worker partial rows\.
  - First step: DP trace of SF1 Q54 on a private clone\. Find the producer of the unique\-ified \{item, catalog/web\_sales,
    date\_dim\_1, customer\} rel and its row estimate input; compare with PG's `create\_unique\_path` \(pathnode\.c\) and
    `estimate\_num\_groups`\. Then check whether the Sort\-over\-Gather vs Gather Merge choice under the Unique is a
    fuzz\-boundary tie, as M0146\-0209 found\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match 72 → 73 \(Q54\); CATEGORIES\-EXCL\-MATCH qual\-placement down\.
  - Closed 2026\-10\-16: the lead was right\. Three ported mechanisms; design
    `docs/design/0100\-0149/m0146\-0210\-gather\-rows\-distinct\-seed\.md`\.
    - `EstimateRows\(\*Gather\|\*GatherMerge\)` now reads its stamped path rows, which are `compute\_gather\_rows`
      \(1 × 1\.7 → 2\), instead of re\-estimating the per\-worker child \(1\)\.
    - The DISTINCT seed reads its input path's cost through Project wrappers \(`limitInputPath`\), not the Project's
      legacy display cost \(\+0\.04\)\.
    - The partial\-DISTINCT gathered rows are now clamped \(`clampRowEst`\), as in the partial\-aggregate twin\.
    - Result: Q54's DISTINCT keeps 2 rows, the join order above it is PG's, and the DISTINCT is `Unique → Sort →
      Gather`\.
    - Gates: units rc=0, tpch\-spotcheck PASS, tpch\-acceptance\-arm PASS \(24 MATCH\), tpcds\-sf025 PASS=99, tpcds\-fireset
      PASS \(introduced=none\), ea\-ratchet PASS; TPC\-H plan text byte\-identical\.
    - Follow\-up filed: M0146\-0211 \(five sibling upper seeds\)\.
  Movement: yes — TPC\-DS SF1 PLAN\-PARITY match 72 → 73 \(Q54\); CATEGORIES\-EXCL\-MATCH SF1 qual\-placement 4 → 3, join\-order 22 → 21; SF0\.25 85 held\.
- [x] **M0146-0211 — the grouping / partial\-aggregate / partial\-sort / ordered / upperrel seeds still charge a Project wrapper's legacy display cost** \(filed 2026\-10\-16 by M0146\-0210\)\.
  Kind: impl
  Parent: M0146-0210
  - M0146\-0210 made the DISTINCT seed read its input path through Project wrappers \(`limitInputPath`\), as PG prices
    an upper rel on `cheapest\_input\_path` \(the target is applied to the path itself, dummypp\)\.
  - The same `legacyDisplayCostOf\(child\)` seed remains in `groupingpaths\.go:82`, `partialaggpaths\.go:248/330`,
    `partialsortpaths\.go:217`, `upperordered\.go:98` and `upperrel\.go:197`\. Each adds a per\-row Project charge PG
    does not make\.
  - First step: apply the same look\-through to each seed, then measure the TPC\-H, SF0\.25 and SF1 captures\. Near\-tie
    shapes may move both ways\.
  - Expected instrument: CATEGORIES\-EXCL\-MATCH moves on the upper\-rel categories \(aggregation\-strategy,
    sort\-strategy\); no match loss\.
  - Closed 2026\-10\-16: `upperInputPathCost` \(cardinality\.go\) now seeds the DISTINCT, grouping,
    partial\-aggregate and ordered producers through Project wrappers\. Design
    `docs/design/0100\-0149/m0146\-0211\-upper\-input\-path\-cost\.md`\.
    - SF0\.25 Q84 regressed MATCH → SHAPE\-DIFF: Sort over Gather 8271\.21 vs Gather Merge 8271\.23\. The old 0\.11
      Project charge had tipped that tie\. Filed as M0146\-0212; kept per R3\.
    - SF1 73 and TPC\-H 20 held\.
    - Gates: units rc=0, tpch\-spotcheck PASS, tpch\-acceptance\-arm PASS \(24 MATCH\), tpcds\-sf025 PASS=99, tpcds\-fireset
      PASS \(introduced=none\), ea\-ratchet PASS\.
  Movement: yes — TPC\-DS SF0\.25 PLAN\-PARITY match 85 → 84 \(Q84 regression, filed M0146\-0212\); SF1 73 and TPC\-H 20 held\.
- [x] **M0146-0212 — REGRESSION \(M0146\-0211\): TPC\-DS SF0\.25 Q84 plans Sort over Gather where PG plans Gather Merge \(sort\-strategy, parallelism\)** \(filed 2026\-10\-16 by M0146\-0211\)\.
  Kind: impl
  Parent: M0146-0211
  - goopg now plans Sort \(8271\.18\.\.8271\.21\) over Gather; the Gather Merge it elected before costs 8269\.98\.\.8271\.23\.
    The removed Project charge \(0\.11 on the Sort arm only\) had tipped the 0\.02 gap the PG way\.
  - PG elects the Gather Merge \(8142\.56\.\.8143\.70\) although its own formulas give Sort over Gather 8143\.66\. A PG
    mechanism is unmodelled: the LIMIT's fractional comparison, create\_ordered\_paths' partial\-path block, or the
    add\_path tie\-break order\.
  - First step: load Q84's six tables \(customer, customer\_address, customer\_demographics, household\_demographics,
    income\_band, store\_returns from `:65438` tpcds025\) into the PG debug build \(`tmp/pgdbg`\)\. Probe
    `create\_ordered\_paths` and `add\_path` for the ordered rel, as M0146\-0209 did, then port the deciding term\.
    Never restore a charge chosen by result \(R6\)\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match 84 → 85 \(Q84\)\.
  - Closed 2026\-10\-16: not a defect\. The record is the M0146\-0019 corpus\-VACUUM residual\. Evidence in
    `analysis/m0146/m0146\-0212/FINDINGS\.md`\.
    - A PG debug build loaded with the same tpcds025 tables plans Sort over Gather right after ANALYZE, and Gather
      Merge once autovacuum has set the visibility map \(the cheaper index\-only probes move the tie\)\.
    - goopg on its never\-vacuumed corpus \(relallvisible 0\) plans Sort over Gather\. After VACUUM on a private clone it
      plans Gather Merge, PG's shape\.
    - The pre\-0211 match came from the removed one\-armed Project charge\.
    - Premise stale: the record is a corpus residual, not a costing defect\.
  Movement: none — premise stale: SF0\.25 Q84 is the M0146\-0019 corpus\-VACUUM residual \(PG itself plans Sort over Gather on the unvacuumed tables\); M0146\-0211 is consistent with PG\.
  > \#\# ESCALATION 2026\-10\-16 \(R3\) — M0146\-0211 lost SF0\.25 Q84
  > The change was kept, not reverted: it removes a non\-PG, one\-armed Project charge\. SF0\.25 match 85 → 84, SF1 and
  > TPC\-H held\. Owner: confirm keeping it, or direct otherwise\.
  > Update 2026\-10\-16 \(M0146\-0212 close\): PG plans the same Sort over Gather on the unvacuumed tables, and goopg
  > plans Gather Merge after VACUUM\. The loss is the M0146\-0019 corpus residual, not a defect of 0211\.
- [x] **M0146-0209 — TPC\-DS SF1 Q18: goopg plans a Gather Merge over per\-worker Sorts below the grouping\-sets GroupAggregate, where PG plans a Sort over a Gather \(sort\-strategy\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016j/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at 4a75cefbb: sort\-strategy and qual\-placement tie at 1 \(SF1 Q18, SF1 Q54\); the differ's order puts
    sort\-strategy first\. Verdict SHAPE\-DIFF, first divergence at depth 3 under the GroupAggregate: PG `Sort`
    \(46169\.49\) over `Gather` \(46167\.84, 53 rows\) \| goopg `Gather Merge` \(46015\.57, 49 rows\)\. SF0\.25 Q18 is MATCH\.
  - Near tie: goopg's own Gather would cost 45009\.43 \+ 1000 \+ 0\.1 × 49 = 46014\.33, plus a sort of 49 rows, against
    Gather Merge's 46015\.57\. That is inside add\_path's fuzz\. Upstream, the estimates also differ: `d\_year = 2001`
    gives 214 rows per worker vs PG's 252, and `catalog\_sales ⋈ date\_dim` 1788 vs 2105\.
  - First step: DP/path trace of SF1 Q18 on a private clone\. Instrument `addPath` for both the Gather\+Sort and the
    Gather Merge candidates at the grouping input, and compare with PG's `cost\_gather\_merge` / `cost\_sort` inputs
    \(the PG debug build, tmp/pgdbg\)\. Then decide between a COSTTIE residual \(record and waive\), a stats\-sample
    difference \(STALE\-SF1 / estimate\), or a costing defect to port\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match 72 → 73 \(Q18\); CATEGORIES\-EXCL\-MATCH sort\-strategy down\.
  - Closed 2026\-10\-16: a COSTTIE residual, not a costing defect\. Evidence in `analysis/m0146/m0146\-0209/FINDINGS\.md`\.
    - A PG debug build with probes shows PG builds the Gather Merge \(45984\.01\), cheaper than Sort\-over\-Gather
      \(45984\.22\)\. `add\_path` rejects it because it is fuzzily equal \(ratio 1\.00973\) to a serial sorted path \(46431\.42\),
      and the tie\-break prefers the parallel\-safe incumbent\.
    - goopg has the same candidates and the same tie\-break \(path\.go:1205\)\. Its serial path sits 1\.154% above the
      GM, so both survive and the GM wins\.
    - Part of the gap is the M0146\-0019 corpus residual: relallvisible is 0, so the cd2 index\-only probe costs 2\.67 vs
      PG's 0\.88\. VACUUM on a private clone brings the ratio to 1\.01007, still about 3 cost units outside the fuzz\.
    - The rest is ANALYZE\-sample noise \(rows: goopg 49, reference PG 53, a freshly analysed PG 45\)\.
    - Premise stale: the record is a fuzz\-boundary tie, not a mechanism to port\.
  Movement: none — premise stale: SF1 Q18 is a COSTTIE residual \(add\_path fuzz boundary, 0\.007% after the M0146\-0019 corpus VACUUM\); goopg's paths and add\_path tie\-break match PG's\.
- [x] **M0146-0208 — TPC\-DS Q45: goopg estimates the OR\-with\-hashed\-SubPlan Join Filter at 0\.75 where PG has 0\.52, so it plans a partial \(Finalize\) GroupAggregate where PG plans a plain GroupAggregate over Sort over Gather \(aggregation\-strategy\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016i/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at 6c0d1a3ac: aggregation\-strategy is the top actionable category \(2: SF0\.25 Q45, SF1 Q45\); sort\-strategy
    SF1 Q18 and qual\-placement SF1 Q54 have 1 each\. Verdict SHAPE\-DIFF, first divergence depth 1 under the Limit: PG
    `GroupAggregate` \| goopg `Finalize GroupAggregate`\. SF0\.25 is the primary record \(S9\(3\)\)\. Its 2026\-10\-13 COSTTIE
    waiver was measured on a different record \(sort\-strategy, depth 2\), so it no longer applies\.
  - Estimate: the Nested Loop's Join Filter is `substr\(ca\_zip, 1, 5\) = ANY \('\{\.\.\.\}'\) OR ANY \(i\_item\_id =
    \(hashed SubPlan 1\)\.col1\)`\. PG keeps 0\.53 of the rows at SF0\.25 \(95 → 50\) and 0\.524 at SF1 \(292 → 153\)\.
    goopg keeps 0\.75 \(91 → 68\) and 0\.749 \(283 → 212\)\. The larger group input makes goopg's partial aggregation the
    cheaper plan\.
  - PG's mechanism: the hashed SubPlan arm has no estimator\. It falls to `boolvarsel`'s default of 0\.5
    \(clausesel\.c:956\)\. The OR is s1 \+ s2 − s1·s2 = 0\.524, so the `substr\(\.\.\.\) = ANY` arm is about 0\.048 \(an
    expression with no statistics; scalararraysel over 9 constants\)\.
  - First step: compute goopg's selectivity of each OR arm for this clause \(clauseSelectivity / the join\-clause
    path, joinselectivity\.go\), and find which arm departs from PG's 0\.048 / 0\.5\. Then port the PG arm\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match 84 → 85 and SF1 71 → 72 \(Q45\); CATEGORIES\-EXCL\-MATCH
    aggregation\-strategy down\.
  - Closed 2026\-10\-16: the substr arm was the departing one\. `orInListSelectivity` priced an OR arm's IN\-list only
    over a bare column and declined `substr\(ca\_zip, 1, 5\)` to the whole\-OR 0\.5, so 0\.5 \+ 0\.5 − 0\.25 = 0\.75\.
    - `orInListExprSelectivity` prices a single\-relation expression operand through the restriction sibling
      `inListExprSelectivity`: 1/200 per element, the disjoint sum 9 × 0\.005\. The OR becomes 0\.5225, PG's figure\.
    - Design: `docs/design/0100\-0149/m0146\-0208\-or\-inlist\-expression\-selectivity\.md`\.
    - Q15 also changed plan and stays MATCH \(its estimate is now 55 rows, PG 54\)\.
    - Gates: units rc=0, tpch\-spotcheck PASS, tpch\-acceptance\-arm PASS \(24 MATCH\), tpcds\-sf025 PASS=99 \(Q15/Q45
      ck\-verified\), tpcds\-fireset PASS \(fires Q15 Q45, introduced=none\), ea\-ratchet PASS; TPC\-H plan text
      byte\-identical\.
  Movement: yes — TPC\-DS SF0\.25 PLAN\-PARITY match 84 → 85, SF1 71 → 72 \(Q45\); CATEGORIES\-EXCL\-MATCH aggregation\-strategy SF0\.25 1 → 0, SF1 6 → 5\.
- [x] **M0146-0207 — TPC-H Q2: PG hash\-joins on `\(SubPlan 1\) = partsupp\.ps\_supplycost` as a second hash key with `part` outer; goopg keeps the SubPlan equality as a Join Filter and hashes `part` \(parameterisation\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016h/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census `routing\-20261016g`, still fresh at 0d1c2d879; SF1 Q68 was re\-routed by M0146\-0204\. Four categories tie
    at 1 \(parameterisation TPC-H Q2, aggregation SF1 Q45, sort\-strategy SF1 Q18, qual\-placement SF1 Q54\); the
    differ's category order puts parameterisation first\. Verdict SHAPE\-DIFF, first divergence at depth 1, the
    top Hash Join under the Sort\.
  - PG: `Hash Cond: \(part\.p\_partkey = partsupp\.ps\_partkey\) AND \(\(SubPlan 1\) = partsupp\.ps\_supplycost\)`,
    outer `Gather` over `part` \(779 rows\), and the 160000\-row `nation ⋈ region ⋈ supplier ⋈ partsupp` hashed;
    48872\.73\. goopg: `Hash Cond: \(partsupp\.ps\_partkey = part\.p\_partkey\)` plus `Join Filter: \(ps\_supplycost =
    \(SubPlan 1\)\)`, with `part` hashed; 27264\.24\. The join tree below and the SubPlan body otherwise match\.
  - Mechanism: SubPlan 1's only outer reference is `part\.p\_partkey`, so pull\_varnos gives the SubPlan relids \{part\}\.
    The equality is then a hashjoinable join clause \{part\} vs \{partsupp\} \(`check\_hashjoinable`, initsplan\.c:3833\)\.
    `final\_cost\_hashjoin` charges `hash\_qual\_cost\.per\_tuple`, SubPlan included, per outer row \(costsize\.c:4496\),
    so `part`\-outer wins\. This is the join\-clause twin of M0146\-0203, which landed the restriction case\.
  - First step: DP trace of Q2 \(audit arm `\-queries 2`\)\. Check whether the top join's clause list classifies
    `ps\_supplycost = \(SubPlan 1\)` as a join clause with relids \{part, partsupp\} and whether it is hashjoinable
    there\. Then check that hash\-path generation \(`hashclauses`\) admits it and that the hash qual cost charges the
    SubPlan's per\-tuple cost per outer row\. Instrument `addPath` for the `part`\-outer two\-key candidate\.
  - Expected instrument: TPC-H PLAN\-PARITY match 19 → 20 \(Q2\); CATEGORIES\-EXCL\-MATCH parameterisation down; TPC\-DS
    SF0\.25 / SF1 byte\-identical or improved \(SubPlan hash keys may recur there\)\.
  - Closed 2026\-10\-16\. goopg kept every sublink\-bearing equality as a residual \(`exprCarriesSublink`, M0146\-0012a\)\.
    The SubPlan's cost then rode the few hash matches of an inner\-unique `part` build, so partsupp\-outer won
    \(27264\.24\)\. A PG `\-DOPTIMIZER\_DEBUG` build keeps only the part\-outer HashPath \(47670\.17\) at the top
    joinrel: PG prices the SubPlan inside `hash\_qual\_cost`, per outer row \(costsize\.c:4458/4475/4497\)\.
    - The PARAM\_EXEC SubPlan equality is now a hash\-only key: `restrictInfo\.subplanKey`,
      `paramExecScalarSublinksOnly`\.
    - It is priced through `qualEvalOps`' sublink pricing\.
    - It is published through `hashKeyExprSide`, which is `exprSideUnder` with `scopeIgnore`\.
    - The merge arms and the parameterised\-index arms skip it \(ledgered\)\.
    - Design: `docs/design/0100\-0149/m0146\-0207\-subplan\-equality\-hash\-key\.md`\.
    - Q2's cost gap \(50310\.96 vs PG 48872\.73\) is the `part` estimate, 899 vs 779 \(ledgered\)\.
    - Gates: units rc=0, tpch\-spotcheck PASS \(Q12=2, Q13=35\), tpch\-acceptance\-arm PASS \(24 MATCH, Q2 469 rows\),
      tpcds\-sf025 PASS=99 \(plan shapes 99 same\), tpcds\-fireset PASS \(no changed plan on SF0\.25 or SF1\), ea\-ratchet
      PASS\.
  Movement: yes — TPC\-H PLAN\-PARITY match 19 → 20 \(Q2\); CATEGORIES\-EXCL\-MATCH join\-order 1 → 0, parameterisation 1 → 0; SF0\.25 84 and SF1 71 unchanged\.
- [ ] **M0146-0205 — WRONG RESULTS: COPY FROM inside an explicit transaction block survives ROLLBACK** \(filed
  2026\-10\-16 by M0146\-0204; S2\)\. On a fresh goopg server at `7010eab30`, `CREATE TABLE c5\(\.\.\.\); BEGIN; \\copy c5 FROM
  file \(4000 rows\); ROLLBACK; SELECT count\(\*\) FROM c5` returns 4000\. An inline `COPY c5 FROM STDIN` of 1 row in a second
  block also survives, giving 4001\. `INSERT` in the same kind of block rolls back correctly\. PG 18\.3 returns 0 both times
  \(scratch PG on 5545\)\. Ledgered on 2026\-08\-13 under M0132\-S7 \("COPY ignores explicit blocks both protocols"\),
  with resume point `dispatchCopyViaExecutor`, but never filed\.
  Kind: impl
  Parent: M0146-0204
  - Rank: below item 10 — correctness\-only, no plan instrument \(S7 carve\-out\), unless the owner places it\.
  - First step: `dispatchCopyViaExecutor` / the executor's copy path \(`copy\.go` "always runs in its own auto\-commit
    transaction"\): when `connTx\.InExplicit\(\)`, join the block's transaction \(`connTx\.Tx\(\)`\) and make the commit
    sites no\-ops, as the M0132\-S3 in\-block `Execute` did\. Test both protocols and COPY inside a SAVEPOINT\.
  - Repro: `analysis/m0146/m0146\-0204/repro\-scratch\.sql`\.
  > \#\# ESCALATION 2026\-10\-16 \(S2\) — M0146\-0205: COPY inside BEGIN … ROLLBACK keeps its rows
  > Wrong results and loss of atomicity: a client that rolls back a failed load keeps its partial rows\. Filed under
  > M0146\-0204\. Owner: place it, or confirm below item 10\.
- [ ] **M0146-0206 — VACUUM tail truncation leaves the visibility map untruncated, so relallvisible exceeds relpages; a tail emptied by DELETE is not truncated** \(filed 2026\-10\-16 by M0146\-0204\)\.
  Checked on a scratch goopg server against a scratch PG 18\.3:
  - COPY 7200 rows, then `VACUUM`: PG gives relpages 53, relallvisible 53; goopg gives 53 and 64\.
  - goopg's `\_vm` fork still marks blocks 53\-63, which are past the end of the heap, all\-visible and all\-frozen\.
    PG clears them when it truncates \(`RelationTruncate` → `visibilitymap\_prepare\_truncate`\) and clamps
    `new\_rel\_allvisible` to `new\_rel\_pages` \(vacuumlazy\.c:904\)\.
  - Insert 20000 rows, delete 18000, `VACUUM` twice: PG truncates to 59 pages; goopg keeps 589\.
  - goopg truncates the FSM on this path, but not the VM \(`internal/commands/vacuum/vacuum\.go`, after
    `pool\.TruncateRelationTail`\)\.
  - No wrong result was observed: in the probes, re\-extension cleared the stale bits, and the planner clamps
    `allvisfrac`\. A stale all\-frozen bit on a re\-extended page could still let VACUUM skip that page\.
  Kind: impl
  Parent: M0146-0204
  - Rank: below item 10 — correctness\-only \(S7 carve\-out\)\. It touches the plan instruments only through relpages
    and relallvisible after the owner's corpus VACUUM \(M0146\-0019\)\.
  - First step: add `VisibilityMap\.TruncateRel\(rel, nblocks\)`, clearing the bits of blocks ≥ nblocks, beside
    `opts\.FSM\.TruncateRel`, and on the WAL replay of the truncate record \(smgr\_redo `SMGR\_TRUNCATE\_VM`\)\. Clamp the
    relallvisible that VACUUM writes\. Then find why `lastNonEmpty` treats a page of LP\_UNUSED items as non\-empty
    \(PG's `count\_nondeletable\_pages` checks `ItemIdIsUsed`\)\.
- [x] **M0146-0204 — TPC\-DS SF1 Q68: goopg probes `household\_demographics\_pkey` in a Nested Loop above the `store` join, where PG hash\-joins a 1800\-row seq\-scanned `household\_demographics` below it \(join\-method\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016g/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at 355cf2d1f: five categories tie at 1 actionable record \(join\-method SF1 Q68, parameterisation TPC-H Q2,
    aggregation SF1 Q45, sort\-strategy SF1 Q18, qual\-placement SF1 Q54\); the differ's category order puts join\-method
    first\. Verdict SHAPE\-DIFF, first divergence depth 8 under the Nested Loop of the parallel subtree below
    `Gather Merge`\. SF0\.25 Q68 is MATCH\.
  - PG: `store\_sales ⋈ date\_dim` \(63104\.18, 687 rows\), then a Hash Join on `ss\_hdemo\_sk` over a Hash on a Seq Scan
    of `household\_demographics` \(161\.00, 1800 rows\), then `store`, 63291\.35, 157 rows\. goopg: `store\_sales ⋈
    date\_dim` \(63105\.03, 658 rows\), then `store`, then a Nested Loop probing `household\_demographics\_pkey`,
    63300\.56, 150 rows\. Row estimates agree at every level, so the difference is in the costs\.
  - PG's `cost\_hashjoin` arithmetic on goopg's inputs gives about 63294 for the hash\-first order, about 6 below
    goopg's choice\. Suspects: \(a\) the partial hash join with a serial Hash inner on `household\_demographics` is not
    generated \(goopg does build one for `store`\); \(b\) it is generated but priced higher, for example by the
    seq scan's filter cost, the unnarrowed inner width, or the bucket\-size term\.
  - First step: DP trace of SF1 Q68 \(estimate\-audit arm, `\-queries 68`, on a private clone\); check that the
    hash\-`household\_demographics` candidate is in the pathlist \(instrument `addPath`\) and compare its cost terms
    with PG's `final\_cost\_hashjoin`\. The gap is about 0\.01%, so also measure whether the record belongs in the
    COSTTIE band; if it does, record it as a waived residual instead of changing costs\.
  - Expected instrument: SF1 PLAN\-PARITY match 71 → 72 \(Q68\); CATEGORIES\-EXCL\-MATCH join\-method down; SF0\.25 Q68 stays
    MATCH\.
  - Closed 2026\-10\-16: neither suspect\. The cause is the corpus, not the planner\. Evidence in
    `analysis/m0146/m0146\-0204/FINDINGS\.md`\.
    - Both candidates are built, and the hash order was priced \(`join\.hash\.partial` 63301\.26 for
      `\{ss, dd, hd\}`\)\. It lost only through the `household\_demographics` seq scan, 172 against PG's 161\.00:
      64 heap blocks against 53\.
    - goopg's file holds PG's exact tuple layout on 53 pages, plus 11 empty pages left by the COPY bulk extension\.
      PG's autovacuum truncated that tail \(`lazy\_truncate\_heap`, 11 ≥ 64/16, vacuumlazy\.c:3189\); goopg's corpus
      has never been vacuumed\.
    - `VACUUM household\_demographics` on a private clone gives 53 blocks and PG's plan shape\.
    - Re\-routed to the M0146\-0019 owner corpus\-VACUUM residual\. SF1 `catalog\_page` \(272 vs 242\) has the same cause\.
    - Premise stale: the record was a corpus\-maintenance residual, not a costing mechanism\.
    - Found on the way and filed: M0146\-0205 \(COPY in an explicit block survives ROLLBACK, S2\) and M0146\-0206
      \(VACUUM truncation leaves the VM untruncated and relallvisible above relpages\)\.
  Movement: none — premise stale: SF1 Q68's record is the M0146\-0019 corpus\-VACUUM residual \(an unvacuumed bulk\-extension tail on household\_demographics\); no planner change\.
- [x] **M0146-0203 — TPC-H Q20: goopg seq\-scans `supplier` at 100\.00 and applies `n\_name = 'CANADA'` on the Nested Loop over a Memoize'd `nation\_pk` probe, where PG filters `nation` and bitmap\-probes `supplier` \(qual\-placement\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016f/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at 692c9df93: qual\-placement is the top actionable category \(2: TPC-H Q20, SF1 Q54\)\. Verdict SHAPE\-DIFF,
    first divergence depth 2 under the semi join: PG Nested Loop \(Seq Scan nation Filter n\_name, Bitmap Heap Scan
    supplier on supplier\_nation\_fkidx\) 252\.34 \| goopg Nested Loop \(Seq Scan supplier, Memoize nation\_pk\) 104\.00 with
    the nation restriction as the join's Filter\.
  - Two suspects: \(a\) goopg's `supplier` seq scan costs 100\.00 here but 325\.00 in its own Q9 plan and PG's — no page
    term, which may be what lets the order win; \(b\) the nation base restriction rides the join instead of the
    parameterised probe \(PG would print it as the Index Scan's Filter\)\.
  - First step: trace the supplier leaf's path costs \(DP\_TRACE=1, audit arm `\-queries 20`\) and the nation probe's
    restriction placement; compare with PG `cost\_seqscan` inputs for supplier\.
  - Other record, possibly another mechanism: SF1 Q54 \(Join Filters swapped between two nested\-loop levels\)\.
  - Expected instrument: TPC-H PLAN\-PARITY match 18 → 19 \(Q20\); CATEGORIES\-EXCL\-MATCH qual\-placement down\.
  - Closed 2026\-10\-16: neither suspect — the search had priced PG's plan cheapest, but the seam declined it
    \(`residual\-hits\-pad`\): `ps\_availqty > \(SubPlan 1\)`, already in PARAM\_EXEC form from the kept\-sublink rebase,
    was refused as partsupp's leaf restriction and held above the root over padded semi\-RHS columns; the fallback
    syntactic plan carried the 100\.00 supplier scan\. `correlatedScalarSublinkBinding` now admits Args/ParParam
    SubPlans \(off under GOOPG\_SCALAR\_UNNEST\)\.
    Design: `docs/design/0100\-0149/m0146\-0203\-param\-subplan\-leaf\-restriction\.md`\.
  Movement: yes — TPCH match 18 → 19 \(Q20\); SF0\.25 84 and SF1 71 byte\-identical\.
- [x] **M0146-0202 — TPC-H Q9: the FK `lineitem\_partsupp\_fk` join selectivity misses its EC\-matched `l\_suppkey` column, so the lineitem probe is estimated at 39 rows where PG has 116871 \(sort\-strategy\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016e/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at a08d4ceb4: sort\-strategy ties qual\-placement at 2 \(TPC-H Q9, SF1 Q18\); differ order picks it\.
    Verdict SHAPE\-DIFF, first divergence depth 0: PG `Sort` over `Finalize HashAggregate` \| goopg `GroupAggregate` over
    `Gather Merge` — downstream of a join estimate 3000x low \(goopg Nested Loop 39 rows from 13034 outer; PG 116871 from
    15640, about 6M/800k per outer row\)\.
  - PG: `get\_foreign\_key\_join\_selectivity` \(costsize\.c:5651\) matches FK columns through equivalence classes
    \(`fkinfo\->eclass\[i\]` against `rinfo\->parent\_ec`, :5718\-5733\), so the applied clause `s\_suppkey = l\_suppkey` of
    the class \{ps\_suppkey, s\_suppkey, l\_suppkey\} covers the FK's `l\_suppkey` column; selectivity 1/800000\.
  - goopg: `superkeyJoinSelectivity` \(joinrelsize\.go\) runs over `oneClausePerEquivClass` \(first explicit clause\);
    `joinKeyPairOf` pairs lineitem with supplier, the FK is not covered, and eqjoinsel multiplies 1/200000 × 1/10000\.
  - First step: port the EC arm of FK matching — a clause whose class contains both the FK column and its referenced
    column covers that FK column \(and is consumed\), as `match\_eclasses\_to\_foreign\_key\_col` /
    `get\_foreign\_key\_join\_selectivity` do; then re\-capture Q9\.
  - Other record, possibly another mechanism: SF1 Q18 \(PG Sort \| goopg Gather Merge under GroupAggregate\)\.
  - Expected instrument: TPC-H PLAN\-PARITY match 17 → 18 \(Q9\); CATEGORIES\-EXCL\-MATCH sort\-strategy down\.
  - Closed 2026\-10\-16: `ecFKPairs` adds equivalence\-class stand\-in pairs owned by the applied clause;
    `fkCoveringClauses` requires each referencing column equated to its own referenced column \(the old cover test
    accepted `l\_suppkey = s\_suppkey` toward an FK onto partsupp\)\. Q9's probe now 97734 rows \(PG 116871\)\.
    Design: `docs/design/0100\-0149/m0146\-0202\-fk\-selectivity\-equivalence\-class\.md`\.
  Movement: yes — TPCH match 17 → 18 \(Q9\); SF0\.25 84 and SF1 71 byte\-identical\.
- [x] **M0146-0201 — TPC-H Q18: PG joins `customer` last by Parallel Hash Join above the `lineitem` probe; goopg hash\-joins it serially before the probe \(join\-method\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016d/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at d1042cfb6: join\-method, sort\-strategy and qual\-placement tie at 2; differ order picks join\-method
    \(TPC-H Q18, SF1 Q68\)\. Verdict SHAPE\-DIFF \[join\-order, join\-method, scan\-type\], first divergence depth 3 under
    Gather: PG `Parallel Hash Join` \(customer, Parallel Hash over Parallel Seq Scan, 301873\.25\) | goopg `Nested Loop`
    \(lineitem probe over a serial Hash Join with `customer`\)\. Totals: PG 404713\.55, goopg 388186\.47\.
  - First step: price PG's shape in goopg — is `\(orders ⋈ agg\) ⋈ lineitem` then Parallel Hash Join `customer` generated
    \(instrument `addPath` at the 4\-rel joinrel\), and how does its cost compare with goopg's winner; compare the
    parallel hash cost terms \(`final\_cost\_hashjoin` with `parallel\_hash`, inner rows / workers\) with PG's\.
  - Also visible: goopg keeps `Subquery Scan on "ANY\_subquery"` \(PG removes the trivial subquery scan\) and prints an
    unqualified Sort Key \(rendering\)\.
  - Other record, possibly another mechanism: SF1 Q68 \(household\_demographics: PG Hash Join, goopg pkey probe; near\-tie\)\.
  - Expected instrument: TPC-H PLAN\-PARITY match 16 → 17 \(Q18\); CATEGORIES\-EXCL\-MATCH join\-method down\.
  - Closed 2026\-10\-16: \(1\) `subtreeHasNoParallelHazard` refused `\*SubqueryScan` as unmodelled, so the ANY\_subquery
    rel was parallel\-unsafe \(DP trace `veto=B2 leaf=other`\) and no joinrel above it had a partial path; it now descends
    the label\. \(2\) The ANY wrapper's consumption was counted by SourceTableIdx in the body's numbering, colliding
    with the outer first FROM item; `SubqueryScan\.anyLink` gives it the link's position 0, so it is stripped\.
    Design: `docs/design/0100\-0149/m0146\-0201\-subqueryscan\-parallel\-safe\-any\-strip\.md`\.
  Movement: yes — TPCH match 16 → 17 \(Q18\); SF0\.25 84 and SF1 71 byte\-identical\.
- [x] **M0146-0200 — TPC-H Q21: PG keeps the EC\-derived `orders\.o\_orderkey = l2\.l\_orderkey` as the semi join's Join Filter over a parameterised `l2` probe; goopg drops it \(qual\-placement\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016c/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at 891d2127f: qual\-placement is the top actionable category \(3: TPC-H Q20, Q21, SF1 Q54\); no SF0\.25
    record in it\. Verdict SHAPE\-DIFF, first divergence depth 3 under Sort; plan shapes otherwise identical\.
  - PG 18 `create\_nestloop\_path` \(pathnode\.c:2703\) drops a join restrict clause only when its `rinfo\_serial` is
    one the parameterised inner enforces \(`get\_param\_path\_clause\_serials`, relnode\.c\)\. The inner enforces
    `l2\.l\_orderkey = l1\.l\_orderkey`; the joinrel's EC clause pairs `orders` with `l2`, a different serial, so PG
    keeps it as a redundant Join Filter\.
  - First step: find where goopg removes the joinrel's EC\-derived clauses that a parameterised inner covers \(it
    likely matches by equivalence class, not by clause identity\) and compare with the serial rule; check the
    clause `generate\_join\_implied\_equalities` picks for this joinrel \(the outer member it pairs with `l2`\)\.
  - Other records, possibly other mechanisms: TPC-H Q20 \(join order, `Hash Right Semi Join`\); SF1 Q54 \(Join
    Filters at different nested\-loop levels\)\.
  - Expected instrument: TPC-H PLAN\-PARITY match 15 → 16 \(Q21\); CATEGORIES\-EXCL\-MATCH qual\-placement down\.
  - Closed 2026\-10\-16: goopg reduced one clause per class only at inner joins, `dropRedundantSemiDerived` removed
    the derived `o\_orderkey = l2\.l\_orderkey` PG picks \(first outer member\), and `ecReduce` was off because the ANTI
    qual joined the class\. ANTI quals now stay out of the classes \(`buildRestrictInfosExcluding`, `antiRighthands`\),
    `ecReduce` = `onlySemiOrAntiJoins`, and a SEMI join is reduced before the derived drop\.
    Design: `docs/design/0100\-0149/m0146\-0200\-semi\-join\-ec\-restrictlist\.md`\.
  Movement: yes — TPCH match 15 → 16 \(Q21\); SF0\.25 84 and SF1 71 byte\-identical\.
- [x] **M0146-0199 — TPC-DS SF1 Q32/Q92: PG memoizes the `date\_dim\_pkey` probe inside SubPlan 1's Nested Loop; goopg probes it bare \(parameterisation\)**
  \(filed 2026\-10\-16 by the M0146\-0014 S9 pass, `analysis/m0146/m0146\-0014/routing\-20261016b/ROUTING\.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census at 7eec2fed8: parameterisation is the top actionable category \(3, tied with qual\-placement; differ
    order breaks the tie\): TPC-H Q2, SF1 Q32, SF1 Q92\. scan\-type \(SF0\.25 Q23, TPC-H Q13/Q16\) re\-routed to the
    M0146\-0019 owner corpus residual \(goopg corpora carry no visibility map; PG's were autovacuumed\)\.
  - SF1 Q32 \(verdict SHAPE\-DIFF, first divergence depth 6, under Nested Loop\): PG `Memoize` \(Cache Key
    `catalog\_sales\_1\.cs\_sold\_date\_sk`, 88 outer rows\) over `Index Scan using date\_dim\_pkey`, Nested Loop 996\.41;
    goopg the bare Index Scan, 88 × 7\.54 → 1009\.29\. SF1 Q92 has the same shape \(depth 7\)\. Both MATCH at SF0\.25
    \(`date\_dim` is the outer there, no Memoize\)\.
  - First step: check whether goopg generates a Memoize candidate for this inner at all, inside the SubPlan's
    join search \(instrument `addPath`, not the cost function\), then compare `cost\_memoize\_rescan`
    \(costsize\.c\) inputs — `ndistinct` of `cs\_sold\_date\_sk` over 88 calls, `est\_entries`, hit ratio — with goopg's\.
  - Other record, possibly another mechanism: TPC-H Q2 \(depth 1 under Sort\)\.
  - Expected instrument: TPC-DS SF1 PLAN\-PARITY match 69 → 71 \(Q32, Q92\); CATEGORIES\-EXCL\-MATCH parameterisation down\.
  - Closed 2026\-10\-16: two causes\. \(1\) `memoizeKeyNDistinct` multiplied raw ndistincts with no per\-relation
    clamp or Yao/Dell'Era restriction term \(`estimate\_num\_groups`, selfuncs\.c:3721\), so 1830 distinct keys over 88
    calls gave hit ratio 0 \(PG: 86 distinct, ~2%\)\. \(2\) The kept\-sublink rebase clones the SubPlan through
    `clonePlanReplacingOuter`, which dropped `InnerMemo`; `rebindInnerMemo` rebuilds it \(also in `remapSourceTableIdx`\)\.
    Design: `docs/design/0100\-0149/m0146\-0199\-memoize\-ndistinct\-restriction\.md`\.
  Movement: yes — SF1 match 69 → 71 \(Q32, Q92\); SF0\.25 84 and TPC-H 15 byte\-identical\.
- [x] **M0146-0198 — TPC-DS Q43: goopg nested-loops `date_dim` after `store_sales ⋈ store` where PG hash-joins `store` after `store_sales ⋈ date_dim` \(join-method\)**
  \(filed 2026-10-16 by the M0146-0014 S9 pass, `analysis/m0146/m0146-0014/routing-20261016/ROUTING.md`\)\.
  Kind: impl
  Parent: M0146-0014
  - Census: join\-method is the top actionable category \(3, tied with scan\-type, parameterisation and qual\-placement;
    differ order breaks the tie\): SF0\.25 Q43, SF1 Q68, TPC-H Q18\. Verdict SHAPE\-DIFF\.
  - SF0\.25 Q43 \(first divergence depth 6, under Sort\): goopg `Seq Scan on store` estimates 1 row under
    `s\_gmt\_offset = \-5` where PG estimates 12; goopg joins `store\_sales ⋈ store` first \(18495 rows\) and nested\-loops
    `date\_dim` \(88 rows out\); PG joins `store\_sales ⋈ date\_dim` first \(Parallel Hash Join, 1110 rows\), then hash\-joins
    `store` \(1062 rows\)\.
  - First step: compare the restriction selectivity of `s\_gmt\_offset = \-5` on `store` \(goopg 1 row vs PG 12 of 12\)
    against PG's `eqsel` / `var\_eq\_const` \(selfuncs\.c\): pg\_stats for `store\.s\_gmt\_offset` on :65438 tpcds025 vs the
    goopg SF0\.25 clone \(MCV list, n\_distinct, null\_frac\), and the numeric constant's comparison against the MCVs\.
  - Other join\-method records, possibly other mechanisms: SF1 Q68; TPC-H Q18 \(PG hash\-joins a parallel\-scanned
    `customer` last, goopg joins `customer` earlier with a serial scan\)\.
  - Expected instrument: TPC-DS SF0\.25 PLAN\-PARITY match 80 → 81 \(Q43\); CATEGORIES\-EXCL\-MATCH join\-method down\.
  - Closed 2026\-10\-16: root cause was the MCV compare, not the join search\. `statLiteralEqual` \(`selectivity\.go`\)
    matched the ANALYZE\-stamped `\-5\.00` against the literal `\-5` byte for byte; `var\_eq\_const` uses `numeric\_eq`\.
    New `statNumericEqual` compares number\-family values by value\. Design: `docs/design/0100\-0149/m0146\-0198\-mcv\-numeric\-equality\.md`\.
  Movement: yes — SF0\.25 match 80 → 84 \(Q33, Q43, Q56, Q60\); SF1 67 → 69 \(Q33, Q60\); TPC-H byte\-identical \(15/22\)\.
    Captures: `analysis/m0146/m0146\-0198/`\. SF1 Q68 and TPC-H Q18 \(other join\-method records\) unmoved — census\.
- [x] **M0146\-0197 — a parameterised index path for EVERY eligible index, not one picked by heuristic** \(filed
  2026\-10\-15 by M0146\-0195\)\. `addOneParameterizedIndexPath` \(`pathparamindex\.go`\) builds one path per
  parameterisation from `pickIndexCoveringLeadingPrefix` \(`nl\_index\_join\.go`: most bound columns, ties → first
  index by name\)\. PG's `build\_index\_paths` \(indxpath\.c\) builds a path for each usable index and `add\_path` keeps
  the cheapest, so two indexes on the same column are chosen between by cost\.
  Kind: impl
  Parent: M0146\-0195
  - Witness: TPC-H on a private clone with compact fk\-index twins — goopg kept the bloated `order\_customer\_fkidx`
    at 7\.07 per probe over a 4\.65 twin until the twin's name sorted first \(`analysis/m0146/m0146\-0195/`\)\.
  - Expected instrument: TPC-H PLAN\-PARITY where an index choice is decided by cost \(Q5's lineitem probe: PG's
    `idx\_lineitem\_orderkey\_fkidx` vs goopg's `lineitem\_pk` skip probe\); CATEGORIES\-EXCL\-MATCH scan\-type\.
  - Optimizer change: landing needs the acceptance arm \(M0146\-0178 escalation\)\.
  - **2026\-10\-15 leading\-prefix half landed** \(design `docs/design/0100\-0149/m0146\-0197\-param\-index\-path\-per\-index\.md`\)\.
    - `addOneParameterizedIndexPath` builds a path per eligible index \(`eligibleLeadingPrefixIndexes`, rule
      `leadingPrefixProbeKeys`\); `tryBuildNLI` keeps its single pick\. Test `TestParameterizedPathsCostEveryEligibleIndex`
      \(old pick: only the large index, 58\.61\)\.
    - Captures HEAD vs patched \(`analysis/m0146/m0146\-0197/`\): TPC-H 12 → 12, SF0\.25 80 → 80, plan text
      byte\-identical on both; fire\-set SF1 A/B: no changed query\. Inert today \(C6\), active once two indexes cover
      a probe\.
    - Gates: units rc=0, tpch\-spotcheck PASS, tpch\-acceptance\-arm PASS \(23 MATCH \+ Q5 EXPECTED\-ERROR\), sf025
      PASS=99, fire set PASS\.
    - Movement: none — plans byte\-identical on both corpora\.
    - Remaining: the skip\-scan arm \(`pickIndexSkipRun`\) still builds one skip path on one index\.
  - **2026\-10\-16 skip\-scan half landed — task CLOSED\.** `skipRunFor` \(rule unchanged\), `eligibleSkipRuns`,
    `addParamSkipPathFor`: a skip path per eligible index, each with its own row/lifetime guards\. Test
    `TestParameterizedSkipPathsCostEveryEligibleIndex` \(old pick: only the large index, 66\.52\)\.
    - Captures vs the M0146\-0178 baselines \(`analysis/m0146/m0146\-0197/skip/`\): plan text byte\-identical on TPC-H
      \(15/22\) and SF0\.25 \(80/99\); fire\-set SF1 A/B no changed query\.
    - Gates: units rc=0, tpch\-spotcheck PASS, tpch\-acceptance\-arm PASS \(24 MATCH\), sf025 PASS=99, fire set PASS\.
    - Residual \(ledger\): the legacy `tryBuildNLI` rewrite keeps a single pick\.
Movement: none — plans byte-identical on TPC-H and SF0.25 (inert on today's corpora)
- [ ] **M0146-0178 — index-only-scan heap-fetch term and int4 index density (TPC-DS Q24)**
  (filed 2026-10-13 by the owner from the M0146-0014d sweep finding F3). Q24 SF0.25 elects a Parallel
  Index Only Scan on `store_returns_pkey` (1441.32, no heap-fetch I/O) where PG keeps a Parallel Seq
  Scan (1653.62); PG's index-only scan must carry ~212 more, i.e. heap fetches (`cost_index`
  allvisfrac, costsize.c). Full index-only scans on `item_pkey` / `customer_pkey` price +101 / +255
  higher in PG. Separately, goopg's int4 pkeys hold ~458 tuples per page, past PG's maximum for
  16-byte tuples (40 vs ~52 pages for item_pkey).
  Kind: impl
  Parent: M0146-0014
  - First step: read-only `relpages, relallvisible` for store_returns, item, customer on :65438
    (tpcds025) and a private SF0.25 clone; PG `SET enable_seqscan=off; EXPLAIN SELECT sr_item_sk,
    sr_ticket_number FROM store_returns`; then decide whether goopg's relallvisible (VACUUM VM
    accounting) or the index density differs.
  - 2026\-10\-13 slice 1 landed \(design `docs/design/0100\-0149/m0146\-0178\-btree\-rightmost\-split\-fillfactor\.md`,
    evidence `analysis/m0146/m0146\-0178/`\)\. Three mechanisms, not one:
    - Heap\-fetch term: PG\'s heaps keep trailing bulk\-extension pages that are not all\-visible
      \(store\_returns 1232 pages / 1196 all\-visible; goopg 1196 / 1196\), so PG\'s `allvisfrac` is below 1\. This is
      the RELPAGES owner residual\.
    - Index pages: `catalog\.IndexRealPages` resolves a DBOid\-0 index under `base/1`, while the file is in
      `base/5`, so the planner uses a width estimate \(item\_pkey 40 pages; the real file is 90, PG 52\)\. This is the
      same mismatch M0145\-0029 slice 5 fixed for `relallvisible`\.
    - Index density, fixed: `\_bt\_findsplitloc` splits a rightmost page at the fillfactor \(leaf 90, internal 70\);
      goopg split evenly\. Ascending inserts now build PG\'s page counts \(18000 int4 keys: 52, was 90; 100000:
      276, was 494\)\. Test `TestAscendingInsertsSplitRightmostAtFillfactor`\.
    - No plan moved: the gate clusters keep their old index files\.
    - Remaining, in order: \(1\) owner reload of the goopg TPC\-DS clusters, so their indexes are rebuilt under the
      new rule — **DONE \(owner, 2026\-10\-15\)**: SF1 was reloaded 2026\-10\-13 \(`eef67e635`, post\-fillfactor\)
      and SF0\.25 `data\-sf025` was re\-initialised and reloaded 2026\-10\-15 from a clean\-HEAD binary
      \(`32fe610c7`, ancestor check on `d5d6f8c52` PASS\)\. Verified on\-disk: item\_pkey 52 = PG 52,
      customer\_pkey 276 = PG 276, date\_dim\_pkey 202 = PG 202 \(were 90 / 494 / —\); heap `relpages` now
      equal PG `tpcds025` on every probed table \(item 1284, customer 2872, store\_sales 12936,
      store\_returns 1232, date\_dim 1424, catalog\_sales 9416, time\_dim 1408, web\_sales 4744\)\.
      **This corpus reload is recorded here — it invalidates `routing\-20261014j` under S9\(1\); the next
      census pass must re\-capture\.**; \(2\) slice 2, resolving `IndexRealPages` through the catalog\'s database
      and threading `searchCtx\.cat`
      into `estimateIndexGeometry` — done earlier it would price today\'s half\-empty files; \(3\) the heap
      residual\.
    - Gates: units, postmaster, tpch\-spotcheck, sf025 PASS=99, TPC\-DS fire set PASS \(no change\), TPC\-H
      acceptance arm PASS, regress A/B 5 index suites identical, nbtree `\-race`\.
  - **2026\-10\-15 slice 2 implemented and PARKED as a patch** \(`analysis/m0146/m0146\-0178/slice2/slice2\.patch`,
    applies to `d7c05acde`; design doc §"Slice 2"\)\.
    - Cause: an index in the `postgres` database carries the namespace key DefaultDBOid \(1\); its file sits under
      `base/5`\. `catalog\.IndexRealPages` took the key literally, missed the file, and `estimateIndexGeometry`
      priced the width guess\. `get\_relation\_info` sizes the opened relation \(plancat\.c:466\)\.
    - Change: `catalog\.IndexRealPagesIn\(cat, idx\)` via `cat\.IndexRelFileNode`; the catalog threaded into
      `estimateIndexGeometry`, `indexPagesForPath`, `seqWinsEqualityProbe`; the pg\_class view maps its namespace key
      to the storage database\. Two tests\.
    - Measured on the reloaded SF0\.25 corpus \(HEAD vs patched captures in `slice2/`\): match 80 → 80;
      CATEGORIES\-EXCL\-MATCH identical except parallelism 10 → 9 \(Q77\); changed index\-scan nodes 45 closer to PG,
      4 farther; plan totals 45 closer, 36 farther; Σ\|log goopg/PG\| 11\.940 → 11\.899\.
    - Gates on the patched tree: units PASS, tpch\-spotcheck PASS \(Q12=2, Q13=35\), sf025 PASS=99, fire set PASS
      \(introduced=none\)\. tpch\-acceptance\-arm: 23 MATCH \+ Q5 BOTH\-ERROR → VERDICT FAIL\.
  - **ESCALATION 2026\-10\-15 \(gate unpassable, G1\) — marked `\[!\]`, OWNER ACTION NEEDED\.**
    - The 2026\-10\-15 \(b\) realign pinned TPC\-H Q5 as an error in `bench/tpch/baseline\-digests\.txt` \(it runs past
      the 600s cap on goopg's divergent plan\)\. `cmd/tpch\-runner` digestdiff fails a both\-arms\-identical error as
      `BOTH\-ERROR` by design \(`TestDiffBothErrorDoesNotPass`\), so `tpch\-acceptance\-arm` FAILs for EVERY tree, HEAD
      included\. `\.githooks/commit\-msg` requires its PASS stamp for any non\-test \.go under internal/optimizer,
      internal/planner, internal/executor or a cost/stat/selfuncs path — i\.e\. all banner item\-3 implementation work\.
    - The loop may not edit `\.ralph/gate\-exceptions\.md`, the hook, or weaken digestdiff\. Owner options: \(a\) a
      gate\-exceptions row for this task \+ gate; \(b\) a digestdiff rule accepting an error the baseline itself pins;
      \(c\) re\-pin Q5 once it completes \(raise the cap, or fix Q5's plan first\)\.
    - **RESOLVED 2026\-10\-15 \(owner\) — option \(b\), narrowed to a baseline pin.** digestdiff gained an `XFAIL`
      status: a baseline line `Q5: XFAIL — <detail>` passes only when the run reproduces the identical error
      \(`EXPECTED\-ERROR`\); a different error is `ERROR\-DIFF`, a recovered OK is `XFAIL\-RESOLVED` \(still a gate
      failure — forces re\-pin, so the pin cannot outlive its cause\), and `XFAIL` in a run log is
      `XFAIL\-IN\-RUN`\. `bench/tpch/baseline\-digests\.txt` Q5 is now an XFAIL pin\. \(a\) was inert —
      gate\-exceptions waive only `SKIP\-BLOCKED` stamps and the arm returned FAIL; \(c\) deadlocks because the
      planner fix for Q5's plan itself needs the arm\. Verified: the parked slice\-2 arm output re\-diffed as
      23 MATCH \+ 1 EXPECTED\-ERROR → `VERDICT: PASS`\.
    - Expected movement once unblocked: CATEGORIES\-EXCL\-MATCH parallelism 10 → 9 at SF0\.25 \(Q77\); index\-scan
      node costs at PG's for 45 nodes\. Resume: `git apply analysis/m0146/m0146\-0178/slice2/slice2\.patch`, re\-run
      the arm, commit\.
  - **2026\-10\-15 slice 2 LANDED** \(the parked patch, unchanged; design §"Slice 2"\)\.
    - Gates on the staged tree, after the owner's `:65433` reload finished: units rc=0, tpch\-spotcheck PASS \(Q12=2,
      Q13=35\), tpch\-acceptance\-arm PASS \(23 MATCH \+ Q5 EXPECTED\-ERROR\), sf025 PASS=99, fire set PASS
      \(introduced=none\)\.
    - Parity \(measured on the patched binary, `slice2/` and `analysis/m0146/m0146\-0195/m0195\-s2\-asis\*`\): SF0\.25 match
      80 → 80, CATEGORIES\-EXCL\-MATCH identical except parallelism 10 → 9 \(Q77, inside the ±3 band\); TPC-H 12 → 12\.
      45 of 49 changed index\-scan nodes moved to PG's cost\.
    - Movement: none — parity held; the only category move \(parallelism 10 → 9\) is inside the ±3 noise band\.
    - Open: the two parallel worker\-count sites and `catalog\.TableRealPages` still use the default\-database lookup
      \(no catalog reaches them\); the heap trailing\-page term is the RELPAGES owner residual\.
  - **2026\-10\-16 worker\-count slice landed** \(design §"Worker\-count sizing"\)\.
    - `catalog\.TableRealPagesIn`; the upper\-rel split \(`upperSplitWorkersForScan`\) reads table and index sizes through
      the statement's catalog \(`upperRels\.cat`\); the post\-pass reads index pages through
      `ParallelSettings\.BlocksForIndex`, wired in both postmaster dispatch paths\.
    - Tests `TestTableRealPagesInUsesCatalogDatabase`, `TestUpperSplitWorkersSizeOnCatalogDatabase`\.
    - Captures vs the M0146\-0197 head arms and a fresh TPC-H HEAD arm on the reloaded corpus: plan text byte\-identical
      on TPC-H and SF0\.25; fire\-set SF1 A/B no changed query\. Inert today\.
    - Gates: units rc=0, postmaster tests, tpch\-spotcheck PASS, tpch\-acceptance\-arm PASS \(24 MATCH\), sf025 PASS=99,
      fire set PASS\.
    - Movement: none — plans byte\-identical\. Remaining: only the RELPAGES heap trailing\-page term \(owner residual\)\.
