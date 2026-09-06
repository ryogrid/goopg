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
C-14 (Incremental Sort) ◄──► E-15 (contract-publication spike, unconditional) ──► E-03 (conditional implementation)
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
- [ ] **B-06 P1-27 CTE-agg statistics.** DEFERRED-OPEN 2026-09-05
  (probe): guard is load-bearing (removal reverts Q74 99s→14s); PG has
  no answer either (single-key uniqueness; Q74 groups by 4). Genuine
  fix needs beyond-PG work (OID-less CTE-output synthesis + multi-key
  per-column ndistinct + FD-bound-only for agg outputs); no safe
  ratchet-moving increment. Ledger `take3-B-06-deferred` (resume steps
  inline). Keep open; revisit only with the synthesis design.
- [ ] **B-07 P1-30 index-endpoint probe + MCV widening.** DEFERRED-OPEN
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
- [ ] **C-03a P3-03 `Path.Jointype` field (inert).** Jointype on paths
  (default Inner), compare-ignore rule, DPPATH label. After B-01.
  *design: `docs/design/planner-c03-jointype-search/DESIGN.md` §4 C-03a
  (reviewed APPROVE-WITH-NITS); gate: unit + suites green.*
- [ ] **C-03b P3-03 jointype-aware `addPaths` (inert).** sjinfo orientation
  carrier into `addPaths`; OUTER legal direction only; SEMI/ANTI
  nestloop-only. *design: C-03 DESIGN.md §4 C-03b; gate: DPPATH
  OFFERED/ACCEPTED units + suites green.*
- [ ] **C-03c P3-03 `createPlanNode` jointype arms (inert).** Path jointype
  into `Join.Type` + SEMI/ANTI left-only schema/sizing narrowing; FULL
  declined with ledger row. *design: C-03 DESIGN.md §4 C-03c; gate:
  searched-shape units + suites + take3 R8 zero-drift.*
- [ ] **C-03d P3-03 enum-trace DPPATH evidence.** PG-only pairings OFFERED
  adjudication on fixtures. *design: C-03 DESIGN.md §4 C-03d; gate:
  enum-trace fixtures green.*
- [ ] **C-04a P3-04 LEFT admission (Q72 witness).** Spine deletion +
  pinning relax + refusal deletion + leaf-flatten + lateral descend +
  per-qual delay + LEFT sizing floor, for LEFT links end to end.
  Implementation starts after C-03a/b/c land (jointype paths).
  **Unblocks P1-18 (C-05).** After B-01.
  *design: `docs/design/planner-c04-single-problem/DESIGN.md` (reviewed
  APPROVE; vertical slices); gate: PP both suites + behavioral Q72 pin
  + enum-trace + R8 values-diff both suites + timing.*
- [ ] **C-04b P3-04 RIGHT admission.** Same vertical cut mirrored.
  *design: C-04 DESIGN.md §5 C-04b; gate as C-04a + DPPATH.*
- [ ] **C-04c P3-04 below-inner + non-first-comma LEFT links.**
  Non-spine LEFT admission. *design: C-04 DESIGN.md §5 C-04c; gate as
  C-04a.*
- [ ] **C-05 P1-18 outer/semi/anti sizing — executes HERE, after C-04.**
  Port `calc_joinrel_size_estimate`'s jointype switch.
  *design: take3 08 §4 + §6.2; gate: EA ratchet.*
- [ ] **C-06 P3-05 retire `GOOPG_PGSHAPED_COLLAPSE`.** Once C-04 makes it
  the only jointree path; `from_collapse_limit`/`join_collapse_limit`
  take upstream meaning. Own commit, both suites timed.
  *design: take3 08 §6.3; gate: take3 09 §5 P3.*
