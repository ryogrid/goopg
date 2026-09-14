# 04 — Forward plan

*What should happen next, and why the current method should change. This proposes
a real re-plan, not a tuning of the round cadence.*

---

# Part A — Summary

## The decision that must come first

**No further round can be scoped honestly until the goal's owner answers two
questions.** Both are recorded in the programme's own artefacts as escalated and
unresolved.

### Question 1 — the width blocker

TPC-H's two closest queries (Q9 at one category, Q4 at two) both block on
executor-side narrowing, every cheaper lever is measured and rejected, and the
enabling capability is **project-level declined** (02 §B1).

**Q1 is a binary: (a) or (b).** (c) is not a third answer to it — it is an
orthogonal *scheduling* decision that should be taken immediately either way.

| | option | consequence |
|---|---|---|
| **(a)** | Build executor-side narrowing | Infrastructure. No parity prediction on its first slice. Strong for Q4; **measurably weak for Q9** (§1.1). Also the prime suspect for the executor's 4.1× TPC-H gap (02 §B9). |
| **(b)** | Accept TPC-H parity is capped near 6–7/22 | Honest. Redirects effort at categories that do not depend on width. |
| **(c)** | **Split the goal — take now, orthogonally** | TPC-DS's dominant blockers (`join-order 89`, `parallelism 87`) do **not** gate on width, so TPC-DS proceeds under either answer. It does not decide Q1; it stops one corpus idling while Q1 is decided. |

**Recommendation: (c) now, and (a) if the owner will fund it** — specifically the
*projection-pushdown half only*, which is not blocked by the `minimize_datum`
decline. See Part B §2.

### Question 2 — should goopg reproduce PG's estimation errors?

Arrived at twice independently: R79 (goopg's ndistinct ≈1.17M vs PG's ≈347k
against a truth of ≈1.5M — *keep the superior statistics*) and R130 (Q9's actual
is **175**; goopg estimates **97**, PG estimates **60,125 = 344× high**).

The goal accepts slower plans. It says nothing about adopting PG's mistakes. This
is load-bearing for Q9, for Q4, and potentially for the whole join-order
programme, and **no route past Q9 can be scoped until it is answered**.

A concrete proposal is offered in Part B §1 so the owner has something to accept
or reject rather than an open question.

## The method changes

| # | change | replaces | why |
|---|---|---|---|
| **M1** | **Two tiers: recons and campaigns.** A *recon* is hours, one document, no implementation licence, no REPORT ceremony. A *campaign* is one capability across many slices with one pre-registered category prediction. | the uniform round | The two best late results were recons; the worst rounds were campaigns wearing a round's clothes (03 §P1). |
| **M2** | **Build a mechanical index over the 126 round directories** — per query and per mechanism — and make consulting it a scope gate. | a written warning | The warning was written *before* R130 and did not work (03 §P2). |
| **M3** | **Repair the instruments before any campaign**: K18 `$$`, capture arm-stamping, TPC-DS match=1/2, `plan-gate` re-baselining, epoch declaration. | ad-hoc workarounds | A campaign measured on decayed instruments will not be believable (03 §P4). |
| **M4** | **Change what a round is judged on**: retire "match count rises" as a success test (**keep the non-regression floor**); require category + `shape-delta` + a declared stats epoch + a decline census + the planning route taken. | match-count success tests | K50: the metric is structurally blind to estimates (03 §P3). |
| **M5** | **Strengthen the correctness channel** with a qual-placement census and a duplicate-sensitive values check. | checksum-only sweeps | Two wrong-rows bugs were found by parity work, not by the gates (01 §F20). |
| **M6** | **Retire debt on a schedule**: delete the resolved-to-delete flag, cap default-off arms at four in the charter, and close or delete each ten-plus-round ledger carry. | indefinite carry | 03 §P7. |

## The programme, sequenced

**Phase 0 — unblock (days, no parity prediction).** Owner answers Q1 and Q2.
Instruments repaired (M3). Index built (M2). Debt retired (M6). Charter updated
(M1, M4, M5).

**Phase 1 — TPC-DS, which needs no answer to Q1** (weeks). Two campaigns:
1. **Parallelism first**, because it is the one concrete, sequenced,
   evidence-backed campaign available: re-measure the failing set under the
   `GOOPG_GATHER_PATHS` flip, adjudicate what genuinely fails against PG, land the
   flip on the category metric, then the partial-Append producer (K43).
