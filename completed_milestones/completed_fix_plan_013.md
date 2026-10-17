# Banner history — archived 2026-10-15

Pre-slim content of `.ralph/fix_plan.md`'s `## Current Priority` banner:
the item-by-item narratives (discharged items 0-2a/4-6/9 and the long
item-3 history), every OWNER DECISIONS block, the M0142-0008 UNFROZEN
detail, and the LINEAGE-BASELINE pin notes. The pin TOKEN LINES and the
condensed items still live in the banner.

## Banner items 0-10 (pre-slim text)


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
   per-query write-up. **Children 0003a and 0003b are closed** (owner disposition 2026-09-23:
   0003a superseded into M0146-0008, 0003b discharged by
   M0145-0004/0004a) — their wall
   is the resolver-time lowering, fixed inside item 3's M0145-0003/0004.
2a. **Post-cutover correctness and gate-red batch** (owner decision
   2026-09-25 — S2 placement; see OWNER DECISIONS 2026-09-25 below).
   The original six drained 2026-09-25/27: **M0145-0008r**, **0008s**,
   **0008m**, the freeze-WAL record, **M0146-0015** (+ its fix
   M0146-0015a) and the TestE2E conf-template item are all `[x]`, and
   `TestPort_RegressSuite` closed as recorded. The explicit-opclass
   index restart defect (M0146-0015e) landed 2026-09-27 (`cf7c5397b`),
   draining the first batch. The second batch drained 2026-10-02/03:
   **M0143-0011** and **M0146-0039, 0041, 0040, 0034, 0032, 0033** are
   all `[x]`; **M0146-0035** is parked `[!]` on non-reproduction.
   The third batch drained 2026-10-04/05: **M0146-0047, 0048, 0044,
   0045, 0046, 0043** are all `[x]`. The fourth batch drained
   2026-10-07/10: **M0146-0056, 0058, 0050, 0059, 0062, 0051, 0052,
   0053, 0054, 0047b** are all `[x]`, and **M0146-0055** closed `[x]`
   under OWNER DECISIONS 2026-10-12 (its open descendants — 0084/0085/
   0086 and the three S2 findings its escalation held — inherit item
   2a's rank through it). **Live members (owner decision 2026-10-12 —
   fifth S2 batch, amended 2026-10-12 evening; see OWNER DECISIONS
   2026-10-12): M0146-0155 [x] → M0146-0156 → M0146-0157 →
   M0146-0158 → M0146-0089 → M0146-0152 → M0146-0126 →
   M0146-0125**, in that order. (0157/0158 also inherit rank via
   Parent: M0146-0155; the explicit naming fixes their sequence.) The three isolation
   specs (EvalPlanQual, ReadWriteUnique4, TemporalRangeIntegrity) are
   SSI-semantics / scheduling divergences and keep M-NIGHTLY order —
   no bump; the tuplelock-upgrade reopen keeps M-NIGHTLY order on the
   same reasoning (a scheduling-flake signature, not wrong results).
   **The fifth batch drained 2026-10-12/13 — all named members are
   `[x]`, and item 2a is DISCHARGED (owner decision 2026-10-13 — see
   OWNER DECISIONS 2026-10-13; the current focus is TPC-H/TPC-DS plan
   parity, item 3 onward).** ~~Descendants filed under this item's
   tasks inherit item 2a's rank (explicit owner placement; S7's
   lowest-candidate tiebreak does not re-queue them under item 3).~~
   **Rank inheritance ended 2026-10-13:** the loop-filed descendants
   0160/0161/0162/0163/0164/0165/0166 carry no item rank — the
   S7 correctness-carve-out below queues them under the unnamed-task
   fallback (below item 10). New correctness findings filed under any
   task keep being filed (record, don't drop) but take no rank without
   explicit owner placement.
3. **M0145 jointree-first planner** (flow unification, owner decision
   2026-09-20 — fix the medium-level route divergence documented in
   `METHODOLOGY4/plan-flow-medium-abstraction.md` at the boundary, not per
   plan: a jointree-level IR built before lowering, sublinks pulled up into
   it, one DP pass over it, one Path→Node lowering at the end; the new
   pipeline is selectable behind `GOOPG_JOINTREE_PIPELINE=1` and the legacy
   pipeline stays the default until the M0145-0008 cutover deletes it —
   **the flip HAS landed 2026-09-24/25: the jointree pipeline is the only
   pipeline and the knob is retired**).
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
   family; likewise a re-evaluation trigger, not an auto-admit) →
   **M0145-0011** (measured re-evaluation of the derived-input
   blockers — knob-arm private evidence only) → **M0145-0012**
   (retire the goopg-only `rows<=1` CTE fallback once `derived >=
   guard effect` is demonstrated or made to hold) →
   **M0145-0013** (seam admission for pulled `*CTEScan` leaves —
   the `pulled-leaf-not-scan`/`flat-leaf-not-scan` class E2
   measured, 30 fires) → **M0145-0014** (nested-sublink pull-up
   recursion — `any-nested-sublink`, 6) → **M0145-0015** (residual
   census + OR/NOT-position sublinks — PG's actual reach only) →
   **M0145-0016** (`semianti-not-tail` leaf reorder — verify
   subsumption by 0005's slice (a) first) → **M0145-0017**
   (never-reach census — sublinks outside top-level WHERE conjuncts)
   → **M0145-0018** (`outer-over-derived` firewall relaxation —
   owner GO 2026-09-21; LAST in the chain: only after 0013 lands and
   a fresh E1 re-verification keeps the relaxed plans clean). The
   Q78 `outer-over-derived` firewall remains a hard constraint on
   every pull-up/flattening task UNTIL M0145-0018 executes it.
   **OWNER GO 2026-09-22** (answer to the M0145-0001 lineage
   escalation and the M0145-0018/0012 escalations): CONTINUE —
   sequence the measured downstream walls first (M0145-0009, then
   **M0145-0019** NL-costing for derived inners — 0018's option
   (c) — then **M0145-0020** `examine_simple_variable` CTE arm,
   which is 0012's named prerequisite). When 0019 lands, 0018's
   fresh E1 re-verification re-runs and 0018 executes the
   relaxation only if it passes at both scales; the
   flow-completion chain (0004 → 0005 → 0007 → 0008) resumes
   after that, still under the firewall constraint until 0018
   executes it. Harness
   additions **M0145-0021** (SF1 fire-set gate template),
   **M0145-0022** (shape/time regression channel) and
   **M0145-0023** (flow-convergence instrument) may run any time;
   rationale `tmp/progress-planner-rewrite-260922/01-progress-assessment.md`
   (local, gitignored). **After the M0145-0008 flip lands, this item's
   continuation is M0146** (`docs/milestones/0146-post-cutover-plan-parity.md`,
   task entries at the end of this file) — the executor-substrate /
   election / statistics burn-down, sequenced by M0146-0001's
   re-baseline census. The flip has landed, so M0146 is live — but item
   2a (above) ranks ahead of it until the batch drains.
   **M0146-internal ordering (owner decision 2026-10-02 — see OWNER
   DECISIONS 2026-10-02):** M0146-0005's scope is re-declared as
   STRUCTURAL plan-shape burn-down (join-order, join-method, scan-type,
   parameterisation, parallelism, sort/aggregation, qual-placement);
   EXPLAIN text-identity / `rendering`-category slices file under
   **M0146-0042** instead — no new `0005<x>` ids for text-only work.
   **M0146-0009 (statistics/cardinality) subtasks are interleaveable:**
   selectable under this item whenever the slice's first-divergence is
   statistics-driven, without waiting for 0005 to close. M0146-0006
   stayed sequenced after 0005, and 0005 could close `[x]` once every
   remaining first-divergence record was routed to a named task
   (0009/0012/0019a/0042/…) — **met: 0005 closed `[x]` 2026-10-04
   (slice 116) and 0006 (Incremental Sort election) has landed
   (`01739f8d1`).** **M0146-0049 is re-opened by the 2026-10-06 pin
   below** — its live children are 0049e, 0049f and the probe-cost
   finding the budget kept unfiled. **M0146-0068 (the B8
   `indexProbeCostMultiplier` corpus A/B) is first among new item-3
   work** — it gated M0145-0008ag, M0146-0009p and M0146-0060.
   **Resolved 2026-10-12 (OWNER DECISIONS): Option A — retire the
   multiplier.** The retirement impl is first among new item-3 work;
   it unblocks M0145-0008ag → 0009p/0060 (0060's other blocker 0061 has
   landed). The probe-runtime residual is M0146-0088's ledgered ~1.9x.
   **Chain landed 2026-10-13:** M0146-0159 `[x]` (`de45f8a6f`),
   M0145-0008ag `[x]`, M0146-0009p `[x]` (`b27c58316`), M0146-0060
   `[x]` (`4557c69d4`), plus M0146-0170/0169/0171/0172/0174 and seven
   M0146-0042 slices — the day's queue drained. **Next item-3 work
   (OWNER DECISIONS 2026-10-13): the parity-closure sweep's five
   mechanism findings, filed as M0146-0176 → 0177 → 0178 → 0179 →
   0180** (each a one-to-two-loop slice; 0176 is a plan regression
   since M0146-0130). M0146-0005dp re-opened alongside (its 0049/0049e
   gate landed). M0146-0014 re-opened under the pin above — new
   findings from its sweeps file under it again.
   **Next item-3 work (OWNER DECISIONS 2026-10-15): M0146-0178
   slice 2** — `IndexRealPages` resolving an index's real file under
   the catalog's database, `searchCtx.cat` threaded into
   `estimateIndexGeometry`. Its owner gate cleared 2026-10-15 (the
   SF0.25 corpus reload — see the task entry); 0176/0177/0179/0180
   and the S9-filed 0190–0194 are all `[x]`.
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
   **Discharged 2026-10-06** — the hold's resume task M0146-0006 landed
   (`01739f8d1`, arm on by default; S2b-9 via M0146-0005bp) after
   M0146-0005 closed, and M0141-S7 is closed `[x]` (OWNER DECISIONS
   2026-10-06); its exec-d residual stays a recorded ledger deferral.
7. **M0140-0006a → 0006b → 0006c** (partial-Append), then
   **M0140-0007** (`Parallel Hash` from a partial inner — M0137-0019
   family A's floor).
8. **M0142-0005**, then **M0142-0016c**, then **M0142-0003i** (0003i only after
   P0-E5 and P0-E6 are `[x]`).
9. **M0143 remaining tasks**, top to bottom (includes the parser failures).
10. M-NIGHTLY open items, then the pre-existing milestones
   (M0119 → M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110).
   (Reachable only after the S9 pass above files nothing.)


## LINEAGE-BASELINE pin notes (token lines remain in banner)

FROZEN-PREFIXES:
(the M0142-0008 chain was UNFROZEN by owner decision 2026-09-20; see below —
the empty prefix list is what makes them selectable again)

LINEAGE-BASELINE: M0145-0001 M0145-0003 M0145-0006 M0145-0014 M0145-0015 M0145-0016 M0145-0017 M0145-0004a M0145-0005 M0145-0007 M0145-0027 M0145-0028 M0145-0004 M0145-0026 M0145-0026a M0145-0029 M0145-0030
(owner re-pin 2026-09-22: the six completed M0145-0001 descendants listed
above are pinned OUT of the S4 last-5 lineage-budget window — the count
restarts from the re-open point. This is NOT a root exemption: every
completion that lands from now on still accumulates toward a fresh budget.
Chosen over exempting the root after M0145-0004's LINEAGE NOTE recorded that
the guard cannot see the owner re-open.)
(owner re-pin 2026-09-23, second: M0145-0004a/0005/0007/0027/0028 are added
to the pin. Their `Movement: none` streak is an instrument artefact — every
movement instrument measures the DEFAULT arm, which none of them touches;
the M0145-0008 flip is the first descendant that can move an instrument.
Blocking the lineage at the flip is the guard's blind spot, not a signal of
inert work. Still not a root exemption: post-flip completions accumulate
toward a fresh budget and the instruments DO see the default arm then.)
(owner re-pin 2026-09-24, third, delegated — resolves the same-day
M0145-0001 escalation, option (a) plus a bounded exemption:
M0145-0004/0026/0026a/0029/0030 are added to the pin — the same pre-flip
instrument-artefact class the second re-pin adjudicated. GO on
M0145-0008b/c/d and on the M0145-0008 legacy-deletion slices.
**Bounded exemption**: while any M0145-0008 legacy-deletion slice remains
open, the loop may APPEND newly completed M0145-0001-descendant ids to
this LINEAGE-BASELINE line itself — owner-delegated, this line only, no
other banner edits — so deletion-era `Movement: none` streaks need no
further owner round-trip. The delegation ends when the deletion slices
close; completions after that accumulate toward a fresh budget.)

LINEAGE-BASELINE: M0142-0005 M0142-0005b M0142-0005c M0142-0005d M0142-0005e M0142-0005f
(owner pin 2026-09-23, delegated: required so M0146-0004 — the per-worker
Memoize re-home — can carry `Parent: M0142-0005`. The five pinned
descendants are the closed B8/`indexProbeCostMultiplier` recon chain; their
Movement:none reflects that the chain's honest exit is a corpus action
outside the loop's reach, not inert planner work. The Memoize resume
measures on M0146-0001's post-flip baseline.)

LINEAGE-BASELINE: M0144-0002 M0144-0011a-2 M0144-0011a-3 M0144-0011b M0144-0011b-1 M0144-0011c
(owner pin 2026-09-23, delegated: required so M0146-0010 — the Materialize
re-home — can carry `Parent: M0144-0011c`. The pinned five are the
M0144-0011 campaign's Movement:none tail; the campaign's only movement
(0011a) falls back inside the window, and the campaign itself is closed —
superseded into M0146 — not restarted.)

LINEAGE-BASELINE: M0146-0009 M0146-0009c M0146-0009d M0146-0009e M0146-0009f M0146-0009k
(owner pin 2026-10-03, delegated — resolves the exhaustion the guard's
new `\-`-normalisation surfaces on this root: the five pinned
completions are the instrument-artefact/recon class — 0009c and 0009f
are ea-ratchet classification recons, 0009d hardened the ea-ratchet
instrument itself (vacuous-PASS fix), 0009e left no Movement line, and
0009k landed the searched-join-rows sizing with the instruments
honestly unchanged. Same adjudication as the 2026-09-23 second
M0145-0001 re-pin. Not a root exemption: later completions accumulate
a fresh budget, so the interleaveable stats children still owe a real
Movement line.)

LINEAGE-BASELINE: M0146-0049 M0146-0049c M0146-0049d M0146-0049d1 M0146-0049d2 M0146-0049d3
(owner pin 2026-10-06, delegated — resolves the 2026-10-03 S4
escalation on this root. The five pinned completions each landed real
substrate — a parameterised Append over a flattened UNION ALL leaf,
ExecHashJoin's empty-inner exit and outer prefetch, an uncorrelated
CTE under LATERAL materialised once (which also fixed a wrong result),
and parameterised hash join paths bound into a lateral nested loop —
and their instruments stayed flat only because each witness still
diverged upstream: Q54 at its Parallel Seq Scan outer (0049e, still
open), Q95 unreachable until M0145-0008ac's pull-up (landed
2026-10-03, `c317b037b`). Not a root exemption: post-pin completions
accumulate a fresh budget. Re-opening the root lets the unfiled
probe-cost finding it names land as a child.)

LINEAGE-BASELINE: M0146-0005 M0146-0111 M0146-0112 M0146-0113 M0146-0114 M0146-0115
(owner pin 2026-10-12, delegated — resolves the 2026-10-09 second S4
exhaustion on this root. The five are regress-fidelity slices
(constant-FALSE/dummy-rel pruning, DISTINCT-over-pinned-keys, correlated
min/max, self-join removal) — real PG-fidelity ports whose only measured
effect is regress diff counts, which were not a recognised Movement
instrument until today. Not a root exemption: later completions
accumulate a fresh budget. Unholds the text-identity campaign
M0146-0042 and M0146-0067, which the loop had parked as proxies.)

LINEAGE-BASELINE: M0146-0007 M0146-0119 M0146-0123 M0146-0124 M0146-0129 M0146-0145
(owner pin 2026-10-12, delegated — resolves the 2026-10-10 second hold
on this root. Same instrument-artefact class: five regress-witnessed
inline_cte/pull-up fidelity slices. The 2026-10-06 reopen was spent as
designed — post-pin completions accumulate a fresh budget. If the next
five are again all regress-only `none`, the family should yield rather
than re-pin; with `regress` now a Movement instrument, honest movement
lines should keep this from recurring.)

LINEAGE-BASELINE: M0146-0055 M0146-0076 M0146-0077 M0146-0080 M0146-0081 M0146-0082
(owner pin 2026-10-12, delegated — resolves the loop-#130 S4 exhaustion
on this self-rooted trigger/COPY family. The five are trigger/PLpgSQL
correctness fixes with no TPC witness — `Movement: none` is structural
there. Pinning empties the window so the escalation's three held S2
findings (partition trigger clones, `regclass IN (SELECT …)`, trigger
durability) can file, and open children 0084/0085/0086 proceed under
inherited item-2a rank. Not an exemption: later completions accumulate
a fresh budget.)

LINEAGE-BASELINE: M0122-0015 M0122-0015b M0122-0015c M0122-0015d M0122-0015e M0122-0015g
(owner pin 2026-10-12 evening, delegated — resolves the 2026-10-12 S4
exhaustion on this client-tool-porting family. Its children carry real
ids, so the pin is safe (unlike M0119-0006 / M0122-0008, which stay
parked on pseudo-id grounds). With `regress` a Movement instrument as
of today this family can claim honest movement — 0015g itself moved
regress foreign_data 2459→2428. Not an exemption: later completions
accumulate a fresh budget.)

LINEAGE-BASELINE: M0146-0014 M0146-0148 M0146-0014c M0146-0149 M0146-0150 M0146-0014d
(owner pin 2026-10-13, delegated — resolves the 2026-10-13 S4
escalation on the parity-closure sweep. The five are routing recons —
their movement lands in the tasks they file, so `Movement: none` is
structural, not inert. Pinning empties the window so the sweep's five
unfiled first-divergence findings (0014d-F1..F5) can be filed as
M0146-0176..0180. Not an exemption: later completions accumulate a
fresh budget.)


## OWNER DECISIONS history

OWNER DECISIONS 2026-09-22 (progress-report review
`tmp/progress-planner-rewrite-260922-2/01-progress-assessment.md`; details
in each task's entry):
- **M0145-0018: EXECUTE — criterion 1 waived as measured-benign.** The
  task is `[ ]` and executable: remove `problemPairsOuterWithDerived`
  incl. Semi/Anti arms, delete `GOOPG_DERIVED_FIREWALL`, full default-arm
  gates + the now-enforced fire-set gate.
- **M0145-0020d: resolved (b)** — G4 widened for UNMATCHED-IN-PG-only
  repins; baseline re-pinned to the 20260922 census dir.
- **M0145-0012 stays `[!]`**; next step is M0145-0024 (Q74 residual
  recon + arm A/B). Do not remove the `rows<=1` arm on 0020a alone.
- **M0145-0021a: enforced at commit level** for optimizer/planner +
  cost/stat/selfuncs paths (see its entry and AGENT.md); `tpch` stays
  opt-in.
- New filings: **M0145-0004a** (whole-chain UNION ALL flattening, the
  lineage-refused residual), **M0145-0024**, **M0145-0025** (TPC-H
  parity baseline re-take on the pinned-seed lane).

OWNER DECISIONS 2026-09-23 (progress-report review
`/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/01-progress-assessment-260923.md`,
delegated scope M0137–M0145; details in each task's entry):
- **M0145-0001 second lineage exhaustion: resolved — option (a).**
  `LINEAGE-BASELINE` re-pinned (0004a/0005/0007/0027/0028 added); the
  Movement:none streak is an instrument artefact (instruments measure
  the default arm). GO on the flip's remaining blockers: groups I/B are
  filed as **M0145-0029** (one-relation index-path coverage) and
  **M0145-0030** (behavioural-triage adjudication) — both land BEFORE
  the M0145-0008 flip commit; option (b) (pin tests to legacy and flip
  over known gaps) is rejected — the group-I class is exactly the
  route-borne bypass loss that regressed Q17/Q20.
- **M0145-0010: CLOSED — premises refuted by measurement.** Option (c)
  of its design doc: the substantive legality work (scope c/d, shared
  jointype predicates) already landed; scope (b)'s consumer
  (partitionwise joins) does not exist; scope (a)'s consumer is 2
  lateral fires, ledgered with a post-cutover re-census trigger
  (M0146-0011).
- **M0140-0007: re-scoped (option a) → M0146-0002** (fidelity port:
  build inside Gather, shared publication, barrier, error propagation).
- **M0141-S7: HELD (option b)** — Incremental Sort stays parked until
  the input-candidate divergence moves; resume = M0146-0006, sequenced
  after M0146-0005.
- **M0144-0003a / 0003b / 0011: CLOSED — superseded into M0146**
  (0003a → M0146-0008, 0003b discharged by M0145-0004/0004a, 0011's
  Materialize hypothesis → M0146-0010, Q8 join-order residue →
  M0146-0005).
- **M0137-0019a / M0142-0005: stay `[!]`** — parked on executor
  capabilities that are now named M0146 members (row-emitting
  PartialAgg = M0146-0003; per-worker Memoize = M0146-0004).
- **M0145-0012 stays `[!]`** per the 2026-09-22 direction — unchanged;
  M0145-0024 is its live recon.
- **New milestone filed: M0146 — post-cutover plan parity**
  (`docs/milestones/0146-post-cutover-plan-parity.md`), the
  executor-substrate / election / statistics burn-down the M0145 flow
  parity makes reachable. Its tasks are gated on the M0145-0008 flip
  landing — do not select them before it.

OWNER DECISIONS 2026-09-24 (progress-report review
`/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/01-progress-assessment-260924.md`,
delegated; details in each task's entry):
- **M0145-0008 flip unblock (a) — `indexProbeCostMultiplier`: option
  (c), sanction a test-only setter.** The multiplier is load-bearing
  (`mult = 1` elects PG-shaped NL plans that run 2–3x slower — the
  documented B8 trade-off), so (a) retiring it is the measured-regression
  exit and stays parked. (b) re-baselining the two executor tests to the
  bitmap shape would silently drop executor coverage of the index-only /
  index-probe path. A test-only setter (same mechanism class as
  `pinLegacyPipeline`) keeps both shapes coverable: tests pin the value
  they exercise.
- **M0145-0008 flip unblock (b) — executor legacy-pin export:
  sanctioned.** Export `pinLegacyPipeline` as a shared testutil helper
  for the executor package's legacy-only tests. It pins the SAME
  env-knob mechanism — it is not a second pipeline-selection mechanism
  (G8's prohibition is production selection paths), and precedent
  `3b8b771f6` already landed the optimizer-side pin. Keeps
  `TestParallelNLIJointypeIdentity/semi|anti`,
  `TestSubqueryUnnestKillSwitch`, `TestHashedInMixedKindFallsBack`
  covered until the legacy-deletion slices remove them.
- **M0145-0012: option (a) — retire the `rows<=1` CTE fallback arm for
  plan parity.** M0145-0024 measured that PG 18.3 collapses Q74
  identically; the arm is now a goopg-only divergence FROM PG's elected
  plan, and the task's own "reproduces the collapse class = automatic
  no-go" criterion is superseded — the collapse class IS PG's plan.
  Timing note: post-removal goopg (29.61 s) still beats PG (53.49 s) on
  the same plan, so this is parity-convergent, not a performance
  concession. Same gate discipline as filed: knob-arm evidence first,
  full gates before default-arm removal.
- **M0141-S2b-17a: G4 extended — repin "introduced but shown PG-shared"
  keys.** Both NEW findings (Q16, Q95) measured PG-shared misestimates;
  a permanently red ratchet gate trains loops to ignore it. The G4 text
  in `AGENT.md` is amended accordingly.
- **S7 amended (design call from the same report §5.3):** unlisted tasks
  filed under a banner-named milestone/family inherit that item's rank
  **and its gates/constraints** — text in this section's Selection rules.
  Families the banner names nowhere (in-progress milestones absent from
  item 10's list, e.g. M0094/M0096/M0097/M0123/M0132) stay below item 10
  by owner confirm — a later re-rank can name them explicitly.
- **Banner re-rank (item 10 split / pre-flip M0146 executor-substrate):
  NOT NEEDED** — the two flip unblocks above restore the M0145-0008
  critical path; M0146's flip gate stays as filed.
- **M0145-0001 third lineage exhaustion (ralph2 loop #49): resolved —
  option (a) + bounded exemption.** The five in the window
  (0004/0026/0026a/0029/0030) are pre-flip completions — the same
  instrument-artefact class the second re-pin adjudicated — so
  `LINEAGE-BASELINE` is re-pinned again; GO on M0145-0008b/c/d (ordered
  0008b → 0008c → 0008d) and on the legacy-deletion slices. Until those
  slices close, the loop may self-append completed M0145-0001
  descendants to the root's `LINEAGE-BASELINE` line (delegated — that
  line only).
- **work_mem 512MB→4MB: RESOLVED 2026-09-24** — every measurement
  cluster on both engines carries `work_mem = 512MB` explicitly in
  `postgresql.conf`, no session `SET`; the remaining BootVal-fidelity
  item is parked per its task entry.

OWNER DECISIONS 2026-09-25 (post-cutover wrong-results review
`/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/00-after-cutover-teport.md`,
delegated; details in each task's entry):
- **S2 batch → new item 2a.** Four open S2 defects, ordered by
  reachability: 0008r → 0008s → 0008m → freeze-WAL. M0146-0015's
  cutover-causality bisect and the TestE2E conf-template fix join the
  same batch; the three isolation specs keep M-NIGHTLY order.
- **M0145-0008ac: option (i) — sequence parameterised-path work
  first.** Q95's SF1 timeout is NOT accepted as a G5 coverage loss.
  The task stays `[!]` until parameterised inner paths through a join
  (M0146-0012 / M0145-0010 family) land, then re-apply the preserved
  patch and re-run the fire set at both scales. The same blocker
  gates M0145-0008y.
- **TLS / SCRAM-SHA-256-PLUS: stay no-SSL.** The PG 18.3 oracle is
  built without SSL, so TLS work is unverifiable divergence today.
  The task stays `[!]` until an SSL-enabled oracle exists.
- **partition_aggregate inventory row: option (i) — correct it to
  `status=failed`.** Measured never-passed and needs two unbuilt
  milestone-scale planner features, so the must-pass gate was
  permanently red on unscheduled work. Row corrected in
  `docs/test-port/postgres-oracle-target-inventory.csv`.
- **template1 catalog namespace: Option A.** Route
  `ResolveDatabaseOid` around the `DefaultDBOid` sentinel for
  template1 (the design doc's recommendation, explicitly a
  workaround); Option B stays the M0122-0007 epic (slices 4b-4e).
  Task re-opened `[ ]`.

OWNER DECISIONS 2026-09-27 (post-cutover progress review
`/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/000-plan-party-progtess-0927.md`,
delegated; details in each task's entry):
- **Explicit-opclass index restart defect (S2) → item 2a.** Wrong rows
  after a clean restart on any explicit-opclass index (the regress
  `tenk1` indexes qualify) — placed as item 2a's live member.
- **Stale TPC-DS measurement data: reload APPROVED — EXECUTED
  2026-09-27.** The owner decision was yes — reload the goopg SF0.25
  and SF1 clusters and ANALYZE the PG SF1 `store` table — and the
  owner then delegated execution in a stopped-loop window (the loop
  restart stays owner-controlled). Done: SF0.25 reloaded via
  `scripts/tpcds-sf025-regression.sh load-goopg`; SF1 reloaded per
  `bench/tpcds/README.md` (schema drop + `scripts/tpcds-load.sh` —
  25/25 tables, ANALYZE + CHECKPOINT); `char(n)` padding verified
  (`customer.c_first_name` octet_length=20; `customer` relpages now
  ~2854 vs PG's 2872, was ~1979 unpadded). PG reference ANALYZE was
  widened to the whole `tpcds` + `tpcds025` databases because every
  reference table, not just `store`, had never been analyzed
  (`pg_stat_user_tables.last_analyze` was NULL for all 25 — prior PG
  plan evidence was captured against no-statistics defaults). TPC-DS
  divergences on `char`-heavy tables recorded before this date may be
  data artifacts — re-derive rather than trust them.
- **Post-reload finding (2026-09-27): bpchar equality divergence,
  previously masked by the unpadded data.** The SF0.25 sweep on the
  reloaded cluster is red (PASS=70 MISMATCH=20 CKMISMATCH=6 vs
  96/96 before) — and the cause is an engine bug the stale data was
  hiding, not a reload defect. Repro: `select 'Javier
  '::char(20) = 'Javier'` returns f on goopg, t on PG (PG's
  `bpchareq` ignores trailing blanks); `select count(*) from
  customer where c_first_name='Javier'` returns 0 on goopg vs 31 on
  PG (`'Javier'::char(20)` returns 31 on both). With unpadded
  storage the bytewise comparison happened to match, so the old
  cluster reported correct rows; correctly padded data now exposes
  that char-vs-shorter-literal comparisons miss. The many
  `goopg=0 oracle=100` mismatches are this class. Re-verified
  meaning: the pre-reload 96/96 was partially phantom. Expected
  follow-up: fix bpchar comparison semantics (text vs bpchar
  coercion + blank-insensitive equality), then re-sweep; queries
  still diverging after that are real planner gaps. Also note the
  census-era "char-heavy divergences may be data artifacts" runs
  both ways — the stale data did not just create noise, it
  *cancelled* a real defect.
- **M0146-0015b rank conflict: conservative reading confirmed.** A
  pre-existing (not cutover-caused) cause keeps normal M0146 order
  even when the task descends from an item-2a member. (Landed
  regardless; recorded to settle the ambiguity.)
- **LEFT JOIN ON outer-ref error stays in normal order.** A loud
  ERROR on valid SQL, not silent wrong results — not S2.
- **template1 extension re-attribution unblocked:** its recorded
  blocker (the template1 namespace `[!]`) got Option A GO on 09-25,
  so the `[!]` is stale — re-opened to `[ ]`.
- **M0146-0016 id collision fixed:** the census-filed Subquery-Scan
  task is renumbered M0146-0026 (the landed presorted-split task
  keeps 0016 — its design doc, analysis dir and ledger already cite
  it). A task-id uniqueness rule is added to
  `scripts/ralph-lineage-guard.py` (Rule E).
- **Nightly tpch `skip(port-busy)` — diagnosed, owner scheduling call
  deferred:** the stage needs :65433 free for a snapshot-copy window,
  but `ref-clusters-ensure` keeps it up for loop gates. Interim: the
  skip is accepted (loop spotcheck + acceptance arm cover the value
  floor); a real fix wants an owner scheduling decision (nightly
  quiesces the bench cluster vs. accepts no nightly TPC-H lane).
- **Guard-denial recurrence:** loops keep re-attempting denied
  operations (no cross-session memory). Mitigation added: a Key
  Principles bullet in `.ralph/PROMPT.md` — denials are durable; check
  the `ci/logs/ralph-guard-denials.log` tail before planning
  cluster/protected-file work and escalate rather than retry.
- **`[!]` blocker staleness:** mechanical check added as
  ralph-lineage-guard Rule F — a `[!]` task whose body names its
  blocker by structured id while the blocker is `[ ]`/`[x]` prints a
  non-blocking advisory. It immediately surfaced M0141-S2b-4e (blocked
  on the landed M0145-0008 — re-opened) and M0141-S7 (its cited S2b
  reference resolved, but the `[!]` stands: the remaining work was
  refiled as M0146-0006 and the `[!]` is the S4 lineage-budget
  escalation, not the citation).

OWNER DECISIONS 2026-10-02 (progress-report review
`/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/00-plan-party-progtess-1002.md`,
delegated; details in each task's entry):
- **Second S2 batch → item 2a.** All seven unplaced S2 escalations are
  placed as item 2a's live members, in this order: **M0146-0035**
  (clone loses db `tpch` — it can erase the bench corpus's only
  database, highest blast radius), **M0146-0039** (temp table
  resurrects as permanent after restart — catalog integrity),
  **M0146-0041** (numeric literal folded through float64 — silent
  wrong values, blocks TPC-DS Q21's match), **M0146-0040**
  (`date + int` returns a timestamp-formatted unknown, feeds 0041's
  Q21 evidence), **M0146-0034** (Limit over plain Gather is
  nondeterministic — wrong *and* flaky), **M0146-0032** (LATERAL outer
  ref binds to the wrong relation — wrong results on a niche shape),
  **M0146-0033** (`array_agg` over arrays returns the wrong
  representation — narrowest impact). Correctness outranks plan
  parity; siblings 0029/0030/0031 set the precedent.
- **Benchmark corpus VM state — aligned by owner action, EXECUTED
  2026-10-02.** The goopg bench clusters had `relallvisible=0` while
  the PG references were autovacuumed all-visible — a systematic
  pro-goopg measurement skew hiding Index-Only-Scan divergences.
  Owner vacuumed `:65433` (each `tpch` table explicitly — bare
  `VACUUM` only covers the default database, the documented
  M0125-0028 deferral — now filed for fix as **M0143-0011**, see
  the last bullet), `:65437` (`tpcds025`, via
  `ref-clusters-ensure --only 65437` start), and `:65436` (`tpcds`,
  clean-tree binary, cgroup-wrapped; both TPC-DS lanes returned to
  their prior down state). All benchmark tables are now all-visible.
  Consequence: **M0146-0019a's "moves no plan until vacuumed" blocker
  is lifted** — its divergences are now real planner gaps, selectable.
- **Nightly tpch `skip(port-busy)` — standing decision: accept the
  skip.** stage-tpch needs a quiesced `:65433` to snapshot-copy, but
  the bench cluster is the loop's gate oracle and must stay up. The
  nightly TPC-H lane stays dormant (spotcheck + acceptance arm cover
  the value floor); a real fix wants a scheduled quiesce window in
  the nightly driver, an owner-side change, not a loop task.
- **M0141-S7 stays held until M0146-0005 closes `[x]`.** 0005's close
  condition is now "every remaining first-divergence record routed to
  a named task" (banner item 3), so the hold releases on that event,
  not on match-metric exhaustion.
- **`pg_policies` prints a Go value dump — filed as M0134-0163d**
  (rowsecurity.sql arm; the view itself exists since M0131-S9.3e
  bootstrapped `pg_policy`, so this is a render defect, not 0163b's
  "does not exist").
- **M0146-0005 scope split — EXPLAIN text-identity campaign re-homed
  to M0146-0042.** The 0005 task entry's own scope is structural
  plan-shape burn-down; the text-parity slices since ~0005cf consumed
  it for `rendering`-category deltas. New text-only slices file under
  M0146-0042 (banner item 3). In-flight 0005\<x\> ids keep their ids;
  no renumbering.
- **Task-selection rationale made auditable.** The baton gains
  `Top-residual:` and `Skip-rationale:` fields (`.ralph/PROMPT.md`);
  "why this task and not the largest divergence class" must be a
  written line, not implicit.
- **Harness (per the report's §5.3):** guard denials now log/echo the
  matched token (`match=` field); the `for…do`/`DO $$`,
  interpreter-doc-text, `\copy (SELECT) TO` and private-path false
  positives are fixed with regression cases in
  `scripts/ralph-bash-guard-test.sh`; `scripts/ralph-prev-loop-check.sh`
  detects a loop that died without a `---RALPH_STATUS---` block
  (PROMPT.md step 2a). A proposed `stop_*.sh --status` read exemption was
  REJECTED in review — those scripts ignore argv and stop the cluster
  anyway; the fix belongs in the scripts themselves if wanted.
- **Bare `VACUUM` targets the wrong database — filed M0143-0011 and
  placed at the head of item 2a** (owner decision 2026-10-02, later
  same day). The corpus vacuum above surfaced it operationally: on
  `:65433`, `VACUUM` with no table list connected to db `tpch` exits
  successfully yet touches none of tpch's tables — the no-target arm
  still enumerates `DefaultDBOid` via `im.AllTables()` deep copies,
  the open residual in the 2026-07-30 M0125-0028 ledger row (which had
  no unchecked owning task — a bookkeeping gap now closed). It jumps
  the queue ahead of the S2 batch because maintenance correctness on
  the bench corpora is a precondition for trusting the campaign's own
  corpus state (a "successful" VACUUM in the wrong db silently
  preserves the measurement skew it was meant to remove), and the fix
  is small and fully specified by the ledger's resume point.

OWNER DECISIONS 2026-10-03 (delegated; details in each task's entry):
- **Third S2 batch → item 2a, in this order:** **M0146-0047** (a
  null-extended LEFT JOIN row's whole-row value is not NULL —
  `count(b)` counts 2000 where PG counts 1715; wrong aggregates on the
  commonest join shape), **M0146-0048** (two windows differing only in
  NULLS FIRST/LAST share one WindowAgg — wrong `rank()` output; not
  corpus-exercised, but a silent wrong-results shape), **M0146-0044**
  (`mod(numeric)` wrong, `numeric % numeric` unsupported — a core
  numeric op), **M0146-0045** (`ARRAY[...]` output unquoted — breaks
  round-trip/serialization fidelity), **M0146-0046** (`ctid` NULL
  under index scans — silent wrong system column, narrower usage),
  **M0146-0043** (`txid_current()`/`pg_current_xact_id()` return 0 —
  monitoring builtin, narrowest). Same policy as the earlier batches:
  correctness outranks plan parity.
- **M0146-0035's `[!]` park RATIFIED.** 37 clean clone starts on
  current code; the `:65433` source restarted 2026-10-01 and kept
  `tpch` (the "next restart loses tpch" risk is retired — the residual
  risk is clone-time only); and the landed `shared catalog reload`
  WARN means any recurrence names its own cause. It stays `[!]` until
  a clone start logs that WARN for `pg_database`/`pg_authid`, or owner
  direction. Its ledger row's side deferral (the `backup_label.old`
  rename) parks with it — harmless while un-triggered, per the row's
  own rationale.
- **`M-NIGHTLY-tuplelock-upgrade-reopen` keeps M-NIGHTLY order** — a
  300 ms wait-timeout scheduling-flake signature, not an S2
  wrong-results defect; same treatment as the three standing SSI
  specs.
- **No action on the 2026-10-02 nbtree units failure** — already filed
  (`units/internal/access/nbtree`, AI-20261002-010412-001) and it did
  not recur in the 10-03 run.
- **Status note (no decision):** slice 114 routed every SF0.25
  first-divergence record to a named task; M0146-0005's close
  condition now reduces to SF1's 65 unrouted records, so the
  M0141-S7 hold stays until then.
- **M0146-0005's S4 lineage escalation: RESOLVED — re-opened
  (`[!]`→`[ ]`).** Took the re-pin arm, implemented as a guard fix
  rather than a `LINEAGE-BASELINE` line: the five named completions
  (0005dn/do/dt/dr/ds) were filed with `Parent: M0146\-0005`, and the
  un-stripped `\-` escape made each one self-rooted — the root's
  mechanical last-5 window never held them (its true tail is
  `0028f`/`0037`, both `Movement: yes`), so a literal pin would have
  pinned ids out of a window they were never in.
  `scripts/ralph-lineage-guard.py` now strips the `\-` markdown
  escape from task ids, `Parent:` values and `LINEAGE-BASELINE` /
  `FROZEN-PREFIXES` tokens (the Rule E canonicalisation extended to
  lineage resolution); new entries should spell ids unescaped, though
  both forms now resolve identically. The normalisation surfaces one
  real exhaustion on **M0146-0009** (0009c/d/e/f/k, all `none`) —
  adjudicated the same instrument-artefact/recon class and pinned on
  its own `LINEAGE-BASELINE` line so the interleaveable stats
  children are not stranded by the fix. M0146-0009k already landed,
  so sequencing it first is moot; **M0146-0049 is next in item 3's
  structural line** — its three slices are the named blocker for
  0005dp/0005dq and M0145-0008ac/0008y, and it was never
  budget-blocked (`Parent: M0146`, own root).

OWNER DECISIONS 2026-10-06 (delegated; details in each task's entry):
- **Fourth S2 batch → item 2a, in this order:** **M0146-0056** (catalog
  index 2663's short read wedges ALL later DDL — cluster corruption,
  the widest blast radius), **M0146-0058** (an ANY-derived arm drops a
  pulled body's WHERE and target expression — silent wrong results in
  the commonest sublink shape), **M0146-0050 → M0146-0059** (the same
  CTERowCache scoping root cause, adjacent work), **M0146-0062** (int
  literals typed bigint — int4 overflow is silent and every integer
  query is a witness), **M0146-0051 → M0146-0052** (the ctid
  subsystem pair), **M0146-0053** (text datum miscompare),
  **M0146-0054 → M0146-0055** (the COPY-path pair), **M0146-0047b**
  (`(expr).field` syntax rejection — a rejection, not wrong results,
  so last). Same policy as the earlier batches: correctness outranks
  plan parity.
- **M0146-0049 re-opened (`[!]`→`[ ]`) with a LINEAGE-BASELINE pin**
  — the escalation's own re-pin arm. The five `none` completions are
  the instrument-artefact class: each landed real substrate and its
  witnesses diverge upstream (Q54's Parallel Seq Scan outer is open
  0049e; Q95 needed M0145-0008ac, which has since landed). Filings
  resume — 0049e, 0049f and the probe-cost item it could not file.
- **M0146-0007 re-opened (`[!]`→`[ ]`), no pin needed.** Under the
  2026-10-03 normalisation its mechanical last-5 is not exhausted
  (M0146-0021's entry sits inside the window), and the hold's premise
  — no remaining residue with a TPC-DS/TPC-H witness — was voided by
  M0146-0065 (TPC-DS Q2's inlined-CTE UNION ALL appendrel at both
  scales). 0065 stays under 0007, where it was filed; this resolves
  its "reopen or place" ask as reopen.
- **M0141-S7 closed `[x]`.** The 2026-09-23 hold's release condition
  is met: M0146-0005 closed `[x]` and the resume task M0146-0006
  landed the Incremental Sort election with the arm on by default
  (`01739f8d1`; the S2b-9 seed arm via M0146-0005bp). The task was
  "re-adjudicate and implement" — both are done. The exec-d residual
  stays a recorded ledger deferral (2026-09-17 row); banner item 6 is
  discharged.
- **M0146-0057's in-slice fix CONFIRMED.** It landed inside
  M0146-0007f's commit because that slice routes more references
  through the defective path and could not land red — the
  correctness-fix-in-parent precedent, not a re-place case.
- **M0145-0008ac's R3 order confirmed.** Its stated dependency (0049's
  through-a-join half) resolves via the 0049 re-open above; nothing to
  re-sequence.
- **B8 / `indexProbeCostMultiplier`: commission the measurement, keep
  the park.** Filed as **M0146-0068** (recon): knob A/B
  `GOOPG_INDEX_PROBE_MULT=1` vs `2` over the SF0.25+SF1 fire set and
  the TPC-H arm, tabulating plan-shape flips AND wall-clock per query.
  The 2026-09-24 park stays in force pending it — mult=1 elects
  PG-shaped NL plans that measured 2–3x slower (Q14 4.9x is the
  standing counter-example). If the table shows no witness regresses
  >10% wall-clock at 1 while shapes move toward PG, the retirement is
  pre-authorised to file as impl with the standard gates; otherwise
  the recon writes the narrowed-window proposal. This unblocks the
  M0145-0008ag → M0146-0009p / M0146-0060 chain.
- **Status notes (no decision):** tonight's new nightly item
  `TestPort_IsolationEvalPlanQual` (AI-20261006-005659-001) is already
  standing-tracked under M-NIGHTLY order. M0146-0060's second blocker
  M0146-0061 (bitmap heap scan range restriction) is a normal item-3
  filing, no placement needed. M0146-0063 (visibility-map persistence)
  is filed and selectable — it guards the 2026-10-02 corpus-vacuum
  work against unclean stops.

OWNER DECISIONS 2026-10-12 (delegated; details in each task's entry):

- **M0146-0068 result → Option A: retire `GOOPG_INDEX_PROBE_MULT`.**
  This is a NEW owner decision, not execution of the 2026-10-06
  pre-authorisation — its gate ("no witness regresses >10% wall-clock")
  FAILED on the measured table (Q17 ~x1.56, Q71 ~x1.27, Q54 ~x2.22 in
  the reported probes). Option A is still the call: +13 plan matches is
  the goal this plan exists to serve, the slowdowns are executor-cost
  masking the wrong planner price (the ledgered ~1.9x probe residual,
  M0146-0088, is the honest owner of that), and Q54's cited cause
  (0049e's Parallel Seq Scan outer) has since landed `[x]` — re-measure
  it during the retirement A/B rather than trusting the stale
  attribution. The retirement impl task is first among new item-3 work
  (banner item 3): remove the env knob and the `*2` applications under
  `internal/optimizer` (`costindex.go` + `cost_funcs.go`), retire the
  flag via the `flagProvenanceRetired` pattern in `flaglabels.go`, and
  update the affected tests — with the standard corpus A/B at both
  scales as evidence. This frees the M0145-0008ag → M0146-0009p /
  M0146-0060 chain it gated.
- **M0146-0005 second exhaustion: re-pinned** (LINEAGE-BASELINE
  0111–0115 — exactly the five; the non-pinned window then leads with
  0106's `Movement: yes`). The five are the same instrument-artefact
  class adjudicated 2026-10-03: regress-witnessed PG-fidelity ports.
  The proxy holds on **M0146-0042** and **M0146-0067** are lifted
  (`[!]`→`[ ]`) — the text-identity campaign resumes.
- **M0146-0007 second hold: re-opened + pinned** (LINEAGE-BASELINE
  0119/0123/0124/0129/0145). Standing note on its entry: a third spend
  yields to lower items rather than re-pinning.
- **M0146-0055: closed `[x]` + pinned** (LINEAGE-BASELINE
  0076/0077/0080/0081/0082) so its three held S2 findings and the open
  0084/0085/0086 proceed — they inherit item-2a rank.
- **Fifth S2 batch into item 2a:** `M0146-0155 → M0146-0089 →
  M0146-0126 → M0146-0125` — function `SET` clauses never applied
  (search_path correctness, found by the 0122-0008 probe), uncommitted
  CREATE TABLE surviving restart, mixed integer/numeric COALESCE
  picking the wrong type, and `||` treating NULL arrays as NULL instead
  of empty. M0146-0084/0085/0086 are not independently placed — they
  inherit rank via the M0146-0055 pin above.
- **M0119-0006 and M0122-0008 stay parked `[!]`.** Their children are
  old-format entries with no task id — the guard parses title fragments
  as pseudo-ids (`SET`, `current_database()`, "`pg_database.datacl`"),
  so a LINEAGE-BASELINE against them is ambiguous or impossible
  (punctuation is normalised away; `SET` matches only the first
  occurrence). Re-baselining would either no-op or silently pin the
  wrong tasks. The escalations' "not filed" residues were reviewed and
  stay ledgered deliberately; if any one is worth doing it can be
  filed `Parent: none` with a real id and placed — the S2-class
  `to_char`/TimeZone one under M0122-0008 is the strongest candidate,
  deferred to a later report rather than filed now.
- **GEQO-RNG residual: waived, recorded.** The two Q64 first-divergence
  records are exact probe ties broken by PostgreSQL's GEQO random
  stream — no deterministic instrument can see them. Named-waived
  residual, not an impl task; it satisfies "no unnamed first-divergence
  records".
- **Harness: `regress` is now a fourth Movement instrument.** Rule D /
  INSTRUMENT_RE / its error text, `scripts/ralph-lineage-guard-test.py`
  cases, this file's `Movement:` paragraph, and AGENT.md §S3 updated
  together. Rationale: this period's dominant S4-exhaustion class was
  correctness families whose only measured movement is regress diff
  counts (0111–0115, 0119/0123/0124/0129/0145, 0076–0082 all cite them).
  `regress` requires a number like every other instrument.
- **Noted, not actioned:** M0141-S2a-fix2r's children 0152/0153/0154
  cite `Parent: M0141-S2a-fix2r-c` — ~~an id that does not exist~~
  **correction 2026-10-12 evening: it does exist** (a completed task at
  the fix2r section, `Parent: M0141-S2a-fix2r`), so the children root
  to M0141-S2a-fix2r and inherited item-4 rank all along; no lineage
  problem and nothing to fix. 0152 is owner-placed into item 2a below
  anyway; ~~0153/0154 keep item-4 rank~~ **superseded 2026-10-13: the
  S7 correctness carve-out re-queues 0153/0154 below item 10.** A
  `Parent:`-dangling warning is
  still a candidate Rule-D-style hardening — this instance just wasn't
  dangling.

OWNER DECISIONS 2026-10-12 — evening (delegated; resolves the
post-placement escalations surfaced by the M0122-0015 selection-drift
investigation):

- **Fifth S2 batch amended:** live members now `M0146-0156 →
  M0146-0157 → M0146-0158 → M0146-0089 → M0146-0152 → M0146-0126 →
  M0146-0125`. 0156 (DATA LOSS) takes the head by severity; 0157/0158
  already inherited 2a rank via Parent: M0146-0155 — confirmed and
  sequenced with the function-domain cluster; 0089/0126/0125 carry
  over; **0152 was missed by the earlier sweep and is placed now**.
- **M0122-0015 re-opened** `[!]`→`[ ]` with the LINEAGE-BASELINE pin
  above — real child ids make the pin safe, and the `regress`
  instrument now lets this family claim honest movement.
- **M0145-0008ag re-opened** `[!]`→`[ ]` — its named blocker (the
  owner park on `indexProbeCostMultiplier`) is resolved by the
  Option-A decision. Re-measure after M0146-0159 lands.
- **M0146-0159 filed** — the multiplier-retirement impl the Option-A
  decision called "first among new item-3 work" (a direction, not a
  selectable entry — now it is one).
- **0152/0153/0154's Parent is NOT dangling** — `M0141-S2a-fix2r-c`
  exists; the earlier note is corrected in place above. Nothing to
  reparent.
- **Placement annotations swept:** every entry the owner named now
  carries "(placed in item 2a by the 2026-10-12 owner decision)" and
  its `Owner: place` block is RESOLVED — including 0084/0085/0086,
  which inherit rank via the 0055 pin rather than explicit naming.
  Open `Owner: place` text on a banner-named task is the stale-belief
  class that caused the 2026-10-12 selection drift.

OWNER DECISIONS 2026-10-13 (delegated; resolves the owner direction
"focus on TPC-H/TPC-DS plan parity — the loop must not keep creating
and consuming correctness/regress-coverage work ahead of it"):

- **Item 2a discharged.** The fifth (amended) S2 batch drained
  overnight — 0155, 0156, 0157, 0158, 0089, 0152, 0126, 0125 and the
  inherited 0084/0085/0086 are all `[x]`. The correctness-batch lane
  is closed; selection resumes at item 3, whose first task is
  **M0146-0159** (retire `indexProbeCostMultiplier`), unblocking
  M0145-0008ag → M0146-0009p/0060.
- **S7 correctness-only carve-out** (Selection rules, amended above):
  a descendant whose expected movement is on `regress` or `none` —
  i.e. executor/catalog/session correctness, not a plan instrument —
  no longer inherits its parent's item rank. It queues under the
  unnamed-task fallback below item 10. This ends the self-sustaining
  S2 conveyor: every landed correctness fix files 1-2 more findings,
  and inheritance was re-promoting them to top rank each loop.
- **The pending "owner: confirm or place" asks are resolved —
  NOT placed.** M0146-0160, 0161, 0162, 0163, 0164, 0165, 0166 stay
  filed (record, don't drop) and queue below item 10. Note for the
  owner: **0162 is severity-high** (every non-superuser LOGIN runs
  with superuser rights — a genuine RBAC hole, ledgered since
  2026-07-09). It is deferred per the parity-first direction, but it
  is the first candidate for the next correctness batch.
- **0153/0154 re-queued below item 10** — they inherited item-4 rank
  through the M0141-S2a-fix2r family; the carve-out removes it (the
  earlier "keep item-4 rank" note is superseded in place).
- **`regress` stays a Movement instrument** (unchanged — the owner
  confirmed this design is correct): it records honest movement and
  keeps lineage budgets unblocked. It never confers rank.
- **Stale-baton caution, standing:** the 2026-10-12 evening decision
  added the "recheck placement claims against the banner" rule to
  `.ralph/PROMPT.md`. The same class applies here — a baton that
  says "S2 findings pending owner placement" is a claim, not state;
  this decision is the state.

OWNER DECISIONS 2026-10-13 (b) — evening (delegated; resolves the
parity-queue-exhausted state the loop hit at 18:56, commit `5953fe051`'s
"items 3-9 had no selectable row → item 10"):

- **M0146-0014 re-opened + pinned.** Its last-5 none streak was routing
  recons — movement lands in the filings, so S4 is structural here.
  LINEAGE-BASELINE: M0146-0014 0148 0014c 0149 0150 0014d.
- **F1–F5 filed as M0146-0176…0180** (Parent: M0146-0014) and named the
  next item-3 work in order 0176 → 0177 → 0178 → 0179 → 0180. Each
  carries a first-divergence record and expected match movement —
  parity, not carve-out work.
- **M0146-0005dp re-opened** — its "re-select when M0146-0049 lands"
  gate is met (0049 `[x]`, 0049e `e75280c05`). Verify premise vs
  residual; PARAM-APPEND first-divergence record.
- **M0142-0008 chain: parked, accurately.** Post-producer reachability
  was re-measured ZERO (M0142-0008a-3i-reach-verify + route-a step 2);
  the residual is the leaf-count/lateral class owned by the M0145-0003
  line. The chain's entries and the UNFROZEN block now say so — no new
  recon filed.
- **M0146-0067 and M0146-0007 residue annotated** — both are
  regress-only/no-TPC-witness classes; they rank below item 10 under
  the S7 carve-out (0067 note added; 0007's "resumes under normal
  item-3 order" superseded).
- **M0142-0005g closed `[x]`** — recon complete (no sub-term diverges;
  the gap is corpus content + sampling noise); remaining action is the
  corpus-side ask on the M0142-0005 escalation.
- **PG reference clusters released from HOLD** — the :65432/:65438
  `pgdata.HOLD` markers were known-cause host-reboot unclean-shutdown
  markers (stale postmaster.pid, dead pid, ~18:20); no evidence value.
  Cleared and restarted via `scripts/ref-clusters-ensure.sh`.
- **SF1 TPC-DS reload DONE later on 2026-10-13** — see the `char(n)`
  stale-load task's RESOLVED block (SF1 `data` rebuilt with the current
  build, relpages now byte-identical to PG, witness Q96 elects PG's
  shape again; PG reference `store` ANALYZEd).
- **Not actioned:** M0119-0006 / M0122-0008's `[!]` (ambiguous pseudo-id
  children — pins unsafe, established decision).

OWNER DECISIONS 2026-10-13 (c) — parity self-replenishment (delegated;
the owner's ask: when the parity stock runs out, the loop itself files
the next breakdown→fix task instead of falling to item 10):

- **New selection rule S9 (parity replenishment)** — see the Selection
  rules block. When items 0-9 hold no actionable task, the loop runs
  ONE census pass before item 10: largest actionable
  CATEGORIES-EXCL-MATCH category across the three corpora → one
  query → file ONE `Kind: impl` task under M0146-0014 (first
  divergence point vs PG 18.3, fix in order, drive to MATCH). The
  filing inherits item-3 rank under S7 and is selected before item 10.
  Bounds: one filing per pass, a `none`-closing pass leaves an
  `exhausted at <sha>` note and re-runs only after planner-relevant
  commits land, two consecutive `none` replenishment tasks escalate.
- **`FILEABLE-ROOTS:` banner token + guard support** — M0146-0014 is
  listed: filings under it are exempt from the Rule-A lineage budget
  (the census's purpose IS filing findings; yesterday's freeze was a
  `none`-streak of routing sweeps starving the supply, not inert work).
  This IS a true exemption — it supersedes the "not an exemption"
  caveat on this root's 2026-10-13 pin, which stays as the record.
  Machine side: parsed from the BASELINE (a candidate cannot
  self-exempt), and a `[ ]`→`[!]` flip on a listed root is a
  violation — the owner de-lists it to hold the lineage.
  `scripts/ralph-lineage-guard.py` + its test suite carry the
  mechanism (cases: listed-root accept, unlisted-root reject,
  candidate-side token ignored, token outside the banner ignored,
  `[!]`-flip violation).
- **Rule B widened to M0146** — `NEWID_RE` is now
  `M01(3[7-9]|4[0-6])|P0-`: an S9 filing missing `Parent:` would
  silently drop to the below-item-10 pool; the guard now requires the
  field so S9 cannot fail invisibly. Harness-scope prose updated to
  match reality (AGENT.md/PROMPT.md now say M0137–M0146; the
  commit-msg `plan_parity_task` regex is unused, untouched).
- **Breadth trade-off acknowledged:** the exemption covers 0014's whole
  subtree — grandchild impl work under filed findings is exempt too.
  Accepted: the subtree is parity work by construction, and the S9
  bounds (one filing/pass, stale-premise accounting, two-`none`
  escalation) bound the abuse channel.

OWNER DECISIONS 2026-10-15 (delegated; resolves the owner's direction
"restore parity focus — the loop has been selecting item-10 tasks"):

- **Cause found, not a selection-rule violation:** the loop skipped to
  item 10 because the only open non-channel items-0-9 task,
  M0146-0178, led its remaining-steps list with an owner gate
  ("(1) owner reload of the goopg TPC-DS clusters") and the SF0.25
  half of that reload had never run (SF1 was reloaded 2026-10-13).
  S9's censuses correctly found every other record routed or waived.
- **SF0.25 reload executed** (see M0146-0178's entry): `data-sf025`
  re-initialised and reloaded from a clean-HEAD binary
  (`32fe610c7`; fillfactor fix `d5d6f8c52` verified ancestor).
  Indexes now match PG page counts on the int4 pkeys; every probed
  heap's `relpages` equals PG `tpcds025`. Recorded reload →
  `routing-20261014j` is stale per S9(1); next census re-captures.
- **Auditability amendment (the systemic fix):** the loop never
  violated S1 — but it skipped an open `[ ]` items-0-9 task on an
  implicit owner-gate reading with NO auditable record
  (`Skip-rationale: n/a`). S9(4) now requires every exhausted note,
  and `.ralph/PROMPT.md`'s `Skip-rationale:` now requires every
  items-0-9 bypass, to enumerate each open `[ ]` items-0-9 task as
  `id(gate)` tokens with the closed vocabulary `channel` /
  `below-item-10` / `owner-<what>` / `blocked-on <id>`. An owner gate
  on a parity task can no longer hide inside an "exhausted" verdict.
- **S9 stock clause tightened:** burn-down parents (today
  M0146-0042, M0146-0007) are stock only while they hold an ungated
  actionable slice of their own — a `[ ]` channel root must not by
  itself keep items 0-9 "live" and starve the replenishment pass.

OWNER DECISIONS 2026-10-15 (b) — TPC-H corpus realignment (delegated;
resolves the owner's delegation "TPC-H corpus mismatch is a structural
cap — judgement and execution delegated"):

- **Realign executed:** the `:65433` bench corpus was replaced
  row-for-row with the PG `:65432` reference corpus —
  `pg_dump --data-only --inserts --rows-per-insert` (8 tables,
  lineitem=5,998,835 / orders=1,500,000), loaded as batched INSERTs on
  a clean-HEAD binary (`tmp/goopg-tpch-bin`, sha256 bc4d6b6e…, built at
  285df1201; COPY was rejected as unsafe at this HEAD — standalone COPY
  contexts carry no per-database catalog resolution, M0122-0015a
  class). All 8 row counts match PG; `relpages` now sits within ~0.5%
  of PG on every table (e.g. lineitem 129214 vs 129346, supplier
  225 = 225). Data preserved at
  `bench/tpch/runtime_goopg/data.pre-realign-20261015` (delete when the
  realign is judged stable); old binary at `goopg-bin.old-20260920`.
- **Anchors re-pinned:** `bench/tpch/spotcheck_expected.env` (Q13
  33→35; Q12=2 structural invariant held),
  `ci/batch/tpch-row-anchors.csv` (dataset tag 20261015; Q3 11356 /
  Q10 20486 / Q11 517 / Q13 35 / Q16 18166 / Q18 12 / Q21 421),
  `bench/tpch/baseline-digests.txt` (full 22-query re-capture).
- **Fresh TPC-H parity truth:** `routing-realign-20261015` captured
  post-realign — **12/22 MATCH, 10 SHAPE-DIFF**. vs routing-20261014j:
  Q10 resolved (MATCH); Q3/Q5/Q7 newly SHAPE-DIFF. The 14-match figure
  was partly corpus-noise coincidence; the 10 remaining records are now
  honest planner divergences on identical data and are S9-actionable.
- **Runtime regression surfaced:** TPC-H Q5 executes its new SHAPE-DIFF
  plan (scan-type divergence) past the 900s digest cap — PG runs it in
  seconds. Recorded as a BOTH-ERROR pin in baseline-digests.txt and a
  note on the Q5 anchor row; it is a parity task candidate, not a data
  problem.
- **Staleness:** `routing-20261014j`'s tpch arm is invalidated by the
  corpus change (S9(1) corpus-freshness clause). The sf025/sf1 arms of
  that artifact remain governed by their own corpus state.

**UNFROZEN (owner decision 2026-09-20) — selectable again:** the M0142-0008
chain (`M0142-0008a-3`, `M0142-0008c-1a`, `M0142-0008c-3d`,
`M0142-0008c-4`) is unfrozen by direct owner instruction. P0-E7's private-lane
A/B already landed the evidence csq-R2's reopen condition named (23/24 digest
lines identical; sole divergence = Q9's 600s timeout in BOTH arms). **Hard
owner constraint:** do NOT lift Q78's `outer-over-derived` firewall — or take
any equivalent shortcut — to obtain reachability **for this chain** (the
firewall's own removal is separately owner-GO'd as M0145-0018 on its own
evidence track; once 0018 lands, the constraint is moot, but M0142-0008's
reachability must still be demonstrated by the producer route, not credited
to the lift). The sanctioned route is the
previously-unfiled producer, now filed as **M0142-0008-producer** below: teach
an IN-unnesting (or EXISTS-variant) path to set `.SJInfo` on a
DP-search-visible `JoinSemi` link, the way c19 did for ANTI. **Status
2026-10-13 (owner): the producer landed `[x]` (`1f76d83d9`) and
post-producer reachability was re-measured as ZERO** —
`M0142-0008a-3i-reach-verify` and the `route-a` step-2 re-measure found
no `.SJInfo`-bearing pair reaches the arm; the residual blockers are the
`leaf-count` (Q16/Q94/Q35 undecomposable composite leaves) and `lateral`
(Q69/Q10) classes, owned by the M0145-0003 line, not by this chain.
The 0008c-1a/3d/4 impls stay parked until that line admits the leaves —
they are NOT selectable work; the earlier "unreachable" entry notes are
accurate post-producer, not stale. csq-R2's
ledger row is updated with this decision (`.ralph/deferral_ledger.md`,
row dated 2026-09-20).


## P0 — Incident recovery (complete; all `[x]`)

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

## M-NIGHTLY run logs + manually-discovered tasks (all `[x]`)

### Nightly run 20260922-004850 (sha `c07ebf0112d7`, 16 items) — filed 2026-09-22
- [x] **testport/TestPort_IsolationEvalPlanQual** — stale nightly failure
  (AI-20260922-004850-001; repro: `go test -v -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgAmcheck003CombinedCorruption** — stale nightly failure
  (AI-20260922-004850-002; repro: `go test -v -run '^TestPort_PgAmcheck003CombinedCorruption$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgAmcheck003MissingHeapFile** — stale nightly failure
  (AI-20260922-004850-003; repro: `go test -v -run '^TestPort_PgAmcheck003MissingHeapFile$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgAmcheck003MissingIndexFork** — stale nightly failure
  (AI-20260922-004850-004; repro: `go test -v -run '^TestPort_PgAmcheck003MissingIndexFork$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgAmcheck003SchemaScoped** — stale nightly failure
  (AI-20260922-004850-005; repro: `go test -v -run '^TestPort_PgAmcheck003SchemaScoped$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropGoopgToPG** — stale nightly failure
  (AI-20260922-004850-006; repro: `go test -v -run '^TestPort_PgoutputInteropGoopgToPG$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgBatchDML** — stale nightly failure
  (AI-20260922-004850-007; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgBatchDML$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgColumnOrderMismatch** — stale nightly failure
  (AI-20260922-004850-008; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgColumnOrderMismatch$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgFullDML** — stale nightly failure
  (AI-20260922-004850-009; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgFullDML$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgPgbenchInsert** — stale nightly failure
  (AI-20260922-004850-010; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgPgbenchInsert$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgPgbenchTpcb** — stale nightly failure
  (AI-20260922-004850-011; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgPgbenchTpcb$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgReplicaIdentityFull** — stale nightly failure
  (AI-20260922-004850-012; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgReplicaIdentityFull$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgSubscriberExtraColumn** — stale nightly failure
  (AI-20260922-004850-013; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgSubscriberExtraColumn$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgSubscriberExtraDefault** — stale nightly failure
  (AI-20260922-004850-014; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgSubscriberExtraDefault$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_PgoutputInteropPGToGoopgTruncate** — stale nightly failure
  (AI-20260922-004850-015; repro: `go test -v -run '^TestPort_PgoutputInteropPGToGoopgTruncate$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **STALE — re\-run 2026\-09\-22 \(loop \#7\): PASSes at HEAD.** All 15
    non\-suite items of this run were re\-run together on a CLEAN tree
    \(unrelated WIP stashed\), which matters: the pgoutput paths these tests
    exercise have uncommitted work in the tree, so a dirty\-tree PASS would
    prove nothing about HEAD.
  - **Why it failed then**: `b7cea12e5` landed **66 seconds after the nightly
    started** and changes exactly the path these tests exercise
    \(`pgoDecodePhysicalValue` / `PadBpchar`\). Three of the 15 —
    `TestPort_IsolationEvalPlanQual`, `TestPort_PgAmcheck003SchemaScoped`,
    `TestPort_PgoutputInteropGoopgToPG` — were additionally re\-run in a
    detached worktree at the nightly's own sha `c07ebf011` and **FAIL** there,
    which is the direct evidence. The other twelve are PASS\-at\-HEAD only and
    share the same cause by attribution, not by their own bisect.
- [x] **testport/TestPort_RegressSuite** — aggregate report; its sole failing
  case is already owned
  (AI-20260922-004850-016; repro: `go test -v -run '^TestPort_RegressSuite$' ./internal/testport/`).
  Kind: impl
  Parent: none
  - **RE\-RUN 2026\-09\-22 \(loop \#7\) at HEAD on a clean tree**: 231 subtests
    PASS or SKIP and exactly **one** FAILs —
    `TestPort_RegressSuite/partition_aggregate`, which already has its own
    `[!]` row \(partitionwise aggregation\). `select_having`,
    `select_implicit` and `union`, named when this row was filed, now pass.
  - The suite therefore stays red until that one row is cleared; this row
    itself carries no separate work.
- [x] **M\-NIGHTLY\-20260929\-004 — testport RegressSuite: limit, vacuum\_parallel, varchar**
  \(AI\-20260928\-004845\-004, AI\-20260929\-003700\-004; repro: `go test \-v
  \-run \'^TestPort\_RegressSuite$\' ./internal/testport/`\)\. Selected ahead of
  banner order under the M\-NIGHTLY exception: a must\-pass gate was red and
  an own commit \(M0146\-0030\) exposed it\.
  Kind: impl
  Parent: none
  - `limit`: the sorted\-split\-over\-Gather run\-time ERROR, fixed by
    M0146\-0005ak \(`68b95f351`\)\.
  - `varchar`/`vacuum\_parallel`: every CREATE failed once pg\_class needed
    more than one internal root in pg\_class\_relname\_nsp\_index \("internal\-root
    overflow inserting downlink 97"\); order\-dependent \(full suite only\);
    bisect → `0994872a3`, which only let join/arrays run further\. Fixed by
    any\-height internal levels in the catalog btree rebuild\. Design
    `docs/design/0100\-0149/0106\-0010\-sys\-btree\-any\-height.md`\.
  - Gates: units, tpch\-spotcheck, sf025 96/96 \(FORCE=1 values\-only, nightly
    batch running\), full TestPort\_RegressSuite PASS, new real\-PG E2E PASS\.
  Movement: none — correctness (nightly must-pass gate green)

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
- [x] **M\-NIGHTLY\-tuplelock\-upgrade\-reopen — testport/TestPort_IsolationTuplelockUpgradeNoDeadlock
  \(`AI-20261003-002454-003`\)** — FAILed again \(15\.55s\) in the 2026\-10\-03
  nightly: the tail of the schedule is shifted by two lines \(expected 253
  lines, got 255\), with `s2\_rollback` / `s0\_rollback` / the
  `s3\_for\_update <\.\.\. completed>` lines landing one step late\. Reopens
  the closed Loop \#24 task above per its own "re\-open if a future nightly
  reproduces" note\.
  Repro: `go test \-v \-run '^TestPort\_IsolationTuplelockUpgradeNoDeadlock$'
  \./internal/testport/`; evidence
  `ci/logs/20261003\-002454/testport/go\-test\.log` \(L245\-255\)\.
  Kind: impl
  Parent: none
  - First step: re\-run it 4× at HEAD to separate a timing flake \(the
    isolation runner decides `<waiting>` by a 300ms timeout\) from a real
    lock\-ordering change; compare against the 2026\-10\-02 nightly, which
    passed it\.
  - **DONE 2026\-10\-10 \(`2544b2c57`\)\.** Timing flake: 4/4 fresh passes
    and every nightly since 2026\-10\-04 \(7\) passes; the 10\-03 run was slow
    \(15\.55 s vs \~12\.5 s\)\. Root cause is the runner\'s timing rules \(300 ms
    → `<waiting>`, 200 ms post\-step drain\)\. Fixed as isolationtester does
    it: `awaitStepOrBlock` probes `pg\_stat\_activity\.wait\_event\_type` for a
    `Lock` wait at launch and in the drain, waiting on non\-blocked steps
    \(1\.5 s fallback cap\)\.
    - Isolation family HEAD vs candidate: strict unchanged; PreparedTransactions
      SKIP → PASS; suite PASS 38 → 48; summed time 571 → 655 s\.
    - Test `TestAwaitStepOrBlockAsksTheProbe`\.
    - Design `docs/design/0100\-0149/m\-nightly\-isolation\-lock\-wait\-probe\.md`\.
  Movement: none — test\-runner fidelity fix; no plan instrument
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
- [x] **units/internal/utils/activity/stats
  TestCounter_PerShardWriteDistribution (AI-20260921-000212-001)** —
  units suite FAIL: "only 1 shards received Adds; per-P sharding looks
  broken" (counter_test.go:105, 0.00s). First-seen tonight; could be a
  scheduling-sensitive flake (per-P shard distribution depends on
  goroutine-to-P mapping) or a real stats-counter regression. Repro:
  `go test -timeout 10m ./internal/utils/activity/stats/`; evidence
  `ci/logs/20260921-000212/units/go-test.log`.
  Kind: test-fix
  Parent: none
  - **RESOLVED 2026-09-22 (loop 57) — scheduling-sensitive flake, and the
    guard was checking the wrong thing.**
    - **Not reproducible here**: 20 consecutive runs each at GOMAXPROCS 2, 3,
      4 and 16, plus a run pinned to a single CPU (`taskset -c 0`) with
      GOMAXPROCS 16 — all PASS.
    - **Root cause of the false failure**: the skip guard tested
      `runtime.GOMAXPROCS(0) >= 2`, which is the configured P COUNT, not the
      parallelism the process actually receives. Under a CPU quota or a
      loaded machine a 16-P process can run every goroutine on one P, so the
      skip does not fire and the assertion fails on a HEALTHY `Counter`. The
      nightly runs the whole suite under exactly that pressure.
    - **The property was also stated too strongly**: "more than one shard
      always accumulates" is a claim about the Go scheduler, not about
      `Counter`. The correct property is CONDITIONAL — whenever the runtime
      actually hands the workload more than one P, more than one shard must
      accumulate.
    - **Fix**: the test now OBSERVES the parallelism it got (sampling the P
      id per iteration in its own pin window) and asserts only when at least
      two distinct Ps were seen; otherwise it skips with that count in the
      message. The sample may not be the very P the `Add` used, but it only
      ever widens the observed set — the conservative direction, since a
      wider set makes the test MORE willing to assert.
    - **Both branches verified, not assumed**: (a) pinned to one CPU it
      passes; (b) with `shardFor` deliberately collapsed to `shards[0]` it
      FAILS with "workload ran on 16 distinct Ps but only 1 shard received
      Adds; per-P sharding has collapsed" — so the test still catches the
      regression it exists for. The probe was reverted immediately.
    - Also added an unconditional `Sum` check (`goroutines*iterations`),
      which holds on one P as well and catches a lost update regardless of
      how the work was distributed — coverage the old test did not have.
    - No deferral-ledger row: no PostgreSQL behaviour is left unimplemented;
      this is test robustness, not a semantics shortcut.
    - Gates: units PASS.
- [x] **testport/TestPort_IsolationEvalPlanQual — EPQ refetch-merge dropped
  by an aliased target list (AI-20260922-004850-001)** — FIXED 2026-09-22.
  The spec's `readforss` returned the PRE-update `tableAValue` where PG
  returns `newTableAValue`.
  Kind: impl
  Parent: none
  Movement: yes — `ColPos` for `table_a` went `[-1 -1]` -> `[0 1]`, measured
  from the spawned server's own log during the harness run; the full
  `TestPort_IsolationEvalPlanQual` spec now matches PG byte-for-byte.
  - **Root cause.** `resolveRowMarkCtidResnos` (internal/optimizer/planner.go)
    located each locked-relation column in the root output by its
    `(Name, SourceTableIdx)` identity. A target-list ALIAS renames the output
    SCHEMA column, so `SELECT ta.value AS ta_value` leaves no `value` entry to
    match and EVERY position resolved to `-1`. At the merge site
    (`lockRowsOp.Next`) a `-1` is indistinguishable from "the projection
    doesn't carry this column", so the correctly re-fetched post-update values
    were silently discarded while `applied` was still set `true`, so the
    fallback never ran either.
  - **Fix.** New `findLockedColByTarget` + `findTopProjectForOutput`: when the
    name scan yields `-1`, resolve through the Project's TARGET expression,
    whose `ColumnRef.Name` keeps the ORIGINAL source column name even when the
    schema entry is aliased. The `-1`-means-absent meaning is preserved.
  - **CORRECTION to loop \#58's narrowing, which was wrong.** That loop
    recorded the discriminator as "a SubPlan attached to the locked scan" and
    filed a minimal pair to prove it. Re-running that pair at HEAD this loop
    returned the CORRECT value, refuting it. The real discriminator is the
    ALIAS; the spec's `readforss` merely happens to have both, and the probe
    written from it dropped the `AS` clauses while keeping the subquery. What
    made the difference was measuring inside the harness — snapshotting the
    spawned server's `cluster.log` (it lives under `t.TempDir()` and is
    deleted at teardown) instead of trusting a hand-built repro to be
    faithful to the spec.
  - Loop \#58's other two refutations still hold and were re-confirmed:
    `refetchRow` applies no snapshot (it reads the raw heap tuple at the
    pointer, as PG's EvalPlanQual requires) and the chain walk is correct
    (`newPtr != ptr`). The instrumentation showed `newLockedCols` really did
    contain `newTableAValue` — exactly branch 1 of the two disjoint fix sites
    the previous loop predicted.
  - **Sibling audit (Hard-won Rule \#2).** Re-ran the whole spec instrumented
    after the fix and read every `ColPos` it produced. `accounts_ext` resolves
    `[0 -1 -1 -1 -1]` but at `rowlen=2`, i.e. those four columns genuinely are
    not in the row — the legitimate `-1`, now pinned by
    `TestPlanCtidRowMarkAbsentColumnKeepsNegativeColPos`. `accounts`,
    `jointest` (all three self-join offsets) and `table_a` resolve fully. No
    latent instance of the same class in this spec.
  - Follow-up lead, NOT a known divergence so no ledger row: other resolvers
    that key on `(Name, SourceTableIdx)` — `findColumnIndexByNameAndSource`,
    `predRebind` — share the identity-by-name pattern. They operate below the
    top Project, in pre-alias coordinates, so they are not obviously exposed;
    nobody has audited them against aliasing.

- [x] **testport/TestPort_IsolationReceiptReport (AI-20260921-000212-003)** —
  DOES NOT REPRODUCE at HEAD. Re-ran the filed repro
  (`go test -v -run '^TestPort_IsolationReceiptReport$' ./internal/testport/`)
  and it PASSES in 7.40s; the nightly's "expected 4215 lines, got 4216"
  is gone, and the item is absent from the newer run 20260922-004850,
  which independently confirms it. Closed as not-reproducing rather than
  fixed — no code changed for it.
  Kind: test-fix
  Parent: none
  Movement: none

- [x] **testport/TestPort_RegressSuite — 3 of 4 subtests FIXED
  (AI-20260922-004850-016)** — `select_having`, `select_implicit` and `union`
  now PASS; `partition_aggregate` is a different, pre-existing gap and is
  re-filed below as its own task. Repro was
  `go test -run '^TestPort_RegressSuite$' ./internal/testport/`.
  Kind: impl
  Parent: none
  Movement: none — no plan moved (SF0.25 `PLAN-SHAPE same=99 changed=0`);
  this is a VALUE/rendering correctness fix, and none of S3's three
  instruments measures it.
  - **One root cause behind all three: the bpchar padding asymmetry that
    M0143-0007b's own storage flip introduced.** Slice 1 made stored bpchar
    blank-padded; three paths were left disagreeing with storage:
    - `lower()`/`upper()` consumed the PADDED image. PG has no
      `lower(bpchar)` — the call resolves through the implicit bpchar->text
      cast, which is `rtrim1` (`pg_cast.dat`, `text(bpchar)`), so the padding
      is stripped first. Fixed with a new `bpcharArgAsText` helper keyed on
      the argument's DECLARED type (a padded image is indistinguishable from
      a text value that genuinely ends in spaces).
    - the CAST **to** `char(n)` still produced a TRIMMED image, so a cast
      value and a stored value of the same logical value were different
      strings. Upstream pads there: `varchar->bpchar` is `castfunc => '0'`
      in `pg_cast.dat`, so the padding comes from the typmod coercion
      (`bpchar()`, varchar.c) — which is exactly where goopg applies it now.
    - `bpchar->varchar` was excluded from the rtrim arm **on a wrong
      assumption I had written there myself**: the comment said upstream's
      bpchar->varchar "is a separate entry". It is a separate pg_cast entry
      naming the SAME function (`text(bpchar)`), so separate-entry did not
      imply separate-behaviour. Corrected.
  - **The `union` case was the dangerous one.** `SELECT CAST(f1 AS char(4))
    FROM VARCHAR_TBL UNION SELECT f1 FROM CHAR_TBL` stopped DE-DUPLICATING,
    because one representation of a value was padded and the other was not,
    so equal values compared unequal and every row was emitted twice. The
    other two only rendered a wrong column WIDTH with correct values — which
    is why every value-comparing gate stayed green through all of it.
  - **A test-design trap worth carrying forward**: `octet_length` is one of
    the `PadBpchar` RE-PADDING render callers, so it reports the padded
    width whether or not the producer padded. A cast-padding test written on
    it PASSES with the fix disabled — verified. The valid witness has to
    observe the raw image; UNION de-duplication does, and that is what the
    pinned test uses. This is the same "sites that re-pad are safe,
    consumers are dangerous" rule from slices 1-3, now applying to the TESTS
    as well as the code.
  - Sibling audit (Hard-won Rule \#2): `lower`/`upper`/`initcap` are one
    switch family and all three now state the rule. `initcap` is a guard not
    a witness — verified by disabling the helper, it passes either way
    because `initCap`'s own word-splitting already drops trailing blanks.
    One pre-existing test (`TestInlineCastVarcharBpcharTypmodTruncation`)
    pinned the OLD unpadded cast; its expectations were re-derived from a
    live PG 18.3 reading rather than flipped to match the new code.
  - Broader class NOT closed, ledgered and filed below: every text function
    that receives a bpchar resolves through the same rtrim1 cast. Measured
    on live PG 18.3: `initcap`, `replace`, `substr` and the string-concatenation operator all strip
    (`octet_length` 2, 2, 2 and 3 on a `char(6)` holding 'ab'). Only the
    `lower`/`upper`/`initcap` family was fixed here.

- [x] **testport/TestPort_RegressSuite/partition_aggregate — partitionwise
  aggregation (AI-20260922-004850-016, split out)** — **SCOPING RECON DONE
  2026-09-22 (loop \#62), no production change. Owner decision
  2026-09-25 (below): inventory row corrected to `status=failed`;
  the feature itself stays unscheduled — CLOSED.**
  Kind: recon
  Parent: none
  Movement: none — no production change.
  - **Finding 1: this is NOT a regression, and it never passed.** Built and
    ran the case in an isolated worktree at `aef4a4257` — the commit BEFORE
    M0143-0007b slice 1, i.e. before any of the bpchar work the nightly's sha
    pointed at — and it fails there with the byte-identical message
    (`output mismatch; normalization rules need extension`). The nightly's
    `first-seen: 20260922 (new tonight)` label is therefore not a code
    change; the 20260921 run simply did not report this subject. I could not
    explain the label itself and am not claiming to — what IS measured is
    that goopg's behaviour is identical at both commits.
  - **Finding 2: the case requires TWO unimplemented planner features.** The
    upstream file sets BOTH `enable_partitionwise_aggregate TO true` (line
    10) and `enable_partitionwise_join TO true` (line 12) and then runs 115
    queries under them. goopg declares both GUCs in
    `internal/catalog/catalog.go:12302-12306` with PG's own `off` default,
    but **nothing consumes either** — a declared-but-unconsumed GUC.
  - **Finding 3: the optimizer has no partition awareness at all.**
    `catalog.Table` carries `PartitionKey`, `PartitionMethod` and
    `PartitionBounds` (`internal/catalog/catalog.go:649-660`), but every
    reader of those fields is in `internal/executor`; `internal/optimizer`
    never reads them. So partition metadata does not reach path generation
    in any form. This is the real size of the task: it is not "add a
    grouping variant", it is "make the planner partition-aware first".
  - **Decomposition** (not filed as tasks yet — see the escalation below;
    filing them presumes the owner wants the feature built):
    (a) surface partition metadata into the optimizer's rel representation
        (PG's `PartitionScheme`/`part_scheme` on `RelOptInfo`);
    (b) partition-bound matching between two rels (PG's
        `partition_bounds_equal`) — the prerequisite BOTH features share;
    (c) partitionwise join (`try_partitionwise_join`, joinrels.c);
    (d) partitionwise grouping (`create_partitionwise_grouping_paths`,
        planner.c) — push the grouping below the Append when the group keys
        include the partition key.
    PG oracle for (d): `postgres/src/backend/optimizer/plan/planner.c`.
  - **Separable, much smaller divergence noticed while reading the diff, NOT
    filed as part of this feature**: goopg's EXPLAIN renders a numeric
    constant as `Filter: (avg(pagg_tab.d) < 15)` where PG renders
    `< '15'::numeric`. Fixing it alone would NOT make this case pass (the
    plan SHAPE still differs), so it is not worth doing under this task —
    recorded so a later loop does not mistake it for the whole gap.
  - **ESCALATION — owner decision required on the inventory row, not on the
    feature.** `docs/test-port/postgres-oracle-target-inventory.csv` line 139
    marks this case `status=pass, pass_required=yes`, whose vocabulary means
    "regress/isolation case passing; must stay passing" — while the SAME
    row's rationale says "output diverges from expected". Finding 1 shows
    the rationale is the accurate half. The suite's must-pass set is exactly
    the rows with `status=pass` (`regressMustPass`,
    `internal/testport/regress_suite_test.go:182`), so this row makes the
    pass-required gate permanently red on a feature nobody has built.
    The loop is NOT demoting it: changing a case's must-pass status is a
    governance decision and the documented promotion workflow only covers
    promotion. The two options for the owner:
      (i) correct the row to `status=failed` (the vocabulary's "in-scope
          case, currently diverging"), which is what the measurement
          supports and what the other in-scope-but-diverging cases use; or
      (ii) keep it must-pass and schedule (a)-(d) above as real work.
    Checked whether this is systematic: 21 rows carry `status=pass` with a
    stale "output diverges" rationale, but the full regress suite is green
    on all of them except this one — so for the other 20 the STATUS is right
    and only the rationale text is stale. This is a single-row defect, not a
    consolidation-wide one, and no other must-pass case is affected.
  - **OWNER ANSWER 2026\-09\-25 \(delegated\): option \(i\) — the
    inventory row is corrected to `status=failed`, `pass_required=no`.**
    Finding 1 \(never passed, byte\-identical before the blamed commit\)
    and Finding 2 \(two unimplemented milestone\-scale planner features\)
    make `status=pass` a false claim; the honest state is "in\-scope,
    diverging". Row corrected in
    `docs/test-port/postgres-oracle-target-inventory.csv`; scheduling
    \(a\)-\(d\) is NOT taken — partitionwise work re\-enters planning
    through the normal milestone route if the owner wants it built.
    CLOSED.

- [x] **bpchar-text-function-class — every text function receiving a bpchar
  must apply the rtrim1 cast** (filed 2026-09-22 out of
  AI-20260922-004850-016) — **12 of 13 divergences FIXED**; the 13th
  (the `concat` OPERATOR) is deferred with a stated reason and re-filed
  below.
  Kind: impl
  Parent: none
  Movement: none — SF0.25 `PLAN-SHAPE same=99 changed=0`; a value/rendering
  correctness fix, which none of S3's three instruments measures.
  - **Method: audited by measurement, not by reading.** Built one query
    computing `length(f('ab'::char(6)))` for 22 candidate functions and ran
    it against the PG 18.3 reference (SELECT-only) and a private goopg
    scratch on `:5533`, then diffed. 13 divergences, 9 already correct.
  - **The most important finding is that the rule is NOT uniform, and
    assuming it was would have produced three new bugs.** `concat`,
    `concat_ws` and `format`'s `%s` KEEP the padding in PG (measured: 7, 8
    and 6 against a `char(6)` holding 'ab'), because they take variadic
    `any` and go through the type's OUTPUT function rather than a
    bpchar->text cast. goopg already matched PG on all three. They are now
    pinned in the test **as non-stripping**, so a later loop cannot "fix"
    them into a consistency upstream does not have.
  - **A sibling-path miss from the earlier slice, caught here**:
    `length` was fixed in M0143-0007b slice 1, but its aliases
    `char_length`/`character_length` were NOT — they are a separate `case`
    in the same switch and still returned the padded width. Exactly the
    Hard-won Rule \#2 shape.
  - **Fixed (12)**: `repeat`, `char_length`, `character_length`, `ltrim`,
    `replace`, `translate`, `split_part`, `left`, `right`, `reverse`,
    `quote_literal`, `quote_ident`, `regexp_replace`.
  - Implementation: a new `coerceBpcharArgDatum` normalises the evaluated
    argument ONCE per function, rather than rewriting every
    `s.StringValue()` inside each body — `regexp_replace` alone reads its
    subject four times, and missing one would strip in some branches but
    not others. Non-string datums pass through, so the bytea branches of
    `ltrim`/`reverse`/`btrim` are untouched.
  - Already-correct control group, pinned so it stays that way: `btrim`,
    `rtrim` (they strip trailing blanks themselves, so they agreed by
    accident), `lpad`, `rpad`, `strpos`, `ascii`.
  - Test is non-vacuous: reverting `expr.go` to its pre-fix state fails
    exactly 13 subtests. Witness design follows this task's own warning —
    the length is taken of the FUNCTION'S RESULT, never via `octet_length`
    of a bpchar-typed expression, because `octet_length` re-pads and can
    pass over a live bug.

- [x] **bpchar-concat-operator — `char(n)` concatenation must strip the
  padding** (deferred out of `bpchar-text-function-class`) — **FIXED
  2026-09-22 on BOTH evaluators.** This closes the 13th and last divergence
  the bpchar text-function audit measured, so the whole class is now done.
  Kind: impl
  Parent: none
  Movement: none — SF0.25 `PLAN-SHAPE same=99 changed=0`; a value
  correctness fix, which none of S3's three instruments measures.
  - **The sibling problem this task was deferred for, and how it was
    solved.** goopg has TWO evaluators for a binary operator — interpreted
    (`evalExprSlot`) and compiled (`evalFastExpr`) — and the rule needs the
    operand's DECLARED type, because a blank-padded bpchar datum is
    indistinguishable from a text value that genuinely ends in spaces. The
    interpreted twin can read the expression. The compiled twin CANNOT: by
    evaluation time only Datums remain.
  - Solved by capturing the width at COMPILE time, which is the last point
    the operand expression still exists: `buildExprCtx`'s `*optimizer.BinaryOp`
    arm now stores `declaredBpcharTypmod` for each side in the node payload
    (`payload[8:12]` / `[12:16]`, previously unused; the field is 40 bytes).
    Both evaluators then call ONE shared helper, `concatOperandsAsText`, so
    the rule itself cannot drift between them — only the route by which each
    obtains the width differs.
  - Rejected alternative, recorded so it is not retried: threading the
    operand expressions into `evalBinary` itself. That signature has ~40
    callers on a hot path, and the compiled twin has no expression to pass.
  - **The test drives BOTH twins explicitly** rather than going through SQL,
    and that is deliberate: a SQL-level test exercises whichever evaluator
    the builder happens to pick and can pass with one twin still broken.
    Verified non-vacuous PER TWIN — zeroing only the compiled payload fails
    exactly the compiled assertions while the interpreted ones still pass
    (the precise Rule \#2 split), and neutralising only the interpreted call
    fails exactly the interpreted ones.
  - Third case pinned alongside: a genuine `text` operand whose value really
    does end in spaces must KEEP them (`'xy  ' || 'z'` is 5). That is what
    makes "strip by declared type, never by inspecting the datum" the only
    correct rule, and it would catch a future "just TrimRight the operand"
    shortcut.
  - End-to-end witness on a scratch server, matching PG 18.3:
    `select length(c || 'z')` = 3 for a stored `char(6)` holding 'ab',
    `length('ab'::char(6) || 'z')` = 3, and the text column = 5.
  - Only OpConcat is routed through the helper. Equality and ordering on
    bpchar already ignore trailing blanks inside the comparator
    (`PGCompareBpcharC`), so sending them through would be a second,
    redundant rule on a path that is already correct.

- [x] **testport/TestPort_PgAmcheck003* re-CREATE EXTENSION after restart
  (AI-20260922-004850-002 … -005, was AI-20260921-000212-004 … -007)** —
  **FIXED 2026-09-22.** All four cases (CombinedCorruption, MissingHeapFile,
  MissingIndexFork, SchemaScoped) now PASS, and they genuinely pass — checked
  with `-v` that none of them takes one of these files' `t.Skipf` paths.
  Kind: test-fix
  Parent: none
  Movement: none — a test-fixture fix with ZERO production diff (verified:
  `git status` over `internal/` shows only `*_test.go`).
  - **The engine was right and the fixture was stale.** Each of the four
    unconditionally re-ran `CREATE EXTENSION amcheck` after its
    stop/corrupt/restart cycle, under the comment "Runtime-only amcheck
    install does not survive restart (gap \#7c)". That was written against a
    goopg whose extension install was in-memory only. Catalog DDL durability
    has since landed, so the `pg_extension` row now SURVIVES the restart —
    and the unconditional re-create started failing 42710
    `extension "amcheck" already exists`, which is exactly what PostgreSQL
    raises for a duplicate `CREATE EXTENSION`. Both halves of the new
    behaviour are PG-correct; only the workaround was obsolete.
  - **Fixed as a POSITIVE assertion, not by softening to
    `CREATE EXTENSION IF NOT EXISTS`.** The obvious minimal edit would have
    been `IF NOT EXISTS`, but that passes whether the row survived or not,
    quietly re-admitting the old gap it was written for. Each site now
    asserts `SELECT count(*) FROM pg_extension WHERE extname='amcheck'` is
    `1` after the restart, turning a stale workaround into a regression pin
    on the behaviour that replaced it.
  - Applied to all FOUR files identically — they carried the same stale step
    verbatim, so fixing one and leaving three would have left the same defect
    under three other names.
  - Non-vacuity checked: flipping the expected count makes the assertion
    report the value it actually read from the live server (`= 1`), so it is
    querying rather than trivially succeeding.
  - No inventory change: the AC-003 row is `defer`/not-pass-required and its
    stated blockers (unsupported index AMs, `box`/`int4range`/`int4[]`
    columns, STORAGE EXTERNAL TOAST corruption, multi-database orchestration)
    are untouched by a fixture fix, so the promotion workflow does not apply.
  - Gates: whole `TestPort_PgAmcheck*` family PASS (9.7s, catches sibling
    breakage across the other amcheck ports), units PASS, pgbench smoke via
    the hook. No spotcheck/sf025/acceptance-arm — zero production diff.

- [x] **testport/TestPort_PgoutputInterop* subscriber/publisher-start
  failures (AI-20260922-004850-006 … -015, was AI-20260921-000212-008 …
  -017)** — **FIXED 2026\-09\-23 \(`0734cab60`\).** Design doc
  `docs/design/0100-0149/nightly-control-socket-sun-path.md`.
  Kind: test-fix
  Parent: none
  Movement: none
  - **Root cause, read from the inlined `cluster.log` tail of nightly
    `20260923-001346`:** every failing subscriber died at
    `control listener: … listen unix …/.goopg.ctl.sock: invalid argument`.
    Under the nightly worktree prefix the socket path is 108–136 bytes;
    `sun_path` holds 107 \+ NUL. Only long case names fail; the repo\-root
    prefix is shorter, which is why local re\-runs never reproduced it.
  - Fix: `control.SocketPathFor` keeps `<datadir>/.goopg.ctl.sock` when
    ≤103 bytes, else binds `$TMPDIR/goopg-<16hex sha256\(abs datadir\)>.ctl.sock`;
    clients already dial the pidfile's `SocketPath`.
  - Verified: HEAD binary reproduces the exact error on a 143\-byte socket
    path; the fix starts/status/query/stops and cleans up. Gates: units,
    tpch\-spotcheck, SF0.25 sweep \(plans same=99\), two interop cases.
  - Final confirmation is the next full nightly \(the `20260923` run
    ABORTED at the TPC\-H stage, rc=4, after testport\).
- [x] **testport isolation schedule diffs in the aborted nightly
  `20260923-001346`** — `TestPort_IsolationEvalPlanQual` \(L1092: `c2:
  COMMIT` printed before `read_a`, 1470 vs 1468 lines\),
  `TestPort_IsolationInsertConflictDoUpdate4` \(L34 missing `c2: COMMIT`,
  49 vs 63 lines\), `TestPort_IsolationIntraGrantInplaceDb` \(extra
  `\(0 rows\)` at L28\). Not in `action-items.md` \(the run aborted before
  generating it\); filed by hand. Step 1: re\-run each at HEAD \(`go test
  -v -run '^<name>$' ./internal/testport/`\) — the isolation runner decides
  `<waiting>` by a 300 ms timing window only, so nightly load is a plausible
  cause and a green local run is the stale/flaky verdict; a red one is a
  real schedule divergence to fix. Also record why the nightly aborted
  \(`[S2] tpch: waiting for canonical 65433 to be server-free`, rc=4\) —
  owner infra, escalate rather than touch `:65433`.
  Kind: test-fix
  Parent: none
  Movement: none
  - **DONE 2026\-09\-23 \(ralph2 loop \#7\), fix `ffe1d020f`.** Re\-run at
    HEAD: `EvalPlanQual` PASS \(timing flake\); the other two FAIL — real
    regressions \(both passed nightlies 09\-20/21/22\).
  - `insert-conflict-do-update-4`: `SELECT … FOR UPDATE` over a
    partitioned table → `short read at block`. `git bisect` over the 29
    commits between the nightly SHAs → `d6e42a7f7` \(M0122\-0012 batch
    filter\): its 64\-row read\-ahead moved the scan leaf's `currentTID`
    past the row being emitted, so LockRows locked a row in the OTHER
    partition. Fix: `disableFilterReadAhead` pins every filterOp under a
    currentTID consumer \(LockRows, NL\-stream ctid capture, hash\-join
    preserved ctids\) to the per\-row path. Pin
    `TestForUpdateOnPartitionedTableThroughBatchFilter` fails with the
    exact error without the fix.
  - `intra-grant-inplace-db`: the spec's ACL change on database
    `isolation_regression` now raises 3D000 \(`6ea8424b8`, PG\-faithful\);
    the test ran in `postgres`. PG's harness runs every spec in
    `isolation_regression` \(`isolation_main.c:133`\); the test now
    creates and uses it.
  - Gates: units, tpch\-spotcheck, acceptance arm, SF0.25 sweep \(plans
    same=99\), the whole `TestPort_Isolation*` family \(411 s\) — all PASS.
  - The nightly abort \(S2 tpch lane waiting on the canonical TPC\-H
    cluster, rc=4\) is owner infra; recorded, not touched.
  - **Reproduction attempted faithfully and FAILED to reproduce.** The named
    repro passes at HEAD (2.7s). All ten cases pass run together (62s). And
    the full `./internal/testport/` package — which is what the nightly
    actually runs, the condition a subset run does not recreate — passes
    with ONE failure, the already-known `partition_aggregate`. So under the
    nightly's own conditions, on this machine, at HEAD, the defect is absent.
  - **What the nightly log does establish**, from
    `ci/logs/20260922-004850/testport/go-test.log`:
    - The cases run SEQUENTIALLY (`=== RUN` / `--- FAIL` interleaved, no
      parallel overlap), so a simple concurrent port race between these ten
      is NOT the mechanism.
    - Failures INTERLEAVE with passes — `UnchangedToast`, `MultiDMLXact`,
      `SavepointXact`, `MultiTable`, `ReplicaIdentityUsingIndex`,
      `KillAndReconnect` and `PgbenchKillAsync` all passed in the same run,
      between failing cases. Whatever it is, it is not a monotonic
      degradation of the machine after some point in the suite.
    - Failures are consistently FASTER (2.60-2.74s) than passes
      (2.78-2.94s), which is consistent with dying at startup rather than
      mid-scenario.
  - **Why the root cause is unrecoverable from either night**: the error
    names a `cluster.log` under `tmp/nightly-src-<run>/`, the nightly's
    throwaway worktree, which is deleted when the run finishes. Both nights'
    logs are already gone. The ONLY evidence of why the process exited was
    behind a path that no longer exists.
  - **Landed this loop — make the next occurrence self-diagnosing.**
    `cluster.Start` now INLINES the tail of `cluster.log` (last 4 KiB) into
    both start-failure errors instead of only naming the path, so the next
    nightly carries the bind error / config rejection / panic header in
    `go-test.log` itself and survives the worktree cleanup. Best-effort by
    construction: a failure to read the log degrades to a short note and can
    never replace the start failure being reported.
  - **Next step for whoever takes this: wait for the next nightly and read
    the inlined tail.** Do NOT keep re-running the suite locally hoping to
    catch it — three attempts at HEAD (single case, all ten, full package)
    were all green, so local re-running has a demonstrated zero hit rate.
    If the next nightly is green too, that is itself evidence worth
    recording before closing.
  - Environment hypotheses NOT yet excluded, and which the inlined tail will
    distinguish at no extra cost: an ephemeral-port collision (both
    `freeTCPPort` and `cluster.freePort` use the racy bind-0/close/rebind
    pattern, and other suite stages start servers too), a leftover datadir
    or socket in a fixed `tmp/` path, and host resource exhaustion partway
    through a ~19-minute stage.

### Nightly run 20260924-005446 (1 item) — filed 2026-09-24
- [x] **tpcds/stage** — nightly TPC-DS stage failed at startup: server not
  ready in 120 s
  (AI-20260924-005446-001; repro: `bash ci/batch/stages/stage-tpcds.sh`,
  evidence `ci/logs/20260924-005446/tpcds/`).
  Kind: impl
  Parent: none
  - Triage 2026\-09\-24 \(ralph2 loop \#34\): the goroutine dump shows
    startup blocked in WAL initialisation \(`xlog.NewWriter` →
    `detectWritePos` → `scanLastSegmentEnd` → `os.ReadFile` of a 16 MB
    segment\), which is I/O\-bound. The window 01:42–01:45 overlaps the
    loop\'s own `FORCE=1` SF0.25 sweep and fire\-set gate, which copy
    multi\-GB clones, so host disk contention is the likely cause.
    - Next: re\-run the repro on a quiet host. If it passes, close as stale
      \(induced by the concurrent forced gates\). If it still hangs, look at
      `scanLastSegmentEnd` reading whole segments at startup.
    - Loop lesson: do not run FORCE=1 disk\-heavy gates while the nightly
      batch runs; it can fail the nightly\'s own stages.
    - The same run\'s TPC\-H stage was `skip\(port\-busy\)`, most likely a
      collision with the loop\'s own spotcheck/acceptance\-arm servers at the
      same time.
  - **CLOSED as stale 2026\-09\-24 \(ralph2 loop \#38\).** Re\-ran the real
    stage on a quiet host \(nightly idle, no loop gates running\):
    `REPO\_ROOT=$PWD RUN\_DIR=tmp/nightly\-repro\-043033
    NIGHTLY\_TPCDS\_BIN=tmp/nightly\-repro\-tpcds\-bin
    NIGHTLY\_TPCDS\_QUERIES=3 bash ci/batch/stages/stage\-tpcds.sh` → rc 0.
    - The same 3284 MB SF1 copy, build, and fresh server on :65435 were
      ready in 8 s \(04:30:37 → 04:30:45\), against the 120 s window; the
      spotchecks Q3=31 and Q98=2531 PASS; Q3 ok.
    - Confirms the triage: the 01:42 failure was induced by the loop\'s
      concurrent FORCE=1 gates contending for disk while startup read WAL
      segments. No code change.
    Movement: none

### Nightly run 20260925-002342 (sha `2e923ff37337`, 5 items) — filed 2026-09-25
- [x] **testport/TestE2E_PGColdStartOnGoopgDataDir** — testport TestE2E\_PGColdStartOnGoopgDataDir FAILed
  (AI-20260925-002342-001; repro: `go test -v -run '^TestE2E_PGColdStartOnGoopgDataDir$' ./internal/testport/`,
  evidence `ci/logs/20260925-002342/testport/go-test.log`).
  Kind: impl
  Parent: none
  - Triage 2026\-09\-25: reproduces at HEAD `5247eecea` on a clean
    worktree. The test rejects goopg\'s `postgresql.conf` because it carries
    an active `work\_mem = 512MB` line besides the harness\'s `fsync = off`
    \(`e2e\_pg\_coldstart\_on\_goopgdata\_test.go:204`\); a plain `goopg init`
    must write only PG\'s commented defaults.
  - **OWNER DIRECTION 2026\-09\-25 \(delegated\): fix the template, keep
    the convention.** `goopg init` writes PG\'s commented `work_mem`
    default again \(PG\-faithful\); the 512MB measurement convention is
    carried by the explicit lines already in the bench runtime
    `postgresql.conf` files \(`bench/tpch|tpcds/runtime*/`\), not by the
    init template. Placed in banner item 2a.
  - **FIXED 2026\-09\-25 \(`b6294af13`\).**
    - `internal/utils/misc/postgresql.conf.sample`: the `work\_mem` line
      is commented again, `\#work\_mem = 512MB` \(the BootVal, as
      `TestSampleConfigCoversRegistry` requires; PG\'s 4MB is the parked
      take3\-B\-13 flip\). No effective setting changes: an unset
      `work\_mem` still boots at 512MB.
    - The convention\'s explicit line was already in all five
      bench/reference confs and `bench/tpch/setup\_goopg.sh`;
      `tpcds\-sf025\-regression.sh load\-goopg` now appends it after its
      plain `goopg init`.
    - New unit test `TestSampleConfigHasNoActiveSetting` pins the
      all\-commented template in the unit gate.
    - Gates: units, `TestE2E\_PGColdStartOnGoopgDataDir` PASS,
      tpch\-spotcheck, SF0.25 sweep 96 PASS / 99 plans unchanged.
    - **Owner action:** AGENT.md §"Plan\-parity harness" still says
      "`goopg init` writes the convention automatically via
      `internal/utils/misc/postgresql.conf.sample`". That sentence is now
      stale, and the loop may not edit that section.
  Movement: none
- [x] **testport/TestPort_IsolationEvalPlanQual** — testport TestPort\_IsolationEvalPlanQual FAILed \(reopened: the 2026\-09\-22 task was closed stale\)
  (AI-20260925-002342-002; repro: `go test -v -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/`,
  evidence `ci/logs/20260925-002342/testport/go-test.log`).
  Kind: impl
  Parent: none
  - Recurred in the 2026\-09\-27 nightly \(`AI-20260927-002707-001`\) and
    again in the 2026\-09\-28 nightly \(`AI-20260928-004845-001`\).
  - Recurred again in the 2026\-10\-06 nightly \(`AI-20261006-005659-001`\).
  - **DONE 2026\-10\-10 \(fixed by `2544b2c57`, evidence this loop\)\.** All
    five failing nightlies since 09\-28 \(09\-28, 09\-29, 10\-06, 10\-08, 10\-09\)
    first diverge at a `COMMIT` step printed `<waiting ...>`: a COMMIT slowed
    past the runner\'s old 300 ms timer, never a lock wait\. The lock\-wait
    probe waits for such a step instead\.
    - Under synthetic load \(12 CPU burners \+ a synced 2 GB writer,
      `tmp/m155/load\.sh`\), interleaved: old runner \(`2544b2c57\~1`\) 1/3
      PASS — both failures with the same `COMMIT; <waiting ...>` line —
      vs probe runner 3/3 PASS\. Quiet host: 3/3 PASS\.
    - Same signature on TestPort\_IsolationAlterTable1 \(below\)\.
  Movement: none — test\-runner fidelity; no plan instrument
- [x] **testport/TestPort_IsolationReadWriteUnique4** — testport TestPort\_IsolationReadWriteUnique4 FAILed
  (AI-20260925-002342-003; repro: `go test -v -run '^TestPort_IsolationReadWriteUnique4$' ./internal/testport/`,
  evidence `ci/logs/20260925-002342/testport/go-test.log`).
  Kind: impl
  Parent: none
  - Recurred in the 2026\-09\-26 nightly \(`AI-20260926-011809-001`\),
    again in the 2026\-09\-27 nightly \(`AI-20260927-002707-002`\) and
    again in the 2026\-09\-28 nightly \(`AI-20260928-004845-002`\).
  - Recurred again in the 2026\-10\-02 nightly \(`AI-20261002-010412-002`\).
  - Recurred again in the 2026\-10\-03 nightly \(`AI-20261003-002454-001`\).
  - Recurred again in the 2026\-10\-06 nightly \(`AI-20261006-005659-002`\).
  - Recurred again in the 2026\-10\-07 nightly \(`AI-20261007-014030-002`\).
  - **DONE 2026\-10\-10 \(`0cc3055db`\)\.** Real divergence \(23505 where PG
    raises 40001 in `r1 r2 w1 w2 c1 c2`\)\. The reads plan as a Bitmap Heap
    Scan and `bitmapHeapScanOp` took no SSI predicate lock and made no
    conflict\-out check — so SERIALIZABLE was unenforced for every
    bitmap\-planned read \(S2 class, found and fixed here; escalation in the
    baton\)\. Fix: relation\-grain SIREAD at open \+ per\-TID
    `ssiRecordTupleRead` / `ssiRecordInvisibleTupleRead`, as `indexScanOp`\.
    - Test `TestSSI\_BitmapHeapScanTakesPredicateLocks` \(fails on HEAD\)\.
    - Isolation family all PASS; TemporalRangeIntegrity fixed by the same
      change \(closed below\)\.
    - Gates: units, spotcheck, acceptance arm, fire set none, sf025 PASS=99\.
    - Design `docs/design/0100\-0149/m\-nightly\-bitmap\-heap\-scan\-ssi\.md`\.
  Movement: none — correctness fix; no plan instrument \(benchmarks run READ COMMITTED\)
- [x] **testport/TestPort_IsolationTemporalRangeIntegrity** — testport TestPort\_IsolationTemporalRangeIntegrity FAILed
  (AI-20260925-002342-004; repro: `go test -v -run '^TestPort_IsolationTemporalRangeIntegrity$' ./internal/testport/`,
  evidence `ci/logs/20260925-002342/testport/go-test.log`).
  Kind: impl
  Parent: none
  - Recurred in the 2026\-09\-26 nightly \(`AI-20260926-011809-002`\),
    again in the 2026\-09\-27 nightly \(`AI-20260927-002707-003`\) and
    again in the 2026\-09\-28 nightly \(`AI-20260928-004845-003`\).
  - Recurred again in the 2026\-10\-02 nightly \(`AI-20261002-010412-003`\).
  - Recurred again in the 2026\-10\-03 nightly \(`AI-20261003-002454-002`\).
  - Recurred again in the 2026\-10\-06 nightly \(`AI-20261006-005659-003`\).
  - Recurred again in the 2026\-10\-07 nightly \(`AI-20261007-014030-003`\).
  - **DONE 2026\-10\-10 \(`0cc3055db`\)\.** Same root cause as
    ReadWriteUnique4 above \(SERIALIZABLE bitmap scans held no SIREAD\); PASSes
    standalone and in the isolation family with that fix\.
  Movement: none — correctness fix; no plan instrument
- [x] **testport/TestE2E_PGStandbyFullCycle** — testport TestE2E\_PGStandbyFullCycle FAILed
  (AI-20261012-033252-001; repro: `go test -v -run '^TestE2E_PGStandbyFullCycle$' ./internal/testport/`,
  evidence `ci/logs/20261012-033252/testport/go-test.log`).
  Kind: impl
  Parent: none
  - Reproduced 2026\-10\-12 on a clean HEAD \(`9b1d866dc`\) worktree, so
    not caused by uncommitted WIP: the goopg reverse standby exits at start
    with `postgresql\.auto\.conf:4: unknown parameter "log\_min\_messages"`
    and `:5: unknown parameter "log\_error\_verbosity"`\. PASSed in the
    2026\-10\-10 nightly \(`7633ac7`\); goopg began reading the auto file
    with ALTER SYSTEM \(`27a3009c5`\), and the PG primary\'s auto file names
    two parameters goopg does not register\.
  - First step: register `log\_min\_messages` / `log\_error\_verbosity`
    \(guc\_tables\.c enums\), or check whether PG\'s own start would warn
    rather than fail on an unknown auto\.conf entry\.
  - **DONE 2026\-10\-12 \(`a5a86654a`\)\.** PG\'s start fails on an unknown config name too
    \(ProcessConfigFileInternal\), so the gap was the two missing GUCs: both
    registered with guc\_tables\.c\'s options; new `Variable\.EnumHidden`
    models hidden enum rows \(debug → debug2; client\_min\_messages info\)\.
    Second cause: the copied auto file names the OLD primary and overrides
    postgresql\.conf, so the test helper now appends the standby settings
    to postgresql\.auto\.conf as pg\_basebackup \-R does\.
    - Tests: TestE2E\_PGStandbyFullCycle and all 31 TestE2E\_\* PASS;
      `TestMessageLevelGUCs`\.
    - Gates: units, internal/postmaster, regress guc/sysviews unchanged,
      tpch\-spotcheck, sf025 PASS=99\.
    - Design `docs/design/0100\-0149/m\-nightly\-standby\-auto\-conf\-message\-level\-gucs\.md`\.
  Movement: none — test\-harness / GUC registry; no plan instrument
- [x] **testport/TestPort_IsolationAlterTable1** — testport TestPort\_IsolationAlterTable1 FAILed
  (AI-20261007-014030-001; repro: `go test -v -run '^TestPort_IsolationAlterTable1$' ./internal/testport/`,
  evidence `ci/logs/20261007-014030/testport/go-test.log`).
  Kind: impl
  Parent: none
  - Triage 2026\-10\-07 \(ralph2 loop\): there is one divergence,
    `step sc1: COMMIT; <waiting ...>`, where PG prints a plain
    `step sc1: COMMIT;`. The rest of the diff is the one\-line shift that
    follows from it. The isolation runner decides `<waiting>` by a 300 ms
    timeout alone \(no pg\_locks probe\), and the run overlapped this
    loop\'s FORCE=1 SF0\.25 sweep and fire\-set gate \(02:29–02:59\). This is
    most likely a load\-induced timing flake, not an engine defect\.
    - Next: re\-run the repro on a quiet host. If it passes, close as stale.
      If not, check whether a NOT VALID foreign\-key COMMIT really blocks.
    - 2026\-10\-10: same `COMMIT; <waiting ...>` signature that the
      EvalPlanQual task proved, under load, is the old runner\'s 300 ms timer;
      `2544b2c57` \(lock\-wait probe\) should resolve it — verify under the same
      load A/B before closing\.
  - **DONE 2026\-10\-10 \(no code; structural fix is `2544b2c57`\)\.** One
    failure in 14 nightlies \(10\-07, overlapping a FORCE=1 sweep \+ fire set\);
    the other 13 pass\. Its only divergence is `step sc1: COMMIT; <waiting
    ...>` — the slow\-COMMIT signature the EvalPlanQual load A/B pinned on the
    old 300 ms runner timer\. Under the same synthetic load
    \(`tmp/m157/load\.sh`\) neither runner reproduced it here \(old 4/4, probe
    4/4 PASS; quiet 2/2\), so this closes on mechanism \+ history, not a
    reproduction\.
  Movement: none — flake; test\-runner fidelity fix already landed
- [x] **tpcds/stage\-startup\-20261007** — recurrence of the closed `tpcds/stage` task: nightly TPC\-DS stage failed at
  startup: server not ready in 120 s
  (AI-20261007-014030-004; repro: `bash ci/batch/stages/stage-tpcds.sh`,
  evidence `ci/logs/20261007-014030/tpcds/`).
  Kind: impl
  Parent: none
  - Triage 2026\-10\-07 \(ralph2 loop\): this is the class the closed
    2026\-09\-24 entry described\. The goroutine dump shows startup in
    `initdb/xact_recovery\.go` → `xlog/recovery_cache\.go` →
    `xlog/reader\.go` `os\.ReadFile` of WAL segments\. The listener bound
    at 02:37:51, five minutes after the 02:32:51 start\. The window again
    overlapped this loop\'s FORCE=1 SF0\.25 sweep and fire\-set gate,
    which copy multi\-GB clones\.
    - Next: re\-run on a quiet host\. Independently, startup recovery reads
      whole 16 MB segments with `os\.ReadFile`; a slow disk turns that into
      minutes\. Check whether xact\_recovery needs to scan the full
      segment range at all\.
    - Loop lesson \(repeated\): the nightly batch runs from about 01:40\.
      Disk\-heavy FORCE=1 gates in that window fail its stages\.
  - **DONE 2026\-10\-10 \(`fd694e4f2`\)\.** The stage passed in the three
    nightlies since \(startup 7\-11 s\); the 10\-07 failure was host contention
    amplified by the reader: `readStreamFrom` read every pg\_wal file until
    ENOENT — on SF1 one live segment plus 70 zero\-filled preallocated ones,
    1\.1 GB per start\. Now `lastLiveSegment` probes each first\-page header
    \(`xlogPageValidator`, as `XLogReaderValidatePageHeader`\) and the read
    stops after the last segment valid for its own address; holes before it
    are still read \(`durableWALAfter` unchanged\)\.
    - Private SF1 clone: startup HEAD 9\.4/7\.1 s → 1\.2/1\.2 s to listener\.
    - Tests `TestReadStreamStopsAtPreallocatedTail`,
      `TestReadStreamReadsThroughAMidStreamHole`\.
    - Gates: units, testport restart/crash/recovery subset, spotcheck,
      acceptance arm, fire set none, sf025 PASS=99\.
    - Design `docs/design/0100\-0149/m\-nightly\-wal\-reader\-live\-tail\.md`\.
  Movement: none — startup I/O fix; no plan instrument
- [x] **units/internal/access/nbtree** — units suite failed in package internal/access/nbtree
  (AI-20261002-010412-001; repro: `go test -timeout 10m ./internal/access/nbtree/`,
  evidence `ci/logs/20261002-010412/units/go-test.log`).
  Kind: impl
  Parent: none
  - Failing test: `TestConcurrentInsertSearch` — `btree\_test\.go:319: no
    successful concurrent searches recorded` \(0\.04s\); first seen
    2026\-10\-02, new tonight\. Looks timing\-dependent \(the searchers may
    not overlap the inserters on a loaded host\); triage before treating as
    a regression\.
  - **DONE 2026\-10\-10 \(`4bf9e3168`, test\-only\)\.** Test race, not a
    B\-tree defect: readers checked `stop` before their first Search, so a
    writer that finished first left zero searches\. Now a start barrier
    \(writer waits for every reader\) plus search\-before\-stop\. 50x, 5x
    `\-race`, `GOMAXPROCS=1` 100x PASS; units nightly\-green otherwise since
    10\-03\.
  Movement: none — test\-only
- [x] **testport/TestPort_RegressSuite** — testport TestPort\_RegressSuite FAILed \(must\-pass subtests: portals\_p2, union; reopened: the 2026\-09\-22 task was closed\)
  (AI-20260925-002342-005; repro: `go test -v -run '^TestPort_RegressSuite$' ./internal/testport/`,
  evidence `ci/logs/20260925-002342/testport/go-test.log`).
  Kind: impl
  Parent: none
  - Triage 2026\-09\-25: `union` PASSes at HEAD \(`b1f93fb81`\).
    `portals\_p2` passes alone but FAILs after `create\_index` with a wrong
    result: a bitmap scan over an unproven partial index → M0145\-0008r
    \(S2 escalation\).
  - `union` PASSes alone but FAILs in the full suite, on a clean HEAD
    worktree too: `count\(\*\)` over `INTERSECT` returns a nondeterministic
    1660 / 1721 \(PG 5000\), a HashSetOp run per worker over Parallel Seq
    Scans → M0145\-0008s \(S2 escalation\). Close this item when 0008r and
    0008s land.
  - **CLOSED 2026\-09\-25:** M0145\-0008r \(`f56d94e86`\) and M0145\-0008s
    \(`031e8452c`\) landed; the full `TestPort\_RegressSuite` PASSes
    \(`analysis/m0145/m0145\-0008s/regress\-suite\-result.txt`\).

### Nightly run 20260927-002707 (sha `104c2c90ba04`, 3 items) — filed 2026-09-27
- [x] **testport isolation SSI divergences at HEAD** \(AI\-20260927\-002707\-002
  / \-003; repro at HEAD `3f3877a88`: `go test \-v \-run
  '^TestPort_IsolationReadWriteUnique4$|^TestPort_IsolationTemporalRangeIntegrity$'
  \./internal/testport/` — both FAIL on a capped private run\).
  `ReadWriteUnique4`: a permutation expects `could not serialize access`
  where goopg raises `duplicate key value violates` — unique\-check vs
  SSI\-predicate ordering\. `TemporalRangeIntegrity`: the permutation
  `rx1 wy1 ry2 wx2 c1 c2` reads a row SSI should have killed \(expected
  379 output lines, got 361 — a whole permutation\'s tail diverges\)\.
  NOT the M0146\-0005x optimizer work — the tests are MVCC/isolation
  scheduler\-level, both failed identically in the previous nightly, and
  `internal/testport` is outside the commit gate\.
  `TestPort_IsolationEvalPlanQual` \(AI \-001\) re\-ran PASS at HEAD —
  stale item, no entry needed\.
  Kind: impl
  Parent: none
  - First step: reproduce each against PG 18\.3 \(the ported spec is the
    oracle\) and locate the serialization\-failure emission point — the
    divergence classes are \(a\) unique violation raised before the SSI
    rw\-dependency check and \(b\) a committed read that should have
    tripped a dangerous\-structure abort\.
  - **DONE 2026\-10\-10 \(fixed by `0cc3055db`; duplicate of the two
    testport tasks closed there\)\.** Both divergence classes had one cause:
    the reads plan as a Bitmap Heap Scan, which took no SSI predicate lock
    and made no conflict\-out check\. Both specs PASS at HEAD `59a8d7f04`\.
  Movement: none — correctness fix; no plan instrument

### Nightly run 20260928-004845 (sha `2ddc97fddc1f`, 4 items) — filed 2026-09-28
- [x] **testport/TestPort_RegressSuite/limit re-fails** \(AI\-20260928\-004845\-004;
  repro: `go test \-v \-run '^TestPort_RegressSuite$/^limit$' ./internal/testport/`,
  evidence `ci/logs/20260928-004845/testport/go-test.log`\). New tonight: the
  must\-pass `limit` regress subtest output diverges again after the
  2026\-09\-18 `FETCH BACKWARD` fix \(closed task above\); the nightly
  message says "output mismatch; normalization rules need extension"\.
  The three isolation AI items \-001/\-002/\-003 recur — recurrence
  noted on their open tasks above; no new entries needed\.
  Kind: impl
  Parent: none
  - First step: run the subtest and diff `limit.out` against
    `postgres/src/test/regress/expected/limit.out` — identify whether the
    mismatch is a new normalization gap or a cursor/fetch regression
    \(the 2026\-09\-18 fix touched `executeFetch`'s backward\-position
    bookkeeping\).
  - **DONE 2026\-10\-10 \(no code; resolved upstream of this task\)\.** It
    failed only on the 09\-28 and 09\-29 nightlies and has PASSed in all 11
    since \(09\-30 → 10\-10\), plus 5/5 fresh runs at HEAD `59a8d7f04`\. The
    nightly log keeps no diff, and 22 executor/optimizer commits landed
    between the last failing and first passing run, so the fixing commit is
    not identified\.
  Movement: none — stale nightly item

### Manually discovered (not yet in a nightly `ci/logs/action-items.md` run) — filed 2026-09-15
- [x] **goopg\'s freeze WAL record is unreadable to real PostgreSQL:
  `xlhp\_freeze\_plan` is written as 11 bytes, PG\'s struct is 12** \(found
  2026\-09\-25 by M0145\-0008v S2\). PG pads one byte after `frzflags`
  before `ntuples` \(`heapam\_xlog.h`\); `EncodeHeapFreezePG`
  \(`pg\_assembled\_emit.go`\) and goopg\'s decoder \(`sizeOfXLHPFreezePlan =
  11`, recovery.go\) both omit it. PG 18.3 `pg\_waldump` shows goopg\'s freeze
  records as `ntuples: 256, offsets: \[768, 1280, …\]` for one plan of one
  slot; a real PG standby would misapply every freeze plan.
  Kind: impl
  Parent: none
  - First step: pad the plan to 12 bytes on both sides \(encoder and
    `decodeXLogHeapPrune`\), keep reading 11\-byte plans from existing WAL
    if old segments must replay, and pin with a `pg\_waldump` check.
  - **FIXED 2026\-09\-25 \(`0b37c784b`\).** Evidence
    `analysis/m\-nightly/freeze\-wal\-plan\-padding/`; design doc
    `docs/design/wal\-pg\-identical\-stream/freeze\-plan\-padding.md`
    \(corrects the A7\-freeze entry of the WAL tracker, which is over the
    D3 size limit\).
    - Encoder and decoder use PG\'s 12\-byte plan; the decoder still reads
      the legacy 11\-byte form, recognised by its odd block\-data length.
    - PG 18.3 `pg\_waldump` now prints `ntuples: 3, offsets: \[1, 3, 5\]`
      \(before: `ntuples: 256`\). `findPGWaldump` now finds the repo\'s PG
      install, so the waldump tests run in the gates instead of skipping.
    - Gates: units, tpch\-spotcheck, SF0.25 sweep \(99 same\),
      `TestE2E\_PGStandbyFullCycle`, Recovery001/013/039 — all PASS.
  Movement: none

  > ## ESCALATION 2026\-09\-25 \(S2\) — a PG standby would corrupt frozen pages
  >
  > goopg\'s freeze records decode wrongly in real PostgreSQL \(missing C
  > struct padding\). Filed and not selected ahead of the banner, per S2;
  > the owner decides its placement.
  >
  > **OWNER ANSWER 2026\-09\-25 \(delegated\):** placed in banner item
  > 2a, fourth of the batch \(after 0008r, 0008s, 0008m\). No live PG
  > standby consumes goopg WAL today, so it ranks below the
  > query\-visible wrong results but ahead of parity work.


- [x] **`CREATE FUNCTION` completes with command tag `OK`, not `CREATE
  FUNCTION`** \(found 2026\-09\-23 while diffing a GRANT script against a
  private PG 18.3: psql printed `OK` where PG prints `CREATE FUNCTION`\).
  Kind: bug
  Parent: none
  - Suspects: the postmaster's generic tag fallbacks
    \(`internal/postmaster/dispatch.go` `return "OK"` at several sites,
    `dispatch_extended.go:620`\) — a DDL kind with no explicit tag arm.
    PG sets the tag from the statement's CommandTag
    \(`./postgres/src/include/tcop/cmdtaglist.h` `CMDTAG_CREATE_FUNCTION`\).
  - Step 1: reproduce over simple and extended protocol; list every DDL
    kind that falls back to `OK` \(the TS\-config ALTER case was fixed the
    same way in M0119\-0004\), fix them as one class.
  Movement: none
  - **FIXED 2026\-09\-23 \(ralph2 loop \#9\), `6de0daf09`.** Design doc
    `docs/design/0100-0149/command-tag-ok-fallback-class.md`.
    - A live sweep against a private PG 18.3 found **29** kinds falling to
      `OK` \(functions/procedures/routines, triggers, event triggers,
      sequences, rules, policies, matviews, publications, subscriptions,
      access method, ALTER OPERATOR, DO, REINDEX, CLUSTER\). One table,
      `stmtCommandTag`, is now the fallback of both `ddlTag` and
      `utilityTag`.
    - Same class: a populating CTAS / SELECT INTO / CREATE MATERIALIZED
      VIEW now completes with `SELECT <n>` \(createas.c:349,
      matview.c:389\) via `executor.DDLProcessedReporter`, forwarded by
      `OpIterator`; CTAS WITH NO DATA → `CREATE TABLE AS`.
    - Gates: units, tpch\-spotcheck, SF0.25 sweep, acceptance arm,
      `TestPort_RegressSuite` — all PASS.
- [x] **six divergences found by the 2026\-09\-23 command\-tag sweep**
  \(each measured live against a private PG 18.3\).
  Kind: bug
  Parent: none
  - `ALTER RULE r ON t RENAME TO r2` completes but does not rename: a
    following `DROP RULE r2 ON t` fails `rule "r2" for relation "t" does
    not exist`.
    - **FIXED 2026\-09\-23 \(ralph2 loop \#10\), `bc75b691f`.** The rename
      touched only `tbl.Rules` while DROP RULE reads the compat registry
      key `name@table`; now both move \(and action\-form rules, registry\-
      only, became renameable\). Same function had a second defect:
      `DROP RULE IF EXISTS` on an EXISTING rule skipped instead of dropping.
      A 16\-statement script is byte\-identical to PG 18.3; pinned by
      `TestAlterRuleRenameThenDrop`. Gates: units, spotcheck, SF0.25,
      acceptance arm, regress suite.
  - `LOCK TABLE t` outside a transaction block is accepted; PG raises
    25P01 `LOCK TABLE can only be used in transaction blocks`
    \(`./postgres/src/backend/tcop/utility.c` RequireTransactionBlock\).
    - **FIXED 2026\-09\-23 \(ralph2 loop \#11\), `40ae756e9`.** 25P01 at top
      level \(`RoutineDepth == 0`\), tag `LOCK TABLE`. Two prerequisites
      fixed with it: DO blocks and CALL bodies now raise `RoutineDepth`
      \(`enterRoutineBody` — PG runs them through SPI, never top\-level\);
      PL/pgSQL now passes non\-DML SQL through as `stmt_execsql` does
      \(`lock table t;` used to fail to parse\). Live script identical to
      PG 18.3 except NOTIFY inside a routine body \(ledgered; see the NOTIFY
      item below\). Gates: units, spotcheck, SF0.25, acceptance arm,
      regress suite, isolation family.
  - `REINDEX TABLE t` on a table with no indexes lacks PG's NOTICE `table
    "t" has no indexes to reindex`.
    - **FIXED 2026\-09\-24 \(ralph2 loop \#40\), `804cfe74b`.** NOTICE when no
      index and no TOAST relation; CONCURRENTLY wording except on temp
      tables; silent for partitioned parents and system catalogs; name
      without schema. A 16\-statement probe is byte\-identical to PG 18.3;
      pinned by `TestReindexTableNoIndexNotice`. Gates: units, spotcheck,
      sf025, acceptance arm, regress reindex\_catalog.
      - Ledgered: goopg\'s TOAST\-able type set is narrower than PG\'s
        \(tsvector, numeric, inet … have none\), which leaves one extra
        NOTICE in upstream create\_index.
  - A session's own `LISTEN ch; NOTIFY ch` reports `received from server
    process with PID 1` — the notifying backend's PID is wrong.
    - Also: NOTIFY/LISTEN inside a routine body \(DO, function,
      procedure\) fails `unsupported statement type *parser.NotifyStmt` —
      they are handled only by the postmaster; PG allows them anywhere
      \(ledgered 2026\-09\-23\).
    - **REFUTED 2026\-09\-24 \(ralph2 loop \#41\).** The self\-notification
      carries this backend\'s own PID, equal to `pg\_backend\_pid\(\)` \(both
      read 1\). goopg numbers backends with a per\-server counter
      \(`s.nextPID`\) rather than OS PIDs, uniformly across BackendKeyData,
      pg\_stat\_activity, cancel and NOTIFY — an architectural divergence,
      ledgered, not a NOTIFY defect.
  - `DISCARD ALL` is a syntax error \(PG: `DISCARD ALL`\).
    - **FIXED 2026\-09\-24 \(ralph2 loop \#41\), `02e57b0c8`.** Grammar
      accepts it; every form gets its own tag \(DISCARD ALL / PLANS /
      SEQUENCES / TEMP — the bare "DISCARD" tag was a second divergence\);
      DiscardAll semantics: 25001 in a block, else session authorization,
      role, RESET ALL, advisory locks, temp tables, sequences \(executor\)
      and prepared statements, cursors, LISTEN \(postmaster, both paths\).
      A 27\-statement probe matches PG 18.3 apart from the work\_mem default
      \(filed below\). Regress guc 452 → 420 diff lines.
  - `CREATE PUBLICATION` lacks PG's WARNING `"wal_level" is insufficient
    to publish logical changes` when wal\_level is not `logical` \(check
    goopg's effective wal\_level first — the warning may be correctly
    absent\).
    - **FIXED 2026\-09\-24 \(ralph2 loop \#42\), `a510ecd68`.** goopg\'s
      wal\_level boots at `replica` like PG, so the warning was genuinely
      missing. Now emitted with its 55000 code and HINT \(hint\-carrying
      warnings added to the executor Context\).
      - Found with it: the extended protocol dropped every executor NOTICE
        and WARNING \(DROP IF EXISTS, REINDEX, …\). Both paths now share
        `executorNoticeFrames`. Verified live with psql `\bind`; the HEAD
        build printed none of the messages.
      - Regress publication \+12 lines: goopg accepts six CREATE
        PUBLICATIONs PG rejects, and now warns on them \(ledgered\).
  - **All six closed 2026\-09\-24** \(two fixed earlier, REINDEX NOTICE
    `804cfe74b`, DISCARD ALL `02e57b0c8`, NOTIFY PID refuted, this one\).
    Movement: none
  - Take one per loop; each is small and independently testable.

- [x] **pg\_get\_indexdef prints a Go struct for a COLLATE expression in a
  partial\-index predicate** \(found 2026\-09\-24 in the upstream create\_index
  diff\): `... WHERE (c1::text > &{105 0x2d861162a1e0 C})` where PG prints
  `(c1)::text > 'A'::text COLLATE "C"`. The deparser formats a
  collate/constant node with `%v`; the pointer also makes the regress diff
  nondeterministic.
  Kind: bug
  Parent: none
  - **Closed 2026\-09\-24 `e8277c163`.** Both deparser twins
    \(`defaultExprToSQL`, `catalog.formatExprForAttrdef`\) gained
    get\_rule\_expr\'s T\_CollateExpr arm. The same predicate also failed
    CREATE INDEX \(XX000 "column ref c2/1 on nil slot"\):
    `exprContainsColumnRef` walked without a CollateExpr arm and const\-folded
    it. It now uses the exhaustive `walkExprRefs`. Regress create\_index:
    the nil\-slot error and three Go\-struct rows are gone.
    Movement: create\_index diff 3338 → 3322 lines
  - Remaining, ledgered: PG prints casts as `(c1)::text`
    \(get\_coercion\_expr\); goopg prints `c1::text`.

- [x] **work\_mem boots at 512MB; PostgreSQL\'s default is 4MB** \(found
  2026\-09\-24 by the DISCARD ALL probe: `RESET ALL` returns each server to
  its own default\). `internal/utils/misc/defaults.go` registers work\_mem with
  `BootVal: "512MB"`, undocumented, against the rule that GUC boot values are
  PG 18\'s \(`guc\_tables.c`: 4096 kB\).
  Kind: impl
  Parent: none
  - **OWNER DECISION 2026\-09\-24 \(owner\-directed change, devin\):** the
    benchmark/measurement question is resolved the OTHER way — instead of
    moving goopg\'s boot value to PG\'s 4MB, **every measurement cluster on
    both engines carries `work_mem = 512MB` explicitly in
    `postgresql.conf` and no session `SET` may override it**. The conf line
    is the single alignment mechanism; the capture scripts
    \(`capture-tpch.sh`, `capture-tpcds.sh`, `goopg-margin-census.py`\) now
    issue no `SET` and verify the values via `SHOW` instead.
    Landed: five cluster confs \(:65432/:65433/:65436/:65437/:65438\),
    `setup_goopg.sh`/`setup_pg.sh` templates,
    `internal/utils/misc/postgresql.conf.sample` \(uncommented — `goopg
    init` writes the convention automatically\), a `server.sh` warning when
    a TPC\-DS conf leaves `work_mem` implicit, AGENT.md harness rule,
    `bench/tp*/README.md`, `m0137-0003` procedure, and the `plans-pg`
    fixtures re\-captured under 512MB.
  - Residual, deliberately parked — NOT required for the convention:
    goopg\'s GUC BootVal remains `512MB` where PG\'s is 4MB, visible via
    `SHOW work_mem`/`RESET ALL` on a cluster whose conf omits the line.
    Flipping the BootVal to 4MB stays parked behind take3\-B\-13\'s stated
    prerequisites \(spill\-cost calibration + a work_mem sweep; deferral
    ledger `take3-B-13-deferred-2` measured real plan regressions at 4MB\).
    Since measurement now never reads the boot value, that flip is a pure
    SHOW-fidelity item — an ordinary M-NIGHTLY-scale task, no longer
    benchmark-blocking. If it ever lands, the conf/sample convention above
    keeps measurement at 512MB regardless.
  - **Incident record 2026\-09\-24 — the first post\-change tpch\-spotcheck
    appeared to hang \(Q12 running >8 min\) and initially looked like an
    executor defect exposed by the 512MB plan flip \(Q12 now elects
    Nested Loop + per\-row `orders_pk` index probes, matching the freshly
    recaptured PG 18\.3 512MB shape\). Investigation showed the cause was
    NOT the executor: `goopg-workloads.slice` sat at ~25\.4GB against
    `memory.high` ~23\.6GB with PSI `some avg10≈78` because the voided
    SF0\.25 sweep had left its server on :65437 holding ~12\.4GB plus the
    SF1 server ~9\.7GB. Every scope in the slice was stalled in direct
    reclaim \(the earlier 5\.7MB\-stalled `pg_basebackup` clone and the
    voided sweep's Q2/Q4/Q5 timeouts are the same signature\). After
    stopping the idle :65437 server, the IDENTICAL spotcheck re\-ran
    clean: Q12 4\.84s rows=2 PASS, Q13 17\.71s rows=33 PASS. No executor
    code was changed — there was nothing to fix.
    - Lesson recorded: a spotcheck/gate run must first reap leftover
      benchmark servers \(`bench/tpcds/server.sh stop sf025`/`sf1`\) when
      the slice is saturated; PSI on `goopg-workloads.slice` is the
      diagnostic.
    - Watch item \(not a defect\): Q13 at ~17\.7s vs ~5s historical under
      the new 512MB plan shape — track via the next sf025/tpch timing
      baseline rather than treating as a regression now.
    - Note: the PASS run still stamped FAIL via gate\-stamp dirty\-code
      detection — `internal/`/`cmd/` had unstaged changes \(this task's
      `cmd/estimate-audit` edit plus a concurrent loop's WIP\); the gate
      verdict itself was PASS and the binary under test was the
      current\-tree build.

- [x] **WRONG RESULTS: an index\-only prefix probe on a composite index
  skips entries whose trailing key column is NULL** \(found 2026\-09\-23 by
  an M0145\-0029 slice\-2 correctness probe; default pipeline affected\).
  Kind: bug
  Parent: none
  - Repro \(executor fixture, `newVMFixture`\): `CREATE TABLE r2 \(a int, b
    int\)`; `CREATE INDEX r2_ab ON r2 \(a, b\)`; `INSERT INTO r2 SELECT g / 3,
    g % 5 FROM generate_series\(1, 5000\) g`; `INSERT INTO r2 VALUES \(10,
    NULL\)`; `ANALYZE r2`. Then `SELECT a FROM r2 WHERE a = 10` plans a bare
    `IndexOnlyScan` \(`Key` = 10, a one\-column prefix of `r2_ab`\) and
    returns **3** rows; PG returns 4. `WHERE a \+ 0 = 10` \(no index\)
    returns 4.
  - Legacy default: `WHERE a = 10 AND b IS NULL` also plans an
    `IndexOnlyScan` and returns **0** rows \(PG: 1\).
  - Suspect: the prefix probe's upper bound
    \(`compositeUpperBound` → `appendCompositeUpperPadding`,
    `internal/executor/pgindex_btree.go:122`, reached from
    `operators_indexonly.go`'s `Key`/`Keys` arms\) stops before entries whose
    trailing column is NULL, which a btree sorts after every non\-NULL value
    \(NULLS LAST\). Check whether the plain `IndexScan` prefix probe, the
    bitmap probe and the tuple\-format \(`pgIndexKeyDesc` \!= nil\) path share
    it.
  - **Wider than index\-only \(2026\-09\-23, loop \#15 probe\):** the plain
    `IndexScan` prefix probe misses the row too — `SELECT a, b, c FROM s2
    WHERE a = 10` on `\(a, b\)` plans `IndexScan … Key = 10` and returns 3,
    PG 4 — and so does the SAOP multi\-descent \(`a IN \(10, 11\)` → 6, PG
    7\). Every composite prefix probe \(`Key`, short `Keys`, `SAOPKeys`\) is
    affected. This probe ran on the jointree arm; the legacy default
    reaches the same executor arms \(its index\-only case is verified
    above\).
  - **ROOT CAUSE found 2026\-09\-23 \(`98f68622d`\):** the byte\-key btree stores
    NO entry whose key has a NULL column \(`collectBTreeEntries` in
    `internal/executor/operators_ddl.go`: "not storable in the byte\-key
    btree \(no null bitmap\)"; runtime maintenance skips them too\). Any
    index scan that does not itself exclude NULLs in some key column misses
    those rows. The jointree restriction producers now decline such indexes;
    the rule\-based, bitmap, parameterised and ordered producers do not yet.
    Real fix: a NULLS\-LAST NULL encoding so the entries are stored.
  - **FIXED \(wrong results\) 2026\-09\-24 \(ralph2 loop \#39\), `821eda191`,
    by the interim guard.** Every remaining index producer now refuses a
    scan that leaves a nullable key column unbound, unless the query\'s own
    quals strictly restrict it.
    - Covered: `findBTreeIndexForColumn` \(quals\-aware\), nested\-loop
      probes, base and parameterised bitmaps, and the full\-scan producers
      \(ordered index paths, index\-ordered GROUP BY, seqscan\-off ordered
      index\-only promotion\).
    - Found wider than filed: a GROUP BY over index order lost its NULL
      group, and a merge LEFT JOIN lost NULL\-keyed outer rows.
    - Tests: `TestIndexProbesKeepNullKeyedRows`,
      `TestIndexFullScansKeepNullKeyedRows`.
    - Gates: units, spotcheck, sf025 \(plan\-shape changed=0\), acceptance
      arm, fire\-set — PASS; regress pass\-required subset status unchanged
      \(two index plans lost over nullable tenk1 columns\).
    - Design doc `docs/design/0100-0149/m-nightly-index-null-key-guard.md`.
      The storage fix is filed below.
    Movement: none
- [x] **store NULL\-keyed index entries \(the real fix behind the NULL\-key
  guard\)** \(filed 2026\-09\-24\). PostgreSQL stores index tuples with NULL
  key columns; goopg skips them, so the planner must refuse valid index
  plans \(upstream `btree\_index`\'s composite SAOP probe, `limit`\'s
  GroupAggregate over an index\-only scan\).
  Kind: impl
  Parent: none
  - Scope, in the tuple format only: writers keep the entry \(uniqueness
    still skips NULL keys\); the key\-change fingerprint distinguishes NULL;
    open\-ended range bounds stop at the first NULL in the bounded column
    \(`\_bt\_checkkeys`\); index\-only scans decode NULL attributes; amcheck
    expects them. Then relax the guard for tuple\-format indexes. Indexes
    built earlier need REINDEX.
  - Design doc §"The real fix".
  - **Design slice 2026\-09\-24 \(ralph2 loop \#43\):**
    `docs/design/0100-0149/m-nightly-store-null-keyed-index-entries.md`.
    - Audit: probe values skip NULL on every path \(row\-derived probes must
      keep that\); open\-ended ranges \(`a > 5`, no recheck on the legacy
      single\-conjunct shape\) and NULLS FIRST low ends would leak stored
      NULLs; writers, the arbiter entry and amcheck assume none; VACUUM and
      HOT need no change; the optimizer cannot see the index format.
    - Old indexes: a cluster capability flag written at initdb
      \(`global/pg\_goopg\_features`\), not a per\-index marker \(none is
      durable without a visible reloption\).
  - [x] **store\-null\-keys S1 — readers stop at NULL** on open\-ended bounds
    \(index, index\-only, bitmap range probes; NULLS FIRST low end\). Design
    §Slices 1.
    Kind: impl
    Parent: none
  - [x] **store\-null\-keys S2 — writers store NULL\-keyed entries behind the
    cluster flag** \(marker file, build/runtime/arbiter writers, amcheck\).
    Design §Slices 2. May land with S1.
    Kind: impl
    Parent: none
    - **S1\+S2 LANDED 2026\-09\-24 \(ralph2 loop \#44\), `83f5635c8`.**
      Capability marker `global/pg\_goopg\_features`; writers \(bulk,
      runtime, arbiter — the arbiter had a separate gap, a NULL conflict key
      skipped its entry\) keep NULLs in capable clusters; one\-sided ranges
      stop at the NULL group; amcheck expects the entries.
      - Proof: heapallindexed on three indexes incl. after kill \-9 \+ WAL
        replay; range shapes vs the no\-index answer, each failing with the
        stop disabled; regress on a capable cluster identical to HEAD.
      - Gates: units, spotcheck, sf025 \(changed=0\), acceptance arm.
      Movement: none
  - [x] **WRONG RESULTS: range scans over DESC index columns** \(found and
    fixed 2026\-09\-24, ralph2 loop \#45, `63d720bd2`, while preparing S3\).
    The tuple\-format comparator honours DESC, but the range scan used the
    ascending ends: on \(a DESC\), `a > 97` returned 2910 of 3000 rows \(want
    60\) and BETWEEN returned none — in every cluster.
    - Fix: value bounds become index\-order bounds \(`rangeColumnReversed`
      swaps ends and strictness\) in the index and index\-only range scans;
      the NULL stop works in index order, so DESC columns get it; a SAOP
      probe leaves DESC second\-column bounds to its Filter recheck.
    - `TestDescIndexRangeScans`: 11 shapes vs the no\-index answer, capability
      off and on; 16 checks fail with the swap disabled.
    - Resolves the ledger row "DESC key columns get no NULL stop / range
      producers ignore DESC" \(2026\-09\-24\).
    Kind: bug
    Parent: none
    Movement: none
  - [x] **store\-null\-keys S3 — relax the planner guard** for tuple\-format
    indexes in flagged clusters \(move the format predicate where the
    optimizer can import it\). Design §Slices 3. After S1 and S2.
    Kind: impl
    Parent: none
    - **LANDED 2026\-09\-24 \(ralph2 loop \#46\), `42a6c9abc`.** The format
      test stays in the executor and is registered with catalog at init;
      `catalog.IndexHasNullKeyedEntries` lets both guard helpers accept
      tuple\-format indexes of capable clusters.
      - Recovered on a capable cluster: limit\'s GroupAggregate over the
        tenk1\_thous\_tenthous index\-only scan; create\_index\'s
        `thousand = 42 AND \(tenthous …\)` index scans. Guard tests run in
        both capability states.
      - Gates: units, spotcheck, sf025 \(changed=0 — the bench clusters
        predate the capability\), acceptance arm, fire\-set.
      - Parent task complete; remaining gap ledgered \(no upgrade path for
        existing clusters\).
    Movement: none
- [x] **setop output type is the FIRST member's, not `select_common_type`'s
  (found 2026-09-21 by an M0145-0004 discovery probe)** — **FIXED
  2026-09-22** for PostgreSQL's numeric type category, which covers both
  reported witnesses. Design doc:
  `docs/design/0100-0149/setop-common-type-resolution.md`.
  Kind: bug
  Parent: none
  Movement: none on the parity instruments (TPC-DS SF0.25
  `PLAN-SHAPE same=98 changed=1`, and that one is cost-only — see below);
  the defect was a declared-TYPE divergence, which none of S3's three
  instruments measures.
  - **Root cause, found by reading rather than guessing**: `SetOp.Output()`
    (`internal/optimizer/plan.go`) returns `n.Left.Output()`, and
    `wrapSetOpBranchWithCasts` coerced only the RIGHT branch to the LEFT's
    schema. Together those two ARE the "first member wins" rule.
  - **Fix**: `setOpUnifyBranches` resolves each column's common type across
    both branches and coerces BOTH to it, so `SetOp.Output()` becomes
    correct with no change to `Output()` itself — coercing the left branch
    is precisely what the old code omitted.
  - Verified against a live PG 18.3 on every pair, not derived from the
    algorithm: `1 UNION ALL 2.5` -> numeric (was bigint);
    `int2 UNION ALL int8` -> int8 (was smallint); `int4`/`float4` ->
    float4; `float4`/`float8` -> float8; and the REVERSED pair
    `float8 UNION ALL int2` -> float8. The reversed case is the one that
    distinguishes a real resolution from a positional rule, and it is
    pinned as the paired control: it passes even WITHOUT the fix, which is
    exactly why it must sit next to a case that does not.
  - Values are unaffected and were checked: `sum` over the mixed union is
    still 3.5, and the rows still render `1` and `2.5`.
  - **Pairwise folding is sound here, and that is a deliberate argument not
    an accident**: `applySetOp` folds members left-deep, so goopg resolves
    pairwise where upstream considers every member at once. Within
    TYPCATEGORY_NUMERIC the implicit-coercion lattice is a TOTAL ORDER
    (`int2 < int4 < int8 < numeric < float4 < float8`, with float8 the
    category's preferred type and already its maximum), so the pairwise
    fold converges on the same answer. That equivalence is what bounds the
    fix to this category.
  - **The one plan-shape delta is cost-only.** TPC-DS Q5 moved
    (`GroupAggregate` 14363.79 -> 14595.00). Its plan is STRUCTURALLY
    IDENTICAL — 66 lines, zero diff once cost text is stripped, row
    estimates and widths unchanged — and the sweep reports
    `MISMATCH=0 CKMISMATCH=0`. The delta is the cost of the coercion
    Project the fix inserts on a branch whose member types differ, which is
    the same coercion PostgreSQL itself inserts; it is a move toward PG,
    not away.
    - Sub-note on how that was checked: the first cost-stripped diff came
      back empty because the awk range used `=== Q5` while the capture
      writes `===== Q5 =====`, so it matched nothing. A vacuous "no diff"
      is indistinguishable from a real one until the range is confirmed to
      select lines — re-ran with a line count before trusting it.
  - Gates: units; FULL upstream regress suite (only the known
    `partition_aggregate` fails, unchanged); tpch-spotcheck Q12=2/Q13=33;
    tpcds-sf025 PASS; acceptance arm 24/24 value-MATCH; pgbench smoke.
    Test verified non-vacuous: neutralising `setOpUnifyBranches` fails
    exactly the four order-sensitive subtests.
  - Deferred and ledgered, NOT silently skipped: pairs outside the numeric
    category are declined and keep the previous behaviour. That includes
    the CROSS-CATEGORY case, where upstream raises 42804 "types %s and %s
    cannot be matched" and goopg still accepts the query. Also unmodelled:
    `unknown`-only members resolving to `text`, and the domain-preserving
    property of upstream's all-same fast path beyond name equality.

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

- [x] **bpchar HASH semantics parity — verify/fix blank-insensitive
  hashing wherever bpchar keys are grouped, joined, or de-duplicated**
  (found 2026-09-27 while analysing the post-reload SF0.25 sweep; the
  companion `bpchareq` finding is recorded in OWNER DECISIONS
  2026-09-27). PG's `bpcharhash` (hash over the blank-stripped image)
  ignores trailing blanks so that `char(20) 'x   '` and `char(5) 'x'`
  hash identically — this is what makes hash joins, `GROUP BY`,
  `DISTINCT`, and SetOp de-duplication on bpchar agree with
  `bpchareq`. If goopg hashes the padded image bytewise, those paths
  miss matches that `=` now accepts — the exact failure mode that
  appeared as `goopg=0 oracle=100` after the reload. The stale
  unpadded load masked BOTH sides of this; padding-correct data
  exposes whichever side remains wrong.
  Kind: impl
  Parent: none
  - Verification scope (each needs a witness query, not just a code
    read): (a) hash join between bpchar columns of different declared
    widths — e.g. a `char(20)` column against a narrower bpchar side;
    (b) `GROUP BY`/`DISTINCT` over bpchar where equal-logical values
    carry different padding (mixed-width inputs via UNION); (c) SetOp
    de-duplication (`UNION`/`INTERSECT`/`EXCEPT` over bpchar) — the
    `union` regress case already bit this class once for CAST
    padding; (d) `IN`/`NOT IN` on bpchar when they take the hashed
    subplan path; (e) `bpchar = text/varchar` comparisons, which
    route through `text(bpchar)` rtrim casts rather than `bpchareq` —
    check the coercion choice matches `pg_cast.dat`.
  - First step: with the padded-data clusters up, run each witness on
    goopg vs the `:65438` reference; then fix the hashing/coercion
    paths that diverge and re-run `scripts/tpcds-sf025-regression.sh
    sweep` — residual MISMATCH/CKMISMATCH rows after the `bpchareq`
    fix are the signal this task exists for.
  - Caveat for whoever fixes `bpchareq`: pad-aware equality alone is
    NOT sufficient — a bytewise hash turns every padded hash
    partition into its own bucket, so join/group paths silently lose
    rows even though `=` is correct. Fix both sides in the same
    change, or file the remainder explicitly.
  - **DONE 2026-09-27 (M0146-0015f).** Both halves landed together per
    the caveat: `comparisonOperandsAsBpchar` gives scalar `=`, `IN`,
    `CASE`, `IS [NOT] DISTINCT FROM`, and the compiled exprnode twin
    the `bcTruelen` comparison image (declared types decide — an
    explicit text operand keeps its padding, an untyped literal
    opposite bpchar coerces to bpchar), and every hash/dedup key
    boundary normalises identically: `evalSortKeyValue` (sort, Gather
    Merge, incremental sort, SetOp merge), `buildKeyTrim`/
    `probeKeyTrim` on hash-join build+probe, merge-join stream keys,
    `aggregateOp.gkTrims` (hash + sorted grouping, `COUNT(DISTINCT)`),
    `distinctOp`/`distinctOnOp` schema trims, `rowKeyTrimmed` for
    SetOp + recursive-CTE dedup, window partition/order/peer keys,
    `subPlanRowHash` multi-column `IN`, `corrSubqHashMap` inner+outer
    trims (the `ExecParamRef` arm is what Q41 needed), and ANALYZE
    frequency bucketing.
  - Witnesses (all match PG 18.3 :65438, probe matrix in
    `analysis/m0146/m0146-0015f/`): cross-width char(20)/char(5)
    equality `t`, bare-literal coercion, `text`-side padding retained,
    `IN`, correlated scalars, semi join, hash join, multi-col `IN`,
    mixed-width `GROUP BY`, `UNION` dedup, window partition,
    `IS [NOT] DISTINCT FROM` both directions.
  - Gates: units PASS; tpch-spotcheck PASS (Q12=2, Q13=33); TPC-H
    acceptance arm 24 MATCH vs baseline-digests; SF0.25 sweep
    **PASS=96 MISMATCH=0 CKMISMATCH=0** (sweep-20260927-130805.txt,
    was 70/26 on the padded reload) with plan-shape `same=99
    changed=0` — executor semantics only, no plan movement.
  - Deferred (ledgered): `op ANY(array-datum)` — elements of a
    `bpchar[]` column or multi-element `ARRAY[...]` carry no declared
    element type, so that element side stays byte-exact while a
    declared-bpchar operand trims.
  - Design `docs/design/0100-0149/m0146-0015f-bpchar-hash-semantics-parity.md`;
    evidence `analysis/m0146/m0146-0015f/`.

- [x] **testport/TestE2E_FailoverPGtoGoopg** — testport TestE2E\_FailoverPGtoGoopg FAILed \(subtest: async\)
  (AI-20261014-001833-001; repro: `go test -v -run '^TestE2E_FailoverPGtoGoopg$' ./internal/testport/`,
  evidence `ci/logs/20261014-001833/testport/go-test.log`; first seen in the 2026-10-14 nightly).
  Kind: impl
  Parent: none
  - Failure line: `e2e_failover_pg_to_goopg_test.go:95: async failover lost too many rows: count(*)=2
    lower_bound=18 killCommitted=38`. That is 2 rows after the async failover where the test admits a loss down
    to 18 of the 38 committed before the kill.
  - Not yet triaged: first establish whether it reproduces on HEAD or is a one-night timing flake (the nightly
    host was saturated by the batch); if goopg really loses rows a PG primary had shipped, it is an S2
    data-loss finding and needs an escalation block for owner placement.
  - Triaged 2026\-10\-14: a timing flake in the test\'s bound, not data loss\.
    - HEAD passes 3/3 on an idle host\.
    - The nightly async subtest ran 12\.7 s against \~9\.5 s normally: the batch saturated the host\.
    - `goopg promote` drains every received record before promoting, and a drain timeout fails the promotion
      \(cmd/goopg/standby\.go runPromote\)\. The promotion succeeded, so the missing 36 commits were never shipped
      before the kill\. That is legal async loss in PG too\.
    - The bound `killCommitted \- 20` assumed the stream lags by at most 20 commits, which no async guarantee says\.
  - Fix \(test only\): both failover tests \(PG→goopg and the goopg→PG sibling\) now take the lower bound from the rows the
    standby had already replayed before the kill\. Promotion must never roll those back, in PG or goopg\. They log
    kept/committed/replayed\. 2/2 runs pass, e\.g\. "kept 65 of 66 \(65 replayed before the kill\)"\.
Movement: none — correctness test hardening (nightly flake), no plan instrument

