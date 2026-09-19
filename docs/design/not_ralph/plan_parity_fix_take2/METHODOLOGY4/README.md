# METHODOLOGY4 — stocktake at the end of the M0137–M0143 era

*Written 2026-09-19 on branch `plan-parity-with-pg-take2-ralph2`. This is **not
a round and not a milestone**. It is the second programme-level stocktake:
what the milestone era (Ralph-loop execution of the M0137–M0143 programme,
2026-09-14 → 2026-09-19) learned, what remains open, and how the work should
change next.*

*Relationship to the existing corpus: `METHODOLOGY.md` still owns the
measurement pipeline and the server traps (now partially superseded by the
M0137 harness work it annotates); `METHODOLOGY2.md` owns the R50 recount;
`METHODOLOGY3/` owns the 9/14 stocktake that produced the milestones;
`TODO.md` remains the authoritative round ledger; `.ralph/fix_plan.md` is the
authoritative task state for M0137–M0143. This directory adds the missing
layer: **what the milestone era itself demonstrated**, read across all seven
milestones rather than inside any one of them.*

**No measurement was run for this document set.** Every number is quoted from
an existing artefact — commit messages, `fix_plan.md` task entries,
`.ralph/deferral_ledger.md` rows, design docs under `docs/design/0100-0149/`,
or the committed capture files — with its provenance and caveat.

---

## The files

| file | what it answers |
|---|---|
| [`01-what-we-learned.md`](01-what-we-learned.md) | What the M0137–M0143 era **proved** — durable findings about goopg's planner vs PG 18.3, about the instruments, and about which levers do and do not move the metric. |
| [`02-open-problems.md`](02-open-problems.md) | What is still **unfixed** — strategic blockers, instrument debts, open tasks per milestone, and engine-correctness residuals. Fixed items are excluded by construction. |
| [`03-forward-plan.md`](03-forward-plan.md) | The proposed next operating model — including two new measurement instruments (a first-divergence census and a cost-margin census) and an instrumented-PG route to end the "attribution ends at pricing" stall. |

Each file leads with a summary section and then a detail section. Read the
four summaries (about ten minutes) before any detail.

---

## Executive summary

### 1. The score, then and now

| corpus | 2026-09-14 (METHODOLOGY3) | 2026-09-19 (now) | provenance |
|---|---|---|---|
| TPC-H serial (22) | match **6** | match **8** (Q3, Q13 added) | `fix_plan.md` M0141-S2a-fix1 / S2b-13; floor re-measured by P0-E7 (HEAD `a0e741a68`), unchanged across 72 commits |
| TPC-H parallel (22) | *unmeasured* (serial-protocol artefact, B13) | match **2/22** → **3/22** | M0137-0017 first parallel-mode reference (2/22); P0-E7 re-measured 3/22, categories byte-identical |
| TPC-DS SF0.25 (99) | match **2** (Q9, Q41) | match **2** | floor holds; M0137-0004 reconciled match=2 vs live PG `:65438` as canonical |
| TPC-DS **SF1** (99) | *never captured* | match **1/99** (Q41 only; Q9 is `SHAPE-DIFF [scan-type]` at SF1) | P0-E7 first-ever SF1 capture; P0-H12 proved the Q9 divergence pre-existing |

Category movement in the era (CATEGORIES-EXCL-MATCH, recorded deltas):

- **TPC-DS aggregation-strategy 71→44→43, sort-strategy 77→69→67** — the two
  largest measured drops of the entire programme (M0141-S2b-13 `COSTS_EQUAL`
  restore, then S2b-15 `cost_gather` rows fix). Caveat: the second delta was
  measured **on a staged tree** — S2b-15's impl is staged uncommitted and the
  task remains open.
- **TPC-H aggregation-strategy 10→3**, sort-strategy 9→~7, with small adverse
  drift inside the ±3 noise band (join-order +2, join-method +1, scan-type +1).
- **join-order — the largest category on both corpora — did not convert**:
  the count drifts inside the noise band (TPC-H 14 at the last census, moving
  14→12→13→14 across the era; TPC-DS ~89→88) while matches stay flat, despite
  ~80 M0142 tasks.

### 2. The verdict in one paragraph

The milestone era **fixed the harness and paid down the measurement debt** —
that part of the plan worked almost completely. It also landed the two biggest
single movers of the programme (agg-input width reaching costing, and the
`add_path` `COSTS_EQUAL` port). But the era's dominant output was
**attribution**, not conversion: machinery built to PG's specification
repeatedly measures *admitted but never wins on cost* (Parallel Append 0/99
corpus occurrences, Incremental Sort 0/99, three absorption arms
byte-identical; M0142-0005a's Memoize driver arms reached byte-exact PG
shape on Q34/Q73 but still did not convert to MATCH). The binding constraint
is no longer "a
missing capability the owner must approve" — it is that **goopg's cost
model and its input path-candidates diverge from PG's upstream of every
mechanism we build**, so each new mechanism loses before it can fire.

