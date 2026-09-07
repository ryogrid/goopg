# TODO_ALL — unified planner + executor performance plan

A single execution checklist for closing the whole planner + executor
performance gap against vanilla PostgreSQL 18.3. It consolidates the
remaining work from four sources and records their dependencies and
blockers in one place:

- `../planner_refactor_take2/TODO.md` (take2 — history; authoritative for
  what already landed, not for what remains)
- `../planner_refactor_take3/TODO.md` (take3 planner — fresh plan over the
  remainder; authoritative for planner sequencing)
- `../planner_refactor_take3/TODO_EXECUTOR.md` (take3 executor bundle —
  authoritative for executor sequencing)
- `TODO.md` in this directory (minimize_datum — the packed-retention
  bundle; blocked items marked inline)

## Blocker inversion — the tracking instrument for the `[!]` rows (2026-09-07)

**Policy set by the owner 2026-09-07: a `[!]` row is NOT a terminal state.**
"Blocked" names a blocker, and a blocker is itself a work item. The only
legitimate terminal states are **done**, or **out of scope on verified
grounds** (no performance gain / infeasible in goopg's design / severe
maintainability loss). Writing an excellent blocked row and moving on had
become a way to close items without doing them — 23 accumulated that way.

Inverting the ledger: those 23 rows collapse to **7 root blockers**. Work the
blocker, not the bookkeeping — and **re-test whether each blocker is still
true before treating it as one** (B-17b below was blocked on something that
had already completed).

| root blocker | holds | status 2026-09-07 |
| :-- | :-- | :-- |
| **spill-cost calibration** | B-13, B-15, E-16 | DESIGN ALREADY LANDED and unimplemented (`docs/design/planner-spill-cost-calibration/DESIGN.md`, 4 gated cuts). IN PROGRESS. |
| **C-15 grouping paths** | B-17b | **STALE — C-15 landed `[x]`; B-17b is unblocked.** IN PROGRESS. |
| **the seam drops `Pathkeys`** | C-07 | Named and measured: `newPrebuiltPath` leaves `Pathkeys` nil, so the Sort arm is the only reachable arm. Second blocker: producer unreachable at `nrels < 2`. IN PROGRESS. |
| **C-19g upper-rel-resident half** | C-19h, D-05 | C-19g replaced the split VERDICT but not the CONSTRUCTION. IN PROGRESS (owns the `plan_snapshots/` pin). |
| **B-01c *applying* half** | D-06, E-01 | B-01c's compute half is `[x]`; the applying half needs two things that do not exist — a narrowing-aware upper rewriter and a key-preservation gate. NOT STARTED. |
| **"the flip moves plans"** | C-06, C-20c, C-20d, C-20e, C-20f, C-20g | Not a missing prerequisite but a finding. The attackable question behind it is the cost defect that makes the moved plan win — e.g. C-06: why does the search win a Merge Left Join at 5x the cost? NOT STARTED. |
| **the D-04 stopping rule / D chain** | D-07, D-08, D-10, D-11, E-02, E-12, E-14, B-17e | Evidence-based stops and chained conversions. Needs per-item re-test of whether the stop still binds. NOT STARTED. |

**One checkbox ≈ one commit.** An item that would move two planner inputs
(or two executor inputs) at once is split before it is started (take3 08
§1 P5 / 13 §1 EX-P3).

Legend: `[ ]` not started · `[~]` in progress · `[x]` done · `[-]` dropped
(with a reason and a ledger row) · `[!]` blocked (blocker named inline).

When closing an item, rewrite its line as:

```
- [x] <ID> <title> — <commit>; gates: <list>; artifacts: <paths>
```

Each item names its design pointer and its gate in 2–4 lines.
Phase-closure verdict files go under
`analysis/planner-refactor-take3/<phase>-<date>/README.md` (planner) or
`analysis/executor-refactor/<phase>-<date>/README.md` (executor),
carrying the measurement-protocol header, the before/after pin + timing +
alloc table, and an explicit statement of anything that got worse.

Scope decision (recorded): this file is the **full planner + executor
mega-plan**, not an MD-centric pointer list. Dependent counterpart tasks
are consumed *while working* the item that needs them — each entry below
says which dependency it digests inline.

---

## 0. Status reconciliation (take2 vs take3 vs the tree)

Take2's TODO kept being updated after take3's TODO was written, so the two
disagree on several Phase-0 items. Every row below was verified against the
tree (`git log -S` / `--grep`) on 2026-09-04; the **tree verdict** governs.

| item | take2 state | take3 state | tree verdict + evidence |
|---|---|---|---|
| P0-08 re-pin plan baseline | `[x]` closed | `[ ]` remaining | **DONE** — `f4a5e7e75` re-pinned to `take2-p0-20260903`. Take3 entry is stale; do not redo. |
| P0-13 collapse positive control | `[x]` closed | `[ ]` remaining | **DONE** — `82dd30bbc` defaulted `GOOPG_PGSHAPED_COLLAPSE` on. Take3 entry is stale; do not redo. |
| P0-04 EXPLAIN naming | `[x]` closed (alias-drop fix; numbering left open) | `[ ]` remaining (suffix numbering) | **Consistent** — renderer fix landed (`P0-04` close entry), suffix numbering vs `select_rtable_names` still open. Work the numbering only. |
| P0-05/06/07 plan-parity instrument | `[ ]` open | `[ ]` remaining | **Consistent** — no capture commits, no `bench/tpch/plans-pg/` or `bench/tpcds/plans-pg/` fixtures exist. Still open. |
| P4-01 PathTarget/projection | take2: rev-10 steps 1–5 landed, default ON (`00d56df90`) | take3: slice 1 landed, slices 2/3 per DESIGN.md | **Superseded by the tree** — slices 1 (`588aa5fb5`), 2 (`8458b7552`) and 3 (`1d804ae02`) all landed; build-side Project narrowing is live (Q9 witness `Batches:` **2→1**, widths 1096→776/896→640/896→736/710→582 per `docs/design/planner-p4-01-target/DESIGN.md:61-71`; the 8-batch figure is the pre-retake baseline). Remaining: deferred slices (a) merge/NL input policy, (b) scan-node application, (c) upper targets (DESIGN.md:105-110) — owned by B-01a/b/c below. |
| EX0 instruments | n/a (take3 work) | all `[x]`, phase closed | **Confirmed** — `6ace9567e`/`503f12cf7`/`b6254f911` (EX0-01/02), `eaff87ee0`/`a7e2f86f6`/`eedc924de` (EX0-03/03b/03c), `86bf7473f` (EX0-04), `14f8d8f1a` (EX0-05), `e8c7400f9` (EX0-06). MD blocker (3) is discharged. |
| EX1 narrowing | n/a | EX1-01/02/02b `[x]`, EX1-03 dropped-then-resumed, EX1-04 cut 1 `[x]` | **Confirmed** — incl. `230a32bd0` (EX1-03 drop), `134324df6` (EX1-04 Cut 0 + unblock review). **Update 2026-09-05:** EX1-03 resumed and landed as a B-14 dependency (`5983b25c5`: DetoastRowBound/DetoastAttr/updateSetRefCols/detoastUpdateEvalRow + counter + json pointer decode arm + per-type contract tests) — B-14 `975ddc059` had committed a `DetoastRowBound` caller without its definition, so HEAD's executor package did not build. Drop-reversal justified by red-tree restoration + reviewed design (`583ff6148`, amended with the codec arm). |
| EX2 clone elimination | n/a | audit + 02a/02c `[x]`, 02b dropped, 03 measure-only | **Confirmed** — incl. `efbca66e4` (EX2-01 audit: 45 sites), `fe2808bc9` (02b drop). MD-04 consumes the EX2-01 audit; no second audit. |
| EX3 spill/sort/hash | n/a | 01/02/03-step1/05-cutA `[x]`, step2 blocked, cutB dropped | **Confirmed** — incl. `53e41fdbf` (EX3-01), `7c586eaf7` (step-2 blocked + resume patch), `a9cf79c81` (cut-B drop). **Plus post-reconciliation landings: EX3-02 Cut 2 (`68ccd68c3`, stratum-D chunk views, unit 2.002→0.005) and EX3-05 Cut A (`fd2e2ae7a`, wantCTIDs gate) with full gates; E-12's "Cut 2 blocked" premise is stale (corrected inline). |

---

## 1. Dependency graph (binding order)

```
P0-04/05/06/07 (parity instrument) ──► everything that gates on PP roll-ups
P4-01 slices 1–3 [x] ──► B-01a/b/c (deferred slices) ──► EX1 sort half ──► EX3 geometry
P4-01 slices 1–3 [x] ──► E-14 (EX1 build-half redesign) ──► D-05/D-06 geometry
P4-01 slices 1–3 [x] ──► EX1 build half (EX1-01/02/02b [x], EX1-04 cut 1 [x])
P4-01 slices 1–3 [x] ──► B-12d/e/f ──► B-13 (P2-02b)
P3-01 ──► P3-03/P3-04 ──► P1-18 (a jointype switch before P3-04 is dead code)
B-16 (MCV-frequency half) + EX1 exit ──► E-02 (EX3-06 skew sizing)
E-07 (Gather slab parity) ──► E-08 (Gather-arm compilation)
C-14 (Incremental Sort) [-] dropped 2026-09-07 ──► E-03 [-] resolved with it; E-15 [x] landed and inert (the contract a future C-14 would build against)
EX2-01 audit [x] ──► D-05 (consume; never re-audit independently)
F-01 (duplicate build map) ──► D-05 (deletes a map D-05 would otherwise convert)
F-02 (probe-seam traffic) ──► D-05 (same assumed win; measure first)
P4-01 ──► EX1 ──► batching geometry ──► D-05 (take3 13 §8.2 + §8.6)
MD-01 ──► D-09 (descriptor needed for the alignment codec)
D-02 verdict ──► D-03 and everything after it (stop is a legal outcome)
D-04 (MD-03.5) measurement ──► D-05 go/no-go (stopping rule 05 §6)
All retention sites (SKIPped sites only with a ledger row, see D-10) ──► D-10
P2-08/P2-10 resume after Phase 3/4 consumers exist (take3 ledger
`take2-P2-08`, `take2-P2-10`); no checkbox until then.
```

---

## 2. Track A — Instruments, parity, licence (first)

*Exit: P0 parity instrument committed with baseline roll-ups for both
suites; plan pin non-skippable; take3-owner acceptance for the MD code
tracks recorded.*

- [x] **A-01 P0-04 suffix numbering.** Aligned 2026-09-05: (i) IOS
  `Alias` stamp + rule-based IndexScan alias fix; (ii) planner-stamped
  global RTID (cuts 1–3: scope, threading, explain_names migration +
  substitution propagation at 8 sites + `rangeBinding.rtid`); (iv) cut 4
  re-measure — ZERO duplicate printed scan labels across 21 planned
  queries (was: Q8/Q17/Q18 lost comparisons + Q11 unresolvable); Q11
  fully pairs; the 46 "upper bound" qualifier discharged (absolute
  parity count re-measured at C-21).
  Gates per cut: optimizer + Explain suites, TPC-H 24/24 MATCH, PP
  label-only DIFFERs all PG-faithful, TPC-DS PASS=95 (cuts 1–2).
  Artifacts: `docs/design/executor-a01ii-rtable-identity/DESIGN.md`
  (F1–F10 folded), `analysis/planner-refactor-take3/a01ii-cut4-20260905/`,
  `analysis/leftdeep-joins/a01ii-cut3*.txt`.
- [x] **A-02 P0-05 plan-parity capture.** Committed 2026-09-05:
  `bench/tpch/plans-pg/` (22, split from the paired PG capture) +
  `bench/tpcds/plans-pg/` (99, captured per-query vs 65438/tpcds05
  SF0.5 with the sweep's EXPLAIN-prefix trick; Q36/70/86 SKIP per
  oracle) + re-capture policy READMEs. Reproducible: capture commands
  recorded in the READMEs; fixtures byte-stable (EXPLAIN-only).
- [x] **A-03 P0-06 plan-parity diff.** Landed 2026-09-05:
  `scripts/pg-plan-parity-diff.py` (normalised tree compare, estimates
  side-column only; verdicts MATCH/SHAPE-DIFF/MISSING-NODE/ERROR/
  TIMEOUT; nine-category taxonomy; declared normalisations incl.
  strip-PG-Hash + alias/suffix canonicalisation) +
  `scripts/pg-plan-parity-diff-test.py` (budget pinned in-test).
  TPC-H roll-up: match=5 shapediff=15 missingnode=2 (PG-only
  Materialize: Q5/Q8) error=0 timeout=0; report-only, exit 0 always.
  Gates: self-test 15/15, unittest 5/5.
- [x] **A-04 P0-07 baseline roll-up.** Committed 2026-09-05:
  `analysis/planner-refactor-take3/a04-baseline-20260905/README.md` —
  TPC-H power table (TOTAL ≈235 s serial, 24/24), TPC-DS PASS=95
  sweep pointer, parity starting roll-up 5/15/2/0/0 with per-category
  decrements enforced from the pinned test budget.
- [x] **A-05b Plan pin re-pinned under the reproducible regime.** Landed
  2026-09-05: `plan_snapshots/pinned-stats-20260905.txt` captured with
  `GOOPG_ANALYZE_SEED=20260905` + bench `autovacuum = off` (`870732855`).
  The previous baseline (`take2-p0-20260903`) failed structurally against
  HEAD itself, so the strict pin was failing on staleness rather than on
  changes. `make plan-gate` now passes **22/22 MATCH in structural mode AND
  in `MODE=costs`** — cost-exact pinning was not reachable at all while the
  sampler was unpinned (estimates moved on every server start).
- [x] **A-05 Non-skippable plan pin.** Landed 2026-09-05: `make
  plan-gate` strict by default (missing baseline / unreachable server →
  FAIL; explicit `PLAN_GATE_ALLOW_SKIP=1` opt-out); `--mode costs`
  (shape + cost/rows/width exact; structural stays default); TPC-DS
  plan-shape tail promotable via `SF05_PLAN_PIN=1` +
  `SF05_PLANS_BASELINE` (unset-baseline-under-pin is FAIL; accept by
  re-pointing; `=none` suppresses one run) — no second channel.
  Gates: plan-snapshot 15/15 (5 new: cost-only detection, SKIP-loud,
  unknown-mode), shell syntax, live dry-runs (FAIL without server,
  SKIP with opt-out, strict diff on fixtures).
- [x] **A-06 Take3-owner acceptance for MD tree commits.** ACCEPTED
  2026-09-05 by Ryo Kanbayashi (take3 owner): MD tree commits may land
  on `master` per the authorised approach in this item (D-02 document-
  only audit; D-04 throwaway prototype with deleted code; D-01/D-03/D-09
  through reviewed design and flag-gated prototype only; D-05 onward
  additionally needs E-14 + B-01c; landing code outside these bounds
  re-opens REVIEW.md B1). Pointer: this line.
  Original terms (kept for the record): MD is a new row representation
  with no re-proposal path in take3 13 §10 (README §Status, 04 §0.2),
  and reviewer (c) returned "do not start MD-01" (REVIEW.md B1).

---

## 3. Track B — Planner width + cost foundations

*Exit: widths fed into costs are right (P4-01 complete); `work_mem`
means the same thing in planner and executor (P2-02b landed);
`btcostestimate` batch lands whole; estimate ratchet monotone.*

Order inside this track: B-01a/b/c → B-12d/e/f → B-13 (P2-02b). Everything
else sequences freely but lands one variable per commit. B-05 and D-08
are EPICS — split into one-checkbox-per-commit items before starting
(ground rule 11).

- [x] **B-01a P4-01 deferred slice (a): merge/NL input policy.**
  Landed 2026-09-05: `deriveJoinKeepsAt` stamps both merge inputs
  (each gated on own `joinSubtreeNarrowable`); `narrowMergeInput`
  with `mergeKeepCoversSortKeys` gate (side-oriented via relids,
  fail-closed); NL = decline policy (probe keys live on inner
  `IndexClauses`, not the join path; `Project` above probe trips
  `innerBase` — plain-inner left as resume point).
  Sort-key-preservation proof: HashKeys order untouched (ascending
  keep), absorbed sorts impose nothing (keys ARE clause operands),
  keep ⊇ sort keys by construction + gate enforcement +
  `translateToLayout` tripwire.
  Gates: 6 new tests (2 existing assertions updated for the policy
  flip); TPC-H 24/24 MATCH; PP zero shape-kind changes (estimate/text
  only); Q12 merge 998→172 live; identical hashes at 64/4/512 MB;
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `internal/optimizer/{narrowoutput,createplanjoin}.go`,
  `pathtarget_test.go`.
- [-] **B-01b P4-01 deferred slice (b): scan-node application.**
  DECLINED 2026-09-05 after pre-implementation review (no safe first
  cut: in-place narrowing replays P4-01b; scan-arm Project lacks
  ancestor context — NLI/probe/outer-ref/poison — and reopens F4;
  restricted variant buys zero memory). Revisit needs (i) executor
  scan projection, (ii) narrowing-aware OuterColumnRef/Args rewriter,
  (iii) NLI probe inventory from IndexClauses, (iv) walkPlanExprs
  coverage + consumer. Ledger `take3-B-01b-declined`.
- [x] **B-01c P4-01 deferred slice (c): upper targets — COMPUTE HALF
  COMPLETE.** Status resolved 2026-09-05: all three compute slices landed
  (`sort_input_target.go`, `group_input_target.go`,
  `window_input_target.go`, each with its test file and fail-closed
  assert). The "Remaining: window-compute" note below predates slice 3 and
  is stale — window-compute IS slice 3.
  The APPLYING cuts are not deferred-by-omission, they are **out of this
  item's accepted scope by its own review verdict** ("NO applying cutting —
  no narrowing-aware upper rewriter exists"), and stay blocked on two
  prerequisites that do not exist yet: a narrowing-aware upper rewriter,
  and a key-preservation gate for it. Filed as ledger
  `take3-B-01c-applying-blocked` rather than left as an open checkbox that
  nothing can close.
  Review verdict: sort-input compute-only first (group second with
  Filter/Passthrough decline rules; window last after walker coverage;
  NO applying cut — no narrowing-aware upper rewriter exists).
  Slice 1 LANDED 2026-09-05: `Sort.InputTarget`/`InputTargetKnown`
  additive payload + `sort_input_target.go` derivation (keys ∪ above
  via existing walkers; unknown on unenumerable) + fail-closed assert;
  keys-only stamp at construction + above-aware re-stamp before return
  (upper tree built after the Sort).
  Gates: 17 new tests; optimizer suite; PP identical to B-01a run
  (zero drift); TPC-H 24/24 MATCH (assert never fired live).
  Remaining: window-compute (walker coverage first),
  applying cuts (need upper rewriter + key-preservation gate).
  Slice 2 (group-input) LANDED 2026-09-05: `Aggregate.InputTarget`
  payload + `group_input_target.go` (keys ∪ above; Filter/Passthrough
  decline; fail-closed assert) + construction stamp in
  `buildAggregateStage`, re-stamp-to-unknown at both Passthrough append
  sites and after `applyIndexOrderedGroupingRule`,
  splitAggregate Final-decline (Partial keeps).
  Gates: 22 group tests; optimizer suite; PP zero drift; TPC-H 24/24.
  Slice 3 (window-input) LANDED 2026-09-05: prerequisite walker
  widening first (`walkPlanExprs` Aggregate Filter/Passthrough +
  WindowAgg Funcs.Args/Filter/frame offsets; all 15 callers audited —
  sole mutator `remapOuterRefsInSubplan` now covers previously-missed
  same-scope refs = fix; readers flip to bail at worst) +
  `WindowAgg.InputTarget` payload + `window_input_target.go` (no
  field-level decline — every field enumerated post-widening; collector
  veto only) + keys-only construction stamps per spec.
  Gates: 29 new tests; optimizer suite; PP zero drift; TPC-H 24/24.
- [x] **B-02 P1-11 TOAST in the catalog heap writer.** Landed 2026-09-05
  as the pre-approved bounded-width interim: try-full-then-truncate in
  `persistStatsToPGStatistic` (pre-write exact measurement; 64 B bound
  cap UTF-8-safe → even thinning endpoints-kept → MCV-tail drop →
  scalar-only; scalars never altered; fitting rows bit-for-bit; NO
  format change, no re-init). Live-verified: c_comment/o_comment/
  ps_comment histograms persist post-ANALYZE (were dropped).
  Gates: stats/analyze suites + catalog suite; PP zero drift (ANALYZE
  changed stats, plans unmoved); TPC-H 24/24 MATCH.
  Fidelity loss ledgered (`take3-B-02-interim`); remove at TOAST
  out-of-line storage (M0125-0029).
  Artifacts: `internal/executor/operators_analyze.go`,
  `internal/executor/pgstatistic_truncate_test.go`.
- [-] **B-03 P1-14b remainder.** CLOSED 2026-09-05 with no
  implementation — all three sub-items predict zero EA-ratchet
  movement: regex ops (no `~` in either suite — dead path; resume =
  port regex_selectivity_sub + prefix analogue); prefix-range precision
  (blend cell unreachable; belongs to B-08, then eq-clamp tail);
  multibyte case-folding (ASCII-only data; executor-owned correctness).
  Ledger `take3-B-03-declined` (+ EA file-level baseline note).
- [x] **B-04 P1-21 `max(outer,inner)` fallback cap — verified-and-KEPT**
  2026-09-05: cap fires iff min(l,r)>200 on the unmeasurable fallback;
  proven both directions (MCV-fires ⟹ measured path; fallback-fires ⟹
  P1-15 declined); deleting moves 6M×800k 6M→2.4e13. Pinned by
  `cardinality_fallbackcap_test.go` (6 tests). Ledger `take3-B-04-kept`.
- [x] **B-05a P1-22 extended-statistics build.** Landed 2026-09-05:
  `_data` write path + ndistinct + dependencies builders (oracle
  magics/layouts pinned); `StatisticsObjectsForTable` accessor; hook
  at end of `analyzeRelationWith`; stattarget incl. 0-skip; reload
  decoder (consumer = later phase); codec KindBytes arms for the 4
  blob types. MCV/expressions explicitly deferred (TOAST +
  `_pg_statistic` encoding). Gates: 12 round-trip tests; catalog +
  initdb suites; live CREATE STATISTICS + ANALYZE builds a decodable
  3429 row; TPC-H 24/24 MATCH (Q1 stats-driven partial-agg move is
  values-correct); PP zero drift otherwise; TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `extstats_{build,ndistinct,dependencies}.go`,
  `sys_pg_statistic_ext_data.go`, `extstats_build_test.go`.
- [x] **B-05b P1-23 `statext_clauselist_selectivity`.** Landed
  2026-09-05: pure-planner port (in-memory registry keyed by table OID;
  empty ⇒ bit-identical old arithmetic); `choose_best_statistics` +
  strongest-dependency + backwards conditional-probability combine;
  clause compatibility = Var-half (col=const, IN, same-col OR, NOT,
  bare bool; ranges/joins/expressions decline); wired at
  conjunction/filter/clause-AND sites (join sizer deliberately NOT
  wired — oracle never runs ext on join lists).
  Gates: 15 tests; optimizer suite; units precommit scope green;
  PP zero drift (Q1 MATCH — fresh-server shape; the B-05a-run partial
  was ANALYZE-warmed stats, see `take3-stats-persistence-gap`);
  TPC-H 24/24 MATCH on a clean fresh-server sweep (prior sweep
  invalidated by mid-sweep server stop — relaunched);
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `internal/optimizer/extstats.go`, `extstats_test.go`.
- [x] **B-05c P1-24 `estimate_multivariate_ndistinct` for GROUP BY.**
  Landed 2026-09-05: exact-set combo lookup first (order-independent),
  fallback to untouched product; clamp/Yao paths unchanged; subset/
  superset fall back (iterative remainder = resume point).
  Gates: 9 tests; optimizer suite; PP zero drift; TPC-H 24/24 MATCH.
  Zero movement by construction (no production registry writer yet —
  loader is the resume point with the 3429 decoder).
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `internal/optimizer/{extstats,cardinality}.go`,
  `extstats_ndistinct_test.go`.