- [~] **C-07 P3-06 — DERIVATION + GATE LANDED 2026-09-05; the "motivate
  index paths" half is BLOCKED on C-11/C-12.**
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
  plan churn, so it was not forced. The consumer is C-11 (`ORDERED` upper
  rel) + C-12 (real upper-rel `PathSort`); once either exists the widening
  is a map union at one line.
  Gates: 6 new test groups incl. a DECISION test
  (`TestAddOrderedIndexPathsGateIsCompleteButGenerationIsNot`) that pins
  the gate saying yes while the producer emits nothing, and names C-11/C-12
  as the item that flips it red-to-green; optimizer suite; TPC-H values
  24/24 MATCH; plans BYTE-IDENTICAL; plan-gate PASS.
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
- [~] **C-10a P4-00a grouping-sets scope + `dNumGroups` fix — CARDINALITY
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
- [ ] **C-10b P3-09 `remove_useless_joins` — RECLASSIFIED to Phase 3.**
  Scoping result: this touches **no** Phase-4 item, so it is not a P4-00
  blocker. PG runs it below the upper planner entirely, changing the
  joinlist; none of C-11…C-18 reads or produces one. goopg has no analogue,
  but both halves of the primitive exist — `joinkeyproof.go:56
  uniqueKeyColumnSets` is `rel_is_distinct_for` for the base-relation case,
  and `pathindexonlyneed.go`'s needed/output name sets answer
  "unused above", both decline-biased.
  *after C-04, beside C-09. gate: P3 PP + a forced fixture + byte-identical
  values. ~200–350 LOC, low-medium.*
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
- [~] **C-10d P4-00d FROM-subquery pull-up — MEASUREMENT DONE 2026-09-05;
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
- [ ] **C-11 P4-02 upper `RelOptInfo`s** (`GROUP_AGG`, `WINDOW`,
  `DISTINCT`, `ORDERED`, `FINAL`) with pathlists.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-12 P4-03 real upper-rel `PathSort`** (has a `createPlanNode` arm;
  today only ever a merge-join child, never competing with a hashed
  alternative).
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-13 P4-04 bounded / top-N sort** (`cost_sort`'s `limit_tuples`
  arm) — the largest recorded `ORDER BY … LIMIT` win.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP + timing).*
- [ ] **C-14 P4-05 Incremental Sort** node + `create_incremental_sort_path`.
  No executor counterpart exists — BLOCKED: resume after executor support
  and excluded from closure until then. Publish the executor input
  contract first (take3 13 §8.7 tiebreaker), do not absorb silently.
  *design: take3 08 §7; gate when unblocked: take3 09 §5 P4 (PP).*
- [ ] **C-15 P4-06 `create_grouping_paths`** (sorted + hashed agg priced by
  `cost_agg` incl. hash spill arm); retire the three aggregate rules.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP + timing).*
- [ ] **C-16 P4-07 `create_distinct_paths`** (hashed / sorted /
  unique-over-sorted). Depends on landed P1-25.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-17 P4-08 `tuple_fraction` end-to-end** (every upper rel, not only
  the join search).
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-18 P4-09 `create_window_paths`** + set-operation paths, priced.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
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
- [ ] **C-19c P5-03 parallel eligibility for plain index scans.** Extend
  `drivingScan` (SeqScan/BitmapHeapScan/IndexOnlyScan + wrappers today;
  plain IndexScan still missing) so PG's Parallel Index Scan has a
  counterpart. Closes a `MISSING-NODE` entry.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP).*
- [ ] **C-19d P5-04 `generate_useful_gather_paths`** producing `PathGather`
  and `PathGatherMerge` priced by `cost_gather`/`cost_gather_merge`, with
  `createPlanNode` arms.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP, parallel-on + serial
  control).*
- [ ] **C-19e P5-05 re-decide Gather Merge → Sort → Parallel scan by
  cost** rather than `sortPartialRootPays`' hard-coded decline. If goopg's
  costs still choose leader-side sorting, record it as a permitted
  divergence with the committed measurement (take3 09 §4.4 case 1).
  *design: take3 08 §8; gate: take3 09 §5 P5 (timing both shapes).*
- [ ] **C-19f P5-06 parallel hash join as a `parallel_aware` hash path,
  priced.** Executor consumer check required (a fixture where the path
  wins must execute as parallel hash).
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP).*
- [ ] **C-19g P5-07 partial aggregation as paths**
  (`create_partial_grouping_paths`), replacing `splitAggregate`. Depends
  on C-15.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP + values-diff).*