2. **The upper-planner ordering contest** (K12(B) / K24 slice 3) — the named lever
   for `aggregation-strategy` (69) and `sort-strategy` (76) — but **gated on a
   slice-0 scoping recon**, because the record calls it "named and unscoped",
   "unreachable by construction" (K96) and "a multi-round *executor* programme"
   (K97). It cannot be scheduled as a campaign until that recon sizes it.

**Phase 2 — TPC-H, gated on Q1's answer.**
- If **(a)**: projection pushdown as a campaign, first slice being *a hook point
  inside the join tree* with no parity prediction.
- If **(b)**: declare the cap in `TODO.md` and move TPC-H effort to Phase 1.

**Phase 3 — join-order costing**, gated on Q2's answer and on Phase 1 having
supplied a believable measurement base.

**Continuous, outside the phases:** the correctness items (02 §C1, §N1–N5), which
are real engine bugs that this workstream discovered and that no parity decision
should hold up.

---

# Part B — Detail

## 1. The two decisions, with proposed answers

### 1.1 The width blocker — why (c) and a bounded (a)

Recommend **(c) split the goal** immediately, because it costs nothing and is
strictly dominant: TPC-DS's two largest categories are `join-order` (89) and
`parallelism` (87), and neither depends on the width programme. R124 §4b did show
that width changes *move* TPC-DS join order and method — *"just not toward PG"* —
so width is not irrelevant there, but it is not the *gating* item the way it is on
TPC-H. Under (b) TPC-DS work is the whole programme; under (a) it runs in parallel
with the infrastructure build. Either way it should start.

On (a), the record supports a **bounded** version that the `minimize_datum` decline
does not cover. `minimize_datum/05-work-estimate.md` §1 distinguishes two changes
people conflate, and warns that confusing them is *"the single most likely way to
misprice this work"*:

| half | reach | approval |
|---|---|---|
| **projection pushdown** — narrow what scans emit inside a join tree | planner + create-plan; `Datum` unchanged | **not covered by the decline** |
| **packed retention format** (`PackedTuple`/`PackedSlot`) | ~70 non-test sites (design 04) | explicitly declined, no re-proposal path |
| a `Datum` re-layout below 48 B | ~3,090 non-test sites | declined by take3 13 §10; nobody proposes it |

Note the proposal **does not shrink `Datum`** — `minimize_datum/README.md` is
explicit that it *"stays exactly 48 bytes and stays the working format"*; what
changes is its role, from storage format × row count to a one-row working buffer.
So "the `DatumBytes` half" is the wrong name for it, and using that name is the
mispricing `05-work-estimate.md` §1 warns about.

So the proposal to put to the owner is narrower than "approve minimize_datum":
**approve projection pushdown as a standalone campaign**, and treat the packed
retention format as a separate decision taken later on that campaign's measured
residue.

Two facts make this defensible rather than speculative:

- K67 already measured the residue: even narrowed to one column goopg is 72 B/row
  → 103 MB and **still spills** at `work_mem=64MB`. So projection pushdown alone is
  **necessary but very likely not sufficient**, and the campaign must pre-register
  that its first slice has *no parity prediction*.
- R70 measured the same thing from the other side on Q9: a 6-column projection
  floor gives +78,344 margin at NBatch 1, but degrades to NBatch 2 at +10% rows —
  *inside plausible estimate noise*. So the honest success criterion for slice 1 is
  **"a hook point exists and scans emit narrow rows"**, not "Q9 flips."
- **And Q9's case is measurably weaker than Q4's.**
  `internal/optimizer/entrywidth.go:38-48` carries a permanent measured comment
  headed *"WHAT THIS DOES NOT BUY"*: correcting the entry width does **not** change
  Q9's batch count, because `nbatch` is 4 at entry 112..194 alike, drops to 2 only
  in the narrow 96..111 window, and **returns to 4 below 96** as the bucket array
  doubles to 100.7 MB. *"The lever on this witness is MapSlotBytes, not the
  entry."* Commit `2e15b8ca3` adds that a packed retention format would make Q9's
  batching **worse** — and R129 measured the named lever, `MapSlotBytes` 48→96,
  **parity-inert**. So this campaign should be argued on **Q4 and on the executor
  gap (02 §B9)**, not on Q9. Anyone funding it on Q9's account is funding against
  a committed measurement.