- [-] **B-06 P1-27 CTE-agg statistics — OUT OF SCOPE 2026-09-07.** Closed
  on evidence already gathered, against the standing rule (no gain /
  infeasible in goopg's design, after verification). Both halves fail it:
  removing the guard is a **regression, not a gain** (Q74 14 s -> 99 s), and
  PG has no answer here either — single-key uniqueness against a 4-key
  GROUP BY — so a faithful port cannot supply one. What remains is
  beyond-PG work (OID-less CTE-output synthesis + multi-key per-column
  ndistinct + FD-bound-only agg outputs) with **no safe ratchet-moving
  increment**, which is the definition of unbounded for this workstream.
  The synthesis design and its inert implementation slice (pure functions
  + 16 tests, no consumers wired) stay landed as the resume point; the
  step-4 guard-removal criterion is unmet and is not reachable from
  inside this refactor. Ledger `take3-B-06-deferred`.
  DEFERRED-OPEN 2026-09-05
  (probe): guard is load-bearing (removal reverts Q74 99s→14s); PG has
  no answer either (single-key uniqueness; Q74 groups by 4). Genuine
  fix needs beyond-PG work (OID-less CTE-output synthesis + multi-key
  per-column ndistinct + FD-bound-only for agg outputs); no safe
  ratchet-moving increment. Ledger `take3-B-06-deferred` (resume steps
  inline). Synthesis design landed (`docs/design/planner-b06-cte-stats/DESIGN.md`,
  reviewed); synthesis implementation slice landed inert (pure
  functions + 16 tests, no consumers wired — guard untouched).
  Keep open until step-4 guard-removal criterion holds.
- [-] **B-07 P1-30 index-endpoint probe + MCV widening — OUT OF SCOPE
  2026-09-07.** Closed on the probe's own findings. The endpoint probe is
  architecturally blocked (no plan-time storage path in the pure planner)
  and **PG itself keeps it `#ifdef NOT_USED`**, so fidelity does not
  demand it. The pure half that IS reachable — MCV-widen `histogramMax`,
  cutoff clamp — predicts **~0 ratchet movement** on this corpus: the
  endpoints are fresh and every literal is in-bounds, so there is no
  witness to improve. Reopen only on a demonstrated in-suite
  out-of-range case, which would be new evidence rather than new effort.
  Ledger `take3-B-07-deferred`. DEFERRED-OPEN
  2026-09-05 (probe): endpoint probe architecturally blocked (no
  plan-time storage path in the pure planner; PG itself keeps it
  `#ifdef NOT_USED`); pure half (MCV-widen `histogramMax`, cutoff
  clamp) predicts ~0 ratchet (fresh endpoints, in-bounds literals).
  Ledger `take3-B-07-deferred`. Resume only on demonstrated
  in-suite out-of-range case or accepted zero-ratchet fidelity.
- [x] **B-08 P1-31 text/network scalars.** Text half LANDED 2026-09-05:
  `convertStringToScalar` faithful port (range scan + class widening +
  <9 ASCII rule + prefix strip + ≤12-char base-N) wired for the text
  family; 0.5 pin flipped to interpolation; B-03 tail clamp
  `Max(prefixsel, eq_sel)`; adjacent float4/float8 label fix.
  Gates: convert/string-histogram/pattern/range suites; PP zero drift;
  TPC-H 24/24 MATCH (timing neutral as predicted).
  Network half DEFERRED (zero in-suite columns/queries; inequality-only
  would not cover inclusion ops) — ledger `take3-B-08-network-deferred`.
  Artifacts: `selectivity.go`, `patternsel.go`, `rangequery_test.go`.
- [x] **B-09 P1-02 tree height + partial-index tuples.** Closed
  2026-09-05 with no code change: partial half already landed
  (`2bcd6b551`, unit-pinned) — verified (partial test green, PP zero
  drift, zero partial indexes in bench DDL so gates vacuous by
  construction); height analogue declined-deferred (fanout already on
  real pages; measured 0.0004%; true analogue needs storage hook +
  nbtree dep for degenerate-only moves). Ledger
  `take3-B-09-height-deferred` (resume: bloated-tree witness;
  PG-shaped predicate-fold reshape filed).
- [-] **B-10 P1-10 expression-index stats.** DECLINED 2026-09-05, same
  test take2 applied: NO consumer — every estimate path resolves bare
  ColumnRef→table-column stats (joinselectivity gap comments,
  pathkeysindex/pathparamindex/indexordered/planner guards, executor
  errors on expression keys); ANALYZE iterates table columns only;
  zero expression indexes in either suite. Ledger `take3-B-10-declined`
  (resume: access paths → collection → matching).
- [x] **B-11 P1-16 re-diagnose Q9.** Filed 2026-09-05: take2 P1-16 had
  already closed rows 779/781/784 as stale + filed the LIKE-restriction
  mechanism; live-confirmed post-patternsel via estimate-audit
  (`analysis/leftdeep-joins/b11-confirm*.txt`): part 66,666→6,060 est
  (PG-like); final joinrel 6,060 vs 321,056 = 53× under (≤100× bar
  PASSES, 0 violations, 0 ambiguous). NEW follow-up filed in
  `take3-B-11-filed`: correction unmasked parameterized-NL fanout
  undercount (d13 estimates outer-only; excess 3.6×→50.4×, ratchet
  holds via floor) — join-size track work, not this item.
- [x] **B-12d Derived-table propagation.** Landed 2026-09-05:
  `planSelectWithParent` gains `ps` (resolves directly from the
  parameter, never parent/lateral — the reverted attempt's lesson);
  `planSubqueryRangeVar` forwards; out-of-scope callers (multi-assign,
  scalar/ARRAY/IN/EXISTS) pass explicit defaults marked B-12e/f.
  Gates: GUC-effect pin (5 arms fail pre-fix via stash, pass post) +
  unchanged-default pin; PP zero drift (Q7 scare = stale-stats
  artifact: ANALYZE mid-gate moved Q7 to merge; values identical;
  reproducible pre/post on demand); TPC-H 24/24 MATCH;
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `planner.go`, `settings_propagation_test.go`.
- [x] **B-12e Set-operation operand propagation.** Landed 2026-09-05:
  leftmost branch, `planSegment` right-operand closure, `SetOpOperand`
  grouping site pass the statement's own settings (line numbers had
  shifted since the design; grouped site gets own arms + per-operand
  isolation subtest).   Gates: GUC-effect (8 arms fail pre-fix via stash,
  pass post) + unchanged-default pin; PP zero drift; TPC-H 24/24 MATCH;
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `planner.go`, `settings_propagation_test.go`.
- [x] **B-12f Scalar-subquery propagation.** Landed 2026-09-05:
  scalar/ARRAY/IN/EXISTS planners take the explicit outer context's
  settings (`parent.settings`/`ctx.settings`, never planParent);
  multi-assign UPDATE via `planUpdate` ps param (both SET/WHERE
  contexts); `planSelectWithParent` zero-guard (pre-P2-01 literals
  fold to defaults); DML-CTE passes explicit defaults.
  Gates: 22+7 subtests (pre-fix FAIL via stash); PP zero drift;
  TPC-H 24/24 MATCH; TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `planner.go`, `with.go`,
  `settings_propagation_test.go`. Unstamped hosts (HAVING/VALUES/
  ORDER BY/CTE bodies, INSERT…SELECT, ON CONFLICT, DML derived
  tables) stay default — future slices.
- [!] **B-13 P2-02b `work_mem` BootVal 512 MB → 4 MB — BLOCKED on
  spill-cost calibration (re-probed 2026-09-05).**
  **Method note added 2026-09-05:** the prerequisite named here — spill-cost
  calibration — is the SAME class of problem C-20d turned into a 27% suite
  win, and it now blocks three items (B-13, B-15, E-16). C-20d's method
  transfers: add a multiplier in front of the charge, measure the suite at
  several values with values-diff on every arm, and adopt the smallest
  value that buys the win. The charge to calibrate is `hashJoinCost`'s
  spill arm (`cost_funcs.go`: `seqPageCost * innerPages` startup and
  `seqPageCost * (innerPages + 2*outerPages)` run, from `spillPages`).
  The complication that makes it more than a repeat of C-20d: the symptom
  ("model prices hash above merge while forced-hash proves faster") is only
  observable at a reduced `work_mem`, which needs E-16's session-`work_mem`
  plumbing — and E-16 is itself blocked on this calibration. Breaking that
  cycle means measuring with the plumbing applied from its filed patch
  (`analysis/planner-refactor-take3/ex303-step2-deferred-20260904/plumbing.patch`)
  WITHOUT landing it, exactly as C-20d measured a multiplier before adopting
  one. Narrowing bought
  16→4 batches, not 1: 8 MB budget vs 32 MB witness → Q9 ~1.5–2×,
  Q7 >1.2× risk — flip now violates B2. Prerequisite is calibration +
  EX3-03 threading (model misranks merge above hash), NOT P5, NOT
  B-01b/c remainder. Three lines verified unflipped. Ledger
  `take3-B-13-deferred` (re-probe trigger inline).
  **Design landed 2026-09-06: `docs/design/planner-spill-cost-calibration/DESIGN.md`.**
  It finds the hash and sort spill charges biased in OPPOSITE directions
  (hash over-charges — `EntryBytes` measures the in-memory entry while
  `spillWriter.WriteRow` uses uvarint framing; sort under-charges twice,
  and one is a TRIGGER error — `costSortRun` fires at `cp.workMem` = 1 GiB
  while `sortOp` spills at the hardcoded 256 MiB `sortChunkBytes`), so a
  single multiplier would fit the difference between two errors. Four
  separately-gated cuts, three probes, negative outcomes written in
  advance.
- [x] **B-14 P2-09a ScalarArrayOp index path.** Landed 2026-09-05:
  InExpr→clause arm (useOr-only, left indexkey, const array, opfamily)
  in the rule-based producers + post-search rewrite; `IndexScan.SAOPKeys`
  one probe per element; cost `numSAScans` + ceil(pages/3) clamp +
  descent×scans; executor multi-descent (per-element descent, TID union
  dedupe, NULL skip, gap lock); `tryPromoteIndexOnlyScan` declines SAOP;
  bitmap deliberately not extended (single-key carrier).
  Gates: Q45-shape unit proof + unmoved pins (NOT IN/ALL/expr/subquery/
  unindexed stay SeqScan); TPC-H 24/24 MATCH, PP zero drift (no movable
  shape in-suite); live Q45 SubPlan 1 → `Index Scan using item_pkey on
  item_1` with `= ANY` cond, outer expression-ANY stays Filter;
  TPC-DS PASS=95 MISMATCH=0 (Q45 7 rows checksummed).
  Artifacts: planner/cost/executor probe + `saop_index_test.go` ×2.
  Follow-ups landed 2026-09-05 (`afb39bb7c`): `Index Cond: (col = ANY
  (..))` EXPLAIN rendering; `indexScanPredicate` rebuilds `col IN
  (keys)` so the SeqScan fallback filters exactly the probed rows
  (without it UPDATE..WHERE IN deleted 0-vs-3 live — wrong answers);
  SSI fingerprint 2→3 for the multi-descent site.
  Gates: executor + optimizer suites green; smoke 0 failed.
- [!] **B-15 P2-09b `btcostestimate` batch — BLOCKED (E1 failure,
  reverted).** R1–R5 implemented unit-green but flips 14 shapes toward
  NL: Q10 5.6×, Q7 ~4×, Q9 2.3×, Q14 2.4×, Q5 2× (Q3 legitimately
  faster inside the failing batch); values 24/24 (pure ranking
  failure). Hypothesis: R5 pro-rating collapses looped-probe costs
  without PG's heap-qpqual counterweight. Resume: unit-confirm bias,
  calibrate, per-query + TOTAL + plans gates. Artifact:
  `analysis/planner-refactor-take3/b15-deferred-20260905/` (README +
  clean batch.patch). Ledger `take3-B-15-blocked`.
  **Design landed 2026-09-06: `docs/design/planner-spill-cost-calibration/DESIGN.md`.**
  It finds the hash and sort spill charges biased in OPPOSITE directions
  (hash over-charges — `EntryBytes` measures the in-memory entry while
  `spillWriter.WriteRow` uses uvarint framing; sort under-charges twice,
  and one is a TRIGGER error — `costSortRun` fires at `cp.workMem` = 1 GiB
  while `sortOp` spills at the hardcoded 256 MiB `sortChunkBytes`), so a
  single multiplier would fit the difference between two errors. Four
  separately-gated cuts, three probes, negative outcomes written in
  advance.
  *design: take3 08 §5.2; gate: take3 09 §5 P2 + TOTAL arm.*
- [x] **B-16 P2-11b MCV-frequency half.** Landed 2026-09-05: MCV scale
  (`mcv/avgfreq` iff hotter than average) + [1e-6,1] clamp +
  default-ndistinct `Max(0.1,mcv)` decision; nbuckets cap explicitly
  out (signature widening). Unblocks the E-02 cost half.
  Gates: skewed/uniform/empty/clamp/isdefault/filtered-scaling pins;
  optimizer suite; PP zero shape moves; TPC-H 24/24 MATCH; TPC-DS SF0.5
  sweep PASS=95 MISMATCH=0.
  Artifacts: `joinselectivity.go`, `joinselectivity_test.go`.
- [x] **B-17a `disabled_nodes` sort setter** (take3 02 §1.2). Landed
  2026-09-05: `sortPathFor` counts `enable_sort` (costsize.c:2144),
  producer never skipped. Material half declined — no `PathMaterial`
  exists by design (executor materialises unconditionally).
  *design: take3 08 §5.4; gate: GUC-effect test
  `TestEnableSortSetsDisabledNodesOnSortPath`.*
- [!] **B-17b `disabled_nodes` agg-hashed/mixed setters** (take3 02 §1.2)
  — BLOCKED on C-15 (P4-06 grouping paths). DECLINED 2026-09-05: no grouping paths exist (P4-06 open), no path to
  carry the count; `enable_hashagg` stays rule-based. Unblocks when P4-06
  lands grouping paths. Ledger `take3-B-17b-blocked`.
- [x] **B-17c `disabled_nodes` gather-merge setter** (take3 02 §1.2).
  Landed 2026-09-05 as opt-out `DisableGatherMerge` (zero value keeps the
  merge arm; `EnableGatherMerge` default-false would have killed
  GatherMerge in production). *gate: GUC-effect test
  `TestEnableGatherMergeOffFallsBackToGatherBelowSort`.*
- [x] **B-17d Retire producer-skipping for scans** (take3 01 §12, 02 §1.2).
  Landed 2026-09-05: seqscan/ordered+param index/plain+param bitmap heap
  always generate + count (`enable_seqscan/indexscan/bitmapscan`);
  index-only splits legacy OR-gate into `indexOnlyHardDisabled`
  (indexonlyscan only stays a gate; indexscan-off counts). Hard gates stay
  gates (index-only, memoize; TID/incremental-sort vacuous, no producer).
  *gate: GUC-effect tests incl. `TestIndexOnlyScanGateStaysAGate`.*
- [!] **B-17e Retire `enable_nestloop_index`** (take3 08 §6.4) — BLOCKED
  on C-20 (P6 single-planner deletion). DECLINED 2026-09-05: NLI is not an ordinary parameterised-nestloop path
  (`addNLIPaths` emits `PathNestLoop` with no `DisabledNodes`; legacy
  `rewriteJoinsToNLI` gated by `EnableNestLoopIndex` is load-bearing,
  take3 P6-04 Q4 semi 12.5×). Retire only with P6. Ledger
  `take3-B-17e-blocked`.
- [x] **B-18 P2-04 cache-key half, commit 1 (key).** Landed 2026-09-05:
  `planCacheKey(sql, dbOid, fingerprint)` over full `PlannerSettings` +
  4 scan toggles (exact float formatting; `ParallelSettings` excluded —
  post-cache pass, unkeyable func field); deleted
  `plannerCostGUCsOverridden`/`plannerScanTogglesActive`/
  `plannerSessionInputsActive` bypass at all 4 guard sites; SET sessions
  now hit the cache instead of bypassing it.
  *design: take3 08 §5.1 pointer is half-stale (real pointers: take2
  impl/P2-A-planner-context.md:165-182, take3 04 §1 row 168, 09 §5 P2);
  gate: `SET random_page_cost` separates cached plans + third-execution
  hit, float-exactness, per-field coverage; postmaster suite green.*
- [x] **B-18 commits 2-4: GUC-effect fixtures** (one variable each).
  Landed 2026-09-05 in `internal/optimizer/cost_guc_effect_test.go`:
  index shape pins `random_page_cost`, `cpu_index_tuple_cost`,
  `effective_cache_size`; Gather shape pins `parallel_setup_cost`
  (startup+total) vs `parallel_tuple_cost` (total only, startup pinned —
  the discriminator). Post-pass `parallel_*` knobs need no fixture.
  Gates: B-17+B-18 default traffic — TPC-H 24/24 MATCH, TPC-DS SF0.5
  sweep PASS=95 MISMATCH=0; optimizer + postmaster suites green.
- [x] **B-19 rowest-A1 null-double-exclusion correction.** Landed
  2026-09-06: paired range bands add the column's null fraction back
  (`s2 += NullFrac` where stats resolve — PG clausesel.c:283), with
  PG's punt guard (either bound == DEFAULT_INEQ_SEL →
  DEFAULT_RANGE_INEQ_SEL) and resolved-identity gating
  (SourceTableIdx != 0). Probe-verified shape from
  `analysis/planner-refactor-take3/rowest-collapse-20260906/` (the
  failed-agent item "rowest fixes B1+A1": B1 already landed, this
  completes A1). Gates: pairing unit + punt + no-stats tests;
  optimizer suite; units scope; live synthetic reproducer (nz
  100-149: 217 → 2430 vs true 2500; null-free table byte-identical);
  TPC-H PP 22/22 MATCH + spotcheck PASS; TPC-DS sweep 95 PASS
  CKMISMATCH=0; TPC-DS PP 28 moves, all attributed to A1 via
  clean-vs-dirty capture (clean 99/99 vs baseline), Q28 rows=1 →
  14932 shape-preserved; review APPROVE-WITH-NITS. Follow-up filed:
  port band formula to the WithSource twin (bounded pre-existing
  divergence, empirically plan-shape-preserving).
  Artifacts: `internal/optimizer/rangequery{,_test}.go`,
  `selectivity.go` (twin-divergence comment).

---

## 4. Track C — Search, upper planner, parallelism, deletion, acceptance

*Exit: PG-only join spines OFFERED or reasoned; upper rels are paths;
parallelism costed not forced; single planner; acceptance run green.
**Do not start P3-02/03/04 before B-01 lands** (take3 08 §2.3 safety
rule).*