- [ ] **C-19h P5-08 retire `MaybeAddGather`.**
  *design: take3 08 §8; gate: take3 09 §5 P5 — plan-parity both suites,
  parallel and serial arms.*
  (Serial control arm unchanged throughout C-19a–h. Ordering trap already
  measured: at small budgets the plan moves onto index-driven joins the
  old post-pass cannot drive — take2 07 §3.2.)
- [ ] **C-20a P6-01 one cardinality estimator.** Delete legacy
  `estimateJoin`/`EstimateRows` + the `joinkeyproof.go` mirror;
  everything reads `calcJoinrelSize`. Prerequisite: EXPLAIN `rows=` from
  the path (P0-02 remainder) + legacy consumers gone (C-11…C-18).
  *design: take3 08 §9; gate: take3 09 §5 P6 (PP + EA ratchet).*
- [ ] **C-20b P6-02 `PathTarget` + range table.** Replace
  `baseLeaf`/`baseOffset`; delete `joinlayout.go` remapping + the
  `createplanroot.go` boundary assertions. Deletes the largest silent
  wrong-answer class — value-level `tpch-runner -diff`, never counts.
  *design: take3 08 §9; gate: value-level `-diff`, never counts.*
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
- [ ] **C-20f P6-06d retire `GOOPG_NLI_COSTGATE`**, regenerating
  `planner-flags.env`. Only once the search selects NLI on its own
  merits (remaining `btcostestimate` + hash terms, per P3-11). Own commit
  with before/after parity roll-up + timing table.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [ ] **C-20g P6-06e retire `GOOPG_PGSHAPED_DP` last**, regenerating
  `planner-flags.env`. The off path is unretirable while the legacy
  rewrites are load-bearing (P6-03/P6-04 must-not-delete). Own commit
  with before/after parity roll-up + timing table.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [ ] **C-20h P6-07 `setrefs` phase + P6-08 `RestrictInfo` caching.**
  `setrefs` only if C-20b shows the executor still needs explicit column
  resolution; caching is planning-speed, not plan-quality.
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
- [!] **E-03 EX3-07 presorted-prefix implementation — file ONLY if
  planner C-14 activates.** E-15 already published the contract, so this
  item is purely conditional. Do not absorb silently.