- **The bundle's own review names two further blockers** beyond the missing
  licence, and an owner weighing (a) should see them: *"The premise was modelled,
  and the measured answer is different — and smaller"*, and *"Sequencing violates
  take3 13 §8.2."*

And the structural blocker is precisely identified, which makes slice 1 scopeable:
*inside a join tree there is no `*Project` above the scan at all* — the join reads
the scan directly. `internal/optimizer/narrowoutput.go` already narrows the build
side and upper nodes (`GOOPG_NARROW_BUILD`, `NARROW_UPPER`, `NARROW_UPPER_SORT`,
all default-ON), so the machinery for *applying* a narrowing exists; what is
missing is a place to attach one at the leaf. Note also that
`goopg_optimizer_no_attr_needed_no_ios_path` records that `attr_needed` is **not**
the blocker (the promotion is schema-preserving) — two earlier diagnoses said it
was and both were wrong. Slice 1 must not re-derive that.

Cheaper adjacent wins the record already prices, which should be taken regardless:
deleting the duplicate build map (`lazyHash` **and** `lazyIntHash` both maintained
→ ~2× peak build memory, *"one commit"*, `minimize_datum/05` §1.5), and correcting
`MapSlotBytes` 48 → 96 (bucket heap 586.7 → 286.0 MB, −34.5% per-worker peak) —
which R129 proved **parity-inert**, so it belongs to `minimize_datum`, not here,
and its preserved patch is stale (02 §N28).

### 1.2 PG's estimation errors — a proposed answer

The record contains enough to propose a rule rather than leave the question open:

> **goopg keeps the more accurate estimate. Where PG's plan is reachable only by
> reproducing a PG estimation error, the query is recorded as
> `PARITY-BLOCKED-BY-ORACLE-ERROR` and excluded from the match target, with the
> measured evidence attached.**