- [x] **C-01 P3-01 `SpecialJoinInfo` population.** Landed 2026-09-05: `makeSpecialJoinInfoScoped` (clause/strict relids over ON/USING via name→leaf scope + lower-OJ ordering scan grow-only + FULL early-return + empty-min punt) + `deconstructJointreeScoped`/`deconstructFromItemScoped` threading + `newSjiScope` (leaf order = deconstruct order); fail-closed syn fallback on any uncertainty; ANTI flags aligned to upstream false/false; USING/NATURAL punt filed as follow-up. Gates: 8 new scoped tests + optimizer suite + units precommit scope + pgbench smoke (0 failed) + agent review APPROVE-WITH-NITS (5 minors addressed). No plan-shape move by construction (no consumer reads the new fields yet). Artifacts: `docs/design/planner-c01-sji-population/DESIGN.md`, `internal/optimizer/{specialjoin,collapse,planner}.go`, `specialjoin_scoped_test.go` — `ea8ca9dfe` (code), `2e59cfe49` (design).
- [x] **C-02a P3-02 delay test (inert).** Landed 2026-09-05: `delayedAboveOJ` pure function (delay iff qual reaches the link's nullable side; no strictness exemption — demotion already ran; FULL always; nil sj delays; SEMI/ANTI fail closed) + 20-case unit table. No callers — no plan/behaviour change by construction. Design `docs/design/planner-c02-qual-placement/DESIGN.md` (reviewed: REQUEST-CHANGES → all 8 resolved → APPROVE; rebased on PG18 `outerjoin_nonnullable`/`incompatible_relids`, slices a–d). Gates: optimizer suite. Artifacts: `internal/optimizer/outerjoin_delay{,_test}.go` — `b90b08d` (code), `0dac569c1` (design).
- [x] **C-02b P3-02 plan-level delay attribution (inert).** Landed 2026-09-05: `outputRelSet`/`qualSrcRelSet`/`planJoinDelaySJI` make the delay proof computable at plan-tree joins (SourceTableIdx attribution, no joinlist alignment). Deliberately NOT consumed by the copy pass — review-proven vacuous there (legacy side gates already decline every nullable-side qual; the check could only fire on Index-vs-srcIdx disagreement). Verdict parity, not fewer copies. Gates: attribution tables + parity pins + walker-inventory pin; optimizer suite;   review REQUEST-CHANGES → reframed to inert infra → re-review APPROVE-WITH-NITS (2 nits fixed in `6f0232c`). Artifacts: `internal/optimizer/outerjoin_delay.go` (extended), `outerjoin_delay_test.go`, inventory pin — `9c0b549` + `6f0232c`.
- [x] **C-02c P3-02 move on all-delay-proven paths.** Landed 2026-09-05:
  `pushTrace` (proven / planted / crossedOuter) carries the full-path delay
  proof through the copy descent; the residual conjunct is DROPPED only on a
  fully-attributed all-INNER path with no sibling derivation planted, and a
  Filter whose every conjunct moved is spliced out (the pass now returns the
  replacement tree). Copy mechanics stay byte-identical to legacy on every
  declining path — an unknown prior proof (idempotence hit), an
  outer-crossing descent (C-02d scope), incomplete attribution, or a planted
  `deriveConstAcrossJoinEquality` copy all keep the residual. Moved
  conjuncts are NOT recorded in `PushedBelow`: nothing is duplicated, so the
  descendant prices them exactly once by construction.
  Gates (pinned regime, `GOOPG_ANALYZE_SEED=20260905`, autovacuum off,
  GOGC=100/GOMEMLIMIT=12GiB, fresh server per arm): TPC-H values **24/24
  MATCH** vs the HEAD baseline arm; plan capture **byte-identical** to
  baseline; PP roll-up unchanged at match=5 shapediff=15 missingnode=2;
  Q12=2/Q13=34 canonical; timing within the band on every query
  (Q9 12.07 -> 12.63 s, Q5 20.13 -> 19.36 s); optimizer suite green with 6
  new pins (move, derivation-veto, outer-keeps-residual, idempotence-hit,
  partial move, jointype-mapping).
  Prerequisite landed first: the gate itself was not reproducible — see
  `docs/design/planner-gate-reproducibility/DESIGN.md` (`870732855`), which
  is why the first measured Q9 "2x regression" was a statistics artifact.
  Ledger `take3-C-02c-noted` records the untouched
  `pushQualsThroughSingleRefCTEs` duplication (orthogonal, pre-existing).
  *design: C-02 DESIGN.md §5 C-02c.*
- [x] **C-02d P3-02 move across preserved-side outer links.** Landed
  2026-09-05: the outer-crossing veto is REMOVED, not replaced — a
  descent can only enter a preserved side (`joinRestrictionSides` answers
  left-only for LEFT, right-only for RIGHT, and refuses FULL/SEMI/ANTI/
  LATERAL), so a crossed outer link is always a preserved-side descent.
  Review-driven strengthening in the same commit: the proof now also
  requires POSITIVE side containment (every identity the conjunct reads
  lies inside the side descended into) and treats a relation-free
  conjunct as unprovable — the negative delay test alone returns false at
  an INNER/CROSS link without inspecting the qual, so containment is what
  makes the claim hold at every link.
  Review: APPROVE-WITH-NITS, no counterexample found across
  Index-vs-SourceTableIdx disagreement, constant quals, multi-level
  spines, nodes above the Filter, volatile/sublink declines, the
  idempotence arm, and the untraced CTE callers; all 9 findings applied
  (2 majors were test gaps, now pinned).
  Gates (pinned regime): TPC-H values 24/24 MATCH; plans byte-identical
  to baseline; `make plan-gate` PASS; PP unchanged 5/15/2; TPC-DS SF0.5
  **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0**; optimizer suite
  green with 7 new pins (LEFT move, RIGHT move + rebased index,
  nullable-side never descends, FULL never descends, two-level blocked,
  two-level INNER-above-LEFT move, identity-disagreement copy, constant
  unprovable).
  *design: C-02 DESIGN.md §5 C-02d + the "Realisation" note.*
- [x] **C-03a P3-03 `Path.Jointype` field (inert).** Landed 2026-09-06: `Path.Jointype parser.JoinType` (zero value = `JoinInner`, so every unstamped path — scans, Sort/Agg/Gather wrappers, test fixtures — is unchanged); the five join producers (`addHashJoinPath`, `addNestLoopPath`, the NLI arm, the merge arm, `addPartialHashJoinPath`) stamp `JoinInner` explicitly so C-03b is a value swap, not a new field; compare-ignore pinned (`comparePaths`/`comparePathCosts`/`addToPathlist` blind to jointype — it is a correctness attribute set by legality, not a cost dimension, and `setCheapest` inherits the rule); DPPATH gains a `jointype=` label appended AFTER `verdict=` so existing readers are unaffected. Carrier is the PATH, never the `RelOptInfo` (a relset-keyed singleton cannot hold one jointype). No consumer reads the field — inert by construction. Gates: 4 new pins (`pathjointype_test.go`: R1 zero-value, producers-stamp-inner, compare-ignore incl. `addToPathlist` non-duplication, DPPATH label rendered off a real stderr pipe with ordering pinned) + optimizer/executor suites + `RALPH_PRECOMMIT_SCOPE=units` exit 0. *design: `docs/design/planner-c03-jointype-search/DESIGN.md` §4 C-03a (reviewed APPROVE-WITH-NITS).*
- [x] **C-03b P3-03 jointype-aware `addPaths` (inert).** Landed 2026-09-06: `joinRelBuilder.addPaths` gains the matched `sjinfo` (nil-safe), passed POST-`reversed`-swap to BOTH directions; `jointypeForDirection` (the `populate_joinrel_with_paths` switch, joinrels.c:906-1029, reduced to one direction) decides per call — legality is an ORIENTATION question, not a jointype one, which is why PG gives its two `add_paths_to_joinrel` calls different jointypes (JOIN_LEFT/JOIN_RIGHT at :932-939). OUTER (LEFT/RIGHT/FULL): legal iff outer covers MinLefthand and inner MinRighthand; the reversed direction is DECLINED rather than emitted as PG's JOIN_RIGHT/JOIN_RIGHT_SEMI/JOIN_RIGHT_ANTI (deliberate narrowing — withholding a path cannot produce a wrong answer). SEMI/ANTI: nestloop-only (both merge arms, the serial hash arm and its partial twin decline as one block — goopg has no `create_unique_path` analogue and a keyed SEMI would MULTIPLY rows). `jointype` threaded into every path producer the way PG threads it into `try_*_path`. Inert: `joinIsLegal` returns nil for every pair the search reaches today, so every production call takes the `sjinfo == nil` arm unchanged. Gates: 5 new adjudications (`joinpathsjointype_test.go`: orientation table, inner positive control stated on DPPATH OFFERED not on the pruned pathlist, OUTER legal-direction-only, SEMI/ANTI nestloop-only incl. PartialPathlist, seam sjinfo+swap, DPPATH OFFERED/ACCEPTED per jointype) + optimizer/executor suites + units precommit exit 0. *design: C-03 DESIGN.md §4 C-03b.*
- [x] **C-03c P3-03 `createPlanNode` jointype arms (inert).** Landed 2026-09-06: all five join arms (hash, merge, plain NL, NLI, bitmap-NLI) read `Path.Jointype` through `planJoinTypeFor` — the SINGLE meeting point of `parser.JoinType` and `optimizer.JoinType`, so a jointype cannot reach a plan by one route and not another; SEMI/ANTI publish left-only SCHEMA and LAYOUT via `joinInputs.publishedSchema`/`publishedLayout` (both halves: `Join.Output` narrows on its own but the layout is returned separately and would misalign every parent, and `NestedLoopIndexJoin.Output` returns the field RAW → executor XX000). FULL DECLINED at path generation in `jointypeForDirection` (empty pathlist → `joinSearch` error → syntactic-shape fallback, the same outcome the pre-C-03 tree reaches), with `planJoinTypeFor` panicking as the second line; ledger row `C-03c FULL-join-search-decline` (resume point = PG's JOIN_FULL arm joinrels.c:938-963 + `useallclauses` joinpath.c:1849-1852). Rel-level sizing (`NCols`/`AvgVarBytes`/`ColVarBytes`/`ConsiderParallel`) deliberately stays at the concatenation: a relset-keyed singleton cannot hold a jointype (C-03a's carrier decision), and over-wide beats under-wide for hash sizing — C-05 installs the real switch. Gates: 4 new searched-shape units (`createplanjointype_test.go`, arms forced through by hand — "an unwinnable path is an untested path") + optimizer/executor suites + units precommit exit 0 + **take3 R8 zero-drift measured against a pre-C-03 binary (f11d4fbcd): TPC-H 24/24 values identical (colsig/ordered/unordered/rows), serial plan capture BYTE-IDENTICAL, PLAN-PARITY 6/14/2 unchanged, `make plan-gate` exit 0**. *design: C-03 DESIGN.md §4 C-03c.*
- [x] **C-03d P3-03 enum-trace DPPATH evidence.** Landed 2026-09-06: `joinsearchjointype_trace_test.go` drives the SEARCH (not the arm) on a spine-shaped fixture — 3 rels, a LEFT/SEMI SJI with min LHS {a} / min RHS {b} — and adjudicates BOTH provenance channels: DPTRACE through the production reader `estimateaudit.ParseEnumTrace` (LEFT pairing `{a} | {b}` OFFERED at its level; `{b} | {c}` recorded as DECLINED reason=`illegal`, not merely absent — the ambiguity `spine.go` closes on), and DPPATH (paths over relids {0,1} carry `jointype=left`/`semi`, OFFERED and ACCEPTED; for SEMI the keyed producers are absent and `join.nestloop` present, asserted on the trace rather than the pruned pathlist). Plus the DESIGN §5 inertness MECHANISM pin: with the full `joinInfoList` handed unfiltered to a prefix problem, `joinIsLegal` returns `(nil, false, nil)` for every prefix-internal pair via `join_is_legal`'s RHS-overlap fast path (joinrels.c:386-387) — which is *why* C-03a..d are inert, not merely evidence that they are. Gates: 3 new fixtures + optimizer/executor suites + units precommit exit 0. *design: C-03 DESIGN.md §4 C-03d.*
- [x] **C-04a P3-04 LEFT admission (Q72 witness).** LANDED `f045f545c`,
  then FIXED TWICE after the TPC-DS SF0.5 gate caught what TPC-H cannot
  see (TPC-H was 24/24 by values and its one plan move, Q13, was a −14%
  improvement). **(1) Q72 WRONG ANSWER**, 84 rows vs oracle 100: the
  admitted LEFT links lost their jointype through the collapse-limit
  sub-problem split and planned as INNER. Fixed `fb6550266` (per-problem
  SJI remap), pinned `TestLeftLinkSurvivesCollapseSplit`. **(2) Q78 15 s →
  327 s TIMEOUT**: the search admitted `ss LEFT JOIN ws LEFT JOIN cs` with
  every path at rows=1 and took an epsilon Nested Loop (3.07 vs Hash 3.09)
  over three full CTE outputs. The `problemPairsOuterWithDerived` firewall
  exists to decline exactly that and did not fire: its classifier saw the
  `*Filter` wrapping each CTE output, not the `*CTEScan` beneath, and
  `with.go`'s synthesised non-nil table defeats `table == nil` anyway.
  Fixed by classifying on node type AND descending through `*Filter`/
  `*Project` — reached only after three wrong hypotheses, each plausible
  from the code and each refuted by one trace line. Pinned
  `…CTEOuter` + `…WrappedCTE`, both mutation-checked. Q78 → **19 s,
  checksum-verified**. Full gates on the final tree: TPC-H **24/24 MATCH**
  by values; **same-session A/B −0.8%** with Q13 **−17.4%** and every
  other query within ±1.5% (a +5.4% read against a 3-hour-old baseline was
  entirely host drift — the unchanged pre-C-04a binary itself moved +6.3%
  over that span; contaminated and stale arms are recorded, not quoted);
  TPC-DS SF0.5 **PASS=95 MISMATCH=0 CKMISMATCH=0 TIMEOUT=0**, plan shapes
  99/99, total delta +0.0%; `make plan-gate` 22/22 structural and
  `MODE=costs`, re-pinned `c04a-fixed-20260906` (diff vs prior pin = the
  Q13 hunk only); optimizer + executor suites green.
  Ledger `take3-C-04a-Q72-jointype-loss`, `take3-C-04a-Q78-firewall-classifier`.
  What C-04b/c inherit: the rows=1 CTE estimate (rowest A3) is NOT fixed,
  only firewalled; SEMI/ANTI remain nestloop-only (a capability gap, not
  parity); the reversed OUTER direction is declined so LEFT joinrels carry
  one orientation.
- [x] **C-04b P3-04 RIGHT admission.** Same vertical cut mirrored.
  *design: C-04 DESIGN.md §5 C-04b; gate as C-04a + DPPATH.*
  **LANDED `b1834abb2`.** RIGHT admitted as the LEFT join it reduces to
  (`reduceRightLink`), through the same reduction `makeSpecialJoinInfoScoped`
  applies to the SJI, so chain and SJI describe the same hand — mirroring
  PG, which hands its two directions different jointypes (joinrels.c:906-1029)
  because legality is an orientation question. Carries both C-04a
  mechanisms rather than rediscovering them (per-problem SJI remap over the
  collapse split; the outer-over-derived firewall, verified firing on a
  RIGHT-over-derived fixture). Also closes a hole C-04a opened: a pinned
  LEFT/RIGHT item reaching the search as a two-item problem is now checked
  against `join_info_list`, fail-closed. Gates as C-05 above.
- [x] **C-04c P3-04 below-inner + non-first-comma LEFT links.**
  Non-spine LEFT admission. *design: C-04 DESIGN.md §5 C-04c; gate as
  C-04a.*
  **LANDED.** `extractSearchLeaves`' `onSpine` flag is WIDENED, not
  deleted: an INNER join preserves both its inputs, so descending one
  carries the flag through (C-04a cleared it, which is what declined a
  LEFT link below an inner one); what clears it is descending into a side
  some link null-extends. The non-first-comma decline is lifted by
  `rebaseChainQual`, built on `cloneExprRefs` — exhaustive over all 32
  Expr types by a build-time gate and fail-closed on an unknown one,
  which is exactly the property `shiftColumnRefsBy` (13 arms of 32,
  `return e` for the rest) lacks and the reason the file header called
  the re-base unsafe. It lifts for INNER links too; the check was one
  test for both.
  **Two shapes stay declined, both measured rather than inherited:**
  (a) an INNER link's `ON` conjunct reaching the nullable side of a link
  BELOW it — an inner qual's "anywhere at or above its own join" licence
  stopped implying "at or above every outer link" the moment an outer
  link could sit below an inner one, and the correct answer is upstream's
  `required_relids` widening, which `restrictInfo` has no field for
  (`chainOnQual.belowNullable`; ledger
  `c04c-inner-on-qual-above-outer-declines`);
  (b) an outer link inside ANOTHER outer link's nullable side — ADMITTED,
  MEASURED, RE-DECLINED. `nsj_t LEFT JOIN nsj_p ON t.id = p.id RIGHT JOIN
  nsj_q ON t.id = q.id` returned `NULL|NULL|c` where PG returns
  `3|NULL|c`: `buildJoinRelRestrictList` re-applies the LOWER link's own
  `ON` clause at the upper join as an outer-join filter clause, filtering
  the rows that join exists to null-extend. C-04b's decline pin
  `TestSeamDeclinesAnOuterLinkUnderARightLinksNullableSide` therefore
  STAYS, contrary to its own comment; ledger
  `c04c-nested-outer-refilters-lower-on-qual`.
  Tests: `joinsearch_c04c_test.go` (admission + the two declines + the
  re-base primitive, including its fail-closed sublink arm),
  `joinsearch_c04c_trace_test.go` (DPPATH jointype=left OFFERED/ACCEPTED
  for both newly admitted positions, the collapse-split SJI-remap mirror
  of C-04a's Q72 pin, and the Q78 outer-over-derived firewall verified on
  the new shape with `with.go`-shaped Filter-wrapped CTE leaves),
  `internal/executor/c04c_nonspine_join_admission_test.go` (11 VALUES
  cases, under and over `join_collapse_limit`).
  Gates: optimizer + executor suites green, vet clean; TPC-H 24/24 MATCH
  by values, same-session A/B on a private clone 141.59 s -> 141.68 s
  (+0.06 %), every mover under 0.25 s absolute; TPC-H plans
  BYTE-IDENTICAL to HEAD; PLAN-PARITY 22/5/15/2 unchanged; plan-gate
  22/22 structural and 22/22 MODE=costs against pin
  `c05-c04b-20260907`, no re-pin needed; TPC-DS SF0.5 sweep (see the
  commit).
- [x] **C-05 P1-18 outer/semi/anti sizing — executes HERE, after C-04.**
  Port `calc_joinrel_size_estimate`'s jointype switch.
  *design: take3 08 §4 + §6.2; gate: EA ratchet.*

> **WARNING (2026-09-06): the EA ratchet cited as this item's gate HAS
> NEVER RUN.** No Makefile target, hook, precommit script or ci/batch
> stage invokes `scripts/tpch-estimate-audit-arm.sh`, and its default
> pinned PG baseline is not in the tree. `estimate-audit -plan-only` (the
> plan-capture step) does run and is fine; the est-vs-actual parity
> ratchet does not. It is also TPC-H-only and joinrel-granular, so it
> cannot see the base-rel `rows=1` collapse diagnosed in
> `docs/design/planner-rowest-collapse/DESIGN.md`. Ledger
> `take3-ea-ratchet-never-ran`. Build the gate before relying on it.
  **LANDED `e7ca37d97`.** All five arms of `calc_joinrel_size_estimate`'s
  jointype switch, plus `joinPublishesInner` (the rel-level SEMI/ANTI
  left-only publication C-03c's per-path narrowing had no counterpart
  for), a dedicated semi selectivity estimator, and `pushedDownSelectivity`
  separating pushed-down from join clauses. The Q78 firewall is
  deliberately NOT removed — its resume condition is B-06's CTE stats, and
  the sweep confirms it still declines. Gates (with C-04b, disjoint by
  file, gated together): TPC-H 24/24 by values, same-session A/B −0.7%;
  TPC-DS SF0.5 PASS=95 all-zero, shapes 99/99, verdict-changes=none;
  plan-gate 22/22 both modes, pin `c05-c04b-20260907`.
- [!] **C-06 P3-05 retire `GOOPG_PGSHAPED_COLLAPSE` — BLOCKED 2026-09-07 on
  its own byte-identical gate; nothing deleted.** Two `estimate-audit
  -plan-only` captures on one cluster, one per flag value: the flip is NOT
  plan-neutral. TPC-H **Q13 moves, and the OFF plan is the PG-parity one** —
  `Index Only Scan using customer_pk` + `Hash Left Join` (cost 66,218) vs the
  ON path's `Index Scan` + `Merge Left Join` (338,223). Runtime is a wash
  (5.40 s OFF / 5.47 s ON, same session, identical digests). Retiring the
  flag would delete the only reachable spelling of a PG-shaped Q13 for no
  measurable gain — the `GOOPG_INDEXKEY_HARVEST` precedent. The item's
  premise is also unmet: the collapse=0 regime is fully alive after C-04c
  and untouched by it. Real blocker, named in ledger
  `c06-collapse-flip-moves-q13`: why the search wins a Merge Left Join at 5x
  the cost. Resume when that is answered — retiring the flag is downstream
  of it, not a prerequisite.
  *design: take3 08 §6.3; gate: take3 09 §5 P3.*
  **BLOCKED ON ITS OWN GATE, measured 2026-09-07 after C-04c.** The gate
  is "byte-identical plans for the flip" and the flip is NOT byte-
  identical: with C-04c in the binary, `GOOPG_PGSHAPED_COLLAPSE=0` and
  `=1` produce different TPC-H plans for **Q13**, and the OFF plan is the
  PG-parity one the seam's own header names as the target — `Index Only
  Scan using customer_pk` + `Hash Left Join` (cost 66 218) against the ON
  path's `Index Scan` + `Merge Left Join` (cost 338 223). Runtime is a
  wash (5.40 s OFF vs 5.47 s ON, same-session, values identical), so
  retiring the flag would delete the only reachable spelling of a
  PG-shaped Q13 and buy nothing. That is the `GOOPG_INDEXKEY_HARVEST`
  precedent (ledger `take3-C-20c-blocked`) exactly: a flag whose OFF path
  is not worse stays.
  The item's premise is also not met. "Once C-04 makes it the only
  jointree path" describes a state C-04c does not reach: the collapse=0
  regime is fully alive (every INNER/LEFT/RIGHT link pins,
  `pinnedOverAPinnedSide` fires, the seam peels the spine and
  `makeRelFromJoinlist` plans the pinned two-item problems), and C-04c
  changed none of it. Nothing was deleted.
  Resume: the blocker is the ON path's Q13 plan, not the flag. Close the
  Q13 gap on the searched path (why the search prices a Merge Left Join
  over the index-only Hash Left Join at 5x the cost and still wins), then
  re-run this one-command measurement — two `estimate-audit -plan-only`
  captures on the same cluster, one per flag value — and delete the flag
  when they come back identical. Ledger `c06-collapse-flip-moves-q13`.
- [!] **C-07 P3-06 — reclassified `[~]` -> `[!]` 2026-09-07: the remaining
  half is BLOCKED, and the blocker is now NAMED and measured.** The row
  had said "blocked on C-11/C-12"; both landed, so C-07 was re-opened and
  the widening was *implemented in a throwaway worktree and instrumented*
  rather than re-argued. Result: **the widening works at the producer and
  moves no plan.** Unioning the query-pathkey columns takes the useful set
  from `[w]` to `[w x]` and `addOneOrderedIndexPath` really does add an
  `index.ordered` path (pathlist 1->2) — and plans stay byte-identical
  across five join shapes, even with `enable_seqscan = off`.
  **The real blocker is the seam, confirmed three ways:**
  `planJoinlistSearch` still returns a Node; C-12's only Node->Path
  bridge, `newPrebuiltPath`, leaves `Pathkeys` nil, so
  `pathkeysContainedIn(nil, keys)` is false and the Sort arm is the only
  arm production can take; and an instrumented `finalPath` prints
  `pathkeys=0` on every probe. C-11's `ORDERED` rel exists but has nothing
  ordered to receive.
  **Second, independent blocker found:** `addOrderedIndexPaths` runs only
  inside the PG-shaped join search, and `tryPGShapedJoinSearch` declines
  at `nrels < 2`, so `SELECT ... FROM t ORDER BY t.pk` — the canonical
  shape the widening serves — never reaches the producer at all.
  Landed: comments + tests only, zero behaviour change. Ledger
  `c07-widening-blocked-on-seam-pathkeys`.
  ORIGINAL ROW FOLLOWS. DERIVATION + GATE LANDED 2026-09-05; the "motivate
  index paths" half RE-ADJUDICATED 2026-09-07 and still BLOCKED — the
  blocker moved from C-11/C-12 (both landed, neither unblocked it) to the
  SEARCH BOUNDARY.**
  Landed: `chooseQueryPathkeys` reproduces `standard_qp_callback`'s
  precedence exactly (group ?: window ?: longer(distinct, sort) ?: setop),
  derived against the FROM-level resolve context so the pathkeys carry the
  same `Index`/`SourceTableIdx` the search's clause operands do — without
  that they would be silently unmatchable. Includes
  `transformGroupClause`'s sortClause-prefix reuse and
  `transformDistinctClause`'s ordering. `hasUsefulPathkeys` is now
  complete and PER-REL, as upstream's is; upstream's three arms reduce to
  two by PROOF (group non-empty implies query non-empty via the
  precedence's first case), not by simplification.
  **Not landed, with evidence:** widening the useful-column set so ORDER BY
  / GROUP BY motivate index paths. Nothing selects a path for its ordering
  — `finalPath()` is cost-only, `planJoinlistSearch` DROPS the chosen
  path's `Pathkeys` at the seam, and the ORDER BY `Sort` is wrapped
  unconditionally far above it in a different coordinate space. An
  ordering-only index path could therefore only lose on cost, win
  `CheapestStartup` under a LIMIT while a redundant Sort still runs, or
  silently disable `applyIndexOrderedGroupingRule`. That is unmotivated
  plan churn, so it was not forced. The consumer was filed as C-11
  (`ORDERED` upper rel) + C-12 (real upper-rel `PathSort`); the widening
  itself is a map union at one line.
  Gates: 6 new test groups incl. a DECISION test
  (`TestAddOrderedIndexPathsGateIsCompleteButGenerationIsNot`) that pins
  the gate saying yes while the producer emits nothing, and names C-11/C-12
  as the item that flips it red-to-green; optimizer suite; TPC-H values
  24/24 MATCH; plans BYTE-IDENTICAL; plan-gate PASS.
  **RE-ADJUDICATION 2026-09-07 (C-11 and C-12 both `[x]` since 2026-09-06):
  the blocker is NOT cleared, and the widening stays unlanded.** The three
  failure modes were re-checked against the current tree and all three are
  still live, and the check was a MEASUREMENT, not a re-reading — the
  widening was implemented in a throwaway worktree and instrumented:
  (i) the widening works — unioning the query-pathkey columns into
  `colExprs` takes `btg`'s useful set from `[w]` to `[w x]` on
  `… WHERE btg.w = oth.k ORDER BY btg.x`, and `addOneOrderedIndexPath`
  adds a real `index.ordered` path on the (x, y) index;
  (ii) no plan moves — not on cost and not under `enable_seqscan = off`;
  `finalPath` returns `pathkeys=0`, `planJoinlistSearch` still publishes
  `r.node`, and `createOrderedPaths` (C-12's producer) consumes a NODE
  whose only Node→Path bridge, `newPrebuiltPath`, leaves `Pathkeys` nil —
  so C-12's `upper.ordered.input` arm is unreachable from production and
  its Sort is unconditional in fact. C-11's ORDERED rel exists but has
  nothing ordered to receive.
  (iii) the `indexOrderedAggInput` regression risk is unchanged: it still
  matches on the child being a bare `*SeqScan`.
  **New, independent blocker found:** `addOrderedIndexPaths` runs only
  inside the PG-shaped join search, and `tryPGShapedJoinSearch` declines at
  `nrels < 2` (joinsearchseam.go), so a single-table
  `SELECT … FROM t ORDER BY t.pk` — the canonical shape the widening is
  meant to serve — never reaches the producer at all.
  **Unblocker, now named precisely:** a search boundary that publishes the
  chosen PATH (or at least its `Pathkeys`) instead of a bare Node. Ledger
  row `c07-widening-blocked-on-seam-pathkeys`.
  Test changes landed with this re-adjudication (comment-and-test only —
  the production diff is comments, zero behaviour change, so no cluster
  gate applies): the decision test is RENAMED and rewritten to record the
  measured verdict rather than the superseded C-11/C-12 prediction
  (`TestAddOrderedIndexPathsGateIsCompleteButGenerationStaysShut`), and a
  consumer-side twin was added that pins the blocker itself —
  `TestCreateOrderedPathsInputArmIsUnreachableFromANode` feeds
  `createOrderedPaths` a child that is ALREADY in the requested order and
  asserts it still stacks a redundant Sort. Gates: `go vet` clean,
  optimizer suite ok, `RALPH_PRECOMMIT_SCOPE=units` full pass.
  Known divergence recorded: group/sort matching is on the written
  expression after alias substitution, not PG's `tleSortGroupRef` identity,
  so `GROUP BY t.a ORDER BY a` misses the match and falls back to ASC —
  the safe direction, since a wrong-direction claim would be worse.
  Original scope note follows.
  **C-07 (original).** (`query_pathkeys =
  group ?: window ?: longer(distinct, sort) ?: setop`) + complete
  `has_useful_pathkeys` so ORDER BY / GROUP BY motivate index paths.
  *design: take3 08 §6.3; gate: take3 09 §5 P3 (PP).*
- [x] **C-08 P3-07 `param_source_rels` derivation.** Landed 2026-09-06:
  per-joinrel PG derivation (`paramSourceRelsForProblem`: RHS rule +
  FULL symmetric + lateral-0 invariant) with the frame remap rule
  (single-leaf-consecutive items else 0; RIGHT SJIs skipped);
  `s.problemItems` stamped per problem; computed once per
  `addPathsToJoinrel`, threaded to NLI + both merge arms;
  `allowStarSchemaJoin` untouched. Provably inert until C-04 (no
  searched joinrel can overlap a peeled-spine SJI's RHS).
  Gates: derivation table + admit/refuse arm pair + mapping coverage;
  optimizer + executor suites; units scope; spotcheck PASS; PP TPC-H
  22/22 MATCH (no sweep triggered per conditional gate); review
  APPROVE-WITH-NITS (5 addressed: RIGHT guard, admit test, stale
  comments, ppi_rows ledger row). Artifacts:
  `internal/optimizer/joinpathsnli.go` (derivation),
  `joinpaths{,merge,mergeouter}.go` + `joinsearch.go` +
  `relfromjoinlist.go` (threading/stamp),
  `joinpathparamsource_test.go` (new) — `b967f38`.
- [-] **C-09 P3-08 `reduce_unique_semijoins`.** DECLINED 2026-09-06
  with verification (SKIP policy 1): unique-inner SEMI has no
  structural value — reorder unavailable (pinned outside search),
  estimates already unique-aware (ledger-794 Q18 inert), exec bounded
  by early-break; plan-rewrite re-indexing risk exceeds any bounded
  gain. Resume only if SEMI enters the search or a measured exec win
  exists. Ledger `take3-C-09-declined`; record
  `docs/design/planner-c09-unique-semi/DESIGN.md` (review
  REQUEST-CHANGES → decline).
- [x] **C-10 P4-00 pre-phase scoping.** Done 2026-09-05: all four areas
  investigated against the tree and split into the items below. One result
  is negative and removes a checkbox rather than adding one. Analysis
  artifact: `analysis/planner-refactor-take3/c10-p400-scoping-20260905/README.md`.
  *design: take3 08 §7; gate: scoped items filed (take3 09 §9 reporting).*
- [x] **C-10a P4-00a grouping-sets scope + `dNumGroups` fix — CLOSED
  2026-09-07; the last condition is discharged by measurement.** Decision
  2 pinned grouping sets to AGG_HASHED *conditional on an unrun SF=1
  memory measurement of Q22/Q67*. It has now run, on the TPC-DS SF=1
  cluster (port 65436) under a cgroup cap, with per-session ANALYZE (goopg
  statistics are per-connection) and `GOOPG_ANALYZE_SEED=20260905`:

  | query | grouping-sets node | hash memory | batches | result |
  |---|---|---:|---:|---|
  | Q22 | `HashAggregate (4 keys, 5 grouping sets)` | **24.3 MB** | **1** | completes, 24.95 s |
  | Q67 | `HashAggregate (8 keys, 9 grouping sets)` | **6.6 MB** | **1** | completes, 26.01 s |

  **`Batches: 1` on every hash table in both plans — nothing spilled**, and
  the largest grouping-sets hash in the pair is 24 MB. The pin's risk was
  that hashing every grouping set would exhaust memory where PG's
  MixedAggregate/GroupAggregate would not; at SF=1 on the two queries the
  decision named, it does not come close. **The pin stands, now on
  evidence rather than on a promise.** (Peak server RSS was 5.95 / 6.79 GB,
  but that is whole-server including a 2 GB buffer pool and is not the
  figure the decision turns on — the aggregate's own memory is.)
  Measurement run late and deliberately: it was held earlier in the day
  while four peer agents held benchmark servers with 12 GB in swap, since
  a memory measurement taken under memory contention answers a different
  question.
  ORIGINAL ROW FOLLOWS. CARDINALITY
  HALF LANDED 2026-09-05.** `estimateAggregate` now SUMS
  `estimateNumGroups` over the grouping sets instead of estimating
  `GroupExprs` (the deduplicated union) once, which is what PG accumulates
  into `dNumGroups`. An N-set query was priced as one set, under-stating by
  up to N×, silently — nothing downstream could tell a rolled-up estimate
  from a plain one, and it fed C-15's `cost_agg` and C-17's
  `tuple_fraction`. Clamped to the input row count (upstream does the same;
  more output rows than input is unreachable for a grouping aggregate) and
  fail-safe on an out-of-range set index (falls back to the union answer
  rather than dropping a dimension, which would under-state further).
  Gates: 4 new tests (sum-over-sets against the single-set answers it is
  built from, plain-aggregate-unchanged, clamp, out-of-range fail-safe);
  optimizer suite; TPC-H values 24/24 MATCH, plan-gate PASS, PP unchanged
  6/14/2; TPC-DS SF0.5 PASS=95 MISMATCH=0 CKMISMATCH=0.
  **SCOPE HALF DONE 2026-09-06** —
  `docs/design/planner-c10a-grouping-sets-scope/DESIGN.md` + 3 gate tests.
  **Decision 1: keep the flat `GroupingSets [][]int`; no rollup list.**
  goopg's flat form IS PG's `RollupData.gsets` minus the rollup level, and
  every other `RollupData` field is derivable except `is_hashed` — which is
  `AGG_MIXED`, not a rollup. A rollup is the unit of ONE SORT ORDER, and
  goopg has no phases, no intra-operator tuplesort, and keys on the SET
  index — so the rollup grouping has no consumer on either side. Reversible
  asymmetrically in favour of flat.
  **Decision 2: C-15 ships NO grouping-sets arm; pin to AGG_HASHED**, on
  three conditions (move the guard into the path producer and KEEP the
  executor gate; the pin is conditional on an unrun SF=1 memory measurement
  of Q22/Q67; teach the parity tool the labels first).
  **Condition status 2026-09-07: 3rd DISCHARGED (see the parity-tool note
  below); 1st discharged by C-15 as shipped; 2nd STILL OWED** — the SF=1
  memory measurement of Q22/Q67 has still never run, and it is the only
  thing between C-10a and done. Deliberately NOT run today: it is a
  memory-exhaustion measurement, and three peer agents held ~6 GB of
  benchmark servers with 12 GB already in swap, which would both distort
  the answer and risk OOM-killing their arms. Resume: TPC-DS SF=1 (port
  65436, `bench/tpcds/runtime_goopg/data`) once the host is quiet — run
  Q22 and Q67 under the cgroup cap and record peak RSS and completion,
  since the pin hashes every grouping set where PG's MixedAggregate/
  GroupAggregate does not.
  **Two things that changed the author's mind, both verified:** retiring the
  three planner declines is NOT a wrong-answer risk — a SECOND gate in
  `aggregateOp.Open` re-tests `GroupingSets == nil`, so retiring them alone
  buys a redundant `Sort` under a node that ignores it. And "hashed is what
  PG picks anyway" is FALSE: PG picks 5 GroupAggregate + 3 MixedAggregate
  and **0 HashAggregate** on this corpus, and structurally cannot pick pure
  hashed for a ROLLUP (the empty set can't be hashed).
  **New finding, must land BEFORE C-15:** `scripts/pg-plan-parity-diff.py`
  **cannot see this divergence** — goopg's label and PG's `MixedAggregate`
  are both unknown node kinds, so every grouping-sets strategy divergence
  is mis-filed as `join-order`, and the `aggregation-strategy` bucket the
  P4 exit criterion reads is empty of the only 8 queries guaranteed to be
  in it. ~5-line fix.
  **DISCHARGED and VERIFIED END-TO-END 2026-09-07.** The fix landed as
  `0194551f6` (`AGG_KINDS` gains `MixedAggregate`; `AGG_SUFFIX_RE` strips
  goopg's `(N keys, M grouping sets)` suffix to a kind), i.e. before C-15
  as required. Verified three ways rather than by reading the code:
  (i) `pg-plan-parity-diff.py --self-test` 17/17; (ii) a synthetic pair in
  the REAL divergence shape — goopg `HashAggregate (2 keys, 3 grouping
  sets)` against PG `MixedAggregate` over the same subtree — now reports
  `aggregation-strategy=1` and names both labels, where it previously
  mis-filed as `join-order`; (iii) C-15's own run observed the bucket
  populated and MOVING (`PP aggregation-strategy 13 -> 14`), which is the
  production confirmation.
  **Corpus facts re-confirmed from the git-tracked PG fixtures (no cluster
  needed):** 12 files match ROLLUP/GROUPING SETS/CUBE, of which
  `query_0.sql` is the junk concatenation -> **11 real**; Q36/Q70/Q86 have
  no aggregate node in `bench/tpcds/plans-pg/` (the three dsqgen SKIPs) ->
  **8 measurable**; and PG's pick is **3 MixedAggregate (Q5, Q77, Q80) + 5
  GroupAggregate (Q14, Q18, Q22, Q27, Q67)**, exactly as claimed.
  **Corpus correction: 11 of 99, not 12 of 100** (the twelfth was the junk
  `query_0.sql` concatenation); measurable corpus is **8** after the three
  dsqgen SKIPs; TPC-H uses grouping sets in **zero** of 22; every corpus
  query is a single ROLLUP, so nothing needs phases.
  Original scope note follows.
  **C-10a (original).** Before C-11
  fixes the upper-`RelOptInfo` struct, decide (i) whether `GROUP_AGG`
  carries a PG-shaped rollup list or keeps the flat
  `Aggregate.GroupingSets [][]int`, and (ii) whether C-15 ships a
  grouping-sets arm or pins them to AGG_HASHED as a measured permitted
  divergence. Land the behaviour-free half now: `estimateAggregate`
  (`cardinality.go:1087`) ignores `a.GroupingSets` entirely, so an N-set
  query is priced as one set — an under-estimate up to N× feeding C-15's
  `cost_agg` and C-17's `tuple_fraction`. The four `GroupingSets != nil`
  declines (`groupagg_hashagg.go:64`, `groupagg_presorted.go:47`,
  `groupagg_indexorder.go:68`, `parallel_agg.go:117`) are C-15's retirement
  checklist — they are what currently keeps grouping sets on the only
  strategy the executor implements.
  *before C-11; cardinality half before C-15/C-17. gate: EA ratchet on the
  12 TPC-DS grouping-sets queries + values unchanged. ~60–150 LOC, medium.*
- [-] **C-10b P3-09 `remove_useless_joins` — DROPPED on a corpus census +
  a hand A/B, 2026-09-07. One removable join in 121 queries, and removing
  it by hand buys nothing.**
  Scoping result (unchanged): this touches **no** Phase-4 item, so it is
  not a P4-00 blocker. PG runs it below the upper planner entirely,
  changing the joinlist; none of C-11…C-18 reads or produces one. goopg has
  no analogue, but both halves of the primitive exist — `joinkeyproof.go:56
  uniqueKeyColumnSets` is `rel_is_distinct_for` for the base-relation case,
  and `pathindexonlyneed.go`'s needed/output name sets answer
  "unused above", both decline-biased.
  **Census (all 22 TPC-H + all 99 TPC-DS queries, against PG 18.3's rule at
  `postgres/src/backend/optimizer/plan/analyzejoins.c:155
  join_is_removable`).** TPC-H: **1** LEFT join in the whole suite (Q13
  `customer LEFT JOIN orders ON c_custkey = o_custkey`); it fails the
  uniqueness test (`orders`' PK is `o_orderkey`) AND the unused-above test
  (`count(o_orderkey)` is in the select list) → **0 removable**. TPC-DS:
  **21** LEFT joins (q5, q40, q49×3, q72×2, q75×3, q77×2, q78×5, q80×3,
  q93) plus 2 FULL joins rejected at `:169`. **All 21 pass the uniqueness
  precondition** — the sales↔returns joins are on the complete returns PK
  (`third-party/tpcds-postgres/…/tools/tpcds.sql`), and q77/q78's CTE inner
  sides are `GROUP BY` subqueries whose grouping columns are exactly the
  join keys (`query_is_distinct_for`, `:1060`). **Exactly one passes the
  unused-above precondition** (`:214`, `bms_is_subset(attr_needed,
  inputrelids)`): **q72's `left outer join catalog_returns`**. Corroborated
  independently against the committed PG plans — a table in the SQL but
  absent from `bench/tpcds/plans-pg/` is a candidate removal, and after
  discounting three `SKIP` files and three alias/literal false positives
  (`item` in q49, `store` in q51/q76), `catalog_returns` in `Q72.txt` is
  the **only** genuine one, in either corpus. Self-join removal
  (`:2488`): no candidates (q72's `d1/d2/d3` join on `d_week_seq`, not the
  PK).
  **The A/B on that one join** (private SF0.5 clone, port 5536, binary
  `9a4b464be45d079e` off clean `d0b4f96e4`, 3 interleaved reps): arm A =
  Q72 as written, arm B = Q72 with the `catalog_returns` line deleted, i.e.
  the exact rewrite the optimisation performs. **Arm B is 0.2–0.6 %
  SLOWER**: 4605.6/4794.4/4879.9 ms (A) vs 4633.2/4806.0/4901.2 ms (B), a
  monotone drift with server age, so the true delta is **0 within noise**.
  Values byte-identical, 100 rows, md5 `640163d4d519bf741d01c170c4b680ab`
  both arms — which also empirically confirms the census's uniqueness
  proof. The reason there is nothing to win is visible in the plan: goopg
  executes the join as a `Nested Loop Left Join` over **749** outer rows
  probing `catalog_returns_pkey`, above a `catalog_sales` seq scan that
  reads 720,657.
  **Does the C-09 decline transfer? Mostly NO, and saying so is part of the
  verdict.** `docs/design/planner-c09-unique-semi/DESIGN.md` (ledger
  `take3-C-09-declined`) declined unique-inner SEMI on two grounds that do
  **not** apply here: (i) "reorder unavailable" — join REMOVAL needs no
  reorder, it deletes a rel outright and strictly SHRINKS the search space;
  (ii) "estimates already unique-aware" — deleting a provably-unique inner
  cannot move a row estimate at all, so there is no estimate to be already
  correct. C-10b is refuted on an independent ground: **corpus incidence**.
  What the two declines share is only the shape of the evidence — a real
  PG optimisation with no witness in goopg's benchmarks.
  **No change is needed in `joinkeyproof.go`** (peer-owned for C-20a): the
  binding precondition is never uniqueness. 21 of 21 candidates prove
  unique and 20 of 21 die at `attr_needed` — so the missing half is a
  per-relation needed-set, which goopg does not have (`pathindexonlyneed.go`
  is a whole-statement NAME set, and a name-based approximation is safe
  only because it is decline-biased; memory
  `goopg_optimizer_no_attr_needed_no_ios_path`).
  **What would change the verdict:** a workload where a query joins a
  lookup/detail table it does not project — the ORM- and view-generated
  shape this optimisation exists for (PG's own motivation: a view
  left-joins a table that a given query never selects from). Hand-written
  benchmark queries project or filter nearly every table they join, which
  is precisely why the incidence is 1 in 121. Ledger
  `take3-C-10b-dropped`.
  *after C-04, beside C-09. gate if ever resumed: P3 PP + a forced fixture
  + byte-identical values. ~200–350 LOC, low-medium.*
- [x] **C-10c P4-00c outer-join qual-placement contract for upper rels.**
  Landed 2026-09-05: `docs/design/planner-c10c-upper-qual-placement/DESIGN.md`
  + 5 fixture tests. `reduceOuterJoins` confirmed to need NO Phase-4 change
  (prep pass, one call site, finished before any path exists), with the
  consequence stated once for all of Phase 4: **every OJ link a Phase-4
  item can see has already survived demotion, so there is no strictness
  escape hatch left — only placement.**
  Per-item re-assert table produced for C-11/C-12/C-13/C-15/C-16/C-17/C-18,
  each naming the PG equivalent and the goopg site. Two corrections to the
  scoping doc's loose pairing: **C-16 retires no arm at all** (there is no
  `*Distinct` case in the pass — so it is the one item with no arm-shaped
  reminder, and its duty is the negative one of introducing no distinct
  input target), and `*Limit` was reassigned to **C-13**.
  The load-bearing finding: PG's guard for the target half is the
  PlaceHolderVar, and **goopg has none** — so goopg's only guard is "do not
  evaluate below the link", and row counts never move when it is broken, so
  every existing gate stays green through the bug.
  Negative controls run and reported (guard inverted → 4 tests red; guard
  dropped → 2 red; C-15's applying cut simulated → the tripwire fires with
  a message naming the design section). Control 2 is the informative
  asymmetry: dropping the guard is INVISIBLE to the two consumer tests,
  which is exactly why this item is a fixture rather than a code change.
  Recorded blind spot: the tripwire calls `stampAggregateInputTarget`
  directly, so a C-15 that applies its cut in a NEW function will not fire
  it — which is why the table names a per-item re-assert site and C-15's
  own gate must stay a values-diff on both suites.
  `reduceOuterJoins` itself needs no change — it is a prep pass at
  `planner.go:2716`, before deconstruction, and C-01/C-02 already document
  their dependence on it. The Phase-4 obligation is the CONSUMER side:
  `pushSingleSideQualsIntoInnerJoinInputs` descends through `*Aggregate`,
  `*WindowAgg`, `*Sort` and `*Limit` arms that C-11/C-15/C-16/C-18 delete,
  and it is the sole consumer of the `delayedAboveOJ` oracle. Name, per
  Phase-4 checkbox, which arm retires and where the delay test is
  re-asserted in the replacement, and pin a fixture where narrowing an
  upper target across a LEFT link would be wrong — so C-15's applying cut
  cannot silently drop the guard.
  *before C-11; fixture before C-15. gate: red-then-green fixture + P4
  values-diff. doc + ~80–150 test LOC, low risk, buys off a high one.*
- [x] **C-10d P4-00d FROM-subquery pull-up — DECIDED 2026-09-07: the
  boundary is PERMANENT; `pull_up_subqueries` is NOT ported in this
  workstream.** The row said the decision was the owner's; here it is,
  with the reason it is not a close call.
  **The measurement refutes the port's own structural justification.** A
  full `pull_up_subqueries` port removes **18 of 46** ABOVE-BLOCKING
  boundaries (39%), leaving **28**. So C-11's upper rels had to support
  boundary-crossing construction REGARDLESS — the port is an optimisation
  on top of that support, never an enabler of it. Paying ~400-700 LOC of
  high-risk change that moves the values of ~46 corpus queries, to
  eliminate 39% of a problem you must solve anyway, is the wrong order.
  **And the decision has in effect already been taken by what shipped:**
  C-11 (P4-02) and C-12 (P4-03) are both landed and both treat the
  boundary as permanent — `relfromjoinlist.go` documents it as deliberate
  and ledgers its two costs (no differently-sorted path for the
  sub-problem; priced for "produce all rows" so an outer LIMIT cannot
  reach in). Reopening that assumption now would re-litigate two done
  items for a fraction of a problem they already handle.
  **The caveat is recorded, not buried:** Q9 — P4-01's own witness — IS in
  the pullable set, putting its entire 6-way join tree inside a derived
  table (verified against the AST, not assumed: 6 inner leaves, sole FROM
  item, outer GROUP BY + aggregate + ORDER BY). So a real, unmeasured
  plan-quality case for the port exists on exactly one high-value query.
  **Successor filed rather than folded in:** port `pull_up_subqueries` as
  its own item, judged on its own measured plan-quality evidence starting
  from Q9, not on the structural argument this measurement refuted. Note
  the scope correction this census also established — `RangeVar.Subquery`
  is only ONE of THREE routes to the same opaque prebuilt leaf; WITH-list
  CTEs (70 in 30 of 99 TPC-DS queries, 61 in a join tree) and views are
  the others, so a pull-up that handles only FROM-subqueries covers less
  of the corpus than the headline count suggests.
  Census: `analysis/planner-refactor-take3/c10d-boundary-census-20260905/README.md`.
  ORIGINAL ROW (measurement, retained verbatim): MEASUREMENT DONE 2026-09-05;
  the decision is the owner's.** AST-derived census (goopg's own parser, not
  a regex — the ~41/100 figure was regex-derived and flagged):
  TPC-H **5** boundaries in 5 queries, TPC-DS **89** boundaries in 41 of 99
  queries. The query-level count was right; the useful number is that only
  about half the boundaries sit where they hurt.
  **ABOVE-BLOCKING** (derived table holds a join tree AND the outer level
  carries grouping/ordering/window/aggregate): TPC-H 4, TPC-DS 42. Of those,
  the true Q9 shape — derived table is the outer query's SOLE FROM item, so
  the whole join search lives one scope down — is TPC-H 4, TPC-DS 24.
  Q9 verified, not assumed: 6 inner leaves, sole FROM item, outer GROUP BY +
  aggregate + ORDER BY. Two corrections: **q22 is NOT ABOVE-BLOCKING**, and
  **q13 is ABOVE-BLOCKING but not pullable** (its own GROUP BY — PG would
  not pull it up either).
  **The deciding number: a full `pull_up_subqueries` port removes 18 of 46
  ABOVE-BLOCKING boundaries (39%), leaving 28.** So C-11's upper rels must
  be boundary-crossing by construction REGARDLESS, which makes the port an
  optimisation on top of the struct rather than an alternative to it.
  Recommendation (not a decision): declare the boundary permanent for
  C-11's struct, file the pull-up separately. Caveat against it: Q9, which
  is P4-01's own witness, IS in the pullable set.
  **Scope correction the item needs:** `RangeVar.Subquery` is one of THREE
  routes to the same opaque prebuilt leaf — WITH-list CTEs (70 in 30 of 99
  TPC-DS queries, 61 with a join tree) and views (a fresh top-level
  planning run, harder) are the others. A pull-up at the subquery site
  covers only the first, which changes the item's "~46 queries" sizing.
  Costs of the permanent boundary to C-12 (must always stack a full Sort),
  C-13 (outer LIMIT cannot push a bound in — and nearly every TPC-DS query
  ends `order by … limit 100`) and C-17 (the fraction stops at the
  boundary, so "end-to-end" means "within one scope") recorded.
  Artifact:
  `analysis/planner-refactor-take3/c10d-boundary-census-20260905/README.md`.
  Remaining: the decision itself.
  Original scope note follows.
  **C-10d (original).** The structural one. goopg has **no** `pull_up_subqueries`: a
  derived table is planned as an opaque sub-problem admitted as one
  prebuilt leaf (`planSubqueryRangeVar`, `planner.go:4295`), and there is
  no `SubqueryScan` node. TPC-H 5/22 queries carry a FROM-subquery and
  TPC-DS ~41/100 (the TPC-DS count is regex-derived and must be re-derived
  from the AST before it enters a design doc). The shape matters more than
  the count: **Q9 — P4-01's own witness — puts its entire 6-way join tree
  inside the derived table**, so the scan/join rel C-11's upper rels must
  sit above is one planning level down. Decide before C-11 whether upper
  rels may cross a foreign planning scope or goopg grows a pull-up.
  `relfromjoinlist.go:26-29` already ledgers what the boundary costs
  C-12/C-13/C-17 (no differently-sorted path, priced for "produce all
  rows", so an outer LIMIT cannot reach in).
  *before C-11 AND before P4-01's applying cut — the tightest ordering
  constraint of the four. gate: measurement filed; if the port is chosen,
  values-diff both suites (it moves ~46 corpus queries at once).
  measurement doc-only; the port would be ~400–700 LOC, high risk.*
- [x] **C-11 P4-02 upper `RelOptInfo`s** (`GROUP_AGG`, `WINDOW`,
  `DISTINCT`, `ORDERED`, `FINAL`) with pathlists. DESIGNED 2026-09-06:
  `docs/design/planner-p4-upper-rels/DESIGN.md` §1/§3 — LANDED 2026-09-06
  (`fcf049b05`) INERT (registry + `fetchUpperRel`, no producer, no
  consumer); three decisions settled there (registry is per planning SCOPE
  not per `searchCtx`; `Relids = 0` so DPPATH renders `relids=-` with no
  format change; upper rels stay OUT of `relMap`/`joinrels`). Load-bearing
  extra duty DONE in the same cut: `sizeUpperRelFromNode` populates
  `Rows`/`Width`/`NCols`/`AvgVarBytes` from the input Node (a zero `NCols`
  would silently suppress `costSortRun`'s external-merge arm — DESIGN
  §4.3); `AvgVarBytes` is a schema-derived per-type-width heuristic,
  PROVISIONAL per the C-12 review (Probe P1 has no witness in either
  corpus: 0/100 TPC-DS sorts spill).
  *design: take3 08 §7 + `docs/design/planner-p4-upper-rels/DESIGN.md`;
  gate: take3 09 §5 P4 (PP `changed=0`).*
- [x] **C-12 P4-03 real upper-rel `PathSort`** (has a `createPlanNode` arm;
  today only ever a merge-join child, never competing with a hashed
  alternative). DESIGNED 2026-09-06:
  `docs/design/planner-p4-upper-rels/DESIGN.md` §4/§5 — LANDED 2026-09-06
  (taken over: `upperordered.go` `createOrderedPaths`/`addOrderedPaths` +
  two `planner.go` ORDER BY sites, option (b) `PathPrebuilt` seed; reuses
  `sortPathFor` verbatim instead of a `sortPathOver` sibling —
  behavior-identical, review-agreed). Agent review APPROVE-WITH-NITS, no
  blockers (MaybeAddGather re-verified parallel-neutral by reading;
  AvgVarBytes third-answer deviation recorded provisional; redundant
  seed.Rows line dropped). Gates, all on an isolated HEAD+C-12-only
  worktree A/B so concurrent C-05/C-04b WIP cannot contaminate: TPC-H
  structural plan-gate 22/22 MATCH, costs mode 17 queries move on Sort
  lines ONLY (34/34 changed lines `Sort (cost=…)`; Q18 1.5M-row spill
  priced UP 2.07M→2.41M — the §5.7 negative did not occur),
  `tpch-runner -digest` vs pre-cut binary + `-diff` VERDICT: PASS 24/24,
  ten longest TPC-H queries no slower (max ratio 1.05), TPC-DS SF0.5 sweep
  PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 (Q78 17 s 45 rows
  ck-verified), TOTAL +2.5% (within ±17%), cost-normalized TPC-DS plans
  byte-identical (shape changed=0). Re-pinned in-commit:
  `plan_snapshots/c12-ordered-sort-20260906.txt`. C-12a (second candidate
  via C-07 widening) explicitly NOT flipped — filed as the next cut.
- [x] **C-13 P4-04 bounded / top-N sort** (`cost_sort`'s `limit_tuples`
  arm) — the largest recorded `ORDER BY … LIMIT` win. DESIGNED 2026-09-06:
  `docs/design/planner-p4-upper-rels/DESIGN.md` §6 — **RE-SCOPE into two
  cuts** (C-13a DEFERRED per Probe P2 NO-GO below; C-13b LANDED above as a
  cost-model correctness item with NO timing claim): C-13a the executor bound (`sortOp` has none; `parallel.go:294`
  says so) which carries the whole runtime claim and depends on NEITHER
  C-11 nor C-12, and C-13b `cost_tuplesort`'s middle branch, which needs
  C-12. Planner plumbing is one line: `preprocessLimit` already returns
  `limitEstimates{count,offset}` and `searchTupleFraction`
  (`joinsearchseam.go:690`) discards it; C-17 is NOT needed (C-13 wants the
  absolute bound at one rel, not the fraction everywhere).
  **Corpus correction: goopg's TPC-H suite is HammerDB's templates and has
  NO `LIMIT` in any of the 22 queries** (0 of 22 committed PG plans contain
  a `Limit` node), so the entire measurable surface is TPC-DS (81/99 plans
  `Limit`-rooted).
  **Probe P2 RUN 2026-09-06 — the go/no-go came back NO-GO for C-13a:**
  `analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/`
  (`EXPLAIN ANALYZE` over all 99 SF0.5 queries at `00688e96c`, 99/99 rc=0).
  The structural hypothesis is CONFIRMED — goopg stacks **77** bindable
  `Limit → Sort` pairs against PG's 54, counted by the same tool — and it
  buys nothing: **54 of the 77 sort an aggregate/window output**, so the
  median actual input is **145 rows**, the largest is 324 249 (Q51, 1.9 ms),
  and the **total** sort time across all 77 is **≤ 119.8 ms of 802 s
  (0.015 %)**. **0 of 100 sorts in the corpus spill** (largest footprint
  26 MB = 10 % of the 256 MiB `sortChunkBytes`), so the design's strongest
  argument — a bound removes the spill outright — has no witness at all.
  Estimates could not have answered this: goopg's `Sort` row estimates on
  these nodes are wrong by 789× (Q22), 8007× (Q99) and 245 587× the other
  way (Q78).
  **Dispositions:**
  **C-13a — DEFERRED** (not cancelled, not wrong): cheap and correct, but
  neither corpus can measure it; reconsider when a top-N-over-raw-rows
  corpus exists (`ORDER BY <ungrouped col> LIMIT k` over a large scan/join),
  which TPC-H (no LIMIT) and TPC-DS (LIMIT over grouped output) both lack.
  Note the gate that looks like it covers C-13a does not: the SF0.5 harness
  reports `ck=n/a` for exactly the LIMIT-saturated queries C-13a touches.
  **C-13b — LANDED 2026-09-06** (`costSortRun` gains `limit_tuples`:
  output selection, disk branch keyed on OUTPUT bytes with input-sized
  page/run math, bounded `N log2 K` arm continuous at `tuples == 2*output`,
  run cost on input tuples; `sortPathForBounded` split keeps the merge side
  byte-identical; `orderLimitTuples` plumbed to the normal ORDER BY arm,
  SRF arm `-1`, WITH TIES declines conservatively, literals-only resolution
  skipping). Agent review APPROVE-WITH-NITS (formula exactly faithful;
  WITH TIES comment corrected to cite the executor; literal pre-check
  added after proving no-folding; resolver + disk-identity pins added).
  Gates on isolated HEAD+C-13b-only A/B: optimizer suite + units precommit
  green; TPC-H structural AND costs plan-gate **22/22 MATCH** (no LIMIT in
  suite — byte-identical numbers, the strongest pin a cost change can get
  there); `tpch-runner -digest` vs pre-cut binary + `-diff` VERDICT: PASS
  24/24, ten longest no slower; TPC-DS SF0.5 sweep PASS=95 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0, TOTAL **-1.0%**, cost-normalized plans
  byte-identical (18 queries move cost-only on Limit-rooted Sorts +
  Limit/Gather rollups; bounded Sorts price DOWN, e.g. 1485→1133 on
  11 940 rows). NO timing claim per the re-scope. No re-pin: the TPC-H
  baseline is unchanged byte-for-byte (TPC-DS plans are untracked).
  **C-14 is not the better item here either**: goopg's Q67 sort — PG's own
  Incremental Sort case — is 1.0 ms over 115 150 rows. General finding: no
  sort-side item has a runtime witness in this corpus; all sorting in all 99
  queries is under 0.2 % of corpus wall time.
  *design: take3 08 §7 + `docs/design/planner-p4-upper-rels/DESIGN.md` §6.6
  (which prescribed this outcome in advance);
  measurement: `analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/`;
  gate when resumed: take3 09 §5 P4 (PP + timing; the SF0.5 checksum arm is
  NOT a correctness gate for this item — see above).*
- [-] **C-14 P4-05 Incremental Sort** node + `create_incremental_sort_path`
  — **DROPPED on measurement 2026-09-07. Out of scope for both corpora.**
  The item's stated blocker is GONE: E-15 (`065182d` + `c7f1f45`) published
  the presorted-prefix executor input contract (`sortPrefixEqual` + an
  order-equivalence oracle, `docs/design/executor-ex3-07-presorted-contract/DESIGN.md`),
  which is exactly what broke the C-14/E-03 circular wait. So this is no
  longer "blocked"; it is a build-or-drop call, and the measurement says
  drop.
  **The witness, re-measured at `d0b4f96e4` on a private SF0.5 clone
  (port 5536, cgroup-capped, 3 reps): Q67 — PG's OWN Incremental Sort
  case — sorts in 1.26 ms.** `Sort.actual_start − WindowAgg.actual_end`
  = 1.259 / 1.312 / 1.247 ms across three reps, over **376,552 rows**,
  `quicksort Memory: 553kB`, no spill, against an Execution Time of
  15,234 / 14,368 / 13,547 ms — the sort is **0.008 % of the query**. This
  is a STRONGER refutation than the census's (1.0 ms over 115,150 rows,
  `analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/`):
  3.3× the input rows and it is still ~1 ms, because
  `sortOp.keyvals` (M0134-0191) already precomputed the keys. An
  incremental sort replaces at most that 1.26 ms, and only partially.
  Corpus-wide the census bounds it further: all sorting in all 99 TPC-DS
  queries is **≤ 119.8 ms of 802 s = 0.015 %**, median sort input **145
  rows**, and **0 of 100 sorts spilled** — so BOTH of PG's mechanisms for
  Incremental Sort have no witness here: the early-rows-under-a-bound
  mechanism (TPC-H has **no `LIMIT` at all** — re-verified: zero `limit`
  in all 25 files of
  `analysis/tpch/goopg-pg-tpch-plan-compare-260718/queries/`) and the
  memory/spill-avoidance mechanism (nothing spills; largest footprint
  26 MB against a 256 MiB `sortChunkBytes` threshold).
  **What would change the verdict** (and the only thing that should
  reopen this row): a corpus containing a large `ORDER BY a, b` whose `a`
  is ALREADY sorted by the input path — an index-ordered or merge-join
  child — with a small `LIMIT` on top, i.e. top-N over raw (ungrouped)
  rows. Neither benchmark has that shape: 54 of 77 bindable TPC-DS sorts
  read already-collapsed aggregate or window output, and TPC-H has no
  bound at all. Plan-parity is not sufficient grounds on its own — PG's
  Q67 Incremental Sort buys goopg 1.26 ms.
  **This resolves E-03**, which is `[!] file ONLY if planner C-14
  activates`: C-14 does not activate, so E-03 is closed with it. E-15's
  contract stays landed and inert (zero production callers), which is the
  correct residue — it is what a future C-14 would build against.
  Ledger `take3-C-14-dropped`.
  *design: take3 08 §7; measurement:
  `analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/README.md`
  §3 + the 3-rep Q67 re-measurement above; gate if ever resumed: take3 09
  §5 P4 (PP).*
- [x] **C-15 P4-06 `create_grouping_paths`** (sorted + hashed agg priced by
  `cost_agg`; hash spill arm OMITTED — executor has no agg spill path);
  three aggregate rules retired. DESIGNED 2026-09-06:
  `docs/design/planner-p4-grouping-paths/DESIGN.md` (review
  REQUEST-CHANGES → 6 blockers fixed → APPROVE). LANDED 2026-09-07
  (`groupingpaths.go` producer + `PathAgg` arm + `costAgg`, rules deleted
  with seeds/helpers surviving as candidate builders). Code review
  APPROVE-WITH-NITS, all addressed (spill prose, stale rule comments,
  `scanShapeDisabled` restored, C10c test non-vacuous). Gates on isolated
  HEAD+C-15-only A/B: optimizer+executor suites + units precommit green;
  TPC-H structural 21/22 MATCH (Q18 access-path-only move, same numbers —
  PG-tiebreak-conformant startup win on tied legacy input prices, B-15
  witness, timing 0.96), costs moves are presorted-Sort pricings + rollups
  (+ Q17 0.01→0.00 stats noise); `tpch-runner -digest` vs pre-cut binary +
  `-diff` VERDICT: PASS 24/24, ten longest no slower; TPC-DS SF0.5 sweep
  PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, TOTAL +1.6%,
  cost-normalized plans byte-identical (4 cost-only moves on
  Limit-rooted Sorts/Aggs, all checksum-verified); PP
  aggregation-strategy 13→14 (Q18 only, direction documented — P4 exit
  criterion tracks the whole phase, grouping-sets residuals ledgered).
  Negative fired as written: spill arm drove Q3/Q10/Q13/Q18 away from PG
  (Q13 5.67 s→8.71 s) → arm DELETED, omission pinned by test, resume named.
  Re-pinned in-commit: `plan_snapshots/c15-grouping-paths-20260907.txt`.
  *design: take3 08 §7 + `docs/design/planner-p4-grouping-paths/DESIGN.md`;
  gate: take3 09 §5 P4 (PP + timing).*
- [x] **C-16 P4-07 `create_distinct_paths`** (hashed / sorted /
  unique-over-sorted). Depended on landed P1-25. DESIGNED 2026-09-07:
  `docs/design/planner-p4-distinct-paths/DESIGN.md` (review
  REQUEST-CHANGES → 2 blockers fixed → APPROVE-WITH-NITS → fixed).
  LANDED 2026-09-07 as hashed + unique (no third sorted-Distinct: identical
  price/order to Unique, dies as duplicate): DISTINCT upper rel +
  `PathDistinct` (+ `Unique` bool) + arm emitting `*Distinct` vs
  all-columns `DistinctOn` (`distinctOnOp` already streams, both render
  `"Unique"` — ~0 executor LOC); `enable_hashagg=off` disables (never
  skips) hashed; DISTINCT ON gated at both wrapper sites
  (defense-in-depth); min/max site threaded. Code review
  APPROVE-WITH-NITS, all addressed (`replaceSingleChild` DistinctOn arm,
  `distinctAllKeyCols` single-sourced, prose swept, tests de-vacuoused).
  Gates on isolated HEAD+C-16-only A/B: optimizer+executor suites +
  units precommit green; TPC-H structural+costs 22/22 MATCH, digest/diff
  PASS 24/24, timing flat; TPC-DS SF0.5 sweep PASS=95 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0, TOTAL +0.1%, shape 99/99 same; Q38
  GUC-off Unique live on 3 DISTINCT legs with values 38/38; PP identical.
  No re-pin (zero moves). `*Unique` node explicitly NOT built (reuse
  adopted after review killed the EXPLAIN-parity premise).
- [x] **C-17 P4-08 `tuple_fraction` end-to-end** (every upper rel, not only
  the join search). LANDED 2026-09-07 after C-18 made "every upper rel"
  enumerable. Four gaps, found by census rather than by symptom:
  (i) `ctx.tupleFraction` was stamped in TWO per-arm places (the `WHERE`
  arm and the outer-link arm), so a WHERE-less statement —
  `SELECT … FROM t ORDER BY a LIMIT 10` — reached every upper rel claiming
  all rows were wanted; it is now stamped ONCE at the convergent block every
  FROM arm passes through, which is upstream's position (`grouping_planner`
  folds LIMIT/OFFSET at its top, planner.c:1451, before `query_planner`).
  The join search is unaffected by the move: both arms that call
  `tryJoinSearch` already stamped it. (ii) the SETOP producer and (iii) the
  min/max escape hatch's DISTINCT producer were handed a literal `0`.
  (iv) a set-op statement's trailing ORDER BY was a bare `&Sort{}` with NO
  ORDERED upper rel at all — the last top-level sort still priced at zero
  (pre-C-12 state) and the one shape that could not reach C-13b's bounded
  arm; it now goes through `createOrderedPaths` with the chain's own fraction
  and `limitTuplesForOrderedSort` bound.
  **Neither corpus witnesses it, which was predicted, not discovered**:
  TPC-H has zero set operations, zero window functions and a WHERE on every
  query; TPC-DS's UNIONs all sit inside subqueries whose ORDER BY belongs to
  the OUTER select, so `wrapSetOpSortLimit` never sees one. A direct probe on
  the TPC-H cluster is therefore the witness, and it is unambiguous —
  `lineitem UNION ALL orders ORDER BY 1`, 2 000 672 rows: with `LIMIT 10` the
  Sort's startup falls 344 423.57 → 178 266.51 (C-13b's bounded heap arm,
  `N log2 2K`, firing for the first time on this shape), and WITHOUT a LIMIT
  it RISES 344 423.57 → 433 323.57 (`costSortRun`'s external-merge arm over
  a rel that is now sized — the spill the bare `&Sort{}` never charged).
  Both directions are the correction, not a regression: the pricing is now
  the same `cost_tuplesort` every other sort in the tree pays.
  Gates on isolated C-18+C-17-only A/B: optimizer+executor suites + vet +
  `RALPH_PRECOMMIT_SCOPE=units` green; TPC-H plan capture byte-identical,
  digests byte-identical 24/24, plan-gate exit 0, PLAN-PARITY 5/15/2
  unchanged, timing +1.5% on identical plans (noise); TPC-DS SF0.5 sweep
  PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, and the raw plan
  capture (COSTS INCLUDED, not shape-normalised) is BYTE-IDENTICAL to
  C-18's, 0 diff lines. No re-pin. Pinned by a static census
  (`tuplefraction_upper_test.go`): no producer may be called with a literal
  0, `ctx.tupleFraction` may be assigned exactly once, and the producer list
  itself is checked against every `func create*Paths` in the package so the
  census cannot silently narrow.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [x] **C-18 P4-09 `create_window_paths`** + set-operation paths, priced.
  DESIGNED 2026-09-07: `docs/design/planner-p4-window-setop-paths/DESIGN.md`
  (written by an agent that ran out of session before any code). LANDED
  2026-09-07 (`windowsetoppaths.go`): the WINDOW upper rel takes ONE
  `PathWindow` per spec group stacked into a single candidate and
  `add_path`ed once (`create_one_window_path`, planner.c:4620 — one path per
  chain, not per group), priced by `costWindow` = `cost_windowagg`'s three
  per-input-row terms PLUS the internal sort, which PG stacks as a separate
  `create_sort_path` and goopg's `windowOp` performs inside the node; the
  SETOP upper rel takes one `PathSetOp` over TWO seeds (the only two-input
  upper candidate in the tree), priced by `cost_append` when the executor
  streams (UNION ALL) and by `create_setop_path`'s hashed arm
  (pathnode.c:3849) when it buffers — `setOpStreams` mirrors `newSetOp`'s own
  predicate so price and operator cannot disagree. Both single-candidate by
  construction: goopg's window executor sorts internally (no presorted
  variant to offer until C-14) and its set-op executor has one form per node.
  **Two DESIGN deviations, both deliberate.** (i) The design named three
  `*SetOp` sites; only `applySetOp` is a set operation — the partition- and
  inheritance-expansion fan-outs reuse the `*SetOp` NODE but are PG
  APPENDRELS below the upper-rel pipeline (`add_paths_to_append_rel`), and
  filing them on the SETOP rel would transcribe a node-reuse coincidence as
  a PG structure; they are untouched. (ii) The design's
  `costSortRun(cp, rows, nkeycols, 0, -1)` was wrong in its third argument:
  `ncols` sizes one ROW for the external-merge arm, so the key count would
  model a 2-column row and suppress the disk charge — the input's real
  column count and `AvgVarBytes` are passed, pinned by
  `TestCostWindowSortTermUsesRowWidthNotKeyCount`. **One real defect found in
  implementation**: `fetch_upper_rel(SETOP, 0)` shares ONE rel across a
  chain, so `A INTERSECT B EXCEPT C` had the outer node's
  `getCheapestFractionalPath` answer with the inner node's candidate and the
  executor's set-op precedence suite went red (wrong rows, not a cost move).
  PG keys its SETOP rel by the node's relids (prepunion.c:805);
  `newUpperRelForNode` allocates one rel per node with a synthetic
  distinctness key, since goopg has no relids above the seam.
  Gates on isolated HEAD(866e6fe7e)+C-18-only A/B: optimizer+executor suites
  + vet green; TPC-H plan capture byte-identical, digests byte-identical
  24/24, plan-gate exit 0, PLAN-PARITY 5/15/2 unchanged, timing −0.2%
  (139.4 s → 139.1 s; Q9 +70.9% on an IDENTICAL plan — a known-volatile
  query, 4.16/7.16 s across arms of the same binary); TPC-DS SF0.5 sweep
  PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan shapes 99/99 same.
  No re-pin (zero moves). As designed, NOTHING observable moved: the prices
  are selection-neutral (one candidate) and display-invisible (`*WindowAgg`
  and `*SetOp` carry no `PlanCost`) — the gate's PASS condition is shape
  identity, not cost movement.
  *design: take3 08 §7 + `docs/design/planner-p4-window-setop-paths/DESIGN.md`;
  gate: take3 09 §5 P4 (PP).*
- [x] **C-19a P5-01 `consider_parallel` per rel.** Landed 2026-09-06
  (`considerparallel.go`): `set_rel_consider_parallel` per base rel (temp,
  virtual catalog, TABLESAMPLE args, CTE, VALUES, SRF proparallel+args,
  subquery `limit_needed`, baserestrictinfo, sublinks, exec params) and
  `build_join_rel`'s "both inputs AND the join's own clauses" propagation;
  every path stamped `ParallelSafe` from its rel. Review
  APPROVE-WITH-NITS found the safety classifier **fail-OPEN in four
  places**, all fixed in the same commit with 16 new pins: `ScalarFuncScan`
  and the `Pg*` catalog-SRF leaves fell to a default arm whose subtree walk
  read "no children" as "nothing unsafe"; a subquery leaf's OWN expressions
  were never checked (`FROM (SELECT nextval('s') …) q` passed);
  `random`/`random_normal`/`setseed` (proparallel 'r') were missing and a
  schema-qualified `pg_catalog.nextval` bypassed the table; index/bitmap
  leaves keep their predicate in `Key/LowKey/HighKey/BitmapQual`, not a
  Filter, so `WHERE id = nextval('s')` as an index key passed. The subtree
  walk now REFUSES any node `parallelChildren` does not model rather than
  descending past it. Three further review items ledgered
  (`take3-C-19ab-review-deferred`) for C-19c/d, where they first bite.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP + serial control).*
- [x] **C-19b P5-02 `create_plain_partial_paths` populating
  `PartialPathlist`.** Landed 2026-09-06 in the same commit: a partial seq
  scan is a real `Path` in `PartialPathlist`, sized by
  `compute_parallel_worker`'s log3 ladder and `max_parallel_workers_per_
  gather` cap (pinned equal to the post-pass twin), priced by
  `cost_seqscan`'s `parallel_workers > 0` arm (CPU ÷ `get_parallel_divisor`
  with the 0.3 leader contribution, disk NOT divided,
  `clamp_row_est(rows/divisor)`). **Serial control arm unchanged by
  construction**: `PartialPathlist` has no reader but `addPath` and the
  trace, `finalPath` reads `Pathlist` only, and `TestPartialPathIsNever
  TheFinalPath` cannot pass vacuously (it fatals on zero partial paths).
  Gates (pinned regime, fresh capped server per arm): TPC-H values **24/24
  MATCH**; plan capture **BYTE-IDENTICAL** to HEAD; `make plan-gate`
  **22/22 structural AND `MODE=costs`**; TPC-DS SF0.5 **PASS=95 MISMATCH=0
  CKMISMATCH=0**; optimizer suite green. (An earlier unpinned capture pair
  the implementing agent took showed a Q9 shape move; re-captured under the
  pinned seed on both binaries it is 0 lines — the instrument, again.)
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP).*
- [x] **C-19c P5-03 parallel eligibility for plain index scans.** LANDED
  `dbb14ca25`. `drivingScan` now admits a plain IndexScan; partial index
  paths are priced through `compute_parallel_worker`'s `index_pages` arm,
  which needed `min_parallel_index_scan_size` (PG's 512kB = 64 blocks)
  plumbed as `PlannerSettings.MinParallelIndexScanSize`.
  `IndexScan.Parallel` mirrors `Plan.parallel_aware`, stamped by
  `stampParallelScan` and printed by both EXPLAIN arms; workers claim
  index LEAF BLOCKS from the shared `parallelIndexScanState` (the
  plain-scan sibling of M0134-0189). Gated combined with E-09a/E-11:
  **TPC-H 24/24 MATCH, plans byte-identical, plan-gate exit=0,
  PLAN-PARITY match=6 shapediff=14 missingnode=2, TPC-DS PASS=95
  CKMISMATCH=0**. Honest note: no TPC-H plan MOVES to the shape at SF=1
  under the pinned stats (hence byte-identical plans) — it is reachable
  and priced, and the two remaining MISSING-NODE entries are other shapes.
  **CORRECTION 2026-09-06: that inference over-claimed.** The plan capture
  is taken by `estimate-audit`, whose `-serial` flag defaults to TRUE and
  sets `max_parallel_workers_per_gather = 0` on the audit session
  (`cmd/estimate-audit/main.go:30-36`, deliberate — goopg does not merge
  per-worker Instrumentation, so nodes under a Gather report no actual
  rows). A serial capture is structurally blind to a PARALLEL index scan,
  which is exactly what C-19c adds. "Byte-identical" is therefore a valid
  serial-control-arm statement and nothing more; whether a parallel plan
  moved is UNMEASURED here and needs a `-serial=false` capture against a
  parallel-mode PG baseline that does not yet exist. Ledger
  `take3-plan-capture-is-serial-only`.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP).*
- [x] **C-19d P5-04 `generate_useful_gather_paths`** producing `PathGather`
  and `PathGatherMerge` priced by `cost_gather`/`cost_gather_merge`, with
  `createPlanNode` arms. LANDED 2026-09-06 (`gatherpaths.go`,
  `createplangather.go`; design `docs/design/planner-c19d-gather-paths/
  DESIGN.md`). `PartialPathlist` finally has a reader: the cheapest partial
  path becomes an ordinary candidate on the serial `Pathlist`, priced by
  `cost_gather` (costsize.c:446 — `gatherCost`'s first search caller since
  it was written) and `cost_gather_merge` (new `gatherMergeCost`), rows from
  `compute_gather_rows`. Call sites are PG's three: per joinrel before
  `set_cheapest`, the same spot in GEQO's `merge_clump`, and per base rel —
  the last only for a multi-baserel statement, which goopg gets free (a
  one-item joinlist never enters the search), so TPC-H Q1's post-pass
  Finalize/Partial split is untouched. `enable_gathermerge` becomes a
  COUNTED `disabled_nodes` term, the conversion
  `ParallelSettings.DisableGatherMerge`'s comment asked P5-04 for.
  `MaybeAddGather` now declines on a tree that already carries a Gather —
  the coexistence rule AND a correctness stop, since `terminatesPartial`
  lists `*Gather` but `findPartialSubtree` DESCENDS through it and would
  have nested a second one. Two executor facts became refusals rather than
  hopes: `gatherMergeOp` attaches only the seq-scan block allocator (a
  Gather Merge over a partial INDEX path returns N copies of every row), and
  `runWorker` ignores `attachParallelScan`'s return value, so
  `createGatherPlan` panics unless `drivingScan` reaches the built
  subtree's scan. **Admission is `GOOPG_GATHER_PATHS` = off (default) /
  top / all, and the default is an OPEN measured question**: partial paths
  exist on base rels only until C-19f, so an admitted Gather sits BELOW
  every join while the post-pass puts one above the whole hash-join
  subtree. Quantified while writing the pins: with the whole relation
  crossing the boundary, `parallel_tuple_cost`×rows (0.1/row) exceeds the
  scan's entire cost while the saving is the 0.01/row CPU share — so a
  base-rel Gather is DOMINATED at any relation size, which is the same
  reason PG's Gather sits above joins and aggregates. C-19d therefore
  cannot pay for itself until C-19f/g shrink what crosses it; flipping the
  default needs the TPC-H A/B (timing per moved plan — a row-count gate
  cannot catch this class) that this session did not own the cluster for.
  Gates: `go build ./...`, `go vet`, full `go test ./internal/optimizer/
  ./internal/executor/` green; units scope of `ralph-precommit-test.sh`
  green; pgbench smoke green (commit hook).
  **ANSWERED by C-19f (2026-09-06)**: with a joinrel's own partial paths
  the Gather does become choosable by cost, and the A/B this row asked
  for was run — 7 TPC-H plans move, values 7/7 MATCH, Q21 −51 %, Q9
  +94 %, Q10 +30 %, suite total inside its own spread. The default still
  does not move; the arithmetic and the plans are in
  `docs/design/planner-c19f-parallel-hashjoin/DESIGN.md` §10. Two of
  C-19d's own `createPlan` arms were also found broken there and fixed
  (`125c4c016`): `createGatherPlan` built the node with a struct literal
  instead of `NewGather` (zero output columns) and `*Gather` did not
  embed `searchedTree` — both unreachable until a Gather could win at a
  search root.
  *design: take3 08 §8 + docs/design/planner-c19d-gather-paths/DESIGN.md;
  gate: take3 09 §5 P5 (PP, parallel-on + serial control) — run under
  C-19f, see that row.*
- [x] **C-19e P5-05 re-decide Gather Merge → Sort → Parallel scan by
  cost** rather than `sortPartialRootPays`' hard-coded decline. LANDED
  2026-09-07; design `docs/design/planner-c19e-partial-sort/DESIGN.md`
  (measurement in its §6). `partialSortRootPays` (partialsortpaths.go) is
  a two-candidate PATH tournament — `Gather Merge -> Sort -> partial`
  against `Sort -> Gather -> partial`, the two plans the post-pass
  actually builds — priced by `costSortRun` + `cost_gather` /
  `cost_gather_merge` and adjudicated by `addPath`/`setCheapest`, in
  C-19g's shape. **No new constant**: the rule it replaces had one
  hard-coded type switch, the replacement has none. Rides a new
  `GOOPG_PARTIAL_SORT_PATHS`, default `off`, which delegates to the
  retired switch unchanged, so the default and serial control arms are
  bit-identical.
  **No permitted divergence is recorded — the anticipated case did not
  occur.** goopg's costs choose the WORKER-side sort, disagreeing with the
  rule, and the measurement backs the costs: exactly one TPC-H plan moves
  (q16, the query the rule's own note cites) and its median over five
  paired observations on one engine image is 0.82 s off / **0.70 s on**.
  The historical q16 1.5 → 2.3 s and q13 4.2 → 6.8 s (M0134-0189) **do not
  reproduce** — they predate E-10's Gather-Merge claim set — and q13's plan
  does not move at all under the new verdict. Suite totals inside their own
  spread (143.80/146.73 and, reversed, 139.37/138.25), so no suite claim.
  Gate result: `go build`, `go vet`, full `go test` on
  `./internal/optimizer/ ./internal/executor/` green; `RALPH_PRECOMMIT_SCOPE=units`
  exit 0; `-race` green on the parallel set; TPC-H `-digest`/`-diff`
  **24 MATCH both pairs**; `make plan-gate` **22/22 structural AND 22/22
  MODE=costs** on the default arm, 21/22 on the `on` arm with q16 the only
  divergence.
  The mandatory both-arms run FOUND A LATENT EXPLAIN BUG (§6.4):
  `rebuildWithGather`'s merge arm stamped `stampParallelScan(root)` with
  root = the `*Sort`, which has no arm there, so the scan under a Gather
  Merge rendered with **no `Parallel ` label** while the workers split it.
  Label-only (the flag is read in `operators_explain.go` and nowhere else)
  and latent since P7 because the shape was unreachable; fixed by stamping
  the Sort's child, pinned by `TestGatherMergeStampsDrivingScan`.
  *design: take3 08 §8; gate: take3 09 §5 P5 (timing both shapes).*
- [x] **C-19f P5-06 parallel hash join as a `parallel_aware` hash path,
  priced.** LANDED `e8456fe82` (mechanism) + `125c4c016` (consumer check
  and two latent fixes); design
  `docs/design/planner-c19f-parallel-hashjoin/DESIGN.md` (`1e3d0a8b6`).
  `addPartialHashJoinPath` = `try_partial_hashjoin_path` (joinpath.c:1299)
  + `hash_inner_and_outer`'s parallel block (:2418): a PARTIAL OUTER over
  a COMPLETE, parallel-safe, unparameterised INNER, filed into the
  joinrel's `PartialPathlist`. goopg has NEITHER of PG's two parallel
  hash joins — its executor pre-builds the table ONCE in the leader and
  shares it by pointer, so the shape is upstream's `parallel_hash=false`
  variant with the N-fold build replication removed, and
  `parallel_hash=true` (a partial inner) is refused for want of an
  executor. **The build is charged ONCE, undivided** — after E-09a/E-09b
  that describes the executor, and it is also what
  `initial_cost_hashjoin` charges (costsize.c:4187); the reverted
  `tmp/d05p4` 5× participant multiplier is refuted, with a pin. Partial
  paths now PROPAGATE upward, which is what C-19d §5 named as the thing
  it lacked. Rides `GOOPG_GATHER_PATHS` (no new flag: the only reader of
  a partial path is behind that mode, so the slice is provably inert at
  the default) and **the default STAYS `off`**.
  Gate result: `go build`, `go vet`, full `go test` on
  `./internal/optimizer/ ./internal/executor/` green; units scope of
  `ralph-precommit-test.sh` exit 0; `-race` green on the new shape.
  Executor consumer check (the item's own requirement) landed in
  `internal/executor/parallel_hash_path_consumer_test.go` and FOUND TWO
  LATENT BUGS in C-19d's landed `createPlan` arms, both unreachable
  until a partial JOIN path made a Gather winnable at a search root:
  `createGatherPlan` built `&Gather{…}` as a struct literal instead of
  `NewGather`, so the node reported ZERO output columns; and
  `*Gather`/`*GatherMerge` did not embed `searchedTree`, so
  `markSearchedTree` panicked. Witness: `Gather (Workers Planned 2) →
  Hash Join (Build Time 0.202 ms, ONE) → Parallel Seq Scan on pq_fact`
  with `Seq Scan on pq_dim … rows=0.00 loops=0` in every worker.
  **MEASURED, TPC-H SF=1, `off` vs `top`, 3 repetitions, pinned regime**
  (DESIGN §10): 7 queries move, 7/7 values MATCH in all 42 runs; three
  movements are real and two disagree in sign — **Q21 17.33→8.42 s
  (−51 %)**, a Gather *inside* a Nested Loop Anti Join's subtree that
  `terminatesPartial` structurally prevents the post-pass from ever
  reaching; **Q9 7.26→14.06 s (+94 %)**, a JOIN-ORDER change (the
  parallel scan moves from `lineitem` 6 M to `orders` 1.5 M because the
  cheaper partial path wins `add_partial_path` at its own rel) — NOT the
  §7.2 `splitAggregate` foreclosure that was predicted; **Q10 +30 %**
  with a byte-identical executed shape, unexplained and recorded as
  open. Suite total −10.7 % is inside its own run-to-run spread. So the
  default does not move: C-19f establishes that a Gather is now
  CHOOSABLE BY COST above a join (which C-19d could not obtain at a base
  rel at any size) and reduces the remaining gap to a CALIBRATION
  problem with a named reproducible witness. Method note recorded:
  `estimate-audit -plan-only` does NOT apply `MaybeAddGather`, so its
  captures show zero Gathers on HEAD and any plan census taken with it
  overstates a parallel change. TPC-DS SF0.5 gate at the shipped default:
  `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`, and
  `PLAN-SHAPE queries=99 same=99 changed=0` — the strongest available
  statement of this slice's inertness when off. That run's NON-BLOCKING
  status-delta channel reported `total-delta=+28.0%`; it is recorded and
  explicitly NOT attributed (DESIGN §10.9): the plan channel in the same
  run shows 99/99 shapes unchanged, the binary carried another agent's
  concurrent E-14 executor WIP, and the host had just been running this
  session's TPC-H arms.
  *design: take3 08 §8 + docs/design/planner-c19f-parallel-hashjoin/
  DESIGN.md; gate: take3 09 §5 P5 (PP). Open: `make plan-gate` /
  `pg-plan-parity-diff.py` cannot judge a parallel plan until they read
  a post-pass-inclusive capture (§10.8); the D-05 re-run at 1× is next
  and must explain Q9.*
- [~] **C-19g P5-07 partial aggregation as paths**
  (`create_partial_grouping_paths`), replacing `splitAggregate`. Depends
  on C-15 (landed `40d7a4667`).
  *design: `docs/design/planner-c19g-partial-agg/DESIGN.md`; take3 08 §8;
  gate: take3 09 §5 P5 (PP + values-diff).*
  **LANDED (`866e6fe7e`), mode-gated `GOOPG_PARTIAL_AGG_PATHS`, default
  OFF.** `createPartialGroupingPaths` (partialaggpaths.go) replaces
  `splitAggregateIsProfitable`'s five invented constants with a
  two-candidate PATH tournament — `PathAgg(partial) → PathGather →
  PathAgg(finalize)` against `PathAgg(simple) → PathGather(whole input)` —
  priced by `costAgg` + `gatherCost` through `costParams` and adjudicated
  by `addPath`/`setCheapest`. No new cost function, no new constant.
  `splitAggregate` remains the only construction site, so double-splitting
  is structurally impossible and C-19d's `subtreeHasGather` stand-down is
  untouched. Executor consumer check green (a priced win executes as
  Finalize→Gather→Partial with byte-identical VALUES at 1/2/4 workers).
  Two findings recorded in the design: goopg's Partial node emits ZERO
  rows, so what crosses the boundary is group-STATES (Q1: 16 vs 5.9 M
  tuples); and the verdict had to be made homogeneous in the input row
  count because `TableStats.RowCount` is never restored (ledger pq-P6) —
  a model needing absolute rows would refuse every split including Q1's.
  MEASURED (design §9, private cluster clone — :65433 was contended all
  session): TPC-H values 24/24 MATCH on VALUES both arms; TPC-DS SF0.5
  with the mode ON `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
  total −3.9%, verdict-changes=none; plan-gate mode-OFF **22/22 MATCH**
  against `c05-c04b-20260907` (the control arm is inert against the shared
  pin, not just by unit test); mode ON moves 3/22 — Q1/Q5/Q9 gain
  `Finalize → Gather → Partial`, the grouped aggregates the size rule
  refused outright. **Q1 8.57 s → 4.14 s median, −51.7%**, over three
  alternating passes whose off-arm spread is 1.2%; nothing else outside
  its own arm's spread. `MODE=costs` NOT evaluable on the clone (cold
  stats drift the control arm 21/22) and is not claimed.
  **RECOMMENDATION: flip the default to `on`** — every §6.4 criterion for a
  positive result is met. Not flipped here because the flip needs a re-pin
  of `plan_snapshots/`, which two peer agents were A/B-ing against all
  session, plus `MODE=costs` on the canonical cluster.
  REMAINDER (not this slice): the upper-rel-resident port needs one change
  in `groupingpaths.go` (owned elsewhere) plus an answer to the plan-cache
  question — see that design's §8.