- [ ] **E-14 EX1 build-half redesign (no second truncation).** The EX1-04
  unblock review proved owned-payload shortening unsafe without
  projection; P4-01 landed projection at the build-side `Project` site,
  so the hash-build half is unblocked-but-needs-redesign (Cut 0 alloc arm
  measured: Q9 20.14→13.88 s, alloc 9.43→8.52 GB, values identical).
  Implement narrowed-width retention on the Project shape without a
  second `[0,bound)` truncation. Unblocks D-05 geometry pricing.
  *design: `docs/design/executor-ex1-04-owned/DESIGN.md` + unblock review
  (`134324df6`); gate: poison tests on the Project shape (Cut 1 pattern)
  + alloc arm + values + pin.*
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
- [ ] **E-07 EX5-01 slab parity for `Gather` — RE-SCOPED 2026-09-05, two
  of its three justifications are now void.** Premise re-verified: workers
  really do take the legacy path (`BuildWorker` → `buildNode`,
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
- [ ] **E-09 EX5-02 shared build hardening — SPLIT 2026-09-06** into the
  three slices below after a static feasibility analysis of the spilling
  case (ledger `take3-D-04-private-worker-builds`: on Q9 all five Gather
  participants build the whole 1.5 M-row `orders` table privately, a 5×
  multiplier that dwarfs everything the minimize-datum bundle proposes on
  the same query). Design:
  `docs/design/executor-e09a-shared-spilling-build/DESIGN.md`.
- [ ] **E-09a Publish a SPILLING shared build (Variant A, private
  reload).** `captureSharedBuild` carries an immutable batch descriptor
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
- [ ] **E-09b Load-once-per-batch (Variant B).** `sync.Once` per batch +
  refcount + `ctx.Done()`-aware wait on the shared descriptor — PG's
  `PHJ_BATCH_LOAD`/`FREE` analogue, and what removes the 5× MEMORY
  multiplier (D-04's 506 MB live map). The first real cross-worker wait in
  the executor; cancellation hazard under LIMIT-above-Gather. On top of A.
  *gate: A's gate + a mandatory cancel-mid-batch test; ~150–200 LOC.*
- [ ] **E-09c Cooperative-stall measurement under skew + worker-count
  scaling on Q9-class shapes** — the original E-09 text, now third.
  *design: take3 13 §7; gate: scaling + skew A/B; Datum-safety tests;
  serial arm.*
- [ ] **E-10 EX5-03 Gather/GatherMerge ordering + exchange.**
  Worker-sorted slices, leader heap merge. Cost-decision stays with
  planner (permitted-divergence candidate).
  *design: take3 13 §7; gate: ordering tests; pin; serial arm.*
- [ ] **E-11 EX5-04 AIO `ReadStream` decision.** Measurement item with two
  legal outcomes: wire with depth policy + timing/alloc gate, or
  ledger-decline (pool hints + workers suffice). Not a commitment.
  *design: take3 13 §7; gate: A/B or ledger row.*
- [!] **E-12 EX3-02 Cut 3 (oversize + teardown) — BLOCKED on E-14.**
  (Record correction 2026-09-04: Cut 2 already LANDED — `68ccd68c3`,
  unit headers 2.002→0.005, TPC-H 24/24 + PP 22/22 + TPC-DS PASS=95;
  only Cut 3 remains here.) Queued behind landed Cut 0/1/2; arena sizing is
  batching geometry over the redesigned build rows.
  *design: `docs/design/executor-ex3-02-dense-build/DESIGN.md`; gate:
  poison tests + gate suite + values + pin.*
- [ ] **E-13 EX1-04 Cut 2 (owned-row tightening on Project-declined
  paths).** Only if a later alloc arm shows a residual. Cut 1 (poison
  tests on the Project shape) landed.
  *gate: alloc arm residual demonstrated first.*
- [!] **E-16 EX3-03 step-2 resume — BLOCKED on spill-cost calibration.**
  Session `work_mem` threading is implemented and unit-green but moves
  Q7/Q9 plans to slower merge shapes at bench `work_mem` (model prices
  hash above merge while forced-hash proves faster). Resume with the
  filed artifact
  (`analysis/planner-refactor-take3/ex303-step2-deferred-20260904/`,
  README + clean-applying `plumbing.patch`, ledger
  `take3-EX3-03-step2-blocked`): recalibrate the spill-cost model first,
  then re-apply the plumbing and re-gate.
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
| C-08 | 2026-09-06 | b967f38 | per-joinrel param_source_rels derivation with frame remap; NLI + merge arms threaded; star-schema escape untouched | derivation table + admit/refuse arm pair; suites + units scope + spotcheck PASS; PP TPC-H 22/22 MATCH (provably inert until C-04, no sweep triggered); review APPROVE-WITH-NITS (5 addressed); ppi_rows ledger row |

## Dropped

| item | date | reason | ledger row |
|---|---|---|---|
| E-08 (EX4-04) | 2026-09-05 | dropped by dependency: it is parallel filterOp compilation, and the serial twin E-04 failed its own measurement | `take3-E-08-dropped` |
| F-03 | 2026-09-05 | rule 3: `Buf` is the arena-detach target; removing it leaves only unbounded retention. Prior attempt returned 0 rows on 7 queries. Win dominated by D-05 on the same sites | `take3-F-03-dropped` |
| E-04 (EX4-01) | 2026-09-05 | no measured gain (predicted ~0.33 pp is below the noise floor; measured a consistent Q18 +8.5%); mechanism overlaps the scan prefilter | `take3-E-04-dropped` |
| C-09 (P3-08) | 2026-09-06 | unique-inner SEMI has no structural value (pinned→no reorder; estimates already unique-aware; exec early-break-bounded); plan-rewrite re-indexing risk exceeds any bounded gain | `take3-C-09-declined` |

(End of file)