### 3. The three load-bearing facts of the era

1. **Category movement is real but does not convert to matches.** TPC-DS
   `aggregation-strategy` fell by 28 instances (the largest single movement
   ever measured) and the match count stayed at 2. The conjunction property
   (METHODOLOGY §2) still binds: every remaining query needs *all* of its
   categories closed at once, and `join-order`/`parallelism` — the two largest
   categories — are the two least moved.
2. **"Admitted but never wins" is the era's signature failure shape.** M0140
   built the complete Parallel-Append pipeline (branch rels → costed producer
   → upper-rel Gather wiring → executor claim sets → all four branch-driving
   kinds → mixed arm) and every slice measured `PLAN-SHAPE 99/99 identical`.
   M0141 built Incremental Sort end-to-end and the corpus reaches it 0 times —
   the *input* candidates are pricier because the parallel/`Gather Merge`
   shapes upstream diverge. Mechanism completeness without input-path parity
   yields zero.
3. **Statistics are now PG-faithful — and it moved the metric by zero.**
   M0138 ported `acquire_sample_rows`/reservoir/MCV machinery to the letter;
   `n_distinct` sign/order agreement is near-total (TPC-H 60/61, TPC-DS
   120/120). The Q9 lesson was subtler than "stats don't matter": the
   *sampler port* moved goopg's estimate toward the truth (97→146, actual
   175) but not toward PG's 344×-high 60,125 — yet M0142-0003i's later
   FK-metadata fix moved the same estimate to ~117,313 vs PG's 75,650, i.e.
   most of the remaining gap was **catalog-metadata-driven**, not
   estimator-code-driven. Reproducing PG's estimate code faithfully did not
   reproduce PG's estimates, because the catalog inputs and the join
   topology both feed them first.

### 4. What the era demonstrated about method

- **Recon-first decomposition works and is now the norm** — M0142's plumbing
  chain (0008a→c22) shows both ends: disciplined per-slice inertness proofs
  landed a cross-cutting feature safely, *and* ~20 slices of inert plumbing
  still ended at "0/96 reachability" before the real unblock was identified
  (an unfiled IN-unnesting `.SJInfo` producer). The lineage budget (S4)
  eventually forces escalation, but the cost before that point is real.
- **The instruments are now trustworthy** — the two pre-conditions METHODOLOGY3
  demanded are met: captures are machine-stamped, epochs are checkable,
  plan-gate has a live baseline (re-staling again — M0137-0022), the INDEX
  files exist, and every gate stamps the staged tree hash.
- **The corpus itself is now known-imperfect at the data layer**: the shared
  `:65433` cluster lacks the canonical 8 FKs (fix landed in
  `build_schema_goopg.sh`, awaiting an owner reload); goopg's `lineitem`
  carries 6,001,255 rows vs PG's 5,998,835 vs canonical dbgen 6,001,215 —
  *both* clusters diverge from canonical; and bpchar trimmed-vs-padded
  storage measurably changes `relpages` (K41 solved, fix owner-gated).

### 5. What should happen next — the one-paragraph version

The next leverage is **not another mechanism build**. It is a measurement
upgrade aimed at the two questions the current tooling cannot answer:
*where exactly does each divergent plan first leave PG's tree*, and *how large
is the cost margin at that point*. Two new instruments — a **first-divergence
census** (per-query tree alignment, replacing the 9-category headline with
mutually-exclusive, actionable counts) and a **margin census** (pricing PG's
chosen shape inside goopg's own cost model to split thin-margin tie-breaks
from thick-margin structural gaps) — plus an **instrumented PG 18.3 build**
(`OPTIMIZER_DEBUG` or a patch-GUC trace build on a private clone) that dumps
per-rel candidate lists and `add_path` verdicts, converting "attribution ends
at pricing" into a diffable artefact. Detail in
[`03-forward-plan.md`](03-forward-plan.md).

---

## Provenance and caveats

- Task counts and statuses were inventoried 2026-09-19/20 from
  `.ralph/fix_plan.md` at HEAD; the milestone docs' own `Status:` header lines
  are dated 2026-09-17 and are stale in the direction of undercounting done
  work (a known hygiene gap — P0-H11 owns the doc-status cleanup class).
- The TPC-H "match 8" headline is the **serial-protocol** number; the first
  parallel-mode measurement (M0137-0017) read **2/22** and P0-E7's
  re-measure reads **3/22** — not floor-comparable, a different, harsher
  instrument.
- TPC-DS `match=2` is the SF0.25-corpus number against live PG `:65438`;
  the first full-SF1 capture reads `match=1/99` and the floor is defined on
  SF0.25 only (P0-H12).
- Every figure quoted with a `→` cites the task entry or ledger row that
  measured it; where two figures conflict (e.g. SF0.25 vs SF1 TPC-DS), both
  are quoted with their provenance rather than reconciled here.
