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
  is not a live (`[ ]` or `[!]`) task and not the leaf-count/lateral
  classes owned by M0145-0003/0010. For TPC-H, each non-MATCH query is
  actionable unless a live task names it. **Owner directive
  2026-10-16: the formerly waived residual classes — COSTTIE,
  GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES, STALE-SF1 — are UN-WAIVED.**
  Waived status was a routing convenience, not proof of plan equality;
  the goal is a plan identical to PG 18.3 for every query on all three
  corpora. Each such record re-ranks by its first-divergence category
  like any other record. A waived record also re-opens whenever its
  premise changes (corpus reload, page-count or cost-model change) —
  e.g. owner verification on 2026-10-16 showed the SF1 corpus already
  fresh-format (relpages identical to PG on all probed tables; char(n)
  padding present), so the STALE-SF1 premise dissolved without a
  reload.
  (3) Rank the differ's nine categories by actionable-record count
  summed across all three corpora; file exactly ONE task —
  `Parent: M0146-0014`, `Kind: impl` — citing corpus + query id +
  category + current verdict + expected instrument. Scope: first step
  records where goopg's plan selection diverges from PG 18.3 (the
  first-divergence point); then implement in order and drive the query
  to MATCH. Prefer the SF0.25 record when a query diverges at both
  scales (smaller corpus, faster iteration; the SF1 stale-load premise
  was cleared by owner verification 2026-10-16 — see (2)). The filed task
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
  (5) Plan-parity direction (owner directive 2026-10-16) — binds all
      parity work, S9-filed or owner-seeded:
      (a) Runtime is never a rejection reason. A change that moves a
          record toward MATCH lands even when slower — a slower plan
          that matches is not a regression (AGENT.md plan-parity rules;
          R3: no reverts for runtime). When a *correct* but slower plan
          makes a gate's stamp non-PASS (sf025 sweep TIMEOUT, fireset
          new-timeout, acceptance-arm timeout), the task does not
          revert: it escalates `[!]` naming the gate — the owner amends
          the gate (XFAIL-class pin, raised timeout, or a
          gate-exceptions row), per the M0146-0178 precedent.
          Correctness regressions still block; `regress` Movement never
          counts as parity movement.
      (b) Same-logic only. Parity comes from porting PG 18.3's same or
          substantially-same processing logic — planner, executor,
          storage/access, bulk-load, ANALYZE sampling, GUC defaults
          alike — and each fix cites the PG mechanism it ports.
          Forbidden: query-text special-casing, forced/pinned plan
          shapes, and cost-constant or threshold fudging that flips a
          choice without a PG citation.
      (c) Physical parity is parity work. When a divergence traces to
          heap/index page count (relpages), VM all-visible state, or
          tuple layout, the task fixes the responsible component —
          copyfrom.c/hio.c bulk extension and VM marking, heap packing
          and fillfactor, nbtree bulk build and page split, ANALYZE
          sampling — not the planner. Page-count parity to
          plan-identical precision is the acceptance bar.
      (d) GEQO-RNG is actionable. goopg already carries the structural
          GEQO port (`internal/optimizer/geqo.go`); the residual gap is
          the RNG — port PG's pg_prng (splitmix64 fseed expansion +
          xoroshiro128** + bitmask-reject `pg_prng_uint64_range`; a
          working copy lives in `internal/executor/
          analyze_block_sampler.go`). PG's `geqo_seed = 0` is already
          deterministic (`pg_prng_fseed`), so captures need no seed
          pinning.
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

0. **P0 — incident recovery.** P0-E4..E7 DISCHARGED `[x]`
   (2026-09-18; archive `completed_fix_plan_013.md`).
   **P0-E8 OPEN — owner placement 2026-10-19 (S2): select FIRST.**
   Concurrent sessions intermittently fail to resolve a CTE name
   (`relation "x" does not exist` on a valid `WITH x AS (...) SELECT ...
   FROM x`), wrong-error class; entry under M-NIGHTLY "Manually
   discovered".
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
   - **Owner placement 2026-10-19 (b):** M0146-0229 (land the parked
     slice 1), then M0146-0229a (Q74 executor recon) and its filed
     sub-tasks — ahead of the next S9 pass.
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
    (**M0122-0012** → M0119 → **M0147** → the rest of M0122 → M0131 → M0134 →
    M0135/M0136 → M0095/M0110).
    (Reachable only after the S9 pass above files nothing.)
    **M0122-0012 return-first rule (owner decision 2026-10-27; replaces the
    2026-10-21 yield rule).** "M0122-0012" means its inline slices and any
    task with `Parent: M0122-0012`. Until it is EXHAUSTED (below), after
    M-NIGHTLY open items item 10 starts with M0122-0012: whenever any other
    item-10 task (not items 0–9 or owner-placed P0 tasks) closes, parks or
    blocks, or the one-task yield below ends, the next selection returns to
    M0122-0012.
    - **Bar:** an attempt must move its own pre-named target by at least 3%.
      `Target:` — written in the entry BEFORE the attempt is measured — names
      exactly ONE query and ONE metric, and the query must be in the newest
      G2 top 10 by absolute excess. Metric: instructions:u; bytes only for
      an allocation slice, and then instructions:u and cycles must be no
      worse than +1%; wall time only on an idle host.
    - **One-task yield:** when two consecutive M0122-0012 attempts since the
      last return (landed, or measured and dropped) each missed the bar,
      select exactly ONE other item-10 task in the order above (skipping
      M0122-0012) and work it for one task — or one landed slice/commit of
      a multi-loop task — then return. If no other item-10 task is
      selectable, skip the yield. Record it in the entry
      (`Yield <date>: <the two attempts + %>; worked <id>`); the miss counter
      resets on return. A missing candidate is NOT a yield reason — generate.
    - **On return, choose** the top eligible entry of the entry's
      **Candidate pool**. Ineligible: everything on the entry's **Below-bar
      list** — in particular the attempts that triggered the yield. The key
      is the mechanism (code site + PG function) on the same target query:
      the same mechanism on a different G2 query stays eligible.
    - **No eligible candidate → generate** (owner-delegated: S8's "do not
      invent work" does not apply; one pass per loop). Tools:
      `analysis/m0122/m0122-0012/tools/` (committed copies of the loop's
      census scripts).
      - G1 Census: warm per-query timings with the same plan (parity MATCH)
        on private clones — TPC-H SF1 and TPC-DS SF0.25 against **release**
        PG (`bench/pg-release`, `analysis/timing-after-planprity-match/`
        `pg-release-scripts/`; its numbers change only on a corpus or query
        change), never the cassert reference clusters. Split planning from
        execution and record allocation (`ds-planinstr.sh`, `ds-alloc.sh`).
        Commit the table under `analysis/m0122/m0122-0012/census-<date>/`; a
        committed census at most 3 days old may be reused, and each
        attempt's own A/B is always fresh.
      - G2 Rank queries by absolute excess time (goopg − PG), then ratio.
      - G3 Profile the top three (perf record -g / pprof, plus the row-count
        census for algorithmic gaps). Attribute the excess to symbols and
        map each to a PG executor mechanism (cite the PG function) or to a
        goopg-only overhead (allocation, clone/copy, deform, interpreted
        expression, per-row re-resolution, planning).
      - G4 A candidate qualifies when it is perf-only (values and plans
        identical — plan-visible ideas go to the parity tasks), one
        mechanism sized for one loop, not on the Below-bar list, and its
        attributed share predicts at least 3% on a G2 top-10 query.
        Design-sized levers (the ledgered structural ones) qualify through
        their first slice-sized step, with a design doc.
      - G5 Add every qualifying candidate to the pool (target query,
        baseline, predicted %, PG citation), ranked by predicted gain; take
        the top one and write its `Target:` before coding. Candidates are
        pool bullets and inline slices, never `- [ ]` child tasks.
    - **Rotation, not exhaustion:** three consecutive misses in one
      mechanism family put that family on the Below-bar list and send G2 to
      the next query.
    - **EXHAUSTED** only when a fresh G1–G3 pass attributes the excess of
      every G2 top-10 query to Below-bar mechanisms, or no query exceeds
      release PG by at least 10% and 50 ms. Write
      `EXHAUSTED at <sha> <date>` with the census path in the entry; item 10
      then proceeds in the order above without M0122-0012. It re-opens when
      a later G1–G3 pass — at most once per 7 days, run when item 10 is
      reached — finds a qualifying candidate, or by owner decision.
    - Baton: `.ralph/working_set.md` carries an `M0122-0012:` line
      (PROMPT.md). Skip-rationale tokens: `M0122-0012(yield-b: <two attempts
      + %>)` during the one-task yield, `M0122-0012(exhausted@<sha>)`
      afterwards.
    M0147 descendants inherit item-10 rank — the S7 correctness-only
    carve-out does not demote them. **M0147-0000 is a channel, not stock**
    (like M0146-0014): never select it; take its first open child in file
    order (`M0147-0000(channel)`).

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
why this matters for S4's budget). For M0147 lever-retirement tasks only,
`lever-count` (`scripts/lever-count.sh`) is an instrument:
`Movement: yes — lever-count 34→30 (census 22/22·99/99·99/99 held)`
(owner decision 2026-10-21).

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
- :65433 TPC-H index compaction: REINDEX was unavailable until
  M0146-0196 closed 2026-10-16 (owner-placed); the cluster was reloaded
  by INSERT replay under a 7b356ec04 binary — a NEGATIVE result:
  identical index sizes (117050 / 28617 / 41512 pages). The remaining
  bloat driver is open under M0146-0195; compacting a ref index goes
  through `REINDEX INDEX <name>` — surgical, NOT `REINDEX TABLE`, which
  would re-bulk-build the deliberately insert-built FK indexes (goopg's
  bulk build is denser than PG's; Q10's match rests on PG-sized FK
  indexes).
- M0146-0196 closed by owner placement (was below item 10, S7).
- M0146-0178 de-escalated `[!]`→`[ ]`; resume = apply the parked
  slice2.patch, re-run the arm, commit.

OWNER DECISIONS 2026-10-19 — TPC-DS corpus reload + PG statistics import:
- Both shipped TPC-DS corpora (`:65436` data, `:65437` data-sf025) were
  REBUILT on the ee18b4649 engine (insert-built indexes with the M0146-0216
  split port) and carry the `:65438` reference's own statistics: COPY →
  per-table VACUUM → `pg_dump --statistics-only` import into
  never-analyzed tables (PG's import merges, so no ANALYZE first). Clears
  the owner gates of M0146-0215 (owner-import stats), M0146-0216
  (owner-index-refresh) and therefore M0146-0218 and M0146-0214's
  blocked-on. Old corpora kept as `*.pre-reload-20261019`.
- Measured (`analysis/m0146/owner-ops-20261019/`): heap relpages /
  reltuples / relallvisible identical to the reference on every table
  at both scales; index pages ±2; pg_stats identical except 4 wide text
  columns (in-row thinning, ledger) and one correlation-0 rendering
  (ledger). PLAN-PARITY on the HEAD binary: SF0.25 92 → **97/99**
  (Q64, Q77 left), SF1 82 → **94/99** (Q13, Q23, Q64, Q72, Q77 left) —
  identical to the loop's routing-20261019d PG-statistics clones. sf025
  sweep PASS=99 (values); all 64 SF1 row anchors hold; EA ratchet PASS
  (its stale clone `tmp/c20a/data-sf025` was moved aside, re-cloned).
- This is a recorded CORPUS RELOAD for S9(1): routing artifacts before
  2026-10-19 14:00 are stale; the next S9 pass captures fresh. The sf025
  plan-shape channel will show large NON-BLOCKING movement on its first
  run — expected.
- Never ANALYZE the shipped TPC-DS corpora (it replaces the imported
  statistics). Rebuild only via owner-only `scripts/tpcds-corpus-rebuild.sh`;
  `tpcds-sf025-regression.sh load-goopg` now imports the same way.
- P0-E8 (concurrent CTE resolution race) placed at item 0 — found while
  verifying the reload; pre-existing (fails at e91816974 too).

OWNER DECISIONS 2026-10-19 (b) — M0146-0229 slice 1 un-parked:
- The f1695a907 escalation is resolved by landing slice 1 (Merge Append /
  Incremental Sort, a port of PG's generate_orderedappend_paths /
  create_ordered_paths). Rationale: without it goopg would have to reach
  PG's plans by different logic. Q74's plan is PG-identical and its
  runtime is unchanged by the slice (543 s vs 522 s); host timings are
  unreliable at present (owner-side load outside WSL2).
- Gate amendment: `scripts/tpcds-fireset-status.py` reads an
  owner-maintained allowlist, `scripts/tpcds-fireset-timeout-allow.tsv`
  (harness-protected). Row: tpcds-sf1 Q74, expires 2026-11-30. An
  allowed introduced timeout is reported, not failed. Provisional.
- Placement: M0146-0229 (resume slice 1), then **M0146-0229a** — the
  executor-layer recon of Q74's runtime, and whatever impl sub-tasks it
  files — rank inside item 3 ahead of the next S9 pass.

OWNER DECISIONS 2026-10-20 — TPC-DS corpora REBUILT for M0146-0216 slices 4+5:
- c74f0c278 (internal-page splits) and 0619c0ac9 (COPY heap placement
  through PG's FSM) closed "shipped corpora unchanged until an owner
  rebuild". Both shipped TPC-DS corpora were rebuilt at 4eea30455 with
  `scripts/tpcds-corpus-rebuild.sh` (COPY → per-table VACUUM → :65438
  statistics imported into never-analyzed tables; reference stats
  unchanged since 2026-10-19). Previous corpora kept as
  `*.pre-rebuild-20261020`. Evidence: `analysis/m0146/owner-ops-20261020/`.
- Physical parity is now EXACT: all 24 pkeys equal PG's page counts at both
  scales (was ±2; customer_demographics_pkey 5269 → 5268), heap per-block
  row counts identical to PG for store_sales / store_returns /
  catalog_sales, heap relpages/reltuples/relallvisible identical,
  `bt_index_parent_check` clean on all 24 pkeys, pg_stats unchanged (the
  5 ledgered diffs).
- Same-binary A/B census (HEAD 4eea30455; old corpus vs rebuilt): SF0.25
  98/99 → 98/99 (Q77), SF1 96/99 → 96/99 (Q13, Q72, Q77); plan shapes
  identical, costs moved on 15 queries per scale (the +4.0-per-probe
  customer_demographics_pkey offset is gone). SF1 Q64 keeps its match.
- Values: sf025 sweep PASS=99 on the HEAD binary; all 64 SF1 row anchors.
  (A sweep built from the dirty tree at 11:54 — the staged M0146-0237 WIP —
  ERRORed Q34/Q64/Q75 on the rebuilt corpus; the HEAD binary does not.)
- EA ratchet: on the rebuilt corpus the seeded goopg ANALYZE samples
  different rows, and Q23's Hash (catalog_sales+cte:frequent_ss_items+
  customer+date_dim, est 42 / actual 0, no PG counterpart) became a
  finding; the same binary on the old corpus passes. Owner RE-PIN of
  `ea-baseline.txt` (Q78 fixed → Q23). Not a code regression.
- Recorded CORPUS REBUILD for S9(1): routing artifacts before 2026-10-20
  12:00 are stale. tpcds-sf025 / tpcds-fireset gate stamps taken on the old
  corpus were removed (re-run the gates).
- M0146-0216: the owner gate is cleared; the loop re-censuses and closes it
  once its open child M0146-0236 is resolved.

OWNER DECISIONS 2026-10-21 — M0147 lever retirement filed:
- Plan parity is complete on all three corpora (routing-20261021p: 22/22,
  99/99, 99/99). The default-OFF and legacy `GOOPG_*` lever arms are now
  retired by **M0147** (`docs/milestones/0147-lever-retirement.md`; section
  `## M0147` at the end of this file), per AGENT.md C5 (promote or delete —
  HOLD is not an outcome). Analysis and review:
  `/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/0----------plan-party-progress-1021.md`.
- Rank: item 10 — superseded 2026-10-27 by the M0122-0012 return-first
  rule (item 10); other item-10 work runs during its one-task yields and
  after its exhaustion.
- Order inside M0147: phase 1 goopg-private retirements (0010–0017), then
  phase 0 PG-port promotions (0020–0023; R3 — never discard a PG arm
  without an owner `[!]`), then phase 2 (0030 recon files the default-ON
  levers' OFF-arm slices).
- Harness: AGENT.md's plan-parity range is now M0137–M0147 (also PROMPT.md
  and the lineage guard's Rule B); `lever-count` added as an M0147
  instrument (AGENT.md S3, lineage guard Rule D; `scripts/lever-count.sh`
  is harness-protected). AGENT.md R6/B1 amended: deleting
  `GOOPG_SORT_PACKED` is in scope; C4 amended: GATHER_PATHS `off`/`top` may
  be retired.
- M0146-0010a (Held) and M0147-0023 are the same flip; M0147-0023 is `[!]`
  blocked-on M0146-0010a. Its release condition (Q4/Q11 `year_total` CTE
  nested-loop estimates) may already hold now that SF0.25/SF1 are 99/99 —
  the loop may re-measure it and, if PG's plans match at both scales, flip
  M0146-0010a `[!]`→`[ ]` (owner authorisation recorded here).

OWNER DECISIONS 2026-10-27 — M0122-0012 return-first:
- M0122-0012 now heads item 10 (after M-NIGHTLY open items) and is
  returned to after every other item-10 task until EXHAUSTED; the
  2026-10-21 yield rule is replaced. Its rule (a) — "no runnable slice" —
  yielded on 2026-10-24 after slice 92 although ledgered candidates
  remained. The procedure (bar, one-task yield, Below-bar exclusion,
  generation G1–G5, rotation, exhaustion) is in item 10.
- The entry gained an owner-seeded Candidate pool and Below-bar list
  (2026-10-27); the loop maintains both. The census scripts it used from
  `tmp/` are committed under `analysis/m0122/m0122-0012/tools/`.
- Other item-10 work (M0119, M0147, the rest of M0122) runs during
  M0122-0012's one-task yields — the next selectable task in item-10
  order — and after its exhaustion.

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

- [x] units/internal/access/transam/xlog — TestCheckpointerSpreadHonoursDeadlines: spread checkpoint 559 ms against the 500 ms bound \(AI\-20261017\-005127\-001; repro: `go test \-timeout 10m \./internal/access/transam/xlog/`\)\.
  - Stale, passes at HEAD 1390b3901: the package passed, and the test passed 3 of 3 in isolation\. It is a timing bound
    under load: the 2026\-10\-17 00:51 nightly overlapped this loop's own gate runs, with host load average 7\.
- [x] testport/TestPort\_CreateSchemaSurvivesRestart — server start timeout after 20 s \(AI\-20261017\-005127\-002; repro: `go test \-v \-run '^TestPort\_CreateSchemaSurvivesRestart$' \./internal/testport/`\)\.
  - Stale, passes at HEAD 1390b3901 \(3\.8 s\)\. The same nightly\-plus\-loop load overlap; the next sibling restart tests
    in the same run passed\.
- [x] units/internal/executor — TestSharedSpillingBuildLoadsOncePerBatch: loadCount=10 with 7 inner files and 4 participants, want exactly 7 \(AI\-20261019\-001758\-001; repro: `go test \-timeout 10m \./internal/executor/`\)\.
  - Nightly ran from a detached worktree at HEAD ab7ff371b, which passed the 2026\-10\-18 nightly; not caused by the staged M0146\-0224 slice\-4 WIP\.
  - Fixed in a9b03992b \(test\-only\)\. The test's barrier held participants BEFORE acquireLoad, so under host load the claimant
    could load, probe and release a batch before a descheduled peer arrived; that peer reloaded it \(the documented straggler
    path\)\. The loader now parks in testHookSharedBatchLoading until the other n\-1 peers wait on done\. Mutation check: an
    injected post\-barrier sleep fails 10/10 without the park \(loadCount=12\) and passes 10/10 with it\.
- [x] race/internal/access/nbtree — TestIncompleteSplitsFinishedConcurrently: finish\_split\_test\.go:390 Search\(865\) = false \(AI\-20261019\-001758\-002; repro: `go test \-race \-timeout 45m \./internal/access/nbtree/`\)\.
  - 2026\-10\-19 01:32: the single test passed 5/5 under \-race on an idle host; not yet stressed or diagnosed\.
  - Fixed in 8fbfdf20f \(test\-only\)\. Not a lost key: all 14 missing keys came from Inserts that returned errSplitAbandoned
    while finishing another writer's half split on contact \(insertIntoBlock \-> finishSplit\), before their own key was placed\.
    The test now retries failed inserts\. Latent since d7c05acde \(14/200 under 8x load\); fixed: 0/400\.
- [x] testport/TestPort\_IsolationEvalPlanQual — output 1470 lines against 1468 expected \(AI\-20261019\-001758\-003; repro: `go test \-v \-run '^TestPort\_IsolationEvalPlanQual$' \./internal/testport/`\)\.
  - Earlier fix: the 2026\-09\-22 alias fix in resolveRowMarkCtidResnos \(deferral ledger row 2408\) made it byte\-identical; if the repro fails at HEAD, that fix did not hold\.
  - Stale, load\-induced; no code change\. Passes 3/3 at HEAD 8fbfdf20f on an idle host \(24 s\), and the same sha passed
    the 2026\-10\-18 nightly\. The nightly diff starts at `step c1: COMMIT; <waiting ...>`: the harness labels any step still
    running at blockProbeCap \(1\.5 s\) as waiting, and `mergevalues` then missed the 5 s drainWindow, so the harness cancelled
    it and the balances stayed 700/1400\. A cancelled merge is not a wrong answer\. Testport ran beside the units and race lanes
    in a MemoryHigh=6G cgroup, so multi\-second stalls are plausible\.
  - Under synthetic CPU load \(10 stress binaries\) 1 of 6 runs failed, on a different permutation, with only the
    timing\-label line differing and every value correct\. Root cause is the harness's timing fallback, already
    ledgered: deferral\_ledger row 2963 \(pg\_isolation\_test\_session\_is\_blocked not implemented\)\. Hardening
    lead, not done: treat a probe that reports no lock wait as still running past blockProbeCap, as isolationtester does\.

- [x] units/internal/access/nbtree — panic `storage: unsupported line pointer state: slot=2 flags=0` in insertItemSorted, from vacuumSplitRound's concurrent Insert \(btree\_vacuum\_split\_race\_test\.go:105\) \(AI\-20261020\-013101\-001; repro: `go test \-timeout 10m \./internal/access/nbtree/`\)\.
  - Nightly sha cb9d2c011 from a detached worktree; that change set touched internal/optimizer only\. A slot read as
    LP\_UNUSED during a sorted insert under a concurrent vacuum is a possible index\-page race, not a timing bound — S2
    candidate \(possible corruption\); escalated in the baton\.
  - 2026\-10\-20 03:15: TestStructuralLockVacuumWithSplits passed 5/5 on an idle host at HEAD cb9d2c011 \(0\.38 s each\);
    intermittent, not yet stressed or diagnosed\.
  - **Done 2026\-10\-20\.** Design `docs/design/0100\-0149/m\-nightly\-nbtree\-unlinked\-page\-moveright\.md`; evidence
    `analysis/m\-nightly/ai\-20261020\-013101\-001/`\.
    - Reproduced at GOMAXPROCS=2 \(about 1 in 40\-100 runs\)\. Two defects, both excluded by PG's deletion protocol:
      a descent that latched a page unlinked under it read the BTDeletedPageData tombstone as line pointers \(the panic\),
      and unlinkEmptyLeaf released its emptiness check's latch before deleting, so an insert in between was destroyed\.
    - Fix 1: isUnlinked \(BTDeleted without BTHalfDead\); descend, tryInsertNoSplit, insertIntoBlock, Search,
      rangeScanPosLeaf and ScanCursor\.Next step right, as \_bt\_moveright does for P\_IGNORE\. Phase\-1 pages stay writable\.
    - Fix 2: unlinkEmptyLeaf re\-tests emptiness after latching the target \(\_bt\_pagedel\) and abandons a repopulated one\.
    - The stress test now gates recycling for the round: a bare pool recycled a deleted block at once, which let a split
      reuse a block a stale inserter still held \(PG gates on safexid; production wires the horizon\)\. Gated: 0/600 runs
      fail\.
    - Pins: TestStructuralLockVacuumWithSplits \(panics without fix 1\); TestUnlinkRechecksEmptinessUnderTheTargetLatch
      \(deterministic hook; loses the key without fix 2\)\.
    - Gates: nbtree \-race, units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck,
      acceptance arm \(values identical\), sf025 PASS=99 same=99\.
  Movement: none — correctness fix — no plan instrument \(TPC\-H and TPC\-DS plans and values unchanged\)
- [x] testport/TestPort\_IsolationAlterTable1 — FAIL \(60\.04 s\) \(AI\-20261020\-013101\-002; repro: `go test \-v \-run '^TestPort\_IsolationAlterTable1$' \./internal/testport/`\)\.
  - 2026\-10\-20: passed inside a full `TestPort\_Isolation` gate at HEAD on an idle host; likely load\-induced, not yet
    re\-run on its own\.
  - The nightly testport lane overlapped this loop's own regress/isolation gates \(load average 9\.5\); check stale first\.
  - Diagnosis 2026\-10\-20: the nightly diff starts at `step sc1: COMMIT; <waiting ...>`\. A COMMIT slowed by load ran past
    the runner's blockProbeCap \(1\.5 s\), which labels a step waiting without a lock wait\. The cap exists only for goopg
    waits that report no wait event; the fix is to make them report PG's, then retire the cap\.
  - **Slice 1 LANDED 2026\-10\-20:** WaitForSlotsToCommit and WaitForPinnedSnapshotsReleased report Lock:virtualxid, as
    PG's WaitForOlderSnapshots does in VirtualXactLock\. Cap fires over the isolation family: 61 → 31 \(evidence
    `analysis/m\-nightly/ai\-20261020\-013101\-002/`\)\. Pin: TestSnapshotWaitsReportVirtualXIDLock\.
    - Still unreported: vacuum\-skip\-locked 14, intra\-grant\-inplace 8, tuplelock\-upgrade\-no\-deadlock 3, reindex\-schema
      2, read\-only\-anomaly\-3 2, insert\-conflict\-specconflict 2\.
    - Next: find each spec's unreported wait \(start with vacuum\-skip\-locked\), report PG's wait event there, then
      remove blockProbeCap from awaitStepOrBlock\.
    - Gates: transam \-race, units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck\.
  - **Slice 2 LANDED 2026\-10\-20 — Done\.** Design §"slice 2" of `docs/design/0100\-0149/m\-nightly\-isolation\-lock\-wait\-probe\.md`\.
    - Goroutine dumps at cap\-fire time \(test server pprof\) located each unreported wait; each now reports PG's
      event: waitPgClassRowMarkReleased Lock:transactionid, acquireTupleLock Lock:tuple and acquireReindexLocks
      Lock:relation \(on a real wait only, TryAcquire first\), WaitForSpecToken Lock:spectoken, waitForSafeSnapshot
      IPC:SafeSnapshot\.
    - The probe counts Lock waits and IPC:SafeSnapshot as blocked \(sessionWaitIsBlocking, isolationtester's
      pg\_isolation\_test\_session\_is\_blocked test\)\.
    - The six cap fallbacks left after the reporting were all slow\-but\-unblocked COMMIT/ROLLBACK/CREATE steps, the
      false positive behind this failure\. blockProbeCap is now a 60 s hang guard, not a blocking rule \(isolationtester:
      max\_step\_wait\)\. Isolation family: PASS in 563 s against 606 s, no test slower by more than 5 s\.
    - Pins: TestSessionWaitIsBlockingMatchesIsolationTester, TestAwaitStepOrBlockAsksTheProbe \(shortened guard\)\.
    - Gates: race \(transam, activity, testport/framework\), units, internal/postmaster, TestPort\_RegressSuite,
      TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm, sf025 PASS=99 same=99\.
  Movement: none — correctness fix — no plan instrument \(isolation harness and pg\_stat\_activity wait\-event fidelity\)
- [x] testport/TestPort\_IsolationClusterConflict — FAIL \(22\.88 s\) \(AI\-20261020\-013101\-003; repro: `go test \-v \-run '^TestPort\_IsolationClusterConflict$' \./internal/testport/`\)\.
  - 2026\-10\-20: passed inside a full `TestPort\_Isolation` gate at HEAD on an idle host; likely load\-induced, not yet
    re\-run on its own\.
  - **Diagnostic LANDED 2026\-10\-21** \(root cause pending\)\. Design `docs/design/0100\-0149/m\-nightly\-cluster\-start\-timeout\-diagnostics\.md`\.
    - Both start timeouts \(\-003, \-004\) stalled inside initdb\.Open: cluster\.log ends at "loaded postgresql\.conf"\. Open takes
      ~0\.1 s warm; neither spec fails alone \(2\.3 s each\), and 24 concurrent cold starts all attached by 1\.76 s\.
    - Hypothesis: the testport unit \(memory\.high=6G\) throttled at memory\.high, with servers at GOMEMLIMIT 5GiB\.
    - cluster\.Start now inlines the cgroup's memory state \(current/high/max, high\-event count, PSI\) and, via SIGQUIT,
      goroutine 1's and every initdb goroutine's stack\. Pins: TestStartTimeoutCarriesStuckGoroutines,
      TestStartupGoroutinesSelectsMainAndInitdb\.
    - Next: read the two notes on the next nightly start timeout; high\-events and PSI stalls → resize the lanes,
      a quiet group plus a parked initdb\.Open frame → fix that wait\.
    - 2026\-10\-21 nightly \(e64b1bfe9, diagnostic armed\): no start timeout in testport; still waiting for one\.
    - 2026\-10\-23 nightly \(c6a780f7e\): recurrence captured as AI\-20261023\-005128\-001 — quiet cgroup, goroutine 1
      runnable in the catalog\-heap reload \(not parked in initdb\.Open's wait\); see that task\.
  - **Resolved 2026\-10\-23** \(harness/CI only, no engine change\)\. The recurrence \(AI\-20261023\-005128\-001\) showed
    a quiet cgroup and goroutine 1 runnable in the catalog\-heap reload: CPU contention under the nightly co\-load
    stretched a ~1 s start past the test's 20 s budget; the repro passes 3/3 in ~2 s on an idle host\.
    internal/testutil/cluster honours PGCTLTIMEOUT \(pg\_ctl's DEFAULT\_WAIT override; raises normal budgets only,
    pinned\) and stage\-testport exports PGCTLTIMEOUT=60\. Design m\-nightly\-cluster\-start\-timeout\-diagnostics §7\.
  Movement: none — CI harness \(test start budget\)\.
- [x] testport/TestPort\_IsolationInsertConflictDoUpdate3 — FAIL \(20\.17 s\) \(AI\-20261020\-013101\-004; repro: `go test \-v \-run '^TestPort\_IsolationInsertConflictDoUpdate3$' \./internal/testport/`\)\.
  - 2026\-10\-20: passed inside a full `TestPort\_Isolation` gate at HEAD on an idle host; likely load\-induced, not yet
    re\-run on its own\.
  - **Diagnostic LANDED 2026\-10\-21** \(root cause pending\): same start timeout inside initdb\.Open as \-003; see that item\.
  - **Resolved 2026\-10\-23** \(harness/CI only, no engine change\)\. The recurrence \(AI\-20261023\-005128\-001\) showed
    a quiet cgroup and goroutine 1 runnable in the catalog\-heap reload: CPU contention under the nightly co\-load
    stretched a ~1 s start past the test's 20 s budget; the repro passes 3/3 in ~2 s on an idle host\.
    internal/testutil/cluster honours PGCTLTIMEOUT \(pg\_ctl's DEFAULT\_WAIT override; raises normal budgets only,
    pinned\) and stage\-testport exports PGCTLTIMEOUT=60\. Design m\-nightly\-cluster\-start\-timeout\-diagnostics §7\.
  Movement: none — CI harness \(test start budget\)\.
- [x] testport/TestPort\_ExtendedStatisticsSurvivesRestart — start timeout after 20 s \(AI\-20261023\-005128\-001; repro: `go test \-v \-run '^TestPort\_ExtendedStatisticsSurvivesRestart$' \./internal/testport/`\)\.
  - Filed 2026\-10\-23\. The \-003/\-004 start\-timeout class, with the 2026\-10\-21 diagnostic armed:
    - cgroup quiet: memory\.current 0\.6 GB of high 6 GB, PSI some/full avg 0\.00 — not a memory throttle\.
    - goroutine 1 **runnable** \(not parked\) in initdb catalog\-heap reload \(reloadUserAggregatesFromHeap →
      scanCatalogHeapRows → DecodeRowIntoMctxPGTuple\) at the 20 s mark: startup was progressing, slowly\.
    - It fired at 01:01:56, while this loop's S9 census \(clone copy \+ 221 EXPLAINs\) ran beside the nightly testport
      lane — CPU starvation of a slow\-but\-moving start, not a wait bug\.
  - Next: re\-run the repro on an idle host; if it passes, the lever is either the 20 s start budget under nightly
    load or the cost of the per\-start catalog heap reload \(it decodes every catalog row on each start\)\.
  - **Resolved 2026\-10\-23** \(harness/CI only, no engine change\)\. The recurrence \(AI\-20261023\-005128\-001\) showed
    a quiet cgroup and goroutine 1 runnable in the catalog\-heap reload: CPU contention under the nightly co\-load
    stretched a ~1 s start past the test's 20 s budget; the repro passes 3/3 in ~2 s on an idle host\.
    internal/testutil/cluster honours PGCTLTIMEOUT \(pg\_ctl's DEFAULT\_WAIT override; raises normal budgets only,
    pinned\) and stage\-testport exports PGCTLTIMEOUT=60\. Design m\-nightly\-cluster\-start\-timeout\-diagnostics §7\.
  Movement: none — CI harness \(test start budget\)\.
- [x] testport/TestPort\_IsolationMultipleCic — FAIL \(23\.23 s\) \(AI\-20261020\-013101\-005; repro: `go test \-v \-run '^TestPort\_IsolationMultipleCic$' \./internal/testport/`\)\.
  - 2026\-10\-20: passed inside a full `TestPort\_Isolation` gate at HEAD on an idle host; likely load\-induced, not yet
    re\-run on its own\.
  - **Done 2026\-10\-21\.** Design §"Follow\-up \(2026\-10\-21, AI\-20261020\-013101\-005\)" of
    `docs/design/0100\-0149/m\-nightly\-isolation\-lock\-wait\-probe\.md`\.
    - Cause: the runner gave a pending step a 5 s drainWindow, then printed `<\.\.\. timed out waiting>` and cancelled it;
      the teardown's `bad connection` followed from the cancel\. isolationtester waits until the step completes and
      cancels only after max\_step\_wait \(360 s\)\.
    - Alone the spec takes 2\.0 s, and 2\.6\-3\.6 s at 2× CPU oversubscription; the nightly failure fell in the same
      01:45\-01:53 window as the \-003/\-004 start timeouts\.
    - drainWindow became pendingStepGuard, a 60 s hang guard like blockProbeCap\. A trace over the family found one
      5 s hit, in the shared\-cluster suite \(drop\-index\-concurrently\-1\), which now PASSes there; suite SKIPs 84 → 79\.
    - Pin: TestIsolationRunnerWaitsForSlowPendingStep, a goopg\-local spec with PG 18\.3 isolationtester expected
      output; it fails with a 5 s guard\.
    - Gates: testport/framework, TestPort\_Isolation\* 122/122 \(561\.7 s\), units, tpch\-spotcheck, sf025\.
  Movement: none — test\-harness fidelity — no plan instrument \(isolation runner timing only\)
- [x] testport/stage — stage testport FAILED: `go test` timed out after 2h0m0s in TestPort\_IsolationSuite \(AI\-20261022\-011400\-001; repro: `go test \-v \-timeout 120m \-run '^TestPort\_IsolationSuite$' \./internal/testport/`\)\.
  - 2026\-10\-22 nightly at f39f376e9: specs alter\-table\-1 and partial\-index were each blocked 1h49m in a server query
    \(alter\-table\-1 in RunSpec→execConn, isolation\_runner\.go:225; partial\-index in its setup, execConnSetupCapture
    :201\)\. Host load was ~0\.2 while they waited, so the server was idle: a lock wait or lost wakeup, not a spinning query\.
  - Nearby in the same run, deferred specs logged `DDL catalog sync: mirror catalogs to postgres db: mirror catalog 1259:
    dst extend produced blk 69, expected 68` on the shared cluster — a candidate for the concurrent\-DDL interaction\.
  - Not reproduced: the two specs pass alone at HEAD b6e96eb84 \(17 s\), and the full TestPort\_Isolation\* family passed
    in this loop's gates at f39f376e9 and b6e96eb84 \(577 s, 770 s\)\. Next: rerun the full suite under the nightly's cgroup
    \(high=6G max=8G\) and, on a hang, capture the server's goroutine dump \(SIGQUIT\) and pg\_locks before the timeout\.
  - **Done 2026\-10\-22 \(harness hang fixed; the underlying wait is not yet diagnosed\)\.** Design §"Follow\-up \(2026\-10\-22,
    AI\-20261022\-011400\-001\)" of `docs/design/0100\-0149/m\-nightly\-isolation\-lock\-wait\-probe\.md`\.
    - Re\-run at HEAD 043bddd4c under the nightly cgroup \(high=6G max=8G GOMEMLIMIT=5GiB\) with a stall watchdog:
      TestPort\_IsolationSuite PASS in 127 s; no stall to capture\.
    - Global setup/teardown now run under IsolationRunner\.SetupTeardownGuard \(5 min\): on expiry the runner snapshots
      pg\_stat\_activity \+ pg\_locks, then cancels, and fails the spec with the snapshot\. A recurrence becomes one failed
      spec with its blocker named, not a 2 h package timeout; the next nightly files it as a new AI item\.
    - Pin: TestIsolationRunnerGuardsBlockedTeardown \(row\-lock\-blocked teardown, 3 s guard → error after 3\.2 s naming the
      waiting teardown and its idle\-in\-transaction holder; ignoring the expiry fails it\)\.
    - Found: goopg's pg\_locks shows no transactionid rows \(ledger\); LOCK TABLE \.\.\. ACCESS EXCLUSIVE in an open
      transaction did not block another session's DROP TABLE in the pin's first draft\.
    - Gates: testport/framework, TestPort\_Isolation\* \+ TestIsolationRunner\* \(612 s\), units, tpch\-spotcheck, acceptance arm,
      sf025\.
  Movement: none — test\-harness robustness — no plan instrument \(isolation runner hang guard\)
- [x] testport/TestE2E\_FailoverPGtoGoopg — FAIL \(80\.49 s, async arm 50\.55 s\) \(AI\-20261021\-010544\-001; repro: `go test \-v \-run '^TestE2E\_FailoverPGtoGoopg$' \./internal/testport/`\)\.
  - 2026\-10\-21 nightly at e64b1bfe9: the async subtest's probe `SELECT count\(\*\) FROM public\.bench\_log WHERE client = \-999`
    stayed 0 for 30 s with the WAL receiver streaming; sync\_remote\_apply and sync\_on passed\. Not yet re\-run alone\.
  - First step: re\-run the async arm alone and under load; read the subtest's replay/apply position against the
    probe's insert LSN\.
  - **Diagnostic LANDED 2026\-10\-21** \(root cause pending\)\. Design
    `docs/design/0100\-0149/m\-nightly\-failover\-bootstrap\-replay\-diagnostics\.md`\.
    - Passes alone 5/5 and 6/6 at 2× CPU oversubscription \(~9\.5 s\)\.
    - The bootstrap row's commit ends at 0/3003100 on every passing run \(deterministic setup\); the failed standby had
      received and flushed 0/30201C8\. The row was shipped: the defect is goopg replay or visibility \(StreamReplayer,
      ReplayXactCommit, snapshot\), not the walsender\. The 0\-byte heap file proves nothing \(unflushed pool pages\)\.
    - pg\_last\_wal\_receive\_lsn / pg\_last\_wal\_replay\_lsn implemented \(were 42883\); the bootstrap wait now reports
      want/primary/sent/receive/replay LSNs \(walProgressDiagnostics\)\. Pin: TestPgLastWALLSN\.
    - Next: on the next occurrence read the notes — replay < want → replay stalled; replay ≥ want → visibility\.
  - **Root\-cause lead 2026\-10\-21:** TestE2E\_PGStandbyFullCycle failed 1 of 4 local runs with the same symptom, and its
    log shows `walreceiver WALData start mismatch start\_lsn=50331648 expected\_start\_lsn=50463176` — expected =
    0x030201C8, the exact LSN the failed nightly standby\'s wal\_receiver row reported\.
    - handleCopyData \(internal/replication/walreceiver\.go\) treats primary bytes that start below the local
      WrittenLSN as already present and trims them; when the local WAL has run AHEAD of what the primary\'s stream
      holds there \(here 0x201C8 bytes into segment 3\), every later frame is dropped and the row never arrives\.
    - Next: find what put the local writer at 0x30201C8 before the primary\'s segment\-3 data \(a timeline\-switch
      re\-send from the switch point, or a goopg\-local append at standby start\) — PG\'s walreceiver restarts at the
      switch point and overwrites; trimming is only right when the overlapping bytes are identical\.
  - **Done 2026\-10\-21\.** Design §6 of `docs/design/0100\-0149/m\-nightly\-failover\-bootstrap\-replay\-diagnostics\.md`\.
    - A temporary Writer\.Append stack probe on the standby showed every local append coming from the walreceiver\.
    - Cause 1: XLogData\'s second field is the sender\'s end of WAL \(SendRqstPtr\), not the frame\'s data end\. A PG
      primary catching up sends 128 kB frames with walEnd past them; handleCopyData appended those through
      Writer\.Append as NEW goopg records, so the local WAL diverged and later frames were trimmed as already present\.
    - Cause 2 \(masked by 1\): START\_REPLICATION was requested at WrittenLSN\+1, one byte past the end for a PG walsender\.
    - Fix: every non\-empty frame is raw bytes at StartLSN; the request is the start of the local end\'s segment
      \(RequestXLogStreaming\); goopg\'s walsender sends max\(chunk end, FlushedLSN\) as walEnd\.
    - Replication E2E family \(16 tests\) 4/4 green; the full cycle failed 1/4 before\. Pin:
      TestWalReceiverAppendsFramesWhoseWalEndIsAhead \(fails on the old receiver: 56\-byte frame grew local WAL to 128\)\.
    - Ledgered: the re\-sent overlap is trimmed, not overwritten as XLogWalRcvWrite does; no divergence detection\.
  Movement: none — replication correctness — no plan instrument \(walreceiver/walsender only\)

### Manually discovered (not yet in a nightly `ci/logs/action-items.md` run)

- [x] **P0\-E8 — concurrent sessions intermittently fail to resolve a CTE name: `relation "x" does not exist`**
  \(filed 2026\-10\-19 by the owner session; placed at banner item 0\)\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(repro 0 aborts over 22 rounds vs HEAD 6; plans byte\-identical, fire set empty\)
  Parent: none
  - Repro: `with x as (select 1 as a) select a from x;` as a pgbench script, 8 concurrent `pgbench \-c 1 \-t 50`
    clients: some abort at `command 0 query 0` with `ERROR:  relation "x" does not exist`\. TPC\-DS query2\.sql run 4\-way
    concurrently: 0\-6 errors per 20 runs; serial runs never fail\. Evidence:
    `analysis/m0146/owner\-ops\-20261019/cte\-first\-statement\-race\.md`\.
  - Pre\-existing: every tested rev of e91816974\.\.ee18b4649 fails, including e91816974\. Every observed failure is the
    first statement of a connection\. Suspects: session\-startup / first\-plan initialisation shared across sessions;
    package\-level planner state \(ledger 2026\-07\-29 M0125\-0003 row: no lock around planning\)\.
  - First step: reproduce under `go test \-race` with N concurrent sessions each issuing one CTE query as its first
    statement; find the shared state \(CTE registry / name\-resolution scope\) and make it per\-query, as PG's
    `ParseState\.p\_ctenamespace` is\.
  - Expected instrument: the repro at 0 errors over 20 rounds; `Movement: none — correctness fix — no plan instrument`\.
  - **DONE 2026\-10\-19\.** Design `docs/design/0100\-0149/p0\-e8\-per\-statement\-planner\-scope\.md`; evidence
    `analysis/p0e8/`\.
    - Cause: the planner's per\-statement dynamic scope lived in package globals swapped on nested\-scope entry and
      restored on exit — planCTEs \(the WITH list\), planParent, and the analyzer's outerScope\. Concurrent sessions
      swapped each other's scope; B's restore of planCTEs to nil let A's `FROM x` fall through to the catalog\. Only
      a cold plan cache plans concurrently, hence "first statement"\.
    - Fix: a per\-statement planState carried on a catalog wrapper \(stmtCatalog, Unwrap\-transparent\), created
      idempotently at every planning entry; re\-entrant plans share it; the analyzer reads its outer scope through
      an OuterScopeCarrier slot; cteDeclSeq and legHookFireCount atomic; sublinkRouteCounts mutex\-guarded \(an
      unsynchronised map write was a latent process\-fatal "concurrent map writes"\)\.
    - A global planning mutex was tried first and rejected: pgbench \-S 58\.7k → 16\.2k tps\. The per\-statement state is
      throughput\-neutral \(59\.0k/59\.6k vs HEAD 58\.8k/59\.4k\)\.
    - Pin: TestConcurrentPlansKeepTheirOwnCTEScope \(\-race clean; mutation\-checked: no sync, or one shared state, fails
      with the production 42P01\)\.
    - Repro: HEAD 6 aborts over 6 cold starts; the fix 0 over 22 rounds \(16 cold\-start\)\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(values identical\), sf025 PASS=99,
      tpcds\-fireset PASS with no fires \(plans byte\-identical on both corpora\)\.
    - Left: viewPlanDepth is a process\-wide atomic view\-nesting guard \(ledger 2026\-10\-19\)\.

- [ ] **P0\-E9 — a seq scan silently drops every row whose `numeric` column holds NaN or ±Infinity**
  \(filed 2026\-10\-21 by the loop, M0122\-0012 slice 22 investigation; S2 — wrong results\)\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(repro rows 3 → 6, PG 18\.3 returns 6\)
  Parent: none
  - > **ESCALATION 2026\-10\-21 \(S2, wrong results\) — OWNER PLACEMENT NEEDED\.** Filed, not selected: the loop
    > continues with the banner\. Repro `analysis/p0e9/numeric\-special\-repro\.sql`; PG 18\.3 output
    > `analysis/p0e9/pg18\.out`, goopg `022774301` output `analysis/p0e9/goopg\-022774301\.out`\.
  - Symptom: `INSERT` of `'NaN'`, `'Infinity'`, `'\-Infinity'` into a `numeric` column succeeds and `count\(\*\)` counts
    them \(6\), but `SELECT t, n FROM zn` returns 3 rows and an equi\-join on the column loses the NaN and Infinity
    matches \(3 rows vs PG's 5\)\.
  - Cause \(read, not yet proven by instrument\): the heap decode's numeric arm \(internal/executor/codec\.go, after
    `nodes\.NumericInt64FromStoredPayload` declines a NUMERIC\_SPECIAL header\) falls through to `parseNumeric\(text\)`,
    which cannot parse `NaN`/`Infinity`; the error makes `seqScanOp\.Next` `continue` past the tuple, the same
    silent skip a decode error always takes there\. `count\(\*\)` deforms nothing, so it still counts the row\.
  - First step: decode NUMERIC\_SPECIAL to the datum the executor uses for a numeric NaN/±Infinity literal \(check
    what `'NaN'::numeric` evaluates to in evalCast\), then audit the silent `continue` on a decode error — PG raises
    instead\. Then check numeric comparison, sort, hash keys \(datumToInt64Key must reject a special\) and
    aggregates against PG\.
  - Expected instrument: the repro identical to `analysis/p0e9/pg18\.out`; TPC\-H/TPC\-DS values unchanged\.

- [ ] **P0\-E10 — an integer operand IN a list of unknown literals compares as text**
  \(filed 2026\-10\-22 by the loop, M0122\-0012 slice 43 investigation; S2 — wrong results\)\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(repro counts 0/3/0 → PG's 2/1/731\)
  Parent: none
  - > **ESCALATION 2026\-10\-22 \(S2, wrong results\) — OWNER PLACEMENT NEEDED\.** Filed, not selected: the loop
    > continues with the banner\. Repro `analysis/p0e10/int\-in\-literal\-repro\.sql`; PG 18\.3 output
    > `analysis/p0e10/pg18\.out`, goopg `62b168220` output `analysis/p0e10/goopg\-62b168220\.out`\.
  - Symptom: `d\_year IN \('02000', ' 2001'\)` matches no row \(PG: 731 on tpcds025\); `y NOT IN \('02000', '\+2001'\)` keeps
    rows PG drops\. `y = '02000'` is correct\.
  - Cause \(read\): compareEq \(internal/executor/expr\.go\), which every IN / NOT IN / `<> ANY` loop uses, has an arm
    comparing KindInt to KindString by `fmt\.Sprintf\("%d"\)` text equality; `=` goes through compareDatum →
    promoteCrossKind, which parses the literal\. PG coerces each literal with int4in at parse time \(transformAExprIn\)\.
  - First step: route compareEq's int/string arms through compareDatum like its numeric arm \(an incompatible pair stays
    not\-equal\), then check evalInHashProbe and the row\-IN paths for the same arm; admit KindInt to
    inListConst\.coercedItems once both paths agree\.
  - Expected instrument: the repro identical to `analysis/p0e10/pg18\.out`; TPC\-H/TPC\-DS values unchanged\.

- [ ] **P0\-E11 — numeric division of a zero dividend prints the wrong display scale**
  \(filed 2026\-10\-23 by the loop, M0122\-0012 slice 74 output comparison; S2 — wrong results\)\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(repro text differs from PG on every column\)
  Parent: none
  - > **ESCALATION 2026\-10\-23 \(S2, wrong results\) — OWNER PLACEMENT NEEDED\.** Filed, not selected: the loop
    > continues with the banner\. Repro `analysis/p0e11/zero\-dividend\-scale\-repro\.sql`; PG 18\.3 output
    > `analysis/p0e11/pg18\.out`, goopg output `analysis/p0e11/goopg\-cb3e7e6b6\.out`\.
  - Symptom: `0::numeric / 3` → goopg `0`, PG `0\.00000000000000000000`; `0::numeric / 12345\.67` → goopg `0\.00`, PG
    `0\.000000000000000000000000`\. Visible in TPC\-DS Q12 / Q20 / Q98 output \(revenueratio of a zero\-revenue item\);
    the sf025 checksum normalises trailing zeros, so the gate does not see it\.
  - Cause \(read\): numericDiv's zero\-numerator short\-circuits — numericDivInt64Fast, numericDivBig, and
    numericDivScaleInt64's "defensive" zero return — use max\(dscale\_a, dscale\_b\)\. PG's select\_div\_scale
    \(numeric\.c\) treats a zero dividend as weight 0 / first digit 0 and still derives rscale from the divisor, then
    div\_var returns zero at that scale\.
  - First step: drop the zero short\-circuits in favour of numericDivScale\(am, bm, da, db\) \(nbaseWeightAndFirstDigit
    already answers \(0, 0\) for a zero mantissa, PG's rule\) in both the int64 and big paths; keep the 22012 check
    first\.
  - Expected instrument: the repro identical to `analysis/p0e11/pg18\.out`; Q12/Q20/Q98 outputs identical to
    :65438 tpcds025\.

- [ ] **P0\-E13 — LIKE's `\_` matches one BYTE, not one character, under UTF\-8**
  \(filed 2026\-10\-27 by the loop, M0122\-0012 C17 read of matchSQLLike; S2 — wrong results\)\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(repro values differ from PG\)
  Parent: none
  - > **ESCALATION 2026\-10\-27 \(S2, wrong results\) — OWNER PLACEMENT NEEDED\.** Filed, not selected: the loop
    > continues with the banner\. Repro `analysis/p0e13/like\-underscore\-multibyte\-repro\.sql`; PG 18\.3 expected
    > `analysis/p0e13/pg18\-expected\.out` \(t f t t\); goopg `analysis/p0e13/goopg\-32e9a7306\.out` \(f t f f\)\.
  - Symptom: `'é' LIKE '\_'` is false and `'é' LIKE '\_\_'` true; `'aéb' LIKE 'a\_b'` false; ILIKE likewise\.
  - Cause \(read\): `matchSQLLikeWalk` \(internal/executor/expr\.go\) advances one byte for `\_` \(and compares the
    escaped byte bytewise\)\. PG's MatchText \(src/backend/utils/adt/like\_match\.c\) advances by NextChar
    \(pg\_mblen\) for `\_` under a multibyte encoding \(UTF8MatchText\)\. Pre\-dates C17: the `%`\-only fast path
    declines patterns containing `\_`\.
  - First step: in the walk, `\_` consumes one UTF\-8 rune \(utf8\.DecodeRuneInString width\) and the `%` backtrack
    restarts on rune boundaries; ILIKE's strings\.ToLower path stays\. Check the evalFast/vectorised LIKE siblings
    use the same matcher\.
  - Expected instrument: the repro identical to `analysis/p0e13/pg18\-expected\.out`\.

- [ ] **P0\-E14 — SET ROLE survives ROLLBACK, and an INITIALLY DEFERRED constraint trigger fires at the statement, not at COMMIT**
  \(filed 2026\-10\-27 by the loop, M0122\-0012 C26 RegressSuite review of the `triggers` case; S2 — wrong results /
  privilege\)\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(repro values differ from PG\)
  Parent: none
  - > **ESCALATION 2026\-10\-27 \(S2, wrong results \+ privilege\) — OWNER PLACEMENT NEEDED\.** Filed, not selected:
    > the loop continues with the banner\. Repros `analysis/p0e14/repro\-role\.sql`, `repro\-deferred\.sql`; PG 18\.3
    > output `\*\.pg\.out` \(scratch PG on :5545\), goopg output `\*\.goopg\.out` \(binary at `08a69974e` \+ C26\)\.
  - Symptom 1 \(privilege\): `BEGIN; SET ROLE r; ROLLBACK;` leaves `current\_user` = r \(PG: the session user\);
    likewise after an aborted transaction\. The session keeps the rolled\-back role's privileges — in the upstream
    `triggers` block the next `SELECT` fails with `permission denied`\.
  - Symptom 2 \(wrong result\): a `CONSTRAINT TRIGGER … DEFERRABLE INITIALLY DEFERRED` fires at the INSERT
    statement \(NOTICE before the next statement\); PG queues it and fires it at COMMIT\. A deferred trigger that
    raises therefore fails the INSERT instead of the COMMIT\.
  - The upstream `triggers` regress case flaps between two goopg states across runs on unchanged code \(c21 = c26 vs
    c23 = c25 in tmp/\*\-regress\): with the trigger not firing at all \(row committed, no error, `t`\), or firing at
    the INSERT with the role left set \(`f`\)\. Neither matches PG\.
  - Cause \(read, not yet traced\): SET ROLE state lives on `connTxState` \(internal/postmaster/query\.go
    `applySetRole`, dispatch\.go `ectx\.SetRole`\) with nothing undoing it at transaction end; PG's `role` is a GUC
    rolled back by AtEOXact\_GUC \(src/backend/utils/misc/guc\.c\) on abort and on a non\-LOCAL SET inside a rolled\-back
    transaction\. Constraint triggers \(`trig\.IsConstraint`, internal/executor/expr\.go ~9422\) do not consult
    tgdeferrable/tginitdeferred; PG queues them in the after\-trigger event list and fires deferred ones at
    AfterTriggerFireDeferred \(src/backend/commands/trigger\.c\)\.
  - First step: snapshot the role state at transaction start and restore it on ROLLBACK/abort \(and for SET LOCAL at
    commit\); then route `IsConstraint` triggers marked initially\-deferred into the deferred\-check queue that the
    deferred FK/unique checks already use at COMMIT\.
  - Expected instrument: both repros identical to their `\*\.pg\.out`; the `triggers` regress case stops flapping\.

- [ ] **P0\-E12 — a computed text value over a borrowed scan aliases the scan's page arena: a hash aggregate's stored group keys are overwritten**
  \(filed 2026\-10\-24 by the loop, M0122\-0012 slice 92 follow\-up probe; S2 — wrong results\)\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(the repro's values differ from the generator's\)
  Parent: none
  - > **ESCALATION 2026\-10\-24 \(S2, wrong results\) — OWNER PLACEMENT NEEDED\.** Filed, not selected: the loop
    > continues with the banner\. Repro `analysis/p0e12/substr\-group\-key\-repro\.sql`; expected output
    > `analysis/p0e12/expected\.out` \(computed from the generator: 60 000 distinct keys, count 1 each\); goopg output
    > `analysis/p0e12/goopg\-a05a55628\-serial\.out` — 60 000 rows but only **36 distinct keys**\.
  - Symptom: on a throwaway cluster, `SELECT substr\(t, 2, 6\), count\(\*\) FROM gq GROUP BY 1` \(serial, a HashAggregate
    over a Seq Scan of a 60 000\-row table with a 200\-byte pad column\) returns most rows as `59972\-\|1` — the stored
    keys show a later page's bytes\. With parallelism forced, 44 keys show the pad column's bytes\. Reproduces at
    `a05a55628` and at `2e8519eff` \(pre\-dates M0122\-0012 slices 84\-92\)\.
  - Cause \(read\): evalSubstr slices `src\.StringValue\(\)` — an `unsafe\.String` over the scan's per\-page arena for
    an arena\-backed cell — and `NewStringDatum` wraps those bytes without copying, so the result has `ArenaID` 0
    while its `Buf` points into the arena\. `MaterializeArena` treats it as owned and does not detach it\. The scan
    borrow \(M0145\-0008u `aggregateBorrowsScanRows`: "group keys MaterializeArena'd at group creation"\) hands the
    aggregate the scan's reused row, so the retained key is overwritten when the arena is reused for a later page\.
    The same class hit TPC\-H Q22 in slice 83 \(memory note "computed values alias the scan arena"\); a hand\-built
    Gather over Project\(substr\) over a parallel scan returns pad bytes the same way\.
  - First step: make a value computed from an arena\-backed string own its bytes or carry the arena mark — at the
    source \(evalSubstr and the other slicing builtins: left/right/split\_part/trim family, lower/upper when
    unchanged\) or by having NewStringDatum's callers on arena inputs copy; then re\-check every borrow gate that
    trusts MaterializeArena on computed values \(aggregateBorrowsScanRows group keys and min/max, gatherOp workers
    through projectOp, slice 92's coopBorrowScan already refuses computed targets\)\.
  - Expected instrument: the repro identical to `analysis/p0e12/expected\.out`, serial and parallel; a regression
    test with a substr group key over a multi\-page text scan; TPC\-H/TPC\-DS values unchanged\.

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
  Placement: below item 10 \(owner decision 2026\-10\-16, S7 carve\-out\) — filed, does not displace parity work\.
    Formerly "S2 defect awaiting owner placement" \(writes accepted where PG raises 25006\)\. It is filed without a
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
  Placement: below item 10 \(owner decision 2026\-10\-16, S7 carve\-out\) — filed, does not displace parity work\.
    Formerly "S2 defect awaiting owner placement" \(data loss: a rolled\-back DROP removes the table\)\. Filed without a
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
  Placement: below item 10 \(owner decision 2026\-10\-16, S7 carve\-out\) — filed, does not displace parity work\.
    Formerly "S2 defect awaiting owner placement" \(wrong results: an object exists that the transaction never
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

- [x] **`pg\_relation\_filepath\(\)` does not exist** \(found 2026\-10\-14 during M0122\-0009 S6b\)\.
  Kind: bug
  Parent: none
  - `SELECT pg\_relation\_filepath\(\'t\'\)` → 42883\. PG returns `base/<db>/<relfilenode>` \(dbsize\.c\)\.
  - First step: an executor builtin from the catalog\'s RelFileNode \(tablespace, database, relfilenode\)\.
  - **Done 2026\-10\-21\.** Design `docs/design/0100\-0149/0119\-0010\-pg\-relation\-filepath\.md`\.
    - pg\_relation\_filepath returns the main fork's data\-directory path \(relpathbackend\), the file the storage
      manager opens; NULL for InvalidOid, an unknown OID or a relkind without storage \(dbsize\.c\)\.
    - Sibling fixed with it: pg\_relation\_filenode returned NULL for every index and a view\'s OID for a view; both
      now share relationStorageForOID over catalog\.TableRelkind / IndexRelkind, the relkind pg\_class shows\.
    - The dbsize functions took no name literal \(`pg\_relation\_size\(\'t\'\)` was NULL\); resolveRegclassOID now goes
      through regclassin \(regIdentifierInput\) and raises 42P01 for a missing name, as PG does\.
    - PG 18\.3 scratch comparison identical except the path\'s database directory \(pg\_database shows 16384, files
      live in base/5: the known 0119\-0006bv split\)\.
    - Ledgered: no t<procnumber>\_ temp prefix; nailed system\-index stubs and shared catalogs return NULL;
      pg\_class\.relfilenode ignores RelFileNodeOID\.
    - Pin: TestPgRelationFilepathAndFilenode \(fails without the relkind check\)\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, tpch\-spotcheck, sf025\.
  Movement: none — catalog/executor correctness — no plan instrument \(dbsize builtins only\)

- [ ] **Boolean predicate columns are advertised as `text`** \(found 2026\-10\-14 during M0122\-0009 S7\)\.
  Kind: bug
  Placement: below item 10 \(owner decision 2026\-10\-16, S7 carve\-out\) — filed, does not displace parity work\.
    Formerly "S2 defect awaiting owner placement" \(wrong result type on the wire: a typed client decodes `t` as a
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
- [x] **In\-process WAL writer timeline switch at promotion, then XLOG\_END\_OF\_RECOVERY** \(filed 2026\-10\-14 at the
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
  - **Done 2026\-10\-21\.** Design `docs/design/0100\-0149/m0119\-promotion\-timeline\-switch\.md`\.
    - Writer\.SwitchTimeline resumes WAL at the end of the last replayed record with its xl\_prev, copies the switch
      segment to the new TLI \(XLogInitNewTimeline\), removes later old\-TLI segments \(RemoveNonParentXlogFiles\), and
      makes the writer\'s TLI live \(atomic; pads read it through padLayout\.tliRef\)\.
    - Readers open a segment\'s newest timeline \(openSegmentFile, RecordIterator fallback\)\.
    - finalizePromotion switches, then writes XLOG\_END\_OF\_RECOVERY \(EncodeEndOfRecoveryPG\) and minRecoveryPoint\.
    - Defects fixed on the way: post\-promotion WAL on the old TLI; the first checkpoint stamping TLI 1 back into
      pg\_control; a stale first xl\_prev after walreceiver appends; post\-promotion records unreadable after a
      partial received tail\.
    - pg\_waldump on the promoted standby: `END\_OF\_RECOVERY tli 2; prev tli 1` at the history switch LSN, prev\-link
      accepted, the post\-promotion insert decodes after it \(all three failover modes\)\.
    - Pins: TestSwitchTimelineResumesAtEndOfLog \(fails without the tracker reset or with oldest\-TLI reads\),
      TestEndOfRecoveryRecordRoundTrips, checkPromotedTimelineWAL\.
    - Ledgered: archive\-recovery promotion path; \.partial rename under archiving; segment recycling; the promotion
      checkpoint request\.
    - Gates: xlog \-race, cmd/goopg, E2E physical/failover/standby family, recovery/pg\_ctl/pg\_waldump ports, units,
      internal/postmaster, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm, sf025\.
  Movement: none — WAL/promotion correctness — no plan instrument \(no planner change\)
- [x] **Visibility\-map changes as XLOG\_HEAP2\_VISIBLE** \(filed 2026\-10\-14 at the M0122\-0009 close\)\.
  Kind: impl
  Parent: none
  - goopg logs every VM change as a native rmgr\-128 `RecordKindHeapVisible` record with no block reference and no
    tablespace \(design §4\.4 item 2\)\. A PG standby cannot replay rmgr 128\.
  - PG logs a set as `XLOG\_HEAP2\_VISIBLE` \(block 0 = VM page, block 1 = heap page\); clears ride the heap records\'
    `\*\_ALL\_VISIBLE\_CLEARED` flags\.
  - First step: emit sets as `XLOG\_HEAP2\_VISIBLE` through the assembler and route goopg replay to the existing decoded
    arm; keep a length\-versioned decode of the native record for old WAL\.
  - Expected instrument: `pg\_waldump` decoding goopg\'s VACUUM WAL; the walsummarizer VM checks stay green\.
  - **Slice 1 LANDED 2026\-10\-21** \(clears\)\. Design `docs/design/0100\-0149/m0119\-visibility\-map\-pg\-wal\.md`\.
    - Order matters: PG\-form sets alone would let a PG standby set all\-visible bits goopg then clears only in rmgr 128,
      so its scans would trust stale pages\. Clears first\.
    - Heap insert/delete/update records now carry XLH\_\*\_ALL\_VISIBLE\_CLEARED, read from the runtime VM at log time
      \(initdb\.Open hooks; every executor site logs, then VM\.ClearBlock, under one page lock\)\.
    - PG\-format heap redo honours the flags \(redoClearAllVisible before the page redo, clearPageAllVisible after\):
      insert/delete/update ignored them, so a goopg standby of PG kept stale all\-visible bits\.
    - pg\_waldump on goopg WAL: INSERT 0x09, DELETE 0x03, HOT\_UPDATE 0x11 after each VACUUM; seeds carry no bit\.
    - Pins: TestPGHeapRecordsClearVMBits \(fails without the insert arm\'s clear\), TestHeapEncodersSetVMClearedFlags,
      TestPgWaldumpHeapRecordsCarryVMCleared\.
    - Next \(slice 2\): emit sets as XLOG\_HEAP2\_VISIBLE \(block 0 VM page, block 1 heap page\) and retire the native
      set/clear records; ledgered gaps in the design §6\.
  - **Slice 2 LANDED 2026\-10\-21 — Done\.** Design §7\.
    - Sets are PG\'s XLOG\_HEAP2\_VISIBLE \(EncodeHeapVisiblePG: block 0 the vm page, block 1 the heap page\); goopg\'s
      redo applies them through the decoded arm written for PG WAL\.
    - Plain clears are unlogged \(VisibilityMap\.ClearBlockNoWAL\) at every site whose heap record carries the flag and
      at VACUUM\'s corrective clear, as in PG\.
    - pg\_waldump: three `Heap2 VISIBLE … fork vm blk 0` records, no custom\-rmgr record in the stream\.
    - Crash test TestVisibilityMapSurvivesCrashFromPGRecords: VACUUM → kill → bit restored; INSERT → kill → bit
      cleared \(fails with redoClearAllVisible disabled\)\.
    - Ledgered: native records remain for the row\-lock frozen clear, a dropped relation\'s map and the catalog
      resync clear; no conflict horizon; no CATALOG\_REL flag\.
    - Gates: units, postmaster, replication E2E family, RegressSuite, vacuum isolation specs, pg\_waldump ports,
      tpch\-spotcheck, acceptance arm, sf025\.
  Movement: none — WAL/redo correctness — no plan instrument \(no planner change\)
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
  - **Candidate pool (owner-seeded 2026-10-27 from the ledger and the slices' Open/Next/Left notes; the loop
    maintains it and re-ranks it by predicted gain after each G1–G3 pass — return-first rule, banner item 10):**
    - C1 Numeric hash-join and GROUP BY keys hashed as canonical strings (Q13 `mapaccess2_faststr` ~5%; HammerDB
      TPC-H keys are numeric) — ledger :3169, :3171.
    - C2 Q14/Q23 per-probe constant factor on `store_sales_pkey` probes: leafScanStart's generic comparator
      (~12% of Q14, slice 80 "Left") and the join-aware read set for inner index scans (ledger :3210).
    - C3 Q67 sort comparator memory access over eight `char(n)` keys (part 9, the merge-sort drop).
    - C4 Q95 remaining excess (part 9 :205-211): the consumer's `retainBuildRow` copy and 10 M CTE rows through
      two semi joins; slice 92 did not move end-to-end time.
    - C5 Q78 sort rows plus merge-join output bytes after slice 88's −48% — re-measure first.
    - C6 Lead-conjunct prefilter for a qual that reads the last column (slice 86 "Open").
    - C7 Lazy deform before a selective hash probe, plus the Parallel Hash empty exit (ledger :3207, slice 72 "Next").
    - C8 Nested-loop pair copy and the per-row index-only-scan output row (ledger :3186, :3187, :3196).
    - C9 Borrow through merge join, Sort and Gather Merge (ledger :3172, :3177).
    - C10 Hashed SAOP for numeric/date (ledger :3198); other hashed DISTINCT sorts (ledger :3204).
    - C11 Lazy window evaluation (ledger :3208); Q51 still 2.7x (slice 62); PG inverse-transition moving aggregate.
    - C12 Multi-key / grouping-set aggregate int lane plus the NULL group (ledger :3173);
      `groupBoundsFixed` ~28 MB/run (slice 75).
    - C13 Flat expression step program, no recursive 48-byte Datum return (ledger :3176, :3181) — design-sized;
      needs a slice-sized first step (the evalFastLeaf proxy is on the Below-bar list).
    - C14 VirtualSlot/PackedSlot arms in fastOperandPtr (ledger :3209); set-valued read set through joins
      (ledger :3179 — re-measure on a release build before ranking).
    - C15 Planning-time items: pathkey canonicalisation (ledger :3182, :3192); sublink lowering for
      WindowAgg/SetOp/SRF (ledger :3212a).
    - C16 \(added 2026\-10\-27 from census\-20261027, ranked FIRST by predicted gain\) TPC\-DS Q64 planning: goopg
      636 ms vs release PG 51\.9 ms per plan \(~584 ms of Q64's 846 ms excess, ~57 % of its goopg time\)\. All of it
      is GEQO's ~500 tours \(gimmeTree → makeJoinRel\); PG runs the same tour count, so the gap is per\-joinrel cost\.
      Profile \(`tmp/q64plan.pprof`, 25 s\): addPaths 59 %, mallocgc 26 %, joinrelNarrowedWidths 12\.5 % \(its pure
      inputs relNarrowedColumns 6\.8 % \+ attrNeededAbove 3\.7 %, recomputed per joinrel per tour; PG reads
      attr\_needed computed once per base rel — build\_joinrel\_tlist, relnode\.c\), innerRelProvenUnique 7\.9 %
      \(provableKeys/keysCovering per call; PG caches innerrel\_is\_unique results in unique\_for\_rels /
      non\_unique\_for\_rels, analyzejoins\.c\), pathkey usefulness 12\.7 %\. Steps: C16a memoise the narrowed\-width
      inputs; C16b cache inner\-unique proofs; then re\-profile\.
    - C17 \(added 2026\-10\-27 loop \#196, G3\) TPC\-H Q13 `o\_comment NOT LIKE '%special%requests%'`:
      `matchSQLLike` 7 % of Q13; a `%lit%lit%` pattern is a substring search \(PG's MatchText over bytes\)\.
    - C18 \(added 2026\-10\-27 loop \#196, G3, ranked first\) B\-tree binary\-search probes re\-derive per\-page
      invariants: `PageGetItemRawAllowDeadNoCopy` recomputes `PageLinePointerCount` and `pgDataSlot` reads the
      special space on EVERY probe of `leafScanStart` / `findChildBlockDirect` \(TPC\-DS Q72: PageLinePointerCount
      \+ Header ~5 % of samples; leafScanStart 27\.6 %, findChildBlockDirect 19\.8 %\); PG's \_bt\_binsrch reads
      PageGetMaxOffsetNumber / P\_FIRSTDATAKEY once per page\. Q14/Q23 share the path \(btree search ~15–18 %\)\.
      - G3 notes 2026\-10\-27: C1's premise is stale \(Q13 keys already on int64 maps; no mapaccess2\_faststr\)\. C2
        \(Q14 comparator\) predicts ~1–1\.5 %: compareFixedPrefix already a fixed\-width fast path; pgTInfo's 10 % in
        Q72 is the first\-touch cache miss of each probed tuple \(PG pays it too\), not key re\-decoding\.
    - C19 \(added 2026\-10\-27 loop \#198, G3 on TPC\-H Q17\) data\-page checksum verification on buffer reads:
      `pageChecksumBlock` 12\.25 % of Q17 \(per\-word bounds check \+ byte assembly \+ a pd\_checksum compare inside
      the 32\-lane loop\)\. Both goopg clones and the release PG clusters have data checksums on \(version 1\); PG's
      pg\_checksum\_block \(checksum\_impl\.h\) is written for compiler vectorisation\. Q14's index\-scan string
      copies \(slicebytetostring ~3 %\) noted, not taken \(borrow/arena lifetime risk, P0\-E12 class\)\.
    - Not M0122-0012 candidates (plan-visible → parity tasks): hashed SubPlan for EXISTS…OR EXISTS (slice 65);
      Gather projection below the Gather (ledger :3190).
  - **Below-bar list (ineligible on return for the same target query — key: mechanism = code site + PG function;
    owner-seeded 2026-10-27; the loop appends every attempt that misses the 3% bar, landed or dropped):**
    - Slice 35 projection under an aggregate (Q14 −2.5%); slice 61 cached-root descents (Q87 −1.4%; Q72 −2.1%);
      slice 85 Gather worker VirtualSlot (Q10 −0.4% instructions).
    - Dropped: evalFastLeaf proxy (reverted, Q6 slower; ledger :3181); operand leaf copy (loop #40); slice 8
      verdict pre-pass (slower); slice 9 constant divisor; `MaterializedSlot.owned` elision (Q97 slower); per-Context
      `pgIndexKeyColumns` cache (flat — relevant to C2); attcacheoff cached-offset deform (slice 72, flat); slice 72's
      conditional NULL write and the skip-arm conditional NULL store (+4.7%); pdqsort (Q87 +8%) and
      `SortStableFunc`; buffered stable merge sort (Q67 −3% instructions, +2.6% cycles); compact sort-to-GM-queue
      (slice 89); NL stream outer copy (does not fire in Q10).
    - Landed below the bar: C16g content\-keyed truncate\_useless\_pathkeys memo \(slice 99, Q64 planning
      instructions −2\.96 %, `pathkeys\_useful\.go` truncMemo vs PG build\_join\_pathkeys\)\.
    - Dropped: C16h merge candidates priced before their Sort paths are built \(`tryMergeJoinPath` vs PG
      try\_mergejoin\_path / initial\_cost\_mergejoin, Q64 planning instructions −0\.5 %\)\.
    - Landed below the bar: C21 bitmap heap scan joins the scan borrow \(slice 103, Q17 instructions −2\.06 %,
      `bitmapHeapScanOp\.emitRow` vs PG ExecStoreBufferHeapTuple\)\.
    - Dropped: C33 NLI\-inner index\-scan read set through buildCompleteChild \(Q14 instructions −0\.03 %: Q14's
      probes sit under lateral Joins the consumer threading does not cross\)\.
    - Dropped: C24 current\-group key compare before the hash lookup in a sorted rollup's drain \(`aggregateOp\.Open`
      vs PG agg\_retrieve\_direct / execTuplesMatch, Q67 instructions −0\.4 %\)\.
    - Dropped: C22 read set for a seq scan at the bottom of a consumer\-complete chain \(`buildCompleteChild` \+
      consumerLiveColumns over lowered sublinks, Q17 instructions ±0 %: the lineitem scan already had readCols
      \{l\_extendedprice, l\_quantity, l\_partkey\} via the Project above it, slice 19\)\.
  - **Yield record:** 2026-10-24 under the old rule (a) after slice 92 (superseded by the return-first rule).
    - Yield 2026\-10\-27 \(loop \#194\): C16g −2\.96 % \(slice 99, landed\) and C16h −0\.5 % \(dropped\) both missed the
      bar on Q64 planning; worked: the next loop takes ONE item\-10 task in order — M0119 holds only `\[\!\]`
      M0119\-0006, M0147 holds no selectable child, so the first is M0122\-0013 \(ledgered resume point: a standby's
      recovery checkpoint advancing pg\_control like PG's CreateRestartPoint\)\. Then return; the miss counter
      resets\.
      - Worked: M0122\-0013 standby restartpoint, landed 2026\-10\-27 \(loop \#195\)\. Yield over — the next item\-10
        selection returns to M0122\-0012 with the miss counter at 0\.
    - Yield 2026\-10\-27 \(loop \#200\): C21 −2\.06 % \(slice 103, landed\) and C22 ±0 % \(dropped\) missed the bar;
      worked: the next loop takes ONE item\-10 task in order — M0119 holds only `\[\!\]` M0119\-0006, M0147 no selectable
      child, so M0122\-0013 \(ledgered residual 2026\-10\-27: a standby never stamps DB\_IN\_ARCHIVE\_RECOVERY, so
      the restartpoint's minRecoveryPoint arm is dormant and a shutdown restartpoint misses
      DB\_SHUTDOWNED\_IN\_RECOVERY\)\. Then return; misses reset\.
      - Worked: M0122\-0013 standby pg\_control state, landed 2026\-10\-27 \(loop \#201\)\. Yield over — the next
        item\-10 selection returns to M0122\-0012 with the miss counter at 0\.
  - **Return 2026\-10\-27 \(loop \#196\)**: C1 stale, C2 below 3 % predicted \(G3 above\); took C18\.
    - **Target \(C18\)**: TPC\-DS SF0\.25 **Q72**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs;
      Q72 plans in 73 ms of a 2\.6 s run, so execution dominates\)\. Bar: −3 %\.
    - **C18 LANDED \(slice 100\)**: `pageProbe` \+ `PageGetItemRawAllowDeadNoCopyN` — leafScanStart and
      findChildBlockDirect read the line\-pointer count and P\_FIRSTDATAKEY once per page\. Q72 26\.999/26\.995 →
      26\.170/26\.171 Ginstr/q \(**−3\.06 %, meets the bar**\); plans identical\. Misses 0\. Next on return: C17 \(Q13
      LIKE substring\) or a fresh G3 on Q14/Q23 \(decodeRowRangeInfo 13 %/9 %\)\. Design: part 9 §Slice 100\.
    - **Target \(C17, loop \#197\)**: TPC\-H SF1 **Q13**, metric **instructions:u per query** \(`tools/h\-instr\.sh`
      on tmp/q17\-clone, 5 runs\)\. Mechanism: a `%`\-only LIKE pattern \(no `\_`, no escape\) matched as prefix /
      ordered substrings / suffix instead of the backtracking walk\. Bar: −3 %\.
    - **C17 LANDED \(slice 101\)**: `matchLikeSegments` \(strings\.Index\) before the walk for `%`\-only patterns\. Q13
      13\.815/13\.815 → 12\.226/12\.226 Ginstr/q \(**−11\.5 %, meets the bar**\); Q13 values identical\. Misses 0\.
      Discovery: P0\-E13 \(LIKE `\_` bytewise under UTF\-8; S2, filed\)\. Next on return: a fresh G3 on Q14/Q23/Q17
      \(decodeRowRangeInfo 13 %/9 %\) — the pool's remaining entries need re\-measurement\. Design: part 9 §101\.
    - **Target \(C19, loop \#198\)**: TPC\-H SF1 **Q17**, metric **instructions:u per query** \(`tools/h\-instr\.sh`,
      5 runs\)\. Mechanism: pg\_checksum\_block over the page as 32\-lane uint32 rows without per\-word bounds checks
      or the in\-loop pd\_checksum test\. Bar: −3 %\.
    - **C19 LANDED \(slice 102\)**: `pageChecksumBlockRows` \(in\-place \[32\]uint32 rows, pd\_checksum masked once\);
      portable form kept as fallback \+ oracle\. Q17 32\.104/32\.082 → 27\.848/27\.862 Ginstr/q \(**−13\.2 %, meets
      the bar**\); micro 1058 vs 2473 ns/page; stored checksums unchanged\. Misses 0\. Next on return: re\-profile
      Q17 \(decodeRowRangeInfo 23 %, NumericInt64FromStoredPayload 9 %\) or Q18\. Design: part 10 §102\.
    - C20 \(added 2026\-10\-27 loop \#199, G3 on Q17/Q18 after C19\): `NumericInt64FromStoredPayload` 9\.35 % of Q17,
      7\.3 % of Q18 \(lineitem's NUMERIC columns, HammerDB\); a third of it is the goopg\-only legacy\-text probe
      \(`numericPayloadIsLegacyText`\), the rest 128\-bit accumulation for 1–2 base\-10000 digits\.
    - **Target \(C20, loop \#199\)**: TPC\-H SF1 **Q17**, metric **instructions:u per query** \(`tools/h\-instr\.sh`, 5
      runs\)\. Mechanism: a short\-form NumericData fast path — reject the legacy\-text probe from the header bytes
      \(byte 1 carries NUMERIC\_SHORT's 0x80; byte 0 ASCII non\-space\), accumulate ≤ 4 digits in uint64\. Bar: −3 %\.
      - Not attempted: the line profile puts most of the legacy\-text probe in call overhead \+ the payload's first
        touch \(paid by the decode anyway\); predicted ≈1–2 %, below the bar\. Superseded by C21\.
    - C21 \(added loop \#199\): Q17's SubPlan, Aggregate\(avg\) → Bitmap Heap Scan on lineitem, clones every fetched
      row \(`bitmapHeapScanOp\.emitRow` → `cloneRowOwned`, 8\.1 % of Q17 \+ its GC\) though the aggregate
      provably keeps nothing \(aggregateBorrowsScanRows\): `markScanBorrowChain` hands the borrow only to seq scans\.
      PG's bitmap heap scan stores a buffer tuple in the slot \(ExecStoreBufferHeapTuple\)\.
    - **Target \(C21, loop \#199\)**: TPC\-H SF1 **Q17**, metric **instructions:u per query** \(`tools/h\-instr\.sh`, 5
      runs\)\. Bar: −3 %\.
    - **C21 LANDED BELOW THE BAR \(slice 103\)**: `markScanBorrowChain` hands the borrow to `bitmapHeapScanOp`;
      `emitRow` yields scanRow when no ctid column is emitted\. Q17 27\.860/27\.877 → 27\.299/27\.289 Ginstr/q =
      **−2\.06 %, misses the 3 % bar** \(the 8 % profile share was mostly allocation/GC cycles, which instructions do
      not count\); values and plans identical; Below\-bar list updated\. **Miss counter 1\.** Design: part 10 §103\.
    - C22 \(added loop \#200, G3 on Q17 after C21\): the main lineitem probe scan decodes columns 0–5 \(six
      NUMERICs; decodeScanRowRange 21 % of Q17\) though only l\_partkey / l\_quantity / l\_extendedprice are read:
      `buildCompleteChild` carries consumer\-completeness through the hash join to its left side \(slice 66\) but has
      no seq\-scan case, so the scan's read set \(`readCols`, slice 19\) stays nil\. PG fetches only referenced
      attributes \(EEOP\_SCAN\_FETCHSOME\)\. TID\-bitmap entry sizing \(goopg 2048\-bit vs PG MaxHeapTuplesPerPage
      291\) noted, ~2 %, not taken\.
    - **Target \(C22, loop \#200\)**: TPC\-H SF1 **Q17**, metric **instructions:u per query** \(`tools/h\-instr\.sh`, 5
      runs\)\. Bar: −3 %\.
    - **C22 MEASURED AND DROPPED \(loop \#200\)**: Q17 27\.304/27\.302 vs 27\.287/27\.322 Ginstr/q = ±0 %\. A
      build trace showed the premise wrong: Join → Project → Seq Scan lineitem, and the Project already narrows the
      scan's readCols to the three read columns \(slice 19\); even with consumerLiveColumns taught lowered sublinks
      \(Q17's join filter made it decline\), nothing further is skipped\. Q17's remaining decode is the three NEEDED
      NUMERICs\. **Second consecutive miss → one\-task yield** \(Yield record\)\.
  - **Return 2026\-10\-27 \(loop \#202, after the M0122\-0013 yield; misses 0\)**: fresh G3 on DS Q23 / Q67 / Q39
    \(perf record, `tmp/g3\-202/`\)\. Q23 is spread thin \(decodeRowRangeInfo 5\.3 %, btree probes ~10 % already C18\);
    Q39 is expression evaluation \(evalFastExpr 8\.7 %\); Q67 has one goopg\-only mechanism, added as C23\.
    - C23 \(G3 on Q67\): a SORTED single\-rollup aggregate \(Q67: 9 grouping sets, 255 275 groups\) hashes every
      group and then re\-sorts all output rows with a comparator that does `map\[int\]bool` set\-membership lookups
      and typed datum compares \(`aggregateOp\.Open\.func4` 9\.5 % of Q67 samples\) to rebuild PG's one\-pass order\.
      PG emits each group when its prefix changes in the sorted input \(nodeAgg\.c agg\_retrieve\_direct\), so its
      order is the input's close order — no comparator\. Predicted 7–9 %\.
    - **Target \(C23\)**: TPC\-DS SF0\.25 **Q67**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs\);
      baseline 24\.770/24\.773 Ginstr/q at `2eeabf36f`\. Bar: −3 %\.
    - **C23 LANDED \(slice 104\)**: the drain stamps each group's `closeSeq` \(the row that left it behind in its
      set\); `rollupCloseOrder` orders by \(close row, set length desc\), falling back to the comparator if a closed
      group is fed again\. Q67 24\.770/24\.773 → 21\.477/21\.471 Ginstr/q \(**−13\.3 %, meets the bar**\); Q67 output
      byte\-identical; plans 99/99 same; regress groupingsets/aggregates/window byte\-identical\. Misses 0\. Design:
      part 10 §104\.
    - C24 \(added loop \#202, after C23\): Q67's drain still hashes each input row into all nine sets' tables
      \(appendGroupKey \+ trimmed char\(n\) keys \+ aeshash \+ mapaccess2\_faststr, ~8 % of Q67\); PG's sorted path
      compares the row with the current group \(agg\_retrieve\_direct / execTuplesMatch\)\. Next on return: C24 on Q67,
      or Q67's final Sort \(lessKeyVals \+ cmpbody ~20 %\) — re\-profile first\.
  - **Return 2026\-10\-27 \(loop \#203\)**: re\-profiled Q67 at `8432307f1` by instructions:u \(`tmp/g3\-203/q67i\.data`\):
    final Sort 14\.7 %, worker Sort 7\.5 %, lessKeyVals 9\.3 %, cmpbody 6\.7 %, **strings\.TrimRight 6\.5 %**\.
    - C25: the bpchar blank trim \(PG bcTruelen, varchar\.c\) runs as Go's byte\-at\-a\-time TrimRight over char\(50\)
      padding \(~40 blanks\) once per sort\-key value \(trimStringDatum\) and once per grouping set per bpchar column
      \(appendGroupColKey\)\. Mechanism: one helper `bcTrimRight` that drops whole 8\-byte words of blanks first, used at
      all 19 blank\-trim sites \(sibling paths stay identical\)\. Predicted ~5 %\. lessKeyVals' cycles are mostly
      key\-load cache misses \(the 48\-byte Datum copies after each load\), which instructions:u does not count\.
    - **Target \(C25\)**: TPC\-DS SF0\.25 **Q67**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs ×
      2, fresh A/B against `8432307f1`\)\. Bar: −3 %\.
    - **C25 LANDED \(slice 105\)**: `bcTrimRight` \(bctrim\.go\) at all 19 blank\-trim sites\. Q67 21\.471/21\.485 →
      20\.618/20\.618 Ginstr/q \(**−4\.0 %, meets the bar**\); Q67 output byte\-identical; plans 99/99 same\. Misses 0\.
      Next on return: Q67's final Sort — PG's SortTuple `datum1` \(first key inline in the sorted array, tuplesort\.c\)
      against goopg's per\-row key slices reached through `perm` \(cycle\-side; check its instruction share first\),
      or C24 \(drain hash vs current\-group compare\)\. Design: part 10 §105\.
  - **Return 2026\-10\-27 \(loop \#204\)**: instructions:u profile of Q67 at `08a69974e` \(`tmp/g3\-204/q67i\.data`\):
    final Sort 17\.9 %, worker Sort 8\.9 %, sort\-key evaluation 8\.2 %, applyAgg 7\.8 %, aggregate map 4\.1 %;
    **lessKeyVals 11\.4 % self**, cmpbody 7\.6 %\.
    - C26: `sortOp\.lessKeyVals` copies both 48\-byte Datums of every key \(`av, bv := a\[i\], b\[i\]`\), tries
      `fastCompareDatum` \(which declines every string pair\), then copies them again through the value\-receiver
      `StringValue` before `strings\.Compare`\. PG compares in place: ApplySortComparator over Datums with a comparator
      resolved once per key \(SortSupport, tuplesort\.c / sortsupport\.h; varstrfastcmp\_c for text\)\. Mechanism: compare
      through pointers into the key slices, with a direct string arm for character\-string keys \(Buf\-backed bytes read
      in place\) and the existing fast/typed fallbacks otherwise\. Predicted ~5 %\.
    - **Target \(C26\)**: TPC\-DS SF0\.25 **Q67**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs ×
      2, fresh A/B against `08a69974e`\)\. Bar: −3 %\.
    - **C26 LANDED \(slice 106\)**: `lessKeyVals` compares through pointers; string keys go straight to
      `strings\.Compare` over `datumStringInPlace`\. Q67 20\.647/20\.616 → 19\.071/19\.065 Ginstr/q \(**−7\.6 %, meets
      the bar**\); Q38 −2\.7 %; Q67 output byte\-identical; plans 99/99 same\. Misses 0\. Discovery: P0\-E14 \(S2,
      filed\)\. Next on return: SortTuple `datum1` \(cycles\) or C24; or rotate G2 to Q14/Q23 \(Q67's excess has fallen
      from 1344 ms\)\. Design: part 10 §106\.
  - **Return 2026\-10\-27 \(loop \#205\)**: instructions:u profiles of DS Q14 and Q23 at `8d794539a`
    \(`tmp/g3\-205/q14i\.data`, `q23i\.data`\): B\-tree descents ~20 % \(C18 territory\), heap decode, buffer lookups;
    prune\-on\-access already once per block; no new mechanism ≥ 3 %\. Q67 re\-profiled \(`q67i\.data`\): aggregate
    drain 30\.6 %, of which `groups\[string\(gkBuf\)\]` \(operators\_join\_agg\.go:2876\) 4\.0 % and the emit loop's
    `groups\[key\]` re\-lookup 1\.5 %\.
    - **Target \(C24\)**: TPC\-DS SF0\.25 **Q67**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs ×
      2, fresh A/B against `8d794539a`\)\. Mechanism: in a sorted rollup \(closeOrderOK\), compare the row's group key
      with the current group's key for that set and consult the hash table only when the group changes \(PG
      agg\_retrieve\_direct compares the row with the group's first tuple, execTuplesMatch\)\. Predicted ~3\.5 %\. Bar:
      −3 %\.
    - **C24 MEASURED AND DROPPED \(loop \#205\)**: Q67 19\.066/19\.055 → 18\.990/18\.978 Ginstr/q = **−0\.4 %, misses the
      bar**\. The shortcut rarely fires where the lookups are: the detail sets have nearly one group per input row
      \(255 275 groups from 133 k rows\), so almost every row changes group in them and still goes to the map\. The
      remaining lever is structural — no hash table at all for a sorted rollup \(every key change founds a group, as
      in PG\), which also removes the map insert and the emit loop's `groups\[key\]` re\-lookup \(~7 % together\) but
      drops the reopen check that guards against a sort/group\-equality mismatch; ledgered, not attempted\. Below\-bar
      list updated\. **Miss counter 1\.**
  - **Return 2026\-10\-27 \(loop \#206\)**: S9 reused `routing\-20261027z` \(`acd4ff2d7` is no\-code\)\. Instruction
    samples skid onto the instruction after a load \(trimStringDatum's 4 % sits on one `movabs` after the word load\),
    so per\-symbol shares lean to stalls; structural repeated work is the safer lever\.
    - C27: a multi\-set aggregate \(grouping sets\) evaluates each aggregate's argument once PER SET per input row —
      `applyAgg` calls `aggEvalList\(o\.aggArgNodes, …\)` inside the drain's `for si, set := range sets` loop, so Q67's
      `sum\(coalesce\(ss\_sales\_price\*ss\_quantity, 0\)\)` is computed 9× per row\. PG's ExecBuildAggTrans
      \(execExpr\.c:3934\-3943\) evaluates a transition's arguments once and then calls the transition for each
      concurrently evaluated grouping set\. Mechanism: per\-row cache of each aggregate's argument value, valid for
      the current input row only, used when the aggregate computes more than one set; values that point at memory
      \(Buf or arena\) are re\-evaluated, so only inline scalars are shared\. Predicted ~4–5 %\.
    - **Target \(C27\)**: TPC\-DS SF0\.25 **Q67**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs ×
      2, fresh A/B against `acd4ff2d7`\)\. Bar: −3 %\.
    - **C27 LANDED \(slice 107\)**: `aggArgShare` / `aggArgRow` / `aggArgSeen` / `aggArgVal` — a multi\-set aggregate
      reuses an inline\-scalar argument already evaluated for the current row\. Q67 19\.065/19\.081 → 18\.221/18\.221
      Ginstr/q \(**−4\.5 %, meets the bar**\); all 11 grouping\-set TPC\-DS queries byte\-identical; plans 99/99 same\.
      **Misses reset to 0\.** Next on return: Q67's remaining excess \(Sort datum1, the no\-hash rollup after the
      equality proof\) or a fresh G1 census \(census\-20261027 expires 2026\-10\-30; Q67 has fallen from 24\.77 to
      18\.22 Ginstr/q\)\. Design: part 10 §107\.
  - **Return 2026\-10\-27 \(loop \#207\)**: S9 exhausted at `83bfa2945` \(census aa\)\. G3 by instructions:u at HEAD:
    Q23 map work in aggregateOp\.Open 5\.2 % \(drain lookup 2\.3 %, emit re\-lookup `groups\[key\]` 1\.8 %; ≤ 3 % even with
    PG\-style table pre\-sizing — the plan carries no numGroups\), allocation spread thin; Q72 ~70 % B\-tree probing
    \(`tmp/g3\-207/q72i\.data`\)\. A release\-PG instruction baseline for Q72 was attempted and denied by the permission
    classifier \(starting bench/pg\-release\) — not retried\.
    - C28 \(G3 on Q72\): both binary\-search sites — `findChildBlockDirect` \(internal pages\) and `leafScanStart` \(leaf,
      non\-posting\) — run every probed tuple through `parseNoCopy` → `parseItemBody`, which validates, extracts the
      downlink/body and builds an `item`, though in the tuple format the compared key is `raw` itself\. PG's
      \_bt\_binsrch → \_bt\_compare reads the tuple in place\. Mechanism: compare `raw` directly, keeping only
      parseItemBody's rejection checks \(length, t\_info size, posting / short heap\-TID pivot\)\. Under
      findChildBlockDirect the parse layer is ~3\.3 % of Q72 \(parseItemBody 2\.3 %, parseAliasing 1\.0 %\)\. Predicted
      ~3 %\.
      - Noted, not taken: the leaf probe of a POSTING tuple calls parsePostingRaw, which allocates the TID array and
        a key copy per probe \(Q72's probed indexes are unique, so it does not show there\)\.
    - **Target \(C28\)**: TPC\-DS SF0\.25 **Q72**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs ×
      2, fresh A/B against `83bfa2945`\)\. Bar: −3 %\.
    - **C28 LANDED \(slice 108\)**: `probeTupleOK` \(parseItemBody's rejections only\); findChildBlockDirect and
      leafScanStart compare `raw` in place in the tuple format\. Q72 26\.166/26\.168 → 24\.351/24\.352 Ginstr/q
      \(**−6\.9 %, meets the bar**\); Q72/Q14/Q23 outputs byte\-identical; plans 99/99 same\. Misses 0\. Next on return:
      the posting\-tuple leaf probe \(parsePostingRaw allocates per probe\) where an index has duplicates, or a fresh
      G1 \(census\-20261027 expires 2026\-10\-30\)\. Design: part 10 §108\.
  - **Return 2026\-10\-27 \(loop \#208\)**: S9 reused census aa \(`e35fff402` is nbtree\-only\)\. Q14 after C28 is flat
    \(`tmp/g3\-208/q14i\.data`: nbtree 34 %, index\-scan heap fetch 31 %, no ≥ 3 % mechanism\)\. TPC\-H Q18
    \(`tmp/g3\-208/h18i\.data`\): the hash aggregate on l\_orderkey \(~1\.5 M groups\) spends `igroups\[ik\]` 10\.3 % \(the
    lookup PG also pays\), then `demoteIntGroups` at end of drain 2\.9 % \+ string\-map inserts/rehash \+
    canonicalNumericKey allocations, and the emit loop's `groups\[key\]` re\-lookup 4\.0 %\.
    - C29: the int64 lane is converted to string keys at the end of every drain only so the emit loop can walk
      `order` \(key strings\) and look each group up again\. PG's agg\_retrieve\_hash\_table iterates the table's entries
      \(ScanTupleHashTable\); nothing is re\-keyed or re\-looked\-up\. Mechanism: keep the founded groups in a
      `\[\]\*groupRuntime` list in founding order, emit from it, and skip the end\-of\-drain demotion on the plain
      \(Simple\-mode\) emit path\. Partial/Final/row\-transport paths keep key strings\. Predicted ~7 %\.
    - **Target \(C29\)**: TPC\-H SF1 **Q18**, metric **instructions:u per query** \(`tools/h\-instr\.sh`, 5 runs × 2, fresh
      A/B against `e35fff402`\)\. Bar: −3 %\.
    - **C29 LANDED \(slice 109\)**: `founded` \(founding\-order group list\); Simple\-mode emit walks it and skips the
      end\-of\-drain int\-lane demotion\. Q18 44\.813/44\.962 → 42\.328/42\.304 Ginstr/q \(**−5\.7 %, meets the bar**\);
      acceptance 24 MATCH; plans 99/99 same\. Misses 0\. Next on return: hash\-table pre\-sizing needs a plan\-node
      numGroups \(planner change; ledgered\); or a fresh G1 \(census\-20261027 expires 2026\-10\-30\)\. Design: part 10
      §109\.
  - **Return 2026\-10\-27 \(loop \#209\)**: S9 exhausted at `a8367e529` \(census ab\)\. **G1/G2 re\-run**:
    `analysis/m0122/m0122\-0012/census\-20261027b/` \(HEAD `a8367e529`, 3 sf025 sweeps \+ 3 tpch\-gap runs vs the committed
    release\-PG tsv\)\. Combined top 10 by excess: DS Q14 4322, DS Q23 2396, H Q17 1040, H Q13 1010, DS Q72 912,
    H Q18 690, DS Q39 672, DS Q78 589, DS Q47 577, DS Q67 570 ms \(Q67 was 1344, Q72 1975, H Q18 1240; DS Q64 left
    the top 10\)\. This census replaces census\-20261027 for ranking \(reusable until 2026\-10\-30\)\.
    - G3 \(instructions:u, `tmp/g3\-208/h13i\.data`, `h17i\.data`\): H Q13 is hash\-join memory behaviour —
      `mapaccess1\_fast64` in the probe 23\.8 % and `MaterializedSlot\.Get` 9\.7 % \(a one\-line slice read: first touch of
      a build row\); both are cache misses that instructions:u does not count, so no qualifying candidate under the
      bar's metric\. H Q17: numeric decode 13\.4 % \(NumericInt64FromStoredPayload 9\.7 %, the two legacy\-format
      probes 3\.7 %\) — the C20 ground, whose line profile put the removable part at 1–2 %; the rest is the lineitem scan
      \(decodeRowRangeInfo 12\.7 %, checksum 6 % after C19\)\. No candidate ≥ 3 % this pass; nothing attempted, miss
      counter stays 0\. Next on return: G3 on DS Q39 \(hash\-key evaluation per probe row, evalHashKeyDatumSlot\), DS Q78,
      DS Q47\.
  - **Return 2026\-10\-27 \(loop \#210\)**: S9 reused census ab \(`2910f8451` is analysis\-only\)\. G3 by instructions:u
    \(`tmp/g3\-210/q\{39,78,47\}i\.data`\): Q39 evalFastExpr 9\.7 % = filterOp\.nextPerRow 8\.4 % \+ hash\-probe keys
    \(evalHashKeyDatumSlot 7\.7 %: a column ref through evalFastExpr plus the slot read's first touch — no lever\); Q78
    flat \(decodeRowRangeInfo 7\.9 %\); Q47 is Q67's shape — string sort/merge comparisons\.
    - C30 \(G3 on Q47\): the Gather Merge / ordered\-merge comparator `mergeKeysLess` — the sibling of
      `sortOp\.lessKeyVals` it must agree with — still copies both Datums per key, tries fastCompareDatum \(which
      declines strings\), then `compareDatumTyped` → `textShapeAmbiguous` / `compareDatum`: 4\.6 % of Q47 under it\.
      C26 gave lessKeyVals in\-place access and a character\-string arm; the merge comparator never got them\. PG's
      Gather Merge heap compares with the same SortSupport comparators \(heap\_compare\_slots → ApplySortComparator\)\.
      Mechanism: mergeKeysLess reads through pointers and takes the character\-string arm, with key text flags computed
      once per merge \(sortKeyTextFlags\)\. Predicted 2\.5–3 %\.
    - **Target \(C30\)**: TPC\-DS SF0\.25 **Q47**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs ×
      2, fresh A/B against `2910f8451`\)\. Bar: −3 %\.
    - **C30 LANDED \(slice 110\)**: `mergeKeysLess\(keys, keyText, …\)` in place with the character\-string arm;
      gatherMergeOp\.keyText, setOp\.mergeTextFlags\(\)\. Q47 16\.993/16\.985 → 16\.130/16\.127 Ginstr/q \(**−5\.1 %, meets
      the bar**\); Q47/Q67/Q14/Q57 outputs byte\-identical; plans 99/99 same\. Misses 0\. Next on return: Q39's
      `filterOp\.nextPerRow` \(8\.4 %\) or Q23 / Q14 again\. Design: part 10 §110\.
  - **Return 2026\-10\-27 \(loop \#211\)**: S9 exhausted at `b996b60d9` \(census ac\)\. G3 on DS Q39
    \(`tmp/g3\-210/q39i\.data`\): big\-number numeric work is 13\.3 % — `exactIntVariance` 8\.2 % \(per\-group stddev\_samp
    finalisation; `nat\.sqrt` 3\.7 %, big\.Int allocations, `intSums` 1\.0 %\) and the filter's stdev/mean division 4\.8 %
    \(numericDiv already has a uint128 path\)\.
    - C31: for int2/int4 inputs PG accumulates Σx, Σx² in int128 \(Int128AggState\) and numeric\_stddev\_internal turns
      them into the variance and its square root; every intermediate of that computation for Q39's groups fits fixed
      width \(variance mantissa < 2^63, sqrt\_var's argument < 2^128\), yet goopg does it all in big\.Int — new big\.Ints
      per group and a Newton big\.Int square root\. Mechanism: `aggRuntime\.intVariance`, a 128\-bit fast path that
      mirrors exactIntVariance step for step \(select\_div\_scale, div\_var round, sqrt\_var's argument and round\_var\)
      with an integer square root seeded from float64, falling back to exactIntVariance on any bound\. Predicted ~6 %\.
    - **Target \(C31\)**: TPC\-DS SF0\.25 **Q39**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 5 runs ×
      2, fresh A/B against `b996b60d9`\)\. Bar: −3 %\.
    - **C31 LANDED \(slice 111\)**: `aggRuntime\.intVariance` / `intVarianceFixed` \(variance\_int128\_fast\.go\), used by
      all four integer variance finalisers; exactIntVariance is the fallback\. Q39 21\.717/21\.752 → 19\.308/19\.304
      Ginstr/q \(**−11\.2 %, meets the bar**\); Q17/Q35/Q39 outputs byte\-identical; plans 99/99 same\. Misses 0\. Next on
      return: Q39's filter division \(numericDivBig cases the uint128 path declines\) or Q23/Q14\. Design: part 10 §111\.
  - **Return 2026\-10\-28 \(loop \#212\)**: S9 exhausted at `3d50052e6` \(census ad\)\. G3 on DS Q23 at HEAD
    \(`tmp/g3\-212/q23i\.data`\): index scans 46 % / nbtree 31\.5 %, entered via indexScanOp\.nextLeafBatch 11\.1 % and
    indexOnlyScanOp\.Rescan 10\.9 % \(rangeScanPosLeaf 14\.8 %\) — descents and leaf walks already worked by C18/C28;
    PG 18's \_bt\_set\_startikey precheck applies only past a primitive scan's first page \(Q23's probes are one\-page
    rescans\), so no lever there\. H Q17 plan \(`tmp/g3\-212/q17ea\.sql`\): Hash Join probe = bare Seq Scan on lineitem,
    5 998 835 rows, 5 471 matches\.
    - C32 \(design\-sized; first slice qualifies per G4\): lazy probe\-side deform — the lineitem scan decodes
      l\_quantity and l\_extendedprice \(NUMERIC\) for every row although only 0\.09 % match the 182\-row hash table\.
      PG's slot deforms lazily: ExecHashGetHashValue's FETCHSOME reaches only the key attribute, HJ\_SCAN\_BUCKET
      fetches the rest for matching tuples \(slot\_getsomeattrs\_int, execTuples\.c\)\. Design:
      `docs/design/0100\-0149/0122\-0012\-c32\-lazy\-probe\-deform\.md` \(negotiation at join Open, scan deferNonKey /
      finishDeferred, join finish sites for every join type, debug poison of pending cells\)\. Predicted ~9 % on Q17
      \(two of three decoded NUMERICs: ~13 % of Q17 is numeric decode\)\. Not attempted this loop \(design only\);
      miss counter stays 0\.
    - **Target \(C32, first slice\)**: TPC\-H SF1 **Q17**, metric **instructions:u per query** \(`tools/h\-instr\.sh`, 5
      runs × 2, fresh A/B against the then\-HEAD\)\. Bar: −3 %\.
    - **C32 SLICE 1 LANDED \(loop \#213\)**: `probe\_defer\.go` — the first pass decodes up to the last probe key
      \(skipping earlier non\-key read columns\), the join finishes matched/emitted rows \(resume at the saved offset, or
      from column 0 when early columns were skipped\), through a narrowing Project; self\-disarm after 4096 deferred
      rows when ≥ half were finished\. Q17 27\.28–27\.31 → 24\.96 Ginstr/q \(**−8\.6 %, meets the bar**\); TPC\-DS Q8
      −32\.9 %, Q26 −21\.0 %, Q7 −17\.7 %, Q59 \+0\.3 %\. The first \(skip\-table, full re\-decode\) form regressed Q59
      \+13\.9 % and was reworked before landing\. Misses 0\. Design: `0122\-0012\-c32\-lazy\-probe\-deform\.md` §Landed\.
  - **Return 2026\-10\-28 \(loop \#214\)**: S9 exhausted at `e3a2535f0` \(census 20261028a\)\. Generate pass \(no attempt;
    misses stay 0\):
    - C32 coverage census \(debug build, top\-10 DS queries, `tmp/g3\-214/sz\-\*\.log`\): hash probes arm in Q14 40×,
      Q23 18×, Q78 11×, Q10 8×, Q39/Q47/Q67/Q64/Q72 4–6×; the declines are probes that are themselves joins, a Gather
      or a CTE scan, an expression key \(Q59\), or a non\-borrowing scan — slice 2 \(index/bitmap/Parallel Hash probes\)
      would arm almost nothing new in the top 10, so it is not the next lever\. Q14 at HEAD 128\.635 G \(C32 moved it
      −0\.5 %\)\.
    - C33 \(G3 on Q14\): index\-scan heap fetches decode every column below `deformBound` — Q14's store\_sales probe
      13 of 23 columns \(it reads ~4\), item 12 or 22 of 22; Q72's date\_dim 28/28, item 22/22, customer\_demographics
      9/9 — while seq scans skip unread columns inside the window \(slice 19, `colTypeInfo\.skip` from
      markProjectScanReads\)\. Decode under index scans is 6\.8 % of Q14's instructions \(2\.0 % of Q72's\)\. PG's
      projection fetches only the attributes its expressions reference \(EEOP\_SCAN\_FETCHSOME, by\-reference values
      left as pointers\); goopg's eager numeric/text conversion makes every decoded column cost\. Mechanism: thread a
      column read set beside `deformBound` through buildNode \(nested\-loop inner, Memoize, Project\) into indexScanOp's
      colInfo skip flags — the index\-scan twin of slice 19\. Predicted ~4 % on Q14; design\-sized \(read\-set threading
      through joins is pool item C14's "set\-valued read set"\)\.
    - **Target \(C33, first slice\)**: TPC\-DS SF0\.25 **Q14**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`,
      5 runs × 2, fresh A/B\)\. Bar: −3 %\. Design doc to write first:
      `docs/design/0100\-0149/0122\-0012\-c33\-index\-scan\-read\-set\.md`\.
  - **Loop \#215**: S9 reused census 20261028a \(`d1e6f0a79` is analysis\-only\)\. C33 design written:
    `docs/design/0100\-0149/0122\-0012\-c33\-index\-scan\-read\-set\.md` — the deform analysis threads only an integer
    bound \(`deformFoldRefs` = max ref \+ 1\) through buildNode / every deformBoundBelow arm, so a set cannot reach an
    index scan without a signature change; the design adds a separate Build\-time pass
    \(`indexScanReadSets`\) that walks the plan top\-down with a column set \(unknown nodes/expressions = all\), consumed
    by the two IndexScan build arms as `colInfo\.skip`, leaving every existing bound path untouched\. Implementation is
    the next loop's slice; nothing attempted, misses 0\.
  - **Loop \#216**: plumbing decided \(design §Plumbing\): `buildNode` has ~39 call sites, the slab builder
    \(`buildRec`\) and the worker builds \(`BuildWorker` / Gather `buildChild`\) would all need a new parameter, package
    state races between sessions, and stamping a cached plan from the executor races too — so the read set is stamped
    at `Plan\(\)`'s tail as `IndexScan\.ReadCols` by `fillIndexScanReadCols` \(precedent `fillJoinHashKeys`\), with a
    conservative optimizer expression walker checked against the executor's `deformScanReadSet`\. Nightly batch was
    running \(load ~11\); implementation deferred to a fresh loop\. Nothing attempted, misses 0\.
  - **C33 MEASURED AND DROPPED \(loop \#217\)**: implemented the slice the executor's existing consumer threading
    allows — `buildCompleteChild` gained a NestedLoopIndexJoin case \(consumers split at the outer width; inner read
    set = consumers' and predicate's inner columns ∪ Cond; outer consumers = their outer columns ∪ the probe's Key/
    Keys/SAOPKeys/Low/HighKey/RangePrefix ∪ Memoize keys; any unenumerable expression declines\), applied as
    indexScanOp `colInfo\.skip`; an on/off/poison equivalence test passed \(2 of 7 queries armed\)\. **Q14 128\.744 →
    128\.701 G = −0\.03 %, misses the bar**; Q72, Q17, Q25, Q29 ±0\. Cause: Q14's probes are the inner IndexScans of
    LATERAL / parameterised `Join`s \(lateralJoinStream\), not NestedLoopIndexJoins, and `joinLeftConsumers` declines
    lateral joins, so no consumer chain reaches them; two\-table index nested loops plan as `Join` too\. Code reverted
    \(correct but inert\)\. **Miss counter 1\.** The remaining lever is lateral\-join consumer threading: right read set
    from the consumers' right columns ∪ predicate ∪ Cond, and left consumers ∪ the inner's OuterColumnRef\{Level 1\}
    reads — needs an OuterColumnRef\-aware visitor \(deformLateralOuterRefs is the int\-bound twin\); design\-sized\.
  - **Loop \#218 \(misses 1\)**: S9 reused census 20261028a \(`b7befe4c0` ralph\-only\)\. Upper bound measured first with
    a throwaway by\-table skip hack \(`tmp/g3\-218/`, same binary on/off, Q14 output identical\): every unread
    index\-scan column skipped 129\.0 → 123\.95 Ginstr/q \(−3\.9 %\); item alone −2\.6 %, date\_dim −0\.9 %, the fact
    tables ±0; item's integer bound 12 alone ±0\. The item volume is the CTE's ~750 k `item\_pkey` probes under
    HashSetOp → Gather → lateral Nested Loop, which no consumer chain reaches \(SetOp, the Gather worker build and
    lateral joins all break it\)\.
    - C34: a Build\-time plan pass `indexReadSets` \(executor\) — the set\-valued twin of the deform walk over Project,
      Filter, Sort, Limit, Aggregate, Join \(lateral included: the left side adds the inner probe's
      OuterColumnRef\{Level 1\} reads\), Memoize, Gather/GatherMerge, SetOp and CTEScan inputs \(every column\),
      SubqueryScan/Materialize; unknown nodes stop the walk \(no entry = full decode\)\. Published per execution on
      `Context` \(copied into worker contexts\), applied in `indexScanOp\.openPrep` as `colInfo\.skip`\. PG:
      EEOP\_SCAN\_FETCHSOME fetches only referenced attributes \(execExpr\.c ExecCreateExprSetupSteps\)\.
    - **Target \(C34\)**: TPC\-DS SF0\.25 **Q14**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 3 runs ×
      2, fresh A/B\)\. Bar: −3 %; predicted −3\.5 % \(hack upper bound −3\.9 %\)\.
    - **C34 LANDED \(loop \#218\)**: `index\_read\_sets\.go` — `computeIndexReadSets` runs in `OpIterator\.Open`,
      published as `Context\.indexReadSets` \(restored at Close; copied by `NewWorkerContext`\), applied by
      `indexScanOp\.openPrep` \(`applyIndexReadSet`\); `exprReadSet` accepts uncorrelated sublinks \(slice 53 rule\)\.
      Q14 129\.443/129\.067 → 123\.901/123\.909 Ginstr/q \(**−4\.1 %, meets the bar**\); Q23 −0\.8 %, Q72 −0\.3 %;
      Q14 output byte\-identical; plans 99/99 same\. Test `TestIndexReadSetsMatchFullDecode` \(on/off/poison, 13
      queries, 11 arm\)\. **Misses 0\.** Design: `0122\-0012\-c33\-index\-scan\-read\-set\.md` §"Landed: C34"\. Next on
      return: re\-census S9 \(executor changed\), then a fresh G3 on the next top\-10 query \(Q23, H Q17/Q13, Q72\)\.
  - **Return 2026\-10\-28 \(loop \#219, misses 0\)**: S9 re\-census `routing\-20261028b` exhausted\. Fresh G3
    \(instructions:u, `tmp/g3\-219/`\) on DS Q23 and H Q17 at `39041efc5`\.
    - Q23 is spread thin\. The IOS customer\_pkey probes are 16\.4 % cumulative but are PG\-equivalent descents;
      the goopg\-only leftovers \(`indexKeyIsDecodable`, growslice, LookupColumn/EqualFold\) total ~1\.5 %\.
    - Q17: decodeRowRangeInfo 15\.3 % self, skipAttrLen 3\.7 %, decodeScanRowRangeInfo 4\.9 %\. The probe's first
      pass walks ~7 skipped columns per lineitem row to reach l\_partkey \(HammerDB column order\)\. Each skipped
      column pays the bitmap/natts/info loads, a non\-inlined skipAttrLen call, a 48\-byte NullDatum copy from the
      global and the poison\-flag test; PG's slot\_deform\_heap\_tuple step is att\_align\_pointer \+
      att\_addlength\_pointer\.
    - C35: a skip\-run loop — on reaching a skipped column, walk the whole run of consecutive skipped columns with
      a precomputed per\-column skip length \(fixed width, or varlena header inline\), then clear the run's cells in
      one bulk store \(NullDatum is the zero Datum\)\. Not slice 72's attcacheoff or conditional NULL store \(both on
      the Below\-bar list\)\.
    - **Target \(C35\)**: TPC\-H SF1 **Q17**, metric **instructions:u per query** \(`tools/h\-instr\.sh`, 3 runs × 2,
      fresh A/B\)\. Bar: −3 %; predicted ~4 %\.
    - **C35 LANDED \(slice 112\)**: `skipColumnRun` \+ `colTypeInfo\.skipLen` \(coltypeinfo\.go\), called from
      decodeRowRangeInfo's loop; the run's cells are cleared at once \(`clear`\)\. Q17 24\.973/24\.967 →
      22\.604/22\.605 Ginstr/q \(**−9\.5 %, meets the bar**\); DS Q78 −1\.3 %, Q7 −0\.2 %; sf025 total −4\.7 %; values
      and plans identical\. Test `TestSkipColumnRunMatchesPerColumnSkip` \(4 000 random tuples, run vs per\-column\)\.
      **Misses 0\.** Design: part 10 §112\. Next on return: re\-census S9 \(executor changed\), then G3 on H Q13
      \(mapaccess1\_fast64 is stall time — check instructions first\) or DS Q72/Q18\.
  - **Return 2026\-10\-28 \(loop \#220, misses 0\)**: S9 re\-census `routing\-20261028c` exhausted\. G3 at
    `b68540921` \(`tmp/g3\-220/`\):
    - H Q18: map lookups 11 % \(stall skid\) and NUMERIC decode ~10 %; the legacy\-text / special\-spelling probes are
      already early\-reject, their share is call overhead plus the payload's first touch \(C20's ≈1–2 % again\)\.
    - H Q13: `MaterializedSlot\.Get` 9 % and `mapaccess1\_fast64` 25 % are the first touch of build rows after the
      hash lookup — stall time, not instructions\. No instruction lever\.
    - DS Q72: skip scan dominates — `btreeSkipEnum\.walkLeaf` ~43 % \(leaf entries\), `cursorAt` descents ~35 %\.
    - C36: walkLeaf checks every leaf entry before the current group's probe\. PG 18 does that only three times
      in a row, then `\_bt\_checkkeys\_look\_ahead` \(nbtutils\.c\) tests a tuple `targetdistance` ahead \(5, doubling
      to MaxIndexTuplesPerPage/2; on a miss rechecks reset and the distance drops to /8\), and `\_bt\_readpage`
      jumps there via `pstate\.skip`\. The state resets per page and on each array advance\. Port: a ScanCursor
      skip slot \(`scanLeafItems` honours it\) and the look\-ahead in walkLeaf's before\-probe branch\.
    - **Target \(C36\)**: TPC\-DS SF0\.25 **Q72**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 3 runs ×
      2, fresh A/B\)\. Bar: −3 %; predicted ~10 % \(walkLeaf ~43 %\)\.
    - **C36 LANDED \(slice 113\)**: `skipLookAhead` in walkLeaf's before\-probe branch \+ `ScanCursor\.SkipTo` /
      `LeafKeyAt` / `LeafItemCount` \(scanLeafItems honours the skip only without a hi bound\)\. Q72 23\.955/23\.954 →
      23\.157/23\.154 Ginstr/q \(**−3\.3 %, meets the bar**; predicted ~10 % — most walked entries are matches or
      group boundaries\); Q72 output identical; plans 99/99 same; sf025 total −3\.3 %\. Test
      `TestSkipLookAheadMatchesEntryWalk` \(mutation\-checked\)\. **Misses 0\.** Design: part 10 §113\. Next on
      return: re\-census S9, then Q72's descents \(`cursorAt` ~35 %: per\-descent page pin \+ bufmap lookup \+
      readOpaque\) or a fresh G1 census \(census\-20261027b expires 2026\-10\-30\)\.
  - **Return 2026\-10\-28 \(loop \#221, misses 0\)**: S9 re\-census `routing\-20261028d` exhausted\. G3 on Q72 at
    `4321d5793` \(`tmp/g3\-221/`\): descents \(`ResetScanCursor`\) 36\.6 %, nearly all the internal\-page binary
    search\. Across the query the key\-compare chain is ~21 % — compareFixedPrefix 9\.7, comparePGIndexTupleAttrsAt
    5\.5, PGCompareInt4 3\.3, compareAttr 2\.8 \(loop \#220 profile\)\. Each fixed\-width attribute goes through the
    by\-value `PGKeyAttr\.compareAttr`, slice construction and an indirect `Compare` call\. PG's `\_bt\_compare` calls
    the opclass proc too, but its int4/int8/date keys reach btint4cmp/btint8cmp with by\-value Datums
    \(`index\_getattr` attcacheoff, `FunctionCall2Coll`\)\.
    - C37: `PGKeyAttr\.IntWidth` \(2/4/8 for the signed\-integer comparators — int2, int4/date, int8/time/timestamp/
      timestamptz — stamped by the executor's descriptor builder\); `compareFixedPrefix` compares such attributes
      inline from the page bytes \(Desc negated\), no compareAttr / indirect call\.
    - **Target \(C37\)**: TPC\-DS SF0\.25 **Q72**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 3 runs ×
      2, fresh A/B\)\. Bar: −3 %; predicted ~4 %\.
    - **C37 LANDED \(slice 114\)**: `PGKeyAttr\.IntWidth` \(stamped by `pgIndexIntWidthForOID`\) and the inline
      signed\-integer arm in `compareFixedPrefix`\. Q72 23\.157/23\.156 → 21\.229/21\.231 Ginstr/q \(**−8\.3 %, meets the
      bar**\); Q23 −2\.3 %; Q72 output identical; plans 99/99 same\. Tests `TestCompareFixedIntMatchesComparator`
      \(mutation\-checked\), `TestPGIndexIntWidthMatchesComparator`\. **Misses 0\.** Design: part 10 §114\. Isolation
      suite needs `\-timeout 30m` \(533–587 s against the 10\-minute default\)\. Next on return: re\-census S9, then a
      fresh G1 refresh \(census\-20261027b expires 2026\-10\-30; release\-PG numbers are reusable, goopg's moved\)\.
  - **Return 2026\-10\-28 \(loop \#222, misses 0\)**: S9 re\-census `routing\-20261028e` exhausted\. **G1 refresh**
    committed as `analysis/m0122/m0122\-0012/census\-20261028/` \(HEAD `f645129a8`, release\-PG numbers reused\); new
    top 10: DS Q14 3205, DS Q23 2631, H Q18 1050 \(host variance — instructions fell 41\.912 → 40\.760 G since
    census\-b\), H Q13 950, H Q17 900, DS Q72 796, **DS Q10 543 \(ratio 4\.05\)**, DS Q78 539, DS Q39 502, DS Q59 462 ms\.
    - G3 on DS Q10 \(`tmp/g3\-222/q10i\*`\): decodeRowRangeInfo 21\.6 % self \(32 % cum\), seqScanOp\.Next 13 %\. The
      plan is Nested Loop \(Join Filter `cd\_demo\_sk = c\.c\_current\_cdemo\_sk`\) with outer Seq Scan on
      customer\_demographics \(1\.92 M rows\) and a Materialize of ~3 inner rows\. goopg decodes \(and, the outer
      scan not borrowing under Sort, clones\) all nine columns of every outer row; PG's join filter fetches only
      attribute 1 \(EEOP\_OUTER\_FETCHSOME\) and the projection the rest for the few matching rows\.
    - C38: C32's lazy probe deform for the streaming nested loop's outer side — arm the outer seq scan \(directly
      or under a bare\-reference projection\) with the outer columns the join predicate reads; the stream finishes
      the row \(and re\-copies its cells into `outerRow`\) before any use beyond the predicate: an emitted pair, a
      semi/anti emit, a null\-extended outer row\. Works for non\-borrowing scans because the page stays pinned
      until the scan advances and `finishDeferred` re\-takes the content lock\.
    - **Target \(C38\)**: TPC\-DS SF0\.25 **Q10**, metric **instructions:u per query** \(`tools/ds\-instr\.sh`, 3 runs ×
      2, fresh A/B\)\. Bar: −3 %; predicted ~20 %\.
    - **C38 LANDED \(slice 115\)**: `nlJoinStream\.armOuterDefer` / `finishOuterRow` \+ `seqScanOp\.armDefer\(keyCols,
      requireBorrow\)`\. Q10 10\.255/10\.277 → 6\.889/6\.899 Ginstr/q \(**−32\.9 %, meets the bar**\); Q35 ±0 \(disarm path
      free\); outputs of Q10/Q35/Q69/Q13/Q48 identical; plans 99/99 same\. A first gate sweep's \+13\.2 % \(Q78, Q26,
      Q23\) was host contention: equal instructions, and the next sweep came back −15\.0 %\. Test
      `TestNLOuterDeferMatchesEager` \(mutation\-checked\)\. **Misses 0\.** Design: part 11 §115\. Next on return:
      re\-census S9, then G3 on the census\-20261028 top 10 \(Q14, Q23, H Q13/Q17, Q78, Q39, Q59\)\.
  - **G3 pass 2026\-10\-28 \(loop \#223, misses 0\)**: S9 re\-census `routing\-20261028f` exhausted\. Profiled DS
    Q78, Q39, Q59, H Q17 and DS Q23 at `821d93323` \(`tmp/g3\-223/`, instructions:u\)\. **No candidate predicted ≥ 3 %**,
    so nothing was attempted \(not a miss\)\. Findings:
    - Q78 and Q39 are spread thin \(no symbol above 6 %; evalFastExpr, seqScanOp\.Next, the hash probe and the
      Gather queue copy are each 3–10 %\)\.
    - Q59: the seven `CASE WHEN d\_day\_name = '<day>'` arms are 17\.9 % of the query, but slice 89's in\-place string
      path already serves them\. What remains per arm is two `plainCompareString` scans, two `bcTrimRight`s and a
      three\-way `strings\.Compare` for `=`: ~4 % in total, about half of it saveable\.
    - H Q17: `seqScanOp\.Next`'s hottest lines are the first touch of each tuple header \(stall skid on the
      visibility call's argument setup\) and the `needsDetoastPrefix` re\-scan of decoded cells \(~2 % of Q17\)\.
    - DS Q23: index probes are 44\.7 % \(item\_pkey ~1\.15 M probes, customer\_pkey IOS\), spread over PG\-equivalent
      descent work; the aggregate's string\-serialised group keys \(`mapaccess2\_faststr`, memmove, aeshash,
      `formatBase10`\) ~4\.5 %, mostly hash/stall work a typed key would still pay\.
    - Added to the pool \(each predicted < 3 % on its query\):
      - C39: the decoder reports toast pointers so the per\-row `needsDetoastPrefix` re\-scan disappears \(H Q17
        ~2 %\);
      - C40: `=`/`<>` on two plain strings as length\-then\-bytes equality, with the constant side's
        plain/trimmed form precomputed at compile time \(Q59 ~2 %\);
      - C41: typed composite hash\-aggregate keys instead of serialised strings, PG's TupleHashTable
        \(execGrouping\.c\) \(Q23 ~2 %\)\.
    - Next pass: DS Q14 after C34 \(its lateral probes\), H Q13 by cycles \(stall\-bound; instructions do not see
      it\), DS Q72 descents\.
  - **Return 2026\-10\-27 \(loop \#187\)**: G1/G2 census committed at `analysis/m0122/m0122\-0012/census\-20261027/`
    \(combined top 10: DS Q14, Q23, Q72, H Q17, DS Q67, H Q18, H Q13, DS Q39, DS Q64, DS Q78\)\. Took C16a\.
    - **Target \(C16a\)**: TPC\-DS SF0\.25 **Q64**, metric **instructions:u per cache\-busted plan**
      \(`tools/ds\-planinstr\.sh`, plan\-only EXPLAIN with a per\-run work\_mem change; planning is 636 of Q64's
      1019 ms sweep time, and repeated runs of identical text skip planning via the plan cache, so whole\-query
      instructions would not see it\)\. Bar: −3 %\.
    - **C16a LANDED \(slice 93\)**: `searchCtx` memoises `relNarrowedColumns` per member rel and `attrNeededAbove`
      per relids \(reset on a clause\-list change\)\. Q64 6\.496/6\.490 → 6\.061/6\.052 Ginstr/plan \(**−6\.7 %, meets
      the bar**\); sweep Q64 971 ms vs census median 1019 ms; plans identical \(sf025 99/99 same, fire\-set no
      fires, TPC\-H census 22/22\)\. Miss counter 0\. Next on return: C16b \(cache inner\-unique proofs,
      innerrel\_is\_unique's unique\_for\_rels\), same Target query; re\-profile first\. Design:
      part 9 §Slice 93\.
    - **C16b LANDED \(slice 94, loop \#188\)**: `searchCtx.innerUniqueMemo` memoises innerRelProvenUnique per
      \(outer relids, inner relids, skipNonKeys, key set\), hits verified element by element\. Q64 6\.052/6\.055 →
      5\.678/5\.679 Ginstr/plan \(**−6\.2 %, meets the bar**; −12\.6 % over C16a\+b\); sweep Q64 944 ms; plans
      identical\. Misses 0\. Next on return: C16c — pathkey usefulness \(14\.8 % after C16b:
      equivalentWithin/countContainedIn/keyOf; PG compares canonical pathkeys by pointer\), same Target; overlaps
      C15\. Design: part 9 §Slice 94\.
    - **C16c LANDED \(slice 95, loop \#189\)**: `pathkeyUsefulnessFor` memoised per relids, and `freshEvalCtx` now
      hands every GEQO tour the parent's search memos \(it built a new searchCtx per tour, so the slice 93/94 memos
      had been per\-tour only — their measured gains came from within\-tour repeats\)\. Q64 5\.683/5\.678 →
      5\.213/5\.204 Ginstr/plan \(**−8\.3 %, meets the bar**; −19\.9 % over 93–95\); sweep Q64 860 ms; plans
      identical\. Misses 0\. Next on return: re\-profile Q64 \(per\-path truncateUselessPathkeys, addPathsForJointype
      allocation\)\. Design: part 9 §Slice 95\.
    - **C16d LANDED \(slice 96, loop \#190\)**: a join rel's ColVarBytes union is formed on first read
      \(`colVarFrom`/`colVarBytes\(\)`\) instead of per joinrel per tour \(unionColVarBytes 8\.5 %\)\. Q64 5\.214/5\.201
      → 4\.853/4\.858 Ginstr/plan \(**−6\.8 %, meets the bar**; −25\.2 % over 93–96\); plans identical\. Misses 0\.
      Next on return: truncateUselessPathkeys per path \(16 %\) or comparePathkeysDimRel's unmemoised pathKeyEqual
      \(7 %; PG compare\_pathkeys is a pointer compare\)\. Design: part 9 §Slice 96\.
    - **C16e LANDED \(slice 97, loop \#191\)**: comparePathkeysDimRel compares keys through the rel's identity\-key
      memo \(usefulKeys\.pathKeyEqual\) instead of re\-formatting both keys per comparison\. Q64 4\.852/4\.854 →
      4\.626/4\.618 Ginstr/plan \(**−4\.8 %, meets the bar**; −28\.8 % over 93–97\); plans identical\. Misses 0\.
      Next on return: re\-profile; truncateUselessPathkeys per path \(16 %\), buildPathkeyUsefulness misses \(7 %\)\.
      Design: part 9 §Slice 97\.
    - **C16f LANDED \(slice 98, loop \#192\)**: truncateUselessPathkeys memoises the kept length per key list
      \(u\.truncMemo, verified key by key, always slicing the caller's list\)\. Q64 4\.611/4\.615 → 3\.229/3\.222
      Ginstr/plan \(**−30\.1 %, meets the bar**; −50\.3 % over 93–98\); sweep Q64 728 ms \(census 1019, PG 173\);
      EXPLAIN with costs byte\-identical on Q64/Q72/Q14/Q39/Q17/Q25/Q29\. Misses 0\. Next on return: re\-profile Q64;
      planning now ~3\.2 G vs PG's 52 ms plan — measure planning ms again \(g1\-split\.sh\) before the next step\.
      Design: part 9 §Slice 98\.
    - **C16g LANDED BELOW THE BAR \(slice 99, loop \#193\)**: the truncation memo keyed by list CONTENTS \(first key
      \+ length, verified with slices\.Equal\) so sort\_inner\_and\_outer's fresh lists hit\. Q64 3\.227/3\.228 →
      3\.135/3\.129 Ginstr/plan = **−2\.96 %, misses the 3 % bar** \(spread ±0\.005 G, not noise\); plans identical,
      all gates green; landed as plan\-neutral and added to the Below\-bar list\. **Miss counter 1\.** Q64 planning
      488 ms by EXPLAIN SUMMARY at slice 98 \(PG 52\)\. Profile after slice 98: sortInnerAndOuter 16 %
      \(tryMergeJoinPath 15\.7 %: buildJoinPathkeysFor 44 % of it, sortPathFor 18 %\), mallocgc 29 %,
      addNestLoopPathFor 9\.4 %, buildPathkeyUsefulness 7\.9 %\. Design: part 9 §Slice 99\.
    - **C16h MEASURED AND DROPPED \(loop \#194\)**: price a merge candidate's sorted inputs \(cost\_sort figure\)
      for initial\_cost\_mergejoin \+ add\_path\_precheck and build the Sort paths only afterwards, join pathkeys
      formed once\. Q64 3\.126/3\.132 → 3\.114/3\.111 Ginstr/plan = −0\.5 % \(precheck rarely rejects on Q64\)\.
      **Second consecutive miss → one\-task yield** \(Yield record\)\. Profile at slice 99
      \(`tmp/q64plan\-c16g\.pprof`\): truncateUselessLen still 4\.3 % \(misses follow per\-relids usefulness
      misses\), addNestLoopPathFor 10\.3 %, tryMergeJoinPath 13\.9 %\.
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
  - **NewBoolDatum result table LANDED 2026\-10\-16 \(loop \#39\)**.
    Movement: none — perf only. The S3 instrument is value/plan identity:
    sf025 `PASS=99 PLAN-SHAPE same=99`, fire\-set `fires=none`, ea\-ratchet
    PASS. Same design doc, "Slice 5".
    - `NewBoolDatum` copies `boolDatums[b]` instead of building, which avoids
      the byte\-wide `Kind` store the caller's reload stalls on. Pinned by
      `TestNewBoolDatumTable`.
    - Measured: the SF1 filtered scan improved ~5% \(~1150 → ~1095 ms\).
      TPC\-H arm: values identical, 36.56 s → 35.93 s.
    - Ruled out: an `ExprIntConst` precomputed Datum measured neutral to
      worse and was not kept.
    - Next lever: the recursive `\(Datum, error\)` return in `evalFastExpr`
      itself, which needs an in\-place evaluator \(structural\).
  - **Operand leaf copy measured 2026\-10\-16 \(loop \#40\) — NOT landed.**
    Movement: none — measured non\-improvement. Same design doc, "Slice 6".
    - Two variants copy a column/`ExprConstVal` operand straight into the
      BinaryOp arm's local. The wrapper variant was −2.5% on store\_sales but
      slower on TPC\-H Q6. The leaf\-only variant was inside the noise on both.
    - Reading: the remaining `evalFastExpr` cost is tree\-walk control flow
      \(per\-node dispatch, recursion\), not Datum copies. The next slice is
      batch evaluation in the scan \(the SeqScanOp entry\), keeping the
      `currentTID` read\-ahead rule.
  - **Batch qual evaluator LANDED 2026\-10\-16 \(loop \#42\), no caller yet.**
    Movement: none — infrastructure; no production path changes \(plan text
    identical\). Design: `docs/design/0100-0149/0122-0012b-scan-batch-qual.md`.
    - Parked one loop as a pending patch. The cause was misread as the
      fire\-set SF1 lane being blocked: `internal/executor/` is outside the
      fire\-set scope, and the real rejection was a code\_tree mismatch after
      peer commit a181f6bfd landed mid\-gate. Gates were re\-run on the
      combined tree.
    - `evalFastExprBatch` \(exprnode\_batch.go\) is total over the slab: leaves
      and BinaryOp are specialised, AND/OR short\-circuit by sub\-selection,
      every other kind goes per row. On any error the caller re\-runs per row.
      Pinned by `TestEvalFastExprBatchMatchesPerRow`; two mutations fail it.
    - The BinaryOp work is shared via `evalFastBinaryApply`. Calling it per row
      cost \+26% by value and \+14% by pointer. With the inlined common case
      \(`fastBinarySimple` \+ `fastIntOverflow`\) the cost is \+1.5–2.5% in
      the microbenchmark.
    - Microbenchmark \(store\_sales qual, 64 rows\): per\-row 11.6 µs, batch 8.4
      µs \(−28%\). The end\-to\-end ceiling is ~12% of the filtered scan.
    - Next: wire it into seqScanOp \(design §4: page\-mode phases,
      admission/exclusions, currentTID / borrowRows invariants, isolation
      family\).
  - **Slice 8 — a pure verdict pre-pass in seqScanOp — measured SLOWER, not landed \(2026\-10\-16, loop \#45\).**
    Movement: none — measured non\-improvement. Design `0122\-0012b\-scan\-batch\-qual.md` §5.
    - A window of up to 64 slots is fetched and prefix\-deformed under the held page RLock, batch\-evaluated, and one
      verdict cached per slot. The loop's visibility, SSI, hint\-bit and `currentTID` order are unchanged. The values
      pin passed; inverting the verdicts failed 6 subtests.
    - SF1 store\_sales: per\-row ~1110 ms; window 64 ~1145 ms, 32 ~1190, 16 ~1233, 8 ~1314. Expression evaluation
      fell 43% → 35%, but the duplicated tuple fetch and copy \(PageGetHeapTupleInto 2.9 → 6.2%\) and the window
      overhead \(5.9%\) paid it back.
    - A win needs a single\-pass restructure \(fetch and deform once, zero\-copy tuple view\), with a ~8% ceiling.
      Not pursued; the evaluator stays as infrastructure.
  - **Slice 9 — a constant\-divisor arm in `NumericInt64FromStoredPayload` — measured inside the noise, not landed
    \(2026\-10\-16, loop \#46\).** Movement: none — measured non\-improvement. Recon doc "Slice 9".
    - The 128/64 DIVQ held ~20% of the function's samples. Dividing by a constant when the high word is zero
      measured ~1180 ms against HEAD's ~1180 ms over six rounds: the latency was already hidden.
    - **Scan\-qual micro\-optimization is exhausted** \(slices 6, 8, 9: three consecutive non\-improvements\). The next
      M0122\-0012 slice must profile a different workload \(hash join / aggregation, TPC\-H Q1/Q9\) before changing
      code.
  - **Slice 10 — numeric promotion passes numeric operands through LANDED 2026\-10\-16 \(loop \#47\).**
    Movement: none — perf only. The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99
    PLAN\-SHAPE same=99, TestPort\_RegressSuite PASS. Recon doc "Slice 10".
    - TPC\-H Q1 profile \(goopg ~1.91 s vs PG ~1.23 s\): evalFastExpr 17%, applyAgg 9.9%, evalBinary 9.7%,
      promoteToNumeric 6.1%.
    - `promoteNumericOperands` promotes only the non\-numeric side. Q1 measured 2.5–4% faster in each of three rounds.
      Pinned by `TestPromoteNumericOperandsMatchesPromoteToNumeric`.
    - Remaining Q1 levers: the per\-row `strings.ToLower` in applyAgg \(~1%\); the per\-row string group key and
      interpreted group\-key evaluation \(~5%\); the int literal's per\-row promotion \(PG coerces it at parse time\).
  - **Slice 11 — the hashed aggregate's group key built in place LANDED 2026\-10\-16 \(loop \#48\).**
    Movement: none — perf only. The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99
    PLAN\-SHAPE same=99, TestPort\_RegressSuite PASS. Recon doc "Slice 11".
    - `appendGroupKey` / `appendDatumKey` build `setGroupKey`'s exact bytes into a reused buffer. The lookup is
      `groups\[string\(buf\)\]`, allocating only for a new group. Pinned by `TestAppendGroupKeyMatchesSetGroupKey`
      \(mutation\-checked\).
    - TPC\-H Q1 went from ~1875 ms to ~1645 ms \(−12%\). Acceptance arm: values identical, total 40.55 s → 37.94 s.
    - Next Q1 levers: applyAgg's per\-row `strings.ToLower`, interpreted group\-key evaluation, and the int literal
      promoted per row.
  - **Slice 12 — applyAgg's cached names and in\-place numeric sum LANDED 2026\-10\-16 \(loop \#49\).** Movement: none
    — perf only. The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS. Recon doc "Slice 12".
    - `aggLowerName` caches each lower\-cased aggregate name. The same\-scale int64 lane of the running numeric sum
      is updated in place \(exactly numericAdd's result\). Pinned by `TestAggNumericSumInPlaceMatchesExact`, which
      checks exact sums incl. overflow into the big lane and is mutation\-checked.
    - Q1 went from ~1660 ms to ~1555 ms \(−6%\); across slices 10\-12, ~1.91 s → ~1.55 s \(PG ~1.23 s\).
  - **Slice 13 — the seq scan reads tuples in place LANDED 2026\-10\-16 \(loop \#50\).** Movement: none — perf only.
    The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS. Recon doc "Slice 13".
    - `seqScanOp.Next` uses `PageGetHeapTupleNoCopy` \(PG's in\-place read under the pin\) instead of copying each
      tuple into a scratch buffer, which was 6.5% memmove on Q1. Every Data/Bitmap use precedes the tuple's RUnlock.
    - Q1 median went from ~1563 ms to ~1505 ms \(about −3.5%\).
  - **Slice 14 — the numeric fast paths test the big\-lane flag LANDED 2026\-10\-16 \(loop \#51\).** Movement: none
    — perf only. The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS. Recon doc "Slice 14".
    - numericAdd/Sub/Mul/Div/Cmp, numericMant and datumToInt64Key test `isBigNumeric` \(an inlined flag test\)
      instead of calling the non\-inlinable `NumericBigValue` for its nil result. That is exact, because
      NumericBigValue is nil iff the flag is clear. Pinned by `TestIsBigNumericMatchesNumericBigValue`.
    - Q1 median went from ~1490 ms to ~1455 ms \(about −2.4%\). Next levers: the int literal promoted per row, and
      the stack\-returned arithmetic Datums.
  - **Slice 15 — evalBinary promotes int operands in place LANDED 2026\-10\-16 \(loop \#52\).** Movement: none — perf
    only. The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS. Recon doc "Slice 15".
    - evalBinary's two numeric arms rewrite a KindInt operand as `numericFromInt` in place, via the inlined
      `promoteIntOperand`. They call `promoteNumericOperands` only when an operand is still not numeric, so the
      numeric/int\-literal case no longer returns a Datum pair through the stack. Pinned by
      `TestEvalBinaryNumericArmsMatchPromoteToNumeric`, which covers all five operators and all operand pairings
      and is mutation\-checked.
    - Q1 median went from ~1460 ms to ~1310 ms \(about −10%\), with no overlap between arms; across slices 10\-15,
      ~1.91 s → ~1.31 s \(PG ~1.23 s\).
  - **Slice 16 — applyAgg takes its AggregateCall by pointer LANDED 2026\-10\-16 \(loop \#53\).** Movement: none —
    perf only. The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS. Recon doc "Slice 16".
    - `optimizer.AggregateCall` is 320 bytes. The three per\-row aggregate loops \(the hashed drain, openSorted and
      openSortedPartialEmit\) copied it in their `range` and again as applyAgg's by\-value argument: two copies per
      aggregate per row. They now pass `&o.plan.Aggs[i]`. applyAgg only reads it, so the behaviour is unchanged by
      construction; the existing applyAgg tests and the window callers were updated.
    - Q1 median went from ~1290 ms to ~1225 ms \(about −5%\); across slices 10\-16, ~1.91 s → ~1.22 s, level with
      PG's ~1.23 s.
  - **Slice 17 — Projects fold their targets; lowered sublinks fold their Args LANDED 2026\-10\-16 \(loop \#54\).**
    Movement: none — perf only. The S3 instrument is value/plan identity: acceptance arm PASS, sf025 PASS=99
    PLAN\-SHAPE same=99, TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS. Recon doc "Slice 17"; EX1\-02 design
    amended.
    - After Q1 reached parity, the per\-query census against PG :65432 put TPC\-H Q17 furthest behind: 6.19 s vs
      1.53 s, with an identical plan. Its lineitem probe scan deformed and cloned all 16 columns per row, because
      the deform\-bound walk reset to full at a reordered Project \(HammerDB's column order\) and at the join filter's
      correlated SubPlan.
    - Every non\-identity Project now folds its targets alone, which is PG's ExecBuildProjectionInfo `last_scan`
      rule. A param\-lowered SubqueryExpr folds its Args in both walkers. Pinned by
      `TestScanDeformProjectThrough` and `TestScanDeformLoweredSublinkArgs`, both mutation\-checked.
    - Q17 warm median went from ~6400 ms to ~4400 ms \(−31%\); PG is ~1.53 s. Remaining Q17 cost: projectOp.Next
      and cloneRowOwnedPrefix \(~8% each\) on the probe side, plus page reads.
  - **Slice 18 — the scan borrow reaches through Projects and hash\-join probes LANDED 2026\-10\-21.** Movement: none —
    perf only. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE
    same=99, TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS. Recon doc "Slice 18".
    - cloneRowOwnedPrefix was 22.6% of warm Q17, all under the lineitem probe: the M0145\-0008u borrow reached only a
      scan directly under a borrowing aggregate, and Q17 is Aggregate → Project → Hash Join → Project → Seq Scan.
    - markScanBorrowChain descends through Projects and the probe side of non\-lateral inner/left/semi/anti hash joins
      \(PG's slot discipline: valid until the next fetch\); anything else stops it\. Pin:
      TestScanBorrowThroughHashJoinProbe \(probe borrows, build never; poison armed; equals the cloning path\)\.
    - Q17 warm median ~4.29 s → ~3.42 s \(−20%, no overlap, 3 interleaved rounds\); PG ~1.53 s. Next lever: deform
      \(decodeRowRangeInfo 27% cum\)\.
  - **Slice 19 — a scan under a Project walks past the columns nobody reads LANDED 2026\-10\-21.** Movement: none —
    perf only. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE
    same=99, TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS. Recon doc "Slice 19".
    - Q17's probe Project reads 3 of the 8 deformed lineitem columns; 5 were fully decoded \(numerics to int64, a date,
      a char\) for nothing\. PG walks those attributes too but leaves by\-reference values as pointers\.
    - markProjectScanReads \(both builders\) records the targets\' read set on a seq scan directly under a non\-identity
      Project; Open marks the rest colTypeInfo\.skip \(no absorbed qual, no SSI hook\); skipAttrLen steps a varlena by
      its header and a by\-value fixed type by its width, else decodes\.
    - Pins: TestScanSkipMatchesFullDecode \(18 types, poison armed; fails with varlena sizes off by one\),
      TestPhysicalPGVarlenaSize\.
    - Q17 warm median ~3.5 s → ~2.95 s \(−16%, no overlap\); slices 17\-19 ~6.4 s → ~2.95 s; PG ~1.53 s\.
  - **Slice 20 — the scan borrow reaches through right and full hash joins LANDED 2026\-10\-21.** Movement: none —
    perf only. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE
    same=99, TestPort\_RegressSuite PASS. Recon doc "Slice 20".
    - Warm per\-query census after slice 19 \(goopg / PG, s\): Q13 3\.73/1\.50, Q12 1\.62/0\.47, Q18 4\.37/3\.39,
      Q21 1\.35/0\.53, Q17 2\.49/1\.73, Q10 1\.29/0\.62; every other query within 0\.3 s\. Q13 was the largest gap\.
    - Q13's orders probe under its Hash Right Join cloned all 1\.5M rows \(cloneRowOwned 27\.6% cum, span clearing 9%\):
      joinBorrowProbeChild stopped at RIGHT/FULL\. It now admits every non\-lateral hash join type, probing the side
      hashBuildIsLeft does not build\. Pin: TestScanBorrowThroughOuterHashJoinProbe \(RIGHT as planned, FULL retyped —
      goopg never plans a FULL hash join, ledger M0134\-0037\)\.
    - Q13 median ~4\.71 s → ~3\.43 s \(−27%, no overlap, 3 interleaved rounds\)\. Next lever: the numeric join key is
      hashed as a canonical string \(mapaccess\*\_faststr ~21%\)\.
  - **Slice 21 — a value\-list IN is judged early, and a literal list is evaluated once LANDED 2026\-10\-21.**
    Movement: none — perf only. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025
    PASS=99 PLAN\-SHAPE same=99, TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS. Doc
    0122\-0012\-tpch\-scan\-pipeline\-slices\.md "Slice 21".
    - Q12's lineitem qual `l\_shipmode = ANY \('\{MAIL,SHIP\}'::bpchar\[\]\)` was judged LATE: PlanScanQual declined every
      InExpr, so all 6M rows were decoded and cloned \(cloneRowOwned 23%\) before ~99% were rejected\. A value\-list
      InExpr \(no Plan/Subquery/Args\) is now early\-eligible; its walked children bound the columns\.
    - collectInValues re\-evaluated and re\-parsed the literal list per row \(10%\)\. inListItems keeps a literal list's
      items \+ bpchar metadata per statement in Context\.inListConsts \(PG folds it to a Const array\)\.
    - Pin: TestScanQualValueListIN \(7 IN quals vs their OR spelling, early on/off; fails caching a column\-item list
      and with the blanket decline\)\. Q12 warm median ~1\.61 s → ~0\.84 s \(−48%\); PG ~0\.47 s\.
  - **Slice 22 — a numeric hash\-join key starts in the int64 lane LANDED 2026\-10\-21.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS, tpcds\-fireset PASS \(no changed plans\)\. Doc "Slice 22"\.
    - HammerDB's TPC\-H keys are numeric; HashKeysAreInt64 admitted machine ints only, so every TPC\-H hash join keyed
      a string map \(Q13 mapaccess\*\_faststr ~21%\)\. HashKeysTakeInt64Lane admits numeric; a non\-integral build key
      demotes once \(demoteIntHash\); a probe miss means no match only because datumToInt64Key is now exact for the
      big lane \(bigNumericInt64Key: 1\.0000000000000000000000 = 1\)\.
    - Pins: TestBigNumericInt64KeyIsExact, TestNumericHashJoinIntLane \(8 joins vs hash off; fails 363/364 with the
      old big\-lane answer\)\. Q13 ~3\.24 → ~2\.93 s; TPC\-H warm pass total 18\.65 → 17\.29 s\.
    - Found while probing NaN keys: P0\-E9 \(seq scan drops numeric NaN/±Infinity rows\), filed with an S2
      escalation under "Manually discovered"\.
  - **Slice 23 — a hash join's build side and a Gather worker's tree are borrow roots LANDED 2026\-10\-21.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025
    PASS=99 PLAN\-SHAPE same=99, TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS\. Doc "Slice 23"\.
    - Q21 cloneRowOwnedPrefix 37% \+ memclr 21%, Q10 26%: lineitem scans feeding a \(Parallel\) Hash or under a Gather
      never got a borrow — the descent started only below a borrowing aggregate\.
    - joinBorrowBuildChild: every build loop copies what it keeps \(retainBuildRow; drainRowsCtxCTID clones\)\.
      gatherOp\.runWorker marks the worker tree \(transferRowForQueue copies\); the leader tree only under a
      borrowing chain \(gatherOp\.borrowLeader\)\.
    - Pin: TestScanBorrowBuildSideAndGatherWorkers \(fails with retainBuildRow uncopied; needs
      MinParallelTableScanBlocks raised, else the cooperative build bypasses the borrowing scan\)\.
    - Q10 ~1\.70 → ~1\.18 s \(−30%\), Q21 ~1\.18 → ~1\.03 s \(−13%\); Q13/Q7 unchanged \(interleaved A/B\)\.
  - **Slice 24 — the hashed aggregate's int64 group lane LANDED 2026\-10\-21.** Movement: none — perf only\. The S3
    instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS\. Doc "Slice 24"\.
    - A single\-set single\-key hashed aggregate files groups under datumToInt64Key\(value\); the first
      unrepresentable key \(NULL, fraction, other kind\) demotes every int group to the string map in founding
      order under canonicalNumericKey\(v, 0\), and the lane always demotes after the drain — groups and order are
      exactly the string path's\. Pin: TestAggregateIntGroupLaneMatchesStringPath \(order included\)\.
    - Q13 ~3\.1–3\.2 → ~2\.9–3\.0 s \(~5%, two experiments\)\. Q18 neutral; slab allocation of groups measured no
      gain and was reverted\. Q18's first three runs per server \(~6\.3 s vs ~4\.5 s\) look like heap growth under
      the bench's GOGC=off — not yet attributed\.
  - **Slice 25 — the early qual decodes only its own columns LANDED 2026\-10\-21.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS\. Doc "Slice 25"\.
    - Q18's swing is NOT GOGC=off: it persists under the server's own GOGC=200 \(4\.5–6\.3 s\); unattributed\.
    - prefilterSkipInfo marks skip on prefix columns the qual does not read; the first pass decodes with that
      pfInfo; survivors decode again from column 0 with colInfo\. deformScanVisit admits value\-list InExpr\.
    - Pin: TestScanPrefilterSkipMatchesFullDecode \(5 quals, poison armed, vs skip off and early off; resuming
      survivors at MaxCols trips the poison\)\. Q12 ~0\.82 → ~0\.66 s \(−20%\), Q10 ~1\.19 → ~1\.11 s, Q6 unchanged\.
  - **Slice 26 — an mmgr memory context reuses the chunks its Reset emptied LANDED 2026\-10\-21.** Movement:
    none — perf only\. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99
    PLAN\-SHAPE same=99, TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS, postmaster PASS\. Doc "Slice 26"\.
    - growChunk always inserted a new chunk after the head, so the seq scan's per\-page arena gained a chunk per
      overflowing page: Reset 4% flat \+ insertion shifts 4\.5% of Q13, and the list held memory until Close\.
    - growChunk now swaps the first empty standard\-size chunk after the head into place \(chunks after the head are
      always empty\); oversized chunks are never reused for small allocations \(offset encoding\)\. Pin:
      TestResetReusesChunks \(8 → 156 chunks without the reuse\)\. Q13 ~2\.93 → ~2\.70 s, Q17 ~2\.55 → ~2\.49 s\.
  - **Slice 27 — a same\-kind comparison compares in place LANDED 2026\-10\-21.** Movement: none — perf only\. The S3
    instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS\. Doc "Slice 27"\.
    - Q6's qual: evalFastExpr 33% flat, evalBinary 22% and compareDatum 16% cum; date/timestamp compares built a
      time\.Time per side\. fastCompareDatum \(int/int, time/time with Scale 0, int64\-lane numeric/numeric\)
      runs inside the fastBinarySimple gate after the NULL check; every other pair takes evalBinary\.
    - Pin: TestFastCompareDatumMatchesCompareDatum \(18 values pairwise vs compareDatum; raw\-Int numeric compare
      fails it\)\. Q6 ~0\.45 → ~0\.31 s \(PG 0\.24\)\.
  - **Slice 28 — the scan borrow reaches through nested loops LANDED 2026\-10\-21.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS\. Doc "Slice 28"\.
    - Q3's orders scan \(Gather worker → Nested Loop → Parallel Hash Join → orders\) never borrowed: the descent
      stopped at the nested\-loop joinOp \(lateral stream, parameterised index inner\)\.
    - markScanBorrowChain now enters the outer side of a streaming nested\-loop joinOp \(sets nlOuterBorrowed so
      stepOuter keeps the row by shallow copy\) and of nestedLoopIndexJoinOp\. Pin:
      TestScanBorrowThroughNestedLoopOuter \(5 shapes incl\. NLI, LEFT, non\-equi, LATERAL\)\.
    - Q3 ~0\.56 → ~0\.45 s \(−20%\)\. Found: executor unit fixtures do not use the per\-page arena, so arena\-reset
      hazards are only witnessed by the server gates \(ledgered\)\.
  - **Slice 29 — filtered scans walk past unread columns LANDED 2026\-10\-21.** Movement: none — perf only\. The
    S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS, TestPort\_Isolation\* PASS\. Doc "Slice 29"\.
    - Slice 19's walk\-past never reached a filtered scan: markProjectScanReads required a SeqScan plan child
      \(a filtered one is Filter\{SeqScan\}\), and Open declined on ssiGistPred/ssiGinPred, which every absorbed
      Filter sets — the hooks only read the row once their index OID resolves, later in Open\.
    - Now: Filter\{SeqScan\} accepted; skipReadSet = readCols ∪ qual columns; flags applied after SSI resolution,
      declining only on a resolved OID\. Pin: TestScanSkipMatchesFullDecode \(3 filtered queries, must arm a skip\)\.
    - Q21 ~0\.95 → ~0\.86 s; Q10 unchanged\. Census note: Q11's 0\.34 s in a full pass is a whole\-suite artifact
      \(~0\.19 s on slice 26/27/28 builds alike\)\.
  - **Slice 30 — a plain\-string comparison compares in place LANDED 2026\-10\-21.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS\. Doc "Slice 30"\.
    - Q10's `l\_returnflag = 'R'` went pg\_lsn sniff → bpchar normalise → evalBinary → compareDatum \(LSN/UUID/row/
      array sniffs\) per row\. plainCompareString \(no '/', < 32 bytes, no leading '\(' or '\{'\) proves none can fire;
      such pairs compare by strings\.Compare after the bpchar normalisation\.
    - Pin: TestExprSiblingParityStringCompare \(compiled vs interpreted; text/bpchar/untyped; caught both mutations\)\.
      Q10 ~1\.04 → ~0\.95 s; Q12 unchanged\.
  - **Measured, not landed 2026\-10\-21 \(after slice 30\)\.** Doc split: slices 25–30 moved to
    0122\-0012\-tpch\-scan\-pipeline\-slices\-part3\.md \(indexed\)\.
    - Read set through joins: a debug build showed Q13/Q10's scans already carry read sets except Q10's orders
      \(early qual rejects ~96%\) — little payoff for a ~30\-site refactor; stays ledgered\.
    - Inline leaf fetch \(evalFastLeaf\) in the compiled BinaryOp arm: Q6 ~0\.32 → ~0\.35 s \(slower\), Q10/Q1 neutral;
      reverted\. Removing the recursion needs a flat step program \(ledger row\)\.
  - **Slice 31 — numeric B\-tree keys compare in place LANDED 2026\-10\-21.** Movement: none — perf only\. The S3
    instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 PLAN\-SHAPE same=99,
    TestPort\_RegressSuite PASS\. Doc part 3 "Slice 31"\.
    - decodeNumericParts copied both keys' NBASE digits into a fresh \[\]int16 per comparison \(Q3 mallocgc 16\.8%,
      PGCompareNumeric 10\.5%\); numericParts now keeps the raw digit bytes and reads them in place\.
    - Pin: TestPGCompareNumericMatchesExactOrder \(407 values vs big\.Rat; a big\-endian digit read fails it — the
      old hand\-picked test did not\)\. Q3 ~0\.46 → ~0\.44 s, Q21 ~2%, Q18 noise\.
  - **Slice 32 — pathkey usefulness compares precomputed identity keys LANDED 2026\-10\-21.** Movement: none —
    planning time only\. The S3 instrument is plan/value identity: tpcds\-fireset PASS \(99/99, no changed query\),
    sf025 PASS=99 PLAN\-SHAPE same=99, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS\. Doc part 3
    "Slice 32"\.
    - TPC\-DS Q64 planned for 36–44 s \(PG 2\.0 s total\): 94% under sameClass → exprEqual → exprIdentityKey →
      fmt\.Sprintf\. classMember now carries its key; keyOf memoises the rest; exprEqualKeyed decides like exprEqual\.
    - Pin: TestPathkeyUsefulnessKeyedMatchesExprEqual \(a pointer\-only compare fails 8 checks\)\. Q64 → ~3\.0 s\.
    - Next levers: TPC\-DS Q72 \(30 s vs PG 3\.4 s; the A/B was unreadable under host load 13–23\), Q23 \(10\.7 vs
      1\.8 s\), Q81 \(8\.3 vs 3\.6 s\); the other exprEqual callers \(pathKeyEqual\) still format keys \(ledger row\)\.
  - **Slice 33 — index key comparison walks attributes; the skip\-scan walk re\-descends on finaltup LANDED
    2026\-10\-21.** Movement: none — perf only\. The S3 instrument is value/plan identity: acceptance arm PASS \(24
    MATCH\), sf025 PASS=99 \(61 ck\-verified\) PLAN\-SHAPE same, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\.
    Doc part 3 "Slice 33"\.
    - Q72 \(inventory\_pkey skip scan probed on inv\_item\_sk\): comparePGIndexTuples deformed both tuples fully;
      compareHigh re\-compared a heap copy for prefix bounds; walkLeaf read whole leaves before/after the probe\.
    - Fix: pgIndexTupleWalk \(stop at first differing attribute\), one\-pass compareHigh, ScanCursor\.LeafLastKey \+
      finaltup re\-descent in walkLeaf \(PG \_bt\_advance\_array\_keys\)\. Q72 ~29 → ~8\.2 s \(PG 3\.4\); sweep 203 → 182 s\.
    - Pins: TestComparePGIndexTuplesWalkMatchesDeformFirst \(26 000 pairs vs the old algorithm; 3 mutations fail\);
      TestSkipScanLeafWalk exercised both jumps \(46 \+ 40\)\. Remaining Q72 gap ledgered \(two descents per group\)\.
  - **Slice 34 — a plain hash aggregate does not sort its output LANDED 2026\-10\-21\.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 \(61 ck\-verified, ordered
    ck\) PLAN\-SHAPE same, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 4 "Slice 34"\.
    - Q23's frequent\_ss\_items CTE \(404K groups\) spent ~2\.1 s in aggregateOp\.Open's M0097\-0117 output pre\-sort;
      PG's agg\_retrieve\_hash\_table emits hash\-table order\. Skipped for AggStrategyHashed without grouping sets\.
    - Q23 ~8\.7 → ~5\.5 s \(PG 2\.0\); TPC\-H Q18 4\.17 → 3\.59 s, Q13 2\.4 → 2\.17 s, total 16\.7 → 15\.9 s\.
    - Next levers: Q23 remaining \(best\_ss\_customer ~1\.4 s vs PG 0\.78\), Q14 \(19\.5 vs PG 8\.5 s\), Q81 \(7\.8 vs 3\.8\)\.
  - **Slice 35 — a projection under an aggregate evaluates only what the aggregate reads LANDED 2026\-10\-21\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025
    PASS=99 \(61 ck\) PLAN\-SHAPE same, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS
    with the deform poison armed globally\. Doc part 4 "Slice 35"\.
    - The planner's re\-expansion Project \(74/85 targets mapping a narrowed join back to the aggregate's arm
      coordinates\) made every join column a consumer: Q14's store\_sales\_pkey probes deformed all 23 columns\.
    - Fix: buildCompleteChild \(plain/partial Aggregate → sorts → Project\) with projectLiveSet; projectOp skips dead
      ColumnRef/constant targets; deformLateralOuterRefs folds lateral probe keys \(OuterColumnRef level 1\)\.
    - TPC\-H Q3 −12 %, Q5 −10 %, Q13 −9 %; Q14 only −2\.5 % \(prefix bound still wide; ledgered\)\.
    - Yield watch \(banner rule b\): slice 35's own target Q14 moved < 3 %; if slice 36 also moves its target < 3 %,
      M0122\-0012 yields to M0147\.
  - **Slice 36 — a re\-opened index probe keeps its decode buffer LANDED 2026\-10\-22\.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc part 4 "Slice 36"\.
    - lateralJoinStream closes/re\-opens the inner per outer row; indexScanOp\.Close dropped scanRow, so every probe
      allocated a full\-width row \(4\.1 s of 30 s CPU\)\. Close now keeps it \(bitmapHeapScanOp too\)\.
    - Named target Q14: 16\.9 → 16\.0 s \(−5\.5 %, ≥ 3 %: no yield\)\. Probe micro −35 %, TPC\-H Q5 −30 %, Q7 −20 %\.
    - Benchmark note: tpch\-gap's GOGC=off moves GC cycles between queries \(a false Q2 0\.40 → 0\.72\); A/B with GOGC=100\.
    - Next levers: lateral emit cloneRow \(2\.2 s on the micro\), IOS per\-row output allocation \(ledger row\)\.
  - **Slice 37 — a borrowed nested loop emits its pair buffer LANDED 2026\-10\-22\.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc part 4 "Slice 37"\.
    - markScanBorrowChain sets nlEmitBorrowed beside nlOuterBorrowed; both streaming nested loops then emit m\.pair
      instead of cloneRow\(m\.pair\)\. Named target probe micro 0\.50–0\.63 → 0\.40–0\.41 s \(−20 %\); Q14, TPC\-H flat\.
    - Erratum: slice 36's TPC\-H Q5/Q7 deltas are bimodal noise \(fresh HEAD Q5 0\.63 / 0\.39 s across two rounds\)\.
  - **Slice 38 — a CTE scan returns one slot LANDED 2026\-10\-22\.** Movement: none — perf only\. The S3 instrument is
    value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, TestPort\_RegressSuite
    PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc part 4 "Slice 38"\.
    - Named target TPC\-DS Q81: cteScanOp\.Next allocated a slot per row \(21 % of the correlated SubPlan's CTE rescans\);
      both CTE scan ops now re\-point one slot \(PG CteScanNext / ss\_ScanTupleSlot\)\. Q81 9\.9–11\.8 → 7\.5–8\.2 s \(~−30 %\)\.
    - sf025 sweep sum 168\.2 → 159\.3 s\. Remaining: interpreted OuterColumnRef comparison \(ledger row\)\.
  - **Slice 39 — a Gather under an aggregate ships only the columns the aggregate reads LANDED 2026\-10\-22\.** Movement:
    none — perf only\. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc part 4
    "Slice 39"\.
    - Named target TPC\-DS Q23: Gather workers deep\-copied ~50\-column join rows \(cloneRowOwned 11\.6 %\) for
      HashAggregates reading three; buildCompleteChild now stamps gatherOp\.liveCols \(consumerLiveColumns\)\.
    - Q23 mean 5\.10 → 4\.49 s \(−12 %, 4 rounds\); sweep sum 159\.3 → 155\.0 s; TPC\-H flat\.
  - **Slice 40 — the legacy Filter evaluates a compiled predicate LANDED 2026\-10\-22\.** Movement: none — perf only \(plus a
    batch\-filter sibling fix\)\. The S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 \(61
    ck\) PLAN\-SHAPE same, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc part 4
    "Slice 40"\.
    - Named target TPC\-DS Q81: the SubPlan's inner filterOp \(legacy build\) interpreted its predicate 47M times; newFilterOp
      now compiles it once and nextPerRow uses evalFastExpr\. Q81 7\.3–9\.0 → 5\.4 s; Q1 17\.8 → 11\.2 s; sweep 155\.0 → 147\.2 s\.
    - Found \+ fixed: evalFilterBatch skipped the bpchar / pg\_lsn / text\-shape rules of the per\-row twins \(text 'ab' =
      'ab  '::char\(4\) was false\); those string rows are now re\-evaluated by the interpreter\. Pin:
      TestFilterOpCompiledPredicateMatchesInterpreted \(batch on/off\)\.
  - **Slice 41 — an exclusive lower bound descends past its group LANDED 2026\-10\-22\.** Movement: none — perf only\. The
    S3 instrument is value/plan identity: acceptance arm PASS \(24 MATCH\), sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, nbtree \-race PASS, executor package PASS poison\-armed\. Doc part 4
    "Slice 41"\.
    - Named target TPC\-DS Q72: 28M leaf binary searches for 0\.93M cursors — the skip walk's past\-the\-group jump descended
      to the group's FIRST leaf; NewScanCursor / rangeScanPosLeaf now use the nextkey descent \(\_bt\_first `>`\) and skip a
      leaf below the bound on its high key\. Q72 8\.4–10\.4 → 2\.3–2\.6 s \(PG 3\.4\); sweep 147\.2 → 142\.0 s; TPC\-H flat\.
  - **Slice 42 — one identity\-key memo per search, indexed sameClass LANDED 2026\-10\-22\.** Movement: none — planning time
    only\. The S3 instrument is plan/value identity: tpcds\-fireset PASS \(99/99, no changed query\), sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 4
    "Slice 42"\.
    - Named target TPC\-DS Q64 \(fresh gap census: 4\.4 s vs PG 0\.36, ratio 12\): leadingMembersOf 50 % — sameClass linear
      scans \(30 %\) and per\-rel key re\-formatting \(26 %\)\. searchCtx\.exprKeys is now shared; ecClassOf indexes members\.
    - Q64 EXPLAIN 2\.9 → 1\.7 s, run 3\.5 → 2\.4 s \(−32 %\); sweep Q64 4\.36 → 1\.85 s; TPC\-H identical\.
    - Gap census \(warm, :65438\) for next targets: Q35 3\.4 vs 0\.35, Q83 1\.3 vs 0\.10, Q5 1\.6 vs 0\.31, Q39 2\.6 vs 0\.59,
      Q21 2\.3 vs 0\.70, Q14 16\.8 vs 7\.7\.
  - **Slice 43 — an unknown literal is coerced once, not per row LANDED 2026\-10\-22\.** Movement: none — perf only\. The
    S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS\. Doc part 4 "Slice 43"\.
    - Named targets TPC\-DS Q83 \(1\.34 s vs PG 0\.10\) and Q58 \(0\.92 vs 0\.087\): 92 % of a `date\_dim` scan under
      `d\_date IN \('2001\-07\-13', …\)` was tryParseStringAs re\-parsing the literal per row \(590 ms vs PG 6\.5 ms\)\.
    - unknownLitCoercion pre\-coerces a compiled comparison's bare literal to the other side's declared kind;
      inListConst\.coercedItems coerces a literal IN list once per operand kind; both apply only on a runtime kind match\.
    - Q83 1\.6 → 0\.24 s, Q58 1\.3 → 0\.6 s, Q16 1\.2 → 0\.92 s, Q32 0\.11 → 0\.04 s; sweep 142\.4 → 139\.6 s; TPC\-H values identical\.
    - Next candidates: Q58 rest is join row cloning \(cloneRowOwnedPrefix\); Q35 3\.4 vs 0\.35, Q39 2\.4 vs 0\.59, Q87, Q51\.
  - **Slice 44 — the sorted aggregate and the incremental sort stream LANDED 2026\-10\-22\.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 4 "Slice 44"\.
    - Named target TPC\-DS Q35 \(3\.4–3\.7 s vs PG 0\.35\): `Limit 100 → GroupAggregate → Incremental Sort → Nested Loop`
      read all 50 000 outer rows \(16\.4 M filter pairs\) where PG stops at 20 300 — both operators drained in Open\.
    - incrementalSortOp reads one prefix group per Next \(one\-row lookahead\); aggregateOp\.nextSorted returns each group
      as the next one's first row closes it — the drained loop's exact call sequence\.
    - Q35 3\.35–3\.96 → 1\.49–1\.65 s; sweep 139\.6 → 136\.5 s; TPC\-H values identical\.
    - Next candidates: Q35 rest = 5\.7 M join\-filter pairs at ~270 ns vs PG ~90 ns \(NL over Materialize\); Q58 join row
      cloning; Q39 2\.4 vs 0\.59, Q87, Q51\.
  - **Slice 45 — the nested\-loop join filter is compiled LANDED 2026\-10\-22\.** Movement: none — perf only\. The S3
    instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 4 "Slice 45"\.
    - Named target TPC\-DS Q35's remaining 1\.55 s: joinPredicateMatch \(NL and lateral streams\) evaluated plan\.Predicate
      interpreted — a rowSlotView interface box \(convTslice\) and an evalExprSlot walk per candidate pair \(32 %\)\.
    - The predicate compiles once per joinOp \(predExprs/predIdx\) and runs evalFastExpr over a reused MaterializedSlot\.
    - Q35 1\.49–1\.83 → 0\.97–1\.09 s, Q74 32\.8 → 17\.3–19\.7 s; sweep 136\.5 → 125\.2 s; TPC\-H values identical\.
    - Next candidates: pair\-buffer copy per pair \(memmove ~8 %\); indexScanOp Filter interpreted \(~14 % of Q35\); Q58
      join row cloning; Q74 still 18 s vs PG \(check its plan\)\.
  - **Slice 46 — integer variance/stddev are PG's numeric\_stddev\_internal LANDED 2026\-10\-22\.** Movement: none — perf
    and output fidelity; plans/row counts identical\. S3 instrument: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance
    arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 5 "Slice 46" \(new file\)\.
    - Named target TPC\-DS Q39 \(2\.5 s vs PG 0\.59\): exactIntVariance's 15\-step big\.Float Newton sqrt was 28 % of the
      query, and its text was not PG's \(12 decimals / 18 significant digits vs select\_div\_scale's rscale\)\.
    - Ported select\_div\_scale, rounded div\_var and sqrt\_var \(exact big\.Int isqrt at F NBASE digits \+ round\_var\);
      numeric Datum result\. 696/696 probe lines byte\-identical to PG; window test now asserts window\.out\.
    - Q39 3\.0–3\.9 → 2\.1–2\.4 s; sweep 125\.2 → 120\.8 s; TPC\-H values identical\.
    - Left: numeric\-input sibling \(no dscale in state\), float path \('g' 15\); ledgered\.
  - **Slice 47 — CASE compiles LANDED 2026\-10\-22\.** Movement: none — perf only\. The S3 instrument is value/plan
    identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS,
    TestPort\_Isolation PASS\. Doc part 5 "Slice 47"\.
    - Named target TPC\-DS Q59 \(2\.2–2\.4 s vs PG 0\.72\): its CTE's seven `sum\(CASE WHEN d\_day\_name = … END\)` ran
      evalCaseExpr through an ExprAdapter — slotToRow plus a rowSlotView box per branch \(46 % \+ convTslice 11 %\)\.
    - New ExprCase node \(exprCaseInfo, evalFastCase\) mirrors evalCaseExpr arm for arm, bpchar facts compiled in\.
    - Q59 2\.32–2\.37 → 1\.80–1\.89 s, Q2 0\.93 → 0\.68 s; TPC\-H values identical\.
    - Next: the bpchar `d\_day\_name = 'Sunday'` compare \(evalFastBinaryApply 23 % of Q59\); Q78, Q97, Q16, Q8 gaps\.
  - **Slice 48 — a Sort is a borrow root LANDED 2026\-10\-22\.** Movement: none — perf only\. The S3 instrument is
    value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite
    PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc part 5 "Slice 48"\.
    - Named target TPC\-DS Q97 \(0\.93–1\.11 s vs PG 0\.11\): cloneRowOwnedPrefix 40 % — every probe row of the two
      Parallel Hash Joins under Gather Merge → Sort was cloned; the Sort stopped markScanBorrowChain\.
    - finishSortNode / finishIncrementalSortNode now call markScanBorrowChain \(both materialise every kept row\)\.
    - Q97 → 0\.77–1\.02 s, Q87 1\.31–2\.27 → 1\.27–1\.36 s; sweep 121\.9 → 116\.7 s; TPC\-H flat, values identical\.
    - Next: Q97 still ~7× PG — split worker sort vs leader Group/Merge Full Join; Q16, Q8, Q78 gaps\.
  - **Slice 49 — a skip\-scan group key is built without re\-resolving the index LANDED 2026\-10\-22\.** Movement: none —
    perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS
    \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 5 "Slice 49"\.
    - Named target TPC\-DS Q16 \(0\.97–1\.20 s vs PG 0\.08\): skip\-scan probes on catalog\_returns\_pkey / catalog\_sales\_pkey;
      groupBounds called ctx\.indexProbeKey twice per prefix group, re\-resolving desc \+ key columns by name \(37 %\)\.
    - groupBounds now uses e\.desc / e\.keyCols via btreeSkipEnum\.probeKey \(byte\-identical, pinned\)\.
    - Q16 → 0\.66–0\.90 s, Q95 3\.69–4\.21 → 3\.37–3\.96 s; sweep 116\.7 → 115\.6 s; TPC\-H values identical\.
    - Next: per\-group decode/re\-encode \+ per\-entry compares \(PG compares scankeys, \_bt\_advance\_array\_keys\)\.
  - **Slice 50 — a literal IN list of nine or more items is hashed LANDED 2026\-10\-22\.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 5 "Slice 50"\.
    - Named target TPC\-DS Q8 \(0\.90–1\.04 s vs PG 0\.14\): `substr\(ca\_zip,1,5\) IN \(400 literals\)` compared linearly
      per row \(591 ms vs PG 20 ms; PG hashes a ≥9\-element constant SAOP\)\.
    - inListConst\.hashed / inListHash\.probe: string/string \(bpchar rule kept exact\) and int/int only\.
    - Q8 → 0\.34–0\.45 s; TPC\-H values identical\.
    - Left \(ledgered\): numeric/date/interval operands and explicit op ANY / <> ANY stay linear\.
  - **Slice 51 — a streaming UNION ALL passes the borrow on LANDED 2026\-10\-22\.** Movement: none — perf only\. The S3
    instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc part 5 "Slice 51"\.
    - Named target TPC\-DS Q71 \(0\.64–0\.66 s vs PG 0\.08\): cloneRowOwnedPrefix 40 % — markScanBorrowChain stopped at the
      Parallel Append \(setOp\), so every probe row of its three Parallel Hash Joins was cloned\.
    - markScanBorrowChain descends both branches of a streaming, merge\-key\-free setOp \(nextStreaming keeps nothing\)\.
    - Q71 → 0\.40–0\.42 s, Q5 0\.87–1\.70 → 0\.68–1\.25 s, Q14 20\.4–20\.9 → 17\.5–19\.3 s; TPC\-H values identical\.
    - Next: Q71 rest = NL index probes \+ Gather queue copies; Q78, Q80, Q59 bpchar compare\.
  - **Slice 52 — a skip\-scan group key is formed from the entry's own images LANDED 2026\-10\-22\.** Movement: none —
    perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS
    \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, nbtree \-race PASS\. Doc part 5 "Slice 52"\.
    - Measurement: goopg clones/corpora run at 128MB shared\_buffers vs PG's 2GB \(DB 761MB\) — warm runs re\-read pages
      and verify checksums; the private clone now runs at 2GB\. At equal buffers Q72 is at parity; Q16 is the top ratio\.
    - Named target TPC\-DS Q16 \(0\.67 s vs PG 0\.06\): groupBounds decode \+ re\-encode 40 %; groupBoundsRaw forms
      probe/pivot from the entry's prefix images \(PGIndexImageInline gate\), byte\-identical \(pinned\)\.
    - Q16 0\.65–0\.74 → 0\.48–0\.51 s, Q95 −8 %; sweep 119\.0 → 115\.7 s; TPC\-H values identical\.
    - Next: Q16 per\-entry whole\-tuple compares \(38 %\); Q97 5×, Q80 3×, Q78 2\.9×, Q59 2\.6× at equal buffers\.
  - **Slice 53 — nested Projects and non\-correlated sublinks keep the deform bound LANDED 2026\-10\-22\.** Movement:
    none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm
    PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS poison\-armed\. Doc
    part 5 "Slice 53"\.
    - Named target TPC\-DS Q14 \(13\.7–16\.3 s vs PG ~8 at equal buffers\): store\_sales / date\_dim / item probes deformed
      every column — an inner Project built with every target live, and a non\-correlated sublink declining the fold\.
    - buildProjectNode gives a child Project a live set from its live targets; deformSublinkArgs reads nothing for an
      IsNonCorrelated sublink without params \(lateral fold too\)\.
    - Q14a 7\.1–7\.8 → 6\.7–6\.8 s, Q14b 8\.0–8\.8 → 6\.8–7\.4 s; sweep 115\.7 → 113\.4 s; TPC\-H flat, values identical\.
    - Tried and dropped: MaterializedSlot\.owned copy elision \(Sort / Gather rows\) — Q97 slightly slower\.
    - Next: slab buildRec Project arm has no live set; CTE bodies built with unbounded Build\(\)\.
  - **Slice 54 — the B\-tree descent compares without copying or returning walks LANDED 2026\-10\-22\.** Movement: none
    — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS
    \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, nbtree \-race PASS\. Doc part 5 "Slice 54"\.
    - Named target TPC\-DS Q72 \(per\-probe descents\): newPGIndexTupleWalk's 48\-byte struct return took 1\.06 s of 21 s
      CPU in Q14, and pageHighKey copied the high key twice per tree level for a compare\-and\-drop move\-right test\.
    - pgIndexTupleWalk\.init fills in place; keyExceedsHighKey / keyAtOrPastHighKey / itemOvershootsHighKey read an
      aliasing key \(pageHighKeyNoCopy\); pageHighKey still copies \(once\)\.
    - Q72 1\.10 → 0\.98 s, Q16 0\.47 → 0\.42 s, Q14b 6\.4 → 5\.9 s, Q14a flat; sweep 113\.4 → 112\.3 s; values identical\.
    - Tried and dropped: a per\-Context pgIndexKeyColumns cache in indexProbeKey — flat \(≈2 % of samples\)\.
    - Next: Q14 descent still the largest share; Gather\-queue cloneRowOwned; unread prefix text decode\.
  - **Slice 55 — index\-tuple comparison takes the cached\-offset path LANDED 2026\-10\-22\.** Movement: none — perf only\.
    The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, nbtree \-race PASS\. Doc part 5 "Slice 55"\.
    - Named target TPC\-DS Q16 \(0\.42 s vs PG 0\.06, same plan and loops\): 82 % in scanLeafItems, the skip walk's up to
      three whole\-tuple compares per entry; comparePGIndexTupleAttrs 42 %\.
    - compareFixedPrefix: no null bitmap → leading fixed\-width attributes sliced at their cached offsets
      \(index\_getattr's attcacheoff\); declines to the walk otherwise \(answers and errors unchanged, pinned\)\.
    - Q16 0\.42–0\.45 → 0\.36 s, Q72 1\.02 → 0\.90 s, Q14 down; sweep 112\.3 → 109\.5 s; values identical\.
    - Next: Q16 still ~6× PG — skip walk's 3 compares/entry vs \_bt\_checkkeys' per\-attribute check; \_bt\_readpage precheck\.
  - **Slice 56 — a fixed\-width skip group's keys are copied into templates LANDED 2026\-10\-22\.** Movement: none — perf
    only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 5 "Slice 56"\.
    - Named target TPC\-DS Q16: groupBoundsRaw 34 % of CPU — per group a deform \(two slice allocs\) plus two generic
      FormPGIndexTuple calls\.
    - groupBoundsFixed: all\-fixed\-width probe → per\-Rescan probe/pivot templates \(formed by formKey\), per group one
      alloc \+ prefix bytes copied at nbtree\.PGIndexFixedAttrOffsets; declines on null bitmap / varlena \(pinned\)\.
    - Q16 0\.34–0\.38 → 0\.26–0\.29 s, Q95 2\.6–2\.9 → 2\.4 s; sweep 109\.5 → 109\.3 s; values identical\.
    - Next: Q16 ~4\.5× PG — walkLeaf's up to 3 tuple compares per entry\.
  - **Slice 57 — the skip walk reads its group position off one comparison LANDED 2026\-10\-22\.** Movement: none — perf
    only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS \(2 of 3 runs; the first failure's output was not captured\),
    nbtree \-race PASS\. Doc part 5 "Slice 57"\.
    - Instrument: the Go CPU profiler under\-samples parallel queries here \(~66 ms vs 1\.1 CPU\-s per Q16 run\); per\-query
      CPU from /proc utime\+stime and `perf record \-p` are the instruments\.
    - Named target TPC\-DS Q16: per entry 3 bound compares in walkLeaf plus a per\-entry `lo` compare in scanLeafItems\.
    - BTree\.CompareBoundAttrs \(deciding attribute\) → skipPos predicates equal the replaced calls \(pinned, mutation\-checked\);
      scanLeafItems drops `lo` once one entry passed it \(leaf order\)\.
    - Q16 0\.268 → 0\.237 s wall, 1\.11 → 0\.975 CPU\-s; Q72 −4 %; sweep sum noise \(Q1/Q81/Q14 rises did not repeat in A/B\)\.
    - Next: Q16 still ~4× PG CPU — per\-entry parse \+ call layers; PG's \_bt\_set\_startikey / look\-ahead\.
  - **Slice 58 — the serial leader path of a sorted Group stops copying and allocating per row LANDED 2026\-10\-22\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, executor package PASS
    poison\-armed\. Doc part 6 "Slice 58" \(new part; part 5 reached 737 lines\)\.
    - Named target TPC\-DS Q97 \(0\.56 s vs PG 0\.12\): perf split by stack — the leader is busy the whole query
      \(~530 ms/run\) vs ~150 ms per worker; its serial chain is the bottleneck\.
    - projectOp copies bare ColumnRef targets from a MaterializedSlot row in place \(projectColRefs\); nextSorted
      compares group keys via sameSortedGroup / appendGroupColKey without per\-row allocations \(both pinned\)\.
    - Q97 CPU 1\.10 → 1\.06 CPU\-s parallel, 0\.91–0\.94 → 0\.83–0\.89 serial; wall noise ±10 % hides it\.
    - Tried and dropped: pdqsort with index tiebreak for sortOp \(Q87 \+8 %\); slices\.SortStableFunc \(flat\)\.
    - Next: leader's Gather Merge key re\-eval, merge join input re\-keying, CASE IS NULL/AND interpreted fallback\.
  - **Slice 59 — IS NULL compiles; sort and merge comparators take the same\-kind fast path LANDED 2026\-10\-22\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 6 "Slice 59"\.
    - Instrument: user\-mode instructions per query \(perf stat \-e instructions:u \-p PID; tmp/ds\-instr\.sh\) — ±0\.005 G
      between rounds, unaffected by host load \(load average 7–10 swung CPU ±10 %\)\.
    - Named target TPC\-DS Q97: ~20 % of serial instructions in lessKeyVals → compareDatum → promoteCrossKind for int
      keys; IsNullExpr ran through ExprAdapter \(interpreted\) per row\.
    - lessKeyVals / mergeKeysLess / compareMergeKeys try fastCompareDatum first; IsNullExpr → ExprNullTest \(pinned,
      mutation\-checked; RowExpr keeps the adapter\)\.
    - Q97 7\.88 → 7\.07 Ginstr \(−10 %\), CPU 1\.09–1\.19 → 0\.94–1\.02; Q51 −6\.4 %; Q16/Q72/Q23/Q95/Q1 identical\.
    - Next: mergeSortedSource re\-keying, Gather Merge key re\-eval, reflected swapper in sortOp \(~3 %\)\.
  - **Slice 60 — string sort keys compare plainly; DISTINCT ON keys stop allocating per row LANDED 2026\-10\-22\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 6 "Slice 60"\.
    - Named target TPC\-DS Q87 \(0\.95 s vs PG 0\.31\): textShapeAmbiguous \+ compareDatum per string sort compare;
      distinctOnOp\.Next concatenated datumKey strings per row\.
    - sortOp\.keyText \(newSortOp\) → strings\.Compare for two KindString values of a character\-string key, also in
      compareMergeKeys; appendDistinctOnKey into a reused buffer \(both pinned, mutation\-checked\)\.
    - Q87 27\.06 → 24\.0 Ginstr \(−11 %\), Q38 −11 %, Q35 −2\.6 %; Q97 \+0\.3 % layout; sweep 109\.9 → 105\.8 s\.
    - Next: mergeKeysLess string path \(no text flags\); distinctOp rowKeyTrimmed per row; Q87 still ~3× PG\.
  - **Slice 61 — descents start from a cached root; scans stop building the descent path LANDED 2026\-10\-22\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS \(a first run hit the 10 m
    package timeout under load at 600 s; re\-run with \-timeout 30m passed in 540 s\), nbtree \-race PASS\. Doc part 6
    "Slice 61"\.
    - Named target TPC\-DS Q87: index probes 28 % of instructions; readMeta per descent ~1\.5 %, path growslice ~1\.3 %\.
    - BTree\.rootCache \(PG rd\_amcache\) validated per descent by rootCacheValid; descendMode needPath=false for scans
      \(pinned incl\. stale\-root and poisoned\-leaf cases, mutation\-checked\)\.
    - Q87 −1\.4 %, Q38 −1\.3 %, Q72 −2\.1 %, Q14 −1\.6 % instructions — **below the 3 % bar** \(counts toward rule b\)\.
    - Discovery filed: M0131\-S33 \(split\-created internal page keeps its first item's key\) \+ ledger row\.
    - Next: if slice 62 also moves its target < 3 %, M0122\-0012 yields to M0147 \(banner item 10 rule b\)\.
  - **Slice 62 — an explicit\-frame window aggregate keeps its running state while the frame head stays put LANDED
    2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS \(643 s with
    \-timeout 30m\)\. Doc part 6 "Slice 62"\.
    - Named target TPC\-DS Q51: evalExplicitFrameAggFuncs re\-aggregated every row's frame from its head — O\(n²\) for
      ROWS UNBOUNDED PRECEDING; PG eval\_windowaggregates continues from aggregatedupto while the head is fixed\.
    - Incremental \[aggStart, aggEnd\) state for sum/count/avg/min/max without EXCLUDE \(pinned against the
      from\-scratch path over ten frames × ten aggregates, mutation\-checked\)\.
    - Q51 15\.79 → 12\.17 Ginstr \(−22\.9 %\), wall ~1\.5 → ~1\.15 s; other window queries identical; sweep 107\.1 → 105\.5 s\.
    - Rule b: slice 62 moved its target ≥ 3 % → no yield\.
    - Deferred \(ledger\): PG's inverse\-transition moving aggregate for sliding frames\.
    - Next: Q51 still ~2\.7× PG — leader row clones via sortOp over CTE scans; Gather queue clones\.
  - **Slice 63 — a Gather Merge under an aggregate ships only live columns; live / prefix clones leave dead cells
    unwritten LANDED 2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99
    \(61 ck; Q78 ck identical\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS,
    TestPort\_Isolation PASS \(632 s, \-timeout 30m\), executor package PASS poison\-armed\. Doc part 6 "Slice 63"\.
    - Named target TPC\-DS Q78: transferRowForQueue in gatherMergeOp deep\-copied ~50\-column join rows \(clone 9\.3 % \+
      memclr 3\.6 % of instructions\); slice 39's live mask covered plain Gather only\.
    - buildCompleteChild GatherMerge arm \(consumers \+ merge keys\); cloneRowOwnedLive / cloneRowOwnedPrefix skip dead\-cell
      writes \(NullDatum is the zero Datum — pinned\)\.
    - Q78 27\.87 → 26\.71 Ginstr \(−4\.2 %\); other queries ±0\.1 %; wall/CPU unresolvable at load 7–9\.
    - Next: worker Sort below Gather Merge stores whole join rows \(worker trees built with buildNode\); full\-width transfer rows\.
  - **Slice 64 — string literals compile to constants; a plain string pair skips the character\-string ambiguity test
    LANDED 2026\-10\-23\.** Movement: none — perf only\. Doc part 6 "Slice 64"\.
    - Named target TPC\-DS Q59: seven char\(9\) `d\_day\_name = 'Sunday'` CASE arms per row; literals ran as ExprAdapter
      \(buildExpr has no fold\); textShapeAmbiguous ran before slice 30's plain\-string shortcut\.
    - StringConst → ExprConstVal; plain pair decided first with the bpchar blank rule inline \(pinned compiled/batch vs
      interpreted over >10 000 pairs incl\. uuid/pg\_lsn/row/array shapes; mutation\-checked\)\.
    - Q59 20\.14 → 17\.19 Ginstr \(−14\.7 %\), Q2 −21\.3 %, Q43 −6\.7 %; Q19/Q3/Q10 identical\.
    - S3 instrument value/plan identity: sf025 PASS=99 \(61 ck; Q2/Q43 ck identical\) PLAN\-SHAPE same, acceptance arm
      PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS — run after the nightly \(fired 00:51,
      ended 02:09\), which the acceptance arm refuses to overlap\. Sweep Q59 1\.60 → 1\.30 s, Q2 0\.46 → 0\.42 s\.
  - **Slice 65 — a subquery IN stops allocating per row; a nested loop copies its outer tuple once per outer row
    LANDED 2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 6
    "Slice 65"\.
    - Target choice: Q25's 22× sweep gap is a 128MB shared\_buffers artifact \(0\.20 s vs PG 0\.14 at 2GB\); Q35 is real
      \(2\.3–3\.1 s vs PG 0\.354 at 2GB\)\.
    - Q35: inListItemsEval allocated itemBP/itemLit at the subquery's result size per row \(EXISTS→ANY\), and the heap
      profiler's stack walks on those allocations cost ~13 %; nlJoinStream\.pairMatches re\-copied the outer tuple per pair\.
    - subqueryInListMeta caches the uniform metadata per InExpr; pairOuterValid copies the outer half once \(pinned;
      the reset is mutation\-checked by existing NL suites\)\.
    - Q35 12\.43 → 10\.39 Ginstr \(−16\.4 %\), wall 2\.3–3\.1 → 0\.62 s; sweep 98\.9 → 95\.5 s\.
    - Next: hashed SubPlan for EXISTS … OR EXISTS … \(PG\) instead of per\-row cached list \+ probe; Q39 \(2\.2 vs 0\.59 s\)\.
  - **Slice 66 — liveness crosses a Project and a join's probe side; a Gather worker carves shipped rows from chunks
    LANDED 2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same \(also PASS=99 on a poison\-armed binary\), acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite
    PASS, TestPort\_Isolation PASS\. Doc part 6 "Slice 66"\.
    - Q39 \(HashAggregate → Project → Hash Join → Gather, 2\.34 M rows per statement\): workers spent 30 % of all
      instructions in transferRowForQueue — VirtualSlot\.Row\(\) of all 9 cells, then a deep copy; liveCols was nil because
      a Project and a join stood between the aggregate and the Gather\.
    - buildProjectNode builds its child through buildCompleteChild; new Join arm \(joinLeftConsumers: INNER/LEFT/CROSS,
      not LATERAL/null\-aware, layout\-checked\); transferRowForQueueLive reads VirtualSlot live cells in place; rowChunk
      \(32 rows per allocation\) for worker rows \(pinned; mutation\-checked\)\.
    - Q39 29\.06 → 26\.86 Ginstr \(−7\.6 %\), CPU −11 %, wall unchanged \(~2\.0 s; leader\-bound\); Q22 −0\.7 %; Q21/Q7/Q26/
      Q48/Q13 flat; sweep 95\.5 s unchanged\.
    - Next: Q39's serial leader — per\-group allocation, string\-keyed group map, numeric finalisation of 540 K groups
      \(PG accumulates int4 in int128, int4\_accum\); or the hashed SubPlan for Q35\.
  - **Slice 67 — numeric division and variance stop formatting and exponentiating; int4 variance input accumulates in 128
    bits LANDED 2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck;
    Q39 ck identical\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation
    PASS\. Doc part 6 "Slice 67"\.
    - Q39 leader: finalizeGroup 31 %, HAVING numericDiv 15 %; pow10Big ran big\.Int\.Exp per call, the NBASE weight helpers
      formatted values \(String / Text\(10\)\) to count digits, stddev\_samp\(int4\) kept two big\.Int sums per group\.
    - pow10 tables; decimalDigitsBig / decimalDigitsUint64; nbaseWeightAndFirstDigit takes the mantissa; int128 lane
      \(sx128/sxx128, PG do\_int128\_accum\) folded by intSums for finalise, serialise and combine \(pinned against the
      string\-based oracles and big\.Int sums; mutation\-checked\)\.
    - Q39 26\.86 → 22\.42 Ginstr \(−16\.5 %\), wall 2\.2 → 1\.56 s \(−30 %\); Q17/Q7/Q22/Q21/Q26 flat; sweep Q39 1\.75 → 1\.37 s\.
    - Next: Q39 per\-group state \(664\-byte aggRuntime per aggregate, string\-keyed group map\); or the hashed SubPlan for Q35\.
  - **Slice 68 — numeric division takes a 128\-bit path and drops redundant big\.Int copies; hash\-aggregate groups come
    from chunks LANDED 2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025
    PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS,
    TestPort\_Isolation PASS\. Doc part 6 "Slice 68"\.
    - Q39 leader after slice 67: newGroup allocation 9 %, HAVING numericDiv 10\.6 % \(numericMant 20 % of it,
      newNumeric 15 %, the int64 fast path tried and failed\)\.
    - numericDivUint128 \(either lane, uint64 magnitudes, bits\.Mul64/Div64\); numericDivBig without the redundant
      numericMant / newNumeric / numerator copies and rounding temporaries; groupRuntime / aggRuntime / key rows from
      32\-group chunks \(pinned: 200 000\-case fuzz vs numericDivBig, rounding \+ declines; mutation\-checked\)\.
    - Q39 22\.48 → 21\.67 Ginstr \(−3\.6 %\), CPU −7 %; Q4 −4\.1 %, Q67 −0\.8 %; Q22/Q11/Q87/Q17 flat\.
    - Q39 wall is bimodal on BOTH binaries \(~1\.45 s or ~2\.2–2\.7 s per 3\-run sample\) — compare instructions/CPU\.
    - Discovery filed: M0122\-0016 \(big numeric results accumulate in the never\-reset permanent arena\)\.
  - **Slice 69 — a hashed DISTINCT feeding an order\-insensitive hashed aggregate stops sorting its output LANDED
    2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 7
    "Slice 69" \(part 7 split from part 6 at the 800\-line limit\)\.
    - Named target TPC\-DS Q75 \(warm 0\.56 s vs PG 0\.24 s\): the UNION's hashed DISTINCT \(distinctOp\) sorted ~485 K rows
      for "deterministic output" — a third of the leader — before a sum\(\) HashAggregate regrouped them; PG's hashed
      DISTINCT never sorts\.
    - distinctOp\.unordered; markDistinctUnordered from the Aggregate arm through Projects; aggregateIgnoresInputOrder
      \(count/min/max, sum/avg over non\-float input; no ORDER BY / grouping sets / Finalize\) \(pinned; mutation\-checked\)\.
    - Q75 14\.45 → 12\.29–12\.39 Ginstr \(−14 to −15 %\); output byte\-identical to before and to :65438 tpcds025; Q87/Q38/
      Q16/Q94 flat\.
    - Finding: tmp/q64\-clone is the SF0\.25 corpus \(not SF1\) — compare against :65438 tpcds025\.
    - Folded in M0122\-0016's leftover mctx\.go comment fix\.
  - **Slice 70 — projections run compiled, COALESCE compiles, dedup keys built in a reused buffer LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 7 "Slice 70"\.
    - Q75 whole\-query profile: projectOp\.Next ran evalExprSlot for every computed target \(~1/5\); COALESCE was an
      ExprAdapter; rowKeyTrimmed formatted every column into a strings\.Builder and distinctOp cloned every input row\.
    - projectOp fast/fastIdx \(compiled once, kept across re\-Opens\); ExprCoalesce chain; appendRowKeyTrimmed \+
      appendFormatNumeric, rowKeyTrimmed wraps it; distinctOp seen\[string\(buf\)\] \(pinned vs interpreter/oracles;
      mutation\-checked\)\.
    - Q75 12\.27 → 11\.71 Ginstr \(−4\.6 %\); Q11 −1\.4 %, Q4 −0\.9 %, Q39 −0\.8 %; seven others within ±0\.3 %\.
    - Next: scan deform \(decodeRowRangeInfo 15 % of Q75\) — PG attcacheoff for null\-free tuples; or Q97 / Q87\.
  - **Slice 71 — a sort read only by consumer\-complete readers stores just their columns; Gather Merge partial plans
    built for their readers; one\-pass VirtualSlot\.Materialize LANDED 2026\-10\-23\.** Movement: none — perf only\. The S3
    instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same \(also PASS=99 on a poison\-armed binary\),
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 7 "Slice 71"\.
    - Named target TPC\-DS Q97 \(warm 0\.36 vs PG 0\.11\): worker Sorts stored ~51\-cell join rows built twice per row;
      transferRowForQueueLive stalled on cache misses reading them \(20 % of worker samples\)\.
    - sortOp\.live / materializeLiveRow / finishSortNodeLive; newGatherMergeNodeFor; VirtualSlot\.Materialize one pass
      \(pinned; mutation\-checked\)\.
    - Q97 7\.65 → 7\.42 Ginstr \(−3\.0 %\), warm user CPU −5 %; Q78 −11\.1 %, Q35 −1\.9 %, Q51 −1\.8 %; Q87/Q38/Q75 flat\.
    - Rule b: slices 70 \(Q75 −4\.6 %\) and 71 \(Q97 −3\.0 %\) both ≥ 3 % → no yield\.
    - Next: narrow sort storage \(PG tuplesort MinimalTuple of the sort targetlist\) — the remaining Q97 cache misses;
      or the attcacheoff deform\.
  - **Slice 72 — a participant probing a leader\-prebuilt shared hash table takes the empty\-inner exit LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 7 "Slice 72"\.
    - Named target TPC\-DS Q61 \(warm 91 ms vs PG 16 ms\): PG's store\_sales scans return 1 row per process — the
      filtered store join is empty at SF0\.25 and ExecHashJoin quits \(totalTuples == 0 && \!HJ\_FILL\_OUTER\); goopg's
      shared\-build participant branch never checked emptyBuildEndsJoin, so workers scanned all of store\_sales\.
    - sharedHashBuild\.buildRows/buildRowsCounted \(captureSharedBuild → applySharedBuild\); shared branch takes the exit
      \(pinned with a real captured shared build; mutation\-checked\)\.
    - Q61 5\.645 → 0\.012 Ginstr, warm 91 → 3–7 ms \(below PG\); sweep 288 → 13 ms; output identical\.
    - Dropped \(flat on Q61\): conditional NULL write for skipped cells; attcacheoff cached offsets \(~40 % of SF0\.25
      store\_sales tuples are NULL\-free\)\.
    - Rule b: slices 71 \(Q97 −3\.0 %\) and 72 \(Q61 −99 %\) ≥ 3 % → no yield\.
    - Next: lazy deform before a selective hash probe \(PG slot\_getsomeattrs\), or the Parallel Hash empty exit\.
  - **Slice 73 — the Parallel Hash build\-side claim walk passes a partial Unique LANDED 2026\-10\-23\.** Movement: none —
    perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS
    \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 7 "Slice 73"\.
    - New instrument: per\-node actual\-rows census goopg vs :65438 tpcds025 \(tmp/rows\-census\.sh / \.py\) — flags
      nodes doing > 3× PG's rows\.
    - Q87/Q38 catalog branch: attachParallelHashBuildSides \(and unwrapToSetOp\) lacked the distinctOnOp arm the three
      scan attachers had — every participant fed the whole date\_dim into the shared table \(4× join rows, hidden by
      the Unique\)\. Added, PartialUnique\-gated \(pinned; mutation\-checked\)\.
    - Q87 23\.73 → 13\.11 Ginstr \(−44\.8 %\), warm 0\.633 → 0\.371 s \(PG 0\.30\); Q38 −45\.4 %; outputs identical to PG\.
    - Census leads for next slices: Q72 inventory\_pkey index scan 20×, Q67/Q51 Sorts 10×/330×, Q17/Q25/Q29
      catalog\_sales\_pkey index scan where PG reads 0 rows\.
  - **Slice 74 — a presorted, partitioned window consumes its input one partition at a time LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 7 "Slice 74"\.
    - Rows census: Q51's Sort emitted 169 297 rows into the WindowAgg where PG's emitted 506 — windowOp drained its
      whole input at Open; PG spools one partition at a time \(spool\_tuples / begin\_partition\), so LIMIT stops it\.
    - streamsPartitions \(Presorted \+ PARTITION BY\) → spoolPartition per partition with a one\-row lookahead; run
      condition skips the buffer; empty input still evaluates the frame offsets \(ExecWindowAgg all\_first\); offsets
      and the aggregate helper cached per scan \(pinned vs whole\-input over 7 window queries; early\-stop pinned;
      mutation\-checked\)\.
    - Q51 11\.91 → 9\.94 Ginstr \(−16\.5 %\), Sort now 514 rows; Q67 −4\.6 %; Q47 \+0\.8 %, Q57 \+0\.7 % \(per\-partition
      overhead\); 9 window queries' outputs identical to PG\.
    - Census triage: Q17/Q25/Q29 = EXPLAIN ANALYZE pre\-Filter row count \(filed M0122\-0017\); Q44 = WindowAgg
      run\-condition early stop still reads the sorted input to the end \(cheap\)\.
    - Discovery filed: P0\-E11 \(S2, zero\-dividend numeric scale\)\.
  - **Slice 75 — correlated references compile, and a comparison reads its operands in place LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS,
    TestPort\_Isolation PASS\. Doc part 7 "Slice 75"\.
    - Q1's SubPlan filter `ctr1\.ctr\_store\_sk = ctr\_store\_sk` runs 166 M times \(12 881 calls × 12 881 CTE rows\);
      the outer reference was an ExprAdapter and both operands were copied as 48\-byte Datums per row\.
    - OuterColumnRef / ExecParamRef → ExprOuterColumnRef / ExprExecParamRef \(PG EEOP\_PARAM\_EXEC\); a comparison
      whose operands lie in a materialized row, outer row, parameter slot or folded constant compares them in place
      \(fastOperandPtr\), gated on fastBinarySimple's static half \(without it, Q81 \+9 %\)\. Pinned vs evalExprSlot
      on 25 expressions incl\. every error path; mutation\-checked\.
    - Q1 122\.81 → 98\.99 Ginstr \(−19\.4 %\), wall 9\.63 → 6\.75 s \(PG 12\.4 s\); Q81 −4\.6 %; Q30 −4\.4 %; Q6 flat\.
  - **Slice 76 — a scan's descent stops above the leaf: one leaf visit per descent LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), nbtree race PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\.
    Doc part 7 "Slice 76"\.
    - Q72's "20× inventory\_pkey rows" census lead was the pre\-Filter count \(M0122\-0017\): 195 ≈ PG's 9\.88 returned
      \+ 191 removed\. Its skip scan makes 862k cursors for 436k date groups, matching PG's 898 676 Index Searches\.
    - descendToLowerBound pinned the leaf, ran its high\-key test and unpinned it; both callers then re\-pinned it
      and re\-ran that test\. The descent now returns the level\-1 downlink \(stopAboveLeaf\), as PG's \_bt\_search
      hands \_bt\_first one leaf buffer\. Pinned: same leaf as the full descent for every bound; leaf\-root tree\.
    - Q72 28\.53 → 27\.39 Ginstr \(−4\.0 %\), wall 0\.69 → 0\.59 s; Q17 −1\.2 %, Q25 −0\.8 %, Q54 −0\.4 %\.
    - Discovery filed: M0122\-0018 \(EXPLAIN ANALYZE TIMING OFF runs Q72 4\.4× slower than the bare query\)\.
  - **Slice 77 — one cursor per skip scan, re\-aimed per descent LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    acceptance arm PASS \(24 MATCH\), nbtree race PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\.
    Doc part 7 "Slice 77"\.
    - Target: Q72's per\-run Go\-heap allocation \(tmp/ds\-alloc\.sh, MemStats delta\)\. A fresh ScanCursor per descent
      was 49 % of allocation samples \(862k × 128 B\); an empty lower bound built an error per comparison\.
    - BTree\.ResetScanCursor re\-aims a cursor; btreeSkipEnum keeps walkSpare/enumSpare across reset \(PG's one
      BTScanOpaque per scan\); an empty bound takes the first downlink directly \(PG \_bt\_endpoint\)\. Pinned with an
      independent key oracle; mutation\-checked\.
    - Q72 192\.9 → 92\.7 MB/run \(−52 %\), 2\.39 M → 1\.41 M allocations; instructions −1\.1 %; warm wall ~0\.58 → 0\.55 s\.
    - Left: groupBoundsFixed's per\-group buffer \(~28 MB/run; needs the cursor to own a copy of lo\)\.
  - **Slice 78 — pathkey usefulness looks each key up once: TPC\-DS Q64 planning −65 % LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: fire\-set fires=none \(99/99 MATCH\), sf025
    PASS=99 \(61 ck\) PLAN\-SHAPE same, acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS,
    TestPort\_Isolation PASS\. Doc part 7 "Slice 78"\.
    - Target: Q64, the largest SF0\.25 gap vs PG \(1719 vs 260 ms\), is planning — plan\-only EXPLAIN 1\.6–2\.0 s vs PG
      Planning Time 64 ms\. Instrument: tmp/ds\-planinstr\.sh \(instructions:u per plan, plan cache busted via work\_mem\)\.
    - 70 % of planning was truncateUselessPathkeys: exprEqual formatted both keys per pair; then memo map lookups
      per pair; the class index was rebuilt per rel; every GEQO tour started an empty key memo\.
    - Each key is looked up once \(pathkeyUsefulness\.pathKeyEqual, groupMeta/distinctMeta/mergeMeta/queryMeta\);
      classIndex is the search's; freshEvalCtx shares exprKeys/ecIdx\. Oracle\-pinned; three mutations caught\.
    - Q64 18\.62 → 6\.48 Ginstr/plan \(−65 %\); Q72 −44 %, Q25 −51 %, Q18 −42 %; Q14 flat at 77\.7 G \(~5 s planning —
      next lead\)\. Plans byte\-identical\.
    - Discovery: goopg's EXPLAIN ANALYZE "Planning Time" reports 0\.17 ms for Q64's ~1\.6 s planning — filed
      M0122\-0019\.
  - **Slice 79 — a re\-expansion Project's live set reads its consumers through any expression LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 7 "Slice 79"\.
    - Target: Q43 \(208 vs PG 75 ms\)\. Its worker Sort held 121–131 MB vs PG 3\.5 MB; 72 % of instructions were the
      80\-target re\-expansion Project, evaluated in full because projectLiveSet's whitelist declined the CASE in
      sum\(CASE WHEN d\_day\_name = 'Sunday' …\)\.
    - projectLiveSet now uses consumerLiveColumns \(any expression, fail\-closed\); out\-of\-range refs and sublinks
      still keep every target\. Pinned \(CASE / sublink / out\-of\-range subtests\); mutation\-checked\.
    - Q43 −19\.3 %, Q59 −24\.9 %, Q2 −16\.9 %, Q75 −15\.2 % instructions; warm wall Q43 0\.25 → 0\.20 s, Q59 1\.30 → 0\.99 s\.
    - Correction to slice 78: Q14's "77\.7 G planning" was its second statement's execution \(two\-statement file;
      the explain prefix covers the first only\) — Q14/Q23/Q24/Q39 need per\-statement plan measurement\.
  - **Slice 80 — an index scan forms its probe key from resolved columns and re\-aims one cursor per rescan LANDED
    2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS
    \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 \(new\) "Slice 80"\.
    - Target: Q14, the largest gap vs release PG 18\.3 \(8\.9 vs 4\.8 s\)\. Same plans and rows; parameterised
      \*\_sales\_pkey rescans \(16 173 each\): lookupKey re\-resolved columns by name and formed the probe generically,
      and every rescan allocated a cursor\.
    - indexProbeForm \(colAt once; fixed\-width template fill\) in indexScanOp and indexOnlyScanOp alike; curSpare \+
      ResetScanCursor\. Byte\-identity pinned vs indexProbeKey; mutation\-checked\.
    - Q14 −4\.9 % instructions, −13 % bytes / −21 % objects allocated; Q17 −4\.5 %, Q25 −2\.9 %\.
    - Left: leafScanStart's generic comparator \(~12 % of Q14\); Q14 still allocates 4\.5 GB / 46 M objects per run\.
  - **Slice 81 — a re\-opened inner index scan keeps its batch; a set\-op drain forms one key per distinct row LANDED
    2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS
    \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 "Slice 81"\.
    - Target: Q14's per\-run allocation \(tmp/ds\-alloc\.sh\)\. Allocation\-sample attribution: scanAppendEntry growslice
      \(the lateral stream re\-opens the inner per outer row; Open/Close nil'd the TID batches\), string decode, and
      drainSetOpInput's per\-row key strings \(the left input's counts are discarded\)\.
    - Open/Close keep tids/poss capacity; drainSetOpInput\(wantCounts\) with a reused key buffer \+ key→slot index\.
      Pinned \(counts vs rowKeyTrimmed incl\. int/numeric, bpchar, NULL canonicalisation\); mutation\-checked\.
    - Q14 objects 45\.96 M → 32\.57 M per run \(−29 %\), bytes −6 %, instructions −2\.9 %; Q38/Q87 −1\.1 %\.
    - Ledger row: index scans decode dead string columns \(needs a join\-aware read set for inner\-side scans\)\.
  - **Slice 82 — the lateral stream copies the outer row's cells; an index\-only scan decodes into reused scratch
    LANDED 2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99
    \(61 ck\) PLAN\-SHAPE same, also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm
    PASS \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 "Slice 82"\.
    - Target: Q23 \(2\.6 s vs release PG 1\.3 s; 3\.9 GB / 21\.5 M objects per run\)\. Fresh census: goopg 67\.6 s vs
      release PG 85\.8 s total; Q47's 4\.8 s was a warm\-up artefact \(1\.2 s warm, instructions unchanged\)\.
    - stepOuter materialised the VirtualSlot then copied it \(appendSlotCells now\); IOS decodeRowFromKey allocated
      keyCols/names/Physical/values/nulls/vals per entry \(pgIndexTupleKeyDatumsInto \+ DeformPGIndexKeyInto with
      per\-descriptor scratch\)\. Pinned; mutation\-checked\.
    - Q23 objects −26\.5 %, bytes −12 %, instructions −2\.4 %; Q14 −2\.1 % instructions; Q72 bytes −21 %\.
  - **Slice 83 — a sort read only by a Gather Merge stores compact rows LANDED 2026\-10\-23\.** Movement: none — perf
    only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, also PASS with the
    deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\), TestPort\_RegressSuite PASS,
    TestPort\_Isolation PASS\. Doc part 8 "Slice 83"\.
    - Target: Q78 \(3\.0 GB / 5\.7 M objects per run; materializeLiveRow \+ transferRowForQueueLive on ~50\-column rows\)\.
    - compactOut \(set by newGatherMergeNodeFor\) → compact live rows, reused full\-width output row, expansion before
      the first spill; keys evaluated on the detached cells\.
    - The first cut evaluated keys on the incoming slot: TPC\-H Q22 1 510 groups instead of 7 \(acceptance ROWS\-DIFF;
      sf025 99/99 missed it\)\. substr over an arena\-backed cell aliases the arena with ArenaID 0, which
      MaterializeArena cannot detach\. Pinned with an arena\-page source; mutation\-checked\.
    - Q78 −26 % bytes, −4\.9 % instructions; Q47 −3\.9 %\. Ledger row: other sorts and the GM queue stay full width\.
  - **Slice 84 — the compiled evaluator applies AND / OR itself and compares a numeric with an int in place LANDED
    2026\-10\-23\.** Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\)
    PLAN\-SHAPE same, also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS
    \(24 MATCH\), TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 "Slice 84"\.
    - Target: boolean\-heavy numeric scan quals \(Q28 2\.9x vs release PG; Q13, Q48\)\. evalFastExpr fell to evalBinary
      for AND/OR and for numeric\-vs\-int comparisons \(fastCompareDatum declined mixed kinds\)\.
    - Kleene AND/OR inline for bool/NULL operands; fastCompareDatum numeric/int arm \(compareDatum's NUMERIC ↔ INT\)\.
      Pinned vs the interpreter; slice 27's pin amended; mutation\-checked\.
    - Q13 −9\.5 %, Q48 −6\.6 %, Q28 −2\.5 % instructions\.
  - **Slice 85 — a Gather worker shipping every column reads a join's VirtualSlot once LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\),
    TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 "Slice 85"\.
    - Target: Q10 \(2\.5x vs release PG; same plan, 1\.92 M\-row outer scan\)\. transferRowForQueue built Row\(\) then
      cloneRowOwned per shipped row for a Gather with no live mask; now one cell\-by\-cell pass into a chunk row\.
    - Q10 −19 % bytes / −7 % objects, −0\.4 % instructions; Q35 −1\.4 %\. **Below 3 %: rule \(b\) — the next slice
      must clear 3 % on its own target or M0122\-0012 yields to M0147\.**
    - Measured and dropped: skip\-arm conditional NULL store \(\+4\.7 % instructions, cycles within noise\); NL stream
      outer copy \(does not fire in Q10\)\.
  - **Slice 86 — a seq scan judges the qual's leading conjunct on its own columns LANDED 2026\-10\-23\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\), spotcheck
    PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 "Slice 86"\.
    - Target: Q28 \(2\.9x vs release PG; same plan\)\. The prefilter decoded all four qual columns \(three numerics up to
      column 19\) before judging rows that 95 % fail on `ss\_quantity` alone\.
    - scanQualLead: the first left operand on the qual's AND spine that reads fewer columns is decoded and evaluated
      alone; FALSE rejects \(the AND short\-circuit's own verdict\), anything else takes the unchanged prefilter path; a
      lead rejecting < 25 % of its first 4096 rows switches off\.
    - Q28 25\.02 → 17\.99 G instructions \(−28 %\); Q13/Q48/Q88/Q96/Q7 flat, Q9 \+0\.2 % noise\. Rule \(b\) watch resets\.
    - Open: a qual reading the last column has no prefilter, hence no lead \(possible later slice\)\.
  - **Slice 87 — a sorted aggregate carries the next group in the finalized group's runtime LANDED 2026\-10\-24\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\), spotcheck
    PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 "Slice 87"\.
    - Target: Q78 \(6\.2x warm vs release PG at cd37060e5; 2224 MB / 5\.50 M objects per run\)\. nextSorted allocated
      a groupRuntime, one 704\-byte aggRuntime per aggregate, a key row and key strings for every group; Q78's groups
      are mostly one row\.
    - Q78 −24 % bytes, −28 % objects, −3\.2 % instructions, warm wall 1\.74 → 1\.45 s; Q97 −3\.7 %, Q51 −3\.1 %
      instructions; Q67 flat\. Rule \(b\): ≥ 3 % on its target\.
    - Open: Q78 sys time ~1\.0 s/run; remaining 1\.7 GB/run \(Gather Merge transfer, sort rows, merge\-join output\)\.
  - **Slice 88 — a Gather Merge read by a borrowing aggregate ships compact rows LANDED 2026\-10\-24\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\), spotcheck
    PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, new test under \-race PASS\. Doc part 8 "Slice 88"\.
    - Target: Q78 bytes \(1683 MB/run\)\. Its sys time is page faults: under GOGC=off every allocated byte is a fresh
      page \(369 k minor faults per run\)\. Workers shipped 2\.4 KB full\-width rows with 4\-5 live cells\.
    - aggregateBorrowsGatherRows marks the Gather Merge borrowOut; liveIdx compact rows, expandLive into two scratch
      rows per source; per\-source sort\-key slices reused\.
    - Q78 −48 % bytes, sys 1\.45 → 0\.40 s, warm wall 1\.67 → 1\.41 s, −2\.9 % instructions\. Rule \(b\): ≥ 3 % on its
      target\.
  - **Slice 89 — a character\-string comparison of in\-place operands compares them where they lie LANDED 2026\-10\-24\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\), spotcheck
    PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 8 "Slice 89"\.
    - Target: Q2/Q59 \(seven char\(9\) `d\_day\_name = '<day>'` CASE arms per row\)\. The comparison copied four Datums
      through two operand calls and evalFastBinaryApply before its plain\-string branch\.
    - Q81 −17 %, Q30 −16 %, Q2 −6\.7 %, Q59 −6\.4 %, Q43 −2\.7 % instructions; Q13/Q48 \+0\.3–0\.4 %\. Rule \(b\): ≥ 3 %\.
    - Dropped: direct compact\-sort read into the Gather Merge queue \(Q78 −0\.8 % instructions, cycles flat\)\.
  - **Slice 90 — the plain\-string test and the string read inline at the comparison LANDED 2026\-10\-24\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\), spotcheck
    PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS\. Doc part 9 \(new\) "Slice 90"\.
    - Target: Q81 \(indexbytebody 26 % from plainCompareString's IndexByte on two\-letter state codes; StringValue
      not inlinable\)\. Byte\-loop plainCompareString \+ inlinable stringValueInline\.
    - Q81 −15\.1 %, Q30 −14\.3 %, Q59 −1\.9 %, Q2 −0\.6 % instructions\. Rule \(b\): ≥ 3 %\.
  - **Slice 91 — a correlated sublink over a CTE scan is lowered, so its result cache keys on the parameters LANDED
    2026\-10\-24\.**
    Movement: none — plan text byte\-identical \(census 22/22, 99/99, 99/99 MATCH; fire\-set no changed query;
    EA\-RATCHET PASS\)\. Values: Q1/Q30/Q81 sorted output == :65438; sf025 PASS=99 \(61 ck\), poison sf025 PASS;
    executor pkg with poison PASS; acceptance 24 MATCH; spotcheck PASS; RegressSuite PASS; Isolation PASS\. Doc part 9
    "Slice 91"; ledger row \(remaining unmodelled kinds; selfContainedPlan hand\-written switch\)\.
    - Target: Q81 SubPlan `calls=6893 hits=0`: the D4\.1 lowering walk had no CTEScan arm, so the sublink stayed on
      the OuterRows stack and its cache was keyed by the whole outer row\.
    - selfContainedPlan: non\-mutating body check \(lateral right / nested sublink one frame deeper, ExecParamRef
      refused\); CTEScan arm never enters the shared body\.
    - Q1 99\.2 → 1\.0 G instructions \(0\.108 s vs release PG 7\.2 s\), Q81 31\.4 → 0\.99 G, Q30 7\.25 → 0\.49 G\.
  - Post\-slice\-91 recon 2026\-10\-24 \(no code landed; doc part 9 "After slice 91"\):
    - SubPlan cache census over all 99 SF0\.25 queries: no `hits=0` SubPlan remains\.
    - Dropped: buffered stable merge sort \(Q67 −3 % instructions but \+2\.6 % serial cycles\)\.
    - Next lever, needs design: cooperative hash build producers clone every build row \(Q95 ~13 %\) that the
      consumer copies again; recycling requires a borrow\-safe transfer\.
    - Ledger: EXPLAIN ANALYZE actual time prints loop totals, PG prints per\-loop averages\.
  - **Slice 92 — cooperative hash build producers borrow their scan and fill recycled batch blocks LANDED 2026\-10\-24\.**
    Movement: none — perf only\. The S3 instrument is value/plan identity: sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same,
    also PASS with the deform poison armed; executor pkg with poison PASS; acceptance arm PASS \(24 MATCH\), spotcheck
    PASS, TestPort\_RegressSuite PASS, TestPort\_Isolation PASS, \-race on the coop tests PASS\. Doc part 9 "Slice 92"\.
    - Target: Q95 \(1534 MB/run\)\. Producers cloned every build row \(their tree never borrowed\) and the consumer
      copied it again \(retainBuildRow\)\.
    - coopBorrowScan admits the bare scan or a column\-only Project over it; rows are copied into coopBatch blocks the
      channelSource hands back on a free list once past the batch\.
    - Q95 −21\.5 % bytes, −8 % objects, ws\_wh build −27 to −48 %; end\-to\-end within noise; Q4 −2\.0 %, Q59
      −1\.2 % instructions\. Rule \(b\): ≥ 3 % on its target \(bytes\)\.
- [x] **M0122-0016 — every big\-mantissa numeric result is written to the never\-reset permanent arena \(unbounded growth \+ a process\-wide lock\)**\.
  Kind: impl
  Parent: M0122-0012
  - Filed 2026\-10\-23 from M0122\-0012 slice 68 \(found while trimming numericDiv's big\.Int path for TPC\-DS Q39\)\.
  - `newNumeric` → `newBigNumericInCtx\(mmgr\.Perm\(\), …\)` stores the sign byte \+ magnitude of every numeric whose
    mantissa passes int64 in the process\-global permanent context, which `mmgr\.Perm` documents as "never reset or
    released"\. Measured: 100 000 big\-lane divisions grew `Perm\(\)\.Usage\(\)` by 1 179 648 bytes \(~12 B per result\)\.
    Q39 alone produces on the order of a million such results per run \(16\-decimal avg / stddev / their quotient\)\.
  - Every such write also takes `permMu` \(a package\-level RWMutex, write\-locked per allocation\), so parallel
    workers producing big numerics serialise on one lock\.
  - PG: a numeric result is palloc'd in the current memory context \(per\-tuple / per\-aggregate\), freed when that
    context resets \(`make\_result`, `numeric.c`\); nothing accumulates across statements\.
  - Resume point: `internal/executor/numeric.go` newNumeric / `datum.go` newBigNumericInCtx — allocate the payload in
    the statement's or expression's context and detach it at the retention boundaries that already exist for
    arena\-backed strings \(`Datum.MaterializeArena`, `cloneRowOwned`, `transferRowForQueue*`\), then drop the
    "big numerics live in Perm" assumption that `AssertTransferable`'s comment records\.
  - Gate: an allocation probe over repeated statements \(Perm usage flat\), the executor package with the deform
    poison, sf025 \(numeric values, 61 ck\), RegressSuite \(numeric\.sql, aggregates\.sql\)\.
  - **LANDED 2026\-10\-23\.** Movement: none — resource fix\. Doc part 6 "M0122\-0016"\.
    - NewNumericBigDatum builds an owned payload \(Datum\.Buf, ArenaID 0, Int = payload length\); bigNumericPayload
      reads owned or arena payloads for NumericBigValue / numericMagUint64; newNumeric, unary minus and
      MaterializeArena produce owned values; newBigNumericInCtx stays as the arena\-backed constructor \(dense builds\)\.
    - TestCloneRowOwnedStillAssertTransferable re\-pinned to the owned contract \(it pinned the Perm one on purpose\)\.
    - Pins: Perm usage flat over 20 000 big\-lane divisions \+ additions; owned round trip incl\. arena detach after
      reset; restoring the Perm write fails both\.
    - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) PLAN\-SHAPE same, RegressSuite, Isolation,
      executor package with the deform poison — all PASS\. Q39 −0\.4 %, Q4 −1\.4 % instructions\.
    - Q39's "bimodal" wall = a fresh server's heap growing to its memory limit \(~300 K page faults/run for 7 runs\);
      steady state 1\.09–1\.11 s\.
    - Left: `internal/utils/mmgr/mctx.go`'s permanentContext comment still says big numerics allocate there —
      fix in the next executor slice \(a comment\-only change after the gates would have invalidated their stamps\)\.
- [x] **M0122-0017 — EXPLAIN ANALYZE counts an index scan's rows before its own Filter**\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(EXPLAIN ANALYZE text fidelity; census 22/22, regress outputs unchanged\)
  Parent: none
  - Done 2026\-10\-27 \(selected as item 10's "rest of M0122": M0122\-0012 yields, every M0147 child is done or held\)\.
    Cause: the witness is a parameterised NL's inner probe whose residual conjunct EXPLAIN moves onto the scan's
    `Filter:` \(M0146\-0005aj\) while goopg still evaluates it at the join, so the scan's rowsOut is pre\-filter\.
    `walkPlanAnalyzeFiltered` now subtracts the probe's attributed rejections on that branch\. Design doc
    `docs/design/0100\-0149/m0122\-0017\-param\-probe\-actual\-rows\.md`\.
  - Test `TestExplainAnalyzeParamProbeRowsAfterMovedFilter`: `Index Scan … \(actual rows=0\.09 loops=100\)` / `Rows
    Removed by Filter: 0` — identical to scratch PG 18\.3 for the same NL shape\.
  - JSON/YAML/XML sibling gap ledgered \(2026\-10\-27\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, TPC\-H census on tree 22/22,
    RegressSuite PASS \(81 mismatches; plpgsql flapper only\), Isolation PASS; fire\-set out of scope \(executor only\)\.
  - Filed 2026\-10\-23 from the M0122\-0012 slice\-73/74 per\-node rows census \(tmp/rows\-census\.sh\)\.
  - TPC\-DS Q17 / Q25 / Q29: `Index Scan using catalog\_sales\_pkey … Filter: \(cs\_bill\_customer\_sk = …\)` reports
    `actual rows=25\.63 loops=7511` with `Rows Removed by Filter: 26`; PG reports `rows=0\.00` — PG counts the tuples the
    node returns \(InstrStopNode after ExecScan's qual\), goopg counts them before the Filter\.
  - Resume point: the instrumentation wrapper around an index scan with a pushed\-down Filter \(maybeInstrument
    placement vs the filterOp the builder adds\) — count emitted rows, keep Rows Removed by Filter as is\.
- [x] **M0122-0018 — EXPLAIN \(ANALYZE, TIMING OFF\) runs TPC\-DS Q72 4\.4× slower than the bare query**\.
  Kind: impl
  Parent: M0122-0012
  Movement: none — premise stale: the 3\.04 s was a once\-per\-server stall, not EXPLAIN overhead\.
  - **Closed 2026\-10\-23 \(loop \#118\)\.** Warm, on one server \(`7050dc003`\): bare 0\.58–0\.62 s, `EXPLAIN \(ANALYZE,
    TIMING OFF\)` 0\.60–0\.67 s \(\+7 %; instructions 27\.39 vs 27\.64 G\)\. The filed 3\.04 s was the server's first
    EXPLAIN run\.
  - A ~3\.4 s run recurs ONCE per server lifetime, bare or EXPLAIN alike, when RSS first crosses ~5 GB \(4 948 /
    5 058 MB in two series\)\. gctrace shows the first GC only at 11\.4 GB, 10 s later — not GC\. Reading: the
    WSL2 VM committing fresh host memory on first touch \(an environment stall\)\.
  - What drives RSS there is real: Q72 allocates ~202 MB and 2\.4 M objects per run \(MemStats TotalAlloc /
    Mallocs over 5 runs\), and GOGC=200 over a 2\.8 GB live heap \(2 GB buffer\-pool arena\) lets it pile to ~11 GB
    before the first GC\. Lead for the next M0122\-0012 slice\.
  - Filed 2026\-10\-23 from M0122\-0012 slice 76\. On the SF0\.25 clone \(binary `88998a861`\) Q72 takes 0\.69 s bare
    and 3\.04 s under `EXPLAIN \(ANALYZE, COSTS OFF, TIMING OFF\)`; PG's InstrStartNode/InstrStopNode without
    timing only bump counters \(instrument\.c\), and PG's own EXPLAIN ANALYZE of Q72 takes 0\.87 s\.
  - Q72 is a 9\-deep parameterised nested loop in 4 parallel participants: ~1M inner rescans of index scans, so a
    per\-rescan or per\-node\-start instrumentation cost multiplies\. Every per\-node timing a slice reads off EXPLAIN
    ANALYZE on such a plan is distorted until this is understood\.
  - Resume point: profile the EXPLAIN ANALYZE run vs the bare run \(tmp/ds\-iprof\.sh with the `explain \(analyze,
    timing off\)` file\); look at the instrumentation wrapper \(maybeInstrument\) on rescans and in parallel workers\.
- [x] **M0122-0020 — EXPLAIN ANALYZE prints a looped node's actual time as the total over loops, PG prints the per\-loop average**\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(EXPLAIN ANALYZE time fidelity; census 22/22, sf025 plans 99/99 same\)
  Parent: none
  - Done 2026\-10\-27 \(loop \#185, item 10's "rest of M0122"\)\. All four `actual time` renderers \(text label, Hash
    line, Worker line, JSON\) divide by loops via `msPerLoop`, as explain\.c does\. Testing exposed a second defect:
    `instrumentedOp` timed the gap between returns \(the parent's work included\), skipped the EOF call, and kept one
    loop's startup, so a child out\-timed its parent\. It now times only its own Open/Next calls and sums startup per
    loop \(InstrEndLoop\)\. Test `TestExplainAnalyzeActualTimePerLoop`\. Design doc
    `docs/design/0100\-0149/m0122\-0020\-actual\-time\-per\-loop\.md`\. JSON `Node Type` label gap ledgered\.
  - Filed 2026\-10\-24 from M0122\-0012 post\-slice\-91 recon\. TPC\-DS Q14 on the SF0\.25 clone:
    `Index Scan using store\_sales\_pkey … \(actual time=0\.003\.\.1857\.725 rows=42\.54 loops=16173\)` — rows is the
    per\-loop average but time is the total; PG's ExplainNode \(explain\.c\) divides startup and total time by nloops\.
  - Resume point: internal/executor/operators\_explain\.go, the actual\-time renderer: divide by loops as explain\.c
    does; ledger row 2026\-10\-24 \(post slice 91 recon\)\.
- [x] **M0122-0019 — EXPLAIN ANALYZE "Planning Time" omits the join search: TPC\-DS Q64 reports 0\.17 ms for ~1\.6 s**\.
  Kind: impl
  Movement: none — correctness fix — no plan instrument \(EXPLAIN summary fidelity; census 22/22, sf025 plans 99/99 same\)
  Parent: none
  - Done 2026\-10\-27 \(loop \#186, item 10's "rest of M0122"\)\. Cause: the timer wrapped executor `buildNode` \(PG's
    ExecInitNode, an Execution Time item\), never the optimizer, which ran earlier in the planner's ExplainStmt arm\.
    The planner now stamps its inner `PlanWithSettings` duration on `optimizer.Explain.PlanningNs`; `buildNode` moved
    into Execution Time; plain `EXPLAIN \(SUMMARY ON\)` prints Planning Time as PG does\. Live Q64 on the SF0\.25
    clone: Planning 836/843 ms, Execution 632/315 ms, wall 1\.47/1\.16 s\. Test
    `TestExplainPlanningTimeIsPlannerDuration`\. Design doc
    `docs/design/0100\-0149/m0122\-0019\-planning\-time\-is\-planner\-duration\.md`\. Analysis\+rewrite inside the
    timer ledgered\.
  - Filed 2026\-10\-23 from M0122\-0012 slice 78\. On the SF0\.25 clone `EXPLAIN \(ANALYZE\)` of Q64 prints
    `Planning Time: 0\.169 ms` while a plan\-only EXPLAIN of the same text takes 1\.6–2\.0 s wall \(profile: 96 % under
    optimizer\.PlanWithSettings → geqoSearch\); PG prints its whole planner time \(ExplainOnePlan's planduration,
    measured around pg\_plan\_query\)\.
  - Resume point: where the EXPLAIN path starts its planning timer relative to optimizer\.PlanWithSettings \(the
    CTE pre\-planning `preplanWithClause` → join search may run before the timer, or the figure is taken from a
    cached/second plan\)\.
- [ ] **M0122-0013 — Physical/streaming replication & standby**.
  - **Standby restartpoint LANDED 2026\-10\-27 \(loop \#195, the M0122\-0012 one\-task yield\)**\. Movement: none —
    recovery path\. `StreamReplayer` records the last replayed checkpoint \(PG RecoveryRestartPoint\);
    `runRecoveryFlush` snapshots it before the flush and `adoptRestartPoint` then advances pg\_control's
    checkPoint/checkPointCopy when its redo is older, minRecoveryPoint only in DB\_IN\_ARCHIVE\_RECOVERY
    \(CreateRestartPoint\)\. A restarted standby now replays from its last restartpoint\.
    - Pin: `TestE2E\_PhysicalStandbyRestartPointAdvancesPgControl` \(standby pg\_control equals the primary's
      checkpoint after a standby CHECKPOINT; restart keeps streaming\); FAILS with the wiring removed \(redo
      unchanged 1016F80\)\. Units: `TestDecodeCheckPointStructRoundTrips`, `TestAdoptRestartPointAdvancesOnlyForward`,
      `TestRecoveryFlushAdoptsReplayedCheckpoint`\.
    - Residual \(ledgered\): no DB\_IN\_ARCHIVE\_RECOVERY stamp on a standby \(minRecoveryPoint arm dormant\), no
      DB\_SHUTDOWNED\_IN\_RECOVERY on a shutdown restartpoint, no restartpoint\-driven WAL removal\.
    - Design: `docs/design/0000\-0049/0005\-0001\-streaming\-replication\-architecture\.md` §"The restartpoint"\.
  - **Standby pg\_control state LANDED 2026\-10\-27 \(loop \#201, the second M0122\-0012 yield\)**\. A standby now stamps
    DB\_IN\_ARCHIVE\_RECOVERY before replay and at the end of Open \(it stamped DB\_IN\_CRASH\_RECOVERY then
    DB\_IN\_PRODUCTION\), a shutdown restartpoint leaves DB\_SHUTDOWNED\_IN\_RECOVERY \(both CreateRestartPoint arms\),
    and promotion stamps DB\_IN\_PRODUCTION\. The restartpoint's minRecoveryPoint arm is live\. Pin: the restartpoint
    E2E asserts all four states; units `TestBeginRecoveryStandbyIsArchiveRecovery`,
    `TestShutdownRestartPointStampsShutdownedInRecovery`\. Residual: restartpoint WAL removal, skip\-arm
    UpdateMinRecoveryPoint \(ledgered\)\. Design: 0005\-0001 §"A standby's pg\_control state"\.
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

- [ ] **M0131-S33 — nbtree: the first item of a split\-created internal page keeps its key \(PG truncates it to minus infinity\)**\.
  Kind: impl
  Parent: none
  - Filed 2026\-10\-22 from M0122\-0012 slice 61 \(discovery while testing the root cache\)\.
  - Repro: an int4 test tree grown past three levels by incremental inserts \(`TestInsertSplitProducesPivotTuples` shape,
    ~60 000 keys\) fails `walkPivotInvariants` with "block N \(internal\) item 0 natts = 1, want 0" — the cache disabled,
    so it predates slice 61; the existing pivot tests only build two\-level trees and never reach it\.
  - PG: `\_bt\_split` \(nbtinsert\.c\) makes the right internal page's first data item a zero\-attribute pivot \(its
    key is minus infinity; only its downlink matters\), so every internal page's first item has natts 0\.
  - Impact: searches still route correctly \(keys stay ordered and goopg's binary search treats the item as a key\),
    but the on\-disk page differs from PG's and PG's amcheck checks this invariant\.
  - Resume: internal\-page split in internal/access/nbtree/btree\.go \(the right half's first item\) — truncate it with
    `BTreeTupleSetNAtts\(raw, 0, false\)` as the leftmost root item already is; then grow
    `TestInsertSplitProducesPivotTuples` past three levels\.
  - S7: correctness/format only, no plan\-instrument rationale → queues below item 10\.
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
*Note 2026\-10\-26 \(M0147\-0016\): the "default\-OFF" above was true at filing; `GOOPG_GATHER_PATHS` is default
`all` today \(`scripts/planner\-flags.env`: `unset\(all\)`\)\. K80's arm is live\.*

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
- [x] **M0141-S3 — Partial-Sorted row emission** — a second Partial-mode code
  path (`Strategy = AggStrategySorted`) emitting real rows (group key + one
  serialized-state column per aggregate) instead of merging into
  `aggPartialAccum`; reuses `combineAggRuntime`'s existing rules; leaves the
  hash-strategy accumulator path untouched. **Entry gate: needs S1/S2's live
  measurement to show a TPC-DS-specific, genuinely parallel-shaped residual
  large enough to justify it.** **Adopted by M0146-0003 (owner disposition
  2026-09-23): this chain runs under M0146, gated on the M0145-0008 flip —
  do not select standalone before it.** Closed 2026-10-16 (owner): the row
  stays superseded — the chain runs under M0146-0003, no standalone work
  remains here.
- [x] **M0141-S4 — `aggRuntime` serialize/deserialize** — PG's
  `aggserialfn`/`aggdeserialfn`, bounded to the decomposable whitelist's actual
  pointer surface (`numericSum`, `intSx`/`intSxx *big.Int`,
  `numericSx`/`numericSxx *big.Rat`, `userState`, the float/bool/count/sum
  scalars — `DISTINCT`/`WITHIN GROUP`/`array_agg`/`string_agg` are already
  refused by `AggregateIsDecomposable`). Needs S3 (defines the transport
  shape). Runs under M0146-0003, gated on the M0145-0008 flip. Closed
  2026-10-16 (owner): superseded — runs under M0146-0003.
- [x] **M0141-S5 — GatherMerge-fed Finalize-Sorted** — a new merge-combine
  executor operator consuming the key-interleaved stream `GatherMerge`
  produces from S3/S4's rows, folding same-key runs across workers via
  `combineAggRuntime` before finalizing (today's Finalize path only handles
  "drain the Gather to EOF, read the whole accumulator", which is wrong for a
  merge-ordered stream where a key can recur). Needs S3+S4. Runs under
  M0146-0003, gated on the M0145-0008 flip. Closed 2026-10-16 (owner):
  superseded — runs under M0146-0003.
- [x] **M0141-S6 — wire and measure** — pick which of the two existing
  producers (`parallel.go`'s legacy pass, or `partialaggupper.go`'s costed-path
  version — note its own unresolved 7/22-vs-12/22 regression against the
  legacy pass, out of scope to fix here) hosts the new shape; fix the
  `Aggregate`/`GroupAggregate (N keys)` EXPLAIN mislabel (doc
  `parallel-query/06` §4.1 — both currently render as a hash aggregate
  regardless of `Strategy`); re-measure the full corpus. Needs S5. Runs
  under M0146-0003, gated on the M0145-0008 flip. Closed 2026-10-16
  (owner): superseded — runs under M0146-0003.
- [x] **M0141-S7 — re-adjudicate and implement Incremental Sort** — **verified
  (completed sub-entries archived → `completed_milestones/completed_fix_plan_015.md`)
  - [ ] **M0141-S7-exec-d (deferred, ledger row filed 2026-09-17)** —
    Kind: impl
    **Note 2026\-10\-24 \(M0147\-0011\):** packed\-tuple retention \(`GOOPG\_SORT\_PACKED`\) is retired and no
    longer part of this task's scope \(AGENT\.md R6/B1\); the remaining items are spill, ctid passthrough and
    per\-group SortStat\.
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
  - **Re\-measured 2026\-10\-26 \(banner OWNER DECISIONS 2026\-10\-21 authorisation\), still held \[\!\]\.** Census
    `routing\-20261026f` is PLAN\-PARITY 22/22, 99/99, 99/99 — and PG\'s own Q4 plan, matched exactly, ALSO nests loops
    over filtered `CTE Scan on year\_total` inners \(rows=10/30 estimates\), so the escalation\'s "PG hash\-joins them"
    premise is stale: the join half of the release test now holds by plan identity\.
    - Strict arm \(`GOOPG\_NL\_BARE\_REEXEC=1`, SF0\.25 sweep, results `tmp/m0146\-0010a\-strict/`, log
      `/tmp/strict\-sf025\.log`\): 54/99 ran before the sweep aborted on a server restart race after a forced shutdown;
      **Q4, Q14 and Q54 TIMEOUT** \(300 s\); Q11 now PASS \(21 s\), Q5 9 s, Q7 3 s, Q31 5 s\.
    - The live blocker is therefore EXECUTOR\-side: on the PG\-identical plan, goopg\'s strict re\-execution of the
      bare `CTE Scan` inner is far costlier than PG\'s rescan of the CTE\'s already\-filled tuplestore
      \(nodeCtescan\.c `ExecReScanCteScan` re\-reads the shared tuplestore; it never re\-runs the CTE\)\. Next step:
      EXPLAIN ANALYZE Q4 under the strict arm on a private SF0\.25 clone and compare per\-node `loops`/rows with
      :65438 tpcds025; check whether goopg\'s CTEScan rescan re\-runs or re\-copies the CTE producer\.
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
  - S9 re\-check 2026\-10\-16 — **exhausted at `378ad9ecf`** \(M0122\-0012 slice 5 changed internal/executor\)\.
    Census `routing\-20261016p` uses a fresh TPC\-H capture plus the fire\-set\-s5 candidate captures\. The records are
    byte\-identical to `routing\-20261016o` \(20/22, 84/99, 73/99\)\. No task is filed; the gates are unchanged\.
  - S9 pass 2026\-10\-16 at `63f48ac7c` \(post\-corpus\-VACUUM, fresh captures of all three corpora\) — **filed
    M0146\-0213** \(scan\-type, TPC\-H Q16\); `routing\-20261016q/ROUTING.md`\. Matches: TPC\-H 21/22, SF0.25 86/99, SF1
    74/99\. The VACUUM resolved TPC\-H Q13, SF0.25 Q23/Q84 and SF1 Q68.
  - S9 pass 2026\-10\-16 — **exhausted at `ca138d51c`**\. Census `routing\-20261016q` is still fresh: since its capture
    sha `63f48ac7c`, only `internal/access/nbtree` changed and no corpus changed. Its only actionable record, TPC\-H Q16,
    is now owned by the live `[!]` M0146\-0213, which awaits an owner rebuild of `partsupp\_pk`. Open items\-0\-9 tasks
    and their gates:
    - M0146\-0014\(channel\), M0146\-0042\(channel\), M0146\-0213\(owner\-rebuild partsupp\_pk\)\.
    - M0146\-0007/0012b/0067/0175\(below\-item\-10\)\.
    - M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0196\(below\-item\-10\), M0146\-0205\(below\-item\-10\)\.
    - M0141\-S2b\-3b/S2b\-4e/S7\-exec\-d\(below\-item\-10\), M0141\-S2b\-4\(blocked\-on M0141\-S2b\-4e\)\.
    - M0142\-0008a\-3/0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\.
  - S9 pass 2026\-10\-16 — **exhausted at `774354fe1`**\. Census `routing\-20261016r` is a fresh capture of all three
    corpora after the owner's `:65433` index rebuild: **TPC\-H 22/22** \(Q16 MATCH\), SF0.25 86/99, SF1 74/99\. The TPC\-DS
    records are byte\-identical to `routing\-20261016q` and all waived\. Open items\-0\-9 tasks and their gates:
    M0146\-0014\(channel\), M0146\-0042\(channel\); M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/
    0166/0167/0168/0173/0205, M0141\-S2b\-3b/S2b\-4e/S7\-exec\-d\(below\-item\-10\); M0141\-S2b\-4\(blocked\-on M0141\-S2b\-4e\);
    M0142\-0008a\-3/0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `b5ffee1f9`** \(M0122\-0012 slice 11 changed internal/executor\)\. Census
    `routing\-20261016s` is a fresh capture of all three corpora; its records are byte\-identical to `routing\-20261016r`
    \(22/22, 86/99, 74/99\)\. No task is filed; the gates are unchanged\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `ddc40a35c`** \(M0122\-0012 slice 12 changed internal/executor\)\. Census
    `routing\-20261016t` is a fresh capture of all three corpora; its records are byte\-identical to `routing\-20261016s`
    \(22/22, 86/99, 74/99\)\. No task is filed; the gates are unchanged\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `7fc2a5bee`** \(M0122\-0012 slice 13 changed internal/executor\)\. Census
    `routing\-20261016u` is a fresh capture of all three corpora; its records are byte\-identical to `routing\-20261016t`
    \(22/22, 86/99, 74/99\)\. No task is filed; the gates are unchanged\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `f28bad61d`** \(M0122\-0012 slice 14 changed internal/executor\)\. Census
    `routing\-20261016v` is a fresh capture of all three corpora; its records are byte\-identical to `routing\-20261016u`
    \(22/22, 86/99, 74/99\)\. No task is filed; the gates are unchanged\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `8883a6145`** \(M0122\-0012 slice 15 changed internal/executor\)\. Census
    `routing\-20261016w` is a fresh capture of all three corpora; its records are byte\-identical to `routing\-20261016v`
    \(22/22, 86/99, 74/99\)\. No task is filed; the gates are unchanged\.
  - S9 re\-check 2026\-10\-16 — **exhausted at `3e6a14e3a`** \(M0122\-0012 slice 16 changed internal/executor\)\. Census
    `routing\-20261016x` is a fresh capture of all three corpora; its records are byte\-identical to `routing\-20261016w`
    \(22/22, 86/99, 74/99\)\. No task is filed; the gates are unchanged\.
  - S9 pass 2026\-10\-20 — **exhausted at `04839ab7f`**\. Census: the M0146\-0242 fire\-set and TPC\-H captures on HEAD's
    code tree \(`analysis/m0146/m0146\-0242/fireset/`, `analysis/m0146/m0146\-0242/tpch/`\): TPC\-H 22/22, TPC\-DS SF0\.25
    99/99, SF1 99/99, every CATEGORIES\-EXCL\-MATCH category 0\. No record is non\-MATCH, so nothing is filed\. Open
    items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\); M0146\-0007/0012b/0067/0175,
    M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239\(below\-item\-10\);
    M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `b084797b2`** \(`44cf7d275` changed internal/executor: wait\-event
    reporting only\)\. Census `analysis/m0146/m0146\-0014/routing\-20261021a/` is a fresh capture of all three corpora:
    TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every CATEGORIES\-EXCL\-MATCH category 0\. No task is filed\. Open
    items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\); M0146\-0007/0012b/0067/0175,
    M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239\(below\-item\-10\);
    M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `250e20249`** \(M0119 pg\_relation\_filepath changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021b/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `943cbd7b1`** \(AI\-20261021\-010544\-001 recovery\-LSN builtins changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261021c/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1
    99/99, every category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\),
    M0146\-0042\(channel\); M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/
    0205/0238/0239\(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for
    this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `b5832b221`** \(M0119 VM WAL slice 2 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021d/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `a9a9b1f55`** \(M0122\-0012 slice 18 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021e/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `2181e6060`** \(M0122\-0012 slice 19 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021f/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `54a37edcd`** \(M0122\-0012 slice 20 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021g/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `022774301`** \(M0122\-0012 slice 21 changed internal/optimizer and internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021h/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `f24bff055`** \(M0122\-0012 slice 22 changed internal/optimizer and internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021i/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `4dba3357b`** \(M0122\-0012 slice 23 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021j/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `19610b0a5`** \(M0122\-0012 slice 24 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021k/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `493de93b5`** \(M0122\-0012 slice 25 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021l/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `7909e3796`** \(census reused: 7909e3796 touched only internal/utils/mmgr, not an S9 trigger path\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021l/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `169a92964`** \(M0122\-0012 slice 27 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021m/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `d857ea0b9`** \(M0122\-0012 slice 28 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021n/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `c53226b14`** \(M0122\-0012 slice 29 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021o/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `5fcdbb6e0`** \(M0122\-0012 slice 30 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021p/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `836273d83`** \(census reused: 836273d83 is documentation\-only\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021p/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `b66b68ed5`** \(census reused: b66b68ed5 touched only internal/access/nbtree, not an S9 trigger path\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021p/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `59a326fc7`** \(M0122\-0012 slice 32 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021q/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `b24046088`** \(M0122\-0012 slice 33 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021r/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `434b23349`** \(M0122\-0012 slice 34 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021s/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-21 — **exhausted at `7f5ce4a07`** \(M0122\-0012 slice 35 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261021t/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `f39f376e9`** \(M0122\-0012 slice 36 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022a/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `b6e96eb84`** \(M0122\-0012 slice 37 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022b/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `043bddd4c`** \(M0122\-0012 slice 38 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022c/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `c3c0e3b2d`** \(census reused: c3c0e3b2d touched only internal/testport, not an S9 trigger path\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022c/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `1becea6e4`** \(M0122\-0012 slice 39 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022d/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `8bbcba80e`** \(M0122\-0012 slice 40 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022e/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `a0a569de7`** \(census reused: a0a569de7 touched only internal/access/nbtree, not an S9 trigger path\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022e/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `62b168220`** \(M0122\-0012 slice 42 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022f/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `a410e5cf8`** \(M0122\-0012 slice 43 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022g/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `8711a1938`** \(M0122\-0012 slice 44 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022h/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `984b3cb89`** \(M0122\-0012 slice 45 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022i/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `f78898a0d`** \(M0122\-0012 slice 46 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022j/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `b57f3f698`** \(M0122\-0012 slice 47 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022k/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `18b8ea331`** \(M0122\-0012 slice 48 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022l/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `5bb2a9977`** \(M0122\-0012 slice 49 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022m/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `0928e85b3`** \(M0122\-0012 slice 50 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022n/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `b535f1ec5`** \(M0122\-0012 slice 51 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022o/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `48af8abcc`** \(M0122\-0012 slice 52 changed internal/executor and nbtree\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022p/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `7e6b592d1`** \(M0122\-0012 slice 53 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022q/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `de41f8c20`** \(M0122\-0012 slice 56 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022r/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `49da13013`** \(M0122\-0012 slice 57 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022s/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `0f365344a`** \(M0122\-0012 slice 58 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022t/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `5d6e91613`** \(M0122\-0012 slice 59 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022u/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-22 — **exhausted at `fdb607c10`** \(M0122\-0012 slice 60 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261022v/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `c6a780f7e`** \(M0122\-0012 slice 62 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023a/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `ecbfe6696`** \(M0122\-0012 slice 63 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023b/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `d164c7319`** \(M0122\-0012 slice 64 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023c/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `d4a855738`** \(M0122\-0012 slice 65 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023d/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `3a50b12ed`** \(M0122\-0012 slice 66 changed internal/executor and internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023e/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `b3f708aee`** \(M0122\-0012 slice 67 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023f/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `97086429c`** \(M0122\-0012 slice 68 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023g/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `32a6ac752`** \(M0122\-0016 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023h/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `606717ffc`** \(M0122\-0012 slice 69 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023i/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `cabdb6039`** \(M0122\-0012 slice 70 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023j/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `2efa8fe12`** \(M0122\-0012 slice 71 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023k/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `d92a36e6f`** \(M0122\-0012 slice 72 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023l/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `cb3e7e6b6`** \(M0122\-0012 slice 73 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023m/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `f7dc0cc55`** \(M0122\-0012 slice 74 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023n/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `88998a861`** \(M0122\-0012 slice 75 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023o/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `a5693f85c`** \(M0122\-0012 slice 77 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023p/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `a53a0c466`** \(M0122\-0012 slice 78 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023q/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `71a749a8e`** \(M0122\-0012 slice 79 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023r/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `afdfe9869`** \(M0122\-0012 slice 80 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023s/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `e9c61e18b`** \(M0122\-0012 slice 81 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023t/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `233658626`** \(M0122\-0012 slice 82 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023u/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `a05a55628`** \(M0122\-0012 slice 83 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023v/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `e9ba4cf5f`** \(M0122\-0012 slice 84 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023w/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `44017260b`** \(M0122\-0012 slice 85 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023x/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-23 — **exhausted at `cd37060e5`** \(M0122\-0012 slice 86 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261023y/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `127b21ea8`** \(M0122\-0012 slice 87 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261024z/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `8c2d59d66`** \(M0122\-0012 slice 88 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261024a/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `e8bf90456`** \(M0122\-0012 slice 89 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261024b/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `4cf358ae6`** \(M0122\-0012 slice 90 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261024c/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `8532a71e3`** \(M0122\-0012 slice 91 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261024d/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **census reused at `a5ba2cb39`** \(docs\-only commit; routing\-20261024d's capture sha
    `8532a71e3` is an ancestor and no planner/executor/cost path changed since\)\. Same items\-0\-9 tokens as the
    `8532a71e3` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `2e8519eff`** \(M0122\-0012 slice 92 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261024e/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9\(owner\-placement\);
    P0\-E10\(owner\-placement\); P0\-E11\(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **census reused at `652989a37`** \(docs\-only filing commit; routing\-20261024e's capture
    `2e8519eff` is an ancestor\)\. Same items\-0\-9 tokens plus P0\-E12\(owner\-placement\)\. Item 10 is live; M0122\-0012
    yielded under rule \(a\) \(no runnable slice after profiling Q14/Q23/Q67\) → M0147\-0010\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `1f05de2c5`** \(M0147\-0010 changed internal/executor \+ storage\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261024f/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Open items\-0\-9 tasks and their gates: M0146\-0014\(channel\), M0146\-0042\(channel\);
    M0146\-0007/0012b/0067/0175, M0146\-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205/0238/0239
    \(below\-item\-10\); M0142\-0008c\-1a/0008c\-3d/0008c\-4\(blocked\-on M0145\-0003/0010\); P0\-E9/E10/E11/E12
    \(owner\-placement\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `b63f03791`** \(M0147\-0011 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261024g/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-24 — **exhausted at `50ecb096b`** \(M0147\-0012 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261024h/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-25 — **exhausted at `1a41444e5`** \(M0147\-0013 `483b19112` changed internal/optimizer;
    `1a41444e5` is analysis\-only\)\. Census `analysis/m0146/m0146\-0014/routing\-20261025a/`: TPC\-H 22/22, TPC\-DS
    SF0\.25 99/99, SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\.
    Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-25 — **exhausted at `033b6c14d`** \(M0147\-0014 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261025b/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `51326adf2`** \(M0147\-0015 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026a/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `f5efcefdf`** \(M0147\-0016 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026b/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `9c3e12d47`** \(M0147\-0017 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026c/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `a4c64c462`** \(M0147\-0020 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026d/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `aafabf804`** \(M0147\-0021 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026e/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `4f7b216cd`** \(M0147\-0022 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026f/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **census reused at `e5f10324e`** \(analysis/fix\_plan\-only recon commit;
    routing\-20261026f\'s capture `4f7b216cd` is an ancestor, no planner/executor/cost path changed\)\. Same items\-0\-9
    tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `63d2ab399`** \(M0147\-0031 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026g/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `8ab965464`** \(M0147\-0032 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026h/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `07d3ad3e9`** \(M0147\-0033 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026i/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `a16b71cdf`** \(M0147\-0034 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026j/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `7608c9ff0`** \(M0147\-0035 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026k/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `eff6e2294`** \(M0147\-0036 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026l/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `f8a8392fd`** \(M0147\-0037 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026m/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `529af8c85`** \(M0147\-0038 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026n/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-26 — **exhausted at `61b3b0dbe`** \(M0147\-0039 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261026o/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `16101a6a1`** \(M0147\-0040 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261027a/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `6c45b63bd`** \(M0147\-0041 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261027b/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `77d4c33b6`** \(M0147\-0042 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261027c/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `abd93c55a`** \(M0147\-0043 changed internal/optimizer\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261027d/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `dd246d001`** \(M0147\-0044 changed internal/optimizer \+ executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027e/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `637799d73`** \(M0147\-0045 changed internal/optimizer \+ executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027f/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `3a8bfaa96`** \(M0147\-0046 changed internal/optimizer \+ executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027g/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `b2665b45c`** \(M0147\-0047 changed internal/optimizer \+ executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027h/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `5a9b2fb84`** \(M0147\-0049 changed internal/optimizer \+ executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027i/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `b7d73466c`** \(M0147\-0053 `54223610d` changed internal/optimizer \+
    executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027j/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99,
    every category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `b7da10d8d`** \(M0147\-0051 changed internal/optimizer \+ executor \+
    postmaster\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027k/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99,
    every category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `e8367f901`** \(M0122\-0017 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261027l/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `da7f0c117`** \(M0122\-0020 changed internal/executor\)\. Census
    `analysis/m0146/m0146\-0014/routing\-20261027m/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every category 0\.
    No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `102bc10a4`** \(M0122\-0019 changed internal/optimizer \+ executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027n/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `fb6c415f1`** \(M0122\-0012 slice 93 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027o/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `b1f699edf`** \(M0122\-0012 slice 94 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027p/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `c0d1f97ed`** \(M0122\-0012 slice 95 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027q/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `660e20d86`** \(M0122\-0012 slice 96 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027r/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `34ccf73db`** \(M0122\-0012 slice 97 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027s/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `d5e071c67`** \(M0122\-0012 slice 98 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027t/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `06d18e112`** \(M0122\-0012 slice 99 changed internal/optimizer\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027u/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `64aa8f62f`** \(M0122\-0012 slice 101 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027v/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 — **exhausted at `986df5900`** \(M0122\-0012 slice 103 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261027w/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#202\) — **exhausted at `2eeabf36f`**, census `routing\-20261027w/` reused:
    `6f2772917` \(ralph only\) and `2eeabf36f` \(internal/initdb, xlog, cmd\) touched no planner\-relevant path\. No task
    is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#203\) — **exhausted at `8432307f1`** \(M0122\-0012 slice 104 changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027x/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99,
    SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live
    for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#204\) — **exhausted at `08a69974e`** \(M0122\-0012 slice 105 changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027y/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99,
    SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note\. Item 10 is live
    for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#205\) — **exhausted at `8d794539a`** \(M0122\-0012 slice 106 changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027z/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99,
    SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the `1f05de2c5` note \(plus
    P0\-E14\(owner\-placement\)\)\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#206\) — **exhausted at `acd4ff2d7`**, census `routing\-20261027z/` reused:
    `acd4ff2d7` is ralph/docs only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is live
    for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#207\) — **exhausted at `83bfa2945`** \(M0122\-0012 slice 107 changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027aa/`: TPC\-H 22/22, TPC\-DS SF0\.25
    99/99, SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is
    live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#208\) — **exhausted at `e35fff402`**, census `routing\-20261027aa/` reused:
    `e35fff402` touched internal/access/nbtree only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\.
    Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#209\) — **exhausted at `a8367e529`** \(M0122\-0012 slice 109 changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027ab/`: TPC\-H 22/22, TPC\-DS SF0\.25
    99/99, SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is
    live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#210\) — **exhausted at `2910f8451`**, census `routing\-20261027ab/` reused:
    `2910f8451` is analysis/ralph only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is
    live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#211\) — **exhausted at `b996b60d9`** \(M0122\-0012 slice 110 changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027ac/`: TPC\-H 22/22, TPC\-DS SF0\.25
    99/99, SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is
    live for this loop\.
  - S9 re\-check 2026\-10\-27 \(loop \#212\) — **exhausted at `3d50052e6`** \(M0122\-0012 slice 111 changed
    internal/executor\)\. Census `analysis/m0146/m0146\-0014/routing\-20261027ad/`: TPC\-H 22/22, TPC\-DS SF0\.25
    99/99, SF1 99/99, every category 0\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is
    live for this loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#213\) — **exhausted at `2349ef9b0`**, census `routing\-20261027ad/` reused:
    `2349ef9b0` is docs only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this
    loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#214\) — **exhausted at `e3a2535f0`** \(C32 slice 1 changed internal/executor\)\.
    Census `analysis/m0146/m0146\-0014/routing\-20261028a/`: TPC\-H 22/22, TPC\-DS SF0\.25 99/99, SF1 99/99, every
    category 0\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#215\) — **exhausted at `d1e6f0a79`**, census `routing\-20261028a/` reused:
    `d1e6f0a79` is analysis\-only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for
    this loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#216\) — **exhausted at `bafc2e139`**, census `routing\-20261028a/` reused:
    `bafc2e139` is docs\-only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this
    loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#217\) — **exhausted at `a3f833e78`**, census `routing\-20261028a/` reused:
    `a3f833e78` is docs\-only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this
    loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#218\) — **exhausted at `b7befe4c0`**, census `routing\-20261028a/` reused:
    `b7befe4c0` is ralph\-only\. No task is filed\. Same items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this
    loop\. The C34 commit changes `internal/executor`, so the next loop re\-runs the census\.
  - S9 re\-check 2026\-10\-28 \(loop \#219\) — **exhausted at `39041efc5`**, fresh census `routing\-20261028b/`
    \(s9aw\): TPC\-H 22/22, SF0\.25 99/99, SF1 99/99, CATEGORIES\-EXCL\-MATCH 0 everywhere\. No task is filed\. Same
    items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#220\) — **exhausted at `b68540921`**, fresh census `routing\-20261028c/`
    \(s9ax\): TPC\-H 22/22, SF0\.25 99/99, SF1 99/99, CATEGORIES\-EXCL\-MATCH 0 everywhere\. No task is filed\. Same
    items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#221\) — **exhausted at `4321d5793`**, fresh census `routing\-20261028d/`
    \(s9ay\): TPC\-H 22/22, SF0\.25 99/99, SF1 99/99, CATEGORIES\-EXCL\-MATCH 0 everywhere\. No task is filed\. Same
    items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#222\) — **exhausted at `f645129a8`**, fresh census `routing\-20261028e/`
    \(s9az\): TPC\-H 22/22, SF0\.25 99/99, SF1 99/99, CATEGORIES\-EXCL\-MATCH 0 everywhere\. No task is filed\. Same
    items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this loop\.
  - S9 re\-check 2026\-10\-28 \(loop \#223\) — **exhausted at `821d93323`**, fresh census `routing\-20261028f/`
    \(s9ba\): TPC\-H 22/22, SF0\.25 99/99, SF1 99/99, CATEGORIES\-EXCL\-MATCH 0 everywhere\. No task is filed\. Same
    items\-0\-9 tokens as the loop \#205 note\. Item 10 is live for this loop\.
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
- [x] **M0146\-0196 — REINDEX and DROP INDEX cannot resolve relations in a CREATE DATABASE'd database** \(filed
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
  - **2026\-10\-16 CLOSED \(owner\-placed, reviewed\)** — the filing's DROP INDEX half was already fixed:
    `execDropIndex` passes `NamespaceDBOid\(ctx\.CurrentDatabaseOid\)` \(operators\_ddl\.go\); verified working on a
    private cluster\. What was actually broken: `REINDEX INDEX`/`TABLE` resolved names via raw
    `ctx\.Catalog\.LookupIndex/LookupTable` \(DefaultDBOid only → 42P01\), and `REINDEX SCHEMA` enumerated
    `TablesInSchema` under DefaultDBOid → **silent no\-op returning success**\. Fix: INDEX/TABLE arms resolve via
    `ctxPlanCatalog` \(search\-path\-aware, same idiom as M0125\-0028's ANALYZE/VACUUM\); the SCHEMA arm uses raw
    catalog \+ explicit `NamespaceDBOid` because `TablesInSchema` has no `SearchPathCatalog` override\. Tests:
    `reindex\_dbid\_routing\_test\.go` \(3 tests: corrupt\-truncate → REINDEX → btree rescan per arm\)\. Verified live
    on a private cluster: all three REINDEX forms succeed in a CREATE DATABASE'd db\. Recorded residual, not
    blocking: `REINDEX DATABASE`/`SYSTEM` still parse but fall through the switch as silent no\-ops;
    `LookupEnum`/`LookupToastRel` inside the rebuild paths are dbOid\-agnostic \(no caller impact today\)\.
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
  Owner confirm 2026\-10\-16 \(R3\): keep as landed\.
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
- [x] **M0146\-0213 — TPC\-H Q16: goopg plans a Parallel Seq Scan on partsupp where PG plans a Parallel Index Only
  Scan on partsupp\_pk** \(filed 2026\-10\-16 by the S9 post\-VACUUM census, `routing\-20261016q`\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-H Q16, category scan\-type, verdict SHAPE\-DIFF, first divergence at depth 5 under the Parallel
    Hash Join's outer side.
    - PG: `Parallel Index Only Scan using partsupp\_pk on partsupp \(cost=350.43..19178.43 rows=100000\)`, with
      Gather Merge Workers Planned: 4.
    - goopg: `Parallel Seq Scan on partsupp \(cost=0.00..21257.81 rows=129032\)`, with Workers Planned: 3.
  - The index path has no index condition. PG offers it because `check\_index\_only` holds: the query reads only
    ps\_partkey and ps\_suppkey, both key columns. The heap is all\-visible since the owner corpus VACUUM, which
    makes the heap\-fetch term near zero \(`build\_index\_paths`, `cost\_index`,
    `postgres/src/backend/optimizer/path/indxpath.c`, `costsize.c`\).
  - Its route was the M0146\-0019 corpus\-VACUUM residual; after the VACUUM it still diverges, so it is actionable.
  - First step: check whether goopg's search generates a qual\-less \(full\) index\-only path and its parallel
    variant for partsupp at all \(instrument `addPath`; memory: goopg's optimizer may lack such a path inside a
    join tree\). Then compare the per\-worker row and worker counts \(`compute\_parallel\_worker` over heap and
    index pages\).
  - Recon note \(filing loop\): the paths should already exist. `addIndexOnlyPaths` \(pathindexonly.go, M0134\-0187\)
    builds the full index\-only path, and its partial twin `indexonly.partial` \(M0134\-0189\) is a Parallel Index
    Only Scan. `indexOnlyResidualAdmissible` admits an uncorrelated hashed SubPlan filter. So the likelier
    divergence is cost or worker count \(3 vs PG 4\), not a missing path. Confirm with an `addPath` trace for
    partsupp before changing anything \(memory planner\_verify\_both\_candidates\_generated\).
  - **2026\-10\-16 diagnosed; engine fix landed; record blocked on an owner index rebuild \(`[!]`\).** Design:
    `docs/design/0100-0149/m0146-0213-btree-split-after-new-item.md`.
    - The `addPath` trace shows PG's Parallel Index Only Scan is generated \(`indexonly.partial`, rows 100000, 4
      workers\) and loses at 27180.4 against the seq scan's 21257.8. The gap is exactly the index size: goopg's
      `partsupp\_pk` is 5170 pages against PG's 3082, and 2088 × random\_page\_cost 4 = 8352.
    - Leaf decode: equal 24\-byte tuples, but goopg's leaves are uniformly ~54% full. The 2026\-10\-15 INSERT replay
      inserted interleaved ascending runs \(17,326 descents\) into an existing index, and the non\-rightmost
      "split after new item" arm \(`\_bt\_afternewitemoff`, nbtsplitloc.c\) was unported.
    - Ported as `afterNewItemOff` \(btree.go\). With the replay's exact inserts, goopg now builds 3973 pages against
      PG 18.3's 4080 \(scratch cluster\); before, 5170. Pinned by `TestAfterNewItemOffDecisions` and
      `TestLocalizedAscendingInsertsPackLeaves`, which fails without the port \(106 vs 68 blocks\).
    - On a private clone, a bulk\-built copy \(3078 pages, as PG's 3082\) gives PG's Q16 plan: Parallel Index Only
      Scan, 4 workers, run cost 18812 against PG's 18828.
    - Gates: nbtree `\-race`, units, tpch\-spotcheck, sf025 PASS=99 PLAN\-SHAPE same=99, full TestPort\_RegressSuite.
  - **ESCALATION \(owner\):** rebuild `:65433`'s `partsupp\_pk` the way PG's was built. Options: `REINDEX TABLE
    partsupp` \(needs M0146\-0196, REINDEX in a CREATE DATABASE'd db\), or a reload that creates indexes after the
    data. `lineitem\_pk` \(1.8×, the M0146\-0195 close row\) has the same cause. Expected: TPC\-H match 21 → 22. The task
    stays `[!]` until then, so the census does not re\-file Q16.
  - **RESOLVED 2026\-10\-16 \(owner, review\-guided\):** M0146\-0196 closed first \(6a17a5154\), then `:65433` was
    restarted on a 6a17a5154 build and both indexes rebuilt with **`REINDEX INDEX`, not `REINDEX TABLE`** —
    surgical, so the deliberately insert\-built FK indexes \(near\-PG sizes that Q3/Q5/Q7/Q10's matches rest on\)
    were not re\-bulk\-built. Measured on `:65433`: `partsupp\_pk` 5170 → **3078** \(PG 3082\), `lineitem\_pk` 41512 →
    **23067** \(PG 23101\); every other index unchanged \(fk twins 16641/2220 intact\)\. Q16 now plans `Parallel
    Index Only Scan using partsupp\_pk`, Workers Planned 4, cost 18812 vs PG's 19178 — the PG plan, and the Q16
    result digest matches PG exactly \(md5 e0f93d05…, 18166 rows\)\. tpch\-spotcheck Q12=2/Q13=35 PASS\.
  Movement: none — instrument artefact: the corpus index was insert\-built by the replay, and the plan follows PG's
  once it is rebuilt \(clone\-verified\).
  - Expected instrument: TPC\-H PLAN\-PARITY match 21 → 22.
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
  - Rank: below item 10 — correctness\-only, no plan instrument \(S7 carve\-out\)\. Owner placement
    2026\-10\-16: below item 10 CONFIRMED — filed, does not displace parity work\.
  - First step: `dispatchCopyViaExecutor` / the executor's copy path \(`copy\.go` "always runs in its own auto\-commit
    transaction"\): when `connTx\.InExplicit\(\)`, join the block's transaction \(`connTx\.Tx\(\)`\) and make the commit
    sites no\-ops, as the M0132\-S3 in\-block `Execute` did\. Test both protocols and COPY inside a SAVEPOINT\.
  - Repro: `analysis/m0146/m0146\-0204/repro\-scratch\.sql`\.
  > \#\# ESCALATION 2026\-10\-16 \(S2\) — M0146\-0205: COPY inside BEGIN … ROLLBACK keeps its rows
  > Wrong results and loss of atomicity: a client that rolls back a failed load keeps its partial rows\. Filed under
  > M0146\-0204\. Owner: place it, or confirm below item 10\.
- [ ] **M0146-0243 — Incremental Sort path counts `enable_sort` in `DisabledNodes`; PG's does not** \(filed 2026\-10\-27
  by M0147\-0043\)\. `incrementalSortPathWithGroups` \(`internal/optimizer/incrementalsortpaths.go`\) sets
  `DisabledNodes: disabledNodesFor\(\!cp\.enableSort, sub\)` and its comment says cost\_incremental\_sort folds
  enable\_sort in via cost\_sort\. It does not: PG 18\.3 `cost_incremental_sort` asserts `enable_incremental_sort`
  and sets `path\->disabled_nodes = input_disabled_nodes` \(`postgres/src/backend/optimizer/path/costsize.c:2123\-2125`\);
  only `cost_sort` adds `enable_sort ? 0 : 1` \(:2162\)\. With `enable_sort=off` goopg disables an Incremental Sort that
  PG would keep, so the ORDERED tournament can pick a different arm\.
  Kind: impl
  Parent: none
  - Rank: below item 10 — no TPC\-H/TPC\-DS query diverges at default GUCs \(the census runs with enable\_sort on\)
    \(S7 carve\-out\)\.
  - First step: set `DisabledNodes` to the input's own count \(`sub\.DisabledNodes`\), fix the comment, and pin it with
    a unit test that plans the ORDERED rel under `enable_sort=off` and expects the Incremental Sort to keep zero
    disabled nodes; cross\-check one `SET enable_sort = off` EXPLAIN against a scratch PG\.
- [x] **M0146-0206 — VACUUM tail truncation leaves the visibility map untruncated, so relallvisible exceeds relpages; a tail emptied by DELETE is not truncated** \(filed 2026\-10\-16 by M0146\-0204\)\.
  DONE 2026\-10\-16 \(owner, a181f6bfd\): `VisibilityMap\.TruncateRel` beside `opts\.FSM\.TruncateRel`,
  `VMTruncateForkForHeap` as the smgr\_redo `SMGR\_TRUNCATE\_VM` arm \(visibilitymap\_prepare\_truncate —
  tail bits of the last surviving map page cleared, fork truncated, idempotent\), `PageUsedLinePointerCount`
  \(ItemIdIsUsed\) for lastNonEmpty, reclaimed\-only pages no longer keep the tail\. relallvisible derives from
  the map so the clamp is automatic\. Tests: VMTruncateRel / VMTruncateForkForHeap /
  VacuumTailTruncationDropsUnusedTail / VacuumTailTruncationTruncatesVM\. units gate green\.
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
- [x] **M0146-0178 — index-only-scan heap-fetch term and int4 index density (TPC-DS Q24)**
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
    - **Closed 2026\-10\-16 \(owner\) — RELPAGES residual.** The remaining heap\-fetch gap is a corpus
      data\-layout artifact, not an engine defect: PG's bulk load leaves a non\-all\-visible tail
      \(store\_returns 1232 pages / 1196 all\-visible\) so its allvisfrac < 1, while goopg's corpus is
      fully all\-visible after the owner corpus VACUUM \(2026\-10\-16, on a181f6bfd: tpch 8/8, tpcds SF1
      25/25, SF0\.25 25/25; relallvisible <= relpages everywhere\)\. Index density landed \(fillfactor
      rightmost split; item\_pkey 52 = PG 52\) and slice 2 \(catalog\-resolved IndexRealPages\) landed\.
      No parity lever remains in the record\.
- [x] **M0146\-0214 — GEQO\-RNG: goopg's GEQO uses a bespoke Knuth LCG where PG uses pg\_prng \(splitmix64
  fseed + xoroshiro128\*\* + bitmask\-reject range\)** \(filed 2026\-10\-16 by owner directive;
  un\-waives GEQO\-RNG under S9\(2\)\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS Q64 \(GEQO\-RNG\), SF0\.25 and SF1; category join\-order, verdict SHAPE\-DIFF — goopg's
    genetic search picks a different tour at the GEQO tie\. PG `geqo\_seed=0` is deterministic
    \(`pg\_prng\_fseed` at geqo\_main\.c\), so identical draws reproduce PG's tour\.
  - First step: port pg\_prng into `internal/optimizer/geqo.go:693\-732` — a working splitmix64 +
    xoroshiro128\*\* implementation already lives in `internal/executor/analyze\_block\_sampler\.go`
    \(`pgPRNGState`\); match PG's draw sites and order \(geqo\_random\.c `pg\_prng\_uint64\_range`
    bitmask\-rejection, `geqo\_selection` `linear_rand`\), and audit the `len\(items\)` vs
    `levels\_needed` threshold dispatch at `relfromjoinlist\.go:729`\.
  - Also fix the stale comments claiming GEQO is a no\-op stub: `internal/utils/misc/defaults\.go:1313`
    and `internal/utils/misc/postgresql\.conf\.sample:260`\.
  - Residual caveat: bit\-identical draws reproduce PG's tour only while tour fitness \(path costs\) is
    bit\-identical; any remaining divergence after the port attributes to the first divergent tour cost
    and re\-routes normally\. Re\-measure Q64 on both scales\.
  - Expected instrument: TPC\-DS PLAN\-PARITY match counts at both scales \(Q64\)\.
  - **Slice 1 LANDED 2026\-10\-16 \(loop \#55\): GEQO draws PG's stream.** Design
    `docs/design/0100-0149/m0146-0214-geqo-pg-prng.md`.
    - `internal/utils/pgprng` \(pg\_prng\.c\) and `internal/utils/pgqsort` \(pg\_qsort, sort\_template\.h\) are
      shared leaf ports. The ANALYZE samplers now use pgprng too, instead of their private copy.
    - In geqo\.go, `geqoRNG` wraps pgprng; `initTour`, `linearRand`, `gimmeEdge` \(abs match\) and the pool\-size
      override \(≥ 2\) follow PG statement for statement; `sortPool` is pg\_qsort, whose tie order selection
      depends on. The threshold dispatch at relfromjoinlist\.go was audited: it is equivalent.
    - Oracles built from PG's own code, no server: a pg\_prng stream harness, a pg\_qsort permutation harness
      \(75 arrays\), and PG's own geqo\_\*\.c compiled into a draw\-order harness \(890 lines\). All are reproduced
      exactly and mutation\-checked.
    - Measured: census TPC\-H 22/22, SF0\.25 86/99, SF1 74/99, unchanged. Q64 is still SHAPE\-DIFF; its SF1 first
      divergence moved from depth 6 to 5, and SF1 parameterisation went 7→6, inside noise. Fire set: Q64 executed
      on both engines with byte\-identical results. Gates: units, tpch\-spotcheck, acceptance arm \(values
      identical\), sf025 PASS=99 \(PLAN\-SHAPE changed=1: Q64\), tpcds\-fireset PASS, TestPort\_RegressSuite.
    - Remaining: the walk is now PG's until `sort_pool`, from which tour worth steers it. goopg's Q64 costs
      differ from PG's \(13866\.65 vs 13874\.27\). **Next slice:** add a `GOOPG_GEQO_TRACE` initial\-pool dump
      \(tour, worth\) mirroring GEQO\_DEBUG `print_pool`, build a GEQO\_DEBUG PG oracle \(tmp/pgdbg recipe\), and
      compare worths tour by tour. The first differing worth names the costing gap.
  - **Slice 2 2026\-10\-16 \(loop \#56\): the remaining gap is physical. Gate: blocked\-on M0146\-0216\.** Design §6;
    evidence in `analysis/m0146/m0146\-0214/physical/`\.
    - Q64 leaf row estimates and seq\-scan costs are identical. Index scan costs differ \(`store\_returns\_pkey` IOS
      4\.38 vs 5\.52 at SF0\.25\)\.
    - pg\_class, goopg clone vs :65438: heap relpages and reltuples are identical; relallvisible differs on every
      table \(goopg fully all\-visible\); index pages differ \(store\_returns\_pkey 277/280, store\_sales\_pkey
      2481/2569\)\.
    - Control: PG 18\.3 restored with `pg\_dump \-\-statistics` \(the reference's exact stats\) but freshly built
      indexes picks a THIRD Q64 plan \(14022\.76 vs 13874\.27\)\. plancat\.c reads index pages, tree height and
      heap blocks physically\. So Q64 needs physical parity first; resume this task after M0146\-0216\.
  - 2026\-10\-19 \(M0146\-0226, c65fb3855\): shipped SF1 Q64 now MATCHes PG; the Filter row rounding moved it\.
    SF0\.25 Q64 still diverges at depth 5 \(join\-order\), so this task's gate is unchanged\.
  - 2026\-10\-19: the blocked\-on M0146\-0216 gate is CLEARED by the owner reload \(index geometry ±2 pages, PG
    statistics imported\)\. Q64 still diverges at BOTH scales on the reloaded corpora — resume slice 3 \(GEQO tour
    worths\) against them\.
  - **Slice 3 LANDED 2026\-10\-19: tour\-by\-tour worths against PG\.** Design §7; evidence
    `analysis/m0146/m0146\-0214/slice3/`\.
    - Probes: `GOOPG_GEQO_TRACE` / `GOOPG_GEQO_TRACE_EVAL` in geqo\.go and the matching geqo\_eval\.c / geqo\_main\.c
      probe in the tmp/pgdbg build \(patch in the evidence dir\)\. Both engines evaluate 500 tours; the first 252
      are identical tours in identical order\.
    - No tour's worth matched PG \(commonest \+0\.1\): Q64's cs\_ui, an inlined CTE over a grouped HAVING query,
      paid cpu\_tuple\_cost at the CTEScan, Project and Filter, and its HAVING was unpriced\. Fixed:
      HAVING priced inside the Aggregate \(cost\_agg's quals arm\), inlined CTEScan peeled like a SubqueryScan, Filter
      projection\-capable over a capable child\. 45 of 252 tours now match PG exactly\.
    - Routed: the \+0\.0103 class is customer\_demographics\_pkey's one extra index page \(M0146\-0216\); the large
      classes start at a `<>` join estimate \(1536640 vs 1536621\), eqjoinsel's missing MCV arm → **M0146\-0230**\.
    - Q64 unchanged at depth 5 \(13889\.69 vs PG 13874\.27\)\. Pins: TestHavingIsPricedInsideTheAggregate,
      TestInlinedCTELeafPricesCostSubqueryscanOnce \(mutation\-checked\); TestExplainLimitClampsToInputRows now pins
      PG's own Limit lines\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(values identical\), sf025 PASS=99
      \(14 plans changed text\), tpcds\-fireset PASS \(29 fired results byte\-identical; parity 97/94 held\)\.
    - Next: M0146\-0230, then re\-run the tour comparison \(`analysis/m0146/m0146\-0214/slice3/trace/joincmp\.sh`\)\.
  - 2026\-10\-19: M0146\-0230 landed \(MCV join estimates\)\. Tour 2's first gap is now a merge\-join COST at equal rows
    \(Q64 join with item, 939 rows: goopg 20675\.72\.\.106647\.34 vs PG 18896\.82\.\.106969\.50\) — next slice: compare
    cost\_mergejoin's terms \(outer sort over 1\.5M rows, inner sort, mergejoin rescan\) with PG on that join\.
  - **Slice 4 LANDED 2026\-10\-19 — SF1 Q64 MATCHes PG \(SF1 PLAN\-PARITY 94→95\)\.** Design §8; evidence
    `analysis/m0146/m0146\-0214/slice4/`\.
    - Tour 2's merge gap was not cost_mergejoin: goopg never built PG's presorted\-index\-inner merge, because
      findMergeClausesForOuterPathkeys matched outer pathkeys to clauses by expression\. PG compares
      EquivalenceClasses; the outer \(ordered on cs\_ui\.cs\_item\_sk\) serves the clause on ss\_item\_sk via the
      class the outer enforces\. Fixed \(indexOfEquivalentOuterKey, serial and partial callers\)\.
    - Tour 12: hash\_inner\_and\_outer tries the cheapest\-STARTUP outer first; goopg built total\-total only\. Fixed
      \(addHashJoinPath files PG's candidate set in PG's order\)\.
    - 155/252 tours within 1e\-6 of PG; SF0\.25 Q64 still depth 5\. Remaining: Gather Merge over partial paths on
      intermediate GEQO joinrels \(tour 14 cartesian\), and customer\_demographics\_pkey's \+1 page \(M0146\-0216\)\.
    - Pins: TestFindMergeClausesForOuterPathkeys\_MatchesThroughTheOuterClass, TestAddHashJoinPath\_
      TriesTheCheapestStartupOuter \(mutation\-checked\)\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm, sf025 PASS=99, tpcds\-fireset PASS
      \(Q64 fired at both scales, byte\-identical\)\.
    - Next slice: compare the GEQO\-joinrel partial / Gather Merge paths with PG on tour 14's join 6
      \(\(b 1 3 7 9 10 11 12 16\): PG Gather Merge 1003\.38\.\.6\.42e9, goopg serial NL 1\.98e10\)\.
  - **Slice 5 \(diagnosis\) 2026\-10\-19 → filed M0146\-0231\.** Design §9; evidence `analysis/m0146/m0146\-0214/slice5/`\.
    - Partial paths match PG up to \(b 1 7 9 10 11 12 16\); adding cs\_ui, PG files a partial Merge Join whose inner
      is a DIFFERENT, parallel\-safe SubqueryScan path of cs\_ui \(0\.7225\.\.42006\.29, ordered on cs\_item\_sk\) —
      set\_subquery\_pathlist keeps every path of the subquery's final rel\. goopg's subquery leaf is one prebuilt
      path \(the cheapest, with a Gather Merge inside, not parallel\-safe\), so no partial merge is possible\.
    - Next: M0146\-0231, then re\-run the 500\-tour comparison\.
  - **M0146\-0231 landed 2026\-10\-19:** SF0\.25 Q64 MATCH; SF1 Q64 lost its match to a GEQO worth tie after tour 262
    \(design §10\)\. Next: the sub\-1% worth residuals before SF1 tour 262 \(evals 41, 90 at 0\.6–0\.75%; eval 160 at
    2\.6e\-4 is the one eval 0231 made worse\) — `analysis/m0146/m0146\-0231/evalcmp\.py` lists them\.
  - **Slice 6 2026\-10\-20: mergejoinscansel ported in full\.** Design §11; evidence `analysis/m0146/m0146\-0214/slice6/`\.
    - SF1 eval 41's first differing joinrel is \(b 1 9 10\): PG's merge startup carries
      merge\_qual\_cost × inner\_skip\_rows × rescanratio \(275 × 1\.54e6 × 0\.0025 = 1056504\); goopg reported start
      fractions as 0\.
    - Landed: the four start/end fractions with PG's believe\-one rules, get\_variable\_range \(histogram widened by MCV
      extremes\), and initial\_cost\_mergejoin's skip rows \(rint\) / clamped scan rows / readjusted selectivities;
      final\_cost\_mergejoin charges the skipped prefix's merge quals at startup\. scalarineqsel's literal form is now
      one core \(scalarIneqSelStats\) shared with the restriction path\.
    - Measured: customer\_demographics ⋈ store\_sales merge 389569\.59\.\.485522\.14 vs PG 389569\.93\.\.485517\.46 \(was
      389568\.55\.\.486177\.77; 4\.0 of the total gap is M0146\-0216's index page\); SF0\.25 Q77's Merge Join 3518\.29 =
      PG \(was 3518\.30\)\. Match counts unchanged \(fire\-set SF0\.25 98, SF1 94\); SF1 Q64 tours equal to PG's stay 262\.
    - Pins: TestMergeJoinChargesSkippedPrefixAtStartup \(mutation\-checked\), TestVariableRangeWidensByMCVExtremes\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(values identical\), sf025 PASS=99 \(Q77
      plan text changed\), tpcds\-fireset PASS \(fired Q77 byte\-identical\)\.
    - Movement: none — parity held; merge\-join costs converge on PG numerically, no match count moved\.
    - Remaining eval\-41 residuals → M0146\-0233 \(the cutoff clamp: 207 vs PG's 275 skipped rows\) and M0146\-0234
      \(sort byte currency and joinrel sort width: join 2's Sort, join 9's partial merge 6\.32e10 vs PG 6\.20e10\)\.
  - **Slice 7 2026\-10\-20: Gather Merge useful pathkeys through the equivalence class\.** Design §12; evidence
    `analysis/m0146/m0146\-0214/slice7/`\.
    - SF1 eval 141 \(worst after M0146\-0234 slice 1, goopg 0\.57% dearer\): PG's final NL takes a Gather Merge over
      \(b 1\.\.17\) sorted on all 14 query pathkeys; goopg's usefulPathkeysForRelation tested computability
      syntactically and stopped at i\_item\_sk \(item, outside the rel\)\. Now relation\_can\_be\_sorted\_early's
      find\_computable\_ec\_member: the pathkey carries a parallel\-safe class member the rel computes \(ss\_item\_sk\)\.
    - SF1 Q64 evals off >1e\-3: 6→1 \(>1e\-4: 29→19; eval 141 0\.0057→7\.1e\-6\)\. Fire\-set 98/94, TPC\-H 22/22 unchanged;
      SF1 Q77's plan text changed \(fired result byte\-identical, same first divergence\)\.
    - Pin: TestSortableEarlyMemberUsesTheEquivalenceClass\.
    - Gates: units \(the first run timed out in internal/initdb under the overlapping nightly; re\-run on an idle host
      PASS\), TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(values identical\), TPC\-H parity 22/22, sf025
      PASS=99, tpcds\-fireset PASS\.
    - Movement: none — parity held; SF1 Q64 eval worths converge on PG \(off >1e\-3: 6→1\)\.
    - Next: eval 173 \(0\.46%\) is M0146\-0234's joinrel attr\_needed width \(PG's partial merge fuzz\-equal to the partial hash
      with better pathkeys drops it; goopg's merge 2\.6933e10 vs PG 2\.5004e10\)\.
  - **Slice 8 2026\-10\-20 \(done\): SF1 Q64 MATCH \(SF1 94→95\)\.** Design §13; evidence `analysis/m0146/m0146\-0214/slice8/`\.
    - After M0146\-0234 slice 2 only two worth\-order inversions remained before tour 262 \(evals 96/138, 159/248\)\. Eval
      159, two mechanisms:
      - generate\_useful\_gather\_paths' incremental\-sort arm was skipped \("deferred to M0146\-0006"\)\. PG files a Gather
        Merge over an Incremental Sort of the partial merge \(presorted on the leading query pathkey\); without it, a
        Nested Loop with the same 7 pathkeys dominated goopg's pk1 Gather Merge\. Ported, with
        `create_incremental_sort_path`'s parallel\_workers and an equivalence\-class\-aware presorted count; the group count
        is estimate\_num\_groups over the presorted keys \(searchRelNumGroups\)\. The Incremental Sort is a per\-worker
        pass\-through in every parallel walk: partialPathDrivingKind, drivingScan, stamp/unstampParallelScan,
        drivingScanCrossesSort, replaceSingleChild, parallelChildren, and the executor's attachParallelScan /
        attachParallelIndexScan / attachParallelBitmapScan / hash\-build claim / unwrapToSetOp / collectBitmapScans /
        collectShareableJoins\.
      - merge\_clump gathered the topmost scan/join rel inside each GEQO tour; PG skips it \("Except for the topmost
        scan/join rel"\) and gathers it in apply\_scanjoin\_target\_to\_paths\. goopg's top\-level Gather Merge \(pk14\)
        dominated the fuzz\-equal Nested Loop and raised the tour's worth\. Now mergeClump skips queryTopRelids
        \(root\->all\_query\_rels\) and geqoSearch gathers the best tour's rebuilt top rel\. DP is unchanged: its top rel
        feeds nothing above, so gathering it in the level loop is equivalent\.
    - SF1 Q64: all 500 GEQO tours equal to PG's \(was 262\); no worth\-order inversion; evals 159/96 now agree to 2\.7e\-9;
      plan MATCH\. Fire\-set SF1 94→95 \(Q64\), SF0\.25 98 \(Q64 still MATCH\); TPC\-H 22/22\.
    - Pin: TestGatherMergeOverIncrementalSortIdentity \(executor; removing attachParallelScan's arm returns 800 rows for
      400 — mutation\-checked\)\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm \(values identical\),
      TPC\-H parity 22/22, sf025 PASS=99 \(no plan\-shape change\), tpcds\-fireset PASS \(fired SF1 Q64 byte\-identical\)\.
    - Children stay open as their own tasks: M0146\-0216 \(index page\), M0146\-0232, M0146\-0233, M0146\-0234\.
  Movement: yes — SF1 match 94→95 \(Q64\)
- [x] **M0146\-0215 — ESTIMATE\+RELPAGES: TPC\-DS Q76's width and join estimates diverge \(goopg
  width=298 vs PG 163\), flipping join\-method/scan\-type and parameterisation** \(filed 2026\-10\-16 by
  owner directive; un\-waives ESTIMATE\+RELPAGES under S9\(2\)\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS Q76, SF0\.25 first divergence depth 6 under Append \(PG Nested Loop Inner vs goopg
    Parallel Hash Join Inner\); SF1 depth 8 \(PG Memoize vs goopg date\_dim\_pkey probe\)\. Census diff
    lines: sf025 `cost=39239\.95\.\.39283\.55 rows=100 width=298` vs PG `39026\.66\.\.39070\.79 rows=100
    width=163`; sf1 `131523\.79\.\.131580\.18` vs `131305\.26\.\.131362\.41`\.
  - First step: attribute the width=298 vs 163 gap — the plan's result\-column width estimate — and the
    estimate delta behind the routing's ESTIMATE component \(Q76's `IS NULL` predicate over a sampled
    column is the documented suspect\); then port the deciding PG term \(clauselist/selfuncs row\-width
    or null\-fraction estimate\)\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY \(Q76\), then SF1\.
  - **Slice 1 2026\-10\-16 \(loop \#57\): attributed — the divergence is ANALYZE sample variance, not planner
    logic\.** Evidence: `analysis/m0146/m0146\-0215/`\.
    - The first divergence is the catalog\_sales branch: PG nested\-loops into item\_pkey on 512 rows per worker;
      goopg uses a Parallel Hash Join on 585\. Every branch's `IS NULL` estimate differs\.
    - null\_frac: PG cs\_bill\_customer\_sk 0\.0044 / ss\_customer\_sk 0\.0447 / ws\_promo\_sk 0\.000233; goopg
      0\.00503 / 0\.0451 / 0\.0002; exact 0\.00478 / 0\.0446 / 0\.000272\. Both are 30000\-row samples of the same
      rows; PG's sampler seed is random per process \(pg\_global\_prng\_state\), so no code port reproduces its
      sample\.
    - Control: the tmp/pgdbg scratch PG with the reference's exact stats \(pg\_dump \-\-statistics\) reproduces the
      reference's Q76 shape\. With only those three null\_frac values set to goopg's
      \(pg\_restore\_attribute\_stats\), PG 18\.3 picks **goopg's exact plan** \(rows=585\)\. goopg's planner is
      PG\-identical on Q76\.
    - Width 298 vs 163 is the known unnarrowed planner width; it does not decide this plan\.
    - Route: PG 18's statistics import, `pg\_restore\_attribute\_stats` / `pg\_restore\_relation\_stats`
      \(statistics/attribute\_stats\.c, relation\_stats\.c\), so a corpus can carry the reference's exact
      statistics\. goopg has the names only in the pg\_proc seed\. Importing into the bench corpora is an owner
      action; the functions are engine work and are the next slice\.
  - **Slice 2 LANDED 2026\-10\-16 \(loop \#57\): PG 18 statistics import\.** Design
    `docs/design/0100-0149/m0146-0215-pg-statistics-import.md`\.
    - `pg\_restore\_attribute\_stats` / `pg\_restore\_relation\_stats` \(executor/stats\_import\.go\) port
      stat\_utils\.c's pair parsing and attribute\_stats\.c / relation\_stats\.c's semantics\. Values go through the
      column type's input with typmod; the result merges into existing stats and persists through ANALYZE's
      writers\. The FROM\-position form pg\_dump emits is planned as a ScalarFuncScan\.
    - `catalog.ColumnStatsDistinct` is the one stadistinct conversion, shared with the startup reload\.
      pg\_stats\.avg\_width now renders the integer stawidth\.
    - Pins: TestPgRestoreAttributeStatsRoundTrip \(the pg\_stats text round\-trips to the restart form\),
      …Semantics \(merge, WARNING/false, ERROR paths\), TestPgRestoreRelationStats,
      TestImportedNullFracReachesPlanner\. Mutation\-checked\.
    - Measured on a private SF0\.25 clone: the reference's 474 stats calls import cleanly in 1\.5 s, and SF0\.25
      PLAN\-PARITY goes **86 → 88** \(Q76, Q99 MATCH, no regressions\), surviving two restarts\. The shipped corpora
      are unchanged\.
    - Gates: units, tpch\-spotcheck, acceptance arm \(values identical\), sf025 PASS=99 same=99, tpcds\-fireset PASS
      \(no plan change\), TestPort\_RegressSuite\. Ledger rows: relallvisible, element/range kinds, clear
      functions\.
    - **Gate: owner\-import stats\.** The record moves only when the bench corpora carry the reference's
      statistics: run the `pg\_restore\_\*\_stats` section of `pg\_dump \-\-statistics \-d tpcds025` \(and
      `\-d tpcds` for SF1\) on the goopg clusters\. That replaces their ANALYZE statistics, so it is the owner's call
      \(see the escalation in the baton\)\.
  - **2026\-10\-19: owner gate CLEARED** — both shipped corpora now carry the reference's statistics \(OWNER DECISIONS
    2026\-10\-19\)\. SF0\.25 Q76 and Q99 MATCH on the shipped corpus; re\-measure and close with the census\.
  - **Closed 2026\-10\-20 \(re\-measured\)\.** Evidence `analysis/m0146/m0146\-0215/closure/` — Q76 and Q99 from the
    d1446d972 fire\-set candidate captures \(`analysis/m0146/m0146\-0214/slice8/fireset/`\), shipped corpora with the
    reference's imported statistics\.
    - Q76 and Q99 MATCH at SF0\.25 and at SF1 \(first\-divergence: no structural divergence\)\.
    - Not plan\-shape: Q76's top Limit still renders width=298 against PG's 163, and its cost is 0\.15% high
      \(SF0\.25 39087\.41\.\.39131\.53 vs 39026\.66\.\.39070\.79\)\. EXPLAIN widths are the rendering burn\-down's
      \(M0146\-0042\); the cost\-width currency is M0146\-0234's\.
  Movement: yes — SF0\.25 match 86→88 \(Q76, Q99\)
- [x] **M0146\-0216 — physical\-parity census: per\-table relpages / relallvisible / pg\_relation\_size
  vs PG :65438 across both TPC\-DS corpora, and the COPY/VM all\-visible marking question**
  \(filed 2026\-10\-16 by owner directive; un\-waives RELPAGES under S9\(2\)\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: RELPAGES class — SF1 Q52 and the ESTIMATE\+RELPAGES records \(Q76\)\. Owner verification
    2026\-10\-16: SF1 heaps are already fresh\-format — relpages identical to PG on all 10 probed tables
    \(item 1284, customer 2872, store\_sales 51784, …\) — so the old 1242\-vs\-1284 premise is dead and
    NO reload is needed\. The remaining physical delta is VM all\-visible state: goopg corpora sit at
    relallvisible = relpages after the owner VACUUM while PG's bulk\-loaded tables keep a non\-visible
    tail \(store\_sales 51747/51784, catalog\_sales 37427/37448\)\.
  - First step: full per\-table `relpages`/`relallvisible`/`reltuples`/`pg_relation_size` census on
    :65436 and :65437 vs :65438 `tpcds`/`tpcds025`, then attribute each diff: if goopg's COPY marks
    bulk\-loaded pages all\-visible where PG's copyfrom\.c does not, that is component work \(COPY VM
    marking\); if a heap/index page count still differs, the fix belongs to hio\.c bulk extension,
    heap packing, or nbtree bulk build per S9\(5\)\(c\) — not the planner\.
  - Expected instrument: TPC\-DS PLAN\-PARITY \(Q52, Q76 and any record the census re\-attributes\)\.
  - **Slice 1 2026\-10\-16 \(loop \#58\): census and attribution\.** Evidence `analysis/m0146/m0146\-0216/`; design
    `docs/design/0100-0149/m0146-0216-physical-parity-relallvisible.md`\.
    - Heap relpages and physical size are identical on every table at both scales\. relallvisible differs on 20/23
      tables \(goopg fully all\-visible after the owner VACUUM; PG keeps tails, and has 0 on small tables autovacuum
      never reached\)\.
    - Index pages: the corpus predates the M0146\-0213 split port \(inventory\_pkey 14502 vs PG 12606\)\. Replaying the
      same COPY on the current engine gives 0\-3% of PG \(inventory\_pkey 12774, web\_sales\_pkey exact\)\.
  - **Slice 2 LANDED 2026\-10\-16 \(loop \#58\): stored pg\_class\.relallvisible\.** PG's planner reads the STORED
    value \(plancat\.c\), set by vac\_update\_relstats at VACUUM, ANALYZE and index builds\. goopg read the live VM\.
    - TableStats\.AllVisible/AllVisibleSet with snapshotRelAllVisible at those three sites\. The planner and the
      pg\_class view read the stored value; a 4th sidecar column persists it \(an old 3\-column row reads as unset,
      falling back to the live map, ledger\); pg\_restore\_relation\_stats applies it\.
    - Pins: TestRelAllVisibleIsStoredLikePG \(postmaster: VACUUM sets it, UPDATE leaves it stale, import plus
      restart\) and TestImportedRelAllVisibleReachesPlanner\. Mutation\-checked\.
    - Measured: shipped\-corpus plans are unchanged \(sf025 same=99, fire set none\)\. With full PG stats including
      relallvisible imported on a clone, SF0\.25 stays at 88/99 \(parallelism 5→4\)\. The 11 remaining divergences are
      planner ones: sort\-strategy 5 \(Q4, Q38, Q72, Q77, Q87\), parallelism 2 \(Q16, Q39\), Q61, Q69, Q78, and Q64
      \(index geometry\)\.
    - Gates: units, internal/postmaster, tpch\-spotcheck, acceptance arm, sf025 PASS=99 same=99, tpcds\-fireset PASS,
      TestPort\_RegressSuite, TestPort\_Isolation\*\.
    - Remaining: corpus index refresh \(owner reload\); the residual 0\-3% split\-location gap against
      \_bt\_findsplitloc \(next engine slice\)\.
  - **Slice 3 LANDED 2026\-10\-16 \(loop \#59\): \_bt\_findsplitloc's interval and suffix\-truncation penalty\.**
    Design §6; evidence `analysis/m0146/m0146\-0216/split/`\.
    - goopg cut a leaf at the single best\-balanced split. PG sorts all legal splits by delta \(pg\_qsort\), bounds an
      interval \(LEAF\_SPLIT\_DISTANCE\), and picks the lowest \_bt\_keep\_natts\_fast penalty, with the
      many\-duplicates and single\-value strategies\. Ported in nbtree/splitloc\.go; the shared splitCandidates
      enumeration keeps both paths' pricing identical\.
    - SF0\.25 replay \(scratch PG reproduces the reference exactly\): inventory 12774→**12606**, catalog\_sales
      1226→**1250**, store\_returns 277→**280**, catalog\_returns 138→**137**, web\_sales 598 \(all = PG\);
      store\_sales 2496→2567 \(PG 2569\)\. inventory is leaf\-sequence identical\. The store\_sales residual traces to
      heap TIDs: PG's COPY reuses earlier pages' free space through the FSM \(hio\.c\); ledger\.
    - Pin: TestInsertBuiltCompositeIndexMatchesPGPageCount \(PG\-measured 273/199/288; HEAD 269/249/304\),
      mutation\-checked\.
    - Gates: nbtree \-race, units, tpch\-spotcheck, acceptance arm, sf025 PASS=99 same=99, TestPort\_RegressSuite,
      TestPort\_Isolation\*\.
    - Shipped corpora are unchanged until they are reloaded on this engine \(owner\-index\-refresh\)\.
  - **2026\-10\-19: owner reload DONE** \(OWNER DECISIONS 2026\-10\-19\): index pages now within ±2 of PG on every
    table at both scales \(store\_sales\_pkey SF0\.25 2567 vs 2569; SF1 11042 vs 11041\); heap pages and relallvisible
    identical\. Remaining: the FSM\-reuse heap\-TID residual \(ledger\)\. Re\-measure and close\.
  - **Slice 4 LANDED 2026\-10\-20 \(loop \#29\): internal\-page splits\.** Design §7; evidence
    `analysis/m0146/m0146\-0216/slice4/`\.
    - Record: customer\_demographics\_pkey 5269 vs PG 5268 on both corpora \(\+4\.0 per probe; SF1 Q64, SF0\.25 Q77\)\.
      Leaves identical; the internal level is 19 pages of 284 against PG's 17 of 285 plus a rightmost 403\.
    - Cause: internal splits used the leaf pricing\. \_bt\_recsplitloc's non\-leaf arm charges no heap\-TID allowance and
      credits the right half with the firstright key, which \_bt\_split stores as a minus\-infinity first item\.
      Ported as pgFindSplitLocInternal \(INTERNAL\_SPLIT\_DISTANCE, smallest\-firstright penalty\) plus the truncation\.
    - Measured \(COPY replay on clones; HEAD reproduces every shipped size\): customer\_demographics\_pkey
      5269→**5268** at both scales\. Unchanged: SF0\.25 store\_sales 2567 \(PG 2569\); SF1 store\_sales 11042
      \(11041\), store\_returns 1081 \(1082\), catalog\_sales 5624 \(5625\)\. Those four ride heap placement: 7361 /
      689 / 17539 SF1 heap blocks hold a different row count than PG's \(the FSM\-reuse ledger row\)\.
    - Pins: TestInsertBuiltSerialIndexMatchesPGInternalLevel and TestFindSplitLocInternalTakesSmallestFirstRight,
      mutation\-checked \(truncation, penalty, heap\-TID allowance, both halves\)\.
    - Gates: nbtree and amcheck \-race, units, TestPort\_RegressSuite, TestPort\_Isolation\* \(the first run hung
      once, ledger; the re\-run passed in 596 s\), tpch\-spotcheck, acceptance arm, sf025 sweep, tpcds\-fireset
      \(SF1 95/99, fired none\)\.
    - Next: COPY heap placement through the FSM \(hio\.c RecordAndGetPageWithFreeSpace; ledger 2026\-10\-16\), which
      is the last engine mechanism here\. Then an owner corpus rebuild, re\-measure and close\.
  - **Slice 5 LANDED 2026\-10\-20 \(loop \#30\): COPY heap placement through PG's FSM\.** Design §8; evidence
    `analysis/m0146/m0146\-0216/slice5/`\.
    - Root cause: server\-side COPY's hand\-built executor context \(postmaster/copy\.go\) had no FSM \(and no VM\)\.
      The bulk path recorded nothing and found nothing, so the heap filled strictly forward\. A trace showed every
      RecordAndGet search failing\.
    - Port: storage/fsm\_pgtree\.go, PG's FSM tree \(fsm\_search\_avail with fp\_next\_slot, own\-leaf\-first
      RecordAndGet, upper levels moved only by FreeSpaceMapVacuumRange with its exclusive end, fsm\_search's stale
      correction\)\. It is used by the bulk path; COPY gets FSM/VM; VACUUM ends with the vacuum range;
      MaxFSMRequestSize 8164→8160\.
    - Measured, full COPY replay at both scales: every pkey = PG \(store\_sales 2567→**2569** SF0\.25,
      11042→**11041** SF1; store\_returns 1081→**1082**; catalog\_sales 5624→**5625**\)\. Heap block counts are
      identical to the PG replica \(0 differing blocks, from 4303/387/58 SF0\.25 and 17539/689/7361 SF1\)\. Heap
      pages = PG after VACUUM truncates COPY's tails\.
    - Pins: TestCopyFromRefillsEarlierPagesThroughTheFSM \(PG\-measured layout plus 8 back\-jumps; fails without the
      ctx FSM, with first\-fit, or without the vacuum range\) and fsm\_pgtree\_test\.go\.
    - Filed M0146\-0236 \(S2\): ctid NULL / xmin unresolved above a WindowAgg\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm, sf025 PASS=99 same=99, tpcds\-fireset \(98/99, 95/99, fired none\)\.
    - Engine work for this task is complete\. Remaining: an owner corpus rebuild on this engine
      \(scripts/tpcds\-corpus\-rebuild\.sh\), then re\-census and close\. The shipped corpora still carry the old
      geometry \(customer\_demographics\_pkey 5269\)\.
  - **2026\-10\-20: owner corpus rebuild DONE** \(OWNER DECISIONS 2026\-10\-20\): all 24 pkeys = PG at both
    scales, heap per\-block identical, amcheck clean; same\-binary census unchanged \(98/99, 96/99\)\. Remaining:
    re\-census and close after the open child M0146\-0236\.
  - **CLOSED 2026\-10\-20 \(loop \#35\)** after M0146\-0236 resolved \(b0c982657\)\. Design §9; evidence
    `analysis/m0146/m0146\-0216/closure/`\.
    - Closure census \(fresh clones of both rebuilt corpora vs :65438; relpages, relallvisible, reltuples,
      pg\_relation\_size for every public table and index\): SF0\.25 0 of 49 differ; SF1 1 of 49 \(dbgen\_version physical
      0 vs 1\)\.
    - dbgen\_version: `scripts/tpcds\-corpus\-rebuild\.sh`'s TABLES list omits it, so the goopg SF1 corpus has no row
      where PG's has one; the matching relpages/reltuples are PG's imported statistics\. A load\-script gap for the owner,
      not an engine one; no TPC\-DS query reads it \(ledger\)\.
    - Original record cleared: the RELPAGES class \(SF1 Q52, Q76\) matches; the owner's same\-binary census held
      98/99 and 96/99 with identical shapes\.
    - Children: M0146\-0236 closed; its filings M0146\-0238/0239 are correctness\-only and stay below item 10
      \(S7\) unless placed\.
  Movement: none — physical census only: plan instruments were measured unchanged by the owner's same\-binary A/B \(98/99, 96/99\)
  - Re\-attributed record \(M0146\-0214 slice 2, 2026\-10\-16\): TPC\-DS **Q64**. Its GEQO walk is now PG's draw for
    draw, but the tours' worth follows the physical inputs: relallvisible on every Q64 table, plus
    store\_returns\_pkey 277 vs 280 and store\_sales\_pkey 2481 vs 2569 index pages at SF0\.25\.
    `analysis/m0146/m0146\-0214/physical/` has the pg\_class pairs. A `pg\_dump \-\-statistics` restore into the
    `tmp/pgdbg` scratch PG reproduces the reference's stats exactly and is a ready control rig; dump at
    `tmp/g14\-tpcds025\-stats\.sql`\.
- [x] **M0146\-0236 — WRONG RESULTS: system columns lost above a WindowAgg — `ctid` reads NULL, `xmin` does not
  resolve** \(filed 2026\-10\-20 by M0146\-0216 slice 5; S2 escalation, owner to place\)\.
  Kind: impl
  Parent: M0146-0216
  - Repro \(any table\): `CREATE TABLE zt\(id int, x int\); INSERT INTO zt VALUES \(1,1\),\(2,2\);`
    - `SELECT ctid, row\_number\(\) OVER \(\) FROM zt` returns NULL for ctid; PG returns \(0,1\) and \(0,2\)\.
    - `SELECT ctid, sum\(x\) OVER \(ORDER BY id\) FROM zt` and `lag\(ctid\) OVER \(...\)` give NULL as well\.
    - `SELECT xmin::text, row\_number\(\) OVER \(\) FROM zt` errors `column "xmin" does not exist`\.
    - Plain `SELECT ctid FROM zt` and `ctid` in a subquery or GROUP BY are correct\.
  - First step: find where the WindowAgg path builds its input target list. The system attribute is probably
    dropped there \(ctid\), or never offered to name resolution \(xmin\)\. Compare with the Agg path, which keeps
    `ctid` correctly\.
  - Gate: correctness\-only under the S7 carve\-out; no plan instrument\.
  - **LANDED 2026\-10\-20 \(loop \#34\), the window case; selected as the child OWNER DECISIONS 2026\-10\-20 names
    for closing M0146\-0216\.** Design `docs/design/0100-0149/m0146-0236-window-ctid.md`\.
    - Cause: goopg's `ctid` is a CTIDExpr reading the slot's TID side\-channel\. windowOp buffered and re\-sorted its
      input and emitted fresh slots, so the channel was lost\. markSortWantCTIDs passed through the window without
      asking it to keep anything\.
    - Change: windowOp keeps a per\-row TID array \(captured with slot\.TID\(\) while buffering, permuted with the
      partition sort, re\-attached on emit\); markSortWantCTIDs sets its wantCTIDs\.
    - Result: ctid beside window functions equals each row's TID, including over a Sort on 200000 rows\. Pin:
      TestCtidSurvivesWindowAgg, mutation\-checked\.
    - Not window\-specific, so filed separately: `xmin` and the other system columns do not resolve anywhere
      \(M0146\-0239\); `ctid` is NULL in every join and binds to non\-relation range entries \(M0146\-0238\)\. `ctid`
      inside the window's own expressions stays NULL \(ledger, same root as 0238\)\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm, sf025 PASS=99 same=99, tpcds\-fireset PASS \(no change; 98/99, 96/99\)\.
  Movement: none — correctness fix — no plan instrument
- [ ] **M0146\-0238 — WRONG RESULTS: `ctid` reads NULL in every join and binds to non\-relation range entries** \(filed
  2026\-10\-20 by M0146\-0236; S2 escalation, owner to place\)\.
  Kind: impl
  Parent: M0146-0236
  - Repro: `CREATE TABLE a\(id int\); CREATE TABLE b\(id int\); INSERT ...;`
    - `SELECT a\.ctid, b\.ctid FROM a JOIN b USING \(id\)` returns NULL for both; PG returns each row's TID\.
    - `SELECT ctid FROM \(SELECT \* FROM a LIMIT 5\) s` returns values; PG errors 42703 \(a subquery has no ctid\)\.
    - `SELECT ctid FROM a, LATERAL \(SELECT 1\) l` errors "ambiguous"; PG resolves it to a\.
    - `lag\(ctid\)`, `PARTITION BY ctid` and a run condition over ctid read NULL inside a WindowAgg\.
  - Cause: ctid is a CTIDExpr reading one TID side\-channel per slot; a join row has two sources, and
    resolveColumnRefAt binds `ctid` to any range binding, not only to relations\.
  - PG: ctid is a Var with attno \-1 of its relation, projected by the scan and carried up like any column\.
    Resume: give CTIDExpr \(or a system\-column Var\) its source binding, have the scan emit it as a column, and
    resolve only against RTE\_RELATION bindings\.
  - Gate: correctness\-only under the S7 carve\-out; no plan instrument\.
- [ ] **M0146\-0239 — `xmin`, `xmax`, `cmin` and `cmax` system columns do not resolve** \(filed 2026\-10\-20 by
  M0146\-0236\)\.
  Kind: impl
  Parent: M0146-0236
  - Repro: `SELECT xmin, xmax, cmin, cmax FROM t` errors 42703 `column "xmin" does not exist`; PG returns the heap
    tuple header's values \(heap\_getsysattr\)\.
  - Resume: resolve them as system\-column Vars of their relation \(the same design as M0146\-0238\) and read
    t\_xmin, t\_xmax \(HeapTupleHeaderGetRawXmax\) and the combo\-cid–aware cmin/cmax from the scanned tuple\.
  - Gate: correctness\-only under the S7 carve\-out; no plan instrument\.
- [x] **M0146\-0217 — sort\-strategy: TPC\-DS Q38/Q87 — PG dedups each DISTINCT set\-operation leg per worker \(Unique under
  Gather Merge\), goopg sorts only** \(filed 2026\-10\-16 by the S9 pass at 4334a3714\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q38 \(and Q87, same shape\), category sort\-strategy, verdict SHAPE\-DIFF at depth 5\. PG:
    `SetOp Intersect → Unique → Gather Merge → Unique → Sort` \(a per\-worker Unique over the sorted partial path,
    create\_partial\_distinct\_paths, planner\.c:4852\)\. goopg: `Unique → Gather Merge → Sort`, with no worker\-side
    Unique\. The same record appears at SF1 \(Q38\)\. Census `analysis/m0146/m0146\-0014/routing\-20261016y/`\.
  - S9 ranking \(actionable first divergences, both TPC\-DS corpora, live\-task records excluded\): sort\-strategy 8,
    join\-order 8, aggregation\-strategy 7, parallelism 5\. Tie broken toward the class with one repeated mechanism
    \(Q38/Q87 at both scales\)\.
  - First step: goopg already ports create\_partial\_distinct\_paths \(optimizer/distinctpaths\.go
    addPartialDistinctPaths\)\. Find why a DISTINCT that is a set\-operation leg \(the INTERSECT/EXCEPT inputs\) does
    not get it: whether the leg is planned through a path that skips the upper\-rel DISTINCT pipeline, or the
    partial path is generated and loses on cost\. Instrument addPath first \(memory
    planner\_verify\_both\_candidates\_generated\)\.
  - Expected instrument: TPC\-DS PLAN\-PARITY match at SF0\.25 \(Q38, Q87\), then SF1\.
  - **LANDED 2026\-10\-16 \(loop \#60\).** Design `docs/design/0100\-0149/m0146\-0217\-partial\-distinct\-final\-rows\.md`;
    evidence `analysis/m0146/m0146\-0217/`\.
    - Both arms were generated, and goopg's split arm cost PG's to the cent \(8918\.69\.\.9031\.81\)\. A scratch\-PG probe
      showed PG's split arm reports 890 rows against the unsplit 891: create\_final\_distinct\_paths runs over the
      partial distinct rel, so it estimates over the Gather Merge's crossing rows\. add\_path keeps the fewer rows
      among fuzzily equal costs\. Ported as splitFinalRows on both split arms\.
    - Movement: Q38 MATCHES PG on an SF0\.25 clone with PG statistics imported\. Shipped SF0\.25 is unchanged \(86/99,
      categories identical; goopg's per\-worker estimate is 387 against PG's 371\)\. Shipped SF1 goes 74→73: Q87
      MATCH→SHAPE\-DIFF, which matches PG again with PG SF1 statistics imported\. Filed as M0146\-0218 \(R3\)\.
    - Pin: TestPartialDistinctSplitArmReestimatesOverCrossingRows, mutation\-checked\.
    - Gates: units, tpch\-spotcheck, acceptance arm \(24 MATCH on values\), sf025 PASS=99 same=99, tpcds\-fireset
      \(Q38/Q87 SF1 results byte\-identical\), TestPort\_RegressSuite\.
Movement: none — instrument artefact — the shipped census plans from goopg's own ANALYZE sample \(SF0\.25 86/99 held; SF1 74→73, Q87 filed M0146\-0218\); the PG\-statistics clones move Q38 \(SF0\.25\) and Q87 \(SF1\) to MATCH
- [x] **M0146\-0218 — TPC\-DS SF1 Q87 MATCH→SHAPE\-DIFF after M0146\-0217's PG\-faithful split\-arm rows: estimate\-driven**
  \(filed 2026\-10\-16 by M0146\-0217 under R3\)\.
  Kind: regression
  Parent: M0146-0014
  - Record: SF1 Q87, categories join\-order/join\-method/sort\-strategy\. On goopg's own ANALYZE statistics, a
    DISTINCT leg's Gather Merge crossing rows fall below the leg's DISTINCT estimate where PG's do not, so goopg now
    keeps the split arm \(`Unique → Gather Merge → Unique → Sort`\) and PG keeps the unsplit one\.
  - Evidence that this is an input gap, not logic: a private SF1 clone with `pg_dump \-\-statistics` from the
    reference imported plans Q87 exactly as PG does \(`analysis/m0146/m0146\-0217/probe/g18\-q87\-imp\.txt`\)\.
  - `[!]` blocked on the owner: clears when the reference statistics are imported into the SF1 corpus \(owner decision, M0146\-0215\)\. An
    engine\-side fix would have to make goopg's ANALYZE sample PG's rows, which M0146\-0215 slice 1 showed is not
    reproducible\.
  - Expected instrument: SF1 PLAN\-PARITY match 73→74 \(Q87\) after the import\.
  - **Closed 2026\-10\-19 by the owner's import** \(OWNER DECISIONS 2026\-10\-19\): the reloaded SF1 corpus carries
    the reference's statistics; Q87 MATCHes PG \(`analysis/m0146/owner\-ops\-20261019/census\-reload\-pgstats\-sf1\-diff\.txt`\)\.
Movement: yes — SF1 PLAN\-PARITY 82→94 \(owner corpus reload \+ PG statistics import; Q87 MATCH\)
- [x] **M0146\-0219 — aggregation\-strategy: TPC\-DS Q61 — PG plans each scalar FROM\-subquery aggregate as `Finalize
  Aggregate → Gather → Partial Aggregate`, goopg as `Aggregate → Gather`** \(filed 2026\-10\-16 by the S9 pass at
  c2a4421fc\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q61, category aggregation\-strategy, verdict SHAPE\-DIFF at depth 3 \(under Nested Loop: PG
    Finalize Aggregate \| goopg Aggregate\)\. The same pattern appears at SF1 as Q13 and Q48 \(depth 0\)\. Census:
    `analysis/m0146/m0146\-0014/routing\-20261016z/`\.
  - The record survives PG\-exact statistics: the SF1 census with the reference's statistics imported \(85/99\) keeps
    Q13, Q23, Q26, Q48 and Q71 as aggregation\-strategy, and M0146\-0215's SF0\.25 import census keeps Q61\. So this
    is planner logic, not sample variance\.
  - S9 ranking: aggregation\-strategy 6, parallelism 4, sort\-strategy 3, join\-order 2, scan\-type 2\. This
    excludes live\-task records and the records that the PG\-statistics import resolves \(routed to M0146\-0215 /
    0218\)\.
  - Costs \(SF0\.25 Q61, first subquery\):
    - PG: Partial Aggregate 16964\.65, Gather 17964\.95, Finalize 17964\.98\.
    - goopg: parallel join 16965\.92, Gather 17966\.22, Aggregate 17966\.24\.
    The two are fuzzily equal, and add\_path's final tiny\-fuzz comparison keeps the strictly cheaper partial
    path\.
  - First step: check whether goopg generates the partial\-aggregate arm for a no\-GROUP\-BY aggregate in a FROM
    subquery \(create\_partial\_grouping\_paths / gather\_grouping\_paths, planner\.c\), and how it is priced against
    the plain arm\. Instrument addPath first \(memory planner\_verify\_both\_candidates\_generated\)\. Then compare
    SF1 Q13/Q48, the top\-level form\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match 86→87 \(Q61\), SF1 Q13/Q48\.
  - **LANDED 2026\-10\-16 \(loop \#62\).** Design
    `docs/design/0100\-0149/m0146\-0219\-scanjoin\-gather\-admission\.md`; evidence `analysis/m0146/m0146\-0219/`\.
    - The divergence did not depend on the FROM nesting: the first subquery on its own plans the same way\.
    - goopg's no\-split `Aggregate → Gather` arm beat the split arm \(17966\.24 against 17966\.27\)\.
    - A PG add\_path probe showed that PG never files that arm\. generate\_gather\_paths \(override\_rows false, rel
      rows\) puts the Gather on the topmost scan/join rel at 1017\.56\.\.17964\.74\. There the serial Nested Loop
      1002\.48\.\.18131\.32 dominates it: fuzz\-equal total, earlier start\. The grouped rel only aggregates the paths
      that survived\.
    - Ported as scanJoinRelKeepsGather, the twin of M0146\-0190's Gather Merge check\. It gates every arm built over
      the no\-split Gather\.
    - Census: SF0\.25 86→88 \(Q61, Q16\); SF1 73→74 \(Q16\); TPC\-H 22/22; no regressions\. SF1 Q13's aggregation now
      matches, and its first divergence moved deeper to qual\-placement\.
    - Residual aggregation\-strategy records at SF1, left to the next S9 pass: Q48 \(depth 0; join estimates 434 vs
      PG 531\), Q23, Q26 and Q71\.
    - Pin: TestScanJoinRelDropsATiedGather, mutation\-checked\.
    - Gates: units, tpch\-spotcheck, acceptance arm \(values identical\), sf025 PASS=99 \(plan\-shape changed=2: Q16,
      Q61\), tpcds\-fireset PASS \(fired results byte\-identical\), TestPort\_RegressSuite\.
Movement: yes — TPC\-DS SF0\.25 match 86→88, SF1 match 73→74
- [x] **M0146\-0220 — aggregation\-strategy: TPC\-DS SF1 Q48 — PG `Finalize Aggregate → Gather → Partial Aggregate`,
  goopg `Aggregate → Gather`** \(filed 2026\-10\-16 by the S9 pass at 9fcfbc795\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF1 Q48, category aggregation\-strategy, verdict SHAPE\-DIFF at depth 0\. Census:
    `analysis/m0146/m0146\-0014/routing\-20261016a/`\. The record persists with PG's statistics imported \(that
    clone's census is 86/99\)\.
  - S9 ranking \(live\-task and import\-routed records excluded\): aggregation\-strategy 4 \(SF1 Q23, Q26, Q48, Q71\),
    sort\-strategy 3, then parallelism, join\-order, scan\-type and qual\-placement at 2 each\.
  - Costs under PG statistics:
    - goopg no\-split `Aggregate → Gather`: 84021\.19\. Its Gather is priced at compute\_gather\_rows = 4; this is the
      M0146\-0219 ledger row\.
    - PG Finalize: 84021\.11\.
    - Had PG's scan/join\-rel Gather survived add\_path, PG's own plain arm would cost about 84020\.8 and win\.
  - First step: probe PG's topmost scan/join rel for Q48 \(tmp/pgdbg add\_path elog on T\_Gather, the M0146\-0219
    method\) to see which serial path rejects the Gather\. Then check whether goopg's sr\.Pathlist holds that
    candidate, and whether scanJoinRelKeepsGather runs at all for this input \(searchedJoinInputRelOf non\-nil\)\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match 74→75 \(Q48\); Q71/Q23 may share the mechanism\.
  - **LANDED 2026\-10\-17 \(loop \#64\).** Design `docs/design/0100\-0149/m0146\-0220\-scanjoin\-gather\-rel\-rows\.md`;
    evidence `analysis/m0146/m0146\-0220/`\.
    - The keep\-check did run\. With PG statistics, the no\-split `Agg → Gather` won at 84021\.19 against the split
      arm's 84021\.20, because its Gather was priced at compute\_gather\_rows = 4\.
    - A scratch\-PG add\_path probe on SF1 \(reference restored with `pg_dump \-\-statistics`\) shows PG's scan/join\-rel
      Gather carries rel\->rows = 5 \(override\_rows false\)\. Its plain arm is 84021\.03 and Finalize wins at
      84020\.94\.
    - Ported as scanJoinGatherRows: both no\-split boundaries, the Gather and the worker\-sort Gather Merge, are now
      sized at the rel's rows, replacing M0146\-0009n's compute\_gather\_rows\.
    - Shipped census: SF0\.25 88→87 \(Q5 lost\); SF1 74→74 \(Q71 gained, Q80 lost\); TPC\-H 22/22\.
    - With PG statistics: SF1 86→88 \(Q48, Q71; Q80 also matches, so its shipped flip is estimate\-driven\); SF0\.25
      91/99, where Q5 is lost even with PG statistics\. Filed M0146\-0221 \(R3\)\.
    - Pin: TestGatheredArmBoundariesCarryScanJoinRelRows, mutation\-checked\.
    - Gates: units, tpch\-spotcheck, acceptance arm \(values identical\), sf025 PASS=99 \(plan\-shape changed=21:
      row/cost text on the worker\-sort Gather Merge\), tpcds\-fireset PASS \(40 fired results byte\-identical\),
      TestPort\_RegressSuite\.
Movement: yes — PG\-statistics SF1 match 86→88 \(shipped SF1 74→74, shipped SF0\.25 88→87, Q5 filed M0146\-0221\)
- [x] **M0146\-0221 — aggregation\-strategy: TPC\-DS SF0\.25 Q5 MATCH→SHAPE\-DIFF after M0146\-0220 — goopg's split arm
  wins the `wsr` GroupAggregate near\-tie that PG's no\-split arm wins** \(filed 2026\-10\-17 by M0146\-0220 under R3\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q5, aggregation\-strategy, depth 5 \(under Subquery Scan: PG GroupAggregate \| goopg
    Finalize GroupAggregate\)\. The record persists with PG's statistics imported\. SF1 Q80 shows the same pattern on the
    shipped corpus, but matches under PG statistics\.
  - Numbers \(goopg with PG statistics\):
    - no\-split `GroupAggregate → Sort → Gather`, 6 rows: 18000\.49\.\.18000\.70\.
    - split `sortinput`, Finalize over Gather Merge, 5 rows: 17999\.85\.\.18000\.65\. It wins on the tight fuzz\.
    - PG reference: no\-split 17642\.09\.\.17642\.30\.
    - A scratch\-PG restore of tpcds025 rejects PG's split arm by about 0\.05\. That restore estimates 7 rows for wsr,
      not 6, so it is not exact\.
  - Also differs: goopg's `Subquery Scan` over the UNION ALL leg costs 8985 against PG's 8447\.
  - First step: get an exact PG replica for Q5 \(or a reduced wsr\-only query on which the restore and the reference
    agree\), then compare the split arm term by term against create\_partial\_grouping\_paths and cost\_gather\_merge\.
    Evidence is in `analysis/m0146/m0146\-0220/probe/`\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match 87→88 \(Q5\)\.
  - **Slice 1 LANDED 2026\-10\-17 \(loop \#65\): crossing rows are compute\_gather\_rows\.** Design
    `docs/design/0100\-0149/m0146\-0221\-split\-arm\-cost\-terms\.md`; evidence `analysis/m0146/m0146\-0221/`\.
    - Exact oracle: `pg_basebackup` of :65438 under the tmp/pgdbg debug build reproduces the reference plan exactly\.
      A dump restore does not, because heap page counts scale reltuples\.
    - PG's wsr arms: no\-split 17642\.3027, accepted \(identical in goopg after the \+358\.40 input shift\); split
      17642\.3286, rejected\. goopg's split arm was 0\.0756 cheaper\.
    - That gap is two terms\. \(1\) Unclamped crossing rows: 4\.8 against PG's 5, fixed in this slice\. \(2\) Missing
      deserialfn cost in the Finalize, 0\.05: slice 2\.
    - Census: SF1 74→75 \(Q80 → MATCH\); SF0\.25 87 and TPC\-H 22 unchanged\.
    - Pin: TestSplitArmBoundariesCarryComputeGatherRows, mutation\-checked\.
  - **Slice 2 \(next\): port get\_agg\_clause\_costs** \(costsize\.c / clauses\.c\)\.
    - Per\-aggregate transfn/combinefn per row, deserialfn per row when deserializing, serialfn per group when
      serializing, finalfn per group unless skipfinal, all from pg\_proc\.procost\.
    - Needs a leaf\-package pg\_aggregate cost table, because the optimizer cannot import initdb, plus agg\-call →
      aggfnoid resolution, and costAgg taking per\-aggregate terms and a split mode\.
    - Expected instrument: Q5 → MATCH \(split 17642\.3265 against no\-split 17642\.3027\)\.
  - **Slice 2 LANDED 2026\-10\-17 \(loop \#66\).**
    - The table: cmd/gen\-pg\-proc\-data \-aggcost generates internal/catalog/builtin\_agg\_costs\_gen\.go from
      pg\_aggregate\.dat and pg\_proc\.dat \(161 aggregates, procost per support function, transition identity\)\.
    - The costing: optimizer aggClauseCosts \(SIMPLE / INITIAL\_SERIAL / FINAL\_DESERIAL, AggInfo and AggTransInfo
      sharing\) feeds costAggClause at every grouping arm; grouping sets stay legacy\.
    - The cross\-check: TestBuiltinAggCostTableAgreesWithPgAggregate \(initdb\) checks the table against the
      bootstrapped pg\_aggregate rows\.
    - Census: SF0\.25 87→88 \(Q5\), SF1 75→76 \(Q52\), TPC\-H 22/22, no regressions\. With PG statistics: SF0\.25
      91→92, SF1 88→90 \(Q26, Q52\)\.
    - Pins: TestAggClauseCostsPortsGetAggClauseCosts, TestLookupBuiltinAggCostResolvesOverloads\.
      TestExplainLimitClampsToInputRows re\-derived: count\(\*\) has no finalfn; the HAVING qual\-cost gap is a ledger row\.
    - Gates: units, tpch\-spotcheck, acceptance arm \(values identical\), sf025 PASS=99 \(46 plan texts, cost numbers\),
      tpcds\-fireset PASS \(96 fired results byte\-identical\), TestPort\_RegressSuite\.
    - The first regress run after the fire set failed union / varchar / vacuum\_parallel\. The rerun on the same code
      passed all three, and vacuum\_parallel passes standalone: nondeterministic\.
Movement: yes — TPC\-DS SF0\.25 match 87→88 \(Q5\), SF1 match 74→76 \(Q80 slice 1, Q52 slice 2\)
- [x] **M0146\-0222 — sort\-strategy: TPC\-DS Q72 — PG sorts below the joins \(Gather Merge → Sort\) and keeps the order
  through its Nested Loops; goopg sorts once above them** \(filed 2026\-10\-17 by the S9 pass at e3a7ae742\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q72, category sort\-strategy, verdict SHAPE\-DIFF at depth 3 \(under GroupAggregate: PG
    Nested Loop Left \| goopg Sort\)\. The same record appears at SF1 Q72, and both persist under PG statistics\.
    Census `analysis/m0146/m0146\-0014/routing\-20261017b/`\.
  - S9 ranking \(records persisting under PG statistics\): sort\-strategy 3, then parallelism, join\-order and
    scan\-type at 2 each\.
  - goopg's plan is cheaper by its own costing \(15267\.53 against PG's 15490\.83\)\.
  - First step: on the exact PG replica \(tmp/m21\-refclone, the pg\_basebackup of the reference\), probe whether PG
    generates goopg's `GroupAggregate → Sort → join tree` candidate, and at what cost\. Use the upper\-rel add\_path
    elog pattern of M0146\-0221 probe/\. Then compare goopg's join\-tree costs against PG's term by term\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match 88→89 \(Q72\), then SF1\.
  - **Slice 1 LANDED 2026\-10\-17 \(loop \#68\): get\_memoize\_path's inner\_unique test\.** Design
    `docs/design/0100\-0149/m0146\-0222\-memoize\-inner\-unique\-and\-loop\-count\.md`; evidence `analysis/m0146/m0146\-0222/`\.
    - Exact replica: PG's input rel has the sorted NL at 15490\.79, which it picks, and an unsorted NL at 15491\.68\.
      goopg's unsorted join was 15267\.48\.
    - Difference 1: goopg memoized a unique date\_dim d2 probe that keeps a join filter; PG refuses it\. Ported as
      extraInnerUnique plus the nliMemoizeCandidate gate\.
    - Difference 2: goopg's d3 probe has loop\_count 1, because its outer is a prebuilt search leaf with rows=1; PG uses
      catalog\_sales's 360397\. That is slice 2\.
    - Census: verdicts unchanged \(SF0\.25 88, SF1 76, TPC\-H 22\); SF0\.25 CATEGORIES\-EXCL\-MATCH parameterisation 3→2\.
    - Pin: TestNLIMemoizeRefusesUniqueJoinWithFilter, mutation\-checked\.
    - Gates: units, spotcheck, acceptance arm, sf025 PASS=99 \(Q72 plan text changed\), fireset PASS \(byte\-identical\),
      TestPort\_RegressSuite\.
  - **Slice 2 LANDED 2026\-10\-17 \(loop \#69\): get\_loop\_count through a sub\-problem leaf\.**
    - A sub\-problem leaf records baseRelInfo\.subproblemBaseRows, keyed by source table\. loopCountForKeys maps a
      probe's outer values through SourceTableIdx to those rows, at the param index, skip and bitmap producers\.
    - The d3 probe is now 0\.29\.\.0\.33, PG\-identical; census verdicts unchanged\.
    - Pin: TestLoopCountReadsSubproblemBaseRows, mutation\-checked\.
    - Gates: units, spotcheck, acceptance arm, sf025 PASS=99, tpcds\-fireset PASS on rerun \(the first run stalled on a
      4\-minute SF1 clone start past the capture's 120 s readiness window\), TestPort\_RegressSuite\.
  - **Slice 3 recon \(loop \#70\): the arms' order is decided by add\_path startup tie\-breaks\.** Design §6;
    `analysis/m0146/m0146\-0222/probe3/`\.
    - The two 8\-rel Nested Loops are fuzz\-equal\. Under consider\_startup the incumbent wins COSTS\_DIFFERENT, and both
      engines generate the pairs in the same order\. But goopg's 0\.13 startup sits on the other pair, so it keeps the
      cheaper\-total unsorted path\.
    - Root: PG's base parallel subtree starts at 1159\.32, goopg's at 1159\.45\. The difference originates in the 6\-rel
      partial paths around the d2 join; next, log add\_partial\_path on both sides\.
  - **Slice 3 LANDED 2026\-10\-17 \(loop \#71\): sub\-problems consider startup; the winner keeps its ordering\.**
    - The 0\.13 was PG's Material/Seq\-Scan warehouse join \(startup 0\), which goopg filed but then let a
      cheaper\-total index join evict\. goopg searched sub\-problems with tuple fraction 0, so consider\_startup was off\.
      makeRelFromJoinlist now passes the fraction through, as relnode\.c sets consider\_startup on every rel\.
    - The sub\-problem is then won by its Gather Merge → Sort, whose ordering the boundary dropped \(only runner\-ups'
      orderings were translated\)\. addCTEScanPathkeys now gives the winning leaf its ordering too\.
    - Census: SF0\.25 88→89 \(Q72 MATCH\); SF1 76 \(Q72 now diverges at depth 7, qual\-placement\); TPC\-H 22/22\.
    - Pins: TestSubproblemSearchConsidersStartup, TestSubproblemWinnerLeafCarriesItsOrdering; both mutation\-checked\.
    - Gates: units, spotcheck, acceptance arm, sf025 PASS=99 \(Q72 plan text changed\), fireset PASS \(Q72 results
      byte\-identical at both scales\), TestPort\_RegressSuite\.
Movement: yes — TPC\-DS SF0\.25 match 88→89 \(Q72\)
  - Residual, for the next S9 pass: SF1 Q72 qual\-placement at depth 7\.
    - With PG statistics, goopg's sorted arm is 15474\.87 against unsorted 15474\.80, a 0\.07 margin; PG has sorted
      15490\.79 against unsorted 15491\.68, a 0\.89 margin the other way\.
    - Compare the sub\-problem's gather\.merge\.sort \(15459\.75\) and gather \(15459\.68\) arms term by term against PG's
      on the replica \(probe2/\)\.
    - Index geometry, a 16 shift, is owner\-gated \(M0146\-0216\)\. The sub\-problem boundary passing no partial paths is
      ledgered\.
- [x] **M0146\-0223 — parallelism: TPC\-DS Q39 — PG joins `item` above the Gather in the `inv` CTE; goopg joins it
  inside the workers** \(filed 2026\-10\-17 by the S9 pass at 472e41064\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q39, category parallelism, verdict SHAPE\-DIFF at depth 4 \(under HashAggregate: PG Hash
    Join Inner \| goopg Gather\)\. The same record appears at SF1, and both persist under PG statistics\. Census
    `analysis/m0146/m0146\-0014/routing\-20261017c/`\.
  - S9 ranking: parallelism, qual\-placement, scan\-type and join\-order tie at 2\. Parallelism wins as one mechanism at
    both scales\.
  - PG: `HashAggregate → Hash Join \(item, Index Only Scan item\_pkey 531\.08\) → Gather → Hash Join \(warehouse\) → Parallel
    Hash Join \(inventory, date\_dim\)`\. goopg: `HashAggregate → Gather → Hash Join \(warehouse\) → Hash Join \(item, IOS
    478\.29\) → Parallel Hash Join`\.
  - First step: on the exact PG replica \(tmp/m21\-refclone\), probe the 4\-rel joinrel's add\_path and
    add\_partial\_path for the item join, then compare goopg's DPPATH lines under PG statistics\. Check the item\_pkey IOS
    cost too: allvisfrac \(relallvisible\) or index geometry\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match 89→90 \(Q39\), then SF1\.
  - **LANDED 2026\-10\-17 \(loop \#73\): truncate\_useless\_pathkeys' grouping and distinct arms\.** Design
    `docs/design/0100\-0149/m0146\-0223\-pathkeys\-useful\-for\-grouping\.md`; evidence `analysis/m0146/m0146\-0223/`\.
    - Replica: PG's 4\-rel Merge Join partial \(\.\.25201\.06, ordered on inv\_item\_sk\) evicts the Hash Join partial
      \(25108\.41\) on pathkeys within fuzz; its Gather \(27377\.76\) loses to the serial item join \(27306\.05\)\.
    - goopg filed the same Merge Join with pathkeys=0: only the merging and ordering arms were ported, and
      i\_item\_sk is a group key but not the leading one\. The IOS 478\.29 vs 531\.08 is relallvisible \(it matches under
      PG statistics\) and is not the cause\.
    - Fix: group\_pathkeys / distinct\_pathkeys ride to searchCtx; leadingMembersOf is list\_member\_ptr on canonical
      pathkeys \(sameClass over every equivalence class\)\. The set\-op arm has no setop\_pathkeys producer\.
    - Census: SF0\.25 89→90, SF1 76→77 \(Q39 MATCH at both\), TPC\-H 22/22; SF0\.25 under PG statistics 93→94\.
    - Pin: TestTruncateUselessPathkeysGroupingAndDistinct, mutation\-checked\.
    - Gates: units, spotcheck, acceptance arm, sf025 PASS \(Q39 plan text changed\), fireset PASS \(Q39 results
      byte\-identical at both scales\), TestPort\_RegressSuite\.
Movement: yes — TPC\-DS SF0\.25 match 89→90, SF1 76→77 \(Q39\)
- [x] **M0146\-0224 — qual\-placement: TPC\-DS Q78 — PG commutes the two outer joins to \(ss ⟕ cs\) ⟕ ws; goopg keeps
  \(ss ⟕ ws\) ⟕ cs** \(filed 2026\-10\-17 by the S9 pass at 1a8760b93\)\.
  Kind: impl
  Movement: yes — SF0\.25 match 90→91, SF1 77→78 \(PLAN\-PARITY census4, Q78 MATCH at both scales\)\.
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q78, category qual\-placement, verdict SHAPE\-DIFF at depth 2 \(under Sort: PG Merge Left
    Join \| goopg Merge Left Join\)\. The same swap is the SF1 join\-order record at depth 2\. Both persist under PG
    statistics\. Census `analysis/m0146/m0146\-0014/routing\-20261017d/`\.
  - S9 ranking: qual\-placement, scan\-type and join\-order tie at 2\. Q78 is one mechanism behind two of the tied
    categories, at both scales\.
  - PG: top `Merge Left Join` on ws \(42027\.86\.\.42481\.11\) over `Merge Left Join` on cs\. goopg: top on cs
    \(42027\.89\.\.42542\.55\) over the ws join\. goopg's chosen plan is 61 dearer than PG's, so either goopg never
    builds PG's join order, or it prices that order higher than PG does\.
  - First step: on the exact PG replica \(tmp/m21\-refclone\), log add\_path for the 3\-rel joinrel \(ss, ws, cs\)\. Then
    compare goopg's DPPATH lines under PG statistics: is the \(ss ⟕ cs\) ⟕ ws pair generated \(join\_is\_legal / outer\-join
    identity 3\), and at what cost\?
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match 90→91 \(Q78\), then SF1\.
  - **Slice 1 LANDED 2026\-10\-17 \(loop \#75\): outer\-join identity 3 over derived leaves\.** Design
    `docs/design/0100\-0149/m0146\-0224\-outer\-join\-identity3\-derived\-leaves\.md`; evidence
    `analysis/m0146/m0146\-0224/`\.
    - goopg's top search never built \{ss, cs\}: the cs link's min\_lefthand was \{ss, ws\}, its syntactic side\.
      resolveSjiColumn gave up on every unqualified column while a subquery/CTE leaf was in scope, and Q78's ON
      clauses name the CTE columns bare\.
    - Fix: derivedLeafColumns gives a table\-less leaf its output names when all are certain \(CTE schema, aliased or
      bare\-column or function targets, column alias list\), as PG's eref\-\>colnames; the unqualified arm scans it like
      a table\.
    - Census: verdicts unchanged \(TPC\-H 22/22, SF0\.25 90, SF1 77\)\. SF1 Q78 now has PG's join order; its first
      divergence moved from join\-order to qual\-placement at depth 2, the SF0\.25 record\.
    - Pin: TestScopedUnqualifiedResolvesDerivedLeaves, mutation\-checked\.
    - Gates: units, spotcheck, acceptance arm, sf025 PASS=99 \(no plan changed\), fireset PASS \(SF1 Q78
      byte\-identical\), TestPort\_RegressSuite, internal/postmaster\.
  - **Slice 2 LANDED 2026\-10\-17 \(loop \#76\): a derived leaf pays cost\_subqueryscan once\.** Design §5;
    `analysis/m0146/m0146\-0224/probe2/`, `census2/`, `fireset2/`\.
    - final\_cost\_mergejoin's terms agreed on both sides \(mergejointuples 16/28, rescanratio 1, forced Material\)\.
      The inputs differed: goopg's CTE leaves entered at 0\.03 per row over their aggregates where PG has 0\.01\. The
      SubqueryScan and the Project each added cpu\_tuple\_cost before costSubplanLeaf added cost\_subqueryscan's own\.
    - Fix: subplanLeafSubpathCost peels the SubqueryScan and a Project over a projection\-capable node
      \(is\_projection\_capable\_path, create\_projection\_path's dummypp arm\)\.
    - Q78's 2\-rel merges now equal PG's \(28283\.61 against 28283\.63\)\. Verdicts unchanged; 13 TPC\-DS plans per
      scale and TPC\-H Q18 changed cost only\.
    - Pin: TestSubplanLeafPricesCostSubqueryscanOnce, mutation\-checked\. Gates: units, spotcheck, acceptance arm,
      sf025 PASS=99, fireset PASS \(26 fired results byte\-identical\), TestPort\_RegressSuite\.
  - **Slice 3 LANDED 2026\-10\-17 \(loop \#77\): delayed WHERE conjuncts are pushed\-down restrictions of the top
    joinrel\.** Design §7; `analysis/m0146/m0146\-0224/census3/`, `fireset3/`\.
    - topDelayedConjuncts picks the held conjuncts whose check\_outerjoin\_delay placement is the join of every leaf\.
      The root problem carries them as restrictInfos with a pushedDown bit \(RINFO\_IS\_PUSHED\_DOWN\)\.
    - The full joinrel sizes them through pselec \(rows 762, as PG\) and charges them as qp\_qual in every join method\.
      They are never keys, join quals or part of the unique/semi tests\.
    - The Filter above the tree applies them, stamped with the winning path's cost\.
    - orRangeSelectivity: an unmeasured inequality arm with no statistics is DEFAULT\_INEQ\_SEL, not 0\.5\.
    - Q78 now has PG's structure at both scales and is the only plan that changed\. It is still SHAPE\-DIFF on qual
      text\. Pins: TestTopDelayedConjunctsClosesOverOuterJoins, TestPushedDownRestrictionIsNeverAJoinKey,
      TestOrRangeArmDefaultsToIneqSel\.
    - Gates: units, spotcheck, acceptance arm, sf025 PASS=99 \(Q78 changed\), fireset PASS \(Q78 byte\-identical at
      both scales\), TestPort\_RegressSuite\.
  - Slice 4 \(next\): EXPLAIN qual text through a removed SubqueryScan\. PG's setrefs replaces the upper Vars with the
    subplan's tlist expressions, so it prints web\_sales\.ws\_item\_sk, web\_sales\.ws\_bill\_customer\_sk and
    sum\(web\_sales\.ws\_quantity\)\. goopg prints the CTE output names ws\_item\_sk, ws\_customer\_sk and ws\_qty\. These
    are the differ's remaining Merge Cond and JoinQual signatures \(design §8\)\.
    - 2026\-10\-19: implemented in tree and staged, NOT committed \(design §8\.1; backup
      `tmp/m0224\-slice4\-wip\.patch`\)\. Semi/anti join keys map to the left input \(joinOutputSide\), a join's
      attached Filter and the Sort keys over the search boundary chase to the subplan target, and a non\-Var target
      is parenthesised as get\_variable does\.
    - Census with it \(`census4/`\): SF0\.25 90→91, SF1 77→78 \(Q78 MATCH at both scales\), TPC\-H 22/22 with Q18's
      Sort Key text\-identical\. No other plan text changed\.
    - Pin: TestStrippedCTELeafQualsDeparseSubplanTargets, mutation\-checked against all three arms\. Units PASS\.
    - Blocked on gates: tpch\-spotcheck and the acceptance arm are SKIP\-BLOCKED because `:65433`'s data dir is
      under HOLD \(unclean shutdown 2026\-10\-17 18:17, owner\-only\); `:65432` and `:65438` hold too\. The sf025
      sweep refused while the 2026\-10\-19 nightly batch ran\. Still to run: sf025, fireset, TestPort\_RegressSuite\.
    - 2026\-10\-19 02:12: sf025 sweep PASS on the staged index \(99 compared, no verdict change, only Q78's plan text
      changed; stamp tmp/gate\-stamps/tpcds\-sf025\.json, code\_tree e6d93c0b\)\. TestPort\_RegressSuite PASS \(459 s\)\.
      Fireset is not required: the staged set is internal/executor only, outside the fire\-set scope\. Only
      tpch\-spotcheck and the acceptance arm remain, both HOLD\-blocked\. Re\-run sf025 if the staged internal/ index changes\.
    - 2026\-10\-19 06:41: the owner released the HOLDs \(`*.HOLD.released-20261019`; ensure fix 8ea3190a7\)\.
      tpch\-spotcheck PASS \(Q12=2, Q13=35\) and acceptance arm m24e PASS \(24/24 MATCH on values,
      `analysis/m0146/m0146\-0224/accept4`\), both on code\_tree e6d93c0b, the same tree as the sf025 stamp\.
  - **Slice 4 LANDED 2026\-10\-19: EXPLAIN qual text through the stripped CTE SubqueryScan\.** Task closed\.
- [x] **M0146\-0225 — scan\-type: TPC\-DS SF1 Q53/Q63 — PG probes `store` with an Index Only Scan under Memoize,
  goopg with a full\-width Index Scan** \(filed 2026\-10\-19 by the S9 pass at 0d94a7133\)\.
  Kind: impl
  Movement: yes — SF1 match 78→80 \(Q53, Q63\); CATEGORIES\-EXCL\-MATCH scan\-type SF1 12→10
  Parent: M0146-0014
  - Record: TPC\-DS SF1 Q53 \(and Q63, same node\), category scan\-type, verdict SHAPE\-DIFF at depth 9 under Memoize\.
    PG `Index Only Scan using store_pkey on store \(cost=0\.14\.\.0\.15 rows=1 width=4\)`; goopg `Index Scan using
    store_pkey on store \(cost=0\.14\.\.0\.15 rows=1 width=1812\)`\. Both persist under PG statistics\. Census
    `analysis/m0146/m0146\-0014/routing\-20261019a/`\.
  - S9 ranking \(persisting records, live\-task and import\-routed records excluded\): scan\-type 2, every other
    category 1\.
  - First step: the costs are identical and goopg's width is the whole row\. Find out whether goopg builds the
    index\-only path for the Memoize's inner rel \(check\_index\_only over the rel's attr\_needed: only `s_store_sk` is
    referenced\), and if it does, how the cost tie is resolved\. PG's add\_path keeps the index\-only path\. Instrument
    addPath first \(memory planner\_verify\_both\_candidates\_generated\), then use the SF1 PG\-statistics clone recipe
    `tmp/s9\-imp/run\.sh`\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match \(Q53, Q63\), 78→80 on the shipped corpus\.
  - **LANDED 2026\-10\-19\.** Design `docs/design/0100\-0149/m0146\-0225\-window\-needed\-columns\.md`; evidence
    `analysis/m0146/m0146\-0225/`\.
    - Bisected on an SF1 clone: the join block alone gets the index\-only probe; adding the window function loses it\.
      `neededColumnNames` declined any OVER or WINDOW clause, so the needed set was unknown and pathparamindex\.go
      never marked the probe index\-only\.
    - Fix: collectWindowDefColumnNames walks PARTITION BY / ORDER BY \(PG build\_base\_rel\_tlists\), plus frame offsets
      for safety; the WINDOW clause is walked in the needed set and its output\-set sibling\.
    - Ten window queries change plan text at each scale; apart from Q53/Q63 only widths shrink \(narrowing now reaches
      them\), costs unchanged\. SF0\.25 stays 91/99\.
    - Pin: TestWindowFunctionKeepsNeededSet, mutation\-checked\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24/24 values\), sf025 PASS \(10 plans
      changed\), tpcds\-fireset PASS \(20 fired results byte\-identical\)\.
- [x] **M0146\-0226 — sort\-strategy: TPC\-DS SF0\.25 Q4 — PG finishes with an Incremental Sort over a merge\-joined
  pair of sorted CTE scans; goopg nested\-loops unsorted CTE scans under a full Sort** \(filed 2026\-10\-19 by the S9
  pass at fe64139d5\)\.
  Kind: impl
  Movement: yes — SF0\.25 match 91→92 \(Q4\), SF1 match 80→81 \(Q64\); CATEGORIES\-EXCL\-MATCH SF0\.25 sort\-strategy 4→3
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q4, category sort\-strategy, verdict SHAPE\-DIFF at depth 1 \(under Limit: PG Incremental
    Sort \| goopg Sort\)\. Persists under PG statistics\. Census `analysis/m0146/m0146\-0014/routing\-20261019b/`\.
  - S9 ranking: a five\-way tie at 1 \(SF0\.25 Q4, SF0\.25 Q69, SF1 Q23, SF1 Q72, SF1 Q77\); SF0\.25 preferred, then the
    lower query id and shallower divergence\.
  - Already different under identical statistics: CTE scan estimates \(goopg 9 / 29 rows, PG 10 / 30\) and the
    year\_total HashAggregate cost \(goopg 46822\.64\.\.55046\.66, PG 53401\.85\.\.61625\.87, same input\)\.
  - First step: on the SF0\.25 PG\-statistics clone \(`tmp/s9\-imp/run\.sh` recipe\), attribute the CTE\-scan row gap
    \(the year\_total filter's selectivity over the CTE's column statistics, PG's set\_cte\_size\_estimates /
    clauselist\_selectivity\) and the HashAggregate cost gap \(cost\_agg AGG\_HASHED\); then check whether the merge
    join \+ Incremental Sort arm is generated \(instrument addPath\) and at what cost\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match \(Q4\), 91→92 on the shipped corpus\.
  - **LANDED 2026\-10\-19\.** Design `docs/design/0100\-0149/m0146\-0226\-filter\-rows\-clamp\-row\-est\.md`; evidence
    `analysis/m0146/m0146\-0226/`\.
    - The CTE scans already cost PG's numbers; only their rows differed\. A trace showed PG's exact selectivities
      \(2\.5e\-5, 8\.33e\-6\); EstimateRows\(\*Filter\) truncated 29\.87→29 and 9\.96→9 where clamp\_row\_est rounds\.
    - Fix: the Filter arm uses clampRowEst \(rint\), like its sibling applyLocalFilterSelectivity\. The legacy
      join\-fallback scaleByFloat callers are untouched \(ledger\)\.
    - Eleven SF0\.25 plans change text through rows rounded up; SF1 Q64 also matches now \(M0146\-0214's record\)\.
    - Open \(ledger\): the year\_total HashAggregate cost gap; the display\-path CTE scan qual cost\.
    - Pin: TestFilterRowsRoundLikeClampRowEst, mutation\-checked\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24/24 values\), sf025 PASS=99 \(11 plans
      changed\), tpcds\-fireset PASS \(25 fired results byte\-identical\)\.
- [x] **M0146\-0227 — join\-method: TPC\-DS SF0\.25 Q69 — PG anti\-joins catalog\_sales on top with a Hash Right Anti
  Join; goopg puts a Nested Loop Anti Join with a Join Filter on web\_sales there** \(filed 2026\-10\-19 by the S9 pass
  at 18d2c73b0\)\.
  Kind: impl
  Movement: yes — SF1 match 81→82 \(Q69\), PG\-statistics SF0\.25 match 96→97 \(Q69\); CATEGORIES\-EXCL\-MATCH SF1 qual\-placement 4→3
  Parent: M0146-0014
  - Record: TPC\-DS SF0\.25 Q69, category join\-method, verdict SHAPE\-DIFF at depth 3 \(under Sort: PG Hash Right Anti
    Join \| goopg Nested Loop Anti Join\)\. Persists under PG statistics\. Census
    `analysis/m0146/m0146\-0014/routing\-20261019c/`\.
  - S9 ranking: a four\-way tie at 1 \(SF0\.25 Q69, SF1 Q23, SF1 Q72, SF1 Q77\); SF0\.25 preferred\.
  - goopg's plan costs 43183\.05 against PG's 43130\.36, so either PG's order is not generated or goopg prices it
    higher\. The EXISTS leg differs already: Nested Loop over the store\_sales HashAggregate 19132\.67\.\.19430\.53
    \(goopg\) against 19132\.67\.\.19681\.46 \(PG\)\.
  - First step: on the SF0\.25 PG\-statistics clone, instrument addPath for the top joinrel to check whether PG's
    arm \(the catalog\_sales Hash Right Anti Join over \(customer ⋈ EXISTS\) ⟕anti web\_sales\) is generated and at
    what cost\. Attribute the 251 gap on the EXISTS Nested Loop \(inner index probe into customer at 858 loops\)
    against PG's cost\_nestloop / cost\_index terms\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match \(Q69\), 92→93 on the shipped corpus\.
  - **Slice 1 LANDED 2026\-10\-19: semijoin loop count over a multi\-relation RHS\.** Design
    `docs/design/0100\-0149/m0146\-0227\-multirel\-semijoin\-loop\-count\.md`; evidence `analysis/m0146/m0146\-0227/`\.
    - The EXISTS leg's customer\_pkey probe cost 0\.29\.\.0\.33 against PG's 0\.63 with identical statistics: goopg's
      adjustRowcountForSemijoins sized only a one\-relation RHS, so the loop count stayed at store\_sales' 719876 rows
      where PG's approximate\_joinrel\_size \+ estimate\_num\_groups give ss\_customer\_sk's 39023\.
    - multiRelSemiRhsUniqueRows ports the multi\-relation arm; the probe now costs PG's 0\.63\.
    - No verdict moved \(SF0\.25 92, SF1 81, TPC\-H 22\); Q10/Q35/Q69 cost\-only\.
    - Pin: TestLoopCountClampsMultiRelSemijoinRHS, mutation\-checked\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24/24\), sf025 PASS=99 \(3 plans
      changed\), tpcds\-fireset PASS \(5 fired results byte\-identical\)\.
  - Slice 2 \(in progress, 2026\-10\-19\): diagnosed; design §4\.
    - PG's top is generated \(right\_anti hash 32211\.13\.\.43141\.28 vs PG 32200\.20\.\.43130\.36\), but goopg's web\_sales
      NL anti \(43444\.91\) dominates it inside the 1% fuzz with a lower startup\.
    - PG never builds that NL anti's outer rel: the exact\-replica add\_path probe shows only \(c, ca, cd, EXISTS,
      web anti\) at seven members\. The cause is join\_collapse\_limit: the third pulled sublink meets 7 \+ 2 > 8, so
      PG plans the first seven as a sub\-problem\. goopg appends pulled leaves flat \(jointreepullup\.go\)\.
    - Fold patch `tmp/m0227/slice2\-collapse\-fold\-wip\.patch` splits Q69 like PG but panics: a sub\-problem
      holding pulled leaves does not publish the binding coordinates the enclosing tree reads \(createplanroot\.go
      boundaryMap\)\. Next: fix that publication, then re\-apply the fold\.
  - **Slice 2 LANDED 2026\-10\-19\.** Design §4/§5; evidence `analysis/m0146/m0146\-0227/census2/`\.
    - foldPulledBodyLeaves folds pulled sublink bodies under join\_collapse\_limit \(deconstruct\_recurse\)\.
    - The root filler licenses an item wholly inside a SEMI/ANTI RHS \(c11's rule for synthetic leaves\)\.
    - A sub\-problem leaf carries subproblemBaseVars, so a join Var inside it keeps its base relation's statistics
      \(examine\_variable / find\_base\_rel\); Q69's top anti join is back at PG's 91 rows\.
    - Q69 MATCHes PG under PG statistics at SF0\.25 and on the shipped SF1 corpus\. The shipped SF0\.25 residual
      \(depth 4\) is statistics\-driven and routes to the M0146\-0215 import\.
    - Pins: TestPulledSublinksFoldUnderJoinCollapseLimit, TestSubproblemLeafVarKeepsBaseRelStats, mutation\-checked\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24/24\), sf025 PASS=99 \(Q69 changed\),
      tpcds\-fireset PASS \(2 fired results byte\-identical\)\.
- [x] **M0146\-0228 — aggregation\-strategy: TPC\-DS SF1 Q23 — PG partially aggregates the best\_ss\_customer CTE under a
  Gather \(Finalize HashAggregate\); goopg aggregates it serially** \(filed 2026\-10\-19 by the S9 pass at 8465f5e72\)\.
  Kind: impl
  Movement: none — S3 instruments unchanged \(SF1 match 82, PG\-statistics 94, CATEGORIES\-EXCL\-MATCH identical\): Q23 statement 1 now MATCHes PG and the query's first divergence moved depth 5→1 onto statement 2 \(sort\-strategy\)
  Parent: M0146-0014
  - Record: TPC\-DS SF1 Q23, category aggregation\-strategy, verdict SHAPE\-DIFF at depth 2 \(under CTE: PG Finalize
    HashAggregate \| goopg HashAggregate\)\. Persists under PG statistics\. Census
    `analysis/m0146/m0146\-0014/routing\-20261019d/`\.
  - S9 ranking: a three\-way tie at 1, all SF1 \(Q23, Q72, Q77\); Q23 has the lowest id and the shallowest divergence\.
  - PG 189437\.85\.\.191187\.85: Finalize HashAggregate → Gather \(4\) → Partial HashAggregate → Parallel Hash Join\.
    goopg 179918\.25\.\.181501\.58: HashAggregate → serial Hash Join \(Seq Scan store\_sales ⋈ Index Only Scan
    customer\_pkey\)\. The CTE's HAVING reads `\(InitPlan 2\)\.col1`\.
  - First step: on an SF1 PG\-statistics clone, check whether the partial\-aggregation arm is generated for the CTE
    body \(instrument addPath on its upper rel; PG create\_partial\_grouping\_paths\)\. If it is declined, find why \(the
    HAVING's InitPlan Param is parallel\-safe in PG: is\_parallel\_safe allows a PARAM\_EXEC from an initplan\)\. If it
    is generated, compare its cost with PG's and the serial arm's against PG's serial cost\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match \(Q23\), 82→83 on the shipped corpus\.
  - **Slice 1 LANDED 2026\-10\-19: the aggregate input\-expression cost\.** Design
    `docs/design/0100\-0149/m0146\-0228\-agg\-input\-expression\-cost\.md`; evidence `analysis/m0146/m0146\-0228/`\.
    - The CTE body alone on the exact replica: PG's serial HashAggregate costs 0\.01 per input row over the shared
      Hash Join, goopg's 0\.005\. get\_agg\_clause\_costs adds cost\_qual\_eval over the aggregate arguments, which
      aggClauseCosts had left out\.
    - aggInputEvalOps adds the arguments' and FILTER's qualEvalOps per transition state, never in the combine step\.
    - No verdict moved; 17 TPC\-DS plans per scale changed cost, converging on PG \(Q2, Q43, Q59, Q66, Q99\)\.
    - Pin: TestAggClauseCostsChargesInputExpressions, mutation\-checked\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24/24\), sf025 PASS=99 \(17 plans
      changed\), tpcds\-fireset PASS \(34 fired results byte\-identical\)\.
  - **Slice 2 LANDED 2026\-10\-19: numeric\-family coercion cost\.** Design §4\.
    - qualEvalOps prices an implicit operand coercion \(int vs numeric/float, numeric vs float\) and a numeric\-family
      CastExpr as a function cast \(procost 1\), free when the operand folds to a constant\.
    - The CTE body's serial HashAggregate startup now equals PG's 119518\.79; Q23's CTE plans PG's Finalize
      HashAggregate → Gather → Partial HashAggregate\. No verdict moved; Q23's first divergence moved to depth 5\.
    - Pin: TestQualEvalPricesNumericCoercions, mutation\-checked\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24/24\), sf025 PASS=99 \(6 plans
      changed\), tpcds\-fireset PASS \(12 fired results byte\-identical\)\.
  - **Slice 3 LANDED 2026\-10\-19: a Parallel Hash build over a partial index scan\.** Design §5/§6; evidence
    `analysis/m0146/m0146\-0228/slice3/`, `census3/`, `fireset3/`\.
    - PH2 vetoed PG's arm \(the build had to be a partial seq scan\); partialPathDrivingKind's hash arm carried the
      same rule, so lifting PH2 alone made makeGatherPath refuse the split's partial input\.
    - parallelHashBuildIsClaimable is now the one predicate for all three gates and admits a plain index or
      index\-only build; the executor already claims it through the join's leaf pidx\.
    - Q23's CTE plans PG's Partial HashAggregate 70312\.46\.\.71562\.46 over the Parallel Hash Join with a Parallel
      Index Only Scan build; statement 1 MATCHes PG\. The rest is a separate sort\-strategy record \(statement 2's
      Incremental Sort\) for the next S9 pass, plus a cost\-only ledger row \(subquery boundary cpu\_tuple\_cost\)\.
    - Pins: TestParallelHashIndexOnlyBuildIdentityWithSerial \(executor values\),
      TestSetOpBranchDrivingKindAdmitsParallelHashSeqBuild \(mutation\-checked\)\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24/24\), sf025 PASS=99, tpcds\-fireset
      PASS \(Q23 fired, results byte\-identical\); TPC\-H 22/22 text\-identical; only Q23 changed\.
  - Slice 3 plan \(done — see the slice 3 bullet above\): under the partial aggregate, PG uses a Parallel Hash Join \(Parallel Index Only Scan
    customer\_pkey \+ Parallel Hash, 2563\.84\.\.63437\.77\); goopg a non\-parallel\-aware Hash Join over a private Hash
    \(3876\.33\.\.64750\.26\)\. Trace the partial paths of \(store\_sales\_2, customer\_1\) \(design §5\)\.
- [x] **M0146\-0229 — sort\-strategy: TPC\-DS SF1 Q23 statement 2 — PG Incremental Sorts a Merge Append of the
  UNION ALL's ordered GroupAggregates; goopg fully Sorts a plain Append** \(filed 2026\-10\-19 by the S9 pass at
  4906691d7\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF1 Q23, category sort\-strategy, verdict SHAPE\-DIFF at depth 1 \(under Limit: PG Incremental Sort
    \| goopg Sort\)\. Persists under PG statistics\. Census `analysis/m0146/m0146\-0014/routing\-20261019e/`\.
  - S9 ranking: a three\-way tie at 1, all SF1 \(Q23 sort\-strategy, Q72 qual\-placement, Q77 join\-order\); Q23 has
    the lowest id and the shallowest divergence\.
  - PG: Limit → Incremental Sort \(Sort Key c\_last\_name, c\_first\_name, sum; Presorted Key c\_last\_name,
    c\_first\_name\) → Merge Append \(Sort Key c\_last\_name, c\_first\_name\) → the two GroupAggregates, each over a
    Sort on its group key\. goopg: Limit → Sort → Append over the same two GroupAggregates\.
  - Branch estimates differ too: PG 173 \+ 126 = 299 rows, goopg 150 \+ 76 = 226 under PG statistics\.
  - First step: on an SF1 PG\-statistics clone, check whether goopg's set\-operation rel offers an ordered
    \(Merge Append\) path for a UNION ALL whose branches deliver the query's leading ORDER BY keys
    \(generate\_orderedappend\_paths in allpaths\.c, over the branches' pathkeys\) and whether the upper sort stage
    considers an incremental sort over a partially sorted input \(create\_ordered\_paths in planner\.c\)\. If both
    exist, compare costs with PG's\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match \(Q23\), 82→83 on the shipped corpus and 94→95 under PG
    statistics\.
  - Note 2026\-10\-19: the owner's corpus reload \(95c0d0474\) made the shipped SF1 corpus carry PG statistics; the
    record stands there \(SF1 94/99; Q23 still depth 1 sort\-strategy\)\. Expected instrument now SF1 match 94→95\.
  - **Slice 1 PARKED 2026\-10\-19 — ESCALATED `[!]` on the tpcds\-fireset gate \(S9 \(5\)\(a\)\)\.** Design
    `docs/design/0100\-0149/m0146\-0229\-ordered\-append\-incremental\-sort\.md` §3/§4; patch
    `analysis/m0146/m0146\-0229/slice1\-ordered\-mergeappend\.patch` \(applies clean on a6636f6fc\+95c0d0474\)\.
    - The slice: addOrderedAppendPaths offers generate\_orderedappend\_paths' Merge Append to the ordered stage, as\-is
      or under an Incremental Sort; costSetOp's streaming arm adds cost\_append's per\-row overhead\. Three pins,
      mutation\-checked\.
    - Q23 does not flip yet: PG's arm is generated \(72528\.97\.\.72546\.65\) but the bounded full Sort costs 72545\.45,
      because goopg's branches estimate 226 rows against PG's 299 \(semi join against the best\_ss\_customer CTE\)\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm \(24 MATCH\), sf025 PASS=99 all PASS\.
      tpcds\-fireset FAILs twice on an introduced SF1 Q74 TIMEOUT\. Q74's plan is text\-identical \(costs stripped\)
      and MATCHes PG; all other fired results are byte\-identical; direct timing on fresh clones is 543 s \(baseline\)
      vs 522 s \(candidate\), outputs identical \(`analysis/m0146/m0146\-0229/q74\-timing/`\)\. Q74 straddles the
      600 s FIRESET\_TIMEOUT\.
    - Owner ask: a Q74 timeout pin, a raised FIRESET\_TIMEOUT, or a gate\-exceptions row\. Resume: `git apply` the
      patch, re\-run the gates, commit\.
    - Dropped from the slice and ledgered: pricing set\-op members through their Projects flips the PG\-oracle pin
      TestParameterisedAppendOverUnionAll\.
    - Next slice after the unblock: the branch row estimate \(design §5\)\.
  - **UN\-PARKED 2026\-10\-19 by the owner \(OWNER DECISIONS 2026\-10\-19 \(b\)\): land slice 1\.** It ports PG's
    generate\_orderedappend\_paths / create\_ordered\_paths logic; withholding it would leave goopg reaching PG's
    plans by different logic\. Q74's runtime is not a rejection reason \(S9 \(5\)\(a\)\), and host timings are
    unreliable at present \(owner\-side load outside WSL2\)\. The tpcds\-fireset gate now tolerates an
    introduced SF1 Q74 timeout through the owner allowlist `scripts/tpcds\-fireset\-timeout\-allow\.tsv`
    \(expires 2026\-11\-30\)\. Resume: `git apply analysis/m0146/m0146\-0229/slice1\-ordered\-mergeappend\.patch`,
    re\-run the gates, commit\. Then M0146\-0229a\.
  - **Slice 1 LANDED 2026\-10\-19** \(the parked patch applied unchanged on 80fc64342\)\. Design §3/§4\.1; evidence
    `analysis/m0146/m0146\-0229/fireset3/`, `accept2`\.
    - Gates: units, TestPort\_RegressSuite, tpch\-spotcheck, acceptance arm, sf025 PASS=99, tpcds\-fireset PASS \(Q74
      finished inside the timeout this run, allowlist unused; 19 fired results byte\-identical; parity 97/94 held\)\.
    - Q23 still SHAPE\-DIFF at depth 1: the Incremental Sort arm is generated but loses on goopg's 226\-row branch
      estimate against PG's 299\. Next slice: that estimate \(design §5\)\. Then M0146\-0229a\.
  - **Slice 2 LANDED 2026\-10\-20 \(loop \#31\): the semi join against a grouped CTE is examined as the CTE\.**
    Design §7; evidence `analysis/m0146/m0146\-0229/slice2/`\.
    - Cause: both branches' semi join against `best\_ss\_customer` \(GROUP BY c\_customer\_sk\)\. PG's
      examine\_simple\_variable RTE\_CTE arm marks the lone group key isunique, so nd2 = 33333 and eqjoinsel\_semi
      gives 0\.5732 \(173 of 302\)\. goopg's pulled sublink leaf never got derivedLeafUniqueCols, so nd2 = 200
      \(default\) and the 0\.5 arm gave 150\.
    - Change: tryPGShapedJoinSearch classifies a pulled derived leaf with derivedLeafUniqueCols, as it already
      did for FROM\-clause derived leaves \(M0146\-0009e\)\.
    - Result: SF1 Q23 branch rows 150/76→173/126 = PG; plan **MATCH** \(Incremental Sort over Merge Append\)\.
      Every row estimate equals PG's; the remaining text difference is a Group Key parenthesisation
      \(M0146\-0042 rendering\)\. SF0\.25 Q23 is still MATCH with a changed plan; fired Q23 results are
      byte\-identical at both scales\.
    - Pin: TestSemiJoinAgainstGroupedCTEUsesIsUnique \(600000 vs the default arm's 500000\), mutation\-checked\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm, TPC\-H
      parity 22/22, sf025 PASS=99 \(changed=1, Q23\), tpcds\-fireset PASS \(SF0\.25 98/99, SF1 95→96\)\.
    - Residual \(ledger\): the member projection charge \(design §5\)\.
  Movement: yes — TPC\-DS SF1 match 95→96 \(Q23\)
- [x] **M0146\-0229a — recon: why SF1 Q74 runs ~520\-560 s on goopg — is there a PG 18\.3 executor\-layer
  optimisation for its shape, or a goopg executor slowdown?** \(filed 2026\-10\-19 by the owner; placed in banner
  item 3 directly after M0146\-0229 slice 1\)\.
  Kind: recon
  Movement: none — recon \(PG 18\.3 runs Q74 in 896 s on the identical plan; goopg 452 s; no mechanism to port\)
  Parent: M0146\-0229
  - Why: Q74's plan MATCHes PG \(text\-identical, costs stripped\), so the runtime gap is executor\-side\. It sits
    on the 600 s FIRESET\_TIMEOUT and is provisionally allowlisted there; the allowlist row expires 2026\-11\-30\.
  - Shape: the `year\_total` CTE \(UNION ALL of store\_sales and web\_sales grouped by customer and year\), scanned
    four times in a self\-join, then ORDER BY \+ LIMIT 100\.
  - Owner evidence 2026\-10\-19: **PG 18\.3 itself runs SF1 Q74 in 897 s** under EXPLAIN \(ANALYZE, BUFFERS\) on :65438
    \(`analysis/m0146/m0146\-0229/q74\-timing/pg\-sf1\-q74\-explain\-analyze\.txt`; ANALYZE adds per\-node timing
    overhead\)\. The time is in PG's own plan: the CTE Scans are estimated at rows=1 \(actual 37708 / 38252 / 11256 /
    11153\), so three nested loops rescan the CTE tuplestore 37708 × 38252 times; the CTE build itself is ~3\.4 s\.
    Plain execution \(no EXPLAIN\) on PG: **896 s**, 100 rows \(owner, 2026\-10\-19\)\.
    goopg's 520\-560 s for the same plan is not slower than PG, so check first whether the recon reduces to "PG is
    equally slow" \(compare plain\-execution runtimes on an idle host, and node by node\) before looking for a
    mechanism\. PG's CTE Scan rescan reads its tuplestore in memory per loop \(nodeCtescan\.c CteScanRecheck /
    tuplestore\_rescan\); compare goopg's per\-rescan cost there\.
  - Method: EXPLAIN \(ANALYZE, BUFFERS\) on both engines on the reloaded SF1 corpus \(goopg on a private clone under
    the cgroup cap, PG read\-only on :65438\); per\-node actual time and rows side by side\. Find the dominant
    nodes, then attribute each gap: \(a\) a PG executor mechanism goopg lacks for this shape \(e\.g\. CTE Scan
    tuplestore reading, HashAggregate / hash join batching and spilling, JIT is off on both, parallel workers in
    the CTE, Memoize, tuple deform\); or \(b\) a goopg\-side cost \(per\-row allocation, datum copying,
    numeric arithmetic, CTE re\-materialisation, GC under GOMEMLIMIT\)\. Profile goopg with pprof / perf on the clone\.
    Host timings are unreliable at present: compare node ratios and CPU profiles, not wall clock alone\.
  - Deliverable: findings in the design doc\. For EACH mechanism found, file an impl sub\-task
    \(M0146\-0229b, \-0229c, …; `Parent: M0146\-0229`\) citing the PG executor code it ports or the goopg hot path it
    removes, with expected instrument = SF1 Q74 runtime \(and the fire\-set allowlist row retired when Q74 clears
    600 s with margin\)\. If nothing is found, close with `Movement: none — recon` and the evidence\.
  - **CLOSED 2026\-10\-19: nothing found — PG is slower on the same plan\.** Design
    `docs/design/0100\-0149/m0146\-0229\-ordered\-append\-incremental\-sort\.md` §6; evidence
    `analysis/m0146/m0146\-0229/q74\-recon/`\.
    - goopg EXPLAIN \(ANALYZE\) on a fresh SF1 clone: 452 s, PG's plan node for node; PG 896 s plain / 897 s under
      ANALYZE\. Both estimate the four filtered CTE Scans at rows=1 \(actual 37708/38252/11256/11153\), so both nest
      ≈1\.44×10⁹ bpchar comparisons\.
    - CPU profile: 98% under the nested loop's join predicate; the 37 s on `textShapeAmbiguous` is the first touch
      \(cache miss\) of each compared string, which the comparison itself would take — not a removable cost\.
    - No impl sub\-task filed\. The SF1 Q74 fire\-set timeout is structural \(inherent to the PG\-identical plan\);
      owner's call: keep the allowlist row past 2026\-11\-30 or raise FIRESET\_TIMEOUT for SF1\.
- [x] **M0146\-0230 — join estimates: port eqjoinsel\_inner's MCV arm \(and neqjoinsel over it\); goopg estimates every
  equality / `<>` join from ndistinct alone** \(filed 2026\-10\-19 by M0146\-0214 slice 3\)\.
  Kind: impl
  Movement: none — parity counts held \(SF0\.25 97/99, SF1 94/99\); fired\-query row estimates identical to PG 220→247/250 \(SF0\.25\), 170→195/203 \(SF1\)
  Parent: M0146-0214
  - Record: TPC\-DS SF0\.25 Q64, join\-order, SHAPE\-DIFF at depth 5\. Tour\-by\-tour against PG, the first large worth
    gap is the cd2 join under `cd1.cd_marital_status <> cd2.cd_marital_status`: goopg 1536640 rows \(1 − 1/5\), PG
    1536621 \(1 − eqjoinsel over the MCV lists\)\. Evidence `analysis/m0146/m0146\-0214/slice3/trace/`\.
  - PG: `eqjoinsel_inner` \(selfuncs\.c\) with both MCV lists: matchprodfreq over matched pairs; otherfreq and
    unmatchfreq spread over the ndistinct beyond each list; selectivity = min\(totalsel1, totalsel2\)\. goopg's
    `eqJoinSelectivityExt` \(joinselectivity\.go\) is the no\-MCV branch only \(ledger 2026\-08\-04 M0127\-P5\.6\-a\)\.
    `catalog.ColumnStats.MCV` carries the imported lists\.
  - First step: port the MCV branch \(value equality by column type\) behind both\-sides\-have\-MCV, keep the no\-MCV
    branch otherwise; pin with PG's numbers for a hand\-built pair of MCV lists; then census both corpora\.
  - Expected instrument: TPC\-DS PLAN\-PARITY match counts at both scales \(and the Q64 tour worths\)\.
  - **DONE 2026\-10\-19\.** Design `docs/design/0100\-0149/m0146\-0230\-search\-eqjoinsel\-mcv\.md`; evidence
    `analysis/m0146/m0146\-0230/`\.
    - The plan\-node estimator already had the MCV branch \(eqjoinselInnerMCV, take2 P1\-15\); its arithmetic is now
      `eqjoinselInnerMCVCore`, shared by the search's `eqJoinSelectivityExt` \(taken first when both sides carry MCV
      lists\)\. `<>` and the SEMI/ANTI clamp read it, as in PG\.
    - Q64 tour 2: the cd2 join is now PG's 1536621 rows\. The next gap in that tour is merge\-join COST at equal rows
      \(join with item: goopg startup 20675\.72 vs 18896\.82, total 106647\.34 vs 106969\.50\) → M0146\-0214\.
    - Pin: TestSearchEqJoinUsesBothSidesMCVLists \(mutation\-checked\)\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm \(values identical\),
      sf025 PASS=99 \(8 plans changed text\), tpcds\-fireset PASS \(14 fired results byte\-identical\)\.
- [x] **M0146\-0231 — subquery / inlined\-CTE leaves offer the join search one prebuilt path; PG's
  set\_subquery\_pathlist offers every surviving path of the subquery's final rel** \(filed 2026\-10\-19 by M0146\-0214
  slice 5\)\.
  Kind: impl
  Parent: M0146-0214
  - Record: TPC\-DS SF0\.25 Q64, join\-order, SHAPE\-DIFF at depth 5\. GEQO tour 14: PG files a partial Merge Join over
    \(b 1 7 9 10 11 12 16\) ⋈ cs\_ui whose inner is cs\_ui's parallel\-safe, cs\_item\_sk\-ordered SubqueryScan path
    \(0\.7225\.\.42006\.29\); its Gather Merge \(1003\.38\.\.6\.42e9\) is the joinrel's cheapest\. goopg's cs\_ui leaf has
    only its cheapest plan \(13811\.71\.\.13815\.10, a Gather Merge inside, not parallel\-safe\) → serial 1\.98e10\.
    Evidence `analysis/m0146/m0146\-0214/slice5/`\.
  - PG: set\_subquery\_pathlist \(allpaths\.c\) builds a SubqueryScan path for each path of the subquery's
    final\_rel \(pathkeys converted by convert\_subquery\_pathkeys; parallel\_safe carried\)\. goopg: the derived leaf
    is one PathPrebuilt \(ctescanpathkeys\.go converts its pathkeys only\)\.
  - First step: find where a derived / inlined\-CTE leaf's PathPrebuilt is built \(joinsearch\.go costSubplanLeaf
    callers\) and whether the subquery's planning keeps its final rel's other paths \(cheapest parallel\-safe; cheapest
    per pathkeys\); offer them as additional leaf paths, each planned to a Node only when chosen\.
  - Expected instrument: TPC\-DS SF0\.25 PLAN\-PARITY match \(Q64\) once the GEQO pool order converges; tour 14's join
    6 cheapest = PG's Gather Merge\.
  - **Done 2026\-10\-19 \(slice 1\)\.** Design `docs/design/0100\-0149/m0146\-0231\-subquery\-leaf\-alt\-paths\.md`;
    evidence `analysis/m0146/m0146\-0231/`\.
    - Grouping paths carry create\_agg\_path's parallel\_safe \(make\_grouping\_rel's consider\_parallel\); the GROUP\_AGG
      rel is recorded on planState; derived and inlined\-CTE leaves offer its surviving paths \(set\_subquery\_pathlist\);
      a Gather\-holding leaf path is parallel\-unsafe and an inlined CTE takes the RTE\_SUBQUERY consider\_parallel arm\.
    - add\_path\_precheck / add\_partial\_path\_precheck ported \(path\.go\) into all five serial and four partial join
      producers, on initial costs \(hashJoinInitialCost, mergeJoinInitialCost, nliNestLoopInitialCost\)\. Without them
      the new parallel\-safe leaf path let several tours go 68% cheaper than PG\.
    - Instrument: SF0\.25 Q64 MATCH \(fire\-set SF0\.25 97→98\); Q64 GEQO tours equal to PG's 252→326 \(SF0\.25\),
      258→262 \(SF1\)\. SF1 Q64 now diverges \(fire\-set SF1 95→94\): both builds reach PG's best tour \(eval 114\), but
      after tour 262 goopg evaluates a second tour of equal worth \(eval 363\) PG never reaches → M0146\-0214\.
    - Pins: m0146\_0231\_test\.go \(precheck and agg\-safety pins mutation\-checked\)\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm \(values identical\),
      sf025 PASS=99 \(only Q64's plan changed; 21 s→32 s on PG's plan, same ck\), tpcds\-fireset PASS \(fired Q64/Q77
      byte\-identical\)\.
    - Residual → M0146\-0232\.
- [x] **M0146\-0233 — ineq\_histogram\_selectivity's cutoff clamp and get\_actual\_variable\_range** \(filed 2026\-10\-20 by
  M0146\-0214 slice 6\)\.
  Kind: impl
  Parent: M0146-0214
  - Record: TPC\-DS SF1 Q64 GEQO eval 41, joinrel \(b 1 9 10\): store\_sales' merge start fraction is PG 9\.545e\-5 \(275
    rows skipped\), goopg 7\.2e\-5 \(207\); startup 1446072\.92 vs 1184789\.15 with the PG sort currency\.
  - PG \(selfuncs\.c ineq\_histogram\_selectivity\): unless the binary search touched an endpoint AND
    get\_actual\_variable\_range found a usable btree index leading on the column \(have\_end\), the histogram
    selectivity is clamped to \[0\.01/\(nvalues\-1\), 1 − that\]; with have\_end the endpoint is replaced by the index's
    actual min/max\. goopg's histogramOpSelectivity applies neither \("Not ported" in its doc comment\)\.
  - Scope: the clamp needs the column's index facts on every caller of histogramOpSelectivity \(restriction path
    via resolveBaseColumn, OR arms, mergejoinscansel\) — sibling paths change together; the actual\-endpoint probe needs a
    planner\-time btree first/last\-visible\-entry read\.
  - Expected instrument: SF1 Q64 eval\-41 joinrel \(b 1 9 10\) startup = PG's; GEQO tours equal to PG's beyond 262\.
  - **LANDED 2026\-10\-20 \(loop \#32\)\.** Design `docs/design/0100-0149/m0146-0233-actual-variable-range.md`; evidence
    `analysis/m0146/m0146\-0233/`\.
    - nbtree ScanFromEnd \(\_bt\_endpoint backward\); executor ActualVariableEndpointProbe / ActualVariableRange
      \(non\-null leading key, all\-visible or not dead\-to\-all heap tuple, VISITED\_PAGES\_LIMIT; btree, non\-partial,
      plain leading column, own collation, default opclass, DESC answers from the other end\); catalog
      ActualVariableRangeFunc wired by initdb\.
    - histogramOpSelectivity is PG's binary search with endpoint refresh, have\_end and the 0\.01/\(n−1\) cutoff;
      no histogram gives scalarineqsel's 0\.5\. Probes reach the restriction path \(own base table only\), the OR and
      range arms, and mergejoinscansel \(joinVarStats\.probe\)\.
    - End to end vs PG on a stale histogram: id \> 15000 selectivity 0\.005 = PG's 0\.00495 \(was clamped to 1
      row\)\. Row counts still differ by estimate\_rel\_size's page scaling → M0146\-0237\.
    - Fire set: parity 98/99 and 96/99 held; Q13 and Q48 changed plans with results identical\. Row\-estimate census:
      queries with every estimate equal to PG's SF0\.25 86→87, SF1 85→86 \(Q48\); nodes 2536→2539 / 2603→2606\.
    - Pins: TestInequalityEstimateReadsActualIndexEndpoints \(postmaster\), TestHistogramOpSelectivityActualEndpoints,
      TestScanFromEndVisitsEntriesInReverseKeyOrder, and the updated TestHistogramOpSelectivityEqSel\.
      Mutation\-checked \(hook unwired, have\_end ignored\)\.
    - Gates: units, internal/postmaster, nbtree \-race, TestPort\_RegressSuite, TestPort\_Isolation\*,
      tpch\-spotcheck, acceptance arm, TPC\-H parity 22/22, sf025 PASS=99 \(changed Q13 Q48\), tpcds\-fireset PASS\.
    - Residuals \(ledger\): aborted\-inserter visibility in the probe, a per\-call btree open, no indisvalid check\.
  Movement: none — parity held; estimate\-identical queries SF0\.25 86→87, SF1 85→86 \(Q48\)
- [x] **M0146\-0237 — estimate\_rel\_size: scale a stale reltuples by the relation's current page count** \(filed
  2026\-10\-20 by M0146\-0233\)\.
  Kind: impl
  Parent: M0146-0233
  - Record: after ANALYZE of 10000 rows and 10000 more inserted \(autovacuum off\), PG estimates a scan of the
    table at 19778 rows; goopg keeps reltuples, 10000\. Every estimate on the table is halved
    \(`analysis/m0146/m0146\-0233/endpoint\-probe\-pg\-vs\-goopg\.txt`\)\.
  - PG \(plancat\.c estimate\_rel\_size → table\_block\_relation\_estimate\_size\): curpages =
    RelationGetNumberOfBlocks; with relpages \> 0 the density is reltuples / relpages, and tuples = rint\(density ×
    curpages\)\. goopg applies its page\-based fallback only to never\-analysed relations \(relsize\.go
    applyRelSizeFallback\)\.
  - Scope: every reader of a base rel's tuples \(baseRelInfo\.baseRows, EstimateRows of a scan, the cost
    functions\); sibling paths change together\.
  - Expected instrument: shipped corpora are unaffected \(relpages = actual pages\); the plan instruments move only
    on tables changed since ANALYZE\. Pin against PG on the same stale\-statistics workload\.
  - **LANDED 2026\-10\-20 \(loop \#33\)\.** Design `docs/design/0100-0149/m0146-0237-estimate-rel-size-scaling.md`;
    evidence `analysis/m0146/m0146\-0237/`\.
    - liveRelSize \(relsize\.go\) is estimate\_rel\_size for an analysed table with a stored page count: pages =
      curpages, tuples = rint\(reltuples / relpages × curpages\)\. It is fed by catalog\.RelationBlocksFunc \(initdb;
      storage CachedNBlocks, no syscall for an open fork\)\. tableRows, baseRelPages and the direct RowCount readers go
      through it\.
    - Sibling fix: ResolvedNDistinct resolves the form StaDistinct reports \(relative once the fraction exceeds
      0\.1\), so a key column's distinct count scales with rel\-\>tuples as PG's does\.
    - Result: the stale\-statistics workload matches PG's EXPLAIN line for line, costs included \(19778 rows /
      286\.78; id \< 9500 18788\)\. Shipped corpora unchanged \(sf025 same=99, SF1 fired none, TPC\-H 22/22, acceptance
      identical\)\.
    - Pin: TestInequalityEstimateReadsActualIndexEndpoints now asserts PG's exact lines; mutation\-checked
      \(liveRelSize off → 50 rows; old ResolvedNDistinct order → 18787\)\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm, TPC\-H parity 22/22 \(audit arm moved to 5586: the owner's SF1 corpus rebuild held 5582\)\. On the corpora
      the owner rebuilt and swapped in at 11:44/11:49: sf025 PASS=99, 61 ck\-verified \(15 shapes changed against
      the pre\-rebuild sweep, the corpus's doing\); tpcds\-fireset A/B PASS, no query changed, 98/99 and 96/99\.
    - Re the owner's note on bb7b36ceb \(a dirty\-tree sweep at 11:54 ERRORed Q34/Q64/Q75 after 102/209/172 s\): the same
      staged code swept the rebuilt corpus at 12:12 with Q34 PASS 0\.23 s, Q64 PASS 37 s and Q75 PASS 0\.9 s, all
      ck\-matched \(`bench/tpcds/runtime_goopg/tpcds\-results\-sf025/sweep\-20261020\-121227\.txt`\)\. At 11:54 this
      loop's three Q64 EXPLAIN timing servers were running in the same goopg\-workloads memory slice, so the
      errors are attributed to that contention, not the code\.
    - Note: the first fire\-set baseline failed SF0\.25 Q64's EXPLAIN on the capture's 120 s timeout\. Q64 plans in
      40\-235 s on every binary, pre\-0233 included \(ledger\)\.
  Movement: none — instrument artefact: the shipped corpora have relpages = actual pages, so no plan instrument can see the scaling; a stale\-statistics workload now matches PG line for line
- [x] **M0146\-0234 — sort cost byte currency: PG relation\_byte\_size and the joinrel's reltarget width** \(filed
  2026\-10\-20 by M0146\-0214 slice 6\)\.
  Kind: impl
  Parent: M0146-0214
  - Record: TPC\-DS SF1 Q64 GEQO eval 41\.
    - Join 2 \(b 1 9 10\): goopg sorts store\_sales as a disk sort \(1190592\) where PG's is in memory \(389568\)\.
    - Join 9: goopg's partial merge costs 6\.32e10 \(PG 6\.20e10\); the outer Sort's backed\-out width is about 664 \(PG
      about 96\), so goopg's partial hash survives add\_partial\_path where PG's fuzz\-equal merge with better pathkeys
      drops it\.
  - PG \(costsize\.c cost\_tuplesort\): input\_bytes = relation\_byte\_size\(tuples, pathtarget\->width\)\. goopg prices with
    hashsize\.EntryBytes unless GOOPG\_PG\_SORT\_RELATION\_BYTES\_COST=1 \(R113, held default\-off by M0139\-0007a on
    2026\-09\-15 for zero measured movement\)\. With the flag, the store\_sales Sort is PG's exactly \(389568\.13\.\.396768\.01\),
    but a joinrel's width is still unnarrowed\.
  - First step: re\-measure R113 default\-on on the fire\-set and the SF1 Q64 eval list; then the joinrel reltarget width
    feeding pathWidth for sort inputs\.
  - Expected instrument: fire\-set match counts; SF1 Q64 GEQO tours equal to PG's beyond 262\.
  - **Slice 1 2026\-10\-20\.** Design `docs/design/0100\-0149/m0146\-0234\-sort\-byte\-currency\-and\-stats\-widths\.md`;
    evidence `analysis/m0146/m0146\-0234/`\.
    - R113 default\-on \(cost\_tuplesort's relation\_byte\_size; "0" opts out\); the grouping sort seed carries the
      narrowed PG width \(aggInputPGWidth\); relNarrowedWidths sums each kept column's ANALYZE AvgWidth
      \(set\_rel\_width's get\_attavgwidth\) instead of the type width\. Join 9's Sort width 664→155 \(PG ≈96\)\.
    - SF1 Q64 GEQO evals before the first differing tour with worth off >1e\-3: 14→6 \(>1e\-4: 33→29; eval 41
      0\.0075→0\.00098, eval 90 0\.0061→6\.6e\-7\)\. Fire\-set 98/94 and TPC\-H parity 22/22 unchanged; only SF1 Q64's plan
      changed \(still divergent, depth 4→5; fired result byte\-identical\)\.
    - Pins: TestRelNarrowedWidthsNarrowsToNeededColumns asserts the stawidth sum \(mutation\-checked\);
      TestSortSeedNarrowsWithTheAggregate / TestCostWindowPresortedCreditIsInertAtZero updated for the PG currency\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm \(values identical\),
      TPC\-H parity capture 22/22, sf025 PASS=99, tpcds\-fireset PASS\.
    - Movement: none — parity held; SF1 Q64 eval worths converge on PG \(off >1e\-3: 14→6\), no match count moved\.
    - Next: build\_joinrel\_tlist's attr\_needed narrowing — a joinrel keeps an attribute only while a relation outside
      it needs it; goopg's NeededCols is one statement\-wide name set \(join 9: 155 vs PG 96\)\. Then non\-table leaves'
      widths from the subquery target list \(set\_subquery\_size\_estimates\)\.
  - **Slice 2 2026\-10\-20: joinrel target after build\_joinrel\_tlist\.** Design §8; evidence
    `analysis/m0146/m0146\-0234/slice2/`\.
    - joinrelNarrowedWidths \(computed at joinrel creation\): keep = outputCols ∪ names read by clauses reaching past the
      joinrel; the triple sums the published base members' kept columns \(SEMI/ANTI RHS excluded\);
      narrowJoinWidths stamps it on every path of the rel\. Declines to the children's sum when ineligible\.
    - SF1 Q64: join 9's outer Sort width 155→99–103 \(PG 96\); evals before the first differing tour off >1e\-3:
      1→0, max error 0\.0046→0\.00067 \(eval 173 0\.0046→0\.00022, eval 41 0\.00098→0\.00012\)\. Two worth\-order
      inversions remain \(evals 96/138, 159/248, ~1e\-5 errors\)\. Fire\-set 98/94 and TPC\-H 22/22 unchanged; no fired
      plan change\.
    - Pin: TestJoinrelNarrowedWidthsKeepsOnlyAttrNeededAbove \(mutation\-checked\)\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm \(values identical\),
      TPC\-H parity 22/22, sf025 PASS=99, tpcds\-fireset PASS\.
    - Movement: none — parity held; SF1 Q64 eval worths converge on PG \(max error 0\.0046→0\.00067\)\.
    - Remaining \(task stays open\): base rels narrow to the statement\-wide set \(PG's base reltarget drops columns read
      only by the rel's own restriction quals\); non\-table leaves use type widths\.
  - **Slice 3 2026\-10\-20 \(loop \#36\): the base rel target, after build\_base\_rel\_tlists\.** Design §9; evidence
    `analysis/m0146/m0146\-0234/slice3/`\.
    - attrNeededAbove \(factored from joinrelNarrowedWidths\) gives a base rel's keep set: above\-tree names plus columns
      of clauses reaching another rel; baseRelNarrowedWidths stamps it \(an empty reltarget keeps the statement\-wide
      set\)\. Restriction\-only columns no longer count in a base rel's cost width\.
    - Measured inert: fire\-set SF0\.25/SF1 plans text\-identical with costs; TPC\-H 22/22; sf025 same=99\. The joins above
      already price with the slice\-2 target, small hash builds are one batch, EXPLAIN widths are unnarrowed\.
    - Pin: TestBaseRelNarrowedWidthsDropsRestrictionOnlyColumns\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm, TPC\-H parity 22/22,
      sf025 PASS=99 same=99, tpcds\-fireset PASS \(no change; 98/99, 96/99\)\.
    - Movement: none — instrument artefact: no captured plan or cost reads a base rel's width on these corpora\.
    - Remaining \(task stays open\): non\-table leaves' widths from the subquery target list
      \(set\_subquery\_size\_estimates / set\_cte\_size\_estimates\)\.
  - **Slice 4 2026\-10\-20 \(loop \#37\): derived leaves' widths, and closure\.** Design §10; evidence
    `analysis/m0146/m0146\-0234/slice4/`\.
    - derivedLeafColumnWidth / subqueryVarWidth: a subquery output that is a plain Var takes its base attribute's
      stawidth \(set\_subquery\_size\_estimates\); expressions, set operations, materialized CTEs and stat\-less columns
      keep the type default; an inlined CTE answers as a subquery\.
    - Measured inert: fire\-set plans text\-identical with costs \(SF0\.25 1882/2572, SF1 1951/2611 cost\-equal nodes,
      unchanged\); TPC\-H 22/22; sf025 same=99\.
    - Pin: TestDerivedLeafColumnWidthFollowsSubqueryVars\.
    - Gates: units, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance arm, TPC\-H parity 22/22,
      sf025 PASS=99 same=99, tpcds\-fireset PASS \(no change; 98/99, 96/99\)\.
    - Closure: the record \(SF1 Q64 GEQO eval 41\) is stale, since SF1 Q64 has matched PG since M0146\-0214 closed
      \(d1446d972\); every item named here has landed\.
  Movement: none — record stale: SF1 Q64 has matched since M0146\-0214; slices 3–4 are fidelity ports no capture reads
- [x] **M0146\-0232 — set\_subquery\_pathlist coverage: subquery partial paths and ungrouped subqueries** \(filed
  2026\-10\-19 by M0146\-0231\)\.
  Kind: impl
  Parent: M0146-0214
  - PG: set\_subquery\_pathlist also files the final rel's partial paths \(when the rel considers parallel\), and offers
    the final rel's paths for EVERY subquery — join\-only bodies and bodies with ORDER BY / DISTINCT / window above
    the grouping included\. goopg offers alternatives only for a grouped body reached through
    Filter/Project/SubqueryScan/inlined CTEScan wrappers \(subqueryLeafAggregate\)\.
  - Also: the precheck reads the pathlist in goopg's producer order; PG's add\_paths\_to\_joinrel order is
    sort\_inner\_and\_outer, match\_unsorted\_outer, hash\_inner\_and\_outer — audit where goopg's order differs\.
  - First step: a census of fired TPC\-DS queries whose divergence sits under a SubqueryScan / inlined CTE leaf
    with no grouped body \(first\-divergence parent chain\)\.
  - Expected instrument: TPC\-DS PLAN\-PARITY match count on the queries that census names\.
  - **Done 2026\-10\-20 \(slice 1\) — CLOSED\.** Design
    `docs/design/0100\-0149/m0146\-0232\-inlined\-cte\-rescan\-and\-material\.md`; evidence `analysis/m0146/m0146\-0232/`\.
    - Census: SF1 Q13 \(join order\), SF1 Q72 \(qual placement\) and SF0\.25 Q77 c0 diverge outside any subquery leaf\.
      The only divergence under one is Q77's c1 branch \(grouped inlined CTE `cr`\)\. No record needs a subquery
      partial path or an ungrouped\-subquery alternative\.
    - Q77 c1: an inlined CTE reference is PG's T\_SubqueryScan \(inline\_cte\)\. isTuplestoreScanNode and
      nodeMaterializesOutput now answer `!Inlined\(\)` for a CTEScan, so rescans re\-execute the body and
      match\_unsorted\_outer's Material wins, as in PG\.
    - A MaterialPath's child keeps the CP\_SMALL\_TLIST regime \(subqueryStripTlistReset gains a Materialize arm\), so
      `Subquery Scan on cr` renders\. A merge join's make\_material \(Path\.mergeJoinMaterial →
      Materialize\.mergeJoinInner\) does not reset it: Q58's ws\_items and Q78's cs still strip\.
    - Instrument: SF0\.25 Q77 CATEGORIES\-EXCL\-MATCH scan\-type 1 → 0 \(within ±3\); match 98 → 98\. SF1 Q77's c1 now
      matches, and its first divergence moves to the pre\-existing c2 web branch \(Merge vs Hash Left Join\); SF1
      96 → 96\. TPC\-H 22/22\.
    - Pin: TestInlinedCTEInnerIsMaterialized \(both halves mutation\-checked\)\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm \(values identical\), TPC\-H parity 22/22, sf025 PASS=99 same=98 \(Q77 changed\), tpcds\-fireset PASS\.
    - Not done \(ledgered\): subquery partial paths, ungrouped\-subquery alternatives and the producer\-order audit, none
      of which a fired record needs\.
  Movement: none — SF0\.25 Q77 scan\-type 1 → 0 is inside ±3; match counts 98/99 and 96/99 unchanged
- [x] **M0146\-0240 — join\-method: TPC\-DS SF1 Q77 web branch — PG Merge Left Joins the ws and wr CTEs; goopg Hash
  Left Joins them, and sorts a merge join's output that already carries the group key's order** \(filed 2026\-10\-20
  by the S9 pass at cdb6a4513\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF1 Q77, category join\-method, verdict SHAPE\-DIFF at depth 5 \(Append c2: PG Merge Left Join \|
    goopg Hash Left Join\)\. Census: the M0146\-0232 fire\-set captures on HEAD's code tree,
    `analysis/m0146/m0146\-0232/fireset/`\.
  - S9 ranking \(SF0\.25 98/99, SF1 96/99, TPC\-H 22/22\): join\-method 2 \(SF1 Q13, SF1 Q77\), sort\-strategy 1
    \(SF0\.25 Q77\), qual\-placement 1 \(SF1 Q72\)\. Q77 is chosen over Q13 because it has the shallower divergence and
    shares its probable cause with SF0\.25 Q77; Q13's ca/store order follows row\-estimate differences under the
    customer\_address filter\.
  - Inside ws, goopg plans Partial GroupAggregate → Sort \(wp\_web\_page\_sk\) → Merge Join \(ws\_web\_page\_sk =
    wp\_web\_page\_sk\); PG feeds the Partial GroupAggregate straight from the Merge Join, whose output order already
    satisfies the group key through the equivalence class\. The extra Sort makes ws 24863\.27 against PG's 24859\.35\.
  - SF0\.25 Q77 c0 \(sort\-strategy\) has the same shape: Sort \(store\_1\.s\_store\_sk\) above Merge Join
    \(sr\_store\_sk = store\_1\.s\_store\_sk\)\. goopg's inner Sort also reports 31 rows over an 18\-row partial input
    \(PG 18\)\.
  - First step: on a private SF0\.25 clone, EXPLAIN the sr CTE alone and check the partial merge join's pathkeys as
    the partial grouping stage sees them \(create\_partial\_grouping\_paths: pathkeys\_contained\_in\(group\_pathkeys,
    path\->pathkeys\) over the canonical EC pathkey\)\. Then check the inner Sort's row count\.
  - Expected instrument: TPC\-DS PLAN\-PARITY match, SF0\.25 98→99 and SF1 96→97, if the web branch then elects
    PG's Merge Left Join\.
  - **Slice 1 LANDED 2026\-10\-20: the partial aggregate reads a presorted partial path\.** Design
    `docs/design/0100\-0149/m0146\-0240\-partial\-agg\-presorted\-input\.md`; evidence `analysis/m0146/m0146\-0240/`\.
    - make\_ordered\_path's first test for the cheapest partial path: pseed keeps the partial path's validated pathkeys,
      renamed through the searched rel's equivalence classes to the group keys \(canonicalPartialInputKeys\)\.
      orderedPartialInput skips the worker Sort in the sorted\-input split, the partial Group arm and the R56 Gather
      Merge arm\. createFinalizeAggSortedPlan and splitAggregateTransportSortedInput accept the Sort\-less input\.
    - Instrument: TPC\-DS SF0\.25 match 98 → 99/99 \(Q77; all categories 0\)\. SF1 96 → 96: ws and wr now cost exactly
      PG's, but the web branch still elects Hash Left Join 29549\.08 over PG's Merge Left Join 29548\.61\. TPC\-H 22/22,
      plans unchanged\.
    - Pin: TestPartialAggOverPresortedPartialJoin \(PG\-anchored shape plus values; two mutations fail it\)\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm \(values identical\), TPC\-H parity 22/22, sf025 PASS=99 same=98 \(Q77\), tpcds\-fireset PASS\.
    - Next \(slice 2\): does goopg file the Merge Left Join of the ws and wr leaves at all? Instrument addPath on that
      joinrel on an SF1 clone, then compare its cost with PG's 29514\.15\.\.29548\.61\.
  - **Slice 2 LANDED 2026\-10\-20 — CLOSED: a sorted aggregate over an EC\-ordered input keeps its order\.** Design §5;
    evidence `analysis/m0146/m0146\-0240/slice2/`\.
    - The Merge Left Join was filed but sorted both inputs: wr's GroupAggregate \(group key wp\_web\_page\_sk\) reads a
      Gather Merge ordered by wr\_web\_page\_sk, the same EC, and the emission twins compared the keys syntactically\.
    - Aggregate\.GroupInputKeys records the EC member the presorted\-seed and searched\-candidate arms proved;
      groupKeyOrderedBy is the shared test of aggregateEmissionPathkeys and groupingEmissionPathkeys\. The upper
      narrowing pass remaps the field \(or drops it\), and the field\-inventory pin lists it\.
    - Repro: Merge Left Join 29514\.15\.\.29549\.06 \(PG \.\.29548\.61\)\. The 0\.45 left is cost\_subqueryscan's
      trivial\_pathtarget exemption, measured on :65438 by reordering the select list \(ledgered\)\.
    - Pin: TestGroupAggOverECOrderedInputKeepsItsOrder \(mutation\-checked\)\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm \(values identical\), TPC\-H parity 22/22 \(plans unchanged\), sf025 PASS=99 same=99, tpcds\-fireset PASS\.
  Movement: yes — TPC\-DS SF1 match 96→97 and SF0\.25 match 98→99 \(Q77\)
- [x] **M0146\-0241 — join\-method: TPC\-DS SF1 Q13 — a range pair inside an OR join clause's arm is multiplied, not paired**
  \(filed 2026\-10\-20 by the S9 pass at 4e0c4e8c1\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF1 Q13, category join\-method, verdict SHAPE\-DIFF at depth 6 \(PG Nested Loop over the
    customer\_address probe \| goopg Hash Join with store first\)\. Census: the M0146\-0240 slice\-2 fire\-set captures on
    HEAD's code tree, `analysis/m0146/m0146\-0240/slice2/fireset/`\.
  - S9 ranking \(SF0\.25 99/99, SF1 97/99, TPC\-H 22/22\): join\-method 1 \(SF1 Q13\), qual\-placement 1 \(SF1 Q72\); Q13
    has the lower id and the shallower divergence\.
  - Diagnosis \(SF1 clone vs :65438\): restriction estimates on customer\_address are identical, and so is each single
    `ca\_state IN … AND ss\_net\_profit BETWEEN …` arm \(11520 rows over store\_sales ⋈ customer\_address\)\. The OR of two
    such arms, a JOIN clause, estimates 31665 against PG's 16054, and the three\-arm OR 80704 against 28320\.
  - PG: clause\_selectivity\_ext prices an OR arm \(an AND\) through clauselist\_selectivity\_ext, which pairs the
    same\-variable bounds of `ss\_net\_profit >= a AND ss\_net\_profit <= b` into one range \(addRangeClause:
    hibound \+ lobound − 1 \+ null fraction\)\. goopg's orArmSelectivity multiplies the two bounds independently\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match \(Q13\), 97 → 98\.
  - **Done 2026\-10\-20 — CLOSED\.** Design `docs/design/0100\-0149/m0146\-0241\-or\-arm\-range\-pairing\.md`; evidence
    `analysis/m0146/m0146\-0241/`\.
    - orArmSelectivity groups single\-side inequality bounds by variable \(orArmRangeBound\) and completes each as
      clausesel\.c does \(orArmRangeSelectivity: hi \+ lo − 1 \+ null fraction, DEFAULT\_RANGE\_INEQ\_SEL for a guessed
      bound, the roundoff clamps\)\.
    - The two\- and three\-arm ORs now estimate PG's 16054 and 28320; SF1 Q13 MATCHes \(82716\.26 both\)\. TPC\-H Q19's OR
      join estimates PG's 47 rows \(was 71\), plan still MATCH\.
    - Pin: TestORJoinClauseArmPairsRangeBounds \(PG row estimates 743 and 1940; mutation\-checked\)\. The file avoids an
      `\_arm\_test\.go` name, which Go reads as a GOARCH constraint\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm \(values identical\), TPC\-H parity 22/22, sf025 PASS=99 \(Q13 changed, still MATCH\), tpcds\-fireset PASS\.
  Movement: yes — TPC\-DS SF1 match 97→98 \(Q13\)
- [x] **M0146\-0242 — qual\-placement: TPC\-DS SF1 Q72 — a nested loop over a parameterised inner keeps an equivalence
  clause the inner already enforces** \(filed 2026\-10\-20 by the S9 pass at 1bb99c7a1\)\.
  Kind: impl
  Parent: M0146-0014
  - Record: TPC\-DS SF1 Q72, category qual\-placement, verdict SHAPE\-DIFF \(the item probe's Nested Loop: goopg
    `Join Filter: \(catalog\_sales\.cs\_item\_sk = item\.i\_item\_sk\)` over `Index Cond: \(i\_item\_sk = inventory\.inv\_item\_sk\)`;
    PG no Join Filter over `Index Cond: \(i\_item\_sk = catalog\_sales\.cs\_item\_sk\)`\)\. Census: the M0146\-0241 fire\-set
    captures on HEAD's code tree, `analysis/m0146/m0146\-0241/fireset/`\.
  - S9 ranking \(SF0\.25 99/99, SF1 98/99, TPC\-H 22/22\): qual\-placement 1 \(SF1 Q72\) is the only open record\.
    M0146\-0194 closed SF0\.25 Q72 as a PG\-internal near\-tie; SF0\.25 Q72 now matches\.
  - PG: the EC \{cs\_item\_sk, inv\_item\_sk, i\_item\_sk\} is enforced once\. create\_nestloop\_path drops every
    restrict clause join\_clause\_is\_movable\_into the parameterised inner, and generate\_join\_implied\_equalities\_normal
    takes the first best\-scoring outer member in EC order \(cs\_item\_sk\)\.
  - Expected instrument: TPC\-DS SF1 PLAN\-PARITY match \(Q72\), 98 → 99\.
  - **Done 2026\-10\-20 — CLOSED\.** Design
    `docs/design/0100\-0149/m0146\-0242\-nestloop\-one\-clause\-per\-equivclass\.md`; evidence `analysis/m0146/m0146\-0242/`\.
    - The joinrel's clause list held every member pair of the class, so each item probe left the sibling it did not
      enforce as a Join Filter; the inventory probe \(cheaper per scan by loop\_count\) won with
      `cs\_item\_sk = i\_item\_sk` as its filter\.
    - nestloopResidualClauses reduces its input with oneClausePerEquivClass before the identity drop: the cs\-keyed
      probe is filterless, the inventory\-keyed one keeps `i = cs`, and PG's probe wins\. A first attempt that dropped
      every sibling made both filterless and was replaced\.
    - Pins: TestNLIResidualKeepsOneClausePerEquivClass \(unit, mutation\-checked\);
      TestNestLoopKeepsOneEquivClassClausePerJoin \(PG\-anchored shape guard, both directions\)\.
    - Gates: units, internal/postmaster, TestPort\_RegressSuite, TestPort\_Isolation\*, tpch\-spotcheck, acceptance
      arm \(values identical\), TPC\-H parity 22/22 \(plans unchanged\), sf025 PASS=99 same=99, tpcds\-fireset PASS\.
  Movement: yes — TPC\-DS SF1 match 98→99 \(Q72\)

## M0147 — Lever retirement (filed 2026-10-21, owner decision)

**Milestone doc:** `docs/milestones/0147-lever-retirement.md`. **Rank:** banner item 10 — M0122-0012 →
**M0147** → the rest of M0122, under the M0122-0012 **return-first rule** in banner item 10 (OWNER DECISIONS
2026-10-27): M0147 tasks run during M0122-0012's one-task yields (when next in item-10 order) and after its
exhaustion.
Basis: the 2026-10-21 lever census and its review
(`/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/0----------plan-party-progress-1021.md`).

**Slice protocol (binding for every M0147 task — owner decision):**
- One task per commit (M0147-0010's four probe switches share their files and retire together). Delete the
  lever's read, the arm it selects that is being retired, the tests that only exercise that arm, and — if the
  lever has a row in `internal/optimizer/flaglabels.go` — keep the row as `retired(<task id>)` and regenerate
  `scripts/planner-flags.env` in the same commit (`TestFlagProvenanceEnvIsGenerated`).
- Keep shared helpers: build first, then delete what the COMPILER reports unused (grep oversizes scope).
- Inert proof (AGENT.md C6): the retired arm is unreachable on the shipped default — no gate, script or
  env file sets the lever (ON-only arm), or the OFF arm is never reached with the lever unset.
- Gates: units, `go test ./internal/postmaster/` when touched, TestPort_RegressSuite, TestPort_Isolation*,
  tpch-spotcheck, acceptance arm (values identical), sf025 sweep PASS=99 **same=99**, tpcds-fireset
  **fires=none**, and the S9 census unchanged (TPC-H 22/22, TPC-DS 99/99, 99/99). A retirement that moves
  ANY plan or value broke a default path: stop and fix it, never re-pin.
- M0147 is inside the plan-parity harness range (AGENT.md, now M0137–M0147): R, C3, C5 (promote or delete —
  HOLD is not an outcome), C6, S3/S4, D and G rules bind it. R3: an arm whose ON side ports PG 18.3 logic is
  promoted, never discarded, without an owner `[!]` decision.
- Ledger: every ledger row / fix_plan task that names a retired lever gets a NEW resolution row (R7,
  append-only) and a note on the task.
- Movement: `Movement: yes — lever-count N→N-k (census 22/22·99/99·99/99 held)` from `scripts/lever-count.sh`
  (34 at filing). A slice that retires no lever literal (stale comments only) closes `Movement: none`.
- Keep list (never retired by M0147; excluded from lever-count): `GOOPG_ANALYZE_SEED`/`_SEED_CHILD`, the traces
  (`PGSHAPED_DP_TRACE`, `NLI_CENSUS`, `GEQO_TRACE[_EVAL]`), `GOOPG_NLI_COSTGATE` (C-20f owner decision,
  `docs/design/not_ralph/minimize_datum/TODO_ALL.md:2565`; the `legacy` arm stays), `GOOPG_WAL_ALLOW_EARLY_END`,
  test-harness / profiling / assertion switches (milestone doc §Keep).
- Order: file order below — phase 1 (0010–0017), then phase 0 (0020–0023), then phase 2 (0030 and the
  children it files).

- [ ] **M0147-0000 — Lever retirement root (tracking channel — NEVER selected)**
  Kind: recon
  Parent: none
  - A channel, not stock (like M0146-0014): never select it; take its first open child in file order. Close it
    only in the commit that closes its last child. Skip-rationale token: `M0147-0000(channel)`. Commit subjects
    cite the child id, never M0147-0000.
- [x] **M0147-0010 — retire the S30.3 / S32.x probes: `GOOPG_PAGEIDENT_PROBE`, `GOOPG_S321_PROBE`, `GOOPG_S321_NOWAIT`, `GOOPG_S322_NOWAIT`**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 34→30 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-24 \(M0122\-0012 yielded under banner rule \(a\)\)\. Files deleted: s321\_probe\.go,
    pageident\_probe\.go, analysis/pageident\_probe\.sh; call sites removed in operators\_storage\.go, bufpool\.go,
    smgr\.go, prefetch\.go; the six NOWAIT conjuncts deleted, the waits kept\. Design doc
    `docs/design/0100\-0149/m0147\-0010\-retire\-s30\-s32\-probes\.md` \(C6 inert proof\)\. Gates: units, spotcheck,
    acceptance 24 MATCH, sf025 PASS=99, census on tree held, RegressSuite, Isolation; fire\-set out of scope\.
  - `internal/storage/pageident_probe.go`, `internal/executor/s321_probe.go` and their hooks (bufpool.go,
    operators_storage.go), plus `analysis/pageident_probe.sh`. **Keep the pending-writer waits**: delete only the
    `!s321NoWait &&` / `!s322NoWait &&` conjuncts at operators_storage.go ~5388/5413/6052/6070/6338/6361 —
    NOWAIT=1 disabled a correctness fix. Resolves ledger row 1194 (new row). lever-count 34→30.
- [x] **M0147-0011 — retire `GOOPG_SORT_PACKED` (packed sort retention)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 30→29 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-24 \(M0122\-0012 yielded under rule \(a\)\)\. The sortOp packed arm deleted; packed\-only
    tests deleted; TestPackedSlotHasNoProducer strict\. Design doc
    `docs/design/0100\-0149/m0147\-0011\-retire\-sort\-packed\.md` \(C6\)\. Gates: units, spotcheck, acceptance 24
    MATCH, sf025 PASS=99 \(SF025\_NO\_PLANS=1; plans captured separately, same=99\), census on tree held,
    RegressSuite, Isolation\.
  - AGENT.md R6/B1 (amended 2026-10-21): no new packed retention; deleting this leftover arm is in scope.
    `packUse` branches in operators.go; packedslot_test.go:657. Supersedes the packed part of M0141-S7-exec-d and
    ledger row 2259 (new rows; note on M0141-S7-exec-d).
- [x] **M0147-0012 — retire `GOOPG_HEAP_PREFETCH_DEPTH`**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 29→28 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-24 \(M0122\-0012 yielded under rule \(a\)\)\. bitmap\_prefetch\.go and its tests deleted; the
    `pf` field, its construction, four drain points and the refill/take branch removed from operators\_bitmap\.go;
    `tbmIterator\.peekBlocks` deleted\. Pool\.StartRead kept\. Design doc
    `docs/design/0100\-0149/m0147\-0012\-retire\-heap\-prefetch\-depth\.md` \(C6\)\. Gates: units, spotcheck,
    acceptance 24 MATCH, sf025 PASS=99 \(SF025\_NO\_PLANS=1; plans standalone same=99\), census on tree held,
    RegressSuite, Isolation\.
  - Remove the prefetch window (`o.pf`) and its construction at operators_bitmap.go:554
    (`newHeapPrefetchWindow(ctx.Pool, o.rel, heapPrefetchDepth())`) with `internal/executor/bitmap_prefetch.go`;
    keep the storage-layer `Pool.StartRead` (reusable per `docs/design/not_ralph/minimize_datum/TODO_ALL.md:4144-4190`,
    which records the measured NO-GO).
- [x] **M0147-0013 — retire `GOOPG_SCALAR_UNNEST` (correlated scalar-subquery decorrelation; PG never does it)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 28→27 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-24 \(M0122\-0012 yielded under rule \(a\)\)\. Switch, `canUnnestSubquery`, `unnestSubquery`,
    `unnestScalarWithResiduals` and 13 helpers left unreachable \(`deadcode ./cmd/...` diff\) deleted; the three
    gates in sublinkjoinclause\.go / local\_filters\.go simplified; flaglabels row → `retired\(M0147\-0013\)`,
    planner\-flags\.env regenerated\. Decorrelation\-only tests deleted; the Q30 CTE test and the M17 duplicate\-outer
    test rebased onto PG\'s SubPlan\. Design doc `docs/design/0100\-0149/m0147\-0013\-retire\-scalar\-unnest\.md` \(C6\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(SF025\_NO\_PLANS=1; plans standalone same=99\),
    fire\-set PASS \(fires=none on SF0\.25 and SF1\), TPC\-H census on tree 22/22, RegressSuite, Isolation\.
  - unnest.go `unnestSubquery` / `unnestScalarWithResiduals` and the inverse gates at sublinkjoinclause.go:155 and
    local_filters.go:631; keep `collectUnnestParamsAndResiduals` (EXISTS path). Tests:
    scalar_unnest_switch_test.go, executor/nl_semi_anti_join_test.go:119, cte_scalar_sublink_unnest_test.go:80 —
    rebase any that pin the default arm onto PG's SubPlan expectation. Ledger row 2819 (new row).
- [x] **M0147-0014 — retire `GOOPG_HASH_OUTER_JOIN`**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 27→26 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-25 \(M0122\-0012 yielded under rule \(a\)\)\. `hashOuterJoinEnabled`, `hashOuterJoinFromEnv`,
    `chooseOuterFillJoinAlgo` deleted; the planner\.go RIGHT/FULL arm is plain `JoinAlgoMerge` \(the shipped default\);
    the hash\-RIGHT `BuildLeft` branch went with the hash arm\. `deadcode ./cmd/...` diff: no new orphan\. flaglabels row
    → `retired\(M0147\-0014\)`, planner\-flags\.env regenerated\. Four hash\-arm\-only tests deleted \(3 in joincost\_test\.go,
    `TestPlanJoinRightFullTakesHashOnceCostable`\)\. Design doc `docs/design/0100\-0149/m0147\-0014\-retire\-hash\-outer\-join\.md`
    \(C6\); ledger resolution row for the 2026\-08\-04 M0127\-P4\.2 and 2026\-09\-03 take2\-P6\-06 rows\.
  - Gates: units, spotcheck, acceptance 24 MATCH \(vs baseline\-digests\), sf025 PASS=99 \(61 ck; SF025\_NO\_PLANS=1;
    plans standalone same=99\), fire\-set PASS \(fires=none on SF0\.25 and SF1\), TPC\-H census on tree 22/22,
    RegressSuite, Isolation\.
  - joincost.go:52; keep `costInnerHash`/`costInnerMerge` (default `chooseInnerJoinAlgo`). Neither arm is PG: the
    post-search `chooseInner/OuterFillJoinAlgo` assignment (planner.go:4873-4887) is goopg-private — list it in
    M0147-0030's phase-2 inventory.
- [x] **M0147-0015 — retire `GOOPG_FORCED_GC`**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 26→25 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. The switch, `maybeForceGCAfterCommit` and its counters
    deleted from dispatch\.go; its 4 call sites \(dispatch\.go autocommit, copy\.go ×2, txn\_verb\.go\) gone;
    `\(\*Context\)\.DidWrite` deleted \(its only caller was the GC guard; deadcode does not flag exported methods, so
    `git grep` confirmed\)\. forced\_gc\_flag\_test\.go and didwrite\_test\.go deleted\. Not a planner flag \(no
    flaglabels row\); fire\-set out of scope \(fireset\-scope\.sh rc=1\)\. postmaster wiki updated\. Design doc
    `docs/design/0100\-0149/m0147\-0015\-retire\-forced\-gc\.md` \(C6\)\. No ledger row named the lever\.
  - Gates: `go test ./internal/postmaster/` PASS \(first run: `TestExplainSplitAggregateChainCosts` i/o timeout on its
    2 s frame deadline during a 600k\-row INSERT under package\-parallel load; passes alone and on the full rerun\),
    units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck; plans standalone same=99\), TPC\-H census on tree
    22/22, RegressSuite, Isolation\.
  - postmaster/dispatch.go `maybeForceGCAfterCommit` and forced_gc_flag_test.go; run `go test ./internal/postmaster/`
    yourself (the units gate excludes it).
- [x] **M0147-0016 — retire the dead `GOOPG_RELSIZE_FALLBACK` read and stale lever text**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 25→24 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. relsize\.go: the never\-loaded stage var \(only
    Store/Swap at HEAD\), init read, parseRelSizeFallbackStage, SetRelSizeFallbackStage deleted; cost\_funcs\.go
    `envFloatDefault` deleted; knob\-is\-live comments fixed in small\_dimension\.go, plan\.go, cardinality\.go;
    flaglabels\.go PG\_SORT\_RELATION\_BYTES\_COST / INCREMENTAL\_SORT comments now say default ON;
    tpch\-relsize\-arm\.sh reduced to cold/warm \(c2/w2 aliases, c1/w1 refused\); M0140 K80 "default\-OFF" text
    annotated\. Tests: TestParseRelSizeFallbackStage and TestSmallDimensionTagIgnoresRelSizeFallbackStage deleted,
    stage loops/setters dropped\. deadcode diff: only the two deleted funcs left the list\. planner\-flags\.env
    unchanged \(row already retired\)\. Design doc
    `docs/design/0100\-0149/m0147\-0016\-retire\-relsize\-fallback\-read\.md` \(C6\); ledger resolution row\.
  - Gates: units, spotcheck, acceptance 24 MATCH, fire\-set PASS \(fires=none on SF0\.25 and SF1\), TPC\-H census on
    tree 22/22, RegressSuite, Isolation, sf025 PASS=99 \(61 ck\) \+ plans standalone same=99 \(first attempt refused
    with FATAL while the nightly batch ran — re\-run after it exited, never FORCE=1\)\.
  - relsize.go:69 (read, stored, never loaded; retired take2-P1-05), its setter and ~10 test call sites
    (relsize_fallback_test.go, small_dimension_test.go, relsize_baserel_placement_test.go), the
    `scripts/tpch-relsize-arm.sh:186` arm that still drives it, the uncalled `envFloatDefault` (cost_funcs.go:14);
    stale comments relsize.go:55-61/:166, flaglabels.go:142/:162 (PG_SORT_RELATION_BYTES_COST and
    INCREMENTAL_SORT are default ON), and the stale "default-OFF" GATHER_PATHS text in the M0140 section.
- [x] **M0147-0017 — retire `GOOPG_DISABLE_NLI` (the M0054-0006 nested-loop-index rollback switch)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 24→23 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. Env read in cmd/goopg/main\.go deleted; the setter went
    too — no PG\-equivalent concern, because the GUC route `goopg.enable_nestloop_index` \(PlannerSettings\) already
    exists: `nliEnabled`, `SetNLIEnabled`, `NLIEnabled` deleted and DefaultPlannerSettings seeds `true`\.
  - Tests: TestNLIRuleRespectsKillSwitch now plans with `PlannerSettings\.EnableNestLoopIndex=false`\. The two
    cluster\-backed parity tests in internal/testutil/tpch were VACUOUS — their off arm called SetNLIEnabled in the test
    process, which the cluster's server subprocess never saw, and on 5\-row fixtures the default plan is a Hash Join
    anyway\. They now compare the default plan with a nested loop forced by PG's `enable\_hashjoin/enable\_mergejoin/
    enable\_seqscan=off` \(new `cluster\.QueryWithOptions`, startup `options`\) and assert the two EXPLAINs differ\.
  - Finding \(ledger row; M0147\-0030 note\): with those switches off, `goopg\.enable\_nestloop\_index=off` still did not
    change the plan — the legacy post\-search rewriteJoinsToNLI pass did not fire\.
  - Wiki cmd\-goopg\.md env table / flow updated\. Design doc
    `docs/design/0100\-0149/m0147\-0017\-retire\-disable\-nli\.md` \(C6\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    on SF0\.25 and SF1\), TPC\-H census on tree 22/22, RegressSuite, Isolation, the two rebased cluster parity tests
    PASS \(go test \-run TestNLIResultParity ./internal/testutil/tpch/\)\.
  - cmd/goopg/main.go:299 and the `SetNLIEnabled` plumbing the switch reaches; keep the setter only if tests use it
    to isolate a path and no PG-equivalent switch exists (`goopg.enable_nestloop_index` is the GUC route).
- [x] **M0147-0020 — promote-or-delete `GOOPG_PARTIAL_SORT_PATHS` (ON = PG cost_sort / cost_gather_merge comparison; OFF = runtime-justified `sortPartialRootPays` type switch)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 23→22 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 — PROMOTED \(M0122\-0012 yielded under rule \(a\)\)\. C6 trace \(`GOOPG\_PGSHAPED\_DP\_TRACE=1`
    captures, `upper gate=sort` records\): the post\-pass IS reached — OFF 3 `sort\-rule` verdicts at SF0\.25, 2 at SF1;
    ON worker 1 / serial 1 / declined 1 at SF0\.25, worker 1 / serial 1 at SF1\. ON captures: goopg plans byte\-identical
    to the OFF census \(0 diff lines\), PLAN\-PARITY 99/99 both\. TPC\-H census lane is serial \(post\-pass unreachable\);
    TPC\-H parallel values via the acceptance arm\.
  - Deleted: mode type/consts/env read/resolver/label, both test setters, the mode\-off branch;
    `sortPartialRootPays` KEPT as the no\-tournament fallback\. flaglabels → `retired\(M0147\-0020\)`, planner\-flags\.env
    regenerated\. Tests: the fail\-closed resolver test and the off\-inertness test deleted; two tests drop their mode pins\.
    deadcode diff: only the two setters left\. Design doc
    `docs/design/0100\-0149/m0147\-0020\-promote\-partial\-sort\-paths\.md` \(C\-19e DESIGN\.md status line added\); ledger row
    \(residual: PG decides in generate\_useful\_gather\_paths/create\_ordered\_paths; goopg in a post\-pass with default
    cost params\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite, Isolation\.
  - First: trace whether the post-pass is still reached now that Gather comes from the search (C6 trace over the
    full corpus). Unreachable: delete both arms. Reached: flip ON, run the gates, then delete the OFF arm. Never
    delete the ON arm without an owner `[!]` (R3). Runtime is not a rejection reason (S9(5)(a)).
- [x] **M0147-0021 — promote `GOOPG_PG_HASH_TUPLE_SPILL_COST` (costsize.c initial_cost_hashjoin / nodeHash.c ExecChooseHashTableSize)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 22→21 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 — PROMOTED \(M0122\-0012 yielded under rule \(a\)\)\. C3 first: ON captures on both TPC\-DS corpora
    byte\-identical to the OFF census \(0 diff lines incl\. costs; 99/99 both\) although the arm engaged \(`DPPGHASH
    currency=pg` on \~164k offered hash joins, batches up to 131072\) — no plan moved, so executor memory cannot move;
    acceptance arm \(TPC\-H SF1 executed, 24 MATCH\) and SF1 fire\-set \(fires=none\) PASS on the promoted tree\.
  - Deleted the switch/env/resolver/enabled/test setter; `pgHashGeometry`/`pgHashSpillPages` kept; invalid PG width
    still falls back to map pricing\. No DPPGHASH reader outside the package \(estimate\-audit does not parse it\)\.
  - Tests: trace \+ cost tests rebased \(map reference = unknown\-width fallback\); three settings\-propagation tests'
    `work\_mem` arms rebased from 16kB on the 100\-row build \(spilled only goopg's map geometry; PG's packed geometry
    never batches it at a legal budget\) to 64kB on \~10000\-row builds with per\-arm baselines; wide IN shape pulls up
    to a semi\-join, so work\_mem covers the other four sublink shapes\.
  - Design doc `docs/design/0100\-0149/m0147\-0021\-promote\-pg\-hash\-tuple\-spill\-cost\.md`; m0139\-0007a doc status line;
    ledger row \(resolves 2188's hash half; residual: executor still sizes with map geometry\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite, Isolation\.
  - **C3 first**: ledger row 2188 warns the planner may charge less memory than the executor allocates — the
    acceptance arm and the SF1 fire set must pass with the arm ON; a memory regression is NOT waived by
    S9(5)(a). Keep `pgHashGeometry`/`pgHashSpillPages`. Retiring the lever changes `tracePGHashTupleGeometry`'s
    PGSHAPED_DP_TRACE output — check the estimate-audit readers. On success delete the OFF arm; ledger 2188 new row.
- [x] **M0147-0022 — complete and promote `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 21→20 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 — PROMOTED \(M0122\-0012 yielded under rule \(a\)\)\. "Complete" was already done: the per\-key
    `get\_expr\_width` term landed in M0139\-0007c \(`costMemoizeRescan` `keyWidth` / `memoizeKeyWidths`\); the task text
    predates it\. ON captures: 0 changed plan lines on both TPC\-DS corpora \(41 / 26 Memoize nodes\), 99/99 both\.
  - Deleted the switch/env/resolver/enabled/test setter; PG currency now whenever `pgRelationByteSize` succeeds,
    unknown\-width fallback to goopg's ruler kept \(both terms together\); `pgRelationByteSize` kept\. Tests: the
    switch\-off test replaced by an unknown\-width fallback test; two currency tests drop pins and OFF halves\.
  - Design doc `docs/design/0100\-0149/m0147\-0022\-promote\-pg\-memoize\-entry\-bytes\-cost\.md`; m0139\-0007b doc status
    line; ledger row \(resolves line 2189; residual: goopg min clamps \+ unknown\-width fallback\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite, Isolation\.
  - The port is incomplete: add PG's per-key `get_expr_width` term (cost_memoize_rescan /
    ExecEstimateCacheEntryOverheadBytes; ledger row 2189), then flip ON, run the gates, delete the OFF arm. Keep
    `pgRelationByteSize`.
- [!] **M0147-0023 — promote `GOOPG_NL_BARE_REEXEC` (PG ExecReScan semantics for a bare nested-loop inner)**
  Kind: impl
  Parent: M0147-0000
  - blocked-on M0146-0010a (the same flip; Held, live blocker = the Q4/Q11 `year_total` CTE nested-loop estimates;
    M0142-0008c-2 already landed). Closes as a pointer when M0146-0010a lands (then delete the attach-time compat
    cache arm and the lever).
- [x] **M0147-0030 — phase 2 recon: default-ON levers' OFF arms (goopg-private legacy)**
  Kind: recon
  Parent: M0147-0000
  - Inventory every default-ON lever left in `scripts/lever-count.sh --list` (MEMOIZE, PARALLEL, EXISTS_TO_ANY,
    INDEXKEY_HARVEST, NARROW_*, PARTIAL_AGG_PATHS, PG_SORT_RELATION_BYTES_COST, INCREMENTAL_SORT,
    COOP_JOIN_BUILD, HASHED_SUBPLAN, JOIN_SLOT_CHAIN, NL_MATERIALIZE_WORK_MEM, SCAN_PREFILTER, SUBPLAN_RESCAN,
    GATHER_PATHS off/top) plus the goopg-private post-search join-algorithm assignment (M0147-0014 note).
  - For each: what the OFF arm is, which tests flip it (e.g. `setPartialAggPathsModeForTest` ×36) and how those
    tests rebase onto a PG oracle expectation or PG's own switch (e.g. `max_parallel_workers_per_gather=0`
    instead of `GATHER_PATHS=off`; AGENT.md C4 amended 2026-10-21) before the arm goes.
  - Files one impl child per lever (M0147-0031…, `Parent: M0147-0000`), lowest risk first; done when they exist.
  - Add to the inventory \(M0147\-0017 finding, 2026\-10\-26\): `goopg.enable_nestloop_index` and the post\-search
    `rewriteJoinsToNLI` pass it gates — on a forced nested\-loop fixture the pass did not fire \(the search built the
    join\); measure its reachability with a census under `PGOPTIONS='\-c goopg.enable_nestloop_index=off'` first\.
  Movement: none — recon
  - Done 2026\-10\-26: inventory `analysis/m0147/m0147\-0030/INVENTORY\.md` \(19 env levers \+ the two env\-less post\-search
    passes\)\. No gate/CI/bench script sets any lever — every OFF arm is test/A\-B only\. Children M0147\-0031…0050 filed
    below, lowest risk first \(executor kill\-switches → narrowing family → PG\-switch rebases → PG\-faithfulness\-in\-
    question \(EXISTS\_TO\_ANY, INDEXKEY\_HARVEST\) → recon\)\.
- [x] **M0147-0031 — retire `GOOPG_NARROW_LEG_HOOK` (counting-only join-leg hook switch)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 20→19 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. Correction: the hook is NOT counting\-only — M0139\-S2 made
    it load\-bearing \(narrowing `\*Project` on join legs\); the stale flaglabels comment and the 0030 inventory row are
    corrected\. Switch \+ resolver deleted, hook unconditional; two tests rebased to the single arm; flaglabels →
    `retired\(M0147\-0031\)`; deadcode diff empty\. Design doc `docs/design/0100\-0149/m0147\-0031\-retire\-narrow\-leg\-hook\.md`\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite, Isolation\.
  - joinleghook.go:49; the OFF arm changes no node (it only disables leg counting), 8 direct test refs in 1 file. Inert proof by construction; keep the hook. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0032 — retire `GOOPG_COOP_JOIN_BUILD` OFF arm (pre-coop nested build rebuild)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 19→18 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. `coopJoinBuildOn` deleted; coopDrivingScan\'s Join arm
    unconditional; the stale "OFF by default" comment corrected\. `TestCoopBuildRecycledBatchesMatchSerialBuild` took
    its "serial" reference from the coop flag, which gates only the join\-tree widening — vacuous for its bare\-scan /
    column\-only\-Project builds; rebased onto `ctx\.MaxParallelWorkers = 0`\. Design doc
    `docs/design/0100\-0149/m0147\-0032\-retire\-coop\-join\-build\.md`\. Fire\-set out of scope \(executor only\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, TPC\-H census on tree
    22/22, RegressSuite, Isolation\.
  - executor parallel_hash_build.go:481; 4 direct test refs. Values gates + TPC-H parallel acceptance arm. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0033 — retire `GOOPG_NL_MATERIALIZE_WORK_MEM=0` (unbounded NL inner cache)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 18→17 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. `nlInnerWorkMemEnabled`, both `setUnbounded` branches and
    `materializeOp\.setUnbounded` deleted — the NL inner cache is always work\_mem\-bounded \(PG tuplestore spill\);
    `withNLInnerWorkMem` helper \+ 3 calls and the spill test\'s env skip removed\. Ledger resolution row \(M0127\-P4\.3
    knob\)\. Design doc `docs/design/0100\-0149/m0147\-0033\-retire\-nl\-materialize\-work\-mem\.md`\. Fire\-set out of scope\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, TPC\-H census on tree
    22/22, RegressSuite, Isolation\.
  - executor join_nl_stream.go:52; ON = PG tuplestore spill past work_mem (M0146-0010c); 3 direct test refs. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0034 — retire `GOOPG_SCAN_PREFILTER` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 17→16 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. The env lever \(`scanPrefilterOn` \+ init read \+
    exported `SetScanPrefilterEnabled`\) is retired, but its forced\-LATE arm is a real oracle for 4 tests \(early vs late
    values / error position / IN\-list / lead / skip identity\), so it stays as the unexported, env\-free test seam
    `scanPrefilterForceLate` \(the `scanSkipDisabled` precedent; PG has no switch to force a late qual\)\. Design doc
    `docs/design/0100\-0149/m0147\-0034\-retire\-scan\-prefilter\.md`\. Fire\-set out of scope\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, TPC\-H census on tree
    22/22, RegressSuite, Isolation\.
  - executor scan_prefilter.go:24; SetScanPrefilterEnabled ×5 — values-identity tests become single-arm. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0035 — retire `GOOPG_JOIN_SLOT_CHAIN` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 16→15 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. Switch, exported test setter and the per\-join
    `lazyChainProbe` field deleted; bindProbe chains whenever the child slot is wide enough; the copy path stays as the
    narrow\-slot fallback\. OFF arm was NOT an oracle \(worker tests compare against a serial baseline\), so the
    kill\-switch test and the benchmark/worker off arms were removed\. Design doc
    `docs/design/0100\-0149/m0147\-0035\-retire\-join\-slot\-chain\.md`\. Fire\-set out of scope\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, TPC\-H census on tree
    22/22, RegressSuite, Isolation\.
  - executor operators_join_agg.go:1681; SetJoinSlotChainEnabled ×8. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0036 — retire `GOOPG_SUBPLAN_RESCAN` OFF arm (legacy per-call SubPlan + CorrSubqOps registry)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 15→14 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. Switch \+ exported setter deleted; the legacy
    Build/Open/Close\-per\-call branch of acquireSubPlanOp, subPlanResultCacheable\'s always\-true legacy arm, the
    expr\.go CorrSubqOps branch and the `Context\.CorrSubqOps` field deleted — the Stage\-9 handle \(PG nodeSubplan
    rescan shape\) is unconditional\. Legacy\-lifecycle test deleted; the DML engine\-settings test keeps its hashed\-IN
    half\. Design doc `docs/design/0100\-0149/m0147\-0036\-retire\-subplan\-rescan\.md`\. Fire\-set out of scope\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, TPC\-H census on tree
    22/22, RegressSuite, Isolation\.
  - executor subplan.go:36; SetSubPlanRescanEnabled ×5; delete the legacy registry with it if nothing else reads it (deadcode diff). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0037 — retire `GOOPG_HASHED_SUBPLAN` OFF arm (linear IN-slice scan)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 14→13 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. Env lever \+ exported setter retired; the forced\-linear
    arm is the hashed tests\' correctness ORACLE \(subplan\_hash\_test, big\-numeric, DML engine\-settings\), so it stays as
    the unexported env\-free test seam `hashedSubPlanForceLinear` \(drives both the probe and the EXPLAIN label\);
    the linear slice scan also stays as the production non\-hashable fallback\. Design doc
    `docs/design/0100\-0149/m0147\-0037\-retire\-hashed\-subplan\.md`\. Fire\-set out of scope\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, TPC\-H census on tree
    22/22, RegressSuite, Isolation\.
  - executor subplan_hash.go:48; ON = PG hashed SubPlan; SetHashedSubPlanEnabled ×10; the linear path survives as the non-hashable fallback. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0038 — retire `GOOPG_NARROW_UPPER_SORT` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 13→12 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. `narrowUpperSort` \+ resolver deleted; the upper\-Sort
    narrowing site is unconditional under GOOPG\_NARROW\_UPPER \(0039 next\); flaglabels → `retired\(M0147\-0038\)`,
    planner\-flags\.env regenerated; the flag\-off test keeps its NARROW\_UPPER=0 case\. Design doc
    `docs/design/0100\-0149/m0147\-0038\-retire\-narrow\-upper\-sort\.md`\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite, Isolation\.
  - optimizer upper_narrow_chain.go:124; 2 direct test refs; flaglabels row. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0039 — retire `GOOPG_NARROW_UPPER` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 12→11 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-26 \(M0122\-0012 yielded under rule \(a\)\)\. `narrowUpper` \+ resolver deleted; both B\-01c upper
    sites \(Aggregate, Sort\) unconditional; the two flag\-off pins deleted; flaglabels → `retired\(M0147\-0039\)`,
    planner\-flags\.env regenerated\. Design doc `docs/design/0100\-0149/m0147\-0039\-retire\-narrow\-upper\.md`\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, Isolation; RegressSuite PASS on rerun \(first run: no case diverged, only a
    `TempDir RemoveAll … directory not empty` teardown flake\)\.
  - optimizer upper_narrow_apply.go:90; 5 direct test refs in 2 files; it also gates NARROW_UPPER_SORT — land after 0038. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0040 — retire `GOOPG_NARROW_BUILD` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 11→10 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(M0122\-0012 yielded under rule \(a\)\)\. `narrowBuild` \+ `narrowBuildFromEnv` \+ resolver
    deleted; `narrowBuildInput` and `narrowMergeInput` decline only on structural conditions; flaglabels →
    `retired\(M0147\-0040\)`, planner\-flags\.env regenerated\. Design doc
    `docs/design/0100\-0149/m0147\-0040\-retire\-narrow\-build\.md`\.
  - Tests: none of the 34 refs was an OFF\-arm oracle — nine ON pins deleted, the wiring test's flag column and
    flag\-off case dropped, the merge subtest kept its keyless half, the polarity test went with its function\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(363 s\), Isolation PASS \(543 s\)\.
  - optimizer narrowoutput.go:11; 34 direct test refs in 2 files. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0041 — retire `GOOPG_NARROW_COST_INPUTS` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 10→9 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `16101a6a1`; M0122\-0012 yielded under rule \(a\)\)\. Env read,
    `narrowCostInputsFromEnv`, `setNarrowCostInputsForTest` and the resolver deleted; flaglabels →
    `retired\(M0147\-0041\)`, planner\-flags\.env regenerated\. Design doc
    `docs/design/0100\-0149/m0147\-0041\-retire\-narrow\-cost\-inputs\.md`\.
  - Tests: the live\-search pin uses the OFF arm as a genuine oracle \(a searched join cost must move\), so it keeps an
    env\-free seam `narrowCostInputsForceOff` \(scanPrefilterForceLate precedent\)\. 24 ON pins and four switch\-only
    tests deleted; no cost pin needed re\-deriving, since ON was already the default every pin ran under\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(311 s\), Isolation PASS \(547 s\)\.
  - optimizer narrowcostinputs.go:66; setNarrowCostInputsForTest ×30; cost pins re-derived at narrowed widths. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0042 — retire `GOOPG_PG_SORT_RELATION_BYTES_COST` OFF arm (goopg Sort currency)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 9→8 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `6c45b63bd`; M0122\-0012 yielded under rule \(a\)\)\. Env read, polarity
    func, `pgSortRelationBytesCostEnabled` and the test setter deleted; `costSortRunWithWidth` elects the PG
    relation\_byte\_size currency whenever its width is valid, goopg entry bytes kept only as the unknown\-width
    fallback; flaglabels → `retired\(M0147\-0042\)`, planner\-flags\.env regenerated\. Design doc
    `docs/design/0100\-0149/m0147\-0042\-retire\-pg\-sort\-relation\-bytes\-cost\.md`\.
  - Tests: six ON pins deleted, the switch\-off\-is\-legacy test deleted, the trace test's off half re\-pinned on the
    width\-0 fallback \(`currency=goopg`, `pgbranch=unknown\-width`\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(321 s\), Isolation PASS \(559 s\)\. The first acceptance and
    sf025 attempts refused while the nightly ran \(01:05\-02:21\); both passed on rerun after it ended\.
  - optimizer sort_pgrelationbytes.go:17; ON = PG cost_sort relation bytes; setPGSortRelationBytesCostForTest ×8; keep the invalid-width fallback (M0147-0021 pattern). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0043 — retire `GOOPG_INCREMENTAL_SORT` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 8→7 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `77d4c33b6`; M0122\-0012 yielded under rule \(a\)\)\. Mode type, env read
    and both setters deleted; `addIncrementalSortPaths` and the `generateUsefulGatherPaths` sibling gate only on
    `cp\.enableIncrementalSort` \(PG's enable\_incremental\_sort\); flaglabels → `retired\(M0147\-0043\)`,
    planner\-flags\.env regenerated\. Design doc `docs/design/0100\-0149/m0147\-0043\-retire\-incremental\-sort\.md`\.
  - Tests: the OFF\-is\-inert test rebased onto `cp\.enableIncrementalSort = false`; three ON pins and the env\-polarity
    test deleted; the flag\-detector test's helper\-wrapped fixture moved to `GOOPG_NLI_COSTGATE`, scanning with
    `flaglabels\.go` excluded \(it names every flag and made the check vacuous\)\.
  - Finding: the Incremental Sort path charges `enable_sort` in `DisabledNodes`; PG's `cost_incremental_sort` does
    not \(costsize\.c:2123\-2125\)\. Filed as M0146\-0243 \(below item 10\) with a ledger row\.
  - Gates: units, spotcheck, acceptance PASS, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(303 s\), Isolation PASS \(647 s\)\.
  - optimizer incrementalsortpaths.go:86; 5 direct test refs; rebase onto PG enable_incremental_sort (PlannerSettings.EnableIncrementalSort). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0044 — retire `GOOPG_MEMOIZE` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 7→6 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `abd93c55a`; M0122\-0012 yielded under rule \(a\)\)\. `memoizeOn`, the env
    read, `memoizeFromEnv`, `SetMemoizeEnabled` and `MemoizeEnabled` deleted; both Memoize arms answer only to the
    statement's `enable_memoize`\. The legacy NLI insertion rule \(`maybeAttachMemoize`\) now receives
    `ps\.EnableMemoize` via `walkRewriteNLI` — before this, a session's `SET enable_memoize = off` did not reach it\.
    Design doc `docs/design/0100\-0149/m0147\-0044\-retire\-memoize\.md`\.
  - Tests: attach/getMemoizePath kill\-switch tests rebased onto the per\-statement setting; executor
    `TestMemoizeExplainAndResults` off arm plans through `runSQLWith` with EnableMemoize/HashJoin/MergeJoin off \(the
    bare NLI\), and asserts it is still a Nested Loop\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(314 s\), Isolation PASS \(567 s\)\.
  - optimizer memoize.go:34; SetMemoizeEnabled ×13; rebase onto PG enable_memoize (PlannerSettings.EnableMemoize). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0045 — retire `GOOPG_PARALLEL` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 6→5 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `dd246d001`; M0122\-0012 yielded under rule \(a\)\)\. `parallelOn`, the env
    read, `parallelFromEnv`, `SetParallelEnabled`, `ParallelEnabled` and the post\-pass strip branch deleted; the four
    gates \(`parallelModeOK`, partial DISTINCT, partial\-agg upper, post\-pass entry\) answer only to
    max\_parallel\_workers\_per\_gather, which already refused/stripped at 0\. Design doc
    `docs/design/0100\-0149/m0147\-0045\-retire\-parallel\.md`\.
  - Tests: `withParallelOn` deleted and its 40 callers unwrapped; 7 direct ON pins deleted; both kill\-switch tests
    rebased onto `MaxWorkersPerGather = 0` with a non\-vacuity check; executor `runForced` serial arm via `runSQLWith`
    \+ `MaxParallelWorkersPerGather = 0`\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(323 s\), Isolation PASS \(543 s\)\.
  - optimizer parallel.go:45; SetParallelEnabled ×8; rebase onto PG max_parallel_workers_per_gather = 0. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0046 — retire `GOOPG_GATHER_PATHS` `off`/`top` arms (AGENT.md C4)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 5→4 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `637799d73`; M0122\-0012 yielded under rule \(a\)\)\. Mode type, env read,
    label/polarity funcs and both setters deleted; the ten off/top gates \(three gather generators, four partial join
    producers, partial SetOp\) removed, so Gather admission is PG's generate\_useful\_gather\_paths at every rel with
    partial paths\. Design doc `docs/design/0100\-0149/m0147\-0046\-retire\-gather\-paths\-off\-top\.md`\.
  - Tests: `gpSearch`/`phjJoinrel` lose their mode parameter; six mode\-only tests/subtests deleted; the upper\-rel
    refusal test and both executor C\-19f consumer checks rebased onto max\_parallel\_workers\_per\_gather = 0
    \(`c19fSerialPlan`\); flaglabels' orphaned resolver comments \(0040/0041/0046\) cleaned\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(316 s\), Isolation PASS \(553 s\)\.
  - optimizer gatherpaths.go:80; setGatherPathsModeForTest ×18 + SetGatherPathsMode ×5; rebase onto max_parallel_workers_per_gather = 0; `all` stays. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0047 — retire `GOOPG_PARTIAL_AGG_PATHS` OFF arm (retired size rule)**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 4→3 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `3a8bfaa96`; M0122\-0012 yielded under rule \(a\)\)\. Measured first: the OFF
    arm is live and far from PG — TPC\-H 10/22, SF0\.25 78/99, SF1 74/99 vs 22/99/99 ON
    \(`analysis/m0147/m0147\-0047/`\)\. Mode plumbing, both OFF branches, the size rule `splitAggregateIsProfitable`
    with its five non\-PG constants, and the duplicate `parallelDivisor` deleted; the priced tournament is
    unconditional\. Design doc `docs/design/0100\-0149/m0147\-0047\-retire\-partial\-agg\-paths\.md`\.
  - Tests: 32 ON pins deleted; FailsClosed keeps its two PG refusals; nine OFF/size\-rule\-only tests deleted
    \(incl\. a duplicate of TestGetParallelDivisor\); executor `runAggSplitPriced` no longer flips the mode\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(338 s\), Isolation PASS \(520 s\)\.
  - optimizer partialaggpaths.go:62; setPartialAggPathsModeForTest ×35; measure reachability + ON-vs-OFF plan diff first (M0147-0020 recipe). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [!] **M0147-0048 — decide and retire `GOOPG_EXISTS_TO_ANY` OFF arm**
  Kind: impl
  Parent: M0147-0000
  - Measured 2026\-10\-27 at `b2665b45c` \(`analysis/m0147/m0147\-0048/`\): with `GOOPG_EXISTS_TO_ANY=off` the census
    reads TPC\-H 22/22, TPC\-DS SF0\.25 97/99 and SF1 97/99 — Q10 and Q35 SHAPE\-DIFF \(join\-order,
    parameterisation, qual\-placement\) at both scales; ON is 22/99/99\. Q10/Q35 are the OR\-of\-EXISTS witnesses
    the pass exists for, and PG's own plans show the hashed ANY SubPlan there\.
  - Decision per this entry's rule: ON is NOT PG logic\. PG plans both forms in `make\_subplan` and setrefs\.c keeps
    the cheaper per expected calls \(AlternativeSubPlan\); goopg's ON always takes the hashed form\. Neither arm is
    PG's choice, so the OFF arm is not deleted without the owner \(R3\)\. The PG\-faithful route is M0146\-0012b
    \(port the cost choice\), after which both arms retire together\.
  > \#\# ESCALATION 2026\-10\-27 \(R3\) — M0147\-0048: retire the EXISTS→ANY OFF arm now, or hold it behind M0146\-0012b?
  > ON matches PG on every corpus query \(OFF loses TPC\-DS Q10/Q35\), but ON is an unconditional stand\-in for PG's
  > AlternativeSubPlan cost choice, not a port of it\. Owner: either authorise retiring OFF now \(lever\-count 3→2;
  > the corpus evidence says ON is the closer arm\), or keep 0048 held until M0146\-0012b lands and retire both
  > env arms in favour of the cost choice\. The loop continues with M0147\-0049 meanwhile\.
  - optimizer exists_to_any.go:79; SetExistsToAnyEnabled ×6. ON is goopg's unconditional analogue of convert_EXISTS_to_ANY (PG keeps an AlternativeSubPlan; ledger M0146-0012b) — if ON is not PG logic, R3 needs an owner [!] before the OFF (PG-closer per-row SubPlan) arm goes. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0049 — measure and retire `GOOPG_INDEXKEY_HARVEST` OFF arm**
  Kind: impl
  Parent: M0147-0000
  Movement: yes — lever-count 3→2 \(census 22/22·99/99·99/99 held\)
  - Done 2026\-10\-27 \(S9 exhausted at `b2665b45c`, reused — `4ac79cdee` touched no planner path; M0122\-0012
    yielded under rule \(a\); M0147\-0048 held \[\!\]\)\. Measured first: with the harvest off the corpus EXPLAIN text
    is byte\-identical to on \(0 differing lines, all three corpora; `analysis/m0147/m0147\-0049/`\), so the
    2026\-09\-05 not\-plan\-neutral note is superseded\. ON is the PG\-shaped arm \(PG pulls IN/EXISTS up before
    any index choice\)\. Switch, env read and setter deleted; `harvestIndexKeyParams` unconditional\. Design doc
    `docs/design/0100\-0149/m0147\-0049\-retire\-indexkey\-harvest\.md`\.
  - Tests: 8 ON pins deleted; the two executor SubPlan\-path result tests now take that path from the test\-only
    `SetSubqueryUnnestEnabled\(false\)` \(restored via t\.Cleanup\)\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(303 s\), Isolation PASS \(645 s\)\.
  - optimizer unnest.go:38; SetIndexKeyHarvestEnabled ×22; after M0147-0013 the harvest serves IN/EXISTS only — re-measure reachability and the 2026-09-05 not-plan-neutral note first. Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.
- [x] **M0147-0050 — recon: `goopg.enable_nestloop_index` / `rewriteJoinsToNLI` and the post-search `jn.Algo` assignment reachability**
  Kind: recon
  Parent: M0147-0000
  Movement: none — recon
  - Done 2026\-10\-27 \(`analysis/m0147/m0147\-0050/`\)\. GUC off: corpus EXPLAIN byte\-identical \(22/99/99\)\.
    Probe: the post\-pass rewrites 0 corpus joins and 20 regress joins \(12 LEFT, 8 INNER\); disabling it keeps the
    regress suite PASS with the same 81 mismatches \(differences are temp\-file names, known flappers and an
    unordered catalog listing\)\. The post\-search `jn\.Algo` assignment's customers: 2 FULL joins per TPC\-DS
    corpus \(MATCH PG\) and 1306 regress joins \(889 CROSS\)\. Children filed below\.
  - Run the three-corpus census with `PGOPTIONS='-c goopg.enable_nestloop_index=off'` (M0147-0017 finding: the pass
    did not fire on a forced fixture) and count the join trees the PG-shaped search did not build (the M0147-0014
    post-search assignment's only customers); file promote-or-delete children.
- [x] **M0147-0051 — delete the legacy NLI post\-pass \(`rewriteJoinsToNLI`\) and `goopg\.enable_nestloop_index`**
  Kind: impl
  Parent: M0147-0050
  Movement: none — parity held: the pass was corpus\-inert \(M0147\-0050\) and customer\-free after M0147\-0053 \(census 22/22·99/99·99/99, regress outputs unchanged bar the plpgsql flapper\)
  - Done 2026\-10\-27\. Second trial \(call disabled\): units gate green\. Deleted the call, `rewriteJoinsToNLI`,
    `walkRewriteNLI`, `tryBuildNLI` and the 22 helpers only it reached \(deadcode fixed point\), `memoize\.go`
    \(`maybeAttachMemoize`\), the `GOOPG\_NLI\_COSTGATE` escape hatch, `PlannerSettings\.EnableNestLoopIndex` with its
    readBool/fingerprint bit, and the `goopg\.enable\_nestloop\_index` GUC \(now an unrecognised parameter, as in
    PG\)\. \~3400 lines removed\. Design doc `docs/design/0100\-0149/m0147\-0051\-delete\-legacy\-nli\-post\-pass\.md`\.
  - Tests: six legacy\-only test files and the legacy tests in mixed files deleted; flag\-detector fixture →
    `GOOPG\_EXISTS\_TO\_ANY`; misc GUC tests → `goopg\.enable\_opportunistic\_prune`\.
  - Follow\-up noted in the design doc: `stampSemiProbePrices` and nlicensus's `rewrite` route are probably inert now\.
  - Gates: units, postmaster package, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set
    PASS \(fires=none both\), TPC\-H census on tree 22/22, RegressSuite PASS \(81 mismatches; only plpgsql moved,
    1659→1655 diff lines\), Isolation PASS\.
  - Filed by M0147\-0050: the pass rewrites no corpus join and changes no regress verdict; PG has no such pass \(its
    parameterised index nested loop is an ordinary `match\_unsorted\_outer` path\)\.
  - First step: list the 20 regress rewrites \(probe recipe in `analysis/m0147/m0147\-0050/README\.md`\) and compare
    each statement's EXPLAIN with PG 18\.3; delete the pass, `tryBuildNLI`'s legacy callers, `maybeAttachMemoize` if
    left unreachable, and the goopg\-private GUC \(keep accepting `SET goopg\.enable\_nestloop\_index` only if a
    client depends on it\)\.
  - **Blocked\-on M0147\-0052** \(2026\-10\-27 trial\)\. With the pass's single call site disabled, the units gate
    fails exactly one test: `TestParallelNLIJointypeIdentity/left` \(`internal/executor/parallel\_nli\_jointype\_identity\_test\.go`\)\.
    Its `o LEFT JOIN i ON i\.k = o\.k AND o\.id > 0` is a join the PG\-shaped search declines, and the legacy
    pass is what turns it into the `Nested Loop Left Join` over an index probe that PG 18\.3 elects for this fixture
    \(M0145\-0030\)\. Deleting the pass first would move such unsearched shapes AWAY from PG; the search must build them
    first \(M0147\-0052\), after which the pass has no customers and goes\. No code changed by the trial\.
  - **Unblocked 2026\-10\-27** by M0147\-0053 \(`54223610d`\): the witness now plans through the search, and its plan
    is PG 18\.3's\. Re\-run the trial \(disable the call, units gate\) before deleting; any other test that still needs
    the pass names the next unsearched class\.
- [x] **M0147-0052 — recon: why regress joins bypass the PG\-shaped search \(the post\-search `jn\.Algo` population\)**
  Kind: recon
  Parent: M0147-0050
  Movement: none — recon
  - Done 2026\-10\-27 \(`analysis/m0147/m0147\-0052/`\)\. One probed regress run: seam declines by reason —
    `lateral` 968 \(≈ the 889 unsearched CROSS joins\), `leaf\-count` 87, `outer\-on\-qual` 45, `prefix\-exceeds\-bindings`
    19, `outer\-link\-no\-sjinfo` 19, tail 9 \(plus 9716 single\-relation statements that need no search\)\.
    `lateral`/`leaf\-count` are banner\-S9\(2\)\-routed to M0145\-0003/0010; `outer\-on\-qual` filed below as the impl
    child that unblocks M0147\-0051; the small classes stay listed in the README\.
  - Filed by M0147\-0050: 1306 regress joins and 2 TPC\-DS FULL joins per scale reach `walkRewriteNLI` unsearched, so
    `planFromItem`'s post\-search `jn\.Algo` assignment, not add\_paths\_to\_joinrel, picks their method\.
  - First step: classify the 1306 by the reason the search declined them \(CROSS 889 first\); file a parity task per
    class that PG plans through the search\.
  - Witness for the NLI\-dependent class \(from the M0147\-0051 trial\): `TestParallelNLIJointypeIdentity/left` —
    a LEFT JOIN whose ON clause carries an outer\-only conjunct \(`AND o\.id > 0`\)\. It unblocks M0147\-0051\.
- [x] **M0147-0053 — admit outer\-join ON conjuncts that test only the preserved side \(the `outer\-on\-qual` seam decline\)**
  Kind: impl
  Parent: M0147-0052
  Movement: none — instrument artefact: the class has no corpus or regress\-EXPLAIN witness \(census 22/22·99/99·99/99, regress outputs identical\); the witness statement now plans PG 18\.3's exact shape \(scratch\-PG verified\)
  - Done 2026\-10\-27\. `outerOnQualsOK` admits a preserved\-side\-only conjunct; the seam carries it as an
    `ojHeldQual` \(scope = one column per leaf\); `appendOJHeld` gives it a restrict info with the outer join's scope
    as `relids` in exactly the problem that splits the join, flagged `ojHeld` and priced as a restriction
    \(PG `distribute\_qual\_to\_rels`, `clause\_selectivity`\)\. Design doc
    `docs/design/0100\-0149/m0147\-0053\-outer\-join\-preserved\-side\-on\-qual\.md`\.
  - Witness `o LEFT JOIN i ON i\.k = o\.k AND o\.id > 0` now plans `Nested Loop Left Join` / `Join Filter: \(o\.id >
    0\)` / parameterised index probe — identical to scratch PG 18\.3\. The fused\-NLI pin
    `TestParallelNLIJointypeIdentity` is retired \(its last case now builds the decomposed family, pinned by
    `parallel\_lateral\_left\_probe\_identity\_test\.go`\); the old RIGHT\-link decline test asserts the new placement\.
  - Constant ON conjuncts still decline — ledger row 2026\-10\-27\.
  - Gates: units, spotcheck, acceptance 24 MATCH, sf025 PASS=99 \(61 ck\) \+ plans same=99, fire\-set PASS \(fires=none
    both\), TPC\-H census on tree 22/22, RegressSuite PASS \(same 81 mismatches, outputs identical\), Isolation PASS\.
  - Filed by M0147\-0052: `outerOnQualsOK` \(`internal/optimizer/joinsearchseam\.go`\) declines a LEFT/RIGHT link whose
    ON clause carries a preserved\-side\-only conjunct or a constant \(45 regress decline events\), so the statement is
    planned outside the PG\-shaped search\. PG keeps such a conjunct as a join clause of that outer join
    \(`distribute\_qual\_to\_rels`, `postgres/src/backend/optimizer/plan/initsplan\.c`: `is\_pushed\_down` false, evaluated
    in the join's restrictlist\), and its search builds the join\.
  - Witness: `TestParallelNLIJointypeIdentity/left` \(`o LEFT JOIN i ON i\.k = o\.k AND o\.id > 0`; PG elects a
    Nested Loop Left Join over the index probe\)\. Landing this unblocks M0147\-0051\.
  - First step: carry the preserved\-side conjunct on the outer link as a join qual the searched join evaluates
    \(not a residual Filter above the tree, not a scan restriction\), admit it in `outerOnQualsOK`, and check the
    witness plans as PG does; then census \+ regress\.