Rationale drawn from the programme's own stated goal: parity is wanted because it
is *evidence that goopg reached PG's decision by PG's reasoning*. A plan matched by
reproducing a 344× estimation error is not that evidence — it is the plan-forcing
the goal forbids, arrived at through the statistics layer instead of the planner.
K92 already applies exactly this reasoning to a *label* (stamping `Parallel Hash`
on goopg's leader-prebuild would be a misdescription and therefore forbidden); the
same principle applied to an *estimate* gives the rule above.

This costs the programme candidly: Q9 likely leaves the TPC-H target, and Q4's
route may too (its needed 0.2317 selectivity derives from PG's ndistinct
undercount). **That is the point of asking** — it converts an unmeasurable
aspiration into a stated, auditable scope. If the owner rejects the rule and
prefers bit-parity with PG including its errors, that is a legitimate answer and it
makes Q9 scopeable again; it simply must be *decided*, because the two answers
lead to opposite work.

## 2. The operating model

### 2.1 Two tiers

**Recon** — the default, and what most rounds should have been.

- Trigger: a hypothesis that could be eliminated by one measurement or one read.
- Licence: measurement and temporary instrumentation only. **No production change.**
- Output: **one document** in a `recon/` subdirectory, named for the hypothesis,
  stating: the question, the cheapest decisive check, the result, and the verdict
  (CONFIRMED / REFUTED / UNOBSERVABLE), plus what it rules out for future scopes.
- Ceremony: none. No scope review, no REPORT, no second commit. One commit.
- Expected duration: hours.

The evidence this is right: the step-(d) FK recon closed an entire chain in one
document, and the R129 bucket recon settled two questions with a one-line probe.
Both were already outside the cadence. Meanwhile ~12 of R100–R130 carried full
ceremony to produce the same class of output.

**Campaign** — rare, for building a capability.

- Trigger: an approved capability (Phase 1 and 2 items below).
- Structure: a scope document with a **pre-registered category prediction**
  (including the negative predictions), multiple slices, and an **explicit
  "no parity prediction on slice 1"** clause where the capability is
  infrastructure.
- Review: adversarial review of the scope, and of the implementation. Keep this —
  it is the part that works (03 Part A).
- Output: one REPORT per slice, and a campaign close-out that scores the
  pre-registered prediction.

### 2.2 Make retrieval mechanical (M2)

The failure mode is documented and the exhortation failed (03 §P2). Build a small
index and make it a gate:

- `METHODOLOGY3/INDEX-by-query.md` — for each of the 22 TPC-H and 99 TPC-DS
  queries, the rounds that touched it and their one-line verdict. Q4 alone spans
  R71, R72, R73, R74, R75, R76, R77, R78, R79, R81, R127; Q9 spans R53, R68, R69,
  R70, R77, R125, R126, R130; Q96 spans R86–R119.
- `METHODOLOGY3/INDEX-by-mechanism.md` — for each mechanism (widths, semi
  selectivity, ndistinct, partial paths, bucket charge, FK evidence, fuzz
  tie-break, …), the rounds and the standing verdict.
- **Scope gate**: a campaign or recon scope must cite the index rows for its target
  query and its target mechanism, or state that none exist. This is a mechanical,
  checkable requirement — unlike "grep the directory first."

This is cheap to build (the material is already assembled in
`01-what-we-learned.md` and `02-open-problems.md`) and it directly prevents the
two most expensive failures of the last month.

### 2.3 Repair the instruments (M3)

Before any campaign, because a campaign measured on these cannot be believed:

1. **Fix K18 at source** — remove `$$` from `capture-tpcds.sh`'s temp filename
   (fixed once in 2026-09-08 and regressed). Add a test that two consecutive
   captures of an unchanged binary diff empty. This trap has produced false
   readings in four rounds.
2. **Machine-stamp every capture** with binary path, inode, serving-PID
   `/proc/<pid>/exe` verification, flag arm, GUCs, and stats epoch. R122 §10's
   *"rests entirely on filename convention"* must become false.
3. **Reconcile TPC-DS match=1 vs match=2** — one recon, aimed at the actual
   difference: both figures come from the same capture-script family, and they
   differ by **reference and session GUCs** (live PG `:65438` vs the committed
   `bench/tpcds/plans-pg` fixture), not by tool. Note K9 binds the TPC-H sibling
   fixture as *not* a parity target, and the owner waiver for re-capturing it is
   still pending — the TPC-DS fixture's standing needs the same ruling.
4. **Re-baseline `plan-gate`** as its own commit, with the 20 diverging queries
   adjudicated or explicitly carried, so the gate becomes a live signal again
   rather than a standing opt-out.
5. **Declare the stats epoch** in every A/B artefact, and make re-taking the OFF
   baseline after a values sweep a checked step rather than a remembered rule.
   Drift measured at 1.31× on Q9 exceeds most effects being claimed.
6. **Give each lane a private clone and a private port.** Shared-resource
   contention on `:65433` is the stated reason for most deferred gates, and it is
   an infrastructure problem with an infrastructure fix.

### 2.4 Change what is judged (M4)

Retire from scope templates: any clause of the form **"match count rises"**. It is
known-mis-specified (`ROADMAP` §3, `handover-090910` §4) and still appears.

**Keep the non-regression floor.** R120's P3 ("match ≥ 6, none of the six flips")
and R128's P2 ("match ≥ 2") are floors, not rises, and R120's **correctly FAILED**
— catching Q10's loss. That is the one match-count clause the record shows
earning its place, and M4 must not delete it.

Require in every report:

- **category movement**, reported as `blocked-excluding-matches` alongside the raw
  tool line (`METHODOLOGY2` §3 asked for this and it is not done);
- **`shape-delta.sh` counts** alongside the categories — a round with
  `shape-changed = 0` moved no plan at all, and one with shape changes and no
  category movement moved plans **sideways**; conflating the two produced a wrong
  conclusion once already;
- **a declared stats epoch** for both arms;
- **a seam-decline census by class, not by total** (R40's lesson: a class can be
  *converted* rather than removed), at a stated timeout (R29's lesson: censuses are
  only comparable at equal timeouts);
- **which planning route the query took** — path search or legacy/prebuilt. This is
  new, and it exists because R115 showed a whole class of experiment was measuring
  the wrong route (02 §B11).

### 2.5 Strengthen the correctness channel (M5)

Two additions, both justified by bugs that actually shipped:

1. **A qual-placement census** as a gate artefact: the count of `Filter:` /
   `Index Cond:` lines per query, diffed between arms. R56's Q78 lost three
   filters with checksums passing; a line census would have caught it instantly.
2. **A duplicate-sensitive values check** — the current digest compares row counts
   and checksums, and R83's Limit-below-Unique bug was masked because `78 < 100`.
   Either widen the corpus or add a synthetic case per known-fragile shape (the
   `ParamRef` LIMIT + DISTINCT case, 02 §C1, is still live).

## 3. The sequenced programme

### Phase 0 — unblock (days)

| item | owner | output |
|---|---|---|
| Answer Q1 (width) and Q2 (PG's errors) | goal owner | a decision recorded in `TODO.md` |
| Instruments repaired (M3 items 1–6) | one recon + one commit each | stamped, reproducible captures |
| Indexes built (M2) | one pass over this directory | two INDEX files + a scope gate |
| Debt retired (M6) | one commit | `GOOPG_HASHAGG_WIDTH_CURRENCY` deleted; four-arm cap in the charter; ten-plus-round ledger carries each closed or deleted |
| Charter updated | — | two tiers, new report requirements, correctness additions |

No parity prediction. Phase 0 is explicitly overhead reduction, and it is the
highest-return work available because it is what makes every later measurement
believable.

### Phase 1 — TPC-DS (weeks), independent of Q1

**Campaign A — parallelism.** Run this first. It is the only Phase-1 item that is
already concrete, sequenced and evidence-backed, and it is the item whose progress
makes the other one measurable.

Sequence, in the order the record supports:

1. **Re-measure the failing set under the flip, then adjudicate what actually
   fails** — a recon, not a campaign. Do **not** inherit the R43-era list
   (`TestSplitEqualityForHashMultiKey/searched_enumerator`,
   `TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`,
   `TestOwnedBuildPoisonPrebuiltBoundary`): it is ~87 rounds stale and at least
   `TestSlice3LiveQ9ShapeDerivation` was re-baselined by R51 nine rounds later.
   The ledger's own adjacent note binds — *"re-measure before relying on any prior
   round's figures."* For whatever genuinely fails, R14's precedent applies: ask
   the oracle, the test can be wrong.
2. **Land the flip on the category metric.** R43 rev 3 measured TPC-H
   `parallelism` 18→16 with no new match; K80 established it needs no new
   parallel-hash work. Pre-register **no match flip**.
3. **Partial-Append producer (K43)** — PG uses Parallel Append in six TPC-DS
   queries; only Q5 and Q76 currently miss.
4. **Explicitly deferred**: Q14's third category (K92), which needs PG's real
   partial-inner execution model and is *"NOT cheap"*; and the non-planner floor
   (K14/K15 heap density, K41's unexplained dimension-table divergence), which no
   planner change can close.

Do **not** re-open the forced-order Q96 line without first re-establishing which
planning route the query takes (02 §B11) — and note B5's terminal state: the
inputs PG's final hash cost needs are **unobservable** and the only oracle route
crashed PG.


**Campaign B — the upper-planner ordering contest (K12(B) / K24 slice 3).**

**Slice 0 is a scoping recon, and the campaign does not start without it.** The
record does not currently support scheduling this as a campaign: 02 B4 calls it
*"named and unscoped"*; K96 says the shape PG picks is *"unreachable by
construction"*; K97 records R45's fix as *"rejected as architecturally
impossible"* and names the real item as PG's
`AGGSPLIT_INITIAL_SERIAL`/`AGGSPLIT_FINAL_DESERIAL` — *"a multi-round **executor**
programme"* of unstated size. Slice 0 must produce that size, a slice list, and an
entry gate. Until it does, **this is the weakest link in option (c)'s argument**:
(c) claims TPC-DS does not depend on the width programme, which is true — but this
campaign depends on a *different* executor programme that nobody has priced.

The named lever for `aggregation-strategy` (TPC-DS 69, TPC-H 10) and, through the
same mechanism, `sort-strategy` (76 / 9). Per K24 it is the largest single item in
the workstream, and per K24 it must **not** be split from the `PartialPathlist`
work — both need the upper planner to receive the join rel's *paths* rather than a
finished `Node`.

Known constraints the campaign must start from, not rediscover:
- K96: `Finalize GroupAggregate` is unreachable by construction —
  `rebuildWithGather` has mutually exclusive arms and `splitAggregate` hardcodes
  `NewGather` while copying the original strategy.
- K97: R45's fix was **rejected as architecturally impossible** — goopg's Partial
  aggregate emits **zero rows**, so a Sort between Partial and Gather would sort an
  empty stream. The real item is PG's
  `AGGSPLIT_INITIAL_SERIAL`/`AGGSPLIT_FINAL_DESERIAL`, i.e. **an executor
  programme**, and it should be scoped as one.
- K97's trap: `aggregateOp` already sorts for determinism — **do not mistake
  incidental ordering for a pathkey.**
- F15/R120 §5: PG's GroupAggregate preference is **pathkey-driven, not
  spill-driven**, so the campaign's success criterion is ordering delivery, not
  spill accounting. R120 already proved the spill route is net-negative.

### Phase 2 — TPC-H, gated on Q1

**If (a) — projection pushdown campaign.**
- Slice 1: **a hook point inside the join tree**, with the pre-registered
  prediction *"no parity movement; the pass fires N > 0 times."* The known
  blocker is that the join reads the scan directly, so there is no `*Project` to
  attach to. `attr_needed` is **not** the blocker.
- Slice 2: reuse `narrowoutput.go`'s existing narrowing at the new hook.
- Slice 3: measure the residue against K67's 72 B/row floor, and use that
  measurement — not an argument — as the input to the packed-retention decision.
- Gate every slice on the qual-placement census proposed in §2.5 / M5 below,
  because narrowing a scan output is exactly the class of change that silently
  drops a filter (02 §C2).

**If (b) — declare the cap.** Record in `TODO.md` that TPC-H parity is capped near
6–7/22 pending an executor-narrowing capability, with `FRONTIER.md` as the
evidence, and move TPC-H effort into Phase 1. This is not a defeat; it is the
difference between a measured limit and an unexplained plateau.

### Phase 3 — join-order costing, gated on Q2

Only after Phase 1 has supplied a believable measurement base, and only if Q2 is
answered. The entry conditions are specific:

- **The candidate half is already open** and needs no further unblocking:
  `joinsearchseam.go:461` calls `inferTransitiveEqualities` unconditionally, and
  R51 adjudicated both Slice-3 tests against PG. (K26 §9.3's "constants-only"
  description is a pre-R51 snapshot — do not schedule against it.) What remains is
  the costing half alone.
- Every attribution round has ended at **pricing** (R53, R68, R96) and every
  pricing round has ended **blocked**. So Phase 3's first act should be a recon
  that asks whether the blockage is still the same one, rather than a campaign
  that assumes it.
- Q9's route depends entirely on Q2. If the proposed rule in §1.2 is adopted, Q9
  leaves the target and Phase 3 should be sized against the *other* 13 TPC-H and
  89 TPC-DS join-order queries.

### Continuous — engine correctness, not gated on anything

These are real bugs this workstream found, and no parity decision should hold them
up:

- `ALTER TABLE … DROP CONSTRAINT` on an FK silently no-ops, and `HasPrimaryKey`
  has the same shape (02 §N1, §N2) — made worse in effect by R126.
- `pg_constraint` returns 0 rows of any contype after a restart (§N4).
- `PhysicalTypeIsVarlena` has no `IsArray` arm — latent for ordinary user `int4[]`
  (§N5).
- `ParamRef` LIMIT + DISTINCT returns wrong rows (§C1).
- No in-process test crosses a DATABASE boundary (§N3) — which is *why* two per-DB
  defects were found in two consecutive rounds, and is the highest-leverage fix of
  the five.

## 4. What success looks like under this plan

Deliberately different from the current definition, because the current one has
been flat for 29 rounds.

| horizon | signal |
|---|---|
| **Phase 0 done** | Two owner decisions recorded. Every capture machine-stamped. `plan-gate` a live signal. Two INDEX files gating scopes. The four-arm flag cap in the charter with the resolved-to-delete arm deleted. |
| **Phase 1 in progress** | TPC-DS `parallelism` falling as partial paths *win* rather than as Gathers are stamped; and, once Campaign B's slice-0 recon has priced it, `aggregation-strategy` and `sort-strategy` falling **together** (they are one mechanism). **Match counts are not the signal and should not be quoted as one** — except as a non-regression floor. |
| **Phase 2 (a) slice 1** | A hook point exists and fires; scans inside join trees emit narrow rows; the K67 residue is *measured* rather than argued. |
| **Phase 2 (b)** | The cap is stated with its evidence, and TPC-H stops consuming rounds that cannot move. |
| **Phase 3 entry** | A recon confirms the pricing blockage is unchanged, with the seam question resolved by Phase 1. |

And one meta-signal worth tracking explicitly, because it is the thing this
retrospective is really about: **the ratio of recons to campaigns should be high,
and the share of commits that touch no code should fall well below the current
~three quarters.**