- [!] **C-19h P5-08 retire `MaybeAddGather` — BLOCKED, and NOT on the
  default flip. Measured 2026-09-07; evidence
  `docs/design/planner-c19h-gather-postpass/DESIGN.md`.**
  The blocker is **C-19g's unfinished upper-rel-resident half**, which was
  not previously named as this item's prerequisite.
  1. *At the default the post-pass is the ONLY producer of parallelism.*
     `generateUsefulGatherPaths` and `addPartialAggPaths` are the only
     producers of `PathGather`/`PathGatherMerge` and both return at their
     first line when their knob is `off` — the default for both. SF=1
     census, one engine image, engine defaults (`GOOPG_PGSHAPED_DP=1`,
     `GOOPG_PGSHAPED_COLLAPSE=1`): **12/22 queries carry a Gather with the
     post-pass, 0/22 without it**, and the suite goes **232.35 s →
     467.03 s (+100.0 %)** (Q18 43→154, Q21 17.7→61.1, Q19 2.6→25.3).
  2. *The conditional retirement fails too.* A probe build standing the
     post-pass down wholesale under `GOOPG_GATHER_PATHS=all` +
     `GOOPG_PARTIAL_AGG_PATHS=on` reaches only **7/22** queries with a
     Gather against the post-pass's 12: it gains Q21 (C-19f's win, the case
     only a path model reaches) and **loses Q1, Q6, Q14, Q15a, Q16, Q19**.
     `top` gives the identical 7.
  3. *Why Q1 is lost.* C-19g's Q1 8.57 → 4.14 s is delivered THROUGH the
     post-pass: it replaced the verdict (`partialAggSplitPays` "returns
     only a boolean and constructs no node") but not the construction —
     `splitAggregate` (parallel.go) still builds
     `Finalize -> Gather -> Partial`. That is exactly what C-19g's own
     `[~]` row means by "the upper-rel-resident half is unfinished".
  The conditional stand-down was written and **reverted rather than
  shipped**: landing it would serialise six queries in the very arm on
  which the default flip is to be judged. C-19d's per-tree
  `subtreeHasGather` stand-down is unchanged and still correct.
  **Double-Gather verification (the item's own requirement), done not
  assumed**: across every arm measured — including `GP=all PA=on` with the
  post-pass live — no plan carries more than one Gather on any
  root-to-leaf path.
  Sequencing: finish C-19g's upper-rel half → re-run the census → retire
  conditionally on `all` → flip the default (needs the `plan_snapshots/`
  re-pin) → only then delete. Only the last step is C-19h as written.
  *design: take3 08 §8; gate: take3 09 §5 P5 — plan-parity both suites,
  parallel and serial arms.*
  (Serial control arm unchanged throughout C-19a–h. Ordering trap already
  measured: at small budgets the plan moves onto index-driven joins the
  old post-pass cannot drive — take2 07 §3.2.)
- [ ] **C-20a P6-01 one cardinality estimator — RE-SCOPED 2026-09-07,
  NOT YET EXECUTABLE.** Original text: delete legacy
  `estimateJoin`/`EstimateRows` + the `joinkeyproof.go` mirror;
  everything reads `calcJoinrelSize`. The census
  (`analysis/planner-refactor-take3/c20a-estimator-census-20260907/DESIGN.md`)
  found none of the three deletions available, and none of them blocked
  on remaining call sites:
  - **`EstimateRows` is a coordinate-space problem.** `calcJoinrelSize`
    is a `searchCtx` method over `*RelOptInfo`, reachable only inside
    the search; `EstimateRows` walks the plan `Node` tree. 28 live call
    sites in 15 files, and three of them are in `internal/executor/`
    (EXPLAIN, hash-table geometry, correlated-subquery cache budget),
    where no `RelOptInfo` exists or ever will. C-11…C-18 DID convert
    their consumers — `groupingpaths`/`distinctpaths`/`partialaggpaths`/
    `windowsetoppaths`/`upperrel` all read `legacyDisplayCostOf(…).PlanRows`
    now — they were simply never the majority.
  - **`joinkeyproof.go` is NOT a mirror; strike it from this item.**
    Only `superkeyJoinEstimate` belongs to `estimateJoin`.
    `resolveBaseColumn` serves `selectivity.go`, `extstats.go` and
    `estimateNumGroups`; `uniqueKeyColumnSets` serves 8 scan-construction
    sites in `planner.go`; `columnsSubset` is a live dependency of
    C-05's own `joinrelsize.go`. It is also the subject of
    `TestResolverFamilyArmListsAgree`, the guard against the missing-arm
    class that cost 8007× on TPC-DS.
  - **`estimateJoin` is not separable** — it is the `*Join` arm of
    `EstimateRows`; deleting it alone re-opens the M0125-0038 collapse.
  **The real prerequisite is smaller than it looked.** `PlanCost.PlanRows`
  already carries the winning path's row count on every search-produced
  node (`stampPlanCost`, one funnel) and EXPLAIN never reads it:
  `explainCostFields` (`operators_explain.go:2086`) takes
  StartupCost/TotalCost/PlanWidth from the carrier and computes `rows=`
  as `EstimateRows(rowSrc)` at all four sites. So the planner CHOOSES
  with `calcJoinrelSize` and EXPLAIN REPORTS `estimateJoin` — and every
  estimate artefact in the tree (plan-gate `MODE=semantic-cost`,
  `estimate-audit`, the c13a census figures, the new EA ratchet) reads
  the reported one. **Successor:** make those four sites read
  `PlanCost.PlanRows`, with the `plan_snapshots` re-pin and the
  `estimateaudit` fixture re-capture in the SAME commit (take2 P0-02's
  own hazard note; 09 §7.1). It moves no plan — it is a display path —
  but it moves every `rows=` in every capture.
  Landed under this item: `internal/optimizer/cardinality_two_estimators_test.go`
  (the two estimators agree on the control and superkey shapes — by two
  independent implementations; nothing observed that before) and the EA
  ratchet below.
  *design: take3 08 §9 + c20a-estimator-census-20260907; gate: take3 09 §5 P6.*

> **RESOLVED (2026-09-07): the EA ratchet now exists and runs —
> `make ea-ratchet`.** It had never run (ledger
> `take3-ea-ratchet-never-ran`): no Makefile target, hook, precommit
> script or ci/batch stage invoked `scripts/tpch-estimate-audit-arm.sh`,
> and its default pinned PG baseline was absent from the tree, so a
> default-flag run exited before measuring. `estimate-audit -plan-only`
> (the plan-capture step) does run and is fine; the est-vs-actual parity
> ratchet did not. The replacement
> (`scripts/estimate-parity-gate.sh` + `scripts/estimate-parity/`) closes
> all four gaps: **TPC-DS SF0.5** rather than TPC-H, `EXPLAIN ANALYZE`
> truth rather than estimates, **base-rel AND joinrel** granularity via
> relation-set keying (a singleton key IS a base relation, so the
> `rows=1` collapse in `docs/design/planner-rowest-collapse/DESIGN.md` is
> now a candidate), and a **PG-relative** bar
> `qerr > max(10, PG_qerr × 2)` — the only bar that passes Q47/Q57/Q81/Q89,
> where PG 18.3 emits the same `rows=1`, and fails Q99's 8007×. Runs on
> its own clone on port 5534; never touches 65433/65437. C-05, C-10a and
> C-21 cite the same gate and can now use this one.
- [x] **C-20b P6-02 `PathTarget` + range table — LANDED 2026-09-07, with
  one of its three deletions REFUSED on evidence.** Design:
  `docs/design/planner-c20b-pathtarget-rangetable/DESIGN.md`.
  1. **`joinlayout.go` remapping: DELETED** (~580 lines; the file goes
     1353 → 768). `remapColumnRefsAfterRewrite` /
     `remapPosMapAfterRewrite` went on a PROOF — the walker took a
     `posMap func(int) int`, never read it, and its body assigned to no
     `Index` field. The bindings-posmap family (`remapWithBindings`,
     `remapTopProjection`, `remapAggExprsWithBindings`,
     `buildBindingsPosMap`, `applyJoinTreePosMap`, `scanKey`,
     `searchedTreeWidth`) went on a CENSUS, to the gate C-20c failed:
     over TPC-H (22) and TPC-DS (99), on both `GOOPG_PGSHAPED_DP` arms,
     the passes were reached up to 408 times and moved **zero**
     ColumnRefs, and `EXPLAIN` text is byte-identical without them on
     all four arms.
  2. **`createplanroot.go` boundary assertions: NOT deleted, and must
     not be.** While `ColumnRef.Index` is a POSITION rather than PG's
     `(varno, varattno)`, `boundaryMap`'s hole / out-of-range /
     duplicate panics are the DETECTOR for the wrong-answer class this
     item names, not a symptom of a missing range table — `searchedtree.go`
     already records that they "remain the primary guard". Deleting them
     would convert three loud plan-time aborts into three silent
     wrong-row classes.
  3. **`baseLeaf`/`baseOffset`: not replaceable, and the premise was
     wrong.** goopg has HAD a range table since M0071-0009 —
     `rangeBinding` carries `sourceIdx` (PG's `varno`, stamped onto every
     column as `SchemaColumn.SourceTableIdx`) and `rtid`; `baseLeaf` /
     `baseOffset` are its projection onto the search's relid space. What
     goopg lacks is PG's `Var`. Every consumer of the two fields lives in
     a file this task did not own; a value-embedded `rangeTblEntry` in
     `RelOptInfo` would preserve all of them through field promotion and
     is the recommended shape, but it is ceremony without the `Var`
     change.
  Landed in place of (2): `internal/optimizer/rangetable.go` —
  `rangeTableFromPath` + `assertBoundaryColumnIdentity`. `boundaryMap`
  proves the root's layout is a PERMUTATION; it cannot say WHICH column
  sits at a position, and a valid permutation that assigns the wrong
  coordinate is exactly this item's silent class (right row count,
  neighbouring relation's values). The new assertion requires every root
  output column to agree with the range table on `(Name,
  SourceTableIdx)`, so a swapped self-join instance — invisible to every
  name-based check in the planner — is caught at plan time. Abstention is
  transcribed from `assertSearchedTreeNeedsNoReconcile`.
  *design: take3 08 §9 + planner-c20b DESIGN; gate: value-level `-diff`,
  never counts.*
- [!] **C-20c P6-06a retire `GOOPG_INDEXKEY_HARVEST` — BLOCKED: the flip
  is not plan-neutral (measured 2026-09-05).** The item's own gate is
  "byte-identical plans for the flip", and the flip fails it. On/off arms
  back to back under the pinned regime (SF=1, serial):
  Q2 0.97 / 1.05, Q4 **2.27 / 1.46**, Q17 0.65 / 0.75, Q20 1.70 / 1.99,
  Q22 **1.17 / 0.79** (ON / OFF seconds). Q4 and Q22 are materially faster
  with the harvest OFF while Q2/Q17/Q20 are faster with it ON, so the off
  path is live, not dead weight, and retiring the flag would lock in the
  slower side of two queries.
  Two by-products of the measurement, both landed: the flag's own doc
  comment claimed "Gated OFF by default" while
  `indexKeyHarvestFromEnv` answers true for an unset variable (it is ON) —
  corrected in place; and the historical catastrophe the comment records
  (Q4 3.87 s → 276.08 s with the harvest) **no longer reproduces**, so the
  prerequisite execution work landed at some point without this note being
  updated.
  Resume: retire only once the search chooses the harvested shape on cost
  for Q4/Q22-class queries too, i.e. after the semi/anti execution work the
  original comment names. Ledger `take3-C-20c-blocked`.
  *design: take3 08 §9; gate: byte-identical plans for the flip
  (take3 09 §5 P6).*
- [!] **C-20d P6-06b `GOOPG_INDEX_PROBE_MULT` — flag KEPT, but its default
  CALIBRATED 1.0 → 2.0. The session's largest performance result.**
  The knob existed because "PG's constants (multiplier 1) under-cost
  goopg's NL-index probe … the DP would pick ruinous PG-shaped NL plans",
  and it **shipped at 1.0** — the value its own comment identifies as
  wrong — because the validation it was waiting for was never run.
  Retiring it at 1.0 would have made the mis-costing permanent.
  Measured (SF=1, serial, pinned statistics, fresh server per arm):
  **Q5 21.60 → 4.07 s, Q7 15.72 → 5.86 s, Q9 13.17 → 7.06 s,
  Q3 6.25 → 2.67 s; suite total 138.58 → 100.79 s (−27%)**, no query
  regressed outside the band. 2 beats 4 (same plans on the probed queries,
  4 marginally worse on Q7), so 2 is the smaller departure from PG's
  constants that buys the whole win.
  Correctness: TPC-H values **24/24 MATCH**; TPC-DS SF0.5 **PASS=95
  MISMATCH=0 CKMISMATCH=0**; Q12=2/Q13=34.
  Plans moved deliberately (95 shape lines; NL-index probes over large
  relations become hash joins) and **plan parity IMPROVED**:
  `match=5 shapediff=15` → **`match=6 shapediff=14`**, the monotone
  direction take3 09 §5 requires — the calibration moved goopg toward PG's
  shapes, not only toward faster. Baseline re-pinned
  (`plan_snapshots/probe-mult2-20260905.txt`); `make plan-gate` 22/22 in
  structural AND `MODE=costs`.
  **Retirement itself stays BLOCKED**: the multiplier is load-bearing at
  2.0, and the comment expects another recalibration once goopg's NL probe
  stops materialising the whole TID list eagerly. The knob keeps a
  validated default instead of an unvalidated one. Ledger
  `take3-C-20d-calibrated`.
  Artifact:
  `analysis/planner-refactor-take3/c20d-probe-calibration-20260905/README.md`.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [!] **C-20e P6-06c retire `GOOPG_HASH_OUTER_JOIN` — still a wash,
  RE-MEASURED 2026-09-05 under the new cost landscape.** The item asked
  for a re-measure after the `btcostestimate` batch (B-15, still blocked);
  it was re-measured after a different and larger landscape change instead
  — C-20d's index-probe calibration, which moved 95 plan-shape lines and
  cut the suite 27%. Result unchanged in character: **100.05 s with the
  flag on vs 100.79 s off, a 0.7% difference inside the ±1.7% run-to-run
  drift band**, so it is not a claim. Values 24/24 MATCH. Per-query moves
  are all sub-second and unsigned (Q1 7.48→6.88, Q13 5.27→4.88,
  Q12 12.78→12.48 for; Q18 30.93→30.99, Q5 4.07→4.09 against).
  Still NOT flipped and still not retired: a flag that changes nothing
  measurable cannot be retired on a "byte-identical plans" gate either,
  because its plans are not byte-identical — it is simply that the
  differences do not pay. Re-measure again after B-15.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [!] **C-20f P6-06d retire `GOOPG_NLI_COSTGATE` — BLOCKED 2026-09-07 on
  its own gate: the flip moves TPC-H Q4. Nothing deleted.**
  Two `estimate-audit -plan-only` captures on ONE private SF=1 clone
  (port 5541, fresh capped server per arm, one binary, pinned stats),
  with an A/A control that came back byte-identical. The flag's only live
  value is `legacy` (the pre-D6.3a stats-blind semi/anti heuristic,
  `nl_index_join.go:62`), and it moves exactly one query:
  **Q4 default `Nested Loop Semi Join` over `idx_lineitem_orderkey_fkidx`,
  cost 8 672.13, 1.60 s — vs `=legacy` `Hash Semi Join` with a Seq Scan on
  lineitem, cost 105 656.84, 18.30 s (11.4x)**, the Q4 semi-join class
  07 §3.8 records at 12.5x.
  Unlike C-06 / C-20c the LOSING arm here is the flag's own off path, so
  retiring the hatch would not change what production plans — it would
  delete the only reachable spelling of the slower plan. That is a
  deliberate exception to the stated gate (08 §9's "or every difference
  explained and timed"), not a pass of it, and the deletion is
  irreversible, so it is left to an explicit decision. `planner-flags.env`
  untouched. Ledger `take3-C-20f-blocked`; artifact
  `analysis/planner-refactor-take3/c20fg-flag-retirement-20260907/README.md`.
  Captures are serial (`-serial` defaults true) and therefore blind to a
  parallelism-only plan change (ledger `take3-plan-capture-is-serial-only`).
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
  **OWNER DECISION 2026-09-07 — the hatch STAYS; C-20f is closed as
  blocked, not deferred.** The adjudicating agent correctly escalated a
  real asymmetry rather than deciding it: unlike C-06, here the LOSING arm
  is the flag's own off path, so retiring the hatch would change no plan
  production reaches, which makes deletion defensible as a deliberate
  *exception* to the stated gate rather than a pass of it. Deciding
  against deletion, for three reasons. (1) The gate as written fails —
  11.4x is not "byte-identical", and an exception granted once is a
  precedent the remaining flag-retirement items inherit. (2) Deletion is
  irreversible and the hatch is a branch, not a maintenance burden of the
  P6-03/P6-04 kind, so the asymmetry of the mistake runs strongly one way.
  (3) The hatch's value is precisely "escape if the cost gate misfires on
  data we have not seen", and we have no evidence about data we have not
  seen — the 11.4x measured here says the gate is right on THIS corpus,
  which is not the same claim. Reopen only with a maintenance cost
  attached to keeping it.
- [!] **C-20g P6-06e retire `GOOPG_PGSHAPED_DP` last — BLOCKED 2026-09-07,
  re-verified on the current tree rather than inherited. Nothing deleted.**
  The item's own text was checked against the post-C-04c tree (C-04c
  changed jointree admission) with the same paired-capture method as
  C-20f, same clone and same binary: **17 of 22 TPC-H queries move**
  (587 diff lines; changed lines/query Q2 67, Q8 59, Q11 42, Q5 41,
  Q7 36, Q9 35, Q21 35, Q10 24, Q3 20, Q18 20, Q20 17, Q13 14, Q12 12,
  Q16 12, Q17 9, Q14 6, Q19 4), and all 17 change top-level cost — e.g.
  Q8 184 794.76 → 187.96, Q2 33 460.37 → 287 869.59, Q12
  1 608 955.75 → 76 169.21 (the arms cost different plan spaces, so the
  off-arm numbers are not a quality claim about the off arm).
  The flag selects the ENUMERATOR (M0127-P5.9): `=0` is no search,
  syntactic order and the legacy rule rewrites — a whole second planner,
  not dead weight. P6-03/P6-04 stay must-not-delete (6.5x / 12.5x,
  recorded, NOT re-measured here) and C-04c did not disturb that.
  `planner-flags.env` untouched. Ledger `take3-C-20g-blocked`; artifact
  `analysis/planner-refactor-take3/c20fg-flag-retirement-20260907/README.md`.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [ ] **C-20h P6-07 `setrefs` phase + P6-08 `RestrictInfo` caching.**
  `setrefs` only if C-20b shows the executor still needs explicit column
  resolution — **C-20b (2026-09-07) shows exactly that, so the `setrefs`
  half is NOT moot; it is the actual P6-02.** `ColumnRef.Index` is a
  position in the pre-search binding concatenation and is the executor's
  only column address (expression evaluation is a flat slice lookup into
  the child's materialised slot); `SourceTableIdx` rides beside it as a
  disambiguation hint that nothing resolves THROUGH. Making the boundary
  map deletable means (i) `ColumnRef` becomes `(SourceTableIdx, attno)`
  with `Index` derived, (ii) every producer stops computing positions,
  (iii) a `setrefs` pass computes them once at the end, (iv) the
  executor's slot resolution is re-pointed at its output — a change
  spanning `plan.go`, the parser binder, the whole optimizer and the
  executor's expression evaluator, which no TODO_ALL row currently
  carries. Caching is planning-speed, not plan-quality.
  **P6-08 LANDED 2026-09-07; P6-07 steps (i)–(iv) DEFERRED,
  ledger `take3-C-20h-var-migration`.** Three `restrictInfo` memos, all
  of them PG's own: `norm_selec` on `joinClauseSelectivityExt`, the
  operand resolution behind `left_relids`/`right_relids` on
  `joinKeyPairOf`, and `MergeScanSelCache` (upstream `cached_scansel`)
  on `mergeJoinScanSel` — the last one is where the time was, and the
  profile said so rather than the guess: `histCmp` under
  `mergejoinscansel` was 43.7% of planning CPU. SEMI/ANTI selectivity is
  deliberately NOT memoised (it is a function of the (outer, inner)
  split, which is why upstream keeps a second field, `outer_selec`).
  Measured on the SF=1 cluster (private clone, warm stats,
  `GOOPG_ANALYZE_SEED=20260905`, 3 servers × 3 reps per arm): TPC-H
  EXPLAIN wall time **73.13 ms → 62.83 ms (−14.1%)**, Q9 −52%, Q21 −34%,
  Q18 −26%, Q5 −23%, Q2 −22%; in-process over a statistics-bearing
  catalog (5 interleaved pairs) 37.84 ms → 27.18 ms per 22-query
  planning pass (−28%). **Plans byte-identical**: all six cost-visible
  EXPLAIN captures share one md5 (`373aed0c…`). Gates: units 44/44,
  `go vet`, TPC-H digest **24/24 MATCH on values**, TPC-DS SF0.5 sweep.
  Cold-vs-warm equality of each memo is pinned by
  `joinrestrict_cache_test.go` (each assertion mutation-checked).
  P6-07 step 0 only: `RelOptInfo` embeds a value
  `rangeTblEntry{baseLeaf, baseOffset}` — C-20b's recommended shape,
  every existing read preserved through field promotion. The `Var`
  migration is 102 `ColumnRef{` sites + 237 `SourceTableIdx` references
  outside tests; steps (i) and (ii) are one commit or none, since an
  `attno` populated at some producers and read by none is scaffolding
  later readers would trust. Nothing was deleted from
  `createplanroot.go` or `rangetable.go` — they remain the detector.
  *design: take3 08 §9; gate: take3 09 §5 P6 (P6-08: planning-time
  comparison, plans byte-identical).*
  P6-03/04/05 stay **must-not-delete** (measured 6.5× / 12.5× /
  live-tripwire oracle — record, do not retry without new evidence).
  *design: take3 08 §9; gate: take3 09 §5 P6 byte-identical or
  explained-and-timed.*
- [ ] **C-21 P7-01 full acceptance run.** Both suites, S-cold and WARM,
  full PP roll-up, EA ratchet, complete timing table, §6 headers on every
  artifact. Bars A1–A5 + B1–B3 + C1–C7 (take3 09 §4); B4 directional only.
  Verdict under `analysis/planner-refactor-take3/acceptance-<date>/`
  with before/after roll-ups and an explicit worse-statement; negative
  results kept verbatim.
  *design: take3 08 (all); gate: take3 09 §8.*

---

## 5. Track D — minimize_datum core

*Gates: 06 (per-commit floor + per-slice gates + measurement protocol).
`Datum` stays 48 bytes — the bundle's falsifiable claim (06 §5.5).
No wall-time target in acceptance (06 §5).*

Geometry-free slices (D-01/D-02/D-03/D-04/D-09) proceed per A-06;
D-05 onward additionally needs A-06 acceptance + E-14 + B-01c.

- [x] **D-01 MD-01 `TupleDesc` + type metadata.** Landed 2026-09-05:
  `attLen`/`attByVal`/`attStorage` on `colTypeInfo`, **derived** through
  `catalog.TypeNameToOID` → `userTypeAttrsForOID` (the existing
  name→OID→descriptor bridge, REVIEW M-goopg-2) rather than transcribed —
  the D-02 audit found FOUR transcriptions of this list already in tree, so
  a fifth written by hand would be the drift hazard 03 §5 names. Arrays
  short-circuit to the varlena descriptor (`catalog.Type` carries the
  ELEMENT name, so an OID lookup would hand back the element's by-value
  descriptor for an `int4[]` column). Purely additive: no consumer yet, and
  `align` deliberately stays on `physicalPGTypeAlign` because unifying
  alignment is D-09's job and changes the on-disk format.
  Gates: 4 new agreement tests (pg_type.dat values incl. `point`/`money`;
  array short-circuit; unknown-type fail-safe DIRECTION — varlena, never
  by-value; and a **sibling-path check that the descriptor's `typalign`
  agrees with `physicalPGTypeAlign` on every type both recognise**, which
  is what keeps the two transcriptions from drifting further); executor
  suite green; TPC-H values 24/24 MATCH; plans byte-identical; plan-gate
  PASS; PP unchanged.
  **Site count re-derived from the tree on landing (05 §5 obligation),
  measured with 05 §2's own commands:** retention struct fields **48**
  (estimate 48 — unchanged); `cloneRowOwned` call sites **20 across 14
  files** (estimate 19 across 14 — one site added since the estimate, in
  the same file set); `resolveColTypeInfo` callers **2**. So 05 §2's
  surface estimate still holds; the delta is +1 ownership-boundary site and
  no new file. D-01 itself changed **0** call sites (additive fields only),
  which is why no consumer-side count moved.
  Original scope note follows.
  **D-01 (original scope note).** `attlen`/`attbyval`/
  `attstorage` on `colTypeInfo`, sharing **one** `pg_type.dat`
  transcription with `userTypeAttrsForOID` (reuse the existing
  name→OID→descriptor bridge — REVIEW M-goopg-2). Honour
  `coltypeinfo.go:12-25`'s DDL-staleness contract. **On landing:
  re-derive the site count from the compiler and record the delta
  against 05 §2 (05 §5).** The A-05 plan-pin hardening is owned by Track
  A — this item points at it and does not duplicate it.
  *design: 04 §3 (D-1), 03 §5 (TD-1); gate: 06 §3 MD-01 (agreement test +
  oracle spot-check); files: `coltypeinfo.go`,
  `pg18_user_catalog_rows.go`, new.*
- [x] **D-02 MD-02 R-1 audit — derived-column type fidelity.** Verdict
  **PROCEED**, 2026-09-05. Census over both suites (in-process planning,
  classifier extracted mechanically from both codec switches rather than
  transcribed): **0 declining columns of 160,302; 0 nodes of 5,876; 0
  retention sites of 985.** All eleven type names the corpora produce are
  Kind-stable, including the 10,731 derived columns.
  The load-bearing finding is a design correction that had to land first:
  04 §3.1's `packableType` definition ("has a named arm") would have
  declined every text column in both suites (13 + 50 `varchar`) and
  produced a **false STOP** — those types have no *named encoder* arm
  because the shared default IS their encoder. Corrected in 04 §3.1, along
  with two spellings the static pass missed (bare `"char"` splits on
  `Args`; the whole float family is kind-ambiguous via NaN/Inf).
  Honest qualifications, both recorded in the report: the margin is
  corpus-luck-sensitive (`date ± interval` in a SELECT list types
  `unknown`; TPC-H only writes it in WHERE), and the row-weighted half is
  formally UNMEASURED (fixture catalogs have no stats, so every PlanRows
  is 1.0) rather than measured-and-zero — moot, since any weighting of an
  empty set is zero.
  Three latent on-disk bugs ledgered per the standing obligation:
  `take3-D-02-enum-encode` (enum values cannot be encoded at all — loud
  failure from the index-scan output path), `take3-D-02-float-spelling`
  (bare `float` missing from two of the FOUR sibling type tables),
  `take3-D-02-jsonb-text` (json/jsonb stored as text varlena).
  Artifact: `analysis/minimize-datum/d02-type-fidelity-20260905/README.md`.
  *design: 04 §3 (D-2), §9.2; gate: 06 §3 MD-02; document only.*
- [x] **D-03 MD-03 `PackedTuple` + `PackedSlot`, unreachable.** Landed
  2026-09-05, ~1,950 LOC (740 code, 1,210 tests). PG MinimalTuple layout
  byte-exact (`t_hoff` on PG's HEAP scale per `heap_form_minimal_tuple`;
  the negative-offset trick deliberately NOT ported, `dataOffset()`
  subtracts instead); 4-byte hash prefix matching `spill.go`'s
  `WriteRowHashed` framing; `(nvalid, off)` watermark; `deformTo` calls the
  UNEXPORTED `decodeRowRangeInfo` (the exported wrapper hardcodes
  `info = nil` and would discard D-01's descriptor — REVIEW M-goopg-3).
  **No producer** — pinned by a repo-wide guard test.
  **R-0: the design's table of six sites was WRONG — there are seven.**
  Review found the `CTIDExpr` switch (`expr.go`) unarmed, which would have
  read `ctid` as NULL *silently*, after two other arms went to the trouble
  of propagating the tid there. An eighth site (`parallel_runtime.go`) is
  correct-by-default and is now documented rather than armed. Both are
  errata against 04 §9.1.
  **Second blocker fixed: a partially-deformed row could escape.** The
  error path set `nvalid = width`, so `Row()` early-returned and handed
  back a slice whose tail held the PREVIOUS tuple's values —
  `poisonDeformTail` cannot prevent it because it is off in production.
  `Row()`/`Materialize()` now fail closed on a latched error.
  Also fixed: the plain-column TOAST check was descriptor-WIDE while its
  message asserted a per-column fact (accepted a TOAST pointer in the
  `int4` of an `{int4,text}` row); and the source guard was vacuous — it
  matched the word "PackedSlot" in a comment, so deleting an arm kept it
  green. It now counts the construct per site and it CAUGHT the missing
  seventh arm when rewritten.
  R-7 (descriptor ownership) and R-8 (scratch reset = tuple Load, into a
  slot-private arena) both resolved as doc comments at the definitions.
  Review: REQUEST-CHANGES → all findings addressed (2 blockers, 4 majors).
  Gates: executor suite green with 25 new tests; TPC-H values 24/24 MATCH;
  plans byte-identical; plan-gate PASS; PP unchanged 6/14/2.
  Two ledger rows for narrowed scope: `take3-D-03-attcacheoff-descriptor-only`
  (descriptor half landed, consuming half not) and
  `take3-D-03-arena-generation` (a PARENT-context reset cascades and would
  invalidate a slot's values — a hard requirement on D-05, the first slice
  with a producer).
  Original scope note follows.
  **D-03 (original).** MinimalTuple
  layout, 15-byte header, `hoff`-relative accessors, 4-byte hash prefix;
  `(nvalid, off)` watermark over the real decode wrapper (call
  `decodeRowRangeInfo` or a new exported wrapper taking `info` — NOT
  `DecodeRowRangeIntoMctxPGTupleStyled`, which hardcodes `info = nil`;
  REVIEW M-goopg-3); **all six type-switch arms** + `attcacheoff` fast
  path + exhaustiveness tests moved from `spill.go`. **No producer.**
  Resolve R-7 (descriptor ownership past the operator) and R-8 (scratch
  reset point) before or in this slice — R-8 is the single most
  consequential unstated detail.
  *design: 04 §2, §6 (D-4), §9.1 (R-0), 03 §7.1 (TD-3), TD-2; gate: 06 §3
  MD-03 (a test per switch, watermark property test, escape check);
  files: `slot.go`, `opnode.go`, `expr.go`, `exprnode.go`,
  `operators.go`, `codec.go`, new.*
- [x] **D-04 MD-03.5 throwaway prototype — VERDICT: STOP, THE MODEL IS
  WRONG.** Run 2026-09-05; prototype built, measured, and deleted (tree
  clean). Stopping rule 05 §6, in its own words: **"batches unchanged →
  the model in D-3 is wrong. Fix the model before touching another site."**
  Four numbers: batches **4 → 4 UNCHANGED**; retained bytes −14.2% (join
  accounting) / −24.4% (live-heap `inuse_space` — the harness 05 §6 says
  does not exist was BUILT); wall time **+6.8% consistently** (n=7 per arm,
  distributions barely overlap, so a real penalty inside the noise band);
  allocations **+39%** — 05 §6's "unchanged by construction" prediction is
  **wrong for this tree**, `EncodeRowPGCtx` costs ~6 allocations per packed
  row against ~1 for the legacy retain. Values MATCH.
  **Two measured reasons the model is wrong:** (i) `avgVarBytes` is ~62%
  too high (model 194 B/row vs measured 120), and it dominates a term
  packing cannot touch — **correcting it alone takes nbatch 4 → 2 with no
  packing at all**; (ii) the model prices rows and ignores the table — peak
  live heap is 506 MB of hash-map buckets against 296 MB of rows, so **the
  largest consumer in this join is not the retention format**.
  **The premise is stale, which matters more than the verdict.** Q9 today
  is a parallel plan, ~15 s, batching join `orders` at Batches 4 — not the
  `2→1`/1098 B/63.8 s pre-state recorded below. EX1 narrowing already
  landed on this build half: **120 B/row, 2 columns**, so the bundle's ~5×
  width premise is **1.9×** post-EX1, on ~14% of the join's peak. And each
  of 5 workers builds all 1.5 M rows privately (sharing is declined for a
  spilling build) — a 5× multiplier no part of this bundle addresses.
  Artifact: `analysis/minimize-datum/d04-pack-prototype-20260905/README.md`.
  Ledger `take3-D-04-model-wrong`. Pack and unpack the Q9 build
  side behind a flag with a hardcoded descriptor, measure 05 §6's four
  numbers, **delete the code**. Tests MD-04's hypothesis (incl. the R-3
  encode-cost question — `encodeValuePGCtx` is ~617 lines per column per
  row) before ~900 LOC is sunk.
  *design: 05 §6; gate: values-diff only (the code does not land); not a
  commit to `master`.*
- [!] **D-05 MD-04 hash join — BLOCKED ON C-19 (Phase 5 parallel costing),
  established by five successive measurements 2026-09-05/06.** Summary
  before the detail: packing (D-04) left batches unchanged; fixing the entry
  width was correct and inert; an honest bucket size halved bucket heap but
  flipped Q14; narrowing the cost side fixed that and cost 10%; charging the
  build honestly cost 22% — and the last three all failed on ONE mechanism:
  goopg's cost model has no parallel dimension, so any term that makes a
  hash join dearer trades a real 5× parallel speedup for a modelled saving.
  Detail, in order:
  D-05 must not proceed as written. Ordered prerequisites now, ahead of the
  original A-06/E-14/A-05 list:
  1. ~~**Fix `avgVarBytes`**~~ **DONE 2026-09-06, and it bought nothing.**
     The entry now reads 120.0 against 120.2 measured (`entrywidth.go`;
     `RelOptInfo.ColVarBytes` summed over the build's actual output schema,
     erring HIGH by falling back to the whole-relation sum). **But `nbatch`
     stays 4, refuting D-04's own prediction**: `nbatch` is NON-MONOTONE in
     entry size, because a smaller entry buys more buckets and the bucket
     array is charged too. Two batches need ≤111.8 B/row and two retained
     Datums plus their slice header are already 120 — and D-04's "ideal
     packed ~63 B/row" lands back on **4**, since the bucket array doubles
     and takes back more than the rows gave up. Timing-neutral (−0.4%,
     inside drift), values PASS, plans byte-identical, TPC-DS
     PASS=95/CKMISMATCH=0. Artifact:
     `analysis/minimize-datum/d05-entrywidth-20260906/README.md`.
  2. ~~**Charge the hash table**~~ **ATTEMPTED 2026-09-06, REVERTED, and
     D-04's premise here was ALSO refuted.** The buckets were *already*
     charged — `join_batch.go` pre-deducts `nbuckets*MapSlotBytes` from
     `spaceAllowed`, which is algebraically identical to PG's trigger. The
     real defect was that **`MapSlotBytes = 48` was a hand-derived guess
     and is 2× low**: measured against go1.25's swisstable runtime, a
     `map[string][]Row` slot costs **96.1 B** and `map[int64][]Row` **80.1 B**
     (Q9's `numeric` key takes the string lane × 1,048,576 slots × 5
     private worker builds = D-04's 506 MB).
     Fixing it to 96 (plus PG's `bucket_bytes <= budget/2` assert as a
     clamp) **halved the bucket heap: 586.7 → 286.0 MB live, per-worker
     peak −34.5%, batches UNCHANGED at 4, Q9 timing neutral, values 24
     MATCH** — and cost **+10.4% total** because **Q14 flipped Hash Join →
     Nested Loop (+3364%)**.
     Q14 diagnosed: it ran `Batches: 1` in BOTH arms — the executor never
     spilled. The planner priced a **9-column build the executor never
     builds**, and the honest bucket price pushed that phantom build 1.5%
     past the budget. So **prerequisite #2 is no longer the lever either:
     it is blocked behind the cost-side narrowing fix** (ledger
     `take3-D-05-costside-unnarrowed`), because until the planner prices
     the build the executor actually builds, correcting `MapSlotBytes`
     amplifies a phantom build across the budget line and the failure mode
     is **plan flips, not spilling**. With the narrowed input the same
     build sits at 53.4 of 134.2 MB — a 2.5× margin, not a 1.5% one.
     Patch preserved (`tmp/d05p2-bucket-charge.patch`); artifact
     `analysis/minimize-datum/d05-bucket-charge-20260906/README.md`.
  2b. ~~**`SpacePeak` should include the bucket array**~~ **LANDED
     2026-09-06.** `Memory Usage:` was reporting the SMALLER of the join's
     two memory terms — on the Q9 `orders` build it printed 44,026 kB of
     rows while omitting 98,304 kB of buckets, so it under-reported peak by
     more than half, and all four measurements in this chain were read
     against it. Reporting ONLY: the growth trigger is already correct and
     was deliberately not touched (`spaceAllowed` is pre-deducted by
     `nbuckets*MapSlotBytes`, making it algebraically identical to PG's
     test), with a counter-pin test asserting the trigger does not move.
     Gates: 2 new tests; executor + optimizer suites; TPC-H values 24/24
     MATCH; plans byte-identical; plan-gate PASS; PP unchanged.
  3. **Re-measure the premise.** The 5× width claim is 1.9× post-EX1, on
     ~14% of the join's peak. Whether that justifies ~900 LOC is a
     different question from the one the bundle was scoped against.
  **3rd and 4th measurements, 2026-09-06 — the blocker moved again, and is
  now precisely located.** The cost-side narrowing fix
  (`take3-D-05-costside-unnarrowed`) was implemented and measured: it makes
  the cost side agree exactly with the executor (Q9 `orders` 530.3 → 120.0
  B/row), errs HIGH, keeps values 24 MATCH, passes TPC-DS PASS=95
  CKMISMATCH=0, and moves plan parity **toward** PG — and it costs
  **+10.3%** (Q5 +216%, Q10 +162%, Q9 +74%, Q7 +62%; Q18 −19% and Q12 −12%
  the other way). Reverted.
  It also **confirmed the coupling hypothesis**: with it applied, the
  bucket-charge patch no longer flips Q14 (0.42 → 0.43 s, against 14.55 s
  alone). So prerequisite #2 is unblocked but free, and the pair still
  costs +11.1%.
  **Why: the inflated build width was an ACCIDENTAL DETERRENT.**
  `hashJoinCost` under-prices *building* a large hash table — it charges
  only `cpu_tuple_cost × rows` plus the child cost, modelling neither the
  five private per-worker copies (sharing is declined for a spilling
  build) nor the table memory. Q5/Q7/Q9/Q10 all flip WHICH SIDE IS BUILT
  once the deterrent is removed.
  ~~**The honest sequence is now: (1) charge what a build actually costs**~~
  **5th measurement, 2026-09-06 — done, REVERTED at +22.3%, and it names
  the structural root.** The build-cost charge was implemented from a
  derivation (5× participant multiplier on a spilling build, the executor's
  own sharing-decline rule; errs HIGH on spilling builds, exactly 1× on
  resident ones so Q14 cannot flip) and confirmed on a live witness: all
  five participants scan `orders` privately, Build Time 2979 of 8062 ms
  charged once. Values 24 MATCH, TPC-DS PASS=95 CKMISMATCH=0. Q5 +444%,
  Q10 +221%, Q9 +115%. **They did not lose a build-side choice — they lost
  PARALLELISM.** A Merge Join on the driving path makes the whole plan
  serial (`drivingScan` admits only a hash join under a Gather), and
  `MaybeAddGather` runs AFTER the search.
  **goopg's cost model has no parallel dimension.** Three
  individually-correct corrections (entry width, bucket charge, build cost)
  have now each failed on exactly this mechanism: every one transferred
  work away from a hash join, and in goopg a hash join is the only join a
  Gather can sit on. **Until the search can prefer a plan BECAUSE it will
  parallelise — C-19a…h — any term that makes a hash join dearer trades a
  real 5× speedup for a modelled saving.** D-05 is therefore blocked on
  C-19, not on its own cost terms. Ledger
  `take3-D-05-parallel-blind-costmodel`; artifact
  `analysis/minimize-datum/d05-buildcost-20260906/README.md`.
  **Correction 2026-09-06, AFTER E-09a landed (`67204579c`): the 5×
  multiplier in the 5th measurement's derivation is no longer true.** That
  charge was derived from "the executor's own sharing-decline rule" — a
  spilling build was NOT shared, so five participants each built the whole
  inner side. E-09a publishes a spilling shared build, and the acceptance
  witness confirms the change: every worker now reports
  `Seq Scan on orders rows=0.00 loops=0` against `rows=1500000.00 loops=1`
  before, with ONE `Build Time` instead of five. So the build-cost term
  that measured +22.3% was pricing a 5× cost that the executor no longer
  pays; on a spilling build the correct multiplier is now **1×**, the same
  as the resident case.
  This does NOT by itself unblock D-05, and it must not be read as one:
  the structural finding stands — the search still cannot prefer a plan
  BECAUSE it will parallelise, and the entry-width and cost-side-narrowing
  measurements (+10.3%, and the Q5/Q10/Q9 parallelism losses) did not
  depend on the multiplier at all. What it does change is that **one of
  the three refuted corrections was refuted against a premise that has
  since been fixed**, so the build-cost charge is worth re-deriving and
  re-measuring at 1× once C-19d lands — before, not after, the packing
  work is re-scoped. Preserved patch `tmp/d05p4-*.patch`; re-derive rather
  than re-apply, since its multiplier is now wrong.
  MEMORY caveat, RESOLVED by E-09b (`d5ce1bb9b`): E-09a shared the BUILD
  but not the reloaded batch, so D-04's 506 MB live map was still five
  maps; E-09b makes it one (measured maxLiveLoads 1 vs 4 with four
  participants). Any memory term in a re-derivation must still state which
  of the two it is charging, and must now charge ONE reloaded batch, not N.
  Two cheaper facts from the same run: the bucket-array term decides
  NOTHING on TPC-H (zero plans move at multiplier 1) and is dropped from
  this list; and the narrowing patch narrows the INNER only while PG
  narrows both sides of `page_size` — a cheap independent defect.
  Earlier ledger `take3-D-05-buildcost-underpriced`; artifact
  `analysis/minimize-datum/d05-costside-20260906/README.md`.
  Also required by D-04's evidence but out of this bundle's stated scope: a
  dense byte arena plus an allocation-free encoder, since packing every
  build row while deforming only matches costs +39% allocations.
  Original blocker text follows.
  **D-05 (original).**
  Serial and parallel in one commit, `hashsize` model re-derived in the
  same commit. The load-bearing EX1 dependency for hash-join geometry is
  the **build half** (E-14 redesign), not the sort half; EX0 is landed.
  Consumes the EX2-01 audit
  (`analysis/planner-refactor-take3/ex201-audit-20260904/README.md`;
  either EX2-01 lands first or MD-04 produces the audit for the sites it
  touches — EX2-01 landed, so consume it). F-01/F-02 measure first (graph
  edges above).
  **Two corrections applied before starting:** (a) the gate is restated
  over the hash-join operator on a named class of shapes (multi-join
  hash cascades over million-row builds at PG `work_mem`, Q9 reported as
  the example — never the gate), satisfying take3 EX-P2 which forbids
  single-query gates; (b) the missing fifth retention lane — the
  composite multi-key lane (ledger M0127), unbounded, packed-byte keys —
  joins Tier A. Report against the **measured** pre-state (Q9 witness
  `Batches:` 2→1, narrowed width ≈100; ledger `take2-executor-residual`:
  widths 1098 B vs PG 23 B, 97 MB vs 38 MB peak, 63.8 s vs 6.2 s), not
  the modelled 128/64.
  **Stopping rule 05 §6: if the measurement says stop, revert MD-04 —
  do not keep it and stop (R-4).**
  *design: 04 §4.1, §5 (D-3), §9.4 (R-3); gate: model-vs-reality test
  (`hashsize.EntryBytes` predicts retained bytes/row within tolerance) +
  batch-count movement on the named shape class + 06 §2 floor with
  `CKMISMATCH=0`; files: `operators_join_agg.go`,
  `parallel_hash_build.go`, `hashsize/hashsize.go`.*
- [!] **D-06 MD-05 sort — BLOCKED on B-01c (sort-side projection).**
  Gate checks **ordering explicitly**, not membership
  (`operators.go:1010-1015`: mismatched sort/merge comparators emit
  out-of-order rows with no error). Needs two deformed rows at once; a
  `PackedSlot` has one scratch `Row` — R-11, re-priced, not mechanical.
  *design: 04 §4.1; gate: 06 §3 MD-05.*
- [!] **D-07 MD-06 materialize — BLOCKED by the D-04 stopping rule**
  (2026-09-06). 05 §6 is explicit: "batches unchanged → the model in D-3 is
  wrong. **Fix the model before touching another site.**" D-04 fired that
  arm, and the five follow-up measurements located the model defect in
  parallel costing (C-19), not in the row format. Converting a second site
  before the first one's premise is re-measured would be exactly the R-4
  two-formats hazard 04 §0.2 forbids. Unblocks when D-05 does.
  *design: 04 §4.1; gate: 06 §2 floor + alloc arm.*
- [!] **D-08 MD-07…MD-12 Tier B [EPIC — split per site before start] —
  BLOCKED by the D-04 stopping rule** (same reasoning as D-07; the split
  rule still binds when it unblocks).
  Window (whole partition set, no spill path — report OOM-exposure
  reduction, not batch counts; goopg has no tuplestore, 04 §4.2); memoize
  + CTE cache + recursive worktable + lateral CTE (recursive-CTE
  lifetimes are the risk; resolve R-7 first); Gather + Gather Merge
  (serial control + worker arms); RETURNING buffers + `conn_tx.Rows`;
  outer-join sweep + agg group representative (four `[][]Datum`
  accumulators **stay Datums** — Tier C boundary); ~13 small `rows []Row`
  sites (mechanical, low value; one commit or dropped).
  *design: 04 §4.1, §8; gate per slice: 06 §2 floor + alloc arm; MD-09
  adds the parallel arm
  (`parallel_substrate_test.go:26-80` pattern).*
- [x] **D-09 MD-1x conditional alignment.** Landed 2026-09-06:
  `att_align_datum` (encode-then-place, short-header-form test) +
  `att_align_pointer` (shared `catalog.AttAlignPointer` peek in heap
  codec ×2 paths, pgoutput walker, pg_index text tails + vectors) +
  per-column `attstorage` (override over type default, inline
  resolution, no cache) + `PhysicalTypeIsVarlena` moved to catalog.
  On-disk format changed (column padding only); upgrade-direction
  compatible, downgrade unsupported (stated). Gates: placement/peek/
  backward/forward/override/round-trip/live-golden tests (goopg bytes
  byte-identical to live PG 18.3 328B tuple); suites (executor,
  catalog, xlog, initdb, optimizer) + units scope + spotcheck PASS;
  TPC-H values 24/24 MATCH; TPC-DS 95 PASS CKMISMATCH=0; plan-gate
  22/22 MATCH; review APPROVE-WITH-NITS → all fixed → re-review
  APPROVE (incl. pg_index walker + align-table ledger rows).
  Artifacts: `docs/design/executor-d09-alignment/DESIGN.md`,
  `016f67b`.
- [!] **D-10 MD-last spill payload — BLOCKED on every in-memory
  retention site.** Convert `spill.go`'s payload to the PG format;
  framing (`WriteRowHashed`'s hash-then-tuple) unchanged. The nine
  existing test functions are **re-pointed at the new codec, not
  deleted**. A site SKIPped under §8 unblocks D-10 only if the SKIP
  records it explicitly out of scope with a ledger row; an
  unrecorded site keeps D-10 blocked.
  *design: 03 TD-5, 04 §4.1 Tier D; gate: 06 §3 MD-last + round-trip
  across a real spill on a spilling shape.*
- [!] **D-11 MD acceptance + open-gap ledgering — BLOCKED** until at least
  one conversion site (D-05 onward) lands; today the "one retention
  format" condition is trivially true because none has. The open-gap
  ledgering half is ALREADY DONE by the chain of D-04/D-05 measurements
  (`take3-D-04-*`, `take3-D-05-*` rows) and by D-02's three latent-bug rows.
  Original text: 06 §5 six conditions:
  values unchanged every commit both suites; one retention format (Tier A
  + significant Tier B + spill; R-4 closed by construction); model
  matches storage (test); batch-count witness moved and recorded;
  **`Datum` still 48 bytes** (`datum.go:187` untouched); byte-format
  fidelity stated honestly (D1 goldens / D2 ledgered open / D3-D4
  non-issues). No time target. Also files the ownerless ledger rows: the
  PG-format TOAST-pointer gap (03 §4 D2 — out of scope, resume point
  required) and any D-08 slice deferred with a keep-open row.
  *gate: 06 §5.*

---

## 6. Track E — Executor remainder (take3 bundle)

*Gates: TODO_EXECUTOR ground rules (no query-specific forcing; timing +
alloc arms together; plan-shape pin `changed=0`; both suites fresh server
per arm; values never counts for projection/join-adjacent changes).*

- [!] **E-01 EX3-04 sort spill runs + merge discipline — BLOCKED on the
  EX1 sort half (B-01c).** Run formation on `flushChunk`, tape-style
  merge-back (logtape analogue, take3 10 §9). Spill thresholds are
  batching geometry: on pre-EX1 widths this is premature by rule (take3
  13 §8.2, EX-P7).
  *design: take3 13 §5; gate: spilling-sort shapes; values + pin.*
- [!] **E-02 EX3-06 skew residency + single-pass build — BLOCKED on B-16 +
  EX1 exit.** MCV-pinned hot keys (consumes planner B-16 input) +
  collapse two-pass/two-map build. Skew-residency sizing is an §8.4 scale
  argument over narrowed rows — last in EX3 for that reason.
  *design: take3 13 §5; gate: skewed-shape A/B; values + pin + alloc
  arm.*
- [x] **E-15 EX3-07 input-contract publication spike (unconditional).**
  Landed 2026-09-05: presorted-prefix contract (input ordered by first-n-keys + contiguous groups + `sortPrefixEqual` framing; executor return: order-equivalence + group-bounded memory; incomparable splits) as doc + `sortPrefixEqual` + order-equivalence oracle vs current full sort (incl. NULL/DESC-first-key variant). Breaks the C-14/E-03 circular wait. Gates: 4 contract tests; executor + optimizer suites green; review APPROVE-WITH-NITS (5 addressed). No behaviour change (pure additive, zero production callers). Artifacts: `docs/design/executor-ex3-07-presorted-contract/DESIGN.md` (`065182d`), `internal/executor/sort_presorted{,_test}.go` (`c7f1f45`).
- [-] **E-03 EX3-07 presorted-prefix implementation — RESOLVED 2026-09-07
  by C-14's verdict: its condition will not be met.** The row was `[!] file
  ONLY if planner C-14 activates`; **C-14 is DROPPED on measurement**
  (Q67 — PG's own Incremental Sort case — sorts in **1.26 ms** over 376,552
  rows, 3 reps at `d0b4f96e4`; corpus-wide all sorting is ≤ 119.8 ms of
  802 s and 0 of 100 sorts spill), so there is no planner consumer to file
  this against and it is closed rather than left dangling. E-15's contract
  (`sortPrefixEqual` + the order-equivalence oracle,
  `docs/design/executor-ex3-07-presorted-contract/DESIGN.md`) STAYS landed
  and inert — zero production callers, no behaviour change — and is exactly
  what a future C-14 would build against, so reopening C-14 reopens this
  row with its prerequisite already paid. Ledger `take3-C-14-dropped`.
- [!] **E-14 EX1 build-half redesign (no second truncation) — Cut A
  DROPPED on measurement 2026-09-07, Cut B quantified and still blocked.**
  **Cut A (Semi/Anti zero-width retention): DROPPED, 0.0065%.** §8b had
  already found P4-01 narrows a Semi/Anti build to `keys ∪ residual`,
  leaving only the zero-width case; §3.3 sized that at
  `24 + w*48 + payload` → `24` B/row and named Q4/Q16/Q18/Q20/Q21/Q22 as
  the shapes that would pay. The whole-suite census says they do not. In
  22 TPC-H queries there are **4 Semi/Anti hash builds** against 43 INNER
  ones; **every one already has `buildWidth = 1`** (P4-01 narrowed them to
  the single key column, exactly as §8b predicted); total Semi/Anti rows
  retained across the suite is **14,747 = 14,747 Datum cells = 0.7 MB**,
  against **10,954 MB** of retained hash-build cells suite-wide — **one
  part in 15,000**. The named shapes do not pay because they are not
  Semi/Anti hash builds in goopg's plans at all: Q4/Q18/Q20/Q21/Q22 emit
  INNER hash joins here. A width change on retained rows is the "0 rows on
  seven TPC-H queries" class, and this one buys 0.0065%. Unlike C-13a's
  deferral the evidence cannot improve with a different corpus — only with
  a planner that emits Semi/Anti hash joins where it currently does not.
  **Cut B (INNER keep-set + the above-walk): the prize is now MEASURED,
  and it is where all the mass is.** Suite-wide retained hash-build
  storage is **16,803,718 rows / 228,212,960 Datum cells / 10,954 MB**, of
  which **99.99% is INNER**; the largest single site is **6,001,255 rows ×
  16 columns = 4,609 MB**, where each dead column is 288 MB. The design's
  own §2 measurement (6 of 15 retained columns are dead key columns on the
  Q9 fixture) puts the corpus prize at order **4 GB** of retained Datum
  cells. That number did not exist before this session — the item said only
  "deferred for want of an alloc + batch-geometry arm".
  **Cut B still not taken, for two specific reasons rather than
  circumstance:** (1) its GEOMETRY half is forfeited at HEAD by
  construction — §5.1: `hashsize.EntryBytes` prices the build node's
  SCHEMA width, so after narrowing the planner still sizes batches for
  storage the executor no longer holds, and the `nbatch` 4→2 that E-12 and
  D-05 are sequenced behind is unreachable until `EntryBytes` prices the
  RETAINED width (the D-05 follow-up the design already files). Landing
  Cut B first buys the memory and none of the geometry. (2) Its acceptance
  is values + batch geometry on SPILLING shapes, and three peer agents held
  the host at load 18–30 for the entire session — the one arm that can
  accept it is the one arm that could not be taken, and a row-count gate
  cannot substitute (21 of 21 byte-identical while Q2 went 43× slower).
  Mechanism/resume unchanged (§8: generalise
  `scan_deform.go:deformJoinBounds` from a prefix bound to a SET, threaded
  through every `buildNode`/`buildRec` arm with both build paths agreeing;
  seam is `ensureLazyVirtual`'s `virtualCol` map plus a NULL-pad source).
  Census instrument, aggregator, arm logs and write-up:
  `analysis/minimize-datum/tracke-e07-e13-e14-e09c-20260907/`; ledger
  `take3-E-14-cutA-dropped-cutB-quantified` (supersedes
  `take3-E-14-cutA-cutB-deferred`).
  Prior record, unchanged below. Design
  landed 2026-09-06 (`docs/design/executor-e14-build-half/DESIGN.md`)
  with the residual MEASURED, plus the structural prerequisite both cuts
  needed; the narrowing itself is deferred with a resume point
  (`take3-E-14-cutA-cutB-deferred`). Note the Cut-0 figures in the old
  wording (Q9 20.14→13.88 s, alloc 9.43→8.52 GB) were the **P4-01
  planner flip**, measurement-only — that prize is already banked, not
  pending.
  - **Residual, measured** on the in-package Q9 fixture (`obpQ9Catalog`):
    after P4-01, **6 of the 15 retained build columns across Q9's five
    hash joins are this join's own key columns that nothing above reads**
    — and every one of them sits at position 0 of its build side, so the
    declined `[0,bound)` prefix could not express the opportunity even if
    it were safe. The replacement shape is a keep-SET gather with the
    reader coordinates moved at ONE seam (the join's `virtualCol` map,
    with a NULL-pad source for dropped positions), so the composed width
    never shrinks and no reader above the join changes.
  - **Prerequisite LANDED, inert:** keyed INNER spill frames
    (`[4B hash][1B tag][key][payload]`). `loadInnerBatch` used to recover
    a reloaded row's key by re-evaluating the build key EXPRESSION
    against it (`buildKeyOfRow`), which pinned the key columns live in
    every retained row and defeated both cuts — and could not be dodged
    by declining when batching, since `presizeLazyHash` installs a batch
    state for every eligible join, `nbatch==1` included. Now the
    canonical key travels in the frame (PG's `ExecHashJoinSaveTuple`
    rationale, "so that we needn't recompute it"); `buildKeyOfRow` and
    `batchKeySlot` are retired and nothing derives a build key from a
    RETAINED row. Behaviour-identical, one expression evaluation per
    reloaded row cheaper. Gates: executor suite + units scope green,
    25 batching/spill/shared-build tests uncached green, `-race` on the
    parallel hash set clean, **TPC-H SF=1 values 24/24 MATCH** on a fresh
    capped server (`analysis/minimize-datum/e14-keyed-inner-frame-20260906/`;
    the spilling labels Q9/Q16/Q18/Q21 are among them).
  - **Deferred:** Cut A (Semi/Anti zero-width retention) and Cut B
    (INNER keep-set + the `deformJoinBounds` walk generalised to a set
    and threaded through `executor.go`). Both are width changes on
    retained rows whose acceptance is values + batch geometry on
    SPILLING shapes, and the TPC-H cluster was held all session. Cut A's
    remaining value also shrank on inspection: P4-01 already narrows
    Semi/Anti builds to `keys ∪ residual`, so only dropping the keys
    themselves is left. Also reported, not fixed: `hashsize.EntryBytes`
    prices the SCHEMA width, so post-narrowing the planner
    over-estimates (conservative, but forfeits part of the geometry win)
    — belongs with D-05.
  *design: `docs/design/executor-e14-build-half/DESIGN.md` (§4a blocker,
  §8 Cut B, §8b Cut A resume point); superseded input:
  `docs/design/executor-ex1-04-owned/DESIGN.md` + unblock review
  (`134324df6`); gate for the deferred cuts: poison tests on the Project
  shape (Cut 1 pattern) + alloc arm + values + pin.*
- [-] **E-04 EX4-01 `filterOp` compilation (serial).** DROPPED 2026-09-05
  after implementing and measuring three variants (compile-per-Open;
  slab cached across re-Opens; adapter-root declined). Values 24/24 MATCH
  and plans byte-identical on every arm, but **no repeatable gain and a
  consistent TPC-H Q18 +8.5%** (34.48/34.62/34.82 s against two baselines
  at 31.93/31.87 s). Root cause of the non-result is structural, not a
  bad attempt: the item's own predicted effect is ~0.33 pp (take3 13 §6),
  an order of magnitude below the ±17% protocol band, and the mechanism
  overlaps `seqScanOp`'s prefilter, which already compiles the same
  predicate pre-deform — so `filterOp` above such a scan only sees
  survivors. Report:
  `analysis/executor-refactor/e04-filterop-compile-20260905/README.md`;
  ledger `take3-E-04-dropped` (resume conditions inline).
- [x] **E-05 EX4-02 join residual + key compilation.** Landed 2026-09-06:
  merge twin of the PS6.1 hash slab (separate `mergeExprs` slab by
  discipline; `initMergeKeys` compiles + `ensureMergeExprs` guard;
  hoisted dedicated residual slot, never `o.slot`; nil-schema
  asserted). Wrapper logic untouched (nil→true, NULL→false, nullKey
  path) — pure dispatch swap at 2 sites. Gates: 12-case twin-parity
  corpus (extended outcome harness + mergedKeySlot in shared list) +
  noExpr pin + compiled-path + 0-alloc assert; suites + units scope;
  merge-join EXPLAIN-confirmed live; TPC-H values 24/24 MATCH (timing
  neutral x0.998); TPC-DS 95 PASS CKMISMATCH=0; plan-gate 22/22;
  spotcheck PASS; review APPROVE-WITH-NITS (7 findings fixed).
  Artifacts: `docs/design/executor-e05-merge-compile/DESIGN.md`,
  `39d26c5`.
- [x] **E-06 EX4-03 agg transition fast path.** Landed 2026-09-06:
  per-call slab (`aggExprs` + node lists parallel to `plan.Aggs`,
  whole-call UserAgg decline) with dispatch swap at all builtin
  transition sites (Filter/Arg/Arg2/Extra/WithinGroup/order-keys/
  delim/regr; UserAgg + group-keys + finishAgg untouched); wrapper
  logic and per-site error handling verbatim. Gates: twin-parity
  corpus + wiring/decline/order-key-failure/per-site-Arg2/0-alloc
  pins; suites + units scope; merge-confirmed parallel Gather shapes
  in R8 arms; TPC-H values 24/24 MATCH (timing neutral after
  noise re-run); TPC-DS 95 PASS CKMISMATCH=0; plan-gate 22/22;
  spotcheck PASS; review APPROVE-WITH-NITS (8 findings fixed).
  Artifacts: `docs/design/executor-e06-agg-compile/DESIGN.md`,
  `6aab9dc`.
- [-] **E-07 EX5-01 slab parity for `Gather` — DROPPED 2026-09-07 on its
  own resume condition.** The item's last surviving justification was "slab
  dispatch beats the legacy `Operator` interface on worker trees", and its
  resume point demanded that delta be measured on a parallel witness shape
  FIRST and implemented only if it cleared the noise band. **Measured: it
  does not clear the band, and its sign is negative.** Witness
  `BenchmarkE07WorkerDispatch{Legacy,Slab}`
  (`internal/executor/e07_worker_dispatch_bench_test.go`) drives the SAME
  plan — `Project(Filter(SeqScan))` over a 50,000-row heap, i.e. the exact
  three-migrated-node chain a TPC-H worker subtree has (`OpSeqScan` →
  `OpFilter` → `OpProject`, confirmed by walking the slab) — through
  `Build`+`Operator.Next` and through `BuildFast`+`opNext`, 8×30 iterations
  each, same binary, same process. Legacy median **27.18 ms/iter**, slab
  median **27.84 ms/iter**: the slab is **+2.2% SLOWER**, ranges overlapping
  (legacy 26.86–28.82, slab 27.37–28.30), allocations identical to 5 parts in
  150,969. The structural reason matches E-04's: per-row work on this shape is
  544 ns (heap read + deform + expression eval), so three saved interface
  dispatches (~2–4 ns each) put the ceiling at ~1.8% — below the band before
  a single line is written. Justifications (a) "unlocks E-08" and (c)
  "re-proves EX4 wins" were already void with E-04/E-08; (b) "re-proves EX1
  wins on workers" is already true because `buildNode` threads the EX1-01
  deform bound. Nothing survives. Benchmark retained as the re-check
  apparatus (E-11 precedent). Ledger `take3-E-07-dropped`.
  Superseded premise (re-verified, still true): workers really do take the
  legacy path (`BuildWorker` → `buildNode`,
  `executor.go:32`), and `buildRec`'s default arm wraps `Gather` in an
  `OpAdapter`, so the slab does not reach a parallel plan.
  What changed: (a) "unlocks E-08" is void — E-08 is EX4-04, parallel
  `filterOp` compilation, and its serial twin E-04 was **dropped on
  measurement** (no gain, consistent Q18 +8.5%), so the thing E-07 exists
  to unlock has no value left; (b) "re-proves EX1 wins on workers" is
  already true without it — `buildNode` threads the EX1-01 deform bound, so
  worker trees carry the narrowing today; (c) "re-proves EX4 wins" is void
  with E-04.
  What survives: the independent claim that slab dispatch beats the legacy
  `Operator` interface on worker trees. That claim has never been measured
  on the parallel path and is NOT inherited from the serial slab result.
  **Resume: measure the dispatch delta on a parallel witness shape FIRST**
  (the E-04 lesson: a sub-1% predicted effect cannot be read off a suite
  total), and only implement the `Gather` arm if it clears the noise band.
  *design: take3 13 §7; gate: parallel slab coverage test; serial arm
  unchanged; pin.*
- [-] **E-08 EX4-04 `Gather`-arm slab reachability.** DROPPED 2026-09-05
  by dependency, not by its own measurement: E-08 IS "compile `filterOp` on
  the parallel path", and the serial twin E-04 was dropped after three
  implemented variants showed no repeatable gain and a consistent TPC-H
  Q18 +8.5% against two agreeing baselines. Compiling the same predicate in
  a worker cannot be worth more than compiling it in the leader, and the
  structural reason for the serial non-result — the predicted effect is
  ~0.33 pp, an order of magnitude below the noise band, and the mechanism
  overlaps `seqScanOp`'s prefilter — applies unchanged to workers.
  Resume only if E-04 is ever resumed and clears its own gate on a witness
  shape. Ledger `take3-E-08-dropped`.
- [x] **E-09 EX5-02 shared build hardening — CLOSED 2026-09-07 by its three
  children; the parent carries no residue.** SPLIT 2026-09-06 into the
  three slices below after a static feasibility analysis of the spilling
  case (ledger `take3-D-04-private-worker-builds`: on Q9 all five Gather
  participants build the whole 1.5 M-row `orders` table privately, a 5×
  multiplier that dwarfs everything the minimize-datum bundle proposes on
  the same query). Design:
  `docs/design/executor-e09a-shared-spilling-build/DESIGN.md`.
  **Closure audit (2026-09-07):** the parent's own text is the split note
  and the design pointer — it states no requirement outside the three
  slices, and the 5× private-build multiplier that motivated it is exactly
  what E-09a removed. Children: **E-09a** `67204579c` (spilling shared
  build published — Q9 workers `rows=1500000 loops=1` → `rows=0 loops=0`,
  five Build Times → one, 8.85 → 7.85 s); **E-09b** `d5ce1bb9b` +
  `b5647d6fa` (load-once-per-batch — one live batch table where there were
  four, `loadCount` 28 → 7, `maxLiveLoads` 4 → 1); **E-09c** delivered
  2026-09-07 as its measurement (`455de27aa`) — the cooperative build is
  **consumer-bound and saturates at ~4 producers** (2→4 buys 14% of build
  wall, 4→8 loses 23%; `recvWaits` = 0 at ≥4), and **skew does not move the
  bottleneck**. Nothing in the parent is left unaddressed; the one thing
  the children ROUTED rather than made — `maxProducers =
  ctx.MaxParallelWorkers` with no ceiling above the saturation point — is
  carried by ledger `take3-E-09c-consumer-bound`, not by this row.
- [x] **E-09a Publish a SPILLING shared build (Variant A, private
  reload).** LANDED `67204579c`. `captureSharedBuild` carries an immutable batch descriptor
  (`nbatch`, `bucketBits`, `nbuckets`, `buildIsLeft`, read-only inner
  files 1..n-1); growth frozen after prebuild (PG's own rule); each
  participant gets a private `hashBatchState` and reloads batch k by
  opening its OWN reader on the shared inner file into its own fresh map.
  **No new synchronisation** — goopg partitions the probe by scan block,
  so shared OUTER files and batch-level work distribution are not needed.
  Does NOT depend on E-07. Must land BEFORE C-19f (whose price must
  describe this executor) and before D-05 is re-measured.
  *gate: forced-shape values tests per join type at batching work_mem +
  poison-writer + exactly-once-open counters + -race + cancellation;
  plans byte-identical; acceptance witness = Q9 workers show
  `Seq Scan on orders loops=0`, one Build Time. ~250–470 LOC, HIGH risk
  (silently partial join).*
  Gate result: every clause above met. Acceptance witness measured —
  HEAD `Worker 0..4: rows=1500000.00 loops=1`, Build Time 4307.315 ms,
  Execution 8.85 s; E-09a `rows=0.00 loops=0` in every worker with one
  `Batches: 4`, Build Time 2978.957 ms, Execution 7.85 s. Combined gate
  with C-19c/E-11: TPC-H 24/24 MATCH, plans byte-identical, plan-gate
  exit=0, TPC-DS PASS=95 CKMISMATCH=0, spotcheck RESULT=PASS
  (Q12=2, Q13=34), `-race` green. Unblocks C-19f and the D-05
  re-measurement; E-09b (load-once-per-batch) LANDED on top, `d5ce1bb9b`.
- [x] **E-09b Load-once-per-batch (Variant B).** LANDED `d5ce1bb9b`
  (mechanism) + `b5647d6fa` (gate); design
  `docs/design/executor-e09a-shared-spilling-build/DESIGN-E09b.md`
  (`2662a2183`). The shared descriptor carries one `sharedBatchLoad` slot
  per batch: the first participant to reach batch k loads it, every other
  one adopts the SAME maps behind a `ctx.Done()`-aware wait, and a refcount
  frees them when the last holder leaves — PG's `PHJ_BATCH_LOAD`/`FREE`
  analogue **without the barrier**, because goopg partitions the probe by
  scan block, so a participant never waits for another to ARRIVE, only for
  a load already in flight to FINISH.
  **Not** `sync.Once`, deliberately: `Once.Do` marks the slot done when its
  function RETURNS, so a loader that returned early (cancelled, or having
  recovered a panic) would publish an EMPTY table and every waiter would
  silently probe nothing — this item's wrong-answer class. The slot is a
  channel closed by a `defer` with `err` pre-set to
  `errSharedBatchAbandoned`, so a panicking loader hands waiters an error
  rather than a channel nobody will close.
  **Why it cannot deadlock:** the loader never waits and never observes
  cancellation — its work is a bounded local file read with no channel
  operation, exactly as uninterruptible as Variant A's private reload
  already was — so `done` always closes and its closure depends on the
  filesystem, never on a peer. Waiters select on `done` AND their own
  `ctx.Done()` (57014 via `lockWaitCancelError`); the reference is taken
  under the descriptor mutex BEFORE the wait and dropped on every exit path.
  Freeing on `refs==0` CLEARS the slot, so a straggler re-loads from the
  still-linked file — which bounds the worst case at exactly Variant A's
  load count.
  Gate result: full `go test ./internal/executor/` green; `-race` green
  (the single `-race` failure, `TestSubquerySemanticsMatrix/M20`,
  reproduces unchanged at E-09a `67204579c` —
  `buildUnderNilScope` vs `maybeInstrument` on the coop parallel build's
  producers, ledger candidate, untouched here). Cancel-mid-batch is tested
  twice: at the protocol level (`TestSharedBatchLoadCancelsWaiters` — the
  loader still parked, every waiter returns 57014, no leaked reference) and
  through the real Gather (`cancelled-with-a-waiter-parked`, which holds
  the loader until `d.waiting >= 1` before cancelling). Memory evidence,
  measured with every participant held at the same batch
  (`TestSharedSpillingBuildLoadsOncePerBatch`, 4 participants, 7 batches):
  **loadCount 7 where Variant A is 28, maxLiveLoads 1 where Variant A is
  4, maxLiveBytes 140,775 where Variant A is ~563,100** — one live map
  where there were four. Mutation-checked: stubbing `releaseHeldBatch` to a
  no-op takes `maxLiveLoads` to 7 and the gate fails. In-situ (unbarriered)
  figures on the existing shapes: loadCount 7–10 vs 21–35, maxLiveLoads 2–3
  vs 3–5.
  E-09a's exactly-once-open invariant is RESTATED, not deleted: it read
  "every participant opens each inner file once", which *is* the
  multiplier. It now reads "every open is a claimed load
  (opens == `loadCount`), no participant opens a batch twice, and no batch
  is loaded more times than there are participants".
  **Bench measurement still owed to the gating pass** (the cluster was held
  by another agent): TPC-H Q9 `EXPLAIN (ANALYZE)` at bench `work_mem` on
  port 65433, five participants, comparing `Memory Usage:` and peak RSS
  against the E-09a witness (`Batches: 4`, Build Time 2978.957 ms,
  Execution 7.85 s) — E-09b must not regress execution time and should
  show the reload peak once rather than five times; plus the standard
  combined gate (TPC-H 24/24 values, plans byte-identical, `make
  plan-gate`, `scripts/tpch-spotcheck.sh`, TPC-DS SF0.5 sweep).
- [x] **E-09c Cooperative-stall measurement under skew + worker-count
  scaling on Q9-class shapes.** DELIVERED 2026-09-07 — this is a
  measurement item, so the measurement is the deliverable. Apparatus is
  in-process (host contention hits every arm equally, which matters: three
  peer agents held the machine at load 18–30 all session): a temporary
  instrument charges blocked time to whichever half of the cooperative
  parallel hash build actually waited — `producerBlockedNs` (parked on
  `ch <- batch`) vs `consumerBlockedNs` + `recvWaits` (the single inserting
  leader parked on `<-ch` with the channel empty). Fixture: a 300,000-row
  build under a 330,000-row probe through `parallelBuildLazyHashTable` at
  `work_mem` = 1 GiB (single batch — the coop path declines at `NBatch>1`),
  2/4/8 producers × uniform and 10:1-skewed build keys, 3 reps
  (6 observations per producer count). Raw log + write-up:
  `analysis/minimize-datum/tracke-e07-e13-e14-e09c-20260907/`.
  | producers | build wall (median) | producer blocked TOTAL | per producer | consumer blocked | recvWaits |
  |---|---|---|---|---|---|
  | 2 | 161.9 ms | 65.8 ms | 32.9 ms (20% of build) | 6.0 ms | 8–110 |
  | 4 | **139.6 ms** | 334 ms | 83.5 ms (**60%**) | 0.0 ms | 0–1 |
  | 8 | 172.0 ms | 1059 ms | 132 ms (**77%**) | 0.0 ms | 0 |
  **Result: the cooperative build is consumer-bound and saturates at about
  four producers.** 2→4 buys 14% of build wall; 4→8 LOSES 23% and costs
  3.2× more producer stall. At ≥4 producers the consumer never waits at all
  (`recvWaits` = 0 in 10 of 12 observations, 1 in another) — the single
  goroutine that evaluates keys and inserts is the whole critical path.
  **Skew does not move the bottleneck**, because the bottleneck is already
  entirely at the consumer: the 10:1 skewed arms have the same signature
  (149.0 / 134.8 / 129.4 ms at 2/4/8 in rep 1). That is the skew half's
  answer, stated as the negative result it is.
  **Routed, NOT made (a tuning default, and this item's mandate is to
  measure):** `parallelBuildLazyHashTable` sets
  `maxProducers = ctx.MaxParallelWorkers` with a floor of 2 and NO ceiling,
  so a cluster configured for 8 workers spawns 8 producers and 8
  `mmgr.Acquire` arenas for a build that stops improving at 4. Ledger
  `take3-E-09c-consumer-bound`.
  *design: take3 13 §7; gate (discharged): scaling + skew A/B.*
- [x] **E-10 EX5-03 Gather/GatherMerge ordering + exchange.** Correctness
  half DONE 2026-09-06; performance half **out of scope, verified**.
  *design: `docs/design/executor-e10-gather-merge-claimset/DESIGN.md`
  (take3 13 §7).*
  C-19f's reported latent wrong-answer **reproduced**: `gatherMergeOp`
  attached only `attachParallelScan`, never the index/bitmap claim sets,
  so a Gather Merge over a partial INDEX path returned `(workers+1)`
  copies of every row **in the correct order** — 5802 / 8703 / 14505 rows
  at 1 / 2 / 4 workers against a serial 2901. Fixed by hoisting all claim
  state into one `parallelClaimSet` (`attachAll` + `prebuildBitmap`)
  embedded by BOTH `gatherOp` and `gatherMergeOp`, so a future claim kind
  cannot be wired into one sibling only; `TestParallelClaimSetAttachesEveryKind`
  fails on a field added without an arm. Second bug found in the same
  function and fixed: `gatherMergeOp.runWorker` deferred `close(chan)`
  AFTER the child build/Open, so either failing left a live channel with
  no closer and `Close` parked forever draining it — M0127-P5.9's Q17
  hang, which `gatherOp` had fixed and this sibling had not.
  Gate: `TestGatherMergeOverParallelIndexScanIdentity` (values, each row
  exactly once ascending, ≥2 leaf blocks claimed, workers 1/2/4), full
  `go test ./internal/executor/` + `-race` on the parallel set,
  units gate, `scripts/tpch-spotcheck.sh` RESULT=PASS,
  `tpch-runner -diff` **24 MATCH** on values.
  Performance half declined with numbers: no sort-side runtime witness
  exists on either corpus — the C-13a census
  (`analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/`)
  puts all sorting at ≤0.015% of TPC-DS SF0.5 corpus wall time, median
  sort input 145 rows, 0/100 sorts spilled, and TPC-H has no `LIMIT` at
  all. Tuning the worker-sort/leader-heap exchange there would be reading
  noise.
  **Planner follow-up (routed, NOT made — `internal/optimizer/` held by
  C-03):** drop `return partialPathDrivingKind(sub) == PathSeqScan` from
  `gatherMergeSubpathIsRunnable` (`gatherpaths.go:267`) — the executor gap
  its comment cites is now closed; see DESIGN §6.
- [-] **E-11 EX5-04 AIO `ReadStream` decision.** DECLINED 2026-09-06 —
  outcome (b), the measurement's own second legal outcome. Re-run on a
  quiet host with the sole lock on the bench cluster (the 05:15 sweep,
  taken while two other agents held the machine, is discarded). One
  binary `9ad4f30d4` (on-disk `04b4178d65eeda2f`), fresh capped server per
  arm, `GOOPG_ANALYZE_SEED=20260905`, depths {0,4,16,64,128} × 3 reps.
  **TPC-H values byte-identical across all 15 arms × 24 queries.** Suite
  medians 138.4 / 136.6 / 141.2 / 142.4 / 134.1 s for d0/d4/d16/d64/d128 —
  a 6.1% band with **no ordering in depth** (d128 fastest, d64 slowest)
  against an observed control-vs-control band of 40.2% worst / 12.0%
  median per query. The knob is structurally inert at the default:
  `refillPrefetchWindow` returns early for a parallel scan by design (P4,
  ch. 04 §4.2) and every TPC-H plan at bench settings is parallel
  (Q6 = `Gather` / Workers Planned: 4 / `Parallel Seq Scan`), so arm A is
  a five-way A/A. Forcing `max_parallel_workers_per_gather=0` makes the
  window live and shows more prefetch is **worse**: removing it is −12.1%
  on a 7-query serial subset and −35.0% on Q6, repetition ranges disjoint;
  the alloc arm puts `Pool.Prefetch` at 63.8% of allocation objects
  (2.85× the object count, 9.9× the bytes vs depth 0). PG's own controller
  would decline here too (`read_stream.c`: no benefit looking ahead past
  one block when no I/O is necessary; SF=1 is 1.9 GiB against ~19 GiB of
  page cache), and goopg's `ReadStream` v0 is offset/`File`-based with the
  bufmgr-aware variant still deferred — so this is a build, not a hookup.
  Default `seqScanLookahead = 4` deliberately **unchanged** (the win is
  warm-cache-only and on a path no bench plan takes; the real defect is
  that `Pool.Prefetch` discards its buffer, filed separately). Instrument
  `c6af781f4` retained as the re-check apparatus. Artifact
  `analysis/executor-refactor/e11-depth-sweep-20260906/README.md`; ledger
  `take3-E-11-readstream-declined` + `take3-E-11-prefetch-discards-buffer`.
  *design: take3 13 §7; gate: ledger row (decline path).*
- [!] **E-12 EX3-02 Cut 3 (oversize + teardown) — BLOCKED on E-14.**
  (Record correction 2026-09-04: Cut 2 already LANDED — `68ccd68c3`,
  unit headers 2.002→0.005, TPC-H 24/24 + PP 22/22 + TPC-DS PASS=95;
  only Cut 3 remains here.) Queued behind landed Cut 0/1/2; arena sizing is
  batching geometry over the redesigned build rows.
  **Blocker status 2026-09-06:** E-14 settled the SHAPE the redesigned
  build rows take (`docs/design/executor-e14-build-half/DESIGN.md` §3.1:
  a keep-set gather, composed width unchanged, one coordinate move at the
  join's `virtualCol` map) and landed the prerequisite that made it
  reachable (keyed inner spill frames), but deferred the width change
  itself pending the bench gate. So Cut 3 can now size arenas against a
  DECIDED shape rather than an open question — but the retained width has
  not actually moved yet, so sizing to the narrowed width is premature.
  *design: `docs/design/executor-ex3-02-dense-build/DESIGN.md`; gate:
  poison tests + gate suite + values + pin.*
- [-] **E-13 EX1-04 Cut 2 (owned-row tightening on Project-declined
  paths) — DROPPED 2026-09-07 on its own gate.** The gate was "only if a
  later alloc arm shows a residual". The arm was taken and there is no
  residual worth collecting: a whole-suite census of every hash-build
  retention site (schema width, threaded EX1-01 deform bound, retained row
  count; instrument + aggregator + arm logs in
  `analysis/minimize-datum/tracke-e07-e13-e14-e09c-20260907/`) measures
  **509,824 dead Datum cells out of 228,212,960 = 24.5 MB out of
  10,954 MB = 0.22%** of retained hash-build storage, from four small
  sites in 22 queries (150,000 and 29,824 rows at `w8/b7`; 10,000 rows at
  `w7/b6`; 10,000 rows at `w7/b5`). All five
  MULTI-MILLION-row retained build sides — including the 6,001,255-row ×
  16-column one, the suite's largest at 4,609 MB — carry
  `bound = deformBoundFull`: P4-01's `narrowBuildInput` Project already
  narrowed the input to what is read, and a second truncation has nothing
  to take. The residual sites are all small (150,000 / 29,824 / 10,000 /
  10,000 rows) and each gives back one or two columns.
  Second, independent reason not to resume it as written, and the stronger
  one: **E-13's mechanism IS the one the EX1-04 review declined** — copying
  `row[0:bound]` at a retention site makes a row shorter than the
  coordinate space its readers address (E-14 DESIGN §1: `slotRow`/`Row`/
  `Materialize` flatten at `len(schema)`; `nullRow(o.lazyRW)` binds a
  FULL-width pad into the same slot field as a retained row; the tail of a
  truncated row is ABSENT, not NULL). The safe replacement is already
  specified as E-14 §3.1's keep-SET gather with the coordinates moved at
  the `virtualCol` seam, of which a prefix is a special case — so any
  residual that ever does appear belongs to **Cut B**, not to a separate
  item carrying a declined mechanism.
  The SORT half was captured separately (a third arm added a retained-row
  counter to `sortOp`) and does not rescue the item either: the per-site
  dead fractions there ARE large (`childWidth=14/childBound=8`,
  `childWidth=11/childBound=4`) but the row counts are not — the largest
  sort input in the suite is **20,451 rows × 8 columns = 7.9 MB** of Datum
  cells TOTAL, then 11,415×4, 1,302×2, 418×28 and everything else under 40
  rows. Three orders of magnitude below the hash side, and in the same
  place C-13a independently put it (median sort input 145 rows, 0 of 100
  sorts spilled). The sites with the biggest proportional residual are the
  ones with almost no rows. Re-open only with E-14 Cut B's seam, never
  with the prefix truncation.
  Ledger `take3-E-13-dropped`. Cut 1 (poison tests on the Project shape)
  remains landed.
  *gate (discharged): alloc arm residual demonstrated first — it was not.*
- [!] **E-16 EX3-03 step-2 resume — BLOCKED on spill-cost calibration.**
  Session `work_mem` threading is implemented and unit-green but moves
  Q7/Q9 plans to slower merge shapes at bench `work_mem` (model prices
  hash above merge while forced-hash proves faster). Resume with the
  filed artifact
  (`analysis/planner-refactor-take3/ex303-step2-deferred-20260904/`,
  README + clean-applying `plumbing.patch`, ledger
  `take3-EX3-03-step2-blocked`): recalibrate the spill-cost model first,
  then re-apply the plumbing and re-gate.
  **Design landed 2026-09-06: `docs/design/planner-spill-cost-calibration/DESIGN.md`.**
  It finds the hash and sort spill charges biased in OPPOSITE directions
  (hash over-charges — `EntryBytes` measures the in-memory entry while
  `spillWriter.WriteRow` uses uvarint framing; sort under-charges twice,
  and one is a TRIGGER error — `costSortRun` fires at `cp.workMem` = 1 GiB
  while `sortOp` spills at the hardcoded 256 MiB `sortChunkBytes`), so a
  single multiplier would fit the difference between two errors. Four
  separately-gated cuts, three probes, negative outcomes written in
  advance.
  *design: `docs/design/executor-ex3-03-workmem/DESIGN.md`; gate:
  Q7/Q9 plans at bench `work_mem` + forced-hash proof + values + pin.*

---

## 7. Cheap interventions first (05 §1.5)

Priced against the MD bundle; each gets a measurement slice before any
larger work that assumes the same win (graph edges in §1). SKIP with a
ledger row if the measurement says no.

- [x] **F-01 Delete the duplicate build map** — already satisfied in-tree by `514913912` (M0127-P0.3: plan-typed single-map build, `lazyHashFinalize`/`lazyBuildAllInt64` deleted; enforced by `join_single_map_build_test.go` dual-map-build-back guard); gates: executor join suites green at that commit; artifacts: `internal/executor/operators_join_agg.go`, `internal/executor/join_single_map_build_test.go`.
- [x] **F-02 Probe-seam re-materialisation** — already satisfied in-tree,
  audit-only close 2026-09-05. The premise (take2 07 §6, evidence from
  `analysis/cost-driven-second-try-200731` Stage 0) is stale: M0127-P1.1
  landed probe-side slot chaining, default ON
  (`joinSlotChainOn`, `operators_join_agg.go:1289-1299`;
  `GOOPG_JOIN_SLOT_CHAIN=off` restores the old seam), so the probe child's
  slot is no longer flattened into a pooled `Row` on every pull.
  Measured at HEAD with the in-tree benchmark, which keeps the pre-fix seam
  runnable through the kill switch so the delta stays reproducible:
  `BenchmarkProbeSeam/chained` **432.2 ns/op, 8 B/op, 0 allocs/op** vs
  `/off` **1115 ns/op, 879 B/op, 10 allocs/op** — the pool round-trips the
  item was filed against are gone, not reduced.
  Applies to both paths: the chain lives on `joinOp` (`ensureLazyVirtual`),
  and workers build their own `joinOp`s through `buildRec`, so the parallel
  arm is covered by construction rather than by a second mechanism.
  Regression guard already in tree: `TestProbeSeamZeroAllocs`
  (`join_slot_chain_test.go`) pins 0 allocs per pass — verified passing.
  No new code; same disposition as F-01.
- [-] **F-03 24 B pointer-free `Datum` remainder.** DROPPED 2026-09-05
  under §8 **rule 3**. The 2× arithmetic is real and was verified, not
  waved away: `Datum` measures **48 B** at HEAD and `Buf` is exactly the
  24 B slice header, with only **18** non-test `.Buf` references — the
  mechanical surface really is small.
  The blocker is semantic, reached before any timing question: `Buf` is
  the **detach target** that gives a retained value a lifetime independent
  of a resettable producer arena. `MaterializeArena` copies into a fresh
  `Buf` at every retention boundary and `cloneRowOwned` calls it per
  column per retained row. A pointer-free `Datum` has nowhere to detach
  to, leaving only unbounded alternatives (permanent-arena retention, or
  producer arenas that never reset).
  This has been paid for once: `aafef4fd4` (2026-05-10) passed the
  five-query gate and then returned **0 rows on seven queries** in the full
  sweep, root-caused to arena slot reuse (04 §11.2).
  Supporting, not load-bearing: the win is dominated on the same retention
  sites by MD-04/D-05 (measured 1098 B/row vs PG's 23 B), which D-02 has
  now cleared to PROCEED — an order of magnitude more, without removing
  the detach mechanism.
  Report: `analysis/minimize-datum/f03-datum-pointer-free-20260905/README.md`;
  ledger `take3-F-03-dropped` (three resume conditions inline, incl. that
  the gate must be a full 21-query sweep, never a narrow set).

---

## 8. SKIP policy

The following outcomes authorise SKIP for any item above, regardless of
sunk effort. SKIP is recorded, never silent: a ledger row in
`.ralph/deferral_ledger.md` (with `postgres/` citation + resume point)
plus a row in this file's Dropped table. Precedents: EX1-03 (owner
direction + no witness), EX2-02b (infeasible — no sole-owner transfer),
EX3-05-cutB (verified no-win), P1-06 (wrong trade), P1-10 (no consumer),
P6-03/04/05 (load-bearing).

1. **No measured performance gain** — verified by the item's own A/B under
   the measurement protocol (fresh server per arm, cgroup cap, stated
   `work_mem`, ±17% noise band respected). A timing claim inside the band
   is not a claim.
2. **Severe maintainability regression** — the change makes its area
   materially harder to reason about, extend, or debug, out of proportion
   to the measured gain. State the mechanism, not the feeling. Applies to
   **non-acceptance items only** (never C-21/D-11), and requires
   take3-owner sign-off (or a second independent measurement) recorded on
   the SKIP row — one agent's judgment alone does not drop plan scope.
3. **Infeasible in goopg's architecture** — the design cannot be realised
   (missing consumer, violated invariant, unbounded liability) no matter
   the effort. Name the architectural fact, not the schedule.

---

## 9. Ground rules (all tracks)

1. **Values are the gate, never counts** — TPC-H `-digest` + `-diff`,
   TPC-DS `CKMISMATCH=0` (not just `MISMATCH=0`); `ck=n/a` queries are
   row-count-only and are not values evidence.
2. **One variable per commit**; cancelling pairs move in one commit.
3. **Plan-shape pin on every commit** — a moved plan is reported with its
   cost roll-up (A-05 non-skippable pin once landed), never fixed
   executor-side by preference.
4. **Timing + allocator arms together** — a CPU win that doubles
   allocations is not a win.
5. **Never `-count=1`** in a gate run. Never `git commit --no-verify`
   for code (`-n` is authorised for **documentation** commits only).
6. **Sibling paths move together**: encode↔decode, serial↔parallel,
   the two cardinality estimators, the two NLI routes.
7. **Every deferral gets a `.ralph/deferral_ledger.md` row** with an
   upstream `postgres/` citation and a resume point.
8. **No query-specific forcing**, no penalty multipliers, no shape
   preferences; gates name operators, never queries.
9. **Fix_plan + working-set discipline**: tick boxes in
   `.ralph/fix_plan.md` per loop; `make ralph-state-guard` before the
   status block; rewrite `.ralph/working_set.md`.
10. **Pre-commit bar**: `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` green before every code commit; the
    hook's pgbench smoke is machine-enforced — never bypass.
11. **Epics split before start**: an item covering N commits (B-05, D-08
    and any item marked [EPIC]) is replaced by N checkboxes with existing
    or new IDs before the first of them starts — the split rule binds the
    plan, not just the work.

---

## Progress log

| item | closed | commit | effect | notes |
|---|---|---|---|---|
| F-01 | 2026-09-05 | 514913912 (pre-existing) | dual-map peak removed; single-map build by plan type | audit-only close; no new code; guard test `join_single_map_build_test.go` |
| C-01 | 2026-09-05 | ea8ca9dfe + 2e59cfe49 | SJI min/strict populated fail-closed; chained-LEFT shrink verified | 8 scoped tests; review APPROVE-WITH-NITS; smoke 0 failed; USING follow-up filed |
| EX1-03 | 2026-09-05 | 5983b25c5 | bound-narrowed detoast + DetoastAttr + json pointer decode; build restored | per-type contract tests; suites + spotcheck PASS; review APPROVE-WITH-NITS; 3 gap ledger rows |
| B-14fu | 2026-09-05 | afb39bb7c | ANY explain + update fallback predicate (fixes live wrong-answer) + SSI fingerprint | executor + optimizer green; smoke 0 failed |
| C-02a | 2026-09-05 | b90b08d + 0dac569c1 | per-link OJ delay test, inert | 20-case table; optimizer green; review APPROVE |
| C-02b | 2026-09-05 | 9c0b549 + 6f0232c | plan-level delay attribution, inert; parity pins | attribution tables + inventory pin; optimizer green; review APPROVE-WITH-NITS |
| E-15 | 2026-09-05 | 065182d + c7f1f45 | presorted-prefix contract published; C-14/E-03 unblocked | 4 contract tests; suites green; review APPROVE-WITH-NITS; no behaviour change |
| gate-repro | 2026-09-05 | 870732855 | plan captures reproducible: A/A noise 455 est / 27 shape lines -> 0 | `GOOPG_ANALYZE_SEED` (OID-mixed) + bench `autovacuum = off` in the TRACKED generator; review APPROVE-WITH-NITS, 9/9 applied; prerequisite for every later plan pin |
| build-cost charge | 2026-09-06 | (reverted; patch in tmp/) | 5x participant multiplier, derived and witness-confirmed; **+22.3%** — Q5/Q9/Q10 lost PARALLELISM, not a build side | **names the root: goopg's cost model has no parallel dimension**; D-05 is blocked on C-19, not on its own terms |
| C-19a + C-19b | 2026-09-06 | (this commit) | Phase 5 begins: consider_parallel per rel + priced partial seq-scan paths; serial arm byte-identical | review found the safety classifier fail-open in 4 places, fixed + 16 pins; values 24/24, plan-gate 22/22 costs-exact, TPC-DS clean |
| cost-side narrowing | 2026-09-06 | (reverted; patch in tmp/) | cost side made to agree with the executor exactly; **+10.3%** — it removed an accidental deterrent | **names the real defect: hashJoinCost under-prices BUILDING a large hash table**; confirms the bucket-charge coupling (Q14 no longer flips) |
| D-05 prereq #2 | 2026-09-06 | (reverted; patch in tmp/) | MapSlotBytes 48 was a guess and 2x low (measured 96 B/slot); honest value halved bucket heap 586->286 MB, batches unchanged — but **Q14 flipped to a nested loop, +10.4% total** | premise refuted again (buckets WERE already charged); blocker moves to the cost side pricing an un-narrowed build |
| D-05 prereq #1 | 2026-09-06 | (this commit) | entry model fixed 194 -> 120 B/row (was half-narrowed, half-full-width); **nbatch unchanged — D-04's claim refuted** | non-monotone: 2 batches need <=111.8 B/row; packed 63 B/row lands back on 4. The lever is MapSlotBytes, not the entry |
| D-04 | 2026-09-05 | (this commit) | **STOP: batches 4->4 unchanged, time +6.8%, allocs +39%, retained bytes -14/-24%** | fires 05 section 6's "fix the model first" arm; premise found stale (5x width claim is 1.9x post-EX1) |
| C-10d (measurement half) | 2026-09-05 | (this commit) | AST census: 89 boundaries / 41 of 99 TPC-DS; a full pull-up removes only 39% of the blocking ones | so C-11 must cross the boundary regardless; also found CTEs+views are two more routes to the same leaf |
| C-10c | 2026-09-05 | (this commit) | Phase-4 OJ qual/target placement contract + 5 fixtures; 3 negative controls | found C-16 retires no arm (no reminder) and that goopg has no PlaceHolderVar equivalent |
| C-07 (derivation half) | 2026-09-05 | (this commit) | query_pathkeys derived with PG's precedence; has_useful_pathkeys completed and made per-rel | inert by design: values 24/24, plans byte-identical; the "motivate index paths" half is evidenced-blocked on C-11/C-12 |
| C-07 (widening half, re-adjudication) | 2026-09-07 | (this commit) | widening implemented in a throwaway worktree and instrumented: it DOES generate the path (`[w]`→`[w x]`), and no plan moves even at `enable_seqscan=off` | C-11+C-12 landed and did not unblock it — `createOrderedPaths` consumes a Node and `newPrebuiltPath` carries no Pathkeys; plus a new independent blocker, the producer never runs at `nrels<2`. Not forced; decision test rewritten + consumer-side twin added |
| D-03 | 2026-09-05 | (this commit) | PackedTuple/PackedSlot unreachable; SEVEN R-0 sites armed (design said six) | review REQUEST-CHANGES -> resolved; values 24/24, plans byte-identical; 25 tests |
| C-10a (cardinality half) | 2026-09-05 | (this commit) | grouping-sets aggregates were priced as ONE set; now summed per set and clamped | values 24/24 + TPC-DS CKMISMATCH=0; PP unchanged; 4 new tests |
| C-20d | 2026-09-05 | (this commit) | **index-probe multiplier calibrated 1.0 -> 2.0: TPC-H suite 138.58 -> 100.79 s (-27%), Q5 5.3x, Q7 2.7x, Q9 1.9x, Q3 2.3x** | values 24/24 + TPC-DS CKMISMATCH=0; plan parity IMPROVED 5/15 -> 6/14; flag retirement stays blocked |
| D-01 | 2026-09-05 | (this commit) | TupleDesc descriptor fields derived, not transcribed; agreement test spans two pg_type.dat transcriptions | additive, no consumer yet; values 24/24, plans byte-identical, plan-gate PASS |
| F-02 | 2026-09-05 | (pre-existing M0127-P1.1) | probe seam already chained: 432 ns/op 0 allocs vs 1115 ns/op 10 allocs for the old seam | audit-only close; no new code; guard `TestProbeSeamZeroAllocs` verified passing |
| D-02 | 2026-09-05 | c6468238f + (this commit) | verdict PROCEED: 0 declining columns of 160,302, 0 retention sites of 985 | corrected 04 section 3.1's packableType definition, which would have produced a false STOP; 3 latent on-disk bugs ledgered |
| C-02d | 2026-09-05 | (this commit) | qual moves across preserved-side outer links; proof gains positive containment | values 24/24, plans byte-identical, plan-gate PASS, TPC-DS CKMISMATCH=0; review found no counterexample |
| C-02c | 2026-09-05 | 2305241f4 | qual MOVED (not copied) on proven all-INNER paths; vacuous Filters spliced | values 24/24, plans byte-identical, PP unchanged, timing in band; 6 new pins |
| D-09 | 2026-09-06 | 016f67b | conditional alignment both directions; on-disk padding matches PG | live-PG byte-identical golden; suites + R8 both suites + plan-gate 22/22; review APPROVE |
| E-05 | 2026-09-06 | 39d26c5 | merge residual+keys via separate slab; pure dispatch swap | twin-parity + 0-alloc; R8 both suites; timing neutral; review APPROVE-WITH-NITS |
| E-06 | 2026-09-06 | 6aab9dc | agg transition via per-call slab; UserAgg whole-call decline | twin-parity + per-site pins + 0-alloc; R8 both suites (parallel shapes incl.); timing neutral; review APPROVE-WITH-NITS |
| B-19 | 2026-09-06 | (this commit) | rowest-A1 paired-band null add-back (failed-agent item completed; B1 already landed) | live reproducer 217→2430 vs true 2500; R8 both suites; PP 28 moves all-attributed, Q28 direction-verified; review APPROVE-WITH-NITS |
| C-08 | 2026-09-06 | b967f38 | per-joinrel param_source_rels derivation with frame remap; NLI + merge arms threaded; star-schema escape untouched | derivation table + admit/refuse arm pair; suites + units scope + spotcheck PASS; PP TPC-H 22/22 MATCH (provably inert until C-04, no sweep triggered); review APPROVE-WITH-NITS (5 addressed); ppi_rows ledger row |

| E-09a | 2026-09-06 | 67204579c | **spilling shared hash build published**: Q9 workers `rows=1500000 loops=1` -> `rows=0 loops=0`, five Build Times -> one; Q9 8.85 -> 7.85 s | immutable batch descriptor, growth frozen after prebuild (PG's rule), private reload per participant, NO new synchronisation; values 24/24, plans byte-identical, TPC-DS PASS=95 CKMISMATCH=0, -race green. Unblocks C-19f and the D-05 re-measurement |
| C-19c | 2026-09-06 | dbb14ca25 | partial index paths + `min_parallel_index_scan_size` + `IndexScan.Parallel` + workers claim index leaf blocks | reachable and priced, but **no TPC-H plan moves to the shape at SF=1** under the pinned stats (plans byte-identical) — recorded as such rather than claimed as a win |
| E-11 (instrument) | 2026-09-06 | c6af781f4 | `GOOPG_SEQSCAN_LOOKAHEAD` knob; default 4 unchanged byte-for-byte | item stays `[~]`: the first depth sweep ran against a contended machine and is not attributable. Instrument only, so the decision can be measured quiet |
| E-11 (prefetch removal) | 2026-09-06 | 3fc3d88 | `Pool.Prefetch` + `SetPrefetchEnabled` + `prefetchEnabled` deleted per design 72b93639d outcome (B); smgr seam kept; no callers/tests remain; storage suite green; units scope green | failed-agent item completed; race-gate red is pre-existing ledgered `take3-instrumentscope-datarace` (reproduced without this change), unrelated |
| D-05 (correction) | 2026-09-06 | c94326cf6 | E-09a invalidates the 5x multiplier in the reverted build-cost charge; on a spilling build it is now 1x | NOT an unblock — the structural finding stands and the other two refutations never used the multiplier. Re-DERIVE at 1x after C-19d |
| spill-cost (design) | 2026-09-06 | 169711fe3..73d851b68 | one design for B-13/B-15/E-16's shared prerequisite; two probes answered without a bench | hash and sort spill charges biased in OPPOSITE directions; `costSortRun` fires at 1 GiB while `sortOp` spills at 256 MiB; probe 6.2 derived the over-statement at 5.1x..1.2x, which **refutes the scalar multiplier** the plan assumed |
| B-06 (synthesis impl) | 2026-09-06 | 1201245 | CTE-output synthesis functions (group-key/aggout-FD/union-literal rules) + 16 tests, inert (no consumers wired; guard untouched) | review REQUEST-CHANGES → both blockers fixed (Project mapping, Op checks) + re-review APPROVE-WITH-NITS; B-06 stays open until step-4 guard-removal criterion |
| spill-cost Cut 3 (model) | 2026-09-06 | 53cd7a073 + b416786f1 | `hashsize.SpillBytes` derived from spill.go's encoder, INERT; `estimatedRowBytes` reads `hashsize.DatumBytes` | 4 agreement tests pin encoder-vs-model and the executor's runtime ruler vs the planner's width model; wiring `spillPages` waits on `cost_funcs.go` ownership |
| MapSlotBytes (doc) | 2026-09-06 | 61450d37f | constant marked KNOWN 2x LOW with the measurement (96.1 B/slot) and the blocker | value deliberately unchanged — 96 costs +10.4% via Q14's phantom-build flip. Same class as C-20d: a comment describing an intention read as a decision |

| E-09b | 2026-09-06 | d5ce1bb9b + b5647d6fa | **one live batch table where there were four**: loads 28 -> 7, maxLiveLoads 4 -> 1, maxLiveBytes ~563,100 -> 140,775 on a 4-participant/7-batch fixture; mutation-checked | NOT `sync.Once` — it marks its slot done when the function RETURNS, so a cancelled or panicking loader would publish an EMPTY map and every waiter would silently probe nothing. Explicit `done` channel closed by `defer` on every exit incl. panic; loader never waits, so closure depends on the filesystem not on a peer — no cycle, no deadlock. Cancel-mid-batch tested at the protocol level and through the real Gather. Bench arm still owed |
| C-19d | 2026-09-06 | a2142383a + e996a4ff2 | `generateUsefulGatherPaths` — `PartialPathlist`'s FIRST reader; `cost_gather` (a function that had existed with no search caller) + new `gatherMergeCost`; `createPlanNode` both arms; `enable_gathermerge` becomes a counted `disabled_nodes` term | **default OFF for an arithmetic reason, not caution**: crossing a Gather costs `parallel_tuple_cost` = 0.1/row against a 4-worker saving of ~0.0075/row, so with only base-rel partial paths the whole relation crosses and `add_path` correctly dominates every Gather at any size. Blocker narrows from "no parallel costing" to **C-19f specifically**. Two latent wrong-answer classes stopped on the way: the post-pass would have nested a second Gather (N workers each launching N) and a Gather Merge over a partial INDEX path would return N copies of every row |
| instrumentScope race | 2026-09-06 | 0ee17b564 (ledger only) | pre-existing `-race` failure confirmed at `67204579c` in a clean worktree, so not E-09b's | `buildUnderNilScope` swaps the package global under a mutex that `maybeInstrument` never takes; its own comment describes the leak it does not prevent. Ledgered, not fixed inline |

| E-11 | 2026-09-06 | 23ce0a60c | **DROPPED, outcome (b)**: 5 depths x 3 reps, medians 138.35/136.55/141.17/142.37/134.14 s for depth 0/4/16/64/128 — spanning 6.1% with NO ordering in depth, inside a 12.0%-median control band. Values byte-identical across all 15 arms | it was a **five-way A/A**: `refillPrefetchWindow` returns early for a parallel scan and EVERY TPC-H plan at bench settings is parallel (Q6 = `Gather` / Workers Planned: 4). Forced serial, where the window is live, MORE prefetch is WORSE — depth 0 beats depth 4 by 12.1% on the subset, 35.0% on Q6, disjoint ranges. Default deliberately NOT flipped to 0: all evidence is warm-cache, so zeroing would bank a warm-only win and hide the real bug |
| C-13a | 2026-09-06 | ca2809ab7 (census only) | **NO-GO before implementation**: goopg stacks 77 `Limit`-direct-child sorts vs PG's 54 — structural hypothesis CONFIRMED — and it buys nothing. 54 of 77 sort already-collapsed aggregate/window output; median input 145 rows; total across all 77 is <= 119.8 ms of 802 s (0.015%); **0 of 100 sorts spilled**, so the design's strongest argument has no witness | corpus-level finding: no sort-side item has a runtime witness in EITHER suite (TPC-H has no LIMIT at all; TPC-DS's LIMITs sit over grouped output). C-14 is not the alternative either — goopg's Q67, PG's own incremental-sort case, is 1.0 ms over 115,150 rows. Deferred not cancelled: the mechanism is cheap and correct, the evidence is missing |
| rowest defect | 2026-09-06 | 56d10d0c0 (ledger) | incidental to C-13a's census: TPC-DS row estimates wrong by 3-5 orders of magnitude in BOTH directions — 22 of 100 Sort inputs est=1 vs up to 245,587 actual (incl. six plain Seq Scans), HashAggregate outputs up to 8007x high | checked and NOT C-10a's grouping-sets summing, which is PG-faithful; the error is inside per-set `estimateNumGroups`. Filed at ledger level because it invalidates the INPUT of every remaining cost item, and no gate catches it — values compare results, plan-parity compares shapes, neither looks at `rows=` |

| C-19f | 2026-09-06 | e8456fe82 + 125c4c016 + 6d94052ec | **a Gather becomes choosable by cost**: crossover `N > 106,667 + 9.87*J` at 4 workers, unreachable for a base rel at any size and for a single FK join, satisfiable by a join TREE. Measured off-vs-top, 3 reps, values 7/7 MATCH in all 42 runs: **Q21 17.33 -> 8.42 s (-51%)**, Q9 +94%, Q10 +30%, suite -10.7% inside its own spread | Q21 is the case only a path model reaches — at HEAD it gets NO Gather (root is a Nested Loop Anti Join; `terminatesPartial` stops `findPartialSubtree`), and C-19f gathers the hash join inside it. Q9 honestly attributed as NOT the predicted foreclosure: the BUILD side flips, 6M rows built undivided, model called it 21% cheaper — lead is the build term (`spillPages` over-states) not the Gather term. Q10 +30% with a byte-identical executed shape, recorded OPEN. Build charged once and undivided (matches the executor after E-09a/E-09b); the reverted `d05p4` 5x multiplier now refused by a test. Default unchanged, no new flag; TPC-DS PASS=95 CKMISMATCH=0 with PLAN-SHAPE 99/99 identical, so inertness is measured. The mandatory executor consumer check found TWO latent `createPlan` bugs unreachable until a Gather could win |
| E-14 | 2026-09-06 | 10b34c633 + 2039ffa66 | prerequisite landed inert: keyed inner spill frames `[4B hash][1B tag][key][payload]`, so `buildKeyOfRow`/`batchKeySlot` retire and **nothing in the executor derives a build key from a retained row**. TPC-H 24/24 MATCH; -race clean | the item's quoted prize was MISATTRIBUTED — Q9 20.14->13.88 s comes from a measurement-only artifact and belongs to the P4-01 planner flip, already banked. Measured residual instead: 6 of 15 retained columns are the join's own key columns and EVERY one sits at position 0, so a `[0,bound)` prefix cannot describe the opportunity at all. Narrowing (Cut A/B) deferred for want of an alloc + batch-geometry arm; ledger `take3-E-14-cutA-cutB-deferred` |
| tooling: serial captures | 2026-09-06 | 752aeb7c9 + 885bcd2a6 | `estimate-audit`'s `-serial` defaults TRUE (`max_parallel_workers_per_gather = 0`), deliberately — goopg does not merge per-worker Instrumentation, so nodes under a Gather report no actual rows. The PG reference is captured the same way, hence 0 Gathers in `plans-pg` too | so every 'plans byte-identical' result from these captures is a SERIAL-CONTROL-ARM statement: like-for-like and valid, but it cannot support 'no parallel plan change occurred'. C-19c's inertness claim was corrected on exactly that ground. Prerequisite for judging any parallel item on plan parity is a parallel-mode PG baseline, which does not exist |

| rowest B1 | 2026-09-06 | 9b43c67f3 | `resolveBaseColumn` gains its `*NestedLoopIndexJoin` arm — every column above an NLI had resolved to nothing, so grouping vars priced at `DEFAULT_NUM_DISTINCT=200` until the product saturated and `estimateNumGroups` returned its own INPUT row count. Probe: Q99 720,657 -> **90 exact**, Q62 359,432 -> **150 exact** | the durable half is `TestResolverFamilyArmListsAgree`, which parses both type switches and pins the arm lists mechanically — this was the THIRD instance of the drift the function's doc comment had been warning about in prose |
| rowest A1 | 2026-09-06 | adc54800b | `conjunctionSelectivity` adds PG's `s2 += nulltestsel(IS_NULL)` (clausesel.c:292-294). Both range bounds already excluded NULLs, so the subtraction excluded them twice: on `ss_quantity` 0.955955+0.040251-1 = -0.003794 -> guard -> 1e-10 -> clamps to 1. TPC-DS fact tables are ~4.4% null nearly everywhere | probe 1 -> 14,932 against 15,410 actual |
| B1+A1 gate | 2026-09-06 | (measured post-hoc) | both landed UNGATED because their agents were terminated mid-gate by a session limit; the arm was run afterwards. **TPC-H values 24/24 MATCH, plan SHAPES unchanged** (0 added, 0 removed; 6 `cost=`/`rows=` lines moved), plan-gate exit 0, PP 6/14 unchanged, TPC-DS PASS=95 CKMISMATCH=0, suite 106.51 -> 105.02 s (-1.4%, inside spread) with **Q2 1.67 -> 0.75 s (-55%, outside it)** | the plan diff is the artifact: Q4 `HashAggregate rows=200 -> 5`, and 200 is `DEFAULT_NUM_DISTINCT` verbatim — B1's signature, sitting in the committed TPC-H plans all along |
| coop parallel deform bound | 2026-09-06 | 5bf764520 | **SILENT WRONG ANSWER fixed**: a hash join with a build-side restriction returned the RIGHT ROW COUNT with a NULL payload in every column. `parallelBuildLazyHashTable` rebuilt the build subtree via `BuildWorker`, i.e. at the ROOT deform bound, so `Filter(dk>3) -> SeqScan(dk,dname)` deformed [0,1) and `dname` was never materialised | severity was UNDER-reported and I corrected it by measuring: reported as WorkMem 0/1 and 'correct at >=4096', it actually fails at **25 of 32** work_mem values including **1 GiB** — goopg's own effective hash budget at the default. Only a narrow 1KiB..64KiB band passes, which is where the original spot check landed. `TestCoopParallelHashBuildValuesAcrossWorkMem` sweeps it and asserts VALUES; mutation-checked 25/32 fail without the fix. Real-session reachability still OPEN |
| bench residency | 2026-09-06 | 0df7dc930 | both TPC-DS clusters ran on the **128MB `shared_buffers` default** (16,384 slots) against 1.113 GiB (SF0.5) / 2.2 GiB (SF1) working sets, while TPC-H had 2048MB and the PG reference 2GB — a **16x unfair** memory comparison. `pg_stat_io`: TPC-DS 2 scans of `store_sales` = reads 59,522 / **evictions 43,138**; TPC-H 3 scans of `lineitem` = reads 136,393 / **evictions 0**, unchanged from scan 2 | VALUES gates unaffected (the SF0.5 oracle is row values; residency cannot change an answer) — every PASS=95 stands. TPC-DS TIMING before today is I/O-bound. `server.sh` now warns when `shared_buffers` is unset |

| C-03a-d | 2026-09-06 | 0d2e7a10c + 4eab748cf + bd2c7c0d0 + 108776383 | jointype-aware paths, all four cuts INERT: `Path.Jointype` (Inner as zero value so unstamped paths are unchanged), sjinfo orientation into `addPaths`, `planJoinTypeFor` as the single meeting point of the two JoinType enums with SEMI/ANTI left-only schema AND layout, enum-trace fixtures through the production reader | inertness REASON pinned, not just observed: `TestSearchIsInertForDisjointJoinInfoList` shows every prefix-internal pair yields a nil SJI via `join_is_legal`'s RHS-overlap fast path, so production always takes the `sjinfo == nil` arm. TPC-H 24/24 identical digests, serial plans byte-identical, PP 6/14 unchanged, plan-gate exit 0, TPC-DS PASS=95 CKMISMATCH=0 with 99/99 plan shapes. FULL declined with ledger row `C-03c FULL-join-search-decline` |
| E-10 (correctness half) | 2026-09-06 | a22d995c8 | **latent WRONG ANSWER fixed**: `gatherMergeOp` attached only `attachParallelScan`, so a Gather Merge over a partial INDEX path returned `(workers+1)x` every row — reproduced at 1/2/4 workers as 5802/8703/14505 against a serial 2901, **in the correct order**, which is why no ordering test could ever have caught it. `parallelClaimSet` now holds all three claim kinds with `attachAll()` as the single wiring site, embedded by BOTH gather operators | second bug found in the same function: `gatherMergeOp.runWorker` registered `defer close(o.chans[idx])` AFTER the child build and `Open`, so a failure left a live channel with no closer while `Close` drains with `for range` — that is the M0127-P5.9 Q17 hang class, which `gatherOp` had fixed and this sibling still carried. Anti-drift tests fail if a field is added to `parallelClaimSet` without an `attachAll` arm, or if either operator re-declares its own claim state |
| E-10 (performance half) | 2026-09-06 | (out of scope, verified) | **marked out of scope with numbers**: no runtime witness on either corpus. TPC-DS SF0.5 sorting is 119.8 ms of 802 s = **0.015%**, median direct-child sort input 145 rows, **0/100 sorts spilled**; TPC-H query text contains no `LIMIT`; and no `GatherMerge` path is generated in production at all today, so the exchange has zero production surface until the planner change lands | tuning it would be reading noise |

| C-04a (fix 1: Q72) | 2026-09-06 | fb6550266 | **WRONG ANSWER closed**: Q72 84 -> 100 rows. Admitted LEFT links lost their jointype through the collapse-limit sub-problem split and planned as INNER; per-problem SJI remap (`sjInfosInItemSpace`) carries the sjinfo across the split | pinned `TestLeftLinkSurvivesCollapseSplit`. The agent was terminated by a session limit mid-work and left this committed plus a correct-but-non-firing Q78 firewall in the tree |
| C-04a (fix 2: Q78) | 2026-09-06 | 3d0aad730 | **20x TIMEOUT closed**: Q78 327 s -> 19 s, checksum-verified, top-level plan byte-identical to pre-C-04a. The `problemPairsOuterWithDerived` firewall was wired right and never fired: `with.go` gives every CTE binding a SYNTHESISED non-nil table (so `table == nil` can never see one) AND the leaves arrive as `*Filter{*CTEScan}` (so a top-node type switch sees only the Filter). Classify by node type, descending through wrappers | reached only after THREE wrong hypotheses, each plausible from the code and each refuted by one trace line (dropped qual — already restored; `base != 0` spine decline — trace showed the chain was ADMITTED; `table == nil` alone — still `derived=[false false false]`). Pins `...CTEOuter` + `...WrappedCTE`, both mutation-checked. Final: TPC-H 24/24, **same-session A/B -0.8%** (a +9.0% under a concurrent sweep and a +5.4% against a 3-hour-old baseline were both wrong — the unchanged binary had drifted +6.3%), Q13 -17.4%; TPC-DS PASS=95 all zeros, shapes 99/99; plan-gate 22/22 both modes, pin `c04a-fixed-20260906` |

| C-11 | 2026-09-07 | fcf049b05 | upper-rel registry (`fetch_upper_rel`) landed INERT — per planning scope, `Relids = 0`, upper rels out of `relMap`/`joinrels` | C-08's pattern. TPC-H 24/24; plan-gate 22/22 both modes |
| C-12 | 2026-09-07 | 60da43bf9 | ORDERED upper rel gets a real `PathSort`. **The finding**: before this, `costSortRun` had ONE production caller (merge-join input sorts), so every top-level Sort was priced at ZERO by `DeriveLegacyDisplayCost` — Q18's `Sort (rows=1565307 width=204)`, the largest in the corpus and in its slowest query, contributed nothing to any path comparison | isolated HEAD+C-12 A/B: structural 22/22, costs move on Sort lines ONLY, digest 24/24, TPC-DS PASS=95 (Q78 17 s ck-verified), total +2.5% |
| C-13b | 2026-09-07 | c65801e0f | `cost_tuplesort`'s `limit_tuples` middle branch. C-13a stays deferred on its own census (no runtime witness: 0.015% of TPC-DS corpus wall time in sorting, 0/100 spills, TPC-H has no LIMIT at all) | structural AND costs 22/22, digest 24/24, ten longest no slower; TPC-DS PASS=95 total -1.0%, cost-normalized plans byte-identical (18 queries move cost-only on Limit-rooted Sorts) |
| C-15 | 2026-09-07 | 40d7a4667 | GROUP_AGG upper rel with `cost_agg` paths; **three aggregate rules retired** — they existed because there were no grouping paths to compare | TPC-H structural 21/22 (Q18 access-path-only move, same numbers, no slower); TPC-DS PASS=95 total +1.6%, ZERO shape change. Unblocks C-19g |
| C-16 | 2026-09-07 | 9fa1162c4 | DISTINCT upper rel with hashed/unique paths via DistinctOn reuse | structural+costs 22/22, digest 24/24, timing flat; TPC-DS PASS=95 total +0.1%, shapes 99/99 |
| spill Cut 1 | 2026-09-07 | 247e0ee73 | `costSortRun` takes the rel's `avgVarBytes` — it had passed 0 where `hashJoinCost` passes the real statistic, sizing text-heavy sorts as fixed-width | prerequisite for C-11's upper rels (a fresh rel with `NCols == 0` silently suppresses the external-merge arm) |

| C-19g | 2026-09-07 | 2ebc11d6a + 866e6fe7e + 9e6138f64 | **the parallel track's first real win**: partial aggregation as priced paths. Q1 **8.57 -> 4.14 s (-51.7%)** against a 1.2% off-arm spread — 50x outside the noise floor; suite -4.3%. goopg's Partial node emits ZERO rows, so Q1 crosses **16 group-states, not 5,901,255 rows** (1.6 vs 590,000 at `parallel_tuple_cost`), and the boundary term that kept C-19d and C-19f off essentially disappears | replaces `splitAggregateIsProfitable`'s five invented constants ('calibrated against one query') with a two-candidate path tournament adjudicated by `addPath` — no new cost function, no new constant. Reaches GROUPED aggregates the old rule could not (previously only the three ungrouped ones split). Ships **default-OFF**: the flip moves 3 TPC-H plans and needs a shared-pin re-pin. TPC-H 24/24 both arms; TPC-DS mode-ON PASS=95 all-zero, total -3.9%; plan-gate mode-OFF 22/22 against the shared pin. `MODE=costs` not evaluable on a private clone and NOT claimed. Row stays `[~]`: the upper-rel-resident half is unfinished |

| C-18 | 2026-09-07 | 41d4eb8db | WINDOW + SETOP upper rels; `PathWindow`/`PathSetOp`, `costWindow` (= `cost_windowagg`'s three per-input-row terms PLUS the sort goopg's `windowOp` performs internally), set-op priced by `cost_append` when streaming and the hashed arm when buffering | cannot move a plan BY CONSTRUCTION: a window spec-group chain is ONE stacked candidate `add_path`ed once (`create_one_window_path`, planner.c:4620). Two design deviations, both documented: only `applySetOp` is a real set operation (the partition/inheritance fan-outs reuse the NODE but are PG appendrels), and the design's `costSortRun(cp, rows, nkeycols, …)` put the KEY COUNT where the ROW WIDTH belongs, which would model a 2-column row and suppress the disk charge — declined and pinned. **Found a real defect**: `fetch_upper_rel(SETOP, 0)` shares one rel across a chain, so `A INTERSECT B EXCEPT C` had the outer node answer with the inner node's candidate (wrong rows); PG keys it by relids (prepunion.c:805) |
| C-17 | 2026-09-07 | 953e64297 | `tuple_fraction` to every upper rel. Census found four gaps: stamped in TWO per-arm places (so a WHERE-less `ORDER BY a LIMIT 10` told every upper rel all rows were wanted), two producers passing a literal 0, and a set-op trailing ORDER BY reaching the executor as a bare `&Sort{}` with NO ORDERED rel — the last top-level sort priced at zero after C-12, and the one shape that could never reach C-13b's bounded arm | shows NOTHING on either corpus and that is the predicted null: TPC-H has zero set ops, zero window functions and a WHERE on every query; TPC-DS's UNIONs sit inside subqueries. Raw SF0.5 capture byte-identical, 0 diff lines. Witness is a direct probe (`lineitem UNION ALL orders ORDER BY 1`): bounded arm 344,423 -> **178,266**, external-merge 344,423 -> **433,323**, both directions the correction. Timing -0.2%/+1.5% on byte-identical plans = noise, NOT credited |

## Dropped

| item | date | reason | ledger row |
|---|---|---|---|
| E-08 (EX4-04) | 2026-09-05 | dropped by dependency: it is parallel filterOp compilation, and the serial twin E-04 failed its own measurement | `take3-E-08-dropped` |
| F-03 | 2026-09-05 | rule 3: `Buf` is the arena-detach target; removing it leaves only unbounded retention. Prior attempt returned 0 rows on 7 queries. Win dominated by D-05 on the same sites | `take3-F-03-dropped` |
| E-04 (EX4-01) | 2026-09-05 | no measured gain (predicted ~0.33 pp is below the noise floor; measured a consistent Q18 +8.5%); mechanism overlaps the scan prefilter | `take3-E-04-dropped` |
| C-09 (P3-08) | 2026-09-06 | unique-inner SEMI has no structural value (pinned→no reorder; estimates already unique-aware; exec early-break-bounded); plan-rewrite re-indexing risk exceeds any bounded gain | `take3-C-09-declined` |

(End of file)
